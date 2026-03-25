package user_settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"novastream/config"
	"novastream/internal/datastore"
	"novastream/models"
)

var (
	ErrStorageDirRequired = errors.New("storage directory not provided")
	ErrUserIDRequired     = errors.New("user id is required")
)

// Service manages persistence and retrieval of per-user settings.
type Service struct {
	mu       sync.RWMutex
	path     string
	store    *datastore.DataStore
	settings map[string]models.UserSettings
}

// useDB returns true when the service is backed by PostgreSQL.
func (s *Service) useDB() bool { return s.store != nil }

// NewServiceWithStore creates a user settings service backed by PostgreSQL.
func NewServiceWithStore(store *datastore.DataStore) (*Service, error) {
	svc := &Service{
		store:    store,
		settings: make(map[string]models.UserSettings),
	}
	if err := svc.load(); err != nil {
		return nil, err
	}
	return svc, nil
}

// NewService creates a user settings service storing data inside the provided directory.
func NewService(storageDir string) (*Service, error) {
	if strings.TrimSpace(storageDir) == "" {
		return nil, ErrStorageDirRequired
	}

	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		return nil, fmt.Errorf("create user settings dir: %w", err)
	}

	svc := &Service{
		path:     filepath.Join(storageDir, "user_settings.json"),
		settings: make(map[string]models.UserSettings),
	}

	if err := svc.load(); err != nil {
		return nil, err
	}

	return svc, nil
}

// sanitizeLanguageCode strips stray quotes and whitespace from language codes.
// Some frontends may wrap values in single quotes (e.g., "'eng'" instead of "eng").
func sanitizeLanguageCode(code string) string {
	code = strings.TrimSpace(code)
	code = strings.Trim(code, "'\"")
	code = strings.TrimSpace(code)
	return code
}

// normalizeSubtitleMode maps legacy subtitle mode values to canonical ones.
func normalizeSubtitleMode(mode string) string {
	switch mode {
	case "auto":
		return "forced-only"
	case "always":
		return "on"
	case "":
		return "off"
	default:
		return mode
	}
}

// Get returns the user's settings, or nil if not set.
func (s *Service) Get(userID string) (*models.UserSettings, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUserIDRequired
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if settings, ok := s.settings[userID]; ok {
		copy := settings
		return &copy, nil
	}

	return nil, nil
}

// HasOverrides returns true if the user has custom settings stored.
func (s *Service) HasOverrides(userID string) bool {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	_, exists := s.settings[userID]
	return exists
}

// GetUsersWithOverrides returns a map of userID -> hasOverrides for all users
// that have custom settings stored.
func (s *Service) GetUsersWithOverrides() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]bool)
	for userID := range s.settings {
		result[userID] = true
	}
	return result
}

