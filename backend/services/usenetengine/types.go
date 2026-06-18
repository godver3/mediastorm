package usenetengine

import (
	"context"
	"net/http"
)

type Status string

const (
	StatusQueued     Status = "queued"
	StatusProcessing Status = "processing"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
	StatusUnknown    Status = "unknown"
)

type SubmitRequest struct {
	FileName string
	NZB      []byte
	Category string
	Priority string
}

type SubmitResult struct {
	JobID string
}

type JobStatus struct {
	JobID      string
	Status     Status
	RawStatus  string
	Progress   float64
	FileName   string
	Category   string
	SizeBytes  int64
	Error      string
	OutputPath string
}

type Engine interface {
	Name() string
	SubmitNZB(ctx context.Context, req SubmitRequest) (*SubmitResult, error)
	Status(ctx context.Context, jobID string) (*JobStatus, error)
	Delete(ctx context.Context, jobID string) error
}

type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}
