package debrid

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/internal/dnscache"
	"novastream/internal/torboxrate"
	"novastream/services/streaming"
)

// isStaleTorrentError returns true if the error indicates the torrent ID
// no longer exists on the debrid provider (expired, deleted, or invalid).
func isStaleTorrentError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// AllDebrid: "This magnet ID does not exists or is invalid"
	// RealDebrid: "torrent not found" / "bad token" on expired IDs
	// Torbox: "not found in Torbox response"
	return strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "magnet id") ||
		(strings.Contains(msg, "torrent") && strings.Contains(msg, "not found"))
}

// readdFromRegistry looks up the magnet link for a stale torrent and re-adds it.
// Returns the new torrent ID on success.
func (p *StreamingProvider) readdFromRegistry(ctx context.Context, client Provider, provider, oldTorrentID string) (string, error) {
	magnetLink, ok := LookupMagnet(provider, oldTorrentID)
	if !ok {
		return "", fmt.Errorf("no magnet registered for %s torrent %s", provider, oldTorrentID)
	}

	log.Printf("[debrid-stream] re-adding magnet for stale torrent %s on %s", oldTorrentID, provider)
	addResp, err := client.AddMagnet(ctx, magnetLink)
	if err != nil {
		return "", fmt.Errorf("re-add magnet failed: %w", err)
	}

	newID := addResp.ID
	// Register the new torrent ID with the same magnet
	RegisterMagnet(provider, newID, magnetLink)

	log.Printf("[debrid-stream] re-added stale torrent %s as %s on %s", oldTorrentID, newID, provider)
	return newID, nil
}

// cachedURL represents a cached unrestricted URL with expiration.
type cachedURL struct {
	url       string
	filename  string
	expiresAt time.Time
	rarOffset int64 // 0 = normal file, >0 = byte offset within RAR
	rarSize   int64 // 0 = normal file, >0 = file size within RAR
}

// StreamingProvider implements streaming.Provider for debrid content.
type StreamingProvider struct {
	cfg        *config.Manager
	urlCache   map[string]cachedURL
	cacheMux   sync.RWMutex
	cacheTTL   time.Duration
	httpClient *http.Client
}

func parseDebridPath(path string) (provider, torrentID, fileID string, err error) {
	trimmed := strings.TrimSpace(path)
	if idx := strings.Index(trimmed, "?"); idx != -1 {
		trimmed = trimmed[:idx]
	}
	if idx := strings.Index(trimmed, "#"); idx != -1 {
		trimmed = trimmed[:idx]
	}
	if !strings.HasPrefix(trimmed, "/debrid/") {
		err = fmt.Errorf("invalid debrid path format: %s", path)
		return
	}

	segments := strings.Split(strings.TrimPrefix(trimmed, "/debrid/"), "/")
	if len(segments) < 2 {
		err = fmt.Errorf("invalid debrid path format: %s", path)
		return
	}

	provider = segments[0]
	torrentID = segments[1]

	if len(segments) >= 4 && segments[2] == "file" {
		fileID = segments[3]
	}

	return
}

func cacheKeyFor(torrentID, fileID string) string {
	if strings.TrimSpace(fileID) == "" {
		return torrentID
	}
	return fmt.Sprintf("%s:%s", torrentID, fileID)
}

// NewStreamingProvider creates a new debrid streaming provider.
func NewStreamingProvider(cfg *config.Manager) *StreamingProvider {
	transport := &http.Transport{
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		MaxIdleConnsPerHost:   8,
		MaxConnsPerHost:       0, // unlimited
		IdleConnTimeout:       5 * time.Minute,
		ResponseHeaderTimeout: 15 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	dnscache.ConfigureTransport(transport, dnscache.DefaultTTL)

	return &StreamingProvider{
		cfg:      cfg,
		urlCache: make(map[string]cachedURL),
		cacheTTL: 10 * time.Minute,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   0, // no overall timeout; context handles cancellation
		},
	}
}

