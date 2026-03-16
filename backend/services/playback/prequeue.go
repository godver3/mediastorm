package playback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"novastream/internal/datastore"
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

// WarmRef is a lightweight reference to a pre-warmed prequeue entry
type WarmRef struct {
	PrequeueID string
}

// PrequeueWorkerFunc is the function signature for the prequeue worker callable by the prewarm service
type PrequeueWorkerFunc func(ctx context.Context, titleID, titleName, imdbID, mediaType string, year int, userID string, targetEpisode *models.EpisodeReference) (string, error)

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
	SelectedAudioTrack    int `json:"selectedAudioTrack"`    // -1 = default/all
	SelectedSubtitleTrack int `json:"selectedSubtitleTrack"` // -1 = none

	// Available tracks (for display in UI)
	AudioTracks    []AudioTrackInfo    `json:"audioTracks,omitempty"`
	SubtitleTracks []SubtitleTrackInfo `json:"subtitleTracks,omitempty"`

	// Pre-extracted subtitle sessions (for direct streaming/VLC path)
	SubtitleSessions map[int]*models.SubtitleSessionInfo `json:"subtitleSessions,omitempty"`

	// AIOStreams passthrough format
	PassthroughName        string `json:"passthroughName,omitempty"`        // Raw display name from AIOStreams
	PassthroughDescription string `json:"passthroughDescription,omitempty"` // Raw description from AIOStreams

	// Parsed metadata attributes from the selected result (for badge display)
	ResultAttributes map[string]string `json:"resultAttributes,omitempty"`

	// On failure:
	Error string `json:"error,omitempty"`
}

