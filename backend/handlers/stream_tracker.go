package handlers

import (
	"context"
	"log"
	"math"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"novastream/internal/auth"
	"novastream/internal/mediaidentity"
	"novastream/internal/requestsecurity"
	"novastream/models"
)

type playbackAutoSeeder interface {
	OnPlaybackStarted(models.PlaybackProgressUpdate)
}

// StreamTracker tracks active video streams for monitoring
type StreamTracker struct {
	streams          map[string]*TrackedStream
	recentlyEnded    map[string]recentlyEndedStream
	stopPlaybacks    map[string]time.Time
	migrationSignals map[string]playbackMigrationSignal
	mu               sync.RWMutex
	counter          uint64
	playbackObserver PlaybackActivityObserver
	autoSeeder       playbackAutoSeeder
}

type recentlyEndedStream struct {
	stream  *TrackedStream
	endedAt time.Time
}

type playbackMigrationSignal struct {
	reason    string
	expiresAt time.Time
}

func playbackMigrationSignalPriority(reason string) int {
	switch strings.TrimSpace(reason) {
	case "backend-source-failure":
		return 5
	case "backend-provider-unavailable":
		return 4
	case "backend-starvation":
		return 3
	case "backend-low-throughput":
		return 2
	case "backend-delivery-starvation":
		return 1
	default:
		return 0
	}
}

// TrackedStream represents an active direct video stream
type TrackedStream struct {
	ID              string
	Path            string
	Filename        string
	ClientIP        string
	ClientID        string
	ProfileID       string
	ProfileName     string
	AccountID       string
	StartTime       time.Time
	LastActivity    time.Time
	BytesStreamed   int64
	ContentLength   int64
	RangeStart      int64
	RangeEnd        int64
	Method          string
	UserAgent       string
	ViaShareLink    bool // stream authenticated by a one-time share link
	MediaMetadata   StreamMediaMetadata
	SharePosition   float64
	ShareDuration   float64
	SharePercent    float64
	ShareUpdatedAt  time.Time
	SharePaused     bool
	ShareBuffering  bool
	ThroughputBps   int64 // instantaneous transfer rate in bits/sec (snapshot on read)
	cancel          context.CancelFunc
	bytesCounter    *int64
	activityCounter *int64 // unix nanos of last byte transfer, updated atomically
	// Rolling throughput sample state, updated atomically (independent of mu).
	lastSampleBytes int64
	lastSampleNanos int64
	throughputBps   int64
	// Upstream health is kept separate from dashboard/client throughput. The
	// proxy handlers feed only time spent inside provider Read calls, so native
	// player backpressure cannot manufacture a low-throughput signal.
	requiredBps                   int64
	upstreamHealthLowSamples      int32
	upstreamHealthLastSignalNanos int64
}

// throughputSampleInterval is the minimum window between throughput samples.
// A guard plus CAS makes sampling safe regardless of how many callers
// (REST + multiple SSE connections) read the tracker concurrently.
const throughputSampleInterval = 2 * time.Second

// throughputSmoothingTau is the time constant for the exponentially-weighted
// moving average of transfer rate. Byte delivery is bursty (a window may catch a
// full burst or an idle gap), so a raw per-window rate bounces between ~0 and the
// peak. Smoothing over ~this many seconds of observed data yields a steady,
// representative speed instead. Larger = smoother but slower to react.
const throughputSmoothingTau = 20 * time.Second

// sampleThroughput updates an exponentially-weighted moving average of the
// bits/sec transfer rate from a cumulative byte counter, and returns the smoothed
// rate. It mutates the provided sample-state pointers atomically, so callers may
// hold an RLock (or no lock) — all access here is via atomics, not the mutex.
//
// The EWMA averages over the observed transfer period rather than reporting a
// single window's delta, so steady playback reads a stable number even though the
// underlying delivery arrives in bursts.
func sampleThroughput(bytesNow int64, lastBytes, lastNanos, outBps *int64) int64 {
	nowNanos := time.Now().UnixNano()
	prevNanos := atomic.LoadInt64(lastNanos)
	if prevNanos == 0 {
		// Seed the baseline on first observation; no rate yet.
		if atomic.CompareAndSwapInt64(lastNanos, 0, nowNanos) {
			atomic.StoreInt64(lastBytes, bytesNow)
		}
		return atomic.LoadInt64(outBps)
	}
	elapsed := nowNanos - prevNanos
	if elapsed < int64(throughputSampleInterval) {
		return atomic.LoadInt64(outBps)
	}
	// Claim this window so concurrent callers don't double-sample. The winner is
	// the sole writer of lastBytes/outBps for this window.
	if !atomic.CompareAndSwapInt64(lastNanos, prevNanos, nowNanos) {
		return atomic.LoadInt64(outBps)
	}
	prevBytes := atomic.SwapInt64(lastBytes, bytesNow)
	deltaBytes := bytesNow - prevBytes
	if deltaBytes < 0 {
		deltaBytes = 0
	}
	// Instantaneous rate over this window (float to avoid overflow at Gbps rates).
	windowBps := float64(deltaBytes) * 8 * float64(time.Second) / float64(elapsed)

	prev := atomic.LoadInt64(outBps)
	if prev <= 0 {
		// First real measurement: seed directly so the display converges quickly
		// instead of ramping up from zero.
		smoothed := int64(windowBps)
		atomic.StoreInt64(outBps, smoothed)
		return smoothed
	}
	// Time-based EWMA so the smoothing is consistent regardless of how long the
	// gap between samples was: alpha = 1 - e^(-elapsed/tau).
	alpha := 1 - math.Exp(-float64(elapsed)/float64(throughputSmoothingTau))
	smoothed := int64(float64(prev) + alpha*(windowBps-float64(prev)))
	if smoothed < 0 {
		smoothed = 0
	}
	atomic.StoreInt64(outBps, smoothed)
	return smoothed
}

