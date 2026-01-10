package mediaresolve

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Candidate represents a playable file that can be scored and compared.
type Candidate struct {
	Label    string
	Priority int
}

// SelectionHints contains release metadata used to narrow down multi-file selections.
type SelectionHints struct {
	ReleaseTitle      string
	QueueName         string
	Directory         string
	TargetSeason      int
	TargetEpisode     int
	TargetEpisodeCode string
}

// EpisodeCode captures a parsed SXXEXX code.
type EpisodeCode struct {
	Season  int
	Episode int
}

var (
	releaseNameExtensions = map[string]struct{}{
		".nzb":  {},
		".mkv":  {},
		".mp4":  {},
		".m4v":  {},
		".avi":  {},
		".mov":  {},
		".mpg":  {},
		".mpeg": {},
		".ts":   {},
		".m2ts": {},
		".mts":  {},
		".rar":  {},
		".zip":  {},
		".7z":   {},
	}
	episodeCodePattern    = regexp.MustCompile(`(?i)s(\d{1,2})\s*e(\d{1,2})`)
	episodeAltPattern     = regexp.MustCompile(`(?i)ep(?:isode)?\.?\s*(\d{1,2})`) // Matches "Ep. 01", "Episode 01", "Ep01"
	episodeNumberPattern  = regexp.MustCompile(`(?i)[-_\s](\d{1,2})[-_\s\[\.]`)   // Matches " - 01 - ", "_01_", "_01[", "_01." for anime
)

// SelectBestCandidate applies SXXEXX matching and fuzzy title similarity against a list of candidates.
// Returns the index of the preferred candidate (or -1) along with a short reason describing the decision.
func SelectBestCandidate(candidates []Candidate, hints SelectionHints) (int, string) {
	if len(candidates) == 0 {
		return -1, ""
	}

	releasePart := NormalizeReleasePart(hints.ReleaseTitle)
	queuePart := NormalizeReleasePart(hints.QueueName)
	dirPart := NormalizeReleasePart(hints.Directory)

	releaseTokens := TokenizeParts(releasePart, queuePart, dirPart)
	releaseFlat := strings.Join(releaseTokens, "")
	var (
		targetEpisode EpisodeCode
		hasEpisode    bool
	)

	switch {
	case hints.TargetSeason > 0 && hints.TargetEpisode > 0:
		targetEpisode = EpisodeCode{Season: hints.TargetSeason, Episode: hints.TargetEpisode}
		hasEpisode = true
		fmt.Printf("[selector] Using target episode from hints: S%02dE%02d\n", targetEpisode.Season, targetEpisode.Episode)
	case strings.TrimSpace(hints.TargetEpisodeCode) != "":
		if season, episode, ok := parseEpisodeFromString(hints.TargetEpisodeCode); ok {
			targetEpisode = EpisodeCode{Season: season, Episode: episode}
			hasEpisode = true
			fmt.Printf("[selector] Parsed episode from code: S%02dE%02d\n", targetEpisode.Season, targetEpisode.Episode)
		}
	}

	if !hasEpisode {
		targetEpisode, hasEpisode = ExtractEpisodeCode(
			hints.ReleaseTitle,
			hints.QueueName,
			hints.Directory,
			releasePart,
			queuePart,
			dirPart,
			hints.TargetEpisodeCode,
		)
		if hasEpisode {
			fmt.Printf("[selector] Extracted episode from strings: S%02dE%02d\n", targetEpisode.Season, targetEpisode.Episode)
		}
	}

	if hasEpisode {
		fmt.Printf("[selector] Looking for episode S%02dE%02d among %d candidates\n", targetEpisode.Season, targetEpisode.Episode, len(candidates))
		var matching []int
		for idx, cand := range candidates {
			matches := CandidateMatchesEpisode(cand.Label, targetEpisode)
			fmt.Printf("[selector]   Candidate[%d]: %q - matches=%v\n", idx, cand.Label, matches)
			if matches {
				matching = append(matching, idx)
			}
		}

		fmt.Printf("[selector] Found %d matching candidates for S%02dE%02d\n", len(matching), targetEpisode.Season, targetEpisode.Episode)

		if len(matching) == 1 {
			return matching[0], fmt.Sprintf("matched episode code S%02dE%02d", targetEpisode.Season, targetEpisode.Episode)
		}
		if len(matching) > 1 {
			if len(releaseTokens) > 0 {
				if idx, score := pickCandidateBySimilarity(candidates, matching, releaseTokens, releaseFlat); idx != -1 {
					return idx, fmt.Sprintf("episode match + title similarity score %d", score)
				}
			}
			if idx := pickBestPriorityIndex(candidates, matching); idx != -1 {
				return idx, fmt.Sprintf("episode match fallback to extension priority (S%02dE%02d)", targetEpisode.Season, targetEpisode.Episode)
			}
		}

		// CRITICAL: If we have a target episode but NO candidates matched, reject this result.
		// This prevents falling back to title similarity which would select the wrong episode
		// (e.g., selecting S01E01 when we need S01E05 but the season pack only has eps 1-2).
		if len(matching) == 0 {
			fmt.Printf("[selector] No candidates match target episode S%02dE%02d - rejecting result\n", targetEpisode.Season, targetEpisode.Episode)
			return -1, fmt.Sprintf("no file matches target episode S%02dE%02d", targetEpisode.Season, targetEpisode.Episode)
		}
	}

	if len(releaseTokens) == 0 {
		return -1, ""
	}

	if idx, score := pickCandidateBySimilarity(candidates, nil, releaseTokens, releaseFlat); idx != -1 {
		return idx, fmt.Sprintf("title similarity score %d", score)
	}

	return -1, ""
}

