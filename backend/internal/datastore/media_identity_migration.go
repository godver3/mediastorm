package datastore

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"novastream/internal/mediaidentity"
	"novastream/models"
)

type mediaIdentityMigrationStats struct {
	watchlistScanned int
	watchlistKept    int
	watchlistMerged  int
	watchlistRekeyed int

	historyScanned int
	historyKept    int
	historyMerged  int
	historyRekeyed int

	progressScanned        int
	progressKept           int
	progressMerged         int
	progressRekeyed        int
	progressClearedWatched int
}

func reconcileMediaIdentityDataMigration(ctx context.Context, ds *DataStore) error {
	var stats mediaIdentityMigrationStats

	if err := ds.WithTx(ctx, func(tx *Tx) error {
		watchlistAll, err := tx.Watchlist().ListAll(ctx)
		if err != nil {
			return fmt.Errorf("list watchlist: %w", err)
		}
		reconciledWatchlist := make(map[string]map[string]models.WatchlistItem, len(watchlistAll))
		for userID, items := range watchlistAll {
			stats.watchlistScanned += len(items)
			reconciled, itemStats := reconcileWatchlistIdentityItems(items)
			stats.watchlistKept += len(reconciled)
			stats.watchlistMerged += itemStats.merged
			stats.watchlistRekeyed += itemStats.rekeyed
			reconciledWatchlist[userID] = reconciled
		}
		if err := rewriteWatchlistIdentityItems(ctx, tx, watchlistAll, reconciledWatchlist); err != nil {
			return err
		}

		historyAll, err := tx.WatchHistory().ListAll(ctx)
		if err != nil {
			return fmt.Errorf("list watch history: %w", err)
		}
		reconciledHistory := make(map[string]map[string]models.WatchHistoryItem, len(historyAll))
		for userID, items := range historyAll {
			stats.historyScanned += len(items)
			reconciled, itemStats := reconcileWatchHistoryIdentityItems(items)
			stats.historyKept += len(reconciled)
			stats.historyMerged += itemStats.merged
			stats.historyRekeyed += itemStats.rekeyed
			reconciledHistory[userID] = reconciled
		}
		if err := rewriteWatchHistoryIdentityItems(ctx, tx, historyAll, reconciledHistory); err != nil {
			return err
		}

		progressAll, err := tx.PlaybackProgress().ListAll(ctx)
		if err != nil {
			return fmt.Errorf("list playback progress: %w", err)
		}
		watchedByUser := watchedHistoryItemsByUserForMigration(reconciledHistory)
		reconciledProgress := make(map[string]map[string]models.PlaybackProgress, len(progressAll))
		for userID, items := range progressAll {
			stats.progressScanned += len(items)
			reconciled, itemStats := reconcilePlaybackProgressIdentityItems(items, watchedByUser[userID])
			stats.progressKept += len(reconciled)
			stats.progressMerged += itemStats.merged
			stats.progressRekeyed += itemStats.rekeyed
			stats.progressClearedWatched += itemStats.clearedWatched
			reconciledProgress[userID] = reconciled
		}
		if err := rewritePlaybackProgressIdentityItems(ctx, tx, progressAll, reconciledProgress); err != nil {
			return err
		}

		return nil
	}); err != nil {
		return err
	}

	log.Printf("[media-identity-migration] watchlist scanned=%d kept=%d merged=%d rekeyed=%d; history scanned=%d kept=%d merged=%d rekeyed=%d; progress scanned=%d kept=%d merged=%d rekeyed=%d clearedWatched=%d",
		stats.watchlistScanned, stats.watchlistKept, stats.watchlistMerged, stats.watchlistRekeyed,
		stats.historyScanned, stats.historyKept, stats.historyMerged, stats.historyRekeyed,
		stats.progressScanned, stats.progressKept, stats.progressMerged, stats.progressRekeyed, stats.progressClearedWatched)
	return nil
}

type identityItemStats struct {
	merged         int
	rekeyed        int
	clearedWatched int
}

type migrationIdentityIndex struct {
	keyByToken map[string]string
}

func newMigrationIdentityIndex(size int) *migrationIdentityIndex {
	return &migrationIdentityIndex{keyByToken: make(map[string]string, size*6)}
}

func (idx *migrationIdentityIndex) add(key string, identity mediaidentity.Identity) {
	for _, token := range migrationIdentityIndexTokens(identity) {
		if _, exists := idx.keyByToken[token]; exists {
			continue
		}
		idx.keyByToken[token] = key
	}
}

