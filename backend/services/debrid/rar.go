package debrid

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
)

// rarFileEntry describes a STORED (uncompressed) file inside a RAR archive.
type rarFileEntry struct {
	Name       string
	DataOffset int64
	Size       int64
}

// RAR4 constants
const (
	rar4Signature   = "Rar!\x1a\x07\x00" // 7-byte RAR4 magic
	rar4HeaderFile  = 0x74               // file header type
	rar4MethodStore = 0x30               // STORE method (uncompressed)
	rar4FlagLarge   = 0x0100             // LARGE_FILE flag (>4GB)
	rar4FlagAddSize = 0x8000             // ADD_SIZE flag (header has ADD_SIZE field)
	rarProbeFetch   = 128 * 1024         // fetch first 128KB for header parsing
)

// rarHeaderFetch is the size of each small range fetch for reading RAR headers.
const rarHeaderFetch = 4096

// fetchRange fetches a byte range from a URL via HTTP Range request.
func fetchRange(ctx context.Context, httpClient *http.Client, url string, start, length int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, start+length-1))

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	return io.ReadAll(io.LimitReader(resp.Body, length))
}

// parseRAR4StoredFiles fetches RAR headers via HTTP Range requests and returns entries for
// STORED (uncompressed) files. Only RAR4 with method STORE is supported.
// For multi-GB archives, it does iterative small fetches — one per file header — jumping
// past each file's data to find the next header.
func parseRAR4StoredFiles(ctx context.Context, httpClient *http.Client, url string) ([]rarFileEntry, error) {
	// Fetch first chunk to validate signature and read initial headers
	data, err := fetchRange(ctx, httpClient, url, 0, rarProbeFetch)
	if err != nil {
		return nil, fmt.Errorf("rar probe: fetch: %w", err)
	}

	if len(data) < 7 || string(data[:7]) != rar4Signature {
		if len(data) >= 8 && string(data[:7]) == rar4Signature[:6] && data[6] == 0x01 {
			return nil, fmt.Errorf("rar probe: RAR5 format not supported")
		}
		return nil, fmt.Errorf("rar probe: not a RAR4 archive")
	}

	// Parse all headers we can from the initial chunk, then iteratively fetch
	// more data at positions past each file's data block.
	var entries []rarFileEntry
	absPos := int64(7) // absolute byte position in the RAR file, past signature

	// Parse the initial chunk starting after signature
	pos := 7 // position within 'data'
	const maxFiles = 50

	for len(entries) < maxFiles {
		// If we don't have enough data at current position, fetch more
		if pos+7 > len(data) {
			chunk, fetchErr := fetchRange(ctx, httpClient, url, absPos, rarHeaderFetch)
			if fetchErr != nil {
				break
			}
			if len(chunk) < 7 {
				break
			}
			data = chunk
			pos = 0
		}

		headerType := data[pos+2]
		headerFlags := binary.LittleEndian.Uint16(data[pos+3 : pos+5])
		headerSize := int(binary.LittleEndian.Uint16(data[pos+5 : pos+7]))

		if headerSize < 7 {
			break
		}

		// If we don't have the full header, fetch it
		if pos+headerSize > len(data) {
			chunk, fetchErr := fetchRange(ctx, httpClient, url, absPos, int64(headerSize)+1024)
			if fetchErr != nil {
				break
			}
			if len(chunk) < headerSize {
				break
			}
			data = chunk
			pos = 0
		}

		if headerType == rar4HeaderFile {
			entry, parseErr := parseRAR4FileHeader(data, pos, headerFlags, headerSize)
			if parseErr != nil {
				// Skip this file header (possibly compressed)
				absPos += int64(headerSize)
				pos += headerSize
				continue
			}

			// Calculate the file's packed size to know where the next header is
			compSize := int64(binary.LittleEndian.Uint32(data[pos+7 : pos+11]))
			if headerFlags&rar4FlagLarge != 0 && pos+36 <= len(data) {
				highCompSize := int64(binary.LittleEndian.Uint32(data[pos+32 : pos+36]))
				compSize |= highCompSize << 32
			}

			if entry != nil {
				// Fix DataOffset to be absolute (not relative to current chunk)
				entry.DataOffset = absPos + int64(headerSize)
				entries = append(entries, *entry)
			}

			// Jump past header + file data to the next header
			absPos += int64(headerSize) + compSize
			// Force a fresh fetch at the new position
			pos = len(data) // triggers fetch on next iteration
			continue
		}

		// Non-file block
		blockSize := int64(headerSize)
		if headerFlags&rar4FlagAddSize != 0 && pos+11 <= len(data) {
			addSize := int64(binary.LittleEndian.Uint32(data[pos+7 : pos+11]))
			blockSize += addSize
		}
		absPos += blockSize
		pos += int(blockSize)
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("rar probe: no stored file entries found")
	}

	return entries, nil
}

