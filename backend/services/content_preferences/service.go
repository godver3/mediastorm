package content_preferences

import (
	"context"
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

	"novastream/internal/datastore"
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
	store       *datastore.DataStore
	preferences map[string]map[string]models.ContentPreference // userID -> contentID -> preference
}

// useDB returns true when the service is backed by PostgreSQL.
func (s *Service) useDB() bool { return s.store != nil }

// NewServiceWithStore creates a content preferences service backed by PostgreSQL.
func NewServiceWithStore(store *datastore.DataStore) (*Service, error) {
	svc := &Service{
		store:       store,
		preferences: make(map[string]map[string]models.ContentPreference),
	}
	if err := svc.load(); err != nil {
		return nil, err
	}
	return svc, nil
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

	if s.useDB() {
		return s.store.ContentPreferences().Get(context.Background(), userID, contentID)
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

	// Normalize the content ID
	pref.ContentID = contentID
	pref.UpdatedAt = time.Now().UTC()

	if s.useDB() {
		return s.store.ContentPreferences().Upsert(context.Background(), userID, &pref)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	perUser := s.ensureUserLocked(userID)
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

	if s.useDB() {
		return s.store.ContentPreferences().Delete(context.Background(), userID, contentID)
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

	if s.useDB() {
		prefs, err := s.store.ContentPreferences().ListByUser(context.Background(), userID)
		if err != nil {
			return nil, err
		}
		if prefs == nil {
			return []models.ContentPreference{}, nil
		}
		// Sort by most recently updated
		sort.Slice(prefs, func(i, j int) bool {
			return prefs[i].UpdatedAt.After(prefs[j].UpdatedAt)
		})
		return prefs, nil
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

// load reads the preferences from disk (or verifies DB connectivity when backed by PostgreSQL).
func (s *Service) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.useDB() {
		// DB mode: preferences are read/written directly via the repository.
		// Verify connectivity with a count check.
		count, err := s.store.ContentPreferences().Count(context.Background())
		if err != nil {
			return fmt.Errorf("load content preferences from db: %w", err)
		}
		log.Printf("[content_preferences] database contains %d preferences", count)
		s.preferences = make(map[string]map[string]models.ContentPreference)
		return nil
	}

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

// saveLocked writes the preferences to disk (or to the database).
// Must be called with s.mu held.
func (s *Service) saveLocked() error {
	if s.useDB() {
		return s.syncToDB()
	}

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

// syncToDB writes the full in-memory content preferences state to PostgreSQL.
func (s *Service) syncToDB() error {
	ctx := context.Background()
	return s.store.WithTx(ctx, func(tx *datastore.Tx) error {
		for userID, perUser := range s.preferences {
			// Get existing DB state for this user to detect deletes
			existing, err := tx.ContentPreferences().ListByUser(ctx, userID)
			if err != nil {
				return err
			}
			dbIDs := make(map[string]bool, len(existing))
			for _, p := range existing {
				dbIDs[p.ContentID] = true
			}

			// Upsert all in-memory preferences for this user
			for _, pref := range perUser {
				p := pref
				if err := tx.ContentPreferences().Upsert(ctx, userID, &p); err != nil {
					return err
				}
				delete(dbIDs, p.ContentID)
			}

			// Delete preferences removed from memory
			for contentID := range dbIDs {
				if err := tx.ContentPreferences().Delete(ctx, userID, contentID); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
