package models

import "time"

// SportsGameDetail is additive to the legacy game response. Empty data never
// implies a zero statistic; capability flags describe verified available panels.
type SportsGameDetail struct {
	Pregame     *SportsPregame          `json:"pregame,omitempty"`
	PlayerStats []SportsPlayerGameStats `json:"playerStats,omitempty"`
	// TournamentContext is the literal soccer provider season/round label, not inferred progression.
	TournamentContext string                   `json:"tournamentContext,omitempty"`
	ScoreHistory      *SportsScoreHistory      `json:"scoreHistory,omitempty"`
	FootballDrives    []SportsFootballDrive    `json:"footballDrives,omitempty"`
	Lineups           []SportsLineup           `json:"lineups,omitempty"`
	Standings         []SportsStandingGroup    `json:"standings,omitempty"`
	Leaders           []SportsDetailLeader     `json:"leaders,omitempty"`
	Sport             *SportsMLBSituation      `json:"sport,omitempty"`
	Source            string                   `json:"source"`
	UpdatedAt         time.Time                `json:"updatedAt"`
	Stale             bool                     `json:"stale"`
	Periods           []SportsPeriodScore      `json:"periods"`
	Plays             []SportsDetailPlay       `json:"plays"`
	Comparisons       []SportsComparison       `json:"comparisons"`
	Capabilities      SportsDetailCapabilities `json:"capabilities"`
}
type SportsPeriodScore struct {
	Label string `json:"label"`
	Away  string `json:"away"`
	Home  string `json:"home"`
}
type SportsPlayParticipant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type SportsPlayerGameStats struct {
	HeadshotURL string                  `json:"headshotUrl,omitempty"`
	ID          string                  `json:"id"`
	TeamID      string                  `json:"teamId"`
	Name        string                  `json:"name"`
	Category    string                  `json:"category"`
	Stats       []SportsPlayerStatistic `json:"stats"`
}
type SportsPlayerStatistic struct {
	Label string `json:"label"`
	Value string `json:"value"`
}
type SportsDetailPlay struct {
	TeamID       string                  `json:"teamId,omitempty"`
	Participants []SportsPlayParticipant `json:"participants,omitempty"`
	Shootout     bool                    `json:"shootout,omitempty"`
	Clock        string                  `json:"clock,omitempty"`
	ID           string                  `json:"id"`
	PeriodLabel  string                  `json:"periodLabel"`
	Title        string                  `json:"title"`
	Description  string                  `json:"description,omitempty"`
	Scoring      bool                    `json:"scoring"`
}
type SportsComparison struct {
	Label string `json:"label"`
	Away  string `json:"away"`
	Home  string `json:"home"`
}
type SportsDetailCapabilities struct {
	Plays          bool `json:"plays"`
	Stats          bool `json:"stats"`
	WinProbability bool `json:"winProbability"`
}

// Pointer counts preserve a real zero without inventing absent live values.
type SportsMLBSituation struct {
	BatterID          string `json:"batterId,omitempty"`
	PitcherID         string `json:"pitcherId,omitempty"`
	PitcherPitchCount *int   `json:"pitcherPitchCount,omitempty"`
	Bases             string `json:"bases,omitempty"`
	Kind              string `json:"kind"`
	Inning            string `json:"inning"`
	Balls             *int   `json:"balls,omitempty"`
	Strikes           *int   `json:"strikes,omitempty"`
	Outs              *int   `json:"outs,omitempty"`
	Pitcher           string `json:"pitcher,omitempty"`
	Batter            string `json:"batter,omitempty"`
}

type SportsDetailLeader struct {
	AthleteID        string `json:"athleteId,omitempty"`
	HeadshotURL      string `json:"headshotUrl,omitempty"`
	Label            string `json:"label"`
	Name             string `json:"name"`
	TeamAbbreviation string `json:"teamAbbreviation"`
	Value            string `json:"value"`
}

// Match lineups and table snapshots have distinct scopes; tables are not as-of-game results.
type SportsLineup struct {
	TeamID    string               `json:"teamId"`
	Formation string               `json:"formation,omitempty"`
	Players   []SportsLineupPlayer `json:"players"`
}
type SportsLineupPlayer struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Number    string `json:"number,omitempty"`
	Position  string `json:"position,omitempty"`
	Starter   bool   `json:"starter"`
	SubbedIn  bool   `json:"subbedIn,omitempty"`
	SubbedOut bool   `json:"subbedOut,omitempty"`
}
type SportsStandingGroup struct {
	Title string              `json:"title"`
	Rows  []SportsStandingRow `json:"rows"`
}
type SportsStandingRow struct {
	TeamID           string `json:"teamId"`
	Name             string `json:"name"`
	Rank             string `json:"rank,omitempty"`
	Played           string `json:"played,omitempty"`
	Points           string `json:"points,omitempty"`
	GoalDifference   string `json:"goalDifference,omitempty"`
	Record           string `json:"record,omitempty"`
	ConferenceRecord string `json:"conferenceRecord,omitempty"`
}

// Coordinates orient the away end at 0 and the home end at 100 for consistent comparison.
type SportsFootballPlay struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Clock       string   `json:"clock"`
	Period      string   `json:"period"`
	Start       *float64 `json:"start,omitempty"`
	End         *float64 `json:"end,omitempty"`
	Scoring     bool     `json:"scoring"`
}
type SportsFootballDrive struct {
	StartKnown  *bool                `json:"startKnown,omitempty"`
	EndKnown    *bool                `json:"endKnown,omitempty"`
	Plays       []SportsFootballPlay `json:"plays,omitempty"`
	ID          string               `json:"id"`
	TeamID      string               `json:"teamId"`
	Start       float64              `json:"start"`
	End         float64              `json:"end"`
	StartLabel  string               `json:"startLabel"`
	EndLabel    string               `json:"endLabel"`
	Period      string               `json:"period"`
	Result      string               `json:"result"`
	Description string               `json:"description"`
	Current     bool                 `json:"current"`
}

// SportsScoreHistory is a validated complete final snapshot, replaced on refresh.
type SportsScoreHistory struct {
	EventID    string             `json:"eventId"`
	AwayTeamID string             `json:"awayTeamId"`
	HomeTeamID string             `json:"homeTeamId"`
	Complete   bool               `json:"complete"`
	Points     []SportsScorePoint `json:"points"`
}
type SportsScorePoint struct {
	ID          string  `json:"id"`
	Sequence    string  `json:"sequence"`
	Period      int     `json:"period"`
	PeriodLabel string  `json:"periodLabel"`
	Clock       string  `json:"clock"`
	Away        float64 `json:"away"`
	Home        float64 `json:"home"`
}
