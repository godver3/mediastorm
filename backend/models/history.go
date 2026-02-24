package models

import "time"

// EpisodeReference captures identifying information for a specific episode.
type EpisodeReference struct {
	SeasonNumber          int       `json:"seasonNumber"`
	EpisodeNumber         int       `json:"episodeNumber"`
	AbsoluteEpisodeNumber int       `json:"absoluteEpisodeNumber,omitempty"`
	EpisodeID             string    `json:"episodeId,omitempty"`
	TvdbID                string    `json:"tvdbId,omitempty"`
	Title                 string    `json:"title,omitempty"`
	Overview              string    `json:"overview,omitempty"`
	RuntimeMinutes        int       `json:"runtimeMinutes,omitempty"`
	AirDate               string    `json:"airDate,omitempty"`
	AirDateTimeUTC        string    `json:"airDateTimeUTC,omitempty"`
	WatchedAt             time.Time `json:"watchedAt,omitempty"`
}

// SeriesWatchState tracks a user's progress for a particular series.
type SeriesWatchState struct {
	SeriesID        string                      `json:"seriesId"`
	SeriesTitle     string                      `json:"seriesTitle"`
	Overview        string                      `json:"overview,omitempty"`
	PosterURL       string                      `json:"posterUrl,omitempty"`
	BackdropURL     string                      `json:"backdropUrl,omitempty"`
	Year            int                         `json:"year,omitempty"`
	ExternalIDs     map[string]string           `json:"externalIds,omitempty"`
	UpdatedAt       time.Time                   `json:"updatedAt"`
	LastWatched     EpisodeReference            `json:"lastWatched"`
	NextEpisode     *EpisodeReference           `json:"nextEpisode,omitempty"`
	WatchedEpisodes map[string]EpisodeReference `json:"watchedEpisodes,omitempty"`
	PercentWatched  float64                     `json:"percentWatched,omitempty"`
	ResumePercent   float64                     `json:"resumePercent,omitempty"`

	// Episode counts for tracking series completion (excludes specials/season 0)
	WatchedEpisodeCount int `json:"watchedEpisodeCount,omitempty"` // Number of episodes user has watched
	TotalEpisodeCount   int `json:"totalEpisodeCount,omitempty"`   // Total released episodes in series
}

// EpisodeWatchPayload represents a request to record that a user started an episode.
type EpisodeWatchPayload struct {
	SeriesID    string            `json:"seriesId"`
	SeriesTitle string            `json:"seriesTitle"`
	PosterURL   string            `json:"posterUrl,omitempty"`
	BackdropURL string            `json:"backdropUrl,omitempty"`
	Year        int               `json:"year,omitempty"`
	ExternalIDs map[string]string `json:"externalIds,omitempty"`
	Episode     EpisodeReference  `json:"episode"`
	NextEpisode *EpisodeReference `json:"nextEpisode,omitempty"`
}

// WatchHistoryItem represents a unified watch history entry for any media (movie, episode, or series).
type WatchHistoryItem struct {
	ID          string            `json:"id"`           // mediaType:itemId (e.g., "movie:tmdb:12345" or "series:tvdb:67890:s01e02")
	MediaType   string            `json:"mediaType"`    // "movie" | "series" | "episode"
	ItemID      string            `json:"itemId"`       // The actual ID (e.g., "tmdb:12345")
	Name        string            `json:"name"`
	Year        int               `json:"year,omitempty"`
	Watched     bool              `json:"watched"`      // Manual watch flag
	WatchedAt   time.Time         `json:"watchedAt,omitempty"`
	ExternalIDs map[string]string `json:"externalIds,omitempty"`

	// Episode-specific fields
	SeasonNumber  int    `json:"seasonNumber,omitempty"`
	EpisodeNumber int    `json:"episodeNumber,omitempty"`
	SeriesID      string `json:"seriesId,omitempty"`      // Parent series ID for episodes
	SeriesName    string `json:"seriesName,omitempty"`
}

// WatchHistoryUpdate represents an update to mark an item as watched/unwatched.
type WatchHistoryUpdate struct {
	MediaType     string            `json:"mediaType"`
	ItemID        string            `json:"itemId"`
	Name          string            `json:"name,omitempty"`
	Year          int               `json:"year,omitempty"`
	Watched       *bool             `json:"watched,omitempty"`
	WatchedAt     time.Time         `json:"watchedAt,omitempty"` // Optional: use specific timestamp instead of now
	ExternalIDs   map[string]string `json:"externalIds,omitempty"`

	// Episode-specific
	SeasonNumber  int    `json:"seasonNumber,omitempty"`
	EpisodeNumber int    `json:"episodeNumber,omitempty"`
	SeriesID      string `json:"seriesId,omitempty"`
	SeriesName    string `json:"seriesName,omitempty"`
}

// PlaybackProgressUpdate represents a playback progress update from the player.
type PlaybackProgressUpdate struct {
	MediaType     string            `json:"mediaType"`    // "movie" | "episode"
	ItemID        string            `json:"itemId"`       // The media ID
	Position      float64           `json:"position"`     // Current playback position in seconds
	Duration      float64           `json:"duration"`     // Total duration in seconds
	Timestamp     time.Time         `json:"timestamp"`    // When this update was sent
	IsPaused      bool              `json:"isPaused"`     // Whether playback is currently paused
	ExternalIDs   map[string]string `json:"externalIds,omitempty"`

	// Episode-specific fields
	SeasonNumber  int    `json:"seasonNumber,omitempty"`
	EpisodeNumber int    `json:"episodeNumber,omitempty"`
	SeriesID      string `json:"seriesId,omitempty"`
	SeriesName    string `json:"seriesName,omitempty"`
	EpisodeName   string `json:"episodeName,omitempty"`

	// Movie-specific fields
	MovieName     string `json:"movieName,omitempty"`
	Year          int    `json:"year,omitempty"`
}

// PlaybackProgress stores the current playback progress for a media item.
type PlaybackProgress struct {
	ID            string            `json:"id"`           // mediaType:itemId
	MediaType     string            `json:"mediaType"`    // "movie" | "episode"
	ItemID        string            `json:"itemId"`       // The media ID
	Position      float64           `json:"position"`     // Last known position in seconds
	Duration      float64           `json:"duration"`     // Total duration in seconds
	PercentWatched float64          `json:"percentWatched"` // Position/Duration * 100
	UpdatedAt     time.Time         `json:"updatedAt"`    // Last update time
	IsPaused      bool              `json:"isPaused"`     // Whether playback is currently paused
	ExternalIDs   map[string]string `json:"externalIds,omitempty"`

	// Episode-specific fields
	SeasonNumber  int    `json:"seasonNumber,omitempty"`
	EpisodeNumber int    `json:"episodeNumber,omitempty"`
	SeriesID      string `json:"seriesId,omitempty"`
	SeriesName    string `json:"seriesName,omitempty"`
	EpisodeName   string `json:"episodeName,omitempty"`

	// Movie-specific fields
	MovieName     string `json:"movieName,omitempty"`
	Year          int    `json:"year,omitempty"`

	// Hidden from continue watching (user dismissed)
	HiddenFromContinueWatching bool `json:"hiddenFromContinueWatching,omitempty"`
}
