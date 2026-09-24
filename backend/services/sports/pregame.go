package sports

import (
	"encoding/json"
	"fmt"
	"novastream/models"
	"sort"
	"strconv"
	"strings"
	"time"
)

type pregameStat struct {
	Name         string        `json:"name"`
	DisplayValue string        `json:"displayValue"`
	Stats        []pregameStat `json:"stats"`
}
type pregameAthlete struct {
	DisplayName string `json:"displayName"`
}
type pregameCompetitor struct {
	ID          string              `json:"id"`
	HomeAway    string              `json:"homeAway"`
	Record      []teamContextRecord `json:"record"`
	CuratedRank struct {
		Current int `json:"current"`
	} `json:"curatedRank"`
	Probables []struct {
		Name    string         `json:"name"`
		Athlete pregameAthlete `json:"athlete"`
	} `json:"probables"`
}
type pregamePayload struct {
	Header struct {
		ID     string `json:"id"`
		Season struct {
			Year int    `json:"year"`
			Name string `json:"name"`
			Type int    `json:"type"`
		} `json:"season"`
		Competitions []struct {
			Date        string              `json:"date"`
			Competitors []pregameCompetitor `json:"competitors"`
		} `json:"competitions"`
	} `json:"header"`
	Boxscore struct {
		Teams []struct {
			Team struct {
				ID string `json:"id"`
			} `json:"team"`
			Statistics []pregameStat `json:"statistics"`
		} `json:"teams"`
	} `json:"boxscore"`
	LastFiveGames []struct {
		Team struct {
			ID string `json:"id"`
		} `json:"team"`
		Events []struct {
			ID         string `json:"id"`
			GameDate   string `json:"gameDate"`
			GameResult string `json:"gameResult"`
			Score      string `json:"score"`
			HomeTeamID string `json:"homeTeamId"`
			AwayTeamID string `json:"awayTeamId"`
			Opponent   struct {
				ID          string `json:"id"`
				DisplayName string `json:"displayName"`
			} `json:"opponent"`
		} `json:"events"`
	} `json:"lastFiveGames"`
	Standings teamContextStandings `json:"standings"`
	Leaders   []struct {
		Team struct {
			ID string `json:"id"`
		} `json:"team"`
		Leaders []struct {
			DisplayName string `json:"displayName"`
			Leaders     []struct {
				DisplayValue string         `json:"displayValue"`
				Athlete      pregameAthlete `json:"athlete"`
			} `json:"leaders"`
		} `json:"leaders"`
	} `json:"leaders"`
	SeasonSeries []struct {
		Events []struct {
			Date        string `json:"date"`
			Status      string `json:"status"`
			Competitors []struct {
				HomeAway string `json:"homeAway"`
				Score    string `json:"score"`
				Team     struct {
					ID string `json:"id"`
				} `json:"team"`
			} `json:"competitors"`
		} `json:"events"`
	} `json:"seasonseries"`
}

func pregameText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "-" || value == "--" || len(value) > 160 {
		return ""
	}
	return value
}

