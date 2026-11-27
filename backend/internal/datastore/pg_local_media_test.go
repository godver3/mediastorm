package datastore

import (
	"context"
	"os"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"novastream/models"
)

// TestListItemsForTitlePostgres runs against an isolated schema of a real
// PostgreSQL database; set LOCAL_MEDIA_TEST_DATABASE_URL to enable it.
func TestListItemsForTitlePostgres(t *testing.T) {
	dsn := os.Getenv("LOCAL_MEDIA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOCAL_MEDIA_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "local_media_test_" + uuid.NewString()[:8]
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE")
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := runMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repo := (&DataStore{pool: pool}).LocalMedia()
	lib := &models.LocalMediaLibrary{ID: "lib", Name: "Series", Type: models.LocalMediaLibraryTypeShow, RootPath: "/srv"}
	if err := repo.CreateLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	item := func(id string, season, episode int, status models.LocalMediaMatchStatus, missing bool, titleID string, meta *models.Title) {
		t.Helper()
		it := &models.LocalMediaItem{ID: id, LibraryID: "lib", RelativePath: id + ".mkv", FilePath: "/srv/" + id + ".mkv", FileName: id + ".mkv",
			LibraryType: models.LocalMediaLibraryTypeShow, SeasonNumber: season, EpisodeNumber: episode, MatchStatus: status,
			MatchedTitleID: titleID, IsMissing: missing, Metadata: meta}
		if err := repo.UpsertItem(ctx, it); err != nil {
			t.Fatal(err)
		}
	}
	item("s1e1", 1, 1, models.LocalMediaMatchStatusMatched, false, "tmdb:tv:1399", nil)
	item("s1e2", 1, 2, models.LocalMediaMatchStatusManual, false, "", &models.Title{TMDBID: 1399, IMDBID: "tt0944947"})
	item("s2e1", 2, 1, models.LocalMediaMatchStatusMatched, false, "tvdb:series:121361", &models.Title{TVDBID: 121361})
	item("gone", 1, 1, models.LocalMediaMatchStatusMatched, true, "tmdb:tv:1399", nil)
	item("unmatched", 1, 1, models.LocalMediaMatchStatusUnmatched, false, "tmdb:tv:1399", nil)
	item("other", 1, 1, models.LocalMediaMatchStatusMatched, false, "tmdb:tv:1", nil)

	ids := func(titleID, imdb, tmdb, tvdb string, season, episode int) []string {
		t.Helper()
		items, err := repo.ListItemsForTitle(ctx, "lib", titleID, imdb, tmdb, tvdb, season, episode)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, it := range items {
			out = append(out, it.ID)
		}
		sort.Strings(out)
		return out
	}
	check := func(name string, got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %v, want %v", name, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s: got %v, want %v", name, got, want)
			}
		}
	}
	check("title ID, whole series", ids("TMDB:TV:1399", "", "", "", 0, 0), "s1e1")
	check("tmdb metadata", ids("", "", "1399", "", 0, 0), "s1e2")
	check("all external IDs", ids("tmdb:tv:1399", "TT0944947", "1399", "121361", 0, 0), "s1e1", "s1e2", "s2e1")
	check("one episode", ids("tmdb:tv:1399", "", "1399", "121361", 1, 2), "s1e2")
	check("no IDs", ids("", "", "", "", 0, 0))
}
