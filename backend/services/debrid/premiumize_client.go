package debrid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
)

// PremiumizeClient handles API interactions with Premiumize.me.
// It implements Provider and uses API-key Bearer authentication.
type PremiumizeClient struct {
	apiKey     string
	httpClient *http.Client
	baseURL    string
}

var _ Provider = (*PremiumizeClient)(nil)
var _ InstantAvailabilityBulkProvider = (*PremiumizeClient)(nil)

// NewPremiumizeClient creates a new Premiumize.me API client.
func NewPremiumizeClient(apiKey string) *PremiumizeClient {
	return &PremiumizeClient{
		apiKey:     strings.TrimSpace(apiKey),
		httpClient: newProviderAPIClient(nil, "Premiumize"),
		baseURL:    "https://www.premiumize.me/api",
	}
}

// Name returns the provider identifier.
func (c *PremiumizeClient) Name() string {
	return "premiumize"
}

func init() {
	RegisterProvider("premiumize", func(apiKey string) Provider {
		return NewPremiumizeClient(apiKey)
	})
}

type premiumizeEnvelope struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	Code    string `json:"code,omitempty"`
}

type premiumizeTransfer struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Status   string  `json:"status"`
	Progress float64 `json:"progress"`
	Message  string  `json:"message"`
	FolderID *string `json:"folder_id"`
	FileID   *string `json:"file_id"`
}

type premiumizeItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Size     int64  `json:"size"`
	MimeType string `json:"mime_type"`
	Link     string `json:"link"`
}

func (c *PremiumizeClient) doRequest(req *http.Request, operation string) ([]byte, error) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "mediastorm/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s request failed: %w", operation, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s response: %w", operation, err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%s failed with status %d: %s", operation, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var envelope premiumizeEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode %s response: %w", operation, err)
	}
	if !strings.EqualFold(envelope.Status, "success") {
		if envelope.Code == "authentication_failed" {
			return nil, fmt.Errorf("premiumize authentication failed: invalid API key")
		}
		message := strings.TrimSpace(envelope.Message)
		if message == "" {
			message = "unknown error"
		}
		if envelope.Code != "" {
			return nil, fmt.Errorf("%s failed: %s (%s)", operation, message, envelope.Code)
		}
		return nil, fmt.Errorf("%s failed: %s", operation, message)
	}
	return body, nil
}

// AddMagnet submits a magnet transfer to Premiumize.me.
func (c *PremiumizeClient) AddMagnet(ctx context.Context, magnetURL string) (*AddMagnetResult, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("premiumize API key not configured")
	}
	magnetURL = strings.TrimSpace(magnetURL)
	if magnetURL == "" {
		return nil, fmt.Errorf("magnet URL is required")
	}

	form := url.Values{"src": []string{magnetURL}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/transfer/create", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build add magnet request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	body, err := c.doRequest(req, "add magnet")
	if err != nil {
		return nil, err
	}
	var result struct {
		premiumizeEnvelope
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode add magnet response: %w", err)
	}
	if strings.TrimSpace(result.ID) == "" {
		return nil, fmt.Errorf("add magnet returned no transfer ID")
	}
	log.Printf("[premiumize] magnet added: transfer_id=%s", result.ID)
	return &AddMagnetResult{ID: result.ID, URI: magnetURL}, nil
}

