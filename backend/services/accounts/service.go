package accounts

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
	ErrStorageDirRequired   = errors.New("storage directory not provided")
	ErrUsernameRequired     = errors.New("username is required")
	ErrPasswordRequired     = errors.New("password is required")
	ErrAccountNotFound      = errors.New("account not found")
	ErrUsernameExists       = errors.New("username already exists")
	ErrInvalidCredentials   = errors.New("invalid username or password")
	ErrCannotDeleteMaster   = errors.New("cannot delete the master account")
	ErrCannotDeleteLastAcct = errors.New("cannot delete the last account")
)

const (
	// DefaultMasterPassword is the initial password for the master account.
	// Users should be warned to change this immediately.
	DefaultMasterPassword = "admin"
)

// Service manages persistence of user accounts.
type Service struct {
	mu       sync.RWMutex
	path     string
	accounts map[string]models.Account
}

// NewService creates an accounts service storing data inside the provided directory.
func NewService(storageDir string) (*Service, error) {
	if strings.TrimSpace(storageDir) == "" {
		return nil, ErrStorageDirRequired
	}

	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		return nil, fmt.Errorf("create accounts dir: %w", err)
	}

	svc := &Service{
		path:     filepath.Join(storageDir, "accounts.json"),
		accounts: make(map[string]models.Account),
	}

	if err := svc.load(); err != nil {
		return nil, err
	}

	if err := svc.ensureMasterAccount(); err != nil {
		return nil, err
	}

	return svc, nil
}

// List returns all accounts sorted by creation time.
func (s *Service) List() []models.Account {
	s.mu.RLock()
	defer s.mu.RUnlock()

	accounts := make([]models.Account, 0, len(s.accounts))
	for _, a := range s.accounts {
		accounts = append(accounts, a)
	}

	sort.Slice(accounts, func(i, j int) bool {
		// Master account first, then by creation time
		if accounts[i].IsMaster != accounts[j].IsMaster {
			return accounts[i].IsMaster
		}
		return accounts[i].CreatedAt.Before(accounts[j].CreatedAt)
	})

	return accounts
}

// Get returns the account with the given ID if present.
func (s *Service) Get(id string) (models.Account, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return models.Account{}, false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	account, ok := s.accounts[id]
	return account, ok
}

// GetByUsername returns the account with the given username if present.
func (s *Service) GetByUsername(username string) (models.Account, bool) {
	username = strings.TrimSpace(strings.ToLower(username))
	if username == "" {
		return models.Account{}, false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, a := range s.accounts {
		if strings.ToLower(a.Username) == username {
			return a, true
		}
	}
	return models.Account{}, false
}

// Exists reports whether an account with the provided ID is registered.
func (s *Service) Exists(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	_, ok := s.accounts[id]
	return ok
}

// Create registers a new account with the provided username and password.
func (s *Service) Create(username, password string) (models.Account, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return models.Account{}, ErrUsernameRequired
	}

	password = strings.TrimSpace(password)
	if password == "" {
		return models.Account{}, ErrPasswordRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if username already exists (case-insensitive)
	lowerUsername := strings.ToLower(username)
	for _, a := range s.accounts {
		if strings.ToLower(a.Username) == lowerUsername {
			return models.Account{}, ErrUsernameExists
		}
	}

	// Hash the password
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return models.Account{}, fmt.Errorf("hash password: %w", err)
	}

	id := uuid.NewString()
	now := time.Now().UTC()
	account := models.Account{
		ID:           id,
		Username:     username,
		PasswordHash: string(hash),
		IsMaster:     false,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	s.accounts[id] = account

	if err := s.saveLocked(); err != nil {
		delete(s.accounts, id)
		return models.Account{}, err
	}

	return account, nil
}

// Authenticate verifies the username and password, returning the account if valid.
func (s *Service) Authenticate(username, password string) (models.Account, error) {
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)

	if username == "" || password == "" {
		return models.Account{}, ErrInvalidCredentials
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Find account by username (case-insensitive)
	lowerUsername := strings.ToLower(username)
	var account models.Account
	found := false
	for _, a := range s.accounts {
		if strings.ToLower(a.Username) == lowerUsername {
			account = a
			found = true
			break
		}
	}

	if !found {
		// Use bcrypt comparison anyway to prevent timing attacks
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$dummy"), []byte(password))
		return models.Account{}, ErrInvalidCredentials
	}

	// Verify password
	if err := bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(password)); err != nil {
		return models.Account{}, ErrInvalidCredentials
	}

	return account, nil
}

