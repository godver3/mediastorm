package localmedia

import (
	"context"
	"strings"
	"testing"

	"novastream/models"
	"novastream/utils/parsett"
)

// TestFindLibraryCopies: the files a library has of a catalog title, of one
// episode when asked, matched by any external ID; missing and unmatched files
// do not count.
func TestFindLibraryCopies(t *testing.T) {
	show := &models.Title{ID: "tvdb:series:396876", TMDBID: 9000, TVDBID: 396876}
	item := func(id string, season, episode int, missing bool, status models.LocalMediaMatchStatus) *models.LocalMediaItem {
		return &models.LocalMediaItem{ID: id, LibraryID: "lib", LibraryType: models.LocalMediaLibraryTypeShow, FileName: id + ".mkv",
			SeasonNumber: season, EpisodeNumber: episode, IsMissing: missing, MatchStatus: status, MatchedTitleID: show.ID, Metadata: show}
	}
	repo := &fakeLocalMediaRepo{
		library: &models.LocalMediaLibrary{ID: "lib", Name: "Anime", Type: models.LocalMediaLibraryTypeShow},
		items: map[string]*models.LocalMediaItem{
			"e2":      item("e2", 1, 2, false, models.LocalMediaMatchStatusMatched),
			"e3":      item("e3", 1, 3, false, models.LocalMediaMatchStatusMatched),
			"gone":    item("gone", 1, 4, true, models.LocalMediaMatchStatusMatched),
			"unknown": item("unknown", 1, 5, false, models.LocalMediaMatchStatusUnmatched),
		},
	}
	svc := &Service{repo: repo}
	ctx := context.Background()

	// Asked by the catalog's TMDB ID; the files carry the TVDB title ID.
	copies, err := svc.FindLibraryCopies(ctx, models.LocalMediaMatchQuery{MediaType: "series", TitleID: "tmdb:tv:9000", TMDBID: "9000"}, 1, 2, 0)
	if err != nil || len(copies) != 1 || copies[0].Item.ID != "e2" || copies[0].LibraryName != "Anime" {
		t.Fatalf("episode copies %+v, %v", copies, err)
	}
	for _, episode := range []int{4, 5, 6} {
		if copies, _ := svc.FindLibraryCopies(ctx, models.LocalMediaMatchQuery{MediaType: "series", TVDBID: "396876"}, 1, episode, 0); len(copies) != 0 {
			t.Errorf("S01E%02d: %d copies", episode, len(copies))
		}
	}
	// Any episode, and nothing for a movie or another title.
	if copies, _ := svc.FindLibraryCopies(ctx, models.LocalMediaMatchQuery{MediaType: "series", TitleID: show.ID}, 0, 0, 0); len(copies) != 2 {
		t.Errorf("any episode: %d copies, want 2", len(copies))
	}
	if copies, _ := svc.FindLibraryCopies(ctx, models.LocalMediaMatchQuery{MediaType: "movie", TitleID: show.ID}, 0, 0, 0); len(copies) != 0 {
		t.Errorf("movie lookup found %d series files", len(copies))
	}
	if copies, _ := svc.FindLibraryCopies(ctx, models.LocalMediaMatchQuery{MediaType: "series", TMDBID: "1"}, 1, 2, 0); len(copies) != 0 {
		t.Errorf("another title: %d copies", len(copies))
	}
}

