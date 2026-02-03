package playback

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/internal/database"
	"novastream/internal/importer"
	"novastream/internal/integration"
	"novastream/internal/mediaresolve"
	"novastream/models"
	"novastream/services/debrid"
	usenetsvc "novastream/services/usenet"

	"github.com/javi11/nzbparser"
)

type usenetHealthService interface {
	CheckHealthWithNZB(ctx context.Context, candidate models.NZBResult, nzbBytes []byte, fileName string) (*models.NZBHealthCheck, error)
}

var _ usenetHealthService = (*usenetsvc.Service)(nil)

type metadataService interface {
	ListDirectory(virtualPath string) ([]string, error)
	ListSubdirectories(virtualPath string) ([]string, error)
}

// Service coordinates NZB validation and prepares backend-hosted playback streams.
type Service struct {
	cfg         *config.Manager
	httpClient  *http.Client
	usenet      usenetHealthService
	debrid      *debrid.PlaybackService
	nzbSystem   *integration.NzbSystem
	metadataSvc metadataService
}

var (
	ErrQueueItemNotFound = errors.New("playback queue item not found")
	ErrQueueItemFailed   = errors.New("playback queue item failed")
)

// HealthCheckResult holds the result of a parallel health check for a single candidate
type HealthCheckResult struct {
	Index     int                    // Original index in the results slice (for priority)
	Candidate models.NZBResult       // The candidate that was checked
	NZBBytes  []byte                 // The fetched NZB bytes (if successful)
	FileName  string                 // The derived filename
	Healthy   bool                   // Whether the health check passed
	Error     error                  // Any error that occurred
	Check     *models.NZBHealthCheck // The health check result (if performed)
}

// NewService returns a new playback service with a default HTTP client when one is not provided.
func NewService(cfg *config.Manager, usenetSvc usenetHealthService, nzbSystem *integration.NzbSystem, metadataSvc metadataService) *Service {
	return &Service{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 20, // Allow parallel NZB fetches from same indexer
				MaxConnsPerHost:     20,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		usenet:      usenetSvc,
		debrid:      debrid.NewPlaybackService(cfg, nil),
		nzbSystem:   nzbSystem,
		metadataSvc: metadataSvc,
	}
}

