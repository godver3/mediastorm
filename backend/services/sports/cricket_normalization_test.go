package sports

import (
	"encoding/json"
	"os"
	"testing"
)

func TestCricketFourInningsPreserveOwnershipAndOvers(t *testing.T) {
	// Compact contract fixture: Test innings alternate ownership; non-batting
	// rows are placeholders. 18.4 is overs/balls, never 18.4 decimal overs.
	raw := `{"id":"test","date":"2026-09-09T10:00Z","endDate":"2026-09-13T18:00Z","competitions":[{"id":"test","status":{"type":{"state":"in"},"summary":"Day 5: target 156"},"competitors":[{"homeAway":"home","score":"161/5 (18.4 ov, target 156)","team":{"id":"a","displayName":"A"},"linescores":[{"period":1,"isBatting":true,"runs":300,"wickets":10,"overs":90,"isCurrent":0},{"period":2,"isBatting":false,"runs":0},{"period":3,"isBatting":true,"runs":0,"wickets":0,"overs":"0.0","isCurrent":0}]},{"homeAway":"away","score":"155/8","team":{"id":"b","displayName":"B"},"linescores":[{"period":2,"isBatting":true,"runs":250,"wickets":10},{"period":4,"isBatting":true,"runs":161,"wickets":5,"overs":"18.4","target":156,"isCurrent":1}]}]}]}`
	var e espnEvent
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		t.Fatal(err)
	}
	g := scoreboardEventGames(e, League{ID: "test", Sport: "cricket"})[0]
	if len(g.Detail.Innings) != 4 {
		t.Fatalf("lost Test innings: %+v", g.Detail.Innings)
	}
	third, last := g.Detail.Innings[2], g.Detail.Innings[3]
	if third.Runs == nil || *third.Runs != 0 || third.Score != "0/0" {
		t.Fatal("genuine zero lost", third)
	}
	if last.Number != 4 || last.TeamID != "b" || last.Overs != "18.4" || last.Batting == nil || !*last.Batting || last.Target == nil || *last.Target != 156 {
		t.Fatal(last)
	}
	if g.EndTime.IsZero() || g.EndTime.Sub(g.StartTime).Hours() < 96 {
		t.Fatal("multi-day range lost")
	}
}

func TestCricketWinnerDefensiveDecoding(t *testing.T) {
	for _, v := range []string{`true`, `false`, `"true"`, `"false"`, `null`} {
		var c espnCompetitor
		if err := json.Unmarshal([]byte(`{"winner":`+v+`}`), &c); err != nil {
			t.Fatalf("%s: %v", v, err)
		}
		if c.Winner != (v == `true` || v == `"true"`) {
			t.Fatal(v, c.Winner)
		}
	}
	for _, v := range []string{`"yes"`, `1`, `{}`, `[]`} {
		var c espnCompetitor
		if err := json.Unmarshal([]byte(`{"winner":`+v+`}`), &c); err == nil {
			t.Fatalf("malformed winner accepted: %s", v)
		}
	}
}

func TestCricketFinalIsNotCurrentlyBatting(t *testing.T) {
	var c espnCompetition
	if err := json.Unmarshal([]byte(`{"status":{"type":{"state":"post"}},"competitors":[{"score":"161/5 (18/20 ov, target 156)","team":{"id":"a"},"linescores":[{"period":2,"isBatting":true,"isCurrent":1,"runs":161,"wickets":5}]}]}`), &c); err != nil {
		t.Fatal(err)
	}
	rows := normalizeCricketInnings(c)
	if len(rows) != 1 || rows[0].Batting == nil || *rows[0].Batting || rows[0].Target == nil || *rows[0].Target != 156 {
		t.Fatal(rows)
	}
}

func TestCricketCapturedSeries(t *testing.T) {
	for _, slug := range []string{"23805", "24200", "24619", "24620", "24621", "24623", "24627"} {
		t.Run(slug, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/cricket-" + slug + "-scoreboard.json")
			if err != nil {
				t.Fatal(err)
			}
			var payload espnScoreboardResponse
			if err = json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			games := scoreboardEventGames(payload.Events[0], League{ID: "espn:cricket:" + slug, Sport: "cricket", Slug: slug})
			if len(games) != 1 || games[0].HomeTeam.Name == "" || games[0].AwayTeam.Name == "" || games[0].StartTime.IsZero() || games[0].EndTime.IsZero() {
				t.Fatal(games)
			}
			g := games[0]
			if slug == "23805" {
				if len(g.Detail.Innings) != 4 || g.HomeTeam.Score != "453 & 130/2 (24.2 ov, target 130)" || g.Detail.Innings[3].Overs != "24.2" || g.Detail.Innings[3].Target == nil || *g.Detail.Innings[3].Target != 130 {
					t.Fatal(g.Detail.Innings)
				}
			} else if len(g.Detail.Innings) != 0 {
				t.Fatal("scheduled innings exposed")
			}
		})
	}
}
