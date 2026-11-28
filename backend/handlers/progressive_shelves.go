package handlers

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"novastream/models"
	"novastream/services/metadata"
)

type progressiveShelfFiltersKey struct{}
type progressiveShelfFilters struct {
	hidden    hiddenItemsService
	hasHidden bool
	applied   bool
}

type shelfCardsService interface {
	GetCustomListSource(context.Context, string) ([]metadata.CuratedItem, error)
	GetShelfCards(context.Context, []metadata.CuratedItem) ([]models.TrendingItem, error)
	FilterShelfVisibility(context.Context, []models.TrendingItem, bool, bool) []models.TrendingItem
}

func progressiveShelfRequest(r *http.Request) bool {
	phase := r.URL.Query().Get("shelfPhase")
	if phase != "cards" && phase != "complete" {
		return false
	}
	// Both phases serve any row size: cards read cheap shelf cards, and the
	// complete pass enriches only the page, reusing per-title results and
	// continuing detached when the client gives up.
	limit, _ := parseLimitOffset(r)
	if limit <= 0 {
		return false
	}
	query := parseDisplayListQuery(r)
	// Media type can be filtered using the source index. Other filters/sorts
	// need a full result and keep the established discovery path.
	query.MediaType = ""
	return !query.RequiresFullList()
}

func (h *MetadataHandler) progressiveShelfFiltered(r *http.Request, userID string, hideUnreleased, hideWatched bool) bool {
	policy := resolveUnreleasedVisibilityPolicy(h.CfgManager, h.UserSettings, h.ClientSettings, userID, requestClientID(r), unreleasedVisibilityLists)
	kids := h.kidsFilterActive(userID) && !isKidsAllowedListRequest(r.Context())
	hidden, _ := r.Context().Value(progressiveShelfFiltersKey{}).(*progressiveShelfFilters)
	mediaType := parseDisplayListQuery(r).MediaType
	return hideUnreleased || hideWatched || kids || !policy.IncludeMovies || !policy.IncludeShows || (hidden != nil && hidden.hasHidden) || (mediaType != "" && mediaType != "all")
}

// Read ahead only until a visible page exists. Exact filtered totals and rich
// metadata arrive in the background; all visibility checks precede publication.
func (h *MetadataHandler) progressiveCuratedShelf(r *http.Request, service metadataService, source []metadata.CuratedItem, label, userID string, hideUnreleased, hideWatched bool, limit, offset int) (*CustomListResponse, error) {
	svc := service.(shelfCardsService)
	query := parseDisplayListQuery(r)
	candidates := make([]metadata.CuratedItem, 0, len(source))
	for _, item := range source {
		mt := item.MediaType
		if mt == "show" || mt == "tv" {
			mt = "series"
		}
		if query.MediaType == "" || query.MediaType == "all" || query.MediaType == mt {
			candidates = append(candidates, item)
		}
	}
	policy := resolveUnreleasedVisibilityPolicy(h.CfgManager, h.UserSettings, h.ClientSettings, userID, requestClientID(r), unreleasedVisibilityLists)
	if hideUnreleased {
		policy.IncludeMovies = false
		policy.IncludeShows = false
	}
	kids := h.kidsFilterActive(userID) && !isKidsAllowedListRequest(r.Context())
	filters, _ := r.Context().Value(progressiveShelfFiltersKey{}).(*progressiveShelfFilters)
	if filters != nil {
		filters.applied = true
	}
	hasHidden := filters != nil && filters.hasHidden
	filtered := hideWatched || kids || !policy.IncludeMovies || !policy.IncludeShows || hasHidden
	complete := r.URL.Query().Get("shelfPhase") == "complete"
	accepted := make([]models.TrendingItem, 0, limit)
	selected := make([]metadata.CuratedItem, 0, limit)
	count, scanned := 0, 0
	var facetItems []models.TrendingItem
	batchSize := limit
	if batchSize < 20 {
		batchSize = 20
	}
	for start := 0; start < len(candidates); start += batchSize {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		end := start + batchSize
		if end > len(candidates) {
			end = len(candidates)
		}
		cards, err := svc.GetShelfCards(r.Context(), candidates[start:end])
		if err != nil {
			return nil, err
		}
		for i := range cards {
			cards[i].Rank = start + i
		}
		if !policy.IncludeMovies || !policy.IncludeShows {
			cards = svc.FilterShelfVisibility(r.Context(), cards, !policy.IncludeMovies, !policy.IncludeShows)
		}
		if hideWatched {
			cards = filterWatchedItems(cards, userID, h.HistoryService)
		}
		if hasHidden {
			visible := cards[:0]
			for _, card := range cards {
				title := card.Title
				ids := map[string]string{"imdb": title.IMDBID}
				if title.TMDBID > 0 {
					ids["tmdb"] = strconv.FormatInt(title.TMDBID, 10)
				}
				if title.TVDBID > 0 {
					ids["tvdb"] = strconv.FormatInt(title.TVDBID, 10)
				}
				if !filters.hidden.IsHidden(userID, title.MediaType, title.ID, ids) {
					visible = append(visible, card)
				}
			}
			cards = visible
		}
		cards = h.filterTrendingByKids(r.Context(), userID, service, cards)
		if query.IncludeFacets {
			facetItems = append(facetItems, cards...)
		}
		for _, card := range cards {
			if count >= offset && len(accepted) < limit {
				accepted = append(accepted, card)
				item := candidates[card.Rank]
				item.TMDBID = card.Title.TMDBID
				item.TVDBID = card.Title.TVDBID
				item.IMDBID = card.Title.IMDBID
				selected = append(selected, item)
			}
			count++
		}
		scanned = end
		if len(accepted) >= limit && (!complete || (!filtered && !query.IncludeFacets)) {
			break
		}
	}
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	totalPending := filtered && scanned < len(candidates)
	total := count
	if !filtered {
		total = len(candidates)
	}
	if totalPending && total <= offset+limit {
		total = offset + limit + 1
	} // unknown remainder; UI omits the count until complete
	if complete && len(selected) > 0 {
		var enriched []models.TrendingItem
		var err error
		if completer, ok := service.(interface {
			CompleteShelfCards(context.Context, []metadata.CuratedItem, string) ([]models.TrendingItem, error)
		}); ok {
			enriched, err = completer.CompleteShelfCards(r.Context(), selected, label)
		} else {
			enriched, err = service.GetCuratedList(r.Context(), selected, label)
		}
		if err != nil {
			return nil, err
		}
		if len(enriched) == len(accepted) {
			for i := range enriched {
				// Identity is fixed on first display so enrichment cannot reset TV focus.
				enriched[i].Title.ID = accepted[i].Title.ID
				enriched[i].Rank = accepted[i].Rank
				if enriched[i].Title.Poster == nil {
					enriched[i].Title.Poster = accepted[i].Title.Poster
				}
				if enriched[i].Title.Backdrop == nil {
					enriched[i].Title.Backdrop = accepted[i].Title.Backdrop
				}
			}
			accepted = enriched
		}
	}
	accepted, _, _ = h.queryTrendingList(userID, service, accepted, query)
	return &CustomListResponse{Items: accepted, Total: total, UnfilteredTotal: len(candidates), TotalPending: totalPending, MetadataPending: !complete && len(accepted) > 0, Genres: displayListGenres(facetItems), AlphabetBuckets: displayListAlphabetBuckets(facetItems, query)}, nil
}

func progressiveShelfSource(source string) bool {
	switch strings.ToLower(source) {
	case "mdblist", "trakt-list", "stremio", "simkl-list", "letterboxd-list", "publicmetadb-list", "tmdb-list":
		return true
	}
	return false
}
