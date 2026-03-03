package trakt

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"novastream/config"
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

// newTestConfigManager creates a config.Manager backed by a temp file with the given settings.
func newTestConfigManager(t *testing.T, settings config.Settings) *config.Manager {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	mgr := config.NewManager(path)
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save test config: %v", err)
	}
	return mgr
}

func TestEnsureValidToken_NotExpiring(t *testing.T) {
	client := NewClient("id", "secret")
	account := &config.TraktAccount{
		ID:          "acc1",
		AccessToken: "valid-token",
		ExpiresAt:   time.Now().Unix() + 7200, // 2 hours from now
	}

	token, err := client.EnsureValidToken(account, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "valid-token" {
		t.Errorf("expected 'valid-token', got %q", token)
	}
}

func TestEnsureValidToken_EmptyAccessToken(t *testing.T) {
	client := NewClient("id", "secret")
	account := &config.TraktAccount{
		ID:          "acc1",
		AccessToken: "",
	}

	token, err := client.EnsureValidToken(account, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "" {
		t.Errorf("expected empty token, got %q", token)
	}
}

func TestEnsureValidToken_RefreshesExpiredToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Errorf("expected path /oauth/token, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "new-access",
			RefreshToken: "new-refresh",
			ExpiresIn:    7776000,
			CreatedAt:    time.Now().Unix(),
		})
	}))
	defer server.Close()

	origURL := traktAPIBaseURL
	defer func() { setBaseURL(origURL) }()
	setBaseURL(server.URL)

	settings := config.Settings{
		Trakt: config.TraktSettings{
			Accounts: []config.TraktAccount{
				{
					ID:           "acc1",
					ClientID:     "cid",
					ClientSecret: "csec",
					AccessToken:  "old-access",
					RefreshToken: "old-refresh",
					ExpiresAt:    time.Now().Unix() - 100, // expired
				},
			},
		},
	}
	mgr := newTestConfigManager(t, settings)

	client := NewClient("cid", "csec")
	account := &config.TraktAccount{
		ID:           "acc1",
		ClientID:     "cid",
		ClientSecret: "csec",
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		ExpiresAt:    time.Now().Unix() - 100,
	}

	token, err := client.EnsureValidToken(account, mgr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "new-access" {
		t.Errorf("expected 'new-access', got %q", token)
	}

	// Verify config was persisted
	saved, err := mgr.Load()
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	savedAcc := saved.Trakt.GetAccountByID("acc1")
	if savedAcc == nil {
		t.Fatal("account not found in saved config")
	}
	if savedAcc.AccessToken != "new-access" {
		t.Errorf("saved access token: expected 'new-access', got %q", savedAcc.AccessToken)
	}
	if savedAcc.RefreshToken != "new-refresh" {
		t.Errorf("saved refresh token: expected 'new-refresh', got %q", savedAcc.RefreshToken)
	}
}

func TestEnsureValidToken_ConcurrentRefreshOnlyCallsOnce(t *testing.T) {
	var refreshCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			refreshCount.Add(1)
			// Simulate some latency so both goroutines overlap
			time.Sleep(50 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(TokenResponse{
				AccessToken:  "refreshed-token",
				RefreshToken: "refreshed-refresh",
				ExpiresIn:    7776000,
				CreatedAt:    time.Now().Unix(),
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	origURL := traktAPIBaseURL
	defer func() { setBaseURL(origURL) }()
	setBaseURL(server.URL)

	settings := config.Settings{
		Trakt: config.TraktSettings{
			Accounts: []config.TraktAccount{
				{
					ID:           "acc1",
					ClientID:     "cid",
					ClientSecret: "csec",
					AccessToken:  "expired-token",
					RefreshToken: "old-refresh",
					ExpiresAt:    time.Now().Unix() - 100,
				},
			},
		},
	}
	mgr := newTestConfigManager(t, settings)
	client := NewClient("cid", "csec")

	// Launch two goroutines that both try to refresh at the same time
	var wg sync.WaitGroup
	tokens := make([]string, 2)
	errs := make([]error, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			acc := &config.TraktAccount{
				ID:           "acc1",
				ClientID:     "cid",
				ClientSecret: "csec",
				AccessToken:  "expired-token",
				RefreshToken: "old-refresh",
				ExpiresAt:    time.Now().Unix() - 100,
			}
			tokens[idx], errs[idx] = client.EnsureValidToken(acc, mgr)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d error: %v", i, err)
		}
	}
	for i, tok := range tokens {
		if tok != "refreshed-token" {
			t.Errorf("goroutine %d: expected 'refreshed-token', got %q", i, tok)
		}
	}

	// The key assertion: only ONE actual refresh call was made to Trakt
	count := refreshCount.Load()
	if count != 1 {
		t.Errorf("expected exactly 1 refresh call, got %d", count)
	}
}
