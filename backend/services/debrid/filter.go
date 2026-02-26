package debrid

import (
	"novastream/models"
	"novastream/utils/filter"
)

// FilterOptions contains the expected metadata for filtering results
type FilterOptions struct {
	ExpectedTitle       string
	ExpectedYear        int
	MediaType           MediaType          // movie or series
	MaxSizeMovieGB      float64            // Maximum size in GB for movies (0 = no limit)
	MaxSizeEpisodeGB    float64            // Maximum size in GB for episodes (0 = no limit)
	MaxResolution       string             // Maximum resolution (e.g., "720p", "1080p", "2160p", empty = no limit)
	HDRDVPolicy         filter.HDRDVPolicy // HDR/DV inclusion policy
	PrioritizeHdr       bool               // Prioritize HDR/DV content in results
	AlternateTitles     []string
	FilterOutTerms      []string                    // Terms to filter out from results (case-insensitive match in title)
	TotalSeriesEpisodes int                         // Deprecated: use EpisodeResolver instead
	EpisodeResolver     filter.EpisodeCountResolver // Resolver for accurate episode counts from metadata
	// Target episode filtering (for TV shows)
	TargetSeason          int    // Target season number (e.g., 22 for S22E68)
	TargetEpisode         int    // Target episode number within season (e.g., 68 for S22E68)
	TargetAbsoluteEpisode int    // Target absolute episode number for anime (e.g., 1153 for One Piece)
	IsDaily               bool   // True for daily shows (talk shows, news) - filter by date
	TargetAirDate         string // For daily shows: air date in YYYY-MM-DD format
}

// FilterResults filters search results based on parsed title information
// For all media types: filters by title similarity (90%+) and year (±1 year) when expected year is set
func FilterResults(results []models.NZBResult, opts FilterOptions) []models.NZBResult {
	filterOpts := filter.Options{
		ExpectedTitle:         opts.ExpectedTitle,
		ExpectedYear:          opts.ExpectedYear,
		IsMovie:               opts.MediaType == MediaTypeMovie,
		MaxSizeMovieGB:        opts.MaxSizeMovieGB,
		MaxSizeEpisodeGB:      opts.MaxSizeEpisodeGB,
		MaxResolution:         opts.MaxResolution,
		HDRDVPolicy:           opts.HDRDVPolicy,
		PrioritizeHdr:         opts.PrioritizeHdr,
		AlternateTitles:       opts.AlternateTitles,
		FilterOutTerms:        opts.FilterOutTerms,
		TotalSeriesEpisodes:   opts.TotalSeriesEpisodes,
		EpisodeResolver:       opts.EpisodeResolver,
		TargetSeason:          opts.TargetSeason,
		TargetEpisode:         opts.TargetEpisode,
		TargetAbsoluteEpisode: opts.TargetAbsoluteEpisode,
		IsDaily:               opts.IsDaily,
		TargetAirDate:         opts.TargetAirDate,
	}
	return filter.Results(results, filterOpts)
}

// ShouldFilter determines if filtering should be applied based on the search context
func ShouldFilter(parsed ParsedQuery) bool {
	return filter.ShouldFilter(parsed.Title)
}
