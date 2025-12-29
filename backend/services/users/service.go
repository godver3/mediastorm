package users

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

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"novastream/models"
)

var (
	ErrStorageDirRequired = errors.New("storage directory not provided")
	ErrNameRequired       = errors.New("name is required")
	ErrUserNotFound       = errors.New("user not found")
	ErrPinRequired        = errors.New("PIN is required")
	ErrPinInvalid         = errors.New("invalid PIN")
	ErrPinTooShort        = errors.New("PIN must be at least 4 characters")
)

// Service manages persistence of NovaStream user profiles.
type Service struct {
	mu    sync.RWMutex
	path  string
	users map[string]models.User
}

// NewService creates a users service storing data inside the provided directory.
func NewService(storageDir string) (*Service, error) {
	if strings.TrimSpace(storageDir) == "" {
		return nil, ErrStorageDirRequired
	}

	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		return nil, fmt.Errorf("create users dir: %w", err)
	}

	svc := &Service{
		path:  filepath.Join(storageDir, "users.json"),
		users: make(map[string]models.User),
	}

	if err := svc.load(); err != nil {
		return nil, err
	}

	if err := svc.ensureDefaultUser(); err != nil {
		return nil, err
	}

	return svc, nil
}

// List returns all users sorted by creation time, then name.
// Deprecated: Use ListForAccount or ListAll instead for account-scoped access.
func (s *Service) List() []models.User {
	return s.ListAll()
}

// ListAll returns all users sorted by creation time, then name.
// This should only be used by master accounts.
func (s *Service) ListAll() []models.User {
	s.mu.RLock()
	defer s.mu.RUnlock()

	users := make([]models.User, 0, len(s.users))
	for _, u := range s.users {
		users = append(users, u)
	}

	sort.Slice(users, func(i, j int) bool {
		if users[i].CreatedAt.Equal(users[j].CreatedAt) {
			return users[i].Name < users[j].Name
		}
		return users[i].CreatedAt.Before(users[j].CreatedAt)
	})

	return users
}

// ListForAccount returns users belonging to a specific account, sorted by creation time, then name.
func (s *Service) ListForAccount(accountID string) []models.User {
	s.mu.RLock()
	defer s.mu.RUnlock()

	users := make([]models.User, 0)
	for _, u := range s.users {
		if u.AccountID == accountID {
			users = append(users, u)
		}
	}

	sort.Slice(users, func(i, j int) bool {
		if users[i].CreatedAt.Equal(users[j].CreatedAt) {
			return users[i].Name < users[j].Name
		}
		return users[i].CreatedAt.Before(users[j].CreatedAt)
	})

	return users
}

// BelongsToAccount checks if a profile belongs to the specified account.
func (s *Service) BelongsToAccount(profileID, accountID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	user, ok := s.users[profileID]
	if !ok {
		return false
	}
	return user.AccountID == accountID
}

// Exists reports whether a user with the provided ID is registered.
func (s *Service) Exists(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	_, ok := s.users[id]
	return ok
}

// Get returns the user with the given ID if present.
func (s *Service) Get(id string) (models.User, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return models.User{}, false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	user, ok := s.users[id]
	return user, ok
}

// Create registers a new user with the provided name.
// Deprecated: Use CreateForAccount instead for account-scoped access.
func (s *Service) Create(name string) (models.User, error) {
	return s.CreateForAccount(models.DefaultAccountID, name)
}

// CreateForAccount registers a new user with the provided name under the specified account.
func (s *Service) CreateForAccount(accountID, name string) (models.User, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return models.User{}, ErrNameRequired
	}

	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		accountID = models.DefaultAccountID
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.createLocked(accountID, trimmed)
}

