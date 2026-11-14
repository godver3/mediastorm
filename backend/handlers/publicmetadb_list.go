package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"novastream/models"
	metadatapkg "novastream/services/metadata"
	"novastream/services/publicmetadb"
)

// PublicMetaDBList serves a configured account's list as a MediaStorm display list.
func (h *MetadataHandler) PublicMetaDBList(w http.ResponseWriter, r *http.Request) {
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	accountID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	listID := strings.TrimSpace(r.URL.Query().Get("listId"))
	if userID == "" || accountID == "" || listID == "" {
		http.Error(w, "userId, accountId and listId required", 400)
		return
	}
	if h.UsersService == nil {
		http.Error(w, "Users unavailable", 500)
		return
	}
	user, ok := h.UsersService.Get(userID)
	if !ok {
		http.Error(w, "User not found", 404)
		return
	}
	settings, err := h.CfgManager.Load()
	if err != nil {
		http.Error(w, "Settings unavailable", 500)
		return
	}
	account := settings.PublicMetaDB.GetAccountByID(accountID)
	if account == nil {
		http.Error(w, "Account not found", 404)
		return
	}
	if account.OwnerAccountID != "" && account.OwnerAccountID != user.AccountID {
		isMaster := false
		if h.AccountsService != nil {
			if owner, ok := h.AccountsService.Get(user.AccountID); ok {
				isMaster = owner.IsMaster
			}
		}
		if !isMaster {
			http.Error(w, "Account unavailable", 403)
			return
		}
	}
	client := h.PublicMetaDBClient
	if client == nil {
		client = &publicmetadb.Client{}
	}
	lists, err := client.Lists(r.Context(), account.APIKey)
	if err != nil {
		http.Error(w, "PublicMetaDB lists unavailable", 502)
		return
	}
	listed := false
	for _, list := range lists {
		if list.ID == listID {
			listed = true
			break
		}
	}
	if !listed {
		http.Error(w, "List not found", 404)
		return
	}
	sourceItems, err := client.Items(r.Context(), account.APIKey, listID)
	if err != nil {
		http.Error(w, "PublicMetaDB list items unavailable", 502)
		return
	}
	curated := make([]metadatapkg.CuratedItem, 0, len(sourceItems))
	for _, item := range sourceItems {
		if item.TMDBID <= 0 {
			continue
		}
		mediaType := "movie"
		if item.MediaType == "tv" || item.MediaType == "show" || item.MediaType == "series" {
			mediaType = "series"
		}
		curated = append(curated, metadatapkg.CuratedItem{TMDBID: item.TMDBID, MediaType: mediaType})
	}
	label := strings.TrimSpace(r.URL.Query().Get("name"))
	if label == "" {
		label = "PublicMetaDB List"
	}
	if _, ok := h.serviceForUser(userID).(shelfCardsService); ok && progressiveShelfRequest(r) {
		limit, offset := parseLimitOffset(r)
		resp := h.buildShelfFromCurated(w, r, curated, label, userID, strings.EqualFold(r.URL.Query().Get("hideUnreleased"), "true"), strings.EqualFold(r.URL.Query().Get("hideWatched"), "true"), limit, offset)
		if resp != nil {
			h.encodeMetadata(w, r, resp)
		}
		return
	}
	items, err := getCuratedListForRequest(r, h.serviceForUser(userID), curated, label)
	if err != nil {
		http.Error(w, "List enrichment unavailable", 502)
		return
	}
	hideUnreleased := strings.EqualFold(r.URL.Query().Get("hideUnreleased"), "true")
	hideWatched := strings.EqualFold(r.URL.Query().Get("hideWatched"), "true")
	policy := resolveUnreleasedVisibilityPolicy(h.CfgManager, h.UserSettings, h.ClientSettings, userID, requestClientID(r), unreleasedVisibilityLists)
	if hideUnreleased {
		policy.IncludeMovies = false
		policy.IncludeShows = false
	}
	items = filterTrendingItemsByUnreleasedVisibility(items, policy)
	if hideWatched && h.HistoryService != nil {
		items = filterWatchedItems(items, userID, h.HistoryService)
	}
	items = h.filterTrendingByKids(r.Context(), userID, h.serviceForUser(userID), items)
	items, genres, alphabet := h.queryTrendingList(userID, h.serviceForUser(userID), items, parseDisplayListQuery(r))
	total := len(items)
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if offset > 0 && offset < len(items) {
		items = items[offset:]
	} else if offset >= len(items) {
		items = []models.TrendingItem{}
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	w.Header().Set("Content-Type", "application/json")
	_ = h.encodeMetadata(w, r, TraktShelfResponse{Items: items, Total: total, Genres: genres, AlphabetBuckets: alphabet})
}
