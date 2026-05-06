package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/internal/datastore"
	"novastream/models"
	"novastream/services/calendar"
)

var (
	ErrStorageDirRequired = errors.New("storage directory not provided")
	ErrUserIDRequired     = errors.New("user id is required")
	ErrSeriesIDRequired   = errors.New("series id is required")
)

const (
	continueWatchingCompletionThreshold = 90.0
	traktStopWatchThreshold             = 80.0
	continueWatchingComingSoonWindow    = 7 * 24 * time.Hour
	liveTVRecordingPathSegment          = "/live/recordings/"
)

// MetadataService provides series and movie metadata for continue watching generation.
type MetadataService interface {
	SeriesDetails(ctx context.Context, req models.SeriesDetailsQuery) (*models.SeriesDetails, error)
	SeriesDetailsLite(ctx context.Context, req models.SeriesDetailsQuery) (*models.SeriesDetails, error)
	SeriesInfo(ctx context.Context, req models.SeriesDetailsQuery) (*models.Title, error)
	MovieDetails(ctx context.Context, req models.MovieDetailsQuery) (*models.Title, error)
	MovieInfo(ctx context.Context, req models.MovieDetailsQuery) (*models.Title, error)
}

// TraktScrobbler handles syncing watch history to Trakt.
type TraktScrobbler interface {
	// ScrobbleMovie syncs a watched movie to Trakt for a specific user.
	ScrobbleMovie(userID string, tmdbID, tvdbID int, imdbID string, watchedAt time.Time) error
	// ScrobbleEpisode syncs a watched episode to Trakt using show TVDB ID + season/episode for a specific user.
	ScrobbleEpisode(userID string, showTVDBID, season, episode int, watchedAt time.Time) error
	// IsEnabled returns whether scrobbling is enabled for any account.
	IsEnabled() bool
	// IsEnabledForUser returns whether scrobbling is enabled for a specific user.
	IsEnabledForUser(userID string) bool
}

// TraktRealTimeScrobbler handles real-time scrobble events (start/pause/stop) to Trakt.
type TraktRealTimeScrobbler interface {
	HandleProgressUpdate(userID string, update models.PlaybackProgressUpdate, percentWatched float64)
	StopSession(userID string, update models.PlaybackProgressUpdate, percentWatched float64)
}

// cachedSeriesMetadata holds cached series details with expiration.
type cachedSeriesMetadata struct {
	details   *models.SeriesDetails
	cachedAt  time.Time
	expiresAt time.Time
}

// cachedMovieMetadata holds cached movie details with expiration.
type cachedMovieMetadata struct {
	details   *models.Title
	cachedAt  time.Time
	expiresAt time.Time
}

// cachedSeriesInfo holds cached lightweight series info with expiration.
type cachedSeriesInfo struct {
	info      *models.Title
	cachedAt  time.Time
	expiresAt time.Time
}

// cachedContinueWatching holds cached continue watching response with expiration.
type cachedContinueWatching struct {
	items     []models.SeriesWatchState
	cachedAt  time.Time
	expiresAt time.Time
}

// Service persists watch history for all content (movies, series, episodes).
type Service struct {
	mu                    sync.RWMutex
	store                 *datastore.DataStore
	path                  string
	watchHistPath         string
	playbackProgressPath  string
	states                map[string]map[string]models.SeriesWatchState // Deprecated: kept for migration only
	watchHistory          map[string]map[string]models.WatchHistoryItem // Manual watch tracking (all media)
	playbackProgress      map[string]map[string]models.PlaybackProgress // userID -> mediaKey -> progress
	metadataService       MetadataService
	traktScrobbler        TraktScrobbler
	traktRTScrobbler      TraktRealTimeScrobbler
	metadataCache         map[string]*cachedSeriesMetadata // seriesID -> metadata (full details)
	seriesInfoCache       map[string]*cachedSeriesInfo     // seriesID -> lightweight info
	movieMetadataCache    map[string]*cachedMovieMetadata  // movieID -> metadata
	metadataCacheTTL      time.Duration
	continueWatchingCache map[string]*cachedContinueWatching // userID -> continue watching
	continueWatchingTTL   time.Duration
}

type continueWatchingRevisionStats struct {
	watchHistoryCount     int
	watchHistoryUpdated   time.Time
	playbackProgressCount int
	hiddenCount           int
	playbackUpdated       time.Time
}

// useDB returns true when the service is backed by PostgreSQL.
func (s *Service) useDB() bool { return s.store != nil }

// NewServiceWithStore creates a history service backed by PostgreSQL.
func NewServiceWithStore(store *datastore.DataStore) (*Service, error) {
	svc := &Service{
		store:                 store,
		states:                make(map[string]map[string]models.SeriesWatchState),
		watchHistory:          make(map[string]map[string]models.WatchHistoryItem),
		playbackProgress:      make(map[string]map[string]models.PlaybackProgress),
		metadataCache:         make(map[string]*cachedSeriesMetadata),
		seriesInfoCache:       make(map[string]*cachedSeriesInfo),
		movieMetadataCache:    make(map[string]*cachedMovieMetadata),
		metadataCacheTTL:      24 * time.Hour,
		continueWatchingCache: make(map[string]*cachedContinueWatching),
		continueWatchingTTL:   10 * time.Minute,
	}

	if err := svc.loadWatchHistory(); err != nil {
		return nil, err
	}

	if err := svc.loadPlaybackProgress(); err != nil {
		return nil, err
	}

	return svc, nil
}

// NewService constructs a history service backed by a JSON file on disk.
func NewService(storageDir string) (*Service, error) {
	if strings.TrimSpace(storageDir) == "" {
		return nil, ErrStorageDirRequired
	}

	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		return nil, fmt.Errorf("create history dir: %w", err)
	}

	svc := &Service{
		path:                  filepath.Join(storageDir, "watch_history.json"),
		watchHistPath:         filepath.Join(storageDir, "watched_items.json"),
		playbackProgressPath:  filepath.Join(storageDir, "playback_progress.json"),
		states:                make(map[string]map[string]models.SeriesWatchState),
		watchHistory:          make(map[string]map[string]models.WatchHistoryItem),
		playbackProgress:      make(map[string]map[string]models.PlaybackProgress),
		metadataCache:         make(map[string]*cachedSeriesMetadata),
		seriesInfoCache:       make(map[string]*cachedSeriesInfo),
		movieMetadataCache:    make(map[string]*cachedMovieMetadata),
		metadataCacheTTL:      24 * time.Hour, // Cache metadata for 24 hours - ensures new episodes are detected daily
		continueWatchingCache: make(map[string]*cachedContinueWatching),
		continueWatchingTTL:   10 * time.Minute, // Cache continue watching response for 10 minutes - reduces frequent rebuilds
	}

	if err := svc.load(); err != nil {
		return nil, err
	}

	if err := svc.loadWatchHistory(); err != nil {
		return nil, err
	}

	if err := svc.loadPlaybackProgress(); err != nil {
		return nil, err
	}

	return svc, nil
}

// SetMetadataService sets the metadata service for continue watching generation.
func (s *Service) SetMetadataService(metadataService MetadataService) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metadataService = metadataService
}

// SetTraktScrobbler sets the Trakt scrobbler for syncing watch history.
func (s *Service) SetTraktScrobbler(scrobbler TraktScrobbler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traktScrobbler = scrobbler
}

// SetTraktRealTimeScrobbler sets the real-time scrobble tracker for live playback events.
func (s *Service) SetTraktRealTimeScrobbler(scrobbler TraktRealTimeScrobbler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traktRTScrobbler = scrobbler
}

// scrobbleWatchedItem syncs a watched item to Trakt if scrobbling is enabled for the user.
// This should be called after an item is marked as watched.
// IMPORTANT: This method must NOT be called while holding s.mu lock, as it spawns
// goroutines that may need to access the lock. Pass the scrobbler reference directly
// if calling from a locked context.
func (s *Service) scrobbleWatchedItem(userID string, item models.WatchHistoryItem) {
	s.mu.RLock()
	scrobbler := s.traktScrobbler
	s.mu.RUnlock()

	s.doScrobble(scrobbler, userID, item)
}

// doScrobble performs the actual scrobbling. This is separated from scrobbleWatchedItem
// to allow callers holding the lock to pass the scrobbler directly without re-acquiring the lock.
func (s *Service) doScrobble(scrobbler TraktScrobbler, userID string, item models.WatchHistoryItem) {
	if scrobbler == nil || !scrobbler.IsEnabledForUser(userID) {
		return
	}

	// Extract IDs from the item
	var tmdbID, tvdbID int
	var imdbID string

	if item.ExternalIDs != nil {
		if id, ok := item.ExternalIDs["tmdb"]; ok {
			tmdbID, _ = strconv.Atoi(id)
		}
		if id, ok := item.ExternalIDs["tvdb"]; ok {
			tvdbID, _ = strconv.Atoi(id)
		}
		if id, ok := item.ExternalIDs["imdb"]; ok {
			imdbID = id
		}
	}

	watchedAt := item.WatchedAt
	if watchedAt.IsZero() {
		watchedAt = time.Now().UTC()
	}

	switch item.MediaType {
	case "movie":
		if tmdbID > 0 || tvdbID > 0 || imdbID != "" {
			go func() {
				if err := scrobbler.ScrobbleMovie(userID, tmdbID, tvdbID, imdbID, watchedAt); err != nil {
					log.Printf("[trakt] failed to scrobble movie %s for user %s: %v", item.Name, userID, err)
				} else {
					log.Printf("[trakt] scrobbled movie: %s for user %s", item.Name, userID)
				}
			}()
		}
	case "episode":
		// For episodes, we need the show's TVDB ID plus season/episode numbers
		if tvdbID > 0 && item.SeasonNumber > 0 && item.EpisodeNumber > 0 {
			season := item.SeasonNumber
			episode := item.EpisodeNumber
			seriesName := item.SeriesName
			go func() {
				if err := scrobbler.ScrobbleEpisode(userID, tvdbID, season, episode, watchedAt); err != nil {
					log.Printf("[trakt] failed to scrobble episode %s S%02dE%02d for user %s: %v", seriesName, season, episode, userID, err)
				} else {
					log.Printf("[trakt] scrobbled episode: %s S%02dE%02d for user %s", seriesName, season, episode, userID)
				}
			}()
		} else {
			log.Printf("[trakt] skipping episode scrobble: missing tvdbID=%d, season=%d, or episode=%d", tvdbID, item.SeasonNumber, item.EpisodeNumber)
		}
	}
}

// RecordEpisode notes that the user has started watching the supplied episode.
// This now records to watch history instead of the old states map.
func (s *Service) RecordEpisode(userID string, payload models.EpisodeWatchPayload) (models.SeriesWatchState, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return models.SeriesWatchState{}, ErrUserIDRequired
	}

	seriesID := strings.TrimSpace(payload.SeriesID)
	if seriesID == "" {
		return models.SeriesWatchState{}, ErrSeriesIDRequired
	}

	episode := normaliseEpisode(payload.Episode)

	// Record episode to watch history
	// Build episode-specific ItemID: seriesID:s01e02 format (lowercase for consistency)
	episodeItemID := fmt.Sprintf("%s:s%02de%02d", seriesID, episode.SeasonNumber, episode.EpisodeNumber)
	watched := true
	update := models.WatchHistoryUpdate{
		MediaType:     "episode",
		ItemID:        episodeItemID,
		Name:          episode.Title,
		Year:          payload.Year,
		Watched:       &watched,
		WatchedAt:     time.Now().UTC(), // Always use current time for active user watches
		ExternalIDs:   payload.ExternalIDs,
		SeasonNumber:  episode.SeasonNumber,
		EpisodeNumber: episode.EpisodeNumber,
		SeriesID:      seriesID,
		SeriesName:    payload.SeriesTitle,
	}

	if _, err := s.UpdateWatchHistory(userID, update); err != nil {
		return models.SeriesWatchState{}, err
	}

	// Invalidate continue watching cache for this user since they watched something new
	s.mu.Lock()
	delete(s.continueWatchingCache, userID)
	s.mu.Unlock()

	// Build and return current state from watch history
	ctx := context.Background()
	states, err := s.buildSeriesStatesFromHistory(ctx, userID, true)
	if err != nil {
		return models.SeriesWatchState{}, err
	}

	// Cache the newly built result
	s.mu.Lock()
	s.continueWatchingCache[userID] = &cachedContinueWatching{
		items:     states,
		cachedAt:  time.Now(),
		expiresAt: time.Now().Add(s.continueWatchingTTL),
	}
	s.mu.Unlock()

	// Find the state for this series
	for _, state := range states {
		if state.SeriesID == seriesID {
			return state, nil
		}
	}

	// If not in continue watching (e.g., no next episode), build a minimal state
	return models.SeriesWatchState{
		SeriesID:    seriesID,
		SeriesTitle: payload.SeriesTitle,
		PosterURL:   payload.PosterURL,
		BackdropURL: payload.BackdropURL,
		Year:        payload.Year,
		ExternalIDs: payload.ExternalIDs,
		LastWatched: episode,
		NextEpisode: payload.NextEpisode,
		UpdatedAt:   time.Now().UTC(),
		WatchedEpisodes: map[string]models.EpisodeReference{
			episodeKey(episode.SeasonNumber, episode.EpisodeNumber): episode,
		},
	}, nil
}

// GetSeriesWatchState returns the watch state for a specific series, or nil if not found.
func (s *Service) GetSeriesWatchState(userID, seriesID string) (*models.SeriesWatchState, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUserIDRequired
	}

	seriesID = strings.TrimSpace(seriesID)
	if seriesID == "" {
		return nil, ErrSeriesIDRequired
	}

	s.mu.RLock()
	// Try deprecated map first for backward compatibility
	if perUser, ok := s.states[userID]; ok {
		if state, ok := perUser[seriesID]; ok {
			s.mu.RUnlock()
			return &state, nil
		}
	}
	s.mu.RUnlock()

	// Not in deprecated map, compute from current history
	// We use the same logic as ListSeriesStates but filter to the specific seriesID.
	// This ensures consistency across the app.
	states, err := s.ListSeriesStates(userID)
	if err != nil {
		return nil, err
	}

	for _, state := range states {
		if state.SeriesID == seriesID {
			return &state, nil
		}
	}

	return nil, nil
}

// ListContinueWatching returns series where a follow-up episode is available.
// This is now generated from watch history instead of explicit RecordEpisode calls.
// Results are cached for a short TTL (10 min) to reduce frequent rebuilds,
// but metadata is cached for 24 hours to detect new episodes/seasons.
func (s *Service) ListContinueWatching(userID string) ([]models.SeriesWatchState, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUserIDRequired
	}

	recordingTitleSet := s.recordingTitleSetForUser(userID)

	// Check cache first
	s.mu.RLock()
	cached, exists := s.continueWatchingCache[userID]
	s.mu.RUnlock()

	if exists && time.Now().Before(cached.expiresAt) {
		return filterRecordingContinueWatchingItems(cached.items, recordingTitleSet), nil
	}

	// Cache miss or expired - rebuild (only in-progress items for continue watching)
	ctx := context.Background()
	items, err := s.buildSeriesStatesFromHistory(ctx, userID, true)
	if err != nil {
		return nil, err
	}
	items = filterRecordingContinueWatchingItems(items, recordingTitleSet)

	// Cache the result
	s.mu.Lock()
	s.continueWatchingCache[userID] = &cachedContinueWatching{
		items:     items,
		cachedAt:  time.Now(),
		expiresAt: time.Now().Add(s.continueWatchingTTL),
	}
	s.mu.Unlock()

	return items, nil
}

func isLiveTVRecordingProgress(progress models.PlaybackProgress) bool {
	itemID := strings.ToLower(strings.TrimSpace(progress.ItemID))
	id := strings.ToLower(strings.TrimSpace(progress.ID))
	return strings.Contains(itemID, liveTVRecordingPathSegment) || strings.Contains(id, liveTVRecordingPathSegment)
}

func isLiveTVRecordingProgressUpdate(update models.PlaybackProgressUpdate) bool {
	itemID := strings.ToLower(strings.TrimSpace(update.ItemID))
	seriesID := strings.ToLower(strings.TrimSpace(update.SeriesID))
	return strings.Contains(itemID, liveTVRecordingPathSegment) || strings.Contains(seriesID, liveTVRecordingPathSegment)
}

func isLegacyRecordingTitleProgress(progress models.PlaybackProgress, recordingTitleSet map[string]struct{}) bool {
	if len(recordingTitleSet) == 0 || progress.MediaType != "movie" || len(progress.ExternalIDs) > 0 {
		return false
	}
	itemID := normaliseRecordingTitleKey(progress.ItemID)
	movieName := normaliseRecordingTitleKey(progress.MovieName)
	if itemID == "" || movieName == "" || itemID != movieName {
		return false
	}
	_, ok := recordingTitleSet[itemID]
	return ok
}

func isUnresolvedTitleOnlyMovieProgress(progress models.PlaybackProgress, movieDetails *models.Title) bool {
	if movieDetails != nil || progress.MediaType != "movie" || len(progress.ExternalIDs) > 0 || progress.Year > 0 {
		return false
	}
	itemID := normaliseRecordingTitleKey(progress.ItemID)
	movieName := normaliseRecordingTitleKey(progress.MovieName)
	return itemID != "" && movieName != "" && itemID == movieName
}

