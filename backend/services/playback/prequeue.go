package playback

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"novastream/models"
)

// PrequeueStatus represents the current state of a prequeue request
type PrequeueStatus string

const (
	PrequeueStatusQueued    PrequeueStatus = "queued"
	PrequeueStatusSearching PrequeueStatus = "searching"
	PrequeueStatusResolving PrequeueStatus = "resolving"
	PrequeueStatusProbing   PrequeueStatus = "probing"
	PrequeueStatusReady     PrequeueStatus = "ready"
	PrequeueStatusFailed    PrequeueStatus = "failed"
	PrequeueStatusExpired   PrequeueStatus = "expired"
)

// PrequeueRequest represents an incoming prequeue request
type PrequeueRequest struct {
	TitleID   string `json:"titleId"`
	TitleName string `json:"titleName"` // The actual title name for search queries
	MediaType string `json:"mediaType"` // "movie" or "series"
	UserID    string `json:"userId"`
	ClientID  string `json:"clientId,omitempty"` // Client device ID for per-client filtering
	ImdbID    string `json:"imdbId,omitempty"`
	Year      int    `json:"year,omitempty"`
	// For series: episode info (determined by backend based on watch history)
	SeasonNumber          int     `json:"seasonNumber,omitempty"`
	EpisodeNumber         int     `json:"episodeNumber,omitempty"`
	AbsoluteEpisodeNumber int     `json:"absoluteEpisodeNumber,omitempty"` // For anime: absolute episode number
	StartOffset           float64 `json:"startOffset,omitempty"`           // Resume position in seconds for subtitle extraction
	// Prequeue reason: "details" (user opened details page) or "next_episode" (auto-queue for next episode)
	// Defaults to "details" if not specified
	Reason  string `json:"reason,omitempty"`
	SkipHLS bool   `json:"skipHLS,omitempty"` // Native clients set this to skip HLS session creation
}

// PrequeueResponse is returned when a prequeue request is initiated
type PrequeueResponse struct {
	PrequeueID    string                   `json:"prequeueId"`
	TargetEpisode *models.EpisodeReference `json:"targetEpisode,omitempty"`
	Status        PrequeueStatus           `json:"status"`
}

// AudioTrackInfo represents an audio track with metadata
type AudioTrackInfo struct {
	Index    int    `json:"index"`    // Track index (ffprobe stream index)
	Language string `json:"language"` // Language code (e.g., "eng", "spa")
	Codec    string `json:"codec"`    // Codec name (e.g., "aac", "ac3", "truehd")
	Title    string `json:"title"`    // Track title/name
}

// SubtitleTrackInfo represents a subtitle track with metadata
type SubtitleTrackInfo struct {
	Index         int    `json:"index"`         // Track index (0-based, for selection in UI)
	AbsoluteIndex int    `json:"absoluteIndex"` // Absolute ffprobe stream index (for ffmpeg -map)
	Language      string `json:"language"`      // Language code (e.g., "eng", "spa")
	Title         string `json:"title"`         // Track title/name
	Codec         string `json:"codec"`         // Codec name
	Forced        bool   `json:"forced"`        // Whether this is a forced subtitle track
	IsBitmap      bool   `json:"isBitmap"`      // Whether this is a bitmap subtitle (PGS, VOBSUB)
}

// PrequeueStatusResponse is the full status of a prequeue entry
type PrequeueStatusResponse struct {
	PrequeueID    string                   `json:"prequeueId"`
	Status        PrequeueStatus           `json:"status"`
	UserID        string                   `json:"userId,omitempty"` // The user who created this prequeue
	TargetEpisode *models.EpisodeReference `json:"targetEpisode,omitempty"`

	// When ready:
	StreamPath   string `json:"streamPath,omitempty"`
	DisplayName  string `json:"displayName,omitempty"` // For display instead of extracting from path
	FileSize     int64  `json:"fileSize,omitempty"`
	HealthStatus string `json:"healthStatus,omitempty"`

	// HDR detection results
	HasDolbyVision     bool   `json:"hasDolbyVision,omitempty"`
	HasHDR10           bool   `json:"hasHdr10,omitempty"`
	DolbyVisionProfile string `json:"dolbyVisionProfile,omitempty"`

	// Audio transcoding detection (TrueHD, DTS, etc.)
	NeedsAudioTranscode bool `json:"needsAudioTranscode,omitempty"`

	// For HLS (HDR content or audio transcoding):
	HLSSessionID   string  `json:"hlsSessionId,omitempty"`
	HLSPlaylistURL string  `json:"hlsPlaylistUrl,omitempty"`
	Duration       float64 `json:"duration,omitempty"` // Total duration in seconds (from HLS session probe)

	// Selected tracks (based on user preferences)
	SelectedAudioTrack    int `json:"selectedAudioTrack,omitempty"`    // -1 = default/all
	SelectedSubtitleTrack int `json:"selectedSubtitleTrack,omitempty"` // -1 = none

	// Available tracks (for display in UI)
	AudioTracks    []AudioTrackInfo    `json:"audioTracks,omitempty"`
	SubtitleTracks []SubtitleTrackInfo `json:"subtitleTracks,omitempty"`

	// Pre-extracted subtitle sessions (for direct streaming/VLC path)
	SubtitleSessions map[int]*models.SubtitleSessionInfo `json:"subtitleSessions,omitempty"`

	// AIOStreams passthrough format
	PassthroughName        string `json:"passthroughName,omitempty"`        // Raw display name from AIOStreams
	PassthroughDescription string `json:"passthroughDescription,omitempty"` // Raw description from AIOStreams

	// On failure:
	Error string `json:"error,omitempty"`
}

