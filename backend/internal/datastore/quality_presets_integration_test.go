package datastore

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"novastream/internal/arr"
	"novastream/models"
	"novastream/services/qualityprofiles"
)

// TestQualityPresetStoreIntegration runs the migrations against a real
// database and exercises the preset store. Set QUALITY_PRESETS_TEST_DB_URL to
// an empty, disposable database to run it.
func TestQualityPresetStoreIntegration(t *testing.T) {
	dsn := os.Getenv("QUALITY_PRESETS_TEST_DB_URL")
	if dsn == "" {
		t.Skip("set QUALITY_PRESETS_TEST_DB_URL to run against Postgres")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := runMigrations(ctx, pool); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	if err := runMigrations(ctx, pool); err != nil {
		t.Fatalf("idempotent rerun: %v", err)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM quality_presets`)
	_, _ = pool.Exec(ctx, `DELETE FROM quality_snapshot_cache`)
	store := &pgQualityPresetRepo{pool: pool}

	now := time.Now().UTC().Truncate(time.Millisecond)
	p := &qualityprofiles.Preset{ID: "p1", App: arr.AppSonarr, Name: "German HD", CreatedAt: now, UpdatedAt: now,
		Upstream: qualityprofiles.Upstream{Commit: "abc", Formats: map[string]arr.CustomFormat{"g": {TrashID: "g", Name: "German"}},
			Profile: arr.Profile{App: arr.AppSonarr, Name: "German HD", FormatScores: map[string]int{"g": 1000}}}}
	if err := store.SavePreset(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetPreset(ctx, "p1")
	if err != nil || got == nil || got.Upstream.Profile.FormatScores["g"] != 1000 || got.Upstream.Formats["g"].Name != "German" {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	name := "Renamed"
	p.Overrides.Name = &name
	p.UpdatedAt = now.Add(time.Minute)
	if err := store.SavePreset(ctx, p); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT name FROM quality_presets WHERE id='p1'`).Scan(&stored); err != nil || stored != "Renamed" {
		t.Errorf("upsert must update the display name column: %q %v", stored, err)
	}
	if missing, err := store.GetPreset(ctx, "nope"); err != nil || missing != nil {
		t.Errorf("missing preset: %v %v", missing, err)
	}
	list, err := store.ListPresets(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %d %v", len(list), err)
	}

	for i := 0; i < maxCachedSnapshots+3; i++ {
		if err := store.PutSnapshot(ctx, "trash", string(rune('a'+i)), []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM quality_snapshot_cache WHERE source_id='trash'`).Scan(&count); err != nil || count != maxCachedSnapshots {
		t.Errorf("snapshot cache must be bounded to %d per source, have %d (%v)", maxCachedSnapshots, count, err)
	}
	newest := string(rune('a' + maxCachedSnapshots + 2))
	if data, err := store.GetSnapshot(ctx, "trash", newest); err != nil || !bytes.Equal(data, []byte{byte(maxCachedSnapshots + 2)}) {
		t.Errorf("newest snapshot missing: %v %v", data, err)
	}
	if data, err := store.GetSnapshot(ctx, "trash", "a"); err != nil || data != nil {
		t.Errorf("oldest snapshot should be evicted: %v %v", data, err)
	}

	if err := store.DeletePreset(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if list, _ := store.ListPresets(ctx); len(list) != 0 {
		t.Error("delete failed")
	}

	// The service works end to end on Postgres.
	svc := qualityprofiles.NewService(store, nil, nil)
	created, err := svc.ImportJSON(ctx, []byte(`[{"trash_id":"x","name":"X","trash_scores":{"default":5},"specifications":[{"name":"t","implementation":"ReleaseTitleSpecification","negate":false,"required":true,"fields":{"value":"foo"}}]}]`), arr.AppRadarr, "pg")
	if err != nil {
		t.Fatal(err)
	}
	active, err := svc.Active(ctx, created.ID)
	if err != nil || active.Evaluator == nil {
		t.Fatalf("active: %v", err)
	}
	if err := svc.Delete(ctx, created.ID); err != nil {
		t.Fatal(err)
	}

	// The preset remembered per profile, device and title.
	if err := store.SetTitlePreset(ctx, "u1", "tv", "tmdb:tv:1", "p1"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTitlePreset(ctx, "u1", "tv", "tmdb:tv:1", "p2"); err != nil {
		t.Fatal(err)
	}
	if id, err := store.GetTitlePreset(ctx, "u1", "tv", "tmdb:tv:1"); err != nil || id != "p2" {
		t.Fatalf("title preset after update: %q %v", id, err)
	}
	if id, _ := store.GetTitlePreset(ctx, "u1", "phone", "tmdb:tv:1"); id != "" {
		t.Errorf("another device: %q", id)
	}
	if err := store.SetTitlePreset(ctx, "u1", "tv", "tmdb:tv:1", ""); err != nil {
		t.Fatal(err)
	}
	if id, _ := store.GetTitlePreset(ctx, "u1", "tv", "tmdb:tv:1"); id != "" {
		t.Errorf("after forgetting: %q", id)
	}

	// ListTitlePresets returns every remembered pick (backups).
	_, _ = pool.Exec(ctx, `DELETE FROM title_quality_presets`)
	_ = store.SetTitlePreset(ctx, "u1", "tv", "tmdb:tv:1", "p1")
	_ = store.SetTitlePreset(ctx, "u2", "", "tmdb:movie:2", "p2")
	recs, err := store.ListTitlePresets(ctx)
	if err != nil || len(recs) != 2 || recs[0].UserID != "u1" || recs[1].PresetID != "p2" || recs[1].ClientID != "" {
		t.Fatalf("list title presets: %+v %v", recs, err)
	}
}

// TestSportsLinksBackupIntegration exercises the list/restore/clear methods
// used by backups. Uses QUALITY_PRESETS_TEST_DB_URL (a disposable database).
func TestSportsLinksBackupIntegration(t *testing.T) {
	dsn := os.Getenv("QUALITY_PRESETS_TEST_DB_URL")
	if dsn == "" {
		t.Skip("set QUALITY_PRESETS_TEST_DB_URL to run against Postgres")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := runMigrations(ctx, pool); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	repo := &pgSportsLinksRepo{pool: pool}
	if err := repo.DeleteAllTeams(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertTeam(ctx, models.SportsTeamRecord{ID: "mlb:1", League: "mlb", EspnTeamID: "1", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	link := models.SportsTeamChannelLink{
		ID: "l1", TeamID: "mlb:1", Slot: models.SportsLinkSlotBackup, Position: 3,
		ChannelName: "Ch", ChannelURL: "http://x", AutoLinked: true, LinkConfidence: 0.9,
		MatchReason: "name", CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.RestoreLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	teams, err := repo.ListAllTeams(ctx)
	if err != nil || len(teams) != 1 {
		t.Fatalf("teams: %+v %v", teams, err)
	}
	links, err := repo.ListAllLinks(ctx)
	if err != nil || len(links) != 1 || links[0].Position != 3 || links[0].Slot != models.SportsLinkSlotBackup || !links[0].AutoLinked {
		t.Fatalf("links: %+v %v", links, err)
	}
	if err := repo.DeleteAllTeams(ctx); err != nil {
		t.Fatal(err)
	}
	if links, _ := repo.ListAllLinks(ctx); len(links) != 0 {
		t.Fatalf("links should cascade: %+v", links)
	}
}