func isLiveTVRecordingContinueWatchingItem(item models.SeriesWatchState, recordingTitleSet map[string]struct{}) bool {
	seriesID := strings.ToLower(strings.TrimSpace(item.SeriesID))
	if strings.Contains(seriesID, liveTVRecordingPathSegment) {
		return true
	}
	if len(recordingTitleSet) == 0 || item.NextEpisode != nil || len(item.ExternalIDs) > 0 {
		return false
	}
	titleKey := normaliseRecordingTitleKey(item.SeriesID)
	if titleKey == "" || titleKey != normaliseRecordingTitleKey(item.SeriesTitle) {
		return false
	}
	_, ok := recordingTitleSet[titleKey]
	return ok
}

func filterRecordingContinueWatchingItems(items []models.SeriesWatchState, recordingTitleSet map[string]struct{}) []models.SeriesWatchState {
	if len(items) == 0 {
		return items
	}

	filtered := make([]models.SeriesWatchState, 0, len(items))
	for _, item := range items {
		if isLiveTVRecordingContinueWatchingItem(item, recordingTitleSet) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func normaliseRecordingTitleKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func (s *Service) recordingTitleSetForUser(userID string) map[string]struct{} {
	if s.store == nil {
		return nil
	}

	recordings, err := s.store.Recordings().List(context.Background(), models.RecordingListFilter{
		UserID:     userID,
		IncludeAll: true,
	})
	if err != nil || len(recordings) == 0 {
		return nil
	}

	titles := make(map[string]struct{}, len(recordings))
	for _, recording := range recordings {
		if title := normaliseRecordingTitleKey(recording.Title); title != "" {
			titles[title] = struct{}{}
		}
	}
	if len(titles) == 0 {
		return nil
	}
	return titles
}

func (s *Service) GetContinueWatchingRevision(userID string) (string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", ErrUserIDRequired
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := continueWatchingRevisionStats{}
	if perUser := s.watchHistory[userID]; perUser != nil {
		stats.watchHistoryCount = len(perUser)
		for _, item := range perUser {
			if item.UpdatedAt.After(stats.watchHistoryUpdated) {
				stats.watchHistoryUpdated = item.UpdatedAt
			}
		}
	}
	if perUser := s.playbackProgress[userID]; perUser != nil {
		stats.playbackProgressCount = len(perUser)
		for _, item := range perUser {
			if item.HiddenFromContinueWatching {
				stats.hiddenCount++
			}
			if item.UpdatedAt.After(stats.playbackUpdated) {
				stats.playbackUpdated = item.UpdatedAt
			}
		}
	}

	return fmt.Sprintf(
		"wh:%d:%d|pp:%d:%d:%d",
		stats.watchHistoryCount,
		stats.watchHistoryUpdated.UTC().UnixNano(),
		stats.playbackProgressCount,
		stats.hiddenCount,
		stats.playbackUpdated.UTC().UnixNano(),
	), nil
}

// ListSeriesStates returns the watch state for ALL series the user has watched,
// including those with no next episode (fully watched).
func (s *Service) ListSeriesStates(userID string) ([]models.SeriesWatchState, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUserIDRequired
	}

	// We don't cache "all series" states currently as it's typically used
	// for the watchlist and we want the most fresh state.
	ctx := context.Background()
	return s.buildSeriesStatesFromHistory(ctx, userID, false)
}

// buildSeriesStatesFromHistory generates watch state for series from watch history and playback progress.
// If onlyInProgress is true, it only returns series with an available next episode or active progress.
// Prioritizes in-progress episodes (partially watched) over completed episodes.
// Metadata lookups are parallelized for better performance.
func (s *Service) buildSeriesStatesFromHistory(ctx context.Context, userID string, onlyInProgress bool) ([]models.SeriesWatchState, error) {
	s.mu.RLock()
	metadataSvc := s.metadataService
	s.mu.RUnlock()

	if metadataSvc == nil {
		// Metadata service not available, return empty list
		return []models.SeriesWatchState{}, nil
	}

	// Get playback progress for in-progress items
	progressItems, err := s.ListPlaybackProgress(userID)
	if err != nil {
		return nil, err
	}

	// Get all watch history items
	items, err := s.ListWatchHistory(userID)
	if err != nil {
		return nil, err
	}

	// Build a canonical series ID map to merge series that share external IDs
	// (e.g., player uses "tvdb:series:353546" while Trakt sync uses "tmdb:tv:82728" for the same show)
	// This must happen before any grouping by seriesID.
	canonicalSeriesID := buildCanonicalSeriesIDMap(items, progressItems)

	// Build set of hidden series IDs from progress items
	hiddenSeriesIDs := make(map[string]bool)
	for _, prog := range progressItems {
		if prog.HiddenFromContinueWatching {
			// Add both the itemID (for movies) and seriesID (for episodes)
			if prog.ItemID != "" {
				hiddenSeriesIDs[prog.ItemID] = true
			}
			if prog.SeriesID != "" {
				hiddenSeriesIDs[resolveCanonicalID(canonicalSeriesID, prog.SeriesID)] = true
			}
			// Also extract series ID from episode itemId (format: "tvdb:series:12345:S01E01")
			// This handles cases where seriesId wasn't stored but can be inferred
			if prog.MediaType == "episode" || (prog.SeasonNumber > 0 && prog.EpisodeNumber > 0) {
				parts := strings.Split(prog.ItemID, ":")
				// Look for :S pattern to find where episode info starts
				for i := len(parts) - 1; i >= 0; i-- {
					if strings.HasPrefix(parts[i], "S") && len(parts[i]) > 1 {
						inferredSeriesID := strings.Join(parts[:i], ":")
						if inferredSeriesID != "" {
							hiddenSeriesIDs[resolveCanonicalID(canonicalSeriesID, inferredSeriesID)] = true
						}
						break
					}
				}
			}
		}
	}
	// Map of seriesID -> in-progress episode (0-90% watched)
	// Note: Check both MediaType=="episode" AND presence of season/episode numbers
	// (in case mediaType wasn't properly set but it has episode data)
	inProgressBySeriesCache := make(map[string]*models.PlaybackProgress)
	for i := range progressItems {
		prog := &progressItems[i]

		// Skip hidden items
		if prog.HiddenFromContinueWatching {
			continue
		}
		if isSeriesLevelPlaybackMarker(*prog) {
			continue
		}

		isEpisode := prog.MediaType == "episode" || (prog.SeasonNumber > 0 && prog.EpisodeNumber > 0)

		// Try to infer series ID if missing (from itemId or external IDs)
		seriesID := prog.SeriesID
		if seriesID == "" && isEpisode {
			// Try to extract from itemId (format: "seriesId:S01E02")
			parts := strings.Split(prog.ItemID, ":")
			if len(parts) >= 2 && strings.HasPrefix(parts[len(parts)-1], "S") {
				// ItemId is like "tvdb:123:S01E02", extract everything before the :S pattern
				for i := len(parts) - 1; i >= 0; i-- {
					if strings.HasPrefix(parts[i], "S") && len(parts[i]) > 1 {
						seriesID = strings.Join(parts[:i], ":")
						break
					}
				}
			} else {
				// ItemId might just be the series ID
				seriesID = prog.ItemID
			}
		}

		// Resolve to canonical ID so player and Trakt entries merge
		seriesID = resolveCanonicalID(canonicalSeriesID, seriesID)

		if isEpisode && seriesID != "" && prog.PercentWatched < 90 && !hiddenSeriesIDs[seriesID] {
			// Keep the most recently updated in-progress episode per series
			existing := inProgressBySeriesCache[seriesID]
			if existing == nil || prog.UpdatedAt.After(existing.UpdatedAt) {
				// Store with inferred seriesID if it was missing
				if prog.SeriesID == "" {
					prog.SeriesID = seriesID
				}
				inProgressBySeriesCache[seriesID] = prog
			}
		}
	}
	// Filter to watched episodes from the past 365 days
	cutoffDate := time.Now().UTC().AddDate(-1, 0, 0) // 365 days ago
	seriesEpisodes := make(map[string][]models.WatchHistoryItem)
	seriesInfo := make(map[string]models.WatchHistoryItem) // Track series metadata

	for _, item := range items {
		if item.MediaType == "episode" && item.Watched && item.SeriesID != "" {
			resolvedID := resolveCanonicalID(canonicalSeriesID, item.SeriesID)
			// Skip hidden series
			if hiddenSeriesIDs[resolvedID] {
				continue
			}
			if item.WatchedAt.After(cutoffDate) {
				seriesEpisodes[resolvedID] = append(seriesEpisodes[resolvedID], item)
				if _, exists := seriesInfo[resolvedID]; !exists {
					seriesInfo[resolvedID] = item
				}
			}
		}
	}

	// Also consider series with only in-progress items (no completed episodes)
	for seriesID, prog := range inProgressBySeriesCache {
		if _, exists := seriesInfo[seriesID]; !exists && prog.SeriesName != "" {
			// Create a minimal watch history item for metadata purposes
			seriesInfo[seriesID] = models.WatchHistoryItem{
				SeriesID:      prog.SeriesID,
				SeriesName:    prog.SeriesName,
				ExternalIDs:   prog.ExternalIDs,
				Year:          prog.Year,
				SeasonNumber:  prog.SeasonNumber,
				EpisodeNumber: prog.EpisodeNumber,
			}
		}
	}

	recordingTitleSet := s.recordingTitleSetForUser(userID)

	// Collect movies that need processing (filter out <5% watched and hidden)
	var moviesToProcess []*models.PlaybackProgress
	for i := range progressItems {
		prog := &progressItems[i]

		// Skip hidden movies
		if prog.HiddenFromContinueWatching {
			continue
		}
		if isLiveTVRecordingProgress(*prog) {
			continue
		}
		if isLegacyRecordingTitleProgress(*prog, recordingTitleSet) {
			continue
		}

		// Only include movies with 5-90% progress (resume watching)
		// Movies with <5% watched are excluded as they likely weren't really started
		if prog.MediaType == "movie" && prog.PercentWatched >= 5 && prog.PercentWatched < 90 {
			moviesToProcess = append(moviesToProcess, prog)
		}
	}

	// === PARALLEL METADATA LOOKUPS ===
	// Use a semaphore to limit concurrent metadata requests
	const maxConcurrent = 10
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	var mu sync.Mutex

	// Results will be collected here
	var continueWatching []models.SeriesWatchState

	// Process series in parallel
	type seriesTask struct {
		seriesID   string
		info       models.WatchHistoryItem
		episodes   []models.WatchHistoryItem
		inProgress *models.PlaybackProgress
	}

	var seriesTasks []seriesTask
	for seriesID := range seriesInfo {
		seriesTasks = append(seriesTasks, seriesTask{
			seriesID:   seriesID,
			info:       seriesInfo[seriesID],
			episodes:   seriesEpisodes[seriesID],
			inProgress: inProgressBySeriesCache[seriesID],
		})
	}

	for _, task := range seriesTasks {
		wg.Add(1)
		go func(t seriesTask) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }()

			var state models.SeriesWatchState
			var nextEpisode *models.EpisodeReference

			// Priority 1: In-progress episode (resume watching)
			// Skip if the in-progress episode is already marked as watched in history
			// (e.g. Trakt sync marked it complete while stale playback progress remains),
			// or if it points behind the furthest watched episode and is not newer than
			// the latest watch-history activity for the series.
			inProgressAlreadyWatched := false
			inProgressStaleBehindLatestWatched := false
			if t.inProgress != nil {
				var latestWatchedAt time.Time
				var furthestWatched *models.WatchHistoryItem
				for _, ep := range t.episodes {
					if ep.SeasonNumber == t.inProgress.SeasonNumber && ep.EpisodeNumber == t.inProgress.EpisodeNumber {
						inProgressAlreadyWatched = true
					}
					if ep.WatchedAt.After(latestWatchedAt) {
						latestWatchedAt = ep.WatchedAt
					}
					if furthestWatched == nil || compareEpisodeOrder(ep.SeasonNumber, ep.EpisodeNumber, furthestWatched.SeasonNumber, furthestWatched.EpisodeNumber) > 0 {
						episode := ep
						furthestWatched = &episode
					}
				}
				if !inProgressAlreadyWatched && furthestWatched != nil &&
					compareEpisodeOrder(furthestWatched.SeasonNumber, furthestWatched.EpisodeNumber, t.inProgress.SeasonNumber, t.inProgress.EpisodeNumber) > 0 &&
					!t.inProgress.UpdatedAt.After(latestWatchedAt) {
					inProgressStaleBehindLatestWatched = true
				}
				if inProgressAlreadyWatched || inProgressStaleBehindLatestWatched {
					// Clean up all stale progress entries for this series where
					// the episode is already in watch history, or stale progress
					// points behind the furthest watched episode for the series.
					watchedEps := make(map[string]bool, len(t.episodes))
					for _, ep := range t.episodes {
						watchedEps[episodeKey(ep.SeasonNumber, ep.EpisodeNumber)] = true
					}
					seriesExtIDs := t.inProgress.ExternalIDs
					targetSeason := t.inProgress.SeasonNumber
					targetEpisode := t.inProgress.EpisodeNumber
					userID := userID
					go func() {
						s.mu.Lock()
						defer s.mu.Unlock()
						perUser, ok := s.playbackProgress[userID]
						if !ok {
							return
						}
						var cleaned int
						for key, prog := range perUser {
							if prog.MediaType != "episode" || prog.SeasonNumber <= 0 {
								continue
							}
							if !hasMatchingExternalID(prog.ExternalIDs, seriesExtIDs) {
								continue
							}
							if watchedEps[episodeKey(prog.SeasonNumber, prog.EpisodeNumber)] ||
								(prog.SeasonNumber == targetSeason && prog.EpisodeNumber == targetEpisode) {
								log.Printf("[history] cleaning up stale progress %s (episode already watched)", key)
								delete(perUser, key)
								cleaned++
							}
						}
						if cleaned > 0 {
							_ = s.savePlaybackProgressLocked()
						}
					}()
				}
			}
			if t.inProgress != nil && !inProgressAlreadyWatched && !inProgressStaleBehindLatestWatched {
				// The in-progress episode IS the next episode to watch
				nextEpisode = &models.EpisodeReference{
					SeasonNumber:  t.inProgress.SeasonNumber,
					EpisodeNumber: t.inProgress.EpisodeNumber,
					Title:         t.inProgress.EpisodeName,
				}

				// Use the most recent timestamp between the in-progress entry and
				// any watched episode (e.g. a Trakt sync may mark many episodes
				// watched today while the in-progress entry is older).
				updatedAt := t.inProgress.UpdatedAt
				for _, ep := range t.episodes {
					if ep.WatchedAt.After(updatedAt) {
						updatedAt = ep.WatchedAt
					}
				}

				// For in-progress, use the in-progress episode as both last and next
				// Copy ExternalIDs to avoid concurrent map writes from parallel goroutines
				extIDs := make(map[string]string, len(t.inProgress.ExternalIDs))
				for k, v := range t.inProgress.ExternalIDs {
					extIDs[k] = v
				}
				state = models.SeriesWatchState{
					SeriesID:    t.seriesID,
					SeriesTitle: t.inProgress.SeriesName,
					Year:        t.inProgress.Year,
					ExternalIDs: extIDs,
					UpdatedAt:   updatedAt,
					LastWatched: *nextEpisode,
					NextEpisode: nextEpisode,
				}

				// Get full series details for poster, backdrop, IDs, and episode counts
				seriesDetails, err := s.getSeriesMetadataWithCache(ctx, t.seriesID, t.info.SeriesName, t.info.ExternalIDs)
				if err == nil && seriesDetails != nil {
					// Add overview from metadata
					if seriesDetails.Title.Overview != "" {
						state.Overview = seriesDetails.Title.Overview
					}
					// Add poster/backdrop from metadata
					if seriesDetails.Title.Poster != nil {
						state.PosterURL = seriesDetails.Title.Poster.URL
					}
					if seriesDetails.Title.TextPoster != nil {
						state.TextPosterURL = seriesDetails.Title.TextPoster.URL
					}
					if seriesDetails.Title.Backdrop != nil {
						state.BackdropURL = seriesDetails.Title.Backdrop.URL
					}

					// Enrich external IDs from metadata (prioritize metadata over history)
					if state.ExternalIDs == nil {
						state.ExternalIDs = make(map[string]string)
					}
					if seriesDetails.Title.IMDBID != "" {
						state.ExternalIDs["imdb"] = seriesDetails.Title.IMDBID
					}
					if seriesDetails.Title.TMDBID > 0 {
						state.ExternalIDs["tmdb"] = fmt.Sprintf("%d", seriesDetails.Title.TMDBID)
					}
					if seriesDetails.Title.TVDBID > 0 {
						state.ExternalIDs["tvdb"] = fmt.Sprintf("%d", seriesDetails.Title.TVDBID)
					}

					// Use metadata year if available
					if seriesDetails.Title.Year > 0 {
						state.Year = seriesDetails.Title.Year
					}

					// Enrich next episode with metadata (title, overview, runtime, air date)
					// when the playback progress entry doesn't have them.
					if state.NextEpisode != nil {
						enrichEpisodeFromMetadata(state.NextEpisode, seriesDetails)
						state.LastWatched = *state.NextEpisode
					}

					// Calculate episode counts for series completion tracking
					state.TotalEpisodeCount = countTotalEpisodes(seriesDetails)
					// For in-progress, count watched episodes from history (dedup by season/episode)
					watchedSet := make(map[string]bool)
					for _, ep := range t.episodes {
						if ep.SeasonNumber > 0 {
							watchedSet[episodeKey(ep.SeasonNumber, ep.EpisodeNumber)] = true
						}
					}
					state.WatchedEpisodeCount = len(watchedSet)
				}
			} else if len(t.episodes) > 0 {
				// Priority 2: Next unwatched episode after most recently completed
				// For this case we DO need full series details to find the next episode
				// Sort episodes by watch date (most recent first)
				episodes := make([]models.WatchHistoryItem, len(t.episodes))
				copy(episodes, t.episodes)
				sort.Slice(episodes, func(i, j int) bool {
					return episodes[i].WatchedAt.After(episodes[j].WatchedAt)
				})

				mostRecentEpisode := episodes[0]

				// Get full series details (with all episodes) to find next unwatched
				seriesDetails, err := s.getSeriesMetadataWithCache(ctx, t.seriesID, t.info.SeriesName, t.info.ExternalIDs)
				if err != nil {
					// Skip this series if metadata unavailable
					return
				}

				// Find next unwatched episode
				nextEpisode = s.findNextUnwatchedEpisode(seriesDetails, mostRecentEpisode, episodes)
				if nextEpisode == nil && onlyInProgress {
					// No next episode available and only in-progress requested, skip this series
					return
				}

				// Copy ExternalIDs to avoid concurrent map writes from parallel goroutines
				extIDs := make(map[string]string, len(mostRecentEpisode.ExternalIDs))
				for k, v := range mostRecentEpisode.ExternalIDs {
					extIDs[k] = v
				}
				state = models.SeriesWatchState{
					SeriesID:    t.seriesID,
					SeriesTitle: mostRecentEpisode.SeriesName,
					Year:        mostRecentEpisode.Year,
					ExternalIDs: extIDs,
					UpdatedAt:   mostRecentEpisode.WatchedAt,
					LastWatched: s.convertToEpisodeRef(mostRecentEpisode),
					NextEpisode: nextEpisode,
				}
				if promotedAt, ok := continueWatchingRecentReleaseTime(nextEpisode); ok && promotedAt.After(state.UpdatedAt) {
					state.UpdatedAt = promotedAt
				}

				// Build watched episodes map
				watchedMap := make(map[string]models.EpisodeReference)
				for _, ep := range episodes {
					key := episodeKey(ep.SeasonNumber, ep.EpisodeNumber)
					watchedMap[key] = s.convertToEpisodeRef(ep)
				}
				state.WatchedEpisodes = watchedMap

				// Enrich with metadata from series details
				if seriesDetails != nil {
					// Add overview from metadata
					if seriesDetails.Title.Overview != "" {
						state.Overview = seriesDetails.Title.Overview
					}
					// Add poster/backdrop from metadata
					if seriesDetails.Title.Poster != nil {
						state.PosterURL = seriesDetails.Title.Poster.URL
					}
					if seriesDetails.Title.TextPoster != nil {
						state.TextPosterURL = seriesDetails.Title.TextPoster.URL
					}
					if seriesDetails.Title.Backdrop != nil {
						state.BackdropURL = seriesDetails.Title.Backdrop.URL
					}

					// Enrich external IDs from metadata (prioritize metadata over history)
					if state.ExternalIDs == nil {
						state.ExternalIDs = make(map[string]string)
					}
					if seriesDetails.Title.IMDBID != "" {
						state.ExternalIDs["imdb"] = seriesDetails.Title.IMDBID
					}
					if seriesDetails.Title.TMDBID > 0 {
						state.ExternalIDs["tmdb"] = fmt.Sprintf("%d", seriesDetails.Title.TMDBID)
					}
					if seriesDetails.Title.TVDBID > 0 {
						state.ExternalIDs["tvdb"] = fmt.Sprintf("%d", seriesDetails.Title.TVDBID)
					}

					// Use metadata year if available
					if seriesDetails.Title.Year > 0 {
						state.Year = seriesDetails.Title.Year
					}

					// Calculate episode counts for series completion tracking
					state.TotalEpisodeCount = countTotalEpisodes(seriesDetails)
					state.WatchedEpisodeCount = countWatchedEpisodes(state.WatchedEpisodes)
				}
			} else {
				// No episodes and no in-progress (shouldn't happen)
				return
			}

			// Add to results
			mu.Lock()
			continueWatching = append(continueWatching, state)
			mu.Unlock()
		}(task)
	}

	// Process movies in parallel
	for _, prog := range moviesToProcess {
		wg.Add(1)
		go func(p *models.PlaybackProgress) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }()

			// Enrich with metadata first (poster, backdrop, overview, etc)
			var movieDetails *models.Title
			if details, err := s.getMovieMetadataWithCache(ctx, p.ItemID, p.MovieName, p.Year, p.ExternalIDs); err == nil && details != nil {
				movieDetails = details
			}
			if isUnresolvedTitleOnlyMovieProgress(*p, movieDetails) {
				return
			}

			// Build the movie state with metadata
			movieState := models.SeriesWatchState{
				SeriesID:       p.ItemID,
				SeriesTitle:    p.MovieName,
				Year:           p.Year,
				ExternalIDs:    p.ExternalIDs,
				UpdatedAt:      p.UpdatedAt,
				PercentWatched: p.PercentWatched,
				// For movies, use LastWatched to store movie info with metadata overview
				LastWatched: models.EpisodeReference{
					Title:    p.MovieName,
					Overview: "", // Will be populated from metadata below
				},
				NextEpisode: nil, // nil indicates this is a movie resume, not a series
			}

			// Apply metadata enrichment if available
			if movieDetails != nil {
				// Add poster/backdrop from metadata
				if movieDetails.Poster != nil {
					movieState.PosterURL = movieDetails.Poster.URL
				}
				if movieDetails.TextPoster != nil {
					movieState.TextPosterURL = movieDetails.TextPoster.URL
				}
				if movieDetails.Backdrop != nil {
					movieState.BackdropURL = movieDetails.Backdrop.URL
				}

				// Use metadata overview (the key fix - populate overview from metadata)
				if movieDetails.Overview != "" {
					movieState.Overview = movieDetails.Overview
					movieState.LastWatched.Overview = movieDetails.Overview
				}

				// Enrich external IDs from metadata (prioritize metadata over progress)
				if movieState.ExternalIDs == nil {
					movieState.ExternalIDs = make(map[string]string)
				}
				if movieDetails.IMDBID != "" {
					movieState.ExternalIDs["imdb"] = movieDetails.IMDBID
				}
				if movieDetails.TMDBID > 0 {
					movieState.ExternalIDs["tmdb"] = fmt.Sprintf("%d", movieDetails.TMDBID)
				}
				if movieDetails.TVDBID > 0 {
					movieState.ExternalIDs["tvdb"] = fmt.Sprintf("%d", movieDetails.TVDBID)
				}

				// Use metadata year if available and more accurate
				if movieDetails.Year > 0 {
					movieState.Year = movieDetails.Year
				}

				// Use metadata title if available (better localization)
				if movieDetails.Name != "" {
					movieState.SeriesTitle = movieDetails.Name
					movieState.LastWatched.Title = movieDetails.Name
				}
			}

			// Add to results
			mu.Lock()
			continueWatching = append(continueWatching, movieState)
			mu.Unlock()
		}(prog)
	}

	// Wait for all metadata lookups to complete
	wg.Wait()

	continueWatching = dedupeContinueWatchingEntries(continueWatching)

	// Sort by most recently updated (in-progress items will naturally sort first if more recent)
	sort.Slice(continueWatching, func(i, j int) bool {
		if continueWatching[i].UpdatedAt.Equal(continueWatching[j].UpdatedAt) {
			return continueWatching[i].SeriesID < continueWatching[j].SeriesID
		}
		return continueWatching[i].UpdatedAt.After(continueWatching[j].UpdatedAt)
	})

	return continueWatching, nil
}

