package indexer

import "context"

// resolveSearchTitles resolves metadata once, keeping the query budget separate
// from title identity. An alias outside the search budget can still identify a
// release returned by the canonical query or an IMDb-based scraper.
func (s *Service) resolveSearchTitles(ctx context.Context, opts SearchOptions, metadataLanguage string, maxAlternates int) (searchTitles, filterTitles, englishFallbackTitles []string) {
	aliases := s.resolveAlternateTitles(ctx, opts, metadataLanguage, 0)
	englishFallbackTitles = s.resolveEnglishFallbackTitles(ctx, opts, metadataLanguage)
	filterTitles = combineFilterTitles(opts.AlternateTitles, aliases, englishFallbackTitles)
	searchTitles = aliases
	if maxAlternates > 0 && len(searchTitles) > maxAlternates {
		searchTitles = searchTitles[:maxAlternates]
	}
	searchTitles = excludeFallbackTitles(searchTitles, englishFallbackTitles)
	return searchTitles, filterTitles, englishFallbackTitles
}
