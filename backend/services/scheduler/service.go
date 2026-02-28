package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/backup"
	"novastream/services/epg"
	"novastream/services/history"
	"novastream/services/plex"
	"novastream/services/trakt"
	"novastream/services/watchlist"
)

// Service manages scheduled task execution
type Service struct {
	configManager    *config.Manager
	plexClient       *plex.Client
	traktClient      *trakt.Client
	watchlistService *watchlist.Service
	epgService       *epg.Service
	backupService    *backup.Service
	historyService   *history.Service

	// Runtime state
	mu      sync.RWMutex
	running bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	// Task state tracking (in-memory, not persisted)
	taskRunning map[string]bool
	taskMu      sync.RWMutex
}

// SyncResult contains the result of a sync operation including dry run details
type SyncResult struct {
	Count    int
	DryRun   bool
	ToAdd    []config.DryRunItem
	ToRemove []config.DryRunItem
	Message  string // Optional message for display
}

// NewService creates a new scheduler service
func NewService(
	configManager *config.Manager,
	plexClient *plex.Client,
	traktClient *trakt.Client,
	watchlistService *watchlist.Service,
) *Service {
	return &Service{
		configManager:    configManager,
		plexClient:       plexClient,
		traktClient:      traktClient,
		watchlistService: watchlistService,
		taskRunning:      make(map[string]bool),
	}
}

// Start begins the scheduler background loop
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return nil
	}

	s.ctx, s.cancel = context.WithCancel(ctx)
	s.running = true

	// Start the main scheduler loop
	s.wg.Add(1)
	go s.schedulerLoop()

	log.Println("[scheduler] Scheduler service started")
	return nil
}

// Stop gracefully stops the scheduler
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	s.cancel()

	// Wait for all tasks to complete with timeout
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Println("[scheduler] Scheduler service stopped gracefully")
	case <-ctx.Done():
		log.Println("[scheduler] Scheduler service stopped (timeout)")
	}

	s.running = false
	return nil
}

// schedulerLoop is the main background loop that checks for tasks to run
func (s *Service) schedulerLoop() {
	defer s.wg.Done()

	// Load check interval from settings
	settings, err := s.configManager.Load()
	if err != nil {
		log.Printf("[scheduler] Failed to load settings: %v", err)
		return
	}

	checkInterval := time.Duration(settings.ScheduledTasks.CheckIntervalSeconds) * time.Second
	if checkInterval < time.Second {
		checkInterval = 60 * time.Second
	}

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	// Run check immediately on start
	s.checkAndRunTasks()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.checkAndRunTasks()
		}
	}
}

// checkAndRunTasks checks all enabled tasks and runs those that are due
func (s *Service) checkAndRunTasks() {
	settings, err := s.configManager.Load()
	if err != nil {
		log.Printf("[scheduler] Failed to load settings: %v", err)
		return
	}

	for _, task := range settings.ScheduledTasks.Tasks {
		if !task.Enabled {
			continue
		}

		if s.shouldRun(task) {
			// Run task in goroutine to not block other tasks
			s.wg.Add(1)
			go func(t config.ScheduledTask) {
				defer s.wg.Done()
				s.executeTask(t)
			}(task)
		}
	}
}

// shouldRun checks if a task is due to run
func (s *Service) shouldRun(task config.ScheduledTask) bool {
	// Check if already running
	s.taskMu.RLock()
	if s.taskRunning[task.ID] {
		s.taskMu.RUnlock()
		return false
	}
	s.taskMu.RUnlock()

	// Never run before
	if task.LastRunAt == nil {
		return true
	}

	interval := s.getInterval(task.Frequency)
	return time.Since(*task.LastRunAt) >= interval
}

// getInterval returns the duration for a given frequency
func (s *Service) getInterval(freq config.ScheduledTaskFrequency) time.Duration {
	switch freq {
	case config.ScheduledTaskFrequency1Min:
		return 1 * time.Minute
	case config.ScheduledTaskFrequency5Min:
		return 5 * time.Minute
	case config.ScheduledTaskFrequency15Min:
		return 15 * time.Minute
	case config.ScheduledTaskFrequency30Min:
		return 30 * time.Minute
	case config.ScheduledTaskFrequencyHourly:
		return 1 * time.Hour
	case config.ScheduledTaskFrequency6Hours:
		return 6 * time.Hour
	case config.ScheduledTaskFrequency12Hours:
		return 12 * time.Hour
	case config.ScheduledTaskFrequencyDaily:
		return 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

// executeTask runs a task and updates its status
func (s *Service) executeTask(task config.ScheduledTask) {
	// Mark as running
	s.taskMu.Lock()
	s.taskRunning[task.ID] = true
	s.taskMu.Unlock()

	defer func() {
		s.taskMu.Lock()
		delete(s.taskRunning, task.ID)
		s.taskMu.Unlock()
	}()

	log.Printf("[scheduler] Executing task: %s (%s)", task.Name, task.Type)

	var err error
	var result SyncResult

	switch task.Type {
	case config.ScheduledTaskTypePlexWatchlistSync:
		result, err = s.executePlexWatchlistSync(task)
	case config.ScheduledTaskTypeTraktListSync:
		result, err = s.executeTraktListSync(task)
	case config.ScheduledTaskTypeEPGRefresh:
		result, err = s.executeEPGRefresh(task)
	case config.ScheduledTaskTypePlaylistRefresh:
		result, err = s.executePlaylistRefresh(task)
	case config.ScheduledTaskTypeBackup:
		result, err = s.executeBackup(task)
	case config.ScheduledTaskTypeTraktHistorySync:
		result, err = s.executeTraktHistorySync(task)
	default:
		log.Printf("[scheduler] Unknown task type: %s", task.Type)
		return
	}

	// Update task status in settings
	s.updateTaskStatus(task.ID, err, result)
}

// updateTaskStatus updates a task's status in the settings file
func (s *Service) updateTaskStatus(taskID string, err error, result SyncResult) {
	settings, loadErr := s.configManager.Load()
	if loadErr != nil {
		log.Printf("[scheduler] Failed to load settings to update task status: %v", loadErr)
		return
	}

	now := time.Now().UTC()
	for i := range settings.ScheduledTasks.Tasks {
		if settings.ScheduledTasks.Tasks[i].ID == taskID {
			settings.ScheduledTasks.Tasks[i].LastRunAt = &now
			settings.ScheduledTasks.Tasks[i].ItemsImported = result.Count

			// Store dry run details if this was a dry run
			if result.DryRun {
				settings.ScheduledTasks.Tasks[i].DryRunDetails = &config.DryRunDetails{
					ToAdd:    result.ToAdd,
					ToRemove: result.ToRemove,
				}
			} else {
				// Clear dry run details for real runs
				settings.ScheduledTasks.Tasks[i].DryRunDetails = nil
			}

			if err != nil {
				settings.ScheduledTasks.Tasks[i].LastStatus = config.ScheduledTaskStatusError
				settings.ScheduledTasks.Tasks[i].LastError = err.Error()
				log.Printf("[scheduler] Task %s failed: %v", taskID, err)
			} else {
				settings.ScheduledTasks.Tasks[i].LastStatus = config.ScheduledTaskStatusSuccess
				settings.ScheduledTasks.Tasks[i].LastError = ""
				if result.DryRun {
					log.Printf("[scheduler] Task %s dry run completed: %d items to add, %d items to remove", taskID, len(result.ToAdd), len(result.ToRemove))
				} else {
					log.Printf("[scheduler] Task %s completed successfully, imported %d items", taskID, result.Count)
				}
			}
			break
		}
	}

	if saveErr := s.configManager.Save(settings); saveErr != nil {
		log.Printf("[scheduler] Failed to save task status: %v", saveErr)
	}
}

// RunTaskNow triggers immediate execution of a task
func (s *Service) RunTaskNow(taskID string) error {
	settings, err := s.configManager.Load()
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}

	for _, task := range settings.ScheduledTasks.Tasks {
		if task.ID == taskID {
			// Check if already running
			s.taskMu.RLock()
			if s.taskRunning[taskID] {
				s.taskMu.RUnlock()
				return errors.New("task is already running")
			}
			s.taskMu.RUnlock()

			s.wg.Add(1)
			go func(t config.ScheduledTask) {
				defer s.wg.Done()
				s.executeTask(t)
			}(task)
			return nil
		}
	}

	return errors.New("task not found")
}

// GetTaskStatus returns all tasks with their current status
// Running tasks will have their status overridden to "running"
func (s *Service) GetTaskStatus() []config.ScheduledTask {
	settings, err := s.configManager.Load()
	if err != nil {
		return nil
	}

	s.taskMu.RLock()
	defer s.taskMu.RUnlock()

	tasks := make([]config.ScheduledTask, len(settings.ScheduledTasks.Tasks))
	for i, task := range settings.ScheduledTasks.Tasks {
		tasks[i] = task
		if s.taskRunning[task.ID] {
			tasks[i].LastStatus = config.ScheduledTaskStatusRunning
		}
	}

	return tasks
}

// IsTaskRunning checks if a specific task is currently running
func (s *Service) IsTaskRunning(taskID string) bool {
	s.taskMu.RLock()
	defer s.taskMu.RUnlock()
	return s.taskRunning[taskID]
}

// SetEPGService sets the EPG service for scheduled EPG refresh tasks.
func (s *Service) SetEPGService(epgService *epg.Service) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epgService = epgService
}

// SetBackupService sets the backup service for scheduled backup tasks.
func (s *Service) SetBackupService(backupService *backup.Service) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backupService = backupService
}

