package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/services/playback"
	"novastream/services/qualityprofiles"
)

func newTitleMemory(t *testing.T) (*PrequeueHandler, *qualityprofiles.Service) {
	t.Helper()
	presets := qualityprofiles.NewService(qualityprofiles.NewMemoryStore(), nil, nil)
	pq := &PrequeueHandler{}
	pq.SetTitlePresetMemory(presets)
	return pq, presets
}

// A preset picked for playback is remembered per profile, device and title:
// another device of the profile keeps its own choice, and each device's
// prepared streams follow its own preset.
func TestTitlePresetMemory(t *testing.T) {
	pq, presets := newTitleMemory(t)
	ctx := context.Background()
	get := func(user, client, title string) string {
		t.Helper()
		id, err := presets.TitlePreset(ctx, user, client, title)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	pq.rememberTitlePreset(titlePresetPick{UserID: "u1", ClientID: "jellyfin-tv", TitleID: "tmdb:tv:1", MediaType: "series", PresetID: "uhd"})
	if got := get("u1", "jellyfin-tv", "TMDB:TV:1"); got != "uhd" {
		t.Fatalf("tv: %q", got)
	}
	if got := get("u1", "jellyfin-phone", "tmdb:tv:1"); got != "" {
		t.Errorf("another device got the TV's preset: %q", got)
	}
	if got := get("u2", "jellyfin-tv", "tmdb:tv:1"); got != "" {
		t.Errorf("another profile got it: %q", got)
	}

	pq.rememberTitlePreset(titlePresetPick{UserID: "u1", ClientID: "jellyfin-phone", TitleID: "tmdb:tv:1", MediaType: "series", PresetID: "hd"})
	if tv, phone := get("u1", "jellyfin-tv", "tmdb:tv:1"), get("u1", "jellyfin-phone", "tmdb:tv:1"); tv != "uhd" || phone != "hd" {
		t.Errorf("tv %q phone %q", tv, phone)
	}

	// Prepared streams are kept apart per device's preset.
	tvScope := pq.prequeueSettingsScopeKey("u1", "jellyfin-tv", "tmdb:tv:1")
	phoneScope := pq.prequeueSettingsScopeKey("u1", "jellyfin-phone", "tmdb:tv:1")
	plainScope := pq.prequeueSettingsScopeKey("u1", "jellyfin-laptop", "tmdb:tv:1")
	if tvScope == phoneScope || tvScope == plainScope || plainScope != playback.DefaultPrequeueSettingsScopeKey {
		t.Errorf("scopes tv=%q phone=%q laptop=%q", tvScope, phoneScope, plainScope)
	}

	// A movie is remembered on its own.
	pq.rememberTitlePreset(titlePresetPick{UserID: "u1", ClientID: "jellyfin-tv", TitleID: "tmdb:movie:2", MediaType: "movie", PresetID: "simple"})
	if got := get("u1", "jellyfin-tv", "tmdb:movie:2"); got != "simple" {
		t.Errorf("movie: %q", got)
	}
}

// Picking the preset a title gets anyway on that device clears its
// remembered preset there, so the title follows the defaults again.
func TestTitlePresetDefaultPickClears(t *testing.T) {
	pq, presets := newTitleMemory(t)
	var asked titlePresetPick
	pq.SetDefaultQualityPreset(func(_ context.Context, mediaType, userID, clientID, titleID, name string, year int, imdbID string) string {
		asked = titlePresetPick{UserID: userID, ClientID: clientID, TitleID: titleID, MediaType: mediaType, Name: name, ImdbID: imdbID, Year: year}
		return "german-hd"
	})
	get := func(client, title string) string {
		id, _ := presets.TitlePreset(context.Background(), "u1", client, title)
		return id
	}
	pick := titlePresetPick{UserID: "u1", ClientID: "tv", TitleID: "tmdb:tv:1", MediaType: "tv", Name: "Dark", ImdbID: "tt1", Year: 2017}

	pick.PresetID = "remux"
	pq.rememberTitlePreset(pick)
	if got := get("tv", "tmdb:tv:1"); got != "remux" {
		t.Fatalf("other preset not remembered: %q", got)
	}
	if want := (titlePresetPick{UserID: "u1", ClientID: "tv", TitleID: "tmdb:tv:1", MediaType: "series", Name: "Dark", ImdbID: "tt1", Year: 2017}); asked != want {
		t.Errorf("default asked with %+v, want %+v", asked, want)
	}

	pick.PresetID = "german-hd"
	pq.rememberTitlePreset(pick)
	if got := get("tv", "tmdb:tv:1"); got != "" {
		t.Fatalf("default pick kept the memory: %q", got)
	}

	// The simple ranking as default: "simple" is the same choice.
	pq.SetDefaultQualityPreset(func(context.Context, string, string, string, string, string, int, string) string { return "" })
	pick.TitleID, pick.PresetID = "tmdb:tv:2", "simple"
	pq.rememberTitlePreset(pick)
	if got := get("tv", "tmdb:tv:2"); got != "" {
		t.Fatalf("simple ranking as default remembered: %q", got)
	}
}

// The preset list reports the selection with and without the title's
// remembered preset.
func TestQualityPresetClientListDefault(t *testing.T) {
	h, _ := newPresetHandler(t)
	h.ProfileOwner = func(string) string { return "acc" }
	h.Select = func(mediaType, _, _, titleID string, _ bool) (string, string) {
		if titleID != "" {
			return "", "title"
		}
		return "", "profile"
	}
	rec := httptest.NewRecorder()
	h.ClientList(rec, presetRequest(http.MethodGet, "/api/quality-presets?userId=u1&mediaType=series&titleId=tmdb:tv:1", "", false, "acc", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("client list: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Movie, Series, MovieDefault, SeriesDefault struct{ Source string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Series.Source != "title" || got.SeriesDefault.Source != "profile" {
		t.Errorf("series: auto %q default %q", got.Series.Source, got.SeriesDefault.Source)
	}
	// The remembered preset belongs to the series only.
	if got.Movie.Source != "profile" || got.MovieDefault.Source != "profile" {
		t.Errorf("movie: auto %q default %q", got.Movie.Source, got.MovieDefault.Source)
	}
}

// A Jellyfin client's device is not registered with mediastorm: the preset
// list still asks for it, so Auto is named after the preset remembered on
// that device. A registered device of another profile stays hidden.
func TestQualityPresetClientListUnknownDevice(t *testing.T) {
	h, _ := newPresetHandler(t)
	h.ProfileOwner = func(string) string { return "acc" }
	h.Devices = fakeDevices{"their-tv": "theirs", "my-tv": "mine"}
	var gotClient string
	h.Select = func(_, _, clientID, _ string, _ bool) (string, string) { gotClient = clientID; return "", "none" }
	for client, want := range map[string]string{"jellyfin-abc": "jellyfin-abc", "my-tv": "my-tv", "their-tv": ""} {
		gotClient = "unset"
		rec := httptest.NewRecorder()
		h.ClientList(rec, presetRequest(http.MethodGet, "/api/quality-presets?userId=mine&clientId="+client, "", false, "acc", nil))
		if rec.Code != http.StatusOK || gotClient != want {
			t.Errorf("clientId %s: status %d, selection asked for %q, want %q", client, rec.Code, gotClient, want)
		}
	}
}

// Two quick picks remember concurrently: only the later one is written,
// whichever remember finishes last.
func TestTitlePresetLaterPickWins(t *testing.T) {
	pq, presets := newTitleMemory(t)
	ctx := context.Background()
	first := titlePresetPick{UserID: "u1", ClientID: "web", TitleID: "tmdb:tv:7", MediaType: "series", PresetID: "uhd"}
	second := first
	second.PresetID = "hd"
	first.seq = pq.titlePresetPicks.take(first)
	second.seq = pq.titlePresetPicks.take(second)

	pq.rememberTitlePreset(second)
	pq.rememberTitlePreset(first) // the earlier pick's remember ends last
	if got, err := presets.TitlePreset(ctx, "u1", "web", "tmdb:tv:7"); err != nil || got != "hd" {
		t.Fatalf("remembered %q (%v), want the later pick hd", got, err)
	}
}

// Finished picks leave nothing behind, including those not written (no
// profile, say).
func TestTitlePresetPicksReleased(t *testing.T) {
	pq, _ := newTitleMemory(t)
	written := titlePresetPick{UserID: "u1", ClientID: "web", TitleID: "tmdb:tv:7", MediaType: "series", PresetID: "uhd"}
	skipped := titlePresetPick{ClientID: "web", TitleID: "tmdb:tv:8", MediaType: "series", PresetID: "uhd"}
	written.seq = pq.titlePresetPicks.take(written)
	skipped.seq = pq.titlePresetPicks.take(skipped)
	pq.rememberTitlePreset(written)
	pq.rememberTitlePreset(skipped)
	if n := len(pq.titlePresetPicks.slots); n != 0 {
		t.Errorf("%d pick slot(s) left", n)
	}
}
