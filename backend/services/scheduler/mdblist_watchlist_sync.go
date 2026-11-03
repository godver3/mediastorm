package scheduler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"novastream/config"
	"novastream/models"
)

// MDBList watchlist export and bidirectional sync. Importing alone still goes
// through syncMDBListWatchlistToLocal; this adds target_to_source (mediastorm →
// MDBList) and bidirectional, with the same removal rules as the Trakt and
// Simkl watchlist syncs.

const mdblistWatchlistPageLimit = 1000

// mdblistWatchlistItem is one entry of GET /watchlist/items.
type mdblistWatchlistItem struct {
	Title       string `json:"title"`
	ReleaseYear int    `json:"release_year"`
	MediaType   string `json:"mediatype"` // "movie" or "show"
	IMDBID      string `json:"imdb_id"`
	IDs         struct {
		IMDB string `json:"imdb"`
		TMDB int    `json:"tmdb"`
		TVDB *int   `json:"tvdb"` // nullable
	} `json:"ids"`
}

// fetchMDBListWatchlist reads the whole watchlist, following the cursor
// pagination (1000 items per page).
func fetchMDBListWatchlist(apiKey string) ([]mdblistWatchlistItem, error) {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	var items []mdblistWatchlistItem
	movies, shows := 0, 0
	cursor := ""
	for page := 0; page < 100; page++ {
		q := url.Values{"apikey": {apiKey}, "limit": {strconv.Itoa(mdblistWatchlistPageLimit)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		req, _ := http.NewRequest(http.MethodGet, "https://api.mdblist.com/watchlist/items?"+q.Encode(), nil)
		req.Header.Set("User-Agent", "mediastorm/1.0")
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch MDBList watchlist: %w", mdblistRequestError(err))
		}
		var body struct {
			Movies     []mdblistWatchlistItem `json:"movies"`
			Shows      []mdblistWatchlistItem `json:"shows"`
			Pagination struct {
				HasMore    bool   `json:"has_more"`
				NextCursor string `json:"next_cursor"`
			} `json:"pagination"`
		}
		status := resp.StatusCode
		decodeErr := json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if status >= 400 {
			return nil, fmt.Errorf("MDBList /watchlist/items returned %d", status)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("decode MDBList watchlist: %w", decodeErr)
		}
		for i := range body.Movies {
			body.Movies[i].MediaType = "movie"
			items = append(items, body.Movies[i])
		}
		for i := range body.Shows {
			body.Shows[i].MediaType = "show"
			items = append(items, body.Shows[i])
		}
		movies += len(body.Movies)
		shows += len(body.Shows)
		if !body.Pagination.HasMore || body.Pagination.NextCursor == "" {
			log.Printf("[scheduler] Fetched %d items from MDBList watchlist (%d movies, %d shows)", len(items), movies, shows)
			return items, nil
		}
		cursor = body.Pagination.NextCursor
	}
	return nil, fmt.Errorf("MDBList watchlist has more than %d pages", 100)
}

// mdblistWatchlistUpsert maps an MDBList entry to a local watchlist upsert.
func (s *Service) mdblistWatchlistUpsert(item mdblistWatchlistItem, syncSource string, now time.Time) (models.WatchlistUpsert, bool) {
	mediaType := "movie"
	if item.MediaType == "show" {
		mediaType = "series"
	}
	extIDs := make(map[string]string)
	if imdb := item.IDs.IMDB; imdb != "" {
		extIDs["imdb"] = imdb
	} else if item.IMDBID != "" {
		extIDs["imdb"] = item.IMDBID
	}
	if item.IDs.TMDB != 0 {
		extIDs["tmdb"] = strconv.Itoa(item.IDs.TMDB)
	}
	if item.IDs.TVDB != nil && *item.IDs.TVDB != 0 {
		extIDs["tvdb"] = strconv.Itoa(*item.IDs.TVDB)
	}
	id := preferredWatchlistSourceID(extIDs)
	if id == "" {
		return models.WatchlistUpsert{}, false
	}
	return s.enrichSyncedWatchlistIdentity("MDBList", models.WatchlistUpsert{
		ID:          id,
		MediaType:   mediaType,
		Name:        item.Title,
		Year:        item.ReleaseYear,
		ExternalIDs: extIDs,
		SyncSource:  syncSource,
		SyncedAt:    &now,
	}), true
}

// mdblistWatchlistIDs returns the {imdb, tmdb} object MDBList's watchlist
// writes accept, or ok=false when the item has neither.
func mdblistWatchlistIDs(mediaType, id string, externalIDs map[string]string) (map[string]interface{}, bool) {
	ids := extractSimklIDs(mediaType, id, "", externalIDs)
	out := formatMDBListIDsMap(mdblistIDs{imdb: ids.IMDB, tmdb: ids.TMDB})
	return out, len(out) > 0
}

// postMDBListWatchlist adds to or removes from the watchlist and returns how
// many items MDBList could not match.
func postMDBListWatchlist(apiKey, path string, movies, shows []map[string]interface{}) (int, error) {
	if len(movies)+len(shows) == 0 {
		return 0, nil
	}
	payload := map[string]interface{}{}
	if len(movies) > 0 {
		payload["movies"] = movies
	}
	if len(shows) > 0 {
		payload["shows"] = shows
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	respBody, err := postToMDBListBody(apiKey, path, string(body))
	if err != nil {
		return 0, err
	}
	return mdblistNotFoundCount(respBody), nil
}

// mdblistNotFoundCount reads not_found from a watchlist write response. The API
// answers with per-type counts ({"movies":0,"shows":0}) although its schema
// documents a single integer; accept both.
func mdblistNotFoundCount(respBody []byte) int {
	var resp struct {
		NotFound json.RawMessage `json:"not_found"`
	}
	if json.Unmarshal(respBody, &resp) != nil || len(resp.NotFound) == 0 {
		return 0
	}
	if n, err := strconv.Atoi(string(resp.NotFound)); err == nil {
		return n
	}
	var perType map[string]int
	if json.Unmarshal(resp.NotFound, &perType) != nil {
		return 0
	}
	total := 0
	for _, n := range perType {
		total += n
	}
	return total
}

// syncMDBListWatchlist handles target_to_source and bidirectional. tracker
// (nil without a snapshot store, or for export-only) decides which local items
// were removed on MDBList.
func (s *Service) syncMDBListWatchlist(account *config.MDBListAccount, profileID, direction, syncSource, deleteBehavior string, since *time.Time, dryRun bool, tracker *watchlistRemovalTracker) (SyncResult, error) {
	now := time.Now().UTC()
	result := SyncResult{DryRun: dryRun}
	importing := direction == "bidirectional"
	propagateDeletes := deleteBehavior != "additive"

	items, err := fetchMDBListWatchlist(account.APIKey)
	if err != nil {
		return result, err
	}
	type remoteItem struct {
		item  mdblistWatchlistItem
		input models.WatchlistUpsert
	}
	remote := make([]remoteItem, 0, len(items))
	remoteKeys := make(map[string]bool)
	for _, item := range items {
		input, ok := s.mdblistWatchlistUpsert(item, syncSource, now)
		if !ok {
			continue
		}
		remote = append(remote, remoteItem{item: item, input: input})
		for _, key := range schedulerWatchlistMatchKeys(input.MediaType, input.ID, input.ExternalIDs) {
			remoteKeys[key] = true
		}
	}
	tracker.setRemote(remoteKeys)

	localItems, err := s.watchlistService.List(profileID)
	if err != nil {
		return result, fmt.Errorf("list local items: %w", err)
	}
	localKeys := make(map[string]bool)
	for _, item := range localItems {
		for _, key := range schedulerWatchlistMatchKeys(item.MediaType, item.ID, item.ExternalIDs) {
			localKeys[key] = true
		}
	}

	synced := 0
	var addMovies, addShows, removeMovies, removeShows []map[string]interface{}
	var exportedItems []models.WatchlistItem

	// MDBList → local: import new items, or push local removals to MDBList.
	for _, r := range remote {
		input := r.input
		if schedulerWatchlistHasAnyKey(localKeys, input.MediaType, input.ID, input.ExternalIDs) {
			if importing && !dryRun {
				if _, err := s.watchlistService.AddOrUpdate(profileID, input); err != nil {
					logWatchlistImportError("MDBList", r.item.Title, err)
				}
			}
			continue
		}
		if propagateDeletes {
			removedLocally := since != nil && s.watchlistService.RemovedSince(profileID, input.MediaType, input.ID, input.ExternalIDs, *since)
			// target_to_source mirror makes MDBList match local exactly.
			if removedLocally || (!importing && deleteBehavior == "mirror") {
				ids, ok := mdblistWatchlistIDs(input.MediaType, input.ID, input.ExternalIDs)
				if !ok {
					continue
				}
				if dryRun {
					result.ToRemove = append(result.ToRemove, config.DryRunItem{Name: r.item.Title + " (from MDBList)", MediaType: input.MediaType, ID: input.ID})
					continue
				}
				if input.MediaType == "movie" {
					removeMovies = append(removeMovies, ids)
				} else {
					removeShows = append(removeShows, ids)
				}
				log.Printf("[scheduler] Will remove from MDBList watchlist (not on local watchlist): %s", r.item.Title)
				continue
			}
		}
		if !importing {
			continue
		}
		if dryRun {
			if s.watchlistService.RemovedSince(profileID, input.MediaType, input.ID, input.ExternalIDs, time.Time{}) {
				continue // the import skips items the user removed locally
			}
			result.ToAdd = append(result.ToAdd, config.DryRunItem{Name: r.item.Title + " (from MDBList)", MediaType: input.MediaType, ID: input.ID})
			synced++
			continue
		}
		if _, err := s.watchlistService.AddOrUpdate(profileID, input); err != nil {
			logWatchlistImportError("MDBList", r.item.Title, err)
			continue
		}
		log.Printf("[scheduler] Imported from MDBList: %s", r.item.Title)
		synced++
	}

	// local → MDBList: export new local items, or apply MDBList removals locally.
	for _, item := range localItems {
		if schedulerWatchlistHasAnyKey(remoteKeys, item.MediaType, item.ID, item.ExternalIDs) {
			continue
		}
		if importing && propagateDeletes {
			removedOnMDBList := item.SyncSource == syncSource
			if tracker != nil {
				removedOnMDBList = tracker.removedRemotely(item)
			}
			if removedOnMDBList {
				if dryRun {
					result.ToRemove = append(result.ToRemove, config.DryRunItem{Name: item.Name, MediaType: item.MediaType, ID: item.ID})
					continue
				}
				remove := s.watchlistService.RemoveSynced
				if tracker != nil {
					// Record it like a user removal so the other watchlist
					// syncs remove it too instead of importing it back.
					remove = s.watchlistService.Remove
				}
				if ok, err := remove(profileID, item.MediaType, item.ID); err != nil {
					log.Printf("[scheduler] Failed to remove watchlist item %s: %v", item.Name, err)
				} else if ok {
					log.Printf("[scheduler] Removed watchlist item no longer on MDBList: %s", item.Name)
				}
				continue
			}
		}
		ids, ok := mdblistWatchlistIDs(item.MediaType, item.ID, item.ExternalIDs)
		if !ok {
			log.Printf("[scheduler] Skipping export of %s: no TMDB/IMDb ID for MDBList", item.Name)
			continue
		}
		if dryRun {
			result.ToAdd = append(result.ToAdd, config.DryRunItem{Name: item.Name + " (to MDBList)", MediaType: item.MediaType, ID: item.ID})
			synced++
			continue
		}
		switch item.MediaType {
		case "movie":
			addMovies = append(addMovies, ids)
		case "series":
			addShows = append(addShows, ids)
		default:
			continue
		}
		exportedItems = append(exportedItems, item)
		synced++
	}

	if !dryRun && len(addMovies)+len(addShows) > 0 {
		notFound, err := postMDBListWatchlist(account.APIKey, "/watchlist/items/add", addMovies, addShows)
		if err != nil {
			return result, fmt.Errorf("add to MDBList watchlist: %w", err)
		}
		log.Printf("[scheduler] Exported %d movies and %d shows to MDBList watchlist (%d not found on MDBList)", len(addMovies), len(addShows), notFound)
		if importing && tracker == nil {
			// Without snapshots the marker is how a later removal on MDBList
			// is recognised.
			for _, item := range exportedItems {
				if _, err := s.watchlistService.AddOrUpdate(profileID, models.WatchlistUpsert{ID: item.ID, MediaType: item.MediaType, SyncSource: syncSource, SyncedAt: &now}); err != nil {
					log.Printf("[scheduler] Failed to mark %s as synced to MDBList: %v", item.Name, err)
				}
			}
		}
	}

	removalFailed := false
	if !dryRun && len(removeMovies)+len(removeShows) > 0 {
		if _, err := postMDBListWatchlist(account.APIKey, "/watchlist/items/remove", removeMovies, removeShows); err != nil {
			log.Printf("[scheduler] Failed to remove items from MDBList watchlist: %v", err)
			removalFailed = true
		} else {
			log.Printf("[scheduler] Removed %d items from MDBList watchlist", len(removeMovies)+len(removeShows))
		}
	}
	if !dryRun {
		if err := s.saveWatchlistRemovalSnapshot(tracker); err != nil {
			log.Printf("[scheduler] %v", err)
		}
	}
	// A failed removal keeps the previous cursor so the next run retries it.
	if !removalFailed {
		result.Config = map[string]string{watchlistSyncCursorKey: now.Format(time.RFC3339Nano)}
	}
	result.Count = synced
	return result, nil
}
