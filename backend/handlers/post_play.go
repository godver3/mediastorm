package handlers

import (
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"novastream/internal/auth"
	"novastream/internal/profileaccess"
	"novastream/models"
	"novastream/services/titleratings"
)

type completedCatalogueResolver interface {
	ResolveCompleted(profile, client, kind, id string) (models.Title, bool)
}

type postPlayContext struct {
	profile string
	client  string
	title   models.Title
}

// PostPlayContext redelivers a server-known title only after owned played
// history confirms its identity. It does not write ratings or watch state.
func (h *MetadataHandler) PostPlayContext(w http.ResponseWriter, r *http.Request) {
	current, ok := h.requirePostPlayContext(w, r)
	if !ok {
		return
	}
	ratings, err := h.TitleRatings.List(r.Context(), current.profile)
	if err != nil {
		http.Error(w, "personal rating unavailable", http.StatusServiceUnavailable)
		return
	}
	title := current.title
	if meta := h.serviceForUser(current.profile); meta != nil {
		poster, backdrop, _ := meta.GetCachedArtworkURLs(title.MediaType, title.TMDBID, title.TVDBID)
		if poster != "" {
			title.TextPoster = &models.Image{URL: poster, Type: "poster"}
		}
		if backdrop != "" {
			title.Backdrop = &models.Image{URL: backdrop, Type: "backdrop"}
		}
		title.Overview = meta.GetCachedOverview(title.MediaType, title.TMDBID, title.TVDBID)
	}
	// The ordinary 30-minute rating projection is renewed only after every
	// authorization, history and current child-policy check has succeeded.
	h.TitleRatings.Observe(current.profile, current.client, []models.Title{title})
	query := r.URL.Query()
	query.Set("userId", current.profile)
	responseRequest := r.Clone(r.Context())
	copiedURL := *r.URL
	copiedURL.RawQuery = query.Encode()
	responseRequest.URL = &copiedURL
	w.Header().Set("Content-Type", "application/json")
	_ = h.encodeMetadata(w, responseRequest, map[string]any{"title": title, "rating": titleratings.Match(ratings, title), "ratingScope": title.MediaType, "completion": map[bool]string{true: "movie", false: "episode"}[title.MediaType == "movie"]})
}

func (h *MetadataHandler) requirePostPlayContext(w http.ResponseWriter, r *http.Request) (postPlayContext, bool) {
	w.Header().Set("Cache-Control", "private, no-store")
	vars := mux.Vars(r)
	profile, kind, id := strings.TrimSpace(vars["userID"]), vars["mediaType"], strings.TrimSpace(vars["titleID"])
	client := requestClientID(r)
	itemID := strings.TrimSpace(r.URL.Query().Get("itemId"))
	fail := func(status int, message string) (postPlayContext, bool) {
		http.Error(w, message, status)
		return postPlayContext{}, false
	}
	if auth.IsShareLinkRequest(r) {
		return fail(http.StatusForbidden, "a profile session is required")
	}
	if session, ok := auth.GetSession(r); ok && session.Scope == models.SessionScopeStream {
		return fail(http.StatusForbidden, "a profile session is required")
	}
	if profile == "" || client == "" || len(client) > 128 || id == "" || len(id) > 128 || kind != "movie" && kind != "series" || len(itemID) > 512 {
		return fail(http.StatusBadRequest, "valid profile, client and title coordinates are required")
	}
	if !profileaccess.CheckResource(w, r, profile, "") {
		return postPlayContext{}, false
	}
	if h.UsersService == nil {
		return fail(http.StatusNotFound, "profile not found")
	}
	user, exists := h.UsersService.Get(profile)
	if !exists || !auth.IsMaster(r) && (auth.GetAccountID(r) == "" || user.AccountID != auth.GetAccountID(r)) {
		return fail(http.StatusNotFound, "profile not found")
	}
	if h.TitleRatings == nil || h.HistoryService == nil {
		return fail(http.StatusServiceUnavailable, "post-play context unavailable")
	}
	resolver, ok := h.TitleRatings.(completedCatalogueResolver)
	if !ok {
		return fail(http.StatusServiceUnavailable, "post-play context unavailable")
	}
	title, known := resolver.ResolveCompleted(profile, client, kind, id)
	if !known {
		return fail(http.StatusConflict, "reload this title before opening post-play")
	}
	if !h.ratingTitleAllowed(profile, title) {
		return fail(http.StatusNotFound, "title not found")
	}
	historyKind := "movie"
	if kind == "series" {
		historyKind = "episode"
		if itemID == "" {
			return fail(http.StatusBadRequest, "the completed episode identity is required")
		}
	}
	if itemID == "" {
		itemID = id
	}
	history, err := h.HistoryService.GetWatchHistoryItem(profile, historyKind, itemID)
	if err != nil {
		return fail(http.StatusServiceUnavailable, "playback history unavailable")
	}
	if history == nil || !history.Watched || history.Source != models.WatchSourcePlayed || !postPlayHistoryMatches(title, *history) {
		return fail(http.StatusConflict, "completed playback is not recorded for this title")
	}
	return postPlayContext{profile: profile, client: client, title: title}, true
}

func postPlayHistoryMatches(title models.Title, item models.WatchHistoryItem) bool {
	id := item.ItemID
	if title.MediaType == "movie" {
		if item.MediaType != "movie" {
			return false
		}
	} else {
		if item.MediaType != "episode" && item.MediaType != "series" || item.SeriesID == "" {
			return false
		}
		id = item.SeriesID
	}
	historyTitle := titleFromSharedShelfItem(id, "", title.MediaType, 0, item.ExternalIDs)
	for _, a := range titleratings.Aliases(title) {
		for _, b := range titleratings.Aliases(historyTitle) {
			if a == b {
				return true
			}
		}
	}
	return false
}

// ObserveDetailsCatalogue accepts resolved server metadata, never query metadata.
// The details entrypoint has already checked resource ownership and profile PIN.
func (h *MetadataHandler) ObserveDetailsCatalogue(r *http.Request, titles []models.Title) {
	if h.TitleRatings == nil || h.UsersService == nil || auth.IsShareLinkRequest(r) {
		return
	}
	if session, ok := auth.GetSession(r); ok && session.Scope == models.SessionScopeStream {
		return
	}
	profile := strings.TrimSpace(mux.Vars(r)["userID"])
	client := requestClientID(r)
	if profile == "" || client == "" || len(client) > 128 {
		return
	}
	user, exists := h.UsersService.Get(profile)
	if !exists || !auth.IsMaster(r) && (auth.GetAccountID(r) == "" || user.AccountID != auth.GetAccountID(r)) {
		return
	}
	if len(titles) > 82 {
		titles = titles[:82]
	}
	allowed := make([]models.Title, 0, len(titles))
	for _, title := range titles {
		if h.ratingTitleAllowed(profile, title) {
			allowed = append(allowed, title)
		}
	}
	h.TitleRatings.Observe(profile, client, allowed)
}
