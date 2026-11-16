package debrid

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newAllDebridTestClient(server *httptest.Server) *AllDebridClient {
	client := NewAllDebridClient("test-key")
	client.baseURL = server.URL
	client.httpClient = server.Client()
	return client
}

func TestAllDebridAddMagnetReturnsAuthoritativeReadyState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/magnet/upload" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"magnets":[{"id":123,"hash":"abcdef","name":"movie.mkv","ready":true}]}}`))
	}))
	defer server.Close()

	result, err := newAllDebridTestClient(server).AddMagnet(context.Background(), "magnet:?xt=urn:btih:abcdef")
	if err != nil {
		t.Fatalf("AddMagnet returned error: %v", err)
	}
	if result.ID != "123" || !result.CacheStatusKnown || !result.Cached {
		t.Fatalf("AddMagnet result = %#v", result)
	}
}

func TestAllDebridAddTorrentFileParsesFilesResponseAndReadyState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/magnet/upload/file" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"files":[{"id":456,"hash":"fedcba","name":"upload","ready":false}]}}`))
	}))
	defer server.Close()

	result, err := newAllDebridTestClient(server).AddTorrentFile(context.Background(), []byte("torrent"), "upload.torrent")
	if err != nil {
		t.Fatalf("AddTorrentFile returned error: %v", err)
	}
	if result.ID != "456" || !result.CacheStatusKnown || result.Cached {
		t.Fatalf("AddTorrentFile result = %#v", result)
	}
}

func TestAllDebridInstantAvailabilityIsReportedUnsupported(t *testing.T) {
	client := NewAllDebridClient("test-key")
	client.baseURL = "http://must-not-be-requested.invalid"

	cached, err := client.CheckInstantAvailability(context.Background(), "ABCDEF1234567890")
	if err == nil {
		t.Fatal("CheckInstantAvailability returned nil error, want unsupported error")
	}
	if cached {
		t.Fatal("CheckInstantAvailability returned cached=true for unsupported lookup")
	}
	if !errors.Is(err, ErrInstantAvailabilityUnsupported) {
		t.Fatalf("CheckInstantAvailability error = %q, want ErrInstantAvailabilityUnsupported", err)
	}
}
