package skipsegments

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newFakeService(t *testing.T, handler http.HandlerFunc, timeout time.Duration) *Service {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return New(Options{IntroDBURL: server.URL + "/intro", SkipDBURL: server.URL + "/skip", ProviderTimeout: timeout})
}

func TestNormalizeEpisodeKey(t *testing.T) {
	key, err := NormalizeEpisodeKey(" TT123 ", 1, 2)
	if err != nil || key.IMDbID != "tt123" {
		t.Fatalf("key = %+v, err = %v", key, err)
	}
	for _, bad := range []struct {
		id      string
		s, e    int
		comment string
	}{{"123", 1, 1, "no tt"}, {"tt1", 0, 1, "season"}, {"tt1", 1, 0, "episode"}} {
		if _, err := NormalizeEpisodeKey(bad.id, bad.s, bad.e); err == nil {
			t.Errorf("%s: expected error", bad.comment)
		}
	}
}

func TestLookupSkipsSkipDBWhenIntroDBComplete(t *testing.T) {
	var introCalls, skipCalls atomic.Int32
	service := newFakeService(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/intro":
			introCalls.Add(1)
			_, _ = w.Write([]byte(`{"intro":{"start_ms":1,"end_ms":2},"recap":{"start_ms":3,"end_ms":4},"outro":{"start_ms":1150000,"end_ms":1190000}}`))
		case "/skip":
			skipCalls.Add(1)
		}
	}, time.Second)
	key, _ := NormalizeEpisodeKey("tt1", 1, 1)
	for i := 0; i < 3; i++ {
		result := service.Lookup(context.Background(), key, 1200)
		if result.IntroDB == nil || result.SkipDB != nil || result.IntroDBErr != nil {
			t.Fatalf("result = %+v", result)
		}
	}
	if introCalls.Load() != 1 || skipCalls.Load() != 0 {
		t.Errorf("calls = %d/%d, want 1/0", introCalls.Load(), skipCalls.Load())
	}
}

func TestLookupFallsBackToSkipDBWithRoundedDuration(t *testing.T) {
	var skipDuration atomic.Value
	service := newFakeService(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/intro":
			http.NotFound(w, r)
		case "/skip":
			skipDuration.Store(r.URL.Query().Get("duration"))
			_, _ = w.Write([]byte(`{"segments":{"intro":{"start_ms":1000,"end_ms":2000,"match":"exact"}}}`))
		}
	}, time.Second)
	key, _ := NormalizeEpisodeKey("tt1", 1, 1)
	result := service.Lookup(context.Background(), key, 1319.6)
	if result.IntroDB != nil || result.IntroDBErr != nil {
		t.Errorf("IntroDB 404 should be an empty, non-error result: %+v", result)
	}
	if result.SkipDB == nil || result.SkipDB.Segments.Intro == nil {
		t.Fatalf("SkipDB result missing: %+v", result)
	}
	if got := skipDuration.Load(); got != "1320" {
		t.Errorf("SkipDB duration = %v, want 1320", got)
	}
}

func TestLookupTimesOutAndNegativelyCachesErrors(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	service := newFakeService(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}, 50*time.Millisecond)
	defer close(release)
	key, _ := NormalizeEpisodeKey("tt1", 1, 1)
	started := time.Now()
	result := service.Lookup(context.Background(), key, 0)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("lookup took %v; provider timeout not applied", elapsed)
	}
	if result.IntroDBErr == nil || result.SkipDBErr == nil {
		t.Fatalf("expected both provider errors: %+v", result)
	}
	before := calls.Load()
	_ = service.Lookup(context.Background(), key, 0)
	if calls.Load() != before {
		t.Errorf("failed lookups should be negatively cached; calls %d -> %d", before, calls.Load())
	}
}

func TestLookupSharesConcurrentFetchesAndHonoursCallerCancel(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	service := newFakeService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/intro" {
			calls.Add(1)
			<-release
			_, _ = w.Write([]byte(`{"intro":{"start_ms":1,"end_ms":2},"recap":{"start_ms":3,"end_ms":4},"outro":{"start_ms":1150000,"end_ms":1190000}}`))
		}
	}, 5*time.Second)
	key, _ := NormalizeEpisodeKey("tt1", 1, 1)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if result := service.Lookup(cancelled, key, 0); result.IntroDBErr == nil {
		t.Errorf("cancelled caller should stop waiting: %+v", result)
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if result := service.Lookup(context.Background(), key, 0); result.IntroDB == nil {
				t.Errorf("shared lookup missing IntroDB: %+v", result)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Errorf("IntroDB calls = %d, want 1", calls.Load())
	}
}
