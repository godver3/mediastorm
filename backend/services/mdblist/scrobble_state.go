package mdblist

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"novastream/models"
)

type scrobbleState int

const (
	stateIdle scrobbleState = iota
	stateWatching
	statePaused
)

// scrobbleSession tracks the real-time scrobble state for one media item.
type scrobbleSession struct {
	state         scrobbleState
	lastAPICall   time.Time
	progress      float64
	mediaType     string
	update        models.PlaybackProgressUpdate
	failedWith400 bool // set on 400 to stop retrying a permanently bad request
}

// ScrobbleStateTracker maps progress updates to MDBList scrobble events per user/item.
type ScrobbleStateTracker struct {
	mu              sync.Mutex
	sessions        map[string]*scrobbleSession // key: "userID:mediaType:itemID"
	client          *ScrobbleClient
	scrobbler       *Scrobbler
	refreshInterval time.Duration
	staleTimeout    time.Duration
}

// NewScrobbleStateTracker creates a new tracker.
func NewScrobbleStateTracker(client *ScrobbleClient, scrobbler *Scrobbler, refreshInterval time.Duration) *ScrobbleStateTracker {
	return &ScrobbleStateTracker{
		sessions:        make(map[string]*scrobbleSession),
		client:          client,
		scrobbler:       scrobbler,
		refreshInterval: refreshInterval,
		staleTimeout:    2 * refreshInterval,
	}
}

func mdblistSessionKey(userID, mediaType, itemID string) string {
	return userID + ":" + mediaType + ":" + strings.ToLower(itemID)
}

// HandleProgressUpdate processes a playback progress update and sends the appropriate scrobble event.
func (t *ScrobbleStateTracker) HandleProgressUpdate(userID string, update models.PlaybackProgressUpdate, percentWatched float64) {
	if !t.scrobbler.IsEnabledForUser(userID) {
		return
	}

	key := mdblistSessionKey(userID, update.MediaType, update.ItemID)

	t.mu.Lock()
	sess, exists := t.sessions[key]
	if !exists {
		sess = &scrobbleSession{
			state:     stateIdle,
			mediaType: update.MediaType,
		}
		t.sessions[key] = sess
	}
	sess.progress = percentWatched
	sess.update = update
	t.mu.Unlock()

	// Resolve API key from user's linked account
	account := t.scrobbler.getAccountForUser(userID)
	if account == nil || account.APIKey == "" {
		return
	}
	t.client.UpdateAPIKey(account.APIKey)

	req := BuildScrobbleRequest(update, percentWatched)

	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()

	// Don't retry if a previous attempt got a 400 (bad request is permanent)
	if sess.failedWith400 {
		return
	}

	if update.IsPaused {
		if sess.state == stateWatching {
			if err := t.client.ScrobblePause(req); err != nil {
				log.Printf("[mdblist-scrobble] pause failed for %s: %v", key, err)
				if is400(err) {
					sess.failedWith400 = true
				}
			} else {
				sess.state = statePaused
				sess.lastAPICall = now
			}
		}
		return
	}

	switch sess.state {
	case stateIdle, statePaused:
		if err := t.client.ScrobbleStart(req); err != nil {
			log.Printf("[mdblist-scrobble] start failed for %s: %v", key, err)
			if is400(err) {
				sess.failedWith400 = true
			}
		} else {
			sess.state = stateWatching
			sess.lastAPICall = now
		}
	case stateWatching:
		if now.Sub(sess.lastAPICall) >= t.refreshInterval {
			if err := t.client.ScrobbleStart(req); err != nil {
				log.Printf("[mdblist-scrobble] refresh failed for %s: %v", key, err)
				if is400(err) {
					sess.failedWith400 = true
				}
			} else {
				sess.lastAPICall = now
			}
		}
	}
}

// StopSession sends scrobble/stop and removes the session.
func (t *ScrobbleStateTracker) StopSession(userID string, update models.PlaybackProgressUpdate, percentWatched float64) {
	if !t.scrobbler.IsEnabledForUser(userID) {
		return
	}

	key := mdblistSessionKey(userID, update.MediaType, update.ItemID)

	t.mu.Lock()
	_, exists := t.sessions[key]
	if !exists {
		t.mu.Unlock()
		return
	}
	delete(t.sessions, key)
	t.mu.Unlock()

	// Resolve API key from user's linked account
	account := t.scrobbler.getAccountForUser(userID)
	if account == nil || account.APIKey == "" {
		return
	}
	t.client.UpdateAPIKey(account.APIKey)

	req := BuildScrobbleRequest(update, percentWatched)
	if err := t.client.ScrobbleStop(req); err != nil {
		log.Printf("[mdblist-scrobble] stop failed for %s: %v", key, err)
	}
}

// StartCleanup starts a goroutine that removes stale sessions.
func (t *ScrobbleStateTracker) StartCleanup(ctx context.Context) {
	ticker := time.NewTicker(t.refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.cleanupStaleSessions()
		}
	}
}

func (t *ScrobbleStateTracker) cleanupStaleSessions() {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	for key, sess := range t.sessions {
		if now.Sub(sess.lastAPICall) > t.staleTimeout {
			log.Printf("[mdblist-scrobble] cleaning up stale session: %s", key)
			delete(t.sessions, key)
		}
	}
}

// is400 checks if an error is a 400 bad request from the MDBList API.
func is400(err error) bool {
	var e *ErrScrobble400
	return errors.As(err, &e)
}
