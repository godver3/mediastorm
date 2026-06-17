package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"

	"novastream/models"
	"novastream/services/customlists"
	"novastream/services/watchlist"

	"github.com/gorilla/mux"
)

type DisplayListHandler struct {
	WatchlistService   watchlistService
	CustomListsService customListsService
	Users              userService
	HistoryService     historyService
	HistoryHandler     *HistoryHandler
	MetadataService    metadataService
	MetadataHandler    *MetadataHandler
}

type DisplayListResponse struct {
	Source string      `json:"source"`
	ListID string      `json:"listId,omitempty"`
	Items  interface{} `json:"items"`
	Total  int         `json:"total"`
}

func NewDisplayListHandler(watchlist watchlistService, customLists customListsService, users userService) *DisplayListHandler {
	return &DisplayListHandler{
		WatchlistService:   watchlist,
		CustomListsService: customLists,
		Users:              users,
	}
}

func (h *DisplayListHandler) SetHistoryService(service historyService) {
	h.HistoryService = service
}

func (h *DisplayListHandler) SetHistoryHandler(handler *HistoryHandler) {
	h.HistoryHandler = handler
}

func (h *DisplayListHandler) SetMetadataService(service metadataService) {
	h.MetadataService = service
}

func (h *DisplayListHandler) SetMetadataHandler(handler *MetadataHandler) {
	h.MetadataHandler = handler
}

