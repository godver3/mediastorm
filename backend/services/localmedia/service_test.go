package localmedia

import (
	"context"
	"os"
	"testing"
	"time"

	"novastream/models"
	"novastream/utils/parsett"
)

func TestDetectTitleMovie(t *testing.T) {
	got := detectTitle(models.LocalMediaLibraryTypeMovie, "The.Matrix.1999.1080p.BluRay.x264.mkv", nil)
	if got.title != "The Matrix" {
		t.Fatalf("title = %q, want %q", got.title, "The Matrix")
	}
	if got.year != 1999 {
		t.Fatalf("year = %d, want 1999", got.year)
	}
}

func TestDetectTitleEpisode(t *testing.T) {
	got := detectTitle(models.LocalMediaLibraryTypeShow, "Severance.S02E03.Who.Is.Alive.1080p.WEB-DL.mkv", nil)
	if got.title != "Severance" {
		t.Fatalf("title = %q, want %q", got.title, "Severance")
	}
	if got.season != 2 || got.episode != 3 {
		t.Fatalf("season/episode = %d/%d, want 2/3", got.season, got.episode)
	}
}

func TestDetectTitleUsesParsettResult(t *testing.T) {
	got := detectTitle(models.LocalMediaLibraryTypeShow, "ignored.mkv", &parsett.ParsedTitle{
		Title:    "The Simpsons",
		Year:     1989,
		IMDBID:   "tt0096697",
		TMDBID:   456,
		TVDBID:   789,
		Seasons:  []int{1},
		Episodes: []int{2},
	})
	if got.title != "The Simpsons" || got.year != 1989 || got.season != 1 || got.episode != 2 || got.imdbID != "tt0096697" || got.tmdbID != 456 || got.tvdbID != 789 {
		t.Fatalf("got %+v", got)
	}
}

func TestDetectTitleExtractsExternalIDsFromFilename(t *testing.T) {
	got := detectTitle(models.LocalMediaLibraryTypeMovie, "Movie.Name.2024.tmdb12345.tvdb67890.tt1234567.1080p.mkv", nil)
	if got.imdbID != "tt1234567" || got.tmdbID != 12345 || got.tvdbID != 67890 {
		t.Fatalf("got %+v", got)
	}
}

func TestHydrateLocalMediaItemExternalIDs(t *testing.T) {
	item := models.LocalMediaItem{
		Metadata: &models.Title{
			IMDBID: "tt1234567",
			TMDBID: 12345,
			TVDBID: 67890,
		},
	}

	hydrateLocalMediaItemExternalIDs(&item)

	if item.ExternalIDs == nil {
		t.Fatal("ExternalIDs = nil, want populated")
	}
	if item.ExternalIDs.IMDB != "tt1234567" || item.ExternalIDs.TMDB != "12345" || item.ExternalIDs.TVDB != "67890" {
		t.Fatalf("ExternalIDs = %+v", item.ExternalIDs)
	}
}

func TestHydrateLocalMediaItemResultExternalIDs(t *testing.T) {
	result := &models.LocalMediaItemListResult{
		Items: []models.LocalMediaItem{
			{
				Metadata: &models.Title{
					IMDBID: "tt7654321",
					TMDBID: 54321,
				},
			},
		},
	}

	hydrateLocalMediaItemResultExternalIDs(result)

	if result.Items[0].ExternalIDs == nil {
		t.Fatal("ExternalIDs = nil, want populated")
	}
	if result.Items[0].ExternalIDs.IMDB != "tt7654321" || result.Items[0].ExternalIDs.TMDB != "54321" || result.Items[0].ExternalIDs.TVDB != "" {
		t.Fatalf("ExternalIDs = %+v", result.Items[0].ExternalIDs)
	}
}

func TestSimilarityScore(t *testing.T) {
	score := similarityScore("The Matrix", "Matrix")
	if score < 0.8 {
		t.Fatalf("score = %.2f, want >= 0.8", score)
	}
}

