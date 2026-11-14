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
	"sync/atomic"
	"time"

	"novastream/internal/datastore"
	"novastream/internal/requestsecurity"
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

const readyStreamValidationTTL = 5 * time.Minute

const persistedPrequeueOperationTimeout = 30 * time.Second

const ManualPrequeueReason = "manual"

var manualPrequeueExpiry = time.Date(9999, time.December, 31, 23, 59, 59, 0, time.UTC)

// WarmRef is a lightweight reference to a pre-warmed prequeue entry
type WarmRef struct {
	PrequeueID string
}

// PrequeueWorkerFunc is the function signature for the prequeue worker callable by the prewarm service
type PrequeueWorkerFunc func(ctx context.Context, titleID, titleName, imdbID, mediaType string, year int, userID string, targetEpisode *models.EpisodeReference) (string, error)

// ScopedPrequeueWorkerFunc is used by scheduled prewarm when a profile/client
// requires a distinct effective settings scope.
type ScopedPrequeueWorkerFunc func(ctx context.Context, titleID, titleName, imdbID, mediaType string, year int, userID, clientID, settingsScopeKey string, targetEpisode *models.EpisodeReference) (string, error)

// PrequeueRequest represents an incoming prequeue request
type PrequeueRequest struct {
	Numbering *models.EpisodeNumbering `json:"numbering,omitempty"`
	TitleID   string                   `json:"titleId"`
	TitleName string                   `json:"titleName"` // The actual title name for search queries
	MediaType string                   `json:"mediaType"` // "movie" or "series"
	UserID    string                   `json:"userId"`
	ClientID  string                   `json:"clientId,omitempty"` // Client device ID for per-client filtering
	ImdbID    string                   `json:"imdbId,omitempty"`
	TmdbID    string                   `json:"tmdbId,omitempty"` // External IDs used to canonicalize TitleID for prequeue reuse
	TvdbID    string                   `json:"tvdbId,omitempty"`
	Year      int                      `json:"year,omitempty"`
	// For series: episode info (determined by backend based on watch history)
	SeasonNumber          int     `json:"seasonNumber,omitempty"`
	EpisodeNumber         int     `json:"episodeNumber,omitempty"`
	AbsoluteEpisodeNumber int     `json:"absoluteEpisodeNumber,omitempty"` // Release absolute number; excludes season-zero specials.
	StartOffset           float64 `json:"startOffset,omitempty"`           // Resume position in seconds for subtitle extraction
	// Prequeue reason: "details", "next_episode", or "manual" (kept until explicitly removed)
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
	Index    int    `json:"index"`             // Track index (ffprobe stream index)
	Language string `json:"language"`          // Language code (e.g., "eng", "spa")
	Codec    string `json:"codec"`             // Codec name (e.g., "aac", "ac3", "truehd")
	Profile  string `json:"profile,omitempty"` // FFmpeg/ffprobe codec profile (for example E-AC-3+Atmos)
	Title    string `json:"title"`             // Track title/name
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
	PrequeueID         string                   `json:"prequeueId"`
	Status             PrequeueStatus           `json:"status"`
	UserID             string                   `json:"userId,omitempty"` // The user who created this prequeue
	TargetEpisode      *models.EpisodeReference `json:"targetEpisode,omitempty"`
	ProgressStage      string                   `json:"progressStage,omitempty"`
	ProgressDetail     string                   `json:"progressDetail,omitempty"`
	ProgressCurrent    int                      `json:"progressCurrent,omitempty"`
	ProgressCurrentMin int                      `json:"progressCurrentMin,omitempty"` // Lowest candidate (1-based) in flight during concurrent resolution
	ProgressCurrentMax int                      `json:"progressCurrentMax,omitempty"` // Highest candidate (1-based) in flight during concurrent resolution
	ProgressTotal      int                      `json:"progressTotal,omitempty"`

	// When ready:
	StreamPath     string `json:"streamPath,omitempty"`
	DisplayName    string `json:"displayName,omitempty"` // For display instead of extracting from path
	ServiceType    string `json:"serviceType,omitempty"`
	DebridProvider string `json:"debridProvider,omitempty"`
	FileSize       int64  `json:"fileSize,omitempty"`
	HealthStatus   string `json:"healthStatus,omitempty"`

	// HDR detection results
	HasDolbyVision           bool                             `json:"hasDolbyVision,omitempty"`
	HasHDR10                 bool                             `json:"hasHdr10,omitempty"`
	DolbyVisionProfile       string                           `json:"dolbyVisionProfile,omitempty"`
	DolbyVisionConfiguration *models.DolbyVisionConfiguration `json:"dolbyVisionConfiguration,omitempty"`

	// Audio transcoding detection (TrueHD, DTS, etc.)
	NeedsAudioTranscode bool `json:"needsAudioTranscode,omitempty"`

	// For HLS (HDR content or audio transcoding):
	HLSSessionID   string  `json:"hlsSessionId,omitempty"`
	HLSPlaylistURL string  `json:"hlsPlaylistUrl,omitempty"`
	Duration       float64 `json:"duration,omitempty"`  // Total duration in seconds (from HLS session probe)
	FrameRate      string  `json:"frameRate,omitempty"` // Primary video avg_frame_rate from ffprobe

	// Selected tracks (based on user preferences). These are container
	// (ffprobe) stream numbers, NOT positions in AudioTracks/SubtitleTracks or
	// in a player's track list; they match AudioTrackInfo.Index /
	// SubtitleTrackInfo.Index and the HLS audioTrack/subtitleTrack params.
	// Kept for older clients; prefer TrackSelection.
	SelectedAudioTrack    int `json:"selectedAudioTrack"`    // container stream index, -1 = player default
	SelectedSubtitleTrack int `json:"selectedSubtitleTrack"` // container stream index, -1 = none

	// TrackSelection describes the chosen audio/subtitle renditions explicitly
	// (stream index, type position, language + same-language position, codec,
	// title, forced) so clients can map them onto their player's track list.
	TrackSelection *models.TrackSelection `json:"trackSelection,omitempty"`

	// Available tracks (for display in UI)
	AudioTracks    []AudioTrackInfo    `json:"audioTracks,omitempty"`
	SubtitleTracks []SubtitleTrackInfo `json:"subtitleTracks,omitempty"`

	// Pre-extracted subtitle sessions (for direct streaming/VLC path)
	SubtitleSessions map[int]*models.SubtitleSessionInfo `json:"subtitleSessions,omitempty"`

	// AIOStreams passthrough format
	PassthroughName        string `json:"passthroughName,omitempty"`        // Raw display name from AIOStreams
	PassthroughDescription string `json:"passthroughDescription,omitempty"` // Raw description from AIOStreams

	// Parsed metadata attributes from the selected result (for badge display)
	ResultAttributes    map[string]string  `json:"resultAttributes,omitempty"`
	SelectedResult      *models.NZBResult  `json:"selectedResult,omitempty"`
	SelectedResultIndex int                `json:"selectedResultIndex,omitempty"`
	MigrationCandidates []models.NZBResult `json:"migrationCandidates,omitempty"`

	// On failure:
	Error string `json:"error,omitempty"`
}