// AddTorrentFile uploads a torrent source to Premiumize.me.
func (c *PremiumizeClient) AddTorrentFile(ctx context.Context, torrentData []byte, filename string) (*AddMagnetResult, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("premiumize API key not configured")
	}
	if len(torrentData) == 0 {
		return nil, fmt.Errorf("torrent data is empty")
	}
	if strings.TrimSpace(filename) == "" {
		filename = "upload.torrent"
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("src", filename)
	if err != nil {
		return nil, fmt.Errorf("create torrent upload field: %w", err)
	}
	if _, err := part.Write(torrentData); err != nil {
		return nil, fmt.Errorf("write torrent upload: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close torrent upload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/transfer/create", &body)
	if err != nil {
		return nil, fmt.Errorf("build add torrent request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	responseBody, err := c.doRequest(req, "add torrent")
	if err != nil {
		return nil, err
	}
	var result struct {
		premiumizeEnvelope
		ID string `json:"id"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return nil, fmt.Errorf("decode add torrent response: %w", err)
	}
	if strings.TrimSpace(result.ID) == "" {
		return nil, fmt.Errorf("add torrent returned no transfer ID")
	}
	log.Printf("[premiumize] torrent file uploaded: transfer_id=%s", result.ID)
	return &AddMagnetResult{ID: result.ID, URI: filename}, nil
}

// GetTorrentInfo retrieves a transfer and its completed cloud files.
func (c *PremiumizeClient) GetTorrentInfo(ctx context.Context, torrentID string) (*TorrentInfo, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("premiumize API key not configured")
	}
	torrentID = strings.TrimSpace(torrentID)
	if torrentID == "" {
		return nil, fmt.Errorf("torrent ID is required")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/transfer/list", nil)
	if err != nil {
		return nil, fmt.Errorf("build transfer list request: %w", err)
	}
	body, err := c.doRequest(req, "get torrent info")
	if err != nil {
		return nil, err
	}
	var result struct {
		premiumizeEnvelope
		Transfers []premiumizeTransfer `json:"transfers"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode transfer list response: %w", err)
	}

	var transfer *premiumizeTransfer
	for i := range result.Transfers {
		if result.Transfers[i].ID == torrentID {
			transfer = &result.Transfers[i]
			break
		}
	}
	if transfer == nil {
		return nil, fmt.Errorf("premiumize transfer %s not found", torrentID)
	}

	info := &TorrentInfo{
		ID:       transfer.ID,
		Filename: transfer.Name,
		Status:   mapPremiumizeStatus(transfer.Status),
	}
	if info.Status != "downloaded" {
		return info, nil
	}

	var items []premiumizePathItem
	switch {
	case transfer.FileID != nil && strings.TrimSpace(*transfer.FileID) != "":
		item, err := c.getItem(ctx, *transfer.FileID)
		if err != nil {
			return nil, err
		}
		items = append(items, premiumizePathItem{Path: item.Name, Item: item})
	case transfer.FolderID != nil && strings.TrimSpace(*transfer.FolderID) != "":
		items, err = c.listFolderFiles(ctx, *transfer.FolderID, "", make(map[string]bool))
		if err != nil {
			return nil, err
		}
	}

	sort.SliceStable(items, func(i, j int) bool {
		return strings.ToLower(items[i].Path) < strings.ToLower(items[j].Path)
	})
	for _, item := range items {
		if strings.TrimSpace(item.Item.Link) == "" {
			continue
		}
		info.Files = append(info.Files, File{
			ID:       len(info.Files) + 1,
			Path:     item.Path,
			Bytes:    item.Item.Size,
			Selected: 1,
		})
		info.Links = append(info.Links, item.Item.Link)
		info.Bytes += item.Item.Size
	}
	return info, nil
}

type premiumizePathItem struct {
	Path string
	Item premiumizeItem
}

func (c *PremiumizeClient) getItem(ctx context.Context, itemID string) (premiumizeItem, error) {
	endpoint := c.baseURL + "/item/details?id=" + url.QueryEscape(itemID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return premiumizeItem{}, fmt.Errorf("build item details request: %w", err)
	}
	body, err := c.doRequest(req, "get item details")
	if err != nil {
		return premiumizeItem{}, err
	}
	var result struct {
		premiumizeEnvelope
		premiumizeItem
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return premiumizeItem{}, fmt.Errorf("decode item details response: %w", err)
	}
	return result.premiumizeItem, nil
}

func (c *PremiumizeClient) listFolderFiles(ctx context.Context, folderID, prefix string, visited map[string]bool) ([]premiumizePathItem, error) {
	if visited[folderID] {
		return nil, fmt.Errorf("premiumize folder cycle detected at %s", folderID)
	}
	visited[folderID] = true
	defer delete(visited, folderID)

	endpoint := c.baseURL + "/folder/list?id=" + url.QueryEscape(folderID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build folder list request: %w", err)
	}
	body, err := c.doRequest(req, "list folder")
	if err != nil {
		return nil, err
	}
	var result struct {
		premiumizeEnvelope
		Content []premiumizeItem `json:"content"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode folder list response: %w", err)
	}

	var files []premiumizePathItem
	for _, item := range result.Content {
		itemPath := item.Name
		if prefix != "" {
			itemPath = prefix + "/" + item.Name
		}
		if strings.EqualFold(item.Type, "folder") {
			nested, err := c.listFolderFiles(ctx, item.ID, itemPath, visited)
			if err != nil {
				return nil, err
			}
			files = append(files, nested...)
			continue
		}
		files = append(files, premiumizePathItem{Path: itemPath, Item: item})
	}
	return files, nil
}

func mapPremiumizeStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "finished", "seeding":
		return "downloaded"
	case "queued":
		return "queued"
	case "running":
		return "downloading"
	case "error":
		return "error"
	default:
		return strings.ToLower(strings.TrimSpace(status))
	}
}

// SelectFiles is a no-op because Premiumize.me processes all transfer files.
func (c *PremiumizeClient) SelectFiles(_ context.Context, torrentID, _ string) error {
	log.Printf("[premiumize] SelectFiles called for transfer %s (no-op, Premiumize processes all files)", torrentID)
	return nil
}

// DeleteTorrent removes the transfer record. Completed cloud files are retained
// because they may still back an active playback URL.
func (c *PremiumizeClient) DeleteTorrent(ctx context.Context, torrentID string) error {
	if c.apiKey == "" {
		return fmt.Errorf("premiumize API key not configured")
	}
	torrentID = strings.TrimSpace(torrentID)
	if torrentID == "" {
		return fmt.Errorf("torrent ID is required")
	}
	form := url.Values{"id": []string{torrentID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/transfer/delete", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build delete transfer request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := c.doRequest(req, "delete torrent"); err != nil {
		return err
	}
	log.Printf("[premiumize] transfer %s deleted", torrentID)
	return nil
}

// UnrestrictLink returns the direct cloud link supplied by Premiumize.me.
func (c *PremiumizeClient) UnrestrictLink(_ context.Context, link string) (*UnrestrictResult, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("premiumize API key not configured")
	}
	link = strings.TrimSpace(link)
	parsed, err := url.Parse(link)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("invalid Premiumize download link")
	}
	filename := path.Base(parsed.Path)
	mimeType := mime.TypeByExtension(path.Ext(filename))
	return &UnrestrictResult{
		Filename:    filename,
		MimeType:    mimeType,
		DownloadURL: link,
	}, nil
}

// CheckInstantAvailability checks whether a torrent hash is in Premiumize's cache.
func (c *PremiumizeClient) CheckInstantAvailability(ctx context.Context, infoHash string) (bool, error) {
	results, err := c.CheckInstantAvailabilityBulk(ctx, []string{infoHash})
	if err != nil {
		return false, err
	}
	return results[strings.ToLower(strings.TrimSpace(infoHash))], nil
}

// CheckInstantAvailabilityBulk checks multiple torrent hashes in one cache request.
func (c *PremiumizeClient) CheckInstantAvailabilityBulk(ctx context.Context, infoHashes []string) (map[string]bool, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("premiumize API key not configured")
	}
	hashes := make([]string, 0, len(infoHashes))
	seen := make(map[string]bool, len(infoHashes))
	for _, infoHash := range infoHashes {
		hash := strings.ToLower(strings.TrimSpace(infoHash))
		if hash == "" || seen[hash] {
			continue
		}
		seen[hash] = true
		hashes = append(hashes, hash)
	}
	if len(hashes) == 0 {
		return nil, fmt.Errorf("info hash is required")
	}

	form := url.Values{}
	for _, hash := range hashes {
		form.Add("items[]", "magnet:?xt=urn:btih:"+hash)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/cache/check", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build cache check request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	body, err := c.doRequest(req, "check instant availability")
	if err != nil {
		return nil, err
	}
	var result struct {
		premiumizeEnvelope
		Response []bool `json:"response"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode cache check response: %w", err)
	}
	if len(result.Response) != len(hashes) {
		return nil, fmt.Errorf("cache check returned %d results for %d hashes", len(result.Response), len(hashes))
	}

	available := make(map[string]bool, len(hashes))
	for i, hash := range hashes {
		available[hash] = result.Response[i]
	}
	return available, nil
}

// GetAccountInfo returns Premiumize.me account/subscription information.
func (c *PremiumizeClient) GetAccountInfo(ctx context.Context) (*AccountInfo, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("premiumize API key not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/account/info", nil)
	if err != nil {
		return nil, fmt.Errorf("build account info request: %w", err)
	}
	body, err := c.doRequest(req, "account info")
	if err != nil {
		return nil, err
	}
	var result struct {
		premiumizeEnvelope
		CustomerID   string `json:"customer_id"`
		PremiumUntil *int64 `json:"premium_until"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode account info response: %w", err)
	}

	info := &AccountInfo{Username: result.CustomerID}
	if result.PremiumUntil != nil && *result.PremiumUntil > time.Now().Unix() {
		expiresAt := time.Unix(*result.PremiumUntil, 0)
		info.PremiumActive = true
		info.ExpiresAt = &expiresAt
		info.DaysRemaining = int(time.Until(expiresAt).Hours() / 24)
	}
	return info, nil
}

// useHTTPTransport installs rt (the provider's proxy) for all API calls.
func (c *PremiumizeClient) useHTTPTransport(rt http.RoundTripper) {
	c.httpClient = newProviderAPIClient(rt, "Premiumize")
}
