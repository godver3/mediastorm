package simkl

import (
	"context"
	"log"
	"math"
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

type scrobbleSession struct {
	state       scrobbleState
	lastAPICall time.Time
	progress    float64
	update      models.PlaybackProgressUpdate
}

// ScrobbleStateTracker maps playback progress updates to Simkl scrobble events.
type ScrobbleStateTracker struct {
	mu              sync.Mutex
	sessions        map[string]*scrobbleSession
	client          *Client
	scrobbler       *Scrobbler
	refreshInterval time.Duration
	staleTimeout    time.Duration
}

func NewScrobbleStateTracker(client *Client, scrobbler *Scrobbler, refreshInterval time.Duration) *ScrobbleStateTracker {
	return &ScrobbleStateTracker{
		sessions:        make(map[string]*scrobbleSession),
		client:          client,
		scrobbler:       scrobbler,
		refreshInterval: refreshInterval,
		staleTimeout:    2 * refreshInterval,
	}
}

func sessionKey(userID, mediaType, itemID string) string {
	return userID + ":" + mediaType + ":" + strings.ToLower(itemID)
}

func (t *ScrobbleStateTracker) HandleProgressUpdate(userID string, update models.PlaybackProgressUpdate, percentWatched float64) {
	if !t.scrobbler.IsEnabledForUser(userID) {
		return
	}

	key := sessionKey(userID, update.MediaType, update.ItemID)

	t.mu.Lock()
	sess, exists := t.sessions[key]
	if !exists {
		sess = &scrobbleSession{state: stateIdle}
		t.sessions[key] = sess
	}
	sess.progress = percentWatched
	sess.update = update
	t.mu.Unlock()

	account := t.scrobbler.getAccountForUser(userID)
	if account == nil || account.ClientID == "" || account.AccessToken == "" {
		return
	}

	req := BuildScrobbleRequest(update, percentWatched)

	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	if update.IsPaused {
		if sess.state == stateWatching {
			if _, err := t.client.ScrobblePause(account.ClientID, account.AccessToken, req); err != nil {
				log.Printf("[simkl] pause failed for %s: %v", key, err)
			} else {
				sess.state = statePaused
				sess.lastAPICall = now
			}
		}
		return
	}

	switch sess.state {
	case stateIdle, statePaused:
		if _, err := t.client.ScrobbleStart(account.ClientID, account.AccessToken, req); err != nil {
			log.Printf("[simkl] start failed for %s: %v", key, err)
		} else {
			sess.state = stateWatching
			sess.lastAPICall = now
		}
	case stateWatching:
		if now.Sub(sess.lastAPICall) >= t.refreshInterval {
			if _, err := t.client.ScrobbleStart(account.ClientID, account.AccessToken, req); err != nil {
				log.Printf("[simkl] refresh failed for %s: %v", key, err)
			} else {
				sess.lastAPICall = now
			}
		}
	}
}

func (t *ScrobbleStateTracker) StopSession(userID string, update models.PlaybackProgressUpdate, percentWatched float64) {
	if !t.scrobbler.IsEnabledForUser(userID) {
		return
	}

	key := sessionKey(userID, update.MediaType, update.ItemID)

	t.mu.Lock()
	_, exists := t.sessions[key]
	if exists {
		delete(t.sessions, key)
	}
	t.mu.Unlock()

	account := t.scrobbler.getAccountForUser(userID)
	if account == nil || account.ClientID == "" || account.AccessToken == "" {
		return
	}

	req := BuildScrobbleRequest(update, percentWatched)
	if _, err := t.client.ScrobbleStop(account.ClientID, account.AccessToken, req); err != nil {
		log.Printf("[simkl] stop failed for %s: %v", key, err)
		return
	}
	t.scrobbler.noteRecentStop(userID, update)
}

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
			log.Printf("[simkl] cleaning up stale session: %s", key)
			delete(t.sessions, key)
		}
	}
}

func BuildScrobbleRequest(update models.PlaybackProgressUpdate, percentWatched float64) ScrobbleRequest {
	req := ScrobbleRequest{
		Progress: math.Round(percentWatched*100) / 100,
	}
	ids := externalIDsToIDs(update.ExternalIDs)
	if update.MediaType == "movie" {
		req.Movie = &Movie{
			Title: update.MovieName,
			Year:  update.Year,
			IDs:   ids,
		}
	} else if update.MediaType == "episode" {
		req.Show = &Show{
			Title: update.SeriesName,
			IDs:   seriesIDToIDs(update.SeriesID, update.ExternalIDs),
		}
		req.Episode = &Episode{
			Season: update.SeasonNumber,
			Number: update.EpisodeNumber,
		}
	}
	return req
}

func externalIDsToIDs(extIDs map[string]string) IDs {
	ids := IDs{}
	if v, ok := extIDs["tmdb"]; ok {
		ids.TMDB, _ = strconv.Atoi(v)
	}
	if v, ok := extIDs["tvdb"]; ok {
		ids.TVDB, _ = strconv.Atoi(v)
	}
	if v, ok := extIDs["imdb"]; ok {
		ids.IMDB = v
	}
	if v, ok := extIDs["simkl"]; ok {
		ids.Simkl, _ = strconv.Atoi(v)
	}
	return ids
}

func seriesIDToIDs(seriesID string, extIDs map[string]string) IDs {
	ids := externalIDsToIDs(extIDs)
	if ids.TVDB == 0 && ids.TMDB == 0 && ids.IMDB == "" && seriesID != "" {
		parts := strings.Split(seriesID, ":")
		if len(parts) >= 2 {
			provider := strings.ToLower(parts[0])
			numericID := parts[len(parts)-1]
			switch provider {
			case "tvdb":
				ids.TVDB, _ = strconv.Atoi(numericID)
			case "tmdb":
				ids.TMDB, _ = strconv.Atoi(numericID)
			case "imdb":
				ids.IMDB = "tt" + strings.TrimPrefix(numericID, "tt")
			}
		}
	}
	return ids
}
