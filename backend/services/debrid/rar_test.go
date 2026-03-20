package debrid

import (
	"encoding/binary"
	"testing"
)

// buildRAR4Archive constructs a minimal RAR4 byte stream with STORED file entries.
func buildRAR4Archive(files []struct {
	name string
	size uint32
	data []byte
}) []byte {
	var buf []byte

	// RAR4 signature
	buf = append(buf, []byte("Rar!\x1a\x07\x00")...)

	// Archive header block (type 0x73)
	archHdr := make([]byte, 13)
	binary.LittleEndian.PutUint16(archHdr[0:2], 0x0000) // CRC (don't care for test)
	archHdr[2] = 0x73                                     // ARCHIVE_HEADER
	binary.LittleEndian.PutUint16(archHdr[3:5], 0x0000)  // flags
	binary.LittleEndian.PutUint16(archHdr[5:7], 13)       // header size
	// reserved bytes 7-12
	buf = append(buf, archHdr...)

	for _, f := range files {
		nameBytes := []byte(f.name)
		// File header: 7 (common) + 25 (fixed fields) + nameSize
		headerSize := 7 + 25 + len(nameBytes)

		hdr := make([]byte, headerSize)
		binary.LittleEndian.PutUint16(hdr[0:2], 0x0000)              // CRC
		hdr[2] = rar4HeaderFile                                       // type
		binary.LittleEndian.PutUint16(hdr[3:5], 0x0000)              // flags
		binary.LittleEndian.PutUint16(hdr[5:7], uint16(headerSize))  // header size
		binary.LittleEndian.PutUint32(hdr[7:11], f.size)             // PACK_SIZE (4)
		binary.LittleEndian.PutUint32(hdr[11:15], f.size)            // UNP_SIZE (4)
		hdr[15] = 0x00                                                // HOST_OS (1)
		binary.LittleEndian.PutUint32(hdr[16:20], 0x00000000)        // FILE_CRC (4)
		binary.LittleEndian.PutUint32(hdr[20:24], 0x00000000)        // FTIME (4)
		hdr[24] = 0x1D                                                // UNP_VER (1)
		hdr[25] = rar4MethodStore                                     // METHOD (1)
		binary.LittleEndian.PutUint16(hdr[26:28], uint16(len(nameBytes))) // NAME_SIZE (2)
		binary.LittleEndian.PutUint32(hdr[28:32], 0x00000000)        // ATTR (4)
		// Filename starts at offset 32
		copy(hdr[32:], nameBytes)

		buf = append(buf, hdr...)

		// Append file data
		if f.data != nil {
			buf = append(buf, f.data...)
		} else {
			buf = append(buf, make([]byte, f.size)...)
		}
	}

	return buf
}

func TestParseRAR4Headers(t *testing.T) {
	data := buildRAR4Archive([]struct {
		name string
		size uint32
		data []byte
	}{
		{name: "Episode.S01E01.mkv", size: 100},
		{name: "Episode.S01E02.mkv", size: 200},
	})

	entries, err := parseRAR4Headers(data) // includes signature, parseRAR4Headers skips it
	if err != nil {
		t.Fatalf("parseRAR4Headers: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	if entries[0].Name != "Episode.S01E01.mkv" {
		t.Errorf("entry 0 name = %q, want Episode.S01E01.mkv", entries[0].Name)
	}
	if entries[0].Size != 100 {
		t.Errorf("entry 0 size = %d, want 100", entries[0].Size)
	}

	if entries[1].Name != "Episode.S01E02.mkv" {
		t.Errorf("entry 1 name = %q, want Episode.S01E02.mkv", entries[1].Name)
	}
	if entries[1].Size != 200 {
		t.Errorf("entry 1 size = %d, want 200", entries[1].Size)
	}

	// Second entry's data offset should be after first entry's header + data
	if entries[1].DataOffset <= entries[0].DataOffset {
		t.Errorf("entry 1 offset (%d) should be after entry 0 offset (%d)", entries[1].DataOffset, entries[0].DataOffset)
	}
}

func TestFindRAREntry(t *testing.T) {
	entries := []rarFileEntry{
		{Name: "Show S01/Episode.S01E01.mkv", DataOffset: 100, Size: 1000},
		{Name: "Show S01/Episode.S01E02.mkv", DataOffset: 1200, Size: 2000},
	}

	tests := []struct {
		name       string
		targetPath string
		wantName   string
		wantNil    bool
	}{
		{"basename match", "Episode.S01E01.mkv", "Show S01/Episode.S01E01.mkv", false},
		{"leading slash", "/Episode.S01E02.mkv", "Show S01/Episode.S01E02.mkv", false},
		{"different dir prefix", "/other/dir/Episode.S01E01.mkv", "Show S01/Episode.S01E01.mkv", false},
		{"case insensitive", "episode.s01e02.MKV", "Show S01/Episode.S01E02.mkv", false},
		{"no match", "Episode.S01E03.mkv", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findRAREntry(entries, tt.targetPath)
			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil entry")
			}
			if got.Name != tt.wantName {
				t.Errorf("got name %q, want %q", got.Name, tt.wantName)
			}
		})
	}
}

