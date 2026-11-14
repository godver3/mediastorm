package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"novastream/config"
	"novastream/internal/auth"
	"novastream/models"
	user_settings "novastream/services/user_settings"
	"novastream/services/users"
)

func withAccount(r *http.Request, accountID string, master bool) *http.Request {
	ctx := context.WithValue(r.Context(), auth.ContextKeyAccountID, accountID)
	ctx = context.WithValue(ctx, auth.ContextKeyIsMaster, master)
	return r.WithContext(ctx)
}

// A household naming another household's profile (here the master
// household's) must not get that profile's channels or provider URLs.
func TestLiveChannelsRejectAnotherHouseholdsProfile(t *testing.T) {
	var fetched int
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched++
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:-1 tvg-id=\"news\",News\nhttps://media.example/user/secret/news.ts\n"))
	}))
	defer provider.Close()
	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	master := []string{models.MasterAccountID}
	if err := manager.Save(config.Settings{Live: config.LiveSettings{Sources: []config.LivePlaylistSource{
		{ID: "main", Mode: "m3u", PlaylistURL: provider.URL + "/list.m3u", AllowedAccounts: &master},
	}}}); err != nil {
		t.Fatal(err)
	}
	h := NewLiveHandler(provider.Client(), false, "", 24, 0, 0, false, manager, nil)
	h.SetUsersService(liveAccountUsers{
		"default": {ID: "default", AccountID: models.MasterAccountID},
		"guest":   {ID: "guest", AccountID: "other"},
	})

	foreign := withAccount(httptest.NewRequest(http.MethodGet, "/api/live/channels?profileId=default", nil), "other", false)
	if channels, _ := h.FetchFilteredChannelsForRequest(foreign); len(channels) != 0 || fetched != 0 {
		t.Fatalf("another household read the master catalog: %d channels, %d fetches", len(channels), fetched)
	}
	w := httptest.NewRecorder()
	h.GetChannels(w, foreign)
	if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("GetChannels for a foreign profile: %d %s", w.Code, w.Body.String())
	}

	own := withAccount(httptest.NewRequest(http.MethodGet, "/api/live/channels?profileId=default", nil), models.MasterAccountID, true)
	if channels, err := h.FetchFilteredChannelsForRequest(own); err != nil || len(channels) != 1 {
		t.Fatalf("master's own profile: %d channels, %v", len(channels), err)
	}
}