func (idx *migrationIdentityIndex) remove(key string, identity mediaidentity.Identity) {
	for _, token := range migrationIdentityIndexTokens(identity) {
		if idx.keyByToken[token] == key {
			delete(idx.keyByToken, token)
		}
	}
}

func migrationIdentityIndexTokens(identity mediaidentity.Identity) []string {
	tokens := make([]string, 0, len(identity.CandidateKeys)+len(identity.Tokens)+1)
	seen := make(map[string]struct{}, cap(tokens))
	add := func(token string) {
		token = strings.ToLower(strings.TrimSpace(token))
		if token == "" {
			return
		}
		if _, exists := seen[token]; exists {
			return
		}
		seen[token] = struct{}{}
		tokens = append(tokens, token)
	}
	add("key:" + identity.Key)
	for _, key := range identity.CandidateKeys {
		add("key:" + key)
	}
	for token := range identity.Tokens {
		add("token:" + token)
	}
	return tokens
}

func reconcileWatchlistIdentityItems(items []models.WatchlistItem) (map[string]models.WatchlistItem, identityItemStats) {
	perUser := make(map[string]models.WatchlistItem, len(items))
	var stats identityItemStats
	for _, item := range items {
		originalKey := item.Key()
		normalized := normalizeWatchlistIdentityItem(item)
		if !strings.EqualFold(originalKey, normalized.Key()) {
			stats.rekeyed++
		}
		if existing, found := takeWatchlistIdentityItem(perUser, normalized.MediaType, normalized.ID, normalized.ExternalIDs); found {
			normalized = mergeWatchlistIdentityItems(existing, normalized)
			stats.merged++
		}
		perUser[normalized.Key()] = normalized
	}
	return perUser, stats
}

func normalizeWatchlistIdentityItem(item models.WatchlistItem) models.WatchlistItem {
	identity := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   item.MediaType,
		ID:          item.ID,
		ExternalIDs: item.ExternalIDs,
	})
	item.MediaType = identity.MediaType
	item.ID = identity.ID
	item.ExternalIDs = identity.ExternalIDs
	if item.AddedAt.IsZero() {
		item.AddedAt = time.Now().UTC()
	}
	return item
}

func takeWatchlistIdentityItem(perUser map[string]models.WatchlistItem, mediaType, canonicalID string, externalIDs map[string]string) (models.WatchlistItem, bool) {
	incoming := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   mediaType,
		ID:          canonicalID,
		ExternalIDs: externalIDs,
	})
	for _, key := range incoming.CandidateKeys {
		if existing, ok := perUser[key]; ok {
			delete(perUser, key)
			return existing, true
		}
	}
	for key, existing := range perUser {
		current := mediaidentity.Resolve(mediaidentity.Input{
			MediaType:   existing.MediaType,
			ID:          existing.ID,
			ExternalIDs: existing.ExternalIDs,
		})
		if !mediaidentity.Equivalent(incoming, current) {
			continue
		}
		delete(perUser, key)
		return existing, true
	}
	return models.WatchlistItem{}, false
}

func mergeWatchlistIdentityItems(base, incoming models.WatchlistItem) models.WatchlistItem {
	base.ExternalIDs = mediaidentity.NormalizeExternalIDs(base.ExternalIDs)
	incoming.ExternalIDs = mediaidentity.NormalizeExternalIDs(incoming.ExternalIDs)
	if base.ID == "" {
		base.ID = incoming.ID
	}
	if base.MediaType == "" {
		base.MediaType = incoming.MediaType
	}
	if base.Name == "" {
		base.Name = incoming.Name
	}
	if base.Overview == "" {
		base.Overview = incoming.Overview
	}
	if base.Year == 0 {
		base.Year = incoming.Year
	}
	if base.PosterURL == "" {
		base.PosterURL = incoming.PosterURL
	}
	if base.TextPosterURL == "" {
		base.TextPosterURL = incoming.TextPosterURL
	}
	if base.BackdropURL == "" {
		base.BackdropURL = incoming.BackdropURL
	}
	if base.RuntimeMinutes == 0 {
		base.RuntimeMinutes = incoming.RuntimeMinutes
	}
	if base.AddedAt.IsZero() || (!incoming.AddedAt.IsZero() && incoming.AddedAt.Before(base.AddedAt)) {
		base.AddedAt = incoming.AddedAt
	}
	if strings.TrimSpace(base.SyncSource) == "" {
		base.SyncSource = incoming.SyncSource
	}
	if base.SyncedAt == nil || (incoming.SyncedAt != nil && incoming.SyncedAt.After(*base.SyncedAt)) {
		base.SyncedAt = incoming.SyncedAt
	}
	base.ExternalIDs = mediaidentity.MergeExternalIDs(base.ExternalIDs, incoming.ExternalIDs)
	if len(base.Genres) == 0 && len(incoming.Genres) > 0 {
		base.Genres = append([]string{}, incoming.Genres...)
	}
	return normalizeWatchlistIdentityItem(base)
}

