package playback

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"novastream/config"
	"novastream/internal/database"
	"novastream/internal/dnscache"
	"novastream/internal/httpheaders"
	"novastream/internal/importer"
	"novastream/internal/integration"
	"novastream/internal/mediaresolve"
	metapb "novastream/internal/nzb/metadata/proto"
	"novastream/models"
	"novastream/services/debrid"
	usenetsvc "novastream/services/usenet"
	"novastream/services/usenetengine"

	"github.com/javi11/nzbparser"
)

type usenetHealthService interface {
	CheckHealthWithNZB(ctx context.Context, candidate models.NZBResult, nzbBytes []byte, fileName string) (*models.NZBHealthCheck, error)
}

var _ usenetHealthService = (*usenetsvc.Service)(nil)

type metadataService interface {
	ListDirectory(virtualPath string) ([]string, error)
	ListSubdirectories(virtualPath string) ([]string, error)
	GetFileMetadata(virtualPath string) (*metapb.FileMetadata, error)
}

// Service coordinates NZB validation and prepares backend-hosted playback streams.
type Service struct {
	cfg         *config.Manager
	httpClient  *http.Client
	usenet      usenetHealthService
	debrid      *debrid.PlaybackService
	nzbSystem   *integration.NzbSystem
	metadataSvc metadataService

	externalMu     sync.Mutex
	externalJobs   map[int64]*externalUsenetJob
	externalNextID atomic.Int64

	// NZB fetch/process counters for diagnostics (atomic, safe for concurrent use).
	// Grep logs for [search-stats] to see totals during playback.
	nzbFetchCount   atomic.Int64 // NZB file downloads from indexers
	nzbProcessCount atomic.Int64 // NZB files sent for immediate processing
}

type externalUsenetJob struct {
	ID             int64
	EngineJobID    string
	Engine         config.UsenetEngineSettings
	SubmittedTitle string
	SourceNZBPath  string
	FileSize       int64
	CreatedAt      time.Time
	LastStatus     string
	LastError      string
}

var (
	ErrQueueItemNotFound = errors.New("playback queue item not found")
	ErrQueueItemFailed   = errors.New("playback queue item failed")
)

const externalQueueIDBase int64 = 1_000_000_000

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
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20, // Allow parallel NZB fetches from same indexer
		MaxConnsPerHost:     20,
		IdleConnTimeout:     90 * time.Second,
	}
	dnscache.ConfigureTransport(transport, dnscache.DefaultTTL)

	service := &Service{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout:   60 * time.Second,
			Transport: transport,
		},
		usenet:       usenetSvc,
		debrid:       debrid.NewPlaybackService(cfg, nil),
		nzbSystem:    nzbSystem,
		metadataSvc:  metadataSvc,
		externalJobs: make(map[int64]*externalUsenetJob),
	}
	service.externalNextID.Store(externalQueueIDBase)
	return service
}

