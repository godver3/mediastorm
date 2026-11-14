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
	ErrRefreshNotAllowed  = errors.New("session scope cannot be refreshed")
	ErrInvalidToken       = errors.New("invalid token")
	ErrStorageDirRequired = errors.New("storage directory not provided")
)

const (
	// DefaultSessionDuration is the default lifetime of a session.
	DefaultSessionDuration = 30 * 24 * time.Hour // 30 days

	// PersistentSessionDuration is the bounded lifetime of a "remember me" session.
	PersistentSessionDuration = 90 * 24 * time.Hour

	// TokenLength is the number of random bytes used for session tokens.
	TokenLength = 32
)

// Service manages session tokens for authenticated accounts.
type Service struct {
	mu              sync.RWMutex
	path            string
	store           *datastore.DataStore
	sessionReader   sessionReader
	sessions        map[string]models.Session
	sessionDuration time.Duration
}

type sessionReader interface {
	Get(context.Context, string) (*models.Session, error)
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
		sessionReader:   store.Sessions(),
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

// CreateForClient generates a session bound to a stable app device ID.
func (s *Service) CreateForClient(accountID string, isMaster bool, userAgent, ipAddress, clientID string) (models.Session, error) {
	return s.createScopedWithResource(accountID, isMaster, userAgent, ipAddress, s.sessionDuration, "", "", clientID)
}

// CreatePersistent generates a longer-lived, bounded session for the given account.
func (s *Service) CreatePersistent(accountID string, isMaster bool, userAgent, ipAddress string) (models.Session, error) {
	return s.CreateWithDuration(accountID, isMaster, userAgent, ipAddress, PersistentSessionDuration)
}

// CreatePersistentForClient generates a persistent session bound to an app device.
func (s *Service) CreatePersistentForClient(accountID string, isMaster bool, userAgent, ipAddress, clientID string) (models.Session, error) {
	return s.createScopedWithResource(accountID, isMaster, userAgent, ipAddress, PersistentSessionDuration, "", "", clientID)
}

// CreateWithDuration generates a new session with a custom duration.
func (s *Service) CreateWithDuration(accountID string, isMaster bool, userAgent, ipAddress string, duration time.Duration) (models.Session, error) {
	return s.CreateScopedWithResource(accountID, isMaster, userAgent, ipAddress, duration, "", "")
}

// CreateScoped generates a new session with a custom duration and access scope.
// An empty scope grants full account access; models.SessionScopeStream limits the
// session to streaming/playback endpoints (used for one-time share links).
func (s *Service) CreateScoped(accountID string, isMaster bool, userAgent, ipAddress string, duration time.Duration, scope string) (models.Session, error) {
	return s.CreateScopedWithResource(accountID, isMaster, userAgent, ipAddress, duration, scope, "")
}

// CreateScopedWithResource generates a scoped session bound to a specific
// source resource. Stream-scoped share sessions use this to prevent a shared
// token from being reused against unrelated media/live URLs.
func (s *Service) CreateScopedWithResource(accountID string, isMaster bool, userAgent, ipAddress string, duration time.Duration, scope string, scopeResource string) (models.Session, error) {
	return s.createScopedWithResource(accountID, isMaster, userAgent, ipAddress, duration, scope, scopeResource, "")
}

func (s *Service) createScopedWithResource(accountID string, isMaster bool, userAgent, ipAddress string, duration time.Duration, scope string, scopeResource string, clientID string) (models.Session, error) {
	token, err := generateToken()
	if err != nil {
		return models.Session{}, err
	}

	now := time.Now().UTC()
	session := models.Session{
		Token:         token,
		AccountID:     accountID,
		IsMaster:      isMaster,
		ExpiresAt:     now.Add(duration),
		CreatedAt:     now,
		UserAgent:     userAgent,
		IPAddress:     ipAddress,
		Scope:         scope,
		ScopeResource: scopeResource,
		ClientID:      strings.TrimSpace(clientID),
	}

	s.mu.Lock()
	if s.useDB() {
		if err := s.store.Sessions().Create(context.Background(), &session); err != nil {
			s.mu.Unlock()
			return models.Session{}, err
		}
		s.sessions[token] = session
		s.mu.Unlock()
		return session, nil
	}

	s.sessions[token] = session
	if err := s.saveLocked(); err != nil {
		delete(s.sessions, token)
		s.mu.Unlock()
		return models.Session{}, err
	}
	s.mu.Unlock()

	return session, nil
}

// RevokeAllForClient invalidates every session issued to a physical app device.
func (s *Service) RevokeAllForClient(clientID string) int {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var tokens []string
	for token, session := range s.sessions {
		if session.ClientID == clientID {
			delete(s.sessions, token)
			tokens = append(tokens, token)
		}
	}
	if s.useDB() {
		for _, token := range tokens {
			_ = s.store.Sessions().Delete(context.Background(), token)
		}
	} else if len(tokens) > 0 {
		_ = s.saveLocked()
	}
	return len(tokens)
}

// GetSessionsForClient returns all active sessions bound to an app device.
func (s *Service) GetSessionsForClient(clientID string) []models.Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []models.Session
	for _, session := range s.sessions {
		if session.ClientID == clientID && !session.IsExpired() {
			result = append(result, session)
		}
	}
	return result
}

