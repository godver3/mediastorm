package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"novastream/config"
	"novastream/models"
	"novastream/services/customlists"
	"novastream/services/playback"
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
	HiddenItemsService hiddenItemsService
	PrequeueStore      persistentPrequeueStore
}

type persistentPrequeueStore interface {
	ListAll() []*playback.PrequeueEntry
}

type DisplayListResponse struct {
	Source          string      `json:"source"`
	ListID          string      `json:"listId,omitempty"`
	Items           interface{} `json:"items"`
	Total           int         `json:"total"`
	UnfilteredTotal int         `json:"unfilteredTotal,omitempty"`
	Genres          []string    `json:"genres,omitempty"`
	AlphabetBuckets []string    `json:"alphabetBuckets,omitempty"`
	MetadataPending bool        `json:"metadataPending,omitempty"`
}

const (
	maxDiscoveryListItems   = 500
	watchTMDBShelfURLPrefix = "mediastorm:tmdb:"
)

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

func (h *DisplayListHandler) SetHiddenItemsService(service hiddenItemsService) {
	h.HiddenItemsService = service
}

func (h *DisplayListHandler) SetPrequeueStore(store persistentPrequeueStore) {
	h.PrequeueStore = store
}

func (h *DisplayListHandler) Get(w http.ResponseWriter, r *http.Request) {
	view := r.URL.Query().Get("homeView")
	filter := view
	if view != "movies" && view != "shows" && config.IsHomeViewID(view) {
		userID, ok := h.requireUser(w, r)
		if !ok {
			return
		}
		filter = config.HomeViewFilter(h.homeViewSettings(userID).Views, view)
	}
	if homeViewMediaType(filter) != "" {
		h.getHomeView(w, r, filter)
		return
	}
	h.get(w, r)
}

