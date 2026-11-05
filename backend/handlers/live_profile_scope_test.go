package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"novastream/config"
	"novastream/models"
)

// A profile source list copied from the global list before the admin set
// Allowed Profiles must not keep granting the withheld source.
func TestResolveProfileLiveSourceDropsWithheldSourcesFromProfileCopy(t *testing.T) {
	global := config.Settings{Live: config.LiveSettings{Sources: []config.LivePlaylistSource{
		{Name: "Shared", Mode: "m3u", PlaylistURL: "http://shared.example/list.m3u"},
		{Name: "Kids only", Mode: "xtream", XtreamHost: "http://kids.example", XtreamUsername: "u", XtreamPassword: "p", AllowedProfiles: []string{"kid"}},
		{ID: "sports", Name: "Sports", Mode: "stremio", ManifestURL: "http://sports.example/manifest.json", AllowedProfiles: []string{"kid"}},
	}}}
	override := true
	h := &LiveHandler{userSettingsSvc: &mockUserSettingsProvider{settings: map[string]*models.UserSettings{
		"adult": {LiveTV: models.LiveTVSettings{SourcesOverride: &override, Sources: []models.LivePlaylistSource{
			{Name: "Shared", Mode: "m3u", PlaylistURL: "http://shared.example/list.m3u"},
			{Name: "Kids only", Mode: "xtream", XtreamHost: "http://kids.example", XtreamUsername: "u", XtreamPassword: "p"},
			{ID: "sports", Name: "Sports renamed", Mode: "stremio", ManifestURL: "http://other.example/manifest.json"},
			{Name: "Adult extra", Mode: "m3u", PlaylistURL: "http://extra.example/list.m3u"},
		}}},
		"kid": {LiveTV: models.LiveTVSettings{SourcesOverride: &override, Sources: []models.LivePlaylistSource{
			{Name: "Kids only", Mode: "xtream", XtreamHost: "http://kids.example", XtreamUsername: "u", XtreamPassword: "p"},
		}}},
	}}}

	got := h.resolveProfileLiveSourceForID("adult", global)
	var names []string
	for _, src := range got.Sources {
		names = append(names, src.Name)
	}
	if len(names) != 2 || names[0] != "Shared" || names[1] != "Adult extra" {
		t.Fatalf("adult sources = %v, want [Shared Adult extra]", names)
	}

	if kid := h.resolveProfileLiveSourceForID("kid", global); len(kid.Sources) != 1 {
		t.Fatalf("kid sources = %+v, want its allowed source kept", kid.Sources)
	}
}

func TestGetChannelsSelectsSourceBySettingsIndex(t *testing.T) {
	playlists := map[string]string{
		"/a.m3u": "#EXTM3U\n#EXTINF:-1,Alpha One\nhttp://stream.example/a1\n",
		"/b.m3u": "#EXTM3U\n#EXTINF:-1,Bravo One\nhttp://stream.example/b1\n",
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(playlists[r.URL.Path]))
	}))
	defer provider.Close()

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(config.Settings{Live: config.LiveSettings{Sources: []config.LivePlaylistSource{
		{Name: "A", Mode: "m3u", PlaylistURL: provider.URL + "/a.m3u", AllowedProfiles: []string{"someone"}},
		{Name: "B", Mode: "m3u", PlaylistURL: provider.URL + "/b.m3u"},
	}}}); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	h := NewLiveHandler(provider.Client(), false, "", 24, 0, 0, false, mgr, nil)

	channelNames := func(handler func(http.ResponseWriter, *http.Request), target string) (int, []string) {
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodGet, target, nil))
		var resp struct {
			Channels []LiveChannel `json:"channels"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		var names []string
		for _, ch := range resp.Channels {
			names = append(names, ch.Name)
		}
		return rec.Code, names
	}

	// Admin view without a profile sees the restricted source at its settings index.
	if code, names := channelNames(h.AdminGetChannels, "/admin/api/live/channels?sourceIndex=0"); code != http.StatusOK || len(names) != 1 || names[0] != "Alpha One" {
		t.Fatalf("admin sourceIndex=0 -> %d %v, want [Alpha One]", code, names)
	}
	if code, names := channelNames(h.AdminGetChannels, "/admin/api/live/channels?sourceIndex=1"); code != http.StatusOK || len(names) != 1 || names[0] != "Bravo One" {
		t.Fatalf("admin sourceIndex=1 -> %d %v, want [Bravo One]", code, names)
	}
	// The app endpoint still withholds the restricted source.
	if code, names := channelNames(h.GetChannels, "/api/live/channels?profileId=other"); code != http.StatusOK || len(names) != 1 || names[0] != "Bravo One" {
		t.Fatalf("profile channels -> %d %v, want only [Bravo One]", code, names)
	}
}
