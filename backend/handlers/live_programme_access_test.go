package handlers

import (
	"context"
	"encoding/json"
	"github.com/gorilla/mux"
	"net/http"
	"net/http/httptest"
	"net/url"
	"novastream/config"
	"novastream/internal/auth"
	"novastream/models"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type changingProgrammeGuide struct {
	current atomic.Pointer[models.EPGProgram]
}

func (g *changingProgrammeGuide) GetNowPlaying(ids []string, _ ...time.Duration) []models.EPGNowPlaying {
	result := make([]models.EPGNowPlaying, 0, len(ids))
	for _, id := range ids {
		result = append(result, models.EPGNowPlaying{ChannelID: id, Current: g.current.Load()})
	}
	return result
}
func (g *changingProgrammeGuide) rated(value string) {
	g.current.Store(&models.EPGProgram{ChannelID: "fixture.epg", Start: time.Now().Add(-time.Minute), Stop: time.Now().Add(time.Hour), Ratings: []models.EPGRating{{System: "FSK", Value: value}}})
}

func programmeAccessFixture(t *testing.T) (*LiveHandler, *changingProgrammeGuide) {
	t.Helper()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:-1 tvg-id=\"fixture.epg\",Family channel\nhttps://8.8.8.8/synthetic-provider-secret/stream.ts\n"))
	}))
	t.Cleanup(provider.Close)
	cfg := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	accounts := []string{"family"}
	if err := cfg.Save(config.Settings{Live: config.LiveSettings{RequireAccountAssignment: true, Sources: []config.LivePlaylistSource{{ID: "family", Mode: "m3u", PlaylistURL: provider.URL, AllowedAccounts: &accounts}}}}); err != nil {
		t.Fatal(err)
	}
	age := 6
	h := NewLiveHandler(provider.Client(), false, "", 24, 0, 0, false, cfg, nil)
	h.SetUsersService(liveAccountUsers{"child": {ID: "child", AccountID: "family", IsKidsProfile: true, KidsLiveMaxAge: &age}, "unset": {ID: "unset", AccountID: "family", IsKidsProfile: true}, "adult": {ID: "adult", AccountID: "family"}})
	guide := &changingProgrammeGuide{}
	guide.rated("6")
	h.SetEPGService(guide)
	return h, guide
}

func TestLiveProgrammeRechecksSameChannelAtProgrammeChanges(t *testing.T) {
	h, guide := programmeAccessFixture(t)
	r := httptest.NewRequest(http.MethodGet, "/api/live/hls/start?profileId=child", nil)
	const source = "https://8.8.8.8/synthetic-provider-secret/stream.ts"
	if boundary, err := h.AuthorizeLiveProgramme(r, source); err != nil || boundary.IsZero() {
		t.Fatalf("FSK 6 blocked: %v", err)
	}
	guide.rated("16")
	if _, err := h.AuthorizeLiveProgramme(r, source); err == nil {
		t.Fatal("same channel remained permitted after programme changed to FSK 16")
	}
	guide.current.Store(nil)
	if _, err := h.AuthorizeLiveProgramme(r, source); err == nil {
		t.Fatal("missing current programme permitted")
	}
	guide.rated("6")
	expired := *guide.current.Load()
	expired.Stop = time.Now().Add(-time.Second)
	guide.current.Store(&expired)
	if _, err := h.AuthorizeLiveProgramme(r, source); err == nil {
		t.Fatal("expired guide permitted")
	}
	guide.rated("6")
	unknown := *guide.current.Load()
	unknown.Ratings = nil
	unknown.Rating = "6"
	guide.current.Store(&unknown)
	if _, err := h.AuthorizeLiveProgramme(r, source); err == nil {
		t.Fatal("ambiguous numeric age permitted")
	}
	r = httptest.NewRequest(http.MethodGet, "/api/live/hls/start?profileId=adult", nil)
	if _, err := h.AuthorizeLiveProgramme(r, source); err != nil {
		t.Fatal("adult was blocked by guide availability")
	}
	r = httptest.NewRequest(http.MethodGet, "/api/live/hls/start?profileId=unset", nil)
	if _, err := h.AuthorizeLiveProgramme(r, source); err == nil {
		t.Fatal("child without explicit ceiling permitted")
	}
}