// BindClient attaches a legacy unbound session to the first app device that
// presents it. Existing bindings are immutable so a caller cannot move another
// device's session merely by changing the header.
func (s *Service) BindClient(token, clientID string) error {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[token]
	if !ok {
		return ErrSessionNotFound
	}
	if session.ClientID != "" {
		return nil
	}
	session.ClientID = clientID
	if s.useDB() {
		if err := s.store.Sessions().Update(context.Background(), &session); err != nil {
			return err
		}
		s.sessions[token] = session
		return nil
	}
	s.sessions[token] = session
	if err := s.saveLocked(); err != nil {
		session.ClientID = ""
		s.sessions[token] = session
		return err
	}
	return nil
}

// Validate checks if a token is valid and returns the associated session.
//
// Jellyfin bridge sessions are never returned here: their tokens travel in
// media URLs and must not unlock the admin/account UIs or account APIs that
// validate cookies and bearer tokens through this method. The bridge uses
// ValidateScoped instead.
func (s *Service) Validate(token string) (models.Session, error) {
	session, err := s.validate(token)
	if err != nil {
		return models.Session{}, err
	}
	if session.Scope == models.SessionScopeJellyfin {
		return models.Session{}, ErrSessionNotFound
	}
	return session, nil
}

// ValidateScoped validates a token that must carry exactly the given scope.
func (s *Service) ValidateScoped(token, scope string) (models.Session, error) {
	session, err := s.validate(token)
	if err != nil {
		return models.Session{}, err
	}
	if session.Scope != scope {
		return models.Session{}, ErrSessionNotFound
	}
	return session, nil
}

