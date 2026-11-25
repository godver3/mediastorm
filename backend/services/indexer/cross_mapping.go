package indexer

import (
	"strconv"
	"strings"

	"novastream/internal/mediaidentity"
	"novastream/services/debrid"
)

// Bind a mapped text query to the corresponding provider IDs without changing
// the catalog options used for filtering, display, history, or cache identity.
func mappedQueryOptions(opts SearchOptions, query string) SearchOptions {
	original := debrid.ParseQueryWithYear(opts.Query, opts.Year)
	requested := debrid.ParseQueryWithYear(query, opts.Year)
	out := opts
	out.Query = query
	if opts.Numbering != nil && strings.HasPrefix(opts.Numbering.SeriesID, "tvdb:series:") {
		if id, err := strconv.ParseInt(strings.TrimPrefix(opts.Numbering.SeriesID, "tvdb:series:"), 10, 64); err == nil && id > 0 {
			out.TVDBID = id
		}
	}
	if opts.Numbering != nil && (strings.HasPrefix(opts.Numbering.SeriesID, "tmdb:") || (opts.Numbering.Ordering != "" && opts.Numbering.Ordering != "official")) {
		out.TVDBID, out.IMDBID = 0, ""
	}

	for _, mapped := range mediaidentity.ReleaseEpisodeAliases(opts.TitleID, original.Season, original.Episode, opts.Numbering) {
		title := mapped.ReleaseTitle
		if title == "" {
			title = original.Title
		}
		if original.MediaType == debrid.MediaTypeSeries && strings.EqualFold(requested.Title, title) && requested.Season == mapped.Season && (requested.Episode == mapped.Episode || requested.Episode == 0) {
			if mapped.Source == "thexem" || (mapped.Numbering != nil && strings.HasPrefix(mapped.Numbering.SeriesID, "tmdb:")) {
				out.TVDBID, out.IMDBID = 0, ""
			}
			if mapped.IMDBID != "" {
				out.IMDBID = mapped.IMDBID
			}
			if mapped.TVDBID > 0 {
				out.TVDBID = mapped.TVDBID
			}
			if mapped.Year > 0 {
				out.Year = mapped.Year
			}
		}
	}

	return out
}

// Preserve both identities until the indexer applies its final ranking/limit.
func crossMappingSourceLimit(opts SearchOptions, limit int) int {
	parsed := debrid.ParseQueryWithYear(opts.Query, opts.Year)
	if parsed.MediaType == debrid.MediaTypeSeries {
		if len(mediaidentity.ReleaseEpisodeAliases(opts.TitleID, parsed.Season, parsed.Episode, opts.Numbering)) > 0 {
			return 0
		}
	}
	return limit
}
