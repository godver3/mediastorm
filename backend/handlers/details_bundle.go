package handlers

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/models"

	"github.com/gorilla/mux"
)

// DetailsBundleHandler serves a combined details-page payload to reduce the
// number of HTTP round-trips when the frontend opens a title details screen.
// All sub-fetches are performed concurrently.
type DetailsBundleHandler struct {
	metadata     metadataService
	history      historyService
	contentPrefs contentPreferencesService
	users        userService
}

type seriesDetailsLiteProvider interface {
	SeriesDetailsLite(context.Context, models.SeriesDetailsQuery) (*models.SeriesDetails, error)
}

// NewDetailsBundleHandler constructs a DetailsBundleHandler.
func NewDetailsBundleHandler(
	metadata metadataService,
	history historyService,
	contentPrefs contentPreferencesService,
	users userService,
) *DetailsBundleHandler {
	return &DetailsBundleHandler{
		metadata:     metadata,
		history:      history,
		contentPrefs: contentPrefs,
		users:        users,
	}
}

// DetailsBundleResponse is the combined payload returned by
// GET /api/users/{userID}/details-bundle.
type DetailsBundleResponse struct {
	SeriesDetails     *models.SeriesDetails     `json:"seriesDetails"`
	MovieDetails      *models.Title             `json:"movieDetails"`
	Similar           []models.Title            `json:"similar"`
	Trailers          *models.TrailerResponse   `json:"trailers"`
	ContentPreference *models.ContentPreference `json:"contentPreference"`
	WatchState        *models.SeriesWatchState  `json:"watchState"`
	PlaybackProgress  []models.PlaybackProgress `json:"playbackProgress"`
}

// DetailsShellResponse is a lightweight early payload returned by
// GET /api/users/{userID}/details-shell so the frontend can start artwork and
// title rendering while the heavier details bundle is still loading.
type DetailsShellResponse struct {
	SeriesDetails *models.SeriesDetails `json:"seriesDetails"`
	MovieDetails  *models.Title         `json:"movieDetails"`
}

func validateDetailsUser(r *http.Request, users userService) (string, bool) {
	vars := mux.Vars(r)
	userID := strings.TrimSpace(vars["userID"])
	if userID == "" {
		return "", false
	}
	if users != nil && !users.Exists(userID) {
		return userID, false
	}
	return userID, true
}

func writeCompressedJSON(w http.ResponseWriter, r *http.Request, payload any, logPrefix string) {
	payloadStart := time.Now()
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}

	rawSize := len(body)
	encoding := "identity"
	w.Header().Set("Content-Type", "application/json")

	if rawSize >= 8*1024 && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		var compressed bytes.Buffer
		gz := gzip.NewWriter(&compressed)
		if _, err := gz.Write(body); err == nil && gz.Close() == nil {
			encoding = "gzip"
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Vary", "Accept-Encoding")
			log.Printf(
				"[%s timing] payload: raw=%dB gzip=%dB marshal=%dms encoding=%s",
				logPrefix,
				rawSize,
				compressed.Len(),
				time.Since(payloadStart).Milliseconds(),
				encoding,
			)
			_, _ = w.Write(compressed.Bytes())
			return
		}
	}

	log.Printf(
		"[%s timing] payload: raw=%dB marshal=%dms encoding=%s",
		logPrefix,
		rawSize,
		time.Since(payloadStart).Milliseconds(),
		encoding,
	)

	_, _ = w.Write(body)
}

func parseDetailsRequest(r *http.Request) (contentType, titleID, name, imdbID string, year, season int, tvdbID, tmdbID int64) {
	query := r.URL.Query()
	contentType = strings.ToLower(strings.TrimSpace(query.Get("type")))
	titleID = strings.TrimSpace(query.Get("titleId"))
	name = strings.TrimSpace(query.Get("name"))
	imdbID = strings.TrimSpace(query.Get("imdbId"))
	year = trimAndParseInt(query.Get("year"))
	season = trimAndParseInt(query.Get("season"))
	tvdbID = trimAndParseInt64(query.Get("tvdbId"))
	tmdbID = trimAndParseInt64(query.Get("tmdbId"))
	return
}

