package handlers

import (
	"novastream/models"
)

// customListPreQuery sorts and filters a remote list on the fields its source
// rows already carry, before pagination, so the metadata service enriches only
// the requested page instead of the whole list.
type customListPreQuery struct {
	query        displayListQueryOptions
	withAlphabet bool
	// alphabet holds the letter facet over the whole list (computed from the
	// rows the service passed in) when source names are display names.
	alphabet []string
}

func newCustomListPreQuery(query displayListQueryOptions, sourceNamesReliable bool) *customListPreQuery {
	return &customListPreQuery{query: query, withAlphabet: sourceNamesReliable}
}

// run is the metadata.CustomListOptions.PreQuery hook. It applies the same
// filter/sort contract as displayListQueryOptions.Apply and returns the indexes
// of the kept rows in display order.
func (p *customListPreQuery) run(rows []models.TrendingItem) []int {
	indexed := make([]models.TrendingItem, len(rows))
	copy(indexed, rows)
	for i := range indexed {
		// Rank is not part of the query contract; borrow it to carry the index.
		indexed[i].Rank = i
	}
	if p.withAlphabet {
		p.alphabet = displayListAlphabetBuckets(indexed, p.query)
	}
	kept := p.query.Apply(indexed)
	order := make([]int, len(kept))
	for i, item := range kept {
		order[i] = item.Rank
	}
	return order
}

// SourceQueryable reports whether every full-list need of the query can be met
// from a remote list's source rows (title, year, media type, source order):
// default/added/year/name sorts, media type, title search and alphabet. Genre
// and watch-status filters and rating/duration sorts need enriched metadata.
// Name-based needs also require that source titles are the display names.
func (q displayListQueryOptions) SourceQueryable(sourceNamesReliable bool) bool {
	if (q.WatchStatus != "" && q.WatchStatus != "all") || len(q.Genres) > 0 {
		return false
	}
	usesNames := q.Title != "" || q.Alphabet != ""
	switch q.SortBy {
	case "", "default", "added", "year":
	case "name":
		usesNames = true
	default:
		return false
	}
	return !usesNames || sourceNamesReliable
}

type sourceTitleLanguageService interface {
	SourceTitlesAreDisplayNames() bool
}

func sourceTitlesAreDisplayNames(service metadataService) bool {
	if svc, ok := service.(sourceTitleLanguageService); ok {
		return svc.SourceTitlesAreDisplayNames()
	}
	return false
}

// customListSourceQuerySupported reports whether the MDBList custom-list query
// can run on source rows. Kids profiles keep the full enriched path because the
// rating filter runs on enriched items before pagination there.
func (h *MetadataHandler) customListSourceQuerySupported(service metadataService, userID string, query displayListQueryOptions) bool {
	if !query.SourceQueryable(sourceTitlesAreDisplayNames(service)) {
		return false
	}
	if h.kidsFilterActive(userID) {
		return false
	}
	return true
}
