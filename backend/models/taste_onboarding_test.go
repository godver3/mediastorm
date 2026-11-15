package models

import (
	"reflect"
	"testing"
	"time"
)

func TestTasteInterestEditsPreserveIdentityDatesAndRemoveOmittedTitles(t *testing.T) {
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	now := old.AddDate(6, 0, 0)
	previous := []TasteInterest{{Title: Title{ID: "tmdb:movie:7", TMDBID: 7, MediaType: "movie", Name: "Old name"}, Aliases: []string{"movie:tmdb:7", "movie:imdb:tt7"}, AddedAt: old}, {Title: Title{ID: "removed", MediaType: "movie"}, Aliases: []string{"movie:id:removed"}, AddedAt: old}}
	next := []TasteInterest{{Title: Title{ID: "tt7", IMDBID: "tt7", MediaType: "movie", Name: "Current name"}, Aliases: []string{"movie:imdb:tt7"}, AddedAt: now}}
	before := append([]string(nil), previous[0].Aliases...)
	got := MergeTasteInterests(previous, next)
	if len(got) != 1 || got[0].Title.TMDBID != 7 || got[0].Title.Name != "Current name" || !got[0].AddedAt.Equal(old) || len(got[0].Aliases) != 2 {
		t.Fatal("edit lost identity/date or restored an omitted title")
	}
	if !reflect.DeepEqual(previous[0].Aliases, before) {
		t.Fatal("merge mutated stored aliases")
	}
	if len(MergeTasteInterests(previous, nil)) != 0 {
		t.Fatal("empty edit failed to remove interests")
	}
}
func TestTasteInterestBridgeConvergesAliasesWithoutCrossingMediaKind(t *testing.T) {
	now := time.Now()
	items := []TasteInterest{
		{Title: Title{ID: "a", MediaType: "movie"}, Aliases: []string{"movie:id:a"}, AddedAt: now},
		{Title: Title{ID: "b", MediaType: "movie"}, Aliases: []string{"movie:id:b"}, AddedAt: now},
		{Title: Title{ID: "bridge", MediaType: "movie"}, Aliases: []string{"movie:id:a", "movie:id:b", "movie:tmdb:7"}, AddedAt: now},
		{Title: Title{ID: "series", MediaType: "series"}, Aliases: []string{"series:tmdb:7"}, AddedAt: now},
	}
	got := MergeTasteInterests(nil, items)
	if len(got) != 2 || got[0].Title.MediaType != "movie" || got[1].Title.MediaType != "series" || len(got[0].Aliases) != 3 {
		t.Fatal("bridged aliases did not converge or crossed kinds")
	}
}
