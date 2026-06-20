package importer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const resolvedNZBIndexFile = ".resolved_nzbs.json"

type ResolvedNZBSource struct {
	DownloadURL string
	Title       string
	Indexer     string
	FileSize    int64
}

type ResolvedNZBEntry struct {
	Key             string `json:"key"`
	NZBHash         string `json:"nzbHash"`
	DownloadURLHash string `json:"downloadUrlHash,omitempty"`
	FileName        string `json:"fileName,omitempty"`
	Title           string `json:"title,omitempty"`
	Indexer         string `json:"indexer,omitempty"`
	StoragePath     string `json:"storagePath"`
	FileSize        int64  `json:"fileSize,omitempty"`
	CreatedAt       int64  `json:"createdAt"`
	LastUsedAt      int64  `json:"lastUsedAt"`
	Hits            int64  `json:"hits"`
}

type ResolvedNZBListResult struct {
	Items      []ResolvedNZBEntry `json:"items"`
	Page       int                `json:"page"`
	PerPage    int                `json:"perPage"`
	Total      int                `json:"total"`
	TotalPages int                `json:"totalPages"`
}

type resolvedNZBIndex struct {
	Entries map[string]ResolvedNZBEntry `json:"entries"`
}

type resolvedNZBCache struct {
	mu              sync.Mutex
	path            string
	metadataService metadataPathValidator
}

type metadataPathValidator interface {
	RootPath() string
	DirectoryExists(virtualPath string) bool
	FileExists(virtualPath string) bool
	DeleteDirectory(virtualPath string) error
	DeleteFileMetadata(virtualPath string) error
}

func newResolvedNZBCache(metadataService metadataPathValidator) *resolvedNZBCache {
	if metadataService == nil {
		return nil
	}
	return &resolvedNZBCache{
		path:            filepath.Join(metadataService.RootPath(), resolvedNZBIndexFile),
		metadataService: metadataService,
	}
}

func resolvedNZBHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func resolvedURLHash(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(rawURL))
	return hex.EncodeToString(sum[:])
}

func (c *resolvedNZBCache) findByNZBHash(ctx context.Context, nzbHash string) (*ResolvedNZBEntry, bool, error) {
	if c == nil || strings.TrimSpace(nzbHash) == "" {
		return nil, false, nil
	}
	select {
	case <-ctx.Done():
		return nil, false, ctx.Err()
	default:
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	idx, err := c.loadLocked()
	if err != nil {
		return nil, false, err
	}
	entry, ok := idx.Entries[nzbHash]
	if !ok || !c.entryExists(entry) {
		if ok {
			delete(idx.Entries, nzbHash)
			_ = c.saveLocked(idx)
		}
		return nil, false, nil
	}
	c.touchLocked(idx, entry)
	return &entry, true, nil
}

func (c *resolvedNZBCache) findByDownloadURL(ctx context.Context, downloadURL string) (*ResolvedNZBEntry, bool, error) {
	if c == nil {
		return nil, false, nil
	}
	urlHash := resolvedURLHash(downloadURL)
	if urlHash == "" {
		return nil, false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	idx, err := c.loadLocked()
	if err != nil {
		return nil, false, err
	}
	for _, entry := range idx.Entries {
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		default:
		}
		if entry.DownloadURLHash != urlHash {
			continue
		}
		if !c.entryExists(entry) {
			delete(idx.Entries, entry.Key)
			_ = c.saveLocked(idx)
			return nil, false, nil
		}
		c.touchLocked(idx, entry)
		return &entry, true, nil
	}
	return nil, false, nil
}

func (c *resolvedNZBCache) put(ctx context.Context, nzbHash string, fileName string, storagePath string, source ResolvedNZBSource) error {
	if c == nil || strings.TrimSpace(nzbHash) == "" || strings.TrimSpace(storagePath) == "" {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	idx, err := c.loadLocked()
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	entry := idx.Entries[nzbHash]
	if entry.CreatedAt == 0 {
		entry.CreatedAt = now
	}
	entry.Key = nzbHash
	entry.NZBHash = nzbHash
	entry.DownloadURLHash = resolvedURLHash(source.DownloadURL)
	entry.FileName = strings.TrimSpace(fileName)
	entry.Title = strings.TrimSpace(source.Title)
	entry.Indexer = strings.TrimSpace(source.Indexer)
	entry.StoragePath = normalizeResolvedStoragePath(storagePath)
	entry.FileSize = source.FileSize
	entry.LastUsedAt = now
	idx.Entries[nzbHash] = entry
	return c.saveLocked(idx)
}

func (c *resolvedNZBCache) list(ctx context.Context, filter string, page, perPage int) (*ResolvedNZBListResult, error) {
	if c == nil {
		return &ResolvedNZBListResult{Items: []ResolvedNZBEntry{}, Page: 1, PerPage: 25}, nil
	}
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 25
	}
	if perPage > 100 {
		perPage = 100
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	idx, err := c.loadLocked()
	if err != nil {
		return nil, err
	}
	filter = strings.ToLower(strings.TrimSpace(filter))
	items := make([]ResolvedNZBEntry, 0, len(idx.Entries))
	changed := false
	for key, entry := range idx.Entries {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if !c.entryExists(entry) {
			delete(idx.Entries, key)
			changed = true
			continue
		}
		if filter != "" && !resolvedEntryMatches(entry, filter) {
			continue
		}
		items = append(items, entry)
	}
	if changed {
		_ = c.saveLocked(idx)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].LastUsedAt > items[j].LastUsedAt
	})

	total := len(items)
	totalPages := 0
	if total > 0 {
		totalPages = (total + perPage - 1) / perPage
	}
	start := (page - 1) * perPage
	if start > total {
		start = total
	}
	end := start + perPage
	if end > total {
		end = total
	}
	return &ResolvedNZBListResult{
		Items:      items[start:end],
		Page:       page,
		PerPage:    perPage,
		Total:      total,
		TotalPages: totalPages,
	}, nil
}

