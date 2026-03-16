package customlists

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"novastream/models"
)

var (
	ErrStorageDirRequired = errors.New("storage directory not provided")
	ErrUserIDRequired     = errors.New("user id is required")
	ErrListNameRequired   = errors.New("list name is required")
	ErrListIDRequired     = errors.New("list id is required")
	ErrIDRequired         = errors.New("id is required")
	ErrMediaTypeRequired  = errors.New("media type is required")
	ErrIdentifierRequired = errors.New("id and media type are required")
)

type persistedUser struct {
	Lists []models.CustomList               `json:"lists"`
	Items map[string][]models.WatchlistItem `json:"items,omitempty"` // listID -> items
}

type userCollection struct {
	lists map[string]models.CustomList
	items map[string]map[string]models.WatchlistItem // listID -> (mediaType:id -> item)
}

// Service manages persistence and retrieval of user custom lists.
type Service struct {
	mu    sync.RWMutex
	path  string
	store *datastore.DataStore
	data  map[string]*userCollection
}

// useDB returns true when the service is backed by PostgreSQL.
func (s *Service) useDB() bool { return s.store != nil }

// NewServiceWithStore creates a custom lists service backed by PostgreSQL.
func NewServiceWithStore(store *datastore.DataStore) (*Service, error) {
	svc := &Service{
		store: store,
		data:  make(map[string]*userCollection),
	}
	if err := svc.load(); err != nil {
		return nil, err
	}
	return svc, nil
}

func NewService(storageDir string) (*Service, error) {
	if strings.TrimSpace(storageDir) == "" {
		return nil, ErrStorageDirRequired
	}

	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		return nil, fmt.Errorf("create custom lists dir: %w", err)
	}

	svc := &Service{
		path: filepath.Join(storageDir, "custom_lists.json"),
		data: make(map[string]*userCollection),
	}
	if err := svc.load(); err != nil {
		return nil, err
	}
	return svc, nil
}

func (s *Service) ListLists(userID string) ([]models.CustomList, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUserIDRequired
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	user := s.data[userID]
	if user == nil {
		return []models.CustomList{}, nil
	}

	lists := make([]models.CustomList, 0, len(user.lists))
	for _, list := range user.lists {
		list.ItemCount = len(user.items[list.ID])
		lists = append(lists, list)
	}

	sort.Slice(lists, func(i, j int) bool {
		if lists[i].UpdatedAt.Equal(lists[j].UpdatedAt) {
			return lists[i].ID < lists[j].ID
		}
		return lists[i].UpdatedAt.After(lists[j].UpdatedAt)
	})

	return lists, nil
}

func (s *Service) CreateList(userID, name string) (models.CustomList, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return models.CustomList{}, ErrUserIDRequired
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return models.CustomList{}, ErrListNameRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	user := s.ensureUserLocked(userID)
	now := time.Now().UTC()
	list := models.CustomList{
		ID:        generateListID(),
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
	}

	user.lists[list.ID] = list
	user.items[list.ID] = make(map[string]models.WatchlistItem)

	if err := s.saveLocked(); err != nil {
		return models.CustomList{}, err
	}

	return list, nil
}

func (s *Service) RenameList(userID, listID, name string) (models.CustomList, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return models.CustomList{}, ErrUserIDRequired
	}
	listID = strings.TrimSpace(listID)
	if listID == "" {
		return models.CustomList{}, ErrListIDRequired
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return models.CustomList{}, ErrListNameRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	user := s.ensureUserLocked(userID)
	list, ok := user.lists[listID]
	if !ok {
		return models.CustomList{}, os.ErrNotExist
	}

	list.Name = name
	list.UpdatedAt = time.Now().UTC()
	user.lists[listID] = list

	if err := s.saveLocked(); err != nil {
		return models.CustomList{}, err
	}

	list.ItemCount = len(user.items[listID])
	return list, nil
}

func (s *Service) DeleteList(userID, listID string) (bool, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false, ErrUserIDRequired
	}
	listID = strings.TrimSpace(listID)
	if listID == "" {
		return false, ErrListIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	user := s.ensureUserLocked(userID)
	if _, ok := user.lists[listID]; !ok {
		return false, nil
	}

	delete(user.lists, listID)
	delete(user.items, listID)

	if err := s.saveLocked(); err != nil {
		return false, err
	}

	return true, nil
}

func (s *Service) ListItems(userID, listID string) ([]models.WatchlistItem, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUserIDRequired
	}
	listID = strings.TrimSpace(listID)
	if listID == "" {
		return nil, ErrListIDRequired
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	user := s.data[userID]
	if user == nil {
		return nil, os.ErrNotExist
	}
	if _, ok := user.lists[listID]; !ok {
		return nil, os.ErrNotExist
	}

	byList := user.items[listID]
	items := make([]models.WatchlistItem, 0, len(byList))
	for _, item := range byList {
		items = append(items, item)
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].AddedAt.Equal(items[j].AddedAt) {
			return items[i].Key() < items[j].Key()
		}
		return items[i].AddedAt.After(items[j].AddedAt)
	})

	return items, nil
}

