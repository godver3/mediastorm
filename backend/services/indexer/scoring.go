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
	ExplicitAudioLanguage  string
	PreferredScraper       string
	// weightCache memoizes term weights per title for one search.
	weightCache *termWeightCache
}

const (
	// levelMax is the magnitude ceiling for any single criterion's normalized
	// sub-score. Each criterion contributes a "level" in [-levelMax, levelMax],
	// which is then multiplied by its priority band weight.
	levelMax = 100

	// scoreBand is the human-facing point spacing between adjacent ranking
	// criteria. Actual ranking uses lexicographic comparison in service.go, so
	// this score can stay bounded and readable instead of encoding priority with
	// huge exponential bands.
	scoreBand = 100

	// downloadMatchWeightCap bounds the combined download-term weight so the
	// display score stays readable.
	downloadMatchWeightCap = 50
)

// ScoreResult computes an absolute score and breakdown for a single NZBResult.
//
// The score is a bounded, human-readable explanation value. Result ordering is
// done lexicographically by criterion priority in service.go.
func ScoreResult(result models.NZBResult, ctx ScoringContext) (int, []models.ScoreBreakdownItem) {
	var breakdown []models.ScoreBreakdownItem
	breakdown = append(breakdown, explicitAudioBreakdown(result, ctx.ExplicitAudioLanguage)...)
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
		band := scoreBand * (n - rank)
		def, ok := rankingRegistry[criterion.ID]
		if !ok {
			continue
		}
		level, reason := def.score(result, ctx)

		points := level * band
		breakdown = append(breakdown, models.ScoreBreakdownItem{
			Criterion: criterion.Name,
			Points:    points,
			RankValue: rankingValueForCriterion(result, criterion.ID, ctx),
			Reason:    reason,
		})
		totalScore += points
	}

	// A general matching year is diagnostic-only. Targeted episode searches use
	// year precedence only when another passed result has an explicit conflicting
	// year. Explicitly wrong years are normally filtered earlier; missing years
	// and valid episode-air years remain neutral.
	if result.Attributes["yearMatch"] == "true" {
		reason := "explicit year matches expected title year"
		if result.Attributes["episodeYearPriority"] == "true" {
			reason = "explicit series year receives targeted episode priority because a conflicting year survived filtering"
		}
		breakdown = append(breakdown, models.ScoreBreakdownItem{
			Criterion: "Year Match",
			Points:    0,
			RankValue: boolRankingValue(result.Attributes["episodeYearPriority"] == "true"),
			Reason:    reason,
		})
	}
	expectedCountry := result.Attributes["expectedCountry"]
	matchesCountry := result.Attributes["countryMatch"] == "true"
	if expectedCountry != "" || matchesCountry {
		reason := "no explicit release country; does not receive country priority"
		if matchesCountry {
			reason = fmt.Sprintf("explicit release country %s matches expected country", result.Attributes["releaseCountry"])
		}
		breakdown = append(breakdown, models.ScoreBreakdownItem{
			Criterion: "Country Match",
			Points:    0,
			RankValue: boolRankingValue(matchesCountry),
			Reason:    reason,
		})
	}

	if ctx.UseDownloadRanking {
		points, reason := scoreDownloadPreferredTerms(result, ctx.DownloadPreferredTerms, scoreBand*(n+1))
		breakdown = append(breakdown, models.ScoreBreakdownItem{
			Criterion: "Download Preferred Terms",
			Points:    points,
			RankValue: rankingValueForDownloadTerms(result, ctx.DownloadPreferredTerms),
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

func boolRankingValue(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

// rankingValueForCriterion mirrors the comparator's exact value. Unlike the
// legacy display points, it is not clamped, rounded, or combined with values
// from lower-priority criteria.
func rankingValueForCriterion(result models.NZBResult, criterion config.RankingCriterionID, ctx ScoringContext) int64 {
	if def, ok := rankingRegistry[criterion]; ok {
		return def.value(result, ctx)
	}
	return 0
}

func rankingValueForDownloadTerms(result models.NZBResult, terms []filter.CompiledTerm) int64 {
	weight, _ := filter.SumMatchedWeights(result.Title, terms)
	return int64(weight)
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
	effectiveSize := r.EffectiveItemSizeBytes()
	if effectiveSize <= 0 {
		return 0, "size unknown"
	}
	sizeGB := float64(effectiveSize) / (1024 * 1024 * 1024)
	level := int((sizeGB / sizeLevelCapGB) * float64(levelMax))
	reason := fmt.Sprintf("%.1f GB", sizeGB)
	if r.EpisodeCount > 1 {
		reason += " per item"
	}
	return clampLevel(level), reason
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

func explicitAudioBreakdown(result models.NZBResult, preferred string) []models.ScoreBreakdownItem {
	if preferred == "" {
		return nil
	}
	_, reason := scoreLanguage(result, preferred)
	if strings.TrimSpace(result.Attributes["languages"]) == "" {
		reason = fmt.Sprintf("audio language unknown; preferred language %q is unverified", preferred)
	}
	return []models.ScoreBreakdownItem{{
		Criterion: "Preferred Audio Language", RankValue: boolRankingValue(language.HasPreferredLanguage(result.Attributes["languages"], preferred)),
		Reason: "fixed first priority: " + reason,
	}}
}

// Release-date mode still reports explicit language ahead of the date rule.
func newestReleaseBreakdown(result models.NZBResult, preferred string) []models.ScoreBreakdownItem {
	points := 0
	if !result.PublishDate.IsZero() {
		points = int(result.PublishDate.Unix())
	}
	return append(explicitAudioBreakdown(result, preferred), models.ScoreBreakdownItem{
		Criterion: "Newest Release", Points: points, RankValue: int64(points), Reason: "source-reported release time",
	})
}
