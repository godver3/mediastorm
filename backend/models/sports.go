package models

import "time"

// SportsTeam represents one side in a scheduled or in-progress game.
type SportsTeam struct {
	StandingSummary  string `json:"standingSummary,omitempty"`
	Color            string `json:"color,omitempty"`
	Rank             int    `json:"rank,omitempty"`
	Record           string `json:"record,omitempty"`
	ConferenceRecord string `json:"conferenceRecord,omitempty"`
	ShootoutScore    *int   `json:"shootoutScore,omitempty"`
	ID               string `json:"id"`
	Name             string `json:"name"`
	Location         string `json:"location,omitempty"`
	Nickname         string `json:"nickname,omitempty"`
	Abbreviation     string `json:"abbreviation,omitempty"`
	LogoURL          string `json:"logoUrl,omitempty"`
	Score            string `json:"score,omitempty"`
	Winner           bool   `json:"winner,omitempty"`
}

// SportsGameStatus is the coarse lifecycle state of a game.
type SportsGameStatus string

const (
	SportsGameScheduled SportsGameStatus = "scheduled"
	SportsGameLive      SportsGameStatus = "live"
	SportsGameFinal     SportsGameStatus = "final"
)

// SportsGame represents a single scheduled, live, or completed game.
type SportsGame struct {
	FootballSituation *SportsFootballSituation `json:"footballSituation,omitempty"`
	LiveSituation     *SportsMLBSituation      `json:"liveSituation,omitempty"`
	Detail            *SportsGameDetail        `json:"detail,omitempty"`
	ID                string                   `json:"id"`
	Title             string                   `json:"title,omitempty"`
	EventKind         string                   `json:"eventKind,omitempty"`
	League            string                   `json:"league"` // e.g. "mlb", "nfl", "nba", "nhl"
	Sport             string                   `json:"sport"`  // e.g. "baseball"
	StartTime         time.Time                `json:"startTime"`
	Status            SportsGameStatus         `json:"status"`
	StatusDetail      string                   `json:"statusDetail,omitempty"` // e.g. "9th - 0:00", "FINAL", "7:05 PM"
	Period            string                   `json:"period,omitempty"`
	Clock             string                   `json:"clock,omitempty"`
	HomeTeam          SportsTeam               `json:"homeTeam"`
	AwayTeam          SportsTeam               `json:"awayTeam"`
	Broadcasts        []string                 `json:"broadcasts,omitempty"` // e.g. ["ESPN", "MLB.TV"]
	VenueName         string                   `json:"venue,omitempty"`
	Participants      []SportsParticipant      `json:"participants,omitempty"`
}

// SportsLeague identifies a supported league/competition.
type SportsLeague struct {
	ID            string `json:"id"` // e.g. "mlb"
	Name          string `json:"name"`
	Sport         string `json:"sport"`
	Category      string `json:"category"`
	EventKind     string `json:"eventKind"` // matchup | fight-card | tournament | race
	SupportsTeams bool   `json:"supportsTeams"`
	Enabled       bool   `json:"enabled"`
}

// SportsParticipant and SportsEvent are the additive, generalized Sports Hub contract.
// The legacy SportsGame endpoints remain intact for existing mobile clients.
type SportsParticipant struct {
	Number       string                `json:"number,omitempty"`
	Team         string                `json:"team,omitempty"`
	Result       string                `json:"result,omitempty"`
	Statistics   []SportsRaceStatistic `json:"statistics,omitempty"`
	ID           string                `json:"id"`
	Name         string                `json:"name"`
	Abbreviation string                `json:"abbreviation,omitempty"`
	LogoURL      string                `json:"logoUrl,omitempty"`
	Score        string                `json:"score,omitempty"`
	Position     int                   `json:"position,omitempty"`
	Winner       bool                  `json:"winner,omitempty"`
}

type SportsRaceStatistic struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Value string `json:"value"`
}

// SportsCircuit is licensed community reference geometry, never live tracking.
type SportsCircuit struct {
	ID                    string       `json:"id"`
	Name                  string       `json:"name"`
	Location              string       `json:"location"`
	ReferenceLengthMeters int          `json:"referenceLengthMeters"`
	Coordinates           [][2]float64 `json:"coordinates"`
	SourceURL             string       `json:"sourceUrl"`
	Attribution           string       `json:"attribution"`
}

