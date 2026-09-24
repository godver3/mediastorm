package sports

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestActivatedOptionalProviderFixtures(t *testing.T) {
	var evidence []struct {
		ID      string `json:"id"`
		Summary struct {
			RequestedEventID string `json:"requestedEventID"`
		} `json:"summary"`
	}
	raw, err := os.ReadFile("../../../docs/sports-coverage/evidence/optional-capabilities.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	events := map[string]string{}
	for _, row := range evidence {
		events[row.ID] = row.Summary.RequestedEventID
	}

	for _, league := range LeagueCatalog {
		if league.Provider != "espn" || !league.active() {
			continue
		}
		for _, capability := range []string{"standings", "summary", "team-identities"} {
			if !league.hasCapability(capability) {
				continue
			}
			t.Run(league.ID+"/"+capability, func(t *testing.T) {
				kind := capability
				if kind == "team-identities" {
					kind = "teams"
				}
				data, err := os.ReadFile(filepath.Join("../../../docs/sports-coverage/evidence/optional", league.Sport+"--"+league.Slug+"--"+kind+".json"))
				if err != nil {
					t.Fatal(err)
				}
				service := NewService(t.TempDir())
				requests := 0
				service.client = &http.Client{Transport: coverageTransport(func(r *http.Request) (*http.Response, error) {
					requests++
					prefix := "/apis/site/v2/sports/"
					params := url.Values{}
					switch kind {
					case "standings":
						prefix = "/apis/v2/sports/"
						params.Set("season", strconv.Itoa(time.Now().Year()))
					case "summary":
						params.Set("event", events[league.ID])
					case "teams":
						params.Set("limit", "1000")
					}
					expected := prefix + league.Sport + "/" + league.Slug + "/" + kind
					if r.URL.Scheme != "https" || r.URL.Host != "site.api.espn.com" || r.URL.Path != expected || r.URL.Query().Encode() != params.Encode() {
						t.Errorf("incorrect %s request %s; expected path %s params %s", kind, r.URL, expected, params.Encode())
					}
					return coverageResponse(200, string(data)), nil
				})}
				switch capability {
				case "standings":
					got := service.GetLeagueStandings(context.Background(), league.ID)
					if len(got.Groups) == 0 {
						t.Fatal("no usable standings")
					}
				case "team-identities":
					teams, err := service.fetchLeagueTeams(context.Background(), league)
					if err != nil || len(teams) == 0 {
						t.Fatalf("identities=%d err=%v", len(teams), err)
					}
				case "summary":
					fixture, err := os.ReadFile(filepath.Join("testdata/coverage", league.Sport+"--"+league.Slug+".json"))
					if err != nil {
						t.Fatal(err)
					}
					var board espnScoreboardResponse
					json.Unmarshal(fixture, &board)
					if len(board.Events) == 0 {
						t.Fatal("missing score fixture")
					}
					games := scoreboardEventGames(board.Events[0], league)
					if len(games) == 0 {
						t.Fatal("missing match")
					}
					// Captured summaries may select a different match than the first board row.
					var header struct {
						Header struct {
							ID string `json:"id"`
						} `json:"header"`
					}
					json.Unmarshal(data, &header)
					for _, event := range board.Events {
						if event.ID == header.Header.ID {
							games = scoreboardEventGames(event, league)
							break
						}
					}
					if games[0].ProviderEventID != header.Header.ID {
						t.Skip("summary event not among compact scoreboard fixtures")
					}
					got := service.EnrichGame(context.Background(), games[0])
					if got.Detail == nil {
						t.Fatal("summary not normalized")
					}
				}
				if requests != 1 {
					t.Errorf("expected one bounded metadata request, got%d", requests)
				}
			})
		}
	}
}
