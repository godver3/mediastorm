package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
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
	TitleName  string `json:"titleName,omitempty"`
	MediaType  string `json:"mediaType,omitempty"`

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
	titleName   string
	mediaType   string
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
func (t *PlaybackLatencyTracker) NotePrequeueRequested(prequeueID, titleID, titleName, mediaType string) {
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
		if s.TitleName == "" {
			s.TitleName = p.titleName
		}
		if s.MediaType == "" {
			s.MediaType = p.mediaType
		}
		delete(t.pending, prequeueID)
	}
	s = deriveLatencyDurations(s)
	t.seq++
	s.ID = fmt.Sprintf("L%d", t.seq)
	t.samples = append(t.samples, s)
	if len(t.samples) > t.max {
		t.samples = t.samples[len(t.samples)-t.max:]
	}
	totalMs := s.TotalMs
	complete := s.Complete
	sessionID := s.SessionID
	t.mu.Unlock()

	log.Printf("[latency] PLAYBACK_LATENCY total=%dms prequeue=%dms hlsCreate=%dms ffmpegWarmup=%dms serveWait=%dms service=%s title=%q complete=%v prequeueId=%s session=%s",
		totalMs, s.PrequeueMs, s.HLSCreateMs, s.FFmpegWarmupMs, s.ServeWaitMs,
		orUnknown(s.ServiceType), s.TitleName, complete, prequeueID, sessionID)
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
		if !s.Complete {
			continue
		}
		snap.Complete++
		totalVals = append(totalVals, s.TotalMs)
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

type latencyPrequeueFlusher interface{ DeleteAll() }
type latencyIndexerFlusher interface{ ClearSearchCache() }
type latencyPrewarmFlusher interface{ ClearAll() }
type latencyPoolFlusher interface{ ClearPool() error }
type latencyImporterFlusher interface{ ClearResolvedNZBs() }

// PlaybackLatencyAdmin exposes the latency window and the cold-test cache
// flush. All dependencies are optional (nil-safe) so tests and reduced setups
// can register what they have.
type PlaybackLatencyAdmin struct {
	tracker       *PlaybackLatencyTracker
	prequeueStore latencyPrequeueFlusher
	indexerSvc    latencyIndexerFlusher
	prewarmSvc    latencyPrewarmFlusher
	poolManager   latencyPoolFlusher
	importerSvc   latencyImporterFlusher
	hlsManager    playbackLatencyFlusher
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

	cleared := []string{}
	flushScope := ""

	if scope == "all" || scope == "resolve" {
		if a.prequeueStore != nil {
			a.prequeueStore.DeleteAll()
		}
		cleared = append(cleared, "prequeue entries")
	}
	if scope == "all" || scope == "resolve" {
		if a.prewarmSvc != nil {
			a.prewarmSvc.ClearAll()
		}
		cleared = append(cleared, "prewarm entries")
	}
	if scope == "all" || scope == "resolve" {
		if a.importerSvc != nil {
			a.importerSvc.ClearResolvedNZBs()
		}
		cleared = append(cleared, "resolved-NZB cache")
	}
	if scope == "all" || scope == "resolve" {
		if a.poolManager != nil {
			if err := a.poolManager.ClearPool(); err != nil {
				log.Printf("[latency] flush: NNTP pool clear failed: %v", err)
			} else {
				cleared = append(cleared, "NNTP connection pool (lazy rebuild)")
			}
		}
	}
	if scope == "all" {
		if a.indexerSvc != nil {
			a.indexerSvc.ClearSearchCache()
		}
		cleared = append(cleared, "indexer search cache")
	}

	// The transcode/stream side is always flushed (both "all" and "stream"):
	// probe cache, hwaccel detection and any live HLS sessions.
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
		flushScope = "all (full cold)"
	case "resolve":
		flushScope = "resolve (search cached)"
	case "stream":
		flushScope = "stream (resolution cached)"
	default:
		http.Error(w, "invalid scope; use all, resolve or stream", http.StatusBadRequest)
		return
	}

	log.Printf("[latency] playback cache flush complete scope=%s: %s", scope, strings.Join(cleared, ", "))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"scope": flushScope, "cleared": cleared})
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
<table>
  <thead><tr>
    <th class="l">#</th><th class="l">Time</th><th class="l">Title</th><th class="l">Service</th>
    <th>Total ms</th><th>Prequeue ms</th><th>HLS create ms</th><th>FFmpeg warmup ms</th><th>Serve wait ms</th><th>Complete</th>
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
      '</tr>';
    }).join("");
    document.getElementById("rows").innerHTML = rows;
  } catch (e) {
    document.getElementById("rows").innerHTML = '<tr><td colspan="10" class="muted">' + e + '</td></tr>';
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
setInterval(refresh, 2500);
refresh();
</script>
</body>
</html>
`