func reconcileWatchHistoryIdentityItems(items []models.WatchHistoryItem) (map[string]models.WatchHistoryItem, identityItemStats) {
	perUser := make(map[string]models.WatchHistoryItem, len(items))
	index := newMigrationIdentityIndex(len(items))
	var stats identityItemStats
	for _, item := range items {
		originalKey := item.ID
		normalized := normalizeWatchHistoryIdentityItem(item)
		if !strings.EqualFold(originalKey, normalized.ID) {
			stats.rekeyed++
		}
		if existing, found := takeWatchHistoryIdentityItem(perUser, index, normalized); found {
			normalized = mergeWatchHistoryIdentityItems(existing, normalized)
			stats.merged++
		}
		perUser[normalized.ID] = normalized
		index.add(normalized.ID, watchHistoryIdentityForMigration(normalized))
	}
	return perUser, stats
}

func normalizeWatchHistoryIdentityItem(item models.WatchHistoryItem) models.WatchHistoryItem {
	storedExternalIDs := mediaidentity.StorageExternalIDs(item.MediaType, item.ItemID, item.SeriesID, item.ExternalIDs)
	identityExternalIDs := mediaidentity.IdentityExternalIDs(item.MediaType, item.ItemID, item.SeriesID, storedExternalIDs)
	identity := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:     item.MediaType,
		ID:            item.ItemID,
		SeriesID:      item.SeriesID,
		SeasonNumber:  item.SeasonNumber,
		EpisodeNumber: item.EpisodeNumber,
		ExternalIDs:   identityExternalIDs,
	})
	item.ID = identity.Key
	item.MediaType = identity.MediaType
	item.ItemID = identity.ID
	item.ExternalIDs = storedExternalIDs
	if identity.MediaType == "episode" {
		item.SeriesID = identity.SeriesID
		item.SeasonNumber = identity.SeasonNumber
		item.EpisodeNumber = identity.EpisodeNumber
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = item.WatchedAt
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = time.Now().UTC()
	}
	return item
}

func takeWatchHistoryIdentityItem(perUser map[string]models.WatchHistoryItem, index *migrationIdentityIndex, item models.WatchHistoryItem) (models.WatchHistoryItem, bool) {
	target := watchHistoryIdentityForMigration(item)
	for _, key := range target.CandidateKeys {
		if existing, ok := perUser[key]; ok {
			delete(perUser, key)
			index.remove(key, watchHistoryIdentityForMigration(existing))
			return existing, true
		}
	}
	for _, token := range migrationIdentityIndexTokens(target) {
		key, ok := index.keyByToken[token]
		if !ok {
			continue
		}
		existing, ok := perUser[key]
		if !ok {
			delete(index.keyByToken, token)
			continue
		}
		if !watchHistoryIdentityMatchesForMigration(existing, target) {
			continue
		}
		delete(perUser, key)
		index.remove(key, watchHistoryIdentityForMigration(existing))
		return existing, true
	}
	return models.WatchHistoryItem{}, false
}

func watchHistoryIdentityForMigration(item models.WatchHistoryItem) mediaidentity.Identity {
	externalIDs := mediaidentity.IdentityExternalIDs(item.MediaType, item.ItemID, item.SeriesID, item.ExternalIDs)
	return mediaidentity.Resolve(mediaidentity.Input{
		MediaType:     item.MediaType,
		ID:            item.ItemID,
		SeriesID:      item.SeriesID,
		SeasonNumber:  item.SeasonNumber,
		EpisodeNumber: item.EpisodeNumber,
		ExternalIDs:   externalIDs,
	})
}

func watchHistoryIdentityMatchesForMigration(item models.WatchHistoryItem, target mediaidentity.Identity) bool {
	current := watchHistoryIdentityForMigration(item)
	if current.Key == target.Key {
		return true
	}
	for _, key := range current.CandidateKeys {
		if key == target.Key {
			return true
		}
	}
	return mediaidentity.Equivalent(current, target)
}

