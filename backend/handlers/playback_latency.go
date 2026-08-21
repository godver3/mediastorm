package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"

	"novastream/services/playback"
)

// PlaybackLatencySample is one click→first-frame measurement of the VOD
// playback path. All timestamps are server wall-clock.
//
//	t0 ClientRequestedAt       - client POST /playback/prequeue arrived
//	t1 PrequeueReadyAt         - prequeue worker marked the entry ready
//	t2 HLSSessionCreatedAt     - HLS session created (StreamStartTime)
//	t3 FirstSegmentReadyAt     - first media segment present on disk
//	t4 FirstSegmentSentAt      - first segment response began streaming
//
// Native (non-HLS) clients never reach t2+; their timeline ends at t1 and is
// reported by the PREQUEUE_LATENCY log line instead.
type PlaybackLatencySample struct {
	ID         string `json:"id"`
	PrequeueID string `json:"prequeueId,omitempty"`
	SessionID  string `json:"sessionId,omitempty"`
	TitleID    string `json:"titleId,omitempty"`
	UserID     string `json:"userId,omitempty"`
	ImdbID     string `json:"imdbId,omitempty"` // re-scoping a bench needs the same search context as the original click
	Year       int    `json:"year,omitempty"`
	TitleName  string `json:"titleName,omitempty"`
	MediaType  string `json:"mediaType,omitempty"`

	// ReleaseName is the selected release (e.g. "Her.2013.1080p.BluRay...-d3g")
	// so benchmark runs can be compared apples-to-apples per release. Backfilled
	// from the prequeue's selected result once resolution makes its choice.
	ReleaseName string `json:"releaseName,omitempty"`

	ServiceType     string `json:"serviceType,omitempty"`     // "usenet" | "debrid" | ""
	ServiceProvider string `json:"serviceProvider,omitempty"` // indexer / debrid provider when known

	ClientRequestedAt   time.Time `json:"clientRequestedAt,omitempty"`   // t0
	PrequeueReadyAt     time.Time `json:"prequeueReadyAt,omitempty"`     // t1
	HLSSessionCreatedAt time.Time `json:"hlsSessionCreatedAt,omitempty"` // t2
	FirstSegmentReadyAt time.Time `json:"firstSegmentReadyAt,omitempty"` // t3
	FirstSegmentSentAt  time.Time `json:"firstSegmentSentAt,omitempty"`  // t4

	// Derived durations in milliseconds. -1 when the phase could not be
	// measured (missing endpoints).
	TotalMs        int64 `json:"totalMs"`        // t0→t4
	PrequeueMs     int64 `json:"prequeueMs"`     // t0→t1 (search+resolve+probe)
	HLSCreateMs    int64 `json:"hlsCreateMs"`    // t1→t2 (session spin-up; from t0 when t1 unknown)
	FFmpegWarmupMs int64 `json:"ffmpegWarmupMs"` // t2→t3 (first segment on disk, incl. playlist fetch)
	ServeWaitMs    int64 `json:"serveWaitMs"`    // t3→t4 (segment file ready → response sent)

	Complete bool     `json:"complete"` // true when t0..t4 all present
	Notes    []string `json:"notes,omitempty"`
}

type pendingPrequeueTimes struct {
	requestedAt time.Time // t0
	readyAt     time.Time // t1
	titleID     string
	userID      string
	imdbID      string
	year        int
	titleName   string
	mediaType   string
	releaseName string
}

// PlaybackLatencyTracker correlates prequeue timestamps with HLS session
// timestamps and retains a rolling window of completed samples.
type PlaybackLatencyTracker struct {
	mu      sync.Mutex
	samples []PlaybackLatencySample // oldest → newest
	max     int
	pending map[string]*pendingPrequeueTimes
	seq     int64
}

func NewPlaybackLatencyTracker(maxSamples int) *PlaybackLatencyTracker {
	if maxSamples <= 0 {
		maxSamples = 400
	}
	return &PlaybackLatencyTracker{
		samples: make([]PlaybackLatencySample, 0, maxSamples),
		max:     maxSamples,
		pending: make(map[string]*pendingPrequeueTimes),
	}
}

// NotePrequeueRequested records t0 for a prequeue. A repeated request for the
// same prequeue (warm re-click) overwrites t0 so the latest click is measured.
func (t *PlaybackLatencyTracker) NotePrequeueRequested(prequeueID, titleID, userID, titleName, mediaType string) {
	if t == nil || prequeueID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	for id, p := range t.pending {
		if now.Sub(p.requestedAt) > 30*time.Minute {
			delete(t.pending, id)
		}
	}
	p := t.pending[prequeueID]
	if p == nil {
		p = &pendingPrequeueTimes{}
		t.pending[prequeueID] = p
	}
	p.requestedAt = now
	p.titleID = titleID
	p.userID = userID
	p.titleName = titleName
	p.mediaType = mediaType
}

// NotePrequeueReady records t1 for a prequeue and, when the prequeue phase is
// measurable, emits the PREQUEUE_LATENCY log line (covers native clients whose
// timeline ends at ready).
func (t *PlaybackLatencyTracker) NotePrequeueReady(prequeueID string) {
	if t == nil || prequeueID == "" {
		return
	}
	t.mu.Lock()
	p := t.pending[prequeueID]
	if p == nil {
		p = &pendingPrequeueTimes{}
		t.pending[prequeueID] = p
	}
	p.readyAt = time.Now()
	requestedAt := p.requestedAt
	titleName := p.titleName
	t.mu.Unlock()

	if !requestedAt.IsZero() {
		log.Printf("[latency] PREQUEUE_LATENCY prequeue=%dms title=%q prequeueId=%s",
			p.readyAt.Sub(requestedAt).Milliseconds(), titleName, prequeueID)
	}
}

