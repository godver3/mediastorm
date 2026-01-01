package importer

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	metapb "novastream/internal/nzb/metadata/proto"
	"novastream/internal/pool"

	"github.com/javi11/nntppool"
	"github.com/javi11/rarlist"
)

// FileDiscoveryCallback is called when a file is discovered during progressive RAR analysis.
// Return true to continue analysis, false to stop early.
type FileDiscoveryCallback func(file rarContent) bool

// RarProcessor interface for analyzing RAR content from NZB data
type RarProcessor interface {
	// AnalyzeRarContentFromNzb analyzes a RAR archive directly from NZB data
	// without downloading. Returns an array of RarContent with file metadata and segments.
	AnalyzeRarContentFromNzb(ctx context.Context, rarFiles []ParsedFile) ([]rarContent, error)
	// AnalyzeRarContentFromNzbProgressive analyzes a RAR archive progressively, calling
	// the callback for each file discovered. This allows for early playback of the first
	// video file while analysis continues in the background.
	AnalyzeRarContentFromNzbProgressive(ctx context.Context, rarFiles []ParsedFile, callback FileDiscoveryCallback) ([]rarContent, error)
	// CreateFileMetadataFromRarContent creates FileMetadata from RarContent for the metadata
	// system. This is used to convert RarContent into the protobuf format used by the metadata system.
	CreateFileMetadataFromRarContent(rarContent rarContent, sourceNzbPath string) *metapb.FileMetadata
}

// RarContent represents a file within a RAR archive for processing
type rarContent struct {
	InternalPath string                `json:"internal_path"`
	Filename     string                `json:"filename"`
	Size         int64                 `json:"size"`
	Segments     []*metapb.SegmentData `json:"segments"`               // Segment data for this file
	IsDirectory  bool                  `json:"is_directory,omitempty"` // Indicates if this is a directory
}

// rarProcessor handles RAR archive analysis and content extraction
type rarProcessor struct {
	log            *slog.Logger
	poolManager    pool.Manager
	maxWorkers     int
	maxCacheSizeMB int
	// Memory preloading configuration
	enableMemoryPreload bool
	maxMemoryGB         int
}

// NewRarProcessor creates a new RAR processor
func NewRarProcessor(poolManager pool.Manager, maxWorkers int, maxCacheSizeMB int) RarProcessor {
	return &rarProcessor{
		log:                 slog.Default().With("component", "rar-processor"),
		poolManager:         poolManager,
		maxWorkers:          maxWorkers,
		maxCacheSizeMB:      maxCacheSizeMB,
		enableMemoryPreload: true, // Enable by default
		maxMemoryGB:         8,    // Default 8GB limit
	}
}

// NewRarProcessorWithConfig creates a new RAR processor with memory preloading configuration
func NewRarProcessorWithConfig(poolManager pool.Manager, maxWorkers int, maxCacheSizeMB int, enableMemoryPreload bool, maxMemoryGB int) RarProcessor {
	return &rarProcessor{
		log:                 slog.Default().With("component", "rar-processor"),
		poolManager:         poolManager,
		maxWorkers:          maxWorkers,
		maxCacheSizeMB:      maxCacheSizeMB,
		enableMemoryPreload: enableMemoryPreload,
		maxMemoryGB:         maxMemoryGB,
	}
}

// CreateFileMetadataFromRarContent creates FileMetadata from RarContent for the metadata system
func (rh *rarProcessor) CreateFileMetadataFromRarContent(
	rarContent rarContent,
	sourceNzbPath string,
) *metapb.FileMetadata {
	now := time.Now().Unix()

	return &metapb.FileMetadata{
		FileSize:      rarContent.Size,
		SourceNzbPath: sourceNzbPath,
		Status:        metapb.FileStatus_FILE_STATUS_HEALTHY,
		CreatedAt:     now,
		ModifiedAt:    now,
		SegmentData:   rarContent.Segments,
	}
}

