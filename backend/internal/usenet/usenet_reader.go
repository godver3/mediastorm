package usenet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/javi11/nntppool"
	"github.com/sourcegraph/conc/pool"
)

// Global usenet memory tracking for diagnostics
var (
	activeReaders   int64 // number of active usenet readers
	activeSegments  int64 // total segments across all active readers
	estimatedMemory int64 // estimated total bytes in usenet pipelines
	readerIDCounter int64
	debugReaderLogs = strings.EqualFold(strings.TrimSpace(os.Getenv("STRMR_USENET_READER_LOGS")), "1") ||
		strings.EqualFold(strings.TrimSpace(os.Getenv("STRMR_USENET_READER_LOGS")), "true")
)

const defaultDownloadWorkers = 15

// ActiveReaders returns the current number of active usenet readers.
func ActiveReaders() int64 {
	return atomic.LoadInt64(&activeReaders)
}

var (
	_ io.ReadCloser = &usenetReader{}
)

type ArticleNotFoundError struct {
	UnderlyingErr error
	BytesRead     int64
}

func (e *ArticleNotFoundError) Error() string {
	return e.UnderlyingErr.Error()
}

func (e *ArticleNotFoundError) Unwrap() error {
	return e.UnderlyingErr
}

type usenetReader struct {
	id                 int64
	log                *slog.Logger
	wg                 sync.WaitGroup
	cancel             context.CancelFunc
	rg                 segmentRange
	maxDownloadWorkers int
	init               chan any
	initDownload       sync.Once
	totalBytesRead     int64
	readStartedAt      time.Time
	lastReadLogAt      time.Time
	lastReadLogBytes   int64
	mu                 sync.Mutex
	closeOnce          sync.Once
	// Sliding window state for memory-efficient streaming
	windowStart int
	windowSize  int
	windowMu    sync.Mutex
}

func NewUsenetReader(
	ctx context.Context,
	cp nntppool.UsenetConnectionPool,
	rg segmentRange,
	maxDownloadWorkers int,
	maxCacheSizeMB ...int, // Optional parameter for compatibility
) (io.ReadCloser, error) {
	log := slog.Default()
	readerID := atomic.AddInt64(&readerIDCounter, 1)

	// Calculate memory usage estimate
	var totalSegmentSize int64
	for _, seg := range rg.segments {
		totalSegmentSize += seg.SegmentSize
	}

	readers := atomic.AddInt64(&activeReaders, 1)
	segs := atomic.AddInt64(&activeSegments, int64(len(rg.segments)))
	estMem := atomic.AddInt64(&estimatedMemory, totalSegmentSize)

	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	if debugReaderLogs {
		log.InfoContext(ctx, "usenet.reader.init",
			"reader_id", readerID,
			"segments", len(rg.segments),
			"range_start", rg.start,
			"range_end", rg.end,
			"range_bytes", rg.end-rg.start+1,
			"max_download_workers", maxDownloadWorkers,
			"est_segment_bytes_mb", totalSegmentSize/1024/1024,
			"global_active_readers", readers,
			"global_active_segments", segs,
			"global_est_memory_mb", estMem/1024/1024,
			"heap_alloc_mb", m.HeapAlloc/1024/1024,
			"pool", summarizePoolSnapshot(cp),
		)
	}
	ctx, cancel := context.WithCancel(ctx)

	// Calculate optimal window size based on workers and total segments
	windowSize := maxDownloadWorkers * 2
	if windowSize > 20 {
		windowSize = 20
	}
	if windowSize < 5 {
		windowSize = 5
	}

	ur := &usenetReader{
		id:                 readerID,
		log:                log,
		cancel:             cancel,
		rg:                 rg,
		init:               make(chan any, 1),
		maxDownloadWorkers: maxDownloadWorkers,
		windowStart:        0,
		windowSize:         windowSize,
	}

	// Will start go routine pool with max download workers that will fill the cache

	ur.wg.Add(1)
	go func() {
		defer ur.wg.Done()
		ur.downloadManager(ctx, cp)
	}()

	return ur, nil
}

