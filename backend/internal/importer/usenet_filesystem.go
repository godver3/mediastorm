package importer

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/javi11/nntppool"
	metapb "novastream/internal/nzb/metadata/proto"
	"novastream/internal/usenet"
)

// Compile-time interface checks
var (
	_ fs.File   = (*UsenetFile)(nil)       // UsenetFile implements fs.File
	_ io.Seeker = (*UsenetFile)(nil)       // UsenetFile implements io.Seeker
	_ fs.FS     = (*UsenetFileSystem)(nil) // UsenetFileSystem implements fs.FS
)

// UsenetFileSystem implements fs.FS for reading RAR archives from Usenet
// This allows rardecode.OpenReader to access multi-part RAR files without downloading them entirely
type UsenetFileSystem struct {
	ctx            context.Context
	cp             nntppool.UsenetConnectionPool
	files          []ParsedFile
	maxWorkers     int
	maxCacheSizeMB int
	fileIndex      map[string]*ParsedFile
}

// UsenetFile implements fs.File and io.Seeker for reading individual RAR parts from Usenet
// The Seeker interface allows rardecode.OpenReader to efficiently seek within RAR parts
type UsenetFile struct {
	name           string
	file           *ParsedFile
	cp             nntppool.UsenetConnectionPool
	ctx            context.Context
	maxWorkers     int
	maxCacheSizeMB int
	size           int64
	reader         io.ReadCloser
	position       int64
	closed         bool
	// Optimization fields for RAR analysis
	analysisMode      bool  // true during initial RAR header analysis
	currentRangeEnd   int64 // end position of current reader's range
	currentChunkSize  int64 // current chunk size for progressive expansion
	// Read-ahead buffer for caching recently read data (reduces re-downloads)
	bufferData     []byte // cached data
	bufferStart    int64  // file position where buffer starts
	bufferSize     int    // amount of valid data in buffer
	maxBufferSize  int    // maximum buffer size (512KB for RAR headers)
}

// UsenetFileInfo implements fs.FileInfo for RAR part files
type UsenetFileInfo struct {
	name string
	size int64
}

// NewUsenetFileSystem creates a new filesystem for accessing RAR parts from Usenet
func NewUsenetFileSystem(ctx context.Context, cp nntppool.UsenetConnectionPool, files []ParsedFile, maxWorkers int, maxCacheSizeMB int) *UsenetFileSystem {
	copiedFiles := make([]ParsedFile, len(files))
	copy(copiedFiles, files)

	fileIndex := make(map[string]*ParsedFile, len(copiedFiles)*2)
	for i := range copiedFiles {
		f := &copiedFiles[i]
		fileIndex[f.Filename] = f
		fileIndex[path.Base(f.Filename)] = f
		fileIndex[filepath.Base(f.Filename)] = f
	}

	return &UsenetFileSystem{
		ctx:            ctx,
		cp:             cp,
		files:          copiedFiles,
		maxWorkers:     maxWorkers,
		maxCacheSizeMB: maxCacheSizeMB,
		fileIndex:      fileIndex,
	}
}

func (ufs *UsenetFileSystem) lookupFile(name string) *ParsedFile {
	if pf, ok := ufs.fileIndex[name]; ok {
		return pf
	}

	if base := path.Base(name); base != name {
		if pf, ok := ufs.fileIndex[base]; ok {
			return pf
		}
	}

	if base := filepath.Base(name); base != name {
		if pf, ok := ufs.fileIndex[base]; ok {
			return pf
		}
	}

	return nil
}

// Open opens a file in the Usenet filesystem
func (ufs *UsenetFileSystem) Open(name string) (fs.File, error) {
	name = path.Clean(name)

	pf := ufs.lookupFile(name)
	if pf != nil {
		maxBufSize := 512 * 1024 // 512KB buffer for RAR header caching
		return &UsenetFile{
			name:             name,
			file:             pf,
			cp:               ufs.cp,
			ctx:              ufs.ctx,
			maxWorkers:       ufs.maxWorkers,
			maxCacheSizeMB:   ufs.maxCacheSizeMB,
			size:             pf.Size,
			position:         0,
			closed:           false,
			analysisMode:     true,           // Start in analysis mode for efficient RAR header reading
			currentRangeEnd:  0,
			currentChunkSize: 256 * 1024,     // Start with 256KB chunks
			bufferData:       make([]byte, maxBufSize),
			bufferStart:      -1,             // -1 indicates empty buffer
			bufferSize:       0,
			maxBufferSize:    maxBufSize,
		}, nil
	}

	return nil, &fs.PathError{
		Op:   "open",
		Path: name,
		Err:  fs.ErrNotExist,
	}
}

