package handlers

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

func TestRangeBlockCache_PutAndGet(t *testing.T) {
	var cache rangeBlockCache
	data := make([]byte, 256)
	for i := range data {
		data[i] = byte(i)
	}
	cache.put("/test.mp4", 100, data)

	// Hit: exact range
	got, ok := cache.get("/test.mp4", 100, 356)
	if !ok {
		t.Fatal("expected cache hit for exact range")
	}
	if !bytes.Equal(got, data) {
		t.Fatal("cached data mismatch")
	}

	// Hit: sub-range
	got, ok = cache.get("/test.mp4", 110, 120)
	if !ok {
		t.Fatal("expected cache hit for sub-range")
	}
	if !bytes.Equal(got, data[10:20]) {
		t.Fatalf("sub-range data mismatch: got %v, want %v", got, data[10:20])
	}

	// Miss: different path
	_, ok = cache.get("/other.mp4", 100, 356)
	if ok {
		t.Fatal("expected cache miss for different path")
	}

	// Miss: range extends beyond cached block
	_, ok = cache.get("/test.mp4", 100, 400)
	if ok {
		t.Fatal("expected cache miss for range beyond cached block")
	}

	// Miss: range starts before cached block
	_, ok = cache.get("/test.mp4", 50, 200)
	if ok {
		t.Fatal("expected cache miss for range before cached block")
	}
}

func TestRangeBlockCache_Expiry(t *testing.T) {
	var cache rangeBlockCache
	data := []byte("testdata")
	cache.put("/test.mp4", 0, data)

	// Should hit before expiry
	_, ok := cache.get("/test.mp4", 0, 8)
	if !ok {
		t.Fatal("expected cache hit before expiry")
	}

	// Manually expire the block
	cache.mu.Lock()
	cache.blocks["/test.mp4"][0].expiry = time.Now().Add(-1 * time.Second)
	cache.mu.Unlock()

	_, ok = cache.get("/test.mp4", 0, 8)
	if ok {
		t.Fatal("expected cache miss after expiry")
	}
}

func TestRangeBlockCache_MaxBlocks(t *testing.T) {
	var cache rangeBlockCache
	data := []byte("block")

	// Fill to max capacity
	for i := 0; i < rangeCacheMaxBlocks+4; i++ {
		cache.put("/test.mp4", int64(i*1000), data)
	}

	cache.mu.Lock()
	count := len(cache.blocks["/test.mp4"])
	cache.mu.Unlock()

	if count > rangeCacheMaxBlocks {
		t.Fatalf("expected at most %d blocks, got %d", rangeCacheMaxBlocks, count)
	}
}

func TestRangeBlockCache_DataIsolation(t *testing.T) {
	var cache rangeBlockCache
	data := []byte("original")
	cache.put("/test.mp4", 0, data)

	// Mutate original data — cache should not be affected
	data[0] = 'X'

	got, ok := cache.get("/test.mp4", 0, 8)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got[0] != 'o' {
		t.Fatal("cache data was mutated by external change")
	}
}

func TestRangeBlockCache_Coalescing(t *testing.T) {
	var cache rangeBlockCache

	// First caller claims the fetch
	isOwner, ch := cache.tryStartFetch("/test.mp4", 0)
	if !isOwner {
		t.Fatal("first caller should be owner")
	}

	// Second caller should not be owner, gets the wait channel
	isOwner2, ch2 := cache.tryStartFetch("/test.mp4", 0)
	if isOwner2 {
		t.Fatal("second caller should not be owner")
	}

	// Both channels should be the same
	if ch != ch2 {
		t.Fatal("waiters should get the same channel")
	}

	// Different fetch window should get ownership
	isOwner3, _ := cache.tryStartFetch("/test.mp4", int64(rangeCacheMinFetchSize))
	if !isOwner3 {
		t.Fatal("different fetch window should get ownership")
	}
	cache.finishFetch("/test.mp4", int64(rangeCacheMinFetchSize))

	// Finish the original fetch — waiters should be unblocked
	done := make(chan struct{})
	go func() {
		<-ch2
		close(done)
	}()

	cache.finishFetch("/test.mp4", 0)

	select {
	case <-done:
		// Success
	case <-time.After(time.Second):
		t.Fatal("waiter was not unblocked after finishFetch")
	}

	// After finish, a new fetch for the same window should get ownership
	isOwner4, _ := cache.tryStartFetch("/test.mp4", 0)
	if !isOwner4 {
		t.Fatal("should get ownership after previous fetch finished")
	}
	cache.finishFetch("/test.mp4", 0)
}

func TestRangeBlockCache_ConcurrentCoalescing(t *testing.T) {
	var cache rangeBlockCache
	data := make([]byte, rangeCacheMinFetchSize)
	for i := range data {
		data[i] = byte(i % 256)
	}

	const numWaiters = 10
	var wg sync.WaitGroup
	hits := make([]bool, numWaiters)

	// First goroutine claims ownership and does the fetch
	isOwner, _ := cache.tryStartFetch("/test.mp4", 0)
	if !isOwner {
		t.Fatal("first caller should be owner")
	}

	// Spawn waiters that will block until fetch completes
	for i := 0; i < numWaiters; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, ch := cache.tryStartFetch("/test.mp4", 0)
			<-ch
			// After unblock, try to read from cache
			_, ok := cache.get("/test.mp4", 0, 100)
			hits[idx] = ok
		}(i)
	}

	// Simulate fetch completing
	time.Sleep(10 * time.Millisecond) // let waiters register
	cache.put("/test.mp4", 0, data)
	cache.finishFetch("/test.mp4", 0)

	wg.Wait()

	for i, hit := range hits {
		if !hit {
			t.Fatalf("waiter %d did not get cache hit after coalesced fetch", i)
		}
	}
}

func TestRangeBlockCache_FetchWindowAlignment(t *testing.T) {
	// Verify that fetch windows align to rangeCacheMinFetchSize boundaries
	// This is tested indirectly — the handler aligns fetchStart, but we can
	// verify the math here
	tests := []struct {
		rangeStart    int64
		expectedStart int64
	}{
		{0, 0},
		{100, 0},
		{rangeCacheMinFetchSize - 1, 0},
		{rangeCacheMinFetchSize, rangeCacheMinFetchSize},
		{rangeCacheMinFetchSize + 500, rangeCacheMinFetchSize},
		{3 * rangeCacheMinFetchSize, 3 * rangeCacheMinFetchSize},
		{3*rangeCacheMinFetchSize + 1000, 3 * rangeCacheMinFetchSize},
	}

	for _, tt := range tests {
		aligned := (tt.rangeStart / rangeCacheMinFetchSize) * rangeCacheMinFetchSize
		if aligned != tt.expectedStart {
			t.Errorf("rangeStart=%d: aligned=%d, want %d", tt.rangeStart, aligned, tt.expectedStart)
		}
	}
}

func TestRangeCacheMinFetchSize(t *testing.T) {
	// Verify the constant is 2MB as intended for 4K DV content
	if rangeCacheMinFetchSize != 2*1024*1024 {
		t.Fatalf("rangeCacheMinFetchSize = %d, want %d (2MB)", rangeCacheMinFetchSize, 2*1024*1024)
	}
}
