package importer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/internal/nzb/metadata"
	metapb "novastream/internal/nzb/metadata/proto"
	"novastream/internal/pool"
)

// Processor handles the processing and storage of parsed NZB files using metadata storage
type Processor struct {
	parser            *Parser
	strmParser        *StrmParser
	metadataService   *metadata.MetadataService
	rarProcessor      RarProcessor
	sevenZipProcessor SevenZipProcessor
	poolManager       pool.Manager // Pool manager for dynamic pool access
	configGetter      config.ConfigGetter
	log               *slog.Logger
	rarMaxWorkers     int
	rarMaxCacheSizeMB int
	rarConfigMu       sync.RWMutex

	// Pre-compiled regex patterns for RAR file sorting
	rarPartPattern    *regexp.Regexp // pattern.part###.rar
	rarRPattern       *regexp.Regexp // pattern.r### or pattern.r##
	rarNumericPattern *regexp.Regexp // pattern.### (numeric extensions)
}

// NewProcessor creates a new NZB processor using metadata storage
func NewProcessor(metadataService *metadata.MetadataService, poolManager pool.Manager, configGetter config.ConfigGetter) *Processor {
	maxWorkers := 40
	maxCacheSizeMB := 128
	enableMemoryPreload := true
	maxMemoryGB := 8

	if configGetter != nil {
		if cfg := configGetter(); cfg != nil {
			if cfg.Import.RarMaxWorkers > 0 {
				maxWorkers = cfg.Import.RarMaxWorkers
			}
			if cfg.Import.RarMaxCacheSizeMB > 0 {
				maxCacheSizeMB = cfg.Import.RarMaxCacheSizeMB
			}
			enableMemoryPreload = cfg.Import.RarEnableMemoryPreload
			if cfg.Import.RarMaxMemoryGB > 0 {
				maxMemoryGB = cfg.Import.RarMaxMemoryGB
			}
		}
	}

	p := &Processor{
		parser:            NewParser(poolManager),
		strmParser:        NewStrmParser(),
		metadataService:   metadataService,
		poolManager:       poolManager,
		configGetter:      configGetter,
		rarMaxWorkers:     maxWorkers,
		rarMaxCacheSizeMB: maxCacheSizeMB,
		log:               slog.Default().With("component", "nzb-processor"),

		// Initialize pre-compiled regex patterns for RAR file sorting
		rarPartPattern:    regexp.MustCompile(`^(.+)\.part(\d+)\.rar$`), // filename.part001.rar
		rarRPattern:       regexp.MustCompile(`^(.+)\.r(\d+)$`),         // filename.r00, filename.r01
		rarNumericPattern: regexp.MustCompile(`^(.+)\.(\d+)$`),          // filename.001, filename.002
	}

	p.rarProcessor = NewRarProcessorWithConfig(poolManager, maxWorkers, maxCacheSizeMB, enableMemoryPreload, maxMemoryGB)
	p.sevenZipProcessor = NewSevenZipProcessorWithConfig(poolManager, maxWorkers, maxCacheSizeMB, enableMemoryPreload, maxMemoryGB)

	return p
}

func (proc *Processor) ensureRarProcessorConfig() {
	if proc.configGetter == nil {
		return
	}

	cfg := proc.configGetter()
	if cfg == nil {
		return
	}

	desiredWorkers := cfg.Import.RarMaxWorkers
	if desiredWorkers <= 0 {
		desiredWorkers = 20
	}

	desiredCache := cfg.Import.RarMaxCacheSizeMB
	if desiredCache <= 0 {
		desiredCache = 128
	}

	desiredMemoryPreload := cfg.Import.RarEnableMemoryPreload
	desiredMaxMemoryGB := cfg.Import.RarMaxMemoryGB
	if desiredMaxMemoryGB <= 0 {
		desiredMaxMemoryGB = 8
	}

	proc.rarConfigMu.RLock()
	currentWorkers := proc.rarMaxWorkers
	currentCache := proc.rarMaxCacheSizeMB
	proc.rarConfigMu.RUnlock()

	// Check if we need to update the RAR processor
	needsUpdate := desiredWorkers != currentWorkers || desiredCache != currentCache

	if needsUpdate {
		proc.rarConfigMu.Lock()
		defer proc.rarConfigMu.Unlock()

		// Re-check inside the lock in case another goroutine already updated.
		if desiredWorkers == proc.rarMaxWorkers && desiredCache == proc.rarMaxCacheSizeMB {
			return
		}

		proc.rarProcessor = NewRarProcessorWithConfig(proc.poolManager, desiredWorkers, desiredCache, desiredMemoryPreload, desiredMaxMemoryGB)
		proc.rarMaxWorkers = desiredWorkers
		proc.rarMaxCacheSizeMB = desiredCache
	}
}

