package handlers

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/models"
	"novastream/services/kids"
)

const (
	personalizedDefaultDays         = 14
	personalizedDefaultLimitPerType = 20
	personalizedMaxLimitPerType     = 40
	personalizedSeedSampleSize      = 12
	personalizedSimilarLimitPerSeed = 28
	personalizedMaxPerPrimarySeed   = 4
	personalizedTopTenBudget        = 1100 * time.Millisecond
	personalizedResponseCacheTTL    = 30 * time.Minute
	personalizedSimilarConcurrency  = 4
)

type PersonalizedRecommendationsResponse struct {
	Items       []models.TrendingItem                  `json:"items"`
	Movies      []models.TrendingItem                  `json:"movies"`
	Series      []models.TrendingItem                  `json:"series"`
	Total       int                                    `json:"total"`
	SeedCount   int                                    `json:"seedCount,omitempty"`
	Explanation *PersonalizedRecommendationExplanation `json:"explanation,omitempty"`
}

type PersonalizedRecommendationExplanation struct {
	Summary         string                           `json:"summary"`
	Days            int                              `json:"days"`
	SeedCount       int                              `json:"seedCount"`
	Seeds           []PersonalizedRecommendationSeed `json:"seeds"`
	PopularityBoost bool                             `json:"popularityBoost"`
	ExcludesKnown   bool                             `json:"excludesKnown"`
}

type PersonalizedRecommendationSeed struct {
	Title     string `json:"title"`
	MediaType string `json:"mediaType"`
	TMDBID    int64  `json:"tmdbId"`
	Source    string `json:"source"`
	Activity  string `json:"activity,omitempty"`
}

type personalizedSeed struct {
	Key       string
	MediaType string
	TMDBID    int64
	Title     string
	Activity  time.Time
	Strength  float64
	Source    string
}

type personalizedCandidate struct {
	Title            models.Title
	Score            float64
	SourceCount      int
	PrimarySeed      string
	PrimarySeedScore float64
	TopTenRank       int
}

type personalizedTopTenResult struct {
	Kind  string
	Items []models.TrendingItem
	Err   error
}

type personalizedKidsRatingFilter struct {
	enabled        bool
	maxMovieRating string
	maxTVRating    string
}

type personalizedBuildDiagnostics struct {
	HistoryCount         int
	ProgressCount        int
	SeedCount            int
	RecentWatchedCount   int
	RecentProgressCount  int
	MissingTMDBCount     int
	SimilarItemCount     int
	SimilarErrorCount    int
	TopTenItemCount      int
	TrendingItemCount    int
	TrendingErrorCount   int
	MovieCandidateCount  int
	SeriesCandidateCount int
}

type personalizedRecommendationsCacheEntry struct {
	response  PersonalizedRecommendationsResponse
	expiresAt time.Time
}

type personalizedRecommendationsBuild struct {
	done     chan struct{}
	response PersonalizedRecommendationsResponse
}

