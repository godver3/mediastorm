package filter

import (
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/mozillazg/go-unidecode"
	"golang.org/x/text/unicode/norm"

	"novastream/internal/mediaresolve"
	"novastream/models"
	"novastream/utils/parsett"
	"novastream/utils/similarity"
)

const (
	// MinTitleSimilarity is the minimum similarity score (0.0-1.0) required
	// for a result's title to match the expected title (90%)
	MinTitleSimilarity = 0.90

	// MaxYearDifference is the maximum difference in years allowed for movies
	MaxYearDifference = 1
)

var (
	resolution2160Pattern  = regexp.MustCompile(`(?i)(^|[^a-z0-9])(?:2160[pi]?|4k|uhd)([^a-z0-9]|$)`)
	resolution1080Pattern  = regexp.MustCompile(`(?i)(^|[^a-z0-9])1080[pi]?([^a-z0-9]|$)`)
	resolution720Pattern   = regexp.MustCompile(`(?i)(^|[^a-z0-9])720[pi]?([^a-z0-9]|$)`)
	resolution576Pattern   = regexp.MustCompile(`(?i)(^|[^a-z0-9])576[pi]?([^a-z0-9]|$)`)
	resolution480Pattern   = regexp.MustCompile(`(?i)(^|[^a-z0-9])480[pi]?([^a-z0-9]|$)`)
	formulaOneRoundPattern = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:f1|formula[.\s_-]*1)[^a-z0-9]+((?:19|20)\d{2})(?:x\d+)*(?:[^a-z0-9]+|x)(?:r|round)[.\s_-]*(\d{1,2})(?:[^a-z0-9]|$)`)
	formulaOneXPattern     = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:f1|formula[.\s_-]*1)[^a-z0-9]+((?:19|20)\d{2})x(\d{1,3})(?:[^a-z0-9]|$)`)
)

// HDRDVPolicy determines what HDR/DV content to exclude from search results.
type HDRDVPolicy string

const (
	// HDRDVPolicyNoExclusion excludes all HDR/DV content - only SDR allowed
	HDRDVPolicyNoExclusion HDRDVPolicy = "none"
	// HDRDVPolicyIncludeHDR allows HDR and DV profile 7/8 (DV profile 5 rejected at probe time)
	HDRDVPolicyIncludeHDR HDRDVPolicy = "hdr"
	// HDRDVPolicyIncludeHDRDV allows all content including all DV profiles - no filtering
	HDRDVPolicyIncludeHDRDV HDRDVPolicy = "hdr_dv"
)

// EpisodeCountResolver provides episode count information from metadata.
// This interface allows the filter to get accurate episode counts without
// directly depending on the metadata service.
type EpisodeCountResolver interface {
	// GetTotalSeriesEpisodes returns the total number of episodes in a series
	GetTotalSeriesEpisodes() int
	// GetEpisodesForSeasons returns the total episodes across the specified seasons
	GetEpisodesForSeasons(seasons []int) int
}

// SeriesEpisodeResolver is a concrete implementation of EpisodeCountResolver
// that uses pre-fetched series episode data.
type SeriesEpisodeResolver struct {
	TotalEpisodes       int         // Total episodes across all seasons
	SeasonEpisodeCounts map[int]int // Map of season number -> episode count
}

// NewSeriesEpisodeResolver creates a resolver from season episode counts.
// seasonCounts is a map of season number to episode count.
func NewSeriesEpisodeResolver(seasonCounts map[int]int) *SeriesEpisodeResolver {
	total := 0
	for _, count := range seasonCounts {
		total += count
	}
	return &SeriesEpisodeResolver{
		TotalEpisodes:       total,
		SeasonEpisodeCounts: seasonCounts,
	}
}

func (r *SeriesEpisodeResolver) GetTotalSeriesEpisodes() int {
	if r == nil {
		return 0
	}
	return r.TotalEpisodes
}

func (r *SeriesEpisodeResolver) GetEpisodesForSeasons(seasons []int) int {
	if r == nil || r.SeasonEpisodeCounts == nil {
		return 0
	}
	total := 0
	for _, seasonNum := range seasons {
		if count, ok := r.SeasonEpisodeCounts[seasonNum]; ok {
			total += count
		}
	}
	return total
}

// Options contains the expected metadata for filtering results
type Options struct {
	ExpectedTitle       string
	ExpectedYear        int
	EpisodeAirYear      int         // Year the target episode aired (allows results tagged with this year)
	IsMovie             bool        // true for movies, false for TV shows
	MaxSizeMovieGB      float64     // Maximum size in GB for movies (0 = no limit)
	MaxSizeEpisodeGB    float64     // Maximum size in GB for episodes (0 = no limit)
	MaxResolution       string      // Maximum resolution (e.g., "720p", "1080p", "2160p", empty = no limit)
	HDRDVPolicy         HDRDVPolicy // HDR/DV inclusion policy
	AlternateTitles     []string
	RequiredTerms       []string             // Terms where at least one must match for a result to be kept
	FilterOutTerms      []string             // Terms to filter out from results (case-insensitive match in title)
	TotalSeriesEpisodes int                  // Deprecated: use EpisodeResolver instead
	EpisodeResolver     EpisodeCountResolver // Resolver for accurate episode counts from metadata
	// Target episode filtering (for TV shows)
	TargetSeason          int    // Target season number (e.g., 22 for S22E68)
	TargetEpisode         int    // Target episode number within season (e.g., 68 for S22E68)
	TargetAbsoluteEpisode int    // Target absolute episode number for anime (e.g., 1153 for One Piece)
	IsDaily               bool   // True for daily shows (talk shows, news) - filter by date
	TargetAirDate         string // For daily shows: air date in YYYY-MM-DD format
}

// filteredResult holds a result with its HDR status for sorting
type filteredResult struct {
	result     models.NZBResult
	hasHDR     bool
	hdrFormats []string
}