// Resolve ingests the supplied NZB search result, verifies it with our Usenet health check, and returns a streaming path.
func (s *Service) Resolve(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error) {
	log.Printf("[playback] resolve start title=%q downloadURL=%q link=%q serviceType=%q", strings.TrimSpace(candidate.Title), strings.TrimSpace(candidate.DownloadURL), strings.TrimSpace(candidate.Link), candidate.ServiceType)

	// Route to debrid service if this is a debrid result
	if candidate.ServiceType == models.ServiceTypeDebrid {
		if s.debrid == nil {
			return nil, fmt.Errorf("debrid service not configured")
		}
		return s.debrid.Resolve(ctx, candidate)
	}

	// Otherwise, handle as usenet
	downloadURL := strings.TrimSpace(candidate.DownloadURL)
	if downloadURL == "" {
		downloadURL = strings.TrimSpace(candidate.Link)
	}
	if downloadURL == "" {
		return nil, fmt.Errorf("candidate is missing a download URL")
	}

	nzbBytes, fileName, err := s.fetchNZB(ctx, downloadURL, candidate)
	if err != nil {
		return nil, err
	}

	log.Printf("[playback] nzb fetched size=%d fileName=%q", len(nzbBytes), fileName)

	// Check if health check should be skipped (optimization for faster startup)
	cfg, err := s.cfg.Load()
	if err != nil {
		log.Printf("[playback] warning: failed to load config, using default health check behavior: %v", err)
	}
	skipHealthCheck := cfg.Import.SkipHealthCheck

	healthStatus := "unknown"
	var healthCheck *models.NZBHealthCheck

	if skipHealthCheck {
		log.Printf("[playback] health check skipped (skipHealthCheck=true in config)")
	} else if s.usenet != nil {
		check, err := s.usenet.CheckHealthWithNZB(ctx, candidate, nzbBytes, fileName)
		if err != nil {
			return nil, fmt.Errorf("check nzb health: %w", err)
		}
		healthCheck = check
		if check != nil {
			healthStatus = strings.ToLower(strings.TrimSpace(check.Status))
			if healthStatus == "" {
				healthStatus = "unknown"
			}
			log.Printf("[playback] backend health status=%q healthy=%t sampled=%t missing=%d", healthStatus, check.Healthy, check.Sampled, len(check.MissingSegments))
			if !check.Healthy {
				return nil, fmt.Errorf("nzb health check reported %s", healthStatus)
			}
		}
	} else {
		log.Printf("[playback] warning: usenet health service not configured; proceeding without pre-flight validation")
	}

	if s.nzbSystem == nil {
		return nil, fmt.Errorf("NZB system not configured")
	}

	// Process NZB immediately without queuing
	service := s.nzbSystem.ImporterService()
	log.Printf("[playback] processing NZB immediately fileName=%q", fileName)

	// Apply usenet resolution timeout if configured
	processCtx := ctx
	if cfg.Streaming.UsenetResolutionTimeoutSec > 0 {
		var cancel context.CancelFunc
		processCtx, cancel = context.WithTimeout(ctx, time.Duration(cfg.Streaming.UsenetResolutionTimeoutSec)*time.Second)
		defer cancel()
		log.Printf("[playback] usenet resolution timeout set to %d seconds", cfg.Streaming.UsenetResolutionTimeoutSec)
	}

	storagePath, err := service.ProcessNZBImmediately(processCtx, fileName, nzbBytes)
	if err != nil {
		if processCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("usenet resolution timed out after %d seconds", cfg.Streaming.UsenetResolutionTimeoutSec)
		}
		return nil, fmt.Errorf("process NZB immediately: %w", err)
	}

	log.Printf("[playback] NZB processed successfully, storagePath=%q", storagePath)

	// If storagePath is a directory (multi-file NZB), find the best playable file within it
	finalPath := storagePath
	if s.metadataSvc != nil && s.isLikelyDirectory(storagePath) {
		log.Printf("[playback] storagePath appears to be a directory, scanning for media files: %q", storagePath)
		hints := buildSelectionHintsFromCandidate(candidate, storagePath)
		mediaFile, findErr := s.findBestMediaFile(storagePath, hints)
		if findErr != nil {
			return nil, fmt.Errorf("directory contains no playable media files: %w", findErr)
		}
		if mediaFile != "" {
			finalPath = mediaFile
			log.Printf("[playback] selected media file from directory: %q", finalPath)
		}
	}

	sourceNZBPath := strings.TrimSpace(fileName)
	if healthCheck != nil && strings.TrimSpace(healthCheck.FileName) != "" {
		sourceNZBPath = strings.TrimSpace(healthCheck.FileName)
	}

	// Calculate file size from NZB if possible
	fileSize := int64(0)
	if parsed, parseErr := nzbparser.Parse(bytes.NewReader(nzbBytes)); parseErr == nil && len(parsed.Files) > 0 {
		for _, f := range parsed.Files {
			var size int64
			for _, seg := range f.Segments {
				size += int64(seg.Bytes)
			}
			if size > fileSize {
				fileSize = size
			}
		}
	}

	// Prepend WebDAV prefix to the final path (file, not directory)
	webdavPath := fmt.Sprintf("%s%s", strings.TrimRight(cfg.WebDAV.Prefix, "/"), finalPath)

	resolution := &models.PlaybackResolution{
		HealthStatus:  "healthy",
		FileSize:      fileSize,
		SourceNZBPath: sourceNZBPath,
		WebDAVPath:    webdavPath,
	}

	log.Printf("[playback] NZB processed and ready for playback, webdavPath=%q", webdavPath)
	return resolution, nil
}

