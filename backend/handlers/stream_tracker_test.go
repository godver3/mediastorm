package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"novastream/internal/auth"
	"novastream/models"
)

func newTestTracker() *StreamTracker {
	return &StreamTracker{
		streams:          make(map[string]*TrackedStream),
		stopPlaybacks:    make(map[string]time.Time),
		migrationSignals: make(map[string]playbackMigrationSignal),
	}
}

func TestStartStreamTracksClientID(t *testing.T) {
	tracker := newTestTracker()
	req := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1&clientId=iphone-client", nil)
	id, _, _ := tracker.StartStreamWithAccount(req, "/test/file.mkv", 1000, 0, 0, "acct1")

	stream, ok := tracker.GetStream(id)
	if !ok || stream == nil {
		t.Fatal("expected tracked stream")
	}
	if stream.ClientID != "iphone-client" {
		t.Fatalf("ClientID = %q, want iphone-client", stream.ClientID)
	}
}

func TestPlaybackHeartbeatAssociatesClientID(t *testing.T) {
	tracker := newTestTracker()
	req := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1&mediaType=movie&itemId=tmdb:movie:17200", nil)
	id, _, _ := tracker.StartStreamWithAccount(req, "/test/file.mkv", 1000, 0, 0, "acct1")

	matched := tracker.AssociateClientWithPlayback("p1", models.PlaybackProgressUpdate{
		MediaType: "movie",
		ItemID:    "tmdb:movie:17200",
	}, "iphone-client")
	if matched != 1 {
		t.Fatalf("matched = %d, want 1", matched)
	}
	stream, ok := tracker.GetStream(id)
	if !ok || stream == nil {
		t.Fatal("expected tracked stream")
	}
	if stream.ClientID != "iphone-client" {
		t.Fatalf("ClientID = %q, want iphone-client", stream.ClientID)
	}
}

func TestUpstreamStarvationWaitsForPlayerBufferPressure(t *testing.T) {
	tracker := newTestTracker()
	req := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1&mediaType=movie&itemId=tmdb:movie:14160", nil)
	id, _, _ := tracker.StartStreamWithAccount(req, "/webdav/nzbs/up/up.mkv", 1000, 0, 0, "acct1")

	if !tracker.MarkPlaybackMigration(id, "backend-starvation") {
		t.Fatal("expected active playback migration signal to be recorded")
	}

	healthyRunway := 20.0
	if reason, migrate := tracker.ShouldMigratePlayback("p1", models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "tmdb:movie:14160",
		IsPaused:    true,
		BufferAhead: &healthyRunway,
	}); migrate {
		t.Fatalf("paused playback consumed confirmed migration signal: reason=%q", reason)
	}

	if reason, migrate := tracker.ShouldMigratePlayback("p1", models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "tmdb:movie:14160",
		BufferAhead: &healthyRunway,
	}); migrate {
		t.Fatalf("transient upstream stall migrated with healthy runway: reason=%q", reason)
	}

	preparationRunway := 10.0
	if reason, prepare := tracker.ShouldPreparePlaybackMigration("p1", models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "tmdb:movie:14160",
		Position:    40,
		BufferAhead: &preparationRunway,
	}); !prepare || reason != "backend-starvation" {
		t.Fatalf("shrinking runway did not expose preparation signal: prepare=%v reason=%q", prepare, reason)
	}

	// A low runway is normal for native players (~16 MB ahead), so it must not
	// switch a source that is still playing; only a real stall does.
	lowRunway := 4.0
	if reason, migrate := tracker.ShouldMigratePlayback("p1", models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "tmdb:movie:14160",
		Position:    41,
		BufferAhead: &lowRunway,
	}); migrate {
		t.Fatalf("low runway without a stall migrated: reason=%q", reason)
	}
	reason, migrate := tracker.ShouldMigratePlayback("p1", models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "tmdb:movie:14160",
		Position:    42,
		BufferAhead: &lowRunway,
		IsBuffering: true,
	})
	if !migrate || reason != "backend-starvation" {
		t.Fatalf("stalled playback did not consume upstream stall: migrate=%v reason=%q", migrate, reason)
	}

	if _, migrate := tracker.ShouldMigratePlayback("p1", models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "tmdb:movie:14160",
		IsBuffering: true,
	}); migrate {
		t.Fatal("migration signal should be consumed once")
	}
}

func TestWeakerMigrationSignalDoesNotOverwriteConfirmedStarvation(t *testing.T) {
	tracker := newTestTracker()
	req := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1&mediaType=movie&itemId=tmdb:movie:14160", nil)
	id, _, _ := tracker.StartStreamWithAccount(req, "/webdav/nzbs/up/up.mkv", 1000, 0, 0, "acct1")

	if !tracker.MarkPlaybackMigration(id, "backend-starvation") {
		t.Fatal("expected confirmed starvation signal")
	}
	if !tracker.MarkPlaybackMigration(id, "backend-low-throughput") {
		t.Fatal("expected later low-throughput observation to match playback")
	}

	healthyRunway := 20.0
	if reason, migrate := tracker.ShouldMigratePlayback("p1", models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "tmdb:movie:14160",
		Position:    40,
		BufferAhead: &healthyRunway,
	}); migrate {
		t.Fatalf("healthy runway consumed confirmed starvation: reason=%q", reason)
	}
	criticalRunway := 4.0
	reason, migrate := tracker.ShouldMigratePlayback("p1", models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "tmdb:movie:14160",
		Position:    41,
		BufferAhead: &criticalRunway,
		IsBuffering: true,
	})
	if !migrate || reason != "backend-starvation" {
		t.Fatalf("weaker signal replaced confirmed starvation: migrate=%v reason=%q", migrate, reason)
	}
}

