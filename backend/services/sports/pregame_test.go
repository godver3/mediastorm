package sports

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"novastream/models"
	"os"
	"strings"
	"testing"
	"time"
)

func pregameFixture(t *testing.T, league string) (models.SportsGame, []byte) {
	t.Helper()
	file := league
	if league == "soccer-eng.1" {
		file = "eng.1"
	}
	raw, err := os.ReadFile("testdata/pregame-" + file + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var p pregamePayload
	if err = json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	g := models.SportsGame{ID: p.Header.ID, League: league, Status: models.SportsGameScheduled}
	g.StartTime = parseESPNDate(p.Header.Competitions[0].Date)
	for _, c := range p.Header.Competitions[0].Competitors {
		if c.HomeAway == "home" {
			g.HomeTeam.ID = c.ID
		} else {
			g.AwayTeam.ID = c.ID
		}
	}
	return g, raw
}
func TestPregameRealProviderSummaries(t *testing.T) {
	now := time.Date(2026, 9, 15, 23, 59, 0, 0, time.UTC)
	for _, league := range []string{"mlb", "nba", "soccer-eng.1"} {
		t.Run(league, func(t *testing.T) {
			g, raw := pregameFixture(t, league)
			p := normalizePregame(g, raw, now)
			if p == nil || len(p.Teams) != 2 || p.SeasonLabel == "" {
				t.Fatalf("missing context: %+v", p)
			}
			if p.Teams[0].TeamID != g.AwayTeam.ID || p.Teams[1].TeamID != g.HomeTeam.ID {
				t.Fatal("reversed team identity")
			}
			for _, team := range p.Teams {
				if team.Record == "" || team.StandingLabel == "" {
					t.Fatalf("missing records/standings: %+v", team)
				}
			}
			if league == "mlb" {
				if len(p.Comparisons) != 6 || len(p.Teams[0].Probables) != 1 || len(p.Teams[0].Leaders) == 0 || len(p.Teams[0].Recent) == 0 || p.PreviousMeeting == nil {
					t.Fatalf("missing MLB preview: %+v", p)
				}
			}
			if league == "nba" && len(p.Comparisons) != 0 {
				t.Fatal("invented meaningful averages before season begins")
			}
			if league == "soccer-eng.1" && len(p.Comparisons) != 4 {
				t.Fatal("missing soccer season comparison")
			}
		})
	}
}
func TestPregameRetainsOnlyHistoryForLiveAndFinal(t *testing.T) {
	g, raw := pregameFixture(t, "mlb")
	now := time.Now()
	for _, state := range []models.SportsGameStatus{models.SportsGameLive, models.SportsGameFinal} {
		g.Status = state
		context := normalizePregame(g, raw, now)
		if context == nil || context.PreviousMeeting == nil || len(context.Teams[0].Recent) == 0 {
			t.Fatal("lost historical context after the game started")
		}
		if len(context.Comparisons) != 0 || context.Teams[0].Record != "" || len(context.Teams[0].Leaders) != 0 || len(context.Teams[0].StandingStats) != 0 {
			t.Fatal("exposed current game or updated aggregate statistics as history")
		}
	}
	g.Status = models.SportsGameScheduled
	g.HomeTeam.ID = "wrong"
	if normalizePregame(g, raw, now) != nil {
		t.Fatal("accepted wrong team")
	}
}
func TestPregameHistoryFiltersFutureDuplicateAndWrongTeam(t *testing.T) {
	g, raw := pregameFixture(t, "mlb")
	var p pregamePayload
	json.Unmarshal(raw, &p)
	group := &p.LastFiveGames[0]
	if len(group.Events) < 2 {
		t.Fatal("fixture has no history")
	}
	valid := group.Events[0]
	group.Events = append(group.Events, valid)
	group.Events[0].GameDate = "2099-01-01T00:00:00Z"
	wrongID := group.Events[1].ID
	group.Events[1].Opponent.ID = "wrong"
	raw, _ = json.Marshal(p)
	out := normalizePregame(g, raw, time.Date(2026, 9, 15, 23, 59, 0, 0, time.UTC))
	for _, team := range out.Teams {
		seen := map[string]bool{}
		for _, e := range team.Recent {
			if e.ID == wrongID || seen[e.ID] || e.Date.Year() > 2026 {
				t.Fatal("duplicate/future history")
			}
			seen[e.ID] = true
		}
		if len(team.Recent) > 5 {
			t.Fatal("unbounded history")
		}
	}
}
func TestPregameUsesOneSummaryRequestAndNoLiveStats(t *testing.T) {
	for _, league := range []string{"mlb", "soccer-eng.1", "nba"} {
		t.Run(league, func(t *testing.T) {
			g, raw := pregameFixture(t, league)
			calls := 0
			s := NewService(t.TempDir())
			s.client = &http.Client{Transport: detailTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})}
			got := s.EnrichGame(context.Background(), g)
			s.EnrichGame(context.Background(), g)
			if calls != 1 || got.Detail == nil || got.Detail.Pregame == nil {
				t.Fatalf("missing cached pregame: calls=%d detail=%+v", calls, got.Detail)
			}
			if got.Detail.Capabilities.Stats || len(got.Detail.PlayerStats) > 0 || len(got.Detail.Comparisons) > 0 {
				t.Fatal("season stats leaked into game stats")
			}
		})
	}
}

func TestPregameCanonicalEventIdentityUsesProviderID(t *testing.T) {
	game, raw := pregameFixture(t, "mlb")
	game.ProviderEventID = game.ID
	game.ID = "espn:baseball:college-baseball:" + game.ID
	game.League = "espn:baseball:college-baseball"
	if normalizePregame(game, raw, time.Now()) == nil {
		t.Fatal("canonical ID discarded identity-matched provider pregame")
	}
	game.ProviderEventID = "different-event"
	if normalizePregame(game, raw, time.Now()) != nil {
		t.Fatal("different provider event accepted")
	}
}
