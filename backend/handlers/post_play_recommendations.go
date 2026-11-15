package handlers

import (
	"context"
	"net/http"
	"sort"
	"time"

	"novastream/models"
	"novastream/services/titleratings"
)

type postPlayHiddenItems interface {
	List(string) ([]models.HiddenItem, error)
}

// PostPlayRecommendations reuses current profile inputs and filtering. It never
// warms ratings/artwork or resolves playback sources just to display candidates.
func (h *MetadataHandler) PostPlayRecommendations(w http.ResponseWriter, r *http.Request) {
	current, ok := h.requirePostPlayContext(w, r)
	if !ok {
		return
	}
	if h.PostPlayHidden == nil {
		http.Error(w, "hidden-title policy unavailable", 503)
		return
	}
	hidden, err := h.PostPlayHidden.List(current.profile)
	if err != nil {
		http.Error(w, "hidden-title policy unavailable", 503)
		return
	}
	ratings, err := h.TitleRatings.List(r.Context(), current.profile)
	if err != nil {
		http.Error(w, "personal recommendations unavailable", 503)
		return
	}
	history, err := h.HistoryService.ListWatchHistory(current.profile)
	if err != nil {
		http.Error(w, "playback history unavailable", 503)
		return
	}
	progress, err := h.HistoryService.ListPlaybackProgress(current.profile)
	if err != nil {
		http.Error(w, "playback history unavailable", 503)
		return
	}
	ratingSeeds, err := h.ratingRecommendationSeeds(r.Context(), current.profile)
	if err != nil {
		http.Error(w, "personal recommendations unavailable", 503)
		return
	}
	tasteSeeds, err := h.tasteRecommendationSeeds(r.Context(), current.profile)
	if err != nil {
		http.Error(w, "personal recommendations unavailable", 503)
		return
	}
	additional := append(h.additionalRecommendationSeeds(current.profile), ratingSeeds...)
	additional = append(additional, tasteSeeds...)
	policy := h.personalizedKidsRatingFilter(current.profile)
	language := ""
	if h.CfgManager != nil {
		if cfg, loadErr := h.CfgManager.Load(); loadErr == nil {
			language, _ = resolveMetadataLanguage(cfg, h.UserSettings, current.profile)
		}
	}
	key := personalizedRecommendationsCacheKey(current.profile, 90, 6, history, progress) + ":" + recommendationInputsFingerprint(additional, h.recommendationSettings(current.profile), policy, language) + ":post-play"
	cachedFallback := h.postPlayCachedSuggestions(current.profile, ratings)
	response, hit := h.cachedPersonalizedRecommendations(key)
	if !hit && h.serviceForUser(current.profile) != nil {
		build, leader := h.beginPersonalizedRecommendationsBuild(key)
		if leader {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			response = h.buildPersonalizedRecommendations(ctx, current.profile, history, progress, 90, 6, policy, additional, true)
			cacheable := ctx.Err() == nil && len(response.Items) > 0
			cancel()
			h.finishPersonalizedRecommendationsBuild(key, build, response, cacheable, current.profile)
		} else {
			select {
			case <-r.Context().Done():
				return
			case <-build.done:
				response = build.response
			}
		}
	}
	items := response.Items
	source := "personalized"
	if len(items) == 0 {
		source = "saved_choices"
		items = cachedFallback
		for _, rating := range ratings {
			if rating.Value == "like" || rating.Value == "love" {
				items = append(items, models.TrendingItem{Title: rating.Title})
			}
		}
		if h.Taste != nil {
			setup, loadErr := h.Taste.Get(r.Context(), current.profile)
			if loadErr != nil {
				http.Error(w, "saved interests unavailable", 503)
				return
			}
			for _, interest := range setup.Interests {
				items = append(items, models.TrendingItem{Title: interest.Title})
			}
		}
	}
	items = h.filterPostPlaySuggestions(current.profile, current.title, items, history, progress, hidden, ratings)
	if len(items) == 0 {
		source = "unavailable"
	}
	// Enrich only this bounded page from existing local metadata caches.
	if meta := h.serviceForUser(current.profile); meta != nil {
		for i := range items {
			title := &items[i].Title
			poster, backdrop, _ := meta.GetCachedArtworkURLs(title.MediaType, title.TMDBID, title.TVDBID)
			if poster != "" {
				title.TextPoster = &models.Image{URL: poster, Type: "poster"}
			}
			if backdrop != "" {
				title.Backdrop = &models.Image{URL: backdrop, Type: "backdrop"}
			}
		}
	}
	query := r.URL.Query()
	query.Set("userId", current.profile)
	responseRequest := r.Clone(r.Context())
	copiedURL := *r.URL
	copiedURL.RawQuery = query.Encode()
	responseRequest.URL = &copiedURL
	w.Header().Set("Content-Type", "application/json")
	_ = h.encodeMetadata(w, responseRequest, map[string]any{"items": items, "total": len(items), "source": source, "rating": titleratings.Match(ratings, current.title), "ratingScope": current.title.MediaType})
}