// ProcessNzbFileWithRelativePath processes an NZB or STRM file maintaining the folder structure relative to relative path
func (proc *Processor) ProcessNzbFile(ctx context.Context, filePath, relativePath string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	proc.ensureRarProcessorConfig()

	// Open and parse the file
	file, err := os.Open(filePath)
	if err != nil {
		return "", NewNonRetryableError("failed to open file", err)
	}
	defer file.Close()

	var parsed *ParsedNzb

	// Determine file type and parse accordingly
	if strings.HasSuffix(strings.ToLower(filePath), ".strm") {
		parsed, err = proc.strmParser.ParseStrmFile(file, filePath)
		if err != nil {
			return "", NewNonRetryableError("failed to parse STRM file", err)
		}

		// Validate the parsed STRM
		if err := proc.strmParser.ValidateStrmFile(parsed); err != nil {
			return "", NewNonRetryableError("STRM validation failed", err)
		}
	} else {
		parsed, err = proc.parser.ParseFile(file, filePath)
		if err != nil {
			return "", NewNonRetryableError("failed to parse NZB file", err)
		}

		// Validate the parsed NZB
		if err := proc.parser.ValidateNzb(parsed); err != nil {
			return "", NewNonRetryableError("NZB validation failed", err)
		}
	}

	// Calculate the relative virtual directory path for this file
	virtualDir := proc.calculateVirtualDirectory(filePath, relativePath)

	proc.log.Info("Processing file",
		"file_path", filePath,
		"virtual_dir", virtualDir,
		"type", parsed.Type,
		"total_size", parsed.TotalSize,
		"files", len(parsed.Files))

	// Process based on file type
	switch parsed.Type {
	case NzbTypeSingleFile:
		return proc.processSingleFileWithDir(ctx, parsed, virtualDir)
	case NzbTypeMultiFile:
		return proc.processMultiFileWithDir(ctx, parsed, virtualDir)
	case NzbTypeRarArchive:
		return proc.processRarArchiveWithDir(ctx, parsed, virtualDir)
	case NzbType7zArchive:
		return proc.process7zArchiveWithDir(ctx, parsed, virtualDir)
	case NzbTypeStrm:
		return proc.processStrmFileWithDir(ctx, parsed, virtualDir)
	default:
		return "", NewNonRetryableError(fmt.Sprintf("unknown file type: %s", parsed.Type), nil)
	}
}

// processSingleFileWithDir handles NZBs with a single file in a specific virtual directory
func (proc *Processor) processSingleFileWithDir(_ context.Context, parsed *ParsedNzb, virtualDir string) (string, error) {
	regularFiles, _ := proc.separatePar2Files(parsed.Files)

	file := regularFiles[0] // Single file NZB, take the first regular file

	// Create the directory structure if needed
	if err := proc.ensureDirectoryExists(virtualDir); err != nil {
		return "", fmt.Errorf("failed to create directory structure: %w", err)
	}

	// Create virtual file path
	virtualFilePath := filepath.Join(virtualDir, file.Filename)
	virtualFilePath = strings.ReplaceAll(virtualFilePath, string(filepath.Separator), "/")
	// Create file metadata using simplified schema
	fileMeta := proc.metadataService.CreateFileMetadata(
		file.Size,
		parsed.Path,
		metapb.FileStatus_FILE_STATUS_HEALTHY,
		file.Segments,
		file.Encryption,
		file.Password,
		file.Salt,
	)

	// Write file metadata to disk
	if err := proc.metadataService.WriteFileMetadata(virtualFilePath, fileMeta); err != nil {
		return "", fmt.Errorf("failed to write metadata for single file %s: %w", file.Filename, err)
	}

	// Store additional metadata if needed
	if len(file.Groups) > 0 {
		proc.log.Debug("Groups metadata", "file", file.Filename, "groups", strings.Join(file.Groups, ","))
	}

	proc.log.Info("Successfully processed single file NZB",
		"file", file.Filename,
		"virtual_path", virtualFilePath,
		"size", file.Size)

	return virtualFilePath, nil
}

