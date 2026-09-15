package handlers

import (
	"testing"
	"time"

	"novastream/models"
)

func testTeamRecord(name, location, nickname, abbreviation string) models.SportsTeamRecord {
	return models.SportsTeamRecord{
		ID: "mlb:test", League: "mlb", Name: name, Location: location,
		Nickname: nickname, Abbreviation: abbreviation,
	}
}

func TestNormalizeSportsText(t *testing.T) {
	got := normalizeSportsText("  Montréal_Canadiens-vs.N.Y.  ")
	if got != "montreal canadiens vs n y" {
		t.Fatalf("normalizeSportsText() = %q", got)
	}
}

func TestWholeTokenBroadcastMatching(t *testing.T) {
	tests := []struct {
		name      string
		channel   string
		broadcast string
		wantMatch bool
		wantTier  string
	}{
		{name: "yes network", channel: "YES Network HD", broadcast: "YES", wantMatch: true, wantTier: "strong"},
		{name: "no substring", channel: "Yesterday Classics", broadcast: "YES", wantMatch: false},
		{name: "nesn", channel: "NESN", broadcast: "NESN", wantMatch: true, wantTier: "strong"},
		{name: "peacock movie stays possible", channel: "Peacock Movies VOD", broadcast: "Peacock", wantMatch: true, wantTier: "possible"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evidence := scoreBroadcastText(tt.channel, tt.broadcast)
			if (evidence.score > 0) != tt.wantMatch {
				t.Fatalf("score = %.2f, want match=%v", evidence.score, tt.wantMatch)
			}
			if tt.wantMatch && confidenceTier(evidence.score) != tt.wantTier {
				t.Fatalf("tier = %q, want %q (score %.2f)", confidenceTier(evidence.score), tt.wantTier, evidence.score)
			}
		})
	}
}

func TestESPNBroadcastAliasCatalog(t *testing.T) {
	tests := []struct{ broadcast, channel string }{
		{"ESPN Unlmtd", "ESPN Unlimited HD"},
		{"NBC Sports CA", "NBCSCA"},
		{"NBC Sports Phil", "NBC Sports Philadelphia"},
		{"SportsNet LA", "Spectrum SportsNet LA"},
		{"CHSN", "Chicago Sports Network"},
		{"MASN", "Mid Atlantic Sports Network"},
		{"MLB.TV", "MLBTV"},
		{"Angels.TV", "AngelsTV"},
		{"CLEGuardians.TV", "Cleveland Guardians TV"},
		{"TVA", "TVA Sports"},
	}
	for _, tt := range tests {
		t.Run(tt.broadcast+"_"+tt.channel, func(t *testing.T) {
			if evidence := scoreBroadcastText(tt.channel, tt.broadcast); evidence.score <= 0 {
				t.Fatalf("%q did not match ESPN label %q", tt.channel, tt.broadcast)
			}
		})
	}
	for _, unsafe := range []struct{ channel, broadcast string }{
		{"Yesterday Classics", "YES"},
		{"Turner RSN Documentary", "Rangers Sports Network"},
		{"Sports News", "Sportsnet"},
	} {
		if evidence := scoreBroadcastText(unsafe.channel, unsafe.broadcast); evidence.score > 0 {
			t.Fatalf("unsafe substring %q matched %q at %.2f", unsafe.channel, unsafe.broadcast, evidence.score)
		}
	}
}

func TestSportsLifecycleFiltering(t *testing.T) {
	if got := sportsLifecycle("End | Phillies at Angels"); got != sportsLifecycleEnded {
		t.Fatalf("End lifecycle = %q", got)
	}
	if got := sportsLifecycle("Off Air: Phillies at Angels"); got != sportsLifecycleEnded {
		t.Fatalf("Off Air lifecycle = %q", got)
	}
	if got := sportsLifecycle("Live | Phillies at Angels"); got != sportsLifecycleLive {
		t.Fatalf("Live lifecycle = %q", got)
	}
	if got := sportsLifecycle("Weekend Baseball"); got != "" {
		t.Fatalf("Weekend incorrectly marked %q", got)
	}
	if evidence := scoreBroadcastText("End | Peacock 001", "Peacock"); evidence.score != 0 {
		t.Fatalf("ended Peacock score = %.2f", evidence.score)
	}
}

