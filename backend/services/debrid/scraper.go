package debrid

import (
	"context"

	"novastream/models"
)

// SearchRequest provides normalized inputs to scraper implementations.
type SearchRequest struct {
	Query      string
	Categories []string
	MaxResults int
	Parsed     ParsedQuery
	IMDBID     string // Optional IMDB ID (e.g., "tt11126994") to bypass search
}

// Scraper describes a pluggable source capable of returning torrent releases.
type Scraper interface {
	Name() string
	Search(ctx context.Context, req SearchRequest) ([]ScrapeResult, error)
}

// ScrapeResult represents the scraper-specific payload prior to normalization.
type ScrapeResult struct {
	Title       string
	Indexer     string
	Magnet      string
	InfoHash    string
	TorrentURL  string // URL to download .torrent file (used when no magnet/infohash available)
	FileIndex   int
	SizeBytes   int64
	Seeders     int
	Provider    string
	Languages   []string
	Resolution  string
	MetaName    string
	MetaID      string
	Source      string
	Attributes  map[string]string
	ServiceType models.ContentServiceType
}
