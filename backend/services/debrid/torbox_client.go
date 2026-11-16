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

	"novastream/internal/apiusage"
)

// TorboxClient handles API interactions with Torbox service.
// It implements the Provider interface.
type TorboxClient struct {
	apiKey         string
	httpClient     *http.Client
	baseURL        string
	autoClearQueue bool // If true, auto-clear downloading torrents when hitting active limit
}

// Ensure TorboxClient implements Provider and Configurable interfaces.
var _ Provider = (*TorboxClient)(nil)
var _ Configurable = (*TorboxClient)(nil)

// NewTorboxClient creates a new Torbox API client.
func NewTorboxClient(apiKey string) *TorboxClient {
	return &TorboxClient{
		apiKey:     strings.TrimSpace(apiKey),
		httpClient: apiusage.TrackClient(&http.Client{Timeout: 30 * time.Second}, "TorBox", "API request"),
		baseURL:    "https://api.torbox.app/v1/api",
	}
}

// Name returns the provider identifier.
func (c *TorboxClient) Name() string {
	return "torbox"
}

// Configure sets provider-specific options from a config map.
func (c *TorboxClient) Configure(config map[string]string) {
	if config == nil {
		return
	}
	if val, ok := config["autoClearQueue"]; ok {
		c.autoClearQueue = val == "true" || val == "1"
	}
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
	ID               int          `json:"id"`
	Hash             string       `json:"hash"`
	CreatedAt        string       `json:"created_at"`
	UpdatedAt        string       `json:"updated_at"`
	Magnet           string       `json:"magnet"`
	Size             int64        `json:"size"`
	Active           bool         `json:"active"`
	AuthID           string       `json:"auth_id"`
	DownloadState    string       `json:"download_state"` // cached, completed, downloading, etc.
	Seeds            int          `json:"seeds"`
	Peers            int          `json:"peers"`
	Ratio            float32      `json:"ratio"`
	Progress         float32      `json:"progress"`
	DownloadSpeed    int          `json:"download_speed"`
	UploadSpeed      int          `json:"upload_speed"`
	Name             string       `json:"name"`
	ETA              int          `json:"eta"`
	Server           int          `json:"server"`
	TorrentFile      bool         `json:"torrent_file"`
	ExpiresAt        string       `json:"expires_at"`
	DownloadPresent  bool         `json:"download_present"`
	DownloadFinished bool         `json:"download_finished"`
	Files            []torboxFile `json:"files"`
	InactiveCheck    int          `json:"inactive_check"`
	Availability     int          `json:"availability"`
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

// torboxTorrentInfo contains only the stable fields needed for playback. TorBox
// has changed types on unrelated mylist fields over time, so decoding the full
// torrent record makes playback unnecessarily sensitive to API drift.
type torboxTorrentInfo struct {
	ID            int          `json:"id"`
	Hash          string       `json:"hash"`
	Size          int64        `json:"size"`
	DownloadState string       `json:"download_state"`
	Name          string       `json:"name"`
	Files         []torboxFile `json:"files"`
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
	req.Header.Set("User-Agent", "mediastorm/1.0")
	req.Header.Set("Accept", "application/json")
	return c.httpClient.Do(req)
}

func torboxAuthError(statusCode int, body []byte) error {
	if statusCode != http.StatusUnauthorized && statusCode != http.StatusForbidden {
		return nil
	}

	bodyText := strings.TrimSpace(string(body))
	if strings.Contains(strings.ToLower(bodyText), "error code: 1010") {
		return fmt.Errorf("torbox request blocked by Cloudflare (error code 1010)")
	}

	return fmt.Errorf("torbox authentication failed: invalid API key")
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
	formData.Set("seed", "1") // Auto seed
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
		return nil, fmt.Errorf("decode add magnet response: %w", err)
	}

	if !result.Success {
		// Check for active download limit error - try auto-clearing if enabled
		if strings.Contains(result.Error, "ACTIVE_LIMIT") && c.autoClearQueue {
			log.Printf("[torbox] hit active download limit, auto-clearing downloading torrents...")
			cleared, clearErr := c.clearDownloadingTorrents(ctx)
			if clearErr != nil {
				log.Printf("[torbox] failed to clear queue: %v", clearErr)
				return nil, fmt.Errorf("add magnet failed: %s (error: %s) - auto-clear also failed: %v", result.Detail, result.Error, clearErr)
			}

			if cleared > 0 {
				// Retry the request
				log.Printf("[torbox] cleared %d torrents, retrying add magnet...", cleared)
				return c.AddMagnet(ctx, magnetURL) // Recursive retry
			}

			// Nothing was cleared (all were cached), still at limit
			return nil, fmt.Errorf("add magnet failed: %s (error: %s) - no downloading torrents to clear", result.Detail, result.Error)
		}

		return nil, fmt.Errorf("add magnet failed: %s (error: %s)", result.Detail, result.Error)
	}

	// Check for valid torrent ID - if 0, the torrent may already exist or response format is different
	if result.Data.TorrentID == 0 {
		log.Printf("[torbox] WARNING: add magnet returned torrent_id=0")

		// Try to find existing torrent by hash if we can extract it from the magnet
		// For now, return an error as ID 0 is not usable
		return nil, fmt.Errorf("add magnet returned invalid torrent_id=0 (torrent may already exist or API error)")
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
		return nil, fmt.Errorf("decode add torrent response: %w", err)
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

	// ID 0 is invalid - Torbox uses positive integers for torrent IDs
	if trimmedID == "0" {
		return nil, fmt.Errorf("invalid torrent ID: 0 (torrent may not have been added)")
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

	torrent, err := parseTorboxTorrentInfoResponse(body, trimmedID)
	if err != nil {
		return nil, err
	}

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

func parseTorboxTorrentInfoResponse(body []byte, torrentID string) (torboxTorrentInfo, error) {
	var envelope torboxResponse[json.RawMessage]
	if err := json.Unmarshal(body, &envelope); err != nil {
		return torboxTorrentInfo{}, fmt.Errorf("decode torrent info response: %w", err)
	}
	if !envelope.Success {
		return torboxTorrentInfo{}, fmt.Errorf("get torrent info failed: %s (error: %s)", envelope.Detail, envelope.Error)
	}

	targetID, err := strconv.Atoi(strings.TrimSpace(torrentID))
	if err != nil || targetID <= 0 {
		return torboxTorrentInfo{}, fmt.Errorf("invalid torrent ID: %s", torrentID)
	}

	data := bytes.TrimSpace(envelope.Data)
	if len(data) == 0 {
		return torboxTorrentInfo{}, fmt.Errorf("decode torrent info response: missing data")
	}

	switch data[0] {
	case '{':
		var torrent torboxTorrentInfo
		if err := json.Unmarshal(data, &torrent); err != nil {
			return torboxTorrentInfo{}, fmt.Errorf("decode torrent info object: %w", err)
		}
		if torrent.ID != targetID {
			return torboxTorrentInfo{}, fmt.Errorf("torrent ID %s not found in Torbox response", torrentID)
		}
		return torrent, nil
	case '[':
		var torrents []torboxTorrentInfo
		if err := json.Unmarshal(data, &torrents); err != nil {
			return torboxTorrentInfo{}, fmt.Errorf("decode torrent info array: %w", err)
		}
		for _, torrent := range torrents {
			if torrent.ID == targetID {
				return torrent, nil
			}
		}
		return torboxTorrentInfo{}, fmt.Errorf("torrent ID %s not found in Torbox response", torrentID)
	default:
		return torboxTorrentInfo{}, fmt.Errorf("decode torrent info response: unsupported data shape")
	}
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
		return fmt.Errorf("decode delete response: %w", err)
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
		return nil, fmt.Errorf("decode requestdl response: %w", err)
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

	log.Printf("[torbox] unrestricted link for torrent %s file %s: %s", torrentID, fileID, safeURLForLog(downloadURL))

	return &UnrestrictResult{
		ID:          fmt.Sprintf("%s:%s", torrentID, fileID),
		DownloadURL: downloadURL,
	}, nil
}

// listTorrents returns all torrents in the user's Torbox account.
func (c *TorboxClient) listTorrents(ctx context.Context) ([]torboxTorrent, error) {
	endpoint := fmt.Sprintf("%s/torrents/mylist", c.baseURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build list torrents request: %w", err)
	}

	resp, err := c.doRequest(req)
	if err != nil {
		return nil, fmt.Errorf("list torrents request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	var result torboxResponse[[]torboxTorrent]
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode list torrents response: %w", err)
	}

	if !result.Success {
		return nil, fmt.Errorf("list torrents failed: %s", result.Detail)
	}

	return result.Data, nil
}

// clearDownloadingTorrents deletes all non-cached torrents (downloading, queued, etc.)
// to free up slots when hitting the active download limit.
func (c *TorboxClient) clearDownloadingTorrents(ctx context.Context) (int, error) {
	torrents, err := c.listTorrents(ctx)
	if err != nil {
		return 0, fmt.Errorf("list torrents: %w", err)
	}

	cleared := 0
	for _, t := range torrents {
		// Skip cached/completed torrents - only delete actively downloading ones
		state := strings.ToLower(t.DownloadState)
		if state == "cached" || state == "completed" {
			continue
		}

		log.Printf("[torbox] clearing downloading torrent: id=%d name=%s state=%s", t.ID, t.Name, t.DownloadState)
		if err := c.DeleteTorrent(ctx, strconv.Itoa(t.ID)); err != nil {
			log.Printf("[torbox] failed to delete torrent %d: %v", t.ID, err)
			continue
		}
		cleared++
	}

	log.Printf("[torbox] cleared %d downloading torrents", cleared)
	return cleared, nil
}

// CheckInstantAvailability checks if a torrent hash is cached on Torbox.
func (c *TorboxClient) CheckInstantAvailability(ctx context.Context, infoHash string) (bool, error) {
	results, err := c.CheckInstantAvailabilityBulk(ctx, []string{infoHash})
	if err != nil {
		return false, err
	}
	return results[strings.ToLower(strings.TrimSpace(infoHash))], nil
}

// CheckInstantAvailabilityBulk checks if torrent hashes are cached on Torbox.
func (c *TorboxClient) CheckInstantAvailabilityBulk(ctx context.Context, infoHashes []string) (map[string]bool, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("torbox API key not configured")
	}

	normalizedHashes := make([]string, 0, len(infoHashes))
	seen := make(map[string]bool, len(infoHashes))
	for _, infoHash := range infoHashes {
		normalizedHash := strings.ToLower(strings.TrimSpace(infoHash))
		if normalizedHash == "" || seen[normalizedHash] {
			continue
		}
		seen[normalizedHash] = true
		normalizedHashes = append(normalizedHashes, normalizedHash)
	}
	if len(normalizedHashes) == 0 {
		return map[string]bool{}, fmt.Errorf("info hash is required")
	}

	results := make(map[string]bool, len(normalizedHashes))
	for start := 0; start < len(normalizedHashes); start += torboxCheckCachedBatchSize {
		end := start + torboxCheckCachedBatchSize
		if end > len(normalizedHashes) {
			end = len(normalizedHashes)
		}
		batch, err := c.checkCachedBatch(ctx, normalizedHashes[start:end])
		if err != nil {
			return nil, err
		}
		for hash, cached := range batch {
			results[hash] = cached
		}
	}
	return results, nil
}

// torboxCheckCachedBatchSize keeps each checkcached GET well under Torbox's
// URL limit: requests over ~8 KB (around 200 hashes) are rejected with an HTML
// 414, which used to fail the whole quick check for large searches.
const torboxCheckCachedBatchSize = 100

func (c *TorboxClient) checkCachedBatch(ctx context.Context, normalizedHashes []string) (map[string]bool, error) {
	values := url.Values{}
	values.Set("hash", strings.Join(normalizedHashes, ","))
	values.Set("list_files", "true")
	endpoint := fmt.Sprintf("%s/torrents/checkcached?%s", c.baseURL, values.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build check cached request: %w", err)
	}

	resp, err := c.doRequest(req)
	if err != nil {
		return nil, fmt.Errorf("check cached request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("torbox authentication failed: invalid API key")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest && !json.Valid(body) {
		return nil, fmt.Errorf("check cached request failed: HTTP %d for %d hashes", resp.StatusCode, len(normalizedHashes))
	}

	cachedHashes, detail, err := parseTorboxCheckCachedResponse(body)
	if err != nil {
		return nil, err
	}

	if detail != "" {
		// Not an error, just not cached
		log.Printf("[torbox] instant availability check failed: %s", detail)
		return map[string]bool{}, nil
	}

	results := make(map[string]bool, len(normalizedHashes))
	for _, normalizedHash := range normalizedHashes {
		if cachedHashes[normalizedHash] {
			log.Printf("[torbox] instant availability: hash %s is CACHED", normalizedHash)
			results[normalizedHash] = true
		} else {
			log.Printf("[torbox] instant availability: hash %s not cached", normalizedHash)
			results[normalizedHash] = false
		}
	}
	return results, nil
}

func parseTorboxCheckCachedResponse(body []byte) (map[string]bool, string, error) {
	var result torboxResponse[json.RawMessage]
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, "", fmt.Errorf("decode check cached response: %w", err)
	}

	if !result.Success {
		return map[string]bool{}, result.Detail, nil
	}

	cachedHashes := make(map[string]bool)
	if len(result.Data) == 0 || string(result.Data) == "null" {
		return cachedHashes, "", nil
	}

	var keyed map[string]torboxCachedItem
	if err := json.Unmarshal(result.Data, &keyed); err == nil {
		for key, item := range keyed {
			hash := strings.ToLower(strings.TrimSpace(item.Hash))
			if hash == "" {
				hash = strings.ToLower(strings.TrimSpace(key))
			}
			if hash != "" {
				cachedHashes[hash] = true
			}
		}
		return cachedHashes, "", nil
	}

	var listed []torboxCachedItem
	if err := json.Unmarshal(result.Data, &listed); err == nil {
		for _, item := range listed {
			hash := strings.ToLower(strings.TrimSpace(item.Hash))
			if hash != "" {
				cachedHashes[hash] = true
			}
		}
		return cachedHashes, "", nil
	}

	var generic map[string]interface{}
	if err := json.Unmarshal(result.Data, &generic); err == nil {
		for key := range generic {
			hash := strings.ToLower(strings.TrimSpace(key))
			if hash != "" {
				cachedHashes[hash] = true
			}
		}
		return cachedHashes, "", nil
	}

	return nil, "", fmt.Errorf("decode check cached data: unsupported response shape")
}
