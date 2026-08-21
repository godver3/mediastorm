package handlers

import (
	"testing"
	"time"
)

func TestPlaybackLatencyTrackerRecordsCompleteSample(t *testing.T) {
	tr := NewPlaybackLatencyTracker(10)

	t0 := time.Now()
	tr.NotePrequeueRequested("pq1", "tt123", "user-1", "Test Movie", "movie")
	tr.NotePrequeueMetadata("pq1", "tt1798709", 2013)
	<-time.After(5 * time.Millisecond) // simulate search+resolve+probe
	t1 := time.Now()
	tr.NotePrequeueReady("pq1")
	// The resolution phase picks the release after ready; the sample must carry it.
	tr.NotePrequeueRelease("pq1", "Test.Movie.2013.1080p.BluRay.x264-GROUP")
	<-time.After(3 * time.Millisecond) // simulate session spin-up

	// Simulate the HLS session + first segment (mirrors what ServeSegment does).
	t2 := time.Now()
	<-time.After(7 * time.Millisecond) // ffmpeg warmup
	t3 := time.Now()
	<-time.After(2 * time.Millisecond) // client fetch
	t4 := time.Now()

	tr.Record(PlaybackLatencySample{
		PrequeueID:          "pq1",
		SessionID:           "sess1",
		TitleName:           "Test Movie",
		ServiceType:         "usenet",
		ClientRequestedAt:   t0,
		PrequeueReadyAt:     t1,
		HLSSessionCreatedAt: t2,
		FirstSegmentReadyAt: t3,
		FirstSegmentSentAt:  t4,
	})

	latest := tr.Latest(1)
	if len(latest) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(latest))
	}
	s := latest[0]
	if !s.Complete {
		t.Fatalf("expected complete sample: %+v", s)
	}
	if s.ReleaseName != "Test.Movie.2013.1080p.BluRay.x264-GROUP" {
		t.Errorf("releaseName = %q, want the selected release", s.ReleaseName)
	}
	if s.ImdbID != "tt1798709" || s.Year != 2013 {
		t.Errorf("imdb/year = %q/%d, want tt1798709/2013 (bench must re-scope the same search)", s.ImdbID, s.Year)
	}
	// Phases must be non-negative and roughly match the sleep budget; the total
	// must be the sum of the phase nanoseconds (allow jitter slack).
	if s.TotalMs < 14 || s.TotalMs > 300 {
		t.Errorf("total=%dms out of expected range", s.TotalMs)
	}
	if s.PrequeueMs < 1 || s.PrequeueMs > 200 {
		t.Errorf("prequeue=%dms out of range", s.PrequeueMs)
	}
	if s.FFmpegWarmupMs < 1 {
		t.Errorf("ffmpegWarmup=%dms should be positive", s.FFmpegWarmupMs)
	}
	if s.HLSCreateMs < 0 {
		t.Errorf("hlsCreate=%dms should not be negative", s.HLSCreateMs)
	}

	// The pending entry must have been dropped after Record.
	reqAt, readyAt := tr.PrequeueTimes("pq1")
	if !reqAt.IsZero() || !readyAt.IsZero() {
		t.Errorf("pending state should be cleared after Record, got %v/%v", reqAt, readyAt)
	}
}

// When no HLS session served a segment (non-HLS stream), the bench must still
// surface the iteration as a prequeue-only sample instead of dropping it.
func TestPlaybackLatencyTrackerNotePrequeueOnlySample(t *testing.T) {
	tr := NewPlaybackLatencyTracker(10)

	tr.NotePrequeueRequested("pq1", "tt123", "user-1", "Her", "movie")
	tr.NotePrequeueMetadata("pq1", "tt1798709", 2013)
	tr.NotePrequeueReady("pq1")
	tr.NotePrequeueRelease("pq1", "Her.2013.1080p.BluRay.x265-LAMA")
	tr.NotePrequeueOnlySample("pq1")

	latest := tr.Latest(1)
	if len(latest) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(latest))
	}
	s := latest[0]
	if s.Complete {
		t.Fatalf("prequeue-only sample must not be complete: %+v", s)
	}
	if s.PrequeueMs < 0 {
		t.Fatalf("prequeueMs = %d, want >= 0", s.PrequeueMs)
	}
	if s.ReleaseName != "Her.2013.1080p.BluRay.x265-LAMA" {
		t.Fatalf("releaseName = %q", s.ReleaseName)
	}
	if s.ImdbID != "tt1798709" || s.Year != 2013 {
		t.Fatalf("imdb/year = %q/%d", s.ImdbID, s.Year)
	}

	// A second call must be a no-op (pending was consumed).
	tr.NotePrequeueOnlySample("pq1")
	if n := tr.Count(); n != 1 {
		t.Fatalf("count = %d after duplicate synthesis, want 1", n)
	}
}