// GetPersonalizedRecommendations returns a non-AI "Recommended For You" shelf.
// It uses recent watch/progress activity as seeds, the details-page Similar
// engine as the candidate generator, and today's top-ten/trending shelves as
// popularity-biased backfill. All watch-history and playback-progress titles
// are excluded from the output.
func (h *MetadataHandler) GetPersonalizedRecommendations(w http.ResponseWriter, r *http.Request) {
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	if userID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "userId is required"})
		return
	}
	if h.HistoryService == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "history service not configured"})
		return
	}

	days := parsePersonalizedIntParam(r, "days", personalizedDefaultDays, 1, 90)
	limitPerType := parsePersonalizedIntParam(r, "limitPerType", personalizedDefaultLimitPerType, 1, personalizedMaxLimitPerType)

	history, err := h.HistoryService.ListWatchHistory(userID)
	if err != nil {
		log.Printf("[metadata] personalized recommendations history error user=%s: %v", userID, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	progress, err := h.HistoryService.ListPlaybackProgress(userID)
	if err != nil {
		log.Printf("[metadata] personalized recommendations progress error user=%s: %v", userID, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	if h.UsersService != nil {
		if profile, ok := h.UsersService.Get(userID); ok && profile.IsKidsProfile && profile.KidsMode == "content_list" {
			h.writePersonalizedRecommendations(w, r, userID, PersonalizedRecommendationsResponse{Items: []models.TrendingItem{}, Movies: []models.TrendingItem{}, Series: []models.TrendingItem{}}, history, progress)
			return
		}
	}
	additional := h.additionalRecommendationSeeds(userID)
	kidsRatingFilter := h.personalizedKidsRatingFilter(userID)
	cacheKey := personalizedRecommendationsCacheKey(userID, days, limitPerType, history, progress)
	language := ""
	if h.CfgManager != nil {
		if cfg, err := h.CfgManager.Load(); err == nil {
			language, _ = resolveMetadataLanguage(cfg, h.UserSettings, userID)
		}
	}
	cacheKey += ":" + recommendationInputsFingerprint(additional, h.recommendationSettings(userID), kidsRatingFilter, language)
	if response, ok := h.cachedPersonalizedRecommendations(cacheKey); ok {
		log.Printf("[metadata] personalized recommendations cache hit user=%s seeds=%d items=%d", userID, response.SeedCount, len(response.Items))
		h.writePersonalizedRecommendations(w, r, userID, response, history, progress)
		return
	}
	build, leader := h.beginPersonalizedRecommendationsBuild(cacheKey)
	if !leader {
		select {
		case <-r.Context().Done():
			return
		case <-build.done:
			log.Printf("[metadata] personalized recommendations shared in-flight result user=%s seeds=%d items=%d", userID, build.response.SeedCount, len(build.response.Items))
			h.writePersonalizedRecommendations(w, r, userID, build.response, history, progress)
			return
		}
	}

	resp := h.buildPersonalizedRecommendations(r.Context(), userID, history, progress, days, limitPerType, kidsRatingFilter, additional, parseShelfLoadOptions(r).DeferArtwork)
	service := h.serviceForUser(userID)
	if resp.Items == nil {
		resp.Items = []models.TrendingItem{}
	}
	if resp.Movies == nil {
		resp.Movies = []models.TrendingItem{}
	}
	if resp.Series == nil {
		resp.Series = []models.TrendingItem{}
	}

	if len(resp.Items) > 0 {
		enrichTrendingRatings(resp.Items, service)
	}
	if len(resp.Movies) > 0 {
		enrichTrendingRatings(resp.Movies, service)
	}
	if len(resp.Series) > 0 {
		enrichTrendingRatings(resp.Series, service)
	}
	cacheResponse := r.Context().Err() == nil && (resp.SeedCount == 0 || len(resp.Items) > 0)
	if !cacheResponse && r.Context().Err() == nil {
		log.Printf("[metadata] personalized recommendations empty result not cached user=%s seeds=%d", userID, resp.SeedCount)
	}
	h.finishPersonalizedRecommendationsBuild(cacheKey, build, resp, cacheResponse)
	h.writePersonalizedRecommendations(w, r, userID, resp, history, progress)
}

func (h *MetadataHandler) writePersonalizedRecommendations(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	response PersonalizedRecommendationsResponse,
	history []models.WatchHistoryItem,
	progress []models.PlaybackProgress,
) {
	service := h.serviceForUser(userID)
	// Cached/in-flight responses may be shared with other clients. Artwork
	// refreshes update copies and must not mutate their base cards.
	response.Items = append(make([]models.TrendingItem, 0, len(response.Items)), response.Items...)
	response.Movies = append(make([]models.TrendingItem, 0, len(response.Movies)), response.Movies...)
	response.Series = append(make([]models.TrendingItem, 0, len(response.Series)), response.Series...)
	deferArtwork := parseShelfLoadOptions(r).DeferArtwork
	enrichPersonalizedArtwork(r.Context(), response.Items, service, deferArtwork)
	enrichPersonalizedArtwork(r.Context(), response.Movies, service, true)
	enrichPersonalizedArtwork(r.Context(), response.Series, service, true)
	if len(response.Items) > 0 {
		cw, _ := h.HistoryService.ListSeriesStates(userID)
		idx := buildWatchStateIndex(history, cw, progress)
		warmEpisodeCounts := strings.EqualFold(r.URL.Query().Get("includeUnwatchedCounts"), "true")
		enrichTrendingItems(response.Items, idx, service, warmEpisodeCounts)
		enrichTrendingItems(response.Movies, idx, service, warmEpisodeCounts)
		enrichTrendingItems(response.Series, idx, service, warmEpisodeCounts)
	}
	policy := resolveUnreleasedVisibilityPolicy(
		h.CfgManager,
		h.UserSettings,
		h.ClientSettings,
		userID,
		requestClientID(r),
		unreleasedVisibilityLists,
	)
	if !policy.IncludeMovies {
		enrichTrendingMovieReleaseVisibility(r.Context(), response.Items, service)
		movieReleases := make(map[int64]models.Title, len(response.Items))
		for i := range response.Items {
			if response.Items[i].Title.TMDBID > 0 && strings.EqualFold(response.Items[i].Title.MediaType, "movie") {
				movieReleases[response.Items[i].Title.TMDBID] = response.Items[i].Title
			}
		}
		for i := range response.Movies {
			if enriched, ok := movieReleases[response.Movies[i].Title.TMDBID]; ok {
				response.Movies[i].Title.Status = enriched.Status
				response.Movies[i].Title.Theatrical = enriched.Theatrical
				response.Movies[i].Title.HomeRelease = enriched.HomeRelease
			}
		}
	}
	response = filterPersonalizedRecommendationsByUnreleasedVisibility(response, policy)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func filterPersonalizedRecommendationsByUnreleasedVisibility(response PersonalizedRecommendationsResponse, policy unreleasedVisibilityPolicy) PersonalizedRecommendationsResponse {
	response.Items = filterTrendingItemsByUnreleasedVisibility(response.Items, policy)
	response.Movies = filterTrendingItemsByUnreleasedVisibility(response.Movies, policy)
	response.Series = filterTrendingItemsByUnreleasedVisibility(response.Series, policy)
	response.Total = len(response.Items)
	return response
}

func personalizedRecommendationsCacheKey(
	userID string,
	days int,
	limitPerType int,
	history []models.WatchHistoryItem,
	progress []models.PlaybackProgress,
) string {
	records := make([]string, 0, len(history)+len(progress))
	for _, item := range history {
		records = append(records, strings.Join([]string{
			"h", item.ID, item.MediaType, item.ItemID, item.SeriesID, item.Name, item.SeriesName,
			strconv.FormatBool(item.Watched),
			strconv.FormatInt(item.WatchedAt.UnixNano(), 10),
			strconv.FormatInt(item.UpdatedAt.UnixNano(), 10),
			personalizedExternalIDsFingerprint(item.ExternalIDs),
		}, "\x00"))
	}
	for _, item := range progress {
		records = append(records, strings.Join([]string{
			"p", item.ID, item.MediaType, item.ItemID, item.SeriesID, item.MovieName, item.SeriesName,
			strconv.FormatFloat(item.PercentWatched, 'f', 3, 64),
			strconv.FormatBool(item.HiddenFromContinueWatching),
			strconv.FormatInt(item.UpdatedAt.UnixNano(), 10),
			personalizedExternalIDsFingerprint(item.ExternalIDs),
		}, "\x00"))
	}
	sort.Strings(records)
	hash := fnv.New64a()
	for _, record := range records {
		_, _ = hash.Write([]byte(record))
		_, _ = hash.Write([]byte{'\n'})
	}
	return strings.Join([]string{
		userID,
		strconv.Itoa(days),
		strconv.Itoa(limitPerType),
		strconv.FormatUint(hash.Sum64(), 16),
	}, ":")
}

func personalizedExternalIDsFingerprint(ids map[string]string) string {
	if len(ids) == 0 {
		return ""
	}
	keys := make([]string, 0, len(ids))
	for key := range ids {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, strings.ToLower(strings.TrimSpace(key))+"="+strings.TrimSpace(ids[key]))
	}
	return strings.Join(parts, "\x1f")
}

func (h *MetadataHandler) cachedPersonalizedRecommendations(key string) (PersonalizedRecommendationsResponse, bool) {
	h.personalizedMu.Lock()
	defer h.personalizedMu.Unlock()
	now := time.Now()
	for cachedKey, cached := range h.personalizedCache {
		if now.After(cached.expiresAt) {
			delete(h.personalizedCache, cachedKey)
		}
	}
	entry, ok := h.personalizedCache[key]
	if !ok {
		return PersonalizedRecommendationsResponse{}, false
	}
	return entry.response, true
}

func (h *MetadataHandler) beginPersonalizedRecommendationsBuild(key string) (*personalizedRecommendationsBuild, bool) {
	h.personalizedMu.Lock()
	defer h.personalizedMu.Unlock()
	if build, ok := h.personalizedInFlight[key]; ok {
		return build, false
	}
	build := &personalizedRecommendationsBuild{done: make(chan struct{})}
	h.personalizedInFlight[key] = build
	return build, true
}

func (h *MetadataHandler) finishPersonalizedRecommendationsBuild(
	key string,
	build *personalizedRecommendationsBuild,
	response PersonalizedRecommendationsResponse,
	cacheResponse bool,
) {
	h.personalizedMu.Lock()
	build.response = response
	if cacheResponse {
		h.personalizedCache[key] = personalizedRecommendationsCacheEntry{
			response:  response,
			expiresAt: time.Now().Add(personalizedResponseCacheTTL),
		}
	}
	delete(h.personalizedInFlight, key)
	close(build.done)
	h.personalizedMu.Unlock()
}

func (h *MetadataHandler) personalizedKidsRatingFilter(userID string) personalizedKidsRatingFilter {
	if userID == "" || h.UsersService == nil {
		return personalizedKidsRatingFilter{}
	}

	user, ok := h.UsersService.Get(userID)
	if !ok || !user.IsKidsProfile || user.KidsMode != "rating" {
		return personalizedKidsRatingFilter{}
	}
	movieRating := user.KidsMaxMovieRating
	tvRating := user.KidsMaxTVRating
	if movieRating == "" && tvRating == "" && user.KidsMaxRating != "" {
		movieRating = user.KidsMaxRating
		tvRating = user.KidsMaxRating
	}

	return personalizedKidsRatingFilter{
		enabled:        strings.TrimSpace(movieRating) != "" || strings.TrimSpace(tvRating) != "",
		maxMovieRating: movieRating,
		maxTVRating:    tvRating,
	}
}

func parsePersonalizedIntParam(r *http.Request, name string, fallback, minValue, maxValue int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func (h *MetadataHandler) fetchPersonalizedTopTen(ctx context.Context, userID string) ([]models.TrendingItem, []models.TrendingItem, []models.TrendingItem) {
	topTenCtx, cancel := context.WithTimeout(ctx, personalizedTopTenBudget)
	defer cancel()
	service := h.serviceForUser(userID)

	requests := []struct {
		kind      string
		mediaType string
	}{
		{kind: "all", mediaType: "all"},
		{kind: "movie", mediaType: "movie"},
		{kind: "series", mediaType: "tv"},
	}
	results := make(chan personalizedTopTenResult, len(requests))
	for _, request := range requests {
		request := request
		go func() {
			items, err := service.GetTopTen(topTenCtx, request.mediaType, nil)
			select {
			case results <- personalizedTopTenResult{Kind: request.kind, Items: items, Err: err}:
			case <-topTenCtx.Done():
			}
		}()
	}

	var all, movies, series []models.TrendingItem
	for remaining := len(requests); remaining > 0; {
		select {
		case result := <-results:
			remaining--
			if result.Err != nil {
				log.Printf("[metadata] personalized recommendations top-ten %s skipped user=%s: %v", result.Kind, userID, result.Err)
				continue
			}
			switch result.Kind {
			case "all":
				all = result.Items
			case "movie":
				movies = result.Items
			case "series":
				series = result.Items
			}
		case <-topTenCtx.Done():
			if ctx.Err() == nil {
				log.Printf("[metadata] personalized recommendations top-ten budget exceeded user=%s budget=%s", userID, personalizedTopTenBudget)
			}
			return all, movies, series
		}
	}
	return all, movies, series
}

func (h *MetadataHandler) buildPersonalizedRecommendations(
	ctx context.Context,
	userID string,
	history []models.WatchHistoryItem,
	progress []models.PlaybackProgress,
	days int,
	limitPerType int,
	kidsRatingFilter personalizedKidsRatingFilter,
	additional []personalizedSeed,
	deferArtwork ...bool,
) PersonalizedRecommendationsResponse {
	startedAt := time.Now()
	service := h.serviceForUser(userID)
	now := time.Now().UTC()
	cutoff := now.AddDate(0, 0, -days)
	excluded := buildPersonalizedExclusion(history, progress)
	seeds := buildPersonalizedSeeds(userID, history, progress, cutoff, now, personalizedSeedSampleSize, additional...)
	for _, seed := range additional {
		for key := range titleExclusionKeys(models.Title{MediaType: seed.MediaType, TMDBID: seed.TMDBID}) {
			excluded[key] = struct{}{}
		}
	}
	diagnostics := summarizePersonalizedSeedEligibility(history, progress, cutoff)
	diagnostics.SeedCount = len(seeds)
	if len(seeds) == 0 {
		logPersonalizedBuildDiagnostics(userID, diagnostics, 0, nil, time.Since(startedAt))
		return PersonalizedRecommendationsResponse{
			Items:  []models.TrendingItem{},
			Movies: []models.TrendingItem{},
			Series: []models.TrendingItem{},
			Total:  0,
		}
	}

	movieCandidates := make(map[string]*personalizedCandidate)
	seriesCandidates := make(map[string]*personalizedCandidate)

	topTenRank := make(map[string]int)
	addTopTenRanks := func(items []models.TrendingItem) {
		for idx, item := range items {
			rank := item.Rank
			if rank <= 0 {
				rank = idx + 1
			}
			for key := range titleExclusionKeys(item.Title) {
				if existing, ok := topTenRank[key]; !ok || rank < existing {
					topTenRank[key] = rank
				}
			}
		}
	}

	topTenAll, topTenMovies, topTenSeries := h.fetchPersonalizedTopTen(ctx, userID)
	diagnostics.TopTenItemCount = len(topTenAll) + len(topTenMovies) + len(topTenSeries)
	addTopTenRanks(topTenAll)
	addTopTenRanks(topTenMovies)
	addTopTenRanks(topTenSeries)

	addCandidate := func(title models.Title, score float64, primarySeed string) {
		mediaType := normalizePersonalizedMediaType(title.MediaType)
		if mediaType == "" || title.Adult {
			return
		}
		title.MediaType = mediaType
		if kidsRatingFilter.enabled && !isPersonalizedRatingAllowed(title, kidsRatingFilter) {
			return
		}
		keys := titleExclusionKeys(title)
		if intersectsStringSet(keys, excluded) {
			return
		}
		canonical := personalizedCanonicalTitleKey(title)
		if canonical == "" {
			return
		}

		bucket := movieCandidates
		if mediaType == "series" {
			bucket = seriesCandidates
		}
		adjustedScore := score * personalizedPopularityWeight(title)
		candidate := bucket[canonical]
		if candidate == nil {
			candidate = &personalizedCandidate{Title: title}
			candidate.Score += personalizedCandidateQualityScore(title)
			if !strings.HasPrefix(primarySeed, "top-ten") {
				for key := range keys {
					if rank, ok := topTenRank[key]; ok {
						candidate.TopTenRank = rank
						candidate.Score += maxFloat(0, 38-float64(rank)*2.8)
						break
					}
				}
			}
			bucket[canonical] = candidate
		}
		candidate.Score += adjustedScore
		candidate.SourceCount++
		if primarySeed != "" && adjustedScore > candidate.PrimarySeedScore {
			candidate.PrimarySeed = primarySeed
			candidate.PrimarySeedScore = adjustedScore
		}
	}

	type similarResult struct {
		titles []models.Title
		err    error
	}
	similarResults := make([]similarResult, len(seeds))
	sem := make(chan struct{}, personalizedSimilarConcurrency)
	var similarWG sync.WaitGroup
	for index, seed := range seeds {
		index, seed := index, seed
		similarWG.Add(1)
		go func() {
			defer similarWG.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				similarResults[index].err = ctx.Err()
				return
			}
			similarResults[index].titles, similarResults[index].err = service.Similar(ctx, seed.MediaType, seed.TMDBID)
		}()
	}
	similarWG.Wait()

	for index, seed := range seeds {
		result := similarResults[index]
		if result.err != nil {
			diagnostics.SimilarErrorCount++
			log.Printf("[metadata] personalized recommendations similar skipped user=%s seedIndex=%d type=%s: %v", userID, index, seed.MediaType, result.err)
			continue
		}
		diagnostics.SimilarItemCount += len(result.titles)
		for idx, title := range result.titles {
			if idx >= personalizedSimilarLimitPerSeed {
				break
			}
			rankScore := maxFloat(0, 52-float64(idx)*1.9)
			recencyScore := personalizedRecencyScore(seed.Activity, cutoff, now) * 14
			addCandidate(title, rankScore*seed.Strength+recencyScore, seed.Key)
		}
	}

	if ctx.Err() != nil {
		movies := finalizePersonalizedBucket(movieCandidates, limitPerType)
		series := finalizePersonalizedBucket(seriesCandidates, limitPerType)
		items := interleavePersonalizedItems(movies, series, limitPerType)
		diagnostics.MovieCandidateCount = len(movieCandidates)
		diagnostics.SeriesCandidateCount = len(seriesCandidates)
		logPersonalizedBuildDiagnostics(userID, diagnostics, len(items), ctx.Err(), time.Since(startedAt))
		return PersonalizedRecommendationsResponse{
			Items:       items,
			Movies:      movies,
			Series:      series,
			Total:       len(items),
			SeedCount:   len(seeds),
			Explanation: buildPersonalizedExplanation(seeds, days, len(topTenAll)+len(topTenMovies)+len(topTenSeries) > 0),
		}
	}

	for idx, item := range topTenAll {
		addCandidate(item.Title, maxFloat(0, 4-float64(idx)*0.3), "top-ten")
	}
	for idx, item := range topTenMovies {
		addCandidate(item.Title, maxFloat(0, 6-float64(idx)*0.4), "top-ten-movie")
	}
	for idx, item := range topTenSeries {
		addCandidate(item.Title, maxFloat(0, 6-float64(idx)*0.4), "top-ten-series")
	}

	if len(movieCandidates) < limitPerType*2 {
		if items, err := service.Trending(ctx, "movie"); err == nil {
			diagnostics.TrendingItemCount += len(items)
			for idx, item := range items {
				addCandidate(item.Title, maxFloat(0, 10-float64(idx)*0.08), "trending-movie")
			}
		} else {
			diagnostics.TrendingErrorCount++
			log.Printf("[metadata] personalized recommendations movie trending skipped user=%s: %v", userID, err)
		}
	}
	if len(seriesCandidates) < limitPerType*2 {
		if items, err := service.Trending(ctx, "series"); err == nil {
			diagnostics.TrendingItemCount += len(items)
			for idx, item := range items {
				addCandidate(item.Title, maxFloat(0, 10-float64(idx)*0.08), "trending-series")
			}
		} else {
			diagnostics.TrendingErrorCount++
			log.Printf("[metadata] personalized recommendations series trending skipped user=%s: %v", userID, err)
		}
	}

	movies := finalizePersonalizedBucket(movieCandidates, limitPerType)
	series := finalizePersonalizedBucket(seriesCandidates, limitPerType)
	enrichPersonalizedArtwork(ctx, movies, service, deferArtwork...)
	enrichPersonalizedArtwork(ctx, series, service, deferArtwork...)
	items := interleavePersonalizedItems(movies, series, limitPerType)
	diagnostics.MovieCandidateCount = len(movieCandidates)
	diagnostics.SeriesCandidateCount = len(seriesCandidates)
	logPersonalizedBuildDiagnostics(userID, diagnostics, len(items), ctx.Err(), time.Since(startedAt))
	return PersonalizedRecommendationsResponse{
		Items:       items,
		Movies:      movies,
		Series:      series,
		Total:       len(items),
		SeedCount:   len(seeds),
		Explanation: buildPersonalizedExplanation(seeds, days, len(topTenAll)+len(topTenMovies)+len(topTenSeries) > 0),
	}
}

func summarizePersonalizedSeedEligibility(
	history []models.WatchHistoryItem,
	progress []models.PlaybackProgress,
	cutoff time.Time,
) personalizedBuildDiagnostics {
	diagnostics := personalizedBuildDiagnostics{
		HistoryCount:  len(history),
		ProgressCount: len(progress),
	}
	for _, item := range history {
		if !item.Watched {
			continue
		}
		activity := firstNonZeroTime(item.WatchedAt, item.UpdatedAt)
		if activity.Before(cutoff) {
			continue
		}
		diagnostics.RecentWatchedCount++
		mediaType, _, id, ids := personalizedHistoryIdentity(item)
		if mediaType != "" && extractTMDBID(mediaType, id, ids) <= 0 {
			diagnostics.MissingTMDBCount++
		}
	}
	for _, item := range progress {
		if item.HiddenFromContinueWatching || item.PercentWatched < 20 || item.UpdatedAt.Before(cutoff) {
			continue
		}
		diagnostics.RecentProgressCount++
		mediaType, _, id, ids := personalizedProgressIdentity(item)
		if mediaType != "" && extractTMDBID(mediaType, id, ids) <= 0 {
			diagnostics.MissingTMDBCount++
		}
	}
	return diagnostics
}

func logPersonalizedBuildDiagnostics(
	userID string,
	diagnostics personalizedBuildDiagnostics,
	itemCount int,
	buildErr error,
	duration time.Duration,
) {
	log.Printf(
		"[metadata] personalized recommendations build user=%s history=%d progress=%d recentWatched=%d recentProgress20=%d missingTMDB=%d seeds=%d similarItems=%d similarErrors=%d topTenItems=%d trendingItems=%d trendingErrors=%d movieCandidates=%d seriesCandidates=%d items=%d duration=%s contextErr=%v",
		userID,
		diagnostics.HistoryCount,
		diagnostics.ProgressCount,
		diagnostics.RecentWatchedCount,
		diagnostics.RecentProgressCount,
		diagnostics.MissingTMDBCount,
		diagnostics.SeedCount,
		diagnostics.SimilarItemCount,
		diagnostics.SimilarErrorCount,
		diagnostics.TopTenItemCount,
		diagnostics.TrendingItemCount,
		diagnostics.TrendingErrorCount,
		diagnostics.MovieCandidateCount,
		diagnostics.SeriesCandidateCount,
		itemCount,
		duration.Round(time.Millisecond),
		buildErr,
	)
}

func isPersonalizedRatingAllowed(title models.Title, filter personalizedKidsRatingFilter) bool {
	maxRating := filter.maxTVRating
	if strings.EqualFold(title.MediaType, "movie") {
		maxRating = filter.maxMovieRating
	}
	if strings.TrimSpace(maxRating) == "" {
		return true
	}
	return kids.IsRatingAllowed(title.Certification, maxRating, title.MediaType)
}

func buildPersonalizedSeeds(userID string, history []models.WatchHistoryItem, progress []models.PlaybackProgress, cutoff, now time.Time, limit int, additional ...personalizedSeed) []personalizedSeed {
	byKey := make(map[string]personalizedSeed)
	addSeed := func(seed personalizedSeed) {
		if seed.TMDBID <= 0 || seed.MediaType == "" || (seed.Activity.Before(cutoff) && seed.Source != "watchlist" && seed.Source != "custom_list") {
			return
		}
		seed.Key = seed.MediaType + ":tmdb:" + strconv.FormatInt(seed.TMDBID, 10)
		existing, ok := byKey[seed.Key]
		if !ok {
			byKey[seed.Key] = seed
			return
		}
		if seed.Activity.After(existing.Activity) {
			existing.Activity = seed.Activity
			if seed.Source != "" {
				existing.Source = seed.Source
			}
		}
		if seed.Title != "" {
			existing.Title = seed.Title
		}
		existing.Strength = minFloat(1.7, existing.Strength+seed.Strength*0.35)
		byKey[seed.Key] = existing
	}

	for _, item := range history {
		if !item.Watched {
			continue
		}
		activity := firstNonZeroTime(item.WatchedAt, item.UpdatedAt)
		mediaType, title, id, ids := personalizedHistoryIdentity(item)
		tmdbID := extractTMDBID(mediaType, id, ids)
		addSeed(personalizedSeed{
			MediaType: mediaType,
			TMDBID:    tmdbID,
			Title:     title,
			Activity:  activity,
			Strength:  1.0,
			Source:    "watched",
		})
	}

	for _, item := range progress {
		if item.HiddenFromContinueWatching || item.PercentWatched < 20 {
			continue
		}
		mediaType, title, id, ids := personalizedProgressIdentity(item)
		tmdbID := extractTMDBID(mediaType, id, ids)
		strength := 0.45 + minFloat(item.PercentWatched, 95)/190
		addSeed(personalizedSeed{
			MediaType: mediaType,
			TMDBID:    tmdbID,
			Title:     title,
			Activity:  item.UpdatedAt,
			Strength:  strength,
			Source:    "progress",
		})
	}

	for _, seed := range additional {
		addSeed(seed)
	}

	seeds := make([]personalizedSeed, 0, len(byKey))
	for _, seed := range byKey {
		seeds = append(seeds, seed)
	}
	dayKey := now.Format("2006-01-02")
	sort.SliceStable(seeds, func(i, j int) bool {
		a := personalizedSeedSampleScore(userID, dayKey, seeds[i], cutoff, now)
		b := personalizedSeedSampleScore(userID, dayKey, seeds[j], cutoff, now)
		if a == b {
			return seeds[i].Key < seeds[j].Key
		}
		return a > b
	})
	if len(seeds) > limit {
		seeds = seeds[:limit]
	}
	return seeds
}

func personalizedSeedSampleScore(userID, dayKey string, seed personalizedSeed, cutoff, now time.Time) float64 {
	random := stableUnitFloat(userID + "|" + dayKey + "|" + seed.Key)
	recency := personalizedRecencyScore(seed.Activity, cutoff, now)
	return random*0.58 + recency*0.28 + minFloat(seed.Strength, 1.7)/1.7*0.14
}

func buildPersonalizedExplanation(seeds []personalizedSeed, days int, popularityBoost bool) *PersonalizedRecommendationExplanation {
	if len(seeds) == 0 {
		return nil
	}
	displayCount := minInt(3, len(seeds))
	names := make([]string, 0, displayCount)
	for _, seed := range seeds {
		if strings.TrimSpace(seed.Title) == "" {
			continue
		}
		names = append(names, seed.Title)
		if len(names) >= displayCount {
			break
		}
	}
	summary := "Based on your recent watch activity"
	if len(names) > 0 {
		summary = "Because you watched " + joinPersonalizedNames(names)
		for _, seed := range seeds {
			if seed.Source == "watchlist" || seed.Source == "custom_list" || seed.Source == "search" {
				summary = "Based on your interests in " + joinPersonalizedNames(names)
				break
			}
		}
	}

	details := make([]PersonalizedRecommendationSeed, 0, minInt(len(seeds), 12))
	for _, seed := range seeds {
		title := strings.TrimSpace(seed.Title)
		if title == "" {
			continue
		}
		source := seed.Source
		if source == "" {
			source = "watched"
		}
		activity := ""
		if !seed.Activity.IsZero() {
			activity = seed.Activity.UTC().Format(time.RFC3339)
		}
		details = append(details, PersonalizedRecommendationSeed{
			Title:     title,
			MediaType: seed.MediaType,
			TMDBID:    seed.TMDBID,
			Source:    source,
			Activity:  activity,
		})
		if len(details) >= 12 {
			break
		}
	}

	return &PersonalizedRecommendationExplanation{
		Summary:         summary,
		Days:            days,
		SeedCount:       len(seeds),
		Seeds:           details,
		PopularityBoost: popularityBoost,
		ExcludesKnown:   true,
	}
}

func joinPersonalizedNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
	}
}

func buildPersonalizedExclusion(history []models.WatchHistoryItem, progress []models.PlaybackProgress) map[string]struct{} {
	excluded := make(map[string]struct{})
	add := func(mediaType, id, title string, year int, ids map[string]string) {
		for key := range identityExclusionKeys(mediaType, id, title, year, ids) {
			excluded[key] = struct{}{}
		}
	}
	for _, item := range history {
		mediaType, title, id, ids := personalizedHistoryIdentity(item)
		add(mediaType, id, title, item.Year, ids)
	}
	for _, item := range progress {
		mediaType, title, id, ids := personalizedProgressIdentity(item)
		add(mediaType, id, title, item.Year, ids)
	}
	return excluded
}

func personalizedHistoryIdentity(item models.WatchHistoryItem) (string, string, string, map[string]string) {
	mediaType := normalizePersonalizedMediaType(item.MediaType)
	title := item.Name
	id := item.ItemID
	if item.MediaType == "episode" {
		mediaType = "series"
		if item.SeriesName != "" {
			title = item.SeriesName
		}
		if item.SeriesID != "" {
			id = item.SeriesID
		}
	}
	return mediaType, title, id, item.ExternalIDs
}

func personalizedProgressIdentity(item models.PlaybackProgress) (string, string, string, map[string]string) {
	mediaType := normalizePersonalizedMediaType(item.MediaType)
	title := item.MovieName
	id := item.ItemID
	if item.MediaType == "episode" {
		mediaType = "series"
		title = item.SeriesName
		if item.SeriesID != "" {
			id = item.SeriesID
		}
	}
	return mediaType, title, id, item.ExternalIDs
}

func identityExclusionKeys(mediaType, id, title string, year int, ids map[string]string) map[string]struct{} {
	mediaType = normalizePersonalizedMediaType(mediaType)
	keys := make(map[string]struct{})
	if mediaType == "" {
		return keys
	}
	if normalizedID := normalizePersonalizedID(id); normalizedID != "" {
		keys[mediaType+":id:"+normalizedID] = struct{}{}
	}
	if tmdbID := extractTMDBID(mediaType, id, ids); tmdbID > 0 {
		keys[mediaType+":tmdb:"+strconv.FormatInt(tmdbID, 10)] = struct{}{}
	}
	if imdb := normalizeExternalID(ids["imdb"]); imdb != "" {
		keys[mediaType+":imdb:"+imdb] = struct{}{}
	}
	if tvdb := normalizeExternalID(ids["tvdb"]); tvdb != "" {
		keys[mediaType+":tvdb:"+tvdb] = struct{}{}
	}
	if normalizedTitle := normalizePersonalizedTitle(title); normalizedTitle != "" {
		keys[mediaType+":title:"+normalizedTitle] = struct{}{}
		if year > 0 {
			keys[mediaType+":title-year:"+normalizedTitle+":"+strconv.Itoa(year)] = struct{}{}
		}
	}
	return keys
}

func titleExclusionKeys(title models.Title) map[string]struct{} {
	mediaType := normalizePersonalizedMediaType(title.MediaType)
	ids := map[string]string{}
	if title.TMDBID > 0 {
		ids["tmdb"] = strconv.FormatInt(title.TMDBID, 10)
	}
	if title.IMDBID != "" {
		ids["imdb"] = title.IMDBID
	}
	if title.TVDBID > 0 {
		ids["tvdb"] = strconv.FormatInt(title.TVDBID, 10)
	}
	return identityExclusionKeys(mediaType, title.ID, title.Name, title.Year, ids)
}

func personalizedCanonicalTitleKey(title models.Title) string {
	mediaType := normalizePersonalizedMediaType(title.MediaType)
	if mediaType == "" {
		return ""
	}
	if title.TMDBID > 0 {
		return mediaType + ":tmdb:" + strconv.FormatInt(title.TMDBID, 10)
	}
	if title.IMDBID != "" {
		return mediaType + ":imdb:" + normalizeExternalID(title.IMDBID)
	}
	if title.TVDBID > 0 {
		return mediaType + ":tvdb:" + strconv.FormatInt(title.TVDBID, 10)
	}
	if title.ID != "" {
		return mediaType + ":id:" + normalizePersonalizedID(title.ID)
	}
	if name := normalizePersonalizedTitle(title.Name); name != "" {
		return mediaType + ":title:" + name
	}
	return ""
}

func extractTMDBID(mediaType, id string, ids map[string]string) int64 {
	if ids != nil {
		if tmdb := parseProviderNumericID(ids["tmdb"]); tmdb > 0 {
			return tmdb
		}
	}
	lowerID := strings.ToLower(strings.TrimSpace(id))
	if !strings.Contains(lowerID, "tmdb") {
		return 0
	}
	parts := strings.Split(lowerID, ":")
	for i := len(parts) - 1; i >= 0; i-- {
		if parsed := parseProviderNumericID(parts[i]); parsed > 0 {
			return parsed
		}
	}
	return 0
}

func parseProviderNumericID(value string) int64 {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return 0
	}
	if strings.Contains(value, ":") {
		parts := strings.Split(value, ":")
		for i := len(parts) - 1; i >= 0; i-- {
			if parsed := parseProviderNumericID(parts[i]); parsed > 0 {
				return parsed
			}
		}
		return 0
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}

func normalizePersonalizedMediaType(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "movie", "movies":
		return "movie"
	case "series", "tv", "show", "shows", "episode":
		return "series"
	default:
		return ""
	}
}

