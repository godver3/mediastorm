package models

import "strings"

// AudioStreamInfo contains audio stream metadata used during playback preparation.
type AudioStreamInfo struct {
	Index    int
	Codec    string
	Profile  string
	Language string
	Title    string
	Channels int // 0 when unknown
}

// SubtitleStreamInfo contains subtitle stream metadata used during playback preparation.
type SubtitleStreamInfo struct {
	Index     int
	Codec     string
	Language  string
	Title     string
	IsForced  bool
	IsDefault bool
}

// IsInterlacedFieldOrder reports whether ffprobe's field_order describes an interlaced
// picture (tt, bb, tb, bt); "progressive", "unknown" and an empty value do not. Jellyfin sets
// MediaStream.IsInterlaced from the same field (it counts anything but "progressive", but
// ffprobe's JSON leaves "unknown" out).
func IsInterlacedFieldOrder(fieldOrder string) bool {
	switch strings.ToLower(strings.TrimSpace(fieldOrder)) {
	case "tt", "bb", "tb", "bt":
		return true
	}
	return false
}

// VideoFullResult contains the reusable result of a full playback metadata probe.
type VideoFullResult struct {
	HasDolbyVision           bool
	HasHDR10                 bool
	DolbyVisionProfile       string
	DolbyVisionConfiguration *DolbyVisionConfiguration
	VideoCodec               string
	VideoPixFmt              string
	VideoProfile             string
	VideoWidth               int
	VideoHeight              int
	VideoLevel               int
	AvgFrameRate             string
	// VideoParamSetsMissing: HEVC whose hvcC carries no VPS/SPS/PPS.
	VideoParamSetsMissing bool
	HasTrueHD             bool
	HasCompatibleAudio    bool
	AudioStreams          []AudioStreamInfo
	SubtitleStreams       []SubtitleStreamInfo
	Duration              float64
}
