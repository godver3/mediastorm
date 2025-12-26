package importer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	metapb "novastream/internal/nzb/metadata/proto"
	"novastream/internal/pool"

	"github.com/javi11/nntppool"
)

// SevenZipDiscoveryCallback is called when a file is discovered during progressive 7z analysis.
// Return true to continue analysis, false to stop early.
type SevenZipDiscoveryCallback func(file sevenZipContent) bool

// SevenZipProcessor interface for analyzing 7z content from NZB data
type SevenZipProcessor interface {
	// Analyze7zContentFromNzb analyzes a 7z archive directly from NZB data
	// without downloading. Returns an array of sevenZipContent with file metadata and segments.
	// Only supports uncompressed (store mode) 7z archives.
	Analyze7zContentFromNzb(ctx context.Context, szFiles []ParsedFile) ([]sevenZipContent, error)
	// Analyze7zContentFromNzbProgressive analyzes a 7z archive progressively, calling
	// the callback for each file discovered. This allows for early playback of the first
	// video file while analysis continues in the background.
	Analyze7zContentFromNzbProgressive(ctx context.Context, szFiles []ParsedFile, callback SevenZipDiscoveryCallback) ([]sevenZipContent, error)
	// CreateFileMetadataFrom7zContent creates FileMetadata from sevenZipContent for the metadata
	// system. This is used to convert sevenZipContent into the protobuf format used by the metadata system.
	CreateFileMetadataFrom7zContent(content sevenZipContent, sourceNzbPath string) *metapb.FileMetadata
}

// sevenZipContent represents a file within a 7z archive for processing
type sevenZipContent struct {
	InternalPath string                `json:"internal_path"`
	Filename     string                `json:"filename"`
	Size         int64                 `json:"size"`
	Segments     []*metapb.SegmentData `json:"segments"`               // Segment data for this file
	IsDirectory  bool                  `json:"is_directory,omitempty"` // Indicates if this is a directory
}

// sevenZipProcessor handles 7z archive analysis and content extraction
type sevenZipProcessor struct {
	log            *slog.Logger
	poolManager    pool.Manager
	maxWorkers     int
	maxCacheSizeMB int
	// Memory preloading configuration
	enableMemoryPreload bool
	maxMemoryGB         int
}

// NewSevenZipProcessor creates a new 7z processor
func NewSevenZipProcessor(poolManager pool.Manager, maxWorkers int, maxCacheSizeMB int) SevenZipProcessor {
	return &sevenZipProcessor{
		log:                 slog.Default().With("component", "7z-processor"),
		poolManager:         poolManager,
		maxWorkers:          maxWorkers,
		maxCacheSizeMB:      maxCacheSizeMB,
		enableMemoryPreload: true, // Enable by default
		maxMemoryGB:         8,    // Default 8GB limit
	}
}

// NewSevenZipProcessorWithConfig creates a new 7z processor with memory preloading configuration
func NewSevenZipProcessorWithConfig(poolManager pool.Manager, maxWorkers int, maxCacheSizeMB int, enableMemoryPreload bool, maxMemoryGB int) SevenZipProcessor {
	return &sevenZipProcessor{
		log:                 slog.Default().With("component", "7z-processor"),
		poolManager:         poolManager,
		maxWorkers:          maxWorkers,
		maxCacheSizeMB:      maxCacheSizeMB,
		enableMemoryPreload: enableMemoryPreload,
		maxMemoryGB:         maxMemoryGB,
	}
}

// CreateFileMetadataFrom7zContent creates FileMetadata from sevenZipContent for the metadata system
func (sp *sevenZipProcessor) CreateFileMetadataFrom7zContent(
	content sevenZipContent,
	sourceNzbPath string,
) *metapb.FileMetadata {
	now := time.Now().Unix()

	return &metapb.FileMetadata{
		FileSize:      content.Size,
		SourceNzbPath: sourceNzbPath,
		Status:        metapb.FileStatus_FILE_STATUS_HEALTHY,
		CreatedAt:     now,
		ModifiedAt:    now,
		SegmentData:   content.Segments,
	}
}

