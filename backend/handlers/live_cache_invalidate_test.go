package handlers

import (
	"net/http"
	"testing"
	"time"
)

func TestInvalidateCatalogCaches(t *testing.T) {
	future := time.Now().Add(time.Hour)
	retryAfter := time.Now().Add(time.Minute)
	h := &LiveHandler{
		xtreamCache: map[string]xtreamChannelsCacheEntry{
			"x": {channels: []LiveChannel{{ID: "a"}}, expiresAt: future, retryAfter: retryAfter},
		},
		xtreamInFlight: map[string]*xtreamChannelsFetch{},
		stremioCache: map[string]stremioChannelsCacheEntry{
			"s": {channels: []LiveChannel{{ID: "b"}}, fetched: time.Now()},
		},
	}
	h.discoveryClient(nil, "", time.Minute)
	h.discovery.entries["d"] = &discoveryEntry{code: http.StatusOK, header: http.Header{"Etag": {"v1"}}, expires: future}

	session := &stalkerPortalSession{
		channels:      []LiveChannel{{ID: "c"}},
		cacheExpiry:   future,
		categoryPages: map[string]stalkerCachedChannelPage{"p": {expiresAt: future}},
	}
	stalkerSessionStore.Lock()
	stalkerSessionStore.sessions["test-invalidate"] = session
	stalkerSessionStore.Unlock()
	t.Cleanup(func() {
		stalkerSessionStore.Lock()
		delete(stalkerSessionStore.sessions, "test-invalidate")
		stalkerSessionStore.Unlock()
	})

	if n := h.invalidateCatalogCaches(); n != 4 {
		t.Fatalf("invalidated = %d, want 4", n)
	}

	now := time.Now()
	x := h.xtreamCache["x"]
	if now.Before(x.expiresAt) || x.expiresAt.IsZero() {
		t.Fatalf("xtream entry expiresAt = %v, want expired but non-zero (stale fallback)", x.expiresAt)
	}
	if len(x.channels) != 1 || !x.retryAfter.Equal(retryAfter) {
		t.Fatalf("xtream stale channels or failure cooldown not preserved: %+v", x)
	}
	if len(h.stremioCache) != 0 {
		t.Fatalf("stremio cache not cleared: %d entries", len(h.stremioCache))
	}
	if now.Before(session.cacheExpiry) || len(session.categoryPages) != 0 || len(session.channels) != 1 {
		t.Fatalf("stalker session not invalidated correctly: expiry=%v pages=%d channels=%d", session.cacheExpiry, len(session.categoryPages), len(session.channels))
	}
	d := h.discovery.entries["d"]
	if now.Before(d.expires) || d.header.Get("Etag") != "v1" {
		t.Fatalf("discovery entry should be expired with validators kept: expires=%v etag=%q", d.expires, d.header.Get("Etag"))
	}
}