// downloadClient returns the HTTP client for fetching providerName's CDN
// links: the shared direct client, or a pooled client through the
// provider's proxy. A misconfigured proxy is an error (fail closed).
func (p *StreamingProvider) downloadClient(providerName string) (*http.Client, error) {
	if p.cfg == nil {
		// No settings means no provider (and so no proxy) is configured.
		return p.httpClient, nil
	}
	settings, err := p.cfg.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	return streamHTTPClientFor(p.httpClient, &settings, providerName)
}

// getCachedURL retrieves a cached unrestricted URL if it exists and hasn't expired.
func (p *StreamingProvider) getCachedURL(cacheKey string) (url string, filename string, rarOffset int64, rarSize int64, found bool) {
	p.cacheMux.RLock()
	defer p.cacheMux.RUnlock()

	cached, exists := p.urlCache[cacheKey]
	if !exists {
		return "", "", 0, 0, false
	}

	if time.Now().After(cached.expiresAt) {
		return "", "", 0, 0, false
	}

	return cached.url, cached.filename, cached.rarOffset, cached.rarSize, true
}

// setCachedURL stores an unrestricted URL in the cache.
func (p *StreamingProvider) setCachedURL(cacheKey, url, filename string, rarOffset, rarSize int64) {
	p.cacheMux.Lock()
	defer p.cacheMux.Unlock()

	p.urlCache[cacheKey] = cachedURL{
		url:       url,
		filename:  filename,
		expiresAt: time.Now().Add(p.cacheTTL),
		rarOffset: rarOffset,
		rarSize:   rarSize,
	}

	// Clean up expired entries while we have the lock
	for id, cached := range p.urlCache {
		if time.Now().After(cached.expiresAt) {
			delete(p.urlCache, id)
		}
	}
}

func (p *StreamingProvider) evictCachedURL(cacheKey string) bool {
	p.cacheMux.Lock()
	defer p.cacheMux.Unlock()

	if _, exists := p.urlCache[cacheKey]; !exists {
		return false
	}
	delete(p.urlCache, cacheKey)
	return true
}

// shouldRetryFailedURL reports whether a failed download URL should be
// dropped and re-resolved once. URLs from this proxy's cache always qualify.
// AllDebrid URLs also qualify on a proxy cache miss because the AllDebrid
// client keeps its own longer-lived unlock cache, which may have handed back
// a link that has since died.
func (p *StreamingProvider) shouldRetryFailedURL(providerName string, fromCache, retryCachedFailure bool) bool {
	if !retryCachedFailure {
		return false
	}
	return fromCache || strings.EqualFold(providerName, "alldebrid")
}

// forgetFailedURL drops a download URL that was proven unusable from every
// cache that could hand it out again.
func (p *StreamingProvider) forgetFailedURL(providerName, cacheKey, downloadURL string) {
	p.evictCachedURL(cacheKey)
	if strings.EqualFold(providerName, "alldebrid") {
		InvalidateAllDebridDownloadURL(downloadURL)
	}
}