// processMultiFileWithDir handles NZBs with multiple files in a specific virtual directory
func (proc *Processor) processMultiFileWithDir(_ context.Context, parsed *ParsedNzb, virtualDir string) (string, error) {
	// Create a folder named after the NZB file for multi-file imports
	nzbBaseName := strings.TrimSuffix(parsed.Filename, filepath.Ext(parsed.Filename))
	nzbVirtualDir := filepath.Join(virtualDir, nzbBaseName)
	nzbVirtualDir = strings.ReplaceAll(nzbVirtualDir, string(filepath.Separator), "/")

	// Create directory structure based on common path prefixes within the NZB virtual directory
	dirStructure := proc.analyzeDirectoryStructureWithBase(parsed.Files, nzbVirtualDir)

	// Create directories first using real filesystem
	for _, dir := range dirStructure.directories {
		if err := proc.ensureDirectoryExists(dir.path); err != nil {
			return "", fmt.Errorf("failed to create directory %s: %w", dir.path, err)
		}
	}

	regularFiles, _ := proc.separatePar2Files(parsed.Files)

	// Create file entries
	for _, file := range regularFiles {
		parentPath, filename := proc.determineFileLocationWithBase(file, dirStructure, nzbVirtualDir)

		// Ensure parent directory exists
		if err := proc.ensureDirectoryExists(parentPath); err != nil {
			return "", fmt.Errorf("failed to create parent directory %s: %w", parentPath, err)
		}

		// Create virtual file path
		virtualPath := filepath.Join(parentPath, filename)
		virtualPath = strings.ReplaceAll(virtualPath, string(filepath.Separator), "/")

		// Create file metadata using simplified schema
		fileMeta := proc.metadataService.CreateFileMetadata(
			file.Size,
			parsed.Path,
			metapb.FileStatus_FILE_STATUS_HEALTHY,
			file.Segments,
			file.Encryption,
			file.Password,
			file.Salt,
		)

		// Write file metadata to disk
		if err := proc.metadataService.WriteFileMetadata(virtualPath, fileMeta); err != nil {
			return "", fmt.Errorf("failed to write metadata for file %s: %w", filename, err)
		}

		// Store additional metadata if needed
		if len(file.Groups) > 0 {
			proc.log.Debug("Groups metadata", "file", filename, "groups", strings.Join(file.Groups, ","))
		}

		proc.log.Debug("Created metadata file",
			"file", filename,
			"virtual_path", virtualPath,
			"size", file.Size)
	}

	proc.log.Info("Successfully processed multi-file NZB",
		"virtual_dir", nzbVirtualDir,
		"files", len(regularFiles),
		"directories", len(dirStructure.directories))

	return nzbVirtualDir, nil
}

