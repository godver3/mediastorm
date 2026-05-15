package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
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
	"novastream/services/jellyfin"
	"novastream/services/localmedia"
	"novastream/services/plex"
	"novastream/services/prewarm"
	"novastream/services/simkl"
	"novastream/services/trakt"
	"novastream/services/watchlist"
)

// Service manages scheduled task execution
type Service struct {
	configManager     *config.Manager
	plexClient        *plex.Client
	traktClient       *trakt.Client
	simklClient       *simkl.Client
	jellyfinClient    *jellyfin.Client
	usersService      schedulerUsersProvider
	watchlistService  *watchlist.Service
	epgService        *epg.Service
	backupService     *backup.Service
	historyService    *history.Service
	prewarmService    *prewarm.Service
	localMediaService localMediaScanner

	// Runtime state
	mu      sync.RWMutex
	running bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	// Task state tracking (in-memory, not persisted)
	taskRunning         map[string]bool
	taskMu              sync.RWMutex
	lastFullSyncTimes   map[string]time.Time // tracks last full Trakt history sync per task ID
	lastFullSyncTimesMu sync.Mutex
}

type schedulerUsersProvider interface {
	Exists(id string) bool
	ListAll() []models.User
}

type localMediaScanner interface {
	ListLibraries(ctx context.Context) ([]models.LocalMediaLibrary, error)
	StartScan(ctx context.Context, libraryID string) (models.LocalMediaScanSummary, error)
}

// SyncResult contains the result of a sync operation including dry run details
type SyncResult struct {
	Count    int
	DryRun   bool
	ToAdd    []config.DryRunItem
	ToRemove []config.DryRunItem
	Message  string // Optional message for display
	Config   map[string]string
}

const (
	traktPlaybackStopThreshold    = 80.0
	traktPlaybackWatchedThreshold = 90.0
)

// NewService creates a new scheduler service
func NewService(
	configManager *config.Manager,
	plexClient *plex.Client,
	traktClient *trakt.Client,
	watchlistService *watchlist.Service,
) *Service {
	return &Service{
		configManager:     configManager,
		plexClient:        plexClient,
		traktClient:       traktClient,
		watchlistService:  watchlistService,
		taskRunning:       make(map[string]bool),
		lastFullSyncTimes: make(map[string]time.Time),
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

	// "once" tasks that have already run should not run again
	if task.Frequency == config.ScheduledTaskFrequencyOnce && task.LastRunAt != nil {
		return false
	}

	// Never run before
	if task.LastRunAt == nil {
		return true
	}

	interval := s.getInterval(task.Type, task.Frequency)
	return time.Since(*task.LastRunAt) >= interval
}

// getInterval returns the duration for a given task type and frequency.
// Prewarm tasks use a hardcoded 15-minute interval (dynamic TTL manages re-resolve cadence).
func (s *Service) getInterval(taskType config.ScheduledTaskType, freq config.ScheduledTaskFrequency) time.Duration {
	if taskType == config.ScheduledTaskTypePrewarm {
		return 15 * time.Minute
	}
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
	case config.ScheduledTaskFrequencyOnce:
		return time.Duration(math.MaxInt64)
	default:
		return 24 * time.Hour
	}
}

func (s *Service) SetLocalMediaService(ls *localmedia.Service) {
	s.localMediaService = ls
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
	case config.ScheduledTaskTypeLocalMediaScan:
		result, err = s.executeLocalMediaScan(task)
	case config.ScheduledTaskTypeTraktHistorySync:
		result, err = s.executeTraktHistorySync(task)
	case config.ScheduledTaskTypeSimklHistorySync:
		result, err = s.executeSimklHistorySync(task)
	case config.ScheduledTaskTypePrewarm:
		result, err = s.executePrewarm(task)
	case config.ScheduledTaskTypePlexHistorySync:
		result, err = s.executePlexHistorySync(task)
	case config.ScheduledTaskTypeJellyfinFavoritesSync:
		result, err = s.executeJellyfinFavoritesSync(task)
	case config.ScheduledTaskTypeJellyfinHistorySync:
		result, err = s.executeJellyfinHistorySync(task)
	case config.ScheduledTaskTypeMDBListWatchlistSync:
		result, err = s.executeMDBListWatchlistSync(task)
	case config.ScheduledTaskTypeMDBListHistorySync:
		result, err = s.executeMDBListHistorySync(task)
	default:
		log.Printf("[scheduler] Unknown task type: %s", task.Type)
		return
	}

	// Update task status in settings
	s.updateTaskStatus(task.ID, err, result)
}

