package handlers

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"novastream/config"
	"novastream/models"
)

func TestJSONModelPathOverridden(t *testing.T) {
	settings := models.UserSettings{
		Playback: models.PlaybackSettings{
			PreferredPlayer:       "vlc",
			AutoPlayTrailersTV:    models.BoolPtr(false),
			RewindOnPlaybackStart: models.IntPtr(0),
			AllowedTrackLanguages: models.StringSlicePtr([]string{}),
		},
		Filtering: models.FilterSettings{AdaptivePlaybackEnabled: models.BoolPtr(false)},
		Display: models.DisplaySettings{
			EnableAnimations: models.BoolPtr(false),
		},
	}
	for _, path := range []string{
		"playback.preferredPlayer",
		"playback.autoPlayTrailersTV",
		"playback.rewindOnPlaybackStart",
		"playback.allowedTrackLanguages",
		"display.enableAnimations",
		"filtering.adaptivePlaybackEnabled",
	} {
		if !jsonModelPathOverridden(settings, path) {
			t.Fatalf("%s should be reported as overridden", path)
		}
	}
	for _, path := range []string{"playback.subtitleSize", "display.hideDetailsPoster", "display.showDetailsRuntime", "ranking.newestReleaseFirst"} {
		if jsonModelPathOverridden(settings, path) {
			t.Fatalf("%s should be reported as inherited", path)
		}
	}
}

func TestPatchJSONObjectPreservesSiblings(t *testing.T) {
	raw, err := patchJSONObject(
		[]byte(`{"playback":{"preferredPlayer":"native","subtitleSize":1.25}}`),
		"playback.preferredPlayer",
		json.RawMessage(`"vlc"`),
		false,
	)
	if err != nil {
		t.Fatalf("patchJSONObject: %v", err)
	}
	var got map[string]map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["playback"]["preferredPlayer"] != "vlc" || got["playback"]["subtitleSize"] != 1.25 {
		t.Fatalf("unexpected patched document: %#v", got)
	}
}

func TestPatchJSONObjectResetPreservesSiblings(t *testing.T) {
	raw, err := patchJSONObject(
		[]byte(`{"playback":{"preferredPlayer":"native","subtitleSize":1.25}}`),
		"playback.preferredPlayer",
		nil,
		true,
	)
	if err != nil {
		t.Fatalf("patchJSONObject: %v", err)
	}
	var got map[string]map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, exists := got["playback"]["preferredPlayer"]; exists {
		t.Fatalf("reset setting was retained: %#v", got)
	}
	if got["playback"]["subtitleSize"] != 1.25 {
		t.Fatalf("sibling was not preserved: %#v", got)
	}
}

func TestValidateUserEditableSettingValueRejectsUnsupportedMultiOption(t *testing.T) {
	err := validateUserEditableSettingValue("playback.allowedTrackLanguages", json.RawMessage(`["not-a-language"]`))
	if err == nil {
		t.Fatal("expected unsupported option to be rejected")
	}
}

func TestClientSettingPathRejectsProfileOnlySettings(t *testing.T) {
	if _, ok := clientSettingPath("homeShelves.itemCap"); ok {
		t.Fatal("home shelf settings must not be accepted as device overrides")
	}
	if got, ok := clientSettingPath("homeShelves.homeShelfFocusModel"); !ok || got != "homeShelfFocusModel" {
		t.Fatalf("home shelf focus clientSettingPath() = %q, %v", got, ok)
	}
	if got, ok := clientSettingPath("display.enableAnimations"); !ok || got != "enableAnimations" {
		t.Fatalf("clientSettingPath() = %q, %v", got, ok)
	}
	if got, ok := clientSettingPath("display.showSeriesBackdropForMissingEpisodeArt"); !ok || got != "showSeriesBackdropForMissingEpisodeArt" {
		t.Fatalf("clientSettingPath() = %q, %v", got, ok)
	}
	if got, ok := clientSettingPath("display.disableTvHomeCardDimming"); !ok || got != "disableTvHomeCardDimming" {
		t.Fatalf("clientSettingPath() = %q, %v", got, ok)
	}
	if got, ok := clientSettingPath("filtering.debrid.hdrDvPolicy"); !ok || got != "debrid.hdrDvPolicy" {
		t.Fatalf("nested clientSettingPath() = %q, %v", got, ok)
	}
	if got, ok := clientSettingPath("animeFiltering.animePreferredLanguage"); !ok || got != "animePreferredLanguage" {
		t.Fatalf("anime clientSettingPath() = %q, %v", got, ok)
	}
	if got, ok := clientSettingPath(experimentalNativeTrailerPlayerTVPath); !ok || got != "experimentalNativeTrailerPlayerTV" {
		t.Fatalf("experimental trailer clientSettingPath() = %q, %v", got, ok)
	}
}

func TestExperimentalNativeTrailerPlayerIsDeviceOnly(t *testing.T) {
	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	body := []byte(`{"path":"playback.experimentalNativeTrailerPlayerTV","value":true}`)

	patch, err := decodeClientFrontendSettingPatch(json.NewDecoder(bytes.NewReader(body)), manager)
	if err != nil {
		t.Fatalf("device experiment patch rejected: %v", err)
	}
	if patch.Path != experimentalNativeTrailerPlayerTVPath {
		t.Fatalf("patch path = %q", patch.Path)
	}

	if _, err := decodeFrontendSettingPatch(json.NewDecoder(bytes.NewReader(body)), manager); err == nil {
		t.Fatal("profile patch unexpectedly accepted the device-only experiment")
	}

	invalid := []byte(`{"path":"playback.experimentalNativeTrailerPlayerTV","value":"yes"}`)
	if _, err := decodeClientFrontendSettingPatch(json.NewDecoder(bytes.NewReader(invalid)), manager); err == nil {
		t.Fatal("non-boolean device experiment value was accepted")
	}
}
