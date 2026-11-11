package models

import "time"

// EpisodeReference captures identifying information for a specific episode.
type EpisodeReference struct {
	Numbering             *EpisodeNumbering `json:"numbering,omitempty"`
	SeasonNumber          int               `json:"seasonNumber"`
	EpisodeNumber         int               `json:"episodeNumber"`
	AbsoluteEpisodeNumber int               `json:"absoluteEpisodeNumber,omitempty"` // Release absolute number; excludes season-zero specials.
	EpisodeID             string            `json:"episodeId,omitempty"`
	TvdbID                string            `json:"tvdbId,omitempty"`
	Title                 string            `json:"title,omitempty"`
	Overview              string            `json:"overview,omitempty"`
	RuntimeMinutes        int               `json:"runtimeMinutes,omitempty"`
	AirDate               string            `json:"airDate,omitempty"`
	AirDateTimeUTC        string            `json:"airDateTimeUTC,omitempty"`
	AirTimeEstimated      bool              `json:"airTimeEstimated,omitempty"` // Date-only end-of-day cutoff, not a displayable broadcast time.
	Image                 *Image            `json:"image,omitempty"`
	WatchedAt             time.Time         `json:"watchedAt,omitempty"`
	// ItemID is the server-built canonical progress/watch-history item ID for
	// this episode (e.g. "tmdb:tv:95350:s01e01"). Response-only; clients should
	// use it verbatim instead of assembling IDs themselves.
	ItemID string `json:"itemId,omitempty"`
}

// ResumeState is the server-computed resume decision for one playable item.
// Clients should use Eligible to decide whether to offer "Resume" rather than
// applying their own thresholds.
//
// Units: Position and Duration are seconds; Percent is 0–100. When only a
// percentage is known (e.g. imported from Trakt), Position and Duration are 0
// and clients should seek to Percent of the media duration once known.
type ResumeState struct {
	Position float64 `json:"position"`
	Duration float64 `json:"duration"`
	Percent  float64 `json:"percent"`
	Eligible bool    `json:"eligible"`
}