// throughputSamplerInterval is how often the background sampler refreshes each
// active stream's throughput EWMA so the dashboard shows live speeds immediately
// on open, even when no dashboard was connected to drive sampling.
const throughputSamplerInterval = 5 * time.Second

const (
	// Client-write throughput is deliberately dashboard-only. HTTP writes are
	// paced by the native player's demand and buffer backpressure. Provider Read
	// service rate, by contrast, directly measures whether the source can keep up.
	upstreamHealthMinRequiredBps     = int64(1_000_000)
	upstreamHealthHeadroom           = 1.25
	upstreamHealthLowSamples         = int32(2)
	upstreamHealthSignalCooldown     = 30 * time.Second
	migrationPreparationBufferRunway = 15.0
)

// SampleThroughput refreshes the throughput EWMA for every active direct stream.
// Work is proportional to the number of active streams (a few atomics + one
// exp() each), so it is effectively free when there are none.
func (t *StreamTracker) SampleThroughput() {
	t.mu.RLock()
	for _, s := range t.streams {
		if s.bytesCounter == nil {
			continue
		}
		sampleThroughput(atomic.LoadInt64(s.bytesCounter), &s.lastSampleBytes, &s.lastSampleNanos, &s.throughputBps)
	}
	t.mu.RUnlock()
}

// StartThroughputSampler launches a background goroutine that keeps each active
// stream's throughput EWMA warm regardless of whether a dashboard is connected.
// It samples both direct streams (this tracker) and HLS sessions every
// throughputSamplerInterval. Because the per-tick cost scales with the number of
// active streams and is negligible (microseconds even for dozens of streams), the
// ticker simply no-ops when nothing is streaming.
func StartThroughputSampler(ctx context.Context, hls *HLSManager) {
	go func() {
		ticker := time.NewTicker(throughputSamplerInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				globalStreamTracker.SampleThroughput()
				if hls != nil {
					hls.SampleThroughput()
				}
			}
		}
	}()
}

// StreamUsageSummary represents the current stream usage for an account or profile.
type StreamUsageSummary struct {
	CurrentStreams   int  `json:"currentStreams"`
	MaxStreams       int  `json:"maxStreams"`
	AvailableStreams int  `json:"availableStreams"`
	AtLimit          bool `json:"atLimit"`
}

// Global stream tracker instance
var globalStreamTracker = &StreamTracker{
	streams:          make(map[string]*TrackedStream),
	stopPlaybacks:    make(map[string]time.Time),
	migrationSignals: make(map[string]playbackMigrationSignal),
}

const playbackStopSignalDuration = 2 * time.Minute
const playbackMigrationSignalDuration = 30 * time.Second
const playbackNotificationTeardownGrace = 30 * time.Second

// GetStreamTracker returns the global stream tracker
func GetStreamTracker() *StreamTracker {
	return globalStreamTracker
}

// AddPlaybackActivityObserver registers one more consumer of matched playback
// activity. See playbackObserverFanout for why there is more than one.
func (t *StreamTracker) AddPlaybackActivityObserver(observer PlaybackActivityObserver) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.playbackObserver = addPlaybackObserver(t.playbackObserver, observer)
	t.mu.Unlock()
}

// SetPlaybackAutoSeeder registers the p2p integration on the only playback
// signal a native player produces.
func (t *StreamTracker) SetPlaybackAutoSeeder(seeder playbackAutoSeeder) {
	if t == nil || seeder == nil {
		return
	}
	t.mu.Lock()
	t.autoSeeder = seeder
	t.mu.Unlock()
}

