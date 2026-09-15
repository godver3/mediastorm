package sports

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"novastream/models"
	"strconv"
	"strings"
	"time"
)

type detailCacheEntry struct {
	game    models.SportsGame
	expires time.Time
}
type mlbRunner struct {
	PlayerID json.RawMessage `json:"playerId"`
}

type mlbSummary struct {
	Situation *struct {
		OnFirst  *mlbRunner `json:"onFirst"`
		OnSecond *mlbRunner `json:"onSecond"`
		OnThird  *mlbRunner `json:"onThird"`
		Balls    *int       `json:"balls"`
		Strikes  *int       `json:"strikes"`
		Outs     *int       `json:"outs"`
		Pitcher  struct {
			PlayerID json.RawMessage `json:"playerId"`
		} `json:"pitcher"`
		Batter struct {
			PlayerID json.RawMessage `json:"playerId"`
		} `json:"batter"`
	} `json:"situation"`
	Rosters []struct {
		Team struct {
			ID string `json:"id"`
		} `json:"team"`
		Roster []struct {
			Athlete struct {
				ID          string `json:"id"`
				DisplayName string `json:"displayName"`
			} `json:"athlete"`
		} `json:"roster"`
	} `json:"rosters"`

	Header struct {
		ID           string `json:"id"`
		Competitions []struct {
			Status      espnStatus `json:"status"`
			Competitors []struct {
				HomeAway   string          `json:"homeAway"`
				ID         string          `json:"id"`
				Score      json.RawMessage `json:"score"`
				Hits       *int            `json:"hits"`
				Errors     *int            `json:"errors"`
				Linescores []struct {
					DisplayValue string `json:"displayValue"`
				} `json:"linescores"`
			} `json:"competitors"`
		} `json:"competitions"`
	} `json:"header"`
	Plays []struct {
		ID      string `json:"id"`
		Text    string `json:"text"`
		Scoring bool   `json:"scoringPlay"`
		Type    struct {
			Text string `json:"text"`
		} `json:"type"`
		Period struct {
			Type   string `json:"type"`
			Number int    `json:"number"`
		} `json:"period"`
	} `json:"plays"`
	Boxscore struct {
		Players []playerBoxscoreTeam `json:"players"`

		Teams []struct {
			Team struct {
				ID string `json:"id"`
			} `json:"team"`
			Statistics []struct {
				Name  string `json:"name"`
				Stats []struct {
					Name         string `json:"name"`
					DisplayValue string `json:"displayValue"`
				} `json:"stats"`
			} `json:"statistics"`
		} `json:"teams"`
	} `json:"boxscore"`
}