// PrequeueEntry is the internal state of a prequeue item
type PrequeueEntry struct {
	ID               string                   `json:"id"`
	TitleID          string                   `json:"titleId"`
	TitleName        string                   `json:"titleName"`
	Year             int                      `json:"year,omitempty"`
	UserID           string                   `json:"userId"`
	SettingsScopeKey string                   `json:"settingsScopeKey,omitempty"`
	MediaType        string                   `json:"mediaType"`
	TargetEpisode    *models.EpisodeReference `json:"targetEpisode,omitempty"`
	Reason           string                   `json:"reason"`
	Persistent       bool                     `json:"persistent,omitempty"`

	Status             PrequeueStatus `json:"status"`
	ProgressStage      string         `json:"progressStage,omitempty"`
	ProgressDetail     string         `json:"progressDetail,omitempty"`
	ProgressCurrent    int            `json:"progressCurrent,omitempty"`
	ProgressCurrentMin int            `json:"progressCurrentMin,omitempty"` // Lowest candidate (1-based) in flight during concurrent resolution
	ProgressCurrentMax int            `json:"progressCurrentMax,omitempty"` // Highest candidate (1-based) in flight during concurrent resolution
	ProgressTotal      int            `json:"progressTotal,omitempty"`
	StreamPath         string         `json:"streamPath,omitempty"`
	MagnetLink         string         `json:"magnetLink,omitempty"` // Original magnet link for re-adding expired torrents
	ServiceType        string         `json:"serviceType,omitempty"`
	DebridProvider     string         `json:"debridProvider,omitempty"`
	FileSize           int64          `json:"fileSize,omitempty"`
	HealthStatus       string         `json:"healthStatus,omitempty"`

	// HDR detection
	HasDolbyVision           bool                             `json:"hasDolbyVision,omitempty"`
	HasHDR10                 bool                             `json:"hasHdr10,omitempty"`
	DolbyVisionProfile       string                           `json:"dolbyVisionProfile,omitempty"`
	DolbyVisionConfiguration *models.DolbyVisionConfiguration `json:"dolbyVisionConfiguration,omitempty"`

	// Audio transcoding detection (TrueHD, DTS, etc.)
	NeedsAudioTranscode bool `json:"needsAudioTranscode,omitempty"`

	// HLS session (for HDR or audio transcoding)
	HLSSessionID   string  `json:"hlsSessionId,omitempty"`
	HLSPlaylistURL string  `json:"hlsPlaylistUrl,omitempty"`
	Duration       float64 `json:"duration,omitempty"`
	FrameRate      string  `json:"frameRate,omitempty"`

	// Selected tracks (based on user preferences); container stream indexes.
	SelectedAudioTrack    int                    `json:"selectedAudioTrack"`
	SelectedSubtitleTrack int                    `json:"selectedSubtitleTrack"`
	TrackSelection        *models.TrackSelection `json:"trackSelection,omitempty"`

	// Pre-extracted subtitle sessions (for direct streaming/VLC path)
	SubtitleSessions map[int]*models.SubtitleSessionInfo `json:"-"`

	// Track info for display in UI
	AudioTracks    []AudioTrackInfo    `json:"audioTracks,omitempty"`
	SubtitleTracks []SubtitleTrackInfo `json:"subtitleTracks,omitempty"`

	// AIOStreams passthrough format
	PassthroughName        string `json:"passthroughName,omitempty"`
	PassthroughDescription string `json:"passthroughDescription,omitempty"`

	// Parsed metadata attributes from selected result
	ResultAttributes    map[string]string  `json:"resultAttributes,omitempty"`
	SelectedResult      *models.NZBResult  `json:"selectedResult,omitempty"`
	SelectedResultIndex int                `json:"selectedResultIndex,omitempty"`
	MigrationCandidates []models.NZBResult `json:"migrationCandidates,omitempty"`
	MigrationAdopted    bool               `json:"migrationAdopted,omitempty"`

	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`

	// For cancellation
	cancelFunc context.CancelFunc `json:"-"`
}

// StreamPathValidator checks whether a ready prequeue stream path is still usable.
type StreamPathValidator func(ctx context.Context, streamPath string) error

type readyValidationSnapshot struct {
	ID                string
	TitleID           string
	UserID            string
	Status            PrequeueStatus
	StreamPath        string
	RecentlyValidated bool
}

// copyPrequeueEntryForRead captures one coherent entry version while the store
// lock is held. Store updates replace fields under the write lock, so callers
// must not retain the live pointer after releasing the lock.
func copyPrequeueEntryForRead(entry *PrequeueEntry) *PrequeueEntry {
	if entry == nil {
		return nil
	}

	copy := *entry
	if entry.TargetEpisode != nil {
		targetEpisode := *entry.TargetEpisode
		if entry.TargetEpisode.Image != nil {
			image := *entry.TargetEpisode.Image
			targetEpisode.Image = &image
		}
		copy.TargetEpisode = &targetEpisode
	}
	if entry.DolbyVisionConfiguration != nil {
		configuration := *entry.DolbyVisionConfiguration
		copy.DolbyVisionConfiguration = &configuration
	}
	copy.AudioTracks = append([]AudioTrackInfo(nil), entry.AudioTracks...)
	copy.SubtitleTracks = append([]SubtitleTrackInfo(nil), entry.SubtitleTracks...)
	if entry.SubtitleSessions != nil {
		copy.SubtitleSessions = make(map[int]*models.SubtitleSessionInfo, len(entry.SubtitleSessions))
		for trackIndex, session := range entry.SubtitleSessions {
			if session == nil {
				copy.SubtitleSessions[trackIndex] = nil
				continue
			}
			sessionCopy := *session
			copy.SubtitleSessions[trackIndex] = &sessionCopy
		}
	}
	if entry.ResultAttributes != nil {
		copy.ResultAttributes = make(map[string]string, len(entry.ResultAttributes))
		for key, value := range entry.ResultAttributes {
			copy.ResultAttributes[key] = value
		}
	}
	if entry.SelectedResult != nil {
		selectedResult := copyNZBResult(*entry.SelectedResult)
		copy.SelectedResult = &selectedResult
	}
	if entry.MigrationCandidates != nil {
		copy.MigrationCandidates = make([]models.NZBResult, len(entry.MigrationCandidates))
		for index, candidate := range entry.MigrationCandidates {
			copy.MigrationCandidates[index] = copyNZBResult(candidate)
		}
	}
	return &copy
}

func copyNZBResult(result models.NZBResult) models.NZBResult {
	result.Categories = append([]string(nil), result.Categories...)
	if result.Attributes != nil {
		attributes := make(map[string]string, len(result.Attributes))
		for key, value := range result.Attributes {
			attributes[key] = value
		}
		result.Attributes = attributes
	}
	return result
}

// PrequeueStore manages prequeue entries with TTL
type PrequeueStore struct {
	mu      sync.RWMutex
	entries map[string]*PrequeueEntry
	// Secondary index: titleId+userId+settingsScopeKey -> prequeueId (to find/replace existing prequeue)
	byTitleUser map[string]string
	defaultTTL  time.Duration
	storagePath string // If set, ready entries are persisted to this file
	repo        datastore.PrequeueRepository

	streamPathValidator StreamPathValidator
	streamPathValidated map[string]time.Time

	// generation counts DeleteAll and DeleteByUser calls. Everything that
	// changes how releases rank (globally or for one profile) clears the
	// store, so callers that keep resolved streams of their own (the
	// Jellyfin bridge) compare it to notice.
	generation atomic.Uint64
}

// DeletedPrequeueEntry describes an entry removed from the prequeue store.
type DeletedPrequeueEntry struct {
	ID         string
	TitleID    string
	TitleName  string
	UserID     string
	StreamPath string
}

// useDB returns true when the store is backed by PostgreSQL.
func (s *PrequeueStore) useDB() bool { return s.repo != nil }

// SetDataStore sets the PostgreSQL datastore for persistence.
// When set, entries are persisted to the database instead of disk.
func (s *PrequeueStore) SetDataStore(store *datastore.DataStore) {
	if store == nil {
		s.repo = nil
		return
	}
	s.repo = store.Prequeue()
}

// SetStreamPathValidator enables lazy validation for restored ready entries.
func (s *PrequeueStore) SetStreamPathValidator(validator StreamPathValidator) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streamPathValidator = validator
}

// NewPrequeueStore creates a new prequeue store with the specified default TTL
func NewPrequeueStore(ttl time.Duration) *PrequeueStore {
	store := &PrequeueStore{
		entries:             make(map[string]*PrequeueEntry),
		byTitleUser:         make(map[string]string),
		defaultTTL:          ttl,
		streamPathValidated: make(map[string]time.Time),
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

// SetStoragePath configures persistence of ready entries in the given directory.
// RestorePersisted performs the potentially expensive initial load separately so
// callers can start serving HTTP before persisted entries are decoded.
func (s *PrequeueStore) SetStoragePath(dir string) {
	if dir == "" {
		return
	}
	s.storagePath = filepath.Join(dir, "prequeue.json")
}

// RestorePersisted loads ready entries from PostgreSQL or disk. Database work is
// bounded so a slow or blocked persistence layer cannot hang background startup.
func (s *PrequeueStore) RestorePersisted(ctx context.Context) error {
	if s.storagePath == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s.useDB() {
		return s.loadFromDB(ctx)
	}
	return s.loadFromDisk(ctx)
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
func (s *PrequeueStore) loadFromDisk(ctx context.Context) error {
	if s.useDB() {
		return s.loadFromDB(ctx)
	}

	started := time.Now()
	log.Printf("[prequeue] Persisted restore started source=disk")

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
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.Reason == ManualPrequeueReason {
			e.Persistent = true
			e.ExpiresAt = manualPrequeueExpiry
		}
		if now.After(e.ExpiresAt) || e.Status != PrequeueStatusReady || e.StreamPath == "" {
			continue
		}
		s.entries[e.ID] = e
		key := titleUserKey(e.TitleID, e.UserID, e.SettingsScopeKey)
		s.byTitleUser[key] = e.ID
		restored++
	}
	if restored > 0 {
		log.Printf("[prequeue] Restored %d ready entries from disk", restored)
	}
	log.Printf("[prequeue] Persisted restore completed source=disk rows=%d duration=%s", restored, time.Since(started))
	return nil
}

// loadFromDB restores ready entries from PostgreSQL.
func (s *PrequeueStore) loadFromDB(parent context.Context) error {
	started := time.Now()
	log.Printf("[prequeue] Persisted restore started source=database timeout=%s", persistedPrequeueOperationTimeout)

	queryStarted := time.Now()
	ctx, cancel := context.WithTimeout(parent, persistedPrequeueOperationTimeout)
	blobs, err := s.repo.List(ctx)
	cancel()
	if err != nil {
		log.Printf("[prequeue] Persisted restore query failed duration=%s: %v", time.Since(queryStarted), err)
		return fmt.Errorf("load prequeue from db: %w", err)
	}
	var payloadBytes int
	for _, data := range blobs {
		payloadBytes += len(data)
	}
	log.Printf("[prequeue] Persisted restore query completed rows=%d payload_bytes=%d duration=%s", len(blobs), payloadBytes, time.Since(queryStarted))

	decodeStarted := time.Now()
	s.mu.Lock()

	now := time.Now()
	restored := 0
	// Track entries loaded in this pass separately from entries created after
	// the listener opened.
	restoredIDs := make(map[string]bool)
	var supersededIDs []string

	for _, data := range blobs {
		if err := parent.Err(); err != nil {
			s.mu.Unlock()
			return err
		}
		var e PrequeueEntry
		if err := json.Unmarshal(data, &e); err != nil {
			log.Printf("[prequeue] Warning: failed to unmarshal DB entry: %v", err)
			continue
		}
		if e.Reason == ManualPrequeueReason {
			e.Persistent = true
			e.ExpiresAt = manualPrequeueExpiry
		}
		if now.After(e.ExpiresAt) || e.Status != PrequeueStatusReady || e.StreamPath == "" {
			continue
		}

		key := titleUserKey(e.TitleID, e.UserID, e.SettingsScopeKey)
		// A request may have created a newer in-memory entry after the HTTP
		// listener opened but before this background restore completed.
		if currentID, exists := s.byTitleUser[key]; exists && currentID != e.ID {
			if current, ok := s.entries[currentID]; ok && !current.CreatedAt.Before(e.CreatedAt) {
				supersededIDs = append(supersededIDs, e.ID)
				continue
			}
		}
		// If we already have an entry for this title+user, the new one (later in created_at order) supersedes it
		previousWasRestored := false
		if prevID, exists := s.byTitleUser[key]; exists {
			previousWasRestored = restoredIDs[prevID]
			delete(s.entries, prevID)
			delete(restoredIDs, prevID)
			supersededIDs = append(supersededIDs, prevID)
		}

		s.entries[e.ID] = &e
		s.byTitleUser[key] = e.ID
		restoredIDs[e.ID] = true
		if !previousWasRestored {
			restored++
		}
	}
	s.mu.Unlock()
	log.Printf("[prequeue] Persisted restore decode completed restored=%d superseded=%d duration=%s", restored, len(supersededIDs), time.Since(decodeStarted))

	// Delete superseded duplicates from DB
	if len(supersededIDs) > 0 {
		for _, id := range supersededIDs {
			deleteCtx, deleteCancel := context.WithTimeout(parent, persistedPrequeueOperationTimeout)
			err := s.repo.Delete(deleteCtx, id)
			deleteCancel()
			if err != nil {
				log.Printf("[prequeue] Warning: failed to delete superseded DB entry %s: %v", id, err)
			}
		}
		log.Printf("[prequeue] Removed %d superseded duplicate entries from database", len(supersededIDs))
	}

	if restored > 0 {
		log.Printf("[prequeue] Restored %d ready entries from database", restored)
	}
	log.Printf("[prequeue] Persisted restore completed source=database rows=%d payload_bytes=%d duration=%s", restored, payloadBytes, time.Since(started))
	return nil
}

// CleanupExpiredPersisted removes expired database rows independently of the
// restore path. It is safe to run after the listener is accepting requests.
func (s *PrequeueStore) CleanupExpiredPersisted(parent context.Context) {
	if !s.useDB() {
		return
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, persistedPrequeueOperationTimeout)
	n, err := s.repo.DeleteExpired(ctx)
	cancel()
	if err != nil {
		log.Printf("[prequeue] Warning: expired-row cleanup failed after %s: %v", time.Since(started), err)
		return
	}
	log.Printf("[prequeue] Expired-row cleanup completed rows=%d duration=%s", n, time.Since(started))
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
	now := time.Now()

	for _, e := range s.entries {
		if e.Status != PrequeueStatusReady || e.StreamPath == "" || now.After(e.ExpiresAt) {
			continue
		}
		s.saveEntryToDB(e)
	}
}

// saveReadyEntry persists the entry that just became ready. PostgreSQL stores
// entries independently, so rewriting the full in-memory collection here only
// adds serialization, network, and JSONB write amplification. File-backed
// persistence still writes a complete snapshot.
func (s *PrequeueStore) saveReadyEntry(entry *PrequeueEntry) {
	if s.useDB() {
		s.saveEntryToDB(entry)
		return
	}
	s.saveToDisk()
}

func (s *PrequeueStore) saveEntryToDB(entry *PrequeueEntry) {
	if entry == nil || !s.useDB() {
		return
	}
	data, err := json.Marshal(entry)
	if err != nil {
		log.Printf("[prequeue] Warning: failed to marshal entry %s for DB: %v", entry.ID, err)
		return
	}
	if err := s.repo.Upsert(
		context.Background(),
		entry.ID,
		entry.TitleID,
		entry.UserID,
		string(entry.Status),
		data,
		entry.ExpiresAt,
	); err != nil {
		log.Printf("[prequeue] Warning: failed to upsert entry %s to DB: %v", entry.ID, err)
	}
}

// prequeueIDSeq keeps IDs unique when two entries are created within one
// clock tick (Windows timers are coarse enough for that to happen).
var prequeueIDSeq atomic.Uint64

// generateID creates a unique prequeue ID.
func generateID() string {
	return fmt.Sprintf("pq_%d_%d", time.Now().UnixNano(), prequeueIDSeq.Add(1))
}

// titleUserKey creates a key for the secondary index
const DefaultPrequeueSettingsScopeKey = "global"

func normalizeSettingsScopeKey(scopeKey string) string {
	scopeKey = strings.TrimSpace(scopeKey)
	if scopeKey == "" {
		return DefaultPrequeueSettingsScopeKey
	}
	return scopeKey
}

// titleUserKey creates a key for the secondary index.
func titleUserKey(titleID, userID, settingsScopeKey string) string {
	return fmt.Sprintf("%s:%s:%s", titleID, userID, normalizeSettingsScopeKey(settingsScopeKey))
}

// Create creates a new prequeue entry and returns its ID
// If an entry already exists for this title+user, it's cancelled and replaced
// reason should be "details" (details page prequeue) or "next_episode" (auto-queue for next episode)
func (s *PrequeueStore) Create(titleID, titleName, userID, mediaType string, year int, targetEpisode *models.EpisodeReference, reason string) (*PrequeueEntry, bool) {
	return s.CreateScoped(titleID, titleName, userID, mediaType, year, targetEpisode, reason, DefaultPrequeueSettingsScopeKey)
}

// ErrPrequeueInProgress is returned by CreateScopedUnlessInProgress when an
// entry for the same title, user and settings scope is still being resolved.
var ErrPrequeueInProgress = errors.New("prequeue already in progress")

// IsPrequeueInProgress reports whether status is a non-terminal resolve state.
func IsPrequeueInProgress(status PrequeueStatus) bool {
	switch status {
	case PrequeueStatusQueued, PrequeueStatusSearching, PrequeueStatusResolving, PrequeueStatusProbing:
		return true
	default:
		return false
	}
}

// CreateScoped creates a prequeue entry for a specific effective settings scope.
func (s *PrequeueStore) CreateScoped(titleID, titleName, userID, mediaType string, year int, targetEpisode *models.EpisodeReference, reason, settingsScopeKey string) (*PrequeueEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createScopedLocked(titleID, titleName, userID, mediaType, year, targetEpisode, reason, settingsScopeKey)
}

// CreateScopedUnlessInProgress is CreateScoped for background callers
// (prewarm): instead of cancelling and replacing an entry for the same key that
// is still resolving — typically a details-page prequeue the client is polling
// — it leaves that entry alone and returns it with ErrPrequeueInProgress.
func (s *PrequeueStore) CreateScopedUnlessInProgress(titleID, titleName, userID, mediaType string, year int, targetEpisode *models.EpisodeReference, reason, settingsScopeKey string) (*PrequeueEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := titleUserKey(titleID, userID, normalizeSettingsScopeKey(settingsScopeKey))
	if existingID, ok := s.byTitleUser[key]; ok {
		if existing, ok := s.entries[existingID]; ok &&
			IsPrequeueInProgress(existing.Status) && time.Now().Before(existing.ExpiresAt) {
			return copyPrequeueEntryForRead(existing), ErrPrequeueInProgress
		}
	}
	entry, _ := s.createScopedLocked(titleID, titleName, userID, mediaType, year, targetEpisode, reason, settingsScopeKey)
	return entry, nil
}

func (s *PrequeueStore) createScopedLocked(titleID, titleName, userID, mediaType string, year int, targetEpisode *models.EpisodeReference, reason, settingsScopeKey string) (*PrequeueEntry, bool) {
	settingsScopeKey = normalizeSettingsScopeKey(settingsScopeKey)
	key := titleUserKey(titleID, userID, settingsScopeKey)
	preservePersistent := false

	// Check if there's an existing entry for this title+user
	if existingID, exists := s.byTitleUser[key]; exists {
		if existing, ok := s.entries[existingID]; ok {
			preservePersistent = existing.Persistent || existing.Reason == ManualPrequeueReason
			// Cancel the existing prequeue
			if existing.cancelFunc != nil {
				existing.cancelFunc()
			}
			// Remove old entry from memory and DB
			delete(s.entries, existingID)
			if s.useDB() {
				if err := s.repo.Delete(context.Background(), existingID); err != nil {
					log.Printf("[prequeue] Warning: failed to delete old DB entry %s: %v", existingID, err)
				}
			}
			log.Printf("[prequeue] Replaced existing prequeue %s for title=%s user=%s scope=%s", existingID, titleID, userID, settingsScopeKey)
		}
	}

	// Create new entry
	id := generateID()
	// Default reason to "details" if not specified
	if reason == "" {
		reason = "details"
	}
	if preservePersistent {
		reason = ManualPrequeueReason
	}
	entry := &PrequeueEntry{
		ID:                    id,
		TitleID:               titleID,
		TitleName:             titleName,
		Year:                  year,
		UserID:                userID,
		SettingsScopeKey:      settingsScopeKey,
		MediaType:             mediaType,
		TargetEpisode:         targetEpisode,
		Reason:                reason,
		Persistent:            reason == ManualPrequeueReason,
		Status:                PrequeueStatusQueued,
		ProgressStage:         "queued",
		SelectedAudioTrack:    -1, // Default: use all/default
		SelectedSubtitleTrack: -1, // Default: none
		CreatedAt:             time.Now(),
	}
	// Use dynamic TTL based on air date; fall back to store default
	dynTTL := entry.DynamicTTL()
	if dynTTL <= 0 {
		dynTTL = s.defaultTTL
	}
	entry.ExpiresAt = time.Now().Add(dynTTL)
	if entry.Persistent {
		entry.ExpiresAt = manualPrequeueExpiry
	}

	s.entries[id] = entry
	s.byTitleUser[key] = id

	log.Printf("[prequeue] Created prequeue %s for title=%s user=%s scope=%s mediaType=%s", id, titleID, userID, settingsScopeKey, mediaType)

	return entry, true
}

// MakePersistent pins an existing entry until it is explicitly deleted. This
// lets a manual request adopt work already started by the details prequeue.
func (s *PrequeueStore) MakePersistent(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[id]
	if !ok {
		return false
	}
	entry.Persistent = true
	entry.Reason = ManualPrequeueReason
	entry.ExpiresAt = manualPrequeueExpiry
	if entry.Status == PrequeueStatusReady && entry.StreamPath != "" {
		s.saveReadyEntry(entry)
	}
	return true
}

// Get retrieves a prequeue entry by ID
func (s *PrequeueStore) Get(id string) (*PrequeueEntry, bool) {
	return s.get(id, false)
}

// GetForPlayback rechecks local registrations before reusing prepared media.
// Status polling retains its cheap validation cache. External URLs are checked
// separately by the handler, so they do not acquire a second remote probe here.
func (s *PrequeueStore) GetForPlayback(id string) (*PrequeueEntry, bool) {
	return s.get(id, true)
}

func (s *PrequeueStore) get(id string, forPlayback bool) (*PrequeueEntry, bool) {
	s.mu.RLock()

	entry, exists := s.entries[id]
	if !exists {
		s.mu.RUnlock()
		return nil, false
	}

	// Check if expired
	if time.Now().After(entry.ExpiresAt) {
		s.mu.RUnlock()
		return nil, false
	}

	entryCopy := copyPrequeueEntryForRead(entry)
	validator := s.streamPathValidator
	snapshot := readyValidationSnapshot{
		ID:         entryCopy.ID,
		TitleID:    entryCopy.TitleID,
		UserID:     entryCopy.UserID,
		Status:     entryCopy.Status,
		StreamPath: entryCopy.StreamPath,
		RecentlyValidated: s.streamPathRecentlyValidatedLocked(id, entryCopy.StreamPath, time.Now()) &&
			(!forPlayback || strings.HasPrefix(entryCopy.StreamPath, "http://") || strings.HasPrefix(entryCopy.StreamPath, "https://")),
	}
	s.mu.RUnlock()

	if !s.validateReadyEntry(snapshot, validator) {
		return nil, false
	}

	return entryCopy, true
}

// GetByTitleUser retrieves a prequeue entry by title+user
func (s *PrequeueStore) GetByTitleUser(titleID, userID string) (*PrequeueEntry, bool) {
	return s.GetByTitleUserScope(titleID, userID, DefaultPrequeueSettingsScopeKey)
}

// GetByTitleUserScope retrieves a prequeue entry by title+user+effective settings scope.
func (s *PrequeueStore) GetByTitleUserScope(titleID, userID, settingsScopeKey string) (*PrequeueEntry, bool) {
	return s.getByTitleUserScope(titleID, userID, settingsScopeKey, false)
}

// GetByTitleUserScopeForPlayback validates local media at the reuse boundary.
func (s *PrequeueStore) GetByTitleUserScopeForPlayback(titleID, userID, settingsScopeKey string) (*PrequeueEntry, bool) {
	return s.getByTitleUserScope(titleID, userID, settingsScopeKey, true)
}

func (s *PrequeueStore) getByTitleUserScope(titleID, userID, settingsScopeKey string, forPlayback bool) (*PrequeueEntry, bool) {
	s.mu.RLock()

	key := titleUserKey(titleID, userID, settingsScopeKey)
	id, exists := s.byTitleUser[key]
	s.mu.RUnlock()

	if !exists {
		return nil, false
	}

	return s.get(id, forPlayback)
}

func (s *PrequeueStore) validateReadyEntry(entry readyValidationSnapshot, validator StreamPathValidator) bool {
	if validator == nil || entry.Status != PrequeueStatusReady || strings.TrimSpace(entry.StreamPath) == "" {
		return true
	}
	if entry.RecentlyValidated {
		return true
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := validator(ctx, entry.StreamPath); err != nil {
		log.Printf("[prequeue] Removing stale ready entry %s for title=%s user=%s path=%q: %s",
			entry.ID, entry.TitleID, entry.UserID, requestsecurity.URLForLog(entry.StreamPath), requestsecurity.TextForLog(err.Error()))
		s.mu.Lock()
		// A slow failed probe must not delete a newer manual selection.
		if current, ok := s.entries[entry.ID]; ok && current.Status == entry.Status && current.StreamPath == entry.StreamPath {
			s.deleteLocked(entry.ID)
		}
		s.mu.Unlock()
		return false
	}

	s.markStreamPathValidated(entry.ID, entry.StreamPath)
	return true
}

func streamPathValidationKey(id, streamPath string) string {
	return id + "\x00" + strings.TrimSpace(streamPath)
}

func (s *PrequeueStore) streamPathRecentlyValidatedLocked(id, streamPath string, now time.Time) bool {
	if s.streamPathValidated == nil {
		return false
	}
	validatedAt, ok := s.streamPathValidated[streamPathValidationKey(id, streamPath)]
	return ok && now.Sub(validatedAt) < readyStreamValidationTTL
}

func (s *PrequeueStore) markStreamPathValidated(id, streamPath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streamPathValidated == nil {
		s.streamPathValidated = make(map[string]time.Time)
	}
	s.streamPathValidated[streamPathValidationKey(id, streamPath)] = time.Now()
}

func (s *PrequeueStore) removeStreamPathValidationsLocked(id string) {
	for key := range s.streamPathValidated {
		if strings.HasPrefix(key, id+"\x00") {
			delete(s.streamPathValidated, key)
		}
	}
}

// Update updates a prequeue entry
func (s *PrequeueStore) Update(id string, updateFn func(*PrequeueEntry)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, exists := s.entries[id]
	if !exists {
		return false
	}

	oldValidationKey := streamPathValidationKey(id, entry.StreamPath)
	wasReady := entry.Status == PrequeueStatusReady
	updateFn(entry)
	if entry.Persistent {
		entry.Reason = ManualPrequeueReason
		entry.ExpiresAt = manualPrequeueExpiry
	}
	if streamPathValidationKey(id, entry.StreamPath) != oldValidationKey {
		delete(s.streamPathValidated, oldValidationKey)
	}

	// Extend TTL when status becomes ready (only if not already set further out)
	if entry.Status == PrequeueStatusReady {
		if strings.TrimSpace(entry.StreamPath) != "" && (!wasReady || streamPathValidationKey(id, entry.StreamPath) != oldValidationKey) {
			if s.streamPathValidated == nil {
				s.streamPathValidated = make(map[string]time.Time)
			}
			s.streamPathValidated[streamPathValidationKey(id, entry.StreamPath)] = time.Now()
		}
		// Only starting a newly ready stream earns a fresh TTL. Metadata and
		// scheduler updates must preserve its renewal deadline.
		if !wasReady {
			dynTTL := entry.DynamicTTL()
			if dynTTL <= 0 {
				dynTTL = s.defaultTTL
			}
			defaultExpiry := time.Now().Add(dynTTL)
			if entry.ExpiresAt.Before(defaultExpiry) {
				entry.ExpiresAt = defaultExpiry
			}
		}
		s.saveReadyEntry(entry)
	}

	return true
}

// UpdateWorker applies a background-worker update unless an explicit source
// replacement has taken ownership of the entry. This prevents a cancelled
// prequeue worker from overwriting a manual selection or migration while it is
// unwinding.
func (s *PrequeueStore) UpdateWorker(id string, updateFn func(*PrequeueEntry)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, exists := s.entries[id]
	if !exists || entry.MigrationAdopted {
		return false
	}

	oldValidationKey := streamPathValidationKey(id, entry.StreamPath)
	wasReady := entry.Status == PrequeueStatusReady
	updateFn(entry)
	if entry.Persistent {
		entry.Reason = ManualPrequeueReason
		entry.ExpiresAt = manualPrequeueExpiry
	}
	if streamPathValidationKey(id, entry.StreamPath) != oldValidationKey {
		delete(s.streamPathValidated, oldValidationKey)
	}

	if entry.Status == PrequeueStatusReady {
		if strings.TrimSpace(entry.StreamPath) != "" && (!wasReady || streamPathValidationKey(id, entry.StreamPath) != oldValidationKey) {
			if s.streamPathValidated == nil {
				s.streamPathValidated = make(map[string]time.Time)
			}
			s.streamPathValidated[streamPathValidationKey(id, entry.StreamPath)] = time.Now()
		}
		// Only starting a newly ready stream earns a fresh TTL. Metadata and
		// scheduler updates must preserve its renewal deadline.
		if !wasReady {
			dynTTL := entry.DynamicTTL()
			if dynTTL <= 0 {
				dynTTL = s.defaultTTL
			}
			defaultExpiry := time.Now().Add(dynTTL)
			if entry.ExpiresAt.Before(defaultExpiry) {
				entry.ExpiresAt = defaultExpiry
			}
		}
		s.saveReadyEntry(entry)
	}

	return true
}

// CancelWork stops any active resolver attached to an entry without removing
// the entry itself. The caller can then replace its payload in place.
func (s *PrequeueStore) CancelWork(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entry, exists := s.entries[id]; exists && entry.cancelFunc != nil {
		entry.cancelFunc()
		entry.cancelFunc = nil
	}
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

	s.deleteLocked(id)
}

func (s *PrequeueStore) deleteLocked(id string) {
	entry, exists := s.entries[id]
	if !exists {
		return
	}

	// Cancel if still running
	if entry.cancelFunc != nil {
		entry.cancelFunc()
	}

	// Remove from secondary index
	key := titleUserKey(entry.TitleID, entry.UserID, entry.SettingsScopeKey)
	if s.byTitleUser[key] == id {
		delete(s.byTitleUser, key)
	}
	s.removeStreamPathValidationsLocked(id)

	delete(s.entries, id)

	// Remove from DB
	if s.useDB() {
		if err := s.repo.Delete(context.Background(), id); err != nil {
			log.Printf("[prequeue] Warning: failed to delete entry %s from DB: %v", id, err)
		}
	} else {
		s.saveToDisk()
	}
}

// DeleteByStreamPath removes ready entries that point at the given stream path.
func (s *PrequeueStore) DeleteByStreamPath(streamPath string) []DeletedPrequeueEntry {
	streamPath = strings.TrimSpace(streamPath)
	if streamPath == "" {
		return nil
	}
	normalizedStreamPath := normalizePrequeueStreamPath(streamPath)

	s.mu.Lock()
	defer s.mu.Unlock()

	var removed []DeletedPrequeueEntry
	for id, entry := range s.entries {
		if strings.TrimSpace(entry.StreamPath) != streamPath && normalizePrequeueStreamPath(entry.StreamPath) != normalizedStreamPath {
			continue
		}

		if entry.cancelFunc != nil {
			entry.cancelFunc()
		}

		key := titleUserKey(entry.TitleID, entry.UserID, entry.SettingsScopeKey)
		if s.byTitleUser[key] == id {
			delete(s.byTitleUser, key)
		}
		s.removeStreamPathValidationsLocked(id)

		removed = append(removed, DeletedPrequeueEntry{
			ID:         id,
			TitleID:    entry.TitleID,
			TitleName:  entry.TitleName,
			UserID:     entry.UserID,
			StreamPath: entry.StreamPath,
		})
		delete(s.entries, id)

		if s.useDB() {
			if err := s.repo.Delete(context.Background(), id); err != nil {
				log.Printf("[prequeue] Warning: failed to delete stream-path entry %s from DB: %v", id, err)
			}
		}
	}

	if len(removed) > 0 && !s.useDB() {
		s.saveToDisk()
	}

	return removed
}

func normalizePrequeueStreamPath(streamPath string) string {
	p := strings.TrimSpace(streamPath)
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimPrefix(p, "webdav/")
	return strings.TrimPrefix(p, "/")
}

// FindReadyByStreamPath locates the most recently created ready entry whose
// stream path matches (after the same normalization the delete path uses). It
// is the read-only counterpart to DeleteByStreamPath and is used to correlate
// ad-hoc HLS starts (POST /video/hls/start) back to the prequeue request so
// end-to-end latency can be measured.
func (s *PrequeueStore) FindReadyByStreamPath(streamPath string) (*PrequeueEntry, bool) {
	streamPath = strings.TrimSpace(streamPath)
	if streamPath == "" {
		return nil, false
	}
	normalized := normalizePrequeueStreamPath(streamPath)

	s.mu.RLock()
	defer s.mu.RUnlock()

	var best *PrequeueEntry
	for _, entry := range s.entries {
		if entry.Status != PrequeueStatusReady {
			continue
		}
		if strings.TrimSpace(entry.StreamPath) != streamPath && normalizePrequeueStreamPath(entry.StreamPath) != normalized {
			continue
		}
		if best == nil || entry.CreatedAt.After(best.CreatedAt) {
			best = copyPrequeueEntryForRead(entry)
		}
	}
	if best == nil {
		return nil, false
	}
	return best, true
}

// Generation changes whenever all entries or a user's entries are cleared,
// i.e. after a change to how releases rank (global or profile ranking and
// filtering settings).
func (s *PrequeueStore) Generation() uint64 { return s.generation.Load() }

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
	s.generation.Add(1)
	s.saveToDisk()
	log.Printf("[prequeue] Cleared all prequeue entries")
}

// DeleteByUser removes all prequeue entries for a specific user.
func (s *PrequeueStore) DeleteByUser(userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(userID) == "" {
		return
	}

	removed := 0
	for id, entry := range s.entries {
		if entry.UserID != userID {
			continue
		}
		if entry.cancelFunc != nil {
			entry.cancelFunc()
		}

		key := titleUserKey(entry.TitleID, entry.UserID, entry.SettingsScopeKey)
		if s.byTitleUser[key] == id {
			delete(s.byTitleUser, key)
		}

		delete(s.entries, id)

		if s.useDB() {
			if err := s.repo.Delete(context.Background(), id); err != nil {
				log.Printf("[prequeue] Warning: failed to delete user entry %s from DB: %v", id, err)
			}
		}
		removed++
	}
	// Bumped even when nothing was queued: the user's ranking changed, and
	// picks kept outside the store must not be reused.
	s.generation.Add(1)

	if removed == 0 {
		return
	}

	s.saveToDisk()
	log.Printf("[prequeue] Cleared %d prequeue entries for user=%s", removed, userID)
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
		key := titleUserKey(entry.TitleID, entry.UserID, entry.SettingsScopeKey)
		if s.byTitleUser[key] == id {
			delete(s.byTitleUser, key)
		}

		delete(s.entries, id)
		log.Printf("[prequeue] Expired and removed prequeue %s", id)
	}
	if len(toDelete) > 0 {
		s.saveToDisk()
	}

	// Bulk-delete expired rows from DB (covers both in-memory and orphaned rows)
	if s.useDB() {
		if n, err := s.repo.DeleteExpired(context.Background()); err != nil {
			log.Printf("[prequeue] Warning: failed to delete expired DB rows: %v", err)
		} else if n > 0 {
			log.Printf("[prequeue] Cleaned up %d expired rows from database", n)
		}
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
			result = append(result, copyPrequeueEntryForRead(entry))
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
	return s.ListExpiringBefore(time.Now())
}

// ListExpiringBefore returns ready entries that need renewal before a deadline.
// Prewarm uses a look-ahead so cleanup cannot create a cold gap at expiry.
func (s *PrequeueStore) ListExpiringBefore(deadline time.Time) []*PrequeueEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*PrequeueEntry
	for _, entry := range s.entries {
		if entry.Status == PrequeueStatusReady && !entry.ExpiresAt.After(deadline) {
			result = append(result, copyPrequeueEntryForRead(entry))
		}
	}
	return result
}

// EpisodeReferencesMatch reports whether two episode references point at the same
// episode. Canonical identifiers and season-relative numbering take precedence;
// release absolute numbering remains a fallback for legacy anime references.
func EpisodeReferencesMatch(a, b *models.EpisodeReference) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if !models.SameEpisodeNumbering(a.Numbering, b.Numbering) {
		return false
	}
	if a.SeasonNumber == b.SeasonNumber && a.EpisodeNumber == b.EpisodeNumber {
		return true
	}
	if a.EpisodeID != "" && b.EpisodeID != "" {
		return a.EpisodeID == b.EpisodeID
	}
	if a.TvdbID != "" && b.TvdbID != "" {
		return a.TvdbID == b.TvdbID
	}
	// A persisted prequeue may already carry canonical metadata while an older
	// client request still carries TVDB's provider absolute number. Do not let
	// that legacy value override a concrete, mismatched season/episode identity.
	if hasCanonicalEpisodeIdentity(a) || hasCanonicalEpisodeIdentity(b) {
		return false
	}
	if a.AbsoluteEpisodeNumber > 0 && b.AbsoluteEpisodeNumber > 0 {
		return a.AbsoluteEpisodeNumber == b.AbsoluteEpisodeNumber
	}
	if a.AbsoluteEpisodeNumber > 0 && b.AbsoluteEpisodeNumber == 0 {
		return a.AbsoluteEpisodeNumber == b.EpisodeNumber
	}
	if b.AbsoluteEpisodeNumber > 0 && a.AbsoluteEpisodeNumber == 0 {
		return b.AbsoluteEpisodeNumber == a.EpisodeNumber
	}
	return false
}

func hasCanonicalEpisodeIdentity(episode *models.EpisodeReference) bool {
	return episode != nil && (episode.EpisodeID != "" || episode.TvdbID != "")
}

// ToResponse converts an entry to a status response
func (e *PrequeueEntry) ToResponse() *PrequeueStatusResponse {
	serviceType := e.ServiceType
	debridProvider := e.DebridProvider
	if serviceType == "" {
		streamPath := strings.ToLower(strings.TrimSpace(e.StreamPath))
		if strings.HasPrefix(streamPath, "/debrid/") {
			serviceType = "debrid"
		} else if streamPath != "" {
			serviceType = "usenet"
		}
	}
	if serviceType == "debrid" && debridProvider == "" {
		debridProvider, _ = parseDebridStreamPath(e.StreamPath)
	}
	return &PrequeueStatusResponse{
		PrequeueID:               e.ID,
		Status:                   e.Status,
		UserID:                   e.UserID,
		TargetEpisode:            e.TargetEpisode,
		ProgressStage:            e.ProgressStage,
		ProgressDetail:           e.ProgressDetail,
		ProgressCurrent:          e.ProgressCurrent,
		ProgressCurrentMin:       e.ProgressCurrentMin,
		ProgressCurrentMax:       e.ProgressCurrentMax,
		ProgressTotal:            e.ProgressTotal,
		StreamPath:               e.StreamPath,
		ServiceType:              serviceType,
		DebridProvider:           debridProvider,
		FileSize:                 e.FileSize,
		HealthStatus:             e.HealthStatus,
		HasDolbyVision:           e.HasDolbyVision,
		HasHDR10:                 e.HasHDR10,
		DolbyVisionProfile:       e.DolbyVisionProfile,
		DolbyVisionConfiguration: e.DolbyVisionConfiguration,
		NeedsAudioTranscode:      e.NeedsAudioTranscode,
		HLSSessionID:             e.HLSSessionID,
		HLSPlaylistURL:           e.HLSPlaylistURL,
		Duration:                 e.Duration,
		FrameRate:                e.FrameRate,
		SelectedAudioTrack:       e.SelectedAudioTrack,
		SelectedSubtitleTrack:    e.SelectedSubtitleTrack,
		TrackSelection:           e.TrackSelection,
		AudioTracks:              e.AudioTracks,
		SubtitleTracks:           e.SubtitleTracks,
		SubtitleSessions:         e.SubtitleSessions,
		PassthroughName:          e.PassthroughName,
		PassthroughDescription:   e.PassthroughDescription,
		ResultAttributes:         e.ResultAttributes,
		SelectedResult:           e.SelectedResult,
		SelectedResultIndex:      e.SelectedResultIndex,
		MigrationCandidates:      e.MigrationCandidates,
		Error:                    e.Error,
	}
}
