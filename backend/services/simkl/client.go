package simkl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

var apiBaseURL = "https://api.simkl.com"

const (
	appName    = "mediastorm"
	appVersion = "1.0"
	userAgent  = "mediastorm/1.0"
)

// SetBaseURLForTest overrides the Simkl API base URL.
func SetBaseURLForTest(url string) {
	apiBaseURL = url
}

// Client handles Simkl API requests.
type Client struct {
	httpClient         *http.Client
	mu                 sync.Mutex
	lastPostByClientID map[string]time.Time
}

// NewClient creates a Simkl API client.
func NewClient() *Client {
	return &Client{
		httpClient:         &http.Client{Timeout: 15 * time.Second},
		lastPostByClientID: make(map[string]time.Time),
	}
}

// SetHTTPClientForTest overrides the HTTP client.
func (c *Client) SetHTTPClientForTest(httpClient *http.Client) {
	if httpClient != nil {
		c.httpClient = httpClient
	}
}

type IDs struct {
	Simkl int    `json:"simkl,omitempty"`
	IMDB  string `json:"imdb,omitempty"`
	TMDB  int    `json:"tmdb,omitempty"`
	TVDB  int    `json:"tvdb,omitempty"`
}

type Movie struct {
	Title string `json:"title,omitempty"`
	Year  int    `json:"year,omitempty"`
	IDs   IDs    `json:"ids,omitempty"`
}

type Show struct {
	Title string `json:"title,omitempty"`
	Year  int    `json:"year,omitempty"`
	IDs   IDs    `json:"ids,omitempty"`
}

type Episode struct {
	Season int `json:"season,omitempty"`
	Number int `json:"number,omitempty"`
}

type ScrobbleRequest struct {
	Progress float64  `json:"progress"`
	Movie    *Movie   `json:"movie,omitempty"`
	Show     *Show    `json:"show,omitempty"`
	Episode  *Episode `json:"episode,omitempty"`
}

type ScrobbleResponse struct {
	ID       int64   `json:"id,omitempty"`
	Action   string  `json:"action,omitempty"`
	Progress float64 `json:"progress,omitempty"`
}

type SyncHistoryMovie struct {
	WatchedAt string `json:"watched_at,omitempty"`
	Title     string `json:"title,omitempty"`
	Year      int    `json:"year,omitempty"`
	IDs       IDs    `json:"ids,omitempty"`
}

type SyncHistoryShow struct {
	Title   string              `json:"title,omitempty"`
	Year    int                 `json:"year,omitempty"`
	IDs     IDs                 `json:"ids,omitempty"`
	Seasons []SyncHistorySeason `json:"seasons,omitempty"`
}

type SyncHistorySeason struct {
	Number   int                  `json:"number"`
	Episodes []SyncHistoryEpisode `json:"episodes,omitempty"`
}

type SyncHistoryEpisode struct {
	Number    int    `json:"number"`
	WatchedAt string `json:"watched_at,omitempty"`
}

type SyncHistoryRequest struct {
	Movies []SyncHistoryMovie `json:"movies,omitempty"`
	Shows  []SyncHistoryShow  `json:"shows,omitempty"`
}

type ActivityResponse map[string]interface{}

type AllItemsResponse struct {
	Movies []json.RawMessage `json:"movies,omitempty"`
	Shows  []json.RawMessage `json:"shows,omitempty"`
	Anime  []json.RawMessage `json:"anime,omitempty"`
	Raw    json.RawMessage   `json:"-"`
}

type apiCredentials struct {
	clientID    string
	accessToken string
}

type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
}

