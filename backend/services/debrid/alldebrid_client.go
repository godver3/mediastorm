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

// AllDebridClient handles API interactions with AllDebrid service.
// It implements the Provider interface.
type AllDebridClient struct {
	apiKey     string
	httpClient *http.Client
	baseURL    string
	agent      string
}

// Ensure AllDebridClient implements Provider interface.
var _ Provider = (*AllDebridClient)(nil)

// NewAllDebridClient creates a new AllDebrid API client.
func NewAllDebridClient(apiKey string) *AllDebridClient {
	return &AllDebridClient{
		apiKey:     strings.TrimSpace(apiKey),
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    "https://api.alldebrid.com/v4",
		agent:      "mediastorm",
	}
}

// Name returns the provider identifier.
func (c *AllDebridClient) Name() string {
	return "alldebrid"
}

func init() {
	RegisterProvider("alldebrid", func(apiKey string) Provider {
		return NewAllDebridClient(apiKey)
	})
}

// allDebridResponse is the generic API response wrapper.
type allDebridResponse[T any] struct {
	Status string `json:"status"` // "success" or "error"
	Data   T      `json:"data,omitempty"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// allDebridMagnet represents a magnet upload response.
type allDebridMagnet struct {
	Magnet string `json:"magnet,omitempty"`
	Name   string `json:"name,omitempty"`
	ID     int    `json:"id,omitempty"`
	Hash   string `json:"hash,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Ready  bool   `json:"ready,omitempty"`
}

// allDebridMagnetUploadData wraps the magnet array response.
type allDebridMagnetUploadData struct {
	Magnets []allDebridMagnet `json:"magnets"`
}

// allDebridStatus represents magnet status response.
type allDebridStatus struct {
	ID             int                 `json:"id"`
	Filename       string              `json:"filename"`
	Size           int64               `json:"size"`
	Hash           string              `json:"hash,omitempty"`
	Status         string              `json:"status"`
	StatusCode     int                 `json:"statusCode"`
	Downloaded     int64               `json:"downloaded"`
	Uploaded       int64               `json:"uploaded"`
	Seeders        int                 `json:"seeders"`
	DownloadSpeed  int                 `json:"downloadSpeed"`
	UploadSpeed    int                 `json:"uploadSpeed"`
	UploadDate     int64               `json:"uploadDate"`
	CompletionDate int64               `json:"completionDate"`
	Links          []allDebridLink     `json:"links,omitempty"`
	Files          []allDebridFileNode `json:"files,omitempty"` // v4.1 nested file tree
	Version        int                 `json:"version,omitempty"`
}