func mergeWatchHistoryIdentityItems(base, incoming models.WatchHistoryItem) models.WatchHistoryItem {
	base = normalizeWatchHistoryIdentityItem(base)
	incoming = normalizeWatchHistoryIdentityItem(incoming)

	result := base
	if watchHistoryStateTimeForMigration(incoming).After(watchHistoryStateTimeForMigration(base)) {
		result.Watched = incoming.Watched
		result.WatchedAt = incoming.WatchedAt
		result.UpdatedAt = incoming.UpdatedAt
	}
	if result.Name == "" {
		result.Name = incoming.Name
	}
	if result.Year == 0 {
		result.Year = incoming.Year
	}
	if result.SeriesName == "" {
		result.SeriesName = incoming.SeriesName
	}
	result.ExternalIDs = mediaidentity.MergeExternalIDs(base.ExternalIDs, incoming.ExternalIDs)
	if incoming.SeasonNumber > 0 {
		result.SeasonNumber = incoming.SeasonNumber
	}
	if incoming.EpisodeNumber > 0 {
		result.EpisodeNumber = incoming.EpisodeNumber
	}
	if incoming.SeriesID != "" {
		result.SeriesID = incoming.SeriesID
	}
	return normalizeWatchHistoryIdentityItem(result)
}

func watchHistoryStateTimeForMigration(item models.WatchHistoryItem) time.Time {
	if !item.UpdatedAt.IsZero() {
		return item.UpdatedAt
	}
	return item.WatchedAt
}

func reconcilePlaybackProgressIdentityItems(items []models.PlaybackProgress, watchedItems []models.WatchHistoryItem) (map[string]models.PlaybackProgress, identityItemStats) {
	perUser := make(map[string]models.PlaybackProgress, len(items))
	index := newMigrationIdentityIndex(len(items))
	var stats identityItemStats
	for _, item := range items {
		originalKey := item.ID
		normalized := normalizePlaybackProgressIdentityItem(item)
		if isPlaybackProgressClearedByWatchedForMigration(normalized, watchedItems) {
			stats.clearedWatched++
			continue
		}
		if !strings.EqualFold(originalKey, normalized.ID) {
			stats.rekeyed++
		}
		if existing, found := takePlaybackProgressIdentityItem(perUser, index, normalized); found {
			normalized = mergePlaybackProgressIdentityItems(existing, normalized)
			stats.merged++
		}
		perUser[normalized.ID] = normalized
		index.add(normalized.ID, playbackProgressIdentityForMigration(normalized))
	}
	return perUser, stats
}

func normalizePlaybackProgressIdentityItem(progress models.PlaybackProgress) models.PlaybackProgress {
	storedExternalIDs := mediaidentity.StorageExternalIDs(progress.MediaType, progress.ItemID, progress.SeriesID, progress.ExternalIDs)
	identityExternalIDs := mediaidentity.IdentityExternalIDs(progress.MediaType, progress.ItemID, progress.SeriesID, storedExternalIDs)
	identity := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:     progress.MediaType,
		ID:            progress.ItemID,
		SeriesID:      progress.SeriesID,
		SeasonNumber:  progress.SeasonNumber,
		EpisodeNumber: progress.EpisodeNumber,
		ExternalIDs:   identityExternalIDs,
	})
	progress.ID = identity.Key
	progress.MediaType = identity.MediaType
	progress.ItemID = identity.ID
	progress.ExternalIDs = storedExternalIDs
	if identity.MediaType == "episode" {
		progress.SeriesID = identity.SeriesID
		progress.SeasonNumber = identity.SeasonNumber
		progress.EpisodeNumber = identity.EpisodeNumber
	}
	if progress.UpdatedAt.IsZero() {
		progress.UpdatedAt = time.Now().UTC()
	}
	return progress
}

func takePlaybackProgressIdentityItem(perUser map[string]models.PlaybackProgress, index *migrationIdentityIndex, item models.PlaybackProgress) (models.PlaybackProgress, bool) {
	target := playbackProgressIdentityForMigration(item)
	for _, key := range target.CandidateKeys {
		if existing, ok := perUser[key]; ok {
			delete(perUser, key)
			index.remove(key, playbackProgressIdentityForMigration(existing))
			return existing, true
		}
	}
	for _, token := range migrationIdentityIndexTokens(target) {
		key, ok := index.keyByToken[token]
		if !ok {
			continue
		}
		existing, ok := perUser[key]
		if !ok {
			delete(index.keyByToken, token)
			continue
		}
		if !playbackProgressIdentityMatchesForMigration(existing, target) {
			continue
		}
		delete(perUser, key)
		index.remove(key, playbackProgressIdentityForMigration(existing))
		return existing, true
	}
	return models.PlaybackProgress{}, false
}

