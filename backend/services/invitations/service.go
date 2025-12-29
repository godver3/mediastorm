package invitations

import (
	"crypto/rand"
	"encoding/base64"
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

	"novastream/models"
)

var (
	ErrStorageDirRequired  = errors.New("storage directory not provided")
	ErrInvitationNotFound  = errors.New("invitation not found")
	ErrInvitationExpired   = errors.New("invitation has expired")
	ErrInvitationUsed      = errors.New("invitation has already been used")
	ErrInvalidToken        = errors.New("invalid invitation token")
)

const (
	// DefaultExpirationDuration is how long invitations are valid by default (7 days)
	DefaultExpirationDuration = 7 * 24 * time.Hour
	// TokenLength is the length of the generated token in bytes (before base64 encoding)
	TokenLength = 32
)

// Service manages invitation links for account creation.
type Service struct {
	mu          sync.RWMutex
	path        string
	invitations map[string]models.Invitation
}

// NewService creates an invitations service storing data inside the provided directory.
func NewService(storageDir string) (*Service, error) {
	if strings.TrimSpace(storageDir) == "" {
		return nil, ErrStorageDirRequired
	}

	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		return nil, fmt.Errorf("create invitations dir: %w", err)
	}

	svc := &Service{
		path:        filepath.Join(storageDir, "invitations.json"),
		invitations: make(map[string]models.Invitation),
	}

	if err := svc.load(); err != nil {
		return nil, err
	}

	return svc, nil
}

// Create generates a new invitation token.
func (s *Service) Create(createdBy string, expiresIn time.Duration) (models.Invitation, error) {
	if expiresIn <= 0 {
		expiresIn = DefaultExpirationDuration
	}

	// Generate a secure random token
	tokenBytes := make([]byte, TokenLength)
	if _, err := rand.Read(tokenBytes); err != nil {
		return models.Invitation{}, fmt.Errorf("generate token: %w", err)
	}
	token := base64.URLEncoding.EncodeToString(tokenBytes)

	s.mu.Lock()
	defer s.mu.Unlock()

	id := uuid.NewString()
	now := time.Now().UTC()
	invitation := models.Invitation{
		ID:        id,
		Token:     token,
		CreatedBy: createdBy,
		ExpiresAt: now.Add(expiresIn),
		CreatedAt: now,
	}

	s.invitations[id] = invitation

	if err := s.saveLocked(); err != nil {
		delete(s.invitations, id)
		return models.Invitation{}, err
	}

	return invitation, nil
}

// GetByToken finds an invitation by its token.
func (s *Service) GetByToken(token string) (models.Invitation, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return models.Invitation{}, ErrInvalidToken
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, inv := range s.invitations {
		if inv.Token == token {
			return inv, nil
		}
	}

	return models.Invitation{}, ErrInvitationNotFound
}

// Validate checks if an invitation token is valid (exists, not expired, not used).
func (s *Service) Validate(token string) error {
	inv, err := s.GetByToken(token)
	if err != nil {
		return err
	}

	if inv.UsedAt != nil {
		return ErrInvitationUsed
	}

	if time.Now().After(inv.ExpiresAt) {
		return ErrInvitationExpired
	}

	return nil
}

// MarkUsed marks an invitation as used.
func (s *Service) MarkUsed(token string, usedBy string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var foundID string
	for id, inv := range s.invitations {
		if inv.Token == token {
			foundID = id
			break
		}
	}

	if foundID == "" {
		return ErrInvitationNotFound
	}

	inv := s.invitations[foundID]
	if inv.UsedAt != nil {
		return ErrInvitationUsed
	}

	now := time.Now().UTC()
	inv.UsedAt = &now
	inv.UsedBy = usedBy
	s.invitations[foundID] = inv

	return s.saveLocked()
}

// List returns all invitations, sorted by creation time (newest first).
func (s *Service) List() []models.Invitation {
	s.mu.RLock()
	defer s.mu.RUnlock()

	invitations := make([]models.Invitation, 0, len(s.invitations))
	for _, inv := range s.invitations {
		invitations = append(invitations, inv)
	}

	sort.Slice(invitations, func(i, j int) bool {
		return invitations[i].CreatedAt.After(invitations[j].CreatedAt)
	})

	return invitations
}

// Delete removes an invitation.
func (s *Service) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.invitations[id]; !ok {
		return ErrInvitationNotFound
	}

	delete(s.invitations, id)
	return s.saveLocked()
}

// CleanupExpired removes expired and used invitations older than the given duration.
func (s *Service) CleanupExpired(olderThan time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-olderThan)
	var count int

	for id, inv := range s.invitations {
		// Delete if expired and older than cutoff, or if used and older than cutoff
		if (time.Now().After(inv.ExpiresAt) && inv.ExpiresAt.Before(cutoff)) ||
			(inv.UsedAt != nil && inv.UsedAt.Before(cutoff)) {
			delete(s.invitations, id)
			count++
		}
	}

	if count > 0 {
		if err := s.saveLocked(); err != nil {
			return 0, err
		}
	}

	return count, nil
}

func (s *Service) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open invitations file: %w", err)
	}
	defer file.Close()

	var stored []models.Invitation
	if err := json.NewDecoder(file).Decode(&stored); err != nil {
		return fmt.Errorf("decode invitations: %w", err)
	}

	s.invitations = make(map[string]models.Invitation, len(stored))
	for _, inv := range stored {
		if strings.TrimSpace(inv.ID) == "" {
			continue
		}
		s.invitations[inv.ID] = inv
	}

	return nil
}

func (s *Service) saveLocked() error {
	invitations := make([]models.Invitation, 0, len(s.invitations))
	for _, inv := range s.invitations {
		invitations = append(invitations, inv)
	}

	sort.Slice(invitations, func(i, j int) bool {
		return invitations[i].CreatedAt.Before(invitations[j].CreatedAt)
	})

	tmp := s.path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create invitations temp file: %w", err)
	}

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(invitations); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("encode invitations: %w", err)
	}

	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync invitations: %w", err)
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close invitations temp file: %w", err)
	}

	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace invitations file: %w", err)
	}

	return nil
}
