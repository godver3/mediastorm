package localmedia

import (
	"errors"
	"testing"

	"novastream/models"
)

func TestWithTitleIDExternalIDs(t *testing.T) {
	cases := map[string]models.LocalMediaMatchQuery{
		"tmdb:tv:1399":      {TMDBID: "1399"},
		"tvdb:series:81189": {TVDBID: "81189"},
		"imdb:tt0944947":    {IMDBID: "tt0944947"},
		"1399":              {},
	}
	for id, want := range cases {
		got := WithTitleIDExternalIDs(models.LocalMediaMatchQuery{TitleID: id})
		if got.TMDBID != want.TMDBID || got.TVDBID != want.TVDBID || got.IMDBID != want.IMDBID {
			t.Errorf("%s: %+v", id, got)
		}
	}
	if got := WithTitleIDExternalIDs(models.LocalMediaMatchQuery{TitleID: "tmdb:movie:1", TMDBID: "9"}); got.TMDBID != "9" {
		t.Errorf("explicit ID overwritten: %+v", got)
	}
}

func TestPickLibraryCopy(t *testing.T) {
	copies := []LibraryCopy{
		{Item: models.LocalMediaItem{ID: "small", SizeBytes: 1}},
		{Item: models.LocalMediaItem{ID: "denied", SizeBytes: 100}},
		{Item: models.LocalMediaItem{ID: "big", SizeBytes: 50}},
	}
	got, err := PickLibraryCopy(copies, func(c LibraryCopy) (bool, error) { return c.Item.ID != "denied", nil })
	if err != nil || got == nil || got.Item.ID != "big" {
		t.Fatalf("pick %+v, %v", got, err)
	}
	if got, _ := PickLibraryCopy(copies, func(LibraryCopy) (bool, error) { return false, nil }); got != nil {
		t.Fatalf("none allowed: %+v", got)
	}
	if _, err := PickLibraryCopy(copies, func(LibraryCopy) (bool, error) { return false, errors.New("x") }); err == nil {
		t.Fatal("error dropped")
	}
}
