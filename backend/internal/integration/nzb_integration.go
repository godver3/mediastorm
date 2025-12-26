package integration

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"novastream/config"
	"novastream/internal/database"
	"novastream/internal/importer"
	"novastream/internal/nzb/metadata"
	"novastream/internal/nzbfilesystem"
	"novastream/internal/pool"
	"novastream/services/streaming"
	"github.com/spf13/afero"
)

// NzbConfig holds configuration for the NZB system
type NzbConfig struct {
	QueueDatabasePath   string
	MetadataRootPath    string // Path to metadata root directory
	Password            string // Global password for .bin files
	Salt                string // Global salt for .bin files
	MaxProcessorWorkers int    // Number of queue workers (default: 2)
	MaxDownloadWorkers  int    // Number of download workers (default: 15)
}

// NzbSystem represents the complete NZB-backed filesystem
type NzbSystem struct {
	database       *database.DB             // Database for processing queue
	metadataReader *metadata.MetadataReader // Metadata reader for serving files
	service        *importer.Service
	fs             afero.Fs
	nzbFs          *nzbfilesystem.NzbFilesystem // Concrete type for context-aware operations
	poolManager    pool.Manager

	// Configuration tracking for dynamic updates
	maxDownloadWorkers  int
	maxProcessorWorkers int
	configMutex         sync.RWMutex
}

// NewNzbSystem creates a new NZB-backed virtual filesystem with metadata + queue architecture
func NewNzbSystem(config NzbConfig, poolManager pool.Manager, configGetter config.ConfigGetter) (*NzbSystem, error) {
	// Initialize metadata service for serving files
	metadataService := metadata.NewMetadataService(config.MetadataRootPath)
	metadataReader := metadata.NewMetadataReader(metadataService)

	// Initialize database (for processing queue)
	dbConfig := database.Config{
		DatabasePath: config.QueueDatabasePath,
	}

	db, err := database.NewDB(dbConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	// Set defaults for workers and scan interval if not configured
	maxProcessorWorkers := config.MaxProcessorWorkers
	if maxProcessorWorkers <= 0 {
		maxProcessorWorkers = 2 // Default: 2 parallel workers
	}

	maxDownloadWorkers := config.MaxDownloadWorkers
	if maxDownloadWorkers <= 0 {
		maxDownloadWorkers = 15 // Default: 15 download workers
	}

	// Create NZB service using metadata + queue
	serviceConfig := importer.ServiceConfig{
		Workers: maxProcessorWorkers,
	}

	// Create service with poolManager for dynamic pool access
	service, err := importer.NewService(serviceConfig, metadataService, db, poolManager, configGetter)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create NZB service: %w", err)
	}

	// Create health repository for file health tracking
	// Note: MediaRepository is not available in this integration context,
	// but basic health checking functionality will still work
	healthRepo := database.NewHealthRepository(db.Connection())

	// Reset all in-progress file health checks on start up
	if err := healthRepo.ResetFileAllChecking(); err != nil {
		slog.Error("failed to reset in progress file health", "err", err)
	}

	// Create metadata-based remote file handler
	metadataRemoteFile := nzbfilesystem.NewMetadataRemoteFile(
		metadataService,
		healthRepo,
		poolManager,
		configGetter,
	)

	// Create filesystem backed by metadata
	fs := nzbfilesystem.NewNzbFilesystem(metadataRemoteFile)

	// Store concrete type for context-aware operations
	nzbFs, ok := fs.(*nzbfilesystem.NzbFilesystem)
	if !ok {
		return nil, fmt.Errorf("failed to convert filesystem to NzbFilesystem type")
	}

	ctx := context.Background()

	if err := service.Start(ctx); err != nil {
		return nil, fmt.Errorf("failed to start NZB service: %w", err)
	}

	return &NzbSystem{
		database:            db,
		metadataReader:      metadataReader,
		service:             service,
		fs:                  fs,
		nzbFs:               nzbFs,
		poolManager:         poolManager,
		maxDownloadWorkers:  maxDownloadWorkers,
		maxProcessorWorkers: maxProcessorWorkers,
	}, nil
}

// GetQueueStats returns current queue statistics
func (ns *NzbSystem) GetQueueStats(ctx context.Context) (*database.QueueStats, error) {
	return ns.service.GetQueueStats(ctx)
}

// GetServiceStats returns service statistics including queue stats
func (ns *NzbSystem) GetServiceStats(ctx context.Context) (*importer.ServiceStats, error) {
	return ns.service.GetStats(ctx)
}

// FileSystem returns the virtual filesystem interface
func (ns *NzbSystem) FileSystem() afero.Fs {
	return ns.fs
}