const continueWatchingRecentReleaseWindow = 72 * time.Hour

func continueWatchingRecentReleaseTime(nextEpisode *models.EpisodeReference) (time.Time, bool) {
	if nextEpisode == nil {
		return time.Time{}, false
	}

	var releaseTime time.Time
	switch {
	case strings.TrimSpace(nextEpisode.AirDateTimeUTC) != "":
		parsed, err := time.Parse(time.RFC3339, nextEpisode.AirDateTimeUTC)
		if err != nil {
			return time.Time{}, false
		}
		releaseTime = parsed.UTC()
	case strings.TrimSpace(nextEpisode.AirDate) != "":
		parsed, err := time.Parse("2006-01-02", nextEpisode.AirDate)
		if err != nil {
			return time.Time{}, false
		}
		releaseTime = parsed.UTC()
	default:
		return time.Time{}, false
	}

	now := time.Now().UTC()
	if releaseTime.After(now) {
		return time.Time{}, false
	}
	if now.Sub(releaseTime) > continueWatchingRecentReleaseWindow {
		return time.Time{}, false
	}
	return releaseTime, true
}

func continueWatchingComingSoonTime(nextEpisode *models.EpisodeReference) (time.Time, bool) {
	if nextEpisode == nil {
		return time.Time{}, false
	}

	var releaseTime time.Time
	switch {
	case strings.TrimSpace(nextEpisode.AirDateTimeUTC) != "":
		parsed, err := time.Parse(time.RFC3339, nextEpisode.AirDateTimeUTC)
		if err != nil {
			return time.Time{}, false
		}
		releaseTime = parsed.UTC()
	case strings.TrimSpace(nextEpisode.AirDate) != "":
		parsed, err := time.Parse("2006-01-02", nextEpisode.AirDate)
		if err != nil {
			return time.Time{}, false
		}
		releaseTime = parsed.UTC()
	default:
		return time.Time{}, false
	}

	now := time.Now().UTC()
	if !releaseTime.After(now) {
		return time.Time{}, false
	}
	if releaseTime.Sub(now) > continueWatchingComingSoonWindow {
		return time.Time{}, false
	}
	return releaseTime, true
}

// getMovieMetadataWithCache retrieves movie metadata with caching.
func (s *Service) getMovieMetadataWithCache(ctx context.Context, movieID, movieName string, year int, externalIDs map[string]string) (*models.Title, error) {
	s.mu.RLock()
	cached, exists := s.movieMetadataCache[movieID]
	metadataSvc := s.metadataService
	s.mu.RUnlock()

	// Check cache validity
	if exists && time.Now().Before(cached.expiresAt) {
		return cached.details, nil
	}

	if metadataSvc == nil {
		return nil, fmt.Errorf("metadata service not available")
	}

	// Build query from external IDs or parse from movieID
	query := models.MovieDetailsQuery{
		TitleID: movieID,
		Name:    movieName,
		Year:    year,
	}

	// Parse IDs from movieID first (more reliable than external IDs from history)
	// Format: "tmdb:movie:617126" or "tvdb:movie:123456"
	parts := strings.Split(movieID, ":")
	if len(parts) >= 2 {
		switch parts[0] {
		case "tvdb":
			// For TVDB movie IDs like "tvdb:movie:123456", extract the TVDB ID
			if len(parts) >= 3 {
				if id, err := strconv.ParseInt(parts[2], 10, 64); err == nil {
					query.TVDBID = id
				}
			} else if len(parts) >= 2 {
				if id, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
					query.TVDBID = id
				}
			}
		case "tmdb":
			// For TMDB movie IDs like "tmdb:movie:617126", extract the TMDB ID
			if len(parts) >= 3 {
				if id, err := strconv.ParseInt(parts[2], 10, 64); err == nil {
					query.TMDBID = id
				}
			}
		}
	}

	// Fallback to external IDs from playback progress to improve accuracy.
	// Always read TMDB/TVDB/IMDB values if they are available, but do not overwrite
	// IDs that were already parsed from the primary movie ID.
	if externalIDs != nil {
		if query.TMDBID == 0 {
			if tmdbID, ok := externalIDs["tmdb"]; ok {
				if id, err := strconv.ParseInt(tmdbID, 10, 64); err == nil {
					query.TMDBID = id
				}
			}
		}
		if query.TVDBID == 0 {
			if tvdbID, ok := externalIDs["tvdb"]; ok {
				if id, err := strconv.ParseInt(tvdbID, 10, 64); err == nil {
					query.TVDBID = id
				}
			}
		}
		if query.IMDBID == "" {
			if imdbID, ok := externalIDs["imdb"]; ok {
				query.IMDBID = imdbID
			}
		}
	}

	// Fetch from metadata service (use MovieInfo for lightweight fetch without ratings)
	details, err := metadataSvc.MovieInfo(ctx, query)
	if err != nil {
		return nil, err
	}

	// Cache the result
	s.mu.Lock()
	s.movieMetadataCache[movieID] = &cachedMovieMetadata{
		details:   details,
		cachedAt:  time.Now(),
		expiresAt: time.Now().Add(s.metadataCacheTTL),
	}
	s.mu.Unlock()

	return details, nil
}

// getSeriesMetadataWithCache retrieves series metadata with caching.
func (s *Service) getSeriesMetadataWithCache(ctx context.Context, seriesID, seriesName string, externalIDs map[string]string) (*models.SeriesDetails, error) {
	s.mu.RLock()
	cached, exists := s.metadataCache[seriesID]
	metadataSvc := s.metadataService
	s.mu.RUnlock()

	// Check cache validity
	if exists && time.Now().Before(cached.expiresAt) {
		return cached.details, nil
	}

	if metadataSvc == nil {
		return nil, fmt.Errorf("metadata service not available")
	}

	// Build query from external IDs or parse from seriesID
	query := models.SeriesDetailsQuery{
		TitleID: seriesID,
		Name:    seriesName,
	}

	// Parse IDs from seriesID first (more reliable than external IDs from history)
	// Format: "tmdb:tv:2190" or "tvdb:123456"
	parts := strings.Split(seriesID, ":")
	if len(parts) >= 2 {
		switch parts[0] {
		case "tvdb":
			if id, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
				query.TVDBID = id
			}
		case "tmdb":
			// For TMDB IDs like "tmdb:tv:2190", extract the TMDB ID
			if len(parts) >= 3 {
				if id, err := strconv.ParseInt(parts[2], 10, 64); err == nil {
					query.TMDBID = id
				}
			}
		}
	}

	// If still no ID, fallback to external IDs from watch history
	// Prefer TMDB over TVDB as TMDB is more reliable for finding correct language version
	if query.TVDBID == 0 && query.TMDBID == 0 && externalIDs != nil {
		// Try TMDB ID first
		if tmdbID, ok := externalIDs["tmdb"]; ok {
			if id, err := strconv.ParseInt(tmdbID, 10, 64); err == nil {
				query.TMDBID = id
			}
		}
		// Only use TVDB ID from history if no TMDB ID available
		// Note: TVDB IDs from history might be incorrect (e.g., foreign language dubs)
		if query.TMDBID == 0 {
			if tvdbID, ok := externalIDs["tvdb"]; ok {
				if id, err := strconv.ParseInt(tvdbID, 10, 64); err == nil {
					query.TVDBID = id
				}
			}
		}
	}

	// Fetch from metadata service (lite variant skips TMDB/MDBList enrichment for speed)
	details, err := metadataSvc.SeriesDetailsLite(ctx, query)
	if err != nil {
		return nil, err
	}

	// Cache the result
	s.mu.Lock()
	s.metadataCache[seriesID] = &cachedSeriesMetadata{
		details:   details,
		cachedAt:  time.Now(),
		expiresAt: time.Now().Add(s.metadataCacheTTL),
	}
	s.mu.Unlock()

	return details, nil
}

// getSeriesInfoWithCache retrieves lightweight series info (poster, backdrop, IDs) with caching.
func (s *Service) getSeriesInfoWithCache(ctx context.Context, seriesID, seriesName string, externalIDs map[string]string) (*models.Title, error) {
	s.mu.RLock()
	cached, exists := s.seriesInfoCache[seriesID]
	metadataSvc := s.metadataService
	s.mu.RUnlock()

	// Check cache validity
	if exists && time.Now().Before(cached.expiresAt) {
		return cached.info, nil
	}

	if metadataSvc == nil {
		return nil, fmt.Errorf("metadata service not available")
	}

	// Build query from external IDs or parse from seriesID
	query := models.SeriesDetailsQuery{
		TitleID: seriesID,
		Name:    seriesName,
	}

	// Parse IDs from seriesID first (more reliable than external IDs from history)
	// Format: "tmdb:tv:2190" or "tvdb:123456"
	parts := strings.Split(seriesID, ":")
	if len(parts) >= 2 {
		switch parts[0] {
		case "tvdb":
			if id, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
				query.TVDBID = id
			}
		case "tmdb":
			// For TMDB IDs like "tmdb:tv:2190", extract the TMDB ID
			if len(parts) >= 3 {
				if id, err := strconv.ParseInt(parts[2], 10, 64); err == nil {
					query.TMDBID = id
				}
			}
		}
	}

	// If still no ID, fallback to external IDs from watch history
	if query.TVDBID == 0 && query.TMDBID == 0 && externalIDs != nil {
		// Try TMDB ID first
		if tmdbID, ok := externalIDs["tmdb"]; ok {
			if id, err := strconv.ParseInt(tmdbID, 10, 64); err == nil {
				query.TMDBID = id
			}
		}
		// Only use TVDB ID from history if no TMDB ID available
		if query.TMDBID == 0 {
			if tvdbID, ok := externalIDs["tvdb"]; ok {
				if id, err := strconv.ParseInt(tvdbID, 10, 64); err == nil {
					query.TVDBID = id
				}
			}
		}
	}

	// Fetch lightweight info from metadata service (no episodes)
	info, err := metadataSvc.SeriesInfo(ctx, query)
	if err != nil {
		return nil, err
	}

	// Cache the result
	s.mu.Lock()
	s.seriesInfoCache[seriesID] = &cachedSeriesInfo{
		info:      info,
		cachedAt:  time.Now(),
		expiresAt: time.Now().Add(s.metadataCacheTTL),
	}
	s.mu.Unlock()

	return info, nil
}