// In account-managed mode a non-master client that reads its profile settings
// and saves them back (the Expo app does this when favouriting a channel) must
// keep the profile's provider copy and its other Live TV preferences.
func TestAccountSettingsRoundTripKeepsHiddenLiveSettings(t *testing.T) {
	dir := t.TempDir()
	manager := config.NewManager(filepath.Join(dir, "settings.json"))
	household := []string{"household"}
	if err := manager.Save(config.Settings{Live: config.LiveSettings{Sources: []config.LivePlaylistSource{
		{ID: "family", Mode: "m3u", PlaylistURL: "https://provider.example/family.m3u", AllowedAccounts: &household},
	}}}); err != nil {
		t.Fatal(err)
	}
	profiles, err := users.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := profiles.CreateForAccount("household", "Fixture")
	if err != nil {
		t.Fatal(err)
	}
	preferences, err := user_settings.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	offset, maxStreams, xmltv := 60, 2, "https://guide.example/secret.xml"
	stored := models.UserSettings{LiveTV: models.LiveTVSettings{
		FavoriteChannels: []string{"one"},
		Sources:          []models.LivePlaylistSource{{ID: "own", Mode: "m3u", PlaylistURL: "https://own.example/secret.m3u"}},
		MaxStreams:       &maxStreams,
		EPG:              &models.EPGOverrides{TimeOffsetMinutes: &offset, XmltvUrl: &xmltv},
	}}
	if err := preferences.Update(profile.ID, stored); err != nil {
		t.Fatal(err)
	}
	api := NewUserSettingsHandler(preferences, profiles, manager)

	r := withAccount(httptest.NewRequest(http.MethodGet, "/api/users/"+profile.ID+"/settings", nil), "household", false)
	r = mux.SetURLVars(r, map[string]string{"userID": profile.ID})
	w := httptest.NewRecorder()
	api.GetSettings(w, r)
	if strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("GET disclosed the profile's provider connection: %s", w.Body.String())
	}
	var read models.UserSettings
	if err := json.Unmarshal(w.Body.Bytes(), &read); err != nil {
		t.Fatal(err)
	}
	read.LiveTV.FavoriteChannels = append(read.LiveTV.FavoriteChannels, "two")
	body, _ := json.Marshal(read)
	r = withAccount(httptest.NewRequest(http.MethodPut, "/api/users/"+profile.ID+"/settings", strings.NewReader(string(body))), "household", false)
	r = mux.SetURLVars(r, map[string]string{"userID": profile.ID})
	w = httptest.NewRecorder()
	api.PutSettings(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}

	saved, err := preferences.Get(profile.ID)
	if err != nil || saved == nil {
		t.Fatalf("Get: %v", err)
	}
	live := saved.LiveTV
	if len(live.FavoriteChannels) != 2 {
		t.Fatalf("favourites not saved: %v", live.FavoriteChannels)
	}
	if len(live.Sources) != 1 || live.Sources[0].ID != "own" {
		t.Fatalf("provider copy erased: %+v", live.Sources)
	}
	if live.MaxStreams == nil || *live.MaxStreams != 2 || live.EPG == nil || live.EPG.TimeOffsetMinutes == nil || *live.EPG.TimeOffsetMinutes != 60 {
		t.Fatalf("Live TV preferences erased: %+v", live)
	}
	if live.EPG.XmltvUrl == nil || *live.EPG.XmltvUrl != xmltv {
		t.Fatal("guide URL erased")
	}
}

// Without account rules, a profile copy of a provider withheld from the
// profile is hidden from non-master readers and kept on their saves.
func TestUnmanagedSettingsRoundTripKeepsWithheldSourceCopies(t *testing.T) {
	adultsOnly := []string{"adult"}
	global := []config.LivePlaylistSource{{ID: "adults", Mode: "m3u", PlaylistURL: "https://provider.example/adults.m3u", AllowedProfiles: adultsOnly}}
	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := manager.Save(config.Settings{Live: config.LiveSettings{Sources: global}}); err != nil {
		t.Fatal(err)
	}
	copyOfWithheld := models.LivePlaylistSource{ID: "adults", Mode: "m3u", PlaylistURL: "https://provider.example/adults.m3u"}
	stored := &models.UserSettings{LiveTV: models.LiveTVSettings{Sources: []models.LivePlaylistSource{copyOfWithheld}}}

	read := *stored
	scopeUserLiveSettings(&read, manager, "child", "household", false)
	if len(read.LiveTV.Sources) != 0 {
		t.Fatal("withheld copy was shown")
	}
	restoreScopedLiveSettings(&read, stored, manager, "child", "household", false)
	if len(read.LiveTV.Sources) != 1 || read.LiveTV.Sources[0].ID != "adults" {
		t.Fatalf("withheld copy erased on save: %+v", read.LiveTV.Sources)
	}

	// The master account reads and writes the stored document unchanged.
	masterRead := *stored
	scopeUserLiveSettings(&masterRead, manager, "child", models.MasterAccountID, true)
	if len(masterRead.LiveTV.Sources) != 1 {
		t.Fatal("master read was scoped")
	}
}

