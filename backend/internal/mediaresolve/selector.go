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
	ReleaseTitle          string
	QueueName             string
	Directory             string
	TargetSeason          int
	TargetEpisode         int
	TargetEpisodeCode     string
	AbsoluteEpisodeNumber int    // For anime: the absolute episode number (e.g., 1153 for One Piece)
	TargetAirDate         string // For daily shows: the air date in YYYY-MM-DD format
	IsDaily               bool   // True if this is a daily show (talk shows, news, etc.)
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
	seasonIndicatorPattern = regexp.MustCompile(`(?i)season[\s._-]*(\d{1,2})`)    // Matches "Season 02", "Season.02", "season_02"

	// Absolute episode patterns for anime (3-4 digit episode numbers)
	// These patterns are specifically designed to match anime release formats
	// while avoiding false positives from resolutions (1080p), years (2024), etc.

	// Primary: "- NNNN" pattern with optional version suffix
	// Matches: "One Piece - 1153 [1080p]", "Anime - 0042 (720p)", "Show - 1153v2", "Anime_-_1153_"
	absoluteEpisodeDashPattern = regexp.MustCompile(`[-–][\s_]*(\d{2,4})(?:v\d)?[\s_]*[\[\(\s_]`)

	// Secondary: "Episode NNNN" or "Ep NNNN" keyword
	// Matches: "Episode 1153", "Ep.42", "Ep 123", "Episode1153", "episode 42.mkv"
	absoluteEpisodeKeywordPattern = regexp.MustCompile(`(?i)(?:episode|ep\.?)\s*(\d{2,4})(?:\s|$|[\[\(\.])`)

	// Standalone E## format (anime without season prefix)
	// Matches: " E01 ", "[E42]", "_E01_", "Show E01 'Title'" - common in anime releases
	// Does NOT match: "S01E01" (the E is preceded by a digit from season number)
	standaloneEpisodePattern = regexp.MustCompile(`(?i)(?:^|[^\d])e(\d{1,4})(?:[\s\]\)\-_\.'"v]|$)`)

	// Tertiary: "#NNNN" hash format
	// Matches: "#1153", "# 042"
	absoluteEpisodeHashPattern = regexp.MustCompile(`#\s*(\d{2,4})(?:\s|$|[\[\(])`)

	// S01ENNNN format - common for anime using absolute episode in S01E format
	// Matches: "S01E1153", "s01e0042" (where episode number is actually absolute)
	s01AbsoluteEpisodePattern = regexp.MustCompile(`(?i)s01e(\d{3,4})(?:\s|$|[\.\-\[\(])`)

	// Negative patterns to avoid false positives
	resolutionPattern = regexp.MustCompile(`(?i)(\d{3,4})p`)         // 1080p, 720p, 480p
	yearPattern       = regexp.MustCompile(`[\(\[](\d{4})[\)\]]`)    // (2024), [2024]
	checksumPattern   = regexp.MustCompile(`[\[\(]([A-Fa-f0-9]{8})[\]\)]`) // [ABCD1234]

	// Daily show date patterns
	// Matches: "2026.01.21", "2026-01-21", "2026 01 21"
	dailyDatePattern = regexp.MustCompile(`(?:^|[.\-_\s])(\d{4})[.\-\s](\d{2})[.\-\s](\d{2})(?:[.\-_\s]|$)`)
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

		// If no S##E## matches found, try matching by absolute episode number (for anime)
		if len(matching) == 0 && hints.AbsoluteEpisodeNumber > 0 {
			fmt.Printf("[selector] No S%02dE%02d matches, trying absolute episode %d\n", targetEpisode.Season, targetEpisode.Episode, hints.AbsoluteEpisodeNumber)
			var absoluteMatching []int
			for idx, cand := range candidates {
				matches := CandidateMatchesAbsoluteEpisode(cand.Label, hints.AbsoluteEpisodeNumber)
				fmt.Printf("[selector]   Candidate[%d]: %q - absoluteMatch=%v\n", idx, cand.Label, matches)
				if matches {
					absoluteMatching = append(absoluteMatching, idx)
				}
			}

			fmt.Printf("[selector] Found %d matching candidates for absolute episode %d\n", len(absoluteMatching), hints.AbsoluteEpisodeNumber)

			if len(absoluteMatching) == 1 {
				return absoluteMatching[0], fmt.Sprintf("matched absolute episode %d", hints.AbsoluteEpisodeNumber)
			}
			if len(absoluteMatching) > 1 {
				if len(releaseTokens) > 0 {
					if idx, score := pickCandidateBySimilarity(candidates, absoluteMatching, releaseTokens, releaseFlat); idx != -1 {
						return idx, fmt.Sprintf("absolute episode match + title similarity score %d", score)
					}
				}
				if idx := pickBestPriorityIndex(candidates, absoluteMatching); idx != -1 {
					return idx, fmt.Sprintf("absolute episode match fallback to extension priority (%d)", hints.AbsoluteEpisodeNumber)
				}
			}
			// If absolute episode matching found candidates, don't reject
			if len(absoluteMatching) > 0 {
				matching = absoluteMatching
			}
		}

		// If no matches found yet, try date-based matching for daily shows
		// Daily shows (talk shows, news) use date format: "Show.Name.2026.01.21.Guest.Name.mkv"
		// IMPORTANT: Use exact date match only - no tolerance. For daily shows, adjacent dates
		// are different episodes, so ±1 day tolerance would match the WRONG episode.
		if len(matching) == 0 && hints.IsDaily && hints.TargetAirDate != "" {
			fmt.Printf("[selector] No S%02dE%02d matches, trying daily date %s (exact match only)\n", targetEpisode.Season, targetEpisode.Episode, hints.TargetAirDate)

			var dateMatching []int
			for idx, cand := range candidates {
				matches := CandidateMatchesDailyDate(cand.Label, hints.TargetAirDate, 0) // Exact match only
				fmt.Printf("[selector]   Candidate[%d]: %q - exactDateMatch=%v\n", idx, cand.Label, matches)
				if matches {
					dateMatching = append(dateMatching, idx)
				}
			}

			fmt.Printf("[selector] Found %d matching candidates for daily date %s\n", len(dateMatching), hints.TargetAirDate)

			if len(dateMatching) == 1 {
				return dateMatching[0], fmt.Sprintf("matched daily date %s", hints.TargetAirDate)
			}
			if len(dateMatching) > 1 {
				if len(releaseTokens) > 0 {
					if idx, score := pickCandidateBySimilarity(candidates, dateMatching, releaseTokens, releaseFlat); idx != -1 {
						return idx, fmt.Sprintf("daily date match + title similarity score %d", score)
					}
				}
				if idx := pickBestPriorityIndex(candidates, dateMatching); idx != -1 {
					return idx, fmt.Sprintf("daily date match fallback to extension priority (%s)", hints.TargetAirDate)
				}
			}
			// If date matching found candidates, don't reject
			if len(dateMatching) > 0 {
				matching = dateMatching
			}
		}

		// CRITICAL: If we have a target episode but NO candidates matched (including absolute and daily date), reject this result.
		// This prevents falling back to title similarity which would select the wrong episode
		// (e.g., selecting S01E01 when we need S01E05 but the season pack only has eps 1-2).
		if len(matching) == 0 {
			if hints.IsDaily && hints.TargetAirDate != "" {
				fmt.Printf("[selector] No candidates match target episode S%02dE%02d or daily date %s - rejecting result\n", targetEpisode.Season, targetEpisode.Episode, hints.TargetAirDate)
				return -1, fmt.Sprintf("no file matches target episode S%02dE%02d or date %s", targetEpisode.Season, targetEpisode.Episode, hints.TargetAirDate)
			}
			if hints.AbsoluteEpisodeNumber > 0 {
				fmt.Printf("[selector] No candidates match target episode S%02dE%02d or absolute %d - rejecting result\n", targetEpisode.Season, targetEpisode.Episode, hints.AbsoluteEpisodeNumber)
				return -1, fmt.Sprintf("no file matches target episode S%02dE%02d (abs: %d)", targetEpisode.Season, targetEpisode.Episode, hints.AbsoluteEpisodeNumber)
			}
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

	// Heavy penalty for non-content files - these are never the actual episode/movie
	lower := strings.ToLower(normalized)
	if strings.Contains(lower, "sample") || strings.Contains(lower, "extras") ||
		strings.Contains(lower, "trailer") || strings.Contains(lower, "featurette") ||
		strings.Contains(lower, "bonus") || strings.Contains(lower, "promo") {
		score = 0
	}
	// Also check full path for non-content directories
	lowerPath := strings.ToLower(candidateName)
	for _, dir := range []string{"/trailers/", "/extras/", "/bonus/", "/featurettes/", "/promos/", "/behind the scenes/"} {
		if strings.Contains(lowerPath, dir) {
			score = 0
			break
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
	// assuming the target season (useful for season packs).
	// BUT only for season 1 - for higher seasons, the episode number alone
	// is ambiguous (e.g., "- 01" in a multi-season pack is likely S01E01, not S02E01).
	// For season 2+, require explicit S##E## match or use absolute episode matching.
	if target.Season == 1 {
		// Before trying bare episode matching, check if the candidate has an
		// explicit season indicator (e.g., "Season 02" in path) that differs
		// from the target season. This prevents matching Season 2 files when
		// looking for S01E02 in a multi-season batch torrent.
		if seasonMatches := seasonIndicatorPattern.FindAllStringSubmatch(candidateLabel, -1); len(seasonMatches) > 0 {
			lastMatch := seasonMatches[len(seasonMatches)-1]
			if len(lastMatch) == 2 {
				if fileSeason, err := strconv.Atoi(lastMatch[1]); err == nil && fileSeason != target.Season {
					return false
				}
			}
		}

		episode, ok = parseEpisodeNumber(candidateLabel)
		if ok && episode == target.Episode {
			return true
		}
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

	// Try " - XX - " format as fallback, but skip numbers preceded by "season"
	// (e.g., "Season 02 - 05.mkv" — "02" is the season number, not the episode)
	lowerValue := strings.ToLower(value)
	allLocs := episodeNumberPattern.FindAllStringSubmatchIndex(value, -1)
	for _, loc := range allLocs {
		if len(loc) < 4 {
			continue
		}
		epStr := value[loc[2]:loc[3]]
		episode, err := strconv.Atoi(epStr)
		if err != nil || episode <= 0 {
			continue
		}
		// Check if this number is preceded by "season" (case-insensitive)
		if loc[0] > 0 {
			prefix := strings.TrimRight(lowerValue[:loc[0]], " \t._-")
			if strings.HasSuffix(prefix, "season") {
				continue
			}
		}
		return episode, true
	}

	return 0, false
}

// ParseAbsoluteEpisodeNumber extracts an absolute episode number from a filename.
// This is designed for anime releases that use absolute numbering (e.g., "One Piece - 1153").
// Returns the episode number and true if found, or 0 and false otherwise.
func ParseAbsoluteEpisodeNumber(value string) (int, bool) {
	if strings.TrimSpace(value) == "" {
		return 0, false
	}

	// First, identify numbers we should exclude (resolutions, years, checksums)
	excludeNums := make(map[int]bool)

	// Exclude resolution numbers (1080, 720, 480, etc.)
	for _, match := range resolutionPattern.FindAllStringSubmatch(value, -1) {
		if len(match) >= 2 {
			if n, err := strconv.Atoi(match[1]); err == nil {
				excludeNums[n] = true
			}
		}
	}

	// Exclude year numbers (2020-2029, etc.)
	for _, match := range yearPattern.FindAllStringSubmatch(value, -1) {
		if len(match) >= 2 {
			if n, err := strconv.Atoi(match[1]); err == nil {
				excludeNums[n] = true
			}
		}
	}

	// Try primary pattern: "- NNNN"
	if matches := absoluteEpisodeDashPattern.FindStringSubmatch(value); len(matches) >= 2 {
		if episode, err := strconv.Atoi(matches[1]); err == nil && episode > 0 && !excludeNums[episode] {
			return episode, true
		}
	}

	// Try secondary pattern: "Episode NNNN" or "Ep NNNN"
	if matches := absoluteEpisodeKeywordPattern.FindStringSubmatch(value); len(matches) >= 2 {
		if episode, err := strconv.Atoi(matches[1]); err == nil && episode > 0 && !excludeNums[episode] {
			return episode, true
		}
	}

	// Try standalone E## pattern (anime without season prefix)
	// Matches: " E01 ", "[E42]", "Show E01 'Title'" but NOT "S01E01"
	if matches := standaloneEpisodePattern.FindStringSubmatch(value); len(matches) >= 2 {
		if episode, err := strconv.Atoi(matches[1]); err == nil && episode > 0 && !excludeNums[episode] {
			return episode, true
		}
	}

	// Try tertiary pattern: "#NNNN"
	if matches := absoluteEpisodeHashPattern.FindStringSubmatch(value); len(matches) >= 2 {
		if episode, err := strconv.Atoi(matches[1]); err == nil && episode > 0 && !excludeNums[episode] {
			return episode, true
		}
	}

	// Try S01ENNNN pattern (anime using absolute episode in S01E format)
	// This is common for long-running anime where releases use S01E1153 instead of S22E68
	if matches := s01AbsoluteEpisodePattern.FindStringSubmatch(value); len(matches) >= 2 {
		if episode, err := strconv.Atoi(matches[1]); err == nil && episode > 0 && !excludeNums[episode] {
			return episode, true
		}
	}

	return 0, false
}

// CandidateMatchesAbsoluteEpisode checks if the candidate label contains the target absolute episode number.
// This is used for anime where episodes are numbered absolutely (e.g., episode 1153 instead of S22E68).
func CandidateMatchesAbsoluteEpisode(candidateLabel string, targetAbsoluteEpisode int) bool {
	if targetAbsoluteEpisode <= 0 {
		return false
	}

	parsedEpisode, ok := ParseAbsoluteEpisodeNumber(candidateLabel)
	if !ok {
		return false
	}

	return parsedEpisode == targetAbsoluteEpisode
}

// ParseDailyDate extracts a date from a filename in YYYY.MM.DD, YYYY-MM-DD, or YYYY MM DD format.
// Returns the year, month, day and true if found, or 0, 0, 0 and false otherwise.
// This is used for daily shows (talk shows, news) that use date-based episode naming.
func ParseDailyDate(value string) (year, month, day int, ok bool) {
	if strings.TrimSpace(value) == "" {
		return 0, 0, 0, false
	}

	matches := dailyDatePattern.FindStringSubmatch(value)
	if len(matches) != 4 {
		return 0, 0, 0, false
	}

	year, err := strconv.Atoi(matches[1])
	if err != nil || year < 1900 || year > 2100 {
		return 0, 0, 0, false
	}

	month, err = strconv.Atoi(matches[2])
	if err != nil || month < 1 || month > 12 {
		return 0, 0, 0, false
	}

	day, err = strconv.Atoi(matches[3])
	if err != nil || day < 1 || day > 31 {
		return 0, 0, 0, false
	}

	return year, month, day, true
}

// DatesMatchWithTolerance checks if two dates (in YYYY-MM-DD format) are within the specified tolerance.
// Returns true if the dates are within toleranceDays of each other.
// This handles the common case where scene releases use the taping date (Jan 21)
// while TVDB uses the broadcast date (Jan 22).
func DatesMatchWithTolerance(fileDate, targetDate string, toleranceDays int) bool {
	if fileDate == "" || targetDate == "" {
		return false
	}

	// Parse file date
	fileParts := strings.Split(fileDate, "-")
	if len(fileParts) != 3 {
		return false
	}
	fileYear, err1 := strconv.Atoi(fileParts[0])
	fileMonth, err2 := strconv.Atoi(fileParts[1])
	fileDay, err3 := strconv.Atoi(fileParts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return false
	}

	// Parse target date
	targetParts := strings.Split(targetDate, "-")
	if len(targetParts) != 3 {
		return false
	}
	targetYear, err1 := strconv.Atoi(targetParts[0])
	targetMonth, err2 := strconv.Atoi(targetParts[1])
	targetDay, err3 := strconv.Atoi(targetParts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return false
	}

	// Calculate difference in days (simplified - assumes same month/year for common case)
	// For exact tolerance, we convert to day-of-year
	fileDOY := fileYear*365 + fileMonth*31 + fileDay
	targetDOY := targetYear*365 + targetMonth*31 + targetDay

	diff := fileDOY - targetDOY
	if diff < 0 {
		diff = -diff
	}

	return diff <= toleranceDays
}

// CandidateMatchesDailyDate checks if the candidate label contains a date that matches
// the target air date within the specified tolerance (typically ±1 day for daily shows).
func CandidateMatchesDailyDate(candidateLabel, targetAirDate string, toleranceDays int) bool {
	if targetAirDate == "" {
		return false
	}

	year, month, day, ok := ParseDailyDate(candidateLabel)
	if !ok {
		return false
	}

	fileDate := fmt.Sprintf("%04d-%02d-%02d", year, month, day)
	return DatesMatchWithTolerance(fileDate, targetAirDate, toleranceDays)
}
