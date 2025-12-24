package handlers

import (
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// StreamTracker tracks active video streams for monitoring
type StreamTracker struct {
	streams map[string]*TrackedStream
	mu      sync.RWMutex
	counter uint64
}

// TrackedStream represents an active direct video stream
type TrackedStream struct {
	ID            string
	Path          string
	Filename      string
	ClientIP      string
	ProfileID     string
	ProfileName   string
	StartTime     time.Time
	LastActivity  time.Time
	BytesStreamed int64
	ContentLength int64
	RangeStart    int64
	RangeEnd      int64
	Method        string
	UserAgent     string
	done          chan struct{}
	bytesCounter  *int64
}

// Global stream tracker instance
var globalStreamTracker = &StreamTracker{
	streams: make(map[string]*TrackedStream),
}

// GetStreamTracker returns the global stream tracker
func GetStreamTracker() *StreamTracker {
	return globalStreamTracker
}

// StartStream registers a new stream and returns its ID
func (t *StreamTracker) StartStream(r *http.Request, path string, contentLength int64, rangeStart, rangeEnd int64) (string, *int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

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

	bytesCounter := new(int64)

	stream := &TrackedStream{
		ID:            id,
		Path:          path,
		Filename:      filename,
		ClientIP:      clientIP,
		ProfileID:     profileID,
		ProfileName:   profileName,
		StartTime:     time.Now(),
		LastActivity:  time.Now(),
		ContentLength: contentLength,
		RangeStart:    rangeStart,
		RangeEnd:      rangeEnd,
		Method:        r.Method,
		UserAgent:     r.UserAgent(),
		done:          make(chan struct{}),
		bytesCounter:  bytesCounter,
	}

	t.streams[id] = stream
	return id, bytesCounter
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
		close(stream.done)
		delete(t.streams, id)
	}
}

// GetActiveStreams returns all currently active streams
func (t *StreamTracker) GetActiveStreams() []*TrackedStream {
	t.mu.RLock()
	defer t.mu.RUnlock()

	streams := make([]*TrackedStream, 0, len(t.streams))
	for _, s := range t.streams {
		// Create a copy with current bytes count
		streamCopy := &TrackedStream{
			ID:            s.ID,
			Path:          s.Path,
			Filename:      s.Filename,
			ClientIP:      s.ClientIP,
			ProfileID:     s.ProfileID,
			ProfileName:   s.ProfileName,
			StartTime:     s.StartTime,
			LastActivity:  s.LastActivity,
			BytesStreamed: atomic.LoadInt64(s.bytesCounter),
			ContentLength: s.ContentLength,
			RangeStart:    s.RangeStart,
			RangeEnd:      s.RangeEnd,
			Method:        s.Method,
			UserAgent:     s.UserAgent,
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
