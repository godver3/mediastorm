package indexer

import (
	"context"
	"testing"

	"novastream/config"
	"novastream/internal/arr"
	"novastream/models"
)

type fakeTitlePresets map[string]string // user|client|title -> preset

func (f fakeTitlePresets) TitlePreset(_ context.Context, userID, clientID, contentID string) (string, error) {
	return f[userID+"|"+clientID+"|"+contentID], nil
}

// The preset last picked for a title on a device ranks the title's later
// searches on that device, ahead of the device's, the profile's and the
// server's choice, but behind a preset the search itself asks for. Other
// devices keep their own.
func TestSelectQualityPresetRemembersTitle(t *testing.T) {
	svc, presets, presetID := newPresetTestService(t, nil, nil)
	settings, _ := svc.cfg.Load()
	picked, err := presets.ImportJSON(context.Background(), []byte(germanPresetJSON), arr.AppSonarr, "Picked")
	if err != nil {
		t.Fatal(err)
	}
	other := picked.ID
	svc.userSettings = fakeUserSettings{settings: map[string]*models.UserSettings{
		"u1": {QualityPresets: &models.QualityPresetSelection{SeriesPreset: &presetID}},
		"u2": {QualityPresets: &models.QualityPresetSelection{PickerPresets: &[]string{presetID}}},
	}}
	svc.clientSettings = fakeClientSettings{settings: map[string]*models.ClientFilterSettings{
		"tv": {QualityPresetSeries: &presetID},
	}}
	svc.SetTitlePresetProvider(fakeTitlePresets{
		"u1|tv|tmdb:tv:1": other,
		"u1||tmdb:tv:2":   "simple",
		"u1||tmdb:tv:3":   "deleted-id",
		"u2||tmdb:tv:1":   other,
	})
	cases := []struct {
		name string
		opts SearchOptions
		want QualityPresetSelection
	}{
		{"title beats device and profile", SearchOptions{MediaType: "series", UserID: "u1", ClientID: "tv", TitleID: "tmdb:tv:1"}, QualityPresetSelection{PresetID: other, Source: "title"}},
		{"simple ranking remembered", SearchOptions{MediaType: "series", UserID: "u1", TitleID: "tmdb:tv:2"}, QualityPresetSelection{Source: "title"}},
		{"request beats title", SearchOptions{MediaType: "series", UserID: "u1", ClientID: "tv", TitleID: "tmdb:tv:1", QualityPreset: presetID}, QualityPresetSelection{PresetID: presetID, Source: "request"}},
		{"other device of the profile", SearchOptions{MediaType: "series", UserID: "u1", ClientID: "phone", TitleID: "tmdb:tv:1"}, QualityPresetSelection{PresetID: presetID, Source: "profile"}},
		{"no device named", SearchOptions{MediaType: "series", UserID: "u1", TitleID: "tmdb:tv:1"}, QualityPresetSelection{PresetID: presetID, Source: "profile"}},
		{"other title", SearchOptions{MediaType: "series", UserID: "u1", TitleID: "tmdb:tv:9"}, QualityPresetSelection{PresetID: presetID, Source: "profile"}},
		{"deleted preset skipped", SearchOptions{MediaType: "series", UserID: "u1", TitleID: "tmdb:tv:3"}, QualityPresetSelection{PresetID: presetID, Source: "profile"}},
		{"not in the profile's picker list", SearchOptions{MediaType: "series", UserID: "u2", TitleID: "tmdb:tv:1"}, QualityPresetSelection{PresetID: presetID, Source: "global"}},
		{"no profile", SearchOptions{MediaType: "series", TitleID: "tmdb:tv:1"}, QualityPresetSelection{PresetID: presetID, Source: "global"}},
		// A search naming the title another way finds it by its canonical ID.
		{"canonical title ID", SearchOptions{MediaType: "series", UserID: "u1", ClientID: "tv", TitleID: "tvdb:series:5", PresetTitleID: "tmdb:tv:1"}, QualityPresetSelection{PresetID: other, Source: "title"}},
	}
	for _, tc := range cases {
		if got := svc.SelectQualityPreset(tc.opts, settings); got != tc.want {
			t.Errorf("%s: got %+v want %+v", tc.name, got, tc.want)
		}
	}

	// A remembered Sonarr preset does not rank a movie: the next level
	// decides instead of the simple ranking.
	svc.SetTitlePresetProvider(fakeTitlePresets{"u1|tv|tmdb:tv:1": other, "u1||tmdb:movie:4": other})
	if got := svc.SelectQualityPreset(SearchOptions{MediaType: "movie", UserID: "u1", TitleID: "tmdb:movie:4"}, settings); got.Source == "title" {
		t.Errorf("a series preset was used for a movie: %+v", got)
	}

	// A preset the server no longer lets anyone pick is not remembered.
	settings.QualityPresets.AllowRequestOverride = false
	settings.QualityPresets.PickerPresets = []string{config.QualityPresetSimple}
	got := svc.SelectQualityPreset(SearchOptions{MediaType: "series", UserID: "u1", ClientID: "tv", TitleID: "tmdb:tv:1"}, settings)
	if want := (QualityPresetSelection{PresetID: presetID, Source: "device"}); got != want {
		t.Errorf("preset no longer offered: got %+v want %+v", got, want)
	}
}