// Rename changes the username for an account.
func (s *Service) Rename(id, newUsername string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrAccountNotFound
	}

	newUsername = strings.TrimSpace(newUsername)
	if newUsername == "" {
		return ErrUsernameRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	account, ok := s.accounts[id]
	if !ok {
		return ErrAccountNotFound
	}

	// Check if new username already exists (case-insensitive), excluding current account
	lowerUsername := strings.ToLower(newUsername)
	for _, a := range s.accounts {
		if a.ID != id && strings.ToLower(a.Username) == lowerUsername {
			return ErrUsernameExists
		}
	}

	account.Username = newUsername
	account.UpdatedAt = time.Now().UTC()
	s.accounts[id] = account

	return s.saveLocked()
}

// UpdatePassword changes the password for an account.
func (s *Service) UpdatePassword(id, newPassword string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrAccountNotFound
	}

	newPassword = strings.TrimSpace(newPassword)
	if newPassword == "" {
		return ErrPasswordRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	account, ok := s.accounts[id]
	if !ok {
		return ErrAccountNotFound
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	account.PasswordHash = string(hash)
	account.UpdatedAt = time.Now().UTC()
	s.accounts[id] = account

	return s.saveLocked()
}


// Delete removes an account by ID. The master account cannot be deleted.
func (s *Service) Delete(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrAccountNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	account, ok := s.accounts[id]
	if !ok {
		return ErrAccountNotFound
	}

	if account.IsMaster {
		return ErrCannotDeleteMaster
	}

	if len(s.accounts) <= 1 {
		return ErrCannotDeleteLastAcct
	}

	delete(s.accounts, id)

	return s.saveLocked()
}

// GetMasterAccount returns the master account.
func (s *Service) GetMasterAccount() (models.Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, a := range s.accounts {
		if a.IsMaster {
			return a, true
		}
	}
	return models.Account{}, false
}

// HasDefaultPassword checks if the master account still has the default password.
func (s *Service) HasDefaultPassword() bool {
	master, ok := s.GetMasterAccount()
	if !ok {
		return false
	}

	err := bcrypt.CompareHashAndPassword([]byte(master.PasswordHash), []byte(DefaultMasterPassword))
	return err == nil
}

// ensureMasterAccount creates the master account if it doesn't exist.
func (s *Service) ensureMasterAccount() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if master account exists
	for _, a := range s.accounts {
		if a.IsMaster {
			return nil
		}
	}

	// Create master account with default password
	hash, err := bcrypt.GenerateFromPassword([]byte(DefaultMasterPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash default password: %w", err)
	}

	now := time.Now().UTC()
	master := models.Account{
		ID:           "master",
		Username:     models.MasterAccountUsername,
		PasswordHash: string(hash),
		IsMaster:     true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	s.accounts[master.ID] = master

	return s.saveLocked()
}

func (s *Service) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open accounts file: %w", err)
	}
	defer file.Close()

	dec := json.NewDecoder(file)
	var stored []models.AccountStorage
	if err := dec.Decode(&stored); err != nil {
		return fmt.Errorf("decode accounts: %w", err)
	}

	s.accounts = make(map[string]models.Account, len(stored))
	for _, accountStorage := range stored {
		if strings.TrimSpace(accountStorage.ID) == "" {
			continue
		}
		account := accountStorage.ToAccount()
		if account.CreatedAt.IsZero() {
			account.CreatedAt = time.Now().UTC()
		}
		if account.UpdatedAt.IsZero() {
			account.UpdatedAt = account.CreatedAt
		}
		s.accounts[account.ID] = account
	}

	return nil
}

func (s *Service) saveLocked() error {
	// Convert to storage format (includes password hash)
	storage := make([]models.AccountStorage, 0, len(s.accounts))
	for _, account := range s.accounts {
		storage = append(storage, account.ToStorage())
	}

	sort.Slice(storage, func(i, j int) bool {
		if storage[i].IsMaster != storage[j].IsMaster {
			return storage[i].IsMaster
		}
		return storage[i].CreatedAt.Before(storage[j].CreatedAt)
	})

	tmp := s.path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create accounts temp file: %w", err)
	}

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(storage); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("encode accounts: %w", err)
	}

	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync accounts: %w", err)
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close accounts temp file: %w", err)
	}

	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace accounts file: %w", err)
	}

	return nil
}
