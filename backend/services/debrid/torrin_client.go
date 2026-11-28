package debrid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const torrinAvailabilityBatchSize = 100

// TorrinClient accesses Torrin's hosted shared cache through its native API.
// Mediastorm only resolves already-cached releases, so adds are preceded by a
// non-mutating availability check to avoid starting a cold Torrin download
// that mediastorm would immediately reject and delete.
type TorrinClient struct {
	apiKey     string
	httpClient *http.Client
	baseURL    string
}

var _ Provider = (*TorrinClient)(nil)
var _ InstantAvailabilityBulkProvider = (*TorrinClient)(nil)

func NewTorrinClient(apiKey string) *TorrinClient {
	return &TorrinClient{
		apiKey:     strings.TrimSpace(apiKey),
		httpClient: newProviderAPIClient(nil, "Torrin"),
		baseURL:    "https://api.torrin.app",
	}
}

func (c *TorrinClient) Name() string { return "torrin" }

func init() {
	RegisterProvider("torrin", func(apiKey string) Provider {
		return NewTorrinClient(apiKey)
	})
}

type torrinFile struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
}

type torrinStream struct {
	FileName  string `json:"file_name"`
	Size      int64  `json:"size"`
	SignedURL string `json:"signed_url"`
}

type torrinJob struct {
	ID         string         `json:"id"`
	InfoHash   string         `json:"info_hash"`
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	Error      string         `json:"error"`
	Files      []torrinFile   `json:"files"`
	FileSize   int64          `json:"file_size"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	StreamURLs []torrinStream `json:"stream_urls"`
}

type torrinAvailability struct {
	Available bool `json:"available"`
}

func (c *TorrinClient) doRequest(req *http.Request, operation string) ([]byte, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("torrin API key not configured")
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "mediastorm/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("torrin %s request failed: %w", operation, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read torrin %s response: %w", operation, err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(string(body))
		var envelope struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil && strings.TrimSpace(envelope.Error) != "" {
			message = strings.TrimSpace(envelope.Error)
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("torrin authentication failed: %s", message)
		}
		return nil, &ProviderError{
			Provider:   c.Name(),
			Operation:  operation,
			StatusCode: resp.StatusCode,
			Message:    message,
			Body:       strings.TrimSpace(string(body)),
		}
	}
	return body, nil
}

func (c *TorrinClient) AddMagnet(ctx context.Context, magnetURL string) (*AddMagnetResult, error) {
	magnetURL = strings.TrimSpace(magnetURL)
	if magnetURL == "" {
		return nil, fmt.Errorf("magnet URL is required")
	}
	infoHash := extractInfoHashFromMagnet(magnetURL)
	if infoHash == "" {
		return nil, fmt.Errorf("magnet URL has no v1 info hash")
	}
	cached, err := c.CheckInstantAvailability(ctx, infoHash)
	if err != nil {
		return nil, err
	}
	if !cached {
		return nil, fmt.Errorf("torrent not cached on torrin")
	}
	return c.addCachedMagnetJob(ctx, magnetURL)
}

func (c *TorrinClient) addCachedMagnetJob(ctx context.Context, magnetURL string) (*AddMagnetResult, error) {
	payload, err := json.Marshal(map[string]string{"magnet": magnetURL})
	if err != nil {
		return nil, fmt.Errorf("encode torrin magnet request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/jobs", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build torrin add magnet request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	body, err := c.doRequest(req, "add magnet")
	if err != nil {
		return nil, err
	}
	job, err := decodeTorrinJob(body, "add magnet")
	if err != nil {
		return nil, err
	}
	return &AddMagnetResult{ID: job.ID, URI: "/api/jobs/" + job.ID, CacheStatusKnown: true, Cached: torrinStatusDownloaded(job.Status)}, nil
}

func (c *TorrinClient) AddTorrentFile(ctx context.Context, torrentData []byte, _ string) (*AddMagnetResult, error) {
	if len(torrentData) == 0 {
		return nil, fmt.Errorf("torrent data is empty")
	}
	infoHash, err := torrentV1InfoHash(torrentData)
	if err != nil {
		return nil, fmt.Errorf("read torrent info hash: %w", err)
	}
	cached, err := c.CheckInstantAvailability(ctx, infoHash)
	if err != nil {
		return nil, err
	}
	if !cached {
		return nil, fmt.Errorf("torrent not cached on torrin")
	}
	// The native upload endpoint always feeds qBittorrent, even when the hash is
	// already in the shared cache. Submit a hash-only magnet instead so Torrin's
	// normal job endpoint links the cached manifest without local downloading.
	return c.addCachedMagnetJob(ctx, "magnet:?xt=urn:btih:"+infoHash)
}

func (c *TorrinClient) GetTorrentInfo(ctx context.Context, torrentID string) (*TorrentInfo, error) {
	torrentID = strings.TrimSpace(torrentID)
	if torrentID == "" {
		return nil, fmt.Errorf("torrent ID is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/jobs/"+url.PathEscape(torrentID), nil)
	if err != nil {
		return nil, fmt.Errorf("build torrin job request: %w", err)
	}
	body, err := c.doRequest(req, "get torrent info")
	if err != nil {
		return nil, err
	}
	job, err := decodeTorrinJob(body, "get torrent info")
	if err != nil {
		return nil, err
	}
	return torrinJobToTorrentInfo(job), nil
}

// Torrin publishes every file in a cached job. Selection is therefore local
// to mediastorm and this call intentionally has no provider-side effect.
func (c *TorrinClient) SelectFiles(_ context.Context, torrentID, _ string) error {
	if strings.TrimSpace(torrentID) == "" {
		return fmt.Errorf("torrent ID is required")
	}
	return nil
}

func (c *TorrinClient) DeleteTorrent(ctx context.Context, torrentID string) error {
	torrentID = strings.TrimSpace(torrentID)
	if torrentID == "" {
		return fmt.Errorf("torrent ID is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/api/jobs/"+url.PathEscape(torrentID), nil)
	if err != nil {
		return fmt.Errorf("build torrin delete request: %w", err)
	}
	_, err = c.doRequest(req, "delete torrent")
	return err
}

// Torrin job links are already signed HTTPS stream URLs, so there is no
// separate unrestriction operation.
func (c *TorrinClient) UnrestrictLink(_ context.Context, link string) (*UnrestrictResult, error) {
	link = strings.TrimSpace(link)
	parsed, err := url.Parse(link)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("invalid torrin stream URL")
	}
	filename, _ := url.PathUnescape(path.Base(parsed.Path))
	if filename == "." || filename == "/" {
		filename = ""
	}
	return &UnrestrictResult{Filename: filename, DownloadURL: link}, nil
}

func (c *TorrinClient) CheckInstantAvailability(ctx context.Context, infoHash string) (bool, error) {
	infoHash = normalizeTorrinHash(infoHash)
	if infoHash == "" {
		return false, fmt.Errorf("info hash is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/availability/"+url.PathEscape(infoHash), nil)
	if err != nil {
		return false, fmt.Errorf("build torrin availability request: %w", err)
	}
	body, err := c.doRequest(req, "instant availability")
	if err != nil {
		return false, err
	}
	var availability torrinAvailability
	if err := json.Unmarshal(body, &availability); err != nil {
		return false, fmt.Errorf("decode torrin availability response: %w", err)
	}
	return availability.Available, nil
}

func (c *TorrinClient) CheckInstantAvailabilityBulk(ctx context.Context, infoHashes []string) (map[string]bool, error) {
	result := make(map[string]bool)
	unique := make([]string, 0, len(infoHashes))
	for _, rawHash := range infoHashes {
		hash := normalizeTorrinHash(rawHash)
		if hash == "" {
			continue
		}
		if _, exists := result[hash]; exists {
			continue
		}
		result[hash] = false
		unique = append(unique, hash)
	}
	for start := 0; start < len(unique); start += torrinAvailabilityBatchSize {
		end := start + torrinAvailabilityBatchSize
		if end > len(unique) {
			end = len(unique)
		}
		payload, err := json.Marshal(map[string][]string{"hashes": unique[start:end]})
		if err != nil {
			return nil, fmt.Errorf("encode torrin availability request: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/availability", bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("build torrin availability request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		body, err := c.doRequest(req, "bulk instant availability")
		if err != nil {
			return nil, err
		}
		var response map[string]torrinAvailability
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, fmt.Errorf("decode torrin availability response: %w", err)
		}
		for hash, availability := range response {
			result[normalizeTorrinHash(hash)] = availability.Available
		}
	}
	return result, nil
}

func (c *TorrinClient) GetAccountInfo(ctx context.Context) (*AccountInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/rest/1.0/user", nil)
	if err != nil {
		return nil, fmt.Errorf("build torrin user request: %w", err)
	}
	body, err := c.doRequest(req, "user")
	if err != nil {
		return nil, err
	}
	var user struct {
		Username   string `json:"username"`
		Email      string `json:"email"`
		Type       string `json:"type"`
		Premium    int64  `json:"premium"`
		Expiration string `json:"expiration"`
	}
	if err := json.Unmarshal(body, &user); err != nil {
		return nil, fmt.Errorf("decode torrin user response: %w", err)
	}
	info := &AccountInfo{Username: user.Username, Email: user.Email, PremiumActive: user.Premium > 0 || strings.EqualFold(user.Type, "premium")}
	if user.Expiration != "" {
		if expiresAt, err := time.Parse(time.RFC3339, user.Expiration); err == nil {
			info.ExpiresAt = &expiresAt
			info.DaysRemaining = max(0, int(time.Until(expiresAt).Hours()/24))
			info.PremiumActive = expiresAt.After(time.Now())
		}
	}
	return info, nil
}

func decodeTorrinJob(body []byte, operation string) (*torrinJob, error) {
	var job torrinJob
	if err := json.Unmarshal(body, &job); err != nil {
		return nil, fmt.Errorf("decode torrin %s response: %w", operation, err)
	}
	if strings.TrimSpace(job.ID) == "" {
		return nil, fmt.Errorf("torrin %s returned no job ID", operation)
	}
	return &job, nil
}

func torrinJobToTorrentInfo(job *torrinJob) *TorrentInfo {
	info := &TorrentInfo{
		ID:       job.ID,
		Filename: job.Name,
		Hash:     job.InfoHash,
		Bytes:    job.FileSize,
		Status:   torrinStatus(job.Status),
		Added:    job.CreatedAt.Format(time.RFC3339),
		Ended:    job.UpdatedAt.Format(time.RFC3339),
		Files:    make([]File, 0, len(job.Files)),
		Links:    make([]string, 0, len(job.StreamURLs)),
	}
	if info.Bytes == 0 {
		for _, file := range job.Files {
			info.Bytes += file.Size
		}
	}
	for index, file := range job.Files {
		fileID := file.Index + 1
		if fileID < 1 {
			fileID = index + 1
		}
		info.Files = append(info.Files, File{ID: fileID, Path: "/" + strings.TrimPrefix(file.Name, "/"), Bytes: file.Size, Selected: 1})
	}
	for _, stream := range job.StreamURLs {
		if strings.TrimSpace(stream.SignedURL) != "" {
			info.Links = append(info.Links, stream.SignedURL)
		}
	}
	return info
}

func torrinStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "complete", "seeding":
		return "downloaded"
	case "downloading", "processing":
		return "downloading"
	case "publishing":
		return "uploading"
	case "failed", "evicted":
		return "error"
	case "pending":
		return "magnet_conversion"
	default:
		return "queued"
	}
}

func torrinStatusDownloaded(status string) bool {
	return torrinStatus(status) == "downloaded"
}

func normalizeTorrinHash(infoHash string) string {
	return strings.ToLower(strings.TrimSpace(infoHash))
}

// useHTTPTransport installs rt (the provider's proxy) for all API calls.
func (c *TorrinClient) useHTTPTransport(rt http.RoundTripper) {
	c.httpClient = newProviderAPIClient(rt, "Torrin")
}
