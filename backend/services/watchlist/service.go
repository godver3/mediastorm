package watchlist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"novastream/internal/datastore"
	"novastream/internal/mediaidentity"
	"novastream/models"
)

var (
	ErrStorageDirRequired = errors.New("storage directory not provided")
	ErrUserIDRequired     = errors.New("user id is required")
	ErrIDRequired         = errors.New("id is required")
	ErrMediaTypeRequired  = errors.New("media type is required")
	ErrIdentifierRequired = errors.New("id and media type are required")
	ErrTombstoned         = errors.New("watchlist item was explicitly removed")
)

// Service manages persistence and retrieval of user watchlist items.
type Service struct {
	mu             sync.RWMutex
	path           string
	tombstonesPath string
	store          *datastore.DataStore
	items          map[string]map[string]models.WatchlistItem
	tombstones     map[string]map[string]models.WatchlistTombstone
}

// useDB returns true when the service is backed by PostgreSQL.
func (s *Service) useDB() bool { return s.store != nil }

// NewServiceWithStore creates a watchlist service backed by PostgreSQL.
func NewServiceWithStore(store *datastore.DataStore) (*Service, error) {
	svc := &Service{
		store:      store,
		items:      make(map[string]map[string]models.WatchlistItem),
		tombstones: make(map[string]map[string]models.WatchlistTombstone),
	}
	if err := svc.load(); err != nil {
		return nil, err
	}
	return svc, nil
}

// NewService creates a watchlist service storing data inside the provided directory.
func NewService(storageDir string) (*Service, error) {
	if strings.TrimSpace(storageDir) == "" {
		return nil, ErrStorageDirRequired
	}

	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		return nil, fmt.Errorf("create watchlist dir: %w", err)
	}

	svc := &Service{
		path:           filepath.Join(storageDir, "watchlist.json"),
		tombstonesPath: filepath.Join(storageDir, "watchlist_tombstones.json"),
		items:          make(map[string]map[string]models.WatchlistItem),
		tombstones:     make(map[string]map[string]models.WatchlistTombstone),
	}

	if err := svc.load(); err != nil {
		return nil, err
	}

	return svc, nil
}

// List returns all watchlist items sorted by most recent additions first.
func (s *Service) List(userID string) ([]models.WatchlistItem, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUserIDRequired
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]models.WatchlistItem, 0)
	if perUser, ok := s.items[userID]; ok {
		items = make([]models.WatchlistItem, 0, len(perUser))
		for _, item := range perUser {
			items = append(items, item)
		}
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].AddedAt.Equal(items[j].AddedAt) {
			return items[i].Key() < items[j].Key()
		}
		return items[i].AddedAt.After(items[j].AddedAt)
	})

	return items, nil
}

// ListBySyncSource returns all watchlist items that were synced from a specific source.
func (s *Service) ListBySyncSource(userID, syncSource string) ([]models.WatchlistItem, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUserIDRequired
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]models.WatchlistItem, 0)
	if perUser, ok := s.items[userID]; ok {
		for _, item := range perUser {
			if item.SyncSource == syncSource {
				items = append(items, item)
			}
		}
	}

	return items, nil
}

