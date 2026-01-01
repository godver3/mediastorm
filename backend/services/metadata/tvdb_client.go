package metadata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Minimal TVDB v4 client (token auth, trending and search endpoints we need)

type tvdbClient struct {
	apiKey   string
	language string
	httpc    *http.Client

	mu          sync.Mutex
	token       string
	tokenExpiry time.Time

	throttleMu  sync.Mutex
	lastRequest time.Time
	minInterval time.Duration

	episodeTranslationCache sync.Map
}

func newTVDBClient(apiKey, language string, httpc *http.Client) *tvdbClient {
	if httpc == nil {
		httpc = &http.Client{Timeout: 15 * time.Second}
	}
	// TVDB uses 3-letter ISO 639-2 codes, normalize from 2-letter to 3-letter
	language = normalizeTVDBLanguage(language)
	return &tvdbClient{apiKey: apiKey, language: language, httpc: httpc, minInterval: 200 * time.Millisecond}
}

// normalizeTVDBLanguage converts 2-letter ISO 639-1 codes to 3-letter ISO 639-2 codes for TVDB
func normalizeTVDBLanguage(lang string) string {
	lang = strings.TrimSpace(strings.ToLower(lang))
	// Map common 2-letter codes to TVDB's 3-letter codes
	switch lang {
	case "en":
		return "eng"
	case "es":
		return "spa"
	case "fr":
		return "fra"
	case "de":
		return "deu"
	case "it":
		return "ita"
	case "pt":
		return "por"
	case "ja":
		return "jpn"
	case "ko":
		return "kor"
	case "zh":
		return "zho"
	case "ru":
		return "rus"
	case "ar":
		return "ara"
	case "hi":
		return "hin"
	case "nl":
		return "nld"
	case "sv":
		return "swe"
	case "no":
		return "nor"
	case "da":
		return "dan"
	case "fi":
		return "fin"
	case "pl":
		return "pol"
	case "tr":
		return "tur"
	case "he":
		return "heb"
	case "cs":
		return "ces"
	case "hu":
		return "hun"
	case "ro":
		return "ron"
	case "th":
		return "tha"
	case "vi":
		return "vie"
	default:
		// If already 3 letters, assume it's correct; otherwise default to eng
		if len(lang) == 3 {
			return lang
		}
		return "eng"
	}
}

func (c *tvdbClient) ensureToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExpiry.Add(-1*time.Minute)) {
		return c.token, nil
	}
	body := map[string]string{"apikey": c.apiKey}
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "https://api4.thetvdb.com/v4/login", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("tvdb login failed: %s", resp.Status)
	}
	var data struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	c.token = data.Data.Token
	c.tokenExpiry = time.Now().Add(23 * time.Hour)
	return c.token, nil
}

func (c *tvdbClient) doGET(u string, q url.Values, v any) error {
	if len(q) > 0 {
		if strings.Contains(u, "?") {
			u = u + "&" + q.Encode()
		} else {
			u = u + "?" + q.Encode()
		}
	}
	var lastErr error
	backoff := 300 * time.Millisecond
	for attempt := 0; attempt < 3; attempt++ {
		token, err := c.ensureToken()
		if err != nil {
			lastErr = err
			time.Sleep(backoff)
			backoff *= 2
			continue
		}
		c.throttleMu.Lock()
		since := time.Since(c.lastRequest)
		if since < c.minInterval {
			time.Sleep(c.minInterval - since)
		}
		c.lastRequest = time.Now()
		c.throttleMu.Unlock()

		req, _ := http.NewRequest(http.MethodGet, u, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		if c.language != "" {
			if acceptLang := normalizeLanguageCode(c.language); acceptLang != "" {
				req.Header.Set("Accept-Language", acceptLang)
			}
		}
		log.Printf("[tvdb] GET %s acceptLanguage=%q", u, req.Header.Get("Accept-Language"))
		resp, err := c.httpc.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(backoff)
			backoff *= 2
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				if ra := resp.Header.Get("Retry-After"); ra != "" {
					if secs, err := strconv.Atoi(ra); err == nil {
						time.Sleep(time.Duration(secs) * time.Second)
					}
				} else {
					time.Sleep(backoff)
					backoff *= 2
				}
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
				lastErr = fmt.Errorf("tvdb get %s failed: %s: %s", u, resp.Status, strings.TrimSpace(string(body)))
				continue
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			return fmt.Errorf("tvdb get %s failed: %s: %s", u, resp.Status, strings.TrimSpace(string(body)))
		}
		return json.NewDecoder(resp.Body).Decode(v)
	}
	return lastErr
}

