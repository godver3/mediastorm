package kids

import (
	"testing"

	"novastream/models"
)

func TestFilterSearchByRatings(t *testing.T) {
	results := []models.SearchResult{
		{Score: 90, Title: models.Title{Name: "G Movie", MediaType: "movie", Certification: "G"}},
		{Score: 80, Title: models.Title{Name: "PG Movie", MediaType: "movie", Certification: "PG"}},
		{Score: 70, Title: models.Title{Name: "R Movie", MediaType: "movie", Certification: "R"}},
		{Score: 60, Title: models.Title{Name: "No Rating", MediaType: "movie"}},
		{Score: 50, Title: models.Title{Name: "TV-Y Show", MediaType: "series", Certification: "TV-Y"}},
		{Score: 40, Title: models.Title{Name: "TV-MA Show", MediaType: "series", Certification: "TV-MA"}},
	}

	filtered := FilterSearchByRatings(results, "PG", "TV-PG")

	// Expected: G Movie, PG Movie, TV-Y Show
	if len(filtered) != 3 {
		t.Fatalf("expected 3 results, got %d: %+v", len(filtered), filtered)
	}

	names := make([]string, len(filtered))
	for i, r := range filtered {
		names[i] = r.Title.Name
	}
	expected := []string{"G Movie", "PG Movie", "TV-Y Show"}
	for i, name := range expected {
		if names[i] != name {
			t.Errorf("result[%d]: expected %q, got %q", i, name, names[i])
		}
	}
}

func TestFilterSearchByRatings_EmptyRatings(t *testing.T) {
	results := []models.SearchResult{
		{Score: 90, Title: models.Title{Name: "Movie", MediaType: "movie", Certification: "R"}},
	}

	// Empty ratings = no filtering
	filtered := FilterSearchByRatings(results, "", "")
	if len(filtered) != 1 {
		t.Fatalf("expected 1 result with empty ratings, got %d", len(filtered))
	}
}

func TestFilterSearchByRatings_MovieOnlyRating(t *testing.T) {
	results := []models.SearchResult{
		{Score: 90, Title: models.Title{Name: "G Movie", MediaType: "movie", Certification: "G"}},
		{Score: 80, Title: models.Title{Name: "R Movie", MediaType: "movie", Certification: "R"}},
		{Score: 70, Title: models.Title{Name: "TV-MA Show", MediaType: "series", Certification: "TV-MA"}},
	}

	// Only movie rating set — series should pass through
	filtered := FilterSearchByRatings(results, "PG", "")
	if len(filtered) != 2 {
		t.Fatalf("expected 2 results, got %d", len(filtered))
	}
	if filtered[0].Title.Name != "G Movie" || filtered[1].Title.Name != "TV-MA Show" {
		t.Fatalf("unexpected results: %+v", filtered)
	}
}

func TestValidateKidsMode_BothRejected(t *testing.T) {
	if ValidateKidsMode("both") {
		t.Fatal("expected 'both' mode to be invalid")
	}
}

func TestValidateKidsMode_ValidModes(t *testing.T) {
	for _, mode := range []string{"", "rating", "content_list"} {
		if !ValidateKidsMode(mode) {
			t.Errorf("expected mode %q to be valid", mode)
		}
	}
}
