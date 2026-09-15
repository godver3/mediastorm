package handlers

import (
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"novastream/models"
)

const strongSportsConfidence = 0.85

var sportsNoiseTokens = map[string]struct{}{
	"4k": {}, "8k": {}, "channel": {}, "feed": {}, "fhd": {}, "hd": {},
	"live": {}, "mlb": {}, "nba": {}, "nfl": {}, "nhl": {}, "premium": {},
	"sd": {}, "sport": {}, "sports": {}, "tv": {}, "uhd": {}, "us": {}, "usa": {},
}

var sportsNegativeTokens = map[string]struct{}{
	"archive": {}, "classic": {}, "movie": {}, "movies": {}, "ondemand": {},
	"ppv": {}, "raw": {}, "replay": {}, "series": {}, "vod": {},
}

// Built-in aliases are deliberately small and league-scoped. Structured ESPN location,
// nickname and abbreviation fields cover the normal path; these entries cover provider
// spellings that those fields cannot express.
var builtInTeamAliases = map[string]map[string][]string{
	"mlb": {
		"arizona diamondbacks": {"d backs", "dbacks"},
		"boston red sox":       {"bosox", "redsox"},
		"chicago white sox":    {"whitesox"},
		"new york yankees":     {"yanks"},
		"st louis cardinals":   {"cards"},
		"toronto blue jays":    {"jays"},
	},
	"nfl": {
		"new england patriots": {"pats"},
		"philadelphia eagles":  {"philly"},
		"san francisco 49ers":  {"niners", "9ers"},
	},
	"nba": {
		"boston celtics":        {"celts"},
		"golden state warriors": {"dubs"},
		"philadelphia 76ers":    {"sixers"},
	},
	"nhl": {
		"colorado avalanche":  {"avs"},
		"detroit red wings":   {"wings"},
		"montreal canadiens":  {"habs"},
		"pittsburgh penguins": {"pens"},
		"tampa bay lightning": {"bolts"},
		"toronto maple leafs": {"leafs"},
		"washington capitals": {"caps"},
	},
}

var broadcastAliases = map[string][]string{
	"abc":                    {"abc", "abc network"},
	"cbs":                    {"cbs", "cbs network"},
	"fox":                    {"fox", "fox network"},
	"nbc":                    {"nbc", "nbc network"},
	"espn":                   {"espn", "espn hd"},
	"espn unlmtd":            {"espn unlmtd", "espn unlimited"},
	"netflix":                {"netflix"},
	"peacock":                {"peacock", "peacock sports"},
	"mlb tv":                 {"mlb tv", "mlbtv"},
	"yes":                    {"yes", "yes network"},
	"nesn":                   {"nesn", "new england sports network"},
	"masn":                   {"masn", "mid atlantic sports network"},
	"chsn":                   {"chsn", "chicago sports network"},
	"sportsnet la":           {"sportsnet la", "spectrum sportsnet la", "snla"},
	"sportsnet":              {"sportsnet"},
	"tva":                    {"tva", "tva sports"},
	"rangers sports network": {"rangers sports network"},
	"nbc sports ca":          {"nbc sports ca", "nbc sports california", "nbcsca"},
	"nbc sports phil":        {"nbc sports phil", "nbc sports philly", "nbc sports philadelphia", "nbcsp"},
	"angels tv":              {"angels tv", "angelstv"},
	"bravesvision":           {"bravesvision", "braves vision"},
	"brewers tv":             {"brewers tv", "brewerstv"},
	"cardinals tv":           {"cardinals tv", "cardinalstv"},
	"cleguardians tv":        {"cleguardians tv", "cle guardians tv", "cleveland guardians tv", "guardians tv", "guardianstv"},
	"mariners tv":            {"mariners tv", "marinerstv"},
	"padres tv":              {"padres tv", "padrestv"},
	"rays tv":                {"rays tv", "raystv"},
	"rockies tv":             {"rockies tv", "rockiestv"},
	"royals tv":              {"royals tv", "royalstv"},
	"tigers tv":              {"tigers tv", "tigerstv"},
	"twins tv":               {"twins tv", "twinstv"},
}

