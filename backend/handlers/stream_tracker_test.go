package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestTracker() *StreamTracker {
	return &StreamTracker{streams: make(map[string]*TrackedStream)}
}

func startTestStream(t *testing.T, tracker *StreamTracker, profileID, accountID string) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/video/stream?profileId="+profileID, nil)
	id, _, _ := tracker.StartStreamWithAccount(r, "/test/file.mkv", 1000, 0, 0, accountID)
	return id
}

func TestGetAccountStreamUsage(t *testing.T) {
	tracker := newTestTracker()

	// No streams — usage should be 0/3
	usage := tracker.GetAccountStreamUsage("acct1", 3)
	if usage.CurrentStreams != 0 || usage.MaxStreams != 3 || usage.AvailableStreams != 3 || usage.AtLimit {
		t.Fatalf("expected 0/3 not at limit, got %+v", usage)
	}

	// Start 2 streams for acct1
	id1 := startTestStream(t, tracker, "p1", "acct1")
	_ = startTestStream(t, tracker, "p2", "acct1")

	usage = tracker.GetAccountStreamUsage("acct1", 3)
	if usage.CurrentStreams != 2 || usage.AvailableStreams != 1 || usage.AtLimit {
		t.Fatalf("expected 2/3 not at limit, got %+v", usage)
	}

	// Start a 3rd — should be at limit
	_ = startTestStream(t, tracker, "p1", "acct1")
	usage = tracker.GetAccountStreamUsage("acct1", 3)
	if usage.CurrentStreams != 3 || usage.AvailableStreams != 0 || !usage.AtLimit {
		t.Fatalf("expected 3/3 at limit, got %+v", usage)
	}

	// End one stream — should no longer be at limit
	tracker.EndStream(id1)
	usage = tracker.GetAccountStreamUsage("acct1", 3)
	if usage.CurrentStreams != 2 || usage.AvailableStreams != 1 || usage.AtLimit {
		t.Fatalf("after end: expected 2/3, got %+v", usage)
	}
}

func TestGetProfileStreamUsage(t *testing.T) {
	tracker := newTestTracker()

	// Start 2 streams for profile "p1"
	_ = startTestStream(t, tracker, "p1", "acct1")
	_ = startTestStream(t, tracker, "p1", "acct1")
	// 1 stream for profile "p2" (same account)
	_ = startTestStream(t, tracker, "p2", "acct1")

	usage := tracker.GetProfileStreamUsage("p1", 2)
	if usage.CurrentStreams != 2 || !usage.AtLimit {
		t.Fatalf("expected 2/2 at limit for p1, got %+v", usage)
	}

	usage = tracker.GetProfileStreamUsage("p2", 2)
	if usage.CurrentStreams != 1 || usage.AtLimit {
		t.Fatalf("expected 1/2 not at limit for p2, got %+v", usage)
	}
}

func TestStreamUsageUnlimited(t *testing.T) {
	tracker := newTestTracker()

	_ = startTestStream(t, tracker, "p1", "acct1")
	_ = startTestStream(t, tracker, "p1", "acct1")

	// maxStreams=0 means unlimited
	usage := tracker.GetAccountStreamUsage("acct1", 0)
	if usage.CurrentStreams != 2 || usage.AtLimit || usage.AvailableStreams != 0 {
		t.Fatalf("unlimited should never be at limit, got %+v", usage)
	}
}

func TestStreamUsageCrossAccount(t *testing.T) {
	tracker := newTestTracker()

	// Streams from different accounts shouldn't interfere
	_ = startTestStream(t, tracker, "p1", "acct1")
	_ = startTestStream(t, tracker, "p2", "acct1")
	_ = startTestStream(t, tracker, "p3", "acct2")

	usage1 := tracker.GetAccountStreamUsage("acct1", 2)
	if usage1.CurrentStreams != 2 || !usage1.AtLimit {
		t.Fatalf("acct1 expected 2/2 at limit, got %+v", usage1)
	}

	usage2 := tracker.GetAccountStreamUsage("acct2", 2)
	if usage2.CurrentStreams != 1 || usage2.AtLimit {
		t.Fatalf("acct2 expected 1/2 not at limit, got %+v", usage2)
	}
}
