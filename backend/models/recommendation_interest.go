package models

import "time"

// RecommendationInterest stores a resolved title, never a raw search query.
type RecommendationInterest struct {
	TMDBID     int64     `json:"tmdbId"`
	MediaType  string    `json:"mediaType"`
	Title      string    `json:"title"`
	SearchedAt time.Time `json:"searchedAt"`
}
