package handlers

import (
	"log"
	"strings"
)

// AudioStreamInfo contains audio stream metadata for track selection
type AudioStreamInfo struct {
	Index    int
	Codec    string
	Language string
	Title    string
}

// SubtitleStreamInfo contains subtitle stream metadata for track selection
type SubtitleStreamInfo struct {
	Index     int
	Codec     string // e.g., "subrip", "ass" - needed for sidecar VTT extraction
	Language  string
	Title     string
	IsForced  bool
	IsDefault bool
}

// CompatibleAudioCodecs lists codecs that can be played without transcoding
var CompatibleAudioCodecs = map[string]bool{
	"aac": true, "ac3": true, "eac3": true, "mp3": true,
}

// IsIncompatibleAudioCodec returns true for codecs that need transcoding.
// This includes TrueHD, DTS, FLAC, PCM, and any other codec not in CompatibleAudioCodecs.
// HLS/fMP4 only supports AAC, AC-3, and E-AC-3 for Apple devices.
func IsIncompatibleAudioCodec(codec string) bool {
	c := strings.ToLower(strings.TrimSpace(codec))
	if c == "" {
		return false // Unknown codec, let FFmpeg handle it
	}
	// If not in the compatible list, it needs transcoding
	return !CompatibleAudioCodecs[c]
}

// IsTrueHDCodec returns true specifically for TrueHD/MLP codecs which are particularly
// problematic for streaming. We prefer to avoid these unless they're the only option.
func IsTrueHDCodec(codec string) bool {
	c := strings.ToLower(strings.TrimSpace(codec))
	return c == "truehd" || c == "mlp"
}

// IsIncompatibleVideoCodec returns true for video codecs that iOS/tvOS cannot play natively.
// iOS only supports H.264 (AVC) and HEVC (H.265). Legacy codecs like MPEG-4 Part 2 (XviD/DivX),
// MPEG-2, VC-1, VP8/VP9, etc. require transcoding to H.264.
func IsIncompatibleVideoCodec(codec string) bool {
	c := strings.ToLower(strings.TrimSpace(codec))
	// Compatible codecs (iOS native support)
	compatibleVideoCodecs := map[string]bool{
		"h264": true, "avc": true, "avc1": true,
		"hevc": true, "h265": true, "hvc1": true, "hev1": true,
	}
	// If empty or compatible, no transcoding needed
	if c == "" || compatibleVideoCodecs[c] {
		return false
	}
	// Any other codec is incompatible and needs transcoding
	return true
}

// IsCommentaryTrack checks if an audio track is a commentary track based on its title
func IsCommentaryTrack(title string) bool {
	lowerTitle := strings.ToLower(strings.TrimSpace(title))
	commentaryIndicators := []string{
		"commentary",
		"director's commentary",
		"directors commentary",
		"audio commentary",
		"cast commentary",
		"crew commentary",
		"isolated score",
		"music only",
		"score only",
	}
	for _, indicator := range commentaryIndicators {
		if strings.Contains(lowerTitle, indicator) {
			return true
		}
	}
	return false
}

// matchesLanguage checks if a stream matches the preferred language
func matchesLanguage(language, title, normalizedPref string) bool {
	language = strings.ToLower(strings.TrimSpace(language))
	title = strings.ToLower(strings.TrimSpace(title))

	// Exact match
	if language == normalizedPref || title == normalizedPref {
		return true
	}
	// Partial match (skip empty strings to avoid false positives)
	if language != "" && (strings.Contains(language, normalizedPref) || strings.Contains(normalizedPref, language)) {
		return true
	}
	if title != "" && (strings.Contains(title, normalizedPref) || strings.Contains(normalizedPref, title)) {
		return true
	}
	return false
}