// GetDetailsShell returns lightweight title metadata so the client can begin
// artwork/logo work before the full details bundle completes.
func (h *DetailsBundleHandler) GetDetailsShell(w http.ResponseWriter, r *http.Request) {
	userID, ok := validateDetailsUser(r, h.users)
	if !ok {
		if userID == "" {
			http.Error(w, "user id is required", http.StatusBadRequest)
		} else {
			http.Error(w, "user not found", http.StatusNotFound)
		}
		return
	}

	contentType, titleID, name, imdbID, year, _, tvdbID, tmdbID := parseDetailsRequest(r)
	shellStart := time.Now()
	resp := DetailsShellResponse{}

	switch contentType {
	case "series":
		query := models.SeriesDetailsQuery{
			TitleID: titleID,
			Name:    name,
			Year:    year,
			TVDBID:  tvdbID,
			TMDBID:  tmdbID,
		}

		start := time.Now()
		if liteProvider, ok := h.metadata.(seriesDetailsLiteProvider); ok {
			details, err := liteProvider.SeriesDetailsLite(r.Context(), query)
			log.Printf("[details-shell timing] series lite: %dms (err=%v)", time.Since(start).Milliseconds(), err)
			if err == nil {
				resp.SeriesDetails = details
				break
			}
			log.Printf("[details-shell] series lite error, falling back to full details: %v", err)
		}

		start = time.Now()
		details, err := h.metadata.SeriesDetails(r.Context(), query)
		log.Printf("[details-shell timing] series details fallback: %dms (err=%v)", time.Since(start).Milliseconds(), err)
		if err != nil {
			log.Printf("[details-shell] series shell error: %v", err)
		} else {
			resp.SeriesDetails = details
		}
	case "movie":
		start := time.Now()
		details, err := h.metadata.MovieDetails(r.Context(), models.MovieDetailsQuery{
			TitleID: titleID,
			Name:    name,
			Year:    year,
			IMDBID:  imdbID,
			TMDBID:  tmdbID,
			TVDBID:  tvdbID,
		})
		log.Printf("[details-shell timing] movie details: %dms (err=%v)", time.Since(start).Milliseconds(), err)
		if err != nil {
			log.Printf("[details-shell] movie shell error: %v", err)
		} else {
			resp.MovieDetails = details
		}
	default:
		http.Error(w, "type must be series or movie", http.StatusBadRequest)
		return
	}

	log.Printf("[details-shell timing] TOTAL: %dms (type=%s, titleId=%s)", time.Since(shellStart).Milliseconds(), contentType, titleID)
	writeCompressedJSON(w, r, resp, "details-shell")
}

// GetDetailsBundle returns all details-page data in a single response.
func (h *DetailsBundleHandler) GetDetailsBundle(w http.ResponseWriter, r *http.Request) {
	userID, ok := validateDetailsUser(r, h.users)
	if !ok {
		if userID == "" {
			http.Error(w, "user id is required", http.StatusBadRequest)
		} else {
			http.Error(w, "user not found", http.StatusNotFound)
		}
		return
	}

	contentType, titleID, name, imdbID, year, season, tvdbID, tmdbID := parseDetailsRequest(r)

	bundleStart := time.Now()
	resp := DetailsBundleResponse{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	// 1. Series or Movie details — fetch first so we can use the resolved
	// TMDB ID for similar content (the request param may be stale/wrong).
	var resolvedTMDBID int64
	var detailsDone sync.WaitGroup
	detailsDone.Add(1)
	go func() {
		defer detailsDone.Done()
		start := time.Now()
		if contentType == "series" {
			details, err := h.metadata.SeriesDetails(r.Context(), models.SeriesDetailsQuery{
				TitleID: titleID,
				Name:    name,
				Year:    year,
				TVDBID:  tvdbID,
				TMDBID:  tmdbID,
			})
			log.Printf("[details-bundle timing] series details: %dms (err=%v)", time.Since(start).Milliseconds(), err)
			if err != nil {
				log.Printf("[details-bundle] series details error: %v", err)
				return
			}
			mu.Lock()
			resp.SeriesDetails = details
			if details != nil && details.Title.TMDBID > 0 {
				resolvedTMDBID = details.Title.TMDBID
			}
			mu.Unlock()
		} else {
			details, err := h.metadata.MovieDetails(r.Context(), models.MovieDetailsQuery{
				TitleID: titleID,
				Name:    name,
				Year:    year,
				IMDBID:  imdbID,
				TMDBID:  tmdbID,
				TVDBID:  tvdbID,
			})
			log.Printf("[details-bundle timing] movie details: %dms (err=%v)", time.Since(start).Milliseconds(), err)
			if err != nil {
				log.Printf("[details-bundle] movie details error: %v", err)
				return
			}
			mu.Lock()
			resp.MovieDetails = details
			if details != nil && details.TMDBID > 0 {
				resolvedTMDBID = details.TMDBID
			}
			mu.Unlock()
		}
	}()

	// 2. Similar content — wait for details to resolve the correct TMDB ID,
	// then fetch similar content. Falls back to the request param if details
	// didn't resolve a TMDB ID.
	wg.Add(1)
	go func() {
		defer wg.Done()
		detailsDone.Wait()

		similarTMDBID := resolvedTMDBID
		if similarTMDBID == 0 {
			similarTMDBID = tmdbID
		}
		if similarTMDBID <= 0 {
			return
		}

		if similarTMDBID != tmdbID && tmdbID > 0 {
			log.Printf("[details-bundle] similar: using resolved TMDB ID %d instead of request param %d", similarTMDBID, tmdbID)
		}

		start := time.Now()
		titles, err := h.metadata.Similar(r.Context(), contentType, similarTMDBID)
		log.Printf("[details-bundle timing] similar: %dms (err=%v)", time.Since(start).Milliseconds(), err)
		if err != nil {
			log.Printf("[details-bundle] similar error: %v", err)
			return
		}
		mu.Lock()
		resp.Similar = slimTitles(titles)
		mu.Unlock()
	}()

	// 3. Trailers
	wg.Add(1)
	go func() {
		defer wg.Done()
		start := time.Now()
		trailerResp, err := h.metadata.Trailers(r.Context(), models.TrailerQuery{
			MediaType:    contentType,
			TitleID:      titleID,
			Name:         name,
			Year:         year,
			IMDBID:       imdbID,
			TMDBID:       tmdbID,
			TVDBID:       tvdbID,
			SeasonNumber: season,
		})
		log.Printf("[details-bundle timing] trailers: %dms (err=%v)", time.Since(start).Milliseconds(), err)
		if err != nil {
			log.Printf("[details-bundle] trailers error: %v", err)
			return
		}
		mu.Lock()
		resp.Trailers = trailerResp
		mu.Unlock()
	}()

	// 4. Content preference
	if titleID != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			pref, err := h.contentPrefs.Get(userID, titleID)
			log.Printf("[details-bundle timing] content preference: %dms (err=%v)", time.Since(start).Milliseconds(), err)
			if err != nil {
				log.Printf("[details-bundle] content preference error: %v", err)
				return
			}
			mu.Lock()
			resp.ContentPreference = pref
			mu.Unlock()
		}()
	}

	// 5. Watch state (series only)
	if contentType == "series" && titleID != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			state, err := h.history.GetSeriesWatchState(userID, titleID)
			log.Printf("[details-bundle timing] watch state: %dms (err=%v)", time.Since(start).Milliseconds(), err)
			if err != nil {
				log.Printf("[details-bundle] watch state error: %v", err)
				return
			}
			mu.Lock()
			resp.WatchState = state
			mu.Unlock()
		}()
	}

	// 6. Playback progress — filtered to just this title's items to avoid
	// sending all 293+ items (113 KB) when only 1–20 are needed.
	wg.Add(1)
	go func() {
		defer wg.Done()
		start := time.Now()
		items, err := h.history.ListPlaybackProgress(userID)
		log.Printf("[details-bundle timing] playback progress: %dms (err=%v)", time.Since(start).Milliseconds(), err)
		if err != nil {
			log.Printf("[details-bundle] playback progress error: %v", err)
			return
		}
		filtered := filterProgressForTitle(items, titleID, contentType)
		mu.Lock()
		resp.PlaybackProgress = filtered
		mu.Unlock()
	}()

	wg.Wait()
	log.Printf("[details-bundle timing] TOTAL: %dms (type=%s, titleId=%s)", time.Since(bundleStart).Milliseconds(), contentType, titleID)

	// Ensure nil slices become empty arrays in JSON
	if resp.Similar == nil {
		resp.Similar = []models.Title{}
	}
	if resp.PlaybackProgress == nil {
		resp.PlaybackProgress = []models.PlaybackProgress{}
	}
	if resp.Trailers != nil && resp.Trailers.Trailers == nil {
		resp.Trailers.Trailers = []models.Trailer{}
	}

	writeCompressedJSON(w, r, resp, "details-bundle")
}

