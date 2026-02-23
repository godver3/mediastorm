package models

// CalendarItem represents an upcoming content event (episode airing or movie release).
type CalendarItem struct {
	Title         string            `json:"title"`
	EpisodeTitle  string            `json:"episodeTitle,omitempty"`
	MediaType     string            `json:"mediaType"`                       // "movie" | "series"
	SeasonNumber  int               `json:"seasonNumber,omitempty"`
	EpisodeNumber int               `json:"episodeNumber,omitempty"`
	AirDate       string            `json:"airDate"`                         // YYYY-MM-DD
	AirTime       string            `json:"airTime,omitempty"`               // HH:MM local air time (from TVDB airsTime)
	AirTimezone   string            `json:"airTimezone,omitempty"`           // IANA timezone for air time
	Network       string            `json:"network,omitempty"`               // Network name (e.g. "HBO")
	ReleaseType   string            `json:"releaseType,omitempty"`           // For movies: "theatrical", "digital", "physical", etc.
	PosterURL     string            `json:"posterUrl,omitempty"`
	Year          int               `json:"year,omitempty"`
	ExternalIDs   map[string]string `json:"externalIds,omitempty"`           // imdb, tvdb, tmdb
	Source        string            `json:"source"`                          // "watchlist" | "history" | "trending" | "mdblist"
}

// CalendarResponse is the API response for the calendar endpoint.
type CalendarResponse struct {
	Items       []CalendarItem `json:"items"`
	Total       int            `json:"total"`
	Timezone    string         `json:"timezone"`
	Days        int            `json:"days"`
	RefreshedAt string         `json:"refreshedAt"`
}