// SeriesWatchState tracks a user's progress for a particular series.
type SeriesWatchState struct {
	SeriesID        string                      `json:"seriesId"`
	SeriesTitle     string                      `json:"seriesTitle"`
	Overview        string                      `json:"overview,omitempty"`
	PosterURL       string                      `json:"posterUrl,omitempty"`
	TextPosterURL   string                      `json:"textPosterUrl,omitempty"`
	BackdropURL     string                      `json:"backdropUrl,omitempty"`
	TextBackdropURL string                      `json:"textBackdropUrl,omitempty"`
	LogoURL         string                      `json:"logoUrl,omitempty"`
	BackdropURLs    []string                    `json:"backdropUrls,omitempty"`
	Year            int                         `json:"year,omitempty"`
	ExternalIDs     map[string]string           `json:"externalIds,omitempty"`
	UpdatedAt       time.Time                   `json:"updatedAt"`
	LastWatched     EpisodeReference            `json:"lastWatched"`
	NextEpisode     *EpisodeReference           `json:"nextEpisode,omitempty"`
	WatchedEpisodes map[string]EpisodeReference `json:"watchedEpisodes,omitempty"`
	PercentWatched  float64                     `json:"percentWatched,omitempty"`
	ResumePercent   float64                     `json:"resumePercent,omitempty"`
	// Resume is the server-computed resume state for the item this entry would
	// play (the movie, or NextEpisode for series). Populated on Continue
	// Watching responses and details-bundle watch state.
	Resume *ResumeState `json:"resume,omitempty"`

	// SortAt is an internal Continue Watching ordering timestamp. It allows a
	// newly released next episode to establish a durable shelf position without
	// misrepresenting UpdatedAt as user playback activity.
	SortAt time.Time `json:"-"`

	// Episode counts for tracking series completion (excludes specials/season 0)
	WatchedEpisodeCount int `json:"watchedEpisodeCount,omitempty"` // Number of episodes user has watched
	TotalEpisodeCount   int `json:"totalEpisodeCount,omitempty"`   // Total released episodes in series

	// Prequeue/prewarm status for the current next item. Prewarm entries are
	// stored in the same prequeue store, so this covers both paths.
	PrequeueID     string `json:"prequeueId,omitempty"`
	PrequeueStatus string `json:"prequeueStatus,omitempty"`

	Status          string   `json:"status,omitempty"`
	LifecycleStatus string   `json:"lifecycleStatus,omitempty"`
	Theatrical      *Release `json:"theatricalRelease,omitempty"`
	HomeRelease     *Release `json:"homeRelease,omitempty"`
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

// WatchHistoryItem represents a unified watch history entry for any media (movie, episode, series, or live).
type WatchHistoryItem struct {
	ID             string    `json:"id"`        // mediaType:itemId (e.g., "movie:tmdb:12345" or "series:tvdb:67890:s01e02")
	MediaType      string    `json:"mediaType"` // "movie" | "series" | "episode" | "live"
	ItemID         string    `json:"itemId"`    // The actual ID (e.g., "tmdb:12345")
	Name           string    `json:"name"`
	Year           int       `json:"year,omitempty"`
	Watched        bool      `json:"watched"` // Manual watch flag
	WatchedAt      time.Time `json:"watchedAt,omitempty"`
	UpdatedAt      time.Time `json:"updatedAt,omitempty"`
	WatchedSeconds float64   `json:"watchedSeconds,omitempty"` // Accumulated actual playback time in seconds
	// Source records how the item became watched (WatchSourcePlayed,
	// WatchSourceMarked or WatchSourceImported). Empty for rows written before
	// sources were tracked that the backfill could not classify.
	Source      string            `json:"source,omitempty"`
	ExternalIDs map[string]string `json:"externalIds,omitempty"`

	// Episode-specific fields
	SeasonNumber  int    `json:"seasonNumber,omitempty"`
	EpisodeNumber int    `json:"episodeNumber,omitempty"`
	SeriesID      string `json:"seriesId,omitempty"` // Parent series ID for episodes
	SeriesName    string `json:"seriesName,omitempty"`
}

// WatchHistoryUpdate represents an update to mark an item as watched/unwatched.
type WatchHistoryUpdate struct {
	MediaType string    `json:"mediaType"`
	ItemID    string    `json:"itemId"`
	Name      string    `json:"name,omitempty"`
	Year      int       `json:"year,omitempty"`
	Watched   *bool     `json:"watched,omitempty"`
	WatchedAt time.Time `json:"watchedAt,omitempty"` // Optional: use specific timestamp instead of now
	// WatchedAtUnknown marks an import whose source has no play date. Such an
	// update only adds items missing locally; it never overrides local state,
	// because "now" would always look newer than a local unwatch.
	WatchedAtUnknown bool    `json:"-"`
	WatchedSeconds   float64 `json:"watchedSeconds,omitempty"`
	// Source is set by the server, never by clients: how a watched=true
	// update came about. Empty means a manual mark.
	Source      string            `json:"-"`
	ExternalIDs map[string]string `json:"externalIds,omitempty"`

	// Episode-specific
	SeasonNumber  int    `json:"seasonNumber,omitempty"`
	EpisodeNumber int    `json:"episodeNumber,omitempty"`
	SeriesID      string `json:"seriesId,omitempty"`
	SeriesName    string `json:"seriesName,omitempty"`
}

// PlaybackProgressUpdate represents a playback progress update from the player.
type PlaybackProgressUpdate struct {
	MediaType            string            `json:"mediaType"`                       // "movie" | "episode" | "live"
	ItemID               string            `json:"itemId"`                          // The media ID
	Position             float64           `json:"position"`                        // Current playback position in seconds
	Duration             float64           `json:"duration"`                        // Total duration in seconds
	PercentWatched       float64           `json:"percentWatched"`                  // Override: set directly when duration is unknown (e.g. Trakt import)
	Timestamp            time.Time         `json:"timestamp"`                       // When this update was sent
	IsPaused             bool              `json:"isPaused"`                        // Whether playback is currently paused
	IsBuffering          bool              `json:"isBuffering"`                     // Whether the player is currently stalled/buffering (not paused)
	PlaybackEnded        bool              `json:"playbackEnded,omitempty"`         // Whether this is the final heartbeat for the playback session
	BufferAhead          *float64          `json:"bufferAheadSeconds,omitempty"`    // Player-reported playable buffer runway, when available
	RequiredMbps         *float64          `json:"requiredBandwidthMbps,omitempty"` // Estimated average bandwidth required by the active release
	SourcePath           string            `json:"sourcePath,omitempty"`            // Active source path, used to isolate migration signals across replacements
	PosterURL            string            `json:"posterUrl,omitempty"`             // Canonical portrait artwork associated with playback
	NotificationImageURL string            `json:"notificationImageUrl,omitempty"`  // Orientation-selected artwork for backend-owned notifications
	ExternalIDs          map[string]string `json:"externalIds,omitempty"`

	// Episode-specific fields
	SeasonNumber  int    `json:"seasonNumber,omitempty"`
	EpisodeNumber int    `json:"episodeNumber,omitempty"`
	SeriesID      string `json:"seriesId,omitempty"`
	SeriesName    string `json:"seriesName,omitempty"`
	EpisodeName   string `json:"episodeName,omitempty"`

	// Movie-specific fields
	MovieName string `json:"movieName,omitempty"`
	Year      int    `json:"year,omitempty"`

	// PlaybackSessionID is assigned internally after a player heartbeat is
	// matched to an Active Streams session. It is never accepted from clients.
	PlaybackSessionID string `json:"-"`
	// ClientID is resolved from the active playback session, never the JSON payload.
	ClientID string `json:"-"`
}

// PlaybackProgress stores the current playback progress for a media item.
type PlaybackProgress struct {
	ID             string            `json:"id"`                       // mediaType:itemId
	MediaType      string            `json:"mediaType"`                // "movie" | "episode" | "live"
	ItemID         string            `json:"itemId"`                   // The media ID
	Position       float64           `json:"position"`                 // Last known position in seconds
	Duration       float64           `json:"duration"`                 // Total duration in seconds
	PercentWatched float64           `json:"percentWatched"`           // Position/Duration * 100
	UpdatedAt      time.Time         `json:"updatedAt"`                // Last update time
	IsPaused       bool              `json:"isPaused"`                 // Whether playback is currently paused
	IsBuffering    bool              `json:"isBuffering,omitempty"`    // Whether the player is currently stalled/buffering (runtime only, not persisted)
	WatchedSeconds float64           `json:"watchedSeconds,omitempty"` // Accumulated actual playback time in seconds
	ExternalIDs    map[string]string `json:"externalIds,omitempty"`

	// Episode-specific fields
	SeasonNumber  int    `json:"seasonNumber,omitempty"`
	EpisodeNumber int    `json:"episodeNumber,omitempty"`
	SeriesID      string `json:"seriesId,omitempty"`
	SeriesName    string `json:"seriesName,omitempty"`
	EpisodeName   string `json:"episodeName,omitempty"`

	// Movie-specific fields
	MovieName string `json:"movieName,omitempty"`
	Year      int    `json:"year,omitempty"`

	// Hidden from continue watching (user dismissed)
	HiddenFromContinueWatching bool `json:"hiddenFromContinueWatching,omitempty"`

	// Resume is computed at response time from Position/Duration/PercentWatched
	// using the server's resume thresholds. Not persisted.
	Resume *ResumeState `json:"resume,omitempty"`

	// Runtime playback control response fields. Not persisted.
	AllowedToContinue             *bool  `json:"allowedToContinue,omitempty"`
	MigrationRequested            bool   `json:"migrationRequested,omitempty"`
	MigrationReason               string `json:"migrationReason,omitempty"`
	MigrationPreparationRequested bool   `json:"migrationPreparationRequested,omitempty"`
	MigrationPreparationReason    string `json:"migrationPreparationReason,omitempty"`
}

// PopularTitle represents a movie or series aggregated across all profiles
// for the "Popular on This Server" shelf.
type PopularTitle struct {
	MediaType   string            `json:"mediaType"` // "movie" or "series"
	ItemID      string            `json:"itemId"`    // series-level or movie-level ID
	Name        string            `json:"name"`
	Year        int               `json:"year,omitempty"`
	WatchCount  int               `json:"watchCount"` // completed media-item views rolled up to this title
	ExternalIDs map[string]string `json:"externalIds,omitempty"`
	LastWatched time.Time         `json:"-"`
}

// RecentWatch represents a single watch event for the "Recently Watched" feed.
type RecentWatch struct {
	UserID        string            `json:"userId,omitempty"` // omitted when anonymous
	UserName      string            `json:"userName"`         // profile name or "Fellow user"
	IsAnonymous   bool              `json:"isAnonymous"`
	MediaType     string            `json:"mediaType"` // "movie" or "episode"
	ItemID        string            `json:"itemId"`
	Name          string            `json:"name"` // movie name or episode title
	SeriesID      string            `json:"seriesId,omitempty"`
	SeriesName    string            `json:"seriesName,omitempty"`
	SeasonNumber  int               `json:"seasonNumber,omitempty"`
	EpisodeNumber int               `json:"episodeNumber,omitempty"`
	WatchedAt     time.Time         `json:"watchedAt"`
	ExternalIDs   map[string]string `json:"externalIds,omitempty"`
}

// Watch sources: how a watch history item became watched.
const (
	// WatchSourcePlayed: playback crossed the watched threshold.
	WatchSourcePlayed = "played"
	// WatchSourceImported: synced from Trakt, Simkl, Plex or another service,
	// where it was watched.
	WatchSourceImported = "imported"
	// WatchSourceMarked: marked watched by hand, without playing it.
	WatchSourceMarked = "marked"
)

func watchSourceRank(source string) int {
	switch source {
	case WatchSourcePlayed:
		return 3
	case WatchSourceImported:
		return 2
	case WatchSourceMarked:
		return 1
	}
	return 0
}

// MergeWatchSource returns the source an item has after a watched=true update
// with source incoming. An item that was not watched takes incoming. One that
// already was keeps the stronger of the two, so marking an episode you played
// leaves it played, while playing a marked one upgrades it.
func MergeWatchSource(wasWatched bool, current, incoming string) string {
	if incoming == "" {
		return current
	}
	if !wasWatched || watchSourceRank(incoming) > watchSourceRank(current) {
		return incoming
	}
	return current
}

// IsRealWatch reports whether a watched item reflects a real viewing rather
// than a bare manual mark: played here, imported from a service where it was
// watched, or marked after some playback here (e.g. stopped at the credits).
// Rows from before sources were tracked count unless their backfill found
// them marked.
func (item WatchHistoryItem) IsRealWatch() bool {
	return item.Source != WatchSourceMarked || item.WatchedSeconds > 0
}