func (b *usenetReader) Close() error {
	b.closeOnce.Do(func() {
		// Decrement global counters
		var totalSegSize int64
		for _, seg := range b.rg.segments {
			totalSegSize += seg.SegmentSize
		}
		readers := atomic.AddInt64(&activeReaders, -1)
		segs := atomic.AddInt64(&activeSegments, -int64(len(b.rg.segments)))
		estMem := atomic.AddInt64(&estimatedMemory, -totalSegSize)

		b.mu.Lock()
		totalBytesRead := b.totalBytesRead
		readStartedAt := b.readStartedAt
		b.mu.Unlock()
		var avgDeliveryMBps float64
		if !readStartedAt.IsZero() {
			avgDeliveryMBps = mbps(totalBytesRead, time.Since(readStartedAt))
		}

		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		if debugReaderLogs {
			b.log.Info("usenet.reader.closing",
				"reader_id", b.id,
				"total_bytes_read", totalBytesRead,
				"avg_delivery_mbps", avgDeliveryMBps,
				"segments_count", len(b.rg.segments),
				"freed_est_mb", totalSegSize/1024/1024,
				"global_active_readers", readers,
				"global_active_segments", segs,
				"global_est_memory_mb", estMem/1024/1024,
				"heap_alloc_mb", m.HeapAlloc/1024/1024,
			)
		}

		b.cancel()
		close(b.init)

		go func() {
			// Use a timeout to prevent cleanup from blocking indefinitely
			// if download workers are stuck on network I/O
			done := make(chan struct{})
			go func() {
				b.wg.Wait()
				close(done)
			}()

			select {
			case <-done:
				// Normal cleanup path
			case <-time.After(30 * time.Second):
				b.log.Warn("usenet.reader.cleanup_timeout",
					"reader_id", b.id,
					"total_bytes_read", b.totalBytesRead,
					"segments_count", len(b.rg.segments),
				)
				// Continue with cleanup anyway to free memory
			}

			_ = b.rg.Clear()
			b.rg = segmentRange{}

			b.log.Debug("usenet.reader.cleanup_complete")
		}()
	})

	return nil
}

