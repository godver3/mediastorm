package jellyfinbridge

import (
	"context"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
)

// Playing a preset version drops what Auto cached for that device (mediastorm
// now ranks the title there with that preset), and nothing of other devices,
// other profiles or preset versions.
func TestForgetAutoRanking(t *testing.T) {
	s := &Server{releases: newReleaseCache(), autoPicks: newAutoPicks(), plays: &playSessions{byID: map[string]*playSession{
		// A prewarmed Auto session of the device was ranked with the old
		// preset; a preset session, another device's and a playing one stay.
		"auto":    {ID: "auto", ProfileID: "p1", DeviceID: "d1", ItemID: "ep2"},
		"preset":  {ID: "preset", ProfileID: "p1", DeviceID: "d1", ItemID: "ep2", PresetID: "a"},
		"other":   {ID: "other", ProfileID: "p1", DeviceID: "d2", ItemID: "ep2"},
		"playing": {ID: "playing", ProfileID: "p1", DeviceID: "d1", ItemID: "ep1", state: &playState{At: time.Now()}},
	}}}
	s.presetChoices.items = map[string]itemPresets{"p1|d1|ep2": {}, "p1|d2|ep2": {}, "p2|d1|ep2": {}}
	for _, key := range []string{"p1|d1|ep2|", "p1|d2|ep3|", "p1|d1|ep2|preset-a", "p2|d1|ep2|"} {
		s.autoPicks.byKey[key] = &autoPick{}
		s.releases.lists[key] = &releaseList{ready: true}
	}

	s.forgetAutoRanking("p1|d1", "", time.Time{}, true)

	if _, ok := s.presetChoices.items["p1|d1|ep2"]; ok {
		t.Error("Auto's label for the device is still cached")
	}
	if _, ok := s.presetChoices.items["p1|d2|ep2"]; !ok {
		t.Error("another device's label was dropped")
	}
	if _, ok := s.presetChoices.items["p2|d1|ep2"]; !ok {
		t.Error("another profile's label was dropped")
	}
	// Every pick of the device goes, so playing a preset version again
	// resolves anew; Auto's release list is searched again (and served until
	// then), a preset version's stays as it is.
	for key, want := range map[string][2]bool{
		"p1|d1|ep2|":         {false, true},
		"p1|d2|ep3|":         {true, false},
		"p1|d1|ep2|preset-a": {false, false},
		"p2|d1|ep2|":         {true, false},
	} {
		if _, ok := s.autoPicks.byKey[key]; ok != want[0] {
			t.Errorf("auto pick %q kept = %v, want %v", key, ok, want[0])
		}
		if list := s.releases.lists[key]; list == nil || list.outdated != want[1] {
			t.Errorf("release list %q: %+v, want outdated %v", key, list, want[1])
		}
	}
	for id, want := range map[string]bool{"auto": false, "preset": true, "other": true, "playing": true} {
		if _, ok := s.plays.byID[id]; ok != want {
			t.Errorf("play session %q kept = %v, want %v", id, ok, want)
		}
	}
}

// The preset version the item gets without a remembered preset is marked.
func TestPresetVersionLabelMarksDefault(t *testing.T) {
	german := presetChoice{ID: "g1", Name: "German HD"}
	simple := presetChoice{ID: "simple", Name: "Simple ranking"}
	cases := []struct {
		c    presetChoice
		def  presetSelection
		want string
	}{
		{german, presetSelection{PresetID: "g1", Source: "profile"}, "Preset · German HD (default)"},
		{german, presetSelection{PresetID: "other", Source: "profile"}, "Preset · German HD"},
		{simple, presetSelection{Source: "none"}, "Preset · Simple ranking (default)"},
		{german, presetSelection{}, "Preset · German HD"}, // default unknown
		{simple, presetSelection{}, "Preset · Simple ranking"},
		// The library copy is no ranking: named as it is.
		{presetChoice{ID: libraryPresetID, Name: "Library copy"}, presetSelection{PresetID: libraryPresetID, Source: "library"}, "Library copy (default)"},
	}
	for _, tc := range cases {
		if got := presetVersionLabel(tc.c, tc.def); got != tc.want {
			t.Errorf("%+v with default %+v: %q, want %q", tc.c, tc.def, got, tc.want)
		}
	}
}

// tvdbSeriesMetadata knows a series by its TVDB ID and reports its TMDB ID,
// as the metadata service does.
type tvdbSeriesMetadata struct{ fakeMetadata }

func (tvdbSeriesMetadata) SeriesInfo(_ context.Context, q models.SeriesDetailsQuery) (*models.Title, error) {
	return &models.Title{ID: q.TitleID, Name: "The End of the F***ing World", MediaType: "series", TMDBID: 74577, TVDBID: 336522, IMDBID: "tt6257970"}, nil
}

// The bridge sends mediastorm the canonical title ID (TMDB for a series it
// knows by its TVDB ID), the form the prequeue stores remembered presets
// under: the preset list, searches and playback then name the same title.
func TestPrequeueRequestUsesCanonicalTitleID(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	h.srv.deps.Metadata = tvdbSeriesMetadata{}
	p := &principal{}
	p.Profile.ID = "u1"
	req, err := h.srv.prequeueRequest(context.Background(), p, itemRef{Kind: refEpisode, Source: "tvdb:series:336522:s1e4"})
	if err != nil {
		t.Fatal(err)
	}
	if req.TitleID != "tmdb:tv:74577" || req.TmdbID != "74577" || req.TvdbID != "336522" {
		t.Errorf("request = titleId %q tmdb %q tvdb %q", req.TitleID, req.TmdbID, req.TvdbID)
	}
}

