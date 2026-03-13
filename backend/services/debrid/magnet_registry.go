package debrid

import (
	"log"
	"sync"
	"time"
)

// magnetEntry stores a magnet link with its registration time for TTL-based cleanup.
type magnetEntry struct {
	magnetLink string
	provider   string
	registedAt time.Time
}

// magnetRegistry maps provider:torrentID -> magnet link so the streaming layer
// can re-add expired/deleted torrents transparently.
type magnetRegistry struct {
	mu      sync.RWMutex
	entries map[string]magnetEntry // key: "provider:torrentID"
	ttl     time.Duration
}

var globalMagnetRegistry = &magnetRegistry{
	entries: make(map[string]magnetEntry),
	ttl:     24 * time.Hour, // Keep mappings for 24 hours
}

func magnetKey(provider, torrentID string) string {
	return provider + ":" + torrentID
}

// RegisterMagnet stores the magnet link for a given provider + torrent ID.
// Called after a successful AddMagnet during playback resolution.
func RegisterMagnet(provider, torrentID, magnetLink string) {
	if magnetLink == "" || torrentID == "" {
		return
	}
	r := globalMagnetRegistry
	r.mu.Lock()
	defer r.mu.Unlock()

	key := magnetKey(provider, torrentID)
	r.entries[key] = magnetEntry{
		magnetLink: magnetLink,
		provider:   provider,
		registedAt: time.Now(),
	}

	// Lazy cleanup of expired entries
	now := time.Now()
	for k, e := range r.entries {
		if now.Sub(e.registedAt) > r.ttl {
			delete(r.entries, k)
		}
	}

	log.Printf("[magnet-registry] registered torrent %s for %s", torrentID, provider)
}

// LookupMagnet retrieves the magnet link for a given provider + torrent ID.
func LookupMagnet(provider, torrentID string) (string, bool) {
	r := globalMagnetRegistry
	r.mu.RLock()
	defer r.mu.RUnlock()

	key := magnetKey(provider, torrentID)
	entry, ok := r.entries[key]
	if !ok {
		return "", false
	}

	// Check TTL
	if time.Since(entry.registedAt) > r.ttl {
		return "", false
	}

	return entry.magnetLink, true
}