// resolutionToNumeric converts a resolution string to a numeric value for comparison.
// Higher values = higher resolution. Returns 0 for unknown resolutions.
func resolutionToNumeric(res string) int {
	res = strings.ToLower(res)
	switch res {
	case "480p":
		return 480
	case "720p":
		return 720
	case "1080p":
		return 1080
	case "2160p", "4k", "uhd":
		return 2160
	default:
		return 0
	}
}

// resolutionToString converts a numeric resolution back to a display string.
func resolutionToString(res int) string {
	switch res {
	case 480:
		return "480p"
	case 720:
		return "720p"
	case 1080:
		return "1080p"
	case 2160:
		return "2160p"
	default:
		return ""
	}
}

// extractResolutionFromTitle extracts resolution from the title using simple string matching.
// This is a fallback for when parsett doesn't detect resolution (e.g., underscore-separated titles).
func extractResolutionFromTitle(title string) int {
	// Check for 4K/UHD (highest priority)
	if resolution2160Pattern.MatchString(title) {
		return 2160
	}
	// Check for 1080p
	if resolution1080Pattern.MatchString(title) {
		return 1080
	}
	// Check for 720p
	if resolution720Pattern.MatchString(title) {
		return 720
	}
	// Check for 576p (PAL)
	if resolution576Pattern.MatchString(title) {
		return 576
	}
	// Check for 480p (NTSC)
	if resolution480Pattern.MatchString(title) {
		return 480
	}

	return 0
}

// FilteredResult holds a result with its pass/fail status and rejection reason.
type FilteredResult struct {
	Result       models.NZBResult
	Passed       bool
	RejectReason string
}

// Results filters NZB search results based on parsed title information
// For movies: filters by title similarity (90%+) and year (±1 year)
// For TV shows: filters by title similarity (90%+) only
func Results(results []models.NZBResult, opts Options) []models.NZBResult {
	detailed := ResultsWithDetails(results, opts)
	passed := make([]models.NZBResult, 0, len(detailed))
	for _, fr := range detailed {
		if fr.Passed {
			passed = append(passed, fr.Result)
		}
	}
	return passed
}

