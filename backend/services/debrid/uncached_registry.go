package debrid

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// uncachedHashTTL is how long a provider/hash pair found not cached is kept
// out of further uploads. Providers without a non-mutating cache lookup
// (AllDebrid, Debrid-Link) learn cache state only by uploading the torrent, so
// without this memory every details page or resolve for the same title
// uploads and deletes the same uncached releases again. That upload/delete
// churn is what got AllDebrid accounts banned for "thousands of links".
const uncachedHashTTL = 24 * time.Hour

// ErrRecentlyUncached marks an upload skipped because the provider reported
// the same info hash as not cached within uncachedHashTTL.
var ErrRecentlyUncached = errors.New("torrent recently found not cached")

// RecentlyUncachedError is returned instead of uploading a torrent whose info
// hash the provider recently reported as not cached.
type RecentlyUncachedError struct {
	Provider   string
	InfoHash   string
	RetryAfter time.Duration
}

func (e *RecentlyUncachedError) Error() string {
	return fmt.Sprintf("torrent not cached on %s (remembered for another %s; skipping upload)",
		e.Provider, e.RetryAfter.Round(time.Minute))
}

func (e *RecentlyUncachedError) Unwrap() error { return ErrRecentlyUncached }

type uncachedHashRegistry struct {
	mu        sync.Mutex
	entries   map[string]time.Time // provider:hash -> expiry
	ttl       time.Duration
	now       func() time.Time
	lastSweep time.Time
}

func newUncachedHashRegistry(ttl time.Duration) *uncachedHashRegistry {
	return &uncachedHashRegistry{entries: make(map[string]time.Time), ttl: ttl, now: time.Now}
}

var recentUncached = newUncachedHashRegistry(uncachedHashTTL)

func uncachedRegistryKey(provider, infoHash string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	infoHash = strings.ToLower(strings.TrimSpace(infoHash))
	if provider == "" || infoHash == "" {
		return ""
	}
	return provider + ":" + infoHash
}

func (r *uncachedHashRegistry) mark(provider, infoHash string) {
	key := uncachedRegistryKey(provider, infoHash)
	if key == "" {
		return
	}
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[key] = now.Add(r.ttl)
	if now.Sub(r.lastSweep) > time.Hour {
		for k, expiry := range r.entries {
			if !now.Before(expiry) {
				delete(r.entries, k)
			}
		}
		r.lastSweep = now
	}
}

func (r *uncachedHashRegistry) remaining(provider, infoHash string) (time.Duration, bool) {
	key := uncachedRegistryKey(provider, infoHash)
	if key == "" {
		return 0, false
	}
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	expiry, ok := r.entries[key]
	if !ok {
		return 0, false
	}
	if !now.Before(expiry) {
		delete(r.entries, key)
		return 0, false
	}
	return expiry.Sub(now), true
}

func (r *uncachedHashRegistry) clear(provider, infoHash string) {
	key := uncachedRegistryKey(provider, infoHash)
	if key == "" {
		return
	}
	r.mu.Lock()
	delete(r.entries, key)
	r.mu.Unlock()
}

// RememberUncached records that provider reported infoHash as not cached. The
// first non-empty hash is used, so callers can pass their candidates in
// preference order (candidate hash, then the provider-reported hash).
func RememberUncached(provider string, infoHashes ...string) {
	for _, hash := range infoHashes {
		if strings.TrimSpace(hash) == "" {
			continue
		}
		recentUncached.mark(provider, hash)
		log.Printf("[debrid-uncached] remembering %s hash %s as not cached for %s", provider, strings.ToLower(strings.TrimSpace(hash)), uncachedHashTTL)
		return
	}
}

// IsRecentlyUncached reports whether provider found infoHash not cached within
// the last uncachedHashTTL.
func IsRecentlyUncached(provider, infoHash string) bool {
	_, ok := recentUncached.remaining(provider, infoHash)
	return ok
}

func recentlyUncachedError(provider, infoHash string) error {
	remaining, ok := recentUncached.remaining(provider, infoHash)
	if !ok {
		return nil
	}
	log.Printf("[debrid-uncached] skipping %s upload of hash %s: not cached when last tried", provider, strings.ToLower(strings.TrimSpace(infoHash)))
	return &RecentlyUncachedError{Provider: provider, InfoHash: strings.ToLower(strings.TrimSpace(infoHash)), RetryAfter: remaining}
}

// noteAddResult updates the registry from an add response that authoritatively
// reported cache state (e.g. AllDebrid's upload "ready" flag).
func noteAddResult(provider, infoHash string, result *AddMagnetResult) {
	if result == nil || !result.CacheStatusKnown || strings.TrimSpace(infoHash) == "" {
		return
	}
	if result.Cached {
		recentUncached.clear(provider, infoHash)
		return
	}
	RememberUncached(provider, infoHash)
}

// addMagnetUnlessUncached uploads a magnet unless its info hash was recently
// found not cached on this provider. Every resolution and health-probe upload
// goes through here (or torrentSource.add) so the memory covers all callers.
func addMagnetUnlessUncached(ctx context.Context, client Provider, magnetURL string) (*AddMagnetResult, error) {
	infoHash := extractInfoHashFromMagnet(magnetURL)
	if err := recentlyUncachedError(client.Name(), infoHash); err != nil {
		return nil, err
	}
	result, err := client.AddMagnet(ctx, magnetURL)
	if err == nil {
		noteAddResult(client.Name(), infoHash, result)
	}
	return result, err
}