// AssociateClientWithPlayback binds the authenticated app client sending a
// playback heartbeat to its active direct transport connections. Older native
// app bundles did not include clientId in the media URL, even though their API
// heartbeats carry X-Client-ID, so the dashboard could not resolve a device.
func (t *StreamTracker) AssociateClientWithPlayback(userID string, update models.PlaybackProgressUpdate, clientID string) int {
	if t == nil || strings.TrimSpace(clientID) == "" {
		return 0
	}
	targetKey := playbackControlKey(userID, update.MediaType, update.ItemID)
	if targetKey == "" {
		return 0
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	matched := 0
	for _, stream := range t.streams {
		matches := false
		for _, key := range streamPlaybackControlKeys(stream) {
			if key == targetKey {
				matches = true
				break
			}
		}
		if !matches {
			continue
		}
		if update.SourcePath != "" && normalizeStreamFailurePath(stream.Path) != normalizeStreamFailurePath(update.SourcePath) {
			continue
		}
		stream.ClientID = strings.TrimSpace(clientID)
		matched++
	}
	return matched
}

// ObservePlaybackActivity matches a player heartbeat to the most recently
// active direct stream before forwarding it to the notification observer.
func (t *StreamTracker) ObservePlaybackActivity(userID string, update models.PlaybackProgressUpdate, percentWatched float64) int {
	if t == nil {
		return 0
	}
	targetKey := playbackControlKey(userID, update.MediaType, update.ItemID)

	t.mu.RLock()
	observer := t.playbackObserver
	var best *TrackedStream
	for _, stream := range t.streams {
		matches := false
		for _, key := range streamPlaybackControlKeys(stream) {
			if key == targetKey {
				matches = true
				break
			}
		}
		if !matches {
			continue
		}
		if update.SourcePath != "" && normalizeStreamFailurePath(stream.Path) != normalizeStreamFailurePath(update.SourcePath) {
			continue
		}
		if best == nil || stream.LastActivity.After(best.LastActivity) {
			best = stream
		}
	}
	if best == nil {
		now := time.Now()
		var bestEndedAt time.Time
		for _, ended := range t.recentlyEnded {
			if ended.stream == nil || now.Sub(ended.endedAt) > playbackNotificationTeardownGrace {
				continue
			}
			matches := false
			for _, key := range streamPlaybackControlKeys(ended.stream) {
				if key == targetKey {
					matches = true
					break
				}
			}
			if !matches {
				continue
			}
			if update.SourcePath != "" && normalizeStreamFailurePath(ended.stream.Path) != normalizeStreamFailurePath(update.SourcePath) {
				continue
			}
			if best == nil || ended.endedAt.After(bestEndedAt) {
				best = ended.stream
				bestEndedAt = ended.endedAt
			}
		}
	}
	if best != nil {
		// Native players open multiple short-lived byte-range connections for a
		// single playback. Key notifications like the consolidated Active
		// Streams row, not by an individual transport connection.
		update.PlaybackSessionID = "direct:" + trackedStreamSlotKey(best)
		// A player that reported no source path still has one: the request this
		// heartbeat was matched to names it. Consumers that must reach the source
		// again, rather than merely name it, depend on that.
		if strings.TrimSpace(update.SourcePath) == "" {
			update.SourcePath = best.Path
		}
		update = enrichPlaybackUpdateFromStream(update, best.MediaMetadata)
	}
	t.mu.RUnlock()
	if observer == nil || best == nil {
		return 0
	}
	go observer.HandlePlaybackUpdate(userID, update, percentWatched)
	return 1
}

func enrichPlaybackUpdateFromStream(update models.PlaybackProgressUpdate, meta StreamMediaMetadata) models.PlaybackProgressUpdate {
	if update.MediaType == "" {
		update.MediaType = meta.MediaType
	}
	if update.ItemID == "" {
		update.ItemID = meta.ItemID
	}
	if update.MovieName == "" {
		update.MovieName = firstStreamValue(meta.MovieName, meta.Title)
	}
	if update.SeriesName == "" {
		update.SeriesName = firstStreamValue(meta.SeriesName, meta.Title)
	}
	if update.EpisodeName == "" {
		update.EpisodeName = meta.EpisodeName
	}
	if update.SeriesID == "" {
		update.SeriesID = meta.SeriesID
	}
	if update.SeasonNumber == 0 {
		update.SeasonNumber = meta.SeasonNumber
	}
	if update.EpisodeNumber == 0 {
		update.EpisodeNumber = meta.EpisodeNumber
	}
	if update.Year == 0 {
		update.Year = meta.Year
	}
	if update.PosterURL == "" {
		update.PosterURL = meta.PosterURL
	}
	if update.NotificationImageURL == "" {
		update.NotificationImageURL = meta.NotificationImageURL
	}
	if len(update.ExternalIDs) == 0 && len(meta.ExternalIDs) > 0 {
		update.ExternalIDs = meta.ExternalIDs
	}
	return update
}

func firstStreamValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// StartStream registers a new stream and returns its ID, a bytes counter, and an activity timestamp counter.
// The caller should atomically update the bytes counter with total bytes transferred
// and the activity counter with time.Now().UnixNano() on each write.
func (t *StreamTracker) StartStream(r *http.Request, path string, contentLength int64, rangeStart, rangeEnd int64) (string, *int64, *int64) {
	return t.StartStreamWithAccount(r, path, contentLength, rangeStart, rangeEnd, "")
}

// StartStreamWithAccount is like StartStream but also records the account ID.
func (t *StreamTracker) StartStreamWithAccount(r *http.Request, path string, contentLength int64, rangeStart, rangeEnd int64, accountID string) (string, *int64, *int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneStopSignalsLocked()
	t.pruneRecentlyEndedLocked(time.Now())

	id := generateStreamID(atomic.AddUint64(&t.counter, 1))

	// Get client IP
	clientIP := getClientIP(r)

	// Extract filename
	filename := filepath.Base(path)

	// Extract profile info from query params
	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		profileID = r.URL.Query().Get("userId")
	}
	profileName := r.URL.Query().Get("profileName")

	now := time.Now()
	bytesCounter := new(int64)
	activityCounter := new(int64)
	*activityCounter = now.UnixNano()

	stream := &TrackedStream{
		ID:              id,
		Path:            path,
		Filename:        filename,
		ClientIP:        clientIP,
		ClientID:        requestClientID(r),
		ProfileID:       profileID,
		ProfileName:     profileName,
		AccountID:       accountID,
		StartTime:       now,
		LastActivity:    now,
		ContentLength:   contentLength,
		RangeStart:      rangeStart,
		RangeEnd:        rangeEnd,
		Method:          r.Method,
		UserAgent:       r.UserAgent(),
		ViaShareLink:    auth.IsShareLinkRequest(r),
		MediaMetadata:   parseStreamMediaMetadata(r),
		bytesCounter:    bytesCounter,
		activityCounter: activityCounter,
	}

	newPlayback := !t.hasPlaybackSlotLocked(nil, trackedStreamSlotKey(stream))
	t.streams[id] = stream
	if newPlayback {
		t.observePlaybackStartLocked(stream)
	}
	return id, bytesCounter, activityCounter
}

// observePlaybackStartLocked hands a newly opened playback to the auto-seeder.
// It never blocks or fails the viewer's stream.
func (t *StreamTracker) observePlaybackStartLocked(stream *TrackedStream) {
	if t.autoSeeder == nil {
		return
	}
	update := enrichPlaybackUpdateFromStream(models.PlaybackProgressUpdate{
		SourcePath:        stream.Path,
		Timestamp:         stream.StartTime,
		PlaybackSessionID: "direct:" + trackedStreamSlotKey(stream),
	}, stream.MediaMetadata)
	// A stream's coordinates come from the query the player opened it with, so a
	// request that omits mediaType or itemId can never be archived - and used to
	// say nothing at all, which is indistinguishable from archiving being off.
	// Observed live: a usenet title streamed for minutes with no seed attempt and
	// no log line explaining the silence.
	if update.MediaType == "" || update.MediaType == "live" || update.ItemID == "" {
		if update.MediaType != "live" {
			log.Printf("[peartube] playback not archivable: no swarm coordinates on the stream request (mediaType=%q itemId=%q path=%q)",
				update.MediaType, update.ItemID, stream.Path)
		}
		return
	}
	seeder := t.autoSeeder
	go seeder.OnPlaybackStarted(update)
}

