package history

import (
	"context"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"novastream/internal/mediaidentity"
	"novastream/models"
)

// hiddenContinueWatchingEnrichLimit bounds concurrent metadata lookups when
// listing hidden titles; the lookups are cached, so this only matters cold.
const hiddenContinueWatchingEnrichLimit = 4

type hiddenContinueWatchingGroup struct {
	identity mediaidentity.Identity
	item     models.HiddenItem
}

// ListHiddenFromContinueWatching returns one entry per title the profile hid
// from Continue Watching, newest hide first. Rows hidden under different ID
// forms of the same title (tmdb vs tvdb vs imdb) collapse into one entry, and
// each entry carries the title's name and artwork where metadata has them.
func (s *Service) ListHiddenFromContinueWatching(ctx context.Context, userID string) ([]models.HiddenItem, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUserIDRequired
	}

	s.mu.RLock()
	var groups []*hiddenContinueWatchingGroup
	for _, progress := range s.playbackProgress[userID] {
		if !progress.HiddenFromContinueWatching {
			continue
		}
		mediaType, id, name, identity := hiddenContinueWatchingTitle(progress)
		if id == "" {
			continue
		}
		var group *hiddenContinueWatchingGroup
		for _, existing := range groups {
			if existing.item.MediaType == mediaType && identitiesReferToSameTitle(existing.identity, identity) {
				group = existing
				break
			}
		}
		if group == nil {
			group = &hiddenContinueWatchingGroup{
				identity: identity,
				item: models.HiddenItem{
					ID:          id,
					MediaType:   mediaType,
					ExternalIDs: map[string]string{},
				},
			}
			groups = append(groups, group)
		}
		if group.item.Name == "" {
			group.item.Name = name
		}
		if group.item.Year == 0 && mediaType == "movie" {
			group.item.Year = progress.Year
		}
		if progress.UpdatedAt.After(group.item.HiddenAt) {
			group.item.HiddenAt = progress.UpdatedAt
		}
		for _, key := range []string{"imdb", "tmdb", "tvdb"} {
			if v := strings.TrimSpace(progress.ExternalIDs[key]); v != "" && group.item.ExternalIDs[key] == "" {
				group.item.ExternalIDs[key] = v
			}
		}
	}
	s.mu.RUnlock()

	items := make([]models.HiddenItem, len(groups))
	for i, group := range groups {
		if len(group.item.ExternalIDs) == 0 {
			group.item.ExternalIDs = nil
		}
		items[i] = group.item
	}

	s.enrichHiddenContinueWatchingItems(ctx, items)

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].HiddenAt.Equal(items[j].HiddenAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].HiddenAt.After(items[j].HiddenAt)
	})
	return items, nil
}

// hiddenContinueWatchingTitle resolves the title a hidden progress row stands
// for: a movie, or the series an episode row (or series-level marker) belongs to.
func hiddenContinueWatchingTitle(progress models.PlaybackProgress) (mediaType, id, name string, identity mediaidentity.Identity) {
	if strings.EqualFold(strings.TrimSpace(progress.MediaType), "movie") {
		id = strings.TrimSpace(progress.ItemID)
		if id == "" {
			id = strings.TrimSpace(progress.SeriesID)
		}
		identity = mediaidentity.Resolve(mediaidentity.Input{MediaType: "movie", ID: id, ExternalIDs: progress.ExternalIDs})
		if identity.ID != "" {
			id = identity.ID
		}
		return "movie", id, strings.TrimSpace(progress.MovieName), identity
	}

	seriesID := strings.TrimSpace(progress.SeriesID)
	if seriesID == "" {
		seriesID = mediaidentity.InferSeriesIDFromEpisodeItemID(progress.ItemID)
	}
	if seriesID == "" && isSeriesLevelPlaybackMarker(progress) {
		seriesID = strings.TrimSpace(progress.ItemID)
	}
	if seriesID == "" {
		return "series", "", "", mediaidentity.Identity{}
	}
	identity = mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   "series",
		ID:          seriesID,
		ExternalIDs: canonicalSeriesExternalIDs(seriesID, progress.ItemID, progress.ExternalIDs),
	})
	if identity.ID != "" {
		seriesID = identity.ID
	}
	return "series", seriesID, strings.TrimSpace(progress.SeriesName), identity
}