// processRarArchiveWithDir handles NZBs containing RAR archives and regular files in a specific virtual directory
func (proc *Processor) processRarArchiveWithDir(ctx context.Context, parsed *ParsedNzb, virtualDir string) (string, error) {
	overallStart := time.Now()

	// Create a folder named after the NZB file for multi-file imports
	nzbBaseName := strings.TrimSuffix(parsed.Filename, filepath.Ext(parsed.Filename))
	nzbVirtualDir := filepath.Join(virtualDir, nzbBaseName)
	nzbVirtualDir = strings.ReplaceAll(nzbVirtualDir, string(filepath.Separator), "/")

	// Separate RAR files from regular files
	regularFiles, rarFiles := proc.separateRarFiles(parsed.Files)

	// Filter out PAR2 files from regular files
	regularFiles, _ = proc.separatePar2Files(regularFiles)

	// Process regular files first (non-RAR files like MKV, MP4, etc.)
	if len(regularFiles) > 0 {
		proc.log.Info("Processing regular files in RAR archive NZB",
			"virtual_dir", nzbVirtualDir,
			"regular_files", len(regularFiles))

		// Create directory structure for regular files
		dirStructure := proc.analyzeDirectoryStructureWithBase(regularFiles, nzbVirtualDir)

		// Create directories first
		for _, dir := range dirStructure.directories {
			if err := proc.ensureDirectoryExists(dir.path); err != nil {
				return "", fmt.Errorf("failed to create directory %s: %w", dir.path, err)
			}
		}

		// Process each regular file
		for _, file := range regularFiles {
			parentPath, filename := proc.determineFileLocationWithBase(file, dirStructure, nzbVirtualDir)

			// Ensure parent directory exists
			if err := proc.ensureDirectoryExists(parentPath); err != nil {
				return "", fmt.Errorf("failed to create parent directory %s: %w", parentPath, err)
			}

			// Create virtual file path
			virtualPath := filepath.Join(parentPath, filename)
			virtualPath = strings.ReplaceAll(virtualPath, string(filepath.Separator), "/")

			// Create file metadata
			fileMeta := proc.metadataService.CreateFileMetadata(
				file.Size,
				parsed.Path,
				metapb.FileStatus_FILE_STATUS_HEALTHY,
				file.Segments,
				file.Encryption,
				file.Password,
				file.Salt,
			)

			// Write file metadata to disk
			if err := proc.metadataService.WriteFileMetadata(virtualPath, fileMeta); err != nil {
				return "", fmt.Errorf("failed to write metadata for regular file %s: %w", filename, err)
			}

			proc.log.Debug("Created metadata for regular file",
				"file", filename,
				"virtual_path", virtualPath,
				"size", file.Size)
		}

		proc.log.Info("Successfully processed regular files",
			"virtual_dir", nzbVirtualDir,
			"files_processed", len(regularFiles))
	}

	// Process RAR archives if any exist
	if len(rarFiles) > 0 {
		// RAR content will be extracted directly into nzbVirtualDir
		// No need for another nested directory with the same name
		rarDirPath := nzbVirtualDir

		proc.log.Info("Processing RAR archive with progressive content analysis",
			"archive", nzbBaseName,
			"parts", len(rarFiles),
			"rar_dir", rarDirPath)

		// Track if we've found the first video file for early availability
		firstVideoFound := false
		var firstVideoPath string

		// Collect nested RAR files for recursive processing
		var nestedRarContents []rarContent

		// Analyze RAR content using progressive analysis with callback
		analysisStart := time.Now()
		rarContents, err := proc.rarProcessor.AnalyzeRarContentFromNzbProgressive(ctx, rarFiles, func(rc rarContent) bool {
			// Skip directories
			if rc.IsDirectory {
				proc.log.Debug("Skipping directory in RAR archive", "path", rc.InternalPath)
				return true // Continue analysis
			}

			// Check if this is a nested RAR file - collect for later processing
			if proc.isRarFile(rc.Filename) {
				proc.log.Info("Found nested RAR file inside archive",
					"file", rc.Filename,
					"internal_path", rc.InternalPath,
					"size", rc.Size,
					"segments", len(rc.Segments))
				nestedRarContents = append(nestedRarContents, rc)
				return true // Continue analysis - don't create metadata for nested RARs
			}

			// Check if this is a video file
			isVideo := proc.isVideoFile(rc.Filename)

			// Determine the virtual file path for this extracted file
			virtualFilePath := filepath.Join(rarDirPath, rc.InternalPath)
			virtualFilePath = strings.ReplaceAll(virtualFilePath, string(filepath.Separator), "/")

			// Ensure parent directory exists for nested files
			if err := proc.ensureDirectoryExists(filepath.Dir(virtualFilePath)); err != nil {
				proc.log.Warn("Failed to create parent directory for RAR file",
					"file", rc.Filename,
					"error", err)
				return true // Continue analysis
			}

			// Create file metadata
			fileMeta := proc.rarProcessor.CreateFileMetadataFromRarContent(rc, parsed.Path)

			// Write file metadata to disk
			if err := proc.metadataService.WriteFileMetadata(virtualFilePath, fileMeta); err != nil {
				proc.log.Warn("Failed to write metadata for RAR file",
					"file", rc.Filename,
					"error", err)
				return true // Continue analysis
			}

			proc.log.Info("Created metadata for RAR extracted file",
				"file", rc.Filename,
				"internal_path", rc.InternalPath,
				"virtual_path", virtualFilePath,
				"size", rc.Size,
				"is_video", isVideo,
				"segments", len(rc.Segments))

			// If this is the first video file, mark it for early playback
			if isVideo && !firstVideoFound {
				firstVideoFound = true
				firstVideoPath = virtualFilePath
				proc.log.Info("First video file discovered - playback can start",
					"file", rc.Filename,
					"path", virtualFilePath,
					"size", rc.Size)
			}

			return true // Continue analyzing remaining files
		})

		if err != nil {
			proc.log.Error("Failed to analyze RAR archive content",
				"archive", nzbBaseName,
				"error", err)

			return "", err
		}

		proc.log.Info("Successfully analyzed RAR archive content",
			"archive", nzbBaseName,
			"files_in_archive", len(rarContents),
			"nested_rar_files", len(nestedRarContents),
			"first_video_found", firstVideoFound,
			"first_video_path", firstVideoPath,
			"analysis_duration", time.Since(analysisStart))

		// Process nested RAR archives if any were found
		if len(nestedRarContents) > 0 && !firstVideoFound {
			proc.log.Info("Processing nested RAR archives",
				"nested_rar_count", len(nestedRarContents),
				"rar_dir", rarDirPath)

			nestedVideoPath, nestedErr := proc.processNestedRarArchives(ctx, nestedRarContents, rarDirPath, parsed.Path)
			if nestedErr != nil {
				proc.log.Warn("Failed to process nested RAR archives",
					"error", nestedErr)
			} else if nestedVideoPath != "" {
				firstVideoFound = true
				firstVideoPath = nestedVideoPath
				proc.log.Info("Found video file in nested RAR archive",
					"video_path", firstVideoPath)
			}
		}

		proc.log.Info("Completed RAR archive metadata materialization",
			"archive", nzbBaseName,
			"files_processed", len(rarContents),
			"total_duration", time.Since(overallStart))

		proc.log.Info("Successfully processed RAR archive with progressive analysis",
			"archive", nzbBaseName,
			"files_processed", len(rarContents))

		// If we found a video file, return its path instead of the directory
		if firstVideoFound && firstVideoPath != "" {
			proc.log.Info("Returning first video file path for immediate playback",
				"video_path", firstVideoPath,
				"nzb_virtual_dir", nzbVirtualDir)
			return firstVideoPath, nil
		}
	}

	proc.log.Info("Returning storage path from RAR processing",
		"nzb_virtual_dir", nzbVirtualDir,
		"regular_files", len(regularFiles),
		"rar_files", len(rarFiles))

	return nzbVirtualDir, nil
}

