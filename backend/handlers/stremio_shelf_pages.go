package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"novastream/services/metadata"
)

// A prefix is resumable, but is never treated as a complete catalog. Snapshots
// own their slices so concurrent foreground/background reads cannot mutate them.
type stremioShelfCatalogCacheEntry struct {
	metas    []stremioMeta
	fetched  time.Time
	more     bool
	nextPage int
	pageSize int
	catalog  stremioCatalogDef
	baseURL  string
}

func (h *MetadataHandler) loadStremioShelfCatalog(ctx context.Context, rawManifestURL, catalogType, catalogID string) ([]stremioMeta, error) {
	entry, err := h.loadStremioShelfCatalogPrefix(ctx, rawManifestURL, catalogType, catalogID, stremioShelfMaxCatalogItems)
	return entry.metas, err
}

func (h *MetadataHandler) loadStremioShelfCatalogPrefix(ctx context.Context, rawManifestURL, catalogType, catalogID string, limit int) (stremioShelfCatalogCacheEntry, error) {
	manifestURL, _, err := normalizeStremioManifestInput(rawManifestURL)
	if err != nil {
		return stremioShelfCatalogCacheEntry{}, err
	}
	key := manifestURL + "|" + catalogType + "|" + catalogID
	h.stremioCatalogMu.Lock()
	entry, cached := h.stremioCatalogCache[key]
	cached = cached && time.Since(entry.fetched) < stremioShelfCatalogTTL
	entry.metas = append([]stremioMeta(nil), entry.metas...)
	h.stremioCatalogMu.Unlock()
	if cached && (!entry.more || len(entry.metas) >= limit) {
		return entry, ctx.Err()
	}
	if !cached {
		manifest, _, baseURL, err := h.loadStremioManifest(ctx, h.stremioShelfClient(), manifestURL)
		if err != nil {
			return stremioShelfCatalogCacheEntry{}, err
		}
		var selected *stremioCatalogDef
		for i := range manifest.Catalogs {
			catalog := &manifest.Catalogs[i]
			if normalizeStremioCatalogType(catalog.Type) == catalogType && strings.TrimSpace(catalog.ID) == catalogID {
				selected = catalog
				break
			}
		}
		if selected == nil {
			return stremioShelfCatalogCacheEntry{}, fmt.Errorf("Stremio catalog is not advertised by the manifest")
		}
		entry = newStremioShelfCatalogEntry(baseURL, *selected)
	}
	entry, err = fetchStremioShelfCatalogPrefix(ctx, h.stremioShelfClient(), entry, limit)
	if err != nil {
		return stremioShelfCatalogCacheEntry{}, fmt.Errorf("fetch Stremio catalog: %w", err)
	}
	h.stremioCatalogMu.Lock()
	if h.stremioCatalogCache == nil {
		h.stremioCatalogCache = make(map[string]stremioShelfCatalogCacheEntry)
	}
	current, exists := h.stremioCatalogCache[key]
	// A late foreground prefix must not overwrite a longer background result.
	if !exists || time.Since(current.fetched) >= stremioShelfCatalogTTL ||
		(current.more && (len(entry.metas) > len(current.metas) || !entry.more)) {
		snapshot := entry
		snapshot.metas = append([]stremioMeta(nil), entry.metas...)
		h.stremioCatalogCache[key] = snapshot
		pruneStremioCatalogCache(h.stremioCatalogCache, stremioShelfCatalogCacheMax)
	}
	h.stremioCatalogMu.Unlock()
	return entry, nil
}

// stremioShelfCatalogCacheMax caps cached catalogs so unique manifest/catalog
// keys cannot grow the in-memory cache without bound.
const stremioShelfCatalogCacheMax = 256

// pruneStremioCatalogCache drops expired entries first, then the oldest ones,
// until at most max remain. Callers hold stremioCatalogMu.
func pruneStremioCatalogCache(cache map[string]stremioShelfCatalogCacheEntry, max int) {
	if len(cache) <= max {
		return
	}
	for k, e := range cache {
		if time.Since(e.fetched) >= stremioShelfCatalogTTL {
			delete(cache, k)
		}
	}
	for len(cache) > max {
		var oldestKey string
		var oldest time.Time
		first := true
		for k, e := range cache {
			if first || e.fetched.Before(oldest) {
				oldestKey, oldest, first = k, e.fetched, false
			}
		}
		delete(cache, oldestKey)
	}
}

func newStremioShelfCatalogEntry(baseURL string, catalog stremioCatalogDef) stremioShelfCatalogCacheEntry {
	pageSize := catalog.PageSize
	if pageSize <= 0 || pageSize > stremioShelfMaxCatalogItems {
		pageSize = 0
	}
	return stremioShelfCatalogCacheEntry{baseURL: baseURL, catalog: catalog, pageSize: pageSize, more: true, fetched: time.Now()}
}