// GetDirectURL returns the unrestricted HTTP download URL for the given debrid path.
// This URL can be used directly by FFmpeg for seekable input.
func (p *StreamingProvider) GetDirectURL(ctx context.Context, path string) (string, error) {
	provider, torrentID, fileID, err := parseDebridPath(path)
	if err != nil {
		return "", err
	}

	// Check cache first
	cacheKey := cacheKeyFor(torrentID, fileID)

	// Get provider config
	settings, err := p.cfg.Load()
	if err != nil {
		return "", fmt.Errorf("load settings: %w", err)
	}

	// Find the provider configuration
	var providerConfig *config.DebridProviderSettings
	for i := range settings.Streaming.DebridProviders {
		debridProvider := &settings.Streaming.DebridProviders[i]
		if strings.EqualFold(debridProvider.Provider, provider) && debridProvider.Enabled && strings.TrimSpace(debridProvider.APIKey) != "" {
			providerConfig = debridProvider
			break
		}
	}

	if providerConfig == nil {
		return "", fmt.Errorf("provider %s not configured or not enabled", provider)
	}

	// A proxied provider never hands out its CDN URL: FFmpeg, probes and
	// client apps would fetch it directly and bypass the proxy. Fail closed
	// on a misconfigured proxy too.
	proxyURL, err := providerConfig.EffectiveProxyURL()
	if err != nil {
		return "", err
	}
	proxied := proxyURL != ""

	if cachedURL, _, rarOffset, _, found := p.getCachedURL(cacheKey); found {
		if rarOffset > 0 {
			// RAR-packed files must go through the proxy for range translation
			return "", fmt.Errorf("RAR-packed file requires proxy streaming")
		}
		if proxied {
			return "", ErrDirectURLProxied
		}
		log.Printf("[debrid-stream] using cached URL for torrent %s file %s", torrentID, fileID)
		return cachedURL, nil
	}

	client, err := NewProviderFromSettings(*providerConfig)
	if err != nil {
		return "", err
	}

	info, err := client.GetTorrentInfo(ctx, torrentID)
	if err != nil && isStaleTorrentError(err) {
		newID, retryErr := p.readdFromRegistry(ctx, client, provider, torrentID)
		if retryErr != nil {
			log.Printf("[debrid-stream] stale torrent %s (direct URL) and re-add failed: %v (original: %v)", torrentID, retryErr, err)
			return "", fmt.Errorf("%w: %v", streaming.ErrStaleTorrent, err)
		}
		log.Printf("[debrid-stream] stale torrent %s re-added as %s (direct URL), retrying", torrentID, newID)
		torrentID = newID
		cacheKey = cacheKeyFor(torrentID, fileID)
		info, err = client.GetTorrentInfo(ctx, torrentID)
	}
	if err != nil {
		return "", fmt.Errorf("get torrent info: %w", err)
	}

	// RAR-packed torrents can't use direct URLs — force proxy path
	if isRARPacked(info) {
		return "", fmt.Errorf("RAR-packed file requires proxy streaming")
	}

	restrictedLink, filename, _, matched := resolveRestrictedLink(info, fileID)
	if restrictedLink == "" {
		return "", fmt.Errorf("no download links available for torrent %s", torrentID)
	}
	if fileID != "" && !matched {
		log.Printf("[debrid-stream] requested file id %s not found for torrent %s; defaulting to first link", fileID, torrentID)
	}
	if filename != "" {
		log.Printf("[debrid-stream] resolved filename: %s", filename)
	}

	unrestricted, err := client.UnrestrictLink(ctx, restrictedLink)
	if err != nil {
		return "", fmt.Errorf("unrestrict link: %w", err)
	}

	downloadURL := unrestricted.DownloadURL
	if downloadURL == "" {
		return "", fmt.Errorf("no download URL returned from provider")
	}

	// Cache the URL and filename for future requests
	p.setCachedURL(cacheKey, downloadURL, filename, 0, 0)

	if proxied {
		// The link stays cached for Stream, which fetches it through the proxy.
		log.Printf("[debrid-stream] resolved URL for path %s behind provider proxy; not exposing direct URL", path)
		return "", ErrDirectURLProxied
	}

	log.Printf("[debrid-stream] resolved direct URL for path %s: %s (filename: %s)", path, safeURLForLog(downloadURL), filename)
	return downloadURL, nil
}

// Stream handles /debrid/ paths by proxying to Real-Debrid download links.
func (p *StreamingProvider) Stream(ctx context.Context, req streaming.Request) (*streaming.Response, error) {
	// Only handle /debrid/ paths
	// Normalize path by removing leading slashes and webdav prefix
	cleanPath := strings.TrimPrefix(req.Path, "/")
	cleanPath = strings.TrimPrefix(cleanPath, "webdav/")

	if !strings.HasPrefix(cleanPath, "debrid/") {
		return nil, streaming.ErrNotFound
	}

	provider, torrentID, fileID, err := parseDebridPath("/" + cleanPath)
	if err != nil {
		return nil, err
	}

	log.Printf("[debrid-stream] streaming request: provider=%s torrentID=%s fileID=%s method=%s range=%q",
		provider, torrentID, fileID, req.Method, req.RangeHeader)

	// Get provider config
	settings, err := p.cfg.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}

	var providerConfig *config.DebridProviderSettings
	for i := range settings.Streaming.DebridProviders {
		pc := &settings.Streaming.DebridProviders[i]
		if !pc.Enabled {
			continue
		}
		if strings.EqualFold(pc.Provider, provider) {
			providerConfig = pc
			break
		}
	}

	if providerConfig == nil {
		return nil, fmt.Errorf("provider %q not configured", provider)
	}

	client, err := NewProviderFromSettings(*providerConfig)
	if err != nil {
		return nil, err
	}

	return p.streamWithProvider(ctx, req, client, torrentID, fileID, true)
}