func (c *tvdbClient) episodeTranslation(id int64, lang string) (*tvdbEpisodeTranslation, error) {
	if lang == "" {
		lang = "eng"
	}
	key := fmt.Sprintf("%d:%s", id, lang)
	if cached, ok := c.episodeTranslationCache.Load(key); ok {
		if cached == nil {
			return nil, nil
		}
		if translation, ok := cached.(*tvdbEpisodeTranslation); ok {
			// return a copy to avoid callers mutating cached value
			clone := *translation
			if strings.TrimSpace(clone.Name) == "" && strings.TrimSpace(clone.Overview) == "" {
				return nil, nil
			}
			return &clone, nil
		}
	}

	var resp struct {
		Data tvdbEpisodeTranslation `json:"data"`
	}
	endpoint := fmt.Sprintf("https://api4.thetvdb.com/v4/episodes/%d/translations/%s", id, lang)
	if err := c.doGET(endpoint, nil, &resp); err != nil {
		c.episodeTranslationCache.Store(key, (*tvdbEpisodeTranslation)(nil))
		return nil, err
	}

	translation := resp.Data
	if strings.TrimSpace(translation.Name) == "" && strings.TrimSpace(translation.Overview) == "" {
		c.episodeTranslationCache.Store(key, (*tvdbEpisodeTranslation)(nil))
		return nil, nil
	}
	// store pointer in cache; make sure copy escapes
	result := translation
	c.episodeTranslationCache.Store(key, &result)
	return &translation, nil
}

func (c *tvdbClient) seriesEpisodesBySeasonType(id int64, seasonType, lang string) ([]tvdbEpisode, error) {
	seasonType = strings.TrimSpace(strings.ToLower(seasonType))
	if seasonType == "" {
		seasonType = "official"
	}
	lang = strings.TrimSpace(strings.ToLower(lang))
	lang = normalizeTVDBLanguage(lang)
	if lang == "" {
		lang = "eng"
	}

	endpoint := fmt.Sprintf("https://api4.thetvdb.com/v4/series/%d/episodes/%s/%s", id, seasonType, lang)
	page := 0
	results := make([]tvdbEpisode, 0, 50)
	for {
		params := url.Values{}
		params.Set("page", strconv.Itoa(page))
		var resp struct {
			Data struct {
				Episodes []tvdbEpisode `json:"episodes"`
			} `json:"data"`
			Links struct {
				Next *string `json:"next"`
			} `json:"links"`
		}
		if err := c.doGET(endpoint, params, &resp); err != nil {
			return nil, err
		}
		results = append(results, resp.Data.Episodes...)
		if resp.Links.Next == nil || strings.TrimSpace(*resp.Links.Next) == "" {
			break
		}
		page++
	}
	return results, nil
}

type tvdbArtworkType string

func (t *tvdbArtworkType) UnmarshalJSON(data []byte) error {
	if len(data) == 0 {
		*t = ""
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*t = tvdbArtworkType(s)
		return nil
	}

	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}
	*t = tvdbArtworkType(n.String())
	return nil
}

func (t tvdbArtworkType) String() string {
	return string(t)
}

type tvdbArtwork struct {
	ID        int64           `json:"id"`
	Image     string          `json:"image"`
	Thumbnail string          `json:"thumbnail"`
	Language  string          `json:"language"`
	Type      tvdbArtworkType `json:"type"`
	Width     int             `json:"width"`
	Height    int             `json:"height"`
}

