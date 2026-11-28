package mdblist

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"novastream/internal/urlredact"
	"sync"
	"time"

	"novastream/internal/apiusage"
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
		apiKey:     apiKey,
		httpClient: apiusage.TrackClient(&http.Client{Timeout: 10 * time.Second}, "MDBList", "Scrobble API"),
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
	IDs     ScrobbleIDs         `json:"ids"`
	Seasons []SyncWatchedSeason `json:"seasons,omitempty"`
}

// SyncWatchedSeason represents a season within a sync/watched show.
type SyncWatchedSeason struct {
	Number   int                  `json:"number"`
	Episodes []SyncWatchedEpisode `json:"episodes,omitempty"`
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

type watchedPlayIDs struct {
	IMDB string `json:"imdb,omitempty"`
	TMDB int    `json:"tmdb,omitempty"`
	TVDB int    `json:"tvdb,omitempty"`
}

type watchedPlaysResponse struct {
	Movies []struct {
		PlayID int `json:"play_id"`
		Movie  struct {
			IDs watchedPlayIDs `json:"ids"`
		} `json:"movie"`
	} `json:"movies"`
	Episodes []struct {
		PlayID  int `json:"play_id"`
		Episode struct {
			Season int            `json:"season"`
			Number int            `json:"number"`
			IDs    watchedPlayIDs `json:"ids"`
			Show   struct {
				IDs watchedPlayIDs `json:"ids"`
			} `json:"show"`
		} `json:"episode"`
	} `json:"episodes"`
	Pagination struct {
		HasMore    bool   `json:"has_more"`
		NextCursor string `json:"next_cursor"`
	} `json:"pagination"`
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

// SyncWatchedResult captures MDBList /sync/watched acceptance counts.
type SyncWatchedResult struct {
	UpdatedEpisodes  int
	NotFoundEpisodes int
	// NotFoundEpisodeKeys are "season:number" entries reported missing.
	NotFoundEpisodeKeys []string
}

// SyncWatched sends a batch of watched items.
func (c *ScrobbleClient) SyncWatched(req SyncWatchedRequest) error {
	_, err := c.SyncWatchedDetailed(req)
	return err
}

// SyncWatchedDetailed sends a batch of watched items and returns not_found info.
func (c *ScrobbleClient) SyncWatchedDetailed(req SyncWatchedRequest) (SyncWatchedResult, error) {
	return c.syncWatchedDetailed("/sync/watched", req)
}

// SyncUnwatched removes watched state while preserving MDBList play history.
func (c *ScrobbleClient) SyncUnwatched(req SyncWatchedRequest) error {
	_, err := c.SyncUnwatchedDetailed(req)
	return err
}

// SyncUnwatchedDetailed removes watched state and returns unmatched episodes.
func (c *ScrobbleClient) SyncUnwatchedDetailed(req SyncWatchedRequest) (SyncWatchedResult, error) {
	return c.syncWatchedDetailed("/sync/watched/remove", req)
}

func (c *ScrobbleClient) syncWatchedDetailed(path string, req SyncWatchedRequest) (SyncWatchedResult, error) {
	var result SyncWatchedResult
	apiKey := c.getAPIKey()
	if apiKey == "" {
		return result, fmt.Errorf("mdblist API key not configured")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return result, fmt.Errorf("marshal sync request: %w", err)
	}

	url := fmt.Sprintf("%s%s?apikey=%s", baseURL, path, apiKey)
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return result, fmt.Errorf("create sync request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return result, fmt.Errorf("sync watched state: %w", urlredact.Error(err))
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode >= 400 {
		return result, fmt.Errorf("mdblist %s returned %d: %s", path, resp.StatusCode, string(respBody))
	}

	var parsed struct {
		Updated struct {
			Episodes int `json:"episodes"`
		} `json:"updated"`
		NotFound struct {
			Episodes []struct {
				Season int `json:"season"`
				Number int `json:"number"`
			} `json:"episodes"`
		} `json:"not_found"`
	}
	if err := json.Unmarshal(respBody, &parsed); err == nil {
		result.UpdatedEpisodes = parsed.Updated.Episodes
		result.NotFoundEpisodes = len(parsed.NotFound.Episodes)
		for _, ep := range parsed.NotFound.Episodes {
			result.NotFoundEpisodeKeys = append(result.NotFoundEpisodeKeys, fmt.Sprintf("%d:%d", ep.Season, ep.Number))
		}
	}
	return result, nil
}

// RemoveMoviePlays removes all retained MDBList play-history rows for a movie.
func (c *ScrobbleClient) RemoveMoviePlays(ids ScrobbleIDs) error {
	return c.removeMatchingPlays("movie", func(play watchedPlaysResponse, index int) bool {
		return watchedPlayIDsMatch(play.Movies[index].Movie.IDs, ids)
	})
}

// RemoveEpisodePlays removes all retained MDBList play-history rows for an episode.
func (c *ScrobbleClient) RemoveEpisodePlays(showIDs ScrobbleIDs, season, episode, episodeTMDB, episodeTVDB int) error {
	return c.removeMatchingPlays("episode", func(play watchedPlaysResponse, index int) bool {
		candidate := play.Episodes[index].Episode
		if episodeTMDB > 0 && candidate.IDs.TMDB == episodeTMDB {
			return true
		}
		if episodeTVDB > 0 && candidate.IDs.TVDB == episodeTVDB {
			return true
		}
		return candidate.Season == season && candidate.Number == episode && watchedPlayIDsMatch(candidate.Show.IDs, showIDs)
	})
}

func (c *ScrobbleClient) removeMatchingPlays(mediaType string, matches func(watchedPlaysResponse, int) bool) error {
	apiKey := c.getAPIKey()
	if apiKey == "" {
		return fmt.Errorf("mdblist API key not configured")
	}
	var playIDs []int
	cursor := ""
	for {
		query := url.Values{"apikey": {apiKey}, "mediatype": {mediaType}, "plays": {"all"}, "limit": {"1000"}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		httpReq, err := http.NewRequest(http.MethodGet, baseURL+"/sync/watched?"+query.Encode(), nil)
		if err != nil {
			return fmt.Errorf("create watched plays request: %w", err)
		}
		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			return fmt.Errorf("list watched plays: %w", urlredact.Error(err))
		}
		var page watchedPlaysResponse
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			resp.Body.Close()
			return fmt.Errorf("mdblist list watched plays returned %d: %s", resp.StatusCode, string(body))
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("decode watched plays: %w", err)
		}
		if mediaType == "movie" {
			for i := range page.Movies {
				if page.Movies[i].PlayID > 0 && matches(page, i) {
					playIDs = append(playIDs, page.Movies[i].PlayID)
				}
			}
		} else {
			for i := range page.Episodes {
				if page.Episodes[i].PlayID > 0 && matches(page, i) {
					playIDs = append(playIDs, page.Episodes[i].PlayID)
				}
			}
		}
		if !page.Pagination.HasMore || page.Pagination.NextCursor == "" {
			break
		}
		cursor = page.Pagination.NextCursor
	}
	if len(playIDs) == 0 {
		return nil
	}
	body, err := json.Marshal(struct {
		PlayIDs []int `json:"play_ids"`
	}{PlayIDs: playIDs})
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/sync/watched/plays/remove?apikey=%s", baseURL, url.QueryEscape(apiKey)), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create remove watched plays request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("remove watched plays: %w", urlredact.Error(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return fmt.Errorf("mdblist remove watched plays returned %d: %s", resp.StatusCode, string(responseBody))
	}
	return nil
}

func watchedPlayIDsMatch(candidate watchedPlayIDs, wanted ScrobbleIDs) bool {
	return (wanted.TMDB > 0 && candidate.TMDB == wanted.TMDB) || (wanted.IMDB != "" && candidate.IMDB == wanted.IMDB)
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
		return fmt.Errorf("scrobble/%s: %w", action, urlredact.Error(err))
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
		return nil, fmt.Errorf("fetch user lists: %w", urlredact.Error(err))
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
		return nil, fmt.Errorf("fetch list items: %w", urlredact.Error(err))
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
		return nil, fmt.Errorf("fetch watchlist: %w", urlredact.Error(err))
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
