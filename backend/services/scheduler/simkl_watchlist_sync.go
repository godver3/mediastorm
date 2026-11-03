package scheduler

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/simkl"
)

// Simkl watchlist sync mirrors the Trakt list sync for Simkl's Plan to Watch
// list. Directions: source_to_target (Simkl → mediastorm), target_to_source
// (mediastorm → Simkl) and bidirectional; deleteBehavior additive, delete or
// mirror, with the same deletion rules as the Trakt watchlist sync.
//
// Items on any other Simkl list (watching, completed, hold, dropped) are never
// exported: add-to-list would move them back to Plan to Watch.

// simklWatchlistBuckets are the Simkl media buckets read for list state.
var simklWatchlistBuckets = []string{"movies", "shows", "anime"}

// simklRemoteItem is one Simkl list entry resolved to a local watchlist shape.
type simklRemoteItem struct {
	item  simkl.ListItem
	input models.WatchlistUpsert
}

func (s *Service) executeSimklWatchlistSync(task config.ScheduledTask) (SyncResult, error) {
	s.mu.RLock()
	simklClient := s.simklClient
	s.mu.RUnlock()
	if simklClient == nil {
		return SyncResult{}, errors.New("simkl client not configured")
	}

	simklAccountID := task.Config["simklAccountId"]
	profileID, err := s.resolveTaskProfileID(task)
	if simklAccountID == "" || profileID == "" {
		return SyncResult{}, errors.New("missing simklAccountId or profileId in task config")
	}
	if err != nil {
		return SyncResult{}, err
	}

	direction := task.Config["syncDirection"]
	if direction == "" {
		direction = "source_to_target"
	}
	if direction != "source_to_target" && direction != "target_to_source" && direction != "bidirectional" {
		return SyncResult{}, fmt.Errorf("unknown sync direction: %s", direction)
	}
	deleteBehavior := task.Config["deleteBehavior"]
	if deleteBehavior == "" {
		deleteBehavior = "additive"
	}
	dryRun := task.Config["dryRun"] == "true"

	settings, err := s.configManager.Load()
	if err != nil {
		return SyncResult{}, fmt.Errorf("load settings: %w", err)
	}
	account := settings.Simkl.GetAccountByID(simklAccountID)
	if account == nil {
		return SyncResult{}, errors.New("simkl account not found")
	}
	if account.ClientID == "" || account.AccessToken == "" {
		return SyncResult{}, errors.New("simkl account not authenticated")
	}
	if _, err := simklClient.EnsureValidToken(account, s.configManager); err != nil {
		return SyncResult{}, err
	}

	syncSource := fmt.Sprintf("simkl:%s:%s:%s", simklAccountID, simkl.PlanToWatchStatus, task.ID)
	var tracker *watchlistRemovalTracker
	if direction != "target_to_source" {
		if tracker, err = s.newWatchlistRemovalTracker(task.ID, "simkl", simklAccountID, profileID); err != nil {
			return SyncResult{}, err
		}
	}
	return s.syncSimklWatchlist(simklClient, account, profileID, direction, syncSource, deleteBehavior, watchlistSyncSince(task), dryRun, tracker)
}

