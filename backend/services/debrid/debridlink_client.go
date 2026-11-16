package debrid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"novastream/internal/apiusage"
)

// DebridLinkClient implements the public v2 seedbox API.
// Documentation: https://debrid-link.com/api_doc/v2/seedbox-add
// Files are downloaded automatically and their downloadUrl is already unrestricted.
type DebridLinkClient struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

var _ Provider = (*DebridLinkClient)(nil)

func init() {
	RegisterProvider("debridlink", func(apiKey string) Provider { return NewDebridLinkClient(apiKey) })
}

func NewDebridLinkClient(apiKey string) *DebridLinkClient {
	return &DebridLinkClient{
		apiKey:     strings.TrimSpace(apiKey),
		baseURL:    "https://debrid-link.com/api/v2",
		httpClient: apiusage.TrackClient(&http.Client{Timeout: 30 * time.Second}, "Debrid-Link", "API request"),
	}
}

func (c *DebridLinkClient) Name() string { return "debridlink" }

type debridLinkTorrent struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Hash            string  `json:"hashString"`
	Size            int64   `json:"totalSize"`
	Status          int     `json:"status"`
	Created         int64   `json:"created"`
	DownloadPercent float64 `json:"downloadPercent"`
	Files           []struct {
		Name            string  `json:"name"`
		Size            int64   `json:"size"`
		DownloadURL     string  `json:"downloadUrl"`
		DownloadPercent float64 `json:"downloadPercent"`
	} `json:"files"`
}

