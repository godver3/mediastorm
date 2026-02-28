package trakt

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
)

type mockTraktUserService struct {
	users map[string]models.User
}

func (m *mockTraktUserService) Get(id string) (models.User, bool) {
	u, ok := m.users[id]
	return u, ok
}

// newTestSetup creates a ScrobbleStateTracker backed by a temp config file for testing.
func newTestSetup(t *testing.T, serverURL string) *ScrobbleStateTracker {
	t.Helper()
	setBaseURL(serverURL)

	client := NewClient("test-id", "test-secret")

	// Write settings to a temp file so config.Manager can load them
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "settings.json")

	settings := config.Settings{
		Trakt: config.TraktSettings{
			Accounts: []config.TraktAccount{
				{
					ID:                "acct1",
					ClientID:          "test-id",
					ClientSecret:      "test-secret",
					AccessToken:       "test-token",
					ScrobblingEnabled: true,
					ExpiresAt:         time.Now().Add(24 * time.Hour).Unix(),
				},
			},
		},
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	cfgMgr := config.NewManager(cfgPath)

	userSvc := &mockTraktUserService{
		users: map[string]models.User{
			"user1": {ID: "user1", TraktAccountID: "acct1"},
		},
	}

	scrobbler := NewScrobbler(client, cfgMgr)
	scrobbler.SetUserService(userSvc)

	return NewScrobbleStateTracker(client, scrobbler, 15*time.Minute)
}

func TestHandleProgressUpdate_StartWatching(t *testing.T) {
	var mu sync.Mutex
	var actions []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		actions = append(actions, r.URL.Path)
		mu.Unlock()

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(ScrobbleResponse{Action: "start"})
	}))
	defer server.Close()

	origURL := traktAPIBaseURL
	defer func() { setBaseURL(origURL) }()

	tracker := newTestSetup(t, server.URL)

	update := models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "12345",
		Position:    300,
		Duration:    7200,
		IsPaused:    false,
		ExternalIDs: map[string]string{"tmdb": "12345"},
		MovieName:   "Test Movie",
		Year:        2024,
	}

	tracker.HandleProgressUpdate("user1", update, 4.17)

	mu.Lock()
	defer mu.Unlock()
	if len(actions) != 1 {
		t.Fatalf("expected 1 API call, got %d", len(actions))
	}
	if actions[0] != "/scrobble/start" {
		t.Errorf("expected /scrobble/start, got %s", actions[0])
	}

	// Verify session state
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	key := sessionKey("user1", "movie", "12345")
	sess, ok := tracker.sessions[key]
	if !ok {
		t.Fatal("expected session to exist")
	}
	if sess.state != stateWatching {
		t.Errorf("expected state watching, got %d", sess.state)
	}
}

func TestHandleProgressUpdate_PauseResume(t *testing.T) {
	var mu sync.Mutex
	var actions []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		actions = append(actions, r.URL.Path)
		mu.Unlock()

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(ScrobbleResponse{})
	}))
	defer server.Close()

	origURL := traktAPIBaseURL
	defer func() { setBaseURL(origURL) }()
	tracker := newTestSetup(t, server.URL)

	update := models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "12345",
		Position:    300,
		Duration:    7200,
		IsPaused:    false,
		ExternalIDs: map[string]string{"tmdb": "12345"},
	}

	// Start watching
	tracker.HandleProgressUpdate("user1", update, 4.17)

	// Pause
	update.IsPaused = true
	update.Position = 600
	tracker.HandleProgressUpdate("user1", update, 8.33)

	// Resume
	update.IsPaused = false
	update.Position = 700
	tracker.HandleProgressUpdate("user1", update, 9.72)

	mu.Lock()
	defer mu.Unlock()
	if len(actions) != 3 {
		t.Fatalf("expected 3 API calls (start, pause, start), got %d: %v", len(actions), actions)
	}
	expected := []string{"/scrobble/start", "/scrobble/pause", "/scrobble/start"}
	for i, exp := range expected {
		if actions[i] != exp {
			t.Errorf("call %d: expected %s, got %s", i, exp, actions[i])
		}
	}
}