func (p *StreamingProvider) streamWithProvider(ctx context.Context, req streaming.Request, client Provider, torrentID, fileID string, retryCachedFailure bool) (*streaming.Response, error) {
	return p.streamWithProviderInfo(ctx, req, client, torrentID, fileID, retryCachedFailure, nil)
}

func (p *StreamingProvider) streamWithProviderInfo(ctx context.Context, req streaming.Request, client Provider, torrentID, fileID string, retryCachedFailure bool, prefetchedInfo *TorrentInfo) (*streaming.Response, error) {
	providerName := client.Name()
	httpClient, err := p.downloadClient(providerName)
	if err != nil {
		return nil, err
	}
	var downloadURL string
	var filename string
	var rarOffset, rarSize int64
	var fromCache bool

	cacheKey := cacheKeyFor(torrentID, fileID)

	// Check cache first
	if cachedURL, cachedFilename, cachedRarOffset, cachedRarSize, found := p.getCachedURL(cacheKey); found {
		log.Printf("[debrid-stream] using cached URL for torrent %s file %s", torrentID, fileID)
		downloadURL = cachedURL
		filename = cachedFilename
		rarOffset = cachedRarOffset
		rarSize = cachedRarSize
		fromCache = true
	} else {
		// Cache miss - need to unrestrict the link
		// Get fresh torrent info to get download links
		info := prefetchedInfo
		var err error
		if info == nil {
			info, err = client.GetTorrentInfo(ctx, torrentID)
		}
		if err != nil && isStaleTorrentError(err) {
			// Torrent expired/deleted — try to re-add from magnet registry
			newID, retryErr := p.readdFromRegistry(ctx, client, providerName, torrentID)
			if retryErr != nil {
				log.Printf("[debrid-stream] stale torrent %s and re-add failed: %v (original: %v)", torrentID, retryErr, err)
				return nil, fmt.Errorf("%w: %v", streaming.ErrStaleTorrent, err)
			}
			// Retry with new torrent ID
			log.Printf("[debrid-stream] stale torrent %s re-added as %s, retrying", torrentID, newID)
			torrentID = newID
			cacheKey = cacheKeyFor(torrentID, fileID)
			info, err = client.GetTorrentInfo(ctx, torrentID)
		}
		if err != nil {
			return nil, fmt.Errorf("get torrent info: %w", err)
		}

		restrictedLink, resolvedFilename, _, matched := resolveRestrictedLink(info, fileID)
		if restrictedLink == "" {
			return nil, fmt.Errorf("no download links available for torrent %s", torrentID)
		}
		if fileID != "" && !matched {
			log.Printf("[debrid-stream] requested file id %s not available for torrent %s; defaulting to first link", fileID, torrentID)
		}
		if resolvedFilename != "" {
			log.Printf("[debrid-stream] resolved filename: %s", resolvedFilename)
		}

		log.Printf("[debrid-stream] unrestricting link: %s", safeURLForLog(restrictedLink))

		// Unrestrict the link to get the actual download URL
		unrestricted, err := client.UnrestrictLink(ctx, restrictedLink)
		if err != nil {
			return nil, fmt.Errorf("unrestrict link: %w", err)
		}

		// Use the direct download URL
		downloadURL = unrestricted.DownloadURL

		// Use filename from provider if available, otherwise use the resolved filename from torrent info
		if unrestricted.Filename != "" {
			filename = unrestricted.Filename
		} else {
			filename = resolvedFilename
		}

		// Detect RAR-packed torrents and parse file offsets
		if isRARPacked(info) {
			log.Printf("[debrid-stream] RAR file detected for torrent %s, parsing headers", torrentID)
			entries, parseErr := parseRAR4StoredFiles(ctx, httpClient, downloadURL)
			if parseErr != nil {
				log.Printf("[debrid-stream] RAR parse failed: %v — falling back to raw stream", parseErr)
			} else {
				for i, e := range entries {
					log.Printf("[debrid-stream] RAR entry[%d]: %q offset=%d size=%d", i, e.Name, e.DataOffset, e.Size)
				}
				entry := findRAREntry(entries, resolvedFilename)
				if entry != nil {
					rarOffset = entry.DataOffset
					rarSize = entry.Size
					filename = filepath.Base(entry.Name)
					log.Printf("[debrid-stream] RAR entry matched: %q → offset=%d size=%d", resolvedFilename, rarOffset, rarSize)
				} else {
					log.Printf("[debrid-stream] RAR entry not found for %q in %d entries", resolvedFilename, len(entries))
				}
			}
		}

		// Cache the URL and filename for future requests
		p.setCachedURL(cacheKey, downloadURL, filename, rarOffset, rarSize)

		log.Printf("[debrid-stream] proxying to unrestricted URL: %s", safeURLForLog(downloadURL))
	}

	// Create HTTP request to the provider
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Translate range headers for RAR-packed files
	if rarOffset > 0 && rarSize > 0 {
		if req.RangeHeader != "" {
			httpReq.Header.Set("Range", translateRangeForRAR(req.RangeHeader, rarOffset, rarSize))
		} else if req.Method == http.MethodGet {
			// No range header on GET — request just the file's extent within the RAR
			httpReq.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", rarOffset, rarOffset+rarSize-1))
		}
	} else if req.RangeHeader != "" {
		// Normal (non-RAR) range forwarding
		httpReq.Header.Set("Range", req.RangeHeader)
	}

	// Use shared HTTP client with connection pooling — rapid seek requests
	// reuse warm TCP+TLS connections to the CDN instead of handshaking each time
	var resp *http.Response
	isTorboxDownload := strings.EqualFold(providerName, "torbox") && torboxrate.IsDownloadURL(downloadURL)
	for attempt := 0; ; attempt++ {
		if isTorboxDownload {
			if err := torboxrate.Downloads.Wait(ctx, downloadURL); err != nil {
				return nil, &SourceError{Provider: providerName, URL: downloadURL, Err: err}
			}
		}

		resp, err = httpClient.Do(httpReq.Clone(ctx))
		if err != nil || !isTorboxDownload || resp.StatusCode != http.StatusTooManyRequests {
			break
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		delay := torboxrate.Downloads.Record(downloadURL, resp.Header.Get("Retry-After"), body)
		if attempt >= 1 {
			return nil, &SourceError{
				Provider:   providerName,
				URL:        downloadURL,
				StatusCode: resp.StatusCode,
				Status:     resp.Status,
				Body:       string(body),
			}
		}
		log.Printf("[debrid-stream] TorBox CDN rate limited torrent %s file %s; retrying same URL after %s", torrentID, fileID, delay.Round(time.Millisecond))
	}
	if err != nil {
		// The caller went away (player seek/stop). The URL is not proven bad, so
		// keep it cached: evicting here forced a fresh unlock on the next range
		// request, which on AllDebrid counts as another generated link.
		if ctx.Err() != nil {
			return nil, &SourceError{Provider: providerName, URL: downloadURL, Err: err}
		}
		if p.shouldRetryFailedURL(providerName, fromCache, retryCachedFailure) {
			p.forgetFailedURL(providerName, cacheKey, downloadURL)
			log.Printf("[debrid-stream] dropped URL for torrent %s file %s after request failure: %v", torrentID, fileID, err)
			return p.streamWithProvider(ctx, req, client, torrentID, fileID, false)
		}
		return nil, &SourceError{
			Provider: providerName,
			URL:      downloadURL,
			Err:      err,
		}
	}

	// For failed requests, close and return error
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable && p.shouldRetryFailedURL(providerName, fromCache, retryCachedFailure) {
			p.forgetFailedURL(providerName, cacheKey, downloadURL)
			log.Printf("[debrid-stream] dropped URL for torrent %s file %s after %s response: %s", torrentID, fileID, providerName, resp.Status)
			return p.streamWithProvider(ctx, req, client, torrentID, fileID, false)
		}
		return nil, &SourceError{
			Provider:   providerName,
			URL:        downloadURL,
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       string(body),
		}
	}

	log.Printf("[debrid-stream] %s response: status=%d content-length=%d range=%q",
		providerName, resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Range"))

	// Build streaming response
	headers := make(http.Header)
	for key, values := range resp.Header {
		for _, value := range values {
			headers.Add(key, value)
		}
	}

	// Ensure Accept-Ranges header is set
	if headers.Get("Accept-Ranges") == "" {
		headers.Set("Accept-Ranges", "bytes")
	}

	// Rewrite response headers for RAR-packed files
	contentLength := resp.ContentLength
	if rarOffset > 0 && rarSize > 0 {
		if cr := headers.Get("Content-Range"); cr != "" {
			headers.Set("Content-Range", rewriteContentRangeForRAR(cr, rarOffset, rarSize))
		}
		if contentLength > 0 {
			// Content-Length from CDN is already correct for the range we requested
			// but for a full GET without client range, report the file size
		}
	}

	// Handle HEAD requests - close body immediately
	if req.Method == http.MethodHead {
		resp.Body.Close()
		headContentLength := contentLength
		if rarOffset > 0 && rarSize > 0 {
			headContentLength = rarSize
		}
		return &streaming.Response{
			Status:        resp.StatusCode,
			Headers:       headers,
			ContentLength: headContentLength,
			Body:          io.NopCloser(strings.NewReader("")),
			Filename:      filename,
		}, nil
	}

	return &streaming.Response{
		Status:        resp.StatusCode,
		Headers:       headers,
		ContentLength: contentLength,
		Body:          &drainOnCloseBody{body: resp.Body},
		Filename:      filename,
	}, nil
}

// StreamRelatedFile locates and streams a companion file from the same debrid
// torrent without requiring callers to know its provider-specific file ID.
func (p *StreamingProvider) StreamRelatedFile(ctx context.Context, sourcePath, relatedPath string) (*streaming.Response, error) {
	providerName, torrentID, _, err := parseDebridPath(sourcePath)
	if err != nil {
		return nil, streaming.ErrNotFound
	}

	settings, err := p.cfg.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}

	var providerConfig *config.DebridProviderSettings
	for i := range settings.Streaming.DebridProviders {
		candidate := &settings.Streaming.DebridProviders[i]
		if candidate.Enabled && strings.EqualFold(candidate.Provider, providerName) {
			providerConfig = candidate
			break
		}
	}
	if providerConfig == nil {
		return nil, streaming.ErrNotFound
	}

	client, err := NewProviderFromSettings(*providerConfig)
	if err != nil {
		return nil, err
	}

	info, err := client.GetTorrentInfo(ctx, torrentID)
	if err != nil {
		return nil, fmt.Errorf("get torrent info: %w", err)
	}

	target := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(relatedPath)), "/")
	fileID := ""
	for _, file := range info.Files {
		candidate := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(file.Path)), "/")
		if strings.EqualFold(candidate, target) {
			fileID = strconv.Itoa(file.ID)
			break
		}
	}
	if fileID == "" {
		return nil, streaming.ErrNotFound
	}

	return p.streamWithProviderInfo(ctx, streaming.Request{
		Path:   sourcePath,
		Method: http.MethodGet,
	}, client, torrentID, fileID, true, info)
}

