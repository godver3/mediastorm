package localmedia

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
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

func TestDetectTitleDoesNotTreatHashDigitsAsEpisode(t *testing.T) {
	got := detectTitle(
		models.LocalMediaLibraryTypeShow,
		"b4d85002f15f79a2e435417c73366291d1f23e714663b3ca1971e48058650181.mkv",
		nil,
	)
	if got.episode != 0 {
		t.Fatalf("episode = %d, want 0", got.episode)
	}
}

func TestDetectTitleBoundsParsedSeasonEpisode(t *testing.T) {
	got := detectTitle(models.LocalMediaLibraryTypeShow, "ignored.mkv", &parsett.ParsedTitle{
		Title:    "Game Changer",
		Seasons:  []int{6},
		Episodes: []int{48058650181},
	})
	if got.season != 6 {
		t.Fatalf("season = %d, want 6", got.season)
	}
	if got.episode != 0 {
		t.Fatalf("episode = %d, want 0", got.episode)
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
	// mu guards the fields: StartScan works in a background goroutine.
	mu      sync.Mutex
	library *models.LocalMediaLibrary
	items   map[string]*models.LocalMediaItem
}

func (f *fakeLocalMediaRepo) scanStatus() models.LocalMediaScanStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.library.LastScanStatus
}

func (f *fakeLocalMediaRepo) itemCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.items)
}

func (f *fakeLocalMediaRepo) ListLibraries(ctx context.Context) ([]models.LocalMediaLibrary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.library == nil {
		return nil, nil
	}
	return []models.LocalMediaLibrary{*f.library}, nil
}

func (f *fakeLocalMediaRepo) GetLibrary(ctx context.Context, id string) (*models.LocalMediaLibrary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.library == nil || f.library.ID != id {
		return nil, nil
	}
	copy := *f.library
	return &copy, nil
}

func (f *fakeLocalMediaRepo) CreateLibrary(ctx context.Context, library *models.LocalMediaLibrary) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	copy := *library
	f.library = &copy
	return nil
}

func (f *fakeLocalMediaRepo) UpdateLibrary(ctx context.Context, library *models.LocalMediaLibrary) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	copy := *library
	f.library = &copy
	return nil
}

func (f *fakeLocalMediaRepo) DeleteLibrary(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.library != nil && f.library.ID == id {
		f.library = nil
	}
	return nil
}

func (f *fakeLocalMediaRepo) ListItemsByLibrary(ctx context.Context, libraryID string, query models.LocalMediaItemListQuery) (*models.LocalMediaItemListResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	f.mu.Lock()
	defer f.mu.Unlock()
	var items []models.LocalMediaItem
	for _, item := range f.items {
		if item.LibraryID == libraryID {
			items = append(items, *item)
		}
	}
	return items, nil
}

// ListItemsForTitle returns every item of the library: the service filters.
func (f *fakeLocalMediaRepo) ListItemsForTitle(ctx context.Context, libraryID, _, _, _, _ string, season, episode int) ([]models.LocalMediaItem, error) {
	items, err := f.ListAllItemsByLibrary(ctx, libraryID)
	if err != nil || episode <= 0 {
		return items, err
	}
	var out []models.LocalMediaItem
	for _, item := range items {
		if item.SeasonNumber == season && item.EpisodeNumber == episode {
			out = append(out, item)
		}
	}
	return out, nil
}

func (f *fakeLocalMediaRepo) UpsertItem(ctx context.Context, item *models.LocalMediaItem) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.items == nil {
		f.items = make(map[string]*models.LocalMediaItem)
	}
	copy := *item
	f.items[item.RelativePath] = &copy
	return nil
}

func (f *fakeLocalMediaRepo) GetItem(ctx context.Context, id string) (*models.LocalMediaItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, item := range f.items {
		if item.ID == id {
			copy := *item
			return &copy, nil
		}
	}
	return nil, nil
}