func (s *Service) AddItem(userID, listID string, input models.WatchlistUpsert) (models.WatchlistItem, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return models.WatchlistItem{}, ErrUserIDRequired
	}
	listID = strings.TrimSpace(listID)
	if listID == "" {
		return models.WatchlistItem{}, ErrListIDRequired
	}
	if strings.TrimSpace(input.ID) == "" {
		return models.WatchlistItem{}, ErrIDRequired
	}
	if strings.TrimSpace(input.MediaType) == "" {
		return models.WatchlistItem{}, ErrMediaTypeRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	user := s.ensureUserLocked(userID)
	list, ok := user.lists[listID]
	if !ok {
		return models.WatchlistItem{}, os.ErrNotExist
	}

	byList, ok := user.items[listID]
	if !ok {
		byList = make(map[string]models.WatchlistItem)
		user.items[listID] = byList
	}

	mediaType := strings.ToLower(strings.TrimSpace(input.MediaType))
	key := mediaType + ":" + input.ID
	item, exists := byList[key]
	if !exists {
		item = models.WatchlistItem{
			ID:        input.ID,
			MediaType: mediaType,
			AddedAt:   time.Now().UTC(),
		}
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
			item.ExternalIDs = copyIDs
		}
	}
	if len(input.Genres) > 0 {
		item.Genres = append([]string{}, input.Genres...)
	}
	if input.RuntimeMinutes != 0 {
		item.RuntimeMinutes = input.RuntimeMinutes
	}

	byList[key] = item
	list.UpdatedAt = time.Now().UTC()
	user.lists[listID] = list

	if err := s.saveLocked(); err != nil {
		return models.WatchlistItem{}, err
	}

	return item, nil
}

func (s *Service) RemoveItem(userID, listID, mediaType, id string) (bool, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false, ErrUserIDRequired
	}
	listID = strings.TrimSpace(listID)
	if listID == "" {
		return false, ErrListIDRequired
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "" || strings.TrimSpace(id) == "" {
		return false, ErrIdentifierRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	user := s.ensureUserLocked(userID)
	list, ok := user.lists[listID]
	if !ok {
		return false, os.ErrNotExist
	}

	byList := user.items[listID]
	if byList == nil {
		return false, nil
	}

	key := mediaType + ":" + id
	if _, exists := byList[key]; !exists {
		return false, nil
	}

	delete(byList, key)
	list.UpdatedAt = time.Now().UTC()
	user.lists[listID] = list

	if err := s.saveLocked(); err != nil {
		return false, err
	}

	return true, nil
}

func (s *Service) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.useDB() {
		ctx := context.Background()
		userIDs, err := s.store.CustomLists().ListUserIDs(ctx)
		if err != nil {
			return fmt.Errorf("load custom list user ids from db: %w", err)
		}
		s.data = make(map[string]*userCollection, len(userIDs))
		for _, userID := range userIDs {
			lists, err := s.store.CustomLists().ListByUser(ctx, userID)
			if err != nil {
				return fmt.Errorf("load custom lists for user %s from db: %w", userID, err)
			}
			collection := &userCollection{
				lists: make(map[string]models.CustomList, len(lists)),
				items: make(map[string]map[string]models.WatchlistItem),
			}
			for _, list := range lists {
				list.ItemCount = 0
				collection.lists[list.ID] = list
				items, err := s.store.CustomLists().GetItems(ctx, list.ID)
				if err != nil {
					return fmt.Errorf("load items for list %s from db: %w", list.ID, err)
				}
				byKey := make(map[string]models.WatchlistItem, len(items))
				for _, item := range items {
					byKey[item.Key()] = item
				}
				collection.items[list.ID] = byKey
			}
			s.data[userID] = collection
		}
		return nil
	}

	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.data = make(map[string]*userCollection)
		return nil
	}
	if err != nil {
		return fmt.Errorf("open custom lists: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read custom lists: %w", err)
	}
	if len(data) == 0 {
		s.data = make(map[string]*userCollection)
		return nil
	}

	var persisted map[string]persistedUser
	if err := json.Unmarshal(data, &persisted); err != nil {
		return fmt.Errorf("decode custom lists: %w", err)
	}

	s.data = make(map[string]*userCollection, len(persisted))
	for userID, user := range persisted {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			continue
		}
		collection := &userCollection{
			lists: make(map[string]models.CustomList, len(user.Lists)),
			items: make(map[string]map[string]models.WatchlistItem, len(user.Items)),
		}
		for _, list := range user.Lists {
			if strings.TrimSpace(list.ID) == "" {
				continue
			}
			if list.CreatedAt.IsZero() {
				list.CreatedAt = time.Now().UTC()
			}
			if list.UpdatedAt.IsZero() {
				list.UpdatedAt = list.CreatedAt
			}
			list.ItemCount = 0
			collection.lists[list.ID] = list
			if _, ok := collection.items[list.ID]; !ok {
				collection.items[list.ID] = make(map[string]models.WatchlistItem)
			}
		}
		for listID, items := range user.Items {
			listID = strings.TrimSpace(listID)
			if listID == "" {
				continue
			}
			byList := make(map[string]models.WatchlistItem, len(items))
			for _, item := range items {
				item = normaliseItem(item)
				byList[item.Key()] = item
			}
			collection.items[listID] = byList
		}
		s.data[userID] = collection
	}

	return nil
}

