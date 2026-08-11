package metadata

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/internal/apiusage"
	"novastream/models"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/sync/errgroup"
)

const (
	tmdbBaseURL      = "https://api.themoviedb.org/3"
	tmdbImageBaseURL = "https://image.tmdb.org/t/p"
	// Use optimized image sizes where practical, but keep TV hero/backdrop art sharp.
	// Posters: w780 = 780px wide (enough for portrait cards and detail posters)
	// Backdrops: original (needed for large TV hero/background art)
	// Episode stills: original (used for landscape Continue Watching cards and TV focus art)
	// Profiles: w185 = 185px wide (good for cast member photos)
	tmdbPosterSize   = "w780"
	tmdbBackdropSize = "original"
	tmdbProfileSize  = "w185"
	tmdbLogoSize     = "w500"
	tmdbStillSize    = "original"

	tmdbBackdropAnalysisSize  = "w300"
	maxTMDBBackdropCandidates = 10
	maxTMDBAlternateBackdrops = 5
)

// TMDB genre ID → name maps (standard IDs that rarely change)
var tmdbMovieGenres = map[int]string{
	28: "Action", 12: "Adventure", 16: "Animation", 35: "Comedy", 80: "Crime",
	99: "Documentary", 18: "Drama", 10751: "Family", 14: "Fantasy", 36: "History",
	27: "Horror", 10402: "Music", 9648: "Mystery", 10749: "Romance", 878: "Sci-Fi",
	10770: "TV Movie", 53: "Thriller", 10752: "War", 37: "Western",
}

var tmdbTVGenres = map[int]string{
	10759: "Action & Adventure", 16: "Animation", 35: "Comedy", 80: "Crime",
	99: "Documentary", 18: "Drama", 10751: "Family", 10762: "Kids", 9648: "Mystery",
	10763: "News", 10764: "Reality", 10765: "Sci-Fi & Fantasy", 10766: "Soap",
	10767: "Talk", 10768: "War & Politics", 37: "Western",
}

var regexpTMDBImageSize = regexp.MustCompile(`/t/p/(?:original|w\d+)/`)

// resolveGenreIDs maps TMDB genre IDs to genre names.
func resolveGenreIDs(ids []int, mediaType string) []string {
	lookup := tmdbMovieGenres
	if mediaType == "tv" {
		lookup = tmdbTVGenres
	}
	var names []string
	for _, id := range ids {
		if name, ok := lookup[id]; ok {
			names = append(names, name)
		}
	}
	return names
}

// movieDetailsCacheEntry provides singleflight-style deduplication for in-flight
// movieDetails calls. Entries live only while a fetch is in progress and are
// removed once it completes — persistence is handled by the file cache.
type movieDetailsCacheEntry struct {
	result *models.Title
	err    error
	done   chan struct{}
}

type tmdbHTTPError struct {
	StatusCode int
	Status     string
}

func (e *tmdbHTTPError) Error() string {
	return "tmdb request failed: " + e.Status
}

func isTMDBNotFound(err error) bool {
	var httpErr *tmdbHTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound
}

type tmdbClient struct {
	apiKey   string
	language string
	httpc    *http.Client
	cache    *fileCache // Optional cache for expensive lookups

	// Rate limiting
	throttleMu    sync.Mutex
	lastRequest   time.Time
	minInterval   time.Duration
	cooldownUntil time.Time

	// In-flight singleflight map for movieDetails — holds only requests
	// currently being fetched (bounded), not a process-lifetime cache.
	movieCache sync.Map
}

func (c *tmdbClient) waitForRequestSlot(ctx context.Context) error {
	for {
		now := time.Now()
		c.throttleMu.Lock()
		if now.Before(c.cooldownUntil) {
			wait := time.Until(c.cooldownUntil)
			c.throttleMu.Unlock()
			if err := sleepWithContext(ctx, wait); err != nil {
				return err
			}
			continue
		}
		availableAt := c.lastRequest.Add(c.minInterval)
		if availableAt.Before(now) {
			availableAt = now
		}
		c.lastRequest = availableAt
		c.throttleMu.Unlock()

		if err := sleepWithContext(ctx, time.Until(availableAt)); err != nil {
			return err
		}

		c.throttleMu.Lock()
		coolingDown := time.Now().Before(c.cooldownUntil)
		c.throttleMu.Unlock()
		if !coolingDown {
			return nil
		}
	}
}

func tmdbRetryDelay(resp *http.Response, fallback time.Duration) time.Duration {
	if resp != nil {
		if raw := strings.TrimSpace(resp.Header.Get("Retry-After")); raw != "" {
			if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
				return time.Duration(seconds) * time.Second
			}
			if retryAt, err := http.ParseTime(raw); err == nil {
				if delay := time.Until(retryAt); delay > 0 {
					return delay
				}
			}
		}
	}
	return fallback
}

func (c *tmdbClient) beginSharedCooldown(delay time.Duration) {
	if delay <= 0 {
		return
	}
	until := time.Now().Add(delay)
	c.throttleMu.Lock()
	if until.After(c.cooldownUntil) {
		c.cooldownUntil = until
	}
	c.throttleMu.Unlock()
}

func newTMDBClient(apiKey, language string, httpc *http.Client, cache *fileCache) *tmdbClient {
	if httpc == nil {
		httpc = &http.Client{Timeout: 15 * time.Second}
	}
	httpc = apiusage.TrackClient(httpc, "TMDB", "Metadata API")
	return &tmdbClient{
		apiKey:      strings.TrimSpace(apiKey),
		language:    language,
		httpc:       httpc,
		cache:       cache,
		minInterval: 5 * time.Millisecond, // TMDB has generous rate limits; retries back off on 429/5xx.
	}
}