// AddOrUpdate inserts a new item or updates metadata for an existing one.
func (s *Service) AddOrUpdate(userID string, input models.WatchlistUpsert) (models.WatchlistItem, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return models.WatchlistItem{}, ErrUserIDRequired
	}

	if strings.TrimSpace(input.ID) == "" {
		return models.WatchlistItem{}, ErrIDRequired
	}
	if strings.TrimSpace(input.MediaType) == "" {
		return models.WatchlistItem{}, ErrMediaTypeRequired
	}

	mediaType := mediaidentity.NormalizeMediaType(input.MediaType)
	input.MediaType = mediaType
	input.ExternalIDs = normaliseExternalIDs(input.ExternalIDs)
	input.ID = canonicalWatchlistID(mediaType, input.ID, input.ExternalIDs)
	s.mu.Lock()
	defer s.mu.Unlock()

	perUser := s.ensureUserLocked(userID)
	item, exists := s.takeMergedItemLocked(perUser, mediaType, input.ID, input.ExternalIDs)

	if !exists {
		if strings.TrimSpace(input.SyncSource) != "" && s.isTombstonedLocked(userID, mediaType, input.ID, input.ExternalIDs) {
			return models.WatchlistItem{}, ErrTombstoned
		}
		item = models.WatchlistItem{
			ID:        input.ID,
			MediaType: mediaType,
			AddedAt:   time.Now().UTC(),
		}
	}
	if strings.TrimSpace(input.SyncSource) == "" {
		s.clearTombstonesLocked(userID, mediaType, input.ID, input.ExternalIDs)
	}

	item.MediaType = mediaType

	if strings.TrimSpace(input.Name) != "" {
		item.Name = input.Name
	}
	if input.Overview != "" {
		item.Overview = input.Overview
	}
	if input.Year != 0 {
		item.Year = input.Year
	}
	if strings.TrimSpace(input.PosterURL) != "" {
		item.PosterURL = input.PosterURL
	}
	if strings.TrimSpace(input.TextPosterURL) != "" {
		item.TextPosterURL = input.TextPosterURL
	}
	if strings.TrimSpace(input.BackdropURL) != "" {
		item.BackdropURL = input.BackdropURL
	}
	if input.ExternalIDs != nil {
		if len(input.ExternalIDs) == 0 {
			item.ExternalIDs = nil
		} else {
			copyIDs := make(map[string]string, len(input.ExternalIDs))
			for k, v := range input.ExternalIDs {
				copyIDs[k] = v
			}
			item.ExternalIDs = mergeStringMaps(item.ExternalIDs, copyIDs)
		}
	}
	if len(input.Genres) > 0 {
		item.Genres = append([]string{}, input.Genres...)
	}
	if input.RuntimeMinutes != 0 {
		item.RuntimeMinutes = input.RuntimeMinutes
	}

	// Update sync tracking fields if provided
	if strings.TrimSpace(input.SyncSource) != "" {
		item.SyncSource = input.SyncSource
	}
	if input.SyncedAt != nil {
		item.SyncedAt = input.SyncedAt
	}
	item.ID = canonicalWatchlistID(mediaType, input.ID, item.ExternalIDs)
	key := item.Key()

	perUser[key] = item

	if err := s.saveLocked(); err != nil {
		return models.WatchlistItem{}, err
	}

	return item, nil
}

// UpdateState is deprecated - watch status is now tracked separately via the history service.
// This method is kept for backwards compatibility but does nothing.
func (s *Service) UpdateState(userID, mediaType, id string, watched *bool, progress interface{}) (models.WatchlistItem, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return models.WatchlistItem{}, ErrUserIDRequired
	}

	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "" || strings.TrimSpace(id) == "" {
		return models.WatchlistItem{}, ErrIdentifierRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	perUser := s.ensureUserLocked(userID)

	var item models.WatchlistItem
	exists := false
	for _, key := range watchlistCandidateKeys(mediaType, id, nil) {
		if existing, ok := perUser[key]; ok {
			item = existing
			exists = true
			break
		}
	}
	if !exists {
		for _, existing := range perUser {
			if !watchlistItemMatchesIdentifier(existing, mediaType, id) {
				continue
			}
			item = existing
			exists = true
			break
		}
	}
	if !exists {
		return models.WatchlistItem{}, os.ErrNotExist
	}

	// Watch status is now tracked separately - this method does nothing but return the item
	return item, nil
}

// Remove deletes an item from the watchlist.
func (s *Service) Remove(userID, mediaType, id string) (bool, error) {
	return s.remove(userID, mediaType, id, true)
}

// RemoveSynced deletes an item as part of sync reconciliation without creating
// a user-removal tombstone.
func (s *Service) RemoveSynced(userID, mediaType, id string) (bool, error) {
	return s.remove(userID, mediaType, id, false)
}

