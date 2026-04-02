package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"novastream/models"
)

type mockProgressService struct {
	all map[string][]models.PlaybackProgress
}

func (m *mockProgressService) ListAllPlaybackProgress() map[string][]models.PlaybackProgress {
	return m.all
}

func TestGetActiveStreams_PauseDetection(t *testing.T) {
	tmpDir := t.TempDir()
	hlsMgr := NewHLSManager(tmpDir, "", "", nil)
	handler := NewAdminHandler(hlsMgr)

	// Add an active direct stream (recent activity)
	tracker := GetStreamTracker()
	activeReq := httptest.NewRequest(http.MethodGet, "/stream?profileId=user1&profileName=Alice", nil)
	activeID, activeBytes, activeActivity := tracker.StartStream(activeReq, "/media/active-movie.mkv", 1000000, 0, 999999)
	atomic.StoreInt64(activeBytes, 50000)
	atomic.StoreInt64(activeActivity, time.Now().UnixNano())

	// Add a paused direct stream (stale activity > 30s)
	pausedReq := httptest.NewRequest(http.MethodGet, "/stream?profileId=user2&profileName=Bob", nil)
	pausedID, pausedBytes, pausedActivity := tracker.StartStream(pausedReq, "/media/paused-movie.mkv", 1000000, 0, 999999)
	atomic.StoreInt64(pausedBytes, 30000)
	// Set activity to 45 seconds ago to simulate pause (between pauseThreshold=30s and hideThreshold=60s)
	atomic.StoreInt64(pausedActivity, time.Now().Add(-45*time.Second).UnixNano())

	// Add a long-idle direct stream (stale > 5 min, should be hidden)
	idleReq := httptest.NewRequest(http.MethodGet, "/stream?profileId=user3&profileName=Carol", nil)
	idleID, idleBytes, idleActivity := tracker.StartStream(idleReq, "/media/idle-movie.mkv", 1000000, 0, 999999)
	atomic.StoreInt64(idleBytes, 10000)
	// Set activity to 10 minutes ago
	atomic.StoreInt64(idleActivity, time.Now().Add(-10*time.Minute).UnixNano())

	// Make the request
	req := httptest.NewRequest(http.MethodGet, "/api/streams", nil)
	rec := httptest.NewRecorder()
	handler.GetActiveStreams(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp StreamsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Should have 2 streams (active + paused), idle one should be hidden
	if resp.Count != 2 {
		t.Errorf("expected 2 streams, got %d", resp.Count)
	}

	// Find each stream by filename
	var activeStream, pausedStream *StreamInfo
	for i := range resp.Streams {
		switch resp.Streams[i].Filename {
		case "active-movie.mkv":
			activeStream = &resp.Streams[i]
		case "paused-movie.mkv":
			pausedStream = &resp.Streams[i]
		case "idle-movie.mkv":
			t.Error("idle stream should have been hidden (> 5 min idle)")
		}
	}

	if activeStream == nil {
		t.Fatal("active stream not found in response")
	}
	if activeStream.IsPaused {
		t.Error("active stream should not be paused")
	}

	if pausedStream == nil {
		t.Fatal("paused stream not found in response")
	}
	if !pausedStream.IsPaused {
		t.Error("paused stream should be marked as paused")
	}

	// Cleanup
	tracker.EndStream(activeID)
	tracker.EndStream(pausedID)
	tracker.EndStream(idleID)
}

func TestGetActiveStreams_UsesCanonicalMediaMetadataForProgressMatch(t *testing.T) {
	tmpDir := t.TempDir()
	hlsMgr := NewHLSManager(tmpDir, "", "", nil)
	handler := NewAdminHandler(hlsMgr)
	handler.SetProgressService(&mockProgressService{
		all: map[string][]models.PlaybackProgress{
			"user1": {
				{
					ID:             "episode:tvdb:one-piece:S02E02",
					MediaType:      "episode",
					ItemID:         "tvdb:one-piece:S02E02",
					Position:       612,
					Duration:       1440,
					PercentWatched: 42.5,
					UpdatedAt:      time.Now().UTC(),
					SeasonNumber:   2,
					EpisodeNumber:  2,
					SeriesName:     "One Piece",
					EpisodeName:    "The Name of the Episode",
				},
			},
		},
	})

	tracker := GetStreamTracker()
	req := httptest.NewRequest(
		http.MethodGet,
		"/stream?profileId=user1&profileName=Alice&mediaType=episode&itemId=tvdb:one-piece:S02E02&title=One%20Piece&seasonNumber=2&episodeNumber=2&seriesName=One%20Piece&episodeName=The%20Name%20of%20the%20Episode",
		nil,
	)
	streamID, bytesCounter, activityCounter := tracker.StartStream(req, "/media/obfuscated-file-abc123.mkv", 1000000, 0, 999999)
	atomic.StoreInt64(bytesCounter, 50000)
	atomic.StoreInt64(activityCounter, time.Now().UnixNano())
	defer tracker.EndStream(streamID)

	rec := httptest.NewRecorder()
	handler.GetActiveStreams(rec, httptest.NewRequest(http.MethodGet, "/api/streams", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp StreamsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.Streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(resp.Streams))
	}

	stream := resp.Streams[0]
	if stream.Title != "One Piece" {
		t.Fatalf("expected canonical title, got %q", stream.Title)
	}
	if stream.EpisodeName != "The Name of the Episode" {
		t.Fatalf("expected canonical episode title, got %q", stream.EpisodeName)
	}
	if stream.PercentWatched != 42.5 {
		t.Fatalf("expected matched progress 42.5, got %v", stream.PercentWatched)
	}
	if stream.CurrentPosition != 612 {
		t.Fatalf("expected matched position 612, got %v", stream.CurrentPosition)
	}
}

func TestGetActiveStreams_UsesCanonicalMetadataWithoutFilenameInference(t *testing.T) {
	tmpDir := t.TempDir()
	hlsMgr := NewHLSManager(tmpDir, "", "", nil)
	handler := NewAdminHandler(hlsMgr)

	tracker := GetStreamTracker()
	req := httptest.NewRequest(
		http.MethodGet,
		"/stream?profileId=user1&profileName=Alice&mediaType=movie&itemId=tmdb:1234&title=The%20Matrix&movieName=The%20Matrix&year=1999",
		nil,
	)
	streamID, bytesCounter, activityCounter := tracker.StartStream(req, "/media/obfuscated-file-abc123.mkv", 1000000, 0, 999999)
	atomic.StoreInt64(bytesCounter, 50000)
	atomic.StoreInt64(activityCounter, time.Now().UnixNano())
	defer tracker.EndStream(streamID)

	rec := httptest.NewRecorder()
	handler.GetActiveStreams(rec, httptest.NewRequest(http.MethodGet, "/api/streams", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp StreamsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.Streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(resp.Streams))
	}

	stream := resp.Streams[0]
	if stream.Title != "The Matrix" {
		t.Fatalf("expected canonical title, got %q", stream.Title)
	}
	if stream.MediaType != "movie" {
		t.Fatalf("expected media type movie, got %q", stream.MediaType)
	}
	if stream.Filename != "obfuscated-file-abc123.mkv" {
		t.Fatalf("expected original filename to remain for diagnostics, got %q", stream.Filename)
	}
	if stream.PercentWatched != 0 {
		t.Fatalf("expected no inferred progress without a heartbeat row, got %v", stream.PercentWatched)
	}
}

func TestGetActiveStreams_HeartbeatPausedMarksPaused(t *testing.T) {
	tmpDir := t.TempDir()
	hlsMgr := NewHLSManager(tmpDir, "", "", nil)
	handler := NewAdminHandler(hlsMgr)
	handler.SetProgressService(&mockProgressService{
		all: map[string][]models.PlaybackProgress{
			"user1": {
				{
					ID:             "movie:tmdb:1234",
					MediaType:      "movie",
					ItemID:         "tmdb:1234",
					Position:       600,
					Duration:       7200,
					PercentWatched: 8.3,
					UpdatedAt:      time.Now().UTC(),
					IsPaused:       true,
					MovieName:      "The Matrix",
					Year:           1999,
				},
			},
		},
	})

	tracker := GetStreamTracker()
	req := httptest.NewRequest(
		http.MethodGet,
		"/stream?profileId=user1&profileName=Alice&mediaType=movie&itemId=tmdb:1234&title=The%20Matrix&movieName=The%20Matrix&year=1999",
		nil,
	)
	streamID, bytesCounter, activityCounter := tracker.StartStream(req, "/media/obfuscated-file-abc123.mkv", 1000000, 0, 999999)
	atomic.StoreInt64(bytesCounter, 50000)
	atomic.StoreInt64(activityCounter, time.Now().UnixNano())
	defer tracker.EndStream(streamID)

	rec := httptest.NewRecorder()
	handler.GetActiveStreams(rec, httptest.NewRequest(http.MethodGet, "/api/streams", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp StreamsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.Streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(resp.Streams))
	}
	if !resp.Streams[0].IsPaused {
		t.Fatalf("expected stream to be paused from heartbeat payload")
	}
}

func TestGetActiveStreams_HeartbeatAgeRemovesEnded(t *testing.T) {
	tmpDir := t.TempDir()
	hlsMgr := NewHLSManager(tmpDir, "", "", nil)
	handler := NewAdminHandler(hlsMgr)
	handler.SetProgressService(&mockProgressService{
		all: map[string][]models.PlaybackProgress{
			"user1": {
				{
					ID:             "movie:tmdb:1234",
					MediaType:      "movie",
					ItemID:         "tmdb:1234",
					Position:       600,
					Duration:       7200,
					PercentWatched: 8.3,
					UpdatedAt:      time.Now().Add(-26 * time.Second).UTC(),
					MovieName:      "The Matrix",
					Year:           1999,
				},
			},
		},
	})

	tracker := GetStreamTracker()
	req := httptest.NewRequest(
		http.MethodGet,
		"/stream?profileId=user1&profileName=Alice&mediaType=movie&itemId=tmdb:1234&title=The%20Matrix&movieName=The%20Matrix&year=1999",
		nil,
	)
	streamID, bytesCounter, activityCounter := tracker.StartStream(req, "/media/obfuscated-file-abc123.mkv", 1000000, 0, 999999)
	atomic.StoreInt64(bytesCounter, 50000)
	atomic.StoreInt64(activityCounter, time.Now().UnixNano())
	defer tracker.EndStream(streamID)

	rec := httptest.NewRecorder()
	handler.GetActiveStreams(rec, httptest.NewRequest(http.MethodGet, "/api/streams", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp StreamsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.Streams) != 0 {
		t.Fatalf("expected ended stream to be removed, got %d streams", len(resp.Streams))
	}
}