func TestTeamChannelSuggestionScores(t *testing.T) {
	team := testTeamRecord("Washington Nationals", "Washington", "Nationals", "WSH")
	channels := []LiveChannel{
		{ID: "dedicated", Name: "MLB Washington Nationals HD", URL: "https://example/dedicated"},
		{ID: "matchup", Name: "Mets vs Nationals", URL: "https://example/matchup"},
		{ID: "other-team", Name: "Washington Capitals", URL: "https://example/capitals"},
	}
	got := rankTeamChannelCandidates(team, channels, "", 100)
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2: %+v", len(got), got)
	}
	if got[0].ChannelID != "dedicated" || got[0].ConfidenceTier != "strong" {
		t.Fatalf("first candidate = %+v, want strong dedicated channel", got[0])
	}
	if got[1].ChannelID != "matchup" || got[1].ConfidenceTier != "possible" || got[1].Confidence >= 0.60 {
		t.Fatalf("second candidate = %+v, want sub-0.60 possible matchup", got[1])
	}
}

func TestSelectableSportsMatches(t *testing.T) {
	matches := []models.SportsStreamMatch{
		{ChannelID: "strong", ChannelURL: "https://strong", Confidence: 0.85, ConfidenceTier: "strong"},
		{ChannelID: "possible", ChannelURL: "https://possible", Confidence: 0.74, ConfidenceTier: "possible"},
		{ChannelID: "weak", ChannelURL: "https://weak", Confidence: 0.42, ConfidenceTier: "possible"},
		{ChannelID: "empty", Confidence: 0.98, ConfidenceTier: "strong"},
	}
	got := selectableSportsMatches(matches)
	if len(got) != 2 || got[0].ChannelID != "strong" || got[1].ChannelID != "possible" {
		t.Fatalf("selectable matches = %#v", got)
	}
}

func TestManualSearchKeepsZeroConfidenceResult(t *testing.T) {
	team := testTeamRecord("Washington Nationals", "Washington", "Nationals", "WSH")
	channels := []LiveChannel{{ID: "manual", Name: "My Custom Baseball Feed", URL: "https://example/manual"}}
	got := rankTeamChannelCandidates(team, channels, "custom", 100)
	if len(got) != 1 || got[0].Confidence != 0 || got[0].ConfidenceTier != "possible" {
		t.Fatalf("manual result = %+v, want visible 0%% possible match", got)
	}
}

func TestMatchupVariantsAndShortCodes(t *testing.T) {
	home := makeSportsIdentity("nfl", "Kansas City Chiefs", "Kansas City", "Chiefs", "KC")
	away := makeSportsIdentity("nfl", "Baltimore Ravens", "Baltimore", "Ravens", "BAL")
	for _, title := range []string{"Kansas City vs Baltimore", "Ravens @ Chiefs", "Baltimore Kansas City", "KC vs BAL", "KC vs Ravens", "BAL @ Chiefs"} {
		if evidence := scoreMatchupText(title, home, away); evidence.score < strongSportsConfidence {
			t.Fatalf("%q score = %.2f, want strong matchup", title, evidence.score)
		}
	}
	if evidence := scoreTextForTeam("Tennessee Sports", home, false); evidence.score != 0 {
		t.Fatalf("KC/short-code false positive score = %.2f", evidence.score)
	}
	newEngland := makeSportsIdentity("nfl", "New England Patriots", "New England", "Patriots", "NE")
	if evidence := scoreTextForTeam("Tennessee Sports", newEngland, false); evidence.score != 0 {
		t.Fatalf("NE matched inside Tennessee with score %.2f", evidence.score)
	}
}

func TestMatchupRequiresBothTeamsInOneSegment(t *testing.T) {
	home := makeSportsIdentity("soccer", "United States", "United States", "United States", "USA")
	away := makeSportsIdentity("soccer", "Australia", "Australia", "Australia", "AUS")
	evidence := scoreMatchupText("USA Soccer07: Australia vs Turkey", home, away)
	if evidence.score >= strongSportsConfidence {
		t.Fatalf("cross-segment evidence = %.2f, want non-strong", evidence.score)
	}
}

func TestStreamGroupingPreservesDistinctFeeds(t *testing.T) {
	matches := []models.SportsStreamMatch{
		{ChannelID: "1", ChannelName: "Live | Phillies at Angels HD", ChannelURL: "https://one", Confidence: .94, ConfidenceTier: "strong", MatchReason: "Both team nicknames"},
		{ChannelID: "2", ChannelName: "Phillies at Angels FHD", ChannelURL: "https://two", Confidence: .94, ConfidenceTier: "strong", MatchReason: "Both team nicknames"},
		{ChannelID: "3", ChannelName: "Phillies at Angels FHD", ChannelURL: "https://two", Confidence: .94, ConfidenceTier: "strong", MatchReason: "Both team nicknames"},
	}
	flat, groups := buildSportsStreamGroups(matches)
	if len(flat) != 2 {
		t.Fatalf("flat len = %d, want exact URL dedupe to 2", len(flat))
	}
	if len(groups) != 1 || groups[0].FeedCount != 2 || len(groups[0].Alternatives) != 1 {
		t.Fatalf("groups = %+v, want one group with two feeds", groups)
	}
}