func (s *Service) remove(userID, mediaType, id string, tombstone bool) (bool, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false, ErrUserIDRequired
	}

	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "" || strings.TrimSpace(id) == "" {
		return false, ErrIdentifierRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	perUser := s.ensureUserLocked(userID)

	removed := false
	var removedItem models.WatchlistItem
	for _, key := range watchlistCandidateKeys(mediaType, id, nil) {
		if existing, exists := perUser[key]; exists {
			removedItem = mergeWatchlistItems(removedItem, existing)
			delete(perUser, key)
			removed = true
		}
	}
	for key, existing := range perUser {
		if !watchlistItemMatchesIdentifier(existing, mediaType, id) {
			continue
		}
		removedItem = mergeWatchlistItems(removedItem, existing)
		delete(perUser, key)
		removed = true
	}
	if !removed {
		return false, nil
	}
	if tombstone {
		s.upsertTombstoneLocked(userID, removedItem)
	}

	if err := s.saveLocked(); err != nil {
		return false, err
	}

	return true, nil
}

func (s *Service) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.useDB() {
		allItems, err := s.store.Watchlist().ListAll(context.Background())
		if err != nil {
			return fmt.Errorf("load watchlist from db: %w", err)
		}
		allTombstones, err := s.store.Watchlist().ListTombstonesAll(context.Background())
		if err != nil {
			return fmt.Errorf("load watchlist tombstones from db: %w", err)
		}
		s.items = make(map[string]map[string]models.WatchlistItem, len(allItems))
		for userID, items := range allItems {
			perUser := make(map[string]models.WatchlistItem, len(items))
			for _, item := range items {
				perUser[item.Key()] = item
			}
			s.items[userID] = perUser
		}
		s.tombstones = make(map[string]map[string]models.WatchlistTombstone, len(allTombstones))
		for userID, tombstones := range allTombstones {
			perUser := make(map[string]models.WatchlistTombstone, len(tombstones))
			for _, tombstone := range tombstones {
				normalised := normaliseTombstone(tombstone)
				perUser[normalised.Key()] = normalised
			}
			s.tombstones[userID] = perUser
		}
		return nil
	}

	s.tombstones = make(map[string]map[string]models.WatchlistTombstone)
	if err := s.loadTombstonesLocked(); err != nil {
		return err
	}

	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.items = make(map[string]map[string]models.WatchlistItem)
		return nil
	}
	if err != nil {
		return fmt.Errorf("open watchlist: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read watchlist: %w", err)
	}
	if len(data) == 0 {
		s.items = make(map[string]map[string]models.WatchlistItem)
		return nil
	}

	var multi map[string][]models.WatchlistItem
	if err := json.Unmarshal(data, &multi); err == nil {
		s.items = make(map[string]map[string]models.WatchlistItem, len(multi))
		for userID, items := range multi {
			userID = strings.TrimSpace(userID)
			if userID == "" {
				continue
			}
			perUser := make(map[string]models.WatchlistItem, len(items))
			for _, item := range items {
				normalised := normaliseItem(item)
				perUser[normalised.Key()] = normalised
			}
			s.items[userID] = perUser
		}
		return nil
	}

	var legacy []models.WatchlistItem
	if err := json.Unmarshal(data, &legacy); err != nil {
		return fmt.Errorf("decode watchlist: %w", err)
	}

	perUser := make(map[string]models.WatchlistItem, len(legacy))
	for _, item := range legacy {
		normalised := normaliseItem(item)
		perUser[normalised.Key()] = normalised
	}

	s.items = map[string]map[string]models.WatchlistItem{
		models.DefaultUserID: perUser,
	}

	return nil
}

func (s *Service) loadTombstonesLocked() error {
	if strings.TrimSpace(s.tombstonesPath) == "" {
		return nil
	}

	file, err := os.Open(s.tombstonesPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open watchlist tombstones: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read watchlist tombstones: %w", err)
	}
	if len(data) == 0 {
		return nil
	}

	var multi map[string][]models.WatchlistTombstone
	if err := json.Unmarshal(data, &multi); err != nil {
		return fmt.Errorf("decode watchlist tombstones: %w", err)
	}
	for userID, tombstones := range multi {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			continue
		}
		perUser := make(map[string]models.WatchlistTombstone, len(tombstones))
		for _, tombstone := range tombstones {
			normalised := normaliseTombstone(tombstone)
			perUser[normalised.Key()] = normalised
		}
		s.tombstones[userID] = perUser
	}
	return nil
}

