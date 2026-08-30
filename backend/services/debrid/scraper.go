package debrid

import (
	"context"
	"strings"
	"time"

	"novastream/models"
)

// SearchRequest provides normalized inputs to scraper implementations.
type SearchRequest struct {
	Query           string
	Categories      []string
	MaxResults      int
	Parsed          ParsedQuery
	IMDBID          string // Optional IMDB ID (e.g., "tt11126994") to bypass search
	TMDBID          string // Optional TMDB ID for exact companion searches
	IsDaily         bool   // True for daily shows (talk shows, news) that use date-based naming
	TargetAirDate   string // For daily shows: the target air date in YYYY-MM-DD format
	EpisodeReleased bool   // True only when metadata confirms the target episode has aired
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
	PublishDate time.Time
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

func parseScraperPublishDate(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		time.RFC1123Z,
		time.RFC1123,
		time.RFC822Z,
		time.RFC822,
		"Mon, 02 Jan 2006 15:04:05 MST",
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}
