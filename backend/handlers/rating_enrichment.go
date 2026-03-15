package handlers

import (
	"context"
	"log"
	"time"

	"novastream/models"
)

// cacheMiss records a title whose ratings are not yet in the MDBList cache.
type cacheMiss struct {
	imdbID    string
	mediaType string
}

// warmCacheMisses fires a background goroutine to fetch ratings for uncached items.
// Uses a single sequential worker to stay within MDBList API rate limits.
func warmCacheMisses(misses []cacheMiss, meta metadataService) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		for _, m := range misses {
			if ctx.Err() != nil {
				break
			}
			if _, err := meta.GetMDBListAllRatings(ctx, m.imdbID, m.mediaType); err != nil {
				log.Printf("[rating-enrichment] background warm error for %s: %v", m.imdbID, err)
			}
		}
		log.Printf("[rating-enrichment] background cache warm done for %d items", len(misses))
	}()
}

// enrichWatchlistRatings hydrates watchlist items with MDBList ratings.
// It uses cached ratings only (non-blocking) and fires a background goroutine
// to warm the cache for any items that don't have cached ratings yet.
func enrichWatchlistRatings(_ context.Context, items []models.WatchlistItem, meta metadataService) {
	if meta == nil || !meta.MDBListIsEnabled() {
		return
	}

	var misses []cacheMiss
	for i := range items {
		imdbID := items[i].ExternalIDs["imdb"]
		if imdbID == "" {
			continue
		}
		mediaType := "movie"
		if items[i].MediaType == "series" {
			mediaType = "show"
		}
		if ratings := meta.GetMDBListAllRatingsCached(imdbID, mediaType); ratings != nil {
			items[i].Ratings = ratings
		} else {
			misses = append(misses, cacheMiss{imdbID: imdbID, mediaType: mediaType})
		}
	}

	if len(misses) > 0 {
		log.Printf("[rating-enrichment] watchlist: %d/%d cached, warming %d in background", len(items)-len(misses), len(items), len(misses))
		warmCacheMisses(misses, meta)
	}
}

// enrichTrendingRatings hydrates trending/explore items with MDBList ratings.
// Same non-blocking pattern: attach cached ratings, warm misses in background.
func enrichTrendingRatings(items []models.TrendingItem, meta metadataService) {
	if meta == nil || !meta.MDBListIsEnabled() {
		return
	}

	var misses []cacheMiss
	for i := range items {
		imdbID := items[i].Title.IMDBID
		if imdbID == "" {
			continue
		}
		mediaType := "movie"
		if items[i].Title.MediaType == "series" {
			mediaType = "show"
		}
		if ratings := meta.GetMDBListAllRatingsCached(imdbID, mediaType); ratings != nil {
			items[i].Title.Ratings = ratings
		} else {
			misses = append(misses, cacheMiss{imdbID: imdbID, mediaType: mediaType})
		}
	}

	if len(misses) > 0 {
		log.Printf("[rating-enrichment] trending: %d/%d cached, warming %d in background", len(items)-len(misses), len(items), len(misses))
		warmCacheMisses(misses, meta)
	}
}