type PinResponse struct {
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type PinTokenResponse struct {
	AccessToken string `json:"access_token"`
	UserID      string `json:"user_id,omitempty"`
	Result      string `json:"result,omitempty"`
	Message     string `json:"message,omitempty"`
}

func (c apiCredentials) valid() bool {
	return c.clientID != "" && c.accessToken != ""
}

func (c *Client) ScrobbleStart(clientID, accessToken string, req ScrobbleRequest) (*ScrobbleResponse, error) {
	return c.scrobble(apiCredentials{clientID: clientID, accessToken: accessToken}, "start", req)
}

func (c *Client) ScrobblePause(clientID, accessToken string, req ScrobbleRequest) (*ScrobbleResponse, error) {
	return c.scrobble(apiCredentials{clientID: clientID, accessToken: accessToken}, "pause", req)
}

func (c *Client) ScrobbleStop(clientID, accessToken string, req ScrobbleRequest) (*ScrobbleResponse, error) {
	return c.scrobble(apiCredentials{clientID: clientID, accessToken: accessToken}, "stop", req)
}

func (c *Client) SyncHistory(clientID, accessToken string, req SyncHistoryRequest) error {
	_, err := c.post(apiCredentials{clientID: clientID, accessToken: accessToken}, "/sync/history", req, nil)
	return err
}

func (c *Client) GetActivities(clientID, accessToken string) (ActivityResponse, error) {
	var out ActivityResponse
	if err := c.get(apiCredentials{clientID: clientID, accessToken: accessToken}, "/sync/activities", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetInitialSyncItems(clientID, accessToken, bucket string) (*AllItemsResponse, error) {
	if bucket != "movies" && bucket != "shows" && bucket != "anime" {
		return nil, fmt.Errorf("unsupported simkl sync bucket: %s", bucket)
	}
	q := url.Values{}
	q.Set("extended", "full")
	q.Set("episode_watched_at", "yes")

	var out AllItemsResponse
	if err := c.get(apiCredentials{clientID: clientID, accessToken: accessToken}, "/sync/"+bucket, q, &out); err != nil {
		return nil, err
	}
	if len(out.Raw) > 0 && out.Movies == nil && out.Shows == nil && out.Anime == nil {
		var items []json.RawMessage
		if err := json.Unmarshal(out.Raw, &items); err == nil {
			switch bucket {
			case "movies":
				out.Movies = items
			case "shows":
				out.Shows = items
			case "anime":
				out.Anime = items
			}
		}
	}
	return &out, nil
}

func (c *Client) GetAllItemsSince(clientID, accessToken, dateFrom string) (*AllItemsResponse, error) {
	q := url.Values{}
	if dateFrom != "" {
		q.Set("date_from", dateFrom)
	}
	q.Set("extended", "full")
	q.Set("episode_watched_at", "yes")

	var out AllItemsResponse
	if err := c.get(apiCredentials{clientID: clientID, accessToken: accessToken}, "/sync/all-items", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) StartPINAuth(clientID string) (*PinResponse, error) {
	endpoint, err := buildAPIURL("/oauth/pin", clientID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create pin request: %w", err)
	}
	req.Header.Set("simkl-api-key", clientID)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("simkl pin request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("simkl pin request returned %d: %s", resp.StatusCode, string(respBody))
	}

	var pin PinResponse
	if err := json.NewDecoder(resp.Body).Decode(&pin); err != nil {
		return nil, fmt.Errorf("decode pin response: %w", err)
	}
	return &pin, nil
}

func (c *Client) CheckPINAuth(clientID, userCode string) (*PinTokenResponse, error) {
	endpoint, err := buildAPIURL("/oauth/pin/"+url.PathEscape(userCode), clientID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create pin check request: %w", err)
	}
	req.Header.Set("simkl-api-key", clientID)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("simkl pin check request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("simkl pin check returned %d: %s", resp.StatusCode, string(respBody))
	}

	var token PinTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, fmt.Errorf("decode pin check response: %w", err)
	}
	return &token, nil
}

func (c *Client) ExchangeCode(clientID, clientSecret, redirectURI, code string) (*TokenResponse, error) {
	payload := map[string]string{
		"code":          code,
		"client_id":     clientID,
		"client_secret": clientSecret,
		"redirect_uri":  redirectURI,
		"grant_type":    "authorization_code",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal token request: %w", err)
	}

	endpoint, err := buildAPIURL("/oauth/token", clientID)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("simkl-api-key", clientID)
	req.Header.Set("User-Agent", userAgent)

	c.waitForPostSlot(clientID)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("simkl token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("simkl token exchange returned %d: %s", resp.StatusCode, string(respBody))
	}

	var token TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	return &token, nil
}

func (c *Client) scrobble(creds apiCredentials, action string, req ScrobbleRequest) (*ScrobbleResponse, error) {
	var out ScrobbleResponse
	_, err := c.post(creds, "/scrobble/"+action, req, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) get(creds apiCredentials, path string, extraQuery url.Values, out interface{}) error {
	if !creds.valid() {
		return errors.New("simkl credentials not configured")
	}

	endpoint, err := buildAPIURL(path, creds.clientID)
	if err != nil {
		return err
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parse simkl url: %w", err)
	}
	q := u.Query()
	for key, values := range extraQuery {
		for _, value := range values {
			q.Set(key, value)
		}
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("simkl-api-key", creds.clientID)
	req.Header.Set("Authorization", "Bearer "+creds.accessToken)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("simkl api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("simkl %s returned %d: %s", path, resp.StatusCode, string(respBody))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if allItems, ok := out.(*AllItemsResponse); ok {
		allItems.Raw = append(allItems.Raw[:0], body...)
		if len(body) > 0 && body[0] == '[' {
			return nil
		}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (c *Client) post(creds apiCredentials, path string, payload interface{}, out interface{}) (int, error) {
	if !creds.valid() {
		return 0, errors.New("simkl credentials not configured")
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal request: %w", err)
	}

	endpoint, err := buildAPIURL(path, creds.clientID)
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("simkl-api-key", creds.clientID)
	req.Header.Set("Authorization", "Bearer "+creds.accessToken)
	req.Header.Set("User-Agent", userAgent)

	c.waitForPostSlot(creds.clientID)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("simkl api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return resp.StatusCode, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return resp.StatusCode, fmt.Errorf("simkl %s returned %d: %s", path, resp.StatusCode, string(respBody))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

func buildAPIURL(path, clientID string) (string, error) {
	u, err := url.Parse(apiBaseURL + path)
	if err != nil {
		return "", fmt.Errorf("parse simkl url: %w", err)
	}
	q := u.Query()
	q.Set("client_id", clientID)
	q.Set("app-name", appName)
	q.Set("app-version", appVersion)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (c *Client) waitForPostSlot(clientID string) {
	if clientID == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastPostByClientID == nil {
		c.lastPostByClientID = make(map[string]time.Time)
	}

	now := time.Now()
	if last, ok := c.lastPostByClientID[clientID]; ok {
		if wait := time.Second - now.Sub(last); wait > 0 {
			time.Sleep(wait)
			now = time.Now()
		}
	}
	c.lastPostByClientID[clientID] = now
}
