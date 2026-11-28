package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"novastream/config"
	"novastream/internal/mediaidentity"
	"novastream/models"
	"novastream/services/mdblist"
	"strconv"
	"strings"
	"time"
)

// executeMDBListHistorySync syncs watch history between MDBList and local.
func (s *Service) executeMDBListHistorySync(task config.ScheduledTask) (SyncResult, error) {
	s.mu.RLock()
	historySvc := s.historyService
	s.mu.RUnlock()

	if historySvc == nil {
		return SyncResult{}, errors.New("history service not configured")
	}

	mdblistAccountID := task.Config["mdblistAccountId"]
	profileID, err := s.resolveTaskProfileID(task)

	if mdblistAccountID == "" || profileID == "" {
		return SyncResult{}, errors.New("missing mdblistAccountId or profileId in task config")
	}
	if err != nil {
		return SyncResult{}, err
	}

	syncDirection := task.Config["syncDirection"]
	if syncDirection == "" {
		syncDirection = "mdblist_to_local"
	}
	dryRun := task.Config["dryRun"] == "true"

	// Load settings to get MDBList account
	settings, err := s.configManager.Load()
	if err != nil {
		return SyncResult{}, fmt.Errorf("load settings: %w", err)
	}

	mdblistAccount := settings.MDBList.GetAccountByID(mdblistAccountID)
	if mdblistAccount == nil {
		return SyncResult{}, errors.New("MDBList account not found")
	}

	if mdblistAccount.APIKey == "" {
		return SyncResult{}, errors.New("MDBList account has no API key")
	}
	log.Printf("[scheduler] MDBList history configuration task=%q account=%q accountName=%q profile=%q configuredProfile=%q direction=%q dryRun=%v fullSync=%v fullExport=%v",
		task.ID, mdblistAccount.ID, mdblistAccount.Name, profileID, task.Config["profileId"], syncDirection, dryRun,
		task.Config["fullSync"] == "true", task.Config["fullExport"] == "true")

	switch syncDirection {
	case "mdblist_to_local":
		return s.syncMDBListHistoryToLocal(task, mdblistAccount, profileID, dryRun)
	case "local_to_mdblist":
		return s.syncLocalHistoryToMDBList(task, mdblistAccount, profileID, dryRun)
	case "bidirectional":
		// Import first, then export
		importResult, err := s.syncMDBListHistoryToLocal(task, mdblistAccount, profileID, dryRun)
		if err != nil {
			return importResult, err
		}
		exportResult, err := s.syncLocalHistoryToMDBList(task, mdblistAccount, profileID, dryRun)
		if err != nil {
			return combineHistorySyncResults(importResult, exportResult), err
		}
		return combineHistorySyncResults(importResult, exportResult), nil
	default:
		return SyncResult{}, fmt.Errorf("unknown sync direction: %s", syncDirection)
	}
}

