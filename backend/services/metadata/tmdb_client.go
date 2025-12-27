package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/models"
)

const (
	tmdbBaseURL      = "https://api.themoviedb.org/3"
	tmdbImageBaseURL = "https://image.tmdb.org/t/p"
	// Use optimized image sizes instead of "original" to reduce memory usage
	// Posters: w500 = 500px wide (plenty for TV cards ~200-300px)
	// Backdrops: w1280 = 1280px wide (good for 1080p backgrounds)
	tmdbPosterSize   = "w500"
	tmdbBackdropSize = "w1280"
)

type tmdbClient struct {
	apiKey   string
	language string
	httpc    *http.Client
}

func newTMDBClient(apiKey, language string, httpc *http.Client) *tmdbClient {
	if httpc == nil {
		httpc = &http.Client{Timeout: 15 * time.Second}
	}
	return &tmdbClient{apiKey: strings.TrimSpace(apiKey), language: language, httpc: httpc}
}

func (c *tmdbClient) isConfigured() bool {
	return c != nil && c.apiKey != ""
}

type tmdbTrendingResponse struct {
	Results []struct {
		ID               int64   `json:"id"`
		Name             string  `json:"name"`
		Title            string  `json:"title"`
		Overview         string  `json:"overview"`
		OriginalLanguage string  `json:"original_language"`
		PosterPath       string  `json:"poster_path"`
		BackdropPath     string  `json:"backdrop_path"`
		Popularity       float64 `json:"popularity"`
		VoteAverage      float64 `json:"vote_average"`
		FirstAirDate     string  `json:"first_air_date"`
		ReleaseDate      string  `json:"release_date"`
		MediaType        string  `json:"media_type"`
	} `json:"results"`
}

type tmdbExternalIDsResponse struct {
	IMDBID      string `json:"imdb_id"`
	FacebookID  string `json:"facebook_id"`
	InstagramID string `json:"instagram_id"`
	TwitterID   string `json:"twitter_id"`
}

type tmdbVideosResponse struct {
	Results []tmdbVideo `json:"results"`
}

type tmdbVideo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Key         string `json:"key"`
	Site        string `json:"site"`
	Type        string `json:"type"`
	Official    bool   `json:"official"`
	PublishedAt string `json:"published_at"`
	ISO6391     string `json:"iso_639_1"`
	ISO31661    string `json:"iso_3166_1"`
	Size        int    `json:"size"`
}

type tmdbReleaseDatesResponse struct {
	Results []tmdbReleaseCountry `json:"results"`
}

type tmdbReleaseCountry struct {
	ISO31661     string             `json:"iso_3166_1"`
	ReleaseDates []tmdbReleaseEntry `json:"release_dates"`
}

type tmdbReleaseEntry struct {
	Certification string   `json:"certification"`
	ISO6391       string   `json:"iso_639_1"`
	Note          string   `json:"note"`
	ReleaseDate   string   `json:"release_date"`
	Type          int      `json:"type"`
	Descriptors   []string `json:"descriptors"`
}

func (c *tmdbClient) trending(ctx context.Context, mediaType string) ([]models.TrendingItem, error) {
	if !c.isConfigured() {
		return nil, errors.New("tmdb api key not configured")
	}

	endpoint, err := url.JoinPath(tmdbBaseURL, "trending", mediaType, "week")
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	q.Set("api_key", c.apiKey)
	if lang := strings.TrimSpace(c.language); lang != "" {
		q.Set("language", normalizeLanguage(lang))
	} else {
		q.Set("language", "en-US")
	}
	req.URL.RawQuery = q.Encode()

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("tmdb trending %s failed: %s", mediaType, resp.Status)
	}

	var payload tmdbTrendingResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	items := make([]models.TrendingItem, len(payload.Results))

	// Fetch IMDB IDs concurrently for all items
	var wg sync.WaitGroup
	for idx, r := range payload.Results {
		wg.Add(1)
		go func(idx int, r struct {
			ID               int64   `json:"id"`
			Name             string  `json:"name"`
			Title            string  `json:"title"`
			Overview         string  `json:"overview"`
			OriginalLanguage string  `json:"original_language"`
			PosterPath       string  `json:"poster_path"`
			BackdropPath     string  `json:"backdrop_path"`
			Popularity       float64 `json:"popularity"`
			VoteAverage      float64 `json:"vote_average"`
			FirstAirDate     string  `json:"first_air_date"`
			ReleaseDate      string  `json:"release_date"`
			MediaType        string  `json:"media_type"`
		}) {
			defer wg.Done()

			title := models.Title{
				ID:         fmt.Sprintf("tmdb:%s:%d", mediaType, r.ID),
				Name:       pickTMDBName(mediaType, r.Name, r.Title),
				Overview:   r.Overview,
				Language:   r.OriginalLanguage,
				MediaType:  mapMediaType(mediaType),
				TMDBID:     r.ID,
				Popularity: scoreFallback(r.Popularity, r.VoteAverage),
			}
			if year := parseTMDBYear(r.ReleaseDate, r.FirstAirDate); year != 0 {
				title.Year = year
			}
			if poster := buildTMDBImage(r.PosterPath, tmdbPosterSize, "poster"); poster != nil {
				title.Poster = poster
			}
			if backdrop := buildTMDBImage(r.BackdropPath, tmdbBackdropSize, "backdrop"); backdrop != nil {
				title.Backdrop = backdrop
			}

			// Fetch IMDB ID from external IDs endpoint
			if imdbID, err := c.fetchExternalID(ctx, mediaType, r.ID); err == nil && imdbID != "" {
				title.IMDBID = imdbID
				log.Printf("[tmdb] fetched imdb id for %q: %s", title.Name, imdbID)
			}

			items[idx] = models.TrendingItem{Rank: idx + 1, Title: title}
		}(idx, r)
	}

	wg.Wait()

	return items, nil
}