// drainOnCloseBody wraps an io.ReadCloser so that on Close(), it drains a small
// amount of unread data in the background instead of immediately closing the
// connection. With HTTP/1.1 CDNs (like Real-Debrid), an unread response body
// forces the TCP connection to be destroyed. By draining briefly, we allow the
// connection to return to the pool for reuse by the next range request,
// eliminating the ~500ms TLS handshake cost during rapid seek storms.
type drainOnCloseBody struct {
	body io.ReadCloser
}

func (d *drainOnCloseBody) Read(p []byte) (int, error) {
	return d.body.Read(p)
}

const (
	drainMaxBytes = 2 * 1024 * 1024 // drain up to 2MB
	drainTimeout  = 2 * time.Second
)

func (d *drainOnCloseBody) Close() error {
	go func() {
		defer d.body.Close()
		timer := time.NewTimer(drainTimeout)
		defer timer.Stop()

		done := make(chan struct{})
		go func() {
			// Discard up to drainMaxBytes to return the connection to the pool
			io.CopyN(io.Discard, d.body, drainMaxBytes)
			close(done)
		}()

		select {
		case <-done:
			// Drained successfully — connection returns to pool
		case <-timer.C:
			// Took too long — just close (kills connection)
			d.body.Close()
		}
	}()
	return nil
}

