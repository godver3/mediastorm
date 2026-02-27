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
	"sync"
	"time"

	"novastream/config"
	"novastream/internal/mediaresolve"
	"novastream/models"
	"novastream/utils"
)

// trackCacheEntry stores cached track probe results
type trackCacheEntry struct {
	audioTracks    []AudioTrackInfo
	subtitleTracks []SubtitleTrackInfo
	probeError     string
	expiresAt      time.Time
}

// HealthService checks debrid item health by verifying cached status.
type HealthService struct {
	cfg         *config.Manager
	ffprobePath string
	// Track cache keyed by info hash
	trackCache   map[string]*trackCacheEntry
	trackCacheMu sync.RWMutex
	// Track which hashes are currently being probed
	probing   map[string]bool
	probingMu sync.Mutex
}

// NewHealthService creates a new debrid health check service.
func NewHealthService(cfg *config.Manager) *HealthService {
	return &HealthService{
		cfg:        cfg,
		trackCache: make(map[string]*trackCacheEntry),
		probing:    make(map[string]bool),
	}
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
	// Track info (populated when cached)
	AudioTracks     []AudioTrackInfo    `json:"audioTracks,omitempty"`
	SubtitleTracks  []SubtitleTrackInfo `json:"subtitleTracks,omitempty"`
	TrackProbeError string              `json:"trackProbeError,omitempty"`
	TracksLoading   bool                `json:"tracksLoading,omitempty"`
}

// AudioTrackInfo contains metadata for an audio track.
type AudioTrackInfo struct {
	Index    int    `json:"index"`
	Language string `json:"language"`
	Codec    string `json:"codec"`
	Title    string `json:"title,omitempty"`
}

