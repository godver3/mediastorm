package scheduler

import (
	"strings"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
)

const otherSyncSource = "trakt:acc:watchlist:other-task"

func TestSimklWatchlist_RemovalDetectedEvenWhenAnotherSyncMarkedTheItem(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	svc.SetHistorySyncSnapshotStore(newMemoryHistorySnapshotStore())
	seedDune(t, watchlistSvc, otherSyncSource)
	buckets := map[string]string{"movies": simklDunePlanToWatch}
	client, stub := installSimklWatchlistStub(t, buckets)

	run := func() {
		t.Helper()
		tracker, err := svc.newWatchlistRemovalTracker("task", "simkl", "acc", "profile-1")
		if err != nil {
			t.Fatal(err)
		}
		since := time.Now().Add(-time.Hour)
		if _, err := svc.syncSimklWatchlist(client, simklTestAccount, "profile-1", "bidirectional", simklWatchlistTestSource, "delete", &since, false, tracker); err != nil {
			t.Fatalf("syncSimklWatchlist: %v", err)
		}
	}

	run() // Dune is on Simkl: snapshot records it.
	// The Trakt sync runs in between and takes over the item's marker.
	seedDune(t, watchlistSvc, otherSyncSource)
	// Dune is removed on Simkl.
	buckets["movies"] = `{"movies":[]}`
	run()

	if items, _ := watchlistSvc.List("profile-1"); len(items) != 0 {
		t.Fatalf("local items = %#v, want Dune removed after its removal on Simkl", items)
	}
	if len(stub.added) != 0 {
		t.Fatalf("re-added to Simkl: %v", stub.added)
	}
}