func playbackProgressIdentityForMigration(progress models.PlaybackProgress) mediaidentity.Identity {
	externalIDs := mediaidentity.IdentityExternalIDs(progress.MediaType, progress.ItemID, progress.SeriesID, progress.ExternalIDs)
	return mediaidentity.Resolve(mediaidentity.Input{
		MediaType:     progress.MediaType,
		ID:            progress.ItemID,
		SeriesID:      progress.SeriesID,
		SeasonNumber:  progress.SeasonNumber,
		EpisodeNumber: progress.EpisodeNumber,
		ExternalIDs:   externalIDs,
	})
}

func playbackProgressIdentityMatchesForMigration(progress models.PlaybackProgress, target mediaidentity.Identity) bool {
	current := playbackProgressIdentityForMigration(progress)
	if current.Key == target.Key {
		return true
	}
	for _, key := range current.CandidateKeys {
		if key == target.Key {
			return true
		}
	}
	return mediaidentity.Equivalent(current, target)
}

func mergePlaybackProgressIdentityItems(base, incoming models.PlaybackProgress) models.PlaybackProgress {
	base = normalizePlaybackProgressIdentityItem(base)
	incoming = normalizePlaybackProgressIdentityItem(incoming)
	result := base
	if playbackProgressPrefersForMigration(incoming, base) {
		result = incoming
	}
	result.ExternalIDs = mediaidentity.MergeExternalIDs(base.ExternalIDs, incoming.ExternalIDs)
	return normalizePlaybackProgressIdentityItem(result)
}

func playbackProgressPrefersForMigration(candidate, existing models.PlaybackProgress) bool {
	if candidate.UpdatedAt.After(existing.UpdatedAt) {
		return true
	}
	if existing.UpdatedAt.After(candidate.UpdatedAt) {
		return false
	}
	return candidate.PercentWatched > existing.PercentWatched
}

func watchedHistoryItemsByUserForMigration(all map[string]map[string]models.WatchHistoryItem) map[string][]models.WatchHistoryItem {
	out := make(map[string][]models.WatchHistoryItem, len(all))
	for userID, perUser := range all {
		for _, item := range perUser {
			if !item.Watched {
				continue
			}
			out[userID] = append(out[userID], item)
		}
	}
	return out
}

func isPlaybackProgressClearedByWatchedForMigration(progress models.PlaybackProgress, watchedItems []models.WatchHistoryItem) bool {
	if progress.HiddenFromContinueWatching {
		return false
	}
	progressIdentity := playbackProgressIdentityForMigration(progress)
	for _, watched := range watchedItems {
		if watched.MediaType != progress.MediaType {
			continue
		}
		watchedIdentity := watchHistoryIdentityForMigration(watched)
		if mediaidentity.Equivalent(progressIdentity, watchedIdentity) {
			return true
		}
		if progress.MediaType == "episode" &&
			progress.SeasonNumber > 0 &&
			progress.EpisodeNumber > 0 &&
			watched.SeasonNumber > 0 &&
			watched.EpisodeNumber > 0 &&
			watchHistoryProgressSameSeriesForMigration(watched, progress) &&
			compareEpisodeOrderForMigration(progress.SeasonNumber, progress.EpisodeNumber, watched.SeasonNumber, watched.EpisodeNumber) <= 0 {
			return true
		}
	}
	return false
}

func watchHistoryProgressSameSeriesForMigration(watched models.WatchHistoryItem, progress models.PlaybackProgress) bool {
	if watched.SeriesID != "" && progress.SeriesID != "" && strings.EqualFold(watched.SeriesID, progress.SeriesID) {
		return true
	}
	watchedSeries := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   "series",
		ID:          watched.SeriesID,
		ExternalIDs: mediaidentity.IdentityExternalIDs("episode", watched.ItemID, watched.SeriesID, watched.ExternalIDs),
	})
	progressSeries := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   "series",
		ID:          progress.SeriesID,
		ExternalIDs: mediaidentity.IdentityExternalIDs("episode", progress.ItemID, progress.SeriesID, progress.ExternalIDs),
	})
	return mediaidentity.Equivalent(watchedSeries, progressSeries)
}

