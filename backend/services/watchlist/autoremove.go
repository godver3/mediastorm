package watchlist

import (
	"context"
	"log"
	"strings"
	"time"

	"novastream/services/history"
)

// Auto-remove modes for movies and shows.
const (
	AutoRemoveOff     = "off"
	AutoRemoveWatched = "watched"
	AutoRemoveStarted = "started"
)

// NormalizeAutoRemoveMode maps unknown/empty values to off.
func NormalizeAutoRemoveMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case AutoRemoveWatched:
		return AutoRemoveWatched
	case AutoRemoveStarted:
		return AutoRemoveStarted
	default:
		return AutoRemoveOff
	}
}

// AutoRemoveModes resolves a profile's effective movie and show modes
// (profile override, else global).
type AutoRemoveModes func(userID string) (movies, shows string)

// SeriesCompletionChecker reports whether every aired non-special episode of
// a series is watched.
type SeriesCompletionChecker func(ctx context.Context, userID, seriesID, seriesName string, externalIDs map[string]string) (bool, error)

// AutoRemover drops titles from a profile's watchlist on watch events.
type AutoRemover struct {
	watchlist      *Service
	modes          AutoRemoveModes
	seriesComplete SeriesCompletionChecker
}

// NewAutoRemover builds an AutoRemover.
func NewAutoRemover(watchlist *Service, modes AutoRemoveModes, seriesComplete SeriesCompletionChecker) *AutoRemover {
	return &AutoRemover{watchlist: watchlist, modes: modes, seriesComplete: seriesComplete}
}

// HandleWatchEvent is a history.WatchEventObserver.
func (a *AutoRemover) HandleWatchEvent(userID string, event history.WatchEvent) {
	if a == nil || a.watchlist == nil || a.modes == nil {
		return
	}
	movies, shows := a.modes(userID)
	switch event.MediaType {
	case "movie":
		if !modeFires(NormalizeAutoRemoveMode(movies), event.Kind) {
			return
		}
		a.remove(userID, "movie", event.ItemID, event.ExternalIDs, event)
	case "episode":
		mode := NormalizeAutoRemoveMode(shows)
		if !modeFires(mode, event.Kind) {
			return
		}
		seriesIDs := seriesExternalIDs(event.ExternalIDs)
		if strings.TrimSpace(event.SeriesID) == "" && len(seriesIDs) == 0 {
			return
		}
		if !a.watchlist.Contains(userID, "series", event.SeriesID, seriesIDs) {
			return
		}
		if mode == AutoRemoveWatched {
			if a.seriesComplete == nil {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			done, err := a.seriesComplete(ctx, userID, event.SeriesID, event.SeriesName, seriesIDs)
			if err != nil {
				log.Printf("[watchlist] auto-remove: series completion check failed user=%s series=%s: %v", userID, event.SeriesID, err)
				return
			}
			if !done {
				return
			}
		}
		a.remove(userID, "series", event.SeriesID, seriesIDs, event)
	}
}

// modeFires: "started" also fires on watched (a title watched without a
// started event, e.g. marked watched, has still been started).
func modeFires(mode string, kind history.WatchEventKind) bool {
	switch mode {
	case AutoRemoveStarted:
		return kind == history.WatchEventStarted || kind == history.WatchEventWatched
	case AutoRemoveWatched:
		return kind == history.WatchEventWatched
	}
	return false
}

func (a *AutoRemover) remove(userID, mediaType, id string, externalIDs map[string]string, event history.WatchEvent) {
	removed, err := a.watchlist.RemoveMatching(userID, mediaType, id, externalIDs)
	if err != nil {
		log.Printf("[watchlist] auto-remove failed user=%s %s=%s: %v", userID, mediaType, id, err)
		return
	}
	if removed {
		log.Printf("[watchlist] auto-removed user=%s %s=%s on %s", userID, mediaType, id, event.Kind)
	}
}

func seriesExternalIDs(ids map[string]string) map[string]string {
	out := make(map[string]string, 3)
	for _, key := range []string{"tmdb", "tvdb", "imdb"} {
		if v := strings.TrimSpace(ids[key]); v != "" {
			out[key] = v
		}
	}
	return out
}