// CompositeProvider combines multiple streaming providers.
type CompositeProvider struct {
	providers []streaming.Provider
}

// NewCompositeProvider creates a new composite provider.
func NewCompositeProvider(providers ...streaming.Provider) *CompositeProvider {
	return &CompositeProvider{providers: providers}
}

// GetDirectURL tries to get a direct URL from any provider that supports it.
func (c *CompositeProvider) GetDirectURL(ctx context.Context, path string) (string, error) {
	for _, provider := range c.providers {
		if provider == nil {
			continue
		}

		// Check if this provider supports direct URLs
		directProvider, ok := provider.(streaming.DirectURLProvider)
		if !ok {
			continue
		}

		url, err := directProvider.GetDirectURL(ctx, path)
		if err == nil && url != "" {
			return url, nil
		}

		// If not found, try next provider
		if err == streaming.ErrNotFound {
			continue
		}

		// For other errors, continue trying other providers
	}

	return "", streaming.ErrNotFound
}

// GetDuration asks the provider that owns path for its catalog duration.
func (c *CompositeProvider) GetDuration(ctx context.Context, path string) (float64, error) {
	for _, provider := range c.providers {
		durationProvider, ok := provider.(streaming.DurationProvider)
		if !ok {
			continue
		}
		duration, err := durationProvider.GetDuration(ctx, path)
		if err == nil && duration > 0 {
			return duration, nil
		}
		if err != nil && !errors.Is(err, streaming.ErrNotFound) {
			return 0, err
		}
	}
	return 0, streaming.ErrNotFound
}