func (h *DisplayListHandler) Get(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	source := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source")))
	if source == "" {
		source = "watchlist"
	}
	metadataSource := source != "watchlist" &&
		source != "custom-list" &&
		source != "custom_user_list" &&
		source != "custom-user-list" &&
		source != "continue-watching" &&
		source != "continue_watching"
	if metadataSource && h.MetadataHandler == nil {
		http.Error(w, "metadata source is unavailable", http.StatusServiceUnavailable)
		return
	}

	listID := strings.TrimSpace(r.URL.Query().Get("listId"))
	var items []models.WatchlistItem
	var err error

	switch source {
	case "watchlist":
		if h.WatchlistService == nil {
			http.Error(w, "watchlist source is unavailable", http.StatusServiceUnavailable)
			return
		}
		items, err = h.WatchlistService.List(userID)
	case "custom-list", "custom_user_list", "custom-user-list":
		source = "custom-list"
		if h.CustomListsService == nil {
			http.Error(w, "custom list source is unavailable", http.StatusServiceUnavailable)
			return
		}
		if listID == "" {
			http.Error(w, "listId is required for custom-list source", http.StatusBadRequest)
			return
		}
		items, err = h.CustomListsService.ListItems(userID, listID)
	case "continue-watching", "continue_watching":
		source = "continue-watching"
		if h.HistoryHandler == nil {
			http.Error(w, "continue-watching source is unavailable", http.StatusServiceUnavailable)
			return
		}
		h.delegateMetadata(w, r, source, h.HistoryHandler.ListContinueWatching, displayListQuery(r, userID, nil))
		return
	case "top-ten":
		h.delegateMetadata(w, r, source, h.MetadataHandler.TopTen, displayListQuery(r, userID, map[string]string{
			"type": firstQueryValue(r, "mediaType", "type"),
		}))
		return
	case "trending":
		h.delegateMetadata(w, r, source, h.MetadataHandler.DiscoverNew, displayListQuery(r, userID, map[string]string{
			"type": firstQueryValue(r, "mediaType", "type"),
		}))
		return
	case "genre":
		h.delegateMetadata(w, r, source, h.MetadataHandler.DiscoverByGenre, displayListQuery(r, userID, map[string]string{
			"type": firstQueryValue(r, "mediaType", "type"),
		}))
		return
	case "decade":
		h.delegateMetadata(w, r, source, h.MetadataHandler.DiscoverByDecade, displayListQuery(r, userID, map[string]string{
			"type": firstQueryValue(r, "mediaType", "type"),
		}))
		return
	case "mdblist", "mdblist-url", "mdblist-shelf", "seasonal":
		source = "mdblist"
		h.delegateMetadata(w, r, source, h.MetadataHandler.CustomList, displayListQuery(r, userID, nil))
		return
	case "trakt-list":
		h.delegateMetadata(w, r, source, h.MetadataHandler.TraktList, displayListQuery(r, userID, nil))
		return
	case "simkl-list":
		h.delegateMetadata(w, r, source, h.MetadataHandler.SimklList, displayListQuery(r, userID, nil))
		return
	case "letterboxd-list":
		h.delegateMetadata(w, r, source, h.MetadataHandler.LetterboxdList, displayListQuery(r, userID, nil))
		return
	case "personalized", "my-recommended":
		source = "personalized"
		h.delegateMetadata(w, r, source, h.MetadataHandler.GetPersonalizedRecommendations, displayListQuery(r, userID, nil))
		return
	case "custom-ai":
		h.delegateMetadata(w, r, source, h.MetadataHandler.GetAICustomRecommendations, displayListQuery(r, userID, nil))
		return
	case "similar":
		h.delegateMetadata(w, r, source, h.MetadataHandler.Similar, displayListQuery(r, userID, map[string]string{
			"type": firstQueryValue(r, "mediaType", "type"),
		}))
		return
	case "collection":
		h.delegateMetadata(w, r, source, h.MetadataHandler.CollectionDetails, displayListQuery(r, userID, map[string]string{
			"id": firstQueryValue(r, "collectionId", "id"),
		}))
		return
	default:
		http.Error(w, "unsupported display list source", http.StatusBadRequest)
		return
	}

	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, customlists.ErrUserIDRequired), errors.Is(err, customlists.ErrListIDRequired):
			status = http.StatusBadRequest
		case errors.Is(err, os.ErrNotExist):
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	h.enrich(userID, items, r)

	if items == nil {
		items = []models.WatchlistItem{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(DisplayListResponse{
		Source: source,
		ListID: listID,
		Items:  items,
		Total:  len(items),
	})
}

func (h *DisplayListHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (h *DisplayListHandler) Post(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	source := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source")))
	if source != "curated" {
		http.Error(w, "unsupported display list source", http.StatusBadRequest)
		return
	}
	if h.MetadataHandler == nil {
		http.Error(w, "metadata source is unavailable", http.StatusServiceUnavailable)
		return
	}
	h.delegateMetadata(w, r, source, h.MetadataHandler.CuratedList, displayListQuery(r, userID, nil))
}

func displayListQuery(r *http.Request, userID string, overrides map[string]string) url.Values {
	query := r.URL.Query()
	query.Del("source")
	if query.Get("userId") == "" {
		query.Set("userId", userID)
	}
	for key, value := range overrides {
		if strings.TrimSpace(value) == "" {
			query.Del(key)
			continue
		}
		query.Set(key, value)
	}
	return query
}

func firstQueryValue(r *http.Request, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(r.URL.Query().Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func (h *DisplayListHandler) delegateMetadata(
	w http.ResponseWriter,
	r *http.Request,
	source string,
	handler func(http.ResponseWriter, *http.Request),
	query url.Values,
) {
	delegated := r.Clone(r.Context())
	delegatedURL := *r.URL
	delegatedURL.RawQuery = query.Encode()
	delegated.URL = &delegatedURL

	rec := httptest.NewRecorder()
	handler(rec, delegated)
	if rec.Code >= http.StatusBadRequest {
		for key, values := range rec.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
		return
	}

	var payload interface{}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		http.Error(w, "failed to decode display list source", http.StatusBadGateway)
		return
	}

	normalised := normaliseDisplayListPayload(source, payload)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(normalised)
}

func normaliseDisplayListPayload(source string, payload interface{}) map[string]interface{} {
	switch typed := payload.(type) {
	case []interface{}:
		return map[string]interface{}{
			"source": source,
			"items":  typed,
			"total":  len(typed),
		}
	case map[string]interface{}:
		typed["source"] = source
		if _, ok := typed["items"]; !ok {
			if movies, ok := typed["movies"]; ok {
				typed["items"] = movies
			}
		}
		if _, ok := typed["total"]; !ok {
			if items, ok := typed["items"].([]interface{}); ok {
				typed["total"] = len(items)
			}
		}
		return typed
	default:
		return map[string]interface{}{
			"source": source,
			"items":  []interface{}{},
			"total":  0,
		}
	}
}

func (h *DisplayListHandler) requireUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	userID := strings.TrimSpace(mux.Vars(r)["userID"])
	if userID == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return "", false
	}
	if h.Users != nil && !h.Users.Exists(userID) {
		http.Error(w, "user not found", http.StatusNotFound)
		return "", false
	}
	return userID, true
}

func (h *DisplayListHandler) enrich(userID string, items []models.WatchlistItem, r *http.Request) {
	if h.HistoryService != nil {
		wh, whErr := h.HistoryService.ListWatchHistory(userID)
		cw, _ := h.HistoryService.ListSeriesStates(userID)
		pp, _ := h.HistoryService.ListPlaybackProgress(userID)
		if whErr == nil {
			idx := buildWatchStateIndex(wh, cw, pp)
			enrichWatchlistItems(items, idx)
		}
	}

	enrichWatchlistRatings(r.Context(), items, h.MetadataService)
	enrichWatchlistArtwork(items, h.MetadataService)
	enrichDisplayListReleases(r, items, h.MetadataService)
}

func enrichDisplayListReleases(r *http.Request, items []models.WatchlistItem, meta metadataService) {
	if meta == nil || len(items) == 0 {
		return
	}

	queries := make([]models.BatchMovieReleasesQuery, 0)
	indexes := make([]int, 0)
	for i := range items {
		if strings.ToLower(strings.TrimSpace(items[i].MediaType)) != "movie" {
			continue
		}
		tmdbID, _ := watchlist.NumericIDs(items[i].ExternalIDs)
		imdbID := strings.TrimSpace(items[i].ExternalIDs["imdb"])
		if tmdbID <= 0 && imdbID == "" {
			continue
		}
		queries = append(queries, models.BatchMovieReleasesQuery{
			TitleID: items[i].ID,
			TMDBID:  tmdbID,
			IMDBID:  imdbID,
		})
		indexes = append(indexes, i)
	}
	if len(queries) == 0 {
		return
	}

	results := meta.BatchMovieReleases(r.Context(), queries)
	for i, result := range results {
		if i >= len(indexes) {
			break
		}
		idx := indexes[i]
		items[idx].Theatrical = result.Theatrical
		items[idx].HomeRelease = result.HomeRelease
	}
}
