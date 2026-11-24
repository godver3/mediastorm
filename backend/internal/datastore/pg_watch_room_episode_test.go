package datastore

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"novastream/models"
)

// TestWatchRoomEpisodePostgres runs against an isolated schema of a real
// PostgreSQL database; set WATCH_ROOM_TEST_DATABASE_URL to enable it.
func TestWatchRoomEpisodePostgres(t *testing.T) {
	dsn := os.Getenv("WATCH_ROOM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set WATCH_ROOM_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "watch_room_test_" + uuid.NewString()[:8]
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
	for _, stmt := range []string{
		`INSERT INTO accounts(id,username,password_hash) VALUES ('acct','acct','x')`,
		`INSERT INTO users(id,account_id,name) VALUES ('host','acct','Host'),('guest','acct','Guest')`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	repo := (&DataStore{pool: pool}).WatchRooms()
	now := time.Now().UTC().Truncate(time.Millisecond)
	caps := models.WatchRoomClientCapabilities{NativePlayback: true, StateSync: true, ProtocolVersion: 1}
	room := &models.WatchRoom{ID: uuid.NewString(), CreatorProfileID: "host", Title: "Show", MediaType: "series", ItemID: "tmdb:tv:1",
		Params: map[string]string{}, Episode: &models.WatchRoomEpisode{SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot"},
		Status: models.WatchRoomStatusLobby, AnchorUpdatedAt: now, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := repo.Create(ctx, room, []string{"guest"}, "c", caps); err != nil {
		t.Fatal(err)
	}
	if err := repo.Join(ctx, room.ID, "guest", "g", caps, now); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, room.ID)
	if err != nil || got.Episode == nil || got.Episode.Key() != "s1e1" || got.Episode.Title != "Pilot" {
		t.Fatalf("Get episode = %#v, %v", got, err)
	}
	// Plain state update (legacy client): no episode change, no readiness reset.
	for _, stmt := range []string{`UPDATE watch_rooms SET status='playing' WHERE id=$1`, `UPDATE watch_room_members SET ready=true WHERE room_id=$1`} {
		if _, err := pool.Exec(ctx, stmt, room.ID); err != nil {
			t.Fatal(err)
		}
	}
	ok, err := repo.UpdateState(ctx, room.ID, "guest", "paused", 50, 0, nil, nil, now)
	if err != nil || !ok {
		t.Fatalf("plain update = %v, %v", ok, err)
	}
	got, _ = repo.Get(ctx, room.ID)
	if got.WaitingForReady || got.Episode.Key() != "s1e1" {
		t.Fatalf("plain update changed episode/readiness: %#v waiting=%v", got.Episode, got.WaitingForReady)
	}
	// Non-creator episode change is refused by the guarded update.
	ep2 := &models.WatchRoomEpisode{SeasonNumber: 1, EpisodeNumber: 2}
	if ok, err := repo.UpdateState(ctx, room.ID, "guest", "playing", 0, 0, nil, ep2, now); err != nil || ok {
		t.Fatalf("guest episode update = %v, %v", ok, err)
	}
	rev := got.Revision
	if ok, err := repo.UpdateState(ctx, room.ID, "host", "playing", 0, 0, &rev, ep2, now); err != nil || !ok {
		t.Fatalf("host episode update = %v, %v", ok, err)
	}
	got, _ = repo.Get(ctx, room.ID)
	if got.Episode.Key() != "s1e2" || !got.WaitingForReady || got.Revision != rev+1 {
		t.Fatalf("after change: %#v waiting=%v rev=%d", got.Episode, got.WaitingForReady, got.Revision)
	}
	for _, m := range got.Members {
		if m.Ready {
			t.Fatalf("member %s still ready", m.ProfileID)
		}
	}
	// Sources are immutable per episode and replaced for a new one.
	if ok, err := repo.BindExternalSource(ctx, room.ID, "host", "/e1", nil, "s1e1", now); err != nil || !ok {
		t.Fatalf("bind e1 = %v, %v", ok, err)
	}
	if ok, _ := repo.BindExternalSource(ctx, room.ID, "host", "/other", nil, "s1e1", now); ok {
		t.Fatal("same-episode rebind accepted")
	}
	later := now.Add(time.Minute)
	if ok, err := repo.BindExternalSource(ctx, room.ID, "host", "/e2", nil, "s1e2", later); err != nil || !ok {
		t.Fatalf("bind e2 = %v, %v", ok, err)
	}
	src, err := repo.GetExternalSource(ctx, room.ID)
	if err != nil || src.Resource != "/e2" || src.EpisodeKey != "s1e2" || !src.BoundAt.Equal(later) {
		t.Fatalf("source = %#v, %v", src, err)
	}
}