// SetStreamCancel attaches a cancellation function to a tracked stream.
func (t *StreamTracker) SetStreamCancel(id string, cancel context.CancelFunc) bool {
	if cancel == nil {
		return false
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	stream, ok := t.streams[id]
	if !ok {
		return false
	}
	stream.cancel = cancel
	return true
}

func (t *StreamTracker) pruneStopSignalsLocked() {
	if t.stopPlaybacks == nil {
		t.stopPlaybacks = make(map[string]time.Time)
	}
	now := time.Now()
	for key, expiresAt := range t.stopPlaybacks {
		if !expiresAt.After(now) {
			delete(t.stopPlaybacks, key)
		}
	}
}

func (t *StreamTracker) pruneMigrationSignalsLocked() {
	if t.migrationSignals == nil {
		t.migrationSignals = make(map[string]playbackMigrationSignal)
	}
	now := time.Now()
	for key, signal := range t.migrationSignals {
		if !signal.expiresAt.After(now) {
			delete(t.migrationSignals, key)
		}
	}
}

func playbackControlKey(userID, mediaType, itemID string) string {
	return strings.ToLower(strings.TrimSpace(userID)) + "|" +
		strings.ToLower(strings.TrimSpace(mediaType)) + "|" +
		strings.ToLower(strings.TrimSpace(itemID))
}

func playbackMigrationKey(controlKey, sourcePath string) string {
	normalizedPath := normalizeStreamFailurePath(sourcePath)
	if normalizedPath == "" {
		return controlKey
	}
	return controlKey + "\x00" + normalizedPath
}

func (t *StreamTracker) playbackMigrationSignalLocked(controlKey, sourcePath string) (string, playbackMigrationSignal, bool) {
	if normalizedPath := normalizeStreamFailurePath(sourcePath); normalizedPath != "" {
		key := playbackMigrationKey(controlKey, normalizedPath)
		signal, ok := t.migrationSignals[key]
		return key, signal, ok
	}

	// Backward compatibility for clients that do not yet report sourcePath:
	// return the freshest recommendation associated with this playback item.
	prefix := controlKey + "\x00"
	bestKey := ""
	bestSignal := playbackMigrationSignal{}
	for key, signal := range t.migrationSignals {
		if key != controlKey && !strings.HasPrefix(key, prefix) {
			continue
		}
		if bestKey == "" || playbackMigrationSignalPriority(signal.reason) > playbackMigrationSignalPriority(bestSignal.reason) ||
			(playbackMigrationSignalPriority(signal.reason) == playbackMigrationSignalPriority(bestSignal.reason) && signal.expiresAt.After(bestSignal.expiresAt)) {
			bestKey = key
			bestSignal = signal
		}
	}
	return bestKey, bestSignal, bestKey != ""
}

func (t *StreamTracker) recordPlaybackMigrationSignalLocked(key string, signal playbackMigrationSignal) {
	existing, ok := t.migrationSignals[key]
	if ok && existing.expiresAt.After(time.Now()) &&
		playbackMigrationSignalPriority(existing.reason) > playbackMigrationSignalPriority(signal.reason) {
		return
	}
	t.migrationSignals[key] = signal
}

func streamPlaybackControlKeys(stream *TrackedStream) []string {
	if stream == nil || strings.TrimSpace(stream.MediaMetadata.ItemID) == "" {
		return nil
	}

	seen := make(map[string]bool)
	var keys []string
	add := func(userID string) {
		if strings.TrimSpace(userID) == "" {
			return
		}
		key := playbackControlKey(userID, stream.MediaMetadata.MediaType, stream.MediaMetadata.ItemID)
		if seen[key] {
			return
		}
		seen[key] = true
		keys = append(keys, key)
	}

	add(stream.ProfileID)
	add(stream.ProfileName)
	return keys
}

func trackedStreamSlotKey(stream *TrackedStream) string {
	if stream == nil {
		return ""
	}
	return streamSlotKey(stream.ProfileID, stream.ProfileName, stream.ClientIP, stream.MediaMetadata.MediaType, stream.MediaMetadata.ItemID, stream.Path)
}

func requestStreamSlotKey(r *http.Request, path string) string {
	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		profileID = r.URL.Query().Get("userId")
	}
	metadata := parseStreamMediaMetadata(r)
	return streamSlotKey(profileID, r.URL.Query().Get("profileName"), getClientIP(r), metadata.MediaType, metadata.ItemID, path)
}

func streamSlotKey(profileID, profileName, clientIP, mediaType, itemID, path string) string {
	profile := strings.ToLower(strings.TrimSpace(profileID))
	if profile == "" {
		profile = strings.ToLower(strings.TrimSpace(profileName))
	}
	if profile == "" {
		profile = "ip:" + strings.ToLower(strings.TrimSpace(clientIP))
	}

	mediaKey := strings.ToLower(strings.TrimSpace(itemID))
	if mediaKey != "" {
		mediaType = strings.ToLower(strings.TrimSpace(mediaType))
		if mediaType != "" {
			mediaKey = mediaType + ":" + mediaKey
		}
	} else {
		mediaKey = "path:" + strings.ToLower(strings.TrimSpace(path))
	}

	return profile + "|" + mediaKey
}

