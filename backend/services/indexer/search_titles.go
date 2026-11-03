package indexer

import (
	"context"
	"log"
	"strings"

	"novastream/internal/mediaidentity"
	"novastream/services/debrid"
	"novastream/utils/language"
)

// resolveSearchTitles resolves release titles, keeping the query budget separate
// from title identity. An alias outside the search budget can still identify a
// release returned by the canonical query or an IMDb-based scraper.
// maxAlternateTitleSearchesCap bounds the alternate titles searched per item
// whatever the setting says (0 used to mean unlimited). Each title is a
// separate query to every indexer, so 18 aliases across 9 indexers turned
// one item into ~160 indexer hits.
const maxAlternateTitleSearchesCap = 3

// effectiveMaxAlternateTitles applies maxAlternateTitleSearchesCap to the
// configured limit; 0 (unlimited) and larger values become the cap.
func effectiveMaxAlternateTitles(configured int) int {
	if configured <= 0 || configured > maxAlternateTitleSearchesCap {
		return maxAlternateTitleSearchesCap
	}
	return configured
}

func (s *Service) resolveSearchTitles(ctx context.Context, opts SearchOptions, metadataLanguage string, maxAlternates int) (searchTitles, filterTitles, requiredTitles []string) {
	maxAlternates = effectiveMaxAlternateTitles(maxAlternates)
	aliases := s.resolveAlternateTitles(ctx, opts, metadataLanguage, 0)
	// Hydrated aliases are plain names in provider order. Resolve the localized
	// main title through the language-scoped metadata cache instead of guessing
	// its language from that order (or from TMDB country codes).
	query := strings.TrimSpace(debrid.ParseQuery(opts.Query).Title)
	if query == "" {
		query = strings.TrimSpace(opts.Query)
	}
	if resolver, ok := s.metadata.(metadataLocalizedTitleResolver); ok && strings.TrimSpace(metadataLanguage) != "" {
		languages := []string{metadataLanguage}
		if !language.HasPreferredLanguage(metadataLanguage, "eng") {
			languages = append(languages, "eng")
		}
		for _, lang := range languages {
			title, err := resolver.ResolveSearchTitle(ctx, opts.MediaType, query, opts.Year, opts.IMDBID, lang)
			if err != nil {
				log.Printf("[indexer] release-title resolution failed query=%q language=%q err=%v", query, lang, err)
			}
			if title != nil {
				if name := strings.TrimSpace(title.Name); name != "" && !strings.EqualFold(name, query) {
					requiredTitles = append(requiredTitles, name)
				}
			}
		}
	}
	requiredTitles = combineFilterTitles(requiredTitles)
	filterTitles = combineFilterTitles(opts.AlternateTitles, requiredTitles, aliases)
	searchTitles = combineFilterTitles(requiredTitles, aliases)
	parsed := debrid.ParseQuery(opts.Query)
	// A parent anthology name must be searched with its mapped season, never
	// with the standalone catalog season. buildSearchQueries adds that request.
	searchTitles = mediaidentity.UnscopedReleaseTitles(searchTitles, query, opts.TitleID, parsed.Season, parsed.Episode, opts.Numbering)
	filterTitles = mediaidentity.UnscopedReleaseTitles(filterTitles, query, opts.TitleID, parsed.Season, parsed.Episode, opts.Numbering)
	requiredTitles = mediaidentity.UnscopedReleaseTitles(requiredTitles, query, opts.TitleID, parsed.Season, parsed.Episode, opts.Numbering)
	// English and the profile's metadata title always fit, even when the
	// configured alternate limit is smaller. Remaining slots go to aliases.
	budget := maxAlternates
	if budget > 0 {
		if budget < len(requiredTitles) {
			budget = len(requiredTitles)
		}
		if len(searchTitles) > budget {
			searchTitles = searchTitles[:budget]
		}
	}
	return searchTitles, filterTitles, requiredTitles
}