// parseRAR4Headers parses RAR4 block headers from raw bytes and extracts STORED file entries.
func parseRAR4Headers(data []byte) ([]rarFileEntry, error) {
	pos := 7 // skip signature
	var entries []rarFileEntry

	for pos+7 <= len(data) {
		if pos+7 > len(data) {
			break
		}

		// RAR4 block header: HEAD_CRC(2) HEAD_TYPE(1) HEAD_FLAGS(2) HEAD_SIZE(2)
		headerType := data[pos+2]
		headerFlags := binary.LittleEndian.Uint16(data[pos+3 : pos+5])
		headerSize := int(binary.LittleEndian.Uint16(data[pos+5 : pos+7]))

		if headerSize < 7 || pos+headerSize > len(data) {
			break
		}

		if headerType == rar4HeaderFile {
			entry, err := parseRAR4FileHeader(data, pos, headerFlags, headerSize)
			if err != nil {
				// Skip this entry but continue parsing
				pos += headerSize
				continue
			}
			if entry != nil {
				entries = append(entries, *entry)
			}

			// Skip past file data
			compSize := int64(binary.LittleEndian.Uint32(data[pos+7 : pos+11]))
			if headerFlags&rar4FlagLarge != 0 && pos+36 <= len(data) {
				highCompSize := int64(binary.LittleEndian.Uint32(data[pos+32 : pos+36]))
				compSize |= highCompSize << 32
			}
			pos += headerSize + int(compSize)
			continue
		}

		// Non-file block: advance by header size + optional ADD_SIZE
		blockSize := headerSize
		if headerFlags&rar4FlagAddSize != 0 && pos+headerSize >= 11 {
			addSize := int(binary.LittleEndian.Uint32(data[pos+7 : pos+11]))
			blockSize += addSize
		}
		pos += blockSize
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("rar probe: no stored file entries found")
	}

	return entries, nil
}

// parseRAR4FileHeader parses a single RAR4 file header block.
func parseRAR4FileHeader(data []byte, pos int, headerFlags uint16, headerSize int) (*rarFileEntry, error) {
	// File header layout after common 7 bytes:
	//   PACK_SIZE(4) UNP_SIZE(4) HOST_OS(1) FILE_CRC(4) FTIME(4) UNP_VER(1) METHOD(1) NAME_SIZE(2) ATTR(4)
	// = 7 + 4 + 4 + 1 + 4 + 4 + 1 + 1 + 2 + 4 = 32 bytes minimum
	// 7 common + 25 fixed = 32 bytes minimum before filename
	if pos+32 > len(data) {
		return nil, fmt.Errorf("file header too short")
	}

	// RAR4 file header fixed fields after 7-byte common header:
	// PACK_SIZE(4) UNP_SIZE(4) HOST_OS(1) FILE_CRC(4) FTIME(4) UNP_VER(1) METHOD(1) NAME_SIZE(2) ATTR(4) = 25 bytes
	compSize := int64(binary.LittleEndian.Uint32(data[pos+7 : pos+11]))
	unpSize := int64(binary.LittleEndian.Uint32(data[pos+11 : pos+15]))
	method := data[pos+25]       // UNP_VER at +24, METHOD at +25
	nameSize := int(binary.LittleEndian.Uint16(data[pos+26 : pos+28]))

	// Handle LARGE_FILE flag for >4GB files
	// HIGH_PACK_SIZE(4) + HIGH_UNP_SIZE(4) come after ATTR
	if headerFlags&rar4FlagLarge != 0 {
		if pos+40 > len(data) {
			return nil, fmt.Errorf("large file header too short")
		}
		highCompSize := int64(binary.LittleEndian.Uint32(data[pos+32 : pos+36]))
		highUnpSize := int64(binary.LittleEndian.Uint32(data[pos+36 : pos+40]))
		compSize |= highCompSize << 32
		unpSize |= highUnpSize << 32
	}

	if method != rar4MethodStore {
		return nil, fmt.Errorf("compressed file (method 0x%02x), not STORE", method)
	}

	// Filename starts after fixed fields (7 common + 25 fixed = 32)
	// With LARGE_FILE: +8 more bytes for high size fields
	nameStart := pos + 32
	if headerFlags&rar4FlagLarge != 0 {
		nameStart = pos + 40
	}

	if nameStart+nameSize > len(data) {
		return nil, fmt.Errorf("filename extends beyond data")
	}

	rawName := data[nameStart : nameStart+nameSize]

	// RAR4 Unicode filename encoding: when the filename contains non-ASCII chars,
	// the name field contains the ASCII name followed by a null byte, then Unicode
	// encoding data. We only need the ASCII portion for matching.
	name := string(rawName)
	if nullIdx := strings.IndexByte(name, 0); nullIdx >= 0 {
		name = name[:nullIdx]
	}

	// Data starts right after the header
	dataOffset := int64(pos + headerSize)

	// For STORE method, compressed size == uncompressed size
	_ = compSize

	return &rarFileEntry{
		Name:       name,
		DataOffset: dataOffset,
		Size:       unpSize,
	}, nil
}

