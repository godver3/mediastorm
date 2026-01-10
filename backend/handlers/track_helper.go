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

// IsIncompatibleAudioCodec returns true for codecs that need transcoding (TrueHD, DTS, etc.)
func IsIncompatibleAudioCodec(codec string) bool {
	c := strings.ToLower(strings.TrimSpace(codec))
	return c == "truehd" || c == "dts" || strings.HasPrefix(c, "dts-") ||
		c == "dts_hd" || c == "dtshd" || c == "mlp"
}

// IsTrueHDCodec returns true specifically for TrueHD/MLP codecs which are particularly
// problematic for streaming. We prefer to avoid these unless they're the only option.
func IsTrueHDCodec(codec string) bool {
	c := strings.ToLower(strings.TrimSpace(codec))
	return c == "truehd" || c == "mlp"
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

// FindSubtitleTrackByPreference finds a subtitle track matching the preferences.
// mode can be "off", "forced-only", or "on".
// Returns -1 if no matching track is found or mode is "off".
func FindSubtitleTrackByPreference(streams []SubtitleStreamInfo, preferredLanguage, mode string) int {
	if len(streams) == 0 || mode == "off" {
		return -1
	}

	normalizedPref := strings.ToLower(strings.TrimSpace(preferredLanguage))

	// Filter by mode
	var candidateStreams []SubtitleStreamInfo
	if mode == "forced-only" {
		for _, s := range streams {
			if s.IsForced {
				candidateStreams = append(candidateStreams, s)
			}
		}
		if len(candidateStreams) == 0 {
			// No forced subtitles available
			return -1
		}
	} else {
		candidateStreams = streams
	}

	// If language preference is set, try to find a match
	if normalizedPref != "" {
		// Try exact match
		for _, stream := range candidateStreams {
			language := strings.ToLower(strings.TrimSpace(stream.Language))
			title := strings.ToLower(strings.TrimSpace(stream.Title))

			if language == normalizedPref || title == normalizedPref {
				return stream.Index
			}
		}

		// Try partial match (skip empty strings to avoid false positives)
		for _, stream := range candidateStreams {
			language := strings.ToLower(strings.TrimSpace(stream.Language))
			title := strings.ToLower(strings.TrimSpace(stream.Title))

			if language != "" && (strings.Contains(language, normalizedPref) || strings.Contains(normalizedPref, language)) {
				return stream.Index
			}
			if title != "" && (strings.Contains(title, normalizedPref) || strings.Contains(normalizedPref, title)) {
				return stream.Index
			}
		}
	}

	// If mode is "on" and no language match, return -1 to trigger auto-search
	// We don't want to fall back to a random language the user didn't ask for
	return -1
}
