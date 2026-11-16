package debrid

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"novastream/services/streaming"
)

func resetAllDebridUnlockCache(t *testing.T) {
	t.Helper()
	allDebridUnlocks = newAllDebridUnlockCacheStore()
	t.Cleanup(func() { allDebridUnlocks = newAllDebridUnlockCacheStore() })
}

// unlockServer answers /link/unlock with downloadURL(n) for the n-th call.
func unlockServer(t *testing.T, calls *int32, downloadURL func(n int32) string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/link/unlock" {
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		n := atomic.AddInt32(calls, 1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"success","data":{"link":%q,"filename":"movie.mkv","filesize":100}}`, downloadURL(n))
	}))
	t.Cleanup(server.Close)
	return server
}

func clientWithKey(server *httptest.Server, key string) *AllDebridClient {
	client := NewAllDebridClient(key)
	client.baseURL = server.URL
	client.httpClient = server.Client()
	return client
}

func TestAllDebridUnlockIsSharedAcrossClients(t *testing.T) {
	resetAllDebridUnlockCache(t)
	var calls int32
	server := unlockServer(t, &calls, func(n int32) string { return fmt.Sprintf("https://cdn.example/%d", n) })

	first, err := clientWithKey(server, "key-a").UnrestrictLink(context.Background(), "https://alldebrid.com/f/abc")
	if err != nil {
		t.Fatalf("first unlock: %v", err)
	}
	// Playback resolution and the streaming proxy build separate clients.
	second, err := clientWithKey(server, " key-a ").UnrestrictLink(context.Background(), " https://alldebrid.com/f/abc ")
	if err != nil {
		t.Fatalf("second unlock: %v", err)
	}
	if calls != 1 {
		t.Fatalf("unlock calls = %d, want 1", calls)
	}
	if first.DownloadURL != second.DownloadURL {
		t.Fatalf("download URLs differ: %q vs %q", first.DownloadURL, second.DownloadURL)
	}

	// A different account must not reuse another account's link.
	if _, err := clientWithKey(server, "key-b").UnrestrictLink(context.Background(), "https://alldebrid.com/f/abc"); err != nil {
		t.Fatalf("other key unlock: %v", err)
	}
	if calls != 2 {
		t.Fatalf("unlock calls after second key = %d, want 2", calls)
	}
}

func TestAllDebridConcurrentUnlocksShareOneRequest(t *testing.T) {
	resetAllDebridUnlockCache(t)
	var calls int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		<-release
		_, _ = w.Write([]byte(`{"status":"success","data":{"link":"https://cdn.example/one"}}`))
	}))
	defer server.Close()

	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := clientWithKey(server, "key").UnrestrictLink(context.Background(), "https://alldebrid.com/f/same")
			errs <- err
		}()
	}
	// Give every goroutine a chance to join the in-flight unlock.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&calls) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("unlock error: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("unlock calls = %d, want 1", calls)
	}
}

func TestAllDebridUnlockFailuresAreNotCached(t *testing.T) {
	resetAllDebridUnlockCache(t)
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch atomic.AddInt32(&calls, 1) {
		case 1:
			_, _ = w.Write([]byte(`{"status":"error","error":{"code":"LINK_DOWN","message":"link down"}}`))
		case 2:
			_, _ = w.Write([]byte(`{"status":"success","data":{"link":"","delayed":5}}`))
		default:
			_, _ = w.Write([]byte(`{"status":"success","data":{"link":"https://cdn.example/ok"}}`))
		}
	}))
	defer server.Close()
	client := clientWithKey(server, "key")

	if _, err := client.UnrestrictLink(context.Background(), "https://alldebrid.com/f/x"); err == nil {
		t.Fatal("expected error response to fail")
	}
	if _, err := client.UnrestrictLink(context.Background(), "https://alldebrid.com/f/x"); err == nil {
		t.Fatal("expected delayed response to fail")
	}
	result, err := client.UnrestrictLink(context.Background(), "https://alldebrid.com/f/x")
	if err != nil || result.DownloadURL != "https://cdn.example/ok" {
		t.Fatalf("third unlock = %+v, %v", result, err)
	}
	if calls != 3 {
		t.Fatalf("unlock calls = %d, want 3", calls)
	}
}

func TestAllDebridUnlockCacheExpiresAndInvalidates(t *testing.T) {
	resetAllDebridUnlockCache(t)
	now := time.Now()
	allDebridUnlocks.now = func() time.Time { return now }
	var calls int32
	server := unlockServer(t, &calls, func(n int32) string { return fmt.Sprintf("https://cdn.example/%d", n) })
	client := clientWithKey(server, "key")
	link := "https://alldebrid.com/f/x"

	first, _ := client.UnrestrictLink(context.Background(), link)
	if !InvalidateAllDebridDownloadURL(first.DownloadURL) {
		t.Fatal("expected invalidation to remove the cached unlock")
	}
	second, _ := client.UnrestrictLink(context.Background(), link)
	if calls != 2 || second.DownloadURL == first.DownloadURL {
		t.Fatalf("after invalidation calls=%d url=%q, want fresh unlock", calls, second.DownloadURL)
	}

	now = now.Add(allDebridUnlockCacheTTL - time.Minute)
	_, _ = client.UnrestrictLink(context.Background(), link)
	if calls != 2 {
		t.Fatalf("unlock before TTL calls = %d, want 2", calls)
	}
	now = now.Add(2 * time.Minute)
	_, _ = client.UnrestrictLink(context.Background(), link)
	if calls != 3 {
		t.Fatalf("unlock after TTL calls = %d, want 3", calls)
	}
}

// allDebridStreamFixture serves the AllDebrid API (status + unlock) and a CDN.
// The first unlocked URL is dead (404); later unlocks return a working URL.
func allDebridStreamFixture(t *testing.T) (client *AllDebridClient, unlockCalls *int32, cdnHits *int32) {
	t.Helper()
	unlockCalls = new(int32)
	cdnHits = new(int32)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(cdnHits, 1)
		if strings.HasSuffix(r.URL.Path, "/dead") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", "5")
		_, _ = w.Write([]byte("video"))
	}))
	t.Cleanup(cdn.Close)

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/magnet/status":
			_, _ = w.Write([]byte(`{"status":"success","data":{"magnets":{"id":7,"filename":"movie","statusCode":4,"files":[{"n":"movie.mkv","s":5,"l":"https://alldebrid.com/f/movie"}]}}}`))
		case "/link/unlock":
			target := cdn.URL + "/good"
			if atomic.AddInt32(unlockCalls, 1) == 1 {
				target = cdn.URL + "/dead"
			}
			fmt.Fprintf(w, `{"status":"success","data":{"link":%q,"filename":"movie.mkv"}}`, target)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	return clientWithKey(api, "key"), unlockCalls, cdnHits
}

func TestStreamingRetriesDeadAllDebridLinkOnce(t *testing.T) {
	resetAllDebridUnlockCache(t)
	client, unlockCalls, _ := allDebridStreamFixture(t)
	p := NewStreamingProvider(nil)

	resp, err := p.streamWithProvider(context.Background(), streaming.Request{Method: http.MethodGet}, client, "7", "1", true)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	resp.Body.Close()
	if *unlockCalls != 2 {
		t.Fatalf("unlock calls = %d, want 2 (dead link dropped and re-unlocked once)", *unlockCalls)
	}
	if cached, _, _, _, ok := p.getCachedURL(cacheKeyFor("7", "1")); !ok || !strings.HasSuffix(cached, "/good") {
		t.Fatalf("cached URL = %q (found=%t), want the working URL", cached, ok)
	}

	// Subsequent range requests reuse the working URL without new unlocks.
	resp, err = p.streamWithProvider(context.Background(), streaming.Request{Method: http.MethodGet, RangeHeader: "bytes=0-1"}, client, "7", "1", true)
	if err != nil {
		t.Fatalf("second stream: %v", err)
	}
	resp.Body.Close()
	if *unlockCalls != 2 {
		t.Fatalf("unlock calls after cached stream = %d, want 2", *unlockCalls)
	}
}

func TestStreamingKeepsCachedURLWhenClientCancels(t *testing.T) {
	resetAllDebridUnlockCache(t)
	blocked := make(chan struct{})
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(blocked)
		<-r.Context().Done()
	}))
	defer cdn.Close()

	var unlockCalls int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/link/unlock" {
			atomic.AddInt32(&unlockCalls, 1)
		}
		http.NotFound(w, r)
	}))
	defer api.Close()

	p := NewStreamingProvider(nil)
	key := cacheKeyFor("7", "1")
	p.setCachedURL(key, cdn.URL+"/movie", "movie.mkv", 0, 0)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-blocked
		cancel()
	}()
	_, err := p.streamWithProvider(ctx, streaming.Request{Method: http.MethodGet, RangeHeader: "bytes=100-"}, clientWithKey(api, "key"), "7", "1", true)
	if err == nil {
		t.Fatal("expected cancelled request to fail")
	}
	var sourceErr *SourceError
	if !errors.As(err, &sourceErr) {
		t.Fatalf("error = %T %v, want *SourceError", err, err)
	}
	if _, _, _, _, ok := p.getCachedURL(key); !ok {
		t.Fatal("cached URL was evicted after a client cancellation")
	}
	if unlockCalls != 0 {
		t.Fatalf("unlock calls = %d, want 0", unlockCalls)
	}
}