// Catch-up URLs never equal a channel URL; in account-managed mode playback
// accepts exactly the ones GetCatchup issued to this profile.
func TestAccountLivePlaybackAcceptsIssuedCatchupURL(t *testing.T) {
	settings := config.Settings{Live: config.LiveSettings{RequireAccountAssignment: true}}
	h := &LiveHandler{}
	catalog := issuingCatalog{LiveHandler: h, channels: []LiveChannel{{ID: "news", URL: "https://8.8.8.8/live/user/pass/1.ts"}}}
	archive := "https://8.8.8.8/timeshift/user/pass/60/2026-11-13:10-00/1.ts"
	h.catchupGrants.issue("child", archive, time.Now())

	authorize := func(profile, raw string) error {
		r := httptest.NewRequest(http.MethodGet, "/api/live/stream?profileId="+profile, nil)
		_, err := authorizeLiveStreamURL(r, raw, staticSecurityConfigProvider{settings}, catalog)
		return err
	}
	if err := authorize("child", archive); err != nil {
		t.Fatalf("issued catch-up URL rejected: %v", err)
	}
	if err := authorize("other", archive); err == nil {
		t.Fatal("catch-up URL accepted for a profile it was not issued to")
	}
	if err := authorize("child", "https://8.8.8.8/timeshift/user/pass/60/2026-11-13:11-00/2.ts"); err == nil {
		t.Fatal("unissued archive URL on the same provider accepted")
	}

	// The master account without a profile keeps global access.
	r := withAccount(httptest.NewRequest(http.MethodGet, "/api/live/stream", nil), models.MasterAccountID, true)
	if _, err := authorizeLiveStreamURL(r, "https://8.8.8.8/live/user/pass/9.ts", staticSecurityConfigProvider{settings}, catalog); err != nil {
		t.Fatalf("master without profile rejected: %v", err)
	}
}

type issuingCatalog struct {
	*LiveHandler
	channels []LiveChannel
}

func (c issuingCatalog) FetchFilteredChannelsForRequest(*http.Request) ([]LiveChannel, error) {
	return c.channels, nil
}

// Stalker providers must appear in the account-visible catalog that playback,
// the guide and recordings check against.
func TestFilteredChannelsIncludeStalkerSources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("action") {
		case "handshake":
			fmt.Fprint(w, `{"js":{"token":"token-1"}}`)
		case "get_profile":
			fmt.Fprint(w, `{"js":{"id":"1"}}`)
		case "get_genres":
			fmt.Fprint(w, `{"js":[{"id":"7","title":"News"}]}`)
		case "get_all_channels":
			fmt.Fprint(w, `{"js":{"data":[{"id":"12","name":"World News","number":"1","tv_genre_id":"7","cmd":"ffmpeg http://localhost/ch/12","xmltv_id":"news.world"}]}}`)
		default:
			http.Error(w, "unexpected action", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	// Portal sessions live in a package-wide store; drop the one this test adds
	// so tests that count cached sessions are unaffected.
	stalkerSessionStore.Lock()
	existing := make(map[string]bool, len(stalkerSessionStore.sessions))
	for key := range stalkerSessionStore.sessions {
		existing[key] = true
	}
	stalkerSessionStore.Unlock()
	t.Cleanup(func() {
		stalkerSessionStore.Lock()
		defer stalkerSessionStore.Unlock()
		for key := range stalkerSessionStore.sessions {
			if !existing[key] {
				delete(stalkerSessionStore.sessions, key)
			}
		}
	})
	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	household := []string{"household"}
	if err := manager.Save(config.Settings{Live: config.LiveSettings{Sources: []config.LivePlaylistSource{{
		ID: "portal", Mode: "stalker", Enabled: models.BoolPtr(true), AllowedAccounts: &household,
		StalkerPortalURL: server.URL + "/stalker_portal/c/", StalkerMAC: "00:1a:79:00:00:01",
	}}}}); err != nil {
		t.Fatal(err)
	}
	h := NewLiveHandler(server.Client(), false, "", 24, 0, 0, false, manager, nil)
	h.SetUsersService(liveAccountUsers{"child": {ID: "child", AccountID: "household"}})
	channels, err := h.FetchFilteredChannelsForRequest(httptest.NewRequest(http.MethodGet, "/api/live/channels?profileId=child", nil))
	if err != nil || len(channels) != 1 {
		t.Fatalf("stalker channels: %d, %v", len(channels), err)
	}
}
