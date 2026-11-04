// Package trackselect chooses audio and subtitle tracks for playback from the
// effective user/profile/client/title language preferences. It is shared by
// prequeue, manual source resolution and any other path that must answer
// "which embedded tracks should the player start with?".
package trackselect

import (
	"log"
	"strings"

	"novastream/models"
	langutil "novastream/utils/language"
)

// CompatibleAudioCodecs lists codecs that can be played without transcoding.
var CompatibleAudioCodecs = map[string]bool{
	"aac": true, "ac3": true, "eac3": true, "mp3": true,
}

// IsTrueHDCodec returns true specifically for TrueHD/MLP codecs which are particularly
// problematic for streaming. We prefer to avoid these unless they're the only option.
func IsTrueHDCodec(codec string) bool {
	c := strings.ToLower(strings.TrimSpace(codec))
	return c == "truehd" || c == "mlp"
}

// IsUnsupportedNativeSubtitleCodec reports DVD/VobSub bitmap subtitles that
// native players cannot render.
func IsUnsupportedNativeSubtitleCodec(codec string) bool {
	c := strings.ToLower(strings.TrimSpace(codec))
	return c == "dvd_subtitle" ||
		c == "dvdsub" ||
		c == "vobsub" ||
		c == "application/vobsub" ||
		strings.Contains(c, "dvd_subtitle") ||
		strings.Contains(c, "dvd subtitle") ||
		strings.Contains(c, "vobsub")
}

var bitmapSubtitleCodecs = map[string]bool{
	"hdmv_pgs_subtitle": true,
	"dvd_subtitle":      true,
	"dvdsub":            true,
	"pgssub":            true,
}

// IsBitmapSubtitleCodec reports image-based subtitle codecs (PGS, VobSub).
func IsBitmapSubtitleCodec(codec string) bool {
	return bitmapSubtitleCodecs[strings.ToLower(strings.TrimSpace(codec))]
}

var textSubtitleCodecs = map[string]bool{
	"subrip": true, "srt": true, "ass": true, "ssa": true,
	"webvtt": true, "vtt": true, "mov_text": true, "text": true,
}

// IsTextSubtitleCodec reports subtitle codecs that text-only renderers (the
// web player's WebVTT path) can display.
func IsTextSubtitleCodec(codec string) bool {
	return textSubtitleCodecs[strings.ToLower(strings.TrimSpace(codec))]
}

// IsCommentaryTrack checks if an audio track is a commentary or audio
// description track (not the main mix) based on its title
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
		"audio description",
		"audiodescription",
		"audio-description",
		"descriptive audio",
		"described video",
		"audiodescripción",
	}
	for _, indicator := range commentaryIndicators {
		if strings.Contains(lowerTitle, indicator) {
			return true
		}
	}
	// "French (France) AD", "AD 5.1": an audio description label.
	for _, word := range strings.FieldsFunc(lowerTitle, func(r rune) bool { return r < 'a' || r > 'z' }) {
		if word == "ad" {
			return true
		}
	}
	return false
}

