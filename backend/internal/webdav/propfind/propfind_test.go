package propfind

import (
	"context"
	"encoding/xml"
	"net/http"
	"os"
	"testing"
	"time"

	"golang.org/x/net/webdav"
)

// mockFileInfo implements os.FileInfo for testing
type mockFileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
	isDir   bool
}

func (m *mockFileInfo) Name() string       { return m.name }
func (m *mockFileInfo) Size() int64        { return m.size }
func (m *mockFileInfo) Mode() os.FileMode  { return m.mode }
func (m *mockFileInfo) ModTime() time.Time { return m.modTime }
func (m *mockFileInfo) IsDir() bool        { return m.isDir }
func (m *mockFileInfo) Sys() interface{}   { return nil }

func TestParseDepth(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"0", 0},
		{"1", 1},
		{"infinity", infiniteDepth},
		{"", invalidDepth},
		{"2", invalidDepth},
		{"invalid", invalidDepth},
		{"Infinity", invalidDepth}, // Case sensitive
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseDepth(tt.input)
			if got != tt.want {
				t.Errorf("parseDepth(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestStripPrefix(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		prefix     string
		wantPath   string
		wantStatus int
		wantErr    bool
	}{
		{
			name:       "empty prefix",
			path:       "/webdav/file.txt",
			prefix:     "",
			wantPath:   "/webdav/file.txt",
			wantStatus: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "matching prefix",
			path:       "/webdav/file.txt",
			prefix:     "/webdav",
			wantPath:   "/file.txt",
			wantStatus: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "non-matching prefix",
			path:       "/other/file.txt",
			prefix:     "/webdav",
			wantPath:   "/other/file.txt",
			wantStatus: http.StatusNotFound,
			wantErr:    true,
		},
		{
			name:       "root path with prefix",
			path:       "/webdav/",
			prefix:     "/webdav/",
			wantPath:   "",
			wantStatus: http.StatusOK,
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPath, gotStatus, err := stripPrefix(tt.path, tt.prefix)
			if (err != nil) != tt.wantErr {
				t.Errorf("stripPrefix() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotPath != tt.wantPath {
				t.Errorf("stripPrefix() path = %q, want %q", gotPath, tt.wantPath)
			}
			if gotStatus != tt.wantStatus {
				t.Errorf("stripPrefix() status = %d, want %d", gotStatus, tt.wantStatus)
			}
		})
	}
}

