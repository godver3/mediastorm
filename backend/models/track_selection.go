package models

// SelectedTrack describes one container track the server chose for playback.
//
// StreamIndex is the container (ffprobe) stream number. It is the same number
// space as AudioTrackInfo.Index / SubtitleTrackInfo.Index and the HLS/stream
// `audioTrack` / `subtitleTrack` parameters. It is NOT a list position.
//
// Players that expose their own track lists (KSPlayer, MPV, ExoPlayer,
// AVPlayer, AetherEngine, ...) should map to their rendition with, in order:
// TypeIndex (when the player lists every container track of that kind in
// container order), then Language + LanguageIndex (when the player drops or
// reorders tracks), then Codec / Title / Forced as tie-breakers.
type SelectedTrack struct {
	// StreamIndex is the container (ffprobe) stream number.
	StreamIndex int `json:"streamIndex"`
	// TypeIndex is the 0-based position among container tracks of the same
	// kind (audio or subtitle), in container order.
	TypeIndex int `json:"typeIndex"`
	// Language is the raw container language tag (may be empty).
	Language string `json:"language,omitempty"`
	// LanguageCode is the normalized ISO 639-2 code for Language when known.
	LanguageCode string `json:"languageCode,omitempty"`
	// LanguageIndex is the 0-based position among same-kind tracks sharing
	// this track's language (compared by LanguageCode, else the raw tag).
	LanguageIndex int `json:"languageIndex"`
	// LanguageCount is the number of same-kind tracks sharing this language.
	LanguageCount int    `json:"languageCount"`
	Codec         string `json:"codec,omitempty"`
	Profile       string `json:"profile,omitempty"`
	Title         string `json:"title,omitempty"`
	// Forced reports a forced subtitle (disposition flag or "forced" title).
	Forced bool `json:"forced,omitempty"`
	// Default reports the container default disposition (subtitles only).
	Default bool `json:"default,omitempty"`
	// Bitmap reports an image-based subtitle (PGS/VobSub) that text-only
	// renderers cannot display.
	Bitmap bool `json:"bitmap,omitempty"`
}

// TrackSelection is the server's audio/subtitle choice for a playback source,
// derived from the effective profile/client/title language preferences.
//
// Semantics:
//   - Audio == nil: no track matched the preferences; keep the player default.
//   - Subtitle == nil && SubtitleMode == "off": subtitles are disabled.
//   - Subtitle == nil && SubtitleMode != "off": no embedded track matched; the
//     client may search for external subtitles in SubtitleLanguage.
//   - TextSubtitle is the same choice restricted to text codecs (SRT/ASS/
//     WebVTT/mov_text) for renderers that cannot draw bitmap subtitles. It
//     equals Subtitle whenever Subtitle is already a text track.
type TrackSelection struct {
	Audio        *SelectedTrack `json:"audio,omitempty"`
	Subtitle     *SelectedTrack `json:"subtitle,omitempty"`
	TextSubtitle *SelectedTrack `json:"textSubtitle,omitempty"`

	// Effective preferences the choice was made from.
	AudioLanguage    string `json:"audioLanguage,omitempty"`
	SubtitleLanguage string `json:"subtitleLanguage,omitempty"`
	// SubtitleMode is the canonical mode: "off", "forced-only" or "on". A
	// "foreign-audio" preference is reported as the mode it resolved to.
	SubtitleMode string `json:"subtitleMode"`
	// SubtitleModePreference is set to "foreign-audio" when SubtitleMode was
	// resolved from it, so a client that picks a different audio track can
	// resolve it again.
	SubtitleModePreference string `json:"subtitleModePreference,omitempty"`
}

// AudioStreamIndex returns the selected audio container stream index or -1.
func (s *TrackSelection) AudioStreamIndex() int {
	if s == nil || s.Audio == nil {
		return -1
	}
	return s.Audio.StreamIndex
}

// SubtitleStreamIndex returns the selected subtitle container stream index or -1.
func (s *TrackSelection) SubtitleStreamIndex() int {
	if s == nil || s.Subtitle == nil {
		return -1
	}
	return s.Subtitle.StreamIndex
}
