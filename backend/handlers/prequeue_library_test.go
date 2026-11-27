package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/playback"
)

// libraryTitleMemory is a TitlePresetMemory in memory.
type libraryTitleMemory struct {
	mu         sync.Mutex
	picks      map[string]string
	remembered []string
}

func (m *libraryTitleMemory) TitlePreset(_ context.Context, userID, clientID, titleID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.picks[userID+"|"+clientID+"|"+titleID], nil
}

func (m *libraryTitleMemory) RememberTitlePreset(_ context.Context, userID, clientID, titleID, presetID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.picks[userID+"|"+clientID+"|"+titleID] = presetID
	m.remembered = append(m.remembered, presetID)
	return true, nil
}

func (m *libraryTitleMemory) last() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.remembered...)
}

// newLibraryPrequeueHandler has a local library with episode S01E02 of the
// series and the movie.
func newLibraryPrequeueHandler(t *testing.T) (*PrequeueHandler, *config.Manager, *libraryTitleMemory, *[]LibraryCopyQuery) {
	t.Helper()
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	settings := config.DefaultSettings()
	settings.QualityPresets.AllowRequestOverride = true
	if err := mgr.Save(settings); err != nil {
		t.Fatal(err)
	}
	memory := &libraryTitleMemory{picks: map[string]string{}}
	h := &PrequeueHandler{store: playback.NewPrequeueStore(time.Hour), configManager: mgr}
	// Workers read the settings file: let them finish before the temporary
	// directory goes (cleanups run last-registered first).
	t.Cleanup(func() {
		for _, e := range h.store.ListAll() {
			waitPrequeue(t, h, e.ID)
		}
	})
	h.SetTitlePresetMemory(memory)
	var mu sync.Mutex
	asked := &[]LibraryCopyQuery{}
	h.SetLibraryCopies(func(_ *http.Request, profileID string, q LibraryCopyQuery) (*LibraryCopy, error) {
		mu.Lock()
		*asked = append(*asked, q)
		mu.Unlock()
		// The library has S01E02 (and so "any episode", episode 0).
		if q.MediaType == "series" && q.Episode != 0 && (q.Season != 1 || q.Episode != 2) {
			return nil, nil
		}
		return &LibraryCopy{ItemID: "item-1", StreamPath: "localmedia:item-1/file.mkv", Name: "file.mkv", LibraryName: "Library", SizeBytes: 1234}, nil
	})
	return h, mgr, memory, asked
}