// Stat returns file information for a file in the Usenet filesystem
// This implements the rarlist.FileSystem interface
func (ufs *UsenetFileSystem) Stat(path string) (os.FileInfo, error) {
	path = filepath.Clean(path)

	if pf := ufs.lookupFile(path); pf != nil {
		return &UsenetFileInfo{
			name: filepath.Base(pf.Filename),
			size: pf.Size,
		}, nil
	}

	return nil, &fs.PathError{
		Op:   "stat",
		Path: path,
		Err:  fs.ErrNotExist,
	}
}

// UsenetFile methods implementing fs.File interface

func (uf *UsenetFile) Stat() (fs.FileInfo, error) {
	return &UsenetFileInfo{
		name: uf.name,
		size: uf.size,
	}, nil
}

func (uf *UsenetFile) Read(p []byte) (n int, err error) {
	if uf.closed {
		return 0, fs.ErrClosed
	}

	// Check for context cancellation
	select {
	case <-uf.ctx.Done():
		return 0, uf.ctx.Err()
	default:
	}

	// Try to serve from buffer first
	if uf.bufferStart >= 0 && uf.position >= uf.bufferStart && uf.position < uf.bufferStart+int64(uf.bufferSize) {
		// Position is in buffer, serve from cache
		offsetInBuffer := int(uf.position - uf.bufferStart)
		bytesAvailable := uf.bufferSize - offsetInBuffer
		bytesToCopy := len(p)
		if bytesToCopy > bytesAvailable {
			bytesToCopy = bytesAvailable
		}
		copy(p, uf.bufferData[offsetInBuffer:offsetInBuffer+bytesToCopy])
		uf.position += int64(bytesToCopy)
		return bytesToCopy, nil
	}

	// Create reader if not exists
	if uf.reader == nil {
		var rangeEnd int64
		var workers int

		if uf.analysisMode {
			// For RAR analysis, use progressive chunk sizes
			// Start with 256KB, double each time we need more
			rangeEnd = uf.position + uf.currentChunkSize - 1
			if rangeEnd >= uf.size {
				rangeEnd = uf.size - 1
			}
			// Use more workers during analysis for faster RAR header reads
			workers = uf.maxWorkers
		} else {
			// Streaming mode: read to end of file with full workers
			rangeEnd = uf.size - 1
			workers = uf.maxWorkers
		}

		reader, err := uf.createUsenetReaderWithWorkers(uf.ctx, uf.position, rangeEnd, workers)
		if err != nil {
			return 0, fmt.Errorf("failed to create usenet reader: %w", err)
		}

		uf.reader = reader
		uf.currentRangeEnd = rangeEnd
	}

	n, err = uf.reader.Read(p)

	// Cache the data we just read if in analysis mode (for potential re-reads)
	if uf.analysisMode && n > 0 {
		// Update buffer with newly read data
		uf.bufferStart = uf.position
		copySize := n
		if copySize > uf.maxBufferSize {
			copySize = uf.maxBufferSize
		}
		copy(uf.bufferData, p[:copySize])
		uf.bufferSize = copySize
	}

	uf.position += int64(n)

	// If in analysis mode and approaching end of current range, expand it
	if uf.analysisMode && uf.position >= uf.currentRangeEnd-4096 && uf.currentRangeEnd < uf.size-1 {
		// Close current reader to trigger recreation with larger range
		if uf.reader != nil {
			uf.reader.Close()
			uf.reader = nil
		}
		// Double chunk size for next read (progressive expansion)
		uf.currentChunkSize *= 2
		// Cap at 4MB to avoid creating too large ranges
		if uf.currentChunkSize > 4*1024*1024 {
			uf.currentChunkSize = 4 * 1024 * 1024
		}
	}

	return n, err
}

