package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"path"
	"sort"
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
	// Profiles: w185 = 185px wide (good for cast member photos)
	tmdbPosterSize   = "w500"
	tmdbBackdropSize = "w1280"
	tmdbProfileSize  = "w185"
)

type tmdbClient struct {
	apiKey   string
	language string
	httpc    *http.Client
	cache    *fileCache // Optional cache for expensive lookups

	// Rate limiting
	throttleMu  sync.Mutex
	lastRequest time.Time
	minInterval time.Duration
}

func newTMDBClient(apiKey, language string, httpc *http.Client, cache *fileCache) *tmdbClient {
	if httpc == nil {
		httpc = &http.Client{Timeout: 15 * time.Second}
	}
	return &tmdbClient{
		apiKey:      strings.TrimSpace(apiKey),
		language:    language,
		httpc:       httpc,
		cache:       cache,
		minInterval: 20 * time.Millisecond, // TMDB has generous rate limits
	}
}

// doGET performs an HTTP GET with rate limiting and retry with exponential backoff
func (c *tmdbClient) doGET(ctx context.Context, endpoint string, v any) error {
	var lastErr error
	backoff := 300 * time.Millisecond

	for attempt := 0; attempt < 3; attempt++ {
		// Rate limiting
		c.throttleMu.Lock()
		since := time.Since(c.lastRequest)
		if since < c.minInterval {
			time.Sleep(c.minInterval - since)
		}
		c.lastRequest = time.Now()
		c.throttleMu.Unlock()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}

		resp, err := c.httpc.Do(req)
		if err != nil {
			lastErr = err
			log.Printf("[tmdb] http error (attempt %d/3): %v", attempt+1, err)
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		// Handle rate limiting and server errors
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			resp.Body.Close()
			log.Printf("[tmdb] rate limited or server error (attempt %d/3): status %d", attempt+1, resp.StatusCode)
			lastErr = fmt.Errorf("tmdb request failed: %s", resp.Status)
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		if resp.StatusCode >= 400 {
			resp.Body.Close()
			return fmt.Errorf("tmdb request failed: %s", resp.Status)
		}

		err = json.NewDecoder(resp.Body).Decode(v)
		resp.Body.Close()
		if err != nil {
			return err
		}
		return nil
	}

	return lastErr
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

type tmdbCreditsResponse struct {
	Cast []struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Character   string `json:"character"`
		Order       int    `json:"order"`
		ProfilePath string `json:"profile_path"`
	} `json:"cast"`
}

// tmdbAggregateCreditsResponse is for TV shows using /aggregate_credits endpoint
type tmdbAggregateCreditsResponse struct {
	Cast []struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Order       int    `json:"order"`
		ProfilePath string `json:"profile_path"`
		Roles       []struct {
			Character    string `json:"character"`
			EpisodeCount int    `json:"episode_count"`
		} `json:"roles"`
	} `json:"cast"`
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

	// Build trending items (IMDB IDs are enriched separately by the service layer with caching)
	for idx, r := range payload.Results {
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

		items[idx] = models.TrendingItem{Rank: idx + 1, Title: title}
	}

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
	lang = strings.TrimSpace(strings.ReplaceAll(lang, "_", "-"))

	// Convert 3-letter ISO 639-2 codes to 2-letter ISO 639-1 codes
	if len(lang) == 3 {
		lang = iso639_2to1(lang)
	}

	if len(lang) == 2 {
		return strings.ToLower(lang) + "-US"
	}
	if len(lang) >= 5 {
		return strings.ToLower(lang[:2]) + "-" + strings.ToUpper(lang[3:])
	}
	return "en-US"
}