// ParallelHealthCheck performs health checks on multiple candidates concurrently.
// It returns results sorted by original index (priority order), with healthy results first.
// The limit parameter controls how many candidates to check in parallel.
func (s *Service) ParallelHealthCheck(ctx context.Context, candidates []models.NZBResult, limit int) []HealthCheckResult {
	if len(candidates) == 0 {
		return nil
	}

	// Only check usenet results - debrid doesn't need health checks
	var usenetCandidates []struct {
		index     int
		candidate models.NZBResult
	}
	for i, c := range candidates {
		if c.ServiceType != models.ServiceTypeDebrid {
			usenetCandidates = append(usenetCandidates, struct {
				index     int
				candidate models.NZBResult
			}{i, c})
		}
		if len(usenetCandidates) >= limit {
			break
		}
	}

	if len(usenetCandidates) == 0 {
		return nil
	}

	log.Printf("[playback] starting parallel health check for %d candidates (limit=%d)", len(usenetCandidates), limit)
	start := time.Now()

	// Check if health checks are disabled
	cfg, err := s.cfg.Load()
	if err != nil {
		log.Printf("[playback] warning: failed to load config for parallel health check: %v", err)
	}
	skipHealthCheck := cfg.Import.SkipHealthCheck

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results []HealthCheckResult
	)

	// Create a child context that we can cancel once we have enough healthy results
	checkCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	for _, uc := range usenetCandidates {
		wg.Add(1)
		go func(idx int, candidate models.NZBResult) {
			defer wg.Done()

			result := HealthCheckResult{
				Index:     idx,
				Candidate: candidate,
			}

			// Check if context was cancelled
			select {
			case <-checkCtx.Done():
				result.Error = checkCtx.Err()
				mu.Lock()
				results = append(results, result)
				mu.Unlock()
				return
			default:
			}

			// Get download URL
			downloadURL := strings.TrimSpace(candidate.DownloadURL)
			if downloadURL == "" {
				downloadURL = strings.TrimSpace(candidate.Link)
			}
			if downloadURL == "" {
				result.Error = fmt.Errorf("missing download URL")
				mu.Lock()
				results = append(results, result)
				mu.Unlock()
				return
			}

			// Fetch NZB
			nzbBytes, fileName, err := s.fetchNZB(checkCtx, downloadURL, candidate)
			if err != nil {
				result.Error = fmt.Errorf("fetch NZB: %w", err)
				mu.Lock()
				results = append(results, result)
				mu.Unlock()
				return
			}

			result.NZBBytes = nzbBytes
			result.FileName = fileName

			// Perform health check if not skipped
			if skipHealthCheck {
				result.Healthy = true
				log.Printf("[playback] parallel health check [%d] %s: skipped (config)", idx, candidate.Title)
			} else if s.usenet != nil {
				check, err := s.usenet.CheckHealthWithNZB(checkCtx, candidate, nzbBytes, fileName)
				if err != nil {
					result.Error = fmt.Errorf("health check: %w", err)
					mu.Lock()
					results = append(results, result)
					mu.Unlock()
					return
				}
				result.Check = check
				result.Healthy = check != nil && check.Healthy
				if result.Healthy {
					log.Printf("[playback] parallel health check [%d] %s: healthy", idx, candidate.Title)
				} else {
					status := "unknown"
					if check != nil {
						status = check.Status
					}
					log.Printf("[playback] parallel health check [%d] %s: %s", idx, candidate.Title, status)
				}
			} else {
				// No health service, assume healthy
				result.Healthy = true
				log.Printf("[playback] parallel health check [%d] %s: no health service, assuming healthy", idx, candidate.Title)
			}

			mu.Lock()
			results = append(results, result)
			mu.Unlock()
		}(uc.index, uc.candidate)
	}

	wg.Wait()

	// Sort results: healthy first (by original index), then unhealthy (by original index)
	sort.Slice(results, func(i, j int) bool {
		if results[i].Healthy != results[j].Healthy {
			return results[i].Healthy // healthy comes first
		}
		return results[i].Index < results[j].Index // then by original priority
	})

	elapsed := time.Since(start)
	healthyCount := 0
	for _, r := range results {
		if r.Healthy {
			healthyCount++
		}
	}
	log.Printf("[playback] parallel health check complete: %d/%d healthy in %v", healthyCount, len(results), elapsed)

	return results
}

