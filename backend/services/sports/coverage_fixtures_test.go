package sports

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEveryActivatedCoverageFixtureNormalizes(t *testing.T) {
	var evidence []struct {
		ID       string `json:"id"`
		Evidence struct {
			Default struct {
				URL string `json:"url"`
			} `json:"default"`
		} `json:"evidence"`
	}
	raw, err := os.ReadFile("../../../docs/sports-coverage/validated-catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	verifiedPaths := map[string]string{}
	for _, row := range evidence {
		u, err := url.Parse(row.Evidence.Default.URL)
		if err != nil {
			t.Fatal(err)
		}
		verifiedPaths[row.ID] = u.Path
	}

	for _, league := range LeagueCatalog {
		if !strings.HasPrefix(league.ID, "espn:") || !league.active() || league.Provider != "espn" {
			continue
		}
		t.Run(league.ID, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "coverage", league.Sport+"--"+league.Slug+".json"))
			if err != nil {
				t.Fatal(err)
			}
			if league.Sport == "racing" {
				var board raceScoreboard
				if err = json.Unmarshal(data, &board); err != nil {
					t.Fatal(err)
				}
				service := NewService(t.TempDir())
				service.SetEnabledLeagueIDs([]string{league.ID})
				service.client = &http.Client{Transport: coverageTransport(func(req *http.Request) (*http.Response, error) {
					if req.URL.Scheme != "https" || req.URL.Host != "site.api.espn.com" || req.URL.Path != verifiedPaths[league.ID] || req.URL.RawQuery != "" {
						t.Errorf("incorrect racing API %s", req.URL)
					}
					return coverageResponse(200, string(data)), nil
				})}
				events := service.GetRaceBoard(context.Background()).Events
				if len(events) == 0 {
					t.Fatal("no usable race events")
				}
				return
			}
			var board espnScoreboardResponse
			if err = json.Unmarshal(data, &board); err != nil {
				t.Fatal(err)
			}
			count := 0
			seen := map[string]bool{}
			for _, event := range board.Events {
				for _, game := range scoreboardEventGames(event, league) {
					if seen[game.ID] {
						t.Errorf("duplicateevent %s", game.ID)
					}
					seen[game.ID] = true
					if game.ID == "" || game.StartTime.IsZero() {
						t.Errorf("invalid identity/time %+v", game)
					}
					if league.EventKind == "matchup" && (game.HomeTeam.Name == "" || game.AwayTeam.Name == "") {
						t.Error("missing team identity")
					}
					if league.Slug == "tgl" && game.Detail != nil && len(game.Detail.Leaderboard) > 0 {
						t.Fatal("TGL rendered as athlete leaderboard")
					}
					count++
				}
			}
			service := NewService(t.TempDir())
			service.client = &http.Client{Transport: coverageTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host != "site.api.espn.com" || req.URL.Scheme != "https" {
					t.Errorf("incorrect provider %s", req.URL)
				}
				if expected := verifiedPaths[league.ID]; expected != "" && req.URL.Path != expected {
					t.Errorf("provider evidence path %s differs from request %s", expected, req.URL.Path)
				}
				if req.URL.Query().Get("dates") != "20260924" || req.URL.Query().Get("groups") != "" {
					t.Errorf("incorrect dated expanded-league parameters %s", req.URL)
				}
				if req.URL.Query().Get("limit") != "200" {
					t.Error("missing bounded schedule limit")
				}
				return coverageResponse(200, string(data)), nil
			})}
			games, err := service.fetchLeagueScoreboardDate(context.Background(), league, "2026-09-24")
			if err != nil || len(games) != count {
				t.Fatalf("HTTP schedule normalization=%d want%d err=%v", len(games), count, err)
			}
			if count == 0 {
				t.Fatal("activated league has no usable normalized events")
			}
		})
	}
}
func TestMalformedScoreIsNotZero(t *testing.T) {
	for _, raw := range []string{`{}`, `{"unexpected":2}`, `null`, `""`} {
		if espnScore(json.RawMessage(raw)) != "" {
			t.Errorf("invented zero for %s", raw)
		}
	}
}

func TestPartialScoreboardRetainsAvailableGamesAndReportsHealth(t *testing.T) {
	data, err := os.ReadFile("testdata/coverage/australian-football--afl.json")
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err = json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	payload["count"] = 500
	data, _ = json.Marshal(payload)
	s := NewService(t.TempDir())
	s.SetEnabledLeagueIDs([]string{"espn:australian-football:afl"})
	s.client = &http.Client{Transport: coverageTransport(func(r *http.Request) (*http.Response, error) { return coverageResponse(200, string(data)), nil })}
	board, err := s.GetDatedScoreboard(context.Background(), time.Now().UTC().Format("2006-01-02"), "")
	if err != nil || len(board.Games) == 0 || !board.Stale || len(board.Leagues) != 1 || !board.Leagues[0].Partial || board.Leagues[0].Unavailable {
		t.Fatalf("partial schedule lost or reported complete: %+v err=%v", board, err)
	}
}
