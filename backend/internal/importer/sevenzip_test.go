package importer

import (
	"testing"
)

func TestGet7zPartNumber(t *testing.T) {
	tests := []struct {
		filename string
		expected int
	}{
		{"movie.7z", 0},
		{"movie.7z.001", 1},
		{"movie.7z.002", 2},
		{"movie.7z.010", 10},
		{"movie.mkv", 999999}, // Unknown format
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			result := get7zPartNumber(tt.filename)
			if result != tt.expected {
				t.Errorf("get7zPartNumber(%q) = %d, expected %d", tt.filename, result, tt.expected)
			}
		})
	}
}

func TestExtractBase7zFilename(t *testing.T) {
	tests := []struct {
		filename string
		expected string
	}{
		{"movie.7z", "movie"},
		{"movie.7z.001", "movie"},
		{"movie.7z.002", "movie"},
		{"Movie.Name.2020.7z.003", "Movie.Name.2020"},
		{"movie.mkv", "movie"}, // Fallback
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			result := extractBase7zFilename(tt.filename)
			if result != tt.expected {
				t.Errorf("extractBase7zFilename(%q) = %q, expected %q", tt.filename, result, tt.expected)
			}
		})
	}
}

func TestGet7zPartSuffix(t *testing.T) {
	tests := []struct {
		filename string
		expected string
	}{
		{"movie.7z", ".7z"},
		{"movie.7z.001", ".7z.001"},
		{"movie.7z.010", ".7z.010"},
		{"movie.mkv", ".mkv"}, // Fallback to extension
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			result := get7zPartSuffix(tt.filename)
			if result != tt.expected {
				t.Errorf("get7zPartSuffix(%q) = %q, expected %q", tt.filename, result, tt.expected)
			}
		})
	}
}

func TestGetFirst7zPart(t *testing.T) {
	tests := []struct {
		name     string
		files    []string
		expected string
		wantErr  bool
	}{
		{
			name:     "single 7z file",
			files:    []string{"movie.7z"},
			expected: "movie.7z",
			wantErr:  false,
		},
		{
			name:     "multipart with .7z first",
			files:    []string{"movie.7z.001", "movie.7z.002", "movie.7z"},
			expected: "movie.7z",
			wantErr:  false,
		},
		{
			name:     "multipart only numbered parts",
			files:    []string{"movie.7z.003", "movie.7z.002", "movie.7z.001"},
			expected: "movie.7z.001",
			wantErr:  false,
		},
		{
			name:    "empty list",
			files:   []string{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := getFirst7zPart(tt.files)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("getFirst7zPart() = %q, expected %q", result, tt.expected)
			}
		})
	}
}

func TestRename7zFilesAndSort(t *testing.T) {
	// Create test files with different naming patterns
	files := []ParsedFile{
		{Filename: "Movie.Name.7z.003", Size: 100},
		{Filename: "Movie.Name.7z.001", Size: 100},
		{Filename: "Movie.Name.7z.002", Size: 100},
	}

	result := rename7zFilesAndSort(files)

	if len(result) != 3 {
		t.Fatalf("expected 3 files, got %d", len(result))
	}

	// Verify they are sorted by part number
	expectedOrder := []int{1, 2, 3}
	for i, f := range result {
		partNum := get7zPartNumber(f.Filename)
		if partNum != expectedOrder[i] {
			t.Errorf("file %d: expected part %d, got %d (filename: %s)", i, expectedOrder[i], partNum, f.Filename)
		}
	}
}

func TestGetCompressionMethodName(t *testing.T) {
	tests := []struct {
		methodID []byte
		expected string
	}{
		{[]byte{0x00}, "Copy (uncompressed)"},
		{[]byte{0x21}, "LZMA2"},
		{[]byte{0x03, 0x01, 0x01}, "LZMA"},
		{[]byte{0x04, 0xf7, 0x11, 0x01}, "Zstandard"},
		{[]byte{0x06, 0xf1, 0x07, 0x01}, "AES-256 (encrypted)"},
		{[]byte{0xff, 0xff}, "unknown"},
	}

	for _, tt := range tests {
		result := getCompressionMethodName(tt.methodID)
		if result != tt.expected {
			t.Errorf("getCompressionMethodName(%x) = %q, expected %q", tt.methodID, result, tt.expected)
		}
	}
}
