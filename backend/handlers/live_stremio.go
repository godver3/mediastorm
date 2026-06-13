package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
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
	Name          string               `json:"name"`
	Title         string               `json:"title"`
	Description   string               `json:"description"`
	URL           string               `json:"url"`
	BehaviorHints stremioBehaviorHints `json:"behaviorHints"`
}

type stremioBehaviorHints struct {
	ProxyHeaders stremioProxyHeaders `json:"proxyHeaders"`
}

type stremioProxyHeaders struct {
	Request map[string]string `json:"request"`
}

type stremioStreamResponse struct {
	Streams []stremioStream `json:"streams"`
}

type StremioStreamOption struct {
	Index       int    `json:"index"`
	Name        string `json:"name,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Label       string `json:"label"`
}

type StremioStreamOptionsResponse struct {
	Streams []StremioStreamOption `json:"streams"`
}

type stremioChannelsCacheEntry struct {
	channels []LiveChannel
	fetched  time.Time
}

type resolvedStremioStream struct {
	URL            string
	RequestHeaders map[string]string
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

func isUnplayableStremioStreamURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	path := strings.ToLower(strings.TrimSuffix(u.EscapedPath(), "/"))
	return strings.HasSuffix(host, ".invalid") || host == "stremverse.invalid" || path == "/subscribe"
}

func firstPlayableStremioStreamURL(streams []stremioStream) (string, bool) {
	resolved, ok := playableStremioStream(streams, -1)
	if !ok {
		return "", false
	}
	return resolved.URL, true
}

func playableStremioStream(streams []stremioStream, selectedIndex int) (resolvedStremioStream, bool) {
	for i, stream := range streams {
		u, headers := normalizeStremioPlayableURL(stream.URL, stream.BehaviorHints.ProxyHeaders.Request)
		if u == "" || isUnplayableStremioStreamURL(u) {
			continue
		}
		if selectedIndex >= 0 && i != selectedIndex {
			continue
		}
		return resolvedStremioStream{
			URL:            u,
			RequestHeaders: headers,
		}, true
	}
	return resolvedStremioStream{}, false
}

func playableStremioStreamOptions(streams []stremioStream) []StremioStreamOption {
	options := make([]StremioStreamOption, 0, len(streams))
	labelCounts := make(map[string]int)
	for i, stream := range streams {
		u, _ := normalizeStremioPlayableURL(stream.URL, stream.BehaviorHints.ProxyHeaders.Request)
		if u == "" || isUnplayableStremioStreamURL(u) {
			continue
		}
		name := strings.TrimSpace(stream.Name)
		title := strings.TrimSpace(stream.Title)
		description := strings.TrimSpace(stream.Description)
		label := description
		if label == "" {
			label = title
		}
		if label == "" {
			label = name
		}
		if label == "" {
			label = fmt.Sprintf("Source %d", len(options)+1)
		}
		labelCounts[label]++
		options = append(options, StremioStreamOption{
			Index:       i,
			Name:        name,
			Title:       title,
			Description: description,
			Label:       label,
		})
	}
	seenLabels := make(map[string]int)
	for i := range options {
		label := options[i].Label
		if labelCounts[label] <= 1 {
			continue
		}
		seenLabels[label]++
		options[i].Label = fmt.Sprintf("%s (Source %d)", label, seenLabels[label])
	}
	return options
}

func normalizeStremioPlayableURL(raw string, headers map[string]string) (string, map[string]string) {
	u := strings.TrimSpace(raw)
	requestHeaders := sanitizeStremioRequestHeaders(headers)
	parsed, err := url.Parse(u)
	if err != nil || parsed == nil {
		return u, requestHeaders
	}
	query := parsed.Query()
	target := strings.TrimSpace(query.Get("d"))
	if target == "" {
		return u, requestHeaders
	}
	targetURL, err := url.Parse(target)
	if err != nil || targetURL == nil || targetURL.Host == "" || (targetURL.Scheme != "http" && targetURL.Scheme != "https") {
		return u, requestHeaders
	}
	if requestHeaders == nil {
		requestHeaders = make(map[string]string)
	}
	for key, values := range query {
		if len(values) == 0 || !strings.HasPrefix(strings.ToLower(key), "h_") {
			continue
		}
		headerName := strings.TrimSpace(key[2:])
		headerValue := strings.TrimSpace(values[0])
		if headerName == "" || headerValue == "" || hasRequestHeader(requestHeaders, headerName) {
			continue
		}
		requestHeaders[headerName] = headerValue
	}
	return targetURL.String(), sanitizeStremioRequestHeaders(requestHeaders)
}

func sanitizeStremioRequestHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	sanitized := make(map[string]string, len(headers))
	for key, value := range headers {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" || strings.ContainsAny(key, "\r\n:") || strings.ContainsAny(value, "\r\n") {
			continue
		}
		sanitized[key] = value
	}
	if len(sanitized) == 0 {
		return nil
	}
	return sanitized
}

func applyRequestHeaders(target http.Header, headers map[string]string) {
	for key, value := range headers {
		target.Set(key, value)
	}
}

func ffmpegHeadersArg(headers map[string]string) string {
	if len(headers) == 0 {
		return ""
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		value := strings.TrimSpace(headers[key])
		if key == "" || value == "" {
			continue
		}
		b.WriteString(key)
		b.WriteString(": ")
		b.WriteString(value)
		b.WriteString("\r\n")
	}
	return b.String()
}

func hasRequestHeader(headers map[string]string, name string) bool {
	for key, value := range headers {
		if strings.EqualFold(strings.TrimSpace(key), name) && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func parseOptionalStremioStreamIndex(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return -1
	}
	index, err := strconv.Atoi(raw)
	if err != nil || index < 0 {
		return -1
	}
	return index
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

// resolveStremioStream fetches a stream resource and returns a playable stream.
// selectedIndex < 0 means "first playable".
func (h *LiveHandler) resolveStremioStream(ctx context.Context, streamResourceURL, proxyURL string, selectedIndex int) (resolvedStremioStream, error) {
	client := h.liveStreamHTTPClient(proxyURL)
	var resp stremioStreamResponse
	if err := getStremioJSON(ctx, client, streamResourceURL, &resp); err != nil {
		return resolvedStremioStream{}, fmt.Errorf("stremio: resolve stream: %w", err)
	}
	if stream, ok := playableStremioStream(resp.Streams, selectedIndex); ok {
		return stream, nil
	}
	return resolvedStremioStream{}, fmt.Errorf("stremio: no playable stream for %s", streamResourceURL)
}

func (h *LiveHandler) GetStremioStreamOptions(w http.ResponseWriter, r *http.Request) {
	streamResourceURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if streamResourceURL == "" {
		http.Error(w, `{"error":"missing url parameter"}`, http.StatusBadRequest)
		return
	}
	parsed, err := h.parseRemoteURL(streamResourceURL)
	if err != nil {
		http.Error(w, `{"error":"invalid url parameter"}`, http.StatusBadRequest)
		return
	}
	if !isStremioStreamResourceURL(parsed) {
		http.Error(w, `{"error":"not a stremio stream resource"}`, http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), liveStreamTimeout)
	defer cancel()

	client := h.liveStreamHTTPClient(h.resolveProxyURLForStream(r, parsed))
	var resp stremioStreamResponse
	if err := getStremioJSON(ctx, client, streamResourceURL, &resp); err != nil {
		log.Printf("[live] failed to fetch stremio stream options %q: %v", streamResourceURL, err)
		http.Error(w, `{"error":"failed to fetch stream options"}`, http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(StremioStreamOptionsResponse{Streams: playableStremioStreamOptions(resp.Streams)}); err != nil {
		log.Printf("[live] GetStremioStreamOptions JSON encode error: %v", err)
	}
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