// MatchesLanguage checks if a stream matches the preferred language
func MatchesLanguage(language, title, normalizedPref string) bool {
	language = strings.ToLower(strings.TrimSpace(language))
	title = strings.ToLower(strings.TrimSpace(title))
	normalizedPref = strings.ToLower(strings.TrimSpace(normalizedPref))
	if normalizedPref == "" {
		return false
	}
	if langutil.NormalizeToCode(normalizedPref) != "" {
		if langutil.HasPreferredLanguage(language, normalizedPref) {
			return true
		}
		// Match labels such as "Polish 5.1" on word boundaries, never a
		// substring of an unrelated word or language name.
		for _, token := range strings.FieldsFunc(language+" "+title, func(r rune) bool {
			return r < 'a' || r > 'z'
		}) {
			if langutil.HasPreferredLanguage(token, normalizedPref) {
				return true
			}
		}
		return false
	}

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

// FindAudioTrack finds an audio track matching the preferred language.
// Prefers E-AC-3 (including Dolby Digital Plus Atmos) before other compatible
// codecs, then avoids TrueHD/DTS when multiple tracks exist.
// Specifically avoids TrueHD/MLP unless it's the only option for the preferred language.
// Skips commentary tracks unless they are the only option.
// A region-tagged preference ("fr-CA", "es-419") first tries that regional
// dub ("VFQ", "Latino") and falls back to any track of the language.
// Returns -1 if no matching track is found.
func FindAudioTrack(streams []models.AudioStreamInfo, preferredLanguage string) int {
	if preferredLanguage == "" || len(streams) == 0 {
		return -1
	}
	if regional := regionalStreams(streams, preferredLanguage); len(regional) > 0 {
		if selected := findAudioTrack(regional, preferredLanguage); selected >= 0 {
			return selected
		}
	}
	return findAudioTrack(streams, preferredLanguage)
}

func findAudioTrack(streams []models.AudioStreamInfo, preferredLanguage string) int {
	normalizedPref := strings.ToLower(strings.TrimSpace(preferredLanguage))

	// Pass 1: E-AC-3 matching language, skipping commentary. Keeping DDP ahead
	// of AAC/AC-3 allows native tvOS playback to preserve an Atmos/JOC stream.
	for _, stream := range streams {
		if MatchesLanguage(stream.Language, stream.Title, normalizedPref) &&
			strings.EqualFold(strings.TrimSpace(stream.Codec), "eac3") &&
			!IsCommentaryTrack(stream.Title) {
			log.Printf("[track] Preferred E-AC-3 audio track %d for language %q",
				stream.Index, preferredLanguage)
			return stream.Index
		}
	}

	// Pass 2: Other compatible codec (AAC, AC3, etc.) matching language, skipping commentary
	for _, stream := range streams {
		if MatchesLanguage(stream.Language, stream.Title, normalizedPref) &&
			CompatibleAudioCodecs[strings.ToLower(stream.Codec)] &&
			!IsCommentaryTrack(stream.Title) {
			log.Printf("[track] Preferred compatible audio track %d (%s) for language %q",
				stream.Index, stream.Codec, preferredLanguage)
			return stream.Index
		}
	}

	// Pass 3: Non-TrueHD incompatible codec (DTS, etc.) matching language, skipping commentary
	// TrueHD is particularly problematic for streaming, so prefer DTS over TrueHD
	for _, stream := range streams {
		if MatchesLanguage(stream.Language, stream.Title, normalizedPref) &&
			!IsTrueHDCodec(stream.Codec) &&
			!IsCommentaryTrack(stream.Title) {
			log.Printf("[track] Selected non-TrueHD audio track %d (%s) for language %q - will need HLS transcoding",
				stream.Index, stream.Codec, preferredLanguage)
			return stream.Index
		}
	}

	// Pass 4: TrueHD/MLP matching language, skipping commentary (only if no other option)
	for _, stream := range streams {
		if MatchesLanguage(stream.Language, stream.Title, normalizedPref) &&
			IsTrueHDCodec(stream.Codec) &&
			!IsCommentaryTrack(stream.Title) {
			log.Printf("[track] Selected TrueHD audio track %d (%s) for language %q (only option) - will need HLS transcoding",
				stream.Index, stream.Codec, preferredLanguage)
			return stream.Index
		}
	}

	// Pass 5: E-AC-3 matching language, including commentary
	for _, stream := range streams {
		if MatchesLanguage(stream.Language, stream.Title, normalizedPref) &&
			strings.EqualFold(strings.TrimSpace(stream.Codec), "eac3") {
			log.Printf("[track] Fallback to E-AC-3 audio track %d (commentary) for language %q",
				stream.Index, preferredLanguage)
			return stream.Index
		}
	}

	// Pass 6: Other compatible codec matching language, including commentary
	for _, stream := range streams {
		if MatchesLanguage(stream.Language, stream.Title, normalizedPref) &&
			CompatibleAudioCodecs[strings.ToLower(stream.Codec)] {
			log.Printf("[track] Fallback to compatible audio track %d (%s, commentary) for language %q",
				stream.Index, stream.Codec, preferredLanguage)
			return stream.Index
		}
	}

	// Pass 7: Non-TrueHD incompatible codec matching language, including commentary
	for _, stream := range streams {
		if MatchesLanguage(stream.Language, stream.Title, normalizedPref) &&
			!IsTrueHDCodec(stream.Codec) {
			log.Printf("[track] Fallback to non-TrueHD audio track %d (%s, commentary) for language %q - will need HLS transcoding",
				stream.Index, stream.Codec, preferredLanguage)
			return stream.Index
		}
	}

	// Pass 8: TrueHD/MLP matching language, including commentary (last resort)
	for _, stream := range streams {
		if MatchesLanguage(stream.Language, stream.Title, normalizedPref) {
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
func isForcedTrack(stream models.SubtitleStreamInfo) bool {
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

// FindSubtitleTrack finds a subtitle track matching the preferences.
// mode can be "off", "forced-only", "on", or "foreign-audio" (resolved from
// audioLanguage by ResolveForeignAudioMode).
// audioLanguage is the language of the selected audio track (used for same-language vs foreign-language priority).
// When mode is "on" with same-language audio, prefers SDH > dubtitles > full/plain > non-signs > signs.
// When mode is "on" with foreign-language audio, prefers SDH > full/plain > dubtitles > non-signs > signs.
// Returns -1 if no matching track is found or mode is "off".
func FindSubtitleTrack(streams []models.SubtitleStreamInfo, preferredLanguage, mode, audioLanguage string) int {
	if mode == SubtitleModeForeignAudio {
		mode = ResolveForeignAudioMode(audioLanguage, preferredLanguage, "")
	}
	if len(streams) == 0 || mode == "off" {
		return -1
	}

	selectableStreams := make([]models.SubtitleStreamInfo, 0, len(streams))
	for _, stream := range streams {
		if !IsUnsupportedNativeSubtitleCodec(stream.Codec) {
			selectableStreams = append(selectableStreams, stream)
		}
	}
	if len(selectableStreams) == 0 {
		return -1
	}

	normalizedPref := strings.ToLower(strings.TrimSpace(preferredLanguage))

	// For forced-only mode, only consider forced tracks
	if mode == "forced-only" {
		var forcedStreams []models.SubtitleStreamInfo
		for _, s := range selectableStreams {
			if isForcedTrack(s) {
				forcedStreams = append(forcedStreams, s)
			}
		}
		if len(forcedStreams) == 0 {
			return -1
		}
		// Find matching forced track
		for _, stream := range forcedStreams {
			if MatchesLanguage(stream.Language, stream.Title, normalizedPref) {
				log.Printf("[track] Selected forced subtitle track %d for language %q", stream.Index, preferredLanguage)
				return stream.Index
			}
		}
		return -1
	}

	// Mode is "on" - audio-aware priority ordering
	if normalizedPref != "" {
		// Collect non-forced streams matching the preferred language
		var nonForcedMatches []models.SubtitleStreamInfo
		for _, stream := range selectableStreams {
			if !isForcedTrack(stream) && MatchesLanguage(stream.Language, stream.Title, normalizedPref) {
				nonForcedMatches = append(nonForcedMatches, stream)
			}
		}

		if len(nonForcedMatches) > 0 {
			// Determine if audio and subtitle languages match
			normalizedAudio := strings.ToLower(strings.TrimSpace(audioLanguage))
			isSameLanguage := MatchesLanguage(normalizedAudio, "", normalizedPref)

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
		for _, stream := range selectableStreams {
			if isForcedTrack(stream) && MatchesLanguage(stream.Language, stream.Title, normalizedPref) {
				log.Printf("[track] Selected forced subtitle track %d for language %q (only option)", stream.Index, preferredLanguage)
				return stream.Index
			}
		}
	}

	// No match found - return -1 to trigger auto-search
	return -1
}

// SanitizeLanguageCode strips stray quotes and whitespace from language codes.
func SanitizeLanguageCode(code string) string {
	code = strings.TrimSpace(code)
	code = strings.Trim(code, "'\"")
	code = strings.TrimSpace(code)
	return code
}

// NormalizeSubtitleMode maps legacy subtitle mode values to canonical ones
// ("off", "forced-only", "on", "foreign-audio").
func NormalizeSubtitleMode(mode string) string {
	mode = strings.TrimSpace(strings.Trim(strings.TrimSpace(mode), "'\""))
	switch mode {
	case "auto":
		return "forced-only"
	case "always":
		return "on"
	case "smart", "foreign":
		return SubtitleModeForeignAudio
	case "":
		return "off"
	default:
		return mode
	}
}

// NormalizeAllowedLanguages lowercases, sanitizes and de-duplicates an
// allowed-track-language list.
func NormalizeAllowedLanguages(languages []string) []string {
	seen := make(map[string]struct{}, len(languages))
	normalized := make([]string, 0, len(languages))
	for _, language := range languages {
		code := strings.ToLower(SanitizeLanguageCode(language))
		if code == "" {
			continue
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		normalized = append(normalized, code)
	}
	return normalized
}

// FindAllowedAudioTrack picks the preferred-language audio track when that
// language is allowed, otherwise the first allowed language with a matching
// track. With no allowed-language restriction it is FindAudioTrack.
func FindAllowedAudioTrack(streams []models.AudioStreamInfo, allowedLanguages []string, preferredLanguage string) int {
	allowedLanguages = NormalizeAllowedLanguages(allowedLanguages)
	if len(allowedLanguages) == 0 {
		return FindAudioTrack(streams, preferredLanguage)
	}

	preferredLanguage = strings.ToLower(SanitizeLanguageCode(preferredLanguage))
	if preferredLanguage != "" {
		for _, allowedLanguage := range allowedLanguages {
			if MatchesLanguage(preferredLanguage, "", allowedLanguage) {
				if selected := FindAudioTrack(streams, preferredLanguage); selected >= 0 {
					return selected
				}
				break
			}
		}
	}

	for _, allowedLanguage := range allowedLanguages {
		if selected := FindAudioTrack(streams, allowedLanguage); selected >= 0 {
			return selected
		}
	}
	return -1
}
