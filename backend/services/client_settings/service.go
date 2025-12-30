package client_settings

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
	ErrClientIDRequired   = errors.New("client id is required")
)

// Service manages persistence of per-client filter settings.
type Service struct {
	mu       sync.RWMutex
	path     string
	settings map[string]models.ClientFilterSettings
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