// Analyze7zContentFromNzb analyzes a 7z archive directly from NZB data without downloading
// This implementation streams the 7z header from Usenet and parses it to extract file metadata
// Only supports uncompressed (store mode) 7z archives
func (sp *sevenZipProcessor) Analyze7zContentFromNzb(ctx context.Context, szFiles []ParsedFile) ([]sevenZipContent, error) {
	if sp.poolManager == nil {
		return nil, NewNonRetryableError("no pool manager available", nil)
	}

	// Rename and sort 7z files
	sortFiles := rename7zFilesAndSort(szFiles)
	if len(sortFiles) == 0 {
		return nil, NewNonRetryableError("no 7z files to process", nil)
	}

	cp, err := sp.poolManager.GetPool()
	if err != nil {
		return nil, NewNonRetryableError("no connection pool available", err)
	}

	// Extract filenames for first part detection
	fileNames := make([]string, len(sortFiles))
	for i, file := range sortFiles {
		fileNames[i] = file.Filename
	}

	// Find the first 7z part
	main7zFile, err := getFirst7zPart(fileNames)
	if err != nil {
		return nil, err
	}

	sp.log.Info("Starting 7z analysis",
		"main_file", main7zFile,
		"total_parts", len(sortFiles),
		"sz_files", len(szFiles),
		"memory_preload_enabled", sp.enableMemoryPreload)

	// Calculate total size
	var totalSize int64
	for _, f := range sortFiles {
		totalSize += f.Size
	}

	// For 7z, we need to read the headers which are typically at the end of the archive
	// We'll use memory preloading for small archives and streaming for large ones
	if sp.enableMemoryPreload && sp.shouldUseMemoryPreload(sortFiles) {
		contents, err := sp.analyze7zWithMemoryPreload(ctx, cp, sortFiles, main7zFile, totalSize)
		if err == nil {
			return contents, nil
		}

		// If memory preload fails, log and fall back to streaming
		sp.log.Warn("Memory preload approach failed, falling back to streaming",
			"error", err)
	}

	// Fall back to streaming approach (but this requires reading more data)
	return sp.analyze7zWithStreaming(ctx, cp, sortFiles, main7zFile, totalSize)
}

// Analyze7zContentFromNzbProgressive analyzes a 7z archive progressively with callbacks
func (sp *sevenZipProcessor) Analyze7zContentFromNzbProgressive(ctx context.Context, szFiles []ParsedFile, callback SevenZipDiscoveryCallback) ([]sevenZipContent, error) {
	// For 7z, we parse all headers at once (they're at the end of the archive)
	// Then call the callback for each discovered file
	contents, err := sp.Analyze7zContentFromNzb(ctx, szFiles)
	if err != nil {
		return nil, err
	}

	// Call callback for each file progressively
	result := make([]sevenZipContent, 0, len(contents))
	for _, content := range contents {
		result = append(result, content)

		if callback != nil {
			shouldContinue := callback(content)
			if !shouldContinue {
				sp.log.Info("Progressive 7z analysis stopped early by callback",
					"files_discovered", len(result),
					"total_files", len(contents))
				return result, nil
			}
		}
	}

	return result, nil
}

// shouldUseMemoryPreload determines if memory preloading should be used based on archive size
func (sp *sevenZipProcessor) shouldUseMemoryPreload(szFiles []ParsedFile) bool {
	// Calculate total size of all 7z parts
	var totalSize int64
	for _, file := range szFiles {
		totalSize += file.Size
	}

	// Convert to GB
	totalSizeGB := totalSize / (1024 * 1024 * 1024)

	// Use memory preload if total size is within our memory limit
	shouldUse := totalSizeGB <= int64(sp.maxMemoryGB)

	sp.log.Debug("Memory preload decision",
		"total_size_gb", totalSizeGB,
		"max_memory_gb", sp.maxMemoryGB,
		"should_use_memory_preload", shouldUse)

	return shouldUse
}

// analyze7zWithMemoryPreload analyzes 7z archive by downloading to memory first
func (sp *sevenZipProcessor) analyze7zWithMemoryPreload(ctx context.Context, cp nntppool.UsenetConnectionPool, sortFiles []ParsedFile, main7zFile string, totalSize int64) ([]sevenZipContent, error) {
	sp.log.Info("Using memory preloading approach for 7z analysis")

	// Phase 1: Download all 7z parts to memory
	downloader := NewParallelRarDownloader(cp, sp.maxWorkers, sp.maxCacheSizeMB)
	memoryFiles, err := downloader.DownloadRarPartsToMemory(ctx, sortFiles)
	if err != nil {
		return nil, fmt.Errorf("failed to download 7z parts to memory: %w", err)
	}

	// Phase 2: Concatenate all parts into a single reader
	reader, size := sp.createMultiPartReader(memoryFiles, sortFiles)

	// Phase 3: Parse 7z headers
	analysisStart := time.Now()
	archiveInfo, err := parse7zHeaders(reader, size)
	if err != nil {
		return nil, err
	}

	analysisDuration := time.Since(analysisStart)

	sp.log.Info("Successfully analyzed 7z archive from memory",
		"main_file", main7zFile,
		"files_found", len(archiveInfo.Files),
		"analysis_duration", analysisDuration,
		"is_uncompressed", archiveInfo.IsUncompressed)

	// Phase 4: Convert to sevenZipContent with segment mapping
	return sp.convertFilesToContent(archiveInfo.Files, sortFiles)
}