// SetHistoryService sets the history service for scheduled history sync tasks.
func (s *Service) SetHistoryService(historyService *history.Service) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.historyService = historyService
}

// executePlexWatchlistSync syncs a Plex watchlist to/from a profile
func (s *Service) executePlexWatchlistSync(task config.ScheduledTask) (SyncResult, error) {
	plexAccountID := task.Config["plexAccountId"]
	profileID := task.Config["profileId"]

	if plexAccountID == "" || profileID == "" {
		return SyncResult{}, errors.New("missing plexAccountId or profileId in task config")
	}

	// Read sync options with defaults
	syncDirection := task.Config["syncDirection"]
	if syncDirection == "" {
		syncDirection = "source_to_target"
	}
	deleteBehavior := task.Config["deleteBehavior"]
	if deleteBehavior == "" {
		deleteBehavior = "additive"
	}
	conflictResolution := task.Config["conflictResolution"]
	if conflictResolution == "" {
		conflictResolution = "source_wins"
	}
	dryRun := task.Config["dryRun"] == "true"

	if dryRun {
		log.Printf("[scheduler] DRY RUN mode enabled - no changes will be made")
	}

	// Load settings to get Plex account
	settings, err := s.configManager.Load()
	if err != nil {
		return SyncResult{}, fmt.Errorf("load settings: %w", err)
	}

	plexAccount := settings.Plex.GetAccountByID(plexAccountID)
	if plexAccount == nil {
		return SyncResult{}, errors.New("plex account not found")
	}

	if plexAccount.AuthToken == "" {
		return SyncResult{}, errors.New("plex account not authenticated")
	}

	// Build sync source identifier for tracking
	syncSource := fmt.Sprintf("plex:%s:%s", plexAccountID, task.ID)

	switch syncDirection {
	case "source_to_target":
		return s.syncPlexToLocal(plexAccount.AuthToken, profileID, syncSource, deleteBehavior, dryRun)
	case "target_to_source":
		return s.syncLocalToPlex(plexAccount.AuthToken, profileID, syncSource, deleteBehavior, dryRun)
	case "bidirectional":
		return s.syncBidirectional(plexAccount.AuthToken, profileID, syncSource, deleteBehavior, conflictResolution, dryRun)
	default:
		return SyncResult{}, fmt.Errorf("unknown sync direction: %s", syncDirection)
	}
}

// syncPlexToLocal imports items from Plex watchlist to local watchlist
func (s *Service) syncPlexToLocal(authToken, profileID, syncSource, deleteBehavior string, dryRun bool) (SyncResult, error) {
	now := time.Now().UTC()
	result := SyncResult{DryRun: dryRun}

	// Fetch watchlist from Plex
	items, err := s.plexClient.GetWatchlist(authToken)
	if err != nil {
		return result, fmt.Errorf("fetch watchlist: %w", err)
	}

	// Build a set of Plex item keys for deletion checking
	plexItemKeys := make(map[string]bool)

	// Get external IDs for items (no progress callback)
	var externalIDs []map[string]string
	if len(items) > 0 {
		externalIDs = s.plexClient.GetWatchlistDetailsWithProgress(authToken, items, nil)
	}

	// Get existing local items to check what's new
	existingItems, _ := s.watchlistService.List(profileID)
	existingKeys := make(map[string]bool)
	for _, item := range existingItems {
		existingKeys[item.Key()] = true
	}

	// Import to watchlist service
	imported := 0

	for i, item := range items {
		itemID := item.RatingKey
		extIDs := map[string]string{}
		if i < len(externalIDs) && externalIDs[i] != nil {
			extIDs = externalIDs[i]
		}

		// Prefer TMDB ID, then IMDB, then Plex ratingKey
		if tmdbID, ok := extIDs["tmdb"]; ok && tmdbID != "" {
			itemID = tmdbID
		} else if imdbID, ok := extIDs["imdb"]; ok && imdbID != "" {
			itemID = imdbID
		}

		// Add plex ID to external IDs
		extIDs["plex"] = item.RatingKey

		mediaType := plex.NormalizeMediaType(item.Type)
		itemKey := mediaType + ":" + itemID

		// Track this item key for deletion checking
		plexItemKeys[itemKey] = true

		// Check if this is a new item (not already in local)
		isNew := !existingKeys[itemKey]

		if dryRun {
			if isNew {
				log.Printf("[scheduler] DRY RUN: Would import from Plex: %s (%s)", item.Title, mediaType)
				result.ToAdd = append(result.ToAdd, config.DryRunItem{
					Name:      item.Title,
					MediaType: mediaType,
					ID:        itemID,
				})
			}
			imported++
			continue
		}

		input := models.WatchlistUpsert{
			ID:          itemID,
			MediaType:   mediaType,
			Name:        item.Title,
			Year:        item.Year,
			PosterURL:   plex.GetPosterURL(item.Thumb, authToken),
			BackdropURL: plex.GetPosterURL(item.Art, authToken),
			ExternalIDs: extIDs,
			SyncSource:  syncSource,
			SyncedAt:    &now,
		}

		if _, err := s.watchlistService.AddOrUpdate(profileID, input); err != nil {
			log.Printf("[scheduler] Failed to import watchlist item %s: %v", item.Title, err)
			continue
		}

		imported++
	}

	// Handle deletions for delete/mirror modes
	if deleteBehavior != "additive" {
		removed := 0
		localItems, err := s.watchlistService.List(profileID)
		if err != nil {
			log.Printf("[scheduler] Failed to list local items for deletion check: %v", err)
		} else {
			for _, localItem := range localItems {
				localKey := localItem.Key()

				// Check if item still exists in Plex watchlist
				if plexItemKeys[localKey] {
					continue // Item still in Plex, keep it
				}

				// For "delete" mode: only remove items that were synced by this task
				if deleteBehavior == "delete" {
					if localItem.SyncSource != syncSource {
						continue // Not synced by this task, preserve it
					}
				}
				// For "mirror" mode: remove all items not in Plex (regardless of source)

				if dryRun {
					log.Printf("[scheduler] DRY RUN: Would remove from local: %s", localItem.Name)
					result.ToRemove = append(result.ToRemove, config.DryRunItem{
						Name:      localItem.Name,
						MediaType: localItem.MediaType,
						ID:        localItem.ID,
					})
					removed++
					continue
				}

				// Remove from local watchlist
				if ok, err := s.watchlistService.Remove(profileID, localItem.MediaType, localItem.ID); err != nil {
					log.Printf("[scheduler] Failed to remove watchlist item %s: %v", localItem.Name, err)
				} else if ok {
					removed++
					log.Printf("[scheduler] Removed watchlist item no longer in Plex: %s", localItem.Name)
				}
			}
		}

		if removed > 0 {
			log.Printf("[scheduler] Removed %d items no longer in Plex watchlist", removed)
		}
	}

	result.Count = imported
	return result, nil
}

// syncLocalToPlex exports items from local watchlist to Plex watchlist
func (s *Service) syncLocalToPlex(authToken, profileID, syncSource, deleteBehavior string, dryRun bool) (SyncResult, error) {
	result := SyncResult{DryRun: dryRun}

	// Get local watchlist items
	localItems, err := s.watchlistService.List(profileID)
	if err != nil {
		return result, fmt.Errorf("list local items: %w", err)
	}

	// Get current Plex watchlist to check what's already there
	plexItems, err := s.plexClient.GetWatchlist(authToken)
	if err != nil {
		return result, fmt.Errorf("fetch plex watchlist: %w", err)
	}

	// Build set of Plex ratingKeys for quick lookup
	plexRatingKeys := make(map[string]bool)
	for _, item := range plexItems {
		plexRatingKeys[item.RatingKey] = true
	}

	// Build set of local item Plex IDs for deletion checking
	localPlexIDs := make(map[string]bool)

	exported := 0

	for _, localItem := range localItems {
		// Get Plex ratingKey from external IDs
		plexID := ""
		if localItem.ExternalIDs != nil {
			plexID = localItem.ExternalIDs["plex"]
		}

		if plexID == "" {
			log.Printf("[scheduler] Skipping item %s: no Plex ID available", localItem.Name)
			continue
		}

		localPlexIDs[plexID] = true

		// Check if already in Plex
		if plexRatingKeys[plexID] {
			continue // Already in Plex
		}

		if dryRun {
			log.Printf("[scheduler] DRY RUN: Would add to Plex: %s", localItem.Name)
			result.ToAdd = append(result.ToAdd, config.DryRunItem{
				Name:      localItem.Name,
				MediaType: localItem.MediaType,
				ID:        localItem.ID,
			})
			exported++
			continue
		}

		// Add to Plex watchlist
		if err := s.plexClient.AddToWatchlist(authToken, plexID); err != nil {
			log.Printf("[scheduler] Failed to add %s to Plex watchlist: %v", localItem.Name, err)
			continue
		}

		log.Printf("[scheduler] Added to Plex watchlist: %s", localItem.Name)
		exported++
	}

	// Handle deletions from Plex for delete/mirror modes
	if deleteBehavior != "additive" {
		removed := 0
		for _, plexItem := range plexItems {
			// Check if item exists in local watchlist
			if localPlexIDs[plexItem.RatingKey] {
				continue // Item in local, keep in Plex
			}

			// For "delete" mode: we can't reliably track what was synced TO Plex
			// So we skip deletion in delete mode for target_to_source
			if deleteBehavior == "delete" {
				continue
			}

			// For "mirror" mode: remove from Plex if not in local
			if dryRun {
				log.Printf("[scheduler] DRY RUN: Would remove from Plex: %s", plexItem.Title)
				result.ToRemove = append(result.ToRemove, config.DryRunItem{
					Name:      plexItem.Title,
					MediaType: plex.NormalizeMediaType(plexItem.Type),
					ID:        plexItem.RatingKey,
				})
				removed++
				continue
			}

			if err := s.plexClient.RemoveFromWatchlist(authToken, plexItem.RatingKey); err != nil {
				log.Printf("[scheduler] Failed to remove %s from Plex watchlist: %v", plexItem.Title, err)
				continue
			}

			log.Printf("[scheduler] Removed from Plex watchlist: %s", plexItem.Title)
			removed++
		}

		if removed > 0 {
			log.Printf("[scheduler] Removed %d items from Plex watchlist", removed)
		}
	}

	result.Count = exported
	return result, nil
}

