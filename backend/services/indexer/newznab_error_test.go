package indexer

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"novastream/config"
	"novastream/internal/providerbreaker"
)

// A bad API key or exhausted limit comes back as HTTP 200 with an <error>
// document; the search error must carry the indexer's message rather than
// a generic "not an RSS feed".
func TestFetchTorznabSurfacesNewznabErrorDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><error code="100" description="Incorrect user credentials"/>`))
	}))
	defer server.Close()

	svc := &Service{httpc: server.Client(), providerBreaker: providerbreaker.New()}
	_, err := svc.fetchTorznab(t.Context(), config.IndexerConfig{Name: "Fixture", URL: server.URL, Type: "newznab", Enabled: true}, SearchOptions{Query: "test"})
	if err == nil || !strings.Contains(err.Error(), "Incorrect user credentials") {
		t.Fatalf("expected indexer error message, got %v", err)
	}
}
