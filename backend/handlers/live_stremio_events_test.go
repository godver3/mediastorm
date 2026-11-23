package handlers

import (
	"testing"
	"time"
)

func TestParseStremioEventTime(t *testing.T) {
	got, ok := parseStremioEventTime("LIVE", "Atalanta vs Inter Milan\n22 Nov 2026 · 19:45 UTC")
	if !ok || !got.Equal(time.Date(2026, 11, 22, 19, 45, 0, 0, time.UTC)) {
		t.Fatalf("got %v %v", got, ok)
	}
	if _, ok := parseStremioEventTime("LIVE", "Live IPTV Recording\nLIVE NOW"); ok {
		t.Fatal("expected no time")
	}
}

func TestStremioEventDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"streamed:american-football-bills-dolphins-1": 3*time.Hour + 30*time.Minute,
		"streamed:football-atalanta-inter-1":          2 * time.Hour,
		"streamed:basketball-hawks-pistons-1":         2*time.Hour + 30*time.Minute,
		"streamed:something-else":                     stremioEventDefaultDuration,
	}
	for id, want := range cases {
		if got := stremioEventDuration(stremioMeta{ID: id}); got != want {
			t.Errorf("%s: got %v want %v", id, got, want)
		}
	}
}

func TestStremioEventIndexPrograms(t *testing.T) {
	now := time.Now().UTC()
	var idx stremioEventIndex
	upcoming := stremioMeta{ID: "streamed:football-a-b-1", Name: "A vs B", Description: "A vs B\nSerie A"}
	idx.record(upcoming, now.Add(time.Hour), false, now)
	// Live match well past its estimated end keeps running while listed LIVE.
	overtime := stremioMeta{ID: "streamed:football-c-d-1", Name: "C vs D"}
	idx.record(overtime, now.Add(-5*time.Hour), true, now)

	got := idx.programs([]string{"STREAMED:football-a-b-1", "streamed:football-c-d-1", "unknown"}, now, now.Add(4*time.Hour))
	a := got["STREAMED:football-a-b-1"]
	if len(a) != 1 || a[0].Title != "A vs B" || a[0].Description != "Serie A" || a[0].Stop.Sub(a[0].Start) != 2*time.Hour {
		t.Fatalf("upcoming: %+v", a)
	}
	c := got["streamed:football-c-d-1"]
	if len(c) != 1 || !c[0].Stop.After(now) {
		t.Fatalf("overtime live event should still be current: %+v", c)
	}
	if _, ok := got["unknown"]; ok {
		t.Fatal("unknown id should be absent")
	}
	// Outside the window.
	if got := idx.programs([]string{"streamed:football-a-b-1"}, now.Add(5*time.Hour), now.Add(6*time.Hour)); len(got) != 0 {
		t.Fatalf("expected none, got %+v", got)
	}
}

func TestStremioMetaEventTimeReleased(t *testing.T) {
	got, ok := stremioMetaEventTime(stremioMeta{Type: "tv", Released: "2026-11-23T01:30:00.000Z"})
	if !ok || !got.Equal(time.Date(2026, 11, 23, 1, 30, 0, 0, time.UTC)) {
		t.Fatalf("got %v %v", got, ok)
	}
	if _, ok := stremioMetaEventTime(stremioMeta{Type: "movie", Released: "2026-11-23T01:30:00.000Z"}); ok {
		t.Fatal("movie release dates are not events")
	}
	if _, ok := stremioMetaEventTime(stremioMeta{Type: "tv", Released: "2026-11-23T00:00:00.000Z"}); ok {
		t.Fatal("midnight dates are treated as date-only")
	}
}
