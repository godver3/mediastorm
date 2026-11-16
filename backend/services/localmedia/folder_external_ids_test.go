package localmedia

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"novastream/models"
)

const gbboFileName = "The Great British Bake Off (2010) - S17E03 - Audience Choice Week [WEBDL-1080p][AAC 2.0][h264]-Kitsune.mkv"

func TestApplyFolderExternalIDsReadsSeriesFolderTag(t *testing.T) {
	path := "/media/tv_shows/The Great British Bake Off (2010) {tvdb-184871}/Season 17/" + gbboFileName
	detected := detectedTitle{title: "The Great British Bake Off", year: 2010, season: 17, episode: 3}
	applyFolderExternalIDs(&detected, path)
	if detected.tvdbID != 184871 || detected.tmdbID != 0 || detected.imdbID != "" {
		t.Fatalf("detected ids = %q/%d/%d, want tvdb 184871 only", detected.imdbID, detected.tmdbID, detected.tvdbID)
	}
	if detected.season != 17 || detected.episode != 3 {
		t.Fatalf("detected S%dE%d, want S17E03", detected.season, detected.episode)
	}
}

func TestApplyFolderExternalIDsKeepsFilenameIDsAndIgnoresLibraryRoot(t *testing.T) {
	detected := detectedTitle{tmdbID: 87012}
	applyFolderExternalIDs(&detected, "/media/Show {tvdb-184871}/Season 1/Show S01E01 {tmdb-87012}.mkv")
	if detected.tmdbID != 87012 || detected.tvdbID != 0 {
		t.Fatalf("filename ids were mixed with folder ids: %+v", detected)
	}

	detected = detectedTitle{}
	applyFolderExternalIDs(&detected, "/media/tvdb-99/Show/Season 1/Show S01E01.mkv")
	if detected.tvdbID != 0 {
		t.Fatalf("read tag above the series folder: %+v", detected)
	}
}

type gbboMetadata struct{ searchCalls, seriesCalls int }

func (m *gbboMetadata) Search(ctx context.Context, query, mediaType string) ([]models.SearchResult, error) {
	m.searchCalls++
	return []models.SearchResult{{Score: 100, Title: models.Title{ID: "tmdb:tv:34549", MediaType: "series", Name: "The Great British Bake Off", Year: 2010, TMDBID: 34549}}}, nil
}

func (m *gbboMetadata) MovieDetails(ctx context.Context, query models.MovieDetailsQuery) (*models.Title, error) {
	return nil, nil
}

func (m *gbboMetadata) SeriesDetails(ctx context.Context, query models.SeriesDetailsQuery) (*models.SeriesDetails, error) {
	m.seriesCalls++
	if query.TVDBID == 184871 {
		return &models.SeriesDetails{Title: models.Title{ID: "tvdb:series:184871", MediaType: "series", Name: "The Great British Bake Off", Year: 2010, TVDBID: 184871}}, nil
	}
	return &models.SeriesDetails{Title: models.Title{ID: "tmdb:tv:34549", MediaType: "series", Name: "The Great British Bake Off", Year: 2010, TMDBID: 34549}}, nil
}

func TestScanMatchesSeriesFolderTagAndRematchesReusedTitleSearchItems(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "The Great British Bake Off (2010) {tvdb-184871}", "Season 17")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := []string{gbboFileName, "The Great British Bake Off (2010) - S17E02 - Biscuit Week [WEBDL-1080p]-Kitsune.mkv"}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(filepath.Join(dir, files[0]))
	if err != nil {
		t.Fatal(err)
	}
	modifiedAt := info.ModTime().UTC()
	relativePath := filepath.Join("The Great British Bake Off (2010) {tvdb-184871}", "Season 17", files[0])

	repo := &fakeLocalMediaRepo{
		library: &models.LocalMediaLibrary{ID: "lib1", Name: "TV", Type: models.LocalMediaLibraryTypeShow, RootPath: root, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		items: map[string]*models.LocalMediaItem{
			// Matched by title search before folder tags were read.
			relativePath: {
				ID: "existing", LibraryID: "lib1", RelativePath: relativePath, FilePath: filepath.Join(dir, files[0]),
				LibraryType: models.LocalMediaLibraryTypeShow, SeasonNumber: 17, EpisodeNumber: 3,
				MatchStatus: models.LocalMediaMatchStatusMatched, MatchedTitleID: "tmdb:tv:34549", MatchedMediaType: "series",
				SizeBytes: info.Size(), ModifiedAt: &modifiedAt, Probe: &models.LocalMediaProbe{},
			},
		},
	}
	metadata := &gbboMetadata{}
	service := &Service{repo: repo, metadata: metadata, ffprobePath: "ffprobe", scans: make(map[string]scanState)}

	if _, err := service.RunScan(context.Background(), "lib1"); err != nil {
		t.Fatalf("RunScan: %v", err)
	}
	for _, item := range repo.items {
		if item.MatchedTitleID != "tvdb:series:184871" || item.SeasonNumber != 17 {
			t.Fatalf("%s matched %q S%d, want tvdb:series:184871 S17", item.FileName, item.MatchedTitleID, item.SeasonNumber)
		}
	}
	if repo.items[relativePath].ID != "existing" {
		t.Fatal("reused item lost its identity")
	}
	if metadata.searchCalls != 0 {
		t.Fatalf("title search ran %d times despite folder id tag", metadata.searchCalls)
	}
}