func (h *DisplayListHandler) get(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	source := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source")))
	if source == "" {
		source = "watchlist"
	}
	// Use the existing remote-shelf transport so current Watch clients can
	// render personal list shelves without a frontend update. Resolve locally;
	// this sentinel must never reach an external MDBList provider.
	customListShelf := false
	listID := strings.TrimSpace(r.URL.Query().Get("listId"))
	if source == "mdblist" || source == "mdblist-url" || source == "mdblist-shelf" {
		if listURL := strings.TrimSpace(r.URL.Query().Get("url")); strings.HasPrefix(listURL, customListShelfURLPrefix) {
			source = "custom-list"
			listID = strings.TrimSpace(strings.TrimPrefix(listURL, customListShelfURLPrefix))
			customListShelf = true
		}
	}
	metadataSource := source != "watchlist" &&
		source != "custom-list" &&
		source != "custom_user_list" &&
		source != "custom-user-list" &&
		source != "continue-watching" &&
		source != "continue_watching" &&
		source != "permanent-prequeue"
	if metadataSource && h.MetadataHandler == nil {
		http.Error(w, "metadata source is unavailable", http.StatusServiceUnavailable)
		return
	}

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
	case "permanent-prequeue":
		if h.PrequeueStore == nil {
			http.Error(w, "permanent prequeue source is unavailable", http.StatusServiceUnavailable)
			return
		}
		items = permanentPrequeueWatchlistItems(h.PrequeueStore.ListAll(), userID)
	case "top-ten":
		h.delegateMetadata(w, r, source, h.MetadataHandler.TopTen, displayListQuery(r, userID, map[string]string{
			"type": firstQueryValue(r, "mediaType", "type"),
		}))
		return
	case "popular-on-server":
		h.delegateMetadata(w, r, source, h.MetadataHandler.PopularOnServer, displayListQuery(r, userID, nil))
		return
	case "recently-watched":
		h.delegateMetadata(w, r, source, h.MetadataHandler.RecentlyWatched, displayListQuery(r, userID, nil))
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
		if shelfID, ok := watchTMDBShelfID(r.URL.Query().Get("url")); ok {
			overrides, found := h.watchTMDBShelfOverrides(userID, shelfID, r.URL.Query().Get("homeView"))
			if !found {
				http.Error(w, "TMDB shelf is unavailable", http.StatusNotFound)
				return
			}
			source = "tmdb-list"
			h.delegateMetadata(w, r, source, h.MetadataHandler.TMDBList, displayListQuery(r, userID, overrides))
			return
		}
		source = "mdblist"
		h.delegateMetadata(w, r, source, h.MetadataHandler.CustomList, displayListQuery(r, userID, nil))
		return
	case "stremio", "stremio-catalog":
		source = "stremio"
		h.delegateMetadata(w, r, source, h.MetadataHandler.StremioList, displayListQuery(r, userID, nil))
		return
	case "tmdb-list":
		h.delegateMetadata(w, r, source, h.MetadataHandler.TMDBList, displayListQuery(r, userID, nil))
		return
	case "trakt-list":
		h.delegateMetadata(w, r, source, h.MetadataHandler.TraktList, displayListQuery(r, userID, nil))
		return
	case "publicmetadb-list":
		h.delegateMetadata(w, r, source, h.MetadataHandler.PublicMetaDBList, displayListQuery(r, userID, nil))
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

	if h.HiddenItemsService != nil {
		items = h.HiddenItemsService.FilterHiddenWatchlistItems(userID, items)
	}
	h.enrich(userID, items, r)
	unfilteredTotal := 0
	if customListShelf {
		unfilteredTotal = len(items)
	}
	// Personal watchlists retain explicitly saved titles regardless of release
	// status. Discovery and other lists still follow release visibility settings.
	if source != "watchlist" && h.MetadataHandler != nil {
		policy := resolveUnreleasedVisibilityPolicy(
			h.MetadataHandler.CfgManager,
			h.MetadataHandler.UserSettings,
			h.MetadataHandler.ClientSettings,
			userID,
			requestClientID(r),
			unreleasedVisibilityLists,
		)
		items = filterWatchlistItemsByUnreleasedVisibility(items, policy)
	}
	if customListShelf {
		if strings.EqualFold(r.URL.Query().Get("hideUnreleased"), "true") {
			items = filterWatchlistItemsByUnreleasedVisibility(items, unreleasedVisibilityPolicy{})
		}
		if strings.EqualFold(r.URL.Query().Get("hideWatched"), "true") {
			visible := make([]models.WatchlistItem, 0, len(items))
			for _, item := range items {
				if item.WatchState != "complete" {
					visible = append(visible, item)
				}
			}
			items = visible
		}
	}
	items, genres, alphabet := queryWatchlistItems(items, parseDisplayListQuery(r))
	total := len(items)
	limit, offset := parseLimitOffset(r)
	if offset >= len(items) {
		items = []models.WatchlistItem{}
	} else {
		if offset > 0 {
			items = items[offset:]
		}
		if limit > 0 && limit < len(items) {
			items = items[:limit]
		}
	}

	if items == nil {
		items = []models.WatchlistItem{}
	}
	logDisplayListWatchlistArtworkTrace(userID, source, items)
	if h.MetadataHandler != nil {
		items = availabilityWatchlist(h.MetadataHandler.SourceAvailability, userID, requestClientID(r), items)
	}
	responseItems := interface{}(items)
	metadataPending := false
	if source == "permanent-prequeue" || customListShelf {
		trendingItems := watchlistItemsToTrending(items)
		if customListShelf {
			deferArtwork := strings.EqualFold(r.URL.Query().Get("shelfPhase"), "cards") ||
				strings.EqualFold(r.URL.Query().Get("deferArtwork"), "true")
			metadataSvc := h.metadataForUser(userID)
			postersPending := enrichCustomListShelfTextPosters(r.Context(), trendingItems, metadataSvc, deferArtwork)
			logosPending := fillMissingTitleLogos(r.Context(), trendingItems, metadataSvc, deferArtwork)
			metadataPending = postersPending || logosPending
		}
		responseItems = trendingItems
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(DisplayListResponse{
		Source:          source,
		ListID:          listID,
		Items:           responseItems,
		Total:           total,
		UnfilteredTotal: unfilteredTotal,
		Genres:          genres,
		AlphabetBuckets: alphabet,
		MetadataPending: metadataPending,
	})
}

func permanentPrequeueWatchlistItems(entries []*playback.PrequeueEntry, userID string) []models.WatchlistItem {
	filtered := make([]*playback.PrequeueEntry, 0, len(entries))
	for _, entry := range entries {
		if entry == nil || !entry.Persistent || entry.UserID != userID {
			continue
		}
		filtered = append(filtered, entry)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})

	items := make([]models.WatchlistItem, 0, len(filtered))
	for _, entry := range filtered {
		items = append(items, models.WatchlistItem{
			ID:          entry.TitleID,
			MediaType:   entry.MediaType,
			Name:        entry.TitleName,
			Year:        entry.Year,
			AddedAt:     entry.CreatedAt,
			ExternalIDs: canonicalTitleExternalIDs(entry.TitleID),
		})
	}
	return items
}