func TestPredictiveMigrationWaitsForBufferPressure(t *testing.T) {
	tracker := newTestTracker()
	req := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1&mediaType=movie&itemId=tmdb:movie:14160", nil)
	id, _, _ := tracker.StartStreamWithAccount(req, "/webdav/nzbs/up/up.mkv", 1000, 0, 0, "acct1")

	if !tracker.MarkPlaybackMigration(id, "backend-low-throughput") {
		t.Fatal("expected active playback migration signal to be recorded")
	}

	healthyRunway := 20.0
	if reason, migrate := tracker.ShouldMigratePlayback("p1", models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "tmdb:movie:14160",
		Position:    40,
		BufferAhead: &healthyRunway,
	}); migrate {
		t.Fatalf("healthy buffer consumed predictive migration signal: reason=%q", reason)
	}

	criticalRunway := 4.0
	reason, migrate := tracker.ShouldMigratePlayback("p1", models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "tmdb:movie:14160",
		Position:    41,
		BufferAhead: &criticalRunway,
		IsBuffering: true,
	})
	if !migrate || reason != "backend-low-throughput" {
		t.Fatalf("critical buffer did not receive migration signal: migrate=%v reason=%q", migrate, reason)
	}
}

func TestUpstreamThroughputArmsPredictiveMigrationAfterSustainedUnderflow(t *testing.T) {
	tracker := newTestTracker()
	path := "/webdav/nzbs/up/up.mkv"
	req := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1&mediaType=movie&itemId=tmdb:movie:14160", nil)
	id, _, _ := tracker.StartStreamWithAccount(req, path, 1000, 0, 0, "acct1")
	requiredMbps := 40.0
	if matched := tracker.ObservePlaybackBandwidth("p1", models.PlaybackProgressUpdate{
		MediaType:    "movie",
		ItemID:       "tmdb:movie:14160",
		SourcePath:   path,
		RequiredMbps: &requiredMbps,
	}); matched != 1 {
		t.Fatalf("matched streams = %d, want 1", matched)
	}

	// 10 MiB served over four seconds is about 21 Mbps, well below the
	// 50 Mbps requirement including migration headroom.
	if tracker.ObserveUpstreamThroughput(id, 10*1024*1024, 4*time.Second) {
		t.Fatal("single low window should not arm migration")
	}
	if !tracker.ObserveUpstreamThroughput(id, 10*1024*1024, 4*time.Second) {
		t.Fatal("second consecutive low window should arm migration")
	}

	runway := 10.0
	if reason, prepare := tracker.ShouldPreparePlaybackMigration("p1", models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "tmdb:movie:14160",
		Position:    40,
		SourcePath:  path,
		BufferAhead: &runway,
	}); !prepare || reason != "backend-low-throughput" {
		t.Fatalf("predictive signal did not request preparation: prepare=%v reason=%q", prepare, reason)
	}

	if reason, migrate := tracker.ShouldMigratePlayback("p1", models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "tmdb:movie:14160",
		Position:    41,
		SourcePath:  path,
		IsBuffering: true,
	}); !migrate || reason != "backend-low-throughput" {
		t.Fatalf("buffering did not consume predictive signal: migrate=%v reason=%q", migrate, reason)
	}
}

func TestUpstreamThroughputHealthyWindowResetsUnderflowAndBandwidthIsSourceScoped(t *testing.T) {
	tracker := newTestTracker()
	requestURL := "/video/stream?profileId=p1&mediaType=movie&itemId=tmdb:movie:14160"
	oldPath := "/webdav/nzbs/up/old.mkv"
	newPath := "/debrid/torbox/new/file/0/new.mkv"
	oldID, _, _ := tracker.StartStreamWithAccount(httptest.NewRequest(http.MethodGet, requestURL, nil), oldPath, 1000, 0, 0, "acct1")
	newID, _, _ := tracker.StartStreamWithAccount(httptest.NewRequest(http.MethodGet, requestURL, nil), newPath, 1000, 0, 0, "acct1")
	requiredMbps := 20.0
	if matched := tracker.ObservePlaybackBandwidth("p1", models.PlaybackProgressUpdate{
		MediaType:    "movie",
		ItemID:       "tmdb:movie:14160",
		SourcePath:   newPath,
		RequiredMbps: &requiredMbps,
	}); matched != 1 {
		t.Fatalf("matched streams = %d, want only replacement source", matched)
	}
	if tracker.ObserveUpstreamThroughput(oldID, 1024, 5*time.Second) {
		t.Fatal("old source inherited replacement bitrate")
	}

	if tracker.ObserveUpstreamThroughput(newID, 5*1024*1024, 4*time.Second) {
		t.Fatal("first low window should not signal")
	}
	if tracker.ObserveUpstreamThroughput(newID, 20*1024*1024, time.Second) {
		t.Fatal("healthy source window should reset rather than signal")
	}
	if tracker.ObserveUpstreamThroughput(newID, 5*1024*1024, 4*time.Second) {
		t.Fatal("low window after a healthy reset should again be the first sample")
	}
}

