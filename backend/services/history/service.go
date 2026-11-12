package history

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/internal/datastore"
	"novastream/internal/mediaidentity"
	"novastream/models"
	"novastream/services/calendar"

	"golang.org/x/sync/singleflight"
)

var (
	ErrStorageDirRequired = errors.New("storage directory not provided")
	ErrUserIDRequired     = errors.New("user id is required")
	ErrSeriesIDRequired   = errors.New("series id is required")
	// ErrEpisodeNotAddressable rejects episode writes that cannot be tied to a
	// specific episode: no episode number, no sNNeNN item-id suffix, and no
	// episode-scoped external ID. Persisting them creates rows that can never be
	// matched, updated, or deleted by ID (e.g. title-string episode keys).
	ErrEpisodeNotAddressable = errors.New("episode updates require an episode number, an sNNeNN item id, or an episode-scoped external id")
	seriesDisplayLabelRE     = regexp.MustCompile(`(?i)\s*[•·]\s*S\d{1,4}E\d{1,5}\b.*$`)
)

const (
	continueWatchingCompletionThreshold = 90.0
	traktStopWatchThreshold             = 80.0
	continueWatchingComingSoonWindow    = 7 * 24 * time.Hour
	liveTVRecordingPathSegment          = "/live/recordings/"
	slowPlaybackProgressThreshold       = time.Second
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
	ScrobbleEpisode(userID string, showTVDBID, season, episode int, watchedAt time.Time, externalIDs map[string]string) error
	// UnscrobbleMovie removes a movie's watched state for a specific user.
	UnscrobbleMovie(userID string, tmdbID, tvdbID int, imdbID string) error
	// UnscrobbleEpisode removes an episode's watched state for a specific user.
	UnscrobbleEpisode(userID string, showTVDBID, season, episode int, externalIDs map[string]string) error
	// IsEnabled returns whether scrobbling is enabled for any account.
	IsEnabled() bool
	// IsEnabledForUser returns whether scrobbling is enabled for a specific user.
	IsEnabledForUser(userID string) bool
}

// TraktRealTimeScrobbler handles real-time scrobble events (start/pause/stop) to Trakt.
type TraktRealTimeScrobbler interface {
	HandleProgressUpdate(userID string, update models.PlaybackProgressUpdate, percentWatched float64)
	StopSession(userID string, update models.PlaybackProgressUpdate, percentWatched float64)
	ClearSession(userID string, update models.PlaybackProgressUpdate)
}

// RealtimePlaybackObserver mirrors playback state to a source server without
// participating in external scrobbling delay rules.
type RealtimePlaybackObserver interface {
	HandleProgressUpdate(userID string, update models.PlaybackProgressUpdate, percentWatched float64)
	StopSession(userID string, update models.PlaybackProgressUpdate, percentWatched float64)
	ClearSession(userID string, update models.PlaybackProgressUpdate)
}

// cachedSeriesMetadata holds cached series details with expiration.
type cachedSeriesMetadata struct {
	details *models.SeriesDetails
	// err is set for negative-cache entries: a recent lookup failed upstream and
	// should not be retried until expiresAt.
	err       error
	cachedAt  time.Time
	expiresAt time.Time
}

// seriesMetadataNegativeCacheTTL bounds how long a failed series metadata
// lookup is remembered before the provider is retried.
const seriesMetadataNegativeCacheTTL = 30 * time.Minute

// seriesStatesCacheTTL is a safety net for the revision-keyed series state
// cache. Revision keying handles every local mutation; the TTL only bounds
// staleness from time-dependent inputs (air dates, metadata cache refresh).
const seriesStatesCacheTTL = 5 * time.Minute

// watchStateRevision identifies the inputs of a user's derived watch state.
// Any mutation of the user's watch history or playback progress bumps user;
// whole-store loads/clears and metadata-service swaps bump global; airtime
// tracks TVmaze airtime refinements. Equal revisions mean equal inputs.
type watchStateRevision struct {
	user    uint64
	global  uint64
	airtime int64
}

// cachedSeriesStates holds a per-user ListSeriesStates result keyed by the
// watch state revision it was built from.
type cachedSeriesStates struct {
	revision  watchStateRevision
	items     []models.SeriesWatchState
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
	mu                        sync.RWMutex
	store                     *datastore.DataStore
	path                      string
	watchHistPath             string
	playbackProgressPath      string
	states                    map[string]map[string]models.SeriesWatchState // Deprecated: kept for migration only
	watchHistory              map[string]map[string]models.WatchHistoryItem // Manual watch tracking (all media)
	playbackProgress          map[string]map[string]models.PlaybackProgress // userID -> mediaKey -> progress
	persistedWatchHistory     map[historyPersistenceKey][sha256.Size]byte
	persistedPlaybackProgress map[historyPersistenceKey][sha256.Size]byte
	// activePlaybackProgress mirrors the most recent progress heartbeat for each
	// item but is NEVER cleared by watched-marking. It exists so the active-stream
	// dashboard can keep tracking position past the 90% auto-watched threshold
	// (which clears the row from playbackProgress). Entries are pruned by age on
	// read. Not persisted — purely in-memory live state.
	activePlaybackProgress     map[string]map[string]models.PlaybackProgress // userID -> mediaKey -> progress
	metadataService            MetadataService
	airtimeClient              *tvmazeAirtimeClient
	airtimeState               airtimeResolutionState
	traktScrobbler             TraktScrobbler
	bulkSyncTail               map[string]<-chan struct{} // protected by mu; preserves bulk action order per user
	traktRTScrobbler           TraktRealTimeScrobbler
	realtimePlaybackObserver   RealtimePlaybackObserver
	scrobbleStartDelayResolver func(userID, clientID string) time.Duration
	scrobbleStartDelaySessions map[string]*scrobbleStartDelaySession // protected by mu
	metadataCache              map[string]*cachedSeriesMetadata      // seriesID -> metadata (full details)
	seriesInfoCache            map[string]*cachedSeriesInfo          // seriesID -> lightweight info
	movieMetadataCache         map[string]*cachedMovieMetadata       // movieID -> metadata
	metadataCacheTTL           time.Duration
	continueWatchingCache      map[string]*cachedContinueWatching // userID -> continue watching
	continueWatchingTTL        time.Duration
	// Watch state revisions (protected by mu). watchStateSeq is a monotonically
	// increasing sequence; userWatchStateRev records the sequence value of each
	// user's latest mutation and globalWatchStateRev that of the latest
	// whole-store change. See watchStateRevision.
	watchStateSeq        uint64
	userWatchStateRev    map[string]uint64
	globalWatchStateRev  uint64
	seriesStatesCache    map[string]*cachedSeriesStates // userID -> all series states
	seriesStatesBuilds   singleflight.Group             // one in-flight rebuild per user+revision
	changeMu             sync.RWMutex
	watchStateChanged    func(userID string)
	playbackProgressGate chan struct{}
	playbackProgressOnce sync.Once
}

type continueWatchingRevisionStats struct {
	watchHistoryCount     int
	watchHistoryUpdated   time.Time
	playbackProgressCount int
	hiddenCount           int
	playbackUpdated       time.Time
}

type historyPersistenceKey struct {
	userID string
	itemID string
}

type watchHistoryPersistenceUpsert struct {
	key         historyPersistenceKey
	item        models.WatchHistoryItem
	fingerprint [sha256.Size]byte
}

type playbackProgressPersistenceUpsert struct {
	key         historyPersistenceKey
	item        models.PlaybackProgress
	fingerprint [sha256.Size]byte
}

// useDB returns true when the service is backed by PostgreSQL.
func (s *Service) useDB() bool { return s.store != nil }