// findNextUnwatchedEpisode finds the next unwatched episode after the most recently watched one.
func (s *Service) findNextUnwatchedEpisode(
	seriesDetails *models.SeriesDetails,
	lastWatched models.WatchHistoryItem,
	watchedEpisodes []models.WatchHistoryItem,
) *models.EpisodeReference {
	if seriesDetails == nil {
		return nil
	}

	// Build set of watched episodes for O(1) lookup
	watchedSet := make(map[string]bool)
	for _, ep := range watchedEpisodes {
		key := episodeKey(ep.SeasonNumber, ep.EpisodeNumber)
		watchedSet[key] = true
	}

	// Flatten all episodes in series order
	type orderedEpisode struct {
		season  int
		episode int
		details models.SeriesEpisode
	}
	var allEpisodes []orderedEpisode

	for _, season := range seriesDetails.Seasons {
		for _, ep := range season.Episodes {
			allEpisodes = append(allEpisodes, orderedEpisode{
				season:  ep.SeasonNumber,
				episode: ep.EpisodeNumber,
				details: ep,
			})
		}
	}

	// Sort by season, then episode number
	sort.Slice(allEpisodes, func(i, j int) bool {
		if allEpisodes[i].season != allEpisodes[j].season {
			return allEpisodes[i].season < allEpisodes[j].season
		}
		return allEpisodes[i].episode < allEpisodes[j].episode
	})

	latestReleasedSeason, latestReleasedEpisode := latestReleasedEpisodeOrder(seriesDetails)

	// Find the last watched episode in the list, then scan forward for next unwatched.
	// Track the first unreleased episode as a fallback so the frontend can show
	// "coming soon" instead of hiding the series entirely.
	foundLast := false
	var firstUnreleased *models.EpisodeReference
	for _, ep := range allEpisodes {
		if ep.season == lastWatched.SeasonNumber && ep.episode == lastWatched.EpisodeNumber {
			foundLast = true
			continue
		}

		if foundLast {
			key := episodeKey(ep.season, ep.episode)
			if watchedSet[key] {
				continue
			}

			// Check if this episode is unreleased using precise air time
			isUnreleased := false
			if ep.details.AiredDate != "" {
				airDateTime := calendar.ParseAirDateTime(ep.details.AiredDate, seriesDetails.Title.AirsTime, seriesDetails.Title.AirsTimezone)
				if !airDateTime.IsZero() && airDateTime.After(time.Now()) {
					isUnreleased = true
				}
			} else if latestReleasedSeason > 0 || latestReleasedEpisode > 0 {
				// TVDB often creates placeholder episodes for future seasons before
				// they have an air date. If an undated episode is ordered after the
				// latest known released episode, treat it as unreleased.
				if compareEpisodeOrder(ep.season, ep.episode, latestReleasedSeason, latestReleasedEpisode) > 0 {
					isUnreleased = true
				}
			}

			ref := &models.EpisodeReference{
				SeasonNumber:   ep.details.SeasonNumber,
				EpisodeNumber:  ep.details.EpisodeNumber,
				EpisodeID:      ep.details.ID,
				Title:          ep.details.Name,
				Overview:       ep.details.Overview,
				RuntimeMinutes: ep.details.Runtime,
				AirDate:        ep.details.AiredDate,
				Image:          ep.details.Image,
			}
			if ep.details.AiredDate != "" {
				utc := calendar.ParseAirDateTime(ep.details.AiredDate, seriesDetails.Title.AirsTime, seriesDetails.Title.AirsTimezone)
				if !utc.IsZero() {
					ref.AirDateTimeUTC = utc.Format(time.RFC3339)
				}
			}

			if isUnreleased {
				// Remember the first unreleased episode as fallback
				if firstUnreleased == nil {
					firstUnreleased = ref
				}
				continue
			}

			// Found a released, unwatched episode
			return ref
		}
	}

	// No released unwatched episode found. Only surface an upcoming episode when
	// it is close enough to be considered "coming soon"; otherwise omit the item
	// from continue watching until a nearer follow-up exists.
	if releaseTime, ok := continueWatchingComingSoonTime(firstUnreleased); ok {
		firstUnreleased.AirDateTimeUTC = releaseTime.Format(time.RFC3339)
		return firstUnreleased
	}
	return nil
}

// convertToEpisodeRef converts a WatchHistoryItem to an EpisodeReference.
func (s *Service) convertToEpisodeRef(item models.WatchHistoryItem) models.EpisodeReference {
	tvdbID := ""
	if item.ExternalIDs != nil {
		if id, ok := item.ExternalIDs["tvdb"]; ok {
			tvdbID = id
		}
	}

	return models.EpisodeReference{
		SeasonNumber:  item.SeasonNumber,
		EpisodeNumber: item.EpisodeNumber,
		Title:         item.Name,
		WatchedAt:     item.WatchedAt,
		TvdbID:        tvdbID,
	}
}

// enrichEpisodeFromMetadata adds metadata details to an episode reference.
func (s *Service) enrichEpisodeFromMetadata(episodeRef *models.EpisodeReference, seriesDetails *models.SeriesDetails) {
	if episodeRef == nil || seriesDetails == nil {
		return
	}

	// Find the matching episode in metadata
	for _, season := range seriesDetails.Seasons {
		if season.Number == episodeRef.SeasonNumber {
			for _, episode := range season.Episodes {
				if episode.EpisodeNumber == episodeRef.EpisodeNumber {
					// Enrich with metadata
					if episodeRef.Title == "" {
						episodeRef.Title = episode.Name
					}
					episodeRef.Overview = episode.Overview
					episodeRef.AirDate = episode.AiredDate
					episodeRef.RuntimeMinutes = episode.Runtime
					if episodeRef.Image == nil && episode.Image != nil {
						episodeRef.Image = episode.Image
					}
					if episode.TVDBID > 0 {
						episodeRef.TvdbID = fmt.Sprintf("%d", episode.TVDBID)
					}
					if episodeRef.EpisodeID == "" && episode.ID != "" {
						episodeRef.EpisodeID = episode.ID
					}
					return
				}
			}
		}
	}
}

func (s *Service) ensureUserLocked(userID string) map[string]models.SeriesWatchState {
	perUser, ok := s.states[userID]
	if !ok {
		perUser = make(map[string]models.SeriesWatchState)
		s.states[userID] = perUser
	}
	return perUser
}

func (s *Service) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.states = make(map[string]map[string]models.SeriesWatchState)
		return nil
	}
	if err != nil {
		return fmt.Errorf("open history: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read history: %w", err)
	}
	if len(data) == 0 {
		s.states = make(map[string]map[string]models.SeriesWatchState)
		return nil
	}

	var decoded map[string]map[string]models.SeriesWatchState
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("decode history: %w", err)
	}

	s.states = make(map[string]map[string]models.SeriesWatchState, len(decoded))
	for userID, perUser := range decoded {
		cleanedUserID := strings.TrimSpace(userID)
		if cleanedUserID == "" {
			continue
		}
		perSeries := make(map[string]models.SeriesWatchState, len(perUser))
		for seriesID, state := range perUser {
			state = normaliseState(state)
			perSeries[seriesID] = state
		}
		s.states[cleanedUserID] = perSeries
	}

	return nil
}

func (s *Service) saveLocked() error {
	data, err := json.MarshalIndent(s.states, "", "  ")
	if err != nil {
		return fmt.Errorf("encode history: %w", err)
	}

	if err := os.WriteFile(s.path, data, 0o644); err != nil {
		return fmt.Errorf("write history: %w", err)
	}

	return nil
}

func episodeKey(season, episode int) string {
	return fmt.Sprintf("s%02de%02d", season, episode)
}

func compareEpisodeOrder(seasonA, episodeA, seasonB, episodeB int) int {
	if seasonA != seasonB {
		return seasonA - seasonB
	}
	return episodeA - episodeB
}

// buildCanonicalSeriesIDMap scans watch history and progress items to find series
// that share the same IMDB or TVDB external IDs but have different seriesID values
// (e.g., "tvdb:series:353546" from the player vs "tmdb:tv:82728" from Trakt sync).
// Returns a map from each seriesID to its canonical (preferred) ID.
func buildCanonicalSeriesIDMap(items []models.WatchHistoryItem, progressItems []models.PlaybackProgress) map[string]string {
	// Map external ID -> list of seriesIDs that reference it
	type seriesEntry struct {
		seriesID string
		hasMore  bool // has richer external IDs
	}
	imdbToSeries := make(map[string][]string)
	tvdbToSeries := make(map[string][]string)
	tmdbToSeries := make(map[string][]string)
	seriesExternalIDCount := make(map[string]int) // track richness

	recordIDs := func(seriesID string, extIDs map[string]string) {
		if seriesID == "" || extIDs == nil {
			return
		}
		count := 0
		if id, ok := extIDs["imdb"]; ok && id != "" {
			imdbToSeries[id] = append(imdbToSeries[id], seriesID)
			count++
		}
		if id, ok := extIDs["tvdb"]; ok && id != "" {
			tvdbToSeries[id] = append(tvdbToSeries[id], seriesID)
			count++
		}
		if id, ok := extIDs["tmdb"]; ok && id != "" {
			tmdbToSeries[id] = append(tmdbToSeries[id], seriesID)
			count++
		}
		if existing, ok := seriesExternalIDCount[seriesID]; !ok || count > existing {
			seriesExternalIDCount[seriesID] = count
		}
	}

	for _, item := range items {
		if item.MediaType == "episode" && item.SeriesID != "" {
			recordIDs(item.SeriesID, item.ExternalIDs)
		}
	}
	for _, prog := range progressItems {
		if isSeriesLevelPlaybackMarker(prog) {
			continue
		}
		if (prog.MediaType == "episode" || prog.SeasonNumber > 0) && prog.SeriesID != "" {
			recordIDs(prog.SeriesID, prog.ExternalIDs)
		}
	}

	// Build union-find: group seriesIDs that share an external ID
	// Pick the one with the most external IDs as canonical (richer metadata)
	canonical := make(map[string]string)

	mergeGroup := func(group []string) {
		if len(group) < 2 {
			return
		}
		// Deduplicate
		seen := make(map[string]bool)
		var unique []string
		for _, id := range group {
			if !seen[id] {
				seen[id] = true
				unique = append(unique, id)
			}
		}
		if len(unique) < 2 {
			return
		}
		// Pick canonical: prefer the one with the most external IDs
		best := unique[0]
		for _, id := range unique[1:] {
			if seriesExternalIDCount[id] > seriesExternalIDCount[best] {
				best = id
			}
		}
		for _, id := range unique {
			canonical[id] = best
		}
	}

	for _, group := range imdbToSeries {
		mergeGroup(group)
	}
	for _, group := range tvdbToSeries {
		mergeGroup(group)
	}
	for _, group := range tmdbToSeries {
		mergeGroup(group)
	}

	// Ensure every seriesID maps to something (itself if not in a group)
	addSelf := func(seriesID string) {
		if _, ok := canonical[seriesID]; !ok {
			canonical[seriesID] = seriesID
		}
	}
	for _, item := range items {
		if item.SeriesID != "" {
			addSelf(item.SeriesID)
		}
	}
	for _, prog := range progressItems {
		if prog.SeriesID != "" {
			addSelf(prog.SeriesID)
		}
	}
	return canonical
}

// resolveCanonicalID looks up a seriesID in the canonical map, returning
// the canonical ID or the original ID if not found.
func resolveCanonicalID(canonical map[string]string, seriesID string) string {
	if resolved, ok := canonical[seriesID]; ok {
		return resolved
	}
	return seriesID
}

// countTotalEpisodes counts the total number of released episodes in a series,
// excluding specials (season 0). Only counts episodes that have aired.
// enrichEpisodeFromMetadata fills in missing episode fields (title, overview,
// runtime, air date) from series metadata.
func enrichEpisodeFromMetadata(ref *models.EpisodeReference, details *models.SeriesDetails) {
	if ref == nil || details == nil {
		return
	}
	for _, season := range details.Seasons {
		for _, ep := range season.Episodes {
			if ep.SeasonNumber == ref.SeasonNumber && ep.EpisodeNumber == ref.EpisodeNumber {
				if ref.Title == "" {
					ref.Title = ep.Name
				}
				if ref.Overview == "" {
					ref.Overview = ep.Overview
				}
				if ref.RuntimeMinutes == 0 && ep.Runtime > 0 {
					ref.RuntimeMinutes = ep.Runtime
				}
				if ref.AirDate == "" {
					ref.AirDate = ep.AiredDate
				}
				if ref.Image == nil && ep.Image != nil {
					ref.Image = ep.Image
				}
				if ref.EpisodeID == "" {
					ref.EpisodeID = ep.ID
				}
				return
			}
		}
	}
}

func countTotalEpisodes(seriesDetails *models.SeriesDetails) int {
	if seriesDetails == nil {
		return 0
	}
	total := 0
	now := time.Now()
	latestReleasedSeason, latestReleasedEpisode := latestReleasedEpisodeOrder(seriesDetails)
	for _, season := range seriesDetails.Seasons {
		// Skip specials (season 0)
		if season.Number == 0 {
			continue
		}
		for _, ep := range season.Episodes {
			// Only count episodes that have aired
			if ep.AiredDate != "" {
				if airDate, err := time.Parse("2006-01-02", ep.AiredDate); err == nil {
					if airDate.Before(now) || airDate.Equal(now) {
						total++
					}
				} else {
					// If we can't parse the date, assume it's released
					total++
				}
			} else if latestReleasedSeason == 0 && latestReleasedEpisode == 0 ||
				compareEpisodeOrder(ep.SeasonNumber, ep.EpisodeNumber, latestReleasedSeason, latestReleasedEpisode) <= 0 {
				// Older catalog entries occasionally lack dates; keep counting those.
				// But do not count undated placeholder episodes that appear after the
				// latest known released episode.
				total++
			}
		}
	}
	return total
}

func latestReleasedEpisodeOrder(seriesDetails *models.SeriesDetails) (int, int) {
	if seriesDetails == nil {
		return 0, 0
	}

	now := time.Now()
	latestSeason := 0
	latestEpisode := 0
	for _, season := range seriesDetails.Seasons {
		if season.Number == 0 {
			continue
		}
		for _, ep := range season.Episodes {
			if strings.TrimSpace(ep.AiredDate) == "" {
				continue
			}
			airDate, err := time.Parse("2006-01-02", ep.AiredDate)
			if err != nil {
				continue
			}
			if airDate.After(now) {
				continue
			}
			if compareEpisodeOrder(ep.SeasonNumber, ep.EpisodeNumber, latestSeason, latestEpisode) > 0 {
				latestSeason = ep.SeasonNumber
				latestEpisode = ep.EpisodeNumber
			}
		}
	}
	return latestSeason, latestEpisode
}

// countWatchedEpisodes counts how many non-special episodes have been watched.
func countWatchedEpisodes(watchedEpisodes map[string]models.EpisodeReference) int {
	count := 0
	for _, ep := range watchedEpisodes {
		// Exclude specials (season 0)
		if ep.SeasonNumber > 0 {
			count++
		}
	}
	return count
}

func normaliseEpisode(ref models.EpisodeReference) models.EpisodeReference {
	if ref.SeasonNumber < 0 {
		ref.SeasonNumber = 0
	}
	if ref.EpisodeNumber < 0 {
		ref.EpisodeNumber = 0
	}
	ref.Title = strings.TrimSpace(ref.Title)
	ref.Overview = strings.TrimSpace(ref.Overview)
	ref.EpisodeID = strings.TrimSpace(ref.EpisodeID)
	ref.TvdbID = strings.TrimSpace(ref.TvdbID)
	if ref.WatchedAt.IsZero() {
		ref.WatchedAt = time.Now().UTC()
	} else {
		ref.WatchedAt = ref.WatchedAt.UTC()
	}
	ref.AirDate = strings.TrimSpace(ref.AirDate)
	if ref.RuntimeMinutes < 0 {
		ref.RuntimeMinutes = 0
	}
	return ref
}

func normaliseState(state models.SeriesWatchState) models.SeriesWatchState {
	state.SeriesID = strings.TrimSpace(state.SeriesID)
	state.SeriesTitle = strings.TrimSpace(state.SeriesTitle)
	state.PosterURL = strings.TrimSpace(state.PosterURL)
	state.BackdropURL = strings.TrimSpace(state.BackdropURL)
	if state.Year < 0 {
		state.Year = 0
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	} else {
		state.UpdatedAt = state.UpdatedAt.UTC()
	}
	state.LastWatched = normaliseEpisode(state.LastWatched)
	if state.NextEpisode != nil {
		next := normaliseEpisode(*state.NextEpisode)
		state.NextEpisode = &next
	}
	if state.WatchedEpisodes == nil {
		state.WatchedEpisodes = make(map[string]models.EpisodeReference)
	} else {
		cleaned := make(map[string]models.EpisodeReference, len(state.WatchedEpisodes))
		for key, episode := range state.WatchedEpisodes {
			cleaned[strings.TrimSpace(key)] = normaliseEpisode(episode)
		}
		state.WatchedEpisodes = cleaned
	}
	return state
}