func TestPlaybackLatencyTrackerDirectHLSWithoutPrequeue(t *testing.T) {
	tr := NewPlaybackLatencyTracker(10)
	now := time.Now()
	tr.Record(PlaybackLatencySample{
		SessionID:           "sess-x",
		HLSSessionCreatedAt: now.Add(-2 * time.Second),
		FirstSegmentReadyAt: now.Add(-1 * time.Second),
		FirstSegmentSentAt:  now,
	})
	latest := tr.Latest(1)
	if len(latest) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(latest))
	}
	s := latest[0]
	if s.Complete {
		t.Fatalf("direct HLS start without prequeue must not be complete: %+v", s)
	}
	if s.TotalMs != -1 {
		t.Errorf("expected totalMs=-1 without t0, got %d", s.TotalMs)
	}
	if s.FFmpegWarmupMs != 1000 {
		t.Errorf("expected ffmpegWarmupMs=1000, got %d", s.FFmpegWarmupMs)
	}
}

func TestPlaybackLatencyTrackerRingAndStats(t *testing.T) {
	tr := NewPlaybackLatencyTracker(3)
	base := time.Now()
	for i := 0; i < 6; i++ {
		offset := time.Duration(i) * time.Second
		// total = 1000 + i*1000 ms: 1000, 2000, ..., 6000
		tr.Record(PlaybackLatencySample{
			PrequeueID:          "pq" + string(rune('a'+i)),
			SessionID:           "s" + string(rune('a'+i)),
			ClientRequestedAt:   base.Add(offset),
			PrequeueReadyAt:     base.Add(offset + 500*time.Millisecond),
			HLSSessionCreatedAt: base.Add(offset + 700*time.Millisecond),
			FirstSegmentReadyAt: base.Add(offset + 1200*time.Millisecond),
			FirstSegmentSentAt:  base.Add(offset + time.Duration(1000+i*1000)*time.Millisecond),
		})
	}
	if got := tr.Count(); got != 3 {
		t.Fatalf("ring should cap at 3, got %d", got)
	}
	snap := tr.Snapshot(10)
	if snap.Total != 3 || snap.Complete != 3 {
		t.Fatalf("snapshot total=%d complete=%d, want 3/3", snap.Total, snap.Complete)
	}
	// Ring holds the last 3 samples: totals 4000/5000/6000.
	if snap.Stats.TotalMs.P50 != 5000 {
		t.Errorf("total p50=%d, want 5000", snap.Stats.TotalMs.P50)
	}
	if snap.Stats.TotalMs.P95 != 6000 {
		t.Errorf("total p95=%d, want 6000", snap.Stats.TotalMs.P95)
	}
	if snap.Stats.TotalMs.Max != 6000 || snap.Stats.TotalMs.Min != 4000 {
		t.Errorf("total min/max=%d/%d, want 4000/6000", snap.Stats.TotalMs.Min, snap.Stats.TotalMs.Max)
	}
	// Newest first ordering.
	if snap.Samples[0].SessionID != "sf" || snap.Samples[2].SessionID != "sd" {
		t.Errorf("samples not newest-first: %+v", snap.Samples)
	}
}

func TestPlaybackLatencyTrackerReClickOverwritesT0(t *testing.T) {
	// Tracker-level primitive only: calling NotePrequeueRequested alone moves t0
	// and leaves t1 untouched. The HTTP handler's ready-entry reuse path stamps
	// NotePrequeueReady immediately after (see
	// TestPlaybackLatencyTrackerReusedReadyEntryIsComplete), so the app flow
	// produces a complete sample instead of an incomplete one.
	tr := NewPlaybackLatencyTracker(10)
	tr.NotePrequeueRequested("pq1", "tt1", "user-1", "Title", "movie")
	tr.NotePrequeueReady("pq1")
	<-time.After(2 * time.Millisecond)
	// Client clicks again on an already-ready entry: t0 moves, t1 stays.
	tr.NotePrequeueRequested("pq1", "tt1", "user-1", "Title", "movie")
	reqAt, readyAt := tr.PrequeueTimes("pq1")
	if !reqAt.After(readyAt) {
		t.Errorf("after re-click t0 (%v) should be after t1 (%v)", reqAt, readyAt)
	}
	// Recording merges the newest t0 and keeps the original ready time.
	now := time.Now()
	tr.Record(PlaybackLatencySample{
		PrequeueID:          "pq1",
		ClientRequestedAt:   reqAt,
		PrequeueReadyAt:     readyAt,
		HLSSessionCreatedAt: now,
		FirstSegmentReadyAt: now,
		FirstSegmentSentAt:  now,
	})
	s := tr.Latest(1)[0]
	if s.PrequeueReadyAt != readyAt {
		t.Errorf("ready time was not preserved across re-click")
	}
}

