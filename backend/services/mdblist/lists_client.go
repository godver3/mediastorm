package mdblist

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// ListsClient reads MDBList lists (including imported external lists such as
// Letterboxd) via the MDBList API.
type ListsClient struct {
	mu         sync.RWMutex
	apiKey     string
	httpClient *http.Client
}

// NewListsClient creates a new MDBList lists client.
func NewListsClient(apiKey string) *ListsClient {
	return &ListsClient{
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// UpdateAPIKey updates the API key at runtime (e.g., when settings change).
func (c *ListsClient) UpdateAPIKey(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apiKey = key
}

// SetHTTPClientForTest overrides the HTTP client.
func (c *ListsClient) SetHTTPClientForTest(httpClient *http.Client) {
	if httpClient != nil {
		c.httpClient = httpClient
	}
}

func (c *ListsClient) getAPIKey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.apiKey
}

// IsConfigured reports whether an API key is set.
func (c *ListsClient) IsConfigured() bool {
	return c.getAPIKey() != ""
}

// ExternalList is a user's imported/linked external list (Letterboxd, IMDb, etc.).
type ExternalList struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Source string `json:"source"`
	Items  int    `json:"items"`
	URL    string `json:"url"`
}

// ExternalListItem is a normalized entry from an MDBList list.
type ExternalListItem struct {
	Title     string
	Year      int
	MediaType string // "movie" or "show"
	IMDBID    string
	TMDBID    int64
	TVDBID    int64
}

type externalListItemRaw struct {
	Title       string `json:"title"`
	ReleaseYear int    `json:"release_year"`
	MediaType   string `json:"mediatype"`
	IMDBID      string `json:"imdb_id"`
	TVDBID      int64  `json:"tvdb_id"`
	IDs         struct {
		IMDB string `json:"imdb"`
		TMDB int64  `json:"tmdb"`
		TVDB int64  `json:"tvdb"`
	} `json:"ids"`
}

func (r externalListItemRaw) normalize() (ExternalListItem, bool) {
	item := ExternalListItem{
		Title:     r.Title,
		Year:      r.ReleaseYear,
		MediaType: "movie",
		IMDBID:    r.IDs.IMDB,
		TMDBID:    r.IDs.TMDB,
		TVDBID:    r.IDs.TVDB,
	}
	if item.MediaType == "movie" && (r.MediaType == "show" || r.MediaType == "series" || r.MediaType == "tv") {
		item.MediaType = "show"
	}
	if item.IMDBID == "" {
		item.IMDBID = r.IMDBID
	}
	if item.TVDBID == 0 {
		item.TVDBID = r.TVDBID
	}
	if item.Title == "" && item.IMDBID == "" && item.TMDBID == 0 && item.TVDBID == 0 {
		return ExternalListItem{}, false
	}
	return item, true
}

// GetExternalLists returns the authenticated user's imported external lists.
func (c *ListsClient) GetExternalLists(ctx context.Context) ([]ExternalList, error) {
	apiKey := c.getAPIKey()
	if apiKey == "" {
		return nil, fmt.Errorf("mdblist api key not configured")
	}

	endpoint := fmt.Sprintf("%s/external/lists/user?apikey=%s", baseURL, url.QueryEscape(apiKey))
	var lists []ExternalList
	if err := c.getJSON(ctx, endpoint, &lists); err != nil {
		return nil, err
	}
	return lists, nil
}

// GetExternalListItems returns the items of an MDBList external list by its ID.
func (c *ListsClient) GetExternalListItems(ctx context.Context, listID string, maxItems ...int) ([]ExternalListItem, error) {
	apiKey := c.getAPIKey()
	if apiKey == "" {
		return nil, fmt.Errorf("mdblist api key not configured")
	}
	if listID == "" {
		return nil, fmt.Errorf("list id required")
	}

	limit := 1000
	if len(maxItems) > 0 && maxItems[0] > 0 && maxItems[0] < limit {
		limit = maxItems[0]
	}
	endpoint := fmt.Sprintf("%s/external/lists/%s/items?apikey=%s&limit=%d",
		baseURL, url.PathEscape(listID), url.QueryEscape(apiKey), limit)

	var resp struct {
		Movies []externalListItemRaw `json:"movies"`
		Shows  []externalListItemRaw `json:"shows"`
	}
	if err := c.getJSON(ctx, endpoint, &resp); err != nil {
		return nil, err
	}

	items := make([]ExternalListItem, 0, len(resp.Movies)+len(resp.Shows))
	for _, raw := range resp.Movies {
		if item, ok := raw.normalize(); ok {
			items = append(items, item)
		}
	}
	for _, raw := range resp.Shows {
		if item, ok := raw.normalize(); ok {
			items = append(items, item)
		}
	}
	return items, nil
}

func (c *ListsClient) getJSON(ctx context.Context, endpoint string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "mediastorm/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("mdblist api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("mdblist api returned %d: %s", resp.StatusCode, string(body))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