// doGET performs an HTTP GET with rate limiting and retry with exponential backoff
func (c *tmdbClient) doGET(ctx context.Context, endpoint string, v any) error {
	var lastErr error
	backoff := 300 * time.Millisecond

	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := c.waitForRequestSlot(ctx); err != nil {
			return err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}

		resp, err := c.httpc.Do(req)
		if err != nil {
			lastErr = err
			log.Printf("[tmdb] http error (attempt %d/3): %v", attempt+1, err)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if err := sleepWithContext(ctx, backoff); err != nil {
				return err
			}
			backoff *= 2
			continue
		}

		// Handle rate limiting and server errors
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			retryDelay := tmdbRetryDelay(resp, backoff)
			resp.Body.Close()
			log.Printf("[tmdb] rate limited or server error (attempt %d/3): status %d", attempt+1, resp.StatusCode)
			lastErr = &tmdbHTTPError{StatusCode: resp.StatusCode, Status: resp.Status}
			if resp.StatusCode == http.StatusTooManyRequests {
				c.beginSharedCooldown(retryDelay)
			}
			if err := sleepWithContext(ctx, retryDelay); err != nil {
				return err
			}
			backoff *= 2
			continue
		}

		if resp.StatusCode >= 400 {
			resp.Body.Close()
			return &tmdbHTTPError{StatusCode: resp.StatusCode, Status: resp.Status}
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

func sleepWithContext(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *tmdbClient) isConfigured() bool {
	return c != nil && c.apiKey != ""
}

type tmdbExternalIDsResponse struct {
	IMDBID      string `json:"imdb_id"`
	TVDBID      int64  `json:"tvdb_id"`
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
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

// tmdbImageItem represents a single image from TMDB's /images endpoint
type tmdbImageItem struct {
	FilePath    string  `json:"file_path"`
	AspectRatio float64 `json:"aspect_ratio"`
	Height      int     `json:"height"`
	Width       int     `json:"width"`
	VoteAverage float64 `json:"vote_average"`
	ISO6391     string  `json:"iso_639_1"`
}

// tmdbImagesResponse represents the response from TMDB's /images endpoint
type tmdbImagesResponse struct {
	Backdrops []tmdbImageItem `json:"backdrops"`
	Logos     []tmdbImageItem `json:"logos"`
	Posters   []tmdbImageItem `json:"posters"`
}

// tmdbImagesResult contains logo plus clean/text variants for posters and backdrops.
type tmdbImagesResult struct {
	Logo             *models.Image
	TextlessPoster   *models.Image
	TextPoster       *models.Image // Best poster with title text (has language tag)
	Posters          []models.Image
	TextlessBackdrop *models.Image
	TextBackdrop     *models.Image // Best backdrop with language tag when available
	Backdrops        []models.Image
}

// fetchImages retrieves logo and textless poster for a movie or TV show from TMDB
// Uses a single API call to get both, improving efficiency
func (c *tmdbClient) fetchImages(ctx context.Context, mediaType string, tmdbID int64) (*tmdbImagesResult, error) {
	if !c.isConfigured() {
		return nil, errors.New("tmdb api key not configured")
	}

	// Map "series" to "tv" for TMDB API
	apiMediaType := strings.ToLower(strings.TrimSpace(mediaType))
	if apiMediaType != "movie" {
		apiMediaType = "tv"
	}

	endpoint, err := url.JoinPath(tmdbBaseURL, apiMediaType, fmt.Sprintf("%d", tmdbID), "images")
	if err != nil {
		return nil, err
	}
	// Don't filter logos server-side — TMDB's include_image_language returns 0 results
	// for many shows. Fetch all logos then filter client-side by language preference.
	preferredLang := c.logoLanguage()
	endpoint = endpoint + "?api_key=" + c.apiKey

	var payload tmdbImagesResponse
	if err := c.doGET(ctx, endpoint, &payload); err != nil {
		return nil, fmt.Errorf("tmdb images for %s/%d failed: %w", apiMediaType, tmdbID, err)
	}

	result := &tmdbImagesResult{}

	// Find best logo: prefer user's language, then English, then no-language.
	// Skip logos in other languages to avoid showing translated text.
	if len(payload.Logos) > 0 {
		if selectedLogo, ok := c.selectLogoCandidate(ctx, payload.Logos, preferredLang); ok {
			result.Logo = buildTMDBImage(selectedLogo.FilePath, tmdbLogoSize, "logo")
			if result.Logo != nil {
				result.Logo.Language = selectedLogo.ISO6391
				result.Logo.IsFallbackLanguage = selectedLogo.ISO6391 != preferredLang
				result.Logo.IsDark = c.isImageDark(ctx, result.Logo.URL)
			}
		}
	}

	if len(payload.Backdrops) > 0 {
		var textless []tmdbImageItem
		var withText []tmdbImageItem
		for _, b := range payload.Backdrops {
			if b.ISO6391 == "" {
				textless = append(textless, b)
			} else if b.ISO6391 == preferredLang || b.ISO6391 == "en" {
				withText = append(withText, b)
			}
		}
		if len(textless) > 0 {
			sort.Slice(textless, func(i, j int) bool {
				return textless[i].VoteAverage > textless[j].VoteAverage
			})
			result.TextlessBackdrop = buildTMDBImage(textless[0].FilePath, tmdbBackdropSize, "backdrop")
			if result.TextlessBackdrop != nil {
				result.TextlessBackdrop.IsTextless = true
			}
		}
		if len(withText) > 0 {
			sort.Slice(withText, func(i, j int) bool {
				if preferredLang != "" && preferredLang != "en" {
					iPref := withText[i].ISO6391 == preferredLang
					jPref := withText[j].ISO6391 == preferredLang
					if iPref != jPref {
						return iPref
					}
				}
				iEng := withText[i].ISO6391 == "en"
				jEng := withText[j].ISO6391 == "en"
				if iEng != jEng {
					return iEng
				}
				return withText[i].VoteAverage > withText[j].VoteAverage
			})
			result.TextBackdrop = buildTMDBImage(withText[0].FilePath, tmdbBackdropSize, "backdrop")
			if result.TextBackdrop != nil {
				result.TextBackdrop.Language = withText[0].ISO6391
				result.TextBackdrop.IsFallbackLanguage = withText[0].ISO6391 != preferredLang
			}
		}
		result.Backdrops = c.rankAlternateBackdrops(ctx, textless, result.TextlessBackdrop, result.TextBackdrop)
	}

	// Find best textless poster (no language = textless) and best text poster (has language tag)
	if len(payload.Posters) > 0 {
		var textless []tmdbImageItem
		var withText []tmdbImageItem
		for _, p := range payload.Posters {
			if p.ISO6391 == "" {
				textless = append(textless, p)
			} else if p.ISO6391 == preferredLang || p.ISO6391 == "en" {
				withText = append(withText, p)
			}
		}
		if len(textless) > 0 {
			// Sort by vote average (highest first)
			sort.Slice(textless, func(i, j int) bool {
				return textless[i].VoteAverage > textless[j].VoteAverage
			})
			result.TextlessPoster = buildTMDBImage(textless[0].FilePath, tmdbPosterSize, "poster")
			if result.TextlessPoster != nil {
				result.TextlessPoster.IsTextless = true
			}
		}
		if len(withText) > 0 {
			// Prefer user's language, then English, then highest vote average
			sort.Slice(withText, func(i, j int) bool {
				if preferredLang != "" && preferredLang != "en" {
					iPref := withText[i].ISO6391 == preferredLang
					jPref := withText[j].ISO6391 == preferredLang
					if iPref != jPref {
						return iPref
					}
				}
				iEng := withText[i].ISO6391 == "en"
				jEng := withText[j].ISO6391 == "en"
				if iEng != jEng {
					return iEng
				}
				return withText[i].VoteAverage > withText[j].VoteAverage
			})
			result.TextPoster = buildTMDBImage(withText[0].FilePath, tmdbPosterSize, "poster")
			if result.TextPoster != nil {
				result.TextPoster.Language = withText[0].ISO6391
				result.TextPoster.IsFallbackLanguage = withText[0].ISO6391 != preferredLang
			}
		}
		result.Posters = rankAlternatePosters(payload.Posters, result.TextlessPoster, preferredLang)
	}

	return result, nil
}

func rankAlternatePosters(items []tmdbImageItem, primary *models.Image, preferredLang string) []models.Image {
	const maxAlternatePosters = 7
	primaryKey := ""
	if primary != nil {
		primaryKey = comparableTMDBImageURL(primary.URL)
	}

	usable := make([]tmdbImageItem, 0, len(items))
	for _, item := range items {
		if logoLanguageRank(item, preferredLang) >= 0 {
			usable = append(usable, item)
		}
	}
	sort.SliceStable(usable, func(i, j int) bool {
		iRank := logoLanguageRank(usable[i], preferredLang)
		jRank := logoLanguageRank(usable[j], preferredLang)
		if iRank != jRank {
			return iRank < jRank
		}
		return usable[i].VoteAverage > usable[j].VoteAverage
	})
	seen := make(map[string]struct{})
	result := make([]models.Image, 0, maxAlternatePosters)
	for _, item := range usable {
		img := buildTMDBImage(item.FilePath, tmdbPosterSize, "poster")
		if img == nil {
			continue
		}
		key := comparableTMDBImageURL(img.URL)
		if key == "" || key == primaryKey {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		img.Language = item.ISO6391
		img.IsTextless = item.ISO6391 == ""
		img.IsFallbackLanguage = item.ISO6391 != "" && item.ISO6391 != preferredLang
		result = append(result, *img)
		if len(result) == maxAlternatePosters {
			break
		}
	}
	return result
}

func (c *tmdbClient) selectLogoCandidate(ctx context.Context, logos []tmdbImageItem, preferredLang string) (tmdbImageItem, bool) {
	return selectLogoCandidate(logos, preferredLang, func(item tmdbImageItem) bool {
		return c.isWhiteOnlySVGLogo(ctx, item)
	})
}

func selectLogoCandidate(logos []tmdbImageItem, preferredLang string, isWhiteOnly func(tmdbImageItem) bool) (tmdbImageItem, bool) {
	var usable []tmdbImageItem
	for _, l := range logos {
		if logoLanguageRank(l, preferredLang) >= 0 {
			usable = append(usable, l)
		}
	}
	if len(usable) == 0 {
		return tmdbImageItem{}, false
	}

	sort.Slice(usable, func(i, j int) bool {
		li, lj := usable[i], usable[j]
		iRank := logoLanguageRank(li, preferredLang)
		jRank := logoLanguageRank(lj, preferredLang)
		if iRank != jRank {
			return iRank < jRank
		}
		return li.VoteAverage > lj.VoteAverage
	})

	selected := usable[0]
	if !isWhiteOnly(selected) {
		return selected, true
	}

	selectedRank := logoLanguageRank(selected, preferredLang)
	for _, candidate := range usable[1:] {
		if logoLanguageRank(candidate, preferredLang) != selectedRank {
			break
		}
		if !isWhiteOnly(candidate) {
			log.Printf("[metadata] logo selection: skipped white-only svg %s in favor of %s", selected.FilePath, candidate.FilePath)
			return candidate, true
		}
	}

	return selected, true
}

func logoLanguageRank(item tmdbImageItem, preferredLang string) int {
	if preferredLang != "" && preferredLang != "en" && item.ISO6391 == preferredLang {
		return 0
	}
	if item.ISO6391 == "en" {
		return 1
	}
	if item.ISO6391 == "" {
		return 2
	}
	return -1
}

func (c *tmdbClient) isWhiteOnlySVGLogo(ctx context.Context, item tmdbImageItem) bool {
	if !strings.HasSuffix(strings.ToLower(item.FilePath), ".svg") {
		return false
	}
	img := buildTMDBImage(item.FilePath, tmdbLogoSize, "logo")
	if img == nil {
		return false
	}
	return c.isWhiteOnlySVGURL(ctx, img.URL)
}

func (c *tmdbClient) isWhiteOnlySVGURL(ctx context.Context, imageURL string) bool {
	if !strings.HasSuffix(strings.ToLower(strings.Split(imageURL, "?")[0]), ".svg") {
		return false
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		log.Printf("[metadata] logo svg color: failed to create request: %v", err)
		return false
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		log.Printf("[metadata] logo svg color: fetch failed: %v", err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[metadata] logo svg color: fetch returned %d", resp.StatusCode)
		return false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		log.Printf("[metadata] logo svg color: read failed: %v", err)
		return false
	}
	return isWhiteOnlySVGXML(string(body))
}

func isWhiteOnlySVGXML(svg string) bool {
	fillMatches := regexp.MustCompile(`(?i)\bfill\s*[:=]\s*["']?\s*(#[0-9a-f]{3,8}|white|rgb\(\s*255\s*,\s*255\s*,\s*255\s*\))`).FindAllStringSubmatch(svg, -1)
	if len(fillMatches) == 0 {
		return false
	}

	hasWhiteFill := false
	for _, match := range fillMatches {
		if len(match) < 2 {
			continue
		}
		color := strings.ToLower(strings.TrimSpace(match[1]))
		if color == "#fff" || color == "#ffffff" || color == "#ffffffff" || color == "white" || strings.HasPrefix(color, "rgb(") {
			hasWhiteFill = true
			continue
		}
		if color != "none" {
			return false
		}
	}
	return hasWhiteFill
}

type backdropVisualSignature struct {
	dHash uint64
	rgb   []float64
	gray  []float64
	edge  []float64
	crops []backdropCropSignature
}

type backdropCropSignature struct {
	rgb  []float64
	gray []float64
	edge []float64
	hist []float64
}

type rankedBackdropCandidate struct {
	image *models.Image
	score float64
}

func (c *tmdbClient) rankAlternateBackdrops(ctx context.Context, items []tmdbImageItem, primary, textBackdrop *models.Image) []models.Image {
	if len(items) == 0 {
		return nil
	}

	primaryKey := comparableTMDBImageURL("")
	if primary != nil {
		primaryKey = comparableTMDBImageURL(primary.URL)
	}
	textKey := ""
	if textBackdrop != nil {
		textKey = comparableTMDBImageURL(textBackdrop.URL)
	}

	seen := make(map[string]struct{})
	var candidates []tmdbImageItem
	for _, item := range items {
		img := buildTMDBImage(item.FilePath, tmdbBackdropSize, "backdrop")
		if img == nil {
			continue
		}
		key := comparableTMDBImageURL(img.URL)
		if key == "" {
			continue
		}
		if key == primaryKey || key == textKey {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		candidates = append(candidates, item)
	}
	if len(candidates) == 0 {
		return nil
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].VoteAverage > candidates[j].VoteAverage
	})
	if len(candidates) > maxTMDBBackdropCandidates {
		candidates = candidates[:maxTMDBBackdropCandidates]
	}

	var primarySig *backdropVisualSignature
	if primary != nil {
		if sig, err := c.fetchBackdropVisualSignature(ctx, primary.URL); err == nil {
			primarySig = sig
		} else {
			log.Printf("[metadata] backdrop visual signature: primary fetch failed: %v", err)
		}
	}

	ranked := make([]rankedBackdropCandidate, 0, len(candidates))
	for _, item := range candidates {
		img := buildTMDBImage(item.FilePath, tmdbBackdropSize, "backdrop")
		if img == nil {
			continue
		}
		img.Language = item.ISO6391
		img.IsTextless = item.ISO6391 == ""
		score := item.VoteAverage * 0.25
		if primarySig != nil {
			if sig, err := c.fetchBackdropVisualSignature(ctx, img.URL); err == nil {
				score += backdropVisualDiversityScore(primarySig, sig)
			} else {
				log.Printf("[metadata] backdrop visual signature: candidate fetch failed path=%s err=%v", item.FilePath, err)
			}
		}
		ranked = append(ranked, rankedBackdropCandidate{image: img, score: score})
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].score > ranked[j].score
	})

	limit := maxTMDBAlternateBackdrops
	if len(ranked) < limit {
		limit = len(ranked)
	}
	result := make([]models.Image, 0, limit)
	for i := 0; i < limit; i++ {
		result = append(result, *ranked[i].image)
	}
	return result
}

func (c *tmdbClient) fetchBackdropVisualSignature(ctx context.Context, imageURL string) (*backdropVisualSignature, error) {
	analysisURL := strings.Replace(imageURL, "/original/", "/"+tmdbBackdropAnalysisSize+"/", 1)
	if analysisURL == imageURL {
		analysisURL = strings.Replace(imageURL, "/w780/", "/"+tmdbBackdropAnalysisSize+"/", 1)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, analysisURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch returned %d", resp.StatusCode)
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(io.LimitReader(resp.Body, 2*1024*1024)); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, err
	}
	return computeBackdropVisualSignature(img), nil
}

func computeBackdropVisualSignature(img image.Image) *backdropVisualSignature {
	return &backdropVisualSignature{
		dHash: computeDHash(img),
		rgb:   sampleRGBLayout(img, 16, 9),
		gray:  sampleGrayLayout(img, 32, 18),
		edge:  sampleEdgeLayout(img, 32, 18),
		crops: sampleBackdropCropSignatures(img),
	}
}

func backdropVisualDiversityScore(primary, candidate *backdropVisualSignature) float64 {
	if primary == nil || candidate == nil {
		return 0
	}
	dHashDistance := float64(bitsSet64(primary.dHash ^ candidate.dHash))
	colorRMSE := normalizedRMSE(primary.rgb, candidate.rgb)
	grayCorr := correlation(primary.gray, candidate.gray)
	edgeCorr := correlation(primary.edge, candidate.edge)

	score := colorRMSE*70 + (1-grayCorr)*18 + (1-edgeCorr)*8 + (dHashDistance/64)*10
	if dHashDistance <= 10 || (grayCorr >= 0.72 && edgeCorr >= 0.45 && colorRMSE <= 0.42) {
		score -= 80
	}
	if cropAlignedDuplicateScore(primary, candidate) >= 28 {
		score -= 80
	}
	return score
}

func cropAlignedDuplicateScore(primary, candidate *backdropVisualSignature) float64 {
	if primary == nil || candidate == nil || len(primary.crops) == 0 || len(candidate.crops) == 0 {
		return 0
	}
	best := 0.0
	for _, a := range primary.crops {
		for _, b := range candidate.crops {
			grayCorr := correlation(a.gray, b.gray)
			edgeCorr := correlation(a.edge, b.edge)
			colorRMSE := normalizedRMSE(a.rgb, b.rgb)
			histSimilarity := histogramIntersection(a.hist, b.hist)
			score := grayCorr*35 + edgeCorr*15 + histSimilarity*20 - colorRMSE*25
			if score > best {
				best = score
			}
		}
	}
	return best
}

func computeDHash(img image.Image) uint64 {
	gray := resizeGray(img, 9, 8)
	var hash uint64
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			hash <<= 1
			if gray[y*9+x] > gray[y*9+x+1] {
				hash |= 1
			}
		}
	}
	return hash
}

func sampleRGBLayout(img image.Image, width, height int) []float64 {
	return sampleRGBLayoutRect(img, img.Bounds(), width, height)
}

func sampleRGBLayoutRect(img image.Image, src image.Rectangle, width, height int) []float64 {
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, src, xdraw.Over, nil)
	out := make([]float64, 0, width*height*3)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r, g, b, _ := dst.At(x, y).RGBA()
			out = append(out, float64(r>>8)/255, float64(g>>8)/255, float64(b>>8)/255)
		}
	}
	return out
}

func sampleGrayLayout(img image.Image, width, height int) []float64 {
	gray := resizeGrayRect(img, img.Bounds(), width, height)
	out := make([]float64, len(gray))
	for i, v := range gray {
		out[i] = float64(v) / 255
	}
	return out
}

func sampleEdgeLayout(img image.Image, width, height int) []float64 {
	return sampleEdgeLayoutRect(img, img.Bounds(), width, height)
}

func sampleEdgeLayoutRect(img image.Image, src image.Rectangle, width, height int) []float64 {
	gray := resizeGrayRect(img, src, width, height)
	out := make([]float64, len(gray))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			left := gray[y*width+maxInt(0, x-1)]
			right := gray[y*width+minInt(width-1, x+1)]
			up := gray[maxInt(0, y-1)*width+x]
			down := gray[minInt(height-1, y+1)*width+x]
			dx := int(right) - int(left)
			dy := int(down) - int(up)
			out[y*width+x] = math.Min(1, math.Sqrt(float64(dx*dx+dy*dy))/255)
		}
	}
	return out
}

func resizeGray(img image.Image, width, height int) []uint8 {
	return resizeGrayRect(img, img.Bounds(), width, height)
}

func resizeGrayRect(img image.Image, src image.Rectangle, width, height int) []uint8 {
	dst := image.NewGray(image.Rect(0, 0, width, height))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, src, xdraw.Over, nil)
	out := make([]uint8, width*height)
	copy(out, dst.Pix[:width*height])
	return out
}

func sampleBackdropCropSignatures(img image.Image) []backdropCropSignature {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil
	}
	rects := []image.Rectangle{
		bounds,
		image.Rect(bounds.Min.X+width*8/100, bounds.Min.Y+height*8/100, bounds.Min.X+width*92/100, bounds.Min.Y+height*92/100),
		image.Rect(bounds.Min.X, bounds.Min.Y+height*8/100, bounds.Min.X+width*84/100, bounds.Min.Y+height*92/100),
		image.Rect(bounds.Min.X+width*16/100, bounds.Min.Y+height*8/100, bounds.Min.X+width, bounds.Min.Y+height*92/100),
		image.Rect(bounds.Min.X+width*8/100, bounds.Min.Y, bounds.Min.X+width*92/100, bounds.Min.Y+height*84/100),
		image.Rect(bounds.Min.X+width*8/100, bounds.Min.Y+height*16/100, bounds.Min.X+width*92/100, bounds.Min.Y+height),
		image.Rect(bounds.Min.X+width*16/100, bounds.Min.Y, bounds.Min.X+width*84/100, bounds.Min.Y+height),
	}
	out := make([]backdropCropSignature, 0, len(rects))
	for _, rect := range rects {
		rect = rect.Intersect(bounds)
		if rect.Dx() <= 0 || rect.Dy() <= 0 {
			continue
		}
		out = append(out, backdropCropSignature{
			rgb:  sampleRGBLayoutRect(img, rect, 64, 36),
			gray: sampleGrayLayoutRect(img, rect, 64, 36),
			edge: sampleEdgeLayoutRect(img, rect, 64, 36),
			hist: sampleRGBHistogramRect(img, rect, 16),
		})
	}
	return out
}

func sampleGrayLayoutRect(img image.Image, src image.Rectangle, width, height int) []float64 {
	gray := resizeGrayRect(img, src, width, height)
	out := make([]float64, len(gray))
	for i, v := range gray {
		out[i] = float64(v) / 255
	}
	return out
}

