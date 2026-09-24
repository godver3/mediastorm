package handlers

import (
	"context"
	"net/http"
	"novastream/models"
	"novastream/services/sports"
	"regexp"
	"strings"
)

// Resolve identifiers against provider-owned feeds, never client-supplied titles.
func (h *SportsHandler) streamEvent(ctx context.Context, id, kind, parentID string) (models.SportsGame, bool) {
	if kind == "" {
		return h.service.GetGame(id)
	}
	if kind == "racing" {
		for _, prefix := range []string{"f1:", "nascar:", "indycar:", "motogp:", "espn:racing:nascar-secondary:", "espn:racing:nascar-truck:"} {
			if strings.HasPrefix(id, prefix) {
				return raceStreamEvent(h.service.GetRaceBoard(ctx).Events, id, parentID)
			}
		}
	}
	if kind == "cycling" && (strings.HasPrefix(id, "aso:") || strings.HasPrefix(id, "rcs:") || strings.HasPrefix(id, "cro:") || strings.HasPrefix(id, "uci:") || strings.HasPrefix(id, "calendar:")) {
		return cyclingStreamEvent(h.service.GetCycling(ctx).Data, id, parentID)
	}
	return models.SportsGame{}, false
}
func raceStreamEvent(events []models.SportsEvent, id, parentID string) (models.SportsGame, bool) {
	for _, parent := range events {
		if parent.ID != parentID {
			continue
		}
		sessions := parent.SubEvents
		if parent.EventKind == "race-session" {
			sessions = []models.SportsEvent{parent}
		}
		for _, session := range sessions {
			if session.ID != id || parent.Stale || session.Stale {
				continue
			}
			return models.SportsGame{ID: id, Title: parent.Title, EventContext: session.SessionType, League: parent.League, Sport: "racing", EventKind: "race-session", StartTime: session.StartTime, Status: session.Status}, true
		}
	}
	return models.SportsGame{}, false
}
func cyclingStreamEvent(races []sports.CyclingRace, id, parentID string) (models.SportsGame, bool) {
	for _, race := range races {
		if race.ID != parentID {
			continue
		}
		for _, stage := range race.Stages {
			if stage.ID != id || stage.Status == "cancelled" {
				continue
			}
			// Date-only stages deliberately have no invented start time.
			return models.SportsGame{ID: id, Title: race.Name, EventContext: stage.Name, League: "cycling", Sport: "cycling", EventKind: "cycling-stage", Status: models.SportsGameStatus("scheduled")}, true
		}
	}
	return models.SportsGame{}, false
}

var watchCardNumber = regexp.MustCompile(`^[0-9]{1,4}$`)
var watchYear = regexp.MustCompile(`^20[0-9]{2}$`)
var watchStage = regexp.MustCompile(`(?i)\b(?:stage|etape|étape)\s*([0-9]+)\b`)

// Keep sponsor normalization explicit: arbitrary prefix dropping could turn
// the Azerbaijan event sponsored by Qatar Airways into the Qatar Grand Prix.
func watchRaceTitle(value string) string {
	tokens := sportsTokens(value)
	if len(tokens) >= 2 && tokens[0] == "qatar" && tokens[1] == "airways" {
		tokens = tokens[2:]
	}
	var expanded []string
	for _, token := range tokens {
		if token == "gp" {
			expanded = append(expanded, "grand", "prix")
		} else {
			expanded = append(expanded, token)
		}
	}
	return strings.Join(expanded, " ")
}

var watchSeriesPattern = regexp.MustCompile(`(?i)\b(f[123]|formula\s*(?:[123]|one|two|three|e)|motogp|moto2|moto3|nascar|indycar)\b`)

func conflictingWatchSeries(value, league string) bool {
	if strings.HasPrefix(league, "espn:racing:nascar-") {
		league = "nascar"
	}
	for _, raw := range watchSeriesPattern.FindAllString(value, -1) {
		series := strings.ReplaceAll(strings.ToLower(raw), " ", "")
		switch series {
		case "formula1", "formulaone":
			series = "f1"
		case "formula2", "formulatwo":
			series = "f2"
		case "formula3", "formulathree":
			series = "f3"
		}
		if series != league {
			return true
		}
	}
	return false
}

var watchPractice = regexp.MustCompile(`(?i)\b(?:fp|(?:free\s+)?practice\s*)([1-3])\b`)

func watchSession(value string) string {
	v := strings.ToLower(value)
	if m := watchPractice.FindStringSubmatch(v); len(m) > 1 {
		return "practice" + m[1]
	}
	if v == "qual" {
		return "qualifying"
	}
	for _, name := range []string{"qualifying", "practice", "sprint", "race"} {
		if strings.Contains(v, name) {
			return name
		}
	}
	return ""
}
func conflictingWatchSegment(value string, game models.SportsGame) bool {
	if game.EventKind == "race-session" {
		actual, wanted := watchSession(value), watchSession(game.EventContext)
		if actual == "" || wanted == "" {
			return false
		}
		a, w := strings.TrimRight(actual, "123"), strings.TrimRight(wanted, "123")
		return a != w || (actual != a && wanted != w && actual != wanted)
	}
	if game.EventKind == "cycling-stage" {
		expected, actual := watchStage.FindStringSubmatch(game.EventContext), watchStage.FindStringSubmatch(value)
		return len(actual) > 1 && len(expected) > 1 && actual[1] != expected[1]
	}
	return false
}