type sportsTeamIdentity struct {
	league       string
	name         string
	location     string
	nickname     string
	abbreviation string
	aliases      []string
}

type sportsEvidence struct {
	score     float64
	reason    string
	terms     []string
	team      string
	on        string
	program   string
	lifecycle string
}

const (
	sportsLifecycleLive  = "live"
	sportsLifecycleEnded = "ended"
)

// normalizeSportsTextCache memoizes normalizeSportsText's (expensive: Unicode NFD
// decomposition over the full string) result per distinct input. Auto-link preview scores
// every channel against every team in a league (e.g. 30 teams x tens of thousands of
// channels on a large Xtream playlist), and the same channel.Name/TvgName gets normalized
// once per team - without caching, that's the same NFD decomposition repeated dozens of
// times per channel, which is what made /sports/leagues/{id}/auto-link/preview hang
// (found by calling it directly against a ~57k-channel test playlist: 30+ seconds, no
// response). sync.Map since handler requests can run concurrently; unbounded but bounded in
// practice by the number of distinct channel/team-name strings ever seen (a few MB at most).
var normalizeSportsTextCache sync.Map

func normalizeSportsText(value string) string {
	if cached, ok := normalizeSportsTextCache.Load(value); ok {
		return cached.(string)
	}
	result := normalizeSportsTextUncached(value)
	normalizeSportsTextCache.Store(value, result)
	return result
}

func normalizeSportsTextUncached(value string) string {
	value = norm.NFD.String(strings.ToLower(value))
	var b strings.Builder
	space := false
	for _, r := range value {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func sportsTokens(value string) []string {
	return strings.Fields(normalizeSportsText(value))
}

func tokenSet(value string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, token := range sportsTokens(value) {
		set[token] = struct{}{}
	}
	return set
}

func containsAllTokens(target string, wanted []string) bool {
	if len(wanted) == 0 {
		return false
	}
	set := tokenSet(target)
	for _, token := range wanted {
		if _, ok := set[token]; !ok {
			return false
		}
	}
	return true
}

func containsMatchupMarker(value string) bool {
	normed := " " + normalizeSportsText(value) + " "
	return strings.Contains(normed, " vs ") || strings.Contains(normed, " versus ") ||
		strings.Contains(normed, " at ") || strings.Contains(value, "@")
}

// sportsLifecycle recognizes provider-owned state prefixes only when they occupy their
// own leading segment. This catches "End | ..." without treating Weekend or "end zone"
// as feed state.
func sportsLifecycle(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	first := trimmed
	if index := strings.IndexAny(first, "|:-"); index >= 0 {
		first = first[:index]
	}
	switch normalizeSportsText(first) {
	case "end", "ended", "finished", "off air", "offline":
		return sportsLifecycleEnded
	case "live":
		return sportsLifecycleLive
	default:
		return ""
	}
}

func sportsMatchSegments(value string) []string {
	if !strings.ContainsAny(value, "|:") {
		return []string{value}
	}
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '|' || r == ':' })
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		if normalizeSportsText(part) == "" || sportsLifecycle(part) != "" {
			continue
		}
		segments = append(segments, part)
	}
	if len(segments) == 0 {
		return []string{value}
	}
	return segments
}

func confidenceTier(score float64) string {
	if score >= strongSportsConfidence {
		return "strong"
	}
	return "possible"
}

func roundSportsConfidence(score float64) float64 {
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	return math.Round(score*100) / 100
}

func identityFromTeam(team models.SportsTeam, league string) sportsTeamIdentity {
	return makeSportsIdentity(league, team.Name, team.Location, team.Nickname, team.Abbreviation)
}

func identityFromRecord(team models.SportsTeamRecord) sportsTeamIdentity {
	return makeSportsIdentity(team.League, team.Name, team.Location, team.Nickname, team.Abbreviation)
}