// PrequeueEntry is the internal state of a prequeue item
type PrequeueEntry struct {
	ID            string                   `json:"id"`
	TitleID       string                   `json:"titleId"`
	TitleName     string                   `json:"titleName"`
	Year          int                      `json:"year,omitempty"`
	UserID        string                   `json:"userId"`
	MediaType     string                   `json:"mediaType"`
	TargetEpisode *models.EpisodeReference `json:"targetEpisode,omitempty"`
	Reason        string                   `json:"reason"`

	Status       PrequeueStatus `json:"status"`
	StreamPath   string         `json:"streamPath,omitempty"`
	MagnetLink   string         `json:"magnetLink,omitempty"`   // Original magnet link for re-adding expired torrents
	FileSize     int64          `json:"fileSize,omitempty"`
	HealthStatus string         `json:"healthStatus,omitempty"`

	// HDR detection
	HasDolbyVision     bool   `json:"hasDolbyVision,omitempty"`
	HasHDR10           bool   `json:"hasHdr10,omitempty"`
	DolbyVisionProfile string `json:"dolbyVisionProfile,omitempty"`

	// Audio transcoding detection (TrueHD, DTS, etc.)
	NeedsAudioTranscode bool `json:"needsAudioTranscode,omitempty"`

	// HLS session (for HDR or audio transcoding)
	HLSSessionID   string  `json:"hlsSessionId,omitempty"`
	HLSPlaylistURL string  `json:"hlsPlaylistUrl,omitempty"`
	Duration       float64 `json:"duration,omitempty"`

	// Selected tracks (based on user preferences)
	SelectedAudioTrack    int `json:"selectedAudioTrack"`
	SelectedSubtitleTrack int `json:"selectedSubtitleTrack"`

	// Pre-extracted subtitle sessions (for direct streaming/VLC path)
	SubtitleSessions map[int]*models.SubtitleSessionInfo `json:"-"`

	// Track info for display in UI
	AudioTracks    []AudioTrackInfo    `json:"audioTracks,omitempty"`
	SubtitleTracks []SubtitleTrackInfo `json:"subtitleTracks,omitempty"`

	// AIOStreams passthrough format
	PassthroughName        string `json:"passthroughName,omitempty"`
	PassthroughDescription string `json:"passthroughDescription,omitempty"`

	// Parsed metadata attributes from selected result
	ResultAttributes map[string]string `json:"resultAttributes,omitempty"`

	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`

	// For cancellation
	cancelFunc context.CancelFunc `json:"-"`
}

// PrequeueStore manages prequeue entries with TTL
type PrequeueStore struct {
	mu      sync.RWMutex
	entries map[string]*PrequeueEntry
	// Secondary index: titleId+userId -> prequeueId (to find/replace existing prequeue)
	byTitleUser map[string]string
	defaultTTL  time.Duration
	storagePath string // If set, ready entries are persisted to this file
	store       *datastore.DataStore
}

// useDB returns true when the store is backed by PostgreSQL.
func (s *PrequeueStore) useDB() bool { return s.store != nil }

// SetDataStore sets the PostgreSQL datastore for persistence.
// When set, entries are persisted to the database instead of disk.
func (s *PrequeueStore) SetDataStore(store *datastore.DataStore) {
	s.store = store
}

// NewPrequeueStore creates a new prequeue store with the specified default TTL
func NewPrequeueStore(ttl time.Duration) *PrequeueStore {
	store := &PrequeueStore{
		entries:     make(map[string]*PrequeueEntry),
		byTitleUser: make(map[string]string),
		defaultTTL:  ttl,
	}

	// Start cleanup goroutine
	go store.cleanupLoop()

	return store
}

// MagnetInfo contains the magnet link and debrid path info for a restored prequeue entry.
type MagnetInfo struct {
	Provider   string
	TorrentID  string
	MagnetLink string
}

// SetStoragePath enables persistence of ready entries to the given directory.
// Entries are saved to prequeue.json in that directory.
func (s *PrequeueStore) SetStoragePath(dir string) {
	if dir == "" {
		return
	}
	s.storagePath = filepath.Join(dir, "prequeue.json")
	if err := s.loadFromDisk(); err != nil {
		log.Printf("[prequeue] Warning: failed to load persisted entries: %v", err)
	}
}

// RestoredMagnets returns magnet link info from restored prequeue entries.
// Called after SetStoragePath to re-populate the magnet registry on restart.
func (s *PrequeueStore) RestoredMagnets() []MagnetInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var magnets []MagnetInfo
	for _, e := range s.entries {
		if e.MagnetLink == "" || e.StreamPath == "" {
			continue
		}
		provider, torrentID := parseDebridStreamPath(e.StreamPath)
		if provider == "" || torrentID == "" {
			continue
		}
		magnets = append(magnets, MagnetInfo{
			Provider:   provider,
			TorrentID:  torrentID,
			MagnetLink: e.MagnetLink,
		})
	}
	return magnets
}

// parseDebridStreamPath extracts provider and torrentID from a debrid stream path.
// Format: /debrid/{provider}/{torrentID}/...
func parseDebridStreamPath(path string) (provider, torrentID string) {
	trimmed := strings.TrimPrefix(path, "/debrid/")
	if trimmed == path {
		return "", "" // not a debrid path
	}
	parts := strings.SplitN(trimmed, "/", 3)
	if len(parts) < 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

// loadFromDisk restores ready entries from disk (or from DB when useDB)
func (s *PrequeueStore) loadFromDisk() error {
	if s.useDB() {
		return s.loadFromDB()
	}

	file, err := os.Open(s.storagePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()

	var stored []*PrequeueEntry
	if err := json.NewDecoder(file).Decode(&stored); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	restored := 0
	for _, e := range stored {
		if now.After(e.ExpiresAt) || e.Status != PrequeueStatusReady || e.StreamPath == "" {
			continue
		}
		s.entries[e.ID] = e
		key := titleUserKey(e.TitleID, e.UserID)
		s.byTitleUser[key] = e.ID
		restored++
	}
	if restored > 0 {
		log.Printf("[prequeue] Restored %d ready entries from disk", restored)
	}
	return nil
}

// loadFromDB restores ready entries from PostgreSQL.
func (s *PrequeueStore) loadFromDB() error {
	blobs, err := s.store.Prequeue().List(context.Background())
	if err != nil {
		return fmt.Errorf("load prequeue from db: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	restored := 0
	for _, data := range blobs {
		var e PrequeueEntry
		if err := json.Unmarshal(data, &e); err != nil {
			log.Printf("[prequeue] Warning: failed to unmarshal DB entry: %v", err)
			continue
		}
		if now.After(e.ExpiresAt) || e.Status != PrequeueStatusReady || e.StreamPath == "" {
			continue
		}
		s.entries[e.ID] = &e
		key := titleUserKey(e.TitleID, e.UserID)
		s.byTitleUser[key] = e.ID
		restored++
	}
	if restored > 0 {
		log.Printf("[prequeue] Restored %d ready entries from database", restored)
	}
	return nil
}

// saveToDisk persists all ready entries to disk (or DB when useDB). Caller must hold s.mu.
func (s *PrequeueStore) saveToDisk() {
	if s.useDB() {
		s.saveToDB()
		return
	}

	if s.storagePath == "" {
		return
	}

	now := time.Now()
	var items []*PrequeueEntry
	for _, e := range s.entries {
		if e.Status == PrequeueStatusReady && e.StreamPath != "" && now.Before(e.ExpiresAt) {
			items = append(items, e)
		}
	}

	tmp := s.storagePath + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		log.Printf("[prequeue] Warning: failed to create temp file for persistence: %v", err)
		return
	}

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(items); err != nil {
		file.Close()
		os.Remove(tmp)
		log.Printf("[prequeue] Warning: failed to encode prequeue entries: %v", err)
		return
	}
	file.Close()
	if err := os.Rename(tmp, s.storagePath); err != nil {
		log.Printf("[prequeue] Warning: failed to persist prequeue entries: %v", err)
	}
}

// saveToDB persists all ready entries to PostgreSQL. Caller must hold s.mu.
func (s *PrequeueStore) saveToDB() {
	ctx := context.Background()
	now := time.Now()

	for _, e := range s.entries {
		if e.Status != PrequeueStatusReady || e.StreamPath == "" || now.After(e.ExpiresAt) {
			continue
		}
		data, err := json.Marshal(e)
		if err != nil {
			log.Printf("[prequeue] Warning: failed to marshal entry %s for DB: %v", e.ID, err)
			continue
		}
		if err := s.store.Prequeue().Upsert(ctx, e.ID, e.TitleID, e.UserID, string(e.Status), data, e.ExpiresAt); err != nil {
			log.Printf("[prequeue] Warning: failed to upsert entry %s to DB: %v", e.ID, err)
		}
	}
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
		CreatedAt: time.Now(),
	}
	// Use dynamic TTL based on air date; fall back to store default
	dynTTL := entry.DynamicTTL()
	if dynTTL <= 0 {
		dynTTL = s.defaultTTL
	}
	entry.ExpiresAt = time.Now().Add(dynTTL)

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

	// Extend TTL when status becomes ready (only if not already set further out)
	if entry.Status == PrequeueStatusReady {
		dynTTL := entry.DynamicTTL()
		if dynTTL <= 0 {
			dynTTL = s.defaultTTL
		}
		defaultExpiry := time.Now().Add(dynTTL)
		if entry.ExpiresAt.Before(defaultExpiry) {
			entry.ExpiresAt = defaultExpiry
		}
		s.saveToDisk()
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

// DeleteAll removes all prequeue entries
func (s *PrequeueStore) DeleteAll() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, entry := range s.entries {
		if entry.cancelFunc != nil {
			entry.cancelFunc()
		}
		delete(s.entries, id)
	}
	s.byTitleUser = make(map[string]string)
	s.saveToDisk()
	log.Printf("[prequeue] Cleared all prequeue entries")
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
	if len(toDelete) > 0 {
		s.saveToDisk()
	}
}

// ListAll returns all non-expired prequeue entries
func (s *PrequeueStore) ListAll() []*PrequeueEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	var result []*PrequeueEntry
	for _, entry := range s.entries {
		if now.Before(entry.ExpiresAt) {
			result = append(result, entry)
		}
	}
	return result
}

// ForceExpiry sets the ExpiresAt time for an entry, bypassing TTL auto-extension.
// Intended for testing.
func (s *PrequeueStore) ForceExpiry(id string, expiresAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, exists := s.entries[id]; exists {
		entry.ExpiresAt = expiresAt
	}
}

// ListExpired returns all entries whose TTL has elapsed (ready entries only).
// Used by prewarm to know which entries need re-resolving.
func (s *PrequeueStore) ListExpired() []*PrequeueEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	var result []*PrequeueEntry
	for _, entry := range s.entries {
		if entry.Status == PrequeueStatusReady && now.After(entry.ExpiresAt) {
			result = append(result, entry)
		}
	}
	return result
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
		ResultAttributes:       e.ResultAttributes,
		Error:                  e.Error,
	}
}
