package handlers

import (
	"net/http"
	"sort"
	"strings"

	"novastream/models"
)

func queryWatchlistItems(items []models.WatchlistItem, query displayListQueryOptions) ([]models.WatchlistItem, []string, []string) {
	genreItems := make([]models.TrendingItem, len(items))
	for i := range items {
		genreItems[i].Title.Genres = items[i].Genres
	}
	genres := displayListGenres(genreItems)
	alphabet := displayListWatchlistAlphabetBuckets(items, query)
	if !query.Active() {
		return items, genres, alphabet
	}
	result := make([]models.WatchlistItem, 0, len(items))
	for _, item := range items {
		if query.MediaType != "" && query.MediaType != "all" && item.MediaType != query.MediaType {
			continue
		}
		if query.Title != "" && !strings.Contains(strings.ToLower(item.Name), strings.ToLower(query.Title)) {
			continue
		}
		if query.WatchStatus != "" && query.WatchStatus != "all" && item.WatchState != query.WatchStatus {
			continue
		}
		if len(query.Genres) > 0 && !hasAnyDisplayListGenre(item.Genres, query.Genres) {
			continue
		}
		if query.Alphabet != "" && displayListAlphabetBucket(item.Name) != query.Alphabet {
			continue
		}
		result = append(result, item)
	}
	if query.SortBy == "" || query.SortBy == "default" {
		return result, genres, alphabet
	}
	direction := 1
	if query.SortDirection == "desc" {
		direction = -1
	}
	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i], result[j]
		cmp := 0
		switch query.SortBy {
		case "added":
			if a.AddedAt.Before(b.AddedAt) {
				cmp = -1
			} else if a.AddedAt.After(b.AddedAt) {
				cmp = 1
			}
		case "name":
			cmp = strings.Compare(sortableDisplayListName(a.Name), sortableDisplayListName(b.Name))
		case "year":
			cmp = compareDisplayListInt(a.Year, b.Year)
		case "duration":
			cmp = compareDisplayListInt(a.RuntimeMinutes, b.RuntimeMinutes)
		case "rating":
			ar, aok := watchlistRating(a, query.RatingSource)
			br, bok := watchlistRating(b, query.RatingSource)
			if aok != bok {
				return aok
			}
			if ar < br {
				cmp = -1
			} else if ar > br {
				cmp = 1
			}
		}
		return cmp*direction < 0
	})
	return result, genres, alphabet
}

func watchlistRating(item models.WatchlistItem, source string) (float64, bool) {
	for _, rating := range item.Ratings {
		if (source == "" || strings.EqualFold(rating.Source, source)) && rating.Max > 0 {
			return rating.Value / rating.Max, true
		}
	}
	return 0, false
}

type displayListQueryOptions struct {
	Title                  string
	MediaType              string
	WatchStatus            string
	Genres                 []string
	SortBy                 string
	SortDirection          string
	RatingSource           string
	IncludeFacets          bool
	IncludeUnwatchedCounts bool
	Alphabet               string
}

func parseDisplayListQuery(r *http.Request) displayListQueryOptions {
	q := r.URL.Query()
	genres := make([]string, 0)
	for _, genre := range strings.Split(q.Get("genres"), ",") {
		if genre = strings.TrimSpace(genre); genre != "" {
			genres = append(genres, genre)
		}
	}
	return displayListQueryOptions{
		Title:                  strings.TrimSpace(q.Get("titleFilter")),
		MediaType:              strings.ToLower(strings.TrimSpace(q.Get("filterMediaType"))),
		WatchStatus:            normalizeDisplayListWatchStatus(q.Get("watchStatus")),
		Genres:                 genres,
		SortBy:                 strings.ToLower(strings.TrimSpace(q.Get("sortBy"))),
		SortDirection:          strings.ToLower(strings.TrimSpace(q.Get("sortDirection"))),
		RatingSource:           strings.ToLower(strings.TrimSpace(q.Get("ratingSource"))),
		IncludeFacets:          strings.EqualFold(strings.TrimSpace(q.Get("includeFacets")), "true"),
		IncludeUnwatchedCounts: strings.EqualFold(strings.TrimSpace(q.Get("includeUnwatchedCounts")), "true"),
		Alphabet:               strings.ToUpper(strings.TrimSpace(q.Get("alphabet"))),
	}
}