// findRAREntry matches a torrent file path to a RAR entry by basename.
func findRAREntry(entries []rarFileEntry, targetPath string) *rarFileEntry {
	targetPath = strings.TrimLeft(targetPath, "/")
	targetBase := filepath.Base(targetPath)

	// Try basename match (RAR entries may use backslash separators from Windows)
	for i := range entries {
		entryName := strings.ReplaceAll(entries[i].Name, "\\", "/")
		entryBase := filepath.Base(entryName)
		if strings.EqualFold(entryBase, targetBase) {
			return &entries[i]
		}
	}

	// Try full path match with normalized separators
	for i := range entries {
		entryName := strings.ReplaceAll(entries[i].Name, "\\", "/")
		if strings.EqualFold(entryName, targetPath) {
			return &entries[i]
		}
	}

	return nil
}

// translateRangeForRAR adjusts a Range header to account for the file's offset within the RAR.
// Input:  "bytes=S-E" (client-relative), offset, fileSize
// Output: "bytes=(S+offset)-(E+offset)" (RAR-absolute)
func translateRangeForRAR(rangeHeader string, offset, fileSize int64) string {
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		return rangeHeader
	}

	spec := strings.TrimPrefix(rangeHeader, "bytes=")
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return rangeHeader
	}

	startStr, endStr := parts[0], parts[1]

	if startStr == "" {
		// Suffix range: bytes=-N (last N bytes)
		n, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil || n <= 0 {
			return rangeHeader
		}
		if n > fileSize {
			n = fileSize
		}
		newStart := offset + fileSize - n
		newEnd := offset + fileSize - 1
		return fmt.Sprintf("bytes=%d-%d", newStart, newEnd)
	}

	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil {
		return rangeHeader
	}

	// Clamp start to file boundary
	if start >= fileSize {
		start = fileSize - 1
	}

	if endStr == "" {
		// Open-ended: bytes=S-
		newStart := offset + start
		newEnd := offset + fileSize - 1
		return fmt.Sprintf("bytes=%d-%d", newStart, newEnd)
	}

	end, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil {
		return rangeHeader
	}

	// Clamp end to file boundary
	if end >= fileSize {
		end = fileSize - 1
	}

	return fmt.Sprintf("bytes=%d-%d", offset+start, offset+end)
}

// isRARPacked returns true when a torrent has more selected files than links,
// indicating the files are packed inside a single archive (RAR).
func isRARPacked(info *TorrentInfo) bool {
	if info == nil || len(info.Links) == 0 {
		return false
	}

	selected := 0
	for _, f := range info.Files {
		if f.Selected == 1 {
			selected++
		}
	}

	return selected > len(info.Links) && len(info.Links) > 0
}

// rewriteContentRangeForRAR translates a CDN Content-Range header from RAR-absolute
// coordinates back to client-relative coordinates.
// Input:  "bytes 281-1280/10569452703" (RAR-absolute)
// Output: "bytes 0-999/3172121762" (client-relative)
func rewriteContentRangeForRAR(contentRange string, offset, fileSize int64) string {
	if !strings.HasPrefix(contentRange, "bytes ") {
		return contentRange
	}

	rest := strings.TrimPrefix(contentRange, "bytes ")
	slashIdx := strings.Index(rest, "/")
	if slashIdx < 0 {
		return contentRange
	}

	rangePart := rest[:slashIdx]
	dashIdx := strings.Index(rangePart, "-")
	if dashIdx < 0 {
		return contentRange
	}

	start, err1 := strconv.ParseInt(rangePart[:dashIdx], 10, 64)
	end, err2 := strconv.ParseInt(rangePart[dashIdx+1:], 10, 64)
	if err1 != nil || err2 != nil {
		return contentRange
	}

	clientStart := start - offset
	clientEnd := end - offset

	return fmt.Sprintf("bytes %d-%d/%d", clientStart, clientEnd, fileSize)
}