// ResultsWithDetails filters NZB search results and returns all results with pass/fail status
// and rejection reasons. This is the detailed version of Results() used by the search tester.
func ResultsWithDetails(results []models.NZBResult, opts Options) []FilteredResult {
	if len(results) == 0 {
		return nil
	}

	// Don't filter if we don't have an expected title
	if strings.TrimSpace(opts.ExpectedTitle) == "" {
		out := make([]FilteredResult, len(results))
		for i, r := range results {
			out[i] = FilteredResult{Result: r, Passed: true}
		}
		return out
	}

	mediaType := "series"
	if opts.IsMovie {
		mediaType = "movie"
	}

	log.Printf("[filter] Filtering %d results with expected title=%q, year=%d, mediaType=%s",
		len(results), opts.ExpectedTitle, opts.ExpectedYear, mediaType)

	// BATCH PARSING: Parse all titles in one Python subprocess call
	titles := make([]string, len(results))
	candidateTitles := normalizeCandidateTitles(opts.ExpectedTitle, opts.AlternateTitles)

	for i, result := range results {
		titles[i] = result.Title
	}

	parsedMap, err := parsett.ParseTitleBatch(titles)
	if err != nil {
		log.Printf("[filter] Batch parsing failed: %v - falling back to keeping all results", err)
		out := make([]FilteredResult, len(results))
		for i, r := range results {
			out[i] = FilteredResult{Result: r, Passed: true}
		}
		return out
	}

	detailed := make([]FilteredResult, 0, len(results))
	compiledRequiredTerms := CompileTerms(opts.RequiredTerms)
	compiledFilterOutTerms := CompileTerms(opts.FilterOutTerms)
	expectedFormulaOneTerms := formulaOneEventTerms(opts.ExpectedTitle)

	reject := func(result models.NZBResult, reason string) {
		detailed = append(detailed, FilteredResult{Result: result, Passed: false, RejectReason: reason})
	}

	for i, result := range results {
		if len(compiledRequiredTerms) > 0 && !MatchesAnyTerm(result.Title, compiledRequiredTerms) {
			reason := "missing required terms"
			log.Printf("[filter] Rejecting %q: %s", result.Title, reason)
			reject(result, reason)
			continue
		}

		// Check filter out terms first (before parsing)
		if matchedTerm := MatchedTerm(result.Title, compiledFilterOutTerms); matchedTerm != "" {
			reason := fmt.Sprintf("matches filter-out term '%s'", matchedTerm)
			log.Printf("[filter] Rejecting %q: %s", result.Title, reason)
			reject(result, reason)
			continue
		}

		// NOTE: Daily show date filtering is handled below alongside S##E## matching.
		// Some "daily" shows (like SNL) use standard S##E## naming, not dates.
		// We accept results with EITHER matching date OR matching S##E##.

		// Get the parsed result from the batch
		parsed := parsedMap[result.Title]
		if parsed == nil {
			log.Printf("[filter] Failed to parse title %q - keeping result", result.Title)
			// Keep results we can't parse to avoid false negatives
			detailed = append(detailed, FilteredResult{Result: result, Passed: true})
			continue
		}

		// Ensure attributes map is initialized early (needed for year match tagging)
		if result.Attributes == nil {
			result.Attributes = make(map[string]string)
		}

		// Log parsed info for first few results
		if i < 5 {
			log.Printf("[filter] Parsed result[%d]: Title=%q -> ParsedTitle=%q, Year=%d, Seasons=%v, Episodes=%v, Complete=%v",
				i, result.Title, parsed.Title, parsed.Year, parsed.Seasons, parsed.Episodes, parsed.Complete)
		}

		// Check title similarity
		titleSim, matchedTitle := bestTitleSimilarity(candidateTitles, parsed.Title)
		if i < 5 {
			ref := opts.ExpectedTitle
			if matchedTitle != "" {
				ref = matchedTitle
			}
			log.Printf("[filter] Title similarity: %q vs %q = %.2f%%",
				ref, parsed.Title, titleSim*100)
		}

		if titleSim < MinTitleSimilarity {
			reason := fmt.Sprintf("title similarity %.0f%% < %.0f%% (parsed: '%s')", titleSim*100, MinTitleSimilarity*100, parsed.Title)
			log.Printf("[filter] Rejecting %q: %s", result.Title, reason)
			reject(result, reason)
			continue
		}
		if len(expectedFormulaOneTerms) > 0 && !formulaOneEventTermsMatch(result.Title, expectedFormulaOneTerms) {
			reason := fmt.Sprintf("missing Formula 1 event terms %v", expectedFormulaOneTerms)
			log.Printf("[filter] Rejecting %q: %s", result.Title, reason)
			reject(result, reason)
			continue
		}

		// Filter by media type using season/episode/volume detection
		// TV shows have seasons/episodes/volumes or are marked as complete packs, movies don't
		// Volumes are common in anime DVD/BD releases (e.g., "Vol 01", "Vol.1-6")
		hasTVPattern := len(parsed.Seasons) > 0 || len(parsed.Episodes) > 0 || len(parsed.Volumes) > 0
		isCompletePack := parsed.Complete
		hasEpisodeResolver := opts.EpisodeResolver != nil

		if opts.IsMovie && hasTVPattern {
			reason := fmt.Sprintf("movie result has TV pattern (S%v E%v)", parsed.Seasons, parsed.Episodes)
			log.Printf("[filter] Rejecting %q: searching for movie but result has TV pattern (seasons=%v, episodes=%v, volumes=%v)",
				result.Title, parsed.Seasons, parsed.Episodes, parsed.Volumes)
			reject(result, reason)
			continue
		}

		// For daily shows, date-based results are valid even without S##E## pattern
		hasDailyDate := opts.IsDaily && opts.TargetAirDate != "" && mediaresolve.CandidateMatchesDailyDate(result.Title, opts.TargetAirDate, 0)

		formulaOneEventYear, formulaOneEventNumbers, hasFormulaOneEventInfo := parseFormulaOneEvents(result.Title)
		hasFormulaOneEvent := !opts.IsMovie && hasFormulaOneEventInfo && formulaOneEventYear == opts.TargetSeason && intSliceContains(formulaOneEventNumbers, opts.TargetEpisode)

		if !opts.IsMovie && hasFormulaOneEventInfo && formulaOneEventYear == opts.TargetSeason && opts.TargetEpisode > 0 && !intSliceContains(formulaOneEventNumbers, opts.TargetEpisode) {
			reason := fmt.Sprintf("Formula 1 event %v does not match target S%04dE%02d", formulaOneEventNumbers, opts.TargetSeason, opts.TargetEpisode)
			log.Printf("[filter] Rejecting %q: %s", result.Title, reason)
			reject(result, reason)
			continue
		}

		if !opts.IsMovie && !hasTVPattern && !isCompletePack && !hasEpisodeResolver && !hasDailyDate && !hasFormulaOneEvent {
			reason := "no season/episode info for TV show"
			log.Printf("[filter] Rejecting %q: searching for TV show but result has no season/episode info",
				result.Title)
			reject(result, reason)
			continue
		}

		// Target episode filtering for TV shows
		// This rejects season packs and episodes that obviously can't contain the target episode
		// Skip this check for daily shows with matching dates - they use date-based matching instead
		if !opts.IsMovie && (opts.TargetSeason > 0 || opts.TargetEpisode > 0 || opts.TargetAbsoluteEpisode > 0) && !hasDailyDate && !hasFormulaOneEvent {
			if rejected, reason := shouldRejectByTargetEpisode(parsed, opts); rejected {
				log.Printf("[filter] Rejecting %q: %s", result.Title, reason)
				reject(result, reason)
				continue
			}
		}

		// Check year for all media types (movies and series)
		if opts.ExpectedYear > 0 {
			if parsed.Year > 0 {
				yearDiff := abs(opts.ExpectedYear - parsed.Year)
				// Also accept if the parsed year matches the episode's air year (±1)
				// This handles shows where S02 airs years after the series premiere
				episodeYearMatch := opts.EpisodeAirYear > 0 && abs(opts.EpisodeAirYear-parsed.Year) <= MaxYearDifference
				formulaOneSeasonYearMatch := hasFormulaOneEvent && opts.TargetSeason > 1900 && parsed.Year == opts.TargetSeason
				if yearDiff > MaxYearDifference && !episodeYearMatch && !formulaOneSeasonYearMatch {
					reason := fmt.Sprintf("year difference %d > %d (expected: %d, got: %d)", yearDiff, MaxYearDifference, opts.ExpectedYear, parsed.Year)
					log.Printf("[filter] Rejecting %q: %s, episodeAirYear: %d",
						result.Title, reason, opts.EpisodeAirYear)
					reject(result, reason)
					continue
				}
				result.Attributes["yearMatch"] = "true"
				if episodeYearMatch && yearDiff > MaxYearDifference {
					log.Printf("[filter] Accepted %q: year %d matches episode air year %d (series year: %d)",
						result.Title, parsed.Year, opts.EpisodeAirYear, opts.ExpectedYear)
				}
			} else {
				// If we can't parse a year from the title, be lenient and keep it
				// but flag it so ranking can derank it below year-matched results
				log.Printf("[filter] Warning: could not parse year from title %q, keeping but deranked", result.Title)
				result.Attributes["yearMatch"] = "false"
			}
		}

		// Check size limits if configured
		if result.SizeBytes > 0 {
			sizeGB := float64(result.SizeBytes) / (1024 * 1024 * 1024)

			if opts.IsMovie && opts.MaxSizeMovieGB > 0 {
				if sizeGB > opts.MaxSizeMovieGB {
					reason := fmt.Sprintf("size %.1f GB > %.1f GB limit", sizeGB, opts.MaxSizeMovieGB)
					log.Printf("[filter] Rejecting %q: %s (movie)", result.Title, reason)
					reject(result, reason)
					continue
				}
			} else if !opts.IsMovie && opts.MaxSizeEpisodeGB > 0 {
				// For TV shows, check if this is a pack and calculate per-episode size
				// A pack is: complete flag OR has seasons but NO specific episodes OR has multiple episodes
				// (S01E01 has both seasons=[1] and episodes=[1], so it's NOT a pack)
				// Anime batches like "01-26" have episodes=[1,2,3...26] with no seasons
				isMultiEpisodePack := len(parsed.Episodes) > 1
				isPack := isCompletePack || (len(parsed.Seasons) > 0 && len(parsed.Episodes) == 0) || isMultiEpisodePack
				effectiveSizeGB := sizeGB

				// Stremio-based scrapers (Torrentio, Comet, AIOStreams) report per-file
				// size via fileIdx, so sizeBytes is already per-episode. Indexer-based
				// scrapers (Zilean, Jackett, Nyaa) report full pack size.
				_, hasFileIndex := result.Attributes["fileIndex"]
				isSingleEpisode := len(parsed.Episodes) == 1

				if isPack {
					var episodeCount int
					if isMultiEpisodePack {
						// Use the actual episode count from parsed title (e.g., anime "01-26" = 26 episodes)
						episodeCount = len(parsed.Episodes)
						log.Printf("[filter] Multi-episode pack detected from title: %d episodes", episodeCount)
					} else {
						// Get episode count from metadata or estimate for season packs
						episodeCount = getPackEpisodeCount(parsed.Seasons, isCompletePack, opts.EpisodeResolver, opts.TotalSeriesEpisodes)
					}
					if episodeCount > 0 {
						if hasFileIndex {
							// Scraper reported a specific file index — sizeBytes is already per-file
							result.EpisodeCount = episodeCount
							result.SizePerFile = true
							log.Printf("[filter] Pack with per-file size: %q - %.2f GB per file (scraper provides fileIndex), %d episodes",
								result.Title, sizeGB, episodeCount)
						} else {
							// Scraper reported total pack size — divide to get per-episode
							effectiveSizeGB = sizeGB / float64(episodeCount)
							result.EpisodeCount = episodeCount // Pass to frontend for display
							log.Printf("[filter] Pack detected: %q - %.2f GB / %d episodes = %.2f GB per episode",
								result.Title, sizeGB, episodeCount, effectiveSizeGB)
						}
					} else {
						if hasFileIndex {
							// Per-file scraper, pack with unknown episode count — still per-file
							result.SizePerFile = true
							log.Printf("[filter] Pack with per-file size (unknown ep count): %q - %.2f GB per file",
								result.Title, sizeGB)
						} else {
							// Complete pack but no season info and no metadata - skip size filter
							log.Printf("[filter] Complete pack %q with unknown episode count - skipping size filter", result.Title)
							effectiveSizeGB = 0
						}
					}
				} else if hasFileIndex && !isSingleEpisode {
					// Stremio scraper with fileIndex but title didn't parse as a pack
					// (e.g., "Show + OVAs [BD]" with no S01 pattern). Since the title
					// doesn't identify a single episode, the size is likely per-file.
					result.SizePerFile = true
					log.Printf("[filter] Per-file size (ambiguous title): %q - %.2f GB per file (scraper provides fileIndex, no single episode in title)",
						result.Title, sizeGB)
				}

				if effectiveSizeGB > opts.MaxSizeEpisodeGB {
					reason := fmt.Sprintf("size %.1f GB > %.1f GB limit", effectiveSizeGB, opts.MaxSizeEpisodeGB)
					log.Printf("[filter] Rejecting %q: %s (episode)", result.Title, reason)
					reject(result, reason)
					continue
				}
			}
		}

		// Check resolution limits if configured
		if opts.MaxResolution != "" {
			maxRes := resolutionToNumeric(opts.MaxResolution)
			var parsedRes int
			var resSource string
			if parsed.Resolution != "" {
				parsedRes = resolutionToNumeric(parsed.Resolution)
				resSource = parsed.Resolution
			}
			// Fallback: extract resolution from title if parsett didn't detect it
			if parsedRes == 0 {
				parsedRes = extractResolutionFromTitle(result.Title)
				if parsedRes > 0 {
					resSource = resolutionToString(parsedRes)
				}
			}
			// Only filter if we can parse both resolutions
			if maxRes > 0 && parsedRes > 0 && parsedRes > maxRes {
				reason := fmt.Sprintf("resolution %s > %s limit", resSource, opts.MaxResolution)
				log.Printf("[filter] Rejecting %q: %s", result.Title, reason)
				reject(result, reason)
				continue
			}
		}

		// Check HDR/DV status
		hasHDR := len(parsed.HDR) > 0
		hasDV := hasDolbyVision(parsed.HDR)

		// Apply HDR/DV policy filtering
		// "none" = exclude all HDR/DV (only SDR allowed)
		// "hdr" = allow SDR + HDR + DV with HDR fallback (DV profile 5 exclusion happens at probe time)
		// "hdr_dv" = allow everything (no filtering)
		switch opts.HDRDVPolicy {
		case HDRDVPolicyNoExclusion:
			// Exclude all HDR/DV content - only allow SDR
			if hasHDR || hasDV {
				reason := "HDR/DV policy excludes HDR content"
				log.Printf("[filter] Rejecting %q: %s", result.Title, reason)
				reject(result, reason)
				continue
			}
		case HDRDVPolicyIncludeHDR:
			// Allow SDR, HDR, and DV with HDR fallback
			// DV profile 5 (no HDR fallback) detection requires ffprobe and happens during prequeue
			// Text-based filtering can't reliably detect DV profile, so we allow all DV here
			// and let the probe phase reject incompatible profiles
		case HDRDVPolicyIncludeHDRDV:
			// Allow everything - no HDR/DV filtering
		}

		// Store HDR info in attributes for downstream sorting
		if result.Attributes == nil {
			result.Attributes = make(map[string]string)
		}
		if hasHDR {
			result.Attributes["hdr"] = strings.Join(parsed.HDR, ",")
			if hasDolbyVision(parsed.HDR) {
				result.Attributes["hasDV"] = "true"
			}
		}

		// Use parsed resolution from parsett (more accurate than scraper detection)
		// This fixes issues like "wolfmax4k" provider name triggering false 4K detection
		if parsed.Resolution != "" {
			result.Attributes["resolution"] = parsed.Resolution
		}

		// Store additional parsed metadata for frontend badge display
		if parsed.Title != "" {
			result.Attributes["parsedTitle"] = parsed.Title
		}
		if parsed.Quality != "" {
			result.Attributes["quality"] = parsed.Quality
		}
		if parsed.Codec != "" {
			result.Attributes["codec"] = parsed.Codec
		}
		if len(parsed.Audio) > 0 {
			result.Attributes["audio"] = strings.Join(parsed.Audio, ",")
		}
		if len(parsed.Channels) > 0 {
			result.Attributes["channels"] = strings.Join(parsed.Channels, ",")
		}
		if parsed.BitDepth != "" {
			result.Attributes["bitDepth"] = parsed.BitDepth
		}
		if parsed.Group != "" {
			result.Attributes["group"] = parsed.Group
		}

		// Result passed all filters
		detailed = append(detailed, FilteredResult{
			Result: result,
			Passed: true,
		})
	}

	passedCount := 0
	for _, fr := range detailed {
		if fr.Passed {
			passedCount++
		}
	}
	log.Printf("[filter] Filtered %d -> %d results (removed %d)",
		len(results), passedCount, len(results)-passedCount)

	return detailed
}