func TestHandleProgressUpdate_NoRefreshBeforeInterval(t *testing.T) {
	var mu sync.Mutex
	var actions []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		actions = append(actions, r.URL.Path)
		mu.Unlock()

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(ScrobbleResponse{})
	}))
	defer server.Close()

	origURL := traktAPIBaseURL
	defer func() { setBaseURL(origURL) }()
	tracker := newTestSetup(t, server.URL)

	update := models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "12345",
		Position:    300,
		Duration:    7200,
		IsPaused:    false,
		ExternalIDs: map[string]string{"tmdb": "12345"},
	}

	// Start watching
	tracker.HandleProgressUpdate("user1", update, 4.17)

	// Another update while still watching — should NOT send another API call
	update.Position = 600
	tracker.HandleProgressUpdate("user1", update, 8.33)

	mu.Lock()
	defer mu.Unlock()
	if len(actions) != 1 {
		t.Fatalf("expected 1 API call (no refresh needed yet), got %d: %v", len(actions), actions)
	}
}

func TestHandleProgressUpdate_RefreshAfterInterval(t *testing.T) {
	var mu sync.Mutex
	var actions []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		actions = append(actions, r.URL.Path)
		mu.Unlock()

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(ScrobbleResponse{})
	}))
	defer server.Close()

	origURL := traktAPIBaseURL
	defer func() { setBaseURL(origURL) }()
	tracker := newTestSetup(t, server.URL)
	tracker.refreshInterval = 1 * time.Millisecond // very short for testing

	update := models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "12345",
		Position:    300,
		Duration:    7200,
		IsPaused:    false,
		ExternalIDs: map[string]string{"tmdb": "12345"},
	}

	// Start watching
	tracker.HandleProgressUpdate("user1", update, 4.17)

	// Wait for refresh interval to elapse
	time.Sleep(5 * time.Millisecond)

	// Another update — should trigger refresh
	update.Position = 600
	tracker.HandleProgressUpdate("user1", update, 8.33)

	mu.Lock()
	defer mu.Unlock()
	if len(actions) != 2 {
		t.Fatalf("expected 2 API calls (start + refresh), got %d: %v", len(actions), actions)
	}
}

func TestStopSession(t *testing.T) {
	var mu sync.Mutex
	var actions []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		actions = append(actions, r.URL.Path)
		mu.Unlock()

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(ScrobbleResponse{})
	}))
	defer server.Close()

	origURL := traktAPIBaseURL
	defer func() { setBaseURL(origURL) }()
	tracker := newTestSetup(t, server.URL)

	update := models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "12345",
		Position:    300,
		Duration:    7200,
		IsPaused:    false,
		ExternalIDs: map[string]string{"tmdb": "12345"},
	}

	// Start watching
	tracker.HandleProgressUpdate("user1", update, 4.17)

	// Stop
	update.Position = 6480
	tracker.StopSession("user1", update, 90.0)

	mu.Lock()
	defer mu.Unlock()
	if len(actions) != 2 {
		t.Fatalf("expected 2 API calls (start, stop), got %d: %v", len(actions), actions)
	}
	if actions[1] != "/scrobble/stop" {
		t.Errorf("expected /scrobble/stop, got %s", actions[1])
	}

	// Verify session removed
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	key := sessionKey("user1", "movie", "12345")
	if _, ok := tracker.sessions[key]; ok {
		t.Error("expected session to be removed after stop")
	}
}

func TestStopSession_NoSession(t *testing.T) {
	var mu sync.Mutex
	var actions []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		actions = append(actions, r.URL.Path)
		mu.Unlock()

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(ScrobbleResponse{})
	}))
	defer server.Close()

	origURL := traktAPIBaseURL
	defer func() { setBaseURL(origURL) }()
	tracker := newTestSetup(t, server.URL)

	// Stop with no existing session — should be a no-op
	update := models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "99999",
		ExternalIDs: map[string]string{"tmdb": "99999"},
	}
	tracker.StopSession("user1", update, 90.0)

	mu.Lock()
	defer mu.Unlock()
	if len(actions) != 0 {
		t.Fatalf("expected no API calls for non-existent session, got %d", len(actions))
	}
}

func TestHandleProgressUpdate_NoTraktAccount(t *testing.T) {
	var mu sync.Mutex
	var actions []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		actions = append(actions, r.URL.Path)
		mu.Unlock()

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(ScrobbleResponse{})
	}))
	defer server.Close()

	origURL := traktAPIBaseURL
	defer func() { setBaseURL(origURL) }()
	tracker := newTestSetup(t, server.URL)

	// User with no Trakt account
	update := models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "12345",
		Position:    300,
		Duration:    7200,
		IsPaused:    false,
		ExternalIDs: map[string]string{"tmdb": "12345"},
	}

	tracker.HandleProgressUpdate("unknown-user", update, 4.17)

	mu.Lock()
	defer mu.Unlock()
	if len(actions) != 0 {
		t.Fatalf("expected no API calls for user without Trakt account, got %d", len(actions))
	}
}