func normalizePersonalizedID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeExternalID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizePersonalizedTitle(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastSpace := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

func personalizedRecencyScore(activity, cutoff, now time.Time) float64 {
	if activity.IsZero() || !activity.After(cutoff) || !now.After(cutoff) {
		return 0
	}
	elapsed := now.Sub(activity).Seconds()
	window := now.Sub(cutoff).Seconds()
	if window <= 0 {
		return 0
	}
	score := 1 - elapsed/window
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

func personalizedPopularityWeight(title models.Title) float64 {
	popularity := title.Popularity
	if popularity <= 0 {
		return 0.65
	}
	switch {
	case popularity < 1:
		return 0.35
	case popularity < 3:
		return 0.55
	case popularity < 6:
		return 0.75
	case popularity < 12:
		return 0.9
	case popularity < 30:
		return 1.0
	default:
		return 1.08
	}
}

func personalizedCandidateQualityScore(title models.Title) float64 {
	score := 0.0
	if title.Popularity > 0 {
		score += minFloat(title.Popularity/6, 14)
		if title.Popularity < 1 {
			score -= 8
		} else if title.Popularity < 3 {
			score -= 4
		}
	}
	if normalizePersonalizedMediaType(title.MediaType) == "movie" && title.Year > 0 && title.Year < 1970 && title.Popularity < 3 {
		score -= 18
	}
	return score
}

func finalizePersonalizedBucket(candidates map[string]*personalizedCandidate, limit int) []models.TrendingItem {
	list := make([]*personalizedCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		candidate.Score += float64(maxInt(0, candidate.SourceCount-1)) * 10
		list = append(list, candidate)
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Score == list[j].Score {
			return list[i].Title.Name < list[j].Title.Name
		}
		return list[i].Score > list[j].Score
	})

	selected := make([]*personalizedCandidate, 0, limit)
	deferred := make([]*personalizedCandidate, 0)
	seedCounts := make(map[string]int)
	for _, candidate := range list {
		if len(selected) >= limit {
			break
		}
		if candidate.PrimarySeed != "" &&
			(strings.HasPrefix(candidate.PrimarySeed, "movie:tmdb:") || strings.HasPrefix(candidate.PrimarySeed, "series:tmdb:")) {
			if seedCounts[candidate.PrimarySeed] >= personalizedMaxPerPrimarySeed {
				deferred = append(deferred, candidate)
				continue
			}
			seedCounts[candidate.PrimarySeed]++
		}
		selected = append(selected, candidate)
	}
	for _, candidate := range deferred {
		if len(selected) >= limit {
			break
		}
		selected = append(selected, candidate)
	}

	items := make([]models.TrendingItem, 0, len(selected))
	for i, candidate := range selected {
		items = append(items, models.TrendingItem{Rank: i + 1, Title: candidate.Title})
	}
	return items
}

func enrichPersonalizedArtwork(ctx context.Context, items []models.TrendingItem, meta metadataService, deferArtwork ...bool) {
	if meta == nil {
		return
	}

	missingArtwork := make([]int, 0, len(items))
	for i := range items {
		title := &items[i].Title
		if title.TMDBID <= 0 && title.TVDBID <= 0 {
			continue
		}

		textPosterURL, textBackdropURL, backdropURLs := meta.GetCachedArtworkURLs(title.MediaType, title.TMDBID, title.TVDBID)
		if textPosterURL != "" {
			title.TextPoster = &models.Image{URL: textPosterURL, Type: "poster"}
		}
		if textBackdropURL != "" {
			title.TextBackdrop = &models.Image{URL: textBackdropURL, Type: "backdrop"}
		}
		if len(backdropURLs) > 0 {
			backdrops := make([]models.Image, 0, len(backdropURLs))
			for _, url := range backdropURLs {
				if strings.TrimSpace(url) == "" {
					continue
				}
				backdrops = append(backdrops, models.Image{URL: url, Type: "backdrop"})
			}
			if len(backdrops) > 0 {
				title.Backdrops = backdrops
			}
		}
		if personalizedArtworkURLCount(*title) < 2 {
			missingArtwork = append(missingArtwork, i)
		}
	}

	if len(deferArtwork) > 0 && deferArtwork[0] {
		fillMissingTitleLogos(ctx, items, meta, true)
		return
	}

	// Personalized candidates often come from Similar() and have not appeared
	// in another shelf yet, so a cache-only lookup leaves most of them with one
	// backdrop. Fetch only the missing artwork, with the same bounded fan-out
	// used by shelf enrichment, and retain it in the metadata cache.
	const maxConcurrent = 5
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	for _, index := range missingArtwork {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			meta.ApplyLocalizedArtwork(ctx, &items[index].Title)
		}()
	}
	wg.Wait()
	fillMissingTitleLogos(ctx, items, meta, false)
}