func (s *Service) saveLocked() error {
	if s.useDB() {
		return s.syncToDB()
	}

	byUser := make(map[string][]models.WatchlistItem, len(s.items))
	for userID, collection := range s.items {
		items := make([]models.WatchlistItem, 0, len(collection))
		for _, item := range collection {
			items = append(items, item)
		}

		sort.Slice(items, func(i, j int) bool {
			if items[i].AddedAt.Equal(items[j].AddedAt) {
				return items[i].Key() < items[j].Key()
			}
			return items[i].AddedAt.Before(items[j].AddedAt)
		})

		byUser[userID] = items
	}

	tmp := s.path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create watchlist temp file: %w", err)
	}

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(byUser); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("encode watchlist: %w", err)
	}

	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync watchlist: %w", err)
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close watchlist temp file: %w", err)
	}

	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace watchlist file: %w", err)
	}

	return s.saveTombstonesLocked()
}

func (s *Service) saveTombstonesLocked() error {
	if strings.TrimSpace(s.tombstonesPath) == "" {
		return nil
	}

	byUser := make(map[string][]models.WatchlistTombstone, len(s.tombstones))
	for userID, collection := range s.tombstones {
		tombstones := make([]models.WatchlistTombstone, 0, len(collection))
		for _, tombstone := range collection {
			tombstones = append(tombstones, tombstone)
		}

		sort.Slice(tombstones, func(i, j int) bool {
			if tombstones[i].RemovedAt.Equal(tombstones[j].RemovedAt) {
				return tombstones[i].Key() < tombstones[j].Key()
			}
			return tombstones[i].RemovedAt.Before(tombstones[j].RemovedAt)
		})

		byUser[userID] = tombstones
	}

	tmp := s.tombstonesPath + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create watchlist tombstones temp file: %w", err)
	}

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(byUser); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("encode watchlist tombstones: %w", err)
	}

	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync watchlist tombstones: %w", err)
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close watchlist tombstones temp file: %w", err)
	}

	if err := os.Rename(tmp, s.tombstonesPath); err != nil {
		return fmt.Errorf("replace watchlist tombstones file: %w", err)
	}

	return nil
}

// syncToDB writes the full in-memory watchlist state to PostgreSQL.
func (s *Service) syncToDB() error {
	ctx := context.Background()
	return s.store.WithTx(ctx, func(tx *datastore.Tx) error {
		// Get existing DB state to detect deletes
		existingAll, err := tx.Watchlist().ListAll(ctx)
		if err != nil {
			return err
		}

		// Build set of existing DB keys per user
		dbKeys := make(map[string]map[string]bool, len(existingAll))
		for userID, items := range existingAll {
			keys := make(map[string]bool, len(items))
			for _, item := range items {
				keys[item.Key()] = true
			}
			dbKeys[userID] = keys
		}

		// Upsert all in-memory items
		for userID, perUser := range s.items {
			items := make([]models.WatchlistItem, 0, len(perUser))
			for _, item := range perUser {
				items = append(items, item)
			}
			if err := tx.Watchlist().BulkUpsert(ctx, userID, items); err != nil {
				return err
			}

			// Remove keys that exist in DB but not in memory for this user
			if existing, ok := dbKeys[userID]; ok {
				for key := range existing {
					if _, inMem := perUser[key]; !inMem {
						if err := tx.Watchlist().Delete(ctx, userID, key); err != nil {
							return err
						}
					}
				}
			}
			delete(dbKeys, userID)
		}

		// Delete entire users that exist in DB but not in memory
		for userID := range dbKeys {
			if err := tx.Watchlist().DeleteByUser(ctx, userID); err != nil {
				return err
			}
		}

		existingTombstones, err := tx.Watchlist().ListTombstonesAll(ctx)
		if err != nil {
			return err
		}
		dbTombstoneKeys := make(map[string]map[string]bool, len(existingTombstones))
		for userID, tombstones := range existingTombstones {
			keys := make(map[string]bool, len(tombstones))
			for _, tombstone := range tombstones {
				keys[tombstone.Key()] = true
			}
			dbTombstoneKeys[userID] = keys
		}
		for userID, perUser := range s.tombstones {
			tombstones := make([]models.WatchlistTombstone, 0, len(perUser))
			for _, tombstone := range perUser {
				tombstones = append(tombstones, tombstone)
			}
			if err := tx.Watchlist().BulkUpsertTombstones(ctx, userID, tombstones); err != nil {
				return err
			}
			if existing, ok := dbTombstoneKeys[userID]; ok {
				for key := range existing {
					if _, inMem := perUser[key]; !inMem {
						if err := tx.Watchlist().DeleteTombstone(ctx, userID, key); err != nil {
							return err
						}
					}
				}
			}
			delete(dbTombstoneKeys, userID)
		}
		for userID := range dbTombstoneKeys {
			if err := tx.Watchlist().DeleteTombstonesByUser(ctx, userID); err != nil {
				return err
			}
		}

		return nil
	})
}

