package nzbdav

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	baseURLEnvVar    = "STRMR_NZBDAV_URL"
	backendURLEnvVar = "STRMR_NZBDAV_BACKEND_URL"
	backendKeyEnvVar = "STRMR_NZBDAV_BACKEND_KEY"
	PathPrefix       = "/nzbdav/"
	defaultCategory  = "mediastorm"
)

var videoExts = regexp.MustCompile(`(?i)\.(mkv|mp4|m4v|avi|mov|wmv|ts|webm)$`)

type Client struct {
	baseURL    string
	backendURL string
	backendKey string
	httpClient *http.Client
	once       sync.Once
	sabAPIKey  string
	providers  []ProviderInfo
	initErr    error
}

type ProviderInfo struct {
	Host     string
	Port     int
	SSL      bool
	Username string
}

type CompletedItem struct {
	NzoID    string
	JobName  string
	Category string
}

func NewClientFromEnv() *Client {
	baseURL := strings.TrimSpace(os.Getenv(baseURLEnvVar))
	backendURL := strings.TrimSpace(os.Getenv(backendURLEnvVar))
	backendKey := strings.TrimSpace(os.Getenv(backendKeyEnvVar))
	if baseURL == "" || backendURL == "" || backendKey == "" {
		return nil
	}
	log.Printf("[nzbdav] client enabled: frontend=%s backend=%s", baseURL, backendURL)
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		backendURL: strings.TrimRight(backendURL, "/"),
		backendKey: backendKey,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *Client) BaseURL() string { return c.baseURL }

func (c *Client) ProviderHosts() []string {
	c.ensureInit()
	var hosts []string
	for _, p := range c.providers {
		if p.Host != "" {
			hosts = append(hosts, p.Host)
		}
	}
	return hosts
}

func (c *Client) ensureInit() {
	c.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := c.discoverConfig(ctx); err != nil {
			c.initErr = err
			log.Printf("[nzbdav] config discovery failed: %v", err)
		}
	})
}