func (s *Service) executeLocalMediaScan(task config.ScheduledTask) (SyncResult, error) {
	if s.localMediaService == nil {
		return SyncResult{}, errors.New("local media service not configured")
	}
	libraryID := strings.TrimSpace(task.Config["libraryId"])
	if libraryID == "" {
		return SyncResult{}, errors.New("local media scan requires libraryId")
	}

	if libraryID == config.ScheduledTaskLocalMediaAllLibraries {
		libraries, err := s.localMediaService.ListLibraries(context.Background())
		if err != nil {
			return SyncResult{}, fmt.Errorf("list local media libraries: %w", err)
		}
		if len(libraries) == 0 {
			return SyncResult{}, errors.New("no local media libraries configured")
		}

		var total models.LocalMediaScanSummary
		scanned := 0
		for _, library := range libraries {
			summary, err := s.localMediaService.StartScan(context.Background(), library.ID)
			if err != nil {
				return SyncResult{}, fmt.Errorf("scan library %q: %w", library.Name, err)
			}
			scanned++
			total.Discovered += summary.Discovered
			total.Matched += summary.Matched
			total.LowConfidence += summary.LowConfidence
			total.Unmatched += summary.Unmatched
		}

		return SyncResult{
			Count: total.Discovered,
			Message: fmt.Sprintf(
				"Local media scan completed for %d libraries: %d discovered, %d matched, %d low confidence, %d unmatched",
				scanned,
				total.Discovered,
				total.Matched,
				total.LowConfidence,
				total.Unmatched,
			),
		}, nil
	}

	summary, err := s.localMediaService.StartScan(context.Background(), libraryID)
	if err != nil {
		return SyncResult{}, err
	}

	return SyncResult{
		Count:   summary.Discovered,
		Message: fmt.Sprintf("Local media scan completed: %d discovered, %d matched, %d low confidence, %d unmatched", summary.Discovered, summary.Matched, summary.LowConfidence, summary.Unmatched),
	}, nil
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
			if result.Config != nil {
				if settings.ScheduledTasks.Tasks[i].Config == nil {
					settings.ScheduledTasks.Tasks[i].Config = make(map[string]string)
				}
				for key, value := range result.Config {
					settings.ScheduledTasks.Tasks[i].Config[key] = value
				}
			}

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

			// Auto-disable "once" tasks after completion
			if settings.ScheduledTasks.Tasks[i].Frequency == config.ScheduledTaskFrequencyOnce {
				settings.ScheduledTasks.Tasks[i].Enabled = false
				log.Printf("[scheduler] Auto-disabled one-time task %s", taskID)
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

// SetSimklClient sets the Simkl client for scheduled Simkl sync tasks.
func (s *Service) SetSimklClient(simklClient *simkl.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.simklClient = simklClient
}

// SetPrewarmService sets the prewarm service for scheduled prewarm tasks.
func (s *Service) SetPrewarmService(prewarmService *prewarm.Service) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prewarmService = prewarmService
}

// SetJellyfinClient sets the Jellyfin client for scheduled Jellyfin sync tasks.
func (s *Service) SetJellyfinClient(jellyfinClient *jellyfin.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jellyfinClient = jellyfinClient
}

// SetUsersService sets the users service for validating and resolving profile IDs.
func (s *Service) SetUsersService(usersService schedulerUsersProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usersService = usersService
}

func (s *Service) resolveTaskProfileID(task config.ScheduledTask) (string, error) {
	profileID := strings.TrimSpace(task.Config["profileId"])
	if profileID == "" {
		return "", errors.New("missing profileId in task config")
	}
	return s.resolveProfileID(profileID)
}

func (s *Service) resolveProfileID(profileID string) (string, error) {
	s.mu.RLock()
	usersService := s.usersService
	s.mu.RUnlock()

	if usersService == nil {
		return profileID, nil
	}

	if usersService.Exists(profileID) {
		return profileID, nil
	}

	if profileID != models.DefaultUserID {
		return "", fmt.Errorf("profile %q not found", profileID)
	}

	users := usersService.ListAll()
	if len(users) == 0 {
		return "", fmt.Errorf("legacy profile %q could not be resolved: no profiles exist", profileID)
	}

	if len(users) == 1 {
		log.Printf("[scheduler] Resolved legacy profile id %q to %q", profileID, users[0].ID)
		return users[0].ID, nil
	}

	var primaryMatches []models.User
	for _, user := range users {
		if user.Name == models.DefaultUserName {
			primaryMatches = append(primaryMatches, user)
		}
	}
	if len(primaryMatches) == 1 {
		log.Printf("[scheduler] Resolved legacy profile id %q to primary profile %q", profileID, primaryMatches[0].ID)
		return primaryMatches[0].ID, nil
	}

	return "", fmt.Errorf("legacy profile %q could not be resolved automatically; update the task to use a current profile id", profileID)
}

// executePlexWatchlistSync syncs a Plex watchlist to/from a profile
func (s *Service) executePlexWatchlistSync(task config.ScheduledTask) (SyncResult, error) {
	plexAccountID := task.Config["plexAccountId"]
	profileID, err := s.resolveTaskProfileID(task)

	if plexAccountID == "" || profileID == "" {
		return SyncResult{}, errors.New("missing plexAccountId or profileId in task config")
	}
	if err != nil {
		return SyncResult{}, err
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
		for _, key := range schedulerWatchlistMatchKeys(item.MediaType, item.ID, item.ExternalIDs) {
			existingKeys[key] = true
		}
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
		for _, key := range schedulerWatchlistMatchKeys(mediaType, itemID, extIDs) {
			plexItemKeys[key] = true
		}
		isNew := !schedulerWatchlistHasAnyKey(existingKeys, mediaType, itemID, extIDs)

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
				if schedulerWatchlistHasAnyKey(plexItemKeys, localItem.MediaType, localItem.ID, localItem.ExternalIDs) {
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
		// Get Plex ratingKey from external IDs, resolving via discover search when
		// the item was added locally from a non-Plex source.
		plexID := ""
		if localItem.ExternalIDs != nil {
			plexID = localItem.ExternalIDs["plex"]
		}
		if plexID == "" && localItem.ExternalIDs != nil {
			resolvedID, err := s.plexClient.ResolveRatingKey(authToken, localItem.Name, localItem.MediaType, localItem.Year, localItem.ExternalIDs)
			if err != nil {
				log.Printf("[scheduler] Failed to resolve Plex ID for %s: %v", localItem.Name, err)
			} else if resolvedID != "" {
				plexID = resolvedID
			}
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
	plexKeys := make(map[string]bool)

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
		for _, key := range schedulerWatchlistMatchKeys(mediaType, itemID, extIDs) {
			plexKeys[key] = true
		}
	}

	localKeys := make(map[string]bool)
	for _, item := range localItems {
		for _, key := range schedulerWatchlistMatchKeys(item.MediaType, item.ID, item.ExternalIDs) {
			localKeys[key] = true
		}
	}

	synced := 0

	// Step 1: Sync Plex → Local (items in Plex not in local)
	for i, plexItem := range plexItems {
		extIDs := map[string]string{}
		if i < len(externalIDs) && externalIDs[i] != nil {
			extIDs = externalIDs[i]
		}
		itemID := plexItem.RatingKey
		if tmdbID, ok := extIDs["tmdb"]; ok && tmdbID != "" {
			itemID = tmdbID
		} else if imdbID, ok := extIDs["imdb"]; ok && imdbID != "" {
			itemID = imdbID
		}
		extIDs["plex"] = plexItem.RatingKey
		mediaType := plex.NormalizeMediaType(plexItem.Type)

		if schedulerWatchlistHasAnyKey(localKeys, mediaType, itemID, extIDs) {
			continue // Already in local
		}

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
	for _, localItem := range localItems {
		if schedulerWatchlistHasAnyKey(plexKeys, localItem.MediaType, localItem.ID, localItem.ExternalIDs) {
			continue // Already in Plex
		}

		// Get Plex ratingKey from external IDs, resolving via discover search when
		// the item was added locally from a non-Plex source.
		plexID := ""
		if localItem.ExternalIDs != nil {
			plexID = localItem.ExternalIDs["plex"]
		}
		if plexID == "" && localItem.ExternalIDs != nil {
			resolvedID, err := s.plexClient.ResolveRatingKey(authToken, localItem.Name, localItem.MediaType, localItem.Year, localItem.ExternalIDs)
			if err != nil {
				log.Printf("[scheduler] Failed to resolve Plex ID for %s: %v", localItem.Name, err)
			} else if resolvedID != "" {
				plexID = resolvedID
			}
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
	profileID, err := s.resolveTaskProfileID(task)
	listType := task.Config["listType"] // watchlist, collection, favorites, custom

	if traktAccountID == "" || profileID == "" {
		return SyncResult{}, errors.New("missing traktAccountId or profileId in task config")
	}
	if err != nil {
		return SyncResult{}, err
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

	// Ensure valid token (handles refresh with proper locking)
	accessToken, err := s.traktClient.EnsureValidToken(traktAccount, s.configManager)
	if err != nil {
		return SyncResult{}, fmt.Errorf("ensure valid trakt token: %w", err)
	}
	traktAccount.AccessToken = accessToken

	// Update client with account credentials
	s.traktClient.UpdateCredentials(traktAccount.ClientID, traktAccount.ClientSecret)

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
		for _, key := range schedulerWatchlistMatchKeys(item.MediaType, item.ID, item.ExternalIDs) {
			existingKeys[key] = true
		}
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

		for _, key := range schedulerWatchlistMatchKeys(item.MediaType, itemID, item.IDs) {
			traktItemKeys[key] = true
		}
		isNew := !schedulerWatchlistHasAnyKey(existingKeys, item.MediaType, itemID, item.IDs)

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
				if schedulerWatchlistHasAnyKey(traktItemKeys, localItem.MediaType, localItem.ID, localItem.ExternalIDs) {
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
		var itemID string
		var mediaType string
		extIDs := map[string]string{}
		if item.Movie != nil {
			mediaType = "movie"
			extIDs = trakt.IDsToMap(item.Movie.IDs)
			if item.Movie.IDs.TMDB != 0 {
				itemID = strconv.Itoa(item.Movie.IDs.TMDB)
			} else if item.Movie.IDs.IMDB != "" {
				itemID = item.Movie.IDs.IMDB
			}
		} else if item.Show != nil {
			mediaType = "series"
			extIDs = trakt.IDsToMap(item.Show.IDs)
			if item.Show.IDs.TMDB != 0 {
				itemID = strconv.Itoa(item.Show.IDs.TMDB)
			} else if item.Show.IDs.IMDB != "" {
				itemID = item.Show.IDs.IMDB
			}
		}
		if itemID != "" {
			for _, key := range schedulerWatchlistMatchKeys(mediaType, itemID, extIDs) {
				traktItemKeys[key] = true
			}
		}
	}

	// Build set of local item keys for deletion checking
	localItemKeys := make(map[string]bool)

	var moviesToAdd []trakt.SyncMovie
	var showsToAdd []trakt.SyncShow
	exported := 0

	for _, localItem := range localItems {
		for _, key := range schedulerWatchlistMatchKeys(localItem.MediaType, localItem.ID, localItem.ExternalIDs) {
			localItemKeys[key] = true
		}

		// Check if already in Trakt
		if schedulerWatchlistHasAnyKey(traktItemKeys, localItem.MediaType, localItem.ID, localItem.ExternalIDs) {
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
			var itemID string
			var mediaType string
			extIDs := map[string]string{}
			var movie *trakt.Movie
			var show *trakt.Show

			if traktItem.Movie != nil {
				movie = traktItem.Movie
				mediaType = "movie"
				extIDs = trakt.IDsToMap(movie.IDs)
				if movie.IDs.TMDB != 0 {
					itemID = strconv.Itoa(movie.IDs.TMDB)
				} else if movie.IDs.IMDB != "" {
					itemID = movie.IDs.IMDB
				}
			} else if traktItem.Show != nil {
				show = traktItem.Show
				mediaType = "series"
				extIDs = trakt.IDsToMap(show.IDs)
				if show.IDs.TMDB != 0 {
					itemID = strconv.Itoa(show.IDs.TMDB)
				} else if show.IDs.IMDB != "" {
					itemID = show.IDs.IMDB
				}
			}

			if itemID == "" {
				continue
			}

			// Check if item exists in local watchlist
			if schedulerWatchlistHasAnyKey(localItemKeys, mediaType, itemID, extIDs) {
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
					MediaType: mediaType,
					ID:        itemID,
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
	traktKeys := make(map[string]bool)
	for _, item := range traktItems {
		itemID := item.IDs["trakt"]
		if tmdbID, ok := item.IDs["tmdb"]; ok && tmdbID != "" {
			itemID = tmdbID
		} else if imdbID, ok := item.IDs["imdb"]; ok && imdbID != "" {
			itemID = imdbID
		}
		for _, key := range schedulerWatchlistMatchKeys(item.MediaType, itemID, item.IDs) {
			traktKeys[key] = true
		}
	}

	localKeys := make(map[string]bool)
	for _, item := range localItems {
		for _, key := range schedulerWatchlistMatchKeys(item.MediaType, item.ID, item.ExternalIDs) {
			localKeys[key] = true
		}
	}

	synced := 0

	// Step 1: Sync Trakt → Local (items in Trakt not in local)
	for _, traktItem := range traktItems {
		itemID := traktItem.IDs["trakt"]
		if tmdbID, ok := traktItem.IDs["tmdb"]; ok && tmdbID != "" {
			itemID = tmdbID
		} else if imdbID, ok := traktItem.IDs["imdb"]; ok && imdbID != "" {
			itemID = imdbID
		}

		if schedulerWatchlistHasAnyKey(localKeys, traktItem.MediaType, itemID, traktItem.IDs) {
			continue // Already in local
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

		for _, localItem := range localItems {
			if schedulerWatchlistHasAnyKey(traktKeys, localItem.MediaType, localItem.ID, localItem.ExternalIDs) {
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
	profileID, err := s.resolveTaskProfileID(task)

	if traktAccountID == "" || profileID == "" {
		return SyncResult{}, errors.New("missing traktAccountId or profileId in task config")
	}
	if err != nil {
		return SyncResult{}, err
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

	// Ensure valid token (handles refresh with proper locking)
	accessToken, err := s.traktClient.EnsureValidToken(traktAccount, s.configManager)
	if err != nil {
		return SyncResult{}, fmt.Errorf("ensure valid trakt token: %w", err)
	}
	traktAccount.AccessToken = accessToken

	// Update client with account credentials
	s.traktClient.UpdateCredentials(traktAccount.ClientID, traktAccount.ClientSecret)

	switch syncDirection {
	case "trakt_to_local":
		result, err := s.syncTraktHistoryToLocal(task, traktAccount, profileID, dryRun)
		if err != nil {
			return result, err
		}
		if !dryRun {
			if err := s.syncPlaybackFromTrakt(traktAccount, profileID, nil); err != nil {
				log.Printf("[scheduler] Warning: sync playback from Trakt failed: %v", err)
			}
		}
		return result, nil
	case "local_to_trakt":
		result, err := s.syncLocalHistoryToTrakt(task, traktAccount, profileID, dryRun)
		if err != nil {
			return result, err
		}
		if !dryRun {
			if _, err := s.syncPlaybackToTrakt(traktAccount, profileID); err != nil {
				log.Printf("[scheduler] Warning: sync playback to Trakt failed: %v", err)
			}
		}
		return result, nil
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

	// Determine incremental cursor: lastRunAt - 5min safety buffer.
	// Periodically (every 6h) do a full sync to catch backdated watches.
	// Trakt's start_at filters by watched_at (event time), so manually-added
	// watches with a past watched_at are missed by incremental syncs.
	const fullSyncInterval = 6 * time.Hour
	var since time.Time
	isFullSync := false

	s.lastFullSyncTimesMu.Lock()
	lastFull, ok := s.lastFullSyncTimes[task.ID]
	s.lastFullSyncTimesMu.Unlock()

	if !ok || time.Since(lastFull) >= fullSyncInterval {
		// Full sync: fetch all history from the past year
		since = time.Now().UTC().AddDate(-1, 0, 0)
		isFullSync = true
		log.Printf("[scheduler] Performing full Trakt history reconciliation (last full sync: %v)", lastFull)
	} else if task.LastRunAt != nil {
		since = task.LastRunAt.Add(-5 * time.Minute)
	}

	log.Printf("[scheduler] Fetching Trakt watch history since=%v (fullSync=%v)", since, isFullSync)

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

	if isFullSync {
		s.lastFullSyncTimesMu.Lock()
		s.lastFullSyncTimes[task.ID] = time.Now().UTC()
		s.lastFullSyncTimesMu.Unlock()
		log.Printf("[scheduler] Full Trakt history reconciliation complete, next in %v", fullSyncInterval)
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

	// Build a set of items already on Trakt, including alternate provider IDs,
	// so local export does not re-scrobble the same movie/show under a different key.
	watched := true
	alreadyOnTrakt := make(map[string]bool)
	for _, ti := range traktItems {
		update := s.traktHistoryItemToUpdate(ti, &watched)
		if update != nil {
			for _, id := range alternateItemIDs(update.MediaType, update.ItemID, update.ExternalIDs) {
				key := strings.ToLower(update.MediaType) + ":" + strings.ToLower(id)
				alreadyOnTrakt[key] = true
			}
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
		if itemAlreadyOnTrakt(item, alreadyOnTrakt) {
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

func itemAlreadyOnTrakt(item models.WatchHistoryItem, alreadyOnTrakt map[string]bool) bool {
	for _, id := range alternateItemIDs(item.MediaType, item.ItemID, item.ExternalIDs) {
		key := strings.ToLower(item.MediaType) + ":" + strings.ToLower(id)
		if alreadyOnTrakt[key] {
			return true
		}
	}
	return false
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

	// Sync playback positions in both directions for bidirectional tasks too.
	// Push first, then pull — passing exported keys so the pull skips items
	// we just pushed (their Trakt paused_at is artificially fresh).
	if !dryRun {
		justExported, err := s.syncPlaybackToTrakt(traktAccount, profileID)
		if err != nil {
			log.Printf("[scheduler] Warning: sync playback to Trakt failed: %v", err)
		}
		if err := s.syncPlaybackFromTrakt(traktAccount, profileID, justExported); err != nil {
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

// executeSimklHistorySync imports watch history from Simkl into local history.
func (s *Service) executeSimklHistorySync(task config.ScheduledTask) (SyncResult, error) {
	s.mu.RLock()
	historySvc := s.historyService
	simklClient := s.simklClient
	s.mu.RUnlock()

	if historySvc == nil {
		return SyncResult{}, errors.New("history service not configured")
	}
	if simklClient == nil {
		return SyncResult{}, errors.New("simkl client not configured")
	}

	simklAccountID := task.Config["simklAccountId"]
	profileID, err := s.resolveTaskProfileID(task)
	if simklAccountID == "" || profileID == "" {
		return SyncResult{}, errors.New("missing simklAccountId or profileId in task config")
	}
	if err != nil {
		return SyncResult{}, err
	}
	dryRun := task.Config["dryRun"] == "true"

	settings, err := s.configManager.Load()
	if err != nil {
		return SyncResult{}, fmt.Errorf("load settings: %w", err)
	}
	simklAccount := settings.Simkl.GetAccountByID(simklAccountID)
	if simklAccount == nil {
		return SyncResult{}, errors.New("simkl account not found")
	}
	if simklAccount.ClientID == "" || simklAccount.AccessToken == "" {
		return SyncResult{}, errors.New("simkl account not authenticated")
	}

	activities, err := simklClient.GetActivities(simklAccount.ClientID, simklAccount.AccessToken)
	if err != nil {
		return SyncResult{}, fmt.Errorf("fetch simkl activities: %w", err)
	}
	latestActivity := latestTimeInSimklActivity(activities)
	savedActivity := strings.TrimSpace(task.Config["lastSimklActivityAt"])
	if savedActivity != "" && !latestActivity.IsZero() {
		savedAt, parseErr := time.Parse(time.RFC3339, savedActivity)
		if parseErr == nil && !latestActivity.After(savedAt) {
			log.Printf("[scheduler] Simkl history unchanged since %s; skipping listing calls", savedActivity)
			return SyncResult{DryRun: dryRun, Count: 0}, nil
		}
	}

	var responses []*simkl.AllItemsResponse
	if savedActivity == "" {
		log.Printf("[scheduler] Performing initial Simkl history sync using sequential bucket fetches")
		for _, bucket := range []string{"movies", "shows", "anime"} {
			resp, err := simklClient.GetInitialSyncItems(simklAccount.ClientID, simklAccount.AccessToken, bucket)
			if err != nil {
				return SyncResult{DryRun: dryRun}, fmt.Errorf("fetch simkl %s history: %w", bucket, err)
			}
			responses = append(responses, resp)
		}
	} else {
		log.Printf("[scheduler] Fetching Simkl history delta since %s", savedActivity)
		resp, err := simklClient.GetAllItemsSince(simklAccount.ClientID, simklAccount.AccessToken, savedActivity)
		if err != nil {
			return SyncResult{DryRun: dryRun}, fmt.Errorf("fetch simkl history delta: %w", err)
		}
		responses = append(responses, resp)
	}

	watched := true
	seen := make(map[string]bool)
	var updates []models.WatchHistoryUpdate
	result := SyncResult{DryRun: dryRun}
	for _, resp := range responses {
		for _, update := range simklAllItemsToWatchHistory(resp, &watched) {
			key := strings.ToLower(update.MediaType) + ":" + strings.ToLower(update.ItemID)
			if seen[key] {
				continue
			}
			seen[key] = true
			if dryRun {
				result.ToAdd = append(result.ToAdd, config.DryRunItem{Name: update.Name, MediaType: update.MediaType, ID: update.ItemID})
				continue
			}
			updates = append(updates, update)
		}
	}

	if dryRun {
		result.Count = len(result.ToAdd)
		return result, nil
	}
	if len(updates) > 0 {
		imported, err := historySvc.ImportWatchHistory(profileID, updates)
		if err != nil {
			return result, fmt.Errorf("import simkl watch history: %w", err)
		}
		result.Count = imported
	}
	if !latestActivity.IsZero() {
		result.Config = map[string]string{"lastSimklActivityAt": latestActivity.UTC().Format(time.RFC3339)}
	}
	log.Printf("[scheduler] Imported %d/%d items from Simkl history", result.Count, len(updates))
	return result, nil
}

func latestTimeInSimklActivity(activity simkl.ActivityResponse) time.Time {
	var latest time.Time
	var walk func(interface{})
	walk = func(v interface{}) {
		switch value := v.(type) {
		case map[string]interface{}:
			for _, child := range value {
				walk(child)
			}
		case []interface{}:
			for _, child := range value {
				walk(child)
			}
		case string:
			if ts, err := time.Parse(time.RFC3339, value); err == nil && ts.After(latest) {
				latest = ts
			}
		}
	}
	walk(map[string]interface{}(activity))
	return latest
}

func simklAllItemsToWatchHistory(resp *simkl.AllItemsResponse, watched *bool) []models.WatchHistoryUpdate {
	if resp == nil {
		return nil
	}
	var updates []models.WatchHistoryUpdate
	for _, raw := range resp.Movies {
		if update := simklMovieToUpdate(raw, watched); update != nil {
			updates = append(updates, *update)
		}
	}
	for _, raw := range resp.Shows {
		updates = append(updates, simklShowToUpdates(raw, watched)...)
	}
	for _, raw := range resp.Anime {
		updates = append(updates, simklShowToUpdates(raw, watched)...)
	}
	return updates
}

func simklMovieToUpdate(raw json.RawMessage, watched *bool) *models.WatchHistoryUpdate {
	obj := decodeJSONObject(raw)
	if len(obj) == 0 || !simklItemIsWatched(obj) {
		return nil
	}
	movie := nestedObject(obj, "movie")
	if movie == nil {
		movie = obj
	}
	ids := simklIDsFromObject(movie)
	if len(ids) == 0 {
		ids = simklIDsFromObject(obj)
	}
	itemID := simklMovieItemID(ids)
	if itemID == "" {
		return nil
	}
	return &models.WatchHistoryUpdate{
		MediaType:   "movie",
		ItemID:      itemID,
		Name:        stringFromAny(movie["title"]),
		Year:        intFromAny(movie["year"]),
		Watched:     watched,
		WatchedAt:   simklFirstTime(obj, "watched_at", "last_watched_at", "completed_at", "last_watched"),
		ExternalIDs: ids,
	}
}

func simklShowToUpdates(raw json.RawMessage, watched *bool) []models.WatchHistoryUpdate {
	obj := decodeJSONObject(raw)
	if len(obj) == 0 || !simklItemIsWatched(obj) {
		return nil
	}
	show := nestedObject(obj, "show")
	if show == nil {
		show = nestedObject(obj, "anime")
	}
	if show == nil {
		show = obj
	}
	ids := simklIDsFromObject(show)
	if len(ids) == 0 {
		ids = simklIDsFromObject(obj)
	}
	seriesID := simklSeriesItemID(ids)
	if seriesID == "" {
		return nil
	}

	showTitle := stringFromAny(show["title"])
	showYear := intFromAny(show["year"])
	var updates []models.WatchHistoryUpdate
	for _, seasonObj := range objectArray(obj["seasons"]) {
		seasonNumber := intFromAny(seasonObj["number"])
		for _, episodeObj := range objectArray(seasonObj["episodes"]) {
			episodeNumber := intFromAny(episodeObj["number"])
			if episodeNumber == 0 {
				episodeNumber = intFromAny(episodeObj["episode"])
			}
			if seasonNumber == 0 || episodeNumber == 0 {
				continue
			}
			watchedAt := simklFirstTime(episodeObj, "watched_at", "last_watched_at", "completed_at", "last_watched")
			if watchedAt.IsZero() {
				watchedAt = simklFirstTime(obj, "watched_at", "last_watched_at", "completed_at", "last_watched")
			}
			updates = append(updates, models.WatchHistoryUpdate{
				MediaType:     "episode",
				ItemID:        fmt.Sprintf("%s:s%02de%02d", seriesID, seasonNumber, episodeNumber),
				Name:          stringFromAny(episodeObj["title"]),
				Year:          showYear,
				Watched:       watched,
				WatchedAt:     watchedAt,
				ExternalIDs:   ids,
				SeriesID:      seriesID,
				SeriesName:    showTitle,
				SeasonNumber:  seasonNumber,
				EpisodeNumber: episodeNumber,
			})
		}
	}
	return updates
}

func simklItemIsWatched(obj map[string]interface{}) bool {
	status := strings.ToLower(strings.TrimSpace(stringFromAny(obj["status"])))
	return status == "" || status == "completed" || status == "watching"
}

func simklMovieItemID(ids map[string]string) string {
	if id := ids["tvdb"]; id != "" {
		return "tvdb:movie:" + id
	}
	if id := ids["tmdb"]; id != "" {
		return "tmdb:movie:" + id
	}
	return ids["imdb"]
}

func simklSeriesItemID(ids map[string]string) string {
	if id := ids["tvdb"]; id != "" {
		return "tvdb:series:" + id
	}
	if id := ids["tmdb"]; id != "" {
		return "tmdb:tv:" + id
	}
	if id := ids["imdb"]; id != "" {
		return "imdb:" + id
	}
	return ""
}

func simklIDsFromObject(obj map[string]interface{}) map[string]string {
	idsObj := nestedObject(obj, "ids")
	ids := make(map[string]string)
	for _, key := range []string{"simkl", "imdb", "tmdb", "tvdb"} {
		if value := stringFromAny(idsObj[key]); value != "" {
			ids[key] = value
		}
	}
	return ids
}

func simklFirstTime(obj map[string]interface{}, keys ...string) time.Time {
	for _, key := range keys {
		if ts, ok := parseFlexibleTime(obj[key]); ok {
			return ts
		}
	}
	return time.Time{}
}

func decodeJSONObject(raw json.RawMessage) map[string]interface{} {
	var obj map[string]interface{}
	_ = json.Unmarshal(raw, &obj)
	return obj
}

func nestedObject(obj map[string]interface{}, key string) map[string]interface{} {
	if obj == nil {
		return nil
	}
	child, _ := obj[key].(map[string]interface{})
	return child
}

func objectArray(value interface{}) []map[string]interface{} {
	array, ok := value.([]interface{})
	if !ok {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(array))
	for _, item := range array {
		if obj, ok := item.(map[string]interface{}); ok {
			out = append(out, obj)
		}
	}
	return out
}

func parseFlexibleTime(value interface{}) (time.Time, bool) {
	v, ok := value.(string)
	if !ok || strings.TrimSpace(v) == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if ts, err := time.Parse(layout, v); err == nil {
			return ts, true
		}
	}
	return time.Time{}, false
}

func stringFromAny(value interface{}) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		if math.Trunc(v) == v {
			return strconv.Itoa(int(v))
		}
		return fmt.Sprintf("%v", v)
	case int:
		return strconv.Itoa(v)
	case json.Number:
		return v.String()
	default:
		return ""
	}
}

func intFromAny(value interface{}) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		i, _ := strconv.Atoi(v)
		return i
	case json.Number:
		i, _ := strconv.Atoi(v.String())
		return i
	default:
		return 0
	}
}

// syncPlaybackToTrakt exports playback progress to Trakt.
// Partial progress below the watched threshold remains incomplete locally.
// Trakt requires scrobble/stop for higher in-progress percentages, so:
// 1-79% uses pause, 80-89% uses stop, and 90%+ becomes watched history.
// Returns the set of exported item keys (mediaType:itemID) so the caller can
// exclude them from a subsequent pull to avoid the push→pull timestamp round-trip.
func (s *Service) syncPlaybackToTrakt(traktAccount *config.TraktAccount, profileID string) (map[string]bool, error) {
	exported := make(map[string]bool)
	if s.historyService == nil {
		return exported, nil
	}

	items, err := s.historyService.ListPlaybackProgress(profileID)
	if err != nil {
		return exported, fmt.Errorf("list playback progress: %w", err)
	}

	s.traktClient.UpdateCredentials(traktAccount.ClientID, traktAccount.ClientSecret)
	accessToken := traktAccount.AccessToken
	var moviesToHistory []trakt.SyncMovie
	type playbackShowKey struct {
		tvdbID int
		tmdbID int
		imdbID string
	}
	showSeasonsToHistory := make(map[playbackShowKey]map[int][]trakt.SyncEpisode)
	showIDsToHistory := make(map[playbackShowKey]trakt.SyncIDs)

	for _, item := range items {
		if item.HiddenFromContinueWatching {
			continue
		}

		if item.PercentWatched <= 1 {
			continue
		}

		// Need valid external IDs to match on Trakt
		if len(item.ExternalIDs) == 0 {
			continue
		}

		if playbackPercentCountsAsWatched(item.PercentWatched) {
			switch item.MediaType {
			case "movie":
				moviesToHistory = append(moviesToHistory, trakt.SyncMovie{
					WatchedAt: item.UpdatedAt.UTC().Format(time.RFC3339),
					IDs:       externalIDsToSyncIDs(item.ExternalIDs),
				})
			case "episode":
				ids := seriesIDToSyncIDs(item.SeriesID, item.ExternalIDs)
				if ids.TVDB == 0 && ids.TMDB == 0 && ids.IMDB == "" {
					continue
				}
				if item.SeasonNumber == 0 || item.EpisodeNumber == 0 {
					continue
				}
				sk := playbackShowKey{tvdbID: ids.TVDB, tmdbID: ids.TMDB, imdbID: ids.IMDB}
				if showSeasonsToHistory[sk] == nil {
					showSeasonsToHistory[sk] = make(map[int][]trakt.SyncEpisode)
				}
				showSeasonsToHistory[sk][item.SeasonNumber] = append(showSeasonsToHistory[sk][item.SeasonNumber], trakt.SyncEpisode{
					Number:    item.EpisodeNumber,
					WatchedAt: item.UpdatedAt.UTC().Format(time.RFC3339),
				})
				showIDsToHistory[sk] = ids
			}
			exported[strings.ToLower(item.MediaType)+":"+strings.ToLower(item.ItemID)] = true
			continue
		}

		if !playbackPercentCountsAsProgress(item.PercentWatched) {
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

		var syncErr error
		if playbackPercentNeedsStop(item.PercentWatched) {
			_, syncErr = s.traktClient.ScrobbleStop(accessToken, req)
		} else {
			// ScrobblePause saves the position on Trakt without triggering "now watching"
			_, syncErr = s.traktClient.ScrobblePause(accessToken, req)
		}
		if syncErr != nil {
			if errors.Is(syncErr, trakt.ErrNotFound) {
				continue
			}
			log.Printf("[scheduler] Failed to sync playback for %s %s: %v", item.MediaType, item.ItemID, syncErr)
			continue
		}
		exported[strings.ToLower(item.MediaType)+":"+strings.ToLower(item.ItemID)] = true
	}

	if len(moviesToHistory) > 0 || len(showSeasonsToHistory) > 0 {
		var shows []trakt.SyncShow
		for sk, seasonEpisodes := range showSeasonsToHistory {
			var seasons []trakt.SyncSeason
			for seasonNumber, episodes := range seasonEpisodes {
				if len(episodes) == 0 {
					continue
				}
				seasons = append(seasons, trakt.SyncSeason{
					Number:   seasonNumber,
					Episodes: episodes,
				})
			}
			shows = append(shows, trakt.SyncShow{
				IDs:     showIDsToHistory[sk],
				Seasons: seasons,
			})
		}
		syncReq := trakt.SyncHistoryRequest{
			Movies: moviesToHistory,
			Shows:  shows,
		}
		if _, err := s.traktClient.AddToHistory(traktAccount.AccessToken, syncReq); err != nil {
			return exported, fmt.Errorf("add high-progress playback to trakt history: %w", err)
		}
	}

	if len(exported) > 0 {
		log.Printf("[scheduler] Exported %d playback positions to Trakt", len(exported))
	}
	return exported, nil
}

// syncPlaybackFromTrakt imports partial playback progress from Trakt to local storage.
// justExported contains item keys that were just pushed to Trakt in this sync cycle;
// those are skipped to avoid the push→pull round-trip that would bump all timestamps to "now".
func (s *Service) syncPlaybackFromTrakt(traktAccount *config.TraktAccount, profileID string, justExported map[string]bool) error {
	if s.historyService == nil {
		return nil
	}

	s.traktClient.UpdateCredentials(traktAccount.ClientID, traktAccount.ClientSecret)
	accessToken := traktAccount.AccessToken

	// Build a reverse index from IMDB ID → local itemID for existing movie progress.
	// Trakt often doesn't return TVDB IDs, so we need IMDB as a bridge to find
	// local entries that use TVDB-prefixed IDs.
	imdbToLocalID := make(map[string]string)
	if allProgress, err := s.historyService.ListPlaybackProgress(profileID); err == nil {
		for _, p := range allProgress {
			if p.MediaType == "movie" && p.ExternalIDs != nil {
				if imdbID, ok := p.ExternalIDs["imdb"]; ok && imdbID != "" {
					imdbToLocalID[imdbID] = p.ItemID
				}
			}
		}
	}

	imported := 0
	watchedImported := 0

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

			// Build all possible ID variants for this item (TVDB, TMDB, IMDB).
			// The same movie/episode may exist locally under a different provider ID.
			// Trakt often lacks TVDB IDs, so also check via IMDB reverse index.
			altIDs := alternateItemIDs(update.MediaType, update.ItemID, update.ExternalIDs)
			if update.MediaType == "movie" {
				if imdbID, ok := update.ExternalIDs["imdb"]; ok && imdbID != "" {
					if localID, found := imdbToLocalID[imdbID]; found {
						// Add the local ID variant if not already present
						alreadyHave := false
						for _, a := range altIDs {
							if strings.EqualFold(a, localID) {
								alreadyHave = true
								break
							}
						}
						if !alreadyHave {
							altIDs = append(altIDs, localID)
						}
					}
				}
			}

			// Skip items we just pushed to Trakt in this sync cycle — their
			// paused_at on Trakt is artificially fresh from the ScrobblePause call.
			exported := false
			for _, aid := range altIDs {
				exportKey := strings.ToLower(update.MediaType) + ":" + strings.ToLower(aid)
				if justExported[exportKey] {
					exported = true
					break
				}
			}
			if exported {
				continue
			}

			if playbackPercentCountsAsWatched(traktItem.Progress) {
				historyUpdate := s.traktPlaybackItemToHistoryUpdate(traktItem)
				if historyUpdate == nil {
					continue
				}
				if _, err := s.historyService.ImportWatchHistory(profileID, []models.WatchHistoryUpdate{*historyUpdate}); err != nil {
					log.Printf("[scheduler] Failed to import Trakt playback as watch history for %s: %v", update.ItemID, err)
					continue
				}
				watchedImported++
				continue
			}

			if s.historyService.IsWatchedEpisodeProgressUpdate(profileID, *update) {
				continue
			}

			// Check if local progress exists under any provider ID variant.
			// If found under a different ID, use that ID so we update the existing
			// entry instead of creating a duplicate.
			var localProgress *models.PlaybackProgress
			for _, aid := range altIDs {
				lp, lpErr := s.historyService.GetPlaybackProgress(profileID, update.MediaType, aid)
				if lpErr == nil && lp != nil {
					localProgress = lp
					if aid != update.ItemID {
						update.ItemID = aid
					}
					break
				}
			}
			hiddenMarker := s.findHiddenPlaybackMarker(profileID, *update)

			if localProgress == nil && hiddenMarker != nil && !traktItem.PausedAt.After(hiddenMarker.UpdatedAt) {
				continue
			}

			if localProgress != nil {
				if localProgress.HiddenFromContinueWatching && playbackImportPercentDelta(localProgress, traktItem.Progress) < 2.0 {
					continue
				}
				// If local progress is newer (or same), skip
				if !localProgress.UpdatedAt.Before(traktItem.PausedAt) {
					continue
				}
			}

			latestWatchState, err := s.latestWatchStateForItem(profileID, update.MediaType, altIDs)
			if err != nil {
				log.Printf("[scheduler] Failed to load local watch state for %s: %v", update.ItemID, err)
				continue
			}
			if latestWatchState != nil {
				// Compare against the newest local watch-state change for this item,
				// not just the last watched timestamp. This lets newer partial progress
				// beat older watch/unwatch state, while still rejecting stale resume
				// imports after a more recent local watch-state change.
				if !latestWatchState.UpdatedAt.IsZero() && !latestWatchState.UpdatedAt.Before(traktItem.PausedAt) {
					continue
				}
			}

			// If local progress exists, only import if Trakt progress meaningfully differs.
			// This prevents the push→pull round-trip from bumping timestamps on every sync.
			if localProgress != nil && localProgress.Duration > 0 {
				traktPosition := (traktItem.Progress / 100) * localProgress.Duration
				positionDelta := traktPosition - localProgress.Position
				if positionDelta < 0 {
					positionDelta = -positionDelta
				}
				percentDelta := (positionDelta / localProgress.Duration) * 100
				if percentDelta < 2.0 {
					continue
				}
				update.Duration = localProgress.Duration
				update.Position = traktPosition
			} else {
				// No local progress — create a new entry from Trakt.
				// First check if the item exists in watch history under a different
				// provider ID, so we use the same ID and avoid duplicates.
				for _, aid := range altIDs {
					if aid == update.ItemID {
						continue
					}
					wh, whErr := s.historyService.GetWatchHistoryItem(profileID, update.MediaType, aid)
					if whErr == nil && wh != nil {
						update.ItemID = aid
						break
					}
				}
				// We only have a percentage from Trakt, not a real duration.
				// Store duration=0, position=0 with percentWatched set directly
				// so the frontend knows to use percentage-based resume.
				update.Duration = 0
				update.Position = 0
				update.PercentWatched = traktItem.Progress
				log.Printf("[scheduler] Creating new local progress from Trakt: %s %q at %.1f%%",
					update.MediaType, update.ItemID, traktItem.Progress)
			}
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
	if watchedImported > 0 {
		log.Printf("[scheduler] Imported %d Trakt playback items as watched history", watchedImported)
	}
	return nil
}

func playbackImportPercentDelta(localProgress *models.PlaybackProgress, traktPercent float64) float64 {
	if localProgress == nil {
		return 100
	}
	if localProgress.Duration > 0 {
		traktPosition := (traktPercent / 100) * localProgress.Duration
		positionDelta := traktPosition - localProgress.Position
		if positionDelta < 0 {
			positionDelta = -positionDelta
		}
		if localProgress.Duration <= 0 {
			return 100
		}
		return (positionDelta / localProgress.Duration) * 100
	}

	percentDelta := traktPercent - localProgress.PercentWatched
	if percentDelta < 0 {
		percentDelta = -percentDelta
	}
	return percentDelta
}

func (s *Service) findHiddenPlaybackMarker(profileID string, update models.PlaybackProgressUpdate) *models.PlaybackProgress {
	if s.historyService == nil || update.MediaType != "episode" {
		return nil
	}

	seriesIDs := alternateSeriesIDs(update.SeriesID, update.ExternalIDs)
	for _, seriesID := range seriesIDs {
		marker, err := s.historyService.GetPlaybackProgress(profileID, "episode", seriesID)
		if err != nil || marker == nil {
			continue
		}
		if marker.HiddenFromContinueWatching &&
			marker.ItemID == marker.SeriesID &&
			marker.SeasonNumber == 0 &&
			marker.EpisodeNumber == 0 {
			return marker
		}
	}

	return nil
}

func alternateSeriesIDs(primarySeriesID string, externalIDs map[string]string) []string {
	if primarySeriesID == "" {
		return nil
	}

	ids := []string{primarySeriesID}
	seen := map[string]bool{strings.ToLower(primarySeriesID): true}
	add := func(id string) {
		lower := strings.ToLower(id)
		if id != "" && !seen[lower] {
			seen[lower] = true
			ids = append(ids, id)
		}
	}

	if v, ok := externalIDs["tvdb"]; ok && v != "" {
		add(fmt.Sprintf("tvdb:series:%s", v))
	}
	if v, ok := externalIDs["tmdb"]; ok && v != "" {
		add(fmt.Sprintf("tmdb:tv:%s", v))
	}
	if v, ok := externalIDs["imdb"]; ok && v != "" {
		add(fmt.Sprintf("imdb:%s", v))
	}

	return ids
}

func (s *Service) latestWatchStateForItem(profileID, mediaType string, itemIDs []string) (*models.WatchHistoryItem, error) {
	var latest *models.WatchHistoryItem
	for _, itemID := range itemIDs {
		item, err := s.historyService.GetWatchHistoryItem(profileID, mediaType, itemID)
		if err != nil {
			return nil, err
		}
		if item == nil {
			continue
		}
		if latest == nil || isNewerWatchState(item, latest) {
			copy := *item
			latest = &copy
		}
	}
	return latest, nil
}

func isNewerWatchState(candidate, current *models.WatchHistoryItem) bool {
	if current == nil {
		return true
	}
	if candidate == nil {
		return false
	}
	if candidate.UpdatedAt.After(current.UpdatedAt) {
		return true
	}
	if current.UpdatedAt.After(candidate.UpdatedAt) {
		return false
	}
	if candidate.Watched != current.Watched {
		return !candidate.Watched
	}
	return candidate.ID < current.ID
}

func playbackPercentCountsAsProgress(percent float64) bool {
	return percent > 1 && percent < traktPlaybackWatchedThreshold
}

func playbackPercentNeedsStop(percent float64) bool {
	return percent >= traktPlaybackStopThreshold && percent < traktPlaybackWatchedThreshold
}

func playbackPercentCountsAsWatched(percent float64) bool {
	return percent >= traktPlaybackWatchedThreshold
}

// traktPlaybackItemToUpdate converts a Trakt PlaybackItem to a PlaybackProgressUpdate.
func (s *Service) traktPlaybackItemToUpdate(item trakt.PlaybackItem) *models.PlaybackProgressUpdate {
	update := &models.PlaybackProgressUpdate{
		Timestamp: item.PausedAt,
		IsPaused:  true,
	}

	if item.Type == "movie" && item.Movie != nil {
		ids := trakt.IDsToMap(item.Movie.IDs)
		// Use prefixed IDs to match the player's format (e.g. "tmdb:movie:603")
		var itemID string
		if tvdbID, ok := ids["tvdb"]; ok && tvdbID != "" {
			itemID = fmt.Sprintf("tvdb:movie:%s", tvdbID)
		} else if tmdbID, ok := ids["tmdb"]; ok && tmdbID != "" {
			itemID = fmt.Sprintf("tmdb:movie:%s", tmdbID)
		} else if imdbID, ok := ids["imdb"]; ok && imdbID != "" {
			itemID = imdbID
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

func (s *Service) traktPlaybackItemToHistoryUpdate(item trakt.PlaybackItem) *models.WatchHistoryUpdate {
	progressUpdate := s.traktPlaybackItemToUpdate(item)
	if progressUpdate == nil {
		return nil
	}

	watched := true
	update := &models.WatchHistoryUpdate{
		MediaType:     progressUpdate.MediaType,
		ItemID:        progressUpdate.ItemID,
		Watched:       &watched,
		WatchedAt:     item.PausedAt,
		ExternalIDs:   progressUpdate.ExternalIDs,
		SeasonNumber:  progressUpdate.SeasonNumber,
		EpisodeNumber: progressUpdate.EpisodeNumber,
		SeriesID:      progressUpdate.SeriesID,
		SeriesName:    progressUpdate.SeriesName,
		Year:          progressUpdate.Year,
	}

	if progressUpdate.MediaType == "episode" {
		update.Name = progressUpdate.EpisodeName
	} else {
		update.Name = progressUpdate.MovieName
	}

	return update
}

// alternateItemIDs returns all possible ID variants for a movie or episode,
// so we can find existing local progress regardless of which provider ID was used.
func alternateItemIDs(mediaType, primaryID string, externalIDs map[string]string) []string {
	ids := []string{primaryID}
	if externalIDs == nil {
		return ids
	}
	seen := map[string]bool{strings.ToLower(primaryID): true}

	add := func(id string) {
		lower := strings.ToLower(id)
		if !seen[lower] {
			seen[lower] = true
			ids = append(ids, id)
		}
	}

	if mediaType == "movie" {
		if v, ok := externalIDs["tvdb"]; ok && v != "" {
			add(fmt.Sprintf("tvdb:movie:%s", v))
		}
		if v, ok := externalIDs["tmdb"]; ok && v != "" {
			add(fmt.Sprintf("tmdb:movie:%s", v))
		}
		if v, ok := externalIDs["imdb"]; ok && v != "" {
			add(v)
		}
	}
	if mediaType == "episode" {
		lowerPrimary := strings.ToLower(primaryID)
		idx := strings.LastIndex(lowerPrimary, ":s")
		if idx != -1 {
			suffix := primaryID[idx:]
			if v, ok := externalIDs["tvdb"]; ok && v != "" {
				add(fmt.Sprintf("tvdb:series:%s%s", v, suffix))
			}
			if v, ok := externalIDs["tmdb"]; ok && v != "" {
				add(fmt.Sprintf("tmdb:tv:%s%s", v, suffix))
			}
			if v, ok := externalIDs["imdb"]; ok && v != "" {
				add(fmt.Sprintf("imdb:%s%s", v, suffix))
			}
		}
	}

	return ids
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

		// Use prefixed IDs to match the player's format (e.g. "tvdb:movie:603")
		// Prefer TVDB > TMDB > IMDB to match the player's primary ID source.
		var itemID string
		if tvdbID, ok := ids["tvdb"]; ok && tvdbID != "" {
			itemID = fmt.Sprintf("tvdb:movie:%s", tvdbID)
		} else if tmdbID, ok := ids["tmdb"]; ok && tmdbID != "" {
			itemID = fmt.Sprintf("tmdb:movie:%s", tmdbID)
		} else if imdbID, ok := ids["imdb"]; ok && imdbID != "" {
			itemID = imdbID
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

// executePrewarm runs the prewarm task to pre-resolve continue watching items
func (s *Service) executePrewarm(task config.ScheduledTask) (SyncResult, error) {
	if s.prewarmService == nil {
		return SyncResult{}, errors.New("prewarm service not configured")
	}

	prewarmResult, err := s.prewarmService.RunOnce(s.ctx)
	if err != nil {
		return SyncResult{}, fmt.Errorf("prewarm failed: %w", err)
	}

	return SyncResult{
		Count:   prewarmResult.Warmed,
		Message: fmt.Sprintf("Warmed %d, skipped %d, failed %d, removed %d", prewarmResult.Warmed, prewarmResult.Skipped, prewarmResult.Failed, prewarmResult.Removed),
	}, nil
}

// executePlexHistorySync imports watch history from Plex into a local profile.
func (s *Service) executePlexHistorySync(task config.ScheduledTask) (SyncResult, error) {
	s.mu.RLock()
	historySvc := s.historyService
	s.mu.RUnlock()

	if historySvc == nil {
		return SyncResult{}, errors.New("history service not configured")
	}

	plexAccountID := task.Config["plexAccountId"]
	profileID, err := s.resolveTaskProfileID(task)

	if plexAccountID == "" || profileID == "" {
		return SyncResult{}, errors.New("missing plexAccountId or profileId in task config")
	}
	if err != nil {
		return SyncResult{}, err
	}

	dryRun := task.Config["dryRun"] == "true"
	if dryRun {
		log.Printf("[scheduler] DRY RUN mode enabled for Plex history sync")
	}

	// Parse optional plexUserId for filtering to a specific Plex Home user
	plexUserID := 0
	if uid := task.Config["plexUserId"]; uid != "" {
		if parsed, parseErr := strconv.Atoi(uid); parseErr == nil {
			plexUserID = parsed
		}
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

	// Fetch all watch history from Plex
	historyItems, err := s.plexClient.GetAllWatchHistory(plexAccount.AuthToken, 5000, plexUserID)
	if err != nil {
		return SyncResult{}, fmt.Errorf("fetch plex history: %w", err)
	}

	log.Printf("[scheduler] Fetched %d Plex history items", len(historyItems))

	result := SyncResult{DryRun: dryRun}
	watched := true
	var updates []models.WatchHistoryUpdate

	for _, item := range historyItems {
		mediaType := plex.NormalizeMediaType(item.Type)
		itemID := item.RatingKey

		// Prefer TMDB then IMDB
		if tmdbID, ok := item.ExternalIDs["tmdb"]; ok && tmdbID != "" {
			itemID = tmdbID
		} else if imdbID, ok := item.ExternalIDs["imdb"]; ok && imdbID != "" {
			itemID = imdbID
		}

		if dryRun {
			name := item.Title
			if item.Type == "episode" && item.GrandparentTitle != "" {
				name = fmt.Sprintf("%s S%02dE%02d", item.GrandparentTitle, item.ParentIndex, item.Index)
			}
			result.ToAdd = append(result.ToAdd, config.DryRunItem{
				Name:      name,
				MediaType: mediaType,
				ID:        itemID,
			})
			continue
		}

		watchedAt := time.Unix(item.ViewedAt, 0).UTC()
		update := models.WatchHistoryUpdate{
			MediaType:   mediaType,
			ItemID:      itemID,
			Name:        item.Title,
			Year:        item.Year,
			Watched:     &watched,
			WatchedAt:   watchedAt,
			ExternalIDs: item.ExternalIDs,
		}

		if item.Type == "episode" {
			update.SeasonNumber = item.ParentIndex
			update.EpisodeNumber = item.Index
			update.SeriesName = item.GrandparentTitle
		}

		updates = append(updates, update)
	}

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
		log.Printf("[scheduler] Imported %d/%d Plex history items", imported, len(updates))
	}

	return result, nil
}

// executeJellyfinFavoritesSync syncs Jellyfin favorites to a local watchlist.
func (s *Service) executeJellyfinFavoritesSync(task config.ScheduledTask) (SyncResult, error) {
	s.mu.RLock()
	jfClient := s.jellyfinClient
	s.mu.RUnlock()

	if jfClient == nil {
		return SyncResult{}, errors.New("jellyfin client not configured")
	}

	jellyfinAccountID := task.Config["jellyfinAccountId"]
	profileID, err := s.resolveTaskProfileID(task)

	if jellyfinAccountID == "" || profileID == "" {
		return SyncResult{}, errors.New("missing jellyfinAccountId or profileId in task config")
	}
	if err != nil {
		return SyncResult{}, err
	}

	syncDirection := task.Config["syncDirection"]
	if syncDirection == "" {
		syncDirection = "source_to_target"
	}
	deleteBehavior := task.Config["deleteBehavior"]
	if deleteBehavior == "" {
		deleteBehavior = "additive"
	}
	dryRun := task.Config["dryRun"] == "true"

	if dryRun {
		log.Printf("[scheduler] DRY RUN mode enabled for Jellyfin favorites sync")
	}

	// Load settings to get Jellyfin account
	settings, err := s.configManager.Load()
	if err != nil {
		return SyncResult{}, fmt.Errorf("load settings: %w", err)
	}

	jfAccount := settings.Jellyfin.GetAccountByID(jellyfinAccountID)
	if jfAccount == nil {
		return SyncResult{}, errors.New("jellyfin account not found")
	}
	if jfAccount.Token == "" {
		return SyncResult{}, errors.New("jellyfin account not authenticated")
	}

	// Only source_to_target is supported for Jellyfin favorites
	if syncDirection != "source_to_target" {
		return SyncResult{}, fmt.Errorf("unsupported sync direction for Jellyfin favorites: %s (only source_to_target supported)", syncDirection)
	}

	// Fetch favorites from Jellyfin
	items, err := jfClient.GetFavorites(jfAccount.ServerURL, jfAccount.Token, jfAccount.UserID)
	if err != nil {
		return SyncResult{}, fmt.Errorf("fetch jellyfin favorites: %w", err)
	}

	log.Printf("[scheduler] Fetched %d Jellyfin favorites", len(items))

	now := time.Now().UTC()
	result := SyncResult{DryRun: dryRun}
	syncSource := fmt.Sprintf("jellyfin:%s:%s", jellyfinAccountID, task.ID)

	// Build set of Jellyfin item keys for deletion checking
	jfItemKeys := make(map[string]bool)

	// Get existing local items to check what's new
	existingItems, _ := s.watchlistService.List(profileID)
	existingKeys := make(map[string]bool)
	for _, item := range existingItems {
		for _, key := range schedulerWatchlistMatchKeys(item.MediaType, item.ID, item.ExternalIDs) {
			existingKeys[key] = true
		}
	}

	imported := 0
	for _, item := range items {
		mediaType := jellyfin.NormalizeMediaType(item.Type)

		// Prefer TMDB then IMDB then Jellyfin ID
		itemID := item.ID
		if tmdbID, ok := item.ProviderIDs["tmdb"]; ok && tmdbID != "" {
			itemID = tmdbID
		} else if imdbID, ok := item.ProviderIDs["imdb"]; ok && imdbID != "" {
			itemID = imdbID
		}

		for _, key := range schedulerWatchlistMatchKeys(mediaType, itemID, item.ProviderIDs) {
			jfItemKeys[key] = true
		}

		isNew := !schedulerWatchlistHasAnyKey(existingKeys, mediaType, itemID, item.ProviderIDs)

		if dryRun {
			if isNew {
				log.Printf("[scheduler] DRY RUN: Would import from Jellyfin: %s (%s)", item.Name, mediaType)
				result.ToAdd = append(result.ToAdd, config.DryRunItem{
					Name:      item.Name,
					MediaType: mediaType,
					ID:        itemID,
				})
			}
			imported++
			continue
		}

		extIDs := item.ProviderIDs
		if extIDs == nil {
			extIDs = map[string]string{}
		}
		extIDs["jellyfin"] = item.ID

		input := models.WatchlistUpsert{
			ID:          itemID,
			MediaType:   mediaType,
			Name:        item.Name,
			Year:        item.Year,
			ExternalIDs: extIDs,
			SyncSource:  syncSource,
			SyncedAt:    &now,
		}

		if _, err := s.watchlistService.AddOrUpdate(profileID, input); err != nil {
			log.Printf("[scheduler] Failed to import Jellyfin favorite %s: %v", item.Name, err)
			continue
		}

		imported++
	}

	// Handle deletions
	if deleteBehavior != "additive" {
		removed := 0
		localItems, err := s.watchlistService.List(profileID)
		if err != nil {
			log.Printf("[scheduler] Failed to list local items for deletion check: %v", err)
		} else {
			for _, localItem := range localItems {
				if schedulerWatchlistHasAnyKey(jfItemKeys, localItem.MediaType, localItem.ID, localItem.ExternalIDs) {
					continue
				}
				if deleteBehavior == "delete" && localItem.SyncSource != syncSource {
					continue
				}

				if dryRun {
					result.ToRemove = append(result.ToRemove, config.DryRunItem{
						Name:      localItem.Name,
						MediaType: localItem.MediaType,
						ID:        localItem.ID,
					})
					removed++
					continue
				}

				if ok, err := s.watchlistService.Remove(profileID, localItem.MediaType, localItem.ID); err != nil {
					log.Printf("[scheduler] Failed to remove watchlist item %s: %v", localItem.Name, err)
				} else if ok {
					removed++
				}
			}
		}
		if removed > 0 {
			log.Printf("[scheduler] Removed %d items no longer in Jellyfin favorites", removed)
		}
	}

	result.Count = imported
	return result, nil
}

// executeJellyfinHistorySync imports watch history from Jellyfin into a local profile.
func (s *Service) executeJellyfinHistorySync(task config.ScheduledTask) (SyncResult, error) {
	s.mu.RLock()
	historySvc := s.historyService
	jfClient := s.jellyfinClient
	s.mu.RUnlock()

	if historySvc == nil {
		return SyncResult{}, errors.New("history service not configured")
	}
	if jfClient == nil {
		return SyncResult{}, errors.New("jellyfin client not configured")
	}

	jellyfinAccountID := task.Config["jellyfinAccountId"]
	profileID, err := s.resolveTaskProfileID(task)

	if jellyfinAccountID == "" || profileID == "" {
		return SyncResult{}, errors.New("missing jellyfinAccountId or profileId in task config")
	}
	if err != nil {
		return SyncResult{}, err
	}

	dryRun := task.Config["dryRun"] == "true"
	if dryRun {
		log.Printf("[scheduler] DRY RUN mode enabled for Jellyfin history sync")
	}

	// Load settings to get Jellyfin account
	settings, err := s.configManager.Load()
	if err != nil {
		return SyncResult{}, fmt.Errorf("load settings: %w", err)
	}

	jfAccount := settings.Jellyfin.GetAccountByID(jellyfinAccountID)
	if jfAccount == nil {
		return SyncResult{}, errors.New("jellyfin account not found")
	}
	if jfAccount.Token == "" {
		return SyncResult{}, errors.New("jellyfin account not authenticated")
	}

	// Fetch watch history from Jellyfin
	items, err := jfClient.GetWatchHistory(jfAccount.ServerURL, jfAccount.Token, jfAccount.UserID)
	if err != nil {
		return SyncResult{}, fmt.Errorf("fetch jellyfin history: %w", err)
	}

	log.Printf("[scheduler] Fetched %d Jellyfin history items", len(items))

	result := SyncResult{DryRun: dryRun}
	watched := true
	var updates []models.WatchHistoryUpdate

	for _, item := range items {
		mediaType := jellyfin.NormalizeMediaType(item.Type)

		// Prefer TMDB then IMDB then Jellyfin ID
		itemID := item.ID
		if tmdbID, ok := item.ProviderIDs["tmdb"]; ok && tmdbID != "" {
			itemID = tmdbID
		} else if imdbID, ok := item.ProviderIDs["imdb"]; ok && imdbID != "" {
			itemID = imdbID
		}

		if dryRun {
			name := item.Name
			if item.Type == "Episode" && item.SeriesName != "" {
				name = fmt.Sprintf("%s S%02dE%02d", item.SeriesName, item.SeasonNum, item.EpisodeNum)
			}
			result.ToAdd = append(result.ToAdd, config.DryRunItem{
				Name:      name,
				MediaType: mediaType,
				ID:        itemID,
			})
			continue
		}

		watchedAt := time.Now().UTC()
		if item.DatePlayed != nil {
			watchedAt = *item.DatePlayed
		}

		extIDs := item.ProviderIDs
		if extIDs == nil {
			extIDs = map[string]string{}
		}
		extIDs["jellyfin"] = item.ID

		update := models.WatchHistoryUpdate{
			MediaType:   mediaType,
			ItemID:      itemID,
			Name:        item.Name,
			Year:        item.Year,
			Watched:     &watched,
			WatchedAt:   watchedAt,
			ExternalIDs: extIDs,
		}

		if item.Type == "Episode" {
			update.SeasonNumber = item.SeasonNum
			update.EpisodeNumber = item.EpisodeNum
			update.SeriesName = item.SeriesName
		}

		updates = append(updates, update)
	}

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
		log.Printf("[scheduler] Imported %d/%d Jellyfin history items", imported, len(updates))
	}

	return result, nil
}

// executeMDBListWatchlistSync imports an MDBList user's watchlist to local.
func (s *Service) executeMDBListWatchlistSync(task config.ScheduledTask) (SyncResult, error) {
	mdblistAccountID := task.Config["mdblistAccountId"]
	profileID, err := s.resolveTaskProfileID(task)

	if mdblistAccountID == "" || profileID == "" {
		return SyncResult{}, errors.New("missing mdblistAccountId or profileId in task config")
	}
	if err != nil {
		return SyncResult{}, err
	}

	syncDirection := task.Config["syncDirection"]
	if syncDirection == "" {
		syncDirection = "source_to_target"
	}
	deleteBehavior := task.Config["deleteBehavior"]
	if deleteBehavior == "" {
		deleteBehavior = "additive"
	}
	dryRun := task.Config["dryRun"] == "true"

	settings, err := s.configManager.Load()
	if err != nil {
		return SyncResult{}, fmt.Errorf("load settings: %w", err)
	}

	mdblistAccount := settings.MDBList.GetAccountByID(mdblistAccountID)
	if mdblistAccount == nil {
		return SyncResult{}, errors.New("MDBList account not found")
	}

	if mdblistAccount.APIKey == "" {
		return SyncResult{}, errors.New("MDBList account has no API key")
	}

	syncSource := fmt.Sprintf("mdblist:%s:%s", mdblistAccountID, task.ID)

	return s.syncMDBListWatchlistToLocal(mdblistAccount, profileID, syncSource, deleteBehavior, dryRun)
}

func (s *Service) syncMDBListWatchlistToLocal(account *config.MDBListAccount, profileID, syncSource, deleteBehavior string, dryRun bool) (SyncResult, error) {
	now := time.Now().UTC()
	result := SyncResult{DryRun: dryRun}

	// Fetch watchlist from MDBList API (/watchlist/items)
	url := fmt.Sprintf("https://api.mdblist.com/watchlist/items?apikey=%s", account.APIKey)
	httpClient := &http.Client{Timeout: 30 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "mediastorm/1.0")
	resp, err := httpClient.Do(req)
	if err != nil {
		return result, fmt.Errorf("fetch MDBList watchlist: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return result, fmt.Errorf("MDBList /watchlist/items returned %d", resp.StatusCode)
	}

	type mdblistWatchlistItem struct {
		Title       string `json:"title"`
		ReleaseYear int    `json:"release_year"`
		MediaType   string `json:"mediatype"` // "movie" or "show"
		IMDBID      string `json:"imdb_id"`
		IDs         struct {
			IMDB string `json:"imdb"`
			TMDB int    `json:"tmdb"`
			TVDB *int   `json:"tvdb"` // nullable
		} `json:"ids"`
	}

	var watchlistResp struct {
		Movies []mdblistWatchlistItem `json:"movies"`
		Shows  []mdblistWatchlistItem `json:"shows"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&watchlistResp); err != nil {
		return result, fmt.Errorf("decode MDBList watchlist: %w", err)
	}

	// Combine movies and shows into a single list
	var items []mdblistWatchlistItem
	for i := range watchlistResp.Movies {
		watchlistResp.Movies[i].MediaType = "movie"
		items = append(items, watchlistResp.Movies[i])
	}
	for i := range watchlistResp.Shows {
		watchlistResp.Shows[i].MediaType = "show"
		items = append(items, watchlistResp.Shows[i])
	}

	log.Printf("[scheduler] Fetched %d items from MDBList watchlist (%d movies, %d shows)",
		len(items), len(watchlistResp.Movies), len(watchlistResp.Shows))

	// Build set of MDBList item keys for deletion checking
	mdblistItemKeys := make(map[string]bool)

	// Build existing keys index using all stable identifiers for cross-ID matching.
	existingItems, _ := s.watchlistService.List(profileID)
	existingKeys := make(map[string]bool)
	for _, item := range existingItems {
		for _, key := range schedulerWatchlistMatchKeys(item.MediaType, item.ID, item.ExternalIDs) {
			existingKeys[key] = true
		}
	}

	imported := 0
	for _, item := range items {
		// Map MDBList mediatype to internal format
		mediaType := "movie"
		if item.MediaType == "show" {
			mediaType = "series"
		}

		// Build item ID and external IDs from the nested ids object
		var itemID string
		extIDs := make(map[string]string)
		if item.IDs.IMDB != "" {
			extIDs["imdb"] = item.IDs.IMDB
			itemID = item.IDs.IMDB
		}
		if item.IDs.TMDB != 0 {
			extIDs["tmdb"] = strconv.Itoa(item.IDs.TMDB)
			if itemID == "" {
				itemID = strconv.Itoa(item.IDs.TMDB)
			}
		}
		if item.IDs.TVDB != nil && *item.IDs.TVDB != 0 {
			extIDs["tvdb"] = strconv.Itoa(*item.IDs.TVDB)
			if itemID == "" {
				itemID = strconv.Itoa(*item.IDs.TVDB)
			}
		}

		if itemID == "" {
			continue
		}

		for _, key := range schedulerWatchlistMatchKeys(mediaType, itemID, extIDs) {
			mdblistItemKeys[key] = true
		}
		isNew := !schedulerWatchlistHasAnyKey(existingKeys, mediaType, itemID, extIDs)

		if dryRun {
			if isNew {
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
			Year:        item.ReleaseYear,
			ExternalIDs: extIDs,
			SyncSource:  syncSource,
			SyncedAt:    &now,
		}

		if _, err := s.watchlistService.AddOrUpdate(profileID, input); err != nil {
			log.Printf("[scheduler] Failed to import MDBList watchlist item %s: %v", item.Title, err)
			continue
		}
		imported++
	}

	// Handle deletions for delete/mirror modes
	if deleteBehavior != "additive" && !dryRun {
		localItems, err := s.watchlistService.List(profileID)
		if err == nil {
			for _, item := range localItems {
				if item.SyncSource == syncSource && !schedulerWatchlistHasAnyKey(mdblistItemKeys, item.MediaType, item.ID, item.ExternalIDs) {
					if _, err := s.watchlistService.Remove(profileID, item.MediaType, item.ID); err != nil {
						log.Printf("[scheduler] Failed to remove stale MDBList watchlist item: %v", err)
					}
				}
			}
		}
	}

	result.Count = imported
	log.Printf("[scheduler] Imported %d items from MDBList watchlist", imported)

	return result, nil
}

func schedulerWatchlistHasAnyKey(keys map[string]bool, mediaType, id string, externalIDs map[string]string) bool {
	for _, key := range schedulerWatchlistMatchKeys(mediaType, id, externalIDs) {
		if keys[key] {
			return true
		}
	}
	return false
}

func schedulerWatchlistMatchKeys(mediaType, id string, externalIDs map[string]string) []string {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	id = strings.TrimSpace(id)
	externalIDs = schedulerNormalizeExternalIDs(externalIDs)

	keys := make([]string, 0, 8)
	seen := make(map[string]bool)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		key := mediaType + ":" + value
		if seen[key] {
			return
		}
		seen[key] = true
		keys = append(keys, key)
	}

	add(id)
	switch mediaType {
	case "movie":
		if v := externalIDs["tvdb"]; v != "" {
			add("tvdb:movie:" + v)
			add(v)
		}
		if v := externalIDs["tmdb"]; v != "" {
			add("tmdb:movie:" + v)
			add(v)
		}
	case "series":
		if v := externalIDs["tvdb"]; v != "" {
			add("tvdb:series:" + v)
			add(v)
		}
		if v := externalIDs["tmdb"]; v != "" {
			add("tmdb:tv:" + v)
			add(v)
		}
	}
	if v := externalIDs["imdb"]; v != "" {
		add(v)
		add("imdb:" + v)
	}
	return keys
}

func schedulerNormalizeExternalIDs(externalIDs map[string]string) map[string]string {
	if len(externalIDs) == 0 {
		return nil
	}
	out := make(map[string]string, len(externalIDs))
	for key, value := range externalIDs {
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// executeMDBListHistorySync syncs watch history between MDBList and local.
func (s *Service) executeMDBListHistorySync(task config.ScheduledTask) (SyncResult, error) {
	s.mu.RLock()
	historySvc := s.historyService
	s.mu.RUnlock()

	if historySvc == nil {
		return SyncResult{}, errors.New("history service not configured")
	}

	mdblistAccountID := task.Config["mdblistAccountId"]
	profileID, err := s.resolveTaskProfileID(task)

	if mdblistAccountID == "" || profileID == "" {
		return SyncResult{}, errors.New("missing mdblistAccountId or profileId in task config")
	}
	if err != nil {
		return SyncResult{}, err
	}

	syncDirection := task.Config["syncDirection"]
	if syncDirection == "" {
		syncDirection = "mdblist_to_local"
	}
	dryRun := task.Config["dryRun"] == "true"

	// Load settings to get MDBList account
	settings, err := s.configManager.Load()
	if err != nil {
		return SyncResult{}, fmt.Errorf("load settings: %w", err)
	}

	mdblistAccount := settings.MDBList.GetAccountByID(mdblistAccountID)
	if mdblistAccount == nil {
		return SyncResult{}, errors.New("MDBList account not found")
	}

	if mdblistAccount.APIKey == "" {
		return SyncResult{}, errors.New("MDBList account has no API key")
	}

	switch syncDirection {
	case "mdblist_to_local":
		return s.syncMDBListHistoryToLocal(task, mdblistAccount, profileID, dryRun)
	case "local_to_mdblist":
		return s.syncLocalHistoryToMDBList(task, mdblistAccount, profileID, dryRun)
	case "bidirectional":
		// Import first, then export
		importResult, err := s.syncMDBListHistoryToLocal(task, mdblistAccount, profileID, dryRun)
		if err != nil {
			return importResult, err
		}
		exportResult, err := s.syncLocalHistoryToMDBList(task, mdblistAccount, profileID, dryRun)
		if err != nil {
			return exportResult, err
		}
		return SyncResult{
			Count:  importResult.Count + exportResult.Count,
			DryRun: dryRun,
			ToAdd:  append(importResult.ToAdd, exportResult.ToAdd...),
		}, nil
	default:
		return SyncResult{}, fmt.Errorf("unknown sync direction: %s", syncDirection)
	}
}

// syncMDBListHistoryToLocal imports watch history from MDBList into local.
func (s *Service) syncMDBListHistoryToLocal(task config.ScheduledTask, account *config.MDBListAccount, profileID string, dryRun bool) (SyncResult, error) {
	result := SyncResult{DryRun: dryRun}

	s.mu.RLock()
	historySvc := s.historyService
	s.mu.RUnlock()

	// Determine incremental cursor
	var since string
	if task.LastRunAt != nil {
		since = task.LastRunAt.Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	}

	// Fetch watched history from MDBList API with pagination
	apiKey := account.APIKey
	var allMovies []json.RawMessage
	var allEpisodes []json.RawMessage
	offset := 0
	limit := 500

	for {
		url := fmt.Sprintf("https://api.mdblist.com/sync/watched?apikey=%s&limit=%d&offset=%d", apiKey, limit, offset)
		if since != "" {
			url += "&since=" + since
		}

		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("User-Agent", "mediastorm/1.0")
		httpClient := &http.Client{Timeout: 30 * time.Second}
		resp, err := httpClient.Do(req)
		if err != nil {
			return result, fmt.Errorf("fetch MDBList history: %w", err)
		}

		var page struct {
			Movies     []json.RawMessage `json:"movies"`
			Episodes   []json.RawMessage `json:"episodes"`
			Pagination struct {
				HasMore bool `json:"has_more"`
			} `json:"pagination"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			resp.Body.Close()
			return result, fmt.Errorf("decode MDBList history: %w", err)
		}
		resp.Body.Close()

		allMovies = append(allMovies, page.Movies...)
		allEpisodes = append(allEpisodes, page.Episodes...)

		if !page.Pagination.HasMore {
			break
		}
		offset += limit
	}

	log.Printf("[scheduler] Fetched %d movies + %d episodes from MDBList watch history", len(allMovies), len(allEpisodes))

	// Convert to WatchHistoryUpdate items
	watched := true
	var updates []models.WatchHistoryUpdate

	// Parse movies
	for _, raw := range allMovies {
		var m struct {
			LastWatchedAt string `json:"last_watched_at"`
			Movie         struct {
				Title string `json:"title"`
				Year  int    `json:"year"`
				IDs   struct {
					IMDB string `json:"imdb"`
					TMDB int    `json:"tmdb"`
					TVDB int    `json:"tvdb"`
				} `json:"ids"`
			} `json:"movie"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}

		watchedAt, _ := time.Parse(time.RFC3339, m.LastWatchedAt)
		extIDs := make(map[string]string)
		var itemID string
		if m.Movie.IDs.IMDB != "" {
			extIDs["imdb"] = m.Movie.IDs.IMDB
			itemID = m.Movie.IDs.IMDB
		}
		if m.Movie.IDs.TMDB != 0 {
			extIDs["tmdb"] = strconv.Itoa(m.Movie.IDs.TMDB)
			if itemID == "" {
				itemID = strconv.Itoa(m.Movie.IDs.TMDB)
			}
		}
		if itemID == "" {
			continue
		}

		if dryRun {
			result.ToAdd = append(result.ToAdd, config.DryRunItem{
				Name:      m.Movie.Title,
				MediaType: "movie",
				ID:        itemID,
			})
			continue
		}

		updates = append(updates, models.WatchHistoryUpdate{
			MediaType:   "movie",
			ItemID:      itemID,
			Name:        m.Movie.Title,
			Year:        m.Movie.Year,
			Watched:     &watched,
			WatchedAt:   watchedAt,
			ExternalIDs: extIDs,
		})
	}

	// Parse episodes
	for _, raw := range allEpisodes {
		var e struct {
			LastWatchedAt string `json:"last_watched_at"`
			Episode       struct {
				Season int    `json:"season"`
				Number int    `json:"number"`
				Name   string `json:"name"`
				Show   struct {
					Title string `json:"title"`
					Year  int    `json:"year"`
					IDs   struct {
						IMDB string `json:"imdb"`
						TMDB int    `json:"tmdb"`
						TVDB int    `json:"tvdb"`
					} `json:"ids"`
				} `json:"show"`
			} `json:"episode"`
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			continue
		}

		watchedAt, _ := time.Parse(time.RFC3339, e.LastWatchedAt)
		extIDs := make(map[string]string)
		var seriesID string

		show := e.Episode.Show
		if show.IDs.TVDB != 0 {
			extIDs["tvdb"] = strconv.Itoa(show.IDs.TVDB)
			seriesID = fmt.Sprintf("tvdb:series:%d", show.IDs.TVDB)
		}
		if show.IDs.TMDB != 0 {
			extIDs["tmdb"] = strconv.Itoa(show.IDs.TMDB)
			if seriesID == "" {
				seriesID = fmt.Sprintf("tmdb:tv:%d", show.IDs.TMDB)
			}
		}
		if show.IDs.IMDB != "" {
			extIDs["imdb"] = show.IDs.IMDB
		}
		if seriesID == "" {
			continue
		}

		itemID := fmt.Sprintf("%s:s%02de%02d", seriesID, e.Episode.Season, e.Episode.Number)

		if dryRun {
			result.ToAdd = append(result.ToAdd, config.DryRunItem{
				Name:      fmt.Sprintf("%s S%02dE%02d", show.Title, e.Episode.Season, e.Episode.Number),
				MediaType: "episode",
				ID:        itemID,
			})
			continue
		}

		updates = append(updates, models.WatchHistoryUpdate{
			MediaType:     "episode",
			ItemID:        itemID,
			Name:          e.Episode.Name,
			Watched:       &watched,
			WatchedAt:     watchedAt,
			ExternalIDs:   extIDs,
			SeasonNumber:  e.Episode.Season,
			EpisodeNumber: e.Episode.Number,
			SeriesID:      seriesID,
			SeriesName:    show.Title,
		})
	}

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
		log.Printf("[scheduler] Imported %d/%d items from MDBList history", imported, len(updates))
	}

	return result, nil
}

// syncLocalHistoryToMDBList exports local watch history to MDBList.
func (s *Service) syncLocalHistoryToMDBList(task config.ScheduledTask, account *config.MDBListAccount, profileID string, dryRun bool) (SyncResult, error) {
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

	var toSync []models.WatchHistoryItem
	for _, item := range items {
		if item.Watched {
			if since.IsZero() || item.WatchedAt.After(since) {
				toSync = append(toSync, item)
			}
		}
	}

	log.Printf("[scheduler] Found %d watched items to sync to MDBList (since %v)", len(toSync), since)

	if len(toSync) == 0 {
		return result, nil
	}

	if dryRun {
		for _, item := range toSync {
			result.ToAdd = append(result.ToAdd, config.DryRunItem{
				Name:      item.Name,
				MediaType: item.MediaType,
				ID:        item.ItemID,
			})
		}
		result.Count = len(result.ToAdd)
		return result, nil
	}

	// Build MDBList sync requests batched by type.
	// MDBList format: movies=[{"ids":{...}, "watched_at":"..."}],
	//                 shows=[{"ids":{...}, "seasons":[{"number":N, "episodes":[{"number":N, "watched_at":"..."}]}]}]
	apiKey := account.APIKey

	// Collect movies
	var moviePayloads []map[string]interface{}
	for _, item := range toSync {
		if item.MediaType != "movie" {
			continue
		}
		ids := extractMDBListIDs(item.ExternalIDs)
		if ids.imdb == "" && ids.tmdb == 0 {
			continue
		}
		m := map[string]interface{}{
			"ids": formatMDBListIDsMap(ids),
		}
		if !item.WatchedAt.IsZero() {
			m["watched_at"] = item.WatchedAt.UTC().Format(time.RFC3339)
		}
		moviePayloads = append(moviePayloads, m)
	}

	// Collect episodes grouped by show
	type showKey struct {
		imdb string
		tmdb int
	}
	type epEntry struct {
		season    int
		episode   int
		watchedAt time.Time
	}
	showMap := make(map[showKey][]epEntry)
	showOrder := make([]showKey, 0)
	for _, item := range toSync {
		if item.MediaType != "episode" {
			continue
		}
		ids := extractMDBListIDs(item.ExternalIDs)
		if ids.imdb == "" && ids.tmdb == 0 {
			continue
		}
		key := showKey{imdb: ids.imdb, tmdb: ids.tmdb}
		if _, exists := showMap[key]; !exists {
			showOrder = append(showOrder, key)
		}
		showMap[key] = append(showMap[key], epEntry{
			season:    item.SeasonNumber,
			episode:   item.EpisodeNumber,
			watchedAt: item.WatchedAt,
		})
	}

	var showPayloads []map[string]interface{}
	for _, key := range showOrder {
		eps := showMap[key]
		ids := mdblistIDs{imdb: key.imdb, tmdb: key.tmdb}

		// Group episodes by season
		seasonMap := make(map[int][]map[string]interface{})
		for _, ep := range eps {
			epObj := map[string]interface{}{
				"number": ep.episode,
			}
			if !ep.watchedAt.IsZero() {
				epObj["watched_at"] = ep.watchedAt.UTC().Format(time.RFC3339)
			}
			seasonMap[ep.season] = append(seasonMap[ep.season], epObj)
		}

		var seasons []map[string]interface{}
		for sNum, sEps := range seasonMap {
			seasons = append(seasons, map[string]interface{}{
				"number":   sNum,
				"episodes": sEps,
			})
		}

		showPayloads = append(showPayloads, map[string]interface{}{
			"ids":     formatMDBListIDsMap(ids),
			"seasons": seasons,
		})
	}

	// Send batched request
	syncCount := 0
	batchSize := 100

	// Sync movies in batches
	for i := 0; i < len(moviePayloads); i += batchSize {
		end := i + batchSize
		if end > len(moviePayloads) {
			end = len(moviePayloads)
		}
		batch := map[string]interface{}{
			"movies": moviePayloads[i:end],
		}
		body, _ := json.Marshal(batch)
		if err := postToMDBList(apiKey, "/sync/watched", string(body)); err != nil {
			log.Printf("[scheduler] MDBList sync movies batch error: %v", err)
			continue
		}
		syncCount += end - i
	}

	// Sync shows in batches
	for i := 0; i < len(showPayloads); i += batchSize {
		end := i + batchSize
		if end > len(showPayloads) {
			end = len(showPayloads)
		}
		batch := map[string]interface{}{
			"shows": showPayloads[i:end],
		}
		body, _ := json.Marshal(batch)
		if err := postToMDBList(apiKey, "/sync/watched", string(body)); err != nil {
			log.Printf("[scheduler] MDBList sync shows batch error: %v", err)
			continue
		}
		for _, show := range showPayloads[i:end] {
			for _, season := range show["seasons"].([]map[string]interface{}) {
				syncCount += len(season["episodes"].([]map[string]interface{}))
			}
		}
	}

	result.Count = syncCount
	log.Printf("[scheduler] Synced %d/%d items to MDBList", syncCount, len(toSync))

	return result, nil
}

type mdblistIDs struct {
	imdb string
	tmdb int
}

func extractMDBListIDs(extIDs map[string]string) mdblistIDs {
	var ids mdblistIDs
	if v, ok := extIDs["imdb"]; ok {
		ids.imdb = v
	}
	if v, ok := extIDs["tmdb"]; ok {
		ids.tmdb, _ = strconv.Atoi(v)
	}
	// Note: MDBList does not support TVDB IDs
	return ids
}

func formatMDBListIDsMap(ids mdblistIDs) map[string]interface{} {
	m := make(map[string]interface{})
	if ids.imdb != "" {
		m["imdb"] = ids.imdb
	}
	if ids.tmdb != 0 {
		m["tmdb"] = ids.tmdb
	}
	return m
}

func postToMDBList(apiKey, path, body string) error {
	url := fmt.Sprintf("https://api.mdblist.com%s?apikey=%s", path, apiKey)
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("mdblist %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("mdblist %s returned %d: %s", path, resp.StatusCode, string(respBody))
	}
	return nil
}
