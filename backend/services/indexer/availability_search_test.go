package indexer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"novastream/config"
)

func TestAvailabilityForegroundCancelsActualProviderRequest(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	svc, idx := dedupTestService(t, func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(stopped) })
	ctx, release, err := svc.BeginAvailabilitySearch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	finished := make(chan error, 1)
	go func() {
		_, err := svc.searchTorznab(ctx, idx, SearchOptions{Query: "Example", AvailabilityCheck: true})
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}
	end := svc.beginForegroundSearch(SearchOptions{})
	defer end()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("actual background HTTP request outlived foreground search")
	}
	if err := <-finished; err == nil {
		t.Fatal("expected cancellation")
	}
	if _, _, err := svc.BeginAvailabilitySearch(context.Background()); err == nil {
		t.Fatal("background started during interactive search")
	}
	end()
	if _, _, err := svc.BeginAvailabilitySearch(context.Background()); err == nil {
		t.Fatal("quiet period not applied")
	}
}

func TestAvailabilityPartialProviderFailureCannotBecomeCompleteEmpty(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusGatewayTimeout} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			svc, good := dedupTestService(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `<rss><channel/></rss>`) })
			badServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "provider unavailable", status) }))
			defer badServer.Close()
			settings := config.DefaultSettings()
			settings.Indexers = []config.IndexerConfig{good, {Name: "Bad", Enabled: true, Type: "newznab", URL: badServer.URL}}
			_, err := svc.fetchUsenetResults(t.Context(), settings, SearchOptions{Query: "Example", AvailabilityCheck: true})
			if err == nil {
				t.Fatal("partial failure returned complete empty success")
			}
		})
	}
}

func TestAvailabilityProviderSelectionNeverAddsUnauthorizedIndexer(t *testing.T) {
	settings := config.DefaultSettings()
	settings.Indexers = []config.IndexerConfig{{Name: "Allowed", Enabled: true}, {Name: "Unselected", Enabled: true}, {Name: "Disabled", Enabled: false}}
	selected := availabilitySearchSettings(settings, SearchOptions{AvailabilityCheck: true, AvailabilityIndexerNames: []string{"Allowed", "Disabled", "Missing"}})
	if len(selected.Indexers) != 1 || selected.Indexers[0].Name != "Allowed" || selected.Streaming.ServiceMode != "usenet" || len(selected.TorrentScrapers) != 0 {
		t.Fatal("incorrect background source configuration")
	}
}
