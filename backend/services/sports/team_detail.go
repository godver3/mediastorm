package sports

import (
	"encoding/json"
	"fmt"
	"math"
	"novastream/models"
	"strconv"
	"strings"
	"time"
)

type teamPlayPoint struct {
	Down           *int     `json:"down"`
	Distance       *float64 `json:"distance"`
	PossessionText string   `json:"possessionText"`
	YardsToEndzone *float64 `json:"yardsToEndzone"`
	Team           struct {
		ID string `json:"id"`
	} `json:"team"`
}
type teamDetailPlay struct {
	ShootingPlay    bool     `json:"shootingPlay"`
	PointsAttempted int      `json:"pointsAttempted"`
	Wallclock       string   `json:"wallclock"`
	StatYardage     *float64 `json:"statYardage"`
	Coordinate      *struct {
		X *float64 `json:"x"`
		Y *float64 `json:"y"`
	} `json:"coordinate"`
	Start teamPlayPoint `json:"start"`
	End   teamPlayPoint `json:"end"`
	Team  struct {
		ID string `json:"id"`
	} `json:"team"`
	Participants []struct {
		Athlete struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
		} `json:"athlete"`
	} `json:"participants"`
	Shootout  bool     `json:"shootout"`
	Sequence  string   `json:"sequenceNumber"`
	AwayScore *float64 `json:"awayScore"`
	HomeScore *float64 `json:"homeScore"`
	ID        string   `json:"id"`
	Text      string   `json:"text"`
	Scoring   bool     `json:"scoringPlay"`
	Type      struct {
		Text string `json:"text"`
		ID   string `json:"id"`
	} `json:"type"`
	Period struct {
		Number       int    `json:"number"`
		DisplayValue string `json:"displayValue"`
	} `json:"period"`
	Clock struct {
		DisplayValue string `json:"displayValue"`
	} `json:"clock"`
}
type teamDrivePoint struct {
	Text   string `json:"text"`
	Period struct {
		Number int `json:"number"`
	} `json:"period"`
}
type teamDetailDrive struct {
	OffensivePlays *int     `json:"offensivePlays"`
	Yards          *float64 `json:"yards"`
	TimeElapsed    struct {
		DisplayValue string `json:"displayValue"`
	} `json:"timeElapsed"`
	ID   string `json:"id"`
	Team struct {
		ID string `json:"id"`
	} `json:"team"`
	Start       teamDrivePoint `json:"start"`
	End         teamDrivePoint `json:"end"`
	Result      string         `json:"result"`
	Description string         `json:"description"`

	Plays []teamDetailPlay `json:"plays"`
}
type teamSportSummary struct {
	WinProbability []struct {
		Home   *float64 `json:"homeWinPercentage"`
		PlayID string   `json:"playId"`
	} `json:"winprobability"`
	Rosters   []teamContextRoster  `json:"rosters"`
	Standings teamContextStandings `json:"standings"`
	Header    struct {
		Season struct {
			Name string `json:"name"`
		} `json:"season"`
		ID           string `json:"id"`
		Competitions []struct {
			Status      espnStatus `json:"status"`
			Competitors []struct {
				ID            string              `json:"id"`
				Record        []teamContextRecord `json:"record"`
				ShootoutScore *float64            `json:"shootoutScore"`
				HomeAway      string              `json:"homeAway"`
				Score         json.RawMessage     `json:"score"`
				Winner        bool                `json:"winner"`
				Linescores    []struct {
					DisplayValue string `json:"displayValue"`
				} `json:"linescores"`
			} `json:"competitors"`
		} `json:"competitions"`
	} `json:"header"`
	Boxscore struct {
		Players []playerBoxscoreTeam `json:"players"`
		Teams   []struct {
			Team struct {
				ID string `json:"id"`
			} `json:"team"`
			Statistics []struct {
				Stats []struct {
					Name         string `json:"name"`
					DisplayValue string `json:"displayValue"`
				} `json:"stats"`
				Name         string `json:"name"`
				DisplayValue string `json:"displayValue"`
			} `json:"statistics"`
		} `json:"teams"`
	} `json:"boxscore"`
	KeyEvents []teamDetailPlay `json:"keyEvents"`
	Plays     []teamDetailPlay `json:"plays"`
	Drives    struct {
		Previous []teamDetailDrive `json:"previous"`
		Current  *teamDetailDrive  `json:"current"`
	} `json:"drives"`
	Leaders []struct {
		Team struct {
			ID string `json:"id"`
		} `json:"team"`
		Leaders []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
			Leaders     []struct {
				DisplayValue string `json:"displayValue"`
				Athlete      struct {
					DisplayName string         `json:"displayName"`
					ID          string         `json:"id"`
					Headshot    playerHeadshot `json:"headshot"`
				} `json:"athlete"`
			} `json:"leaders"`
		} `json:"leaders"`
	} `json:"leaders"`
}

