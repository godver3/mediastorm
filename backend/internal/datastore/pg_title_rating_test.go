package datastore

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"novastream/models"
	"novastream/services/titleratings"
)

// Only a disposable, explicitly configured Unix-socket fixture database may
// create databases for this integration test. No default deployment DSN is used.
func TestTitleRatingsPostgresMigrationPersistenceConcurrencyAndCascade(t *testing.T) {
	raw := os.Getenv("TITLE_RATINGS_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("disposable PostgreSQL fixture required")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "postgresql" || u.Host != "" || !strings.HasPrefix(u.Query().Get("host"), "/") || (u.User == nil || !strings.HasPrefix(u.User.Username(), "synthetic_")) || (u.Path != "/postgres" && !strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "synthetic_")) {
		t.Fatal("a synthetic Unix-socket fixture database is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatal("connect to fixture")
	}
	defer admin.Close()
	for _, from70 := range []bool{false, true} {
		name := "fresh"
		if from70 {
			name = "upgrade70"
		}
		t.Run(name, func(t *testing.T) {
			dbName := fmt.Sprintf("synthetic_title_ratings_%d", time.Now().UnixNano())
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
			if from70 {
				goose.SetBaseFS(embedMigrations)
				if err := goose.SetDialect("postgres"); err != nil {
					t.Fatal(err)
				}
				db := stdlib.OpenDBFromPool(pool)
				err = goose.UpToContext(ctx, db, "migrations", 70)
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
			if _, err := pool.Exec(ctx, `INSERT INTO accounts(id,username,password_hash) VALUES ('fixture-account','synthetic-rating-account','fixture-hash'); INSERT INTO users(id,account_id,name) VALUES ('first','fixture-account','Synthetic First'),('second','fixture-account','Synthetic Second')`); err != nil {
				t.Fatal(err)
			}
			repo := &pgTitleRatingRepo{pool: pool}
			service := titleratings.New(repo)
			film := models.Title{ID: "tmdb:movie:7", TMDBID: 7, IMDBID: "tt0000007", MediaType: "movie", Name: "Synthetic Film"}
			if err := service.Put(ctx, "first", film, "love"); err != nil {
				t.Fatal(err)
			}
			// A different server representation of the same verified title must replace
			// the preference rather than create a second, contradictory vote.
			alternate := film
			alternate.ID = "tt0000007"
			if err := service.Put(ctx, "first", alternate, "dislike"); err != nil {
				t.Fatal(err)
			}
			reopened, err := New(ctx, target.String())
			if err != nil {
				t.Fatal(err)
			}
			persisted, err := reopened.TitleRatings().List(ctx, "first")
			reopened.Close()
			if err != nil || len(persisted) != 1 || persisted[0].Value != "dislike" || titleratings.Match(persisted, film) != "dislike" {
				t.Fatal("reopened connection lost alias preference")
			}
			other, err := repo.List(ctx, "second")
			if err != nil || len(other) != 0 {
				t.Fatal("profile preference crossed boundary")
			}
			series := film
			series.MediaType = "series"
			series.ID = "tmdb:tv:7"
			if err := service.Put(ctx, "first", series, "like"); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			failures := make(chan error, 20)
			for i := 0; i < 20; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					target := film
					if i%2 == 0 {
						target = alternate
					}
					failures <- service.Put(ctx, "first", target, []string{"like", "love", "dislike"}[i%3])
				}(i)
			}
			wg.Wait()
			close(failures)
			for err := range failures {
				if err != nil {
					t.Fatal(err)
				}
			}
			items, err := repo.List(ctx, "first")
			if err != nil || len(items) != 2 {
				t.Fatal("concurrent aliases created duplicate votes")
			}
			before := titleratings.Match(items, film)
			invalid := models.TitleRating{Key: titleratings.Aliases(film)[0], Aliases: titleratings.Aliases(film), Title: film, Value: "invalid", UpdatedAt: time.Now()}
			if repo.Put(ctx, "first", invalid) == nil {
				t.Fatal("database accepted invalid rating")
			}
			items, err = repo.List(ctx, "first")
			if err != nil || len(items) != 2 || titleratings.Match(items, film) != before {
				t.Fatal("failed transaction lost previous preference")
			}
			if err := service.Put(ctx, "first", alternate, "none"); err != nil {
				t.Fatal(err)
			}
			items, err = repo.List(ctx, "first")
			if err != nil || len(items) != 1 || titleratings.Match(items, series) != "like" {
				t.Fatal("clearing movie affected series preference")
			}
			if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id='first'`); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM profile_title_ratings`).Scan(&count); err != nil || count != 0 {
				t.Fatal("profile deletion retained preferences")
			}
		})
	}
}
