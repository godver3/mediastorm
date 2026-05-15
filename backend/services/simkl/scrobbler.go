package simkl

import (
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/models"
)

// UserService provides access to user profile data for scrobbling.
type UserService interface {
	Get(id string) (models.User, bool)
}

// Scrobbler syncs watched state to Simkl using per-profile account credentials.
type Scrobbler struct {
	client        *Client
	configManager *config.Manager
	userService   UserService

	mu          sync.Mutex
	recentStops map[string]time.Time
}

func NewScrobbler(client *Client, configManager *config.Manager) *Scrobbler {
	return &Scrobbler{
		client:        client,
		configManager: configManager,
		recentStops:   make(map[string]time.Time),
	}
}

func (s *Scrobbler) SetUserService(userService UserService) {
	s.userService = userService
}

func (s *Scrobbler) IsEnabled() bool {
	settings, err := s.configManager.Load()
	if err != nil {
		return false
	}
	for _, account := range settings.Simkl.Accounts {
		if account.ClientID != "" && account.AccessToken != "" {
			return true
		}
	}
	return false
}

func (s *Scrobbler) IsEnabledForUser(userID string) bool {
	account := s.getAccountForUser(userID)
	return account != nil && account.ClientID != "" && account.AccessToken != ""
}

func (s *Scrobbler) getAccountForUser(userID string) *config.SimklAccount {
	if s.userService == nil {
		return nil
	}

	user, ok := s.userService.Get(userID)
	if !ok || user.SimklAccountID == "" {
		return nil
	}

	settings, err := s.configManager.Load()
	if err != nil {
		return nil
	}

	return settings.Simkl.GetAccountByID(user.SimklAccountID)
}

func (s *Scrobbler) ScrobbleMovie(userID string, tmdbID, tvdbID int, imdbID string, watchedAt time.Time) error {
	account := s.getAccountForUser(userID)
	if account == nil || account.ClientID == "" || account.AccessToken == "" {
		return nil
	}
	if s.wasRecentlyStopped(userID, "movie", tmdbID, tvdbID, imdbID, 0, 0) {
		return nil
	}

	req := SyncHistoryRequest{
		Movies: []SyncHistoryMovie{{
			WatchedAt: watchedAt.UTC().Format(time.RFC3339),
			IDs:       IDs{TMDB: tmdbID, TVDB: tvdbID, IMDB: imdbID},
		}},
	}

	log.Printf("[simkl] syncing watched movie for user %s (imdb=%s tmdb=%d tvdb=%d)", userID, imdbID, tmdbID, tvdbID)
	return s.client.SyncHistory(account.ClientID, account.AccessToken, req)
}

func (s *Scrobbler) ScrobbleEpisode(userID string, showTVDBID, season, episode int, watchedAt time.Time) error {
	account := s.getAccountForUser(userID)
	if account == nil || account.ClientID == "" || account.AccessToken == "" {
		return nil
	}
	if showTVDBID <= 0 || season <= 0 || episode <= 0 {
		return nil
	}
	if s.wasRecentlyStopped(userID, "episode", 0, showTVDBID, "", season, episode) {
		return nil
	}

	req := SyncHistoryRequest{
		Shows: []SyncHistoryShow{{
			IDs: IDs{TVDB: showTVDBID},
			Seasons: []SyncHistorySeason{{
				Number: season,
				Episodes: []SyncHistoryEpisode{{
					Number:    episode,
					WatchedAt: watchedAt.UTC().Format(time.RFC3339),
				}},
			}},
		}},
	}

	log.Printf("[simkl] syncing watched episode for user %s (tvdb=%d s%02de%02d)", userID, showTVDBID, season, episode)
	return s.client.SyncHistory(account.ClientID, account.AccessToken, req)
}

func (s *Scrobbler) noteRecentStop(userID string, update models.PlaybackProgressUpdate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupRecentStopsLocked(time.Now())
	s.recentStops[recentStopKeyFromUpdate(userID, update)] = time.Now()
}

func (s *Scrobbler) wasRecentlyStopped(userID, mediaType string, tmdbID, tvdbID int, imdbID string, season, episode int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.cleanupRecentStopsLocked(now)

	keys := []string{
		recentStopKey(userID, mediaType, "tmdb", strconv.Itoa(tmdbID), season, episode),
		recentStopKey(userID, mediaType, "tvdb", strconv.Itoa(tvdbID), season, episode),
		recentStopKey(userID, mediaType, "imdb", imdbID, season, episode),
	}
	for _, key := range keys {
		if key == "" {
			continue
		}
		if stoppedAt, ok := s.recentStops[key]; ok && now.Sub(stoppedAt) <= 2*time.Minute {
			return true
		}
	}
	return false
}

func (s *Scrobbler) cleanupRecentStopsLocked(now time.Time) {
	for key, stoppedAt := range s.recentStops {
		if now.Sub(stoppedAt) > 5*time.Minute {
			delete(s.recentStops, key)
		}
	}
}

func recentStopKeyFromUpdate(userID string, update models.PlaybackProgressUpdate) string {
	if update.MediaType == "episode" {
		if v, ok := update.ExternalIDs["tvdb"]; ok && v != "" {
			return recentStopKey(userID, update.MediaType, "tvdb", v, update.SeasonNumber, update.EpisodeNumber)
		}
	}
	for _, provider := range []string{"tmdb", "tvdb", "imdb"} {
		if v, ok := update.ExternalIDs[provider]; ok && v != "" {
			return recentStopKey(userID, update.MediaType, provider, v, update.SeasonNumber, update.EpisodeNumber)
		}
	}
	return recentStopKey(userID, update.MediaType, "item", strings.ToLower(update.ItemID), update.SeasonNumber, update.EpisodeNumber)
}

func recentStopKey(userID, mediaType, provider, id string, season, episode int) string {
	if strings.TrimSpace(id) == "" || id == "0" {
		return ""
	}
	key := strings.ToLower(userID) + ":" + strings.ToLower(mediaType) + ":" + provider + ":" + strings.ToLower(id)
	if strings.ToLower(mediaType) == "episode" {
		key += ":s" + strconv.Itoa(season) + "e" + strconv.Itoa(episode)
	}
	return key
}
