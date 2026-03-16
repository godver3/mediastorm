package client_settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"novastream/internal/datastore"
	"novastream/models"
)

var (
	ErrStorageDirRequired = errors.New("storage directory not provided")
	ErrClientIDRequired   = errors.New("client id is required")
)

// Service manages persistence of per-client filter settings.
type Service struct {
	mu       sync.RWMutex
	path     string
	store    *datastore.DataStore
	settings map[string]models.ClientFilterSettings
}

// useDB returns true when the service is backed by PostgreSQL.
func (s *Service) useDB() bool { return s.store != nil }

// NewServiceWithStore creates a client settings service backed by PostgreSQL.
func NewServiceWithStore(store *datastore.DataStore) (*Service, error) {
	svc := &Service{
		store:    store,
		settings: make(map[string]models.ClientFilterSettings),
	}
	if err := svc.load(); err != nil {
		return nil, err
	}
	return svc, nil
}

// NewService creates a client settings service storing data inside the provided directory.
func NewService(storageDir string) (*Service, error) {
	if strings.TrimSpace(storageDir) == "" {
		return nil, ErrStorageDirRequired
	}

	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		return nil, fmt.Errorf("create client settings dir: %w", err)
	}

	svc := &Service{
		path:     filepath.Join(storageDir, "client_settings.json"),
		settings: make(map[string]models.ClientFilterSettings),
	}

	if err := svc.load(); err != nil {
		return nil, err
	}

	return svc, nil
}

// Get returns the client's filter settings, or nil if not set.
func (s *Service) Get(clientID string) (*models.ClientFilterSettings, error) {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return nil, ErrClientIDRequired
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if settings, ok := s.settings[clientID]; ok {
		copy := settings
		return &copy, nil
	}

	return nil, nil
}

// Update saves the client's filter settings.
func (s *Service) Update(clientID string, settings models.ClientFilterSettings) error {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return ErrClientIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// If all settings are nil, remove the entry instead
	if settings.IsEmpty() {
		delete(s.settings, clientID)
	} else {
		s.settings[clientID] = settings
	}

	return s.saveLocked()
}

// GetAll returns a copy of all client settings.
func (s *Service) GetAll() map[string]models.ClientFilterSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]models.ClientFilterSettings, len(s.settings))
	for k, v := range s.settings {
		out[k] = v
	}
	return out
}

// UpdateBatch replaces all client settings with the provided map and saves.
// Empty entries are removed.
func (s *Service) UpdateBatch(settings map[string]models.ClientFilterSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cleaned := make(map[string]models.ClientFilterSettings, len(settings))
	for k, v := range settings {
		if !v.IsEmpty() {
			cleaned[k] = v
		}
	}
	s.settings = cleaned
	return s.saveLocked()
}

// Delete removes a client's filter settings.
func (s *Service) Delete(clientID string) error {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return ErrClientIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.settings[clientID]; !exists {
		return nil
	}

	delete(s.settings, clientID)

	return s.saveLocked()
}

func (s *Service) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.useDB() {
		settings, err := s.store.ClientSettings().List(context.Background())
		if err != nil {
			return fmt.Errorf("load client settings from db: %w", err)
		}
		s.settings = settings
		return nil
	}

	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.settings = make(map[string]models.ClientFilterSettings)
		return nil
	}
	if err != nil {
		return fmt.Errorf("open client settings: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read client settings: %w", err)
	}
	if len(data) == 0 {
		s.settings = make(map[string]models.ClientFilterSettings)
		return nil
	}

	var settings map[string]models.ClientFilterSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("decode client settings: %w", err)
	}

	s.settings = settings
	return nil
}

func (s *Service) saveLocked() error {
	if s.useDB() {
		return s.syncToDB()
	}

	tmp := s.path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create client settings temp file: %w", err)
	}

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s.settings); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("encode client settings: %w", err)
	}

	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync client settings: %w", err)
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close client settings temp file: %w", err)
	}

	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace client settings file: %w", err)
	}

	return nil
}

// syncToDB writes the full in-memory client settings state to PostgreSQL.
func (s *Service) syncToDB() error {
	ctx := context.Background()
	return s.store.WithTx(ctx, func(tx *datastore.Tx) error {
		// Get existing DB state to detect deletes
		existing, err := tx.ClientSettings().List(ctx)
		if err != nil {
			return err
		}
		dbIDs := make(map[string]bool, len(existing))
		for id := range existing {
			dbIDs[id] = true
		}
		// Upsert all in-memory settings
		for clientID, settings := range s.settings {
			cs := settings
			if err := tx.ClientSettings().Upsert(ctx, clientID, &cs); err != nil {
				return err
			}
			delete(dbIDs, clientID)
		}
		// Delete settings removed from memory
		for id := range dbIDs {
			if err := tx.ClientSettings().Delete(ctx, id); err != nil {
				return err
			}
		}
		return nil
	})
}
