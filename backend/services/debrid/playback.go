package debrid

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"novastream/config"
	"novastream/models"
)

// PlaybackService handles debrid playback resolution.
type PlaybackService struct {
	cfg           *config.Manager
	healthService *HealthService
}

// NewPlaybackService creates a new debrid playback service.
func NewPlaybackService(cfg *config.Manager, healthService *HealthService) *PlaybackService {
	if healthService == nil {
		healthService = NewHealthService(cfg)
	}
	return &PlaybackService{
		cfg:           cfg,
		healthService: healthService,
	}
}

// Resolve checks if a debrid item is cached and returns playback information.
// For debrid, we add the torrent, select files, and get the download link.
func (s *PlaybackService) Resolve(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error) {
	log.Printf("[debrid-playback] resolve start title=%q link=%q", strings.TrimSpace(candidate.Title), strings.TrimSpace(candidate.Link))

	// Check if this is a pre-resolved stream (e.g., from AIOStreams)
	// Pre-resolved streams already have a direct playback URL, no debrid resolution needed
	if candidate.Attributes["preresolved"] == "true" {
		streamURL := strings.TrimSpace(candidate.Attributes["stream_url"])
		if streamURL == "" {
			// Fallback: check TorrentURL field (where we stored the stream URL in the scraper)
			streamURL = strings.TrimSpace(candidate.Attributes["torrentURL"])
		}
		if streamURL == "" {
			return nil, fmt.Errorf("pre-resolved stream missing stream_url")
		}

		log.Printf("[debrid-playback] using pre-resolved stream URL: %s", streamURL)

		// Extract filename from attributes or URL
		filename := strings.TrimSpace(candidate.Attributes["raw_title"])
		if filename == "" {
			filename = strings.TrimSpace(candidate.Title)
		}

		// For pre-resolved streams, the WebDAV path is the direct URL
		// The video handler will detect this and stream directly
		resolution := &models.PlaybackResolution{
			QueueID:       0,
			WebDAVPath:    streamURL, // Direct stream URL
			HealthStatus:  "cached", // Use "cached" for frontend compatibility
			FileSize:      candidate.SizeBytes,
			SourceNZBPath: streamURL,
		}

		log.Printf("[debrid-playback] pre-resolved resolution: url=%s filename=%s", streamURL, filename)
		return resolution, nil
	}

	// Extract info hash from candidate (may be empty if using torrent file upload)
	infoHash := strings.TrimSpace(candidate.Attributes["infoHash"])
	if infoHash == "" {
		if strings.HasPrefix(strings.ToLower(candidate.Link), "magnet:") {
			infoHash = extractInfoHashFromMagnet(candidate.Link)
		}
	}

	// Check if we have a torrent URL (for cases without magnet/infohash)
	torrentURL := strings.TrimSpace(candidate.Attributes["torrentURL"])

	// We need either infohash/magnet or torrent URL
	hasMagnet := strings.HasPrefix(strings.ToLower(candidate.Link), "magnet:")
	if infoHash == "" && !hasMagnet && torrentURL == "" {
		return nil, fmt.Errorf("missing info hash and no torrent URL available")
	}

	// Get provider config
	settings, err := s.cfg.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}

	// Determine provider - use attribute if specified, otherwise use first enabled provider
	provider := strings.TrimSpace(candidate.Attributes["provider"])

	var providerConfig *config.DebridProviderSettings
	for i := range settings.Streaming.DebridProviders {
		p := &settings.Streaming.DebridProviders[i]
		if !p.Enabled {
			continue
		}
		// If provider specified, match it; otherwise use first enabled
		if provider == "" || strings.EqualFold(p.Provider, provider) {
			providerConfig = p
			break
		}
	}

	if providerConfig == nil {
		if provider == "" {
			return nil, fmt.Errorf("no debrid provider configured or enabled")
		}
		return nil, fmt.Errorf("provider %q not configured or not enabled", provider)
	}

	// Get provider from registry
	client, ok := GetProvider(strings.ToLower(providerConfig.Provider), providerConfig.APIKey)
	if !ok {
		return nil, fmt.Errorf("provider %q not registered", providerConfig.Provider)
	}

	return s.resolveWithProvider(ctx, client, candidate, infoHash, torrentURL)
}

