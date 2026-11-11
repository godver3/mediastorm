package history

import (
	"testing"
	"time"

	"novastream/models"
)

func watchedPtr(v bool) *bool { return &v }

func movieUpdate(id string) models.WatchHistoryUpdate {
	return models.WatchHistoryUpdate{MediaType: "movie", ItemID: "tmdb:movie:" + id, Name: "Film " + id, Watched: watchedPtr(true),
		ExternalIDs: map[string]string{"tmdb": id}}
}

func historySource(t *testing.T, svc *Service, userID, id string) string {
	t.Helper()
	item, err := svc.GetWatchHistoryItem(userID, "movie", "tmdb:movie:"+id)
	if err != nil || item == nil {
		t.Fatalf("GetWatchHistoryItem(%s) = %v, %v", id, item, err)
	}
	return item.Source
}

func TestWatchHistorySourceByWritePath(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	const user = "user-watch-source"

	if _, err := svc.BulkUpdateWatchHistory(user, []models.WatchHistoryUpdate{movieUpdate("1")}); err != nil {
		t.Fatalf("bulk: %v", err)
	}
	if _, err := svc.UpdateWatchHistory(user, movieUpdate("2")); err != nil {
		t.Fatalf("update: %v", err)
	}
	imported := movieUpdate("3")
	imported.WatchedAt = time.Now().Add(-time.Hour)
	if _, err := svc.ImportWatchHistory(user, []models.WatchHistoryUpdate{imported}); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := svc.UpdatePlaybackProgress(user, models.PlaybackProgressUpdate{
		MediaType: "movie", ItemID: "tmdb:movie:4", MovieName: "Film 4", Position: 5900, Duration: 6000,
		ExternalIDs: map[string]string{"tmdb": "4"},
	}); err != nil {
		t.Fatalf("progress: %v", err)
	}

	for id, want := range map[string]string{
		"1": models.WatchSourceMarked,
		"2": models.WatchSourceMarked,
		"3": models.WatchSourceImported,
		"4": models.WatchSourcePlayed,
	} {
		if got := historySource(t, svc, user, id); got != want {
			t.Errorf("movie %s source = %q, want %q", id, got, want)
		}
	}

	// Marking a played item again keeps it played.
	if _, err := svc.BulkUpdateWatchHistory(user, []models.WatchHistoryUpdate{movieUpdate("4")}); err != nil {
		t.Fatalf("re-mark: %v", err)
	}
	if got := historySource(t, svc, user, "4"); got != models.WatchSourcePlayed {
		t.Errorf("re-marked played movie source = %q, want played", got)
	}

	// Unwatching then marking again makes it a mark.
	unwatch := movieUpdate("4")
	unwatch.Watched = watchedPtr(false)
	if _, err := svc.UpdateWatchHistory(user, unwatch); err != nil {
		t.Fatalf("unwatch: %v", err)
	}
	if _, err := svc.UpdateWatchHistory(user, movieUpdate("4")); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if got := historySource(t, svc, user, "4"); got != models.WatchSourceMarked {
		t.Errorf("unwatched then marked source = %q, want marked", got)
	}

	// Real-only listing drops the marks.
	page, err := svc.ListWatchHistoryPaginated(user, 1, 50, "", true)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.Total != 1 || page.Items[0].ItemID != "tmdb:movie:3" {
		t.Fatalf("real-only listing = %+v, want only the imported movie", page.Items)
	}
	all, err := svc.ListWatchHistoryPaginated(user, 1, 50, "", false)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if all.Total != 4 {
		t.Fatalf("full listing total = %d, want 4", all.Total)
	}
}

func TestMarkingPlayedThenUpgradesOnPlayback(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	const user = "user-watch-source-upgrade"
	if _, err := svc.UpdateWatchHistory(user, movieUpdate("9")); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if _, err := svc.UpdatePlaybackProgress(user, models.PlaybackProgressUpdate{
		MediaType: "movie", ItemID: "tmdb:movie:9", MovieName: "Film 9", Position: 5900, Duration: 6000,
		ExternalIDs: map[string]string{"tmdb": "9"},
	}); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if got := historySource(t, svc, user, "9"); got != models.WatchSourcePlayed {
		t.Fatalf("source after playing a marked movie = %q, want played", got)
	}
}

