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
	var itemsImported int

	switch task.Type {
	case config.ScheduledTaskTypePlexWatchlistSync:
		itemsImported, err = s.executePlexWatchlistSync(task)
	default:
		log.Printf("[scheduler] Unknown task type: %s", task.Type)
		return
	}

	// Update task status in settings
	s.updateTaskStatus(task.ID, err, itemsImported)
}

// updateTaskStatus updates a task's status in the settings file
func (s *Service) updateTaskStatus(taskID string, err error, itemsImported int) {
	settings, loadErr := s.configManager.Load()
	if loadErr != nil {
		log.Printf("[scheduler] Failed to load settings to update task status: %v", loadErr)
		return
	}

	now := time.Now().UTC()
	for i := range settings.ScheduledTasks.Tasks {
		if settings.ScheduledTasks.Tasks[i].ID == taskID {
			settings.ScheduledTasks.Tasks[i].LastRunAt = &now
			settings.ScheduledTasks.Tasks[i].ItemsImported = itemsImported

			if err != nil {
				settings.ScheduledTasks.Tasks[i].LastStatus = config.ScheduledTaskStatusError
				settings.ScheduledTasks.Tasks[i].LastError = err.Error()
				log.Printf("[scheduler] Task %s failed: %v", taskID, err)
			} else {
				settings.ScheduledTasks.Tasks[i].LastStatus = config.ScheduledTaskStatusSuccess
				settings.ScheduledTasks.Tasks[i].LastError = ""
				log.Printf("[scheduler] Task %s completed successfully, imported %d items", taskID, itemsImported)
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

// executePlexWatchlistSync syncs a Plex watchlist to a profile
func (s *Service) executePlexWatchlistSync(task config.ScheduledTask) (int, error) {
	plexAccountID := task.Config["plexAccountId"]
	profileID := task.Config["profileId"]

	if plexAccountID == "" || profileID == "" {
		return 0, errors.New("missing plexAccountId or profileId in task config")
	}

	// Load settings to get Plex account
	settings, err := s.configManager.Load()
	if err != nil {
		return 0, fmt.Errorf("load settings: %w", err)
	}

	plexAccount := settings.Plex.GetAccountByID(plexAccountID)
	if plexAccount == nil {
		return 0, errors.New("plex account not found")
	}

	if plexAccount.AuthToken == "" {
		return 0, errors.New("plex account not authenticated")
	}

	// Fetch watchlist from Plex
	items, err := s.plexClient.GetWatchlist(plexAccount.AuthToken)
	if err != nil {
		return 0, fmt.Errorf("fetch watchlist: %w", err)
	}

	if len(items) == 0 {
		return 0, nil
	}

	// Get external IDs for items (no progress callback)
	externalIDs := s.plexClient.GetWatchlistDetailsWithProgress(plexAccount.AuthToken, items, nil)

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

		input := models.WatchlistUpsert{
			ID:          itemID,
			MediaType:   mediaType,
			Name:        item.Title,
			Year:        item.Year,
			PosterURL:   plex.GetPosterURL(item.Thumb, plexAccount.AuthToken),
			BackdropURL: plex.GetPosterURL(item.Art, plexAccount.AuthToken),
			ExternalIDs: extIDs,
		}

		if _, err := s.watchlistService.AddOrUpdate(profileID, input); err != nil {
			log.Printf("[scheduler] Failed to import watchlist item %s: %v", item.Title, err)
			continue
		}

		imported++
	}

	return imported, nil
}