// hasDolbyVision checks if the HDR formats include Dolby Vision
func hasDolbyVision(hdrFormats []string) bool {
	for _, format := range hdrFormats {
		lower := strings.ToLower(format)
		if lower == "dv" || lower == "dolby vision" || strings.Contains(lower, "dolby") {
			return true
		}
	}
	return false
}

// dvProfile78Regex matches DV profile 7 or 8 patterns (e.g., "DV P7", "DoVi P8", "Dolby Vision P07")
var dvProfile78Regex = regexp.MustCompile(`(?i)(dv|dovi|dolby\s*vision)\s*p?0?[78]`)

// hasDVProfile78 checks if the HDR formats include DV profile 7 or 8
// These profiles have an HDR10/HDR10+ fallback layer for non-DV displays
func hasDVProfile78(hdrFormats []string) bool {
	for _, format := range hdrFormats {
		if dvProfile78Regex.MatchString(format) {
			return true
		}
	}
	return false
}

// hasNonDVHDR checks if there's HDR content that isn't Dolby Vision
// (e.g., HDR10, HDR10+, HLG)
func hasNonDVHDR(hdrFormats []string) bool {
	for _, format := range hdrFormats {
		lower := strings.ToLower(format)
		// Skip DV formats
		if lower == "dv" || lower == "dolby vision" || strings.Contains(lower, "dolby") || strings.Contains(lower, "dovi") {
			continue
		}
		// Check for other HDR formats
		if strings.Contains(lower, "hdr") || lower == "hlg" {
			return true
		}
	}
	return false
}

