package models

// NZBHealthCheck describes the result of checking an NZB against a Usenet server.
type NZBHealthCheck struct {
	Status          string             `json:"status"`
	Healthy         bool               `json:"healthy"`
	CheckedSegments int                `json:"checkedSegments"`
	TotalSegments   int                `json:"totalSegments"`
	MissingSegments []string           `json:"missingSegments,omitempty"`
	FileName        string             `json:"fileName,omitempty"`
	Sampled         bool               `json:"sampled,omitempty"`
	// Track probing results (populated when probeForTracks=true in request)
	TracksProbed    bool               `json:"tracksProbed,omitempty"`
	AudioTracks     []NZBAudioTrack    `json:"audioTracks,omitempty"`
	SubtitleTracks  []NZBSubtitleTrack `json:"subtitleTracks,omitempty"`
	TrackProbeError string             `json:"trackProbeError,omitempty"`
}

// NZBAudioTrack contains audio track metadata for a Usenet file.
type NZBAudioTrack struct {
	Index    int    `json:"index"`
	Language string `json:"language"`
	Codec    string `json:"codec"`
	Title    string `json:"title,omitempty"`
}

// NZBSubtitleTrack contains subtitle track metadata for a Usenet file.
type NZBSubtitleTrack struct {
	Index      int    `json:"index"`
	Language   string `json:"language"`
	Codec      string `json:"codec"`
	Title      string `json:"title,omitempty"`
	Forced     bool   `json:"forced"`
	IsBitmap   bool   `json:"isBitmap"`
	BitmapType string `json:"bitmapType,omitempty"`
}