func TestPlaybackMigrationForPathTriggersOnBuffering(t *testing.T) {
	tracker := newTestTracker()
	req := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1&mediaType=movie&itemId=tmdb:movie:14160", nil)
	tracker.StartStreamWithAccount(req, "/webdav/nzbs/up/up.mkv", 1000, 0, 0, "acct1")

	if marked := tracker.MarkPlaybackMigrationForPath("webdav/nzbs/up/up.mkv", "backend-starvation"); marked != 1 {
		t.Fatalf("marked playbacks = %d, want 1", marked)
	}
	reason, migrate := tracker.ShouldMigratePlayback("p1", models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "tmdb:movie:14160",
		IsBuffering: true,
	})
	if !migrate || reason != "backend-starvation" {
		t.Fatalf("buffering playback did not receive path migration: migrate=%v reason=%q", migrate, reason)
	}
}

func TestTerminalSourceFailureMigratesWithoutWaitingForBufferPressure(t *testing.T) {
	tracker := newTestTracker()
	req := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1&mediaType=movie&itemId=tmdb:movie:14160", nil)
	id, _, _ := tracker.StartStreamWithAccount(req, "/webdav/nzbs/up/up.mkv", 1000, 0, 0, "acct1")
	if !tracker.MarkPlaybackMigration(id, "backend-source-failure") {
		t.Fatal("expected terminal source failure signal")
	}
	healthyRunway := 20.0
	reason, migrate := tracker.ShouldMigratePlayback("p1", models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "tmdb:movie:14160",
		Position:    40,
		BufferAhead: &healthyRunway,
	})
	if !migrate || reason != "backend-source-failure" {
		t.Fatalf("terminal source failure was not actionable: migrate=%v reason=%q", migrate, reason)
	}
}

func TestTransientProviderOutageMigratesWithoutBecomingContentFailure(t *testing.T) {
	tracker := newTestTracker()
	req := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1&mediaType=movie&itemId=tmdb:movie:14160", nil)
	id, _, _ := tracker.StartStreamWithAccount(req, "/debrid/torbox/123/file/0/title.mkv", 1000, 0, 0, "acct1")
	if !tracker.MarkPlaybackMigration(id, "backend-provider-unavailable") {
		t.Fatal("expected transient provider outage signal")
	}
	healthyRunway := 20.0
	reason, migrate := tracker.ShouldMigratePlayback("p1", models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "tmdb:movie:14160",
		Position:    40,
		BufferAhead: &healthyRunway,
	})
	if !migrate || reason != "backend-provider-unavailable" {
		t.Fatalf("provider outage was not immediately actionable: migrate=%v reason=%q", migrate, reason)
	}
}

func TestTerminalSourceFailureOutranksProviderOutageInEitherOrder(t *testing.T) {
	for _, reasons := range [][]string{
		{"backend-source-failure", "backend-provider-unavailable"},
		{"backend-provider-unavailable", "backend-source-failure"},
	} {
		tracker := newTestTracker()
		path := "/debrid/torbox/123/file/0/title.mkv"
		req := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1&mediaType=movie&itemId=tmdb:movie:14160", nil)
		id, _, _ := tracker.StartStreamWithAccount(req, path, 1000, 0, 0, "acct1")
		for _, reason := range reasons {
			if !tracker.MarkPlaybackMigration(id, reason) {
				t.Fatalf("MarkPlaybackMigration(%q) = false", reason)
			}
		}
		healthyRunway := 20.0
		reason, migrate := tracker.ShouldMigratePlayback("p1", models.PlaybackProgressUpdate{
			MediaType:   "movie",
			ItemID:      "tmdb:movie:14160",
			SourcePath:  path,
			BufferAhead: &healthyRunway,
		})
		if !migrate || reason != "backend-source-failure" {
			t.Fatalf("signals %v resolved to migrate=%v reason=%q, want backend-source-failure", reasons, migrate, reason)
		}
	}
}

func TestPlaybackMigrationSignalIsScopedToSource(t *testing.T) {
	tracker := newTestTracker()
	requestURL := "/video/stream?profileId=p1&mediaType=movie&itemId=tmdb:movie:14160"
	oldID, _, _ := tracker.StartStreamWithAccount(
		httptest.NewRequest(http.MethodGet, requestURL, nil),
		"/webdav/nzbs/up/old.mkv",
		1000,
		0,
		0,
		"acct1",
	)
	if !tracker.MarkPlaybackMigration(oldID, "backend-low-throughput") {
		t.Fatal("expected old source migration signal")
	}

	if reason, prepare := tracker.ShouldPreparePlaybackMigration("p1", models.PlaybackProgressUpdate{
		MediaType:  "movie",
		ItemID:     "tmdb:movie:14160",
		Position:   40,
		SourcePath: "/debrid/torbox/123/file/0/new.mkv",
	}); prepare {
		t.Fatalf("old source signal leaked to replacement: reason=%q", reason)
	}
	if reason, prepare := tracker.ShouldPreparePlaybackMigration("p1", models.PlaybackProgressUpdate{
		MediaType:  "movie",
		ItemID:     "tmdb:movie:14160",
		Position:   40,
		SourcePath: "/webdav/nzbs/up/old.mkv",
		BufferAhead: func() *float64 {
			value := 10.0
			return &value
		}(),
	}); !prepare || reason != "backend-low-throughput" {
		t.Fatalf("matching source did not receive signal: prepare=%v reason=%q", prepare, reason)
	}
}

