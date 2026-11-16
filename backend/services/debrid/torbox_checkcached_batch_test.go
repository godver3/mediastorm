package debrid

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Torbox rejects checkcached URLs over ~8 KB with an HTML 414. A large search
// (335 hashes in the field) must be split so the quick check still works.
func TestTorboxBulkCheckSplitsLargeHashListsUnderURLLimit(t *testing.T) {
	var requests int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		if len(r.URL.RawQuery) > 8000 {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusRequestURITooLong)
			_, _ = w.Write([]byte("<html>414 Request-URI Too Large</html>"))
			return
		}
		hashes := strings.Split(r.URL.Query().Get("hash"), ",")
		var entries []string
		for _, hash := range hashes {
			if strings.HasPrefix(hash, "0") { // deterministic subset is cached
				entries = append(entries, fmt.Sprintf(`%q:{"hash":%q,"name":"x","size":1}`, hash, hash))
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"detail":"ok","data":{` + strings.Join(entries, ",") + `}}`))
	}))
	defer server.Close()

	client := NewTorboxClient("test-key")
	client.baseURL = server.URL

	hashes := make([]string, 335)
	wantCached := 0
	for i := range hashes {
		sum := sha1.Sum([]byte(fmt.Sprint(i)))
		hashes[i] = hex.EncodeToString(sum[:])
		if strings.HasPrefix(hashes[i], "0") {
			wantCached++
		}
	}

	got, err := client.CheckInstantAvailabilityBulk(context.Background(), hashes)
	if err != nil {
		t.Fatalf("CheckInstantAvailabilityBulk: %v", err)
	}
	if len(got) != len(hashes) {
		t.Fatalf("results = %d, want %d", len(got), len(hashes))
	}
	cached := 0
	for _, isCached := range got {
		if isCached {
			cached++
		}
	}
	if cached != wantCached {
		t.Fatalf("cached = %d, want %d", cached, wantCached)
	}
	if n := atomic.LoadInt64(&requests); n != 4 {
		t.Fatalf("requests = %d, want 4 batches of <=%d", n, torboxCheckCachedBatchSize)
	}
}

func TestTorboxBulkCheckReportsHTTPStatusForNonJSONError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>bad gateway</html>"))
	}))
	defer server.Close()

	client := NewTorboxClient("test-key")
	client.baseURL = server.URL
	_, err := client.CheckInstantAvailabilityBulk(context.Background(), []string{"abc"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("error = %v, want HTTP 502", err)
	}
}