// FindAudioTrackByLanguage finds an audio track matching the preferred language.
// Prefers compatible audio codecs (AAC, AC3, etc.) over TrueHD/DTS when multiple tracks exist.
// Specifically avoids TrueHD/MLP unless it's the only option for the preferred language.
// Skips commentary tracks unless they are the only option.
// Returns -1 if no matching track is found.
func FindAudioTrackByLanguage(streams []AudioStreamInfo, preferredLanguage string) int {
	if preferredLanguage == "" || len(streams) == 0 {
		return -1
	}

	normalizedPref := strings.ToLower(strings.TrimSpace(preferredLanguage))

	// Pass 1: Compatible codec (AAC, AC3, etc.) matching language, skipping commentary
	for _, stream := range streams {
		if matchesLanguage(stream.Language, stream.Title, normalizedPref) &&
			CompatibleAudioCodecs[strings.ToLower(stream.Codec)] &&
			!IsCommentaryTrack(stream.Title) {
			log.Printf("[track] Preferred compatible audio track %d (%s) for language %q",
				stream.Index, stream.Codec, preferredLanguage)
			return stream.Index
		}
	}

	// Pass 2: Non-TrueHD incompatible codec (DTS, etc.) matching language, skipping commentary
	// TrueHD is particularly problematic for streaming, so prefer DTS over TrueHD
	for _, stream := range streams {
		if matchesLanguage(stream.Language, stream.Title, normalizedPref) &&
			!IsTrueHDCodec(stream.Codec) &&
			!IsCommentaryTrack(stream.Title) {
			log.Printf("[track] Selected non-TrueHD audio track %d (%s) for language %q - will need HLS transcoding",
				stream.Index, stream.Codec, preferredLanguage)
			return stream.Index
		}
	}

	// Pass 3: TrueHD/MLP matching language, skipping commentary (only if no other option)
	for _, stream := range streams {
		if matchesLanguage(stream.Language, stream.Title, normalizedPref) &&
			IsTrueHDCodec(stream.Codec) &&
			!IsCommentaryTrack(stream.Title) {
			log.Printf("[track] Selected TrueHD audio track %d (%s) for language %q (only option) - will need HLS transcoding",
				stream.Index, stream.Codec, preferredLanguage)
			return stream.Index
		}
	}

	// Pass 4: Compatible codec matching language, including commentary
	for _, stream := range streams {
		if matchesLanguage(stream.Language, stream.Title, normalizedPref) &&
			CompatibleAudioCodecs[strings.ToLower(stream.Codec)] {
			log.Printf("[track] Fallback to compatible audio track %d (%s, commentary) for language %q",
				stream.Index, stream.Codec, preferredLanguage)
			return stream.Index
		}
	}

	// Pass 5: Non-TrueHD incompatible codec matching language, including commentary
	for _, stream := range streams {
		if matchesLanguage(stream.Language, stream.Title, normalizedPref) &&
			!IsTrueHDCodec(stream.Codec) {
			log.Printf("[track] Fallback to non-TrueHD audio track %d (%s, commentary) for language %q - will need HLS transcoding",
				stream.Index, stream.Codec, preferredLanguage)
			return stream.Index
		}
	}

	// Pass 6: TrueHD/MLP matching language, including commentary (last resort)
	for _, stream := range streams {
		if matchesLanguage(stream.Language, stream.Title, normalizedPref) {
			log.Printf("[track] Fallback to TrueHD audio track %d (%s, commentary) for language %q (only option) - will need HLS transcoding",
				stream.Index, stream.Codec, preferredLanguage)
			return stream.Index
		}
	}

	return -1
}

// isSDHTrack checks if a subtitle track is SDH (Subtitles for Deaf/Hard of Hearing)
func isSDHTrack(title string) bool {
	lower := strings.ToLower(strings.TrimSpace(title))
	return strings.Contains(lower, "sdh") || strings.Contains(lower, "deaf") || strings.Contains(lower, "hard of hearing")
}

// FindSubtitleTrackByPreference finds a subtitle track matching the preferences.
// mode can be "off", "forced-only", or "on".
// When mode is "on", prefers SDH > regular > forced tracks.
// Returns -1 if no matching track is found or mode is "off".
func FindSubtitleTrackByPreference(streams []SubtitleStreamInfo, preferredLanguage, mode string) int {
	if len(streams) == 0 || mode == "off" {
		return -1
	}

	normalizedPref := strings.ToLower(strings.TrimSpace(preferredLanguage))

	// For forced-only mode, only consider forced tracks
	if mode == "forced-only" {
		var forcedStreams []SubtitleStreamInfo
		for _, s := range streams {
			if s.IsForced {
				forcedStreams = append(forcedStreams, s)
			}
		}
		if len(forcedStreams) == 0 {
			return -1
		}
		// Find matching forced track
		for _, stream := range forcedStreams {
			if matchesLanguage(stream.Language, stream.Title, normalizedPref) {
				log.Printf("[track] Selected forced subtitle track %d for language %q", stream.Index, preferredLanguage)
				return stream.Index
			}
		}
		return -1
	}

	// Mode is "on" - prefer SDH > regular > forced
	if normalizedPref != "" {
		// Pass 1: SDH tracks matching language (non-forced)
		for _, stream := range streams {
			if !stream.IsForced && isSDHTrack(stream.Title) && matchesLanguage(stream.Language, stream.Title, normalizedPref) {
				log.Printf("[track] Selected SDH subtitle track %d for language %q", stream.Index, preferredLanguage)
				return stream.Index
			}
		}

		// Pass 2: Regular non-forced, non-SDH tracks matching language
		for _, stream := range streams {
			if !stream.IsForced && !isSDHTrack(stream.Title) && matchesLanguage(stream.Language, stream.Title, normalizedPref) {
				log.Printf("[track] Selected regular subtitle track %d for language %q", stream.Index, preferredLanguage)
				return stream.Index
			}
		}

		// Pass 3: Forced tracks matching language (last resort for "on" mode)
		for _, stream := range streams {
			if stream.IsForced && matchesLanguage(stream.Language, stream.Title, normalizedPref) {
				log.Printf("[track] Selected forced subtitle track %d for language %q (only option)", stream.Index, preferredLanguage)
				return stream.Index
			}
		}
	}

	// No match found - return -1 to trigger auto-search
	return -1
}
