package trakt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/internal/apiusage"
)

// ErrNotFound is returned when Trakt cannot find the requested item (404).
var ErrNotFound = errors.New("trakt: item not found")

var traktAPIBaseURL = "https://api.trakt.tv"

const traktAPIVersion = "2"

// setBaseURL overrides the Trakt API base URL (used by tests).
func setBaseURL(url string) {
	traktAPIBaseURL = url
}

// SetBaseURLForTest overrides the Trakt API base URL. Intended for tests outside this package.
func SetBaseURLForTest(url string) {
	setBaseURL(url)
}

// GetBaseURLForTest returns the current Trakt API base URL. Intended for tests outside this package.
func GetBaseURLForTest() string {
	return traktAPIBaseURL
}

// SetHTTPClientForTest overrides the HTTP client. Intended for tests outside this package.
func (c *Client) SetHTTPClientForTest(httpClient *http.Client) {
	if httpClient != nil {
		c.httpClient = httpClient
	}
}

// Client handles Trakt API interactions for OAuth and data fetching.
//
// A single base Client is shared process-wide, but Trakt credentials
// (client ID/secret) are per Trakt account. Concurrent callers must NOT mutate
// the shared client's credentials; instead derive a request-scoped client with
// WithCredentials, which shares the HTTP client, refresh locks and list cache
// with its parent but carries its own immutable credentials.
type Client struct {
	httpClient *http.Client

	credMu       sync.RWMutex
	clientID     string
	clientSecret string

	// shared holds state common to a base client and all clients derived
	// from it via WithCredentials.
	shared *clientShared
}

// clientShared is state shared between a base client and derived clients.
type clientShared struct {
	// Per-account mutexes for coordinating token refresh
	refreshMuMap   map[string]*sync.Mutex
	refreshMuGuard sync.Mutex

	// lists caches list/watchlist fetch results for Home shelves.
	lists *listCache
}

func newClientShared() *clientShared {
	return &clientShared{
		refreshMuMap: make(map[string]*sync.Mutex),
		lists:        newListCache(DefaultListCacheTTL),
	}
}

// DeviceCodeResponse represents the response from /oauth/device/code
type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// TokenResponse represents the response from /oauth/device/token
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	CreatedAt    int64  `json:"created_at"`
}

// UserProfile represents basic Trakt user information
type UserProfile struct {
	Username string `json:"username"`
	Name     string `json:"name,omitempty"`
	VIP      bool   `json:"vip"`
	Private  bool   `json:"private"`
	IDs      struct {
		Slug string `json:"slug"`
	} `json:"ids"`
}

// IDs holds external identifiers for a media item
type IDs struct {
	Trakt int    `json:"trakt,omitempty"`
	Slug  string `json:"slug,omitempty"`
	IMDB  string `json:"imdb,omitempty"`
	TMDB  int    `json:"tmdb,omitempty"`
	TVDB  int    `json:"tvdb,omitempty"`
}

// Movie represents a Trakt movie
type Movie struct {
	Title string `json:"title"`
	Year  int    `json:"year"`
	IDs   IDs    `json:"ids"`
}

// Show represents a Trakt TV show
type Show struct {
	Title string `json:"title"`
	Year  int    `json:"year"`
	IDs   IDs    `json:"ids"`
}

// Episode represents a Trakt episode
type Episode struct {
	Season    int    `json:"season"`
	Number    int    `json:"number"`
	NumberAbs int    `json:"number_abs,omitempty"`
	Title     string `json:"title"`
	IDs       IDs    `json:"ids"`
}

// WatchlistItem represents an item from the Trakt watchlist
type WatchlistItem struct {
	Rank     int       `json:"rank"`
	ListedAt time.Time `json:"listed_at"`
	Type     string    `json:"type"` // "movie" or "show"
	Movie    *Movie    `json:"movie,omitempty"`
	Show     *Show     `json:"show,omitempty"`
}

// HistoryItem represents an item from Trakt watch history
type HistoryItem struct {
	ID        int64     `json:"id"`
	WatchedAt time.Time `json:"watched_at"`
	Action    string    `json:"action"` // "watch" or "scrobble"
	Type      string    `json:"type"`   // "movie" or "episode"
	Movie     *Movie    `json:"movie,omitempty"`
	Episode   *Episode  `json:"episode,omitempty"`
	Show      *Show     `json:"show,omitempty"`
}

// NewClient creates a new Trakt API client
func NewClient(clientID, clientSecret string) *Client {
	return &Client{
		httpClient:   apiusage.TrackClient(&http.Client{Timeout: 30 * time.Second}, "Trakt", "API request"),
		clientID:     clientID,
		clientSecret: clientSecret,
		shared:       newClientShared(),
	}
}

// WithCredentials returns a lightweight client bound to the given Trakt app
// credentials. It shares the HTTP client, token-refresh locks and list cache
// with c, so it is cheap to create per request/account. Use this instead of
// UpdateCredentials on any path that may run concurrently.
func (c *Client) WithCredentials(clientID, clientSecret string) *Client {
	if c == nil {
		return nil
	}
	return &Client{
		httpClient:   c.httpClient,
		clientID:     clientID,
		clientSecret: clientSecret,
		shared:       c.shared,
	}
}

