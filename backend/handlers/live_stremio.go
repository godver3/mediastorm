package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"novastream/internal/requestsecurity"
	"novastream/models"
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
	ID        string              `json:"id"`
	Version   string              `json:"version"`
	Name      string              `json:"name"`
	Resources []json.RawMessage   `json:"resources"`
	Types     []string            `json:"types"`
	Catalogs  []stremioCatalogDef `json:"catalogs"`
}

type stremioCatalogDef struct {
	Type     string             `json:"type"`
	ID       string             `json:"id"`
	Name     string             `json:"name"`
	PageSize int                `json:"pageSize,omitempty"`
	Extra    []stremioExtraProp `json:"extra"`
}

type stremioExtraProp struct {
	Name       string   `json:"name"`
	IsRequired bool     `json:"isRequired"`
	Options    []string `json:"options"`
}

type stremioMeta struct {
	ID          string      `json:"id"`
	Type        string      `json:"type"`
	Name        string      `json:"name"`
	Poster      string      `json:"poster"`
	Background  string      `json:"background"`
	Genres      []string    `json:"genres"`
	Description string      `json:"description"`
	ReleaseInfo string      `json:"releaseInfo"`
	IMDBRating  interface{} `json:"imdbRating"`
	Rank        int         `json:"rank"`
}

type stremioCatalogResponse struct {
	Metas []stremioMeta `json:"metas"`
}

type stremioStream struct {
	// Addon metadata is optional and not standardized; tolerate unexpected types.
	Resolution    json.RawMessage      `json:"resolution"`
	Quality       json.RawMessage      `json:"quality"`
	Bitrate       json.RawMessage      `json:"bitrate"`
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
	ReportedQuality *models.SportsReportedQuality `json:"reportedQuality,omitempty"`
	Index           int                           `json:"index"`
	Name            string                        `json:"name,omitempty"`
	Title           string                        `json:"title,omitempty"`
	Description     string                        `json:"description,omitempty"`
	Label           string                        `json:"label"`
}

type StremioStreamOptionsResponse struct {
	Streams []StremioStreamOption `json:"streams"`
}

type stremioChannelsCacheEntry struct {
	channels []LiveChannel
	fetched  time.Time
}

type resolvedStremioStream struct {
	URL              string
	OriginalURL      string
	RequestHeaders   map[string]string
	IsHLS            bool
	Index            int
	AvailableIndexes []int
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
	if err != nil || u == nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return true
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
	availableIndexes := playableStremioStreamIndexes(streams)
	for _, i := range availableIndexes {
		stream := streams[i]
		u, headers := normalizeStremioPlayableURL(stream.URL, stream.BehaviorHints.ProxyHeaders.Request)
		if u == "" || isUnplayableStremioStreamURL(u) {
			continue
		}
		if selectedIndex >= 0 && i != selectedIndex {
			continue
		}
		return resolvedStremioStream{
			URL:              u,
			OriginalURL:      strings.TrimSpace(stream.URL),
			RequestHeaders:   headers,
			IsHLS:            stremioStreamLooksLikeHLS(stream, u),
			Index:            i,
			AvailableIndexes: availableIndexes,
		}, true
	}
	return resolvedStremioStream{}, false
}

func stremioStreamLooksLikeHLS(stream stremioStream, normalizedURL string) bool {
	if inputLooksLikeHLS(normalizedURL) || inputLooksLikeHLS(stream.URL) {
		return true
	}
	// Some sports resolvers expose an extensionless worker endpoint and signal
	// the actual format only in a query value such as `ext=.m3u8`. The generic
	// URL detector intentionally ignores queries, but Stremio stream metadata is
	// trusted as a format hint and must retain this signal.
	if strings.Contains(strings.ToLower(normalizedURL), ".m3u8") || strings.Contains(strings.ToLower(stream.URL), ".m3u8") {
		return true
	}
	hint := strings.ToLower(strings.Join([]string{stream.Name, stream.Title, stream.Description}, " "))
	return strings.Contains(hint, "m3u8") || strings.Contains(hint, "hls")
}

func playableStremioStreamIndexes(streams []stremioStream) []int {
	options := playableStremioStreamOptions(streams)
	indexes := make([]int, 0, len(options))
	for _, option := range options {
		indexes = append(indexes, option.Index)
	}
	return indexes
}

