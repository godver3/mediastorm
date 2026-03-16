package prewarm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/internal/datastore"
	"novastream/models"
	"novastream/services/playback"
)

// HistoryProvider provides access to continue watching data
type HistoryProvider interface {
	ListContinueWatching(userID string) ([]models.SeriesWatchState, error)
}

// UsersProvider provides access to user profiles
type UsersProvider interface {
	ListAll() []models.User
}

// DebridURLRefresher refreshes debrid direct URLs to keep them alive
type DebridURLRefresher interface {
	GetDirectURL(ctx context.Context, path string) (string, error)
}

// WarmEntry represents a pre-warmed continue watching item
type WarmEntry struct {
	TitleID       string                   `json:"titleId"`
	TitleName     string                   `json:"titleName"`
	UserID        string                   `json:"userId"`
	MediaType     string                   `json:"mediaType"`
	Year          int                      `json:"year,omitempty"`
	ImdbID        string                   `json:"imdbId,omitempty"`
	TargetEpisode *models.EpisodeReference `json:"targetEpisode,omitempty"`
	PrequeueID    string                   `json:"prequeueId"`
	StreamPath    string                   `json:"streamPath,omitempty"`
	LastRefresh   time.Time                `json:"lastRefresh"`
	LastResolve   time.Time                `json:"lastResolve"`
	Error         string                   `json:"error,omitempty"`
	ExpiresAt     time.Time                `json:"expiresAt"`
}

// SyncResult contains the result of a prewarm cycle
type SyncResult struct {
	Warmed  int
	Skipped int
	Failed  int
	Removed int
}

// Service manages pre-warming of continue watching items
type Service struct {
	mu      sync.RWMutex
	entries map[string]*WarmEntry // key: "titleID:userID"
	path    string                // persistence file path
	store   *datastore.DataStore  // PostgreSQL backing store (nil = JSON file mode)

	// Ad-hoc entries adopted from user prequeue requests (key: prequeueID, value: adoption time)
	adhocMu      sync.RWMutex
	adhocEntries map[string]time.Time

	historySvc      HistoryProvider
	usersSvc        UsersProvider
	prequeueStore   *playback.PrequeueStore
	debridStreaming DebridURLRefresher
	configManager   *config.Manager
	workerFn        playback.PrequeueWorkerFunc

	ctx    context.Context
	cancel context.CancelFunc
}

func hasTrackMetadata(entry *playback.PrequeueEntry) bool {
	if entry == nil {
		return false
	}
	return len(entry.AudioTracks) > 0 || len(entry.SubtitleTracks) > 0
}

// NewService creates a new prewarm service. If storageDir is provided, warm entries
// are persisted to prewarm.json and survive restarts.
func NewService(cfgManager *config.Manager, storageDir string) *Service {
	svc := &Service{
		entries:       make(map[string]*WarmEntry),
		adhocEntries:  make(map[string]time.Time),
		configManager: cfgManager,
	}

	if strings.TrimSpace(storageDir) != "" {
		svc.path = filepath.Join(storageDir, "prewarm.json")
		if err := svc.load(); err != nil {
			log.Printf("[prewarm] Warning: failed to load persisted data: %v", err)
		}
	}

	return svc
}

// SetHistoryService sets the history provider
func (s *Service) SetHistoryService(svc HistoryProvider) {
	s.historySvc = svc
}

// SetUsersService sets the users provider
func (s *Service) SetUsersService(svc UsersProvider) {
	s.usersSvc = svc
}

// SetPrequeueStore sets the prequeue store for creating prequeue entries
func (s *Service) SetPrequeueStore(store *playback.PrequeueStore) {
	s.prequeueStore = store
}

// SetDebridStreaming sets the debrid URL refresher
func (s *Service) SetDebridStreaming(provider DebridURLRefresher) {
	s.debridStreaming = provider
}

// SetWorkerFunc sets the prequeue worker function for resolving items
func (s *Service) SetWorkerFunc(fn playback.PrequeueWorkerFunc) {
	s.workerFn = fn
}

// SetDataStore sets the PostgreSQL backing store for persistence.
func (s *Service) SetDataStore(store *datastore.DataStore) {
	s.store = store
}

// useDB returns true when the service is backed by PostgreSQL.
func (s *Service) useDB() bool { return s.store != nil }

