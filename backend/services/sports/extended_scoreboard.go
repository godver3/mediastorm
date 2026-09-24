package sports

import (
	"encoding/json"
	"fmt"
	"novastream/models"
	"sort"
	"time"
)

type espnLineScore struct {
	Winner       *bool           `json:"winner"`
	Period       int             `json:"period"`
	Value        json.RawMessage `json:"value"`
	DisplayValue json.RawMessage `json:"displayValue"`
	Linescores   []espnLineScore `json:"linescores"`
	ScoreType    struct {
		DisplayValue string `json:"displayValue"`
	} `json:"scoreType"`
	Runs        *int            `json:"runs"`
	Wickets     *int            `json:"wickets"`
	Overs       json.RawMessage `json:"overs"`
	IsBatting   bool            `json:"isBatting"`
	Description string          `json:"description"`
	Target      *int            `json:"target"`
	IsCurrent   json.RawMessage `json:"isCurrent"`
}

// Cricket uses quoted booleans while other ESPN sports use JSON booleans.
func (c *espnCompetitor) UnmarshalJSON(data []byte) error {
	type alias espnCompetitor
	var raw struct {
		*alias
		Winner json.RawMessage `json:"winner"`
	}
	raw.alias = (*alias)(c)
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	winner, err := parseESPNBoolean(raw.Winner)
	if err != nil {
		return fmt.Errorf("competitor winner: %w", err)
	}
	c.Winner = winner != nil && *winner
	return nil
}

func applyScoreboardDetail(g *models.SportsGame, comp espnCompetition, league League) {
	if league.Sport != "golf" && league.Sport != "cricket" && league.Sport != "tennis" && league.Sport != "rugby" && league.Sport != "rugby-league" {
		return
	}
	d := &models.SportsGameDetail{Source: "espn", UpdatedAt: time.Now(), Periods: []models.SportsPeriodScore{}, Plays: []models.SportsDetailPlay{}, Comparisons: []models.SportsComparison{}}
	switch league.Sport {
	case "rugby", "rugby-league":
		for i, play := range comp.Details {
			row := models.SportsDetailPlay{ID: fmt.Sprintf("%s:%d", g.ID, i), TeamID: play.Team.ID, Title: play.Type.Text, Clock: play.Clock.DisplayValue}
			row.Scoring = play.Type.Text == "try" || play.Type.Text == "conversion" || play.Type.Text == "penalty goal" || play.Type.Text == "drop goal"
			for _, a := range play.Athletes {
				row.Participants = append(row.Participants, models.SportsPlayParticipant{ID: a.ID, Name: a.DisplayName})
				if row.Description != "" {
					row.Description += " · "
				}
				row.Description += a.DisplayName
			}
			d.Plays = append(d.Plays, row)
		}
		d.Capabilities.Plays = len(d.Plays) > 0
	case "golf":
		if league.Slug == "tgl" {
			return
		}
		g.HomeTeam = models.SportsTeam{}
		g.AwayTeam = models.SportsTeam{}
		g.EventKind = "tournament"
		for i := range g.Participants {
			g.Participants[i].Position = 0
			if g.Status == models.SportsGameScheduled {
				g.Participants[i].Score = ""
			}
		}
		for _, c := range comp.Competitors {
			team := competitorTeam(c)
			if team.ID == "" || team.Name == "" {
				continue
			}
			entry := models.SportsGolfEntry{ID: team.ID, Name: team.Name, Score: team.Score}
			if g.Status == models.SportsGameScheduled {
				entry.Score = ""
				d.Leaderboard = append(d.Leaderboard, entry)
				continue
			}
			for _, r := range c.Linescores {
				if r.Period < 1 || (len(r.Value) == 0 && len(r.Linescores) == 0) {
					continue
				}
				round := models.SportsGolfRound{Round: r.Period, Strokes: espnScore(r.Value), ToPar: espnScore(r.DisplayValue)}
				for _, h := range r.Linescores {
					if h.Period < 1 || h.Period > 18 || len(h.Value) == 0 {
						continue
					}
					round.Holes = append(round.Holes, models.SportsGolfHole{Hole: h.Period, Strokes: espnScore(h.Value), ToPar: h.ScoreType.DisplayValue})
				}
				sort.SliceStable(round.Holes, func(i, j int) bool { return round.Holes[i].Hole < round.Holes[j].Hole })
				entry.Rounds = append(entry.Rounds, round)
			}
			d.Leaderboard = append(d.Leaderboard, entry)
		}
		d.Capabilities.Stats = len(d.Leaderboard) > 0
	case "cricket":
		if g.Status == models.SportsGameScheduled {
			g.Detail = d
			return
		}
		g.Clock = ""
		if comp.Status.Summary != "" {
			g.StatusDetail = comp.Status.Summary
		}
		d.Innings = normalizeCricketInnings(comp)
		d.Capabilities.Stats = len(d.Innings) > 0
	case "tennis":
		if g.Status == models.SportsGameScheduled {
			g.Detail = d
			return
		}
		rows := map[int]*models.SportsPeriodScore{}
		for _, c := range comp.Competitors {
			for i, set := range c.Linescores {
				value := espnScore(set.Value)
				if value == "" {
					continue
				}
				n := set.Period
				if n < 1 {
					n = i + 1
				}
				if rows[n] == nil {
					rows[n] = &models.SportsPeriodScore{Label: fmt.Sprintf("Set %d", n), Away: "–", Home: "–"}
				}
				if competitorTeam(c).ID == g.AwayTeam.ID {
					rows[n].Away = value
				} else {
					rows[n].Home = value
				}
			}
		}
		keys := []int{}
		for n := range rows {
			keys = append(keys, n)
		}
		sort.Ints(keys)
		for _, n := range keys {
			d.Periods = append(d.Periods, *rows[n])
		}
	}
	g.Detail = d
}
