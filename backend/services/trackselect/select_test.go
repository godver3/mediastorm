package trackselect

import (
	"errors"
	"testing"

	"novastream/config"
	"novastream/models"
)

func sampleStreams() ([]models.AudioStreamInfo, []models.SubtitleStreamInfo) {
	audio := []models.AudioStreamInfo{
		{Index: 1, Codec: "truehd", Language: "eng", Title: "TrueHD Atmos"},
		{Index: 2, Codec: "eac3", Language: "eng", Title: "DDP 5.1"},
		{Index: 3, Codec: "ac3", Language: "jpn"},
		{Index: 4, Codec: "aac", Language: "en", Title: "Commentary"},
	}
	subs := []models.SubtitleStreamInfo{
		{Index: 5, Codec: "hdmv_pgs_subtitle", Language: "eng"},
		{Index: 6, Codec: "subrip", Language: "eng", Title: "Forced", IsForced: true},
		{Index: 7, Codec: "subrip", Language: "eng", Title: "SDH"},
		{Index: 8, Codec: "hdmv_pgs_subtitle", Language: "spa", IsDefault: true},
		{Index: 9, Codec: "subrip", Language: "spa"},
	}
	return audio, subs
}

func TestSelectDescribesAudioAndSubtitle(t *testing.T) {
	audio, subs := sampleStreams()
	sel := Select(audio, subs, Preferences{AudioLanguage: "eng", SubtitleLanguage: "eng", SubtitleMode: "always"})

	if sel.SubtitleMode != "on" {
		t.Fatalf("SubtitleMode = %q, want on", sel.SubtitleMode)
	}
	if sel.Audio == nil || sel.Audio.StreamIndex != 2 {
		t.Fatalf("audio = %#v, want E-AC-3 stream 2", sel.Audio)
	}
	if sel.Audio.TypeIndex != 1 || sel.Audio.LanguageCode != "eng" || sel.Audio.LanguageIndex != 1 || sel.Audio.LanguageCount != 3 {
		t.Fatalf("audio descriptor = %#v, want typeIndex 1, eng #1 of 3 (en folds into eng)", sel.Audio)
	}
	if sel.Subtitle == nil || sel.Subtitle.StreamIndex != 7 {
		t.Fatalf("subtitle = %#v, want SDH stream 7", sel.Subtitle)
	}
	if sel.Subtitle.TypeIndex != 2 || sel.Subtitle.LanguageIndex != 2 || sel.Subtitle.LanguageCount != 3 || sel.Subtitle.Bitmap {
		t.Fatalf("subtitle descriptor = %#v", sel.Subtitle)
	}
	if sel.TextSubtitle != sel.Subtitle {
		t.Fatalf("TextSubtitle should equal Subtitle for a text pick, got %#v", sel.TextSubtitle)
	}
	if sel.AudioStreamIndex() != 2 || sel.SubtitleStreamIndex() != 7 {
		t.Fatalf("stream index helpers = %d/%d", sel.AudioStreamIndex(), sel.SubtitleStreamIndex())
	}
}

func TestSelectProvidesTextAlternativeForBitmapPick(t *testing.T) {
	audio, subs := sampleStreams()
	sel := Select(audio, subs, Preferences{AudioLanguage: "jpn", SubtitleLanguage: "spa", SubtitleMode: "on"})

	if sel.Audio == nil || sel.Audio.StreamIndex != 3 {
		t.Fatalf("audio = %#v, want jpn stream 3", sel.Audio)
	}
	if sel.Subtitle == nil || sel.Subtitle.StreamIndex != 8 || !sel.Subtitle.Bitmap || !sel.Subtitle.Default {
		t.Fatalf("subtitle = %#v, want bitmap default stream 8", sel.Subtitle)
	}
	if sel.TextSubtitle == nil || sel.TextSubtitle.StreamIndex != 9 || sel.TextSubtitle.TypeIndex != 4 || sel.TextSubtitle.LanguageIndex != 1 {
		t.Fatalf("textSubtitle = %#v, want subrip stream 9", sel.TextSubtitle)
	}
}