// NotePrequeueRelease records the selected release for a prequeue once the
// resolution phase picks it, so latency samples name the exact release.
func (t *PlaybackLatencyTracker) NotePrequeueRelease(prequeueID, releaseName string) {
	if t == nil || prequeueID == "" || releaseName == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if p := t.pending[prequeueID]; p != nil {
		p.releaseName = releaseName
	}
}

// NotePrequeueMetadata records the extra search context (IMDb id + year) of a
// prequeue request so a later benchmark can re-scope the exact same search.
func (t *PlaybackLatencyTracker) NotePrequeueMetadata(prequeueID, imdbID string, year int) {
	if t == nil || prequeueID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if p := t.pending[prequeueID]; p != nil {
		p.imdbID = imdbID
		p.year = year
	}
}

// PrequeueTimes returns the recorded t0/t1 for a prequeue ID.
func (t *PlaybackLatencyTracker) PrequeueTimes(prequeueID string) (requestedAt, readyAt time.Time) {
	if t == nil || prequeueID == "" {
		return time.Time{}, time.Time{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.pending[prequeueID]
	if p == nil {
		return time.Time{}, time.Time{}
	}
	return p.requestedAt, p.readyAt
}

// Record stores a completed sample, fills derived durations, emits the
// PLAYBACK_LATENCY log line, and drops the pending prequeue state.
func (t *PlaybackLatencyTracker) Record(s PlaybackLatencySample) {
	if t == nil || s.FirstSegmentSentAt.IsZero() {
		return
	}
	prequeueID := s.PrequeueID
	t.mu.Lock()
	if p := t.pending[prequeueID]; p != nil {
		if s.ClientRequestedAt.IsZero() {
			s.ClientRequestedAt = p.requestedAt
		}
		if s.PrequeueReadyAt.IsZero() {
			s.PrequeueReadyAt = p.readyAt
		}
		if s.PrequeueID != "" && s.TitleID == "" {
			s.TitleID = p.titleID
		}
		if s.UserID == "" {
			s.UserID = p.userID
		}
		if s.ImdbID == "" {
			s.ImdbID = p.imdbID
		}
		if s.Year == 0 {
			s.Year = p.year
		}
		if s.TitleName == "" {
			s.TitleName = p.titleName
		}
		if s.ReleaseName == "" {
			s.ReleaseName = p.releaseName
		}
		if s.MediaType == "" {
			s.MediaType = p.mediaType
		}
		delete(t.pending, prequeueID)
	}
	t.mu.Unlock()
	t.storeAndLog(s)
}

// storeAndLog derives durations, appends the sample to the rolling window and
// emits the PLAYBACK_LATENCY line. Callers handle pending-state correlation.
func (t *PlaybackLatencyTracker) storeAndLog(s PlaybackLatencySample) {
	s = deriveLatencyDurations(s)
	t.mu.Lock()
	t.seq++
	s.ID = fmt.Sprintf("L%d", t.seq)
	t.samples = append(t.samples, s)
	if len(t.samples) > t.max {
		t.samples = t.samples[len(t.samples)-t.max:]
	}
	t.mu.Unlock()

	log.Printf("[latency] PLAYBACK_LATENCY total=%dms prequeue=%dms hlsCreate=%dms ffmpegWarmup=%dms serveWait=%dms service=%s title=%q complete=%v prequeueId=%s session=%s",
		s.TotalMs, s.PrequeueMs, s.HLSCreateMs, s.FFmpegWarmupMs, s.ServeWaitMs,
		orUnknown(s.ServiceType), s.TitleName, s.Complete, s.PrequeueID, s.SessionID)
}

// NotePrequeueOnlySample records a prequeue-phase-only sample (t0→t1) when no
// HLS session ever served a media segment (non-HLS stream, or the segment never
// materialized). It surfaces those iterations in the latency table as
// complete=false with a valid prequeueMs — precisely the phase the OPP-1/2/3/12
// work targets. No-op once a full sample has consumed the pending state.
func (t *PlaybackLatencyTracker) NotePrequeueOnlySample(prequeueID string) {
	if t == nil || prequeueID == "" {
		return
	}
	t.mu.Lock()
	p := t.pending[prequeueID]
	if p == nil {
		t.mu.Unlock()
		return // already recorded (a full sample consumed it) or unknown
	}
	delete(t.pending, prequeueID)
	t.mu.Unlock()

	t.storeAndLog(PlaybackLatencySample{
		PrequeueID:        prequeueID,
		TitleID:           p.titleID,
		UserID:            p.userID,
		ImdbID:            p.imdbID,
		Year:              p.year,
		TitleName:         p.titleName,
		MediaType:         p.mediaType,
		ReleaseName:       p.releaseName,
		ClientRequestedAt: p.requestedAt,
		PrequeueReadyAt:   p.readyAt,
		Notes:             []string{"no HLS session served a segment — prequeue phase only"},
	})
}

func orUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

func deriveLatencyDurations(s PlaybackLatencySample) PlaybackLatencySample {
	ms := func(from, to time.Time) int64 {
		if from.IsZero() || to.IsZero() {
			return -1
		}
		return to.Sub(from).Milliseconds()
	}
	s.PrequeueMs = ms(s.ClientRequestedAt, s.PrequeueReadyAt)
	// Ready before the click (shared prewarm work / re-click on an already-ready
	// entry) means the prequeue phase cost the client nothing.
	if s.PrequeueMs < 0 && !s.ClientRequestedAt.IsZero() && !s.PrequeueReadyAt.IsZero() {
		s.PrequeueMs = 0
	}
	// HLS creation is normally after ready (web path). For prequeue-created
	// sessions (HDR/audio transcode) it can precede ready; measure from t0.
	hlsFrom := s.PrequeueReadyAt
	if hlsFrom.IsZero() {
		hlsFrom = s.ClientRequestedAt
	}
	s.HLSCreateMs = ms(hlsFrom, s.HLSSessionCreatedAt)
	if s.HLSCreateMs < 0 && !s.HLSSessionCreatedAt.IsZero() {
		s.HLSCreateMs = -2 // session exists but t1 unknown
	}
	s.FFmpegWarmupMs = ms(s.HLSSessionCreatedAt, s.FirstSegmentReadyAt)
	s.ServeWaitMs = ms(s.FirstSegmentReadyAt, s.FirstSegmentSentAt)
	s.TotalMs = ms(s.ClientRequestedAt, s.FirstSegmentSentAt)
	s.Complete = !s.ClientRequestedAt.IsZero() && !s.PrequeueReadyAt.IsZero() &&
		!s.HLSSessionCreatedAt.IsZero() && !s.FirstSegmentReadyAt.IsZero() && !s.FirstSegmentSentAt.IsZero()
	if !s.ClientRequestedAt.IsZero() && s.FirstSegmentSentAt.IsZero() {
		s.Notes = append(s.Notes, "no first segment served")
	}
	if s.ClientRequestedAt.IsZero() {
		s.Notes = append(s.Notes, "no prequeue correlation (direct HLS start)")
	}
	return s
}

// Latest returns up to n samples, newest first.
func (t *PlaybackLatencyTracker) Latest(n int) []PlaybackLatencySample {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]PlaybackLatencySample, 0, n)
	for i := len(t.samples) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, t.samples[i])
	}
	return out
}