// A reused-ready prequeue must yield a complete sample: the handler stamps t1
// (NotePrequeueReady) right after refreshing t0, so the click→frame is fully
// measurable with a ~0ms prequeue phase instead of complete=false/prequeue=-1.
func TestPlaybackLatencyTrackerReusedReadyEntryIsComplete(t *testing.T) {
	tr := NewPlaybackLatencyTracker(10)
	tr.NotePrequeueRequested("pq-reuse", "tt123", "user-1", "Her", "movie")
	tr.NotePrequeueMetadata("pq-reuse", "tt1798709", 2013)
	// The reuse path stamps requested + ready back-to-back.
	tr.NotePrequeueReady("pq-reuse")
	reqAt, readyAt := tr.PrequeueTimes("pq-reuse")
	if readyAt.Before(reqAt) {
		t.Fatalf("reused-ready t1 (%v) must be at/after t0 (%v)", readyAt, reqAt)
	}
	now := time.Now()
	tr.Record(PlaybackLatencySample{
		PrequeueID:          "pq-reuse",
		ClientRequestedAt:   reqAt,
		PrequeueReadyAt:     readyAt,
		HLSSessionCreatedAt: now.Add(50 * time.Millisecond),
		FirstSegmentReadyAt: now.Add(120 * time.Millisecond),
		FirstSegmentSentAt:  now.Add(150 * time.Millisecond),
	})
	s := tr.Latest(1)[0]
	if !s.Complete {
		t.Fatalf("reused-ready sample must be complete: %+v", s)
	}
	if s.PrequeueMs > 5 {
		t.Errorf("prequeueMs=%d, want ≈0 for a reused-ready entry", s.PrequeueMs)
	}
}

// The aggregate stats must surface the phases each sample actually measured, even
// for incomplete samples — a prequeue-only bench (SDR, no HLS segment) used to
// leave every p50/p95 chip at -1ms because Snapshot gated everything on Complete.
func TestPlaybackLatencySnapshotAggregatesIncompletePhases(t *testing.T) {
	tr := NewPlaybackLatencyTracker(20)
	tr.NotePrequeueRequested("pq-sdr", "tt123", "user-1", "Her", "movie")
	tr.NotePrequeueReady("pq-sdr")
	tr.NotePrequeueOnlySample("pq-sdr") // complete=false, but prequeueMs>0

	// A session that measured t2..t4 but lacked t1 (e.g. reused ready prequeue
	// whose pending was consumed): complete=false, yet hlsCreate/ffmpegWarmup/
	// serveWait/total are real and must feed the stats.
	t0 := time.Now()
	tr.Record(PlaybackLatencySample{
		PrequeueID:          "pq-partial",
		SessionID:           "s-partial",
		ClientRequestedAt:   t0.Add(-2000 * time.Millisecond),
		HLSSessionCreatedAt: t0.Add(-1300 * time.Millisecond),
		FirstSegmentReadyAt: t0.Add(-600 * time.Millisecond),
		FirstSegmentSentAt:  t0.Add(-500 * time.Millisecond),
	})

	snap := tr.Snapshot(10)
	if snap.Complete != 0 {
		t.Fatalf("complete=%d, want 0 (both samples are incomplete)", snap.Complete)
	}
	if snap.Total != 2 {
		t.Fatalf("total=%d, want 2", snap.Total)
	}
	if snap.Stats.PrequeueMs.P50 < 0 {
		t.Errorf("prequeue p50=%d, want the SDR sample's prequeueMs to feed the stats", snap.Stats.PrequeueMs.P50)
	}
	if snap.Stats.HLSCreateMs.P50 != 700 {
		t.Errorf("hlsCreate p50=%d, want 700 (partial sample still measured its phases)", snap.Stats.HLSCreateMs.P50)
	}
	if snap.Stats.FFmpegWarmupMs.P50 != 700 {
		t.Errorf("ffmpegWarmup p50=%d, want 700", snap.Stats.FFmpegWarmupMs.P50)
	}
	if snap.Stats.TotalMs.P50 != 1500 {
		t.Errorf("total p50=%d, want 1500 (partial sample measured t0..t4 minus t1)", snap.Stats.TotalMs.P50)
	}
}
