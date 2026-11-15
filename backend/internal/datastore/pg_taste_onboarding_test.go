package datastore

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"novastream/models"
	"novastream/services/tasteonboarding"
	"novastream/services/titleratings"
)

func TestTasteOnboardingPostgresMigrationEditsSkipConcurrencyAndCascade(t *testing.T) {
	raw := os.Getenv("TASTE_ONBOARDING_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("disposable PostgreSQL fixture required")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "postgresql" || u.Host != "" || !strings.HasPrefix(u.Query().Get("host"), "/") || u.User == nil || !strings.HasPrefix(u.User.Username(), "synthetic_") || u.Path != "/postgres" && !strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "synthetic_") {
		t.Fatal("a synthetic Unix-socket fixture database is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatal("connect to fixture")
	}
	defer admin.Close()
	for _, upgrade := range []bool{false, true} {
		name := "fresh"
		if upgrade {
			name = "upgrade71"
		}
		t.Run(name, func(t *testing.T) {
			dbName := fmt.Sprintf("synthetic_taste_setup_%d", time.Now().UnixNano())
			if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{dbName}.Sanitize()); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{dbName}.Sanitize()+" WITH (FORCE)"); err != nil {
					t.Error("remove own fixture database")
				}
			}()
			target := *u
			target.Path = "/" + dbName
			pool, err := pgxpool.New(ctx, target.String())
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			if upgrade {
				goose.SetBaseFS(embedMigrations)
				if err := goose.SetDialect("postgres"); err != nil {
					t.Fatal(err)
				}
				db := stdlib.OpenDBFromPool(pool)
				err = goose.UpToContext(ctx, db, "migrations", 71)
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := runMigrations(ctx, pool); err != nil {
				t.Fatal(err)
			}
			if err := runMigrations(ctx, pool); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO accounts(id,username,password_hash) VALUES('fixture-account','synthetic-taste-account','fixture-hash'); INSERT INTO users(id,account_id,name) VALUES('first','fixture-account','Synthetic First'),('second','fixture-account','Synthetic Second')`); err != nil {
				t.Fatal(err)
			}
			repo := &pgTasteOnboardingRepo{pool: pool}
			service := tasteonboarding.New(repo)
			movie := models.Title{ID: "tmdb:movie:7", TMDBID: 7, IMDBID: "tt0000007", MediaType: "movie", Name: "Synthetic Film", Year: 2000}
			ratings := titleratings.New(&pgTitleRatingRepo{pool: pool})
			if err := ratings.Put(ctx, "first", movie, "dislike"); err != nil {
				t.Fatal(err)
			}
			if err := service.Save(ctx, "first", "manual", []models.Title{movie}); err != nil {
				t.Fatal(err)
			}
			before, err := service.Get(ctx, "first")
			if err != nil || len(before.Interests) != 1 {
				t.Fatal("selection missing")
			}
			sparse := models.Title{ID: "tt0000007", IMDBID: "tt0000007", MediaType: "movie", Name: "Current Name"}
			if err := service.Save(ctx, "first", "manual", []models.Title{sparse}); err != nil {
				t.Fatal(err)
			}
			if err := service.Skip(ctx, "first"); err != nil {
				t.Fatal(err)
			}
			state, err := service.Get(ctx, "first")
			if err != nil || state.Status != "skipped" || state.Source != "manual" || len(state.Interests) != 1 || !state.Interests[0].AddedAt.Equal(before.Interests[0].AddedAt) || state.Interests[0].Title.TMDBID != 7 {
				t.Fatal("edit or skip lost verified identity/date")
			}
			reopened, err := New(ctx, target.String())
			if err != nil {
				t.Fatal(err)
			}
			persisted, err := reopened.TasteOnboarding().Get(ctx, "first")
			reopened.Close()
			if err != nil || !reflect.DeepEqual(persisted, state) {
				t.Fatal("reopen lost setup data")
			}
			other, err := service.Get(ctx, "second")
			if err != nil || other.Status != "new" || len(other.Interests) != 0 {
				t.Fatal("selection crossed profiles")
			}
			invalid := state
			invalid.Status = "invalid"
			if repo.Save(ctx, "first", invalid) == nil {
				t.Fatal("database accepted invalid setup state")
			}
			stateAfter, err := service.Get(ctx, "first")
			if err != nil || !reflect.DeepEqual(stateAfter, state) {
				t.Fatal("failed transaction changed setup")
			}
			failures := make(chan error, 20)
			var wg sync.WaitGroup
			for i := 0; i < 20; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					if i%2 == 0 {
						failures <- service.Skip(ctx, "first")
					} else {
						failures <- service.Save(ctx, "first", "manual", []models.Title{movie})
					}
				}(i)
			}
			wg.Wait()
			close(failures)
			for err := range failures {
				if err != nil {
					t.Fatal(err)
				}
			}
			state, err = service.Get(ctx, "first")
			if err != nil || len(state.Interests) != 1 || state.Source != "manual" || state.Status != "completed" && state.Status != "skipped" {
				t.Fatal("concurrent save/skip lost interests")
			}
			votes, err := ratings.List(ctx, "first")
			if err != nil || len(votes) != 1 || votes[0].Value != "dislike" {
				t.Fatal("taste setup changed explicit rating")
			}
			if err := service.Save(ctx, "first", "manual", []models.Title{}); err != nil {
				t.Fatal(err)
			}
			state, err = service.Get(ctx, "first")
			if err != nil || state.Status != "completed" || len(state.Interests) != 0 {
				t.Fatal("empty edit retained interests")
			}
			if err := service.Delete(ctx, "first"); err != nil {
				t.Fatal(err)
			}
			state, _ = service.Get(ctx, "first")
			if state.Status != "new" {
				t.Fatal("clear retained setup status")
			}
			if err := service.Save(ctx, "first", "history", []models.Title{movie}); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id='first'`); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM profile_taste_onboarding`).Scan(&count); err != nil || count != 0 {
				t.Fatal("profile deletion retained setup data")
			}
		})
	}
}
