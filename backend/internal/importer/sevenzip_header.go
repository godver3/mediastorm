package importer

import (
	"bytes"
	"fmt"
	"io"
	"reflect"

	"github.com/bodgit/sevenzip"
)

// sevenZipFileEntry represents a file entry within a 7z archive with byte offset info
type sevenZipFileEntry struct {
	Name             string
	UncompressedSize int64
	PackedOffset     int64 // Absolute byte offset in the archive where this file's data starts
	IsDirectory      bool
	FolderIndex      int
}

// sevenZipArchiveInfo contains parsed information about a 7z archive
type sevenZipArchiveInfo struct {
	Files            []sevenZipFileEntry
	IsUncompressed   bool   // true if all files use Copy (no compression)
	CompressionError string // populated if archive uses unsupported compression
}

// Err7zCompressed is returned when a 7z archive contains compressed files
var Err7zCompressed = NewNonRetryableError(
	"7z archive contains compressed files; only uncompressed (store mode) 7z archives are supported for streaming",
	nil,
)

// Err7zEncrypted is returned when a 7z archive is encrypted
var Err7zEncrypted = NewNonRetryableError(
	"encrypted 7z archives are not supported for streaming",
	nil,
)

// copyMethodID is the 7z method ID for uncompressed (Copy) data
var copyMethodID = []byte{0x00}

// parse7zHeaders parses a 7z archive and extracts file entries with byte offsets.
// This uses the bodgit/sevenzip library with reflection to access internal fields.
// Returns an error if the archive uses compression (only store/Copy mode is supported).
func parse7zHeaders(r io.ReaderAt, size int64) (*sevenZipArchiveInfo, error) {
	// Open the archive using bodgit/sevenzip
	reader, err := sevenzip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("failed to open 7z archive: %w", err)
	}

	info := &sevenZipArchiveInfo{
		Files:          make([]sevenZipFileEntry, 0, len(reader.File)),
		IsUncompressed: true,
	}

	// Use reflection to access internal fields
	readerVal := reflect.ValueOf(reader).Elem()

	// Get Reader.start (start of pack data)
	startField := readerVal.FieldByName("start")
	if !startField.IsValid() {
		return nil, fmt.Errorf("failed to access Reader.start field via reflection")
	}
	readerStart := startField.Int()

	// Get Reader.si (streamsInfo pointer)
	siField := readerVal.FieldByName("si")
	if !siField.IsValid() || siField.IsNil() {
		return nil, fmt.Errorf("failed to access Reader.si field via reflection")
	}

	// Check if the archive uses only Copy (uncompressed) method
	isUncompressed, compressionError := check7zCompressionMethod(siField)
	if compressionError != "" {
		info.IsUncompressed = false
		info.CompressionError = compressionError
		return info, Err7zCompressed
	}
	info.IsUncompressed = isUncompressed

	// Process each file
	for _, file := range reader.File {
		// Skip directories
		fileInfo := file.FileInfo()
		if fileInfo.IsDir() {
			continue
		}

		// Get internal fields from File struct using reflection
		fileVal := reflect.ValueOf(file).Elem()

		// Get File.folder (folder index)
		folderField := fileVal.FieldByName("folder")
		if !folderField.IsValid() {
			return nil, fmt.Errorf("failed to access File.folder field via reflection")
		}
		folderIndex := int(folderField.Int())

		// Get File.offset (offset within uncompressed stream of the folder)
		offsetField := fileVal.FieldByName("offset")
		if !offsetField.IsValid() {
			return nil, fmt.Errorf("failed to access File.offset field via reflection")
		}
		fileOffset := offsetField.Int()

		// Calculate the folder's starting offset in pack data
		folderOffset, err := get7zFolderOffset(siField, folderIndex)
		if err != nil {
			return nil, fmt.Errorf("failed to calculate folder offset: %w", err)
		}

		// For uncompressed archives, the absolute byte offset is:
		// Reader.start + folderOffset + file.offset
		absoluteOffset := readerStart + folderOffset + fileOffset

		entry := sevenZipFileEntry{
			Name:             file.Name,
			UncompressedSize: int64(file.UncompressedSize),
			PackedOffset:     absoluteOffset,
			IsDirectory:      false,
			FolderIndex:      folderIndex,
		}
		info.Files = append(info.Files, entry)
	}

	return info, nil
}

