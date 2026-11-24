package jellyfinbridge

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
	"novastream/services/playback"
)

// Presets the admin offers in version pickers appear as "Preset · name"
// versions and play like Auto, ranked by that preset.
func TestPresetVersionsAreOfferedAndPlay(t *testing.T) {
	// Release versions off: preset versions are offered on their own.
	h := newHarness(t, config.JellyfinBridgeSettings{ReleaseVersions: 0})
	installFakePlayback(t, h)
	h.srv.deps.Handlers.QualityPresets = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"offered": []map[string]any{
			{"id": "p-german", "name": "German HD", "mediaType": "movie"},
			{"id": "p-anime", "name": "Anime", "mediaType": "series"}, // not for movies
			{"id": "simple", "name": "Simple ranking"},                // both media types
		}})
	}
	var mu sync.Mutex
	var prequeued []string
	h.srv.deps.Handlers.Prequeue = func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req playback.PrequeueRequest
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		prequeued = append(prequeued, req.QualityPreset)
		mu.Unlock()
		writeJSON(w, http.StatusOK, playback.PrequeueResponse{PrequeueID: "pq-" + req.QualityPreset, Status: playback.PrequeueStatusQueued})
	}

	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)
	got := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+item, tok, nil))
	var names []string
	for _, ms := range got.MediaSources {
		names = append(names, ms.Name)
	}
	if len(got.MediaSources) != 3 || got.MediaSources[0].ID != item ||
		got.MediaSources[1].Name != "Preset · German HD" || got.MediaSources[2].Name != "Preset · Simple ranking" {
		t.Fatalf("versions = %q", names)
	}
	german := got.MediaSources[1]
	if german.ID == item || german.ID == got.MediaSources[2].ID {
		t.Fatal("each preset version needs its own ID")
	}
	// Clients load the selected version as an item.
	if v := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+german.ID, tok, nil)); v.ID != german.ID {
		t.Fatalf("version item = %s", v.ID)
	}

	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+german.ID+"/PlaybackInfo", tok, map[string]any{"MediaSourceId": german.ID}))
	if len(info.MediaSources) != 1 || info.MediaSources[0].ID != german.ID {
		t.Fatalf("preset playback info = %+v", info)
	}
	auto := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, map[string]any{"MediaSourceId": item}))
	if len(auto.MediaSources) != 1 || auto.MediaSources[0].ID != item || auto.PlaySessionID == info.PlaySessionID {
		t.Fatalf("auto must be its own session: %+v", auto)
	}
	// Asking again for the preset version reuses its session.
	again := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+german.ID+"/PlaybackInfo", tok, map[string]any{"MediaSourceId": german.ID}))
	if again.PlaySessionID == auto.PlaySessionID {
		t.Fatal("the preset version must not reuse the Auto session")
	}

	mu.Lock()
	defer mu.Unlock()
	sawGerman, sawDefault := false, false
	for _, p := range prequeued {
		sawGerman = sawGerman || p == "p-german"
		sawDefault = sawDefault || p == ""
	}
	if !sawGerman || !sawDefault {
		t.Fatalf("prequeue presets = %q, want the picked preset and the profile's own", prequeued)
	}
}

// Without offered presets the version list is unchanged.
func TestNoPresetVersionsWhenNoneOffered(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{ReleaseVersions: 0})
	installFakePlayback(t, h)
	h.srv.deps.Handlers.QualityPresets = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"offered": []map[string]any{}})
	}
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)
	if got := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+item, tok, nil)); len(got.MediaSources) != 1 {
		t.Fatalf("versions = %d, want only the item's own", len(got.MediaSources))
	}
}

// A direct player (Plezy, mpv) may request the stream while a slow version
// is still being picked; the request waits for it instead of failing with 503.
func TestDirectStreamWaitsForSlowVersion(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{PlaybackInfoWaitSeconds: 1})
	installFakePlayback(t, h)
	h.srv.deps.Handlers.QualityPresets = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"offered": []map[string]any{{"id": "p-german", "name": "German HD", "mediaType": "movie"}}})
	}
	// The stream becomes ready 2.5s after PlaybackInfo is asked (set below),
	// so login and loading the item may take as long as they need.
	var readyMu sync.Mutex
	readyAt := time.Now().Add(time.Hour)
	ready := func() time.Time { readyMu.Lock(); defer readyMu.Unlock(); return readyAt }
	h.srv.deps.Handlers.PrequeueStatus = func(w http.ResponseWriter, r *http.Request) {
		if time.Now().Before(ready()) {
			writeJSON(w, http.StatusOK, playback.PrequeueStatusResponse{PrequeueID: "pq1", Status: playback.PrequeueStatusResolving})
			return
		}
		writeJSON(w, http.StatusOK, playback.PrequeueStatusResponse{PrequeueID: "pq1", Status: playback.PrequeueStatusReady, StreamPath: "/webdav/movie.mkv", Duration: 120})
	}
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)
	got := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+item, tok, nil))
	if len(got.MediaSources) != 2 {
		t.Fatalf("versions = %d", len(got.MediaSources))
	}
	german := got.MediaSources[1].ID

	// PlaybackInfo gives up waiting after a second; the stream is not ready.
	readyMu.Lock()
	readyAt = time.Now().Add(2500 * time.Millisecond)
	readyMu.Unlock()
	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+german+"/PlaybackInfo", tok, map[string]any{"MediaSourceId": german}))
	if len(info.MediaSources) != 1 || time.Now().After(ready()) {
		t.Fatalf("playback info = %+v (test timing off)", info)
	}
	q := url.Values{"Static": {"true"}, "api_key": {tok}, "MediaSourceId": {german}}
	start := time.Now()
	rec := h.do(http.MethodGet, "/Videos/"+item+"/stream?"+q.Encode(), "", nil)
	if rec.Code >= 300 {
		t.Fatalf("stream = %d %s, want it to wait for the version", rec.Code, rec.Body.String())
	}
	if time.Since(start) < 500*time.Millisecond {
		t.Fatal("the stream request should have waited for resolution")
	}
}

