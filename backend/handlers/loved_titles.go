package handlers

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gorilla/mux"
	"novastream/internal/auth"
	"novastream/internal/profileaccess"
	"novastream/models"
	"novastream/services/titleratings"
)

// LovedTitles is a read-only view of existing explicit preferences. It does
// not create a second favorites list or start metadata/provider searches.
func (h *MetadataHandler) LovedTitles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	profile := strings.TrimSpace(mux.Vars(r)["userID"])
	client := requestClientID(r)
	if auth.IsShareLinkRequest(r) {
		http.Error(w, "a profile session is required", http.StatusForbidden)
		return
	}
	if session, ok := auth.GetSession(r); ok && session.Scope == models.SessionScopeStream {
		http.Error(w, "a profile session is required", http.StatusForbidden)
		return
	}
	if profile == "" || client == "" || len(client) > 128 {
		http.Error(w, "profile and client identity are required", http.StatusBadRequest)
		return
	}
	if !profileaccess.CheckResource(w, r, profile, "") {
		return
	}
	if h.UsersService == nil {
		http.NotFound(w, r)
		return
	}
	user, exists := h.UsersService.Get(profile)
	if !exists || !auth.IsMaster(r) && (auth.GetAccountID(r) == "" || user.AccountID != auth.GetAccountID(r)) {
		http.NotFound(w, r)
		return
	}
	if h.TitleRatings == nil {
		http.Error(w, "personal favorites unavailable", http.StatusServiceUnavailable)
		return
	}
	if user.IsKidsProfile && !h.tasteAvailable(profile) {
		http.Error(w, "personal favorites unavailable for this profile", http.StatusForbidden)
		return
	}
	ratings, err := h.TitleRatings.List(r.Context(), profile)
	if err != nil {
		http.Error(w, "could not load personal favorites", http.StatusServiceUnavailable)
		return
	}
	// Repositories and test adapters may share slices; sort a private copy.
	ratings = append([]models.TitleRating(nil), ratings...)
	sort.SliceStable(ratings, func(i, j int) bool {
		if !ratings[i].UpdatedAt.Equal(ratings[j].UpdatedAt) {
			return ratings[i].UpdatedAt.After(ratings[j].UpdatedAt)
		}
		return ratings[i].Title.MediaType+":"+ratings[i].Title.ID < ratings[j].Title.MediaType+":"+ratings[j].Title.ID
	})
	items := []models.TrendingItem{}
	for _, rating := range ratings {
		title := titleratings.BareTitle(rating.Title)
		if rating.Value != "love" || title.ID == "" || len(title.ID) > 128 || title.MediaType != "movie" && title.MediaType != "series" || !h.ratingTitleAllowed(profile, title) {
			continue
		}
		items = append(items, models.TrendingItem{Rank: len(items) + 1, Title: title})
	}
	query := parseDisplayListQuery(r)
	// Read this profile's existing state without warming provider metadata.
	if h.HistoryService != nil {
		history, historyErr := h.HistoryService.ListWatchHistory(profile)
		series, seriesErr := h.HistoryService.ListSeriesStates(profile)
		progress, progressErr := h.HistoryService.ListPlaybackProgress(profile)
		if historyErr == nil && seriesErr == nil && progressErr == nil {
			enrichTrendingItems(items, buildWatchStateIndex(history, series, progress), nil, false)
		} else if query.WatchStatus != "" && query.WatchStatus != "all" {
			http.Error(w, "watch state unavailable", http.StatusServiceUnavailable)
			return
		}
	} else if query.WatchStatus != "" && query.WatchStatus != "all" {
		http.Error(w, "watch state unavailable", http.StatusServiceUnavailable)
		return
	}
	items = query.Apply(items)
	total := len(items)
	if total > maxDiscoveryListItems {
		total = maxDiscoveryListItems
		items = items[:total]
	}
	genres, alphabet := displayListGenres(items), displayListAlphabetBuckets(items, query)
	limit, offset := parseLimitOffset(r)
	items = paginateTrendingItems(items, offset, limit)
	meta := h.serviceForUser(profile)
	if meta != nil {
		for i := range items {
			title := &items[i].Title
			poster, backdrop, _ := meta.GetCachedArtworkURLs(title.MediaType, title.TMDBID, title.TVDBID)
			if poster != "" {
				title.TextPoster = &models.Image{URL: poster, Type: "poster"}
			}
			if backdrop != "" {
				title.Backdrop = &models.Image{URL: backdrop, Type: "backdrop"}
			}
			title.Overview = meta.GetCachedOverview(title.MediaType, title.TMDBID, title.TVDBID)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	queryValues := r.URL.Query()
	queryValues.Set("userId", profile)
	responseRequest := r.Clone(r.Context())
	copiedURL := *r.URL
	copiedURL.RawQuery = queryValues.Encode()
	responseRequest.URL = &copiedURL
	_ = h.encodeMetadata(w, responseRequest, map[string]interface{}{
		"source": "loved", "items": items, "total": total, "genres": genres, "alphabetBuckets": alphabet,
	})
}
