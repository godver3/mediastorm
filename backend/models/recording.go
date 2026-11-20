package models

import "time"

type RecordingType string

const (
	RecordingTypeEPG       RecordingType = "epg"
	RecordingTypeTimeBlock RecordingType = "time_block"
)

type RecordingStatus string

const (
	RecordingStatusPending   RecordingStatus = "pending"
	RecordingStatusStarting  RecordingStatus = "starting"
	RecordingStatusRunning   RecordingStatus = "running"
	RecordingStatusCompleted RecordingStatus = "completed"
	RecordingStatusFailed    RecordingStatus = "failed"
	RecordingStatusCancelled RecordingStatus = "cancelled"
)

type Recording struct {
	ID                   string          `json:"id"`
	UserID               string          `json:"userId"`
	RuleID               string          `json:"ruleId,omitempty"`
	Type                 RecordingType   `json:"type"`
	Status               RecordingStatus `json:"status"`
	ChannelID            string          `json:"channelId"`
	TvgID                string          `json:"tvgId,omitempty"`
	ChannelName          string          `json:"channelName"`
	Title                string          `json:"title"`
	Description          string          `json:"description,omitempty"`
	SourceURL            string          `json:"sourceUrl"`
	StartAt              time.Time       `json:"startAt"`
	EndAt                time.Time       `json:"endAt"`
	PaddingBeforeSeconds int             `json:"paddingBeforeSeconds"`
	PaddingAfterSeconds  int             `json:"paddingAfterSeconds"`
	OutputPath           string          `json:"outputPath,omitempty"`
	OutputSizeBytes      int64           `json:"outputSizeBytes,omitempty"`
	ActualStartAt        *time.Time      `json:"actualStartAt,omitempty"`
	ActualEndAt          *time.Time      `json:"actualEndAt,omitempty"`
	Error                string          `json:"error,omitempty"`
	CreatedAt            time.Time       `json:"createdAt"`
	UpdatedAt            time.Time       `json:"updatedAt"`
	ScheduleKey          string          `json:"-"`
	// Guide holds what the guide said about the programme when the
	// recording was scheduled (nil for time blocks and unknown programmes).
	Guide *RecordingGuide `json:"guide,omitempty"`
}

// CommercialBreak is a commercial break comskip found in a recording, in
// seconds from the start of the file.
type CommercialBreak struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// RecordingGuide is the guide's description of a recorded programme, kept
// with the recording because the guide moves on (XMLTV fields).
type RecordingGuide struct {
	Image        string   `json:"image,omitempty"`        // <icon>, http(s) only
	EpisodeTitle string   `json:"episodeTitle,omitempty"` // <sub-title>
	Episode      string   `json:"episode,omitempty"`      // <episode-num>, e.g. "S12 E2/8"
	Year         int      `json:"year,omitempty"`         // <date>
	Categories   []string `json:"categories,omitempty"`
	Rating       string   `json:"rating,omitempty"`
}

type RecordingRuleMatchType string

const (
	RecordingRuleMatchTitle RecordingRuleMatchType = "title"
	RecordingRuleMatchRegex RecordingRuleMatchType = "regex"
)

// RecordingRule records every matching EPG airing. NewOnly skips airings the
// guide marks as reruns (see EPGProgram.New).
type RecordingRule struct {
	ID                   string                 `json:"id"`
	UserID               string                 `json:"userId"`
	MatchType            RecordingRuleMatchType `json:"matchType"`
	Pattern              string                 `json:"pattern"`
	ChannelID            string                 `json:"channelId,omitempty"`
	TvgID                string                 `json:"tvgId,omitempty"`
	ChannelName          string                 `json:"channelName,omitempty"`
	AllChannels          bool                   `json:"allChannels"`
	Enabled              bool                   `json:"enabled"`
	NewOnly              bool                   `json:"newOnly"`
	PaddingBeforeSeconds int                    `json:"paddingBeforeSeconds"`
	PaddingAfterSeconds  int                    `json:"paddingAfterSeconds"`
	CreatedAt            time.Time              `json:"createdAt"`
	UpdatedAt            time.Time              `json:"updatedAt"`
}

type CreateRecordingRuleRequest struct {
	ProfileID            string                 `json:"profileId"`
	MatchType            RecordingRuleMatchType `json:"matchType"`
	Pattern              string                 `json:"pattern"`
	ChannelID            string                 `json:"channelId,omitempty"`
	TvgID                string                 `json:"tvgId,omitempty"`
	ChannelName          string                 `json:"channelName,omitempty"`
	AllChannels          bool                   `json:"allChannels"`
	NewOnly              bool                   `json:"newOnly,omitempty"`
	PaddingBeforeSeconds *int                   `json:"paddingBeforeSeconds,omitempty"`
	PaddingAfterSeconds  *int                   `json:"paddingAfterSeconds,omitempty"`
}

type UpdateRecordingRuleRequest struct {
	ProfileID            string `json:"profileId"`
	Enabled              *bool  `json:"enabled,omitempty"`
	NewOnly              *bool  `json:"newOnly,omitempty"`
	PaddingBeforeSeconds *int   `json:"paddingBeforeSeconds,omitempty"`
	PaddingAfterSeconds  *int   `json:"paddingAfterSeconds,omitempty"`
}

type RecordingSettings struct {
	PaddingBeforeSeconds *int `json:"paddingBeforeSeconds,omitempty"`
	PaddingAfterSeconds  *int `json:"paddingAfterSeconds,omitempty"`
}

type UpdateRecordingSettingsRequest struct {
	ProfileID            string `json:"profileId"`
	PaddingBeforeSeconds int    `json:"paddingBeforeSeconds"`
	PaddingAfterSeconds  int    `json:"paddingAfterSeconds"`
}

type RecordingListFilter struct {
	UserID          string
	Statuses        []RecordingStatus
	IncludeAll      bool
	Limit           int
	OnlyStartBefore *time.Time
	// OnlyStartAfter keeps recordings starting at or after this time.
	OnlyStartAfter *time.Time
}

type CreateEPGRecordingRequest struct {
	ProfileID            string `json:"profileId"`
	ChannelID            string `json:"channelId"`
	TvgID                string `json:"tvgId"`
	ChannelName          string `json:"channelName"`
	Title                string `json:"title"`
	Description          string `json:"description"`
	SourceURL            string `json:"sourceUrl"`
	Start                string `json:"start"`
	Stop                 string `json:"stop"`
	PaddingBeforeSeconds int    `json:"paddingBeforeSeconds"`
	PaddingAfterSeconds  int    `json:"paddingAfterSeconds"`
}

type CreateTimeBlockRecordingRequest struct {
	ProfileID            string `json:"profileId"`
	ChannelID            string `json:"channelId"`
	TvgID                string `json:"tvgId"`
	ChannelName          string `json:"channelName"`
	Title                string `json:"title"`
	Description          string `json:"description"`
	SourceURL            string `json:"sourceUrl"`
	Start                string `json:"start"`
	Stop                 string `json:"stop"`
	PaddingBeforeSeconds int    `json:"paddingBeforeSeconds"`
	PaddingAfterSeconds  int    `json:"paddingAfterSeconds"`
}