// Watch History Methods (unified manual watch tracking for all media)

// ListWatchHistory returns all watched items for a user.
func (s *Service) ListWatchHistory(userID string) ([]models.WatchHistoryItem, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUserIDRequired
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]models.WatchHistoryItem, 0)
	if perUser, ok := s.watchHistory[userID]; ok {
		items = make([]models.WatchHistoryItem, 0, len(perUser))
		for _, item := range perUser {
			items = append(items, item)
		}
	}

	// Sort by most recently watched
	sort.Slice(items, func(i, j int) bool {
		if items[i].WatchedAt.Equal(items[j].WatchedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].WatchedAt.After(items[j].WatchedAt)
	})

	return items, nil
}

// WatchHistoryPage represents a paginated response of watch history items.
type WatchHistoryPage struct {
	Items      []models.WatchHistoryItem `json:"items"`
	Total      int                       `json:"total"`
	Page       int                       `json:"page"`
	PageSize   int                       `json:"pageSize"`
	TotalPages int                       `json:"totalPages"`
}

// ListWatchHistoryPaginated returns paginated watched items for a user.
// Supports optional filtering by media type ("movie", "episode", or "" for all).
func (s *Service) ListWatchHistoryPaginated(userID string, page, pageSize int, mediaTypeFilter string) (*WatchHistoryPage, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUserIDRequired
	}

	// Default/validate pagination params
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Collect and filter items
	items := make([]models.WatchHistoryItem, 0)
	if perUser, ok := s.watchHistory[userID]; ok {
		for _, item := range perUser {
			// Apply media type filter if specified
			if mediaTypeFilter != "" && item.MediaType != mediaTypeFilter {
				continue
			}
			items = append(items, item)
		}
	}

	// Sort by most recently watched
	sort.Slice(items, func(i, j int) bool {
		if items[i].WatchedAt.Equal(items[j].WatchedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].WatchedAt.After(items[j].WatchedAt)
	})

	total := len(items)
	totalPages := (total + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}

	// Calculate slice bounds
	start := (page - 1) * pageSize
	end := start + pageSize

	if start >= total {
		// Page is beyond available data
		return &WatchHistoryPage{
			Items:      []models.WatchHistoryItem{},
			Total:      total,
			Page:       page,
			PageSize:   pageSize,
			TotalPages: totalPages,
		}, nil
	}

	if end > total {
		end = total
	}

	return &WatchHistoryPage{
		Items:      items[start:end],
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
	}, nil
}

// GetWatchHistoryItem returns a specific watch history item.
func (s *Service) GetWatchHistoryItem(userID, mediaType, itemID string) (*models.WatchHistoryItem, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUserIDRequired
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	key := makeWatchKey(mediaType, itemID)
	if perUser, ok := s.watchHistory[userID]; ok {
		if item, ok := perUser[key]; ok {
			return &item, nil
		}
	}

	return nil, nil
}

// ToggleWatched toggles the watched status for an item (movie, series, or episode).
func (s *Service) ToggleWatched(userID string, update models.WatchHistoryUpdate) (models.WatchHistoryItem, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return models.WatchHistoryItem{}, ErrUserIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	perUser := s.ensureWatchHistoryUserLocked(userID)

	// Normalize itemID to lowercase for consistent key matching
	normalizedItemID := strings.ToLower(update.ItemID)
	key := makeWatchKey(update.MediaType, normalizedItemID)
	item, exists := perUser[key]

	now := time.Now().UTC()
	if !exists {
		// Create new item marked as watched
		item = models.WatchHistoryItem{
			ID:        key,
			MediaType: strings.ToLower(update.MediaType),
			ItemID:    normalizedItemID,
			Watched:   true,
			WatchedAt: now,
			UpdatedAt: now,
		}
	} else {
		// Toggle existing item
		item.Watched = !item.Watched
		if item.Watched {
			item.WatchedAt = now
		}
		item.UpdatedAt = now
	}

	// Update metadata if provided
	if update.Name != "" {
		item.Name = update.Name
	}
	if update.Year > 0 {
		item.Year = update.Year
	}
	if update.ExternalIDs != nil {
		item.ExternalIDs = update.ExternalIDs
	}

	// Episode-specific fields
	if update.SeasonNumber > 0 {
		item.SeasonNumber = update.SeasonNumber
	}
	if update.EpisodeNumber > 0 {
		item.EpisodeNumber = update.EpisodeNumber
	}
	if update.SeriesID != "" {
		item.SeriesID = update.SeriesID
	}
	if update.SeriesName != "" {
		item.SeriesName = update.SeriesName
	}

	perUser[key] = item

	if err := s.saveWatchHistoryLocked(); err != nil {
		return models.WatchHistoryItem{}, err
	}

	// Clear playback progress when toggling watched status (both marking as watched and unwatched)
	progressCleared := s.clearPlaybackProgressEntryLocked(userID, item.MediaType, item.ItemID)
	if item.MediaType == "movie" {
		if s.clearMovieProgressByExternalIDMatchLocked(userID, item.ExternalIDs) {
			progressCleared = true
		}
	}

	// If marking an episode as watched, also clear progress for earlier episodes
	if item.Watched && item.MediaType == "episode" && item.SeriesID != "" && item.SeasonNumber > 0 && item.EpisodeNumber > 0 {
		if s.clearEarlierEpisodesProgressLocked(userID, item.SeriesID, item.SeasonNumber, item.EpisodeNumber) {
			progressCleared = true
		}
		// Also clear progress by external ID matching (handles cross-ID-format mismatches)
		if s.clearProgressByExternalIDMatchLocked(userID, item.SeasonNumber, item.EpisodeNumber, item.ExternalIDs) {
			progressCleared = true
		}
	}

	if progressCleared {
		if err := s.savePlaybackProgressLocked(); err != nil {
			return models.WatchHistoryItem{}, err
		}
	}

	// Invalidate continue watching cache for this user
	delete(s.continueWatchingCache, userID)

	// Get scrobbler reference while holding lock (safe since we have write lock)
	scrobbler := s.traktScrobbler

	// Scrobble to Trakt if now marked as watched
	// Note: doScrobble is safe to call while holding lock since it spawns goroutines
	if item.Watched {
		s.doScrobble(scrobbler, userID, item)
	}

	return item, nil
}

// UpdateWatchHistory updates or creates a watch history item.
func (s *Service) UpdateWatchHistory(userID string, update models.WatchHistoryUpdate) (models.WatchHistoryItem, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return models.WatchHistoryItem{}, ErrUserIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	perUser := s.ensureWatchHistoryUserLocked(userID)

	// Normalize itemID to lowercase for consistent key matching
	normalizedItemID := strings.ToLower(update.ItemID)
	key := makeWatchKey(update.MediaType, normalizedItemID)
	item, exists := perUser[key]

	now := time.Now().UTC()
	if !exists {
		item = models.WatchHistoryItem{
			ID:        key,
			MediaType: strings.ToLower(update.MediaType),
			ItemID:    normalizedItemID,
			Watched:   false,
		}
	}

	progressCleared := false
	wasAlreadyWatched := item.Watched

	// Update fields
	if update.Name != "" {
		item.Name = update.Name
	}
	if update.Year > 0 {
		item.Year = update.Year
	}
	if update.Watched != nil {
		stateUpdatedAt := now
		if !update.WatchedAt.IsZero() {
			stateUpdatedAt = update.WatchedAt.UTC()
		}
		item.Watched = *update.Watched
		if *update.Watched {
			// Only set WatchedAt on the unwatched→watched transition, or when
			// an explicit timestamp is provided (e.g. Trakt import).
			// Repeated progress updates for an already-watched item must NOT
			// bump the timestamp forward, as that desynchronises the local
			// WatchedAt from the Trakt scrobble time and causes the periodic
			// Trakt sync to re-export the item as a duplicate.
			if !update.WatchedAt.IsZero() {
				item.WatchedAt = update.WatchedAt.UTC()
			} else if !wasAlreadyWatched {
				item.WatchedAt = now
			}
		}
		item.UpdatedAt = stateUpdatedAt
		// Clear playback progress when watched status changes (both marking as watched and unwatched)
		progressCleared = s.clearPlaybackProgressEntryLocked(userID, update.MediaType, update.ItemID)
		// For episodes/movies, also clear any matching progress stored under a different ID format.
		if update.MediaType == "episode" && update.SeasonNumber > 0 && update.EpisodeNumber > 0 {
			if s.clearProgressByExternalIDMatchLocked(userID, update.SeasonNumber, update.EpisodeNumber, update.ExternalIDs) {
				progressCleared = true
			}
		} else if update.MediaType == "movie" {
			if s.clearMovieProgressByExternalIDMatchLocked(userID, update.ExternalIDs) {
				progressCleared = true
			}
		}
	}
	if update.ExternalIDs != nil {
		item.ExternalIDs = update.ExternalIDs
	}

	// Episode-specific fields
	if update.SeasonNumber > 0 {
		item.SeasonNumber = update.SeasonNumber
	}
	if update.EpisodeNumber > 0 {
		item.EpisodeNumber = update.EpisodeNumber
	}
	if update.SeriesID != "" {
		item.SeriesID = update.SeriesID
	}
	if update.SeriesName != "" {
		item.SeriesName = update.SeriesName
	}

	perUser[key] = item

	// If marking an episode as watched, also clear progress for earlier episodes
	if update.Watched != nil && *update.Watched && update.MediaType == "episode" && update.SeriesID != "" && update.SeasonNumber > 0 && update.EpisodeNumber > 0 {
		if s.clearEarlierEpisodesProgressLocked(userID, update.SeriesID, update.SeasonNumber, update.EpisodeNumber) {
			progressCleared = true
		}
		// Also clear progress by external ID matching (handles cross-ID-format mismatches,
		// e.g. Trakt using tvdb:series:X while player uses tmdb:tv:Y for the same show)
		if s.clearProgressByExternalIDMatchLocked(userID, update.SeasonNumber, update.EpisodeNumber, update.ExternalIDs) {
			progressCleared = true
		}
	}

	if err := s.saveWatchHistoryLocked(); err != nil {
		return models.WatchHistoryItem{}, err
	}

	if progressCleared {
		if err := s.savePlaybackProgressLocked(); err != nil {
			return models.WatchHistoryItem{}, err
		}
	}

	// Invalidate continue watching cache for this user
	delete(s.continueWatchingCache, userID)

	// Get scrobbler reference while holding lock (safe since we have write lock)
	scrobbler := s.traktScrobbler

	// Only scrobble if the watched state actually changed from unwatched to watched.
	// This prevents duplicate Trakt history entries when an already-watched item
	// is updated again (e.g. metadata refresh, redundant API calls).
	if update.Watched != nil && *update.Watched && !wasAlreadyWatched {
		s.doScrobble(scrobbler, userID, item)
	}

	return item, nil
}

// IsWatched checks if an item is marked as watched.
func (s *Service) IsWatched(userID, mediaType, itemID string) (bool, error) {
	item, err := s.GetWatchHistoryItem(userID, mediaType, itemID)
	if err != nil {
		return false, err
	}
	if item == nil {
		return false, nil
	}
	return item.Watched, nil
}

// BulkUpdateWatchHistory marks multiple episodes as watched/unwatched in a single operation.
func (s *Service) BulkUpdateWatchHistory(userID string, updates []models.WatchHistoryUpdate) ([]models.WatchHistoryItem, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUserIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	perUser := s.ensureWatchHistoryUserLocked(userID)
	results := make([]models.WatchHistoryItem, 0, len(updates))
	wasAlreadyWatched := make([]bool, 0, len(updates))
	now := time.Now().UTC()
	progressCleared := false

	for _, update := range updates {
		// Normalize itemID to lowercase for consistent key matching
		normalizedItemID := strings.ToLower(update.ItemID)
		key := makeWatchKey(update.MediaType, normalizedItemID)
		item, exists := perUser[key]

		if !exists {
			item = models.WatchHistoryItem{
				ID:        key,
				MediaType: strings.ToLower(update.MediaType),
				ItemID:    normalizedItemID,
				Watched:   false,
			}
		}

		wasAlreadyWatched = append(wasAlreadyWatched, item.Watched)

		// Update fields
		if update.Name != "" {
			item.Name = update.Name
		}
		if update.Year > 0 {
			item.Year = update.Year
		}
		if update.Watched != nil {
			stateUpdatedAt := now
			if !update.WatchedAt.IsZero() {
				stateUpdatedAt = update.WatchedAt.UTC()
			}
			item.Watched = *update.Watched
			if *update.Watched {
				// Use provided timestamp if set, otherwise use now
				if !update.WatchedAt.IsZero() {
					item.WatchedAt = update.WatchedAt.UTC()
				} else {
					item.WatchedAt = now
				}
			}
			item.UpdatedAt = stateUpdatedAt
			// Clear playback progress when watched status changes (both marking as watched and unwatched)
			if s.clearPlaybackProgressEntryLocked(userID, update.MediaType, update.ItemID) {
				progressCleared = true
			}
			// For episodes/movies, also clear any matching progress stored under a different ID format.
			if update.MediaType == "episode" && update.SeasonNumber > 0 && update.EpisodeNumber > 0 {
				if s.clearProgressByExternalIDMatchLocked(userID, update.SeasonNumber, update.EpisodeNumber, update.ExternalIDs) {
					progressCleared = true
				}
			} else if update.MediaType == "movie" {
				if s.clearMovieProgressByExternalIDMatchLocked(userID, update.ExternalIDs) {
					progressCleared = true
				}
			}
		}
		if update.ExternalIDs != nil {
			item.ExternalIDs = update.ExternalIDs
		}

		// Episode-specific fields
		if update.SeasonNumber > 0 {
			item.SeasonNumber = update.SeasonNumber
		}
		if update.EpisodeNumber > 0 {
			item.EpisodeNumber = update.EpisodeNumber
		}
		if update.SeriesID != "" {
			item.SeriesID = update.SeriesID
		}
		if update.SeriesName != "" {
			item.SeriesName = update.SeriesName
		}

		perUser[key] = item

		// If marking an episode as watched, also clear progress for earlier episodes
		if update.Watched != nil && *update.Watched && update.MediaType == "episode" && update.SeriesID != "" && update.SeasonNumber > 0 && update.EpisodeNumber > 0 {
			if s.clearEarlierEpisodesProgressLocked(userID, update.SeriesID, update.SeasonNumber, update.EpisodeNumber) {
				progressCleared = true
			}
			// Also clear progress by external ID matching (handles cross-ID-format mismatches)
			if s.clearProgressByExternalIDMatchLocked(userID, update.SeasonNumber, update.EpisodeNumber, update.ExternalIDs) {
				progressCleared = true
			}
		}

		results = append(results, item)
	}

	if err := s.saveWatchHistoryLocked(); err != nil {
		return nil, err
	}

	if progressCleared {
		if err := s.savePlaybackProgressLocked(); err != nil {
			return nil, err
		}
	}

	// Invalidate continue watching cache for this user
	delete(s.continueWatchingCache, userID)

	// Get scrobbler reference while holding lock (safe since we have write lock)
	scrobbler := s.traktScrobbler

	// Only scrobble items whose watched state actually changed from unwatched to watched.
	// This prevents duplicate Trakt history entries on redundant bulk updates.
	for i, update := range updates {
		if update.Watched != nil && *update.Watched && !wasAlreadyWatched[i] {
			s.doScrobble(scrobbler, userID, results[i])
		}
	}

	return results, nil
}