// syncBidirectional syncs items in both directions between Plex and local
func (s *Service) syncBidirectional(authToken, profileID, syncSource, deleteBehavior, conflictResolution string, dryRun bool) (SyncResult, error) {
	now := time.Now().UTC()
	result := SyncResult{DryRun: dryRun}

	// Get both watchlists
	plexItems, err := s.plexClient.GetWatchlist(authToken)
	if err != nil {
		return result, fmt.Errorf("fetch plex watchlist: %w", err)
	}

	localItems, err := s.watchlistService.List(profileID)
	if err != nil {
		return result, fmt.Errorf("list local items: %w", err)
	}

	// Get external IDs for Plex items
	var externalIDs []map[string]string
	if len(plexItems) > 0 {
		externalIDs = s.plexClient.GetWatchlistDetailsWithProgress(authToken, plexItems, nil)
	}

	// Build maps for quick lookup
	// plexByKey: mediaType:id -> plex item
	plexByKey := make(map[string]plex.WatchlistItem)
	plexExtIDs := make(map[string]map[string]string)

	for i, item := range plexItems {
		itemID := item.RatingKey
		extIDs := map[string]string{}
		if i < len(externalIDs) && externalIDs[i] != nil {
			extIDs = externalIDs[i]
		}

		if tmdbID, ok := extIDs["tmdb"]; ok && tmdbID != "" {
			itemID = tmdbID
		} else if imdbID, ok := extIDs["imdb"]; ok && imdbID != "" {
			itemID = imdbID
		}

		extIDs["plex"] = item.RatingKey
		mediaType := plex.NormalizeMediaType(item.Type)
		key := mediaType + ":" + itemID

		plexByKey[key] = item
		plexExtIDs[key] = extIDs
	}

	// localByKey: mediaType:id -> local item
	localByKey := make(map[string]models.WatchlistItem)
	for _, item := range localItems {
		localByKey[item.Key()] = item
	}

	synced := 0

	// Step 1: Sync Plex → Local (items in Plex not in local)
	for key, plexItem := range plexByKey {
		if _, exists := localByKey[key]; exists {
			continue // Already in local
		}

		extIDs := plexExtIDs[key]
		itemID := plexItem.RatingKey
		if tmdbID, ok := extIDs["tmdb"]; ok && tmdbID != "" {
			itemID = tmdbID
		} else if imdbID, ok := extIDs["imdb"]; ok && imdbID != "" {
			itemID = imdbID
		}

		mediaType := plex.NormalizeMediaType(plexItem.Type)

		if dryRun {
			log.Printf("[scheduler] DRY RUN: Would import from Plex: %s (%s)", plexItem.Title, mediaType)
			result.ToAdd = append(result.ToAdd, config.DryRunItem{
				Name:      plexItem.Title + " (from Plex)",
				MediaType: mediaType,
				ID:        itemID,
			})
			synced++
			continue
		}

		input := models.WatchlistUpsert{
			ID:          itemID,
			MediaType:   mediaType,
			Name:        plexItem.Title,
			Year:        plexItem.Year,
			PosterURL:   plex.GetPosterURL(plexItem.Thumb, authToken),
			BackdropURL: plex.GetPosterURL(plexItem.Art, authToken),
			ExternalIDs: extIDs,
			SyncSource:  syncSource,
			SyncedAt:    &now,
		}

		if _, err := s.watchlistService.AddOrUpdate(profileID, input); err != nil {
			log.Printf("[scheduler] Failed to import %s from Plex: %v", plexItem.Title, err)
			continue
		}

		log.Printf("[scheduler] Imported from Plex: %s", plexItem.Title)
		synced++
	}

	// Step 2: Sync Local → Plex (items in local not in Plex)
	for key, localItem := range localByKey {
		if _, exists := plexByKey[key]; exists {
			continue // Already in Plex
		}

		// Get Plex ratingKey from external IDs
		plexID := ""
		if localItem.ExternalIDs != nil {
			plexID = localItem.ExternalIDs["plex"]
		}

		if plexID == "" {
			log.Printf("[scheduler] Skipping export of %s: no Plex ID available", localItem.Name)
			continue
		}

		if dryRun {
			log.Printf("[scheduler] DRY RUN: Would export to Plex: %s", localItem.Name)
			result.ToAdd = append(result.ToAdd, config.DryRunItem{
				Name:      localItem.Name + " (to Plex)",
				MediaType: localItem.MediaType,
				ID:        localItem.ID,
			})
			synced++
			continue
		}

		// Add to Plex watchlist
		if err := s.plexClient.AddToWatchlist(authToken, plexID); err != nil {
			log.Printf("[scheduler] Failed to add %s to Plex: %v", localItem.Name, err)
			continue
		}

		log.Printf("[scheduler] Exported to Plex: %s", localItem.Name)
		synced++
	}

	// Step 3: Handle deletions (for delete/mirror modes with bidirectional)
	// In bidirectional mode with delete behavior:
	// - Items removed from Plex should be removed from local (if synced)
	// - Items removed from local should be removed from Plex (if has plex ID)
	// This is tricky because we need to track "what was previously synced"
	// For now, bidirectional + delete/mirror means union of both lists (no deletions)
	// TODO: Implement proper deletion tracking for bidirectional sync

	if deleteBehavior != "additive" {
		log.Printf("[scheduler] Note: delete/mirror behavior in bidirectional mode currently only adds items (deletion tracking not yet implemented)")
	}

	result.Count = synced
	return result, nil
}

// executeTraktListSync syncs a Trakt list to/from a profile
func (s *Service) executeTraktListSync(task config.ScheduledTask) (SyncResult, error) {
	traktAccountID := task.Config["traktAccountId"]
	profileID := task.Config["profileId"]
	listType := task.Config["listType"] // watchlist, collection, favorites, custom

	if traktAccountID == "" || profileID == "" {
		return SyncResult{}, errors.New("missing traktAccountId or profileId in task config")
	}

	if listType == "" {
		listType = "watchlist"
	}

	// Read sync options with defaults
	syncDirection := task.Config["syncDirection"]
	if syncDirection == "" {
		syncDirection = "source_to_target"
	}
	deleteBehavior := task.Config["deleteBehavior"]
	if deleteBehavior == "" {
		deleteBehavior = "additive"
	}
	conflictResolution := task.Config["conflictResolution"]
	if conflictResolution == "" {
		conflictResolution = "source_wins"
	}
	dryRun := task.Config["dryRun"] == "true"

	if dryRun {
		log.Printf("[scheduler] DRY RUN mode enabled - no changes will be made")
	}

	// Load settings to get Trakt account
	settings, err := s.configManager.Load()
	if err != nil {
		return SyncResult{}, fmt.Errorf("load settings: %w", err)
	}

	traktAccount := settings.Trakt.GetAccountByID(traktAccountID)
	if traktAccount == nil {
		return SyncResult{}, errors.New("trakt account not found")
	}

	if traktAccount.AccessToken == "" {
		return SyncResult{}, errors.New("trakt account not authenticated")
	}

	// Update client with account credentials
	s.traktClient.UpdateCredentials(traktAccount.ClientID, traktAccount.ClientSecret)

	// Check token expiry and refresh if needed
	if time.Now().Unix() >= traktAccount.ExpiresAt {
		log.Printf("[scheduler] Trakt token expired, refreshing...")
		tokenResp, err := s.traktClient.RefreshAccessToken(traktAccount.RefreshToken)
		if err != nil {
			return SyncResult{}, fmt.Errorf("refresh trakt token: %w", err)
		}
		traktAccount.AccessToken = tokenResp.AccessToken
		traktAccount.RefreshToken = tokenResp.RefreshToken
		traktAccount.ExpiresAt = tokenResp.CreatedAt + int64(tokenResp.ExpiresIn)
		settings.Trakt.UpdateAccount(*traktAccount)
		if err := s.configManager.Save(settings); err != nil {
			log.Printf("[scheduler] Warning: failed to save refreshed Trakt token: %v", err)
		}
	}

	// Build sync source identifier for tracking
	syncSource := fmt.Sprintf("trakt:%s:%s:%s", traktAccountID, listType, task.ID)

	switch syncDirection {
	case "source_to_target":
		return s.syncTraktToLocal(traktAccount, profileID, listType, task.Config["customListId"], syncSource, deleteBehavior, dryRun)
	case "target_to_source":
		return s.syncLocalToTrakt(traktAccount, profileID, syncSource, deleteBehavior, dryRun)
	case "bidirectional":
		return s.syncTraktBidirectional(traktAccount, profileID, listType, task.Config["customListId"], syncSource, deleteBehavior, conflictResolution, dryRun)
	default:
		return SyncResult{}, fmt.Errorf("unknown sync direction: %s", syncDirection)
	}
}