// IsGrowing asks the provider that owns path whether the source is still
// being written (a running recording).
func (c *CompositeProvider) IsGrowing(ctx context.Context, path string) (bool, error) {
	for _, provider := range c.providers {
		growingProvider, ok := provider.(streaming.GrowingSourceProvider)
		if !ok {
			continue
		}
		growing, err := growingProvider.IsGrowing(ctx, path)
		if err == nil {
			return growing, nil
		}
		if !errors.Is(err, streaming.ErrNotFound) {
			return false, err
		}
	}
	return false, streaming.ErrNotFound
}

// StreamRelatedFile asks each capable provider for a companion file belonging
// to the same source collection.
func (c *CompositeProvider) StreamRelatedFile(ctx context.Context, sourcePath, relatedPath string) (*streaming.Response, error) {
	for _, provider := range c.providers {
		relatedProvider, ok := provider.(streaming.RelatedFileProvider)
		if !ok {
			continue
		}

		resp, err := relatedProvider.StreamRelatedFile(ctx, sourcePath, relatedPath)
		if err == nil {
			return resp, nil
		}
		if !errors.Is(err, streaming.ErrNotFound) {
			return nil, err
		}
	}
	return nil, streaming.ErrNotFound
}

// Stream tries each provider in order until one handles the request.
func (c *CompositeProvider) Stream(ctx context.Context, req streaming.Request) (*streaming.Response, error) {
	for _, provider := range c.providers {
		if provider == nil {
			continue
		}

		resp, err := provider.Stream(ctx, req)
		if err == nil {
			return resp, nil
		}

		// If not found, try next provider
		if err == streaming.ErrNotFound {
			continue
		}

		// For other errors, return immediately
		return nil, err
	}

	return nil, streaming.ErrNotFound
}
