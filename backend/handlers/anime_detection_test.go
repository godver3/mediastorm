package handlers

import (
	"strings"
	"testing"

	"novastream/models"
)

func TestAnimeDecisionReasons(t *testing.T) {
	cases := []struct {
		title  models.Title
		anime  bool
		reason string
	}{
		{models.Title{Genres: []string{"Anime"}}, true, "genre Anime"},
		{models.Title{Genres: []string{"Animation"}, Language: "ja"}, true, "original language ja"},
		{models.Title{Genres: []string{"Animation"}, OriginalName: "進撃の巨人"}, true, "original title"},
		{models.Title{Genres: []string{"Animation"}, AirsTimezone: "Asia/Tokyo"}, true, "airs in Asia/Tokyo"},
		{models.Title{Genres: []string{"Animation"}, Language: "en"}, false, "without East Asian origin"},
		{models.Title{Genres: []string{"Drama"}, Language: "ja"}, false, "no Anime or Animation genre"},
	}
	for _, c := range cases {
		anime, reason := animeDecision(&c.title)
		if anime != c.anime || !strings.Contains(reason, c.reason) {
			t.Errorf("%+v: anime=%v %q, want %v containing %q", c.title, anime, reason, c.anime, c.reason)
		}
	}
	if anime, _ := animeDecision(nil); anime {
		t.Error("no metadata is not anime")
	}
}
