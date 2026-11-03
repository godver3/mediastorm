package scheduler

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"novastream/config"
	"novastream/services/simkl"
)

const simklWatchlistTestSource = "simkl:acc:plantowatch:task"

// simklWatchlistStub serves fixed Simkl all-items buckets and records the
// bodies of add-to-list and history/remove calls.
type simklWatchlistStub struct {
	mu      sync.Mutex
	added   []string
	removed []string
}

func installSimklWatchlistStub(t *testing.T, buckets map[string]string) (*simkl.Client, *simklWatchlistStub) {
	t.Helper()
	simkl.SetBaseURLForTest("https://simkl.example")
	t.Cleanup(func() { simkl.SetBaseURLForTest("https://api.simkl.com") })

	stub := &simklWatchlistStub{}
	client := simkl.NewClient()
	client.SetHTTPClientForTest(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := ""
		if req.Body != nil {
			raw, _ := io.ReadAll(req.Body)
			body = string(raw)
		}
		switch {
		case strings.HasPrefix(req.URL.Path, "/sync/all-items/"):
			bucket := strings.TrimPrefix(req.URL.Path, "/sync/all-items/")
			if payload, ok := buckets[bucket]; ok {
				return jsonResponse(http.StatusOK, payload), nil
			}
			return jsonResponse(http.StatusOK, `{}`), nil
		case req.URL.Path == "/sync/add-to-list":
			stub.mu.Lock()
			stub.added = append(stub.added, body)
			stub.mu.Unlock()
			return jsonResponse(http.StatusCreated, `{"added":{},"not_found":{"movies":[],"shows":[]}}`), nil
		case req.URL.Path == "/sync/history/remove":
			stub.mu.Lock()
			stub.removed = append(stub.removed, body)
			stub.mu.Unlock()
			return jsonResponse(http.StatusOK, `{}`), nil
		}
		t.Fatalf("unexpected Simkl request: %s", req.URL)
		return nil, nil
	})})
	return client, stub
}

var simklTestAccount = &config.SimklAccount{ID: "acc", ClientID: "client", AccessToken: "token"}

const (
	simklDunePlanToWatch = `{"movies":[{"status":"plantowatch","movie":{"title":"Dune","year":2021,"ids":{"simkl":1,"tmdb":"438631","imdb":"tt1160419"}}}]}`
	simklDuneCompleted   = `{"movies":[{"status":"completed","movie":{"title":"Dune","year":2021,"ids":{"simkl":1,"tmdb":"438631","imdb":"tt1160419"}}}]}`
)

func TestSyncSimklWatchlist_ImportsOnlyPlanToWatch(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	client, stub := installSimklWatchlistStub(t, map[string]string{
		"movies": `{"movies":[
			{"status":"plantowatch","movie":{"title":"Dune","year":2021,"ids":{"simkl":1,"tmdb":"438631"}}},
			{"status":"completed","movie":{"title":"Arrival","year":2016,"ids":{"simkl":2,"tmdb":"329865"}}}
		]}`,
		"shows": `{"shows":[{"status":"plantowatch","show":{"title":"Severance","year":2022,"ids":{"simkl":3,"tmdb":"95396"}}}]}`,
	})

	result, err := svc.syncSimklWatchlist(client, simklTestAccount, "profile-1", "source_to_target", simklWatchlistTestSource, "additive", nil, false, nil)
	if err != nil {
		t.Fatalf("syncSimklWatchlist() error = %v", err)
	}
	items, _ := watchlistSvc.List("profile-1")
	names := map[string]string{}
	for _, item := range items {
		names[item.Name] = item.MediaType
	}
	if len(items) != 2 || names["Dune"] != "movie" || names["Severance"] != "series" {
		t.Fatalf("local items = %#v, want Dune (movie) and Severance (series) only", names)
	}
	if result.Count != 2 || len(stub.added) != 0 || len(stub.removed) != 0 {
		t.Fatalf("count=%d added=%v removed=%v", result.Count, stub.added, stub.removed)
	}
}