func makeSportsIdentity(league, name, location, nickname, abbreviation string) sportsTeamIdentity {
	name = strings.TrimSpace(name)
	location = strings.TrimSpace(location)
	nickname = strings.TrimSpace(nickname)

	// Older rows have no structured identity. Prefer known multi-word suffixes before
	// falling back to the final token.
	if location == "" || nickname == "" {
		normName := normalizeSportsText(name)
		knownSuffixes := []string{"red sox", "white sox", "blue jays", "maple leafs", "red wings", "trail blazers"}
		for _, suffix := range knownSuffixes {
			if strings.HasSuffix(normName, suffix) {
				nickname = suffix
				location = strings.TrimSpace(strings.TrimSuffix(normName, suffix))
				break
			}
		}
		if nickname == "" {
			parts := strings.Fields(name)
			if len(parts) > 0 {
				nickname = parts[len(parts)-1]
				location = strings.Join(parts[:len(parts)-1], " ")
			}
		}
	}

	identity := sportsTeamIdentity{
		league:       strings.ToLower(strings.TrimSpace(league)),
		name:         name,
		location:     location,
		nickname:     nickname,
		abbreviation: abbreviation,
	}
	if aliases := builtInTeamAliases[identity.league][normalizeSportsText(name)]; len(aliases) > 0 {
		identity.aliases = aliases
	}
	return identity
}

func meaningfulExtraTokens(target string, matched []string) []string {
	matchedSet := make(map[string]struct{}, len(matched))
	for _, token := range matched {
		matchedSet[token] = struct{}{}
	}
	var extras []string
	for _, token := range sportsTokens(target) {
		if _, ok := matchedSet[token]; ok {
			continue
		}
		if _, ok := sportsNoiseTokens[token]; ok {
			continue
		}
		allDigits := true
		for _, r := range token {
			if !unicode.IsDigit(r) {
				allDigits = false
				break
			}
		}
		if allDigits {
			continue
		}
		extras = append(extras, token)
	}
	return extras
}

func hasNegativeSportsLabel(target string) bool {
	for token := range tokenSet(target) {
		if _, ok := sportsNegativeTokens[token]; ok {
			return true
		}
	}
	return false
}

func scoreTextForTeam(target string, team sportsTeamIdentity, permanentLink bool) sportsEvidence {
	normTarget := normalizeSportsText(target)
	if normTarget == "" {
		return sportsEvidence{}
	}
	fullTokens := sportsTokens(team.name)
	locationTokens := sportsTokens(team.location)
	nicknameTokens := sportsTokens(team.nickname)
	abbrTokens := sportsTokens(team.abbreviation)

	if normTarget == normalizeSportsText(team.name) {
		return sportsEvidence{score: 1, reason: "Exact team name", terms: fullTokens, team: team.name}
	}
	if containsAllTokens(target, fullTokens) {
		extras := meaningfulExtraTokens(target, fullTokens)
		score := 0.96
		if len(extras) == 1 {
			score = 0.92
		} else if len(extras) > 1 {
			score = 0.86
		}
		if permanentLink && containsMatchupMarker(target) {
			score = 0.58
		}
		if hasNegativeSportsLabel(target) {
			score -= 0.18
		}
		return sportsEvidence{score: score, reason: "Full team name", terms: fullTokens, team: team.name}
	}

	for _, alias := range team.aliases {
		aliasTokens := sportsTokens(alias)
		if containsAllTokens(target, aliasTokens) {
			score := 0.88
			if permanentLink && containsMatchupMarker(target) {
				score = 0.52
			}
			return sportsEvidence{score: score, reason: "Team alias", terms: aliasTokens, team: team.name}
		}
	}

	if containsAllTokens(target, nicknameTokens) {
		score := 0.82
		if len(nicknameTokens) == 1 && len(nicknameTokens[0]) < 4 {
			score = 0
		}
		if permanentLink && containsMatchupMarker(target) {
			score = 0.48
		}
		if hasNegativeSportsLabel(target) {
			score -= 0.18
		}
		return sportsEvidence{score: score, reason: team.nickname + " nickname", terms: nicknameTokens, team: team.name}
	}

	if len(abbrTokens) == 1 && len(abbrTokens[0]) >= 3 && containsAllTokens(target, abbrTokens) {
		score := 0.72
		if normTarget == abbrTokens[0] {
			score = 0.8
		}
		return sportsEvidence{score: score, reason: "Team abbreviation", terms: abbrTokens, team: team.name}
	}

	if containsAllTokens(target, locationTokens) {
		// A bare city is weak evidence. If the target has another meaningful word it
		// likely names a different team from the same market (Capitals vs Nationals).
		if len(meaningfulExtraTokens(target, locationTokens)) > 0 {
			return sportsEvidence{}
		}
		return sportsEvidence{score: 0.42, reason: "Team location only", terms: locationTokens, team: team.name}
	}
	return sportsEvidence{}
}