// ShouldFilter determines if filtering should be applied based on the title
func ShouldFilter(title string) bool {
	return strings.TrimSpace(title) != ""
}

func normalizeCandidateTitles(primary string, alternates []string) []string {
	seen := make(map[string]struct{})
	var titles []string
	add := func(value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		lowered := strings.ToLower(trimmed)
		if _, exists := seen[lowered]; exists {
			return
		}
		seen[lowered] = struct{}{}
		titles = append(titles, trimmed)
	}
	addWithRomanization := func(value string) {
		add(value)
		if romanized := romanizeJapanese(value); romanized != "" {
			add(romanized)
		}
	}
	addFormulaOneAlias := func(value string) {
		normalized := normalizeFormulaOneTitle(value)
		switch normalized {
		case "formula 1":
			add("F1")
			add("Formula")
			add("Formula1")
		case "f1":
			add("Formula 1")
			add("Formula1")
		}
		if normalized != "formula 1" && normalized != "f1" && len(formulaOneEventTerms(value)) > 0 {
			add("Formula 1")
			add("Formula1")
			add("F1")
			add("Formula")
		}
	}
	addWithRomanization(primary)
	addFormulaOneAlias(primary)
	for _, alt := range alternates {
		addWithRomanization(alt)
		addFormulaOneAlias(alt)
	}
	return titles
}

func romanizeJapanese(value string) string {
	if !containsJapaneseRune(value) {
		return ""
	}
	romanized := strings.TrimSpace(unidecode.Unidecode(value))
	if romanized == "" {
		return ""
	}
	romanized = strings.Join(strings.Fields(romanized), " ")
	if romanized == "" {
		return ""
	}
	return romanized
}

func containsJapaneseRune(value string) bool {
	for _, r := range value {
		switch {
		case unicode.In(r, unicode.Hiragana, unicode.Katakana, unicode.Han):
			return true
		case r >= 0xFF66 && r <= 0xFF9D: // Half-width Katakana
			return true
		}
	}
	return false
}

func bestTitleSimilarity(candidates []string, parsedTitle string) (float64, string) {
	if len(candidates) == 0 {
		return 0.0, ""
	}
	var (
		bestScore     float64
		bestCandidate string
	)

	parsedTitles := parsedTitleVariants(parsedTitle)

	for _, candidate := range candidates {
		normalizedCandidate := normalizeForContainment(candidate)
		for _, parsedVariant := range parsedTitles {
			score := similarity.Similarity(candidate, parsedVariant)

			// Also check containment: if one title contains the other as a whole word/phrase,
			// consider it a high-confidence match. This handles cases like:
			// - "F1 The Movie" contains "F1" (TMDB original title)
			// - "The Matrix Reloaded" contains "Matrix Reloaded"
			normalizedParsed := normalizeForContainment(parsedVariant)
			if containmentScore := titleContainmentScore(normalizedParsed, normalizedCandidate); containmentScore > score {
				score = containmentScore
			}

			if score > bestScore {
				bestScore = score
				bestCandidate = candidate
			}
		}
	}
	return bestScore, bestCandidate
}