// AdoptEntry registers an ad-hoc prequeue entry with prewarm so it stays alive.
// Called by the prequeue handler after creating an ad-hoc entry.
func (s *Service) AdoptEntry(prequeueID string) {
	s.adhocMu.Lock()
	defer s.adhocMu.Unlock()
	s.adhocEntries[prequeueID] = time.Now()
	log.Printf("[prewarm] Adopted ad-hoc prequeue entry %s", prequeueID)
}

// RestorePrequeueEntries re-creates PrequeueStore entries from persisted warm data.
// Call this after wiring all dependencies and before starting the service.
func (s *Service) RestorePrequeueEntries() {
	if s.prequeueStore == nil {
		log.Printf("[prewarm] RestorePrequeueEntries: no prequeue store set, skipping")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	log.Printf("[prewarm] RestorePrequeueEntries: %d entries loaded from disk (path=%s)", len(s.entries), s.path)

	now := time.Now()
	restored := 0
	removed := 0

	for key, entry := range s.entries {
		// Remove expired entries
		if now.After(entry.ExpiresAt) {
			log.Printf("[prewarm] Removing expired entry: %s (%s) expired=%v", key, entry.TitleName, entry.ExpiresAt)
			delete(s.entries, key)
			removed++
			continue
		}

		// Skip entries with errors or no stream path
		if entry.Error != "" || entry.StreamPath == "" {
			log.Printf("[prewarm] Removing entry with error/no stream: %s (%s) error=%q streamPath=%q", key, entry.TitleName, entry.Error, entry.StreamPath)
			delete(s.entries, key)
			removed++
			continue
		}

		// Check if the prequeue store already has a valid entry for this title+user
		// (loaded from prequeue.json which contains full track data)
		if existing, ok := s.prequeueStore.GetByTitleUser(entry.TitleID, entry.UserID); ok && existing.Status == playback.PrequeueStatusReady {
			entry.PrequeueID = existing.ID
			restored++
			log.Printf("[prewarm] Reused existing prequeue entry %s for %s (from prequeue.json)", existing.ID, entry.TitleName)
			continue
		}

		// No existing prequeue entry found with full metadata. Keep warm metadata,
		// but force a fresh resolve on next prewarm/details request instead of
		// recreating a title-only ready entry that lacks audio/subtitle tracks.
		entry.PrequeueID = ""
		log.Printf("[prewarm] Deferring restore for %s (%s): missing prequeue entry with tracks, will re-warm",
			key, entry.TitleName)
	}

	if restored > 0 || removed > 0 {
		log.Printf("[prewarm] Restored %d warm entries from disk (%d expired/removed)", restored, removed)
	}
	if err := s.saveLocked(); err != nil {
		log.Printf("[prewarm] Warning: failed to save after restore: %v", err)
	}
}

// Start begins the background URL refresh ticker
func (s *Service) Start(ctx context.Context) {
	s.ctx, s.cancel = context.WithCancel(ctx)

	refreshInterval := s.getRefreshInterval()

	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()

		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				if err := s.RefreshURLs(s.ctx); err != nil {
					log.Printf("[prewarm] URL refresh error: %v", err)
				}
			}
		}
	}()

	log.Printf("[prewarm] Background URL refresh started (interval: %v)", refreshInterval)
}

// Stop stops the background refresh
func (s *Service) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
}