// Options handles CORS preflight for the details-bundle endpoint.
func (h *DetailsBundleHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// slimTitles strips heavy fields from Title objects that aren't needed for
// card rendering (releases, trailers, ratings, credits, collection, etc.).
func slimTitles(titles []models.Title) []models.Title {
	slim := make([]models.Title, len(titles))
	for i, t := range titles {
		slim[i] = models.Title{
			ID:          t.ID,
			Name:        t.Name,
			Overview:    t.Overview,
			Year:        t.Year,
			Poster:      t.Poster,
			Backdrop:    t.Backdrop,
			MediaType:   t.MediaType,
			TVDBID:      t.TVDBID,
			IMDBID:      t.IMDBID,
			TMDBID:      t.TMDBID,
			Theatrical:  t.Theatrical,
			HomeRelease: t.HomeRelease,
			Genres:      t.Genres,
		}
	}
	return slim
}

// filterProgressForTitle returns only playback progress items relevant to the
// given title. For movies, this is typically 1 item; for series, it's the
// episodes of that series. This avoids sending all ~300 items (113 KB) when
// only a handful are needed.
func filterProgressForTitle(items []models.PlaybackProgress, titleID, contentType string) []models.PlaybackProgress {
	if titleID == "" {
		return items
	}
	prefix := titleID + ":"
	var filtered []models.PlaybackProgress
	for _, p := range items {
		// Match by seriesId (episodes of this series) or by itemId/ID (direct match for movies)
		if p.SeriesID == titleID || p.ItemID == titleID || p.ID == titleID ||
			strings.HasPrefix(p.ItemID, prefix) || strings.HasPrefix(p.ID, prefix) {
			filtered = append(filtered, p)
		}
	}
	if filtered == nil {
		filtered = []models.PlaybackProgress{}
	}
	return filtered
}

func trimAndParseInt(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return parsed
}

func trimAndParseInt64(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}
