package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var introDBSegmentsURL = "https://api.introdb.app/segments"

var introDBIMDbPattern = regexp.MustCompile(`^tt[0-9]+$`)

type introDBSegment struct {
	StartMS         *int64  `json:"start_ms"`
	EndMS           *int64  `json:"end_ms"`
	Confidence      float64 `json:"confidence"`
	SubmissionCount int     `json:"submission_count"`
}

type introDBSegmentsResponse struct {
	IMDbID  string          `json:"imdb_id,omitempty"`
	Season  int             `json:"season,omitempty"`
	Episode int             `json:"episode,omitempty"`
	Intro   *introDBSegment `json:"intro"`
	Recap   *introDBSegment `json:"recap"`
	Outro   *introDBSegment `json:"outro"`
}

type cachedIntroDBSegments struct {
	response  introDBSegmentsResponse
	expiresAt time.Time
}

var webIntroDBCache = struct {
	sync.RWMutex
	entries map[string]cachedIntroDBSegments
}{entries: make(map[string]cachedIntroDBSegments)}

var webIntroDBHTTPClient = &http.Client{Timeout: 6 * time.Second}

// GetIntroSegments proxies IntroDB for the standalone web player. IntroDB only
// allows browser requests from its own origin, so same-origin playback needs a
// small authenticated backend bridge.
func (h *VideoHandler) GetIntroSegments(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		h.HandleOptions(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	imdbID := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("imdbId")))
	season, seasonErr := strconv.Atoi(r.URL.Query().Get("season"))
	episode, episodeErr := strconv.Atoi(r.URL.Query().Get("episode"))
	if !introDBIMDbPattern.MatchString(imdbID) || seasonErr != nil || episodeErr != nil || season <= 0 || episode <= 0 {
		http.Error(w, "valid imdbId, season, and episode are required", http.StatusBadRequest)
		return
	}

	cacheKey := fmt.Sprintf("%s:%d:%d", imdbID, season, episode)
	if cached, ok := getCachedIntroDBSegments(cacheKey); ok {
		writeIntroDBSegments(w, cached)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	query := url.Values{
		"imdb_id": {imdbID},
		"season":  {strconv.Itoa(season)},
		"episode": {strconv.Itoa(episode)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, introDBSegmentsURL+"?"+query.Encode(), nil)
	if err != nil {
		http.Error(w, "failed to create segment request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "MediaStorm-WebPlayer/1.0")

	resp, err := webIntroDBHTTPClient.Do(req)
	if err != nil {
		http.Error(w, "intro segments unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		empty := introDBSegmentsResponse{IMDbID: imdbID, Season: season, Episode: episode}
		cacheIntroDBSegments(cacheKey, empty, 30*time.Minute)
		writeIntroDBSegments(w, empty)
		return
	}
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "intro segments unavailable", http.StatusBadGateway)
		return
	}

	var result introDBSegmentsResponse
	decoder := json.NewDecoder(http.MaxBytesReader(w, resp.Body, 128*1024))
	if err := decoder.Decode(&result); err != nil {
		http.Error(w, "invalid intro segment response", http.StatusBadGateway)
		return
	}
	result.IMDbID = imdbID
	result.Season = season
	result.Episode = episode
	result.Intro = validIntroDBSegment(result.Intro)
	result.Recap = validIntroDBSegment(result.Recap)
	result.Outro = validIntroDBSegment(result.Outro)
	cacheIntroDBSegments(cacheKey, result, 6*time.Hour)
	writeIntroDBSegments(w, result)
}

func validIntroDBSegment(segment *introDBSegment) *introDBSegment {
	if segment == nil || segment.StartMS == nil || segment.EndMS == nil || *segment.StartMS < 0 || *segment.EndMS <= *segment.StartMS {
		return nil
	}
	return segment
}

func getCachedIntroDBSegments(key string) (introDBSegmentsResponse, bool) {
	webIntroDBCache.RLock()
	entry, ok := webIntroDBCache.entries[key]
	webIntroDBCache.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		if ok {
			webIntroDBCache.Lock()
			delete(webIntroDBCache.entries, key)
			webIntroDBCache.Unlock()
		}
		return introDBSegmentsResponse{}, false
	}
	return entry.response, true
}

func cacheIntroDBSegments(key string, response introDBSegmentsResponse, ttl time.Duration) {
	webIntroDBCache.Lock()
	webIntroDBCache.entries[key] = cachedIntroDBSegments{response: response, expiresAt: time.Now().Add(ttl)}
	webIntroDBCache.Unlock()
}

func writeIntroDBSegments(w http.ResponseWriter, response introDBSegmentsResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, max-age=300")
	_ = json.NewEncoder(w).Encode(response)
}