func teamPeriodLabel(league string, period int) string {
	if strings.HasPrefix(league, "espn:") && !strings.HasPrefix(league, "espn:soccer:") {
		for _, l := range LeagueCatalog {
			if l.ID == league {
				return periodLabel(period, l.Sport)
			}
		}
	}
	if period <= 0 {
		return ""
	}
	if strings.HasPrefix(league, "soccer-") || strings.HasPrefix(league, "espn:soccer:") {
		switch period {
		case 1:
			return "H1"
		case 2:
			return "H2"
		case 3:
			return "ET1"
		case 4:
			return "ET2"
		case 5:
			return "PEN"
		default:
			return ""
		}
	}
	regulation := 4
	prefix := "Q"
	if league == "mens-college-basketball" || strings.HasPrefix(league, "soccer-") {
		regulation = 2
		prefix = "H"
	}
	if league == "nhl" {
		regulation = 3
		prefix = "P"
	}
	if period <= regulation {
		return prefix + strconv.Itoa(period)
	}
	if period == regulation+1 {
		return "OT"
	}
	return strconv.Itoa(period-regulation) + "OT"
}

// normalizeTeamDetail uses a single summary snapshot for scores and game stats.
// Live-only possession, bonus and penalty clocks remain absent until sourced.
func normalizeTeamDetail(game models.SportsGame, p teamSportSummary, now time.Time) (models.SportsGame, error) {
	if !supportsHubLeague(game.League) || game.League == "mlb" {
		return game, fmt.Errorf("unsupported detail league")
	}
	if p.Header.ID != providerEventID(game) || len(p.Header.Competitions) != 1 {
		return game, fmt.Errorf("summary identity mismatch")
	}
	c := p.Header.Competitions[0]
	lines := map[string][]string{}
	for _, t := range c.Competitors {
		expected := game.HomeTeam.ID
		if t.HomeAway == "away" {
			expected = game.AwayTeam.ID
		} else if t.HomeAway != "home" {
			return game, fmt.Errorf("invalid side")
		}
		if expected != t.ID {
			return game, fmt.Errorf("team identity mismatch")
		}
		if _, exists := lines[t.HomeAway]; exists {
			return game, fmt.Errorf("duplicate side")
		}
		line := make([]string, len(t.Linescores))
		for i, v := range t.Linescores {
			line[i] = v.DisplayValue
		}
		lines[t.HomeAway] = line
		if game.League == "nfl" || strings.Contains(game.League, "college") {
			if t.HomeAway == "away" {
				applyTeamRecords(&game.AwayTeam, t.Record)
			} else {
				applyTeamRecords(&game.HomeTeam, t.Record)
			}
		}
		if t.HomeAway == "away" {
			game.AwayTeam.Score = espnScore(t.Score)
			game.AwayTeam.ShootoutScore = soccerShootoutScore(t.ShootoutScore)
			game.AwayTeam.Winner = t.Winner
		} else {
			game.HomeTeam.Score = espnScore(t.Score)
			game.HomeTeam.ShootoutScore = soccerShootoutScore(t.ShootoutScore)
			game.HomeTeam.Winner = t.Winner
		}
	}
	if len(lines) != 2 || c.Status.Type.State == "" {
		return game, fmt.Errorf("incomplete matchup")
	}
	game.Status = espnStatusToGameStatus(c.Status.Type)
	game.StatusDetail = c.Status.Type.Detail
	if c.Status.DisplayClock != "" {
		game.Clock = c.Status.DisplayClock
	}
	if c.Status.Period > 0 {
		game.Period = teamPeriodLabel(game.League, c.Status.Period)
	}
	d := &models.SportsGameDetail{Source: "espn", UpdatedAt: now, Periods: []models.SportsPeriodScore{}, Plays: []models.SportsDetailPlay{}, Comparisons: []models.SportsComparison{}, Leaders: []models.SportsDetailLeader{}}
	if strings.HasPrefix(game.League, "soccer-") {
		d.TournamentContext = strings.TrimSpace(p.Header.Season.Name)
	}
	d.FootballDrives = normalizeFootballDrives(game, p)
	previousDetail := game.Detail
	game.Detail = d
	normalizeTeamContext(&game, p)
	// Pregame summaries contain season leaders and statistics, not game values.
	if game.Status == models.SportsGameScheduled {
		return game, nil
	}
	for i := 0; i < max(len(lines["away"]), len(lines["home"])); i++ {
		a, h := "–", "–"
		if i < len(lines["away"]) && lines["away"][i] != "" {
			a = lines["away"][i]
		}
		if i < len(lines["home"]) && lines["home"][i] != "" {
			h = lines["home"][i]
		}
		d.Periods = append(d.Periods, models.SportsPeriodScore{Label: teamPeriodLabel(game.League, i+1), Away: a, Home: h})
	}
	plays := p.Plays
	if strings.HasPrefix(game.League, "soccer-") {
		plays = p.KeyEvents
	}
	family := game.League
	if game.League == "college-football" {
		family = "nfl"
	}
	if game.League == "wnba" || strings.Contains(game.League, "college-basketball") {
		family = "nba"
	}
	if strings.HasPrefix(game.League, "soccer-") {
		family = "soccer"
	}
	if strings.HasPrefix(game.League, "rugby-") && !strings.HasPrefix(game.League, "rugby-league-") {
		family = "rugby"
	}
	if strings.HasPrefix(game.League, "rugby-league-") {
		family = "rugby-league"
	}
	if family == "nfl" {
		plays = nil
		for _, drive := range p.Drives.Previous {
			plays = append(plays, drive.Plays...)
		}
		if p.Drives.Current != nil {
			plays = append(plays, p.Drives.Current.Plays...)
		}
	}
	d.PlayerStats = normalizePlayerGameStats(game, p.Boxscore.Players)
	d.PlayerStats = append(d.PlayerStats, normalizeSoccerPlayerStats(game, p.Rosters)...)
	d.ScoreHistory = normalizeScoreHistory(game, p)
	if family == "nba" {
		normalizeBasketballTracking(game, p, d)
	}
	seen := map[string]bool{}
	for i := len(plays) - 1; i >= 0 && len(d.Plays) < 200; i-- {
		play := plays[i]
		if play.ID == "" || play.Text == "" || seen[play.ID] {
			continue
		}
		seen[play.ID] = true
		row := models.SportsDetailPlay{ID: play.ID, PeriodLabel: teamPeriodLabel(game.League, play.Period.Number), Clock: play.Clock.DisplayValue, Title: play.Type.Text, Description: play.Text, Scoring: play.Scoring, Shootout: play.Shootout}
		if play.Team.ID == game.AwayTeam.ID || play.Team.ID == game.HomeTeam.ID {
			row.TeamID = play.Team.ID
		}
		if family == "nfl" && play.Scoring {
			// Score deltas identify the scoring side even on defensive returns.
			// Never infer it from possession or the drive owner.
			row.TeamID = footballScoringTeam(play, plays[:i], game)
		}
		for _, person := range play.Participants {
			if person.Athlete.ID != "" && person.Athlete.DisplayName != "" {
				row.Participants = append(row.Participants, models.SportsPlayParticipant{ID: person.Athlete.ID, Name: person.Athlete.DisplayName})
			}
		}
		d.Plays = append(d.Plays, row)
	}
	stats := map[string]map[string]string{}
	for _, team := range p.Boxscore.Teams {
		values := map[string]string{}
		for _, stat := range team.Statistics {
			values[stat.Name] = stat.DisplayValue
			for _, nested := range stat.Stats {
				values[nested.Name] = nested.DisplayValue
			}
		}
		stats[team.Team.ID] = values
	}
	fields := map[string][][2]string{
		"rugby-league": {{"tries", "Tries"}, {"conversionGoals", "Conversions"}, {"tackles", "Tackles"}, {"missedTackles", "Missed tackles"}, {"metres", "Meters run"}, {"cleanBreaks", "Clean breaks"}, {"offload", "Offloads"}, {"penaltiesConceded", "Penalties conceded"}},
		"rugby":        {{"possession", "Possession %"}, {"territory", "Territory %"}, {"tries", "Tries"}, {"conversionGoals", "Conversions"}, {"tackles", "Tackles"}, {"metres", "Meters run"}, {"penaltiesConceded", "Penalties conceded"}, {"lineoutsWon", "Lineouts won"}, {"scrumsWon", "Scrums won"}},
		"soccer":       {{"possessionPct", "Possession %"}, {"totalShots", "Shots"}, {"shotsOnTarget", "Shots on target"}, {"wonCorners", "Corners"}, {"foulsCommitted", "Fouls"}, {"yellowCards", "Yellow cards"}, {"redCards", "Red cards"}, {"saves", "Saves"}, {"offsides", "Offsides"}, {"totalPasses", "Passes"}, {"passPct", "Pass accuracy %"}, {"totalCrosses", "Crosses"}, {"crossPct", "Cross accuracy %"}, {"totalTackles", "Tackles"}, {"interceptions", "Interceptions"}, {"blockedShots", "Blocked shots"}, {"totalClearance", "Clearances"}},
		"nfl":          {{"totalYards", "Total yards"}, {"netPassingYards", "Passing yards"}, {"rushingYards", "Rushing yards"}, {"firstDowns", "First downs"}, {"thirdDownEff", "Third downs"}, {"fourthDownEff", "Fourth downs"}, {"turnovers", "Turnovers"}, {"totalPenaltiesYards", "Penalties–yards"}, {"possessionTime", "Time of possession"}},
		"nba":          {{"fieldGoalsMade-fieldGoalsAttempted", "Field goals"}, {"fieldGoalPct", "Field goal %"}, {"threePointFieldGoalsMade-threePointFieldGoalsAttempted", "Three-pointers"}, {"freeThrowsMade-freeThrowsAttempted", "Free throws"}, {"totalRebounds", "Rebounds"}, {"assists", "Assists"}, {"steals", "Steals"}, {"blocks", "Blocks"}, {"totalTurnovers", "Turnovers"}, {"pointsInPaint", "Points in paint"}},
		"nhl":          {{"shotsTotal", "Shots on goal"}, {"hits", "Hits"}, {"blockedShots", "Blocked shots"}, {"faceoffPercent", "Faceoff win %"}, {"powerPlayGoals", "Power-play goals"}, {"powerPlayOpportunities", "Power-play chances"}, {"penaltyMinutes", "Penalty minutes"}, {"giveaways", "Giveaways"}, {"takeaways", "Takeaways"}},
	}[family]
	for _, field := range fields {
		a, h := stats[game.AwayTeam.ID][field[0]], stats[game.HomeTeam.ID][field[0]]
		if a != "" && h != "" {
			d.Comparisons = append(d.Comparisons, models.SportsComparison{Label: field[1], Away: a, Home: h})
		}
	}
	for _, group := range p.Leaders {
		abbreviation := ""
		if group.Team.ID == game.AwayTeam.ID {
			abbreviation = game.AwayTeam.Abbreviation
		} else if group.Team.ID == game.HomeTeam.ID {
			abbreviation = game.HomeTeam.Abbreviation
		} else {
			continue
		}
		for _, category := range group.Leaders {
			if family == "nfl" && category.Name != "passingYards" && category.Name != "rushingYards" && category.Name != "receivingYards" {
				continue
			}
			for _, leader := range category.Leaders {
				if category.DisplayName == "" || leader.Athlete.DisplayName == "" || leader.DisplayValue == "" {
					continue
				}
				d.Leaders = append(d.Leaders, models.SportsDetailLeader{Label: category.DisplayName, Name: leader.Athlete.DisplayName, AthleteID: leader.Athlete.ID, HeadshotURL: string(leader.Athlete.Headshot), TeamAbbreviation: abbreviation, Value: leader.DisplayValue})
				break
			}
		}
	}
	if family == "rugby" && previousDetail != nil && len(d.Plays) == 0 {
		d.Plays = previousDetail.Plays
	}
	if game.League == "nhl" {
		d.Shots = normalizeHockeyShots(p.Plays, game.AwayTeam.ID, game.HomeTeam.ID)
	}
	d.Capabilities.Plays = len(d.Plays) > 0
	d.Capabilities.Stats = len(d.Comparisons) > 0 || len(d.Leaders) > 0 || len(d.PlayerStats) > 0
	return game, nil
}