type SportsEvent struct {
	Circuit         *SportsCircuit      `json:"circuit,omitempty"`
	ProviderEventID string              `json:"providerEventId,omitempty"`
	SessionID       string              `json:"sessionId,omitempty"`
	SessionType     string              `json:"sessionType,omitempty"`
	UpdatedAt       time.Time           `json:"updatedAt,omitempty"`
	Stale           bool                `json:"stale,omitempty"`
	ID              string              `json:"id"`
	Title           string              `json:"title"`
	League          string              `json:"league"`
	Sport           string              `json:"sport"`
	EventKind       string              `json:"eventKind"`
	StartTime       time.Time           `json:"startTime"`
	Status          SportsGameStatus    `json:"status"`
	StatusDetail    string              `json:"statusDetail,omitempty"`
	Participants    []SportsParticipant `json:"participants"`
	SubEvents       []SportsEvent       `json:"subEvents,omitempty"`
	Broadcasts      []string            `json:"broadcasts,omitempty"`
	VenueName       string              `json:"venue,omitempty"`
	Featured        bool                `json:"featured,omitempty"`
}

// SportsStreamMatch is one candidate Live TV channel/stream for watching a given game,
// found by heuristically matching the game's teams/broadcasts against the user's
// configured Live TV channels and EPG now-playing data. Best-effort, not authoritative.
type SportsReportedQuality struct {
	ResolutionHeight int    `json:"resolutionHeight,omitempty"`
	BitrateBps       int64  `json:"bitrateBps,omitempty"`
	Origin           string `json:"origin"`
}

type SportsStreamMatch struct {
	ReportedQuality *SportsReportedQuality `json:"reportedQuality,omitempty"`
	ChannelID       string                 `json:"channelId"`
	ChannelName     string                 `json:"channelName"`
	ChannelURL      string                 `json:"channelUrl"`
	ChannelLogo     string                 `json:"channelLogo,omitempty"`
	ChannelTvgID    string                 `json:"channelTvgId,omitempty"`
	SourceID        string                 `json:"sourceId,omitempty"`
	SourceName      string                 `json:"sourceName,omitempty"`
	ProgramTitle    string                 `json:"programTitle,omitempty"`
	MatchedOn       string                 `json:"matchedOn"`             // "broadcast" | "team-name" | "matchup-name" | "epg-title"
	Broadcast       string                 `json:"broadcast,omitempty"`   // set when MatchedOn == "broadcast": which broadcast name matched
	MatchedTeam     string                 `json:"matchedTeam,omitempty"` // set when MatchedOn == "team-name" or "epg-title": which team's name matched
	Confidence      float64                `json:"confidence"`
	ConfidenceTier  string                 `json:"confidenceTier"` // "strong" | "possible"
	MatchReason     string                 `json:"matchReason"`
	MatchedTerms    []string               `json:"matchedTerms,omitempty"`
	LifecycleState  string                 `json:"lifecycleState,omitempty"`
}

// SportsStreamGroup collapses equivalent playlist entries while preserving every distinct
// source/URL as an explicit alternative. The legacy flat streams response remains available
// for clients that do not understand groups yet.
type SportsStreamGroup struct {
	GroupKey       string              `json:"groupKey"`
	DisplayName    string              `json:"displayName"`
	Confidence     float64             `json:"confidence"`
	ConfidenceTier string              `json:"confidenceTier"`
	MatchReason    string              `json:"matchReason"`
	FeedCount      int                 `json:"feedCount"`
	Primary        SportsStreamMatch   `json:"primary"`
	Alternatives   []SportsStreamMatch `json:"alternatives"`
}

// SportsStatus reports the health of the sports scoreboard service.
type SportsStatus struct {
	DateQueries bool       `json:"dateQueries"`
	Enabled     bool       `json:"enabled"`
	LastRefresh *time.Time `json:"lastRefresh,omitempty"`
	LastError   string     `json:"lastError,omitempty"`
	GameCount   int        `json:"gameCount"`
	Refreshing  bool       `json:"refreshing"`
}

