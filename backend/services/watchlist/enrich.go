package watchlist

import (
	"context"
	"strconv"

	"novastream/models"
)

// ArtworkMetadataProvider supplies the metadata lookups needed to enrich
// watchlist items that arrive without artwork. Items imported from an external
// source (Trakt/MDBList/Plex/Jellyfin sync) carry only IDs and a title, and the
// metadata cache is cold for them, so a thumbnail never resolves until the cache
// is warmed (which otherwise only happens when the user opens the details page
// or manually re-adds the item).
//
// The full *services/metadata.Service satisfies this interface, as does the
// handler's metadataService interface.
type ArtworkMetadataProvider interface {
	GetTextPosterURL(mediaType string, tmdbID int64, tvdbID int64) string
	MovieDetails(ctx context.Context, req models.MovieDetailsQuery) (*models.Title, error)
	SeriesDetails(ctx context.Context, req models.SeriesDetailsQuery) (*models.SeriesDetails, error)
}

// EnrichMissingArtwork fills artwork for watchlist items that lack a text poster.
// For each such item it first tries the cheap cache-only lookup; when the item
// has no thumbnail at all and the cache is cold, it warms the metadata cache with
// a single details fetch, then retries and persists the resolved text poster URL.
// It returns the number of items enriched.
//
// This is safe to call repeatedly: items that already have a text poster are
// skipped, and a fetch that resolves nothing leaves the item untouched for a
// later retry.
func (s *Service) EnrichMissingArtwork(userIDs []string, meta ArtworkMetadataProvider) int {
	if meta == nil {
		return 0
	}
	updated := 0
	for _, userID := range userIDs {
		items, err := s.List(userID)
		if err != nil {
			continue
		}
		for _, item := range items {
			if item.TextPosterURL != "" {
				continue // already has a text poster
			}
			tmdbID, tvdbID := NumericIDs(item.ExternalIDs)
			if tmdbID == 0 && tvdbID == 0 {
				continue
			}

			// Cheap path: artwork may already be warm in the cache.
			url := meta.GetTextPosterURL(item.MediaType, tmdbID, tvdbID)

			// Cold cache and no thumbnail at all (e.g. a fresh external import):
			// warm the metadata cache with a details fetch, then retry. Items
			// that already have a plain poster still render a thumbnail, so we
			// don't spend an API call on them here.
			if url == "" && item.PosterURL == "" {
				warmWatchlistArtworkCache(meta, item, tmdbID, tvdbID)
				url = meta.GetTextPosterURL(item.MediaType, tmdbID, tvdbID)
			}

			if url == "" {
				continue
			}

			if _, err := s.AddOrUpdate(userID, models.WatchlistUpsert{
				ID:            item.ID,
				MediaType:     item.MediaType,
				Name:          item.Name,
				TextPosterURL: url,
				ExternalIDs:   item.ExternalIDs,
			}); err != nil {
				continue
			}
			updated++
		}
	}
	return updated
}

// warmWatchlistArtworkCache performs a single metadata details fetch for an item
// so the artwork cache (read by GetTextPosterURL) is populated. Errors are
// ignored: a failed fetch simply leaves the item unenriched, to be retried later.
func warmWatchlistArtworkCache(meta ArtworkMetadataProvider, item models.WatchlistItem, tmdbID, tvdbID int64) {
	imdbID := ""
	if item.ExternalIDs != nil {
		imdbID = item.ExternalIDs["imdb"]
	}
	ctx := context.Background()
	if item.MediaType == "movie" {
		meta.MovieDetails(ctx, models.MovieDetailsQuery{
			TitleID: item.ID,
			Name:    item.Name,
			Year:    item.Year,
			TMDBID:  tmdbID,
			TVDBID:  tvdbID,
			IMDBID:  imdbID,
		})
		return
	}
	meta.SeriesDetails(ctx, models.SeriesDetailsQuery{
		TitleID: item.ID,
		Name:    item.Name,
		Year:    item.Year,
		TVDBID:  tvdbID,
		TMDBID:  tmdbID,
		IMDBID:  imdbID,
	})
}

// NumericIDs extracts the numeric TMDB and TVDB IDs from an item's external ID
// map, returning 0 for any that are absent or unparseable.
func NumericIDs(externalIDs map[string]string) (tmdbID, tvdbID int64) {
	if v, ok := externalIDs["tmdb"]; ok {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			tmdbID = id
		}
	}
	if v, ok := externalIDs["tvdb"]; ok {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			tvdbID = id
		}
	}
	return tmdbID, tvdbID
}