func (c *Client) discoverConfig(ctx context.Context) error {
	apiURL := fmt.Sprintf("%s/api?mode=get_config&apikey=%s&output=json",
		c.baseURL, url.QueryEscape(c.backendKey))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("config API returned %d", resp.StatusCode)
	}
	var cfgResp struct {
		Config struct {
			Misc    struct{ APIKey string `json:"api_key"` } `json:"misc"`
			Servers []struct {
				Host     string `json:"host"`
				Port     int    `json:"port"`
				SSL      int    `json:"ssl"`
				Username string `json:"username"`
			} `json:"servers"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &cfgResp); err != nil {
		return err
	}
	c.sabAPIKey = cfgResp.Config.Misc.APIKey
	if c.sabAPIKey == "" {
		c.sabAPIKey = c.backendKey
	}
	for _, srv := range cfgResp.Config.Servers {
		if srv.Host != "" {
			c.providers = append(c.providers, ProviderInfo{Host: srv.Host, Port: srv.Port, SSL: srv.SSL == 1, Username: srv.Username})
		}
	}
	log.Printf("[nzbdav] discovered SAB API key and %d provider(s)", len(c.providers))
	return nil
}

func (c *Client) getAPIKey() string {
	c.ensureInit()
	if c.sabAPIKey != "" {
		return c.sabAPIKey
	}
	return c.backendKey
}

// FindCompleted checks nzbdav history for a completed item matching releaseName.
func (c *Client) FindCompleted(ctx context.Context, releaseName string) *CompletedItem {
	normalized := normalizeTitle(releaseName)
	if normalized == "" {
		return nil
	}
	apiURL := fmt.Sprintf("%s/api?mode=history&apikey=%s&output=json&limit=500",
		c.baseURL, url.QueryEscape(c.getAPIKey()))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var hr struct {
		History struct {
			Slots []struct {
				NzoID    string `json:"nzo_id"`
				Name     string `json:"name"`
				Status   string `json:"status"`
				Category string `json:"category"`
			} `json:"slots"`
		} `json:"history"`
	}
	json.NewDecoder(resp.Body).Decode(&hr)
	for _, s := range hr.History.Slots {
		if s.Status == "Completed" && normalizeTitle(s.Name) == normalized {
			log.Printf("[nzbdav] found completed: %q (nzo=%s)", s.Name, s.NzoID)
			return &CompletedItem{NzoID: s.NzoID, JobName: s.Name, Category: s.Category}
		}
	}
	return nil
}

// FindVideoFile searches /content/{category}/{jobName}/ for the largest video file.
func (c *Client) FindVideoFile(ctx context.Context, category, jobName string) (string, error) {
	if category == "" {
		category = defaultCategory
	}
	contentPath := fmt.Sprintf("/content/%s/%s", url.PathEscape(category), url.PathEscape(jobName))
	files, err := c.listWebDAVDir(ctx, contentPath)
	if err != nil {
		return "", fmt.Errorf("list content dir: %w", err)
	}
	var bestFile string
	var bestSize int64
	for _, f := range files {
		if videoExts.MatchString(f.name) && f.size > bestSize {
			bestFile = f.name
			bestSize = f.size
		}
	}
	if bestFile == "" {
		return "", fmt.Errorf("no video file found in %s", contentPath)
	}
	viewPath := path.Join(contentPath, bestFile)
	log.Printf("[nzbdav] found video: %s (%d bytes)", viewPath, bestSize)
	return viewPath, nil
}

type davEntry struct {
	name string
	size int64
}

func (c *Client) listWebDAVDir(ctx context.Context, dirPath string) ([]davEntry, error) {
	targetURL := fmt.Sprintf("%s%s", c.baseURL, dirPath)
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Depth", "1")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 207 {
		return nil, fmt.Errorf("PROPFIND returned %d for %s", resp.StatusCode, dirPath)
	}
	// Parse WebDAV multistatus XML — nzbdav uses DAV: namespace prefixed as D:
	// Try namespace-aware parsing first, fall back to regex if it returns no results.
	type davProp struct {
		DisplayName   string `xml:"displayname"`
		ContentLength int64  `xml:"getcontentlength"`
		IsCollection  int    `xml:"iscollection"`
	}
	type davPropstat struct {
		Prop davProp `xml:"prop"`
	}
	type davResponse struct {
		Href     string      `xml:"href"`
		Propstat davPropstat `xml:"propstat"`
	}
	type davMultistatus struct {
		Responses []davResponse `xml:"response"`
	}

	var ms davMultistatus
	xml.Unmarshal(body, &ms)

	dirBase := path.Base(dirPath)
	var entries []davEntry
	for _, r := range ms.Responses {
		name := r.Propstat.Prop.DisplayName
		if name == "" {
			name = path.Base(r.Href)
		}
		if name == dirBase || name == "" || name == "." {
			continue
		}
		if r.Propstat.Prop.IsCollection == 1 {
			continue
		}
		entries = append(entries, davEntry{name: name, size: r.Propstat.Prop.ContentLength})
	}

	// Fallback: regex-based extraction if structured parsing found nothing
	if len(entries) == 0 {
		log.Printf("[nzbdav] PROPFIND XML parsed 0 file entries, trying regex fallback")
		entries = parsePropfindRegex(body, dirBase)
	}

	return entries, nil
}

// parsePropfindRegex extracts file entries from WebDAV XML using regex,
// as a fallback when namespace-aware XML parsing fails.
func parsePropfindRegex(body []byte, dirBase string) []davEntry {
	var entries []davEntry
	nameRe := regexp.MustCompile(`<[^>]*displayname>([^<]+)</`)
	sizeRe := regexp.MustCompile(`<[^>]*getcontentlength>(\d+)</`)
	collRe := regexp.MustCompile(`<[^>]*iscollection>(\d)</`)
	respRe := regexp.MustCompile(`(?s)<[^>]*response>(.*?)</[^>]*response>`)

	for _, match := range respRe.FindAllSubmatch(body, -1) {
		block := match[1]
		nameMatch := nameRe.FindSubmatch(block)
		if nameMatch == nil {
			continue
		}
		name := string(nameMatch[1])
		if name == dirBase || name == "" {
			continue
		}
		collMatch := collRe.FindSubmatch(block)
		if collMatch != nil && string(collMatch[1]) == "1" {
			continue
		}
		var size int64
		sizeMatch := sizeRe.FindSubmatch(block)
		if sizeMatch != nil {
			fmt.Sscanf(string(sizeMatch[1]), "%d", &size)
		}
		entries = append(entries, davEntry{name: name, size: size})
	}
	log.Printf("[nzbdav] regex fallback found %d file entries", len(entries))
	return entries
}

type sabResponse struct {
	Status bool     `json:"status"`
	Error  *string  `json:"error,omitempty"`
	NzoIDs []string `json:"nzo_ids,omitempty"`
}

func (c *Client) SubmitNZB(ctx context.Context, nzbBytes []byte, filename string) (string, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("nzbfile", filename)
	io.Copy(part, bytes.NewReader(nzbBytes))
	writer.WriteField("priority", "2")
	writer.WriteField("cat", defaultCategory)
	writer.Close()
	apiURL := fmt.Sprintf("%s/api?mode=addfile&apikey=%s&output=json", c.baseURL, url.QueryEscape(c.getAPIKey()))
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("submit NZB: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("nzbdav HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	var sabResp sabResponse
	json.Unmarshal(respBody, &sabResp)
	if !sabResp.Status {
		msg := "unknown"
		if sabResp.Error != nil {
			msg = *sabResp.Error
		}
		return "", fmt.Errorf("nzbdav: %s", msg)
	}
	if len(sabResp.NzoIDs) == 0 {
		return "", fmt.Errorf("nzbdav returned no NZO ID")
	}
	log.Printf("[nzbdav] submitted %q → nzo=%s", filename, sabResp.NzoIDs[0])
	return sabResp.NzoIDs[0], nil
}

// WaitForCompletion polls until completed or failed. Returns jobName and category.
func (c *Client) WaitForCompletion(ctx context.Context, nzoID string, poll time.Duration) (jobName, category string, err error) {
	if poll <= 0 {
		poll = 2 * time.Second
	}
	for {
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		default:
		}
		if name, cat, status, failMsg, _ := c.checkHistorySlot(ctx, nzoID); status == "Completed" {
			return name, cat, nil
		} else if status == "Failed" {
			if failMsg != "" {
				return "", "", fmt.Errorf("nzbdav failed: %s", failMsg)
			}
			return "", "", fmt.Errorf("nzbdav failed for %s", nzoID)
		}
		if qs, _ := c.checkQueue(ctx, nzoID); qs != "" {
			log.Printf("[nzbdav] %s: %s", nzoID, qs)
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(poll):
		}
	}
}

func (c *Client) checkHistorySlot(ctx context.Context, nzoID string) (name, category, status, failMsg string, err error) {
	apiURL := fmt.Sprintf("%s/api?mode=history&apikey=%s&output=json", c.baseURL, url.QueryEscape(c.getAPIKey()))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", "", "", err
	}
	defer resp.Body.Close()
	var hr struct {
		History struct {
			Slots []struct {
				NzoID       string `json:"nzo_id"`
				Name        string `json:"name"`
				Status      string `json:"status"`
				Category    string `json:"category"`
				FailMessage string `json:"fail_message"`
			} `json:"slots"`
		} `json:"history"`
	}
	json.NewDecoder(resp.Body).Decode(&hr)
	for _, s := range hr.History.Slots {
		if s.NzoID == nzoID {
			return s.Name, s.Category, s.Status, s.FailMessage, nil
		}
	}
	return "", "", "", "", nil
}

func (c *Client) checkQueue(ctx context.Context, nzoID string) (string, error) {
	apiURL := fmt.Sprintf("%s/api?mode=queue&apikey=%s&output=json", c.baseURL, url.QueryEscape(c.getAPIKey()))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var qr struct {
		Queue struct {
			Slots []struct {
				NzoID      string `json:"nzo_id"`
				Status     string `json:"status"`
				Percentage string `json:"percentage"`
			} `json:"slots"`
		} `json:"queue"`
	}
	json.NewDecoder(resp.Body).Decode(&qr)
	for _, s := range qr.Queue.Slots {
		if s.NzoID == nzoID {
			return fmt.Sprintf("%s (%s%%)", s.Status, s.Percentage), nil
		}
	}
	return "", nil
}

func normalizeTitle(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ".nzb")
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, ".", " ")
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.ReplaceAll(s, "_", " ")
	return strings.Join(strings.Fields(s), " ")
}
