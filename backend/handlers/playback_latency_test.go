package handlers

import (
	"testing"
	"time"
)

func TestPlaybackLatencyTrackerRecordsCompleteSample(t *testing.T) {
	tr := NewPlaybackLatencyTracker(10)

	t0 := time.Now()
	tr.NotePrequeueRequested("pq1", "tt123", "Test Movie", "movie")
	<-time.After(5 * time.Millisecond) // simulate search+resolve+probe
	t1 := time.Now()
	tr.NotePrequeueReady("pq1")
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
	tr := NewPlaybackLatencyTracker(10)
	tr.NotePrequeueRequested("pq1", "tt1", "Title", "movie")
	tr.NotePrequeueReady("pq1")
	<-time.After(2 * time.Millisecond)
	// Client clicks again on an already-ready entry: t0 moves, t1 stays.
	tr.NotePrequeueRequested("pq1", "tt1", "Title", "movie")
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
