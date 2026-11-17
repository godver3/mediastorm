package datastore

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"novastream/internal/nzbfetch"
	"novastream/internal/providerbreaker"
)

func TestNZBAcquisitionPostgres(t *testing.T) {
	dsn := os.Getenv("NZB_POLICY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NZB_POLICY_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "nzb_test_" + uuid.NewString()[:8]
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
	if err := runMigrations(ctx, pool); err != nil {
		t.Fatalf("migration rerun: %v", err)
	}
	ds := &DataStore{pool: pool}
	r := ds.NZBAcquisition()
	now := time.Now().UTC().Truncate(time.Second)
	reserve := func(title, provider string, at time.Time, allowed bool) time.Time {
		t.Helper()
		retry, err := r.ReserveNZB(ctx, title, provider, at)
		if err != nil || retry.IsZero() != allowed {
			t.Fatalf("reserve %s/%s allowed=%v: retry=%v err=%v", title, provider, allowed, retry, err)
		}
		return retry
	}
	reserve("tmdb:movie:78", "one", now, true)
	if retry := reserve("tmdb:movie:78", "two", now, false); !retry.Equal(now.Add(nzbfetch.Spacing)) {
		t.Fatalf("spacing: %v", retry)
	}
	reserve("tmdb:movie:78", "two", now.Add(nzbfetch.Spacing), true)
	reserve("tmdb:movie:78", "three", now.Add(2*nzbfetch.Spacing), true)
	reserve("tmdb:movie:78", "four", now.Add(3*nzbfetch.Spacing), false)
	reserve("tmdb:movie:79", "one", now, true)
	reserve("tvdb:series:1:s1e1", "one", now, true)
	reserve("tvdb:series:1:s1e2", "one", now, true)
	// Reconstruct the repository as a fresh backend would, without clearing PG.
	r = (&DataStore{pool: pool}).NZBAcquisition()
	reserve("tmdb:movie:78", "one", now.Add(3*nzbfetch.Spacing), false)
	reserve("tmdb:movie:78", "one", now.Add(nzbfetch.Window), true)

	t.Run("concurrent reservations", func(t *testing.T) {
		var accepted atomic.Int32
		var wg sync.WaitGroup
		for range 20 {
			wg.Go(func() {
				retry, err := r.ReserveNZB(ctx, "tmdb:movie:100", "one", now)
				if err != nil {
					t.Error(err)
				} else if retry.IsZero() {
					accepted.Add(1)
				}
			})
		}
		wg.Wait()
		if accepted.Load() != 1 {
			t.Fatalf("accepted %d concurrent grabs", accepted.Load())
		}
	})
	t.Run("title lock isolation", func(t *testing.T) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "tmdb:movie:200"); err != nil {
			t.Fatal(err)
		}
		bounded, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if retry, err := r.ReserveNZB(bounded, "tmdb:movie:201", "one", now); err != nil || !retry.IsZero() {
			t.Fatalf("unrelated title blocked: %v %v", retry, err)
		}
	})
	t.Run("persistent cooldowns", func(t *testing.T) {
		first := providerbreaker.New()
		if err := first.Restore(ctx, r); err != nil {
			t.Fatal(err)
		}
		until := first.RecordRateLimit("Fixture", 48*time.Hour)
		second := providerbreaker.New()
		if err := second.Restore(ctx, r); err != nil {
			t.Fatal(err)
		}
		allowed, restored, _ := second.Allow("fixture")
		if allowed || restored.Sub(until).Abs() > time.Microsecond {
			t.Fatalf("lost pause: %v %v", allowed, restored)
		}
		// Stale writes and clears must not shorten a newer pause.
		if err := r.SaveCooldown(ctx, "fixture", providerbreaker.Cooldown{Until: now, Failures: 1}); err != nil {
			t.Fatal(err)
		}
		if err := r.ClearCooldown(ctx, "fixture", now); err != nil {
			t.Fatal(err)
		}
		states, err := r.LoadCooldowns(ctx)
		if err != nil || !states["fixture"].Until.After(now) {
			t.Fatalf("new pause overwritten: %v %v", states, err)
		}
	})
}