func TestPlaybackMigrationPreparationDoesNotConsumeSignal(t *testing.T) {
	tracker := newTestTracker()
	req := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1&mediaType=movie&itemId=tmdb:movie:14160", nil)
	id, _, _ := tracker.StartStreamWithAccount(req, "/webdav/nzbs/up/up.mkv", 1000, 0, 0, "acct1")
	if !tracker.MarkPlaybackMigration(id, "backend-low-throughput") {
		t.Fatal("expected migration signal")
	}
	bufferAhead := 20.0
	if reason, migrate := tracker.ShouldMigratePlayback("p1", models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "tmdb:movie:14160",
		Position:    39,
		BufferAhead: &bufferAhead,
	}); migrate {
		t.Fatalf("healthy playback should prepare rather than migrate: reason=%q", reason)
	}

	reason, prepare := tracker.ShouldPreparePlaybackMigration("p1", models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "tmdb:movie:14160",
		Position:    40,
		BufferAhead: &bufferAhead,
	})
	if prepare {
		t.Fatalf("healthy runway unexpectedly prepared: reason=%q", reason)
	}

	preparationRunway := 10.0
	reason, prepare = tracker.ShouldPreparePlaybackMigration("p1", models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "tmdb:movie:14160",
		Position:    40,
		BufferAhead: &preparationRunway,
	})
	if !prepare || reason != "backend-low-throughput" {
		t.Fatalf("prepare=%v reason=%q", prepare, reason)
	}

	reason, migrate := tracker.ShouldMigratePlayback("p1", models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "tmdb:movie:14160",
		Position:    41,
		IsBuffering: true,
	})
	if !migrate || reason != "backend-low-throughput" {
		t.Fatalf("preparation consumed signal: migrate=%v reason=%q", migrate, reason)
	}
}

func TestPlaybackMigrationPreparesWhenNativeBufferRunwayIsUnavailable(t *testing.T) {
	tracker := newTestTracker()
	path := "/webdav/nzbs/up/up.mkv"
	req := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1&mediaType=movie&itemId=tmdb:movie:14160", nil)
	id, _, _ := tracker.StartStreamWithAccount(req, path, 1000, 0, 0, "acct1")
	if !tracker.MarkPlaybackMigration(id, "backend-starvation") {
		t.Fatal("expected starvation signal")
	}
	if reason, prepare := tracker.ShouldPreparePlaybackMigration("p1", models.PlaybackProgressUpdate{
		MediaType:  "movie",
		ItemID:     "tmdb:movie:14160",
		Position:   40,
		SourcePath: path,
	}); !prepare || reason != "backend-starvation" {
		t.Fatalf("unknown native runway did not prepare: prepare=%v reason=%q", prepare, reason)
	}
	if reason, migrate := tracker.ShouldMigratePlayback("p1", models.PlaybackProgressUpdate{
		MediaType:  "movie",
		ItemID:     "tmdb:movie:14160",
		Position:   41,
		SourcePath: path,
	}); migrate {
		t.Fatalf("preparation without buffer pressure migrated immediately: reason=%q", reason)
	}
}

func startTestStream(t *testing.T, tracker *StreamTracker, profileID, accountID string) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/video/stream?profileId="+profileID, nil)
	path := "/test/file-" + string(rune('a'+tracker.Count())) + ".mkv"
	id, _, _ := tracker.StartStreamWithAccount(r, path, 1000, 0, 0, accountID)
	return id
}

func TestStartStreamMarksShareLinkScopedSessions(t *testing.T) {
	tracker := newTestTracker()

	// A request carrying a stream-scoped (share link) session is flagged.
	shareReq := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1", nil)
	shareReq = shareReq.WithContext(context.WithValue(shareReq.Context(),
		auth.ContextKeySession, models.Session{Scope: models.SessionScopeStream}))
	shareID, _, _ := tracker.StartStreamWithAccount(shareReq, "/test/shared.mkv", 1000, 0, 0, "acct1")

	// A normal full-access session request is not flagged.
	normalReq := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p2", nil)
	normalReq = normalReq.WithContext(context.WithValue(normalReq.Context(),
		auth.ContextKeySession, models.Session{}))
	normalID, _, _ := tracker.StartStreamWithAccount(normalReq, "/test/normal.mkv", 1000, 0, 0, "acct1")

	shared, ok := tracker.GetStream(shareID)
	if !ok || !shared.ViaShareLink {
		t.Fatalf("expected share-scoped stream to be flagged ViaShareLink, got ok=%v stream=%+v", ok, shared)
	}

	normal, ok := tracker.GetStream(normalID)
	if !ok || normal.ViaShareLink {
		t.Fatalf("expected normal stream not to be flagged ViaShareLink, got ok=%v stream=%+v", ok, normal)
	}

	for _, s := range tracker.GetActiveStreams() {
		if s.ID == shareID && !s.ViaShareLink {
			t.Errorf("GetActiveStreams dropped ViaShareLink flag for shared stream")
		}
		if s.ID == normalID && s.ViaShareLink {
			t.Errorf("GetActiveStreams set ViaShareLink flag for normal stream")
		}
	}
}

