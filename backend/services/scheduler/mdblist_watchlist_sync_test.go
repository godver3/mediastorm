package scheduler

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
)

const mdblistWatchlistTestSource = "mdblist:acc:task"

type mdblistWatchlistStub struct {
	mu        sync.Mutex
	watchlist string
	added     []string
	removed   []string
	gets      int
}

func installMDBListWatchlistStub(t *testing.T, watchlist string) *mdblistWatchlistStub {
	t.Helper()
	stub := &mdblistWatchlistStub{watchlist: watchlist}
	orig := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		body := ""
		if req.Body != nil {
			raw, _ := io.ReadAll(req.Body)
			body = string(raw)
		}
		switch {
		case req.URL.Host == "api.mdblist.com" && req.URL.Path == "/watchlist/items":
			stub.gets++
			return jsonResponse(http.StatusOK, stub.watchlist), nil
		case req.URL.Path == "/watchlist/items/add":
			stub.added = append(stub.added, body)
			return jsonResponse(http.StatusOK, `{"added":1,"existing":0,"not_found":0}`), nil
		case req.URL.Path == "/watchlist/items/remove":
			stub.removed = append(stub.removed, body)
			return jsonResponse(http.StatusOK, `{"added":0,"existing":0,"not_found":0}`), nil
		}
		t.Errorf("unexpected MDBList request: %s", req.URL)
		return jsonResponse(http.StatusNotFound, `{}`), nil
	})
	t.Cleanup(func() { http.DefaultTransport = orig })
	return stub
}

const (
	mdblistArrival = `{"title":"Arrival","release_year":2016,"ids":{"imdb":"tt2543164","tmdb":329865}}`
	mdblistDune    = `{"title":"Dune","release_year":2021,"ids":{"imdb":"tt1160419","tmdb":438631}}`
)

func mdblistList(movies ...string) string {
	return `{"movies":[` + strings.Join(movies, ",") + `],"shows":[],"pagination":{"has_more":false}}`
}

func runMDBListBidirectional(t *testing.T, svc *Service) {
	t.Helper()
	tracker, err := svc.newWatchlistRemovalTracker("task", "mdblist", "acc", "profile-1")
	if err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-time.Minute)
	if _, err := svc.syncMDBListWatchlist(&config.MDBListAccount{APIKey: "key"}, "profile-1", "bidirectional", mdblistWatchlistTestSource, "delete", &since, false, tracker); err != nil {
		t.Fatalf("syncMDBListWatchlist: %v", err)
	}
}

func localNames(t *testing.T, items []models.WatchlistItem) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, item := range items {
		names[item.Name] = true
	}
	return names
}

func TestMDBListWatchlist_BidirectionalImportsAndExports(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	svc.SetHistorySyncSnapshotStore(newMemoryHistorySnapshotStore())
	seedDune(t, watchlistSvc, "")
	stub := installMDBListWatchlistStub(t, mdblistList(mdblistArrival))

	runMDBListBidirectional(t, svc)

	items, _ := watchlistSvc.List("profile-1")
	if names := localNames(t, items); !names["Arrival"] || !names["Dune"] {
		t.Fatalf("local = %v, want Arrival imported and Dune kept", names)
	}
	if len(stub.added) != 1 || !strings.Contains(stub.added[0], `"tmdb":438631`) || !strings.Contains(stub.added[0], `"movies"`) {
		t.Fatalf("added = %v, want Dune exported", stub.added)
	}
}

func TestMDBListWatchlist_RemovalOnMDBListRemovesLocally(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	svc.SetHistorySyncSnapshotStore(newMemoryHistorySnapshotStore())
	seedDune(t, watchlistSvc, "")
	stub := installMDBListWatchlistStub(t, mdblistList(mdblistArrival, mdblistDune))

	runMDBListBidirectional(t, svc) // snapshot records both
	stub.mu.Lock()
	stub.watchlist = mdblistList(mdblistArrival) // Dune removed on MDBList
	stub.mu.Unlock()
	runMDBListBidirectional(t, svc)

	items, _ := watchlistSvc.List("profile-1")
	if names := localNames(t, items); names["Dune"] || !names["Arrival"] {
		t.Fatalf("local = %v, want Dune removed and Arrival kept", names)
	}
	if len(stub.added) != 0 {
		t.Fatalf("re-added to MDBList: %v", stub.added)
	}
	if !watchlistSvc.RemovedSince("profile-1", "movie", "tmdb:movie:438631", map[string]string{"tmdb": "438631"}, time.Time{}) {
		t.Fatal("removal on MDBList should be recorded locally so other syncs propagate it")
	}
}

func TestMDBListWatchlist_LocalRemovalRemovesFromMDBList(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	svc.SetHistorySyncSnapshotStore(newMemoryHistorySnapshotStore())
	seedDune(t, watchlistSvc, "")
	stub := installMDBListWatchlistStub(t, mdblistList(mdblistArrival, mdblistDune))
	runMDBListBidirectional(t, svc)

	if _, err := watchlistSvc.Remove("profile-1", "movie", "tmdb:movie:438631"); err != nil {
		t.Fatal(err)
	}
	runMDBListBidirectional(t, svc)

	if len(stub.removed) != 1 || !strings.Contains(stub.removed[0], `"tmdb":438631`) {
		t.Fatalf("removed = %v, want Dune removed from MDBList", stub.removed)
	}
	items, _ := watchlistSvc.List("profile-1")
	if localNames(t, items)["Dune"] {
		t.Fatal("Dune imported back from MDBList")
	}
}

func TestFetchMDBListWatchlist_FollowsCursor(t *testing.T) {
	calls := 0
	orig := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Query().Get("cursor") == "" {
			return jsonResponse(http.StatusOK, `{"movies":[`+mdblistArrival+`],"shows":[],"pagination":{"has_more":true,"next_cursor":"c2"}}`), nil
		}
		if req.URL.Query().Get("cursor") != "c2" {
			t.Errorf("cursor = %q", req.URL.Query().Get("cursor"))
		}
		return jsonResponse(http.StatusOK, mdblistList(mdblistDune)), nil
	})
	defer func() { http.DefaultTransport = orig }()

	items, err := fetchMDBListWatchlist("key")
	if err != nil || len(items) != 2 || calls != 2 {
		t.Fatalf("items=%d calls=%d err=%v", len(items), calls, err)
	}
}

func TestMDBListNotFoundCountAcceptsBothShapes(t *testing.T) {
	for body, want := range map[string]int{
		`{"added":{"movies":1,"shows":0},"not_found":{"movies":2,"shows":1}}`: 3,
		`{"added":1,"existing":0,"not_found":4}`:                              4,
		`{"added":1}`:                                                         0,
	} {
		if got := mdblistNotFoundCount([]byte(body)); got != want {
			t.Errorf("%s: got %d, want %d", body, got, want)
		}
	}
}