func normalizeMLBDetail(game models.SportsGame, payload mlbSummary, now time.Time) (models.SportsGame, error) {
	if payload.Header.ID != game.ID || len(payload.Header.Competitions) != 1 {
		return game, fmt.Errorf("MLB summary identity mismatch")
	}
	competition := payload.Header.Competitions[0]
	sides := make(map[string][]string)
	counts := map[string]map[string]string{}
	for _, team := range competition.Competitors {
		expected := game.HomeTeam.ID
		if team.HomeAway == "away" {
			expected = game.AwayTeam.ID
		} else if team.HomeAway != "home" {
			return game, fmt.Errorf("invalid MLB side")
		}
		if team.ID != expected {
			return game, fmt.Errorf("MLB team identity mismatch")
		}
		if _, exists := sides[team.HomeAway]; exists {
			return game, fmt.Errorf("duplicate MLB side")
		}
		line := make([]string, len(team.Linescores))
		for i, inning := range team.Linescores {
			line[i] = inning.DisplayValue
		}
		sides[team.HomeAway] = line
		counts[team.HomeAway] = map[string]string{"Runs": espnScore(team.Score)}
		if team.Hits != nil {
			counts[team.HomeAway]["Hits"] = strconv.Itoa(*team.Hits)
		}
		if team.Errors != nil {
			counts[team.HomeAway]["Errors"] = strconv.Itoa(*team.Errors)
		}
		if team.HomeAway == "away" {
			game.AwayTeam.Score = espnScore(team.Score)
		} else {
			game.HomeTeam.Score = espnScore(team.Score)
		}
	}
	if len(sides) != 2 {
		return game, fmt.Errorf("incomplete MLB matchup")
	}
	if competition.Status.Type.State == "" {
		return game, fmt.Errorf("missing MLB lifecycle")
	}
	game.Status = espnStatusToGameStatus(competition.Status.Type)
	game.StatusDetail = competition.Status.Type.Detail
	game.Clock = competition.Status.DisplayClock
	game.Period = competition.Status.Type.ShortDetail
	detail := &models.SportsGameDetail{Source: "espn", UpdatedAt: now, Periods: []models.SportsPeriodScore{}, Plays: []models.SportsDetailPlay{}, Comparisons: []models.SportsComparison{}}
	// Pregame payloads can contain season statistics: never present these as game stats.
	if game.Status != models.SportsGameScheduled {
		detail.PlayerStats = normalizePlayerGameStats(game, payload.Boxscore.Players)
		for _, label := range []string{"Runs", "Hits", "Errors"} {
			a, h := counts["away"][label], counts["home"][label]
			if a != "" && h != "" {
				detail.Comparisons = append(detail.Comparisons, models.SportsComparison{Label: label, Away: a, Home: h})
			}
		}
		n := max(len(sides["away"]), len(sides["home"]))
		for i := 0; i < n; i++ {
			a, h := "–", "–"
			if i < len(sides["away"]) && sides["away"][i] != "" {
				a = sides["away"][i]
			}
			if i < len(sides["home"]) && sides["home"][i] != "" {
				h = sides["home"][i]
			}
			detail.Periods = append(detail.Periods, models.SportsPeriodScore{Label: strconv.Itoa(i + 1), Away: a, Home: h})
		}
		seen := map[string]bool{}
		// ESPN's chronological plays become newest-first for Latest and the timeline.
		for i := len(payload.Plays) - 1; i >= 0; i-- {
			p := payload.Plays[i]
			if p.ID == "" || p.Text == "" || seen[p.ID] {
				continue
			}
			seen[p.ID] = true
			detail.Plays = append(detail.Plays, models.SportsDetailPlay{ID: p.ID, PeriodLabel: fmt.Sprintf("%s %d", p.Period.Type, p.Period.Number), Title: p.Type.Text, Description: p.Text, Scoring: p.Scoring})
			if len(detail.Plays) == 200 {
				break
			}
		}
		values := map[string]map[string]string{}
		for _, team := range payload.Boxscore.Teams {
			stats := map[string]string{}
			for _, group := range team.Statistics {
				if group.Name != "batting" {
					continue
				}
				for _, stat := range group.Stats {
					stats[stat.Name] = stat.DisplayValue
				}
			}
			values[team.Team.ID] = stats
		}
		for _, field := range []struct{ key, label string }{{"homeRuns", "Home runs"}, {"RBIs", "RBI"}, {"walks", "Walks"}, {"strikeouts", "Strikeouts"}, {"leftOnBase", "Left on base"}} {
			a := values[game.AwayTeam.ID][field.key]
			h := values[game.HomeTeam.ID][field.key]
			if a != "" && h != "" {
				detail.Comparisons = append(detail.Comparisons, models.SportsComparison{Label: field.label, Away: a, Home: h})
			}
		}
	}
	if game.Status == models.SportsGameLive && payload.Situation != nil {
		sit := payload.Situation
		pitcherID := espnScore(sit.Pitcher.PlayerID)
		var pitcherPitchCount *int
		names := map[string]string{}
		for _, roster := range payload.Rosters {
			if roster.Team.ID != game.AwayTeam.ID && roster.Team.ID != game.HomeTeam.ID {
				continue
			}
			for _, player := range roster.Roster {
				names[player.Athlete.ID] = player.Athlete.DisplayName
			}
		}
		for _, team := range payload.Boxscore.Players {
			if team.Team.ID != game.AwayTeam.ID && team.Team.ID != game.HomeTeam.ID {
				continue
			}
			for _, group := range team.Statistics {
				pcIndex := -1
				for i, name := range group.Names {
					if name == "PC" {
						pcIndex = i
						break
					}
				}
				for _, player := range group.Athletes {
					names[player.Athlete.ID] = player.Athlete.DisplayName
					if pitcherID != "" && pitcherID != "0" && player.Athlete.ID == pitcherID && pcIndex >= 0 && pcIndex < len(player.Stats) {
						if count, err := strconv.Atoi(player.Stats[pcIndex]); err == nil && count >= 0 {
							pitcherPitchCount = &count
						}
					}
				}
			}
		}
		occupied := []string{}
		for _, base := range []struct {
			label  string
			runner *mlbRunner
		}{{"1st", sit.OnFirst}, {"2nd", sit.OnSecond}, {"3rd", sit.OnThird}} {
			if base.runner != nil && espnScore(base.runner.PlayerID) != "" && espnScore(base.runner.PlayerID) != "0" {
				occupied = append(occupied, base.label)
			}
		}
		bases := ""
		if len(occupied) > 0 {
			bases = "Runner on " + strings.Join(occupied, " & ")
			if len(occupied) > 1 {
				bases = "Runners on " + strings.Join(occupied, " & ")
			}
		}
		detail.Sport = &models.SportsMLBSituation{BatterID: espnScore(sit.Batter.PlayerID), PitcherID: pitcherID, Kind: "mlb", Bases: bases, Inning: game.Period, Balls: validCount(sit.Balls, 3), Strikes: validCount(sit.Strikes, 2), Outs: validCount(sit.Outs, 3), Pitcher: names[pitcherID], PitcherPitchCount: pitcherPitchCount, Batter: names[espnScore(sit.Batter.PlayerID)]}
	}
	detail.Capabilities.Plays = len(detail.Plays) > 0
	detail.Capabilities.Stats = len(detail.Comparisons) > 0 || len(detail.PlayerStats) > 0
	game.Detail = detail
	return game, nil
}

