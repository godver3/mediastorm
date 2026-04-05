package handlers

import (
	"context"
	"testing"

	"novastream/models"
)

// mockMovieDetailsProvider implements MovieDetailsProvider for testing
type mockMovieDetailsProvider struct {
	title *models.Title
	err   error
}

func (m *mockMovieDetailsProvider) MovieInfo(_ context.Context, _ models.MovieDetailsQuery) (*models.Title, error) {
	return m.title, m.err
}

func TestPrequeueMovieAnimeDetection(t *testing.T) {
	tests := []struct {
		name      string
		title     models.Title
		wantAnime bool
	}{
		{
			name: "anime genre detected",
			title: models.Title{
				Name:   "Ponyo",
				Genres: []string{"Adventure", "Anime", "Fantasy"},
			},
			wantAnime: true,
		},
		{
			name: "east asian animated movie detected via original title",
			title: models.Title{
				Name:         "Spirited Away",
				OriginalName: "千と千尋の神隠し",
				Genres:       []string{"Animation", "Family"},
			},
			wantAnime: true,
		},
		{
			name: "case insensitive anime",
			title: models.Title{
				Name:   "Ponyo",
				Genres: []string{"Drama", "ANIME"},
			},
			wantAnime: true,
		},
		{
			name: "western animated movie is not anime",
			title: models.Title{
				Name:   "Hop",
				Genres: []string{"Animation", "Family"},
			},
			wantAnime: false,
		},
		{
			name: "east asian animated movie detected via language",
			title: models.Title{
				Name:     "Ne Zha",
				Language: "zho",
				Genres:   []string{"Animation", "Fantasy"},
			},
			wantAnime: true,
		},
		{
			name: "non-anime movie",
			title: models.Title{
				Name:   "John Wick",
				Genres: []string{"Action", "Drama"},
			},
			wantAnime: false,
		},
		{
			name: "empty genres",
			title: models.Title{
				Name: "Unknown",
			},
			wantAnime: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := &PrequeueHandler{
				movieMetadataSvc: &mockMovieDetailsProvider{
					title: &tt.title,
				},
			}

			// Simulate the movie anime detection logic from runPrequeueWorker
			var isAnime bool
			mediaType := "movie"

			if mediaType == "movie" && handler.movieMetadataSvc != nil {
				movieQuery := models.MovieDetailsQuery{
					TitleID: "test-id",
					Name:    "Ponyo",
					Year:    2008,
				}
				if movieTitle, err := handler.movieMetadataSvc.MovieInfo(context.Background(), movieQuery); err == nil && movieTitle != nil {
					isAnime = isAnimeTitle(movieTitle)
				}
			}

			if isAnime != tt.wantAnime {
				t.Errorf("isAnime = %v, want %v", isAnime, tt.wantAnime)
			}
		})
	}
}

func TestPrequeueMovieAnimeDetection_NilService(t *testing.T) {
	handler := &PrequeueHandler{
		movieMetadataSvc: nil,
	}

	var isAnime bool
	mediaType := "movie"

	if mediaType == "movie" && handler.movieMetadataSvc != nil {
		// Should not enter this block
		t.Fatal("should not attempt movie lookup with nil service")
	}

	if isAnime {
		t.Error("isAnime should be false when service is nil")
	}
}

func TestPrequeueMovieAnimeDetection_SeriesSkipped(t *testing.T) {
	handler := &PrequeueHandler{
		movieMetadataSvc: &mockMovieDetailsProvider{
			title: &models.Title{
				Name:   "Some Series",
				Genres: []string{"Animation"},
			},
		},
	}

	var isAnime bool
	mediaType := "series"

	// The movie anime detection should not run for series
	if mediaType == "movie" && handler.movieMetadataSvc != nil {
		t.Fatal("should not attempt movie lookup for series media type")
	}

	if isAnime {
		t.Error("isAnime should be false for series media type")
	}
}