// ImportWatchHistory writes items to watch history without scrobbling back to Trakt.
// This is used by the scheduled Trakt history sync to avoid creating duplicates.
// "Most recent wins" — won't overwrite a local item that has a newer WatchedAt.
// Returns the count of items that were actually newly recorded.
func (s *Service) ImportWatchHistory(userID string, updates []models.WatchHistoryUpdate) (int, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return 0, ErrUserIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	perUser := s.ensureWatchHistoryUserLocked(userID)
	now := time.Now().UTC()
	progressCleared := false
	imported := 0

	// Build cross-provider dedup index: "s01e01:imdb:tt123" → existing watch key
	// This lets us find existing entries for the same episode imported under a different ID format.
	type episodeIndex struct {
		watchKey string
	}
	crossProviderIndex := make(map[string]episodeIndex)
	for key, item := range perUser {
		if item.MediaType != "episode" || item.SeasonNumber == 0 || item.EpisodeNumber == 0 {
			continue
		}
		epKey := episodeKey(item.SeasonNumber, item.EpisodeNumber)
		for idType, idValue := range item.ExternalIDs {
			if idValue == "" || idType == "titleId" {
				continue
			}
			indexKey := epKey + ":" + idType + ":" + idValue
			crossProviderIndex[indexKey] = episodeIndex{watchKey: key}
		}
	}

	for _, update := range updates {
		normalizedItemID := strings.ToLower(update.ItemID)
		key := makeWatchKey(update.MediaType, normalizedItemID)
		existing, exists := perUser[key]

		// crossProviderRekeyed is set when dedup finds the item under a different key and
		// deletes the old entry. In that case the SKIP path must re-save under the new key
		// to prevent the item from disappearing and triggering a re-scrobble loop.
		crossProviderRekeyed := false

		// Cross-provider dedup: if no direct match, look for an existing entry
		// with matching external IDs and same season/episode numbers.
		if !exists && update.SeasonNumber > 0 && update.EpisodeNumber > 0 && len(update.ExternalIDs) > 0 {
			epKey := episodeKey(update.SeasonNumber, update.EpisodeNumber)
			for idType, idValue := range update.ExternalIDs {
				if idValue == "" || idType == "titleId" {
					continue
				}
				indexKey := epKey + ":" + idType + ":" + idValue
				if idx, found := crossProviderIndex[indexKey]; found && idx.watchKey != key {
					// Found a match under a different key — merge into existing entry
					existing = perUser[idx.watchKey]
					exists = true
					crossProviderRekeyed = true
					// Remove old key, will re-key under new canonical key below
					delete(perUser, idx.watchKey)
					// Clean up old index entries
					for oldIDType, oldIDValue := range existing.ExternalIDs {
						if oldIDValue == "" || oldIDType == "titleId" {
							continue
						}
						oldIndexKey := epKey + ":" + oldIDType + ":" + oldIDValue
						delete(crossProviderIndex, oldIndexKey)
					}
					log.Printf("[history] import: DEDUP cross-provider %s %q (old key %s -> new key %s)",
						update.MediaType, update.Name, idx.watchKey, key)
					break
				}
			}
		}

		// For movies: cross-provider dedup by external ID matching
		if !exists && update.MediaType == "movie" && len(update.ExternalIDs) > 0 {
			for existingKey, existingItem := range perUser {
				if existingItem.MediaType != "movie" || existingKey == key {
					continue
				}
				if hasMatchingExternalID(update.ExternalIDs, existingItem.ExternalIDs) {
					existing = existingItem
					exists = true
					crossProviderRekeyed = true
					delete(perUser, existingKey)
					log.Printf("[history] import: DEDUP cross-provider movie %q (old key %s -> new key %s)",
						update.Name, existingKey, key)
					break
				}
			}
		}

		// "Most recent wins" for watched items.
		// Also preserve a manual local unwatch when the imported event is not newer
		// than the last known watched timestamp on the local item.
		if exists && existing.Watched {
			incomingStateTime := update.WatchedAt
			if incomingStateTime.IsZero() {
				incomingStateTime = now
			}
			if !existing.UpdatedAt.IsZero() && (existing.UpdatedAt.After(incomingStateTime) || existing.UpdatedAt.Equal(incomingStateTime)) {
				// Already recorded, but still clean up any stale playback progress
				// that may exist under a different ID format.
				if update.MediaType == "episode" && update.SeriesID != "" && update.SeasonNumber > 0 && update.EpisodeNumber > 0 {
					if s.clearEarlierEpisodesProgressLocked(userID, update.SeriesID, update.SeasonNumber, update.EpisodeNumber) {
						progressCleared = true
					}
				}
				log.Printf("[history] import: SKIP (local newer) %s %q watchedAt=%s (trakt=%s)",
					update.MediaType, update.Name, existing.UpdatedAt.Format(time.RFC3339), incomingStateTime.Format(time.RFC3339))
				// If cross-provider dedup deleted the old key, re-save under the new canonical
				// key so the item isn't lost, which would cause a re-scrobble loop.
				if crossProviderRekeyed {
					existing.ID = key
					existing.ItemID = normalizedItemID
					perUser[key] = existing
					imported++
				}
				continue
			}
			log.Printf("[history] import: UPDATE (trakt newer) %s %q localWatchedAt=%s -> traktWatchedAt=%s seriesID=%s",
				update.MediaType, update.Name, existing.UpdatedAt.Format(time.RFC3339), incomingStateTime.Format(time.RFC3339), update.SeriesID)
		} else if exists && !existing.Watched {
			incomingStateTime := update.WatchedAt
			if incomingStateTime.IsZero() {
				incomingStateTime = now
			}
			if !existing.UpdatedAt.IsZero() && (existing.UpdatedAt.After(incomingStateTime) || existing.UpdatedAt.Equal(incomingStateTime)) {
				log.Printf("[history] import: SKIP (manual unwatch newer/equal) %s %q localWatchedAt=%s importedWatchedAt=%s seriesID=%s",
					update.MediaType, update.Name, existing.UpdatedAt.Format(time.RFC3339), incomingStateTime.Format(time.RFC3339), update.SeriesID)
				// If cross-provider dedup deleted the old key, re-save under the new canonical key.
				if crossProviderRekeyed {
					existing.ID = key
					existing.ItemID = normalizedItemID
					perUser[key] = existing
					imported++
				}
				continue
			}
			log.Printf("[history] import: RESTORE (external newer than manual unwatch baseline) %s %q localWatchedAt=%s -> importedWatchedAt=%s seriesID=%s",
				update.MediaType, update.Name, existing.UpdatedAt.Format(time.RFC3339), incomingStateTime.Format(time.RFC3339), update.SeriesID)
		} else if !exists {
			log.Printf("[history] import: NEW %s %q watchedAt=%s seriesID=%s",
				update.MediaType, update.Name, update.WatchedAt.Format(time.RFC3339), update.SeriesID)
		}

		if update.Watched != nil && *update.Watched && s.hasHighInProgressPlaybackLocked(userID, update) {
			log.Printf("[history] import: SKIP (preserve local in-progress) %s %q watchedAt=%s seriesID=%s",
				update.MediaType, update.Name, update.WatchedAt.Format(time.RFC3339), update.SeriesID)
			if crossProviderRekeyed {
				existing.ID = key
				existing.ItemID = normalizedItemID
				perUser[key] = existing
				imported++
			}
			continue
		}

		item := existing
		if !exists {
			item = models.WatchHistoryItem{
				ID:        key,
				MediaType: strings.ToLower(update.MediaType),
				ItemID:    normalizedItemID,
				Watched:   false,
			}
		}

		// Update fields
		if update.Name != "" {
			item.Name = update.Name
		}
		if update.Year > 0 {
			item.Year = update.Year
		}
		if update.Watched != nil {
			stateUpdatedAt := now
			if !update.WatchedAt.IsZero() {
				stateUpdatedAt = update.WatchedAt.UTC()
			}
			item.Watched = *update.Watched
			if *update.Watched {
				if !update.WatchedAt.IsZero() {
					item.WatchedAt = update.WatchedAt.UTC()
				} else {
					item.WatchedAt = now
				}
			}
			item.UpdatedAt = stateUpdatedAt
			if s.clearPlaybackProgressEntryLocked(userID, update.MediaType, update.ItemID) {
				progressCleared = true
			}
			if update.MediaType == "movie" {
				if s.clearMovieProgressByExternalIDMatchLocked(userID, update.ExternalIDs) {
					progressCleared = true
				}
			}
		}
		if update.ExternalIDs != nil {
			// Merge external IDs so cross-provider dedup preserves IDs from both sources
			if item.ExternalIDs == nil {
				item.ExternalIDs = make(map[string]string)
			}
			for k, v := range update.ExternalIDs {
				if v != "" {
					item.ExternalIDs[k] = v
				}
			}
		}
		if update.SeasonNumber > 0 {
			item.SeasonNumber = update.SeasonNumber
		}
		if update.EpisodeNumber > 0 {
			item.EpisodeNumber = update.EpisodeNumber
		}
		if update.SeriesID != "" {
			item.SeriesID = update.SeriesID
		}
		if update.SeriesName != "" {
			item.SeriesName = update.SeriesName
		}

		perUser[key] = item

		// Update cross-provider index so subsequent items in this batch can find this entry
		if item.MediaType == "episode" && item.SeasonNumber > 0 && item.EpisodeNumber > 0 {
			epKey := episodeKey(item.SeasonNumber, item.EpisodeNumber)
			for idType, idValue := range item.ExternalIDs {
				if idValue == "" || idType == "titleId" {
					continue
				}
				crossProviderIndex[epKey+":"+idType+":"+idValue] = episodeIndex{watchKey: key}
			}
		}

		if update.Watched != nil && *update.Watched && update.MediaType == "episode" && update.SeriesID != "" && update.SeasonNumber > 0 && update.EpisodeNumber > 0 {
			if s.clearEarlierEpisodesProgressLocked(userID, update.SeriesID, update.SeasonNumber, update.EpisodeNumber) {
				progressCleared = true
			}
			// Also clear progress by external ID matching (handles cross-ID-format mismatches)
			if s.clearProgressByExternalIDMatchLocked(userID, update.SeasonNumber, update.EpisodeNumber, update.ExternalIDs) {
				progressCleared = true
			}
		}

		imported++
	}

	if imported > 0 {
		if err := s.saveWatchHistoryLocked(); err != nil {
			return 0, err
		}
	}

	if progressCleared {
		if err := s.savePlaybackProgressLocked(); err != nil {
			return 0, err
		}
	}

	// Invalidate continue watching cache for this user
	delete(s.continueWatchingCache, userID)

	// NOTE: No scrobbling — this method is specifically for importing from external sources

	return imported, nil
}

func (s *Service) ensureWatchHistoryUserLocked(userID string) map[string]models.WatchHistoryItem {
	perUser, ok := s.watchHistory[userID]
	if !ok {
		perUser = make(map[string]models.WatchHistoryItem)
		s.watchHistory[userID] = perUser
	}
	return perUser
}

func (s *Service) loadWatchHistory() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.useDB() {
		ctx := context.Background()
		allItems, err := s.store.WatchHistory().ListAll(ctx)
		if err != nil {
			return fmt.Errorf("load watch history from db: %w", err)
		}
		s.watchHistory = make(map[string]map[string]models.WatchHistoryItem, len(allItems))
		for userID, items := range allItems {
			perUser := make(map[string]models.WatchHistoryItem, len(items))
			for _, item := range items {
				key := makeWatchKey(item.MediaType, item.ItemID)
				perUser[key] = item
			}
			s.watchHistory[userID] = perUser
		}
		return nil
	}

	file, err := os.Open(s.watchHistPath)
	if errors.Is(err, os.ErrNotExist) {
		s.watchHistory = make(map[string]map[string]models.WatchHistoryItem)
		return nil
	}
	if err != nil {
		return fmt.Errorf("open watch history: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read watch history: %w", err)
	}
	if len(data) == 0 {
		s.watchHistory = make(map[string]map[string]models.WatchHistoryItem)
		return nil
	}

	// Load as map[userID][]WatchHistoryItem
	var loaded map[string][]models.WatchHistoryItem
	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("decode watch history: %w", err)
	}

	s.watchHistory = make(map[string]map[string]models.WatchHistoryItem)
	needsSave := false
	for userID, items := range loaded {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			continue
		}
		perUser := make(map[string]models.WatchHistoryItem, len(items))
		for _, item := range items {
			// Normalize itemID and ID to lowercase for consistent key matching
			normalizedItemID := strings.ToLower(item.ItemID)
			normalizedID := strings.ToLower(item.ID)
			if item.ItemID != normalizedItemID || item.ID != normalizedID {
				needsSave = true
				item.ItemID = normalizedItemID
				item.ID = normalizedID
			}
			if item.UpdatedAt.IsZero() {
				if !item.WatchedAt.IsZero() {
					item.UpdatedAt = item.WatchedAt.UTC()
				} else {
					item.UpdatedAt = time.Now().UTC()
				}
				needsSave = true
			}

			// Migrate old-format episode entries: if itemID is just the series ID but
			// has season/episode numbers, convert to new format (seriesId:s01e02)
			if item.MediaType == "episode" && item.SeasonNumber > 0 && item.EpisodeNumber > 0 {
				// Check if itemID lacks episode suffix (old format)
				hasEpisodeSuffix := strings.Contains(item.ItemID, ":s") || strings.Contains(item.ItemID, ":S")
				if !hasEpisodeSuffix {
					// Convert to new format
					newItemID := fmt.Sprintf("%s:s%02de%02d", item.ItemID, item.SeasonNumber, item.EpisodeNumber)
					newID := fmt.Sprintf("episode:%s", newItemID)
					log.Printf("[history] migrating old episode format: %s -> %s", item.ID, newID)
					item.ItemID = newItemID
					item.ID = newID
					needsSave = true
				}
			}

			key := makeWatchKey(item.MediaType, item.ItemID)
			// If duplicate exists, keep the one that is watched (or most recently watched)
			if existing, exists := perUser[key]; exists {
				// Prefer watched over unwatched
				if item.Watched && !existing.Watched {
					perUser[key] = item
				} else if existing.Watched && !item.Watched {
					// Keep existing (it's watched)
				} else if item.UpdatedAt.After(existing.UpdatedAt) {
					// Both same status, keep more recent
					perUser[key] = item
				}
				needsSave = true // Mark that we merged duplicates
			} else {
				perUser[key] = item
			}
		}
		s.watchHistory[userID] = perUser
	}

	// Save if we normalized any keys or merged duplicates
	if needsSave {
		if err := s.saveWatchHistoryLocked(); err != nil {
			log.Printf("[history] warning: failed to save normalized watch history: %v", err)
		} else {
			log.Printf("[history] normalized watch history keys to lowercase")
		}
	}

	return nil
}

func (s *Service) saveWatchHistoryLocked() error {
	if s.useDB() {
		return s.syncWatchedToDB()
	}

	// Convert to array format for storage
	toSave := make(map[string][]models.WatchHistoryItem)
	for userID, perUser := range s.watchHistory {
		items := make([]models.WatchHistoryItem, 0, len(perUser))
		for _, item := range perUser {
			items = append(items, item)
		}
		// Sort by most recently watched
		sort.Slice(items, func(i, j int) bool {
			if items[i].WatchedAt.Equal(items[j].WatchedAt) {
				return items[i].ID < items[j].ID
			}
			return items[i].WatchedAt.After(items[j].WatchedAt)
		})
		toSave[userID] = items
	}

	data, err := json.MarshalIndent(toSave, "", "  ")
	if err != nil {
		return fmt.Errorf("encode watch history: %w", err)
	}

	if err := os.WriteFile(s.watchHistPath, data, 0o644); err != nil {
		return fmt.Errorf("write watch history: %w", err)
	}

	return nil
}

func makeWatchKey(mediaType, itemID string) string {
	return strings.ToLower(mediaType) + ":" + strings.ToLower(itemID)
}

// Playback Progress Methods