func TestConservativeAutoLinkEvaluation(t *testing.T) {
	teams := []models.SportsTeamRecord{
		testTeamRecord("Washington Nationals", "Washington", "Nationals", "WSH"),
		{ID: "mlb:yankees", League: "mlb", Name: "New York Yankees", Location: "New York", Nickname: "Yankees", Abbreviation: "NYY"},
	}
	teams[0].ID = "mlb:nationals"
	channels := []LiveChannel{
		{ID: "nationals", SourceID: "one", Name: "Washington Nationals", URL: "https://nationals"},
		{ID: "ended", SourceID: "one", Name: "End | Washington Nationals", URL: "https://ended"},
		{ID: "yanks", SourceID: "one", Name: "Yanks Network", URL: "https://yanks"},
	}
	evaluation := evaluateSportsAutoLinks("mlb", teams, map[string][]models.SportsTeamChannelLink{}, channels)
	if evaluation.preview.EligibleCount != 2 || len(evaluation.links) != 2 {
		t.Fatalf("evaluation = %+v, want two eligible permanent links", evaluation.preview)
	}
	if evaluation.links[0].ChannelName == "End | Washington Nationals" {
		t.Fatal("ended feed was auto-linked")
	}

	existing := map[string][]models.SportsTeamChannelLink{
		"mlb:nationals": {{TeamID: "mlb:nationals", Slot: models.SportsLinkSlotPrimary}},
	}
	evaluation = evaluateSportsAutoLinks("mlb", teams, existing, channels)
	if evaluation.preview.EligibleCount != 1 || evaluation.preview.Results[0].SkipReason != "Primary channel already linked" {
		t.Fatalf("existing primary was not protected: %+v", evaluation.preview)
	}
}

type sportsMatcherEPG struct {
	items []models.EPGNowPlaying
}

func (f sportsMatcherEPG) GetNowPlaying(_ []string, _ ...time.Duration) []models.EPGNowPlaying {
	return f.items
}

func TestGameEPGPromotesNetworkMatch(t *testing.T) {
	game := models.SportsGame{
		League:   "mlb",
		HomeTeam: models.SportsTeam{Name: "Washington Nationals", Location: "Washington", Nickname: "Nationals", Abbreviation: "WSH"},
		AwayTeam: models.SportsTeam{Name: "New York Mets", Location: "New York", Nickname: "Mets", Abbreviation: "NYM"},
	}
	program := models.EPGProgram{Title: "New York Mets at Washington Nationals"}
	channels := []LiveChannel{{ID: "yes", Name: "YES Network", URL: "https://example/yes", TvgID: "yes.us"}}
	matches := matchGameToChannels(game, channels, sportsMatcherEPG{items: []models.EPGNowPlaying{
		{ChannelID: "yes.us", Next: &program},
	}}, "YES")
	if len(matches) != 1 || matches[0].Confidence != 0.99 || matches[0].ConfidenceTier != "strong" {
		t.Fatalf("matches = %+v, want 99%% EPG-promoted YES match", matches)
	}
}

func matchingFixtureGame() models.SportsGame {
	return models.SportsGame{
		League: "mlb", StartTime: time.Date(2026, 9, 15, 19, 0, 0, 0, time.UTC),
		HomeTeam: models.SportsTeam{Name: "Toronto Blue Jays", Location: "Toronto", Nickname: "Blue Jays", Abbreviation: "TOR"},
		AwayTeam: models.SportsTeam{Name: "Detroit Tigers", Location: "Detroit", Nickname: "Tigers", Abbreviation: "DET"},
	}
}

func TestEventStreamRecallAndFalsePositives(t *testing.T) {
	game := matchingFixtureGame()
	for _, tc := range []struct{ name, tier string }{
		{"DET vs TOR HD", "strong"}, {"TOR @ DET", "strong"},
		{"Detroit Tigers", "possible"}, {"Tigers vs TOR", "strong"},
		{"DET vs Jays", "possible"}, {"Jays vs Tigers", "strong"},
		{"Detroit", ""}, {"DETOUR vs TORRENT news", ""},
		{"Replay | Detroit Tigers at Toronto Blue Jays", ""},
		{"End | DET vs TOR", ""}, {"World Series DET vs TOR", "strong"},
		{"Washington Nationals", ""}, {"Detroit Tigers vs Chicago Cubs", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := selectableSportsMatches(matchGameToChannels(game, []LiveChannel{{ID: "feed", Name: tc.name, URL: "https://example.test/live"}}, nil, ""))
			if tc.tier == "" {
				if len(got) != 0 {
					t.Fatalf("unexpected candidates: %+v", got)
				}
			} else if len(got) != 1 || got[0].ConfidenceTier != tc.tier {
				t.Fatalf("got %+v, want %s", got, tc.tier)
			}
		})
	}
}