// check7zCompressionMethod checks if the archive uses only Copy (uncompressed) method.
// Returns (true, "") if uncompressed, (false, error_description) if compressed.
func check7zCompressionMethod(siField reflect.Value) (bool, string) {
	// streamsInfo -> unpackInfo -> folder[] -> coder[] -> id
	siVal := siField.Elem()

	unpackInfoField := siVal.FieldByName("unpackInfo")
	if !unpackInfoField.IsValid() || unpackInfoField.IsNil() {
		// No unpack info means no files or empty archive
		return true, ""
	}

	unpackInfoVal := unpackInfoField.Elem()
	foldersField := unpackInfoVal.FieldByName("folder")
	if !foldersField.IsValid() {
		return false, "failed to access folder field"
	}

	// Iterate through all folders
	for i := 0; i < foldersField.Len(); i++ {
		folderPtr := foldersField.Index(i)
		if folderPtr.IsNil() {
			continue
		}
		folderVal := folderPtr.Elem()

		codersField := folderVal.FieldByName("coder")
		if !codersField.IsValid() {
			return false, "failed to access coder field"
		}

		// Check each coder in the folder
		for j := 0; j < codersField.Len(); j++ {
			coderPtr := codersField.Index(j)
			if coderPtr.IsNil() {
				continue
			}
			coderVal := coderPtr.Elem()

			idField := coderVal.FieldByName("id")
			if !idField.IsValid() {
				return false, "failed to access coder.id field"
			}

			// Get the coder ID as []byte
			idBytes := idField.Bytes()
			if !bytes.Equal(idBytes, copyMethodID) {
				// Found a non-Copy method - archive is compressed
				methodName := getCompressionMethodName(idBytes)
				return false, fmt.Sprintf("archive uses %s compression (method ID: %x)", methodName, idBytes)
			}
		}
	}

	return true, ""
}

// get7zFolderOffset calculates the byte offset where a folder's pack data starts.
// This mirrors the streamsInfo.folderOffset() method from the library.
func get7zFolderOffset(siField reflect.Value, folderIndex int) (int64, error) {
	siVal := siField.Elem()

	// Get packInfo.position (starting position of pack data)
	packInfoField := siVal.FieldByName("packInfo")
	if !packInfoField.IsValid() || packInfoField.IsNil() {
		return 0, fmt.Errorf("packInfo is nil")
	}
	packInfoVal := packInfoField.Elem()

	positionField := packInfoVal.FieldByName("position")
	if !positionField.IsValid() {
		return 0, fmt.Errorf("failed to access packInfo.position")
	}
	packPosition := int64(positionField.Uint())

	// Get packInfo.size (slice of pack sizes)
	packSizeField := packInfoVal.FieldByName("size")
	if !packSizeField.IsValid() {
		return 0, fmt.Errorf("failed to access packInfo.size")
	}

	// Get unpackInfo.folder (to calculate packed streams per folder)
	unpackInfoField := siVal.FieldByName("unpackInfo")
	if !unpackInfoField.IsValid() || unpackInfoField.IsNil() {
		return 0, fmt.Errorf("unpackInfo is nil")
	}
	unpackInfoVal := unpackInfoField.Elem()
	foldersField := unpackInfoVal.FieldByName("folder")
	if !foldersField.IsValid() {
		return 0, fmt.Errorf("failed to access folder field")
	}

	// Calculate offset for the target folder
	// This mirrors the logic in streamsInfo.folderOffset()
	offset := uint64(0)
	packIndex := uint64(0)

	for i := 0; i < folderIndex; i++ {
		folderPtr := foldersField.Index(i)
		if folderPtr.IsNil() {
			continue
		}
		folderVal := folderPtr.Elem()

		packedStreamsField := folderVal.FieldByName("packedStreams")
		if !packedStreamsField.IsValid() {
			return 0, fmt.Errorf("failed to access packedStreams field")
		}
		packedStreams := packedStreamsField.Uint()

		// Sum up the sizes of packed streams for this folder
		for j := packIndex; j < packIndex+packedStreams; j++ {
			if int(j) >= packSizeField.Len() {
				break
			}
			offset += packSizeField.Index(int(j)).Uint()
		}
		packIndex += packedStreams
	}

	return packPosition + int64(offset), nil
}

// getCompressionMethodName returns a human-readable name for common 7z compression methods
func getCompressionMethodName(methodID []byte) string {
	switch {
	case bytes.Equal(methodID, []byte{0x00}):
		return "Copy (uncompressed)"
	case bytes.Equal(methodID, []byte{0x03}):
		return "Delta"
	case bytes.Equal(methodID, []byte{0x03, 0x01, 0x01}):
		return "LZMA"
	case bytes.Equal(methodID, []byte{0x21}):
		return "LZMA2"
	case bytes.Equal(methodID, []byte{0x04, 0x01, 0x08}):
		return "Deflate"
	case bytes.Equal(methodID, []byte{0x04, 0x02, 0x02}):
		return "BZip2"
	case bytes.Equal(methodID, []byte{0x04, 0xf7, 0x11, 0x01}):
		return "Zstandard"
	case bytes.Equal(methodID, []byte{0x04, 0xf7, 0x11, 0x02}):
		return "Brotli"
	case bytes.Equal(methodID, []byte{0x04, 0xf7, 0x11, 0x04}):
		return "LZ4"
	case bytes.Equal(methodID, []byte{0x06, 0xf1, 0x07, 0x01}):
		return "AES-256 (encrypted)"
	case bytes.HasPrefix(methodID, []byte{0x03, 0x03}):
		return "BCJ filter"
	default:
		return "unknown"
	}
}
