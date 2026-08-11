package peartube

// A live PearTube relay, end to end.
//
// Every other test in this package runs against an httptest stub that answers
// the shapes this client expects. That proves the client parses what it was
// written to parse, and nothing about whether a real relay speaks the same
// contract - which is exactly where an integration breaks: a route renamed, a
// field dropped, a default port that no relay listens on.
//
// This test needs a relay holding at least one TMDB-tagged publication, so it
// is opt-in:
//
//	peartube-relay ui --storage /tmp/pt-live/storage --host 127.0.0.1 --port 8174
//	PEARTUBE_LIVE_RELAY=http://127.0.0.1:8174 \
//	PEARTUBE_COMPANION_CLIENT=mediastorm \
//	PEARTUBE_COMPANION_SHARED_SECRET=<64-lowercase-hex> \
//	go test ./backend/services/peartube -run Live

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"novastream/models"
)

func liveRelay(t *testing.T) *Client {
	t.Helper()
	base := strings.TrimSpace(os.Getenv("PEARTUBE_LIVE_RELAY"))
	if base == "" {
		t.Skip("set PEARTUBE_LIVE_RELAY to a running relay base URL to run this test")
	}
	client, err := New(base)
	if err != nil {
		t.Fatalf("New(%q): %v", base, err)
	}
	return client
}

func TestLiveRelayReturnsDeferredCandidates(t *testing.T) {
	client := liveRelay(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	state := client.Probe(ctx)
	if !state.Reachable {
		t.Fatalf("relay %s is not reachable: %s", state.RelayURL, state.Detail)
	}
	if state.NotOpen {
		t.Fatalf("relay refuses to enumerate: %s", state.Remedy)
	}
	if state.CatalogEntities == 0 {
		t.Fatal("relay catalog is empty; seed a publication before running this test")
	}

	entities, err := client.Catalog(ctx)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}

	// Search by the coordinates the relay itself reported, so the assertion is
	// about the contract rather than about whatever happens to be seeded.
	var (
		wantTitle string
		wantTMDB  string
	)
	for _, entity := range entities {
		for _, source := range entity.Sources {
			if coords, ok := coordinatesForSource(entity, source); ok && coords.Kind == "movie" {
				wantTitle, wantTMDB = entity.Title, coords.TMDBID
				break
			}
		}
		if wantTMDB != "" {
			break
		}
	}
	if wantTMDB == "" {
		t.Skip("no TMDB-tagged movie in the live catalog")
	}

	search := SearchRequest{Title: wantTitle, TMDBID: wantTMDB, MediaType: "movie"}
	candidates, err := client.Search(ctx, search)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(candidates) == 0 {
		t.Fatalf("search for tmdb=%s matched nothing in a catalog that reported it", wantTMDB)
	}
	if !validCandidateRef(candidates[0].CandidateRef) {
		t.Fatalf("candidateRef = %q", candidates[0].CandidateRef)
	}

	results := MapCandidates(search, candidates)
	got := results[0]
	if got.ServiceType != models.ServiceTypePearTube {
		t.Fatalf("serviceType = %q", got.ServiceType)
	}
	if got.Link != "" || got.DownloadURL != "" {
		t.Fatalf("live search minted a playback URL: %+v", got)
	}
	if got.Attributes["peartube_candidate_ref"] != candidates[0].CandidateRef {
		t.Fatalf("candidate ref was not preserved: %+v", got.Attributes)
	}
	if got.Attributes["preresolved"] != "" || got.Attributes["stream_url"] != "" {
		t.Fatalf("live search marked candidate resolved: %+v", got.Attributes)
	}
}
