package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

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