func (s *PlaybackService) resolveWithProvider(ctx context.Context, client Provider, candidate models.NZBResult, infoHash, torrentURL string) (*models.PlaybackResolution, error) {
	providerName := client.Name()

	var addResp *AddMagnetResult
	var err error

	// Determine how to add the torrent: magnet link or torrent file upload
	if strings.HasPrefix(strings.ToLower(candidate.Link), "magnet:") {
		// Use magnet link
		log.Printf("[debrid-playback] adding magnet to %s", providerName)
		addResp, err = client.AddMagnet(ctx, candidate.Link)
		if err != nil {
			return nil, fmt.Errorf("add magnet: %w", err)
		}
	} else if torrentURL != "" {
		// Download and upload torrent file
		log.Printf("[debrid-playback] downloading torrent file from %s", torrentURL)
		torrentData, filename, downloadErr := s.downloadTorrentFile(ctx, torrentURL)
		if downloadErr != nil {
			return nil, fmt.Errorf("download torrent file: %w", downloadErr)
		}
		log.Printf("[debrid-playback] uploading torrent file (%d bytes) to %s", len(torrentData), providerName)
		addResp, err = client.AddTorrentFile(ctx, torrentData, filename)
		if err != nil {
			return nil, fmt.Errorf("add torrent file: %w", err)
		}
	} else {
		return nil, fmt.Errorf("no magnet link or torrent URL available")
	}

	torrentID := addResp.ID
	log.Printf("[debrid-playback] torrent added with ID %s", torrentID)

	// Get torrent info to see available files
	info, err := client.GetTorrentInfo(ctx, torrentID)
	if err != nil {
		return nil, fmt.Errorf("get torrent info: %w", err)
	}

	// Select the most relevant media file (but send all files to trigger caching)
	selection := selectMediaFiles(info.Files, buildSelectionHints(candidate, info.Filename))
	if selection == nil || len(selection.OrderedIDs) == 0 {
		_ = client.DeleteTorrent(ctx, torrentID)
		return nil, fmt.Errorf("no media files found in torrent")
	}

	if selection.PreferredID != "" {
		log.Printf("[debrid-playback] primary file candidate: %q (reason: %s, id=%s)", selection.PreferredLabel, selection.PreferredReason, selection.PreferredID)
	}

	fileSelection := strings.Join(selection.OrderedIDs, ",")
	log.Printf("[debrid-playback] selecting %d media files for caching: %s", len(selection.OrderedIDs), fileSelection)
	logSelectedFileDetails(info.Files, selection)

	if err := client.SelectFiles(ctx, torrentID, fileSelection); err != nil {
		_ = client.DeleteTorrent(ctx, torrentID)
		return nil, fmt.Errorf("select files: %w", err)
	}

	// Get torrent info again to get download links
	info, err = client.GetTorrentInfo(ctx, torrentID)
	if err != nil {
		_ = client.DeleteTorrent(ctx, torrentID)
		return nil, fmt.Errorf("get torrent info after selection: %w", err)
	}

	// Check if cached
	isCached := strings.ToLower(info.Status) == "downloaded"
	log.Printf("[debrid-playback] torrent %s status=%s cached=%t links=%d", torrentID, info.Status, isCached, len(info.Links))

	if !isCached {
		// Torrent is not cached - it may be downloading. We must remove it from the account
		// to avoid leaving orphaned downloads (especially important for Torbox).
		log.Printf("[debrid-playback] torrent %s is not cached (status=%s), removing from %s account", torrentID, info.Status, providerName)
		if err := client.DeleteTorrent(ctx, torrentID); err != nil {
			log.Printf("[debrid-playback] warning: failed to delete non-cached torrent %s: %v", torrentID, err)
		}
		return nil, fmt.Errorf("torrent not cached (status: %s)", info.Status)
	}

	if len(info.Links) == 0 {
		_ = client.DeleteTorrent(ctx, torrentID)
		return nil, fmt.Errorf("no download links available")
	}

	restrictedLink, filename, preferredLinkIdx, matched := resolveRestrictedLink(info, selection.PreferredID)
	if !matched && selection.PreferredID != "" {
		log.Printf("[debrid-playback] preferred file id %s not found among %s links; defaulting to index %d", selection.PreferredID, providerName, preferredLinkIdx)
	}
	if filename != "" {
		log.Printf("[debrid-playback] resolved filename: %s", filename)
	}

	downloadURL := restrictedLink
	if selection.PreferredLabel != "" {
		log.Printf("[debrid-playback] using download link #%d for %q (reason: %s)", preferredLinkIdx, selection.PreferredLabel, selection.PreferredReason)
	} else {
		log.Printf("[debrid-playback] using download link #%d for selected file (id=%s)", preferredLinkIdx, selection.PreferredID)
	}

	// Keep torrent in provider for playback
	// Note: We don't delete the torrent here because we need it for streaming
	log.Printf("[debrid-playback] keeping torrent %s in %s for playback", torrentID, providerName)

	// Return webdavPath as a path that the streaming provider can recognize
	// Format: /debrid/{provider}/TORRENT_ID[/file/ID][/FILENAME]
	// This works with both web (/api/video/stream?path=...) and mobile (direct URL)
	// We append the filename so it can be displayed in the player UI
	webdavPath := fmt.Sprintf("/debrid/%s/%s", providerName, torrentID)
	if selection.PreferredID != "" {
		webdavPath = fmt.Sprintf("%s/file/%s", webdavPath, selection.PreferredID)
	}
	// Append filename for display purposes (will be ignored by streaming provider)
	if filename != "" {
		webdavPath = fmt.Sprintf("%s/%s", webdavPath, filename)
	}

	// If the link is an actual URL (not an internal reference like torrent_id:file_id),
	// verify it's accessible and check for archives
	isActualURL := strings.HasPrefix(downloadURL, "http://") || strings.HasPrefix(downloadURL, "https://")

	if isActualURL {
		// Check for unsupported archives
		if archiveExt := detectArchiveExtension(downloadURL); archiveExt != "" {
			_ = client.DeleteTorrent(ctx, torrentID)
			return nil, fmt.Errorf("download URL points to unsupported archive (%s)", archiveExt)
		}

		// Verify the download URL is accessible with a HEAD request
		log.Printf("[debrid-playback] verifying download URL is accessible: %s", downloadURL)
		headReq, err := http.NewRequestWithContext(ctx, http.MethodHead, downloadURL, nil)
		if err != nil {
			_ = client.DeleteTorrent(ctx, torrentID)
			return nil, fmt.Errorf("failed to create HEAD request: %w", err)
		}

		headResp, err := http.DefaultClient.Do(headReq)
		if err != nil {
			_ = client.DeleteTorrent(ctx, torrentID)
			return nil, fmt.Errorf("download URL not accessible: %w", err)
		}
		defer headResp.Body.Close()

		if headResp.StatusCode >= 400 {
			_ = client.DeleteTorrent(ctx, torrentID)
			return nil, fmt.Errorf("download URL returned error status: %d %s", headResp.StatusCode, headResp.Status)
		}

		log.Printf("[debrid-playback] download URL verified accessible (status: %d)", headResp.StatusCode)
	} else {
		// For providers like Torbox that use internal references (torrent_id:file_id),
		// the actual URL is resolved at stream time via UnrestrictLink
		log.Printf("[debrid-playback] download link is internal reference, will be resolved at stream time: %s", downloadURL)
	}

	resolution := &models.PlaybackResolution{
		QueueID:       0, // Debrid doesn't use queues
		WebDAVPath:    webdavPath,
		HealthStatus:  "cached",
		FileSize:      candidate.SizeBytes,
		SourceNZBPath: downloadURL, // Store the actual download URL here
	}

	log.Printf("[debrid-playback] resolution successful: webdavPath=%s downloadURL=%s", webdavPath, downloadURL)
	return resolution, nil
}