// GetWithDefaults returns the user's settings merged with defaults.
// If the user has no custom settings, returns the defaults.
// If the user has settings but is missing fields, those are filled from defaults.
func (s *Service) GetWithDefaults(userID string, defaults models.UserSettings) (models.UserSettings, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return models.UserSettings{}, ErrUserIDRequired
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if settings, ok := s.settings[userID]; ok {
		log.Printf("[user-settings] GetWithDefaults(%q): found in cache, raw subMode=%q, defaults subMode=%q",
			userID, settings.Playback.PreferredSubtitleMode, defaults.Playback.PreferredSubtitleMode)
		// Sanitize language codes (strip stray quotes/whitespace)
		settings.Playback.PreferredAudioLanguage = sanitizeLanguageCode(settings.Playback.PreferredAudioLanguage)
		settings.Playback.PreferredSubtitleLanguage = sanitizeLanguageCode(settings.Playback.PreferredSubtitleLanguage)
		settings.Playback.PreferredSubtitleMode = strings.TrimSpace(strings.Trim(settings.Playback.PreferredSubtitleMode, "'\""))

		// Fill in missing Playback fields from defaults
		// Empty strings indicate "not set" and should inherit from defaults
		if settings.Playback.PreferredPlayer == "" {
			settings.Playback.PreferredPlayer = defaults.Playback.PreferredPlayer
		}
		if settings.Playback.PreferredAudioLanguage == "" {
			settings.Playback.PreferredAudioLanguage = sanitizeLanguageCode(defaults.Playback.PreferredAudioLanguage)
		}
		if settings.Playback.PreferredSubtitleLanguage == "" {
			settings.Playback.PreferredSubtitleLanguage = sanitizeLanguageCode(defaults.Playback.PreferredSubtitleLanguage)
		}
		if settings.Playback.PreferredSubtitleMode == "" {
			settings.Playback.PreferredSubtitleMode = defaults.Playback.PreferredSubtitleMode
		}
		settings.Playback.PreferredSubtitleMode = normalizeSubtitleMode(settings.Playback.PreferredSubtitleMode)
		log.Printf("[user-settings] GetWithDefaults(%q): final subMode=%q", userID, settings.Playback.PreferredSubtitleMode)
		// SubtitleSize of 0 means "use default"
		if settings.Playback.SubtitleSize == 0 {
			settings.Playback.SubtitleSize = defaults.Playback.SubtitleSize
		}

		// Fill in missing Display section from defaults
		if settings.Display.BadgeVisibility == nil {
			settings.Display = defaults.Display
		}
		if shelves, changed := models.EnsureDefaultHomeShelves(settings.HomeShelves.Shelves); changed {
			settings.HomeShelves.Shelves = shelves
		}
		return settings, nil
	}

	// Sanitize defaults too
	defaults.Playback.PreferredAudioLanguage = sanitizeLanguageCode(defaults.Playback.PreferredAudioLanguage)
	defaults.Playback.PreferredSubtitleLanguage = sanitizeLanguageCode(defaults.Playback.PreferredSubtitleLanguage)
	return defaults, nil
}

// Update saves the user's settings.
// If the settings are empty (no actual overrides), the user entry is deleted instead.
func (s *Service) Update(userID string, settings models.UserSettings) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ErrUserIDRequired
	}

	// Sanitize language codes on save to prevent stray quotes from persisting
	settings.Playback.PreferredAudioLanguage = sanitizeLanguageCode(settings.Playback.PreferredAudioLanguage)
	settings.Playback.PreferredSubtitleLanguage = sanitizeLanguageCode(settings.Playback.PreferredSubtitleLanguage)
	settings.Playback.PreferredSubtitleMode = strings.TrimSpace(strings.Trim(settings.Playback.PreferredSubtitleMode, "'\""))

	log.Printf("[user-settings] Update(%q): subMode=%q, audioLang=%q, subLang=%q",
		userID, settings.Playback.PreferredSubtitleMode, settings.Playback.PreferredAudioLanguage, settings.Playback.PreferredSubtitleLanguage)

	s.mu.Lock()
	defer s.mu.Unlock()

	// If settings are empty, delete the entry instead of saving
	if isSettingsEmpty(settings) {
		log.Printf("[user-settings] Update(%q): settings empty, deleting entry", userID)
		delete(s.settings, userID)
	} else {
		log.Printf("[user-settings] Update(%q): storing in cache", userID)
		s.settings[userID] = settings
	}

	return s.saveLocked()
}