// MetadataReader returns the metadata reader instance (for serving files)
func (ns *NzbSystem) MetadataReader() *metadata.MetadataReader {
	return ns.metadataReader
}

// Database returns the database instance (for processing queue)
func (ns *NzbSystem) Database() *database.DB {
	return ns.database
}

// ImporterService returns the importer service instance
func (ns *NzbSystem) ImporterService() *importer.Service {
	return ns.service
}

// StartService starts the NZB service (including background scanning and processing)
func (ns *NzbSystem) StartService(ctx context.Context) error {
	return ns.service.Start(ctx)
}

// StopService stops the NZB service
func (ns *NzbSystem) StopService(ctx context.Context) error {
	return ns.service.Stop(ctx)
}

// Close closes the NZB system and releases resources
func (ns *NzbSystem) Close() error {
	if err := ns.service.Close(); err != nil {
		return err
	}

	// Close database (metadata doesn't need closing)
	if err := ns.database.Close(); err != nil {
		return err
	}

	return nil
}

// GetStats returns statistics about the NZB system using metadata
func (ns *NzbSystem) GetStats() (*Stats, error) {
	// TODO: Implement metadata queries to get statistics
	// For now return empty stats - this would use metadata reader for actual counts
	return &Stats{
		TotalNzbFiles:     0,
		TotalVirtualFiles: 0,
		TotalSize:         0,
	}, nil
}

// Stats holds statistics about the NZB system
type Stats struct {
	TotalNzbFiles     int
	TotalVirtualFiles int
	TotalSize         int64
}

// UpdateImportWorkers - removed: processor worker changes require server restart
// The maxProcessorWorkers field is maintained for reference but changes only take effect on restart

// GetDownloadWorkers returns the current download worker count
func (ns *NzbSystem) GetDownloadWorkers() int {
	ns.configMutex.RLock()
	defer ns.configMutex.RUnlock()
	return ns.maxDownloadWorkers
}

// GetImportWorkers returns the current import processor worker count
func (ns *NzbSystem) GetImportWorkers() int {
	ns.configMutex.RLock()
	defer ns.configMutex.RUnlock()
	return ns.maxProcessorWorkers
}

var queuePathRegex = regexp.MustCompile(`^queue/(\d+)/`)

// Stream implements streaming.Provider for both queue paths and storage paths
func (ns *NzbSystem) Stream(ctx context.Context, req streaming.Request) (*streaming.Response, error) {
	// Check if this is a queue path
	matches := queuePathRegex.FindStringSubmatch(req.Path)
	if len(matches) >= 2 {
		// Handle queue path
		queueID, err := strconv.ParseInt(matches[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid queue ID: %w", err)
		}
		return ns.streamQueuePath(ctx, req, queueID)
	}

	// Not a queue path - try to serve from NzbFilesystem (for completed items)
	return ns.streamStoragePath(ctx, req)
}

// streamQueuePath handles streaming for queue paths (items being processed)
func (ns *NzbSystem) streamQueuePath(ctx context.Context, req streaming.Request, queueID int64) (*streaming.Response, error) {

	// Get queue item from database
	queueItem, err := ns.database.Repository.GetQueueItem(queueID)
	if err != nil {
		slog.Error("failed to get queue item", "queue_id", queueID, "error", err)
		return nil, streaming.ErrNotFound
	}

	// Check if queue item is completed
	if queueItem.Status != database.QueueStatusCompleted {
		// Queue item not ready yet - return 503 so frontend retries
		return &streaming.Response{
			Status: http.StatusServiceUnavailable,
			Headers: http.Header{
				"Retry-After": []string{"4"},
			},
			Body: io.NopCloser(strings.NewReader("")),
		}, nil
	}

	// Get the actual file path
	if queueItem.StoragePath == nil || *queueItem.StoragePath == "" {
		slog.Error("queue item completed but no storage path", "queue_id", queueID)
		return nil, streaming.ErrNotFound
	}

	actualPath := strings.TrimPrefix(*queueItem.StoragePath, "/")

	// For HEAD requests, just return file info without body
	if req.Method == http.MethodHead {
		fileInfo, err := ns.fs.Stat(actualPath)
		if err != nil {
			slog.Error("file not found for completed queue item", "queue_id", queueID, "path", actualPath, "error", err)
			return nil, streaming.ErrNotFound
		}

		return &streaming.Response{
			Status:        http.StatusOK,
			ContentLength: fileInfo.Size(),
			Headers: http.Header{
				"Accept-Ranges":  []string{"bytes"},
				"Content-Type":   []string{"video/mp4"}, // Default, could be detected
				"Content-Length": []string{strconv.FormatInt(fileInfo.Size(), 10)},
			},
			Body: io.NopCloser(strings.NewReader("")),
		}, nil
	}

	// For GET requests, open and stream the file with request context
	// This ensures usenet readers are cancelled when the client disconnects
	file, err := ns.nzbFs.OpenWithContext(ctx, actualPath)
	if err != nil {
		slog.Error("failed to open file for completed queue item", "queue_id", queueID, "path", actualPath, "error", err)
		return nil, streaming.ErrNotFound
	}

	fileInfo, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	// Handle range requests
	contentLength := fileInfo.Size()
	status := http.StatusOK
	headers := http.Header{
		"Accept-Ranges": []string{"bytes"},
		"Content-Type":  []string{"video/mp4"},
	}

	if req.RangeHeader != "" {
		// Parse range header (simplified - only handles single range)
		rangeSpec := strings.TrimPrefix(req.RangeHeader, "bytes=")
		parts := strings.Split(rangeSpec, "-")
		if len(parts) == 2 {
			start, _ := strconv.ParseInt(parts[0], 10, 64)
			end := contentLength - 1
			if parts[1] != "" {
				end, _ = strconv.ParseInt(parts[1], 10, 64)
			}

			// Seek to start position
			if seeker, ok := file.(io.Seeker); ok {
				if _, err := seeker.Seek(start, io.SeekStart); err != nil {
					file.Close()
					return nil, fmt.Errorf("seek failed: %w", err)
				}

				rangeLength := end - start + 1
				status = http.StatusPartialContent
				headers.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, contentLength))
				headers.Set("Content-Length", strconv.FormatInt(rangeLength, 10))
				contentLength = rangeLength
			}
		}
	} else {
		headers.Set("Content-Length", strconv.FormatInt(contentLength, 10))
	}

	return &streaming.Response{
		Status:        status,
		ContentLength: contentLength,
		Headers:       headers,
		Body:          file,
	}, nil
}

