package models

import "time"

type ContentServiceType string

const (
	ServiceTypeUnknown  ContentServiceType = ""
	ServiceTypeUsenet   ContentServiceType = "usenet"
	ServiceTypeDebrid   ContentServiceType = "debrid"
	ServiceTypePearTube ContentServiceType = "peartube"
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

// EffectiveItemSizeBytes returns the size represented by one playable item.
// SizeBytes remains the source-reported value so clients can also show the
// total pack size. Indexer-style sources generally report a pack total, while
// Stremio-style sources with a file index generally report the selected file.
func (r NZBResult) EffectiveItemSizeBytes() int64 {
	if r.SizeBytes <= 0 {
		return r.SizeBytes
	}
	if r.EpisodeCount > 1 && !r.SizePerFile {
		return r.SizeBytes / int64(r.EpisodeCount)
	}
	return r.SizeBytes
}

// ScoreBreakdownItem represents a single scoring criterion's contribution to a result's total score.
type ScoreBreakdownItem struct {
	Criterion string `json:"criterion"` // Display name of the criterion
	Points    int    `json:"points"`    // Points awarded (positive or negative)
	Reason    string `json:"reason"`    // Human-readable explanation
}

// ScoredNZBResult extends NZBResult with filter status, scoring breakdown, and rejection reason.
type ScoredNZBResult struct {
	NZBResult
	FilterStatus   string               `json:"filterStatus"`             // "passed" or "filtered"
	FilterReason   string               `json:"filterReason,omitempty"`   // Reason for exclusion (empty if passed)
	TotalScore     int                  `json:"totalScore"`               // Sum of all scoring points
	ScoreBreakdown []ScoreBreakdownItem `json:"scoreBreakdown,omitempty"` // Per-criterion scoring details
}