// syncMDBListHistoryToLocal imports watch history from MDBList into local.
func (s *Service) syncMDBListHistoryToLocal(task config.ScheduledTask, account *config.MDBListAccount, profileID string, dryRun bool) (SyncResult, error) {
	result := SyncResult{DryRun: dryRun}

	// Determine incremental cursor
	var since string
	if historySyncLastRun(task) != nil {
		since = historySyncLastRun(task).Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	}
	mode := "full"
	if since != "" {
		mode = "incremental"
	}
	lastRun := ""
	if task.LastRunAt != nil {
		lastRun = task.LastRunAt.UTC().Format(time.RFC3339)
	}
	log.Printf("[scheduler] MDBList history import start task=%q account=%q profile=%q mode=%s since=%q previousRun=%q previousStatus=%q dryRun=%v",
		task.ID, account.ID, profileID, mode, since, lastRun, task.LastStatus, dryRun)

	// Keep both pagination and retries bounded, and stop promptly on shutdown.
	parent := s.taskContext()
	ctx, cancel := context.WithTimeout(parent, 30*time.Minute)
	defer cancel()
	metadataCache := make(map[string]*models.SeriesDetails)
	httpClient := &http.Client{Timeout: 30 * time.Second}
	offset, limit, pages := 0, 500, 0
	totalMovies, totalEpisodes := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("MDBList history import stopped after %d imported items: %w", result.Count, err)
		}
		values := url.Values{"apikey": {account.APIKey}, "limit": {strconv.Itoa(limit)}, "offset": {strconv.Itoa(offset)}}
		if since != "" {
			values.Set("since", since)
		}
		resp, err := fetchMDBListHistoryPage(ctx, httpClient, "https://api.mdblist.com/sync/watched?"+values.Encode(), task.ID, offset, waitMDBListRetry)
		if err != nil {
			if dryRun {
				return result, err
			}
			return result, fmt.Errorf("%w; %d items imported from completed pages", err, result.Count)
		}
		var page *struct {
			Movies     []json.RawMessage `json:"movies"`
			Episodes   []json.RawMessage `json:"episodes"`
			Shows      []json.RawMessage `json:"shows"`
			Seasons    []json.RawMessage `json:"seasons"`
			Pagination struct {
				HasMore       bool `json:"has_more"`
				TotalMovies   *int `json:"total_movies"`
				TotalEpisodes *int `json:"total_episodes"`
				Limit         int  `json:"limit"`
			} `json:"pagination"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if decodeErr != nil {
			return result, fmt.Errorf("decode MDBList history: %w", decodeErr)
		}
		if page == nil {
			return result, errors.New("MDBList returned null instead of history")
		}
		pages++
		log.Printf("[scheduler] MDBList history page task=%q page=%d offset=%d requestedLimit=%d responseLimit=%d movies=%d episodes=%d shows=%d seasons=%d hasMore=%v apiTotalMovies=%s apiTotalEpisodes=%s",
			task.ID, pages, offset, limit, page.Pagination.Limit, len(page.Movies), len(page.Episodes), len(page.Shows), len(page.Seasons),
			page.Pagination.HasMore, mdblistReportedTotal(page.Pagination.TotalMovies), mdblistReportedTotal(page.Pagination.TotalEpisodes))
		pageResult, importErr := s.importMDBListHistoryPage(ctx, task, profileID, dryRun, page.Movies, page.Episodes, metadataCache)
		result = combineHistorySyncResults(result, pageResult)
		if importErr != nil {
			return result, importErr
		}
		totalMovies += len(page.Movies)
		totalEpisodes += len(page.Episodes)
		log.Printf("[scheduler] MDBList history progress task=%q pages=%d movies=%d episodes=%d imported=%d dryRun=%v", task.ID, pages, totalMovies, totalEpisodes, result.Count, dryRun)
		if !page.Pagination.HasMore {
			break
		}
		// MDBList paginates all four buckets together; use the server's limit,
		// not just the number of movie/episode entries we import.
		step := page.Pagination.Limit
		if step <= 0 {
			step = limit
		}
		if len(page.Movies)+len(page.Episodes)+len(page.Shows)+len(page.Seasons) == 0 {
			return result, fmt.Errorf("MDBList history returned an empty page with has_more=true (offset %d)", offset)
		}
		offset += step
	}
	log.Printf("[scheduler] Fetched %d movies + %d episodes from MDBList watch history (task=%q profile=%q mode=%s pages=%d)", totalMovies, totalEpisodes, task.ID, profileID, mode, pages)
	if totalMovies == 0 && since != "" {
		log.Printf("[scheduler] MDBList history import task=%q: zero movies returned with since=%q; Full sync requests older history without this filter", task.ID, since)
	}
	return result, nil
}

// importMDBListHistoryPage persists each page before requesting the next one.
func (s *Service) importMDBListHistoryPage(ctx context.Context, task config.ScheduledTask, profileID string, dryRun bool, allMovies, allEpisodes []json.RawMessage, metadataCache map[string]*models.SeriesDetails) (SyncResult, error) {
	result := SyncResult{DryRun: dryRun}
	s.mu.RLock()
	historySvc := s.historyService
	s.mu.RUnlock()
	// Convert to WatchHistoryUpdate items
	watched := true
	var updates []models.WatchHistoryUpdate
	parsedMovies, parsedEpisodes := 0, 0
	skippedMalformed, skippedUnwatched, skippedIdentity, skippedCoordinates := 0, 0, 0, 0
	invalidTimestamps := 0

	// Parse movies
	for _, raw := range allMovies {
		var m struct {
			LastWatchedAt string `json:"last_watched_at"`
			Movie         struct {
				Title string `json:"title"`
				Year  int    `json:"year"`
				IDs   struct {
					IMDB string `json:"imdb"`
					TMDB int    `json:"tmdb"`
					TVDB int    `json:"tvdb"`
				} `json:"ids"`
			} `json:"movie"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			skippedMalformed++
			continue
		}

		if m.LastWatchedAt == "" {
			skippedUnwatched++
			continue
		}
		watchedAt, parseErr := time.Parse(time.RFC3339, m.LastWatchedAt)
		if parseErr != nil {
			invalidTimestamps++
		}
		extIDs := make(map[string]string)
		var itemID string
		if m.Movie.IDs.IMDB != "" {
			extIDs["imdb"] = m.Movie.IDs.IMDB
			itemID = m.Movie.IDs.IMDB
		}
		if m.Movie.IDs.TMDB != 0 {
			extIDs["tmdb"] = strconv.Itoa(m.Movie.IDs.TMDB)
			if itemID == "" {
				itemID = strconv.Itoa(m.Movie.IDs.TMDB)
			}
		}
		if itemID == "" {
			skippedIdentity++
			continue
		}
		parsedMovies++

		if dryRun {
			result.ToAdd = append(result.ToAdd, config.DryRunItem{
				Name:      m.Movie.Title,
				MediaType: "movie",
				ID:        itemID,
			})
			continue
		}

		updates = append(updates, models.WatchHistoryUpdate{
			MediaType:   "movie",
			ItemID:      itemID,
			Name:        m.Movie.Title,
			Year:        m.Movie.Year,
			Watched:     &watched,
			WatchedAt:   watchedAt,
			ExternalIDs: extIDs,
		})
	}

	// Parse episodes
	for _, raw := range allEpisodes {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		var e struct {
			LastWatchedAt string `json:"last_watched_at"`
			Episode       struct {
				Season int    `json:"season"`
				Number int    `json:"number"`
				Name   string `json:"name"`
				Show   struct {
					Title string `json:"title"`
					Year  int    `json:"year"`
					IDs   struct {
						IMDB string `json:"imdb"`
						TMDB int    `json:"tmdb"`
						TVDB int    `json:"tvdb"`
					} `json:"ids"`
				} `json:"show"`
			} `json:"episode"`
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			skippedMalformed++
			continue
		}

		if e.LastWatchedAt == "" {
			skippedUnwatched++
			continue
		}
		if e.Episode.Season < 0 || e.Episode.Number <= 0 {
			skippedCoordinates++
			continue
		}
		watchedAt, parseErr := time.Parse(time.RFC3339, e.LastWatchedAt)
		if parseErr != nil {
			invalidTimestamps++
		}
		extIDs := make(map[string]string)
		var seriesID string

		show := e.Episode.Show
		if show.IDs.TVDB != 0 {
			extIDs["tvdb"] = strconv.Itoa(show.IDs.TVDB)
			seriesID = fmt.Sprintf("tvdb:series:%d", show.IDs.TVDB)
		}
		if show.IDs.TMDB != 0 {
			extIDs["tmdb"] = strconv.Itoa(show.IDs.TMDB)
			if seriesID == "" {
				seriesID = fmt.Sprintf("tmdb:tv:%d", show.IDs.TMDB)
			}
		}
		if show.IDs.IMDB != "" {
			extIDs["imdb"] = show.IDs.IMDB
		}
		if seriesID == "" {
			skippedIdentity++
			continue
		}
		parsedEpisodes++

		absoluteEpisode := 0
		if e.Episode.Number >= 1000 {
			absoluteEpisode = e.Episode.Number
		}
		localSeason, localEpisode, localAbsolute, episodeTitle := s.canonicalizeProviderEpisodeCached(ctx, metadataCache,
			"mdblist", extIDs, nil, e.Episode.Season, e.Episode.Number, absoluteEpisode, e.Episode.Name,
		)
		if localAbsolute > 0 {
			extIDs["absoluteEpisode"] = strconv.Itoa(localAbsolute)
		}
		itemID := fmt.Sprintf("%s:s%02de%02d", seriesID, localSeason, localEpisode)

		if dryRun {
			result.ToAdd = append(result.ToAdd, config.DryRunItem{
				Name:      fmt.Sprintf("%s S%02dE%02d", show.Title, localSeason, localEpisode),
				MediaType: "episode",
				ID:        itemID,
			})
			continue
		}

		updates = append(updates, models.WatchHistoryUpdate{
			MediaType:     "episode",
			ItemID:        itemID,
			Name:          episodeTitle,
			Watched:       &watched,
			WatchedAt:     watchedAt,
			ExternalIDs:   extIDs,
			SeasonNumber:  localSeason,
			EpisodeNumber: localEpisode,
			SeriesID:      seriesID,
			SeriesName:    show.Title,
		})
	}

	log.Printf("[scheduler] MDBList history parsed task=%q profile=%q movies=%d episodes=%d skippedMalformed=%d skippedUnwatched=%d skippedIdentity=%d skippedCoordinates=%d invalidTimestamps=%d dryRun=%v",
		task.ID, profileID, parsedMovies, parsedEpisodes, skippedMalformed, skippedUnwatched, skippedIdentity, skippedCoordinates, invalidTimestamps, dryRun)
	if dryRun {
		result.Count = len(result.ToAdd)
		return result, nil
	}

	if len(updates) > 0 {
		imported, err := historySvc.ImportWatchHistory(profileID, updates)
		if err != nil {
			return result, fmt.Errorf("import watch history: %w", err)
		}
		result.Count = imported
	}
	log.Printf("[scheduler] Imported %d/%d items from MDBList history (task=%q profile=%q movieCandidates=%d episodeCandidates=%d)",
		result.Count, len(updates), task.ID, profileID, parsedMovies, parsedEpisodes)

	return result, nil
}