func TestUpdateSharePlaybackProgressTracksLivePosition(t *testing.T) {
	tracker := newTestTracker()
	req := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1&profileName=Shared&mediaType=movie&itemId=tmdb:movie:123", nil)
	req = req.WithContext(context.WithValue(req.Context(),
		auth.ContextKeySession, models.Session{Scope: models.SessionScopeStream}))
	id, _, _ := tracker.StartStreamWithAccount(req, "/test/shared.mkv", 1000, 0, 999, "acct1")

	matched := tracker.UpdateSharePlaybackProgress("p1", "Shared", models.PlaybackProgressUpdate{
		MediaType: "movie",
		ItemID:    "tmdb:movie:123",
		Position:  125,
		Duration:  1500,
	})
	if matched != 1 {
		t.Fatalf("expected 1 matched share stream, got %d", matched)
	}

	stream, ok := tracker.GetStream(id)
	if !ok {
		t.Fatal("expected stream to still be active")
	}
	if stream.SharePosition != 125 || stream.ShareDuration != 1500 {
		t.Fatalf("unexpected share progress: position=%v duration=%v", stream.SharePosition, stream.ShareDuration)
	}
	if stream.SharePercent < 8.3 || stream.SharePercent > 8.4 {
		t.Fatalf("unexpected share percent: %v", stream.SharePercent)
	}
	if stream.ShareUpdatedAt.IsZero() {
		t.Fatal("expected share updated timestamp")
	}
}

func TestGetAccountStreamUsage(t *testing.T) {
	tracker := newTestTracker()

	// No streams — usage should be 0/3
	usage := tracker.GetAccountStreamUsage("acct1", 3)
	if usage.CurrentStreams != 0 || usage.MaxStreams != 3 || usage.AvailableStreams != 3 || usage.AtLimit {
		t.Fatalf("expected 0/3 not at limit, got %+v", usage)
	}

	// Start 2 streams for acct1
	id1 := startTestStream(t, tracker, "p1", "acct1")
	_ = startTestStream(t, tracker, "p2", "acct1")

	usage = tracker.GetAccountStreamUsage("acct1", 3)
	if usage.CurrentStreams != 2 || usage.AvailableStreams != 1 || usage.AtLimit {
		t.Fatalf("expected 2/3 not at limit, got %+v", usage)
	}

	// Start a 3rd — should be at limit
	_ = startTestStream(t, tracker, "p1", "acct1")
	usage = tracker.GetAccountStreamUsage("acct1", 3)
	if usage.CurrentStreams != 3 || usage.AvailableStreams != 0 || !usage.AtLimit {
		t.Fatalf("expected 3/3 at limit, got %+v", usage)
	}

	// End one stream — should no longer be at limit
	tracker.EndStream(id1)
	usage = tracker.GetAccountStreamUsage("acct1", 3)
	if usage.CurrentStreams != 2 || usage.AvailableStreams != 1 || usage.AtLimit {
		t.Fatalf("after end: expected 2/3, got %+v", usage)
	}
}

func TestGetProfileStreamUsage(t *testing.T) {
	tracker := newTestTracker()

	// Start 2 streams for profile "p1"
	_ = startTestStream(t, tracker, "p1", "acct1")
	_ = startTestStream(t, tracker, "p1", "acct1")
	// 1 stream for profile "p2" (same account)
	_ = startTestStream(t, tracker, "p2", "acct1")

	usage := tracker.GetProfileStreamUsage("p1", 2)
	if usage.CurrentStreams != 2 || !usage.AtLimit {
		t.Fatalf("expected 2/2 at limit for p1, got %+v", usage)
	}

	usage = tracker.GetProfileStreamUsage("p2", 2)
	if usage.CurrentStreams != 1 || usage.AtLimit {
		t.Fatalf("expected 1/2 not at limit for p2, got %+v", usage)
	}
}

func TestStreamUsageUnlimited(t *testing.T) {
	tracker := newTestTracker()

	_ = startTestStream(t, tracker, "p1", "acct1")
	_ = startTestStream(t, tracker, "p1", "acct1")

	// maxStreams=0 means unlimited
	usage := tracker.GetAccountStreamUsage("acct1", 0)
	if usage.CurrentStreams != 2 || usage.AtLimit || usage.AvailableStreams != 0 {
		t.Fatalf("unlimited should never be at limit, got %+v", usage)
	}
}

func TestMarkStopPlaybackStopsMatchingHeartbeat(t *testing.T) {
	tracker := newTestTracker()
	req := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=default&profileName=godver3&mediaType=episode&itemId=tvdb:series:392276:s02e05", nil)
	id, _, _ := tracker.StartStreamWithAccount(req, "/test/file.mkv", 1000, 0, 0, "acct1")

	if !tracker.MarkStopPlayback(id) {
		t.Fatal("expected playback stop signal to be marked")
	}

	update := models.PlaybackProgressUpdate{MediaType: "episode", ItemID: "tvdb:series:392276:s02e05"}
	if !tracker.ShouldStopPlayback("godver3", update) {
		t.Fatal("expected matching heartbeat to be told to stop")
	}

	other := models.PlaybackProgressUpdate{MediaType: "episode", ItemID: "tvdb:series:392276:s02e06"}
	if tracker.ShouldStopPlayback("godver3", other) {
		t.Fatal("did not expect different media item to be stopped")
	}
}

