package handlers

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

const (
	// shareTokenBytes is the number of random bytes in a share link token.
	shareTokenBytes = 32

	// ShareLinkTTL is how long an unopened share link remains valid.
	ShareLinkTTL = 24 * time.Hour

	// shareCleanupInterval is how often the janitor purges expired/consumed shares.
	shareCleanupInterval = 30 * time.Minute
)

// ShareRecord is a one-time shareable playback link. It captures the playback
// parameters at share time; opening it mints a short-lived stream-scoped session
// and is single-use.
type ShareRecord struct {
	Token     string
	AccountID string
	IsMaster  bool
	Params    map[string]string
	CreatedAt time.Time
	ExpiresAt time.Time
	Consumed  bool
}

// ShareStore is an in-memory store of one-time share links. Pending (unopened)
// links do not survive a backend restart, which is acceptable for a casual
// share-with-family feature.
type ShareStore struct {
	mu     sync.Mutex
	shares map[string]*ShareRecord
}

// NewShareStore creates a ShareStore and starts its background janitor.
func NewShareStore() *ShareStore {
	s := &ShareStore{shares: make(map[string]*ShareRecord)}
	go s.cleanupLoop()
	return s
}

// Create stores a new one-time share link for the captured params and returns it.
func (s *ShareStore) Create(accountID string, isMaster bool, params map[string]string) (*ShareRecord, error) {
	token, err := generateShareToken()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	rec := &ShareRecord{
		Token:     token,
		AccountID: accountID,
		IsMaster:  isMaster,
		Params:    params,
		CreatedAt: now,
		ExpiresAt: now.Add(ShareLinkTTL),
	}

	s.mu.Lock()
	s.shares[token] = rec
	s.mu.Unlock()
	return rec, nil
}

// Consume atomically validates and consumes a share link. It returns the record
// only if it exists, has not expired, and has not already been consumed; the
// record is marked consumed so subsequent calls fail (single-use).
func (s *ShareStore) Consume(token string) (*ShareRecord, bool) {
	if token == "" {
		return nil, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.shares[token]
	if !ok || rec.Consumed || time.Now().After(rec.ExpiresAt) {
		return nil, false
	}
	rec.Consumed = true
	return rec, true
}

// cleanupLoop periodically removes consumed or expired share links.
func (s *ShareStore) cleanupLoop() {
	ticker := time.NewTicker(shareCleanupInterval)
	defer ticker.Stop()
	for range ticker.C {
		s.purge()
	}
}

func (s *ShareStore) purge() {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, rec := range s.shares {
		if rec.Consumed || now.After(rec.ExpiresAt) {
			delete(s.shares, token)
		}
	}
}

func generateShareToken() (string, error) {
	buf := make([]byte, shareTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