func detectArchiveExtension(downloadURL string) string {
	if strings.TrimSpace(downloadURL) == "" {
		return ""
	}
	parsed, err := url.Parse(downloadURL)
	if err != nil {
		return ""
	}
	ext := strings.ToLower(path.Ext(parsed.Path))
	switch ext {
	case ".rar", ".zip", ".7z", ".tar", ".tar.gz", ".tgz":
		return ext
	default:
		return ""
	}
}

func logSelectedFileDetails(files []File, selection *mediaFileSelection) {
	if selection == nil {
		log.Printf("[debrid-playback] no media file selection available to log")
		return
	}

	if len(selection.OrderedIDs) == 0 {
		log.Printf("[debrid-playback] selection contained zero file IDs")
		return
	}

	fileLookup := make(map[string]File, len(files))
	for _, file := range files {
		fileLookup[fmt.Sprintf("%d", file.ID)] = file
	}

	log.Printf("[debrid-playback] detailed selected files (preferred id=%s):", selection.PreferredID)
	for idx, id := range selection.OrderedIDs {
		file, ok := fileLookup[id]
		preferred := selection.PreferredID == id
		if !ok {
			log.Printf("[debrid-playback]   #%d id=%s preferred=%t (details unavailable from provider)", idx+1, id, preferred)
			continue
		}

		sizeMB := float64(file.Bytes) / (1024 * 1024)
		log.Printf(
			"[debrid-playback]   #%d id=%s preferred=%t path=%q size=%d bytes (~%.2f MB) selected=%t",
			idx+1,
			id,
			preferred,
			file.Path,
			file.Bytes,
			sizeMB,
			file.Selected == 1,
		)
	}
}

