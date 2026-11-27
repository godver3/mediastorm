package kids

import (
	"testing"

	"novastream/models"
)

func TestFilterCalendarByRatings(t *testing.T) {
	items := []models.CalendarItem{
		{Title: "Edgerunners", MediaType: "series", Certification: "TV-MA"},
		{Title: "Bluey", MediaType: "series", Certification: "TV-Y"},
		{Title: "Unrated", MediaType: "series"},
		{Title: "Cars", MediaType: "movie", Certification: "G"},
		{Title: "Alien", MediaType: "movie", Certification: "R"},
	}
	got := FilterCalendarByRatings(items, "G", "TV-PG")
	if len(got) != 2 || got[0].Title != "Bluey" || got[1].Title != "Cars" {
		t.Fatalf("unexpected result: %+v", got)
	}
	if len(FilterCalendarByRatings(items, "", "")) != len(items) {
		t.Fatal("empty limits should not filter")
	}
}
