package trakt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const (
	traktAPIBaseURL = "https://api.trakt.tv"
	traktAPIVersion = "2"
)

// Client handles Trakt API interactions for OAuth and data fetching
type Client struct {
	httpClient   *http.Client
	clientID     string
	clientSecret string
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
	Season int    `json:"season"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	IDs    IDs    `json:"ids"`
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
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		clientID:     clientID,
		clientSecret: clientSecret,
	}
}

// setTraktHeaders adds required Trakt API headers to a request
func (c *Client) setTraktHeaders(req *http.Request, accessToken string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("trakt-api-version", traktAPIVersion)
	req.Header.Set("trakt-api-key", c.clientID)
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
}

// GetDeviceCode initiates the device code OAuth flow
func (c *Client) GetDeviceCode() (*DeviceCodeResponse, error) {
	payload := map[string]string{
		"client_id": c.clientID,
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
	payload := map[string]string{
		"code":          deviceCode,
		"client_id":     c.clientID,
		"client_secret": c.clientSecret,
	}
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
	payload := map[string]string{
		"refresh_token": refreshToken,
		"client_id":     c.clientID,
		"client_secret": c.clientSecret,
		"redirect_uri":  "urn:ietf:wg:oauth:2.0:oob",
		"grant_type":    "refresh_token",
	}
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
	url := fmt.Sprintf("%s/users/me/watchlist?page=%d&limit=%d", traktAPIBaseURL, page, limit)

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
	var allItems []WatchlistItem
	page := 1
	limit := 100 // Max items per page

	for {
		items, totalCount, err := c.GetWatchlist(accessToken, page, limit)
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
	url := traktAPIBaseURL + "/users/me/history"
	if historyType != "" {
		url += "/" + historyType
	}
	url += fmt.Sprintf("?page=%d&limit=%d", page, limit)

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

	return items, totalCount, nil
}

// GetAllWatchHistory retrieves the complete watch history (all pages)
func (c *Client) GetAllWatchHistory(accessToken string) ([]HistoryItem, error) {
	var allItems []HistoryItem
	page := 1
	limit := 100 // Max items per page

	for {
		items, totalCount, err := c.GetWatchHistory(accessToken, page, limit, "")
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

// GetWatchHistorySince retrieves watch history since the given time (all pages).
// If since is zero, fetches all history (same as GetAllWatchHistory).
func (c *Client) GetWatchHistorySince(accessToken string, since time.Time) ([]HistoryItem, error) {
	var allItems []HistoryItem
	page := 1
	limit := 100

	for {
		url := traktAPIBaseURL + "/users/me/history"
		url += fmt.Sprintf("?page=%d&limit=%d", page, limit)
		if !since.IsZero() {
			url += "&start_at=" + since.UTC().Format(time.RFC3339)
		}

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
			return nil, fmt.Errorf("trakt history failed: %s - %s", resp.Status, string(respBody))
		}

		totalCount := 0
		if totalHeader := resp.Header.Get("X-Pagination-Item-Count"); totalHeader != "" {
			totalCount, _ = strconv.Atoi(totalHeader)
		}

		var items []HistoryItem
		if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}

		allItems = append(allItems, items...)

		if len(allItems) >= totalCount || len(items) == 0 {
			break
		}

		page++
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

// NormalizeMediaType converts Trakt media type to strmr media type
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

// HasCredentials checks if the client has valid credentials configured
func (c *Client) HasCredentials() bool {
	return c.clientID != "" && c.clientSecret != ""
}

// UpdateCredentials updates the client credentials
func (c *Client) UpdateCredentials(clientID, clientSecret string) {
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
	Number    int    `json:"number"`
	WatchedAt string `json:"watched_at,omitempty"` // ISO 8601 format
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
func (c *Client) AddEpisodeToHistory(accessToken string, showTVDBID, season, episode int, watchedAt string) error {
	request := SyncHistoryRequest{
		Shows: []SyncShow{
			{
				IDs: SyncIDs{
					TVDB: showTVDBID,
				},
				Seasons: []SyncSeason{
					{
						Number: season,
						Episodes: []SyncEpisode{
							{
								Number:    episode,
								WatchedAt: watchedAt,
							},
						},
					},
				},
			},
		},
	}

	_, err := c.AddToHistory(accessToken, request)
	return err
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

// GetUserLists retrieves all custom lists for the authenticated user
func (c *Client) GetUserLists(accessToken string) ([]UserList, error) {
	url := fmt.Sprintf("%s/users/me/lists", traktAPIBaseURL)

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
		return nil, fmt.Errorf("trakt user lists failed: %s - %s", resp.Status, string(respBody))
	}

	var lists []UserList
	if err := json.NewDecoder(resp.Body).Decode(&lists); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return lists, nil
}

// GetListItems retrieves items from a specific user list
func (c *Client) GetListItems(accessToken string, listID string, page, limit int) ([]ListItem, int, error) {
	url := fmt.Sprintf("%s/users/me/lists/%s/items?page=%d&limit=%d", traktAPIBaseURL, listID, page, limit)

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
	var allItems []ListItem
	page := 1
	limit := 100

	for {
		items, totalCount, err := c.GetListItems(accessToken, listID, page, limit)
		if err != nil {
			return nil, err
		}

		allItems = append(allItems, items...)

		if len(allItems) >= totalCount || len(items) == 0 {
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
