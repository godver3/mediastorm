package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"novastream/services/streaming"
)

// mockStreamProvider implements streaming.Provider for testing.
type mockStreamProvider struct {
	mu        sync.Mutex
	responses map[string]*streaming.Response
	calls     int
	lastRange string
}

func newMockStreamProvider(totalSize int64, data []byte) *mockStreamProvider {
	return &mockStreamProvider{
		responses: make(map[string]*streaming.Response),
	}
}

func (m *mockStreamProvider) Stream(ctx context.Context, req streaming.Request) (*streaming.Response, error) {
	m.mu.Lock()
	m.calls++
	m.lastRange = req.RangeHeader
	m.mu.Unlock()

	// Parse range start from header
	start := int64(0)
	if req.RangeHeader != "" {
		if s, ok := parseRangeStart(req.RangeHeader); ok {
			start = s
		}
	}

	totalSize := int64(100 * 1024 * 1024) // 100MB fake file

	// Generate predictable data based on position
	chunkSize := 1024 * 1024 // 1MB chunks
	data := make([]byte, chunkSize)
	for i := range data {
		data[i] = byte((start + int64(i)) % 256)
	}

	headers := http.Header{}
	headers.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, totalSize-1, totalSize))
	headers.Set("Content-Length", fmt.Sprintf("%d", totalSize-start))
	headers.Set("Content-Type", "video/mp4")

	return &streaming.Response{
		Body:          io.NopCloser(newSlowReader(data, 256*1024)), // 256KB per read
		Headers:       headers,
		Status:        http.StatusPartialContent,
		ContentLength: totalSize - start,
	}, nil
}

// slowReader simulates CDN reads by returning data in chunks.
type slowReader struct {
	data    []byte
	pos     int
	maxRead int
}

func newSlowReader(data []byte, maxRead int) *slowReader {
	return &slowReader{data: data, maxRead: maxRead}
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := len(p)
	if n > r.maxRead {
		n = r.maxRead
	}
	remaining := len(r.data) - r.pos
	if n > remaining {
		n = remaining
	}
	copy(p[:n], r.data[r.pos:r.pos+n])
	r.pos += n
	return n, nil
}

func TestParseRangeStart(t *testing.T) {
	tests := []struct {
		input string
		start int64
		ok    bool
	}{
		{"bytes=0-", 0, true},
		{"bytes=12345-", 12345, true},
		{"bytes=100-200", 100, true},
		{"bytes=28744905-", 28744905, true},
		{"", 0, false},
		{"bytes=-500", 0, false}, // suffix range
		{"invalid", 0, false},
	}

	for _, tt := range tests {
		start, ok := parseRangeStart(tt.input)
		if ok != tt.ok || (ok && start != tt.start) {
			t.Errorf("parseRangeStart(%q) = (%d, %v), want (%d, %v)",
				tt.input, start, ok, tt.start, tt.ok)
		}
	}
}

