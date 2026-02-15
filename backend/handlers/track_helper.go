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

// isForcedTrack checks if a subtitle track is a forced track.
// Checks both the IsForced flag (from ffprobe disposition) and the title for "(forced)".
// Many release groups put "forced" in the title without setting the disposition flag.
func isForcedTrack(stream SubtitleStreamInfo) bool {
	if stream.IsForced {
		return true
	}
	// Also check title for "forced" keyword
	lower := strings.ToLower(strings.TrimSpace(stream.Title))
	return strings.Contains(lower, "forced")
}

// isSignsTrack checks if a subtitle track is a "Signs and Songs" track.
// These only show foreign text inserts (signs, karaoke), not dialogue.
func isSignsTrack(title string) bool {
	lower := strings.ToLower(strings.TrimSpace(title))
	return strings.Contains(lower, "sign") || strings.Contains(lower, "song")
}

// isDubtitleTrack checks if a subtitle track is a "Dubtitle" track.
// Dubtitles are dialogue captions matching the dub audio (same-language subs).
func isDubtitleTrack(title string) bool {
	lower := strings.ToLower(strings.TrimSpace(title))
	return strings.Contains(lower, "dubtitle")
}

// isFullSubsTrack checks if a subtitle track is a "Full Subs" track.
func isFullSubsTrack(title string) bool {
	lower := strings.ToLower(strings.TrimSpace(title))
	return strings.Contains(lower, "full sub")
}

// FindSubtitleTrackByPreference finds a subtitle track matching the preferences.
// mode can be "off", "forced-only", or "on".
// audioLanguage is the language of the selected audio track (used for same-language vs foreign-language priority).
// When mode is "on" with same-language audio, prefers SDH > dubtitles > full/plain > non-signs > signs.
// When mode is "on" with foreign-language audio, prefers SDH > full/plain > dubtitles > non-signs > signs.
// Returns -1 if no matching track is found or mode is "off".
func FindSubtitleTrackByPreference(streams []SubtitleStreamInfo, preferredLanguage, mode, audioLanguage string) int {
	if len(streams) == 0 || mode == "off" {
		return -1
	}

	normalizedPref := strings.ToLower(strings.TrimSpace(preferredLanguage))

	// For forced-only mode, only consider forced tracks
	if mode == "forced-only" {
		var forcedStreams []SubtitleStreamInfo
		for _, s := range streams {
			if isForcedTrack(s) {
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

	// Mode is "on" - audio-aware priority ordering
	if normalizedPref != "" {
		// Collect non-forced streams matching the preferred language
		var nonForcedMatches []SubtitleStreamInfo
		for _, stream := range streams {
			if !isForcedTrack(stream) && matchesLanguage(stream.Language, stream.Title, normalizedPref) {
				nonForcedMatches = append(nonForcedMatches, stream)
			}
		}

		if len(nonForcedMatches) > 0 {
			// Determine if audio and subtitle languages match
			normalizedAudio := strings.ToLower(strings.TrimSpace(audioLanguage))
			isSameLanguage := normalizedAudio != "" && normalizedPref != "" &&
				(normalizedAudio == normalizedPref ||
					strings.Contains(normalizedAudio, normalizedPref) ||
					strings.Contains(normalizedPref, normalizedAudio))

			// Priority 1: SDH subtitles (always top priority)
			for _, stream := range nonForcedMatches {
				if isSDHTrack(stream.Title) && !isSignsTrack(stream.Title) {
					log.Printf("[track] Selected SDH subtitle track %d for language %q (audioLang: %q)", stream.Index, preferredLanguage, audioLanguage)
					return stream.Index
				}
			}

			if isSameLanguage {
				// Same-language subs (e.g. English audio + English subs)
				// Priority 2: Dubtitle tracks
				for _, stream := range nonForcedMatches {
					if isDubtitleTrack(stream.Title) {
						log.Printf("[track] Selected dubtitle subtitle track %d for language %q (same-lang audio: %q)", stream.Index, preferredLanguage, audioLanguage)
						return stream.Index
					}
				}
				// Priority 3: Full subs or plain (no title)
				for _, stream := range nonForcedMatches {
					if isFullSubsTrack(stream.Title) || strings.TrimSpace(stream.Title) == "" {
						log.Printf("[track] Selected full/plain subtitle track %d for language %q (same-lang audio: %q)", stream.Index, preferredLanguage, audioLanguage)
						return stream.Index
					}
				}
			} else {
				// Foreign-language subs (e.g. Japanese audio + English subs)
				// Priority 2: Full subs or plain (no title)
				for _, stream := range nonForcedMatches {
					if isFullSubsTrack(stream.Title) || strings.TrimSpace(stream.Title) == "" {
						log.Printf("[track] Selected full/plain subtitle track %d for language %q (foreign audio: %q)", stream.Index, preferredLanguage, audioLanguage)
						return stream.Index
					}
				}
				// Priority 3: Dubtitle tracks
				for _, stream := range nonForcedMatches {
					if isDubtitleTrack(stream.Title) {
						log.Printf("[track] Selected dubtitle subtitle track %d for language %q (foreign audio: %q)", stream.Index, preferredLanguage, audioLanguage)
						return stream.Index
					}
				}
			}

			// Priority 4: Any non-signs, non-forced track
			for _, stream := range nonForcedMatches {
				if !isSignsTrack(stream.Title) {
					log.Printf("[track] Selected non-signs subtitle track %d for language %q (audioLang: %q)", stream.Index, preferredLanguage, audioLanguage)
					return stream.Index
				}
			}

			// Priority 5: Signs/Songs (last resort)
			log.Printf("[track] Selected signs/songs subtitle track %d for language %q (last resort, audioLang: %q)", nonForcedMatches[0].Index, preferredLanguage, audioLanguage)
			return nonForcedMatches[0].Index
		}

		// Fallback: forced tracks matching language
		for _, stream := range streams {
			if isForcedTrack(stream) && matchesLanguage(stream.Language, stream.Title, normalizedPref) {
				log.Printf("[track] Selected forced subtitle track %d for language %q (only option)", stream.Index, preferredLanguage)
				return stream.Index
			}
		}
	}

	// No match found - return -1 to trigger auto-search
	return -1
}