func (t *PlaybackLatencyTracker) Count() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.samples)
}

// ClearSamples drops the sample window (pending prequeue state is retained so
// in-flight playbacks still complete a sample).
func (t *PlaybackLatencyTracker) ClearSamples() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.samples = nil
}

type latencyStat struct {
	Min int64 `json:"minMs"`
	P50 int64 `json:"p50Ms"`
	P95 int64 `json:"p95Ms"`
	Max int64 `json:"maxMs"`
	Avg int64 `json:"avgMs"`
	N   int   `json:"n"`
}

type LatencyStats struct {
	TotalMs        latencyStat `json:"totalMs"`
	PrequeueMs     latencyStat `json:"prequeueMs"`
	HLSCreateMs    latencyStat `json:"hlsCreateMs"`
	FFmpegWarmupMs latencyStat `json:"ffmpegWarmupMs"`
	ServeWaitMs    latencyStat `json:"serveWaitMs"`
}

type PlaybackLatencySnapshot struct {
	Samples  []PlaybackLatencySample `json:"samples"`
	Total    int                     `json:"total"`
	Complete int                     `json:"complete"`
	Stats    LatencyStats            `json:"stats"`
}

func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return -1
	}
	idx := int(float64(len(sorted)) * p)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func statFor(values []int64) latencyStat {
	if len(values) == 0 {
		return latencyStat{Min: -1, P50: -1, P95: -1, Max: -1, Avg: -1}
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var sum int64
	for _, v := range values {
		sum += v
	}
	return latencyStat{
		Min: sorted[0],
		P50: percentile(sorted, 0.50),
		P95: percentile(sorted, 0.95),
		Max: sorted[len(sorted)-1],
		Avg: sum / int64(len(values)),
		N:   len(values),
	}
}

func (t *PlaybackLatencyTracker) Snapshot(limit int) PlaybackLatencySnapshot {
	if t == nil {
		return PlaybackLatencySnapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	snap := PlaybackLatencySnapshot{Total: len(t.samples)}
	var totalVals, prequeueVals, hlsVals, warmupVals, serveVals []int64
	for _, s := range t.samples {
		if s.Complete {
			snap.Complete++
		}
		// Aggregate each phase from whatever the sample actually measures. A
		// prequeue-only sample (native SDR playback — no HLS segment) still has a
		// real prequeueMs, and a session that reused a ready prequeue still has
		// real hlsCreate/ffmpegWarmup/serveWait/total. Only the fully-correlated
		// ones count toward snap.Complete. Gating the stats on Complete alone is
		// what left every p50/p95 chip at -1ms whenever the bench was resolving
		// SDR releases.
		if s.TotalMs >= 0 {
			totalVals = append(totalVals, s.TotalMs)
		}
		if s.PrequeueMs >= 0 {
			prequeueVals = append(prequeueVals, s.PrequeueMs)
		}
		if s.HLSCreateMs >= 0 {
			hlsVals = append(hlsVals, s.HLSCreateMs)
		}
		if s.FFmpegWarmupMs >= 0 {
			warmupVals = append(warmupVals, s.FFmpegWarmupMs)
		}
		if s.ServeWaitMs >= 0 {
			serveVals = append(serveVals, s.ServeWaitMs)
		}
	}
	snap.Stats = LatencyStats{
		TotalMs:        statFor(totalVals),
		PrequeueMs:     statFor(prequeueVals),
		HLSCreateMs:    statFor(hlsVals),
		FFmpegWarmupMs: statFor(warmupVals),
		ServeWaitMs:    statFor(serveVals),
	}

	// Newest-first window.
	out := t.samples
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	rev := make([]PlaybackLatencySample, len(out))
	for i := range out {
		rev[len(out)-1-i] = out[i]
	}
	snap.Samples = rev
	return snap
}

// ---------------------------------------------------------------------------
// Admin surface: JSON endpoint + cache flush (cold-test support) + mini page.
// ---------------------------------------------------------------------------

type playbackLatencyFlusher interface {
	ClearPlaybackCaches() (int, error) // HLSManager
}

// latencyHLSSegmentDriver lets the in-process benchmark serve real HLS media
// segments so t3/t4 and the complete sample are recorded without a browser or
// auth tokens. Implemented by HLSManager.
type latencyHLSSegmentDriver interface {
	ServeSegment(w http.ResponseWriter, r *http.Request, sessionID, segmentName string)
}

type latencyPrequeueFlusher interface{ DeleteAll() }
type latencyIndexerFlusher interface{ ClearSearchCache() }
type latencyPrewarmFlusher interface{ ClearAll() }
type latencyPoolFlusher interface{ ClearPool() error }
type latencyImporterFlusher interface{ ClearResolvedNZBs() }

// PlaybackLatencyAdmin exposes the latency window and the cold-test cache
// flush. All dependencies are optional (nil-safe) so tests and reduced setups
// can register what they have.
type PlaybackLatencyAdmin struct {
	tracker        *PlaybackLatencyTracker
	prequeueStore  latencyPrequeueFlusher
	indexerSvc     latencyIndexerFlusher
	prewarmSvc     latencyPrewarmFlusher
	poolManager    latencyPoolFlusher
	importerSvc    latencyImporterFlusher
	hlsManager     playbackLatencyFlusher
	hlsDriver      latencyHLSSegmentDriver
	benchStore     *playback.PrequeueStore
	prequeueHandle *PrequeueHandler
}

func NewPlaybackLatencyAdmin(tracker *PlaybackLatencyTracker) *PlaybackLatencyAdmin {
	return &PlaybackLatencyAdmin{tracker: tracker}
}

// --- setters (wired from main.go) ---

func (a *PlaybackLatencyAdmin) SetPrequeueStore(s latencyPrequeueFlusher)   { a.prequeueStore = s }
func (a *PlaybackLatencyAdmin) SetIndexerService(s latencyIndexerFlusher)   { a.indexerSvc = s }
func (a *PlaybackLatencyAdmin) SetPrewarmService(s latencyPrewarmFlusher)   { a.prewarmSvc = s }
func (a *PlaybackLatencyAdmin) SetPoolManager(s latencyPoolFlusher)         { a.poolManager = s }
func (a *PlaybackLatencyAdmin) SetImporterService(s latencyImporterFlusher) { a.importerSvc = s }
func (a *PlaybackLatencyAdmin) SetHLSManager(m playbackLatencyFlusher)      { a.hlsManager = m }

// SetHLSSegmentDriver supplies the segment-serving path for the in-process
// benchmark (the same HLSManager instance as SetHLSManager).
func (a *PlaybackLatencyAdmin) SetHLSSegmentDriver(d latencyHLSSegmentDriver) { a.hlsDriver = d }

// SetPrequeueHandler gives the benchmark the real prequeue worker + store so
// it can run cold iterations in-process.
func (a *PlaybackLatencyAdmin) SetPrequeueHandler(h *PrequeueHandler) {
	a.prequeueHandle = h
	if h != nil {
		a.benchStore = h.GetStore()
	}
}

// ServePlaybackLatencyJSON renders the latest samples + aggregate stats.
func (a *PlaybackLatencyAdmin) ServePlaybackLatencyJSON(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &limit); err != nil || limit <= 0 {
			limit = 50
		}
		if limit > 500 {
			limit = 500
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(a.tracker.Snapshot(limit))
}

// ServeLatencyPage renders a small self-contained admin page for watching the
// click→first-frame numbers and triggering cold-cache flushes.
func (a *PlaybackLatencyAdmin) ServeLatencyPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, latencyPageHTML)
}