func TestSelectSubtitlesOffAndNoMatch(t *testing.T) {
	audio, subs := sampleStreams()
	off := Select(audio, subs, Preferences{AudioLanguage: "eng", SubtitleLanguage: "eng", SubtitleMode: ""})
	if off.SubtitleMode != "off" || off.Subtitle != nil || off.TextSubtitle != nil {
		t.Fatalf("off selection = %#v", off)
	}

	none := Select(audio, subs, Preferences{AudioLanguage: "fra", SubtitleLanguage: "deu", SubtitleMode: "on"})
	if none.Audio != nil || none.Subtitle != nil || none.SubtitleMode != "on" || none.SubtitleLanguage != "deu" {
		t.Fatalf("no-match selection = %#v", none)
	}
	if none.AudioStreamIndex() != -1 || none.SubtitleStreamIndex() != -1 {
		t.Fatalf("no-match indexes = %d/%d", none.AudioStreamIndex(), none.SubtitleStreamIndex())
	}
}

func TestSelectForcedOnly(t *testing.T) {
	audio, subs := sampleStreams()
	sel := Select(audio, subs, Preferences{AudioLanguage: "eng", SubtitleLanguage: "eng", SubtitleMode: "auto"})
	if sel.SubtitleMode != "forced-only" || sel.Subtitle == nil || sel.Subtitle.StreamIndex != 6 || !sel.Subtitle.Forced {
		t.Fatalf("forced-only selection = %#v", sel.Subtitle)
	}
}

func TestSelectHonorsAllowedLanguages(t *testing.T) {
	audio, subs := sampleStreams()
	sel := Select(audio, subs, Preferences{AudioLanguage: "eng", AllowedLanguages: []string{"jpn"}})
	if sel.Audio == nil || sel.Audio.StreamIndex != 3 {
		t.Fatalf("audio = %#v, want allowed jpn stream 3", sel.Audio)
	}
}

type fakeConfig struct {
	settings config.Settings
	err      error
}

func (f fakeConfig) Load() (config.Settings, error) { return f.settings, f.err }

type fakeUserSettings struct {
	playback map[string]models.PlaybackSettings
}

func (f fakeUserSettings) GetWithDefaults(userID string, defaults models.UserSettings) (models.UserSettings, error) {
	if p, ok := f.playback[userID]; ok {
		defaults.Playback = p
		return defaults, nil
	}
	return defaults, nil
}

type fakeClientSettings struct{ settings *models.ClientFilterSettings }

func (f fakeClientSettings) Get(clientID, userID string) (*models.ClientFilterSettings, error) {
	if clientID != "tv" {
		return nil, nil
	}
	return f.settings, nil
}

type fakeContentPrefs struct {
	pref *models.ContentPreference
	err  error
}

func (f fakeContentPrefs) Get(userID, contentID string) (*models.ContentPreference, error) {
	if contentID != "tmdb:movie:1" {
		return nil, f.err
	}
	return f.pref, nil
}

func TestResolverPrecedence(t *testing.T) {
	global := config.Settings{}
	global.Playback.PreferredAudioLanguage = "eng"
	global.Playback.PreferredSubtitleLanguage = "eng"
	global.Playback.PreferredSubtitleMode = "off"

	jpn := "jpn"
	resolver := &Resolver{
		Config: fakeConfig{settings: global},
		UserSettings: fakeUserSettings{playback: map[string]models.PlaybackSettings{
			"user1": {PreferredAudioLanguage: "spa", PreferredSubtitleLanguage: "spa", PreferredSubtitleMode: "always"},
		}},
		ClientSettings:     fakeClientSettings{settings: &models.ClientFilterSettings{PreferredAudioLanguage: &jpn}},
		ContentPreferences: fakeContentPrefs{pref: &models.ContentPreference{SubtitleLanguage: "'fra'", SubtitleMode: "forced-only"}},
	}

	if got := resolver.Resolve("", "", ""); got.AudioLanguage != "eng" || got.SubtitleMode != "off" {
		t.Fatalf("global prefs = %#v", got)
	}
	if got := resolver.Resolve("user1", "", ""); got.AudioLanguage != "spa" || got.SubtitleLanguage != "spa" || got.SubtitleMode != "on" {
		t.Fatalf("user prefs = %#v", got)
	}
	if got := resolver.Resolve("user1", "tv", ""); got.AudioLanguage != "jpn" || got.SubtitleLanguage != "spa" {
		t.Fatalf("client prefs = %#v", got)
	}
	got := resolver.Resolve("user1", "tv", "tmdb:movie:1")
	if got.AudioLanguage != "jpn" || got.SubtitleLanguage != "fra" || got.SubtitleMode != "forced-only" {
		t.Fatalf("content prefs = %#v", got)
	}
}

