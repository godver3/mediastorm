package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	body := rec2.Body.String()
	if len(body) < 1000 {
		t.Fatalf("page body suspiciously small: %d bytes", len(body))
	}
	// The per-row benchmark command builder must be present (data-bench buttons
	// that prefill a latency_bench.sh invocation for that sample/release).
	for _, want := range []string{
		"data-bench", "data-bench-run", "copyBenchRow", "runBenchRow", "buildBenchCmdFor",
		"mediastorm_admin_session", "latency_bench.sh", "latency/bench", "session-token", "id=\"toast\"",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("latency page missing %q", want)
		}
	}
}

// The session cookie is HttpOnly, so the page must fetch the token from the
// master-only endpoint rather than document.cookie.
func TestServeLatencySessionToken(t *testing.T) {
	admin := NewPlaybackLatencyAdmin(NewPlaybackLatencyTracker(10))

	req := httptest.NewRequest(http.MethodGet, "/admin/api/latency/session-token", nil)
	req.AddCookie(&http.Cookie{Name: adminSessionCookieName, Value: "token-abc"})
	rec := httptest.NewRecorder()
	admin.ServeLatencySessionToken(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Token != "token-abc" {
		t.Fatalf("token = %q, want token-abc", body.Token)
	}

	// Missing cookie → 401 so the page quietly degrades to an empty token.
	req2 := httptest.NewRequest(http.MethodGet, "/admin/api/latency/session-token", nil)
	rec2 := httptest.NewRecorder()
	admin.ServeLatencySessionToken(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("no-cookie status=%d, want 401", rec2.Code)
	}
}

// The backend-side bench must validate its body and acknowledge the background
// run (the actual worker/HLS driving happens without a handler wired in tests).
func TestRunPlaybackBenchValidatesAndStarts(t *testing.T) {
	admin := NewPlaybackLatencyAdmin(NewPlaybackLatencyTracker(10))

	// Valid -> 200 + started
	body := strings.NewReader(`{"titleId":"tmdb:movie:1","titleName":"Her","userId":"u1","iterations":3,"scope":"resolve"}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/latency/bench", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	admin.RunPlaybackBench(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Started    bool `json:"started"`
		Iterations int  `json:"iterations"`
		Scope      string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Started || resp.Iterations != 3 || resp.Scope != "resolve" {
		t.Fatalf("bench ack = %+v", resp)
	}

	// Missing titleId -> 400
	bad := httptest.NewRequest(http.MethodPost, "/admin/api/latency/bench", strings.NewReader(`{"titleName":"Her","userId":"u1"}`))
	badRec := httptest.NewRecorder()
	admin.RunPlaybackBench(badRec, bad)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("missing-title status=%d, want 400", badRec.Code)
	}

	// Bad scope -> 400
	bad2 := httptest.NewRequest(http.MethodPost, "/admin/api/latency/bench", strings.NewReader(`{"titleId":"t","titleName":"n","userId":"u","scope":"bogus"}`))
	badRec2 := httptest.NewRecorder()
	admin.RunPlaybackBench(badRec2, bad2)
	if badRec2.Code != http.StatusBadRequest {
		t.Fatalf("bad-scope status=%d, want 400", badRec2.Code)
	}
}