func postPrequeue(t *testing.T, h *PrequeueHandler, req playback.PrequeueRequest) *playback.PrequeueEntry {
	t.Helper()
	body, _ := json.Marshal(req)
	rec := httptest.NewRecorder()
	h.Prequeue(rec, httptest.NewRequest(http.MethodPost, "/api/playback/prequeue", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("prequeue: %d %s", rec.Code, rec.Body.String())
	}
	var resp playback.PrequeueResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	entry, ok := h.store.Get(resp.PrequeueID)
	if !ok {
		t.Fatalf("no entry %s", resp.PrequeueID)
	}
	return entry
}

// waitPrequeue waits until an entry is ready or failed.
func waitPrequeue(t *testing.T, h *PrequeueHandler, id string) *playback.PrequeueEntry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok := h.store.Get(id); ok && (e.Status == playback.PrequeueStatusReady || e.Status == playback.PrequeueStatusFailed) {
			return e
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("entry %s never finished", id)
	return nil
}

func episodeRequest(episode int, preset string) playback.PrequeueRequest {
	return playback.PrequeueRequest{TitleID: "tvdb:series:396876", TitleName: "Shadows House", MediaType: "series", UserID: "u1", ClientID: "jellyfin-tv",
		SeasonNumber: 1, EpisodeNumber: episode, QualityPreset: preset}
}

// TestPrequeuePlaysLibraryCopy: a title (here an episode) a local library has
// plays from there, from any app (Jellyfin's Continue watching too), without
// a search; an episode the library lacks searches as before.
func TestPrequeuePlaysLibraryCopy(t *testing.T) {
	h, _, _, asked := newLibraryPrequeueHandler(t)

	req := episodeRequest(2, "")
	req.AbsoluteEpisodeNumber = 14
	entry := postPrequeue(t, h, req)
	if !strings.HasSuffix(entry.SettingsScopeKey, "|library") {
		t.Fatalf("scope %q: not the library copy", entry.SettingsScopeKey)
	}
	done := waitPrequeue(t, h, entry.ID)
	if done.Status != playback.PrequeueStatusReady || done.StreamPath != "localmedia:item-1/file.mkv" || done.FileSize != 1234 {
		t.Fatalf("library prequeue: %+v", done)
	}
	if q := (*asked)[0]; q.MediaType != "series" || q.Season != 1 || q.Episode != 2 || q.Absolute != 14 || q.TitleID != "tvdb:series:396876" {
		t.Fatalf("asked %+v", q)
	}

	// S01E03 is not in the library: the usual search.
	if other := postPrequeue(t, h, episodeRequest(3, "")); strings.HasSuffix(other.SettingsScopeKey, "|library") {
		t.Fatal("an episode the library lacks played from it")
	}
}

// TestPrequeueLibraryCopyFollowsPicks: a preset picked for the title (now or
// remembered) searches instead, and stays remembered even when it is the
// default one; picking "Library copy" plays the file and forgets it.
func TestPrequeueLibraryCopyFollowsPicks(t *testing.T) {
	h, _, memory, _ := newLibraryPrequeueHandler(t)
	h.SetDefaultQualityPreset(func(context.Context, string, string, string, string, string, int, string) string { return "simple" })

	// Picking the simple ranking (the default) for a title the library has.
	if e := postPrequeue(t, h, episodeRequest(2, "simple")); strings.HasSuffix(e.SettingsScopeKey, "|library") {
		t.Fatal("a picked preset played the library copy")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(memory.last()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := memory.last(); len(got) != 1 || got[0] != "simple" {
		t.Fatalf("remembered %v, want the default preset kept", got)
	}
	// Later playbacks of the title search too.
	if e := postPrequeue(t, h, episodeRequest(2, "")); strings.HasSuffix(e.SettingsScopeKey, "|library") {
		t.Fatal("a remembered preset played the library copy")
	}

	// Picking the library copy plays it and clears the memory.
	if e := postPrequeue(t, h, episodeRequest(2, config.QualityPresetLocal)); !strings.HasSuffix(e.SettingsScopeKey, "|library") || strings.Contains(e.SettingsScopeKey, "|preset:") {
		t.Fatalf("picked library copy: scope %q", e.SettingsScopeKey)
	}
	for len(memory.last()) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := memory.last(); len(got) != 2 || got[1] != "" {
		t.Fatalf("remembered %v, want the pick forgotten", got)
	}
	if e := postPrequeue(t, h, episodeRequest(2, "")); !strings.HasSuffix(e.SettingsScopeKey, "|library") {
		t.Fatal("after picking the library copy, Auto did not play it")
	}
}

// TestPrequeueIgnoresLibraryCopiesWhenSwitchedOff: the server setting off
// always searches.
func TestPrequeueIgnoresLibraryCopiesWhenSwitchedOff(t *testing.T) {
	h, mgr, _, asked := newLibraryPrequeueHandler(t)
	s, _ := mgr.Load()
	s.QualityPresets.IgnoreLibraryCopies = true
	if err := mgr.Save(s); err != nil {
		t.Fatal(err)
	}
	if e := postPrequeue(t, h, episodeRequest(2, "")); strings.HasSuffix(e.SettingsScopeKey, "|library") || len(*asked) != 0 {
		t.Fatalf("switched off: scope %q, %d lookups", e.SettingsScopeKey, len(*asked))
	}
}

// TestPrequeueLibraryCopyEdges: "Stream instead" streams and remembers
// nothing; a second request for the same file shares its entry; a default
// preset picked on an episode the library lacks stays remembered when other
// episodes are there; a scheduled pre-warm skips the library copy.
func TestPrequeueLibraryCopyEdges(t *testing.T) {
	h, _, memory, _ := newLibraryPrequeueHandler(t)
	h.SetDefaultQualityPreset(func(context.Context, string, string, string, string, string, int, string) string { return "simple" })

	stream := episodeRequest(2, "")
	stream.NoLibraryCopy = true
	if e := postPrequeue(t, h, stream); strings.HasSuffix(e.SettingsScopeKey, "|library") {
		t.Fatal("Stream instead played the library copy")
	}

	first := postPrequeue(t, h, episodeRequest(2, ""))
	second := postPrequeue(t, h, episodeRequest(2, ""))
	if first.ID != second.ID {
		t.Fatalf("the library copy was prepared twice: %s, %s", first.ID, second.ID)
	}

	if _, err := h.RunWorkerSyncScoped(context.Background(), "tvdb:series:396876", "Shadows House", "", "series", 0, "u1", "jellyfin-tv", "scope", &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 2}); !errors.Is(err, errPrewarmLibraryCopy) {
		t.Fatalf("pre-warm of a library episode: %v", err)
	}

	// S01E03 is not in the library, S01E02 is: the default preset picked on
	// S01E03 is a pick for the series and stays remembered, so S01E02 searches
	// with it instead of playing the file.
	postPrequeue(t, h, episodeRequest(3, "simple"))
	deadline := time.Now().Add(5 * time.Second)
	for len(memory.last()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := memory.last(); len(got) != 1 || got[0] != "simple" {
		t.Fatalf("remembered %q, want the default preset kept", got)
	}
	if e := postPrequeue(t, h, episodeRequest(2, "")); strings.HasSuffix(e.SettingsScopeKey, "|library") {
		t.Fatal("after a series pick, S01E02 played the library copy")
	}
}

// TestPrequeueRejectedPresetSearches: a preset the server rejects is still a
// pick of a stream; it does not fall into the library copy.
func TestPrequeueRejectedPresetSearches(t *testing.T) {
	h, _, _, _ := newLibraryPrequeueHandler(t)
	if e := postPrequeue(t, h, episodeRequest(2, "no-such-preset")); strings.HasSuffix(e.SettingsScopeKey, "|library") {
		t.Fatal("a rejected preset played the library copy")
	}
}

// TestPrequeueLibraryCopySkipsOtherOrderings: library files are named in the
// aired order, so an episode numbered in another one (DVD) is not taken from
// the library.
func TestPrequeueLibraryCopySkipsOtherOrderings(t *testing.T) {
	h, _, _, _ := newLibraryPrequeueHandler(t)
	dvd := episodeRequest(2, "")
	dvd.Numbering = &models.EpisodeNumbering{SeriesID: "tvdb:series:396876", Ordering: "dvd"}
	if e := postPrequeue(t, h, dvd); strings.HasSuffix(e.SettingsScopeKey, "|library") {
		t.Fatal("a DVD-ordered episode played the library copy")
	}
	aired := episodeRequest(2, "")
	aired.Numbering = &models.EpisodeNumbering{SeriesID: "tvdb:series:396876", Ordering: "official"}
	if e := postPrequeue(t, h, aired); !strings.HasSuffix(e.SettingsScopeKey, "|library") {
		t.Fatal("the default ordering did not play the library copy")
	}
}