func scoreMatchupSegment(target string, home, away sportsTeamIdentity) sportsEvidence {
	type matchupVariant struct {
		home   []string
		away   []string
		score  float64
		reason string
	}
	variants := []matchupVariant{
		{sportsTokens(home.name), sportsTokens(away.name), 0.98, "Both full team names"},
		{sportsTokens(home.nickname), sportsTokens(away.nickname), 0.9, "Both team nicknames"},
		{sportsTokens(home.location), sportsTokens(away.location), 0.86, "Both team locations"},
		{sportsTokens(home.location), sportsTokens(away.nickname), 0.84, "Location and opponent nickname"},
		{sportsTokens(home.nickname), sportsTokens(away.location), 0.84, "Nickname and opponent location"},
	}
	// Abbreviations require an explicit matchup marker and distinct whole tokens.
	homeAbbr, awayAbbr := sportsTokens(home.abbreviation), sportsTokens(away.abbreviation)
	if containsMatchupMarker(target) {
		if len(homeAbbr) == 1 && len(homeAbbr[0]) >= 2 {
			variants = append(variants, matchupVariant{homeAbbr, sportsTokens(away.nickname), 0.86, "Team abbreviation and opponent nickname"})
		}
		if len(awayAbbr) == 1 && len(awayAbbr[0]) >= 2 {
			variants = append(variants, matchupVariant{sportsTokens(home.nickname), awayAbbr, 0.86, "Team nickname and opponent abbreviation"})
		}
		if len(homeAbbr) == 1 && len(awayAbbr) == 1 && len(homeAbbr[0]) >= 2 && len(awayAbbr[0]) >= 2 && homeAbbr[0] != awayAbbr[0] {
			variants = append(variants, matchupVariant{homeAbbr, awayAbbr, 0.86, "Both team abbreviations"})
		}
	}
	for _, variant := range variants {
		if len(variant.home) == 0 || len(variant.away) == 0 {
			continue
		}
		if containsAllTokens(target, variant.home) && containsAllTokens(target, variant.away) {
			score := variant.score
			if containsMatchupMarker(target) {
				score = math.Min(1, score+0.04)
			}
			return sportsEvidence{
				score: score, reason: variant.reason,
				terms: append(append([]string{}, variant.home...), variant.away...), on: "matchup-name",
			}
		}
	}

	homeScore := scoreTextForTeam(target, home, false)
	awayScore := scoreTextForTeam(target, away, false)
	if homeScore.score > 0 && awayScore.score > 0 {
		score := math.Min(homeScore.score, awayScore.score)
		if containsMatchupMarker(target) {
			score = math.Min(1, score+0.08)
		}
		return sportsEvidence{
			score:  score,
			reason: "Both teams in matchup",
			terms:  append(append([]string{}, homeScore.terms...), awayScore.terms...),
			on:     "matchup-name",
		}
	}
	// A dedicated team channel is plausible; an explicit matchup with a different
	// opponent is not evidence for this event.
	if containsMatchupMarker(target) {
		return scoreFuzzySportsMatchup(target, home, away)
	}

	if homeScore.score >= awayScore.score && homeScore.score > 0 {
		homeScore.score = math.Min(homeScore.score, 0.74)
		homeScore.on = "team-name"
		return homeScore
	}
	if awayScore.score > 0 {
		awayScore.score = math.Min(awayScore.score, 0.74)
		awayScore.on = "team-name"
		return awayScore
	}
	return sportsEvidence{}
}

