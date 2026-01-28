package usenet

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/javi11/nntppool"
	"github.com/sourcegraph/conc/pool"
)

const defaultDownloadWorkers = 15

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
	log                *slog.Logger
	wg                 sync.WaitGroup
	cancel             context.CancelFunc
	rg                 segmentRange
	maxDownloadWorkers int
	init               chan any
	initDownload       sync.Once
	totalBytesRead     int64
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

	// Calculate memory usage estimate
	var totalSegmentSize int64
	for _, seg := range rg.segments {
		totalSegmentSize += seg.SegmentSize
	}

	log.InfoContext(ctx, "usenet.reader.init",
		"segments", len(rg.segments),
		"range_start", rg.start,
		"range_end", rg.end,
		"max_download_workers", maxDownloadWorkers,
	)
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
		b.log.Debug("usenet.reader.closing",
			"total_bytes_read", b.totalBytesRead,
			"segments_count", len(b.rg.segments),
		)

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
		b.log.Debug("usenet.reader.start_download")
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
				"segment_id", s.Id,
				"chunk_bytes", nn,
				"segment_bytes_read", s.BytesRead(),
				"segment_expected", s.Length(),
				"reader_total_bytes", totalRead,
				"buffer_size", len(p),
			)
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

		// Log concurrent download setup
		b.log.InfoContext(ctx, "usenet.download_manager.starting",
			"total_segments", len(b.rg.segments),
			"max_workers", downloadWorkers,
		)

		// Download all segments in the range (now limited at segment range level)
		for _, seg := range b.rg.segments {
			if ctx.Err() != nil {
				break
			}

			s := seg
			segmentID := s.Id
			pool.Go(func(c context.Context) error {
				w := s.writer
				b.log.DebugContext(ctx, "usenet.segment.download_starting",
					"segment_id", segmentID,
					"segment_size", s.SegmentSize,
				)

				// Set the item ready to read with retry logic for incomplete downloads
				_, err := cp.Body(ctx, segmentID, s.Writer(), s.groups)
				if !errors.Is(err, context.Canceled) {
					cErr := w.CloseWithError(err)
					if cErr != nil {
						b.log.ErrorContext(ctx, "Error closing segment buffer:", "error", cErr)
					}

					if err != nil && !errors.Is(err, context.Canceled) {
						b.log.WarnContext(ctx, "usenet segment fetch error", "segment_id", segmentID, "error", err)
						return err
					}

					return nil
				}

				err = w.Close()
				if err != nil {
					b.log.ErrorContext(ctx, "Error closing segment writer:", "error", err)
				}

				return nil
			})
		}

		if err := pool.Wait(); err != nil {
			b.log.DebugContext(ctx, "Error downloading segments:", "error", err)
			return
		}
	case <-ctx.Done():
		return
	}
}