// AnalyzeRarContentFromNzb analyzes a RAR archive directly from NZB data without downloading
// This implementation uses rarlist with UsenetFileSystem to analyze RAR structure and stream data from Usenet
// Returns an array of files to be added to the metadata with all the info and segments for each file
func (rh *rarProcessor) AnalyzeRarContentFromNzb(ctx context.Context, rarFiles []ParsedFile) ([]rarContent, error) {
	if rh.poolManager == nil {
		return nil, NewNonRetryableError("no pool manager available", nil)
	}

	// Rename RAR files to match the first file's base name that will allow parse rar that have different files name
	sortFiles := renameRarFilesAndSort(rarFiles)

	cp, err := rh.poolManager.GetPool()
	if err != nil {
		return nil, NewNonRetryableError("no connection pool available", err)
	}

	// Extract filenames for first part detection
	fileNames := make([]string, len(sortFiles))
	for i, file := range sortFiles {
		fileNames[i] = file.Filename
	}

	// Find the first RAR part using intelligent detection
	mainRarFile, err := rh.getFirstRarPart(fileNames)
	if err != nil {
		return nil, err
	}

	rh.log.Info("Starting RAR analysis",
		"main_file", mainRarFile,
		"total_parts", len(sortFiles),
		"rar_files", len(rarFiles),
		"memory_preload_enabled", rh.enableMemoryPreload)

	// Try memory preloading approach first if enabled and feasible
	if rh.enableMemoryPreload && rh.shouldUseMemoryPreload(sortFiles) {
		rarContents, err := rh.analyzeRarWithMemoryPreload(ctx, cp, sortFiles, mainRarFile)
		if err == nil {
			return rarContents, nil
		}

		// If memory preload fails, log the error and fall back to streaming
		rh.log.Warn("Memory preload approach failed, falling back to streaming",
			"error", err)
	}

	// Fall back to original streaming approach
	return rh.analyzeRarWithStreaming(ctx, cp, sortFiles, mainRarFile)
}

// AnalyzeRarContentFromNzbProgressive analyzes a RAR archive progressively with callbacks
// This allows discovering and returning the first video file early while continuing analysis in background
func (rh *rarProcessor) AnalyzeRarContentFromNzbProgressive(ctx context.Context, rarFiles []ParsedFile, callback FileDiscoveryCallback) ([]rarContent, error) {
	if rh.poolManager == nil {
		return nil, NewNonRetryableError("no pool manager available", nil)
	}

	// Rename RAR files to match the first file's base name
	sortFiles := renameRarFilesAndSort(rarFiles)

	cp, err := rh.poolManager.GetPool()
	if err != nil {
		return nil, NewNonRetryableError("no connection pool available", err)
	}

	// Extract filenames for first part detection
	fileNames := make([]string, len(sortFiles))
	for i, file := range sortFiles {
		fileNames[i] = file.Filename
	}

	// Find the first RAR part using intelligent detection
	mainRarFile, err := rh.getFirstRarPart(fileNames)
	if err != nil {
		return nil, err
	}

	rh.log.Info("Starting progressive RAR analysis",
		"main_file", mainRarFile,
		"total_parts", len(sortFiles),
		"rar_files", len(rarFiles))

	// Always use streaming approach for progressive analysis
	// Memory preload would defeat the purpose of early discovery
	return rh.analyzeRarWithStreamingProgressive(ctx, cp, sortFiles, mainRarFile, callback)
}

// analyzeRarWithStreamingProgressive analyzes RAR with progressive file discovery callbacks
func (rh *rarProcessor) analyzeRarWithStreamingProgressive(ctx context.Context, cp nntppool.UsenetConnectionPool, sortFiles []ParsedFile, mainRarFile string, callback FileDiscoveryCallback) ([]rarContent, error) {
	rh.log.Info("Using streaming approach for progressive RAR analysis")

	// Check for context cancellation before starting
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Create Usenet filesystem for RAR access
	ufs := NewUsenetFileSystem(ctx, cp, sortFiles, rh.maxWorkers, rh.maxCacheSizeMB)

	analysisStart := time.Now()

	// Check for context cancellation before volume discovery
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Use lower-level rarlist API to get progressive results
	// First, discover all RAR volumes
	volPaths, err := rarlist.DiscoverVolumesFS(ufs, mainRarFile)
	if err != nil {
		return nil, NewNonRetryableError("failed to discover RAR volumes", err)
	}

	rh.log.Debug("Discovered RAR volumes",
		"main_file", mainRarFile,
		"volumes", len(volPaths))

	// Index volumes to build file catalog
	volumes, err := rarlist.IndexVolumesParallel(ufs, volPaths, rh.maxWorkers)
	if err != nil {
		return nil, NewNonRetryableError("failed to index RAR volumes", err)
	}

	if len(volumes) == 0 {
		return nil, NewNonRetryableError("no RAR volumes indexed", nil)
	}

	// Aggregate files from indexed volumes
	aggregatedFiles := rarlist.AggregateFiles(volumes)

	if len(aggregatedFiles) == 0 {
		return nil, NewNonRetryableError("no valid files found in RAR archive. Compressed or encrypted RARs are not supported", nil)
	}

	analysisDuration := time.Since(analysisStart)

	rh.log.Info("Successfully analyzed RAR archive via progressive streaming",
		"main_file", mainRarFile,
		"files_found", len(aggregatedFiles),
		"analysis_duration", analysisDuration)

	// Convert files progressively, calling callback for each one
	rarContents := make([]rarContent, 0, len(aggregatedFiles))

	for i, af := range aggregatedFiles {
		// Convert this file to rarContent
		fileContents, err := rh.convertAggregatedFilesToRarContent([]rarlist.AggregatedFile{af}, sortFiles)
		if err != nil {
			rh.log.Warn("Failed to convert aggregated file",
				"file", af.Name,
				"index", i,
				"error", err)
			continue
		}

		if len(fileContents) == 0 {
			continue
		}

		rc := fileContents[0]
		rarContents = append(rarContents, rc)

		// Call the callback with this discovered file
		if callback != nil {
			shouldContinue := callback(rc)
			if !shouldContinue {
				rh.log.Info("Progressive analysis stopped early by callback",
					"files_discovered", len(rarContents),
					"total_files", len(aggregatedFiles))
				// Return what we have so far
				return rarContents, nil
			}
		}
	}

	return rarContents, nil
}