// ResolveWithHealthResult processes an NZB using pre-fetched health check results.
// This avoids re-fetching and re-checking the NZB when we already have the data.
func (s *Service) ResolveWithHealthResult(ctx context.Context, result HealthCheckResult) (*models.PlaybackResolution, error) {
	if !result.Healthy {
		return nil, fmt.Errorf("health check failed")
	}
	if result.Error != nil {
		return nil, result.Error
	}
	if len(result.NZBBytes) == 0 {
		return nil, fmt.Errorf("no NZB data")
	}

	log.Printf("[playback] resolving with pre-checked result: %s", result.Candidate.Title)

	if s.nzbSystem == nil {
		return nil, fmt.Errorf("NZB system not configured")
	}

	cfg, err := s.cfg.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	// Process NZB immediately without queuing
	service := s.nzbSystem.ImporterService()
	log.Printf("[playback] processing NZB immediately fileName=%q", result.FileName)

	// Apply usenet resolution timeout if configured
	processCtx := ctx
	if cfg.Streaming.UsenetResolutionTimeoutSec > 0 {
		var cancel context.CancelFunc
		processCtx, cancel = context.WithTimeout(ctx, time.Duration(cfg.Streaming.UsenetResolutionTimeoutSec)*time.Second)
		defer cancel()
		log.Printf("[playback] usenet resolution timeout set to %d seconds", cfg.Streaming.UsenetResolutionTimeoutSec)
	}

	storagePath, err := service.ProcessNZBImmediately(processCtx, result.FileName, result.NZBBytes)
	if err != nil {
		if processCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("usenet resolution timed out after %d seconds", cfg.Streaming.UsenetResolutionTimeoutSec)
		}
		return nil, fmt.Errorf("process NZB immediately: %w", err)
	}

	log.Printf("[playback] NZB processed successfully, storagePath=%q", storagePath)

	sourceNZBPath := strings.TrimSpace(result.FileName)
	if result.Check != nil && strings.TrimSpace(result.Check.FileName) != "" {
		sourceNZBPath = strings.TrimSpace(result.Check.FileName)
	}

	// Calculate file size from NZB if possible
	fileSize := int64(0)
	if parsed, parseErr := nzbparser.Parse(bytes.NewReader(result.NZBBytes)); parseErr == nil && len(parsed.Files) > 0 {
		for _, f := range parsed.Files {
			var size int64
			for _, seg := range f.Segments {
				size += int64(seg.Bytes)
			}
			if size > fileSize {
				fileSize = size
			}
		}
	}

	// Prepend WebDAV prefix to the storage path
	webdavPath := fmt.Sprintf("%s%s", strings.TrimRight(cfg.WebDAV.Prefix, "/"), storagePath)

	resolution := &models.PlaybackResolution{
		HealthStatus:  "healthy",
		FileSize:      fileSize,
		SourceNZBPath: sourceNZBPath,
		WebDAVPath:    webdavPath,
	}

	log.Printf("[playback] NZB processed and ready for playback, webdavPath=%q", webdavPath)
	return resolution, nil
}

// QueueStatus inspects the importer queue for the given ID and returns the current playback resolution state.
func (s *Service) QueueStatus(_ context.Context, queueID int64) (*models.PlaybackResolution, error) {
	if s.nzbSystem == nil {
		return nil, fmt.Errorf("NZB system not configured")
	}

	importerSvc := s.nzbSystem.ImporterService()
	if importerSvc == nil {
		return nil, fmt.Errorf("importer service not configured")
	}

	queueItem, err := importerSvc.Database().Repository.GetQueueItem(queueID)
	if err != nil {
		return nil, fmt.Errorf("get queue item: %w", err)
	}
	if queueItem == nil {
		return nil, ErrQueueItemNotFound
	}

	meta := parseQueueMetadata(queueItem.Metadata)
	health := queueStatusToHealth(queueItem.Status)
	fileSize := int64(0)
	if queueItem.FileSize != nil {
		fileSize = *queueItem.FileSize
	}

	switch queueItem.Status {
	case database.QueueStatusFailed:
		errMsg := "unknown error"
		if queueItem.ErrorMessage != nil && strings.TrimSpace(*queueItem.ErrorMessage) != "" {
			errMsg = strings.TrimSpace(*queueItem.ErrorMessage)
		}
		return nil, fmt.Errorf("%w: %s", ErrQueueItemFailed, errMsg)
	case database.QueueStatusCompleted:
		resolution, err := s.buildResolutionFromCompletedItem(queueItem, meta)
		if err != nil {
			return nil, err
		}
		return resolution, nil
	default:
		res := &models.PlaybackResolution{
			QueueID:      queueItem.ID,
			HealthStatus: health,
			FileSize:     fileSize,
		}
		if strings.TrimSpace(meta.SourceNZBPath) != "" {
			res.SourceNZBPath = strings.TrimSpace(meta.SourceNZBPath)
		}
		return res, nil
	}
}