func footballFieldPoint(label string, game models.SportsGame) (float64, bool) {
	fields := strings.Fields(label)
	if len(fields) == 1 && fields[0] == "50" {
		return 50, true
	}
	if len(fields) != 2 {
		return 0, false
	}
	yards, err := strconv.ParseFloat(fields[1], 64)
	if err != nil || math.IsNaN(yards) || yards < 0 || yards > 50 {
		return 0, false
	}
	if fields[0] == game.AwayTeam.Abbreviation && fields[0] != "" {
		return yards, true
	}
	if fields[0] == game.HomeTeam.Abbreviation && fields[0] != "" {
		return 100 - yards, true
	}
	return 0, false
}

// ESPN yardsToEndzone is relative to the point's owning team, including turnovers.
func footballPlayPoint(point teamPlayPoint, game models.SportsGame) *float64 {
	if point.PossessionText != "" {
		if value, ok := footballFieldPoint(point.PossessionText, game); ok {
			return &value
		}
	}
	if point.YardsToEndzone == nil || math.IsNaN(*point.YardsToEndzone) || math.IsInf(*point.YardsToEndzone, 0) || *point.YardsToEndzone < 0 || *point.YardsToEndzone > 100 {
		return nil
	}
	value := 100 - *point.YardsToEndzone
	if point.Team.ID == game.HomeTeam.ID && point.Team.ID != "" {
		value = 100 - value
	} else if point.Team.ID != game.AwayTeam.ID || point.Team.ID == "" {
		return nil
	}
	return &value
}
func normalizeFootballDrives(game models.SportsGame, p teamSportSummary) []models.SportsFootballDrive {
	if game.Status == models.SportsGameScheduled || (game.League != "nfl" && game.League != "college-football") {
		return nil
	}
	rows := []models.SportsFootballDrive{}
	seen := map[string]bool{}
	add := func(drive teamDetailDrive, current bool) {
		start, okStart := footballFieldPoint(drive.Start.Text, game)
		end, okEnd := footballFieldPoint(drive.End.Text, game)
		if drive.ID == "" || seen[drive.ID] || len(rows) >= 80 || (drive.Team.ID != game.AwayTeam.ID && drive.Team.ID != game.HomeTeam.ID) {
			return
		}
		seen[drive.ID] = true
		row := models.SportsFootballDrive{PlayCount: drive.OffensivePlays, Yards: drive.Yards, Elapsed: drive.TimeElapsed.DisplayValue, ID: drive.ID, TeamID: drive.Team.ID, Start: start, End: end, StartKnown: &okStart, EndKnown: &okEnd, StartLabel: drive.Start.Text, EndLabel: drive.End.Text, Period: teamPeriodLabel(game.League, drive.Start.Period.Number), Result: drive.Result, Description: drive.Description, Current: current}
		seenPlays := map[string]bool{}
		for _, play := range drive.Plays {
			if play.ID == "" || seenPlays[play.ID] {
				continue
			}
			seenPlays[play.ID] = true
			row.Plays = append(row.Plays, models.SportsFootballPlay{TypeID: play.Type.ID, Wallclock: play.Wallclock, Down: play.Start.Down, Distance: play.Start.Distance, Yards: play.StatYardage, PossessionTeamID: play.Start.Team.ID, ID: play.ID, Type: play.Type.Text, Description: play.Text, Clock: play.Clock.DisplayValue, Period: teamPeriodLabel(game.League, play.Period.Number), Start: footballPlayPoint(play.Start, game), End: footballPlayPoint(play.End, game), Scoring: play.Scoring})
		}
		rows = append(rows, row)
	}
	for _, drive := range p.Drives.Previous {
		// The provider repeats the active drive in previous; its current snapshot wins.
		if game.Status == models.SportsGameLive && p.Drives.Current != nil && drive.ID == p.Drives.Current.ID {
			continue
		}
		add(drive, false)
	}
	if p.Drives.Current != nil && game.Status == models.SportsGameLive {
		add(*p.Drives.Current, true)
	}
	return rows
}