// shouldUseMemoryPreload determines if memory preloading should be used based on archive size
func (rh *rarProcessor) shouldUseMemoryPreload(rarFiles []ParsedFile) bool {
	// Calculate total size of all RAR parts
	var totalSize int64
	for _, file := range rarFiles {
		totalSize += file.Size
	}

	// Convert to GB
	totalSizeGB := totalSize / (1024 * 1024 * 1024)

	// Use memory preload if total size is within our memory limit
	shouldUse := totalSizeGB <= int64(rh.maxMemoryGB)

	rh.log.Debug("Memory preload decision",
		"total_size_gb", totalSizeGB,
		"max_memory_gb", rh.maxMemoryGB,
		"should_use_memory_preload", shouldUse)

	return shouldUse
}

// analyzeRarWithMemoryPreload analyzes RAR archive using memory preloading approach
func (rh *rarProcessor) analyzeRarWithMemoryPreload(ctx context.Context, cp nntppool.UsenetConnectionPool, sortFiles []ParsedFile, mainRarFile string) ([]rarContent, error) {
	rh.log.Info("Using memory preloading approach for RAR analysis")

	// Phase 1: Parallel download all RAR parts to memory
	downloader := NewParallelRarDownloader(cp, rh.maxWorkers, rh.maxCacheSizeMB)
	memoryFiles, err := downloader.DownloadRarPartsToMemory(ctx, sortFiles)
	if err != nil {
		return nil, fmt.Errorf("failed to download RAR parts to memory: %w", err)
	}

	// Phase 2: Create memory-based filesystem
	memoryFS := NewMemoryFileSystem(memoryFiles)

	// Phase 3: Fast sequential analysis on in-memory data
	analysisStart := time.Now()
	aggregatedFiles, err := rarlist.ListFilesFS(memoryFS, mainRarFile)
	if err != nil {
		return nil, NewNonRetryableError("failed to analyze RAR archive from memory", err)
	}

	analysisDuration := time.Since(analysisStart)

	if len(aggregatedFiles) == 0 {
		return nil, NewNonRetryableError("no valid files found in RAR archive. Compressed or encrypted RARs are not supported", nil)
	}

	rh.log.Info("Successfully analyzed RAR archive from memory",
		"main_file", mainRarFile,
		"files_found", len(aggregatedFiles),
		"analysis_duration", analysisDuration)

	// Convert rarlist results to RarContent
	rarContents, err := rh.convertAggregatedFilesToRarContent(aggregatedFiles, sortFiles)
	if err != nil {
		return nil, NewNonRetryableError("failed to convert rarlist results to RarContent", err)
	}

	return rarContents, nil
}