func TestStreamUsageCrossAccount(t *testing.T) {
	tracker := newTestTracker()

	// Streams from different accounts shouldn't interfere
	_ = startTestStream(t, tracker, "p1", "acct1")
	_ = startTestStream(t, tracker, "p2", "acct1")
	_ = startTestStream(t, tracker, "p3", "acct2")

	usage1 := tracker.GetAccountStreamUsage("acct1", 2)
	if usage1.CurrentStreams != 2 || !usage1.AtLimit {
		t.Fatalf("acct1 expected 2/2 at limit, got %+v", usage1)
	}

	usage2 := tracker.GetAccountStreamUsage("acct2", 2)
	if usage2.CurrentStreams != 1 || usage2.AtLimit {
		t.Fatalf("acct2 expected 1/2 not at limit, got %+v", usage2)
	}
}

func TestStreamUsageCollapsesRangeRequestsForSamePlayback(t *testing.T) {
	tracker := newTestTracker()

	req1 := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1&profileName=User&mediaType=episode&itemId=tvdb:series:1:s01e01", nil)
	req1.Header.Set("Range", "bytes=0-4194303")
	id1, _, _ := tracker.StartStreamWithAccount(req1, "/test/file.mkv", 4194304, 0, 4194303, "acct1")
	defer tracker.EndStream(id1)

	req2 := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1&profileName=User&mediaType=episode&itemId=tvdb:series:1:s01e01", nil)
	req2.Header.Set("Range", "bytes=4194304-8388607")
	id2, _, _ := tracker.StartStreamWithAccount(req2, "/test/file.mkv", 4194304, 4194304, 8388607, "acct1")
	defer tracker.EndStream(id2)

	accountUsage := tracker.GetAccountStreamUsage("acct1", 1)
	if accountUsage.CurrentStreams != 1 || !accountUsage.AtLimit {
		t.Fatalf("same playback should count as one account slot at 1/1, got %+v", accountUsage)
	}

	profileUsage := tracker.GetProfileStreamUsage("p1", 1)
	if profileUsage.CurrentStreams != 1 || !profileUsage.AtLimit {
		t.Fatalf("same playback should count as one profile slot at 1/1, got %+v", profileUsage)
	}

	_, exceeds := tracker.WouldExceedAccountLimit(req2, "/test/file.mkv", "acct1", 1)
	if exceeds {
		t.Fatal("same playback range request should not exceed account limit")
	}
}

func TestStreamUsageRejectsNewPlaybackWhenSlotLimitReached(t *testing.T) {
	tracker := newTestTracker()

	activeReq := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1&mediaType=episode&itemId=tvdb:series:1:s01e01", nil)
	activeID, _, _ := tracker.StartStreamWithAccount(activeReq, "/test/file1.mkv", 1000, 0, 999, "acct1")
	defer tracker.EndStream(activeID)

	nextReq := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1&mediaType=episode&itemId=tvdb:series:1:s01e02", nil)
	usage, exceeds := tracker.WouldExceedAccountLimit(nextReq, "/test/file2.mkv", "acct1", 1)
	if !exceeds {
		t.Fatal("different playback should exceed account limit")
	}
	if usage.CurrentStreams != 1 || usage.MaxStreams != 1 || !usage.AtLimit {
		t.Fatalf("expected 1/1 at limit, got %+v", usage)
	}
}

func TestMergeDashboardStreamRowsCollapsesNativeConnections(t *testing.T) {
	base := time.Date(2026, 6, 22, 22, 31, 0, 0, time.UTC)
	// Mirrors a real capture: one profile, one item (Pressure), several KSPlayer
	// byte-range connections registered as separate tracked streams.
	rows := []map[string]interface{}{
		{
			"id": "J7", "type": "direct", "profile_id": "9c234ec9", "profile_name": "Primary Profile",
			"client_ip": "127.0.0.1", "media_type": "movie", "item_id": "tvdb:movie:358587",
			"bytes_streamed": int64(1000), "throughput_bps": int64(500), "last_access": base,
		},
		{
			"id": "Z3", "type": "direct", "profile_id": "9c234ec9", "profile_name": "Primary Profile",
			"client_ip": "127.0.0.1", "media_type": "movie", "item_id": "tvdb:movie:358587",
			"bytes_streamed": int64(20), "throughput_bps": int64(0), "last_access": base.Add(24 * time.Second),
		},
		{
			"id": "A4", "type": "direct", "profile_id": "9c234ec9", "profile_name": "Primary Profile",
			"client_ip": "127.0.0.1", "media_type": "movie", "item_id": "tvdb:movie:358587",
			"bytes_streamed": int64(3000), "throughput_bps": int64(800), "last_access": base.Add(25 * time.Second),
			"via_share_link": true,
		},
		// Different profile, same title -> stays a separate row.
		{
			"id": "X9", "type": "direct", "profile_id": "df1382af", "profile_name": "Amrit",
			"client_ip": "10.0.0.5", "media_type": "movie", "item_id": "tvdb:movie:358587",
			"bytes_streamed": int64(7000), "throughput_bps": int64(900), "last_access": base,
		},
	}

	merged := mergeDashboardStreamRows(rows)
	if len(merged) != 2 {
		t.Fatalf("expected 2 rows (one per profile), got %d", len(merged))
	}

	var primary map[string]interface{}
	for _, row := range merged {
		if row["profile_name"] == "Primary Profile" {
			primary = row
		}
	}
	if primary == nil {
		t.Fatal("merged Primary Profile row not found")
	}
	// Representative is the most-recently-active connection (A4).
	if primary["id"] != "A4" {
		t.Errorf("expected representative id A4 (latest activity), got %v", primary["id"])
	}
	// Bytes and throughput summed across all three connections.
	if got := primary["bytes_streamed"].(int64); got != 4020 {
		t.Errorf("expected summed bytes 4020, got %d", got)
	}
	if got := primary["throughput_bps"].(int64); got != 1300 {
		t.Errorf("expected summed throughput 1300, got %d", got)
	}
	if got := primary["connection_count"].(int); got != 3 {
		t.Errorf("expected connection_count 3, got %d", got)
	}
	if got := primary["via_share_link"].(bool); !got {
		t.Errorf("expected via_share_link to survive merge")
	}
}