// RunOnce performs a single prewarm cycle: syncs continue watching items for all profiles
func (s *Service) RunOnce(ctx context.Context) (SyncResult, error) {
	if s.historySvc == nil || s.usersSvc == nil || s.workerFn == nil {
		return SyncResult{}, fmt.Errorf("prewarm service not fully configured")
	}

	var result SyncResult

	// Get all profiles
	users := s.usersSvc.ListAll()
	if len(users) == 0 {
		log.Printf("[prewarm] No profiles found, skipping")
		return result, nil
	}

	maxAge := s.getMaxAge()

	// Track which entries are still valid (in continue watching)
	activeKeys := make(map[string]bool)

	for _, user := range users {
		states, err := s.historySvc.ListContinueWatching(user.ID)
		if err != nil {
			log.Printf("[prewarm] Failed to get continue watching for user %s: %v", user.Name, err)
			continue
		}

		for _, state := range states {
			key := entryKey(state.SeriesID, user.ID)
			activeKeys[key] = true

			// Check if we already have a warm entry that's ready
			s.mu.RLock()
			existing, hasExisting := s.entries[key]
			s.mu.RUnlock()

			if hasExisting && existing.PrequeueID != "" {
				// Check if the prequeue entry is still valid
				if entry, ok := s.prequeueStore.Get(existing.PrequeueID); ok && entry.Status == playback.PrequeueStatusReady && hasTrackMetadata(entry) {
					result.Skipped++
					continue
				}
				log.Printf("[prewarm] Re-warming %q for user %s: existing prequeue missing track metadata or not ready",
					state.SeriesTitle, user.Name)
			}

			// Determine target episode
			var targetEpisode *models.EpisodeReference
			mediaType := "movie"

			if state.NextEpisode != nil && state.NextEpisode.SeasonNumber > 0 {
				targetEpisode = state.NextEpisode
				mediaType = "series"
			} else if state.LastWatched.SeasonNumber > 0 {
				// Has episode info but no next episode — it's a series that's been fully watched
				// Skip fully watched series
				continue
			}

			// Get IMDB ID from external IDs
			imdbID := ""
			if state.ExternalIDs != nil {
				imdbID = state.ExternalIDs["imdbId"]
			}

			log.Printf("[prewarm] Warming: %q (%s) for user %s", state.SeriesTitle, mediaType, user.Name)

			// Run the prequeue worker synchronously
			prequeueID, err := s.workerFn(ctx, state.SeriesID, state.SeriesTitle, imdbID, mediaType, state.Year, user.ID, targetEpisode)

			warmEntry := &WarmEntry{
				TitleID:       state.SeriesID,
				TitleName:     state.SeriesTitle,
				UserID:        user.ID,
				MediaType:     mediaType,
				Year:          state.Year,
				ImdbID:        imdbID,
				TargetEpisode: targetEpisode,
				PrequeueID:    prequeueID,
				LastResolve:   time.Now(),
				ExpiresAt:     time.Now().Add(maxAge),
			}

			if err != nil {
				warmEntry.Error = err.Error()
				result.Failed++
				log.Printf("[prewarm] Failed to warm %q for user %s: %v", state.SeriesTitle, user.Name, err)
			} else {
				// Get stream path from prequeue entry for URL refresh and extend its TTL
				if pqEntry, ok := s.prequeueStore.Get(prequeueID); ok {
					warmEntry.StreamPath = pqEntry.StreamPath
					warmEntry.LastRefresh = time.Now()
					s.prequeueStore.Update(prequeueID, func(e *playback.PrequeueEntry) {
						e.ExpiresAt = warmEntry.ExpiresAt
					})
				}
				result.Warmed++
				log.Printf("[prewarm] Warmed: %q for user %s (prequeueID=%s)", state.SeriesTitle, user.Name, prequeueID)
			}

			s.mu.Lock()
			s.entries[key] = warmEntry
			if err := s.saveLocked(); err != nil {
				log.Printf("[prewarm] Warning: failed to persist after warming %q: %v", state.SeriesTitle, err)
			}
			s.mu.Unlock()
		}
	}

	// Remove entries that are no longer in continue watching (but keep ad-hoc adopted ones)
	s.mu.Lock()
	for key := range s.entries {
		if !activeKeys[key] && !s.isAdoptedEntry(s.entries[key]) {
			log.Printf("[prewarm] Removing stale warm entry: %s", key)
			delete(s.entries, key)
			result.Removed++
		}
	}
	if err := s.saveLocked(); err != nil {
		log.Printf("[prewarm] Warning: failed to persist warm entries: %v", err)
	}
	s.mu.Unlock()

	// Re-resolve expired prequeue entries (dynamic TTL refresh)
	reResolved := s.reResolveExpired(ctx)

	// Prune ad-hoc entries older than 24h that aren't in continue watching
	pruned := s.pruneAdhocEntries(activeKeys)

	log.Printf("[prewarm] Cycle complete: warmed=%d skipped=%d failed=%d removed=%d reResolved=%d adhocPruned=%d",
		result.Warmed, result.Skipped, result.Failed, result.Removed, reResolved, pruned)

	return result, nil
}

