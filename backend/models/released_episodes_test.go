package models

import "testing"

func TestReleasedEpisodeSetContains(t *testing.T) {
	set := NewReleasedEpisodeSet([]ReleasedEpisode{
		{Season: 0, Episode: 1},
		{Season: 22, Episode: 30, Absolute: 1155},
		{Season: 23, Episode: 8, Absolute: 1163},
		{Season: 23, Episode: 8, Absolute: 1163},
	})
	if set.Len() != 2 {
		t.Fatalf("Len = %d, want 2 (specials and duplicates dropped)", set.Len())
	}
	cases := []struct {
		season, episode int
		want            bool
	}{
		{23, 8, true},
		{23, 1163, true},  // legacy absolute numbering maps within the season
		{24, 1155, false}, // absolute remap may not move a row to an earlier season
		{10, 13, false},   // not in the catalog
		{0, 1, false},     // specials never count
	}
	for _, c := range cases {
		if got := set.Contains(c.season, c.episode); got != c.want {
			t.Errorf("Contains(%d, %d) = %v, want %v", c.season, c.episode, got, c.want)
		}
	}
	var empty *ReleasedEpisodeSet
	if empty.Len() != 0 || empty.Contains(1, 1) {
		t.Fatal("nil set should be empty")
	}
}
