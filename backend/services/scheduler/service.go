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
	"novastream/internal/mediaidentity"
	"novastream/models"
	"novastream/services/backup"
	"novastream/services/epg"
	"novastream/services/history"
	"novastream/services/jellyfin"
	"novastream/services/localmedia"
	"novastream/services/plex"
	"novastream/services/prewarm"
	"novastream/services/scrob"
	"novastream/services/simkl"
	"novastream/services/trakt"
	"novastream/services/watchlist"
)

// Service manages scheduled task execution
type Service struct {
	configManager      *config.Manager
	plexClient         *plex.Client
	traktClient        *trakt.Client
	simklClient        *simkl.Client
	scrobClient        *scrob.Client
	jellyfinClient     *jellyfin.Client
	usersService       schedulerUsersProvider
	watchlistService   *watchlist.Service
	epgService         *epg.Service
	backupService      *backup.Service
	historyService     *history.Service
	metadataService    schedulerMetadataService
	prewarmService     *prewarm.Service
	localMediaService  localMediaScanner
	livePlaylistWarmer livePlaylistWarmer

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

	// Remote history key sets from previous bidirectional history syncs.
	historySnapshots historySyncSnapshotStore
}

type schedulerUsersProvider interface {
	Exists(id string) bool
	ListAll() []models.User
}

type localMediaScanner interface {
	ListLibraries(ctx context.Context) ([]models.LocalMediaLibrary, error)
	// RunScan runs a library scan to completion (synchronous).
	RunScan(ctx context.Context, libraryID string) (models.LocalMediaScanSummary, error)
}

type schedulerMetadataService interface {
	SeriesDetailsLite(ctx context.Context, req models.SeriesDetailsQuery) (*models.SeriesDetails, error)
	// Artwork warming for watchlist items imported without posters.
	GetTextPosterURL(mediaType string, tmdbID int64, tvdbID int64) string
	MovieDetails(ctx context.Context, req models.MovieDetailsQuery) (*models.Title, error)
	SeriesDetails(ctx context.Context, req models.SeriesDetailsQuery) (*models.SeriesDetails, error)
}

type livePlaylistWarmer interface {
	WarmPlaylistCache(ctx context.Context) (int, error)
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

type traktHistoryState struct {
	watchedAt time.Time
}

const (
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
	wasRunning := s.running
	cancel := s.cancel
	if wasRunning {
		s.running = false
		s.ctx = nil
		s.cancel = nil
	}
	s.mu.Unlock()

	if wasRunning && cancel != nil {
		cancel()
	}

	// Wait for all tasks to complete with timeout
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		if wasRunning {
			log.Println("[scheduler] Scheduler service stopped gracefully")
		}
	case <-ctx.Done():
		log.Println("[scheduler] Scheduler service stopped (timeout)")
	}

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
			s.startTask(task)
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

	if task.Type == config.ScheduledTaskTypeEPGRefresh && task.Frequency != config.ScheduledTaskFrequencyOnce {
		s.mu.RLock()
		guide := s.epgService
		s.mu.RUnlock()
		if guide != nil && guide.HDHomeRunRefreshDue() {
			return true
		}
	}
	interval := s.getInterval(task.Type, task.Frequency)
	return time.Since(*task.LastRunAt) >= interval
}