func TestSimklWatchlist_ExportSimklNeverAcceptedIsNotRemovedLocally(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	svc.SetHistorySyncSnapshotStore(newMemoryHistorySnapshotStore())
	seedDune(t, watchlistSvc, "")
	// Simkl never lists Dune (its add reported not_found).
	client, stub := installSimklWatchlistStub(t, nil)

	for i := 0; i < 2; i++ {
		tracker, err := svc.newWatchlistRemovalTracker("task", "simkl", "acc", "profile-1")
		if err != nil {
			t.Fatal(err)
		}
		since := time.Now().Add(-time.Hour)
		if _, err := svc.syncSimklWatchlist(client, simklTestAccount, "profile-1", "bidirectional", simklWatchlistTestSource, "delete", &since, false, tracker); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if items, _ := watchlistSvc.List("profile-1"); len(items) != 1 {
		t.Fatalf("local items = %#v, want Dune kept", items)
	}
	if len(stub.added) != 2 {
		t.Fatalf("added = %v, want Dune offered to Simkl on each run", stub.added)
	}
}

func TestTraktWatchlist_RemovalDetectedEvenWhenAnotherSyncMarkedTheItem(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	svc.SetHistorySyncSnapshotStore(newMemoryHistorySnapshotStore())
	seedDune(t, watchlistSvc, simklWatchlistTestSource)

	run := func(watchlistJSON string) *traktWatchlistStub {
		t.Helper()
		stub := installTraktWatchlistStub(t, svc, watchlistJSON)
		tracker, err := svc.newWatchlistRemovalTracker("task", "trakt", "acc", "profile-1")
		if err != nil {
			t.Fatal(err)
		}
		since := time.Now().Add(-time.Hour)
		if _, err := svc.syncTraktBidirectional(&config.TraktAccount{AccessToken: "token"}, "profile-1", "watchlist", "", traktWatchlistTestSource, "delete", "source_wins", &since, false, tracker); err != nil {
			t.Fatalf("syncTraktBidirectional: %v", err)
		}
		return stub
	}

	run(duneTraktWatchlistJSON)
	seedDune(t, watchlistSvc, simklWatchlistTestSource) // the Simkl sync re-marks it
	stub := run(`[]`)

	if items, _ := watchlistSvc.List("profile-1"); len(items) != 0 {
		t.Fatalf("local items = %#v, want Dune removed after its removal on Trakt", items)
	}
	if len(stub.added) != 0 {
		t.Fatalf("re-added to Trakt: %v", stub.added)
	}
}

func TestWatchlistRemovalTracker_EmptyRemoteListIsUnsafe(t *testing.T) {
	prevKeys := map[string]struct{}{"movie:438631": {}}
	for i := 0; i < watchlistRemovalEmptyListMaxKeys; i++ {
		prevKeys["movie:"+string(rune('a'+i))] = struct{}{}
	}
	tracker := &watchlistRemovalTracker{
		provider: "simkl",
		previous: &models.HistorySyncSnapshot{SyncedAt: time.Now()},
		prevKeys: prevKeys,
	}
	tracker.setRemote(map[string]bool{})
	item := models.WatchlistItem{ID: "tmdb:movie:438631", MediaType: "movie", ExternalIDs: map[string]string{"tmdb": "438631"}, AddedAt: time.Now().Add(-time.Hour)}
	if tracker.removedRemotely(item) {
		t.Fatal("an empty remote listing must not be read as removals")
	}
}

func TestWatchlistRemovalTracker_ItemAddedLocallyAfterSnapshotIsKept(t *testing.T) {
	snapshotAt := time.Now().Add(-time.Hour)
	tracker := &watchlistRemovalTracker{
		provider: "simkl",
		previous: &models.HistorySyncSnapshot{SyncedAt: snapshotAt},
		prevKeys: map[string]struct{}{"movie:438631": {}},
	}
	tracker.setRemote(map[string]bool{"movie:1": true})
	item := models.WatchlistItem{ID: "tmdb:movie:438631", MediaType: "movie", ExternalIDs: map[string]string{"tmdb": "438631"}, AddedAt: time.Now()}
	if tracker.removedRemotely(item) {
		t.Fatal("an item re-added locally after the snapshot must be kept")
	}
	item.AddedAt = snapshotAt.Add(-time.Minute)
	if !tracker.removedRemotely(item) {
		t.Fatal("an item seen at the snapshot and gone now should be reported removed")
	}
}

func TestWatchlistRemovalOnTraktCascadesToSimkl(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	svc.SetHistorySyncSnapshotStore(newMemoryHistorySnapshotStore())
	seedDune(t, watchlistSvc, "")
	since := time.Now().Add(-time.Hour)

	runTrakt := func(watchlistJSON string) {
		t.Helper()
		installTraktWatchlistStub(t, svc, watchlistJSON)
		tracker, err := svc.newWatchlistRemovalTracker("trakt-task", "trakt", "acc", "profile-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.syncTraktBidirectional(&config.TraktAccount{AccessToken: "token"}, "profile-1", "watchlist", "", traktWatchlistTestSource, "delete", "source_wins", &since, false, tracker); err != nil {
			t.Fatalf("syncTraktBidirectional: %v", err)
		}
	}
	arrival := `{"rank":2,"type":"movie","movie":{"title":"Arrival","year":2016,"ids":{"trakt":2,"tmdb":329865}}}`
	runTrakt(`[` + arrival + `,{"rank":1,"type":"movie","movie":{"title":"Dune","year":2021,"ids":{"trakt":1,"tmdb":438631,"imdb":"tt1160419"}}}]`)
	runTrakt(`[` + arrival + `]`) // Dune removed on Trakt

	// Simkl still has Dune: it must be removed there, not imported back.
	client, stub := installSimklWatchlistStub(t, map[string]string{"movies": simklDunePlanToWatch})
	tracker, err := svc.newWatchlistRemovalTracker("simkl-task", "simkl", "acc", "profile-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.syncSimklWatchlist(client, simklTestAccount, "profile-1", "bidirectional", simklWatchlistTestSource, "delete", &since, false, tracker); err != nil {
		t.Fatalf("syncSimklWatchlist: %v", err)
	}
	for _, item := range func() []models.WatchlistItem { items, _ := watchlistSvc.List("profile-1"); return items }() {
		if item.Name == "Dune" {
			t.Fatalf("Dune was imported back from Simkl after its removal on Trakt")
		}
	}
	if len(stub.removed) != 1 || !strings.Contains(stub.removed[0], `"tmdb":438631`) {
		t.Fatalf("removed = %v, want Dune removed from Simkl", stub.removed)
	}
}