// TraktListItem is a unified representation of items from different Trakt list types
type TraktListItem struct {
	Title     string
	Year      int
	MediaType string // "movie" or "series"
	IDs       map[string]string
}

// getTraktListItems fetches items from the specified Trakt list type
func (s *Service) getTraktListItems(accessToken string, listType string, customListID string) ([]TraktListItem, error) {
	var items []TraktListItem

	switch listType {
	case "watchlist":
		watchlistItems, err := s.traktClient.GetAllWatchlist(accessToken)
		if err != nil {
			return nil, fmt.Errorf("get trakt watchlist: %w", err)
		}
		for _, item := range watchlistItems {
			if item.Movie != nil {
				items = append(items, TraktListItem{
					Title:     item.Movie.Title,
					Year:      item.Movie.Year,
					MediaType: "movie",
					IDs:       trakt.IDsToMap(item.Movie.IDs),
				})
			} else if item.Show != nil {
				items = append(items, TraktListItem{
					Title:     item.Show.Title,
					Year:      item.Show.Year,
					MediaType: "series",
					IDs:       trakt.IDsToMap(item.Show.IDs),
				})
			}
		}

	case "collection":
		collectionItems, err := s.traktClient.GetAllCollection(accessToken)
		if err != nil {
			return nil, fmt.Errorf("get trakt collection: %w", err)
		}
		for _, item := range collectionItems {
			if item.Movie != nil {
				items = append(items, TraktListItem{
					Title:     item.Movie.Title,
					Year:      item.Movie.Year,
					MediaType: "movie",
					IDs:       trakt.IDsToMap(item.Movie.IDs),
				})
			} else if item.Show != nil {
				items = append(items, TraktListItem{
					Title:     item.Show.Title,
					Year:      item.Show.Year,
					MediaType: "series",
					IDs:       trakt.IDsToMap(item.Show.IDs),
				})
			}
		}

	case "favorites":
		favoriteItems, err := s.traktClient.GetAllFavorites(accessToken)
		if err != nil {
			return nil, fmt.Errorf("get trakt favorites: %w", err)
		}
		for _, item := range favoriteItems {
			if item.Movie != nil {
				items = append(items, TraktListItem{
					Title:     item.Movie.Title,
					Year:      item.Movie.Year,
					MediaType: "movie",
					IDs:       trakt.IDsToMap(item.Movie.IDs),
				})
			} else if item.Show != nil {
				items = append(items, TraktListItem{
					Title:     item.Show.Title,
					Year:      item.Show.Year,
					MediaType: "series",
					IDs:       trakt.IDsToMap(item.Show.IDs),
				})
			}
		}

	case "custom":
		if customListID == "" {
			return nil, errors.New("custom list ID is required for custom list sync")
		}
		listItems, err := s.traktClient.GetAllListItems(accessToken, customListID)
		if err != nil {
			return nil, fmt.Errorf("get trakt custom list: %w", err)
		}
		for _, item := range listItems {
			if item.Movie != nil {
				items = append(items, TraktListItem{
					Title:     item.Movie.Title,
					Year:      item.Movie.Year,
					MediaType: "movie",
					IDs:       trakt.IDsToMap(item.Movie.IDs),
				})
			} else if item.Show != nil {
				items = append(items, TraktListItem{
					Title:     item.Show.Title,
					Year:      item.Show.Year,
					MediaType: "series",
					IDs:       trakt.IDsToMap(item.Show.IDs),
				})
			}
		}

	default:
		return nil, fmt.Errorf("unknown list type: %s", listType)
	}

	return items, nil
}

// syncTraktToLocal imports items from a Trakt list to local watchlist
func (s *Service) syncTraktToLocal(traktAccount *config.TraktAccount, profileID, listType, customListID, syncSource, deleteBehavior string, dryRun bool) (SyncResult, error) {
	now := time.Now().UTC()
	result := SyncResult{DryRun: dryRun}

	// Fetch items from Trakt list
	items, err := s.getTraktListItems(traktAccount.AccessToken, listType, customListID)
	if err != nil {
		return result, err
	}

	// Build a set of Trakt item keys for deletion checking
	traktItemKeys := make(map[string]bool)

	// Get existing local items to check what's new
	existingItems, _ := s.watchlistService.List(profileID)
	existingKeys := make(map[string]bool)
	for _, item := range existingItems {
		existingKeys[item.Key()] = true
	}

	imported := 0

	for _, item := range items {
		// Prefer TMDB ID, then IMDB, then Trakt ID
		itemID := item.IDs["trakt"]
		if tmdbID, ok := item.IDs["tmdb"]; ok && tmdbID != "" {
			itemID = tmdbID
		} else if imdbID, ok := item.IDs["imdb"]; ok && imdbID != "" {
			itemID = imdbID
		}

		itemKey := item.MediaType + ":" + itemID

		// Track this item key for deletion checking
		traktItemKeys[itemKey] = true

		// Check if this is a new item (not already in local)
		isNew := !existingKeys[itemKey]

		if dryRun {
			if isNew {
				log.Printf("[scheduler] DRY RUN: Would import from Trakt %s: %s (%s)", listType, item.Title, item.MediaType)
				result.ToAdd = append(result.ToAdd, config.DryRunItem{
					Name:      item.Title,
					MediaType: item.MediaType,
					ID:        itemID,
				})
			}
			imported++
			continue
		}

		input := models.WatchlistUpsert{
			ID:          itemID,
			MediaType:   item.MediaType,
			Name:        item.Title,
			Year:        item.Year,
			ExternalIDs: item.IDs,
			SyncSource:  syncSource,
			SyncedAt:    &now,
		}

		if _, err := s.watchlistService.AddOrUpdate(profileID, input); err != nil {
			log.Printf("[scheduler] Failed to import Trakt item %s: %v", item.Title, err)
			continue
		}

		imported++
	}

	// Handle deletions for delete/mirror modes
	if deleteBehavior != "additive" {
		removed := 0
		localItems, err := s.watchlistService.List(profileID)
		if err != nil {
			log.Printf("[scheduler] Failed to list local items for deletion check: %v", err)
		} else {
			for _, localItem := range localItems {
				localKey := localItem.Key()

				// Check if item still exists in Trakt list
				if traktItemKeys[localKey] {
					continue // Item still in Trakt, keep it
				}

				// For "delete" mode: only remove items that were synced by this task
				if deleteBehavior == "delete" {
					if localItem.SyncSource != syncSource {
						continue // Not synced by this task, preserve it
					}
				}
				// For "mirror" mode: remove all items not in Trakt (regardless of source)

				if dryRun {
					log.Printf("[scheduler] DRY RUN: Would remove from local: %s", localItem.Name)
					result.ToRemove = append(result.ToRemove, config.DryRunItem{
						Name:      localItem.Name,
						MediaType: localItem.MediaType,
						ID:        localItem.ID,
					})
					removed++
					continue
				}

				// Remove from local watchlist
				if ok, err := s.watchlistService.Remove(profileID, localItem.MediaType, localItem.ID); err != nil {
					log.Printf("[scheduler] Failed to remove watchlist item %s: %v", localItem.Name, err)
				} else if ok {
					removed++
					log.Printf("[scheduler] Removed watchlist item no longer in Trakt %s: %s", listType, localItem.Name)
				}
			}
		}

		if removed > 0 {
			log.Printf("[scheduler] Removed %d items no longer in Trakt %s", removed, listType)
		}
	}

	result.Count = imported
	return result, nil
}