// ServeLatencySessionToken returns the admin session token so the page can
// prefill latency_bench.sh commands. The cookie itself is HttpOnly (unreadable
// from JS); this endpoint is master-only like the rest of the latency admin
// surface. The same token value doubles as the Bearer token for the protected
// playback/HLS routes the harness drives.
func (a *PlaybackLatencyAdmin) ServeLatencySessionToken(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	c, err := r.Cookie(adminSessionCookieName)
	if err != nil || c.Value == "" {
		http.Error(w, "no admin session cookie", http.StatusUnauthorized)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"token": c.Value})
}

// ---------------------------------------------------------------------------
// In-process cold-cache benchmark (backend-side; no auth tokens involved).
// ---------------------------------------------------------------------------

type playbackBenchRequest struct {
	TitleID    string `json:"titleId"`
	TitleName  string `json:"titleName"`
	UserID     string `json:"userId"`
	MediaType  string `json:"mediaType,omitempty"`
	Year       int    `json:"year,omitempty"`
	ImdbID     string `json:"imdbId,omitempty"`
	ClientID   string `json:"clientId,omitempty"`
	Iterations int    `json:"iterations,omitempty"`
	Scope      string `json:"scope,omitempty"`
}

// RunPlaybackBench triggers a backend-side cold-cache benchmark: N iterations
// of flush → prequeue worker → first HLS segment, each recorded into the
// passive tracker exactly like a real playthrough (same worker, same
// ServeSegment path). It runs in the background; the response only
// acknowledges the start and the new samples appear in the latency table.
// Because the measured phases are server wall-clock, the numbers stay
// representative — only the "client" is in-process, so no session/token
// plumbing is involved.
func (a *PlaybackLatencyAdmin) RunPlaybackBench(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	var req playbackBenchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.TitleID) == "" || strings.TrimSpace(req.TitleName) == "" || strings.TrimSpace(req.UserID) == "" {
		http.Error(w, "titleId, titleName and userId are required", http.StatusBadRequest)
		return
	}
	if req.Iterations <= 0 {
		req.Iterations = 10
	}
	if req.Iterations > 50 {
		req.Iterations = 50
	}
	if req.MediaType == "" {
		req.MediaType = "movie"
	}
	switch req.Scope {
	case "":
		req.Scope = "resolve"
	case "all", "resolve", "stream":
	default:
		http.Error(w, "invalid scope; use all, resolve or stream", http.StatusBadRequest)
		return
	}

	runID := fmt.Sprintf("bench-%d", time.Now().UnixNano())
	go a.runBench(runID, req)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"started": true, "runId": runID, "iterations": req.Iterations, "scope": req.Scope,
	})
}

