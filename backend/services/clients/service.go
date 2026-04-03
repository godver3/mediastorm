package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	ErrClientIDRequired   = errors.New("client id is required")
	ErrClientNotFound     = errors.New("client not found")
	ErrUserNotFound       = errors.New("user not found")
)

// Service manages persistence of client devices.
type Service struct {
	mu      sync.RWMutex
	path    string
	store   *datastore.DataStore
	clients map[string]models.Client
}

// useDB returns true when the service is backed by PostgreSQL.
func (s *Service) useDB() bool { return s.store != nil }

// NewServiceWithStore creates a clients service backed by PostgreSQL.
func NewServiceWithStore(store *datastore.DataStore) (*Service, error) {
	svc := &Service{
		store:   store,
		clients: make(map[string]models.Client),
	}
	if err := svc.load(); err != nil {
		return nil, err
	}
	return svc, nil
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

	// When backed by PostgreSQL, verify the user exists before inserting
	// to avoid FK constraint violations on clients.user_id → users.id.
	if s.useDB() && userID != "" {
		u, err := s.store.Users().Get(context.Background(), userID)
		if err != nil {
			return models.Client{}, fmt.Errorf("check user: %w", err)
		}
		if u == nil {
			return models.Client{}, ErrUserNotFound
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()

	if existing, ok := s.clients[id]; ok {
		// Update existing client — only overwrite UserID when a
		// non-empty value is provided so that re-registration from
		// AuthContext (which omits userId) doesn't blank it out and
		// violate the FK constraint on the clients table.
		if userID != "" {
			existing.UserID = userID
		}
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

	// New clients require a userId (FK constraint on clients.user_id → users.id).
	if userID == "" {
		return models.Client{}, errors.New("userId is required for new client registration")
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

// ReassignUser changes a client's associated profile/user.
func (s *Service) ReassignUser(id, newUserID string) (models.Client, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return models.Client{}, ErrClientIDRequired
	}

	newUserID = strings.TrimSpace(newUserID)
	if newUserID == "" {
		return models.Client{}, errors.New("new user ID is required")
	}

	if s.useDB() {
		u, err := s.store.Users().Get(context.Background(), newUserID)
		if err != nil {
			return models.Client{}, fmt.Errorf("check user: %w", err)
		}
		if u == nil {
			return models.Client{}, ErrUserNotFound
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	client, ok := s.clients[id]
	if !ok {
		return models.Client{}, ErrClientNotFound
	}

	client.UserID = newUserID
	s.clients[id] = client

	if err := s.saveLocked(); err != nil {
		return models.Client{}, err
	}

	return client, nil
}

func (s *Service) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.useDB() {
		clients, err := s.store.Clients().List(context.Background())
		if err != nil {
			return fmt.Errorf("load clients from db: %w", err)
		}
		s.clients = make(map[string]models.Client, len(clients))
		for _, c := range clients {
			s.clients[c.ID] = c
		}
		return nil
	}

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
	if s.useDB() {
		return s.syncToDB()
	}

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

func (s *Service) pruneInvalidClientsLocked(validUserIDs map[string]struct{}) []models.Client {
	var removed []models.Client
	for id, c := range s.clients {
		if _, ok := validUserIDs[c.UserID]; ok {
			continue
		}
		delete(s.clients, id)
		removed = append(removed, c)
	}
	return removed
}

// syncToDB writes the full in-memory clients state to PostgreSQL.
func (s *Service) syncToDB() error {
	ctx := context.Background()
	return s.store.WithTx(ctx, func(tx *datastore.Tx) error {
		users, err := tx.Users().List(ctx)
		if err != nil {
			return fmt.Errorf("list users: %w", err)
		}
		validUserIDs := make(map[string]struct{}, len(users))
		for _, u := range users {
			validUserIDs[u.ID] = struct{}{}
		}
		for _, c := range s.pruneInvalidClientsLocked(validUserIDs) {
			log.Printf("[clients] pruning orphaned client %q for missing user %q", c.ID, c.UserID)
		}

		// Get existing DB state to detect deletes
		existing, err := tx.Clients().List(ctx)
		if err != nil {
			return err
		}
		dbIDs := make(map[string]bool, len(existing))
		for _, c := range existing {
			dbIDs[c.ID] = true
		}
		// Upsert all in-memory clients
		for _, c := range s.clients {
			client := c
			if dbIDs[c.ID] {
				if err := tx.Clients().Update(ctx, &client); err != nil {
					return err
				}
			} else {
				if err := tx.Clients().Create(ctx, &client); err != nil {
					return err
				}
			}
			delete(dbIDs, c.ID)
		}
		// Delete clients removed from memory
		for id := range dbIDs {
			if err := tx.Clients().Delete(ctx, id); err != nil {
				return err
			}
		}
		return nil
	})
}