func pickTMDBName(mediaType, seriesName, movieTitle string) string {
	if mediaType == "movie" && movieTitle != "" {
		return movieTitle
	}
	if seriesName != "" {
		return seriesName
	}
	if movieTitle != "" {
		return movieTitle
	}
	return ""
}

func mapMediaType(mediaType string) string {
	if mediaType == "movie" {
		return "movie"
	}
	return "series"
}

func parseTMDBYear(movieDate, seriesDate string) int {
	date := movieDate
	if date == "" {
		date = seriesDate
	}
	if date == "" {
		return 0
	}
	if t, err := time.Parse("2006-01-02", date); err == nil {
		return t.Year()
	}
	if len(date) >= 4 {
		if y, err := strconv.Atoi(date[:4]); err == nil {
			return y
		}
	}
	return 0
}

func buildTMDBImage(imagePath, size, imageType string) *models.Image {
	trimmed := strings.TrimSpace(imagePath)
	if trimmed == "" {
		return nil
	}
	fullPath := path.Join(size, strings.TrimPrefix(trimmed, "/"))
	return &models.Image{
		URL:  fmt.Sprintf("%s/%s", tmdbImageBaseURL, fullPath),
		Type: imageType,
	}
}

func normalizeLanguage(lang string) string {
	lang = strings.ReplaceAll(lang, "_", "-")
	if len(lang) == 2 {
		return strings.ToLower(lang) + "-US"
	}
	if len(lang) >= 5 {
		return strings.ToLower(lang[:2]) + "-" + strings.ToUpper(lang[3:])
	}
	return "en-US"
}

func scoreFallback(popularity, voteAverage float64) float64 {
	if popularity > 0 {
		return popularity
	}
	if voteAverage > 0 {
		return voteAverage
	}
	return 0
}

func (c *tmdbClient) fetchTrailers(ctx context.Context, mediaType string, tmdbID int64) ([]models.Trailer, error) {
	if !c.isConfigured() {
		return nil, errors.New("tmdb api key not configured")
	}

	apiMediaType := strings.ToLower(strings.TrimSpace(mediaType))
	if apiMediaType != "movie" {
		apiMediaType = "tv"
	}

	endpoint, err := url.JoinPath(tmdbBaseURL, apiMediaType, fmt.Sprintf("%d", tmdbID), "videos")
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	q.Set("api_key", c.apiKey)
	if lang := strings.TrimSpace(c.language); lang != "" {
		q.Set("language", normalizeLanguage(lang))
	}
	req.URL.RawQuery = q.Encode()

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("tmdb videos %s/%d failed: %s", apiMediaType, tmdbID, resp.Status)
	}

	var payload tmdbVideosResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	trailers := make([]models.Trailer, 0, len(payload.Results))
	for _, video := range payload.Results {
		url := strings.TrimSpace(video.Key)
		if url == "" {
			continue
		}
		site := strings.TrimSpace(video.Site)
		videoType := strings.TrimSpace(video.Type)
		trailer := models.Trailer{
			Name:        strings.TrimSpace(video.Name),
			Site:        site,
			Type:        videoType,
			Key:         strings.TrimSpace(video.Key),
			Official:    video.Official,
			PublishedAt: strings.TrimSpace(video.PublishedAt),
			Resolution:  video.Size,
			Language:    strings.TrimSpace(video.ISO6391),
			Country:     strings.TrimSpace(video.ISO31661),
			Source:      "tmdb",
		}

		switch strings.ToLower(site) {
		case "youtube":
			trailer.URL = fmt.Sprintf("https://www.youtube.com/watch?v=%s", trailer.Key)
			trailer.EmbedURL = fmt.Sprintf("https://www.youtube.com/embed/%s", trailer.Key)
			trailer.ThumbnailURL = fmt.Sprintf("https://img.youtube.com/vi/%s/hqdefault.jpg", trailer.Key)
		case "vimeo":
			trailer.URL = fmt.Sprintf("https://vimeo.com/%s", trailer.Key)
			trailer.EmbedURL = fmt.Sprintf("https://player.vimeo.com/video/%s", trailer.Key)
		default:
			trailer.URL = trailer.Key
		}

		if trailer.URL == "" {
			continue
		}

		trailers = append(trailers, trailer)
	}

	return trailers, nil
}

