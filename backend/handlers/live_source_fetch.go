package handlers

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
)

// liveSourceFetchConcurrency caps how many providers one channel request contacts at once.
const liveSourceFetchConcurrency = 4

// liveSourceFetch is one source's channels for a channel-list request.
type liveSourceFetch struct {
	skipped      bool
	channels     []LiveChannel
	err          error
	errorMessage string
	// stalkerTotal is the category-paged Stalker total, or -1.
	stalkerTotal      int
	stalkerCategories []string
}

// fetchLiveSourcesConcurrently runs fetch for every source not skipped, up to
// liveSourceFetchConcurrency at a time, and returns the results in source order.
func fetchLiveSourcesConcurrently(
	ctx context.Context,
	sources []resolvedM3USource,
	skip func(resolvedM3USource) bool,
	fetch func(context.Context, resolvedM3USource) liveSourceFetch,
) []liveSourceFetch {
	results := make([]liveSourceFetch, len(sources))
	slots := make(chan struct{}, liveSourceFetchConcurrency)
	var wg sync.WaitGroup
	for index, source := range sources {
		if skip != nil && skip(source) {
			results[index] = liveSourceFetch{skipped: true, stalkerTotal: -1}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				results[index] = liveSourceFetch{err: ctx.Err(), errorMessage: ctx.Err().Error(), stalkerTotal: -1}
				return
			}
			defer func() { <-slots }()
			// These goroutines are outside net/http's per-request recover, so a
			// panic in one provider's fetch would otherwise kill the process.
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[live] source fetch panicked: %v\n%s", r, debug.Stack())
					err := fmt.Errorf("source fetch failed: %v", r)
					results[index] = liveSourceFetch{err: err, errorMessage: err.Error(), stalkerTotal: -1}
				}
			}()
			results[index] = fetch(ctx, source)
		}()
	}
	wg.Wait()
	return results
}