// syncLocalToTrakt exports items from local watchlist to Trakt watchlist
// Note: Trakt only supports adding to watchlist via API, not to collection/favorites/custom lists
func (s *Service) syncLocalToTrakt(traktAccount *config.TraktAccount, profileID, syncSource, deleteBehavior string, dryRun bool) (SyncResult, error) {
	result := SyncResult{DryRun: dryRun}

	// Get local watchlist items
	localItems, err := s.watchlistService.List(profileID)
	if err != nil {
		return result, fmt.Errorf("list local items: %w", err)
	}

	// Get current Trakt watchlist to check what's already there
	traktItems, err := s.traktClient.GetAllWatchlist(traktAccount.AccessToken)
	if err != nil {
		return result, fmt.Errorf("fetch trakt watchlist: %w", err)
	}

	// Build set of Trakt item keys for quick lookup
	traktItemKeys := make(map[string]bool)
	for _, item := range traktItems {
		var key string
		if item.Movie != nil {
			if item.Movie.IDs.TMDB != 0 {
				key = "movie:" + strconv.Itoa(item.Movie.IDs.TMDB)
			} else if item.Movie.IDs.IMDB != "" {
				key = "movie:" + item.Movie.IDs.IMDB
			}
		} else if item.Show != nil {
			if item.Show.IDs.TMDB != 0 {
				key = "series:" + strconv.Itoa(item.Show.IDs.TMDB)
			} else if item.Show.IDs.IMDB != "" {
				key = "series:" + item.Show.IDs.IMDB
			}
		}
		if key != "" {
			traktItemKeys[key] = true
		}
	}

	// Build set of local item keys for deletion checking
	localItemKeys := make(map[string]bool)

	var moviesToAdd []trakt.SyncMovie
	var showsToAdd []trakt.SyncShow
	exported := 0

	for _, localItem := range localItems {
		localKey := localItem.Key()
		localItemKeys[localKey] = true

		// Check if already in Trakt
		if traktItemKeys[localKey] {
			continue // Already in Trakt
		}

		// Get IDs for Trakt API
		var tmdbID int
		var imdbID string
		if localItem.ExternalIDs != nil {
			if tmdbStr, ok := localItem.ExternalIDs["tmdb"]; ok {
				tmdbID, _ = strconv.Atoi(tmdbStr)
			}
			imdbID = localItem.ExternalIDs["imdb"]
		}

		// Fall back to trying to parse the ID as TMDB ID
		if tmdbID == 0 && imdbID == "" {
			tmdbID, _ = strconv.Atoi(localItem.ID)
		}

		if tmdbID == 0 && imdbID == "" {
			log.Printf("[scheduler] Skipping item %s: no valid IDs for Trakt", localItem.Name)
			continue
		}

		if dryRun {
			log.Printf("[scheduler] DRY RUN: Would add to Trakt watchlist: %s", localItem.Name)
			result.ToAdd = append(result.ToAdd, config.DryRunItem{
				Name:      localItem.Name,
				MediaType: localItem.MediaType,
				ID:        localItem.ID,
			})
			exported++
			continue
		}

		if localItem.MediaType == "movie" {
			moviesToAdd = append(moviesToAdd, trakt.SyncMovie{
				IDs: trakt.SyncIDs{
					TMDB: tmdbID,
					IMDB: imdbID,
				},
			})
		} else if localItem.MediaType == "series" {
			showsToAdd = append(showsToAdd, trakt.SyncShow{
				IDs: trakt.SyncIDs{
					TMDB: tmdbID,
					IMDB: imdbID,
				},
			})
		}
		exported++
	}

	// Add items to Trakt
	if !dryRun && (len(moviesToAdd) > 0 || len(showsToAdd) > 0) {
		if err := s.traktClient.AddToWatchlist(traktAccount.AccessToken, moviesToAdd, showsToAdd); err != nil {
			return result, fmt.Errorf("add to trakt watchlist: %w", err)
		}
		log.Printf("[scheduler] Added %d movies and %d shows to Trakt watchlist", len(moviesToAdd), len(showsToAdd))
	}

	// Handle deletions from Trakt for delete/mirror modes
	if deleteBehavior != "additive" {
		var moviesToRemove []trakt.SyncMovie
		var showsToRemove []trakt.SyncShow
		removed := 0

		for _, traktItem := range traktItems {
			var key string
			var movie *trakt.Movie
			var show *trakt.Show

			if traktItem.Movie != nil {
				movie = traktItem.Movie
				if movie.IDs.TMDB != 0 {
					key = "movie:" + strconv.Itoa(movie.IDs.TMDB)
				} else if movie.IDs.IMDB != "" {
					key = "movie:" + movie.IDs.IMDB
				}
			} else if traktItem.Show != nil {
				show = traktItem.Show
				if show.IDs.TMDB != 0 {
					key = "series:" + strconv.Itoa(show.IDs.TMDB)
				} else if show.IDs.IMDB != "" {
					key = "series:" + show.IDs.IMDB
				}
			}

			if key == "" {
				continue
			}

			// Check if item exists in local watchlist
			if localItemKeys[key] {
				continue // Item in local, keep in Trakt
			}

			// For "delete" mode: we can't reliably track what was synced TO Trakt
			// So we skip deletion in delete mode for target_to_source
			if deleteBehavior == "delete" {
				continue
			}

			// For "mirror" mode: remove from Trakt if not in local
			if dryRun {
				name := ""
				if movie != nil {
					name = movie.Title
				} else if show != nil {
					name = show.Title
				}
				log.Printf("[scheduler] DRY RUN: Would remove from Trakt watchlist: %s", name)
				result.ToRemove = append(result.ToRemove, config.DryRunItem{
					Name:      name,
					MediaType: key[:strings.Index(key, ":")],
					ID:        key[strings.Index(key, ":")+1:],
				})
				removed++
				continue
			}

			if movie != nil {
				moviesToRemove = append(moviesToRemove, trakt.SyncMovie{
					IDs: trakt.SyncIDs{
						TMDB: movie.IDs.TMDB,
						IMDB: movie.IDs.IMDB,
					},
				})
				removed++
				log.Printf("[scheduler] Will remove from Trakt watchlist: %s", movie.Title)
			} else if show != nil {
				showsToRemove = append(showsToRemove, trakt.SyncShow{
					IDs: trakt.SyncIDs{
						TMDB: show.IDs.TMDB,
						IMDB: show.IDs.IMDB,
					},
				})
				removed++
				log.Printf("[scheduler] Will remove from Trakt watchlist: %s", show.Title)
			}
		}

		if !dryRun && (len(moviesToRemove) > 0 || len(showsToRemove) > 0) {
			if err := s.traktClient.RemoveFromWatchlist(traktAccount.AccessToken, moviesToRemove, showsToRemove); err != nil {
				log.Printf("[scheduler] Failed to remove items from Trakt watchlist: %v", err)
			} else {
				log.Printf("[scheduler] Removed %d items from Trakt watchlist", removed)
			}
		}
	}

	result.Count = exported
	return result, nil
}

// syncTraktBidirectional syncs items in both directions between Trakt and local
func (s *Service) syncTraktBidirectional(traktAccount *config.TraktAccount, profileID, listType, customListID, syncSource, deleteBehavior, conflictResolution string, dryRun bool) (SyncResult, error) {
	now := time.Now().UTC()
	result := SyncResult{DryRun: dryRun}

	// Get both lists
	traktItems, err := s.getTraktListItems(traktAccount.AccessToken, listType, customListID)
	if err != nil {
		return result, err
	}

	localItems, err := s.watchlistService.List(profileID)
	if err != nil {
		return result, fmt.Errorf("list local items: %w", err)
	}

	// Build maps for quick lookup
	traktByKey := make(map[string]TraktListItem)
	for _, item := range traktItems {
		itemID := item.IDs["trakt"]
		if tmdbID, ok := item.IDs["tmdb"]; ok && tmdbID != "" {
			itemID = tmdbID
		} else if imdbID, ok := item.IDs["imdb"]; ok && imdbID != "" {
			itemID = imdbID
		}
		key := item.MediaType + ":" + itemID
		traktByKey[key] = item
	}

	localByKey := make(map[string]models.WatchlistItem)
	for _, item := range localItems {
		localByKey[item.Key()] = item
	}

	synced := 0

	// Step 1: Sync Trakt → Local (items in Trakt not in local)
	for key, traktItem := range traktByKey {
		if _, exists := localByKey[key]; exists {
			continue // Already in local
		}

		itemID := traktItem.IDs["trakt"]
		if tmdbID, ok := traktItem.IDs["tmdb"]; ok && tmdbID != "" {
			itemID = tmdbID
		} else if imdbID, ok := traktItem.IDs["imdb"]; ok && imdbID != "" {
			itemID = imdbID
		}

		if dryRun {
			log.Printf("[scheduler] DRY RUN: Would import from Trakt: %s (%s)", traktItem.Title, traktItem.MediaType)
			result.ToAdd = append(result.ToAdd, config.DryRunItem{
				Name:      traktItem.Title + " (from Trakt)",
				MediaType: traktItem.MediaType,
				ID:        itemID,
			})
			synced++
			continue
		}

		input := models.WatchlistUpsert{
			ID:          itemID,
			MediaType:   traktItem.MediaType,
			Name:        traktItem.Title,
			Year:        traktItem.Year,
			ExternalIDs: traktItem.IDs,
			SyncSource:  syncSource,
			SyncedAt:    &now,
		}

		if _, err := s.watchlistService.AddOrUpdate(profileID, input); err != nil {
			log.Printf("[scheduler] Failed to import %s from Trakt: %v", traktItem.Title, err)
			continue
		}

		log.Printf("[scheduler] Imported from Trakt: %s", traktItem.Title)
		synced++
	}

	// Step 2: Sync Local → Trakt (items in local not in Trakt)
	// Note: Only supported for watchlist list type
	if listType == "watchlist" {
		var moviesToAdd []trakt.SyncMovie
		var showsToAdd []trakt.SyncShow

		for key, localItem := range localByKey {
			if _, exists := traktByKey[key]; exists {
				continue // Already in Trakt
			}

			// Get IDs for Trakt API
			var tmdbID int
			var imdbID string
			if localItem.ExternalIDs != nil {
				if tmdbStr, ok := localItem.ExternalIDs["tmdb"]; ok {
					tmdbID, _ = strconv.Atoi(tmdbStr)
				}
				imdbID = localItem.ExternalIDs["imdb"]
			}

			if tmdbID == 0 && imdbID == "" {
				tmdbID, _ = strconv.Atoi(localItem.ID)
			}

			if tmdbID == 0 && imdbID == "" {
				log.Printf("[scheduler] Skipping export of %s: no valid IDs for Trakt", localItem.Name)
				continue
			}

			if dryRun {
				log.Printf("[scheduler] DRY RUN: Would export to Trakt: %s", localItem.Name)
				result.ToAdd = append(result.ToAdd, config.DryRunItem{
					Name:      localItem.Name + " (to Trakt)",
					MediaType: localItem.MediaType,
					ID:        localItem.ID,
				})
				synced++
				continue
			}

			if localItem.MediaType == "movie" {
				moviesToAdd = append(moviesToAdd, trakt.SyncMovie{
					IDs: trakt.SyncIDs{
						TMDB: tmdbID,
						IMDB: imdbID,
					},
				})
			} else if localItem.MediaType == "series" {
				showsToAdd = append(showsToAdd, trakt.SyncShow{
					IDs: trakt.SyncIDs{
						TMDB: tmdbID,
						IMDB: imdbID,
					},
				})
			}

			log.Printf("[scheduler] Will export to Trakt: %s", localItem.Name)
			synced++
		}

		if !dryRun && (len(moviesToAdd) > 0 || len(showsToAdd) > 0) {
			if err := s.traktClient.AddToWatchlist(traktAccount.AccessToken, moviesToAdd, showsToAdd); err != nil {
				log.Printf("[scheduler] Failed to add items to Trakt watchlist: %v", err)
			} else {
				log.Printf("[scheduler] Exported %d movies and %d shows to Trakt watchlist", len(moviesToAdd), len(showsToAdd))
			}
		}
	} else {
		log.Printf("[scheduler] Note: Bidirectional sync to Trakt only supported for watchlist type (current: %s)", listType)
	}

	if deleteBehavior != "additive" {
		log.Printf("[scheduler] Note: delete/mirror behavior in bidirectional mode currently only adds items (deletion tracking not yet implemented)")
	}

	result.Count = synced
	return result, nil
}

