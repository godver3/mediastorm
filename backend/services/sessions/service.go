package sessions

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"novastream/internal/datastore"
	"novastream/models"
)

var (
	ErrSessionNotFound    = errors.New("session not found")
	ErrSessionExpired     = errors.New("session expired")
	ErrInvalidToken       = errors.New("invalid token")
	ErrStorageDirRequired = errors.New("storage directory not provided")
)

const (
	// DefaultSessionDuration is the default lifetime of a session.
	DefaultSessionDuration = 30 * 24 * time.Hour // 30 days

	// PersistentSessionDuration is the lifetime of a "remember me" session (100 years).
	PersistentSessionDuration = 100 * 365 * 24 * time.Hour

	// TokenLength is the number of random bytes used for session tokens.
	TokenLength = 32
)

// Service manages session tokens for authenticated accounts.
type Service struct {
	mu              sync.RWMutex
	path            string
	store           *datastore.DataStore
	sessions        map[string]models.Session
	sessionDuration time.Duration
}

// useDB returns true when the service is backed by PostgreSQL.
func (s *Service) useDB() bool { return s.store != nil }

// NewServiceWithStore creates a sessions service backed by PostgreSQL.
func NewServiceWithStore(store *datastore.DataStore, defaultDuration time.Duration) (*Service, error) {
	if defaultDuration <= 0 {
		defaultDuration = DefaultSessionDuration
	}

	svc := &Service{
		store:           store,
		sessions:        make(map[string]models.Session),
		sessionDuration: defaultDuration,
	}

	if err := svc.load(); err != nil {
		return nil, err
	}

	go svc.cleanupLoop()

	return svc, nil
}

// NewService creates a new sessions service with persistence.
// storageDir is the directory where sessions.json will be stored.
// If storageDir is empty, sessions will only be stored in memory (not recommended).
func NewService(storageDir string, sessionDuration time.Duration) (*Service, error) {
	if sessionDuration <= 0 {
		sessionDuration = DefaultSessionDuration
	}

	svc := &Service{
		sessions:        make(map[string]models.Session),
		sessionDuration: sessionDuration,
	}

	// Set up persistence if storage directory is provided
	if strings.TrimSpace(storageDir) != "" {
		if err := os.MkdirAll(storageDir, 0o755); err != nil {
			return nil, fmt.Errorf("create sessions dir: %w", err)
		}
		svc.path = filepath.Join(storageDir, "sessions.json")

		// Load existing sessions from disk
		if err := svc.load(); err != nil {
			return nil, err
		}
	}

	// Start background cleanup goroutine
	go svc.cleanupLoop()

	return svc, nil
}

// Create generates a new session for the given account.
func (s *Service) Create(accountID string, isMaster bool, userAgent, ipAddress string) (models.Session, error) {
	return s.CreateWithDuration(accountID, isMaster, userAgent, ipAddress, s.sessionDuration)
}

// CreatePersistent generates a new persistent (never expires) session for the given account.
func (s *Service) CreatePersistent(accountID string, isMaster bool, userAgent, ipAddress string) (models.Session, error) {
	return s.CreateWithDuration(accountID, isMaster, userAgent, ipAddress, PersistentSessionDuration)
}

// CreateWithDuration generates a new session with a custom duration.
func (s *Service) CreateWithDuration(accountID string, isMaster bool, userAgent, ipAddress string, duration time.Duration) (models.Session, error) {
	token, err := generateToken()
	if err != nil {
		return models.Session{}, err
	}

	now := time.Now().UTC()
	session := models.Session{
		Token:     token,
		AccountID: accountID,
		IsMaster:  isMaster,
		ExpiresAt: now.Add(duration),
		CreatedAt: now,
		UserAgent: userAgent,
		IPAddress: ipAddress,
	}

	s.mu.Lock()
	s.sessions[token] = session
	if err := s.saveLocked(); err != nil {
		delete(s.sessions, token)
		s.mu.Unlock()
		return models.Session{}, err
	}
	s.mu.Unlock()

	return session, nil
}

// Validate checks if a token is valid and returns the associated session.
func (s *Service) Validate(token string) (models.Session, error) {
	if token == "" {
		return models.Session{}, ErrInvalidToken
	}

	s.mu.RLock()
	session, ok := s.sessions[token]
	s.mu.RUnlock()

	if !ok {
		return models.Session{}, ErrSessionNotFound
	}

	if session.IsExpired() {
		// Clean up expired session
		s.mu.Lock()
		delete(s.sessions, token)
		s.mu.Unlock()
		return models.Session{}, ErrSessionExpired
	}

	return session, nil
}

// Revoke invalidates a session by its token.
func (s *Service) Revoke(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessions[token]; !ok {
		return ErrSessionNotFound
	}

	delete(s.sessions, token)
	return s.saveLocked()
}