func (a *PlaybackLatencyAdmin) runBench(runID string, req playbackBenchRequest) {
	if a.prequeueHandle == nil || a.benchStore == nil {
		log.Printf("[latency] bench %s: prequeue handler/store not wired; aborting", runID)
		return
	}
	log.Printf("[latency] bench %s start title=%q scope=%s iterations=%d", runID, req.TitleName, req.Scope, req.Iterations)

	for i := 1; i <= req.Iterations; i++ {
		iterStart := time.Now()
		if _, _, err := a.flushScope(req.Scope); err != nil {
			log.Printf("[latency] bench %s iter %d: flush: %v", runID, i, err)
			continue
		}
		entry, ok := a.benchStore.Create(req.TitleID, req.TitleName, req.UserID, req.MediaType, req.Year, nil, "details")
		if !ok || entry == nil {
			log.Printf("[latency] bench %s iter %d: failed to create prequeue entry", runID, i)
			continue
		}
		prequeueID := entry.ID
		if a.tracker != nil {
			a.tracker.NotePrequeueRequested(prequeueID, req.TitleID, req.UserID, req.TitleName, req.MediaType)
			a.tracker.NotePrequeueMetadata(prequeueID, req.ImdbID, req.Year)
		}
		// Runs synchronously through the real worker (search→resolve→probe→HLS).
		a.prequeueHandle.runPrequeueWorker(prequeueID, req.TitleID, req.TitleName, req.ImdbID,
			req.MediaType, req.Year, req.UserID, req.ClientID, nil, 0, false)

		sessionID := ""
		if cur, ok := a.benchStore.Get(prequeueID); ok && cur != nil {
			sessionID = cur.HLSSessionID
		}
		served := false
		if sessionID != "" && a.hlsDriver != nil {
			served = a.serveFirstSegment(sessionID)
		}
		// If no media segment was served (non-HLS stream, or generation failed)
		// there is no full sample yet: synthesize a prequeue-only row so the
		// iteration still surfaces with its prequeueMs. No-op when a full sample
		// already consumed the pending state.
		if a.tracker != nil {
			if _, readyAt := a.tracker.PrequeueTimes(prequeueID); !readyAt.IsZero() {
				a.tracker.NotePrequeueOnlySample(prequeueID)
			}
		}
		log.Printf("[latency] bench %s iter %d done hls=%t served=%t elapsed=%v", runID, i, sessionID != "", served, time.Since(iterStart))
	}
	log.Printf("[latency] bench %s complete", runID)
}

// serveFirstSegment requests the first media segment through the real
// ServeSegment path, which blocks until the transcode produces it (so t3/t4 and
// the complete sample are recorded when it succeeds, and no false 404 sample is
// created). Handles both TS and fMP4 sessions.
func (a *PlaybackLatencyAdmin) serveFirstSegment(sessionID string) bool {
	for _, name := range []string{"segment0.ts", "segment0.m4s"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/video/hls/"+sessionID+"/"+name, nil)
		a.hlsDriver.ServeSegment(rec, req, sessionID, name)
		if rec.Code == http.StatusOK {
			return true
		}
	}
	return false
}

// ClearLatencySamples drops the sample window.
func (a *PlaybackLatencyAdmin) ClearLatencySamples(w http.ResponseWriter, r *http.Request) {
	if a.tracker != nil {
		a.tracker.ClearSamples()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"cleared": true})
}