func sampleRGBHistogramRect(img image.Image, src image.Rectangle, bins int) []float64 {
	if bins <= 0 {
		return nil
	}
	dst := image.NewRGBA(image.Rect(0, 0, 64, 36))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, src, xdraw.Over, nil)
	out := make([]float64, bins*3)
	total := 0.0
	for y := 0; y < dst.Bounds().Dy(); y++ {
		for x := 0; x < dst.Bounds().Dx(); x++ {
			r, g, b, _ := dst.At(x, y).RGBA()
			out[minInt(bins-1, int((r>>8)*uint32(bins)/256))]++
			out[bins+minInt(bins-1, int((g>>8)*uint32(bins)/256))]++
			out[bins*2+minInt(bins-1, int((b>>8)*uint32(bins)/256))]++
			total += 3
		}
	}
	if total == 0 {
		return out
	}
	for i := range out {
		out[i] /= total
	}
	return out
}

func histogramIntersection(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	total := 0.0
	for i := range a {
		total += math.Min(a[i], b[i])
	}
	return total
}

func normalizedRMSE(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 1
	}
	var total float64
	for i := range a {
		diff := a[i] - b[i]
		total += diff * diff
	}
	return math.Sqrt(total / float64(len(a)))
}

func correlation(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var meanA, meanB float64
	for i := range a {
		meanA += a[i]
		meanB += b[i]
	}
	meanA /= float64(len(a))
	meanB /= float64(len(b))

	var num, denA, denB float64
	for i := range a {
		da := a[i] - meanA
		db := b[i] - meanB
		num += da * db
		denA += da * da
		denB += db * db
	}
	if denA == 0 || denB == 0 {
		return 0
	}
	return num / math.Sqrt(denA*denB)
}

func bitsSet64(v uint64) int {
	count := 0
	for v != 0 {
		v &= v - 1
		count++
	}
	return count
}

func comparableTMDBImageURL(imageURL string) string {
	if imageURL == "" {
		return ""
	}
	withoutQuery := strings.Split(imageURL, "?")[0]
	return regexpTMDBImageSize.ReplaceAllString(withoutQuery, "/")
}

// isImageDark fetches a small thumbnail of the logo and checks if the non-transparent
// pixels are predominantly dark (average luminance < 50/255). Used to detect black
// logos on transparent backgrounds that need tinting on dark UI.
//
// Only returns true for logos that are actually cutouts on a transparent background.
// Logos with solid/opaque backgrounds (>70% opaque pixels) are left untinted even if
// dark, since they have built-in contrast (e.g. white text on a dark banner).
func (c *tmdbClient) isImageDark(ctx context.Context, imageURL string) bool {
	// Use w92 thumbnail for analysis — tiny and fast to download
	analysisURL := strings.Replace(imageURL, "/w500/", "/w92/", 1)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, analysisURL, nil)
	if err != nil {
		log.Printf("[metadata] logo brightness: failed to create request: %v", err)
		return false
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		log.Printf("[metadata] logo brightness: fetch failed: %v", err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[metadata] logo brightness: fetch returned %d", resp.StatusCode)
		return false
	}

	img, _, err := image.Decode(resp.Body)
	if err != nil {
		log.Printf("[metadata] logo brightness: decode failed: %v", err)
		return false
	}

	bounds := img.Bounds()
	totalPixels := (bounds.Max.X - bounds.Min.X) * (bounds.Max.Y - bounds.Min.Y)
	var totalLuminance float64
	var opaquePixels int

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			// Skip transparent pixels (alpha < 128 in 8-bit = 32768 in 16-bit)
			if a < 32768 {
				continue
			}
			// Convert from 16-bit [0,65535] to 8-bit [0,255] for luminance calc
			r8 := float64(r) / 257.0
			g8 := float64(g) / 257.0
			b8 := float64(b) / 257.0
			// Handle pre-multiplied alpha: un-premultiply
			if a < 65535 {
				alpha := float64(a) / 65535.0
				r8 /= alpha
				g8 /= alpha
				b8 /= alpha
			}
			totalLuminance += 0.299*r8 + 0.587*g8 + 0.114*b8
			opaquePixels++
		}
	}

	if opaquePixels == 0 {
		log.Printf("[metadata] logo brightness: no opaque pixels")
		return false
	}

	avgLuminance := totalLuminance / float64(opaquePixels)
	isDark := avgLuminance < 50

	// If the image is mostly opaque (>70%), it has a solid background with built-in
	// contrast (e.g. Jury Duty: white text on dark banner). Tinting these destroys
	// the logo, so skip the dark flag.
	opaqueRatio := float64(opaquePixels) / float64(totalPixels)
	if isDark && opaqueRatio > 0.70 {
		log.Printf("[metadata] logo brightness: avg=%.1f opaque=%d (%.0f%%) — solid background, skipping dark flag url=%s", avgLuminance, opaquePixels, opaqueRatio*100, analysisURL)
		return false
	}

	log.Printf("[metadata] logo brightness: avg=%.1f opaque=%d (%.0f%%) dark=%v url=%s", avgLuminance, opaquePixels, opaqueRatio*100, isDark, analysisURL)
	return isDark
}

// fetchSeriesGenres retrieves genres for a TV series from TMDB
func (c *tmdbClient) fetchSeriesGenres(ctx context.Context, tmdbID int64) ([]string, error) {
	if !c.isConfigured() {
		return nil, errors.New("tmdb api key not configured")
	}

	endpoint, err := url.JoinPath(tmdbBaseURL, "tv", fmt.Sprintf("%d", tmdbID))
	if err != nil {
		return nil, err
	}
	endpoint = endpoint + "?api_key=" + c.apiKey

	var payload struct {
		Genres []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"genres"`
	}
	if err := c.doGET(ctx, endpoint, &payload); err != nil {
		return nil, fmt.Errorf("tmdb tv/%d failed: %w", tmdbID, err)
	}

	var genres []string
	for _, g := range payload.Genres {
		if g.Name != "" {
			genres = append(genres, g.Name)
		}
	}
	return genres, nil
}

func (c *tmdbClient) seriesDetails(ctx context.Context, tmdbID int64) (*models.Title, error) {
	if !c.isConfigured() {
		return nil, errors.New("tmdb api key not configured")
	}
	if tmdbID <= 0 {
		return nil, errors.New("tmdb id required")
	}

	endpoint, err := url.JoinPath(tmdbBaseURL, "tv", fmt.Sprintf("%d", tmdbID))
	if err != nil {
		return nil, err
	}
	endpoint = endpoint + "?api_key=" + c.apiKey + "&append_to_response=external_ids"
	if lang := strings.TrimSpace(c.language); lang != "" {
		endpoint += "&language=" + normalizeLanguage(lang)
	}

	var payload struct {
		ID               int64    `json:"id"`
		Name             string   `json:"name"`
		OriginalName     string   `json:"original_name"`
		Overview         string   `json:"overview"`
		OriginalLanguage string   `json:"original_language"`
		FirstAirDate     string   `json:"first_air_date"`
		PosterPath       string   `json:"poster_path"`
		BackdropPath     string   `json:"backdrop_path"`
		Status           string   `json:"status"`
		Popularity       float64  `json:"popularity"`
		VoteAverage      float64  `json:"vote_average"`
		OriginCountry    []string `json:"origin_country"`
		Genres           []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"genres"`
		Networks []struct {
			Name string `json:"name"`
		} `json:"networks"`
		ExternalIDs tmdbExternalIDsResponse `json:"external_ids"`
	}

	if err := c.doGET(ctx, endpoint, &payload); err != nil {
		return nil, fmt.Errorf("tmdb tv/%d failed: %w", tmdbID, err)
	}

	title := &models.Title{
		ID:              fmt.Sprintf("tmdb:tv:%d", tmdbID),
		Name:            strings.TrimSpace(payload.Name),
		OriginalName:    strings.TrimSpace(payload.OriginalName),
		Overview:        strings.TrimSpace(payload.Overview),
		Language:        strings.TrimSpace(payload.OriginalLanguage),
		MediaType:       "series",
		TMDBID:          tmdbID,
		IMDBID:          strings.TrimSpace(payload.ExternalIDs.IMDBID),
		TVDBID:          payload.ExternalIDs.TVDBID,
		Status:          models.SeriesReleaseStatusFromDate(payload.FirstAirDate),
		LifecycleStatus: strings.TrimSpace(payload.Status),
		Popularity:      scoreFallback(payload.Popularity, payload.VoteAverage),
	}
	if title.Name == "" {
		title.Name = title.OriginalName
	}
	if year := parseTMDBYear("", payload.FirstAirDate); year != 0 {
		title.Year = year
	}
	if poster := buildTMDBImage(payload.PosterPath, tmdbPosterSize, "poster"); poster != nil {
		title.Poster = poster
	}
	if backdrop := buildTMDBImage(payload.BackdropPath, tmdbBackdropSize, "backdrop"); backdrop != nil {
		title.Backdrop = backdrop
	}
	for _, genre := range payload.Genres {
		if name := strings.TrimSpace(genre.Name); name != "" {
			title.Genres = append(title.Genres, name)
		}
	}
	if len(payload.Networks) > 0 {
		title.Network = strings.TrimSpace(payload.Networks[0].Name)
	}
	if len(payload.OriginCountry) > 0 {
		title.CountryCode = strings.TrimSpace(payload.OriginCountry[0])
	}
	return title, nil
}

func (c *tmdbClient) seriesDetailsWithSeasons(ctx context.Context, tmdbID int64) (*models.SeriesDetails, error) {
	title, err := c.seriesDetails(ctx, tmdbID)
	if err != nil {
		return nil, err
	}
	if title == nil {
		return nil, errors.New("tmdb returned nil series")
	}

	summaries, err := c.seriesSeasonSummaries(ctx, tmdbID)
	if err != nil {
		return nil, err
	}

	seasons := make([]models.SeriesSeason, 0, len(summaries))
	for _, summary := range summaries {
		if summary.Number < 0 {
			continue
		}
		season, err := c.seriesSeasonDetails(ctx, tmdbID, summary)
		if err != nil {
			log.Printf("[tmdb] season details failed tv/%d season/%d: %v", tmdbID, summary.Number, err)
			seasons = append(seasons, summary.toModel(tmdbID))
			continue
		}
		seasons = append(seasons, season)
	}

	sort.Slice(seasons, func(i, j int) bool {
		return seasons[i].Number < seasons[j].Number
	})

	title.Status = models.SeriesReleaseStatusFromSeasons(seasons)
	return &models.SeriesDetails{
		Title:               *title,
		Seasons:             seasons,
		EpisodeTMDBEnriched: true,
	}, nil
}

type tmdbSeasonSummary struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Overview     string `json:"overview"`
	Number       int    `json:"season_number"`
	EpisodeCount int    `json:"episode_count"`
	AirDate      string `json:"air_date"`
	PosterPath   string `json:"poster_path"`
}

func (s tmdbSeasonSummary) toModel(tmdbID int64) models.SeriesSeason {
	season := models.SeriesSeason{
		ID:           fmt.Sprintf("tmdb:tv:%d:season:%d", tmdbID, s.Number),
		Name:         strings.TrimSpace(s.Name),
		Number:       s.Number,
		Overview:     strings.TrimSpace(s.Overview),
		EpisodeCount: s.EpisodeCount,
		Episodes:     []models.SeriesEpisode{},
	}
	if season.Name == "" {
		if s.Number == 0 {
			season.Name = "Specials"
		} else {
			season.Name = fmt.Sprintf("Season %d", s.Number)
		}
	}
	if poster := buildTMDBImage(s.PosterPath, tmdbPosterSize, "poster"); poster != nil {
		season.Image = poster
	}
	return season
}

func (c *tmdbClient) seriesSeasonSummaries(ctx context.Context, tmdbID int64) ([]tmdbSeasonSummary, error) {
	if !c.isConfigured() {
		return nil, errors.New("tmdb api key not configured")
	}
	endpoint, err := url.JoinPath(tmdbBaseURL, "tv", fmt.Sprintf("%d", tmdbID))
	if err != nil {
		return nil, err
	}
	endpoint = endpoint + "?api_key=" + c.apiKey
	if lang := strings.TrimSpace(c.language); lang != "" {
		endpoint += "&language=" + normalizeLanguage(lang)
	}

	var payload struct {
		Seasons []tmdbSeasonSummary `json:"seasons"`
	}
	if err := c.doGET(ctx, endpoint, &payload); err != nil {
		return nil, fmt.Errorf("tmdb tv/%d seasons failed: %w", tmdbID, err)
	}
	return payload.Seasons, nil
}