// maybeRouteStremioStreamThroughAddonRelay detects the optional HLS relay used
// by some live-sports addons. Their signed upstream URLs can be bound to the
// addon's network/session context and return 404 when FFmpeg fetches them
// directly, even with behaviorHints.proxyHeaders. Keeping the request on the
// addon's origin preserves that context while MediaStorm still owns playback.
func maybeRouteStremioStreamThroughAddonRelay(ctx context.Context, client *http.Client, streamResourceURL string, stream resolvedStremioStream) resolvedStremioStream {
	if client == nil {
		return stream
	}

	resourceURL, err := url.Parse(streamResourceURL)
	if err != nil || resourceURL == nil || resourceURL.Host == "" {
		return stream
	}
	streamPathIndex := strings.Index(resourceURL.Path, "/stream/")
	if streamPathIndex < 0 {
		return stream
	}

	addonPrefix := strings.TrimSuffix(resourceURL.Path[:streamPathIndex], "/")
	candidates := make([]url.URL, 0, 3)
	seen := make(map[string]struct{})
	addCandidate := func(candidate url.URL) {
		candidate.RawPath = ""
		candidate.Fragment = ""
		key := candidate.String()
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, candidate)
	}

	// Several Nuvio-compatible addons return a wrapper on an internal hostname,
	// for example /proxy/hls/manifest.m3u8?d=<signed-url>&h_Referer=.... The
	// wrapper is the playable resource; decoding `d` and contacting the signed
	// URL from MediaStorm changes the network/session context and yields 404.
	// Rebase the exact wrapper path and query onto the already-allowed addon
	// origin so the addon continues to own its upstream session.
	if original, parseErr := url.Parse(strings.TrimSpace(stream.OriginalURL)); parseErr == nil && original != nil && original.Query().Get("d") != "" {
		rebased := *resourceURL
		rebased.Path = addonPrefix + "/" + strings.TrimPrefix(original.Path, "/")
		rebased.RawQuery = original.RawQuery
		addCandidate(rebased)
		if addonPrefix != "" {
			rebased.Path = "/" + strings.TrimPrefix(original.Path, "/")
			addCandidate(rebased)
		}
	}

	// Some addons expose a generic relay rather than returning a wrapper URL.
	if len(stream.RequestHeaders) > 0 && strings.Contains(strings.ToLower(stream.URL), ".m3u8") {
		relay := *resourceURL
		relay.Path = addonPrefix + "/api/hls/playlist.m3u8"
		query := url.Values{}
		query.Set("url", stream.URL)
		if referer := requestHeaderValue(stream.RequestHeaders, "Referer"); referer != "" {
			query.Set("referer", referer)
		}
		if origin := requestHeaderValue(stream.RequestHeaders, "Origin"); origin != "" {
			query.Set("embedOrigin", origin)
		}
		relay.RawQuery = query.Encode()
		addCandidate(relay)
	}

	for _, relayURL := range candidates {
		probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		req, reqErr := http.NewRequestWithContext(probeCtx, http.MethodGet, relayURL.String(), nil)
		if reqErr != nil {
			cancel()
			continue
		}
		resp, reqErr := client.Do(req)
		if reqErr != nil {
			cancel()
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		resp.Body.Close()
		cancel()
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices || readErr != nil || !strings.Contains(string(body), "#EXTM3U") {
			continue
		}

		log.Printf("[live][stremio] using addon HLS relay for stream host %s", requestsecurity.URLForLog(stream.URL))
		stream.URL = relayURL.String()
		stream.RequestHeaders = nil
		stream.IsHLS = true
		return stream
	}
	if len(candidates) > 0 {
		log.Printf("[live][stremio] addon HLS relay unavailable after %d candidate(s); using direct stream host %s", len(candidates), requestsecurity.URLForLog(stream.URL))
	}
	return stream
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
			ReportedQuality: reportedStremioQuality(stream),
			Index:           i,
			Name:            name,
			Title:           title,
			Description:     description,
			Label:           label,
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
	sort.SliceStable(options, func(i, j int) bool {
		return compareSportsQuality(options[i].ReportedQuality, options[j].ReportedQuality) < 0
	})
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
	return requestHeaderValue(headers, name) != ""
}