type fakeLocalMediaRepo struct {
	library *models.LocalMediaLibrary
	items   map[string]*models.LocalMediaItem
}

func (f *fakeLocalMediaRepo) ListLibraries(ctx context.Context) ([]models.LocalMediaLibrary, error) {
	if f.library == nil {
		return nil, nil
	}
	return []models.LocalMediaLibrary{*f.library}, nil
}

func (f *fakeLocalMediaRepo) GetLibrary(ctx context.Context, id string) (*models.LocalMediaLibrary, error) {
	if f.library == nil || f.library.ID != id {
		return nil, nil
	}
	copy := *f.library
	return &copy, nil
}

func (f *fakeLocalMediaRepo) CreateLibrary(ctx context.Context, library *models.LocalMediaLibrary) error {
	copy := *library
	f.library = &copy
	return nil
}

func (f *fakeLocalMediaRepo) UpdateLibrary(ctx context.Context, library *models.LocalMediaLibrary) error {
	copy := *library
	f.library = &copy
	return nil
}

func (f *fakeLocalMediaRepo) DeleteLibrary(ctx context.Context, id string) error {
	if f.library != nil && f.library.ID == id {
		f.library = nil
	}
	return nil
}

func (f *fakeLocalMediaRepo) ListItemsByLibrary(ctx context.Context, libraryID string, query models.LocalMediaItemListQuery) (*models.LocalMediaItemListResult, error) {
	var items []models.LocalMediaItem
	for _, item := range f.items {
		if item.LibraryID == libraryID {
			items = append(items, *item)
		}
	}
	return &models.LocalMediaItemListResult{
		Items:  items,
		Total:  len(items),
		Limit:  query.Limit,
		Offset: query.Offset,
	}, nil
}

func (f *fakeLocalMediaRepo) ListAllItemsByLibrary(ctx context.Context, libraryID string) ([]models.LocalMediaItem, error) {
	var items []models.LocalMediaItem
	for _, item := range f.items {
		if item.LibraryID == libraryID {
			items = append(items, *item)
		}
	}
	return items, nil
}

func (f *fakeLocalMediaRepo) UpsertItem(ctx context.Context, item *models.LocalMediaItem) error {
	if f.items == nil {
		f.items = make(map[string]*models.LocalMediaItem)
	}
	copy := *item
	f.items[item.RelativePath] = &copy
	return nil
}

func (f *fakeLocalMediaRepo) GetItem(ctx context.Context, id string) (*models.LocalMediaItem, error) {
	for _, item := range f.items {
		if item.ID == id {
			copy := *item
			return &copy, nil
		}
	}
	return nil, nil
}

func (f *fakeLocalMediaRepo) MarkItemsMissingNotSeenInScan(ctx context.Context, libraryID, scanID string, missingSince interface{}) error {
	ts, _ := missingSince.(time.Time)
	for _, item := range f.items {
		if item.LibraryID != libraryID {
			continue
		}
		if item.LastSeenScanID != scanID && !item.IsMissing {
			item.IsMissing = true
			item.MissingSince = &ts
		}
	}
	return nil
}

func (f *fakeLocalMediaRepo) DeleteItem(ctx context.Context, id string) error {
	for path, item := range f.items {
		if item.ID == id {
			delete(f.items, path)
			return nil
		}
	}
	return nil
}