// isSettingsEmpty checks if user settings have no actual values set.
// Empty arrays, zero values, and empty strings are considered "not set".
func isSettingsEmpty(s models.UserSettings) bool {
	// Check Playback - if any field is non-default, not empty
	if s.Playback.PreferredPlayer != "" ||
		s.Playback.PreferredAudioLanguage != "" ||
		s.Playback.PreferredSubtitleLanguage != "" ||
		s.Playback.PreferredSubtitleMode != "" ||
		s.Playback.PauseWhenAppInactive ||
		s.Playback.UseLoadingScreen ||
		s.Playback.SubtitleSize != 0 {
		return false
	}

	// Check HomeShelves
	if len(s.HomeShelves.Shelves) > 0 {
		return false
	}

	// Check Filtering - pointer fields
	if s.Filtering.MaxSizeMovieGB != nil ||
		s.Filtering.MaxSizeEpisodeGB != nil ||
		s.Filtering.MaxResolution != "" ||
		s.Filtering.HDRDVPolicy != "" ||
		len(s.Filtering.FilterOutTerms) > 0 ||
		len(s.Filtering.PreferredTerms) > 0 {
		return false
	}

	// Check LiveTV
	if len(s.LiveTV.HiddenChannels) > 0 ||
		len(s.LiveTV.FavoriteChannels) > 0 ||
		len(s.LiveTV.SelectedCategories) > 0 ||
		s.LiveTV.Mode != nil ||
		s.LiveTV.PlaylistURL != nil ||
		s.LiveTV.XtreamHost != nil ||
		s.LiveTV.XtreamUsername != nil ||
		s.LiveTV.XtreamPassword != nil ||
		s.LiveTV.MaxStreams != nil ||
		s.LiveTV.PlaylistCacheTTLHours != nil ||
		s.LiveTV.ProbeSizeMB != nil ||
		s.LiveTV.AnalyzeDurationSec != nil ||
		s.LiveTV.LowLatency != nil ||
		s.LiveTV.Filtering != nil ||
		s.LiveTV.EPG != nil {
		return false
	}

	// Check Display
	if len(s.Display.BadgeVisibility) > 0 ||
		s.Display.BypassFilteringForAIOStreamsOnly != nil {
		return false
	}

	// Check Playback - MaxResultsPerResolution
	if s.Playback.MaxResultsPerResolution != nil {
		return false
	}

	// Check Network
	if s.Network.HomeWifiSSID != "" ||
		s.Network.HomeBackendUrl != "" ||
		s.Network.RemoteBackendUrl != "" {
		return false
	}

	// Check AnimeFiltering
	if s.AnimeFiltering.AnimeLanguageEnabled != nil ||
		s.AnimeFiltering.AnimePreferredLanguage != nil {
		return false
	}

	// Check Ranking
	if s.Ranking != nil && len(s.Ranking.Criteria) > 0 {
		return false
	}

	// Check Calendar
	if s.Calendar.Watchlist != nil ||
		s.Calendar.History != nil ||
		s.Calendar.Trending != nil ||
		s.Calendar.TopTrending != nil ||
		s.Calendar.MDBLists != nil ||
		len(s.Calendar.MDBListShelves) > 0 {
		return false
	}

	return true
}

// Delete removes a user's settings.
func (s *Service) Delete(userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ErrUserIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.settings[userID]; !exists {
		return nil
	}

	delete(s.settings, userID)

	return s.saveLocked()
}

