package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/mux"
	"novastream/config"
	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/epg"
	user_settings "novastream/services/user_settings"
	"novastream/services/users"
)

type liveAccountUsers map[string]models.User

func (u liveAccountUsers) Get(id string) (models.User, bool) { p, ok := u[id]; return p, ok }

func TestLiveAccountAllProfilesAndCopiedSourceCannotExpandRights(t *testing.T) {
	accounts := []string{"household"}
	onlyAdult := []string{"adult"}
	settings := config.Settings{Live: config.LiveSettings{RequireAccountAssignment: true, Sources: []config.LivePlaylistSource{
		{ID: "family", Mode: "m3u", PlaylistURL: "http://provider.example/family.m3u", AllowedAccounts: &accounts},
		{ID: "adults", Mode: "m3u", PlaylistURL: "http://provider.example/adults.m3u", AllowedAccounts: &accounts, AllowedProfiles: onlyAdult},
	}}}
	users := liveAccountUsers{"adult": {ID: "adult", AccountID: "household"}, "child": {ID: "child", AccountID: "household"}, "bernd": {ID: "bernd", AccountID: "other"}}
	override := true
	h := &LiveHandler{usersSvc: users, userSettingsSvc: &mockUserSettingsProvider{settings: map[string]*models.UserSettings{
		"bernd": {LiveTV: models.LiveTVSettings{SourcesOverride: &override, Sources: []models.LivePlaylistSource{{ID: "family", Mode: "m3u", PlaylistURL: "http://provider.example/family.m3u"}}}},
		"child": {LiveTV: models.LiveTVSettings{SourcesOverride: &override, Sources: []models.LivePlaylistSource{{ID: "adults", Mode: "m3u", PlaylistURL: "http://provider.example/adults.m3u"}}}},
	}}}
	for profile, want := range map[string]int{"adult": 2, "child": 1, "bernd": 0, "unknown": 0} {
		resolved := h.resolveProfileLiveSourceForID(profile, settings)
		if got := len(resolvedLiveSources(resolved)); got != want {
			t.Fatalf("%s has %d sources, want %d", profile, got, want)
		}
	}
}

func TestAccountSettingsDoNotReturnObsoleteProviderCredentials(t *testing.T) {
	dir := t.TempDir()
	manager := config.NewManager(filepath.Join(dir, "settings.json"))
	if err := manager.Save(config.Settings{Live: config.LiveSettings{RequireAccountAssignment: true}}); err != nil {
		t.Fatal(err)
	}
	profiles, err := users.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := profiles.CreateForAccount("other-account", "Fixture")
	if err != nil {
		t.Fatal(err)
	}
	preferences, err := user_settings.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := preferences.Update(profile.ID, models.UserSettings{LiveTV: models.LiveTVSettings{
		FavoriteChannels: []string{"favorite-id"}, Sources: []models.LivePlaylistSource{{ID: "private", Mode: "m3u", PlaylistURL: "https://private.example/synthetic-private-secret"}},
	}}); err != nil {
		t.Fatal(err)
	}
	h := NewAdminUIHandler(filepath.Join(dir, "settings.json"), "", nil, profiles, preferences, manager)
	for _, raw := range []string{"true", "false"} {
		r := httptest.NewRequest(http.MethodGet, "/account/api/user-settings?userId="+profile.ID+"&raw="+raw, nil)
		r = r.WithContext(context.WithValue(r.Context(), adminSessionContextKey{}, &models.Session{AccountID: "other-account"}))
		w := httptest.NewRecorder()
		h.GetUserSettings(w, r)
		if w.Code != 200 || strings.Contains(w.Body.String(), "synthetic-private-secret") || !strings.Contains(w.Body.String(), "favorite-id") {
			t.Fatalf("account page raw=%s: %d %s", raw, w.Code, w.Body.String())
		}
	}
	api := NewUserSettingsHandler(preferences, profiles, manager)
	r := httptest.NewRequest(http.MethodGet, "/api/users/"+profile.ID+"/settings", nil)
	r = mux.SetURLVars(r, map[string]string{"userID": profile.ID})
	r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, "other-account"))
	w := httptest.NewRecorder()
	api.GetSettings(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "synthetic-private-secret") || !strings.Contains(w.Body.String(), "favorite-id") {
		t.Fatalf("API settings: %d %s", w.Code, w.Body.String())
	}
}

func TestAccountCatalogRevocationAppliesBeforeWarmPlaylistCache(t *testing.T) {
	var fetched atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched.Add(1)
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:-1 tvg-id=\"allowed.epg\",Allowed\nhttps://media.example/allowed.ts\n"))
	}))
	defer provider.Close()
	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	accounts := []string{"household"}
	settings := config.Settings{Live: config.LiveSettings{RequireAccountAssignment: true, Sources: []config.LivePlaylistSource{{ID: "family", Mode: "m3u", PlaylistURL: provider.URL + "/list.m3u", AllowedAccounts: &accounts}}}}
	if err := manager.Save(settings); err != nil {
		t.Fatal(err)
	}
	h := NewLiveHandler(provider.Client(), false, "", 24, 0, 0, false, manager, nil)
	h.SetUsersService(liveAccountUsers{"adult": {ID: "adult", AccountID: "household"}, "child": {ID: "child", AccountID: "household"}, "bernd": {ID: "bernd", AccountID: "other"}})
	get := func(profile string) []LiveChannel {
		channels, _ := h.FetchFilteredChannelsForRequest(httptest.NewRequest(http.MethodGet, "/api/live/channels?profileId="+profile, nil))
		return channels
	}
	if len(get("adult")) != 1 || len(get("child")) != 1 {
		t.Fatal("account profiles did not inherit the assigned catalog")
	}
	before := fetched.Load()
	if len(get("bernd")) != 0 || fetched.Load() != before {
		t.Fatal("foreign account fetched the provider")
	}
	nobody := []string{}
	settings.Live.Sources[0].AllowedAccounts = &nobody
	if err := manager.Save(settings); err != nil {
		t.Fatal(err)
	}
	if len(get("adult")) != 0 || fetched.Load() != before {
		t.Fatal("warm catalog bypassed revoked assignment")
	}
}

