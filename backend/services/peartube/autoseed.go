package peartube

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

type PlaybackState string

const (
	PlaybackUnqualified PlaybackState = "unqualified"
	PlaybackQualified   PlaybackState = "qualified"
	PlaybackCancelled   PlaybackState = "cancelled"

	DefaultMeaningfulWatchDuration = 30 * time.Second
	DefaultMeaningfulWatchFraction = 0.05
	DefaultPlaybackObservationGap  = 20 * time.Second
	DefaultPlaybackObservationTTL  = 6 * time.Hour
	DefaultPlaybackObservationCap  = 256
	playbackMinimumEvidence        = 10 * time.Second
	playbackSeekTolerance          = 2 * time.Second
	playbackMaximumRate            = 2
)

type PlaybackObserverConfig struct {
	MeaningfulWatchDuration time.Duration
	MeaningfulWatchFraction float64
	MaxObservationGap       time.Duration
	EntryTTL                time.Duration
	Capacity                int
}

type PlaybackEvent struct {
	PlaybackID       string
	SourceID         string
	Position         time.Duration
	Duration         time.Duration
	ObservedAt       time.Time
	Paused           bool
	Buffering        bool
	Abandoned        bool
	RestartCancelled bool
	// QualifiesImmediately makes this observation enough on its own, which is
	// what an operator asks for by archiving whole titles on playback start.
	// It never overrides abandonment, and it is deduplicated by the same
	// per-source ledger continuous progress writes, so a title still qualifies
	// once per TTL however it got there.
	QualifiesImmediately bool
}

type PlaybackObservation struct {
	State          PlaybackState
	Accumulated    time.Duration
	FirstQualified bool
	FirstCancelled bool
}

type playbackObservationEntry struct {
	sourceID       string
	state          PlaybackState
	accumulated    time.Duration
	lastPosition   time.Duration
	lastObservedAt time.Time
	updatedAt      time.Time
}

type PlaybackObserver struct {
	mu               sync.Mutex
	config           PlaybackObserverConfig
	playbacks        map[string]*playbackObservationEntry
	qualifiedSources map[string]time.Time
}

func NewPlaybackObserver(config PlaybackObserverConfig) *PlaybackObserver {
	if config.MeaningfulWatchDuration <= 0 {
		config.MeaningfulWatchDuration = DefaultMeaningfulWatchDuration
	}
	if config.MeaningfulWatchFraction <= 0 || config.MeaningfulWatchFraction > 1 {
		config.MeaningfulWatchFraction = DefaultMeaningfulWatchFraction
	}
	if config.MaxObservationGap <= 0 {
		config.MaxObservationGap = DefaultPlaybackObservationGap
	}
	if config.EntryTTL <= 0 {
		config.EntryTTL = DefaultPlaybackObservationTTL
	}
	if config.Capacity <= 0 {
		config.Capacity = DefaultPlaybackObservationCap
	}
	return &PlaybackObserver{
		config:           config,
		playbacks:        make(map[string]*playbackObservationEntry),
		qualifiedSources: make(map[string]time.Time),
	}
}