func TestIsRealWatchLegacyRows(t *testing.T) {
	cases := []struct {
		item models.WatchHistoryItem
		want bool
	}{
		{models.WatchHistoryItem{Source: models.WatchSourcePlayed}, true},
		{models.WatchHistoryItem{Source: models.WatchSourceImported}, true},
		{models.WatchHistoryItem{Source: models.WatchSourceMarked, WatchedSeconds: 600}, true},
		{models.WatchHistoryItem{Source: models.WatchSourceMarked}, false},
		{models.WatchHistoryItem{WatchedSeconds: 600}, true},
		{models.WatchHistoryItem{}, true},
	}
	for _, tc := range cases {
		if got := tc.item.IsRealWatch(); got != tc.want {
			t.Errorf("IsRealWatch(%+v) = %v, want %v", tc.item, got, tc.want)
		}
	}
}

func TestDedupeKeepsStrongestWatchSource(t *testing.T) {
	// The same episode under absolute (E1167) and seasonal (E12) numbering:
	// the seasonal row survives, but the absolute one was the played one.
	seasonal := models.WatchHistoryItem{ID: "a", MediaType: "episode", Watched: true, SeriesName: "Show", Name: "Pilot",
		SeasonNumber: 23, EpisodeNumber: 12, Source: models.WatchSourceMarked}
	absolute := seasonal
	absolute.ID, absolute.EpisodeNumber, absolute.Source = "b", 1167, models.WatchSourcePlayed

	out := DedupeWatchHistory([]models.WatchHistoryItem{absolute, seasonal})
	if len(out) != 1 || out[0].EpisodeNumber != 12 {
		t.Fatalf("dedupe = %+v, want the seasonal row only", out)
	}
	if out[0].Source != models.WatchSourcePlayed {
		t.Fatalf("deduped source = %q, want played", out[0].Source)
	}
}

func TestMarkingAfterPartialPlaybackCountsAsRealWatch(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	const user = "user-watch-source-partial"
	// Watched for a while, stopped short of the 90% auto-watch threshold, then marked.
	for _, position := range []float64{30, 90} {
		if _, err := svc.UpdatePlaybackProgress(user, models.PlaybackProgressUpdate{
			MediaType: "movie", ItemID: "tmdb:movie:7", MovieName: "Film 7", Position: position, Duration: 6000,
			ExternalIDs: map[string]string{"tmdb": "7"},
		}); err != nil {
			t.Fatalf("progress: %v", err)
		}
	}
	if progress, _ := svc.GetPlaybackProgress(user, "movie", "tmdb:movie:7"); progress == nil || progress.WatchedSeconds <= 0 {
		t.Fatalf("progress recorded no watched seconds: %+v", progress)
	}
	if _, err := svc.BulkUpdateWatchHistory(user, []models.WatchHistoryUpdate{movieUpdate("7")}); err != nil {
		t.Fatalf("mark: %v", err)
	}
	item, err := svc.GetWatchHistoryItem(user, "movie", "tmdb:movie:7")
	if err != nil || item == nil {
		t.Fatalf("GetWatchHistoryItem = %v, %v", item, err)
	}
	if item.Source != models.WatchSourceMarked || item.WatchedSeconds <= 0 || !item.IsRealWatch() {
		t.Fatalf("marked after playback = source %q seconds %v real %v; want marked, seconds kept, real",
			item.Source, item.WatchedSeconds, item.IsRealWatch())
	}
	page, err := svc.ListWatchHistoryPaginated(user, 1, 50, "", true)
	if err != nil || page.Total != 1 {
		t.Fatalf("real-only listing total = %v (err %v), want 1", page, err)
	}
}
