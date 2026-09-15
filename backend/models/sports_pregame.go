package models

import "time"

// SportsPregame is season/matchup context, never a live game-stat capability.
type SportsPregame struct {
	SeasonLabel     string                `json:"seasonLabel,omitempty"`
	UpdatedAt       time.Time             `json:"updatedAt"`
	Teams           []SportsPregameTeam   `json:"teams"`
	Comparisons     []SportsComparison    `json:"comparisons,omitempty"`
	PreviousMeeting *SportsPregameMeeting `json:"previousMeeting,omitempty"`
}
type SportsPregameTeam struct {
	TeamID           string                  `json:"teamId"`
	Record           string                  `json:"record,omitempty"`
	HomeRecord       string                  `json:"homeRecord,omitempty"`
	AwayRecord       string                  `json:"awayRecord,omitempty"`
	ConferenceRecord string                  `json:"conferenceRecord,omitempty"`
	Rank             int                     `json:"rank,omitempty"`
	StandingLabel    string                  `json:"standingLabel,omitempty"`
	StandingStats    []SportsPlayerStatistic `json:"standingStats,omitempty"`
	Recent           []SportsPregameResult   `json:"recent,omitempty"`
	Leaders          []SportsPregamePlayer   `json:"leaders,omitempty"`
	Probables        []SportsPregamePlayer   `json:"probables,omitempty"`
}
type SportsPregameResult struct {
	ID       string    `json:"id"`
	Date     time.Time `json:"date"`
	Opponent string    `json:"opponent"`
	Result   string    `json:"result"`
	Score    string    `json:"score,omitempty"`
}
type SportsPregamePlayer struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Value string `json:"value,omitempty"`
}
type SportsPregameMeeting struct {
	Date       time.Time `json:"date"`
	HomeTeamID string    `json:"homeTeamId"`
	AwayTeamID string    `json:"awayTeamId"`
	HomeScore  string    `json:"homeScore"`
	AwayScore  string    `json:"awayScore"`
}