// MarkStopPlaybackForProfileMedia marks a profile/media pair as disallowed on heartbeat.
func (t *StreamTracker) MarkStopPlaybackForProfileMedia(profileID, profileName, mediaType, itemID string) bool {
	if strings.TrimSpace(itemID) == "" {
		return false
	}

	seen := make(map[string]bool)
	var keys []string
	add := func(userID string) {
		if strings.TrimSpace(userID) == "" {
			return
		}
		key := playbackControlKey(userID, mediaType, itemID)
		if seen[key] {
			return
		}
		seen[key] = true
		keys = append(keys, key)
	}
	add(profileID)
	add(profileName)
	if len(keys) == 0 {
		return false
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneStopSignalsLocked()

	expiresAt := time.Now().Add(playbackStopSignalDuration)
	for _, key := range keys {
		t.stopPlaybacks[key] = expiresAt
	}
	return true
}

// MarkStopPlayback marks the playback tied to a tracked stream as disallowed on heartbeat.
func (t *StreamTracker) MarkStopPlayback(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneStopSignalsLocked()

	stream, ok := t.streams[id]
	if !ok {
		return false
	}

	keys := streamPlaybackControlKeys(stream)
	if len(keys) == 0 {
		return false
	}
	expiresAt := time.Now().Add(playbackStopSignalDuration)
	for _, key := range keys {
		t.stopPlaybacks[key] = expiresAt
	}
	return true
}

// ShouldStopPlayback reports whether the player should stop on its progress heartbeat.
func (t *StreamTracker) ShouldStopPlayback(userID string, update models.PlaybackProgressUpdate) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneStopSignalsLocked()

	key := playbackControlKey(userID, update.MediaType, update.ItemID)
	expiresAt, ok := t.stopPlaybacks[key]
	if !ok || !expiresAt.After(time.Now()) {
		return false
	}
	delete(t.stopPlaybacks, key)
	return true
}

// MarkPlaybackMigration records a short-lived recommendation for the player
// associated with an active stream to switch to its next ranked candidate.
// The recommendation is delivered through the next playback-progress response
// once the player is buffering or reports critically low buffer runway.
func (t *StreamTracker) MarkPlaybackMigration(id, reason string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneMigrationSignalsLocked()

	stream, ok := t.streams[id]
	if !ok {
		return false
	}
	keys := streamPlaybackControlKeys(stream)
	if len(keys) == 0 {
		return false
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "backend-starvation"
	}
	signal := playbackMigrationSignal{reason: reason, expiresAt: time.Now().Add(playbackMigrationSignalDuration)}
	for _, key := range keys {
		t.recordPlaybackMigrationSignalLocked(playbackMigrationKey(key, stream.Path), signal)
	}
	return true
}

// MarkPlaybackMigrationForPath applies a recommendation to every active stream
// reading the normalized path. This lets shared stream-pool readers notify the
// correct player without coupling pool slots to individual HTTP requests.
func (t *StreamTracker) MarkPlaybackMigrationForPath(path, reason string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneMigrationSignalsLocked()

	normalized := normalizeStreamFailurePath(path)
	if normalized == "" {
		return 0
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "backend-starvation"
	}
	signal := playbackMigrationSignal{reason: reason, expiresAt: time.Now().Add(playbackMigrationSignalDuration)}
	marked := 0
	for _, stream := range t.streams {
		if normalizeStreamFailurePath(stream.Path) != normalized {
			continue
		}
		keys := streamPlaybackControlKeys(stream)
		if len(keys) == 0 {
			continue
		}
		for _, key := range keys {
			t.recordPlaybackMigrationSignalLocked(playbackMigrationKey(key, stream.Path), signal)
		}
		marked++
	}
	return marked
}

// ObservePlaybackBandwidth binds the active release's estimated average bitrate
// to matching direct streams. SourcePath keeps an older range request from
// inheriting the replacement source's requirement during a handoff.
func (t *StreamTracker) ObservePlaybackBandwidth(userID string, update models.PlaybackProgressUpdate) int {
	if update.RequiredMbps == nil || *update.RequiredMbps <= 0 || math.IsNaN(*update.RequiredMbps) || math.IsInf(*update.RequiredMbps, 0) {
		return 0
	}
	required := int64(*update.RequiredMbps * 1_000_000)
	if required < upstreamHealthMinRequiredBps {
		return 0
	}
	targetKey := playbackControlKey(userID, update.MediaType, update.ItemID)

	t.mu.RLock()
	defer t.mu.RUnlock()
	matched := 0
	for _, stream := range t.streams {
		matchesPlayback := false
		for _, key := range streamPlaybackControlKeys(stream) {
			if key == targetKey {
				matchesPlayback = true
				break
			}
		}
		if !matchesPlayback {
			continue
		}
		if update.SourcePath != "" && normalizeStreamFailurePath(stream.Path) != normalizeStreamFailurePath(update.SourcePath) {
			continue
		}

		previous := atomic.SwapInt64(&stream.requiredBps, required)
		materialChange := previous <= 0 || math.Abs(float64(previous-required))/float64(required) > 0.10
		if materialChange {
			atomic.StoreInt32(&stream.upstreamHealthLowSamples, 0)
			atomic.StoreInt64(&stream.upstreamHealthLastSignalNanos, 0)
		}
		matched++
	}
	return matched
}

