package trakt

import (
	"context"
	"fmt"
	"log"
	"strconv"
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
	lastTraktCall time.Time
	progress      float64
	mediaType     string
	update        models.PlaybackProgressUpdate // last seen update (carries IDs, season/episode, etc.)
}

// ScrobbleStateTracker maps progress updates to Trakt scrobble events per user/item.
type ScrobbleStateTracker struct {
	mu              sync.Mutex
	sessions        map[string]*scrobbleSession // key: "userID:mediaType:itemID"
	client          *Client
	scrobbler       *Scrobbler
	refreshInterval time.Duration // how often to re-send scrobble/start while watching (default 15min)
	staleTimeout    time.Duration // stop sessions with no update for this long (default 30min)
}

// NewScrobbleStateTracker creates a new tracker.
func NewScrobbleStateTracker(client *Client, scrobbler *Scrobbler, refreshInterval time.Duration) *ScrobbleStateTracker {
	return &ScrobbleStateTracker{
		sessions:        make(map[string]*scrobbleSession),
		client:          client,
		scrobbler:       scrobbler,
		refreshInterval: refreshInterval,
		staleTimeout:    2 * refreshInterval, // 30min for 15min refresh
	}
}

func sessionKey(userID, mediaType, itemID string) string {
	return userID + ":" + mediaType + ":" + strings.ToLower(itemID)
}

// HandleProgressUpdate processes a playback progress update and sends the appropriate scrobble event.
func (t *ScrobbleStateTracker) HandleProgressUpdate(userID string, update models.PlaybackProgressUpdate, percentWatched float64) {
	if !t.scrobbler.IsEnabledForUser(userID) {
		return
	}

	key := sessionKey(userID, update.MediaType, update.ItemID)

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

	accessToken, err := t.scrobbler.getAccessTokenForUser(userID)
	if err != nil || accessToken == "" {
		if err != nil {
			log.Printf("[trakt-scrobble] failed to get token for user %s: %v", userID, err)
		}
		return
	}

	account := t.scrobbler.getAccountForUser(userID)
	if account != nil {
		t.client.UpdateCredentials(account.ClientID, account.ClientSecret)
	}

	req := buildScrobbleRequest(update, percentWatched)

	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()

	if update.IsPaused {
		// Transition to paused
		if sess.state == stateWatching {
			if _, err := t.client.ScrobblePause(accessToken, req); err != nil {
				log.Printf("[trakt-scrobble] pause failed for %s: %v", key, err)
			} else {
				sess.state = statePaused
				sess.lastTraktCall = now
			}
		}
		return
	}

	// Not paused — should be watching
	switch sess.state {
	case stateIdle, statePaused:
		// Start or resume
		if _, err := t.client.ScrobbleStart(accessToken, req); err != nil {
			log.Printf("[trakt-scrobble] start failed for %s: %v", key, err)
		} else {
			sess.state = stateWatching
			sess.lastTraktCall = now
		}
	case stateWatching:
		// Re-send start periodically to keep "now watching" active
		if now.Sub(sess.lastTraktCall) >= t.refreshInterval {
			if _, err := t.client.ScrobbleStart(accessToken, req); err != nil {
				log.Printf("[trakt-scrobble] refresh failed for %s: %v", key, err)
			} else {
				sess.lastTraktCall = now
			}
		}
	}
}

// StopSession sends scrobble/stop and removes the session.
func (t *ScrobbleStateTracker) StopSession(userID string, update models.PlaybackProgressUpdate, percentWatched float64) {
	if !t.scrobbler.IsEnabledForUser(userID) {
		return
	}

	key := sessionKey(userID, update.MediaType, update.ItemID)

	t.mu.Lock()
	sess, exists := t.sessions[key]
	if !exists {
		t.mu.Unlock()
		return
	}
	delete(t.sessions, key)
	_ = sess // session removed
	t.mu.Unlock()

	accessToken, err := t.scrobbler.getAccessTokenForUser(userID)
	if err != nil || accessToken == "" {
		return
	}

	account := t.scrobbler.getAccountForUser(userID)
	if account != nil {
		t.client.UpdateCredentials(account.ClientID, account.ClientSecret)
	}

	req := buildScrobbleRequest(update, percentWatched)
	if _, err := t.client.ScrobbleStop(accessToken, req); err != nil {
		log.Printf("[trakt-scrobble] stop failed for %s: %v", key, err)
	}
}

// StartCleanup starts a goroutine that removes stale sessions (no update for staleTimeout).
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
		if now.Sub(sess.lastTraktCall) > t.staleTimeout {
			log.Printf("[trakt-scrobble] cleaning up stale session: %s", key)
			delete(t.sessions, key)
		}
	}
}

// buildScrobbleRequest converts a PlaybackProgressUpdate to a Trakt ScrobbleRequest.
func buildScrobbleRequest(update models.PlaybackProgressUpdate, percentWatched float64) ScrobbleRequest {
	req := ScrobbleRequest{
		Progress: percentWatched,
	}

	if update.MediaType == "movie" {
		req.Movie = &ScrobbleMovie{
			Title: update.MovieName,
			Year:  update.Year,
			IDs:   externalIDsToSyncIDs(update.ExternalIDs),
		}
	} else if update.MediaType == "episode" {
		req.Episode = &ScrobbleEpisode{
			Season: update.SeasonNumber,
			Number: update.EpisodeNumber,
			Title:  update.EpisodeName,
		}
		req.Show = &ScrobbleShow{
			Title: update.SeriesName,
			IDs:   seriesIDToSyncIDs(update.SeriesID, update.ExternalIDs),
		}
	}

	return req
}

// externalIDsToSyncIDs converts the map[string]string external IDs to SyncIDs.
func externalIDsToSyncIDs(extIDs map[string]string) SyncIDs {
	ids := SyncIDs{}
	if v, ok := extIDs["tmdb"]; ok {
		ids.TMDB, _ = strconv.Atoi(v)
	}
	if v, ok := extIDs["imdb"]; ok {
		ids.IMDB = v
	}
	if v, ok := extIDs["tvdb"]; ok {
		ids.TVDB, _ = strconv.Atoi(v)
	}
	return ids
}

// seriesIDToSyncIDs extracts show IDs from seriesID (e.g. "tvdb:series:12345") and external IDs.
func seriesIDToSyncIDs(seriesID string, extIDs map[string]string) SyncIDs {
	ids := SyncIDs{}

	// Try external IDs first
	if v, ok := extIDs["tvdb"]; ok {
		ids.TVDB, _ = strconv.Atoi(v)
	}
	if v, ok := extIDs["tmdb"]; ok {
		ids.TMDB, _ = strconv.Atoi(v)
	}
	if v, ok := extIDs["imdb"]; ok {
		ids.IMDB = v
	}

	// Fall back to parsing seriesID if no IDs found
	if ids.TVDB == 0 && ids.TMDB == 0 && ids.IMDB == "" && seriesID != "" {
		parts := strings.Split(seriesID, ":")
		if len(parts) >= 3 {
			provider := strings.ToLower(parts[0])
			numericID := parts[len(parts)-1]
			switch provider {
			case "tvdb":
				ids.TVDB, _ = strconv.Atoi(numericID)
			case "tmdb":
				ids.TMDB, _ = strconv.Atoi(numericID)
			case "imdb":
				ids.IMDB = fmt.Sprintf("tt%s", numericID)
			}
		}
	}

	return ids
}