func (s *Service) validate(token string) (models.Session, error) {
	if token == "" {
		return models.Session{}, ErrInvalidToken
	}

	session, ok, err := s.sessionForValidation(token)
	if err != nil {
		return models.Session{}, fmt.Errorf("load session for validation: %w", err)
	}

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

// sessionForValidation treats PostgreSQL as authoritative when configured.
// Recovery and administrative revocation can happen in another process, so a
// session present only in this process's startup cache must not remain valid.
func (s *Service) sessionForValidation(token string) (models.Session, bool, error) {
	if s.sessionReader != nil {
		session, err := s.sessionReader.Get(context.Background(), token)
		if err != nil {
			return models.Session{}, false, err
		}

		s.mu.Lock()
		defer s.mu.Unlock()
		if session == nil {
			delete(s.sessions, token)
			return models.Session{}, false, nil
		}
		s.sessions[token] = *session
		return *session, true, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[token]
	return session, ok, nil
}

// Revoke invalidates a session by its token.
func (s *Service) Revoke(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessions[token]; !ok {
		return ErrSessionNotFound
	}

	delete(s.sessions, token)
	if s.useDB() {
		return s.store.Sessions().Delete(context.Background(), token)
	}
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
		if s.useDB() {
			_ = s.store.Sessions().DeleteByAccount(context.Background(), accountID)
			return count
		}
		_ = s.saveLocked()
	}
	return count
}

// RevokeAllForAccountExcept invalidates an account's other sessions, preserving
// the token used to perform a sensitive authenticated operation.
func (s *Service) RevokeAllForAccountExcept(accountID, keepToken string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := 0
	for token, session := range s.sessions {
		if session.AccountID == accountID && token != keepToken {
			delete(s.sessions, token)
			count++
		}
	}
	if count == 0 {
		return 0
	}
	if s.useDB() {
		_ = s.store.Sessions().DeleteByAccountExcept(context.Background(), accountID, keepToken)
		return count
	}
	_ = s.saveLocked()
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

// Refresh rotates the token and extends a normal account session. Scoped share
// sessions have an absolute expiry and cannot be refreshed.
func (s *Service) Refresh(token string) (models.Session, error) {
	return s.RefreshForClient(token, "")
}

// RefreshForClient rotates a session while binding it to the requesting device.
func (s *Service) RefreshForClient(token, clientID string) (models.Session, error) {
	newToken, err := generateToken()
	if err != nil {
		return models.Session{}, err
	}
	if _, err := s.Validate(token); err != nil {
		return models.Session{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[token]
	if !ok {
		return models.Session{}, ErrSessionNotFound
	}

	if session.IsExpired() {
		delete(s.sessions, token)
		if s.useDB() {
			_ = s.store.Sessions().Delete(context.Background(), token)
		} else {
			_ = s.saveLocked()
		}
		return models.Session{}, ErrSessionExpired
	}
	if session.Scope != "" {
		return models.Session{}, ErrRefreshNotAllowed
	}

	originalSession := session
	oldToken := session.Token
	session.Token = newToken
	session.ExpiresAt = time.Now().UTC().Add(s.sessionDuration)
	if clientID = strings.TrimSpace(clientID); clientID != "" && session.ClientID == "" {
		session.ClientID = clientID
	}
	if s.useDB() {
		if err := s.store.Sessions().Create(context.Background(), &session); err != nil {
			return models.Session{}, err
		}
		if err := s.store.Sessions().Delete(context.Background(), oldToken); err != nil {
			_ = s.store.Sessions().Delete(context.Background(), newToken)
			return models.Session{}, err
		}
	}
	delete(s.sessions, oldToken)
	s.sessions[newToken] = session
	if !s.useDB() {
		if err := s.saveLocked(); err != nil {
			delete(s.sessions, newToken)
			s.sessions[oldToken] = originalSession
			return models.Session{}, err
		}
	}

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
		if s.useDB() {
			_, _ = s.store.Sessions().DeleteExpired(context.Background())
		} else {
			_ = s.saveLocked()
		}
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

// SessionLookupFunc loads a session by token; it returns (nil, nil) when the
// session does not exist.
type SessionLookupFunc func(ctx context.Context, token string) (*models.Session, error)

func (f SessionLookupFunc) Get(ctx context.Context, token string) (*models.Session, error) {
	return f(ctx, token)
}

// NewServiceWithLookup creates an in-memory service whose validation reads
// through lookup, as the PostgreSQL-backed service does. It lets callers test
// how they handle session store failures.
func NewServiceWithLookup(lookup SessionLookupFunc, sessionDuration time.Duration) *Service {
	if sessionDuration <= 0 {
		sessionDuration = DefaultSessionDuration
	}
	return &Service{
		sessionReader:   lookup,
		sessions:        make(map[string]models.Session),
		sessionDuration: sessionDuration,
	}
}