func (s *Service) saveLocked() error {
	if s.useDB() {
		return s.syncToDB()
	}

	byUser := make(map[string]persistedUser, len(s.data))
	for userID, collection := range s.data {
		lists := make([]models.CustomList, 0, len(collection.lists))
		for _, list := range collection.lists {
			list.ItemCount = 0
			lists = append(lists, list)
		}
		sort.Slice(lists, func(i, j int) bool {
			if lists[i].CreatedAt.Equal(lists[j].CreatedAt) {
				return lists[i].ID < lists[j].ID
			}
			return lists[i].CreatedAt.Before(lists[j].CreatedAt)
		})

		items := make(map[string][]models.WatchlistItem, len(collection.items))
		for listID, byKey := range collection.items {
			listItems := make([]models.WatchlistItem, 0, len(byKey))
			for _, item := range byKey {
				listItems = append(listItems, item)
			}
			sort.Slice(listItems, func(i, j int) bool {
				if listItems[i].AddedAt.Equal(listItems[j].AddedAt) {
					return listItems[i].Key() < listItems[j].Key()
				}
				return listItems[i].AddedAt.Before(listItems[j].AddedAt)
			})
			items[listID] = listItems
		}

		byUser[userID] = persistedUser{
			Lists: lists,
			Items: items,
		}
	}

	tmp := s.path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create custom lists temp file: %w", err)
	}

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(byUser); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("encode custom lists: %w", err)
	}

	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync custom lists: %w", err)
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close custom lists temp file: %w", err)
	}

	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace custom lists file: %w", err)
	}
	return nil
}

// syncToDB writes the full in-memory custom lists state to PostgreSQL.
func (s *Service) syncToDB() error {
	ctx := context.Background()
	return s.store.WithTx(ctx, func(tx *datastore.Tx) error {
		for userID, collection := range s.data {
			// Get existing DB lists for this user to detect deletes
			existing, err := tx.CustomLists().ListByUser(ctx, userID)
			if err != nil {
				return err
			}
			dbListIDs := make(map[string]bool, len(existing))
			for _, l := range existing {
				dbListIDs[l.ID] = true
			}

			// Upsert all in-memory lists and their items
			for listID, list := range collection.lists {
				cl := list
				if dbListIDs[listID] {
					if err := tx.CustomLists().UpdateList(ctx, &cl); err != nil {
						return err
					}
				} else {
					if err := tx.CustomLists().CreateList(ctx, userID, &cl); err != nil {
						return err
					}
				}
				delete(dbListIDs, listID)

				// Get existing items for this list to detect deletes
				existingItems, err := tx.CustomLists().GetItems(ctx, listID)
				if err != nil {
					return err
				}
				dbItemKeys := make(map[string]bool, len(existingItems))
				for _, item := range existingItems {
					dbItemKeys[item.Key()] = true
				}

				// Upsert all in-memory items
				for key, item := range collection.items[listID] {
					it := item
					if err := tx.CustomLists().UpsertItem(ctx, listID, &it); err != nil {
						return err
					}
					delete(dbItemKeys, key)
				}

				// Delete items removed from memory
				for key := range dbItemKeys {
					if err := tx.CustomLists().DeleteItem(ctx, listID, key); err != nil {
						return err
					}
				}
			}

			// Delete lists removed from memory (CASCADE will handle items)
			for listID := range dbListIDs {
				if err := tx.CustomLists().DeleteList(ctx, listID); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (s *Service) ensureUserLocked(userID string) *userCollection {
	user, ok := s.data[userID]
	if !ok {
		user = &userCollection{
			lists: make(map[string]models.CustomList),
			items: make(map[string]map[string]models.WatchlistItem),
		}
		s.data[userID] = user
	}
	return user
}

func normaliseItem(item models.WatchlistItem) models.WatchlistItem {
	item.MediaType = strings.ToLower(strings.TrimSpace(item.MediaType))
	if item.AddedAt.IsZero() {
		item.AddedAt = time.Now().UTC()
	}
	return item
}

func generateListID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("list-%d", time.Now().UTC().UnixNano())
	}
	return "list-" + hex.EncodeToString(b[:])
}
