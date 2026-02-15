package filter

import (
	"fmt"
	"log"
	"regexp"
	"strings"
	"unicode"

	"github.com/mozillazg/go-unidecode"

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
	TotalEpisodes     int         // Total episodes across all seasons
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
	IsMovie             bool        // true for movies, false for TV shows
	MaxSizeMovieGB      float64     // Maximum size in GB for movies (0 = no limit)
	MaxSizeEpisodeGB    float64     // Maximum size in GB for episodes (0 = no limit)
	MaxResolution       string      // Maximum resolution (e.g., "720p", "1080p", "2160p", empty = no limit)
	HDRDVPolicy         HDRDVPolicy // HDR/DV inclusion policy
	PrioritizeHdr       bool        // Prioritize HDR/DV content in results
	AlternateTitles     []string
	FilterOutTerms      []string               // Terms to filter out from results (case-insensitive match in title)
	TotalSeriesEpisodes int                    // Deprecated: use EpisodeResolver instead
	EpisodeResolver     EpisodeCountResolver   // Resolver for accurate episode counts from metadata
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
	title = strings.ToLower(title)

	// Check for 4K/UHD (highest priority)
	if strings.Contains(title, "2160p") || strings.Contains(title, "4k") || strings.Contains(title, "uhd") {
		return 2160
	}
	// Check for 1080p
	if strings.Contains(title, "1080p") || strings.Contains(title, "1080i") {
		return 1080
	}
	// Check for 720p
	if strings.Contains(title, "720p") || strings.Contains(title, "720i") {
		return 720
	}
	// Check for 576p (PAL)
	if strings.Contains(title, "576p") || strings.Contains(title, "576i") {
		return 576
	}
	// Check for 480p (NTSC)
	if strings.Contains(title, "480p") || strings.Contains(title, "480i") {
		return 480
	}

	return 0
}

