package debrid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TorboxClient handles API interactions with Torbox service.
// It implements the Provider interface.
type TorboxClient struct {
	apiKey     string
	httpClient *http.Client
	baseURL    string
}

// Ensure TorboxClient implements Provider interface.
var _ Provider = (*TorboxClient)(nil)

// NewTorboxClient creates a new Torbox API client.
func NewTorboxClient(apiKey string) *TorboxClient {
	return &TorboxClient{
		apiKey:     strings.TrimSpace(apiKey),
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    "https://api.torbox.app/v1/api",
	}
}

// Name returns the provider identifier.
func (c *TorboxClient) Name() string {
	return "torbox"
}

func init() {
	RegisterProvider("torbox", func(apiKey string) Provider {
		return NewTorboxClient(apiKey)
	})
}

// torboxResponse is the generic API response wrapper.
type torboxResponse[T any] struct {
	Success bool   `json:"success"`
	Data    T      `json:"data,omitempty"`
	Detail  string `json:"detail"`
	Error   string `json:"error,omitempty"`
}

// torboxCreateTorrentData is the response data from createtorrent endpoint.
type torboxCreateTorrentData struct {
	TorrentID int    `json:"torrent_id"`
	Name      string `json:"name"`
	Hash      string `json:"hash"`
	AuthID    string `json:"auth_id"`
}

// torboxTorrent represents a torrent in Torbox.
type torboxTorrent struct {
	ID               int            `json:"id"`
	Hash             string         `json:"hash"`
	CreatedAt        string         `json:"created_at"`
	UpdatedAt        string         `json:"updated_at"`
	Magnet           string         `json:"magnet"`
	Size             int64          `json:"size"`
	Active           bool           `json:"active"`
	AuthID           string         `json:"auth_id"`
	DownloadState    string         `json:"download_state"` // cached, completed, downloading, etc.
	Seeds            int            `json:"seeds"`
	Peers            int            `json:"peers"`
	Ratio            float32        `json:"ratio"`
	Progress         float32        `json:"progress"`
	DownloadSpeed    int            `json:"download_speed"`
	UploadSpeed      int            `json:"upload_speed"`
	Name             string         `json:"name"`
	ETA              int            `json:"eta"`
	Server           int            `json:"server"`
	TorrentFile      bool           `json:"torrent_file"`
	ExpiresAt        string         `json:"expires_at"`
	DownloadPresent  bool           `json:"download_present"`
	DownloadFinished bool           `json:"download_finished"`
	Files            []torboxFile   `json:"files"`
	InactiveCheck    int            `json:"inactive_check"`
	Availability     int            `json:"availability"`
}

