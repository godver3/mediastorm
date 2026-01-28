package importer

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	metapb "novastream/internal/nzb/metadata/proto"
	"novastream/internal/usenet"

	"github.com/javi11/nntppool"
	"github.com/sourcegraph/conc/pool"
)

// ParallelRarDownloader handles parallel downloading of RAR parts to memory
type ParallelRarDownloader struct {
	log            *slog.Logger
	poolManager    nntppool.UsenetConnectionPool
	maxWorkers     int
	maxCacheSizeMB int
}

// NewParallelRarDownloader creates a new parallel RAR downloader
func NewParallelRarDownloader(poolManager nntppool.UsenetConnectionPool, maxWorkers int, maxCacheSizeMB int) *ParallelRarDownloader {
	return &ParallelRarDownloader{
		log:            slog.Default().With("component", "parallel-rar-downloader"),
		poolManager:    poolManager,
		maxWorkers:     maxWorkers,
		maxCacheSizeMB: maxCacheSizeMB,
	}
}

// DownloadRarPartsToMemory downloads all RAR parts to memory in parallel
func (prd *ParallelRarDownloader) DownloadRarPartsToMemory(ctx context.Context, rarFiles []ParsedFile) (map[string][]byte, error) {
	if len(rarFiles) == 0 {
		return make(map[string][]byte), nil
	}

	prd.log.Info("Starting parallel RAR part download",
		"total_parts", len(rarFiles),
		"max_workers", prd.maxWorkers)

	downloadStart := time.Now()

	// Create worker pool for parallel downloads
	workerPool := pool.New().WithMaxGoroutines(prd.maxWorkers)

	// Results storage
	results := make(map[string][]byte, len(rarFiles))
	resultsMu := sync.Mutex{}

	// Error tracking
	var downloadErrors []error
	errorsMu := sync.Mutex{}

	// Download each RAR part in parallel
	for _, rarFile := range rarFiles {
		rarFile := rarFile // capture loop variable

		workerPool.Go(func() {
			content, err := prd.downloadSingleRarPart(ctx, rarFile)

			resultsMu.Lock()
			if err != nil {
				errorsMu.Lock()
				downloadErrors = append(downloadErrors, fmt.Errorf("failed to download %s: %w", rarFile.Filename, err))
				errorsMu.Unlock()
			} else {
				results[rarFile.Filename] = content
				prd.log.Debug("Downloaded RAR part to memory",
					"filename", rarFile.Filename,
					"size_mb", len(content)/(1024*1024))
			}
			resultsMu.Unlock()
		})
	}

	// Wait for all downloads to complete
	workerPool.Wait()

	downloadDuration := time.Since(downloadStart)

	// Check for errors
	errorsMu.Lock()
	hasErrors := len(downloadErrors) > 0
	errorsMu.Unlock()

	if hasErrors {
		errorsMu.Lock()
		prd.log.Warn("Some RAR parts failed to download",
			"failed_count", len(downloadErrors),
			"total_parts", len(rarFiles))
		errorsMu.Unlock()
	}

	resultsMu.Lock()
	successCount := len(results)
	resultsMu.Unlock()

	prd.log.Info("Completed parallel RAR part download",
		"successful_downloads", successCount,
		"total_parts", len(rarFiles),
		"duration", downloadDuration,
		"avg_duration_per_part", downloadDuration/time.Duration(len(rarFiles)))

	// If we have some successful downloads, return what we have
	// The rarlist library can work with partial archives in some cases
	if successCount > 0 {
		return results, nil
	}

	// If no downloads succeeded, return the first error
	errorsMu.Lock()
	defer errorsMu.Unlock()
	if len(downloadErrors) > 0 {
		return nil, downloadErrors[0]
	}

	return nil, fmt.Errorf("no RAR parts could be downloaded")
}

// maxSegmentsPerRarPart is the maximum number of segments allowed per RAR part
// to prevent memory explosion. With ~750KB per segment, 200 segments = ~150MB max per part.
const maxSegmentsPerRarPart = 200

// downloadSingleRarPart downloads a single RAR part to memory
func (prd *ParallelRarDownloader) downloadSingleRarPart(ctx context.Context, rarFile ParsedFile) ([]byte, error) {
	// Check segment count upfront to prevent memory explosion
	if len(rarFile.Segments) > maxSegmentsPerRarPart {
		return nil, fmt.Errorf("RAR part %s has too many segments (%d > %d max), file too large for memory import",
			rarFile.Filename, len(rarFile.Segments), maxSegmentsPerRarPart)
	}

	// Create a segment loader for this RAR file
	loader := parallelDbSegmentLoader{segs: rarFile.Segments}

	// Get segments for the entire file (limited by check above)
	rg := usenet.GetSegmentsInRange(0, rarFile.Size-1, loader)

	// Create a Usenet reader for the entire file
	reader, err := usenet.NewUsenetReader(ctx, prd.poolManager, rg, prd.maxWorkers, prd.maxCacheSizeMB)
	if err != nil {
		return nil, fmt.Errorf("failed to create usenet reader: %w", err)
	}
	defer reader.Close()

	// Read the entire file into memory
	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read RAR part: %w", err)
	}

	// Validate that we got the expected amount of data
	if int64(len(content)) != rarFile.Size {
		return nil, fmt.Errorf("size mismatch: expected %d bytes, got %d bytes", rarFile.Size, len(content))
	}

	return content, nil
}

// parallelDbSegmentLoader implements the segment loader interface for database segments
type parallelDbSegmentLoader struct {
	segs []*metapb.SegmentData
}

func (dl parallelDbSegmentLoader) GetSegmentCount() int {
	return len(dl.segs)
}

func (dl parallelDbSegmentLoader) GetSegment(index int) (segment usenet.Segment, groups []string, ok bool) {
	if index < 0 || index >= len(dl.segs) {
		return usenet.Segment{}, nil, false
	}
	seg := dl.segs[index]

	return usenet.Segment{
		Id:    seg.Id,
		Start: seg.StartOffset,
		Size:  seg.SegmentSize,
	}, nil, true
}