// fetchExternalID retrieves the IMDB ID for a TMDB movie or TV show
// movieDetails fetches movie details from TMDB including poster and backdrop
func (c *tmdbClient) movieDetails(ctx context.Context, tmdbID int64) (*models.Title, error) {
	if !c.isConfigured() {
		return nil, errors.New("tmdb api key not configured")
	}

	endpoint, err := url.JoinPath(tmdbBaseURL, "movie", fmt.Sprintf("%d", tmdbID))
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	q.Set("api_key", c.apiKey)
	if lang := strings.TrimSpace(c.language); lang != "" {
		q.Set("language", normalizeLanguage(lang))
	} else {
		q.Set("language", "en-US")
	}
	req.URL.RawQuery = q.Encode()

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("tmdb movie details failed: %s", resp.Status)
	}

	var movie struct {
		ID           int64  `json:"id"`
		Title        string `json:"title"`
		Overview     string `json:"overview"`
		PosterPath   string `json:"poster_path"`
		BackdropPath string `json:"backdrop_path"`
		ReleaseDate  string `json:"release_date"`
		IMDBId       string `json:"imdb_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&movie); err != nil {
		return nil, err
	}

	title := &models.Title{
		ID:        fmt.Sprintf("tmdb:movie:%d", movie.ID),
		Name:      movie.Title,
		Overview:  movie.Overview,
		MediaType: "movie",
		TMDBID:    movie.ID,
		IMDBID:    movie.IMDBId,
	}

	if year := parseTMDBYear(movie.ReleaseDate, ""); year != 0 {
		title.Year = year
	}
	if poster := buildTMDBImage(movie.PosterPath, tmdbPosterSize, "poster"); poster != nil {
		title.Poster = poster
	}
	if backdrop := buildTMDBImage(movie.BackdropPath, tmdbBackdropSize, "backdrop"); backdrop != nil {
		title.Backdrop = backdrop
	}

	return title, nil
}

func (c *tmdbClient) movieReleaseDates(ctx context.Context, tmdbID int64) ([]models.Release, error) {
	if !c.isConfigured() {
		return nil, errors.New("tmdb api key not configured")
	}

	endpoint, err := url.JoinPath(tmdbBaseURL, "movie", fmt.Sprintf("%d", tmdbID), "release_dates")
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	q.Set("api_key", c.apiKey)
	req.URL.RawQuery = q.Encode()

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("tmdb movie release dates failed: %s", resp.Status)
	}

	var payload tmdbReleaseDatesResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	now := time.Now()
	releases := make([]models.Release, 0, 8)
	for _, country := range payload.Results {
		countryCode := strings.TrimSpace(country.ISO31661)
		for _, entry := range country.ReleaseDates {
			releaseType := mapTMDBReleaseType(entry.Type)
			if releaseType == "" {
				continue
			}
			date := strings.TrimSpace(entry.ReleaseDate)
			released := false
			if t, err := time.Parse(time.RFC3339, date); err == nil {
				released = !t.After(now)
			} else if len(date) >= 10 {
				if t, err := time.Parse("2006-01-02", date[:10]); err == nil {
					released = !t.After(now)
				}
			}
			note := strings.TrimSpace(entry.Note)
			if note == "" && releaseType == "theatricalLimited" {
				note = "Limited"
			}
			releases = append(releases, models.Release{
				Type:     releaseType,
				Date:     date,
				Country:  countryCode,
				Note:     note,
				Source:   "tmdb",
				Released: released,
			})
		}
	}

	return releases, nil
}

func (c *tmdbClient) fetchExternalID(ctx context.Context, mediaType string, tmdbID int64) (string, error) {
	if !c.isConfigured() {
		return "", errors.New("tmdb api key not configured")
	}

	// Map "series" to "tv" for TMDB API
	apiMediaType := mediaType
	if mediaType == "series" {
		apiMediaType = "tv"
	}

	endpoint, err := url.JoinPath(tmdbBaseURL, apiMediaType, fmt.Sprintf("%d", tmdbID), "external_ids")
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}

	q := req.URL.Query()
	q.Set("api_key", c.apiKey)
	req.URL.RawQuery = q.Encode()

	resp, err := c.httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("tmdb external_ids for %s/%d failed: %s", apiMediaType, tmdbID, resp.Status)
	}

	var payload tmdbExternalIDsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}

	return strings.TrimSpace(payload.IMDBID), nil
}

func mapTMDBReleaseType(releaseType int) string {
	switch releaseType {
	case 1:
		return "premiere"
	case 2:
		return "theatricalLimited"
	case 3:
		return "theatrical"
	case 4:
		return "digital"
	case 5:
		return "physical"
	case 6:
		return "tv"
	default:
		return ""
	}
}