// ForAccount returns a client bound to the account's Trakt app credentials.
func (c *Client) ForAccount(account *config.TraktAccount) *Client {
	if account == nil {
		return c
	}
	return c.WithCredentials(account.ClientID, account.ClientSecret)
}

// credentials returns the client's current app credentials.
func (c *Client) credentials() (clientID, clientSecret string) {
	c.credMu.RLock()
	defer c.credMu.RUnlock()
	return c.clientID, c.clientSecret
}

// getRefreshMu returns the per-account mutex for token refresh coordination.
func (c *Client) getRefreshMu(accountID string) *sync.Mutex {
	sh := c.shared
	if sh == nil {
		// Client not built via NewClient; fall back to a lock local to this
		// call (no cross-goroutine serialization possible without shared state).
		return &sync.Mutex{}
	}
	sh.refreshMuGuard.Lock()
	defer sh.refreshMuGuard.Unlock()
	if sh.refreshMuMap == nil {
		sh.refreshMuMap = make(map[string]*sync.Mutex)
	}
	mu, ok := sh.refreshMuMap[accountID]
	if !ok {
		mu = &sync.Mutex{}
		sh.refreshMuMap[accountID] = mu
	}
	return mu
}

// EnsureValidToken checks if the account's access token is valid and refreshes
// it if needed, using a per-account mutex to prevent concurrent refresh races.
// Trakt refresh tokens are single-use: if two goroutines try to refresh with
// the same token, the second will get invalid_grant, permanently breaking auth.
func (c *Client) EnsureValidToken(account *config.TraktAccount, configManager *config.Manager) (string, error) {
	if account.AccessToken == "" {
		return "", nil
	}

	// Fast path: token not expiring within 1 hour, no refresh needed
	if account.ExpiresAt > 0 && account.ExpiresAt-time.Now().Unix() >= 3600 {
		return account.AccessToken, nil
	}

	if account.RefreshToken == "" {
		return "", nil
	}

	// Acquire per-account lock to serialize refresh attempts
	mu := c.getRefreshMu(account.ID)
	mu.Lock()
	defer mu.Unlock()

	// Double-check: re-load config to see if another goroutine already refreshed
	settings, err := configManager.Load()
	if err != nil {
		return "", fmt.Errorf("load config for token refresh: %w", err)
	}

	freshAccount := settings.Trakt.GetAccountByID(account.ID)
	if freshAccount == nil {
		return "", fmt.Errorf("trakt account %s not found after lock", account.ID)
	}

	// If the token was already refreshed by another goroutine, use the new one
	if freshAccount.ExpiresAt > 0 && freshAccount.ExpiresAt-time.Now().Unix() >= 3600 {
		// Update the caller's account struct with fresh tokens
		account.AccessToken = freshAccount.AccessToken
		account.RefreshToken = freshAccount.RefreshToken
		account.ExpiresAt = freshAccount.ExpiresAt
		return freshAccount.AccessToken, nil
	}

	// We still need to refresh — use a client bound to this account's
	// credentials (never mutate the shared client's credentials).
	// The refresh deliberately ignores request cancellation: Trakt refresh
	// tokens are single-use, so abandoning a refresh after Trakt consumed the
	// token but before we persist the new one would break the account.
	log.Printf("[trakt] Refreshing token for account %s (%s)", freshAccount.Name, freshAccount.ID)
	token, err := c.WithCredentials(freshAccount.ClientID, freshAccount.ClientSecret).RefreshAccessToken(freshAccount.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("refresh trakt token: %w", err)
	}

	// Save new tokens to config
	freshAccount.AccessToken = token.AccessToken
	freshAccount.RefreshToken = token.RefreshToken
	freshAccount.ExpiresAt = token.CreatedAt + int64(token.ExpiresIn)
	settings.Trakt.UpdateAccount(*freshAccount)

	if err := configManager.Save(settings); err != nil {
		return "", fmt.Errorf("save refreshed trakt token: %w", err)
	}

	// Update the caller's account struct
	account.AccessToken = token.AccessToken
	account.RefreshToken = token.RefreshToken
	account.ExpiresAt = freshAccount.ExpiresAt

	return token.AccessToken, nil
}

// setTraktHeaders adds required Trakt API headers to a request
func (c *Client) setTraktHeaders(req *http.Request, accessToken string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("trakt-api-version", traktAPIVersion)
	clientID, _ := c.credentials()
	req.Header.Set("trakt-api-key", clientID)
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
}

// GetDeviceCode initiates the device code OAuth flow
func (c *Client) GetDeviceCode() (*DeviceCodeResponse, error) {
	clientID, _ := c.credentials()
	payload := map[string]string{
		"client_id": clientID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, traktAPIBaseURL+"/oauth/device/code", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	c.setTraktHeaders(req, "")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("trakt api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("trakt device code failed: %s - %s", resp.Status, string(respBody))
	}

	var deviceCode DeviceCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&deviceCode); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &deviceCode, nil
}

