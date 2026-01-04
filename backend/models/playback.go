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