// streamStoragePath handles streaming for storage paths (completed items)
func (ns *NzbSystem) streamStoragePath(ctx context.Context, req streaming.Request) (*streaming.Response, error) {
	// Clean the path - remove leading slash and webdav prefix
	actualPath := strings.TrimPrefix(req.Path, "/")
	actualPath = strings.TrimPrefix(actualPath, "webdav/")
	if actualPath == "" {
		return nil, streaming.ErrNotFound
	}

	// For HEAD requests, just return file info without body
	if req.Method == http.MethodHead {
		fileInfo, err := ns.fs.Stat(actualPath)
		if err != nil {
			slog.Debug("file not found for storage path", "path", actualPath, "error", err)
			return nil, streaming.ErrNotFound
		}

		return &streaming.Response{
			Status:        http.StatusOK,
			ContentLength: fileInfo.Size(),
			Headers: http.Header{
				"Accept-Ranges":  []string{"bytes"},
				"Content-Type":   []string{"video/mp4"}, // Default, could be detected
				"Content-Length": []string{strconv.FormatInt(fileInfo.Size(), 10)},
			},
			Body: io.NopCloser(strings.NewReader("")),
		}, nil
	}

	// For GET requests, open and stream the file with request context
	// This ensures usenet readers are cancelled when the client disconnects
	file, err := ns.nzbFs.OpenWithContext(ctx, actualPath)
	if err != nil {
		slog.Debug("failed to open file for storage path", "path", actualPath, "error", err)
		return nil, streaming.ErrNotFound
	}

	fileInfo, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	// Handle range requests
	contentLength := fileInfo.Size()
	status := http.StatusOK
	headers := http.Header{
		"Accept-Ranges": []string{"bytes"},
		"Content-Type":  []string{"video/mp4"},
	}

	if req.RangeHeader != "" {
		// Parse range header (simplified - only handles single range)
		rangeSpec := strings.TrimPrefix(req.RangeHeader, "bytes=")
		parts := strings.Split(rangeSpec, "-")
		if len(parts) == 2 {
			start, _ := strconv.ParseInt(parts[0], 10, 64)
			end := contentLength - 1
			if parts[1] != "" {
				end, _ = strconv.ParseInt(parts[1], 10, 64)
			}

			// Seek to start position
			if seeker, ok := file.(io.Seeker); ok {
				if _, err := seeker.Seek(start, io.SeekStart); err != nil {
					file.Close()
					return nil, fmt.Errorf("seek failed: %w", err)
				}

				rangeLength := end - start + 1
				status = http.StatusPartialContent
				headers.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, contentLength))
				headers.Set("Content-Length", strconv.FormatInt(rangeLength, 10))
				contentLength = rangeLength
			}
		}
	} else {
		headers.Set("Content-Length", strconv.FormatInt(contentLength, 10))
	}

	return &streaming.Response{
		Status:        status,
		ContentLength: contentLength,
		Headers:       headers,
		Body:          file,
	}, nil
}