// analyze7zWithStreaming analyzes 7z archive by streaming from usenet
func (sp *sevenZipProcessor) analyze7zWithStreaming(ctx context.Context, cp nntppool.UsenetConnectionPool, sortFiles []ParsedFile, main7zFile string, totalSize int64) ([]sevenZipContent, error) {
	sp.log.Info("Using streaming approach for 7z analysis")

	// Create Usenet filesystem for 7z access
	ufs := NewUsenetFileSystem(ctx, cp, sortFiles, sp.maxWorkers, sp.maxCacheSizeMB)

	// For 7z, we need to create a virtual multi-part file reader
	// The 7z signature header is at the start, but the file table is at the end
	reader, size, err := sp.createUsenetMultiPartReader(ufs, sortFiles, main7zFile)
	if err != nil {
		return nil, fmt.Errorf("failed to create multi-part reader: %w", err)
	}

	// Parse 7z headers
	analysisStart := time.Now()
	archiveInfo, err := parse7zHeaders(reader, size)
	if err != nil {
		return nil, err
	}

	analysisDuration := time.Since(analysisStart)

	sp.log.Info("Successfully analyzed 7z archive via streaming",
		"main_file", main7zFile,
		"files_found", len(archiveInfo.Files),
		"analysis_duration", analysisDuration,
		"is_uncompressed", archiveInfo.IsUncompressed)

	// Convert to sevenZipContent with segment mapping
	return sp.convertFilesToContent(archiveInfo.Files, sortFiles)
}

// createMultiPartReader creates a reader from in-memory files
func (sp *sevenZipProcessor) createMultiPartReader(memoryFiles map[string][]byte, sortFiles []ParsedFile) (io.ReaderAt, int64) {
	// Create a concatenated byte slice of all parts in order
	var totalSize int64
	for _, f := range sortFiles {
		totalSize += f.Size
	}

	combined := make([]byte, 0, totalSize)
	for _, f := range sortFiles {
		if data, ok := memoryFiles[f.Filename]; ok {
			combined = append(combined, data...)
		}
	}

	return bytes.NewReader(combined), int64(len(combined))
}

// createUsenetMultiPartReader creates a reader that can read across multiple 7z parts from usenet
func (sp *sevenZipProcessor) createUsenetMultiPartReader(ufs *UsenetFileSystem, sortFiles []ParsedFile, main7zFile string) (io.ReaderAt, int64, error) {
	// For streaming, we create a virtual reader that maps reads across all parts
	// This is similar to how the library handles .7z.001 multipart files

	// Calculate total size and part offsets
	var totalSize int64
	partOffsets := make([]int64, len(sortFiles))
	for i, f := range sortFiles {
		partOffsets[i] = totalSize
		totalSize += f.Size
	}

	reader := &multiPart7zReader{
		ufs:         ufs,
		sortFiles:   sortFiles,
		partOffsets: partOffsets,
		totalSize:   totalSize,
	}

	return reader, totalSize, nil
}

// multiPart7zReader implements io.ReaderAt for multi-part 7z archives
type multiPart7zReader struct {
	ufs         *UsenetFileSystem
	sortFiles   []ParsedFile
	partOffsets []int64 // Starting offset of each part in the combined file
	totalSize   int64
}

// ReadAt implements io.ReaderAt for multi-part 7z files
func (r *multiPart7zReader) ReadAt(p []byte, off int64) (n int, err error) {
	if off >= r.totalSize {
		return 0, io.EOF
	}

	// Find which part(s) this read spans
	remaining := len(p)
	totalRead := 0
	currentOff := off

	for remaining > 0 && currentOff < r.totalSize {
		// Find the part that contains currentOff
		partIdx := r.findPartForOffset(currentOff)
		if partIdx < 0 || partIdx >= len(r.sortFiles) {
			break
		}

		part := r.sortFiles[partIdx]
		partStart := r.partOffsets[partIdx]
		partEnd := partStart + part.Size

		// Calculate how much we can read from this part
		offsetInPart := currentOff - partStart
		maxFromPart := partEnd - currentOff
		toRead := int64(remaining)
		if toRead > maxFromPart {
			toRead = maxFromPart
		}

		// Open the file from the filesystem
		f, err := r.ufs.Open(part.Filename)
		if err != nil {
			return totalRead, fmt.Errorf("failed to open part %s: %w", part.Filename, err)
		}

		// Seek to the offset within the part
		if seeker, ok := f.(io.Seeker); ok {
			_, err = seeker.Seek(offsetInPart, io.SeekStart)
			if err != nil {
				f.Close()
				return totalRead, fmt.Errorf("failed to seek in part %s: %w", part.Filename, err)
			}
		}

		// Read from the part
		nr, err := io.ReadFull(f, p[totalRead:totalRead+int(toRead)])
		f.Close()

		totalRead += nr
		currentOff += int64(nr)
		remaining -= nr

		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return totalRead, err
		}
	}

	if totalRead == 0 && remaining > 0 {
		return 0, io.EOF
	}

	return totalRead, nil
}

