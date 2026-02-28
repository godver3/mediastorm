package trakt

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestScrobbleStart(t *testing.T) {
	var receivedBody ScrobbleRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/scrobble/start" {
			t.Errorf("expected path /scrobble/start, got %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("expected Authorization header")
		}
		if r.Header.Get("trakt-api-key") != "test-client-id" {
			t.Errorf("expected trakt-api-key header")
		}

		json.NewDecoder(r.Body).Decode(&receivedBody)

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(ScrobbleResponse{
			ID:       1,
			Action:   "start",
			Progress: 25.0,
		})
	}))
	defer server.Close()

	// Override base URL for testing
	origURL := traktAPIBaseURL
	defer func() { setBaseURL(origURL) }()
	setBaseURL(server.URL)

	client := NewClient("test-client-id", "test-secret")
	req := ScrobbleRequest{
		Movie: &ScrobbleMovie{
			Title: "Test Movie",
			Year:  2024,
			IDs:   SyncIDs{TMDB: 12345},
		},
		Progress: 25.0,
	}

	resp, err := client.ScrobbleStart("test-token", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Action != "start" {
		t.Errorf("expected action 'start', got %s", resp.Action)
	}
	if resp.Progress != 25.0 {
		t.Errorf("expected progress 25.0, got %f", resp.Progress)
	}
	if receivedBody.Movie == nil {
		t.Fatal("expected movie in request body")
	}
	if receivedBody.Movie.IDs.TMDB != 12345 {
		t.Errorf("expected TMDB ID 12345, got %d", receivedBody.Movie.IDs.TMDB)
	}
	if receivedBody.Progress != 25.0 {
		t.Errorf("expected progress 25.0, got %f", receivedBody.Progress)
	}
}

func TestScrobblePause(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/scrobble/pause" {
			t.Errorf("expected path /scrobble/pause, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(ScrobbleResponse{
			Action:   "pause",
			Progress: 50.0,
		})
	}))
	defer server.Close()

	origURL := traktAPIBaseURL
	defer func() { setBaseURL(origURL) }()
	setBaseURL(server.URL)

	client := NewClient("id", "secret")
	resp, err := client.ScrobblePause("token", ScrobbleRequest{
		Episode: &ScrobbleEpisode{Season: 1, Number: 5},
		Show:    &ScrobbleShow{IDs: SyncIDs{TVDB: 999}},
		Progress: 50.0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != "pause" {
		t.Errorf("expected action 'pause', got %s", resp.Action)
	}
}

func TestScrobbleStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/scrobble/stop" {
			t.Errorf("expected path /scrobble/stop, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(ScrobbleResponse{
			Action:   "stop",
			Progress: 92.0,
		})
	}))
	defer server.Close()

	origURL := traktAPIBaseURL
	defer func() { setBaseURL(origURL) }()
	setBaseURL(server.URL)

	client := NewClient("id", "secret")
	resp, err := client.ScrobbleStop("token", ScrobbleRequest{
		Movie:    &ScrobbleMovie{IDs: SyncIDs{IMDB: "tt1234567"}},
		Progress: 92.0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != "stop" {
		t.Errorf("expected action 'stop', got %s", resp.Action)
	}
}

func TestScrobbleConflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(ScrobbleResponse{Action: "start"})
	}))
	defer server.Close()

	origURL := traktAPIBaseURL
	defer func() { setBaseURL(origURL) }()
	setBaseURL(server.URL)

	client := NewClient("id", "secret")
	_, err := client.ScrobbleStart("token", ScrobbleRequest{
		Movie: &ScrobbleMovie{IDs: SyncIDs{TMDB: 1}},
	})
	if err != nil {
		t.Fatalf("409 Conflict should be treated as success, got error: %v", err)
	}
}

func TestScrobbleError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	}))
	defer server.Close()

	origURL := traktAPIBaseURL
	defer func() { setBaseURL(origURL) }()
	setBaseURL(server.URL)

	client := NewClient("id", "secret")
	_, err := client.ScrobbleStart("bad-token", ScrobbleRequest{
		Movie: &ScrobbleMovie{IDs: SyncIDs{TMDB: 1}},
	})
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
}

func TestGetPlaybackProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sync/playback/movies" {
			t.Errorf("expected path /sync/playback/movies, got %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode([]PlaybackItem{
			{
				ID:       42,
				Progress: 35.5,
				Type:     "movie",
				Movie:    &Movie{Title: "Test", IDs: IDs{TMDB: 100}},
			},
		})
	}))
	defer server.Close()

	origURL := traktAPIBaseURL
	defer func() { setBaseURL(origURL) }()
	setBaseURL(server.URL)

	client := NewClient("id", "secret")
	items, err := client.GetPlaybackProgress("token", "movies")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].ID != 42 {
		t.Errorf("expected ID 42, got %d", items[0].ID)
	}
	if items[0].Progress != 35.5 {
		t.Errorf("expected progress 35.5, got %f", items[0].Progress)
	}
	if items[0].Movie == nil || items[0].Movie.IDs.TMDB != 100 {
		t.Error("expected movie with TMDB ID 100")
	}
}

func TestRemovePlaybackItem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sync/playback/42" {
			t.Errorf("expected path /sync/playback/42, got %s", r.URL.Path)
		}
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	origURL := traktAPIBaseURL
	defer func() { setBaseURL(origURL) }()
	setBaseURL(server.URL)

	client := NewClient("id", "secret")
	err := client.RemovePlaybackItem("token", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRemovePlaybackItemError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("not found"))
	}))
	defer server.Close()

	origURL := traktAPIBaseURL
	defer func() { setBaseURL(origURL) }()
	setBaseURL(server.URL)

	client := NewClient("id", "secret")
	err := client.RemovePlaybackItem("token", 999)
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}
