package content_preferences

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"novastream/models"
)

var (
	ErrStorageDirRequired = errors.New("storage directory not provided")
	ErrUserIDRequired     = errors.New("user id is required")
	ErrContentIDRequired  = errors.New("content id is required")
)

// Service persists per-content audio and subtitle preferences.
type Service struct {
	mu          sync.RWMutex
	path        string
	preferences map[string]map[string]models.ContentPreference // userID -> contentID -> preference
}

// NewService constructs a content preferences service backed by a JSON file on disk.
func NewService(storageDir string) (*Service, error) {
	if strings.TrimSpace(storageDir) == "" {
		return nil, ErrStorageDirRequired
	}

	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		return nil, fmt.Errorf("create content preferences dir: %w", err)
	}

	svc := &Service{
		path:        filepath.Join(storageDir, "content_preferences.json"),
		preferences: make(map[string]map[string]models.ContentPreference),
	}

	if err := svc.load(); err != nil {
		return nil, err
	}

	return svc, nil
}

// Get retrieves the content preference for a specific content item.
// Returns nil if no preference is set.
func (s *Service) Get(userID, contentID string) (*models.ContentPreference, error) {
	userID = strings.TrimSpace(userID)
	contentID = strings.TrimSpace(strings.ToLower(contentID))

	if userID == "" {
		return nil, ErrUserIDRequired
	}
	if contentID == "" {
		return nil, ErrContentIDRequired
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	perUser, ok := s.preferences[userID]
	if !ok {
		return nil, nil
	}

	pref, ok := perUser[contentID]
	if !ok {
		return nil, nil
	}

	return &pref, nil
}

// Set creates or updates a content preference.
func (s *Service) Set(userID string, pref models.ContentPreference) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ErrUserIDRequired
	}

	contentID := strings.TrimSpace(strings.ToLower(pref.ContentID))
	if contentID == "" {
		return ErrContentIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	perUser := s.ensureUserLocked(userID)

	// Normalize the content ID
	pref.ContentID = contentID
	pref.UpdatedAt = time.Now().UTC()

	perUser[contentID] = pref

	return s.saveLocked()
}

// Delete removes a content preference.
func (s *Service) Delete(userID, contentID string) error {
	userID = strings.TrimSpace(userID)
	contentID = strings.TrimSpace(strings.ToLower(contentID))

	if userID == "" {
		return ErrUserIDRequired
	}
	if contentID == "" {
		return ErrContentIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	perUser, ok := s.preferences[userID]
	if !ok {
		return nil // Nothing to delete
	}

	delete(perUser, contentID)

	// Clean up empty user maps
	if len(perUser) == 0 {
		delete(s.preferences, userID)
	}

	return s.saveLocked()
}

// List returns all content preferences for a user.
func (s *Service) List(userID string) ([]models.ContentPreference, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUserIDRequired
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	perUser, ok := s.preferences[userID]
	if !ok {
		return []models.ContentPreference{}, nil
	}

	result := make([]models.ContentPreference, 0, len(perUser))
	for _, pref := range perUser {
		result = append(result, pref)
	}

	// Sort by most recently updated
	sort.Slice(result, func(i, j int) bool {
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})

	return result, nil
}

// ensureUserLocked creates the per-user map if it doesn't exist.
// Must be called with s.mu held.
func (s *Service) ensureUserLocked(userID string) map[string]models.ContentPreference {
	perUser, ok := s.preferences[userID]
	if !ok {
		perUser = make(map[string]models.ContentPreference)
		s.preferences[userID] = perUser
	}
	return perUser
}

// load reads the preferences from disk.
func (s *Service) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.preferences = make(map[string]map[string]models.ContentPreference)
		return nil
	}
	if err != nil {
		return fmt.Errorf("open content preferences: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read content preferences: %w", err)
	}
	if len(data) == 0 {
		s.preferences = make(map[string]map[string]models.ContentPreference)
		return nil
	}

	// Load as map[userID][]ContentPreference (array format for storage)
	var loaded map[string][]models.ContentPreference
	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("decode content preferences: %w", err)
	}

	s.preferences = make(map[string]map[string]models.ContentPreference)
	for userID, items := range loaded {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			continue
		}
		perUser := make(map[string]models.ContentPreference, len(items))
		for _, pref := range items {
			// Normalize content ID to lowercase
			contentID := strings.ToLower(pref.ContentID)
			pref.ContentID = contentID
			perUser[contentID] = pref
		}
		s.preferences[userID] = perUser
	}

	log.Printf("[content_preferences] loaded preferences for %d users", len(s.preferences))
	return nil
}

// saveLocked writes the preferences to disk.
// Must be called with s.mu held.
func (s *Service) saveLocked() error {
	// Convert to array format for storage
	toSave := make(map[string][]models.ContentPreference)
	for userID, perUser := range s.preferences {
		items := make([]models.ContentPreference, 0, len(perUser))
		for _, pref := range perUser {
			items = append(items, pref)
		}
		// Sort by most recently updated
		sort.Slice(items, func(i, j int) bool {
			return items[i].UpdatedAt.After(items[j].UpdatedAt)
		})
		toSave[userID] = items
	}

	data, err := json.MarshalIndent(toSave, "", "  ")
	if err != nil {
		return fmt.Errorf("encode content preferences: %w", err)
	}

	if err := os.WriteFile(s.path, data, 0o644); err != nil {
		return fmt.Errorf("write content preferences: %w", err)
	}

	return nil
}
