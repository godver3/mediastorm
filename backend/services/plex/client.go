package plex

import (
	"encoding/json"
	"fmt"
	"io"
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
	var allItems []WatchlistItem
	offset := 0
	pageSize := 50 // Request 50 items per page

	for {
		items, totalSize, err := c.getWatchlistPage(authToken, offset, pageSize)
		if err != nil {
			return nil, err
		}

		allItems = append(allItems, items...)

		// Check if we've fetched all items
		if len(allItems) >= totalSize || len(items) == 0 {
			break
		}

		offset += len(items)
	}

	return allItems, nil
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