// Reassign moves a profile to a different account. This is a master-only operation.
func (s *Service) Reassign(profileID, newAccountID string) (models.User, error) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return models.User{}, ErrUserNotFound
	}

	newAccountID = strings.TrimSpace(newAccountID)
	if newAccountID == "" {
		return models.User{}, errors.New("account ID is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	user, ok := s.users[profileID]
	if !ok {
		return models.User{}, ErrUserNotFound
	}

	user.AccountID = newAccountID
	user.UpdatedAt = time.Now().UTC()
	s.users[profileID] = user

	if err := s.saveLocked(); err != nil {
		return models.User{}, err
	}

	return user, nil
}

// Rename updates the user's name.
func (s *Service) Rename(id, name string) (models.User, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return models.User{}, ErrUserNotFound
	}

	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return models.User{}, ErrNameRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	user, ok := s.users[id]
	if !ok {
		return models.User{}, ErrUserNotFound
	}

	user.Name = trimmed
	user.UpdatedAt = time.Now().UTC()
	s.users[id] = user

	if err := s.saveLocked(); err != nil {
		return models.User{}, err
	}

	return user, nil
}

// SetColor updates the user's color.
func (s *Service) SetColor(id, color string) (models.User, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return models.User{}, ErrUserNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	user, ok := s.users[id]
	if !ok {
		return models.User{}, ErrUserNotFound
	}

	user.Color = strings.TrimSpace(color)
	user.UpdatedAt = time.Now().UTC()
	s.users[id] = user

	if err := s.saveLocked(); err != nil {
		return models.User{}, err
	}

	return user, nil
}

// SetPin sets or updates the user's PIN.
func (s *Service) SetPin(id, pin string) (models.User, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return models.User{}, ErrUserNotFound
	}

	pin = strings.TrimSpace(pin)
	if pin == "" {
		return models.User{}, ErrPinRequired
	}
	if len(pin) < 4 {
		return models.User{}, ErrPinTooShort
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	user, ok := s.users[id]
	if !ok {
		return models.User{}, ErrUserNotFound
	}

	// Hash the PIN with bcrypt
	hash, err := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.DefaultCost)
	if err != nil {
		return models.User{}, fmt.Errorf("hash PIN: %w", err)
	}

	user.PinHash = string(hash)
	user.UpdatedAt = time.Now().UTC()
	s.users[id] = user

	if err := s.saveLocked(); err != nil {
		return models.User{}, err
	}

	return user, nil
}

// ClearPin removes the user's PIN.
func (s *Service) ClearPin(id string) (models.User, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return models.User{}, ErrUserNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	user, ok := s.users[id]
	if !ok {
		return models.User{}, ErrUserNotFound
	}

	user.PinHash = ""
	user.UpdatedAt = time.Now().UTC()
	s.users[id] = user

	if err := s.saveLocked(); err != nil {
		return models.User{}, err
	}

	return user, nil
}

// VerifyPin checks if the provided PIN matches the user's stored PIN hash.
// Returns nil if PIN is correct, ErrPinInvalid if incorrect, or ErrUserNotFound if user doesn't exist.
func (s *Service) VerifyPin(id, pin string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrUserNotFound
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	user, ok := s.users[id]
	if !ok {
		return ErrUserNotFound
	}

	// If no PIN is set, any PIN (or empty) is valid
	if user.PinHash == "" {
		return nil
	}

	// Verify the PIN against the hash
	if err := bcrypt.CompareHashAndPassword([]byte(user.PinHash), []byte(pin)); err != nil {
		return ErrPinInvalid
	}

	return nil
}

// HasPin returns true if the user has a PIN set.
func (s *Service) HasPin(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	user, ok := s.users[id]
	if !ok {
		return false
	}

	return user.PinHash != ""
}

// SetKidsProfile sets whether this is a kids profile.
func (s *Service) SetKidsProfile(id string, isKids bool) (models.User, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return models.User{}, ErrUserNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	user, ok := s.users[id]
	if !ok {
		return models.User{}, ErrUserNotFound
	}

	user.IsKidsProfile = isKids
	user.UpdatedAt = time.Now().UTC()
	s.users[id] = user

	if err := s.saveLocked(); err != nil {
		return models.User{}, err
	}

	return user, nil
}

// SetTraktAccountID associates a Trakt account with the user.
func (s *Service) SetTraktAccountID(id, traktAccountID string) (models.User, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return models.User{}, ErrUserNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	user, ok := s.users[id]
	if !ok {
		return models.User{}, ErrUserNotFound
	}

	user.TraktAccountID = strings.TrimSpace(traktAccountID)
	user.UpdatedAt = time.Now().UTC()
	s.users[id] = user

	if err := s.saveLocked(); err != nil {
		return models.User{}, err
	}

	return user, nil
}