// DirectoryStructure represents the analyzed directory structure
type DirectoryStructure struct {
	directories []DirectoryInfo
	commonRoot  string
}

// DirectoryInfo represents information about a directory
type DirectoryInfo struct {
	path   string
	name   string
	parent *string
}

// determineFileLocationWithBase determines where a file should be placed in the virtual structure within a base directory
func (proc *Processor) determineFileLocationWithBase(file ParsedFile, _ *DirectoryStructure, baseDir string) (parentPath, filename string) {
	dir := filepath.Dir(file.Filename)
	name := filepath.Base(file.Filename)

	if dir == "." || dir == "/" {
		return baseDir, name
	}

	virtualPath := filepath.Join(baseDir, dir)
	virtualPath = strings.ReplaceAll(virtualPath, string(filepath.Separator), "/")
	return virtualPath, name
}

// analyzeDirectoryStructureWithBase analyzes files to determine directory structure within a base directory
func (proc *Processor) analyzeDirectoryStructureWithBase(files []ParsedFile, baseDir string) *DirectoryStructure {
	// Simple implementation: group files by common prefixes in their filenames within the base directory
	pathMap := make(map[string]bool)

	for _, file := range files {
		dir := filepath.Dir(file.Filename)
		if dir != "." && dir != "/" {
			// Add the directory path within the base directory
			virtualPath := filepath.Join(baseDir, dir)
			virtualPath = strings.ReplaceAll(virtualPath, string(filepath.Separator), "/")
			pathMap[virtualPath] = true
		}
	}

	var dirs []DirectoryInfo
	for path := range pathMap {
		parent := filepath.Dir(path)
		if parent == "." || parent == "/" {
			parent = baseDir
		}

		dirs = append(dirs, DirectoryInfo{
			path:   path,
			name:   filepath.Base(path),
			parent: stringPtr(parent),
		})
	}

	return &DirectoryStructure{
		directories: dirs,
		commonRoot:  baseDir,
	}
}

// calculateVirtualDirectory determines the virtual directory path based on NZB file location relative to watch root
func (proc *Processor) calculateVirtualDirectory(nzbPath, relativePath string) string {
	// Check if this is a queue item (temp directory NZB)
	if strings.Contains(nzbPath, "/novastream-nzbs/") || strings.Contains(nzbPath, "\\novastream-nzbs\\") {
		// For queue items, use root directory and let the playback service use storage_path
		return "/"
	}

	if relativePath == "" {
		// No watch root specified, place in root directory
		return "/"
	}

	// Clean paths for consistent comparison
	nzbPath = filepath.Clean(nzbPath)
	relativePath = filepath.Clean(relativePath)

	// Get relative path from watch root to NZB file
	relPath, err := filepath.Rel(relativePath, nzbPath)
	if err != nil {
		// If we can't get relative path, default to root
		return "/"
	}

	// Get directory of NZB file (without filename)
	relDir := filepath.Dir(relPath)

	// Convert to virtual path
	if relDir == "." || relDir == "" {
		// NZB is directly in watch root
		return "/"
	}

	// Ensure virtual path starts with / and uses forward slashes
	virtualPath := "/" + strings.ReplaceAll(relDir, string(filepath.Separator), "/")
	return filepath.Clean(virtualPath)
}

// ensureDirectoryExists creates directory structure in the metadata filesystem
func (proc *Processor) ensureDirectoryExists(virtualDir string) error {
	if virtualDir == "/" {
		// Root directory always exists
		return nil
	}

	// Get the actual filesystem path for this virtual directory
	metadataDir := proc.metadataService.GetMetadataDirectoryPath(virtualDir)

	// Create the directory structure using os.MkdirAll
	if err := os.MkdirAll(metadataDir, 0755); err != nil {
		return fmt.Errorf("failed to create metadata directory %s: %w", metadataDir, err)
	}

	return nil
}

// Helper function to create string pointer
func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// isPar2File checks if a filename is a PAR2 repair file
func (proc *Processor) isPar2File(filename string) bool {
	lower := strings.ToLower(filename)
	return strings.HasSuffix(lower, ".par2")
}