type fixedAccountChannels struct{ channels []LiveChannel }

func TestAccountLivePlaybackRejectsUnlistedPublicURL(t *testing.T) {
	settings := config.Settings{Live: config.LiveSettings{RequireAccountAssignment: true}}
	catalog := fixedAccountChannels{channels: []LiveChannel{{ID: "allowed", URL: "https://8.8.8.8/allowed.ts"}}}
	for _, tc := range []struct {
		profile, source string
		allowed         bool
	}{
		{"child", "https://8.8.8.8/allowed.ts", true},
		{"child", "https://8.8.8.8/private.ts", false},
		{"", "https://8.8.8.8/allowed.ts", false},
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/live/stream?profileId="+tc.profile, nil)
		_, err := authorizeLiveStreamURL(r, tc.source, staticSecurityConfigProvider{settings}, catalog)
		if (err == nil) != tc.allowed {
			t.Fatalf("profile=%q source=%q: %v", tc.profile, tc.source, err)
		}
	}
}

func (p fixedAccountChannels) FetchFilteredChannelsForRequest(*http.Request) ([]LiveChannel, error) {
	return p.channels, nil
}

func TestAccountGuideQueriesCannotNameInvisibleChannels(t *testing.T) {
	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := manager.Save(config.Settings{Live: config.LiveSettings{RequireAccountAssignment: true}}); err != nil {
		t.Fatal(err)
	}
	provider := fixedAccountChannels{channels: []LiveChannel{{ID: "allowed", TvgID: "allowed.epg"}}}
	r := httptest.NewRequest(http.MethodGet, "/api/live/epg/now?profileId=child", nil)
	got, err := filterEPGChannelIDs(r, manager, provider, []string{"private.epg", "allowed.epg"})
	if err != nil || len(got) != 1 || got[0] != "allowed.epg" {
		t.Fatalf("mixed query: %v %v", got, err)
	}
	if _, err := filterEPGChannelIDs(r, manager, provider, []string{"private.epg"}); err == nil {
		t.Fatal("invisible guide was readable")
	}
	r = httptest.NewRequest(http.MethodGet, "/api/live/epg/now", nil)
	if _, err := filterEPGChannelIDs(r, manager, provider, []string{"allowed.epg"}); err == nil {
		t.Fatal("unscoped account request was permitted")
	}
}

func TestAccountGuideHTTPRoutesRequireVisibleChannel(t *testing.T) {
	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := manager.Save(config.Settings{Live: config.LiveSettings{RequireAccountAssignment: true, EPG: config.EPGSettings{Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	h := NewEPGHandler(epg.NewService(t.TempDir(), manager), manager, nil)
	h.SetLiveChannelProvider(fixedAccountChannels{channels: []LiveChannel{{ID: "visible", TvgID: "visible.epg"}}})
	for _, tc := range []struct {
		target, body string
		handler      http.HandlerFunc
	}{
		{"/api/live/epg/now?profileId=child&channels=private.epg", "", h.GetNowPlaying},
		{"/api/live/epg/now?profileId=child", `{"channels":["private.epg"]}`, h.GetNowPlaying},
		{"/api/live/epg/schedule?profileId=child&channel=private.epg", "", h.GetSchedule},
		{"/api/live/epg/schedule/batch?profileId=child&channels=private.epg", "", h.GetScheduleMultiple},
		{"/api/live/epg/channel/private.epg?profileId=child", "", h.GetChannelSchedule},
	} {
		method := http.MethodGet
		if tc.body != "" {
			method = http.MethodPost
		}
		r := httptest.NewRequest(method, tc.target, strings.NewReader(tc.body))
		w := httptest.NewRecorder()
		tc.handler(w, r)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s %s: %d %s", method, tc.target, w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/live/epg/now?profileId=child&channels=visible.epg", nil)
	w := httptest.NewRecorder()
	h.GetNowPlaying(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("visible guide: %d %s", w.Code, w.Body.String())
	}
}

func TestAccountRecordingSourceIsTakenFromVisibleCatalog(t *testing.T) {
	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := manager.Save(config.Settings{Live: config.LiveSettings{RequireAccountAssignment: true}}); err != nil {
		t.Fatal(err)
	}
	h := &RecordingsHandler{cfgManager: manager, liveChannels: fixedAccountChannels{channels: []LiveChannel{{ID: "allowed", URL: "https://trusted.example/live", Name: "Allowed"}}}}
	r := httptest.NewRequest(http.MethodPost, "/api/recordings", strings.NewReader(`{"sourceUrl":"https://foreign.example/private"}`))
	payload := createRecordingPayload{ChannelID: "allowed", SourceURL: "https://foreign.example/private"}
	if !h.bindRecordingChannel(httptest.NewRecorder(), r, "child", &payload) || payload.SourceURL != "https://trusted.example/live" {
		t.Fatal("client URL overrode the authorized catalog")
	}
	payload = createRecordingPayload{ChannelID: "forbidden", SourceURL: "https://foreign.example/private"}
	w := httptest.NewRecorder()
	if h.bindRecordingChannel(w, r, "child", &payload) || w.Code != http.StatusNotFound {
		t.Fatal("unavailable channel could be recorded")
	}
}