func parsedTitleVariants(title string) []string {
	seen := make(map[string]struct{})
	var variants []string
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		variants = append(variants, value)
	}

	add(title)
	normalized := normalizeFormulaOneTitle(title)
	switch normalized {
	case "formula 1":
		add("F1")
		add("Formula")
	case "f1":
		add("Formula 1")
		add("F1")
	}
	return variants
}

func formulaOneEventMatchesTarget(title string, targetSeason, targetEpisode int) bool {
	if targetSeason <= 0 || targetEpisode <= 0 {
		return false
	}
	year, eventNumbers, ok := parseFormulaOneEvents(title)
	return ok && year == targetSeason && intSliceContains(eventNumbers, targetEpisode)
}

func parseFormulaOneEvents(title string) (int, []int, bool) {
	xYear, xNumber, xRaw, hasX := parseFormulaOneEventPattern(formulaOneXPattern, title)
	roundYear, roundNumber, _, hasRound := parseFormulaOneEventPattern(formulaOneRoundPattern, title)

	if hasX {
		// Torrentio uses both "2026x14 R01" (TVDB episode 14, round 1)
		// and "2026x004 R01" (release sequence 004, round 1). Treat
		// three-digit x values as sequence numbers when a round is present.
		if len(xRaw) <= 2 || !hasRound || roundYear != xYear {
			return xYear, []int{xNumber}, true
		}
		return roundYear, []int{roundNumber}, true
	}

	if hasRound {
		return roundYear, []int{roundNumber}, true
	}

	return 0, nil, false
}

func parseFormulaOneEventPattern(pattern *regexp.Regexp, title string) (int, int, string, bool) {
	matches := pattern.FindStringSubmatch(title)
	if len(matches) != 3 {
		return 0, 0, "", false
	}
	year, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, 0, "", false
	}
	eventNumber, err := strconv.Atoi(matches[2])
	if err != nil {
		return 0, 0, "", false
	}
	return year, eventNumber, matches[2], true
}