func TestSlashClean(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "/"},
		{"/", "/"},
		{"foo", "/foo"},
		{"/foo", "/foo"},
		{"/foo/", "/foo"},
		{"/foo/bar", "/foo/bar"},
		{"/foo//bar", "/foo/bar"},
		{"/foo/../bar", "/bar"},
		{"foo/bar", "/foo/bar"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := slashClean(tt.input)
			if got != tt.want {
				t.Errorf("slashClean(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestEscapeXML(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"hello world", "hello world"},
		{"file_name", "file_name"},
		{"file-name", "file-name"},
		{"file.txt", "file.txt"},
		{"test123", "test123"},
		{"<script>", "&lt;script&gt;"},
		{"a&b", "a&amp;b"},
		{"\"quoted\"", "&#34;quoted&#34;"},
		{"file\twith\ttabs", "file&#x9;with&#x9;tabs"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := escapeXML(tt.input)
			if got != tt.want {
				t.Errorf("escapeXML(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFindResourceType(t *testing.T) {
	tests := []struct {
		name  string
		isDir bool
		want  string
	}{
		{
			name:  "directory",
			isDir: true,
			want:  `<D:collection xmlns:D="DAV:"/>`,
		},
		{
			name:  "file",
			isDir: false,
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fi := &mockFileInfo{isDir: tt.isDir}
			got, err := findResourceType(context.Background(), "/test", fi)
			if err != nil {
				t.Errorf("findResourceType() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("findResourceType() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFindDisplayName(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		fileName string
		want     string
	}{
		{
			name:     "root path",
			path:     "/",
			fileName: "root",
			want:     "",
		},
		{
			name:     "regular file",
			path:     "/test.txt",
			fileName: "test.txt",
			want:     "test.txt",
		},
		{
			name:     "file with special chars",
			path:     "/test<file>.txt",
			fileName: "test<file>.txt",
			want:     "test&lt;file&gt;.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fi := &mockFileInfo{name: tt.fileName}
			got, err := findDisplayName(context.Background(), tt.path, fi)
			if err != nil {
				t.Errorf("findDisplayName() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("findDisplayName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFindContentLength(t *testing.T) {
	tests := []struct {
		name string
		size int64
		want string
	}{
		{"zero", 0, "0"},
		{"small", 1024, "1024"},
		{"large", 1073741824, "1073741824"}, // 1GB
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fi := &mockFileInfo{size: tt.size}
			got, err := findContentLength(context.Background(), "/test", fi)
			if err != nil {
				t.Errorf("findContentLength() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("findContentLength() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFindLastModified(t *testing.T) {
	testTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	fi := &mockFileInfo{modTime: testTime}

	got, err := findLastModified(context.Background(), "/test", fi)
	if err != nil {
		t.Errorf("findLastModified() error = %v", err)
		return
	}

	want := "Mon, 15 Jan 2024 10:30:00 GMT"
	if got != want {
		t.Errorf("findLastModified() = %q, want %q", got, want)
	}
}

func TestFindContentType(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"txt file", "/test.txt", "text/plain; charset=utf-8"},
		{"html file", "/test.html", "text/html; charset=utf-8"},
		{"json file", "/test.json", "application/json"},
		{"mkv file", "/test.mkv", "video/x-matroska"},
		{"unknown extension", "/test.qqq", "application/octet-stream"},
		{"no extension", "/testfile", "application/octet-stream"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fi := &mockFileInfo{}
			got, err := findContentType(context.Background(), tt.path, fi)
			if err != nil {
				t.Errorf("findContentType() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("findContentType() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFindETag(t *testing.T) {
	testTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	fi := &mockFileInfo{
		modTime: testTime,
		size:    1024,
	}

	got, err := findETag(context.Background(), "/test", fi)
	if err != nil {
		t.Errorf("findETag() error = %v", err)
		return
	}

	// ETag should be in format `"%x%x"` where first is nanoseconds, second is size
	if got == "" {
		t.Error("findETag() returned empty string")
	}
	if got[0] != '"' || got[len(got)-1] != '"' {
		t.Errorf("findETag() = %q, should be quoted", got)
	}
}

func TestFindSupportedLock(t *testing.T) {
	fi := &mockFileInfo{}

	got, err := findSupportedLock(context.Background(), "/test", fi)
	if err != nil {
		t.Errorf("findSupportedLock() error = %v", err)
		return
	}

	expected := `<D:lockentry xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype></D:lockentry>`
	if got != expected {
		t.Errorf("findSupportedLock() = %q, want %q", got, expected)
	}
}

func TestFindFilesystemId(t *testing.T) {
	fi := &mockFileInfo{}

	got, err := findFilesystemId(context.Background(), "/test", fi)
	if err != nil {
		t.Errorf("findFilesystemId() error = %v", err)
		return
	}

	if got != "altmount-nzbfs-v1" {
		t.Errorf("findFilesystemId() = %q, want %q", got, "altmount-nzbfs-v1")
	}
}

func TestPropnames(t *testing.T) {
	tests := []struct {
		name  string
		isDir bool
	}{
		{"directory", true},
		{"file", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fi := &mockFileInfo{isDir: tt.isDir}
			pnames, err := propnames(fi)
			if err != nil {
				t.Errorf("propnames() error = %v", err)
				return
			}
			if len(pnames) == 0 {
				t.Error("propnames() returned empty slice")
			}
		})
	}
}

func TestMakePropstats(t *testing.T) {
	tests := []struct {
		name  string
		x     webdav.Propstat
		y     webdav.Propstat
		wantN int
	}{
		{
			name:  "both empty",
			x:     webdav.Propstat{},
			y:     webdav.Propstat{},
			wantN: 1, // Returns default 200 OK
		},
		{
			name: "x has props",
			x: webdav.Propstat{
				Status: http.StatusOK,
				Props:  []webdav.Property{{XMLName: xml.Name{Local: "test"}}},
			},
			y:     webdav.Propstat{},
			wantN: 1,
		},
		{
			name: "y has props",
			x:    webdav.Propstat{},
			y: webdav.Propstat{
				Status: http.StatusNotFound,
				Props:  []webdav.Property{{XMLName: xml.Name{Local: "test"}}},
			},
			wantN: 1,
		},
		{
			name: "both have props",
			x: webdav.Propstat{
				Status: http.StatusOK,
				Props:  []webdav.Property{{XMLName: xml.Name{Local: "found"}}},
			},
			y: webdav.Propstat{
				Status: http.StatusNotFound,
				Props:  []webdav.Property{{XMLName: xml.Name{Local: "notfound"}}},
			},
			wantN: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := makePropstats(tt.x, tt.y)
			if len(got) != tt.wantN {
				t.Errorf("makePropstats() returned %d propstats, want %d", len(got), tt.wantN)
			}
		})
	}
}

func TestProps(t *testing.T) {
	fi := &mockFileInfo{
		name:    "test.txt",
		size:    1024,
		modTime: time.Now(),
		isDir:   false,
	}

	// Test requesting known property
	pnames := []xml.Name{
		{Space: "DAV:", Local: "getcontentlength"},
	}

	pstats, err := props(context.Background(), fi, "/test.txt", pnames)
	if err != nil {
		t.Fatalf("props() error = %v", err)
	}

	if len(pstats) == 0 {
		t.Fatal("props() returned empty slice")
	}
}

func TestAllprop(t *testing.T) {
	fi := &mockFileInfo{
		name:    "test.txt",
		size:    1024,
		modTime: time.Now(),
		isDir:   false,
	}

	pstats, err := allprop(context.Background(), fi, "/test.txt", nil)
	if err != nil {
		t.Fatalf("allprop() error = %v", err)
	}

	if len(pstats) == 0 {
		t.Fatal("allprop() returned empty slice")
	}

	// Should have at least one propstat with OK status
	foundOK := false
	for _, pstat := range pstats {
		if pstat.Status == http.StatusOK {
			foundOK = true
			break
		}
	}
	if !foundOK {
		t.Error("allprop() did not return any OK propstat")
	}
}

func TestAllpropWithInclude(t *testing.T) {
	fi := &mockFileInfo{
		name:    "test.txt",
		size:    1024,
		modTime: time.Now(),
		isDir:   false,
	}

	include := []xml.Name{
		{Space: "custom:", Local: "customprop"},
	}

	pstats, err := allprop(context.Background(), fi, "/test.txt", include)
	if err != nil {
		t.Fatalf("allprop() error = %v", err)
	}

	if len(pstats) == 0 {
		t.Fatal("allprop() returned empty slice")
	}
}
