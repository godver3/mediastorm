package history

import (
	"context"
	"strings"
	"time"

	"novastream/models"
)

// WatchEventKind distinguishes the watch events reported to a WatchEventObserver.
type WatchEventKind string

const (
	// WatchEventStarted fires when a movie or episode gets its first playback
	// progress row (playback started).
	WatchEventStarted WatchEventKind = "started"
	// WatchEventWatched fires when a movie or episode flips from unwatched to
	// watched (auto-marked at 90%, toggled, or bulk marked; not imports).
	WatchEventWatched WatchEventKind = "watched"
)

// WatchEvent describes one started/watched transition for a movie or episode.
type WatchEvent struct {
	Kind          WatchEventKind
	MediaType     string // "movie" or "episode"
	ItemID        string
	ExternalIDs   map[string]string
	SeriesID      string
	SeriesName    string
	SeasonNumber  int
	EpisodeNumber int
}

// WatchEventObserver receives watch events. It runs on its own goroutine,
// never under the history lock.
type WatchEventObserver func(userID string, event WatchEvent)

// SetWatchEventObserver registers the observer for started/watched events.
func (s *Service) SetWatchEventObserver(fn WatchEventObserver) {
	s.changeMu.Lock()
	defer s.changeMu.Unlock()
	s.watchEventObserver = fn
}

func (s *Service) emitWatchEvent(userID string, event WatchEvent) {
	s.changeMu.RLock()
	fn := s.watchEventObserver
	s.changeMu.RUnlock()
	if fn == nil {
		return
	}
	event.MediaType = strings.ToLower(strings.TrimSpace(event.MediaType))
	if event.MediaType != "movie" && event.MediaType != "episode" {
		return
	}
	go fn(userID, event)
}

func watchEventFromItem(kind WatchEventKind, item models.WatchHistoryItem) WatchEvent {
	return WatchEvent{
		Kind:          kind,
		MediaType:     item.MediaType,
		ItemID:        item.ItemID,
		ExternalIDs:   item.ExternalIDs,
		SeriesID:      item.SeriesID,
		SeriesName:    item.SeriesName,
		SeasonNumber:  item.SeasonNumber,
		EpisodeNumber: item.EpisodeNumber,
	}
}

func watchEventFromProgress(kind WatchEventKind, update models.PlaybackProgressUpdate) WatchEvent {
	return WatchEvent{
		Kind:          kind,
		MediaType:     update.MediaType,
		ItemID:        update.ItemID,
		ExternalIDs:   update.ExternalIDs,
		SeriesID:      update.SeriesID,
		SeriesName:    update.SeriesName,
		SeasonNumber:  update.SeasonNumber,
		EpisodeNumber: update.EpisodeNumber,
	}
}

// IsSeriesFullyWatched reports whether the user has watched every aired,
// non-special (season > 0) episode of the series. A series with no aired
// regular episodes is never fully watched.
func (s *Service) IsSeriesFullyWatched(ctx context.Context, userID, seriesID, seriesName string, externalIDs map[string]string) (bool, error) {
	details, err := s.getSeriesMetadataWithCache(ctx, seriesID, seriesName, externalIDs)
	if err != nil || details == nil {
		return false, err
	}
	history, err := s.ListWatchHistory(userID)
	if err != nil {
		return false, err
	}
	idx := NewSeriesProgressIndex(seriesID, externalIDs, nil, history)
	return seriesFullyWatched(details, idx, time.Now()), nil
}

func seriesFullyWatched(details *models.SeriesDetails, idx *SeriesProgressIndex, now time.Time) bool {
	aired := 0
	for _, season := range details.Seasons {
		if season.Number <= 0 {
			continue
		}
		for _, ep := range season.Episodes {
			if ep.SeasonNumber <= 0 || ep.EpisodeNumber <= 0 {
				continue
			}
			ref := &models.EpisodeReference{AirDate: ep.AiredDate, AirDateTimeUTC: ep.AiredDateTimeUTC}
			if !episodeHasAired(ref, now) {
				continue
			}
			aired++
			if !idx.Watched(ep.SeasonNumber, ep.EpisodeNumber) {
				return false
			}
		}
	}
	return aired > 0
}

// episodeHasAired treats episodes without a parseable air date as unaired.
func episodeHasAired(ep *models.EpisodeReference, now time.Time) bool {
	if strings.TrimSpace(ep.AirDateTimeUTC) == "" && strings.TrimSpace(ep.AirDate) == "" {
		return false
	}
	if !episodeAiredAfter(ep, now) {
		// Unparseable dates also land here; require at least one to parse.
		if _, err := time.Parse(time.RFC3339, strings.TrimSpace(ep.AirDateTimeUTC)); err == nil {
			return true
		}
		_, err := time.Parse("2006-01-02", strings.TrimSpace(ep.AirDate))
		return err == nil
	}
	return false
}
