package watchlist_test

import (
	"context"
	"testing"

	"novastream/models"
	"novastream/services/history"
	"novastream/services/watchlist"
)

func newAutoRemoveFixture(t *testing.T) *watchlist.Service {
	t.Helper()
	svc, err := watchlist.NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []models.WatchlistUpsert{
		{ID: "tmdb:movie:603", MediaType: "movie", Name: "The Matrix", ExternalIDs: map[string]string{"tmdb": "603", "imdb": "tt0133093"}},
		{ID: "tmdb:tv:1396", MediaType: "series", Name: "Breaking Bad", ExternalIDs: map[string]string{"tmdb": "1396", "tvdb": "81189"}},
	} {
		if _, err := svc.AddOrUpdate("u1", in); err != nil {
			t.Fatal(err)
		}
	}
	return svc
}

func modes(movies, shows string) watchlist.AutoRemoveModes {
	return func(string) (string, string) { return movies, shows }
}

func complete(done bool) watchlist.SeriesCompletionChecker {
	return func(context.Context, string, string, string, map[string]string) (bool, error) { return done, nil }
}

var (
	movieStarted = history.WatchEvent{Kind: history.WatchEventStarted, MediaType: "movie", ItemID: "imdb:tt0133093", ExternalIDs: map[string]string{"imdb": "tt0133093"}}
	movieWatched = history.WatchEvent{Kind: history.WatchEventWatched, MediaType: "movie", ItemID: "tmdb:movie:603"}
	epStarted    = history.WatchEvent{Kind: history.WatchEventStarted, MediaType: "episode", ItemID: "x", SeriesID: "tvdb:series:81189", SeasonNumber: 1, EpisodeNumber: 1, ExternalIDs: map[string]string{"tvdb": "81189"}}
	epWatched    = history.WatchEvent{Kind: history.WatchEventWatched, MediaType: "episode", ItemID: "x", SeriesID: "tmdb:tv:1396", SeasonNumber: 5, EpisodeNumber: 16}
)

func TestAutoRemove(t *testing.T) {
	cases := []struct {
		name          string
		movies, shows string
		done          bool
		event         history.WatchEvent
		mediaType, id string
		wantRemoved   bool
	}{
		{"off keeps movie", "off", "off", false, movieWatched, "movie", "tmdb:movie:603", false},
		{"empty mode is off", "", "", false, movieWatched, "movie", "tmdb:movie:603", false},
		{"movie watched mode ignores start", "watched", "off", false, movieStarted, "movie", "tmdb:movie:603", false},
		{"movie watched mode removes on watched", "watched", "off", false, movieWatched, "movie", "tmdb:movie:603", true},
		{"movie started mode removes on start via imdb", "started", "off", false, movieStarted, "movie", "tmdb:movie:603", true},
		{"movie started mode removes on watched", "started", "off", false, movieWatched, "movie", "tmdb:movie:603", true},
		{"show started removes via tvdb", "off", "started", false, epStarted, "series", "tmdb:tv:1396", true},
		{"show watched keeps when incomplete", "off", "watched", false, epWatched, "series", "tmdb:tv:1396", false},
		{"show watched removes when complete", "off", "watched", true, epWatched, "series", "tmdb:tv:1396", true},
		{"show watched ignores start", "off", "watched", true, epStarted, "series", "tmdb:tv:1396", false},
		{"movie mode does not touch shows", "started", "off", true, epStarted, "series", "tmdb:tv:1396", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newAutoRemoveFixture(t)
			watchlist.NewAutoRemover(svc, modes(tc.movies, tc.shows), complete(tc.done)).HandleWatchEvent("u1", tc.event)
			if got := !svc.Contains("u1", tc.mediaType, tc.id, nil); got != tc.wantRemoved {
				t.Fatalf("removed=%v want %v", got, tc.wantRemoved)
			}
		})
	}
}