// getInterval returns the duration for a given task type and frequency.
// Prewarm tasks run often enough to renew the shortest 15-minute dynamic TTL
// before cleanup removes it.
func (s *Service) getInterval(taskType config.ScheduledTaskType, freq config.ScheduledTaskFrequency) time.Duration {
	if taskType == config.ScheduledTaskTypePrewarm {
		return 10 * time.Minute
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

func (s *Service) SetLivePlaylistWarmer(warmer livePlaylistWarmer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.livePlaylistWarmer = warmer
}

// Reserve the task before launching its goroutine. Manual and scheduled starts
// can race; only one may own the running slot and publish its final result.
func (s *Service) startTask(task config.ScheduledTask) bool {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if s.taskRunning[task.ID] {
		return false
	}
	s.taskRunning[task.ID] = true
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.executeTask(task)
	}()
	return true
}

// executeTask runs an already reserved task and updates its status.
func (s *Service) executeTask(task config.ScheduledTask) {
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
	case config.ScheduledTaskTypeSimklWatchlistSync:
		result, err = s.executeSimklWatchlistSync(task)
	case config.ScheduledTaskTypeScrobHistorySync:
		result, err = s.executeScrobHistorySync(task)
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

	// After a successful watchlist sync, enrich items that were imported without
	// artwork. External sources (Plex/Trakt/MDBList/Jellyfin) provide only IDs,
	// so the metadata cache is cold and thumbnails stay blank until warmed. This
	// makes re-running the sync populate posters without a manual remove/re-add.
	if err == nil && isWatchlistSyncTask(task.Type) {
		s.enrichSyncedWatchlistArtwork(task)
	}
}

// isWatchlistSyncTask reports whether a task imports items into the local
// watchlist (and therefore may need artwork enrichment afterwards).
func isWatchlistSyncTask(t config.ScheduledTaskType) bool {
	switch t {
	case config.ScheduledTaskTypePlexWatchlistSync,
		config.ScheduledTaskTypeTraktListSync,
		config.ScheduledTaskTypeMDBListWatchlistSync,
		config.ScheduledTaskTypeSimklWatchlistSync,
		config.ScheduledTaskTypeJellyfinFavoritesSync:
		return true
	default:
		return false
	}
}

// enrichSyncedWatchlistArtwork warms the metadata cache and persists artwork for
// the synced profile's watchlist items that arrived without a poster.
func (s *Service) enrichSyncedWatchlistArtwork(task config.ScheduledTask) {
	s.mu.RLock()
	meta := s.metadataService
	s.mu.RUnlock()
	if meta == nil || s.watchlistService == nil {
		return
	}
	profileID, err := s.resolveTaskProfileID(task)
	if err != nil {
		return
	}
	if n := s.watchlistService.EnrichMissingArtwork([]string{profileID}, meta); n > 0 {
		log.Printf("[scheduler] Enriched artwork for %d watchlist items after %s", n, task.Type)
	}
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
			summary, err := s.localMediaService.RunScan(context.Background(), library.ID)
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

	summary, err := s.localMediaService.RunScan(context.Background(), libraryID)
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
			if err == nil && !result.DryRun && result.Config != nil {
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
					log.Printf("[scheduler] Task %s completed successfully, processed %d changes", taskID, result.Count)
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
func (s *Service) RunTaskNow(taskID string, fullSync ...bool) error {
	settings, err := s.configManager.Load()
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}

	for _, task := range settings.ScheduledTasks.Tasks {
		if task.ID == taskID {
			if len(fullSync) > 0 && fullSync[0] {
				var err error
				task, err = prepareFullSync(task)
				if err != nil {
					return err
				}
			}
			if !s.startTask(task) {
				return errors.New("task is already running")
			}
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
			tasks[i].LastError = ""
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

// SetMetadataService sets the metadata service for scheduled history sync tasks.
func (s *Service) SetMetadataService(metadataService schedulerMetadataService) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metadataService = metadataService
}

// SetSimklClient sets the Simkl client for scheduled Simkl sync tasks.
func (s *Service) SetSimklClient(simklClient *simkl.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.simklClient = simklClient
}

// SetScrobClient sets the client used for self-hosted Scrob history tasks.
func (s *Service) SetScrobClient(scrobClient *scrob.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scrobClient = scrobClient
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
	s.usersService = usersService
	s.mu.Unlock()

	if err := s.migrateLegacyDefaultTaskProfiles(); err != nil {
		log.Printf("[scheduler] Failed to migrate legacy scheduled task profile references: %v", err)
	}
}

// migrateLegacyDefaultTaskProfiles replaces legacy "default" task references
// with a concrete profile ID. This is deliberately a one-time persisted
// migration: runtime task execution must never silently follow a different
// profile after the configured profile is deleted.
func (s *Service) migrateLegacyDefaultTaskProfiles() error {
	s.mu.RLock()
	usersService := s.usersService
	s.mu.RUnlock()
	if usersService == nil || usersService.Exists(models.DefaultUserID) {
		return nil
	}

	users := usersService.ListAll()
	var replacementID string
	if len(users) == 1 {
		replacementID = users[0].ID
	} else {
		for _, user := range users {
			if user.Name != models.DefaultUserName {
				continue
			}
			if replacementID != "" {
				return nil
			}
			replacementID = user.ID
		}
	}
	if replacementID == "" {
		return nil
	}

	settings, err := s.configManager.Load()
	if err != nil {
		return err
	}
	changed := false
	for i := range settings.ScheduledTasks.Tasks {
		task := &settings.ScheduledTasks.Tasks[i]
		if strings.TrimSpace(task.Config["profileId"]) != models.DefaultUserID {
			continue
		}
		task.Config["profileId"] = replacementID
		changed = true
		log.Printf("[scheduler] Migrated task %q profile id from %q to %q", task.Name, models.DefaultUserID, replacementID)
	}
	if !changed {
		return nil
	}
	return s.configManager.Save(settings)
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

	return "", fmt.Errorf("profile %q not found", profileID)
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
		result, err := s.syncPlexToLocal(plexAccount.AuthToken, profileID, syncSource, deleteBehavior, dryRun)
		return result, plexAuthError(err)
	case "target_to_source":
		result, err := s.syncLocalToPlex(plexAccount.AuthToken, profileID, syncSource, deleteBehavior, watchlistSyncSince(task), dryRun)
		return result, plexAuthError(err)
	case "bidirectional":
		result, err := s.syncBidirectional(plexAccount.AuthToken, profileID, syncSource, deleteBehavior, conflictResolution, watchlistSyncSince(task), dryRun)
		return result, plexAuthError(err)
	default:
		return SyncResult{}, fmt.Errorf("unknown sync direction: %s", syncDirection)
	}
}

func plexAuthError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "401 Unauthorized") || strings.Contains(msg, "Invalid token") || strings.Contains(msg, "could not be authenticated") {
		return fmt.Errorf("Plex account authentication failed; reconnect the Plex account in Tools before running this task again: %w", err)
	}
	return err
}

func logWatchlistImportError(source, title string, err error) {
	if errors.Is(err, watchlist.ErrTombstoned) {
		log.Printf("[scheduler] Skipped %s from %s: explicitly removed from local watchlist", title, source)
		return
	}
	log.Printf("[scheduler] Failed to import %s from %s: %v", title, source, err)
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
		extIDs := map[string]string{}
		if i < len(externalIDs) && externalIDs[i] != nil {
			extIDs = externalIDs[i]
		}
		// Add plex ID to external IDs
		extIDs["plex"] = item.RatingKey

		mediaType := plex.NormalizeMediaType(item.Type)
		input := s.enrichSyncedWatchlistIdentity("Plex", models.WatchlistUpsert{
			ID:          item.RatingKey,
			MediaType:   mediaType,
			Name:        item.Title,
			Year:        item.Year,
			PosterURL:   plex.GetPosterURL(item.Thumb, authToken),
			BackdropURL: plex.GetPosterURL(item.Art, authToken),
			ExternalIDs: extIDs,
			SyncSource:  syncSource,
			SyncedAt:    &now,
		})
		for _, key := range schedulerWatchlistMatchKeys(mediaType, input.ID, input.ExternalIDs) {
			plexItemKeys[key] = true
		}
		isNew := !schedulerWatchlistHasAnyKey(existingKeys, mediaType, input.ID, input.ExternalIDs)
		if isNew && dryRun && s.watchlistService.RemovedSince(profileID, mediaType, input.ID, input.ExternalIDs, time.Time{}) {
			isNew = false // the import skips items the user removed locally
		}

		if dryRun {
			if isNew {
				log.Printf("[scheduler] DRY RUN: Would import from Plex: %s (%s)", item.Title, mediaType)
				result.ToAdd = append(result.ToAdd, config.DryRunItem{
					Name:      item.Title,
					MediaType: mediaType,
					ID:        input.ID,
				})
			}
			imported++
			continue
		}

		if _, err := s.watchlistService.AddOrUpdate(profileID, input); err != nil {
			logWatchlistImportError("Plex", item.Title, err)
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
				if ok, err := s.watchlistService.RemoveSynced(profileID, localItem.MediaType, localItem.ID); err != nil {
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

// syncLocalToPlex exports items from local watchlist to Plex watchlist.
// "mirror" removes every Plex item missing locally; "delete" removes only the
// items the user removed locally after since.
func (s *Service) syncLocalToPlex(authToken, profileID, syncSource, deleteBehavior string, since *time.Time, dryRun bool) (SyncResult, error) {
	now := time.Now().UTC()
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
	plexRemovalFailed := false
	if deleteBehavior != "additive" {
		// "delete" matches local removals by provider ID, which needs the Plex
		// items' external IDs.
		var plexExternalIDs []map[string]string
		if deleteBehavior == "delete" && since != nil && len(plexItems) > 0 {
			plexExternalIDs = s.plexClient.GetWatchlistDetailsWithProgress(authToken, plexItems, nil)
		}

		removed := 0
		for i, plexItem := range plexItems {
			// Check if item exists in local watchlist
			if localPlexIDs[plexItem.RatingKey] {
				continue // Item in local, keep in Plex
			}

			// For "delete" mode: only remove items the user removed locally since
			// the last run, leaving items that were only ever on Plex.
			if deleteBehavior == "delete" {
				if since == nil {
					continue
				}
				extIDs := map[string]string{}
				if i < len(plexExternalIDs) && plexExternalIDs[i] != nil {
					extIDs = plexExternalIDs[i]
				}
				extIDs["plex"] = plexItem.RatingKey
				if !s.watchlistService.RemovedSince(profileID, plex.NormalizeMediaType(plexItem.Type), plexItem.RatingKey, extIDs, *since) {
					continue
				}
			}

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
				plexRemovalFailed = true
				continue
			}

			log.Printf("[scheduler] Removed from Plex watchlist: %s", plexItem.Title)
			removed++
		}

		if removed > 0 {
			log.Printf("[scheduler] Removed %d items from Plex watchlist", removed)
		}
	}

	// A failed Plex removal keeps the previous cursor so the next run retries it.
	if !plexRemovalFailed {
		result.Config = map[string]string{watchlistSyncCursorKey: now.Format(time.RFC3339Nano)}
	}

	result.Count = exported
	return result, nil
}

// watchlistSyncCursorKey stores when the last successful watchlist sync that
// exports to a remote list started. Local removals newer than it are pushed to
// the remote list.
const watchlistSyncCursorKey = "bidirectionalDeletionCursor"

// watchlistSyncSince returns the point after which local watchlist removals
// have not yet been propagated to the remote list, or nil before the first run.
func watchlistSyncSince(task config.ScheduledTask) *time.Time {
	if raw := strings.TrimSpace(task.Config[watchlistSyncCursorKey]); raw != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return &parsed
		}
	}
	return task.LastRunAt
}

// syncBidirectional syncs items in both directions between Plex and local.
//
// With delete/mirror behavior, removals propagate too:
//   - a local item this task has already synced (SyncSource == syncSource)
//     that is missing from Plex was removed there, so it is removed locally;
//   - a Plex item the user removed locally after since (a watchlist tombstone)
//     is removed from Plex.
//
// since is nil before the first run, when only additions are synced.
func (s *Service) syncBidirectional(authToken, profileID, syncSource, deleteBehavior, conflictResolution string, since *time.Time, dryRun bool) (SyncResult, error) {
	now := time.Now().UTC()
	result := SyncResult{DryRun: dryRun}
	propagateDeletes := deleteBehavior != "additive"

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
	resolvedPlexInputs := make([]models.WatchlistUpsert, len(plexItems))

	for i, item := range plexItems {
		extIDs := map[string]string{}
		if i < len(externalIDs) && externalIDs[i] != nil {
			extIDs = externalIDs[i]
		}

		extIDs["plex"] = item.RatingKey
		mediaType := plex.NormalizeMediaType(item.Type)
		input := s.enrichSyncedWatchlistIdentity("Plex", models.WatchlistUpsert{
			ID:          item.RatingKey,
			MediaType:   mediaType,
			Name:        item.Title,
			Year:        item.Year,
			PosterURL:   plex.GetPosterURL(item.Thumb, authToken),
			BackdropURL: plex.GetPosterURL(item.Art, authToken),
			ExternalIDs: extIDs,
			SyncSource:  syncSource,
			SyncedAt:    &now,
		})
		resolvedPlexInputs[i] = input
		for _, key := range schedulerWatchlistMatchKeys(mediaType, input.ID, input.ExternalIDs) {
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
	removedFromPlex := 0
	removedFromLocal := 0
	plexRemovalFailed := false

	// Step 1: Sync Plex → Local (items in Plex not in local), or push local
	// removals to Plex.
	for i, plexItem := range plexItems {
		input := resolvedPlexInputs[i]
		mediaType := input.MediaType

		if schedulerWatchlistHasAnyKey(localKeys, mediaType, input.ID, input.ExternalIDs) {
			// Re-upsert matched source metadata so newly resolved provider IDs can
			// merge legacy source-only duplicates and remain available next run.
			if !dryRun {
				if _, err := s.watchlistService.AddOrUpdate(profileID, input); err != nil {
					logWatchlistImportError("Plex", plexItem.Title, err)
				}
			}
			continue
		}

		if propagateDeletes && since != nil && s.watchlistService.RemovedSince(profileID, mediaType, input.ID, input.ExternalIDs, *since) {
			if dryRun {
				log.Printf("[scheduler] DRY RUN: Would remove from Plex (removed locally): %s", plexItem.Title)
				result.ToRemove = append(result.ToRemove, config.DryRunItem{
					Name:      plexItem.Title + " (from Plex)",
					MediaType: mediaType,
					ID:        plexItem.RatingKey,
				})
				removedFromPlex++
				continue
			}
			if err := s.plexClient.RemoveFromWatchlist(authToken, plexItem.RatingKey); err != nil {
				log.Printf("[scheduler] Failed to remove %s from Plex watchlist: %v", plexItem.Title, err)
				plexRemovalFailed = true
				continue
			}
			log.Printf("[scheduler] Removed from Plex watchlist (removed locally): %s", plexItem.Title)
			removedFromPlex++
			continue
		}

		if dryRun {
			if s.watchlistService.RemovedSince(profileID, mediaType, input.ID, input.ExternalIDs, time.Time{}) {
				continue // the import skips items the user removed locally
			}
			log.Printf("[scheduler] DRY RUN: Would import from Plex: %s (%s)", plexItem.Title, mediaType)
			result.ToAdd = append(result.ToAdd, config.DryRunItem{
				Name:      plexItem.Title + " (from Plex)",
				MediaType: mediaType,
				ID:        input.ID,
			})
			synced++
			continue
		}

		if _, err := s.watchlistService.AddOrUpdate(profileID, input); err != nil {
			logWatchlistImportError("Plex", plexItem.Title, err)
			continue
		}

		log.Printf("[scheduler] Imported from Plex: %s", plexItem.Title)
		synced++
	}

	// Step 2: Sync Local → Plex (items in local not in Plex), or apply Plex
	// removals locally.
	for _, localItem := range localItems {
		if schedulerWatchlistHasAnyKey(plexKeys, localItem.MediaType, localItem.ID, localItem.ExternalIDs) {
			continue // Already in Plex
		}

		// Items this task already synced were in Plex at an earlier run, so their
		// absence now means they were removed from Plex.
		if propagateDeletes && localItem.SyncSource == syncSource {
			if dryRun {
				log.Printf("[scheduler] DRY RUN: Would remove from local (removed from Plex): %s", localItem.Name)
				result.ToRemove = append(result.ToRemove, config.DryRunItem{
					Name:      localItem.Name,
					MediaType: localItem.MediaType,
					ID:        localItem.ID,
				})
				removedFromLocal++
				continue
			}
			if ok, err := s.watchlistService.RemoveSynced(profileID, localItem.MediaType, localItem.ID); err != nil {
				log.Printf("[scheduler] Failed to remove watchlist item %s: %v", localItem.Name, err)
			} else if ok {
				log.Printf("[scheduler] Removed watchlist item no longer in Plex: %s", localItem.Name)
				removedFromLocal++
			}
			continue
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

		// Mark the item as synced so a later removal from Plex is recognised even
		// if it happens before the next run sees it there.
		if _, err := s.watchlistService.AddOrUpdate(profileID, models.WatchlistUpsert{
			ID:          localItem.ID,
			MediaType:   localItem.MediaType,
			ExternalIDs: map[string]string{"plex": plexID},
			SyncSource:  syncSource,
			SyncedAt:    &now,
		}); err != nil {
			log.Printf("[scheduler] Failed to mark %s as synced to Plex: %v", localItem.Name, err)
		}

		log.Printf("[scheduler] Exported to Plex: %s", localItem.Name)
		synced++
	}

	if removedFromPlex > 0 || removedFromLocal > 0 {
		log.Printf("[scheduler] Bidirectional Plex sync removed %d items from Plex and %d items locally", removedFromPlex, removedFromLocal)
	}

	// A failed Plex removal keeps the previous cursor so the next run retries it.
	if !plexRemovalFailed {
		result.Config = map[string]string{watchlistSyncCursorKey: now.Format(time.RFC3339Nano)}
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

	// Only the Trakt watchlist can be written to; exporting any other list type
	// would read and write the watchlist instead.
	if syncDirection != "source_to_target" && listType != "watchlist" {
		return SyncResult{}, fmt.Errorf("Trakt %s lists can only be imported; set the sync direction to Trakt → mediastorm", listType)
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

	// Per-account credentials are bound via s.traktFor(traktAccount) in the
	// sync helpers; the shared client's credentials are never mutated.

	// Build sync source identifier for tracking
	syncSource := fmt.Sprintf("trakt:%s:%s:%s", traktAccountID, listType, task.ID)

	switch syncDirection {
	case "source_to_target":
		return s.syncTraktToLocal(traktAccount, profileID, listType, task.Config["customListId"], syncSource, deleteBehavior, dryRun)
	case "target_to_source":
		return s.syncLocalToTrakt(traktAccount, profileID, syncSource, deleteBehavior, watchlistSyncSince(task), dryRun)
	case "bidirectional":
		tracker, err := s.newWatchlistRemovalTracker(task.ID, "trakt", traktAccountID, profileID)
		if err != nil {
			return SyncResult{}, err
		}
		return s.syncTraktBidirectional(traktAccount, profileID, listType, task.Config["customListId"], syncSource, deleteBehavior, conflictResolution, watchlistSyncSince(task), dryRun, tracker)
	default:
		return SyncResult{}, fmt.Errorf("unknown sync direction: %s", syncDirection)
	}
}

// traktFor returns a Trakt client bound to the account's app credentials. The
// shared client is used by handlers and background jobs concurrently, so its
// credentials must never be mutated in place.
func (s *Service) traktFor(account *config.TraktAccount) *trakt.Client {
	return s.traktClient.ForAccount(account)
}

// TraktListItem is a unified representation of items from different Trakt list types
type TraktListItem struct {
	Title     string
	Year      int
	MediaType string // "movie" or "series"
	IDs       map[string]string
}

// getTraktListItems fetches items from the specified Trakt list type
func (s *Service) getTraktListItems(client *trakt.Client, accessToken string, listType string, customListID string) ([]TraktListItem, error) {
	var items []TraktListItem

	switch listType {
	case "watchlist":
		watchlistItems, err := client.GetAllWatchlist(accessToken)
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
		collectionItems, err := client.GetAllCollection(accessToken)
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
		favoriteItems, err := client.GetAllFavorites(accessToken)
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
		listItems, err := client.GetAllListItems(accessToken, customListID)
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
	items, err := s.getTraktListItems(s.traktFor(traktAccount), traktAccount.AccessToken, listType, customListID)
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
		input := s.enrichSyncedWatchlistIdentity("Trakt", models.WatchlistUpsert{
			ID:          preferredWatchlistSourceID(item.IDs),
			MediaType:   item.MediaType,
			Name:        item.Title,
			Year:        item.Year,
			ExternalIDs: item.IDs,
			SyncSource:  syncSource,
			SyncedAt:    &now,
		})

		for _, key := range schedulerWatchlistMatchKeys(item.MediaType, input.ID, input.ExternalIDs) {
			traktItemKeys[key] = true
		}
		isNew := !schedulerWatchlistHasAnyKey(existingKeys, item.MediaType, input.ID, input.ExternalIDs)
		if isNew && dryRun && s.watchlistService.RemovedSince(profileID, item.MediaType, input.ID, input.ExternalIDs, time.Time{}) {
			isNew = false // the import skips items the user removed locally
		}

		if dryRun {
			if isNew {
				log.Printf("[scheduler] DRY RUN: Would import from Trakt %s: %s (%s)", listType, item.Title, item.MediaType)
				result.ToAdd = append(result.ToAdd, config.DryRunItem{
					Name:      item.Title,
					MediaType: item.MediaType,
					ID:        input.ID,
				})
			}
			imported++
			continue
		}

		if _, err := s.watchlistService.AddOrUpdate(profileID, input); err != nil {
			logWatchlistImportError("Trakt", item.Title, err)
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
				if ok, err := s.watchlistService.RemoveSynced(profileID, localItem.MediaType, localItem.ID); err != nil {
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

// syncLocalToTrakt exports items from local watchlist to Trakt watchlist.
// "mirror" removes every Trakt item missing locally; "delete" removes only the
// items the user removed locally after since.
// Note: Trakt only supports adding to watchlist via API, not to collection/favorites/custom lists
func (s *Service) syncLocalToTrakt(traktAccount *config.TraktAccount, profileID, syncSource, deleteBehavior string, since *time.Time, dryRun bool) (SyncResult, error) {
	now := time.Now().UTC()
	result := SyncResult{DryRun: dryRun}

	// Get local watchlist items
	localItems, err := s.watchlistService.List(profileID)
	if err != nil {
		return result, fmt.Errorf("list local items: %w", err)
	}

	// Get current Trakt watchlist to check what's already there
	traktItems, err := s.traktFor(traktAccount).GetAllWatchlist(traktAccount.AccessToken)
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
		err := s.traktFor(traktAccount).AddToWatchlist(traktAccount.AccessToken, moviesToAdd, showsToAdd)
		// Drop cached Home-shelf data for this account; even a failed call may
		// have partially applied.
		s.traktClient.InvalidateListCache(traktAccount.ID)
		if err != nil {
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

			// For "delete" mode: only remove items the user removed locally since
			// the last run, leaving items that were only ever on Trakt.
			if deleteBehavior == "delete" && (since == nil || !s.watchlistService.RemovedSince(profileID, mediaType, itemID, extIDs, *since)) {
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
			err := s.traktFor(traktAccount).RemoveFromWatchlist(traktAccount.AccessToken, moviesToRemove, showsToRemove)
			s.traktClient.InvalidateListCache(traktAccount.ID)
			if err != nil {
				log.Printf("[scheduler] Failed to remove items from Trakt watchlist: %v", err)
				// Keep the previous cursor so the next run retries the removals.
				result.Count = exported
				return result, nil
			}
			log.Printf("[scheduler] Removed %d items from Trakt watchlist", removed)
		}
	}

	result.Config = map[string]string{watchlistSyncCursorKey: now.Format(time.RFC3339Nano)}
	result.Count = exported
	return result, nil
}

// traktSyncIDsFor returns the TMDB/IMDb identity the Trakt sync API accepts for
// a watchlist entry, or ok=false when it has neither.
func traktSyncIDsFor(id string, externalIDs map[string]string) (trakt.SyncIDs, bool) {
	var ids trakt.SyncIDs
	if externalIDs != nil {
		ids.TMDB, _ = strconv.Atoi(externalIDs["tmdb"])
		ids.IMDB = externalIDs["imdb"]
	}
	if ids.TMDB == 0 && ids.IMDB == "" {
		ids.TMDB, _ = strconv.Atoi(id)
	}
	return ids, ids.TMDB != 0 || ids.IMDB != ""
}

// syncTraktBidirectional syncs items in both directions between Trakt and
// local. Only the watchlist can be written to, so other list types are
// rejected before this runs.
//
// With delete/mirror behavior, removals propagate too, as in the Plex sync:
//   - a local item this task has already synced that is missing from Trakt
//     was removed there, so it is removed locally;
//   - a Trakt item the user removed locally after since is removed from Trakt.
//
// tracker (nil without a snapshot store) decides which local items were removed
// on Trakt; without it the SyncSource marker is used.
func (s *Service) syncTraktBidirectional(traktAccount *config.TraktAccount, profileID, listType, customListID, syncSource, deleteBehavior, conflictResolution string, since *time.Time, dryRun bool, tracker *watchlistRemovalTracker) (SyncResult, error) {
	now := time.Now().UTC()
	result := SyncResult{DryRun: dryRun}
	propagateDeletes := deleteBehavior != "additive"

	// Get both lists
	traktItems, err := s.getTraktListItems(s.traktFor(traktAccount), traktAccount.AccessToken, listType, customListID)
	if err != nil {
		return result, err
	}

	localItems, err := s.watchlistService.List(profileID)
	if err != nil {
		return result, fmt.Errorf("list local items: %w", err)
	}

	// Build maps for quick lookup
	traktKeys := make(map[string]bool)
	resolvedTraktInputs := make([]models.WatchlistUpsert, len(traktItems))
	for i, item := range traktItems {
		input := s.enrichSyncedWatchlistIdentity("Trakt", models.WatchlistUpsert{
			ID:          preferredWatchlistSourceID(item.IDs),
			MediaType:   item.MediaType,
			Name:        item.Title,
			Year:        item.Year,
			ExternalIDs: item.IDs,
			SyncSource:  syncSource,
			SyncedAt:    &now,
		})
		resolvedTraktInputs[i] = input
		for _, key := range schedulerWatchlistMatchKeys(item.MediaType, input.ID, input.ExternalIDs) {
			traktKeys[key] = true
		}
	}

	tracker.setRemote(traktKeys)

	localKeys := make(map[string]bool)
	for _, item := range localItems {
		for _, key := range schedulerWatchlistMatchKeys(item.MediaType, item.ID, item.ExternalIDs) {
			localKeys[key] = true
		}
	}

	synced := 0
	var moviesToRemove []trakt.SyncMovie
	var showsToRemove []trakt.SyncShow

	// Step 1: Sync Trakt → Local (items in Trakt not in local), or collect
	// local removals to push to Trakt.
	for i, traktItem := range traktItems {
		input := resolvedTraktInputs[i]
		if schedulerWatchlistHasAnyKey(localKeys, traktItem.MediaType, input.ID, input.ExternalIDs) {
			if !dryRun {
				if _, err := s.watchlistService.AddOrUpdate(profileID, input); err != nil {
					logWatchlistImportError("Trakt", traktItem.Title, err)
				}
			}
			continue
		}

		if propagateDeletes && since != nil && s.watchlistService.RemovedSince(profileID, traktItem.MediaType, input.ID, input.ExternalIDs, *since) {
			ids, ok := traktSyncIDsFor(input.ID, traktItem.IDs)
			if !ok {
				log.Printf("[scheduler] Cannot remove %s from Trakt: no TMDB/IMDb ID", traktItem.Title)
				continue
			}
			if dryRun {
				log.Printf("[scheduler] DRY RUN: Would remove from Trakt (removed locally): %s", traktItem.Title)
				result.ToRemove = append(result.ToRemove, config.DryRunItem{
					Name:      traktItem.Title + " (from Trakt)",
					MediaType: traktItem.MediaType,
					ID:        input.ID,
				})
				continue
			}
			if traktItem.MediaType == "movie" {
				moviesToRemove = append(moviesToRemove, trakt.SyncMovie{IDs: ids})
			} else {
				showsToRemove = append(showsToRemove, trakt.SyncShow{IDs: ids})
			}
			log.Printf("[scheduler] Will remove from Trakt watchlist (removed locally): %s", traktItem.Title)
			continue
		}

		if dryRun {
			if s.watchlistService.RemovedSince(profileID, traktItem.MediaType, input.ID, input.ExternalIDs, time.Time{}) {
				continue // the import skips items the user removed locally
			}
			log.Printf("[scheduler] DRY RUN: Would import from Trakt: %s (%s)", traktItem.Title, traktItem.MediaType)
			result.ToAdd = append(result.ToAdd, config.DryRunItem{
				Name:      traktItem.Title + " (from Trakt)",
				MediaType: traktItem.MediaType,
				ID:        input.ID,
			})
			synced++
			continue
		}

		if _, err := s.watchlistService.AddOrUpdate(profileID, input); err != nil {
			logWatchlistImportError("Trakt", traktItem.Title, err)
			continue
		}

		log.Printf("[scheduler] Imported from Trakt: %s", traktItem.Title)
		synced++
	}

	// Step 2: Sync Local → Trakt (items in local not in Trakt), or apply Trakt
	// removals locally.
	var moviesToAdd []trakt.SyncMovie
	var showsToAdd []trakt.SyncShow
	var exportedItems []models.WatchlistItem
	removedFromLocal := 0

	for _, localItem := range localItems {
		if schedulerWatchlistHasAnyKey(traktKeys, localItem.MediaType, localItem.ID, localItem.ExternalIDs) {
			continue // Already in Trakt
		}

		// An item that was on Trakt at an earlier run and is gone now was
		// removed from Trakt.
		removedOnTrakt := localItem.SyncSource == syncSource
		if tracker != nil {
			removedOnTrakt = tracker.removedRemotely(localItem)
		}
		if propagateDeletes && removedOnTrakt {
			if dryRun {
				log.Printf("[scheduler] DRY RUN: Would remove from local (removed from Trakt): %s", localItem.Name)
				result.ToRemove = append(result.ToRemove, config.DryRunItem{
					Name:      localItem.Name,
					MediaType: localItem.MediaType,
					ID:        localItem.ID,
				})
				continue
			}
			remove := s.watchlistService.RemoveSynced
			if tracker != nil {
				// Record it like a user removal so the other watchlist syncs
				// (Simkl, …) remove it too instead of importing it back.
				remove = s.watchlistService.Remove
			}
			if ok, err := remove(profileID, localItem.MediaType, localItem.ID); err != nil {
				log.Printf("[scheduler] Failed to remove watchlist item %s: %v", localItem.Name, err)
			} else if ok {
				log.Printf("[scheduler] Removed watchlist item no longer in Trakt: %s", localItem.Name)
				removedFromLocal++
			}
			continue
		}

		ids, ok := traktSyncIDsFor(localItem.ID, localItem.ExternalIDs)
		if !ok {
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
			moviesToAdd = append(moviesToAdd, trakt.SyncMovie{IDs: ids})
		} else if localItem.MediaType == "series" {
			showsToAdd = append(showsToAdd, trakt.SyncShow{IDs: ids})
		} else {
			continue
		}
		exportedItems = append(exportedItems, localItem)

		log.Printf("[scheduler] Will export to Trakt: %s", localItem.Name)
		synced++
	}

	if !dryRun && (len(moviesToAdd) > 0 || len(showsToAdd) > 0) {
		err := s.traktFor(traktAccount).AddToWatchlist(traktAccount.AccessToken, moviesToAdd, showsToAdd)
		s.traktClient.InvalidateListCache(traktAccount.ID)
		if err != nil {
			log.Printf("[scheduler] Failed to add items to Trakt watchlist: %v", err)
		} else {
			log.Printf("[scheduler] Exported %d movies and %d shows to Trakt watchlist", len(moviesToAdd), len(showsToAdd))
			// Mark the items as synced so a later removal from Trakt is
			// recognised even if it happens before the next run sees them there.
			for _, item := range exportedItems {
				if _, err := s.watchlistService.AddOrUpdate(profileID, models.WatchlistUpsert{
					ID:         item.ID,
					MediaType:  item.MediaType,
					SyncSource: syncSource,
					SyncedAt:   &now,
				}); err != nil {
					log.Printf("[scheduler] Failed to mark %s as synced to Trakt: %v", item.Name, err)
				}
			}
		}
	}

	traktRemovalFailed := false
	if !dryRun && (len(moviesToRemove) > 0 || len(showsToRemove) > 0) {
		err := s.traktFor(traktAccount).RemoveFromWatchlist(traktAccount.AccessToken, moviesToRemove, showsToRemove)
		s.traktClient.InvalidateListCache(traktAccount.ID)
		if err != nil {
			log.Printf("[scheduler] Failed to remove items from Trakt watchlist: %v", err)
			traktRemovalFailed = true
		} else {
			log.Printf("[scheduler] Removed %d items from Trakt watchlist (removed locally)", len(moviesToRemove)+len(showsToRemove))
		}
	}
	if removedFromLocal > 0 {
		log.Printf("[scheduler] Removed %d local items no longer in Trakt watchlist", removedFromLocal)
	}

	if !dryRun {
		if err := s.saveWatchlistRemovalSnapshot(tracker); err != nil {
			log.Printf("[scheduler] %v", err)
		}
	}
	// A failed Trakt removal keeps the previous cursor so the next run retries it.
	if !traktRemovalFailed {
		result.Config = map[string]string{watchlistSyncCursorKey: now.Format(time.RFC3339Nano)}
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

// executePlaylistRefresh clears the cached Live TV playlist and warms it again.
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

	s.mu.RLock()
	warmer := s.livePlaylistWarmer
	s.mu.RUnlock()
	if warmer == nil {
		return SyncResult{Count: cleared, Message: "Cleared playlist cache; no warmer configured"}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	channelCount, err := warmer.WarmPlaylistCache(ctx)
	if err != nil {
		return SyncResult{Count: cleared}, fmt.Errorf("playlist cache cleared but warm failed: %w", err)
	}

	return SyncResult{
		Count:   channelCount,
		Message: fmt.Sprintf("Cleared %d cache files and warmed %d channels", cleared, channelCount),
	}, nil
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

	// Per-account credentials are bound via s.traktFor(traktAccount) in the
	// sync helpers; the shared client's credentials are never mutated.

	switch syncDirection {
	case "trakt_to_local":
		result, err := s.syncTraktHistoryToLocal(task, traktAccount, profileID, dryRun)
		if err != nil {
			return result, err
		}
		if !dryRun {
			if _, err := s.syncPlaybackFromTrakt(traktAccount, profileID, nil); err != nil {
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
			if _, err := s.syncPlaybackToTrakt(traktAccount, profileID, nil); err != nil {
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

	lastFull, ok := s.lastFullHistorySync(task, task.ID, lastFullHistoryImportConfigKey)

	if task.Config["fullSync"] == "true" {
		// A manual full sync includes all history, including watches older than a year.
		isFullSync = true
	} else if historySyncLastRun(task) == nil || !ok || time.Since(lastFull) >= fullSyncInterval {
		// Reconcile the entire library, including backdated and older watches.
		isFullSync = true
		log.Printf("[scheduler] Performing full Trakt history reconciliation (last full sync: %v)", lastFull)
	} else if historySyncLastRun(task) != nil {
		since = historySyncLastRun(task).Add(-5 * time.Minute)
	}

	log.Printf("[scheduler] Fetching Trakt watch history since=%v (fullSync=%v)", since, isFullSync)

	items, err := s.traktFor(traktAccount).GetWatchHistorySince(traktAccount.AccessToken, since)
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
		s.markFullHistorySync(&result, task.ID, lastFullHistoryImportConfigKey)
		log.Printf("[scheduler] Full Trakt history reconciliation complete, next in %v", fullSyncInterval)
	}

	return result, nil
}

// syncLocalHistoryToTrakt exports local watch history to Trakt
func (s *Service) syncLocalHistoryToTrakt(task config.ScheduledTask, traktAccount *config.TraktAccount, profileID string, dryRun bool) (SyncResult, error) {
	result := SyncResult{DryRun: dryRun}
	startedAt := time.Now().UTC()

	s.mu.RLock()
	historySvc := s.historyService
	s.mu.RUnlock()

	// Get all local watch history for this profile
	items, err := historySvc.ListWatchHistory(profileID)
	if err != nil {
		return result, fmt.Errorf("list local history: %w", err)
	}
	items = s.enrichAndCollapseHistoryItems(items)

	// Reconcile older local watches periodically as well as on manual full sync.
	exportKey := task.ID + ":trakt_export"
	lastFull, haveFull := s.lastFullHistorySync(task, exportKey, lastFullHistoryExportConfigKey)
	isFullExport := task.Config["fullSync"] == "true" || task.Config["fullExport"] == "true" ||
		!haveFull || time.Since(lastFull) >= 6*time.Hour
	var since time.Time
	if !isFullExport && historySyncLastRun(task) != nil {
		since = historySyncLastRun(task).Add(-5 * time.Minute)
	}

	// Fetch existing Trakt history to avoid creating duplicate watch events.
	// Trakt's AddToHistory creates a NEW event each call — it is not idempotent.
	traktItems, err := s.traktFor(traktAccount).GetWatchHistorySince(traktAccount.AccessToken, since)
	if err != nil {
		return result, fmt.Errorf("fetch trakt history for dedup: %w", err)
	}

	hasRecentLocalUnwatch := false
	for _, item := range items {
		if item.Watched {
			continue
		}
		if since.IsZero() || !item.UpdatedAt.Before(since) {
			hasRecentLocalUnwatch = true
			break
		}
	}

	// An unwatch can be recent while the original Trakt watched event is old.
	// Use full history for deletion matching so bidirectional sync can remove
	// stale Trakt watched rows instead of missing them outside the incremental window.
	deletionTraktItems := traktItems
	if hasRecentLocalUnwatch && !since.IsZero() {
		deletionTraktItems, err = s.traktFor(traktAccount).GetWatchHistorySince(traktAccount.AccessToken, time.Time{})
		if err != nil {
			return result, fmt.Errorf("fetch full trakt history for unwatch sync: %w", err)
		}
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

	// Bidirectional: a play removed on Trakt since the last sync must not be
	// re-added by the full export. Only a full listing proves absence.
	removals, err := s.newHistoryRemovalTracker(task, "trakt", traktAccount.ID, profileID, startedAt)
	if err != nil {
		return result, err
	}
	if since.IsZero() {
		removals.setCompleteRemote(alreadyOnTrakt)
	}
	var remoteRemoved []models.WatchHistoryItem

	traktHistoryByKey := make(map[string]traktHistoryState)
	for _, ti := range deletionTraktItems {
		update := s.traktHistoryItemToUpdate(ti, &watched)
		if update == nil {
			continue
		}
		for _, id := range alternateItemIDs(update.MediaType, update.ItemID, update.ExternalIDs) {
			key := strings.ToLower(update.MediaType) + ":" + strings.ToLower(id)
			existing, ok := traktHistoryByKey[key]
			if !ok || ti.WatchedAt.After(existing.watchedAt) {
				traktHistoryByKey[key] = traktHistoryState{watchedAt: ti.WatchedAt}
			}
		}
	}

	// Group episodes by show for batch API call
	showEpisodes := make(map[showKey]map[int][]trakt.SyncEpisode)
	absoluteRetryByCoordinate := make(map[showKey]map[int]map[int]trakt.SyncEpisode)
	showIDs := make(map[showKey]trakt.SyncIDs)
	removeShowEpisodes := make(map[showKey]map[int][]trakt.SyncEpisode)
	removeShowIDs := make(map[showKey]trakt.SyncIDs)
	var movies []trakt.SyncMovie
	var removeMovies []trakt.SyncMovie
	exported := 0
	removed := 0
	skipped := 0

	for _, item := range items {
		if !item.Watched {
			if !since.IsZero() && item.UpdatedAt.Before(since) {
				continue
			}

			traktState, existsOnTrakt := localItemTraktHistoryState(item, traktHistoryByKey)
			if !existsOnTrakt {
				continue
			}
			if !item.UpdatedAt.IsZero() && item.UpdatedAt.Before(traktState.watchedAt) {
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
					result.ToRemove = append(result.ToRemove, config.DryRunItem{
						Name:      item.Name,
						MediaType: "movie",
						ID:        item.ItemID,
					})
					removed++
					continue
				}
				removeMovies = append(removeMovies, trakt.SyncMovie{
					IDs: trakt.SyncIDs{
						TMDB: tmdbID,
						TVDB: tvdbID,
						IMDB: imdbID,
					},
				})
				removed++
			} else if item.MediaType == "episode" {
				sk, syncIDs, ok := traktShowKeyForItem(item)
				if !ok || item.SeasonNumber < 0 || item.EpisodeNumber <= 0 {
					continue
				}
				if dryRun {
					result.ToRemove = append(result.ToRemove, config.DryRunItem{
						Name:      fmt.Sprintf("%s S%02dE%02d", item.SeriesName, item.SeasonNumber, item.EpisodeNumber),
						MediaType: "episode",
						ID:        item.ItemID,
					})
					removed++
					continue
				}
				if removeShowEpisodes[sk] == nil {
					removeShowEpisodes[sk] = make(map[int][]trakt.SyncEpisode)
				}
				removeShowEpisodes[sk][item.SeasonNumber] = append(removeShowEpisodes[sk][item.SeasonNumber], trakt.SyncEpisode{
					Number: item.EpisodeNumber,
					IDs:    episodeIDsToSyncIDs(item.ExternalIDs),
				})
				if _, exists := removeShowIDs[sk]; !exists {
					removeShowIDs[sk] = syncIDs
				}
				removed++
			}
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
		if removals.removedRemotely(item, historyAliasKeys(item.MediaType, item.ItemID, item.ExternalIDs)) {
			remoteRemoved = append(remoteRemoved, item)
			if dryRun {
				result.ToRemove = append(result.ToRemove, historyRemovalDryRunItem(item))
			}
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
			sk, syncIDs, ok := traktShowKeyForItem(item)
			if !ok || item.SeasonNumber < 0 || item.EpisodeNumber <= 0 {
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

			if showEpisodes[sk] == nil {
				showEpisodes[sk] = make(map[int][]trakt.SyncEpisode)
			}
			showEpisodes[sk][item.SeasonNumber] = append(showEpisodes[sk][item.SeasonNumber], trakt.SyncEpisode{
				Number:    item.EpisodeNumber,
				WatchedAt: item.WatchedAt.UTC().Format(time.RFC3339),
				IDs:       episodeIDsToSyncIDs(item.ExternalIDs),
			})
			if absoluteEpisode := traktAbsoluteEpisodeNumber(item.EpisodeNumber, item.ExternalIDs); absoluteEpisode != item.EpisodeNumber {
				if absoluteRetryByCoordinate[sk] == nil {
					absoluteRetryByCoordinate[sk] = make(map[int]map[int]trakt.SyncEpisode)
				}
				if absoluteRetryByCoordinate[sk][item.SeasonNumber] == nil {
					absoluteRetryByCoordinate[sk][item.SeasonNumber] = make(map[int]trakt.SyncEpisode)
				}
				absoluteRetryByCoordinate[sk][item.SeasonNumber][item.EpisodeNumber] = trakt.SyncEpisode{
					Number:    absoluteEpisode,
					WatchedAt: item.WatchedAt.UTC().Format(time.RFC3339),
					IDs:       episodeIDsToSyncIDs(item.ExternalIDs),
				}
			}
			if _, exists := showIDs[sk]; !exists {
				showIDs[sk] = syncIDs
			}
			exported++
		}
	}

	log.Printf("[scheduler] Local→Trakt: %d to export, %d to remove, %d skipped (already on Trakt), %d removed on Trakt", exported, removed, skipped, len(remoteRemoved))

	if dryRun {
		result.Count = exported + removed + len(remoteRemoved)
		return result, nil
	}

	// Build SyncShow batch structure
	buildShows := func(grouped map[showKey]map[int][]trakt.SyncEpisode, ids map[showKey]trakt.SyncIDs) []trakt.SyncShow {
		var shows []trakt.SyncShow
		for sk, seasonEps := range grouped {
			var seasons []trakt.SyncSeason
			for seasonNum, eps := range seasonEps {
				seasons = append(seasons, trakt.SyncSeason{
					Number:   seasonNum,
					Episodes: eps,
				})
			}

			shows = append(shows, trakt.SyncShow{
				IDs:     ids[sk],
				Seasons: seasons,
			})
		}
		return shows
	}

	removeShows := buildShows(removeShowEpisodes, removeShowIDs)
	if len(removeMovies) > 0 || len(removeShows) > 0 {
		removeReq := trakt.SyncHistoryRequest{
			Movies: removeMovies,
			Shows:  removeShows,
		}
		resp, err := s.traktFor(traktAccount).RemoveFromHistory(traktAccount.AccessToken, removeReq)
		if err != nil {
			return result, fmt.Errorf("remove from trakt history: %w", err)
		}
		log.Printf("[scheduler] Removed from Trakt history: %d movies, %d episodes", resp.Deleted.Movies, resp.Deleted.Episodes)
		result.Count += resp.Deleted.Movies + resp.Deleted.Episodes
	}

	shows := buildShows(showEpisodes, showIDs)

	if len(movies) > 0 || len(shows) > 0 {
		syncReq := trakt.SyncHistoryRequest{
			Movies: movies,
			Shows:  shows,
		}
		resp, err := s.traktFor(traktAccount).AddToHistory(traktAccount.AccessToken, syncReq)
		if err != nil {
			return result, fmt.Errorf("add to trakt history: %w", err)
		}
		log.Printf("[scheduler] Synced to Trakt: %d movies, %d episodes added", resp.Added.Movies, resp.Added.Episodes)
		result.Count += resp.Added.Movies + resp.Added.Episodes

		retryEpisodes := make(map[showKey]map[int][]trakt.SyncEpisode)
		for _, missingShow := range resp.NotFound.Shows {
			sk := traktShowKeyFromSyncIDs(missingShow.IDs)
			for _, season := range missingShow.Seasons {
				for _, episode := range season.Episodes {
					candidate, ok := absoluteRetryByCoordinate[sk][season.Number][episode.Number]
					if !ok {
						continue
					}
					if retryEpisodes[sk] == nil {
						retryEpisodes[sk] = make(map[int][]trakt.SyncEpisode)
					}
					retryEpisodes[sk][season.Number] = append(retryEpisodes[sk][season.Number], candidate)
				}
			}
		}
		if len(retryEpisodes) > 0 {
			absoluteShows := buildShows(retryEpisodes, showIDs)
			retryReq := trakt.SyncHistoryRequest{Shows: absoluteShows}
			retryResp, retryErr := s.traktFor(traktAccount).AddToHistory(traktAccount.AccessToken, retryReq)
			if retryErr != nil {
				return result, fmt.Errorf("Trakt absolute episode retry: %w", retryErr)
			} else {
				log.Printf("[scheduler] Trakt absolute episode retry added %d episodes", retryResp.Added.Episodes)
				result.Count += retryResp.Added.Episodes
			}
		}
	}

	mirrored, err := s.mirrorRemoteHistoryRemovals(profileID, "Trakt", remoteRemoved)
	result.Count += mirrored
	if err != nil {
		return result, err
	}
	if err := s.saveHistoryRemovalSnapshot(removals, alreadyOnTrakt, since.IsZero()); err != nil {
		return result, err
	}

	if isFullExport {
		s.markFullHistorySync(&result, exportKey, lastFullHistoryExportConfigKey)
	}
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

func localItemTraktHistoryState(item models.WatchHistoryItem, traktHistoryByKey map[string]traktHistoryState) (traktHistoryState, bool) {
	var zero traktHistoryState
	for _, id := range alternateItemIDs(item.MediaType, item.ItemID, item.ExternalIDs) {
		key := strings.ToLower(item.MediaType) + ":" + strings.ToLower(id)
		if state, ok := traktHistoryByKey[key]; ok {
			return state, true
		}
	}
	return zero, false
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
		return combineHistorySyncResults(toLocalResult, toTraktResult), fmt.Errorf("local to trakt: %w", err)
	}

	// Sync playback positions in both directions for bidirectional tasks too.
	// Pull first so external-player progress on Trakt wins over stale local
	// progress; then skip exporting those same items back in this cycle.
	if !dryRun {
		justImported, err := s.syncPlaybackFromTrakt(traktAccount, profileID, nil)
		if err != nil {
			log.Printf("[scheduler] Warning: sync playback from Trakt failed: %v", err)
		}
		if _, err := s.syncPlaybackToTrakt(traktAccount, profileID, justImported); err != nil {
			log.Printf("[scheduler] Warning: sync playback to Trakt failed: %v", err)
		}
	}

	// Combine results
	combined := combineHistorySyncResults(toLocalResult, toTraktResult)
	return combined, nil
}

// extractSimklIDs builds Simkl show/movie IDs from local history identity fields.
func extractSimklIDs(mediaType, itemID, seriesID string, extIDs map[string]string) simkl.IDs {
	ids := simkl.IDs{}
	if mediaType == "episode" {
		extIDs = mediaidentity.EnrichShowExternalIDs(seriesID, itemID, extIDs)
	} else {
		extIDs = mediaidentity.NormalizeExternalIDs(extIDs)
		// Promote provider from catalog item IDs like tmdb:movie:123.
		if provider, numeric := mediaidentity.SeriesProviderAndID(itemID); provider != "" && numeric != "" {
			if extIDs == nil {
				extIDs = map[string]string{}
			}
			if strings.TrimSpace(extIDs[provider]) == "" {
				extIDs[provider] = numeric
			}
		}
	}
	if extIDs == nil {
		return ids
	}
	if v := strings.TrimSpace(extIDs["imdb"]); v != "" {
		ids.IMDB = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(extIDs["tmdb"])); err == nil && v > 0 {
		ids.TMDB = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(extIDs["tvdb"])); err == nil && v > 0 {
		ids.TVDB = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(extIDs["simkl"])); err == nil && v > 0 {
		ids.Simkl = v
	}
	return ids
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
	return (&Service{}).simklAllItemsToWatchHistory(resp, watched)
}

func (s *Service) simklAllItemsToWatchHistory(resp *simkl.AllItemsResponse, watched *bool) []models.WatchHistoryUpdate {
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
		updates = append(updates, s.simklShowToUpdates(raw, watched)...)
	}
	for _, raw := range resp.Anime {
		if strings.EqualFold(stringFromAny(decodeJSONObject(raw)["anime_type"]), "movie") {
			if update := simklMovieToUpdate(raw, watched); update != nil {
				updates = append(updates, *update)
			}
			continue
		}
		updates = append(updates, s.simklShowToUpdates(raw, watched)...)
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
		movie = nestedObject(obj, "show") // Simkl puts anime movies in the anime bucket.
	}
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
	return (&Service{}).simklShowToUpdates(raw, watched)
}

func (s *Service) simklShowToUpdates(raw json.RawMessage, watched *bool) []models.WatchHistoryUpdate {
	obj := decodeJSONObject(raw)
	if len(obj) == 0 {
		return nil
	}
	parentWatched := simklItemIsWatched(obj)
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
			if seasonNumber < 0 || episodeNumber <= 0 {
				continue
			}
			watchedAt := simklFirstTime(episodeObj, "watched_at", "last_watched_at", "completed_at", "last_watched")
			if watchedAt.IsZero() {
				if !parentWatched {
					continue
				}
				watchedAt = simklFirstTime(obj, "watched_at", "last_watched_at", "completed_at", "last_watched")
			}
			localSeriesID, localTitle, localYear := seriesID, showTitle, showYear
			localIDs, sourceSeason := ids, seasonNumber
			if mappedID, mappedIDs, mappedSeason, ok := simkl.CatalogEpisodeIdentity(ids, seasonNumber, episodeNumber); ok {
				localSeriesID, localIDs, sourceSeason = mappedID, mappedIDs, mappedSeason
				localTitle, localYear = "Monster: The Lizzie Borden Story", 2026
			}
			episodeIDs := cloneStringMap(localIDs)
			absoluteEpisode := 0
			if episodeNumber >= 1000 {
				absoluteEpisode = episodeNumber
			}
			localSeason, localEpisode, localAbsolute, episodeTitle := s.canonicalizeProviderEpisode("simkl", localIDs, nil, sourceSeason, episodeNumber, absoluteEpisode, stringFromAny(episodeObj["title"]))
			if localAbsolute > 0 {
				episodeIDs["absoluteEpisode"] = strconv.Itoa(localAbsolute)
			}
			updates = append(updates, models.WatchHistoryUpdate{
				MediaType:     "episode",
				ItemID:        fmt.Sprintf("%s:s%02de%02d", localSeriesID, localSeason, localEpisode),
				Name:          episodeTitle,
				Year:          localYear,
				Watched:       watched,
				WatchedAt:     watchedAt,
				ExternalIDs:   episodeIDs,
				SeriesID:      localSeriesID,
				SeriesName:    localTitle,
				SeasonNumber:  localSeason,
				EpisodeNumber: localEpisode,
			})
		}
	}
	return updates
}

func simklItemIsWatched(obj map[string]interface{}) bool {
	status := strings.ToLower(strings.TrimSpace(stringFromAny(obj["status"])))
	return status == "" || status == "completed"
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
// Scheduled sync always uses scrobble/pause for incomplete progress; scrobble/stop
// creates watched history entries on Trakt and would be repeated on every sync.
// 90%+ is exported through the explicit watched-history path.
// Returns the set of exported item keys (mediaType:itemID) so the caller can
// exclude them from a subsequent pull to avoid the push→pull timestamp round-trip.
func (s *Service) syncPlaybackToTrakt(traktAccount *config.TraktAccount, profileID string, skipExport map[string]bool) (map[string]bool, error) {
	exported := make(map[string]bool)
	if s.historyService == nil {
		return exported, nil
	}

	items, err := s.historyService.ListPlaybackProgress(profileID)
	if err != nil {
		return exported, fmt.Errorf("list playback progress: %w", err)
	}

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
		itemIDs := alternateItemIDs(item.MediaType, item.ItemID, item.ExternalIDs)
		skipItem := false
		for _, id := range itemIDs {
			if skipExport[strings.ToLower(item.MediaType)+":"+strings.ToLower(id)] {
				skipItem = true
				break
			}
		}
		if skipItem {
			continue
		}

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
					IDs:       episodeIDsToSyncIDs(item.ExternalIDs),
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
				Season:    item.SeasonNumber,
				Number:    item.EpisodeNumber,
				NumberAbs: traktAbsoluteEpisodeNumber(item.EpisodeNumber, item.ExternalIDs),
				Title:     item.EpisodeName,
				IDs:       episodeIDsToSyncIDs(item.ExternalIDs),
			}
			req.Show = &trakt.ScrobbleShow{
				Title: item.SeriesName,
				IDs:   seriesIDToSyncIDs(item.SeriesID, item.ExternalIDs),
			}
		} else {
			continue
		}

		// ScrobblePause saves the position on Trakt without adding a watched event.
		_, syncErr := s.traktFor(traktAccount).ScrobblePause(accessToken, req)
		if errors.Is(syncErr, trakt.ErrNotFound) && req.Episode != nil && req.Episode.NumberAbs > 0 && req.Episode.NumberAbs != req.Episode.Number {
			fallbackReq := req
			episode := *req.Episode
			episode.Number = episode.NumberAbs
			fallbackReq.Episode = &episode
			_, syncErr = s.traktFor(traktAccount).ScrobblePause(accessToken, fallbackReq)
			if syncErr == nil {
				log.Printf("[scheduler] Synced playback for %s using Trakt absolute episode number %d", item.ItemID, episode.Number)
			}
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
		if _, err := s.traktFor(traktAccount).AddToHistory(traktAccount.AccessToken, syncReq); err != nil {
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
func (s *Service) syncPlaybackFromTrakt(traktAccount *config.TraktAccount, profileID string, justExported map[string]bool) (map[string]bool, error) {
	importedKeys := make(map[string]bool)
	if s.historyService == nil {
		return importedKeys, nil
	}

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
		traktItems, err := s.traktFor(traktAccount).GetPlaybackProgress(accessToken, mediaType)
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

			hiddenMarker := s.findHiddenPlaybackMarker(profileID, *update)
			if hiddenMarker != nil && !traktItem.PausedAt.After(hiddenMarker.UpdatedAt) {
				if traktItem.ID > 0 {
					if err := s.traktFor(traktAccount).RemovePlaybackItem(accessToken, traktItem.ID); err != nil {
						log.Printf("[scheduler] Failed to remove hidden Trakt playback item %d for %s %s: %v",
							traktItem.ID, update.MediaType, update.ItemID, err)
					} else {
						log.Printf("[scheduler] Removed hidden Trakt playback item %d for %s %s",
							traktItem.ID, update.MediaType, update.ItemID)
					}
				}
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

			if s.historyService.IsWatchedProgressUpdate(profileID, *update) {
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

			if localProgress != nil {
				if localProgress.HiddenFromContinueWatching && !traktItem.PausedAt.After(localProgress.UpdatedAt) {
					if traktItem.ID > 0 {
						if err := s.traktFor(traktAccount).RemovePlaybackItem(accessToken, traktItem.ID); err != nil {
							log.Printf("[scheduler] Failed to remove hidden Trakt playback item %d for %s %s: %v",
								traktItem.ID, update.MediaType, update.ItemID, err)
						} else {
							log.Printf("[scheduler] Removed hidden Trakt playback item %d for %s %s",
								traktItem.ID, update.MediaType, update.ItemID)
						}
					}
					continue
				}
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
			for _, id := range alternateItemIDs(update.MediaType, update.ItemID, update.ExternalIDs) {
				importedKeys[strings.ToLower(update.MediaType)+":"+strings.ToLower(id)] = true
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
	return importedKeys, nil
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
	if s.historyService == nil {
		return nil
	}

	switch update.MediaType {
	case "movie":
		for _, itemID := range alternateItemIDs(update.MediaType, update.ItemID, update.ExternalIDs) {
			marker, err := s.historyService.GetPlaybackProgress(profileID, "movie", itemID)
			if err != nil || marker == nil {
				continue
			}
			if marker.HiddenFromContinueWatching &&
				marker.MediaType == "movie" &&
				marker.ItemID == marker.SeriesID &&
				marker.SeasonNumber == 0 &&
				marker.EpisodeNumber == 0 {
				return marker
			}
		}
	case "episode":
		seriesIDs := alternateSeriesIDs(update.SeriesID, update.ExternalIDs)
		for _, seriesID := range seriesIDs {
			marker, err := s.historyService.GetPlaybackProgress(profileID, "episode", seriesID)
			if err != nil || marker == nil {
				continue
			}
			if marker.HiddenFromContinueWatching &&
				marker.MediaType == "episode" &&
				marker.SeasonNumber == 0 &&
				marker.EpisodeNumber == 0 {
				return marker
			}
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
		if tmdbID, ok := ids["tmdb"]; ok && tmdbID != "" {
			itemID = fmt.Sprintf("tmdb:movie:%s", tmdbID)
		} else if tvdbID, ok := ids["tvdb"]; ok && tvdbID != "" {
			itemID = fmt.Sprintf("tvdb:movie:%s", tvdbID)
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
		episodeIDs := trakt.IDsToMap(item.Episode.IDs)
		addEpisodeExternalIDs(showIDs, episodeIDs, item.Episode.NumberAbs)
		var seriesID, itemID string
		if tmdbID, ok := showIDs["tmdb"]; ok && tmdbID != "" {
			seriesID = fmt.Sprintf("tmdb:tv:%s", tmdbID)
			itemID = fmt.Sprintf("tmdb:tv:%s:s%02de%02d", tmdbID, item.Episode.Season, item.Episode.Number)
		} else if tvdbID, ok := showIDs["tvdb"]; ok && tvdbID != "" {
			seriesID = fmt.Sprintf("tvdb:series:%s", tvdbID)
			itemID = fmt.Sprintf("tvdb:series:%s:s%02de%02d", tvdbID, item.Episode.Season, item.Episode.Number)
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

func episodeIDsToSyncIDs(extIDs map[string]string) trakt.SyncIDs {
	ids := trakt.SyncIDs{}
	if v, ok := extIDs["episodeTmdb"]; ok {
		ids.TMDB, _ = strconv.Atoi(v)
	}
	if v, ok := extIDs["episodeImdb"]; ok {
		ids.IMDB = v
	}
	if v, ok := extIDs["episodeTvdb"]; ok {
		ids.TVDB, _ = strconv.Atoi(v)
	}
	if v, ok := extIDs["episodeTrakt"]; ok {
		ids.Trakt, _ = strconv.Atoi(v)
	}
	return ids
}

func addEpisodeExternalIDs(showIDs map[string]string, episodeIDs map[string]string, absoluteEpisode int) {
	if showIDs == nil {
		return
	}
	if v := episodeIDs["tmdb"]; v != "" {
		showIDs["episodeTmdb"] = v
	}
	if v := episodeIDs["imdb"]; v != "" {
		showIDs["episodeImdb"] = v
	}
	if v := episodeIDs["tvdb"]; v != "" {
		showIDs["episodeTvdb"] = v
	}
	if v := episodeIDs["trakt"]; v != "" {
		showIDs["episodeTrakt"] = v
	}
	if absoluteEpisode > 0 {
		showIDs["absoluteEpisode"] = strconv.Itoa(absoluteEpisode)
	}
}

func (s *Service) canonicalizeTraktEpisode(showIDs map[string]string, episodeIDs map[string]string, episode trakt.Episode) (int, int, int, string) {
	return s.canonicalizeProviderEpisode("trakt", showIDs, episodeIDs, episode.Season, episode.Number, episode.NumberAbs, episode.Title)
}

func (s *Service) canonicalizeProviderEpisode(provider string, showIDs map[string]string, episodeIDs map[string]string, seasonNumber, episodeNumber, absoluteEpisode int, episodeTitle string) (int, int, int, string) {
	return s.canonicalizeProviderEpisodeCached(context.Background(), nil, provider, showIDs, episodeIDs, seasonNumber, episodeNumber, absoluteEpisode, episodeTitle)
}

// The cache belongs to one import, including failed lookups, so a large history
// never repeats metadata requests for every episode of the same show.
func (s *Service) canonicalizeProviderEpisodeCached(parent context.Context, cache map[string]*models.SeriesDetails, provider string, showIDs map[string]string, episodeIDs map[string]string, seasonNumber, episodeNumber, absoluteEpisode int, episodeTitle string) (int, int, int, string) {
	originalSeason := seasonNumber
	originalEpisode := episodeNumber

	s.mu.RLock()
	metadataSvc := s.metadataService
	s.mu.RUnlock()
	if metadataSvc == nil {
		return seasonNumber, episodeNumber, absoluteEpisode, episodeTitle
	}

	var query models.SeriesDetailsQuery
	if tvdbID, ok := positiveIntFromMap(showIDs, "tvdb"); ok {
		query.TVDBID = int64(tvdbID)
	}
	if tmdbID, ok := positiveIntFromMap(showIDs, "tmdb"); ok {
		query.TMDBID = int64(tmdbID)
	}
	query.IMDBID = strings.TrimSpace(showIDs["imdb"])
	if tvdbID, ok := positiveIntFromMap(showIDs, "tvdb"); ok {
		query.TitleID = fmt.Sprintf("tvdb:series:%d", tvdbID)
	} else if tmdbID, ok := positiveIntFromMap(showIDs, "tmdb"); ok {
		query.TitleID = fmt.Sprintf("tmdb:tv:%d", tmdbID)
	} else if query.IMDBID != "" {
		query.TitleID = fmt.Sprintf("imdb:%s", query.IMDBID)
	}
	if query.TVDBID == 0 && query.TMDBID == 0 && query.IMDBID == "" && query.TitleID == "" {
		return seasonNumber, episodeNumber, absoluteEpisode, episodeTitle
	}

	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	cacheKey := fmt.Sprintf("%d:%d:%s", query.TVDBID, query.TMDBID, query.IMDBID)
	details, cached := cache[cacheKey]
	var err error
	if !cached {
		details, err = metadataSvc.SeriesDetailsLite(ctx, query)
		if cache != nil {
			cache[cacheKey] = details
		}
	}
	if err != nil || details == nil {
		if err != nil {
			log.Printf("[scheduler] %s import: unable to resolve canonical episode for %s S%02dE%02d: %v",
				provider, query.TitleID, originalSeason, originalEpisode, err)
		}
		return seasonNumber, episodeNumber, absoluteEpisode, episodeTitle
	}

	episodeTVDBID, hasEpisodeTVDBID := positiveIntFromMap(episodeIDs, "tvdb")
	episodeTMDBID, hasEpisodeTMDBID := positiveIntFromMap(episodeIDs, "tmdb")
	var exactMatch, absoluteMatch *models.SeriesEpisode
	for _, season := range details.Seasons {
		for _, localEpisode := range season.Episodes {
			matchesEpisodeID := false
			if hasEpisodeTVDBID && localEpisode.TVDBID == int64(episodeTVDBID) {
				matchesEpisodeID = true
			}
			if !matchesEpisodeID && hasEpisodeTMDBID && (localEpisode.TMDBID == int64(episodeTMDBID) || strings.TrimSpace(localEpisode.ID) == fmt.Sprintf("tmdb:episode:%d", episodeTMDBID)) {
				matchesEpisodeID = true
			}
			if exactMatch == nil && localEpisode.SeasonNumber == originalSeason && localEpisode.EpisodeNumber == originalEpisode {
				episodeCopy := localEpisode
				exactMatch = &episodeCopy
			}
			matchesAbsolute := absoluteEpisode > 0 && localEpisode.AbsoluteEpisodeNumber == absoluteEpisode
			if !matchesEpisodeID && !matchesAbsolute {
				continue
			}
			if !matchesEpisodeID && matchesAbsolute {
				episodeCopy := localEpisode
				absoluteMatch = &episodeCopy
				continue
			}

			addResolvedEpisodeExternalIDs(episodeIDs, localEpisode)
			if localEpisode.AbsoluteEpisodeNumber > 0 {
				absoluteEpisode = localEpisode.AbsoluteEpisodeNumber
			}
			if strings.TrimSpace(localEpisode.Name) != "" {
				episodeTitle = localEpisode.Name
			}
			if localEpisode.SeasonNumber != originalSeason || localEpisode.EpisodeNumber != originalEpisode {
				log.Printf("[scheduler] %s import: canonicalized episode %s S%02dE%02d abs=%d -> S%02dE%02d",
					provider, query.TitleID, originalSeason, originalEpisode, absoluteEpisode, localEpisode.SeasonNumber, localEpisode.EpisodeNumber)
			}
			return localEpisode.SeasonNumber, localEpisode.EpisodeNumber, absoluteEpisode, episodeTitle
		}
	}
	if absoluteMatch != nil {
		addResolvedEpisodeExternalIDs(episodeIDs, *absoluteMatch)
		if absoluteMatch.AbsoluteEpisodeNumber > 0 {
			absoluteEpisode = absoluteMatch.AbsoluteEpisodeNumber
		}
		if strings.TrimSpace(absoluteMatch.Name) != "" {
			episodeTitle = absoluteMatch.Name
		}
		if absoluteMatch.SeasonNumber != originalSeason || absoluteMatch.EpisodeNumber != originalEpisode {
			log.Printf("[scheduler] %s import: canonicalized episode %s S%02dE%02d abs=%d -> S%02dE%02d",
				provider, query.TitleID, originalSeason, originalEpisode, absoluteEpisode, absoluteMatch.SeasonNumber, absoluteMatch.EpisodeNumber)
		}
		return absoluteMatch.SeasonNumber, absoluteMatch.EpisodeNumber, absoluteEpisode, episodeTitle
	}
	if exactMatch != nil {
		addResolvedEpisodeExternalIDs(episodeIDs, *exactMatch)
		if exactMatch.AbsoluteEpisodeNumber > 0 {
			absoluteEpisode = exactMatch.AbsoluteEpisodeNumber
		}
		if strings.TrimSpace(exactMatch.Name) != "" {
			episodeTitle = exactMatch.Name
		}
		return exactMatch.SeasonNumber, exactMatch.EpisodeNumber, absoluteEpisode, episodeTitle
	}

	return seasonNumber, episodeNumber, absoluteEpisode, episodeTitle
}

func addResolvedEpisodeExternalIDs(episodeIDs map[string]string, episode models.SeriesEpisode) {
	if episodeIDs == nil {
		return
	}
	if episode.TMDBID > 0 && strings.TrimSpace(episodeIDs["tmdb"]) == "" {
		episodeIDs["tmdb"] = strconv.FormatInt(episode.TMDBID, 10)
	}
	if episode.TVDBID > 0 && strings.TrimSpace(episodeIDs["tvdb"]) == "" {
		episodeIDs["tvdb"] = strconv.FormatInt(episode.TVDBID, 10)
	}
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func traktAbsoluteEpisodeNumber(episodeNumber int, extIDs map[string]string) int {
	if absolute, ok := positiveIntFromMap(extIDs, "absoluteEpisode"); ok {
		return absolute
	}
	return episodeNumber
}

func positiveIntFromMap(values map[string]string, key string) (int, bool) {
	if values == nil {
		return 0, false
	}
	value, err := strconv.Atoi(values[key])
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
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
// showKey groups episodes by show for batched Trakt sync calls. It carries
// every show identifier we know so shows without a TVDB ID (tmdb-only player
// metadata) can still be exported/removed instead of being silently skipped.
type showKey struct {
	tvdbID int
	tmdbID int
	imdbID string
}

// traktShowKeyForItem derives the show grouping key and Trakt sync IDs for a
// local episode history item. ok is false when no show identifier is known.
func traktShowKeyForItem(item models.WatchHistoryItem) (showKey, trakt.SyncIDs, bool) {
	var sk showKey
	if item.ExternalIDs != nil {
		if id, ok := item.ExternalIDs["tvdb"]; ok {
			sk.tvdbID, _ = strconv.Atoi(id)
		}
		if id, ok := item.ExternalIDs["tmdb"]; ok {
			sk.tmdbID, _ = strconv.Atoi(id)
		}
		sk.imdbID = strings.ToLower(strings.TrimSpace(item.ExternalIDs["imdb"]))
	}
	if sk.tvdbID == 0 && sk.tmdbID == 0 && sk.imdbID == "" {
		return sk, trakt.SyncIDs{}, false
	}
	// Collapse aliases: a show keyed once with {tvdb} and once with {tvdb,tmdb}
	// must group together, so prefer the single strongest identifier.
	syncIDs := trakt.SyncIDs{TVDB: sk.tvdbID, TMDB: sk.tmdbID, IMDB: sk.imdbID}
	switch {
	case sk.tvdbID != 0:
		sk = showKey{tvdbID: sk.tvdbID}
	case sk.tmdbID != 0:
		sk = showKey{tmdbID: sk.tmdbID}
	default:
		sk = showKey{imdbID: sk.imdbID}
	}
	return sk, syncIDs, true
}

func traktShowKeyFromSyncIDs(ids trakt.SyncIDs) showKey {
	switch {
	case ids.TVDB > 0:
		return showKey{tvdbID: ids.TVDB}
	case ids.TMDB > 0:
		return showKey{tmdbID: ids.TMDB}
	default:
		return showKey{imdbID: strings.ToLower(strings.TrimSpace(ids.IMDB))}
	}
}

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
		episodeIDs := trakt.IDsToMap(item.Episode.IDs)
		seasonNumber, episodeNumber, absoluteEpisode, episodeTitle := s.canonicalizeTraktEpisode(showIDs, episodeIDs, *item.Episode)
		addEpisodeExternalIDs(showIDs, episodeIDs, absoluteEpisode)

		// Build episode-specific composite ID: tvdb:series:SHOWID:s01e02
		// Prefer TVDB to match the player/details page ID format
		var seriesID, itemID string
		if tvdbID, ok := showIDs["tvdb"]; ok && tvdbID != "" {
			seriesID = fmt.Sprintf("tvdb:series:%s", tvdbID)
			itemID = fmt.Sprintf("tvdb:series:%s:s%02de%02d", tvdbID, seasonNumber, episodeNumber)
		} else if tmdbID, ok := showIDs["tmdb"]; ok && tmdbID != "" {
			seriesID = fmt.Sprintf("tmdb:tv:%s", tmdbID)
			itemID = fmt.Sprintf("tmdb:tv:%s:s%02de%02d", tmdbID, seasonNumber, episodeNumber)
		} else if imdbID, ok := showIDs["imdb"]; ok && imdbID != "" {
			seriesID = fmt.Sprintf("imdb:%s", imdbID)
			itemID = fmt.Sprintf("imdb:%s:s%02de%02d", imdbID, seasonNumber, episodeNumber)
		}
		if itemID == "" {
			return nil
		}

		update.MediaType = "episode"
		update.ItemID = itemID
		update.Name = episodeTitle
		update.Year = item.Show.Year
		update.ExternalIDs = showIDs
		update.SeriesID = seriesID
		update.SeriesName = item.Show.Title
		update.SeasonNumber = seasonNumber
		update.EpisodeNumber = episodeNumber
	} else {
		return nil
	}

	return update
}

// executePrewarm runs the prewarm task to pre-resolve configured home-shelf items.
func (s *Service) executePrewarm(task config.ScheduledTask) (SyncResult, error) {
	if s.prewarmService == nil {
		return SyncResult{}, errors.New("prewarm service not configured")
	}

	prewarmResult, err := s.prewarmService.RunOnceWithConfig(s.ctx, task.Config)
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

	// New tasks pin history sync to one server connection. Keep the all-server
	// behavior for existing tasks that predate these config fields.
	serverID := strings.TrimSpace(task.Config["plexServerId"])
	serverURL := strings.TrimSpace(task.Config["plexServerUrl"])
	log.Printf("[scheduler] Plex history configuration task=%q account=%q accountName=%q profile=%q server=%q plexUser=%d dryRun=%v fullSync=%v",
		task.ID, plexAccount.ID, plexAccount.Name, profileID, serverID, plexUserID, dryRun, task.Config["fullSync"] == "true")
	var historyItems []plex.WatchHistoryItem
	if serverID != "" && serverURL != "" {
		historyItems, err = s.plexClient.GetWatchHistoryForServer(plexAccount.AuthToken, serverID, serverURL, 5000, plexUserID)
	} else {
		historyItems, err = s.plexClient.GetAllWatchHistory(plexAccount.AuthToken, 5000, plexUserID)
	}
	if err != nil {
		return SyncResult{}, fmt.Errorf("fetch plex history: %w", err)
	}

	log.Printf("[scheduler] Fetched %d Plex history items", len(historyItems))

	result := SyncResult{DryRun: dryRun}
	var updates []models.WatchHistoryUpdate

	skippedIdentity := 0
	for _, item := range historyItems {
		update, ok := plexHistoryUpdate(item)
		if !ok {
			skippedIdentity++
			log.Printf("[scheduler] Plex history: skipping %q (%s): no usable catalog identity or episode coordinates", item.Title, item.Type)
			continue
		}
		if dryRun {
			name := update.Name
			if update.MediaType == "episode" && update.SeriesName != "" {
				name = fmt.Sprintf("%s S%02dE%02d", update.SeriesName, update.SeasonNumber, update.EpisodeNumber)
			}
			result.ToAdd = append(result.ToAdd, config.DryRunItem{
				Name: name, MediaType: update.MediaType, ID: update.ItemID,
			})
			continue
		}
		updates = append(updates, update)
	}
	if skippedIdentity > 0 {
		result.Message = fmt.Sprintf("Skipped %d Plex history items without a usable catalog identity", skippedIdentity)
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

		extIDs := schedulerNormalizeExternalIDs(item.ProviderIDs)
		if extIDs == nil {
			extIDs = make(map[string]string)
		}
		extIDs["jellyfin"] = item.ID

		input := s.enrichSyncedWatchlistIdentity("Jellyfin", models.WatchlistUpsert{
			ID:          item.ID,
			MediaType:   mediaType,
			Name:        item.Name,
			Year:        item.Year,
			ExternalIDs: extIDs,
			SyncSource:  syncSource,
			SyncedAt:    &now,
		})
		for _, key := range schedulerWatchlistMatchKeys(mediaType, input.ID, input.ExternalIDs) {
			jfItemKeys[key] = true
		}

		isNew := !schedulerWatchlistHasAnyKey(existingKeys, mediaType, input.ID, input.ExternalIDs)
		if isNew && dryRun && s.watchlistService.RemovedSince(profileID, mediaType, input.ID, input.ExternalIDs, time.Time{}) {
			isNew = false // the import skips items the user removed locally
		}

		if dryRun {
			if isNew {
				log.Printf("[scheduler] DRY RUN: Would import from Jellyfin: %s (%s)", item.Name, mediaType)
				result.ToAdd = append(result.ToAdd, config.DryRunItem{
					Name:      item.Name,
					MediaType: mediaType,
					ID:        input.ID,
				})
			}
			imported++
			continue
		}

		if _, err := s.watchlistService.AddOrUpdate(profileID, input); err != nil {
			logWatchlistImportError("Jellyfin", item.Name, err)
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

				if ok, err := s.watchlistService.RemoveSynced(profileID, localItem.MediaType, localItem.ID); err != nil {
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

		// Without a play date the import must not override local state: "now"
		// would always beat a local unwatch and re-watch the item every run.
		var watchedAt time.Time
		if item.DatePlayed != nil {
			watchedAt = *item.DatePlayed
		}

		extIDs := item.ProviderIDs
		if extIDs == nil {
			extIDs = map[string]string{}
		}
		extIDs["jellyfin"] = item.ID

		update := models.WatchHistoryUpdate{
			MediaType:        mediaType,
			ItemID:           itemID,
			Name:             item.Name,
			Year:             item.Year,
			Watched:          &watched,
			WatchedAt:        watchedAt,
			WatchedAtUnknown: item.DatePlayed == nil,
			ExternalIDs:      extIDs,
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

	if syncDirection != "source_to_target" && syncDirection != "target_to_source" && syncDirection != "bidirectional" {
		return SyncResult{}, fmt.Errorf("unknown sync direction: %s", syncDirection)
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

	if syncDirection == "source_to_target" {
		return s.syncMDBListWatchlistToLocal(mdblistAccount, profileID, syncSource, deleteBehavior, dryRun)
	}
	var tracker *watchlistRemovalTracker
	if syncDirection == "bidirectional" {
		if tracker, err = s.newWatchlistRemovalTracker(task.ID, "mdblist", mdblistAccountID, profileID); err != nil {
			return SyncResult{}, err
		}
	}
	return s.syncMDBListWatchlist(mdblistAccount, profileID, syncDirection, syncSource, deleteBehavior, watchlistSyncSince(task), dryRun, tracker)
}

func (s *Service) syncMDBListWatchlistToLocal(account *config.MDBListAccount, profileID, syncSource, deleteBehavior string, dryRun bool) (SyncResult, error) {
	now := time.Now().UTC()
	result := SyncResult{DryRun: dryRun}

	items, err := fetchMDBListWatchlist(account.APIKey)
	if err != nil {
		return result, err
	}

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
			log.Printf("[scheduler] Skipping MDBList watchlist item %q: no provider IDs", item.Title)
			continue
		}
		input := s.enrichSyncedWatchlistIdentity("MDBList", models.WatchlistUpsert{
			ID:          itemID,
			MediaType:   mediaType,
			Name:        item.Title,
			Year:        item.ReleaseYear,
			ExternalIDs: extIDs,
			SyncSource:  syncSource,
			SyncedAt:    &now,
		})

		for _, key := range schedulerWatchlistMatchKeys(mediaType, input.ID, input.ExternalIDs) {
			mdblistItemKeys[key] = true
		}
		isNew := !schedulerWatchlistHasAnyKey(existingKeys, mediaType, input.ID, input.ExternalIDs)
		if isNew && dryRun && s.watchlistService.RemovedSince(profileID, mediaType, input.ID, input.ExternalIDs, time.Time{}) {
			isNew = false // the import skips items the user removed locally
		}

		if dryRun {
			if isNew {
				result.ToAdd = append(result.ToAdd, config.DryRunItem{
					Name:      item.Title,
					MediaType: mediaType,
					ID:        input.ID,
				})
			}
			imported++
			continue
		}

		if _, err := s.watchlistService.AddOrUpdate(profileID, input); err != nil {
			logWatchlistImportError("MDBList", item.Title, err)
			continue
		}
		imported++
	}

	// Handle deletions for delete/mirror modes
	if deleteBehavior != "additive" {
		localItems, err := s.watchlistService.List(profileID)
		if err != nil {
			log.Printf("[scheduler] Failed to list local items for deletion check: %v", err)
		}
		for _, item := range localItems {
			if schedulerWatchlistHasAnyKey(mdblistItemKeys, item.MediaType, item.ID, item.ExternalIDs) {
				continue // Item still in MDBList, keep it
			}
			// "delete" only removes items this task synced; "mirror" removes
			// everything not in MDBList.
			if deleteBehavior == "delete" && item.SyncSource != syncSource {
				continue
			}
			if dryRun {
				log.Printf("[scheduler] DRY RUN: Would remove from local: %s", item.Name)
				result.ToRemove = append(result.ToRemove, config.DryRunItem{
					Name:      item.Name,
					MediaType: item.MediaType,
					ID:        item.ID,
				})
				continue
			}
			if _, err := s.watchlistService.RemoveSynced(profileID, item.MediaType, item.ID); err != nil {
				log.Printf("[scheduler] Failed to remove stale MDBList watchlist item: %v", err)
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
	// Source-native IDs are required to reconcile an older source-only row after
	// metadata enrichment adds a canonical TMDB/TVDB/IMDb identity.
	for _, provider := range []string{"plex", "trakt", "jellyfin"} {
		if v := externalIDs[provider]; v != "" {
			add(v)
			add(provider + ":" + v)
		}
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
	_, err := postToMDBListBody(apiKey, path, body)
	return err
}

func postToMDBListBody(apiKey, path, body string) ([]byte, error) {
	url := fmt.Sprintf("https://api.mdblist.com%s?apikey=%s", path, apiKey)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("mdblist %s: %w", path, mdblistRequestError(err))
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if readErr != nil {
		return nil, fmt.Errorf("read mdblist %s response: %w", path, readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return respBody, fmt.Errorf("mdblist %s returned %d: %s", path, resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

// parseMDBListNotFoundEpisodes returns show-scoped "imdb|tmdb|season:number" keys
// from a /sync/watched response.
func parseMDBListNotFoundEpisodes(respBody []byte) map[string]bool {
	out := make(map[string]bool)
	if len(respBody) == 0 {
		return out
	}
	var parsed struct {
		NotFound struct {
			Episodes []struct {
				Season int `json:"season"`
				Number int `json:"number"`
				Show   struct {
					IDs struct {
						IMDB string `json:"imdb"`
						TMDB int    `json:"tmdb"`
					} `json:"ids"`
				} `json:"show"`
			} `json:"episodes"`
		} `json:"not_found"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return out
	}
	for _, ep := range parsed.NotFound.Episodes {
		out[mdblistNotFoundKey(ep.Show.IDs.IMDB, ep.Show.IDs.TMDB, ep.Season, ep.Number)] = true
	}
	return out
}

func mdblistNotFoundKey(imdb string, tmdb, season, number int) string {
	return fmt.Sprintf("%s|%d|%d:%d", imdb, tmdb, season, number)
}