func (f *fakeLocalMediaRepo) DeleteItemsNotSeenInScan(ctx context.Context, libraryID, scanID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for path, item := range f.items {
		if item.LibraryID != libraryID {
			continue
		}
		if item.LastSeenScanID != scanID {
			delete(f.items, path)
		}
	}
	return nil
}

func (f *fakeLocalMediaRepo) DeleteItem(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
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

	summary, err := service.RunScan(context.Background(), "lib1")
	if err != nil {
		t.Fatalf("RunScan error: %v", err)
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

func TestMultipleLibraryRootsKeepSameNamedFilesDistinct(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	for _, root := range []string{first, second} {
		if err := os.WriteFile(filepath.Join(root, "Same.Movie.2024.mkv"), []byte("video"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	repo := &fakeLocalMediaRepo{library: &models.LocalMediaLibrary{
		ID: "multi", Name: "Movies", Type: models.LocalMediaLibraryTypeMovie,
		RootPath: first, RootPaths: []string{first, second}, CreatedAt: now, UpdatedAt: now,
	}, items: make(map[string]*models.LocalMediaItem)}
	service := &Service{repo: repo, scans: make(map[string]scanState)}
	for i := 0; i < 2; i++ {
		summary, err := service.RunScan(context.Background(), "multi")
		if err != nil {
			t.Fatal(err)
		}
		if summary.Discovered != 2 || len(repo.items) != 2 {
			t.Fatalf("scan %d: discovered %d, stored %d", i, summary.Discovered, len(repo.items))
		}
	}
	updated, err := service.UpdateLibrary(context.Background(), "multi", models.LocalMediaLibraryCreateInput{
		Name: "Movies", Type: models.LocalMediaLibraryTypeMovie, RootPaths: []string{second, first},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.RootPath != first {
		t.Fatalf("primary root changed to %q", updated.RootPath)
	}
	if _, err := service.RunScan(context.Background(), "multi"); err != nil {
		t.Fatal(err)
	}
	if len(repo.items) != 2 {
		t.Fatalf("items after reordering roots = %d", len(repo.items))
	}
	if _, ok := repo.items["Same.Movie.2024.mkv"]; !ok {
		t.Fatal("original root item path changed")
	}
	for _, item := range repo.items {
		if !pathWithinLibraryRoots(*repo.library, item.FilePath) {
			t.Fatalf("item outside roots: %s", item.FilePath)
		}
	}
}

func TestMultipleLibraryRootsRejectOverlap(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, err := validateLocalMediaLibraryInput(models.LocalMediaLibraryCreateInput{
		Name: "Movies", Type: models.LocalMediaLibraryTypeMovie, RootPaths: []string{root, child},
	})
	if err == nil {
		t.Fatal("expected overlapping roots to be rejected")
	}
}

func TestStartScanAcceptsBackgroundScanWithoutBlocking(t *testing.T) {
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
	if !summary.Async {
		t.Fatal("summary.Async = false, want true")
	}
	if summary.Status != string(models.LocalMediaScanStatusScanning) {
		t.Fatalf("summary.Status = %q, want %q", summary.Status, models.LocalMediaScanStatusScanning)
	}
	if repo.scanStatus() != models.LocalMediaScanStatusScanning &&
		repo.scanStatus() != models.LocalMediaScanStatusComplete {
		t.Fatalf("LastScanStatus immediately after StartScan = %v, want scanning or complete", repo.scanStatus())
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if repo.scanStatus() == models.LocalMediaScanStatusComplete {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if repo.scanStatus() != models.LocalMediaScanStatusComplete {
		t.Fatalf("LastScanStatus = %v after wait, want %v", repo.scanStatus(), models.LocalMediaScanStatusComplete)
	}
	if repo.itemCount() != 1 {
		t.Fatalf("items stored = %d, want 1", repo.itemCount())
	}
}

func TestStartScanRejectsWhenAlreadyInProgress(t *testing.T) {
	repo := &fakeLocalMediaRepo{
		library: &models.LocalMediaLibrary{
			ID:             "lib1",
			Name:           "Movies",
			Type:           models.LocalMediaLibraryTypeMovie,
			RootPath:       t.TempDir(),
			LastScanStatus: models.LocalMediaScanStatusScanning,
			CreatedAt:      time.Now().UTC(),
			UpdatedAt:      time.Now().UTC(),
		},
	}
	service := &Service{
		repo:  repo,
		scans: map[string]scanState{"lib1": {inProgress: true, startedAt: time.Now().UTC()}},
	}
	if _, err := service.StartScan(context.Background(), "lib1"); !errors.Is(err, ErrLibraryScanning) {
		t.Fatalf("StartScan error = %v, want ErrLibraryScanning", err)
	}
	if _, err := service.RunScan(context.Background(), "lib1"); !errors.Is(err, ErrLibraryScanning) {
		t.Fatalf("RunScan error = %v, want ErrLibraryScanning", err)
	}
}

func TestStartScanDeletesItemsNotSeenInScan(t *testing.T) {
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
			"already-missing.mkv": {
				ID:             "old2",
				LibraryID:      "lib1",
				RelativePath:   "already-missing.mkv",
				FilePath:       root + "/already-missing.mkv",
				FileName:       "already-missing.mkv",
				LastSeenScanID: "prior-scan",
				IsMissing:      true,
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

	_, err := service.RunScan(context.Background(), "lib1")
	if err != nil {
		t.Fatalf("RunScan error: %v", err)
	}

	if _, ok := repo.items["old.mkv"]; ok {
		t.Fatal("old item still present, want deleted after scan")
	}
	if _, ok := repo.items["already-missing.mkv"]; ok {
		t.Fatal("previously missing item still present, want deleted after scan")
	}
	if len(repo.items) != 1 {
		t.Fatalf("items stored = %d, want 1 (only the file still on disk)", len(repo.items))
	}
	if _, ok := repo.items["Movie.Title.2024.mkv"]; !ok {
		t.Fatal("current on-disk file missing from items after scan")
	}
}

func TestGetItemReturnsNotFoundForMissingItem(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()

	repo := &fakeLocalMediaRepo{
		library: &models.LocalMediaLibrary{
			ID:        "lib1",
			Name:      "Movies",
			Type:      models.LocalMediaLibraryTypeMovie,
			RootPath:  root,
			CreatedAt: now,
			UpdatedAt: now,
		},
		items: map[string]*models.LocalMediaItem{
			"Movie.Title.2024.mkv": {
				ID:           "item1",
				LibraryID:    "lib1",
				FilePath:     filepath.Join(root, "Movie.Title.2024.mkv"),
				FileName:     "Movie.Title.2024.mkv",
				RelativePath: "Movie.Title.2024.mkv",
				IsMissing:    true,
				CreatedAt:    now,
				UpdatedAt:    now,
			},
		},
	}
	service := &Service{repo: repo}

	item, err := service.GetItem(context.Background(), "item1")
	if err == nil || !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("GetItem() err = %v, want %v", err, ErrItemNotFound)
	}
	if item != nil {
		t.Fatalf("GetItem() item = %+v, want nil", item)
	}
}

func TestUpdateLibraryPersistsFilterSettings(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
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
	}
	service := &Service{repo: repo}

	updated, err := service.UpdateLibrary(context.Background(), "lib1", models.LocalMediaLibraryCreateInput{
		Name:             "Movies HD",
		Type:             models.LocalMediaLibraryTypeMovie,
		RootPath:         root,
		FilterOutTerms:   []string{"Trailer", " Featurette ", "trailer"},
		MinFileSizeBytes: 512 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("UpdateLibrary error: %v", err)
	}

	if updated.Name != "Movies HD" {
		t.Fatalf("updated.Name = %q, want %q", updated.Name, "Movies HD")
	}
	if len(updated.FilterOutTerms) != 2 {
		t.Fatalf("updated.FilterOutTerms = %#v, want 2 unique entries", updated.FilterOutTerms)
	}
	if updated.MinFileSizeBytes != 512*1024*1024 {
		t.Fatalf("updated.MinFileSizeBytes = %d", updated.MinFileSizeBytes)
	}
}

func TestStartScanDeletesItemsExcludedByFilterTerms(t *testing.T) {
	root := t.TempDir()
	mainPath := root + "/Movie.Title.2024.mkv"
	trailerPath := root + "/Movie.Title.2024.Trailer.mkv"
	if err := os.WriteFile(mainPath, []byte("main-video"), 0o644); err != nil {
		t.Fatalf("write main file: %v", err)
	}
	if err := os.WriteFile(trailerPath, []byte("trailer-video"), 0o644); err != nil {
		t.Fatalf("write trailer file: %v", err)
	}

	now := time.Now().UTC().Add(-time.Hour)
	repo := &fakeLocalMediaRepo{
		library: &models.LocalMediaLibrary{
			ID:               "lib1",
			Name:             "Movies",
			Type:             models.LocalMediaLibraryTypeMovie,
			RootPath:         root,
			FilterOutTerms:   []string{"Trailer"},
			LastScanStatus:   models.LocalMediaScanStatusIdle,
			CreatedAt:        now,
			UpdatedAt:        now,
			MinFileSizeBytes: 0,
		},
		items: map[string]*models.LocalMediaItem{
			"Movie.Title.2024.Trailer.mkv": {
				ID:           "trailer1",
				LibraryID:    "lib1",
				RelativePath: "Movie.Title.2024.Trailer.mkv",
				FilePath:     trailerPath,
				FileName:     "Movie.Title.2024.Trailer.mkv",
				CreatedAt:    now,
				UpdatedAt:    now,
			},
		},
	}
	service := &Service{
		repo:        repo,
		ffprobePath: "ffprobe",
		scans:       make(map[string]scanState),
	}

	summary, err := service.RunScan(context.Background(), "lib1")
	if err != nil {
		t.Fatalf("RunScan error: %v", err)
	}
	if summary.Discovered != 1 {
		t.Fatalf("summary.Discovered = %d, want 1", summary.Discovered)
	}
	if _, exists := repo.items["Movie.Title.2024.Trailer.mkv"]; exists {
		t.Fatal("excluded trailer item still present after scan")
	}
	if _, exists := repo.items["Movie.Title.2024.mkv"]; !exists {
		t.Fatal("main movie item not stored after scan")
	}
}

func TestShouldIncludeLocalMediaFileRespectsMinimumSize(t *testing.T) {
	library := models.LocalMediaLibrary{
		MinFileSizeBytes: 100,
	}

	include, reason := shouldIncludeLocalMediaFile(library, "Show/S01E01.mkv", 99)
	if include {
		t.Fatal("include = true, want false")
	}
	if reason != "min_size" {
		t.Fatalf("reason = %q, want %q", reason, "min_size")
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
		library: &models.LocalMediaLibrary{
			ID:                 "lib1",
			LastScanStatus:     models.LocalMediaScanStatusComplete,
			LastScanDiscovered: 1,
		},
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
	if repo.library.LastScanDiscovered != 0 {
		t.Fatalf("LastScanDiscovered = %d, want 0", repo.library.LastScanDiscovered)
	}
	if repo.library.LastScanMatched != 0 {
		t.Fatalf("LastScanMatched = %d, want 0", repo.library.LastScanMatched)
	}
	if repo.library.LastScanLowConf != 0 {
		t.Fatalf("LastScanLowConf = %d, want 0", repo.library.LastScanLowConf)
	}
}

func TestListItemsExcludesMissingByDefault(t *testing.T) {
	now := time.Now().UTC()
	repo := &fakeLocalMediaRepo{
		library: &models.LocalMediaLibrary{
			ID:        "lib1",
			Name:      "Movies",
			Type:      models.LocalMediaLibraryTypeMovie,
			RootPath:  t.TempDir(),
			CreatedAt: now,
			UpdatedAt: now,
		},
		items: map[string]*models.LocalMediaItem{
			"available.mkv": {
				ID:           "item1",
				LibraryID:    "lib1",
				RelativePath: "available.mkv",
				FileName:     "available.mkv",
				UpdatedAt:    now,
				CreatedAt:    now,
			},
			"missing.mkv": {
				ID:           "item2",
				LibraryID:    "lib1",
				RelativePath: "missing.mkv",
				FileName:     "missing.mkv",
				IsMissing:    true,
				UpdatedAt:    now,
				CreatedAt:    now,
			},
		},
	}
	service := &Service{repo: repo}

	result, err := service.ListItems(context.Background(), "lib1", models.LocalMediaItemListQuery{})
	if err != nil {
		t.Fatalf("ListItems error: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("result.Total = %d, want 1", result.Total)
	}
	if len(result.Items) != 1 {
		t.Fatalf("len(result.Items) = %d, want 1", len(result.Items))
	}
	if result.Items[0].ID != "item1" {
		t.Fatalf("result.Items[0].ID = %q, want %q", result.Items[0].ID, "item1")
	}
}

func TestListGroupsExcludesMissingByDefault(t *testing.T) {
	now := time.Now().UTC()
	repo := &fakeLocalMediaRepo{
		library: &models.LocalMediaLibrary{
			ID:        "lib1",
			Name:      "Movies",
			Type:      models.LocalMediaLibraryTypeMovie,
			RootPath:  t.TempDir(),
			CreatedAt: now,
			UpdatedAt: now,
		},
		items: map[string]*models.LocalMediaItem{
			"available.mkv": {
				ID:             "item1",
				LibraryID:      "lib1",
				RelativePath:   "available.mkv",
				FileName:       "available.mkv",
				MatchedTitleID: "tmdb:movie:123",
				MatchedName:    "Example Movie",
				MatchedYear:    2024,
				LibraryType:    models.LocalMediaLibraryTypeMovie,
				UpdatedAt:      now,
				CreatedAt:      now,
			},
			"missing.mkv": {
				ID:             "item2",
				LibraryID:      "lib1",
				RelativePath:   "missing.mkv",
				FileName:       "missing.mkv",
				MatchedTitleID: "tmdb:movie:123",
				MatchedName:    "Example Movie",
				MatchedYear:    2024,
				LibraryType:    models.LocalMediaLibraryTypeMovie,
				IsMissing:      true,
				UpdatedAt:      now,
				CreatedAt:      now,
			},
		},
	}
	service := &Service{repo: repo}

	result, err := service.ListGroups(context.Background(), "lib1", models.LocalMediaItemListQuery{})
	if err != nil {
		t.Fatalf("ListGroups error: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("result.Total = %d, want 1", result.Total)
	}
	if len(result.Groups) != 1 {
		t.Fatalf("len(result.Groups) = %d, want 1", len(result.Groups))
	}
	if result.Groups[0].ItemCount != 1 {
		t.Fatalf("result.Groups[0].ItemCount = %d, want 1", result.Groups[0].ItemCount)
	}
	if result.Groups[0].MissingCount != 0 {
		t.Fatalf("result.Groups[0].MissingCount = %d, want 0", result.Groups[0].MissingCount)
	}
}

func TestListGroupsIncludeCardsOmitsNestedItemsAndSeasons(t *testing.T) {
	now := time.Now().UTC()
	repo := &fakeLocalMediaRepo{
		library: &models.LocalMediaLibrary{
			ID:        "lib1",
			Name:      "Shows",
			Type:      models.LocalMediaLibraryTypeShow,
			RootPath:  t.TempDir(),
			CreatedAt: now,
			UpdatedAt: now,
		},
		items: map[string]*models.LocalMediaItem{
			"show/S01E01.mkv": {
				ID:             "item1",
				LibraryID:      "lib1",
				RelativePath:   "show/S01E01.mkv",
				FileName:       "S01E01.mkv",
				MatchedTitleID: "tvdb:series:123",
				MatchedName:    "Example Show",
				MatchedYear:    2024,
				LibraryType:    models.LocalMediaLibraryTypeShow,
				SeasonNumber:   1,
				EpisodeNumber:  1,
				UpdatedAt:      now,
				CreatedAt:      now,
			},
		},
	}
	service := &Service{repo: repo}

	result, err := service.ListGroups(context.Background(), "lib1", models.LocalMediaItemListQuery{IncludeCards: true})
	if err != nil {
		t.Fatalf("ListGroups error: %v", err)
	}
	if len(result.Groups) != 1 {
		t.Fatalf("len(result.Groups) = %d, want 1", len(result.Groups))
	}
	group := result.Groups[0]
	if group.Title != "Example Show" || group.Year != 2024 || group.ItemCount != 1 {
		t.Fatalf("unexpected card group: %+v", group)
	}
	if group.Items != nil {
		t.Fatalf("expected nil Items for card projection, got %#v", group.Items)
	}
	if group.Seasons != nil {
		t.Fatalf("expected nil Seasons for card projection, got %#v", group.Seasons)
	}
}

func TestFilterLocalMediaGroupsByMediaType(t *testing.T) {
	groups := []models.LocalMediaItemGroup{
		{Title: "Movie", LibraryType: models.LocalMediaLibraryTypeMovie},
		{Title: "Show", LibraryType: models.LocalMediaLibraryTypeShow},
	}
	filtered := filterLocalMediaGroupsByMediaType(groups, "series")
	if len(filtered) != 1 || filtered[0].Title != "Show" {
		t.Fatalf("unexpected filtered groups: %#v", filtered)
	}
}

func TestFilterLocalMediaGroupsByAlphabetNormalizesArticles(t *testing.T) {
	groups := []models.LocalMediaItemGroup{{Title: "The Zebra"}, {Title: "Alpha"}, {Title: "1917"}}
	filtered := filterLocalMediaGroupsByAlphabet(groups, "Z")
	if len(filtered) != 1 || filtered[0].Title != "The Zebra" {
		t.Fatalf("unexpected filtered groups: %#v", filtered)
	}
	buckets := localMediaAlphabetBuckets(groups)
	if len(buckets) != 3 || buckets[0] != "#" || buckets[1] != "A" || buckets[2] != "Z" {
		t.Fatalf("unexpected buckets: %#v", buckets)
	}
}

func TestUpdateItemMatchRefreshesLibrarySummary(t *testing.T) {
	now := time.Now().UTC()
	repo := &fakeLocalMediaRepo{
		library: &models.LocalMediaLibrary{
			ID:                 "lib1",
			LastScanStatus:     models.LocalMediaScanStatusComplete,
			LastScanDiscovered: 1,
			LastScanMatched:    0,
			LastScanLowConf:    1,
			CreatedAt:          now,
			UpdatedAt:          now,
		},
		items: map[string]*models.LocalMediaItem{
			"show/S01E01.mkv": {
				ID:            "item1",
				LibraryID:     "lib1",
				RelativePath:  "show/S01E01.mkv",
				FileName:      "S01E01.mkv",
				MatchStatus:   models.LocalMediaMatchStatusLowConfidence,
				DetectedTitle: "Example Show",
				CreatedAt:     now,
				UpdatedAt:     now,
			},
		},
	}
	service := &Service{repo: repo}

	updated, err := service.UpdateItemMatch(context.Background(), "item1", models.LocalMediaMatchInput{
		MatchedTitleID:   "tmdb:tv:123",
		MatchedMediaType: "series",
		MatchedName:      "Example Show",
		MatchedYear:      2024,
		Confidence:       1,
		MatchStatus:      models.LocalMediaMatchStatusManual,
	})
	if err != nil {
		t.Fatalf("UpdateItemMatch error: %v", err)
	}
	if updated.MatchStatus != models.LocalMediaMatchStatusManual {
		t.Fatalf("updated.MatchStatus = %q, want %q", updated.MatchStatus, models.LocalMediaMatchStatusManual)
	}
	if repo.library.LastScanDiscovered != 1 {
		t.Fatalf("LastScanDiscovered = %d, want 1", repo.library.LastScanDiscovered)
	}
	if repo.library.LastScanMatched != 1 {
		t.Fatalf("LastScanMatched = %d, want 1", repo.library.LastScanMatched)
	}
	if repo.library.LastScanLowConf != 0 {
		t.Fatalf("LastScanLowConf = %d, want 0", repo.library.LastScanLowConf)
	}
}

// Anime releases name the episode but not the season ("[Erai-raws] Shadows House - 01"): the
// season folder supplies it. A season in the file name wins; Specials and Season 00 stay 0.
func TestApplyFolderSeason(t *testing.T) {
	show := models.LocalMediaLibraryTypeShow
	for _, tc := range []struct {
		path            string
		libraryType     models.LocalMediaLibraryType
		season, episode int
		want            int
	}{
		{"/media/serien/Shadows House/Season 01/[Erai-raws] Shadows House - 01 [1080p].mkv", show, 0, 1, 1},
		{"/media/serien/Show/Season 1/Show - 05.mkv", show, 0, 5, 1},
		{"/media/serien/Show/Staffel 3/Show - 05.mkv", show, 0, 5, 3},
		{"/media/serien/Show/S02/Show - 05.mkv", show, 0, 5, 2},
		{"/media/serien/Show/Season 12 (2021)/Show - 05.mkv", show, 0, 5, 12},
		{"/media/serien/Show/Season 00/Show - 05.mkv", show, 0, 5, 0},
		{"/media/serien/Show/Specials/Show - 05.mkv", show, 0, 5, 0},
		{"/media/serien/Shadows House/Show - 05.mkv", show, 0, 5, 0},      // no season folder
		{"/media/serien/Show/Season 01/Show S02E05.mkv", show, 2, 5, 2},   // the file name wins
		{"/media/serien/Show/Season 01/Show - Extras.mkv", show, 0, 0, 0}, // no episode
		{"/media/filme/Season 01/Movie 2024.mkv", models.LocalMediaLibraryTypeMovie, 0, 1, 0},
		{"/media/serien/Show/Season 100/Show - 05.mkv", show, 0, 5, 0},
	} {
		detected := detectedTitle{title: "Show", season: tc.season, episode: tc.episode}
		applyFolderSeason(&detected, tc.libraryType, filepath.FromSlash(tc.path))
		if detected.season != tc.want {
			t.Errorf("%s: season %d, want %d", tc.path, detected.season, tc.want)
		}
	}
}

// A rescan applies the current detection to files it reuses unchanged (same size and time), so
// episodes scanned before the season-folder rule get their season.
func TestBuildItemReusedEpisodeTakesDetectedSeason(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Shadows House", "Season 01")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(dir, "[Erai-raws] Shadows House - 01 [1080p][Multiple Subtitle].mkv")
	if err := os.WriteFile(filePath, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	modified := info.ModTime().UTC()
	existing := models.LocalMediaItem{
		ID: "item1", FilePath: filePath, SizeBytes: info.Size(), ModifiedAt: &modified,
		SeasonNumber: 0, EpisodeNumber: 1,
	}
	library := models.LocalMediaLibrary{ID: "lib", Type: models.LocalMediaLibraryTypeShow, RootPath: root}
	detected := detectedTitle{title: "Shadows House", episode: 1}
	applyFolderSeason(&detected, library.Type, filePath)
	item, reused, err := (&Service{}).buildItem(context.Background(), library, filePath, "Shadows House/Season 01/x.mkv", detected, nil, "scan2", existing, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reused || item.SeasonNumber != 1 || item.EpisodeNumber != 1 {
		t.Fatalf("reused=%v season=%d episode=%d, want reused S1E1", reused, item.SeasonNumber, item.EpisodeNumber)
	}
}
