package debrid

import (
	"context"
	"sync"
	"testing"
	"time"
)

type janitorDeletes struct {
	mu  sync.Mutex
	ids []string
	ch  chan string
}

func newTestJanitor(t *testing.T, streaming *StreamingProvider) (*MagnetJanitor, *janitorDeletes) {
	t.Helper()
	deletes := &janitorDeletes{ch: make(chan string, 16)}
	j := NewMagnetJanitor(nil, streaming)
	j.grace = 20 * time.Millisecond
	j.deleteFn = func(_ context.Context, provider, torrentID string) error {
		deletes.mu.Lock()
		deletes.ids = append(deletes.ids, provider+"/"+torrentID)
		deletes.mu.Unlock()
		deletes.ch <- torrentID
		return nil
	}
	return j, deletes
}

func (d *janitorDeletes) waitFor(t *testing.T, id string) {
	t.Helper()
	select {
	case got := <-d.ch:
		if got != id {
			t.Fatalf("deleted %q, want %q", got, id)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("magnet %s was not deleted", id)
	}
}

func (d *janitorDeletes) expectNone(t *testing.T) {
	t.Helper()
	select {
	case got := <-d.ch:
		t.Fatalf("unexpected delete of %q", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestParseDebridStreamTorrent(t *testing.T) {
	cases := []struct {
		path, provider, id string
		ok                 bool
	}{
		{"/debrid/alldebrid/123/file/2/movie.mkv", "alldebrid", "123", true},
		{"webdav/debrid/AllDebrid/9", "alldebrid", "9", true},
		{"debrid/realdebrid/abc/file/1", "realdebrid", "abc", true},
		{"/webdav/usenet/movie.mkv", "", "", false},
		{"https://aiostreams.example/api/v1/proxy/x", "", "", false},
		{"", "", "", false},
	}
	for _, tc := range cases {
		provider, id, ok := ParseDebridStreamTorrent(tc.path)
		if ok != tc.ok || provider != tc.provider || id != tc.id {
			t.Errorf("ParseDebridStreamTorrent(%q) = %q, %q, %t; want %q, %q, %t", tc.path, provider, id, ok, tc.provider, tc.id, tc.ok)
		}
	}
}

func TestMagnetJanitorDeletesUnusedAllDebridMagnetAfterGrace(t *testing.T) {
	j, deletes := newTestJanitor(t, nil)
	j.ReleaseStreamPath("/debrid/alldebrid/42/file/1/movie.mkv", "test")
	deletes.waitFor(t, "42")
}

func TestMagnetJanitorIgnoresOtherProvidersAndPaths(t *testing.T) {
	j, deletes := newTestJanitor(t, nil)
	j.ReleaseStreamPath("/debrid/realdebrid/42/file/1", "test")
	j.ReleaseStreamPath("/webdav/usenet/movie.mkv", "test")
	j.ReleaseTorrent("torbox", "7", "test")
	deletes.expectNone(t)
}

func TestMagnetJanitorKeepsMagnetStillReferencedByStreamPath(t *testing.T) {
	j, deletes := newTestJanitor(t, nil)
	// AllDebrid returns the same magnet ID for the same hash, so another
	// file of the same magnet still in use must protect it.
	j.AddStreamPathSource(func() []string { return []string{"webdav/debrid/alldebrid/42/file/3/other.mkv"} })
	j.ReleaseStreamPath("/debrid/alldebrid/42/file/1/movie.mkv", "test")
	deletes.expectNone(t)
}

func TestMagnetJanitorKeepsMagnetWithActivePlaybackMark(t *testing.T) {
	j, deletes := newTestJanitor(t, nil)
	health := NewHealthService(nil)
	health.MarkTorrentActive("alldebrid", "42")
	j.addHealthService(health)
	j.ReleaseTorrent("alldebrid", "42", "test")
	deletes.expectNone(t)
}

func TestMagnetJanitorKeepsRecentlyStreamedMagnet(t *testing.T) {
	streaming := NewStreamingProvider(nil)
	streaming.noteStreamed("alldebrid", "42")
	j, deletes := newTestJanitor(t, streaming)
	j.ReleaseTorrent("alldebrid", "42", "test")
	deletes.expectNone(t)
}

func TestMagnetJanitorCoalescesRepeatedReleases(t *testing.T) {
	j, deletes := newTestJanitor(t, nil)
	for i := 0; i < 5; i++ {
		j.ReleaseTorrent("alldebrid", "42", "test")
	}
	deletes.waitFor(t, "42")
	deletes.expectNone(t)
}

func TestMagnetJanitorRechecksUsageWhenGraceEnds(t *testing.T) {
	j, deletes := newTestJanitor(t, nil)
	j.grace = 100 * time.Millisecond
	var mu sync.Mutex
	var paths []string
	j.AddStreamPathSource(func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	})
	j.ReleaseTorrent("alldebrid", "42", "test")
	// The race winner for the same hash becomes ready during the grace period.
	mu.Lock()
	paths = []string{"/debrid/alldebrid/42/file/1"}
	mu.Unlock()
	deletes.expectNone(t)
}

func TestDiscardProviderTorrentKeepsIDWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := &ProviderCacheResult{TorrentID: "42"}
	discardProviderTorrent(ctx, nil, result)
	if result.TorrentID != "42" {
		t.Fatalf("TorrentID = %q, want it kept for the janitor", result.TorrentID)
	}
}

func TestMultiProviderLateResultsAreReleased(t *testing.T) {
	j, deletes := newTestJanitor(t, nil)
	svc := &MultiProviderService{janitor: j}
	results := make(chan *ProviderCacheResult, 2)
	// A losing AllDebrid check that also came back cached keeps its magnet;
	// a check that already cleaned up has no torrent ID.
	results <- &ProviderCacheResult{Client: NewAllDebridClient("key"), TorrentID: "77", IsCached: true}
	results <- &ProviderCacheResult{Client: NewAllDebridClient("key")}
	svc.releaseLateResults(results, 2)
	deletes.waitFor(t, "77")
	deletes.expectNone(t)
}