// Picking a preset version forgets only that title's items on the device:
// mediastorm remembers the preset per title.
func TestForgetAutoRankingOfTitle(t *testing.T) {
	s := &Server{releases: newReleaseCache(), autoPicks: newAutoPicks(), ids: newIDStore("")}
	t.Cleanup(s.ids.close)
	ep := func(source string) string {
		return s.ids.put(refEpisode, source, refContext{Season: 1, Episode: 1})
	}
	same, sibling, other := ep("tmdb:tv:1:s1e1"), ep("tmdb:tv:1:s1e2"), ep("tmdb:tv:2:s1e1")
	for _, item := range []string{same, sibling, other} {
		s.autoPicks.byKey["p1|d1|"+item+"|"] = &autoPick{}
	}
	ref, _ := s.ids.get(same)
	title := refTitle(ref)
	if title == "" {
		t.Fatal("no title for the episode")
	}
	s.forgetAutoRanking("p1|d1", title, time.Time{}, true)
	for item, want := range map[string]bool{same: false, sibling: false, other: true} {
		if _, ok := s.autoPicks.byKey["p1|d1|"+item+"|"]; ok != want {
			t.Errorf("pick of %s kept = %v, want %v", item, ok, want)
		}
	}
}

// The later forget passes after a pick leave Auto sessions started after
// the pick alone: only sessions resolved before it ranked with the old
// preset.
func TestForgetAutoRankingKeepsSessionsAfterPick(t *testing.T) {
	pickedAt := time.Now()
	s := &Server{releases: newReleaseCache(), autoPicks: newAutoPicks(), plays: &playSessions{byID: map[string]*playSession{
		"before": {ID: "before", ProfileID: "p1", DeviceID: "d1", ItemID: "ep2", created: pickedAt.Add(-time.Second)},
		"after":  {ID: "after", ProfileID: "p1", DeviceID: "d1", ItemID: "ep2", created: pickedAt.Add(time.Second)},
	}}}
	s.forgetAutoRanking("p1|d1", "", pickedAt, false)
	for id, want := range map[string]bool{"before": false, "after": true} {
		if _, ok := s.plays.byID[id]; ok != want {
			t.Errorf("play session %q kept = %v, want %v", id, ok, want)
		}
	}
}

// Specials belong to their series like its other episodes: a preset picked
// on one forgets the series' Auto caches and the other way round.
func TestRefTitleIncludesSpecials(t *testing.T) {
	special := refTitle(itemRef{Kind: refEpisode, Source: "tmdb:tv:1:s0e1"})
	regular := refTitle(itemRef{Kind: refEpisode, Source: "tmdb:tv:1:s1e2", Ctx: refContext{Season: 1, Episode: 2}})
	if special == "" || special != regular {
		t.Errorf("special %q, regular episode %q: want the same series", special, regular)
	}
}

type absoluteSeriesMetadata struct{ tvdbSeriesMetadata }

func (absoluteSeriesMetadata) SeriesDetails(_ context.Context, _ models.SeriesDetailsQuery) (*models.SeriesDetails, error) {
	season := func(number, first, count int) models.SeriesSeason {
		s := models.SeriesSeason{Number: number}
		for i := 1; i <= count; i++ {
			s.Episodes = append(s.Episodes, models.SeriesEpisode{SeasonNumber: number, EpisodeNumber: i, AbsoluteEpisodeNumber: first + i - 1})
		}
		return s
	}
	return &models.SeriesDetails{Seasons: []models.SeriesSeason{season(1, 1, 12), season(2, 13, 3)}}, nil
}

// The bridge sends an episode's absolute number, as the other clients do: a
// library of files named by it (anime) has the episode under that number.
func TestPrequeueRequestSendsAbsoluteEpisodeNumber(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	h.srv.deps.Metadata = absoluteSeriesMetadata{}
	p := &principal{}
	p.Profile.ID = "u1"
	req, err := h.srv.prequeueRequest(context.Background(), p, itemRef{Kind: refEpisode, Source: "tvdb:series:336522:s2e2", Ctx: refContext{Season: 2, Episode: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if req.SeasonNumber != 2 || req.EpisodeNumber != 2 || req.AbsoluteEpisodeNumber != 14 {
		t.Errorf("request = S%dE%d absolute %d, want S2E2 absolute 14", req.SeasonNumber, req.EpisodeNumber, req.AbsoluteEpisodeNumber)
	}
}

// An episode numbered in another ordering than the library's files gets no
// library copy (and no absolute number from the aired order).
func TestPrequeueRequestSkipsLibraryForOtherOrderings(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	h.srv.deps.Metadata = absoluteSeriesMetadata{}
	p := &principal{}
	p.Profile.ID = "u1"
	req, err := h.srv.prequeueRequest(context.Background(), p, itemRef{Kind: refEpisode, Source: "tvdb:series:336522:s2e2", Ctx: refContext{Season: 2, Episode: 2, SeasonOrdering: "dvd"}})
	if err != nil {
		t.Fatal(err)
	}
	if !req.NoLibraryCopy || req.AbsoluteEpisodeNumber != 0 {
		t.Errorf("request = noLibraryCopy %v absolute %d, want true 0", req.NoLibraryCopy, req.AbsoluteEpisodeNumber)
	}
}