// PollForToken polls for the OAuth token after user has authorized
// Returns nil, nil if still pending authorization
func (c *Client) PollForToken(deviceCode string) (*TokenResponse, error) {
	clientID, clientSecret := c.credentials()
	payload := map[string]string{
		"code":      deviceCode,
		"client_id": clientID,
	}
	addClientSecret(payload, clientSecret)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, traktAPIBaseURL+"/oauth/device/token", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	c.setTraktHeaders(req, "")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("trakt api request: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var token TokenResponse
		if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		return &token, nil
	case http.StatusBadRequest:
		// 400 means still waiting for user to authorize - this is expected during polling
		return nil, nil
	case http.StatusGone:
		return nil, fmt.Errorf("device code expired")
	case http.StatusConflict:
		return nil, fmt.Errorf("device code already used")
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("polling too fast, slow down")
	default:
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("trakt token poll failed: %s - %s", resp.Status, string(respBody))
	}
}

// RefreshAccessToken refreshes an expired access token
func (c *Client) RefreshAccessToken(refreshToken string) (*TokenResponse, error) {
	clientID, clientSecret := c.credentials()
	payload := map[string]string{
		"refresh_token": refreshToken,
		"client_id":     clientID,
		"redirect_uri":  OOBRedirectURI,
		"grant_type":    "refresh_token",
	}
	addClientSecret(payload, clientSecret)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, traktAPIBaseURL+"/oauth/token", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	c.setTraktHeaders(req, "")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("trakt api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("trakt token refresh failed: %s - %s", resp.Status, string(respBody))
	}

	var token TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &token, nil
}