func fetchStremioShelfCatalog(ctx context.Context, client *http.Client, baseURL string, catalog stremioCatalogDef) ([]stremioMeta, error) {
	entry, err := fetchStremioShelfCatalogPrefix(ctx, client, newStremioShelfCatalogEntry(baseURL, catalog), stremioShelfMaxCatalogItems)
	return entry.metas, err
}

func stremioShelfMetaKey(meta stremioMeta) string {
	identity := strings.TrimSpace(meta.ID)
	if identity == "" {
		data, _ := json.Marshal(meta)
		identity = string(data)
	}
	return meta.Type + "\x00" + identity
}

func fetchStremioShelfCatalogPrefix(ctx context.Context, client *http.Client, entry stremioShelfCatalogCacheEntry, limit int) (stremioShelfCatalogCacheEntry, error) {
	supportsSkip := false
	for _, extra := range entry.catalog.Extra {
		if strings.EqualFold(strings.TrimSpace(extra.Name), "skip") {
			supportsSkip = true
			break
		}
	}
	seen := make(map[string]bool, len(entry.metas))
	for _, meta := range entry.metas {
		seen[stremioShelfMetaKey(meta)] = true
	}
	for entry.more && len(entry.metas) < limit {
		endpoint := fmt.Sprintf("%s/catalog/%s/%s.json", entry.baseURL, url.PathEscape(entry.catalog.Type), url.PathEscape(entry.catalog.ID))
		if entry.nextPage > 0 {
			endpoint = fmt.Sprintf("%s/catalog/%s/%s/skip=%d.json", entry.baseURL, url.PathEscape(entry.catalog.Type), url.PathEscape(entry.catalog.ID), entry.nextPage*entry.pageSize)
		}
		var response stremioCatalogResponse
		if err := getStremioShelfJSON(ctx, client, endpoint, &response); err != nil {
			if entry.nextPage == 0 || ctx.Err() != nil {
				return stremioShelfCatalogCacheEntry{}, err
			}
			entry.more = false
			break
		}
		entry.nextPage++
		if len(response.Metas) == 0 {
			entry.more = false
			break
		}
		if entry.pageSize == 0 {
			// Nuvio omits pageSize. Preserve the first page's stride even when
			// later pages omit titles or contain duplicates.
			entry.pageSize = len(response.Metas)
		}
		newItems := 0
		for _, meta := range response.Metas {
			key := stremioShelfMetaKey(meta)
			if seen[key] {
				continue
			}
			seen[key] = true
			entry.metas = append(entry.metas, meta)
			newItems++
			if len(entry.metas) == stremioShelfMaxCatalogItems {
				break
			}
		}
		// Short pages can have successors; empty/repeated pages end the catalog.
		entry.more = supportsSkip && newItems > 0 && len(entry.metas) < stremioShelfMaxCatalogItems
	}
	if err := ctx.Err(); err != nil {
		return stremioShelfCatalogCacheEntry{}, err
	}
	return entry, nil
}

func (h *MetadataHandler) progressiveStremioShelf(r *http.Request, manifestURL, catalogType, catalogID string) (*CustomListResponse, error) {
	query := r.URL.Query()
	userID := strings.TrimSpace(query.Get("userId"))
	hideUnreleased := strings.EqualFold(query.Get("hideUnreleased"), "true")
	hideWatched := strings.EqualFold(query.Get("hideWatched"), "true")
	limit, offset := parseLimitOffset(r)
	label := strings.TrimSpace(query.Get("name"))
	if label == "" {
		label = catalogID
	}
	batch := max(limit, 20)
	wanted := min(stremioShelfMaxCatalogItems, offset+batch)
	for {
		entry, err := h.loadStremioShelfCatalogPrefix(r.Context(), manifestURL, catalogType, catalogID, wanted)
		if err != nil {
			return nil, err
		}
		source := make([]metadata.CuratedItem, 0, len(entry.metas))
		for _, meta := range entry.metas {
			item := curatedItemFromStremioMeta(meta, catalogType)
			if item.Title != "" || item.IMDBID != "" || item.TMDBID > 0 {
				source = append(source, item)
			}
		}
		response, err := h.progressiveCuratedShelf(r, h.serviceForUser(userID), source, label, userID, hideUnreleased, hideWatched, limit, offset)
		if err != nil {
			return nil, err
		}
		if !entry.more || len(response.Items) >= limit {
			if entry.more {
				response.TotalPending = true
				response.Total = max(response.Total, offset+len(response.Items)+1)
			}
			return response, nil
		}
		// Watched/hidden/unreleased/kids filtering may require another page.
		wanted = min(stremioShelfMaxCatalogItems, len(entry.metas)+1)
	}
}