// ObserveUpstreamThroughput compares one provider-read service window with the
// active release bitrate. activeReadDuration excludes time spent writing to a
// backpressured client, so only a source that cannot serve bytes fast enough can
// arm this predictive signal. Two consecutive deficient windows are required.
func (t *StreamTracker) ObserveUpstreamThroughput(streamID string, bytes int64, activeReadDuration time.Duration) bool {
	if bytes <= 0 || activeReadDuration <= 0 {
		return false
	}

	t.mu.RLock()
	stream := t.streams[streamID]
	t.mu.RUnlock()
	if stream == nil {
		return false
	}
	required := atomic.LoadInt64(&stream.requiredBps)
	if required < upstreamHealthMinRequiredBps {
		atomic.StoreInt32(&stream.upstreamHealthLowSamples, 0)
		return false
	}

	actual := int64(float64(bytes) * 8 * float64(time.Second) / float64(activeReadDuration))
	threshold := int64(float64(required) * upstreamHealthHeadroom)
	if actual >= threshold {
		atomic.StoreInt32(&stream.upstreamHealthLowSamples, 0)
		return false
	}
	if atomic.AddInt32(&stream.upstreamHealthLowSamples, 1) < upstreamHealthLowSamples {
		return false
	}

	nowNanos := time.Now().UnixNano()
	lastSignal := atomic.LoadInt64(&stream.upstreamHealthLastSignalNanos)
	if lastSignal > 0 && time.Duration(nowNanos-lastSignal) < upstreamHealthSignalCooldown {
		return false
	}
	if !atomic.CompareAndSwapInt64(&stream.upstreamHealthLastSignalNanos, lastSignal, nowNanos) {
		return false
	}
	atomic.StoreInt32(&stream.upstreamHealthLowSamples, 0)
	if !t.MarkPlaybackMigration(streamID, "backend-low-throughput") {
		return false
	}
	log.Printf("[stream-migration] sustained upstream underflow detected: streamID=%s actual=%.1fMbps required=%.1fMbps threshold=%.0f%% samples=%d",
		streamID, float64(actual)/1_000_000, float64(required)/1_000_000,
		upstreamHealthHeadroom*100, upstreamHealthLowSamples)
	return true
}

// ShouldMigratePlayback consumes transient recommendations only after the player
// confirms buffer pressure. Terminal source/provider failures are immediately
// actionable because the current request cannot recover normally.
func (t *StreamTracker) ShouldMigratePlayback(userID string, update models.PlaybackProgressUpdate) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneMigrationSignalsLocked()

	controlKey := playbackControlKey(userID, update.MediaType, update.ItemID)
	key, signal, ok := t.playbackMigrationSignalLocked(controlKey, update.SourcePath)
	if !ok || !signal.expiresAt.After(time.Now()) {
		return "", false
	}
	if update.IsPaused {
		return "", false
	}
	terminalSourceFailure := signal.reason == "backend-source-failure" || signal.reason == "backend-provider-unavailable"
	if !terminalSourceFailure && !update.IsBuffering && (update.BufferAhead == nil || *update.BufferAhead > 5) {
		return "", false
	}
	if signal.reason == "backend-low-throughput" && update.Position <= 3 {
		return "", false
	}
	delete(t.migrationSignals, key)
	return signal.reason, true
}