// ClearTraktAccountID removes the Trakt account association from the user.
func (s *Service) ClearTraktAccountID(id string) (models.User, error) {
	return s.SetTraktAccountID(id, "")
}

// GetUsersByTraktAccountID returns all users that have the specified Trakt account linked.
func (s *Service) GetUsersByTraktAccountID(traktAccountID string) []models.User {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var users []models.User
	for _, user := range s.users {
		if user.TraktAccountID == traktAccountID {
			users = append(users, user)
		}
	}
	return users
}

// SetPlexAccountID associates a Plex account with the user.
func (s *Service) SetPlexAccountID(id, plexAccountID string) (models.User, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return models.User{}, ErrUserNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	user, ok := s.users[id]
	if !ok {
		return models.User{}, ErrUserNotFound
	}

	user.PlexAccountID = strings.TrimSpace(plexAccountID)
	user.UpdatedAt = time.Now().UTC()
	s.users[id] = user

	if err := s.saveLocked(); err != nil {
		return models.User{}, err
	}

	return user, nil
}

// ClearPlexAccountID removes the Plex account association from the user.
func (s *Service) ClearPlexAccountID(id string) (models.User, error) {
	return s.SetPlexAccountID(id, "")
}

// GetUsersByPlexAccountID returns all users that have the specified Plex account linked.
func (s *Service) GetUsersByPlexAccountID(plexAccountID string) []models.User {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var users []models.User
	for _, user := range s.users {
		if user.PlexAccountID == plexAccountID {
			users = append(users, user)
		}
	}
	return users
}

// Delete removes a user by ID. The last remaining user cannot be deleted.
func (s *Service) Delete(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrUserNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.users[id]; !ok {
		return ErrUserNotFound
	}

	if len(s.users) <= 1 {
		return fmt.Errorf("cannot delete the last user")
	}

	delete(s.users, id)

	return s.saveLocked()
}

func (s *Service) ensureDefaultUser() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Only create Primary Profile if there are no users at all
	if len(s.users) > 0 {
		return nil
	}

	_, err := s.createLocked(models.DefaultAccountID, models.DefaultUserName)
	return err
}

func (s *Service) createLocked(accountID, name string) (models.User, error) {
	id := uuid.NewString()

	if len(s.users) == 0 {
		id = models.DefaultUserID
	} else if _, exists := s.users[id]; exists {
		return models.User{}, fmt.Errorf("generated duplicate user id")
	}

	now := time.Now().UTC()
	user := models.User{
		ID:        id,
		AccountID: accountID,
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
	}

	s.users[user.ID] = user

	if err := s.saveLocked(); err != nil {
		return models.User{}, err
	}

	return user, nil
}

func (s *Service) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open users file: %w", err)
	}
	defer file.Close()

	dec := json.NewDecoder(file)
	var stored []models.User
	if err := dec.Decode(&stored); err != nil {
		return fmt.Errorf("decode users: %w", err)
	}

	s.users = make(map[string]models.User, len(stored))
	needsSave := false
	for _, user := range stored {
		if strings.TrimSpace(user.ID) == "" {
			continue
		}
		if user.CreatedAt.IsZero() {
			user.CreatedAt = time.Now().UTC()
		}
		if user.UpdatedAt.IsZero() {
			user.UpdatedAt = user.CreatedAt
		}
		// Migration: assign default account ID to profiles without one
		if user.AccountID == "" {
			user.AccountID = models.DefaultAccountID
			needsSave = true
		}
		s.users[user.ID] = user
	}

	// Save migrated data
	if needsSave {
		return s.saveLocked()
	}

	return nil
}

func (s *Service) saveLocked() error {
	users := make([]models.User, 0, len(s.users))
	for _, user := range s.users {
		users = append(users, user)
	}

	sort.Slice(users, func(i, j int) bool {
		if users[i].CreatedAt.Equal(users[j].CreatedAt) {
			return users[i].Name < users[j].Name
		}
		return users[i].CreatedAt.Before(users[j].CreatedAt)
	})

	tmp := s.path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create users temp file: %w", err)
	}

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(users); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("encode users: %w", err)
	}

	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync users: %w", err)
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close users temp file: %w", err)
	}

	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace users file: %w", err)
	}

	return nil
}
