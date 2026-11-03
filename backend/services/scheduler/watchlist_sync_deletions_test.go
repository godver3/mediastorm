package scheduler

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"novastream/config"
	"novastream/services/trakt"
)

const traktWatchlistTestSource = "trakt:acc:watchlist:task"

// traktWatchlistStub serves a fixed Trakt watchlist and records the bodies of
// watchlist add/remove calls.
type traktWatchlistStub struct {
	mu      sync.Mutex
	added   []string
	removed []string
}

func installTraktWatchlistStub(t *testing.T, svc *Service, watchlistJSON string) *traktWatchlistStub {
	t.Helper()
	origURL := trakt.GetBaseURLForTest()
	trakt.SetBaseURLForTest("https://trakt.example")
	t.Cleanup(func() { trakt.SetBaseURLForTest(origURL) })

	stub := &traktWatchlistStub{}
	client := trakt.NewClient("id", "secret")
	client.SetHTTPClientForTest(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := ""
		if req.Body != nil {
			raw, _ := io.ReadAll(req.Body)
			body = string(raw)
		}
		resp := jsonResponse(http.StatusOK, `{}`)
		switch req.URL.Path {
		case "/users/me/watchlist":
			resp = jsonResponse(http.StatusOK, watchlistJSON)
			resp.Header.Set("X-Pagination-Item-Count", "1")
		case "/sync/watchlist":
			resp = jsonResponse(http.StatusCreated, `{}`)
			stub.mu.Lock()
			stub.added = append(stub.added, body)
			stub.mu.Unlock()
		case "/sync/watchlist/remove":
			stub.mu.Lock()
			stub.removed = append(stub.removed, body)
			stub.mu.Unlock()
		default:
			t.Fatalf("unexpected Trakt request: %s", req.URL)
		}
		return resp, nil
	})})
	svc.traktClient = client
	return stub
}

const duneTraktWatchlistJSON = `[{"rank":1,"type":"movie","movie":{"title":"Dune","year":2021,"ids":{"trakt":1,"tmdb":438631,"imdb":"tt1160419"}}}]`

func TestSyncTraktBidirectional_MirrorRemovesLocalItemRemovedFromTrakt(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	seedDune(t, watchlistSvc, traktWatchlistTestSource)
	stub := installTraktWatchlistStub(t, svc, `[]`)

	since := time.Now().Add(-time.Hour)
	if _, err := svc.syncTraktBidirectional(&config.TraktAccount{AccessToken: "token"}, "profile-1", "watchlist", "", traktWatchlistTestSource, "mirror", "source_wins", &since, false, nil); err != nil {
		t.Fatalf("syncTraktBidirectional() error = %v", err)
	}

	if len(stub.added) != 0 {
		t.Fatalf("re-added to Trakt: %v", stub.added)
	}
	if items, _ := watchlistSvc.List("profile-1"); len(items) != 0 {
		t.Fatalf("local items = %#v, want Dune removed", items)
	}
}

