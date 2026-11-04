package trackselect

import (
	"strings"

	"novastream/models"
	langutil "novastream/utils/language"
)

// Preferences are the effective language preferences for one playback
// (global defaults merged with profile, client and per-title overrides).
type Preferences struct {
	AudioLanguage    string
	AllowedLanguages []string
	SubtitleLanguage string
	// SubtitleMode accepts canonical ("off", "forced-only", "on",
	// "foreign-audio") or legacy ("auto", "always") values.
	SubtitleMode string
}

// Normalized returns a copy with sanitized languages and a canonical mode.
func (p Preferences) Normalized() Preferences {
	return Preferences{
		AudioLanguage:    SanitizeLanguageCode(p.AudioLanguage),
		AllowedLanguages: NormalizeAllowedLanguages(p.AllowedLanguages),
		SubtitleLanguage: SanitizeLanguageCode(p.SubtitleLanguage),
		SubtitleMode:     NormalizeSubtitleMode(p.SubtitleMode),
	}
}

// PreferencesFromPlayback extracts track preferences from playback settings.
func PreferencesFromPlayback(settings models.PlaybackSettings) Preferences {
	var allowed []string
	if settings.AllowedTrackLanguages != nil {
		allowed = append(allowed, (*settings.AllowedTrackLanguages)...)
	}
	return Preferences{
		AudioLanguage:    settings.PreferredAudioLanguage,
		AllowedLanguages: allowed,
		SubtitleLanguage: settings.PreferredSubtitleLanguage,
		SubtitleMode:     settings.PreferredSubtitleMode,
	}.Normalized()
}

// Select chooses the audio and subtitle tracks for the given container
// streams. It is the single selection routine shared by prequeue and manual
// resolution, so every client receives the same answer for the same file and
// preferences.
func Select(audioStreams []models.AudioStreamInfo, subtitleStreams []models.SubtitleStreamInfo, prefs Preferences) *models.TrackSelection {
	prefs = prefs.Normalized()
	selection := &models.TrackSelection{
		AudioLanguage:    prefs.AudioLanguage,
		SubtitleLanguage: prefs.SubtitleLanguage,
		SubtitleMode:     prefs.SubtitleMode,
	}

	audioIndex := -1
	if prefs.AudioLanguage != "" || len(prefs.AllowedLanguages) > 0 {
		audioIndex = FindAllowedAudioTrack(audioStreams, prefs.AllowedLanguages, prefs.AudioLanguage)
	}
	selection.Audio = DescribeAudioTrack(audioStreams, audioIndex)

	// "foreign-audio" becomes "on" or "forced-only" from the audio that will
	// play, and the selection reports that resolved mode so clients that only
	// know the three base modes keep working.
	if prefs.SubtitleMode == SubtitleModeForeignAudio {
		prefs.SubtitleMode = ResolveForeignAudioMode(playingAudioLanguage(audioStreams, selection.Audio),
			prefs.SubtitleLanguage, prefs.AudioLanguage)
		selection.SubtitleMode = prefs.SubtitleMode
		selection.SubtitleModePreference = SubtitleModeForeignAudio
	}

	if prefs.SubtitleMode == "off" {
		return selection
	}

	// Audio-aware subtitle priority uses the language actually selected.
	actualAudioLanguage := prefs.AudioLanguage
	if selection.Audio != nil {
		actualAudioLanguage = selection.Audio.Language
	}

	subtitleIndex := FindSubtitleTrack(subtitleStreams, prefs.SubtitleLanguage, prefs.SubtitleMode, actualAudioLanguage)
	selection.Subtitle = DescribeSubtitleTrack(subtitleStreams, subtitleIndex)

	if selection.Subtitle != nil && IsTextSubtitleCodec(selection.Subtitle.Codec) {
		selection.TextSubtitle = selection.Subtitle
	} else {
		textStreams := make([]models.SubtitleStreamInfo, 0, len(subtitleStreams))
		for _, stream := range subtitleStreams {
			if IsTextSubtitleCodec(stream.Codec) {
				textStreams = append(textStreams, stream)
			}
		}
		textIndex := FindSubtitleTrack(textStreams, prefs.SubtitleLanguage, prefs.SubtitleMode, actualAudioLanguage)
		selection.TextSubtitle = DescribeSubtitleTrack(subtitleStreams, textIndex)
	}
	return selection
}

