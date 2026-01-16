package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/plex"
	"novastream/services/watchlist"
)

// Service manages scheduled task execution
type Service struct {
	configManager    *config.Manager
	plexClient       *plex.Client
	watchlistService *watchlist.Service

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
	Count      int
	DryRun     bool
	ToAdd      []config.DryRunItem
	ToRemove   []config.DryRunItem
}

// NewService creates a new scheduler service
func NewService(
	configManager *config.Manager,
	plexClient *plex.Client,
	watchlistService *watchlist.Service,
) *Service {
	return &Service{
		configManager:    configManager,
		plexClient:       plexClient,
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