func TestHandleProgressUpdate_EpisodeScrobble(t *testing.T) {
	var receivedBody ScrobbleRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(ScrobbleResponse{Action: "start"})
	}))
	defer server.Close()

	origURL := traktAPIBaseURL
	defer func() { setBaseURL(origURL) }()
	tracker := newTestSetup(t, server.URL)

	update := models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        "tvdb:series:73255:s02e05",
		Position:      600,
		Duration:      2700,
		IsPaused:      false,
		ExternalIDs:   map[string]string{"tvdb": "73255"},
		SeasonNumber:  2,
		EpisodeNumber: 5,
		SeriesID:      "tvdb:series:73255",
		SeriesName:    "Test Show",
		EpisodeName:   "Episode Five",
	}

	tracker.HandleProgressUpdate("user1", update, 22.22)

	if receivedBody.Show == nil {
		t.Fatal("expected show in request body")
	}
	if receivedBody.Show.IDs.TVDB != 73255 {
		t.Errorf("expected show TVDB ID 73255, got %d", receivedBody.Show.IDs.TVDB)
	}
	if receivedBody.Episode == nil {
		t.Fatal("expected episode in request body")
	}
	if receivedBody.Episode.Season != 2 || receivedBody.Episode.Number != 5 {
		t.Errorf("expected S02E05, got S%02dE%02d", receivedBody.Episode.Season, receivedBody.Episode.Number)
	}
}

func TestCleanupStaleSessions(t *testing.T) {
	tracker := &ScrobbleStateTracker{
		sessions:        make(map[string]*scrobbleSession),
		refreshInterval: 15 * time.Minute,
		staleTimeout:    30 * time.Minute,
	}

	// Add a stale session (last call was 31 min ago)
	tracker.sessions["user1:movie:123"] = &scrobbleSession{
		state:         stateWatching,
		lastTraktCall: time.Now().Add(-31 * time.Minute),
	}

	// Add a fresh session
	tracker.sessions["user1:movie:456"] = &scrobbleSession{
		state:         stateWatching,
		lastTraktCall: time.Now(),
	}

	tracker.cleanupStaleSessions()

	if _, ok := tracker.sessions["user1:movie:123"]; ok {
		t.Error("expected stale session to be cleaned up")
	}
	if _, ok := tracker.sessions["user1:movie:456"]; !ok {
		t.Error("expected fresh session to remain")
	}
}

func TestBuildScrobbleRequest_Movie(t *testing.T) {
	update := models.PlaybackProgressUpdate{
		MediaType:   "movie",
		MovieName:   "Test Movie",
		Year:        2024,
		ExternalIDs: map[string]string{"tmdb": "12345", "imdb": "tt1234567"},
	}

	req := buildScrobbleRequest(update, 50.0)

	if req.Movie == nil {
		t.Fatal("expected movie in request")
	}
	if req.Movie.Title != "Test Movie" {
		t.Errorf("expected title 'Test Movie', got %s", req.Movie.Title)
	}
	if req.Movie.IDs.TMDB != 12345 {
		t.Errorf("expected TMDB 12345, got %d", req.Movie.IDs.TMDB)
	}
	if req.Movie.IDs.IMDB != "tt1234567" {
		t.Errorf("expected IMDB tt1234567, got %s", req.Movie.IDs.IMDB)
	}
	if req.Progress != 50.0 {
		t.Errorf("expected progress 50.0, got %f", req.Progress)
	}
	if req.Episode != nil || req.Show != nil {
		t.Error("expected no episode/show for movie scrobble")
	}
}

func TestBuildScrobbleRequest_Episode(t *testing.T) {
	update := models.PlaybackProgressUpdate{
		MediaType:     "episode",
		SeasonNumber:  3,
		EpisodeNumber: 7,
		EpisodeName:   "Episode Title",
		SeriesName:    "Show Name",
		SeriesID:      "tvdb:series:54321",
		ExternalIDs:   map[string]string{"tvdb": "54321"},
	}

	req := buildScrobbleRequest(update, 25.0)

	if req.Episode == nil {
		t.Fatal("expected episode in request")
	}
	if req.Episode.Season != 3 || req.Episode.Number != 7 {
		t.Errorf("expected S03E07, got S%02dE%02d", req.Episode.Season, req.Episode.Number)
	}
	if req.Show == nil {
		t.Fatal("expected show in request")
	}
	if req.Show.IDs.TVDB != 54321 {
		t.Errorf("expected TVDB 54321, got %d", req.Show.IDs.TVDB)
	}
	if req.Movie != nil {
		t.Error("expected no movie for episode scrobble")
	}
}