// separatePar2Files separates PAR2 files from regular files
func (proc *Processor) separatePar2Files(files []ParsedFile) ([]ParsedFile, []ParsedFile) {
	var regularFiles []ParsedFile
	var par2Files []ParsedFile

	for _, file := range files {
		if proc.isPar2File(file.Filename) {
			par2Files = append(par2Files, file)
		} else {
			regularFiles = append(regularFiles, file)
		}
	}

	return regularFiles, par2Files
}

// separateRarFiles separates RAR files from regular files
func (proc *Processor) separateRarFiles(files []ParsedFile) ([]ParsedFile, []ParsedFile) {
	var regularFiles []ParsedFile
	var rarFiles []ParsedFile

	for _, file := range files {
		if file.IsRarArchive {
			rarFiles = append(rarFiles, file)
		} else {
			regularFiles = append(regularFiles, file)
		}
	}

	return regularFiles, rarFiles
}

// processStrmFileWithDir handles STRM files (single file from NXG link) in a specific virtual directory
func (proc *Processor) processStrmFileWithDir(_ context.Context, parsed *ParsedNzb, virtualDir string) (string, error) {
	if len(parsed.Files) != 1 {
		return "", NewNonRetryableError(fmt.Sprintf("STRM file should contain exactly one file, got %d", len(parsed.Files)), nil)
	}

	file := parsed.Files[0]

	// Create the directory structure if needed
	if err := proc.ensureDirectoryExists(virtualDir); err != nil {
		return "", fmt.Errorf("failed to create directory structure: %w", err)
	}

	// Create virtual file path
	virtualFilePath := filepath.Join(virtualDir, file.Filename)
	virtualFilePath = strings.ReplaceAll(virtualFilePath, string(filepath.Separator), "/")

	// Create file metadata using simplified schema
	fileMeta := proc.metadataService.CreateFileMetadata(
		file.Size,
		parsed.Path,
		metapb.FileStatus_FILE_STATUS_HEALTHY,
		file.Segments,
		file.Encryption,
		file.Password,
		file.Salt,
	)

	// Write file metadata to disk
	if err := proc.metadataService.WriteFileMetadata(virtualFilePath, fileMeta); err != nil {
		return "", fmt.Errorf("failed to write metadata for STRM file %s: %w", file.Filename, err)
	}

	proc.log.Info("Successfully processed STRM file",
		"file", file.Filename,
		"virtual_path", virtualFilePath,
		"size", file.Size,
		"segments", len(file.Segments))

	return virtualDir, nil
}

// isVideoFile checks if a filename has a common video extension
func (proc *Processor) isVideoFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	videoExtensions := []string{
		".mkv", ".mp4", ".avi", ".mov", ".wmv", ".flv", ".webm",
		".m4v", ".mpg", ".mpeg", ".m2ts", ".ts", ".vob", ".ogv",
	}
	for _, videoExt := range videoExtensions {
		if ext == videoExt {
			return true
		}
	}
	return false
}

// isRarFile checks if a filename is a RAR archive file
func (proc *Processor) isRarFile(filename string) bool {
	lower := strings.ToLower(filename)
	// Check common RAR patterns: .rar, .r00, .r01, .part01.rar, etc.
	if strings.HasSuffix(lower, ".rar") {
		return true
	}
	// Check .r## pattern (e.g., .r00, .r01)
	if len(lower) > 3 && lower[len(lower)-3] == '.' && lower[len(lower)-2] == 'r' {
		lastChar := lower[len(lower)-1]
		if lastChar >= '0' && lastChar <= '9' {
			return true
		}
	}
	return false
}