// Read reads len(p) byte from the Buffer starting at the current offset.
// It returns the number of bytes read and an error if any.
// Returns io.EOF error if pointer is at the end of the Buffer.
func (b *usenetReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	b.initDownload.Do(func() {
		b.mu.Lock()
		b.readStartedAt = time.Now()
		b.lastReadLogAt = b.readStartedAt
		b.mu.Unlock()
		if debugReaderLogs {
			b.log.Info("usenet.reader.start_download",
				"reader_id", b.id,
				"segments", len(b.rg.segments),
				"range_start", b.rg.start,
				"range_end", b.rg.end,
				"range_bytes", b.rg.end-b.rg.start+1,
				"max_download_workers", b.maxDownloadWorkers,
			)
		}
		b.init <- struct{}{}
	})

	s, err := b.rg.Get()
	if err != nil {
		// Check if this is an article not found error
		b.mu.Lock()
		totalRead := b.totalBytesRead
		b.mu.Unlock()

		if b.isArticleNotFoundError(err) {
			if totalRead > 0 {
				// We read some data before failing - this is partial content
				return 0, &ArticleNotFoundError{
					UnderlyingErr: err,
					BytesRead:     totalRead,
				}
			}
			// No data read at all - this is corrupted/missing
			return 0, &ArticleNotFoundError{
				UnderlyingErr: err,
				BytesRead:     0,
			}
		}
		return 0, io.EOF
	}

	n := 0
	for n < len(p) {
		reader := s.GetReader()
		nn, err := reader.Read(p[n:])
		n += nn
		s.addBytesRead(nn)

		if err != nil && !errors.Is(err, io.EOF) {
			b.log.Warn("usenet segment read error",
				"segment_id", s.Id,
				"bytes", nn,
				"total_read", n,
				"error", err,
			)
		}

		// Track total bytes read
		b.mu.Lock()
		b.totalBytesRead += int64(nn)
		totalRead := b.totalBytesRead
		b.mu.Unlock()

		if nn > 0 {
			b.log.Debug("usenet.reader.bytes_read",
				"reader_id", b.id,
				"segment_id", s.Id,
				"chunk_bytes", nn,
				"segment_bytes_read", s.BytesRead(),
				"segment_expected", s.Length(),
				"reader_total_bytes", totalRead,
				"buffer_size", len(p),
			)
			b.maybeLogReadThroughput(totalRead)
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				segmentTotal := s.BytesRead()
				expected := s.Length()

				if segmentTotal > 0 && segmentTotal < expected && !s.HitNNTPBufferLimit() {
					s.adjustToBytesRead(segmentTotal)
					expected = s.Length()
				}

				b.log.Debug("usenet.segment.eof",
					"segment_id", s.Id,
					"segment_bytes_read", segmentTotal,
					"expected", expected,
					"hit_buffer_limit", s.HitNNTPBufferLimit(),
					"is_complete", s.IsComplete(),
				)

				// Validate segment boundary - bytesRead should match expected
				if !s.IsComplete() {
					if s.HitNNTPBufferLimit() {
						b.log.Warn(
							"usenet segment truncated by NNTP buffer limit",
							"segment_id", s.Id,
							"bytes_read", segmentTotal,
							"expected", expected,
						)
					} else if s.IsIncomplete() {
						b.log.Error(
							"usenet segment incomplete",
							"segment_id", s.Id,
							"bytes_read", segmentTotal,
							"expected", expected,
							"missing", expected-segmentTotal,
						)
					} else {
						b.log.Warn(
							"usenet segment boundary mismatch",
							"segment_id", s.Id,
							"bytes_read", segmentTotal,
							"expected", expected,
						)
					}
				}

				// Segment is fully read, remove it from the cache
				s, err = b.rg.Next()
				if err != nil {
					if n > 0 {
						return n, nil
					}

					// Check if this is an article not found error for next segment
					if b.isArticleNotFoundError(err) {
						if totalRead > 0 {
							// Return what we have read so far and the article error
							return n, &ArticleNotFoundError{
								UnderlyingErr: err,
								BytesRead:     totalRead,
							}
						}
					}
					return n, io.EOF
				}
			} else {
				// Check if this is an article not found error
				if b.isArticleNotFoundError(err) {
					b.log.Warn(
						"usenet segment missing from providers",
						"segment_id", s.Id,
						"bytes_read", s.BytesRead(),
						"total_bytes", totalRead,
						"error", err,
					)
					return n, &ArticleNotFoundError{
						UnderlyingErr: err,
						BytesRead:     totalRead,
					}
				}
				return n, err
			}
		}
	}

	return n, nil
}

// isArticleNotFoundError checks if the error indicates articles were not found in providers
func (b *usenetReader) isArticleNotFoundError(err error) bool {
	return errors.Is(err, nntppool.ErrArticleNotFoundInProviders)
}

func (b *usenetReader) maybeLogReadThroughput(totalRead int64) {
	if !debugReaderLogs {
		return
	}
	now := time.Now()

	b.mu.Lock()
	startedAt := b.readStartedAt
	lastAt := b.lastReadLogAt
	lastBytes := b.lastReadLogBytes
	if startedAt.IsZero() {
		b.mu.Unlock()
		return
	}
	if now.Sub(lastAt) < 15*time.Second && totalRead-lastBytes < 64*1024*1024 {
		b.mu.Unlock()
		return
	}
	b.lastReadLogAt = now
	b.lastReadLogBytes = totalRead
	currentSegment := b.rg.current
	segmentCount := len(b.rg.segments)
	b.mu.Unlock()

	b.log.Info("usenet.reader.throughput",
		"reader_id", b.id,
		"delivered_bytes", totalRead,
		"delivered_mb", totalRead/1024/1024,
		"avg_delivery_mbps", mbps(totalRead, now.Sub(startedAt)),
		"recent_delivery_mbps", mbps(totalRead-lastBytes, now.Sub(lastAt)),
		"segment_index", currentSegment,
		"segments", segmentCount,
		"elapsed", now.Sub(startedAt).String(),
	)
}