func (c *resolvedNZBCache) delete(ctx context.Context, key string) error {
	if c == nil {
		return nil
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("resolved NZB key is required")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	idx, err := c.loadLocked()
	if err != nil {
		return err
	}
	entry, ok := idx.Entries[key]
	if !ok {
		return os.ErrNotExist
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if c.metadataService.DirectoryExists(entry.StoragePath) {
		if err := c.metadataService.DeleteDirectory(entry.StoragePath); err != nil {
			return err
		}
	} else if c.metadataService.FileExists(entry.StoragePath) {
		if err := c.metadataService.DeleteFileMetadata(entry.StoragePath); err != nil {
			return err
		}
	}
	delete(idx.Entries, key)
	return c.saveLocked(idx)
}

func (c *resolvedNZBCache) touchLocked(idx *resolvedNZBIndex, entry ResolvedNZBEntry) {
	entry.LastUsedAt = time.Now().Unix()
	entry.Hits++
	idx.Entries[entry.Key] = entry
	_ = c.saveLocked(idx)
}

func (c *resolvedNZBCache) loadLocked() (*resolvedNZBIndex, error) {
	data, err := os.ReadFile(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &resolvedNZBIndex{Entries: map[string]ResolvedNZBEntry{}}, nil
		}
		return nil, err
	}
	var idx resolvedNZBIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, err
	}
	if idx.Entries == nil {
		idx.Entries = map[string]ResolvedNZBEntry{}
	}
	return &idx, nil
}

func (c *resolvedNZBCache) saveLocked(idx *resolvedNZBIndex) error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

func (c *resolvedNZBCache) entryExists(entry ResolvedNZBEntry) bool {
	storagePath := normalizeResolvedStoragePath(entry.StoragePath)
	return storagePath != "" && (c.metadataService.DirectoryExists(storagePath) || c.metadataService.FileExists(storagePath))
}

func resolvedEntryMatches(entry ResolvedNZBEntry, filter string) bool {
	for _, value := range []string{entry.FileName, entry.Title, entry.Indexer, entry.StoragePath, entry.NZBHash} {
		if strings.Contains(strings.ToLower(value), filter) {
			return true
		}
	}
	return false
}

func normalizeResolvedStoragePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return filepath.ToSlash(filepath.Clean(path))
}