func (s *Service) ensureUserLocked(userID string) map[string]models.WatchlistItem {
	perUser, ok := s.items[userID]
	if !ok {
		perUser = make(map[string]models.WatchlistItem)
		s.items[userID] = perUser
	}
	return perUser
}

func (s *Service) ensureTombstonesLocked(userID string) map[string]models.WatchlistTombstone {
	perUser, ok := s.tombstones[userID]
	if !ok {
		perUser = make(map[string]models.WatchlistTombstone)
		s.tombstones[userID] = perUser
	}
	return perUser
}

func (s *Service) upsertTombstoneLocked(userID string, item models.WatchlistItem) {
	tombstone := normaliseTombstone(models.WatchlistTombstone{
		ID:          item.ID,
		MediaType:   item.MediaType,
		Name:        item.Name,
		Year:        item.Year,
		ExternalIDs: item.ExternalIDs,
		RemovedAt:   time.Now().UTC(),
	})
	perUser := s.ensureTombstonesLocked(userID)
	if existing, found := takeMergedTombstone(perUser, tombstone.MediaType, tombstone.ID, tombstone.ExternalIDs); found {
		tombstone = mergeTombstones(existing, tombstone)
	}
	perUser[tombstone.Key()] = tombstone
}

func (s *Service) isTombstonedLocked(userID, mediaType, canonicalID string, externalIDs map[string]string) bool {
	perUser, ok := s.tombstones[userID]
	if !ok {
		return false
	}
	for _, key := range watchlistCandidateKeys(mediaType, canonicalID, externalIDs) {
		if _, exists := perUser[key]; exists {
			return true
		}
	}
	for _, tombstone := range perUser {
		if tombstoneMatchesIdentifier(tombstone, mediaType, canonicalID, externalIDs) {
			return true
		}
	}
	return false
}

func (s *Service) clearTombstonesLocked(userID, mediaType, canonicalID string, externalIDs map[string]string) {
	perUser, ok := s.tombstones[userID]
	if !ok {
		return
	}
	for _, key := range watchlistCandidateKeys(mediaType, canonicalID, externalIDs) {
		delete(perUser, key)
	}
	for key, tombstone := range perUser {
		if tombstoneMatchesIdentifier(tombstone, mediaType, canonicalID, externalIDs) {
			delete(perUser, key)
		}
	}
	if len(perUser) == 0 {
		delete(s.tombstones, userID)
	}
}

func normaliseItem(item models.WatchlistItem) models.WatchlistItem {
	identity := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   item.MediaType,
		ID:          item.ID,
		ExternalIDs: item.ExternalIDs,
	})
	item.MediaType = identity.MediaType
	item.ExternalIDs = identity.ExternalIDs
	item.ID = identity.ID
	if item.AddedAt.IsZero() {
		item.AddedAt = time.Now().UTC()
	}
	return item
}

