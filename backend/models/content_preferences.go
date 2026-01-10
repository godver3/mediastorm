package models

import "time"

// ContentPreference stores per-content audio and subtitle preferences.
// For series, one preference applies to all episodes.
// For movies, each movie has its own preference.
type ContentPreference struct {
	ContentID        string    `json:"contentId"`                  // e.g., "tmdb:tv:12345" for series, "tmdb:movie:67890" for movies
	ContentType      string    `json:"contentType"`                // "series" or "movie"
	AudioLanguage    string    `json:"audioLanguage,omitempty"`    // ISO 639-2 code (eng, jpn, spa, etc.)
	SubtitleLanguage string    `json:"subtitleLanguage,omitempty"` // ISO 639-2 code or empty
	SubtitleMode     string    `json:"subtitleMode,omitempty"`     // "off", "on", "forced-only"
	UpdatedAt        time.Time `json:"updatedAt"`
}

// ContentPreferenceUpdate represents a request to update content preferences.
type ContentPreferenceUpdate struct {
	ContentID        string `json:"contentId"`
	ContentType      string `json:"contentType"`
	AudioLanguage    string `json:"audioLanguage,omitempty"`
	SubtitleLanguage string `json:"subtitleLanguage,omitempty"`
	SubtitleMode     string `json:"subtitleMode,omitempty"`
}