// ResolveBatch performs a single set of provider API calls and resolves all episodes from memory.
// Only supported for debrid results.
func (s *Service) ResolveBatch(ctx context.Context, candidate models.NZBResult, episodes []models.BatchEpisodeTarget) (*models.BatchResolveResponse, error) {
	if candidate.ServiceType != models.ServiceTypeDebrid {
		return nil, fmt.Errorf("batch resolve is only supported for debrid results")
	}
	if s.debrid == nil {
		return nil, fmt.Errorf("debrid service not configured")
	}
	return s.debrid.ResolveBatch(ctx, candidate, episodes)
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

	if res, err := s.resolveExternalUsenet(ctx, cfg, candidate, nzbBytes, fileName); err != nil {
		return nil, err
	} else if res != nil {
		return res, nil
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
	processNum := s.nzbProcessCount.Add(1)
	log.Printf("[search-stats] NZB process #%d started (fileName=%q, totals: fetches=%d, processes=%d)",
		processNum, fileName, s.nzbFetchCount.Load(), s.nzbProcessCount.Load())
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
	if isNonContentMediaPath(finalPath) {
		return nil, fmt.Errorf("resolved media path appears to be a sample/extras file: %s", path.Base(finalPath))
	}
	if err := s.validateResolvedMediaFile(finalPath, storagePath, candidate); err != nil {
		return nil, err
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
	externalEnabled := externalUsenetEnabledForProfile(cfg, "")

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

			// Perform health check if not skipped. External engines own the final
			// availability/import decision, so do not reject candidates with our
			// direct NNTP segment sampler before they can be submitted.
			if skipHealthCheck {
				result.Healthy = true
				log.Printf("[playback] parallel health check [%d] %s: skipped (config)", idx, candidate.Title)
			} else if externalEnabled || externalUsenetEnabledForCandidate(cfg, candidate) {
				result.Healthy = true
				log.Printf("[playback] parallel health check [%d] %s: skipped (external usenet engine)", idx, candidate.Title)
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
	if result.Error != nil {
		return nil, result.Error
	}
	if len(result.NZBBytes) == 0 {
		return nil, fmt.Errorf("no NZB data")
	}

	log.Printf("[playback] resolving with pre-checked result: %s", result.Candidate.Title)

	cfg, err := s.cfg.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	if !result.Healthy && !externalUsenetEnabledForCandidate(cfg, result.Candidate) {
		return nil, fmt.Errorf("health check failed")
	}
	if res, err := s.resolveExternalUsenet(ctx, cfg, result.Candidate, result.NZBBytes, result.FileName); err != nil {
		return nil, err
	} else if res != nil {
		return res, nil
	}

	if !result.Healthy {
		return nil, fmt.Errorf("health check failed")
	}
	if s.nzbSystem == nil {
		return nil, fmt.Errorf("NZB system not configured")
	}

	// Process NZB immediately without queuing
	service := s.nzbSystem.ImporterService()
	processNum := s.nzbProcessCount.Add(1)
	log.Printf("[search-stats] NZB process #%d started (fileName=%q, totals: fetches=%d, processes=%d)",
		processNum, result.FileName, s.nzbFetchCount.Load(), s.nzbProcessCount.Load())
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

	finalPath := storagePath
	if s.metadataSvc != nil && s.isLikelyDirectory(storagePath) {
		log.Printf("[playback] storagePath appears to be a directory, scanning for media files: %q", storagePath)
		hints := buildSelectionHintsFromCandidate(result.Candidate, storagePath)
		mediaFile, findErr := s.findBestMediaFile(storagePath, hints)
		if findErr != nil {
			return nil, fmt.Errorf("directory contains no playable media files: %w", findErr)
		}
		if mediaFile != "" {
			finalPath = mediaFile
			log.Printf("[playback] selected media file from directory: %q", finalPath)
		}
	}
	if isNonContentMediaPath(finalPath) {
		return nil, fmt.Errorf("resolved media path appears to be a sample/extras file: %s", path.Base(finalPath))
	}
	if err := s.validateResolvedMediaFile(finalPath, storagePath, result.Candidate); err != nil {
		return nil, err
	}

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

// QueueStatus inspects the importer queue for the given ID and returns the current playback resolution state.
func (s *Service) QueueStatus(ctx context.Context, queueID int64) (*models.PlaybackResolution, error) {
	if res, handled, err := s.externalQueueStatus(ctx, queueID); handled {
		return res, err
	}
	if queueID >= externalQueueIDBase {
		return nil, ErrQueueItemNotFound
	}

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

func (s *Service) resolveExternalUsenet(ctx context.Context, settings config.Settings, candidate models.NZBResult, nzbBytes []byte, fileName string) (*models.PlaybackResolution, error) {
	profileID := strings.TrimSpace(candidate.Attributes["profileId"])
	engines := usenetengine.EnabledEngines(settings, profileID)
	if len(engines) == 0 {
		return nil, nil
	}

	engineSettings := engines[0]
	engine, err := usenetengine.NewFromSettings(engineSettings, s.httpClient)
	if err != nil {
		return nil, fmt.Errorf("configure usenet engine %q: %w", engineSettings.Name, err)
	}

	category := strings.TrimSpace(engineSettings.Category)
	if candidateCategory := strings.TrimSpace(candidate.Attributes["category"]); candidateCategory != "" {
		category = candidateCategory
	}
	priority := strings.TrimSpace(engineSettings.Priority)

	log.Printf("[playback] submitting NZB to external usenet engine name=%q type=%q fileName=%q", engineSettings.Name, engineSettings.Type, fileName)
	submit, err := engine.SubmitNZB(ctx, usenetengine.SubmitRequest{
		FileName: fileName,
		NZB:      nzbBytes,
		Category: category,
		Priority: priority,
	})
	if err != nil {
		return nil, fmt.Errorf("submit NZB to external usenet engine: %w", err)
	}

	queueID := s.externalNextID.Add(1)
	if queueID <= 0 {
		queueID = s.externalNextID.Add(1)
	}
	sourceNZBPath := strings.TrimSpace(fileName)
	fileSize := estimateNZBFileSize(nzbBytes)

	s.externalMu.Lock()
	s.externalJobs[queueID] = &externalUsenetJob{
		ID:             queueID,
		EngineJobID:    submit.JobID,
		Engine:         engineSettings,
		SubmittedTitle: strings.TrimSpace(candidate.Title),
		SourceNZBPath:  sourceNZBPath,
		FileSize:       fileSize,
		CreatedAt:      time.Now(),
		LastStatus:     string(usenetengine.StatusQueued),
	}
	s.externalMu.Unlock()

	log.Printf("[playback] external usenet job queued queueID=%d engineJobID=%q engine=%q", queueID, submit.JobID, engine.Name())
	return &models.PlaybackResolution{
		QueueID:       queueID,
		HealthStatus:  "queued",
		FileSize:      fileSize,
		SourceNZBPath: sourceNZBPath,
	}, nil
}

func (s *Service) externalQueueStatus(ctx context.Context, queueID int64) (*models.PlaybackResolution, bool, error) {
	s.externalMu.Lock()
	job := s.externalJobs[queueID]
	s.externalMu.Unlock()
	if job == nil {
		return nil, false, nil
	}

	engine, err := usenetengine.NewFromSettings(job.Engine, s.httpClient)
	if err != nil {
		return nil, true, fmt.Errorf("configure usenet engine %q: %w", job.Engine.Name, err)
	}
	status, err := engine.Status(ctx, job.EngineJobID)
	if err != nil {
		s.rememberExternalJobError(queueID, err)
		return nil, true, fmt.Errorf("poll external usenet engine: %w", err)
	}
	if status == nil {
		status = &usenetengine.JobStatus{JobID: job.EngineJobID, Status: usenetengine.StatusUnknown}
	}

	health := externalHealthStatus(status.Status)
	fileSize := firstPositiveInt64(status.SizeBytes, job.FileSize)
	sourceNZBPath := strings.TrimSpace(job.SourceNZBPath)
	if sourceNZBPath == "" {
		sourceNZBPath = strings.TrimSpace(status.FileName)
	}
	statusFileName := strings.TrimSpace(status.FileName)
	statusFileNameMatchesSubmission := statusFileName == "" || externalReleaseNameMatchesSubmitted(statusFileName, job.SubmittedTitle, sourceNZBPath)

	s.externalMu.Lock()
	if existing := s.externalJobs[queueID]; existing != nil {
		existing.LastStatus = health
		existing.LastError = status.Error
		if fileSize > 0 {
			existing.FileSize = fileSize
		}
		if statusFileName != "" && statusFileNameMatchesSubmission {
			existing.SourceNZBPath = statusFileName
		}
	}
	s.externalMu.Unlock()

	switch status.Status {
	case usenetengine.StatusFailed:
		errMsg := strings.TrimSpace(status.Error)
		if errMsg == "" {
			errMsg = strings.TrimSpace(status.RawStatus)
		}
		if errMsg == "" {
			errMsg = "external usenet engine reported failure"
		}
		s.deleteExternalJob(queueID)
		return nil, true, fmt.Errorf("%w: %s", ErrQueueItemFailed, errMsg)
	case usenetengine.StatusCompleted:
		if !statusFileNameMatchesSubmission {
			log.Printf("[playback] external usenet status filename mismatch queueID=%d engineJobID=%q submitted=%q sourceNZB=%q statusFileName=%q; ignoring status filename",
				queueID, job.EngineJobID, job.SubmittedTitle, sourceNZBPath, statusFileName)
		}
		streamURL := ""
		if statusFileNameMatchesSubmission && externalOutputPathMatchesSubmitted(status.OutputPath, job.SubmittedTitle, sourceNZBPath) {
			var urlErr error
			streamURL, urlErr = s.resolveExternalWebDAVStream(ctx, job.Engine, status.OutputPath)
			if urlErr != nil {
				return nil, true, urlErr
			}
		}
		if streamURL == "" {
			log.Printf("[playback] external usenet completed output not bound to submitted release queueID=%d engineJobID=%q submitted=%q sourceNZB=%q statusFileName=%q output=%q; probing exact WebDAV fallback",
				queueID, job.EngineJobID, job.SubmittedTitle, sourceNZBPath, statusFileName, status.OutputPath)
			if fallbackURL, ok, fallbackErr := s.resolveExternalWebDAVFallback(ctx, job.Engine, job.SubmittedTitle, sourceNZBPath); fallbackErr != nil {
				return nil, true, fallbackErr
			} else if ok {
				s.deleteExternalJob(queueID)
				return &models.PlaybackResolution{
					QueueID:       queueID,
					WebDAVPath:    fallbackURL,
					HealthStatus:  "healthy",
					FileSize:      fileSize,
					SourceNZBPath: sourceNZBPath,
				}, true, nil
			}
			return &models.PlaybackResolution{
				QueueID:       queueID,
				HealthStatus:  "processing",
				FileSize:      fileSize,
				SourceNZBPath: sourceNZBPath,
			}, true, nil
		}
		s.deleteExternalJob(queueID)
		return &models.PlaybackResolution{
			QueueID:       queueID,
			WebDAVPath:    streamURL,
			HealthStatus:  "healthy",
			FileSize:      fileSize,
			SourceNZBPath: sourceNZBPath,
		}, true, nil
	default:
		if streamURL, ok, fallbackErr := s.resolveExternalWebDAVFallback(ctx, job.Engine, job.SubmittedTitle, sourceNZBPath); fallbackErr != nil {
			return nil, true, fallbackErr
		} else if ok {
			s.deleteExternalJob(queueID)
			return &models.PlaybackResolution{
				QueueID:       queueID,
				WebDAVPath:    streamURL,
				HealthStatus:  "healthy",
				FileSize:      fileSize,
				SourceNZBPath: sourceNZBPath,
			}, true, nil
		}
		return &models.PlaybackResolution{
			QueueID:       queueID,
			HealthStatus:  health,
			FileSize:      fileSize,
			SourceNZBPath: sourceNZBPath,
		}, true, nil
	}
}

func externalReleaseNameMatchesSubmitted(value, submittedTitle, sourceNZBPath string) bool {
	candidate := externalReleaseName(value)
	if candidate == "" {
		return false
	}
	for _, releaseName := range externalFallbackReleaseNames(submittedTitle, sourceNZBPath) {
		if strings.EqualFold(candidate, releaseName) {
			return true
		}
	}
	return false
}

func externalOutputPathMatchesSubmitted(outputPath, submittedTitle, sourceNZBPath string) bool {
	outputPath = strings.TrimSpace(outputPath)
	if outputPath == "" {
		return false
	}
	for _, releaseName := range externalFallbackReleaseNames(submittedTitle, sourceNZBPath) {
		if externalPathHasExactRelease(outputPath, releaseName) {
			return true
		}
	}
	return false
}

func externalPathHasExactRelease(rawPath, releaseName string) bool {
	releaseName = strings.TrimSpace(releaseName)
	if releaseName == "" {
		return false
	}
	pathText := strings.TrimSpace(rawPath)
	if parsed, err := url.Parse(pathText); err == nil && parsed.Path != "" {
		pathText = parsed.Path
	}
	if decoded, err := url.PathUnescape(pathText); err == nil {
		pathText = decoded
	}
	pathText = strings.Trim(pathText, "/")
	for _, segment := range strings.Split(pathText, "/") {
		segment = strings.TrimSpace(segment)
		if strings.EqualFold(segment, releaseName) {
			return true
		}
		if strings.EqualFold(externalReleaseName(segment), releaseName) {
			return true
		}
	}
	return false
}

func externalReleaseName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Path != "" {
		value = path.Base(strings.TrimRight(parsed.Path, "/"))
	} else {
		value = path.Base(strings.TrimRight(value, "/"))
	}
	if decoded, err := url.PathUnescape(value); err == nil {
		value = decoded
	}
	for {
		ext := path.Ext(value)
		if ext == "" {
			break
		}
		lowerExt := strings.ToLower(ext)
		if lowerExt != ".nzb" {
			if _, ok := playableExtensionPriority[lowerExt]; !ok {
				break
			}
		}
		value = strings.TrimSuffix(value, ext)
	}
	return strings.TrimSpace(value)
}

func (s *Service) rememberExternalJobError(queueID int64, err error) {
	s.externalMu.Lock()
	defer s.externalMu.Unlock()
	if job := s.externalJobs[queueID]; job != nil && err != nil {
		job.LastError = err.Error()
	}
}

func (s *Service) deleteExternalJob(queueID int64) {
	s.externalMu.Lock()
	delete(s.externalJobs, queueID)
	s.externalMu.Unlock()
}

func externalHealthStatus(status usenetengine.Status) string {
	switch status {
	case usenetengine.StatusQueued:
		return "queued"
	case usenetengine.StatusProcessing:
		return "processing"
	case usenetengine.StatusCompleted:
		return "healthy"
	case usenetengine.StatusFailed:
		return "failed"
	default:
		return "processing"
	}
}

func (s *Service) resolveExternalWebDAVStream(ctx context.Context, engine config.UsenetEngineSettings, outputPath string) (string, error) {
	streamURL, err := externalWebDAVURL(engine, outputPath)
	if err != nil {
		return "", err
	}
	if isExternalPlayableURL(streamURL) && !isNonContentMediaPath(streamURL) {
		return streamURL, nil
	}
	if isExternalPlayableURL(streamURL) && isNonContentMediaPath(streamURL) {
		return "", fmt.Errorf("external usenet engine selected non-content media path: %s", path.Base(streamURL))
	}

	selected, err := s.findExternalWebDAVMediaFile(ctx, engine, streamURL, 0)
	if err != nil {
		return "", err
	}
	if selected == "" {
		return "", fmt.Errorf("external usenet WebDAV directory contains no playable media files")
	}
	return selected, nil
}

func (s *Service) resolveExternalWebDAVFallback(ctx context.Context, engine config.UsenetEngineSettings, submittedTitle, sourceNZBPath string) (string, bool, error) {
	engineType := strings.ToLower(strings.TrimSpace(engine.Type))
	if engineType != "decypharr" && engineType != "altmount" {
		return "", false, nil
	}
	releaseNames := externalFallbackReleaseNames(submittedTitle, sourceNZBPath)
	if len(releaseNames) == 0 {
		return "", false, nil
	}

	for _, releaseName := range releaseNames {
		for _, basePath := range externalFallbackBasePaths(engine) {
			for _, candidatePath := range externalExactFallbackPaths(engineType, basePath, releaseName) {
				candidateURL, err := externalWebDAVURL(engine, candidatePath)
				if err != nil {
					return "", true, err
				}
				if isExternalPlayableURL(candidateURL) {
					if ok := s.externalWebDAVURLExists(ctx, engine, candidateURL); !ok {
						continue
					}
					return candidateURL, true, nil
				}
				selected, err := s.findExternalWebDAVMediaFile(ctx, engine, candidateURL, 0)
				if err != nil {
					continue
				}
				if selected == "" {
					continue
				}
				return selected, true, nil
			}
		}
	}

	return "", false, nil
}

func externalFallbackReleaseNames(submittedTitle, sourceNZBPath string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, 4)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if strings.EqualFold(path.Ext(value), ".nzb") {
			value = strings.TrimSuffix(value, path.Ext(value))
		}
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		out = append(out, value)
	}
	add(sourceNZBPath)
	if base := stripDuplicateReleaseSuffix(sourceNZBPath); base != sourceNZBPath {
		add(base)
	}
	add(submittedTitle)
	return out
}

func externalExactFallbackPaths(engineType, basePath, releaseName string) []string {
	switch strings.ToLower(strings.TrimSpace(engineType)) {
	case "altmount":
		out := []string{path.Join(basePath, releaseName)}
		for ext := range playableExtensionPriority {
			out = append(out, path.Join(basePath, releaseName+ext))
		}
		return out
	default:
		return []string{path.Join(basePath, releaseName)}
	}
}

func stripDuplicateReleaseSuffix(value string) string {
	value = strings.TrimSpace(value)
	ext := path.Ext(value)
	if strings.EqualFold(ext, ".nzb") {
		value = strings.TrimSuffix(value, ext)
	}
	idx := strings.LastIndex(value, "_")
	if idx <= 0 || idx == len(value)-1 {
		return value
	}
	if _, err := strconv.Atoi(value[idx+1:]); err != nil {
		return value
	}
	return value[:idx]
}

func externalFallbackBasePaths(engine config.UsenetEngineSettings) []string {
	engineType := strings.ToLower(strings.TrimSpace(engine.Type))
	switch engineType {
	case "altmount":
		category := strings.TrimSpace(engine.Category)
		if category == "" {
			category = strings.TrimSpace(engine.Config["webdavCategory"])
		}
		if category == "" {
			category = "Default"
		}
		completeDir := strings.Trim(strings.TrimSpace(engine.Config["webdavCompleteDir"]), "/")
		if completeDir == "" {
			completeDir = "complete"
		}
		return []string{path.Join(category, completeDir)}
	case "decypharr":
		return []string{"nzbs"}
	default:
		return nil
	}
}

func externalWebDAVURL(engine config.UsenetEngineSettings, outputPath string) (string, error) {
	outputPath = strings.TrimSpace(outputPath)
	if outputPath == "" {
		return "", fmt.Errorf("external usenet engine completed without a stream path")
	}
	if strings.HasPrefix(outputPath, "http://") || strings.HasPrefix(outputPath, "https://") {
		return externalWebDAVAbsoluteURL(engine, outputPath)
	}
	base := strings.TrimSpace(engine.WebDAVBaseURL)
	if base == "" {
		return "", fmt.Errorf("external usenet engine completed at %q but webdavBaseUrl is not configured", outputPath)
	}
	relative := externalWebDAVRelativePath(engine, outputPath)
	if relative == "" {
		return "", fmt.Errorf("unable to map external usenet output path %q to WebDAV URL", outputPath)
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse webdavBaseUrl: %w", err)
	}
	basePath := strings.TrimRight(baseURL.Path, "/")
	relPath := strings.TrimLeft(relative, "/")
	baseURL.Path = joinURLPath(basePath, relPath)
	baseURL.RawQuery = ""
	baseURL.Fragment = ""
	return baseURL.String(), nil
}

func externalWebDAVAbsoluteURL(engine config.UsenetEngineSettings, outputURL string) (string, error) {
	parsed, err := url.Parse(outputURL)
	if err != nil {
		return "", fmt.Errorf("parse external usenet output URL: %w", err)
	}
	base := strings.TrimSpace(engine.WebDAVBaseURL)
	if base == "" || !isLikelyInternalExternalEngineHost(parsed) {
		return outputURL, nil
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse webdavBaseUrl: %w", err)
	}
	basePath := strings.TrimRight(baseURL.Path, "/")
	relPath := strings.TrimLeft(parsed.EscapedPath(), "/")
	if unescaped, unescapeErr := url.PathUnescape(relPath); unescapeErr == nil {
		relPath = unescaped
	}
	baseURL.Path = joinURLPath(basePath, relPath)
	baseURL.RawQuery = parsed.RawQuery
	baseURL.Fragment = ""
	return baseURL.String(), nil
}

func isLikelyInternalExternalEngineHost(parsed *url.URL) bool {
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	port := strings.TrimSpace(parsed.Port())
	if host == "localhost" || host == "127.0.0.1" || host == "0.0.0.0" || host == "::1" {
		return port == "" || port == "8080" || port == "3000"
	}
	return false
}

func isExternalPlayableURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	ext := strings.ToLower(path.Ext(parsed.Path))
	_, ok := playableExtensionPriority[ext]
	return ok
}

func (s *Service) findExternalWebDAVMediaFile(ctx context.Context, engine config.UsenetEngineSettings, directoryURL string, depth int) (string, error) {
	if depth > webDAVScanMaxDepth {
		return "", nil
	}
	entries, err := s.listExternalWebDAVDirectory(ctx, engine, directoryURL)
	if err != nil {
		return "", err
	}

	var bestURL string
	var bestSize int64 = -1
	var bestPriority int = 999

	for _, entry := range entries {
		if entry.URL == "" {
			continue
		}
		if entry.IsDir {
			name := strings.ToLower(strings.Trim(strings.TrimSpace(entry.Name), "/"))
			if name == "sample" || name == "samples" || name == "extras" || name == "extra" {
				continue
			}
			nested, nestedErr := s.findExternalWebDAVMediaFile(ctx, engine, entry.URL, depth+1)
			if nestedErr != nil {
				return "", nestedErr
			}
			if nested == "" {
				continue
			}
			nestedSize := entry.Size
			nestedPriority := externalMediaPriority(nested)
			if shouldPreferExternalMedia(nestedPriority, nestedSize, bestPriority, bestSize) {
				bestURL = nested
				bestSize = nestedSize
				bestPriority = nestedPriority
			}
			continue
		}

		if isExternalRcloneLinkURL(entry.URL) {
			if !isExternalPlayableRcloneLink(entry.URL, entry.Name) || isNonContentMediaPath(entry.URL) {
				continue
			}
			resolved, linkErr := s.resolveExternalRcloneLink(ctx, engine, entry.URL)
			if linkErr != nil {
				return "", linkErr
			}
			if resolved == "" {
				continue
			}
			priority := externalMediaPriorityForPath(firstNonEmpty(entry.Name, entry.URL))
			if shouldPreferExternalMedia(priority, entry.Size, bestPriority, bestSize) {
				bestURL = resolved
				bestSize = entry.Size
				bestPriority = priority
			}
			continue
		}

		if !isExternalPlayableURL(entry.URL) || isNonContentMediaPath(entry.URL) {
			continue
		}
		priority := externalMediaPriority(entry.URL)
		if shouldPreferExternalMedia(priority, entry.Size, bestPriority, bestSize) {
			bestURL = entry.URL
			bestSize = entry.Size
			bestPriority = priority
		}
	}

	return bestURL, nil
}

func (s *Service) resolveExternalRcloneLink(ctx context.Context, engine config.UsenetEngineSettings, linkURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, linkURL, nil)
	if err != nil {
		return "", fmt.Errorf("build external WebDAV rclonelink request: %w", err)
	}
	applyExternalWebDAVAuth(req, engine)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("read external WebDAV rclonelink: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("external WebDAV rclonelink returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if err != nil {
		return "", fmt.Errorf("read external WebDAV rclonelink body: %w", err)
	}
	target := strings.Trim(strings.TrimSpace(string(body)), "\x00")
	if target == "" {
		return "", nil
	}
	resolved, err := externalWebDAVURL(engine, target)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func (s *Service) externalWebDAVURLExists(ctx context.Context, engine config.UsenetEngineSettings, rawURL string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return false
	}
	applyExternalWebDAVAuth(req, engine)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return true
	}
	if resp.StatusCode != http.StatusMethodNotAllowed {
		return false
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Range", "bytes=0-0")
	applyExternalWebDAVAuth(req, engine)
	resp, err = s.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

func isExternalRcloneLinkURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(path.Ext(parsed.Path), ".rclonelink")
}

func isExternalPlayableRcloneLink(rawURL string, name string) bool {
	if isExternalPlayableRcloneLinkName(name) {
		return true
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return isExternalPlayableRcloneLinkName(path.Base(parsed.Path))
}

func isExternalPlayableRcloneLinkName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	lower := strings.ToLower(name)
	if !strings.HasSuffix(lower, ".rclonelink") {
		return false
	}
	mediaName := strings.TrimSuffix(name, name[len(name)-len(".rclonelink"):])
	ext := strings.ToLower(path.Ext(mediaName))
	_, ok := playableExtensionPriority[ext]
	return ok
}

func (s *Service) listExternalWebDAVDirectory(ctx context.Context, engine config.UsenetEngineSettings, directoryURL string) ([]webDAVEntry, error) {
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", ensureTrailingSlash(directoryURL), strings.NewReader(`<?xml version="1.0" encoding="utf-8"?><D:propfind xmlns:D="DAV:"><D:prop><D:displayname/><D:getcontentlength/><D:resourcetype/></D:prop></D:propfind>`))
	if err != nil {
		return nil, fmt.Errorf("build WebDAV PROPFIND: %w", err)
	}
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	applyExternalWebDAVAuth(req, engine)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list external WebDAV directory: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("external WebDAV PROPFIND returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var multi webDAVMultiStatus
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&multi); err != nil {
		return nil, fmt.Errorf("parse external WebDAV PROPFIND: %w", err)
	}

	baseURL, err := url.Parse(ensureTrailingSlash(directoryURL))
	if err != nil {
		return nil, fmt.Errorf("parse WebDAV directory URL: %w", err)
	}

	entries := make([]webDAVEntry, 0, len(multi.Responses))
	for _, response := range multi.Responses {
		entryURL := resolveWebDAVHref(baseURL, response.Href)
		if entryURL == "" || sameURLPath(entryURL, baseURL.String()) {
			continue
		}
		prop := response.firstOKProp()
		name := strings.TrimSpace(prop.DisplayName)
		if name == "" {
			if parsed, parseErr := url.Parse(entryURL); parseErr == nil {
				name = path.Base(strings.TrimRight(parsed.Path, "/"))
			}
		}
		entries = append(entries, webDAVEntry{
			Name:  name,
			URL:   entryURL,
			Size:  parseInt64String(prop.ContentLength),
			IsDir: prop.ResourceType.Collection != nil || strings.HasSuffix(entryURL, "/"),
		})
	}
	return entries, nil
}

type webDAVMultiStatus struct {
	Responses []webDAVResponse `xml:"response"`
}

type webDAVResponse struct {
	Href     string            `xml:"href"`
	PropStat []webDAVPropStats `xml:"propstat"`
}

type webDAVPropStats struct {
	Status string        `xml:"status"`
	Prop   webDAVXMLProp `xml:"prop"`
}

type webDAVXMLProp struct {
	DisplayName   string             `xml:"displayname"`
	ContentLength string             `xml:"getcontentlength"`
	ResourceType  webDAVResourceType `xml:"resourcetype"`
}

type webDAVResourceType struct {
	Collection *struct{} `xml:"collection"`
}

func (r webDAVResponse) firstOKProp() webDAVXMLProp {
	for _, propStat := range r.PropStat {
		if strings.Contains(propStat.Status, " 200 ") || strings.HasPrefix(propStat.Status, "HTTP/1.1 200") || strings.TrimSpace(propStat.Status) == "" {
			return propStat.Prop
		}
	}
	if len(r.PropStat) > 0 {
		return r.PropStat[0].Prop
	}
	return webDAVXMLProp{}
}

func resolveWebDAVHref(baseURL *url.URL, href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	ref, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if ref.IsAbs() && isLikelyInternalExternalEngineHost(ref) {
		resolved := *ref
		resolved.Scheme = baseURL.Scheme
		resolved.Host = baseURL.Host

		basePath := strings.TrimRight(baseURL.EscapedPath(), "/")
		refPath := resolved.EscapedPath()
		if basePath != "" && !strings.HasPrefix(strings.TrimRight(refPath, "/")+"/", basePath+"/") {
			relPath := strings.TrimLeft(refPath, "/")
			if unescaped, unescapeErr := url.PathUnescape(relPath); unescapeErr == nil {
				relPath = unescaped
			}
			resolved.Path = joinURLPath(basePath, relPath)
			resolved.RawPath = ""
		}
		return resolved.String()
	}
	resolved := baseURL.ResolveReference(ref)
	return resolved.String()
}

func applyExternalWebDAVAuth(req *http.Request, engine config.UsenetEngineSettings) {
	if req == nil {
		return
	}
	if engine.WebDAVUsername != "" || engine.WebDAVPassword != "" {
		req.SetBasicAuth(engine.WebDAVUsername, engine.WebDAVPassword)
	}
}

func sameURLPath(a string, b string) bool {
	au, errA := url.Parse(a)
	bu, errB := url.Parse(b)
	if errA != nil || errB != nil {
		return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
	}
	return au.Scheme == bu.Scheme && au.Host == bu.Host && strings.TrimRight(au.Path, "/") == strings.TrimRight(bu.Path, "/")
}

func ensureTrailingSlash(rawURL string) string {
	if strings.HasSuffix(rawURL, "/") {
		return rawURL
	}
	return rawURL + "/"
}

func externalMediaPriority(rawURL string) int {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return 999
	}
	return externalMediaPriorityForPath(parsed.Path)
}