func (b *usenetReader) downloadManager(
	ctx context.Context,
	cp nntppool.UsenetConnectionPool,
) {
	select {
	case _, ok := <-b.init:
		if !ok {
			return
		}

		downloadWorkers := b.maxDownloadWorkers
		if downloadWorkers == 0 {
			downloadWorkers = defaultDownloadWorkers
		}

		pool := pool.New().
			WithMaxGoroutines(downloadWorkers).
			WithContext(ctx)

		var activeDownloads int64
		var completedDownloads int64
		var failedDownloads int64
		var downloadedBytes int64

		if debugReaderLogs {
			b.log.InfoContext(ctx, "usenet.download_manager.starting",
				"reader_id", b.id,
				"total_segments", len(b.rg.segments),
				"max_workers", downloadWorkers,
				"pool", summarizePoolSnapshot(cp),
			)
		}

		progressDone := make(chan struct{})
		if debugReaderLogs {
			go b.logDownloadProgress(ctx, cp, progressDone, &activeDownloads, &completedDownloads, &failedDownloads, &downloadedBytes)
		}

		// Download all segments in the range (now limited at segment range level)
		for _, seg := range b.rg.segments {
			if ctx.Err() != nil {
				break
			}

			s := seg
			segmentID := s.Id
			pool.Go(func(c context.Context) error {
				w := s.writer
				startedAt := time.Now()
				active := atomic.AddInt64(&activeDownloads, 1)
				b.log.DebugContext(ctx, "usenet.segment.download_starting",
					"reader_id", b.id,
					"segment_id", segmentID,
					"segment_size", s.SegmentSize,
					"active_downloads", active,
				)
				defer atomic.AddInt64(&activeDownloads, -1)

				// Set the item ready to read with retry logic for incomplete downloads
				bytesFetched, err := cp.Body(ctx, segmentID, s.Writer(), s.groups)
				duration := time.Since(startedAt)
				if bytesFetched > 0 {
					atomic.AddInt64(&downloadedBytes, bytesFetched)
				}
				if !errors.Is(err, context.Canceled) {
					cErr := w.CloseWithError(err)
					if cErr != nil {
						b.log.ErrorContext(ctx, "Error closing segment buffer:",
							"reader_id", b.id,
							"segment_id", segmentID,
							"error", cErr,
						)
					}

					if err != nil && !errors.Is(err, context.Canceled) {
						atomic.AddInt64(&failedDownloads, 1)
						b.log.WarnContext(ctx, "usenet.segment.fetch_error",
							"reader_id", b.id,
							"segment_id", segmentID,
							"segment_size", s.SegmentSize,
							"bytes_fetched", bytesFetched,
							"duration", duration.String(),
							"download_mbps", mbps(bytesFetched, duration),
							"active_downloads", atomic.LoadInt64(&activeDownloads),
							"error", err,
							"pool", summarizePoolSnapshot(cp),
						)
						return err
					}

					completed := atomic.AddInt64(&completedDownloads, 1)
					b.log.DebugContext(ctx, "usenet.segment.fetch_complete",
						"reader_id", b.id,
						"segment_id", segmentID,
						"segment_size", s.SegmentSize,
						"bytes_fetched", bytesFetched,
						"duration", duration.String(),
						"download_mbps", mbps(bytesFetched, duration),
						"completed_segments", completed,
						"total_segments", len(b.rg.segments),
						"active_downloads", atomic.LoadInt64(&activeDownloads),
					)
					return nil
				}

				err = w.Close()
				if err != nil {
					b.log.ErrorContext(ctx, "Error closing segment writer:",
						"reader_id", b.id,
						"segment_id", segmentID,
						"error", err,
					)
				}

				return nil
			})
		}

		if err := pool.Wait(); err != nil {
			if debugReaderLogs {
				close(progressDone)
			}
			b.log.DebugContext(ctx, "Error downloading segments:",
				"reader_id", b.id,
				"error", err,
				"pool", summarizePoolSnapshot(cp),
			)
			return
		}
		if debugReaderLogs {
			close(progressDone)
			b.log.InfoContext(ctx, "usenet.download_manager.complete",
				"reader_id", b.id,
				"total_segments", len(b.rg.segments),
				"completed_segments", atomic.LoadInt64(&completedDownloads),
				"failed_segments", atomic.LoadInt64(&failedDownloads),
				"downloaded_mb", atomic.LoadInt64(&downloadedBytes)/1024/1024,
				"pool", summarizePoolSnapshot(cp),
			)
		}
	case <-ctx.Done():
		return
	}
}

