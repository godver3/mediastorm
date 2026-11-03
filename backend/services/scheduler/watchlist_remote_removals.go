package scheduler

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"novastream/models"
)

// watchlistRemovalTracker detects items removed from a remote watchlist since
// the previous run of a sync task: an item whose keys were in the remote list
// last time and are absent from the complete remote list now.
//
// It replaces the per-item SyncSource marker for that decision. The marker holds
// one source, so with two bidirectional watchlist syncs (Trakt and Simkl) it
// flips between them every run and a removal is only seen by whichever task
// marked the item last. It also marked exports the remote never accepted (not
// found), which a delete-mode run then removed locally. A snapshot only holds
// what the remote actually listed, per task, account and profile.
type watchlistRemovalTracker struct {
	key      string
	provider string
	previous *models.HistorySyncSnapshot
	prevKeys map[string]struct{}
	current  map[string]bool
	unsafe   bool
}

// A complete remote listing missing at least this many previously seen keys,
// and more than half of them, is treated as a bad response, not removals. An
// empty listing is treated the same once the previous one had more keys than
// watchlistRemovalEmptyListMaxKeys (about five items), so a short list can
// still be emptied.
const (
	watchlistRemovalSafetyMinMissing = 25
	watchlistRemovalEmptyListMaxKeys = 20
)

// newWatchlistRemovalTracker returns nil when no snapshot store is configured;
// callers then fall back to the SyncSource marker.
func (s *Service) newWatchlistRemovalTracker(taskID, provider, accountID, profileID string) (*watchlistRemovalTracker, error) {
	s.mu.RLock()
	store := s.historySnapshots
	s.mu.RUnlock()
	if store == nil {
		return nil, nil
	}
	t := &watchlistRemovalTracker{
		key:      strings.Join([]string{"watchlist", taskID, provider, accountID, profileID}, ":"),
		provider: provider,
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyRemovalSnapshotTimeout)
	defer cancel()
	previous, err := store.Get(ctx, t.key)
	if err != nil {
		return nil, fmt.Errorf("load %s watchlist snapshot: %w", provider, err)
	}
	if previous != nil {
		t.previous = previous
		t.prevKeys = make(map[string]struct{}, len(previous.Keys))
		for _, key := range previous.Keys {
			t.prevKeys[key] = struct{}{}
		}
	}
	return t, nil
}

// setRemote supplies the complete current remote key set.
func (t *watchlistRemovalTracker) setRemote(current map[string]bool) {
	if t == nil {
		return
	}
	t.current = current
	if t.previous == nil {
		return
	}
	missing := 0
	for key := range t.prevKeys {
		if !current[key] {
			missing++
		}
	}
	if (len(current) == 0 && len(t.prevKeys) > watchlistRemovalEmptyListMaxKeys) || (missing >= watchlistRemovalSafetyMinMissing && missing*2 > len(t.prevKeys)) {
		t.unsafe = true
		log.Printf("[scheduler] %s watchlist: %d of %d previously seen remote keys are missing (current=%d); not inferring remote removals or replacing the snapshot",
			t.provider, missing, len(t.prevKeys), len(current))
	}
}

// removedRemotely reports whether a local item was on the remote list at the
// previous run and is gone now. An item added locally since then is kept (and
// exported again) rather than removed.
func (t *watchlistRemovalTracker) removedRemotely(item models.WatchlistItem) bool {
	if t == nil || t.previous == nil || t.current == nil || t.unsafe {
		return false
	}
	if item.AddedAt.After(t.previous.SyncedAt) {
		return false
	}
	seenBefore := false
	for _, key := range schedulerWatchlistMatchKeys(item.MediaType, item.ID, item.ExternalIDs) {
		if t.current[key] {
			return false
		}
		if _, ok := t.prevKeys[key]; ok {
			seenBefore = true
		}
	}
	return seenBefore
}

// save replaces the snapshot with the current remote listing.
func (s *Service) saveWatchlistRemovalSnapshot(t *watchlistRemovalTracker) error {
	if t == nil || t.unsafe || t.current == nil {
		return nil
	}
	s.mu.RLock()
	store := s.historySnapshots
	s.mu.RUnlock()
	if store == nil {
		return nil
	}
	keys := make([]string, 0, len(t.current))
	for key, ok := range t.current {
		if ok {
			keys = append(keys, key)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyRemovalSnapshotTimeout)
	defer cancel()
	// Stamp the end of the run, not its start: items this run imported carry an
	// AddedAt inside the run and must still count as "on the remote" next time.
	if err := store.Put(ctx, models.HistorySyncSnapshot{Key: t.key, Keys: keys, SyncedAt: time.Now().UTC()}); err != nil {
		return fmt.Errorf("save %s watchlist snapshot: %w", t.provider, err)
	}
	return nil
}
