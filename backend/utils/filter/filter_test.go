package filter

import (
	"testing"

	"novastream/models"
)

func TestResults_MovieFiltering(t *testing.T) {
	results := []models.NZBResult{
		{Title: "The.Matrix.1999.1080p.BluRay.x264-SPARKS"},        // Should match
		{Title: "The.Matrix.Reloaded.2003.1080p.BluRay.x264"},      // Wrong year, should be filtered
		{Title: "Inception.2010.1080p.BluRay.x264"},                // Wrong title, should be filtered
		{Title: "The.Matrix.1999.720p.WEB-DL.x264"},                // Should match
		{Title: "The.Matrix.2000.1080p.BluRay.x264"},               // Year within ±1, should match
		{Title: "The.Matrix.Resurrections.2021.1080p.BluRay.x264"}, // Wrong movie, should be filtered
	}

	opts := Options{
		ExpectedTitle: "The Matrix",
		ExpectedYear:  1999,
		IsMovie:       true,
	}

	filtered := Results(results, opts)

	// Should keep results at indices 0, 3, 4 (The Matrix 1999, 1999, 2000)
	expectedCount := 3
	if len(filtered) != expectedCount {
		t.Errorf("Expected %d results, got %d", expectedCount, len(filtered))
		for i, r := range filtered {
			t.Logf("  Result[%d]: %s", i, r.Title)
		}
	}

	// Verify we got the right ones
	expectedTitles := map[string]bool{
		"The.Matrix.1999.1080p.BluRay.x264-SPARKS": true,
		"The.Matrix.1999.720p.WEB-DL.x264":         true,
		"The.Matrix.2000.1080p.BluRay.x264":        true,
	}

	for _, r := range filtered {
		if !expectedTitles[r.Title] {
			t.Errorf("Unexpected result in filtered list: %s", r.Title)
		}
	}
}

func TestResults_TVShowFiltering(t *testing.T) {
	results := []models.NZBResult{
		{Title: "The.Simpsons.S01E01.1080p.BluRay.x265"},     // Should match
		{Title: "The.Simpsons.S01E02.720p.WEB-DL.x264"},      // Should match
		{Title: "Family.Guy.S01E01.1080p.BluRay.x264"},       // Wrong show, should be filtered
		{Title: "The.Simpsons.Movie.2007.1080p.BluRay.x264"}, // Will be filtered (title not similar enough - 66%)
	}

	opts := Options{
		ExpectedTitle: "The Simpsons",
		ExpectedYear:  0, // Year doesn't matter for TV shows
		IsMovie:       false,
	}

	filtered := Results(results, opts)

	// Should keep results at indices 0, 1 (The Simpsons episodes only)
	// Note: "The Simpsons Movie" is only 66.67% similar, below the 90% threshold
	expectedCount := 2
	if len(filtered) != expectedCount {
		t.Errorf("Expected %d results, got %d", expectedCount, len(filtered))
		for i, r := range filtered {
			t.Logf("  Result[%d]: %s", i, r.Title)
		}
	}
}

func TestResults_NoFiltering(t *testing.T) {
	results := []models.NZBResult{
		{Title: "Some.Random.Release.1080p.BluRay.x264"},
		{Title: "Another.Release.720p.WEB-DL.x264"},
	}

	opts := Options{
		ExpectedTitle: "",
		ExpectedYear:  0,
		IsMovie:       true,
	}

	// With no expected title, all results should be kept (parse errors)
	filtered := Results(results, opts)

	if len(filtered) != len(results) {
		t.Errorf("Expected all %d results to be kept, got %d", len(results), len(filtered))
	}
}