// findPartForOffset returns the index of the part containing the given offset
func (r *multiPart7zReader) findPartForOffset(off int64) int {
	for i := len(r.partOffsets) - 1; i >= 0; i-- {
		if off >= r.partOffsets[i] {
			return i
		}
	}
	return 0
}

// szPartInfo holds information about a 7z archive part for segment mapping
type szPartInfo struct {
	file     *ParsedFile
	startOff int64 // Start offset in combined archive
	endOff   int64 // End offset in combined archive
}

// convertFilesToContent converts 7z file entries to sevenZipContent with segment mapping
func (sp *sevenZipProcessor) convertFilesToContent(files []sevenZipFileEntry, sortFiles []ParsedFile) ([]sevenZipContent, error) {
	contents := make([]sevenZipContent, 0, len(files))

	// Build segment data for the combined archive
	// All parts are concatenated, so we need to track absolute offsets
	parts := make([]szPartInfo, len(sortFiles))
	var currentOff int64
	for i := range sortFiles {
		parts[i] = szPartInfo{
			file:     &sortFiles[i],
			startOff: currentOff,
			endOff:   currentOff + sortFiles[i].Size,
		}
		currentOff += sortFiles[i].Size
	}

	for _, entry := range files {
		if entry.IsDirectory {
			continue
		}

		// Skip non-video/audio files for efficiency
		ext := strings.ToLower(filepath.Ext(entry.Name))
		if !isMediaFile(ext) {
			sp.log.Debug("Skipping non-media file in 7z",
				"file", entry.Name,
				"ext", ext)
			continue
		}

		// Map the file's byte range to segments
		segments, err := sp.mapFileToSegments(entry.PackedOffset, entry.UncompressedSize, parts)
		if err != nil {
			sp.log.Warn("Failed to map 7z file to segments",
				"file", entry.Name,
				"offset", entry.PackedOffset,
				"size", entry.UncompressedSize,
				"error", err)
			continue
		}

		content := sevenZipContent{
			InternalPath: entry.Name,
			Filename:     filepath.Base(entry.Name),
			Size:         entry.UncompressedSize,
			Segments:     segments,
			IsDirectory:  false,
		}
		contents = append(contents, content)
	}

	return contents, nil
}

// isMediaFile checks if the extension represents a media file
func isMediaFile(ext string) bool {
	mediaExts := map[string]bool{
		".mkv": true, ".mp4": true, ".avi": true, ".mov": true,
		".wmv": true, ".flv": true, ".webm": true, ".m4v": true,
		".mpg": true, ".mpeg": true, ".ts": true, ".m2ts": true,
		".mp3": true, ".flac": true, ".aac": true, ".ogg": true,
		".wav": true, ".wma": true, ".m4a": true,
		".srt": true, ".ass": true, ".ssa": true, ".sub": true, ".idx": true,
	}
	return mediaExts[ext]
}

// mapFileToSegments maps a file's byte range within the archive to NZB segments
func (sp *sevenZipProcessor) mapFileToSegments(fileOffset, fileSize int64, parts []szPartInfo) ([]*metapb.SegmentData, error) {
	if fileSize <= 0 {
		return nil, nil
	}

	fileEnd := fileOffset + fileSize - 1
	var segments []*metapb.SegmentData

	for _, part := range parts {
		// Skip parts that don't overlap with the file
		if part.endOff <= fileOffset || part.startOff > fileEnd {
			continue
		}

		// Calculate the overlap region
		overlapStart := fileOffset
		if overlapStart < part.startOff {
			overlapStart = part.startOff
		}
		overlapEnd := fileEnd
		if overlapEnd >= part.endOff {
			overlapEnd = part.endOff - 1
		}

		// Convert to offsets within the part
		partDataOffset := overlapStart - part.startOff
		partDataSize := overlapEnd - overlapStart + 1

		// Slice the segments from this part
		sliced, covered, err := slicePartSegments(part.file.Segments, partDataOffset, partDataSize)
		if err != nil {
			sp.log.Warn("Failed slicing part segments",
				"error", err,
				"part", part.file.Filename,
				"offset", partDataOffset,
				"size", partDataSize)
			continue
		}

		if covered != partDataSize {
			sp.log.Debug("Part coverage mismatch",
				"part", part.file.Filename,
				"expected", partDataSize,
				"covered", covered)
		}

		segments = append(segments, sliced...)
	}

	return segments, nil
}