// ShouldPreparePlaybackMigration exposes a pending transient recommendation
// without consuming it once the current buffer runway is shrinking. When a
// native player cannot report runway, confirmed upstream health evidence still
// prepares an alternative; the actual handoff remains gated by buffering.
func (t *StreamTracker) ShouldPreparePlaybackMigration(userID string, update models.PlaybackProgressUpdate) (string, bool) {
	if update.IsPaused || update.Position <= 3 {
		return "", false
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneMigrationSignalsLocked()

	controlKey := playbackControlKey(userID, update.MediaType, update.ItemID)
	_, signal, ok := t.playbackMigrationSignalLocked(controlKey, update.SourcePath)
	if !ok || !signal.expiresAt.After(time.Now()) {
		return "", false
	}
	if !update.IsBuffering && update.BufferAhead != nil && *update.BufferAhead > migrationPreparationBufferRunway {
		return "", false
	}
	return signal.reason, true
}

// GetStream returns a copy of a tracked stream.
func (t *StreamTracker) GetStream(id string) (*TrackedStream, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	stream, ok := t.streams[id]
	if !ok {
		return nil, false
	}

	lastActivity := stream.StartTime
	if stream.activityCounter != nil {
		if nanos := atomic.LoadInt64(stream.activityCounter); nanos > 0 {
			lastActivity = time.Unix(0, nanos)
		}
	}

	return &TrackedStream{
		ID:             stream.ID,
		Path:           stream.Path,
		Filename:       stream.Filename,
		ClientIP:       stream.ClientIP,
		ClientID:       stream.ClientID,
		ProfileID:      stream.ProfileID,
		ProfileName:    stream.ProfileName,
		AccountID:      stream.AccountID,
		StartTime:      stream.StartTime,
		LastActivity:   lastActivity,
		BytesStreamed:  atomic.LoadInt64(stream.bytesCounter),
		ContentLength:  stream.ContentLength,
		RangeStart:     stream.RangeStart,
		RangeEnd:       stream.RangeEnd,
		Method:         stream.Method,
		UserAgent:      stream.UserAgent,
		ViaShareLink:   stream.ViaShareLink,
		MediaMetadata:  stream.MediaMetadata,
		SharePosition:  stream.SharePosition,
		ShareDuration:  stream.ShareDuration,
		SharePercent:   stream.SharePercent,
		ShareUpdatedAt: stream.ShareUpdatedAt,
		SharePaused:    stream.SharePaused,
		ShareBuffering: stream.ShareBuffering,
	}, true
}

// UpdateSharePlaybackProgress records a live, in-memory playback heartbeat for
// share-link streams. It intentionally does not persist to watch history.
func (t *StreamTracker) UpdateSharePlaybackProgress(profileID, profileName string, update models.PlaybackProgressUpdate) int {
	t.mu.Lock()
	defer t.mu.Unlock()

	percent := update.PercentWatched
	if percent <= 0 && update.Duration > 0 {
		percent = (update.Position / update.Duration) * 100
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	when := update.Timestamp
	if when.IsZero() {
		when = time.Now()
	}

	target := streamMediaIdentity(StreamMediaMetadata{
		MediaType:     update.MediaType,
		ItemID:        update.ItemID,
		SeasonNumber:  update.SeasonNumber,
		EpisodeNumber: update.EpisodeNumber,
		SeriesID:      update.SeriesID,
		ExternalIDs:   update.ExternalIDs,
	})
	if target.MediaType == "" || target.ID == "" {
		return 0
	}
	targetKeys := make(map[string]struct{}, len(target.CandidateKeys)+1)
	targetKeys[target.Key] = struct{}{}
	for _, key := range target.CandidateKeys {
		targetKeys[key] = struct{}{}
	}

	matches := 0
	for _, stream := range t.streams {
		if !stream.ViaShareLink {
			continue
		}
		if profileID != "" && stream.ProfileID != "" && stream.ProfileID != profileID {
			continue
		}
		if profileName != "" && stream.ProfileName != "" && !strings.EqualFold(stream.ProfileName, profileName) {
			continue
		}
		if !streamMetadataMatchesIdentity(stream.MediaMetadata, target, targetKeys) {
			continue
		}
		stream.SharePosition = update.Position
		stream.ShareDuration = update.Duration
		stream.SharePercent = percent
		stream.ShareUpdatedAt = when
		stream.SharePaused = update.IsPaused
		stream.ShareBuffering = update.IsBuffering
		matches++
	}
	return matches
}

func streamMetadataMatchesIdentity(meta StreamMediaMetadata, target mediaidentity.Identity, targetKeys map[string]struct{}) bool {
	identity := streamMediaIdentity(meta)
	if identity.MediaType != target.MediaType {
		return false
	}
	if _, ok := targetKeys[identity.Key]; ok {
		return true
	}
	for _, key := range identity.CandidateKeys {
		if _, ok := targetKeys[key]; ok {
			return true
		}
	}
	return false
}

// UpdateBytes updates the bytes streamed for a stream
func (t *StreamTracker) UpdateBytes(id string, bytes int64) {
	t.mu.RLock()
	stream, ok := t.streams[id]
	t.mu.RUnlock()

	if ok {
		atomic.StoreInt64(&stream.BytesStreamed, bytes)
		stream.LastActivity = time.Now()
	}
}

// EndStream removes a stream from tracking
func (t *StreamTracker) EndStream(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if stream, ok := t.streams[id]; ok {
		t.rememberEndedStreamLocked(stream, time.Now())
		delete(t.streams, id)
	}
}

// TerminateStream cancels the stream transport when the handler registered a cancel function.
func (t *StreamTracker) TerminateStream(id string) bool {
	t.mu.Lock()
	stream, ok := t.streams[id]
	if !ok {
		t.mu.Unlock()
		return false
	}
	cancel := stream.cancel
	if cancel == nil {
		t.mu.Unlock()
		return false
	}
	t.rememberEndedStreamLocked(stream, time.Now())
	delete(t.streams, id)
	t.mu.Unlock()

	cancel()
	return true
}

func (t *StreamTracker) rememberEndedStreamLocked(stream *TrackedStream, now time.Time) {
	if stream == nil {
		return
	}
	t.pruneRecentlyEndedLocked(now)
	if t.recentlyEnded == nil {
		t.recentlyEnded = make(map[string]recentlyEndedStream)
	}
	t.recentlyEnded[stream.ID] = recentlyEndedStream{stream: stream, endedAt: now}
}

func (t *StreamTracker) pruneRecentlyEndedLocked(now time.Time) {
	for id, ended := range t.recentlyEnded {
		if ended.stream == nil || now.Sub(ended.endedAt) > playbackNotificationTeardownGrace {
			delete(t.recentlyEnded, id)
		}
	}
}

// GetActiveStreams returns all currently active streams
func (t *StreamTracker) GetActiveStreams() []*TrackedStream {
	t.mu.RLock()
	defer t.mu.RUnlock()

	streams := make([]*TrackedStream, 0, len(t.streams))
	for _, s := range t.streams {
		// Read last activity from atomic counter
		lastActivity := s.StartTime
		if s.activityCounter != nil {
			if nanos := atomic.LoadInt64(s.activityCounter); nanos > 0 {
				lastActivity = time.Unix(0, nanos)
			}
		}
		bytesNow := atomic.LoadInt64(s.bytesCounter)
		// Create a copy with current bytes count and activity time
		streamCopy := &TrackedStream{
			ID:             s.ID,
			Path:           s.Path,
			Filename:       s.Filename,
			ClientIP:       s.ClientIP,
			ClientID:       s.ClientID,
			ProfileID:      s.ProfileID,
			ProfileName:    s.ProfileName,
			AccountID:      s.AccountID,
			StartTime:      s.StartTime,
			LastActivity:   lastActivity,
			BytesStreamed:  bytesNow,
			ContentLength:  s.ContentLength,
			RangeStart:     s.RangeStart,
			RangeEnd:       s.RangeEnd,
			Method:         s.Method,
			UserAgent:      s.UserAgent,
			ViaShareLink:   s.ViaShareLink,
			MediaMetadata:  s.MediaMetadata,
			SharePosition:  s.SharePosition,
			ShareDuration:  s.ShareDuration,
			SharePercent:   s.SharePercent,
			ShareUpdatedAt: s.ShareUpdatedAt,
			SharePaused:    s.SharePaused,
			ShareBuffering: s.ShareBuffering,
			ThroughputBps:  sampleThroughput(bytesNow, &s.lastSampleBytes, &s.lastSampleNanos, &s.throughputBps),
		}
		streams = append(streams, streamCopy)
	}
	return streams
}

// Count returns the number of active streams
func (t *StreamTracker) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.streams)
}

// CountPlaybackSlots returns the number of distinct playbacks represented by
// the active stream requests. A single player often opens multiple byte-range
// requests for one media item; those should consume one stream-limit slot.
func (t *StreamTracker) CountPlaybackSlots() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return countPlaybackSlotsLocked(t.streams, nil)
}

// CountForAccount returns the number of active streams for the given account.
func (t *StreamTracker) CountForAccount(accountID string) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return countPlaybackSlotsLocked(t.streams, func(s *TrackedStream) bool {
		return s.AccountID == accountID
	})
}

