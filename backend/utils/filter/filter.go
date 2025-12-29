package filter

import (
	"log"
	"strings"
	"unicode"

	"github.com/mozillazg/go-unidecode"

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

// Options contains the expected metadata for filtering results
type Options struct {
	ExpectedTitle    string
	ExpectedYear     int
	IsMovie          bool     // true for movies, false for TV shows
	MaxSizeMovieGB   float64  // Maximum size in GB for movies (0 = no limit)
	MaxSizeEpisodeGB float64  // Maximum size in GB for episodes (0 = no limit)
	MaxResolution    string   // Maximum resolution (e.g., "720p", "1080p", "2160p", empty = no limit)
	ExcludeHdr       bool     // Exclude HDR content from results
	PrioritizeHdr    bool     // Prioritize HDR/DV content in results (when not excluded)
	AlternateTitles  []string
	FilterOutTerms   []string // Terms to filter out from results (case-insensitive match in title)
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
	switch strings.ToLower(res) {
	case "480p":
		return 480
	case "720p":
		return 720
	case "1080p":
		return 1080
	case "2160p", "4k":
		return 2160
	default:
		return 0
	}
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

	for i, result := range results {
		// Check filter out terms first (before parsing)
		if len(opts.FilterOutTerms) > 0 {
			titleLower := strings.ToLower(result.Title)
			shouldFilter := false
			for _, term := range opts.FilterOutTerms {
				termLower := strings.ToLower(strings.TrimSpace(term))
				if termLower != "" && strings.Contains(titleLower, termLower) {
					log.Printf("[filter] Rejecting %q: contains filtered term %q", result.Title, term)
					shouldFilter = true
					break
				}
			}
			if shouldFilter {
				continue
			}
		}

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
			log.Printf("[filter] Parsed result[%d]: Title=%q -> ParsedTitle=%q, Year=%d, Seasons=%v, Episodes=%v",
				i, result.Title, parsed.Title, parsed.Year, parsed.Seasons, parsed.Episodes)
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

		// Filter by media type using season/episode detection
		// TV shows have seasons/episodes, movies don't
		hasTVPattern := len(parsed.Seasons) > 0 || len(parsed.Episodes) > 0

		if opts.IsMovie && hasTVPattern {
			// Searching for a movie but result has TV show pattern (S01E01 etc)
			log.Printf("[filter] Rejecting %q: searching for movie but result has TV pattern (seasons=%v, episodes=%v)",
				result.Title, parsed.Seasons, parsed.Episodes)
			continue
		}

		if !opts.IsMovie && !hasTVPattern {
			// Searching for a TV show but result has no TV indicators
			log.Printf("[filter] Rejecting %q: searching for TV show but result has no season/episode info",
				result.Title)
			continue
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
				if sizeGB > opts.MaxSizeEpisodeGB {
					log.Printf("[filter] Rejecting %q: size %.2f GB > %.2f GB limit (episode)",
						result.Title, sizeGB, opts.MaxSizeEpisodeGB)
					continue
				}
			}
		}

		// Check resolution limits if configured
		if opts.MaxResolution != "" && parsed.Resolution != "" {
			maxRes := resolutionToNumeric(opts.MaxResolution)
			parsedRes := resolutionToNumeric(parsed.Resolution)
			// Only filter if we can parse both resolutions
			if maxRes > 0 && parsedRes > 0 && parsedRes > maxRes {
				log.Printf("[filter] Rejecting %q: resolution %s > %s limit",
					result.Title, parsed.Resolution, opts.MaxResolution)
				continue
			}
		}

		// Check HDR status
		hasHDR := len(parsed.HDR) > 0

		// Check HDR exclusion if enabled
		if opts.ExcludeHdr && hasHDR {
			log.Printf("[filter] Rejecting %q: HDR exclusion enabled, detected HDR: %v", result.Title, parsed.HDR)
			continue
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
	if opts.PrioritizeHdr && !opts.ExcludeHdr {
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
	for _, candidate := range candidates {
		score := similarity.Similarity(candidate, parsedTitle)
		if score > bestScore {
			bestScore = score
			bestCandidate = candidate
		}
	}
	return bestScore, bestCandidate
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
