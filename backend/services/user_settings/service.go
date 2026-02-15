package user_settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

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
	settings map[string]models.UserSettings
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
		// Fill in missing Playback fields from defaults
		// Empty strings indicate "not set" and should inherit from defaults
		if settings.Playback.PreferredPlayer == "" {
			settings.Playback.PreferredPlayer = defaults.Playback.PreferredPlayer
		}
		if settings.Playback.PreferredAudioLanguage == "" {
			settings.Playback.PreferredAudioLanguage = defaults.Playback.PreferredAudioLanguage
		}
		if settings.Playback.PreferredSubtitleLanguage == "" {
			settings.Playback.PreferredSubtitleLanguage = defaults.Playback.PreferredSubtitleLanguage
		}
		if settings.Playback.PreferredSubtitleMode == "" {
			settings.Playback.PreferredSubtitleMode = defaults.Playback.PreferredSubtitleMode
		}
		// SubtitleSize of 0 means "use default"
		if settings.Playback.SubtitleSize == 0 {
			settings.Playback.SubtitleSize = defaults.Playback.SubtitleSize
		}

		// Fill in missing Display section from defaults
		if settings.Display.BadgeVisibility == nil {
			settings.Display = defaults.Display
		}
		return settings, nil
	}

	return defaults, nil
}

// Update saves the user's settings.
// If the settings are empty (no actual overrides), the user entry is deleted instead.
func (s *Service) Update(userID string, settings models.UserSettings) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ErrUserIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// If settings are empty, delete the entry instead of saving
	if isSettingsEmpty(settings) {
		delete(s.settings, userID)
	} else {
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
		s.Filtering.PrioritizeHdr != nil ||
		len(s.Filtering.FilterOutTerms) > 0 ||
		len(s.Filtering.PreferredTerms) > 0 ||
		s.Filtering.BypassFilteringForAIOStreamsOnly != nil {
		return false
	}

	// Check LiveTV
	if len(s.LiveTV.HiddenChannels) > 0 ||
		len(s.LiveTV.FavoriteChannels) > 0 ||
		len(s.LiveTV.SelectedCategories) > 0 {
		return false
	}

	// Check Display
	if len(s.Display.BadgeVisibility) > 0 {
		return false
	}

	// Check Network
	if s.Network.HomeWifiSSID != "" ||
		s.Network.HomeBackendUrl != "" ||
		s.Network.RemoteBackendUrl != "" {
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

	var settings map[string]models.UserSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("decode user settings: %w", err)
	}

	// Migrate: force hideUnreleased=false on trending shelves (curated lists handle this now)
	for userID, us := range settings {
		changed := false
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
	return nil
}

func (s *Service) saveLocked() error {
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