func (s *Service) fetchNZB(ctx context.Context, downloadURL string, candidate models.NZBResult) ([]byte, string, error) {
	log.Printf("[playback] fetching nzb url=%q title=%q", downloadURL, strings.TrimSpace(candidate.Title))

	// Create a context with timeout for the entire fetch operation
	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build nzb request: %w", err)
	}

	log.Printf("[playback] sending http request...")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download nzb: %w", err)
	}
	defer resp.Body.Close()

	log.Printf("[playback] nzb response status=%s contentLength=%d", resp.Status, resp.ContentLength)

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, "", fmt.Errorf("download nzb failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	log.Printf("[playback] reading nzb body...")

	// Limit NZB file size to 50MB to prevent excessive memory usage
	const maxNZBSize = 50 * 1024 * 1024
	limitedReader := io.LimitReader(resp.Body, maxNZBSize)

	// Create a channel to handle the read with timeout
	type readResult struct {
		data []byte
		err  error
	}
	resultChan := make(chan readResult, 1)

	go func() {
		data, err := io.ReadAll(limitedReader)
		resultChan <- readResult{data: data, err: err}
	}()

	select {
	case <-fetchCtx.Done():
		return nil, "", fmt.Errorf("nzb download timeout or cancelled: %w", fetchCtx.Err())
	case result := <-resultChan:
		if result.err != nil {
			return nil, "", fmt.Errorf("read nzb body: %w", result.err)
		}
		if len(result.data) == maxNZBSize {
			log.Printf("[playback] warning: nzb file may have been truncated at %d bytes", maxNZBSize)
		}
		log.Printf("[playback] nzb body read complete size=%d", len(result.data))
		fileName := deriveFileName(resp, downloadURL, candidate)
		return result.data, fileName, nil
	}
}

func deriveFileName(resp *http.Response, downloadURL string, candidate models.NZBResult) string {
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if name := parseFileNameFromContentDisposition(cd); name != "" {
			return ensureNZBExtension(name)
		}
	}

	if parsed, err := url.Parse(downloadURL); err == nil {
		base := path.Base(parsed.Path)
		if base != "" && base != "/" {
			return ensureNZBExtension(base)
		}
	}

	if strings.TrimSpace(candidate.Title) != "" {
		safe := strings.Map(func(r rune) rune {
			switch {
			case r == ' ':
				return '.'
			case r >= 'a' && r <= 'z':
				fallthrough
			case r >= 'A' && r <= 'Z':
				fallthrough
			case r >= '0' && r <= '9':
				return r
			case r == '.' || r == '-' || r == '_':
				return r
			default:
				return -1
			}
		}, candidate.Title)
		if safe != "" {
			return ensureNZBExtension(safe)
		}
	}

	return ensureNZBExtension("novastream")
}

func parseFileNameFromContentDisposition(header string) string {
	parts := strings.Split(header, ";")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(part), "filename=") {
			value := strings.TrimPrefix(part, "filename=")
			value = strings.Trim(value, "\"'")
			if value != "" {
				return value
			}
		}
	}
	return ""
}

func ensureNZBExtension(name string) string {
	if strings.HasSuffix(strings.ToLower(name), ".nzb") {
		return name
	}
	return name + ".nzb"
}

const webDAVScanMaxDepth = 3

var playableExtensionPriority = map[string]int{
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
}

type webDAVEntry struct {
	Name  string
	Size  int64
	IsDir bool
}

type mediaFileCandidate struct {
	path     string
	priority int
}

// buildSelectionHintsFromCandidate extracts selection hints from an NZBResult for file matching.
// This enables episode matching (S01E01) when selecting files from multi-file NZBs.
func buildSelectionHintsFromCandidate(candidate models.NZBResult, directory string) mediaresolve.SelectionHints {
	hints := mediaresolve.SelectionHints{
		ReleaseTitle: candidate.Title,
		QueueName:    candidate.GUID,
		Directory:    directory,
	}

	if candidate.Attributes != nil {
		if code := strings.TrimSpace(candidate.Attributes["targetEpisodeCode"]); code != "" {
			hints.TargetEpisodeCode = code
		}
		if season, _ := strconv.Atoi(strings.TrimSpace(candidate.Attributes["targetSeason"])); season > 0 {
			hints.TargetSeason = season
		}
		if episode, _ := strconv.Atoi(strings.TrimSpace(candidate.Attributes["targetEpisode"])); episode > 0 {
			hints.TargetEpisode = episode
		}
		// Build episode code if we have season/episode but no code
		if hints.TargetEpisodeCode == "" && hints.TargetSeason > 0 && hints.TargetEpisode > 0 {
			hints.TargetEpisodeCode = fmt.Sprintf("S%02dE%02d", hints.TargetSeason, hints.TargetEpisode)
		}
		// Daily show detection
		if isDaily := strings.TrimSpace(candidate.Attributes["isDaily"]); isDaily == "true" {
			hints.IsDaily = true
		}
		// Air date for daily shows
		if airDate := strings.TrimSpace(candidate.Attributes["targetAirDate"]); airDate != "" {
			hints.TargetAirDate = airDate
		}
	}

	return hints
}