func scoreMatchupText(target string, home, away sportsTeamIdentity) sportsEvidence {
	best := sportsEvidence{}
	for _, segment := range sportsMatchSegments(target) {
		candidate := scoreMatchupSegment(segment, home, away)
		if candidate.score > best.score {
			best = candidate
		}
	}
	best.lifecycle = sportsLifecycle(target)
	return best
}

func canonicalBroadcastVariants(value string) [][]string {
	normed := normalizeSportsText(value)
	canonical := normed
	for key, values := range broadcastAliases {
		if key == normed {
			canonical = key
			break
		}
		for _, alias := range values {
			if normalizeSportsText(alias) == normed {
				canonical = key
				break
			}
		}
	}
	aliases := append([]string{}, broadcastAliases[canonical]...)
	if len(aliases) == 0 {
		aliases = []string{value}
	}
	if strings.HasSuffix(normed, " tv") {
		aliases = append(aliases, normed, strings.ReplaceAll(normed, " ", ""))
	}
	seen := make(map[string]struct{})
	var variants [][]string
	for _, alias := range aliases {
		tokens := sportsTokens(alias)
		key := strings.Join(tokens, " ")
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		variants = append(variants, tokens)
	}
	return variants
}

func scoreBroadcastText(target, broadcast string) sportsEvidence {
	lifecycle := sportsLifecycle(target)
	if lifecycle == sportsLifecycleEnded {
		return sportsEvidence{lifecycle: lifecycle}
	}
	normTarget := normalizeSportsText(target)
	best := sportsEvidence{}
	for _, tokens := range canonicalBroadcastVariants(broadcast) {
		if !containsAllTokens(target, tokens) {
			continue
		}
		extraCount := len(meaningfulExtraTokens(target, tokens))
		score := 0.76
		if normTarget == strings.Join(tokens, " ") {
			score = 0.98
		} else if extraCount == 0 {
			score = 0.94
		} else if extraCount == 1 {
			score = 0.86
		} else if extraCount >= 3 {
			score = 0.62
		}
		if hasNegativeSportsLabel(target) {
			score -= 0.18
		}
		if score > best.score {
			reason := broadcast + " channel name"
			if score >= 0.88 {
				reason = "Exact " + broadcast + " network"
			}
			best = sportsEvidence{score: score, reason: reason, terms: tokens, on: "broadcast", lifecycle: lifecycle}
		}
	}
	return best
}

func explicitChannelQueryMatches(channel LiveChannel, query string) bool {
	tokens := sportsTokens(query)
	return containsAllTokens(channel.Name, tokens) || containsAllTokens(channel.TvgName, tokens) || containsAllTokens(channel.TvgID, tokens)
}

func channelCandidateFromEvidence(channel LiveChannel, evidence sportsEvidence) models.SportsChannelCandidate {
	score := roundSportsConfidence(evidence.score)
	return models.SportsChannelCandidate{
		ChannelID: channel.ID, ChannelName: channel.Name, ChannelURL: channel.URL,
		ChannelLogo: channel.Logo, ChannelTvgID: channel.TvgID, SourceID: channel.SourceID,
		SourceName: channel.SourceName, Group: channel.Group, Confidence: score,
		ConfidenceTier: confidenceTier(score), MatchReason: evidence.reason, MatchedTerms: evidence.terms,
	}
}