// processNestedRarArchives handles RAR files found inside another RAR archive
func (proc *Processor) processNestedRarArchives(ctx context.Context, nestedRarContents []rarContent, rarDirPath string, sourceNzbPath string) (string, error) {
	if len(nestedRarContents) == 0 {
		return "", nil
	}

	// Convert rarContent to ParsedFile format for the RAR processor
	nestedRarFiles := make([]ParsedFile, len(nestedRarContents))
	for i, rc := range nestedRarContents {
		nestedRarFiles[i] = ParsedFile{
			Filename:     rc.Filename,
			Size:         rc.Size,
			Segments:     rc.Segments,
			IsRarArchive: true,
		}
	}

	proc.log.Info("Processing nested RAR archive",
		"parts", len(nestedRarFiles),
		"rar_dir", rarDirPath)

	// Track if we've found a video file
	var firstVideoPath string

	// Analyze the nested RAR content
	nestedContents, err := proc.rarProcessor.AnalyzeRarContentFromNzbProgressive(ctx, nestedRarFiles, func(rc rarContent) bool {
		// Skip directories
		if rc.IsDirectory {
			proc.log.Debug("Skipping directory in nested RAR archive", "path", rc.InternalPath)
			return true
		}

		// Check if this is yet another nested RAR (prevent infinite recursion - max 2 levels)
		if proc.isRarFile(rc.Filename) {
			proc.log.Warn("Found deeply nested RAR file (3+ levels) - skipping to prevent infinite recursion",
				"file", rc.Filename,
				"internal_path", rc.InternalPath)
			return true
		}

		// Check if this is a video file
		isVideo := proc.isVideoFile(rc.Filename)

		// Determine the virtual file path for this extracted file
		virtualFilePath := filepath.Join(rarDirPath, rc.InternalPath)
		virtualFilePath = strings.ReplaceAll(virtualFilePath, string(filepath.Separator), "/")

		// Ensure parent directory exists for nested files
		if err := proc.ensureDirectoryExists(filepath.Dir(virtualFilePath)); err != nil {
			proc.log.Warn("Failed to create parent directory for nested RAR file",
				"file", rc.Filename,
				"error", err)
			return true
		}

		// Create file metadata
		fileMeta := proc.rarProcessor.CreateFileMetadataFromRarContent(rc, sourceNzbPath)

		// Write file metadata to disk
		if err := proc.metadataService.WriteFileMetadata(virtualFilePath, fileMeta); err != nil {
			proc.log.Warn("Failed to write metadata for nested RAR file",
				"file", rc.Filename,
				"error", err)
			return true
		}

		proc.log.Info("Created metadata for nested RAR extracted file",
			"file", rc.Filename,
			"internal_path", rc.InternalPath,
			"virtual_path", virtualFilePath,
			"size", rc.Size,
			"is_video", isVideo,
			"segments", len(rc.Segments))

		// If this is the first video file, mark it
		if isVideo && firstVideoPath == "" {
			firstVideoPath = virtualFilePath
			proc.log.Info("First video file discovered in nested RAR - playback can start",
				"file", rc.Filename,
				"path", virtualFilePath,
				"size", rc.Size)
		}

		return true
	})

	if err != nil {
		return "", fmt.Errorf("failed to analyze nested RAR archive: %w", err)
	}

	proc.log.Info("Successfully processed nested RAR archive",
		"files_in_archive", len(nestedContents),
		"video_found", firstVideoPath != "")

	return firstVideoPath, nil
}

// separate7zFiles separates 7z files from regular files
func (proc *Processor) separate7zFiles(files []ParsedFile) ([]ParsedFile, []ParsedFile) {
	var regularFiles []ParsedFile
	var szFiles []ParsedFile

	for _, file := range files {
		if file.Is7zArchive {
			szFiles = append(szFiles, file)
		} else {
			regularFiles = append(regularFiles, file)
		}
	}

	return regularFiles, szFiles
}

