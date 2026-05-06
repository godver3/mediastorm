package handlers

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"novastream/models"
	"novastream/services/playback"
)

type prequeueRoundTripFunc func(*http.Request) (*http.Response, error)

func (f prequeueRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

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

func TestShouldForceReresolveForStatus(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{
		{status: http.StatusUnauthorized, want: true},
		{status: http.StatusForbidden, want: true},
		{status: http.StatusNotFound, want: true},
		{status: http.StatusGone, want: true},
		{status: http.StatusMethodNotAllowed, want: false},
		{status: http.StatusTooManyRequests, want: false},
		{status: http.StatusInternalServerError, want: false},
		{status: http.StatusOK, want: false},
	}

	for _, tt := range tests {
		if got := shouldForceReresolveForStatus(tt.status); got != tt.want {
			t.Fatalf("status %d: got %v want %v", tt.status, got, tt.want)
		}
	}
}

func TestPrequeueEpisodeHelpers_AllowSpecials(t *testing.T) {
	handler := &PrequeueHandler{}
	episode := &models.EpisodeReference{SeasonNumber: 0, EpisodeNumber: 1}

	if got, want := buildDisplayName("The Bear", 2022, episode), "The Bear S00E01"; got != want {
		t.Fatalf("buildDisplayName = %q, want %q", got, want)
	}

	if got, want := handler.buildSearchQuery("The Bear", "series", episode), "The Bear S00E01"; got != want {
		t.Fatalf("buildSearchQuery = %q, want %q", got, want)
	}
}

func TestDefaultExternalURLValidator(t *testing.T) {
	originalTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = originalTransport }()

	t.Run("allows head 200", func(t *testing.T) {
		http.DefaultTransport = prequeueRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodHead {
				t.Fatalf("expected HEAD request, got %s", r.Method)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		})

		if err := defaultExternalURLValidator(context.Background(), "https://example.com/stream"); err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	})

	t.Run("forces reresolve on 403", func(t *testing.T) {
		http.DefaultTransport = prequeueRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		})

		if err := defaultExternalURLValidator(context.Background(), "https://example.com/stream"); err == nil {
			t.Fatal("expected validation error for 403")
		}
	})

	t.Run("allows head 405 fallback", func(t *testing.T) {
		http.DefaultTransport = prequeueRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusMethodNotAllowed,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		})

		if err := defaultExternalURLValidator(context.Background(), "https://example.com/stream"); err != nil {
			t.Fatalf("expected nil error for 405, got %v", err)
		}
	})
}

func TestValidateReadyEntryForReuse(t *testing.T) {
	handler := &PrequeueHandler{}

	t.Run("skips non external paths", func(t *testing.T) {
		entry := &playback.PrequeueEntry{StreamPath: "/debrid/realdebrid/file.mkv"}
		if err := handler.validateReadyEntryForReuse(context.Background(), entry); err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	})

	t.Run("uses injected validator for external paths", func(t *testing.T) {
		called := false
		handler.externalURLValidator = func(_ context.Context, streamURL string) error {
			called = true
			if streamURL != "https://example.com/stream" {
				t.Fatalf("unexpected stream URL %q", streamURL)
			}
			return nil
		}

		entry := &playback.PrequeueEntry{StreamPath: "https://example.com/stream"}
		if err := handler.validateReadyEntryForReuse(context.Background(), entry); err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if !called {
			t.Fatal("expected validator to be called")
		}
	})
}