func rankTeamChannelCandidates(team models.SportsTeamRecord, channels []LiveChannel, query string, limit int) []models.SportsChannelCandidate {
	identity := identityFromRecord(team)
	explicit := strings.TrimSpace(query) != ""
	results := make([]models.SportsChannelCandidate, 0)
	for _, channel := range channels {
		ended := sportsLifecycle(channel.Name) == sportsLifecycleEnded || sportsLifecycle(channel.TvgName) == sportsLifecycleEnded
		if ended && !explicit {
			continue
		}
		if explicit && !explicitChannelQueryMatches(channel, query) {
			continue
		}
		evidence := scoreTextForTeam(channel.Name, identity, true)
		if evidence.score <= 0 && channel.TvgName != "" {
			evidence = scoreTextForTeam(channel.TvgName, identity, true)
		}
		if evidence.score <= 0 && !explicit {
			continue
		}
		if evidence.score <= 0 {
			evidence = sportsEvidence{score: 0, reason: "Manual text match", terms: sportsTokens(query), team: team.Name}
		}
		if ended {
			evidence.score = math.Min(evidence.score, 0.2)
			evidence.reason = "Ended/offline feed"
			evidence.lifecycle = sportsLifecycleEnded
		}
		results = append(results, channelCandidateFromEvidence(channel, evidence))
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Confidence != results[j].Confidence {
			return results[i].Confidence > results[j].Confidence
		}
		if results[i].ChannelName != results[j].ChannelName {
			return strings.ToLower(results[i].ChannelName) < strings.ToLower(results[j].ChannelName)
		}
		return results[i].SourceName < results[j].SourceName
	})
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results
}

func sortSportsStreamMatches(matches []models.SportsStreamMatch) {
	for i := range matches {
		matches[i].ReportedQuality = reportedSportsQuality(matches[i].ChannelName)
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Confidence != matches[j].Confidence {
			return matches[i].Confidence > matches[j].Confidence
		}
		if order := compareSportsQuality(matches[i].ReportedQuality, matches[j].ReportedQuality); order != 0 {
			return order < 0
		}
		if matches[i].ChannelName != matches[j].ChannelName {
			return strings.ToLower(matches[i].ChannelName) < strings.ToLower(matches[j].ChannelName)
		}
		return matches[i].SourceName < matches[j].SourceName
	})
}

func canonicalSportsGroupKey(match models.SportsStreamMatch) string {
	value := match.ChannelName
	if match.ProgramTitle != "" {
		value = match.ProgramTitle
	}
	segments := sportsMatchSegments(value)
	if len(segments) > 0 {
		value = strings.Join(segments, " ")
	}
	kept := make([]string, 0)
	for _, token := range sportsTokens(value) {
		if _, noise := sportsNoiseTokens[token]; noise {
			continue
		}
		kept = append(kept, token)
	}
	key := strings.Join(kept, " ")
	if key == "" {
		key = normalizeSportsText(match.ChannelName)
	}
	if key == "" {
		key = match.SourceID + ":" + match.ChannelID
	}
	return key
}

// buildSportsStreamGroups removes exact duplicate stream identities, then collapses
// equivalent display/program titles while retaining distinct feeds as alternatives.
func buildSportsStreamGroups(matches []models.SportsStreamMatch) ([]models.SportsStreamMatch, []models.SportsStreamGroup) {
	unique := make([]models.SportsStreamMatch, 0, len(matches))
	seen := make(map[string]struct{})
	for _, match := range matches {
		identity := strings.TrimSpace(match.ChannelURL)
		if identity == "" {
			identity = match.SourceID + ":" + match.ChannelID
		}
		if _, exists := seen[identity]; exists {
			continue
		}
		seen[identity] = struct{}{}
		unique = append(unique, match)
	}

	groups := make([]models.SportsStreamGroup, 0)
	byKey := make(map[string]int)
	for _, match := range unique {
		key := canonicalSportsGroupKey(match)
		// A possible feed must never inherit a strong group's confidence or auto-play eligibility.
		if match.ConfidenceTier != "strong" {
			key += ":possible"
		}
		if index, exists := byKey[key]; exists {
			groups[index].Alternatives = append(groups[index].Alternatives, match)
			groups[index].FeedCount++
			continue
		}
		byKey[key] = len(groups)
		display := match.ProgramTitle
		if display == "" {
			display = match.ChannelName
		}
		groups = append(groups, models.SportsStreamGroup{
			GroupKey: key, DisplayName: display, Confidence: match.Confidence,
			ConfidenceTier: match.ConfidenceTier, MatchReason: match.MatchReason,
			FeedCount: 1, Primary: match, Alternatives: []models.SportsStreamMatch{},
		})
	}
	return unique, groups
}