// UpdatePlaybackProgress updates the playback progress for a media item.
// Automatically marks items as watched when they reach 90% completion.
func (s *Service) UpdatePlaybackProgress(userID string, update models.PlaybackProgressUpdate) (models.PlaybackProgress, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return models.PlaybackProgress{}, ErrUserIDRequired
	}

	if update.Duration <= 0 && update.PercentWatched <= 0 {
		return models.PlaybackProgress{}, fmt.Errorf("duration must be positive")
	}

	if update.Position < 0 {
		return models.PlaybackProgress{}, fmt.Errorf("position cannot be negative")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	perUser := s.ensurePlaybackProgressUserLocked(userID)
	staleWatchedEpisodeUpdate := s.isWatchedEpisodeProgressUpdateLocked(userID, update)
	// Normalize itemID to lowercase for consistent key matching
	normalizedItemID := strings.ToLower(update.ItemID)
	key := makeWatchKey(update.MediaType, normalizedItemID)

	// Calculate percent watched
	var percentWatched float64
	if update.Duration > 0 {
		percentWatched = (update.Position / update.Duration) * 100
		if percentWatched > 100 {
			percentWatched = 100
		}
	} else {
		// No real duration (e.g. Trakt import) — use the provided percentage directly.
		// Position and duration stay at 0; the frontend uses percentWatched for resume.
		percentWatched = update.PercentWatched
	}

	// Create or update progress
	// Note: HiddenFromContinueWatching defaults to false, which clears any previous hidden state
	updatedAt := time.Now().UTC()
	if !update.Timestamp.IsZero() {
		// Preserve the original timestamp (e.g. from Trakt sync) to avoid
		// artificially bumping items in the continue-watching sort order.
		updatedAt = update.Timestamp.UTC()
	}
	progress := models.PlaybackProgress{
		ID:             key,
		MediaType:      strings.ToLower(update.MediaType),
		ItemID:         normalizedItemID,
		Position:       update.Position,
		Duration:       update.Duration,
		PercentWatched: percentWatched,
		UpdatedAt:      updatedAt,
		IsPaused:       update.IsPaused,
		ExternalIDs:    update.ExternalIDs,
		SeasonNumber:   update.SeasonNumber,
		EpisodeNumber:  update.EpisodeNumber,
		SeriesID:       update.SeriesID,
		SeriesName:     update.SeriesName,
		EpisodeName:    update.EpisodeName,
		MovieName:      update.MovieName,
		Year:           update.Year,
	}

	// For episodes, collapse same-episode progress stored under alternate ID formats
	// before writing the new row. This keeps each episode in a single "in progress"
	// state even when different sources use TMDB/TVDB/IMDB-based item IDs.
	if progress.MediaType == "episode" && progress.SeasonNumber > 0 && progress.EpisodeNumber > 0 {
		s.clearOtherEpisodeProgressByExternalIDMatchLocked(perUser, key, progress.SeasonNumber, progress.EpisodeNumber, progress.ExternalIDs)
	}
	if progress.MediaType == "movie" {
		s.clearOtherMovieProgressByExternalIDMatchLocked(perUser, key, progress.ExternalIDs)
	}

	perUser[key] = progress

	// Clear hidden flag for related series entries when new progress is logged
	// This ensures the series reappears in continue watching when user resumes watching
	if update.SeriesID != "" {
		// Extract provider/ID from the update's series ID for external ID matching
		// (e.g. "tvdb:series:73562" → provider="tvdb", numericID="73562")
		var unhideProvider, unhideNumericID string
		sParts := strings.Split(update.SeriesID, ":")
		if len(sParts) >= 2 {
			unhideProvider = strings.ToLower(sParts[0])
			unhideNumericID = sParts[len(sParts)-1]
		}

		for existingKey, existingProg := range perUser {
			if !existingProg.HiddenFromContinueWatching {
				continue
			}
			match := existingProg.ItemID == update.SeriesID || existingProg.SeriesID == update.SeriesID
			// Also match by external ID (handles canonical ID mismatches)
			if !match && unhideProvider != "" && unhideNumericID != "" {
				if extVal, ok := existingProg.ExternalIDs[unhideProvider]; ok && extVal == unhideNumericID {
					match = true
				}
			}
			// Also check reverse: does the update's external IDs match the marker's series ID?
			if !match && len(update.ExternalIDs) > 0 {
				markerParts := strings.Split(existingProg.SeriesID, ":")
				if len(markerParts) >= 2 {
					markerProvider := strings.ToLower(markerParts[0])
					markerNumericID := markerParts[len(markerParts)-1]
					if extVal, ok := update.ExternalIDs[markerProvider]; ok && extVal == markerNumericID {
						match = true
					}
				}
			}
			// Also check if any shared external ID key has the same value
			// (e.g. both have imdb: tt0115108 even though series IDs differ)
			if !match && len(update.ExternalIDs) > 0 && len(existingProg.ExternalIDs) > 0 {
				for k, v := range update.ExternalIDs {
					if ev, ok := existingProg.ExternalIDs[k]; ok && ev == v {
						match = true
						break
					}
				}
			}
			if match {
				if staleWatchedEpisodeUpdate &&
					existingProg.SeasonNumber == 0 &&
					existingProg.EpisodeNumber == 0 &&
					existingProg.ItemID == existingProg.SeriesID {
					continue
				}
				if isSeriesLevelPlaybackMarker(existingProg) {
					delete(perUser, existingKey)
					continue
				}
				existingProg.HiddenFromContinueWatching = false
				perUser[existingKey] = existingProg
			}
		}
	}

	if err := s.savePlaybackProgressLocked(); err != nil {
		return models.PlaybackProgress{}, err
	}

	// Invalidate continue watching cache for this user since progress changed
	delete(s.continueWatchingCache, userID)

	// Grab real-time scrobbler reference while holding the lock
	rtScrobbler := s.traktRTScrobbler
	allowRealtimeScrobble := !isLiveTVRecordingProgressUpdate(update)

	// Auto-mark as watched if >= 90% complete
	if percentWatched >= 90 {
		// Send scrobble/stop before marking as watched
		if rtScrobbler != nil && allowRealtimeScrobble {
			go rtScrobbler.StopSession(userID, update, percentWatched)
		}

		s.mu.Unlock() // Unlock before calling other methods
		err := s.markAsWatchedFromProgress(userID, update)
		s.mu.Lock() // Re-lock after
		if err != nil {
			// Log but don't fail the progress update
			fmt.Printf("Warning: failed to auto-mark as watched: %v\n", err)
		}
	} else if rtScrobbler != nil && allowRealtimeScrobble {
		// Below 90%: report real-time progress (start/pause/refresh)
		go rtScrobbler.HandleProgressUpdate(userID, update, percentWatched)
	}

	return progress, nil
}

func (s *Service) isWatchedEpisodeProgressUpdateLocked(userID string, update models.PlaybackProgressUpdate) bool {
	if strings.ToLower(strings.TrimSpace(update.MediaType)) != "episode" {
		return false
	}
	if update.SeasonNumber <= 0 || update.EpisodeNumber <= 0 {
		return false
	}

	perUserHistory, ok := s.watchHistory[userID]
	if !ok || len(perUserHistory) == 0 {
		return false
	}

	updateSeriesID := strings.TrimSpace(update.SeriesID)
	var updateProvider, updateNumericID string
	if updateSeriesID != "" {
		parts := strings.Split(updateSeriesID, ":")
		if len(parts) >= 2 {
			updateProvider = strings.ToLower(parts[0])
			updateNumericID = parts[len(parts)-1]
		}
	}

	for _, item := range perUserHistory {
		if item.MediaType != "episode" || !item.Watched {
			continue
		}
		if item.SeasonNumber != update.SeasonNumber || item.EpisodeNumber != update.EpisodeNumber {
			continue
		}
		if updateSeriesID != "" && item.SeriesID == updateSeriesID {
			return true
		}
		if updateProvider != "" && updateNumericID != "" {
			if extVal, ok := item.ExternalIDs[updateProvider]; ok && extVal == updateNumericID {
				return true
			}
		}
		if len(update.ExternalIDs) > 0 && len(item.ExternalIDs) > 0 {
			for k, v := range update.ExternalIDs {
				if v == "" || k == "titleId" {
					continue
				}
				if iv, ok := item.ExternalIDs[k]; ok && iv == v {
					return true
				}
			}
		}
	}

	return false
}

// IsWatchedEpisodeProgressUpdate reports whether the supplied progress update
// refers to an episode that is already marked watched in local history. Matching
// is tolerant of provider-ID differences and shared external IDs.
func (s *Service) IsWatchedEpisodeProgressUpdate(userID string, update models.PlaybackProgressUpdate) bool {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.isWatchedEpisodeProgressUpdateLocked(userID, update)
}

// markAsWatchedFromProgress marks an item as watched based on progress threshold.
func (s *Service) markAsWatchedFromProgress(userID string, update models.PlaybackProgressUpdate) error {
	watched := true
	historyUpdate := models.WatchHistoryUpdate{
		MediaType:     update.MediaType,
		ItemID:        update.ItemID,
		Watched:       &watched,
		ExternalIDs:   update.ExternalIDs,
		SeasonNumber:  update.SeasonNumber,
		EpisodeNumber: update.EpisodeNumber,
		SeriesID:      update.SeriesID,
		SeriesName:    update.SeriesName,
	}

	if update.MediaType == "episode" {
		historyUpdate.Name = update.EpisodeName
	} else {
		historyUpdate.Name = update.MovieName
		historyUpdate.Year = update.Year
	}

	_, err := s.UpdateWatchHistory(userID, historyUpdate)
	return err
}

// GetPlaybackProgress retrieves the playback progress for a specific media item.
func (s *Service) GetPlaybackProgress(userID, mediaType, itemID string) (*models.PlaybackProgress, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUserIDRequired
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	key := makeWatchKey(mediaType, itemID)
	if perUser, ok := s.playbackProgress[userID]; ok {
		if progress, ok := perUser[key]; ok {
			return &progress, nil
		}
	}

	return nil, nil
}

// ListPlaybackProgress returns all playback progress items for a user.
func (s *Service) ListPlaybackProgress(userID string) ([]models.PlaybackProgress, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUserIDRequired
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]models.PlaybackProgress, 0)
	if perUser, ok := s.playbackProgress[userID]; ok {
		items = make([]models.PlaybackProgress, 0, len(perUser))
		for _, progress := range perUser {
			// Deep copy to avoid concurrent map access during JSON encoding
			copy := progress
			if progress.ExternalIDs != nil {
				copy.ExternalIDs = make(map[string]string, len(progress.ExternalIDs))
				for k, v := range progress.ExternalIDs {
					copy.ExternalIDs[k] = v
				}
			}
			items = append(items, copy)
		}
	}

	// Sort by most recently updated
	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})

	return items, nil
}

// DeletePlaybackProgress removes playback progress for a specific media item.
func (s *Service) DeletePlaybackProgress(userID, mediaType, itemID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ErrUserIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := makeWatchKey(mediaType, itemID)
	if perUser, ok := s.playbackProgress[userID]; ok {
		delete(perUser, key)
		// Invalidate continue watching cache for this user since progress changed
		delete(s.continueWatchingCache, userID)
		return s.savePlaybackProgressLocked()
	}

	return nil
}

func (s *Service) ensurePlaybackProgressUserLocked(userID string) map[string]models.PlaybackProgress {
	perUser, ok := s.playbackProgress[userID]
	if !ok {
		perUser = make(map[string]models.PlaybackProgress)
		s.playbackProgress[userID] = perUser
	}
	return perUser
}

func (s *Service) loadPlaybackProgress() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.useDB() {
		ctx := context.Background()
		allItems, err := s.store.PlaybackProgress().ListAll(ctx)
		if err != nil {
			return fmt.Errorf("load playback progress from db: %w", err)
		}
		s.playbackProgress = make(map[string]map[string]models.PlaybackProgress, len(allItems))
		for userID, items := range allItems {
			perUser := make(map[string]models.PlaybackProgress, len(items))
			for _, item := range items {
				key := makeWatchKey(item.MediaType, item.ItemID)
				perUser[key] = item
			}
			s.playbackProgress[userID] = perUser
		}
		return nil
	}

	file, err := os.Open(s.playbackProgressPath)
	if errors.Is(err, os.ErrNotExist) {
		s.playbackProgress = make(map[string]map[string]models.PlaybackProgress)
		return nil
	}
	if err != nil {
		return fmt.Errorf("open playback progress: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read playback progress: %w", err)
	}
	if len(data) == 0 {
		s.playbackProgress = make(map[string]map[string]models.PlaybackProgress)
		return nil
	}

	// Load as map[userID][]PlaybackProgress
	var loaded map[string][]models.PlaybackProgress
	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("decode playback progress: %w", err)
	}

	s.playbackProgress = make(map[string]map[string]models.PlaybackProgress)
	needsSave := false
	for userID, items := range loaded {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			continue
		}
		perUser := make(map[string]models.PlaybackProgress, len(items))
		for _, item := range items {
			// Normalize itemID and ID to lowercase for consistent key matching
			normalizedItemID := strings.ToLower(item.ItemID)
			normalizedID := strings.ToLower(item.ID)
			if item.ItemID != normalizedItemID || item.ID != normalizedID {
				needsSave = true
				item.ItemID = normalizedItemID
				item.ID = normalizedID
			}

			key := makeWatchKey(item.MediaType, item.ItemID)
			// If duplicate exists, keep the most recent one
			if existing, exists := perUser[key]; exists {
				if item.UpdatedAt.After(existing.UpdatedAt) {
					perUser[key] = item
				}
				needsSave = true
			} else {
				perUser[key] = item
			}
		}
		s.playbackProgress[userID] = perUser
	}

	// Save if we normalized any keys or merged duplicates
	if needsSave {
		if err := s.savePlaybackProgressLocked(); err != nil {
			log.Printf("[history] warning: failed to save normalized playback progress: %v", err)
		} else {
			log.Printf("[history] normalized playback progress keys to lowercase")
		}
	}

	return nil
}

func (s *Service) savePlaybackProgressLocked() error {
	if s.useDB() {
		return s.syncProgressToDB()
	}

	// Convert to array format for storage
	toSave := make(map[string][]models.PlaybackProgress)
	for userID, perUser := range s.playbackProgress {
		items := make([]models.PlaybackProgress, 0, len(perUser))
		for _, item := range perUser {
			items = append(items, item)
		}
		// Sort by most recently updated
		sort.Slice(items, func(i, j int) bool {
			if items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
				return items[i].ID < items[j].ID
			}
			return items[i].UpdatedAt.After(items[j].UpdatedAt)
		})
		toSave[userID] = items
	}

	data, err := json.MarshalIndent(toSave, "", "  ")
	if err != nil {
		return fmt.Errorf("encode playback progress: %w", err)
	}

	if err := os.WriteFile(s.playbackProgressPath, data, 0o644); err != nil {
		return fmt.Errorf("write playback progress: %w", err)
	}

	return nil
}

