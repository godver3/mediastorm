package sports

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"novastream/models"
	"sort"
	"strconv"
	"strings"
	"time"
)

const cflAPIBase = "https://api.stats.cfl.ca"
const cflLeagueID = "espn:football:cfl" // Stable catalog identity; the provider is CFL, not ESPN.
const cflMaxResponseBytes = 4 << 20

type cflFixture struct {
	ID         int      `json:"ID"`
	Week       int      `json:"week"`
	HomeTeamID *int     `json:"home_team_id"`
	AwayTeamID *int     `json:"away_team_id"`
	StartAt    string   `json:"start_at"`
	HomeScore  *int     `json:"home_team_score"`
	AwayScore  *int     `json:"away_team_score"`
	Status     string   `json:"game_status"`
	Broadcasts []string `json:"broadcasting_options"`
}
type cflFixtureResponse struct {
	Year       string       `json:"year"`
	Preseason  []cflFixture `json:"preseason"`
	Season     []cflFixture `json:"season"`
	Semifinals []cflFixture `json:"semiFinals"`
	Finals     []cflFixture `json:"finals"`
}
type cflTeam struct {
	ID             int    `json:"ID"`
	Name           string `json:"name"`
	Region         string `json:"region_label"`
	Abbreviation   string `json:"abbreviation"`
	Color          string `json:"primary_color"`
	AlternateColor string `json:"accent_color"`
	Logo           string `json:"logo_primary"`
}

func (s *Service) readCFLJSON(ctx context.Context, path string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cflAPIBase+path, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("CFL %s HTTP %d", path, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, cflMaxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > cflMaxResponseBytes {
		return fmt.Errorf("CFL response exceeds limit")
	}
	if err = json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode CFL %s: %w", path, err)
	}
	return nil
}

// fetchCFLScoreboardDate uses the public API configured by stats.cfl.ca.
// Adjacent UTC dates preserve fixtures around a viewer's local midnight. The
// scoreboard caller performs final local-day selection, as with ESPN responses.
func (s *Service) fetchCFLScoreboardDate(ctx context.Context, date string) ([]models.SportsGame, error) {
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
	}
	day, err := time.Parse("2006-01-02", date)
	if err != nil {
		return nil, fmt.Errorf("invalid CFL date: %w", err)
	}
	// Team artwork/names are optional. A short independent request cannot turn
	// valid fixtures into an error when the metadata endpoint is unavailable.
	teamCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	teamsCh := make(chan []cflTeam, 1)
	go func() {
		var teams []cflTeam
		if s.readCFLJSON(teamCtx, "/teams", &teams) != nil {
			teams = nil
		}
		teamsCh <- teams
	}()
	var response cflFixtureResponse
	if err = s.readCFLJSON(ctx, fmt.Sprintf("/fixtures/%d", day.Year()), &response); err != nil {
		return nil, err
	}
	if response.Year != strconv.Itoa(day.Year()) {
		return nil, fmt.Errorf("CFL returned season %q for %d", response.Year, day.Year())
	}
	var teams []cflTeam
	select {
	case teams = <-teamsCh:
	case <-teamCtx.Done():
	}
	lookup := map[int]cflTeam{}
	for _, team := range teams {
		if team.ID > 0 {
			lookup[team.ID] = team
		}
	}
	games := normalizeCFLFixtures(response, lookup, time.Now())
	result := make([]models.SportsGame, 0, len(games))
	from, to := day.AddDate(0, 0, -1), day.AddDate(0, 0, 2)
	for _, g := range games {
		if !g.StartTime.Before(from) && g.StartTime.Before(to) {
			result = append(result, g)
		}
	}
	return result, nil
}

func cflSportsTeam(id *int, teams map[int]cflTeam) models.SportsTeam {
	if id == nil || *id <= 0 {
		return models.SportsTeam{Name: "TBD"}
	}
	team := teams[*id]
	result := models.SportsTeam{ID: fmt.Sprintf("cfl:%d", *id), Name: strings.TrimSpace(team.Region + " " + team.Name), Location: team.Region, Nickname: team.Name, Abbreviation: team.Abbreviation, Color: team.Color, AlternateColor: team.AlternateColor}
	if result.Name == "" {
		result.Name = fmt.Sprintf("Team %d", *id)
	}
	// Inline SVG, arbitrary hosts and private URLs are never promoted to artwork.
	if u, err := url.Parse(team.Logo); err == nil && u.Scheme == "https" && u.Host == "content.cfl.ca" && u.User == nil {
		result.LogoURL = u.String()
	}
	return result
}