func TestScanKeepsManualMatchOverFolderTag(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Show {tvdb-184871}", "Season 1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "Show - S01E01.mkv")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	modifiedAt := info.ModTime().UTC()
	relativePath := filepath.Join("Show {tvdb-184871}", "Season 1", "Show - S01E01.mkv")
	repo := &fakeLocalMediaRepo{
		library: &models.LocalMediaLibrary{ID: "lib1", Type: models.LocalMediaLibraryTypeShow, RootPath: root},
		items: map[string]*models.LocalMediaItem{relativePath: {
			ID: "manual", LibraryID: "lib1", RelativePath: relativePath, FilePath: path, LibraryType: models.LocalMediaLibraryTypeShow,
			MatchStatus: models.LocalMediaMatchStatusManual, MatchedTitleID: "tmdb:tv:1", SizeBytes: info.Size(), ModifiedAt: &modifiedAt,
		}},
	}
	service := &Service{repo: repo, metadata: &gbboMetadata{}, ffprobePath: "ffprobe", scans: make(map[string]scanState)}
	if _, err := service.RunScan(context.Background(), "lib1"); err != nil {
		t.Fatalf("RunScan: %v", err)
	}
	if got := repo.items[relativePath].MatchedTitleID; got != "tmdb:tv:1" {
		t.Fatalf("manual match replaced with %q", got)
	}
}

// Postgres stores modified_at at microsecond precision; a file whose mtime has
// a sub-microsecond part must still be reused, keeping its manual match.
func TestScanReusesItemWithMicrosecondTruncatedModifiedAt(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Some Movie (2001).mkv")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2024, 2, 19, 5, 27, 17, 123456789, time.UTC)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.ModTime().Nanosecond()%1000 == 0 {
		t.Skip("filesystem does not keep sub-microsecond mtimes")
	}
	stored := info.ModTime().UTC().Truncate(time.Microsecond)
	repo := &fakeLocalMediaRepo{
		library: &models.LocalMediaLibrary{ID: "lib1", Type: models.LocalMediaLibraryTypeMovie, RootPath: root},
		items: map[string]*models.LocalMediaItem{"Some Movie (2001).mkv": {
			ID: "manual", LibraryID: "lib1", RelativePath: "Some Movie (2001).mkv", FilePath: path, LibraryType: models.LocalMediaLibraryTypeMovie,
			MatchStatus: models.LocalMediaMatchStatusManual, MatchedTitleID: "tmdb:movie:42", SizeBytes: info.Size(), ModifiedAt: &stored,
		}},
	}
	service := &Service{repo: repo, metadata: &gbboMetadata{}, ffprobePath: "ffprobe", scans: make(map[string]scanState)}
	if _, err := service.RunScan(context.Background(), "lib1"); err != nil {
		t.Fatalf("RunScan: %v", err)
	}
	item := repo.items["Some Movie (2001).mkv"]
	if item.MatchStatus != models.LocalMediaMatchStatusManual || item.MatchedTitleID != "tmdb:movie:42" {
		t.Fatalf("manual match lost: status=%s title=%q", item.MatchStatus, item.MatchedTitleID)
	}
}

func TestScanKeepsManualMatchWhenFileChanged(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Some Movie (2001).mkv")
	if err := os.WriteFile(path, []byte("changed contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	metadata := &gbboMetadata{}
	repo := &fakeLocalMediaRepo{
		library: &models.LocalMediaLibrary{ID: "lib1", Type: models.LocalMediaLibraryTypeMovie, RootPath: root},
		items: map[string]*models.LocalMediaItem{"Some Movie (2001).mkv": {
			ID: "manual", LibraryID: "lib1", RelativePath: "Some Movie (2001).mkv", FilePath: path, LibraryType: models.LocalMediaLibraryTypeMovie,
			MatchStatus: models.LocalMediaMatchStatusManual, MatchedTitleID: "tmdb:movie:42", MatchedName: "Chosen", SizeBytes: 1, ModifiedAt: &old,
		}},
	}
	service := &Service{repo: repo, metadata: metadata, ffprobePath: "ffprobe", scans: make(map[string]scanState)}
	if _, err := service.RunScan(context.Background(), "lib1"); err != nil {
		t.Fatalf("RunScan: %v", err)
	}
	item := repo.items["Some Movie (2001).mkv"]
	if item.ID != "manual" || item.MatchStatus != models.LocalMediaMatchStatusManual || item.MatchedTitleID != "tmdb:movie:42" || item.MatchedName != "Chosen" {
		t.Fatalf("manual match lost after file change: %+v", item)
	}
	if item.SizeBytes != int64(len("changed contents")) {
		t.Fatalf("size not refreshed: %d", item.SizeBytes)
	}
	if metadata.searchCalls != 0 {
		t.Fatalf("searched metadata %d times for a manually matched file", metadata.searchCalls)
	}
}
