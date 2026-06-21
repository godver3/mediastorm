package usenetengine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

type SABConfig struct {
	Name            string
	BaseURL         string
	APIPath         string
	FileFieldName   string
	CategoryInQuery bool
	APIKey          string
	APIKeyAsBearer  bool
	Username        string
	Password        string
	UsernameParam   string
	PasswordParam   string
}

type SABClient struct {
	cfg        SABConfig
	httpClient HTTPDoer
}

func NewSABClient(cfg SABConfig, httpClient HTTPDoer) (*SABClient, error) {
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}
	if cfg.APIPath == "" {
		cfg.APIPath = "/api"
	}
	if cfg.Name == "" {
		cfg.Name = "sab-compatible"
	}
	if strings.TrimSpace(cfg.FileFieldName) == "" {
		cfg.FileFieldName = "nzbfile"
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &SABClient{cfg: cfg, httpClient: httpClient}, nil
}

func (c *SABClient) Name() string {
	return c.cfg.Name
}

func (c *SABClient) SubmitNZB(ctx context.Context, req SubmitRequest) (*SubmitResult, error) {
	if len(req.NZB) == 0 {
		return nil, fmt.Errorf("nzb payload is empty")
	}
	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = "download.nzb"
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(c.cfg.FileFieldName, fileName)
	if err != nil {
		return nil, fmt.Errorf("create multipart file: %w", err)
	}
	if _, err := part.Write(req.NZB); err != nil {
		return nil, fmt.Errorf("write nzb payload: %w", err)
	}
	category := strings.TrimSpace(req.Category)
	if category != "" && !c.cfg.CategoryInQuery {
		if err := writer.WriteField("cat", req.Category); err != nil {
			return nil, fmt.Errorf("write category: %w", err)
		}
	}
	if req.Priority != "" {
		if err := writer.WriteField("priority", req.Priority); err != nil {
			return nil, fmt.Errorf("write priority: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close multipart body: %w", err)
	}

	var extra url.Values
	if category != "" && c.cfg.CategoryInQuery {
		extra = url.Values{"cat": []string{category}}
	}
	endpoint, err := c.apiURL("addfile", extra)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return nil, fmt.Errorf("build addfile request: %w", err)
	}
	httpReq.Header.Set("Content-Type", writer.FormDataContentType())
	c.applyAuth(httpReq)

	respBody, err := c.doJSON(httpReq)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Status bool     `json:"status"`
		Error  string   `json:"error"`
		NzoIDs []string `json:"nzo_ids"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("parse addfile response: %w", err)
	}
	if !parsed.Status {
		if parsed.Error == "" {
			parsed.Error = "unknown SAB-compatible API error"
		}
		return nil, fmt.Errorf("addfile failed: %s", parsed.Error)
	}
	if len(parsed.NzoIDs) == 0 || strings.TrimSpace(parsed.NzoIDs[0]) == "" {
		return nil, fmt.Errorf("addfile response did not include nzo_ids")
	}
	return &SubmitResult{JobID: strings.TrimSpace(parsed.NzoIDs[0])}, nil
}

func (c *SABClient) Status(ctx context.Context, jobID string) (*JobStatus, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, fmt.Errorf("job id is required")
	}

	status, err := c.queueStatus(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if status != nil {
		return status, nil
	}
	return c.historyStatus(ctx, jobID)
}

func (c *SABClient) Delete(ctx context.Context, jobID string) error {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return fmt.Errorf("job id is required")
	}
	endpoint, err := c.apiURL("queue", url.Values{
		"name":    []string{"delete"},
		"nzo_ids": []string{jobID},
		"value":   []string{jobID},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build delete request: %w", err)
	}
	c.applyAuth(req)
	_, err = c.doJSON(req)
	return err
}

func (c *SABClient) queueStatus(ctx context.Context, jobID string) (*JobStatus, error) {
	endpoint, err := c.apiURL("queue", url.Values{"nzo_ids": []string{jobID}, "limit": []string{"100"}})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build queue request: %w", err)
	}
	c.applyAuth(req)
	respBody, err := c.doJSON(req)
	if err != nil {
		return nil, err
	}
	var parsed queueResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("parse queue response: %w", err)
	}
	for _, slot := range parsed.Queue.Slots {
		if strings.EqualFold(strings.TrimSpace(slot.NzoID), jobID) {
			return slot.toJobStatus(jobID), nil
		}
	}
	return nil, nil
}

func (c *SABClient) historyStatus(ctx context.Context, jobID string) (*JobStatus, error) {
	endpoint, err := c.apiURL("history", url.Values{"nzo_ids": []string{jobID}, "limit": []string{"100"}})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build history request: %w", err)
	}
	c.applyAuth(req)
	respBody, err := c.doJSON(req)
	if err != nil {
		return nil, err
	}
	var parsed historyResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("parse history response: %w", err)
	}
	for _, slot := range parsed.History.Slots {
		if strings.EqualFold(strings.TrimSpace(slot.NzoID), jobID) {
			return slot.toJobStatus(jobID), nil
		}
	}
	return &JobStatus{JobID: jobID, Status: StatusUnknown}, nil
}

func (c *SABClient) apiURL(mode string, extra url.Values) (string, error) {
	base, err := url.Parse(c.cfg.BaseURL)
	if err != nil {
		return "", fmt.Errorf("parse base URL: %w", err)
	}
	apiPath := strings.TrimSpace(c.cfg.APIPath)
	if apiPath == "" {
		apiPath = "/api"
	}
	base.Path = path.Join(base.Path, apiPath)
	if strings.HasSuffix(apiPath, "/") && !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}
	q := base.Query()
	q.Set("mode", mode)
	q.Set("output", "json")
	if c.cfg.APIKey != "" {
		q.Set("apikey", c.cfg.APIKey)
	}
	if c.cfg.UsernameParam != "" && c.cfg.Username != "" {
		q.Set(c.cfg.UsernameParam, c.cfg.Username)
	}
	if c.cfg.PasswordParam != "" && c.cfg.Password != "" {
		q.Set(c.cfg.PasswordParam, c.cfg.Password)
	}
	for key, values := range extra {
		for _, value := range values {
			q.Add(key, value)
		}
	}
	base.RawQuery = q.Encode()
	return base.String(), nil
}

func (c *SABClient) applyAuth(req *http.Request) {
	if c.cfg.Username != "" || c.cfg.Password != "" {
		req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	}
	if c.cfg.APIKey != "" {
		req.Header.Set("X-Api-Key", c.cfg.APIKey)
		if c.cfg.APIKeyAsBearer {
			req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
		}
	}
}

func (c *SABClient) doJSON(req *http.Request) ([]byte, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send %s request: %w", req.URL.Query().Get("mode"), err)
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if readErr != nil {
		return nil, fmt.Errorf("read response: %w", readErr)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("SAB-compatible API returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

type queueResponse struct {
	Queue struct {
		Slots []sabSlot `json:"slots"`
	} `json:"queue"`
}

type historyResponse struct {
	History struct {
		Slots []sabSlot `json:"slots"`
	} `json:"history"`
}

type sabSlot struct {
	NzoID        string `json:"nzo_id"`
	Status       string `json:"status"`
	Filename     string `json:"filename"`
	NZBName      string `json:"nzb_name"`
	Category     string `json:"cat"`
	Percentage   string `json:"percentage"`
	TruePercent  string `json:"true_percentage"`
	SizeMB       string `json:"mb"`
	SizeBytes    int64  `json:"bytes"`
	StoragePath  string `json:"storage_path"`
	Storage      string `json:"storage"`
	DownloadPath string `json:"download_path"`
	Error        string `json:"error"`
	FailMessage  string `json:"fail_message"`
}

func (s sabSlot) toJobStatus(jobID string) *JobStatus {
	progress := parsePercent(s.TruePercent)
	if progress == 0 {
		progress = parsePercent(s.Percentage)
	}
	fileName := strings.TrimSpace(s.Filename)
	if fileName == "" {
		fileName = strings.TrimSpace(s.NZBName)
	}
	errMsg := strings.TrimSpace(s.Error)
	if errMsg == "" {
		errMsg = strings.TrimSpace(s.FailMessage)
	}
	return &JobStatus{
		JobID:      jobID,
		Status:     normalizeSABStatus(s.Status),
		RawStatus:  strings.TrimSpace(s.Status),
		Progress:   progress,
		FileName:   fileName,
		Category:   strings.TrimSpace(s.Category),
		SizeBytes:  firstPositiveInt64(s.SizeBytes, parseSizeMB(s.SizeMB)),
		Error:      errMsg,
		OutputPath: firstNonEmpty(s.StoragePath, s.Storage, s.DownloadPath),
	}
}

func normalizeSABStatus(status string) Status {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued", "pending":
		return StatusQueued
	case "downloading", "processing", "streaming", "extracting", "repairing", "health-checking", "uploading":
		return StatusProcessing
	case "completed", "complete", "success":
		return StatusCompleted
	case "failed", "failure", "error", "upload failed":
		return StatusFailed
	default:
		return StatusUnknown
	}
}

func parsePercent(value string) float64 {
	value = strings.TrimSuffix(strings.TrimSpace(value), "%")
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func parseSizeMB(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return int64(parsed * 1024 * 1024)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstPositiveInt64(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
