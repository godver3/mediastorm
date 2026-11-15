package playback

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/models"
)

func configureNZBGatewayTest(t *testing.T, endpoint string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 48)), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NZB_GATEWAY_URL", endpoint)
	t.Setenv("NZB_GATEWAY_TOKEN_FILE", path)
	t.Setenv("NZB_GATEWAY_REQUIRED", "1")
}

func TestNZBGatewayFetchUsesStableCatalogKeyAndRawNZBBytes(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/fetch" || r.Method != http.MethodPost {
			t.Error("unexpected acquisition route")
		}
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("x", 48) {
			t.Error("missing gateway credential")
		}
		var item map[string]string
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			t.Error(err)
		}
		if item["content_key"] != "movie:tmdb:42" || item["release_key"] != "release-1" {
			t.Errorf("unstable request identity: %#v", item)
		}
		w.Header().Set("X-NZB-Filename", "Fixture%20Release.nzb")
		_, _ = w.Write([]byte("<nzb/>"))
	}))
	defer server.Close()
	configureNZBGatewayTest(t, server.URL)
	svc := &Service{}
	data, name, err := svc.fetchNZB(context.Background(), "https://indexer.invalid/api?apikey=synthetic", models.NZBResult{
		GUID: "release-1", Title: "Fixture", Indexer: "Indexer", Attributes: map[string]string{"nzbContentKey": "movie:tmdb:42"},
	})
	if err != nil || string(data) != "<nzb/>" || name != "Fixture Release.nzb" || calls != 1 {
		t.Fatalf("fetch failed: name=%q calls=%d err=%v", name, calls, err)
	}
}

func TestNZBGatewayLongCooldownIsReturnedWithoutDirectFallback(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":"indexer_cooldown","retry_after":7200}`))
	}))
	defer server.Close()
	configureNZBGatewayTest(t, server.URL)
	_, _, err := (&Service{}).fetchNZB(context.Background(), "https://indexer.invalid/file", models.NZBResult{Title: "Fixture"})
	if err == nil || !strings.Contains(err.Error(), "7200") || calls != 1 {
		t.Fatalf("cooldown was not preserved: calls=%d err=%v", calls, err)
	}
}

func TestNZBGatewaySpacingWaitCancelsWithTheCaller(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":"spacing","retry_after":7200}`))
	}))
	defer server.Close()
	configureNZBGatewayTest(t, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := (&Service{}).fetchNZB(ctx, "https://indexer.invalid/file", models.NZBResult{Title: "Fixture"})
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("cancel did not terminate spacing wait: %v", err)
	}
}

func TestNZBGatewayRequiredConfigurationFailsClosed(t *testing.T) {
	t.Setenv("NZB_GATEWAY_URL", "")
	t.Setenv("NZB_GATEWAY_REQUIRED", "1")
	_, _, err := (&Service{}).fetchNZB(context.Background(), "https://indexer.invalid/file", models.NZBResult{Title: "Fixture"})
	if err == nil || !strings.Contains(err.Error(), "configure") {
		t.Fatalf("missing configuration did not fail closed: %v", err)
	}
}