func requestHeaderValue(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(strings.TrimSpace(key), name) && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
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
	if _, err := h.parseRemoteURL(ctx, manifestURL); err != nil {
		return nil, fmt.Errorf("stremio: manifest URL is not allowed")
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
	client := h.discoveryClient(h.livePlaylistScanHTTPClient(proxyURL), proxyURL, stremioChannelsTTL)

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
				SportsMetadata: strings.TrimSpace(meta.Description),
				ID:             id,
				Name:           strings.TrimSpace(meta.Name),
				URL:            stremioStreamResourceURL(baseURL, catalog.Type, id),
				Logo:           strings.TrimSpace(meta.Poster),
				Group:          group,
				TvgID:          id,
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

// stremioCatalogFilters builds browse requests for required enumerated filters.
// An advertised All option covers the catalog; otherwise visit each option.
func stremioCatalogFilters(catalog stremioCatalogDef) ([]url.Values, error) {
	filters := []url.Values{{}}
	for _, extra := range catalog.Extra {
		if !extra.IsRequired || extra.Name == "skip" {
			continue
		}
		options := extra.Options
		if len(options) == 0 {
			return nil, fmt.Errorf("stremio: catalog %q requires %q without browse options", catalog.ID, extra.Name)
		}
		for _, option := range options {
			if strings.EqualFold(option, "all") {
				options = []string{option}
				break
			}
		}
		if len(options) > 32/len(filters) {
			return nil, fmt.Errorf("stremio: catalog %q has too many required filter combinations", catalog.ID)
		}
		var next []url.Values
		for _, filter := range filters {
			for _, option := range options {
				values := url.Values{}
				for key, value := range filter {
					values[key] = append([]string(nil), value...)
				}
				values.Set(extra.Name, option)
				next = append(next, values)
			}
		}
		filters = next
	}
	return filters, nil
}

func fetchStremioCatalog(ctx context.Context, client *http.Client, baseURL string, catalog stremioCatalogDef) ([]stremioMeta, error) {
	filters, err := stremioCatalogFilters(catalog)
	if err != nil {
		return nil, err
	}
	supportsSkip := false
	for _, extra := range catalog.Extra {
		if strings.EqualFold(strings.TrimSpace(extra.Name), "skip") {
			supportsSkip = true
		}
	}
	var all []stremioMeta
	seen := make(map[string]bool)
	var firstErr error
	succeeded := false
	for _, filter := range filters {
		offset := 0
		pageSeen := make(map[string]bool)
		for page := 0; page < stremioMaxCatalogPages; page++ {
			endpoint := fmt.Sprintf("%s/catalog/%s/%s", baseURL, url.PathEscape(catalog.Type), url.PathEscape(catalog.ID))
			if page > 0 {
				filter.Set("skip", strconv.Itoa(offset))
			}
			if len(filter) > 0 {
				endpoint += "/" + strings.ReplaceAll(filter.Encode(), "+", "%20")
			}
			var resp stremioCatalogResponse
			if err := getStremioJSON(ctx, client, endpoint+".json", &resp); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				break // Preserve other filters and already loaded pages.
			}
			succeeded = true
			newItems := 0
			for _, meta := range resp.Metas {
				key := meta.Type + "\x00" + meta.ID
				if strings.TrimSpace(meta.ID) == "" {
					continue
				}
				if !pageSeen[key] {
					pageSeen[key] = true
					newItems++
				}
				if !seen[key] {
					seen[key] = true
					all = append(all, meta)
				}
			}
			// Page sizes vary between addons. Advance by the actual number returned,
			// and stop on empty/repeated pages if an addon ignores skip.
			offset += len(resp.Metas)
			if !supportsSkip || newItems == 0 {
				break
			}
		}
	}
	if !succeeded {
		return nil, firstErr
	}
	return all, nil
}

// resolveStremioStream fetches a stream resource and returns a playable stream.
// selectedIndex < 0 means highest reported quality; explicit indexes are preserved.
func (h *LiveHandler) resolveStremioStream(ctx context.Context, streamResourceURL, proxyURL string, selectedIndex int) (resolvedStremioStream, error) {
	if _, err := h.parseRemoteURL(ctx, streamResourceURL); err != nil {
		return resolvedStremioStream{}, fmt.Errorf("stremio: stream URL is not allowed")
	}
	client := h.liveStreamHTTPClient(proxyURL)
	var resp stremioStreamResponse
	if err := getStremioJSON(ctx, client, streamResourceURL, &resp); err != nil {
		return resolvedStremioStream{}, fmt.Errorf("stremio: resolve stream: %w", err)
	}
	if stream, ok := playableStremioStream(resp.Streams, selectedIndex); ok {
		return maybeRouteStremioStreamThroughAddonRelay(ctx, client, streamResourceURL, stream), nil
	}
	return resolvedStremioStream{}, fmt.Errorf("stremio: no playable stream for %s", streamResourceURL)
}

func (h *LiveHandler) GetStremioStreamOptions(w http.ResponseWriter, r *http.Request) {
	streamResourceURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if streamResourceURL == "" {
		http.Error(w, `{"error":"missing url parameter"}`, http.StatusBadRequest)
		return
	}
	parsed, err := h.parseRemoteURL(r.Context(), streamResourceURL)
	if err != nil {
		http.Error(w, `{"error":"invalid url parameter"}`, http.StatusBadRequest)
		return
	}
	if !isStremioStreamResourceURL(parsed) {
		http.Error(w, `{"error":"not a stremio stream resource"}`, http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	proxy := h.resolveProxyURLForStream(r, parsed)
	client := h.discoveryClient(h.liveStreamHTTPClientWithTimeout(proxy, 30*time.Second), proxy, 15*time.Second)
	var resp stremioStreamResponse
	if err := getStremioJSON(ctx, client, streamResourceURL, &resp); err != nil {
		log.Printf("[live] failed to fetch stremio stream options %q: %v", streamResourceURL, err)
		var upstream *providerMetadataError
		if errors.As(err, &upstream) && upstream.Status == 429 {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(time.Until(upstream.RetryAt).Seconds()))))
			http.Error(w, `{"error":"provider rate limited; retry after cooldown"}`, 429)
		} else {
			http.Error(w, `{"error":"failed to fetch stream options"}`, http.StatusBadGateway)
		}
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
		return metadataResponseError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