// executeEPGRefresh refreshes the EPG data from all configured sources.
func (s *Service) executeEPGRefresh(task config.ScheduledTask) (SyncResult, error) {
	s.mu.RLock()
	epgSvc := s.epgService
	s.mu.RUnlock()

	if epgSvc == nil {
		return SyncResult{}, errors.New("EPG service not configured")
	}

	// Create a context with timeout for the refresh
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := epgSvc.Refresh(ctx); err != nil {
		return SyncResult{}, fmt.Errorf("EPG refresh failed: %w", err)
	}

	// Get the status to report the counts
	status := epgSvc.GetStatus()

	return SyncResult{
		Count: status.ProgramCount,
	}, nil
}

// executePlaylistRefresh clears the cached Live TV playlist to force a fresh fetch.
func (s *Service) executePlaylistRefresh(task config.ScheduledTask) (SyncResult, error) {
	cacheDir := "cache/live"

	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			// Cache directory doesn't exist, nothing to clear
			log.Printf("[scheduler] playlist cache directory doesn't exist, nothing to clear")
			return SyncResult{Count: 0}, nil
		}
		return SyncResult{}, fmt.Errorf("failed to read cache directory: %w", err)
	}

	cleared := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Only remove .m3u and .meta files
		if strings.HasSuffix(name, ".m3u") || strings.HasSuffix(name, ".meta") {
			path := cacheDir + "/" + name
			if err := os.Remove(path); err != nil {
				log.Printf("[scheduler] failed to remove cache file %s: %v", name, err)
			} else {
				cleared++
			}
		}
	}

	log.Printf("[scheduler] cleared %d cached playlist files", cleared)
	return SyncResult{Count: cleared}, nil
}

// executeBackup creates a system backup and runs cleanup based on retention settings.
func (s *Service) executeBackup(task config.ScheduledTask) (SyncResult, error) {
	s.mu.RLock()
	backupSvc := s.backupService
	s.mu.RUnlock()

	if backupSvc == nil {
		return SyncResult{}, errors.New("backup service not configured")
	}

	// Update global retention settings from task config if present
	if task.Config != nil {
		retDaysStr := task.Config["retentionDays"]
		retCountStr := task.Config["retentionCount"]
		if retDaysStr != "" || retCountStr != "" {
			settings, err := s.configManager.Load()
			if err == nil {
				days, _ := strconv.Atoi(retDaysStr)
				count, _ := strconv.Atoi(retCountStr)
				if days > 0 || count > 0 {
					settings.BackupRetention.RetentionDays = days
					settings.BackupRetention.RetentionCount = count
					s.configManager.Save(settings)
				}
			}
		}
	}

	// Create backup
	info, err := backupSvc.CreateBackup(backup.BackupTypeScheduled)
	if err != nil {
		return SyncResult{}, fmt.Errorf("create backup: %w", err)
	}

	// Run cleanup based on retention settings
	cleaned, cleanErr := backupSvc.CleanupOldBackups()
	if cleanErr != nil {
		log.Printf("[scheduler] Warning: backup cleanup failed: %v", cleanErr)
	}

	msg := fmt.Sprintf("Backup created: %s", info.Filename)
	if cleaned > 0 {
		msg += fmt.Sprintf(", cleaned %d old backups", cleaned)
	}

	log.Printf("[scheduler] %s", msg)
	return SyncResult{Count: 1, Message: msg}, nil
}

// executeTraktHistorySync syncs watch history between Trakt and local
func (s *Service) executeTraktHistorySync(task config.ScheduledTask) (SyncResult, error) {
	s.mu.RLock()
	historySvc := s.historyService
	s.mu.RUnlock()

	if historySvc == nil {
		return SyncResult{}, errors.New("history service not configured")
	}

	traktAccountID := task.Config["traktAccountId"]
	profileID := task.Config["profileId"]

	if traktAccountID == "" || profileID == "" {
		return SyncResult{}, errors.New("missing traktAccountId or profileId in task config")
	}

	syncDirection := task.Config["syncDirection"]
	if syncDirection == "" {
		syncDirection = "trakt_to_local"
	}
	dryRun := task.Config["dryRun"] == "true"

	if dryRun {
		log.Printf("[scheduler] DRY RUN mode enabled for trakt history sync")
	}

	// Load settings to get Trakt account
	settings, err := s.configManager.Load()
	if err != nil {
		return SyncResult{}, fmt.Errorf("load settings: %w", err)
	}

	traktAccount := settings.Trakt.GetAccountByID(traktAccountID)
	if traktAccount == nil {
		return SyncResult{}, errors.New("trakt account not found")
	}

	if traktAccount.AccessToken == "" {
		return SyncResult{}, errors.New("trakt account not authenticated")
	}

	// Update client with account credentials
	s.traktClient.UpdateCredentials(traktAccount.ClientID, traktAccount.ClientSecret)

	// Check token expiry and refresh if needed
	if time.Now().Unix() >= traktAccount.ExpiresAt {
		log.Printf("[scheduler] Trakt token expired, refreshing...")
		tokenResp, err := s.traktClient.RefreshAccessToken(traktAccount.RefreshToken)
		if err != nil {
			return SyncResult{}, fmt.Errorf("refresh trakt token: %w", err)
		}
		traktAccount.AccessToken = tokenResp.AccessToken
		traktAccount.RefreshToken = tokenResp.RefreshToken
		traktAccount.ExpiresAt = tokenResp.CreatedAt + int64(tokenResp.ExpiresIn)
		settings.Trakt.UpdateAccount(*traktAccount)
		if err := s.configManager.Save(settings); err != nil {
			log.Printf("[scheduler] Warning: failed to save refreshed Trakt token: %v", err)
		}
	}

	switch syncDirection {
	case "trakt_to_local":
		return s.syncTraktHistoryToLocal(task, traktAccount, profileID, dryRun)
	case "local_to_trakt":
		return s.syncLocalHistoryToTrakt(task, traktAccount, profileID, dryRun)
	case "bidirectional":
		return s.syncHistoryBidirectional(task, traktAccount, profileID, dryRun)
	default:
		return SyncResult{}, fmt.Errorf("unknown sync direction: %s", syncDirection)
	}
}

// syncTraktHistoryToLocal imports watch history from Trakt into local
func (s *Service) syncTraktHistoryToLocal(task config.ScheduledTask, traktAccount *config.TraktAccount, profileID string, dryRun bool) (SyncResult, error) {
	result := SyncResult{DryRun: dryRun}

	s.mu.RLock()
	historySvc := s.historyService
	s.mu.RUnlock()

	// Determine incremental cursor: lastRunAt - 5min safety buffer
	var since time.Time
	if task.LastRunAt != nil {
		since = task.LastRunAt.Add(-5 * time.Minute)
	}

	log.Printf("[scheduler] Fetching Trakt watch history since=%v", since)

	items, err := s.traktClient.GetWatchHistorySince(traktAccount.AccessToken, since)
	if err != nil {
		return result, fmt.Errorf("fetch trakt history: %w", err)
	}

	log.Printf("[scheduler] Fetched %d Trakt history items", len(items))

	// Deduplicate Trakt history items by mediaType:itemID, keeping only the most
	// recent watch per item. Trakt returns items in reverse chronological order
	// (newest first), so the first occurrence of each key is the most recent.
	watched := true
	seen := make(map[string]bool)
	var updates []models.WatchHistoryUpdate

	for _, item := range items {
		update := s.traktHistoryItemToUpdate(item, &watched)
		if update == nil {
			continue
		}

		key := strings.ToLower(update.MediaType) + ":" + strings.ToLower(update.ItemID)
		if seen[key] {
			continue
		}
		seen[key] = true

		if dryRun {
			name := ""
			if item.Movie != nil {
				name = item.Movie.Title
			} else if item.Show != nil && item.Episode != nil {
				name = fmt.Sprintf("%s S%02dE%02d", item.Show.Title, item.Episode.Season, item.Episode.Number)
			}
			result.ToAdd = append(result.ToAdd, config.DryRunItem{
				Name:      name,
				MediaType: update.MediaType,
				ID:        update.ItemID,
			})
			continue
		}

		updates = append(updates, *update)
	}

	log.Printf("[scheduler] %d unique items after dedup (from %d Trakt history entries)", len(seen), len(items))

	if dryRun {
		result.Count = len(result.ToAdd)
		return result, nil
	}

	if len(updates) > 0 {
		imported, err := historySvc.ImportWatchHistory(profileID, updates)
		if err != nil {
			return result, fmt.Errorf("import watch history: %w", err)
		}
		result.Count = imported
		log.Printf("[scheduler] Imported %d/%d unique items from Trakt history", imported, len(updates))
	}

	return result, nil
}