// syncLocalHistoryToMDBList exports local watch history to MDBList.
func (s *Service) syncLocalHistoryToMDBList(task config.ScheduledTask, account *config.MDBListAccount, profileID string, dryRun bool) (SyncResult, error) {
	result := SyncResult{DryRun: dryRun}
	startedAt := time.Now().UTC()

	s.mu.RLock()
	historySvc := s.historyService
	s.mu.RUnlock()

	// Get all local watch history for this profile
	items, err := historySvc.ListWatchHistory(profileID)
	if err != nil {
		return result, fmt.Errorf("list local history: %w", err)
	}
	items = s.enrichAndCollapseHistoryItems(items)

	// Incremental export uses LastRunAt - 5min. That misses older watches that
	// never landed on MDBList (sparse-ID scrobble failures). Periodically do a
	// full export; MDBList /sync/watched is idempotent so re-export is safe.
	// Force with task config fullExport=true for one-shot healing.
	const fullExportInterval = 6 * time.Hour
	exportKey := task.ID + ":mdblist_export"
	forceFull := task.Config["fullExport"] == "true" || task.Config["fullSync"] == "true"

	lastFull, ok := s.lastFullHistorySync(task, exportKey, lastFullHistoryExportConfigKey)

	isFullExport := forceFull || !ok || time.Since(lastFull) >= fullExportInterval
	var since time.Time
	if !isFullExport && historySyncLastRun(task) != nil {
		since = historySyncLastRun(task).Add(-5 * time.Minute)
	}
	var removalSince time.Time
	if historySyncLastRun(task) != nil {
		removalSince = historySyncLastRun(task).Add(-5 * time.Minute)
	}

	// Bidirectional full exports list the whole MDBList watched history first so
	// plays removed on MDBList since the last snapshot are not re-sent. The
	// incremental import (since=) cannot prove that a play is gone.
	removals, err := s.newHistoryRemovalTracker(task, "mdblist", account.ID, profileID, startedAt)
	if err != nil {
		return result, err
	}
	var remoteKeys map[string]bool
	if removals != nil && since.IsZero() {
		remoteKeys, err = s.fetchMDBListRemoteHistoryKeys(task, account)
		if err != nil {
			return result, err
		}
		removals.setCompleteRemote(remoteKeys)
	}

	var toSync, toRemove, remoteRemoved []models.WatchHistoryItem
	for _, item := range items {
		if item.Watched {
			if since.IsZero() || item.WatchedAt.After(since) {
				if removals.removedRemotely(item, mdblistLocalHistoryKeys(item)) {
					remoteRemoved = append(remoteRemoved, item)
					continue
				}
				toSync = append(toSync, item)
			}
		} else if task.Config["fullSync"] == "true" || (!removalSince.IsZero() && item.UpdatedAt.After(removalSince)) {
			toRemove = append(toRemove, item)
		}
	}

	log.Printf("[scheduler] Found %d watched items to sync, %d recent unwatches to remove from MDBList and %d plays removed on MDBList (since %v fullExport=%v force=%v)",
		len(toSync), len(toRemove), len(remoteRemoved), since, isFullExport, forceFull)

	// Mirror MDBList-side removals and record the listing once the export succeeds.
	finish := func() error {
		mirrored, err := s.mirrorRemoteHistoryRemovals(profileID, "MDBList", remoteRemoved)
		result.Count += mirrored
		if err != nil {
			return err
		}
		if remoteKeys != nil {
			if err := s.saveHistoryRemovalSnapshot(removals, remoteKeys, true); err != nil {
				return err
			}
		}
		if isFullExport {
			s.markFullHistorySync(&result, exportKey, lastFullHistoryExportConfigKey)
		}
		return nil
	}

	if dryRun {
		for _, item := range toSync {
			result.ToAdd = append(result.ToAdd, config.DryRunItem{
				Name:      item.Name,
				MediaType: item.MediaType,
				ID:        item.ItemID,
			})
		}
		for _, item := range toRemove {
			result.ToRemove = append(result.ToRemove, config.DryRunItem{Name: item.Name, MediaType: item.MediaType, ID: item.ItemID})
		}
		for _, item := range remoteRemoved {
			result.ToRemove = append(result.ToRemove, historyRemovalDryRunItem(item))
		}
		result.Count = len(result.ToAdd) + len(result.ToRemove)
		return result, nil
	}

	if len(toSync) == 0 && len(toRemove) == 0 {
		return result, finish()
	}

	// Build MDBList sync requests batched by type.
	// MDBList format: movies=[{"ids":{...}, "watched_at":"..."}],
	//                 shows=[{"ids":{...}, "seasons":[{"number":N, "episodes":[{"number":N, "watched_at":"..."}]}]}]
	apiKey := account.APIKey

	// Collect movies
	skippedNoIDs := 0
	var moviePayloads []map[string]interface{}
	for _, item := range toSync {
		if item.MediaType != "movie" {
			continue
		}
		ids := extractMDBListIDs(item.ExternalIDs)
		if ids.imdb == "" && ids.tmdb == 0 {
			skippedNoIDs++
			continue
		}
		m := map[string]interface{}{
			"ids": formatMDBListIDsMap(ids),
		}
		if !item.WatchedAt.IsZero() {
			m["watched_at"] = item.WatchedAt.UTC().Format(time.RFC3339)
		}
		moviePayloads = append(moviePayloads, m)
	}

	// Collect episodes grouped by show
	type showKey struct {
		imdb string
		tmdb int
	}
	type epEntry struct {
		season    int
		episode   int
		watchedAt time.Time
		absolute  int // distinct absoluteEpisode for hybrid retry; 0 if N/A
	}
	showMap := make(map[showKey][]epEntry)
	showOrder := make([]showKey, 0)
	for _, item := range toSync {
		if item.MediaType != "episode" {
			continue
		}
		// Enrich sparse rows that only store titleId (e.g. tmdb:tv:82782)
		// so bidirectional history export can heal MDBList after missed scrobbles.
		ids := extractMDBListIDs(mediaidentity.EnrichShowExternalIDs(item.SeriesID, item.ItemID, item.ExternalIDs))
		if ids.imdb == "" && ids.tmdb == 0 {
			skippedNoIDs++
			continue
		}
		key := showKey{imdb: ids.imdb, tmdb: ids.tmdb}
		if _, exists := showMap[key]; !exists {
			showOrder = append(showOrder, key)
		}
		// Pure seasonal first. Hybrid absolute retry happens after not_found
		// responses (absoluteEpisode is common on non-anime shows too).
		showMap[key] = append(showMap[key], epEntry{
			season:    item.SeasonNumber,
			episode:   item.EpisodeNumber,
			watchedAt: item.WatchedAt,
			absolute:  mdblist.HybridEpisodeNumber(item.EpisodeNumber, item.ExternalIDs),
		})
	}

	var showPayloads []map[string]interface{}
	for _, key := range showOrder {
		eps := showMap[key]
		ids := mdblistIDs{imdb: key.imdb, tmdb: key.tmdb}

		// Group episodes by season
		seasonMap := make(map[int][]map[string]interface{})
		for _, ep := range eps {
			epObj := map[string]interface{}{
				"number": ep.episode,
			}
			if !ep.watchedAt.IsZero() {
				epObj["watched_at"] = ep.watchedAt.UTC().Format(time.RFC3339)
			}
			seasonMap[ep.season] = append(seasonMap[ep.season], epObj)
		}

		var seasons []map[string]interface{}
		for sNum, sEps := range seasonMap {
			seasons = append(seasons, map[string]interface{}{
				"number":   sNum,
				"episodes": sEps,
			})
		}

		showPayloads = append(showPayloads, map[string]interface{}{
			"ids":     formatMDBListIDsMap(ids),
			"seasons": seasons,
		})
	}

	// Send batched request
	syncCount := 0
	batchSize := 100

	// Sync movies in batches
	for i := 0; i < len(moviePayloads); i += batchSize {
		end := i + batchSize
		if end > len(moviePayloads) {
			end = len(moviePayloads)
		}
		batch := map[string]interface{}{
			"movies": moviePayloads[i:end],
		}
		body, _ := json.Marshal(batch)
		respBody, err := postToMDBListBody(apiKey, "/sync/watched", string(body))
		if err != nil {
			result.Count = syncCount
			return result, fmt.Errorf("MDBList sync movies batch: %w", err)
		}
		var response struct {
			NotFound struct {
				Movies []json.RawMessage `json:"movies"`
			} `json:"not_found"`
		}
		if len(respBody) > 0 {
			if err := json.Unmarshal(respBody, &response); err != nil {
				result.Count = syncCount
				return result, fmt.Errorf("decode MDBList movie sync response: %w", err)
			}
		}
		syncCount += end - i - len(response.NotFound.Movies)
	}

	// Sync shows in batches (seasonal numbering). Collect not_found for hybrid retry.
	type hybridRetry struct {
		key       showKey
		season    int
		absolute  int
		watchedAt time.Time
	}
	var hybridRetries []hybridRetry

	for i := 0; i < len(showPayloads); i += batchSize {
		end := i + batchSize
		if end > len(showPayloads) {
			end = len(showPayloads)
		}
		batch := map[string]interface{}{
			"shows": showPayloads[i:end],
		}
		body, _ := json.Marshal(batch)
		respBody, err := postToMDBListBody(apiKey, "/sync/watched", string(body))
		if err != nil {
			result.Count = syncCount
			return result, fmt.Errorf("MDBList sync shows batch: %w", err)
		}
		notFound := parseMDBListNotFoundEpisodes(respBody)
		accepted := 0
		for _, show := range showPayloads[i:end] {
			for _, season := range show["seasons"].([]map[string]interface{}) {
				accepted += len(season["episodes"].([]map[string]interface{}))
			}
		}
		accepted -= len(notFound)
		if accepted > 0 {
			syncCount += accepted
		}
		// Map not_found seasonal keys back to absolute candidates (scoped by show ids).
		for _, key := range showOrder {
			for _, ep := range showMap[key] {
				if ep.absolute <= 0 {
					continue
				}
				nfKey := mdblistNotFoundKey(key.imdb, key.tmdb, ep.season, ep.episode)
				if !notFound[nfKey] {
					continue
				}
				hybridRetries = append(hybridRetries, hybridRetry{
					key:       key,
					season:    ep.season,
					absolute:  ep.absolute,
					watchedAt: ep.watchedAt,
				})
			}
		}
	}

	if len(hybridRetries) > 0 {
		log.Printf("[scheduler] MDBList hybrid absolute retry for %d not_found episodes", len(hybridRetries))
		// Group hybrid retries by show
		hybridByShow := make(map[showKey][]hybridRetry)
		var hybridOrder []showKey
		for _, hr := range hybridRetries {
			if _, ok := hybridByShow[hr.key]; !ok {
				hybridOrder = append(hybridOrder, hr.key)
			}
			hybridByShow[hr.key] = append(hybridByShow[hr.key], hr)
		}
		var hybridPayloads []map[string]interface{}
		for _, key := range hybridOrder {
			seasonMap := make(map[int][]map[string]interface{})
			for _, hr := range hybridByShow[key] {
				epObj := map[string]interface{}{"number": hr.absolute}
				if !hr.watchedAt.IsZero() {
					epObj["watched_at"] = hr.watchedAt.UTC().Format(time.RFC3339)
				}
				seasonMap[hr.season] = append(seasonMap[hr.season], epObj)
			}
			var seasons []map[string]interface{}
			for sNum, sEps := range seasonMap {
				seasons = append(seasons, map[string]interface{}{"number": sNum, "episodes": sEps})
			}
			hybridPayloads = append(hybridPayloads, map[string]interface{}{
				"ids":     formatMDBListIDsMap(mdblistIDs{imdb: key.imdb, tmdb: key.tmdb}),
				"seasons": seasons,
			})
		}
		for i := 0; i < len(hybridPayloads); i += batchSize {
			end := i + batchSize
			if end > len(hybridPayloads) {
				end = len(hybridPayloads)
			}
			batch := map[string]interface{}{"shows": hybridPayloads[i:end]}
			body, _ := json.Marshal(batch)
			respBody, err := postToMDBListBody(apiKey, "/sync/watched", string(body))
			if err != nil {
				result.Count = syncCount
				return result, fmt.Errorf("MDBList hybrid sync batch: %w", err)
			}
			nf := parseMDBListNotFoundEpisodes(respBody)
			count := 0
			for _, show := range hybridPayloads[i:end] {
				for _, season := range show["seasons"].([]map[string]interface{}) {
					count += len(season["episodes"].([]map[string]interface{}))
				}
			}
			count -= len(nf)
			if count > 0 {
				syncCount += count
			}
		}
	}

	removeMovies, removeShows, removeSkipped := buildMDBListRemovalPayload(toRemove)
	skippedNoIDs += removeSkipped
	for i := 0; i < len(removeMovies); i += batchSize {
		end := i + batchSize
		if end > len(removeMovies) {
			end = len(removeMovies)
		}
		body, _ := json.Marshal(map[string]interface{}{"movies": removeMovies[i:end]})
		if err := postToMDBList(apiKey, "/sync/watched/remove", string(body)); err != nil {
			result.Count = syncCount
			return result, fmt.Errorf("MDBList remove movies batch: %w", err)
		}
		syncCount += end - i
	}
	for i := 0; i < len(removeShows); i += batchSize {
		end := i + batchSize
		if end > len(removeShows) {
			end = len(removeShows)
		}
		body, _ := json.Marshal(map[string]interface{}{"shows": removeShows[i:end]})
		if err := postToMDBList(apiKey, "/sync/watched/remove", string(body)); err != nil {
			result.Count = syncCount
			return result, fmt.Errorf("MDBList remove shows batch: %w", err)
		}
		for _, show := range removeShows[i:end] {
			for _, season := range show["seasons"].([]map[string]interface{}) {
				syncCount += len(season["episodes"].([]map[string]interface{}))
			}
		}
	}

	result.Count = syncCount
	log.Printf("[scheduler] Synced %d/%d changes to MDBList (skippedNoIDs=%d fullExport=%v)",
		syncCount, len(toSync)+len(toRemove), skippedNoIDs, isFullExport)

	if err := finish(); err != nil {
		return result, err
	}
	if isFullExport {
		log.Printf("[scheduler] Full MDBList history export complete, next full export in %v", fullExportInterval)
	}

	return result, nil
}

