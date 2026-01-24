package models

import (
	"time"
)

// EPGProgram represents a single program in the EPG schedule.
type EPGProgram struct {
	ChannelID   string    `json:"channelId"`             // Links to LiveChannel.TvgID
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Start       time.Time `json:"start"`
	Stop        time.Time `json:"stop"`
	Icon        string    `json:"icon,omitempty"`
	Categories  []string  `json:"categories,omitempty"`
	Episode     string    `json:"episode,omitempty"` // Episode number in standard format (e.g., "S01E05")
	Rating      string    `json:"rating,omitempty"`
}

// EPGChannel represents a channel's metadata from EPG data.
type EPGChannel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Icon string `json:"icon,omitempty"`
}

// EPGSchedule holds the complete EPG data for all channels.
type EPGSchedule struct {
	Channels    map[string]EPGChannel   `json:"channels"`              // channelId -> channel metadata
	Programs    map[string][]EPGProgram `json:"programs"`              // channelId -> sorted programs
	LastUpdated time.Time               `json:"lastUpdated"`
	SourceType  string                  `json:"sourceType"` // "xmltv" or "xtream"
}

// EPGNowPlaying represents the current and next program for a channel.
type EPGNowPlaying struct {
	ChannelID string      `json:"channelId"`
	Current   *EPGProgram `json:"current,omitempty"`
	Next      *EPGProgram `json:"next,omitempty"`
}

// EPGStatus represents the status of the EPG service.
type EPGStatus struct {
	Enabled      bool       `json:"enabled"`
	LastRefresh  *time.Time `json:"lastRefresh,omitempty"`
	LastError    string     `json:"lastError,omitempty"`
	ChannelCount int        `json:"channelCount"`
	ProgramCount int        `json:"programCount"`
	Refreshing   bool       `json:"refreshing"`
	SourceCount  int        `json:"sourceCount"`
}