// enrichHiddenContinueWatchingItems fills name, year and artwork from metadata.
// A failed lookup leaves the entry as it is; the stored name (if any) stays.
func (s *Service) enrichHiddenContinueWatchingItems(ctx context.Context, items []models.HiddenItem) {
	if len(items) == 0 {
		return
	}
	s.mu.RLock()
	hasMetadata := s.metadataService != nil
	s.mu.RUnlock()
	if !hasMetadata {
		return
	}

	sem := make(chan struct{}, hiddenContinueWatchingEnrichLimit)
	var wg sync.WaitGroup
	for i := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(item *models.HiddenItem) {
			defer wg.Done()
			defer func() { <-sem }()
			lookupCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()

			var title *models.Title
			var err error
			if item.MediaType == "movie" {
				title, err = s.getMovieMetadataWithCache(lookupCtx, item.ID, item.Name, item.Year, item.ExternalIDs)
			} else {
				title, err = s.getSeriesInfoWithCache(lookupCtx, item.ID, item.Name, item.ExternalIDs)
			}
			if err != nil || title == nil {
				if err != nil {
					log.Printf("[history] hidden continue-watching: metadata lookup failed id=%q: %v", item.ID, err)
				}
				return
			}
			if strings.TrimSpace(title.Name) != "" {
				item.Name = title.Name
			}
			if title.Year > 0 {
				item.Year = title.Year
			}
			if title.Poster != nil {
				item.PosterURL = title.Poster.URL
			}
			if title.Backdrop != nil {
				item.BackdropURL = title.Backdrop.URL
			}
		}(&items[i])
	}
	wg.Wait()
}

// UnhideFromContinueWatching reverses HideFromContinueWatching for a series or
// movie: pure hide markers are removed and real progress rows lose their hidden
// flag, so the title returns to Continue Watching if it still qualifies. Rows
// stored under any alias of the title are matched. Unhiding a title that is not
// hidden is a no-op.
func (s *Service) UnhideFromContinueWatching(userID, seriesID string) error {
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

	perUser, ok := s.playbackProgress[userID]
	if !ok {
		return nil
	}

	externalIDs := s.collectExternalIDsForSeriesLocked(userID, seriesID)
	movieTarget := mediaidentity.Resolve(mediaidentity.Input{MediaType: "movie", ID: seriesID, ExternalIDs: externalIDs})
	seriesTarget := mediaidentity.Resolve(mediaidentity.Input{MediaType: "series", ID: seriesID, ExternalIDs: externalIDs})

	removed, unflagged := 0, 0
	for key, progress := range perUser {
		if !progress.HiddenFromContinueWatching {
			continue
		}
		match := progress.MediaType != "episode" && playbackProgressMatchesIdentity(progress, movieTarget)
		if !match {
			match = progressSeriesMatchesIdentity(progress, seriesTarget)
		}
		if !match {
			continue
		}
		if isHideOnlyMarker(progress) {
			delete(perUser, key)
			removed++
			continue
		}
		progress.HiddenFromContinueWatching = false
		perUser[key] = progress
		unflagged++
	}
	if removed+unflagged == 0 {
		log.Printf("[history] UnhideFromContinueWatching: nothing hidden for user=%q seriesID=%q", userID, seriesID)
		return nil
	}

	s.bumpWatchStateRevisionLocked(userID)
	s.invalidateContinueWatchingLocked(userID)
	if err := s.savePlaybackProgressLocked(); err != nil {
		log.Printf("[history] UnhideFromContinueWatching: save failed: %v", err)
		return err
	}
	log.Printf("[history] UnhideFromContinueWatching: user=%q seriesID=%q removed %d markers, unhid %d rows", userID, seriesID, removed, unflagged)
	return nil
}

// isHideOnlyMarker reports whether a hidden row exists only to record the hide
// (a series-level marker or a movie marker with no playback), as opposed to a
// real progress row that was flagged hidden.
func isHideOnlyMarker(progress models.PlaybackProgress) bool {
	if isSeriesLevelPlaybackMarker(progress) {
		return true
	}
	return progress.Position == 0 && progress.Duration == 0
}