func footballScoringTeam(play teamDetailPlay, earlier []teamDetailPlay, game models.SportsGame) string {
	if !play.Scoring || play.AwayScore == nil || play.HomeScore == nil {
		return ""
	}
	for i := len(earlier) - 1; i >= 0; i-- {
		previous := earlier[i]
		if previous.ID == play.ID || previous.AwayScore == nil || previous.HomeScore == nil {
			continue
		}
		away := *play.AwayScore - *previous.AwayScore
		home := *play.HomeScore - *previous.HomeScore
		if away > 0 && away <= 8 && home == 0 {
			return game.AwayTeam.ID
		}
		if home > 0 && home <= 8 && away == 0 {
			return game.HomeTeam.ID
		}
		return ""
	}
	return ""
}

func normalizeHockeyShots(plays []teamDetailPlay, awayID, homeID string) []models.SportsHockeyShot {
	out := []models.SportsHockeyShot{}
	seen := map[string]bool{}
	for _, p := range plays {
		kind := strings.ToLower(p.Type.Text)
		if kind != "shot" && kind != "goal" && kind != "missed" && kind != "blocked" {
			continue
		}
		if p.ID == "" || seen[p.ID] || p.Coordinate == nil || p.Coordinate.X == nil || p.Coordinate.Y == nil {
			continue
		}
		x, y := *p.Coordinate.X, *p.Coordinate.Y
		if math.IsNaN(x) || math.IsNaN(y) || math.Abs(x) > 100 || math.Abs(y) > 42.5 {
			continue
		}
		teamID := p.Team.ID
		if teamID != awayID && teamID != homeID {
			continue
		}
		// ESPN credits a blocked event to the defending team; the attempt belongs to its opponent.
		if kind == "blocked" {
			if teamID == awayID {
				teamID = homeID
			} else {
				teamID = awayID
			}
		}
		seen[p.ID] = true
		out = append(out, models.SportsHockeyShot{ID: p.ID, TeamID: teamID, X: x, Y: y, Kind: kind, Period: p.Period.DisplayValue, Clock: p.Clock.DisplayValue, Description: p.Text})
	}
	return out
}