func (b *usenetReader) logDownloadProgress(
	ctx context.Context,
	cp nntppool.UsenetConnectionPool,
	done <-chan struct{},
	activeDownloads, completedDownloads, failedDownloads, downloadedBytes *int64,
) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	startedAt := time.Now()
	var lastBytes int64
	var lastAt = startedAt

	for {
		select {
		case <-ticker.C:
			now := time.Now()
			currentBytes := atomic.LoadInt64(downloadedBytes)
			b.mu.Lock()
			deliveredBytes := b.totalBytesRead
			currentSegment := b.rg.current
			b.mu.Unlock()
			b.log.InfoContext(ctx, "usenet.download_manager.progress",
				"reader_id", b.id,
				"active_downloads", atomic.LoadInt64(activeDownloads),
				"completed_segments", atomic.LoadInt64(completedDownloads),
				"failed_segments", atomic.LoadInt64(failedDownloads),
				"total_segments", len(b.rg.segments),
				"downloaded_mb", currentBytes/1024/1024,
				"delivered_mb", deliveredBytes/1024/1024,
				"avg_download_mbps", mbps(currentBytes, now.Sub(startedAt)),
				"recent_download_mbps", mbps(currentBytes-lastBytes, now.Sub(lastAt)),
				"avg_delivery_mbps", mbps(deliveredBytes, now.Sub(startedAt)),
				"segment_index", currentSegment,
				"pool", summarizePoolSnapshot(cp),
			)
			lastBytes = currentBytes
			lastAt = now
		case <-done:
			return
		case <-ctx.Done():
			return
		}
	}
}

// GlobalReaderStats returns current global usenet reader memory diagnostics.
func GlobalReaderStats() (readers, segments, estMemoryMB int64) {
	return atomic.LoadInt64(&activeReaders),
		atomic.LoadInt64(&activeSegments),
		atomic.LoadInt64(&estimatedMemory) / 1024 / 1024
}

func mbps(bytes int64, duration time.Duration) float64 {
	if bytes <= 0 || duration <= 0 {
		return 0
	}
	return float64(bytes) / 1024 / 1024 / duration.Seconds()
}

func summarizePoolSnapshot(cp nntppool.UsenetConnectionPool) string {
	if cp == nil {
		return "unavailable"
	}

	snapshot := cp.GetMetricsSnapshot()
	providers := make([]string, 0, len(snapshot.ProviderMetrics))
	for _, p := range snapshot.ProviderMetrics {
		providers = append(providers, fmt.Sprintf(
			"%s state=%v conn=%d/%d acquired=%d idle=%d constructing=%d empty_acquires=%d empty_wait=%s bytes_down_mb=%d articles=%d success=%.1f%%",
			p.ProviderID,
			p.State,
			p.TotalConnections,
			p.MaxConnections,
			p.AcquiredConnections,
			p.IdleConnections,
			p.ConstructingConnections,
			p.EmptyAcquireCount,
			p.EmptyAcquireWaitTime,
			p.TotalBytesDownloaded/1024/1024,
			p.TotalArticlesRetrieved,
			p.SuccessRate,
		))
	}

	return fmt.Sprintf(
		"conn=%d acquired=%d idle=%d active=%d acquires=%d releases=%d avg_wait=%s recent_down=%.2fMBps historical_down=%.2fMBps errors=%d retries=%d providers=[%s]",
		snapshot.TotalConnections,
		snapshot.AcquiredConnections,
		snapshot.IdleConnections,
		snapshot.ActiveConnections,
		snapshot.TotalAcquires,
		snapshot.TotalReleases,
		snapshot.AverageAcquireWaitTime,
		snapshot.DownloadSpeed/1024/1024,
		snapshot.HistoricalDownloadSpeed/1024/1024,
		snapshot.TotalErrors,
		snapshot.TotalRetries,
		strings.Join(providers, "; "),
	)
}