func TestGetLingeringStreamsBridgesRangeRequestGaps(t *testing.T) {
	now := time.Now()
	movie := func(id string) *TrackedStream {
		return &TrackedStream{
			ID:            id,
			ProfileID:     "profile",
			Path:          "/movie.mkv",
			MediaMetadata: StreamMediaMetadata{MediaType: "movie", ItemID: "tmdb:movie:1"},
		}
	}
	episode := &TrackedStream{
		ID:            "episode-range",
		ProfileID:     "profile",
		Path:          "/episode.mkv",
		MediaMetadata: StreamMediaMetadata{MediaType: "episode", ItemID: "tmdb:tv:2:s01e01"},
	}
	tracker := &StreamTracker{
		streams: make(map[string]*TrackedStream),
		recentlyEnded: map[string]recentlyEndedStream{
			"older":         {stream: movie("older"), endedAt: now.Add(-10 * time.Second)},
			"newer":         {stream: movie("newer"), endedAt: now.Add(-2 * time.Second)},
			"episode-range": {stream: episode, endedAt: now.Add(-dashboardLingerGrace - time.Second)},
		},
	}

	got := tracker.GetLingeringStreams()
	if len(got) != 1 || got[0].ID != "newer" {
		t.Fatalf("GetLingeringStreams() = %v, want only the latest ended request of the unexpired playback", streamIDs(got))
	}

	// An open request for the same playback already lists it.
	tracker.streams["open"] = movie("open")
	if got := tracker.GetLingeringStreams(); len(got) != 0 {
		t.Fatalf("GetLingeringStreams() with an open request = %v, want none", streamIDs(got))
	}
}

// Large read-ahead buffers leave minutes between range requests; the latest
// ended request of the playback must survive pruning past the teardown grace.
func TestRecentlyEndedPruneKeepsLatestPerSlotForDashboard(t *testing.T) {
	now := time.Now()
	movie := func(id string) *TrackedStream {
		return &TrackedStream{
			ID:            id,
			ProfileID:     "profile",
			Path:          "/movie.mkv",
			MediaMetadata: StreamMediaMetadata{MediaType: "movie", ItemID: "tmdb:movie:1"},
		}
	}
	tracker := &StreamTracker{
		streams: make(map[string]*TrackedStream),
		recentlyEnded: map[string]recentlyEndedStream{
			"old":    {stream: movie("old"), endedAt: now.Add(-3 * time.Minute)},
			"latest": {stream: movie("latest"), endedAt: now.Add(-2 * time.Minute)},
		},
	}
	tracker.pruneRecentlyEndedLocked(now)
	if _, ok := tracker.recentlyEnded["old"]; ok {
		t.Fatal("older ended request of the slot survived prune")
	}
	got := tracker.GetLingeringStreams()
	if len(got) != 1 || got[0].ID != "latest" {
		t.Fatalf("GetLingeringStreams() = %v, want latest after a 2-minute gap", streamIDs(got))
	}
	if _, ok := tracker.GetEndedStream("latest"); !ok {
		t.Fatal("GetEndedStream(latest) = false, want the lingering stream stoppable")
	}
}

func TestGetLingeringStreamsSkipsTerminatedStreams(t *testing.T) {
	stream := &TrackedStream{
		ID:            "cut",
		ProfileID:     "profile",
		Path:          "/movie.mkv",
		MediaMetadata: StreamMediaMetadata{MediaType: "movie", ItemID: "tmdb:movie:1"},
		cancel:        func() {},
	}
	tracker := &StreamTracker{streams: map[string]*TrackedStream{stream.ID: stream}}
	if !tracker.TerminateStream(stream.ID) {
		t.Fatal("TerminateStream() = false")
	}
	if got := tracker.GetLingeringStreams(); len(got) != 0 {
		t.Fatalf("GetLingeringStreams() after terminate = %v, want none", streamIDs(got))
	}
}

func streamIDs(streams []*TrackedStream) []string {
	ids := make([]string, 0, len(streams))
	for _, s := range streams {
		ids = append(ids, s.ID)
	}
	return ids
}