func (c *DebridLinkClient) request(ctx context.Context, method, path, contentType string, body io.Reader, out any) error {
	if c.apiKey == "" {
		return fmt.Errorf("debridlink API key not configured")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("build debridlink request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("debridlink request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("debridlink authentication failed: invalid API key or insufficient permissions")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("debridlink request failed with status %d", resp.StatusCode)
	}
	var envelope struct {
		Success bool            `json:"success"`
		Error   string          `json:"error"`
		Value   json.RawMessage `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("decode debridlink response: %w", err)
	}
	if !envelope.Success {
		return fmt.Errorf("debridlink API error: %s", envelope.Error)
	}
	if out != nil {
		if len(envelope.Value) == 0 || string(envelope.Value) == "null" {
			return fmt.Errorf("debridlink response missing value")
		}
		if err := json.Unmarshal(envelope.Value, out); err != nil {
			return fmt.Errorf("decode debridlink value: %w", err)
		}
	}
	return nil
}

func (c *DebridLinkClient) add(ctx context.Context, contentType string, body io.Reader) (*AddMagnetResult, error) {
	var torrent debridLinkTorrent
	if err := c.request(ctx, http.MethodPost, "/seedbox/add", contentType, body, &torrent); err != nil {
		return nil, err
	}
	if torrent.ID == "" {
		return nil, fmt.Errorf("debridlink returned an empty torrent ID")
	}
	// Metadata can be incomplete on add. Polling GetTorrentInfo determines readiness.
	return &AddMagnetResult{ID: torrent.ID}, nil
}

func (c *DebridLinkClient) AddMagnet(ctx context.Context, magnetURL string) (*AddMagnetResult, error) {
	magnetURL = strings.TrimSpace(magnetURL)
	if magnetURL == "" {
		return nil, fmt.Errorf("magnet URL is required")
	}
	body, err := json.Marshal(map[string]any{"url": magnetURL, "wait": false, "structureType": "list"})
	if err != nil {
		return nil, err
	}
	return c.add(ctx, "application/json", bytes.NewReader(body))
}

func (c *DebridLinkClient) AddTorrentFile(ctx context.Context, data []byte, filename string) (*AddMagnetResult, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("torrent data is empty")
	}
	if strings.TrimSpace(filename) == "" {
		filename = "upload.torrent"
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err = part.Write(data); err != nil {
		return nil, err
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	return c.add(ctx, writer.FormDataContentType(), &body)
}

func (c *DebridLinkClient) GetTorrentInfo(ctx context.Context, torrentID string) (*TorrentInfo, error) {
	for attempt := 0; ; attempt++ {
		info, err := c.getTorrentInfo(ctx, torrentID)
		if err != nil || len(info.Files) > 0 || attempt == 4 {
			return info, err
		}
		// The API documents incomplete metadata immediately after adding a magnet.
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *DebridLinkClient) getTorrentInfo(ctx context.Context, torrentID string) (*TorrentInfo, error) {
	torrentID = strings.TrimSpace(torrentID)
	if torrentID == "" {
		return nil, fmt.Errorf("torrent ID is required")
	}
	var torrents []debridLinkTorrent
	if err := c.request(ctx, http.MethodGet, "/seedbox/list?"+url.Values{"ids": {torrentID}, "structureType": {"list"}}.Encode(), "", nil, &torrents); err != nil {
		return nil, err
	}
	for _, torrent := range torrents {
		if torrent.ID != torrentID {
			continue
		}
		info := &TorrentInfo{ID: torrent.ID, Filename: torrent.Name, Hash: torrent.Hash, Bytes: torrent.Size, Status: "downloading"}
		if torrent.Created > 0 {
			info.Added = time.Unix(torrent.Created, 0).UTC().Format(time.RFC3339)
		}
		if torrent.Status == 0 || torrent.Status == 1 {
			info.Status = "queued"
		}
		ready := len(torrent.Files) > 0
		for i, file := range torrent.Files {
			selected := 0
			if file.DownloadPercent == 100 && strings.TrimSpace(file.DownloadURL) != "" {
				selected = 1
				info.Links = append(info.Links, file.DownloadURL)
			} else {
				ready = false
			}
			info.Files = append(info.Files, File{ID: i + 1, Path: file.Name, Bytes: file.Size, Selected: selected})
		}
		if ready {
			info.Status = "downloaded"
		}
		return info, nil
	}
	return nil, fmt.Errorf("debridlink torrent not found")
}

// All files are auto-selected by adding without wait; local media selection chooses playback.
func (c *DebridLinkClient) SelectFiles(ctx context.Context, torrentID, fileIDs string) error {
	return ctx.Err()
}

func (c *DebridLinkClient) DeleteTorrent(ctx context.Context, torrentID string) error {
	torrentID = strings.TrimSpace(torrentID)
	if torrentID == "" {
		return fmt.Errorf("torrent ID is required")
	}
	return c.request(ctx, http.MethodDelete, "/seedbox/"+url.PathEscape(torrentID)+"/remove", "", nil, nil)
}

func (c *DebridLinkClient) UnrestrictLink(ctx context.Context, link string) (*UnrestrictResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	link = strings.TrimSpace(link)
	parsed, err := url.Parse(link)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, fmt.Errorf("invalid debridlink download URL")
	}
	return &UnrestrictResult{DownloadURL: link}, nil
}

// The current public API has no non-mutating instant-availability endpoint.
func (c *DebridLinkClient) CheckInstantAvailability(_ context.Context, infoHash string) (bool, error) {
	if c.apiKey == "" {
		return false, fmt.Errorf("debridlink API key not configured")
	}
	if strings.TrimSpace(infoHash) == "" {
		return false, fmt.Errorf("info hash is required")
	}
	return false, fmt.Errorf("debridlink has no non-mutating cache check: %w", ErrInstantAvailabilityUnsupported)
}

func (c *DebridLinkClient) GetAccountInfo(ctx context.Context) (*AccountInfo, error) {
	var account struct {
		Username    string `json:"username"`
		Email       string `json:"email"`
		PremiumLeft int64  `json:"premiumLeft"`
	}
	if err := c.request(ctx, http.MethodGet, "/account/infos", "", nil, &account); err != nil {
		return nil, err
	}
	info := &AccountInfo{Username: account.Username, Email: account.Email, PremiumActive: account.PremiumLeft > 0}
	if account.PremiumLeft > 0 {
		expires := time.Now().Add(time.Duration(account.PremiumLeft) * time.Second)
		info.ExpiresAt = &expires
		info.DaysRemaining = int(account.PremiumLeft / 86400)
	}
	return info, nil
}
