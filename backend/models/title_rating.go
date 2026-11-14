package models

import "time"

// TitleRating is an explicit profile preference, independent of playback,
// watchlists, hidden titles and external rating services.
type TitleRating struct {
	Key       string    `json:"-"`
	Aliases   []string  `json:"-"`
	Title     Title     `json:"-"`
	Value     string    `json:"rating"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func ValidTitleRating(value string) bool {
	return value == "like" || value == "love" || value == "dislike" || value == "none"
}
