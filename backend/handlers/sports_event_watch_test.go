package handlers

import (
	"novastream/models"
	"novastream/services/sports"
	"testing"
	"time"
)

func TestRaceStreamIdentity(t *testing.T) {
	events := []models.SportsEvent{{ID: "f1:weekend", Title: "Monaco Grand Prix", League: "f1", SubEvents: []models.SportsEvent{{ID: "f1:weekend:race", SessionType: "Race", Status: "live"}, {ID: "f1:weekend:qualifying", SessionType: "Qualifying"}}}}
	game, ok := raceStreamEvent(events, "f1:weekend:race", "f1:weekend")
	if !ok || game.Title != "Monaco Grand Prix" || game.EventContext != "Race" {
		t.Fatal(game, ok)
	}
	if _, ok := raceStreamEvent(events, "f1:weekend:race", "untrusted"); ok {
		t.Fatal("accepted wrong parent")
	}
	events[0].Stale = true
	if _, ok := raceStreamEvent(events, "f1:weekend:race", "f1:weekend"); ok {
		t.Fatal("accepted stale feed")
	}
}
func TestCyclingStreamIdentity(t *testing.T) {
	races := []sports.CyclingRace{{ID: "aso:tour:2026", Name: "Tour de France", Stages: []sports.CyclingStage{{ID: "aso:tour:2026:3", Name: "Stage 3"}}}}
	game, ok := cyclingStreamEvent(races, "aso:tour:2026:3", "aso:tour:2026")
	if !ok || !game.StartTime.IsZero() || game.EventContext != "Stage 3" {
		t.Fatal(game, ok)
	}
	races[0].Stages[0].Status = "cancelled"
	if _, ok := cyclingStreamEvent(races, "aso:tour:2026:3", "aso:tour:2026"); ok {
		t.Fatal("accepted cancelled stage")
	}
}
func TestNonMatchStreamCandidates(t *testing.T) {
	race := models.SportsGame{Title: "Monaco Grand Prix", League: "f1", EventKind: "race-session", EventContext: "Race"}
	cycle := models.SportsGame{Title: "Tour de France", League: "cycling", EventKind: "cycling-stage", EventContext: "Stage 3"}
	for _, tc := range []struct {
		name  string
		game  models.SportsGame
		value string
		want  bool
	}{
		{"race", race, "F1 Monaco Grand Prix Race 1080p", true},
		{"qualifying conflict", race, "F1 Monaco Grand Prix Qualifying", false},
		{"different race", race, "F1 Italian Grand Prix", false},
		{"different series", race, "MotoGP Monaco Grand Prix", false},
		{"generic channel", race, "Sky Sports F1", false},
		{"stage", cycle, "Cycling Tour de France Stage 3", true},
		{"wrong stage", cycle, "Tour de France Stage 4", false},
		{"wrong competition", cycle, "Tour de France Femmes Stage 3", false},
		{"unrelated france", cycle, "France news", false},
		{"race only selection", cycle, "Tour de France Live", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := scoreWatchEvent(tc.value, tc.game)
			if (e.score > 0) != tc.want {
				t.Fatalf("%+v", e)
			}
			if e.score >= strongSportsConfidence {
				t.Fatal("must require explicit stream selection")
			}
		})
	}
	for _, source := range []string{"iptv", "addon"} {
		matches := selectableSportsMatches(matchGameToChannels(race, []LiveChannel{{ID: "a", Name: "F1 Monaco Grand Prix Race", URL: "https://example.com/live", SourceID: source}, {ID: "b", Name: "F1 Italian Grand Prix", URL: "https://example.com/wrong", SourceID: source}}, nil, ""))
		if len(matches) != 1 || matches[0].SourceID != source {
			t.Fatalf("%s: %+v", source, matches)
		}
	}
}

type watchEPG struct{ program *models.EPGProgram }

