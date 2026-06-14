package indexer

import (
	"fmt"
	"strings"

	"novastream/config"
	"novastream/models"
	"novastream/utils/filter"
	"novastream/utils/language"
)

// ScoringContext holds the settings needed to score results.
type ScoringContext struct {
	RankingCriteria        []config.RankingCriterion
	ServicePriority        config.StreamingServicePriority
	PreferredTerms         []filter.CompiledTerm
	NonPreferredTerms      []filter.CompiledTerm
	DownloadPreferredTerms []filter.CompiledTerm
	UseDownloadRanking     bool
	PreferredLang          string
	PreferredScraper       string
}

const (
	// levelMax is the magnitude ceiling for any single criterion's normalized
	// sub-score. Each criterion contributes a "level" in [-levelMax, levelMax],
	// which is then multiplied by its priority band weight.
	levelMax = 100

	// bandBase is the separation between adjacent priority bands. It MUST be
	// strictly greater than levelMax+1 so that a one-level difference in a
	// higher-priority criterion outweighs the combined maximum of every
	// lower-priority criterion. This is what makes criterion order a TRUE
	// priority: the #1 criterion always wins, and lower criteria only ever
	// act as tiebreakers when higher criteria are equal.
	bandBase = 128

	// downloadMatchWeightCap bounds the combined download-term weight so the
	// top band (bandBase^(N+1)) cannot overflow int64.
	downloadMatchWeightCap = 50
)

// ipow returns base**exp for non-negative exp as an int.
func ipow(base, exp int) int {
	result := 1
	for i := 0; i < exp; i++ {
		result *= base
	}
	return result
}

// ScoreResult computes an absolute score and breakdown for a single NZBResult.
//
// Scoring is lexicographic by criterion priority rather than a flat weighted
// sum: enabled criteria are assigned geometrically separated band weights
// (bandBase^exponent) so that the highest-priority criterion dominates the sum
// of all lower-priority criteria combined. Each criterion yields a normalized
// "level" in [-levelMax, levelMax]; lower criteria only break ties.
func ScoreResult(result models.NZBResult, ctx ScoringContext) (int, []models.ScoreBreakdownItem) {
	var breakdown []models.ScoreBreakdownItem
	totalScore := 0

	// Collect enabled criteria in priority order; disabled criteria do not
	// consume a band so they have zero influence on ordering.
	enabled := make([]config.RankingCriterion, 0, len(ctx.RankingCriteria))
	for _, c := range ctx.RankingCriteria {
		if c.Enabled {
			enabled = append(enabled, c)
		}
	}
	n := len(enabled)

	for rank, criterion := range enabled {
		// rank 0 (highest priority) -> exponent n (largest band);
		// rank n-1 (lowest priority) -> exponent 1. Exponent 0 is reserved
		// for the year-match tiebreaker below.
		band := ipow(bandBase, n-rank)
		var level int
		var reason string

		switch criterion.ID {
		case config.RankingServicePriority:
			level, reason = scoreServicePriority(result, ctx.ServicePriority)
		case config.RankingPreferredTerms:
			level, reason = scorePreferredTerms(result, ctx.PreferredTerms)
		case config.RankingNonPreferredTerms:
			level, reason = scoreNonPreferredTerms(result, ctx.NonPreferredTerms)
		case config.RankingResolution:
			level, reason = scoreResolution(result)
		case config.RankingLanguage:
			level, reason = scoreLanguage(result, ctx.PreferredLang)
		case config.RankingSize:
			level, reason = scoreSize(result)
		case config.RankingPreferredScraper:
			level, reason = scorePreferredScraper(result, ctx.PreferredScraper)
		default:
			continue
		}

		points := level * band
		breakdown = append(breakdown, models.ScoreBreakdownItem{
			Criterion: criterion.Name,
			Points:    points,
			Reason:    reason,
		})
		totalScore += points
	}

	// Year match tiebreaker occupies the reserved bottom band (exponent 0,
	// weight 1) so it only matters when every criterion above is equal.
	if result.Attributes["yearMatch"] == "true" {
		breakdown = append(breakdown, models.ScoreBreakdownItem{
			Criterion: "Year Match",
			Points:    1,
			Reason:    "confirmed year match",
		})
		totalScore += 1
	}

	if ctx.UseDownloadRanking {
		// Download-preferred terms override the entire priority order: they sit
		// one band above the highest criterion (exponent n+1).
		points, reason := scoreDownloadPreferredTerms(result, ctx.DownloadPreferredTerms, ipow(bandBase, n+1))
		breakdown = append(breakdown, models.ScoreBreakdownItem{
			Criterion: "Download Preferred Terms",
			Points:    points,
			Reason:    reason,
		})
		totalScore += points
	}

	return totalScore, breakdown
}