func (t *PlaybackObserver) Observe(event PlaybackEvent) PlaybackObservation {
	if t == nil {
		return PlaybackObservation{State: PlaybackUnqualified}
	}
	playbackID := strings.TrimSpace(event.PlaybackID)
	sourceID := strings.TrimSpace(event.SourceID)
	if playbackID == "" || sourceID == "" || len(playbackID) > 256 || len(sourceID) > 128 {
		return PlaybackObservation{State: PlaybackUnqualified}
	}
	now := event.ObservedAt
	if now.IsZero() {
		now = time.Now()
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked(now)

	entry := t.playbacks[playbackID]
	if entry != nil && entry.sourceID != sourceID {
		delete(t.playbacks, playbackID)
		entry = nil
	}
	if entry != nil && entry.state == PlaybackCancelled && event.RestartCancelled && !event.Abandoned {
		delete(t.playbacks, playbackID)
		entry = nil
	}
	if entry == nil {
		t.makeRoomLocked()
		state := PlaybackUnqualified
		if until := t.qualifiedSources[sourceID]; until.After(now) {
			state = PlaybackQualified
		}
		entry = &playbackObservationEntry{
			sourceID:       sourceID,
			state:          state,
			lastPosition:   maxDuration(event.Position, 0),
			lastObservedAt: now,
			updatedAt:      now,
		}
		t.playbacks[playbackID] = entry
		if event.Abandoned {
			entry.state = PlaybackCancelled
			return PlaybackObservation{State: entry.state, FirstCancelled: true}
		}
		if event.QualifiesImmediately && entry.state == PlaybackUnqualified {
			return t.qualifyLocked(entry, sourceID, now)
		}
		return PlaybackObservation{State: entry.state}
	}

	entry.updatedAt = now
	if event.Abandoned {
		first := entry.state != PlaybackCancelled
		entry.state = PlaybackCancelled
		return PlaybackObservation{State: entry.state, Accumulated: entry.accumulated, FirstCancelled: first}
	}
	if entry.state != PlaybackUnqualified {
		return PlaybackObservation{State: entry.state, Accumulated: entry.accumulated}
	}
	if event.QualifiesImmediately {
		return t.qualifyLocked(entry, sourceID, now)
	}

	elapsed := now.Sub(entry.lastObservedAt)
	position := maxDuration(event.Position, 0)
	progress := position - entry.lastPosition
	continuous := !event.Paused &&
		!event.Buffering &&
		elapsed > 0 &&
		elapsed <= t.config.MaxObservationGap &&
		progress > 0 &&
		progress <= elapsed*playbackMaximumRate+playbackSeekTolerance
	entry.lastObservedAt = now
	if position >= entry.lastPosition {
		entry.lastPosition = position
	}
	if !continuous {
		return PlaybackObservation{State: entry.state, Accumulated: entry.accumulated}
	}
	if progress < elapsed {
		elapsed = progress
	}
	entry.accumulated += elapsed
	if entry.accumulated < t.meaningfulThreshold(event.Duration) {
		return PlaybackObservation{State: entry.state, Accumulated: entry.accumulated}
	}
	return t.qualifyLocked(entry, sourceID, now)
}

// qualifyLocked promotes a playback to qualified and reports whether this is the
// first time its source has qualified inside the TTL. That per-source ledger is
// the only thing standing between one title and one submission per heartbeat, so
// every route to qualification has to go through it.
func (t *PlaybackObserver) qualifyLocked(
	entry *playbackObservationEntry,
	sourceID string,
	now time.Time,
) PlaybackObservation {
	entry.state = PlaybackQualified
	if until := t.qualifiedSources[sourceID]; until.After(now) {
		return PlaybackObservation{State: entry.state, Accumulated: entry.accumulated}
	}
	t.qualifiedSources[sourceID] = now.Add(t.config.EntryTTL)
	return PlaybackObservation{State: entry.state, Accumulated: entry.accumulated, FirstQualified: true}
}

// ForgetQualifiedSource drops a source's qualification so the next heartbeat can
// qualify it again.
//
// The per-source ledger and the swarm-key claim are two independent guards, and
// releasing only the claim leaves the stricter one in force. Observed live: Thor
// Ragnarok qualified on its very first heartbeat, a moment before the debrid link
// it needed had been unrestricted, so the attempt failed for a reason that was
// gone seconds later. The claim was shortened to two minutes, correctly — and
// nothing retried, because qualification is once per source per six hours and no
// later heartbeat could reach the attempt at all.
//
// So a transient failure has to release both. The claim window is what stops
// this becoming a retry every heartbeat.
func (t *PlaybackObserver) ForgetQualifiedSource(sourceID string) {
	if t == nil || sourceID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.qualifiedSources, sourceID)
}

func (t *PlaybackObserver) meaningfulThreshold(duration time.Duration) time.Duration {
	threshold := t.config.MeaningfulWatchDuration
	if duration > 0 {
		fraction := time.Duration(float64(duration) * t.config.MeaningfulWatchFraction)
		if fraction < playbackMinimumEvidence {
			fraction = playbackMinimumEvidence
		}
		if fraction < threshold {
			threshold = fraction
		}
	}
	return threshold
}

func (t *PlaybackObserver) pruneLocked(now time.Time) {
	for key, entry := range t.playbacks {
		if now.Sub(entry.updatedAt) >= t.config.EntryTTL {
			delete(t.playbacks, key)
		}
	}
	for sourceID, until := range t.qualifiedSources {
		if !until.After(now) {
			delete(t.qualifiedSources, sourceID)
		}
	}
}

