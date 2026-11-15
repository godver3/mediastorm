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

	"novastream/config"
	"novastream/internal/profileaccess"
	"novastream/models"
	"novastream/services/history"
	metadatapkg "novastream/services/metadata"

	"github.com/gorilla/mux"
)

// DetailsBundleHandler serves a combined details-page payload to reduce the
// number of HTTP round-trips when the frontend opens a title details screen.
// All sub-fetches are performed concurrently.
type DetailsBundleHandler struct {
	metadata          metadataService
	history           historyService
	contentPrefs      contentPreferencesService
	users             userService
	cfgManager        *config.Manager
	userSettings      userSettingsProvider
	watchlist         watchlistMembership
	catalogueObserver func(*http.Request, []models.Title)
}

// movieWatchState answers "is this movie marked watched?" across ID forms.
// Optional on the history service, so lighter fakes need not implement it.
type movieWatchState interface {
	IsMovieWatched(userID, itemID string, externalIDs map[string]string) bool
}

// watchlistMembership answers "is this title on the profile's watchlist?"
// with the watchlist's own identity matching.
type watchlistMembership interface {
	Contains(userID, mediaType, id string, externalIDs map[string]string) bool
}

type seriesDetailsLiteProvider interface {
	SeriesDetailsLite(context.Context, models.SeriesDetailsQuery) (*models.SeriesDetails, error)
}

