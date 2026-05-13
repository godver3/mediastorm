package handlers

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"novastream/models"
)

// StreamTracker tracks active video streams for monitoring
type StreamTracker struct {
	streams       map[string]*TrackedStream
	stopPlaybacks map[string]time.Time
	mu            sync.RWMutex
	counter       uint64
}

// TrackedStream represents an active direct video stream
type TrackedStream struct {
	ID              string
	Path            string
	Filename        string
	ClientIP        string
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
	MediaMetadata   StreamMediaMetadata
	cancel          context.CancelFunc
	bytesCounter    *int64
	activityCounter *int64 // unix nanos of last byte transfer, updated atomically
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
	streams:       make(map[string]*TrackedStream),
	stopPlaybacks: make(map[string]time.Time),
}

const playbackStopSignalDuration = 2 * time.Minute

// GetStreamTracker returns the global stream tracker
func GetStreamTracker() *StreamTracker {
	return globalStreamTracker
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
		MediaMetadata:   parseStreamMediaMetadata(r),
		bytesCounter:    bytesCounter,
		activityCounter: activityCounter,
	}

	t.streams[id] = stream
	return id, bytesCounter, activityCounter
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

func playbackControlKey(userID, mediaType, itemID string) string {
	return strings.ToLower(strings.TrimSpace(userID)) + "|" +
		strings.ToLower(strings.TrimSpace(mediaType)) + "|" +
		strings.ToLower(strings.TrimSpace(itemID))
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
		ID:            stream.ID,
		Path:          stream.Path,
		Filename:      stream.Filename,
		ClientIP:      stream.ClientIP,
		ProfileID:     stream.ProfileID,
		ProfileName:   stream.ProfileName,
		AccountID:     stream.AccountID,
		StartTime:     stream.StartTime,
		LastActivity:  lastActivity,
		BytesStreamed: atomic.LoadInt64(stream.bytesCounter),
		ContentLength: stream.ContentLength,
		RangeStart:    stream.RangeStart,
		RangeEnd:      stream.RangeEnd,
		Method:        stream.Method,
		UserAgent:     stream.UserAgent,
		MediaMetadata: stream.MediaMetadata,
	}, true
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

	if _, ok := t.streams[id]; ok {
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
	delete(t.streams, id)
	t.mu.Unlock()

	cancel()
	return true
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
		// Create a copy with current bytes count and activity time
		streamCopy := &TrackedStream{
			ID:            s.ID,
			Path:          s.Path,
			Filename:      s.Filename,
			ClientIP:      s.ClientIP,
			ProfileID:     s.ProfileID,
			ProfileName:   s.ProfileName,
			AccountID:     s.AccountID,
			StartTime:     s.StartTime,
			LastActivity:  lastActivity,
			BytesStreamed: atomic.LoadInt64(s.bytesCounter),
			ContentLength: s.ContentLength,
			RangeStart:    s.RangeStart,
			RangeEnd:      s.RangeEnd,
			Method:        s.Method,
			UserAgent:     s.UserAgent,
			MediaMetadata: s.MediaMetadata,
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

// CountForAccount returns the number of active streams for the given account.
func (t *StreamTracker) CountForAccount(accountID string) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	count := 0
	for _, s := range t.streams {
		if s.AccountID == accountID {
			count++
		}
	}
	return count
}

// CountForProfile returns the number of active streams for the given profile.
func (t *StreamTracker) CountForProfile(profileID string) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	count := 0
	for _, s := range t.streams {
		if s.ProfileID == profileID {
			count++
		}
	}
	return count
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
	// Check X-Forwarded-For header first (for proxied requests)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first IP in the chain
		if idx := len(xff); idx > 0 {
			for i, c := range xff {
				if c == ',' {
					return xff[:i]
				}
			}
			return xff
		}
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// Fall back to RemoteAddr
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
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