// Parse only the already-fetched, identity-verified summary. No additional requests.
func normalizePregame(game models.SportsGame, raw []byte, now time.Time) *models.SportsPregame {
	var p pregamePayload
	if json.Unmarshal(raw, &p) != nil || p.Header.ID != providerEventID(game) || len(p.Header.Competitions) != 1 {
		return nil
	}
	c := p.Header.Competitions[0]
	if len(c.Competitors) != 2 || game.HomeTeam.ID == game.AwayTeam.ID {
		return nil
	}
	out := &models.SportsPregame{UpdatedAt: now}
	if p.Header.Season.Year >= 1900 && p.Header.Season.Year <= 2200 {
		out.SeasonLabel = pregameText(p.Header.Season.Name)
		if out.SeasonLabel == "" {
			out.SeasonLabel = strconv.Itoa(p.Header.Season.Year)
			if game.League == "nba" || game.League == "nhl" || strings.Contains(game.League, "college-basketball") {
				out.SeasonLabel = fmt.Sprintf("%d–%02d", p.Header.Season.Year-1, p.Header.Season.Year%100)
			}
			switch p.Header.Season.Type {
			case 1:
				out.SeasonLabel += " · Preseason"
			case 2:
				out.SeasonLabel += " · Regular season"
			case 3:
				out.SeasonLabel += " · Postseason"
			}
		}
	}
	teams := map[string]*models.SportsPregameTeam{}
	for _, entry := range c.Competitors {
		expected := game.HomeTeam
		if entry.HomeAway == "away" {
			expected = game.AwayTeam
		} else if entry.HomeAway != "home" {
			return nil
		}
		if entry.ID != expected.ID || teams[entry.ID] != nil {
			return nil
		}
		t := &models.SportsPregameTeam{TeamID: entry.ID, Record: pregameText(expected.Record), ConferenceRecord: pregameText(expected.ConferenceRecord), Rank: expected.Rank}
		for _, r := range entry.Record {
			v := pregameText(r.Summary)
			if v == "" {
				continue
			}
			switch r.Type {
			case "total":
				t.Record = v
			case "home":
				t.HomeRecord = v
			case "road", "away":
				t.AwayRecord = v
			case "vsconf":
				t.ConferenceRecord = v
			}
		}
		if entry.CuratedRank.Current > 0 && entry.CuratedRank.Current <= 25 {
			t.Rank = entry.CuratedRank.Current
		}
		for _, probable := range entry.Probables {
			if probable.Name == "probableStartingPitcher" && pregameText(probable.Athlete.DisplayName) != "" && len(t.Probables) == 0 {
				t.Probables = append(t.Probables, models.SportsPregamePlayer{Name: pregameText(probable.Athlete.DisplayName), Label: "Probable starting pitcher"})
			}
		}
		teams[entry.ID] = t
	}
	stats := map[string]map[string]string{}
	for _, t := range p.Boxscore.Teams {
		if teams[t.Team.ID] == nil || stats[t.Team.ID] != nil {
			continue
		}
		values := map[string]string{}
		for _, stat := range t.Statistics {
			if v := pregameText(stat.DisplayValue); v != "" {
				values[stat.Name] = v
			}
			for _, child := range stat.Stats {
				if v := pregameText(child.DisplayValue); v != "" {
					values[stat.Name+"."+child.Name] = v
				}
			}
		}
		stats[t.Team.ID] = values
	}
	// An empty/new-season record is not evidence for meaningful zero season averages.
	played := func(id string) bool {
		for _, token := range strings.FieldsFunc(teams[id].Record, func(r rune) bool { return r == '-' }) {
			if n, e := strconv.Atoi(token); e == nil && n > 0 {
				return true
			}
		}
		for _, key := range []string{"gamesPlayed", "batting.gamesPlayed"} {
			if n, e := strconv.Atoi(stats[id][key]); e == nil && n > 0 {
				return true
			}
		}
		return false
	}
	fields := pregameComparisonFields(game.League)
	if out.SeasonLabel != "" && played(game.AwayTeam.ID) && played(game.HomeTeam.ID) {
		for _, field := range fields {
			a, h := stats[game.AwayTeam.ID][field[0]], stats[game.HomeTeam.ID][field[0]]
			if a != "" && h != "" {
				out.Comparisons = append(out.Comparisons, models.SportsComparison{Label: field[1], Away: a, Home: h})
			}
		}
	}
	for _, group := range p.Standings.Groups {
		for _, row := range group.Standings.Entries {
			t := teams[row.ID]
			if t == nil || t.StandingLabel != "" || pregameText(group.Header) == "" {
				continue
			}
			t.StandingLabel = pregameText(group.Header)
			values := map[string]string{}
			for _, s := range row.Stats {
				values[s.Name] = pregameText(s.DisplayValue)
			}
			for _, f := range [][2]string{{"rank", "Position"}, {"playoffSeed", "Seed"}, {"points", "Points"}, {"gamesPlayed", "Played"}, {"wins", "Wins"}, {"losses", "Losses"}, {"gamesBehind", "Games back"}, {"streak", "Streak"}} {
				if v := values[f[0]]; v != "" {
					t.StandingStats = append(t.StandingStats, models.SportsPlayerStatistic{Label: f[1], Value: v})
				}
			}
		}
	}
	cutoff := parseESPNDate(c.Date)
	if cutoff.IsZero() {
		cutoff = game.StartTime
	}
	if cutoff.IsZero() || cutoff.After(now) {
		cutoff = now
	}
	for _, group := range p.LastFiveGames {
		t := teams[group.Team.ID]
		if t == nil || len(t.Recent) > 0 {
			continue
		}
		seen := map[string]bool{}
		for _, e := range group.Events {
			date := parseESPNDate(e.GameDate)
			other := e.HomeTeamID
			if other == t.TeamID {
				other = e.AwayTeamID
			} else if e.AwayTeamID != t.TeamID {
				continue
			}
			if date.IsZero() || !date.Before(cutoff) || e.ID == providerEventID(game) || e.ID == "" || seen[e.ID] || e.Opponent.ID != other || other == t.TeamID || pregameText(e.Opponent.DisplayName) == "" {
				continue
			}
			if e.GameResult != "W" && e.GameResult != "L" && e.GameResult != "D" && e.GameResult != "T" {
				continue
			}
			seen[e.ID] = true
			t.Recent = append(t.Recent, models.SportsPregameResult{ID: e.ID, Date: date, Opponent: pregameText(e.Opponent.DisplayName), Result: e.GameResult, Score: pregameText(e.Score)})
		}
		sort.SliceStable(t.Recent, func(i, j int) bool { return t.Recent[i].Date.After(t.Recent[j].Date) })
		if len(t.Recent) > 5 {
			t.Recent = t.Recent[:5]
		}
	}
	if out.SeasonLabel != "" {
		for _, group := range p.Leaders {
			t := teams[group.Team.ID]
			if t == nil || !played(t.TeamID) {
				continue
			}
			seen := map[string]bool{}
			for _, category := range group.Leaders {
				label := pregameText(category.DisplayName)
				if label == "" || seen[label] || len(t.Leaders) >= 3 {
					continue
				}
				for _, entry := range category.Leaders {
					name, value := pregameText(entry.Athlete.DisplayName), pregameText(entry.DisplayValue)
					if name != "" && value != "" {
						t.Leaders = append(t.Leaders, models.SportsPregamePlayer{Name: name, Label: label, Value: value})
						seen[label] = true
						break
					}
				}
			}
		}
	}
	for _, series := range p.SeasonSeries {
		for _, e := range series.Events {
			date := parseESPNDate(e.Date)
			if date.IsZero() || e.Status != "post" || !date.Before(cutoff) || len(e.Competitors) != 2 || (out.PreviousMeeting != nil && !date.After(out.PreviousMeeting.Date)) {
				continue
			}
			meeting := models.SportsPregameMeeting{Date: date}
			valid := true
			seen := map[string]bool{}
			for _, t := range e.Competitors {
				if teams[t.Team.ID] == nil || seen[t.Team.ID] || pregameText(t.Score) == "" {
					valid = false
					break
				}
				seen[t.Team.ID] = true
				if t.HomeAway == "home" {
					meeting.HomeTeamID = t.Team.ID
					meeting.HomeScore = pregameText(t.Score)
				} else if t.HomeAway == "away" {
					meeting.AwayTeamID = t.Team.ID
					meeting.AwayScore = pregameText(t.Score)
				} else {
					valid = false
				}
			}
			if valid && meeting.HomeTeamID != "" && meeting.AwayTeamID != "" {
				out.PreviousMeeting = &meeting
			}
		}
	}
	if game.Status != models.SportsGameScheduled {
		// Live summaries use boxscore/leader fields for this game, not season context.
		// Keep only dated historical results; records/standings may already include it.
		out.Comparisons = nil
		for id, team := range teams {
			teams[id] = &models.SportsPregameTeam{TeamID: team.TeamID, Recent: team.Recent}
		}
	}
	for _, id := range []string{game.AwayTeam.ID, game.HomeTeam.ID} {
		out.Teams = append(out.Teams, *teams[id])
	}
	return out
}