// Results filters NZB search results based on parsed title information
// For movies: filters by title similarity (90%+) and year (±1 year)
// For TV shows: filters by title similarity (90%+) only
func Results(results []models.NZBResult, opts Options) []models.NZBResult {
	if len(results) == 0 {
		return results
	}

	// Don't filter if we don't have an expected title
	if strings.TrimSpace(opts.ExpectedTitle) == "" {
		return results
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
		return results
	}

	filtered := make([]filteredResult, 0, len(results))
	compiledFilterOutTerms := CompileTerms(opts.FilterOutTerms)

	for i, result := range results {
		// Check filter out terms first (before parsing)
		if MatchesAnyTerm(result.Title, compiledFilterOutTerms) {
			log.Printf("[filter] Rejecting %q: matches filter-out term", result.Title)
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
			filtered = append(filtered, filteredResult{result: result, hasHDR: false})
			continue
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
			log.Printf("[filter] Rejecting %q: title similarity %.2f%% < %.2f%% (parsed title: %q, best match: %q)",
				result.Title, titleSim*100, MinTitleSimilarity*100, parsed.Title, matchedTitle)
			continue
		}

		// Filter by media type using season/episode/volume detection
		// TV shows have seasons/episodes/volumes or are marked as complete packs, movies don't
		// Volumes are common in anime DVD/BD releases (e.g., "Vol 01", "Vol.1-6")
		hasTVPattern := len(parsed.Seasons) > 0 || len(parsed.Episodes) > 0 || len(parsed.Volumes) > 0
		isCompletePack := parsed.Complete
		hasEpisodeResolver := opts.EpisodeResolver != nil

		if opts.IsMovie && hasTVPattern {
			// Searching for a movie but result has TV show pattern (S01E01, volumes, etc)
			log.Printf("[filter] Rejecting %q: searching for movie but result has TV pattern (seasons=%v, episodes=%v, volumes=%v)",
				result.Title, parsed.Seasons, parsed.Episodes, parsed.Volumes)
			continue
		}

		// For daily shows, date-based results are valid even without S##E## pattern
		hasDailyDate := opts.IsDaily && opts.TargetAirDate != "" && mediaresolve.CandidateMatchesDailyDate(result.Title, opts.TargetAirDate, 0)

		if !opts.IsMovie && !hasTVPattern && !isCompletePack && !hasEpisodeResolver && !hasDailyDate {
			// Searching for a TV show but result has no TV indicators, isn't a complete pack,
			// we don't have an episode resolver to map files to episodes, and it's not a daily show with matching date
			log.Printf("[filter] Rejecting %q: searching for TV show but result has no season/episode info",
				result.Title)
			continue
		}

		// Target episode filtering for TV shows
		// This rejects season packs and episodes that obviously can't contain the target episode
		// Skip this check for daily shows with matching dates - they use date-based matching instead
		if !opts.IsMovie && (opts.TargetSeason > 0 || opts.TargetAbsoluteEpisode > 0) && !hasDailyDate {
			if rejected, reason := shouldRejectByTargetEpisode(parsed, opts); rejected {
				log.Printf("[filter] Rejecting %q: %s", result.Title, reason)
				continue
			}
		}

		// For movies, also check year
		if opts.IsMovie && opts.ExpectedYear > 0 {
			if parsed.Year > 0 {
				yearDiff := abs(opts.ExpectedYear - parsed.Year)
				if yearDiff > MaxYearDifference {
					log.Printf("[filter] Rejecting %q: year difference %d > %d (expected: %d, got: %d)",
						result.Title, yearDiff, MaxYearDifference, opts.ExpectedYear, parsed.Year)
					continue
				}
			} else {
				// If we can't parse a year from a movie title, be lenient and keep it
				// This handles edge cases where year isn't in the release name
				log.Printf("[filter] Warning: could not parse year from movie title %q, keeping anyway", result.Title)
			}
		}

		// Check size limits if configured
		if result.SizeBytes > 0 {
			sizeGB := float64(result.SizeBytes) / (1024 * 1024 * 1024)

			if opts.IsMovie && opts.MaxSizeMovieGB > 0 {
				if sizeGB > opts.MaxSizeMovieGB {
					log.Printf("[filter] Rejecting %q: size %.2f GB > %.2f GB limit (movie)",
						result.Title, sizeGB, opts.MaxSizeMovieGB)
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
						effectiveSizeGB = sizeGB / float64(episodeCount)
						result.EpisodeCount = episodeCount // Pass to frontend for display
						log.Printf("[filter] Pack detected: %q - %.2f GB / %d episodes = %.2f GB per episode",
							result.Title, sizeGB, episodeCount, effectiveSizeGB)
					} else {
						// Complete pack but no season info and no metadata - skip size filter
						log.Printf("[filter] Complete pack %q with unknown episode count - skipping size filter", result.Title)
						// Don't apply size filter for packs we can't estimate
						effectiveSizeGB = 0
					}
				}

				if effectiveSizeGB > opts.MaxSizeEpisodeGB {
					log.Printf("[filter] Rejecting %q: size %.2f GB > %.2f GB limit (episode)",
						result.Title, effectiveSizeGB, opts.MaxSizeEpisodeGB)
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
				log.Printf("[filter] Rejecting %q: resolution %s > %s limit",
					result.Title, resSource, opts.MaxResolution)
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
				log.Printf("[filter] Rejecting %q: policy excludes HDR/DV content", result.Title)
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

		// Result passed all filters
		filtered = append(filtered, filteredResult{
			result:     result,
			hasHDR:     hasHDR,
			hdrFormats: parsed.HDR,
		})
	}

	log.Printf("[filter] Filtered %d -> %d results (removed %d)",
		len(results), len(filtered), len(results)-len(filtered))

	// Note: HDR prioritization is now handled in the indexer service sorting
	// which considers resolution BEFORE HDR (so 2160p SDR ranks above 1080p HDR).
	// We still log that HDR info was processed for debugging.
	if opts.PrioritizeHdr {
		log.Printf("[filter] HDR attributes set on results (sorting handled by indexer)")
	}

	// Extract just the results for return
	finalResults := make([]models.NZBResult, len(filtered))
	for i, fr := range filtered {
		finalResults[i] = fr.result
	}

	return finalResults
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
	addWithRomanization(primary)
	for _, alt := range alternates {
		addWithRomanization(alt)
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

	// Normalize parsed title for containment checks
	normalizedParsed := normalizeForContainment(parsedTitle)

	for _, candidate := range candidates {
		score := similarity.Similarity(candidate, parsedTitle)

		// Also check containment: if one title contains the other as a whole word/phrase,
		// consider it a high-confidence match. This handles cases like:
		// - "F1 The Movie" contains "F1" (TMDB original title)
		// - "The Matrix Reloaded" contains "Matrix Reloaded"
		normalizedCandidate := normalizeForContainment(candidate)
		if containmentScore := titleContainmentScore(normalizedParsed, normalizedCandidate); containmentScore > score {
			score = containmentScore
		}

		if score > bestScore {
			bestScore = score
			bestCandidate = candidate
		}
	}
	return bestScore, bestCandidate
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
	// Collapse multiple spaces and trim
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// titleContainmentScore returns a similarity score if one title contains the other.
// Returns 0 if no containment is found or if the contained portion is too small.
// The contained title must be at least 2 characters and represent a word boundary match.
func titleContainmentScore(title1, title2 string) float64 {
	longer, shorter := title1, title2
	if len(title1) < len(title2) {
		longer, shorter = title2, title1
	}

	// Require minimum length to avoid matching single characters
	if len(shorter) < 2 {
		return 0
	}

	// Check if the shorter title is contained in the longer one
	if !strings.Contains(longer, shorter) {
		return 0
	}

	// Verify word boundary: the match should be at word boundaries
	// (start/end of string or adjacent to space)
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

	// Score based on how much of the longer title is matched
	// A higher ratio means a better match
	ratio := float64(len(shorter)) / float64(len(longer))

	// For very short matches (e.g., "F1" in "F1 The Movie"), require the short title
	// to be a significant starting portion or the ratio to be reasonable
	if len(shorter) <= 3 && ratio < 0.2 {
		// Short title like "F1" matching "F1 The Movie" - still valid if at word boundary
		// Give it a moderate score since it's a valid match but could be coincidental
		return 0.92
	}

	// Higher containment ratio = higher score
	// 20% containment -> 0.92, 50% -> 0.95, 80% -> 0.98
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

	// Case 1: Season pack(s) - reject if none of the seasons match the target
	// A season pack has seasons but no specific episodes
	isSeasonPack := len(parsed.Seasons) > 0 && len(parsed.Episodes) == 0

	if isSeasonPack && opts.TargetSeason > 0 {
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
		} else if !isAnimeAbsoluteFormat && hasSeason && opts.TargetSeason > 0 {
			// Standard seasonal release - check if target season is in the release
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