func (uf *UsenetFile) Close() error {
	if uf.closed {
		return nil
	}

	uf.closed = true

	if uf.reader != nil {
		return uf.reader.Close()
	}

	return nil
}

// Seek implements io.Seeker interface for efficient RAR part access
func (uf *UsenetFile) Seek(offset int64, whence int) (int64, error) {
	if uf.closed {
		return 0, fs.ErrClosed
	}

	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = uf.position + offset
	case io.SeekEnd:
		abs = uf.size + offset
	default:
		return 0, fmt.Errorf("invalid whence value: %d", whence)
	}

	if abs < 0 {
		return 0, fmt.Errorf("negative seek position: %d", abs)
	}

	if abs > uf.size {
		return 0, fmt.Errorf("seek position beyond file size: %d > %d", abs, uf.size)
	}

	// Check if seeking to a position within our buffer
	inBuffer := uf.bufferStart >= 0 && abs >= uf.bufferStart && abs < uf.bufferStart+int64(uf.bufferSize)

	// Seeking usually indicates playback is starting, switch to streaming mode
	// But small seeks within buffer might just be RAR header parsing
	if uf.analysisMode && abs != 0 && !inBuffer {
		uf.analysisMode = false
	}

	// If seeking to a different position outside buffer, close current reader
	if abs != uf.position && !inBuffer && uf.reader != nil {
		uf.reader.Close()
		uf.reader = nil
	}

	uf.position = abs
	return abs, nil
}

// createUsenetReaderWithWorkers creates a Usenet reader for the specified range with custom worker count
func (uf *UsenetFile) createUsenetReaderWithWorkers(ctx context.Context, start, end int64, workers int) (io.ReadCloser, error) {
	// Filter segments for this specific file
	loader := dbSegmentLoader{segs: uf.file.Segments}

	// Limit segments to prevent memory explosion during imports
	// Use workers * 8 with a cap of 150 segments max
	maxSegments := workers * 8
	if maxSegments > 150 {
		maxSegments = 150
	}
	if maxSegments < 20 {
		maxSegments = 20
	}

	rg := usenet.GetSegmentsInRangeWithLimit(start, end, loader, maxSegments)

	// Log when using optimized parameters for RAR analysis
	if uf.analysisMode {
		rangeSize := end - start + 1
		slog.Debug("Creating optimized reader for RAR analysis",
			"file", filepath.Base(uf.name),
			"range_size_kb", rangeSize/1024,
			"workers", workers,
			"max_workers", uf.maxWorkers,
			"max_segments", maxSegments)
	}

	return usenet.NewUsenetReader(ctx, uf.cp, rg, workers, uf.maxCacheSizeMB)
}

// createUsenetReader creates a Usenet reader for the specified range with default worker count
func (uf *UsenetFile) createUsenetReader(ctx context.Context, start, end int64) (io.ReadCloser, error) {
	return uf.createUsenetReaderWithWorkers(ctx, start, end, uf.maxWorkers)
}

// dbSegmentLoader implements the segment loader interface for database segments
type dbSegmentLoader struct {
	segs []*metapb.SegmentData
}

func (dl dbSegmentLoader) GetSegmentCount() int {
	return len(dl.segs)
}

func (dl dbSegmentLoader) GetSegment(index int) (segment usenet.Segment, groups []string, ok bool) {
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

// UsenetFileInfo methods implementing fs.FileInfo interface

func (ufi *UsenetFileInfo) Name() string       { return ufi.name }
func (ufi *UsenetFileInfo) Size() int64        { return ufi.size }
func (ufi *UsenetFileInfo) Mode() fs.FileMode  { return 0644 }
func (ufi *UsenetFileInfo) ModTime() time.Time { return time.Now() }
func (ufi *UsenetFileInfo) IsDir() bool        { return false }
func (ufi *UsenetFileInfo) Sys() interface{}   { return nil }
