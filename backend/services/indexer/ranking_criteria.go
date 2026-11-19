package indexer

import (
	"strings"
	"sync"

	"novastream/config"
	"novastream/models"
	"novastream/utils/filter"
	"novastream/utils/language"
)

// rankingCriterion is one user-orderable ranking criterion. The comparator,
// the score breakdown and the exact rank values all read the same
// definition, so a new criterion is added in exactly one place.
type rankingCriterion struct {
	// compare returns -1 when i ranks before j, 1 when after, 0 on a tie.
	compare func(i, j models.NZBResult, ctx ScoringContext) int
	// score returns a level in [-levelMax, levelMax] and a reason for the
	// human-readable breakdown.
	score func(r models.NZBResult, ctx ScoringContext) (int, string)
	// value is the exact value the comparator orders by.
	value func(r models.NZBResult, ctx ScoringContext) int64
}

var rankingRegistry = map[config.RankingCriterionID]rankingCriterion{
	config.RankingServicePriority: {
		compare: func(i, j models.NZBResult, ctx ScoringContext) int {
			return compareServicePriority(i, j, ctx.ServicePriority)
		},
		score: func(r models.NZBResult, ctx ScoringContext) (int, string) {
			return scoreServicePriority(r, ctx.ServicePriority)
		},
		value: func(r models.NZBResult, ctx ScoringContext) int64 {
			preferred := (ctx.ServicePriority == config.StreamingServicePriorityUsenet && r.ServiceType == models.ServiceTypeUsenet) ||
				(ctx.ServicePriority == config.StreamingServicePriorityDebrid && r.ServiceType == models.ServiceTypeDebrid)
			return boolRankingValue(ctx.ServicePriority != config.StreamingServicePriorityNone && preferred)
		},
	},
	config.RankingPreferredTerms: {
		compare: func(i, j models.NZBResult, ctx ScoringContext) int {
			return compareWeights(ctx.termWeight(i.Title, termPreferred), ctx.termWeight(j.Title, termPreferred))
		},
		score: func(r models.NZBResult, ctx ScoringContext) (int, string) {
			return scorePreferredTerms(r, ctx.PreferredTerms)
		},
		value: func(r models.NZBResult, ctx ScoringContext) int64 {
			return int64(ctx.termWeight(r.Title, termPreferred))
		},
	},
	config.RankingNonPreferredTerms: {
		compare: func(i, j models.NZBResult, ctx ScoringContext) int {
			return -compareWeights(ctx.termWeight(i.Title, termNonPreferred), ctx.termWeight(j.Title, termNonPreferred))
		},
		score: func(r models.NZBResult, ctx ScoringContext) (int, string) {
			return scoreNonPreferredTerms(r, ctx.NonPreferredTerms)
		},
		value: func(r models.NZBResult, ctx ScoringContext) int64 {
			return -int64(ctx.termWeight(r.Title, termNonPreferred))
		},
	},
	config.RankingResolution: {
		compare: func(i, j models.NZBResult, _ ScoringContext) int { return compareResolution(i, j) },
		score:   func(r models.NZBResult, _ ScoringContext) (int, string) { return scoreResolution(r) },
		value:   func(r models.NZBResult, _ ScoringContext) int64 { return int64(extractResolutionFromResult(r)) },
	},
	config.RankingLanguage: {
		compare: func(i, j models.NZBResult, ctx ScoringContext) int { return compareLanguage(i, j, ctx.PreferredLang) },
		score:   func(r models.NZBResult, ctx ScoringContext) (int, string) { return scoreLanguage(r, ctx.PreferredLang) },
		value: func(r models.NZBResult, ctx ScoringContext) int64 {
			return boolRankingValue(ctx.PreferredLang != "" && language.HasPreferredLanguage(r.Attributes["languages"], ctx.PreferredLang))
		},
	},
	config.RankingSize: {
		compare: func(i, j models.NZBResult, _ ScoringContext) int { return compareSize(i, j) },
		score:   func(r models.NZBResult, _ ScoringContext) (int, string) { return scoreSize(r) },
		value:   func(r models.NZBResult, _ ScoringContext) int64 { return r.EffectiveItemSizeBytes() },
	},
	config.RankingPreferredScraper: {
		compare: func(i, j models.NZBResult, ctx ScoringContext) int {
			return comparePreferredScraper(i, j, ctx.PreferredScraper)
		},
		score: func(r models.NZBResult, ctx ScoringContext) (int, string) {
			return scorePreferredScraper(r, ctx.PreferredScraper)
		},
		value: func(r models.NZBResult, ctx ScoringContext) int64 {
			return boolRankingValue(ctx.PreferredScraper != "" && strings.EqualFold(r.Indexer, ctx.PreferredScraper))
		},
	},
}

// FixedRankingCheck describes a check that always runs before the
// user-orderable criteria because it protects correctness or an explicit
// user choice, not a preference. They are listed so the UI can show them.
type FixedRankingCheck struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// FixedRankingChecks lists the locked checks in the order they apply.
var FixedRankingChecks = []FixedRankingCheck{
	{ID: "explicit-audio-language", Name: "Preferred Audio Language", Description: "A profile or device audio language that differs from the title's original language ranks matching releases first."},
	{ID: "country-match", Name: "Country Match", Description: "Releases tagged with the title's production country rank before untagged ones."},
	{ID: "episode-year", Name: "Episode Year", Description: "When a result for a same-named series from another year survives filtering, releases with this series' year rank first."},
}

func compareWeights(a, b int) int {
	switch {
	case a > b:
		return -1
	case a < b:
		return 1
	}
	return 0
}

type termKind int

const (
	termPreferred termKind = iota
	termNonPreferred
	termDownload
)

// termWeightCache memoizes term weights per title for one search, so a
// sort computes each release's weights once instead of on every comparison.
type termWeightCache struct {
	mu      sync.Mutex
	weights map[termKind]map[string]int
}

func newTermWeightCache() *termWeightCache {
	return &termWeightCache{weights: map[termKind]map[string]int{}}
}

func (ctx ScoringContext) terms(kind termKind) []filter.CompiledTerm {
	switch kind {
	case termNonPreferred:
		return ctx.NonPreferredTerms
	case termDownload:
		return ctx.DownloadPreferredTerms
	default:
		return ctx.PreferredTerms
	}
}

// termWeight returns the summed weight of the matching terms of a kind.
func (ctx ScoringContext) termWeight(title string, kind termKind) int {
	terms := ctx.terms(kind)
	if len(terms) == 0 {
		return 0
	}
	cache := ctx.weightCache
	if cache == nil {
		w, _ := filter.SumMatchedWeights(title, terms)
		return w
	}
	cache.mu.Lock()
	byTitle := cache.weights[kind]
	if byTitle == nil {
		byTitle = map[string]int{}
		cache.weights[kind] = byTitle
	}
	if w, ok := byTitle[title]; ok {
		cache.mu.Unlock()
		return w
	}
	cache.mu.Unlock()
	w, _ := filter.SumMatchedWeights(title, terms)
	cache.mu.Lock()
	byTitle[title] = w
	cache.mu.Unlock()
	return w
}
