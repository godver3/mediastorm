package debrid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// RealDebridClient handles API interactions with Real-Debrid service.
// It implements the Provider interface.
type RealDebridClient struct {
	apiKey     string
	httpClient *http.Client
	baseURL    string
}

// Ensure RealDebridClient implements Provider interface.
var _ Provider = (*RealDebridClient)(nil)

// NewRealDebridClient creates a new Real-Debrid API client.
func NewRealDebridClient(apiKey string) *RealDebridClient {
	return &RealDebridClient{
		apiKey:     strings.TrimSpace(apiKey),
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    "https://api.real-debrid.com/rest/1.0",
	}
}

// Name returns the provider identifier.
func (c *RealDebridClient) Name() string {
	return "realdebrid"
}

func init() {
	RegisterProvider("realdebrid", func(apiKey string) Provider {
		return NewRealDebridClient(apiKey)
	})
}

// ErrorResponse represents a Real-Debrid API error response.
type ErrorResponse struct {
	Error     string `json:"error"`
	ErrorCode int    `json:"error_code"`
}

// ensureReplayableBody buffers the request body so it can be replayed between retries.
func ensureReplayableBody(req *http.Request) error {
	if req == nil {
		return fmt.Errorf("request is nil")
	}

	if req.Body == nil || req.Body == http.NoBody || req.GetBody != nil {
		return nil
	}

	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return fmt.Errorf("buffer request body: %w", err)
	}
	_ = req.Body.Close()

	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(bodyBytes)), nil
	}

	bodyCopy, err := req.GetBody()
	if err != nil {
		return fmt.Errorf("rewind request body: %w", err)
	}

	req.Body = bodyCopy
	req.ContentLength = int64(len(bodyBytes))

	return nil
}

// resetRequestBody rewinds the request body for the next retry attempt.
func resetRequestBody(req *http.Request) error {
	if req == nil || req.GetBody == nil {
		return nil
	}

	bodyCopy, err := req.GetBody()
	if err != nil {
		return err
	}

	req.Body = bodyCopy
	return nil
}

// doWithRetry performs an HTTP request with exponential backoff for 429 and transient 503 errors.
// Request bodies are buffered as needed so POST requests can be replayed between retries.
func (c *RealDebridClient) doWithRetry(req *http.Request, maxRetries int) (*http.Response, error) {
	var resp *http.Response
	var err error

	if err := ensureReplayableBody(req); err != nil {
		return nil, err
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			if err := resetRequestBody(req); err != nil {
				return nil, fmt.Errorf("reset request body: %w", err)
			}
		}

		resp, err = c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		// Check if we should retry this error
		shouldRetry, retryReason := c.shouldRetryError(resp)
		if !shouldRetry {
			return resp, nil
		}

		// Read the error body for logging
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()

		// Don't retry on last attempt
		if attempt == maxRetries {
			log.Printf("[realdebrid] %s after %d retries: %s", retryReason, maxRetries, string(body))
			// Return a new error response with the original status
			return &http.Response{
				StatusCode: resp.StatusCode,
				Status:     resp.Status,
				Body:       io.NopCloser(strings.NewReader(string(body))),
				Header:     resp.Header,
			}, nil
		}

		// Calculate delay with exponential backoff
		delay := c.calculateRetryDelay(resp, attempt)

		log.Printf("[realdebrid] %s, retrying in %v (attempt %d/%d): %s",
			retryReason, delay, attempt+1, maxRetries, string(body))

		select {
		case <-time.After(delay):
			// Continue to next attempt
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}

	return resp, nil
}

// shouldRetryError determines if an HTTP response should be retried.
// Returns (shouldRetry, reason) where reason describes why we're retrying.
func (c *RealDebridClient) shouldRetryError(resp *http.Response) (bool, string) {
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return true, "rate limited (429)"

	case http.StatusServiceUnavailable:
		// For 503, we need to check if it's a transient error
		// Read a copy of the body to check the error type
		bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 512))
		if err != nil {
			return false, ""
		}
		// Restore the body for the caller
		resp.Body = io.NopCloser(io.MultiReader(
			strings.NewReader(string(bodyBytes)),
			resp.Body,
		))

		// Check if this is a "hoster_unavailable" error (error_code 19)
		var errorResp ErrorResponse
		if err := json.Unmarshal(bodyBytes, &errorResp); err == nil {
			if errorResp.Error == "hoster_unavailable" || errorResp.ErrorCode == 19 {
				return true, "hoster unavailable (503)"
			}
		}

		return false, ""

	default:
		return false, ""
	}
}

// calculateRetryDelay calculates the delay before the next retry attempt.
func (c *RealDebridClient) calculateRetryDelay(resp *http.Response, attempt int) time.Duration {
	// Check for Retry-After header (mainly for 429)
	retryAfter := resp.Header.Get("Retry-After")
	if retryAfter != "" {
		// Try to parse as seconds
		if seconds, parseErr := strconv.Atoi(retryAfter); parseErr == nil {
			delay := time.Duration(seconds) * time.Second
			// Cap at 30 seconds for Retry-After
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
			return delay
		}
	}

	// Use exponential backoff: 1s, 2s, 4s, 8s, 16s
	delay := time.Duration(1<<uint(attempt)) * time.Second

	// Cap the delay at 30 seconds
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}

	return delay
}