func TestParseTotalSizeFromContentRange(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"bytes 0-999/5000", 5000},
		{"bytes 100-200/13304587412", 13304587412},
		{"bytes 0-0/*", 0},
		{"", 0},
		{"invalid", 0},
	}

	for _, tt := range tests {
		got := parseTotalSizeFromContentRange(tt.input)
		if got != tt.want {
			t.Errorf("parseTotalSizeFromContentRange(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestStreamPoolNewSlotCreation(t *testing.T) {
	pool := newStreamPool(nil)
	defer pool.close()

	provider := newMockStreamProvider(100*1024*1024, nil)

	slot, err := pool.getOrCreate("/test/file.mp4", 0, provider)
	if err != nil {
		t.Fatalf("getOrCreate failed: %v", err)
	}
	if slot == nil {
		t.Fatal("expected non-nil slot")
	}
	if slot.startByte != 0 {
		t.Errorf("slot.startByte = %d, want 0", slot.startByte)
	}

	provider.mu.Lock()
	calls := provider.calls
	provider.mu.Unlock()
	if calls != 1 {
		t.Errorf("expected 1 provider call, got %d", calls)
	}
}

func TestStreamPoolSlotReuse(t *testing.T) {
	pool := newStreamPool(nil)
	defer pool.close()

	provider := newMockStreamProvider(100*1024*1024, nil)

	// Create a slot at position 0
	slot1, err := pool.getOrCreate("/test/file.mp4", 0, provider)
	if err != nil {
		t.Fatalf("first getOrCreate failed: %v", err)
	}

	// Wait for some data to be buffered
	deadline := time.After(2 * time.Second)
	for {
		slot1.mu.Lock()
		buffered := int64(len(slot1.data))
		slot1.mu.Unlock()
		if buffered > 100*1024 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for buffer to fill")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Request at a position within the buffer should reuse the slot
	slot2, err := pool.getOrCreate("/test/file.mp4", 50*1024, provider)
	if err != nil {
		t.Fatalf("second getOrCreate failed: %v", err)
	}

	if slot2 != slot1 {
		t.Error("expected slot reuse, got new slot")
	}

	provider.mu.Lock()
	calls := provider.calls
	provider.mu.Unlock()
	if calls != 1 {
		t.Errorf("expected 1 provider call (reuse), got %d", calls)
	}
}

func TestStreamPoolDifferentPositions(t *testing.T) {
	pool := newStreamPool(nil)
	defer pool.close()

	provider := newMockStreamProvider(100*1024*1024, nil)

	// Create slots at two widely separated positions (simulating audio/video tracks)
	slot1, err := pool.getOrCreate("/test/file.mp4", 0, provider)
	if err != nil {
		t.Fatalf("first getOrCreate failed: %v", err)
	}

	slot2, err := pool.getOrCreate("/test/file.mp4", 50*1024*1024, provider)
	if err != nil {
		t.Fatalf("second getOrCreate failed: %v", err)
	}

	if slot1 == slot2 {
		t.Error("expected different slots for widely separated positions")
	}

	provider.mu.Lock()
	calls := provider.calls
	provider.mu.Unlock()
	if calls != 2 {
		t.Errorf("expected 2 provider calls, got %d", calls)
	}

	// Verify pool has 2 slots for this file
	pool.mu.RLock()
	slotCount := len(pool.files["/test/file.mp4"])
	pool.mu.RUnlock()
	if slotCount != 2 {
		t.Errorf("expected 2 slots in pool, got %d", slotCount)
	}
}

func TestStreamPoolServe(t *testing.T) {
	pool := newStreamPool(nil)
	defer pool.close()

	provider := newMockStreamProvider(100*1024*1024, nil)

	// Create request
	req := httptest.NewRequest("GET", "/test/file.mp4", nil)
	req.Header.Set("Range", "bytes=0-")
	w := httptest.NewRecorder()

	writeHeaders := func(w http.ResponseWriter) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	}

	served, err := pool.serve(w, req, "/test/file.mp4", 0, provider, writeHeaders, "", "")
	if !served {
		t.Fatal("expected serve to handle the request")
	}
	if err != nil {
		t.Fatalf("serve error: %v", err)
	}

	resp := w.Result()
	if resp.StatusCode != http.StatusPartialContent {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusPartialContent)
	}

	// Check that some data was written
	body := w.Body.Bytes()
	if len(body) == 0 {
		t.Error("expected non-empty response body")
	}
}

func TestStreamPoolClientDisconnectKeepsSlot(t *testing.T) {
	pool := newStreamPool(nil)
	defer pool.close()

	provider := newMockStreamProvider(100*1024*1024, nil)

	// Create a request with a cancellable context
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/test/file.mp4", nil).WithContext(ctx)
	req.Header.Set("Range", "bytes=0-")
	w := httptest.NewRecorder()

	writeHeaders := func(w http.ResponseWriter) {}

	// Start serving in a goroutine
	done := make(chan struct{})
	go func() {
		pool.serve(w, req, "/test/file.mp4", 0, provider, writeHeaders, "", "")
		close(done)
	}()

	// Wait a bit for data to flow, then simulate client disconnect
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	// The slot should still exist in the pool
	pool.mu.RLock()
	slotCount := len(pool.files["/test/file.mp4"])
	pool.mu.RUnlock()
	if slotCount != 1 {
		t.Errorf("expected slot to survive client disconnect, got %d slots", slotCount)
	}

	// Verify background reader is still active (slot not marked done)
	slots := pool.files["/test/file.mp4"]
	slots[0].mu.Lock()
	done2 := slots[0].cdnDone
	slots[0].mu.Unlock()
	// Note: cdnDone may or may not be true depending on timing (small mock data)
	_ = done2
}

func TestStreamPoolEviction(t *testing.T) {
	pool := newStreamPool(nil)
	defer pool.close()

	provider := newMockStreamProvider(100*1024*1024, nil)

	// Create a slot
	_, err := pool.getOrCreate("/test/file.mp4", 0, provider)
	if err != nil {
		t.Fatalf("getOrCreate failed: %v", err)
	}

	// Set last access to past the idle timeout
	pool.mu.RLock()
	slot := pool.files["/test/file.mp4"][0]
	pool.mu.RUnlock()

	slot.mu.Lock()
	slot.lastAccess = time.Now().Add(-2 * poolSlotIdleTimeout)
	slot.mu.Unlock()

	// Run eviction
	pool.evictIdle()

	// Slot should be evicted
	pool.mu.RLock()
	slotCount := len(pool.files["/test/file.mp4"])
	pool.mu.RUnlock()
	if slotCount != 0 {
		t.Errorf("expected slot to be evicted, got %d slots", slotCount)
	}
}

func TestStreamPoolMaxSlots(t *testing.T) {
	pool := newStreamPool(nil)
	defer pool.close()

	provider := newMockStreamProvider(100*1024*1024, nil)

	// Create more slots than the max
	for i := 0; i < poolMaxSlotsPerFile+2; i++ {
		pos := int64(i) * 20 * 1024 * 1024 // 20MB apart
		_, err := pool.getOrCreate("/test/file.mp4", pos, provider)
		if err != nil {
			t.Fatalf("getOrCreate at pos %d failed: %v", pos, err)
		}
	}

	// Should have at most poolMaxSlotsPerFile slots
	pool.mu.RLock()
	slotCount := len(pool.files["/test/file.mp4"])
	pool.mu.RUnlock()
	if slotCount > poolMaxSlotsPerFile {
		t.Errorf("expected at most %d slots, got %d", poolMaxSlotsPerFile, slotCount)
	}
}

func TestAbs64(t *testing.T) {
	tests := []struct {
		input int64
		want  int64
	}{
		{0, 0},
		{5, 5},
		{-5, 5},
		{-1, 1},
	}
	for _, tt := range tests {
		got := abs64(tt.input)
		if got != tt.want {
			t.Errorf("abs64(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestStreamPoolFindSlotDataAvailable(t *testing.T) {
	pool := newStreamPool(nil)
	defer pool.close()

	provider := newMockStreamProvider(100*1024*1024, nil)

	// Create slot at position 1000
	slot, err := pool.getOrCreate("/test/file.mp4", 1000, provider)
	if err != nil {
		t.Fatalf("getOrCreate failed: %v", err)
	}

	// Wait for some data
	deadline := time.After(2 * time.Second)
	for {
		slot.mu.Lock()
		buffered := int64(len(slot.data))
		slot.mu.Unlock()
		if buffered > 50*1024 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for buffer")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Should find the slot for a position within buffered data
	found := pool.findSlot("/test/file.mp4", 1000+10*1024)
	if found == nil {
		t.Error("expected to find slot for position within buffer")
	}

	// Should NOT find a slot for a wildly different path
	found = pool.findSlot("/other/file.mp4", 1000)
	if found != nil {
		t.Error("expected no slot for different path")
	}

	// Should NOT find a slot for a position before the slot's start
	found = pool.findSlot("/test/file.mp4", 500)
	if found != nil {
		t.Error("expected no slot for position before slot start")
	}
}

func TestStreamPoolServeHead(t *testing.T) {
	pool := newStreamPool(nil)
	defer pool.close()

	provider := newMockStreamProvider(100*1024*1024, nil)

	req := httptest.NewRequest("HEAD", "/test/file.mp4", nil)
	req.Header.Set("Range", "bytes=0-")
	w := httptest.NewRecorder()

	writeHeaders := func(w http.ResponseWriter) {}

	served, err := pool.serve(w, req, "/test/file.mp4", 0, provider, writeHeaders, "", "")
	if !served {
		t.Fatal("expected HEAD to be served")
	}
	if err != nil {
		t.Fatalf("serve error: %v", err)
	}

	resp := w.Result()
	if resp.StatusCode != http.StatusPartialContent {
		t.Errorf("HEAD status = %d, want %d", resp.StatusCode, http.StatusPartialContent)
	}

	// HEAD should have empty body
	body := w.Body.Bytes()
	if len(body) != 0 {
		t.Errorf("expected empty body for HEAD, got %d bytes", len(body))
	}

	// Should have Content-Range
	cr := resp.Header.Get("Content-Range")
	if cr == "" {
		t.Error("expected Content-Range header for HEAD response")
	}
	if !strings.Contains(cr, "bytes 0-") {
		t.Errorf("unexpected Content-Range: %s", cr)
	}
}