// EnrichGame fetches the selected core-league game, with a bounded cache and timeout.
// The separate lock coalesces concurrent requests without blocking scoreboard reads.
func (s *Service) EnrichGame(ctx context.Context, game models.SportsGame) models.SportsGame {
	sport := map[string]string{"mlb": "baseball", "nfl": "football", "nba": "basketball", "nhl": "hockey", "college-football": "football", "mens-college-basketball": "basketball", "womens-college-basketball": "basketball"}[game.League]
	slug := game.League
	if strings.HasPrefix(game.League, "soccer-") {
		sport = "soccer"
		slug = strings.TrimPrefix(game.League, "soccer-")
	}
	if sport == "" {
		return game
	}
	cacheID := game.ID
	if game.League != "mlb" {
		cacheID = game.League + ":" + game.ID
	}
	s.detailMu.Lock()
	defer s.detailMu.Unlock()
	if s.details == nil {
		s.details = map[string]detailCacheEntry{}
	}
	cached, exists := s.details[cacheID]
	if exists && time.Now().Before(cached.expires) {
		if cached.game.League == "nfl" {
			s.applyFootballStanding(&cached.game.AwayTeam)
			s.applyFootballStanding(&cached.game.HomeTeam)
		}
		return cached.game
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	endpoint := "https://site.api.espn.com/apis/site/v2/sports/" + sport + "/" + slug + "/summary?event=" + url.QueryEscape(game.ID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	var result models.SportsGame
	if err == nil {
		var response *http.Response
		response, err = s.client.Do(req)
		if err == nil {
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				err = fmt.Errorf("sports summary HTTP %d", response.StatusCode)
			} else {
				decoder := json.NewDecoder(io.LimitReader(response.Body, 8<<20))
				var raw json.RawMessage
				err = decoder.Decode(&raw)
				if game.League == "mlb" {
					var payload mlbSummary
					if err == nil {
						err = json.Unmarshal(raw, &payload)
					}
					if err == nil {
						result, err = normalizeMLBDetail(game, payload, time.Now())
					}
				} else {
					var payload teamSportSummary
					if err == nil {
						err = json.Unmarshal(raw, &payload)
					}
					if err == nil {
						result, err = normalizeTeamDetail(game, payload, time.Now())
					}
				}
				if err == nil && result.Detail != nil {
					result.Detail.Pregame = normalizePregame(result, raw, time.Now())
				}
			}
		}
	}
	if err != nil {
		if exists && cached.game.Detail != nil {
			game = cached.game
			d := *game.Detail
			d.Stale = true
			game.Detail = &d
		}
		// Back off failed fetches; do not turn unknown detail into fabricated data.
		s.cacheDetail(cacheID, detailCacheEntry{game: game, expires: time.Now().Add(15 * time.Second)})
		return game
	}
	if result.League == "nfl" {
		s.applyFootballStanding(&result.AwayTeam)
		s.applyFootballStanding(&result.HomeTeam)
	}
	ttl := 30 * time.Second
	if result.Status == models.SportsGameFinal {
		ttl = 10 * time.Minute
	}
	s.cacheDetail(cacheID, detailCacheEntry{game: result, expires: time.Now().Add(ttl)})
	return result
}

func validCount(value *int, maximum int) *int {
	if value == nil || *value < 0 || *value > maximum {
		return nil
	}
	return value
}

// cacheDetail is called with detailMu held. Bound successes and failures alike,
// preserving other last-good snapshots when refreshing an existing entry.
func (s *Service) cacheDetail(id string, entry detailCacheEntry) {
	if _, exists := s.details[id]; !exists && len(s.details) >= 64 {
		var oldestID string
		var oldest time.Time
		for key, value := range s.details {
			if oldestID == "" || value.expires.Before(oldest) {
				oldestID, oldest = key, value.expires
			}
		}
		delete(s.details, oldestID)
	}
	s.details[id] = entry
}