func personalizedArtworkURLCount(title models.Title) int {
	seen := make(map[string]struct{}, len(title.Backdrops)+2)
	add := func(image *models.Image) {
		if image == nil {
			return
		}
		url := strings.TrimSpace(image.URL)
		if url != "" {
			seen[url] = struct{}{}
		}
	}
	add(title.Backdrop)
	add(title.TextBackdrop)
	for i := range title.Backdrops {
		add(&title.Backdrops[i])
	}
	return len(seen)
}

func interleavePersonalizedItems(movies, series []models.TrendingItem, limitPerType int) []models.TrendingItem {
	totalLimit := limitPerType * 2
	items := make([]models.TrendingItem, 0, minInt(totalLimit, len(movies)+len(series)))
	maxLen := maxInt(len(movies), len(series))
	for i := 0; i < maxLen && len(items) < totalLimit; i++ {
		if i < len(movies) {
			items = append(items, movies[i])
		}
		if i < len(series) && len(items) < totalLimit {
			items = append(items, series[i])
		}
	}
	for i := range items {
		items[i].Rank = i + 1
	}
	return items
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}

func intersectsStringSet(a, b map[string]struct{}) bool {
	for key := range a {
		if _, ok := b[key]; ok {
			return true
		}
	}
	return false
}

func stableUnitFloat(value string) float64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(value))
	return float64(h.Sum64()%1_000_000) / 1_000_000
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
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