// GetUserProfile retrieves information about the authenticated user
func (c *Client) GetUserProfile(accessToken string) (*UserProfile, error) {
	req, err := http.NewRequest(http.MethodGet, traktAPIBaseURL+"/users/me", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	c.setTraktHeaders(req, accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("trakt api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("trakt user profile failed: %s - %s", resp.Status, string(respBody))
	}

	var profile UserProfile
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &profile, nil
}

// GetWatchlist retrieves the user's watchlist with pagination
// Returns items, total item count, and error
func (c *Client) GetWatchlist(accessToken string, page, limit int) ([]WatchlistItem, int, error) {
	return c.GetWatchlistCtx(context.Background(), accessToken, page, limit)
}

// GetWatchlistCtx is GetWatchlist bound to ctx; cancelling ctx aborts the request.
func (c *Client) GetWatchlistCtx(ctx context.Context, accessToken string, page, limit int) ([]WatchlistItem, int, error) {
	url := fmt.Sprintf("%s/users/me/watchlist?page=%d&limit=%d", traktAPIBaseURL, page, limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}

	c.setTraktHeaders(req, accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("trakt api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, 0, fmt.Errorf("trakt watchlist failed: %s - %s", resp.Status, string(respBody))
	}

	// Get total count from headers
	totalCount := 0
	if totalHeader := resp.Header.Get("X-Pagination-Item-Count"); totalHeader != "" {
		totalCount, _ = strconv.Atoi(totalHeader)
	}

	var items []WatchlistItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, 0, fmt.Errorf("decode response: %w", err)
	}

	return items, totalCount, nil
}

// GetAllWatchlist retrieves the complete watchlist (all pages)
func (c *Client) GetAllWatchlist(accessToken string) ([]WatchlistItem, error) {
	return c.GetAllWatchlistCtx(context.Background(), accessToken)
}

// GetAllWatchlistCtx retrieves all watchlist pages, stopping when ctx is cancelled.
func (c *Client) GetAllWatchlistCtx(ctx context.Context, accessToken string) ([]WatchlistItem, error) {
	var allItems []WatchlistItem
	page := 1
	limit := 100 // Max items per page

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		items, totalCount, err := c.GetWatchlistCtx(ctx, accessToken, page, limit)
		if err != nil {
			return nil, err
		}

		allItems = append(allItems, items...)

		// Check if we have all items
		if len(allItems) >= totalCount || len(items) == 0 {
			break
		}

		page++
	}

	return allItems, nil
}

// GetWatchHistory retrieves the user's watch history with pagination
// historyType can be "movies", "shows", "episodes", or empty for all
// Returns items, total item count, and error
func (c *Client) GetWatchHistory(accessToken string, page, limit int, historyType string) ([]HistoryItem, int, error) {
	return c.getWatchHistoryPage(accessToken, page, limit, historyType, time.Time{})
}

// A page owns and closes its response before the next request starts.
func (c *Client) getWatchHistoryPage(accessToken string, page, limit int, historyType string, since time.Time) ([]HistoryItem, int, error) {
	url := traktAPIBaseURL + "/users/me/history"
	if historyType != "" {
		url += "/" + historyType
	}
	url += fmt.Sprintf("?page=%d&limit=%d", page, limit)
	if !since.IsZero() {
		url += "&start_at=" + since.UTC().Format(time.RFC3339)
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}

	c.setTraktHeaders(req, accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("trakt api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, 0, fmt.Errorf("trakt history failed: %s - %s", resp.Status, string(respBody))
	}

	// Get total count from headers
	totalCount := 0
	if totalHeader := resp.Header.Get("X-Pagination-Item-Count"); totalHeader != "" {
		totalCount, _ = strconv.Atoi(totalHeader)
	}

	var items []HistoryItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, 0, fmt.Errorf("decode response: %w", err)
	}
	if items == nil {
		return nil, 0, errors.New("Trakt returned null instead of history")
	}

	return items, totalCount, nil
}

// GetAllWatchHistory retrieves the complete watch history (all pages).
func (c *Client) GetAllWatchHistory(accessToken string) ([]HistoryItem, error) {
	return c.GetWatchHistorySince(accessToken, time.Time{})
}

// GetWatchHistorySince retrieves all pages, without a date filter when since is zero.
func (c *Client) GetWatchHistorySince(accessToken string, since time.Time) ([]HistoryItem, error) {
	var allItems []HistoryItem
	const limit = 100
	for page := 1; ; page++ {
		items, totalCount, err := c.getWatchHistoryPage(accessToken, page, limit, "", since)
		if err != nil {
			return nil, err
		}
		allItems = append(allItems, items...)
		if len(items) == 0 || (totalCount > 0 && len(allItems) >= totalCount) ||
			(totalCount == 0 && len(items) < limit) {
			break
		}
	}
	return allItems, nil
}

// IDsToMap converts IDs struct to a map for compatibility with watchlist service
func IDsToMap(ids IDs) map[string]string {
	result := make(map[string]string)
	if ids.IMDB != "" {
		result["imdb"] = ids.IMDB
	}
	if ids.TMDB != 0 {
		result["tmdb"] = strconv.Itoa(ids.TMDB)
	}
	if ids.TVDB != 0 {
		result["tvdb"] = strconv.Itoa(ids.TVDB)
	}
	if ids.Trakt != 0 {
		result["trakt"] = strconv.Itoa(ids.Trakt)
	}
	return result
}

// NormalizeMediaType converts Trakt media type to mediastorm media type
func NormalizeMediaType(traktType string) string {
	switch traktType {
	case "movie":
		return "movie"
	case "show":
		return "series"
	case "episode":
		return "episode"
	default:
		return traktType
	}
}

// OOBRedirectURI is the redirect URI Trakt expects on token refreshes for
// device-flow apps. Apps must list it under their Redirect URIs.
const OOBRedirectURI = "urn:ietf:wg:oauth:2.0:oob"

// addClientSecret adds client_secret to an OAuth payload only when one is
// configured. Trakt stopped issuing secrets for apps created from 2026-10-01
// (they use PKCE / public-client device flow); legacy apps still send theirs.
func addClientSecret(payload map[string]string, clientSecret string) {
	if secret := strings.TrimSpace(clientSecret); secret != "" {
		payload["client_secret"] = secret
	}
}

// HasCredentials reports whether a Client ID is configured. A Client Secret is
// optional: Trakt no longer issues one for new apps.
func (c *Client) HasCredentials() bool {
	clientID, _ := c.credentials()
	return clientID != ""
}

// UpdateCredentials updates the client credentials in place.
//
// This is memory-safe, but it mutates state seen by every user of this
// client. Never call it on a client shared across concurrent requests or
// accounts; use WithCredentials/ForAccount to get a per-account client.
func (c *Client) UpdateCredentials(clientID, clientSecret string) {
	c.credMu.Lock()
	defer c.credMu.Unlock()
	c.clientID = clientID
	c.clientSecret = clientSecret
}

// SyncHistoryRequest represents the request body for /sync/history
type SyncHistoryRequest struct {
	Movies []SyncMovie `json:"movies,omitempty"`
	Shows  []SyncShow  `json:"shows,omitempty"`
}

// SyncMovie represents a movie to add to history
type SyncMovie struct {
	WatchedAt string  `json:"watched_at,omitempty"` // ISO 8601 format
	IDs       SyncIDs `json:"ids"`
}

// SyncShow represents a show with episodes to add to history
type SyncShow struct {
	IDs     SyncIDs      `json:"ids"`
	Seasons []SyncSeason `json:"seasons,omitempty"`
}

// SyncSeason represents a season with episodes
type SyncSeason struct {
	Number   int           `json:"number"`
	Episodes []SyncEpisode `json:"episodes,omitempty"`
}

// SyncEpisode represents an episode to add to history
type SyncEpisode struct {
	Number    int     `json:"number"`
	WatchedAt string  `json:"watched_at,omitempty"` // ISO 8601 format
	IDs       SyncIDs `json:"ids,omitempty"`
}

// SyncIDs holds IDs for sync operations
type SyncIDs struct {
	Trakt int    `json:"trakt,omitempty"`
	IMDB  string `json:"imdb,omitempty"`
	TMDB  int    `json:"tmdb,omitempty"`
	TVDB  int    `json:"tvdb,omitempty"`
}

// SyncHistoryResponse represents the response from /sync/history
type SyncHistoryResponse struct {
	Added struct {
		Movies   int `json:"movies"`
		Episodes int `json:"episodes"`
	} `json:"added"`
	Deleted struct {
		Movies   int `json:"movies"`
		Episodes int `json:"episodes"`
	} `json:"deleted"`
	NotFound struct {
		Movies []SyncMovie `json:"movies"`
		Shows  []SyncShow  `json:"shows"`
	} `json:"not_found"`
}

// AddToHistory adds movies and/or episodes to the user's watch history on Trakt
func (c *Client) AddToHistory(accessToken string, request SyncHistoryRequest) (*SyncHistoryResponse, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, traktAPIBaseURL+"/sync/history", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	c.setTraktHeaders(req, accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("trakt api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("trakt sync history failed: %s - %s", resp.Status, string(respBody))
	}

	var syncResp SyncHistoryResponse
	if err := json.NewDecoder(resp.Body).Decode(&syncResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &syncResp, nil
}

// RemoveFromHistory removes movies and/or episodes from the user's Trakt watch history.
func (c *Client) RemoveFromHistory(accessToken string, request SyncHistoryRequest) (*SyncHistoryResponse, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, traktAPIBaseURL+"/sync/history/remove", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	c.setTraktHeaders(req, accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("trakt api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("trakt remove history failed: %s - %s", resp.Status, string(respBody))
	}

	var syncResp SyncHistoryResponse
	if err := json.NewDecoder(resp.Body).Decode(&syncResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &syncResp, nil
}

// AddMovieToHistory adds a single movie to the user's Trakt watch history
func (c *Client) AddMovieToHistory(accessToken string, tmdbID, tvdbID int, imdbID string, watchedAt string) error {
	request := SyncHistoryRequest{
		Movies: []SyncMovie{
			{
				WatchedAt: watchedAt,
				IDs: SyncIDs{
					TMDB: tmdbID,
					TVDB: tvdbID,
					IMDB: imdbID,
				},
			},
		},
	}

	_, err := c.AddToHistory(accessToken, request)
	return err
}

// AddEpisodeToHistory adds a single episode to the user's Trakt watch history
// using the show's TVDB ID and season/episode numbers
func (c *Client) AddEpisodeToHistory(accessToken string, showTVDBID, season, episode int, watchedAt string, episodeIDs SyncIDs) error {
	return c.AddEpisodeToHistoryForShow(accessToken, SyncIDs{TVDB: showTVDBID}, season, episode, watchedAt, episodeIDs)
}

// AddEpisodeToHistoryForShow adds a single episode to the user's Trakt watch
// history, identifying the show by any combination of Trakt/TVDB/TMDB/IMDB IDs
// so episodes from tvdb-less metadata sources can still be synced.
func (c *Client) AddEpisodeToHistoryForShow(accessToken string, showIDs SyncIDs, season, episode int, watchedAt string, episodeIDs SyncIDs) error {
	request := SyncHistoryRequest{
		Shows: []SyncShow{
			{
				IDs: showIDs,
				Seasons: []SyncSeason{
					{
						Number: season,
						Episodes: []SyncEpisode{
							{
								Number:    episode,
								WatchedAt: watchedAt,
								IDs:       episodeIDs,
							},
						},
					},
				},
			},
		},
	}

	resp, err := c.AddToHistory(accessToken, request)
	if err != nil {
		return err
	}
	if resp != nil && resp.Added.Episodes == 0 {
		return ErrNotFound
	}
	return nil
}

// CollectionItem represents an item from the Trakt collection
type CollectionItem struct {
	CollectedAt time.Time `json:"collected_at"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
	Movie       *Movie    `json:"movie,omitempty"`
	Show        *Show     `json:"show,omitempty"`
}

// FavoriteItem represents an item from the Trakt favorites
type FavoriteItem struct {
	Rank     int       `json:"rank"`
	ListedAt time.Time `json:"listed_at"`
	Type     string    `json:"type"` // "movie" or "show"
	Movie    *Movie    `json:"movie,omitempty"`
	Show     *Show     `json:"show,omitempty"`
}

// UserList represents a custom Trakt list
type UserList struct {
	Name           string    `json:"name"`
	Description    string    `json:"description,omitempty"`
	Privacy        string    `json:"privacy"`
	DisplayNumbers bool      `json:"display_numbers"`
	AllowComments  bool      `json:"allow_comments"`
	SortBy         string    `json:"sort_by"`
	SortHow        string    `json:"sort_how"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	ItemCount      int       `json:"item_count"`
	CommentCount   int       `json:"comment_count"`
	Likes          int       `json:"likes"`
	IDs            struct {
		Trakt int    `json:"trakt"`
		Slug  string `json:"slug"`
	} `json:"ids"`
	User *UserProfile `json:"user,omitempty"`
}

// SmartList represents a Trakt dynamic list (called a Smart List by the API).
// Smart Lists are VIP-enhanced and resolve their items from saved filters.
type SmartList struct {
	Name      string    `json:"name"`
	Privacy   string    `json:"privacy"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Source    string    `json:"source"`
	MediaType string    `json:"media_type"`
	IDs       struct {
		Trakt int    `json:"trakt"`
		Slug  string `json:"slug"`
	} `json:"ids"`
}

const smartListIDPrefix = "smart:"

// EncodeSmartListID returns an opaque list ID that can travel through the
// existing custom-list settings without changing saved personal-list slugs.
func EncodeSmartListID(mediaType, slug string) string {
	return smartListIDPrefix + strings.TrimSpace(mediaType) + ":" + strings.TrimSpace(slug)
}

func decodeSmartListID(listID string) (mediaType, slug string, ok bool) {
	if !strings.HasPrefix(listID, smartListIDPrefix) {
		return "", "", false
	}

	parts := strings.SplitN(strings.TrimPrefix(listID, smartListIDPrefix), ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", false
	}

	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
}

// ListItem represents an item from a Trakt custom list
type ListItem struct {
	Rank     int       `json:"rank"`
	ID       int64     `json:"id"`
	ListedAt time.Time `json:"listed_at"`
	Notes    string    `json:"notes,omitempty"`
	Type     string    `json:"type"` // "movie" or "show"
	Movie    *Movie    `json:"movie,omitempty"`
	Show     *Show     `json:"show,omitempty"`
}

// GetCollection retrieves the user's collection (owned media)
// mediaType can be "movies" or "shows"
func (c *Client) GetCollection(accessToken string, mediaType string) ([]CollectionItem, error) {
	url := fmt.Sprintf("%s/users/me/collection/%s", traktAPIBaseURL, mediaType)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	c.setTraktHeaders(req, accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("trakt api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("trakt collection failed: %s - %s", resp.Status, string(respBody))
	}

	var items []CollectionItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return items, nil
}

// GetAllCollection retrieves the complete collection (movies and shows)
func (c *Client) GetAllCollection(accessToken string) ([]CollectionItem, error) {
	movies, err := c.GetCollection(accessToken, "movies")
	if err != nil {
		return nil, fmt.Errorf("get movie collection: %w", err)
	}

	shows, err := c.GetCollection(accessToken, "shows")
	if err != nil {
		return nil, fmt.Errorf("get show collection: %w", err)
	}

	return append(movies, shows...), nil
}

// GetFavorites retrieves the user's favorites with pagination
// mediaType can be "movies" or "shows"
func (c *Client) GetFavorites(accessToken string, mediaType string, page, limit int) ([]FavoriteItem, int, error) {
	url := fmt.Sprintf("%s/users/me/favorites/%s?page=%d&limit=%d", traktAPIBaseURL, mediaType, page, limit)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}

	c.setTraktHeaders(req, accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("trakt api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, 0, fmt.Errorf("trakt favorites failed: %s - %s", resp.Status, string(respBody))
	}

	totalCount := 0
	if totalHeader := resp.Header.Get("X-Pagination-Item-Count"); totalHeader != "" {
		totalCount, _ = strconv.Atoi(totalHeader)
	}

	var items []FavoriteItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, 0, fmt.Errorf("decode response: %w", err)
	}

	return items, totalCount, nil
}

// GetAllFavorites retrieves all favorites (movies and shows)
func (c *Client) GetAllFavorites(accessToken string) ([]FavoriteItem, error) {
	var allItems []FavoriteItem

	// Get movie favorites
	page := 1
	limit := 100
	for {
		items, totalCount, err := c.GetFavorites(accessToken, "movies", page, limit)
		if err != nil {
			return nil, fmt.Errorf("get movie favorites: %w", err)
		}
		allItems = append(allItems, items...)
		if len(allItems) >= totalCount || len(items) == 0 {
			break
		}
		page++
	}

	// Get show favorites
	page = 1
	movieCount := len(allItems)
	for {
		items, totalCount, err := c.GetFavorites(accessToken, "shows", page, limit)
		if err != nil {
			return nil, fmt.Errorf("get show favorites: %w", err)
		}
		allItems = append(allItems, items...)
		if len(allItems)-movieCount >= totalCount || len(items) == 0 {
			break
		}
		page++
	}

	return allItems, nil
}

// GetUserLists retrieves all personal lists for the authenticated user.
func (c *Client) GetUserLists(accessToken string) ([]UserList, error) {
	const limit = 100
	var allLists []UserList

	for page := 1; ; page++ {
		requestURL := fmt.Sprintf("%s/users/me/lists?page=%d&limit=%d", traktAPIBaseURL, page, limit)

		req, err := http.NewRequest(http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}

		c.setTraktHeaders(req, accessToken)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("trakt api request: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("trakt user lists failed: %s - %s", resp.Status, string(respBody))
		}

		var lists []UserList
		decodeErr := json.NewDecoder(resp.Body).Decode(&lists)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("decode response: %w", decodeErr)
		}

		allLists = append(allLists, lists...)
		pageCount, _ := strconv.Atoi(resp.Header.Get("X-Pagination-Page-Count"))
		if len(lists) == 0 || (pageCount > 0 && page >= pageCount) || (pageCount == 0 && len(lists) < limit) {
			break
		}
	}

	return allLists, nil
}

// GetUserSmartLists retrieves all dynamic Smart Lists for the authenticated user.
func (c *Client) GetUserSmartLists(accessToken string) ([]SmartList, error) {
	requestURL := fmt.Sprintf("%s/users/me/smart-lists", traktAPIBaseURL)

	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	c.setTraktHeaders(req, accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("trakt api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("trakt user smart lists failed: %s - %s", resp.Status, string(respBody))
	}

	var lists []SmartList
	if err := json.NewDecoder(resp.Body).Decode(&lists); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return lists, nil
}

// GetSmartListItems retrieves one page of items resolved by a Smart List.
func (c *Client) GetSmartListItems(accessToken, slug, mediaType string, page, limit int) ([]ListItem, int, error) {
	return c.GetSmartListItemsCtx(context.Background(), accessToken, slug, mediaType, page, limit)
}

// GetSmartListItemsCtx is GetSmartListItems bound to ctx.
func (c *Client) GetSmartListItemsCtx(ctx context.Context, accessToken, slug, mediaType string, page, limit int) ([]ListItem, int, error) {
	itemType := strings.TrimSpace(mediaType)
	if itemType == "" || itemType == "media" {
		itemType = "media"
	}

	requestURL := fmt.Sprintf(
		"%s/smart-lists/%s/items/%s/rank/asc?page=%d&limit=%d",
		traktAPIBaseURL,
		url.PathEscape(slug),
		url.PathEscape(itemType),
		page,
		limit,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}

	c.setTraktHeaders(req, accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("trakt api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, 0, fmt.Errorf("trakt smart list items failed: %s - %s", resp.Status, string(respBody))
	}

	totalCount, _ := strconv.Atoi(resp.Header.Get("X-Pagination-Item-Count"))
	var items []ListItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, 0, fmt.Errorf("decode response: %w", err)
	}

	return items, totalCount, nil
}

// GetListItems retrieves items from a specific user list
func (c *Client) GetListItems(accessToken string, listID string, page, limit int) ([]ListItem, int, error) {
	return c.GetListItemsCtx(context.Background(), accessToken, listID, page, limit)
}

// GetListItemsCtx is GetListItems bound to ctx.
func (c *Client) GetListItemsCtx(ctx context.Context, accessToken string, listID string, page, limit int) ([]ListItem, int, error) {
	url := fmt.Sprintf("%s/users/me/lists/%s/items?page=%d&limit=%d", traktAPIBaseURL, listID, page, limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}

	c.setTraktHeaders(req, accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("trakt api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, 0, fmt.Errorf("trakt list items failed: %s - %s", resp.Status, string(respBody))
	}

	totalCount := 0
	if totalHeader := resp.Header.Get("X-Pagination-Item-Count"); totalHeader != "" {
		totalCount, _ = strconv.Atoi(totalHeader)
	}

	var items []ListItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, 0, fmt.Errorf("decode response: %w", err)
	}

	return items, totalCount, nil
}

// GetAllListItems retrieves all items from a specific user list
func (c *Client) GetAllListItems(accessToken string, listID string) ([]ListItem, error) {
	return c.GetAllListItemsCtx(context.Background(), accessToken, listID)
}

// GetAllListItemsCtx retrieves all list pages, stopping when ctx is cancelled.
func (c *Client) GetAllListItemsCtx(ctx context.Context, accessToken string, listID string) ([]ListItem, error) {
	var allItems []ListItem
	page := 1
	limit := 100
	mediaType, smartListSlug, isSmartList := decodeSmartListID(listID)

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var items []ListItem
		var totalCount int
		var err error
		if isSmartList {
			items, totalCount, err = c.GetSmartListItemsCtx(ctx, accessToken, smartListSlug, mediaType, page, limit)
		} else {
			items, totalCount, err = c.GetListItemsCtx(ctx, accessToken, listID, page, limit)
		}
		if err != nil {
			return nil, err
		}

		allItems = append(allItems, items...)

		if len(items) == 0 || (totalCount > 0 && len(allItems) >= totalCount) || (totalCount == 0 && len(items) < limit) {
			break
		}

		page++
	}

	return allItems, nil
}

// AddToWatchlist adds movies and/or shows to the user's Trakt watchlist
func (c *Client) AddToWatchlist(accessToken string, movies []SyncMovie, shows []SyncShow) error {
	payload := map[string]interface{}{}
	if len(movies) > 0 {
		payload["movies"] = movies
	}
	if len(shows) > 0 {
		payload["shows"] = shows
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, traktAPIBaseURL+"/sync/watchlist", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	c.setTraktHeaders(req, accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("trakt api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("trakt add to watchlist failed: %s - %s", resp.Status, string(respBody))
	}

	return nil
}

// ScrobbleRequest represents the request body for /scrobble/{action}
type ScrobbleRequest struct {
	Movie    *ScrobbleMovie   `json:"movie,omitempty"`
	Show     *ScrobbleShow    `json:"show,omitempty"`
	Episode  *ScrobbleEpisode `json:"episode,omitempty"`
	Progress float64          `json:"progress"`
}

// ScrobbleMovie identifies a movie in a scrobble request
type ScrobbleMovie struct {
	Title string  `json:"title,omitempty"`
	Year  int     `json:"year,omitempty"`
	IDs   SyncIDs `json:"ids"`
}

// ScrobbleShow identifies a show in a scrobble request
type ScrobbleShow struct {
	Title string  `json:"title,omitempty"`
	Year  int     `json:"year,omitempty"`
	IDs   SyncIDs `json:"ids"`
}

// ScrobbleEpisode identifies an episode in a scrobble request
type ScrobbleEpisode struct {
	Season    int     `json:"season"`
	Number    int     `json:"number"`
	NumberAbs int     `json:"number_abs,omitempty"`
	Title     string  `json:"title,omitempty"`
	IDs       SyncIDs `json:"ids,omitempty"`
}

// ScrobbleResponse represents the response from /scrobble/{action}
type ScrobbleResponse struct {
	ID       int64   `json:"id"`
	Action   string  `json:"action"`
	Progress float64 `json:"progress"`
}

// PlaybackItem represents an item from /sync/playback/{type}
type PlaybackItem struct {
	ID       int64     `json:"id"`
	Progress float64   `json:"progress"`
	PausedAt time.Time `json:"paused_at"`
	Type     string    `json:"type"`
	Movie    *Movie    `json:"movie,omitempty"`
	Episode  *Episode  `json:"episode,omitempty"`
	Show     *Show     `json:"show,omitempty"`
}

// scrobble sends a scrobble action (start/pause/stop) to Trakt
func (c *Client) scrobble(accessToken, action string, req ScrobbleRequest) (*ScrobbleResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, traktAPIBaseURL+"/scrobble/"+action, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	c.setTraktHeaders(httpReq, accessToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("trakt api request: %w", err)
	}
	defer resp.Body.Close()

	// 201 = scrobble successful, 409 = already scrobbled (treat as success)
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("trakt scrobble/%s failed: %s - %s", action, resp.Status, string(respBody))
	}

	var scrobbleResp ScrobbleResponse
	if err := json.NewDecoder(resp.Body).Decode(&scrobbleResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &scrobbleResp, nil
}

// ScrobbleStart reports that the user has started watching
func (c *Client) ScrobbleStart(accessToken string, req ScrobbleRequest) (*ScrobbleResponse, error) {
	return c.scrobble(accessToken, "start", req)
}

// ScrobblePause reports that the user has paused watching
func (c *Client) ScrobblePause(accessToken string, req ScrobbleRequest) (*ScrobbleResponse, error) {
	return c.scrobble(accessToken, "pause", req)
}

// ScrobbleStop reports that the user has stopped watching
func (c *Client) ScrobbleStop(accessToken string, req ScrobbleRequest) (*ScrobbleResponse, error) {
	return c.scrobble(accessToken, "stop", req)
}

// GetPlaybackProgress retrieves playback progress items from Trakt
// mediaType should be "movies" or "episodes"
func (c *Client) GetPlaybackProgress(accessToken, mediaType string) ([]PlaybackItem, error) {
	req, err := http.NewRequest(http.MethodGet, traktAPIBaseURL+"/sync/playback/"+mediaType, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	c.setTraktHeaders(req, accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("trakt api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("trakt playback progress failed: %s - %s", resp.Status, string(respBody))
	}

	var items []PlaybackItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return items, nil
}

// RemovePlaybackItem removes a specific playback progress item from Trakt
func (c *Client) RemovePlaybackItem(accessToken string, id int64) error {
	url := fmt.Sprintf("%s/sync/playback/%d", traktAPIBaseURL, id)

	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	c.setTraktHeaders(req, accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("trakt api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("trakt remove playback failed: %s - %s", resp.Status, string(respBody))
	}

	return nil
}

// RemoveFromWatchlist removes movies and/or shows from the user's Trakt watchlist
func (c *Client) RemoveFromWatchlist(accessToken string, movies []SyncMovie, shows []SyncShow) error {
	payload := map[string]interface{}{}
	if len(movies) > 0 {
		payload["movies"] = movies
	}
	if len(shows) > 0 {
		payload["shows"] = shows
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, traktAPIBaseURL+"/sync/watchlist/remove", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	c.setTraktHeaders(req, accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("trakt api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("trakt remove from watchlist failed: %s - %s", resp.Status, string(respBody))
	}

	return nil
}