// Only use unambiguous sport words as exclusions. Generic "Motorsport" addon
// categories can also contain cycling, and are not reliable series evidence.
func conflictingNamedEventSport(value string, game models.SportsGame) bool {
	if game.EventKind != "tournament" && game.EventKind != "fight-card" {
		return false
	}
	wanted := game.Sport
	if wanted == "" {
		wanted = map[string]string{"pga": "golf", "ufc": "mma", "boxing": "boxing"}[game.League]
	}
	if wanted == "" {
		return false
	}
	for _, token := range sportsTokens(value) {
		switch token {
		case "golf", "tennis", "snooker", "darts", "boxing", "mma", "cycling", "basketball", "baseball", "cricket", "hockey":
			if token != wanted {
				return true
			}
		}
	}
	return false
}

// Strip only known leading category labels, not arbitrary sponsor/event words.
func watchEventTitle(title string, game models.SportsGame) string {
	title = normalizeSportsText(title)
	labels := map[string][]string{
		"f1":     {"formula 1", "formula one", "f1"},
		"motogp": {"motogp"}, "nascar": {"nascar"}, "indycar": {"indycar"},
		"espn:racing:nascar-secondary": {"nascar", "xfinity", "oreilly"}, "espn:racing:nascar-truck": {"nascar", "trucks", "truck"},
		"pga":     {"pga tour", "pga", "golf"},
		"cycling": {"cycling"}, "boxing": {"boxing"}, "ufc": {"mma"},
	}
	for _, label := range labels[game.League] {
		title = strings.TrimSpace(strings.TrimPrefix(title, label+" "))
	}
	return title
}

func scoreWatchEvent(value string, game models.SportsGame) sportsEvidence {
	if conflictingNamedEventSport(value, game) {
		return sportsEvidence{}
	}
	if game.EventKind != "race-session" && game.EventKind != "cycling-stage" {
		// Preserve colon-separated card/bout identities while removing category labels.
		parts := strings.Split(game.Title, ":")
		for i, part := range parts {
			parts[i] = watchEventTitle(part, game)
		}
		evidence := scoreSportsEventTitle(value, strings.Join(parts, ":"))
		if game.EventKind != "fight-card" && evidence.score > 0.78 {
			evidence.score = 0.78
		}
		return evidence
	}
	if conflictingWatchSegment(value, game) {
		return sportsEvidence{}
	}
	title := watchEventTitle(game.Title, game)
	// A race name must remain a phrase: scattered Tour / de / France words
	// also occur in golf's DP World Tour Open de France.
	if game.EventKind == "cycling-stage" && !strings.Contains(" "+normalizeSportsText(value)+" ", " "+normalizeSportsText(title)+" ") {
		return sportsEvidence{}
	}
	if game.EventKind == "race-session" {
		if conflictingWatchSeries(value, game.League) {
			return sportsEvidence{}
		}
		value, title = watchRaceTitle(value), watchRaceTitle(title)
	}
	tokens := sportsTokens(value)
	set := map[string]bool{}
	for _, t := range tokens {
		set[t] = true
	}
	meaningful := 0
	for _, t := range sportsTokens(title) {
		switch t {
		case "the", "and", "de", "d", "a", "championship":
			continue
		}
		if len(t) < 3 || watchYear.MatchString(t) {
			continue
		}
		meaningful++
		if !set[t] {
			return sportsEvidence{}
		}
	}
	generic := strings.ToLower(strings.TrimSpace(title))
	if meaningful == 0 || generic == "race" || generic == "grand prix" || generic == "qualifying" || generic == "practice" {
		return sportsEvidence{}
	}
	if game.EventKind == "race-session" {
		v := normalizeForMatch(value)
		series := map[string][]string{"f1": {"formula1", "formulaone"}, "motogp": {"motogp"}, "nascar": {"nascar"}, "indycar": {"indycar"}, "espn:racing:nascar-secondary": {"nascar", "xfinity", "oreilly"}, "espn:racing:nascar-truck": {"nascar", "craftsman", "trucks"}}
		found := game.League == "f1" && set["f1"]
		for _, alias := range series[game.League] {
			if strings.Contains(v, alias) {
				found = true
			}
		}
		if !found {
			// Addons often omit the series. Require both the specific race
			// identity above and an explicit matching session; never auto-select it.
			actual, wanted := watchSession(value), watchSession(game.EventContext)
			if actual == "" || actual != wanted {
				return sportsEvidence{}
			}
		}

	} else {
		expected, actual := watchStage.FindStringSubmatch(game.EventContext), watchStage.FindStringSubmatch(value)
		if len(actual) > 1 && len(expected) > 1 && actual[1] != expected[1] {
			return sportsEvidence{}
		}
		women := func(v string) bool {
			v = strings.ToLower(v)
			return strings.Contains(v, "femmes") || strings.Contains(v, "femenina") || strings.Contains(v, "women")
		}
		if women(value) != women(game.Title) {
			return sportsEvidence{}
		}
	}
	// Race identity is plausible evidence, not proof of a live broadcast: require selection.
	return sportsEvidence{score: 0.78, reason: "Race / session title match — verify broadcast", terms: sportsTokens(game.Title), on: "event-title"}
}

func streamEventQuery(r *http.Request) (string, string) {
	return r.URL.Query().Get("kind"), r.URL.Query().Get("parentId")
}
