package clients

import (
	"encoding/json"
	"errors"
	"fmt"
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
	ErrClientIDRequired   = errors.New("client id is required")
	ErrClientNotFound     = errors.New("client not found")
)

// Service manages persistence of client devices.
type Service struct {
	mu      sync.RWMutex
	path    string
	clients map[string]models.Client
}

// NewService creates a clients service storing data inside the provided directory.
func NewService(storageDir string) (*Service, error) {
	if strings.TrimSpace(storageDir) == "" {
		return nil, ErrStorageDirRequired
	}

	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		return nil, fmt.Errorf("create clients dir: %w", err)
	}

	svc := &Service{
		path:    filepath.Join(storageDir, "clients.json"),
		clients: make(map[string]models.Client),
	}

	if err := svc.load(); err != nil {
		return nil, err
	}

	return svc, nil
}

// Register registers or updates a client device.
// If the client already exists, it updates LastSeenAt and device info.
// If new, it creates the client with auto-generated name.
func (s *Service) Register(id, userID, deviceType, os, appVersion string) (models.Client, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return models.Client{}, ErrClientIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()

	if existing, ok := s.clients[id]; ok {
		// Update existing client
		existing.UserID = userID
		existing.DeviceType = deviceType
		existing.OS = os
		existing.AppVersion = appVersion
		existing.LastSeenAt = now
		s.clients[id] = existing

		if err := s.saveLocked(); err != nil {
			return models.Client{}, err
		}
		return existing, nil
	}

	// Create new client with auto-generated name
	client := models.Client{
		ID:            id,
		UserID:        userID,
		Name:          generateClientName(deviceType, os),
		DeviceType:    deviceType,
		OS:            os,
		AppVersion:    appVersion,
		FirstSeenAt:   now,
		LastSeenAt:    now,
		FilterEnabled: false, // Disabled by default
	}

	s.clients[id] = client

	if err := s.saveLocked(); err != nil {
		return models.Client{}, err
	}

	return client, nil
}

// generateClientName creates a display name like "iPhone - iOS" or "Apple TV - tvOS"
func generateClientName(deviceType, os string) string {
	if deviceType == "" && os == "" {
		return "Unknown Device"
	}
	if deviceType == "" {
		return os
	}
	if os == "" {
		return deviceType
	}
	return fmt.Sprintf("%s - %s", deviceType, os)
}

// Get returns a client by ID.
func (s *Service) Get(id string) (*models.Client, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrClientIDRequired
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if client, ok := s.clients[id]; ok {
		copy := client
		return &copy, nil
	}

	return nil, nil
}

// List returns all clients sorted by last seen time (most recent first).
func (s *Service) List() []models.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()

	clients := make([]models.Client, 0, len(s.clients))
	for _, c := range s.clients {
		clients = append(clients, c)
	}

	sort.Slice(clients, func(i, j int) bool {
		return clients[i].LastSeenAt.After(clients[j].LastSeenAt)
	})

	return clients
}

// ListByUser returns all clients for a specific user/profile.
func (s *Service) ListByUser(userID string) []models.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()

	clients := make([]models.Client, 0)
	for _, c := range s.clients {
		if c.UserID == userID {
			clients = append(clients, c)
		}
	}

	sort.Slice(clients, func(i, j int) bool {
		return clients[i].LastSeenAt.After(clients[j].LastSeenAt)
	})

	return clients
}

// Rename updates a client's display name.
func (s *Service) Rename(id, name string) (models.Client, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return models.Client{}, ErrClientIDRequired
	}

	name = strings.TrimSpace(name)
	if name == "" {
		return models.Client{}, errors.New("name is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	client, ok := s.clients[id]
	if !ok {
		return models.Client{}, ErrClientNotFound
	}

	client.Name = name
	s.clients[id] = client

	if err := s.saveLocked(); err != nil {
		return models.Client{}, err
	}

	return client, nil
}

// SetFilterEnabled enables or disables custom filtering for a client.
func (s *Service) SetFilterEnabled(id string, enabled bool) (models.Client, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return models.Client{}, ErrClientIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	client, ok := s.clients[id]
	if !ok {
		return models.Client{}, ErrClientNotFound
	}

	client.FilterEnabled = enabled
	s.clients[id] = client

	if err := s.saveLocked(); err != nil {
		return models.Client{}, err
	}

	return client, nil
}

// UpdateLastSeen updates the last seen timestamp for a client.
func (s *Service) UpdateLastSeen(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrClientIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	client, ok := s.clients[id]
	if !ok {
		return nil // Silently ignore unknown clients
	}

	client.LastSeenAt = time.Now().UTC()
	s.clients[id] = client

	return s.saveLocked()
}

// Delete removes a client by ID.
func (s *Service) Delete(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrClientIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.clients[id]; !ok {
		return ErrClientNotFound
	}

	delete(s.clients, id)

	return s.saveLocked()
}

func (s *Service) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.clients = make(map[string]models.Client)
		return nil
	}
	if err != nil {
		return fmt.Errorf("open clients file: %w", err)
	}
	defer file.Close()

	var clients map[string]models.Client
	if err := json.NewDecoder(file).Decode(&clients); err != nil {
		return fmt.Errorf("decode clients: %w", err)
	}

	s.clients = clients
	return nil
}

func (s *Service) saveLocked() error {
	tmp := s.path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create clients temp file: %w", err)
	}

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s.clients); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("encode clients: %w", err)
	}

	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync clients: %w", err)
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close clients temp file: %w", err)
	}

	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace clients file: %w", err)
	}

	return nil
}
