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
}

type RecordingListFilter struct {
	UserID          string
	Statuses        []RecordingStatus
	IncludeAll      bool
	Limit           int
	OnlyStartBefore *time.Time
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
