package models

import "time"

// CalendarItem represents an upcoming content event (episode airing or movie release).
type CalendarItem struct {
	Title           string            `json:"title"`
	EpisodeTitle    string            `json:"episodeTitle,omitempty"`
	EpisodeOverview string            `json:"episodeOverview,omitempty"`
	MediaType       string            `json:"mediaType"` // "movie" | "series"
	SeasonNumber    int               `json:"seasonNumber,omitempty"`
	EpisodeNumber   int               `json:"episodeNumber,omitempty"`
	AirDate         string            `json:"airDate"`                 // YYYY-MM-DD
	AirTime         string            `json:"airTime,omitempty"`       // HH:MM local air time (from TVDB airsTime)
	AirTimezone     string            `json:"airTimezone,omitempty"`   // IANA timezone for air time
	Network         string            `json:"network,omitempty"`       // Network name (e.g. "HBO")
	ReleaseType     string            `json:"releaseType,omitempty"`   // For movies: "theatrical", "digital", "physical", etc.
	ReleaseStatus   string            `json:"releaseStatus,omitempty"` // Stable availability state used by release notifications.
	PosterURL       string            `json:"posterUrl,omitempty"`
	TextPosterURL   string            `json:"textPosterUrl,omitempty"` // Poster with title text when PosterURL is textless
	BackdropURL     string            `json:"backdropUrl,omitempty"`
	TextBackdropURL string            `json:"textBackdropUrl,omitempty"` // Backdrop with title text when BackdropURL is textless
	BackdropURLs    []string          `json:"backdropUrls,omitempty"`    // Ranked alternate backdrops
	Overview        string            `json:"overview,omitempty"`
	Logo            *Image            `json:"logo,omitempty"`
	Ratings         []Rating          `json:"ratings,omitempty"`
	Certification   string            `json:"certification,omitempty"` // MPAA/TV content rating, used for kids-profile filtering
	Genres          []string          `json:"genres,omitempty"`
	RuntimeMinutes  int               `json:"runtimeMinutes,omitempty"`
	Theatrical      *Release          `json:"theatricalRelease,omitempty"`
	HomeRelease     *Release          `json:"homeRelease,omitempty"`
	Year            int               `json:"year,omitempty"`
	ExternalIDs     map[string]string `json:"externalIds,omitempty"` // imdb, tvdb, tmdb
	Source          string            `json:"source"`                // "watchlist" | "history" | "trending" | "top-trending" | "mdblist"
	SourceRank      int               `json:"sourceRank,omitempty"`  // 1-based rank within a trending source
}

// CalendarResponse is the API response for the calendar endpoint.
type CalendarResponse struct {
	Items       []CalendarItem `json:"items"`
	Total       int            `json:"total"`
	Timezone    string         `json:"timezone"`
	Days        int            `json:"days"`
	RecentDays  int            `json:"recentDays"`
	RefreshedAt string         `json:"refreshedAt"`
	// Pending is set on home-shelf requests when no calendar has been built for
	// the user yet; a build was started in the background and the client should
	// retry shortly instead of treating the empty list as final.
	Pending bool `json:"pending,omitempty"`
}

// UserCalendarSnapshot is a persisted, fully built calendar for one user.
// Kind is "full" (the Calendar tab build) or "lite" (the fast home build).
// Stale marks a snapshot whose inputs changed after it was built: it may still
// be shown on Home while a rebuild runs but must not count as fresh.
type UserCalendarSnapshot struct {
	UserID      string
	Kind        string
	Items       []CalendarItem
	RefreshedAt time.Time
	Stale       bool
}
