package handlers

import (
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
	SeriesDetails     *models.SeriesDetails    `json:"seriesDetails"`
	MovieDetails      *models.Title            `json:"movieDetails"`
	Similar           []models.Title           `json:"similar"`
	Trailers          *models.TrailerResponse   `json:"trailers"`
	ContentPreference *models.ContentPreference `json:"contentPreference"`
	WatchState        *models.SeriesWatchState  `json:"watchState"`
	PlaybackProgress  []models.PlaybackProgress `json:"playbackProgress"`
}

// GetDetailsBundle returns all details-page data in a single response.
func (h *DetailsBundleHandler) GetDetailsBundle(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	userID := strings.TrimSpace(vars["userID"])
	if userID == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	if h.users != nil && !h.users.Exists(userID) {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	query := r.URL.Query()

	contentType := strings.ToLower(strings.TrimSpace(query.Get("type")))
	titleID := strings.TrimSpace(query.Get("titleId"))
	name := strings.TrimSpace(query.Get("name"))
	imdbID := strings.TrimSpace(query.Get("imdbId"))

	year := trimAndParseInt(query.Get("year"))
	season := trimAndParseInt(query.Get("season"))
	tvdbID := trimAndParseInt64(query.Get("tvdbId"))
	tmdbID := trimAndParseInt64(query.Get("tmdbId"))

	bundleStart := time.Now()
	resp := DetailsBundleResponse{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	// 1. Series or Movie details
	wg.Add(1)
	go func() {
		defer wg.Done()
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
			mu.Unlock()
		}
	}()

	// 2. Similar content (requires tmdbId)
	if tmdbID > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			titles, err := h.metadata.Similar(r.Context(), contentType, tmdbID)
			log.Printf("[details-bundle timing] similar: %dms (err=%v)", time.Since(start).Milliseconds(), err)
			if err != nil {
				log.Printf("[details-bundle] similar error: %v", err)
				return
			}
			mu.Lock()
			resp.Similar = titles
			mu.Unlock()
		}()
	}

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

	// 6. Playback progress
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
		mu.Lock()
		resp.PlaybackProgress = items
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// Options handles CORS preflight for the details-bundle endpoint.
func (h *DetailsBundleHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
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