func (h *MetadataHandler) postPlayCachedSuggestions(profile string, ratings []models.TitleRating) []models.TrendingItem {
	type cached struct {
		key   string
		entry personalizedRecommendationsCacheEntry
	}
	snapshots := []cached{}
	h.personalizedMu.Lock()
	for key, entry := range h.personalizedCache {
		if entry.profile == profile && time.Since(entry.expiresAt) < 24*time.Hour {
			snapshots = append(snapshots, cached{key, entry})
		}
	}
	h.personalizedMu.Unlock()
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].entry.expiresAt.Equal(snapshots[j].entry.expiresAt) {
			return snapshots[i].key < snapshots[j].key
		}
		return snapshots[i].entry.expiresAt.After(snapshots[j].entry.expiresAt)
	})
	out := []models.TrendingItem{}
	for _, snapshot := range snapshots {
		allowed := true
		for _, rating := range ratings {
			if rating.Value != "dislike" {
				continue
			}
			if rating.Title.TMDBID <= 0 || snapshot.entry.response.Explanation == nil {
				allowed = false
				break
			}
			for _, seed := range snapshot.entry.response.Explanation.Seeds {
				if seed.MediaType == rating.Title.MediaType && seed.TMDBID == rating.Title.TMDBID {
					allowed = false
					break
				}
			}
			if !allowed {
				break
			}
		}
		if allowed {
			out = append(out, snapshot.entry.response.Items...)
		}
		if len(out) >= 120 {
			return out[:120]
		}
	}
	return out
}

func (h *MetadataHandler) filterPostPlaySuggestions(profile string, current models.Title, candidates []models.TrendingItem, history []models.WatchHistoryItem, progress []models.PlaybackProgress, hidden []models.HiddenItem, ratings []models.TitleRating) []models.TrendingItem {
	excluded := map[string]bool{}
	add := func(title models.Title) {
		for _, key := range titleratings.Aliases(title) {
			excluded[key] = true
		}
	}
	add(current)
	for _, item := range history {
		if !item.Watched {
			continue
		}
		id, kind := item.ItemID, item.MediaType
		if kind == "episode" || kind == "series" {
			if item.SeriesID != "" {
				id = item.SeriesID
			} else if kind == "episode" {
				continue
			}
			kind = "series"
		}
		add(titleFromSharedShelfItem(id, "", kind, 0, item.ExternalIDs))
	}
	for _, item := range progress {
		if item.Position <= 0 && item.PercentWatched <= 0 {
			continue
		}
		id, kind := item.ItemID, item.MediaType
		if kind == "episode" {
			id = item.SeriesID
			kind = "series"
		}
		add(titleFromSharedShelfItem(id, "", kind, 0, item.ExternalIDs))
	}
	for _, item := range hidden {
		add(titleFromSharedShelfItem(item.ID, "", item.MediaType, 0, item.ExternalIDs))
	}
	movies, series := []models.TrendingItem{}, []models.TrendingItem{}
	for _, candidate := range candidates {
		title := candidate.Title
		if title.MediaType == "tv" {
			title.MediaType = "series"
		}
		if title.ID == "" || len(title.ID) > 128 || title.MediaType != "movie" && title.MediaType != "series" || !h.ratingTitleAllowed(profile, title) || titleratings.Match(ratings, title) == "dislike" {
			continue
		}
		keys := titleratings.Aliases(title)
		reject := false
		for _, key := range keys {
			if excluded[key] {
				reject = true
				break
			}
		}
		if reject || len(keys) == 0 {
			continue
		}
		add(title)
		candidate.Title = title
		if title.MediaType == "movie" {
			if len(movies) < 6 {
				movies = append(movies, candidate)
			}
		} else if len(series) < 6 {
			series = append(series, candidate)
		}
	}
	out := append(movies, series...)
	if current.MediaType == "series" {
		out = append(series, movies...)
	}
	if out == nil {
		out = []models.TrendingItem{}
	}
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}
