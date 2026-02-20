package models

import "time"

type ContentServiceType string

const (
	ServiceTypeUnknown ContentServiceType = ""
	ServiceTypeUsenet  ContentServiceType = "usenet"
	ServiceTypeDebrid  ContentServiceType = "debrid"
)

// NZBResult represents a normalized search result from a Torznab/Newznab indexer.
type NZBResult struct {
	Title        string             `json:"title"`
	Indexer      string             `json:"indexer"`
	GUID         string             `json:"guid"`
	Link         string             `json:"link"`
	DownloadURL  string             `json:"downloadUrl"`
	SizeBytes    int64              `json:"sizeBytes"`
	PublishDate  time.Time          `json:"publishDate"`
	Categories   []string           `json:"categories,omitempty"`
	Attributes   map[string]string  `json:"attributes,omitempty"`
	ServiceType  ContentServiceType `json:"serviceType,omitempty"`
	EpisodeCount int                `json:"episodeCount,omitempty"` // Number of episodes in pack (0 if not a pack)
	SizePerFile  bool               `json:"sizePerFile,omitempty"`  // True when sizeBytes is per-file (Stremio scrapers), false when total pack
}