// clampLevel constrains a raw sub-score to [-levelMax, levelMax].
func clampLevel(v int) int {
	if v > levelMax {
		return levelMax
	}
	if v < -levelMax {
		return -levelMax
	}
	return v
}

// Each scorer returns a normalized "level" in [-levelMax, levelMax]. The caller
// multiplies the level by the criterion's priority band weight.

func scoreServicePriority(r models.NZBResult, priority config.StreamingServicePriority) (int, string) {
	if priority == config.StreamingServicePriorityNone {
		return 0, "no service priority configured"
	}
	isPrioritized := (priority == config.StreamingServicePriorityUsenet && r.ServiceType == models.ServiceTypeUsenet) ||
		(priority == config.StreamingServicePriorityDebrid && r.ServiceType == models.ServiceTypeDebrid)
	if isPrioritized {
		return levelMax, fmt.Sprintf("matches preferred service '%s'", priority)
	}
	return 0, fmt.Sprintf("not preferred service (is '%s', want '%s')", r.ServiceType, priority)
}

func scorePreferredTerms(r models.NZBResult, terms []filter.CompiledTerm) (int, string) {
	if len(terms) == 0 {
		return 0, "no preferred terms configured"
	}
	totalWeight, matchedNames := filter.SumMatchedWeights(r.Title, terms)
	if totalWeight > 0 {
		return clampLevel(totalWeight), fmt.Sprintf("matches preferred terms '%s' (combined weight %d)", strings.Join(matchedNames, ", "), totalWeight)
	}
	return 0, "no preferred terms matched"
}

func scoreNonPreferredTerms(r models.NZBResult, terms []filter.CompiledTerm) (int, string) {
	if len(terms) == 0 {
		return 0, "no non-preferred terms configured"
	}
	totalWeight, matchedNames := filter.SumMatchedWeights(r.Title, terms)
	if totalWeight > 0 {
		return -clampLevel(totalWeight), fmt.Sprintf("matches non-preferred terms '%s' (combined weight %d)", strings.Join(matchedNames, ", "), totalWeight)
	}
	return 0, "no non-preferred terms matched"
}

func scoreResolution(r models.NZBResult) (int, string) {
	res := extractResolutionFromResult(r)
	if res <= 0 {
		return 0, "resolution unknown"
	}
	return clampLevel((res * levelMax) / 2160), fmt.Sprintf("resolution %dp", res)
}

func scoreLanguage(r models.NZBResult, preferredLang string) (int, string) {
	if preferredLang == "" {
		return 0, "no preferred language configured"
	}
	if language.HasPreferredLanguage(r.Attributes["languages"], preferredLang) {
		return levelMax, fmt.Sprintf("has preferred language '%s'", preferredLang)
	}
	return 0, fmt.Sprintf("missing preferred language '%s'", preferredLang)
}

// sizeLevelCapGB is the file size (GB) at which the size criterion saturates to
// levelMax. Files at or above this earn the maximum size level; below it scales
// linearly so that, when Size is the top criterion, the largest file wins.
const sizeLevelCapGB = 100.0

func scoreSize(r models.NZBResult) (int, string) {
	if r.SizeBytes <= 0 {
		return 0, "size unknown"
	}
	sizeGB := float64(r.SizeBytes) / (1024 * 1024 * 1024)
	level := int((sizeGB / sizeLevelCapGB) * float64(levelMax))
	return clampLevel(level), fmt.Sprintf("%.1f GB", sizeGB)
}

func scorePreferredScraper(r models.NZBResult, preferredScraper string) (int, string) {
	if preferredScraper == "" {
		return 0, "no preferred scraper configured"
	}
	if strings.EqualFold(r.Indexer, preferredScraper) {
		return levelMax, fmt.Sprintf("matches preferred scraper '%s'", preferredScraper)
	}
	return 0, fmt.Sprintf("not preferred scraper (is '%s')", r.Indexer)
}

func scoreDownloadPreferredTerms(r models.NZBResult, terms []filter.CompiledTerm, band int) (int, string) {
	if len(terms) == 0 {
		return 0, "download ranking enabled, but no download preferred terms configured"
	}
	totalWeight, matchedNames := filter.SumMatchedWeights(r.Title, terms)
	if totalWeight > 0 {
		if totalWeight > downloadMatchWeightCap {
			totalWeight = downloadMatchWeightCap
		}
		return totalWeight * band, fmt.Sprintf("matches download preferred terms '%s' (combined weight %d)", strings.Join(matchedNames, ", "), totalWeight)
	}
	return 0, "no download preferred terms matched"
}