func intSliceContains(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func normalizeFormulaOneTitle(title string) string {
	normalized := normalizeForContainment(title)
	fields := strings.Fields(normalized)
	for len(fields) > 0 {
		if _, err := strconv.Atoi(fields[0]); err == nil {
			fields = fields[1:]
			continue
		}
		break
	}
	if len(fields) == 1 && fields[0] == "f1" {
		return "f1"
	}
	if len(fields) == 1 && fields[0] == "formula1" {
		return "formula 1"
	}
	if len(fields) == 2 && fields[0] == "formula" && fields[1] == "1" {
		return "formula 1"
	}
	return normalized
}

func formulaOneTitleIdentity(normalizedTitle string) string {
	fields := strings.Fields(normalizedTitle)
	for i, field := range fields {
		if field == "f1" {
			return "f1"
		}
		if field == "formula1" {
			return "formula 1"
		}
		if field == "formula" && i+1 < len(fields) && fields[i+1] == "1" {
			return "formula 1"
		}
	}
	return ""
}

func formulaOneEventTerms(title string) []string {
	normalized := normalizeForContainment(title)
	if formulaOneTitleIdentity(normalized) == "" {
		return nil
	}
	terms := []string{}
	add := func(term string) {
		if strings.Contains(normalized, term) {
			terms = append(terms, term)
		}
	}
	add("bahrain")
	add("barcelona")
	add("pre season")
	add("testing")
	add("shakedown")
	add("qualifying")
	add("sprint")
	add("race")
	return terms
}

func formulaOneEventTermsMatch(title string, expectedTerms []string) bool {
	normalized := normalizeForContainment(title)
	for _, term := range expectedTerms {
		if !strings.Contains(normalized, term) {
			return false
		}
	}
	return true
}

// normalizeForContainment normalizes a title for containment comparison.
// Converts to lowercase, replaces separators with spaces, and collapses whitespace.
func normalizeForContainment(s string) string {
	s = strings.ToLower(s)
	// Replace common separators with spaces
	s = strings.ReplaceAll(s, ".", " ")
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, ":", " ")
	// Strip diacritical marks (é→e, ü→u, etc.)
	s = stripDiacritics(s)
	// Collapse multiple spaces and trim
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// stripDiacritics removes combining marks from a string after NFD decomposition,
// converting accented characters to their base form (e.g., "réseau" → "reseau").
func stripDiacritics(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFD.String(s) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// titleContainmentScore returns a similarity score based on containment between
// a parsed result title (title1) and a candidate/expected title (title2).
//
// Direction matters to avoid false positives from spinoff shows:
//   - candidate contains parsed (result is subset of expected): always valid
//     e.g., expected "The Matrix Reloaded" contains result "Matrix Reloaded"
//   - parsed contains candidate (result has MORE words than expected): only valid
//     if the candidate appears at the START of the parsed title (prefix match)
//     e.g., expected "F1" at start of result "F1 The Movie" → OK
//     e.g., expected "The First 48" NOT at start of "After the First 48" → rejected
func titleContainmentScore(parsedTitle, candidate string) float64 {
	// Require minimum length to avoid matching single characters
	if len(candidate) < 2 || len(parsedTitle) < 2 {
		return 0
	}

	// Case 1: candidate (expected) is longer or equal and contains the parsed title.
	// This means the result is a subset of the expected title. Only treat it as a
	// high-confidence match when the subset is substantial; otherwise a generic
	// trailing word can match a different show (e.g. "Ragnarok" vs "Record of Ragnarok").
	if len(candidate) >= len(parsedTitle) && strings.Contains(candidate, parsedTitle) {
		return candidateContainsParsedScore(candidate, parsedTitle)
	}

	// Case 2: parsed title (result) is longer and contains the candidate
	// Only valid if the candidate appears at the START (prefix match)
	// This prevents "After the First 48" from matching "The First 48"
	// but allows "F1 The Movie" to match "F1"
	if len(parsedTitle) > len(candidate) && strings.HasPrefix(parsedTitle, candidate) {
		// Verify word boundary at end of prefix
		endIdx := len(candidate)
		if endIdx == len(parsedTitle) || parsedTitle[endIdx] == ' ' {
			ratio := float64(len(candidate)) / float64(len(parsedTitle))
			if len(candidate) <= 3 && ratio < 0.2 {
				return 0.92
			}
			return 0.90 + (ratio * 0.10)
		}
	}

	return 0
}

func candidateContainsParsedScore(candidate, parsedTitle string) float64 {
	score := containmentScoreWithBoundaryCheck(candidate, parsedTitle)
	if score == 0 {
		return 0
	}
	if isSubstantialTitleSubset(candidate, parsedTitle) {
		return score
	}
	return 0
}

func isSubstantialTitleSubset(candidate, parsedTitle string) bool {
	if candidate == parsedTitle {
		return true
	}

	candidateWords := strings.Fields(candidate)
	parsedWords := strings.Fields(parsedTitle)
	if len(candidateWords) == 0 || len(parsedWords) == 0 {
		return false
	}

	// A single word that is only part of a longer expected title is too ambiguous.
	if len(parsedWords) == 1 && len(candidateWords) > 1 {
		return false
	}

	ratio := float64(len(parsedTitle)) / float64(len(candidate))
	return ratio >= 0.60
}

// containmentScoreWithBoundaryCheck checks word boundaries and returns a containment score.
func containmentScoreWithBoundaryCheck(longer, shorter string) float64 {
	idx := strings.Index(longer, shorter)
	if idx == -1 {
		return 0
	}

	// Check start boundary
	validStart := idx == 0 || longer[idx-1] == ' '
	// Check end boundary
	endIdx := idx + len(shorter)
	validEnd := endIdx == len(longer) || longer[endIdx] == ' '

	if !validStart || !validEnd {
		return 0
	}

	ratio := float64(len(shorter)) / float64(len(longer))

	if len(shorter) <= 3 && ratio < 0.2 {
		return 0.92
	}

	return 0.90 + (ratio * 0.10)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// getPackEpisodeCount gets the number of episodes in a pack using the resolver.
// Priority: 1) Use resolver for accurate count, 2) Use legacy totalSeriesEpisodes,
// 3) Estimate from seasons array, 4) Return 0 if no information available.
const defaultEpisodesPerSeason = 13 // Fallback estimate for most TV shows

func getPackEpisodeCount(seasons []int, isCompletePack bool, resolver EpisodeCountResolver, legacyTotal int) int {
	// Priority 1: Use resolver for accurate metadata-based counts
	if resolver != nil {
		if isCompletePack && len(seasons) == 0 {
			// Complete pack with no season info - get total series episodes
			if total := resolver.GetTotalSeriesEpisodes(); total > 0 {
				return total
			}
		} else if len(seasons) > 0 {
			// Pack with specific seasons - get episodes for those seasons
			if count := resolver.GetEpisodesForSeasons(seasons); count > 0 {
				return count
			}
		}
	}

	// Priority 2: Use legacy total if provided (backwards compatibility)
	if legacyTotal > 0 {
		return legacyTotal
	}

	// Priority 3: Estimate from seasons array
	if len(seasons) > 0 {
		return len(seasons) * defaultEpisodesPerSeason
	}

	// No information available - return 0 to signal unknown
	return 0
}

// Deprecated: use getPackEpisodeCount instead
func estimatePackEpisodeCount(seasons []int, totalSeriesEpisodes int) int {
	return getPackEpisodeCount(seasons, len(seasons) == 0, nil, totalSeriesEpisodes)
}

// shouldRejectByTargetEpisode checks if a result should be rejected based on target episode info.
// Returns (shouldReject, reason) where reason explains why the result was rejected.
func shouldRejectByTargetEpisode(parsed *parsett.ParsedTitle, opts Options) (bool, string) {
	if parsed == nil {
		return false, ""
	}
	hasTargetSeason := opts.TargetSeason > 0 || (opts.TargetSeason == 0 && opts.TargetEpisode > 0)

	// Case 1: Season pack(s) - reject if none of the seasons match the target
	// A season pack has seasons but no specific episodes
	isSeasonPack := len(parsed.Seasons) > 0 && len(parsed.Episodes) == 0

	if isSeasonPack && hasTargetSeason {
		// Check if target season is in the pack's seasons
		targetInPack := false
		for _, s := range parsed.Seasons {
			if s == opts.TargetSeason {
				targetInPack = true
				break
			}
		}
		if !targetInPack {
			return true, fmt.Sprintf("season pack contains seasons %v but target is S%02d", parsed.Seasons, opts.TargetSeason)
		}
	}

	// Case 2: Single episode or episode range - check season and episode match
	// This handles S01E01 style releases and fansub absolute releases
	hasEpisodes := len(parsed.Episodes) > 0
	hasSeason := len(parsed.Seasons) > 0

	if hasEpisodes {
		// Detect anime absolute format: either no season (fansub style) or S01E#### with high episode
		isAnimeAbsoluteFormat := false
		if !hasSeason {
			// Fansub style: "[SubsPlease] Anime - 1153 (1080p)" - no season, just episode
			isAnimeAbsoluteFormat = true
		} else if len(parsed.Seasons) == 1 && parsed.Seasons[0] == 1 {
			// S01E#### style - check if episode number suggests absolute (> typical season length)
			for _, ep := range parsed.Episodes {
				if ep > 100 {
					isAnimeAbsoluteFormat = true
					break
				}
			}
		}

		if isAnimeAbsoluteFormat && opts.TargetAbsoluteEpisode > 0 {
			// For anime absolute format, check if any episode matches the target absolute
			hasMatchingEpisode := false
			for _, ep := range parsed.Episodes {
				if ep == opts.TargetAbsoluteEpisode {
					hasMatchingEpisode = true
					break
				}
			}
			if !hasMatchingEpisode {
				// Check if it's a range that includes the target
				if len(parsed.Episodes) > 1 {
					minEp, maxEp := parsed.Episodes[0], parsed.Episodes[0]
					for _, ep := range parsed.Episodes {
						if ep < minEp {
							minEp = ep
						}
						if ep > maxEp {
							maxEp = ep
						}
					}
					if opts.TargetAbsoluteEpisode >= minEp && opts.TargetAbsoluteEpisode <= maxEp {
						hasMatchingEpisode = true
					}
				}
			}
			if !hasMatchingEpisode {
				return true, fmt.Sprintf("anime episode %v does not match target absolute episode %d", parsed.Episodes, opts.TargetAbsoluteEpisode)
			}
		} else if !isAnimeAbsoluteFormat && hasSeason && hasTargetSeason {
			// Standard seasonal release - check season then episode
			seasonMatch := false
			for _, s := range parsed.Seasons {
				if s == opts.TargetSeason {
					seasonMatch = true
					break
				}
			}
			if !seasonMatch {
				return true, fmt.Sprintf("episode is from season(s) %v but target is S%02d", parsed.Seasons, opts.TargetSeason)
			}
			// Also reject if the result has a specific episode that doesn't match the target.
			// Skip when parsett has double-parsed the season number as an episode
			// (e.g. "[SubsPlease] One Piece - Season 22 [1080p]" → Seasons=[22], Episodes=[22]).
			if opts.TargetEpisode > 0 {
				episodeIsSeason := len(parsed.Episodes) == 1 && len(parsed.Seasons) == 1 && parsed.Episodes[0] == parsed.Seasons[0]
				if !episodeIsSeason && !episodeMatchesTarget(parsed.Episodes, opts.TargetEpisode) {
					return true, fmt.Sprintf("episode %v does not match target S%02dE%02d", parsed.Episodes, opts.TargetSeason, opts.TargetEpisode)
				}
			}
		} else if isAnimeAbsoluteFormat && opts.TargetAbsoluteEpisode == 0 && opts.TargetEpisode > 0 && hasSeason {
			// High episode number on S01 but not an anime title (TargetAbsoluteEpisode not set).
			// e.g. Antigang S01E112 when we want S01E111 — still need season+episode checks.
			if opts.TargetSeason > 0 {
				seasonMatch := false
				for _, s := range parsed.Seasons {
					if s == opts.TargetSeason {
						seasonMatch = true
						break
					}
				}
				if !seasonMatch {
					return true, fmt.Sprintf("episode is from season(s) %v but target is S%02d", parsed.Seasons, opts.TargetSeason)
				}
			}
			if !episodeMatchesTarget(parsed.Episodes, opts.TargetEpisode) {
				return true, fmt.Sprintf("episode %v does not match target S%02dE%02d", parsed.Episodes, opts.TargetSeason, opts.TargetEpisode)
			}
		}
	}

	// Case 3: Absolute episode filtering for season packs
	// If we have a target absolute episode and an episode resolver, we can check
	// if a season pack could possibly contain the target absolute episode
	if opts.TargetAbsoluteEpisode > 0 && opts.EpisodeResolver != nil && isSeasonPack {
		// Calculate the absolute episode range for the seasons in this pack
		minAbsolute, maxAbsolute := getAbsoluteEpisodeRange(parsed.Seasons, opts.EpisodeResolver)

		if maxAbsolute > 0 && opts.TargetAbsoluteEpisode > maxAbsolute {
			return true, fmt.Sprintf("season pack (seasons %v) contains absolute episodes %d-%d but target is absolute %d",
				parsed.Seasons, minAbsolute, maxAbsolute, opts.TargetAbsoluteEpisode)
		}
		if minAbsolute > 0 && opts.TargetAbsoluteEpisode < minAbsolute {
			return true, fmt.Sprintf("season pack (seasons %v) contains absolute episodes %d-%d but target is absolute %d",
				parsed.Seasons, minAbsolute, maxAbsolute, opts.TargetAbsoluteEpisode)
		}
	}

	return false, ""
}

// episodeMatchesTarget checks whether the target episode is present in episodes
// (exact match) or falls within the range when there are multiple episodes (multi-ep file).
func episodeMatchesTarget(episodes []int, target int) bool {
	for _, ep := range episodes {
		if ep == target {
			return true
		}
	}
	if len(episodes) > 1 {
		minEp, maxEp := episodes[0], episodes[0]
		for _, ep := range episodes {
			if ep < minEp {
				minEp = ep
			}
			if ep > maxEp {
				maxEp = ep
			}
		}
		if target >= minEp && target <= maxEp {
			return true
		}
	}
	return false
}

// getAbsoluteEpisodeRange calculates the absolute episode range for given seasons.
// Returns (minAbsolute, maxAbsolute). Returns (0, 0) if calculation is not possible.
func getAbsoluteEpisodeRange(seasons []int, resolver EpisodeCountResolver) (int, int) {
	if resolver == nil || len(seasons) == 0 {
		return 0, 0
	}

	// We need the episode resolver to be a SeriesEpisodeResolver to access per-season counts
	ser, ok := resolver.(*SeriesEpisodeResolver)
	if !ok || ser == nil || ser.SeasonEpisodeCounts == nil {
		return 0, 0
	}

	// Find min and max season in the pack
	minSeason, maxSeason := seasons[0], seasons[0]
	for _, s := range seasons {
		if s < minSeason {
			minSeason = s
		}
		if s > maxSeason {
			maxSeason = s
		}
	}

	// Calculate absolute episode numbers
	// minAbsolute = sum of episodes in seasons 1 to (minSeason-1) + 1
	// maxAbsolute = sum of episodes in seasons 1 to maxSeason
	minAbsolute := 1
	for s := 1; s < minSeason; s++ {
		if count, ok := ser.SeasonEpisodeCounts[s]; ok {
			minAbsolute += count
		}
	}

	maxAbsolute := 0
	for s := 1; s <= maxSeason; s++ {
		if count, ok := ser.SeasonEpisodeCounts[s]; ok {
			maxAbsolute += count
		}
	}

	return minAbsolute, maxAbsolute
}
