package playback

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/internal/httpheaders"
	"novastream/models"
)

func TestFetchNZBSetsDownloadHeaders(t *testing.T) {
	var receivedUserAgent string
	var receivedAccept string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUserAgent = r.Header.Get("User-Agent")
		receivedAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Disposition", `attachment; filename="test.nzb"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<?xml version="1.0"?><nzb></nzb>`))
	}))
	defer server.Close()

	svc := &Service{httpClient: server.Client()}
	_, _, err := svc.fetchNZB(context.Background(), server.URL+"/test.nzb", models.NZBResult{Title: "Test"})
	if err != nil {
		t.Fatalf("fetchNZB returned error: %v", err)
	}
	if receivedUserAgent != httpheaders.UserAgent {
		t.Fatalf("User-Agent = %q, want %q", receivedUserAgent, httpheaders.UserAgent)
	}
	if receivedAccept == "" {
		t.Fatal("expected Accept header to be set")
	}
}