func (c *tvdbClient) seriesArtworks(id int64) ([]tvdbArtwork, error) {
	var resp struct {
		Data []tvdbArtwork `json:"data"`
	}
	if err := c.doGET(fmt.Sprintf("https://api4.thetvdb.com/v4/series/%d/artworks", id), nil, &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func (c *tvdbClient) movieArtworks(id int64) ([]tvdbArtwork, error) {
	extended, err := c.movieExtended(id, []string{"artwork"})
	if err != nil {
		return nil, err
	}
	return extended.Artworks, nil
}

func (c *tvdbClient) seriesAliases(id int64) ([]tvdbAlias, error) {
	var resp struct {
		Data struct {
			Aliases []tvdbAlias `json:"aliases"`
		} `json:"data"`
	}
	if err := c.doGET(fmt.Sprintf("https://api4.thetvdb.com/v4/series/%d", id), nil, &resp); err != nil {
		return nil, err
	}
	return resp.Data.Aliases, nil
}

func (c *tvdbClient) movieAliases(id int64) ([]tvdbAlias, error) {
	var resp struct {
		Data struct {
			Aliases []tvdbAlias `json:"aliases"`
		} `json:"data"`
	}
	if err := c.doGET(fmt.Sprintf("https://api4.thetvdb.com/v4/movies/%d", id), nil, &resp); err != nil {
		return nil, err
	}
	return resp.Data.Aliases, nil
}

func (c *tvdbClient) seriesExtended(id int64, meta []string) (tvdbSeriesExtendedData, error) {
	var resp struct {
		Data tvdbSeriesExtendedData `json:"data"`
	}
	params := url.Values{}
	if len(meta) > 0 {
		params.Set("meta", strings.Join(meta, ","))
	}
	if err := c.doGET(fmt.Sprintf("https://api4.thetvdb.com/v4/series/%d/extended", id), params, &resp); err != nil {
		return tvdbSeriesExtendedData{}, err
	}
	return resp.Data, nil
}

func (c *tvdbClient) movieExtended(id int64, meta []string) (tvdbMovieExtendedData, error) {
	var resp struct {
		Data tvdbMovieExtendedData `json:"data"`
	}
	params := url.Values{}
	if len(meta) > 0 {
		params.Set("meta", strings.Join(meta, ","))
	}
	if err := c.doGET(fmt.Sprintf("https://api4.thetvdb.com/v4/movies/%d/extended", id), params, &resp); err != nil {
		return tvdbMovieExtendedData{}, err
	}
	return resp.Data, nil
}

// seriesTranslations fetches translation for a series in the specified language
func (c *tvdbClient) seriesTranslations(id int64, lang string) (*tvdbSeriesTranslation, error) {
	var resp struct {
		Data tvdbSeriesTranslation `json:"data"`
	}
	endpoint := fmt.Sprintf("https://api4.thetvdb.com/v4/series/%d/translations/%s", id, lang)
	if err := c.doGET(endpoint, nil, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// movieTranslations fetches translation for a movie in the specified language
func (c *tvdbClient) movieTranslations(id int64, lang string) (*tvdbSeriesTranslation, error) {
	var resp struct {
		Data tvdbSeriesTranslation `json:"data"`
	}
	endpoint := fmt.Sprintf("https://api4.thetvdb.com/v4/movies/%d/translations/%s", id, lang)
	if err := c.doGET(endpoint, nil, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// seasonTranslations fetches translation for a season in the specified language
func (c *tvdbClient) seasonTranslations(id int64, lang string) (*tvdbSeriesTranslation, error) {
	var resp struct {
		Data tvdbSeriesTranslation `json:"data"`
	}
	endpoint := fmt.Sprintf("https://api4.thetvdb.com/v4/seasons/%d/translations/%s", id, lang)
	if err := c.doGET(endpoint, nil, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// filterMovies queries the movies/filter endpoint with the specified parameters
func (c *tvdbClient) filterMovies(params url.Values) ([]tvdbMovie, error) {
	var resp struct {
		Data []tvdbMovie `json:"data"`
	}
	if err := c.doGET("https://api4.thetvdb.com/v4/movies/filter", params, &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

type tvdbMovie struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Overview string   `json:"overview"`
	Year     tvdbYear `json:"year"`
	Score    float64  `json:"score"`
}

type tvdbSeasonType struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type tvdbSeason struct {
	ID       int64          `json:"id"`
	SeriesID int64          `json:"seriesId"`
	Type     tvdbSeasonType `json:"type"`
	Name     string         `json:"name"`
	Number   int            `json:"number"`
	Overview string         `json:"overview"`
	Image    string         `json:"image"`
}

type tvdbEpisode struct {
	ID           int64                    `json:"id"`
	SeriesID     int64                    `json:"seriesId"`
	SeasonID     int64                    `json:"seasonId"`
	SeasonNumber int                      `json:"seasonNumber"`
	Number       int                      `json:"number"`
	Name         string                   `json:"name"`
	Abbreviation string                   `json:"abbreviation"`
	Overview     string                   `json:"overview"`
	Aired        string                   `json:"aired"`
	Runtime      int                      `json:"runtime"`
	Image        string                   `json:"image"`
	Translations []tvdbEpisodeTranslation `json:"translations"`
}

type tvdbEpisodeTranslation struct {
	Language  string `json:"language"`
	Name      string `json:"name"`
	Overview  string `json:"overview"`
	IsPrimary bool   `json:"isPrimary"`
}

type tvdbAlias struct {
	Language string `json:"language"`
	Name     string `json:"name"`
}

type tvdbSeriesTranslation struct {
	Language  string `json:"language"`
	Name      string `json:"name"`
	Overview  string `json:"overview"`
	IsPrimary bool   `json:"isPrimary"`
}

type tvdbSeriesExtendedData struct {
	ID        int64         `json:"id"`
	Name      string        `json:"name"`
	Overview  string        `json:"overview"`
	Year      tvdbYear      `json:"year"`
	Network   string        `json:"network"`
	Image     string        `json:"image"`
	Poster    string        `json:"poster"`
	Fanart    string        `json:"fanart"`
	Seasons   []tvdbSeason  `json:"seasons"`
	Episodes  []tvdbEpisode `json:"episodes"`
	Trailers  []tvdbTrailer `json:"trailers"`
	Artworks  []tvdbArtwork `json:"artworks"`
	RemoteIDs []struct {
		ID         string `json:"id"`
		Type       int    `json:"type"`
		SourceName string `json:"sourceName"`
	} `json:"remoteIds"`
	Status struct {
		Name string `json:"name"` // "Continuing", "Ended", "Upcoming"
	} `json:"status"`
}

type tvdbMovieExtendedData struct {
	ID        int64         `json:"id"`
	Name      string        `json:"name"`
	Overview  string        `json:"overview"`
	Year      tvdbYear      `json:"year"`
	Trailers  []tvdbTrailer `json:"trailers"`
	Artworks  []tvdbArtwork `json:"artworks"`
	RemoteIDs []struct {
		ID         string `json:"id"`
		Type       int    `json:"type"`
		SourceName string `json:"sourceName"`
	} `json:"remoteIds"`
}

type tvdbTrailer struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Language  string `json:"language"`
	Runtime   int    `json:"runtime"`
	Thumbnail string `json:"thumbnail"`
}

// MDBList movie structure
type mdblistMovie struct {
	ID          int    `json:"id"`
	Rank        int    `json:"rank"`
	Adult       int    `json:"adult"`
	Title       string `json:"title"`
	TVDBID      *int64 `json:"tvdbid"`
	IMDBID      string `json:"imdb_id"`
	MediaType   string `json:"mediatype"`
	ReleaseYear int    `json:"release_year"`
}

// MDBList TV show structure (same as movie but with mediatype: "show")
type mdblistTVShow struct {
	ID          int    `json:"id"`
	Rank        int    `json:"rank"`
	Adult       int    `json:"adult"`
	Title       string `json:"title"`
	TVDBID      *int64 `json:"tvdbid"`
	IMDBID      string `json:"imdb_id"`
	MediaType   string `json:"mediatype"`
	ReleaseYear int    `json:"release_year"`
}

// fetchMDBListMovies fetches the top movies of the week from MDBList
func (c *tvdbClient) fetchMDBListMovies() ([]mdblistMovie, error) {
	req, err := http.NewRequest(http.MethodGet, "https://mdblist.com/lists/garycrawfordgc/top-movies-of-the-week/json", nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mdblist request failed: %s", resp.Status)
	}

	var movies []mdblistMovie
	if err := json.NewDecoder(resp.Body).Decode(&movies); err != nil {
		return nil, err
	}

	return movies, nil
}

// fetchMDBListTVShows fetches the latest TV shows from MDBList
func (c *tvdbClient) fetchMDBListTVShows() ([]mdblistTVShow, error) {
	req, err := http.NewRequest(http.MethodGet, "https://mdblist.com/lists/garycrawfordgc/latest-tv-shows/json", nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mdblist request failed: %s", resp.Status)
	}

	var tvShows []mdblistTVShow
	if err := json.NewDecoder(resp.Body).Decode(&tvShows); err != nil {
		return nil, err
	}

	// Take only the first 10 results
	if len(tvShows) > 10 {
		tvShows = tvShows[:10]
	}

	return tvShows, nil
}