// SubtitleTrackInfo contains metadata for a subtitle track.
type SubtitleTrackInfo struct {
	Index      int    `json:"index"`
	Language   string `json:"language"`
	Codec      string `json:"codec"`
	Title      string `json:"title,omitempty"`
	Forced     bool   `json:"forced"`
	IsBitmap   bool   `json:"isBitmap"`
	BitmapType string `json:"bitmapType,omitempty"`
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
			headCtx, headCancel := context.WithTimeout(ctx, 20*time.Second)
			defer headCancel()

			// Encode URL properly (handles spaces and special characters)
			encodedStreamURL, encErr := utils.EncodeURLWithSpaces(streamURL)
			if encErr != nil {
				log.Printf("[debrid-health] failed to encode stream URL: %v", encErr)
				encodedStreamURL = streamURL // Fall back to original
			}

			headReq, err := http.NewRequestWithContext(headCtx, http.MethodHead, encodedStreamURL, nil)
			if err == nil {
				headReq.Header.Set("User-Agent", "Mozilla/5.0 (compatible; mediastorm/1.0)")
				if resp, err := http.DefaultClient.Do(headReq); err == nil {
					contentLength := resp.ContentLength
					contentType := resp.Header.Get("Content-Type")
					finalURL := resp.Request.URL.String()
					resp.Body.Close()

					log.Printf("[debrid-health] pre-resolved stream %s: HEAD status=%d content-length=%d content-type=%q final-url=%q",
						result.Title, resp.StatusCode, contentLength, contentType, finalURL)

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
					// 405 = Method Not Allowed means HEAD isn't supported but GET may work fine
					// Fall through to ffprobe check instead of treating as uncached
					// Don't check content-length for 405 - it's the error body size, not the file size
					if resp.StatusCode == http.StatusMethodNotAllowed {
						log.Printf("[debrid-health] pre-resolved stream %s: HEAD not supported (405), falling through to ffprobe", result.Title)
					} else if resp.StatusCode >= 400 {
						log.Printf("[debrid-health] pre-resolved stream %s returned HTTP %d - treating as uncached", result.Title, resp.StatusCode)
						return &DebridHealthCheck{
							Healthy:      false,
							Status:       "not_cached",
							Cached:       false,
							Provider:     result.Attributes["tracker"],
							ErrorMessage: fmt.Sprintf("stream returned HTTP %d", resp.StatusCode),
						}, nil
					} else {
						// Only check content-length for successful HEAD responses (2xx/3xx)
						// Real video files are typically > 10MB, placeholders are usually < 1MB
						if contentLength > 0 && contentLength < 1*1024*1024 {
							log.Printf("[debrid-health] pre-resolved stream %s has suspiciously small size (%d bytes) - likely a placeholder", result.Title, contentLength)
							return &DebridHealthCheck{
								Healthy:      false,
								Status:       "not_cached",
								Cached:       false,
								Provider:     result.Attributes["tracker"],
								ErrorMessage: fmt.Sprintf("stream too small (%d bytes) - likely a placeholder", contentLength),
							}, nil
						}
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

		// Probe for track info
		healthResult := &DebridHealthCheck{
			Healthy:  true,
			Status:   "cached",
			Cached:   true,
			Provider: result.Attributes["tracker"],
		}

		// Probe tracks in background with timeout (don't block if it fails)
		if s.ffprobePath != "" && streamURL != "" {
			tracks, err := s.probeAllTracks(ctx, streamURL)
			if err != nil {
				log.Printf("[debrid-health] track probe failed for %s: %v", result.Title, err)
				healthResult.TrackProbeError = err.Error()
			} else {
				healthResult.AudioTracks = tracks.AudioTracks
				healthResult.SubtitleTracks = tracks.SubtitleTracks
			}
		}

		return healthResult, nil
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
	if selection == nil {
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
	if selection.RejectionReason != "" {
		_ = client.DeleteTorrent(ctx, torrentID)
		log.Printf("[debrid-health] %s torrent %s rejected: %s", providerName, torrentID, selection.RejectionReason)
		return &DebridHealthCheck{
			Healthy:      false,
			Status:       "error",
			Cached:       false,
			Provider:     providerName,
			InfoHash:     infoHash,
			ErrorMessage: selection.RejectionReason,
		}, nil
	}
	if len(selection.OrderedIDs) == 0 {
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

	// Prepare the result
	healthResult := &DebridHealthCheck{
		Healthy:  isCached,
		Status:   "not_cached",
		Cached:   isCached,
		Provider: providerName,
		InfoHash: infoHash,
	}
	if isCached {
		healthResult.Status = "cached"
	}

	// If cached and has links, check track cache or start async probe
	if isCached && len(info.Links) > 0 && s.ffprobePath != "" && infoHash != "" {
		// Find the link for the preferred file (not just the first link)
		// Links are ordered by original file ID, not selection order
		preferredLinkIdx := 0
		if selection != nil && selection.PreferredID != "" {
			preferredFileID := 0
			fmt.Sscanf(selection.PreferredID, "%d", &preferredFileID)
			if preferredFileID > 0 {
				// Build list of selected file IDs in order (this matches links order)
				var selectedFileIDs []int
				for _, f := range info.Files {
					if f.Selected == 1 {
						selectedFileIDs = append(selectedFileIDs, f.ID)
					}
				}
				// Find index of preferred file in selected files list
				for idx, fid := range selectedFileIDs {
					if fid == preferredFileID {
						preferredLinkIdx = idx
						break
					}
				}
				log.Printf("[debrid-health] preferred file ID=%d, link index=%d (of %d links)",
					preferredFileID, preferredLinkIdx, len(info.Links))
			}
		}
		// Ensure link index is valid
		if preferredLinkIdx >= len(info.Links) {
			preferredLinkIdx = 0
		}

		// Check track cache first
		s.trackCacheMu.RLock()
		cached, hasCached := s.trackCache[infoHash]
		s.trackCacheMu.RUnlock()

		if hasCached && time.Now().Before(cached.expiresAt) {
			// Return cached tracks
			healthResult.AudioTracks = cached.audioTracks
			healthResult.SubtitleTracks = cached.subtitleTracks
			healthResult.TrackProbeError = cached.probeError
			log.Printf("[debrid-health] track cache HIT for %s: %d audio, %d subtitle",
				infoHash, len(cached.audioTracks), len(cached.subtitleTracks))
		} else {
			// Check if already probing
			s.probingMu.Lock()
			isProbing := s.probing[infoHash]
			if !isProbing {
				s.probing[infoHash] = true
			}
			s.probingMu.Unlock()

			if isProbing {
				// Already probing, return loading state
				healthResult.TracksLoading = true
				log.Printf("[debrid-health] track probe in progress for %s", infoHash)
			} else {
				// Start async probe - need to unrestrict link first (before torrent is deleted)
				unrestricted, err := client.UnrestrictLink(ctx, info.Links[preferredLinkIdx])
				if err != nil {
					log.Printf("[debrid-health] failed to unrestrict link for track probe: %v", err)
					healthResult.TrackProbeError = fmt.Sprintf("unrestrict failed: %v", err)
					// Clear probing state
					s.probingMu.Lock()
					delete(s.probing, infoHash)
					s.probingMu.Unlock()
				} else if unrestricted.DownloadURL != "" {
					// Start async probe with the download URL
					downloadURL := unrestricted.DownloadURL
					healthResult.TracksLoading = true
					go s.probeTracksAsync(infoHash, downloadURL)
					log.Printf("[debrid-health] started async track probe for %s (link %d: %s)",
						infoHash, preferredLinkIdx, unrestricted.Filename)
				}
			}
		}
	}

	// Always remove the torrent after checking - especially important for non-cached torrents
	// which may have started downloading (e.g., Torbox starts downloads immediately)
	if !isCached {
		log.Printf("[debrid-health] torrent %s is not cached (status=%s), removing from %s account", torrentID, info.Status, providerName)
	}
	deleteErr := client.DeleteTorrent(ctx, torrentID)
	if deleteErr != nil {
		log.Printf("[debrid-health] warning: failed to delete torrent %s: %v", torrentID, deleteErr)
	}

	return healthResult, nil
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
	OrderedIDs       []string
	PreferredID      string
	PreferredLabel   string
	PreferredReason  string
	RejectionReason  string // Set when selection is rejected (e.g., target episode not found)
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
		size     int64
	}

	if len(files) == 0 {
		return nil
	}

	orderedIDs := make([]string, 0, len(files))
	var candidates []candidate
	var resolverCandidates []mediaresolve.Candidate
	bestIdx := -1
	isBDMV := false

	for _, file := range files {
		id := fmt.Sprintf("%d", file.ID)

		// Detect BDMV (Blu-ray) structure
		if strings.Contains(strings.ToUpper(file.Path), "/BDMV/STREAM/") {
			isBDMV = true
		}

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
			size:     file.Bytes,
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
		// For single-file torrents, validate the file matches the target episode
		// This prevents playing the wrong episode when absolute numbering differs from S##E##
		if hints.TargetSeason > 0 && hints.TargetEpisode > 0 {
			targetCode := mediaresolve.EpisodeCode{Season: hints.TargetSeason, Episode: hints.TargetEpisode}
			matchesSeasonEpisode := mediaresolve.CandidateMatchesEpisode(candidates[0].label, targetCode)
			matchesAbsolute := hints.AbsoluteEpisodeNumber > 0 && mediaresolve.CandidateMatchesAbsoluteEpisode(candidates[0].label, hints.AbsoluteEpisodeNumber)
			// For daily shows, use exact date match only - no tolerance.
			// Adjacent dates are different episodes, so tolerance would match the WRONG episode.
			matchesDailyDate := hints.IsDaily && hints.TargetAirDate != "" &&
				mediaresolve.CandidateMatchesDailyDate(candidates[0].label, hints.TargetAirDate, 0)

			if !matchesSeasonEpisode && !matchesAbsolute && !matchesDailyDate {
				var rejectionMsg string
				if hints.IsDaily && hints.TargetAirDate != "" {
					rejectionMsg = fmt.Sprintf("single file %q does not match target S%02dE%02d or date %s",
						candidates[0].label, hints.TargetSeason, hints.TargetEpisode, hints.TargetAirDate)
				} else if hints.AbsoluteEpisodeNumber > 0 {
					rejectionMsg = fmt.Sprintf("single file %q does not match target S%02dE%02d or absolute ep %d",
						candidates[0].label, hints.TargetSeason, hints.TargetEpisode, hints.AbsoluteEpisodeNumber)
				} else {
					rejectionMsg = fmt.Sprintf("single file %q does not match target S%02dE%02d",
						candidates[0].label, hints.TargetSeason, hints.TargetEpisode)
				}
				log.Printf("[debrid-playback] rejecting result: %s", rejectionMsg)
				return &mediaFileSelection{
					RejectionReason: rejectionMsg,
				}
			}

			// Log which matching method succeeded
			if matchesDailyDate && !matchesSeasonEpisode && !matchesAbsolute {
				log.Printf("[debrid-playback] single file matched by daily date %s", hints.TargetAirDate)
			} else if matchesAbsolute && !matchesSeasonEpisode {
				log.Printf("[debrid-playback] single file matched by absolute episode %d", hints.AbsoluteEpisodeNumber)
			}
		}

		selection.PreferredID = candidates[0].id
		selection.PreferredLabel = candidates[0].label
		selection.PreferredReason = "only playable file found"
		selection.promotePreferredToFront()
		return selection
	}

	// For BDMV (Blu-ray) structures, select the largest file as it's the main feature.
	// BDMV files are named numerically (00000.m2ts, 00001.m2ts, etc.) so title matching
	// doesn't work. The main movie is always significantly larger than bonus content.
	if isBDMV {
		largestIdx := 0
		for idx, cand := range candidates {
			if cand.size > candidates[largestIdx].size {
				largestIdx = idx
			}
		}
		selection.PreferredID = candidates[largestIdx].id
		selection.PreferredLabel = candidates[largestIdx].label
		selection.PreferredReason = fmt.Sprintf("BDMV largest file (%d MB)", candidates[largestIdx].size/(1024*1024))
		selection.promotePreferredToFront()
		log.Printf("[debrid-playback] BDMV structure detected, selecting largest file: %s (%d bytes)", candidates[largestIdx].label, candidates[largestIdx].size)
		return selection
	}

	selectedIdx, reason := mediaresolve.SelectBestCandidate(resolverCandidates, hints)
	if selectedIdx == -1 {
		// Check if we were looking for a specific episode that wasn't found
		// In this case, reject the result entirely rather than falling back
		if hints.TargetEpisode > 0 && hints.TargetSeason > 0 {
			var rejectionMsg string
			if hints.IsDaily && hints.TargetAirDate != "" {
				rejectionMsg = fmt.Sprintf("target episode S%02dE%02d (date: %s) not found in torrent", hints.TargetSeason, hints.TargetEpisode, hints.TargetAirDate)
			} else if hints.AbsoluteEpisodeNumber > 0 {
				rejectionMsg = fmt.Sprintf("target episode S%02dE%02d (abs: %d) not found in torrent", hints.TargetSeason, hints.TargetEpisode, hints.AbsoluteEpisodeNumber)
			} else {
				rejectionMsg = fmt.Sprintf("target episode S%02dE%02d not found in torrent", hints.TargetSeason, hints.TargetEpisode)
			}
			log.Printf("[debrid-playback] rejecting result: %s", rejectionMsg)
			return &mediaFileSelection{
				RejectionReason: rejectionMsg,
			}
		}
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
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; mediastorm/1.0)")

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

// bitmapSubtitleCodecs maps codec names to their display type
var bitmapSubtitleCodecs = map[string]string{
	"hdmv_pgs_subtitle": "PGS",
	"dvd_subtitle":      "VOBSUB",
	"dvdsub":            "VOBSUB",
	"pgssub":            "PGS",
}

// TrackProbeResult holds audio and subtitle track information from probing.
type TrackProbeResult struct {
	AudioTracks    []AudioTrackInfo
	SubtitleTracks []SubtitleTrackInfo
}

// probeAllTracks probes a URL for audio and subtitle track information.
// Unlike the HLS probe, this function includes bitmap subtitles with isBitmap flag.
func (s *HealthService) probeAllTracks(ctx context.Context, streamURL string) (*TrackProbeResult, error) {
	if s.ffprobePath == "" {
		return nil, fmt.Errorf("ffprobe not configured")
	}

	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	args := []string{
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		"-analyzeduration", "10000000", // 10 seconds
		"-probesize", "10000000",       // 10MB
		streamURL,
	}

	cmd := exec.CommandContext(probeCtx, s.ffprobePath, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w (stderr: %s)", err, stderr.String())
	}

	var result struct {
		Streams []struct {
			Index       int               `json:"index"`
			CodecType   string            `json:"codec_type"`
			CodecName   string            `json:"codec_name"`
			Tags        map[string]string `json:"tags"`
			Disposition map[string]int    `json:"disposition"`
		} `json:"streams"`
	}

	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}

	probeResult := &TrackProbeResult{}

	for _, stream := range result.Streams {
		codec := strings.ToLower(strings.TrimSpace(stream.CodecName))

		switch stream.CodecType {
		case "audio":
			lang := ""
			title := ""
			if stream.Tags != nil {
				lang = stream.Tags["language"]
				title = stream.Tags["title"]
			}
			probeResult.AudioTracks = append(probeResult.AudioTracks, AudioTrackInfo{
				Index:    stream.Index,
				Language: lang,
				Codec:    codec,
				Title:    title,
			})

		case "subtitle":
			lang := ""
			title := ""
			isForced := false
			if stream.Tags != nil {
				lang = stream.Tags["language"]
				title = stream.Tags["title"]
			}
			if stream.Disposition != nil {
				isForced = stream.Disposition["forced"] > 0
			}

			// Check if this is a bitmap subtitle
			isBitmap := false
			bitmapType := ""
			if bt, ok := bitmapSubtitleCodecs[codec]; ok {
				isBitmap = true
				bitmapType = bt
			}

			probeResult.SubtitleTracks = append(probeResult.SubtitleTracks, SubtitleTrackInfo{
				Index:      stream.Index,
				Language:   lang,
				Codec:      codec,
				Title:      title,
				Forced:     isForced,
				IsBitmap:   isBitmap,
				BitmapType: bitmapType,
			})
		}
	}

	log.Printf("[debrid-health] track probe: audio=%d subtitle=%d", len(probeResult.AudioTracks), len(probeResult.SubtitleTracks))
	return probeResult, nil
}

// probeTracksAsync probes tracks in the background and caches the results.
func (s *HealthService) probeTracksAsync(infoHash, downloadURL string) {
	defer func() {
		// Clear probing state when done
		s.probingMu.Lock()
		delete(s.probing, infoHash)
		s.probingMu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	entry := &trackCacheEntry{
		expiresAt: time.Now().Add(2 * time.Hour), // Cache for 2 hours
	}

	tracks, err := s.probeAllTracks(ctx, downloadURL)
	if err != nil {
		log.Printf("[debrid-health] async track probe failed for %s: %v", infoHash, err)
		entry.probeError = err.Error()
	} else {
		entry.audioTracks = tracks.AudioTracks
		entry.subtitleTracks = tracks.SubtitleTracks
		log.Printf("[debrid-health] async track probe complete for %s: %d audio, %d subtitle",
			infoHash, len(tracks.AudioTracks), len(tracks.SubtitleTracks))
	}

	// Store in cache
	s.trackCacheMu.Lock()
	s.trackCache[infoHash] = entry
	s.trackCacheMu.Unlock()
}