// PrequeueEntry is the internal state of a prequeue item
type PrequeueEntry struct {
	ID            string
	TitleID       string
	TitleName     string // For display purposes
	Year          int    // For display purposes
	UserID        string
	MediaType     string
	TargetEpisode *models.EpisodeReference
	Reason        string // "details" or "next_episode" - affects HLS startup timeout

	Status       PrequeueStatus
	StreamPath   string
	FileSize     int64
	HealthStatus string

	// HDR detection
	HasDolbyVision     bool
	HasHDR10           bool
	DolbyVisionProfile string

	// Audio transcoding detection (TrueHD, DTS, etc.)
	NeedsAudioTranscode bool

	// HLS session (for HDR or audio transcoding)
	HLSSessionID   string
	HLSPlaylistURL string
	Duration       float64 // Total duration in seconds (from HLS session probe)

	// Selected tracks (based on user preferences)
	SelectedAudioTrack    int // -1 = default/all
	SelectedSubtitleTrack int // -1 = none

	// Pre-extracted subtitle sessions (for direct streaming/VLC path)
	SubtitleSessions map[int]*models.SubtitleSessionInfo

	// Track info for display in UI
	AudioTracks    []AudioTrackInfo
	SubtitleTracks []SubtitleTrackInfo

	// AIOStreams passthrough format
	PassthroughName        string
	PassthroughDescription string

	Error     string
	CreatedAt time.Time
	ExpiresAt time.Time

	// For cancellation
	cancelFunc context.CancelFunc
}

// PrequeueStore manages prequeue entries with TTL
type PrequeueStore struct {
	mu      sync.RWMutex
	entries map[string]*PrequeueEntry
	// Secondary index: titleId+userId -> prequeueId (to find/replace existing prequeue)
	byTitleUser map[string]string
	ttl         time.Duration
}

// NewPrequeueStore creates a new prequeue store with the specified TTL
func NewPrequeueStore(ttl time.Duration) *PrequeueStore {
	store := &PrequeueStore{
		entries:     make(map[string]*PrequeueEntry),
		byTitleUser: make(map[string]string),
		ttl:         ttl,
	}

	// Start cleanup goroutine
	go store.cleanupLoop()

	return store
}

// generateID creates a unique prequeue ID
func generateID() string {
	return fmt.Sprintf("pq_%d", time.Now().UnixNano())
}

// titleUserKey creates a key for the secondary index
func titleUserKey(titleID, userID string) string {
	return fmt.Sprintf("%s:%s", titleID, userID)
}

// Create creates a new prequeue entry and returns its ID
// If an entry already exists for this title+user, it's cancelled and replaced
// reason should be "details" (details page prequeue) or "next_episode" (auto-queue for next episode)
func (s *PrequeueStore) Create(titleID, titleName, userID, mediaType string, year int, targetEpisode *models.EpisodeReference, reason string) (*PrequeueEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := titleUserKey(titleID, userID)

	// Check if there's an existing entry for this title+user
	if existingID, exists := s.byTitleUser[key]; exists {
		if existing, ok := s.entries[existingID]; ok {
			// Cancel the existing prequeue
			if existing.cancelFunc != nil {
				existing.cancelFunc()
			}
			// Remove old entry
			delete(s.entries, existingID)
			log.Printf("[prequeue] Replaced existing prequeue %s for title=%s user=%s", existingID, titleID, userID)
		}
	}

	// Create new entry
	id := generateID()
	// Default reason to "details" if not specified
	if reason == "" {
		reason = "details"
	}
	entry := &PrequeueEntry{
		ID:                    id,
		TitleID:               titleID,
		TitleName:             titleName,
		Year:                  year,
		UserID:                userID,
		MediaType:             mediaType,
		TargetEpisode:         targetEpisode,
		Reason:                reason,
		Status:                PrequeueStatusQueued,
		SelectedAudioTrack:    -1, // Default: use all/default
		SelectedSubtitleTrack: -1, // Default: none
		CreatedAt:             time.Now(),
		ExpiresAt:             time.Now().Add(s.ttl),
	}

	s.entries[id] = entry
	s.byTitleUser[key] = id

	log.Printf("[prequeue] Created prequeue %s for title=%s user=%s mediaType=%s", id, titleID, userID, mediaType)

	return entry, true
}