// The bridge asks mediastorm per item, so anime presets appear only where
// mediastorm says they fit, and Auto is named after the preset it uses.
func TestPresetVersionsFollowTheItem(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{ReleaseVersions: 0})
	installFakePlayback(t, h)
	var asked url.Values
	h.srv.deps.Handlers.QualityPresets = func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query()
		writeJSON(w, http.StatusOK, map[string]any{
			"offered": []map[string]any{{"id": "p-anime", "name": "Anime HD", "mediaType": "movie"}},
			"movie":   map[string]any{"presetId": "p-anime", "name": "Anime HD", "source": "global-anime"},
		})
	}
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)
	got := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+item, tok, nil))
	if asked.Get("mediaType") != "movie" || asked.Get("titleId") == "" || asked.Get("name") == "" {
		t.Fatalf("the presets request must name the title: %v", asked)
	}
	if len(got.MediaSources) != 2 || got.MediaSources[0].Name != "Auto · Anime HD" || got.MediaSources[1].Name != "Preset · Anime HD" {
		var names []string
		for _, ms := range got.MediaSources {
			names = append(names, ms.Name)
		}
		t.Fatalf("versions = %q, want Auto named after its preset plus the offered preset", names)
	}
}

// Without any quality preset items skip the preset lookup and Auto keeps
// its original name.
func TestNoPresetsSkipLookupAndKeepAutoName(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{ReleaseVersions: 1})
	installFakePlayback(t, h)
	calls := 0
	h.srv.deps.Handlers.QualityPresets = func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeJSON(w, http.StatusOK, map[string]any{"offered": []map[string]any{}})
	}
	h.srv.deps.QualityPresetsInUse = func() bool { return false }
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)
	got := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+item, tok, nil))
	if calls != 0 {
		t.Errorf("preset lookups without presets: %d", calls)
	}
	if len(got.MediaSources) == 0 {
		t.Fatal("no media sources")
	}
	if label := h.srv.autoVersionLabel(t.Context(), nil, item, itemRef{Kind: refMovie}); label != "Auto (mediastorm)" {
		t.Errorf("auto version = %q", label)
	}
}

// A preset list answered without the title's metadata (a lookup ran out of
// time) is asked for again soon, not kept for the full TTL.
func TestPresetChoicesWithoutMetadataRetrySoon(t *testing.T) {
	for reason, wantShort := range map[string]bool{"no metadata": true, "genre Anime": false} {
		h := newHarness(t, config.JellyfinBridgeSettings{ReleaseVersions: 0})
		installFakePlayback(t, h)
		h.srv.deps.Handlers.QualityPresets = func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"offered": []map[string]any{{"id": "p-german", "name": "German HD", "mediaType": "movie"}}, "animeReason": reason})
		}
		tok := h.login("Living Room", "alice-pass")
		h.do(http.MethodGet, "/Items/"+movieID(t, h, tok), tok, nil)
		h.srv.presetChoices.mu.Lock()
		var kept []time.Duration
		for _, cached := range h.srv.presetChoices.items {
			kept = append(kept, presetChoicesTTL-time.Since(cached.fetched))
		}
		h.srv.presetChoices.mu.Unlock()
		if len(kept) == 0 {
			t.Fatalf("%s: nothing cached", reason)
		}
		for _, d := range kept {
			if short := d <= presetChoicesRetry; short != wantShort {
				t.Errorf("%s: kept for %s", reason, d.Round(time.Second))
			}
		}
	}
}

// A preset version the device was offered plays with its preset even when
// the preset lookup fails by the time it is played.
func TestOfferedPresetVersionPlaysWithoutLookup(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{ReleaseVersions: 0})
	installFakePlayback(t, h)
	var mu sync.Mutex
	failing := false
	h.srv.deps.Handlers.QualityPresets = func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if failing {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"offered": []map[string]any{{"id": "p-german", "name": "German HD", "mediaType": "movie"}}})
	}
	var prequeued []string
	h.srv.deps.Handlers.Prequeue = func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req playback.PrequeueRequest
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		prequeued = append(prequeued, req.QualityPreset)
		mu.Unlock()
		writeJSON(w, http.StatusOK, playback.PrequeueResponse{PrequeueID: "pq-" + req.QualityPreset, Status: playback.PrequeueStatusQueued})
	}
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)
	got := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+item, tok, nil))
	if len(got.MediaSources) != 2 {
		t.Fatalf("versions = %d", len(got.MediaSources))
	}
	german := got.MediaSources[1].ID

	// The cached choices expired and mediastorm does not answer now.
	h.srv.presetChoices.mu.Lock()
	h.srv.presetChoices.items = nil
	h.srv.presetChoices.mu.Unlock()
	mu.Lock()
	failing = true
	mu.Unlock()

	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+german+"/PlaybackInfo", tok, map[string]any{"MediaSourceId": german}))
	if len(info.MediaSources) != 1 || info.MediaSources[0].ID != german {
		t.Fatalf("playback info = %+v, want the preset version", info)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(prequeued) == 0 || prequeued[len(prequeued)-1] != "p-german" {
		t.Errorf("prequeue presets = %q, want the picked preset", prequeued)
	}
}