// CheckHealthQuick performs a quick cached check without adding/removing torrents.
// This is useful for filtering search results or auto-selection.
func (s *PlaybackService) CheckHealthQuick(ctx context.Context, candidate models.NZBResult) (*DebridHealthCheck, error) {
	// Quick check - don't verify by adding
	return s.healthService.CheckHealth(ctx, candidate, false)
}

// FilterCachedResults filters a list of results to only include cached debrid items.
// This is useful for auto-selection or pre-filtering search results.
// Only checks the first 3 results to minimize API calls.
func (s *PlaybackService) FilterCachedResults(ctx context.Context, results []models.NZBResult) []models.NZBResult {
	var cached []models.NZBResult

	log.Printf("[debrid-playback] filtering %d results for cached items (checking first 3 only)", len(results))

	checked := 0
	for i, result := range results {
		// Only check debrid items
		if result.ServiceType != models.ServiceTypeDebrid {
			log.Printf("[debrid-playback] [%d/%d] skipping non-debrid result: %s", i+1, len(results), result.Title)
			continue
		}

		// Only check first 3 debrid results to minimize API calls
		if checked >= 3 {
			log.Printf("[debrid-playback] reached limit of 3 health checks, skipping remaining results")
			break
		}
		checked++

		health, err := s.CheckHealthQuick(ctx, result)
		if err != nil {
			log.Printf("[debrid-playback] [%d/%d] health check failed for %s: %v", i+1, len(results), result.Title, err)
			continue
		}

		if health == nil {
			log.Printf("[debrid-playback] [%d/%d] health check returned nil for %s", i+1, len(results), result.Title)
			continue
		}

		if health.Status == "error" && health.ErrorMessage != "" {
			log.Printf("[debrid-playback] [%d/%d] %s: healthy=%t cached=%t status=%s error=%q",
				i+1, len(results), result.Title, health.Healthy, health.Cached, health.Status, health.ErrorMessage)
		} else {
			log.Printf("[debrid-playback] [%d/%d] %s: healthy=%t cached=%t status=%s",
				i+1, len(results), result.Title, health.Healthy, health.Cached, health.Status)
		}

		if health.Healthy && health.Cached {
			cached = append(cached, result)
		}
	}

	log.Printf("[debrid-playback] filtered results: %d cached out of %d checked", len(cached), checked)
	return cached
}

// downloadTorrentFile downloads a .torrent file from a URL and returns its contents.
func (s *PlaybackService) downloadTorrentFile(ctx context.Context, torrentURL string) ([]byte, string, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, torrentURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("create request: %w", err)
	}

	// Set common headers that some trackers expect
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; strmr/1.0)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	// Limit torrent file size to 10MB (should be more than enough)
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, "", fmt.Errorf("read response: %w", err)
	}

	// Verify it looks like a torrent file (starts with "d" for bencoded dictionary)
	if len(data) < 10 || data[0] != 'd' {
		return nil, "", fmt.Errorf("invalid torrent file format (expected bencoded data)")
	}

	// Extract filename from URL or Content-Disposition header
	filename := extractTorrentFilename(resp, torrentURL)

	log.Printf("[debrid-playback] downloaded torrent file: %s (%d bytes)", filename, len(data))
	return data, filename, nil
}

// extractTorrentFilename tries to get a filename for the torrent file.
func extractTorrentFilename(resp *http.Response, torrentURL string) string {
	// Try Content-Disposition header first
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if strings.Contains(cd, "filename=") {
			parts := strings.Split(cd, "filename=")
			if len(parts) >= 2 {
				filename := strings.Trim(parts[1], `"' `)
				if filename != "" {
					return filename
				}
			}
		}
	}

	// Try to extract from URL path
	if parsed, err := url.Parse(torrentURL); err == nil {
		filename := path.Base(parsed.Path)
		if filename != "" && filename != "." && filename != "/" {
			if !strings.HasSuffix(strings.ToLower(filename), ".torrent") {
				filename += ".torrent"
			}
			return filename
		}
	}

	return "download.torrent"
}