// allDebridLink represents a file link in status response (v4 format).
type allDebridLink struct {
	Link     string `json:"link"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

// allDebridFileNode represents a file or directory in the v4.1 nested tree structure.
type allDebridFileNode struct {
	N string              `json:"n"`           // name
	S int64               `json:"s,omitempty"` // size (for files)
	L string              `json:"l,omitempty"` // link (for files)
	E []allDebridFileNode `json:"e,omitempty"` // entries (for directories)
}

// allDebridStatusData wraps status response - uses json.RawMessage to handle both object and array.
type allDebridStatusData struct {
	Magnets json.RawMessage `json:"magnets"`
}

// allDebridUnlock represents an unlocked link response.
type allDebridUnlock struct {
	Link       string `json:"link"`
	Filename   string `json:"filename"`
	Host       string `json:"host"`
	Filesize   int64  `json:"filesize"`
	ID         string `json:"id,omitempty"`
	HostDomain string `json:"hostDomain,omitempty"`
	Delayed    int    `json:"delayed,omitempty"`
}

// allDebridInstantData represents instant availability response.
type allDebridInstantData struct {
	Magnets []struct {
		Magnet  string `json:"magnet"`
		Hash    string `json:"hash"`
		Instant bool   `json:"instant"`
		Files   []struct {
			N string `json:"n"` // filename
			S int64  `json:"s"` // size
		} `json:"files,omitempty"`
	} `json:"magnets"`
}

// AllDebrid status codes
const (
	allDebridStatusInQueue              = 0
	allDebridStatusDownloading          = 1
	allDebridStatusCompressingMoving    = 2
	allDebridStatusUploading            = 3
	allDebridStatusReady                = 4
	allDebridStatusUploadFail           = 5
	allDebridStatusInternalErrorUnpack  = 6
	allDebridStatusNotDownloaded20Min   = 7
	allDebridStatusFileTooBig           = 8
	allDebridStatusInternalError        = 9
	allDebridStatusDownloadTook72h      = 10
	allDebridStatusDeletedOnHoster      = 11
)

// doRequest performs an HTTP request with authorization.
func (c *AllDebridClient) doRequest(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	return c.httpClient.Do(req)
}

// buildURL constructs an API URL with required agent parameter.
func (c *AllDebridClient) buildURL(path string) string {
	return fmt.Sprintf("%s%s", c.baseURL, path)
}

// AddMagnet adds a magnet link to AllDebrid and returns the torrent ID.
func (c *AllDebridClient) AddMagnet(ctx context.Context, magnetURL string) (*AddMagnetResult, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("alldebrid API key not configured")
	}

	trimmedMagnet := strings.TrimSpace(magnetURL)
	if trimmedMagnet == "" {
		return nil, fmt.Errorf("magnet URL is required")
	}

	endpoint := c.buildURL("/magnet/upload")

	formData := url.Values{}
	formData.Set("agent", c.agent)
	formData.Set("magnets[]", trimmedMagnet)

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
		return nil, fmt.Errorf("alldebrid authentication failed: invalid API key")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	var result allDebridResponse[allDebridMagnetUploadData]
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode add magnet response: %w (body: %s)", err, string(body))
	}

	if result.Status != "success" {
		errMsg := "unknown error"
		if result.Error != nil {
			errMsg = result.Error.Message
		}
		return nil, fmt.Errorf("add magnet failed: %s", errMsg)
	}

	if len(result.Data.Magnets) == 0 {
		return nil, fmt.Errorf("no magnet data returned")
	}

	magnet := result.Data.Magnets[0]
	log.Printf("[alldebrid] magnet added: id=%d hash=%s name=%s ready=%v", magnet.ID, magnet.Hash, magnet.Name, magnet.Ready)

	return &AddMagnetResult{
		ID:  strconv.Itoa(magnet.ID),
		URI: trimmedMagnet,
	}, nil
}

// AddTorrentFile uploads a .torrent file to AllDebrid and returns the torrent ID.
func (c *AllDebridClient) AddTorrentFile(ctx context.Context, torrentData []byte, filename string) (*AddMagnetResult, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("alldebrid API key not configured")
	}

	if len(torrentData) == 0 {
		return nil, fmt.Errorf("torrent data is empty")
	}

	if filename == "" {
		filename = "upload.torrent"
	}

	endpoint := c.buildURL("/magnet/upload/file")

	// Create multipart form
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add agent field
	if err := writer.WriteField("agent", c.agent); err != nil {
		return nil, fmt.Errorf("write agent field: %w", err)
	}

	// Add the torrent file
	part, err := writer.CreateFormFile("files[]", filename)
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}

	if _, err := part.Write(torrentData); err != nil {
		return nil, fmt.Errorf("write torrent data: %w", err)
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
		return nil, fmt.Errorf("alldebrid authentication failed: invalid API key")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	var result allDebridResponse[allDebridMagnetUploadData]
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode add torrent response: %w (body: %s)", err, string(body))
	}

	if result.Status != "success" {
		errMsg := "unknown error"
		if result.Error != nil {
			errMsg = result.Error.Message
		}
		return nil, fmt.Errorf("add torrent failed: %s", errMsg)
	}

	if len(result.Data.Magnets) == 0 {
		return nil, fmt.Errorf("no torrent data returned")
	}

	magnet := result.Data.Magnets[0]
	log.Printf("[alldebrid] torrent file uploaded: id=%d hash=%s name=%s", magnet.ID, magnet.Hash, magnet.Name)

	return &AddMagnetResult{
		ID:  strconv.Itoa(magnet.ID),
		URI: filename,
	}, nil
}

// GetTorrentInfo retrieves information about a torrent by ID.
func (c *AllDebridClient) GetTorrentInfo(ctx context.Context, torrentID string) (*TorrentInfo, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("alldebrid API key not configured")
	}

	trimmedID := strings.TrimSpace(torrentID)
	if trimmedID == "" {
		return nil, fmt.Errorf("torrent ID is required")
	}

	// Use v4.1 endpoint for status with files
	endpoint := fmt.Sprintf("%s/magnet/status?agent=%s&id=%s",
		strings.Replace(c.baseURL, "/v4", "/v4.1", 1),
		url.QueryEscape(c.agent),
		trimmedID)

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
		return nil, fmt.Errorf("alldebrid authentication failed: invalid API key")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	var result allDebridResponse[allDebridStatusData]
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode torrent info response: %w (body: %s)", err, string(body))
	}

	if result.Status != "success" {
		errMsg := "unknown error"
		if result.Error != nil {
			errMsg = result.Error.Message
		}
		return nil, fmt.Errorf("get torrent info failed: %s", errMsg)
	}

	// Parse magnets - can be either a single object or an array depending on the request
	var status allDebridStatus
	if len(result.Data.Magnets) == 0 {
		return nil, fmt.Errorf("torrent not found (empty response)")
	}

	// Try parsing as single object first (when requesting by ID)
	if result.Data.Magnets[0] == '{' {
		if err := json.Unmarshal(result.Data.Magnets, &status); err != nil {
			return nil, fmt.Errorf("decode single magnet: %w", err)
		}
	} else {
		// Parse as array
		var magnets []allDebridStatus
		if err := json.Unmarshal(result.Data.Magnets, &magnets); err != nil {
			return nil, fmt.Errorf("decode magnets array: %w", err)
		}
		if len(magnets) == 0 {
			return nil, fmt.Errorf("torrent not found")
		}
		status = magnets[0]
	}

	// Convert to provider-agnostic TorrentInfo
	info := &TorrentInfo{
		ID:       strconv.Itoa(status.ID),
		Filename: status.Filename,
		Hash:     status.Hash,
		Bytes:    status.Size,
		Status:   c.mapStatusCode(status.StatusCode),
		Files:    make([]File, 0),
		Links:    make([]string, 0),
	}

	// Handle v4.1 nested file tree structure
	if len(status.Files) > 0 {
		c.flattenFileTree(status.Files, "", info)
	} else {
		// Fallback to v4 flat links structure
		for i, link := range status.Links {
			info.Files = append(info.Files, File{
				ID:       i + 1,
				Path:     link.Filename,
				Bytes:    link.Size,
				Selected: 1,
			})
			info.Links = append(info.Links, link.Link)
		}
	}

	return info, nil
}

// flattenFileTree recursively flattens the nested v4.1 file tree into Files and Links slices.
func (c *AllDebridClient) flattenFileTree(nodes []allDebridFileNode, basePath string, info *TorrentInfo) {
	for _, node := range nodes {
		path := node.N
		if basePath != "" {
			path = basePath + "/" + node.N
		}

		if len(node.E) > 0 {
			// This is a directory, recurse into it
			c.flattenFileTree(node.E, path, info)
		} else if node.L != "" {
			// This is a file with a link
			info.Files = append(info.Files, File{
				ID:       len(info.Files) + 1,
				Path:     path,
				Bytes:    node.S,
				Selected: 1,
			})
			info.Links = append(info.Links, node.L)
		}
	}
}

// mapStatusCode converts AllDebrid status codes to provider-agnostic status.
func (c *AllDebridClient) mapStatusCode(statusCode int) string {
	switch statusCode {
	case allDebridStatusReady:
		return "downloaded"
	case allDebridStatusInQueue:
		return "queued"
	case allDebridStatusDownloading, allDebridStatusCompressingMoving, allDebridStatusUploading:
		return "downloading"
	case allDebridStatusUploadFail, allDebridStatusInternalErrorUnpack,
		allDebridStatusNotDownloaded20Min, allDebridStatusFileTooBig,
		allDebridStatusInternalError, allDebridStatusDownloadTook72h,
		allDebridStatusDeletedOnHoster:
		return "error"
	default:
		return "unknown"
	}
}

// SelectFiles is a no-op for AllDebrid since files are auto-processed.
// AllDebrid doesn't require explicit file selection like Real-Debrid.
func (c *AllDebridClient) SelectFiles(ctx context.Context, torrentID string, fileIDs string) error {
	// AllDebrid auto-processes all files, so this is a no-op
	log.Printf("[alldebrid] SelectFiles called for torrent %s (no-op, AllDebrid auto-processes)", torrentID)
	return nil
}

// DeleteTorrent removes a torrent from AllDebrid.
func (c *AllDebridClient) DeleteTorrent(ctx context.Context, torrentID string) error {
	if c.apiKey == "" {
		return fmt.Errorf("alldebrid API key not configured")
	}

	trimmedID := strings.TrimSpace(torrentID)
	if trimmedID == "" {
		return fmt.Errorf("torrent ID is required")
	}

	endpoint := c.buildURL("/magnet/delete")

	formData := url.Values{}
	formData.Set("agent", c.agent)
	formData.Set("id", trimmedID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(formData.Encode()))
	if err != nil {
		return fmt.Errorf("build delete torrent request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.doRequest(req)
	if err != nil {
		return fmt.Errorf("delete torrent request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("alldebrid authentication failed: invalid API key")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	var result allDebridResponse[interface{}]
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("decode delete response: %w (body: %s)", err, string(body))
	}

	if result.Status != "success" {
		errMsg := "unknown error"
		if result.Error != nil {
			errMsg = result.Error.Message
		}
		return fmt.Errorf("delete torrent failed: %s", errMsg)
	}

	log.Printf("[alldebrid] torrent %s deleted", torrentID)
	return nil
}

// UnrestrictLink converts an AllDebrid link to an actual download URL.
func (c *AllDebridClient) UnrestrictLink(ctx context.Context, link string) (*UnrestrictResult, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("alldebrid API key not configured")
	}

	trimmedLink := strings.TrimSpace(link)
	if trimmedLink == "" {
		return nil, fmt.Errorf("link is required")
	}

	endpoint := c.buildURL("/link/unlock")

	formData := url.Values{}
	formData.Set("agent", c.agent)
	formData.Set("link", trimmedLink)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build unrestrict request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.doRequest(req)
	if err != nil {
		return nil, fmt.Errorf("unrestrict request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("alldebrid authentication failed: invalid API key")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	var result allDebridResponse[allDebridUnlock]
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode unrestrict response: %w (body: %s)", err, string(body))
	}

	if result.Status != "success" {
		errMsg := "unknown error"
		if result.Error != nil {
			errMsg = result.Error.Message
		}
		return nil, fmt.Errorf("unrestrict failed: %s", errMsg)
	}

	// Check if link is delayed (needs processing)
	if result.Data.Delayed > 0 {
		return nil, fmt.Errorf("link is being processed, try again in %d seconds", result.Data.Delayed)
	}

	log.Printf("[alldebrid] unrestricted link: %s -> %s", trimmedLink, result.Data.Link)

	return &UnrestrictResult{
		ID:          result.Data.ID,
		Filename:    result.Data.Filename,
		Filesize:    result.Data.Filesize,
		DownloadURL: result.Data.Link,
	}, nil
}

// CheckInstantAvailability checks if a torrent hash is cached on AllDebrid.
func (c *AllDebridClient) CheckInstantAvailability(ctx context.Context, infoHash string) (bool, error) {
	if c.apiKey == "" {
		return false, fmt.Errorf("alldebrid API key not configured")
	}

	normalizedHash := strings.ToLower(strings.TrimSpace(infoHash))
	if normalizedHash == "" {
		return false, fmt.Errorf("info hash is required")
	}

	endpoint := fmt.Sprintf("%s/magnet/instant?agent=%s&magnets[]=%s",
		c.baseURL, url.QueryEscape(c.agent), normalizedHash)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, fmt.Errorf("build instant availability request: %w", err)
	}

	resp, err := c.doRequest(req)
	if err != nil {
		return false, fmt.Errorf("instant availability request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return false, fmt.Errorf("alldebrid authentication failed: invalid API key")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("read response body: %w", err)
	}

	var result allDebridResponse[allDebridInstantData]
	if err := json.Unmarshal(body, &result); err != nil {
		return false, fmt.Errorf("decode instant availability response: %w (body: %s)", err, string(body))
	}

	if result.Status != "success" {
		// Not an error, just not available
		log.Printf("[alldebrid] instant availability check failed: %v", result.Error)
		return false, nil
	}

	// Check if hash is instantly available
	for _, magnet := range result.Data.Magnets {
		if strings.EqualFold(magnet.Hash, normalizedHash) && magnet.Instant {
			log.Printf("[alldebrid] instant availability: hash %s is CACHED", normalizedHash)
			return true, nil
		}
	}

	log.Printf("[alldebrid] instant availability: hash %s not cached", normalizedHash)
	return false, nil
}