func normalizeCFLFixtures(response cflFixtureResponse, teams map[int]cflTeam, now time.Time) []models.SportsGame {
	result := []models.SportsGame{}
	seen := map[int]bool{}
	groups := []struct {
		label    string
		fixtures []cflFixture
	}{{"Preseason", response.Preseason}, {"Regular season", response.Season}, {"Semifinals", response.Semifinals}, {"Finals", response.Finals}}
	for _, group := range groups {
		for _, f := range group.fixtures {
			start, err := time.Parse(time.RFC3339, f.StartAt)
			if f.ID <= 0 || err != nil || seen[f.ID] {
				continue
			}
			seen[f.ID] = true
			g := models.SportsGame{ID: fmt.Sprintf("cfl:%d", f.ID), League: cflLeagueID, Sport: "football", EventKind: "matchup", StartTime: start, Status: models.SportsGameScheduled, StatusDetail: "Scheduled", EventContext: group.label, HomeTeam: cflSportsTeam(f.HomeTeamID, teams), AwayTeam: cflSportsTeam(f.AwayTeamID, teams), Broadcasts: f.Broadcasts}
			if group.label == "Regular season" && f.Week > 0 {
				g.EventContext = fmt.Sprintf("Regular season · Week %d", f.Week)
			}
			g.Title = g.AwayTeam.Name + " at " + g.HomeTeam.Name
			status := strings.TrimSpace(f.Status)
			// The official feed has both Finished and the nested-quoted "Finished".
			if unquoted, err := strconv.Unquote(status); err == nil {
				status = unquoted
			}
			if strings.EqualFold(status, "Finished") {
				g.Status = models.SportsGameFinal
				g.StatusDetail = "Final"
			} else if status != "" {
				g.StatusDetail = status
			} else if start.Before(now) {
				g.StatusDetail = "Status unavailable"
			}
			if f.HomeScore != nil && *f.HomeScore >= 0 {
				g.HomeTeam.Score = strconv.Itoa(*f.HomeScore)
			}
			if f.AwayScore != nil && *f.AwayScore >= 0 {
				g.AwayTeam.Score = strconv.Itoa(*f.AwayScore)
			}
			if g.Status == models.SportsGameFinal && f.HomeScore != nil && f.AwayScore != nil && *f.HomeScore >= 0 && *f.AwayScore >= 0 {
				g.HomeTeam.Winner = *f.HomeScore > *f.AwayScore
				g.AwayTeam.Winner = *f.AwayScore > *f.HomeScore
			}
			// total_periods and game_clock in completed fixtures are not dependable
			// current-quarter data. Do not reuse NFL field/down diagrams for CFL.
			result = append(result, g)
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].StartTime.Before(result[j].StartTime) })
	return result
}

// fetchCFLTeams exposes the same provider identity used by matchup participants.
// EspnTeamID is a legacy storage field; its value stays provider-namespaced.
func (s *Service) fetchCFLTeams(ctx context.Context) ([]models.SportsTeamRecord, error) {
	var raw []cflTeam
	if err := s.readCFLJSON(ctx, "/teams", &raw); err != nil {
		return nil, err
	}
	result := make([]models.SportsTeamRecord, 0, len(raw))
	seen := map[int]bool{}
	now := time.Now().UTC()
	for _, team := range raw {
		if team.ID <= 0 || strings.TrimSpace(team.Name) == "" || strings.TrimSpace(team.Region) == "" || seen[team.ID] {
			continue
		}
		seen[team.ID] = true
		normalized := cflSportsTeam(&team.ID, map[int]cflTeam{team.ID: team})
		result = append(result, models.SportsTeamRecord{ID: cflLeagueID + ":" + normalized.ID, League: cflLeagueID, EspnTeamID: normalized.ID, Name: normalized.Name, Location: normalized.Location, Nickname: normalized.Nickname, Abbreviation: normalized.Abbreviation, LogoURL: normalized.LogoURL, UpdatedAt: now})
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("CFL teams contains no named identities")
	}
	return result, nil
}

type cflStandingRow struct {
	TeamID        int    `json:"team_id"`
	Season        int    `json:"season"`
	Abbreviation  string `json:"abbreviation"`
	Place         *int   `json:"place"`
	PlaceOverride *int   `json:"place_override"`
	GamesPlayed   *int   `json:"games_played"`
	Wins          *int   `json:"wins"`
	Losses        *int   `json:"losses"`
	Ties          *int   `json:"ties"`
	Points        *int   `json:"points"`
}
type cflStandingDivision struct {
	Name string           `json:"division_name"`
	Rows []cflStandingRow `json:"standings"`
}
type cflStandingsResponse struct {
	Data struct {
		Divisions map[string]cflStandingDivision `json:"divisions"`
	} `json:"data"`
}