// torboxFile represents a file within a torrent.
type torboxFile struct {
	ID        int    `json:"id"`
	MD5       string `json:"md5"`
	S3Path    string `json:"s3_path"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	MimeType  string `json:"mimetype"`
	ShortName string `json:"short_name"`
}

// torboxCachedItem represents a cached torrent check result.
type torboxCachedItem struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Hash  string `json:"hash"`
	Files []struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	} `json:"files"`
}

// torboxRequestDLData is the response from requestdl endpoint.
type torboxRequestDLData struct {
	Link string `json:"link,omitempty"` // Sometimes returned as string directly
}

// doRequest performs an HTTP request with authorization.
func (c *TorboxClient) doRequest(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	return c.httpClient.Do(req)
}

// AddMagnet adds a magnet link to Torbox and returns the torrent ID.
func (c *TorboxClient) AddMagnet(ctx context.Context, magnetURL string) (*AddMagnetResult, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("torbox API key not configured")
	}

	trimmedMagnet := strings.TrimSpace(magnetURL)
	if trimmedMagnet == "" {
		return nil, fmt.Errorf("magnet URL is required")
	}

	endpoint := fmt.Sprintf("%s/torrents/createtorrent", c.baseURL)

	formData := url.Values{}
	formData.Set("magnet", trimmedMagnet)
	formData.Set("seed", "1")        // Auto seed
	formData.Set("allow_zip", "false")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build add magnet request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.doRequest(req)
	if err != nil {
		return nil, fmt.Errorf("add magnet request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("torbox authentication failed: invalid API key")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	var result torboxResponse[torboxCreateTorrentData]
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode add magnet response: %w (body: %s)", err, string(body))
	}

	if !result.Success {
		return nil, fmt.Errorf("add magnet failed: %s (error: %s)", result.Detail, result.Error)
	}

	log.Printf("[torbox] magnet added: torrent_id=%d hash=%s name=%s", result.Data.TorrentID, result.Data.Hash, result.Data.Name)

	return &AddMagnetResult{
		ID:  strconv.Itoa(result.Data.TorrentID),
		URI: trimmedMagnet,
	}, nil
}

// AddTorrentFile uploads a .torrent file to Torbox and returns the torrent ID.
func (c *TorboxClient) AddTorrentFile(ctx context.Context, torrentData []byte, filename string) (*AddMagnetResult, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("torbox API key not configured")
	}

	if len(torrentData) == 0 {
		return nil, fmt.Errorf("torrent data is empty")
	}

	if filename == "" {
		filename = "upload.torrent"
	}

	endpoint := fmt.Sprintf("%s/torrents/createtorrent", c.baseURL)

	// Create multipart form
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add the torrent file
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}

	if _, err := part.Write(torrentData); err != nil {
		return nil, fmt.Errorf("write torrent data: %w", err)
	}

	// Add additional form fields
	if err := writer.WriteField("seed", "1"); err != nil {
		return nil, fmt.Errorf("write seed field: %w", err)
	}
	if err := writer.WriteField("allow_zip", "false"); err != nil {
		return nil, fmt.Errorf("write allow_zip field: %w", err)
	}

	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &buf)
	if err != nil {
		return nil, fmt.Errorf("build add torrent request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.doRequest(req)
	if err != nil {
		return nil, fmt.Errorf("add torrent request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("torbox authentication failed: invalid API key")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	var result torboxResponse[torboxCreateTorrentData]
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode add torrent response: %w (body: %s)", err, string(body))
	}

	if !result.Success {
		return nil, fmt.Errorf("add torrent failed: %s (error: %s)", result.Detail, result.Error)
	}

	log.Printf("[torbox] torrent file uploaded: torrent_id=%d hash=%s name=%s", result.Data.TorrentID, result.Data.Hash, result.Data.Name)

	return &AddMagnetResult{
		ID:  strconv.Itoa(result.Data.TorrentID),
		URI: filename,
	}, nil
}

// GetTorrentInfo retrieves information about a torrent by ID.
func (c *TorboxClient) GetTorrentInfo(ctx context.Context, torrentID string) (*TorrentInfo, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("torbox API key not configured")
	}

	trimmedID := strings.TrimSpace(torrentID)
	if trimmedID == "" {
		return nil, fmt.Errorf("torrent ID is required")
	}

	endpoint := fmt.Sprintf("%s/torrents/mylist?id=%s", c.baseURL, trimmedID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build torrent info request: %w", err)
	}

	resp, err := c.doRequest(req)
	if err != nil {
		return nil, fmt.Errorf("torrent info request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("torbox authentication failed: invalid API key")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	// When requesting by ID, Torbox returns a single torrent object in data
	var result torboxResponse[torboxTorrent]
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode torrent info response: %w (body: %s)", err, string(body))
	}

	if !result.Success {
		return nil, fmt.Errorf("get torrent info failed: %s (error: %s)", result.Detail, result.Error)
	}

	torrent := result.Data

	// Convert to provider-agnostic TorrentInfo
	info := &TorrentInfo{
		ID:       strconv.Itoa(torrent.ID),
		Filename: torrent.Name,
		Hash:     torrent.Hash,
		Bytes:    torrent.Size,
		Status:   c.mapDownloadState(torrent.DownloadState),
		Files:    make([]File, 0, len(torrent.Files)),
		Links:    make([]string, 0, len(torrent.Files)),
	}

	// Convert files
	for _, f := range torrent.Files {
		info.Files = append(info.Files, File{
			ID:       f.ID,
			Path:     f.Name,
			Bytes:    f.Size,
			Selected: 1, // Torbox auto-selects all files
		})
		// Generate download link for each file
		// Format: torrent_id:file_id (we'll resolve actual URL in UnrestrictLink)
		info.Links = append(info.Links, fmt.Sprintf("%d:%d", torrent.ID, f.ID))
	}

	return info, nil
}

// mapDownloadState converts Torbox download states to provider-agnostic status.
func (c *TorboxClient) mapDownloadState(state string) string {
	switch strings.ToLower(state) {
	case "cached", "completed":
		return "downloaded"
	case "downloading", "metadl", "checkingresumedata":
		return "downloading"
	case "paused":
		return "paused"
	case "uploading":
		return "uploading"
	default:
		return state
	}
}

// SelectFiles is a no-op for Torbox since files are auto-selected.
// Torbox doesn't require explicit file selection like Real-Debrid.
func (c *TorboxClient) SelectFiles(ctx context.Context, torrentID string, fileIDs string) error {
	// Torbox auto-selects all files, so this is a no-op
	log.Printf("[torbox] SelectFiles called for torrent %s (no-op, Torbox auto-selects)", torrentID)
	return nil
}

// DeleteTorrent removes a torrent from Torbox.
func (c *TorboxClient) DeleteTorrent(ctx context.Context, torrentID string) error {
	if c.apiKey == "" {
		return fmt.Errorf("torbox API key not configured")
	}

	trimmedID := strings.TrimSpace(torrentID)
	if trimmedID == "" {
		return fmt.Errorf("torrent ID is required")
	}

	id, err := strconv.Atoi(trimmedID)
	if err != nil {
		return fmt.Errorf("invalid torrent ID: %w", err)
	}

	endpoint := fmt.Sprintf("%s/torrents/controltorrent", c.baseURL)

	payload := map[string]interface{}{
		"torrent_id": id,
		"operation":  "delete",
	}

	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal delete request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(jsonBody)))
	if err != nil {
		return fmt.Errorf("build delete torrent request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.doRequest(req)
	if err != nil {
		return fmt.Errorf("delete torrent request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("torbox authentication failed: invalid API key")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	var result torboxResponse[interface{}]
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("decode delete response: %w (body: %s)", err, string(body))
	}

	if !result.Success {
		return fmt.Errorf("delete torrent failed: %s (error: %s)", result.Detail, result.Error)
	}

	log.Printf("[torbox] torrent %s deleted", torrentID)
	return nil
}

// UnrestrictLink converts a Torbox link reference to an actual download URL.
// For Torbox, the "link" is in format "torrent_id:file_id" and we call requestdl.
func (c *TorboxClient) UnrestrictLink(ctx context.Context, link string) (*UnrestrictResult, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("torbox API key not configured")
	}

	trimmedLink := strings.TrimSpace(link)
	if trimmedLink == "" {
		return nil, fmt.Errorf("link is required")
	}

	// Parse torrent_id:file_id format
	parts := strings.SplitN(trimmedLink, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid link format, expected torrent_id:file_id, got: %s", link)
	}

	torrentID := parts[0]
	fileID := parts[1]

	// Build requestdl URL
	endpoint := fmt.Sprintf("%s/torrents/requestdl?token=%s&torrent_id=%s&file_id=%s",
		c.baseURL, url.QueryEscape(c.apiKey), torrentID, fileID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build requestdl request: %w", err)
	}

	resp, err := c.doRequest(req)
	if err != nil {
		return nil, fmt.Errorf("requestdl request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("torbox authentication failed: invalid API key")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	// Try parsing as object response first
	var result torboxResponse[interface{}]
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode requestdl response: %w (body: %s)", err, string(body))
	}

	if !result.Success {
		return nil, fmt.Errorf("requestdl failed: %s (error: %s)", result.Detail, result.Error)
	}

	// The data field can be a string (the URL directly) or an object with "link" field
	var downloadURL string
	switch data := result.Data.(type) {
	case string:
		downloadURL = data
	case map[string]interface{}:
		if link, ok := data["link"].(string); ok {
			downloadURL = link
		}
	}

	if downloadURL == "" {
		return nil, fmt.Errorf("no download URL returned from Torbox")
	}

	log.Printf("[torbox] unrestricted link for torrent %s file %s: %s", torrentID, fileID, downloadURL)

	return &UnrestrictResult{
		ID:          fmt.Sprintf("%s:%s", torrentID, fileID),
		DownloadURL: downloadURL,
	}, nil
}

// CheckInstantAvailability checks if a torrent hash is cached on Torbox.
func (c *TorboxClient) CheckInstantAvailability(ctx context.Context, infoHash string) (bool, error) {
	if c.apiKey == "" {
		return false, fmt.Errorf("torbox API key not configured")
	}

	normalizedHash := strings.ToLower(strings.TrimSpace(infoHash))
	if normalizedHash == "" {
		return false, fmt.Errorf("info hash is required")
	}

	endpoint := fmt.Sprintf("%s/torrents/checkcached?hash=%s&list_files=true", c.baseURL, normalizedHash)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, fmt.Errorf("build check cached request: %w", err)
	}

	resp, err := c.doRequest(req)
	if err != nil {
		return false, fmt.Errorf("check cached request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return false, fmt.Errorf("torbox authentication failed: invalid API key")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("read response body: %w", err)
	}

	// checkcached returns an array of cached items
	var result torboxResponse[[]torboxCachedItem]
	if err := json.Unmarshal(body, &result); err != nil {
		// Try parsing as empty object (returned when not cached)
		var emptyResult torboxResponse[interface{}]
		if err2 := json.Unmarshal(body, &emptyResult); err2 == nil {
			if emptyResult.Success {
				log.Printf("[torbox] instant availability: hash %s not cached (empty response)", normalizedHash)
				return false, nil
			}
		}
		return false, fmt.Errorf("decode check cached response: %w (body: %s)", err, string(body))
	}

	if !result.Success {
		// Not an error, just not cached
		log.Printf("[torbox] instant availability: hash %s check failed: %s", normalizedHash, result.Detail)
		return false, nil
	}

	// Check if any items match our hash
	for _, item := range result.Data {
		if strings.EqualFold(item.Hash, normalizedHash) {
			log.Printf("[torbox] instant availability: hash %s is CACHED", normalizedHash)
			return true, nil
		}
	}

	log.Printf("[torbox] instant availability: hash %s not cached", normalizedHash)
	return false, nil
}