func TestResults_AlternateTitles(t *testing.T) {
	results := []models.NZBResult{
		{Title: "La.Casa.de.Papel.S01E01.1080p.NF.WEB-DL.x265"},
		{Title: "Random.Show.S01E01.1080p"},
	}

	opts := Options{
		ExpectedTitle:   "Money Heist",
		ExpectedYear:    0,
		IsMovie:         false,
		AlternateTitles: []string{"La Casa de Papel"},
	}

	filtered := Results(results, opts)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 result to match alternate title, got %d", len(filtered))
	}
	if filtered[0].Title != results[0].Title {
		t.Fatalf("expected alternate title match for %q", results[0].Title)
	}
}

func TestResults_JapaneseRomanization(t *testing.T) {
	results := []models.NZBResult{
		{Title: "Ikusagami.S01E01.1080p.WEB-DL"},
		{Title: "Completely.Different.S01E01"},
	}

	opts := Options{
		ExpectedTitle:   "الساموراي الصامد الأخير",
		AlternateTitles: []string{"イクサガミ"},
	}

	filtered := Results(results, opts)
	if len(filtered) != 1 {
		t.Fatalf("expected romanized alternate to match, got %d results", len(filtered))
	}
	if filtered[0].Title != results[0].Title {
		t.Fatalf("expected Ikusagami release to survive filtering")
	}
}

func TestResults_MediaTypeFiltering(t *testing.T) {
	// Test that TV show results are filtered out when searching for movies
	t.Run("movie search rejects TV patterns", func(t *testing.T) {
		results := []models.NZBResult{
			{Title: "Trigger.Point.2022.1080p.BluRay.x264"},            // Movie pattern - should match
			{Title: "Trigger.Point.S01E01.1080p.WEB-DL.x264"},          // TV pattern - should be filtered
			{Title: "Trigger.Point.S03E01.Episode.1.1080p.AMZN.WEB-DL"}, // TV pattern - should be filtered
		}

		opts := Options{
			ExpectedTitle: "Trigger Point",
			ExpectedYear:  2022,
			IsMovie:       true,
		}

		filtered := Results(results, opts)

		if len(filtered) != 1 {
			t.Errorf("Expected 1 result (movie only), got %d", len(filtered))
			for i, r := range filtered {
				t.Logf("  Result[%d]: %s", i, r.Title)
			}
		}

		if len(filtered) > 0 && filtered[0].Title != "Trigger.Point.2022.1080p.BluRay.x264" {
			t.Errorf("Expected movie result, got: %s", filtered[0].Title)
		}
	})

	// Test that movie results are filtered out when searching for TV shows
	t.Run("TV search rejects movie patterns", func(t *testing.T) {
		results := []models.NZBResult{
			{Title: "Trigger.Point.S01E01.1080p.WEB-DL.x264"},           // TV pattern - should match
			{Title: "Trigger.Point.S02E05.720p.HDTV.x264"},              // TV pattern - should match
			{Title: "Trigger.Point.2022.1080p.BluRay.x264"},             // Movie pattern - should be filtered
		}

		opts := Options{
			ExpectedTitle: "Trigger Point",
			ExpectedYear:  0,
			IsMovie:       false,
		}

		filtered := Results(results, opts)

		if len(filtered) != 2 {
			t.Errorf("Expected 2 results (TV episodes only), got %d", len(filtered))
			for i, r := range filtered {
				t.Logf("  Result[%d]: %s", i, r.Title)
			}
		}

		// Verify movie pattern was filtered out
		for _, r := range filtered {
			if r.Title == "Trigger.Point.2022.1080p.BluRay.x264" {
				t.Error("Movie result should have been filtered from TV show search")
			}
		}
	})
}

func TestShouldFilter(t *testing.T) {
	tests := []struct {
		title    string
		expected bool
	}{
		{"The Matrix", true},
		{"", false},
		{"  ", false},
		{"The Simpsons S01E01", true},
	}

	for _, tt := range tests {
		result := ShouldFilter(tt.title)
		if result != tt.expected {
			t.Errorf("ShouldFilter(%q) = %v, want %v", tt.title, result, tt.expected)
		}
	}
}

