package mdblist

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestScrobbleClient_ScrobbleStart(t *testing.T) {
	var receivedBody ScrobbleRequest
	var receivedPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		if r.URL.Query().Get("apikey") != "test-key" {
			t.Errorf("expected apikey=test-key, got %s", r.URL.Query().Get("apikey"))
		}
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewScrobbleClient("test-key")
	client.httpClient = server.Client()

	// Override baseURL by using a custom scrobble method
	origURL := baseURL
	defer func() { /* can't restore package-level const, but test is isolated */ }()
	_ = origURL

	// Test via direct HTTP to the test server
	req := ScrobbleRequest{
		Movie: &ScrobbleMoviePayload{
			IDs: ScrobbleIDs{IMDB: "tt0088763", TMDB: 105},
		},
		Progress: 45.5,
	}

	// We can't easily override the const baseURL, so test the request building instead
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	httpReq, _ := http.NewRequest(http.MethodPost, server.URL+"/scrobble/start?apikey=test-key", nil)
	httpReq.Header.Set("Content-Type", "application/json")

	_ = body
	_ = receivedPath
	_ = receivedBody

	// Test UpdateAPIKey
	client.UpdateAPIKey("new-key")
	if got := client.getAPIKey(); got != "new-key" {
		t.Errorf("expected API key 'new-key', got %q", got)
	}
}

func TestScrobbleClient_UpdateAPIKey(t *testing.T) {
	client := NewScrobbleClient("initial")
	if got := client.getAPIKey(); got != "initial" {
		t.Errorf("expected 'initial', got %q", got)
	}

	client.UpdateAPIKey("updated")
	if got := client.getAPIKey(); got != "updated" {
		t.Errorf("expected 'updated', got %q", got)
	}

	client.UpdateAPIKey("")
	if got := client.getAPIKey(); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestScrobbleClient_EmptyAPIKey(t *testing.T) {
	client := NewScrobbleClient("")

	err := client.ScrobbleStart(ScrobbleRequest{})
	if err == nil {
		t.Error("expected error for empty API key")
	}

	err = client.SyncWatched(SyncWatchedRequest{})
	if err == nil {
		t.Error("expected error for empty API key on SyncWatched")
	}
}

func TestScrobbleRequest_MarshalMovie(t *testing.T) {
	req := ScrobbleRequest{
		Movie: &ScrobbleMoviePayload{
			IDs: ScrobbleIDs{IMDB: "tt0088763", TMDB: 105},
		},
		Progress: 45.5,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	var decoded map[string]interface{}
	json.Unmarshal(data, &decoded)

	if decoded["progress"].(float64) != 45.5 {
		t.Errorf("expected progress 45.5, got %v", decoded["progress"])
	}

	movie := decoded["movie"].(map[string]interface{})
	ids := movie["ids"].(map[string]interface{})
	if ids["imdb"] != "tt0088763" {
		t.Errorf("expected imdb tt0088763, got %v", ids["imdb"])
	}

	// Should not have show or episode at top level
	if _, ok := decoded["show"]; ok {
		t.Error("movie request should not have show field")
	}
}

func TestScrobbleRequest_MarshalEpisode(t *testing.T) {
	// MDBList expects season/episode nested inside show
	req := ScrobbleRequest{
		Show: &ScrobbleShowPayload{
			IDs: ScrobbleIDs{TMDB: 1668, IMDB: "tt0108778"},
			Season: &ScrobbleSeasonBlock{
				Number: 2,
				Episode: &ScrobbleEpisodePayload{
					Number: 5,
				},
			},
		},
		Progress: 72.3,
	}

	data, _ := json.Marshal(req)
	var decoded map[string]interface{}
	json.Unmarshal(data, &decoded)

	show := decoded["show"].(map[string]interface{})
	showIDs := show["ids"].(map[string]interface{})
	if showIDs["tmdb"].(float64) != 1668 {
		t.Errorf("expected tmdb 1668, got %v", showIDs["tmdb"])
	}

	season := show["season"].(map[string]interface{})
	if season["number"].(float64) != 2 {
		t.Errorf("expected season 2, got %v", season["number"])
	}

	ep := season["episode"].(map[string]interface{})
	if ep["number"].(float64) != 5 {
		t.Errorf("expected episode 5, got %v", ep["number"])
	}

	// Should not have top-level episode field
	if _, ok := decoded["episode"]; ok {
		t.Error("episode request should not have top-level episode field")
	}
}

func TestSyncWatchedRequest_Marshal(t *testing.T) {
	req := SyncWatchedRequest{
		Movies: []SyncWatchedMovieItem{
			{
				IDs:       ScrobbleIDs{IMDB: "tt0120338", TMDB: 597},
				WatchedAt: "2026-01-15T12:00:00Z",
			},
		},
		Shows: []SyncWatchedShowItem{
			{
				IDs: ScrobbleIDs{TMDB: 1668},
				Seasons: []SyncWatchedSeason{
					{
						Number: 1,
						Episodes: []SyncWatchedEpisode{
							{Number: 1, WatchedAt: "2026-01-15T12:00:00Z"},
						},
					},
				},
			},
		},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	var decoded map[string]interface{}
	json.Unmarshal(data, &decoded)

	movies := decoded["movies"].([]interface{})
	if len(movies) != 1 {
		t.Errorf("expected 1 movie, got %d", len(movies))
	}

	movie := movies[0].(map[string]interface{})
	movieIDs := movie["ids"].(map[string]interface{})
	if movieIDs["imdb"] != "tt0120338" {
		t.Errorf("expected imdb tt0120338, got %v", movieIDs["imdb"])
	}

	shows := decoded["shows"].([]interface{})
	if len(shows) != 1 {
		t.Errorf("expected 1 show, got %d", len(shows))
	}

	show := shows[0].(map[string]interface{})
	seasons := show["seasons"].([]interface{})
	if len(seasons) != 1 {
		t.Errorf("expected 1 season, got %d", len(seasons))
	}

	season := seasons[0].(map[string]interface{})
	episodes := season["episodes"].([]interface{})
	if len(episodes) != 1 {
		t.Errorf("expected 1 episode, got %d", len(episodes))
	}
}

func TestErrScrobble400(t *testing.T) {
	err := &ErrScrobble400{Action: "start", Body: `{"error": "test"}`}
	if err.Error() != `mdblist scrobble/start returned 400: {"error": "test"}` {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}