func (c *tmdbClient) seriesSeasonDetails(ctx context.Context, tmdbID int64, summary tmdbSeasonSummary) (models.SeriesSeason, error) {
	if !c.isConfigured() {
		return models.SeriesSeason{}, errors.New("tmdb api key not configured")
	}
	endpoint, err := url.JoinPath(tmdbBaseURL, "tv", fmt.Sprintf("%d", tmdbID), "season", fmt.Sprintf("%d", summary.Number))
	if err != nil {
		return models.SeriesSeason{}, err
	}
	endpoint = endpoint + "?api_key=" + c.apiKey
	if lang := strings.TrimSpace(c.language); lang != "" {
		endpoint += "&language=" + normalizeLanguage(lang)
	}

	var payload struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		Overview   string `json:"overview"`
		Number     int    `json:"season_number"`
		AirDate    string `json:"air_date"`
		PosterPath string `json:"poster_path"`
		Episodes   []struct {
			ID            int64  `json:"id"`
			Name          string `json:"name"`
			Overview      string `json:"overview"`
			SeasonNumber  int    `json:"season_number"`
			EpisodeNumber int    `json:"episode_number"`
			AirDate       string `json:"air_date"`
			Runtime       int    `json:"runtime"`
			StillPath     string `json:"still_path"`
		} `json:"episodes"`
	}
	if err := c.doGET(ctx, endpoint, &payload); err != nil {
		return models.SeriesSeason{}, fmt.Errorf("tmdb tv/%d season/%d failed: %w", tmdbID, summary.Number, err)
	}

	season := summary.toModel(tmdbID)
	seasonID := payload.ID
	if seasonID == 0 {
		seasonID = summary.ID
	}
	if seasonID > 0 {
		season.ID = fmt.Sprintf("tmdb:season:%d", seasonID)
	}
	if name := strings.TrimSpace(payload.Name); name != "" {
		season.Name = name
	}
	if overview := strings.TrimSpace(payload.Overview); overview != "" {
		season.Overview = overview
	}
	if payload.Number >= 0 {
		season.Number = payload.Number
	}
	if poster := buildTMDBImage(payload.PosterPath, tmdbPosterSize, "poster"); poster != nil {
		season.Image = poster
	}

	episodes := make([]models.SeriesEpisode, 0, len(payload.Episodes))
	for _, ep := range payload.Episodes {
		seasonNumber := ep.SeasonNumber
		if seasonNumber == 0 && season.Number != 0 {
			seasonNumber = season.Number
		}
		episodeNumber := ep.EpisodeNumber
		episodeID := fmt.Sprintf("tmdb:episode:%d", ep.ID)
		if ep.ID == 0 {
			episodeID = fmt.Sprintf("tmdb:tv:%d:s%02de%02d", tmdbID, seasonNumber, episodeNumber)
		}
		episode := models.SeriesEpisode{
			ID:                episodeID,
			TMDBID:            ep.ID,
			TMDBSeasonNumber:  seasonNumber,
			TMDBEpisodeNumber: episodeNumber,
			Name:              strings.TrimSpace(ep.Name),
			Overview:          strings.TrimSpace(ep.Overview),
			SeasonNumber:      seasonNumber,
			EpisodeNumber:     episodeNumber,
			AiredDate:         strings.TrimSpace(ep.AirDate),
			Runtime:           ep.Runtime,
		}
		if episode.Name == "" && episodeNumber > 0 {
			episode.Name = fmt.Sprintf("Episode %d", episodeNumber)
		}
		if still := buildTMDBImage(ep.StillPath, tmdbStillSize, "still"); still != nil {
			episode.Image = still
		}
		episodes = append(episodes, episode)
	}
	sort.Slice(episodes, func(i, j int) bool {
		if episodes[i].SeasonNumber != episodes[j].SeasonNumber {
			return episodes[i].SeasonNumber < episodes[j].SeasonNumber
		}
		return episodes[i].EpisodeNumber < episodes[j].EpisodeNumber
	})
	season.Episodes = episodes
	if len(episodes) > season.EpisodeCount {
		season.EpisodeCount = len(episodes)
	}
	return season, nil
}

func (c *tmdbClient) searchTitles(ctx context.Context, query, mediaType string, limit int, includeAdult bool) ([]models.SearchResult, error) {
	if !c.isConfigured() {
		return nil, errors.New("tmdb api key not configured")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return []models.SearchResult{}, nil
	}
	if limit <= 0 {
		limit = 20
	}

	apiMediaType := strings.ToLower(strings.TrimSpace(mediaType))
	if apiMediaType == "series" {
		apiMediaType = "tv"
	}
	if apiMediaType != "movie" && apiMediaType != "tv" {
		apiMediaType = "multi"
	}

	endpoint := fmt.Sprintf("%s/search/%s?api_key=%s&query=%s&include_adult=%t",
		tmdbBaseURL, apiMediaType, c.apiKey, url.QueryEscape(query), includeAdult)
	if lang := strings.TrimSpace(c.language); lang != "" {
		endpoint += "&language=" + normalizeLanguage(lang)
	}

	var payload struct {
		Results []struct {
			ID               int64   `json:"id"`
			Name             string  `json:"name"`
			Title            string  `json:"title"`
			OriginalName     string  `json:"original_name"`
			OriginalTitle    string  `json:"original_title"`
			MediaType        string  `json:"media_type"`
			Overview         string  `json:"overview"`
			OriginalLanguage string  `json:"original_language"`
			PosterPath       string  `json:"poster_path"`
			BackdropPath     string  `json:"backdrop_path"`
			ReleaseDate      string  `json:"release_date"`
			FirstAirDate     string  `json:"first_air_date"`
			Popularity       float64 `json:"popularity"`
			VoteAverage      float64 `json:"vote_average"`
			VoteCount        int     `json:"vote_count"`
			Adult            bool    `json:"adult"`
		} `json:"results"`
	}
	if err := c.doGET(ctx, endpoint, &payload); err != nil {
		return nil, fmt.Errorf("tmdb search for %q failed: %w", query, err)
	}

	results := make([]models.SearchResult, 0, minInt(limit, len(payload.Results)))
	for _, r := range payload.Results {
		if len(results) >= limit {
			break
		}
		resultMediaType := "movie"
		resultAPIType := apiMediaType
		if resultAPIType == "multi" {
			resultAPIType = strings.ToLower(strings.TrimSpace(r.MediaType))
		}
		switch resultAPIType {
		case "tv":
			resultMediaType = "series"
		case "movie":
			resultMediaType = "movie"
		default:
			continue
		}

		title := models.Title{
			ID:           fmt.Sprintf("tmdb:%s:%d", resultAPIType, r.ID),
			Name:         pickTMDBName(resultAPIType, r.Name, r.Title),
			OriginalName: strings.TrimSpace(firstNonEmpty(r.OriginalName, r.OriginalTitle)),
			Overview:     strings.TrimSpace(r.Overview),
			Language:     strings.TrimSpace(r.OriginalLanguage),
			MediaType:    resultMediaType,
			TMDBID:       r.ID,
			Popularity:   scoreFallback(r.Popularity, r.VoteAverage),
			VoteCount:    r.VoteCount,
			Adult:        r.Adult,
		}
		if title.Name == "" {
			title.Name = title.OriginalName
		}
		if title.Name == "" {
			continue
		}
		if year := parseTMDBYear(r.ReleaseDate, r.FirstAirDate); year != 0 {
			title.Year = year
		}
		if resultMediaType == "movie" {
			title.Status = models.MovieReleaseStatusFromReleaseDate(r.ReleaseDate)
		} else if resultMediaType == "series" {
			title.Status = models.SeriesReleaseStatusFromDate(r.FirstAirDate)
		}
		if poster := buildTMDBImage(r.PosterPath, tmdbPosterSize, "poster"); poster != nil {
			title.Poster = poster
		}
		if backdrop := buildTMDBImage(r.BackdropPath, tmdbBackdropSize, "backdrop"); backdrop != nil {
			title.Backdrop = backdrop
		}

		score := int(math.Round(searchRankScore(title.Popularity, title.VoteCount)))
		if score <= 0 {
			score = len(payload.Results) - len(results)
		}
		results = append(results, models.SearchResult{Title: title, Score: score})
	}
	return results, nil
}

func searchRankScore(popularity float64, voteCount int) float64 {
	score := popularity
	if voteCount > 0 {
		score += math.Log1p(float64(voteCount)) * 8
	}
	return score
}