func normaliseTombstone(tombstone models.WatchlistTombstone) models.WatchlistTombstone {
	identity := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   tombstone.MediaType,
		ID:          tombstone.ID,
		ExternalIDs: tombstone.ExternalIDs,
	})
	tombstone.MediaType = identity.MediaType
	tombstone.ExternalIDs = identity.ExternalIDs
	tombstone.ID = identity.ID
	if tombstone.RemovedAt.IsZero() {
		tombstone.RemovedAt = time.Now().UTC()
	}
	return tombstone
}

// Reconcile normalizes watchlist identities, merges equivalent variants, and
// persists the canonicalized state. It is intended for one-off cleanup and safe
// to call repeatedly.
func (s *Service) Reconcile() error {
	if err := s.load(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	reconciled := make(map[string]map[string]models.WatchlistItem, len(s.items))
	for userID, items := range s.items {
		perUser := make(map[string]models.WatchlistItem, len(items))
		for _, item := range items {
			normalised := normaliseItem(item)
			merged, found := s.takeMergedItemLocked(perUser, normalised.MediaType, normalised.ID, normalised.ExternalIDs)
			if found {
				normalised = mergeWatchlistItems(merged, normalised)
			}
			perUser[normalised.Key()] = normalised
		}
		reconciled[userID] = perUser
	}
	s.items = reconciled
	reconciledTombstones := make(map[string]map[string]models.WatchlistTombstone, len(s.tombstones))
	for userID, tombstones := range s.tombstones {
		perUser := make(map[string]models.WatchlistTombstone, len(tombstones))
		for _, tombstone := range tombstones {
			normalised := normaliseTombstone(tombstone)
			merged, found := takeMergedTombstone(perUser, normalised.MediaType, normalised.ID, normalised.ExternalIDs)
			if found {
				normalised = mergeTombstones(merged, normalised)
			}
			perUser[normalised.Key()] = normalised
		}
		reconciledTombstones[userID] = perUser
	}
	s.tombstones = reconciledTombstones
	return s.saveLocked()
}

func (s *Service) takeMergedItemLocked(perUser map[string]models.WatchlistItem, mediaType, canonicalID string, externalIDs map[string]string) (models.WatchlistItem, bool) {
	var merged models.WatchlistItem
	found := false
	for _, key := range watchlistCandidateKeys(mediaType, canonicalID, externalIDs) {
		existing, ok := perUser[key]
		if !ok {
			continue
		}
		if !found {
			merged = existing
			found = true
		} else {
			merged = mergeWatchlistItems(merged, existing)
		}
		delete(perUser, key)
	}
	for key, existing := range perUser {
		if !watchlistItemsEquivalent(mediaType, canonicalID, externalIDs, existing) {
			continue
		}
		if !found {
			merged = existing
			found = true
		} else {
			merged = mergeWatchlistItems(merged, existing)
		}
		delete(perUser, key)
	}
	return merged, found
}

func mergeWatchlistItems(base, incoming models.WatchlistItem) models.WatchlistItem {
	base.ExternalIDs = normaliseExternalIDs(base.ExternalIDs)
	incoming.ExternalIDs = normaliseExternalIDs(incoming.ExternalIDs)
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
	base.ExternalIDs = mergeStringMaps(base.ExternalIDs, incoming.ExternalIDs)
	if len(base.Genres) == 0 && len(incoming.Genres) > 0 {
		base.Genres = append([]string{}, incoming.Genres...)
	}
	base.ID = canonicalWatchlistID(base.MediaType, base.ID, base.ExternalIDs)
	return base
}

func takeMergedTombstone(perUser map[string]models.WatchlistTombstone, mediaType, canonicalID string, externalIDs map[string]string) (models.WatchlistTombstone, bool) {
	var merged models.WatchlistTombstone
	found := false
	for _, key := range watchlistCandidateKeys(mediaType, canonicalID, externalIDs) {
		existing, ok := perUser[key]
		if !ok {
			continue
		}
		if !found {
			merged = existing
			found = true
		} else {
			merged = mergeTombstones(merged, existing)
		}
		delete(perUser, key)
	}
	for key, existing := range perUser {
		if !tombstoneMatchesIdentifier(existing, mediaType, canonicalID, externalIDs) {
			continue
		}
		if !found {
			merged = existing
			found = true
		} else {
			merged = mergeTombstones(merged, existing)
		}
		delete(perUser, key)
	}
	return merged, found
}

func mergeTombstones(base, incoming models.WatchlistTombstone) models.WatchlistTombstone {
	base.ExternalIDs = normaliseExternalIDs(base.ExternalIDs)
	incoming.ExternalIDs = normaliseExternalIDs(incoming.ExternalIDs)
	if base.ID == "" {
		base.ID = incoming.ID
	}
	if base.MediaType == "" {
		base.MediaType = incoming.MediaType
	}
	if base.Name == "" {
		base.Name = incoming.Name
	}
	if base.Year == 0 {
		base.Year = incoming.Year
	}
	if base.RemovedAt.IsZero() || (!incoming.RemovedAt.IsZero() && incoming.RemovedAt.After(base.RemovedAt)) {
		base.RemovedAt = incoming.RemovedAt
	}
	base.ExternalIDs = mergeStringMaps(base.ExternalIDs, incoming.ExternalIDs)
	base.ID = canonicalWatchlistID(base.MediaType, base.ID, base.ExternalIDs)
	return base
}

func mergeStringMaps(base, incoming map[string]string) map[string]string {
	return mediaidentity.MergeExternalIDs(base, incoming)
}

func normaliseExternalIDs(ids map[string]string) map[string]string {
	return mediaidentity.NormalizeExternalIDs(ids)
}

func canonicalWatchlistID(mediaType, id string, externalIDs map[string]string) string {
	return mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   mediaType,
		ID:          id,
		ExternalIDs: externalIDs,
	}).ID
}