func canonicalTitleExternalIDs(titleID string) map[string]string {
	parts := strings.Split(strings.TrimSpace(titleID), ":")
	ids := make(map[string]string)
	if len(parts) >= 2 {
		switch strings.ToLower(parts[0]) {
		case "tmdb":
			ids["tmdb"] = parts[len(parts)-1]
		case "tvdb":
			ids["tvdb"] = parts[len(parts)-1]
		case "imdb":
			ids["imdb"] = parts[len(parts)-1]
		}
	} else if strings.HasPrefix(strings.ToLower(titleID), "tt") {
		ids["imdb"] = titleID
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}

func watchlistItemsToTrending(items []models.WatchlistItem) []models.TrendingItem {
	result := make([]models.TrendingItem, 0, len(items))
	for i, item := range items {
		title := models.Title{
			SourceAvailability: item.SourceAvailability,
			CardSubtitle:       item.CardSubtitle,
			ForceTitleOverlay:  item.ForceTitleOverlay,
			ID:                 item.ID,
			Name:               item.Name,
			Overview:           item.Overview,
			Year:               item.Year,
			MediaType:          item.MediaType,
			Genres:             item.Genres,
			RuntimeMinutes:     item.RuntimeMinutes,
			WatchState:         item.WatchState,
			UnwatchedCount:     item.UnwatchedCount,
			Ratings:            item.Ratings,
			Status:             item.Status,
			LifecycleStatus:    item.LifecycleStatus,
			Theatrical:         item.Theatrical,
			HomeRelease:        item.HomeRelease,
			ReleaseDate:        item.ReleaseDate,
		}
		if value := item.ExternalIDs["tmdb"]; value != "" {
			title.TMDBID, _ = strconv.ParseInt(value, 10, 64)
		}
		if value := item.ExternalIDs["tvdb"]; value != "" {
			title.TVDBID, _ = strconv.ParseInt(value, 10, 64)
		}
		title.IMDBID = item.ExternalIDs["imdb"]
		if item.PosterURL != "" {
			title.Poster = &models.Image{URL: item.PosterURL, Type: "poster"}
		}
		if item.TextPosterURL != "" {
			title.TextPoster = &models.Image{URL: item.TextPosterURL, Type: "poster"}
		}
		if item.BackdropURL != "" {
			title.Backdrop = &models.Image{URL: item.BackdropURL, Type: "backdrop"}
		}
		if item.TextBackdropURL != "" {
			title.TextBackdrop = &models.Image{URL: item.TextBackdropURL, Type: "backdrop"}
		}
		if item.LogoURL != "" {
			title.Logo = &models.Image{URL: item.LogoURL, Type: "logo"}
		}
		for _, url := range item.BackdropURLs {
			if url != "" {
				title.Backdrops = append(title.Backdrops, models.Image{URL: url, Type: "backdrop"})
			}
		}
		result = append(result, models.TrendingItem{Rank: i + 1, Title: title})
	}
	return result
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
func watchTMDBShelfID(listURL string) (string, bool) {
	listURL = strings.TrimSpace(listURL)
	if !strings.HasPrefix(listURL, watchTMDBShelfURLPrefix) {
		return "", false
	}
	shelfID := strings.TrimSpace(strings.TrimPrefix(listURL, watchTMDBShelfURLPrefix))
	return shelfID, shelfID != ""
}

func (h *DisplayListHandler) watchTMDBShelfOverrides(userID, shelfID string, views ...string) (map[string]string, bool) {
	if h.MetadataHandler == nil {
		return nil, false
	}
	view := ""
	if len(views) > 0 {
		view = views[0]
	}
	home := h.homeViewSettings(userID)
	home = models.ResolveHomeView(home, view)
	for _, shelf := range home.Shelves {
		if shelf.ID == shelfID && strings.EqualFold(strings.TrimSpace(shelf.Type), "tmdb") {
			return tmdbShelfQueryOverrides(shelf.TMDBSourceType, shelf.TMDBSourceID, shelf.TMDBMediaType, shelf.Sort, shelf.TMDBDiscoverQuery), true
		}
	}
	return nil, false
}

func tmdbShelfQueryOverrides(sourceType, sourceID, mediaType, sortBy, discoverQuery string) map[string]string {
	return map[string]string{
		"url":           "",
		"sourceType":    sourceType,
		"sourceId":      sourceID,
		"mediaType":     mediaType,
		"sort":          sortBy,
		"discoverQuery": discoverQuery,
	}
}

func (h *DisplayListHandler) delegateMetadata(
	w http.ResponseWriter,
	r *http.Request,
	source string,
	handler func(http.ResponseWriter, *http.Request),
	query url.Values,
) {
	query = cappedDisplayListQuery(query)
	filters := &progressiveShelfFilters{}
	ctx := r.Context()
	if progressiveShelfRequest(r) && progressiveShelfSource(source) {
		filters.hidden = h.HiddenItemsService
		if filters.hidden != nil {
			items, err := filters.hidden.List(query.Get("userId"))
			filters.hasHidden = err != nil || len(items) > 0
		}
		ctx = context.WithValue(ctx, progressiveShelfFiltersKey{}, filters)
	}
	delegated := r.Clone(ctx)
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
	if h.HiddenItemsService != nil && !filters.applied {
		normalised = h.filterHiddenPayload(query.Get("userId"), normalised)
	}
	capDisplayListPayload(normalised, query)
	logDisplayListPayloadArtworkTrace(query.Get("userId"), source, normalised)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(normalised)
}

func cappedDisplayListQuery(query url.Values) url.Values {
	capped := make(url.Values, len(query))
	for key, values := range query {
		capped[key] = append([]string(nil), values...)
	}
	offset := 0
	if parsed, err := strconv.Atoi(capped.Get("offset")); err == nil && parsed > 0 {
		offset = parsed
	}
	remaining := maxDiscoveryListItems - offset
	if remaining < 0 {
		remaining = 0
	}
	limit, err := strconv.Atoi(capped.Get("limit"))
	if err != nil || limit <= 0 || limit > remaining {
		forwardedLimit := remaining
		if forwardedLimit == 0 {
			// Metadata handlers treat zero as unlimited. Ask for the smallest
			// possible page and discard it below when the cap is exhausted.
			forwardedLimit = 1
		}
		capped.Set("limit", strconv.Itoa(forwardedLimit))
	}
	return capped
}

func capDisplayListPayload(payload map[string]interface{}, query url.Values) {
	offset := 0
	if parsed, err := strconv.Atoi(query.Get("offset")); err == nil && parsed > 0 {
		offset = parsed
	}
	remaining := maxDiscoveryListItems - offset
	if remaining < 0 {
		remaining = 0
	}

	for _, key := range []string{"items", "movies", "series"} {
		if items, ok := payload[key].([]interface{}); ok && len(items) > remaining {
			payload[key] = items[:remaining]
		}
	}
	for _, key := range []string{"total", "sourceTotal", "unfilteredTotal"} {
		switch value := payload[key].(type) {
		case float64:
			if value > maxDiscoveryListItems {
				payload[key] = float64(maxDiscoveryListItems)
			}
		case int:
			if value > maxDiscoveryListItems {
				payload[key] = maxDiscoveryListItems
			}
		}
	}
}

func (h *DisplayListHandler) filterHiddenPayload(userID string, payload map[string]interface{}) map[string]interface{} {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return payload
	}
	// Preserve the source's pre-pagination total before replacing `total` with
	// the visible count from this response page. Clients need this value to know
	// that another page exists even when the current page has hidden items (or
	// simply contains fewer records than the full source).
	if total, ok := payload["total"]; ok {
		payload["sourceTotal"] = total
	}
	for _, key := range []string{"items", "movies", "series"} {
		rawItems, ok := payload[key].([]interface{})
		if !ok {
			continue
		}
		filtered := rawItems[:0]
		for _, raw := range rawItems {
			itemMap, ok := raw.(map[string]interface{})
			if !ok {
				filtered = append(filtered, raw)
				continue
			}
			titleMap, ok := itemMap["title"].(map[string]interface{})
			if !ok {
				titleMap = itemMap
			}
			if h.HiddenItemsService.ShouldHideTitleMap(userID, titleMap) {
				continue
			}
			filtered = append(filtered, raw)
		}
		payload[key] = filtered
		if key == "items" {
			payload["total"] = len(filtered)
		}
	}
	return payload
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

func (h *DisplayListHandler) metadataForUser(userID string) metadataService {
	metadataSvc := h.MetadataService
	if h.MetadataHandler != nil {
		metadataSvc = h.MetadataHandler.serviceForUser(userID)
	}
	return metadataSvc
}

func (h *DisplayListHandler) enrich(userID string, items []models.WatchlistItem, r *http.Request) {
	metadataSvc := h.metadataForUser(userID)

	if h.HistoryService != nil {
		wh, whErr := h.HistoryService.ListWatchHistory(userID)
		cw, _ := h.HistoryService.ListSeriesStates(userID)
		pp, _ := h.HistoryService.ListPlaybackProgress(userID)
		if whErr == nil {
			idx := buildWatchStateIndex(wh, cw, pp)
			warmEpisodeCounts := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("includeUnwatchedCounts")), "true")
			enrichWatchlistItems(items, idx, metadataSvc, warmEpisodeCounts)
		}
	}

	enrichWatchlistRatings(r.Context(), items, metadataSvc)
	enrichWatchlistArtwork(items, metadataSvc)
	enrichDisplayListReleases(r, items, metadataSvc)
}

