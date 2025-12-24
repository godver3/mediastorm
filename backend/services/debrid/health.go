package debrid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"path"
	"strings"
	"time"

	"novastream/config"
	"novastream/internal/mediaresolve"
	"novastream/models"
)

// HealthService checks debrid item health by verifying cached status.
type HealthService struct {
	cfg         *config.Manager
	ffprobePath string
}

// NewHealthService creates a new debrid health check service.
func NewHealthService(cfg *config.Manager) *HealthService {
	return &HealthService{cfg: cfg}
}

// SetFFProbePath sets the ffprobe path for probing pre-resolved streams.
func (s *HealthService) SetFFProbePath(path string) {
	s.ffprobePath = path
}

// DebridHealthCheck represents the health status of a debrid item.
type DebridHealthCheck struct {
	Healthy      bool   `json:"healthy"`
	Status       string `json:"status"`
	Cached       bool   `json:"cached"`
	Provider     string `json:"provider"`
	InfoHash     string `json:"infoHash,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// CheckHealth verifies if a debrid result is healthy (cached and available).
// For Real-Debrid, this checks instant availability.
// For uncached items, we optionally add+check+remove to verify.
func (s *HealthService) CheckHealth(ctx context.Context, result models.NZBResult, verifyUncached bool) (*DebridHealthCheck, error) {
	if s.cfg == nil {
		return nil, fmt.Errorf("health service not configured")
	}

	// Check if this is a pre-resolved stream (e.g., from AIOStreams)
	// Pre-resolved streams need to be probed to check if they're actually cached
	if result.Attributes["preresolved"] == "true" {
		streamURL := result.Attributes["stream_url"]
		if streamURL == "" {
			streamURL = result.Link
		}
		log.Printf("[debrid-health] checking pre-resolved stream: %s", result.Title)

		// First, do a quick HEAD request to check if the URL is accessible
		// This catches 404s and other HTTP errors quickly without waiting for ffprobe
		if streamURL != "" {
			headCtx, headCancel := context.WithTimeout(ctx, 10*time.Second)
			defer headCancel()

			headReq, err := http.NewRequestWithContext(headCtx, http.MethodHead, streamURL, nil)
			if err == nil {
				headReq.Header.Set("User-Agent", "Mozilla/5.0 (compatible; strmr/1.0)")
				if resp, err := http.DefaultClient.Do(headReq); err == nil {
					resp.Body.Close()
					if resp.StatusCode == http.StatusNotFound {
						log.Printf("[debrid-health] pre-resolved stream %s returned 404 - treating as uncached", result.Title)
						return &DebridHealthCheck{
							Healthy:      false,
							Status:       "not_cached",
							Cached:       false,
							Provider:     result.Attributes["tracker"],
							ErrorMessage: "stream returned 404 (not found)",
						}, nil
					}
					if resp.StatusCode >= 400 {
						log.Printf("[debrid-health] pre-resolved stream %s returned HTTP %d - treating as uncached", result.Title, resp.StatusCode)
						return &DebridHealthCheck{
							Healthy:      false,
							Status:       "not_cached",
							Cached:       false,
							Provider:     result.Attributes["tracker"],
							ErrorMessage: fmt.Sprintf("stream returned HTTP %d", resp.StatusCode),
						}, nil
					}
				} else {
					log.Printf("[debrid-health] HEAD request failed for pre-resolved stream %s: %v", result.Title, err)
				}
			}
		}

		// If we have ffprobe, verify the stream has audio (placeholder videos have 0 audio streams)
		if s.ffprobePath != "" && streamURL != "" {
			audioCount, err := s.probeAudioStreamCount(ctx, streamURL)
			if err != nil {
				log.Printf("[debrid-health] probe failed for pre-resolved stream %s: %v", result.Title, err)
				// On probe failure, treat as potentially uncached
				return &DebridHealthCheck{
					Healthy:      false,
					Status:       "not_cached",
					Cached:       false,
					Provider:     result.Attributes["tracker"],
					ErrorMessage: fmt.Sprintf("probe failed: %v", err),
				}, nil
			}

			if audioCount == 0 {
				log.Printf("[debrid-health] pre-resolved stream %s has 0 audio streams - treating as uncached placeholder", result.Title)
				return &DebridHealthCheck{
					Healthy:      false,
					Status:       "not_cached",
					Cached:       false,
					Provider:     result.Attributes["tracker"],
					ErrorMessage: "stream appears to be a placeholder (no audio streams)",
				}, nil
			}

			log.Printf("[debrid-health] pre-resolved stream %s verified with %d audio streams", result.Title, audioCount)
		}

		return &DebridHealthCheck{
			Healthy:  true,
			Status:   "cached",
			Cached:   true,
			Provider: result.Attributes["tracker"],
		}, nil
	}

	// Extract info hash from result attributes (may be empty for torrent file uploads)
	infoHash := strings.TrimSpace(result.Attributes["infoHash"])
	if infoHash == "" {
		// Try to extract from magnet link
		if strings.HasPrefix(strings.ToLower(result.Link), "magnet:") {
			infoHash = extractInfoHashFromMagnet(result.Link)
		}
	}

	// Check if we have a torrent URL (for cases without magnet/infohash)
	torrentURL := strings.TrimSpace(result.Attributes["torrentURL"])

	// We need either infohash/magnet or torrent URL
	hasMagnet := strings.HasPrefix(strings.ToLower(result.Link), "magnet:")
	if infoHash == "" && !hasMagnet && torrentURL == "" {
		return &DebridHealthCheck{
			Healthy:      false,
			Status:       "error",
			Cached:       false,
			ErrorMessage: "missing info hash and no torrent URL available",
		}, nil
	}

	settings, err := s.cfg.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}

	// Determine provider - use attribute if specified, otherwise use first enabled provider
	provider := strings.TrimSpace(result.Attributes["provider"])

	// Find provider config
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
		errMsg := "no debrid provider configured or enabled"
		if provider != "" {
			errMsg = fmt.Sprintf("provider %q not configured or not enabled", provider)
		}
		return &DebridHealthCheck{
			Healthy:      false,
			Status:       "error",
			Cached:       false,
			Provider:     provider,
			ErrorMessage: errMsg,
		}, nil
	}

	// Get provider from registry
	client, ok := GetProvider(strings.ToLower(providerConfig.Provider), providerConfig.APIKey)
	if !ok {
		return &DebridHealthCheck{
			Healthy:      false,
			Status:       "error",
			Cached:       false,
			Provider:     provider,
			InfoHash:     infoHash,
			ErrorMessage: fmt.Sprintf("provider %q not registered", providerConfig.Provider),
		}, nil
	}

	return s.checkProviderHealth(ctx, client, result, infoHash, torrentURL, verifyUncached)
}

func (s *HealthService) checkProviderHealth(ctx context.Context, client Provider, result models.NZBResult, infoHash, torrentURL string, verifyUncached bool) (*DebridHealthCheck, error) {
	providerName := client.Name()

	// Use add+check+remove method to verify cache status
	identifier := infoHash
	if identifier == "" {
		identifier = torrentURL
	}
	log.Printf("[debrid-health] %s checking torrent %s via add+check+remove", providerName, identifier)

	var addResp *AddMagnetResult
	var err error

	// Determine how to add the torrent: magnet link or torrent file upload
	if strings.HasPrefix(strings.ToLower(result.Link), "magnet:") {
		// Use magnet link
		log.Printf("[debrid-health] adding magnet to %s", providerName)
		addResp, err = client.AddMagnet(ctx, result.Link)
		if err != nil {
			log.Printf("[debrid-health] %s add magnet failed for %s: %v", providerName, identifier, err)
			return &DebridHealthCheck{
				Healthy:      false,
				Status:       "error",
				Cached:       false,
				Provider:     providerName,
				InfoHash:     infoHash,
				ErrorMessage: fmt.Sprintf("add magnet failed: %v", err),
			}, nil
		}
	} else if torrentURL != "" {
		// Download and upload torrent file
		log.Printf("[debrid-health] downloading torrent file from %s", torrentURL)
		torrentData, filename, downloadErr := s.downloadTorrentFile(ctx, torrentURL)
		if downloadErr != nil {
			log.Printf("[debrid-health] %s download torrent failed for %s: %v", providerName, identifier, downloadErr)
			return &DebridHealthCheck{
				Healthy:      false,
				Status:       "error",
				Cached:       false,
				Provider:     providerName,
				InfoHash:     infoHash,
				ErrorMessage: fmt.Sprintf("download torrent file failed: %v", downloadErr),
			}, nil
		}
		log.Printf("[debrid-health] uploading torrent file (%d bytes) to %s", len(torrentData), providerName)
		addResp, err = client.AddTorrentFile(ctx, torrentData, filename)
		if err != nil {
			log.Printf("[debrid-health] %s add torrent file failed for %s: %v", providerName, identifier, err)
			return &DebridHealthCheck{
				Healthy:      false,
				Status:       "error",
				Cached:       false,
				Provider:     providerName,
				InfoHash:     infoHash,
				ErrorMessage: fmt.Sprintf("add torrent file failed: %v", err),
			}, nil
		}
	} else {
		return &DebridHealthCheck{
			Healthy:      false,
			Status:       "error",
			Cached:       false,
			Provider:     providerName,
			InfoHash:     infoHash,
			ErrorMessage: "no magnet link or torrent URL available",
		}, nil
	}

	torrentID := addResp.ID
	log.Printf("[debrid-health] %s torrent added with ID %s, getting file list", providerName, torrentID)

	// First, get the torrent info to see what files are available
	info, err := client.GetTorrentInfo(ctx, torrentID)
	if err != nil {
		_ = client.DeleteTorrent(ctx, torrentID)
		log.Printf("[debrid-health] %s get initial torrent info failed for %s: %v", providerName, torrentID, err)
		return &DebridHealthCheck{
			Healthy:      false,
			Status:       "error",
			Cached:       false,
			Provider:     providerName,
			InfoHash:     infoHash,
			ErrorMessage: fmt.Sprintf("get torrent info failed: %v", err),
		}, nil
	}

	// Select all files for caching, but track the preferred playable target
	selection := selectMediaFiles(info.Files, buildSelectionHints(result, info.Filename))
	if selection == nil || len(selection.OrderedIDs) == 0 {
		_ = client.DeleteTorrent(ctx, torrentID)
		log.Printf("[debrid-health] %s torrent %s has no media files", providerName, torrentID)
		return &DebridHealthCheck{
			Healthy:      false,
			Status:       "error",
			Cached:       false,
			Provider:     providerName,
			InfoHash:     infoHash,
			ErrorMessage: "no media files found in torrent",
		}, nil
	}

	if selection.PreferredID != "" {
		log.Printf("[debrid-health] primary file candidate: %q (reason: %s, id=%s)", selection.PreferredLabel, selection.PreferredReason, selection.PreferredID)
	}

	fileSelection := strings.Join(selection.OrderedIDs, ",")
	log.Printf("[debrid-health] %s torrent %s selecting %d media files: %s", providerName, torrentID, len(selection.OrderedIDs), fileSelection)

	// Select media files - this is required to trigger the provider to check cache status
	if err := client.SelectFiles(ctx, torrentID, fileSelection); err != nil {
		_ = client.DeleteTorrent(ctx, torrentID)
		log.Printf("[debrid-health] %s select files failed for %s: %v", providerName, torrentID, err)
		return &DebridHealthCheck{
			Healthy:      false,
			Status:       "error",
			Cached:       false,
			Provider:     providerName,
			InfoHash:     infoHash,
			ErrorMessage: fmt.Sprintf("select files failed: %v", err),
		}, nil
	}

	// Check the torrent info again to see if it's cached or needs download
	info, err = client.GetTorrentInfo(ctx, torrentID)
	if err != nil {
		// Try to clean up even if we got an error
		_ = client.DeleteTorrent(ctx, torrentID)
		log.Printf("[debrid-health] %s get torrent info failed for %s: %v", providerName, torrentID, err)
		return &DebridHealthCheck{
			Healthy:      false,
			Status:       "error",
			Cached:       false,
			Provider:     providerName,
			InfoHash:     infoHash,
			ErrorMessage: fmt.Sprintf("get torrent info failed: %v", err),
		}, nil
	}

	// Check if the torrent is already downloaded (cached)
	isCached := strings.ToLower(info.Status) == "downloaded"
	log.Printf("[debrid-health] %s torrent %s status=%s cached=%t", providerName, torrentID, info.Status, isCached)

	// Always remove the torrent after checking - especially important for non-cached torrents
	// which may have started downloading (e.g., Torbox starts downloads immediately)
	if !isCached {
		log.Printf("[debrid-health] torrent %s is not cached (status=%s), removing from %s account", torrentID, info.Status, providerName)
	}
	deleteErr := client.DeleteTorrent(ctx, torrentID)
	if deleteErr != nil {
		log.Printf("[debrid-health] warning: failed to delete torrent %s: %v", torrentID, deleteErr)
	}

	if isCached {
		return &DebridHealthCheck{
			Healthy:  true,
			Status:   "cached",
			Cached:   true,
			Provider: providerName,
			InfoHash: infoHash,
		}, nil
	}

	return &DebridHealthCheck{
		Healthy:  false,
		Status:   "not_cached",
		Cached:   false,
		Provider: providerName,
		InfoHash: infoHash,
	}, nil
}

// extractInfoHashFromMagnet extracts the info hash from a magnet URI.
func extractInfoHashFromMagnet(magnetURL string) string {
	// magnet:?xt=urn:btih:HASH...
	lower := strings.ToLower(magnetURL)
	xtIndex := strings.Index(lower, "xt=urn:btih:")
	if xtIndex == -1 {
		return ""
	}

	hashStart := xtIndex + len("xt=urn:btih:")
	remaining := magnetURL[hashStart:]

	// Hash ends at & or end of string
	ampIndex := strings.Index(remaining, "&")
	if ampIndex == -1 {
		return strings.ToLower(strings.TrimSpace(remaining))
	}

	return strings.ToLower(strings.TrimSpace(remaining[:ampIndex]))
}

var mediaExtensionPriority = map[string]int{
	".mp4":  0,
	".m4v":  1,
	".mkv":  2,
	".webm": 3,
	".mov":  4,
	".avi":  5,
	".mpg":  6,
	".mpeg": 6,
	".ts":   7,
	".m2ts": 7,
	".mts":  7,
	".wmv":  8,
	".flv":  9,
	".vob":  10,
	".ogv":  11,
	".3gp":  12,
	".divx": 13,
}

type mediaFileSelection struct {
	OrderedIDs      []string
	PreferredID     string
	PreferredLabel  string
	PreferredReason string
}

func (s *mediaFileSelection) promotePreferredToFront() {
	if s == nil {
		return
	}
	if s.PreferredID == "" || len(s.OrderedIDs) == 0 {
		return
	}
	for idx, id := range s.OrderedIDs {
		if id == s.PreferredID {
			if idx != 0 {
				s.OrderedIDs[0], s.OrderedIDs[idx] = s.OrderedIDs[idx], s.OrderedIDs[0]
			}
			return
		}
	}
}

// selectMediaFiles returns a selection structure that includes only media file IDs (for caching)
// while designating a preferred playable file for streaming.
func selectMediaFiles(files []File, hints mediaresolve.SelectionHints) *mediaFileSelection {
	type candidate struct {
		id       string
		label    string
		priority int
	}

	if len(files) == 0 {
		return nil
	}

	orderedIDs := make([]string, 0, len(files))
	var candidates []candidate
	var resolverCandidates []mediaresolve.Candidate
	bestIdx := -1

	for _, file := range files {
		id := fmt.Sprintf("%d", file.ID)

		ext := strings.ToLower(path.Ext(file.Path))
		priority, ok := mediaExtensionPriority[ext]
		if !ok {
			// Skip non-media files - don't add them to orderedIDs
			continue
		}

		// Only add media files to the ordered list
		orderedIDs = append(orderedIDs, id)

		candidates = append(candidates, candidate{
			id:       id,
			label:    file.Path,
			priority: priority,
		})
		resolverCandidates = append(resolverCandidates, mediaresolve.Candidate{
			Label:    file.Path,
			Priority: priority,
		})
		idx := len(candidates) - 1
		if bestIdx == -1 || candidates[idx].priority < candidates[bestIdx].priority {
			bestIdx = idx
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	selection := &mediaFileSelection{
		OrderedIDs: orderedIDs,
	}

	if len(candidates) == 1 {
		selection.PreferredID = candidates[0].id
		selection.PreferredLabel = candidates[0].label
		selection.PreferredReason = "only playable file found"
		selection.promotePreferredToFront()
		return selection
	}

	selectedIdx, reason := mediaresolve.SelectBestCandidate(resolverCandidates, hints)
	if selectedIdx == -1 {
		selectedIdx = bestIdx
		reason = "fallback to extension priority"
	}

	selection.PreferredID = candidates[selectedIdx].id
	selection.PreferredLabel = candidates[selectedIdx].label
	selection.PreferredReason = reason
	selection.promotePreferredToFront()

	return selection
}

// downloadTorrentFile downloads a .torrent file from a URL and returns its contents.
func (s *HealthService) downloadTorrentFile(ctx context.Context, torrentURL string) ([]byte, string, error) {
	// 60s timeout for private trackers via Jackett (two-hop: backend → Jackett → tracker)
	client := &http.Client{Timeout: 60 * time.Second}

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
	filename := s.extractTorrentFilename(resp, torrentURL)

	log.Printf("[debrid-health] downloaded torrent file: %s (%d bytes)", filename, len(data))
	return data, filename, nil
}

// extractTorrentFilename tries to get a filename for the torrent file.
func (s *HealthService) extractTorrentFilename(resp *http.Response, torrentURL string) string {
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

// probeAudioStreamCount uses ffprobe to count audio streams in a URL.
// Returns 0 if no audio streams are found (indicating a placeholder video).
func (s *HealthService) probeAudioStreamCount(ctx context.Context, streamURL string) (int, error) {
	if s.ffprobePath == "" {
		return -1, fmt.Errorf("ffprobe not configured")
	}

	// Use a short timeout - we just need to read the header
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	args := []string{
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		"-select_streams", "a", // Only audio streams
		"-analyzeduration", "5000000", // 5 seconds
		"-probesize", "5000000", // 5MB
		streamURL,
	}

	cmd := exec.CommandContext(probeCtx, s.ffprobePath, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("ffprobe failed: %w (stderr: %s)", err, stderr.String())
	}

	// Parse the JSON output
	var result struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
		} `json:"streams"`
	}

	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return 0, fmt.Errorf("parse ffprobe output: %w", err)
	}

	return len(result.Streams), nil
}