// syncLocalHistoryToTrakt exports local watch history to Trakt
func (s *Service) syncLocalHistoryToTrakt(task config.ScheduledTask, traktAccount *config.TraktAccount, profileID string, dryRun bool) (SyncResult, error) {
	result := SyncResult{DryRun: dryRun}

	s.mu.RLock()
	historySvc := s.historyService
	s.mu.RUnlock()

	// Get all local watch history for this profile
	items, err := historySvc.ListWatchHistory(profileID)
	if err != nil {
		return result, fmt.Errorf("list local history: %w", err)
	}

	// Filter to items watched since last run (with 5min safety buffer)
	var since time.Time
	if task.LastRunAt != nil {
		since = task.LastRunAt.Add(-5 * time.Minute)
	}

	// Fetch existing Trakt history to avoid creating duplicate watch events.
	// Trakt's AddToHistory creates a NEW event each call — it is not idempotent.
	traktItems, err := s.traktClient.GetWatchHistorySince(traktAccount.AccessToken, since)
	if err != nil {
		return result, fmt.Errorf("fetch trakt history for dedup: %w", err)
	}

	// Build a set of items already on Trakt, keyed by mediaType:itemID
	watched := true
	alreadyOnTrakt := make(map[string]bool)
	for _, ti := range traktItems {
		update := s.traktHistoryItemToUpdate(ti, &watched)
		if update != nil {
			key := strings.ToLower(update.MediaType) + ":" + strings.ToLower(update.ItemID)
			alreadyOnTrakt[key] = true
		}
	}
	log.Printf("[scheduler] Found %d unique items already on Trakt history", len(alreadyOnTrakt))

	// Group episodes by show for batch API call
	type showKey struct {
		tvdbID int
	}
	showEpisodes := make(map[showKey][]trakt.SyncEpisode)
	showIDs := make(map[showKey]trakt.SyncIDs)
	var movies []trakt.SyncMovie
	exported := 0
	skipped := 0

	for _, item := range items {
		if !item.Watched {
			continue
		}

		// Skip items before the incremental cursor
		if !since.IsZero() && item.WatchedAt.Before(since) {
			continue
		}

		// Skip items already on Trakt to avoid duplicate watch events
		localKey := strings.ToLower(item.MediaType) + ":" + strings.ToLower(item.ItemID)
		if alreadyOnTrakt[localKey] {
			skipped++
			continue
		}

		if item.MediaType == "movie" {
			var tmdbID, tvdbID int
			var imdbID string
			if item.ExternalIDs != nil {
				if id, ok := item.ExternalIDs["tmdb"]; ok {
					tmdbID, _ = strconv.Atoi(id)
				}
				if id, ok := item.ExternalIDs["tvdb"]; ok {
					tvdbID, _ = strconv.Atoi(id)
				}
				if id, ok := item.ExternalIDs["imdb"]; ok {
					imdbID = id
				}
			}
			if tmdbID == 0 && tvdbID == 0 && imdbID == "" {
				continue
			}

			if dryRun {
				result.ToAdd = append(result.ToAdd, config.DryRunItem{
					Name:      item.Name,
					MediaType: "movie",
					ID:        item.ItemID,
				})
				exported++
				continue
			}

			movies = append(movies, trakt.SyncMovie{
				WatchedAt: item.WatchedAt.UTC().Format(time.RFC3339),
				IDs: trakt.SyncIDs{
					TMDB: tmdbID,
					TVDB: tvdbID,
					IMDB: imdbID,
				},
			})
			exported++
		} else if item.MediaType == "episode" {
			var tvdbID int
			if item.ExternalIDs != nil {
				if id, ok := item.ExternalIDs["tvdb"]; ok {
					tvdbID, _ = strconv.Atoi(id)
				}
			}
			if tvdbID == 0 || item.SeasonNumber == 0 || item.EpisodeNumber == 0 {
				continue
			}

			if dryRun {
				result.ToAdd = append(result.ToAdd, config.DryRunItem{
					Name:      fmt.Sprintf("%s S%02dE%02d", item.SeriesName, item.SeasonNumber, item.EpisodeNumber),
					MediaType: "episode",
					ID:        item.ItemID,
				})
				exported++
				continue
			}

			sk := showKey{tvdbID: tvdbID}
			showEpisodes[sk] = append(showEpisodes[sk], trakt.SyncEpisode{
				Number:    item.EpisodeNumber,
				WatchedAt: item.WatchedAt.UTC().Format(time.RFC3339),
			})
			if _, exists := showIDs[sk]; !exists {
				showIDs[sk] = trakt.SyncIDs{TVDB: tvdbID}
			}
			exported++
		}
	}

	log.Printf("[scheduler] Local→Trakt: %d to export, %d skipped (already on Trakt)", exported, skipped)

	if dryRun {
		result.Count = exported
		return result, nil
	}

	// Build SyncShow batch structure
	var shows []trakt.SyncShow
	for sk, episodes := range showEpisodes {
		// Group episodes by season
		seasonEps := make(map[int][]trakt.SyncEpisode)
		for _, ep := range episodes {
			// Find the season number from the local items
			for _, item := range items {
				if item.MediaType == "episode" && item.EpisodeNumber == ep.Number {
					if item.ExternalIDs != nil {
						if id, ok := item.ExternalIDs["tvdb"]; ok {
							tvdbID, _ := strconv.Atoi(id)
							if tvdbID == sk.tvdbID {
								seasonEps[item.SeasonNumber] = append(seasonEps[item.SeasonNumber], ep)
								break
							}
						}
					}
				}
			}
		}

		var seasons []trakt.SyncSeason
		for seasonNum, eps := range seasonEps {
			seasons = append(seasons, trakt.SyncSeason{
				Number:   seasonNum,
				Episodes: eps,
			})
		}

		shows = append(shows, trakt.SyncShow{
			IDs:     showIDs[sk],
			Seasons: seasons,
		})
	}

	if len(movies) > 0 || len(shows) > 0 {
		syncReq := trakt.SyncHistoryRequest{
			Movies: movies,
			Shows:  shows,
		}
		resp, err := s.traktClient.AddToHistory(traktAccount.AccessToken, syncReq)
		if err != nil {
			return result, fmt.Errorf("add to trakt history: %w", err)
		}
		log.Printf("[scheduler] Synced to Trakt: %d movies, %d episodes added", resp.Added.Movies, resp.Added.Episodes)
	}

	result.Count = exported
	return result, nil
}

// syncHistoryBidirectional syncs watch history in both directions
func (s *Service) syncHistoryBidirectional(task config.ScheduledTask, traktAccount *config.TraktAccount, profileID string, dryRun bool) (SyncResult, error) {
	// Run Trakt → Local first
	toLocalResult, err := s.syncTraktHistoryToLocal(task, traktAccount, profileID, dryRun)
	if err != nil {
		return toLocalResult, fmt.Errorf("trakt to local: %w", err)
	}

	// Then Local → Trakt
	toTraktResult, err := s.syncLocalHistoryToTrakt(task, traktAccount, profileID, dryRun)
	if err != nil {
		return toTraktResult, fmt.Errorf("local to trakt: %w", err)
	}

	// Sync partial playback positions (non-fatal errors)
	if !dryRun {
		if err := s.syncPlaybackToTrakt(traktAccount, profileID); err != nil {
			log.Printf("[scheduler] Warning: sync playback to Trakt failed: %v", err)
		}
		if err := s.syncPlaybackFromTrakt(traktAccount, profileID); err != nil {
			log.Printf("[scheduler] Warning: sync playback from Trakt failed: %v", err)
		}
	}

	// Combine results
	combined := SyncResult{
		Count:  toLocalResult.Count + toTraktResult.Count,
		DryRun: dryRun,
		ToAdd:  append(toLocalResult.ToAdd, toTraktResult.ToAdd...),
	}
	return combined, nil
}

// syncPlaybackToTrakt exports partial playback progress (1-80%) to Trakt via scrobble/pause.
// Trakt requires scrobble/stop for progress above ~80%, so we cap at 80% here.
// Items at 90%+ are already handled by the existing watch history sync.
func (s *Service) syncPlaybackToTrakt(traktAccount *config.TraktAccount, profileID string) error {
	if s.historyService == nil {
		return nil
	}

	items, err := s.historyService.ListPlaybackProgress(profileID)
	if err != nil {
		return fmt.Errorf("list playback progress: %w", err)
	}

	s.traktClient.UpdateCredentials(traktAccount.ClientID, traktAccount.ClientSecret)
	accessToken := traktAccount.AccessToken

	exported := 0
	for _, item := range items {
		// Only sync items with meaningful partial progress
		// Trakt requires scrobble/stop above ~80%, so cap at 80% for pause-based sync
		if item.PercentWatched <= 1 || item.PercentWatched >= 80 {
			continue
		}

		// Need valid external IDs to match on Trakt
		if len(item.ExternalIDs) == 0 {
			continue
		}

		req := trakt.ScrobbleRequest{
			Progress: item.PercentWatched,
		}

		if item.MediaType == "movie" {
			req.Movie = &trakt.ScrobbleMovie{
				Title: item.MovieName,
				Year:  item.Year,
				IDs:   externalIDsToSyncIDs(item.ExternalIDs),
			}
		} else if item.MediaType == "episode" {
			req.Episode = &trakt.ScrobbleEpisode{
				Season: item.SeasonNumber,
				Number: item.EpisodeNumber,
				Title:  item.EpisodeName,
			}
			req.Show = &trakt.ScrobbleShow{
				Title: item.SeriesName,
				IDs:   seriesIDToSyncIDs(item.SeriesID, item.ExternalIDs),
			}
		} else {
			continue
		}

		// ScrobblePause saves the position on Trakt without triggering "now watching"
		if _, err := s.traktClient.ScrobblePause(accessToken, req); err != nil {
			if errors.Is(err, trakt.ErrNotFound) {
				continue
			}
			log.Printf("[scheduler] Failed to sync playback for %s %s: %v", item.MediaType, item.ItemID, err)
			continue
		}
		exported++
	}

	if exported > 0 {
		log.Printf("[scheduler] Exported %d partial playback positions to Trakt", exported)
	}
	return nil
}

