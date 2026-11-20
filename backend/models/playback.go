package models

// DolbyVisionConfiguration is FFmpeg's decoder configuration record from a
// pre-playback probe. Native players can inject it when they intentionally skip
// packet analysis during startup.
type DolbyVisionConfiguration struct {
	StreamIndex             int    `json:"streamIndex"`
	PixelFormat             string `json:"pixelFormat"`
	VersionMajor            int    `json:"versionMajor"`
	VersionMinor            int    `json:"versionMinor"`
	Profile                 int    `json:"profile"`
	Level                   int    `json:"level"`
	RPUPresentFlag          int    `json:"rpuPresentFlag"`
	ELPresentFlag           int    `json:"elPresentFlag"`
	BLPresentFlag           int    `json:"blPresentFlag"`
	BLSignalCompatibilityID int    `json:"blSignalCompatibilityId"`
}

// SubtitleSessionInfo represents a pre-extracted subtitle track session
type SubtitleSessionInfo struct {
	SessionID    string  `json:"sessionId"`
	VTTUrl       string  `json:"vttUrl"`
	SubtitleURL  string  `json:"subtitleUrl,omitempty"`
	Format       string  `json:"format,omitempty"`
	TrackIndex   int     `json:"trackIndex"`
	Language     string  `json:"language"`
	Title        string  `json:"title"`
	Codec        string  `json:"codec"`
	IsForced     bool    `json:"isForced"`
	IsExtracting bool    `json:"isExtracting"`           // true if extraction is still in progress
	FirstCueTime float64 `json:"firstCueTime,omitempty"` // Time of first extracted cue (for subtitle sync)
}

// BatchEpisodeTarget describes one episode to resolve within a batch request.
type BatchEpisodeTarget struct {
	SeasonNumber          int    `json:"seasonNumber"`
	EpisodeNumber         int    `json:"episodeNumber"`
	EpisodeCode           string `json:"episodeCode,omitempty"`
	AbsoluteEpisodeNumber int    `json:"absoluteEpisodeNumber,omitempty"`
	AirDate               string `json:"airDate,omitempty"`
	IsDaily               bool   `json:"isDaily,omitempty"`
}

// BatchEpisodeResult is the per-episode outcome of a batch resolve.
type BatchEpisodeResult struct {
	SeasonNumber          int                 `json:"seasonNumber"`
	EpisodeNumber         int                 `json:"episodeNumber"`
	EpisodeCode           string              `json:"episodeCode,omitempty"`
	AbsoluteEpisodeNumber int                 `json:"absoluteEpisodeNumber,omitempty"`
	Resolution            *PlaybackResolution `json:"resolution,omitempty"`
	Error                 string              `json:"error,omitempty"`
}

// BatchResolveResponse wraps the per-episode results of a batch resolve.
type BatchResolveResponse struct {
	Results []BatchEpisodeResult `json:"results"`
}

// PlaybackResolution contains the derived streaming details for an NZB selection.
type PlaybackResolution struct {
	QueueID        int64  `json:"queueId"`
	WebDAVPath     string `json:"webdavPath"`
	HealthStatus   string `json:"healthStatus"`
	DebridProvider string `json:"debridProvider,omitempty"`
	FileSize       int64  `json:"fileSize,omitempty"`
	SourceNZBPath  string `json:"sourceNzbPath,omitempty"`
	// Probe is internal reusable metadata populated while resolving pre-resolved
	// streams. It avoids probing the same remote URL again in prequeue.
	Probe *VideoFullResult `json:"-"`
	// Pre-extracted subtitles (for manual selection path)
	SubtitleSessions map[int]*SubtitleSessionInfo `json:"subtitleSessions,omitempty"`
	// TrackSelection is the server's audio/subtitle choice for this source.
	// Only populated when the resolve request sets includeTrackSelection.
	TrackSelection *TrackSelection `json:"trackSelection,omitempty"`
	// FrameRate is the video's frame rate as ffprobe gives it ("24000/1001"),
	// from the track selection's probe, so clients can match the display
	// refresh rate before playback starts.
	FrameRate string `json:"frameRate,omitempty"`
}