func TestPlaybackSlotStatsAccumulatesAcrossRangeRequests(t *testing.T) {
	movie := func(id string, bytes int64) *TrackedStream {
		counter := bytes
		return &TrackedStream{
			ID:            id,
			ProfileID:     "profile",
			Path:          "/movie.mkv",
			MediaMetadata: StreamMediaMetadata{MediaType: "movie", ItemID: "tmdb:movie:1"},
			bytesCounter:  &counter,
		}
	}
	tracker := &StreamTracker{streams: make(map[string]*TrackedStream)}
	for i, bytes := range []int64{1000, 2000} {
		s := movie(fmt.Sprintf("range-%d", i), bytes)
		tracker.streams[s.ID] = s
		tracker.EndStream(s.ID)
	}
	open := movie("open", 500)
	tracker.streams[open.ID] = open

	key := trackedStreamSlotKey(open)
	if got := tracker.PlaybackSlotStats()[key].BytesStreamed; got != 3500 {
		t.Fatalf("BytesStreamed = %d, want ended + open requests (3500)", got)
	}

	// A playback idle past the dashboard linger grace is dropped.
	delete(tracker.streams, open.ID)
	tracker.slotTotals[key].lastSeen = time.Now().Add(-dashboardLingerGrace - time.Second)
	if _, ok := tracker.PlaybackSlotStats()[key]; ok {
		t.Fatal("expired playback slot still reported")
	}
}

// A native player between two range requests is listed through its last
// ended request; stopping it from the dashboard must work and drop it.
func TestMarkStopPlaybackStopsLingeringStream(t *testing.T) {
	stream := &TrackedStream{
		ID:            "between-ranges",
		ProfileID:     "profile",
		Path:          "/episode.mkv",
		MediaMetadata: StreamMediaMetadata{MediaType: "episode", ItemID: "tmdb:tv:2004:s03e06"},
	}
	tracker := &StreamTracker{
		streams:       make(map[string]*TrackedStream),
		recentlyEnded: map[string]recentlyEndedStream{stream.ID: {stream: stream, endedAt: time.Now()}},
	}
	if got, ok := tracker.GetEndedStream(stream.ID); !ok || got.ProfileID != "profile" {
		t.Fatalf("GetEndedStream() = %v, %v", got, ok)
	}
	if !tracker.MarkStopPlayback(stream.ID) {
		t.Fatal("MarkStopPlayback() = false for a lingering stream")
	}
	if !tracker.ShouldStopPlayback("profile", models.PlaybackProgressUpdate{MediaType: "episode", ItemID: "tmdb:tv:2004:s03e06"}) {
		t.Fatal("the player's next heartbeat is not told to stop")
	}
	if got := tracker.GetLingeringStreams(); len(got) != 0 {
		t.Fatalf("stopped stream still listed: %v", streamIDs(got))
	}
	if _, ok := tracker.GetEndedStream(stream.ID); ok {
		t.Fatal("GetEndedStream() still returns the stopped stream")
	}
}

// Stopping a native player between range requests hides its older ended
// requests too, instead of listing the next-older one in its place.
func TestMarkStopPlaybackHidesOlderRangeRequests(t *testing.T) {
	metadata := StreamMediaMetadata{MediaType: "episode", ItemID: "tmdb:tv:2004:s03e06"}
	older := &TrackedStream{ID: "range-1", ProfileID: "profile", Path: "/episode.mkv", MediaMetadata: metadata}
	newer := &TrackedStream{ID: "range-2", ProfileID: "profile", Path: "/episode.mkv", MediaMetadata: metadata}
	tracker := &StreamTracker{
		streams: make(map[string]*TrackedStream),
		recentlyEnded: map[string]recentlyEndedStream{
			older.ID: {stream: older, endedAt: time.Now().Add(-5 * time.Second)},
			newer.ID: {stream: newer, endedAt: time.Now()},
		},
	}
	if !tracker.MarkStopPlayback(newer.ID) {
		t.Fatal("MarkStopPlayback() = false for a lingering stream")
	}
	if got := tracker.GetLingeringStreams(); len(got) != 0 {
		t.Fatalf("older range request of the stopped playback still listed: %v", streamIDs(got))
	}
}

func TestStoppedPlaybackBlocksStreamsAfterHeartbeatConsumesStop(t *testing.T) {
	tracker := newTestTracker()
	url := "/video/stream?profileId=p1&mediaType=movie&itemId=tmdb:movie:14160"
	id, _, _ := tracker.StartStreamWithAccount(httptest.NewRequest(http.MethodGet, url, nil), "/webdav/a.mkv", 1000, 0, 0, "acct1")

	if tracker.PlaybackBlocked(httptest.NewRequest(http.MethodGet, url, nil)) {
		t.Fatal("playback blocked before it was stopped")
	}
	if !tracker.MarkStopPlayback(id) {
		t.Fatal("expected stop to be recorded")
	}
	update := models.PlaybackProgressUpdate{MediaType: "movie", ItemID: "tmdb:movie:14160"}
	if !tracker.ShouldStopPlayback("p1", update) {
		t.Fatal("heartbeat did not receive the stop")
	}
	// The heartbeat signal is one-shot, the stream block is not.
	if !tracker.PlaybackBlocked(httptest.NewRequest(http.MethodGet, url, nil)) {
		t.Fatal("next range request was not refused")
	}
	other := "/video/stream?profileId=p1&mediaType=movie&itemId=tmdb:movie:1"
	if tracker.PlaybackBlocked(httptest.NewRequest(http.MethodGet, other, nil)) {
		t.Fatal("block leaked to another title")
	}
}
