package mdblist

import (
	"log"
	"math"
	"strconv"
	"time"

	"novastream/config"
	"novastream/models"
)

// UserService provides access to user profile data for scrobbling.
type UserService interface {
	Get(id string) (models.User, bool)
}

// Scrobbler implements history.TraktScrobbler for MDBList watched-item sync.
type Scrobbler struct {
	client        *ScrobbleClient
	configManager *config.Manager
	userService   UserService
}

// NewScrobbler creates a new MDBList scrobbler.
func NewScrobbler(client *ScrobbleClient, configManager *config.Manager) *Scrobbler {
	return &Scrobbler{
		client:        client,
		configManager: configManager,
	}
}

// SetUserService sets the user service for looking up profile MDBList account associations.
func (s *Scrobbler) SetUserService(userService UserService) {
	s.userService = userService
}

// IsEnabled returns whether any MDBList account is configured for scrobbling.
func (s *Scrobbler) IsEnabled() bool {
	settings, err := s.configManager.Load()
	if err != nil {
		return false
	}
	if !settings.MDBList.Enabled {
		return false
	}
	for _, account := range settings.MDBList.Accounts {
		if account.APIKey != "" {
			return true
		}
	}
	return false
}

// IsEnabledForUser returns whether MDBList scrobbling is enabled for a specific user
// (i.e., the user is linked to an MDBList account with a valid API key).
func (s *Scrobbler) IsEnabledForUser(userID string) bool {
	account := s.getAccountForUser(userID)
	return account != nil && account.APIKey != ""
}

// getAccountForUser returns the MDBList account associated with the given user, or nil if none.
func (s *Scrobbler) getAccountForUser(userID string) *config.MDBListAccount {
	if s.userService == nil {
		return nil
	}

	user, ok := s.userService.Get(userID)
	if !ok || user.MdblistAccountID == "" {
		return nil
	}

	settings, err := s.configManager.Load()
	if err != nil {
		return nil
	}

	return settings.MDBList.GetAccountByID(user.MdblistAccountID)
}

// ScrobbleMovie syncs a watched movie to MDBList for the given user.
func (s *Scrobbler) ScrobbleMovie(userID string, tmdbID, tvdbID int, imdbID string, watchedAt time.Time) error {
	account := s.getAccountForUser(userID)
	if account == nil || account.APIKey == "" {
		return nil
	}

	s.client.UpdateAPIKey(account.APIKey)

	item := SyncWatchedMovieItem{
		IDs: ScrobbleIDs{
			IMDB: imdbID,
			TMDB: tmdbID,
		},
		WatchedAt: watchedAt.UTC().Format(time.RFC3339),
	}

	log.Printf("[mdblist-scrobble] syncing watched movie for user %s (imdb=%s tmdb=%d)", userID, imdbID, tmdbID)
	return s.client.SyncWatched(SyncWatchedRequest{
		Movies: []SyncWatchedMovieItem{item},
	})
}

// ScrobbleEpisode syncs a watched episode to MDBList for the given user.
// Note: showTVDBID is not usable with MDBList (no TVDB support). The caller's
// ExternalIDs may contain TMDB/IMDB which are extracted upstream, but this
// sync/watched path receives only the show's TVDB ID from the history service.
// We log a warning if we have no usable IDs.
func (s *Scrobbler) ScrobbleEpisode(userID string, showTVDBID, season, episode int, watchedAt time.Time) error {
	account := s.getAccountForUser(userID)
	if account == nil || account.APIKey == "" {
		return nil
	}

	s.client.UpdateAPIKey(account.APIKey)

	// The history service only passes showTVDBID for episodes, but MDBList
	// doesn't support TVDB. We have no IMDB/TMDB for the show here.
	// Log and skip — the real-time scrobble path has access to ExternalIDs
	// and will handle episode scrobbling when the user is actually watching.
	log.Printf("[mdblist-scrobble] episode sync/watched skipped for user %s (s%02de%02d) — only TVDB ID available (tvdb=%d), MDBList requires IMDB/TMDB",
		userID, season, episode, showTVDBID)
	return nil
}

// BuildScrobbleRequest converts a PlaybackProgressUpdate to an MDBList ScrobbleRequest.
func BuildScrobbleRequest(update models.PlaybackProgressUpdate, percentWatched float64) ScrobbleRequest {
	// MDBList requires at most 5 total digits in progress (e.g. 99.99, not 45.123456)
	req := ScrobbleRequest{
		Progress: math.Round(percentWatched*100) / 100,
	}

	if update.MediaType == "movie" {
		req.Movie = &ScrobbleMoviePayload{
			IDs: externalIDsToScrobbleIDs(update.ExternalIDs),
		}
	} else if update.MediaType == "episode" {
		req.Show = &ScrobbleShowPayload{
			IDs: seriesIDToScrobbleIDs(update.SeriesID, update.ExternalIDs),
			Season: &ScrobbleSeasonBlock{
				Number: update.SeasonNumber,
				Episode: &ScrobbleEpisodePayload{
					Number: update.EpisodeNumber,
				},
			},
		}
	}

	return req
}

func externalIDsToScrobbleIDs(extIDs map[string]string) ScrobbleIDs {
	ids := ScrobbleIDs{}
	if v, ok := extIDs["tmdb"]; ok {
		ids.TMDB, _ = strconv.Atoi(v)
	}
	if v, ok := extIDs["imdb"]; ok {
		ids.IMDB = v
	}
	// Note: MDBList does not recognize TVDB IDs — only IMDB and TMDB
	return ids
}

func seriesIDToScrobbleIDs(seriesID string, extIDs map[string]string) ScrobbleIDs {
	ids := externalIDsToScrobbleIDs(extIDs)

	// Fall back to parsing seriesID if no IDs found
	if ids.TMDB == 0 && ids.IMDB == "" && seriesID != "" {
		parts := splitSeriesID(seriesID)
		if len(parts) >= 3 {
			provider := parts[0]
			numericID := parts[len(parts)-1]
			switch provider {
			case "tmdb":
				ids.TMDB, _ = strconv.Atoi(numericID)
			case "imdb":
				ids.IMDB = "tt" + numericID
			// Note: TVDB not supported by MDBList — skip
			}
		}
	}

	return ids
}

func splitSeriesID(s string) []string {
	var parts []string
	start := 0
	for i := range s {
		if s[i] == ':' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}
