package plex

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	plexTVBaseURL       = "https://plex.tv/api/v2"
	plexDiscoverBaseURL = "https://discover.provider.plex.tv"
	plexMetadataBaseURL = "https://metadata.provider.plex.tv"
	plexAuthURL         = "https://app.plex.tv/auth"
)

// Client handles Plex API interactions for OAuth and watchlist fetching
type Client struct {
	httpClient *http.Client
	clientID   string
}

// PINResponse represents the response from creating/checking a PIN
type PINResponse struct {
	ID         int       `json:"id"`
	Code       string    `json:"code"`
	AuthToken  string    `json:"authToken,omitempty"`
	ExpiresAt  time.Time `json:"expiresAt,omitempty"`
	Trusted    bool      `json:"trusted,omitempty"`
	ClientID   string    `json:"clientIdentifier,omitempty"`
	NewAccount bool      `json:"newRegistration,omitempty"`
}

// WatchlistItem represents an item from the Plex watchlist
type WatchlistItem struct {
	RatingKey      string  `json:"ratingKey"`
	Key            string  `json:"key"`
	GUID           string  `json:"guid"`
	Type           string  `json:"type"` // "movie" or "show"
	Title          string  `json:"title"`
	Year           int     `json:"year"`
	Thumb          string  `json:"thumb"`
	Art            string  `json:"art"`
	AudienceRating float64 `json:"audienceRating"`
	AddedAt        int64   `json:"addedAt"`
}

// WatchlistResponse represents the Plex watchlist API response
type WatchlistResponse struct {
	MediaContainer struct {
		Size     int             `json:"size"`
		Metadata []WatchlistItem `json:"Metadata"`
	} `json:"MediaContainer"`
}

// UserInfo represents basic Plex user information
type UserInfo struct {
	ID       int    `json:"id"`
	UUID     string `json:"uuid"`
	Username string `json:"username"`
	Title    string `json:"title"`
	Email    string `json:"email"`
	Thumb    string `json:"thumb"`
}

// NewClient creates a new Plex API client
func NewClient(clientID string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		clientID:   clientID,
	}
}

// setPlexHeaders adds required Plex headers to a request
func (c *Client) setPlexHeaders(req *http.Request) {
	req.Header.Set("X-Plex-Client-Identifier", c.clientID)
	req.Header.Set("X-Plex-Product", "strmr")
	req.Header.Set("X-Plex-Version", "1.0.0")
	req.Header.Set("X-Plex-Platform", "Web")
	req.Header.Set("Accept", "application/json")
}

// CreatePIN creates a new PIN for OAuth authentication
func (c *Client) CreatePIN() (*PINResponse, error) {
	req, err := http.NewRequest(http.MethodPost, plexTVBaseURL+"/pins?strong=true", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	c.setPlexHeaders(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("plex api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("plex pin creation failed: %s - %s", resp.Status, string(body))
	}

	var pin PINResponse
	if err := json.NewDecoder(resp.Body).Decode(&pin); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &pin, nil
}

// CheckPIN checks the status of a PIN and returns the auth token if authenticated
func (c *Client) CheckPIN(pinID int) (*PINResponse, error) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/pins/%d", plexTVBaseURL, pinID), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	c.setPlexHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("plex api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("plex pin check failed: %s - %s", resp.Status, string(body))
	}

	var pin PINResponse
	if err := json.NewDecoder(resp.Body).Decode(&pin); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &pin, nil
}

// GetAuthURL returns the Plex authentication URL for the given PIN code
func (c *Client) GetAuthURL(pinCode string) string {
	params := url.Values{}
	params.Set("clientID", c.clientID)
	params.Set("code", pinCode)
	params.Set("context[device][product]", "strmr")

	return fmt.Sprintf("%s#?%s", plexAuthURL, params.Encode())
}