func enrichDisplayListReleases(r *http.Request, items []models.WatchlistItem, meta metadataService) {
	if meta == nil || len(items) == 0 {
		return
	}

	enrichDisplayListSeriesReleases(items, meta)

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
		items[idx].Status = result.Status
		items[idx].Theatrical = result.Theatrical
		items[idx].HomeRelease = result.HomeRelease
		if items[idx].Status == "" {
			items[idx].Status = models.MovieReleaseStatusFromWindows(items[idx].Theatrical, items[idx].HomeRelease)
		}
	}
}

// cachedSeriesReleaseProvider is the optional cache-only series release lookup.
type cachedSeriesReleaseProvider interface {
	GetCachedSeriesRelease(tmdbID int64, tvdbID int64) (status string, releaseDate string)
}

// enrichDisplayListSeriesReleases refreshes series release status and premiere
// date from cached metadata: saved items keep the status from when they were
// added (often none), so an unreleased show would otherwise carry no badge.
func enrichDisplayListSeriesReleases(items []models.WatchlistItem, meta metadataService) {
	cached, ok := meta.(cachedSeriesReleaseProvider)
	if !ok {
		return
	}
	for i := range items {
		switch strings.ToLower(strings.TrimSpace(items[i].MediaType)) {
		case "series", "tv", "show":
		default:
			continue
		}
		tmdbID, tvdbID := watchlist.NumericIDs(items[i].ExternalIDs)
		if tmdbID <= 0 && tvdbID <= 0 {
			continue
		}
		status, releaseDate := cached.GetCachedSeriesRelease(tmdbID, tvdbID)
		if status != "" {
			items[i].Status = status
		}
		if releaseDate != "" {
			items[i].ReleaseDate = releaseDate
		}
	}
}