// SportsTeamRecord is a persisted team identity (Postgres sports_teams table), keyed on
// league+ESPN team ID rather than name so it survives the team being renamed/rebranded
// upstream. Populated from ESPN's complete league team catalog, with scoreboard teams as
// an additional fallback so the management UI is complete even on light schedule days.
type SportsTeamRecord struct {
	ID           string    `json:"id"` // "{league}:{espnTeamId}"
	League       string    `json:"league"`
	EspnTeamID   string    `json:"espnTeamId"`
	Name         string    `json:"name"`
	Location     string    `json:"location,omitempty"`
	Nickname     string    `json:"nickname,omitempty"`
	Abbreviation string    `json:"abbreviation,omitempty"`
	LogoURL      string    `json:"logoUrl,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// SportsChannelCandidate is a ranked Live TV channel suggestion for a team.
// Explicit text searches may include candidates below the normal confidence floor so
// administrators can always make a manual selection.
type SportsChannelCandidate struct {
	ChannelID      string   `json:"channelId"`
	ChannelName    string   `json:"channelName"`
	ChannelURL     string   `json:"channelUrl"`
	ChannelLogo    string   `json:"channelLogo,omitempty"`
	ChannelTvgID   string   `json:"channelTvgId,omitempty"`
	SourceID       string   `json:"sourceId,omitempty"`
	SourceName     string   `json:"sourceName,omitempty"`
	Group          string   `json:"group,omitempty"`
	Confidence     float64  `json:"confidence"`
	ConfidenceTier string   `json:"confidenceTier"`
	MatchReason    string   `json:"matchReason"`
	MatchedTerms   []string `json:"matchedTerms,omitempty"`
}

type SportsAutoLinkTeamResult struct {
	Team               SportsTeamRecord        `json:"team"`
	Candidate          *SportsChannelCandidate `json:"candidate,omitempty"`
	RunnerUpConfidence float64                 `json:"runnerUpConfidence,omitempty"`
	ConfidenceGap      float64                 `json:"confidenceGap,omitempty"`
	Eligible           bool                    `json:"eligible"`
	SkipReason         string                  `json:"skipReason,omitempty"`
}

type SportsAutoLinkPreview struct {
	League        string                     `json:"league"`
	Message       string                     `json:"message,omitempty"`
	EligibleCount int                        `json:"eligibleCount"`
	SkippedCount  int                        `json:"skippedCount"`
	Results       []SportsAutoLinkTeamResult `json:"results"`
}

// SportsChannelLinkSlot is which position a linked channel occupies for a team.
type SportsChannelLinkSlot string

const (
	SportsLinkSlotPrimary SportsChannelLinkSlot = "primary"
	SportsLinkSlotBackup  SportsChannelLinkSlot = "backup"
)

// SportsTeamChannelLink is one Live TV channel permanently linked to a team (Postgres
// sports_team_channel_links table) - the "Manage Team Channels" feature. Channel identity
// is stored denormalized (not just a foreign key) because M3U/Xtream channel IDs regenerate
// on every playlist parse; Resolved reports whether channel_tvg_id/channel_name still
// matches something in the caller's current live channel list at read time.
type SportsTeamChannelLink struct {
	ID             string                `json:"id"`
	TeamID         string                `json:"teamId"`
	Slot           SportsChannelLinkSlot `json:"slot"`
	Position       int                   `json:"position"`
	ChannelTvgID   string                `json:"channelTvgId,omitempty"`
	ChannelName    string                `json:"channelName"`
	ChannelURL     string                `json:"channelUrl"`
	ChannelLogo    string                `json:"channelLogo,omitempty"`
	SourceID       string                `json:"sourceId,omitempty"`
	SourceName     string                `json:"sourceName,omitempty"`
	AutoLinked     bool                  `json:"autoLinked"`
	LinkConfidence float64               `json:"linkConfidence,omitempty"`
	MatchReason    string                `json:"matchReason,omitempty"`
	LastVerifiedAt *time.Time            `json:"lastVerifiedAt,omitempty"`
	CreatedAt      time.Time             `json:"createdAt"`
	UpdatedAt      time.Time             `json:"updatedAt"`
	Resolved       bool                  `json:"resolved"`
}

// SportsTeamWithLinks is a team plus its current channel links, for the "Manage Team
// Channels" list view.
type SportsTeamWithLinks struct {
	SportsTeamRecord
	Links []SportsTeamChannelLink `json:"links"`
}

// Optional counts distinguish an unavailable timeout count from zero remaining.
type SportsFootballSituation struct {
	Kind          string `json:"kind"`
	Possession    string `json:"possession"`
	DownDistance  string `json:"downDistance"`
	FieldPosition string `json:"fieldPosition"`
	AwayTimeouts  *int   `json:"awayTimeouts,omitempty"`
	HomeTimeouts  *int   `json:"homeTimeouts,omitempty"`
}