func TestResolverFallsBackOnErrors(t *testing.T) {
	resolver := &Resolver{
		Config:             fakeConfig{err: errors.New("boom")},
		ContentPreferences: fakeContentPrefs{err: errors.New("boom")},
	}
	got := resolver.Resolve("user1", "", "other")
	if got.AudioLanguage != "eng" {
		t.Fatalf("expected built-in default audio language, got %#v", got)
	}
	var nilResolver *Resolver
	if got := nilResolver.Resolve("user1", "tv", "x"); got.AudioLanguage != "eng" {
		t.Fatalf("nil resolver prefs = %#v", got)
	}
}

func TestSelectForeignAudioModeUsesForcedForOwnLanguageAudio(t *testing.T) {
	audio, subs := sampleStreams()
	sel := Select(audio, subs, Preferences{AudioLanguage: "eng", SubtitleLanguage: "eng", SubtitleMode: "foreign-audio"})

	if sel.SubtitleMode != "forced-only" {
		t.Fatalf("SubtitleMode = %q, want forced-only for English audio", sel.SubtitleMode)
	}
	if sel.Subtitle == nil || sel.Subtitle.StreamIndex != 6 {
		t.Fatalf("subtitle = %#v, want forced stream 6", sel.Subtitle)
	}
}

func TestSelectForeignAudioModeUsesFullSubtitlesForForeignAudio(t *testing.T) {
	audio, subs := sampleStreams()
	sel := Select(audio, subs, Preferences{AudioLanguage: "jpn", SubtitleLanguage: "eng", SubtitleMode: "foreign-audio"})

	if sel.Audio == nil || sel.Audio.StreamIndex != 3 {
		t.Fatalf("audio = %#v, want jpn stream 3", sel.Audio)
	}
	if sel.SubtitleMode != "on" {
		t.Fatalf("SubtitleMode = %q, want on for Japanese audio", sel.SubtitleMode)
	}
	if sel.SubtitleModePreference != "foreign-audio" {
		t.Fatalf("SubtitleModePreference = %q, want foreign-audio", sel.SubtitleModePreference)
	}
	if sel.Subtitle == nil || sel.Subtitle.Forced {
		t.Fatalf("subtitle = %#v, want a full English track", sel.Subtitle)
	}
}

func TestSelectForeignAudioModeFallsBackToDefaultAudioTrack(t *testing.T) {
	// No English audio: the player starts the first (Japanese) track, so
	// English subtitles come on even though the preference is English.
	audio := []models.AudioStreamInfo{{Index: 1, Codec: "aac", Language: "jpn"}}
	subs := []models.SubtitleStreamInfo{
		{Index: 2, Codec: "subrip", Language: "eng", Title: "Signs", IsForced: true},
		{Index: 3, Codec: "subrip", Language: "eng"},
	}
	sel := Select(audio, subs, Preferences{AudioLanguage: "eng", SubtitleLanguage: "eng", SubtitleMode: "foreign-audio"})

	if sel.Audio != nil {
		t.Fatalf("audio = %#v, want no preference match", sel.Audio)
	}
	if sel.SubtitleMode != "on" || sel.Subtitle == nil || sel.Subtitle.StreamIndex != 3 {
		t.Fatalf("mode %q subtitle %#v, want on with stream 3", sel.SubtitleMode, sel.Subtitle)
	}
}

func TestResolveForeignAudioMode(t *testing.T) {
	cases := []struct {
		audio, subtitle, preferredAudio, want string
	}{
		{"eng", "eng", "", "forced-only"},
		{"en", "eng", "", "forced-only"},
		{"English", "en", "", "forced-only"},
		{"jpn", "eng", "eng", "on"},
		{"fre", "en", "", "on"},
		{"", "eng", "", "forced-only"},
		{"und", "eng", "", "forced-only"},
		{"jpn", "", "eng", "on"},
		{"jpn", "", "", "forced-only"},
	}
	for _, c := range cases {
		if got := ResolveForeignAudioMode(c.audio, c.subtitle, c.preferredAudio); got != c.want {
			t.Errorf("ResolveForeignAudioMode(%q, %q, %q) = %q, want %q", c.audio, c.subtitle, c.preferredAudio, got, c.want)
		}
	}
}

func TestNormalizeSubtitleModeKeepsForeignAudio(t *testing.T) {
	for _, mode := range []string{"foreign-audio", "smart", "foreign"} {
		if got := NormalizeSubtitleMode(mode); got != "foreign-audio" {
			t.Errorf("NormalizeSubtitleMode(%q) = %q, want foreign-audio", mode, got)
		}
	}
}
