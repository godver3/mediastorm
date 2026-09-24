package sports

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type StandingColumn struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}
type LeagueStandingRow struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Abbreviation string            `json:"abbreviation,omitempty"`
	Kind         string            `json:"kind"`
	Values       map[string]string `json:"values"`
}
type LeagueStandingGroup struct {
	ID          string              `json:"id"`
	Title       string              `json:"title"`
	Season      int                 `json:"season"`
	SeasonLabel string              `json:"seasonLabel"`
	Columns     []StandingColumn    `json:"columns"`
	Rows        []LeagueStandingRow `json:"rows"`
}
type LeagueStandings struct {
	League    string                `json:"league"`
	State     string                `json:"state"`
	Source    string                `json:"source"`
	SourceURL string                `json:"sourceUrl"`
	UpdatedAt *time.Time            `json:"updatedAt,omitempty"`
	Reason    string                `json:"reason,omitempty"`
	Groups    []LeagueStandingGroup `json:"groups"`
}
type standingsCache struct {
	mu      sync.Mutex
	entries map[string]*standingsSlot
}
type standingsSlot struct {
	mu      sync.Mutex
	value   LeagueStandings
	expires time.Time
}
type standingsIdentity struct {
	ID           string `json:"id"`
	DisplayName  string `json:"displayName"`
	Name         string `json:"name"`
	Abbreviation string `json:"abbreviation"`
}
type standingsSeason int

func (season *standingsSeason) UnmarshalJSON(data []byte) error {
	var value string
	if len(data) > 0 && data[0] == '"' {
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
	} else {
		value = string(data)
	}
	number, err := strconv.Atoi(value)
	if err != nil {
		return err
	}
	*season = standingsSeason(number)
	return nil
}

type standingsNode struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Children  []standingsNode `json:"children"`
	Standings struct {
		Season  standingsSeason `json:"season"`
		Entries []struct {
			Team    standingsIdentity `json:"team"`
			Athlete standingsIdentity `json:"athlete"`
			Stats   []struct {
				Name         string `json:"name"`
				DisplayValue string `json:"displayValue"`
			} `json:"stats"`
		} `json:"entries"`
	} `json:"standings"`
}
type standingsResponse struct {
	standingsNode
	Season struct {
		Year        int    `json:"year"`
		DisplayName string `json:"displayName"`
	} `json:"season"`
}

var standingStats = []StandingColumn{{"rank", "Rank"}, {"playoffSeed", "Seed"}, {"overall", "Record"}, {"championshipPts", "Points"}, {"points", "Points"}, {"gamesPlayed", "Played"}, {"wins", "Wins"}, {"losses", "Losses"}, {"ties", "Draws"}, {"pointDifferential", "Diff"}, {"winPercent", "Win %"}, {"gamesBehind", "GB"}, {"vs. Conf.", "Conference"}, {"streak", "Streak"}, {"Home", "Home"}, {"Road", "Away"}, {"Last Ten Games", "Last 10"}}