func (t *PlaybackObserver) makeRoomLocked() {
	if len(t.playbacks) < t.config.Capacity {
		return
	}
	var oldestKey string
	var oldestAt time.Time
	for key, entry := range t.playbacks {
		if oldestKey == "" || entry.updatedAt.Before(oldestAt) ||
			(entry.updatedAt.Equal(oldestAt) && key < oldestKey) {
			oldestKey, oldestAt = key, entry.updatedAt
		}
	}
	delete(t.playbacks, oldestKey)
}

func maxDuration(value, floor time.Duration) time.Duration {
	if value < floor {
		return floor
	}
	return value
}

// EntityKey is the swarm's identity for a title, derived from the coordinates a
// seed would publish it under. One key serves two purposes that must agree:
// asking whether the relay already carries a title, and claiming it so it is
// only seeded once.
//
// The form mirrors the coordinates a relay entity id encodes, which is what
// makes the two comparable: `movie:603`, `show:1399:s1:e1`. Coordinates that
// cannot be published — no TMDB id, an episode without season and episode
// numbers — have no key.
func EntityKey(coords ArchiveCoordinates) string {
	tmdbID := strings.TrimSpace(coords.TMDBID)
	if tmdbID == "" {
		return ""
	}
	switch coords.ContentKind {
	case "movie":
		return "movie:" + tmdbID
	case "episode":
		if coords.TMDBSeason < 1 || coords.TMDBEpisode < 1 {
			return ""
		}
		return fmt.Sprintf("show:%s:s%d:e%d", tmdbID, coords.TMDBSeason, coords.TMDBEpisode)
	default:
		return ""
	}
}

// catalogEntityKey recovers the same key from a relay catalog entity id, so a
// published entity and a pending seed are compared on identical terms.
func catalogEntityKey(entityID string) string {
	coords, ok := parseEntityCoordinates(entityID)
	if !ok {
		return ""
	}
	return EntityKey(ArchiveCoordinates{
		ContentKind: coords.Kind,
		TMDBID:      coords.TMDBID,
		TMDBSeason:  coords.Season,
		TMDBEpisode: coords.Episode,
	})
}

func catalogSourceKey(entity CatalogEntity, source CatalogSource) string {
	if coords, ok := coordinatesForSource(entity, source); ok {
		return EntityKey(ArchiveCoordinates{
			ContentKind: coords.Kind,
			TMDBID:      coords.TMDBID,
			TMDBSeason:  coords.Season,
			TMDBEpisode: coords.Episode,
		})
	}
	return ""
}

// CatalogHasEntity reports whether the swarm can already serve these
// coordinates. It reads the same briefly-cached catalog a search reads, so a
// watch that follows a search costs no round trip.
//
// An entity with no addressable source does not count as published: the stream
// endpoint could not serve it, which is the situation seeding exists to fix.
//
// A relay that is slow, unreachable, or refusing to enumerate returns the error
// rather than false. "I could not find out" is not "it is missing", and the
// caller must not turn a catalog timeout into a duplicate fetch of a whole file.
func (c *Client) CatalogHasEntity(ctx context.Context, coords ArchiveCoordinates) (bool, error) {
	if c == nil {
		return false, nil
	}
	key := EntityKey(coords)
	if key == "" {
		return false, nil
	}
	searchReq := SearchRequest{
		MediaType: coords.ContentKind,
		TMDBID:    coords.TMDBID,
		Season:    coords.TMDBSeason,
		Episode:   coords.TMDBEpisode,
		Title:     coords.TMDBTitle,
		Year:      coords.TMDBYear,
	}
	candidates, err := c.Search(ctx, searchReq)
	if err == nil {
		for _, cand := range candidates {
			if cand.Publication != nil && cand.Publication.PublicationID != "" {
				return true, nil
			}
		}
		return false, nil
	}
	entities, err := c.Catalog(ctx)
	if err != nil {
		return false, err
	}
	for _, entity := range entities {
		for _, source := range entity.Sources {
			if source.PublicationID == "" || source.RenditionID == "" {
				continue
			}
			if catalogSourceKey(entity, source) == key {
				return true, nil
			}
		}
	}
	return false, nil
}