func logDisplayListWatchlistArtworkTrace(userID, source string, items []models.WatchlistItem) {
	if source != "watchlist" && source != "custom-list" {
		return
	}
	for _, item := range items {
		name := strings.ToLower(strings.TrimSpace(item.Name))
		isTarget := strings.Contains(name, "bang my box") || strings.Contains(name, "robyn bird") || strings.Contains(name, "robin byrd")
		hasAnyArtwork := item.PosterURL != "" || item.TextPosterURL != "" || item.BackdropURL != "" ||
			item.TextBackdropURL != "" || len(item.BackdropURLs) > 0
		if !isTarget && hasAnyArtwork {
			continue
		}
		log.Printf(
			"[display-list][artwork] user=%s id=%s mediaType=%s name=%q year=%d target=%t poster=%t textPoster=%t backdrop=%t textBackdrop=%t backdropCount=%d externalIds=%v",
			userID,
			item.ID,
			item.MediaType,
			item.Name,
			item.Year,
			isTarget,
			item.PosterURL != "",
			item.TextPosterURL != "",
			item.BackdropURL != "",
			item.TextBackdropURL != "",
			len(item.BackdropURLs),
			item.ExternalIDs,
		)
	}
}

func logDisplayListPayloadArtworkTrace(userID, source string, payload map[string]interface{}) {
	rawItems, ok := payload["items"].([]interface{})
	if !ok {
		return
	}
	for _, raw := range rawItems {
		itemMap, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		titleMap, ok := itemMap["title"].(map[string]interface{})
		if !ok {
			titleMap = itemMap
		}
		name := displayListString(titleMap["name"])
		if name == "" {
			name = displayListString(titleMap["title"])
		}
		normalizedName := strings.ToLower(strings.TrimSpace(name))
		isTarget := strings.Contains(normalizedName, "bang my box") || strings.Contains(normalizedName, "robyn bird") || strings.Contains(normalizedName, "robin byrd")
		if !isTarget {
			continue
		}
		posterURL := displayListNestedURL(titleMap["poster"])
		textPosterURL := displayListNestedURL(titleMap["textPoster"])
		backdropURL := displayListNestedURL(titleMap["backdrop"])
		textBackdropURL := displayListNestedURL(titleMap["textBackdrop"])
		log.Printf(
			"[display-list][artwork] user=%s source=%s id=%s mediaType=%s name=%q year=%v target=%t poster=%t textPoster=%t backdrop=%t textBackdrop=%t tmdbId=%v tvdbId=%v imdbId=%v",
			userID,
			source,
			displayListString(titleMap["id"]),
			displayListString(titleMap["mediaType"]),
			name,
			titleMap["year"],
			isTarget,
			posterURL != "",
			textPosterURL != "",
			backdropURL != "",
			textBackdropURL != "",
			titleMap["tmdbId"],
			titleMap["tvdbId"],
			titleMap["imdbId"],
		)
	}
}

func displayListString(value interface{}) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func displayListNestedURL(value interface{}) string {
	m, ok := value.(map[string]interface{})
	if !ok {
		return ""
	}
	return displayListString(m["url"])
}
