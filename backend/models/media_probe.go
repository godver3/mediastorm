package models

// AudioStreamInfo contains audio stream metadata used during playback preparation.
type AudioStreamInfo struct {
	Index    int
	Codec    string
	Profile  string
	Language string
	Title    string
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
