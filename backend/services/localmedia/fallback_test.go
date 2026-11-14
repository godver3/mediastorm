package localmedia

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"novastream/models"
)

func TestPlaybackCandidatesRequireExactConfirmedEpisode(t *testing.T) {
	base := models.LocalMediaItem{ID: "exact", LibraryID: "library", LibraryType: models.LocalMediaLibraryTypeShow, MatchedTitleID: "tmdb:tv:123", MatchStatus: models.LocalMediaMatchStatusMatched, SeasonNumber: 2, EpisodeNumber: 3}
	cases := []struct {
		name   string
		change func(*models.LocalMediaItem)
		allow  bool
		want   int
	}{
		{"confirmed", func(*models.LocalMediaItem) {}, true, 1},
		{"manual correction", func(i *models.LocalMediaItem) { i.MatchStatus = models.LocalMediaMatchStatusManual }, true, 1},
		{"wrong title", func(i *models.LocalMediaItem) { i.MatchedTitleID = "tmdb:tv:456" }, true, 0},
		{"wrong season", func(i *models.LocalMediaItem) { i.SeasonNumber = 1 }, true, 0},
		{"wrong episode", func(i *models.LocalMediaItem) { i.EpisodeNumber = 4 }, true, 0},
		{"low confidence", func(i *models.LocalMediaItem) { i.MatchStatus = models.LocalMediaMatchStatusLowConfidence }, true, 0},
		{"unmatched", func(i *models.LocalMediaItem) { i.MatchStatus = models.LocalMediaMatchStatusUnmatched }, true, 0},
		{"missing", func(i *models.LocalMediaItem) { i.IsMissing = true }, true, 0},
		{"denied library", func(*models.LocalMediaItem) {}, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := base
			tc.change(&item)
			s := &Service{repo: &fakeLocalMediaRepo{library: &models.LocalMediaLibrary{ID: "library", Type: models.LocalMediaLibraryTypeShow}, items: map[string]*models.LocalMediaItem{item.ID: &item}}}
			items, err := s.FindPlaybackCandidates(t.Context(), models.LocalMediaMatchQuery{MediaType: "series", TitleID: "tmdb:tv:123"}, &models.EpisodeReference{SeasonNumber: 2, EpisodeNumber: 3}, func(string) bool { return tc.allow })
			if err != nil || len(items) != tc.want {
				t.Fatalf("got %d candidates, error %v; want %d", len(items), err, tc.want)
			}
		})
	}
}

func TestPlaybackCandidatesNeverUseNameOnlyOrFilesystem(t *testing.T) {
	item := &models.LocalMediaItem{ID: "movie", LibraryID: "library", LibraryType: models.LocalMediaLibraryTypeMovie, MatchedTitleID: "tmdb:movie:123", MatchedName: "Synthetic Movie", MatchStatus: models.LocalMediaMatchStatusMatched, FilePath: "/nonexistent/synthetic/movie.mkv"}
	s := &Service{repo: &fakeLocalMediaRepo{library: &models.LocalMediaLibrary{ID: "library", Type: models.LocalMediaLibraryTypeMovie}, items: map[string]*models.LocalMediaItem{item.ID: item}}}
	for _, query := range []models.LocalMediaMatchQuery{{MediaType: "movie", Title: "Synthetic Movie"}, {MediaType: "movie", TitleID: "tmdb:movie:123"}} {
		items, err := s.FindPlaybackCandidates(t.Context(), query, nil, func(string) bool { return true })
		want := 0
		if query.TitleID != "" {
			want = 1
		}
		if err != nil || len(items) != want {
			t.Fatalf("query %+v: got %d, %v; want %d", query, len(items), err, want)
		}
	}
}

func TestFallbackItemValidatesCurrentFileAndRoot(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "synthetic.mkv")
	if err := os.WriteFile(valid, []byte("synthetic media fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path       string
		missing, wantErr bool
	}{
		{"readable", valid, false, false}, {"deleted", filepath.Join(root, "deleted.mkv"), false, true}, {"directory", root, false, true}, {"outside root", filepath.Join(t.TempDir(), "outside.mkv"), false, true}, {"missing catalog", valid, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := &models.LocalMediaItem{ID: "item", LibraryID: "library", FilePath: tc.path, IsMissing: tc.missing}
			s := &Service{repo: &fakeLocalMediaRepo{library: &models.LocalMediaLibrary{ID: "library", RootPath: root}, items: map[string]*models.LocalMediaItem{"item": item}}}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			got, err := s.GetFallbackItem(ctx, "item")
			if (err != nil) != tc.wantErr {
				t.Fatalf("item=%v error=%v", got, err)
			}
			if err == nil && got.SizeBytes != int64(len("synthetic media fixture")) {
				t.Fatalf("incorrect current file size: %d", got.SizeBytes)
			}
		})
	}
}

func TestPlaybackCandidatesRejectConflictingExternalIDs(t *testing.T) {
	item := models.LocalMediaItem{MatchedTitleID: "tmdb:movie:123", MatchStatus: models.LocalMediaMatchStatusMatched, ExternalIDs: &models.LocalMediaExternalIDs{IMDB: "tt0000123"}}
	if fallbackIdentityMatches(item, models.LocalMediaMatchQuery{TitleID: "tmdb:movie:123", IMDBID: "tt0000456"}) {
		t.Fatal("accepted conflicting identities")
	}
	if !fallbackIdentityMatches(item, models.LocalMediaMatchQuery{TitleID: "tmdb:movie:123", IMDBID: "tt0000123"}) {
		t.Fatal("rejected matching identities")
	}
}

type blockedFallbackRepository struct {
	*fakeLocalMediaRepo
	entered, release, finished chan struct{}
}

func (r *blockedFallbackRepository) GetItem(ctx context.Context, id string) (*models.LocalMediaItem, error) {
	close(r.entered)
	<-r.release
	defer close(r.finished)
	return r.fakeLocalMediaRepo.GetItem(ctx, id)
}

func TestFallbackItemDoesNotWaitForBlockedFilesystemLookup(t *testing.T) {
	repo := &blockedFallbackRepository{fakeLocalMediaRepo: &fakeLocalMediaRepo{}, entered: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	s := &Service{repo: repo}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := s.GetFallbackItem(ctx, "item")
	close(repo.release)
	<-repo.entered
	<-repo.finished
	if err != context.DeadlineExceeded || time.Since(started) > time.Second {
		t.Fatalf("blocked check: %v", err)
	}
}