func watchlistCandidateKeys(mediaType, canonicalID string, externalIDs map[string]string) []string {
	return mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   mediaType,
		ID:          canonicalID,
		ExternalIDs: externalIDs,
	}).CandidateKeys
}

func watchlistItemsEquivalent(mediaType, canonicalID string, externalIDs map[string]string, existing models.WatchlistItem) bool {
	incoming := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   mediaType,
		ID:          canonicalID,
		ExternalIDs: externalIDs,
	})
	current := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   existing.MediaType,
		ID:          existing.ID,
		ExternalIDs: existing.ExternalIDs,
	})
	return mediaidentity.Equivalent(incoming, current)
}

func tombstoneMatchesIdentifier(existing models.WatchlistTombstone, mediaType, canonicalID string, externalIDs map[string]string) bool {
	incoming := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   mediaType,
		ID:          canonicalID,
		ExternalIDs: externalIDs,
	})
	current := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   existing.MediaType,
		ID:          existing.ID,
		ExternalIDs: existing.ExternalIDs,
	})
	return mediaidentity.Equivalent(incoming, current)
}

func watchlistItemMatchesIdentifier(existing models.WatchlistItem, mediaType, id string) bool {
	targetKey := mediaidentity.Key(mediaType, id)
	if targetKey == "" {
		return false
	}
	current := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   existing.MediaType,
		ID:          existing.ID,
		ExternalIDs: existing.ExternalIDs,
	})
	for _, key := range current.CandidateKeys {
		if key == targetKey {
			return true
		}
	}
	return false
}

func watchlistIdentityTokens(mediaType, id string, externalIDs map[string]string) map[string]bool {
	identity := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   mediaType,
		ID:          id,
		ExternalIDs: externalIDs,
	})
	tokens := make(map[string]bool, len(identity.Tokens))
	for token := range identity.Tokens {
		tokens[token] = true
	}
	return tokens
}