// TestFindLibraryCopiesMultiEpisodeAndAbsolute: an episode inside a file that
// holds several is found by the file's name; an anime episode by its absolute
// number when the library numbers the series absolutely, and not when the
// library has the asked season itself.
func TestFindLibraryCopiesMultiEpisodeAndAbsolute(t *testing.T) {
	show := &models.Title{ID: "tvdb:series:81797", TMDBID: 37854, TVDBID: 81797}
	item := func(id, file string, season, episode int) *models.LocalMediaItem {
		return &models.LocalMediaItem{ID: id, LibraryID: "lib", LibraryType: models.LocalMediaLibraryTypeShow, FileName: file,
			SeasonNumber: season, EpisodeNumber: episode, MatchStatus: models.LocalMediaMatchStatusMatched, MatchedTitleID: show.ID, Metadata: show}
	}
	repo := &fakeLocalMediaRepo{
		library: &models.LocalMediaLibrary{ID: "lib", Name: "Anime", Type: models.LocalMediaLibraryTypeShow},
		items: map[string]*models.LocalMediaItem{
			"double": item("double", "One.Piece.S01E07E08.1080p.mkv", 1, 7),
			"range": {ID: "range", LibraryID: "lib", LibraryType: models.LocalMediaLibraryTypeShow, FileName: "[Group] One Piece - 11-12 (1080p).mkv", RelativePath: "Season 01/[Group] One Piece - 11-12 (1080p).mkv",
				SeasonNumber: 1, EpisodeNumber: 11, MatchStatus: models.LocalMediaMatchStatusMatched, MatchedTitleID: show.ID, Metadata: show},
			"abs": item("abs", "[SubsPlease] One Piece - 1071 (1080p).mkv", 1, 1071),
		},
	}
	svc := &Service{repo: repo}
	ctx := context.Background()
	q := models.LocalMediaMatchQuery{MediaType: "series", TitleID: show.ID}

	if copies, err := svc.FindLibraryCopies(ctx, q, 1, 8, 0); err != nil || len(copies) != 1 || copies[0].Item.ID != "double" {
		t.Fatalf("second episode of a double file: %+v, %v", copies, err)
	}
	if copies, _ := svc.FindLibraryCopies(ctx, q, 1, 12, 12); len(copies) != 1 || copies[0].Item.ID != "range" {
		t.Fatalf("second episode of an \"11-12\" file: %+v", copies)
	}
	if copies, _ := svc.FindLibraryCopies(ctx, q, 1, 9, 9); len(copies) != 0 {
		t.Fatalf("S01E09 outside the double file: %+v", copies)
	}
	if copies, _ := svc.FindLibraryCopies(ctx, q, 21, 5, 1071); len(copies) != 1 || copies[0].Item.ID != "abs" {
		t.Fatalf("absolute 1071 as S21E05: %+v", copies)
	}
	if copies, _ := svc.FindLibraryCopies(ctx, q, 21, 5, 0); len(copies) != 0 {
		t.Fatalf("without the absolute number: %+v", copies)
	}
	// The library has season 21 itself: it is numbered by season, the
	// absolute number does not apply.
	repo.items["s21"] = item("s21", "One.Piece.S21E01.mkv", 21, 1)
	if copies, _ := svc.FindLibraryCopies(ctx, q, 21, 5, 1071); len(copies) != 0 {
		t.Fatalf("season-numbered library matched by absolute number: %+v", copies)
	}
}

// TestFindLibraryCopiesSpecialsAndScannedAbsolute: a special (S00E05) never
// stands in for an episode; a file named by absolute number alone, as the
// scanner stores it (season 0, episode 1071), is found under that number.
func TestFindLibraryCopiesSpecialsAndScannedAbsolute(t *testing.T) {
	scanned := detectTitle(models.LocalMediaLibraryTypeShow, "[SubsPlease] One Piece - 1071 (1080p).mkv", mustParse(t, "[SubsPlease] One Piece - 1071 (1080p).mkv"))
	if scanned.episode != 1071 {
		t.Fatalf("scanned episode %d, want 1071", scanned.episode)
	}
	show := &models.Title{ID: "tvdb:series:1", TVDBID: 1}
	item := func(id, file string, season, episode int) *models.LocalMediaItem {
		return &models.LocalMediaItem{ID: id, LibraryID: "lib", LibraryType: models.LocalMediaLibraryTypeShow, FileName: file,
			SeasonNumber: season, EpisodeNumber: episode, MatchStatus: models.LocalMediaMatchStatusMatched, MatchedTitleID: show.ID, Metadata: show}
	}
	repo := &fakeLocalMediaRepo{
		library: &models.LocalMediaLibrary{ID: "lib", Name: "Shows", Type: models.LocalMediaLibraryTypeShow},
		items: map[string]*models.LocalMediaItem{
			"special": item("special", "Show.S00E05.Behind.the.Scenes.mkv", 0, 5),
			"sp15":    item("sp15", "Show.S00E15.mkv", 0, 15),
			"abs":     item("abs", "[SubsPlease] One Piece - 1071 (1080p).mkv", scanned.season, scanned.episode),
		},
	}
	svc := &Service{repo: repo}
	ctx := context.Background()
	q := models.LocalMediaMatchQuery{MediaType: "series", TitleID: show.ID}
	for _, tc := range []struct{ season, episode, absolute int }{{1, 5, 5}, {2, 3, 15}} {
		if copies, _ := svc.FindLibraryCopies(ctx, q, tc.season, tc.episode, tc.absolute); len(copies) != 0 {
			t.Errorf("S%02dE%02d (absolute %d) played a special: %+v", tc.season, tc.episode, tc.absolute, copies)
		}
	}
	if copies, _ := svc.FindLibraryCopies(ctx, q, 21, 5, 1071); len(copies) != 1 || copies[0].Item.ID != "abs" {
		t.Fatalf("scanned absolute file: %+v", copies)
	}
}