func externalMediaPriorityForPath(value string) int {
	if strings.HasSuffix(strings.ToLower(value), ".rclonelink") {
		value = value[:len(value)-len(".rclonelink")]
	}
	if priority, ok := playableExtensionPriority[strings.ToLower(path.Ext(value))]; ok {
		return priority
	}
	return 999
}

func shouldPreferExternalMedia(priority int, size int64, bestPriority int, bestSize int64) bool {
	if bestSize < 0 {
		return true
	}
	if size > 0 && bestSize > 0 && size != bestSize {
		return size > bestSize
	}
	return priority < bestPriority
}

func parseInt64String(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func externalWebDAVRelativePath(engine config.UsenetEngineSettings, outputPath string) string {
	outputPath = strings.TrimSpace(outputPath)
	prefix := strings.TrimSpace(engine.Config["webdavPathPrefix"])
	if prefix != "" {
		if rel, ok := trimPathPrefix(outputPath, prefix); ok {
			return rel
		}
	}
	for _, prefix := range []string{"/mnt/davex", "/mnt/nzbdav"} {
		if rel, ok := trimPathPrefix(outputPath, prefix); ok {
			return rel
		}
	}
	for _, marker := range []string{"/webdav/", "/completed-symlinks/", "/completed-downloads/", "/content/", "/__all__/", "/nzbs/"} {
		if idx := strings.Index(outputPath, marker); idx >= 0 {
			if marker == "/webdav/" {
				return outputPath[idx+len(marker):]
			}
			return outputPath[idx+1:]
		}
	}
	return strings.TrimLeft(outputPath, "/")
}

func trimPathPrefix(value, prefix string) (string, bool) {
	value = strings.TrimSpace(value)
	prefix = strings.TrimRight(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		return value, true
	}
	if value == prefix {
		return "", true
	}
	if strings.HasPrefix(value, prefix+"/") {
		return strings.TrimLeft(strings.TrimPrefix(value, prefix), "/"), true
	}
	return "", false
}

func joinURLPath(basePath, relPath string) string {
	switch {
	case basePath == "" && relPath == "":
		return "/"
	case basePath == "":
		return "/" + relPath
	case relPath == "":
		return basePath
	default:
		return basePath + "/" + relPath
	}
}

func estimateNZBFileSize(nzbBytes []byte) int64 {
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
	return fileSize
}

func firstPositiveInt64(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func externalUsenetEnabledForCandidate(settings config.Settings, candidate models.NZBResult) bool {
	return externalUsenetEnabledForProfile(settings, strings.TrimSpace(candidate.Attributes["profileId"]))
}

func externalUsenetEnabledForProfile(settings config.Settings, profileID string) bool {
	return len(usenetengine.EnabledEngines(settings, profileID)) > 0
}

func (s *Service) fetchNZB(ctx context.Context, downloadURL string, candidate models.NZBResult) ([]byte, string, error) {
	fetchNum := s.nzbFetchCount.Add(1)
	log.Printf("[search-stats] NZB fetch #%d started (title=%q, indexer=%q)", fetchNum, strings.TrimSpace(candidate.Title), strings.TrimSpace(candidate.Indexer))
	log.Printf("[playback] fetching nzb url=%q title=%q", downloadURL, strings.TrimSpace(candidate.Title))

	// Large NZBs for full-disc releases can be 10+ MB and some indexers stream
	// them slowly. Keep the bound finite, but avoid failing valid releases while
	// reading the body.
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build nzb request: %w", err)
	}
	httpheaders.SetNZBDownloadHeaders(req)

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
const (
	suspiciousSelectedMediaMinAdvertisedSize = 300 * 1024 * 1024
	suspiciousSelectedMediaMaxSize           = 100 * 1024 * 1024
	suspiciousSelectedMediaRatioDivisor      = 4
)

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

var episodeSpecificReleasePattern = regexp.MustCompile(`(?i)s\d{1,2}\s*e\d{1,4}`)

type webDAVEntry struct {
	Name  string
	URL   string
	Size  int64
	IsDir bool
}

type mediaFileCandidate struct {
	path     string
	priority int
}

func isNonContentMediaPath(mediaPath string) bool {
	base := strings.ToLower(path.Base(strings.TrimSpace(mediaPath)))
	if base == "" || base == "." || base == "/" {
		return false
	}
	if _, ok := playableExtensionPriority[strings.ToLower(path.Ext(base))]; !ok {
		return false
	}
	return strings.Contains(base, "sample") ||
		strings.Contains(base, "extras") ||
		strings.Contains(base, "trailer") ||
		strings.Contains(base, "featurette") ||
		strings.Contains(base, "bonus") ||
		strings.Contains(base, "promo")
}

func fileSizeFromPtr(size *int64) int64 {
	if size == nil {
		return 0
	}
	return *size
}

func (s *Service) validateResolvedMediaFile(finalPath, storagePath string, candidate models.NZBResult) error {
	if s.metadataSvc == nil {
		return nil
	}
	selectedSize := s.fileSizeForPath(finalPath)
	if selectedSize <= 0 {
		return nil
	}

	expectedSize := expectedPerFileSize(candidate)
	if expectedSize <= 0 {
		return nil
	}

	if selectedSize < suspiciousSelectedMediaMaxSize &&
		expectedSize >= suspiciousSelectedMediaMinAdvertisedSize &&
		selectedSize*suspiciousSelectedMediaRatioDivisor < expectedSize {
		log.Printf("[playback] rejected suspiciously small media selection finalPath=%q storagePath=%q selectedSize=%d expectedSize=%d title=%q",
			finalPath, storagePath, selectedSize, expectedSize, candidate.Title)
		return fmt.Errorf("resolved media file appears to be a short sample: %s (%d MB selected from %d MB release)",
			path.Base(finalPath), selectedSize/(1024*1024), expectedSize/(1024*1024))
	}

	return nil
}

func (s *Service) fileSizeForPath(filePath string) int64 {
	if s.metadataSvc == nil || strings.TrimSpace(filePath) == "" {
		return 0
	}
	meta, err := s.metadataSvc.GetFileMetadata(filePath)
	if err != nil {
		log.Printf("[playback] failed to read metadata for selected media file %q: %v", filePath, err)
		return 0
	}
	if meta == nil {
		return 0
	}
	return meta.GetFileSize()
}

func expectedPerFileSize(candidate models.NZBResult) int64 {
	if candidate.SizeBytes <= 0 {
		return 0
	}
	if candidate.SizePerFile {
		return candidate.SizeBytes
	}
	if candidate.EpisodeCount > 1 {
		return 0
	}
	if isEpisodeSpecificRelease(candidate) {
		return candidate.SizeBytes
	}
	return 0
}

func isEpisodeSpecificRelease(candidate models.NZBResult) bool {
	title := strings.ToLower(strings.TrimSpace(candidate.Title))
	if title == "" {
		return false
	}
	if candidate.Attributes != nil {
		if code := strings.ToLower(strings.TrimSpace(candidate.Attributes["targetEpisodeCode"])); code != "" && strings.Contains(title, strings.ToLower(code)) {
			return true
		}
		if season, _ := strconv.Atoi(strings.TrimSpace(candidate.Attributes["targetSeason"])); season > 0 {
			if episode, _ := strconv.Atoi(strings.TrimSpace(candidate.Attributes["targetEpisode"])); episode > 0 {
				if strings.Contains(title, fmt.Sprintf("s%02de%02d", season, episode)) {
					return true
				}
			}
		}
	}
	return episodeSpecificReleasePattern.MatchString(title)
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
	if isNonContentMediaPath(finalPath) {
		return nil, fmt.Errorf("resolved media path appears to be a sample/extras file: %s", path.Base(finalPath))
	}
	if err := s.validateResolvedMediaFile(finalPath, storagePath, models.NZBResult{
		Title:     meta.SourceNZBPath,
		SizeBytes: fileSizeFromPtr(queueItem.FileSize),
	}); err != nil {
		return nil, err
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