func TestTranslateRangeForRAR(t *testing.T) {
	const offset int64 = 281
	const fileSize int64 = 3172121762

	tests := []struct {
		name   string
		input  string
		want   string
	}{
		{"standard range", "bytes=0-999", "bytes=281-1280"},
		{"open-ended", "bytes=0-", "bytes=281-3172122042"},
		{"mid-file range", "bytes=1000-1999", "bytes=1281-2280"},
		{"suffix range", "bytes=-500", "bytes=3172121543-3172122042"},
		{"clamp end past file", "bytes=0-9999999999999", "bytes=281-3172122042"},
		{"non-byte range", "items=0-10", "items=0-10"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := translateRangeForRAR(tt.input, offset, fileSize)
			if got != tt.want {
				t.Errorf("translateRangeForRAR(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsRARPacked(t *testing.T) {
	tests := []struct {
		name     string
		info     *TorrentInfo
		expected bool
	}{
		{
			"nil info",
			nil,
			false,
		},
		{
			"no links",
			&TorrentInfo{Files: []File{{ID: 1, Selected: 1}}, Links: nil},
			false,
		},
		{
			"normal torrent - equal files and links",
			&TorrentInfo{
				Files: []File{{ID: 1, Selected: 1}, {ID: 2, Selected: 1}},
				Links: []string{"link1", "link2"},
			},
			false,
		},
		{
			"RAR packed - more selected files than links",
			&TorrentInfo{
				Files: []File{{ID: 1, Selected: 1}, {ID: 2, Selected: 1}, {ID: 3, Selected: 1}},
				Links: []string{"link1"},
			},
			true,
		},
		{
			"unselected files don't count",
			&TorrentInfo{
				Files: []File{{ID: 1, Selected: 1}, {ID: 2, Selected: 0}, {ID: 3, Selected: 0}},
				Links: []string{"link1"},
			},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRARPacked(tt.info)
			if got != tt.expected {
				t.Errorf("isRARPacked() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestRewriteContentRangeForRAR(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"standard", "bytes 281-1280/10569452703", "bytes 0-999/3172121762"},
		{"non-bytes", "items 0-10/100", "items 0-10/100"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteContentRangeForRAR(tt.input, 281, 3172121762)
			if got != tt.want {
				t.Errorf("rewriteContentRangeForRAR(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestResolveRestrictedLinkRARPack(t *testing.T) {
	// RAR pack: 3 selected files but only 1 link
	info := &TorrentInfo{
		Files: []File{
			{ID: 1, Selected: 1, Path: "/Episode.S01E01.mkv"},
			{ID: 2, Selected: 1, Path: "/Episode.S01E02.mkv"},
			{ID: 3, Selected: 1, Path: "/Episode.S01E03.mkv"},
		},
		Links: []string{"https://rd.example.com/archive.rar"},
	}

	// File ID 2 should match and return the single link with correct filename
	link, filename, idx, matched := resolveRestrictedLink(info, "2")
	if !matched {
		t.Fatal("expected match for file id 2 in RAR pack")
	}
	if link != "https://rd.example.com/archive.rar" {
		t.Errorf("expected RAR link, got %s", link)
	}
	if filename != "/Episode.S01E02.mkv" {
		t.Errorf("expected /Episode.S01E02.mkv, got %s", filename)
	}
	if idx != 0 {
		t.Errorf("expected index 0, got %d", idx)
	}
}