// logoLanguage returns the 2-letter ISO 639-1 language code for logo filtering.
// Handles inputs like "en", "eng", "en-US", "pt-BR", etc.
func (c *tmdbClient) logoLanguage() string {
	lang := strings.TrimSpace(c.language)
	if lang == "" {
		return "en"
	}
	lang = strings.ReplaceAll(lang, "_", "-")
	// Convert 3-letter codes first
	if len(lang) == 3 {
		return iso639_2to1(lang)
	}
	// Extract 2-letter prefix from "en-US" style
	if len(lang) >= 2 {
		return strings.ToLower(lang[:2])
	}
	return "en"
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
// movieDetails fetches movie details from TMDB including poster and backdrop.
// Results are cached in-memory with singleflight deduplication — concurrent calls
// for the same TMDB ID share one HTTP request.
func (c *tmdbClient) movieDetails(ctx context.Context, tmdbID int64) (*models.Title, error) {
	if !c.isConfigured() {
		return nil, errors.New("tmdb api key not configured")
	}

	// Persistent file cache — survives restarts and is scoped per language,
	// unlike the in-memory singleflight map below.
	var cacheID string
	if c.cache != nil {
		cacheID = cacheKey("tmdb", "movie", "details", "v1", c.language, fmt.Sprintf("%d", tmdbID))
		var cached models.Title
		if ok, _ := c.cache.get(cacheID, &cached); ok && cached.TMDBID > 0 {
			return &cached, nil
		}
	}

	// Singleflight: dedupe concurrent fetches for the same id. The entry is
	// removed once the fetch completes so the map only ever holds in-flight
	// requests (bounded), not a process-lifetime cache.
	entry := &movieDetailsCacheEntry{done: make(chan struct{})}
	if existing, loaded := c.movieCache.LoadOrStore(tmdbID, entry); loaded {
		e := existing.(*movieDetailsCacheEntry)
		<-e.done
		return e.result, e.err
	}
	// We won the race — fetch and populate
	result, err := c.movieDetailsFetch(ctx, tmdbID)
	if err == nil && result != nil && cacheID != "" {
		_ = c.cache.set(cacheID, result)
	}
	entry.result = result
	entry.err = err
	close(entry.done)
	c.movieCache.Delete(tmdbID)
	return result, err
}

func (c *tmdbClient) movieDetailsFetch(ctx context.Context, tmdbID int64) (*models.Title, error) {

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
		OriginalTitle       string `json:"original_title"`
		OriginalLanguage    string `json:"original_language"`
		Overview            string `json:"overview"`
		PosterPath          string `json:"poster_path"`
		BackdropPath        string `json:"backdrop_path"`
		ReleaseDate         string `json:"release_date"`
		IMDBId              string `json:"imdb_id"`
		Runtime             int    `json:"runtime"`
		ProductionCountries []struct {
			Code string `json:"iso_3166_1"`
		} `json:"production_countries"`
		Genres []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"genres"`
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
	if originalTitle := strings.TrimSpace(movie.OriginalTitle); originalTitle != "" && !strings.EqualFold(originalTitle, movie.Title) {
		title.OriginalName = originalTitle
	}
	if originalLanguage := strings.TrimSpace(movie.OriginalLanguage); originalLanguage != "" {
		title.Language = originalLanguage
	}
	if len(movie.ProductionCountries) > 0 {
		title.CountryCode = strings.TrimSpace(movie.ProductionCountries[0].Code)
	}

	if year := parseTMDBYear(movie.ReleaseDate, ""); year != 0 {
		title.Year = year
	}
	title.Status = models.MovieReleaseStatusFromReleaseDate(movie.ReleaseDate)
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

	// Extract genre names
	for _, g := range movie.Genres {
		if g.Name != "" {
			title.Genres = append(title.Genres, g.Name)
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
		title.Status = models.MovieReleaseStatusFromReleaseDate(part.ReleaseDate)
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

// movieReleaseDatesResult contains releases and the US certification
type movieReleaseDatesResult struct {
	Releases      []models.Release
	Certification string // US MPAA rating (G, PG, PG-13, R, NC-17)
}

func (c *tmdbClient) movieReleaseDates(ctx context.Context, tmdbID int64) ([]models.Release, error) {
	result, err := c.movieReleaseDatesWithCert(ctx, tmdbID)
	if err != nil {
		return nil, err
	}
	return result.Releases, nil
}

func (c *tmdbClient) movieReleaseDatesWithCert(ctx context.Context, tmdbID int64) (*movieReleaseDatesResult, error) {
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
	var usCertification string

	for _, country := range payload.Results {
		countryCode := strings.TrimSpace(country.ISO31661)

		// Extract US certification (prefer theatrical releases for rating)
		if countryCode == "US" && usCertification == "" {
			for _, entry := range country.ReleaseDates {
				cert := strings.TrimSpace(entry.Certification)
				if cert != "" {
					usCertification = cert
					break
				}
			}
		}

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

	return &movieReleaseDatesResult{
		Releases:      releases,
		Certification: usCertification,
	}, nil
}

// fetchTVContentRating fetches the US TV content rating for a TV show
func (c *tmdbClient) fetchTVContentRating(ctx context.Context, tmdbID int64) (string, error) {
	if !c.isConfigured() {
		return "", errors.New("tmdb api key not configured")
	}

	endpoint, err := url.JoinPath(tmdbBaseURL, "tv", fmt.Sprintf("%d", tmdbID), "content_ratings")
	if err != nil {
		return "", err
	}
	endpoint = endpoint + "?api_key=" + c.apiKey

	var payload struct {
		Results []struct {
			ISO31661 string `json:"iso_3166_1"`
			Rating   string `json:"rating"`
		} `json:"results"`
	}

	if err := c.doGET(ctx, endpoint, &payload); err != nil {
		return "", fmt.Errorf("tmdb tv/%d content_ratings failed: %w", tmdbID, err)
	}

	// Find US rating
	for _, r := range payload.Results {
		if r.ISO31661 == "US" {
			return strings.TrimSpace(r.Rating), nil
		}
	}

	return "", nil
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
		if err := c.waitForRequestSlot(ctx); err != nil {
			return "", err
		}

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
			retryDelay := tmdbRetryDelay(resp, backoff)
			resp.Body.Close()
			log.Printf("[tmdb] fetchExternalID rate limited (attempt %d/3): status %d", attempt+1, resp.StatusCode)
			lastErr = &tmdbHTTPError{StatusCode: resp.StatusCode, Status: resp.Status}
			if resp.StatusCode == http.StatusTooManyRequests {
				c.beginSharedCooldown(retryDelay)
			}
			if err := sleepWithContext(ctx, retryDelay); err != nil {
				return "", err
			}
			backoff *= 2
			continue
		}

		if resp.StatusCode >= 400 {
			resp.Body.Close()
			return "", &tmdbHTTPError{StatusCode: resp.StatusCode, Status: resp.Status}
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
		// Don't retry if context is already canceled
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}

		if err := c.waitForRequestSlot(ctx); err != nil {
			return 0, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return 0, err
		}

		resp, err := c.httpc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err() // context canceled — don't retry
			}
			lastErr = err
			log.Printf("[tmdb] findMovieByIMDBID http error (attempt %d/3): %v", attempt+1, err)
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			retryDelay := tmdbRetryDelay(resp, backoff)
			resp.Body.Close()
			log.Printf("[tmdb] findMovieByIMDBID rate limited (attempt %d/3): status %d", attempt+1, resp.StatusCode)
			lastErr = &tmdbHTTPError{StatusCode: resp.StatusCode, Status: resp.Status}
			if resp.StatusCode == http.StatusTooManyRequests {
				c.beginSharedCooldown(retryDelay)
			}
			if err := sleepWithContext(ctx, retryDelay); err != nil {
				return 0, err
			}
			backoff *= 2
			continue
		}

		if resp.StatusCode >= 400 {
			resp.Body.Close()
			return 0, &tmdbHTTPError{StatusCode: resp.StatusCode, Status: resp.Status}
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

// findTVByIMDBID looks up a TV show's TMDB ID using its IMDB ID
func (c *tmdbClient) findTVByIMDBID(ctx context.Context, imdbID string) (int64, error) {
	if !c.isConfigured() {
		return 0, errors.New("tmdb api key not configured")
	}
	if imdbID == "" {
		return 0, errors.New("imdb id required")
	}
	if !strings.HasPrefix(imdbID, "tt") {
		imdbID = "tt" + imdbID
	}

	endpoint := fmt.Sprintf("%s/find/%s?api_key=%s&external_source=imdb_id", tmdbBaseURL, imdbID, c.apiKey)

	var lastErr error
	backoff := 300 * time.Millisecond

	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}

		if err := c.waitForRequestSlot(ctx); err != nil {
			return 0, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return 0, err
		}

		resp, err := c.httpc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			lastErr = err
			log.Printf("[tmdb] findTVByIMDBID http error (attempt %d/3): %v", attempt+1, err)
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			retryDelay := tmdbRetryDelay(resp, backoff)
			resp.Body.Close()
			log.Printf("[tmdb] findTVByIMDBID rate limited (attempt %d/3): status %d", attempt+1, resp.StatusCode)
			lastErr = &tmdbHTTPError{StatusCode: resp.StatusCode, Status: resp.Status}
			if resp.StatusCode == http.StatusTooManyRequests {
				c.beginSharedCooldown(retryDelay)
			}
			if err := sleepWithContext(ctx, retryDelay); err != nil {
				return 0, err
			}
			backoff *= 2
			continue
		}

		if resp.StatusCode >= 400 {
			resp.Body.Close()
			return 0, &tmdbHTTPError{StatusCode: resp.StatusCode, Status: resp.Status}
		}

		var result struct {
			TVResults []struct {
				ID int64 `json:"id"`
			} `json:"tv_results"`
		}
		err = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if err != nil {
			return 0, err
		}

		if len(result.TVResults) > 0 {
			return result.TVResults[0].ID, nil
		}
		return 0, fmt.Errorf("no TV show found for IMDB ID %s", imdbID)
	}

	return 0, lastErr
}

// findTVByTVDBID looks up a TV show's TMDB id from its TVDB id.
func (c *tmdbClient) findTVByTVDBID(ctx context.Context, tvdbID string) (int64, error) {
	if !c.isConfigured() {
		return 0, errors.New("tmdb api key not configured")
	}
	tvdbID = strings.TrimSpace(tvdbID)
	if tvdbID == "" {
		return 0, errors.New("tvdb id required")
	}

	endpoint := fmt.Sprintf("%s/find/%s?api_key=%s&external_source=tvdb_id",
		tmdbBaseURL, url.PathEscape(tvdbID), c.apiKey)
	var payload struct {
		TVResults []struct {
			ID int64 `json:"id"`
		} `json:"tv_results"`
	}
	if err := c.doGET(ctx, endpoint, &payload); err != nil {
		return 0, err
	}
	if len(payload.TVResults) == 0 {
		return 0, fmt.Errorf("no TV show found for TVDB ID %s", tvdbID)
	}
	return payload.TVResults[0].ID, nil
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
			Title            string  `json:"title"` // Movies
			Name             string  `json:"name"`  // TV shows
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

	// Deduplicate credits by show/movie ID - TMDB returns separate entries for different roles
	// in the same production (e.g., multiple characters in American Dad, different SNL appearances)
	type creditKey struct {
		ID        int64
		MediaType string
	}
	creditMap := make(map[creditKey]struct {
		ID               int64
		Title            string
		Name             string
		Overview         string
		PosterPath       string
		BackdropPath     string
		MediaType        string
		ReleaseDate      string
		FirstAirDate     string
		Popularity       float64
		VoteAverage      float64
		Character        string
		OriginalLanguage string
		Order            int
		EpisodeCount     int
		GenreIDs         []int
	})

	for _, credit := range payload.Cast {
		key := creditKey{ID: credit.ID, MediaType: credit.MediaType}
		if existing, ok := creditMap[key]; ok {
			// Merge: keep best order (lowest), sum episode counts, keep highest popularity
			if credit.Order < existing.Order {
				existing.Order = credit.Order
			}
			// Sum episode counts (different roles may have different episode appearances)
			existing.EpisodeCount += credit.EpisodeCount
			// Keep highest popularity
			if credit.Popularity > existing.Popularity {
				existing.Popularity = credit.Popularity
			}
			// Keep highest vote average
			if credit.VoteAverage > existing.VoteAverage {
				existing.VoteAverage = credit.VoteAverage
			}
			creditMap[key] = existing
		} else {
			creditMap[key] = struct {
				ID               int64
				Title            string
				Name             string
				Overview         string
				PosterPath       string
				BackdropPath     string
				MediaType        string
				ReleaseDate      string
				FirstAirDate     string
				Popularity       float64
				VoteAverage      float64
				Character        string
				OriginalLanguage string
				Order            int
				EpisodeCount     int
				GenreIDs         []int
			}{
				ID:               credit.ID,
				Title:            credit.Title,
				Name:             credit.Name,
				Overview:         credit.Overview,
				PosterPath:       credit.PosterPath,
				BackdropPath:     credit.BackdropPath,
				MediaType:        credit.MediaType,
				ReleaseDate:      credit.ReleaseDate,
				FirstAirDate:     credit.FirstAirDate,
				Popularity:       credit.Popularity,
				VoteAverage:      credit.VoteAverage,
				Character:        credit.Character,
				OriginalLanguage: credit.OriginalLanguage,
				Order:            credit.Order,
				EpisodeCount:     credit.EpisodeCount,
				GenreIDs:         credit.GenreIDs,
			}
		}
	}

	// Convert map back to slice for processing
	deduplicatedCast := make([]struct {
		ID               int64
		Title            string
		Name             string
		Overview         string
		PosterPath       string
		BackdropPath     string
		MediaType        string
		ReleaseDate      string
		FirstAirDate     string
		Popularity       float64
		VoteAverage      float64
		Character        string
		OriginalLanguage string
		Order            int
		EpisodeCount     int
		GenreIDs         []int
	}, 0, len(creditMap))
	for _, credit := range creditMap {
		deduplicatedCast = append(deduplicatedCast, credit)
	}
	log.Printf("[metadata] person credits deduplicated: %d -> %d entries", len(payload.Cast), len(deduplicatedCast))

	// Collect TV show IDs to fetch total episode counts
	tvShowIDs := make(map[int64]bool)
	for _, credit := range deduplicatedCast {
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
	titles := make([]models.Title, 0, len(deduplicatedCast))
	for _, credit := range deduplicatedCast {
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
		if mediaType == "movie" {
			title.Status = models.MovieReleaseStatusFromReleaseDate(credit.ReleaseDate)
		} else if mediaType == "series" {
			title.Status = models.SeriesReleaseStatusFromDate(credit.FirstAirDate)
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

// titleSeedInfo holds the genre IDs, keyword IDs, original language, and year
// of a title, used to build discover queries for similar content.
type titleSeedInfo struct {
	GenreIDs         []int64
	KeywordIDs       []int64
	OriginalLanguage string // ISO 639-1, e.g. "en", "ja"
	Year             int
}

// fetchTitleSeedInfo fetches genre IDs, keyword IDs, original language, and
// year for a title in parallel. Used by the custom recommendations engine.
func (c *tmdbClient) fetchTitleSeedInfo(ctx context.Context, mediaType string, tmdbID int64) (titleSeedInfo, error) {
	if !c.isConfigured() {
		return titleSeedInfo{}, errors.New("tmdb api key not configured")
	}

	apiMediaType := strings.ToLower(strings.TrimSpace(mediaType))
	if apiMediaType != "movie" {
		apiMediaType = "tv"
	}

	type detailsResult struct {
		genres   []int64
		origLang string
		year     int
		err      error
	}
	type kwResult struct {
		keywords []int64
		err      error
	}
	detailsCh := make(chan detailsResult, 1)
	kwCh := make(chan kwResult, 1)

	// Fetch genres + original language + year from base details
	go func() {
		endpoint, e := url.JoinPath(tmdbBaseURL, apiMediaType, fmt.Sprintf("%d", tmdbID))
		if e != nil {
			detailsCh <- detailsResult{err: e}
			return
		}
		endpoint = endpoint + "?api_key=" + c.apiKey
		var payload struct {
			Genres []struct {
				ID int64 `json:"id"`
			} `json:"genres"`
			OriginalLanguage string `json:"original_language"`
			FirstAirDate     string `json:"first_air_date"`
			ReleaseDate      string `json:"release_date"`
		}
		if e := c.doGET(ctx, endpoint, &payload); e != nil {
			detailsCh <- detailsResult{err: e}
			return
		}
		ids := make([]int64, 0, len(payload.Genres))
		for _, g := range payload.Genres {
			ids = append(ids, g.ID)
		}
		detailsCh <- detailsResult{
			genres:   ids,
			origLang: payload.OriginalLanguage,
			year:     parseTMDBYear(payload.ReleaseDate, payload.FirstAirDate),
		}
	}()

	// Fetch keywords
	go func() {
		endpoint, e := url.JoinPath(tmdbBaseURL, apiMediaType, fmt.Sprintf("%d", tmdbID), "keywords")
		if e != nil {
			kwCh <- kwResult{err: e}
			return
		}
		endpoint = endpoint + "?api_key=" + c.apiKey
		// TMDB returns "keywords" for movies, "results" for TV
		var payload struct {
			Keywords []struct {
				ID int64 `json:"id"`
			} `json:"keywords"`
			Results []struct {
				ID int64 `json:"id"`
			} `json:"results"`
		}
		if e := c.doGET(ctx, endpoint, &payload); e != nil {
			kwCh <- kwResult{err: e}
			return
		}
		combined := append(payload.Keywords, payload.Results...)
		ids := make([]int64, 0, len(combined))
		for _, k := range combined {
			ids = append(ids, k.ID)
		}
		kwCh <- kwResult{keywords: ids}
	}()

	dr := <-detailsCh
	kr := <-kwCh
	if dr.err != nil && kr.err != nil {
		return titleSeedInfo{}, fmt.Errorf("details: %w; keywords: %v", dr.err, kr.err)
	}
	return titleSeedInfo{
		GenreIDs:         dr.genres,
		KeywordIDs:       kr.keywords,
		OriginalLanguage: dr.origLang,
		Year:             dr.year,
	}, nil
}

// discoverSimilarOpts controls optional filters for the discover query.
type discoverSimilarOpts struct {
	KeywordIDs       []int64
	OriginalLanguage string // filter to same original language (e.g. "en")
	YearFrom         int    // e.g. 1984
	YearTo           int    // e.g. 2000
}

// discoverSimilar uses TMDB's /discover endpoint with genre and keyword filters
// to find content similar to a seed title. Returns up to 20 titles.
func (c *tmdbClient) discoverSimilar(ctx context.Context, mediaType string, genreIDs []int64, excludeTMDBID int64, opts discoverSimilarOpts) ([]models.Title, error) {
	if !c.isConfigured() {
		return nil, errors.New("tmdb api key not configured")
	}

	apiMediaType := strings.ToLower(strings.TrimSpace(mediaType))
	if apiMediaType != "movie" {
		apiMediaType = "tv"
	}

	// Build genre filter — use at most 2 genres (AND logic) to avoid overly narrow results
	var genreParts []string
	for i, id := range genreIDs {
		if i >= 2 {
			break
		}
		genreParts = append(genreParts, fmt.Sprintf("%d", id))
	}

	// Build keyword filter — OR logic (pipe separated)
	var kwParts []string
	for _, id := range opts.KeywordIDs {
		kwParts = append(kwParts, fmt.Sprintf("%d", id))
	}

	endpoint := fmt.Sprintf("%s/discover/%s?api_key=%s&sort_by=vote_average.desc&vote_count.gte=50&page=1",
		tmdbBaseURL, apiMediaType, c.apiKey)
	if len(genreParts) > 0 {
		endpoint += "&with_genres=" + strings.Join(genreParts, ",")
	}
	if len(kwParts) > 0 {
		endpoint += "&with_keywords=" + strings.Join(kwParts, "|")
	}
	if opts.OriginalLanguage != "" {
		endpoint += "&with_original_language=" + url.QueryEscape(opts.OriginalLanguage)
	}
	dateField := "first_air_date"
	if apiMediaType == "movie" {
		dateField = "primary_release_date"
	}
	if opts.YearFrom > 0 {
		endpoint += fmt.Sprintf("&%s.gte=%d-01-01", dateField, opts.YearFrom)
	}
	if opts.YearTo > 0 {
		endpoint += fmt.Sprintf("&%s.lte=%d-12-31", dateField, opts.YearTo)
	}
	if lang := strings.TrimSpace(c.language); lang != "" {
		endpoint += "&language=" + normalizeLanguage(lang)
	}

	var payload struct {
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
			GenreIDs         []int   `json:"genre_ids"`
		} `json:"results"`
	}
	if err := c.doGET(ctx, endpoint, &payload); err != nil {
		return nil, fmt.Errorf("tmdb discover similar for %s failed: %w", apiMediaType, err)
	}

	titles := make([]models.Title, 0, len(payload.Results))
	resultMediaType := "movie"
	if apiMediaType == "tv" {
		resultMediaType = "series"
	}
	for _, r := range payload.Results {
		if r.ID == excludeTMDBID {
			continue
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
		if resultMediaType == "movie" {
			title.Status = models.MovieReleaseStatusFromReleaseDate(r.ReleaseDate)
		} else if resultMediaType == "series" {
			title.Status = models.SeriesReleaseStatusFromDate(r.FirstAirDate)
		}
		if poster := buildTMDBImage(r.PosterPath, tmdbPosterSize, "poster"); poster != nil {
			title.Poster = poster
		}
		if backdrop := buildTMDBImage(r.BackdropPath, tmdbBackdropSize, "backdrop"); backdrop != nil {
			title.Backdrop = backdrop
		}
		title.Popularity = scoreFallback(r.Popularity, r.VoteAverage)
		if genres := resolveGenreIDs(r.GenreIDs, apiMediaType); len(genres) > 0 {
			title.Genres = genres
		}
		titles = append(titles, title)
		if len(titles) >= 20 {
			break
		}
	}

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
	originalLanguage := tmdbOriginalLanguageFilter(c.language)

	var payload tmdbSimilarResponse
	if err := c.doGET(ctx, endpoint, &payload); err != nil {
		return nil, fmt.Errorf("tmdb similar for %s/%d failed: %w", apiMediaType, tmdbID, err)
	}

	// Convert results to Title slice
	titles := make([]models.Title, 0, len(payload.Results))
	for _, r := range payload.Results {
		if originalLanguage != "" && !tmdbOriginalLanguageMatches(r.OriginalLanguage, originalLanguage) {
			continue
		}
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
		if resultMediaType == "movie" {
			title.Status = models.MovieReleaseStatusFromReleaseDate(r.ReleaseDate)
		} else if resultMediaType == "series" {
			title.Status = models.SeriesReleaseStatusFromDate(r.FirstAirDate)
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

// searchByTitle searches TMDB for a movie or TV show by title and optional year.
// Returns the best matching Title or nil if no match found.
func (c *tmdbClient) searchByTitle(ctx context.Context, title string, year int, mediaType string) (*models.Title, error) {
	if !c.isConfigured() {
		return nil, errors.New("tmdb api key not configured")
	}

	apiMediaType := strings.ToLower(strings.TrimSpace(mediaType))
	if apiMediaType != "movie" && apiMediaType != "tv" {
		apiMediaType = "multi"
	}
	if apiMediaType == "series" {
		apiMediaType = "tv"
	}

	endpoint := fmt.Sprintf("%s/search/%s?api_key=%s&query=%s",
		tmdbBaseURL, apiMediaType, c.apiKey, url.QueryEscape(title))
	if year > 0 {
		if apiMediaType == "movie" {
			endpoint += fmt.Sprintf("&year=%d", year)
		} else {
			endpoint += fmt.Sprintf("&first_air_date_year=%d", year)
		}
	}
	if lang := strings.TrimSpace(c.language); lang != "" {
		endpoint += "&language=" + normalizeLanguage(lang)
	}

	var payload struct {
		Results []struct {
			ID           int64   `json:"id"`
			Name         string  `json:"name"`
			Title        string  `json:"title"`
			MediaType    string  `json:"media_type"`
			Overview     string  `json:"overview"`
			PosterPath   string  `json:"poster_path"`
			BackdropPath string  `json:"backdrop_path"`
			ReleaseDate  string  `json:"release_date"`
			FirstAirDate string  `json:"first_air_date"`
			Popularity   float64 `json:"popularity"`
			VoteAverage  float64 `json:"vote_average"`
		} `json:"results"`
	}

	if err := c.doGET(ctx, endpoint, &payload); err != nil {
		return nil, fmt.Errorf("tmdb search for %q failed: %w", title, err)
	}

	if len(payload.Results) == 0 {
		return nil, nil
	}

	r := payload.Results[0]

	// Determine result media type
	resultMediaType := "movie"
	if apiMediaType == "tv" || r.MediaType == "tv" {
		resultMediaType = "series"
	}

	result := &models.Title{
		ID:        fmt.Sprintf("tmdb:%s:%d", apiMediaType, r.ID),
		Name:      pickTMDBName(apiMediaType, r.Name, r.Title),
		Overview:  r.Overview,
		MediaType: resultMediaType,
		TMDBID:    r.ID,
	}
	if y := parseTMDBYear(r.ReleaseDate, r.FirstAirDate); y != 0 {
		result.Year = y
	}
	if poster := buildTMDBImage(r.PosterPath, tmdbPosterSize, "poster"); poster != nil {
		result.Poster = poster
	}
	if backdrop := buildTMDBImage(r.BackdropPath, tmdbBackdropSize, "backdrop"); backdrop != nil {
		result.Backdrop = backdrop
	}
	result.Popularity = scoreFallback(r.Popularity, r.VoteAverage)

	return result, nil
}

// discoverByGenre fetches movies or TV shows for a given genre from TMDB discover API
func (c *tmdbClient) discoverByGenre(ctx context.Context, mediaType string, genreID int64, page int, sortBy, direction string) ([]models.Title, int, error) {
	return c.discoverTitles(ctx, mediaType, fmt.Sprintf("&with_genres=%d", genreID), fmt.Sprintf("genre genreId=%d", genreID), page, sortBy, direction)
}

// discoverByDecade fetches movies or TV shows released within a decade (e.g. 1980 → 1980-1989)
// from the TMDB discover API. A vote-count floor keeps current-popularity spikes on obscure
// titles from drowning out the well-known releases of older decades.
func (c *tmdbClient) discoverByDecade(ctx context.Context, mediaType string, decadeStart int, page int, sortBy, direction string) ([]models.Title, int, error) {
	apiMediaType := strings.ToLower(strings.TrimSpace(mediaType))
	dateField := "primary_release_date"
	minVotes := 300
	if apiMediaType != "movie" {
		dateField = "first_air_date"
		minVotes = 50
	}
	filter := fmt.Sprintf("&%s.gte=%d-01-01&%s.lte=%d-12-31&vote_count.gte=%d", dateField, decadeStart, dateField, decadeStart+9, minVotes)
	return c.discoverTitles(ctx, mediaType, filter, fmt.Sprintf("decade decade=%d", decadeStart), page, sortBy, direction)
}

// discoverTitles runs a TMDB discover query with the given extra filter params
// (already URL-encoded, starting with "&") sorted by popularity.
func (c *tmdbClient) discoverTitles(ctx context.Context, mediaType, filterQuery, logLabel string, page int, sortBy, direction string) ([]models.Title, int, error) {
	start := time.Now()
	if !c.isConfigured() {
		return nil, 0, errors.New("tmdb api key not configured")
	}

	// Map "series" to "tv" for TMDB API
	apiMediaType := strings.ToLower(strings.TrimSpace(mediaType))
	if apiMediaType != "movie" {
		apiMediaType = "tv"
	}

	tmdbSort := tmdbDiscoverSort(apiMediaType, sortBy, direction)
	endpoint := fmt.Sprintf("%s/discover/%s?api_key=%s%s&sort_by=%s&page=%d",
		tmdbBaseURL, apiMediaType, c.apiKey, filterQuery, url.QueryEscape(tmdbSort), page)
	if lang := strings.TrimSpace(c.language); lang != "" {
		endpoint = endpoint + "&language=" + normalizeLanguage(lang)
		if originalLanguage := tmdbOriginalLanguageFilter(lang); originalLanguage != "" {
			endpoint = endpoint + "&with_original_language=" + url.QueryEscape(originalLanguage)
		}
	}

	var payload struct {
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
			GenreIDs         []int   `json:"genre_ids"`
		} `json:"results"`
		TotalResults int `json:"total_results"`
	}
	if err := c.doGET(ctx, endpoint, &payload); err != nil {
		return nil, 0, fmt.Errorf("tmdb discover %s for %s failed: %w", logLabel, apiMediaType, err)
	}
	log.Printf(
		"[tmdb] discover %s fetched mediaType=%s page=%d results=%d total=%d duration=%s",
		logLabel,
		apiMediaType,
		page,
		len(payload.Results),
		payload.TotalResults,
		time.Since(start).Round(time.Millisecond),
	)

	titles := make([]models.Title, 0, len(payload.Results))
	for _, r := range payload.Results {
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
		if genres := resolveGenreIDs(r.GenreIDs, apiMediaType); len(genres) > 0 {
			title.Genres = genres
		}
		titles = append(titles, title)
	}

	return titles, payload.TotalResults, nil
}

func tmdbDiscoverSort(apiMediaType, sortBy, direction string) string {
	if direction != "asc" && direction != "desc" {
		direction = "desc"
	}
	switch strings.ToLower(strings.TrimSpace(sortBy)) {
	case "name":
		if apiMediaType == "tv" {
			return "name." + direction
		}
		return "title." + direction
	case "year":
		if apiMediaType == "tv" {
			return "first_air_date." + direction
		}
		return "primary_release_date." + direction
	case "rating":
		return "vote_average." + direction
	default:
		return "popularity.desc"
	}
}

const (
	TMDBSourcePublicList        = "public-list"
	TMDBSourceProductionCompany = "production-company"
	TMDBSourceNetwork           = "network"
	TMDBSourceMovieCollection   = "movie-collection"
	TMDBSourcePersonCredits     = "person-credits"
	TMDBSourceDirectorCredits   = "director-credits"
	TMDBSourceCustomDiscover    = "custom-discover"
)

var tmdbShelfSourceTypes = map[string]struct{}{
	TMDBSourcePublicList:        {},
	TMDBSourceProductionCompany: {},
	TMDBSourceNetwork:           {},
	TMDBSourceMovieCollection:   {},
	TMDBSourcePersonCredits:     {},
	TMDBSourceDirectorCredits:   {},
	TMDBSourceCustomDiscover:    {},
}

// TMDBListOptions identifies a TMDB-backed shelf and controls its paging.
type TMDBListOptions struct {
	SourceType    string
	SourceID      string
	MediaType     string
	Sort          string
	DiscoverQuery string
	Limit         int
	Offset        int
	ArtworkLimit  int
}

// TMDBSourceResult is a company, network, collection, person, or public list
// that can be selected as the source of a TMDB shelf.
type TMDBSourceResult struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"mediaType,omitempty"`
	ImageURL  string `json:"imageUrl,omitempty"`
}

type tmdbShelfTitle struct {
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	Title            string  `json:"title"`
	Overview         string  `json:"overview"`
	OriginalLanguage string  `json:"original_language"`
	PosterPath       string  `json:"poster_path"`
	BackdropPath     string  `json:"backdrop_path"`
	MediaType        string  `json:"media_type"`
	ReleaseDate      string  `json:"release_date"`
	FirstAirDate     string  `json:"first_air_date"`
	Popularity       float64 `json:"popularity"`
	VoteAverage      float64 `json:"vote_average"`
	VoteCount        int     `json:"vote_count"`
	GenreIDs         []int   `json:"genre_ids"`
	Adult            bool    `json:"adult"`
	Job              string  `json:"job"`
	Department       string  `json:"department"`
}

func (item tmdbShelfTitle) toTitle(forcedMediaType string) (models.Title, bool) {
	apiMediaType := strings.ToLower(strings.TrimSpace(item.MediaType))
	if apiMediaType == "" {
		apiMediaType = strings.ToLower(strings.TrimSpace(forcedMediaType))
	}
	if apiMediaType != "movie" && apiMediaType != "tv" {
		if strings.TrimSpace(item.Title) != "" {
			apiMediaType = "movie"
		} else if strings.TrimSpace(item.Name) != "" {
			apiMediaType = "tv"
		}
	}
	if item.ID <= 0 || (apiMediaType != "movie" && apiMediaType != "tv") {
		return models.Title{}, false
	}
	mediaType := "movie"
	if apiMediaType == "tv" {
		mediaType = "series"
	}
	title := models.Title{
		ID:         fmt.Sprintf("tmdb:%s:%d", apiMediaType, item.ID),
		Name:       pickTMDBName(apiMediaType, item.Name, item.Title),
		Overview:   item.Overview,
		Language:   item.OriginalLanguage,
		MediaType:  mediaType,
		TMDBID:     item.ID,
		Popularity: scoreFallback(item.Popularity, item.VoteAverage),
		VoteCount:  item.VoteCount,
		Adult:      item.Adult,
	}
	title.Year = parseTMDBYear(item.ReleaseDate, item.FirstAirDate)
	title.Poster = buildTMDBImage(item.PosterPath, tmdbPosterSize, "poster")
	if title.Poster == nil {
		title.Poster = buildTMDBImage(item.BackdropPath, tmdbPosterSize, "poster")
	}
	title.Backdrop = buildTMDBImage(item.BackdropPath, tmdbBackdropSize, "backdrop")
	title.Genres = resolveGenreIDs(item.GenreIDs, apiMediaType)
	if item.VoteAverage > 0 {
		title.Ratings = []models.Rating{{Source: "tmdb", Value: item.VoteAverage, Max: 10}}
	}
	return title, strings.TrimSpace(title.Name) != ""
}

func normalizeTMDBShelfSourceType(sourceType string) string {
	return strings.ToLower(strings.TrimSpace(sourceType))
}

func parseTMDBSourceID(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id > 0 {
		return id
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	parts := strings.FieldsFunc(parsed.Path, func(r rune) bool {
		return r == '/' || r == '-'
	})
	for i := len(parts) - 1; i >= 0; i-- {
		if id, err := strconv.ParseInt(parts[i], 10, 64); err == nil && id > 0 {
			return id
		}
	}
	return 0
}

func (c *tmdbClient) searchShelfSources(ctx context.Context, sourceType, query string) ([]TMDBSourceResult, error) {
	if !c.isConfigured() {
		return nil, errors.New("tmdb api key not configured")
	}
	sourceType = normalizeTMDBShelfSourceType(sourceType)
	if _, ok := tmdbShelfSourceTypes[sourceType]; !ok {
		return nil, fmt.Errorf("unsupported tmdb source type %q", sourceType)
	}
	if sourceType == TMDBSourceCustomDiscover {
		return []TMDBSourceResult{}, nil
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("tmdb source search query required")
	}

	if sourceID := parseTMDBSourceID(query); sourceID > 0 {
		detailPath := map[string]string{
			TMDBSourcePublicList:        "list",
			TMDBSourceProductionCompany: "company",
			TMDBSourceNetwork:           "network",
			TMDBSourceMovieCollection:   "collection",
			TMDBSourcePersonCredits:     "person",
			TMDBSourceDirectorCredits:   "person",
		}[sourceType]
		endpoint := fmt.Sprintf("%s/%s/%d?api_key=%s", tmdbBaseURL, detailPath, sourceID, url.QueryEscape(c.apiKey))
		if lang := strings.TrimSpace(c.language); lang != "" {
			endpoint += "&language=" + url.QueryEscape(normalizeLanguage(lang))
		}
		var payload struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			Title       string `json:"title"`
			PosterPath  string `json:"poster_path"`
			ProfilePath string `json:"profile_path"`
			LogoPath    string `json:"logo_path"`
		}
		if err := c.doGET(ctx, endpoint, &payload); err != nil {
			return nil, fmt.Errorf("tmdb source lookup failed: %w", err)
		}
		name := strings.TrimSpace(payload.Name)
		if name == "" {
			name = strings.TrimSpace(payload.Title)
		}
		imagePath := payload.LogoPath
		if imagePath == "" {
			imagePath = payload.ProfilePath
		}
		if imagePath == "" {
			imagePath = payload.PosterPath
		}
		result := TMDBSourceResult{ID: strconv.FormatInt(payload.ID, 10), Name: name}
		if image := buildTMDBImage(imagePath, tmdbPosterSize, "poster"); image != nil {
			result.ImageURL = image.URL
		}
		return []TMDBSourceResult{result}, nil
	}

	searchPath := map[string]string{
		TMDBSourceProductionCompany: "company",
		TMDBSourceMovieCollection:   "collection",
		TMDBSourcePersonCredits:     "person",
		TMDBSourceDirectorCredits:   "person",
	}[sourceType]
	if searchPath == "" {
		return nil, errors.New("this tmdb source requires a numeric ID or TMDB URL")
	}
	params := url.Values{
		"api_key":       {c.apiKey},
		"query":         {query},
		"include_adult": {"false"},
		"page":          {"1"},
	}
	if lang := strings.TrimSpace(c.language); lang != "" {
		params.Set("language", normalizeLanguage(lang))
	}
	var payload struct {
		Results []struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			Title       string `json:"title"`
			PosterPath  string `json:"poster_path"`
			ProfilePath string `json:"profile_path"`
			LogoPath    string `json:"logo_path"`
		} `json:"results"`
	}
	if err := c.doGET(ctx, tmdbBaseURL+"/search/"+searchPath+"?"+params.Encode(), &payload); err != nil {
		return nil, fmt.Errorf("tmdb source search failed: %w", err)
	}
	results := make([]TMDBSourceResult, 0, len(payload.Results))
	for _, item := range payload.Results {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = strings.TrimSpace(item.Title)
		}
		if item.ID <= 0 || name == "" {
			continue
		}
		result := TMDBSourceResult{ID: strconv.FormatInt(item.ID, 10), Name: name}
		imagePath := item.LogoPath
		if imagePath == "" {
			imagePath = item.ProfilePath
		}
		if imagePath == "" {
			imagePath = item.PosterPath
		}
		if image := buildTMDBImage(imagePath, tmdbPosterSize, "poster"); image != nil {
			result.ImageURL = image.URL
		}
		results = append(results, result)
		if len(results) == 20 {
			break
		}
	}
	return results, nil
}