func TestResults_PackSizeCalculation(t *testing.T) {
	// Test that complete packs are not rejected based on full pack size
	// when per-episode size is within limit
	t.Run("complete pack with 4 seasons passes size limit", func(t *testing.T) {
		// ReBoot: 4 seasons × ~13 eps = 52 eps, 26 GB pack = ~0.5 GB/ep
		results := []models.NZBResult{
			{
				Title:     "ReBoot.1994.COMPLETE.1080p.WEBRip.x264-[TroubleGod]",
				SizeBytes: 26 * 1024 * 1024 * 1024, // 26 GB
			},
		}

		opts := Options{
			ExpectedTitle:    "ReBoot",
			ExpectedYear:     1994,
			IsMovie:          false,
			MaxSizeEpisodeGB: 10.0, // 10 GB per episode limit
		}

		filtered := Results(results, opts)

		// Should pass: 26 GB / (4 seasons × 13 eps) = ~0.5 GB per episode
		if len(filtered) != 1 {
			t.Errorf("Expected complete pack to pass size filter (per-ep size < limit), got %d results", len(filtered))
		}
	})

	t.Run("season pack with 1 season passes size limit", func(t *testing.T) {
		// Breaking Bad S01: 1 season × ~13 eps = 13 eps, 13 GB pack = 1 GB/ep
		results := []models.NZBResult{
			{
				Title:     "Breaking.Bad.S01.1080p.BluRay.x265",
				SizeBytes: 13 * 1024 * 1024 * 1024, // 13 GB
			},
		}

		opts := Options{
			ExpectedTitle:    "Breaking Bad",
			ExpectedYear:     0,
			IsMovie:          false,
			MaxSizeEpisodeGB: 5.0, // 5 GB per episode limit
		}

		filtered := Results(results, opts)

		// Should pass: 13 GB / 13 eps = 1 GB per episode
		if len(filtered) != 1 {
			t.Errorf("Expected season pack to pass size filter (per-ep size < limit), got %d results", len(filtered))
		}
	})

	t.Run("massive pack exceeds per-episode limit", func(t *testing.T) {
		// Giant pack: 5 seasons × 13 eps = 65 eps, 1700 GB pack = ~26 GB/ep
		results := []models.NZBResult{
			{
				Title:     "Breaking.Bad.COMPLETE.S01-S05.2160p.WEB-DL",
				SizeBytes: 1700 * 1024 * 1024 * 1024, // 1700 GB
			},
		}

		opts := Options{
			ExpectedTitle:    "Breaking Bad",
			ExpectedYear:     0,
			IsMovie:          false,
			MaxSizeEpisodeGB: 10.0, // 10 GB per episode limit
		}

		filtered := Results(results, opts)

		// Should fail: 1700 GB / 65 eps = ~26 GB per episode > 10 GB limit
		if len(filtered) != 0 {
			t.Errorf("Expected massive pack to be filtered (per-ep size > limit), got %d results", len(filtered))
		}
	})

	t.Run("with TotalSeriesEpisodes provided", func(t *testing.T) {
		// Pack with exact episode count from metadata
		results := []models.NZBResult{
			{
				Title:     "ReBoot.1994.COMPLETE.1080p.WEBRip.x264",
				SizeBytes: 26 * 1024 * 1024 * 1024, // 26 GB
			},
		}

		opts := Options{
			ExpectedTitle:       "ReBoot",
			ExpectedYear:        1994,
			IsMovie:             false,
			MaxSizeEpisodeGB:    1.0, // 1 GB per episode limit
			TotalSeriesEpisodes: 47,  // Actual ReBoot episode count
		}

		filtered := Results(results, opts)

		// Should pass: 26 GB / 47 eps = ~0.55 GB per episode < 1 GB limit
		if len(filtered) != 1 {
			t.Errorf("Expected pack to pass with TotalSeriesEpisodes, got %d results", len(filtered))
		}
	})

	t.Run("single episode not treated as pack", func(t *testing.T) {
		results := []models.NZBResult{
			{
				Title:     "ReBoot.S01E01.1080p.WEBRip.x264",
				SizeBytes: 12 * 1024 * 1024 * 1024, // 12 GB single episode
			},
		}

		opts := Options{
			ExpectedTitle:    "ReBoot",
			ExpectedYear:     0,
			IsMovie:          false,
			MaxSizeEpisodeGB: 10.0, // 10 GB per episode limit
		}

		filtered := Results(results, opts)

		// Should fail: single episode at 12 GB > 10 GB limit
		if len(filtered) != 0 {
			t.Errorf("Expected single episode to be filtered, got %d results", len(filtered))
		}
	})

	t.Run("with EpisodeResolver for complete pack", func(t *testing.T) {
		// Complete pack without season info - uses resolver for total episodes
		results := []models.NZBResult{
			{
				Title:     "ReBoot.1994.COMPLETE.1080p.WEBRip.x264",
				SizeBytes: 26 * 1024 * 1024 * 1024, // 26 GB
			},
		}

		// Mock resolver with season counts: S1=13, S2=12, S3=10, S4=12 = 47 total
		resolver := NewSeriesEpisodeResolver(map[int]int{
			1: 13,
			2: 12,
			3: 10,
			4: 12,
		})

		opts := Options{
			ExpectedTitle:    "ReBoot",
			ExpectedYear:     1994,
			IsMovie:          false,
			MaxSizeEpisodeGB: 1.0, // 1 GB per episode limit
			EpisodeResolver:  resolver,
		}

		filtered := Results(results, opts)

		// Should pass: 26 GB / 47 eps = ~0.55 GB per episode < 1 GB limit
		if len(filtered) != 1 {
			t.Errorf("Expected complete pack to pass with EpisodeResolver, got %d results", len(filtered))
		}
	})

	t.Run("with EpisodeResolver for season pack", func(t *testing.T) {
		// Season 2 pack - uses resolver for that specific season
		results := []models.NZBResult{
			{
				Title:     "Breaking.Bad.S02.1080p.BluRay.x265",
				SizeBytes: 13 * 1024 * 1024 * 1024, // 13 GB
			},
		}

		// Mock resolver with Breaking Bad seasons
		resolver := NewSeriesEpisodeResolver(map[int]int{
			1: 7,
			2: 13,
			3: 13,
			4: 13,
			5: 16,
		})

		opts := Options{
			ExpectedTitle:    "Breaking Bad",
			ExpectedYear:     0,
			IsMovie:          false,
			MaxSizeEpisodeGB: 2.0, // 2 GB per episode limit
			EpisodeResolver:  resolver,
		}

		filtered := Results(results, opts)

		// Should pass: 13 GB / 13 eps (S02) = 1.0 GB per episode < 2 GB limit
		if len(filtered) != 1 {
			t.Errorf("Expected season pack to pass with EpisodeResolver, got %d results", len(filtered))
		}
	})
}

func TestEstimatePackEpisodeCount(t *testing.T) {
	tests := []struct {
		name                string
		seasons             []int
		totalSeriesEpisodes int
		expected            int
	}{
		{"with totalSeriesEpisodes", []int{1, 2, 3}, 47, 47},
		{"5 seasons no total", []int{1, 2, 3, 4, 5}, 0, 65}, // 5 × 13
		{"1 season no total", []int{1}, 0, 13},              // 1 × 13
		{"no info", nil, 0, 0},                              // 0 = skip size filter
		{"empty seasons no total", []int{}, 0, 0},           // 0 = skip size filter
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := estimatePackEpisodeCount(tt.seasons, tt.totalSeriesEpisodes)
			if result != tt.expected {
				t.Errorf("estimatePackEpisodeCount(%v, %d) = %d, want %d",
					tt.seasons, tt.totalSeriesEpisodes, result, tt.expected)
			}
		})
	}
}