func (s *Service) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.useDB() {
		settings, err := s.store.UserSettings().List(context.Background())
		if err != nil {
			return fmt.Errorf("load user settings from db: %w", err)
		}
		s.settings = settings
		if s.settings == nil {
			s.settings = make(map[string]models.UserSettings)
		}

		// Apply migrations to DB-loaded settings
		needsSave := false
		for userID, us := range s.settings {
			changed := false
			if shelves, shelvesChanged := models.EnsureDefaultHomeShelves(us.HomeShelves.Shelves); shelvesChanged {
				us.HomeShelves.Shelves = shelves
				changed = true
			}
			for i := range us.HomeShelves.Shelves {
				id := us.HomeShelves.Shelves[i].ID
				if (id == "trending-movies" || id == "trending-tv") && us.HomeShelves.Shelves[i].HideUnreleased {
					us.HomeShelves.Shelves[i].HideUnreleased = false
					changed = true
				}
			}
			if changed {
				s.settings[userID] = us
				needsSave = true
			}
		}

		for userID, us := range s.settings {
			log.Printf("[user-settings] load: user=%q subMode=%q audioLang=%q subLang=%q",
				userID, us.Playback.PreferredSubtitleMode, us.Playback.PreferredAudioLanguage, us.Playback.PreferredSubtitleLanguage)
		}

		if needsSave {
			log.Printf("[user-settings] persisting migrated user settings to db")
			if err := s.syncToDB(); err != nil {
				log.Printf("[user-settings] warning: failed to persist migration: %v", err)
			}
		}
		return nil
	}

	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.settings = make(map[string]models.UserSettings)
		return nil
	}
	if err != nil {
		return fmt.Errorf("open user settings: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read user settings: %w", err)
	}
	if len(data) == 0 {
		s.settings = make(map[string]models.UserSettings)
		return nil
	}

	// First decode into raw map to apply migrations before struct unmarshalling
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMap); err != nil {
		return fmt.Errorf("decode user settings (raw): %w", err)
	}
	needsSave := false
	for userID, rawUser := range rawMap {
		var userRaw map[string]interface{}
		if err := json.Unmarshal(rawUser, &userRaw); err != nil {
			continue
		}
		// Check if migration is needed (old field exists in filtering)
		if filteringRaw, ok := userRaw["filtering"].(map[string]interface{}); ok {
			if _, has := filteringRaw["bypassFilteringForAioStreamsOnly"]; has {
				config.MigrateRawUserSettings(userRaw)
				migrated, _ := json.Marshal(userRaw)
				rawMap[userID] = migrated
				needsSave = true
			} else if _, has := filteringRaw["maxResultsPerResolution"]; has {
				config.MigrateRawUserSettings(userRaw)
				migrated, _ := json.Marshal(userRaw)
				rawMap[userID] = migrated
				needsSave = true
			}
		}
	}
	// Re-encode and decode into typed map
	migratedData, err := json.Marshal(rawMap)
	if err != nil {
		return fmt.Errorf("re-encode user settings: %w", err)
	}

	var settings map[string]models.UserSettings
	if err := json.Unmarshal(migratedData, &settings); err != nil {
		return fmt.Errorf("decode user settings: %w", err)
	}

	// Migrate: force hideUnreleased=false on trending shelves (curated lists handle this now)
	for userID, us := range settings {
		changed := false
		if shelves, shelvesChanged := models.EnsureDefaultHomeShelves(us.HomeShelves.Shelves); shelvesChanged {
			us.HomeShelves.Shelves = shelves
			changed = true
			needsSave = true
		}
		for i := range us.HomeShelves.Shelves {
			id := us.HomeShelves.Shelves[i].ID
			if (id == "trending-movies" || id == "trending-tv") && us.HomeShelves.Shelves[i].HideUnreleased {
				us.HomeShelves.Shelves[i].HideUnreleased = false
				changed = true
			}
		}
		if changed {
			settings[userID] = us
		}
	}

	s.settings = settings
	for userID, us := range s.settings {
		log.Printf("[user-settings] load: user=%q subMode=%q audioLang=%q subLang=%q",
			userID, us.Playback.PreferredSubtitleMode, us.Playback.PreferredAudioLanguage, us.Playback.PreferredSubtitleLanguage)
	}

	// Persist migrated data so migration only runs once
	if needsSave {
		log.Printf("[user-settings] persisting migrated user settings")
		if err := s.saveLocked(); err != nil {
			log.Printf("[user-settings] warning: failed to persist migration: %v", err)
		}
	}

	return nil
}

func (s *Service) saveLocked() error {
	if s.useDB() {
		return s.syncToDB()
	}

	tmp := s.path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create user settings temp file: %w", err)
	}

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s.settings); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("encode user settings: %w", err)
	}

	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync user settings: %w", err)
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close user settings temp file: %w", err)
	}

	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace user settings file: %w", err)
	}

	return nil
}

// syncToDB writes the full in-memory user settings state to PostgreSQL.
func (s *Service) syncToDB() error {
	ctx := context.Background()
	return s.store.WithTx(ctx, func(tx *datastore.Tx) error {
		// Get existing DB state to detect deletes
		existing, err := tx.UserSettings().List(ctx)
		if err != nil {
			return err
		}
		dbIDs := make(map[string]bool, len(existing))
		for id := range existing {
			dbIDs[id] = true
		}
		// Upsert all in-memory settings
		for userID, us := range s.settings {
			settings := us
			if err := tx.UserSettings().Upsert(ctx, userID, &settings); err != nil {
				return err
			}
			delete(dbIDs, userID)
		}
		// Delete settings removed from memory
		for id := range dbIDs {
			if err := tx.UserSettings().Delete(ctx, id); err != nil {
				return err
			}
		}
		return nil
	})
}