func normalizeTMDBShelfMediaTypes(sourceType, mediaType string) []string {
	switch sourceType {
	case TMDBSourceNetwork:
		return []string{"tv"}
	case TMDBSourceMovieCollection:
		return []string{"movie"}
	}
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "movie", "movies":
		return []string{"movie"}
	case "tv", "series", "show", "shows":
		return []string{"tv"}
	default:
		return []string{"movie", "tv"}
	}
}

func normalizeTMDBShelfSort(apiMediaType, requested string) string {
	requested = strings.ToLower(strings.TrimSpace(requested))
	switch requested {
	case "popularity.asc", "popularity.desc", "vote_average.asc", "vote_average.desc":
		return requested
	case "release_date.asc":
		if apiMediaType == "tv" {
			return "first_air_date.asc"
		}
		return "primary_release_date.asc"
	case "release_date.desc":
		if apiMediaType == "tv" {
			return "first_air_date.desc"
		}
		return "primary_release_date.desc"
	case "title.asc":
		if apiMediaType == "tv" {
			return "name.asc"
		}
		return "title.asc"
	case "title.desc":
		if apiMediaType == "tv" {
			return "name.desc"
		}
		return "title.desc"
	default:
		return "popularity.desc"
	}
}

func parseTMDBDiscoverQuery(raw string) (url.Values, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return url.Values{}, nil
	}
	if strings.Contains(raw, "?") {
		raw = raw[strings.Index(raw, "?")+1:]
	}
	raw = strings.TrimPrefix(raw, "?")
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid tmdb discover query: %w", err)
	}
	for _, controlled := range []string{"api_key", "language", "page", "sort_by"} {
		values.Del(controlled)
	}
	return values, nil
}

