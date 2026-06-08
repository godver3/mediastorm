package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Stremio Live TV source support.
//
// A Stremio addon exposes an HTTP API: a manifest.json describing catalogs, and
// catalog/meta/stream resources. We treat such an addon as a Live TV source by
// turning its catalog "metas" into channels. Because addon stream URLs are
// frequently short-lived (signed, expiring), a channel's URL points at the
// addon's stream resource (".../stream/{type}/{id}.json") and is resolved to a
// concrete playable URL at tune-in time by StreamChannel.

const (
	// stremioChannelsTTL bounds how long a fetched catalog (channel list) is
	// cached in memory. Kept short because live/event catalogs change often.
	stremioChannelsTTL = 5 * time.Minute
	// stremioMaxCatalogPages caps catalog pagination to avoid unbounded fetches.
	stremioMaxCatalogPages = 10
	// stremioCatalogPageSize matches the Stremio default skip increment.
	stremioCatalogPageSize = 100
)

type stremioManifest struct {
	Catalogs []stremioCatalogDef `json:"catalogs"`
}

type stremioCatalogDef struct {
	Type  string             `json:"type"`
	ID    string             `json:"id"`
	Name  string             `json:"name"`
	Extra []stremioExtraProp `json:"extra"`
}

type stremioExtraProp struct {
	Name string `json:"name"`
}

type stremioMeta struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Poster      string   `json:"poster"`
	Genres      []string `json:"genres"`
	Description string   `json:"description"`
}

type stremioCatalogResponse struct {
	Metas []stremioMeta `json:"metas"`
}

type stremioStream struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

type stremioStreamResponse struct {
	Streams []stremioStream `json:"streams"`
}

type stremioChannelsCacheEntry struct {
	channels []LiveChannel
	fetched  time.Time
}

// normalizeStremioBaseURL strips a trailing slash and an optional
// "/manifest.json" suffix, preserving any addon config path segment.
func normalizeStremioBaseURL(raw string) string {
	base := strings.TrimSpace(raw)
	base = strings.TrimSuffix(base, "/")
	base = strings.TrimSuffix(base, "/manifest.json")
	return strings.TrimSuffix(base, "/")
}

// stremioStreamResourceURL builds the stream resource URL for a meta id.
func stremioStreamResourceURL(baseURL, mediaType, id string) string {
	return fmt.Sprintf("%s/stream/%s/%s.json", baseURL, mediaType, url.PathEscape(id))
}

// isStremioStreamResourceURL reports whether a URL points at a Stremio stream
// resource (".../stream/{type}/{id}.json"), which must be resolved before play.
func isStremioStreamResourceURL(u *url.URL) bool {
	if u == nil {
		return false
	}
	path := u.Path
	return strings.Contains(path, "/stream/") && strings.HasSuffix(path, ".json")
}

// fetchStremioChannels returns the channel list for a Stremio addon, building
// channels from every catalog the addon advertises. Results are cached in
// memory for stremioChannelsTTL.
func (h *LiveHandler) fetchStremioChannels(ctx context.Context, manifestURL, proxyURL string) ([]LiveChannel, error) {
	manifestURL = strings.TrimSpace(manifestURL)
	if manifestURL == "" {
		return nil, fmt.Errorf("stremio: empty manifest url")
	}

	cacheKey := manifestURL + "|" + strings.TrimSpace(proxyURL)
	h.stremioMu.Lock()
	if entry, ok := h.stremioCache[cacheKey]; ok && time.Since(entry.fetched) < stremioChannelsTTL {
		channels := entry.channels
		h.stremioMu.Unlock()
		return channels, nil
	}
	h.stremioMu.Unlock()

	baseURL := normalizeStremioBaseURL(manifestURL)
	client := h.livePlaylistScanHTTPClient(proxyURL)

	manifest, err := fetchStremioManifest(ctx, client, baseURL)
	if err != nil {
		return nil, err
	}

	var channels []LiveChannel
	seen := make(map[string]bool)
	for _, catalog := range manifest.Catalogs {
		metas, err := fetchStremioCatalog(ctx, client, baseURL, catalog)
		if err != nil {
			// A single failing catalog shouldn't sink the whole source.
			log.Printf("[live][stremio] catalog %q (%s) failed: %v", catalog.ID, catalog.Type, err)
			continue
		}
		for _, meta := range metas {
			id := strings.TrimSpace(meta.ID)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			group := strings.TrimSpace(catalog.Name)
			if len(meta.Genres) > 0 && strings.TrimSpace(meta.Genres[0]) != "" {
				group = strings.TrimSpace(meta.Genres[0])
			}
			channels = append(channels, LiveChannel{
				ID:    id,
				Name:  strings.TrimSpace(meta.Name),
				URL:   stremioStreamResourceURL(baseURL, catalog.Type, id),
				Logo:  strings.TrimSpace(meta.Poster),
				Group: group,
				TvgID: id,
			})
		}
	}

	h.stremioMu.Lock()
	h.stremioCache[cacheKey] = stremioChannelsCacheEntry{channels: channels, fetched: time.Now()}
	h.stremioMu.Unlock()

	return channels, nil
}

func fetchStremioManifest(ctx context.Context, client *http.Client, baseURL string) (*stremioManifest, error) {
	var manifest stremioManifest
	if err := getStremioJSON(ctx, client, baseURL+"/manifest.json", &manifest); err != nil {
		return nil, fmt.Errorf("stremio: fetch manifest: %w", err)
	}
	if len(manifest.Catalogs) == 0 {
		return nil, fmt.Errorf("stremio: manifest has no catalogs")
	}
	return &manifest, nil
}

func fetchStremioCatalog(ctx context.Context, client *http.Client, baseURL string, catalog stremioCatalogDef) ([]stremioMeta, error) {
	supportsSkip := false
	for _, extra := range catalog.Extra {
		if strings.EqualFold(strings.TrimSpace(extra.Name), "skip") {
			supportsSkip = true
			break
		}
	}

	var all []stremioMeta
	for page := 0; page < stremioMaxCatalogPages; page++ {
		endpoint := fmt.Sprintf("%s/catalog/%s/%s.json", baseURL, catalog.Type, url.PathEscape(catalog.ID))
		if page > 0 {
			endpoint = fmt.Sprintf("%s/catalog/%s/%s/skip=%d.json", baseURL, catalog.Type, url.PathEscape(catalog.ID), page*stremioCatalogPageSize)
		}
		var resp stremioCatalogResponse
		if err := getStremioJSON(ctx, client, endpoint, &resp); err != nil {
			if page == 0 {
				return nil, err
			}
			break // later pages 404 once the catalog is exhausted
		}
		if len(resp.Metas) == 0 {
			break
		}
		all = append(all, resp.Metas...)
		if !supportsSkip || len(resp.Metas) < stremioCatalogPageSize {
			break
		}
	}
	return all, nil
}

// resolveStremioStream fetches a stream resource and returns the first playable
// stream URL.
func (h *LiveHandler) resolveStremioStream(ctx context.Context, streamResourceURL, proxyURL string) (string, error) {
	client := h.liveStreamHTTPClient(proxyURL)
	var resp stremioStreamResponse
	if err := getStremioJSON(ctx, client, streamResourceURL, &resp); err != nil {
		return "", fmt.Errorf("stremio: resolve stream: %w", err)
	}
	for _, stream := range resp.Streams {
		if u := strings.TrimSpace(stream.URL); u != "" {
			return u, nil
		}
	}
	return "", fmt.Errorf("stremio: no playable stream for %s", streamResourceURL)
}

func getStremioJSON(ctx context.Context, client *http.Client, endpoint string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", liveStreamUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