var sportsMonthTokens = map[string]struct{}{
	"jan": {}, "january": {}, "feb": {}, "february": {}, "mar": {}, "march": {},
	"apr": {}, "april": {}, "may": {}, "jun": {}, "june": {}, "jul": {}, "july": {},
	"aug": {}, "august": {}, "sep": {}, "sept": {}, "september": {}, "oct": {}, "october": {},
	"nov": {}, "november": {}, "dec": {}, "december": {},
}

func hasDatedSportsLabel(value string) bool {
	for _, token := range sportsTokens(value) {
		if _, month := sportsMonthTokens[token]; month {
			return true
		}
		if len(token) == 8 || len(token) == 6 || len(token) == 4 {
			allDigits := true
			for _, r := range token {
				if !unicode.IsDigit(r) {
					allDigits = false
					break
				}
			}
			if allDigits && len(token) >= 4 {
				return true
			}
		}
	}
	return false
}

func isPermanentAutoLinkChannel(channel LiveChannel) bool {
	for _, value := range []string{channel.Name, channel.TvgName} {
		if sportsLifecycle(value) != "" || containsMatchupMarker(value) || hasNegativeSportsLabel(value) || hasDatedSportsLabel(value) {
			return false
		}
	}
	return true
}

// buildChannelKeyIndex is hoisted out of autoLinkCandidatesForTeam by its caller
// (evaluateSportsAutoLinks) and built once per request rather than once per team - a
// league's auto-link preview calls autoLinkCandidatesForTeam once per team (e.g. 30x for
// MLB), and rebuilding a channels-sized map on every call was pure wasted work at large
// playlist sizes.
func buildChannelKeyIndex(channels []LiveChannel) map[string]LiveChannel {
	index := make(map[string]LiveChannel, len(channels))
	for _, channel := range channels {
		index[channel.SourceID+":"+channel.ID] = channel
	}
	return index
}

func autoLinkCandidatesForTeam(team models.SportsTeamRecord, channels []LiveChannel, channelByKey map[string]LiveChannel) []models.SportsChannelCandidate {
	ranked := rankTeamChannelCandidates(team, channels, "", 0)
	results := make([]models.SportsChannelCandidate, 0, len(ranked))
	seenBrands := make(map[string]struct{})
	for _, candidate := range ranked {
		channel, ok := channelByKey[candidate.SourceID+":"+candidate.ChannelID]
		if !ok || !isPermanentAutoLinkChannel(channel) {
			continue
		}
		if candidate.MatchReason == "Team location only" || candidate.MatchReason == "Manual text match" {
			continue
		}
		brand := normalizeSportsText(candidate.ChannelName)
		if brand == "" {
			brand = candidate.SourceID + ":" + candidate.ChannelID
		}
		if _, exists := seenBrands[brand]; exists {
			continue
		}
		seenBrands[brand] = struct{}{}
		results = append(results, candidate)
	}
	return results
}

// PPV and series can describe live sports; exclude only explicit non-live content here.
func hasNonLiveSportsLabel(value string) bool {
	for token := range tokenSet(value) {
		switch token {
		case "replay", "archive", "classic", "movie", "movies", "ondemand", "vod":
			return true
		}
	}
	return false
}
