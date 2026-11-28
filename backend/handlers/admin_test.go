package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
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

func TestAddDashboardWatchSecondsTracksTypeBucketsByRange(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	weekCutoff := now.AddDate(0, 0, -7)
	monthCutoff := now.AddDate(0, -1, 0)
	stats := dashboardWatchTimeStats{}

	addDashboardWatchSeconds(&stats, "movie", 3600, now.AddDate(0, 0, -1), weekCutoff, monthCutoff)
	addDashboardWatchSeconds(&stats, "episode", 1800, now.AddDate(0, 0, -10), weekCutoff, monthCutoff)
	addDashboardWatchSeconds(&stats, "live", 900, now.AddDate(0, -2, 0), weekCutoff, monthCutoff)

	if stats.EverSeconds != 6300 {
		t.Fatalf("expected ever total 6300, got %.0f", stats.EverSeconds)
	}
	if stats.ByRange.Ever.TotalSeconds != 6300 || stats.ByRange.Ever.MovieSeconds != 3600 || stats.ByRange.Ever.EpisodeSeconds != 1800 || stats.ByRange.Ever.LiveSeconds != 900 {
		t.Fatalf("unexpected ever buckets: %+v", stats.ByRange.Ever)
	}
	if stats.MonthSeconds != 5400 {
		t.Fatalf("expected month total 5400, got %.0f", stats.MonthSeconds)
	}
	if stats.ByRange.Month.TotalSeconds != 5400 || stats.ByRange.Month.MovieSeconds != 3600 || stats.ByRange.Month.EpisodeSeconds != 1800 || stats.ByRange.Month.LiveSeconds != 0 {
		t.Fatalf("unexpected month buckets: %+v", stats.ByRange.Month)
	}
	if stats.WeekSeconds != 3600 {
		t.Fatalf("expected week total 3600, got %.0f", stats.WeekSeconds)
	}
	if stats.ByRange.Week.TotalSeconds != 3600 || stats.ByRange.Week.MovieSeconds != 3600 || stats.ByRange.Week.EpisodeSeconds != 0 || stats.ByRange.Week.LiveSeconds != 0 {
		t.Fatalf("unexpected week buckets: %+v", stats.ByRange.Week)
	}
	if stats.MovieSeconds != stats.ByRange.Ever.MovieSeconds || stats.EpisodeSeconds != stats.ByRange.Ever.EpisodeSeconds || stats.LiveSeconds != stats.ByRange.Ever.LiveSeconds {
		t.Fatalf("legacy type totals should mirror ever buckets: stats=%+v ever=%+v", stats, stats.ByRange.Ever)
	}
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

func TestGetActiveStreams_UsesDefaultProfileProgressForWebPlaybackAlias(t *testing.T) {
	tmpDir := t.TempDir()
	hlsMgr := NewHLSManager(tmpDir, "", "", nil)
	handler := NewAdminHandler(hlsMgr)
	tracker := GetStreamTracker()
	for _, active := range tracker.GetActiveStreams() {
		tracker.EndStream(active.ID)
	}
	progress := map[string][]models.PlaybackProgress{
		"default": {
			{
				ID:             "episode:tmdb:tv:37854:s23e10",
				MediaType:      "episode",
				ItemID:         "tmdb:tv:37854:s23e10",
				Position:       398,
				Duration:       1415,
				PercentWatched: 28.1,
				UpdatedAt:      time.Now().UTC(),
				ExternalIDs: map[string]string{
					"imdb":    "tt0388629",
					"titleId": "tmdb:tv:37854",
					"tvdb":    "81797",
				},
				SeasonNumber:  23,
				EpisodeNumber: 10,
				SeriesID:      "tmdb:tv:37854",
				SeriesName:    "One Piece",
				EpisodeName:   "A Welcome with Friends' Cups and Intruders Seeking Loki",
			},
		},
	}
	handler.SetProgressService(&mockProgressService{
		all: progress,
	})

	req := httptest.NewRequest(
		http.MethodGet,
		"/stream?profileId=f2bbe488-7f3b-430e-9e35-1d5c13d6159d&profileName=godver3&mediaType=episode&itemId=tmdb:tv:37854&title=One%20Piece&seasonNumber=23&episodeNumber=10&seriesId=tmdb:tv:37854&seriesName=One%20Piece&episodeName=A%20Welcome%20with%20Friends%27%20Cups%20and%20Intruders%20Seeking%20Loki&imdb=tt0388629&tvdb=81797",
		nil,
	)
	streamID, bytesCounter, activityCounter := tracker.StartStream(req, "/media/one-piece-obfuscated.mkv", 1000000, 0, 999999)
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
	if stream.CurrentPosition != 398 {
		t.Fatalf("expected default-profile progress position 398, got %v", stream.CurrentPosition)
	}
	if stream.PercentWatched != 28.1 {
		t.Fatalf("expected default-profile progress percent 28.1, got %v", stream.PercentWatched)
	}
	if stream.Duration != 1415 {
		t.Fatalf("expected default-profile progress duration 1415, got %v", stream.Duration)
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
	// Both the progress heartbeat and the transport are stale: the stream has
	// genuinely ended and should be removed.
	atomic.StoreInt64(activityCounter, time.Now().Add(-61*time.Second).UnixNano())
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

// TestGetActiveStreams_StaleHeartbeatActiveTransportKept covers the case where a
// client stops sending progress heartbeats after crossing the 90% watched
// threshold but is still actively streaming. The stream must remain visible (so
// the dashboard keeps advancing via interpolation) rather than freezing or
// disappearing.
func TestGetActiveStreams_StaleHeartbeatActiveTransportKept(t *testing.T) {
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
					Position:       6600,
					Duration:       7200,
					PercentWatched: 91.6,
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
	// Heartbeat is stale (past the watched threshold) but transport is fresh.
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
		t.Fatalf("expected still-streaming entry to be kept, got %d streams", len(resp.Streams))
	}
	if resp.Streams[0].IsPaused {
		t.Fatalf("expected actively-streaming entry to not be marked paused")
	}
}

func TestRestartServer_RespondsAcceptedAndSignals(t *testing.T) {
	hlsMgr := NewHLSManager(t.TempDir(), "", "", nil)
	handler := NewAdminHandler(hlsMgr)

	// Stub signalSelf so the test runner is not actually terminated.
	gotSignal := make(chan os.Signal, 1)
	orig := signalSelf
	signalSelf = func(sig os.Signal) error {
		gotSignal <- sig
		return nil
	}
	defer func() { signalSelf = orig }()

	req := httptest.NewRequest(http.MethodPost, "/api/admin/restart", nil)
	rec := httptest.NewRecorder()
	handler.RestartServer(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["status"] != "restarting" {
		t.Errorf("expected status 'restarting', got %v", resp["status"])
	}

	// The signal is sent asynchronously after a short delay.
	select {
	case sig := <-gotSignal:
		if sig != syscall.SIGTERM {
			t.Errorf("expected SIGTERM, got %v", sig)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected process to be signalled, but it was not")
	}
}

// Native players read in short range requests with idle gaps between them.
// ActiveStreams (behind /api/homepage) must keep such a playback listed
// through a gap while its heartbeat is fresh, as the admin dashboard does.
func TestActiveStreams_ListsLingeringStreamWithFreshHeartbeat(t *testing.T) {
	for _, tc := range []struct {
		name      string
		heartbeat time.Duration
		want      int
	}{
		{name: "fresh heartbeat", heartbeat: -2 * time.Second, want: 1},
		{name: "stale heartbeat", heartbeat: -40 * time.Second, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			itemID := "tmdb:lingering-" + strings.ReplaceAll(tc.name, " ", "-")
			handler := NewAdminHandler(NewHLSManager(t.TempDir(), "", "", nil))
			handler.SetProgressService(&mockProgressService{
				all: map[string][]models.PlaybackProgress{
					"kids": {{
						ID:        "movie:" + itemID,
						MediaType: "movie",
						ItemID:    itemID,
						Position:  600,
						Duration:  7200,
						UpdatedAt: time.Now().Add(tc.heartbeat).UTC(),
						MovieName: "Frozen",
					}},
				},
			})

			tracker := GetStreamTracker()
			req := httptest.NewRequest(http.MethodGet,
				"/stream?profileId=kids&profileName=Kids&mediaType=movie&itemId="+itemID+"&title=Frozen&movieName=Frozen",
				nil)
			streamID, bytesCounter, _ := tracker.StartStream(req, "/media/"+itemID+".mkv", 1000000, 0, 999999)
			atomic.StoreInt64(bytesCounter, 50000)
			tracker.EndStream(streamID) // range request finished; playback continues
			defer tracker.TerminateStream(streamID)

			got := 0
			for _, s := range handler.ActiveStreams().Streams {
				if s.ItemID == itemID {
					got++
				}
			}
			if got != tc.want {
				t.Fatalf("ActiveStreams() listed %d streams for the lingering playback, want %d", got, tc.want)
			}
		})
	}
}