// CountForProfile returns the number of active streams for the given profile.
func (t *StreamTracker) CountForProfile(profileID string) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return countPlaybackSlotsLocked(t.streams, func(s *TrackedStream) bool {
		return s.ProfileID == profileID
	})
}

func countPlaybackSlotsLocked(streams map[string]*TrackedStream, include func(*TrackedStream) bool) int {
	seen := make(map[string]struct{})
	for _, s := range streams {
		if include != nil && !include(s) {
			continue
		}
		key := trackedStreamSlotKey(s)
		if key == "" {
			key = s.ID
		}
		seen[key] = struct{}{}
	}
	return len(seen)
}

func (t *StreamTracker) hasPlaybackSlotLocked(include func(*TrackedStream) bool, slotKey string) bool {
	if slotKey == "" {
		return false
	}
	for _, s := range t.streams {
		if include != nil && !include(s) {
			continue
		}
		if trackedStreamSlotKey(s) == slotKey {
			return true
		}
	}
	return false
}

// WouldExceedGlobalLimit reports whether starting this request would create a
// new playback slot beyond the global limit.
func (t *StreamTracker) WouldExceedGlobalLimit(r *http.Request, path string, maxStreams int) (StreamUsageSummary, bool) {
	if maxStreams <= 0 {
		return StreamUsageSummary{MaxStreams: maxStreams}, false
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	current := countPlaybackSlotsLocked(t.streams, nil)
	available := maxStreams - current
	if available < 0 {
		available = 0
	}
	usage := StreamUsageSummary{
		CurrentStreams:   current,
		MaxStreams:       maxStreams,
		AvailableStreams: available,
		AtLimit:          current >= maxStreams,
	}
	if current < maxStreams {
		return usage, false
	}
	return usage, !t.hasPlaybackSlotLocked(nil, requestStreamSlotKey(r, path))
}

// WouldExceedAccountLimit reports whether starting this request would create a
// new account playback slot beyond the account limit.
func (t *StreamTracker) WouldExceedAccountLimit(r *http.Request, path, accountID string, maxStreams int) (StreamUsageSummary, bool) {
	if maxStreams <= 0 {
		return StreamUsageSummary{MaxStreams: maxStreams}, false
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	include := func(s *TrackedStream) bool { return s.AccountID == accountID }
	current := countPlaybackSlotsLocked(t.streams, include)
	available := maxStreams - current
	if available < 0 {
		available = 0
	}
	usage := StreamUsageSummary{
		CurrentStreams:   current,
		MaxStreams:       maxStreams,
		AvailableStreams: available,
		AtLimit:          current >= maxStreams,
	}
	if current < maxStreams {
		return usage, false
	}
	return usage, !t.hasPlaybackSlotLocked(include, requestStreamSlotKey(r, path))
}

// WouldExceedProfileLimit reports whether starting this request would create a
// new profile playback slot beyond the profile limit.
func (t *StreamTracker) WouldExceedProfileLimit(r *http.Request, path, profileID string, maxStreams int) (StreamUsageSummary, bool) {
	if maxStreams <= 0 {
		return StreamUsageSummary{MaxStreams: maxStreams}, false
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	include := func(s *TrackedStream) bool { return s.ProfileID == profileID }
	current := countPlaybackSlotsLocked(t.streams, include)
	available := maxStreams - current
	if available < 0 {
		available = 0
	}
	usage := StreamUsageSummary{
		CurrentStreams:   current,
		MaxStreams:       maxStreams,
		AvailableStreams: available,
		AtLimit:          current >= maxStreams,
	}
	if current < maxStreams {
		return usage, false
	}
	return usage, !t.hasPlaybackSlotLocked(include, requestStreamSlotKey(r, path))
}

// GetAccountStreamUsage returns a usage summary for the given account.
func (t *StreamTracker) GetAccountStreamUsage(accountID string, maxStreams int) StreamUsageSummary {
	current := t.CountForAccount(accountID)
	available := 0
	atLimit := false
	if maxStreams > 0 {
		available = maxStreams - current
		if available < 0 {
			available = 0
		}
		atLimit = current >= maxStreams
	}
	return StreamUsageSummary{
		CurrentStreams:   current,
		MaxStreams:       maxStreams,
		AvailableStreams: available,
		AtLimit:          atLimit,
	}
}

// GetProfileStreamUsage returns a usage summary for the given profile.
func (t *StreamTracker) GetProfileStreamUsage(profileID string, maxStreams int) StreamUsageSummary {
	current := t.CountForProfile(profileID)
	available := 0
	atLimit := false
	if maxStreams > 0 {
		available = maxStreams - current
		if available < 0 {
			available = 0
		}
		atLimit = current >= maxStreams
	}
	return StreamUsageSummary{
		CurrentStreams:   current,
		MaxStreams:       maxStreams,
		AvailableStreams: available,
		AtLimit:          atLimit,
	}
}

func generateStreamID(counter uint64) string {
	return time.Now().Format("20060102150405") + "-" + string(rune('A'+counter%26)) + string(rune('0'+counter%10))
}

func getClientIP(r *http.Request) string {
	return requestsecurity.ClientIP(r)
}

// TrackingResponseWriter wraps http.ResponseWriter to track bytes written
type TrackingResponseWriter struct {
	http.ResponseWriter
	bytesWritten *int64
}

// Write tracks bytes written
func (w *TrackingResponseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	if n > 0 {
		atomic.AddInt64(w.bytesWritten, int64(n))
	}
	return n, err
}

// NewTrackingResponseWriter creates a new tracking response writer
func NewTrackingResponseWriter(w http.ResponseWriter, counter *int64) *TrackingResponseWriter {
	return &TrackingResponseWriter{
		ResponseWriter: w,
		bytesWritten:   counter,
	}
}
