package localmedia

import (
	"context"
	"os"
	"sort"
	"strings"

	"novastream/models"
)

// FindPlaybackCandidates reads the scanned catalog without enriching metadata or
// scanning the filesystem. Automatic playback requires a confirmed identity;
// fuzzy names and low-confidence matches are only suitable for manual browsing.
func (s *Service) FindPlaybackCandidates(ctx context.Context, query models.LocalMediaMatchQuery, episode *models.EpisodeReference, allowLibrary func(string) bool) ([]models.LocalMediaItem, error) {
	if allowLibrary == nil || (strings.TrimSpace(query.TitleID) == "" && strings.TrimSpace(query.IMDBID) == "" && strings.TrimSpace(query.TMDBID) == "" && strings.TrimSpace(query.TVDBID) == "") {
		return nil, nil
	}
	libraryType := normalizeLookupLibraryType(query.MediaType)
	if libraryType == "" || (libraryType == models.LocalMediaLibraryTypeShow && (episode == nil || episode.SeasonNumber < 0 || episode.EpisodeNumber <= 0)) {
		return nil, nil
	}
	libraries, err := s.repo.ListLibraries(ctx)
	if err != nil {
		return nil, err
	}
	var candidates []models.LocalMediaItem
	for _, library := range libraries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if library.Type != libraryType || !allowLibrary(library.ID) {
			continue
		}
		items, err := s.repo.ListAllItemsByLibrary(ctx, library.ID)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if item.IsMissing || item.LibraryID != library.ID || item.LibraryType != libraryType || (item.MatchStatus != models.LocalMediaMatchStatusMatched && item.MatchStatus != models.LocalMediaMatchStatusManual) {
				continue
			}
			hydrateLocalMediaItemExternalIDs(&item)
			if !fallbackIdentityMatches(item, query) {
				continue
			}
			if libraryType == models.LocalMediaLibraryTypeShow && (item.SeasonNumber != episode.SeasonNumber || item.EpisodeNumber != episode.EpisodeNumber) {
				continue
			}
			candidates = append(candidates, item)
		}
	}
	// Catalog ordering is not stable across database implementations.
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	return candidates, nil
}

func fallbackIdentityMatches(item models.LocalMediaItem, query models.LocalMediaMatchQuery) bool {
	if item.ExternalIDs != nil {
		for _, pair := range [][2]string{{item.ExternalIDs.IMDB, query.IMDBID}, {item.ExternalIDs.TMDB, query.TMDBID}, {item.ExternalIDs.TVDB, query.TVDBID}} {
			if strings.TrimSpace(pair[0]) != "" && strings.TrimSpace(pair[1]) != "" && !strings.EqualFold(strings.TrimSpace(pair[0]), strings.TrimSpace(pair[1])) {
				return false
			}
		}
	}
	return localMediaItemMatchesLookup(item, query)
}

// A stale network mount can block os.Stat/os.Open beyond a context deadline.
// Bound both the caller's wait and the number of outstanding filesystem calls.
var fallbackFileChecks = make(chan struct{}, 8)

func (s *Service) GetFallbackItem(ctx context.Context, itemID string) (*models.LocalMediaItem, error) {
	select {
	case fallbackFileChecks <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	type result struct {
		item *models.LocalMediaItem
		err  error
	}
	done := make(chan result, 1)
	go func() {
		defer func() { <-fallbackFileChecks }()
		item, err := s.GetItem(ctx, itemID)
		if err == nil {
			var file *os.File
			file, err = os.Open(item.FilePath)
			if err == nil {
				info, statErr := file.Stat()
				err = statErr
				if statErr == nil {
					if !info.Mode().IsRegular() || info.Size() == 0 {
						err = ErrLocalMediaNotPlayable
					} else {
						item.SizeBytes = info.Size()
					}
				}
				_ = file.Close()
			}
		}
		done <- result{item, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-done:
		return result.item, result.err
	}
}
