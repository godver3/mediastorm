package datastore

import (
	"testing"
	"time"

	"novastream/models"
)

func TestReconcileWatchlistIdentityItemsMergesProviderAliases(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	items := []models.WatchlistItem{
		{
			ID:          "tvdb:movie:344109",
			MediaType:   "movie",
			Name:        "Zootopia",
			AddedAt:     now,
			ExternalIDs: map[string]string{"tvdb": "344109", "tmdb": "1084242", "plex": "plex-1"},
			SyncSource:  "plex:task",
		},
		{
			ID:          "tmdb:movie:1084242",
			MediaType:   "movie",
			Name:        "Zootopia",
			AddedAt:     now.Add(time.Hour),
			ExternalIDs: map[string]string{"tmdb": "1084242", "tvdb": "344109"},
		},
	}

	reconciled, stats := reconcileWatchlistIdentityItems(items)
	if len(reconciled) != 1 {
		t.Fatalf("expected 1 reconciled item, got %d: %+v", len(reconciled), reconciled)
	}
	item := reconciled["movie:tmdb:movie:1084242"]
	if item.ID != "tmdb:movie:1084242" {
		t.Fatalf("canonical ID = %q", item.ID)
	}
	if item.ExternalIDs["plex"] != "plex-1" {
		t.Fatalf("expected plex external ID to survive, got %+v", item.ExternalIDs)
	}
	if stats.merged != 1 || stats.rekeyed != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestReconcileWatchHistoryIdentityItemsNewestStateWins(t *testing.T) {
	older := time.Date(2026, 6, 11, 1, 2, 55, 0, time.UTC)
	newer := time.Date(2026, 6, 11, 1, 19, 45, 0, time.UTC)
	items := []models.WatchHistoryItem{
		{
			ID:            "episode:tvdb:series:81797:s23e10",
			MediaType:     "episode",
			ItemID:        "tvdb:series:81797:s23e10",
			Name:          "One Piece",
			Watched:       true,
			WatchedAt:     older,
			UpdatedAt:     older,
			SeriesID:      "tvdb:series:81797",
			SeasonNumber:  23,
			EpisodeNumber: 10,
			ExternalIDs:   map[string]string{"imdb": "tt0388629", "tmdb": "37854", "tvdb": "81797", "episodeTvdb": "11700062"},
		},
		{
			ID:            "episode:tmdb:tv:37854:s23e10",
			MediaType:     "episode",
			ItemID:        "tmdb:tv:37854:s23e10",
			Name:          "One Piece",
			Watched:       false,
			WatchedAt:     older,
			UpdatedAt:     newer,
			SeriesID:      "tmdb:tv:37854",
			SeasonNumber:  23,
			EpisodeNumber: 10,
			ExternalIDs:   map[string]string{"imdb": "tt0388629", "tmdb": "37854", "tvdb": "81797", "episodeTvdb": "11700062"},
		},
	}

	reconciled, stats := reconcileWatchHistoryIdentityItems(items)
	if len(reconciled) != 1 {
		t.Fatalf("expected 1 reconciled item, got %d: %+v", len(reconciled), reconciled)
	}
	item := reconciled["episode:tmdb:tv:37854:s23e10"]
	if item.ItemID != "tmdb:tv:37854:s23e10" {
		t.Fatalf("canonical item ID = %q", item.ItemID)
	}
	if item.Watched {
		t.Fatalf("expected newest unwatched state to win: %+v", item)
	}
	if stats.merged != 1 || stats.rekeyed != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestReconcileWatchHistoryIdentityItemsRekeysContradictoryEpisodeProviderIDs(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	items := []models.WatchHistoryItem{
		{
			ID:            "episode:tmdb:tv:4629:s02e09",
			MediaType:     "episode",
			ItemID:        "tmdb:tv:4629:s02e09",
			Name:          "9:00 P.M.",
			SeriesName:    "The Pitt",
			Watched:       true,
			WatchedAt:     now,
			UpdatedAt:     now,
			SeriesID:      "tmdb:tv:4629",
			SeasonNumber:  2,
			EpisodeNumber: 9,
			ExternalIDs: map[string]string{
				"imdb":        "tt31938062",
				"tmdb":        "250307",
				"tvdb":        "448176",
				"titleId":     "tmdb:tv:4629",
				"episodeTmdb": "6768687",
			},
		},
	}

	reconciled, stats := reconcileWatchHistoryIdentityItems(items)
	if _, exists := reconciled["episode:tmdb:tv:4629:s02e09"]; exists {
		t.Fatalf("expected stale key to be removed: %+v", reconciled)
	}
	item, exists := reconciled["episode:tmdb:tv:250307:s02e09"]
	if !exists {
		t.Fatalf("expected provider canonical key to be present: %+v", reconciled)
	}
	if item.SeriesID != "tmdb:tv:250307" || item.ItemID != "tmdb:tv:250307:s02e09" {
		t.Fatalf("expected provider canonical episode, got seriesID=%q itemID=%q", item.SeriesID, item.ItemID)
	}
	if !item.Watched {
		t.Fatal("expected watched state to survive rekey")
	}
	if stats.rekeyed != 1 {
		t.Fatalf("expected one rekey, got stats %+v", stats)
	}
}

func TestReconcilePlaybackProgressIdentityItemsClearsWatchedProgress(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	watchedItems := []models.WatchHistoryItem{normalizeWatchHistoryIdentityItem(models.WatchHistoryItem{
		ID:          "movie:tmdb:movie:1084242",
		MediaType:   "movie",
		ItemID:      "tmdb:movie:1084242",
		Watched:     true,
		WatchedAt:   now,
		UpdatedAt:   now,
		ExternalIDs: map[string]string{"tmdb": "1084242", "tvdb": "344109"},
	})}
	progressItems := []models.PlaybackProgress{
		{
			ID:             "movie:tvdb:movie:344109",
			MediaType:      "movie",
			ItemID:         "tvdb:movie:344109",
			PercentWatched: 40,
			UpdatedAt:      now.Add(time.Hour),
			ExternalIDs:    map[string]string{"tmdb": "1084242", "tvdb": "344109"},
		},
		{
			ID:                         "movie:tmdb:movie:1084242",
			MediaType:                  "movie",
			ItemID:                     "tmdb:movie:1084242",
			HiddenFromContinueWatching: true,
			UpdatedAt:                  now.Add(2 * time.Hour),
			ExternalIDs:                map[string]string{"tmdb": "1084242", "tvdb": "344109"},
		},
	}

	reconciled, stats := reconcilePlaybackProgressIdentityItems(progressItems, watchedItems)
	if len(reconciled) != 1 {
		t.Fatalf("expected only hidden marker to remain, got %d: %+v", len(reconciled), reconciled)
	}
	item := reconciled["movie:tmdb:movie:1084242"]
	if !item.HiddenFromContinueWatching {
		t.Fatalf("expected hidden marker to survive: %+v", item)
	}
	if stats.clearedWatched != 1 {
		t.Fatalf("expected one watched progress clear, got stats %+v", stats)
	}
}
