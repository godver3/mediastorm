package mdblist

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const baseURL = "https://api.mdblist.com"

// ScrobbleClient is the HTTP client for MDBList scrobble API.
type ScrobbleClient struct {
	mu         sync.RWMutex
	apiKey     string
	httpClient *http.Client
}

// NewScrobbleClient creates a new MDBList scrobble client.
func NewScrobbleClient(apiKey string) *ScrobbleClient {
	return &ScrobbleClient{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// UpdateAPIKey updates the API key at runtime (e.g., when settings change).
func (c *ScrobbleClient) UpdateAPIKey(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apiKey = key
}

func (c *ScrobbleClient) getAPIKey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.apiKey
}

// ScrobbleMoviePayload identifies a movie for scrobbling.
type ScrobbleMoviePayload struct {
	IDs ScrobbleIDs `json:"ids"`
}

// ScrobbleShowPayload identifies a show for scrobbling.
// MDBList expects season/episode nested inside the show object.
type ScrobbleShowPayload struct {
	IDs    ScrobbleIDs          `json:"ids"`
	Season *ScrobbleSeasonBlock `json:"season,omitempty"`
}

// ScrobbleSeasonBlock is the nested season block inside a show scrobble.
type ScrobbleSeasonBlock struct {
	Number  int                     `json:"number"`
	Episode *ScrobbleEpisodePayload `json:"episode,omitempty"`
}

// ScrobbleEpisodePayload identifies an episode within a season.
type ScrobbleEpisodePayload struct {
	Number int `json:"number"`
}

// ScrobbleIDs holds external IDs for MDBList API.
// Note: MDBList does not recognize TVDB IDs — only IMDB and TMDB.
type ScrobbleIDs struct {
	IMDB string `json:"imdb,omitempty"`
	TMDB int    `json:"tmdb,omitempty"`
}

// ScrobbleRequest is the request body for /scrobble/{action} endpoints.
type ScrobbleRequest struct {
	Movie    *ScrobbleMoviePayload `json:"movie,omitempty"`
	Show     *ScrobbleShowPayload  `json:"show,omitempty"`
	Progress float64               `json:"progress"`
}

// SyncWatchedMovieItem represents a movie in a /sync/watched request.
type SyncWatchedMovieItem struct {
	IDs       ScrobbleIDs `json:"ids"`
	WatchedAt string      `json:"watched_at,omitempty"`
}

// SyncWatchedShowItem represents a show with season/episode info in a /sync/watched request.
type SyncWatchedShowItem struct {
	IDs     ScrobbleIDs        `json:"ids"`
	Seasons []SyncWatchedSeason `json:"seasons,omitempty"`
}

// SyncWatchedSeason represents a season within a sync/watched show.
type SyncWatchedSeason struct {
	Number   int                   `json:"number"`
	Episodes []SyncWatchedEpisode  `json:"episodes,omitempty"`
}

// SyncWatchedEpisode represents an episode within a sync/watched season.
type SyncWatchedEpisode struct {
	Number    int    `json:"number"`
	WatchedAt string `json:"watched_at,omitempty"`
}

// SyncWatchedRequest is the request body for /sync/watched.
type SyncWatchedRequest struct {
	Movies []SyncWatchedMovieItem `json:"movies,omitempty"`
	Shows  []SyncWatchedShowItem  `json:"shows,omitempty"`
}

// ScrobbleStart sends a scrobble/start event.
func (c *ScrobbleClient) ScrobbleStart(req ScrobbleRequest) error {
	return c.scrobble("start", req)
}

// ScrobblePause sends a scrobble/pause event.
func (c *ScrobbleClient) ScrobblePause(req ScrobbleRequest) error {
	return c.scrobble("pause", req)
}

// ScrobbleStop sends a scrobble/stop event.
func (c *ScrobbleClient) ScrobbleStop(req ScrobbleRequest) error {
	return c.scrobble("stop", req)
}

// SyncWatched sends a batch of watched items.
func (c *ScrobbleClient) SyncWatched(req SyncWatchedRequest) error {
	apiKey := c.getAPIKey()
	if apiKey == "" {
		return fmt.Errorf("mdblist API key not configured")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal sync request: %w", err)
	}

	url := fmt.Sprintf("%s/sync/watched?apikey=%s", baseURL, apiKey)
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create sync request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("sync watched: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("mdblist sync/watched returned %d", resp.StatusCode)
	}
	return nil
}

// ErrScrobble400 is returned for 400 responses so callers can detect bad requests.
type ErrScrobble400 struct {
	Action string
	Body   string
}

func (e *ErrScrobble400) Error() string {
	return fmt.Sprintf("mdblist scrobble/%s returned 400: %s", e.Action, e.Body)
}

func (c *ScrobbleClient) scrobble(action string, req ScrobbleRequest) error {
	apiKey := c.getAPIKey()
	if apiKey == "" {
		return fmt.Errorf("mdblist API key not configured")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal scrobble request: %w", err)
	}

	url := fmt.Sprintf("%s/scrobble/%s?apikey=%s", baseURL, action, apiKey)
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create scrobble request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("scrobble/%s: %w", action, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if resp.StatusCode == 400 {
			return &ErrScrobble400{Action: action, Body: string(respBody)}
		}
		return fmt.Errorf("mdblist scrobble/%s returned %d: %s", action, resp.StatusCode, string(respBody))
	}
	return nil
}

// ListItem represents an item in an MDBList list.
type ListItem struct {
	ID        int    `json:"id"`
	Title     string `json:"title"`
	Year      int    `json:"year"`
	MediaType string `json:"mediatype"` // "movie" or "show"
	IMDBID    string `json:"imdb_id"`
	TMDBID    int    `json:"tmdb_id"`
	TVDBID    int    `json:"tvdb_id"`
}

// UserList represents an MDBList user list.
type UserList struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// GetUserLists fetches the user's lists from MDBList.
func (c *ScrobbleClient) GetUserLists(apiKey string) ([]UserList, error) {
	url := fmt.Sprintf("%s/lists/user?apikey=%s", baseURL, apiKey)
	resp, err := c.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch user lists: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("mdblist /lists/user returned %d", resp.StatusCode)
	}

	var lists []UserList
	if err := json.NewDecoder(resp.Body).Decode(&lists); err != nil {
		return nil, fmt.Errorf("decode user lists: %w", err)
	}
	return lists, nil
}

// GetListItems fetches items from an MDBList list by ID.
func (c *ScrobbleClient) GetListItems(apiKey string, listID int) ([]ListItem, error) {
	url := fmt.Sprintf("%s/lists/%d/items?apikey=%s", baseURL, listID, apiKey)
	resp, err := c.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch list items: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("mdblist /lists/%d/items returned %d", listID, resp.StatusCode)
	}

	var items []ListItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("decode list items: %w", err)
	}
	return items, nil
}

// GetWatchlist fetches the user's watchlist from MDBList.
func (c *ScrobbleClient) GetWatchlist(apiKey string) ([]ListItem, error) {
	url := fmt.Sprintf("%s/user/watchlist?apikey=%s", baseURL, apiKey)
	resp, err := c.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch watchlist: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("mdblist /user/watchlist returned %d", resp.StatusCode)
	}

	var items []ListItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("decode watchlist: %w", err)
	}
	return items, nil
}