// GetUserInfo retrieves information about the authenticated user
func (c *Client) GetUserInfo(authToken string) (*UserInfo, error) {
	req, err := http.NewRequest(http.MethodGet, plexTVBaseURL+"/user", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	c.setPlexHeaders(req)
	req.Header.Set("X-Plex-Token", authToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("plex api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("plex user info failed: %s - %s", resp.Status, string(body))
	}

	var user UserInfo
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &user, nil
}

// WatchlistPaginatedResponse represents the Plex watchlist API response with pagination info
type WatchlistPaginatedResponse struct {
	MediaContainer struct {
		Size             int             `json:"size"`
		TotalSize        int             `json:"totalSize"`
		Offset           int             `json:"offset"`
		Metadata         []WatchlistItem `json:"Metadata"`
	} `json:"MediaContainer"`
}

// GetWatchlist retrieves the user's Plex watchlist (all pages)
func (c *Client) GetWatchlist(authToken string) ([]WatchlistItem, error) {
	return c.GetWatchlistWithProgress(authToken, nil)
}

// WatchlistItemWithDetails contains a watchlist item with its external IDs
type WatchlistItemWithDetails struct {
	WatchlistItem
	ExternalIDs map[string]string
}

// GetWatchlistWithProgress retrieves watchlist with progress reporting and parallel detail fetching
func (c *Client) GetWatchlistWithProgress(authToken string, progress ProgressCallback) ([]WatchlistItem, error) {
	if progress != nil {
		progress("fetching", 0, 0)
	}

	var allItems []WatchlistItem
	offset := 0
	pageSize := 50 // Request 50 items per page

	for {
		items, totalSize, err := c.getWatchlistPage(authToken, offset, pageSize)
		if err != nil {
			return nil, err
		}

		allItems = append(allItems, items...)

		if progress != nil {
			progress("fetching", len(allItems), totalSize)
		}

		// Check if we've fetched all items
		if len(allItems) >= totalSize || len(items) == 0 {
			break
		}

		offset += len(items)
	}

	return allItems, nil
}

// GetWatchlistDetailsWithProgress fetches external IDs for watchlist items in parallel
func (c *Client) GetWatchlistDetailsWithProgress(authToken string, items []WatchlistItem, progress ProgressCallback) []map[string]string {
	if len(items) == 0 {
		return nil
	}

	const numWorkers = 10
	results := make([]map[string]string, len(items))

	type job struct {
		index int
		item  WatchlistItem
	}

	jobs := make(chan job, len(items))
	done := make(chan struct{}, len(items))

	// Start workers
	for w := 0; w < numWorkers; w++ {
		go func() {
			for j := range jobs {
				externalIDs, _ := c.GetItemDetails(authToken, j.item.RatingKey)
				if externalIDs == nil {
					externalIDs = ParseGUID(j.item.GUID)
				}
				results[j.index] = externalIDs
				done <- struct{}{}
			}
		}()
	}

	// Send jobs
	for i, item := range items {
		jobs <- job{index: i, item: item}
	}
	close(jobs)

	// Wait for completion with progress
	completed := 0
	for range items {
		<-done
		completed++
		if progress != nil && completed%5 == 0 {
			progress("details", completed, len(items))
		}
	}

	if progress != nil {
		progress("details", len(items), len(items))
	}

	return results
}

// getWatchlistPage retrieves a single page of the watchlist
func (c *Client) getWatchlistPage(authToken string, offset, limit int) ([]WatchlistItem, int, error) {
	// Use the new discover API endpoint (metadata.provider.plex.tv was deprecated)
	watchlistURL := fmt.Sprintf("%s/library/sections/watchlist/all?X-Plex-Container-Start=%d&X-Plex-Container-Size=%d",
		plexDiscoverBaseURL, offset, limit)

	req, err := http.NewRequest(http.MethodGet, watchlistURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}

	c.setPlexHeaders(req)
	req.Header.Set("X-Plex-Token", authToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("plex api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, 0, fmt.Errorf("plex watchlist failed: %s - %s", resp.Status, string(body))
	}

	var watchlistResp WatchlistPaginatedResponse
	if err := json.NewDecoder(resp.Body).Decode(&watchlistResp); err != nil {
		return nil, 0, fmt.Errorf("decode response: %w", err)
	}

	return watchlistResp.MediaContainer.Metadata, watchlistResp.MediaContainer.TotalSize, nil
}

// ParseGUID extracts external IDs from a Plex GUID string
// Example GUID: "plex://movie/5d7768532e80df001ebe18e3" or contains references like "imdb://tt1234567"
func ParseGUID(guid string) map[string]string {
	ids := make(map[string]string)

	// Common patterns for GUIDs
	patterns := map[string]*regexp.Regexp{
		"imdb":  regexp.MustCompile(`imdb://?(tt\d+)`),
		"tmdb":  regexp.MustCompile(`tmdb://(\d+)`),
		"tvdb":  regexp.MustCompile(`tvdb://(\d+)`),
		"plex":  regexp.MustCompile(`plex://(?:movie|show)/([a-f0-9]+)`),
	}

	for service, pattern := range patterns {
		if matches := pattern.FindStringSubmatch(guid); len(matches) > 1 {
			ids[service] = matches[1]
		}
	}

	return ids
}

// GetItemDetails retrieves detailed information about a watchlist item including external IDs
func (c *Client) GetItemDetails(authToken string, ratingKey string) (map[string]string, error) {
	// Fetch item details to get GUIDs (use discover API)
	detailsURL := fmt.Sprintf("%s/library/metadata/%s", plexDiscoverBaseURL, ratingKey)

	req, err := http.NewRequest(http.MethodGet, detailsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	c.setPlexHeaders(req)
	req.Header.Set("X-Plex-Token", authToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("plex api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil // Return empty on error, non-critical
	}

	var detailsResp struct {
		MediaContainer struct {
			Metadata []struct {
				GUID  string `json:"guid"`
				Guids []struct {
					ID string `json:"id"`
				} `json:"Guid"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&detailsResp); err != nil {
		return nil, nil
	}

	ids := make(map[string]string)
	if len(detailsResp.MediaContainer.Metadata) > 0 {
		item := detailsResp.MediaContainer.Metadata[0]

		// Parse main GUID
		for k, v := range ParseGUID(item.GUID) {
			ids[k] = v
		}

		// Parse additional GUIDs array
		for _, g := range item.Guids {
			for k, v := range ParseGUID(g.ID) {
				ids[k] = v
			}
		}
	}

	return ids, nil
}

// AddToWatchlist adds an item to the user's Plex watchlist
func (c *Client) AddToWatchlist(authToken string, ratingKey string) error {
	actionURL := fmt.Sprintf("%s/actions/addToWatchlist?ratingKey=%s", plexDiscoverBaseURL, ratingKey)

	req, err := http.NewRequest(http.MethodPut, actionURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	c.setPlexHeaders(req)
	req.Header.Set("X-Plex-Token", authToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("plex api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to add to watchlist: status %d, body: %s", resp.StatusCode, string(body))
	}

	return nil
}

// RemoveFromWatchlist removes an item from the user's Plex watchlist
func (c *Client) RemoveFromWatchlist(authToken string, ratingKey string) error {
	actionURL := fmt.Sprintf("%s/actions/removeFromWatchlist?ratingKey=%s", plexDiscoverBaseURL, ratingKey)

	req, err := http.NewRequest(http.MethodPut, actionURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	c.setPlexHeaders(req)
	req.Header.Set("X-Plex-Token", authToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("plex api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to remove from watchlist: status %d, body: %s", resp.StatusCode, string(body))
	}

	return nil
}

// NormalizeMediaType converts Plex media type to strmr media type
func NormalizeMediaType(plexType string) string {
	switch strings.ToLower(plexType) {
	case "movie":
		return "movie"
	case "show":
		return "series"
	default:
		return plexType
	}
}

// GetPosterURL constructs a full poster URL from a Plex thumb path
func GetPosterURL(thumb string, authToken string) string {
	if thumb == "" {
		return ""
	}
	// Plex thumb paths are relative, need to construct full URL (use discover API)
	if strings.HasPrefix(thumb, "/") {
		return fmt.Sprintf("%s%s?X-Plex-Token=%s", plexDiscoverBaseURL, thumb, authToken)
	}
	return thumb
}

// ClientID returns the client identifier
func (c *Client) ClientID() string {
	return c.clientID
}

// GenerateClientID generates a new unique client identifier
func GenerateClientID() string {
	return "strmr-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// PlexHomeUser represents a user in a Plex Home
type PlexHomeUser struct {
	ID       int    `json:"id"`
	Title    string `json:"title"`
	Username string `json:"username,omitempty"`
	Thumb    string `json:"thumb,omitempty"`
	Admin    bool   `json:"admin,omitempty"`
	Guest    bool   `json:"guest,omitempty"`
}

// GetHomeUsers retrieves the list of users from all owned Plex servers.
// These are server-local account IDs which are used for filtering watch history.
func (c *Client) GetHomeUsers(authToken string) ([]PlexHomeUser, error) {
	servers, err := c.GetOwnedServers(authToken)
	if err != nil {
		log.Printf("[plex] Failed to get owned servers: %v", err)
		return nil, err
	}

	if len(servers) == 0 {
		log.Printf("[plex] No online owned servers found for user listing")
		return nil, fmt.Errorf("no online owned Plex servers found")
	}

	log.Printf("[plex] Fetching users from %d server(s)", len(servers))

	// Use a map to dedupe users across servers (by ID)
	userMap := make(map[int]PlexHomeUser)

	for _, server := range servers {
		users, err := c.GetServerUsers(server)
		if err != nil {
			log.Printf("[plex] Failed to get users from server %q: %v", server.Name, err)
			continue // Try other servers
		}
		log.Printf("[plex] Got %d user(s) from server %q", len(users), server.Name)
		for _, user := range users {
			if _, exists := userMap[user.ID]; !exists {
				userMap[user.ID] = user
			}
		}
	}

	// Convert map to slice
	users := make([]PlexHomeUser, 0, len(userMap))
	for _, user := range userMap {
		users = append(users, user)
	}

	log.Printf("[plex] Home users retrieved: %d total from %d server(s)", len(users), len(servers))

	return users, nil
}

// GetServerUsers retrieves the list of users/accounts from a specific Plex server.
// These IDs match the accountID field in watch history items.
func (c *Client) GetServerUsers(server PlexResource) ([]PlexHomeUser, error) {
	// Find the best connection to use
	var serverURL string
	for _, conn := range server.Connections {
		if !conn.Relay && conn.Protocol == "https" {
			serverURL = conn.URI
			break
		}
	}
	if serverURL == "" {
		for _, conn := range server.Connections {
			if !conn.Relay {
				serverURL = conn.URI
				break
			}
		}
	}
	if serverURL == "" && len(server.Connections) > 0 {
		serverURL = server.Connections[0].URI
	}
	if serverURL == "" {
		return nil, fmt.Errorf("no available connection for server %s", server.Name)
	}

	accountsURL := fmt.Sprintf("%s/accounts?X-Plex-Token=%s", serverURL, server.AccessToken)

	req, err := http.NewRequest(http.MethodGet, accountsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	c.setPlexHeaders(req)
	req.Header.Set("X-Plex-Token", server.AccessToken)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("plex server request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("plex accounts failed: %s - %s", resp.Status, string(body))
	}

	var accountsResp struct {
		MediaContainer struct {
			Account []struct {
				ID   int    `json:"id"`
				Name string `json:"name"`
			} `json:"Account"`
		} `json:"MediaContainer"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&accountsResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	users := make([]PlexHomeUser, 0, len(accountsResp.MediaContainer.Account))
	skipped := 0
	for _, acc := range accountsResp.MediaContainer.Account {
		// Skip system accounts (ID 0) and accounts with no name
		// Note: ID 1 is typically the admin/owner account - don't skip it
		if acc.ID <= 0 || acc.Name == "" {
			skipped++
			continue
		}
		users = append(users, PlexHomeUser{
			ID:    acc.ID,
			Title: acc.Name,
		})
	}

	if skipped > 0 {
		log.Printf("[plex] Server %q: %d total accounts, %d users returned, %d system accounts skipped",
			server.Name, len(accountsResp.MediaContainer.Account), len(users), skipped)
	}

	return users, nil
}

// PlexResource represents a Plex server or client resource
type PlexResource struct {
	Name             string               `json:"name"`
	Product          string               `json:"product"`
	ProductVersion   string               `json:"productVersion"`
	Platform         string               `json:"platform"`
	PlatformVersion  string               `json:"platformVersion"`
	Device           string               `json:"device"`
	ClientIdentifier string               `json:"clientIdentifier"`
	CreatedAt        time.Time            `json:"createdAt"`
	LastSeenAt       time.Time            `json:"lastSeenAt"`
	Provides         string               `json:"provides"`
	Owned            bool                 `json:"owned"`
	AccessToken      string               `json:"accessToken"`
	Connections      []PlexConnection     `json:"connections"`
	Presence         bool                 `json:"presence"`
}

// PlexConnection represents a connection endpoint for a Plex resource
type PlexConnection struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	URI      string `json:"uri"`
	Local    bool   `json:"local"`
	Relay    bool   `json:"relay"`
}

// WatchHistoryItem represents an item from Plex watch history
type WatchHistoryItem struct {
	RatingKey            string `json:"ratingKey"`
	Key                  string `json:"key"`
	ParentRatingKey      string `json:"parentRatingKey,omitempty"`
	GrandparentRatingKey string `json:"grandparentRatingKey,omitempty"`
	Title                string `json:"title"`
	GrandparentTitle     string `json:"grandparentTitle,omitempty"`
	ParentTitle          string `json:"parentTitle,omitempty"`
	Type                 string `json:"type"` // "movie", "episode"
	Thumb                string `json:"thumb,omitempty"`
	GrandparentThumb     string `json:"grandparentThumb,omitempty"`
	ViewedAt             int64  `json:"viewedAt"`
	AccountID            int    `json:"accountID"`
	LibrarySectionID     string `json:"librarySectionID,omitempty"` // String - Plex returns mixed types
	Index                int    `json:"index,omitempty"`            // Episode number
	ParentIndex          int    `json:"parentIndex,omitempty"`      // Season number
	Year                 int    `json:"year,omitempty"`
	GUID                 string `json:"guid,omitempty"`
	// External IDs (populated after fetching details)
	ExternalIDs map[string]string `json:"externalIds,omitempty"`
	// Server info
	ServerName string `json:"serverName,omitempty"`
}

// GetResources retrieves the user's Plex resources (servers, clients)
func (c *Client) GetResources(authToken string) ([]PlexResource, error) {
	resourcesURL := fmt.Sprintf("%s/resources?includeHttps=1&includeRelay=1", plexTVBaseURL)

	req, err := http.NewRequest(http.MethodGet, resourcesURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	c.setPlexHeaders(req)
	req.Header.Set("X-Plex-Token", authToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("plex api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("plex resources failed: %s - %s", resp.Status, string(body))
	}

	var resources []PlexResource
	if err := json.NewDecoder(resp.Body).Decode(&resources); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return resources, nil
}

// GetOwnedServers returns only owned Plex Media Server resources that are online
func (c *Client) GetOwnedServers(authToken string) ([]PlexResource, error) {
	resources, err := c.GetResources(authToken)
	if err != nil {
		return nil, err
	}

	var servers []PlexResource
	for _, r := range resources {
		// Filter for owned servers that provide "server" capability
		if r.Owned && strings.Contains(r.Provides, "server") && r.Presence {
			servers = append(servers, r)
		}
	}
	return servers, nil
}

// GetServerWatchHistory fetches watch history from a specific Plex server
// If accountID > 0, filters history to only that Plex user account
func (c *Client) GetServerWatchHistory(server PlexResource, limit int, accountID int) ([]WatchHistoryItem, error) {
	// Find the best connection to use (prefer direct over relay)
	var serverURL string
	for _, conn := range server.Connections {
		if !conn.Relay && conn.Protocol == "https" {
			serverURL = conn.URI
			break
		}
	}
	// Fallback to any available connection
	if serverURL == "" {
		for _, conn := range server.Connections {
			if !conn.Relay {
				serverURL = conn.URI
				break
			}
		}
	}
	// Last resort: use relay
	if serverURL == "" && len(server.Connections) > 0 {
		serverURL = server.Connections[0].URI
	}

	if serverURL == "" {
		return nil, fmt.Errorf("no available connection for server %s", server.Name)
	}

	// Build history URL with pagination and optional account filter
	historyURL := fmt.Sprintf("%s/status/sessions/history/all", serverURL)
	params := url.Values{}
	params.Set("X-Plex-Token", server.AccessToken)
	if limit > 0 {
		params.Set("X-Plex-Container-Size", strconv.Itoa(limit))
	}
	if accountID > 0 {
		params.Set("accountID", strconv.Itoa(accountID))
	}

	fullURL := historyURL + "?" + params.Encode()

	req, err := http.NewRequest(http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	c.setPlexHeaders(req)
	req.Header.Set("X-Plex-Token", server.AccessToken)

	// Use a client with longer timeout for server connections
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("plex server request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("plex history failed: %s - %s", resp.Status, string(body))
	}

	var historyResp struct {
		MediaContainer struct {
			Size     int                `json:"size"`
			Metadata []WatchHistoryItem `json:"Metadata"`
		} `json:"MediaContainer"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&historyResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	// Add server name to each item
	for i := range historyResp.MediaContainer.Metadata {
		historyResp.MediaContainer.Metadata[i].ServerName = server.Name
	}

	return historyResp.MediaContainer.Metadata, nil
}

// GetServerItemDetails fetches detailed metadata including GUIDs from a Plex server
func (c *Client) GetServerItemDetails(server PlexResource, ratingKey string) (*WatchHistoryItem, error) {
	// Find connection URL
	var serverURL string
	for _, conn := range server.Connections {
		if !conn.Relay && conn.Protocol == "https" {
			serverURL = conn.URI
			break
		}
	}
	if serverURL == "" {
		for _, conn := range server.Connections {
			if !conn.Relay {
				serverURL = conn.URI
				break
			}
		}
	}
	if serverURL == "" && len(server.Connections) > 0 {
		serverURL = server.Connections[0].URI
	}
	if serverURL == "" {
		return nil, fmt.Errorf("no available connection for server %s", server.Name)
	}

	detailsURL := fmt.Sprintf("%s/library/metadata/%s?X-Plex-Token=%s", serverURL, ratingKey, server.AccessToken)

	req, err := http.NewRequest(http.MethodGet, detailsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	c.setPlexHeaders(req)
	req.Header.Set("X-Plex-Token", server.AccessToken)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("plex server request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil // Non-critical, return nil
	}

	var detailsResp struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey        string `json:"ratingKey"`
				Title            string `json:"title"`
				Type             string `json:"type"`
				Year             int    `json:"year"`
				GUID             string `json:"guid"`
				GrandparentTitle string `json:"grandparentTitle,omitempty"`
				ParentTitle      string `json:"parentTitle,omitempty"`
				GrandparentRatingKey string `json:"grandparentRatingKey,omitempty"`
				Index            int    `json:"index,omitempty"`
				ParentIndex      int    `json:"parentIndex,omitempty"`
				Thumb            string `json:"thumb,omitempty"`
				Guids            []struct {
					ID string `json:"id"`
				} `json:"Guid"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&detailsResp); err != nil {
		return nil, nil
	}

	if len(detailsResp.MediaContainer.Metadata) == 0 {
		return nil, nil
	}

	item := detailsResp.MediaContainer.Metadata[0]
	result := &WatchHistoryItem{
		RatingKey:        item.RatingKey,
		Title:            item.Title,
		Type:             item.Type,
		Year:             item.Year,
		GUID:             item.GUID,
		GrandparentTitle: item.GrandparentTitle,
		ParentTitle:      item.ParentTitle,
		GrandparentRatingKey: item.GrandparentRatingKey,
		Index:            item.Index,
		ParentIndex:      item.ParentIndex,
		Thumb:            item.Thumb,
		ExternalIDs:      make(map[string]string),
	}

	// Parse main GUID
	for k, v := range ParseGUID(item.GUID) {
		result.ExternalIDs[k] = v
	}

	// Parse additional GUIDs array
	for _, g := range item.Guids {
		for k, v := range ParseGUID(g.ID) {
			result.ExternalIDs[k] = v
		}
	}

	return result, nil
}

// ProgressCallback is called to report progress during long operations
type ProgressCallback func(stage string, current, total int)

// GetAllWatchHistory fetches watch history from all owned servers
// If accountID > 0, filters history to only that Plex user account
func (c *Client) GetAllWatchHistory(authToken string, limit int, accountID int) ([]WatchHistoryItem, error) {
	return c.GetAllWatchHistoryWithProgress(authToken, limit, accountID, nil)
}

// GetAllWatchHistoryWithProgress fetches watch history with progress reporting
func (c *Client) GetAllWatchHistoryWithProgress(authToken string, limit int, accountID int, progress ProgressCallback) ([]WatchHistoryItem, error) {
	if progress != nil {
		progress("servers", 0, 0)
	}

	servers, err := c.GetOwnedServers(authToken)
	if err != nil {
		return nil, err
	}

	if len(servers) == 0 {
		return nil, fmt.Errorf("no online owned Plex servers found")
	}

	var allHistory []WatchHistoryItem
	var lastErr error

	for serverIdx, server := range servers {
		if progress != nil {
			progress("fetching", serverIdx+1, len(servers))
		}

		history, err := c.GetServerWatchHistory(server, limit, accountID)
		if err != nil {
			lastErr = err
			continue // Try other servers
		}

		// Fetch details in parallel with worker pool
		c.fetchDetailsParallel(server, history, progress)

		allHistory = append(allHistory, history...)
	}

	// If no history but we had errors, return the last error
	if len(allHistory) == 0 && lastErr != nil {
		return nil, lastErr
	}

	return allHistory, nil
}

// detailsJob represents a job to fetch details for a history item
type detailsJob struct {
	index int
	item  *WatchHistoryItem
}

// fetchDetailsParallel fetches item details concurrently using a worker pool
func (c *Client) fetchDetailsParallel(server PlexResource, history []WatchHistoryItem, progress ProgressCallback) {
	if len(history) == 0 {
		return
	}

	const numWorkers = 10 // Concurrent requests to Plex server

	jobs := make(chan detailsJob, len(history))
	done := make(chan struct{}, len(history))

	// Start workers
	for w := 0; w < numWorkers; w++ {
		go func() {
			for job := range jobs {
				details, err := c.GetServerItemDetails(server, job.item.RatingKey)
				if err == nil && details != nil {
					job.item.ExternalIDs = details.ExternalIDs
					job.item.GUID = details.GUID
					job.item.Year = details.Year
					// For episodes, also fetch show details
					if job.item.Type == "episode" && job.item.GrandparentRatingKey != "" {
						showDetails, err := c.GetServerItemDetails(server, job.item.GrandparentRatingKey)
						if err == nil && showDetails != nil {
							job.item.ExternalIDs = showDetails.ExternalIDs
						}
					}
				}
				done <- struct{}{}
			}
		}()
	}

	// Send jobs
	for i := range history {
		jobs <- detailsJob{index: i, item: &history[i]}
	}
	close(jobs)

	// Wait for completion with progress updates
	completed := 0
	for range history {
		<-done
		completed++
		if progress != nil && completed%5 == 0 { // Update every 5 items to reduce overhead
			progress("details", completed, len(history))
		}
	}

	// Final progress update
	if progress != nil {
		progress("details", len(history), len(history))
	}
}