func normalizeCFLStandings(raw cflStandingsResponse, teams []models.SportsTeamRecord, year int) []LeagueStandingGroup {
	if year < 1900 || year > 2200 {
		return nil
	}
	names := map[string]models.SportsTeamRecord{}
	for _, team := range teams {
		if team.League == cflLeagueID && strings.TrimSpace(team.Name) != "" {
			names[team.EspnTeamID] = team
		}
	}
	keys := []string{}
	for key := range raw.Data.Divisions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	groups := []LeagueStandingGroup{}
	for _, key := range keys {
		division := raw.Data.Divisions[key]
		if strings.TrimSpace(division.Name) == "" {
			continue
		}
		group := LeagueStandingGroup{ID: "cfl:" + key, Title: division.Name, Season: year, SeasonLabel: strconv.Itoa(year), Rows: []LeagueStandingRow{}, Columns: []StandingColumn{}}
		seen := map[int]bool{}
		present := map[string]bool{}
		for _, row := range division.Rows {
			team, known := names[fmt.Sprintf("cfl:%d", row.TeamID)]
			if row.Season != year || row.TeamID <= 0 || seen[row.TeamID] || !known {
				continue
			}
			seen[row.TeamID] = true
			values := map[string]string{}
			rank := row.Place
			if row.PlaceOverride != nil && *row.PlaceOverride > 0 {
				rank = row.PlaceOverride
			}
			for key, value := range map[string]*int{"rank": rank, "gamesPlayed": row.GamesPlayed, "wins": row.Wins, "losses": row.Losses, "ties": row.Ties, "points": row.Points} {
				if value == nil || *value < 0 || (key == "rank" && *value == 0) {
					continue
				}
				values[key] = strconv.Itoa(*value)
				present[key] = true
			}
			if len(values) == 0 {
				continue
			}
			group.Rows = append(group.Rows, LeagueStandingRow{ID: team.EspnTeamID, Name: team.Name, Abbreviation: team.Abbreviation, Kind: "team", Values: values})
		}
		for _, col := range []StandingColumn{{"rank", "Rank"}, {"gamesPlayed", "Played"}, {"wins", "Wins"}, {"losses", "Losses"}, {"ties", "Ties"}, {"points", "Points"}} {
			if present[col.Key] {
				group.Columns = append(group.Columns, col)
			}
		}
		sort.SliceStable(group.Rows, func(i, j int) bool {
			a, ea := strconv.Atoi(group.Rows[i].Values["rank"])
			b, eb := strconv.Atoi(group.Rows[j].Values["rank"])
			if ea != nil {
				return false
			}
			if eb != nil {
				return true
			}
			return a < b
		})
		if len(group.Rows) > 0 {
			groups = append(groups, group)
		}
	}
	return groups
}

// fetchCFLStandings is optional and never called from the fixture score path.
// Discover the provider's season list before selecting the latest non-future
// season; table rows must independently confirm the same year.
func (s *Service) fetchCFLStandings(ctx context.Context) LeagueStandings {
	s.standings.mu.Lock()
	if s.standings.entries == nil {
		s.standings.entries = map[string]*standingsSlot{}
	}
	slot := s.standings.entries[cflLeagueID]
	if slot == nil {
		slot = &standingsSlot{}
		s.standings.entries[cflLeagueID] = slot
	}
	s.standings.mu.Unlock()
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if time.Now().Before(slot.expires) {
		return slot.value
	}
	value := LeagueStandings{League: cflLeagueID, State: "unavailable", Source: "CFL", Groups: []LeagueStandingGroup{}, Reason: "Official CFL standings are unavailable"}
	requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var seasons []int
	err := s.readCFLJSON(requestCtx, "/seasons", &seasons)
	year := 0
	if err == nil {
		for _, season := range seasons {
			if season >= 1900 && season <= time.Now().UTC().Year() && season > year {
				year = season
			}
		}
		if year == 0 {
			err = fmt.Errorf("CFL returned no known current or past season")
		}
	}
	if err == nil {
		value.SourceURL = fmt.Sprintf("%s/standings/%d", cflAPIBase, year)
		var raw cflStandingsResponse
		err = s.readCFLJSON(requestCtx, fmt.Sprintf("/standings/%d", year), &raw)
		if err == nil {
			var teams []models.SportsTeamRecord
			teams, err = s.fetchCFLTeams(requestCtx)
			if err == nil {
				value.Groups = normalizeCFLStandings(raw, teams, year)
				if len(value.Groups) == 0 {
					err = fmt.Errorf("CFL table has no named rows for season %d", year)
				}
			}
		}
	}
	ttl := 15 * time.Minute
	if err == nil {
		now := time.Now().UTC()
		value.UpdatedAt = &now
		value.State = "available"
		value.Reason = ""
	} else {
		ttl = time.Minute
		if slot.value.UpdatedAt != nil {
			value = slot.value
			value.State = "stale"
			value.Reason = "Official CFL standings refresh unavailable; showing the last successful table"
		}
	}
	slot.value = value
	slot.expires = time.Now().Add(ttl)
	return value
}