// syncPlaybackFromTrakt imports partial playback progress from Trakt to local storage.
func (s *Service) syncPlaybackFromTrakt(traktAccount *config.TraktAccount, profileID string) error {
	if s.historyService == nil {
		return nil
	}

	s.traktClient.UpdateCredentials(traktAccount.ClientID, traktAccount.ClientSecret)
	accessToken := traktAccount.AccessToken

	imported := 0

	for _, mediaType := range []string{"movies", "episodes"} {
		traktItems, err := s.traktClient.GetPlaybackProgress(accessToken, mediaType)
		if err != nil {
			log.Printf("[scheduler] Failed to get Trakt playback progress for %s: %v", mediaType, err)
			continue
		}

		for _, traktItem := range traktItems {
			update := s.traktPlaybackItemToUpdate(traktItem)
			if update == nil {
				continue
			}

			// Check if local progress exists and is newer
			localProgress, err := s.historyService.GetPlaybackProgress(profileID, update.MediaType, update.ItemID)
			if err == nil && localProgress != nil {
				// If local progress is newer (or same), skip
				if !localProgress.UpdatedAt.Before(traktItem.PausedAt) {
					continue
				}
			}

			// Import from Trakt — need local duration to compute a meaningful position
			if localProgress == nil || localProgress.Duration <= 0 {
				// No local duration reference — skip (will import on next sync if duration becomes available)
				continue
			}

			update.Duration = localProgress.Duration
			update.Position = (traktItem.Progress / 100) * localProgress.Duration
			update.IsPaused = true

			if _, err := s.historyService.UpdatePlaybackProgress(profileID, *update); err != nil {
				log.Printf("[scheduler] Failed to import Trakt playback for %s: %v", update.ItemID, err)
				continue
			}
			imported++
		}
	}

	if imported > 0 {
		log.Printf("[scheduler] Imported %d playback positions from Trakt", imported)
	}
	return nil
}

// traktPlaybackItemToUpdate converts a Trakt PlaybackItem to a PlaybackProgressUpdate.
func (s *Service) traktPlaybackItemToUpdate(item trakt.PlaybackItem) *models.PlaybackProgressUpdate {
	update := &models.PlaybackProgressUpdate{
		Timestamp: item.PausedAt,
		IsPaused:  true,
	}

	if item.Type == "movie" && item.Movie != nil {
		ids := trakt.IDsToMap(item.Movie.IDs)
		var itemID string
		if tmdbID, ok := ids["tmdb"]; ok && tmdbID != "" {
			itemID = tmdbID
		} else if imdbID, ok := ids["imdb"]; ok && imdbID != "" {
			itemID = imdbID
		} else if tvdbID, ok := ids["tvdb"]; ok && tvdbID != "" {
			itemID = tvdbID
		}
		if itemID == "" {
			return nil
		}

		update.MediaType = "movie"
		update.ItemID = itemID
		update.MovieName = item.Movie.Title
		update.Year = item.Movie.Year
		update.ExternalIDs = ids
	} else if item.Type == "episode" && item.Episode != nil && item.Show != nil {
		showIDs := trakt.IDsToMap(item.Show.IDs)
		var seriesID, itemID string
		if tvdbID, ok := showIDs["tvdb"]; ok && tvdbID != "" {
			seriesID = fmt.Sprintf("tvdb:series:%s", tvdbID)
			itemID = fmt.Sprintf("tvdb:series:%s:s%02de%02d", tvdbID, item.Episode.Season, item.Episode.Number)
		} else if tmdbID, ok := showIDs["tmdb"]; ok && tmdbID != "" {
			seriesID = fmt.Sprintf("tmdb:tv:%s", tmdbID)
			itemID = fmt.Sprintf("tmdb:tv:%s:s%02de%02d", tmdbID, item.Episode.Season, item.Episode.Number)
		} else if imdbID, ok := showIDs["imdb"]; ok && imdbID != "" {
			seriesID = fmt.Sprintf("imdb:%s", imdbID)
			itemID = fmt.Sprintf("imdb:%s:s%02de%02d", imdbID, item.Episode.Season, item.Episode.Number)
		}
		if itemID == "" {
			return nil
		}

		update.MediaType = "episode"
		update.ItemID = itemID
		update.EpisodeName = item.Episode.Title
		update.ExternalIDs = showIDs
		update.SeriesID = seriesID
		update.SeriesName = item.Show.Title
		update.SeasonNumber = item.Episode.Season
		update.EpisodeNumber = item.Episode.Number
	} else {
		return nil
	}

	return update
}

// externalIDsToSyncIDs converts map[string]string external IDs to trakt.SyncIDs.
func externalIDsToSyncIDs(extIDs map[string]string) trakt.SyncIDs {
	ids := trakt.SyncIDs{}
	if v, ok := extIDs["tmdb"]; ok {
		ids.TMDB, _ = strconv.Atoi(v)
	}
	if v, ok := extIDs["imdb"]; ok {
		ids.IMDB = v
	}
	if v, ok := extIDs["tvdb"]; ok {
		ids.TVDB, _ = strconv.Atoi(v)
	}
	return ids
}

// seriesIDToSyncIDs extracts show IDs from seriesID and external IDs for Trakt.
func seriesIDToSyncIDs(seriesID string, extIDs map[string]string) trakt.SyncIDs {
	ids := trakt.SyncIDs{}
	if v, ok := extIDs["tvdb"]; ok {
		ids.TVDB, _ = strconv.Atoi(v)
	}
	if v, ok := extIDs["tmdb"]; ok {
		ids.TMDB, _ = strconv.Atoi(v)
	}
	if v, ok := extIDs["imdb"]; ok {
		ids.IMDB = v
	}

	if ids.TVDB == 0 && ids.TMDB == 0 && ids.IMDB == "" && seriesID != "" {
		parts := strings.Split(seriesID, ":")
		if len(parts) >= 3 {
			provider := strings.ToLower(parts[0])
			numericID := parts[len(parts)-1]
			switch provider {
			case "tvdb":
				ids.TVDB, _ = strconv.Atoi(numericID)
			case "tmdb":
				ids.TMDB, _ = strconv.Atoi(numericID)
			case "imdb":
				ids.IMDB = fmt.Sprintf("tt%s", numericID)
			}
		}
	}

	return ids
}

// traktHistoryItemToUpdate converts a Trakt HistoryItem to a WatchHistoryUpdate.
// Returns nil if the item can't be mapped (missing IDs).
func (s *Service) traktHistoryItemToUpdate(item trakt.HistoryItem, watched *bool) *models.WatchHistoryUpdate {
	update := &models.WatchHistoryUpdate{
		Watched:   watched,
		WatchedAt: item.WatchedAt,
	}

	if item.Type == "movie" && item.Movie != nil {
		ids := trakt.IDsToMap(item.Movie.IDs)

		// Prefer TMDB > IMDB > TVDB as itemID
		var itemID string
		if tmdbID, ok := ids["tmdb"]; ok && tmdbID != "" {
			itemID = tmdbID
		} else if imdbID, ok := ids["imdb"]; ok && imdbID != "" {
			itemID = imdbID
		} else if tvdbID, ok := ids["tvdb"]; ok && tvdbID != "" {
			itemID = tvdbID
		}
		if itemID == "" {
			return nil
		}

		update.MediaType = "movie"
		update.ItemID = itemID
		update.Name = item.Movie.Title
		update.Year = item.Movie.Year
		update.ExternalIDs = ids
	} else if item.Type == "episode" && item.Episode != nil && item.Show != nil {
		showIDs := trakt.IDsToMap(item.Show.IDs)

		// Build episode-specific composite ID: tvdb:series:SHOWID:s01e02
		// Prefer TVDB to match the player/details page ID format
		var seriesID, itemID string
		if tvdbID, ok := showIDs["tvdb"]; ok && tvdbID != "" {
			seriesID = fmt.Sprintf("tvdb:series:%s", tvdbID)
			itemID = fmt.Sprintf("tvdb:series:%s:s%02de%02d", tvdbID, item.Episode.Season, item.Episode.Number)
		} else if tmdbID, ok := showIDs["tmdb"]; ok && tmdbID != "" {
			seriesID = fmt.Sprintf("tmdb:tv:%s", tmdbID)
			itemID = fmt.Sprintf("tmdb:tv:%s:s%02de%02d", tmdbID, item.Episode.Season, item.Episode.Number)
		} else if imdbID, ok := showIDs["imdb"]; ok && imdbID != "" {
			seriesID = fmt.Sprintf("imdb:%s", imdbID)
			itemID = fmt.Sprintf("imdb:%s:s%02de%02d", imdbID, item.Episode.Season, item.Episode.Number)
		}
		if itemID == "" {
			return nil
		}

		update.MediaType = "episode"
		update.ItemID = itemID
		update.Name = item.Episode.Title
		update.Year = item.Show.Year
		update.ExternalIDs = showIDs
		update.SeriesID = seriesID
		update.SeriesName = item.Show.Title
		update.SeasonNumber = item.Episode.Season
		update.EpisodeNumber = item.Episode.Number
	} else {
		return nil
	}

	return update
}