// RefreshURLs refreshes debrid URLs for all warm entries to keep them alive
func (s *Service) RefreshURLs(ctx context.Context) error {
	if s.debridStreaming == nil {
		return nil
	}

	refreshInterval := s.getRefreshInterval()

	s.mu.RLock()
	entries := make([]*WarmEntry, 0, len(s.entries))
	for _, e := range s.entries {
		entries = append(entries, e)
	}
	s.mu.RUnlock()

	refreshed := 0
	for _, entry := range entries {
		if entry.StreamPath == "" || entry.Error != "" {
			continue
		}

		if time.Since(entry.LastRefresh) < refreshInterval {
			continue
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		_, err := s.debridStreaming.GetDirectURL(ctx, entry.StreamPath)
		if err != nil {
			log.Printf("[prewarm] URL refresh warning for %q: %v (URL may still work)", entry.TitleName, err)
			continue
		}

		s.mu.Lock()
		entry.LastRefresh = time.Now()
		s.mu.Unlock()
		refreshed++
	}

	if refreshed > 0 {
		log.Printf("[prewarm] Refreshed %d URLs", refreshed)
	}
	return nil
}

// GetWarm returns a warm entry for the given title+user, or nil if not found
func (s *Service) GetWarm(titleID, userID string) *playback.WarmRef {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.entries[entryKey(titleID, userID)]
	if !ok || entry.Error != "" || entry.PrequeueID == "" {
		return nil
	}

	if time.Now().After(entry.ExpiresAt) {
		return nil
	}

	return &playback.WarmRef{
		PrequeueID: entry.PrequeueID,
	}
}

// ListAll returns all warm entries (for admin viewer)
func (s *Service) ListAll() []*WarmEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*WarmEntry, 0, len(s.entries))
	for _, e := range s.entries {
		result = append(result, e)
	}
	return result
}

// load reads persisted warm entries from disk or database
func (s *Service) load() error {
	if s.useDB() {
		rows, err := s.store.Prewarm().List(context.Background())
		if err != nil {
			return fmt.Errorf("load prewarm from db: %w", err)
		}
		s.entries = make(map[string]*WarmEntry, len(rows))
		for _, data := range rows {
			var e WarmEntry
			if err := json.Unmarshal(data, &e); err != nil {
				log.Printf("[prewarm] Warning: failed to unmarshal db entry: %v", err)
				continue
			}
			s.entries[entryKey(e.TitleID, e.UserID)] = &e
		}
		log.Printf("[prewarm] Loaded %d warm entries from database", len(s.entries))
		return nil
	}

	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil // First run
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", s.path, err)
	}
	defer file.Close()

	var stored []*WarmEntry
	if err := json.NewDecoder(file).Decode(&stored); err != nil {
		return fmt.Errorf("decode %s: %w", s.path, err)
	}

	s.entries = make(map[string]*WarmEntry, len(stored))
	for _, e := range stored {
		s.entries[entryKey(e.TitleID, e.UserID)] = e
	}
	log.Printf("[prewarm] Loaded %d warm entries from disk", len(stored))
	return nil
}