// syncWatchedToDB writes the full in-memory watch history state to PostgreSQL.
func (s *Service) syncWatchedToDB() error {
	ctx := context.Background()
	return s.store.WithTx(ctx, func(tx *datastore.Tx) error {
		// Get existing DB state to detect deletes
		existing, err := tx.WatchHistory().ListAll(ctx)
		if err != nil {
			return err
		}
		// Build set of existing DB keys per user
		dbKeys := make(map[string]map[string]bool)
		for userID, items := range existing {
			keys := make(map[string]bool, len(items))
			for _, item := range items {
				keys[item.ID] = true
			}
			dbKeys[userID] = keys
		}
		// Upsert all in-memory items
		for userID, perUser := range s.watchHistory {
			for _, item := range perUser {
				itemCopy := item
				if err := tx.WatchHistory().Upsert(ctx, userID, &itemCopy); err != nil {
					return err
				}
				if dbKeys[userID] != nil {
					delete(dbKeys[userID], item.ID)
				}
			}
		}
		// Delete items removed from memory
		for userID, keys := range dbKeys {
			for key := range keys {
				if err := tx.WatchHistory().Delete(ctx, userID, key); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// syncProgressToDB writes the full in-memory playback progress state to PostgreSQL.
func (s *Service) syncProgressToDB() error {
	ctx := context.Background()
	return s.store.WithTx(ctx, func(tx *datastore.Tx) error {
		// Get existing DB state to detect deletes
		existing, err := tx.PlaybackProgress().ListAll(ctx)
		if err != nil {
			return err
		}
		// Build set of existing DB keys per user
		dbKeys := make(map[string]map[string]bool)
		for userID, items := range existing {
			keys := make(map[string]bool, len(items))
			for _, item := range items {
				keys[item.ID] = true
			}
			dbKeys[userID] = keys
		}
		// Upsert all in-memory items
		for userID, perUser := range s.playbackProgress {
			for _, item := range perUser {
				itemCopy := item
				if err := tx.PlaybackProgress().Upsert(ctx, userID, &itemCopy); err != nil {
					return err
				}
				if dbKeys[userID] != nil {
					delete(dbKeys[userID], item.ID)
				}
			}
		}
		// Delete items removed from memory
		for userID, keys := range dbKeys {
			for key := range keys {
				if err := tx.PlaybackProgress().Delete(ctx, userID, key); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// ListAllPlaybackProgress returns all playback progress for all users (for admin dashboard).
func (s *Service) ListAllPlaybackProgress() map[string][]models.PlaybackProgress {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string][]models.PlaybackProgress)
	for userID, perUser := range s.playbackProgress {
		items := make([]models.PlaybackProgress, 0, len(perUser))
		for _, progress := range perUser {
			// Only include items that haven't been hidden from continue watching
			if !progress.HiddenFromContinueWatching {
				items = append(items, progress)
			}
		}
		if len(items) > 0 {
			result[userID] = items
		}
	}
	return result
}

// clearPlaybackProgressEntryLocked removes a stored playback entry for the supplied item.
// Callers must hold s.mu before invoking this helper.
func (s *Service) clearPlaybackProgressEntryLocked(userID, mediaType, itemID string) bool {
	userID = strings.TrimSpace(userID)
	mediaType = strings.TrimSpace(mediaType)
	itemID = strings.TrimSpace(itemID)
	if userID == "" || mediaType == "" || itemID == "" {
		return false
	}

	perUser, ok := s.playbackProgress[userID]
	if !ok {
		return false
	}

	key := makeWatchKey(mediaType, itemID)
	if entry, exists := perUser[key]; exists {
		delete(perUser, key)
		if entry.HiddenFromContinueWatching {
			s.preserveSeriesHiddenMarkerLocked(perUser, entry)
		}
		return true
	}

	// Fall back to case-insensitive match (handles S/E casing differences)
	target := strings.ToLower(key)
	for existingKey, entry := range perUser {
		if strings.ToLower(existingKey) == target {
			delete(perUser, existingKey)
			if entry.HiddenFromContinueWatching {
				s.preserveSeriesHiddenMarkerLocked(perUser, entry)
			}
			return true
		}
	}

	return false
}

// HideFromContinueWatching marks an item as hidden from the continue watching list.
// The item will reappear if new progress is logged.
func (s *Service) HideFromContinueWatching(userID, seriesID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ErrUserIDRequired
	}
	seriesID = strings.TrimSpace(seriesID)
	if seriesID == "" {
		return ErrSeriesIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	perUser := s.ensurePlaybackProgressUserLocked(userID)

	// Extract provider and numeric ID from the seriesID being hidden
	// (e.g., "tmdb:tv:958" → provider="tmdb", numericID="958";
	//  "tvdb:series:73562" → provider="tvdb", numericID="73562")
	// This lets us match progress entries that use a different ID format
	// for the same show by checking their externalIDs map.
	var hideProvider, hideNumericID string
	parts := strings.Split(seriesID, ":")
	if len(parts) >= 2 {
		hideProvider = strings.ToLower(parts[0])
		hideNumericID = parts[len(parts)-1]
	}

	// Find any progress entries for this series (both movies and episodes)
	found := false
	markedCount := 0
	hiddenAt := time.Now().UTC()
	for key, progress := range perUser {
		// For movies, the itemID matches seriesID directly
		// For episodes, the seriesID field matches
		match := progress.ItemID == seriesID || progress.SeriesID == seriesID
		// Also match by external ID (handles canonical ID mismatches like tmdb:tv:958 vs tvdb:series:73562)
		if !match && hideProvider != "" && hideNumericID != "" {
			if extVal, ok := progress.ExternalIDs[hideProvider]; ok && extVal == hideNumericID {
				match = true
			}
		}
		if match {
			progress.HiddenFromContinueWatching = true
			progress.UpdatedAt = hiddenAt
			perUser[key] = progress
			found = true
			markedCount++
			log.Printf("[history] HideFromContinueWatching: marked entry hidden key=%q itemID=%q seriesID=%q", key, progress.ItemID, progress.SeriesID)
		}
	}

	// If no progress entry exists, create a minimal one just to track the hidden state
	if !found {
		// Determine if this is a movie or series based on the ID format
		mediaType := "episode"
		if strings.Contains(seriesID, ":movie:") {
			mediaType = "movie"
		}

		// Collect external IDs from watch history entries for this series
		// so the marker can be found by IMDB/TVDB when unhiding via a different ID format
		markerExternalIDs := s.collectExternalIDsForSeriesLocked(userID, seriesID)

		key := makeWatchKey(mediaType, seriesID)
		perUser[key] = models.PlaybackProgress{
			ID:                         key,
			MediaType:                  mediaType,
			ItemID:                     seriesID,
			SeriesID:                   seriesID,
			ExternalIDs:                markerExternalIDs,
			UpdatedAt:                  time.Now().UTC(),
			HiddenFromContinueWatching: true,
		}
		log.Printf("[history] HideFromContinueWatching: no existing entry found, created marker key=%q for seriesID=%q externalIDs=%v", key, seriesID, markerExternalIDs)
	} else {
		log.Printf("[history] HideFromContinueWatching: marked %d existing entries hidden for seriesID=%q", markedCount, seriesID)
	}

	// Invalidate continue watching cache
	delete(s.continueWatchingCache, userID)

	if err := s.savePlaybackProgressLocked(); err != nil {
		log.Printf("[history] HideFromContinueWatching: save failed: %v", err)
		return err
	}
	log.Printf("[history] HideFromContinueWatching: saved successfully for user=%q seriesID=%q", userID, seriesID)
	return nil
}

// clearEarlierEpisodesProgressLocked removes playback progress for the current episode
// and all earlier episodes of the same series when an episode is marked as watched.
// Callers must hold s.mu before invoking this helper.
func (s *Service) clearEarlierEpisodesProgressLocked(userID, seriesID string, seasonNumber, episodeNumber int) bool {
	if userID == "" || seriesID == "" {
		return false
	}

	perUser, ok := s.playbackProgress[userID]
	if !ok {
		return false
	}

	// Extract provider and numeric ID from seriesID (e.g. "tvdb:series:73762" -> "tvdb","73762")
	// for fallback matching against progress entries that use a different ID format.
	var idProvider, idValue string
	parts := strings.Split(seriesID, ":")
	if len(parts) >= 2 {
		idProvider = strings.ToLower(parts[0])
		idValue = parts[len(parts)-1]
	}

	anyCleared := false
	for key, progress := range perUser {
		if progress.MediaType != "episode" {
			continue
		}
		// Direct seriesID match, or fallback: check if the progress entry's external IDs
		// contain a matching provider+value (handles cross-ID-format mismatches,
		// e.g. Trakt imports with tvdb:series:73762 vs player progress with seriesId "1416").
		if progress.SeriesID != seriesID {
			matched := false
			if idProvider != "" && idValue != "" {
				if v, ok := progress.ExternalIDs[idProvider]; ok && v == idValue {
					matched = true
				}
			}
			if !matched {
				continue
			}
		}

		// Skip series-level hidden markers (no real episode data, just hidden state)
		if progress.HiddenFromContinueWatching && progress.SeasonNumber == 0 && progress.EpisodeNumber == 0 {
			continue
		}

		// Check if this episode is the current one or earlier
		isCurrentOrEarlier := false
		if progress.SeasonNumber < seasonNumber {
			isCurrentOrEarlier = true
		} else if progress.SeasonNumber == seasonNumber && progress.EpisodeNumber <= episodeNumber {
			isCurrentOrEarlier = true
		}

		if isCurrentOrEarlier {
			delete(perUser, key)
			if progress.HiddenFromContinueWatching {
				s.preserveSeriesHiddenMarkerLocked(perUser, progress)
			}
			anyCleared = true
		}
	}

	return anyCleared
}

// collectExternalIDsForSeriesLocked gathers external IDs (imdb, tvdb, tmdb) from
// watch history and progress entries that match the given seriesID. This enriches
// hidden markers so they can be found by cross-provider ID matching when unhiding.
// Callers must hold s.mu before invoking this helper.
func (s *Service) collectExternalIDsForSeriesLocked(userID, seriesID string) map[string]string {
	collected := make(map[string]string)

	// Check watch history
	if perUser, ok := s.watchHistory[userID]; ok {
		for _, item := range perUser {
			if item.SeriesID == seriesID {
				for _, k := range []string{"imdb", "tvdb", "tmdb"} {
					if v, ok := item.ExternalIDs[k]; ok && v != "" {
						collected[k] = v
					}
				}
				if len(collected) > 0 {
					break // one match is enough
				}
			}
		}
	}

	// Also check playback progress
	if perUser, ok := s.playbackProgress[userID]; ok {
		for _, prog := range perUser {
			if prog.SeriesID == seriesID {
				for _, k := range []string{"imdb", "tvdb", "tmdb"} {
					if v, ok := prog.ExternalIDs[k]; ok && v != "" {
						if _, exists := collected[k]; !exists {
							collected[k] = v
						}
					}
				}
			}
		}
	}

	if len(collected) == 0 {
		return nil
	}
	return collected
}

// clearProgressByExternalIDMatchLocked removes playback progress entries for a specific
// episode that match by season+episode number and share at least one external ID.
// This handles cross-ID-format mismatches where the same show uses different ID prefixes
// (e.g. Trakt imports with tvdb:series:X vs player progress with tmdb:tv:Y).
// Callers must hold s.mu before invoking this helper.
func (s *Service) clearProgressByExternalIDMatchLocked(userID string, seasonNumber, episodeNumber int, externalIDs map[string]string) bool {
	if seasonNumber <= 0 || episodeNumber <= 0 || len(externalIDs) == 0 {
		return false
	}

	perUser, ok := s.playbackProgress[userID]
	if !ok {
		return false
	}

	anyCleared := false
	for key, progress := range perUser {
		if progress.MediaType != "episode" ||
			progress.SeasonNumber != seasonNumber ||
			progress.EpisodeNumber != episodeNumber {
			continue
		}

		// Check if this progress entry shares any external ID with the update
		if hasMatchingExternalID(progress.ExternalIDs, externalIDs) {
			delete(perUser, key)
			if progress.HiddenFromContinueWatching {
				s.preserveSeriesHiddenMarkerLocked(perUser, progress)
			}
			anyCleared = true
		}
	}

	return anyCleared
}

// clearMovieProgressByExternalIDMatchLocked removes playback progress entries for a movie
// when the stored row shares any canonical external ID with the incoming watched update.
// This handles cross-provider mismatches like a watched TMDB import that should clear a
// stale TVDB resume row for the same movie.
// Callers must hold s.mu before invoking this helper.
func (s *Service) clearMovieProgressByExternalIDMatchLocked(userID string, externalIDs map[string]string) bool {
	if len(externalIDs) == 0 {
		return false
	}

	perUser, ok := s.playbackProgress[userID]
	if !ok {
		return false
	}

	anyCleared := false
	for key, progress := range perUser {
		if progress.MediaType != "movie" {
			continue
		}
		if !hasMatchingExternalID(progress.ExternalIDs, externalIDs) {
			continue
		}
		delete(perUser, key)
		if progress.HiddenFromContinueWatching {
			s.preserveSeriesHiddenMarkerLocked(perUser, progress)
		}
		anyCleared = true
	}

	return anyCleared
}

// clearOtherMovieProgressByExternalIDMatchLocked removes duplicate in-progress rows
// for the same movie stored under alternate ID formats, preserving keepKey.
// Callers must hold s.mu before invoking this helper.
func (s *Service) clearOtherMovieProgressByExternalIDMatchLocked(perUser map[string]models.PlaybackProgress, keepKey string, externalIDs map[string]string) bool {
	if len(externalIDs) == 0 {
		return false
	}

	anyCleared := false
	for key, progress := range perUser {
		if key == keepKey {
			continue
		}
		if progress.MediaType != "movie" {
			continue
		}
		if !hasMatchingExternalID(progress.ExternalIDs, externalIDs) {
			continue
		}
		delete(perUser, key)
		if progress.HiddenFromContinueWatching {
			s.preserveSeriesHiddenMarkerLocked(perUser, progress)
		}
		anyCleared = true
	}

	return anyCleared
}

// clearOtherEpisodeProgressByExternalIDMatchLocked removes duplicate in-progress rows
// for the same episode stored under alternate ID formats, preserving the entry whose
// key matches keepKey. Callers must hold s.mu before invoking this helper.
func (s *Service) clearOtherEpisodeProgressByExternalIDMatchLocked(perUser map[string]models.PlaybackProgress, keepKey string, seasonNumber, episodeNumber int, externalIDs map[string]string) bool {
	if seasonNumber <= 0 || episodeNumber <= 0 || len(externalIDs) == 0 {
		return false
	}

	anyCleared := false
	for key, progress := range perUser {
		if key == keepKey {
			continue
		}
		if progress.MediaType != "episode" ||
			progress.SeasonNumber != seasonNumber ||
			progress.EpisodeNumber != episodeNumber {
			continue
		}
		if hasMatchingExternalID(progress.ExternalIDs, externalIDs) {
			delete(perUser, key)
			if progress.HiddenFromContinueWatching {
				s.preserveSeriesHiddenMarkerLocked(perUser, progress)
			}
			anyCleared = true
		}
	}

	return anyCleared
}

// preserveSeriesHiddenMarkerLocked ensures the hidden-from-continue-watching state
// survives when an episode's playback progress is cleared (e.g. by Trakt sync marking
// the episode as watched). It creates a minimal series-level marker entry if one
// doesn't already exist for this series.
// Callers must hold s.mu before invoking this helper.
func (s *Service) preserveSeriesHiddenMarkerLocked(perUser map[string]models.PlaybackProgress, deleted models.PlaybackProgress) {
	seriesID := deleted.SeriesID
	if seriesID == "" {
		seriesID = deleted.ItemID
	}
	if seriesID == "" {
		return
	}

	// Check if another hidden entry already covers this series
	for _, p := range perUser {
		if p.HiddenFromContinueWatching && p.SeriesID == seriesID {
			return // already covered
		}
	}

	// Create a minimal series-level hidden marker (same format as HideFromContinueWatching fallback)
	mediaType := "episode"
	if strings.Contains(seriesID, ":movie:") {
		mediaType = "movie"
	}
	key := makeWatchKey(mediaType, seriesID)
	perUser[key] = models.PlaybackProgress{
		ID:                         key,
		MediaType:                  mediaType,
		ItemID:                     seriesID,
		SeriesID:                   seriesID,
		UpdatedAt:                  deleted.UpdatedAt,
		HiddenFromContinueWatching: true,
	}
}

// hasMatchingExternalID returns true if two external ID maps share at least one
// common key+value pair (excluding non-standard keys like "titleId").
func hasMatchingExternalID(a, b map[string]string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	for k, v := range a {
		if v == "" {
			continue
		}
		// Skip non-standard keys that aren't reliable for matching
		if k == "titleId" {
			continue
		}
		if bv, ok := b[k]; ok && bv == v {
			return true
		}
	}
	return false
}

func isSeriesLevelPlaybackMarker(progress models.PlaybackProgress) bool {
	return strings.EqualFold(strings.TrimSpace(progress.MediaType), "episode") &&
		progress.ItemID != "" &&
		progress.ItemID == progress.SeriesID &&
		progress.SeasonNumber == 0 &&
		progress.EpisodeNumber == 0
}

func dedupeContinueWatchingEntries(items []models.SeriesWatchState) []models.SeriesWatchState {
	if len(items) < 2 {
		return items
	}

	deduped := make([]models.SeriesWatchState, 0, len(items))
	seriesIndexByID := make(map[string]int)
	seriesIndexBySignature := make(map[string]int)
	movieIndexBySignature := make(map[string]int)

	for _, item := range items {
		if item.NextEpisode != nil {
			if existingIndex, ok := seriesIndexByID[item.SeriesID]; ok {
				if preferContinueWatchingEntry(item, deduped[existingIndex]) {
					deduped[existingIndex] = item
				}
				continue
			}

			signature := continueWatchingSeriesSignature(item)
			if signature != "" {
				if existingIndex, ok := seriesIndexBySignature[signature]; ok {
					if preferContinueWatchingEntry(item, deduped[existingIndex]) {
						deduped[existingIndex] = item
					}
					continue
				}
				seriesIndexBySignature[signature] = len(deduped)
			}

			seriesIndexByID[item.SeriesID] = len(deduped)
			deduped = append(deduped, item)
			continue
		}

		signature := continueWatchingMovieSignature(item)
		if signature == "" {
			deduped = append(deduped, item)
			continue
		}

		if existingIndex, ok := movieIndexBySignature[signature]; ok {
			if preferContinueWatchingEntry(item, deduped[existingIndex]) {
				deduped[existingIndex] = item
			}
			continue
		}

		movieIndexBySignature[signature] = len(deduped)
		deduped = append(deduped, item)
	}

	return deduped
}

func continueWatchingSeriesSignature(item models.SeriesWatchState) string {
	if item.NextEpisode == nil {
		return ""
	}

	if item.ExternalIDs != nil {
		if imdbID := strings.TrimSpace(item.ExternalIDs["imdb"]); imdbID != "" {
			return "imdb:" + strings.ToLower(imdbID)
		}
		if tmdbID := strings.TrimSpace(item.ExternalIDs["tmdb"]); tmdbID != "" {
			return "tmdb:" + tmdbID
		}
		if tvdbID := strings.TrimSpace(item.ExternalIDs["tvdb"]); tvdbID != "" {
			return "tvdb:" + tvdbID
		}
	}

	title := strings.ToLower(strings.TrimSpace(item.SeriesTitle))
	if title == "" {
		return ""
	}

	episodeRef := item.LastWatched
	if item.NextEpisode != nil {
		episodeRef = *item.NextEpisode
	}

	if item.Year > 0 {
		return fmt.Sprintf("title:%s:%d:s%02de%02d", title, item.Year, episodeRef.SeasonNumber, episodeRef.EpisodeNumber)
	}
	return fmt.Sprintf("title:%s:s%02de%02d", title, episodeRef.SeasonNumber, episodeRef.EpisodeNumber)
}

func continueWatchingMovieSignature(item models.SeriesWatchState) string {
	if item.NextEpisode != nil {
		return ""
	}

	if item.ExternalIDs != nil {
		if imdbID := strings.TrimSpace(item.ExternalIDs["imdb"]); imdbID != "" {
			return "imdb:" + strings.ToLower(imdbID)
		}
		if tmdbID := strings.TrimSpace(item.ExternalIDs["tmdb"]); tmdbID != "" {
			return "tmdb:" + tmdbID
		}
		if tvdbID := strings.TrimSpace(item.ExternalIDs["tvdb"]); tvdbID != "" {
			return "tvdb:" + tvdbID
		}
	}

	title := strings.ToLower(strings.TrimSpace(item.SeriesTitle))
	if title == "" {
		return ""
	}
	if item.Year > 0 {
		return fmt.Sprintf("title:%s:%d", title, item.Year)
	}
	return "title:" + title
}

func preferContinueWatchingEntry(candidate, existing models.SeriesWatchState) bool {
	if candidate.UpdatedAt.After(existing.UpdatedAt) {
		return true
	}
	if existing.UpdatedAt.After(candidate.UpdatedAt) {
		return false
	}
	if len(candidate.ExternalIDs) > len(existing.ExternalIDs) {
		return true
	}
	if len(existing.ExternalIDs) > len(candidate.ExternalIDs) {
		return false
	}
	candidateHasArtwork := candidate.PosterURL != "" || candidate.BackdropURL != ""
	existingHasArtwork := existing.PosterURL != "" || existing.BackdropURL != ""
	if candidateHasArtwork != existingHasArtwork {
		return candidateHasArtwork
	}
	return candidate.PercentWatched > existing.PercentWatched
}

func (s *Service) hasHighInProgressPlaybackLocked(userID string, update models.WatchHistoryUpdate) bool {
	perUser, ok := s.playbackProgress[userID]
	if !ok {
		return false
	}

	for _, progress := range perUser {
		if !isMatchingPlaybackForWatchUpdate(progress, update) {
			continue
		}
		if progress.PercentWatched >= traktStopWatchThreshold && progress.PercentWatched < continueWatchingCompletionThreshold {
			return true
		}
	}

	return false
}

func isMatchingPlaybackForWatchUpdate(progress models.PlaybackProgress, update models.WatchHistoryUpdate) bool {
	if strings.ToLower(progress.MediaType) != strings.ToLower(update.MediaType) {
		return false
	}

	if progress.ItemID == update.ItemID {
		return true
	}

	if update.MediaType == "movie" {
		return hasMatchingExternalID(progress.ExternalIDs, update.ExternalIDs)
	}

	if update.MediaType == "episode" {
		if progress.SeasonNumber != update.SeasonNumber || progress.EpisodeNumber != update.EpisodeNumber {
			return false
		}
		if progress.SeriesID != "" && update.SeriesID != "" && progress.SeriesID == update.SeriesID {
			return true
		}
		return hasMatchingExternalID(progress.ExternalIDs, update.ExternalIDs)
	}

	return false
}