// InstantAvailabilityResponse represents the cached status for a torrent hash.
type InstantAvailabilityResponse map[string]map[string][]InstantAvailabilityVariant

// InstantAvailabilityVariant represents a specific file variant that is cached.
type InstantAvailabilityVariant struct {
	Filename string                               `json:"filename"`
	Filesize int64                                `json:"filesize"`
	ID       json.Number                          `json:"id"` // File ID within the torrent
	Files    map[string]InstantAvailabilityFileID `json:"files,omitempty"`
}

// InstantAvailabilityFileID represents file IDs that are available.
type InstantAvailabilityFileID struct {
	Filename string `json:"filename,omitempty"`
	Filesize int64  `json:"filesize,omitempty"`
}

// CheckInstantAvailability checks if a torrent hash is cached on Real-Debrid.
// Returns true if the torrent is cached and available for instant download.
func (c *RealDebridClient) CheckInstantAvailability(ctx context.Context, infoHash string) (bool, error) {
	if c.apiKey == "" {
		return false, fmt.Errorf("real-debrid API key not configured")
	}

	normalizedHash := strings.ToLower(strings.TrimSpace(infoHash))
	if normalizedHash == "" {
		return false, fmt.Errorf("info hash is required")
	}

	endpoint := fmt.Sprintf("%s/torrents/instantAvailability/%s", c.baseURL, normalizedHash)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, fmt.Errorf("build instant availability request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

	resp, err := c.doWithRetry(req, 3)
	if err != nil {
		return false, fmt.Errorf("instant availability request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return false, fmt.Errorf("real-debrid authentication failed: invalid API key")
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return false, fmt.Errorf("instant availability failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var availability InstantAvailabilityResponse
	if err := json.NewDecoder(resp.Body).Decode(&availability); err != nil {
		return false, fmt.Errorf("decode instant availability response: %w", err)
	}

	// Check if the hash exists and has cached variants
	hashData, exists := availability[normalizedHash]
	if !exists {
		log.Printf("[realdebrid] instant availability: hash %s not found in response", normalizedHash)
		return false, nil
	}

	if len(hashData) == 0 {
		log.Printf("[realdebrid] instant availability: hash %s has no variants", normalizedHash)
		return false, nil
	}

	// If any variant exists for this hash, it's cached
	variantCount := 0
	for _, variants := range hashData {
		if len(variants) > 0 {
			variantCount++
		}
	}

	if variantCount > 0 {
		log.Printf("[realdebrid] instant availability: hash %s is CACHED with %d variants", normalizedHash, variantCount)
		return true, nil
	}

	log.Printf("[realdebrid] instant availability: hash %s has no valid variants", normalizedHash)
	return false, nil
}

// addMagnetResponse represents the raw Real-Debrid API response when adding a magnet link.
type addMagnetResponse struct {
	ID  string `json:"id"`
	URI string `json:"uri"`
}

// AddMagnet adds a magnet link to Real-Debrid and returns the torrent ID.
func (c *RealDebridClient) AddMagnet(ctx context.Context, magnetURL string) (*AddMagnetResult, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("real-debrid API key not configured")
	}

	trimmedMagnet := strings.TrimSpace(magnetURL)
	if trimmedMagnet == "" {
		return nil, fmt.Errorf("magnet URL is required")
	}

	endpoint := fmt.Sprintf("%s/torrents/addMagnet", c.baseURL)

	formData := url.Values{}
	formData.Set("magnet", trimmedMagnet)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build add magnet request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.doWithRetry(req, 3)
	if err != nil {
		return nil, fmt.Errorf("add magnet request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("real-debrid authentication failed: invalid API key")
	}

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("add magnet failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result addMagnetResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode add magnet response: %w", err)
	}

	return &AddMagnetResult{
		ID:  result.ID,
		URI: result.URI,
	}, nil
}

// AddTorrentFile uploads a .torrent file to Real-Debrid and returns the torrent ID.
// Real-Debrid expects the raw torrent file binary data sent directly in the request body.
func (c *RealDebridClient) AddTorrentFile(ctx context.Context, torrentData []byte, filename string) (*AddMagnetResult, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("real-debrid API key not configured")
	}

	if len(torrentData) == 0 {
		return nil, fmt.Errorf("torrent data is empty")
	}

	endpoint := fmt.Sprintf("%s/torrents/addTorrent", c.baseURL)

	// Real-Debrid expects raw binary data, not multipart form
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(torrentData))
	if err != nil {
		return nil, fmt.Errorf("build add torrent request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := c.doWithRetry(req, 3)
	if err != nil {
		return nil, fmt.Errorf("add torrent request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("real-debrid authentication failed: invalid API key")
	}

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("add torrent failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result addMagnetResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode add torrent response: %w", err)
	}

	log.Printf("[realdebrid] torrent file uploaded: id=%s", result.ID)

	return &AddMagnetResult{
		ID:  result.ID,
		URI: result.URI,
	}, nil
}

// TorrentInfo represents detailed information about a torrent.
type TorrentInfo struct {
	ID       string   `json:"id"`
	Filename string   `json:"filename"`
	Hash     string   `json:"hash"`
	Bytes    int64    `json:"bytes"`
	Status   string   `json:"status"` // e.g., "downloaded", "downloading", "queued", "error"
	Added    string   `json:"added"`
	Files    []File   `json:"files,omitempty"`
	Links    []string `json:"links,omitempty"`
	Ended    string   `json:"ended,omitempty"`
}

// File represents a file within a torrent.
type File struct {
	ID       int    `json:"id"`
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
	Selected int    `json:"selected"` // 0 = not selected, 1 = selected
}

// GetTorrentInfo retrieves detailed information about a torrent by ID.
func (c *RealDebridClient) GetTorrentInfo(ctx context.Context, torrentID string) (*TorrentInfo, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("real-debrid API key not configured")
	}

	trimmedID := strings.TrimSpace(torrentID)
	if trimmedID == "" {
		return nil, fmt.Errorf("torrent ID is required")
	}

	endpoint := fmt.Sprintf("%s/torrents/info/%s", c.baseURL, trimmedID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build torrent info request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

	resp, err := c.doWithRetry(req, 3)
	if err != nil {
		return nil, fmt.Errorf("torrent info request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("real-debrid authentication failed: invalid API key")
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("torrent info failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var info TorrentInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode torrent info response: %w", err)
	}

	return &info, nil
}

// DeleteTorrent removes a torrent from Real-Debrid.
func (c *RealDebridClient) DeleteTorrent(ctx context.Context, torrentID string) error {
	if c.apiKey == "" {
		return fmt.Errorf("real-debrid API key not configured")
	}

	trimmedID := strings.TrimSpace(torrentID)
	if trimmedID == "" {
		return fmt.Errorf("torrent ID is required")
	}

	endpoint := fmt.Sprintf("%s/torrents/delete/%s", c.baseURL, trimmedID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build delete torrent request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

	resp, err := c.doWithRetry(req, 3)
	if err != nil {
		return fmt.Errorf("delete torrent request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("real-debrid authentication failed: invalid API key")
	}

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("delete torrent failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return nil
}

// unrestrictLinkResponse represents the raw Real-Debrid API response from unrestricting a link.
type unrestrictLinkResponse struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	MimeType string `json:"mimeType"`
	Filesize int64  `json:"filesize"`
	Link     string `json:"link"`     // This is the actual direct download URL
	Host     string `json:"host"`
	HostIcon string `json:"host_icon"`
	Chunks   int    `json:"chunks"`
	Download string `json:"download"` // Alternative download URL
}

// UnrestrictLink converts a Real-Debrid restricted link to an actual download URL.
// This is required for /d/ links returned from torrent info.
func (c *RealDebridClient) UnrestrictLink(ctx context.Context, link string) (*UnrestrictResult, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("real-debrid API key not configured")
	}

	trimmedLink := strings.TrimSpace(link)
	if trimmedLink == "" {
		return nil, fmt.Errorf("link is required")
	}

	endpoint := fmt.Sprintf("%s/unrestrict/link", c.baseURL)

	formData := url.Values{}
	formData.Set("link", trimmedLink)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build unrestrict request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.doWithRetry(req, 3)
	if err != nil {
		return nil, fmt.Errorf("unrestrict request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("real-debrid authentication failed: invalid API key")
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("unrestrict failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result unrestrictLinkResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode unrestrict response: %w", err)
	}

	// Use Download URL if available, otherwise fall back to Link
	downloadURL := result.Download
	if downloadURL == "" {
		downloadURL = result.Link
	}

	return &UnrestrictResult{
		ID:          result.ID,
		Filename:    result.Filename,
		MimeType:    result.MimeType,
		Filesize:    result.Filesize,
		DownloadURL: downloadURL,
	}, nil
}

// SelectFiles selects files in a torrent for download.
// Pass "all" to select all files, or a comma-separated list of file IDs.
func (c *RealDebridClient) SelectFiles(ctx context.Context, torrentID string, files string) error {
	if c.apiKey == "" {
		return fmt.Errorf("real-debrid API key not configured")
	}

	trimmedID := strings.TrimSpace(torrentID)
	if trimmedID == "" {
		return fmt.Errorf("torrent ID is required")
	}

	if files == "" {
		files = "all"
	}

	endpoint := fmt.Sprintf("%s/torrents/selectFiles/%s", c.baseURL, trimmedID)

	formData := url.Values{}
	formData.Set("files", files)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(formData.Encode()))
	if err != nil {
		return fmt.Errorf("build select files request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.doWithRetry(req, 3)
	if err != nil {
		return fmt.Errorf("select files request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("real-debrid authentication failed: invalid API key")
	}

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("select files failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return nil
}