// NewServiceWithStore creates a history service backed by PostgreSQL.
func NewServiceWithStore(store *datastore.DataStore) (*Service, error) {
	svc := &Service{
		store:                      store,
		states:                     make(map[string]map[string]models.SeriesWatchState),
		watchHistory:               make(map[string]map[string]models.WatchHistoryItem),
		playbackProgress:           make(map[string]map[string]models.PlaybackProgress),
		activePlaybackProgress:     make(map[string]map[string]models.PlaybackProgress),
		scrobbleStartDelaySessions: make(map[string]*scrobbleStartDelaySession),
		metadataCache:              make(map[string]*cachedSeriesMetadata),
		seriesInfoCache:            make(map[string]*cachedSeriesInfo),
		movieMetadataCache:         make(map[string]*cachedMovieMetadata),
		metadataCacheTTL:           24 * time.Hour,
		continueWatchingCache:      make(map[string]*cachedContinueWatching),
		continueWatchingTTL:        10 * time.Minute,
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
		path:                       filepath.Join(storageDir, "watch_history.json"),
		watchHistPath:              filepath.Join(storageDir, "watched_items.json"),
		playbackProgressPath:       filepath.Join(storageDir, "playback_progress.json"),
		states:                     make(map[string]map[string]models.SeriesWatchState),
		watchHistory:               make(map[string]map[string]models.WatchHistoryItem),
		playbackProgress:           make(map[string]map[string]models.PlaybackProgress),
		activePlaybackProgress:     make(map[string]map[string]models.PlaybackProgress),
		scrobbleStartDelaySessions: make(map[string]*scrobbleStartDelaySession),
		metadataCache:              make(map[string]*cachedSeriesMetadata),
		seriesInfoCache:            make(map[string]*cachedSeriesInfo),
		movieMetadataCache:         make(map[string]*cachedMovieMetadata),
		metadataCacheTTL:           24 * time.Hour, // Cache metadata for 24 hours - ensures new episodes are detected daily
		continueWatchingCache:      make(map[string]*cachedContinueWatching),
		continueWatchingTTL:        10 * time.Minute, // Cache continue watching response for 10 minutes - reduces frequent rebuilds
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
	s.bumpAllWatchStateRevisionsLocked()
}

// SetWatchStateChangedHook registers a callback invoked when a user's watch
// history/progress changes enough to affect continue watching.
func (s *Service) SetWatchStateChangedHook(fn func(userID string)) {
	s.changeMu.Lock()
	defer s.changeMu.Unlock()
	s.watchStateChanged = fn
}

func (s *Service) invalidateContinueWatchingLocked(userID string) {
	delete(s.continueWatchingCache, userID)
	s.bumpWatchStateRevisionLocked(userID)
	s.notifyWatchStateChanged(userID)
}

// bumpWatchStateRevisionLocked records that userID's watch history or playback
// progress changed, invalidating every revision-keyed derived cache for that
// user. Callers must hold s.mu for writing. Bumping more often than strictly
// necessary is harmless (it only costs a rebuild); missing a bump serves stale
// state, so mutation paths bump on entry to the critical section.
func (s *Service) bumpWatchStateRevisionLocked(userID string) {
	if s.userWatchStateRev == nil {
		s.userWatchStateRev = make(map[string]uint64)
	}
	s.watchStateSeq++
	s.userWatchStateRev[userID] = s.watchStateSeq
	delete(s.seriesStatesCache, userID)
}

// bumpAllWatchStateRevisionsLocked invalidates derived watch state for every
// user (whole-store loads/clears, metadata service changes). Callers must hold
// s.mu for writing.
func (s *Service) bumpAllWatchStateRevisionsLocked() {
	s.watchStateSeq++
	s.globalWatchStateRev = s.watchStateSeq
	s.seriesStatesCache = nil
}

// watchStateRevisionLocked returns the current revision of userID's derived
// watch state inputs. Callers must hold s.mu (read or write).
func (s *Service) watchStateRevisionLocked(userID string) watchStateRevision {
	return watchStateRevision{
		user:    s.userWatchStateRev[userID],
		global:  s.globalWatchStateRev,
		airtime: s.airtimeState.generation,
	}
}

// invalidateContinueWatchingCacheLocked refreshes position-sensitive Continue
// Watching data without notifying broader consumers whose inputs did not
// change. In particular, ordinary playback heartbeats do not change the set of
// series used by the calendar.
func (s *Service) invalidateContinueWatchingCacheLocked(userID string) {
	delete(s.continueWatchingCache, userID)
	s.bumpWatchStateRevisionLocked(userID)
}

func (s *Service) notifyWatchStateChanged(userID string) {
	s.changeMu.RLock()
	fn := s.watchStateChanged
	s.changeMu.RUnlock()
	if fn != nil {
		fn(userID)
	}
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

// SetRealtimePlaybackObserver installs the source-server playback reporter.
// It receives heartbeats immediately, independently of external scrobble delay.
func (s *Service) SetRealtimePlaybackObserver(observer RealtimePlaybackObserver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.realtimePlaybackObserver = observer
}

// SetScrobbleStartDelayResolver resolves global, profile, and client playback
// settings before the history lock is acquired for a progress update.
func (s *Service) SetScrobbleStartDelayResolver(resolver func(userID, clientID string) time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scrobbleStartDelayResolver = resolver
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

// seriesTVDBIDFromUpdate extracts a series TVDB id from a progress update,
// checking the external-ID map first and then a "tvdb:series:N" SeriesID.
func seriesTVDBIDFromUpdate(update models.PlaybackProgressUpdate) int {
	if update.ExternalIDs != nil {
		if raw, ok := update.ExternalIDs["tvdb"]; ok {
			if id, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && id > 0 {
				return id
			}
		}
	}
	if sid := strings.TrimSpace(update.SeriesID); sid != "" {
		if idx := strings.LastIndex(sid, ":"); idx >= 0 {
			if id, err := strconv.Atoi(sid[idx+1:]); err == nil && id > 0 && strings.Contains(sid, "tvdb") {
				return id
			}
		}
	}
	return 0
}

// GetSeriesOrdering returns the user's selected ordering season-type for a
// series (lowercase), or "" when none is set (the default/official ordering).
func (s *Service) GetSeriesOrdering(userID string, seriesTVDBID int64) (string, error) {
	if s.store == nil || seriesTVDBID <= 0 {
		return "", nil
	}
	pref, err := s.store.SeriesOrdering().Get(context.Background(), userID, seriesTVDBID)
	if err != nil || pref == nil {
		return "", err
	}
	return strings.ToLower(strings.TrimSpace(pref.SeasonType)), nil
}

// SetSeriesOrdering records the user's chosen ordering for a series. Selecting
// the official/default ordering clears any override.
func (s *Service) SetSeriesOrdering(userID string, seriesTVDBID int64, seasonType string) error {
	if s.store == nil {
		return fmt.Errorf("series ordering requires database storage")
	}
	if seriesTVDBID <= 0 {
		return fmt.Errorf("invalid series tvdb id")
	}
	seasonType = strings.ToLower(strings.TrimSpace(seasonType))
	if seasonType == "" || seasonType == "official" || seasonType == "default" {
		return s.store.SeriesOrdering().Delete(context.Background(), userID, seriesTVDBID)
	}
	return s.store.SeriesOrdering().Upsert(context.Background(), userID, &models.SeriesOrderingPref{
		SeriesTVDBID: seriesTVDBID,
		SeasonType:   seasonType,
		UpdatedAt:    time.Now().UTC(),
	})
}

// seriesOrderingIsAlternate reports whether the user has a non-official ordering
// active for the series, which disables scrobbling and external watch-sync.
func (s *Service) seriesOrderingIsAlternate(userID string, seriesTVDBID int) bool {
	if seriesTVDBID <= 0 {
		return false
	}
	st, err := s.GetSeriesOrdering(userID, int64(seriesTVDBID))
	if err != nil {
		return false
	}
	return st != "" && st != "official"
}

// doScrobble performs the actual scrobbling. This is separated from scrobbleWatchedItem
// to allow callers holding the lock to pass the scrobbler directly without re-acquiring the lock.
func (s *Service) doScrobble(scrobbler TraktScrobbler, userID string, item models.WatchHistoryItem) {
	if scrobbler == nil || !scrobbler.IsEnabledForUser(userID) {
		return
	}

	// Promote show-level IDs from seriesID / titleId so sparse history rows
	// (e.g. only {"titleId":"tmdb:tv:82782"}) still scrobble to providers.
	externalIDs := mediaidentity.EnrichShowExternalIDs(item.SeriesID, item.ItemID, item.ExternalIDs)

	// Extract IDs from the enriched map
	var tmdbID, tvdbID int
	var imdbID string

	if externalIDs != nil {
		if id, ok := externalIDs["tmdb"]; ok {
			tmdbID, _ = strconv.Atoi(id)
		}
		if id, ok := externalIDs["tvdb"]; ok {
			tvdbID, _ = strconv.Atoi(id)
		}
		if id, ok := externalIDs["imdb"]; ok {
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
		// Skip scrobbling when the user is viewing this series under a non-official
		// TVDB ordering: season/episode numbers diverge from the canonical aired
		// order that Trakt/Simkl/MDBList expect, so any sync would be wrong.
		if s.seriesOrderingIsAlternate(userID, tvdbID) {
			log.Printf("[trakt] skipping episode scrobble: alternate ordering active for series tvdb=%d user=%s", tvdbID, userID)
			return
		}
		// For episodes we need season/episode numbers plus at least one show
		// identifier. The scrobblers receive the full external-ID map and pick
		// whatever provider IDs they support (tvdb/tmdb/imdb), so a tvdb-less
		// row (e.g. tmdb-only player metadata) must not be dropped here.
		if (tvdbID > 0 || tmdbID > 0 || imdbID != "") && item.SeasonNumber > 0 && item.EpisodeNumber > 0 {
			season := item.SeasonNumber
			episode := item.EpisodeNumber
			seriesName := item.SeriesName
			go func() {
				if err := scrobbler.ScrobbleEpisode(userID, tvdbID, season, episode, watchedAt, externalIDs); err != nil {
					log.Printf("[trakt] failed to scrobble episode %s S%02dE%02d for user %s: %v", seriesName, season, episode, userID, err)
				} else {
					log.Printf("[trakt] scrobbled episode: %s S%02dE%02d for user %s", seriesName, season, episode, userID)
				}
			}()
		} else {
			log.Printf("[trakt] skipping episode scrobble: missing show IDs (tvdb=%d tmdb=%d imdb=%q), season=%d, or episode=%d", tvdbID, tmdbID, imdbID, item.SeasonNumber, item.EpisodeNumber)
		}
	}
}

// doUnscrobble removes an item's watched state from all enabled providers.
// Provider calls run asynchronously, matching watched scrobble behavior.
func (s *Service) doUnscrobble(scrobbler TraktScrobbler, userID string, item models.WatchHistoryItem) {
	if scrobbler == nil || !scrobbler.IsEnabledForUser(userID) {
		return
	}

	externalIDs := mediaidentity.EnrichShowExternalIDs(item.SeriesID, item.ItemID, item.ExternalIDs)
	var tmdbID, tvdbID int
	var imdbID string
	if externalIDs != nil {
		tmdbID, _ = strconv.Atoi(externalIDs["tmdb"])
		tvdbID, _ = strconv.Atoi(externalIDs["tvdb"])
		imdbID = externalIDs["imdb"]
	}

	switch item.MediaType {
	case "movie":
		if tmdbID > 0 || tvdbID > 0 || imdbID != "" {
			go func() {
				if err := scrobbler.UnscrobbleMovie(userID, tmdbID, tvdbID, imdbID); err != nil {
					log.Printf("[history] failed to sync unwatched movie %s for user %s: %v", item.Name, userID, err)
				}
			}()
		}
	case "episode":
		if s.seriesOrderingIsAlternate(userID, tvdbID) {
			log.Printf("[history] skipping episode unwatch sync: alternate ordering active for series tvdb=%d user=%s", tvdbID, userID)
			return
		}
		if (tvdbID > 0 || tmdbID > 0 || imdbID != "") && item.SeasonNumber > 0 && item.EpisodeNumber > 0 {
			season, episode := item.SeasonNumber, item.EpisodeNumber
			go func() {
				if err := scrobbler.UnscrobbleEpisode(userID, tvdbID, season, episode, externalIDs); err != nil {
					log.Printf("[history] failed to sync unwatched episode %s S%02dE%02d for user %s: %v", item.SeriesName, season, episode, userID, err)
				}
			}()
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
	s.invalidateContinueWatchingLocked(userID)
	s.mu.Unlock()

	// Build and return current state from watch history
	generation := s.airtimeGeneration()
	ctx := context.Background()
	states, err := s.buildSeriesStatesFromHistory(ctx, userID, true)
	if err != nil {
		return models.SeriesWatchState{}, err
	}

	// Cache the newly built result
	s.mu.Lock()
	s.cacheContinueWatchingLocked(userID, states, generation)
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
		Status:      models.SeriesReleaseStatusReleased,
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

	// Exact match first so identical-form lookups stay cheap and deterministic.
	for _, state := range states {
		if state.SeriesID == seriesID {
			return &state, nil
		}
	}

	// Fall back to identity matching: the state list advertises whichever ID
	// form the most recent rows carried (e.g. tvdb:series:X from a Trakt sync),
	// while clients may query by the tmdb:tv:Y form they know the show as.
	target := mediaidentity.Resolve(mediaidentity.Input{MediaType: "series", ID: seriesID})
	for _, state := range states {
		if seriesWatchStateMatchesIdentity(state, target) {
			return &state, nil
		}
	}

	return nil, nil
}

// seriesWatchStateMatchesIdentity reports whether a series watch state refers to
// the same show as the target identity, using the state's external IDs (which
// the continue-watching builder enriches from metadata) to bridge provider forms.
func seriesWatchStateMatchesIdentity(state models.SeriesWatchState, target mediaidentity.Identity) bool {
	current := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   "series",
		ID:          state.SeriesID,
		ExternalIDs: state.ExternalIDs,
	})
	return identitiesReferToSameTitle(current, target)
}

// identitiesReferToSameTitle compares two resolved identities of the same media
// type across canonical keys, alias candidate keys, and identity tokens.
func identitiesReferToSameTitle(a, b mediaidentity.Identity) bool {
	if a.Key != "" && a.Key == b.Key {
		return true
	}
	for _, key := range a.CandidateKeys {
		if key == b.Key {
			return true
		}
	}
	for _, key := range b.CandidateKeys {
		if key == a.Key {
			return true
		}
	}
	return mediaidentity.Equivalent(a, b)
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
	generation := s.airtimeState.generation
	revision := s.watchStateRevisionLocked(userID)
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

	// Cache the result unless watch state changed while building; otherwise an
	// in-flight build could re-populate the cache with pre-mutation state right
	// after the mutation invalidated it.
	s.mu.Lock()
	if s.watchStateRevisionLocked(userID) == revision {
		s.cacheContinueWatchingLocked(userID, items, generation)
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

func isLiveProgressUpdate(update models.PlaybackProgressUpdate) bool {
	return strings.EqualFold(strings.TrimSpace(update.MediaType), "live")
}

// hasCatalogExternalIDs reports whether external IDs include a real metadata
// provider identity (IMDB/TMDB/TVDB). Bare library keys stored as titleId alone
// (e.g. Plex rating keys for home videos) do not count.
func hasCatalogExternalIDs(externalIDs map[string]string) bool {
	if len(externalIDs) == 0 {
		return false
	}
	for _, key := range []string{"imdb", "tmdb", "tvdb", "episodeImdb", "episodeTmdb", "episodeTvdb"} {
		if strings.TrimSpace(externalIDs[key]) != "" {
			return true
		}
	}
	return isCatalogMediaID(externalIDs["titleId"])
}

// isCatalogMediaID reports whether an id is a provider-prefixed catalog identity
// (tmdb:/tvdb:/imdb:) or an IMDB tt… id. Bare numeric library keys are not.
func isCatalogMediaID(id string) bool {
	id = strings.TrimSpace(strings.ToLower(id))
	if id == "" {
		return false
	}
	if strings.HasPrefix(id, "tt") {
		if len(id) <= 2 {
			return false
		}
		for _, r := range id[2:] {
			if r < '0' || r > '9' {
				return false
			}
		}
		return true
	}
	provider, _, _ := strings.Cut(id, ":")
	switch provider {
	case "tmdb", "tvdb", "imdb":
		return true
	default:
		return false
	}
}

// lacksCatalogIdentity is true for playback that has no TVDB/IMDB/TMDB (etc)
// identity — home videos, unmatched Plex/Jellyfin "other" library items, and
// similar. Like live TV, these may still store progress for resume, but must
// not enter continue watching or watched history.
func lacksCatalogIdentity(mediaType, itemID, seriesID string, externalIDs map[string]string) bool {
	if strings.EqualFold(strings.TrimSpace(mediaType), "live") {
		return true
	}
	if hasCatalogExternalIDs(externalIDs) {
		return false
	}
	if isCatalogMediaID(itemID) || isCatalogMediaID(seriesID) {
		return false
	}
	return true
}

func isUntaggedMediaProgress(progress models.PlaybackProgress) bool {
	return lacksCatalogIdentity(progress.MediaType, progress.ItemID, progress.SeriesID, progress.ExternalIDs)
}

func isUntaggedMediaProgressUpdate(update models.PlaybackProgressUpdate) bool {
	return lacksCatalogIdentity(update.MediaType, update.ItemID, update.SeriesID, update.ExternalIDs)
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
		"wh:%d:%d:%x|pp:%d:%d:%d|air:%d",
		stats.watchHistoryCount,
		stats.watchHistoryUpdated.UTC().UnixNano(),
		watchHistoryIdentityRevision(s.watchHistory[userID]),
		stats.playbackProgressCount,
		stats.hiddenCount,
		stats.playbackUpdated.UTC().UnixNano(),
		s.airtimeState.generation,
	), nil
}

func (s *Service) GetWatchHistoryRevision(userID string) (string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", ErrUserIDRequired
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	count := 0
	var newest time.Time
	if perUser := s.watchHistory[userID]; perUser != nil {
		count = len(perUser)
		for _, item := range perUser {
			if item.UpdatedAt.After(newest) {
				newest = item.UpdatedAt
			}
		}
	}

	return fmt.Sprintf("wh:%d:%d:%x", count, newest.UTC().UnixNano(), watchHistoryIdentityRevision(s.watchHistory[userID])), nil
}

// ListSeriesStates returns the watch state for ALL series the user has watched,
// including those with no next episode (fully watched).
func (s *Service) ListSeriesStates(userID string) ([]models.SeriesWatchState, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUserIDRequired
	}

	// Callers (watchlist/shelf enrichment) want the freshest state, so the
	// cache is keyed by the watch state revision: any history/progress
	// mutation, store reload or airtime refinement produces a new revision and
	// forces a rebuild. The TTL is only a safety net for time-dependent inputs.
	s.mu.RLock()
	revision := s.watchStateRevisionLocked(userID)
	cached := s.seriesStatesCache[userID]
	s.mu.RUnlock()
	if cached != nil && cached.revision == revision && time.Now().Before(cached.expiresAt) {
		return cloneSeriesWatchStates(cached.items), nil
	}

	buildKey := fmt.Sprintf("%s\x00%d:%d:%d", userID, revision.user, revision.global, revision.airtime)
	built, err, _ := s.seriesStatesBuilds.Do(buildKey, func() (any, error) {
		items, err := s.buildSeriesStatesFromHistory(context.Background(), userID, false)
		if err != nil {
			return nil, err
		}

		s.mu.Lock()
		// Only cache when nothing changed while building: a concurrent mutation
		// (or an identity repair performed by the build itself) means the result may
		// reflect pre-mutation inputs.
		if s.watchStateRevisionLocked(userID) == revision && s.metadataService != nil {
			if s.seriesStatesCache == nil {
				s.seriesStatesCache = make(map[string]*cachedSeriesStates)
			}
			s.seriesStatesCache[userID] = &cachedSeriesStates{
				revision:  revision,
				items:     cloneSeriesWatchStates(items),
				expiresAt: s.seriesStatesExpiryLocked(userID, time.Now()),
			}
		}
		s.mu.Unlock()
		return items, nil
	})
	if err != nil {
		return nil, err
	}
	// The build result is shared by every caller that joined it.
	items, _ := built.([]models.SeriesWatchState)
	return cloneSeriesWatchStates(items), nil
}

// seriesStatesExpiryLocked returns when a series state cache entry built now
// must expire: the TTL safety net, or earlier if a live heartbeat overlaid on
// playback progress will age out (changing ListPlaybackProgress output without
// a mutation). Callers must hold s.mu.
func (s *Service) seriesStatesExpiryLocked(userID string, now time.Time) time.Time {
	expiresAt := now.Add(seriesStatesCacheTTL)
	for _, progress := range s.activePlaybackProgress[userID] {
		if activeExpiry := progress.UpdatedAt.Add(activeProgressTTL); activeExpiry.After(now) && activeExpiry.Before(expiresAt) {
			expiresAt = activeExpiry
		}
	}
	return expiresAt
}

// cloneSeriesWatchStates deep-copies series watch states so cached results
// cannot be mutated by callers (or vice versa).
func cloneSeriesWatchStates(items []models.SeriesWatchState) []models.SeriesWatchState {
	if items == nil {
		return nil
	}
	out := make([]models.SeriesWatchState, len(items))
	for i, item := range items {
		out[i] = cloneSeriesWatchState(item)
	}
	return out
}

func cloneSeriesWatchState(item models.SeriesWatchState) models.SeriesWatchState {
	if item.BackdropURLs != nil {
		item.BackdropURLs = append([]string(nil), item.BackdropURLs...)
	}
	if item.ExternalIDs != nil {
		ids := make(map[string]string, len(item.ExternalIDs))
		for k, v := range item.ExternalIDs {
			ids[k] = v
		}
		item.ExternalIDs = ids
	}
	item.LastWatched = cloneEpisodeReference(item.LastWatched)
	if item.NextEpisode != nil {
		next := cloneEpisodeReference(*item.NextEpisode)
		item.NextEpisode = &next
	}
	if item.WatchedEpisodes != nil {
		eps := make(map[string]models.EpisodeReference, len(item.WatchedEpisodes))
		for k, v := range item.WatchedEpisodes {
			eps[k] = cloneEpisodeReference(v)
		}
		item.WatchedEpisodes = eps
	}
	if item.Theatrical != nil {
		release := *item.Theatrical
		item.Theatrical = &release
	}
	if item.HomeRelease != nil {
		release := *item.HomeRelease
		item.HomeRelease = &release
	}
	return item
}

func cloneEpisodeReference(ep models.EpisodeReference) models.EpisodeReference {
	if ep.Numbering != nil {
		numbering := *ep.Numbering
		ep.Numbering = &numbering
	}
	if ep.Image != nil {
		image := *ep.Image
		ep.Image = &image
	}
	return ep
}

// buildSeriesStatesFromHistory generates watch state for series from watch history and playback progress.
// If onlyInProgress is true, it only returns series with an available next episode or active progress.
// Prioritizes in-progress episodes (partially watched) over completed episodes.
// Metadata lookups are parallelized for better performance.
func (s *Service) buildSeriesStatesFromHistory(ctx context.Context, userID string, onlyInProgress bool) ([]models.SeriesWatchState, error) {
	return s.buildSeriesStatesWithIdentityRepair(ctx, userID, onlyInProgress, true)
}

func (s *Service) buildSeriesStatesWithIdentityRepair(ctx context.Context, userID string, onlyInProgress, allowRepair bool) ([]models.SeriesWatchState, error) {
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

	// Global watched-episode index keyed by provider-scoped tokens
	// ("tvdb:371980:s01e01"). Series groups that share no stored external IDs
	// (tmdb-only progress vs tvdb-only history for the same show) can't be
	// merged by the canonical map above; this index lets the per-series pass
	// recognise — via its metadata-enriched IDs — that an in-progress episode
	// was already watched under the other provider's rows.
	watchedEpisodeProviderTokens := make(map[string]time.Time)
	for _, item := range items {
		if item.MediaType != "episode" || !item.Watched || item.EpisodeNumber <= 0 {
			continue
		}
		for idType, idValue := range canonicalSeriesExternalIDs(item.SeriesID, item.ItemID, item.ExternalIDs) {
			if idValue == "" {
				continue
			}
			token := idType + ":" + strings.ToLower(idValue) + ":" + episodeKey(item.SeasonNumber, item.EpisodeNumber)
			if at := watchHistoryActivityTime(item); at.After(watchedEpisodeProviderTokens[token]) {
				watchedEpisodeProviderTokens[token] = at
			}
		}
	}

	// Collect hidden markers for the late suppression pass below. Early
	// suppression (hiddenSeriesIDs) only catches rows the canonical map can
	// link; once states are metadata-enriched we can also suppress groups that
	// match a marker only through metadata external IDs.
	type hiddenMarkerInfo struct {
		mediaType   string
		seriesID    string
		itemID      string
		externalIDs map[string]string
		updatedAt   time.Time
	}
	var hiddenMarkers []hiddenMarkerInfo
	for _, prog := range progressItems {
		if !prog.HiddenFromContinueWatching {
			continue
		}
		hiddenMarkers = append(hiddenMarkers, hiddenMarkerInfo{
			mediaType:   prog.MediaType,
			seriesID:    prog.SeriesID,
			itemID:      prog.ItemID,
			externalIDs: prog.ExternalIDs,
			updatedAt:   prog.UpdatedAt,
		})
	}

	// Track the newest hidden marker per series. A marker only suppresses activity
	// at or before the hide time; playback that happened later must make the
	// series visible again even if a legacy/stale marker row survived persistence.
	hiddenSeriesAt := make(map[string]time.Time)
	markHidden := func(seriesID string, hiddenAt time.Time) {
		seriesID = resolveCanonicalID(canonicalSeriesID, seriesID)
		if seriesID == "" {
			return
		}
		if existing, ok := hiddenSeriesAt[seriesID]; !ok || hiddenAt.After(existing) {
			hiddenSeriesAt[seriesID] = hiddenAt
		}
	}
	for _, prog := range progressItems {
		if prog.HiddenFromContinueWatching {
			// Add both the itemID (for movies) and seriesID (for episodes)
			if prog.ItemID != "" {
				markHidden(prog.ItemID, prog.UpdatedAt)
			}
			if prog.SeriesID != "" {
				markHidden(prog.SeriesID, prog.UpdatedAt)
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
							markHidden(inferredSeriesID, prog.UpdatedAt)
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
		// Untagged library media (home videos / no TVDB/IMDB/TMDB) must not
		// enter continue watching — same policy as live TV.
		if isUntaggedMediaProgress(*prog) {
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

		hiddenAt, hidden := hiddenSeriesAt[seriesID]
		if isEpisode && seriesID != "" && prog.PercentWatched < 90 && (!hidden || prog.UpdatedAt.After(hiddenAt)) {
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
	// Every watched episode regardless of age. The 365-day window above only
	// decides which series surface on continue watching; episode counts must
	// span the whole history or a show watched over several years reports its
	// older seasons as unwatched.
	seriesLifetimeEpisodes := make(map[string][]models.WatchHistoryItem)
	seriesLifetimeUnwatched := make(map[string][]models.WatchHistoryItem)
	seriesHistory := make(map[string][]models.WatchHistoryItem)
	seriesActivityAt := make(map[string]time.Time)
	seriesInfo := make(map[string]models.WatchHistoryItem) // Track series metadata
	corruptedUnwatchedBySeries := make(map[string][]models.WatchHistoryItem)
	promotedCorruptedGapsBySeries := make(map[string]map[string]struct{})
	watchedEpisodeSetBySeries := make(map[string]map[string]struct{})
	type episodePosition struct {
		season  int
		episode int
	}
	furthestWatchedBySeries := make(map[string]episodePosition)

	for _, item := range items {
		if item.MediaType == "episode" && item.SeriesID != "" {
			resolvedID := resolveCanonicalID(canonicalSeriesID, item.SeriesID)
			// Skip a series only when its hide marker is at least as recent as
			// this history activity. Newer viewing must supersede the marker.
			if hiddenAt, hidden := hiddenSeriesAt[resolvedID]; hidden && !watchHistoryActivityTime(item).After(hiddenAt) {
				continue
			}
			activityAt := watchHistoryActivityTime(item)
			if activityAt.After(cutoffDate) {
				seriesHistory[resolvedID] = append(seriesHistory[resolvedID], item)
				if activityAt.After(seriesActivityAt[resolvedID]) {
					seriesActivityAt[resolvedID] = activityAt
				}
			}
			if hasContradictorySeriesExternalIDs(item.SeriesID, item.ItemID, item.ExternalIDs) && !item.Watched {
				corruptedUnwatchedBySeries[resolvedID] = append(corruptedUnwatchedBySeries[resolvedID], item)
				continue
			}
			if item.Watched {
				seriesLifetimeEpisodes[resolvedID] = append(seriesLifetimeEpisodes[resolvedID], item)
			} else {
				seriesLifetimeUnwatched[resolvedID] = append(seriesLifetimeUnwatched[resolvedID], item)
			}
			if item.Watched && item.WatchedAt.After(cutoffDate) {
				seriesEpisodes[resolvedID] = append(seriesEpisodes[resolvedID], item)
				if watchedEpisodeSetBySeries[resolvedID] == nil {
					watchedEpisodeSetBySeries[resolvedID] = make(map[string]struct{})
				}
				watchedEpisodeSetBySeries[resolvedID][episodeKey(item.SeasonNumber, item.EpisodeNumber)] = struct{}{}
				furthest := furthestWatchedBySeries[resolvedID]
				if furthest.season == 0 || compareEpisodeOrder(item.SeasonNumber, item.EpisodeNumber, furthest.season, furthest.episode) > 0 {
					furthestWatchedBySeries[resolvedID] = episodePosition{season: item.SeasonNumber, episode: item.EpisodeNumber}
				}
				if _, exists := seriesInfo[resolvedID]; !exists {
					seriesInfo[resolvedID] = item
				}
			}
		}
	}

	for seriesID, corruptedItems := range corruptedUnwatchedBySeries {
		furthest := furthestWatchedBySeries[seriesID]
		if furthest.season == 0 {
			continue
		}
		for _, item := range corruptedItems {
			if compareEpisodeOrder(item.SeasonNumber, item.EpisodeNumber, furthest.season, furthest.episode) > 0 {
				continue
			}
			key := episodeKey(item.SeasonNumber, item.EpisodeNumber)
			if _, exists := watchedEpisodeSetBySeries[seriesID][key]; exists {
				continue
			}
			item.Watched = true
			if item.WatchedAt.IsZero() {
				item.WatchedAt = watchHistoryActivityTime(item)
			}
			seriesEpisodes[seriesID] = append(seriesEpisodes[seriesID], item)
			if promotedCorruptedGapsBySeries[seriesID] == nil {
				promotedCorruptedGapsBySeries[seriesID] = make(map[string]struct{})
			}
			promotedCorruptedGapsBySeries[seriesID][key] = struct{}{}
			watchedEpisodeSetBySeries[seriesID][key] = struct{}{}
			if _, exists := seriesInfo[seriesID]; !exists {
				seriesInfo[seriesID] = item
			}
		}
	}

	// Also consider series with only in-progress items (no completed episodes).
	// Without other watch history, a below-floor episode is a sample rather than
	// a started show, so it doesn't put the series on continue watching.
	for seriesID, prog := range inProgressBySeriesCache {
		if _, exists := seriesInfo[seriesID]; exists {
			continue
		}
		if !MeetsResumeFloor(prog.Position, prog.PercentWatched) {
			delete(inProgressBySeriesCache, seriesID)
			continue
		}
		if prog.SeriesName != "" {
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
		// Home videos / unmatched library files without catalog IDs must not
		// appear on continue watching (and must not be name/ID-collided into
		// unrelated TMDB/TVDB titles).
		if isUntaggedMediaProgress(*prog) {
			continue
		}

		// Only include movies past the resume floor and under 90% (resume
		// watching); anything shorter likely wasn't really started.
		if prog.MediaType == "movie" && MeetsResumeFloor(prog.Position, prog.PercentWatched) && prog.PercentWatched < 90 {
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
	var identityRepairs []episodeSeriesIdentityRepair
	var repairLookups sync.Map

	// Process series in parallel
	type seriesTask struct {
		seriesID   string
		info       models.WatchHistoryItem
		episodes   []models.WatchHistoryItem
		lifetime   []models.WatchHistoryItem
		unwatched  []models.WatchHistoryItem
		history    []models.WatchHistoryItem
		activityAt time.Time
		inProgress *models.PlaybackProgress
		promoted   map[string]struct{}
	}

	var seriesTasks []seriesTask
	for seriesID := range seriesInfo {
		seriesTasks = append(seriesTasks, seriesTask{
			seriesID:   seriesID,
			info:       seriesInfo[seriesID],
			episodes:   seriesEpisodes[seriesID],
			lifetime:   seriesLifetimeEpisodes[seriesID],
			unwatched:  seriesLifetimeUnwatched[seriesID],
			history:    seriesHistory[seriesID],
			activityAt: seriesActivityAt[seriesID],
			inProgress: inProgressBySeriesCache[seriesID],
			promoted:   promotedCorruptedGapsBySeries[seriesID],
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

			// Fetch series metadata once (cached) so the in-progress reconciliation,
			// next-episode lookup, and enrichment below can all reuse it.
			seriesDetails, seriesErr := s.getSeriesMetadataWithCache(ctx, t.seriesID, t.info.SeriesName, t.info.ExternalIDs)
			if allowRepair {
				repairDetails := seriesDetails
				if seriesErr != nil && strings.TrimSpace(t.info.SeriesName) != "" {
					// A legacy Plex import can contain an episode ID in the series
					// slot. Search by name only to obtain a candidate, then require
					// an exact episode-provider ID match before changing history.
					name := normalizeSeriesName(t.info.SeriesName)
					entry, _ := repairLookups.LoadOrStore(strings.ToLower(name), &episodeSeriesRepairLookup{})
					lookup := entry.(*episodeSeriesRepairLookup)
					lookup.once.Do(func() { lookup.details = s.getEpisodeSeriesRepairMetadata(ctx, name) })
					repairDetails = lookup.details
				}
				for _, item := range t.history {
					if repaired, ok := repairEpisodeSeriesIdentity(item, repairDetails); ok {
						mu.Lock()
						identityRepairs = append(identityRepairs, episodeSeriesIdentityRepair{before: item, after: repaired})
						mu.Unlock()
					}
				}
			}
			numbering := newEpisodeNumberingIndex(seriesDetails)
			t.episodes = watchedEpisodesAfterManualUnwatch(t.episodes, t.history, numbering)
			lifetimeWatchedCount := countLifetimeWatchedEpisodes(
				watchedEpisodesAfterManualUnwatch(t.lifetime, t.unwatched, numbering), t.episodes, t.promoted, numbering,
				releasedEpisodeSet(seriesDetails))

			// Priority 1: In-progress episode (resume watching)
			// Skip if the in-progress episode is already marked as watched in history
			// (e.g. Trakt sync marked it complete while stale playback progress remains),
			// or if it points behind the furthest watched episode and is not newer than
			// the latest watch-history activity for the series.
			inProgressAlreadyWatched := false
			inProgressStaleBehindLatestWatched := false
			// The watched event that made the in-progress entry count as watched.
			var inProgressWatchedAt time.Time
			if t.inProgress != nil {
				// Reconcile absolute episode numbering before comparing against watch
				// history so a finished episode recorded under its absolute number
				// (e.g. One Piece S23E1163) is recognised as the season-relative entry
				// already in history (S23E08) instead of resurfacing as "next up".
				inProgressSeason, inProgressEpisode := numbering.canonical(t.inProgress.SeasonNumber, t.inProgress.EpisodeNumber)
				var latestWatchedAt time.Time
				var matchingWatchedAt time.Time
				var furthestSeason, furthestEpisode int
				haveFurthest := false
				for _, ep := range t.episodes {
					watchedSeason, watchedEpisode := numbering.canonical(ep.SeasonNumber, ep.EpisodeNumber)
					if watchedSeason == inProgressSeason && watchedEpisode == inProgressEpisode {
						if at := watchHistoryActivityTime(ep); at.After(matchingWatchedAt) {
							matchingWatchedAt = at
						}
					}
					if ep.WatchedAt.After(latestWatchedAt) {
						latestWatchedAt = ep.WatchedAt
					}
					if !haveFurthest || compareEpisodeOrder(watchedSeason, watchedEpisode, furthestSeason, furthestEpisode) > 0 {
						furthestSeason, furthestEpisode = watchedSeason, watchedEpisode
						haveFurthest = true
					}
				}
				// Progress at or before the watched event is stale. Newer progress
				// counts as a rewatch only once it passes the resume floor, then
				// replaces the on-deck resume target.
				inProgressAlreadyWatched = !matchingWatchedAt.IsZero() &&
					(!MeetsResumeFloor(t.inProgress.Position, t.inProgress.PercentWatched) || !t.inProgress.UpdatedAt.After(matchingWatchedAt))
				inProgressWatchedAt = matchingWatchedAt
				if !inProgressAlreadyWatched && haveFurthest &&
					compareEpisodeOrder(furthestSeason, furthestEpisode, inProgressSeason, inProgressEpisode) > 0 &&
					!t.inProgress.UpdatedAt.After(latestWatchedAt) {
					inProgressStaleBehindLatestWatched = true
				}
				// Cross-provider check: the episode may be marked watched under
				// rows keyed by another provider that share no external IDs with
				// this group. Use the metadata-enriched provider IDs to look it
				// up in the global watched-episode index.
				if !inProgressAlreadyWatched {
					groupProviderIDs := make(map[string]string, 4)
					for k, v := range t.inProgress.ExternalIDs {
						switch k {
						case "imdb", "tmdb", "tvdb":
							groupProviderIDs[k] = v
						}
					}
					if seriesErr == nil && seriesDetails != nil {
						if seriesDetails.Title.IMDBID != "" {
							groupProviderIDs["imdb"] = seriesDetails.Title.IMDBID
						}
						if seriesDetails.Title.TMDBID > 0 {
							groupProviderIDs["tmdb"] = fmt.Sprintf("%d", seriesDetails.Title.TMDBID)
						}
						if seriesDetails.Title.TVDBID > 0 {
							groupProviderIDs["tvdb"] = fmt.Sprintf("%d", seriesDetails.Title.TVDBID)
						}
					}
					episodeKeys := []string{episodeKey(inProgressSeason, inProgressEpisode)}
					if rawKey := episodeKey(t.inProgress.SeasonNumber, t.inProgress.EpisodeNumber); rawKey != episodeKeys[0] {
						episodeKeys = append(episodeKeys, rawKey)
					}
					for idType, idValue := range groupProviderIDs {
						if idValue == "" {
							continue
						}
						for _, ek := range episodeKeys {
							if watchedAt, ok := watchedEpisodeProviderTokens[idType+":"+strings.ToLower(idValue)+":"+ek]; ok &&
								(!MeetsResumeFloor(t.inProgress.Position, t.inProgress.PercentWatched) || !t.inProgress.UpdatedAt.After(watchedAt)) {
								inProgressAlreadyWatched = true
								inProgressWatchedAt = watchedAt
								break
							}
						}
						if inProgressAlreadyWatched {
							break
						}
					}
				}
				if inProgressAlreadyWatched || inProgressStaleBehindLatestWatched {
					// Clean up stale progress entries for this series: progress on an
					// episode that is no newer than that episode's watched event, and
					// the in-progress entry when it is stale. Progress newer than the
					// watched event is a rewatch and is kept, however short, so the
					// episode can still resume from details.
					watchedEps := make(map[string]time.Time, len(t.episodes))
					for _, ep := range t.episodes {
						key := episodeKey(ep.SeasonNumber, ep.EpisodeNumber)
						if at := watchHistoryActivityTime(ep); at.After(watchedEps[key]) {
							watchedEps[key] = at
						}
					}
					seriesExtIDs := t.inProgress.ExternalIDs
					targetSeason := t.inProgress.SeasonNumber
					targetEpisode := t.inProgress.EpisodeNumber
					removeTarget := inProgressStaleBehindLatestWatched || !t.inProgress.UpdatedAt.After(inProgressWatchedAt)
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
							watchedAt, watched := watchedEps[episodeKey(prog.SeasonNumber, prog.EpisodeNumber)]
							isTarget := prog.SeasonNumber == targetSeason && prog.EpisodeNumber == targetEpisode
							if (watched && !prog.UpdatedAt.After(watchedAt)) || (isTarget && removeTarget) {
								log.Printf("[history] cleaning up stale progress %s (episode already watched)", key)
								s.removePlaybackProgressEntryLocked(userID, perUser, key, prog)
								cleaned++
							}
						}
						if cleaned > 0 {
							_ = s.savePlaybackProgressLocked()
							s.invalidateContinueWatchingLocked(userID)
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
				if t.activityAt.After(updatedAt) {
					updatedAt = t.activityAt
				}

				// For in-progress, use the in-progress episode as both last and next
				// Copy ExternalIDs to avoid concurrent map writes from parallel goroutines
				extIDs := make(map[string]string, len(t.inProgress.ExternalIDs))
				for k, v := range t.inProgress.ExternalIDs {
					extIDs[k] = v
				}
				state = models.SeriesWatchState{
					SeriesID:       t.seriesID,
					SeriesTitle:    t.inProgress.SeriesName,
					Year:           t.inProgress.Year,
					ExternalIDs:    extIDs,
					UpdatedAt:      updatedAt,
					LastWatched:    *nextEpisode,
					NextEpisode:    nextEpisode,
					PercentWatched: t.inProgress.PercentWatched,
					ResumePercent:  t.inProgress.PercentWatched,
				}

				// Use full series details for poster, backdrop, IDs, and episode counts
				if seriesErr == nil && seriesDetails != nil {
					// Add overview from metadata
					if seriesDetails.Title.Overview != "" {
						state.Overview = seriesDetails.Title.Overview
					}
					enrichSeriesWatchStateArtwork(&state, &seriesDetails.Title)

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
					state.TotalEpisodeCount = releasedEpisodeSet(seriesDetails).Len()
					state.Status = seriesDetails.Title.Status
					state.LifecycleStatus = seriesDetails.Title.LifecycleStatus
					state.WatchedEpisodeCount = lifetimeWatchedCount
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

				// Series details (with all episodes) are required to find next unwatched
				if seriesErr != nil {
					// Skip this series if metadata unavailable
					return
				}

				// Find next unwatched episode
				nextEpisode = s.findNextUnwatchedEpisode(seriesDetails, mostRecentEpisode, episodes)
				if onlyInProgress && seriesDetails != nil {
					s.enrichContinueWatchingAirtime(ctx, seriesDetails.Title, mostRecentEpisode.ExternalIDs, nextEpisode)
				}
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
				if t.activityAt.After(state.UpdatedAt) {
					state.UpdatedAt = t.activityAt
				}
				if releaseAt, ok := continueWatchingReleaseTime(nextEpisode); ok && releaseAt.After(state.UpdatedAt) {
					state.SortAt = releaseAt
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
					enrichSeriesWatchStateArtwork(&state, &seriesDetails.Title)

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
					state.TotalEpisodeCount = releasedEpisodeSet(seriesDetails).Len()
					state.Status = seriesDetails.Title.Status
					state.LifecycleStatus = seriesDetails.Title.LifecycleStatus
					state.WatchedEpisodeCount = lifetimeWatchedCount
				}
			} else {
				// No episodes and no in-progress (shouldn't happen)
				return
			}
			if state.Status == "" {
				state.Status = models.SeriesReleaseStatusReleased
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
				enrichSeriesWatchStateArtwork(&movieState, movieDetails)

				// Use metadata overview (the key fix - populate overview from metadata)
				if movieDetails.Overview != "" {
					movieState.Overview = movieDetails.Overview
					movieState.LastWatched.Overview = movieDetails.Overview
				}
				movieState.Status = movieDetails.Status
				movieState.LifecycleStatus = movieDetails.LifecycleStatus
				movieState.Theatrical = movieDetails.Theatrical
				movieState.HomeRelease = movieDetails.HomeRelease

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
	if len(identityRepairs) > 0 {
		changed, err := s.applyEpisodeSeriesIdentityRepairs(userID, identityRepairs)
		if err != nil {
			return nil, err
		}
		if changed {
			// Re-group using the repaired stored identities before selecting next
			// episodes; merging finished shelf cards loses watched-episode sets.
			return s.buildSeriesStatesWithIdentityRepair(ctx, userID, onlyInProgress, false)
		}
	}

	continueWatching = dedupeContinueWatchingEntries(continueWatching)

	// Late hidden-marker suppression: now that states carry metadata-enriched
	// external IDs, drop any state the user hid even when the hide marker and
	// the surviving rows are keyed under different providers and share no
	// stored external IDs ("dismissed but came back" under the other ID form).
	// Newer activity beats the marker so resuming playback still unhides.
	if len(hiddenMarkers) > 0 && len(continueWatching) > 0 {
		type resolvedMarker struct {
			movie     mediaidentity.Identity
			series    mediaidentity.Identity
			isMovie   bool
			updatedAt time.Time
		}
		resolved := make([]resolvedMarker, 0, len(hiddenMarkers))
		for _, m := range hiddenMarkers {
			markerID := strings.TrimSpace(m.seriesID)
			if markerID == "" {
				markerID = strings.TrimSpace(m.itemID)
			}
			if markerID == "" {
				continue
			}
			resolved = append(resolved, resolvedMarker{
				movie:     mediaidentity.Resolve(mediaidentity.Input{MediaType: "movie", ID: markerID, ExternalIDs: m.externalIDs}),
				series:    mediaidentity.Resolve(mediaidentity.Input{MediaType: "series", ID: markerID, ExternalIDs: m.externalIDs}),
				isMovie:   strings.EqualFold(m.mediaType, "movie"),
				updatedAt: m.updatedAt,
			})
		}
		filtered := make([]models.SeriesWatchState, 0, len(continueWatching))
		for _, state := range continueWatching {
			suppressed := false
			stateIsMovie := state.NextEpisode == nil && len(state.WatchedEpisodes) == 0 && state.LastWatched.EpisodeNumber == 0
			for _, m := range resolved {
				if state.UpdatedAt.After(m.updatedAt) {
					continue
				}
				if m.isMovie != stateIsMovie {
					continue
				}
				target := m.series
				mediaType := "series"
				if m.isMovie {
					target = m.movie
					mediaType = "movie"
				}
				current := mediaidentity.Resolve(mediaidentity.Input{
					MediaType:   mediaType,
					ID:          state.SeriesID,
					ExternalIDs: state.ExternalIDs,
				})
				if identitiesReferToSameTitle(current, target) {
					suppressed = true
					break
				}
			}
			if suppressed {
				log.Printf("[history] continue-watching: suppressed %q (%s) via hidden marker", state.SeriesTitle, state.SeriesID)
				continue
			}
			filtered = append(filtered, state)
		}
		continueWatching = filtered
	}

	// Sort by genuine activity or, for an available next episode, the episode's
	// release time. Release ordering is intentionally durable: the frontend owns
	// the separate 72-hour "New" badge window, while the shelf position drifts
	// down naturally as newer activity and releases arrive.
	sort.Slice(continueWatching, func(i, j int) bool {
		iSortAt := continueWatchingSortTime(continueWatching[i])
		jSortAt := continueWatchingSortTime(continueWatching[j])
		if iSortAt.Equal(jSortAt) {
			return continueWatching[i].SeriesID < continueWatching[j].SeriesID
		}
		return iSortAt.After(jSortAt)
	})

	return continueWatching, nil
}

func continueWatchingSortTime(state models.SeriesWatchState) time.Time {
	if state.SortAt.After(state.UpdatedAt) {
		return state.SortAt
	}
	return state.UpdatedAt
}

func continueWatchingReleaseTime(nextEpisode *models.EpisodeReference) (time.Time, bool) {
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

func watchedEpisodesAfterManualUnwatch(watchedEpisodes, history []models.WatchHistoryItem, numbering *episodeNumberingIndex) []models.WatchHistoryItem {
	if len(watchedEpisodes) == 0 || len(history) == 0 {
		return watchedEpisodes
	}

	latestUnwatched := make(map[string]time.Time)
	for _, item := range history {
		if item.Watched {
			continue
		}
		if hasContradictorySeriesExternalIDs(item.SeriesID, item.ItemID, item.ExternalIDs) {
			continue
		}
		season, episode := numbering.canonical(item.SeasonNumber, item.EpisodeNumber)
		key := episodeKey(season, episode)
		activityAt := watchHistoryActivityTime(item)
		if activityAt.After(latestUnwatched[key]) {
			latestUnwatched[key] = activityAt
		}
	}
	if len(latestUnwatched) == 0 {
		return watchedEpisodes
	}

	filtered := make([]models.WatchHistoryItem, 0, len(watchedEpisodes))
	for _, item := range watchedEpisodes {
		season, episode := numbering.canonical(item.SeasonNumber, item.EpisodeNumber)
		key := episodeKey(season, episode)
		if unwatchedAt, ok := latestUnwatched[key]; ok && !watchHistoryActivityTime(item).After(unwatchedAt) {
			continue
		}
		item.SeasonNumber = season
		item.EpisodeNumber = episode
		filtered = append(filtered, item)
	}
	return filtered
}

func watchHistoryActivityTime(item models.WatchHistoryItem) time.Time {
	if item.UpdatedAt.After(item.WatchedAt) {
		return item.UpdatedAt
	}
	return item.WatchedAt
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

	// Check cache validity (negative entries replay the recorded failure so a
	// broken series does not hit the provider twice on every build).
	if exists && time.Now().Before(cached.expiresAt) {
		if cached.err != nil {
			return nil, cached.err
		}
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

	// Prefer the lite path for speed, but fall back to full details when the
	// provider cannot represent a TMDB-only series in TVDB. Continue Watching
	// still needs the full result to populate artwork and episode metadata.
	details, err := metadataSvc.SeriesDetailsLite(ctx, query)
	if err != nil {
		liteErr := err
		log.Printf("[history] series metadata lite lookup failed for %q, falling back to full details: %v", seriesID, liteErr)
		details, err = metadataSvc.SeriesDetails(ctx, query)
		if err != nil {
			lookupErr := fmt.Errorf("series metadata lite lookup failed: %v; full lookup failed: %w", liteErr, err)
			// Don't remember failures caused by the caller's own cancellation or
			// deadline; those say nothing about the series.
			if ctx.Err() == nil {
				now := time.Now()
				s.mu.Lock()
				s.metadataCache[seriesID] = &cachedSeriesMetadata{
					err:       lookupErr,
					cachedAt:  now,
					expiresAt: now.Add(seriesMetadataNegativeCacheTTL),
				}
				s.mu.Unlock()
			}
			return nil, lookupErr
		}
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

	// History can legitimately contain both season-relative and absolute-numbered
	// rows for the same episode (for example One Piece S23E18 and S23E1173).
	// Keep the source rows intact, but canonicalize their numbering for the
	// derived on-deck calculation so an absolute-numbered latest row can still be
	// located in season metadata.
	numbering := newEpisodeNumberingIndex(seriesDetails)
	lastWatchedSeason, lastWatchedEpisode := numbering.canonical(lastWatched.SeasonNumber, lastWatched.EpisodeNumber)
	if lastWatchedSeason <= 0 {
		// Imported specials must not seed a queue of more specials or restart
		// the regular series. Continue from the newest regular watched episode.
		var regular *models.WatchHistoryItem
		for i := range watchedEpisodes {
			ep := &watchedEpisodes[i]
			if ep.SeasonNumber > 0 && (regular == nil || ep.WatchedAt.After(regular.WatchedAt)) {
				regular = ep
			}
		}
		if regular == nil {
			return nil
		}
		lastWatchedSeason, lastWatchedEpisode = numbering.canonical(regular.SeasonNumber, regular.EpisodeNumber)
	}

	// Build set of watched episodes for O(1) lookup
	watchedSet := make(map[string]bool)
	for _, ep := range watchedEpisodes {
		if ep.SeasonNumber <= 0 {
			continue
		}
		season, episode := numbering.canonical(ep.SeasonNumber, ep.EpisodeNumber)
		key := episodeKey(season, episode)
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
			// Specials remain playable/resumable, but are not automatic next-up
			// candidates, including when season containers use mixed numbering.
			if ep.SeasonNumber <= 0 {
				continue
			}
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
		if ep.season == lastWatchedSeason && ep.episode == lastWatchedEpisode {
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
				Numbering:             ep.details.Numbering,
				SeasonNumber:          ep.details.SeasonNumber,
				EpisodeNumber:         ep.details.EpisodeNumber,
				AbsoluteEpisodeNumber: ep.details.AbsoluteEpisodeNumber,
				EpisodeID:             ep.details.ID,
				Title:                 ep.details.Name,
				Overview:              ep.details.Overview,
				RuntimeMinutes:        ep.details.Runtime,
				AirDate:               ep.details.AiredDate,
				Image:                 ep.details.Image,
			}
			if ep.details.AiredDate != "" {
				utc := calendar.ParseAirDateTime(ep.details.AiredDate, seriesDetails.Title.AirsTime, seriesDetails.Title.AirsTimezone)
				if !utc.IsZero() {
					ref.AirDateTimeUTC = utc.Format(time.RFC3339)
					ref.AirTimeEstimated = seriesDetails.Title.AirsTime == "" || seriesDetails.Title.AirsTimezone == "" || utc.Format("15:04:05") == "23:59:59"
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

	if episodeRef.Numbering != nil && !models.SameEpisodeNumbering(episodeRef.Numbering, seriesDetails.Numbering) {
		return
	}
	// Find the matching episode in metadata
	for _, season := range seriesDetails.Seasons {
		if season.Number == episodeRef.SeasonNumber {
			for _, episode := range season.Episodes {
				if episode.EpisodeNumber == episodeRef.EpisodeNumber {
					applyEpisodeMetadata(episodeRef, episode)
					return
				}
			}
		}
	}

	if episodeRef.AbsoluteEpisodeNumber > 0 {
		if episode, ok := findEpisodeByAbsoluteNumber(seriesDetails, episodeRef.AbsoluteEpisodeNumber); ok {
			applyEpisodeMetadata(episodeRef, episode)
		}
		return
	}
	if episodeRef.EpisodeNumber > 0 {
		if episode, ok := findEpisodeByAbsoluteNumber(seriesDetails, episodeRef.EpisodeNumber); ok {
			log.Printf("[history] normalized legacy absolute episode S%02dE%02d to S%02dE%02d (abs: %d)",
				episodeRef.SeasonNumber, episodeRef.EpisodeNumber, episode.SeasonNumber, episode.EpisodeNumber, episode.AbsoluteEpisodeNumber)
			applyEpisodeMetadata(episodeRef, episode)
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
	s.bumpAllWatchStateRevisionsLocked()

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

	recordIDs := func(seriesID string, extIDs map[string]string, countOverride ...int) {
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
		if len(countOverride) > 0 {
			count = countOverride[0]
		}
		if existing, ok := seriesExternalIDCount[seriesID]; !ok || count > existing {
			seriesExternalIDCount[seriesID] = count
		}
	}

	recordSeriesAliases := func(seriesID, itemID string, extIDs map[string]string) {
		canonicalExtIDs := canonicalSeriesExternalIDs(seriesID, itemID, extIDs)
		canonicalSeriesID := mediaidentity.Resolve(mediaidentity.Input{
			MediaType:   "series",
			ID:          seriesID,
			ExternalIDs: canonicalExtIDs,
		}).ID
		if canonicalSeriesID == "" {
			canonicalSeriesID = seriesID
		}
		if seriesID != canonicalSeriesID {
			recordIDs(seriesID, canonicalExtIDs, 0)
		} else {
			recordIDs(seriesID, canonicalExtIDs)
		}
		if canonicalExtIDs == nil {
			return
		}
		recordIDs(canonicalSeriesID, canonicalExtIDs)
		if id := strings.TrimSpace(canonicalExtIDs["tmdb"]); id != "" {
			recordIDs("tmdb:tv:"+id, canonicalExtIDs)
		}
		if id := strings.TrimSpace(canonicalExtIDs["tvdb"]); id != "" {
			recordIDs("tvdb:series:"+id, canonicalExtIDs)
		}
		if id := strings.TrimSpace(canonicalExtIDs["imdb"]); id != "" {
			recordIDs(id, canonicalExtIDs)
		}
	}

	for _, item := range items {
		if item.MediaType == "episode" && item.SeriesID != "" {
			recordSeriesAliases(item.SeriesID, item.ItemID, item.ExternalIDs)
		}
	}
	for _, prog := range progressItems {
		if isSeriesLevelPlaybackMarker(prog) {
			continue
		}
		if (prog.MediaType == "episode" || prog.SeasonNumber > 0) && prog.SeriesID != "" {
			recordSeriesAliases(prog.SeriesID, prog.ItemID, prog.ExternalIDs)
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
		// Pick canonical: prefer the one with the most external IDs, then the
		// most display-compatible provider when aliases have equivalent data.
		best := unique[0]
		for _, id := range unique[1:] {
			if seriesExternalIDCount[id] > seriesExternalIDCount[best] ||
				(seriesExternalIDCount[id] == seriesExternalIDCount[best] && preferProgressID(id, best)) {
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

func canonicalSeriesExternalIDs(seriesID, itemID string, extIDs map[string]string) map[string]string {
	return mediaidentity.CanonicalSeriesExternalIDs(seriesID, itemID, extIDs)
}

func seriesProviderAndID(seriesID string) (string, string) {
	return mediaidentity.SeriesProviderAndID(seriesID)
}

func inferSeriesIDFromEpisodeItemID(itemID string) string {
	return mediaidentity.InferSeriesIDFromEpisodeItemID(itemID)
}

func hasContradictorySeriesExternalIDs(seriesID, itemID string, extIDs map[string]string) bool {
	return mediaidentity.HasContradictorySeriesExternalIDs(seriesID, itemID, extIDs)
}

func enrichSeriesWatchStateArtwork(state *models.SeriesWatchState, title *models.Title) {
	if state == nil || title == nil {
		return
	}
	if title.Poster != nil {
		state.PosterURL = title.Poster.URL
	}
	if title.TextPoster != nil {
		state.TextPosterURL = title.TextPoster.URL
	}
	if title.Backdrop != nil {
		state.BackdropURL = title.Backdrop.URL
	}
	if title.TextBackdrop != nil {
		state.TextBackdropURL = title.TextBackdrop.URL
	}
	if title.Logo != nil {
		state.LogoURL = title.Logo.URL
	}

	seen := make(map[string]struct{})
	state.BackdropURLs = state.BackdropURLs[:0]
	for _, image := range title.Backdrops {
		if image.URL == "" {
			continue
		}
		if _, ok := seen[image.URL]; ok {
			continue
		}
		seen[image.URL] = struct{}{}
		state.BackdropURLs = append(state.BackdropURLs, image.URL)
	}
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
				applyEpisodeMetadata(ref, ep)
				return
			}
		}
	}
	if ref.AbsoluteEpisodeNumber > 0 {
		if ep, ok := findEpisodeByAbsoluteNumber(details, ref.AbsoluteEpisodeNumber); ok {
			applyEpisodeMetadata(ref, ep)
		}
		return
	}
	if ref.EpisodeNumber > 0 {
		if ep, ok := findEpisodeByAbsoluteNumber(details, ref.EpisodeNumber); ok && legacyAbsoluteRemapAllowed(ref.SeasonNumber, ep.SeasonNumber) {
			log.Printf("[history] normalized legacy absolute episode S%02dE%02d to S%02dE%02d (abs: %d)",
				ref.SeasonNumber, ref.EpisodeNumber, ep.SeasonNumber, ep.EpisodeNumber, ep.AbsoluteEpisodeNumber)
			applyEpisodeMetadata(ref, ep)
		}
	}
}

// episodeNumberingIndex reconciles episodes that were recorded under a series'
// absolute episode numbering back to canonical season-relative numbers. This is
// common for long-running anime (e.g. One Piece) where some clients/scrobbles
// record progress as S23E1163 while watch history uses S23E08.
type episodeNumberingIndex struct {
	valid    map[string]struct{} // keys of real season-relative episodes
	absolute map[int][2]int      // absolute number -> {season, episode}
}

func newEpisodeNumberingIndex(details *models.SeriesDetails) *episodeNumberingIndex {
	idx := &episodeNumberingIndex{
		valid:    make(map[string]struct{}),
		absolute: make(map[int][2]int),
	}
	if details == nil {
		return idx
	}
	for _, season := range details.Seasons {
		for _, ep := range season.Episodes {
			idx.valid[episodeKey(ep.SeasonNumber, ep.EpisodeNumber)] = struct{}{}
			if ep.AbsoluteEpisodeNumber > 0 {
				idx.absolute[ep.AbsoluteEpisodeNumber] = [2]int{ep.SeasonNumber, ep.EpisodeNumber}
			}
		}
	}
	return idx
}

// canonical returns the season-relative season/episode for the given numbers. It
// returns the input unchanged when it already references a real episode, and only
// remaps when the episode number matches a known absolute episode number.
func (idx *episodeNumberingIndex) canonical(season, episode int) (int, int) {
	if idx == nil || season <= 0 || episode <= 0 {
		return season, episode
	}
	if _, ok := idx.valid[episodeKey(season, episode)]; ok {
		return season, episode
	}
	if mapped, ok := idx.absolute[episode]; ok && legacyAbsoluteRemapAllowed(season, mapped[0]) {
		return mapped[0], mapped[1]
	}
	return season, episode
}

// legacyAbsoluteRemapAllowed rejects reading a season-relative episode number
// as an absolute number when that would move it to an earlier season. Absolute
// numbers only grow across seasons, so a legacy S23E1163 maps forward or within
// S23; an S17E03 missing from stale or truncated metadata is not absolute #3.
func legacyAbsoluteRemapAllowed(season, mappedSeason int) bool {
	return season <= 0 || mappedSeason >= season
}

func findEpisodeByAbsoluteNumber(details *models.SeriesDetails, absoluteEpisodeNumber int) (models.SeriesEpisode, bool) {
	if details == nil || absoluteEpisodeNumber <= 0 {
		return models.SeriesEpisode{}, false
	}
	for _, season := range details.Seasons {
		for _, ep := range season.Episodes {
			if ep.AbsoluteEpisodeNumber == absoluteEpisodeNumber {
				return ep, true
			}
		}
	}
	return models.SeriesEpisode{}, false
}

func applyEpisodeMetadata(ref *models.EpisodeReference, ep models.SeriesEpisode) {
	if ref == nil {
		return
	}
	ref.Numbering = ep.Numbering
	ref.SeasonNumber = ep.SeasonNumber
	ref.EpisodeNumber = ep.EpisodeNumber
	if ep.AbsoluteEpisodeNumber > 0 {
		ref.AbsoluteEpisodeNumber = ep.AbsoluteEpisodeNumber
	}
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
	if ep.TVDBID > 0 {
		ref.TvdbID = fmt.Sprintf("%d", ep.TVDBID)
	}
	if ref.EpisodeID == "" {
		ref.EpisodeID = ep.ID
	}
}

// releasedEpisodeSet is the set of aired regular-season episodes that make up
// a series' total episode count.
func releasedEpisodeSet(seriesDetails *models.SeriesDetails) *models.ReleasedEpisodeSet {
	if seriesDetails == nil {
		return models.NewReleasedEpisodeSet(nil)
	}
	var released []models.ReleasedEpisode
	add := func(ep models.SeriesEpisode) {
		released = append(released, models.ReleasedEpisode{
			Season: ep.SeasonNumber, Episode: ep.EpisodeNumber, Absolute: ep.AbsoluteEpisodeNumber,
		})
	}
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
						add(ep)
					}
				} else {
					// If we can't parse the date, assume it's released
					add(ep)
				}
			} else if latestReleasedSeason == 0 && latestReleasedEpisode == 0 ||
				compareEpisodeOrder(ep.SeasonNumber, ep.EpisodeNumber, latestReleasedSeason, latestReleasedEpisode) <= 0 {
				// Older catalog entries occasionally lack dates; keep counting those.
				// But do not count undated placeholder episodes that appear after the
				// latest known released episode.
				add(ep)
			}
		}
	}
	return models.NewReleasedEpisodeSet(released)
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

// countLifetimeWatchedEpisodes counts distinct regular-season episodes watched
// across the series' whole history plus the windowed episode list (which
// already carries canonical numbering), skipping corrupted gaps that were
// promoted to watched only for next-episode selection.
//
// When released is non-empty, only watched episodes in it count: the result is
// subtracted from released.Len() for the unwatched badge, so a history row for
// an episode the catalog does not list must not reduce it.
func countLifetimeWatchedEpisodes(lifetime, windowed []models.WatchHistoryItem, promoted map[string]struct{}, numbering *episodeNumberingIndex, released *models.ReleasedEpisodeSet) int {
	seen := make(map[string]struct{}, len(lifetime))
	add := func(ep models.WatchHistoryItem) {
		season, episode := numbering.canonical(ep.SeasonNumber, ep.EpisodeNumber)
		if season <= 0 || episode <= 0 {
			return
		}
		if released.Len() > 0 && !released.Contains(season, episode) {
			return
		}
		key := episodeKey(season, episode)
		if _, skip := promoted[key]; skip {
			return
		}
		seen[key] = struct{}{}
	}
	for _, ep := range lifetime {
		add(ep)
	}
	for _, ep := range windowed {
		if ep.Watched {
			add(ep)
		}
	}
	return len(seen)
}

func countWatchedEpisodesExcluding(watchedEpisodes map[string]models.EpisodeReference, excluded map[string]struct{}) int {
	if len(excluded) == 0 {
		return countWatchedEpisodes(watchedEpisodes)
	}
	count := 0
	for _, ep := range watchedEpisodes {
		if ep.SeasonNumber <= 0 {
			continue
		}
		if _, skip := excluded[episodeKey(ep.SeasonNumber, ep.EpisodeNumber)]; skip {
			continue
		}
		count++
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
// ListWatchHistoryPaginated pages a user's watch history. With realOnly it
// leaves out items that were only marked watched (see IsRealWatch).
func (s *Service) ListWatchHistoryPaginated(userID string, page, pageSize int, mediaTypeFilter string, realOnly bool) (*WatchHistoryPage, error) {
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

	// Collapse the same watch recorded under multiple identifier forms (absolute
	// vs seasonal episode numbering, tmdb- vs tvdb-form IDs) so it appears once.
	items = DedupeWatchHistory(items)
	if realOnly {
		items = FilterRealWatches(items)
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

// watchHistoryDedupKey identifies a watch independent of the identifier form it
// was recorded under. The same episode can be stored under both absolute and
// seasonal episode numbering (e.g. S23E1167 and S23E12) and under tmdb- vs
// tvdb-form IDs; keying on the human identity (series + season + episode name,
// or title + year) lets those collapse to a single entry.
func watchHistoryDedupKey(item models.WatchHistoryItem) string {
	if strings.EqualFold(strings.TrimSpace(item.MediaType), "episode") {
		series := strings.ToLower(strings.TrimSpace(item.SeriesName))
		episode := strings.ToLower(strings.TrimSpace(item.Name))
		if series != "" && episode != "" {
			return "ep|" + series + "|s" + strconv.Itoa(item.SeasonNumber) + "|" + episode
		}
		// No episode name to bridge numbering forms on — fall back to the stored
		// numbering, which still keeps genuinely distinct episodes apart.
		if series != "" {
			return "ep|" + series + "|s" + strconv.Itoa(item.SeasonNumber) + "|e" + strconv.Itoa(item.EpisodeNumber)
		}
	}
	title := strings.ToLower(strings.TrimSpace(item.Name))
	if title == "" {
		title = strings.ToLower(strings.TrimSpace(item.SeriesName))
	}
	return strings.ToLower(strings.TrimSpace(item.MediaType)) + "|" + title + "|y" + strconv.Itoa(item.Year)
}

// watchHistoryPreferred reports whether candidate should replace current as the
// representative of a dedup group. The seasonal episode form (the smaller
// episode number) is preferred over the absolute one — so S23E12 is kept and the
// duplicate S23E1167 hidden; ties fall back to the most-recently watched entry.
func watchHistoryPreferred(candidate, current models.WatchHistoryItem) bool {
	if strings.EqualFold(candidate.MediaType, "episode") && strings.EqualFold(current.MediaType, "episode") &&
		candidate.EpisodeNumber != current.EpisodeNumber {
		return candidate.EpisodeNumber < current.EpisodeNumber
	}
	return candidate.WatchedAt.After(current.WatchedAt)
}

// FilterRealWatches keeps the items that reflect a real viewing, dropping
// ones only marked watched by hand. Run it after DedupeWatchHistory, which
// carries a played source across duplicate rows.
func FilterRealWatches(items []models.WatchHistoryItem) []models.WatchHistoryItem {
	out := items[:0:0]
	for _, item := range items {
		if item.IsRealWatch() {
			out = append(out, item)
		}
	}
	return out
}

// playbackWatchedSecondsLocked returns the playback time accumulated in the
// progress entries for update's item, which marking it watched is about to
// clear. Watch history keeps that time so a mark made after real viewing
// (stopping at the credits, say) still counts as watched.
func (s *Service) playbackWatchedSecondsLocked(userID string, update models.WatchHistoryUpdate) float64 {
	perUser := s.playbackProgress[userID]
	if len(perUser) == 0 {
		return 0
	}
	identity := watchHistoryUpdateIdentity(update)
	seconds := 0.0
	for _, entry := range perUser {
		if entry.WatchedSeconds > seconds && playbackProgressMatchesIdentity(entry, identity) {
			seconds = entry.WatchedSeconds
		}
	}
	return seconds
}

func keepPlayedSeconds(item *models.WatchHistoryItem, seconds float64) {
	if seconds > item.WatchedSeconds {
		item.WatchedSeconds = seconds
	}
}

// watchSourceOrMarked defaults an update's source: callers that do not say
// otherwise are clients marking an item by hand.
func watchSourceOrMarked(source string) string {
	if source == "" {
		return models.WatchSourceMarked
	}
	return source
}

// DedupeWatchHistory collapses entries that represent the same watch recorded
// under multiple identifier forms (absolute vs seasonal episode numbering,
// tmdb- vs tvdb-form IDs) into one, keeping the seasonal episode form as the
// representative. Callers pass a single user's items; first-seen order is
// preserved. See watchHistoryDedupKey / watchHistoryPreferred.
func DedupeWatchHistory(items []models.WatchHistoryItem) []models.WatchHistoryItem {
	if len(items) <= 1 {
		return items
	}
	out := make([]models.WatchHistoryItem, 0, len(items))
	index := make(map[string]int, len(items))
	for _, item := range items {
		key := watchHistoryDedupKey(item)
		if i, ok := index[key]; ok {
			// One viewing recorded under several ID forms: if any form was
			// really played, so was the survivor.
			source := ""
			if out[i].Watched {
				source = out[i].Source
			}
			if item.Watched {
				source = models.MergeWatchSource(true, source, item.Source)
			}
			if watchHistoryPreferred(item, out[i]) {
				out[i] = item
			}
			if out[i].Watched {
				out[i].Source = source
			}
			continue
		}
		index[key] = len(out)
		out = append(out, item)
	}
	return out
}

// GetWatchHistoryItem returns a specific watch history item.
func (s *Service) GetWatchHistoryItem(userID, mediaType, itemID string) (*models.WatchHistoryItem, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUserIDRequired
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	identity := mediaidentity.Resolve(mediaidentity.Input{MediaType: mediaType, ID: itemID})
	if perUser, ok := s.watchHistory[userID]; ok {
		for _, key := range identity.CandidateKeys {
			if item, ok := perUser[key]; ok {
				return &item, nil
			}
		}
		for _, item := range perUser {
			if watchHistoryItemMatchesIdentity(item, identity) {
				return &item, nil
			}
		}
		if item, ok := perUser[identity.Key]; ok {
			return &item, nil
		}
	}

	return nil, nil
}

// IsMovieWatched reports whether a movie is marked watched, matching rows stored
// under any equivalent ID form (TMDB, IMDB, Trakt/Simkl imports) via its external IDs.
func (s *Service) IsMovieWatched(userID, itemID string, externalIDs map[string]string) bool {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	perUser, ok := s.watchHistory[userID]
	if !ok {
		return false
	}
	identity := watchHistoryUpdateIdentity(models.WatchHistoryUpdate{MediaType: "movie", ItemID: itemID, ExternalIDs: externalIDs})
	for _, key := range identity.CandidateKeys {
		if item, ok := perUser[key]; ok {
			return item.Watched
		}
	}
	for _, item := range perUser {
		if watchHistoryItemMatchesIdentity(item, identity) {
			return item.Watched
		}
	}
	return false
}

// ToggleWatched toggles the watched status for an item (movie, series, or episode).
func (s *Service) ToggleWatched(userID string, update models.WatchHistoryUpdate) (models.WatchHistoryItem, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return models.WatchHistoryItem{}, ErrUserIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.bumpWatchStateRevisionLocked(userID)

	update = normalizeWatchHistoryUpdate(update)
	if !isAddressableEpisodeUpdate(update.MediaType, update.ItemID, update.EpisodeNumber, update.ExternalIDs) {
		return models.WatchHistoryItem{}, ErrEpisodeNotAddressable
	}
	perUser := s.ensureWatchHistoryUserLocked(userID)

	// Normalize itemID to lowercase for consistent key matching
	identity := watchHistoryUpdateIdentity(update)
	item, exists := takeWatchHistoryItemLocked(perUser, identity)
	key, normalizedItemID := canonicalWatchHistorySurvivor(item, exists, identity, &update)

	now := time.Now().UTC()
	wasAlreadyWatched := exists && item.Watched
	if !exists {
		// Create new item marked as watched
		item = models.WatchHistoryItem{
			ID:        key,
			MediaType: strings.ToLower(update.MediaType),
			ItemID:    normalizedItemID,
			Watched:   true,
			WatchedAt: now,
			UpdatedAt: now,
			Source:    models.WatchSourceMarked,
		}
	} else {
		// Toggle existing item
		item.Watched = !item.Watched
		if item.Watched {
			item.WatchedAt = now
			item.Source = models.WatchSourceMarked
			keepPlayedSeconds(&item, s.playbackWatchedSecondsLocked(userID, update))
		}
		item.UpdatedAt = now
	}
	// Keep the row coherent with the key it is stored under (see UpdateWatchHistory).
	item.ID = key
	item.ItemID = normalizedItemID
	item.MediaType = strings.ToLower(update.MediaType)

	// Update metadata if provided
	if update.Name != "" {
		item.Name = update.Name
	}
	if update.Year > 0 {
		item.Year = update.Year
	}
	mergeWatchHistoryExternalIDs(&item, update.ExternalIDs)

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
		if s.clearProgressByExternalIDMatchLocked(userID, item.SeriesID, item.SeasonNumber, item.EpisodeNumber, item.ExternalIDs) {
			progressCleared = true
		}
	}

	if progressCleared {
		if err := s.savePlaybackProgressLocked(); err != nil {
			return models.WatchHistoryItem{}, err
		}
	}

	// Invalidate continue watching cache for this user
	s.invalidateContinueWatchingLocked(userID)

	// Get scrobbler reference while holding lock (safe since we have write lock)
	scrobbler := s.traktScrobbler

	// Scrobble to Trakt if now marked as watched
	// Note: doScrobble is safe to call while holding lock since it spawns goroutines
	if item.Watched {
		s.doScrobble(scrobbler, userID, item)
	} else if wasAlreadyWatched {
		s.doUnscrobble(scrobbler, userID, item)
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
	s.bumpWatchStateRevisionLocked(userID)

	update = normalizeWatchHistoryUpdate(update)
	if !isAddressableEpisodeUpdate(update.MediaType, update.ItemID, update.EpisodeNumber, update.ExternalIDs) {
		return models.WatchHistoryItem{}, ErrEpisodeNotAddressable
	}
	perUser := s.ensureWatchHistoryUserLocked(userID)

	// Normalize itemID to lowercase for consistent key matching
	identity := watchHistoryUpdateIdentity(update)
	item, exists := takeWatchHistoryItemLocked(perUser, identity)
	key, normalizedItemID := canonicalWatchHistorySurvivor(item, exists, identity, &update)

	now := time.Now().UTC()
	if !exists {
		item = models.WatchHistoryItem{
			ID:        key,
			MediaType: strings.ToLower(update.MediaType),
			ItemID:    normalizedItemID,
			Watched:   false,
		}
	}
	// Keep the row coherent with the key it is stored under: a stale ID/ItemID
	// from a previous key scheme makes the row unfindable via its own reported
	// ID (GET 404s, DELETE silently no-ops) and desyncs the DB row from memory.
	item.ID = key
	item.ItemID = normalizedItemID
	item.MediaType = strings.ToLower(update.MediaType)

	progressCleared := false
	wasAlreadyWatched := item.Watched

	// Update fields
	if update.Name != "" {
		item.Name = update.Name
	}
	if update.Year > 0 {
		item.Year = update.Year
	}
	if update.WatchedSeconds > item.WatchedSeconds {
		item.WatchedSeconds = update.WatchedSeconds
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
			item.Source = models.MergeWatchSource(wasAlreadyWatched, item.Source, watchSourceOrMarked(update.Source))
		}
		item.UpdatedAt = stateUpdatedAt
		if *update.Watched {
			keepPlayedSeconds(&item, s.playbackWatchedSecondsLocked(userID, update))
		}
		// Clear playback progress when watched status changes (both marking as watched and unwatched)
		progressCleared = s.clearPlaybackProgressEntryLocked(userID, update.MediaType, update.ItemID)
		// For episodes/movies, also clear any matching progress stored under a different ID format.
		if update.MediaType == "episode" && update.SeasonNumber > 0 && update.EpisodeNumber > 0 {
			if s.clearProgressByExternalIDMatchLocked(userID, update.SeriesID, update.SeasonNumber, update.EpisodeNumber, update.ExternalIDs) {
				progressCleared = true
			}
		} else if update.MediaType == "movie" {
			if s.clearMovieProgressByExternalIDMatchLocked(userID, update.ExternalIDs) {
				progressCleared = true
			}
		}
	}
	mergeWatchHistoryExternalIDs(&item, update.ExternalIDs)

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
	syncEquivalentEpisodeWatchHistoryLocked(perUser, key, item)

	// If marking an episode as watched, also clear progress for earlier episodes
	if update.Watched != nil && *update.Watched && update.MediaType == "episode" && update.SeriesID != "" && update.SeasonNumber > 0 && update.EpisodeNumber > 0 {
		if s.clearEarlierEpisodesProgressLocked(userID, update.SeriesID, update.SeasonNumber, update.EpisodeNumber) {
			progressCleared = true
		}
		// Also clear progress by external ID matching (handles cross-ID-format mismatches,
		// e.g. Trakt using tvdb:series:X while player uses tmdb:tv:Y for the same show)
		if s.clearProgressByExternalIDMatchLocked(userID, update.SeriesID, update.SeasonNumber, update.EpisodeNumber, update.ExternalIDs) {
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
	s.invalidateContinueWatchingLocked(userID)

	// Get scrobbler reference while holding lock (safe since we have write lock)
	scrobbler := s.traktScrobbler

	// Only scrobble if the watched state actually changed from unwatched to watched.
	// This prevents duplicate Trakt history entries when an already-watched item
	// is updated again (e.g. metadata refresh, redundant API calls).
	if update.Watched != nil && *update.Watched && !wasAlreadyWatched {
		s.doScrobble(scrobbler, userID, item)
	} else if update.Watched != nil && !*update.Watched && wasAlreadyWatched {
		s.doUnscrobble(scrobbler, userID, item)
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

// DeleteWatchHistoryItem removes a watch-history row, matching equivalent
// provider aliases for the supplied identifier.
func (s *Service) DeleteWatchHistoryItem(userID, mediaType, itemID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ErrUserIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.bumpWatchStateRevisionLocked(userID)

	perUser, ok := s.watchHistory[userID]
	if !ok {
		return nil
	}

	identity := mediaidentity.Resolve(mediaidentity.Input{MediaType: mediaType, ID: itemID})
	removed := false
	for _, key := range identity.CandidateKeys {
		if _, exists := perUser[key]; exists {
			delete(perUser, key)
			removed = true
		}
	}
	for key, item := range perUser {
		if !watchHistoryItemMatchesIdentity(item, identity) {
			continue
		}
		delete(perUser, key)
		removed = true
	}
	if !removed {
		return nil
	}

	if err := s.saveWatchHistoryLocked(); err != nil {
		return err
	}
	s.invalidateContinueWatchingLocked(userID)
	return nil
}

// ClearWatchHistory removes watch history for every profile and returns the
// number of entries removed. The in-memory state is restored if persistence
// fails so callers never observe a partially applied clear.
func (s *Service) ClearWatchHistory() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bumpAllWatchStateRevisionsLocked()

	count := 0
	for _, perUser := range s.watchHistory {
		count += len(perUser)
	}
	previous := s.watchHistory
	s.watchHistory = make(map[string]map[string]models.WatchHistoryItem)
	if err := s.saveWatchHistoryLocked(); err != nil {
		s.watchHistory = previous
		return 0, err
	}
	s.continueWatchingCache = make(map[string]*cachedContinueWatching)
	return count, nil
}

// BulkUpdateWatchHistory marks multiple episodes as watched/unwatched in a single operation.
func (s *Service) BulkUpdateWatchHistory(userID string, updates []models.WatchHistoryUpdate) ([]models.WatchHistoryItem, error) {
	return s.BulkUpdateScopedWatchHistory(userID, updates, "")
}

// BulkUpdateScopedWatchHistory carries explicit whole-show/season intent for
// providers whose bulk APIs cannot accept an arbitrary episode selection.
func (s *Service) BulkUpdateScopedWatchHistory(userID string, updates []models.WatchHistoryUpdate, scope string) ([]models.WatchHistoryItem, error) {
	if err := validateBulkScope(updates, scope); err != nil {
		return nil, err
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUserIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.bumpWatchStateRevisionLocked(userID)

	perUser := s.ensureWatchHistoryUserLocked(userID)
	results := make([]models.WatchHistoryItem, 0, len(updates))
	wasAlreadyWatched := make([]bool, 0, len(updates))
	now := time.Now().UTC()
	progressCleared := false

	for _, update := range updates {
		update = normalizeWatchHistoryUpdate(update)
		if !isAddressableEpisodeUpdate(update.MediaType, update.ItemID, update.EpisodeNumber, update.ExternalIDs) {
			return nil, ErrEpisodeNotAddressable
		}
		// Normalize itemID to lowercase for consistent key matching
		identity := watchHistoryUpdateIdentity(update)
		item, exists := takeWatchHistoryItemLocked(perUser, identity)
		key, normalizedItemID := canonicalWatchHistorySurvivor(item, exists, identity, &update)

		if !exists {
			item = models.WatchHistoryItem{
				ID:        key,
				MediaType: strings.ToLower(update.MediaType),
				ItemID:    normalizedItemID,
				Watched:   false,
			}
		}
		// Keep the row coherent with the key it is stored under (see UpdateWatchHistory).
		item.ID = key
		item.ItemID = normalizedItemID
		item.MediaType = strings.ToLower(update.MediaType)

		wasAlreadyWatched = append(wasAlreadyWatched, item.Watched)

		// Update fields
		if update.Name != "" {
			item.Name = update.Name
		}
		if update.Year > 0 {
			item.Year = update.Year
		}
		if update.WatchedSeconds > item.WatchedSeconds {
			item.WatchedSeconds = update.WatchedSeconds
		}
		if update.Watched != nil {
			stateUpdatedAt := now
			if !update.WatchedAt.IsZero() {
				stateUpdatedAt = update.WatchedAt.UTC()
			}
			wasWatched := item.Watched
			item.Watched = *update.Watched
			if *update.Watched {
				// Use provided timestamp if set, otherwise use now
				if !update.WatchedAt.IsZero() {
					item.WatchedAt = update.WatchedAt.UTC()
				} else {
					item.WatchedAt = now
				}
				item.Source = models.MergeWatchSource(wasWatched, item.Source, watchSourceOrMarked(update.Source))
			}
			item.UpdatedAt = stateUpdatedAt
			if *update.Watched {
				keepPlayedSeconds(&item, s.playbackWatchedSecondsLocked(userID, update))
			}
			// Clear playback progress when watched status changes (both marking as watched and unwatched)
			if s.clearPlaybackProgressEntryLocked(userID, update.MediaType, update.ItemID) {
				progressCleared = true
			}
			// For episodes/movies, also clear any matching progress stored under a different ID format.
			if update.MediaType == "episode" && update.SeasonNumber > 0 && update.EpisodeNumber > 0 {
				if s.clearProgressByExternalIDMatchLocked(userID, update.SeriesID, update.SeasonNumber, update.EpisodeNumber, update.ExternalIDs) {
					progressCleared = true
				}
			} else if update.MediaType == "movie" {
				if s.clearMovieProgressByExternalIDMatchLocked(userID, update.ExternalIDs) {
					progressCleared = true
				}
			}
		}
		mergeWatchHistoryExternalIDs(&item, update.ExternalIDs)

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
		syncEquivalentEpisodeWatchHistoryLocked(perUser, key, item)

		// If marking an episode as watched, also clear progress for earlier episodes
		if update.Watched != nil && *update.Watched && update.MediaType == "episode" && update.SeriesID != "" && update.SeasonNumber > 0 && update.EpisodeNumber > 0 {
			if s.clearEarlierEpisodesProgressLocked(userID, update.SeriesID, update.SeasonNumber, update.EpisodeNumber) {
				progressCleared = true
			}
			// Also clear progress by external ID matching (handles cross-ID-format mismatches)
			if s.clearProgressByExternalIDMatchLocked(userID, update.SeriesID, update.SeasonNumber, update.EpisodeNumber, update.ExternalIDs) {
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
	s.invalidateContinueWatchingLocked(userID)

	// Get scrobbler reference while holding lock (safe since we have write lock)
	scrobbler := s.traktScrobbler

	// Only scrobble items whose watched state actually changed from unwatched to watched.
	// This prevents duplicate Trakt history entries on redundant bulk updates.
	changed := make([]models.WatchHistoryItem, 0, len(results))
	initial := make(map[string]bool)
	final := make(map[string]models.WatchHistoryItem)
	var order []string
	for i, update := range updates {
		if update.Watched == nil {
			continue
		}
		item := results[i]
		if _, seen := initial[item.ID]; !seen {
			initial[item.ID] = wasAlreadyWatched[i]
			order = append(order, item.ID)
		}
		final[item.ID] = item
	}
	for _, id := range order {
		if item := final[id]; item.Watched != initial[id] {
			changed = append(changed, item)
		}
	}
	s.doBulkScrobble(scrobbler, userID, changed, scope)

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
	s.bumpWatchStateRevisionLocked(userID)

	perUser := s.ensureWatchHistoryUserLocked(userID)
	now := time.Now().UTC()
	progressCleared := false
	imported := 0
	identityEnriched := false
	identityEnrichedItems := 0
	skippedUnaddressable, skippedLocalNewer, skippedManualUnwatch, skippedInProgress := 0, 0, 0, 0

	// Build cross-provider dedup index: "s01e01:imdb:tt123" → existing watch key
	// This lets us find existing entries for the same episode imported under a different ID format.
	type episodeIndex struct {
		watchKey string
		stateAt  time.Time
		watched  bool
	}
	crossProviderIndex := make(map[string]episodeIndex)
	episodeScopedIDIndex := make(map[string]episodeIndex)
	putEpisodeIndex := func(index map[string]episodeIndex, indexKey, watchKey string, item models.WatchHistoryItem) {
		next := episodeIndex{
			watchKey: watchKey,
			stateAt:  watchHistoryImportStateTime(item),
			watched:  item.Watched,
		}
		current, ok := index[indexKey]
		if !ok ||
			next.stateAt.After(current.stateAt) ||
			(next.stateAt.Equal(current.stateAt) && current.watched && !next.watched) ||
			(next.stateAt.Equal(current.stateAt) && current.watched == next.watched && next.watchKey < current.watchKey) {
			index[indexKey] = next
		}
	}
	removeEpisodeIndexes := func(item models.WatchHistoryItem) {
		if item.MediaType != "episode" || item.SeasonNumber == 0 || item.EpisodeNumber == 0 {
			return
		}
		epKey := episodeKey(item.SeasonNumber, item.EpisodeNumber)
		for oldIDType, oldIDValue := range canonicalSeriesExternalIDs(item.SeriesID, item.ItemID, item.ExternalIDs) {
			if oldIDValue == "" {
				continue
			}
			delete(crossProviderIndex, epKey+":"+oldIDType+":"+oldIDValue)
		}
		for _, oldIndexKey := range episodeScopedIndexKeys(item.ExternalIDs) {
			delete(episodeScopedIDIndex, oldIndexKey)
		}
	}
	putItemEpisodeIndexes := func(key string, item models.WatchHistoryItem) {
		if item.MediaType != "episode" || item.SeasonNumber == 0 || item.EpisodeNumber == 0 {
			return
		}
		epKey := episodeKey(item.SeasonNumber, item.EpisodeNumber)
		for idType, idValue := range canonicalSeriesExternalIDs(item.SeriesID, item.ItemID, item.ExternalIDs) {
			if idValue == "" {
				continue
			}
			putEpisodeIndex(crossProviderIndex, epKey+":"+idType+":"+idValue, key, item)
		}
		for _, indexKey := range episodeScopedIndexKeys(item.ExternalIDs) {
			putEpisodeIndex(episodeScopedIDIndex, indexKey, key, item)
		}
	}
	for key, item := range perUser {
		putItemEpisodeIndexes(key, item)
	}

	for _, update := range updates {
		update = normalizeWatchHistoryUpdate(update)
		if !isAddressableEpisodeUpdate(update.MediaType, update.ItemID, update.EpisodeNumber, update.ExternalIDs) {
			skippedUnaddressable++
			log.Printf("[history] import: SKIP (unaddressable episode) %q itemID=%q seriesID=%q", update.Name, update.ItemID, update.SeriesID)
			continue
		}
		identity := watchHistoryUpdateIdentity(update)
		normalizedItemID := identity.ID
		key := identity.Key
		matchedKey, existing, exists := bestWatchHistoryImportMatchLocked(perUser, identity)

		// crossProviderRekeyed is set when dedup finds the item under a different key and
		// deletes the old entry. In that case the SKIP path must re-save under the new key
		// to prevent the item from disappearing and triggering a re-scrobble loop.
		crossProviderRekeyed := false
		dedupedEquivalent := exists && matchedKey != key
		if exists && matchedKey != key {
			if preferProgressID(existing.ItemID, normalizedItemID) {
				key = matchedKey
				normalizedItemID = existing.ItemID
				log.Printf("[history] import: DEDUP identity %s %q (attaching to canonical key %s)",
					update.MediaType, update.Name, key)
			} else {
				crossProviderRekeyed = true
				delete(perUser, matchedKey)
				removeEpisodeIndexes(existing)
				log.Printf("[history] import: DEDUP identity %s %q (old key %s -> new key %s)",
					update.MediaType, update.Name, matchedKey, key)
			}
		}
		persistDedupedSkip := func() {
			if !exists {
				return
			}
			if crossProviderRekeyed {
				existing.ID = key
				existing.ItemID = normalizedItemID
				existing.MediaType = strings.ToLower(update.MediaType)
				if update.Name != "" {
					existing.Name = update.Name
				}
				if update.Year > 0 {
					existing.Year = update.Year
				}
				if update.SeasonNumber > 0 {
					existing.SeasonNumber = update.SeasonNumber
				}
				if update.EpisodeNumber > 0 {
					existing.EpisodeNumber = update.EpisodeNumber
				}
				if update.SeriesID != "" {
					existing.SeriesID = update.SeriesID
				}
				if update.SeriesName != "" {
					existing.SeriesName = update.SeriesName
				}
			}
			// An older watch event can still supply aliases needed by another
			// catalog's watched badges. Preserve local state and existing IDs.
			aliasesAdded := false
			for k, v := range update.ExternalIDs {
				if v != "" && existing.ExternalIDs[k] == "" {
					if existing.ExternalIDs == nil {
						existing.ExternalIDs = make(map[string]string)
					}
					existing.ExternalIDs[k] = v
					identityEnriched = true
					aliasesAdded = true
				}
			}
			if !crossProviderRekeyed && !dedupedEquivalent && !aliasesAdded {
				return
			}
			if aliasesAdded {
				identityEnrichedItems++
			}
			perUser[key] = existing
			syncEquivalentEpisodeWatchHistoryLocked(perUser, key, existing)
			putItemEpisodeIndexes(key, existing)
			if crossProviderRekeyed {
				imported++
			}
		}

		// Cross-provider dedup: if no direct match, look for an existing entry
		// with matching external IDs and same season/episode numbers.
		if !exists && update.SeasonNumber > 0 && update.EpisodeNumber > 0 && len(update.ExternalIDs) > 0 {
			epKey := episodeKey(update.SeasonNumber, update.EpisodeNumber)
			for idType, idValue := range canonicalSeriesExternalIDs(update.SeriesID, update.ItemID, update.ExternalIDs) {
				if idValue == "" {
					continue
				}
				indexKey := epKey + ":" + idType + ":" + idValue
				if idx, found := crossProviderIndex[indexKey]; found && idx.watchKey != key {
					// Found a match under a different key — merge into existing entry.
					existingItem, ok := perUser[idx.watchKey]
					if !ok {
						continue
					}
					existing = existingItem
					exists = true
					dedupedEquivalent = true
					if preferProgressID(existing.ItemID, normalizedItemID) {
						// Existing entry's key is the canonical (TMDB-preferred)
						// survivor; attach to it instead of re-keying to the
						// incoming scheme. Its index entries stay valid.
						key = idx.watchKey
						normalizedItemID = existing.ItemID
						crossProviderRekeyed = false
						log.Printf("[history] import: DEDUP cross-provider %s %q (attaching to canonical key %s)",
							update.MediaType, update.Name, key)
					} else {
						// Incoming key is canonical; drop the old row and re-key
						// onto the incoming key below.
						crossProviderRekeyed = true
						delete(perUser, idx.watchKey)
						removeEpisodeIndexes(existing)
						log.Printf("[history] import: DEDUP cross-provider %s %q (old key %s -> new key %s)",
							update.MediaType, update.Name, idx.watchKey, key)
					}
					break
				}
			}
		}

		// Absolute-numbered anime imports can refer to the same episode using
		// SxxE<absolute> while local history uses the aired-order SxxEyy. If an
		// episode-scoped provider ID matches an existing local item, preserve the
		// local canonical season/episode/key instead of re-keying to the absolute
		// Trakt number.
		if !exists && update.MediaType == "episode" && len(update.ExternalIDs) > 0 {
			for _, indexKey := range episodeScopedIndexKeys(update.ExternalIDs) {
				if idx, found := episodeScopedIDIndex[indexKey]; found && idx.watchKey != key {
					existingItem, ok := perUser[idx.watchKey]
					if !ok {
						continue
					}
					existing = existingItem
					exists = true
					dedupedEquivalent = true
					crossProviderRekeyed = false
					normalizedItemID = existing.ItemID
					key = idx.watchKey
					update.SeasonNumber = existing.SeasonNumber
					update.EpisodeNumber = existing.EpisodeNumber
					if update.SeriesID == "" {
						update.SeriesID = existing.SeriesID
					}
					log.Printf("[history] import: DEDUP episode-scoped ID %s %q (preserving canonical key %s)",
						update.MediaType, update.Name, key)
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
					dedupedEquivalent = true
					if preferProgressID(existingItem.ItemID, normalizedItemID) {
						// Existing key is the canonical (TMDB-preferred) survivor.
						key = existingKey
						normalizedItemID = existingItem.ItemID
						crossProviderRekeyed = false
						log.Printf("[history] import: DEDUP cross-provider movie %q (attaching to canonical key %s)",
							update.Name, key)
					} else {
						crossProviderRekeyed = true
						delete(perUser, existingKey)
						log.Printf("[history] import: DEDUP cross-provider movie %q (old key %s -> new key %s)",
							update.Name, existingKey, key)
					}
					break
				}
			}
		}

		if exists && update.WatchedAtUnknown {
			if existing.Watched {
				skippedLocalNewer++
			} else {
				skippedManualUnwatch++
			}
			log.Printf("[history] import: SKIP (no play date, local state kept) %s %q itemID=%q watched=%t",
				update.MediaType, update.Name, normalizedItemID, existing.Watched)
			persistDedupedSkip()
			continue
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
				skippedLocalNewer++
				// Already recorded, but still clean up any stale playback progress
				// that may exist under a different ID format.
				if update.MediaType == "episode" && update.SeriesID != "" && update.SeasonNumber > 0 && update.EpisodeNumber > 0 {
					if s.clearEarlierEpisodesProgressLocked(userID, update.SeriesID, update.SeasonNumber, update.EpisodeNumber) {
						progressCleared = true
					}
				}
				log.Printf("[history] import: SKIP (local newer) %s %q itemID=%q watchedAt=%s importedWatchedAt=%s",
					update.MediaType, update.Name, normalizedItemID, existing.UpdatedAt.Format(time.RFC3339), incomingStateTime.Format(time.RFC3339))
				// If cross-provider dedup deleted the old key, re-save under the new canonical
				// key so the item isn't lost, which would cause a re-scrobble loop.
				persistDedupedSkip()
				continue
			}
			log.Printf("[history] import: UPDATE (external newer) %s %q itemID=%q localWatchedAt=%s -> importedWatchedAt=%s seriesID=%s",
				update.MediaType, update.Name, normalizedItemID, existing.UpdatedAt.Format(time.RFC3339), incomingStateTime.Format(time.RFC3339), update.SeriesID)
		} else if exists && !existing.Watched {
			incomingStateTime := update.WatchedAt
			if incomingStateTime.IsZero() {
				incomingStateTime = now
			}
			if !existing.UpdatedAt.IsZero() && (existing.UpdatedAt.After(incomingStateTime) || existing.UpdatedAt.Equal(incomingStateTime)) {
				skippedManualUnwatch++
				log.Printf("[history] import: SKIP (manual unwatch newer/equal) %s %q localWatchedAt=%s importedWatchedAt=%s seriesID=%s",
					update.MediaType, update.Name, existing.UpdatedAt.Format(time.RFC3339), incomingStateTime.Format(time.RFC3339), update.SeriesID)
				// If cross-provider dedup deleted the old key, re-save under the new canonical key.
				persistDedupedSkip()
				continue
			}
			log.Printf("[history] import: RESTORE (external newer than manual unwatch baseline) %s %q localWatchedAt=%s -> importedWatchedAt=%s seriesID=%s",
				update.MediaType, update.Name, existing.UpdatedAt.Format(time.RFC3339), incomingStateTime.Format(time.RFC3339), update.SeriesID)
		} else if !exists {
			log.Printf("[history] import: NEW %s %q itemID=%q watchedAt=%s seriesID=%s",
				update.MediaType, update.Name, normalizedItemID, update.WatchedAt.Format(time.RFC3339), update.SeriesID)
		}

		if update.Watched != nil && *update.Watched && s.hasConflictingInProgressPlaybackLocked(userID, update) {
			skippedInProgress++
			log.Printf("[history] import: SKIP (preserve local in-progress) %s %q watchedAt=%s seriesID=%s",
				update.MediaType, update.Name, update.WatchedAt.Format(time.RFC3339), update.SeriesID)
			persistDedupedSkip()
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
		item.ID = key
		item.MediaType = strings.ToLower(update.MediaType)
		item.ItemID = normalizedItemID

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
			wasWatched := item.Watched
			item.Watched = *update.Watched
			if *update.Watched {
				if !update.WatchedAt.IsZero() {
					item.WatchedAt = update.WatchedAt.UTC()
				} else {
					item.WatchedAt = now
				}
				source := update.Source
				if source == "" {
					source = models.WatchSourceImported
				}
				item.Source = models.MergeWatchSource(wasWatched, item.Source, source)
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
		syncEquivalentEpisodeWatchHistoryLocked(perUser, key, item)

		// Update cross-provider index so subsequent items in this batch can find this entry
		putItemEpisodeIndexes(key, item)

		if update.Watched != nil && *update.Watched && update.MediaType == "episode" && update.SeriesID != "" && update.SeasonNumber > 0 && update.EpisodeNumber > 0 {
			if s.clearEarlierEpisodesProgressLocked(userID, update.SeriesID, update.SeasonNumber, update.EpisodeNumber) {
				progressCleared = true
			}
			// Also clear progress by external ID matching (handles cross-ID-format mismatches)
			if s.clearProgressByExternalIDMatchLocked(userID, update.SeriesID, update.SeasonNumber, update.EpisodeNumber, update.ExternalIDs) {
				progressCleared = true
			}
		}

		imported++
	}

	if imported > 0 || identityEnriched {
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
	s.invalidateContinueWatchingLocked(userID)

	// NOTE: No scrobbling — this method is specifically for importing from external sources
	log.Printf("[history] import summary profile=%q received=%d recordedChanges=%d identityEnriched=%d skippedLocalNewer=%d skippedManualUnwatch=%d skippedInProgress=%d skippedUnaddressable=%d",
		userID, len(updates), imported, identityEnrichedItems, skippedLocalNewer, skippedManualUnwatch, skippedInProgress, skippedUnaddressable)

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

func episodeScopedIndexKeys(externalIDs map[string]string) []string {
	if len(externalIDs) == 0 {
		return nil
	}
	keys := make([]string, 0, 4)
	for _, idType := range []string{"episodeTvdb", "episodeTmdb", "episodeImdb", "episodeTrakt"} {
		if idValue := strings.TrimSpace(externalIDs[idType]); idValue != "" {
			keys = append(keys, strings.ToLower(idType)+":"+strings.ToLower(idValue))
		}
	}
	return keys
}

type watchHistoryReconcileIndexKey struct {
	seriesToken string
	season      int
	number      int
}

type watchHistoryReconcileIndex struct {
	byEpisode  map[watchHistoryReconcileIndexKey]map[string]struct{}
	byAbsolute map[watchHistoryReconcileIndexKey]map[string]struct{}
}

func newWatchHistoryReconcileIndex(perUser map[string]models.WatchHistoryItem) *watchHistoryReconcileIndex {
	idx := &watchHistoryReconcileIndex{
		byEpisode:  make(map[watchHistoryReconcileIndexKey]map[string]struct{}),
		byAbsolute: make(map[watchHistoryReconcileIndexKey]map[string]struct{}),
	}
	for key, item := range perUser {
		idx.add(key, item)
	}
	return idx
}

func (idx *watchHistoryReconcileIndex) add(itemKey string, item models.WatchHistoryItem) {
	if idx == nil || item.MediaType != "episode" || item.SeasonNumber <= 0 || item.EpisodeNumber <= 0 || len(item.ExternalIDs) == 0 {
		return
	}
	seriesIDs := canonicalSeriesExternalIDs(item.SeriesID, item.ItemID, item.ExternalIDs)
	absolute, hasAbsolute := positiveExternalIDInt(item.ExternalIDs, "absoluteEpisode")
	for provider, id := range seriesIDs {
		provider = strings.ToLower(strings.TrimSpace(provider))
		id = strings.ToLower(strings.TrimSpace(id))
		if provider == "" || id == "" {
			continue
		}
		seriesToken := provider + "\x00" + id
		idx.addKey(idx.byEpisode, watchHistoryReconcileIndexKey{seriesToken: seriesToken, season: item.SeasonNumber, number: item.EpisodeNumber}, itemKey)
		if hasAbsolute {
			idx.addKey(idx.byAbsolute, watchHistoryReconcileIndexKey{seriesToken: seriesToken, season: item.SeasonNumber, number: absolute}, itemKey)
		}
	}
}

func (idx *watchHistoryReconcileIndex) addKey(index map[watchHistoryReconcileIndexKey]map[string]struct{}, key watchHistoryReconcileIndexKey, itemKey string) {
	items := index[key]
	if items == nil {
		items = make(map[string]struct{})
		index[key] = items
	}
	items[itemKey] = struct{}{}
}

func (idx *watchHistoryReconcileIndex) candidates(item models.WatchHistoryItem) map[string]struct{} {
	result := make(map[string]struct{})
	if idx == nil {
		return result
	}
	seriesIDs := canonicalSeriesExternalIDs(item.SeriesID, item.ItemID, item.ExternalIDs)
	absolute, hasAbsolute := positiveExternalIDInt(item.ExternalIDs, "absoluteEpisode")
	for provider, id := range seriesIDs {
		provider = strings.ToLower(strings.TrimSpace(provider))
		id = strings.ToLower(strings.TrimSpace(id))
		if provider == "" || id == "" {
			continue
		}
		seriesToken := provider + "\x00" + id
		idx.addCandidates(result, idx.byEpisode[watchHistoryReconcileIndexKey{seriesToken: seriesToken, season: item.SeasonNumber, number: item.EpisodeNumber}])
		idx.addCandidates(result, idx.byAbsolute[watchHistoryReconcileIndexKey{seriesToken: seriesToken, season: item.SeasonNumber, number: item.EpisodeNumber}])
		if hasAbsolute {
			idx.addCandidates(result, idx.byEpisode[watchHistoryReconcileIndexKey{seriesToken: seriesToken, season: item.SeasonNumber, number: absolute}])
			idx.addCandidates(result, idx.byAbsolute[watchHistoryReconcileIndexKey{seriesToken: seriesToken, season: item.SeasonNumber, number: absolute}])
		}
	}
	return result
}

func (idx *watchHistoryReconcileIndex) addCandidates(dst, src map[string]struct{}) {
	for key := range src {
		dst[key] = struct{}{}
	}
}

func reconcileEquivalentEpisodeWatchHistoryLocked(perUser map[string]models.WatchHistoryItem) bool {
	if len(perUser) < 2 {
		return false
	}
	items := make([]models.WatchHistoryItem, 0, len(perUser))
	for _, item := range perUser {
		if item.MediaType == "episode" && item.SeasonNumber > 0 && item.EpisodeNumber > 0 && len(item.ExternalIDs) > 0 {
			items = append(items, item)
		}
	}
	if len(items) < 2 {
		return false
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].UpdatedAt.Before(items[j].UpdatedAt)
	})

	index := newWatchHistoryReconcileIndex(perUser)
	changed := false
	for _, item := range items {
		if previous, exists := perUser[item.ID]; !exists || watchHistoryItemsDiffer(previous, item) {
			changed = true
		}
		perUser[item.ID] = item
		index.add(item.ID, item)
		for key := range index.candidates(item) {
			if key == item.ID {
				continue
			}
			candidate, exists := perUser[key]
			if !exists || candidate.MediaType != "episode" || candidate.SeasonNumber != item.SeasonNumber ||
				!watchHistorySeriesIdentitiesCompatible(candidate, item) ||
				!watchHistoryEquivalentEpisodeIDsMatch(candidate, item) {
				continue
			}
			updated, updatedCandidate := mergeEquivalentEpisodeWatchHistoryItem(item, candidate)
			if !updatedCandidate {
				continue
			}
			perUser[key] = updated
			index.add(key, updated)
			changed = true
		}
	}
	return changed
}

func watchHistoryItemsDiffer(a, b models.WatchHistoryItem) bool {
	if a.Watched != b.Watched ||
		a.Source != b.Source ||
		a.WatchedSeconds != b.WatchedSeconds ||
		!a.WatchedAt.Equal(b.WatchedAt) ||
		!a.UpdatedAt.Equal(b.UpdatedAt) ||
		len(a.ExternalIDs) != len(b.ExternalIDs) {
		return true
	}
	for k, v := range a.ExternalIDs {
		if b.ExternalIDs[k] != v {
			return true
		}
	}
	return false
}

func syncEquivalentEpisodeWatchHistoryLocked(perUser map[string]models.WatchHistoryItem, canonicalKey string, item models.WatchHistoryItem) {
	if item.MediaType != "episode" || item.SeasonNumber <= 0 || item.EpisodeNumber <= 0 || len(item.ExternalIDs) == 0 {
		return
	}

	for key, candidate := range perUser {
		if key == canonicalKey ||
			candidate.MediaType != "episode" ||
			candidate.SeasonNumber != item.SeasonNumber ||
			!watchHistorySeriesIdentitiesCompatible(candidate, item) ||
			!watchHistoryEquivalentEpisodeIDsMatch(candidate, item) {
			continue
		}

		updated, changed := mergeEquivalentEpisodeWatchHistoryItem(item, candidate)
		if !changed {
			continue
		}
		perUser[key] = updated
	}
}

func mergeEquivalentEpisodeWatchHistoryItem(source, target models.WatchHistoryItem) (models.WatchHistoryItem, bool) {
	if !preferEquivalentEpisodeWatchHistoryState(source, target) {
		return target, false
	}
	updated := target
	updated.Watched = source.Watched
	if source.WatchedSeconds > updated.WatchedSeconds {
		updated.WatchedSeconds = source.WatchedSeconds
	}
	updated.UpdatedAt = source.UpdatedAt
	if source.Watched {
		updated.WatchedAt = source.WatchedAt
		updated.Source = source.Source
	}
	updated.ExternalIDs = make(map[string]string, len(target.ExternalIDs)+len(source.ExternalIDs))
	for k, v := range target.ExternalIDs {
		updated.ExternalIDs[k] = v
	}
	for k, v := range source.ExternalIDs {
		if v != "" {
			updated.ExternalIDs[k] = v
		}
	}
	return updated, watchHistoryItemsDiffer(target, updated)
}

func preferEquivalentEpisodeWatchHistoryState(source, target models.WatchHistoryItem) bool {
	sourceTime := watchHistoryImportStateTime(source)
	targetTime := watchHistoryImportStateTime(target)
	if !sourceTime.Equal(targetTime) {
		return sourceTime.After(targetTime)
	}
	if source.Watched != target.Watched {
		return !source.Watched
	}
	return false
}

func watchHistorySeriesIdentitiesCompatible(a, b models.WatchHistoryItem) bool {
	aIDs := canonicalSeriesExternalIDs(a.SeriesID, a.ItemID, a.ExternalIDs)
	bIDs := canonicalSeriesExternalIDs(b.SeriesID, b.ItemID, b.ExternalIDs)
	if len(aIDs) == 0 || len(bIDs) == 0 {
		return false
	}
	for provider, aID := range aIDs {
		if aID == "" {
			continue
		}
		if bID := bIDs[provider]; bID != "" && strings.EqualFold(aID, bID) {
			return true
		}
	}
	return false
}

func watchHistoryEquivalentEpisodeIDsMatch(candidate, item models.WatchHistoryItem) bool {
	if candidate.EpisodeNumber == item.EpisodeNumber {
		return hasMatchingExternalID(candidate.ExternalIDs, item.ExternalIDs)
	}
	if !watchHistoryAbsoluteEpisodeDuplicateMatch(candidate.EpisodeNumber, item.EpisodeNumber, candidate.ExternalIDs, item.ExternalIDs) {
		return false
	}
	// Different episode numbers are aliases when an episode-scoped provider ID
	// agrees. If both rows carry episode IDs and they disagree, show-level IDs
	// merely prove that the rows belong to the same series; they must not merge
	// adjacent episodes whose stale absolute numbers happen to collide.
	if hasMatchingEpisodeScopedExternalID(candidate.ExternalIDs, item.ExternalIDs) {
		return true
	}
	if hasEpisodeScopedExternalIDs(candidate.ExternalIDs) && hasEpisodeScopedExternalIDs(item.ExternalIDs) {
		return false
	}

	// Preserve compatibility with legacy absolute-number rows that predate
	// episode-scoped IDs (for example S23E1164 versus S23E09, abs 1164). This
	// requires one row's episode number to be the other's absolute number; equal
	// absolute metadata alone is not enough.
	candidateAbsolute, candidateHasAbsolute := positiveExternalIDInt(candidate.ExternalIDs, "absoluteEpisode")
	itemAbsolute, itemHasAbsolute := positiveExternalIDInt(item.ExternalIDs, "absoluteEpisode")
	return (itemHasAbsolute && !candidateHasAbsolute && candidate.EpisodeNumber == itemAbsolute) ||
		(candidateHasAbsolute && !itemHasAbsolute && item.EpisodeNumber == candidateAbsolute)
}

func hasMatchingEpisodeScopedExternalID(a, b map[string]string) bool {
	a = mediaidentity.NormalizeExternalIDs(a)
	b = mediaidentity.NormalizeExternalIDs(b)
	for _, key := range []string{"episodeTvdb", "episodeTmdb", "episodeImdb", "episodeTrakt"} {
		if a[key] != "" && strings.EqualFold(a[key], b[key]) {
			return true
		}
	}
	return false
}

func watchHistoryAbsoluteEpisodeDuplicateMatch(candidateEpisode, updateEpisode int, candidateExternalIDs, updateExternalIDs map[string]string) bool {
	candidateAbsolute, candidateHasAbsolute := positiveExternalIDInt(candidateExternalIDs, "absoluteEpisode")
	updateAbsolute, updateHasAbsolute := positiveExternalIDInt(updateExternalIDs, "absoluteEpisode")
	smallerEpisode := candidateEpisode
	if updateEpisode < smallerEpisode {
		smallerEpisode = updateEpisode
	}
	if updateHasAbsolute && candidateEpisode == updateAbsolute && updateAbsolute > smallerEpisode {
		return true
	}
	if candidateHasAbsolute && updateEpisode == candidateAbsolute && candidateAbsolute > smallerEpisode {
		return true
	}
	return candidateHasAbsolute && updateHasAbsolute && candidateAbsolute == updateAbsolute && candidateAbsolute > smallerEpisode
}

func (s *Service) loadWatchHistory() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bumpAllWatchStateRevisionsLocked()

	if s.useDB() {
		loadStarted := time.Now()
		ctx := context.Background()
		queryStarted := time.Now()
		allItems, err := s.store.WatchHistory().ListAll(ctx)
		if err != nil {
			return fmt.Errorf("load watch history from db: %w", err)
		}
		queryDuration := time.Since(queryStarted)
		processingStarted := time.Now()
		s.watchHistory = make(map[string]map[string]models.WatchHistoryItem, len(allItems))
		persisted := make(map[historyPersistenceKey][sha256.Size]byte)
		rowCount := 0
		var reconcileDuration time.Duration
		for userID, items := range allItems {
			rowCount += len(items)
			perUser := make(map[string]models.WatchHistoryItem, len(items))
			for _, item := range items {
				fingerprint, err := watchHistoryPersistenceFingerprint(item)
				if err != nil {
					return fmt.Errorf("snapshot loaded watch history item %s: %w", item.ID, err)
				}
				persisted[historyPersistenceKey{userID: userID, itemID: item.ID}] = fingerprint
				item = normalizeWatchHistoryItem(item)
				key := item.ID
				perUser[key] = item
			}
			reconcileStarted := time.Now()
			reconcileEquivalentEpisodeWatchHistoryLocked(perUser)
			reconcileDuration += time.Since(reconcileStarted)
			s.watchHistory[userID] = perUser
		}
		s.persistedWatchHistory = persisted
		log.Printf("[history] Startup load completed table=watch_history rows=%d users=%d query=%s processing=%s reconcile=%s total=%s",
			rowCount, len(allItems), queryDuration, time.Since(processingStarted), reconcileDuration, time.Since(loadStarted))
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

			normalized := normalizeWatchHistoryItem(item)
			if normalized.ID != item.ID || normalized.ItemID != item.ItemID || normalized.SeriesID != item.SeriesID {
				needsSave = true
			}
			item = normalized
			key := item.ID
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
		if reconcileEquivalentEpisodeWatchHistoryLocked(perUser) {
			needsSave = true
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
	return mediaidentity.Key(mediaType, itemID)
}

func storageIdentityExternalIDs(mediaType, itemID, seriesID string, externalIDs map[string]string) map[string]string {
	return mediaidentity.IdentityExternalIDs(mediaType, itemID, seriesID, externalIDs)
}

func normalizeStorageExternalIDs(mediaType, itemID, seriesID string, externalIDs map[string]string) map[string]string {
	return mediaidentity.StorageExternalIDs(mediaType, itemID, seriesID, externalIDs)
}

func normalizeWatchHistoryUpdate(update models.WatchHistoryUpdate) models.WatchHistoryUpdate {
	update.SeriesName = normalizeSeriesName(update.SeriesName)
	normalizedExternalIDs := normalizeStorageExternalIDs(update.MediaType, update.ItemID, update.SeriesID, update.ExternalIDs)
	externalIDs := storageIdentityExternalIDs(update.MediaType, update.ItemID, update.SeriesID, normalizedExternalIDs)
	identity := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:     update.MediaType,
		ID:            update.ItemID,
		SeriesID:      update.SeriesID,
		SeasonNumber:  update.SeasonNumber,
		EpisodeNumber: update.EpisodeNumber,
		ExternalIDs:   externalIDs,
	})
	update.MediaType = identity.MediaType
	update.ItemID = identity.ID
	update.ExternalIDs = normalizedExternalIDs
	if identity.MediaType == "episode" {
		update.SeriesID = identity.SeriesID
		update.SeasonNumber = identity.SeasonNumber
		update.EpisodeNumber = identity.EpisodeNumber
	}
	return update
}

func normalizeWatchHistoryItem(item models.WatchHistoryItem) models.WatchHistoryItem {
	item.SeriesName = normalizeSeriesName(item.SeriesName)
	normalizedExternalIDs := normalizeStorageExternalIDs(item.MediaType, item.ItemID, item.SeriesID, item.ExternalIDs)
	externalIDs := storageIdentityExternalIDs(item.MediaType, item.ItemID, item.SeriesID, normalizedExternalIDs)
	identity := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:     item.MediaType,
		ID:            item.ItemID,
		SeriesID:      item.SeriesID,
		SeasonNumber:  item.SeasonNumber,
		EpisodeNumber: item.EpisodeNumber,
		ExternalIDs:   externalIDs,
	})
	item.ID = identity.Key
	item.MediaType = identity.MediaType
	item.ItemID = identity.ID
	item.ExternalIDs = normalizedExternalIDs
	if identity.MediaType == "episode" {
		item.SeriesID = identity.SeriesID
		item.SeasonNumber = identity.SeasonNumber
		item.EpisodeNumber = identity.EpisodeNumber
	}
	return item
}

func normalizePlaybackProgressUpdate(update models.PlaybackProgressUpdate) models.PlaybackProgressUpdate {
	update.SeriesName = normalizeSeriesName(update.SeriesName)
	if isLiveProgressUpdate(update) {
		update.MediaType = "live"
		update.ItemID = mediaidentity.SanitizeID(update.ItemID)
		update.ExternalIDs = mediaidentity.NormalizeExternalIDs(update.ExternalIDs)
		return update
	}
	normalizedExternalIDs := normalizeStorageExternalIDs(update.MediaType, update.ItemID, update.SeriesID, update.ExternalIDs)
	externalIDs := storageIdentityExternalIDs(update.MediaType, update.ItemID, update.SeriesID, normalizedExternalIDs)
	identity := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:     update.MediaType,
		ID:            update.ItemID,
		SeriesID:      update.SeriesID,
		SeasonNumber:  update.SeasonNumber,
		EpisodeNumber: update.EpisodeNumber,
		ExternalIDs:   externalIDs,
	})
	update.MediaType = identity.MediaType
	update.ItemID = identity.ID
	update.ExternalIDs = normalizedExternalIDs
	if identity.MediaType == "episode" {
		update.SeriesID = identity.SeriesID
		update.SeasonNumber = identity.SeasonNumber
		update.EpisodeNumber = identity.EpisodeNumber
	}
	return update
}

func normalizePlaybackProgressItem(progress models.PlaybackProgress) models.PlaybackProgress {
	progress.SeriesName = normalizeSeriesName(progress.SeriesName)
	if strings.EqualFold(strings.TrimSpace(progress.MediaType), "live") {
		progress.MediaType = "live"
		progress.ItemID = mediaidentity.SanitizeID(progress.ItemID)
		progress.ID = makeWatchKey(progress.MediaType, progress.ItemID)
		progress.ExternalIDs = mediaidentity.NormalizeExternalIDs(progress.ExternalIDs)
		return progress
	}
	normalizedExternalIDs := normalizeStorageExternalIDs(progress.MediaType, progress.ItemID, progress.SeriesID, progress.ExternalIDs)
	externalIDs := storageIdentityExternalIDs(progress.MediaType, progress.ItemID, progress.SeriesID, normalizedExternalIDs)
	identity := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:     progress.MediaType,
		ID:            progress.ItemID,
		SeriesID:      progress.SeriesID,
		SeasonNumber:  progress.SeasonNumber,
		EpisodeNumber: progress.EpisodeNumber,
		ExternalIDs:   externalIDs,
	})
	progress.ID = identity.Key
	progress.MediaType = identity.MediaType
	progress.ItemID = identity.ID
	progress.ExternalIDs = normalizedExternalIDs
	if identity.MediaType == "episode" {
		progress.SeriesID = identity.SeriesID
		progress.SeasonNumber = identity.SeasonNumber
		progress.EpisodeNumber = identity.EpisodeNumber
	}
	return progress
}

func normalizeSeriesName(name string) string {
	trimmed := strings.TrimSpace(name)
	cleaned := strings.TrimSpace(seriesDisplayLabelRE.ReplaceAllString(trimmed, ""))
	if cleaned != "" {
		return cleaned
	}
	return trimmed
}

// mayMatchMediaIdentity is a cheap pre-filter so full identity resolution is
// skipped for rows that cannot match the target. Episodes with known but
// different season/episode numbers can only be equivalent through
// episode-scoped external IDs (absolute-numbering aliases); everything else is
// rejected without allocating.
func mayMatchMediaIdentity(mediaType string, seasonNumber, episodeNumber int, externalIDs map[string]string, target mediaidentity.Identity) bool {
	if mediaidentity.NormalizeMediaType(mediaType) != target.MediaType {
		return false
	}
	if target.MediaType != "episode" {
		return true
	}
	if episodeNumber <= 0 || target.EpisodeNumber <= 0 {
		return true
	}
	if seasonNumber == target.SeasonNumber && episodeNumber == target.EpisodeNumber {
		return true
	}
	return hasEpisodeScopedExternalIDs(externalIDs) && hasEpisodeScopedExternalIDs(target.ExternalIDs)
}

func hasEpisodeScopedExternalIDs(externalIDs map[string]string) bool {
	for key, value := range externalIDs {
		if value == "" {
			continue
		}
		if len(key) >= len("episode") && strings.EqualFold(key[:len("episode")], "episode") {
			return true
		}
	}
	return false
}

func watchHistoryItemMatchesIdentity(item models.WatchHistoryItem, target mediaidentity.Identity) bool {
	if !mayMatchMediaIdentity(item.MediaType, item.SeasonNumber, item.EpisodeNumber, item.ExternalIDs, target) {
		return false
	}
	externalIDs := storageIdentityExternalIDs(item.MediaType, item.ItemID, item.SeriesID, item.ExternalIDs)
	current := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:     item.MediaType,
		ID:            item.ItemID,
		SeriesID:      item.SeriesID,
		SeasonNumber:  item.SeasonNumber,
		EpisodeNumber: item.EpisodeNumber,
		ExternalIDs:   externalIDs,
	})
	if current.Key == target.Key {
		return true
	}
	for _, key := range current.CandidateKeys {
		if key == target.Key {
			return true
		}
	}
	return mediaidentity.Equivalent(current, target)
}

func watchHistoryUpdateIdentity(update models.WatchHistoryUpdate) mediaidentity.Identity {
	externalIDs := storageIdentityExternalIDs(update.MediaType, update.ItemID, update.SeriesID, update.ExternalIDs)
	return mediaidentity.Resolve(mediaidentity.Input{
		MediaType:     update.MediaType,
		ID:            update.ItemID,
		SeriesID:      update.SeriesID,
		SeasonNumber:  update.SeasonNumber,
		EpisodeNumber: update.EpisodeNumber,
		ExternalIDs:   externalIDs,
	})
}

func bestWatchHistoryImportMatchLocked(perUser map[string]models.WatchHistoryItem, identity mediaidentity.Identity) (string, models.WatchHistoryItem, bool) {
	var bestKey string
	var best models.WatchHistoryItem
	found := false

	consider := func(key string, item models.WatchHistoryItem, force bool) {
		if mediaidentity.NormalizeMediaType(item.MediaType) != identity.MediaType {
			return
		}
		if identity.MediaType == "episode" && !sameEpisodeWatchHistoryImportSlot(item, identity) {
			return
		}
		if !force && !watchHistoryItemMatchesIdentity(item, identity) {
			return
		}
		if !found || preferWatchHistoryImportMatch(item, best) {
			bestKey = key
			best = item
			found = true
		}
	}

	if item, ok := perUser[identity.Key]; ok {
		consider(identity.Key, item, true)
	}
	for _, key := range identity.CandidateKeys {
		if key == identity.Key {
			continue
		}
		if item, ok := perUser[key]; ok {
			consider(key, item, true)
		}
	}
	for key, item := range perUser {
		consider(key, item, false)
	}
	if !found {
		return "", models.WatchHistoryItem{}, false
	}
	return bestKey, best, true
}

func sameEpisodeWatchHistoryImportSlot(item models.WatchHistoryItem, identity mediaidentity.Identity) bool {
	if identity.MediaType != "episode" {
		return true
	}
	if item.SeasonNumber <= 0 || item.EpisodeNumber <= 0 || identity.SeasonNumber <= 0 || identity.EpisodeNumber <= 0 {
		return true
	}
	return item.SeasonNumber == identity.SeasonNumber && item.EpisodeNumber == identity.EpisodeNumber
}

func preferWatchHistoryImportMatch(candidate, current models.WatchHistoryItem) bool {
	candidateTime := watchHistoryImportStateTime(candidate)
	currentTime := watchHistoryImportStateTime(current)
	if !candidateTime.Equal(currentTime) {
		return candidateTime.After(currentTime)
	}
	if candidate.Watched != current.Watched {
		return !candidate.Watched
	}
	return candidate.ID < current.ID
}

func watchHistoryImportStateTime(item models.WatchHistoryItem) time.Time {
	if !item.UpdatedAt.IsZero() {
		return item.UpdatedAt
	}
	return item.WatchedAt
}

func playbackProgressUpdateIdentity(update models.PlaybackProgressUpdate) mediaidentity.Identity {
	externalIDs := storageIdentityExternalIDs(update.MediaType, update.ItemID, update.SeriesID, update.ExternalIDs)
	return mediaidentity.Resolve(mediaidentity.Input{
		MediaType:     update.MediaType,
		ID:            update.ItemID,
		SeriesID:      update.SeriesID,
		SeasonNumber:  update.SeasonNumber,
		EpisodeNumber: update.EpisodeNumber,
		ExternalIDs:   externalIDs,
	})
}

// enrichPlaybackProgressFromHistoryLocked restores provider mappings that a
// player heartbeat does not carry. Details and sync imports often establish a
// richer canonical identity before playback begins; retaining those mappings
// keeps real-time scrobblers addressable for older and provider-specific
// clients without making the live path perform a remote metadata lookup.
func (s *Service) enrichPlaybackProgressFromHistoryLocked(userID string, update models.PlaybackProgressUpdate) models.PlaybackProgressUpdate {
	perUser := s.watchHistory[userID]
	if len(perUser) == 0 {
		return update
	}

	identity := playbackProgressUpdateIdentity(update)
	_, item, found := bestWatchHistoryImportMatchLocked(perUser, identity)
	if !found {
		return update
	}

	update.ExternalIDs = unionExternalIDs(item.ExternalIDs, update.ExternalIDs)
	return normalizePlaybackProgressUpdate(update)
}

// canonicalWatchHistorySurvivor decides which storage key a matched watch-history
// row keeps when an update arrives under a different identity key. The stored
// row wins when its provider form is preferred (mirrors ImportWatchHistory's
// dedup) or when the match came through an episode-scoped alias with different
// numbering (absolute vs aired order), in which case the stored canonical
// season/episode are copied back onto the update so the row stays coherent.
func canonicalWatchHistorySurvivor(item models.WatchHistoryItem, exists bool, identity mediaidentity.Identity, update *models.WatchHistoryUpdate) (key, itemID string) {
	key = identity.Key
	itemID = identity.ID
	if !exists {
		return key, itemID
	}
	existingKey := makeWatchKey(item.MediaType, item.ItemID)
	if existingKey == "" || existingKey == key {
		return key, itemID
	}

	keepExisting := false
	if identity.MediaType == "episode" && item.SeasonNumber > 0 && item.EpisodeNumber > 0 &&
		(item.SeasonNumber != identity.SeasonNumber || item.EpisodeNumber != identity.EpisodeNumber) {
		keepExisting = true
	} else if preferProgressID(item.ItemID, identity.ID) {
		keepExisting = true
	}
	if !keepExisting {
		return key, itemID
	}

	key = existingKey
	itemID = item.ItemID
	if update != nil && identity.MediaType == "episode" {
		if item.SeasonNumber > 0 || item.EpisodeNumber > 0 {
			update.SeasonNumber = item.SeasonNumber
			update.EpisodeNumber = item.EpisodeNumber
		}
		if item.SeriesID != "" {
			update.SeriesID = item.SeriesID
		}
	}
	return key, itemID
}

// mergeWatchHistoryExternalIDs folds the update's external IDs into the stored
// row without dropping previously-known mappings. Incoming values win per key;
// keys absent from the update are preserved so a narrower update (e.g. a
// tmdb-only client) cannot strip the imdb/tvdb bridges that later alias-form
// lookups depend on.
func mergeWatchHistoryExternalIDs(item *models.WatchHistoryItem, incoming map[string]string) {
	if len(incoming) == 0 {
		return
	}
	if item.ExternalIDs == nil {
		item.ExternalIDs = make(map[string]string, len(incoming))
	}
	for k, v := range incoming {
		if v != "" {
			item.ExternalIDs[k] = v
		}
	}
}

// isAddressableEpisodeUpdate reports whether an episode write can be tied to a
// concrete episode. Season 0 (specials) is valid as long as an episode number,
// an sNNeNN item-id suffix, or an episode-scoped external ID is present.
func isAddressableEpisodeUpdate(mediaType, itemID string, episodeNumber int, externalIDs map[string]string) bool {
	if mediaidentity.NormalizeMediaType(mediaType) != "episode" {
		return true
	}
	if episodeNumber > 0 {
		return true
	}
	if _, _, _, ok := mediaidentity.ParseEpisodeID(itemID); ok {
		return true
	}
	return hasEpisodeScopedExternalIDs(mediaidentity.NormalizeExternalIDs(externalIDs))
}

func takeWatchHistoryItemLocked(perUser map[string]models.WatchHistoryItem, identity mediaidentity.Identity) (models.WatchHistoryItem, bool) {
	for _, key := range identity.CandidateKeys {
		item, ok := perUser[key]
		if !ok {
			continue
		}
		if key != identity.Key {
			delete(perUser, key)
		}
		return item, true
	}
	for key, item := range perUser {
		if !watchHistoryItemMatchesIdentity(item, identity) {
			continue
		}
		if key != identity.Key {
			delete(perUser, key)
		}
		return item, true
	}
	return models.WatchHistoryItem{}, false
}

func playbackProgressMatchesIdentity(progress models.PlaybackProgress, target mediaidentity.Identity) bool {
	if !mayMatchMediaIdentity(progress.MediaType, progress.SeasonNumber, progress.EpisodeNumber, progress.ExternalIDs, target) {
		return false
	}
	externalIDs := storageIdentityExternalIDs(progress.MediaType, progress.ItemID, progress.SeriesID, progress.ExternalIDs)
	current := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:     progress.MediaType,
		ID:            progress.ItemID,
		SeriesID:      progress.SeriesID,
		SeasonNumber:  progress.SeasonNumber,
		EpisodeNumber: progress.EpisodeNumber,
		ExternalIDs:   externalIDs,
	})
	if current.Key == target.Key {
		return true
	}
	for _, key := range current.CandidateKeys {
		if key == target.Key {
			return true
		}
	}
	return mediaidentity.Equivalent(current, target)
}

// progressSeriesMatchesIdentity reports whether a playback-progress row belongs
// to the series described by target. It resolves the row's series identity from
// its SeriesID (or the series portion of an episode item ID, or the item ID
// itself for series-level markers) plus its external IDs.
func progressSeriesMatchesIdentity(progress models.PlaybackProgress, target mediaidentity.Identity) bool {
	seriesID := strings.TrimSpace(progress.SeriesID)
	if seriesID == "" {
		seriesID = mediaidentity.InferSeriesIDFromEpisodeItemID(progress.ItemID)
	}
	if seriesID == "" && progress.MediaType == "episode" && progress.SeasonNumber == 0 && progress.EpisodeNumber == 0 {
		seriesID = strings.TrimSpace(progress.ItemID)
	}
	if seriesID == "" {
		return false
	}
	current := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   "series",
		ID:          seriesID,
		ExternalIDs: canonicalSeriesExternalIDs(seriesID, progress.ItemID, progress.ExternalIDs),
	})
	return identitiesReferToSameTitle(current, target)
}

// Playback Progress Methods

// UpdatePlaybackProgress updates the playback progress for a media item using
// a background context for compatibility with non-HTTP callers.
func (s *Service) UpdatePlaybackProgress(userID string, update models.PlaybackProgressUpdate) (models.PlaybackProgress, error) {
	return s.UpdatePlaybackProgressContext(context.Background(), userID, update)
}

// acquirePlaybackProgressGate keeps heartbeat writers out of the shared history
// mutex queue until they are the next writer. Unlike sync.RWMutex.Lock, the gate
// wait is interruptible, so disconnected clients disappear immediately instead
// of accumulating behind a slow persistence operation. Once admitted, at most
// one heartbeat can be waiting on the history mutex, preserving RWMutex writer
// fairness without a polling TryLock loop.
func (s *Service) acquirePlaybackProgressGate(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.playbackProgressOnce.Do(func() {
		s.playbackProgressGate = make(chan struct{}, 1)
	})
	select {
	case s.playbackProgressGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) releasePlaybackProgressGate() {
	<-s.playbackProgressGate
}

// UpdatePlaybackProgressContext updates playback progress and propagates caller
// cancellation through lock acquisition and PostgreSQL persistence. It
// automatically marks items as watched when they reach 90% completion.
func (s *Service) UpdatePlaybackProgressContext(ctx context.Context, userID string, update models.PlaybackProgressUpdate) (models.PlaybackProgress, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	startedAt := time.Now()
	lockWaitStartedAt := startedAt
	lockWait := time.Duration(0)
	persistDuration := time.Duration(0)
	defer func() {
		total := time.Since(startedAt)
		if total >= slowPlaybackProgressThreshold {
			log.Printf("[history] slow playback progress update user=%s mediaType=%s itemID=%s total=%s lockWait=%s persist=%s",
				userID, update.MediaType, update.ItemID, total.Round(time.Millisecond), lockWait.Round(time.Millisecond), persistDuration.Round(time.Millisecond))
		}
	}()

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

	update = normalizePlaybackProgressUpdate(update)
	if !isAddressableEpisodeUpdate(update.MediaType, update.ItemID, update.EpisodeNumber, update.ExternalIDs) {
		return models.PlaybackProgress{}, ErrEpisodeNotAddressable
	}
	isLiveProgress := isLiveProgressUpdate(update)
	// Untagged library media (no IMDB/TMDB/TVDB) is progress-only: keep resume
	// positions but do not feed continue watching, watched history, or scrobble.
	isUntaggedProgress := !isLiveProgress && isUntaggedMediaProgressUpdate(update)
	excludeFromHistoryShelves := isLiveProgress || isUntaggedProgress
	scrobbleStartDelay := time.Duration(0)
	if !excludeFromHistoryShelves {
		scrobbleStartDelay = s.resolveRealtimeScrobbleStartDelay(userID, update)
	}

	if err := s.acquirePlaybackProgressGate(ctx); err != nil {
		lockWait = time.Since(lockWaitStartedAt)
		return models.PlaybackProgress{}, err
	}
	defer s.releasePlaybackProgressGate()
	s.mu.Lock()
	lockWait = time.Since(lockWaitStartedAt)
	defer s.mu.Unlock()
	// Bump on entry so every exit path (including early returns and persistence
	// failures after in-memory changes) invalidates derived watch state.
	s.bumpWatchStateRevisionLocked(userID)
	if err := ctx.Err(); err != nil {
		return models.PlaybackProgress{}, err
	}
	update = s.enrichPlaybackProgressFromHistoryLocked(userID, update)

	perUser := s.ensurePlaybackProgressUserLocked(userID)
	staleWatchedEpisodeUpdate := s.isWatchedEpisodeProgressUpdateLocked(userID, update)
	// Normalize itemID to lowercase for consistent key matching
	normalizedItemID := update.ItemID
	key := makeWatchKey(update.MediaType, normalizedItemID)
	_, hadExistingProgress := findMatchingPlaybackProgress(perUser, key, update)
	watchStateChanged := !hadExistingProgress

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

	if !hasMeaningfulProgressValues(update.Position, percentWatched) {
		if existing, ok := findMatchingMeaningfulPlaybackProgress(perUser, key, update); ok {
			log.Printf("[history] ignored zero playback progress overwrite user=%s mediaType=%s itemID=%s existingItemID=%s existingPosition=%.3f existingPercent=%.3f",
				userID, update.MediaType, update.ItemID, existing.ItemID, existing.Position, existing.PercentWatched)
			return existing, nil
		}
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
		IsBuffering:    update.IsBuffering,
		ExternalIDs:    update.ExternalIDs,
		SeasonNumber:   update.SeasonNumber,
		EpisodeNumber:  update.EpisodeNumber,
		SeriesID:       update.SeriesID,
		SeriesName:     update.SeriesName,
		EpisodeName:    update.EpisodeName,
		MovieName:      update.MovieName,
		Year:           update.Year,
	}

	// Collapse same-item progress stored under alternate ID schemes (split
	// series/movie IDs) onto a single deterministic key, preferring TMDB. This
	// keeps each episode/movie in a single "in progress" state regardless of
	// whether the player or a sync service wrote last. Storage only — episode
	// numbering and metadata are resolved at read time from external IDs and are
	// unaffected by which key survives.
	canonicalKey := key
	if progress.MediaType == "episode" && progress.SeasonNumber > 0 && progress.EpisodeNumber > 0 {
		var canonicalItemID, canonicalSeriesID string
		canonicalKey, canonicalItemID, canonicalSeriesID, progress.ExternalIDs =
			s.consolidateEpisodeProgressLocked(userID, perUser, key, normalizedItemID, progress.SeriesID, progress.SeasonNumber, progress.EpisodeNumber, progress.ExternalIDs)
		progress.ItemID = canonicalItemID
		if canonicalSeriesID != "" {
			progress.SeriesID = canonicalSeriesID
		}
	} else if progress.MediaType == "movie" {
		var canonicalItemID string
		canonicalKey, canonicalItemID, progress.ExternalIDs =
			s.consolidateMovieProgressLocked(userID, perUser, key, normalizedItemID, progress.ExternalIDs)
		progress.ItemID = canonicalItemID
	}
	progress.ID = canonicalKey
	if existing, ok := perUser[canonicalKey]; ok {
		progress.WatchedSeconds = accumulatedWatchedSeconds(existing, progress)
	} else {
		progress.WatchedSeconds = initialWatchedSeconds(progress)
	}

	// If consolidation chose an existing canonical key, drop any stale row under
	// the incoming key so a duplicate isn't left behind.
	if canonicalKey != key {
		if entry, exists := perUser[key]; exists {
			s.removePlaybackProgressEntryLocked(userID, perUser, key, entry)
			s.clearActivePlaybackProgressKeyLocked(userID, key)
		} else {
			s.clearActivePlaybackProgressKeyLocked(userID, key)
		}
	}
	perUser[canonicalKey] = progress
	if !staleWatchedEpisodeUpdate {
		if removed := clearSupersededSeriesHiddenMarkers(perUser, progress); removed > 0 {
			watchStateChanged = true
			log.Printf("[history] removed %d stale hidden marker(s) after newer playback user=%s itemID=%s", removed, userID, progress.ItemID)
		}
	}

	// Mirror the heartbeat into the active-progress map so the active-stream
	// dashboard can keep tracking position even after the row above is cleared by
	// the 90% auto-watched marking below. This is in-memory live state only.
	s.recordActiveProgressLocked(userID, progress)

	// Clear hidden flag for related series entries when new progress is logged.
	// Live TV and untagged library media do not participate in continue-watching state.
	if !excludeFromHistoryShelves && update.SeriesID != "" {
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
					watchStateChanged = true
					continue
				}
				existingProg.HiddenFromContinueWatching = false
				perUser[existingKey] = existingProg
				watchStateChanged = true
			}
		}
	}

	persistStartedAt := time.Now()
	if err := s.savePlaybackProgressLockedContext(ctx); err != nil {
		persistDuration = time.Since(persistStartedAt)
		return models.PlaybackProgress{}, err
	}
	persistDuration = time.Since(persistStartedAt)

	if !excludeFromHistoryShelves {
		// Continue Watching includes the current position, so refresh it on every
		// heartbeat. Notify calendar and other broad watch-state consumers only
		// when membership/visibility can change, not for position-only updates.
		// A threshold-crossing update is followed by UpdateWatchHistory below,
		// which performs the broad notification after it clears progress. Keep
		// this stage cache-only so that transition emits one notification.
		if watchStateChanged && percentWatched < 90 {
			s.invalidateContinueWatchingLocked(userID)
		} else {
			s.invalidateContinueWatchingCacheLocked(userID)
		}
	}

	// Grab real-time scrobbler reference while holding the lock
	rtScrobbler := s.traktRTScrobbler
	realtimePlaybackObserver := s.realtimePlaybackObserver
	allowRealtimeScrobble := !excludeFromHistoryShelves && !isLiveTVRecordingProgressUpdate(update)
	// Disable realtime scrobbling for episodes under a non-official ordering
	// (season/episode numbers don't match the canonical order used for sync).
	if allowRealtimeScrobble && update.MediaType == "episode" {
		if s.seriesOrderingIsAlternate(userID, seriesTVDBIDFromUpdate(update)) {
			allowRealtimeScrobble = false
		}
	}
	realtimeScrobbleStarted := false
	if rtScrobbler != nil && allowRealtimeScrobble {
		realtimeScrobbleStarted = s.shouldEmitRealtimeScrobbleLocked(userID, update, scrobbleStartDelay, time.Now())
	} else if update.PlaybackEnded {
		s.clearScrobbleStartDelaySessionLocked(userID, update)
	}

	// Auto-mark as watched if >= 90% complete
	if !excludeFromHistoryShelves && percentWatched >= 90 {
		// Local watched-history sync below writes the Trakt watched event. Clear
		// any active realtime session so we don't also create a scrobble event.
		if rtScrobbler != nil && allowRealtimeScrobble && realtimeScrobbleStarted {
			if update.PlaybackEnded {
				rtScrobbler.StopSession(userID, update, percentWatched)
			} else {
				rtScrobbler.ClearSession(userID, update)
			}
		}
		if realtimePlaybackObserver != nil && allowRealtimeScrobble {
			if update.PlaybackEnded {
				go realtimePlaybackObserver.StopSession(userID, update, percentWatched)
			} else {
				go realtimePlaybackObserver.ClearSession(userID, update)
			}
		}
		s.clearScrobbleStartDelaySessionLocked(userID, update)

		s.mu.Unlock() // Unlock before calling other methods
		err := s.markAsWatchedFromProgress(userID, update, progress.WatchedSeconds)
		s.mu.Lock() // Re-lock after
		if err != nil {
			// Log but don't fail the progress update
			fmt.Printf("Warning: failed to auto-mark as watched: %v\n", err)
		}
	} else if allowRealtimeScrobble {
		if update.PlaybackEnded {
			if rtScrobbler != nil && realtimeScrobbleStarted {
				go rtScrobbler.StopSession(userID, update, percentWatched)
			}
			if realtimePlaybackObserver != nil {
				go realtimePlaybackObserver.StopSession(userID, update, percentWatched)
			}
		} else {
			// Mirror source-server playback immediately; external scrobble events
			// wait until the configured active-playback delay has elapsed.
			if rtScrobbler != nil && realtimeScrobbleStarted {
				go rtScrobbler.HandleProgressUpdate(userID, update, percentWatched)
			}
			if realtimePlaybackObserver != nil {
				go realtimePlaybackObserver.HandleProgressUpdate(userID, update, percentWatched)
			}
		}
	}

	return progress, nil
}

func (s *Service) isWatchedProgressUpdateLocked(userID string, update models.PlaybackProgressUpdate) bool {
	perUserHistory, ok := s.watchHistory[userID]
	if !ok || len(perUserHistory) == 0 {
		return false
	}

	update = normalizePlaybackProgressUpdate(update)
	target := playbackProgressUpdateIdentity(update)

	// Fast path: stored keys are canonical after load normalization, so the
	// vast majority of watched rows are found by direct candidate-key lookup
	// without scanning.
	for _, key := range target.CandidateKeys {
		if item, ok := perUserHistory[key]; ok && item.Watched {
			return true
		}
	}

	for _, item := range perUserHistory {
		if !item.Watched || item.MediaType != target.MediaType {
			continue
		}
		if watchHistoryItemMatchesIdentity(item, target) {
			return true
		}
	}

	return false
}

func (s *Service) isWatchedEpisodeProgressUpdateLocked(userID string, update models.PlaybackProgressUpdate) bool {
	if strings.ToLower(strings.TrimSpace(update.MediaType)) != "episode" {
		return false
	}
	if update.SeasonNumber <= 0 || update.EpisodeNumber <= 0 {
		return false
	}

	if s.isWatchedProgressUpdateLocked(userID, update) {
		return true
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

// IsWatchedProgressUpdate reports whether the supplied playback update refers
// to a movie or episode already marked watched in local history.
func (s *Service) IsWatchedProgressUpdate(userID string, update models.PlaybackProgressUpdate) bool {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.isWatchedProgressUpdateLocked(userID, update)
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
func (s *Service) markAsWatchedFromProgress(userID string, update models.PlaybackProgressUpdate, watchedSeconds float64) error {
	watched := true
	historyUpdate := models.WatchHistoryUpdate{
		MediaType:      update.MediaType,
		ItemID:         update.ItemID,
		Watched:        &watched,
		WatchedSeconds: watchedSeconds,
		Source:         models.WatchSourcePlayed,
		ExternalIDs:    update.ExternalIDs,
		SeasonNumber:   update.SeasonNumber,
		EpisodeNumber:  update.EpisodeNumber,
		SeriesID:       update.SeriesID,
		SeriesName:     update.SeriesName,
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

	identity := mediaidentity.Resolve(mediaidentity.Input{MediaType: mediaType, ID: itemID})
	if perUser, ok := s.playbackProgress[userID]; ok {
		for _, key := range identity.CandidateKeys {
			if progress, ok := perUser[key]; ok {
				return &progress, nil
			}
		}
		for _, progress := range perUser {
			if playbackProgressMatchesIdentity(progress, identity) {
				return &progress, nil
			}
		}
		if progress, ok := perUser[identity.Key]; ok {
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

	// Read under the shared lock so concurrent shelf requests don't serialize
	// (or block progress writes). Expired live heartbeats are skipped here and
	// only pruned below, under the write lock, when some actually exist.
	s.mu.RLock()
	now := time.Now()
	merged := make(map[string]models.PlaybackProgress)
	if perUser, ok := s.playbackProgress[userID]; ok {
		for _, progress := range perUser {
			merged[progress.ID] = progress
		}
	}
	hasExpiredActive := false
	if perUser, ok := s.activePlaybackProgress[userID]; ok {
		for _, progress := range perUser {
			if now.Sub(progress.UpdatedAt) > activeProgressTTL {
				hasExpiredActive = true
				continue
			}
			existing, ok := merged[progress.ID]
			if !ok {
				continue
			}
			if preferPlaybackProgress(progress, existing) {
				merged[progress.ID] = progress
			}
		}
		if len(perUser) == 0 {
			hasExpiredActive = true
		}
	}

	items := make([]models.PlaybackProgress, 0, len(merged))
	for _, progress := range merged {
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
	s.mu.RUnlock()

	if hasExpiredActive {
		s.pruneExpiredActiveProgress(userID)
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

// pruneExpiredActiveProgress drops live heartbeats older than activeProgressTTL
// for userID. Expiry is re-checked under the write lock because a heartbeat may
// have refreshed an entry since the caller observed it as expired. Pruning
// does not change ListPlaybackProgress output (expired entries are already
// ignored on read), so it does not bump the watch state revision.
func (s *Service) pruneExpiredActiveProgress(userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	perUser, ok := s.activePlaybackProgress[userID]
	if !ok {
		return
	}
	now := time.Now()
	for key, progress := range perUser {
		if now.Sub(progress.UpdatedAt) > activeProgressTTL {
			delete(perUser, key)
		}
	}
	if len(perUser) == 0 {
		delete(s.activePlaybackProgress, userID)
	}
}

func preferPlaybackProgress(candidate, existing models.PlaybackProgress) bool {
	if candidate.UpdatedAt.After(existing.UpdatedAt) {
		return true
	}
	if existing.UpdatedAt.After(candidate.UpdatedAt) {
		return false
	}
	return candidate.PercentWatched > existing.PercentWatched
}

func hasMeaningfulProgressValues(position, percentWatched float64) bool {
	return position > 0.5 || percentWatched > 0.1
}

const maxWatchedProgressDeltaSeconds = 5 * 60

func initialWatchedSeconds(progress models.PlaybackProgress) float64 {
	if progress.IsPaused || progress.Position <= 0 {
		return 0
	}
	if progress.Position > maxWatchedProgressDeltaSeconds {
		return 0
	}
	if progress.Duration > 0 && progress.Position > progress.Duration {
		return progress.Duration
	}
	return progress.Position
}

func accumulatedWatchedSeconds(existing, next models.PlaybackProgress) float64 {
	total := existing.WatchedSeconds
	if next.IsPaused {
		return clampWatchedSeconds(total, next.Duration)
	}
	if !next.UpdatedAt.IsZero() && !existing.UpdatedAt.IsZero() && next.UpdatedAt.Before(existing.UpdatedAt) {
		return clampWatchedSeconds(total, next.Duration)
	}
	delta := next.Position - existing.Position
	if delta <= 0 || delta > maxWatchedProgressDeltaSeconds {
		return clampWatchedSeconds(total, next.Duration)
	}
	return clampWatchedSeconds(total+delta, next.Duration)
}

func clampWatchedSeconds(seconds, duration float64) float64 {
	if seconds < 0 {
		return 0
	}
	if duration > 0 && seconds > duration {
		return duration
	}
	return seconds
}

func findMatchingMeaningfulPlaybackProgress(perUser map[string]models.PlaybackProgress, incomingKey string, update models.PlaybackProgressUpdate) (models.PlaybackProgress, bool) {
	var best models.PlaybackProgress
	found := false
	for key, progress := range perUser {
		if !hasMeaningfulProgressValues(progress.Position, progress.PercentWatched) {
			continue
		}
		if !playbackProgressMatchesUpdate(key, progress, incomingKey, update) {
			continue
		}
		if !found || preferPlaybackProgress(progress, best) {
			best = progress
			found = true
		}
	}
	return best, found
}

func findMatchingPlaybackProgress(perUser map[string]models.PlaybackProgress, incomingKey string, update models.PlaybackProgressUpdate) (models.PlaybackProgress, bool) {
	var best models.PlaybackProgress
	found := false
	for key, progress := range perUser {
		if !playbackProgressMatchesUpdate(key, progress, incomingKey, update) {
			continue
		}
		if !found || preferPlaybackProgress(progress, best) {
			best = progress
			found = true
		}
	}
	return best, found
}

func playbackProgressMatchesUpdate(existingKey string, progress models.PlaybackProgress, incomingKey string, update models.PlaybackProgressUpdate) bool {
	if strings.ToLower(progress.MediaType) != strings.ToLower(update.MediaType) {
		return false
	}
	if strings.ToLower(existingKey) == strings.ToLower(incomingKey) {
		return true
	}
	if strings.EqualFold(progress.ItemID, update.ItemID) {
		return true
	}

	switch strings.ToLower(update.MediaType) {
	case "movie":
		return hasMatchingExternalID(progress.ExternalIDs, update.ExternalIDs)
	case "episode":
		if progress.SeasonNumber != update.SeasonNumber || progress.EpisodeNumber != update.EpisodeNumber {
			return false
		}
		if progress.SeriesID != "" && update.SeriesID != "" && strings.EqualFold(progress.SeriesID, update.SeriesID) {
			return true
		}
		return hasMatchingExternalID(progress.ExternalIDs, update.ExternalIDs)
	default:
		return false
	}
}

// DeletePlaybackProgress removes playback progress for a specific media item.
func (s *Service) DeletePlaybackProgress(userID, mediaType, itemID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ErrUserIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.bumpWatchStateRevisionLocked(userID)

	identity := mediaidentity.Resolve(mediaidentity.Input{MediaType: mediaType, ID: itemID})
	removed := false
	if perUser, ok := s.playbackProgress[userID]; ok {
		// An explicit delete that targets a pure hidden marker (no playback
		// position, just hidden state) removes it outright; re-creating the
		// marker here would make junk markers impossible to ever clean up.
		removeEntry := func(key string, entry models.PlaybackProgress) {
			if entry.HiddenFromContinueWatching && entry.Position == 0 {
				delete(perUser, key)
				return
			}
			s.removePlaybackProgressEntryLocked(userID, perUser, key, entry)
		}
		for _, key := range identity.CandidateKeys {
			if entry, exists := perUser[key]; exists {
				removeEntry(key, entry)
				removed = true
			}
		}
		for key, entry := range perUser {
			if !playbackProgressMatchesIdentity(entry, identity) {
				continue
			}
			removeEntry(key, entry)
			removed = true
		}
		s.clearActivePlaybackProgressIdentityLocked(userID, identity)
		// Invalidate continue watching cache for this user since progress changed
		if removed {
			s.invalidateContinueWatchingLocked(userID)
			return s.savePlaybackProgressLocked()
		}
		return nil
	}
	s.clearActivePlaybackProgressIdentityLocked(userID, identity)

	return nil
}

// ClearPlaybackProgress removes saved resume positions for every profile and
// returns the number of entries removed.
func (s *Service) ClearPlaybackProgress() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bumpAllWatchStateRevisionsLocked()

	count := 0
	for _, perUser := range s.playbackProgress {
		count += len(perUser)
	}
	previous := s.playbackProgress
	s.playbackProgress = make(map[string]map[string]models.PlaybackProgress)
	if err := s.savePlaybackProgressLocked(); err != nil {
		s.playbackProgress = previous
		return 0, err
	}
	s.activePlaybackProgress = make(map[string]map[string]models.PlaybackProgress)
	s.continueWatchingCache = make(map[string]*cachedContinueWatching)
	return count, nil
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
	s.bumpAllWatchStateRevisionsLocked()

	if s.useDB() {
		loadStarted := time.Now()
		ctx := context.Background()
		queryStarted := time.Now()
		allItems, err := s.store.PlaybackProgress().ListAll(ctx)
		if err != nil {
			return fmt.Errorf("load playback progress from db: %w", err)
		}
		queryDuration := time.Since(queryStarted)
		processingStarted := time.Now()
		s.playbackProgress = make(map[string]map[string]models.PlaybackProgress, len(allItems))
		persisted := make(map[historyPersistenceKey][sha256.Size]byte)
		rowCount := 0
		for userID, items := range allItems {
			rowCount += len(items)
			perUser := make(map[string]models.PlaybackProgress, len(items))
			for _, item := range items {
				fingerprint, err := playbackProgressPersistenceFingerprint(item)
				if err != nil {
					return fmt.Errorf("snapshot loaded playback progress item %s: %w", item.ID, err)
				}
				persisted[historyPersistenceKey{userID: userID, itemID: item.ID}] = fingerprint
				item = normalizePlaybackProgressItem(item)
				key := item.ID
				perUser[key] = item
			}
			s.playbackProgress[userID] = perUser
		}
		s.persistedPlaybackProgress = persisted
		staleMarkersRemoved := 0
		for _, perUser := range s.playbackProgress {
			staleMarkersRemoved += clearSupersededSeriesHiddenMarkersFromStoredProgress(perUser)
		}
		if staleMarkersRemoved > 0 {
			if err := s.syncProgressToDBContext(ctx); err != nil {
				return fmt.Errorf("remove stale playback-progress hide markers: %w", err)
			}
			log.Printf("[history] removed %d stale hidden marker(s) during startup load", staleMarkersRemoved)
		}
		log.Printf("[history] Startup load completed table=playback_progress rows=%d users=%d query=%s processing=%s total=%s",
			rowCount, len(allItems), queryDuration, time.Since(processingStarted), time.Since(loadStarted))
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

			normalized := normalizePlaybackProgressItem(item)
			if normalized.ID != item.ID || normalized.ItemID != item.ItemID || normalized.SeriesID != item.SeriesID {
				needsSave = true
			}
			item = normalized
			key := item.ID
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
	for _, perUser := range s.playbackProgress {
		if clearSupersededSeriesHiddenMarkersFromStoredProgress(perUser) > 0 {
			needsSave = true
		}
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
	return s.savePlaybackProgressLockedContext(context.Background())
}

func (s *Service) savePlaybackProgressLockedContext(ctx context.Context) error {
	if s.useDB() {
		return s.syncProgressToDBContext(ctx)
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

func watchHistoryPersistenceFingerprint(item models.WatchHistoryItem) ([sha256.Size]byte, error) {
	data, err := json.Marshal(item)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(data), nil
}

func playbackProgressPersistenceFingerprint(item models.PlaybackProgress) ([sha256.Size]byte, error) {
	// Runtime playback-control fields are deliberately excluded because the
	// PostgreSQL repository does not persist them.
	item.IsBuffering = false
	item.AllowedToContinue = nil
	item.MigrationRequested = false
	item.MigrationReason = ""
	item.MigrationPreparationRequested = false
	item.MigrationPreparationReason = ""

	data, err := json.Marshal(item)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(data), nil
}

func watchHistoryPersistenceFingerprints(items map[string]map[string]models.WatchHistoryItem) (map[historyPersistenceKey][sha256.Size]byte, error) {
	out := make(map[historyPersistenceKey][sha256.Size]byte)
	for userID, perUser := range items {
		for itemID, item := range perUser {
			fingerprint, err := watchHistoryPersistenceFingerprint(item)
			if err != nil {
				return nil, err
			}
			out[historyPersistenceKey{userID: userID, itemID: itemID}] = fingerprint
		}
	}
	return out, nil
}

func playbackProgressPersistenceFingerprints(items map[string]map[string]models.PlaybackProgress) (map[historyPersistenceKey][sha256.Size]byte, error) {
	out := make(map[historyPersistenceKey][sha256.Size]byte)
	for userID, perUser := range items {
		for itemID, item := range perUser {
			fingerprint, err := playbackProgressPersistenceFingerprint(item)
			if err != nil {
				return nil, err
			}
			out[historyPersistenceKey{userID: userID, itemID: itemID}] = fingerprint
		}
	}
	return out, nil
}

func planWatchHistoryPersistence(
	current map[string]map[string]models.WatchHistoryItem,
	persisted map[historyPersistenceKey][sha256.Size]byte,
) ([]watchHistoryPersistenceUpsert, []historyPersistenceKey, error) {
	seen := make(map[historyPersistenceKey]struct{})
	var upserts []watchHistoryPersistenceUpsert
	for userID, perUser := range current {
		for itemID, item := range perUser {
			key := historyPersistenceKey{userID: userID, itemID: itemID}
			seen[key] = struct{}{}
			fingerprint, err := watchHistoryPersistenceFingerprint(item)
			if err != nil {
				return nil, nil, err
			}
			if previous, ok := persisted[key]; ok && previous == fingerprint {
				continue
			}
			upserts = append(upserts, watchHistoryPersistenceUpsert{
				key: key, item: item, fingerprint: fingerprint,
			})
		}
	}

	var deletes []historyPersistenceKey
	for key := range persisted {
		if _, ok := seen[key]; !ok {
			deletes = append(deletes, key)
		}
	}
	return upserts, deletes, nil
}

func planPlaybackProgressPersistence(
	current map[string]map[string]models.PlaybackProgress,
	persisted map[historyPersistenceKey][sha256.Size]byte,
) ([]playbackProgressPersistenceUpsert, []historyPersistenceKey, error) {
	seen := make(map[historyPersistenceKey]struct{})
	var upserts []playbackProgressPersistenceUpsert
	for userID, perUser := range current {
		for itemID, item := range perUser {
			key := historyPersistenceKey{userID: userID, itemID: itemID}
			seen[key] = struct{}{}
			fingerprint, err := playbackProgressPersistenceFingerprint(item)
			if err != nil {
				return nil, nil, err
			}
			if previous, ok := persisted[key]; ok && previous == fingerprint {
				continue
			}
			upserts = append(upserts, playbackProgressPersistenceUpsert{
				key: key, item: item, fingerprint: fingerprint,
			})
		}
	}

	var deletes []historyPersistenceKey
	for key := range persisted {
		if _, ok := seen[key]; !ok {
			deletes = append(deletes, key)
		}
	}
	return upserts, deletes, nil
}

// syncWatchedToDB writes only changed and deleted watch-history rows.
func (s *Service) syncWatchedToDB() error {
	ctx := context.Background()
	upserts, deletes, err := planWatchHistoryPersistence(s.watchHistory, s.persistedWatchHistory)
	if err != nil {
		return fmt.Errorf("plan watch history persistence: %w", err)
	}
	if len(upserts) == 0 && len(deletes) == 0 {
		return nil
	}

	if err := s.store.WithTx(ctx, func(tx *datastore.Tx) error {
		for i := range upserts {
			change := &upserts[i]
			if err := tx.WatchHistory().Upsert(ctx, change.key.userID, &change.item); err != nil {
				return err
			}
		}
		for _, key := range deletes {
			if err := tx.WatchHistory().Delete(ctx, key.userID, key.itemID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}

	if s.persistedWatchHistory == nil {
		s.persistedWatchHistory = make(map[historyPersistenceKey][sha256.Size]byte)
	}
	for _, change := range upserts {
		s.persistedWatchHistory[change.key] = change.fingerprint
	}
	for _, key := range deletes {
		delete(s.persistedWatchHistory, key)
	}
	return nil
}

// syncProgressToDB writes only changed and deleted playback-progress rows.
func (s *Service) syncProgressToDB() error {
	return s.syncProgressToDBContext(context.Background())
}

func (s *Service) syncProgressToDBContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	upserts, deletes, err := planPlaybackProgressPersistence(s.playbackProgress, s.persistedPlaybackProgress)
	if err != nil {
		return fmt.Errorf("plan playback progress persistence: %w", err)
	}
	if len(upserts) == 0 && len(deletes) == 0 {
		return nil
	}

	if err := s.store.WithTx(ctx, func(tx *datastore.Tx) error {
		for i := range upserts {
			change := &upserts[i]
			if err := tx.PlaybackProgress().Upsert(ctx, change.key.userID, &change.item); err != nil {
				return err
			}
		}
		for _, key := range deletes {
			if err := tx.PlaybackProgress().Delete(ctx, key.userID, key.itemID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}

	if s.persistedPlaybackProgress == nil {
		s.persistedPlaybackProgress = make(map[historyPersistenceKey][sha256.Size]byte)
	}
	for _, change := range upserts {
		s.persistedPlaybackProgress[change.key] = change.fingerprint
	}
	for _, key := range deletes {
		delete(s.persistedPlaybackProgress, key)
	}
	return nil
}

// activeProgressTTL bounds how long a heartbeat lingers in activePlaybackProgress
// after the last update. It must exceed the dashboard's own heartbeat-ended
// threshold so the dashboard, not this map, decides when a stream has stopped.
const activeProgressTTL = 2 * time.Minute

// recordActiveProgressLocked stores the latest heartbeat for an item in the
// active-progress map. Callers must hold s.mu.
func (s *Service) recordActiveProgressLocked(userID string, progress models.PlaybackProgress) {
	if s.activePlaybackProgress == nil {
		s.activePlaybackProgress = make(map[string]map[string]models.PlaybackProgress)
	}
	perUser, ok := s.activePlaybackProgress[userID]
	if !ok {
		perUser = make(map[string]models.PlaybackProgress)
		s.activePlaybackProgress[userID] = perUser
	}
	perUser[progress.ID] = progress
}

// ListAllPlaybackProgress returns playback progress for the active-stream
// dashboard. Live heartbeats (activePlaybackProgress) are overlaid on the stored
// progress so position keeps advancing past the 90% auto-watched threshold,
// which clears the underlying row. This method is the dashboard feed only;
// continue-watching uses GetContinueWatching.
func (s *Service) ListAllPlaybackProgress() map[string][]models.PlaybackProgress {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	merged := make(map[string]map[string]models.PlaybackProgress)
	for userID, perUser := range s.playbackProgress {
		for _, progress := range perUser {
			// Only include items that haven't been hidden from continue watching
			if progress.HiddenFromContinueWatching {
				continue
			}
			if merged[userID] == nil {
				merged[userID] = make(map[string]models.PlaybackProgress)
			}
			merged[userID][progress.ID] = progress
		}
	}

	// Overlay fresher live heartbeats, pruning stale entries as we go.
	for userID, perUser := range s.activePlaybackProgress {
		for key, progress := range perUser {
			if now.Sub(progress.UpdatedAt) > activeProgressTTL {
				delete(perUser, key)
				continue
			}
			if merged[userID] == nil {
				merged[userID] = make(map[string]models.PlaybackProgress)
			}
			merged[userID][progress.ID] = progress
		}
		if len(perUser) == 0 {
			delete(s.activePlaybackProgress, userID)
		}
	}

	result := make(map[string][]models.PlaybackProgress, len(merged))
	for userID, perUser := range merged {
		items := make([]models.PlaybackProgress, 0, len(perUser))
		for _, progress := range perUser {
			items = append(items, progress)
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

	identity := mediaidentity.Resolve(mediaidentity.Input{MediaType: mediaType, ID: itemID})
	removed := false
	for _, key := range identity.CandidateKeys {
		if entry, exists := perUser[key]; exists {
			s.removePlaybackProgressEntryLocked(userID, perUser, key, entry)
			removed = true
		}
	}

	for existingKey, entry := range perUser {
		if !playbackProgressMatchesIdentity(entry, identity) {
			continue
		}
		s.removePlaybackProgressEntryLocked(userID, perUser, existingKey, entry)
		removed = true
	}

	return removed
}

func (s *Service) removePlaybackProgressEntryLocked(userID string, perUser map[string]models.PlaybackProgress, key string, entry models.PlaybackProgress) {
	delete(perUser, key)
	if entry.HiddenFromContinueWatching {
		s.preserveSeriesHiddenMarkerLocked(perUser, entry)
	}
}

func (s *Service) clearActivePlaybackProgressKeyLocked(userID, key string) {
	if perUser, ok := s.activePlaybackProgress[userID]; ok {
		delete(perUser, key)
		if len(perUser) == 0 {
			delete(s.activePlaybackProgress, userID)
		}
	}
}

func (s *Service) clearActivePlaybackProgressIdentityLocked(userID string, identity mediaidentity.Identity) {
	perUser, ok := s.activePlaybackProgress[userID]
	if !ok {
		return
	}
	for _, key := range identity.CandidateKeys {
		delete(perUser, key)
	}
	for key, progress := range perUser {
		if playbackProgressMatchesIdentity(progress, identity) {
			delete(perUser, key)
		}
	}
	if len(perUser) == 0 {
		delete(s.activePlaybackProgress, userID)
	}
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
	s.bumpWatchStateRevisionLocked(userID)

	perUser := s.ensurePlaybackProgressUserLocked(userID)

	// Collect every external ID we already know for this series/movie so the
	// incoming ID can be resolved to its canonical form and matched against
	// rows stored under any alias (tmdb:tv:X vs tvdb:series:Y vs imdb).
	hiddenAt := time.Now().UTC()
	markerExternalIDs := s.collectExternalIDsForSeriesLocked(userID, seriesID)
	movieTarget := mediaidentity.Resolve(mediaidentity.Input{MediaType: "movie", ID: seriesID, ExternalIDs: markerExternalIDs})
	seriesTarget := mediaidentity.Resolve(mediaidentity.Input{MediaType: "series", ID: seriesID, ExternalIDs: markerExternalIDs})

	found := false
	movieMatched := false
	markedCount := 0
	for key, progress := range perUser {
		match := false
		if progress.MediaType != "episode" && playbackProgressMatchesIdentity(progress, movieTarget) {
			match = true
			movieMatched = true
		}
		if !match && progressSeriesMatchesIdentity(progress, seriesTarget) {
			match = true
		}
		if match {
			if markerExternalIDs == nil {
				markerExternalIDs = make(map[string]string)
			}
			for _, k := range []string{"imdb", "tvdb", "tmdb"} {
				if v, ok := progress.ExternalIDs[k]; ok && v != "" {
					if _, exists := markerExternalIDs[k]; !exists {
						markerExternalIDs[k] = v
					}
				}
			}
			progress.HiddenFromContinueWatching = true
			progress.UpdatedAt = hiddenAt
			perUser[key] = progress
			found = true
			markedCount++
			log.Printf("[history] HideFromContinueWatching: marked entry hidden key=%q itemID=%q seriesID=%q", key, progress.ItemID, progress.SeriesID)
		}
	}

	// Movie hides are tracked by the hidden flag on the matched rows themselves;
	// series hides additionally keep a series-level marker so the hidden state
	// survives episode-row churn (e.g. Trakt sync clearing watched progress).
	isMovieHide := movieMatched || strings.Contains(strings.ToLower(seriesID), ":movie:")
	markerSeriesID := seriesTarget.ID
	markerMediaType := "episode"
	if isMovieHide {
		markerSeriesID = movieTarget.ID
		markerMediaType = "movie"
	}

	if !found {
		// No progress entry exists — create a minimal marker just to track the
		// hidden state, keyed under the canonical ID form.
		key := makeWatchKey(markerMediaType, markerSeriesID)
		perUser[key] = models.PlaybackProgress{
			ID:                         key,
			MediaType:                  markerMediaType,
			ItemID:                     markerSeriesID,
			SeriesID:                   markerSeriesID,
			ExternalIDs:                markerExternalIDs,
			UpdatedAt:                  hiddenAt,
			HiddenFromContinueWatching: true,
		}
		log.Printf("[history] HideFromContinueWatching: no existing entry found, created marker key=%q for seriesID=%q externalIDs=%v", key, markerSeriesID, markerExternalIDs)
	} else {
		if !isMovieHide {
			key := makeWatchKey("episode", markerSeriesID)
			perUser[key] = models.PlaybackProgress{
				ID:                         key,
				MediaType:                  "episode",
				ItemID:                     markerSeriesID,
				SeriesID:                   markerSeriesID,
				ExternalIDs:                markerExternalIDs,
				UpdatedAt:                  hiddenAt,
				HiddenFromContinueWatching: true,
			}
			log.Printf("[history] HideFromContinueWatching: upserted series hidden marker key=%q for seriesID=%q externalIDs=%v", key, markerSeriesID, markerExternalIDs)
		}
		log.Printf("[history] HideFromContinueWatching: marked %d existing entries hidden for seriesID=%q", markedCount, seriesID)
	}

	// Invalidate continue watching cache
	s.invalidateContinueWatchingLocked(userID)

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
			s.removePlaybackProgressEntryLocked(userID, perUser, key, progress)
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
	target := mediaidentity.Resolve(mediaidentity.Input{MediaType: "series", ID: seriesID})
	collect := func(ids map[string]string) {
		for _, k := range []string{"imdb", "tvdb", "tmdb"} {
			if v, ok := ids[k]; ok && v != "" {
				if _, exists := collected[k]; !exists {
					collected[k] = v
				}
			}
		}
	}

	// Check watch history
	if perUser, ok := s.watchHistory[userID]; ok {
		for _, item := range perUser {
			itemIdentity := mediaidentity.Resolve(mediaidentity.Input{
				MediaType:   "series",
				ID:          item.SeriesID,
				ExternalIDs: item.ExternalIDs,
			})
			if identitiesReferToSameTitle(itemIdentity, target) {
				collect(item.ExternalIDs)
			}
		}
	}

	// Also check playback progress
	if perUser, ok := s.playbackProgress[userID]; ok {
		for _, prog := range perUser {
			if progressSeriesMatchesIdentity(prog, target) {
				collect(prog.ExternalIDs)
			}
		}
	}

	if len(collected) == 0 {
		return nil
	}
	return collected
}

// clearProgressByExternalIDMatchLocked removes playback progress entries for a
// specific episode that match by season+episode number and refer to the same
// series. It accepts either shared external IDs or compatible series IDs so
// sparse local progress rows are cleared by bulk manual unwatch actions too.
// Callers must hold s.mu before invoking this helper.
func (s *Service) clearProgressByExternalIDMatchLocked(userID, seriesID string, seasonNumber, episodeNumber int, externalIDs map[string]string) bool {
	if seasonNumber <= 0 || episodeNumber <= 0 || (strings.TrimSpace(seriesID) == "" && len(externalIDs) == 0) {
		return false
	}

	perUser, ok := s.playbackProgress[userID]
	if !ok {
		return false
	}

	anyCleared := false
	for key, progress := range perUser {
		if progress.MediaType != "episode" || progress.SeasonNumber != seasonNumber {
			continue
		}
		if !episodeNumbersMatchForProgressClear(progress.EpisodeNumber, episodeNumber, progress.ExternalIDs, externalIDs) {
			continue
		}

		if episodeProgressMatchesSeries(progress, seriesID, externalIDs) {
			s.removePlaybackProgressEntryLocked(userID, perUser, key, progress)
			anyCleared = true
		}
	}

	return anyCleared
}

func episodeProgressMatchesSeries(progress models.PlaybackProgress, seriesID string, externalIDs map[string]string) bool {
	seriesID = strings.TrimSpace(seriesID)
	if seriesID != "" {
		if progress.SeriesID == seriesID {
			return true
		}
		provider, value := providerAndValue(seriesID)
		if provider != "" && value != "" {
			if v, ok := progress.ExternalIDs[provider]; ok && v == value {
				return true
			}
		}
		progressProvider, progressValue := providerAndValue(progress.SeriesID)
		if progressProvider != "" && progressValue != "" {
			if v, ok := externalIDs[progressProvider]; ok && v == progressValue {
				return true
			}
		}
	}
	return hasMatchingExternalID(progress.ExternalIDs, externalIDs)
}

func providerAndValue(id string) (string, string) {
	parts := strings.Split(strings.TrimSpace(id), ":")
	if len(parts) < 2 {
		return "", ""
	}
	return strings.ToLower(parts[0]), parts[len(parts)-1]
}

func episodeNumbersMatchForProgressClear(progressEpisode, updateEpisode int, progressExternalIDs, updateExternalIDs map[string]string) bool {
	if progressEpisode == updateEpisode {
		return true
	}
	progressAbsolute, progressHasAbsolute := positiveExternalIDInt(progressExternalIDs, "absoluteEpisode")
	updateAbsolute, updateHasAbsolute := positiveExternalIDInt(updateExternalIDs, "absoluteEpisode")
	if progressHasAbsolute && progressEpisode == progressAbsolute && updateAbsolute == progressAbsolute {
		return true
	}
	if updateHasAbsolute && updateEpisode == updateAbsolute && progressAbsolute == updateAbsolute {
		return true
	}
	return false
}

func positiveExternalIDInt(externalIDs map[string]string, key string) (int, bool) {
	if len(externalIDs) == 0 {
		return 0, false
	}
	value, ok := externalIDs[key]
	if !ok {
		return 0, false
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, false
	}
	return parsed, true
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
		s.removePlaybackProgressEntryLocked(userID, perUser, key, progress)
		anyCleared = true
	}

	return anyCleared
}

// seriesIDProviderRank ranks an item/series ID string by provider so duplicate
// progress rows stored under different schemes collapse onto a single
// deterministic key. TMDB is preferred because the native details page and the
// continue-watching metadata path both resolve episodes via TMDB; ranking it
// highest keeps stored progress aligned with how it is displayed. This affects
// only which storage key survives — episode numbering and metadata are resolved
// at read time from external IDs (buildCanonicalSeriesIDMap +
// getSeriesMetadataWithCache) and are unaffected by the choice here.
func seriesIDProviderRank(id string) int {
	lower := strings.ToLower(strings.TrimSpace(id))
	switch {
	case strings.HasPrefix(lower, "tmdb:"):
		return 3
	case strings.HasPrefix(lower, "tvdb:"):
		return 2
	case strings.HasPrefix(lower, "imdb:"), strings.HasPrefix(lower, "tt"):
		return 1
	default:
		return 0
	}
}

// preferProgressID reports whether candidate should win over current as the
// canonical key: higher provider rank first, then a stable lexical order so the
// outcome is independent of map iteration order.
func preferProgressID(candidate, current string) bool {
	rc, rcur := seriesIDProviderRank(candidate), seriesIDProviderRank(current)
	if rc != rcur {
		return rc > rcur
	}
	return candidate < current
}

// unionExternalIDs copies non-empty external IDs from src into dst without
// overwriting existing values, preserving the richest set so the read-side
// canonical resolver keeps its choice stable when rows are consolidated.
func unionExternalIDs(dst, src map[string]string) map[string]string {
	if dst == nil {
		dst = make(map[string]string, len(src))
	}
	for k, v := range src {
		if v == "" {
			continue
		}
		if _, ok := dst[k]; !ok {
			dst[k] = v
		}
	}
	return dst
}

// consolidateMovieProgressLocked collapses duplicate movie progress rows stored
// under alternate ID schemes onto a single deterministic key. It returns the
// canonical key/itemID the incoming progress should be written under and the
// union of external IDs across every merged row. Non-survivor rows are deleted
// (hidden-from-continue-watching state preserved). Callers must hold s.mu.
func (s *Service) consolidateMovieProgressLocked(userID string, perUser map[string]models.PlaybackProgress, incomingKey, incomingItemID string, incomingExternalIDs map[string]string) (canonicalKey, canonicalItemID string, merged map[string]string) {
	canonicalKey, canonicalItemID = incomingKey, incomingItemID
	merged = unionExternalIDs(nil, incomingExternalIDs)
	if len(incomingExternalIDs) == 0 {
		return canonicalKey, canonicalItemID, merged
	}

	var matchedKeys []string
	for key, progress := range perUser {
		if key == incomingKey || progress.MediaType != "movie" {
			continue
		}
		if !hasMatchingExternalID(progress.ExternalIDs, incomingExternalIDs) {
			continue
		}
		matchedKeys = append(matchedKeys, key)
		merged = unionExternalIDs(merged, progress.ExternalIDs)
		if preferProgressID(progress.ItemID, canonicalItemID) {
			canonicalKey, canonicalItemID = key, progress.ItemID
		}
	}

	for _, key := range matchedKeys {
		if key == canonicalKey {
			continue
		}
		progress := perUser[key]
		s.removePlaybackProgressEntryLocked(userID, perUser, key, progress)
		s.clearActivePlaybackProgressKeyLocked(userID, key)
	}
	return canonicalKey, canonicalItemID, merged
}

// consolidateEpisodeProgressLocked collapses duplicate same-episode progress
// rows stored under alternate series-ID schemes onto a single deterministic key.
// It returns the canonical key/itemID/seriesID the incoming progress should be
// written under and the union of external IDs across every merged row.
// Non-survivor rows are deleted (hidden state preserved). Callers must hold s.mu.
func (s *Service) consolidateEpisodeProgressLocked(userID string, perUser map[string]models.PlaybackProgress, incomingKey, incomingItemID, incomingSeriesID string, seasonNumber, episodeNumber int, incomingExternalIDs map[string]string) (canonicalKey, canonicalItemID, canonicalSeriesID string, merged map[string]string) {
	canonicalKey, canonicalItemID, canonicalSeriesID = incomingKey, incomingItemID, incomingSeriesID
	merged = unionExternalIDs(nil, incomingExternalIDs)
	if seasonNumber <= 0 || episodeNumber <= 0 || len(incomingExternalIDs) == 0 {
		return canonicalKey, canonicalItemID, canonicalSeriesID, merged
	}

	var matchedKeys []string
	for key, progress := range perUser {
		if key == incomingKey {
			continue
		}
		if progress.MediaType != "episode" ||
			progress.SeasonNumber != seasonNumber ||
			progress.EpisodeNumber != episodeNumber {
			continue
		}
		if !hasMatchingEpisodeIdentityForProgressDedupe(progress, incomingSeriesID, incomingExternalIDs) {
			continue
		}
		matchedKeys = append(matchedKeys, key)
		merged = unionExternalIDs(merged, progress.ExternalIDs)
		if preferProgressID(progress.ItemID, canonicalItemID) {
			canonicalKey, canonicalItemID, canonicalSeriesID = key, progress.ItemID, progress.SeriesID
		}
	}

	for _, key := range matchedKeys {
		if key == canonicalKey {
			continue
		}
		progress := perUser[key]
		s.removePlaybackProgressEntryLocked(userID, perUser, key, progress)
		s.clearActivePlaybackProgressKeyLocked(userID, key)
	}
	return canonicalKey, canonicalItemID, canonicalSeriesID, merged
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

	target := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   "series",
		ID:          seriesID,
		ExternalIDs: canonicalSeriesExternalIDs(seriesID, deleted.ItemID, deleted.ExternalIDs),
	})

	// Check if another hidden entry already covers this series
	for _, p := range perUser {
		if p.HiddenFromContinueWatching && progressSeriesMatchesIdentity(p, target) {
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
		ExternalIDs:                canonicalSeriesExternalIDs(seriesID, deleted.ItemID, deleted.ExternalIDs),
		UpdatedAt:                  deleted.UpdatedAt,
		HiddenFromContinueWatching: true,
	}
}

// hasMatchingExternalID returns true if two external ID maps share at least one
// common provider key+value pair (excluding titleId and episode numbering).
func hasMatchingExternalID(a, b map[string]string) bool {
	return mediaidentity.HasMatchingExternalID(a, b)
}

func hasMatchingEpisodeIdentityForProgressDedupe(existing models.PlaybackProgress, incomingSeriesID string, incomingExternalIDs map[string]string) bool {
	if progressSeriesMatchesIdentity(existing, mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   "series",
		ID:          incomingSeriesID,
		ExternalIDs: canonicalSeriesExternalIDs(incomingSeriesID, "", incomingExternalIDs),
	})) {
		return true
	}
	return hasMatchingConcreteEpisodeExternalID(existing.ExternalIDs, incomingExternalIDs)
}

func hasMatchingConcreteEpisodeExternalID(a, b map[string]string) bool {
	a = mediaidentity.NormalizeExternalIDs(a)
	b = mediaidentity.NormalizeExternalIDs(b)
	for _, key := range []string{"episodeTvdb", "episodeTmdb", "episodeImdb", "episodeTrakt"} {
		if value := strings.TrimSpace(a[key]); value != "" && value == strings.TrimSpace(b[key]) {
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

// clearSupersededSeriesHiddenMarkers removes series-level hide markers older
// than a concrete, visible episode progress row for the same series. New
// playback is the documented unhide signal; comparing timestamps also repairs
// legacy rows where the write-side marker deletion was missed.
func clearSupersededSeriesHiddenMarkers(perUser map[string]models.PlaybackProgress, candidate models.PlaybackProgress) int {
	if candidate.HiddenFromContinueWatching || isSeriesLevelPlaybackMarker(candidate) ||
		!strings.EqualFold(strings.TrimSpace(candidate.MediaType), "episode") ||
		candidate.SeasonNumber <= 0 || candidate.EpisodeNumber <= 0 || candidate.UpdatedAt.IsZero() {
		return 0
	}

	target := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   "series",
		ID:          candidate.SeriesID,
		ExternalIDs: canonicalSeriesExternalIDs(candidate.SeriesID, candidate.ItemID, candidate.ExternalIDs),
	})
	removed := 0
	for key, marker := range perUser {
		if !marker.HiddenFromContinueWatching || !isSeriesLevelPlaybackMarker(marker) ||
			!candidate.UpdatedAt.After(marker.UpdatedAt) || !progressSeriesMatchesIdentity(marker, target) {
			continue
		}
		delete(perUser, key)
		removed++
	}
	return removed
}

func clearSupersededSeriesHiddenMarkersFromStoredProgress(perUser map[string]models.PlaybackProgress) int {
	removed := 0
	for _, progress := range perUser {
		removed += clearSupersededSeriesHiddenMarkers(perUser, progress)
	}
	return removed
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

func (s *Service) hasConflictingInProgressPlaybackLocked(userID string, update models.WatchHistoryUpdate) bool {
	perUser, ok := s.playbackProgress[userID]
	if !ok {
		return false
	}

	for _, progress := range perUser {
		if !isMatchingPlaybackForWatchUpdate(progress, update) {
			continue
		}
		if progress.PercentWatched >= continueWatchingCompletionThreshold {
			continue
		}

		// If local playback continued or was paused after an imported completion
		// timestamp, the newer incomplete heartbeat is stronger evidence about
		// this device's current state. Preserve it instead of clearing progress.
		// The provider-side writer may be another client and cannot be inferred
		// from the imported history record alone.
		if strings.EqualFold(update.MediaType, "episode") &&
			!update.WatchedAt.IsZero() && progress.UpdatedAt.After(update.WatchedAt) {
			return true
		}

		// Preserve the existing threshold-conflict behavior for providers that
		// consider an 80%-complete stop watched while local history requires 90%.
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

// AggregatePopularTitles returns titles ranked by completed media-item views
// across all eligible profiles within the given lookback window. Episodes are
// rolled up to their parent series, but each completed episode contributes one
// view to the series total.
func (s *Service) AggregatePopularTitles(
	eligibleUsers map[string]bool,
	windowDays int,
	minViews int,
) []models.PopularTitle {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cutoff := time.Now().UTC().AddDate(0, 0, -windowDays)
	if minViews < 1 {
		minViews = 1
	}

	// Aggregate each completed movie or episode as one view of its canonical
	// movie or parent-series title.
	type aggEntry struct {
		title models.PopularTitle
	}
	aggregated := make(map[string]*aggEntry)

	for userID, perUser := range s.watchHistory {
		if !eligibleUsers[userID] {
			continue
		}
		for _, item := range perUser {
			if !item.Watched || item.WatchedSeconds <= 0 || item.WatchedAt.Before(cutoff) {
				continue
			}
			key := popularTitleKey(item)
			if key == "" {
				continue
			}
			entry := aggregated[key]
			if entry == nil {
				entry = &aggEntry{}
				aggregated[key] = entry
			}
			entry.title.WatchCount++
			if entry.title.Name == "" || item.WatchedAt.After(entry.title.LastWatched) {
				mediaType, name, year, extIDs := popularTitleMetadata(item)
				watchCount := entry.title.WatchCount
				entry.title = models.PopularTitle{
					MediaType:   mediaType,
					ItemID:      popularTitleItemID(item),
					Name:        name,
					Year:        year,
					WatchCount:  watchCount,
					ExternalIDs: cloneStringMap(extIDs),
					LastWatched: item.WatchedAt,
				}
			} else {
				mergeMissingExternalIDs(entry.title.ExternalIDs, item.ExternalIDs)
			}
		}
	}

	result := make([]models.PopularTitle, 0, len(aggregated))
	for _, entry := range aggregated {
		if entry.title.Name == "" || entry.title.WatchCount < minViews {
			continue
		}
		result = append(result, entry.title)
	}

	// Rank by completed media-item views, then by the most recent qualifying
	// view so ties do not produce an arbitrary alphabetical archive.
	sort.Slice(result, func(i, j int) bool {
		if result[i].WatchCount == result[j].WatchCount {
			if !result[i].LastWatched.Equal(result[j].LastWatched) {
				return result[i].LastWatched.After(result[j].LastWatched)
			}
			return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
		}
		return result[i].WatchCount > result[j].WatchCount
	})

	return result
}

// ListRecentWatches returns the most recent watch events across eligible profiles,
// capped at maxPerProfile entries per user. Anonymous profiles (shared_anonymous)
// have their UserID cleared and IsAnonymous set to true.
func (s *Service) ListRecentWatches(eligibleUsers map[string]models.User, windowDays int, maxPerProfile int) []models.RecentWatch {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cutoff := time.Now().UTC().AddDate(0, 0, -windowDays)

	type userWatch struct {
		item models.WatchHistoryItem
		user models.User
	}

	// Collect all eligible watches
	var allWatches []userWatch
	for userID, perUser := range s.watchHistory {
		user, ok := eligibleUsers[userID]
		if !ok {
			continue
		}
		for _, item := range perUser {
			if !item.Watched || item.WatchedSeconds <= 0 || item.WatchedAt.Before(cutoff) {
				continue
			}
			allWatches = append(allWatches, userWatch{item: item, user: user})
		}
	}

	// Sort by watched date descending
	sort.Slice(allWatches, func(i, j int) bool {
		return allWatches[i].item.WatchedAt.After(allWatches[j].item.WatchedAt)
	})

	// Group by user and apply per-profile cap
	type userGroup struct {
		items []models.WatchHistoryItem
		user  models.User
	}
	userGroups := make(map[string]*userGroup)
	var userOrder []string // preserve chronological order of first appearance

	for _, uw := range allWatches {
		uid := uw.user.ID
		grp, ok := userGroups[uid]
		if !ok {
			grp = &userGroup{user: uw.user}
			userGroups[uid] = grp
			userOrder = append(userOrder, uid)
		}
		if len(grp.items) < maxPerProfile {
			grp.items = append(grp.items, uw.item)
		}
	}

	// Build result preserving global chronological order
	// (re-sort cap-applied items by time)
	var result []models.RecentWatch
	for _, uid := range userOrder {
		grp := userGroups[uid]
		for _, item := range grp.items {
			isAnonymous := strings.EqualFold(strings.TrimSpace(grp.user.ActivityPrivacy), "shared_anonymous")
			userName := grp.user.Name
			if isAnonymous {
				userName = "Fellow user"
			}
			rw := models.RecentWatch{
				UserID:        grp.user.ID,
				UserName:      userName,
				IsAnonymous:   isAnonymous,
				MediaType:     item.MediaType,
				ItemID:        item.ItemID,
				Name:          item.Name,
				SeriesID:      item.SeriesID,
				SeriesName:    item.SeriesName,
				SeasonNumber:  item.SeasonNumber,
				EpisodeNumber: item.EpisodeNumber,
				WatchedAt:     item.WatchedAt,
				ExternalIDs:   item.ExternalIDs,
			}
			if isAnonymous {
				rw.UserID = "" // clear user ID for anonymous entries
			}
			result = append(result, rw)
		}
	}

	// Sort by WatchedAt descending
	sort.Slice(result, func(i, j int) bool {
		return result[i].WatchedAt.After(result[j].WatchedAt)
	})

	return result
}

// popularTitleKey returns a canonical key for a watch history item:
// "movie:<itemID>" for movies, "series:<seriesID>" for episodes.
// Returns "" if the item cannot be resolved.
func popularTitleKey(item models.WatchHistoryItem) string {
	mediaType := strings.ToLower(strings.TrimSpace(item.MediaType))
	if mediaType == "episode" || mediaType == "series" {
		mediaType = "series"
	} else if mediaType == "movie" {
		mediaType = "movie"
	} else {
		return ""
	}
	for _, provider := range []string{"imdb", "tmdb", "tvdb"} {
		if value := strings.ToLower(strings.TrimSpace(item.ExternalIDs[provider])); value != "" {
			return mediaType + ":" + provider + ":" + value
		}
	}

	switch item.MediaType {
	case "movie":
		id := strings.TrimSpace(item.ItemID)
		if id == "" {
			return ""
		}
		return "movie:" + strings.ToLower(id)
	case "episode", "series":
		sid := strings.TrimSpace(item.SeriesID)
		if item.MediaType == "series" && sid == "" {
			sid = strings.TrimSpace(item.ItemID)
		}
		if sid == "" {
			// Try to infer from ItemID (strip :S##E## suffix)
			sid = inferSeriesID(item.ItemID)
		}
		if sid == "" {
			return ""
		}
		return "series:" + strings.ToLower(sid)
	default:
		return ""
	}
}

// popularTitleItemID returns the item-level ID for a watch history item
// (series ID for episodes, item ID for movies).
func popularTitleItemID(item models.WatchHistoryItem) string {
	if item.MediaType == "episode" || item.MediaType == "series" {
		sid := strings.TrimSpace(item.SeriesID)
		if item.MediaType == "series" && sid == "" {
			sid = strings.TrimSpace(item.ItemID)
		}
		if sid != "" {
			return sid
		}
		return inferSeriesID(item.ItemID)
	}
	return item.ItemID
}

// popularTitleMetadata extracts display metadata from a watch history item.
func popularTitleMetadata(item models.WatchHistoryItem) (mediaType, name string, year int, extIDs map[string]string) {
	if item.MediaType == "episode" || item.MediaType == "series" {
		name = strings.TrimSpace(item.SeriesName)
		if name == "" {
			name = strings.TrimSpace(item.Name)
		}
		return "series", name, item.Year, item.ExternalIDs
	}
	return "movie", item.Name, item.Year, item.ExternalIDs
}

func cloneStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func mergeMissingExternalIDs(target map[string]string, source map[string]string) {
	if target == nil {
		return
	}
	for key, value := range source {
		if strings.TrimSpace(target[key]) == "" && strings.TrimSpace(value) != "" {
			target[key] = value
		}
	}
}

// inferSeriesID strips the :S##E## suffix from an episode ItemID to yield
// the parent series ID. Returns "" if the pattern is not recognised.
func inferSeriesID(itemID string) string {
	itemID = strings.TrimSpace(itemID)
	// Look for :S##E## at the end, case-insensitive
	upper := strings.ToUpper(itemID)
	idx := strings.LastIndex(upper, ":S")
	if idx < 0 {
		idx = strings.LastIndex(upper, "/S")
	}
	if idx < 0 {
		return ""
	}
	// After :S or /S should be digits, then E, then digits
	rest := upper[idx+2:]
	eIdx := strings.Index(rest, "E")
	if eIdx < 0 {
		return ""
	}
	return itemID[:idx]
}
