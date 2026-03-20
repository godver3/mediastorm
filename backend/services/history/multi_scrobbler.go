package history

import (
	"log"
	"time"

	"novastream/models"
)

// MultiScrobbler fans out scrobble calls to multiple providers.
// Implements TraktScrobbler.
type MultiScrobbler struct {
	scrobblers []TraktScrobbler
}

// NewMultiScrobbler creates a multi-scrobbler wrapping the given providers.
func NewMultiScrobbler(scrobblers ...TraktScrobbler) *MultiScrobbler {
	return &MultiScrobbler{scrobblers: scrobblers}
}

func (m *MultiScrobbler) ScrobbleMovie(userID string, tmdbID, tvdbID int, imdbID string, watchedAt time.Time) error {
	var firstErr error
	for _, s := range m.scrobblers {
		if err := s.ScrobbleMovie(userID, tmdbID, tvdbID, imdbID, watchedAt); err != nil {
			log.Printf("[multi-scrobbler] movie scrobble error: %v", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (m *MultiScrobbler) ScrobbleEpisode(userID string, showTVDBID, season, episode int, watchedAt time.Time) error {
	var firstErr error
	for _, s := range m.scrobblers {
		if err := s.ScrobbleEpisode(userID, showTVDBID, season, episode, watchedAt); err != nil {
			log.Printf("[multi-scrobbler] episode scrobble error: %v", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (m *MultiScrobbler) IsEnabled() bool {
	for _, s := range m.scrobblers {
		if s.IsEnabled() {
			return true
		}
	}
	return false
}

func (m *MultiScrobbler) IsEnabledForUser(userID string) bool {
	for _, s := range m.scrobblers {
		if s.IsEnabledForUser(userID) {
			return true
		}
	}
	return false
}

// MultiRealTimeScrobbler fans out real-time scrobble calls to multiple providers.
// Implements TraktRealTimeScrobbler.
type MultiRealTimeScrobbler struct {
	scrobblers []TraktRealTimeScrobbler
}

// NewMultiRealTimeScrobbler creates a multi-scrobbler wrapping the given providers.
func NewMultiRealTimeScrobbler(scrobblers ...TraktRealTimeScrobbler) *MultiRealTimeScrobbler {
	return &MultiRealTimeScrobbler{scrobblers: scrobblers}
}

func (m *MultiRealTimeScrobbler) HandleProgressUpdate(userID string, update models.PlaybackProgressUpdate, percentWatched float64) {
	for _, s := range m.scrobblers {
		s.HandleProgressUpdate(userID, update, percentWatched)
	}
}

func (m *MultiRealTimeScrobbler) StopSession(userID string, update models.PlaybackProgressUpdate, percentWatched float64) {
	for _, s := range m.scrobblers {
		s.StopSession(userID, update, percentWatched)
	}
}
