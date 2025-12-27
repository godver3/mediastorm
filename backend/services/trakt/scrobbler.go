package trakt

import (
	"time"

	"novastream/config"
)

// Scrobbler syncs watch history to Trakt.
type Scrobbler struct {
	client        *Client
	configManager *config.Manager
}

// NewScrobbler creates a new Trakt scrobbler.
func NewScrobbler(client *Client, configManager *config.Manager) *Scrobbler {
	return &Scrobbler{
		client:        client,
		configManager: configManager,
	}
}

// IsEnabled returns whether scrobbling is currently enabled.
func (s *Scrobbler) IsEnabled() bool {
	settings, err := s.configManager.Load()
	if err != nil {
		return false
	}
	return settings.Trakt.ScrobblingEnabled && settings.Trakt.AccessToken != ""
}

// getAccessToken returns a valid access token, refreshing if needed.
func (s *Scrobbler) getAccessToken() (string, error) {
	settings, err := s.configManager.Load()
	if err != nil {
		return "", err
	}

	if settings.Trakt.AccessToken == "" {
		return "", nil
	}

	// Update client with current credentials
	s.client.UpdateCredentials(settings.Trakt.ClientID, settings.Trakt.ClientSecret)

	// Check if token needs refresh (within 1 hour of expiry)
	if settings.Trakt.ExpiresAt > 0 {
		expiresIn := settings.Trakt.ExpiresAt - time.Now().Unix()
		if expiresIn < 3600 && settings.Trakt.RefreshToken != "" {
			token, err := s.client.RefreshAccessToken(settings.Trakt.RefreshToken)
			if err != nil {
				return "", err
			}

			// Update settings with new tokens
			settings.Trakt.AccessToken = token.AccessToken
			settings.Trakt.RefreshToken = token.RefreshToken
			settings.Trakt.ExpiresAt = token.CreatedAt + int64(token.ExpiresIn)

			if err := s.configManager.Save(settings); err != nil {
				return "", err
			}

			return token.AccessToken, nil
		}
	}

	return settings.Trakt.AccessToken, nil
}

// ScrobbleMovie syncs a watched movie to Trakt.
func (s *Scrobbler) ScrobbleMovie(tmdbID, tvdbID int, imdbID string, watchedAt time.Time) error {
	accessToken, err := s.getAccessToken()
	if err != nil || accessToken == "" {
		return err
	}

	watchedAtStr := watchedAt.UTC().Format(time.RFC3339)
	return s.client.AddMovieToHistory(accessToken, tmdbID, tvdbID, imdbID, watchedAtStr)
}

// ScrobbleEpisode syncs a watched episode to Trakt using show TVDB ID + season/episode.
func (s *Scrobbler) ScrobbleEpisode(showTVDBID, season, episode int, watchedAt time.Time) error {
	accessToken, err := s.getAccessToken()
	if err != nil || accessToken == "" {
		return err
	}

	watchedAtStr := watchedAt.UTC().Format(time.RFC3339)
	return s.client.AddEpisodeToHistory(accessToken, showTVDBID, season, episode, watchedAtStr)
}
