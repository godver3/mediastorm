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
	downloadPreferredTermsMultiplier = 100000
	languageMatchMaxPoints           = 50
)

// ScoreResult computes an absolute score and breakdown for a single NZBResult.
func ScoreResult(result models.NZBResult, ctx ScoringContext) (int, []models.ScoreBreakdownItem) {
	var breakdown []models.ScoreBreakdownItem
	totalScore := 0

	for position, criterion := range ctx.RankingCriteria {
		if !criterion.Enabled {
			continue
		}

		weight := 1000 / (position + 1)
		var points int
		var reason string

		switch criterion.ID {
		case config.RankingServicePriority:
			points, reason = scoreServicePriority(result, ctx.ServicePriority, weight)
		case config.RankingPreferredTerms:
			points, reason = scorePreferredTerms(result, ctx.PreferredTerms, weight)
		case config.RankingNonPreferredTerms:
			points, reason = scoreNonPreferredTerms(result, ctx.NonPreferredTerms, weight)
		case config.RankingResolution:
			points, reason = scoreResolution(result, weight)
		case config.RankingLanguage:
			points, reason = scoreLanguage(result, ctx.PreferredLang, weight)
		case config.RankingSize:
			points, reason = scoreSize(result, weight)
		case config.RankingPreferredScraper:
			points, reason = scorePreferredScraper(result, ctx.PreferredScraper, weight)
		default:
			continue
		}

		breakdown = append(breakdown, models.ScoreBreakdownItem{
			Criterion: criterion.Name,
			Points:    points,
			Reason:    reason,
		})
		totalScore += points
	}

	// Year match tiebreaker (fixed small bonus)
	if result.Attributes["yearMatch"] == "true" {
		breakdown = append(breakdown, models.ScoreBreakdownItem{
			Criterion: "Year Match",
			Points:    10,
			Reason:    "confirmed year match",
		})
		totalScore += 10
	}

	if ctx.UseDownloadRanking {
		points, reason := scoreDownloadPreferredTerms(result, ctx.DownloadPreferredTerms)
		breakdown = append(breakdown, models.ScoreBreakdownItem{
			Criterion: "Download Preferred Terms",
			Points:    points,
			Reason:    reason,
		})
		totalScore += points
	}

	return totalScore, breakdown
}

func scoreServicePriority(r models.NZBResult, priority config.StreamingServicePriority, weight int) (int, string) {
	if priority == config.StreamingServicePriorityNone {
		return 0, "no service priority configured"
	}
	isPrioritized := (priority == config.StreamingServicePriorityUsenet && r.ServiceType == models.ServiceTypeUsenet) ||
		(priority == config.StreamingServicePriorityDebrid && r.ServiceType == models.ServiceTypeDebrid)
	if isPrioritized {
		return weight, fmt.Sprintf("matches preferred service '%s'", priority)
	}
	return 0, fmt.Sprintf("not preferred service (is '%s', want '%s')", r.ServiceType, priority)
}

func scorePreferredTerms(r models.NZBResult, terms []filter.CompiledTerm, weight int) (int, string) {
	if len(terms) == 0 {
		return 0, "no preferred terms configured"
	}
	totalWeight, matchedNames := filter.SumMatchedWeights(r.Title, terms)
	if totalWeight > 0 {
		return weight * totalWeight, fmt.Sprintf("matches preferred terms '%s' (combined weight %d)", strings.Join(matchedNames, ", "), totalWeight)
	}
	return 0, "no preferred terms matched"
}

func scoreNonPreferredTerms(r models.NZBResult, terms []filter.CompiledTerm, weight int) (int, string) {
	if len(terms) == 0 {
		return 0, "no non-preferred terms configured"
	}
	totalWeight, matchedNames := filter.SumMatchedWeights(r.Title, terms)
	if totalWeight > 0 {
		return -weight * totalWeight, fmt.Sprintf("matches non-preferred terms '%s' (combined weight %d)", strings.Join(matchedNames, ", "), totalWeight)
	}
	return 0, "no non-preferred terms matched"
}

func scoreResolution(r models.NZBResult, weight int) (int, string) {
	res := extractResolutionFromResult(r)
	if res <= 0 {
		return 0, "resolution unknown"
	}
	points := (res * weight) / 2160
	return points, fmt.Sprintf("resolution %dp", res)
}

func scoreLanguage(r models.NZBResult, preferredLang string, weight int) (int, string) {
	if preferredLang == "" {
		return 0, "no preferred language configured"
	}
	if language.HasPreferredLanguage(r.Attributes["languages"], preferredLang) {
		points := weight
		if points > languageMatchMaxPoints {
			points = languageMatchMaxPoints
		}
		return points, fmt.Sprintf("has preferred language '%s'", preferredLang)
	}
	return 0, fmt.Sprintf("missing preferred language '%s'", preferredLang)
}

func scoreSize(r models.NZBResult, weight int) (int, string) {
	if r.SizeBytes <= 0 {
		return 0, "size unknown"
	}
	// Scale size proportionally: cap at 100GB for full weight
	sizeGB := float64(r.SizeBytes) / (1024 * 1024 * 1024)
	capGB := 100.0
	if sizeGB > capGB {
		sizeGB = capGB
	}
	points := int((sizeGB / capGB) * float64(weight))
	return points, fmt.Sprintf("%.1f GB", float64(r.SizeBytes)/(1024*1024*1024))
}

func scorePreferredScraper(r models.NZBResult, preferredScraper string, weight int) (int, string) {
	if preferredScraper == "" {
		return 0, "no preferred scraper configured"
	}
	if strings.EqualFold(r.Indexer, preferredScraper) {
		return weight, fmt.Sprintf("matches preferred scraper '%s'", preferredScraper)
	}
	return 0, fmt.Sprintf("not preferred scraper (is '%s')", r.Indexer)
}

func scoreDownloadPreferredTerms(r models.NZBResult, terms []filter.CompiledTerm) (int, string) {
	if len(terms) == 0 {
		return 0, "download ranking enabled, but no download preferred terms configured"
	}
	totalWeight, matchedNames := filter.SumMatchedWeights(r.Title, terms)
	if totalWeight > 0 {
		return downloadPreferredTermsMultiplier * totalWeight, fmt.Sprintf("matches download preferred terms '%s' (combined weight %d)", strings.Join(matchedNames, ", "), totalWeight)
	}
	return 0, "no download preferred terms matched"
}