// TestFindLibraryCopiesTrustsOnlyWhatNamesSay: the fallback never guesses: a
// number in an episode's title is no episode run, specials (by folder or
// name) are no absolute episodes, a run without a season in the name only
// counts where the season is not a guess, a file named by season is never
// taken by absolute number, and a special asked for is not an absolutely
// numbered episode stored in season 0.
func TestFindLibraryCopiesTrustsOnlyWhatNamesSay(t *testing.T) {
	show := &models.Title{ID: "tvdb:series:2", TVDBID: 2}
	item := func(path string, season, episode int) *models.LocalMediaItem {
		file := path
		if i := strings.LastIndex(path, "/"); i >= 0 {
			file = path[i+1:]
		}
		return &models.LocalMediaItem{ID: path, LibraryID: "lib", LibraryType: models.LocalMediaLibraryTypeShow, FileName: file, RelativePath: path,
			SeasonNumber: season, EpisodeNumber: episode, MatchStatus: models.LocalMediaMatchStatusMatched, MatchedTitleID: show.ID, Metadata: show}
	}
	repo := &fakeLocalMediaRepo{
		library: &models.LocalMediaLibrary{ID: "lib", Name: "Shows", Type: models.LocalMediaLibraryTypeShow},
		items:   map[string]*models.LocalMediaItem{},
	}
	for _, it := range []*models.LocalMediaItem{
		item("Season 01/Show - 05 - 10 Things.mkv", 1, 5),
		item("Season 01/Show - 20 - 22 Days.mkv", 1, 20),
		item("Specials/[Group] Show - 03 (1080p).mkv", 0, 3),
		item("Season 00/[Group] Show - 04.mkv", 0, 4),
		item("[Group] Show - OVA 06 (1080p).mkv", 0, 6),
		item("[Group] Show - 09 (1080p).mkv", 0, 9),
		item("[Group] Show - 30-31 (1080p).mkv", 30, 30), // "30-31" misread as season 30
		item("Season 30/Show.S30E01.mkv", 30, 1),
		item("Show.S01E11.mkv", 1, 11), // DVD order: the catalog's S02E01 is absolute 11
	} {
		repo.items[it.ID] = it
	}
	svc := &Service{repo: repo}
	ctx := context.Background()
	q := models.LocalMediaMatchQuery{MediaType: "series", TitleID: show.ID}
	for _, tc := range []struct {
		name                      string
		season, episode, absolute int
	}{
		{"title number as a run", 1, 7, 7},
		{"title number as a run, last", 1, 10, 10},
		{"short title number", 1, 22, 22},
		{"Specials folder", 1, 3, 3},
		{"Season 00 folder", 1, 4, 4},
		{"OVA", 1, 6, 6},
		{"season misread from a run", 30, 31, 0},
		{"named by season", 2, 1, 11},
		{"special asked for", 0, 9, 0},
	} {
		if copies, _ := svc.FindLibraryCopies(ctx, q, tc.season, tc.episode, tc.absolute); len(copies) != 0 {
			t.Errorf("%s: S%02dE%02d (absolute %d) found %s", tc.name, tc.season, tc.episode, tc.absolute, copies[0].Item.RelativePath)
		}
	}
	// The absolutely named file is still found as the episode it numbers.
	if copies, _ := svc.FindLibraryCopies(ctx, q, 1, 9, 9); len(copies) != 1 || copies[0].Item.FileName != "[Group] Show - 09 (1080p).mkv" {
		t.Fatalf("absolute 9: %+v", copies)
	}
}