func TestLiveProgrammeCatalogUsesOpaqueReferences(t *testing.T) {
	h, _ := programmeAccessFixture(t)
	r := httptest.NewRequest(http.MethodGet, "/api/live/channels?profileId=child", nil)
	w := httptest.NewRecorder()
	h.GetChannels(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "synthetic-provider-secret") {
		t.Fatalf("provider stream disclosed: %d", w.Code)
	}
	var catalog LiveChannelsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Channels) != 1 || !isKidsLiveReference(catalog.Channels[0].URL) {
		t.Fatal("missing opaque channel reference")
	}
	video := NewVideoHandler(false, "", "")
	video.SetLiveChannelProvider(h)
	video.SetUsersService(h.usersSvc)
	request := httptest.NewRequest(http.MethodGet, "/api/live/hls/start?profileId=child", nil)
	source, ok := video.resolveKidsLiveReference(httptest.NewRecorder(), request, catalog.Channels[0].URL)
	if !ok || !strings.Contains(source, "synthetic-provider-secret") || request.URL.Query().Get("channelId") == "" {
		t.Fatal("managed tune did not resolve the authorized catalog")
	}
	if _, ok = video.resolveKidsLiveReference(httptest.NewRecorder(), request, "https://8.8.8.8/arbitrary.ts"); ok {
		t.Fatal("raw stream accepted for child")
	}
	if video.requireAllowedExternalPath(httptest.NewRecorder(), request, catalog.Channels[0].URL) {
		t.Fatal("generic proxy accepted opaque reference")
	}
}

func TestLiveProgrammeHLSChecksStoredProfileOnEveryRequest(t *testing.T) {
	h, guide := programmeAccessFixture(t)
	video := NewVideoHandler(false, "", "")
	video.SetLiveChannelProvider(h)
	video.SetUsersService(h.usersSvc)
	video.hlsManager = &HLSManager{sessions: map[string]*HLSSession{"session": {ID: "session", ProfileID: "child", IsLive: true, OriginalPath: "https://8.8.8.8/synthetic-provider-secret/stream.ts"}}}
	r := httptest.NewRequest(http.MethodGet, "/api/video/hls/session/segment.ts?profileId=adult", nil)
	if !video.requireHLSSessionAccess(httptest.NewRecorder(), r, "session") {
		t.Fatal("allowed programme rejected")
	}
	guide.rated("18")
	w := httptest.NewRecorder()
	if video.requireHLSSessionAccess(w, r, "session") || w.Code != 403 {
		t.Fatal("substituted profile or cached HLS session bypassed programme change")
	}
}

func TestLiveProgrammeBlockedBeforeStartingTranscode(t *testing.T) {
	h, guide := programmeAccessFixture(t)
	guide.rated("16")
	video := NewVideoHandlerWithProvider(true, stubBinary(t, "true"), stubBinary(t, "true"), t.TempDir(), nil)
	defer video.hlsManager.Shutdown()
	video.SetLiveChannelProvider(h)
	video.SetUsersService(h.usersSvc)
	video.SetConfigManager(h.cfgManager)
	channels, err := visibleLiveChannels(httptest.NewRequest(http.MethodGet, "/", nil), h, "child")
	if err != nil || len(channels) != 1 {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/live/hls/start?profileId=child&url="+url.QueryEscape(kidsLiveReference(channels[0].ID)), nil)
	r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, "family"))
	w := httptest.NewRecorder()
	video.StartLiveHLSSession(w, r)
	if w.Code != 403 || len(video.hlsManager.sessions) != 0 {
		t.Fatalf("blocked transcode started: %d", w.Code)
	}
}

func TestLiveProgrammeDisallowsUncontrolledChildrenPaths(t *testing.T) {
	h, _ := programmeAccessFixture(t)
	for _, handler := range []http.HandlerFunc{h.FetchPlaylist, h.StreamChannel, h.GetCatchup, h.GetStremioStreamOptions} {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodGet, "/api/live?profileId=child", nil))
		if w.Code != 403 {
			t.Fatalf("uncontrolled path status %d", w.Code)
		}
	}
}