// analyzeRarWithStreaming analyzes RAR archive using the original streaming approach
func (rh *rarProcessor) analyzeRarWithStreaming(ctx context.Context, cp nntppool.UsenetConnectionPool, sortFiles []ParsedFile, mainRarFile string) ([]rarContent, error) {
	rh.log.Info("Using streaming approach for RAR analysis")

	// Create Usenet filesystem for RAR access - this enables rarlist to access
	// RAR part files directly from Usenet without downloading
	ufs := NewUsenetFileSystem(ctx, cp, sortFiles, rh.maxWorkers, rh.maxCacheSizeMB)

	analysisStart := time.Now()
	aggregatedFiles, err := rarlist.ListFilesFS(ufs, mainRarFile)
	if err != nil {
		return nil, NewNonRetryableError("failed to aggregate RAR files", err)
	}

	analysisDuration := time.Since(analysisStart)

	if len(aggregatedFiles) == 0 {
		return nil, NewNonRetryableError("no valid files found in RAR archive. Compressed or encrypted RARs are not supported", nil)
	}

	rh.log.Info("Successfully analyzed RAR archive via streaming",
		"main_file", mainRarFile,
		"files_found", len(aggregatedFiles),
		"analysis_duration", analysisDuration)

	// Convert rarlist results to RarContent
	rarContents, err := rh.convertAggregatedFilesToRarContent(aggregatedFiles, sortFiles)
	if err != nil {
		return nil, NewNonRetryableError("failed to convert rarlist results to RarContent", err)
	}

	return rarContents, nil
}

// getFirstRarPart finds and returns the filename of the first part of a RAR archive
// This method prioritizes .rar files over .part001.rar over .r00 files
func (rh *rarProcessor) getFirstRarPart(rarFileNames []string) (string, error) {
	if len(rarFileNames) == 0 {
		return "", NewNonRetryableError("no RAR files provided", nil)
	}

	// If only one file, return it
	if len(rarFileNames) == 1 {
		return rarFileNames[0], nil
	}

	// Group files by base name and find first parts
	type candidateFile struct {
		filename string
		baseName string
		partNum  int
		priority int // Lower number = higher priority
	}

	var candidates []candidateFile

	for _, filename := range rarFileNames {
		base, part := rh.parseRarFilename(filename)

		// Only consider files that are actually first parts (part 0)
		if part != 0 {
			continue
		}

		// Determine priority based on file extension pattern
		priority := rh.getRarFilePriority(filename)

		candidates = append(candidates, candidateFile{
			filename: filename,
			baseName: base,
			partNum:  part,
			priority: priority,
		})
	}

	if len(candidates) == 0 {
		return "", NewNonRetryableError("no valid first RAR part found in archive", nil)
	}

	// Sort by priority (lower number = higher priority), then by filename for consistency
	best := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.priority < best.priority ||
			(candidate.priority == best.priority && candidate.filename < best.filename) {
			best = candidate
		}
	}

	rh.log.Debug("Selected first RAR part",
		"filename", best.filename,
		"base_name", best.baseName,
		"priority", best.priority,
		"total_candidates", len(candidates))

	return best.filename, nil
}

// getRarFilePriority returns the priority for different RAR file types
// Lower number = higher priority
func (rh *rarProcessor) getRarFilePriority(filename string) int {
	lowerName := strings.ToLower(filename)

	// Priority 1: .rar files (main archive)
	if strings.HasSuffix(lowerName, ".rar") && !strings.Contains(lowerName, ".part") {
		return 1
	}

	// Priority 2: .part001.rar, .part01.rar patterns
	if strings.Contains(lowerName, ".part") && strings.HasSuffix(lowerName, ".rar") {
		return 2
	}

	// Priority 3: .r00 patterns
	if strings.Contains(lowerName, ".r0") {
		return 3
	}

	// Priority 4: .001 numeric patterns
	if len(lowerName) > 4 && lowerName[len(lowerName)-4:len(lowerName)-3] == "." {
		return 4
	}

	// Priority 5: Everything else
	return 5
}