// TestFindLibraryCopiesAnimeLayouts: specials in season folders, files of
// different folders or names under one number, Sonarr's absolutely numbered
// season folders and number runs in episode titles never play the wrong file.
func TestFindLibraryCopiesAnimeLayouts(t *testing.T) {
	show := &models.Title{ID: "tvdb:series:3", TVDBID: 3}
	newSvc := func(files map[string][2]int, lowConfidence ...string) (*Service, models.LocalMediaMatchQuery) {
		repo := &fakeLocalMediaRepo{
			library: &models.LocalMediaLibrary{ID: "lib", Name: "Anime", Type: models.LocalMediaLibraryTypeShow},
			items:   map[string]*models.LocalMediaItem{},
		}
		for path, se := range files {
			file := path
			if i := strings.LastIndex(path, "/"); i >= 0 {
				file = path[i+1:]
			}
			status := models.LocalMediaMatchStatusMatched
			for _, low := range lowConfidence {
				if low == path {
					status = models.LocalMediaMatchStatusLowConfidence
				}
			}
			repo.items[path] = &models.LocalMediaItem{ID: path, LibraryID: "lib", LibraryType: models.LocalMediaLibraryTypeShow, FileName: file, RelativePath: path,
				SeasonNumber: se[0], EpisodeNumber: se[1], MatchStatus: status, MatchedTitleID: show.ID, Metadata: show}
		}
		return &Service{repo: repo}, models.LocalMediaMatchQuery{MediaType: "series", TitleID: show.ID}
	}
	ctx := context.Background()
	expect := func(t *testing.T, svc *Service, q models.LocalMediaMatchQuery, season, episode, absolute int, want string) {
		t.Helper()
		copies, err := svc.FindLibraryCopies(ctx, q, season, episode, absolute)
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		if len(copies) > 1 {
			t.Fatalf("S%02dE%02d (absolute %d): %d copies", season, episode, absolute, len(copies))
		}
		if len(copies) == 1 {
			got = copies[0].Item.RelativePath
		}
		if got != want {
			t.Errorf("S%02dE%02d (absolute %d): got %q, want %q", season, episode, absolute, got, want)
		}
	}

	t.Run("specials in season folders", func(t *testing.T) {
		svc, q := newSvc(map[string][2]int{
			"Season 1/[Group] Show - OVA 06 (1080p).mkv": {1, 6},
			"Season 1/Show - 15.5 - Recap.mkv":           {1, 15},
			"Season 1/[Group] Show - 12 NCED.mkv":        {1, 12},
			"Specials/[Group] Show - OVA 2.mkv":          {0, 2},
			"Specials/Show.S00E03.mkv":                   {0, 3},
		})
		expect(t, svc, q, 1, 6, 6, "")
		expect(t, svc, q, 1, 15, 15, "")
		expect(t, svc, q, 1, 12, 12, "")
		expect(t, svc, q, 0, 2, 0, "") // a special asked for: only an S00Exx name
		expect(t, svc, q, 0, 3, 0, "Specials/Show.S00E03.mkv")
	})

	t.Run("different folders or names under one number", func(t *testing.T) {
		svc, q := newSvc(map[string][2]int{
			"Book 1/Show - 05.mkv":             {0, 5},
			"Book 2/Show - 05.mkv":             {0, 5},
			"[Group] Show - 07 (1080p).mkv":    {0, 7},
			"[Group] Show II - 07 (1080p).mkv": {0, 7},
			"[A] Show - 09 (1080p).mkv":        {0, 9},
			"[B] Show - 09 (720p).mkv":         {0, 9},
		})
		expect(t, svc, q, 1, 5, 5, "")
		expect(t, svc, q, 1, 7, 7, "")
		if copies, _ := svc.FindLibraryCopies(ctx, q, 1, 9, 9); len(copies) != 2 {
			t.Errorf("two versions of one file: %d copies", len(copies))
		}
	})

	t.Run("absolutely numbered season folders", func(t *testing.T) {
		svc, q := newSvc(map[string][2]int{
			"Season 01/Show - 001.mkv":         {1, 1},
			"Season 01/Show - 012.mkv":         {1, 12},
			"Season 02/Show - 013 - Title.mkv": {2, 13},
			"Season 02/Show - 015-016.mkv":     {2, 15},
		})
		expect(t, svc, q, 2, 13, 25, "")
		expect(t, svc, q, 2, 1, 13, "Season 02/Show - 013 - Title.mkv")
		expect(t, svc, q, 2, 16, 28, "")
		expect(t, svc, q, 2, 4, 16, "Season 02/Show - 015-016.mkv")
		expect(t, svc, q, 1, 12, 12, "Season 01/Show - 012.mkv")
	})

	t.Run("season-numbered folders stay as they are", func(t *testing.T) {
		svc, q := newSvc(map[string][2]int{
			"Season 01/Show - 01.mkv": {1, 1},
			"Season 01/Show - 10.mkv": {1, 10},
			"Season 02/Show - 01.mkv": {2, 1},
			"Season 02/Show - 05.mkv": {2, 5},
		})
		expect(t, svc, q, 2, 5, 15, "Season 02/Show - 05.mkv")
		expect(t, svc, q, 2, 6, 16, "")
	})

	t.Run("season folders that cannot tell", func(t *testing.T) {
		// Season 1 missing (or its first episodes deleted): absolute or by
		// season looks the same, so neither is trusted.
		svc, q := newSvc(map[string][2]int{
			"Season 02/Show - 05.mkv": {2, 5},
			"Season 02/Show - 06.mkv": {2, 6},
		})
		expect(t, svc, q, 2, 5, 17, "")
		expect(t, svc, q, 1, 5, 5, "")
		// A name that gives its season numbers by it.
		svc, q = newSvc(map[string][2]int{
			"Season 02/[SubsPlease] Show S2 - 05 (1080p).mkv": {2, 5},
		})
		expect(t, svc, q, 2, 5, 17, "Season 02/[SubsPlease] Show S2 - 05 (1080p).mkv")
		expect(t, svc, q, 1, 5, 5, "")
		// Season 2's first episodes deleted where season 1 has more episodes.
		svc, q = newSvc(map[string][2]int{
			"Season 01/Show - 01.mkv": {1, 1},
			"Season 01/Show - 12.mkv": {1, 12},
			"Season 02/Show - 05.mkv": {2, 5},
		})
		expect(t, svc, q, 2, 5, 17, "Season 02/Show - 05.mkv")
	})

	t.Run("stray files do not hide an absolute folder", func(t *testing.T) {
		svc, q := newSvc(map[string][2]int{
			"Season 01/Show - 001.mkv":           {1, 1},
			"Season 01/Show - 012.mkv":           {1, 12},
			"Season 02/Show - 013.mkv":           {2, 13},
			"Season 02/Show - 014.mkv":           {2, 14},
			"Season 02/Show - NCOP1.mkv":         {0, 0},
			"Season 02/Show - 012.5 - Recap.mkv": {2, 12},
		})
		expect(t, svc, q, 2, 13, 25, "")
		expect(t, svc, q, 2, 1, 13, "Season 02/Show - 013.mkv")
		// Asked with another season start than the folder's: unclear.
		expect(t, svc, q, 2, 2, 16, "")
	})

	t.Run("a recap before the extension", func(t *testing.T) {
		svc, q := newSvc(map[string][2]int{
			"Season 01/[Group] Show - 05 (1080p).mkv": {1, 5},
			"Season 01/[Group] Show - 05.5.mkv":       {1, 5},
		})
		expect(t, svc, q, 1, 5, 5, "Season 01/[Group] Show - 05 (1080p).mkv")
	})

	t.Run("number runs in episode titles and low confidence", func(t *testing.T) {
		svc, q := newSvc(map[string][2]int{
			"Show - S01E01 - 1-2 Punch.mkv":        {1, 1},
			"Show.S01E03.Pilot.Part.3-4.mkv":       {1, 3},
			"Season 1/Show - 30 - 30-32 Lives.mkv": {1, 30},
			"Show - S01E40.mkv":                    {1, 40},
		}, "Show - S01E40.mkv")
		expect(t, svc, q, 1, 2, 2, "")
		expect(t, svc, q, 1, 4, 4, "")
		expect(t, svc, q, 1, 31, 31, "")
		expect(t, svc, q, 1, 40, 40, "")
	})
}

// TestDropAmbiguousAcrossLibraries: a sequel named without its season in
// another library makes the number ambiguous; versions of one file in two
// libraries do not.
func TestDropAmbiguousAcrossLibraries(t *testing.T) {
	copy := func(library, path string, episode int) LibraryCopy {
		return LibraryCopy{LibraryID: library, Item: models.LocalMediaItem{RelativePath: path, FileName: path[strings.LastIndex(path, "/")+1:], EpisodeNumber: episode}}
	}
	if got := dropAmbiguous([]LibraryCopy{copy("a", "Show/[Group] Show - 05 (1080p).mkv", 5), copy("b", "Show II/[Group] Show II - 05 (1080p).mkv", 5)}); len(got) != 0 {
		t.Errorf("sequel in another library: %d copies", len(got))
	}
	if got := dropAmbiguous([]LibraryCopy{copy("a", "Show/[Group] Show - 05 (1080p).mkv", 5), copy("b", "Anime/[Other] Show - 05 (720p).mkv", 5)}); len(got) != 2 {
		t.Errorf("one episode in two libraries: %d copies", len(got))
	}
}

func mustParse(t *testing.T, name string) *parsett.ParsedTitle {
	t.Helper()
	parsed, err := parsett.ParseTitle(name)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