// syncSimklWatchlist runs one sync. tracker (nil without a snapshot store)
// decides which local items were removed on Simkl; without it the SyncSource
// marker is used.
func (s *Service) syncSimklWatchlist(client *simkl.Client, account *config.SimklAccount, profileID, direction, syncSource, deleteBehavior string, since *time.Time, dryRun bool, tracker *watchlistRemovalTracker) (SyncResult, error) {
	now := time.Now().UTC()
	result := SyncResult{DryRun: dryRun}
	importing := direction == "source_to_target" || direction == "bidirectional"
	exporting := direction == "target_to_source" || direction == "bidirectional"
	propagateDeletes := deleteBehavior != "additive"

	planToWatch, onAnyList, err := s.fetchSimklWatchlistState(client, account, syncSource, now)
	if err != nil {
		return result, err
	}
	planKeys := make(map[string]bool)
	for _, remote := range planToWatch {
		for _, key := range schedulerWatchlistMatchKeys(remote.input.MediaType, remote.input.ID, remote.input.ExternalIDs) {
			planKeys[key] = true
		}
	}

	tracker.setRemote(planKeys)

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
	var addMovies, addShows, removeMovies, removeShows []simkl.WatchlistEntry
	var exportedItems []models.WatchlistItem
	queueRemoteRemoval := func(remote simklRemoteItem, reason string) {
		if dryRun {
			result.ToRemove = append(result.ToRemove, config.DryRunItem{Name: remote.item.Title + " (from Simkl)", MediaType: remote.input.MediaType, ID: remote.input.ID})
			return
		}
		entry := simkl.WatchlistEntry{IDs: remote.item.IDs}
		if remote.input.MediaType == "movie" {
			removeMovies = append(removeMovies, entry)
		} else {
			removeShows = append(removeShows, entry)
		}
		log.Printf("[scheduler] Will remove from Simkl Plan to Watch (%s): %s", reason, remote.item.Title)
	}
	removeLocal := func(item models.WatchlistItem, reason string, removedOnSimkl bool) {
		if dryRun {
			result.ToRemove = append(result.ToRemove, config.DryRunItem{Name: item.Name, MediaType: item.MediaType, ID: item.ID})
			return
		}
		remove := s.watchlistService.RemoveSynced
		if removedOnSimkl && tracker != nil {
			// Record it like a user removal so the other watchlist syncs
			// (Trakt, …) remove it too instead of importing it back.
			remove = s.watchlistService.Remove
		}
		if ok, err := remove(profileID, item.MediaType, item.ID); err != nil {
			log.Printf("[scheduler] Failed to remove watchlist item %s: %v", item.Name, err)
		} else if ok {
			log.Printf("[scheduler] Removed watchlist item (%s): %s", reason, item.Name)
		}
	}

	// Simkl → local: import Plan to Watch items, or push local removals to Simkl.
	for _, remote := range planToWatch {
		input := remote.input
		inLocal := schedulerWatchlistHasAnyKey(localKeys, input.MediaType, input.ID, input.ExternalIDs)
		if inLocal {
			if importing && !dryRun {
				if _, err := s.watchlistService.AddOrUpdate(profileID, input); err != nil {
					logWatchlistImportError("Simkl", remote.item.Title, err)
				}
			}
			continue
		}
		if exporting && propagateDeletes {
			removedLocally := since != nil && s.watchlistService.RemovedSince(profileID, input.MediaType, input.ID, input.ExternalIDs, *since)
			// target_to_source mirror makes Simkl match local exactly.
			if removedLocally || (!importing && deleteBehavior == "mirror") {
				queueRemoteRemoval(remote, "not on local watchlist")
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
			result.ToAdd = append(result.ToAdd, config.DryRunItem{Name: remote.item.Title + " (from Simkl)", MediaType: input.MediaType, ID: input.ID})
			synced++
			continue
		}
		if _, err := s.watchlistService.AddOrUpdate(profileID, input); err != nil {
			logWatchlistImportError("Simkl", remote.item.Title, err)
			continue
		}
		log.Printf("[scheduler] Imported from Simkl: %s", remote.item.Title)
		synced++
	}

	// local → Simkl: export new local items, or apply Simkl removals locally.
	for _, item := range localItems {
		if schedulerWatchlistHasAnyKey(planKeys, item.MediaType, item.ID, item.ExternalIDs) {
			continue
		}
		if importing && propagateDeletes {
			// Items this task synced were on Simkl before, so their absence
			// means they left Plan to Watch there (removed, or watched).
			// source_to_target mirror makes local match Simkl exactly.
			removedOnSimkl := item.SyncSource == syncSource
			if tracker != nil {
				removedOnSimkl = tracker.removedRemotely(item)
			}
			if removedOnSimkl || (!exporting && deleteBehavior == "mirror") {
				removeLocal(item, "no longer on Simkl Plan to Watch", removedOnSimkl)
				continue
			}
		}
		if !exporting {
			continue
		}
		if schedulerWatchlistHasAnyKey(onAnyList, item.MediaType, item.ID, item.ExternalIDs) {
			continue // tracked on another Simkl list; don't move it back
		}
		entry, ok := simklWatchlistEntryFor(item)
		if !ok {
			log.Printf("[scheduler] Skipping export of %s: no TMDB/IMDb/TVDB ID for Simkl", item.Name)
			continue
		}
		if dryRun {
			result.ToAdd = append(result.ToAdd, config.DryRunItem{Name: item.Name + " (to Simkl)", MediaType: item.MediaType, ID: item.ID})
			synced++
			continue
		}
		switch item.MediaType {
		case "movie":
			addMovies = append(addMovies, entry)
		case "series":
			addShows = append(addShows, entry)
		default:
			continue
		}
		exportedItems = append(exportedItems, item)
		synced++
	}

	if !dryRun && len(addMovies)+len(addShows) > 0 {
		resp, err := client.AddToPlanToWatch(account.ClientID, account.AccessToken, addMovies, addShows)
		if err != nil {
			return result, fmt.Errorf("add to simkl plan to watch: %w", err)
		}
		log.Printf("[scheduler] Exported %d movies and %d shows to Simkl Plan to Watch (%d not found on Simkl)", len(addMovies), len(addShows), resp.NotFoundCount())
		if importing {
			// Mark exports as synced so a later removal on Simkl is recognised.
			for _, item := range exportedItems {
				if _, err := s.watchlistService.AddOrUpdate(profileID, models.WatchlistUpsert{ID: item.ID, MediaType: item.MediaType, SyncSource: syncSource, SyncedAt: &now}); err != nil {
					log.Printf("[scheduler] Failed to mark %s as synced to Simkl: %v", item.Name, err)
				}
			}
		}
	}

	removalFailed := false
	if !dryRun && len(removeMovies)+len(removeShows) > 0 {
		if err := client.RemoveFromWatchlist(account.ClientID, account.AccessToken, removeMovies, removeShows); err != nil {
			log.Printf("[scheduler] Failed to remove items from Simkl Plan to Watch: %v", err)
			removalFailed = true
		} else {
			log.Printf("[scheduler] Removed %d items from Simkl Plan to Watch", len(removeMovies)+len(removeShows))
		}
	}
	if !dryRun {
		if err := s.saveWatchlistRemovalSnapshot(tracker); err != nil {
			log.Printf("[scheduler] %v", err)
		}
	}
	// A failed removal keeps the previous cursor so the next run retries it.
	if exporting && !removalFailed {
		result.Config = map[string]string{watchlistSyncCursorKey: now.Format(time.RFC3339Nano)}
	}
	result.Count = synced
	return result, nil
}

// fetchSimklWatchlistState reads every Simkl list once. It returns the Plan to
// Watch entries resolved for import, plus match keys for items on any list.
func (s *Service) fetchSimklWatchlistState(client *simkl.Client, account *config.SimklAccount, syncSource string, now time.Time) ([]simklRemoteItem, map[string]bool, error) {
	var planToWatch []simklRemoteItem
	onAnyList := make(map[string]bool)
	for _, bucket := range simklWatchlistBuckets {
		items, err := client.GetListItems(account.ClientID, account.AccessToken, bucket, "")
		if err != nil {
			return nil, nil, fmt.Errorf("get simkl %s list: %w", bucket, err)
		}
		for _, item := range items {
			mediaType := "series"
			if item.MediaType == "movie" {
				mediaType = "movie"
			}
			extIDs := simklListItemExternalIDs(item.IDs)
			id := preferredWatchlistSourceID(extIDs)
			if id == "" {
				continue
			}
			for _, key := range schedulerWatchlistMatchKeys(mediaType, id, extIDs) {
				onAnyList[key] = true
			}
			if item.Status != simkl.PlanToWatchStatus {
				continue
			}
			input := s.enrichSyncedWatchlistIdentity("Simkl", models.WatchlistUpsert{
				ID:          id,
				MediaType:   mediaType,
				Name:        item.Title,
				Year:        item.Year,
				ExternalIDs: extIDs,
				SyncSource:  syncSource,
				SyncedAt:    &now,
			})
			planToWatch = append(planToWatch, simklRemoteItem{item: item, input: input})
		}
	}
	log.Printf("[scheduler] Fetched %d Simkl Plan to Watch items", len(planToWatch))
	return planToWatch, onAnyList, nil
}

func simklListItemExternalIDs(ids simkl.IDs) map[string]string {
	out := make(map[string]string)
	if ids.TMDB != 0 {
		out["tmdb"] = strconv.Itoa(ids.TMDB)
	}
	if ids.IMDB != "" {
		out["imdb"] = ids.IMDB
	}
	if ids.TVDB != 0 {
		out["tvdb"] = strconv.Itoa(ids.TVDB)
	}
	if ids.Simkl != 0 {
		out["simkl"] = strconv.Itoa(ids.Simkl)
	}
	return out
}

// simklWatchlistEntryFor builds the add-to-list entry for a local item. It
// sends IDs only: given a title, Simkl falls back to a fuzzy title match when
// it doesn't know the IDs and can file the item under a different show (the
// 2003 Battlestar Galactica miniseries became the 2004 series). Without one it
// reports the item as not found instead.
func simklWatchlistEntryFor(item models.WatchlistItem) (simkl.WatchlistEntry, bool) {
	ids := extractSimklIDs(item.MediaType, item.ID, "", item.ExternalIDs)
	if ids.TMDB == 0 && ids.IMDB == "" && ids.TVDB == 0 && ids.Simkl == 0 {
		if traktIDs, ok := traktSyncIDsFor(item.ID, item.ExternalIDs); ok {
			ids.TMDB, ids.IMDB = traktIDs.TMDB, traktIDs.IMDB
		}
	}
	if ids.TMDB == 0 && ids.IMDB == "" && ids.TVDB == 0 && ids.Simkl == 0 {
		return simkl.WatchlistEntry{}, false
	}
	return simkl.WatchlistEntry{IDs: ids}, true
}