func TestSyncSimklWatchlist_BidirectionalExportsNewLocalItems(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	seedDune(t, watchlistSvc, "")
	client, stub := installSimklWatchlistStub(t, nil)

	result, err := svc.syncSimklWatchlist(client, simklTestAccount, "profile-1", "bidirectional", simklWatchlistTestSource, "mirror", nil, false, nil)
	if err != nil {
		t.Fatalf("syncSimklWatchlist() error = %v", err)
	}
	if len(stub.added) != 1 || !strings.Contains(stub.added[0], `"to":"plantowatch"`) || !strings.Contains(stub.added[0], `"tmdb":438631`) {
		t.Fatalf("added = %v, want Dune sent to plantowatch", stub.added)
	}
	if strings.Contains(stub.added[0], `"title"`) {
		t.Fatalf("added = %v, want IDs only so Simkl can't fuzzy-match a different title", stub.added)
	}
	items, _ := watchlistSvc.List("profile-1")
	if len(items) != 1 || items[0].SyncSource != simklWatchlistTestSource {
		t.Fatalf("local items = %#v, want Dune marked as synced", items)
	}
	if result.Config[watchlistSyncCursorKey] == "" {
		t.Fatal("missing deletion cursor in result config")
	}
}

func TestSyncSimklWatchlist_DoesNotMoveCompletedItemsBackToPlanToWatch(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	seedDune(t, watchlistSvc, "")
	client, stub := installSimklWatchlistStub(t, map[string]string{"movies": simklDuneCompleted})

	if _, err := svc.syncSimklWatchlist(client, simklTestAccount, "profile-1", "target_to_source", simklWatchlistTestSource, "additive", nil, false, nil); err != nil {
		t.Fatalf("syncSimklWatchlist() error = %v", err)
	}
	if len(stub.added) != 0 {
		t.Fatalf("added = %v, want completed Dune left alone", stub.added)
	}
}

func TestSyncSimklWatchlist_MirrorRemovesLocalItemRemovedFromSimkl(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	seedDune(t, watchlistSvc, simklWatchlistTestSource)
	client, stub := installSimklWatchlistStub(t, nil)

	since := time.Now().Add(-time.Hour)
	if _, err := svc.syncSimklWatchlist(client, simklTestAccount, "profile-1", "bidirectional", simklWatchlistTestSource, "mirror", &since, false, nil); err != nil {
		t.Fatalf("syncSimklWatchlist() error = %v", err)
	}
	if len(stub.added) != 0 {
		t.Fatalf("re-added to Simkl: %v", stub.added)
	}
	if items, _ := watchlistSvc.List("profile-1"); len(items) != 0 {
		t.Fatalf("local items = %#v, want Dune removed", items)
	}
}

func TestSyncSimklWatchlist_MirrorRemovesFromSimklAfterLocalRemoval(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	seedDune(t, watchlistSvc, simklWatchlistTestSource)
	since := time.Now().Add(-time.Minute)
	if _, err := watchlistSvc.Remove("profile-1", "movie", "tmdb:movie:438631"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	client, stub := installSimklWatchlistStub(t, map[string]string{"movies": simklDunePlanToWatch})

	if _, err := svc.syncSimklWatchlist(client, simklTestAccount, "profile-1", "bidirectional", simklWatchlistTestSource, "mirror", &since, false, nil); err != nil {
		t.Fatalf("syncSimklWatchlist() error = %v", err)
	}
	if len(stub.removed) != 1 || !strings.Contains(stub.removed[0], `"tmdb":438631`) || strings.Contains(stub.removed[0], `"to"`) {
		t.Fatalf("removed = %v, want Dune removed from Simkl", stub.removed)
	}
	if items, _ := watchlistSvc.List("profile-1"); len(items) != 0 {
		t.Fatalf("local items = %#v, want Dune not re-imported", items)
	}
}

func TestSyncSimklWatchlist_AdditiveNeverRemoves(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	seedDune(t, watchlistSvc, simklWatchlistTestSource)
	client, stub := installSimklWatchlistStub(t, nil)

	since := time.Now().Add(-time.Hour)
	if _, err := svc.syncSimklWatchlist(client, simklTestAccount, "profile-1", "bidirectional", simklWatchlistTestSource, "additive", &since, false, nil); err != nil {
		t.Fatalf("syncSimklWatchlist() error = %v", err)
	}
	if items, _ := watchlistSvc.List("profile-1"); len(items) != 1 {
		t.Fatalf("local items = %#v, want Dune kept", items)
	}
	if len(stub.removed) != 0 || len(stub.added) != 1 {
		t.Fatalf("added=%v removed=%v, want Dune re-exported and nothing removed", stub.added, stub.removed)
	}
}
