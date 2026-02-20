package debrid

import "testing"

func TestParseQuery(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		wantTitle   string
		wantYear    int
		wantSeason  int
		wantEpisode int
		wantType    MediaType
	}{
		{
			name:        "movie with year and quality",
			query:       "The Matrix 1999 1080p BluRay x265",
			wantTitle:   "The Matrix",
			wantYear:    1999,
			wantSeason:  0,
			wantEpisode: 0,
			wantType:    MediaTypeMovie,
		},
		{
			name:        "series with season episode shorthand",
			query:       "Breaking Bad S01E02 2160p",
			wantTitle:   "Breaking Bad",
			wantYear:    0,
			wantSeason:  1,
			wantEpisode: 2,
			wantType:    MediaTypeSeries,
		},
		{
			name:        "series with words season episode",
			query:       "Slow Horses Season 3 Episode 5 HDR",
			wantTitle:   "Slow Horses",
			wantYear:    0,
			wantSeason:  3,
			wantEpisode: 5,
			wantType:    MediaTypeSeries,
		},
		{
			name:        "season-only query defaults episode to 1",
			query:       "56 Days S01",
			wantTitle:   "56 Days",
			wantYear:    0,
			wantSeason:  1,
			wantEpisode: 1,
			wantType:    MediaTypeSeries,
		},
		{
			name:        "fallback retains tokens",
			query:       "Dune Part Two",
			wantTitle:   "Dune Part Two",
			wantYear:    0,
			wantSeason:  0,
			wantEpisode: 0,
			wantType:    MediaTypeUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseQuery(tt.query)
			if got.Title != tt.wantTitle {
				t.Fatalf("Title = %q, want %q", got.Title, tt.wantTitle)
			}
			if got.Year != tt.wantYear {
				t.Fatalf("Year = %d, want %d", got.Year, tt.wantYear)
			}
			if got.Season != tt.wantSeason {
				t.Fatalf("Season = %d, want %d", got.Season, tt.wantSeason)
			}
			if got.Episode != tt.wantEpisode {
				t.Fatalf("Episode = %d, want %d", got.Episode, tt.wantEpisode)
			}
			if got.MediaType != tt.wantType {
				t.Fatalf("MediaType = %v, want %v", got.MediaType, tt.wantType)
			}
		})
	}
}