func (e watchEPG) GetNowPlaying(_ []string, _ ...time.Duration) []models.EPGNowPlaying {
	return []models.EPGNowPlaying{{ChannelID: "race", Current: e.program}}
}
func TestRaceStreamRejectsContradictoryCurrentProgram(t *testing.T) {
	game := models.SportsGame{Title: "Monaco Grand Prix", League: "f1", EventKind: "race-session", EventContext: "Race"}
	channels := []LiveChannel{{ID: "one", TvgID: "race", Name: "F1 Monaco Grand Prix Race", URL: "https://example.com/live"}}
	matches := matchGameToChannels(game, channels, watchEPG{&models.EPGProgram{Title: "F1 Monaco Grand Prix Qualifying"}}, "")
	if len(matches) != 0 {
		t.Fatalf("wrong session promoted: %+v", matches)
	}
	matches = matchGameToChannels(game, channels, watchEPG{&models.EPGProgram{Title: "F1 Monaco Grand Prix Race"}}, "")
	if len(matches) != 1 || matches[0].Confidence >= strongSportsConfidence {
		t.Fatalf("expected manual candidate: %+v", matches)
	}
	practice := game
	practice.EventContext = "Free Practice 1"
	if scoreWatchEvent("F1 Monaco Grand Prix FP2", practice).score > 0 {
		t.Fatal("wrong practice session")
	}
}

func TestCyclingCalendarStreamIdentity(t *testing.T) {
	races := []sports.CyclingRace{{ID: "calendar:road-abc:2026", Name: "Tour de Langkawi", CalendarOnly: true, Stages: []sports.CyclingStage{{ID: "calendar:road-abc:2026:1", Name: "Race schedule", Status: "scheduled"}}}}
	game, ok := cyclingStreamEvent(races, "calendar:road-abc:2026:1", "calendar:road-abc:2026")
	if !ok || !game.StartTime.IsZero() || game.Title != "Tour de Langkawi" {
		t.Fatal(game, ok)
	}
	if scoreWatchEvent("Cycling Tour de Langkawi Stage 2 Live", game).score == 0 {
		t.Fatal("calendar event cannot find stage broadcasts")
	}
	if _, ok := cyclingStreamEvent(races, "calendar:road-forged:2026:1", "calendar:road-abc:2026"); ok {
		t.Fatal("accepted unknown stream target")
	}
}

func TestNuvioF1SessionMatching(t *testing.T) {
	game := models.SportsGame{Title: "Qatar Airways Azerbaijan Grand Prix", League: "f1", EventKind: "race-session", EventContext: "FP2"}
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"🔴 LIVE: Motorsports - Azerbaijan Grand Prix - Practice 2", true},
		{"🔴 LIVE: Qatar Airways Azerbaijan Grand Prix - FP2", true},
		{"🔴 LIVE: Azerbaijan Grand Prix - Practice 2", true},
		{"🔴 LIVE: Qatar Airways Azerbaijan GP - Practice 2", true},
		{"🔴 LIVE: Azerbaijan Grand Prix - Practice 1", false},
		{"🔴 LIVE: Azerbaijan Grand Prix - Qualifying", false},
		{"🔴 LIVE: Formula 2 Azerbaijan Grand Prix - Practice 2", false},
		{"🔴 LIVE: MotoGP Azerbaijan Grand Prix - Practice 2", false},
		{"🔴 LIVE: Qatar Grand Prix - Practice 2", false},
		{"🔴 LIVE: Azerbaijan Grand Prix", false},
		{"Replay: Azerbaijan Grand Prix - Practice 2", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := selectableSportsMatches(matchGameToChannels(game, []LiveChannel{{ID: "nuvio", Name: tc.name, URL: "https://addon.test/stream/tv/event.json"}}, nil, ""))
			if (len(got) > 0) != tc.want {
				t.Fatalf("matches=%+v, want match=%v", got, tc.want)
			}
			for _, m := range got {
				if m.Confidence >= strongSportsConfidence {
					t.Fatal("must require manual selection")
				}
			}
		})
	}
}

func TestRaceStreamQualAbbreviation(t *testing.T) {
	game := models.SportsGame{Title: "Qatar Airways Azerbaijan Grand Prix", League: "f1", EventKind: "race-session", EventContext: "Qual"}
	if scoreWatchEvent("LIVE: Azerbaijan Grand Prix - Qualifying", game).score == 0 {
		t.Fatal("ESPN Qual abbreviation did not match")
	}
	if scoreWatchEvent("F1 Azerbaijan Grand Prix - Race", game).score != 0 {
		t.Fatal("wrong session matched Qual")
	}
}

