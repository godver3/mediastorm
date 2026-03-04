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

	historySvc     HistoryProvider
	usersSvc       UsersProvider
	prequeueStore  *playback.PrequeueStore
	debridStreaming DebridURLRefresher
	configManager  *config.Manager
	workerFn       playback.PrequeueWorkerFunc

	ctx    context.Context
	cancel context.CancelFunc
}

// NewService creates a new prewarm service. If storageDir is provided, warm entries
// are persisted to prewarm.json and survive restarts.
func NewService(cfgManager *config.Manager, storageDir string) *Service {
	svc := &Service{
		entries:       make(map[string]*WarmEntry),
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

		// No existing entry — create a new one with just the stream path
		pqEntry, _ := s.prequeueStore.Create(
			entry.TitleID, entry.TitleName, entry.UserID,
			entry.MediaType, entry.Year, entry.TargetEpisode, "prewarm",
		)
		if pqEntry != nil {
			s.prequeueStore.Update(pqEntry.ID, func(e *playback.PrequeueEntry) {
				e.Status = playback.PrequeueStatusReady
				e.StreamPath = entry.StreamPath
				e.ExpiresAt = entry.ExpiresAt
			})
			entry.PrequeueID = pqEntry.ID
			restored++
		}
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
				if entry, ok := s.prequeueStore.Get(existing.PrequeueID); ok && entry.Status == playback.PrequeueStatusReady {
					result.Skipped++
					continue
				}
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

	// Remove entries that are no longer in continue watching
	s.mu.Lock()
	for key := range s.entries {
		if !activeKeys[key] {
			log.Printf("[prewarm] Removing stale warm entry: %s", key)
			delete(s.entries, key)
			result.Removed++
		}
	}
	if err := s.saveLocked(); err != nil {
		log.Printf("[prewarm] Warning: failed to persist warm entries: %v", err)
	}
	s.mu.Unlock()

	log.Printf("[prewarm] Cycle complete: warmed=%d skipped=%d failed=%d removed=%d",
		result.Warmed, result.Skipped, result.Failed, result.Removed)

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

// load reads persisted warm entries from disk
func (s *Service) load() error {
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

// saveLocked writes warm entries to disk atomically. Caller must hold s.mu.
func (s *Service) saveLocked() error {
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

func entryKey(titleID, userID string) string {
	return titleID + ":" + userID
}
