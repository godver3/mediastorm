package handlers

import (
	"context"
	"testing"
)

func TestFetchLiveSourcesConcurrentlyRecoversPanics(t *testing.T) {
	sources := []resolvedM3USource{{}, {}}
	calls := 0
	results := fetchLiveSourcesConcurrently(context.Background(), sources[:1], nil, func(context.Context, resolvedM3USource) liveSourceFetch {
		calls++
		panic("bad provider response")
	})
	if calls != 1 || results[0].err == nil {
		t.Fatalf("results = %+v, want an error result instead of a crash", results)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results = fetchLiveSourcesConcurrently(ctx, sources, nil, func(context.Context, resolvedM3USource) liveSourceFetch {
		return liveSourceFetch{}
	})
	_ = results // a cancelled request may run or skip fetches; it must not hang
}