// saveLocked writes warm entries to disk or database atomically. Caller must hold s.mu.
func (s *Service) saveLocked() error {
	if s.useDB() {
		return s.syncToDB()
	}

	if s.path == "" {
		return nil
	}

	items := make([]*WarmEntry, 0, len(s.entries))
	for _, e := range s.entries {
		items = append(items, e)
	}

	tmp := s.path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(items); err != nil {
		file.Close()
		os.Remove(tmp)
		return fmt.Errorf("encode: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(tmp)
		return fmt.Errorf("sync: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// syncToDB writes the full in-memory prewarm state to PostgreSQL.
func (s *Service) syncToDB() error {
	ctx := context.Background()
	repo := s.store.Prewarm()

	// Upsert all in-memory entries
	for key, entry := range s.entries {
		data, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("marshal prewarm entry %s: %w", key, err)
		}
		if err := repo.Upsert(ctx, key, entry.TitleID, entry.UserID, data, entry.ExpiresAt); err != nil {
			return fmt.Errorf("upsert prewarm entry %s: %w", key, err)
		}
	}

	// Delete entries from DB that are no longer in memory
	dbRows, err := repo.List(ctx)
	if err != nil {
		return fmt.Errorf("list prewarm from db for cleanup: %w", err)
	}
	memKeys := make(map[string]bool, len(s.entries))
	for key := range s.entries {
		memKeys[key] = true
	}
	for _, data := range dbRows {
		var e WarmEntry
		if err := json.Unmarshal(data, &e); err != nil {
			continue
		}
		key := entryKey(e.TitleID, e.UserID)
		if !memKeys[key] {
			_ = repo.Delete(ctx, key)
		}
	}

	return nil
}

// getRefreshInterval returns the URL refresh interval from config (default: 8 minutes)
func (s *Service) getRefreshInterval() time.Duration {
	if s.configManager != nil {
		settings, err := s.configManager.Load()
		if err == nil {
			for _, task := range settings.ScheduledTasks.Tasks {
				if task.Type == config.ScheduledTaskTypePrewarm {
					if val, ok := task.Config["refreshIntervalMin"]; ok {
						if mins, err := strconv.Atoi(val); err == nil && mins > 0 {
							return time.Duration(mins) * time.Minute
						}
					}
					break
				}
			}
		}
	}
	return 8 * time.Minute
}

// getMaxAge returns how long warm entries stay valid (default: 12 hours)
func (s *Service) getMaxAge() time.Duration {
	if s.configManager != nil {
		settings, err := s.configManager.Load()
		if err == nil {
			for _, task := range settings.ScheduledTasks.Tasks {
				if task.Type == config.ScheduledTaskTypePrewarm {
					if val, ok := task.Config["maxAgeHours"]; ok {
						if hours, err := strconv.Atoi(val); err == nil && hours > 0 {
							return time.Duration(hours) * time.Hour
						}
					}
					break
				}
			}
		}
	}
	return 12 * time.Hour
}

// reResolveExpired re-resolves prequeue entries whose dynamic TTL has expired.
// Returns the number of entries re-resolved.
func (s *Service) reResolveExpired(ctx context.Context) int {
	if s.prequeueStore == nil || s.workerFn == nil {
		return 0
	}

	expired := s.prequeueStore.ListExpired()
	if len(expired) == 0 {
		return 0
	}

	reResolved := 0
	for _, entry := range expired {
		select {
		case <-ctx.Done():
			return reResolved
		default:
		}

		log.Printf("[prewarm] Re-resolving expired prequeue %s (%s) for user %s",
			entry.ID, entry.TitleName, entry.UserID)

		var imdbID string
		var targetEpisode *models.EpisodeReference
		if entry.TargetEpisode != nil {
			targetEpisode = entry.TargetEpisode
		}

		newPqID, err := s.workerFn(ctx, entry.TitleID, entry.TitleName, imdbID, entry.MediaType, entry.Year, entry.UserID, targetEpisode)
		if err != nil {
			log.Printf("[prewarm] Re-resolve failed for %s (%s): %v", entry.ID, entry.TitleName, err)
			continue
		}

		// Update warm entry if we have one
		key := entryKey(entry.TitleID, entry.UserID)
		s.mu.Lock()
		if warmEntry, ok := s.entries[key]; ok {
			warmEntry.PrequeueID = newPqID
			warmEntry.LastResolve = time.Now()
			warmEntry.Error = ""
			if pqEntry, ok := s.prequeueStore.Get(newPqID); ok {
				warmEntry.StreamPath = pqEntry.StreamPath
				warmEntry.LastRefresh = time.Now()
			}
		}
		s.mu.Unlock()

		reResolved++
		log.Printf("[prewarm] Re-resolved %s → %s", entry.ID, newPqID)
	}

	if reResolved > 0 {
		s.mu.Lock()
		if err := s.saveLocked(); err != nil {
			log.Printf("[prewarm] Warning: failed to persist after re-resolve: %v", err)
		}
		s.mu.Unlock()
	}

	return reResolved
}

// pruneAdhocEntries removes ad-hoc entries older than 24h that aren't in continue watching.
// Returns the number pruned.
func (s *Service) pruneAdhocEntries(activeKeys map[string]bool) int {
	s.adhocMu.Lock()
	defer s.adhocMu.Unlock()

	now := time.Now()
	pruned := 0
	const adhocMaxAge = 24 * time.Hour

	for pqID, adoptedAt := range s.adhocEntries {
		if now.Sub(adoptedAt) < adhocMaxAge {
			continue
		}

		// Check if this prequeue entry's title+user is in continue watching
		if pqEntry, ok := s.prequeueStore.Get(pqID); ok {
			key := entryKey(pqEntry.TitleID, pqEntry.UserID)
			if activeKeys[key] {
				continue // In continue watching, keep it
			}
		}

		delete(s.adhocEntries, pqID)
		pruned++
		log.Printf("[prewarm] Pruned ad-hoc entry %s (adopted %v ago)", pqID, now.Sub(adoptedAt).Round(time.Minute))
	}

	return pruned
}

// isAdoptedEntry checks if a warm entry corresponds to an adopted ad-hoc prequeue.
func (s *Service) isAdoptedEntry(entry *WarmEntry) bool {
	if entry == nil || entry.PrequeueID == "" {
		return false
	}
	s.adhocMu.RLock()
	defer s.adhocMu.RUnlock()
	_, adopted := s.adhocEntries[entry.PrequeueID]
	return adopted
}

func entryKey(titleID, userID string) string {
	return titleID + ":" + userID
}
