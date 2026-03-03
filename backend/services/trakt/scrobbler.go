package trakt

import (
	"log"
	"time"

	"novastream/config"
	"novastream/models"
)

// UserService provides access to user profile data for scrobbling.
type UserService interface {
	Get(id string) (models.User, bool)
}

// Scrobbler syncs watch history to Trakt using per-profile account credentials.
type Scrobbler struct {
	client        *Client
	configManager *config.Manager
	userService   UserService
}

// NewScrobbler creates a new Trakt scrobbler.
func NewScrobbler(client *Client, configManager *config.Manager) *Scrobbler {
	return &Scrobbler{
		client:        client,
		configManager: configManager,
	}
}

// SetUserService sets the user service for looking up profile Trakt account associations.
func (s *Scrobbler) SetUserService(userService UserService) {
	s.userService = userService
}

// IsEnabled returns whether scrobbling is enabled for any account.
// This is a general check - specific user scrobbling depends on their linked account.
func (s *Scrobbler) IsEnabled() bool {
	settings, err := s.configManager.Load()
	if err != nil {
		return false
	}
	// Check if any account has scrobbling enabled
	for _, account := range settings.Trakt.Accounts {
		if account.ScrobblingEnabled && account.AccessToken != "" {
			return true
		}
	}
	return false
}

// IsEnabledForUser returns whether scrobbling is enabled for a specific user.
func (s *Scrobbler) IsEnabledForUser(userID string) bool {
	account := s.getAccountForUser(userID)
	return account != nil && account.ScrobblingEnabled && account.AccessToken != ""
}

// getAccountForUser returns the Trakt account associated with the given user, or nil if none.
func (s *Scrobbler) getAccountForUser(userID string) *config.TraktAccount {
	if s.userService == nil {
		return nil
	}

	user, ok := s.userService.Get(userID)
	if !ok || user.TraktAccountID == "" {
		return nil
	}

	settings, err := s.configManager.Load()
	if err != nil {
		return nil
	}

	return settings.Trakt.GetAccountByID(user.TraktAccountID)
}

// getAccessTokenForUser returns a valid access token for the user's Trakt account, refreshing if needed.
func (s *Scrobbler) getAccessTokenForUser(userID string) (string, error) {
	account := s.getAccountForUser(userID)
	if account == nil {
		return "", nil
	}

	return s.client.EnsureValidToken(account, s.configManager)
}

// ScrobbleMovie syncs a watched movie to Trakt for the given user.
func (s *Scrobbler) ScrobbleMovie(userID string, tmdbID, tvdbID int, imdbID string, watchedAt time.Time) error {
	if !s.IsEnabledForUser(userID) {
		log.Printf("[trakt] scrobbling not enabled for user %s", userID)
		return nil
	}

	accessToken, err := s.getAccessTokenForUser(userID)
	if err != nil || accessToken == "" {
		return err
	}

	// Set client credentials for this account
	account := s.getAccountForUser(userID)
	if account != nil {
		s.client.UpdateCredentials(account.ClientID, account.ClientSecret)
	}

	watchedAtStr := watchedAt.UTC().Format(time.RFC3339)
	return s.client.AddMovieToHistory(accessToken, tmdbID, tvdbID, imdbID, watchedAtStr)
}

// ScrobbleEpisode syncs a watched episode to Trakt using show TVDB ID + season/episode for the given user.
func (s *Scrobbler) ScrobbleEpisode(userID string, showTVDBID, season, episode int, watchedAt time.Time) error {
	if !s.IsEnabledForUser(userID) {
		log.Printf("[trakt] scrobbling not enabled for user %s", userID)
		return nil
	}

	accessToken, err := s.getAccessTokenForUser(userID)
	if err != nil || accessToken == "" {
		return err
	}

	// Set client credentials for this account
	account := s.getAccountForUser(userID)
	if account != nil {
		s.client.UpdateCredentials(account.ClientID, account.ClientSecret)
	}

	watchedAtStr := watchedAt.UTC().Format(time.RFC3339)
	return s.client.AddEpisodeToHistory(accessToken, showTVDBID, season, episode, watchedAtStr)
}

// ScrobbleMovieLegacy is for backward compatibility - scrobbles without user context.
// Deprecated: Use ScrobbleMovie with userID instead.
func (s *Scrobbler) ScrobbleMovieLegacy(tmdbID, tvdbID int, imdbID string, watchedAt time.Time) error {
	settings, err := s.configManager.Load()
	if err != nil {
		return err
	}

	// Find first account with scrobbling enabled
	for _, account := range settings.Trakt.Accounts {
		if account.ScrobblingEnabled && account.AccessToken != "" {
			s.client.UpdateCredentials(account.ClientID, account.ClientSecret)
			watchedAtStr := watchedAt.UTC().Format(time.RFC3339)
			return s.client.AddMovieToHistory(account.AccessToken, tmdbID, tvdbID, imdbID, watchedAtStr)
		}
	}

	return nil
}

// ScrobbleEpisodeLegacy is for backward compatibility - scrobbles without user context.
// Deprecated: Use ScrobbleEpisode with userID instead.
func (s *Scrobbler) ScrobbleEpisodeLegacy(showTVDBID, season, episode int, watchedAt time.Time) error {
	settings, err := s.configManager.Load()
	if err != nil {
		return err
	}

	// Find first account with scrobbling enabled
	for _, account := range settings.Trakt.Accounts {
		if account.ScrobblingEnabled && account.AccessToken != "" {
			s.client.UpdateCredentials(account.ClientID, account.ClientSecret)
			watchedAtStr := watchedAt.UTC().Format(time.RFC3339)
			return s.client.AddEpisodeToHistory(account.AccessToken, showTVDBID, season, episode, watchedAtStr)
		}
	}

	return nil
}