type localizedMetadataProvider interface {
	WithLanguage(string) *metadatapkg.Service
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

func (h *DetailsBundleHandler) SetConfigManager(cfg *config.Manager) {
	h.cfgManager = cfg
}

func (h *DetailsBundleHandler) SetUserSettingsProvider(provider userSettingsProvider) {
	h.userSettings = provider
}

func (h *DetailsBundleHandler) SetWatchlistService(service watchlistMembership) {
	h.watchlist = service
}

// SetCatalogueObserver registers only metadata actually delivered by this handler.
func (h *DetailsBundleHandler) SetCatalogueObserver(observer func(*http.Request, []models.Title)) {
	h.catalogueObserver = observer
}

func (h *DetailsBundleHandler) observeCatalogue(w http.ResponseWriter, r *http.Request, movie *models.Title, series *models.SeriesDetails, similar []models.Title) {
	if h.catalogueObserver == nil {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	titles := make([]models.Title, 0, 82)
	if movie != nil {
		titles = append(titles, *movie)
	}
	if series != nil {
		titles = append(titles, series.Title)
	}
	if len(similar) > 80 {
		similar = similar[:80]
	}
	titles = append(titles, similar...)
	h.catalogueObserver(r, titles)
}

func (h *DetailsBundleHandler) metadataForUser(userID string) metadataService {
	if h.metadata == nil || h.cfgManager == nil {
		return h.metadata
	}
	localized, ok := h.metadata.(localizedMetadataProvider)
	if !ok {
		return h.metadata
	}
	settings, err := h.cfgManager.Load()
	if err != nil {
		return h.metadata
	}
	language := settings.Metadata.EffectivePrimaryLanguage()
	if h.userSettings != nil && strings.TrimSpace(userID) != "" {
		if profileSettings, err := h.userSettings.Get(userID); err == nil && profileSettings != nil {
			if profileLanguage := strings.TrimSpace(profileSettings.Metadata.PrimaryLanguage); profileLanguage != "" {
				for _, enabled := range settings.Metadata.Language {
					if strings.EqualFold(strings.TrimSpace(enabled), profileLanguage) {
						language = strings.TrimSpace(enabled)
						break
					}
				}
			}
		}
	}
	return localized.WithLanguage(language)
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

	// UpNextEpisode (series only) is the episode the page should offer to play:
	// watchState.nextEpisode when the user has history, otherwise the first
	// episode of the first regular season. Carries a canonical itemId.
	UpNextEpisode *models.EpisodeReference `json:"upNextEpisode,omitempty"`
	// Resume is the server resume decision for the primary playable item: the
	// movie, or UpNextEpisode for series. Present (eligible=false) even when
	// there is no progress, so clients need no thresholds of their own.
	Resume *models.ResumeState `json:"resume,omitempty"`
	// InWatchlist says whether the title is on the profile's watchlist, matched
	// across ID forms. Absent when the server has no watchlist service.
	InWatchlist *bool `json:"inWatchlist,omitempty"`
	// Watched says whether a movie is marked watched, matched across ID forms.
	// Movies only (series carry per-episode watched); absent on older servers.
	Watched *bool `json:"watched,omitempty"`
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

// resolveSeasonType picks the active episode ordering for a series details
// request: an explicit ?seasonType= query param wins (the client just toggled),
// otherwise the user's stored per-series preference is applied.
func (h *DetailsBundleHandler) resolveSeasonType(r *http.Request, userID string, tvdbID int64) string {
	if st := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("seasonType"))); st != "" {
		return st
	}
	if h.history == nil || tvdbID <= 0 {
		return ""
	}
	st, err := h.history.GetSeriesOrdering(userID, tvdbID)
	if err != nil {
		return ""
	}
	return st
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

	if !profileaccess.CheckResource(w, r, userID, "") {
		return
	}

	contentType, titleID, name, imdbID, year, _, tvdbID, tmdbID := parseDetailsRequest(r)
	metadataSvc := h.metadataForUser(userID)
	shellStart := time.Now()
	resp := DetailsShellResponse{}

	switch contentType {
	case "series":
		query := models.SeriesDetailsQuery{
			TitleID:    titleID,
			Name:       name,
			Year:       year,
			TVDBID:     tvdbID,
			TMDBID:     tmdbID,
			SeasonType: h.resolveSeasonType(r, userID, tvdbID),
		}

		start := time.Now()
		if liteProvider, ok := metadataSvc.(seriesDetailsLiteProvider); ok {
			details, err := liteProvider.SeriesDetailsLite(r.Context(), query)
			log.Printf("[details-shell timing] series lite: %dms (err=%v)", time.Since(start).Milliseconds(), err)
			if err == nil {
				resp.SeriesDetails = details
				break
			}
			log.Printf("[details-shell] series lite error, falling back to full details: %v", err)
		}

		start = time.Now()
		details, err := metadataSvc.SeriesDetails(r.Context(), query)
		log.Printf("[details-shell timing] series details fallback: %dms (err=%v)", time.Since(start).Milliseconds(), err)
		if err != nil {
			log.Printf("[details-shell] series shell error: %v", err)
		} else {
			resp.SeriesDetails = details
		}
	case "movie":
		start := time.Now()
		details, err := metadataSvc.MovieDetails(r.Context(), models.MovieDetailsQuery{
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
	h.observeCatalogue(w, r, resp.MovieDetails, resp.SeriesDetails, nil)
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

	if !profileaccess.CheckResource(w, r, userID, "") {
		return
	}

	contentType, titleID, name, imdbID, year, season, tvdbID, tmdbID := parseDetailsRequest(r)
	metadataSvc := h.metadataForUser(userID)

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
			details, err := metadataSvc.SeriesDetails(r.Context(), models.SeriesDetailsQuery{
				TitleID:    titleID,
				Name:       name,
				Year:       year,
				TVDBID:     tvdbID,
				TMDBID:     tmdbID,
				SeasonType: h.resolveSeasonType(r, userID, tvdbID),
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
			details, err := metadataSvc.MovieDetails(r.Context(), models.MovieDetailsQuery{
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

	// 2. Similar content — starts immediately from the request's TMDB ID so it
	// overlaps the details fetch. Details may resolve a different (correct) TMDB
	// ID, in which case similar is fetched again for that ID. Without a request
	// TMDB ID it waits for details to resolve one.
	wg.Add(1)
	go func() {
		defer wg.Done()

		type similarResult struct {
			titles []models.Title
			err    error
		}
		var speculative chan similarResult
		if tmdbID > 0 {
			speculative = make(chan similarResult, 1)
			go func() {
				titles, err := metadataSvc.Similar(r.Context(), contentType, tmdbID)
				speculative <- similarResult{titles: titles, err: err}
			}()
		}

		start := time.Now()
		detailsDone.Wait()
		mu.Lock()
		similarTMDBID := resolvedTMDBID
		mu.Unlock()
		if similarTMDBID == 0 {
			similarTMDBID = tmdbID
		}
		if similarTMDBID <= 0 {
			return
		}

		var titles []models.Title
		var err error
		if speculative != nil && similarTMDBID == tmdbID {
			result := <-speculative
			titles, err = result.titles, result.err
		} else {
			if tmdbID > 0 {
				log.Printf("[details-bundle] similar: using resolved TMDB ID %d instead of request param %d", similarTMDBID, tmdbID)
			}
			titles, err = metadataSvc.Similar(r.Context(), contentType, similarTMDBID)
		}
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
		trailerResp, err := metadataSvc.Trailers(r.Context(), models.TrailerQuery{
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

	// 6. Playback progress — filtered (after details resolve, below) to just
	// this title's items to avoid sending all 293+ items (113 KB) when only
	// 1–20 are needed.
	var allProgress []models.PlaybackProgress
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
		allProgress = items
		mu.Unlock()
	}()

	// 7. Watch history (series only) — used server-side to mark watched
	// episodes; the history itself is not sent to the client.
	var watchHistory []models.WatchHistoryItem
	if contentType == "series" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			items, err := h.history.ListWatchHistory(userID)
			log.Printf("[details-bundle timing] watch history: %dms (err=%v)", time.Since(start).Milliseconds(), err)
			if err != nil {
				log.Printf("[details-bundle] watch history error: %v", err)
				return
			}
			mu.Lock()
			watchHistory = items
			mu.Unlock()
		}()
	}

	wg.Wait()
	// detailsDone is always complete here: the similar goroutine waits on it.
	if allProgress != nil {
		resp.PlaybackProgress = filterProgressForTitle(allProgress, titleID, contentType, detailsBundleExternalIDs(resp, imdbID, tvdbID, tmdbID))
	}
	annotateDetailsBundleProgress(&resp, contentType, titleID, imdbID, tvdbID, tmdbID, watchHistory)
	if h.watchlist != nil {
		mediaType := "movie" // the details fetch above treats anything but series as a movie
		if contentType == "series" {
			mediaType = "series"
		}
		inWatchlist := h.watchlist.Contains(userID, mediaType, titleID, detailsBundleExternalIDs(resp, imdbID, tvdbID, tmdbID).asMap())
		resp.InWatchlist = &inWatchlist
	}
	if contentType != "series" && resp.MovieDetails != nil {
		if watchState, ok := h.history.(movieWatchState); ok {
			watched := watchState.IsMovieWatched(userID, titleID, detailsBundleExternalIDs(resp, imdbID, tvdbID, tmdbID).asMap())
			resp.Watched = &watched
		}
	}
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

	h.observeCatalogue(w, r, resp.MovieDetails, resp.SeriesDetails, resp.Similar)
	writeCompressedJSON(w, r, resp, "details-bundle")
}

// detailsBundleExternalIDs merges the request's provider IDs with those of the
// resolved title, so progress recorded under the resolved provider (e.g. a TMDB
// series ID when the request only carried TVDB) is still matched.
func detailsBundleExternalIDs(resp DetailsBundleResponse, imdbID string, tvdbID, tmdbID int64) titleExternalIDs {
	ext := titleExternalIDs{
		imdb: strings.ToLower(strings.TrimSpace(imdbID)),
		tvdb: nonZeroInt64String(tvdbID),
		tmdb: nonZeroInt64String(tmdbID),
	}
	var resolved *models.Title
	if resp.SeriesDetails != nil {
		resolved = &resp.SeriesDetails.Title
	} else if resp.MovieDetails != nil {
		resolved = resp.MovieDetails
	}
	if resolved != nil {
		if v := strings.ToLower(strings.TrimSpace(resolved.IMDBID)); v != "" {
			ext.imdb = v
		}
		if v := nonZeroInt64String(resolved.TVDBID); v != "" {
			ext.tvdb = v
		}
		if v := nonZeroInt64String(resolved.TMDBID); v != "" {
			ext.tmdb = v
		}
	}
	return ext
}

// annotateDetailsBundleProgress adds the server-computed progress fields to a
// details bundle: `resume` on every playbackProgress row, per-episode
// itemId/watched/resume on series episodes, a merged watchState (resume +
// nextEpisode.itemId), upNextEpisode, and the top-level resume state.
func annotateDetailsBundleProgress(resp *DetailsBundleResponse, contentType, titleID, imdbID string, tvdbID, tmdbID int64, watchHistory []models.WatchHistoryItem) {
	progress := resp.PlaybackProgress
	resp.PlaybackProgress = history.WithProgressResume(progress)

	if contentType != "series" {
		var latest *models.PlaybackProgress
		for i := range progress {
			if progress[i].MediaType != "movie" {
				continue
			}
			if latest == nil || progress[i].UpdatedAt.After(latest.UpdatedAt) {
				latest = &progress[i]
			}
		}
		if latest != nil {
			resp.Resume = history.ProgressResume(*latest)
		} else if resp.MovieDetails != nil {
			resp.Resume = &models.ResumeState{}
		}
		return
	}

	seriesID := titleID
	externalIDs := map[string]string{}
	if imdbID != "" {
		externalIDs["imdb"] = imdbID
	}
	if tvdbID > 0 {
		externalIDs["tvdb"] = strconv.FormatInt(tvdbID, 10)
	}
	if tmdbID > 0 {
		externalIDs["tmdb"] = strconv.FormatInt(tmdbID, 10)
	}
	if resp.SeriesDetails != nil {
		t := resp.SeriesDetails.Title
		if seriesID == "" {
			seriesID = t.ID
		}
		// Resolved metadata IDs beat request params, which may be stale.
		if t.IMDBID != "" {
			externalIDs["imdb"] = t.IMDBID
		}
		if t.TVDBID > 0 {
			externalIDs["tvdb"] = strconv.FormatInt(t.TVDBID, 10)
		}
		if t.TMDBID > 0 {
			externalIDs["tmdb"] = strconv.FormatInt(t.TMDBID, 10)
		}
	}
	idx := history.NewSeriesProgressIndex(seriesID, externalIDs, progress, watchHistory)
	resp.SeriesDetails = idx.AnnotateSeriesDetails(resp.SeriesDetails)

	if resp.WatchState != nil && resp.WatchState.NextEpisode != nil {
		if merged := history.MergeProgressIntoContinueWatching([]models.SeriesWatchState{*resp.WatchState}, progress); len(merged) == 1 {
			resp.WatchState = &merged[0]
		}
	}
	resp.UpNextEpisode = idx.UpNextEpisode(resp.WatchState, resp.SeriesDetails)
	if resp.UpNextEpisode != nil {
		if resume := idx.Resume(resp.UpNextEpisode.SeasonNumber, resp.UpNextEpisode.EpisodeNumber); resume != nil {
			resp.Resume = resume
		} else {
			resp.Resume = &models.ResumeState{}
		}
	}
}

// InteractiveMetadataPriority marks a request's upstream metadata calls as
// interactive, so shared provider rate limiters serve them ahead of queued
// background enrichment. Use it only for routes a person waits on (details).
func InteractiveMetadataPriority(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		next(w, r.WithContext(metadatapkg.WithInteractivePriority(r.Context())))
	}
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
			ID:              t.ID,
			Name:            t.Name,
			Overview:        t.Overview,
			Year:            t.Year,
			Poster:          t.Poster,
			TextPoster:      t.TextPoster,
			Backdrop:        t.Backdrop,
			TextBackdrop:    t.TextBackdrop,
			Backdrops:       t.Backdrops,
			MediaType:       t.MediaType,
			TVDBID:          t.TVDBID,
			IMDBID:          t.IMDBID,
			TMDBID:          t.TMDBID,
			Status:          t.Status,
			LifecycleStatus: t.LifecycleStatus,
			Theatrical:      t.Theatrical,
			HomeRelease:     t.HomeRelease,
			ReleaseDate:     t.ReleaseDate,
			Genres:          t.Genres,
		}
	}
	return slim
}

// titleExternalIDs carries the requested title's provider IDs so progress
// recorded under a different series-ID variant can still be matched.
type titleExternalIDs struct {
	imdb string // lowercased
	tvdb string // numeric string, "" when unknown
	tmdb string // numeric string, "" when unknown
}

// asMap returns the known IDs keyed the way external ID maps are elsewhere.
func (ext titleExternalIDs) asMap() map[string]string {
	ids := map[string]string{}
	if ext.imdb != "" {
		ids["imdb"] = ext.imdb
	}
	if ext.tvdb != "" {
		ids["tvdb"] = ext.tvdb
	}
	if ext.tmdb != "" {
		ids["tmdb"] = ext.tmdb
	}
	return ids
}

// filterProgressForTitle returns only playback progress items relevant to the
// given title. For movies, this is typically 1 item; for series, it's the
// episodes of that series. This avoids sending all ~300 items (113 KB) when
// only a handful are needed.
//
// Beyond the exact titleID, it matches on shared external IDs (tmdb/tvdb/imdb)
// so episodes recorded under a non-canonical series-ID variant (split series
// IDs) are still surfaced — the same class of bug that hid progress on the
// continue-watching shelf.
func filterProgressForTitle(items []models.PlaybackProgress, titleID, contentType string, ext titleExternalIDs) []models.PlaybackProgress {
	if titleID == "" && ext.imdb == "" && ext.tvdb == "" && ext.tmdb == "" {
		return items
	}
	prefix := titleID + ":"
	var filtered []models.PlaybackProgress
	for _, p := range items {
		// Match by seriesId (episodes of this series) or by itemId/ID (direct match for movies)
		if titleID != "" && (p.SeriesID == titleID || p.ItemID == titleID || p.ID == titleID ||
			strings.HasPrefix(p.ItemID, prefix) || strings.HasPrefix(p.ID, prefix)) {
			filtered = append(filtered, p)
			continue
		}
		if progressMatchesTitleExternalIDs(p, ext, contentType) {
			filtered = append(filtered, p)
		}
	}
	if filtered == nil {
		filtered = []models.PlaybackProgress{}
	}
	return filtered
}

// progressMatchesTitleExternalIDs reports whether a progress entry shares an
// external ID with the requested title. Media kind is checked first so that a
// movie and a series carrying the same numeric tmdb/tvdb ID cannot cross-match.
func progressMatchesTitleExternalIDs(p models.PlaybackProgress, ext titleExternalIDs, contentType string) bool {
	if len(p.ExternalIDs) == 0 {
		return false
	}
	switch contentType {
	case "series":
		if p.MediaType != "episode" && p.MediaType != "series" {
			return false
		}
	case "movie":
		if p.MediaType != "movie" {
			return false
		}
	}
	if ext.tvdb != "" && strings.TrimSpace(p.ExternalIDs["tvdb"]) == ext.tvdb {
		return true
	}
	if ext.tmdb != "" && strings.TrimSpace(p.ExternalIDs["tmdb"]) == ext.tmdb {
		return true
	}
	if ext.imdb != "" && strings.EqualFold(strings.TrimSpace(p.ExternalIDs["imdb"]), ext.imdb) {
		return true
	}
	return false
}

// nonZeroInt64String renders a positive int64 as a decimal string, returning ""
// for zero/negative so unknown IDs don't produce spurious matches.
func nonZeroInt64String(v int64) string {
	if v <= 0 {
		return ""
	}
	return strconv.FormatInt(v, 10)
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
