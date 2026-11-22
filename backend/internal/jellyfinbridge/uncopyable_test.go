package jellyfinbridge

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
	"novastream/services/playback"
)

// With video transcoding disabled, a release mediastorm cannot copy (422)
// is replaced by the next ranked release instead of failing over and over.
func TestUncopyableReleaseFallsBackToNextRelease(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{DisableVideoTranscoding: true})
	installFakePlayback(t, h)
	hd := &h.srv.deps.Handlers
	hd.PrequeueStatus = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, playback.PrequeueStatusResponse{
			PrequeueID: "pq1", Status: playback.PrequeueStatusReady, StreamPath: "/webdav/bad.mkv",
			// A prequeue names its pick in SelectedResult (DisplayName is for demo mode).
			SelectedResult: &models.NZBResult{Title: "Movie.2024.2160p.BluRay.REMUX.HEVC-BAD"}, Duration: 120, SelectedAudioTrack: 1, SelectedSubtitleTrack: -1,
		})
	}
	var mu sync.Mutex
	hd.IndexerSearch = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, []map[string]any{
			{"title": "Movie.2024.2160p.BluRay.REMUX.HEVC-BAD", "guid": "bad", "sizeBytes": int64(40) << 30, "serviceType": "debrid"},
			{"title": "Movie.2024.2160p.WEB-DL.HEVC-GOOD", "guid": "good", "sizeBytes": int64(15) << 30, "serviceType": "debrid"},
		})
	}
	var resolves int
	hd.PlaybackResolve = func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		resolves++
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"webdavPath": "/webdav/good.mkv", "fileSize": int64(15) << 30})
	}
	var started []string
	hd.HLSStart = func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		mu.Lock()
		started = append(started, path+"|"+r.URL.Query().Get("audioTrack"))
		mu.Unlock()
		if path == "/webdav/bad.mkv" {
			http.Error(w, "failed to create HLS session: transcoding disabled: no usable keyframe index", http.StatusUnprocessableEntity)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessionId": "good1", "playlistUrl": "/video/hls/x/stream.m3u8", "duration": 120.0})
	}
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)

	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, map[string]any{}))
	url := info.MediaSources[0].TranscodingURL + "&AudioStreamIndex=5"
	if rec := h.do(http.MethodGet, url, "", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("uncopyable release = %d, want 503 while the next one resolves", rec.Code)
	}
	if _, ok := h.srv.plays.get(info.PlaySessionID); !ok {
		t.Fatal("the play session was dropped instead of moving to the next release")
	}
	var rec = h.do(http.MethodGet, url, "", nil)
	for deadline := time.Now().Add(5 * time.Second); rec.Code != http.StatusFound && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
		rec = h.do(http.MethodGet, url, "", nil)
	}
	if rec.Code != http.StatusFound {
		t.Fatalf("after the swap = %d, want a redirect to the next release's playlist", rec.Code)
	}
	mu.Lock()
	last, resolved := started[len(started)-1], resolves
	mu.Unlock()
	if resolved != 1 {
		t.Fatalf("resolved %d releases, want only the next one (the refused one is skipped)", resolved)
	}
	// The client's audio index described the refused release.
	if !strings.HasPrefix(last, "/webdav/good.mkv|") || strings.HasSuffix(last, "|5") {
		t.Fatalf("last HLS start = %q, want the next release without the old audio index", last)
	}

	// A release the user picked stays that release: the failure is final.
	sess, _ := h.srv.plays.get(info.PlaySessionID)
	pinned := &playSession{ItemID: sess.ItemID, ProfileID: sess.ProfileID, ReleaseKey: "k", status: &playback.PrequeueStatusResponse{Status: playback.PrequeueStatusReady}}
	if h.srv.swapUncopyableRelease(sess.owner, pinned) {
		t.Fatal("a picked release was swapped")
	}
}

// Once no other release is left, the item stops searching for one.
func TestUncopyableReleasesExhausted(t *testing.T) {
	s := &Server{}
	s.noteUncopyable("p", "i", "A", "/a.mkv")
	s.markUncopyableExhausted("p", "i")
	if e := s.noteUncopyable("p", "i", "A", "/a.mkv"); !e.exhausted || len(e.names) != 1 || len(e.paths) != 1 {
		t.Fatalf("entry = %+v", e)
	}
	if !skipsRelease([]string{"Movie.2024-GRP"}, "movie.2024-grp") || skipsRelease([]string{"Movie.2024-GRP"}, "Movie.2024-OTHER") {
		t.Fatal("skipsRelease")
	}
}
