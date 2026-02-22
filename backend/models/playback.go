package models

// SubtitleSessionInfo represents a pre-extracted subtitle track session
type SubtitleSessionInfo struct {
	SessionID    string  `json:"sessionId"`
	VTTUrl       string  `json:"vttUrl"`
	TrackIndex   int     `json:"trackIndex"`
	Language     string  `json:"language"`
	Title        string  `json:"title"`
	Codec        string  `json:"codec"`
	IsForced     bool    `json:"isForced"`
	IsExtracting bool    `json:"isExtracting"`          // true if extraction is still in progress
	FirstCueTime float64 `json:"firstCueTime,omitempty"` // Time of first extracted cue (for subtitle sync)
}

// BatchEpisodeTarget describes one episode to resolve within a batch request.
type BatchEpisodeTarget struct {
	SeasonNumber         int    `json:"seasonNumber"`
	EpisodeNumber        int    `json:"episodeNumber"`
	EpisodeCode          string `json:"episodeCode,omitempty"`
	AbsoluteEpisodeNumber int   `json:"absoluteEpisodeNumber,omitempty"`
	AirDate              string `json:"airDate,omitempty"`
	IsDaily              bool   `json:"isDaily,omitempty"`
}

// BatchEpisodeResult is the per-episode outcome of a batch resolve.
type BatchEpisodeResult struct {
	SeasonNumber         int                 `json:"seasonNumber"`
	EpisodeNumber        int                 `json:"episodeNumber"`
	EpisodeCode          string              `json:"episodeCode,omitempty"`
	AbsoluteEpisodeNumber int                `json:"absoluteEpisodeNumber,omitempty"`
	Resolution           *PlaybackResolution `json:"resolution,omitempty"`
	Error                string              `json:"error,omitempty"`
}

// BatchResolveResponse wraps the per-episode results of a batch resolve.
type BatchResolveResponse struct {
	Results []BatchEpisodeResult `json:"results"`
}

// PlaybackResolution contains the derived streaming details for an NZB selection.
type PlaybackResolution struct {
	QueueID       int64  `json:"queueId"`
	WebDAVPath    string `json:"webdavPath"`
	HealthStatus  string `json:"healthStatus"`
	FileSize      int64  `json:"fileSize,omitempty"`
	SourceNZBPath string `json:"sourceNzbPath,omitempty"`
	// Pre-extracted subtitles (for manual selection path)
	SubtitleSessions map[int]*SubtitleSessionInfo `json:"subtitleSessions,omitempty"`
}
