package config

import "testing"

func TestEnsureDefaultHomeShelvesAddsRecordingsBelowLiveFavorites(t *testing.T) {
	shelves := []ShelfConfig{
		{ID: "continue-watching", Order: 0},
		{ID: "live-favorites", Order: 1},
		{ID: "watchlist", Order: 2},
	}
	next, changed := EnsureDefaultHomeShelves(shelves)
	if !changed {
		t.Fatal("expected change")
	}
	order := map[string]int{}
	for _, s := range next {
		order[s.ID] = s.Order
		if s.ID == "recordings" && s.Enabled {
			t.Fatal("recordings shelf should default off")
		}
	}
	rec, ok := order["recordings"]
	if !ok {
		t.Fatal("recordings shelf missing")
	}
	if rec <= order["live-favorites"] {
		t.Fatalf("recordings order %d, live-favorites %d", rec, order["live-favorites"])
	}
}