// normalizeDisplayListWatchStatus maps the watch-status filter onto the
// canonical WatchState values (none, partial, complete). Descriptive aliases
// are accepted so clients that do not share the frontend's vocabulary (for
// example the native tvOS client) can use the same contract.
func normalizeDisplayListWatchStatus(value string) string {
	status := strings.ToLower(strings.TrimSpace(value))
	switch status {
	case "unwatched", "not_watched", "not-watched":
		return "none"
	case "in_progress", "in-progress", "inprogress", "watching", "started":
		return "partial"
	case "watched", "completed":
		return "complete"
	}
	return status
}

func (q displayListQueryOptions) RequiresIndex() bool {
	return q.Active() || q.IncludeFacets
}

// RequiresFullList reports whether filtering or sorting needs to see the whole
// list before pagination. Facets and per-item watch counts can be computed for
// the requested page without blocking the first page on every list item.
func (q displayListQueryOptions) RequiresFullList() bool {
	return q.Title != "" || (q.MediaType != "" && q.MediaType != "all") ||
		(q.WatchStatus != "" && q.WatchStatus != "all") || len(q.Genres) > 0 ||
		(q.SortBy != "" && q.SortBy != "default") || q.Alphabet != ""
}

func (q displayListQueryOptions) Active() bool {
	return q.RequiresFullList() || q.IncludeUnwatchedCounts
}

func (q displayListQueryOptions) Apply(items []models.TrendingItem) []models.TrendingItem {
	result := make([]models.TrendingItem, 0, len(items))
	for _, item := range items {
		title := item.Title
		if q.MediaType != "" && q.MediaType != "all" && title.MediaType != q.MediaType {
			continue
		}
		if q.Title != "" && !strings.Contains(strings.ToLower(title.Name), strings.ToLower(q.Title)) {
			continue
		}
		if q.WatchStatus != "" && q.WatchStatus != "all" && title.WatchState != q.WatchStatus {
			continue
		}
		if len(q.Genres) > 0 && !hasAnyDisplayListGenre(title.Genres, q.Genres) {
			continue
		}
		if q.Alphabet != "" && displayListAlphabetBucket(title.Name) != q.Alphabet {
			continue
		}
		result = append(result, item)
	}
	if q.SortBy == "" || q.SortBy == "default" {
		return result
	}
	direction := 1
	if q.SortDirection == "desc" {
		direction = -1
	}
	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i].Title, result[j].Title
		cmp := 0
		switch q.SortBy {
		case "added":
			// Remote lists do not expose an added timestamp; retain source order.
			cmp = 0
		case "name":
			cmp = strings.Compare(sortableDisplayListName(a.Name), sortableDisplayListName(b.Name))
		case "year":
			cmp = compareDisplayListInt(a.Year, b.Year)
		case "duration":
			cmp = compareDisplayListInt(a.RuntimeMinutes, b.RuntimeMinutes)
		case "rating":
			ar, aok := displayListRating(a, q.RatingSource)
			br, bok := displayListRating(b, q.RatingSource)
			if aok != bok {
				return aok
			}
			if ar < br {
				cmp = -1
			} else if ar > br {
				cmp = 1
			}
		default:
			cmp = 0
		}
		return cmp*direction < 0
	})
	return result
}

func paginateTrendingItems(items []models.TrendingItem, offset, limit int) []models.TrendingItem {
	if offset >= len(items) {
		return []models.TrendingItem{}
	}
	if offset > 0 {
		items = items[offset:]
	}
	if limit > 0 && limit < len(items) {
		items = items[:limit]
	}
	return items
}

func displayListGenres(items []models.TrendingItem) []string {
	set := make(map[string]string)
	for _, item := range items {
		for _, genre := range item.Title.Genres {
			trimmed := strings.TrimSpace(genre)
			if trimmed != "" {
				set[strings.ToLower(trimmed)] = trimmed
			}
		}
	}
	genres := make([]string, 0, len(set))
	for _, genre := range set {
		genres = append(genres, genre)
	}
	sort.Strings(genres)
	return genres
}