// findBestMediaFile recursively scans a directory for the best playable media file
func (s *Service) findBestMediaFile(dirPath string, hints mediaresolve.SelectionHints) (string, error) {
	var candidates []mediaFileCandidate
	var resolverCandidates []mediaresolve.Candidate
	bestIdx := -1

	var scan func(currentPath string, depth int) error
	scan = func(currentPath string, depth int) error {
		if depth > webDAVScanMaxDepth {
			return nil
		}

		// List files in current directory
		files, err := s.metadataSvc.ListDirectory(currentPath)
		if err != nil {
			log.Printf("[playback] failed to list directory %q: %v", currentPath, err)
			return err
		}

		log.Printf("[playback] scanning directory %q: found %d files", currentPath, len(files))

		// Check each file
		for _, filename := range files {
			ext := strings.ToLower(path.Ext(filename))
			priority, isPlayable := playableExtensionPriority[ext]

			if isPlayable {
				// Skip sample/extras files early - these should never be selected
				lowerName := strings.ToLower(filename)
				if strings.Contains(lowerName, "sample") || strings.Contains(lowerName, "extras") {
					log.Printf("[playback] skipping sample/extras file: %q", filename)
					continue
				}

				filePath := path.Join(currentPath, filename)
				log.Printf("[playback] found playable file: %q (ext=%s priority=%d)", filePath, ext, priority)

				candidates = append(candidates, mediaFileCandidate{
					path:     filePath,
					priority: priority,
				})
				resolverCandidates = append(resolverCandidates, mediaresolve.Candidate{
					Label:    filePath,
					Priority: priority,
				})
				idx := len(candidates) - 1
				if bestIdx == -1 || candidates[idx].priority < candidates[bestIdx].priority {
					bestIdx = idx
				}
			}
		}

		// Scan subdirectories
		subdirs, err := s.metadataSvc.ListSubdirectories(currentPath)
		if err != nil {
			log.Printf("[playback] failed to list subdirectories in %q: %v", currentPath, err)
			return err
		}

		log.Printf("[playback] scanning directory %q: found %d subdirectories", currentPath, len(subdirs))

		for _, subdir := range subdirs {
			// Skip sample/extras directories - they never contain main content
			lowerDir := strings.ToLower(subdir)
			if lowerDir == "sample" || lowerDir == "samples" || lowerDir == "extras" || lowerDir == "extra" {
				log.Printf("[playback] skipping sample/extras directory: %q", subdir)
				continue
			}

			subdirPath := path.Join(currentPath, subdir)
			if err := scan(subdirPath, depth+1); err != nil {
				log.Printf("[playback] error scanning subdirectory %q: %v", subdirPath, err)
			}
		}

		return nil
	}

	if err := scan(dirPath, 0); err != nil {
		return "", err
	}

	if len(candidates) == 0 {
		return "", fmt.Errorf("no playable media files found")
	}

	if len(candidates) == 1 {
		log.Printf("[playback] only playable file found; selecting %q", candidates[0].path)
		return candidates[0].path, nil
	}

	selectorHints := hints
	if strings.TrimSpace(selectorHints.Directory) == "" {
		selectorHints.Directory = dirPath
	}

	selectedIdx, reason := mediaresolve.SelectBestCandidate(resolverCandidates, selectorHints)
	if selectedIdx != -1 {
		if strings.TrimSpace(reason) == "" {
			reason = "heuristic match"
		}
		log.Printf("[playback] selected media candidate %q (%s)", candidates[selectedIdx].path, reason)
		return candidates[selectedIdx].path, nil
	}

	if bestIdx != -1 {
		log.Printf("[playback] selector did not find a definitive match; falling back to extension priority candidate %q", candidates[bestIdx].path)
		return candidates[bestIdx].path, nil
	}

	log.Printf("[playback] selector returned no result; defaulting to first candidate %q", candidates[0].path)
	return candidates[0].path, nil
}

