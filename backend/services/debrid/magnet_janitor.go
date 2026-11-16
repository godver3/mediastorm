package debrid

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"novastream/config"
)

// MagnetJanitor deletes AllDebrid magnets that MediaStorm added for playback
// but no longer needs: prequeue candidates that lost the race or failed
// validation, resolves cancelled mid-flight, and prequeue entries that expired
// or were replaced (e.g. by a prewarm re-resolve).
//
// AllDebrid hands back the existing magnet ID when the same hash is added
// again, so one magnet can back several candidates, users or an active stream.
// Releases are therefore deferred by a grace period and the magnet is only
// deleted if, at that moment, nothing still references it: no ready prequeue
// or warm entry, no recent stream activity, no active-playback mark. When in
// doubt the magnet is kept. If a referenced magnet is ever removed anyway, the
// streaming layer re-adds it from the magnet registry on the next request.
type MagnetJanitor struct {
	cfg       *config.Manager
	streaming *StreamingProvider
	grace     time.Duration

	mu      sync.Mutex
	health  []*HealthService
	sources []func() []string
	pending map[string]*time.Timer

	// deleteFn is overridable in tests.
	deleteFn func(ctx context.Context, provider, torrentID string) error
}

const (
	magnetJanitorGrace          = 10 * time.Minute
	magnetJanitorStreamActivity = 30 * time.Minute
	magnetJanitorDeleteTimeout  = 30 * time.Second
)

// NewMagnetJanitor creates a janitor. streaming may be nil.
func NewMagnetJanitor(cfg *config.Manager, streaming *StreamingProvider) *MagnetJanitor {
	j := &MagnetJanitor{
		cfg:       cfg,
		streaming: streaming,
		grace:     magnetJanitorGrace,
		pending:   make(map[string]*time.Timer),
	}
	j.deleteFn = j.deleteTorrent
	return j
}

// addHealthService registers a health service whose active-playback marks
// protect torrents (each PlaybackService owns one).
func (j *MagnetJanitor) addHealthService(health *HealthService) {
	if j == nil || health == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, existing := range j.health {
		if existing == health {
			return
		}
	}
	j.health = append(j.health, health)
}

// AddStreamPathSource registers a function listing stream paths that are
// still in use (ready prequeue entries, warm entries, ...).
func (j *MagnetJanitor) AddStreamPathSource(source func() []string) {
	if j == nil || source == nil {
		return
	}
	j.mu.Lock()
	j.sources = append(j.sources, source)
	j.mu.Unlock()
}

// ParseDebridStreamTorrent extracts provider and torrent ID from a
// /debrid/{provider}/{torrentID}/... stream path (optionally prefixed with
// webdav/). ok is false for anything else (usenet paths, external URLs).
func ParseDebridStreamTorrent(streamPath string) (provider, torrentID string, ok bool) {
	p := strings.TrimSpace(streamPath)
	if p == "" || strings.Contains(p, "://") {
		return "", "", false
	}
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimPrefix(p, "webdav/")
	provider, torrentID, _, err := parseDebridPath("/" + p)
	if err != nil || provider == "" || torrentID == "" {
		return "", "", false
	}
	return strings.ToLower(provider), torrentID, true
}

// ReleaseStreamPath schedules the magnet behind streamPath for deletion.
// Non-AllDebrid and non-debrid paths are ignored.
func (j *MagnetJanitor) ReleaseStreamPath(streamPath, reason string) {
	provider, torrentID, ok := ParseDebridStreamTorrent(streamPath)
	if !ok {
		return
	}
	j.ReleaseTorrent(provider, torrentID, reason)
}

// ReleaseTorrent schedules a provider torrent for deletion after the grace
// period. Only AllDebrid is handled for now.
func (j *MagnetJanitor) ReleaseTorrent(provider, torrentID, reason string) {
	if j == nil {
		return
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	torrentID = strings.TrimSpace(torrentID)
	if provider != "alldebrid" || torrentID == "" {
		return
	}
	key := provider + "|" + torrentID

	j.mu.Lock()
	defer j.mu.Unlock()
	if timer, exists := j.pending[key]; exists {
		// Restart the grace window from the latest release.
		timer.Stop()
	}
	j.pending[key] = time.AfterFunc(j.grace, func() {
		j.mu.Lock()
		delete(j.pending, key)
		j.mu.Unlock()
		j.sweep(provider, torrentID, reason)
	})
}

// sweep deletes the torrent if nothing references it any more.
func (j *MagnetJanitor) sweep(provider, torrentID, reason string) {
	if holder := j.inUse(provider, torrentID); holder != "" {
		log.Printf("[magnet-janitor] keeping %s magnet %s (%s): still used by %s", provider, torrentID, reason, holder)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), magnetJanitorDeleteTimeout)
	defer cancel()
	if err := j.deleteFn(ctx, provider, torrentID); err != nil {
		log.Printf("[magnet-janitor] failed to delete %s magnet %s (%s): %v", provider, torrentID, reason, err)
		return
	}
	log.Printf("[magnet-janitor] deleted unused %s magnet %s (%s)", provider, torrentID, reason)
}

// inUse returns a description of what still references the torrent, or "".
func (j *MagnetJanitor) inUse(provider, torrentID string) string {
	j.mu.Lock()
	healthServices := append([]*HealthService(nil), j.health...)
	sources := append([]func() []string(nil), j.sources...)
	j.mu.Unlock()
	for _, health := range healthServices {
		if health.isTorrentActive(provider, torrentID) {
			return "active playback"
		}
	}
	if j.streaming != nil && j.streaming.streamedWithin(provider, torrentID, magnetJanitorStreamActivity) {
		return "recent stream activity"
	}
	for _, source := range sources {
		for _, path := range source() {
			if p, id, ok := ParseDebridStreamTorrent(path); ok && p == provider && id == torrentID {
				return "stream path " + path
			}
		}
	}
	return ""
}

func (j *MagnetJanitor) deleteTorrent(ctx context.Context, provider, torrentID string) error {
	if j.cfg == nil {
		return nil
	}
	settings, err := j.cfg.Load()
	if err != nil {
		return err
	}
	for _, pc := range settings.Streaming.DebridProviders {
		if !strings.EqualFold(pc.Provider, provider) || strings.TrimSpace(pc.APIKey) == "" {
			continue
		}
		client, ok := GetProvider(provider, pc.APIKey)
		if !ok {
			continue
		}
		return client.DeleteTorrent(ctx, torrentID)
	}
	return nil
}