// SubtitleModeForeignAudio shows forced subtitles when the audio is in the
// viewer's language and full subtitles when it is not.
const SubtitleModeForeignAudio = "foreign-audio"

// ResolveForeignAudioMode returns "on" when audioLanguage is known and not the
// viewer's language (the subtitle language, else the preferred audio
// language), otherwise "forced-only". Untagged audio counts as the viewer's
// language, so an unlabelled track never forces full subtitles on.
func ResolveForeignAudioMode(audioLanguage, subtitleLanguage, preferredAudioLanguage string) string {
	mine := SanitizeLanguageCode(subtitleLanguage)
	if mine == "" {
		mine = SanitizeLanguageCode(preferredAudioLanguage)
	}
	audio := strings.ToLower(strings.TrimSpace(audioLanguage))
	if mine == "" || audio == "" || audio == "und" || MatchesLanguage(audio, "", mine) {
		return "forced-only"
	}
	return "on"
}

// playingAudioLanguage is the language of the audio the player will start:
// the selected track, else the container's first audio track (the player's
// default when no track matched the preferences).
func playingAudioLanguage(streams []models.AudioStreamInfo, selected *models.SelectedTrack) string {
	if selected != nil {
		return selected.Language
	}
	if len(streams) > 0 {
		return streams[0].Language
	}
	return ""
}

// languageKey groups tracks by language: the normalized ISO 639-2 code when
// known (folding bibliographic/terminologic variants), else the raw tag.
func languageKey(language string) string {
	if code := canonicalLanguageCode(language); code != "" {
		return code
	}
	return strings.ToLower(strings.TrimSpace(language))
}

var bibliographicToTerminologic = map[string]string{
	"chi": "zho", "dut": "nld", "cze": "ces", "gre": "ell", "rum": "ron",
	"fre": "fra", "ger": "deu",
}

func canonicalLanguageCode(language string) string {
	code := langutil.NormalizeToCode(language)
	if mapped, ok := bibliographicToTerminologic[code]; ok {
		return mapped
	}
	return code
}

// DescribeAudioTrack returns the descriptor for the audio stream with the
// given container index, or nil when it is not present.
func DescribeAudioTrack(streams []models.AudioStreamInfo, streamIndex int) *models.SelectedTrack {
	if streamIndex < 0 {
		return nil
	}
	for typeIndex, stream := range streams {
		if stream.Index != streamIndex {
			continue
		}
		key := languageKey(stream.Language)
		languageIndex, languageCount := 0, 0
		for i, other := range streams {
			if languageKey(other.Language) != key {
				continue
			}
			if i < typeIndex {
				languageIndex++
			}
			languageCount++
		}
		return &models.SelectedTrack{
			StreamIndex:   stream.Index,
			TypeIndex:     typeIndex,
			Language:      stream.Language,
			LanguageCode:  canonicalLanguageCode(stream.Language),
			LanguageIndex: languageIndex,
			LanguageCount: languageCount,
			Codec:         stream.Codec,
			Profile:       stream.Profile,
			Title:         stream.Title,
		}
	}
	return nil
}

// DescribeSubtitleTrack returns the descriptor for the subtitle stream with
// the given container index, or nil when it is not present.
func DescribeSubtitleTrack(streams []models.SubtitleStreamInfo, streamIndex int) *models.SelectedTrack {
	if streamIndex < 0 {
		return nil
	}
	for typeIndex, stream := range streams {
		if stream.Index != streamIndex {
			continue
		}
		key := languageKey(stream.Language)
		languageIndex, languageCount := 0, 0
		for i, other := range streams {
			if languageKey(other.Language) != key {
				continue
			}
			if i < typeIndex {
				languageIndex++
			}
			languageCount++
		}
		return &models.SelectedTrack{
			StreamIndex:   stream.Index,
			TypeIndex:     typeIndex,
			Language:      stream.Language,
			LanguageCode:  canonicalLanguageCode(stream.Language),
			LanguageIndex: languageIndex,
			LanguageCount: languageCount,
			Codec:         stream.Codec,
			Title:         stream.Title,
			Forced:        isForcedTrack(stream),
			Default:       stream.IsDefault,
			Bitmap:        IsBitmapSubtitleCodec(stream.Codec),
		}
	}
	return nil
}