func pickCandidateBySimilarity(candidates []Candidate, subset []int, releaseTokens []string, releaseFlat string) (int, int) {
	if len(releaseTokens) == 0 {
		return -1, 0
	}

	indices := subset
	if len(indices) == 0 {
		indices = make([]int, len(candidates))
		for i := range candidates {
			indices[i] = i
		}
	}

	bestIdx := -1
	bestScore := 0

	for _, idx := range indices {
		score := ComputeSimilarityScore(candidates[idx].Label, releaseTokens, releaseFlat)
		if score <= 0 {
			continue
		}

		if bestIdx == -1 || score > bestScore || (score == bestScore && candidates[idx].Priority < candidates[bestIdx].Priority) {
			bestIdx = idx
			bestScore = score
		}
	}

	return bestIdx, bestScore
}

func pickBestPriorityIndex(candidates []Candidate, indices []int) int {
	bestIdx := -1
	for _, idx := range indices {
		if bestIdx == -1 || candidates[idx].Priority < candidates[bestIdx].Priority {
			bestIdx = idx
		}
	}
	return bestIdx
}

// ComputeSimilarityScore returns a rough similarity score between a candidate name and release tokens.
func ComputeSimilarityScore(candidateName string, releaseTokens []string, releaseFlat string) int {
	if len(releaseTokens) == 0 {
		return 0
	}

	normalized := NormalizeReleasePart(candidateName)
	if normalized == "" {
		normalized = candidateName
	}

	candidateTokens := TokenizeParts(normalized)
	tokenSet := make(map[string]struct{}, len(candidateTokens))
	for _, tok := range candidateTokens {
		tokenSet[tok] = struct{}{}
	}

	score := 0
	for _, releaseTok := range releaseTokens {
		if len(releaseTok) <= 2 {
			continue
		}
		if _, ok := tokenSet[releaseTok]; ok {
			score += 10
		}
	}

	candidateFlat := strings.Join(candidateTokens, "")
	if candidateFlat != "" && releaseFlat != "" {
		if strings.Contains(candidateFlat, releaseFlat) || strings.Contains(releaseFlat, candidateFlat) {
			score += 25
		}
	}

	lower := strings.ToLower(normalized)
	if strings.Contains(lower, "sample") || strings.Contains(lower, "extras") {
		if score > 0 {
			score -= 20
			if score < 0 {
				score = 0
			}
		}
	}

	return score
}

// TokenizeParts splits release components into lowercase alphanumeric tokens.
func TokenizeParts(parts ...string) []string {
	var tokens []string
	for _, part := range parts {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "" {
			continue
		}
		fields := strings.FieldsFunc(part, func(r rune) bool {
			if r >= 'a' && r <= 'z' {
				return false
			}
			if r >= '0' && r <= '9' {
				return false
			}
			return true
		})
		for _, field := range fields {
			if field != "" {
				tokens = append(tokens, field)
			}
		}
	}
	return tokens
}

// NormalizeReleasePart flattens a release string by trimming whitespace, normalizing separators, and dropping known extensions.
func NormalizeReleasePart(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}

	normalized := strings.ReplaceAll(trimmed, "\\", "/")
	base := path.Base(normalized)
	if base == "." || base == "/" || base == "" {
		base = trimmed
	}

	extFull := path.Ext(base)
	extLower := strings.ToLower(extFull)
	if _, ok := releaseNameExtensions[extLower]; ok && extFull != "" {
		base = strings.TrimSuffix(base, extFull)
	}

	return base
}

// ExtractEpisodeCode tries to find an SXXEXX pattern across multiple strings.
func ExtractEpisodeCode(parts ...string) (EpisodeCode, bool) {
	for _, part := range parts {
		if season, episode, ok := parseEpisodeFromString(part); ok {
			return EpisodeCode{Season: season, Episode: episode}, true
		}
	}
	return EpisodeCode{}, false
}

// CandidateMatchesEpisode checks if the candidate label contains the target SXXEXX code.
func CandidateMatchesEpisode(candidateLabel string, target EpisodeCode) bool {
	season, episode, ok := parseEpisodeFromString(candidateLabel)
	if ok && season == target.Season && episode == target.Episode {
		return true
	}

	// If standard SXXEXX didn't match, try alternative patterns
	// assuming the target season (useful for season packs)
	episode, ok = parseEpisodeNumber(candidateLabel)
	if ok && target.Season > 0 && episode == target.Episode {
		return true
	}

	return false
}

func parseEpisodeFromString(value string) (int, int, bool) {
	if strings.TrimSpace(value) == "" {
		return 0, 0, false
	}
	matches := episodeCodePattern.FindStringSubmatch(value)
	if len(matches) != 3 {
		return 0, 0, false
	}

	season, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, 0, false
	}

	episode, err := strconv.Atoi(matches[2])
	if err != nil {
		return 0, 0, false
	}

	return season, episode, true
}

// parseEpisodeNumber tries to extract just an episode number from various formats
// like "Ep. 01", "Episode 01", " - 01 - ", etc.
func parseEpisodeNumber(value string) (int, bool) {
	if strings.TrimSpace(value) == "" {
		return 0, false
	}

	// Try "Ep. XX" or "Episode XX" format first
	matches := episodeAltPattern.FindStringSubmatch(value)
	if len(matches) == 2 {
		episode, err := strconv.Atoi(matches[1])
		if err == nil && episode > 0 {
			return episode, true
		}
	}

	// Try " - XX - " format as fallback
	matches = episodeNumberPattern.FindStringSubmatch(value)
	if len(matches) == 2 {
		episode, err := strconv.Atoi(matches[1])
		if err == nil && episode > 0 {
			return episode, true
		}
	}

	return 0, false
}