func compareEpisodeOrderForMigration(seasonA, episodeA, seasonB, episodeB int) int {
	if seasonA != seasonB {
		return seasonA - seasonB
	}
	return episodeA - episodeB
}

func rewriteWatchlistIdentityItems(ctx context.Context, tx *Tx, existingAll map[string][]models.WatchlistItem, reconciled map[string]map[string]models.WatchlistItem) error {
	dbKeys := make(map[string]map[string]bool, len(existingAll))
	for userID, items := range existingAll {
		keys := make(map[string]bool, len(items))
		for _, item := range items {
			keys[item.Key()] = true
		}
		dbKeys[userID] = keys
	}
	for userID, perUser := range reconciled {
		items := make([]models.WatchlistItem, 0, len(perUser))
		for _, item := range perUser {
			items = append(items, item)
		}
		if err := tx.Watchlist().BulkUpsert(ctx, userID, items); err != nil {
			return fmt.Errorf("upsert watchlist %s: %w", userID, err)
		}
		for key := range dbKeys[userID] {
			if _, keep := perUser[key]; !keep {
				if err := tx.Watchlist().Delete(ctx, userID, key); err != nil {
					return fmt.Errorf("delete watchlist %s %s: %w", userID, key, err)
				}
			}
		}
		delete(dbKeys, userID)
	}
	for userID := range dbKeys {
		if err := tx.Watchlist().DeleteByUser(ctx, userID); err != nil {
			return fmt.Errorf("delete watchlist user %s: %w", userID, err)
		}
	}
	return nil
}

func rewriteWatchHistoryIdentityItems(ctx context.Context, tx *Tx, existingAll map[string][]models.WatchHistoryItem, reconciled map[string]map[string]models.WatchHistoryItem) error {
	dbKeys := make(map[string]map[string]bool, len(existingAll))
	for userID, items := range existingAll {
		keys := make(map[string]bool, len(items))
		for _, item := range items {
			keys[item.ID] = true
		}
		dbKeys[userID] = keys
	}
	for userID, perUser := range reconciled {
		items := make([]models.WatchHistoryItem, 0, len(perUser))
		for _, item := range perUser {
			items = append(items, item)
		}
		if err := tx.WatchHistory().BulkUpsert(ctx, userID, items); err != nil {
			return fmt.Errorf("upsert watch history %s: %w", userID, err)
		}
		for key := range dbKeys[userID] {
			if _, keep := perUser[key]; !keep {
				if err := tx.WatchHistory().Delete(ctx, userID, key); err != nil {
					return fmt.Errorf("delete watch history %s %s: %w", userID, key, err)
				}
			}
		}
		delete(dbKeys, userID)
	}
	for userID := range dbKeys {
		if err := tx.WatchHistory().DeleteByUser(ctx, userID); err != nil {
			return fmt.Errorf("delete watch history user %s: %w", userID, err)
		}
	}
	return nil
}

func rewritePlaybackProgressIdentityItems(ctx context.Context, tx *Tx, existingAll map[string][]models.PlaybackProgress, reconciled map[string]map[string]models.PlaybackProgress) error {
	dbKeys := make(map[string]map[string]bool, len(existingAll))
	for userID, items := range existingAll {
		keys := make(map[string]bool, len(items))
		for _, item := range items {
			keys[item.ID] = true
		}
		dbKeys[userID] = keys
	}
	for userID, perUser := range reconciled {
		items := make([]models.PlaybackProgress, 0, len(perUser))
		for _, item := range perUser {
			items = append(items, item)
		}
		if err := tx.PlaybackProgress().BulkUpsert(ctx, userID, items); err != nil {
			return fmt.Errorf("upsert playback progress %s: %w", userID, err)
		}
		for key := range dbKeys[userID] {
			if _, keep := perUser[key]; !keep {
				if err := tx.PlaybackProgress().Delete(ctx, userID, key); err != nil {
					return fmt.Errorf("delete playback progress %s %s: %w", userID, key, err)
				}
			}
		}
		delete(dbKeys, userID)
	}
	for userID := range dbKeys {
		if err := tx.PlaybackProgress().DeleteByUser(ctx, userID); err != nil {
			return fmt.Errorf("delete playback progress user %s: %w", userID, err)
		}
	}
	return nil
}