// parseRarFilename extracts base name and part number from RAR filename
// This is a simplified version of the logic from processor.go
func (rh *rarProcessor) parseRarFilename(filename string) (base string, part int) {
	lowerFilename := strings.ToLower(filename)

	// Pattern 1: filename.part###.rar (e.g., movie.part001.rar, movie.part01.rar)
	if matches := partPattern.FindStringSubmatch(filename); len(matches) > 2 {
		base = matches[1]
		if partNum := parseInt(matches[2]); partNum >= 0 {
			// Convert 1-based part numbers to 0-based (part001 becomes 0, part002 becomes 1)
			if partNum > 0 {
				part = partNum - 1
			}
			return base, part
		}
	}

	// Pattern 2: filename.rar (first part)
	if strings.HasSuffix(lowerFilename, ".rar") {
		base = strings.TrimSuffix(filename, filepath.Ext(filename))
		return base, 0 // First part
	}

	// Pattern 3: filename.r## or filename.r### (e.g., movie.r00, movie.r01)
	if matches := rPattern.FindStringSubmatch(filename); len(matches) > 2 {
		base = matches[1]
		if partNum := parseInt(matches[2]); partNum >= 0 {
			// .r00 is part 0, .r01 is part 1, etc.
			return base, partNum
		}
	}

	// Pattern 4: filename.### (numeric extensions like .001, .002)
	if matches := numericPattern.FindStringSubmatch(filename); len(matches) > 2 {
		base = matches[1]
		if partNum := parseInt(matches[2]); partNum >= 0 {
			// .001 becomes part 0, .002 becomes part 1, etc.
			if partNum > 0 {
				part = partNum - 1
			}
			return base, part
		}
	}

	// Unknown pattern - return filename as base with high part number (sorts last)
	return filename, 999999
}

// convertAggregatedFilesToRarContent converts rarlist.AggregatedFile results to RarContent
func (rh *rarProcessor) convertAggregatedFilesToRarContent(aggregatedFiles []rarlist.AggregatedFile, rarFiles []ParsedFile) ([]rarContent, error) {
	// Build quick lookup for rar part ParsedFile by both full path and base name
	fileIndex := make(map[string]*ParsedFile, len(rarFiles)*2)
	for i := range rarFiles {
		pf := &rarFiles[i]
		fileIndex[pf.Filename] = pf
		fileIndex[filepath.Base(pf.Filename)] = pf
	}

	out := make([]rarContent, 0, len(aggregatedFiles))

	for _, af := range aggregatedFiles {
		rc := rarContent{
			InternalPath: af.Name,
			Filename:     filepath.Base(af.Name),
			Size:         af.TotalPackedSize,
		}

		var fileSegments []*metapb.SegmentData
		var accumulated int64

		for partIdx, part := range af.Parts {
			if part.PackedSize <= 0 {
				continue
			}

			pf := fileIndex[part.Path]
			if pf == nil {
				pf = fileIndex[filepath.Base(part.Path)]
			}
			if pf == nil {
				rh.log.Warn("RAR part not found among parsed NZB files", "part_path", part.Path, "file", af.Name)
				continue
			}

			// Extract the slice of this part's bytes that belong to the aggregated file.
			sliced, covered, err := slicePartSegments(pf.Segments, part.DataOffset, part.PackedSize)
			if err != nil {
				rh.log.Warn("Failed slicing part segments", "error", err, "part_path", part.Path, "file", af.Name)
				continue
			}
			// Append maintaining order: parts order then segment order within part.
			fileSegments = append(fileSegments, sliced...)
			accumulated += covered

			if covered != part.PackedSize {
				rh.log.Warn("Part coverage mismatch", "file", af.Name, "part_index", partIdx, "expected", part.PackedSize, "covered", covered, "data_offset", part.DataOffset)
			}
		}

		// Validation: sum of trimmed segment lengths should match total packed size.
		var sum int64
		for _, s := range fileSegments {
			sum += (s.EndOffset - s.StartOffset + 1)
		}
		if sum != af.TotalPackedSize {
			rh.log.Warn("Aggregated file coverage mismatch", "file", af.Name, "expected", af.TotalPackedSize, "got", sum)
		}
		rc.Segments = fileSegments
		out = append(out, rc)
	}

	return out, nil
}