func TestEventStreamEPGDescriptionAndTime(t *testing.T) {
	game := matchingFixtureGame()
	for _, tc := range []struct {
		name, title, description, tier string
		offset                         time.Duration
		noTime                         bool
	}{
		{name: "description nearby", title: "MLB Baseball", description: "Detroit Tigers at Toronto Blue Jays", tier: "strong"},
		{name: "description without time", title: "MLB Baseball", description: "Detroit Tigers at Toronto Blue Jays", tier: "possible", noTime: true},
		{name: "time mismatch", title: "Detroit Tigers at Toronto Blue Jays", tier: "possible", offset: 4 * time.Hour},
		{name: "wrong day", title: "Detroit Tigers at Toronto Blue Jays", offset: 24 * time.Hour},
		{name: "replay", title: "MLB Replay", description: "Detroit Tigers at Toronto Blue Jays"},
		{name: "unrelated", title: "MLB Baseball", description: "Washington Nationals at New York Mets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := game.StartTime.Add(tc.offset)
			if tc.noTime {
				start = time.Time{}
			}
			epg := sportsMatcherEPG{items: []models.EPGNowPlaying{{ChannelID: "network", Current: &models.EPGProgram{Title: tc.title, Description: tc.description, Start: start}}}}
			got := selectableSportsMatches(matchGameToChannels(game, []LiveChannel{{ID: "network", Name: "Network HD", URL: "https://example.test/live", TvgID: "network"}}, epg, ""))
			if tc.tier == "" {
				if len(got) != 0 {
					t.Fatalf("unexpected candidates: %+v", got)
				}
			} else if len(got) != 1 || got[0].ConfidenceTier != tc.tier {
				t.Fatalf("got %+v, want %s", got, tc.tier)
			}
		})
	}
}

func TestPossibleFeedsDoNotInheritStrongConfidence(t *testing.T) {
	_, groups := buildSportsStreamGroups([]models.SportsStreamMatch{
		{ChannelName: "Game HD", ChannelURL: "https://example.test/strong", Confidence: .98, ConfidenceTier: "strong"},
		{ChannelName: "Game FHD", ChannelURL: "https://example.test/possible", Confidence: .74, ConfidenceTier: "possible"},
	})
	if len(groups) != 2 || groups[1].ConfidenceTier != "possible" || len(groups[0].Alternatives) != 0 {
		t.Fatalf("confidence was lost when grouping: %+v", groups)
	}
}

func BenchmarkSportsMatchupRecall(b *testing.B) {
	game := matchingFixtureGame()
	home := identityFromTeam(game.HomeTeam, "mlb")
	away := identityFromTeam(game.AwayTeam, "mlb")
	names := []string{"Detroit Tigers vs Toronto Blue Jays", "Tigrs vs Blue Jays", "Tigrs vs Yankees", "Other network HD", "Boston Red Sox vs New York Yankees"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for j := 0; j < 10000; j++ {
			scoreMatchupText(names[j%len(names)], home, away)
		}
	}
}
func TestSportsFuzzyMatchup(t *testing.T) {
	home := identityFromTeam(matchingFixtureGame().HomeTeam, "mlb")
	away := identityFromTeam(matchingFixtureGame().AwayTeam, "mlb")
	for _, tc := range []struct {
		title    string
		possible bool
	}{
		{"Tigres vs Blue Jays", false}, // Transposition is two edits, deliberately unsupported.
		{"Tigrs vs Blue Jays", true}, {"Blue Jays @ Tigerrs", true},
		{"Tigrs vs Yankees", false}, {"Tigrs", false}, {"Detroit vs Toronto", false},
		{"Tigrs vs Blue Jays vs Yankees", false}, {"Tigrs vs Blue Jays replay", false},
	} {
		got := scoreFuzzySportsMatchup(tc.title, home, away)
		if (got.score >= .65) != tc.possible || got.score >= strongSportsConfidence {
			t.Errorf("%q: %+v", tc.title, got)
		}
	}
	if got := scoreMatchupText("Tigrs vs Blue Jays", home, away); got.score < .65 || got.score >= strongSportsConfidence {
		t.Fatalf("fallback not integrated: %+v", got)
	}
}