func tmdbDiscoverFilters(apiMediaType string, filters url.Values) url.Values {
	mapped := url.Values{}
	for key, values := range filters {
		targetKey := key
		switch key {
		case "genres":
			targetKey = "with_genres"
		case "date.gte":
			if apiMediaType == "tv" {
				targetKey = "first_air_date.gte"
			} else {
				targetKey = "primary_release_date.gte"
			}
		case "date.lte":
			if apiMediaType == "tv" {
				targetKey = "first_air_date.lte"
			} else {
				targetKey = "primary_release_date.lte"
			}
		case "rating.gte":
			targetKey = "vote_average.gte"
		case "rating.lte":
			targetKey = "vote_average.lte"
		case "votes.gte":
			targetKey = "vote_count.gte"
		case "language":
			targetKey = "with_original_language"
		case "country":
			targetKey = "with_origin_country"
		case "keywords":
			targetKey = "with_keywords"
		case "companies":
			targetKey = "with_companies"
		case "networks":
			if apiMediaType != "tv" {
				continue
			}
			targetKey = "with_networks"
		case "year":
			if apiMediaType == "tv" {
				targetKey = "first_air_date_year"
			} else {
				targetKey = "primary_release_year"
			}
		}
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				mapped.Add(targetKey, value)
			}
		}
	}
	return mapped
}

func addRequiredTMDBFilter(filters url.Values, genericKey, sourceID string) {
	existing := strings.TrimSpace(filters.Get(genericKey))
	if existing == "" {
		filters.Set(genericKey, sourceID)
		return
	}
	filters.Set(genericKey, sourceID+","+existing)
}

func tmdbFilterValue(filters url.Values, genericKey, apiKey string) string {
	if value := strings.TrimSpace(filters.Get(genericKey)); value != "" {
		return value
	}
	return strings.TrimSpace(filters.Get(apiKey))
}

func tmdbFilterFloat(filters url.Values, genericKey, apiKey string) (float64, bool) {
	raw := tmdbFilterValue(filters, genericKey, apiKey)
	if raw == "" {
		return 0, false
	}
	value, err := strconv.ParseFloat(raw, 64)
	return value, err == nil
}

func tmdbFilterInt(filters url.Values, genericKey, apiKey string) (int, bool) {
	raw := tmdbFilterValue(filters, genericKey, apiKey)
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	return value, err == nil
}

func tmdbIDsMatchFilter(available []int64, raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	have := make(map[int64]struct{}, len(available))
	for _, id := range available {
		have[id] = struct{}{}
	}
	if strings.Contains(raw, "|") {
		for _, part := range strings.Split(raw, "|") {
			id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
			if err == nil {
				if _, ok := have[id]; ok {
					return true
				}
			}
		}
		return false
	}
	for _, part := range strings.Split(raw, ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil {
			return false
		}
		if _, ok := have[id]; !ok {
			return false
		}
	}
	return true
}

func tmdbIDsContainAny(available []int64, raw string) bool {
	have := make(map[int64]struct{}, len(available))
	for _, id := range available {
		have[id] = struct{}{}
	}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '|' }) {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err == nil {
			if _, ok := have[id]; ok {
				return true
			}
		}
	}
	return false
}

type tmdbShelfFilterDetails struct {
	ProductionCompanies []struct {
		ID int64 `json:"id"`
	} `json:"production_companies"`
	ProductionCountries []struct {
		Code string `json:"iso_3166_1"`
	} `json:"production_countries"`
	Networks []struct {
		ID int64 `json:"id"`
	} `json:"networks"`
	OriginCountry []string `json:"origin_country"`
	Keywords      struct {
		Keywords []struct {
			ID int64 `json:"id"`
		} `json:"keywords"`
		Results []struct {
			ID int64 `json:"id"`
		} `json:"results"`
	} `json:"keywords"`
	Runtime int `json:"runtime"`
}

func tmdbShelfFiltersNeedDetails(filters url.Values) bool {
	return tmdbFilterValue(filters, "companies", "with_companies") != "" ||
		tmdbFilterValue(filters, "networks", "with_networks") != "" ||
		tmdbFilterValue(filters, "keywords", "with_keywords") != "" ||
		tmdbFilterValue(filters, "country", "with_origin_country") != "" ||
		tmdbFilterValue(filters, "runtime.gte", "with_runtime.gte") != ""
}

func (c *tmdbClient) shelfFilterDetails(ctx context.Context, apiMediaType string, itemID int64) (tmdbShelfFilterDetails, error) {
	cacheID := cacheKey(
		"tmdb",
		"shelf-filter-details",
		"v1",
		c.language,
		apiMediaType,
		strconv.FormatInt(itemID, 10),
	)
	var details tmdbShelfFilterDetails
	if c.cache != nil {
		if ok, _ := c.cache.get(cacheID, &details); ok {
			return details, nil
		}
	}
	params := url.Values{
		"api_key":            {c.apiKey},
		"append_to_response": {"keywords"},
	}
	if lang := strings.TrimSpace(c.language); lang != "" {
		params.Set("language", normalizeLanguage(lang))
	}
	endpoint := fmt.Sprintf("%s/%s/%d?%s", tmdbBaseURL, apiMediaType, itemID, params.Encode())
	if err := c.doGET(ctx, endpoint, &details); err != nil {
		return tmdbShelfFilterDetails{}, fmt.Errorf("tmdb shelf filter details failed: %w", err)
	}
	if c.cache != nil {
		_ = c.cache.set(cacheID, &details)
	}
	return details, nil
}