func (s *Service) isLikelyDirectory(p string) bool {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return false
	}
	if strings.HasSuffix(trimmed, "/") {
		return true
	}
	base := path.Base(trimmed)
	ext := strings.ToLower(path.Ext(base))
	if ext == "" {
		return true
	}
	if _, ok := playableExtensionPriority[ext]; ok {
		return false
	}
	return true
}

type queueMetadata struct {
	SourceNZBPath   string `json:"sourceNzbPath,omitempty"`
	PreflightHealth string `json:"preflightHealth,omitempty"`
}

func (s *Service) persistQueueMetadata(importerSvc *importer.Service, queueID int64, meta queueMetadata) error {
	if importerSvc == nil {
		return fmt.Errorf("importer service unavailable")
	}

	if strings.TrimSpace(meta.SourceNZBPath) == "" && strings.TrimSpace(meta.PreflightHealth) == "" {
		return nil
	}

	encoded, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal queue metadata: %w", err)
	}

	metadataStr := string(encoded)
	if err := importerSvc.Database().Repository.UpdateMetadata(queueID, &metadataStr); err != nil {
		return fmt.Errorf("persist queue metadata: %w", err)
	}

	return nil
}

func parseQueueMetadata(raw *string) queueMetadata {
	if raw == nil {
		return queueMetadata{}
	}

	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return queueMetadata{}
	}

	var meta queueMetadata
	if err := json.Unmarshal([]byte(trimmed), &meta); err != nil {
		log.Printf("[playback] WARN: failed to parse queue metadata %q: %v", trimmed, err)
		return queueMetadata{}
	}

	return meta
}

func queueStatusToHealth(status database.QueueStatus) string {
	switch status {
	case database.QueueStatusPending:
		return "queued"
	case database.QueueStatusProcessing, database.QueueStatusRetrying:
		return "processing"
	case database.QueueStatusCompleted:
		return "healthy"
	case database.QueueStatusFailed:
		return "failed"
	default:
		return strings.TrimSpace(string(status))
	}
}

func (s *Service) buildResolutionFromCompletedItem(queueItem *database.ImportQueueItem, meta queueMetadata) (*models.PlaybackResolution, error) {
	if queueItem == nil {
		return nil, fmt.Errorf("queue item is nil")
	}
	if queueItem.StoragePath == nil || strings.TrimSpace(*queueItem.StoragePath) == "" {
		return nil, fmt.Errorf("completed queue item missing storage path")
	}

	storagePath := strings.TrimSpace(*queueItem.StoragePath)
	finalPath := storagePath
	if s.metadataSvc != nil && s.isLikelyDirectory(storagePath) {
		log.Printf("[playback] storagePath appears to be a directory, scanning for media files: %q", storagePath)
		mediaFile, err := s.findBestMediaFile(storagePath, mediaresolve.SelectionHints{
			ReleaseTitle: meta.SourceNZBPath,
			QueueName:    queueItem.NzbPath,
			Directory:    storagePath,
		})
		if err != nil {
			return nil, fmt.Errorf("directory contains no playable media files: %w", err)
		}

		if mediaFile != "" {
			finalPath = mediaFile
			log.Printf("[playback] found media file in directory: %q", finalPath)
		} else {
			log.Printf("[playback] WARNING: no media file found in directory %q", storagePath)
		}
	}

	settings, err := s.cfg.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}

	webdavPath := fmt.Sprintf("%s%s", strings.TrimRight(settings.WebDAV.Prefix, "/"), finalPath)
	fileSize := int64(0)
	if queueItem.FileSize != nil {
		fileSize = *queueItem.FileSize
	}

	health := strings.TrimSpace(meta.PreflightHealth)
	if health == "" {
		health = "healthy"
	}

	resolution := &models.PlaybackResolution{
		QueueID:      queueItem.ID,
		WebDAVPath:   webdavPath,
		HealthStatus: health,
		FileSize:     fileSize,
	}

	if strings.TrimSpace(meta.SourceNZBPath) != "" {
		resolution.SourceNZBPath = strings.TrimSpace(meta.SourceNZBPath)
	}

	return resolution, nil
}