// Get retrieves a prequeue entry by ID
func (s *PrequeueStore) Get(id string) (*PrequeueEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, exists := s.entries[id]
	if !exists {
		return nil, false
	}

	// Check if expired
	if time.Now().After(entry.ExpiresAt) {
		return nil, false
	}

	return entry, true
}

// GetByTitleUser retrieves a prequeue entry by title+user
func (s *PrequeueStore) GetByTitleUser(titleID, userID string) (*PrequeueEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := titleUserKey(titleID, userID)
	id, exists := s.byTitleUser[key]
	if !exists {
		return nil, false
	}

	entry, exists := s.entries[id]
	if !exists {
		return nil, false
	}

	// Check if expired
	if time.Now().After(entry.ExpiresAt) {
		return nil, false
	}

	return entry, true
}

// Update updates a prequeue entry
func (s *PrequeueStore) Update(id string, updateFn func(*PrequeueEntry)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, exists := s.entries[id]
	if !exists {
		return false
	}

	updateFn(entry)

	// Extend TTL when status becomes ready
	if entry.Status == PrequeueStatusReady {
		entry.ExpiresAt = time.Now().Add(s.ttl)
	}

	return true
}

// SetCancelFunc sets the cancel function for an entry
func (s *PrequeueStore) SetCancelFunc(id string, cancelFunc context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entry, exists := s.entries[id]; exists {
		entry.cancelFunc = cancelFunc
	}
}

// Delete removes a prequeue entry
func (s *PrequeueStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, exists := s.entries[id]
	if !exists {
		return
	}

	// Cancel if still running
	if entry.cancelFunc != nil {
		entry.cancelFunc()
	}

	// Remove from secondary index
	key := titleUserKey(entry.TitleID, entry.UserID)
	if s.byTitleUser[key] == id {
		delete(s.byTitleUser, key)
	}

	delete(s.entries, id)
}

// cleanupLoop periodically removes expired entries
func (s *PrequeueStore) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s.cleanup()
	}
}

// cleanup removes expired entries
func (s *PrequeueStore) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	var toDelete []string

	for id, entry := range s.entries {
		if now.After(entry.ExpiresAt) {
			toDelete = append(toDelete, id)
		}
	}

	for _, id := range toDelete {
		entry := s.entries[id]
		if entry.cancelFunc != nil {
			entry.cancelFunc()
		}

		// Remove from secondary index
		key := titleUserKey(entry.TitleID, entry.UserID)
		if s.byTitleUser[key] == id {
			delete(s.byTitleUser, key)
		}

		delete(s.entries, id)
		log.Printf("[prequeue] Expired and removed prequeue %s", id)
	}
}

// ToResponse converts an entry to a status response
func (e *PrequeueEntry) ToResponse() *PrequeueStatusResponse {
	return &PrequeueStatusResponse{
		PrequeueID:             e.ID,
		Status:                 e.Status,
		UserID:                 e.UserID,
		TargetEpisode:          e.TargetEpisode,
		StreamPath:             e.StreamPath,
		FileSize:               e.FileSize,
		HealthStatus:           e.HealthStatus,
		HasDolbyVision:         e.HasDolbyVision,
		HasHDR10:               e.HasHDR10,
		DolbyVisionProfile:     e.DolbyVisionProfile,
		NeedsAudioTranscode:    e.NeedsAudioTranscode,
		HLSSessionID:           e.HLSSessionID,
		HLSPlaylistURL:         e.HLSPlaylistURL,
		Duration:               e.Duration,
		SelectedAudioTrack:     e.SelectedAudioTrack,
		SelectedSubtitleTrack:  e.SelectedSubtitleTrack,
		AudioTracks:            e.AudioTracks,
		SubtitleTracks:         e.SubtitleTracks,
		SubtitleSessions:       e.SubtitleSessions,
		PassthroughName:        e.PassthroughName,
		PassthroughDescription: e.PassthroughDescription,
		Error:                  e.Error,
	}
}