func TestNamedEventsWithoutSportLabels(t *testing.T) {
	for _, tc := range []struct {
		name, league, sport, kind, title, session, channel string
		want                                               bool
	}{
		{"motogp", "motogp", "racing", "race-session", "Thailand Grand Prix", "Race", "Thailand GP - Race", true},
		{"nascar", "nascar", "racing", "race-session", "NASCAR Hollywood Casino 400", "Race", "Hollywood Casino 400 - Race", true},
		{"nascar secondary", "espn:racing:nascar-secondary", "racing", "race-session", "NASCAR Hollywood Casino 400", "Race", "NASCAR Hollywood Casino 400", true},
		{"nascar trucks", "espn:racing:nascar-truck", "racing", "race-session", "NASCAR UNOH 200", "Race", "NASCAR UNOH 200", true},
		{"indycar", "indycar", "racing", "race-session", "IndyCar Long Beach Grand Prix", "Qualifying", "Long Beach GP - Qualifying", true},
		{"wrong series", "motogp", "racing", "race-session", "Thailand Grand Prix", "Race", "Formula 1 Thailand Grand Prix - Race", false},
		{"generic race", "nascar", "racing", "race-session", "NASCAR Race", "Race", "Live Race", false},
		{"wrong numbered race", "nascar", "racing", "race-session", "NASCAR Hollywood Casino 400", "Race", "Hollywood Casino 500 - Race", false},
		{"single named golf", "pga", "golf", "tournament", "PGA Tour The Masters", "", "The Masters - Round 2", true},
		{"golf event", "pga", "golf", "tournament", "Golf Ryder Cup", "", "Ryder Cup Live", true},
		{"different golf event", "pga", "golf", "tournament", "PGA Tour BMW Championship", "", "PGA Tour Championship", false},
		{"partial golf identity", "pga", "golf", "tournament", "Farmers Insurance Open", "", "Farmers Insurance News", false},
		{"generic event", "pga", "golf", "tournament", "Golf World Championship", "", "World Championship Live", false},
		{"boxing", "boxing", "boxing", "fight-card", "Boxing: Canelo Alvarez vs Terence Crawford", "", "Canelo Alvarez vs Terence Crawford", true},
		{"one fighter", "boxing", "boxing", "fight-card", "Boxing: Canelo Alvarez vs Terence Crawford", "", "Canelo Alvarez vs Another Boxer", false},
		{"cycling", "cycling", "cycling", "cycling-stage", "Cycling Tour de France", "Stage 3", "Tour de France Stage 3", true},
		{"wrong stage", "cycling", "cycling", "cycling-stage", "Cycling Tour de France", "Stage 3", "Tour de France Stage 4", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			game := models.SportsGame{League: tc.league, Sport: tc.sport, EventKind: tc.kind, Title: tc.title, EventContext: tc.session}
			got := selectableSportsMatches(matchGameToChannels(game, []LiveChannel{{ID: "event", Name: tc.channel, URL: "https://example.test/live"}}, nil, ""))
			if (len(got) > 0) != tc.want {
				t.Fatalf("matches=%+v want=%v", got, tc.want)
			}
		})
	}
}

func TestNamedTournamentRejectsOtherSportMetadata(t *testing.T) {
	game := models.SportsGame{Title: "PGA Tour The Masters", League: "pga", Sport: "golf", EventKind: "tournament"}
	channels := []LiveChannel{
		{ID: "golf", Name: "The Masters", SportsMetadata: "Category: Golf", URL: "https://example.test/golf"},
		{ID: "snooker", Name: "The Masters", SportsMetadata: "Category: Snooker", URL: "https://example.test/snooker"},
	}
	got := selectableSportsMatches(matchGameToChannels(game, channels, nil, ""))
	if len(got) != 1 || got[0].ChannelID != "golf" {
		t.Fatalf("wrong sport matched: %+v", got)
	}
}