func TestSyncTraktBidirectional_MirrorRemovesFromTraktAfterLocalRemoval(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	seedDune(t, watchlistSvc, traktWatchlistTestSource)
	since := time.Now().Add(-time.Minute)
	if _, err := watchlistSvc.Remove("profile-1", "movie", "tmdb:movie:438631"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	stub := installTraktWatchlistStub(t, svc, duneTraktWatchlistJSON)

	if _, err := svc.syncTraktBidirectional(&config.TraktAccount{AccessToken: "token"}, "profile-1", "watchlist", "", traktWatchlistTestSource, "mirror", "source_wins", &since, false, nil); err != nil {
		t.Fatalf("syncTraktBidirectional() error = %v", err)
	}

	if len(stub.removed) != 1 || !strings.Contains(stub.removed[0], `"tmdb":438631`) {
		t.Fatalf("removed = %v, want Dune removed from Trakt", stub.removed)
	}
	if items, _ := watchlistSvc.List("profile-1"); len(items) != 0 {
		t.Fatalf("local items = %#v, want Dune not re-imported", items)
	}
}

func TestSyncTraktBidirectional_ExportMarksItemSynced(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	seedDune(t, watchlistSvc, "")
	stub := installTraktWatchlistStub(t, svc, `[]`)

	since := time.Now().Add(-time.Hour)
	result, err := svc.syncTraktBidirectional(&config.TraktAccount{AccessToken: "token"}, "profile-1", "watchlist", "", traktWatchlistTestSource, "mirror", "source_wins", &since, false, nil)
	if err != nil {
		t.Fatalf("syncTraktBidirectional() error = %v", err)
	}

	if len(stub.added) != 1 || !strings.Contains(stub.added[0], `"tmdb":438631`) {
		t.Fatalf("added = %v, want Dune exported", stub.added)
	}
	items, _ := watchlistSvc.List("profile-1")
	if len(items) != 1 || items[0].SyncSource != traktWatchlistTestSource {
		t.Fatalf("local items = %#v, want Dune marked as synced", items)
	}
	if result.Config[watchlistSyncCursorKey] == "" {
		t.Fatal("missing deletion cursor in result config")
	}
}

func TestSyncLocalToTrakt_DeleteRemovesOnlyLocalRemovals(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	seedDune(t, watchlistSvc, "")
	since := time.Now().Add(-time.Minute)
	if _, err := watchlistSvc.Remove("profile-1", "movie", "tmdb:movie:438631"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	// Dune was removed locally; Arrival was only ever on Trakt.
	stub := installTraktWatchlistStub(t, svc, `[
		{"rank":1,"type":"movie","movie":{"title":"Dune","year":2021,"ids":{"trakt":1,"tmdb":438631,"imdb":"tt1160419"}}},
		{"rank":2,"type":"movie","movie":{"title":"Arrival","year":2016,"ids":{"trakt":2,"tmdb":329865,"imdb":"tt2543164"}}}
	]`)

	if _, err := svc.syncLocalToTrakt(&config.TraktAccount{AccessToken: "token"}, "profile-1", traktWatchlistTestSource, "delete", &since, false); err != nil {
		t.Fatalf("syncLocalToTrakt() error = %v", err)
	}

	if len(stub.removed) != 1 {
		t.Fatalf("removed = %v, want one call", stub.removed)
	}
	var body struct {
		Movies []trakt.SyncMovie `json:"movies"`
	}
	if err := json.Unmarshal([]byte(stub.removed[0]), &body); err != nil {
		t.Fatalf("decode removal: %v", err)
	}
	if len(body.Movies) != 1 || body.Movies[0].IDs.TMDB != 438631 {
		t.Fatalf("removed movies = %#v, want only Dune", body.Movies)
	}
}

func TestExecuteTraktListSync_RejectsExportForNonWatchlist(t *testing.T) {
	svc := &Service{}
	for _, direction := range []string{"target_to_source", "bidirectional"} {
		_, err := svc.executeTraktListSync(config.ScheduledTask{Config: map[string]string{
			"traktAccountId": "acc",
			"profileId":      "profile-1",
			"listType":       "collection",
			"syncDirection":  direction,
		}})
		if err == nil || !strings.Contains(err.Error(), "can only be imported") {
			t.Fatalf("%s: err = %v, want import-only error", direction, err)
		}
	}
}

func TestSyncLocalToPlex_DeleteRemovesOnlyLocalRemovals(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	seedDune(t, watchlistSvc, "")
	since := time.Now().Add(-time.Minute)
	if _, err := watchlistSvc.Remove("profile-1", "movie", "tmdb:movie:438631"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	// Dune was removed locally; Arrival was only ever on Plex.
	stub := installPlexBidirectionalStub(t, svc, `{
		"MediaContainer": {
			"size": 2,
			"totalSize": 2,
			"Metadata": [
				{"ratingKey": "plex-dune", "type": "movie", "title": "Dune", "year": 2021},
				{"ratingKey": "plex-arrival", "type": "movie", "title": "Arrival", "year": 2016}
			]
		}
	}`)

	result, err := svc.syncLocalToPlex("plex-token", "profile-1", plexBidirectionalTestSource, "delete", &since, false)
	if err != nil {
		t.Fatalf("syncLocalToPlex() error = %v", err)
	}

	if len(stub.removed) != 1 || stub.removed[0] != "plex-dune" {
		t.Fatalf("removed = %v, want [plex-dune]", stub.removed)
	}
	if result.Config[watchlistSyncCursorKey] == "" {
		t.Fatal("missing deletion cursor in result config")
	}
}

func TestExecuteMDBListWatchlistSync_RejectsUnknownDirection(t *testing.T) {
	svc := &Service{}
	_, err := svc.executeMDBListWatchlistSync(config.ScheduledTask{Config: map[string]string{
		"mdblistAccountId": "acc",
		"profileId":        "profile-1",
		"syncDirection":    "sideways",
	}})
	if err == nil || !strings.Contains(err.Error(), "unknown sync direction") {
		t.Fatalf("err = %v, want unknown direction error", err)
	}
}

func TestSyncMDBListWatchlistToLocal_MirrorRemovesUnsyncedItems(t *testing.T) {
	for _, tc := range []struct {
		name   string
		dryRun bool
	}{{"apply", false}, {"dry run", true}} {
		t.Run(tc.name, func(t *testing.T) {
			svc, watchlistSvc := newPlexBidirectionalTestService(t)
			seedDune(t, watchlistSvc, "") // added locally, not by this task

			origTransport := http.DefaultTransport
			http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host != "api.mdblist.com" || req.URL.Path != "/watchlist/items" {
					t.Fatalf("unexpected request: %s", req.URL)
				}
				return jsonResponse(http.StatusOK, `{"movies":[],"shows":[]}`), nil
			})
			defer func() { http.DefaultTransport = origTransport }()

			result, err := svc.syncMDBListWatchlistToLocal(&config.MDBListAccount{APIKey: "key"}, "profile-1", "mdblist:acc:task", "mirror", tc.dryRun)
			if err != nil {
				t.Fatalf("syncMDBListWatchlistToLocal() error = %v", err)
			}

			items, _ := watchlistSvc.List("profile-1")
			if tc.dryRun {
				if len(items) != 1 || len(result.ToRemove) != 1 {
					t.Fatalf("dry run: items = %d, ToRemove = %#v, want Dune listed and kept", len(items), result.ToRemove)
				}
				return
			}
			if len(items) != 0 {
				t.Fatalf("local items = %#v, want Dune removed in mirror mode", items)
			}
		})
	}
}