func TestLiveProgrammeMonitorClosesPausedSessionAtBoundary(t *testing.T) {
	h, guide := programmeAccessFixture(t)
	current := *guide.current.Load()
	current.Stop = time.Now().Add(100 * time.Millisecond)
	guide.current.Store(&current)
	video := NewVideoHandler(false, "", "")
	video.SetLiveChannelProvider(h)
	video.SetUsersService(h.usersSvc)
	video.hlsManager = &HLSManager{sessions: map[string]*HLSSession{"paused": {ID: "paused", ProfileID: "child", IsLive: true, OutputDir: t.TempDir(), OriginalPath: "https://8.8.8.8/synthetic-provider-secret/stream.ts"}}}
	r := httptest.NewRequest(http.MethodGet, "/api/live/hls/start?profileId=child", nil)
	video.monitorLiveProgramme(r, "paused", "https://8.8.8.8/synthetic-provider-secret/stream.ts")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		video.hlsManager.mu.RLock()
		_, exists := video.hlsManager.sessions["paused"]
		video.hlsManager.mu.RUnlock()
		if !exists {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("paused session retained upstream work beyond the guide boundary")
}

func TestLiveProgrammePlaylistAndSegmentRoutesBlockChangedProgramme(t *testing.T) {
	h, guide := programmeAccessFixture(t)
	guide.rated("16")
	video := NewVideoHandler(false, "", "")
	video.SetLiveChannelProvider(h)
	video.SetUsersService(h.usersSvc)
	video.hlsManager = &HLSManager{sessions: map[string]*HLSSession{"session": {ID: "session", ProfileID: "child", IsLive: true, OriginalPath: "https://8.8.8.8/synthetic-provider-secret/stream.ts"}}}
	for _, handler := range []http.HandlerFunc{video.ServeHLSPlaylist, video.ServeHLSMasterPlaylist, video.ServeHLSSegment, video.ServeHLSSubtitles} {
		r := httptest.NewRequest(http.MethodGet, "/api/video/hls/session/segment.ts?profileId=adult", nil)
		r = mux.SetURLVars(r, map[string]string{"sessionID": "session", "segment": "segment_000001.ts"})
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != 403 {
			t.Fatalf("stored profile policy bypassed: %d", w.Code)
		}
	}
}

func TestLiveProgrammeIgnoresChildProviderAndGuideOverrides(t *testing.T) {
	h, _ := programmeAccessFixture(t)
	settings, err := h.cfgManager.Load()
	if err != nil {
		t.Fatal(err)
	}
	settings.Live.RequireAccountAssignment = false
	settings.Live.Sources[0].AllowedAccounts = nil
	if err = h.cfgManager.Save(settings); err != nil {
		t.Fatal(err)
	}
	override := true
	offset := 60
	h.userSettingsSvc = &mockUserSettingsProvider{settings: map[string]*models.UserSettings{"child": {LiveTV: models.LiveTVSettings{SourcesOverride: &override, Sources: []models.LivePlaylistSource{{ID: "unsafe", Mode: "m3u", PlaylistURL: "https://8.8.8.8/attacker.m3u"}}, EPG: &models.EPGOverrides{TimeOffsetMinutes: &offset}}}}}
	source := h.resolveProfileLiveSourceForID("child", settings)
	sources := resolvedLiveSources(source)
	if len(sources) != 1 || sources[0].ID != "family" || source.EPGTimeOffsetMinutes != 0 {
		t.Fatal("child replaced the trusted provider or shifted its guide")
	}
}

func TestLiveProgrammeRejectsChildRecordingJobs(t *testing.T) {
	h, _ := programmeAccessFixture(t)
	if _, err := h.AuthorizeRecording(context.Background(), models.Recording{UserID: "child", ChannelID: "fixture"}); err == nil {
		t.Fatal("child recording escaped the programme guard")
	}
}
