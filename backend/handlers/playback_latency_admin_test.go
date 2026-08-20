package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeLatencyFlushPrequeue struct{ cleared bool }
type fakeLatencyFlushIndexer struct{ cleared bool }
type fakeLatencyFlushPrewarm struct{ cleared bool }
type fakeLatencyFlushPool struct{ cleared bool }
type fakeLatencyFlushImporter struct{ cleared bool }
type fakeLatencyFlushHLS struct{ cleared bool }

func (f *fakeLatencyFlushPrequeue) DeleteAll()                   { f.cleared = true }
func (f *fakeLatencyFlushIndexer) ClearSearchCache()             { f.cleared = true }
func (f *fakeLatencyFlushPrewarm) ClearAll()                     { f.cleared = true }
func (f *fakeLatencyFlushPool) ClearPool() error                 { f.cleared = true; return nil }
func (f *fakeLatencyFlushImporter) ClearResolvedNZBs()           { f.cleared = true }
func (f *fakeLatencyFlushHLS) ClearPlaybackCaches() (int, error) { f.cleared = true; return 2, nil }

func TestFlushPlaybackCachesCallsEveryRegisteredFlusher(t *testing.T) {
	admin, pq, idx, pw, pool, imp, hls := newFlushTestAdmin()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/latency/flush", nil)
	rec := httptest.NewRecorder()
	admin.FlushPlaybackCaches(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	var resp struct {
		Cleared []string `json:"cleared"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, rec.Body.String())
	}
	if len(resp.Cleared) != 6 {
		t.Fatalf("expected 6 cleared items, got %v", resp.Cleared)
	}
	if !pq.cleared || !idx.cleared || !pw.cleared || !pool.cleared || !imp.cleared || !hls.cleared {
		t.Fatalf("not all flushers were invoked: prequeue=%v indexer=%v prewarm=%v pool=%v importer=%v hls=%v",
			pq.cleared, idx.cleared, pw.cleared, pool.cleared, imp.cleared, hls.cleared)
	}
}

// Scope=resolve must keep the search cache AND the transcode/probe side warm,
// clearing only resolution state (prequeue, prewarm, resolved-NZB, pool) so a
// repeat play isolates the resolve+parse cost.
func TestFlushResolveScopeKeepsSearchCache(t *testing.T) {
	admin, pq, idx, pw, pool, imp, hls := newFlushTestAdmin()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/latency/flush?scope=resolve", nil)
	rec := httptest.NewRecorder()
	admin.FlushPlaybackCaches(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	if idx.cleared || hls.cleared {
		t.Fatalf("scope=resolve must keep search and stream warm: indexer=%v hls=%v", idx.cleared, hls.cleared)
	}
	if !pq.cleared || !pw.cleared || !imp.cleared || !pool.cleared {
		t.Fatalf("scope=resolve should clear resolution state: prequeue=%v prewarm=%v importer=%v pool=%v",
			pq.cleared, pw.cleared, imp.cleared, pool.cleared)
	}
}

// Scope=stream must only touch the transcode side (search, resolution and pool
// all stay warm).
func TestFlushStreamScopeOnlyClearsTranscode(t *testing.T) {
	admin, pq, idx, pw, pool, imp, hls := newFlushTestAdmin()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/latency/flush?scope=stream", nil)
	rec := httptest.NewRecorder()
	admin.FlushPlaybackCaches(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	if !hls.cleared {
		t.Fatal("scope=stream must clear HLS/transcode state")
	}
	if pq.cleared || idx.cleared || pw.cleared || pool.cleared || imp.cleared {
		t.Fatalf("scope=stream must NOT clear other state: prequeue=%v indexer=%v prewarm=%v pool=%v importer=%v",
			pq.cleared, idx.cleared, pw.cleared, pool.cleared, imp.cleared)
	}
}

func newFlushTestAdmin() (*PlaybackLatencyAdmin, *fakeLatencyFlushPrequeue, *fakeLatencyFlushIndexer,
	*fakeLatencyFlushPrewarm, *fakeLatencyFlushPool, *fakeLatencyFlushImporter, *fakeLatencyFlushHLS) {
	admin := NewPlaybackLatencyAdmin(NewPlaybackLatencyTracker(10))
	pq := &fakeLatencyFlushPrequeue{}
	idx := &fakeLatencyFlushIndexer{}
	pw := &fakeLatencyFlushPrewarm{}
	pool := &fakeLatencyFlushPool{}
	imp := &fakeLatencyFlushImporter{}
	hls := &fakeLatencyFlushHLS{}
	admin.SetPrequeueStore(pq)
	admin.SetIndexerService(idx)
	admin.SetPrewarmService(pw)
	admin.SetPoolManager(pool)
	admin.SetImporterService(imp)
	admin.SetHLSManager(hls)
	return admin, pq, idx, pw, pool, imp, hls
}

// Ensure the latency JSON and page endpoints render.
func TestServeLatencyEndpoints(t *testing.T) {
	tr := NewPlaybackLatencyTracker(10)
	now := time.Now()
	tr.Record(PlaybackLatencySample{
		PrequeueID:          "pq1",
		SessionID:           "s1",
		ClientRequestedAt:   now.Add(-8 * time.Millisecond),
		PrequeueReadyAt:     now.Add(-5 * time.Millisecond),
		HLSSessionCreatedAt: now.Add(-4 * time.Millisecond),
		FirstSegmentReadyAt: now.Add(-1 * time.Millisecond),
		FirstSegmentSentAt:  now,
	})
	admin := NewPlaybackLatencyAdmin(tr)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/latency?limit=10", nil)
	rec := httptest.NewRecorder()
	admin.ServePlaybackLatencyJSON(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("json status=%d", rec.Code)
	}
	var snap PlaybackLatencySnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if snap.Total != 1 || snap.Complete != 1 || len(snap.Samples) != 1 {
		t.Fatalf("snapshot mismatch: %+v", snap)
	}
	if snap.Samples[0].SessionID != "s1" {
		t.Fatalf("sample lost: %+v", snap.Samples)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/admin/latency", nil)
	rec2 := httptest.NewRecorder()
	admin.ServeLatencyPage(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("page status=%d", rec2.Code)
	}
	if len(rec2.Body.String()) < 1000 {
		t.Fatalf("page body suspiciously small: %d bytes", len(rec2.Body.String()))
	}
}