// iso639_2to1 converts 3-letter ISO 639-2 language codes to 2-letter ISO 639-1 codes
func iso639_2to1(code string) string {
	code = strings.ToLower(code)
	switch code {
	case "eng":
		return "en"
	case "spa":
		return "es"
	case "fra":
		return "fr"
	case "deu":
		return "de"
	case "ita":
		return "it"
	case "por":
		return "pt"
	case "jpn":
		return "ja"
	case "kor":
		return "ko"
	case "zho":
		return "zh"
	case "rus":
		return "ru"
	case "ara":
		return "ar"
	case "hin":
		return "hi"
	case "nld":
		return "nl"
	case "swe":
		return "sv"
	case "nor":
		return "no"
	case "dan":
		return "da"
	case "fin":
		return "fi"
	case "pol":
		return "pl"
	case "tur":
		return "tr"
	case "heb":
		return "he"
	case "ces":
		return "cs"
	case "hun":
		return "hu"
	case "ron":
		return "ro"
	case "tha":
		return "th"
	case "vie":
		return "vi"
	default:
		return "en"
	}
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

// calculateRoleImportance computes a score that reflects how important a role was
// for an actor, rather than just the title's global popularity.
// This helps rank lead roles in quality productions higher than cameos in popular shows.
func calculateRoleImportance(popularity, voteAverage float64, billingOrder, episodeCount, totalEpisodes int, isTV bool) float64 {
	if popularity <= 0 {
		popularity = 1.0
	}

	// Billing order weight: lower order = more prominent role
	// order 0 = 1.0, order 5 = 0.8, order 10 = 0.67, order 20 = 0.5
	billingWeight := 1.0 / (1.0 + float64(billingOrder)*0.05)

	// Quality weight based on vote average (0-10 scale)
	qualityWeight := 0.5 + (voteAverage / 20.0) // Range: 0.5 to 1.0

	if isTV {
		// For TV: use percentage of episodes appeared in
		var episodeWeight float64

		if totalEpisodes > 0 {
			// Calculate percentage of show appeared in
			percentage := float64(episodeCount) / float64(totalEpisodes)

			if percentage < 0.05 {
				// Guest appearance (<5% of show) - hard cap
				episodeWeight = 0.05
			} else {
				// Scale from 0.1 (5%) to 1.0 (50%+)
				// 5% = 0.1, 25% = 0.5, 50%+ = 1.0
				episodeWeight = math.Min(percentage*2.0, 1.0)
			}
		} else {
			// Fallback if we don't have total episodes: use absolute count
			if episodeCount <= 2 {
				episodeWeight = 0.05
			} else {
				episodeWeight = 0.1 + 0.9*math.Min(float64(episodeCount)/10.0, 1.0)
			}
		}

		return popularity * episodeWeight * billingWeight * qualityWeight
	}

	// For movies: billing order and quality matter most
	return popularity * billingWeight * qualityWeight
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

// fetchSeasonTrailers fetches trailers for a specific season of a TV show from TMDB
func (c *tmdbClient) fetchSeasonTrailers(ctx context.Context, tmdbID int64, seasonNumber int) ([]models.Trailer, error) {
	if !c.isConfigured() {
		return nil, errors.New("tmdb api key not configured")
	}

	// TMDB API: /tv/{series_id}/season/{season_number}/videos
	endpoint, err := url.JoinPath(tmdbBaseURL, "tv", fmt.Sprintf("%d", tmdbID), "season", fmt.Sprintf("%d", seasonNumber), "videos")
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
		return nil, fmt.Errorf("tmdb season videos tv/%d/season/%d failed: %s", tmdbID, seasonNumber, resp.Status)
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
			Name:         strings.TrimSpace(video.Name),
			Site:         site,
			Type:         videoType,
			Key:          strings.TrimSpace(video.Key),
			Official:     video.Official,
			PublishedAt:  strings.TrimSpace(video.PublishedAt),
			Resolution:   video.Size,
			Language:     strings.TrimSpace(video.ISO6391),
			Country:      strings.TrimSpace(video.ISO31661),
			Source:       "tmdb",
			SeasonNumber: seasonNumber,
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
		ID                  int64  `json:"id"`
		Title               string `json:"title"`
		Overview            string `json:"overview"`
		PosterPath          string `json:"poster_path"`
		BackdropPath        string `json:"backdrop_path"`
		ReleaseDate         string `json:"release_date"`
		IMDBId              string `json:"imdb_id"`
		Runtime             int    `json:"runtime"`
		BelongsToCollection *struct {
			ID           int64  `json:"id"`
			Name         string `json:"name"`
			PosterPath   string `json:"poster_path"`
			BackdropPath string `json:"backdrop_path"`
		} `json:"belongs_to_collection"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&movie); err != nil {
		return nil, err
	}

	title := &models.Title{
		ID:             fmt.Sprintf("tmdb:movie:%d", movie.ID),
		Name:           movie.Title,
		Overview:       movie.Overview,
		MediaType:      "movie",
		TMDBID:         movie.ID,
		IMDBID:         movie.IMDBId,
		RuntimeMinutes: movie.Runtime,
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
	if movie.BelongsToCollection != nil {
		title.Collection = &models.Collection{
			ID:   movie.BelongsToCollection.ID,
			Name: movie.BelongsToCollection.Name,
		}
		if poster := buildTMDBImage(movie.BelongsToCollection.PosterPath, tmdbPosterSize, "poster"); poster != nil {
			title.Collection.Poster = poster
		}
		if backdrop := buildTMDBImage(movie.BelongsToCollection.BackdropPath, tmdbBackdropSize, "backdrop"); backdrop != nil {
			title.Collection.Backdrop = backdrop
		}
	}

	return title, nil
}

// fetchCollectionDetails retrieves details of a movie collection from TMDB
// including all movies in the collection
func (c *tmdbClient) fetchCollectionDetails(ctx context.Context, collectionID int64) (*models.CollectionDetails, error) {
	if !c.isConfigured() {
		return nil, errors.New("tmdb api key not configured")
	}

	endpoint, err := url.JoinPath(tmdbBaseURL, "collection", fmt.Sprintf("%d", collectionID))
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
		return nil, fmt.Errorf("tmdb collection details failed: %s", resp.Status)
	}

	var collection struct {
		ID           int64  `json:"id"`
		Name         string `json:"name"`
		Overview     string `json:"overview"`
		PosterPath   string `json:"poster_path"`
		BackdropPath string `json:"backdrop_path"`
		Parts        []struct {
			ID           int64   `json:"id"`
			Title        string  `json:"title"`
			Overview     string  `json:"overview"`
			PosterPath   string  `json:"poster_path"`
			BackdropPath string  `json:"backdrop_path"`
			ReleaseDate  string  `json:"release_date"`
			Popularity   float64 `json:"popularity"`
		} `json:"parts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&collection); err != nil {
		return nil, err
	}

	details := &models.CollectionDetails{
		ID:       collection.ID,
		Name:     collection.Name,
		Overview: collection.Overview,
	}
	if poster := buildTMDBImage(collection.PosterPath, tmdbPosterSize, "poster"); poster != nil {
		details.Poster = poster
	}
	if backdrop := buildTMDBImage(collection.BackdropPath, tmdbBackdropSize, "backdrop"); backdrop != nil {
		details.Backdrop = backdrop
	}

	// Convert parts to Title slice, sorted by release date
	details.Movies = make([]models.Title, 0, len(collection.Parts))
	for _, part := range collection.Parts {
		title := models.Title{
			ID:        fmt.Sprintf("tmdb:movie:%d", part.ID),
			Name:      part.Title,
			Overview:  part.Overview,
			MediaType: "movie",
			TMDBID:    part.ID,
		}
		if year := parseTMDBYear(part.ReleaseDate, ""); year != 0 {
			title.Year = year
		}
		if poster := buildTMDBImage(part.PosterPath, tmdbPosterSize, "poster"); poster != nil {
			title.Poster = poster
		}
		if backdrop := buildTMDBImage(part.BackdropPath, tmdbBackdropSize, "backdrop"); backdrop != nil {
			title.Backdrop = backdrop
		}
		title.Popularity = part.Popularity
		details.Movies = append(details.Movies, title)
	}

	// Sort movies by year (release date)
	sort.Slice(details.Movies, func(i, j int) bool {
		return details.Movies[i].Year < details.Movies[j].Year
	})

	return details, nil
}

// fetchCredits retrieves cast information from TMDB for movies or TV shows
// Returns top 8 billed cast members with profile images
func (c *tmdbClient) fetchCredits(ctx context.Context, mediaType string, tmdbID int64) (*models.Credits, error) {
	if !c.isConfigured() {
		return nil, errors.New("tmdb api key not configured")
	}

	// Map "series" to "tv" for TMDB API
	apiMediaType := strings.ToLower(strings.TrimSpace(mediaType))
	if apiMediaType != "movie" {
		apiMediaType = "tv"
	}

	// For TV shows, use aggregate_credits to get all appearances across seasons
	// For movies, use regular credits
	if apiMediaType == "tv" {
		return c.fetchTVCredits(ctx, tmdbID)
	}
	return c.fetchMovieCredits(ctx, tmdbID)
}

func (c *tmdbClient) fetchMovieCredits(ctx context.Context, tmdbID int64) (*models.Credits, error) {
	endpoint, err := url.JoinPath(tmdbBaseURL, "movie", fmt.Sprintf("%d", tmdbID), "credits")
	if err != nil {
		return nil, err
	}
	endpoint = endpoint + "?api_key=" + c.apiKey
	if lang := strings.TrimSpace(c.language); lang != "" {
		endpoint = endpoint + "&language=" + normalizeLanguage(lang)
	}

	var payload tmdbCreditsResponse
	if err := c.doGET(ctx, endpoint, &payload); err != nil {
		return nil, fmt.Errorf("tmdb credits for movie/%d failed: %w", tmdbID, err)
	}

	// Limit to top 8 cast members by order
	maxCast := 8
	if len(payload.Cast) < maxCast {
		maxCast = len(payload.Cast)
	}

	cast := make([]models.CastMember, 0, maxCast)
	for i := 0; i < maxCast; i++ {
		cm := payload.Cast[i]
		member := models.CastMember{
			ID:        cm.ID,
			Name:      strings.TrimSpace(cm.Name),
			Character: strings.TrimSpace(cm.Character),
			Order:     cm.Order,
		}
		if cm.ProfilePath != "" {
			member.ProfilePath = cm.ProfilePath
			member.ProfileURL = fmt.Sprintf("%s/%s%s", tmdbImageBaseURL, tmdbProfileSize, cm.ProfilePath)
		}
		cast = append(cast, member)
	}

	return &models.Credits{Cast: cast}, nil
}

func (c *tmdbClient) fetchTVCredits(ctx context.Context, tmdbID int64) (*models.Credits, error) {
	endpoint, err := url.JoinPath(tmdbBaseURL, "tv", fmt.Sprintf("%d", tmdbID), "aggregate_credits")
	if err != nil {
		return nil, err
	}
	endpoint = endpoint + "?api_key=" + c.apiKey
	if lang := strings.TrimSpace(c.language); lang != "" {
		endpoint = endpoint + "&language=" + normalizeLanguage(lang)
	}

	var payload tmdbAggregateCreditsResponse
	if err := c.doGET(ctx, endpoint, &payload); err != nil {
		return nil, fmt.Errorf("tmdb aggregate_credits for tv/%d failed: %w", tmdbID, err)
	}

	// Limit to top 8 cast members by order
	maxCast := 8
	if len(payload.Cast) < maxCast {
		maxCast = len(payload.Cast)
	}

	cast := make([]models.CastMember, 0, maxCast)
	for i := 0; i < maxCast; i++ {
		cm := payload.Cast[i]
		// Get primary character from roles (first one with most episodes)
		character := ""
		if len(cm.Roles) > 0 {
			character = strings.TrimSpace(cm.Roles[0].Character)
		}
		member := models.CastMember{
			ID:        cm.ID,
			Name:      strings.TrimSpace(cm.Name),
			Character: character,
			Order:     cm.Order,
		}
		if cm.ProfilePath != "" {
			member.ProfilePath = cm.ProfilePath
			member.ProfileURL = fmt.Sprintf("%s/%s%s", tmdbImageBaseURL, tmdbProfileSize, cm.ProfilePath)
		}
		cast = append(cast, member)
	}

	return &models.Credits{Cast: cast}, nil
}

// fetchTVShowTotalEpisodes fetches the total number of episodes for a TV show (cached)
func (c *tmdbClient) fetchTVShowTotalEpisodes(ctx context.Context, tmdbID int64) (int, error) {
	if !c.isConfigured() {
		return 0, errors.New("tmdb api key not configured")
	}

	// Check cache first
	cacheKey := fmt.Sprintf("tv:%d:episode_count", tmdbID)
	if c.cache != nil {
		var cached int
		if ok, _ := c.cache.get(cacheKey, &cached); ok {
			return cached, nil
		}
	}

	endpoint, err := url.JoinPath(tmdbBaseURL, "tv", fmt.Sprintf("%d", tmdbID))
	if err != nil {
		return 0, err
	}
	endpoint = endpoint + "?api_key=" + c.apiKey

	var payload struct {
		NumberOfEpisodes int `json:"number_of_episodes"`
	}
	if err := c.doGET(ctx, endpoint, &payload); err != nil {
		return 0, fmt.Errorf("tmdb tv/%d failed: %w", tmdbID, err)
	}

	// Cache the result
	if c.cache != nil && payload.NumberOfEpisodes > 0 {
		c.cache.set(cacheKey, payload.NumberOfEpisodes)
	}

	return payload.NumberOfEpisodes, nil
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
	endpoint = endpoint + "?api_key=" + c.apiKey

	var payload tmdbExternalIDsResponse
	var lastErr error
	backoff := 300 * time.Millisecond

	for attempt := 0; attempt < 3; attempt++ {
		// Rate limiting
		c.throttleMu.Lock()
		since := time.Since(c.lastRequest)
		if since < c.minInterval {
			time.Sleep(c.minInterval - since)
		}
		c.lastRequest = time.Now()
		c.throttleMu.Unlock()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return "", err
		}

		resp, err := c.httpc.Do(req)
		if err != nil {
			lastErr = err
			log.Printf("[tmdb] fetchExternalID http error (attempt %d/3): %v", attempt+1, err)
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		// Handle rate limiting and server errors with retry
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			resp.Body.Close()
			log.Printf("[tmdb] fetchExternalID rate limited (attempt %d/3): status %d", attempt+1, resp.StatusCode)
			lastErr = fmt.Errorf("tmdb external_ids for %s/%d failed: %s", apiMediaType, tmdbID, resp.Status)
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		if resp.StatusCode >= 400 {
			resp.Body.Close()
			return "", fmt.Errorf("tmdb external_ids for %s/%d failed: %s", apiMediaType, tmdbID, resp.Status)
		}

		err = json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(payload.IMDBID), nil
	}

	return "", lastErr
}

// findMovieByIMDBID looks up a movie's TMDB ID using its IMDB ID
func (c *tmdbClient) findMovieByIMDBID(ctx context.Context, imdbID string) (int64, error) {
	if !c.isConfigured() {
		return 0, errors.New("tmdb api key not configured")
	}
	if imdbID == "" {
		return 0, errors.New("imdb id required")
	}

	// Ensure IMDB ID has tt prefix
	if !strings.HasPrefix(imdbID, "tt") {
		imdbID = "tt" + imdbID
	}

	endpoint := fmt.Sprintf("%s/find/%s?api_key=%s&external_source=imdb_id", tmdbBaseURL, imdbID, c.apiKey)

	var lastErr error
	backoff := 300 * time.Millisecond

	for attempt := 0; attempt < 3; attempt++ {
		// Rate limiting
		c.throttleMu.Lock()
		since := time.Since(c.lastRequest)
		if since < c.minInterval {
			time.Sleep(c.minInterval - since)
		}
		c.lastRequest = time.Now()
		c.throttleMu.Unlock()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return 0, err
		}

		resp, err := c.httpc.Do(req)
		if err != nil {
			lastErr = err
			log.Printf("[tmdb] findMovieByIMDBID http error (attempt %d/3): %v", attempt+1, err)
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			resp.Body.Close()
			log.Printf("[tmdb] findMovieByIMDBID rate limited (attempt %d/3): status %d", attempt+1, resp.StatusCode)
			lastErr = fmt.Errorf("tmdb find %s failed: %s", imdbID, resp.Status)
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		if resp.StatusCode >= 400 {
			resp.Body.Close()
			return 0, fmt.Errorf("tmdb find %s failed: %s", imdbID, resp.Status)
		}

		var result struct {
			MovieResults []struct {
				ID int64 `json:"id"`
			} `json:"movie_results"`
		}
		err = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if err != nil {
			return 0, err
		}

		if len(result.MovieResults) > 0 {
			return result.MovieResults[0].ID, nil
		}
		return 0, fmt.Errorf("no movie found for IMDB ID %s", imdbID)
	}

	return 0, lastErr
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

// tmdbSimilarResponse represents the response from TMDB's /similar endpoint
type tmdbSimilarResponse struct {
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
	} `json:"results"`
}

// fetchPersonDetails retrieves detailed information about a person from TMDB
func (c *tmdbClient) fetchPersonDetails(ctx context.Context, personID int64) (*models.Person, error) {
	if !c.isConfigured() {
		return nil, errors.New("tmdb api key not configured")
	}

	endpoint, err := url.JoinPath(tmdbBaseURL, "person", fmt.Sprintf("%d", personID))
	if err != nil {
		return nil, err
	}
	endpoint = endpoint + "?api_key=" + c.apiKey
	if lang := strings.TrimSpace(c.language); lang != "" {
		endpoint = endpoint + "&language=" + normalizeLanguage(lang)
	}

	var payload struct {
		ID                 int64  `json:"id"`
		Name               string `json:"name"`
		Biography          string `json:"biography"`
		Birthday           string `json:"birthday"`
		Deathday           string `json:"deathday"`
		PlaceOfBirth       string `json:"place_of_birth"`
		ProfilePath        string `json:"profile_path"`
		KnownForDepartment string `json:"known_for_department"`
	}
	if err := c.doGET(ctx, endpoint, &payload); err != nil {
		return nil, fmt.Errorf("tmdb person details for %d failed: %w", personID, err)
	}

	person := &models.Person{
		ID:           payload.ID,
		Name:         strings.TrimSpace(payload.Name),
		Biography:    strings.TrimSpace(payload.Biography),
		Birthday:     strings.TrimSpace(payload.Birthday),
		Deathday:     strings.TrimSpace(payload.Deathday),
		PlaceOfBirth: strings.TrimSpace(payload.PlaceOfBirth),
		KnownFor:     strings.TrimSpace(payload.KnownForDepartment),
	}
	if payload.ProfilePath != "" {
		person.ProfileURL = fmt.Sprintf("%s/%s%s", tmdbImageBaseURL, tmdbPosterSize, payload.ProfilePath)
	}

	return person, nil
}

// fetchPersonCombinedCredits retrieves all movie and TV credits for a person from TMDB
func (c *tmdbClient) fetchPersonCombinedCredits(ctx context.Context, personID int64) ([]models.Title, error) {
	if !c.isConfigured() {
		return nil, errors.New("tmdb api key not configured")
	}

	endpoint, err := url.JoinPath(tmdbBaseURL, "person", fmt.Sprintf("%d", personID), "combined_credits")
	if err != nil {
		return nil, err
	}
	endpoint = endpoint + "?api_key=" + c.apiKey
	if lang := strings.TrimSpace(c.language); lang != "" {
		endpoint = endpoint + "&language=" + normalizeLanguage(lang)
	}

	var payload struct {
		Cast []struct {
			ID               int64   `json:"id"`
			Title            string  `json:"title"`       // Movies
			Name             string  `json:"name"`        // TV shows
			Overview         string  `json:"overview"`
			PosterPath       string  `json:"poster_path"`
			BackdropPath     string  `json:"backdrop_path"`
			MediaType        string  `json:"media_type"` // "movie" or "tv"
			ReleaseDate      string  `json:"release_date"`
			FirstAirDate     string  `json:"first_air_date"`
			Popularity       float64 `json:"popularity"`
			VoteAverage      float64 `json:"vote_average"`
			Character        string  `json:"character"`
			OriginalLanguage string  `json:"original_language"`
			Order            int     `json:"order"`         // Billing order (lower = more prominent)
			EpisodeCount     int     `json:"episode_count"` // Number of episodes (TV only)
			GenreIDs         []int   `json:"genre_ids"`     // Genre IDs (10767 = Talk Show)
		} `json:"cast"`
	}
	if err := c.doGET(ctx, endpoint, &payload); err != nil {
		return nil, fmt.Errorf("tmdb person combined_credits for %d failed: %w", personID, err)
	}

	// Collect TV show IDs to fetch total episode counts
	tvShowIDs := make(map[int64]bool)
	for _, credit := range payload.Cast {
		if credit.MediaType == "tv" {
			tvShowIDs[credit.ID] = true
		}
	}

	// Fetch total episode counts for all TV shows in parallel
	tvEpisodeCounts := make(map[int64]int)
	if len(tvShowIDs) > 0 {
		var wg sync.WaitGroup
		var mu sync.Mutex
		for tvID := range tvShowIDs {
			wg.Add(1)
			go func(id int64) {
				defer wg.Done()
				total, err := c.fetchTVShowTotalEpisodes(ctx, id)
				if err == nil && total > 0 {
					mu.Lock()
					tvEpisodeCounts[id] = total
					mu.Unlock()
				}
			}(tvID)
		}
		wg.Wait()
	}

	// Convert to Title slice and calculate role importance score
	titles := make([]models.Title, 0, len(payload.Cast))
	for _, credit := range payload.Cast {
		// Skip talk shows (genre 10767) - these are typically interview appearances, not acting roles
		isTalkShow := false
		for _, gid := range credit.GenreIDs {
			if gid == 10767 {
				isTalkShow = true
				break
			}
		}
		if isTalkShow {
			continue
		}

		// Determine media type and name
		mediaType := "movie"
		name := credit.Title
		if credit.MediaType == "tv" {
			mediaType = "series"
			name = credit.Name
		}

		if name == "" {
			continue // Skip entries without a name
		}

		title := models.Title{
			ID:        fmt.Sprintf("tmdb:%s:%d", credit.MediaType, credit.ID),
			Name:      name,
			Overview:  credit.Overview,
			MediaType: mediaType,
			TMDBID:    credit.ID,
			Language:  credit.OriginalLanguage,
		}
		if year := parseTMDBYear(credit.ReleaseDate, credit.FirstAirDate); year != 0 {
			title.Year = year
		}
		if poster := buildTMDBImage(credit.PosterPath, tmdbPosterSize, "poster"); poster != nil {
			title.Poster = poster
		}
		if backdrop := buildTMDBImage(credit.BackdropPath, tmdbBackdropSize, "backdrop"); backdrop != nil {
			title.Backdrop = backdrop
		}

		// Get total episodes for TV shows (0 for movies)
		totalEpisodes := tvEpisodeCounts[credit.ID]

		// Calculate role importance score using hybrid algorithm
		// This considers: popularity, billing order, episode percentage (TV), and rating
		title.Popularity = calculateRoleImportance(
			credit.Popularity,
			credit.VoteAverage,
			credit.Order,
			credit.EpisodeCount,
			totalEpisodes,
			credit.MediaType == "tv",
		)

		// Debug logging for score calculation
		if credit.MediaType == "tv" {
			pct := 0.0
			if totalEpisodes > 0 {
				pct = float64(credit.EpisodeCount) / float64(totalEpisodes) * 100
			}
			log.Printf("[metadata] score: %q pop=%.1f order=%d ep=%d/%d (%.1f%%) -> %.1f",
				name, credit.Popularity, credit.Order, credit.EpisodeCount, totalEpisodes, pct, title.Popularity)
		} else {
			log.Printf("[metadata] score: %q pop=%.1f order=%d vote=%.1f -> %.1f",
				name, credit.Popularity, credit.Order, credit.VoteAverage, title.Popularity)
		}

		titles = append(titles, title)
	}

	// Sort by role importance score (highest first)
	sort.Slice(titles, func(i, j int) bool {
		return titles[i].Popularity > titles[j].Popularity
	})

	return titles, nil
}

// fetchSimilar retrieves similar movies or TV shows from TMDB
// Returns up to 20 similar titles
func (c *tmdbClient) fetchSimilar(ctx context.Context, mediaType string, tmdbID int64) ([]models.Title, error) {
	if !c.isConfigured() {
		return nil, errors.New("tmdb api key not configured")
	}

	// Map "series" to "tv" for TMDB API
	apiMediaType := strings.ToLower(strings.TrimSpace(mediaType))
	if apiMediaType != "movie" {
		apiMediaType = "tv"
	}

	endpoint, err := url.JoinPath(tmdbBaseURL, apiMediaType, fmt.Sprintf("%d", tmdbID), "similar")
	if err != nil {
		return nil, err
	}
	endpoint = endpoint + "?api_key=" + c.apiKey
	if lang := strings.TrimSpace(c.language); lang != "" {
		endpoint = endpoint + "&language=" + normalizeLanguage(lang)
	}

	var payload tmdbSimilarResponse
	if err := c.doGET(ctx, endpoint, &payload); err != nil {
		return nil, fmt.Errorf("tmdb similar for %s/%d failed: %w", apiMediaType, tmdbID, err)
	}

	// Convert results to Title slice
	titles := make([]models.Title, 0, len(payload.Results))
	for _, r := range payload.Results {
		// Determine the media type for the result
		resultMediaType := "movie"
		if apiMediaType == "tv" {
			resultMediaType = "series"
		}

		title := models.Title{
			ID:        fmt.Sprintf("tmdb:%s:%d", apiMediaType, r.ID),
			Name:      pickTMDBName(apiMediaType, r.Name, r.Title),
			Overview:  r.Overview,
			Language:  r.OriginalLanguage,
			MediaType: resultMediaType,
			TMDBID:    r.ID,
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
		title.Popularity = scoreFallback(r.Popularity, r.VoteAverage)

		titles = append(titles, title)
	}

	return titles, nil
}