// FlushPlaybackCaches resets playback-related state so a repeat play is cold.
// The default scope ("all") clears everything: prequeue entries, HLS probe
// cache, hwaccel detection, live sessions (killing FFmpeg), the search cache,
// warm entries, the resolved-NZB cache and the NNTP pool (which lazily
// rebuilds). Narrower scopes keep the expensive orthogonal phases warm so a
// specific phase's latency can be isolated:
//
//	scope=all      — everything (default).
//	scope=resolve  — resolution cold, search warm: clears prequeue entries,
//	                 resolved-NZB cache, prewarm and the pool, but keeps the
//	                 indexer search cache (isolates resolve+parse, skips the
//	                 multi-minute search).
//	scope=stream   — transcode cold, everything else warm: clears the HLS probe
//	                 cache, hwaccel detection and live sessions only (isolates
//	                 ffmpeg input probing / first-segment warmup).
//
// The NNTP pool is only ever dropped (cold connections), never permanently
// disabled: it lazily rebuilds from the last configured providers.
func (a *PlaybackLatencyAdmin) FlushPlaybackCaches(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	scope := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("scope")))
	if scope == "" {
		scope = "all"
	}
	label, cleared, err := a.flushScope(scope)
	if err != nil {
		http.Error(w, "invalid scope; use all, resolve or stream", http.StatusBadRequest)
		return
	}
	log.Printf("[latency] playback cache flush complete scope=%s: %s", scope, strings.Join(cleared, ", "))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"scope": label, "cleared": cleared})
}

// flushScope clears the playback caches for one scope ("all" | "resolve" |
// "stream") and returns a human label + the list of what was cleared. Shared by
// the HTTP flush endpoint and the in-process benchmark.
func (a *PlaybackLatencyAdmin) flushScope(scope string) (string, []string, error) {
	cleared := []string{}
	switch scope {
	case "all", "resolve":
		if a.prequeueStore != nil {
			a.prequeueStore.DeleteAll()
			cleared = append(cleared, "prequeue entries")
		}
		if a.prewarmSvc != nil {
			a.prewarmSvc.ClearAll()
			cleared = append(cleared, "prewarm entries")
		}
		if a.importerSvc != nil {
			a.importerSvc.ClearResolvedNZBs()
			cleared = append(cleared, "resolved-NZB cache")
		}
		if a.poolManager != nil {
			if err := a.poolManager.ClearPool(); err != nil {
				log.Printf("[latency] flush: NNTP pool clear failed: %v", err)
			} else {
				cleared = append(cleared, "NNTP connection pool (lazy rebuild)")
			}
		}
		if scope == "all" && a.indexerSvc != nil {
			a.indexerSvc.ClearSearchCache()
			cleared = append(cleared, "indexer search cache")
		}
	case "stream":
		// only the transcode side below
	default:
		return "", nil, fmt.Errorf("invalid scope %q", scope)
	}

	// The transcode/stream side is always flushed (both "all" and "stream").
	if scope == "all" || scope == "stream" {
		if a.hlsManager != nil {
			if n, err := a.hlsManager.ClearPlaybackCaches(); err != nil {
				log.Printf("[latency] flush: HLS cache clear failed: %v", err)
			} else {
				cleared = append(cleared, fmt.Sprintf("HLS probe cache + %d session(s) killed", n))
			}
		}
	}

	switch scope {
	case "all":
		return "all (full cold)", cleared, nil
	case "resolve":
		return "resolve (search cached)", cleared, nil
	case "stream":
		return "stream (resolution cached)", cleared, nil
	}
	return "", cleared, fmt.Errorf("invalid scope %q", scope)
}

const latencyPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Playback Latency</title>
<style>
  body { font-family: ui-monospace, Menlo, monospace; font-size: 13px; background: #111; color: #ddd; margin: 24px; }
  h1 { font-size: 18px; color: #fff; }
  .chips { display: flex; gap: 12px; flex-wrap: wrap; margin: 12px 0 18px; }
  .chip { background: #1d2733; border: 1px solid #2e3c4d; border-radius: 8px; padding: 8px 14px; }
  .chip b { color: #7fd4ff; }
  table { border-collapse: collapse; width: 100%; }
  th, td { text-align: right; padding: 5px 10px; border-bottom: 1px solid #26303b; }
  th { color: #8aa0b4; font-weight: 600; position: sticky; top: 0; background: #111; }
  td.l, th.l { text-align: left; }
  .muted { color: #8aa0b4; }
  .good { color: #6fdc8c; } .slow { color: #ffc76f; } .bad { color: #ff7b7b; }
  button { background: #27445f; color: #fff; border: none; border-radius: 6px; padding: 8px 14px; cursor: pointer; }
  button.danger { background: #5f2e2e; }
  .row { display: flex; gap: 10px; align-items: center; margin-bottom: 10px; }
  .note { color: #8aa0b4; max-width: 1000px; line-height: 1.5; }
  button.mini { background: transparent; border: 1px solid #2e3c4d; border-radius: 5px;
    padding: 1px 9px; font-size: 13px; }
  button.mini:hover { background: #27445f; }
  .toast { position: fixed; bottom: 24px; right: 24px; background: #1f7a45; color: #fff;
    padding: 10px 16px; border-radius: 8px; opacity: 0; transition: opacity .25s; }
  .toast.show { opacity: 1; }
</style>
</head>
<body>
<h1>Playback Latency — click → first frame</h1>
<div class="note">
  Measures the server-side path: t0 = prequeue request (click), t1 = prequeue ready,
  t2 = HLS session created, t3 = first segment on disk, t4 = first segment response.
  Native (non-HLS) clients stop at t1 (see PREQUEUE_LATENCY in server logs).
  Click <b>Flush playback caches</b> before each test run to force a cold path,
  then re-play the same title in the app as many times as you like.
</div>
<div class="row">
  <button onclick="flushCaches('all')">🧊 Flush playback caches (cold test)</button>
  <button class="danger" onclick="clearSamples()">Clear samples</button>
</div>
<div class="row">
  <button onclick="flushCaches('resolve')" title="Resolution cold, multi-minute search kept warm; isolates resolve+parse">Flush resolution only</button>
  <button onclick="flushCaches('stream')" title="Transcode cold; keeps search + resolution warm; isolates ffmpeg/first-segment">Flush stream only</button>
  <span class="note">Tip: scope=resolve skips the ~90s search so a repeat play measures resolve+parse+transcode.</span>
</div>
<div class="chips" id="chips"></div>
<div class="toast" id="toast"></div>
<table>
  <thead><tr>
    <th class="l">#</th><th class="l">Time</th><th class="l">Title</th><th class="l">Service</th>
    <th>Total ms</th><th>Prequeue ms</th><th>HLS create ms</th><th>FFmpeg warmup ms</th><th>Serve wait ms</th><th>Complete</th><th class="l">Bench</th>
  </tr></thead>
  <tbody id="rows"></tbody>
</table>

<script>
async function api(path, opts) {
  const res = await fetch(path, opts);
  if (!res.ok) throw new Error(path + " -> " + res.status);
  return res.json();
}
function cls(ms) { if (ms < 0) return "muted"; if (ms < 5000) return "good"; if (ms < 15000) return "slow"; return "bad"; }
function fmtMs(ms) { return ms < 0 ? "–" : String(ms); }
function fmtTime(t) {
  if (!t) return "–";
  const d = new Date(t);
  return d.toLocaleTimeString() + "." + String(d.getMilliseconds()).padStart(3, "0");
}
async function refresh() {
  try {
    const snap = await api("/admin/api/latency?limit=60");
    const s = snap.stats;
    const chipData = [
      ["samples", snap.total], ["complete", snap.complete],
      ["total p50", s.totalMs.p50Ms + "ms"], ["total p95", s.totalMs.p95Ms + "ms"],
      ["total max", s.totalMs.maxMs + "ms"],
      ["prequeue p50", s.prequeueMs.p50Ms + "ms"], ["ffmpeg warmup p50", s.ffmpegWarmupMs.p50Ms + "ms"],
    ];
    document.getElementById("chips").innerHTML = chipData.map(function (c) {
      return '<div class="chip">' + c[0] + ": <b>" + c[1] + "</b></div>";
    }).join("");
    benchSamples = snap.samples || [];   // for the per-row ⚡ bench buttons
    var rows = snap.samples.map(function (x) {
      var note = (x.notes || []).join("; ");
      var title = x.titleName || "–";
      var prov = x.serviceProvider ? " (" + escapeHtml(x.serviceProvider) + ")" : "";
      var when = fmtTime(x.firstSegmentSentAt || x.clientRequestedAt);
      return '<tr>' +
        '<td class="l muted">' + x.id + '</td>' +
        '<td class="l muted" title="' + note + '">' + when + '</td>' +
        '<td class="l">' + escapeHtml(title) + '</td>' +
        '<td class="l muted">' + (x.serviceType || "–") + prov + '</td>' +
        '<td class="' + cls(x.totalMs) + '">' + fmtMs(x.totalMs) + '</td>' +
        '<td class="' + cls(x.prequeueMs) + '">' + fmtMs(x.prequeueMs) + '</td>' +
        '<td class="' + cls(x.hlsCreateMs) + '">' + fmtMs(x.hlsCreateMs) + '</td>' +
        '<td class="' + cls(x.ffmpegWarmupMs) + '">' + fmtMs(x.ffmpegWarmupMs) + '</td>' +
        '<td class="' + cls(x.serveWaitMs) + '">' + fmtMs(x.serveWaitMs) + '</td>' +
        '<td>' + (x.complete ? "✅" : "–") + '</td>' +
        '<td class="l">' +
          '<button class="mini" data-bench="' + x.id + '" title="Copy latency_bench.sh command prefilled for this release">⚡</button>' +
          ' <button class="mini" data-bench-run="' + x.id + '" title="Run 10× resolve bench on the server (no terminal/auth)">▶</button>' +
        '</td>' +
      '</tr>';
    }).join("");
    document.getElementById("rows").innerHTML = rows;
  } catch (e) {
    document.getElementById("rows").innerHTML = '<tr><td colspan="11" class="muted">' + e + '</td></tr>';
  }
}
function escapeHtml(s) { const d = document.createElement("div"); d.textContent = s; return d.innerHTML; }
async function flushCaches(scope) {
  var q = scope ? "?scope=" + scope : "";
  try {
    const r = await api("/admin/api/latency/flush" + q, { method: "POST" });
    alert("Flushed [" + r.scope + "]: " + r.cleared.join(", "));
  } catch (e) { alert("flush failed: " + e); }
}
async function clearSamples() {
  try { await api("/admin/api/latency/clear", { method: "POST" }); refresh(); }
  catch (e) { alert("clear failed: " + e); }
}

// ---- per-row benchmark command --------------------------------------------
// The ⚡ button on each sample row copies a fully-prefilled latency_bench.sh
// invocation for that measured release: BASE_URL from this page, the session
// token (which doubles as the Bearer for the protected playback/HLS routes),
// and the sample's USER_ID / TITLE_ID / TITLE_NAME. The release is pinned via
// -f so the benchmark summary only scores the exact release the operator
// already validated in the app.
var benchSamples = [];   // latest snapshot for row-button lookup
function findBenchSample(id) {
  for (var i = 0; i < benchSamples.length; i++) {
    if (benchSamples[i].id === id) return benchSamples[i];
  }
  return null;
}
function showToast(msg, color) {
  var el = document.getElementById("toast");
  el.textContent = msg;
  el.style.background = color || "#1f7a45";
  el.classList.add("show");
  setTimeout(function () { el.classList.remove("show"); }, 6000);
}
var __latencyBenchToken = null;
// The admin session cookie is HttpOnly, so JS cannot read it directly; the
// token comes from the master-only /admin/api/latency/session-token endpoint
// (fetched lazily, cached for the page lifetime).
async function getSessionToken() {
  if (__latencyBenchToken !== null) return __latencyBenchToken;
  try {
    var res = await fetch("/admin/api/latency/session-token");
    if (!res.ok) throw new Error(String(res.status));
    __latencyBenchToken = ((await res.json()).token) || "";
  } catch (e) {
    __latencyBenchToken = "";
  }
  return __latencyBenchToken;
}
function shq(v) { return "'" + String(v || "").replace(/'/g, "'\\''") + "'"; }

async function buildBenchCmdFor(s) {
  var token = await getSessionToken();
  var ext = [];
  if (s.year) ext.push("YEAR=" + shq(String(s.year)));
  if (s.imdbId) ext.push("IMDB_ID=" + shq(s.imdbId));
  var parts = [
    "BASE_URL=" + shq(window.location.origin),
    "TOKEN=" + shq(token),
    "ADMIN_COOKIE=" + shq("mediastorm_admin_session=" + token),
    "USER_ID=" + shq(s.userId),
    "TITLE_ID=" + shq(s.titleId),
    "TITLE_NAME=" + shq(s.titleName),
  ].concat(ext);
  var cmd = parts.join(" \\\n    ") +
    " \\\n    ./backend/scripts/latency_bench.sh -n 10 -s resolve";
  if (s.releaseName) {
    cmd += " -f " + shq(s.releaseName);   // score only this exact release
  }
  return cmd;
}

async function copyBenchRow(id) {
  var s = findBenchSample(id);
  if (!s) return;
  var token = await getSessionToken();
  if (!token) {
    showToast("⚠ No admin session token (length 0) — log in to /admin and reload this page, then re-copy.", "#8a5f1f");
    return;
  }
  var cmd = await buildBenchCmdFor(s);
  try {
    await navigator.clipboard.writeText(cmd);
  } catch (e) {
    var ta = document.createElement("textarea");
    ta.value = cmd; document.body.appendChild(ta); ta.select();
    document.execCommand("copy"); document.body.removeChild(ta);
  }
  var toast = document.getElementById("toast");
  showToast("Copied bench command (token length " + token.length + ") — " + (s.titleName || "?") +
    (s.releaseName ? "  " + s.releaseName : "") +
    "  (includes your session token — treat like a secret)", "#1f7a45");
}

// Run the benchmark entirely on the server: no tokens, no terminal. The samples
// land in this table as each iteration completes.
async function runBenchRow(id) {
  var s = findBenchSample(id);
  if (!s) return;
  if (!s.userId || !s.titleId || !s.titleName) {
    showToast("⚠ Sample lacks userId/titleId/titleName to benchmark.", "#8a5f1f");
    return;
  }
  try {
    var res = await fetch("/admin/api/latency/bench", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        titleId: s.titleId, titleName: s.titleName, userId: s.userId,
        mediaType: s.mediaType || "movie", year: s.year || 0, imdbId: s.imdbId || "",
        iterations: 10, scope: "resolve",
      }),
    });
    var payload = await res.json().catch(function () { return {}; });
    if (!res.ok) {
      showToast("⚠ Bench start failed (HTTP " + res.status + ") — " + JSON.stringify(payload), "#8a5f1f");
      return;
    }
    showToast("▶ Bench started: " + (s.titleName || "?") + " · " + payload.iterations + "× flush " + payload.scope +
      " — watch the table fill in (server-side, no auth needed).");
  } catch (e) {
    showToast("⚠ Bench start failed: " + e, "#8a5f1f");
  }
}

// Event delegation keeps the table rows free of inline handlers.
document.getElementById("rows").addEventListener("click", function (ev) {
  var t = ev.target;
  var copyBtn = t.closest ? t.closest("[data-bench]") : null;
  var runBtn = t.closest ? t.closest("[data-bench-run]") : null;
  if (runBtn) runBenchRow(runBtn.getAttribute("data-bench-run"));
  else if (copyBtn) copyBenchRow(copyBtn.getAttribute("data-bench"));
});
setInterval(refresh, 2500);
refresh();
</script>
</body>
</html>
`