func standingsLeague(id string) (League, bool) {
	for _, league := range LeagueCatalog {
		if league.ID == id && league.active() && ((!strings.HasPrefix(id, "espn:") && league.SupportsTeams) || league.hasCapability("standings") || id == "f1" || id == "nascar" || id == "indycar") {
			return league, true
		}
	}
	return League{}, false
}
func normalizeLeagueStandings(raw standingsResponse, league string) []LeagueStandingGroup {
	groups := []LeagueStandingGroup{}
	var walk func(standingsNode, int)
	walk = func(node standingsNode, depth int) {
		if depth > 4 || len(groups) >= 64 {
			return
		}
		if len(node.Standings.Entries) > 0 && node.Standings.Season >= 1900 && node.Standings.Season <= 2200 {
			season := int(node.Standings.Season)
			label := strconv.Itoa(season)
			if raw.Season.Year == season && raw.Season.DisplayName != "" {
				label = raw.Season.DisplayName
			}
			group := LeagueStandingGroup{ID: fmt.Sprintf("%s:%d", node.ID, len(groups)), Title: node.Name, Season: season, SeasonLabel: label, Rows: []LeagueStandingRow{}, Columns: []StandingColumn{}}
			seen := map[string]bool{}
			present := map[string]bool{}
			for _, entry := range node.Standings.Entries {
				identity, kind := entry.Team, "team"
				if identity.ID == "" {
					identity, kind = entry.Athlete, "athlete"
				}
				name := identity.DisplayName
				if name == "" {
					name = identity.Name
				}
				key := kind + ":" + identity.ID
				if identity.ID == "" || strings.TrimSpace(name) == "" || seen[key] || len(group.Rows) >= 512 {
					continue
				}
				seen[key] = true
				values := map[string]string{}
				for _, stat := range entry.Stats {
					for _, allowed := range standingStats {
						if stat.Name == allowed.Key && strings.TrimSpace(stat.DisplayValue) != "" {
							if _, exists := values[stat.Name]; !exists {
								values[stat.Name] = stat.DisplayValue
								present[stat.Name] = true
							}
							break
						}
					}
				}
				if len(values) == 0 {
					continue
				}
				group.Rows = append(group.Rows, LeagueStandingRow{identity.ID, name, identity.Abbreviation, kind, values})
			}
			// Points mean standings points only for soccer/hockey or championship points in racing.
			for _, col := range standingStats {
				if present[col.Key] && !(col.Key == "points" && !strings.HasPrefix(league, "soccer-") && !strings.HasPrefix(league, "espn:soccer:") && !strings.HasPrefix(league, "espn:hockey:") && !strings.HasPrefix(league, "espn:racing:") && league != "nhl" && league != "f1" && league != "nascar" && league != "indycar") {
					group.Columns = append(group.Columns, col)
				}
			}
			orderKey := "rank"
			if !present[orderKey] {
				orderKey = "playoffSeed"
			}
			position := func(row LeagueStandingRow) int {
				n, err := strconv.Atoi(row.Values[orderKey])
				if err != nil || n <= 0 {
					return 100000
				}
				return n
			}
			sort.SliceStable(group.Rows, func(i, j int) bool { return position(group.Rows[i]) < position(group.Rows[j]) })
			if len(group.Rows) > 0 && group.Title != "" {
				groups = append(groups, group)
			}
		}
		for _, child := range node.Children {
			walk(child, depth+1)
		}
	}
	walk(raw.standingsNode, 0)
	return groups
}
func (s *Service) GetLeagueStandings(ctx context.Context, id string) LeagueStandings {
	if id == cflLeagueID {
		return s.fetchCFLStandings(ctx)
	}
	value := LeagueStandings{League: id, State: "unavailable", Source: "ESPN", Groups: []LeagueStandingGroup{}, Reason: "Standings are not available for this competition"}
	league, ok := standingsLeague(id)
	if !ok {
		return value
	}
	value.SourceURL = "https://site.api.espn.com/apis/v2/sports/" + league.Sport + "/" + league.Slug + "/standings"
	if strings.HasPrefix(id, "espn:") {
		value.SourceURL += "?season=" + strconv.Itoa(time.Now().Year())
	}
	s.standings.mu.Lock()
	if s.standings.entries == nil {
		s.standings.entries = map[string]*standingsSlot{}
	}
	slot := s.standings.entries[id]
	if slot == nil {
		slot = &standingsSlot{}
		s.standings.entries[id] = slot
	}
	s.standings.mu.Unlock()
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if time.Now().Before(slot.expires) {
		return slot.value
	}
	requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var raw standingsResponse
	err := s.racingJSON(requestCtx, value.SourceURL, &raw)
	if err == nil {
		value.Groups = normalizeLeagueStandings(raw, id)
		if len(value.Groups) == 0 {
			err = fmt.Errorf("no valid standings")
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
			value.Reason = "Standings refresh unavailable; showing the last successful table"
		}
	}
	slot.value = value
	slot.expires = time.Now().Add(ttl)
	return value
}