func pregameComparisonFields(league string) [][2]string {
	switch {
	case league == "mlb":
		return [][2]string{{"batting.avg", "Batting average"}, {"batting.OPS", "OPS"}, {"batting.runs", "Runs scored"}, {"batting.homeRuns", "Home runs"}, {"pitching.ERA", "ERA"}, {"pitching.WHIP", "WHIP"}}
	case league == "nba" || strings.Contains(league, "college-basketball"):
		return [][2]string{{"avgPoints", "Points per game"}, {"avgPointsAgainst", "Points allowed per game"}, {"fieldGoalPct", "Field goal %"}, {"threePointFieldGoalPct", "Three-point %"}, {"avgRebounds", "Rebounds per game"}, {"avgAssists", "Assists per game"}}
	case league == "nfl" || league == "college-football":
		return [][2]string{{"totalPointsPerGame", "Points per game"}, {"totalYardsPerGame", "Yards per game"}, {"passingYardsPerGame", "Passing yards per game"}, {"rushingYardsPerGame", "Rushing yards per game"}, {"totalPointsAllowedPerGame", "Points allowed per game"}}
	case league == "nhl":
		return [][2]string{{"avgGoals", "Goals per game"}, {"avgGoalsAgainst", "Goals allowed per game"}, {"powerPlayPct", "Power play %"}, {"penaltyKillPct", "Penalty kill %"}}
	case strings.HasPrefix(league, "soccer-"):
		return [][2]string{{"totalGoals", "Goals scored"}, {"goalsConceded", "Goals conceded"}, {"goalDifference", "Goal difference"}, {"goalAssists", "Assists"}}
	}
	return nil
}
