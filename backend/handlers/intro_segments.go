package handlers

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"novastream/services/skipsegments"
)

// skipSegmentService is shared so upstream IntroDB/SkipDB lookups are cached
// across clients. Tests replace it with one pointed at httptest fakes.
var skipSegmentService = skipsegments.New(skipsegments.Options{})

const (
	// Overall budget for one request; each provider also has its own shorter timeout.
	skipSegmentRequestTimeout = 8 * time.Second
	maxSkipSegmentChapters    = 500
	maxSkipSegmentBodyBytes   = 256 * 1024
	maxSkipSegmentTitleLength = 200
)

type legacySegment struct {
	StartMS         *float64 `json:"start_ms"`
	EndMS           *float64 `json:"end_ms"`
	Confidence      float64  `json:"confidence,omitempty"`
	SubmissionCount int      `json:"submission_count,omitempty"`
	Source          string   `json:"source,omitempty"`
}

// introDBSegmentsResponse is the GET /video/segments payload. The intro/recap/outro
// fields are the original IntroDB-then-SkipDB shape used by the web player;
// Segments is the additive, normalized merge (no chapters on GET).
type introDBSegmentsResponse struct {
	IMDbID   string                 `json:"imdb_id,omitempty"`
	Season   int                    `json:"season,omitempty"`
	Episode  int                    `json:"episode,omitempty"`
	Intro    *legacySegment         `json:"intro"`
	Recap    *legacySegment         `json:"recap"`
	Outro    *legacySegment         `json:"outro"`
	Segments []skipsegments.Segment `json:"segments"`
}

// resolveSkipSegmentsRequest is the POST /video/segments body. All fields are
// optional: providers are queried only for a valid imdbId+season+episode, and
// chapter names are always merged as the lowest-priority source.
type resolveSkipSegmentsRequest struct {
	IMDbID   string                 `json:"imdbId"`
	Season   int                    `json:"season"`
	Episode  int                    `json:"episode"`
	Duration float64                `json:"duration"`
	Chapters []skipsegments.Chapter `json:"chapters"`
}

type resolveSkipSegmentsResponse struct {
	Segments []skipsegments.Segment `json:"segments"`
}

// GetIntroSegments serves /video/segments.
//   - GET  (imdbId, season, episode, duration query params): IntroDB, then SkipDB.
//     Kept for the web player and older clients.
//   - POST (JSON body with optional chapters): full IntroDB → SkipDB → chapter merge.
func (h *VideoHandler) GetIntroSegments(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodOptions:
		h.HandleOptions(w, r)
	case http.MethodGet:
		h.getLegacyIntroSegments(w, r)
	case http.MethodPost:
		h.resolveSkipSegments(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *VideoHandler) getLegacyIntroSegments(w http.ResponseWriter, r *http.Request) {
	season, seasonErr := strconv.Atoi(r.URL.Query().Get("season"))
	episode, episodeErr := strconv.Atoi(r.URL.Query().Get("episode"))
	key, keyErr := skipsegments.NormalizeEpisodeKey(r.URL.Query().Get("imdbId"), season, episode)
	if keyErr != nil || seasonErr != nil || episodeErr != nil {
		http.Error(w, "valid imdbId, season, and episode are required", http.StatusBadRequest)
		return
	}
	duration, _ := strconv.ParseFloat(r.URL.Query().Get("duration"), 64)
	duration = math.Round(skipsegments.SanitizeDuration(duration))

	ctx, cancel := context.WithTimeout(r.Context(), skipSegmentRequestTimeout)
	defer cancel()
	lookup := skipSegmentService.Lookup(ctx, key, duration)
	if lookup.IntroDBErr != nil && lookup.SkipDBErr != nil {
		http.Error(w, "intro segments unavailable", http.StatusBadGateway)
		return
	}

	result := introDBSegmentsResponse{IMDbID: key.IMDbID, Season: key.Season, Episode: key.Episode}
	if lookup.IntroDB != nil {
		result.Intro = legacyProviderSegment(lookup.IntroDB.Intro, "", duration)
		result.Recap = legacyProviderSegment(lookup.IntroDB.Recap, "", duration)
		result.Outro = legacyOutroSegment(lookup.IntroDB.Outro, "", duration)
	}
	if lookup.SkipDB != nil {
		if result.Intro == nil {
			result.Intro = legacyProviderSegment(lookup.SkipDB.Segments.Intro, skipsegments.SourceSkipDB, duration)
		}
		if result.Recap == nil {
			result.Recap = legacyProviderSegment(lookup.SkipDB.Segments.Recap, skipsegments.SourceSkipDB, duration)
		}
		if result.Outro == nil {
			result.Outro = legacyOutroSegment(lookup.SkipDB.Segments.Outro, skipsegments.SourceSkipDB, duration)
		}
	}
	result.Segments = skipsegments.Resolve(lookup.IntroDB, lookup.SkipDB, nil, duration)
	writeSkipSegmentsJSON(w, result)
}

// legacyProviderSegment applies the shared validation in the millisecond legacy
// shape. IntroDB entries keep their metadata and carry no source tag.
func legacyProviderSegment(segment *skipsegments.ProviderSegment, source string, duration float64) *legacySegment {
	validateAs := source
	if validateAs == "" {
		validateAs = skipsegments.SourceIntroDB
	}
	startMS, endMS, ok := skipsegments.ValidProviderRange(validateAs, segment, duration)
	if !ok {
		return nil
	}
	legacy := &legacySegment{StartMS: &startMS, EndMS: &endMS, Source: source}
	if source == "" {
		legacy.Confidence = segment.Confidence
		legacy.SubmissionCount = segment.SubmissionCount
	}
	return legacy
}

// legacyOutroSegment is legacyProviderSegment plus the resolver's check that the
// credits finish near the end of this file (not a different cut's).
func legacyOutroSegment(segment *skipsegments.ProviderSegment, source string, duration float64) *legacySegment {
	legacy := legacyProviderSegment(segment, source, duration)
	if legacy == nil || !skipsegments.OutroFitsDuration(*legacy.EndMS, duration) {
		return nil
	}
	return legacy
}

func (h *VideoHandler) resolveSkipSegments(w http.ResponseWriter, r *http.Request) {
	var request resolveSkipSegmentsRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSkipSegmentBodyBytes+1))
	if err != nil || len(body) > maxSkipSegmentBodyBytes {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &request); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
	}
	if len(request.Chapters) > maxSkipSegmentChapters {
		request.Chapters = request.Chapters[:maxSkipSegmentChapters]
	}
	for index := range request.Chapters {
		if title := []rune(request.Chapters[index].Title); len(title) > maxSkipSegmentTitleLength {
			request.Chapters[index].Title = string(title[:maxSkipSegmentTitleLength])
		}
	}
	duration := skipsegments.SanitizeDuration(request.Duration)

	var lookup skipsegments.LookupResult
	if key, err := skipsegments.NormalizeEpisodeKey(request.IMDbID, request.Season, request.Episode); err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), skipSegmentRequestTimeout)
		lookup = skipSegmentService.Lookup(ctx, key, duration)
		cancel()
	}
	// Provider failures degrade to chapter-only results; this endpoint never fails playback.
	writeSkipSegmentsJSON(w, resolveSkipSegmentsResponse{
		Segments: skipsegments.Resolve(lookup.IntroDB, lookup.SkipDB, request.Chapters, duration),
	})
}

func writeSkipSegmentsJSON(w http.ResponseWriter, response any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, max-age=300")
	_ = json.NewEncoder(w).Encode(response)
}
