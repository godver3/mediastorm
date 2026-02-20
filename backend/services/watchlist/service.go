package watchlist

import (
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

	"novastream/models"
)

var (
	ErrStorageDirRequired = errors.New("storage directory not provided")
	ErrUserIDRequired     = errors.New("user id is required")
	ErrIDRequired         = errors.New("id is required")
	ErrMediaTypeRequired  = errors.New("media type is required")
	ErrIdentifierRequired = errors.New("id and media type are required")
)

// Service manages persistence and retrieval of user watchlist items.
type Service struct {
	mu    sync.RWMutex
	path  string
	items map[string]map[string]models.WatchlistItem
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
		path:  filepath.Join(storageDir, "watchlist.json"),
		items: make(map[string]map[string]models.WatchlistItem),
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

	mediaType := strings.ToLower(strings.TrimSpace(input.MediaType))
	s.mu.Lock()
	defer s.mu.Unlock()

	perUser := s.ensureUserLocked(userID)

	key := mediaType + ":" + input.ID
	item, exists := perUser[key]

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

	// Update sync tracking fields if provided
	if strings.TrimSpace(input.SyncSource) != "" {
		item.SyncSource = input.SyncSource
	}
	if input.SyncedAt != nil {
		item.SyncedAt = input.SyncedAt
	}

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

	key := mediaType + ":" + id
	item, exists := perUser[key]
	if !exists {
		return models.WatchlistItem{}, os.ErrNotExist
	}

	// Watch status is now tracked separately - this method does nothing but return the item
	return item, nil
}

// Remove deletes an item from the watchlist.
func (s *Service) Remove(userID, mediaType, id string) (bool, error) {
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

	key := mediaType + ":" + id
	if _, exists := perUser[key]; !exists {
		return false, nil
	}

	delete(perUser, key)

	if err := s.saveLocked(); err != nil {
		return false, err
	}

	return true, nil
}

func (s *Service) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

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

func (s *Service) saveLocked() error {
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

	return nil
}

func (s *Service) ensureUserLocked(userID string) map[string]models.WatchlistItem {
	perUser, ok := s.items[userID]
	if !ok {
		perUser = make(map[string]models.WatchlistItem)
		s.items[userID] = perUser
	}
	return perUser
}

func normaliseItem(item models.WatchlistItem) models.WatchlistItem {
	item.MediaType = strings.ToLower(strings.TrimSpace(item.MediaType))
	if item.AddedAt.IsZero() {
		item.AddedAt = time.Now().UTC()
	}
	return item
}