// process7zArchiveWithDir handles NZBs containing 7z archives and regular files in a specific virtual directory
func (proc *Processor) process7zArchiveWithDir(ctx context.Context, parsed *ParsedNzb, virtualDir string) (string, error) {
	overallStart := time.Now()

	// Create a folder named after the NZB file for multi-file imports
	nzbBaseName := strings.TrimSuffix(parsed.Filename, filepath.Ext(parsed.Filename))
	nzbVirtualDir := filepath.Join(virtualDir, nzbBaseName)
	nzbVirtualDir = strings.ReplaceAll(nzbVirtualDir, string(filepath.Separator), "/")

	// Separate 7z files from regular files
	regularFiles, szFiles := proc.separate7zFiles(parsed.Files)

	// Filter out PAR2 files from regular files
	regularFiles, _ = proc.separatePar2Files(regularFiles)

	// Process regular files first (non-7z files like MKV, MP4, etc.)
	if len(regularFiles) > 0 {
		proc.log.Info("Processing regular files in 7z archive NZB",
			"virtual_dir", nzbVirtualDir,
			"regular_files", len(regularFiles))

		// Create directory structure for regular files
		dirStructure := proc.analyzeDirectoryStructureWithBase(regularFiles, nzbVirtualDir)

		// Create directories first
		for _, dir := range dirStructure.directories {
			if err := proc.ensureDirectoryExists(dir.path); err != nil {
				return "", fmt.Errorf("failed to create directory %s: %w", dir.path, err)
			}
		}

		// Process each regular file
		for _, file := range regularFiles {
			parentPath, filename := proc.determineFileLocationWithBase(file, dirStructure, nzbVirtualDir)

			// Ensure parent directory exists
			if err := proc.ensureDirectoryExists(parentPath); err != nil {
				return "", fmt.Errorf("failed to create parent directory %s: %w", parentPath, err)
			}

			// Create virtual file path
			virtualPath := filepath.Join(parentPath, filename)
			virtualPath = strings.ReplaceAll(virtualPath, string(filepath.Separator), "/")

			// Create file metadata
			fileMeta := proc.metadataService.CreateFileMetadata(
				file.Size,
				parsed.Path,
				metapb.FileStatus_FILE_STATUS_HEALTHY,
				file.Segments,
				file.Encryption,
				file.Password,
				file.Salt,
			)

			// Write file metadata to disk
			if err := proc.metadataService.WriteFileMetadata(virtualPath, fileMeta); err != nil {
				return "", fmt.Errorf("failed to write metadata for regular file %s: %w", filename, err)
			}

			proc.log.Debug("Created metadata for regular file",
				"file", filename,
				"virtual_path", virtualPath,
				"size", file.Size)
		}

		proc.log.Info("Successfully processed regular files",
			"virtual_dir", nzbVirtualDir,
			"files_processed", len(regularFiles))
	}

	// Process 7z archives if any exist
	if len(szFiles) > 0 {
		// 7z content will be extracted directly into nzbVirtualDir
		szDirPath := nzbVirtualDir

		proc.log.Info("Processing 7z archive with progressive content analysis",
			"archive", nzbBaseName,
			"parts", len(szFiles),
			"sz_dir", szDirPath)

		// Track if we've found the first video file for early availability
		firstVideoFound := false
		var firstVideoPath string

		// Analyze 7z content using progressive analysis with callback
		analysisStart := time.Now()
		szContents, err := proc.sevenZipProcessor.Analyze7zContentFromNzbProgressive(ctx, szFiles, func(sc sevenZipContent) bool {
			// Skip directories
			if sc.IsDirectory {
				proc.log.Debug("Skipping directory in 7z archive", "path", sc.InternalPath)
				return true // Continue analysis
			}

			// Check if this is a video file
			isVideo := proc.isVideoFile(sc.Filename)

			// Determine the virtual file path for this extracted file
			virtualFilePath := filepath.Join(szDirPath, sc.InternalPath)
			virtualFilePath = strings.ReplaceAll(virtualFilePath, string(filepath.Separator), "/")

			// Ensure parent directory exists for nested files
			if err := proc.ensureDirectoryExists(filepath.Dir(virtualFilePath)); err != nil {
				proc.log.Warn("Failed to create parent directory for 7z file",
					"file", sc.Filename,
					"error", err)
				return true // Continue analysis
			}

			// Create file metadata
			fileMeta := proc.sevenZipProcessor.CreateFileMetadataFrom7zContent(sc, parsed.Path)

			// Write file metadata to disk
			if err := proc.metadataService.WriteFileMetadata(virtualFilePath, fileMeta); err != nil {
				proc.log.Warn("Failed to write metadata for 7z file",
					"file", sc.Filename,
					"error", err)
				return true // Continue analysis
			}

			proc.log.Info("Created metadata for 7z extracted file",
				"file", sc.Filename,
				"internal_path", sc.InternalPath,
				"virtual_path", virtualFilePath,
				"size", sc.Size,
				"is_video", isVideo,
				"segments", len(sc.Segments))

			// If this is the first video file, mark it for early playback
			if isVideo && !firstVideoFound {
				firstVideoFound = true
				firstVideoPath = virtualFilePath
				proc.log.Info("First video file discovered - playback can start",
					"file", sc.Filename,
					"path", virtualFilePath,
					"size", sc.Size)
			}

			return true // Continue analyzing remaining files
		})

		if err != nil {
			proc.log.Error("Failed to analyze 7z archive content",
				"archive", nzbBaseName,
				"error", err)

			return "", err
		}

		proc.log.Info("Successfully analyzed 7z archive content",
			"archive", nzbBaseName,
			"files_in_archive", len(szContents),
			"first_video_found", firstVideoFound,
			"first_video_path", firstVideoPath,
			"analysis_duration", time.Since(analysisStart))

		proc.log.Info("Completed 7z archive metadata materialization",
			"archive", nzbBaseName,
			"files_processed", len(szContents),
			"total_duration", time.Since(overallStart))

		proc.log.Info("Successfully processed 7z archive with progressive analysis",
			"archive", nzbBaseName,
			"files_processed", len(szContents))

		// If we found a video file, return its path instead of the directory
		if firstVideoFound && firstVideoPath != "" {
			proc.log.Info("Returning first video file path for immediate playback",
				"video_path", firstVideoPath,
				"nzb_virtual_dir", nzbVirtualDir)
			return firstVideoPath, nil
		}
	}

	proc.log.Info("Returning storage path from 7z processing",
		"nzb_virtual_dir", nzbVirtualDir,
		"regular_files", len(regularFiles),
		"sz_files", len(szFiles))

	return nzbVirtualDir, nil
}