// fetchMDBListRemoteHistoryKeys pages through the full MDBList watched history
// (no since filter) and returns the keys used to match local history.
func (s *Service) fetchMDBListRemoteHistoryKeys(task config.ScheduledTask, account *config.MDBListAccount) (map[string]bool, error) {
	parent := s.taskContext()
	ctx, cancel := context.WithTimeout(parent, 30*time.Minute)
	defer cancel()
	httpClient := &http.Client{Timeout: 30 * time.Second}
	keys := make(map[string]bool)
	offset, limit := 0, 500
	for {
		values := url.Values{"apikey": {account.APIKey}, "limit": {strconv.Itoa(limit)}, "offset": {strconv.Itoa(offset)}}
		resp, err := fetchMDBListHistoryPage(ctx, httpClient, "https://api.mdblist.com/sync/watched?"+values.Encode(), task.ID, offset, waitMDBListRetry)
		if err != nil {
			return nil, fmt.Errorf("list MDBList history for removal check: %w", err)
		}
		var page *struct {
			Movies []struct {
				LastWatchedAt string `json:"last_watched_at"`
				Movie         struct {
					IDs struct {
						IMDB string `json:"imdb"`
						TMDB int    `json:"tmdb"`
					} `json:"ids"`
				} `json:"movie"`
			} `json:"movies"`
			Episodes []struct {
				LastWatchedAt string `json:"last_watched_at"`
				Episode       struct {
					Season int `json:"season"`
					Number int `json:"number"`
					Show   struct {
						IDs struct {
							IMDB string `json:"imdb"`
							TMDB int    `json:"tmdb"`
						} `json:"ids"`
					} `json:"show"`
				} `json:"episode"`
			} `json:"episodes"`
			Shows      []json.RawMessage `json:"shows"`
			Seasons    []json.RawMessage `json:"seasons"`
			Pagination struct {
				HasMore bool `json:"has_more"`
				Limit   int  `json:"limit"`
			} `json:"pagination"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("decode MDBList history for removal check: %w", decodeErr)
		}
		if page == nil {
			return nil, errors.New("MDBList returned null instead of history")
		}
		for _, m := range page.Movies {
			if m.LastWatchedAt == "" {
				continue
			}
			for _, key := range mdblistHistoryKeys("movie", mdblistIDs{imdb: m.Movie.IDs.IMDB, tmdb: m.Movie.IDs.TMDB}, 0, 0) {
				keys[key] = true
			}
		}
		for _, e := range page.Episodes {
			if e.LastWatchedAt == "" {
				continue
			}
			ids := mdblistIDs{imdb: e.Episode.Show.IDs.IMDB, tmdb: e.Episode.Show.IDs.TMDB}
			for _, key := range mdblistHistoryKeys("episode", ids, e.Episode.Season, e.Episode.Number) {
				keys[key] = true
			}
		}
		if !page.Pagination.HasMore {
			return keys, nil
		}
		step := page.Pagination.Limit
		if step <= 0 {
			step = limit
		}
		if len(page.Movies)+len(page.Episodes)+len(page.Shows)+len(page.Seasons) == 0 {
			return nil, fmt.Errorf("MDBList history returned an empty page with has_more=true (offset %d)", offset)
		}
		offset += step
	}
}

// mdblistLocalHistoryKeys mirrors the IDs and coordinates the exporter sends,
// including the hybrid absolute number used for not_found retries.
func mdblistLocalHistoryKeys(item models.WatchHistoryItem) []string {
	if item.MediaType == "movie" {
		return mdblistHistoryKeys("movie", extractMDBListIDs(item.ExternalIDs), 0, 0)
	}
	ids := extractMDBListIDs(mediaidentity.EnrichShowExternalIDs(item.SeriesID, item.ItemID, item.ExternalIDs))
	keys := mdblistHistoryKeys("episode", ids, item.SeasonNumber, item.EpisodeNumber)
	if absolute := mdblist.HybridEpisodeNumber(item.EpisodeNumber, item.ExternalIDs); absolute > 0 && absolute != item.EpisodeNumber {
		keys = append(keys, mdblistHistoryKeys("episode", ids, item.SeasonNumber, absolute)...)
	}
	return keys
}

func mdblistHistoryKeys(mediaType string, ids mdblistIDs, season, episode int) []string {
	var keys []string
	switch mediaType {
	case "movie":
		if ids.tmdb > 0 {
			keys = append(keys, fmt.Sprintf("movie:tmdb:%d", ids.tmdb))
		}
		if imdb := strings.ToLower(strings.TrimSpace(ids.imdb)); imdb != "" {
			keys = append(keys, "movie:imdb:"+imdb)
		}
	case "episode":
		if season < 0 || episode <= 0 {
			return nil
		}
		if ids.tmdb > 0 {
			keys = append(keys, fmt.Sprintf("episode:tmdb:%d:%d:%d", ids.tmdb, season, episode))
		}
		if imdb := strings.ToLower(strings.TrimSpace(ids.imdb)); imdb != "" {
			keys = append(keys, fmt.Sprintf("episode:imdb:%s:%d:%d", imdb, season, episode))
		}
	}
	return keys
}

// API keys live in MDBList query strings; do not expose the URL in task errors.
func mdblistRequestError(err error) error {
	var requestErr *url.Error
	if errors.As(err, &requestErr) {
		return requestErr.Err
	}
	return err
}

func mdblistReportedTotal(total *int) string {
	if total == nil {
		return "unknown"
	}
	return strconv.Itoa(*total)
}