func TestStartScanCompletesAndPersistsSummary(t *testing.T) {
	root := t.TempDir()
	filePath := root + "/Movie.Title.2024.mkv"
	if err := os.WriteFile(filePath, []byte("not-a-real-video"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	repo := &fakeLocalMediaRepo{
		library: &models.LocalMediaLibrary{
			ID:             "lib1",
			Name:           "Movies",
			Type:           models.LocalMediaLibraryTypeMovie,
			RootPath:       root,
			LastScanStatus: models.LocalMediaScanStatusIdle,
			CreatedAt:      time.Now().UTC(),
			UpdatedAt:      time.Now().UTC(),
		},
		items: make(map[string]*models.LocalMediaItem),
	}
	service := &Service{
		repo:        repo,
		ffprobePath: "ffprobe",
		scans:       make(map[string]scanState),
	}

	summary, err := service.StartScan(context.Background(), "lib1")
	if err != nil {
		t.Fatalf("StartScan error: %v", err)
	}
	if summary.Discovered != 1 {
		t.Fatalf("summary.Discovered = %d, want 1", summary.Discovered)
	}
	if repo.library == nil || repo.library.LastScanStatus != models.LocalMediaScanStatusComplete {
		t.Fatalf("LastScanStatus = %v, want %v", repo.library.LastScanStatus, models.LocalMediaScanStatusComplete)
	}
	if repo.library.LastScanFinishedAt == nil {
		t.Fatal("LastScanFinishedAt = nil, want non-nil")
	}
	if len(repo.items) != 1 {
		t.Fatalf("items stored = %d, want 1", len(repo.items))
	}
}

func TestStartScanMarksMissingItemsInsteadOfDeleting(t *testing.T) {
	root := t.TempDir()
	filePath := root + "/Movie.Title.2024.mkv"
	if err := os.WriteFile(filePath, []byte("not-a-real-video"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	now := time.Now().UTC().Add(-time.Hour)
	repo := &fakeLocalMediaRepo{
		library: &models.LocalMediaLibrary{
			ID:             "lib1",
			Name:           "Movies",
			Type:           models.LocalMediaLibraryTypeMovie,
			RootPath:       root,
			LastScanStatus: models.LocalMediaScanStatusIdle,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
		items: map[string]*models.LocalMediaItem{
			"old.mkv": {
				ID:             "old1",
				LibraryID:      "lib1",
				RelativePath:   "old.mkv",
				FilePath:       root + "/old.mkv",
				FileName:       "old.mkv",
				LastSeenScanID: "prior-scan",
				CreatedAt:      now,
				UpdatedAt:      now,
			},
		},
	}
	service := &Service{
		repo:        repo,
		ffprobePath: "ffprobe",
		scans:       make(map[string]scanState),
	}

	_, err := service.StartScan(context.Background(), "lib1")
	if err != nil {
		t.Fatalf("StartScan error: %v", err)
	}

	oldItem := repo.items["old.mkv"]
	if oldItem == nil {
		t.Fatal("old item deleted, want marked missing")
	}
	if !oldItem.IsMissing {
		t.Fatal("old item IsMissing = false, want true")
	}
	if oldItem.MissingSince == nil {
		t.Fatal("old item MissingSince = nil, want non-nil")
	}
}

func TestDeleteItemRequiresMissingState(t *testing.T) {
	repo := &fakeLocalMediaRepo{
		items: map[string]*models.LocalMediaItem{
			"movie.mkv": {
				ID:           "item1",
				LibraryID:    "lib1",
				RelativePath: "movie.mkv",
			},
		},
	}
	service := &Service{repo: repo}

	err := service.DeleteItem(context.Background(), "item1")
	if err == nil {
		t.Fatal("DeleteItem error = nil, want error")
	}
}

func TestDeleteItemDeletesMissingItem(t *testing.T) {
	now := time.Now().UTC()
	repo := &fakeLocalMediaRepo{
		items: map[string]*models.LocalMediaItem{
			"movie.mkv": {
				ID:           "item1",
				LibraryID:    "lib1",
				RelativePath: "movie.mkv",
				IsMissing:    true,
				MissingSince: &now,
			},
		},
	}
	service := &Service{repo: repo}

	if err := service.DeleteItem(context.Background(), "item1"); err != nil {
		t.Fatalf("DeleteItem error: %v", err)
	}
	if len(repo.items) != 0 {
		t.Fatalf("items stored = %d, want 0", len(repo.items))
	}
}