// RevokeAllForAccount invalidates all sessions for an account.
func (s *Service) RevokeAllForAccount(accountID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := 0
	for token, session := range s.sessions {
		if session.AccountID == accountID {
			delete(s.sessions, token)
			count++
		}
	}
	if count > 0 {
		_ = s.saveLocked()
	}
	return count
}

// GetSessionsForAccount returns all active sessions for an account.
func (s *Service) GetSessionsForAccount(accountID string) []models.Session {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var sessions []models.Session
	for _, session := range s.sessions {
		if session.AccountID == accountID && !session.IsExpired() {
			sessions = append(sessions, session)
		}
	}
	return sessions
}

// Refresh extends a session's expiration time.
func (s *Service) Refresh(token string) (models.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[token]
	if !ok {
		return models.Session{}, ErrSessionNotFound
	}

	if session.IsExpired() {
		delete(s.sessions, token)
		_ = s.saveLocked()
		return models.Session{}, ErrSessionExpired
	}

	session.ExpiresAt = time.Now().UTC().Add(s.sessionDuration)
	s.sessions[token] = session
	_ = s.saveLocked()

	return session, nil
}

// Cleanup removes all expired sessions.
func (s *Service) Cleanup() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := 0
	now := time.Now()
	for token, session := range s.sessions {
		if now.After(session.ExpiresAt) {
			delete(s.sessions, token)
			count++
		}
	}
	if count > 0 {
		_ = s.saveLocked()
	}
	return count
}

// cleanupLoop periodically removes expired sessions.
func (s *Service) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		s.Cleanup()
	}
}

// generateToken creates a cryptographically secure random token.
func generateToken() (string, error) {
	bytes := make([]byte, TokenLength)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(bytes), nil
}

// Count returns the total number of active sessions.
func (s *Service) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}

// load reads sessions from the JSON file on disk (or from the database).
func (s *Service) load() error {
	if s.useDB() {
		sessions, err := s.store.Sessions().List(context.Background())
		if err != nil {
			return fmt.Errorf("load sessions from db: %w", err)
		}
		s.sessions = make(map[string]models.Session, len(sessions))
		for _, sess := range sessions {
			s.sessions[sess.Token] = sess
		}
		return nil
	}

	if s.path == "" {
		return nil // No persistence configured
	}

	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil // No sessions file yet, start fresh
	}
	if err != nil {
		return fmt.Errorf("open sessions file: %w", err)
	}
	defer file.Close()

	var stored []models.Session
	dec := json.NewDecoder(file)
	if err := dec.Decode(&stored); err != nil {
		return fmt.Errorf("decode sessions: %w", err)
	}

	// Load sessions, filtering out expired ones
	now := time.Now()
	s.sessions = make(map[string]models.Session, len(stored))
	for _, session := range stored {
		if strings.TrimSpace(session.Token) == "" {
			continue
		}
		if now.After(session.ExpiresAt) {
			continue // Skip expired sessions
		}
		s.sessions[session.Token] = session
	}

	return nil
}

// saveLocked writes sessions to the JSON file (or syncs to DB). Must be called with mu held.
func (s *Service) saveLocked() error {
	if s.useDB() {
		return s.syncToDB()
	}

	if s.path == "" {
		return nil // No persistence configured
	}

	// Convert map to slice for JSON encoding
	sessions := make([]models.Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}

	// Write to temp file first, then rename (atomic write)
	tmp := s.path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create sessions temp file: %w", err)
	}

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(sessions); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("encode sessions: %w", err)
	}

	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync sessions: %w", err)
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close sessions temp file: %w", err)
	}

	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace sessions file: %w", err)
	}

	return nil
}

// syncToDB writes the full in-memory sessions state to PostgreSQL.
func (s *Service) syncToDB() error {
	ctx := context.Background()
	return s.store.WithTx(ctx, func(tx *datastore.Tx) error {
		// Get existing DB state to detect deletes
		existing, err := tx.Sessions().List(ctx)
		if err != nil {
			return err
		}
		dbTokens := make(map[string]bool, len(existing))
		for _, sess := range existing {
			dbTokens[sess.Token] = true
		}
		// Upsert all in-memory sessions
		for _, sess := range s.sessions {
			s := sess
			if dbTokens[sess.Token] {
				// Session exists in DB — delete and re-create (sessions have no Update method)
				if err := tx.Sessions().Delete(ctx, s.Token); err != nil {
					return err
				}
			}
			if err := tx.Sessions().Create(ctx, &s); err != nil {
				return err
			}
			delete(dbTokens, sess.Token)
		}
		// Delete sessions removed from memory
		for token := range dbTokens {
			if err := tx.Sessions().Delete(ctx, token); err != nil {
				return err
			}
		}
		return nil
	})
}
