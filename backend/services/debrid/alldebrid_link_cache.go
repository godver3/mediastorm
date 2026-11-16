package debrid

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// AllDebrid counts every /link/unlock call as a generated link and suspends
// accounts that generate many links without downloading them ("Your account
// generates thousand of links without any download"). Playback resolution,
// the streaming proxy, health-check track probes and the prewarm URL refresher
// each used to unlock the same file independently, so one play could create
// several links and every warm prewarm entry created a new one on a timer.
//
// The cache below is shared by every AllDebridClient instance (clients are
// constructed per request), keyed by API key and restricted link, so all of
// those callers reuse one unlocked URL. Concurrent misses for the same link
// share a single /link/unlock call.
const (
	allDebridUnlockCacheTTL        = 6 * time.Hour
	allDebridUnlockCacheMaxEntries = 4096
)

type allDebridUnlockCacheEntry struct {
	result    UnrestrictResult
	expiresAt time.Time
}

type allDebridUnlockCacheStore struct {
	mu      sync.Mutex
	entries map[string]allDebridUnlockCacheEntry
	group   singleflight.Group
	now     func() time.Time
}

var allDebridUnlocks = newAllDebridUnlockCacheStore()

func newAllDebridUnlockCacheStore() *allDebridUnlockCacheStore {
	return &allDebridUnlockCacheStore{
		entries: make(map[string]allDebridUnlockCacheEntry),
		now:     time.Now,
	}
}

func allDebridUnlockCacheKey(apiKey, link string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(apiKey)))
	return hex.EncodeToString(sum[:8]) + "|" + strings.TrimSpace(link)
}

func (s *allDebridUnlockCacheStore) get(key string) (*UnrestrictResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok {
		return nil, false
	}
	if !s.now().Before(entry.expiresAt) {
		delete(s.entries, key)
		return nil, false
	}
	result := entry.result
	return &result, true
}

func (s *allDebridUnlockCacheStore) put(key string, result *UnrestrictResult) {
	if result == nil || strings.TrimSpace(result.DownloadURL) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if len(s.entries) >= allDebridUnlockCacheMaxEntries {
		for k, entry := range s.entries {
			if !now.Before(entry.expiresAt) {
				delete(s.entries, k)
			}
		}
		// Still full of live entries: drop the one closest to expiry.
		if len(s.entries) >= allDebridUnlockCacheMaxEntries {
			oldestKey := ""
			var oldest time.Time
			for k, entry := range s.entries {
				if oldestKey == "" || entry.expiresAt.Before(oldest) {
					oldestKey, oldest = k, entry.expiresAt
				}
			}
			delete(s.entries, oldestKey)
		}
	}
	s.entries[key] = allDebridUnlockCacheEntry{result: *result, expiresAt: now.Add(allDebridUnlockCacheTTL)}
}

// invalidateDownloadURL drops every cached unlock that produced downloadURL.
// Returns true when at least one entry was removed.
func (s *allDebridUnlockCacheStore) invalidateDownloadURL(downloadURL string) bool {
	downloadURL = strings.TrimSpace(downloadURL)
	if downloadURL == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := false
	for k, entry := range s.entries {
		if entry.result.DownloadURL == downloadURL {
			delete(s.entries, k)
			removed = true
		}
	}
	return removed
}

// InvalidateAllDebridDownloadURL forgets a cached AllDebrid unlock after its
// download URL was proven unusable, so the next request unlocks a fresh link.
func InvalidateAllDebridDownloadURL(downloadURL string) bool {
	return allDebridUnlocks.invalidateDownloadURL(downloadURL)
}