func displayListAlphabetBuckets(items []models.TrendingItem, query displayListQueryOptions) []string {
	withoutAlphabet := query
	withoutAlphabet.Alphabet = ""
	if withoutAlphabet.Active() {
		items = withoutAlphabet.Apply(items)
	}
	set := make(map[string]struct{})
	for _, item := range items {
		set[displayListAlphabetBucket(item.Title.Name)] = struct{}{}
	}
	return sortedDisplayListAlphabetBuckets(set)
}

func displayListWatchlistAlphabetBuckets(items []models.WatchlistItem, query displayListQueryOptions) []string {
	withoutAlphabet := query
	withoutAlphabet.Alphabet = ""
	filtered, _, _ := queryWatchlistItemsWithoutAlphabet(items, withoutAlphabet)
	set := make(map[string]struct{})
	for _, item := range filtered {
		set[displayListAlphabetBucket(item.Name)] = struct{}{}
	}
	return sortedDisplayListAlphabetBuckets(set)
}

func queryWatchlistItemsWithoutAlphabet(items []models.WatchlistItem, query displayListQueryOptions) ([]models.WatchlistItem, []string, []string) {
	query.Alphabet = ""
	// Avoid recursively computing buckets.
	result := make([]models.WatchlistItem, 0, len(items))
	for _, item := range items {
		if query.MediaType != "" && query.MediaType != "all" && item.MediaType != query.MediaType {
			continue
		}
		if query.Title != "" && !strings.Contains(strings.ToLower(item.Name), strings.ToLower(query.Title)) {
			continue
		}
		if query.WatchStatus != "" && query.WatchStatus != "all" && item.WatchState != query.WatchStatus {
			continue
		}
		if len(query.Genres) > 0 && !hasAnyDisplayListGenre(item.Genres, query.Genres) {
			continue
		}
		result = append(result, item)
	}
	return result, nil, nil
}

func displayListAlphabetBucket(name string) string {
	sortable := strings.ToUpper(sortableDisplayListName(name))
	if sortable == "" || sortable[0] < 'A' || sortable[0] > 'Z' {
		return "#"
	}
	return sortable[:1]
}

func sortedDisplayListAlphabetBuckets(set map[string]struct{}) []string {
	buckets := make([]string, 0, len(set))
	for bucket := range set {
		buckets = append(buckets, bucket)
	}
	sort.Slice(buckets, func(i, j int) bool {
		if buckets[i] == "#" {
			return true
		}
		if buckets[j] == "#" {
			return false
		}
		return buckets[i] < buckets[j]
	})
	return buckets
}

func hasAnyDisplayListGenre(actual, selected []string) bool {
	for _, a := range actual {
		for _, s := range selected {
			if strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(s)) {
				return true
			}
		}
	}
	return false
}

func sortableDisplayListName(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, article := range []string{"the ", "an ", "a "} {
		if strings.HasPrefix(lower, article) {
			return strings.TrimSpace(lower[len(article):])
		}
	}
	return lower
}

func displayListRating(title models.Title, source string) (float64, bool) {
	for _, rating := range title.Ratings {
		if (source == "" || strings.EqualFold(rating.Source, source)) && rating.Max > 0 {
			return rating.Value / rating.Max, true
		}
	}
	return 0, false
}

func compareDisplayListInt(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// queryTrendingList applies the shared shelf-page query contract to a complete
// source dataset. Callers remain responsible for visibility and kids rules.
func (h *MetadataHandler) queryTrendingList(
	userID string,
	service metadataService,
	items []models.TrendingItem,
	query displayListQueryOptions,
) ([]models.TrendingItem, []string, []string) {
	// Every page carries watch state, not only sorted or filtered ones: clients
	// draw watched / in-progress badges from it. Clients showing the unwatched
	// count ask for it, which warms series totals the cache is missing.
	if userID != "" && h.HistoryService != nil {
		wh, whErr := h.HistoryService.ListWatchHistory(userID)
		if whErr == nil {
			cw, _ := h.HistoryService.ListSeriesStates(userID)
			pp, _ := h.HistoryService.ListPlaybackProgress(userID)
			enrichTrendingItems(items, buildWatchStateIndex(wh, cw, pp), service, query.IncludeUnwatchedCounts)
		}
	}
	enrichTrendingRatings(items, service)
	genres := displayListGenres(items)
	alphabet := displayListAlphabetBuckets(items, query)
	if query.Active() {
		items = query.Apply(items)
	}
	return items, genres, alphabet
}