func (c *tmdbClient) staticShelfTitleMatchesFilters(ctx context.Context, item tmdbShelfTitle, filters url.Values) (bool, error) {
	if len(filters) == 0 {
		return true, nil
	}
	apiMediaType := strings.ToLower(strings.TrimSpace(item.MediaType))
	if apiMediaType == "" {
		if item.Title != "" {
			apiMediaType = "movie"
		} else {
			apiMediaType = "tv"
		}
	}
	genres := make([]int64, len(item.GenreIDs))
	for i, id := range item.GenreIDs {
		genres[i] = int64(id)
	}
	if !tmdbIDsMatchFilter(genres, tmdbFilterValue(filters, "genres", "with_genres")) {
		return false, nil
	}
	if tmdbIDsContainAny(genres, tmdbFilterValue(filters, "without_genres", "without_genres")) {
		return false, nil
	}
	date := item.ReleaseDate
	if apiMediaType == "tv" {
		date = item.FirstAirDate
	}
	dateFrom := tmdbFilterValue(filters, "date.gte", map[string]string{"movie": "primary_release_date.gte", "tv": "first_air_date.gte"}[apiMediaType])
	dateTo := tmdbFilterValue(filters, "date.lte", map[string]string{"movie": "primary_release_date.lte", "tv": "first_air_date.lte"}[apiMediaType])
	if dateFrom != "" && (date == "" || date < dateFrom) {
		return false, nil
	}
	if dateTo != "" && (date == "" || date > dateTo) {
		return false, nil
	}
	if minimum, ok := tmdbFilterFloat(filters, "rating.gte", "vote_average.gte"); ok && item.VoteAverage < minimum {
		return false, nil
	}
	if maximum, ok := tmdbFilterFloat(filters, "rating.lte", "vote_average.lte"); ok && item.VoteAverage > maximum {
		return false, nil
	}
	if minimum, ok := tmdbFilterInt(filters, "votes.gte", "vote_count.gte"); ok && item.VoteCount < minimum {
		return false, nil
	}
	language := tmdbFilterValue(filters, "language", "with_original_language")
	if language != "" && !strings.EqualFold(strings.TrimSpace(item.OriginalLanguage), language) {
		return false, nil
	}
	if year, ok := tmdbFilterInt(filters, "year", map[string]string{"movie": "primary_release_year", "tv": "first_air_date_year"}[apiMediaType]); ok {
		if parseTMDBYear(item.ReleaseDate, item.FirstAirDate) != year {
			return false, nil
		}
	}

	companyFilter := tmdbFilterValue(filters, "companies", "with_companies")
	networkFilter := tmdbFilterValue(filters, "networks", "with_networks")
	keywordFilter := tmdbFilterValue(filters, "keywords", "with_keywords")
	countryFilter := tmdbFilterValue(filters, "country", "with_origin_country")
	runtimeMinimum, filterRuntime := tmdbFilterInt(filters, "runtime.gte", "with_runtime.gte")
	filterRuntime = filterRuntime && apiMediaType == "movie"
	if companyFilter == "" && networkFilter == "" && keywordFilter == "" && countryFilter == "" && !filterRuntime {
		return true, nil
	}
	details, err := c.shelfFilterDetails(ctx, apiMediaType, item.ID)
	if err != nil {
		return false, err
	}
	if filterRuntime && details.Runtime < runtimeMinimum {
		return false, nil
	}
	companies := make([]int64, 0, len(details.ProductionCompanies))
	for _, company := range details.ProductionCompanies {
		companies = append(companies, company.ID)
	}
	if !tmdbIDsMatchFilter(companies, companyFilter) {
		return false, nil
	}
	networks := make([]int64, 0, len(details.Networks))
	for _, network := range details.Networks {
		networks = append(networks, network.ID)
	}
	if !tmdbIDsMatchFilter(networks, networkFilter) {
		return false, nil
	}
	keywords := make([]int64, 0, len(details.Keywords.Keywords)+len(details.Keywords.Results))
	for _, keyword := range details.Keywords.Keywords {
		keywords = append(keywords, keyword.ID)
	}
	for _, keyword := range details.Keywords.Results {
		keywords = append(keywords, keyword.ID)
	}
	if !tmdbIDsMatchFilter(keywords, keywordFilter) {
		return false, nil
	}
	if countryFilter != "" {
		found := false
		for _, country := range details.OriginCountry {
			if strings.EqualFold(country, countryFilter) {
				found = true
				break
			}
		}
		if !found {
			for _, country := range details.ProductionCountries {
				if strings.EqualFold(country.Code, countryFilter) {
					found = true
					break
				}
			}
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
}

func (c *tmdbClient) filterStaticShelfTitles(ctx context.Context, items []tmdbShelfTitle, filters url.Values) ([]tmdbShelfTitle, error) {
	if len(filters) == 0 {
		return items, nil
	}
	if !tmdbShelfFiltersNeedDetails(filters) {
		filtered := make([]tmdbShelfTitle, 0, len(items))
		for _, item := range items {
			matches, err := c.staticShelfTitleMatchesFilters(ctx, item, filters)
			if err != nil {
				return nil, err
			}
			if matches {
				filtered = append(filtered, item)
			}
		}
		return filtered, nil
	}

	matches := make([]bool, len(items))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(6)
	for i := range items {
		i := i
		group.Go(func() error {
			matched, err := c.staticShelfTitleMatchesFilters(groupCtx, items[i], filters)
			if err != nil {
				return err
			}
			matches[i] = matched
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	filtered := make([]tmdbShelfTitle, 0, len(items))
	for i, matched := range matches {
		if matched {
			filtered = append(filtered, items[i])
		}
	}
	return filtered, nil
}

type tmdbShelfPage struct {
	Items []tmdbShelfTitle `json:"items"`
	Total int              `json:"total"`
}

func (c *tmdbClient) cachedDiscoverShelfPage(apiMediaType string, filters url.Values, page int, sortKey string) (tmdbShelfPage, bool) {
	if c.cache == nil {
		return tmdbShelfPage{}, false
	}
	cacheID := cacheKey(
		"tmdb", "discover-shelf-page", "v1", c.language, apiMediaType,
		strings.ToLower(strings.TrimSpace(sortKey)), filters.Encode(), strconv.Itoa(page),
	)
	var cached tmdbShelfPage
	ok, _ := c.cache.get(cacheID, &cached)
	return cached, ok
}

func (c *tmdbClient) cacheDiscoverShelfPage(apiMediaType string, filters url.Values, page int, sortKey string, result tmdbShelfPage) {
	if c.cache == nil {
		return
	}
	cacheID := cacheKey(
		"tmdb", "discover-shelf-page", "v1", c.language, apiMediaType,
		strings.ToLower(strings.TrimSpace(sortKey)), filters.Encode(), strconv.Itoa(page),
	)
	if err := c.cache.set(cacheID, result); err != nil {
		log.Printf("[tmdb] failed to cache discover shelf page: %v", err)
	}
}

func (c *tmdbClient) fetchDiscoverShelfTitles(
	ctx context.Context,
	apiMediaType string,
	filters url.Values,
	sortKey string,
	needed int,
	unlimited bool,
) ([]tmdbShelfTitle, int, error) {
	first, sourceTotal, err := c.discoverShelfPage(ctx, apiMediaType, filters, 1, sortKey)
	if err != nil {
		return nil, 0, err
	}
	totalPages := max(1, (sourceTotal+19)/20)
	pageCount := totalPages
	if !unlimited {
		pageCount = minInt(pageCount, max(1, (needed+19)/20))
	}
	pages := make([][]tmdbShelfTitle, totalPages)
	pages[0] = first

	fetchThrough := func(lastPage int) error {
		group, groupCtx := errgroup.WithContext(ctx)
		group.SetLimit(6)
		for page := pageCount + 1; page <= lastPage; page++ {
			page := page
			group.Go(func() error {
				items, _, fetchErr := c.discoverShelfPage(groupCtx, apiMediaType, filters, page, sortKey)
				if fetchErr != nil {
					return fetchErr
				}
				pages[page-1] = items
				return nil
			})
		}
		if err := group.Wait(); err != nil {
			return err
		}
		pageCount = lastPage
		return nil
	}

	// Fetch the initial range in one bounded concurrent wave.
	initialPageCount := pageCount
	pageCount = 1
	if initialPageCount > 1 {
		if err := fetchThrough(initialPageCount); err != nil {
			return nil, 0, err
		}
	}

	items := make([]tmdbShelfTitle, 0, minInt(sourceTotal, pageCount*20))
	for page := range pageCount {
		items = append(items, pages[page]...)
	}
	return items, sourceTotal, nil
}

// discoverShelfPage caches individual TMDB pages so a later offset does not
// refetch every preceding page. This also lets full Explore loads reuse pages
// previously fetched by the home shelf.
func (c *tmdbClient) discoverShelfPage(ctx context.Context, apiMediaType string, filters url.Values, page int, sortKey string) ([]tmdbShelfTitle, int, error) {
	if cached, ok := c.cachedDiscoverShelfPage(apiMediaType, filters, page, sortKey); ok {
		return cached.Items, cached.Total, nil
	}
	params := url.Values{
		"api_key": {c.apiKey},
		"page":    {strconv.Itoa(page)},
		"sort_by": {normalizeTMDBShelfSort(apiMediaType, sortKey)},
	}
	if lang := strings.TrimSpace(c.language); lang != "" {
		params.Set("language", normalizeLanguage(lang))
	}
	for key, values := range tmdbDiscoverFilters(apiMediaType, filters) {
		for _, value := range values {
			params.Add(key, value)
		}
	}
	var payload struct {
		Results      []tmdbShelfTitle `json:"results"`
		TotalResults int              `json:"total_results"`
	}
	endpoint := fmt.Sprintf("%s/discover/%s?%s", tmdbBaseURL, apiMediaType, params.Encode())
	if err := c.doGET(ctx, endpoint, &payload); err != nil {
		return nil, 0, fmt.Errorf("tmdb discover shelf failed: %w", err)
	}
	for i := range payload.Results {
		payload.Results[i].MediaType = apiMediaType
	}
	result := tmdbShelfPage{Items: payload.Results, Total: payload.TotalResults}
	c.cacheDiscoverShelfPage(apiMediaType, filters, page, sortKey, result)
	return result.Items, result.Total, nil
}

func (c *tmdbClient) staticShelfTitles(ctx context.Context, opts TMDBListOptions, sourceID int64) ([]tmdbShelfTitle, error) {
	params := url.Values{"api_key": {c.apiKey}}
	if lang := strings.TrimSpace(c.language); lang != "" {
		params.Set("language", normalizeLanguage(lang))
	}
	var endpoint string
	switch opts.SourceType {
	case TMDBSourcePublicList:
		endpoint = fmt.Sprintf("%s/list/%d?%s", tmdbBaseURL, sourceID, params.Encode())
		var payload struct {
			Items []tmdbShelfTitle `json:"items"`
		}
		if err := c.doGET(ctx, endpoint, &payload); err != nil {
			return nil, fmt.Errorf("tmdb public list failed: %w", err)
		}
		return payload.Items, nil
	case TMDBSourceMovieCollection:
		endpoint = fmt.Sprintf("%s/collection/%d?%s", tmdbBaseURL, sourceID, params.Encode())
		var payload struct {
			Parts []tmdbShelfTitle `json:"parts"`
		}
		if err := c.doGET(ctx, endpoint, &payload); err != nil {
			return nil, fmt.Errorf("tmdb movie collection failed: %w", err)
		}
		for i := range payload.Parts {
			payload.Parts[i].MediaType = "movie"
		}
		return payload.Parts, nil
	case TMDBSourcePersonCredits, TMDBSourceDirectorCredits:
		endpoint = fmt.Sprintf("%s/person/%d/combined_credits?%s", tmdbBaseURL, sourceID, params.Encode())
		var payload struct {
			Cast []tmdbShelfTitle `json:"cast"`
			Crew []tmdbShelfTitle `json:"crew"`
		}
		if err := c.doGET(ctx, endpoint, &payload); err != nil {
			return nil, fmt.Errorf("tmdb person credits failed: %w", err)
		}
		if opts.SourceType == TMDBSourcePersonCredits {
			return payload.Cast, nil
		}
		directed := make([]tmdbShelfTitle, 0, len(payload.Crew))
		for _, item := range payload.Crew {
			if strings.EqualFold(strings.TrimSpace(item.Job), "director") {
				directed = append(directed, item)
			}
		}
		return directed, nil
	default:
		return nil, fmt.Errorf("unsupported static tmdb source %q", opts.SourceType)
	}
}

func tmdbShelfTitleMatchesMediaTypes(item tmdbShelfTitle, mediaTypes []string) bool {
	itemType := strings.ToLower(strings.TrimSpace(item.MediaType))
	if itemType == "" {
		if strings.TrimSpace(item.Title) != "" {
			itemType = "movie"
		} else if strings.TrimSpace(item.Name) != "" {
			itemType = "tv"
		}
	}
	for _, mediaType := range mediaTypes {
		if itemType == mediaType {
			return true
		}
	}
	return false
}

func sortTMDBShelfTitles(items []tmdbShelfTitle, sortKey string) {
	sortKey = strings.ToLower(strings.TrimSpace(sortKey))
	if sortKey == "" || sortKey == "original" {
		return
	}
	descending := !strings.HasSuffix(sortKey, ".asc")
	sort.SliceStable(items, func(i, j int) bool {
		comparison := 0
		switch {
		case strings.HasPrefix(sortKey, "vote_average"):
			comparison = cmp.Compare(items[i].VoteAverage, items[j].VoteAverage)
		case strings.HasPrefix(sortKey, "release_date"):
			left := items[i].ReleaseDate
			if left == "" {
				left = items[i].FirstAirDate
			}
			right := items[j].ReleaseDate
			if right == "" {
				right = items[j].FirstAirDate
			}
			comparison = strings.Compare(left, right)
		case strings.HasPrefix(sortKey, "title"):
			left := pickTMDBName(items[i].MediaType, items[i].Name, items[i].Title)
			right := pickTMDBName(items[j].MediaType, items[j].Name, items[j].Title)
			comparison = strings.Compare(strings.ToLower(left), strings.ToLower(right))
		default:
			comparison = cmp.Compare(items[i].Popularity, items[j].Popularity)
		}
		if descending {
			return comparison > 0
		}
		return comparison < 0
	})
}

func (c *tmdbClient) fetchShelfTitles(ctx context.Context, opts TMDBListOptions) ([]models.Title, int, error) {
	if !c.isConfigured() {
		return nil, 0, errors.New("tmdb api key not configured")
	}
	opts.SourceType = normalizeTMDBShelfSourceType(opts.SourceType)
	if _, ok := tmdbShelfSourceTypes[opts.SourceType]; !ok {
		return nil, 0, fmt.Errorf("unsupported tmdb source type %q", opts.SourceType)
	}
	sourceID := parseTMDBSourceID(opts.SourceID)
	if opts.SourceType != TMDBSourceCustomDiscover && sourceID <= 0 {
		return nil, 0, errors.New("tmdb source ID required")
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	unlimited := opts.Limit <= 0
	if unlimited {
		opts.Limit = 0
	} else if opts.Limit > 500 {
		opts.Limit = 500
	}
	rawCapacity := 0
	if !unlimited {
		rawCapacity = opts.Offset + opts.Limit
	}
	mediaTypes := normalizeTMDBShelfMediaTypes(opts.SourceType, opts.MediaType)
	rawItems := make([]tmdbShelfTitle, 0, rawCapacity)
	total := 0

	filters, err := parseTMDBDiscoverQuery(opts.DiscoverQuery)
	if err != nil {
		return nil, 0, err
	}
	switch opts.SourceType {
	case TMDBSourceProductionCompany, TMDBSourceNetwork, TMDBSourceCustomDiscover:
		if opts.SourceType == TMDBSourceProductionCompany {
			addRequiredTMDBFilter(filters, "companies", strconv.FormatInt(sourceID, 10))
		} else if opts.SourceType == TMDBSourceNetwork {
			addRequiredTMDBFilter(filters, "networks", strconv.FormatInt(sourceID, 10))
		}
		needed := opts.Offset + opts.Limit
		for _, mediaType := range mediaTypes {
			typeItems, typeTotal, fetchErr := c.fetchDiscoverShelfTitles(
				ctx,
				mediaType,
				filters,
				opts.Sort,
				needed,
				unlimited,
			)
			if fetchErr != nil {
				return nil, 0, fetchErr
			}
			rawItems = append(rawItems, typeItems...)
			total += typeTotal
		}
	default:
		items, fetchErr := c.staticShelfTitles(ctx, opts, sourceID)
		if fetchErr != nil {
			return nil, 0, fetchErr
		}
		mediaItems := make([]tmdbShelfTitle, 0, len(items))
		for _, item := range items {
			if tmdbShelfTitleMatchesMediaTypes(item, mediaTypes) {
				mediaItems = append(mediaItems, item)
			}
		}
		rawItems, fetchErr = c.filterStaticShelfTitles(ctx, mediaItems, filters)
		if fetchErr != nil {
			return nil, 0, fetchErr
		}
	}

	if opts.SourceType != TMDBSourceProductionCompany &&
		opts.SourceType != TMDBSourceNetwork &&
		opts.SourceType != TMDBSourceCustomDiscover {
		total = len(rawItems)
	}
	sortTMDBShelfTitles(rawItems, opts.Sort)
	seen := make(map[string]struct{}, len(rawItems))
	titleCapacity := len(rawItems)
	if !unlimited {
		titleCapacity = minInt(opts.Limit, titleCapacity)
	}
	titles := make([]models.Title, 0, titleCapacity)
	for _, item := range rawItems {
		forcedMediaType := ""
		if len(mediaTypes) == 1 {
			forcedMediaType = mediaTypes[0]
		}
		title, ok := item.toTitle(forcedMediaType)
		if !ok {
			continue
		}
		if _, exists := seen[title.ID]; exists {
			continue
		}
		seen[title.ID] = struct{}{}
		if len(seen) <= opts.Offset {
			continue
		}
		titles = append(titles, title)
		if !unlimited && len(titles) == opts.Limit {
			break
		}
	}
	return titles, total, nil
}

func tmdbOriginalLanguageFilter(lang string) string {
	normalized := normalizeLanguage(lang)
	if len(normalized) < 2 {
		return ""
	}
	return strings.ToLower(normalized[:2])
}

func tmdbOriginalLanguageMatches(value, want string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	want = strings.ToLower(strings.TrimSpace(want))
	return value != "" && want != "" && value == want
}