// slicePartSegments returns the slice of segment ranges (cloned and trimmed) covering
// [dataOffset, dataOffset+length-1] within a part file represented by ordered segments.
// Assumes each segment's Start/End offsets are relative to the segment itself starting at 0
// and that segments are contiguous in the original order. Returns covered bytes actually found.
func slicePartSegments(segments []*metapb.SegmentData, dataOffset int64, length int64) ([]*metapb.SegmentData, int64, error) {
	if length <= 0 {
		return nil, 0, nil
	}
	if dataOffset < 0 {
		return nil, 0, NewNonRetryableError("negative dataOffset", nil)
	}

	targetStart := dataOffset
	targetEnd := dataOffset + length - 1
	var covered int64
	out := []*metapb.SegmentData{}

	// cumulative absolute position inside the part file
	var absPos int64
	for _, seg := range segments {
		segSize := (seg.EndOffset - seg.StartOffset + 1)
		if segSize <= 0 {
			continue
		}
		segAbsStart := absPos + seg.StartOffset // usually absPos
		segAbsEnd := absPos + seg.EndOffset

		// If segment ends before target range starts, skip
		if segAbsEnd < targetStart {
			absPos += segSize
			continue
		}
		// If segment starts after target range ends, we can stop.
		if segAbsStart > targetEnd {
			break
		}

		overlapStart := segAbsStart
		if overlapStart < targetStart {
			overlapStart = targetStart
		}
		overlapEnd := segAbsEnd
		if overlapEnd > targetEnd {
			overlapEnd = targetEnd
		}
		if overlapEnd >= overlapStart {
			// Translate back to segment-relative offsets.
			relStart := seg.StartOffset + (overlapStart - segAbsStart)
			relEnd := seg.StartOffset + (overlapEnd - segAbsStart)
			if relStart < seg.StartOffset {
				relStart = seg.StartOffset
			}
			if relEnd > seg.EndOffset {
				relEnd = seg.EndOffset
			}
			out = append(out, &metapb.SegmentData{
				Id:          seg.Id,
				StartOffset: relStart,
				EndOffset:   relEnd,
				SegmentSize: seg.SegmentSize,
			})
			covered += (relEnd - relStart + 1)
			if overlapEnd == targetEnd { // done
				break
			}
		}
		absPos += segSize
	}

	return out, covered, nil
}

// extractBaseFilename extracts the base filename without the part suffix
// This works with the original patterns (including leading zeros) to properly extract the base
func extractBaseFilename(filename string) string {
	// Try each pattern and extract the base name (group 1)
	if matches := partPattern.FindStringSubmatch(filename); len(matches) > 1 {
		return matches[1]
	}
	if matches := rPattern.FindStringSubmatch(filename); len(matches) > 1 {
		return matches[1]
	}
	if matches := numericPattern.FindStringSubmatch(filename); len(matches) > 1 {
		return matches[1]
	}

	// If no pattern matches, return the filename without extension
	return strings.TrimSuffix(filename, filepath.Ext(filename))
}

// stripLeadingZeros removes leading zeros from a numeric string while preserving at least one digit
func stripLeadingZeros(s string) string {
	if s == "" {
		return "0"
	}

	// Find first non-zero digit
	i := 0
	for i < len(s) && s[i] == '0' {
		i++
	}

	// If all digits are zero, return "0"
	if i == len(s) {
		return "0"
	}

	// Return string starting from first non-zero digit
	return s[i:]
}

func renameRarFilesAndSort(rarFiles []ParsedFile) []ParsedFile {
	if len(rarFiles) == 0 {
		return nil
	}

	// Get the base name of the first RAR file (without extension)
	// We need to use the original suffix (with leading zeros) to properly extract the base name
	firstFileBase := extractBaseFilename(rarFiles[0].Filename)

	type rarFileWithPart struct {
		file ParsedFile
		part int
	}

	withParts := make([]rarFileWithPart, len(rarFiles))

	for i, rf := range rarFiles {
		partSuffix := getPartSuffix(rf.Filename)
		rf.Filename = firstFileBase + partSuffix

		withParts[i] = rarFileWithPart{
			file: rf,
			part: extractRarPartNumber(rf.Filename),
		}
	}

	sort.SliceStable(withParts, func(i, j int) bool {
		return withParts[i].part < withParts[j].part
	})

	renamed := make([]ParsedFile, len(withParts))
	for i := range withParts {
		renamed[i] = withParts[i].file
	}

	return renamed
}

func getPartSuffix(originalFileName string) string {
	if matches := partPatternNumber.FindStringSubmatch(originalFileName); len(matches) > 1 {
		return fmt.Sprintf(".part%s.rar", stripLeadingZeros(matches[1]))
	} else if matches := rPatternNumber.FindStringSubmatch(originalFileName); len(matches) > 1 {
		return fmt.Sprintf(".r%s", matches[1])
	} else if matches := numericPatternNumber.FindStringSubmatch(originalFileName); len(matches) > 1 {
		return fmt.Sprintf(".%s", matches[1])
	}

	return filepath.Ext(originalFileName)
}

// extractRarPartNumber extracts numeric part from RAR extension for sorting
func extractRarPartNumber(fileName string) int {
	partNumber := getPartNumber(fileName)
	if partNumber > 0 {
		return partNumber
	}

	return 999999 // Unknown format goes last
}
