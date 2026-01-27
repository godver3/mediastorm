package webdav

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"novastream/internal/nzbfilesystem"

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

// mockWebDAVFile implements webdav.File for testing
type mockWebDAVFile struct {
	readErr  error
	readData []byte
	readN    int
}

func (m *mockWebDAVFile) Close() error                             { return nil }
func (m *mockWebDAVFile) Read(p []byte) (int, error)               { return m.readN, m.readErr }
func (m *mockWebDAVFile) Write(p []byte) (int, error)              { return len(p), nil }
func (m *mockWebDAVFile) Seek(offset int64, whence int) (int64, error) { return 0, nil }
func (m *mockWebDAVFile) Readdir(count int) ([]os.FileInfo, error) { return nil, nil }
func (m *mockWebDAVFile) Stat() (os.FileInfo, error)               { return &mockFileInfo{}, nil }

// mockWebDAVFileSystem implements webdav.FileSystem for testing
type mockWebDAVFileSystem struct {
	mkdirErr     error
	removeAllErr error
	renameErr    error
	statInfo     os.FileInfo
	statErr      error
	openFile     webdav.File
	openFileErr  error
}

func (m *mockWebDAVFileSystem) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	return m.mkdirErr
}

func (m *mockWebDAVFileSystem) RemoveAll(ctx context.Context, name string) error {
	return m.removeAllErr
}

func (m *mockWebDAVFileSystem) Rename(ctx context.Context, oldName, newName string) error {
	return m.renameErr
}

func (m *mockWebDAVFileSystem) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	return m.statInfo, m.statErr
}

func (m *mockWebDAVFileSystem) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	return m.openFile, m.openFileErr
}

func TestHTTPError_Error(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		message    string
		want       string
	}{
		{
			name:       "not found",
			statusCode: 404,
			message:    "File not found",
			want:       "HTTP 404: File not found",
		},
		{
			name:       "partial content",
			statusCode: 206,
			message:    "Partial content available",
			want:       "HTTP 206: Partial content available",
		},
		{
			name:       "service unavailable",
			statusCode: 503,
			message:    "Service unavailable",
			want:       "HTTP 503: Service unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &HTTPError{
				StatusCode: tt.statusCode,
				Message:    tt.message,
			}
			if got := err.Error(); got != tt.want {
				t.Errorf("HTTPError.Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHTTPError_Unwrap(t *testing.T) {
	innerErr := errors.New("inner error")
	httpErr := &HTTPError{
		StatusCode: 500,
		Message:    "server error",
		Err:        innerErr,
	}

	if unwrapped := httpErr.Unwrap(); unwrapped != innerErr {
		t.Errorf("HTTPError.Unwrap() = %v, want %v", unwrapped, innerErr)
	}
}

func TestCustomErrorHandler_Mkdir(t *testing.T) {
	mockFS := &mockWebDAVFileSystem{}
	handler := &customErrorHandler{fileSystem: mockFS}

	err := handler.Mkdir(context.Background(), "/test", 0755)
	if err != nil {
		t.Errorf("Mkdir() error = %v", err)
	}

	// Test with error
	mockFS.mkdirErr = errors.New("mkdir failed")
	err = handler.Mkdir(context.Background(), "/test", 0755)
	if err == nil {
		t.Error("Mkdir() expected error")
	}
}

func TestCustomErrorHandler_RemoveAll(t *testing.T) {
	mockFS := &mockWebDAVFileSystem{}
	handler := &customErrorHandler{fileSystem: mockFS}

	err := handler.RemoveAll(context.Background(), "/test")
	if err != nil {
		t.Errorf("RemoveAll() error = %v", err)
	}

	// Test with error
	mockFS.removeAllErr = errors.New("remove failed")
	err = handler.RemoveAll(context.Background(), "/test")
	if err == nil {
		t.Error("RemoveAll() expected error")
	}
}

func TestCustomErrorHandler_Rename(t *testing.T) {
	mockFS := &mockWebDAVFileSystem{}
	handler := &customErrorHandler{fileSystem: mockFS}

	err := handler.Rename(context.Background(), "/old", "/new")
	if err != nil {
		t.Errorf("Rename() error = %v", err)
	}

	// Test with error
	mockFS.renameErr = errors.New("rename failed")
	err = handler.Rename(context.Background(), "/old", "/new")
	if err == nil {
		t.Error("Rename() expected error")
	}
}

func TestCustomErrorHandler_Stat(t *testing.T) {
	mockInfo := &mockFileInfo{name: "test.txt", size: 100}
	mockFS := &mockWebDAVFileSystem{statInfo: mockInfo}
	handler := &customErrorHandler{fileSystem: mockFS}

	info, err := handler.Stat(context.Background(), "/test.txt")
	if err != nil {
		t.Errorf("Stat() error = %v", err)
	}
	if info.Name() != "test.txt" {
		t.Errorf("Stat() name = %q, want %q", info.Name(), "test.txt")
	}

	// Test with error
	mockFS.statErr = os.ErrNotExist
	_, err = handler.Stat(context.Background(), "/notfound")
	if err == nil {
		t.Error("Stat() expected error")
	}
}

func TestCustomErrorHandler_OpenFile(t *testing.T) {
	mockFile := &mockWebDAVFile{}
	mockFS := &mockWebDAVFileSystem{openFile: mockFile}
	handler := &customErrorHandler{fileSystem: mockFS}

	file, err := handler.OpenFile(context.Background(), "/test.txt", os.O_RDONLY, 0644)
	if err != nil {
		t.Errorf("OpenFile() error = %v", err)
	}
	if file == nil {
		t.Error("OpenFile() returned nil file")
	}
}

func TestCustomErrorHandler_mapError_PartialContent(t *testing.T) {
	mockFS := &mockWebDAVFileSystem{
		openFileErr: &nzbfilesystem.PartialContentError{
			BytesRead:     500,
			TotalExpected: 1000,
		},
	}
	handler := &customErrorHandler{fileSystem: mockFS}

	_, err := handler.OpenFile(context.Background(), "/test.txt", os.O_RDONLY, 0644)
	if err == nil {
		t.Fatal("expected error")
	}

	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTPError, got %T", err)
	}
	if httpErr.StatusCode != 206 {
		t.Errorf("StatusCode = %d, want 206", httpErr.StatusCode)
	}
}

func TestCustomErrorHandler_mapError_CorruptedFile(t *testing.T) {
	mockFS := &mockWebDAVFileSystem{
		openFileErr: &nzbfilesystem.CorruptedFileError{
			TotalExpected: 1000,
		},
	}
	handler := &customErrorHandler{fileSystem: mockFS}

	_, err := handler.OpenFile(context.Background(), "/test.txt", os.O_RDONLY, 0644)
	if err == nil {
		t.Fatal("expected error")
	}

	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTPError, got %T", err)
	}
	if httpErr.StatusCode != 404 {
		t.Errorf("StatusCode = %d, want 404", httpErr.StatusCode)
	}
}

func TestCustomErrorHandler_mapError_ErrFileIsCorrupted(t *testing.T) {
	mockFS := &mockWebDAVFileSystem{
		openFileErr: nzbfilesystem.ErrFileIsCorrupted,
	}
	handler := &customErrorHandler{fileSystem: mockFS}

	_, err := handler.OpenFile(context.Background(), "/test.txt", os.O_RDONLY, 0644)
	if err == nil {
		t.Fatal("expected error")
	}

	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTPError, got %T", err)
	}
	if httpErr.StatusCode != 404 {
		t.Errorf("StatusCode = %d, want 404", httpErr.StatusCode)
	}
}

func TestCustomErrorHandler_mapError_RegularError(t *testing.T) {
	regularErr := errors.New("some other error")
	mockFS := &mockWebDAVFileSystem{
		openFileErr: regularErr,
	}
	handler := &customErrorHandler{fileSystem: mockFS}

	_, err := handler.OpenFile(context.Background(), "/test.txt", os.O_RDONLY, 0644)
	if err == nil {
		t.Fatal("expected error")
	}
	if err != regularErr {
		t.Errorf("expected original error to be returned, got %v", err)
	}
}

func TestErrorHandlingFile_Read_Success(t *testing.T) {
	data := []byte("test data")
	mockFile := &mockWebDAVFile{
		readN:   len(data),
		readErr: nil,
	}

	file := &errorHandlingFile{
		File: mockFile,
		ctx:  context.Background(),
	}

	buf := make([]byte, 100)
	n, err := file.Read(buf)
	if err != nil {
		t.Errorf("Read() error = %v", err)
	}
	if n != len(data) {
		t.Errorf("Read() n = %d, want %d", n, len(data))
	}
}

func TestErrorHandlingFile_Read_EOF(t *testing.T) {
	mockFile := &mockWebDAVFile{
		readN:   0,
		readErr: io.EOF,
	}

	file := &errorHandlingFile{
		File: mockFile,
		ctx:  context.Background(),
	}

	buf := make([]byte, 100)
	_, err := file.Read(buf)
	if err != io.EOF {
		t.Errorf("Read() error = %v, want io.EOF", err)
	}
}

func TestErrorHandlingFile_Read_PartialContent(t *testing.T) {
	mockFile := &mockWebDAVFile{
		readN: 500,
		readErr: &nzbfilesystem.PartialContentError{
			BytesRead:     500,
			TotalExpected: 1000,
		},
	}

	file := &errorHandlingFile{
		File: mockFile,
		ctx:  context.Background(),
	}

	buf := make([]byte, 1000)
	n, err := file.Read(buf)

	if n != 500 {
		t.Errorf("Read() n = %d, want 500", n)
	}

	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTPError, got %T", err)
	}
	if httpErr.StatusCode != 206 {
		t.Errorf("StatusCode = %d, want 206", httpErr.StatusCode)
	}
}

func TestErrorHandlingFile_Read_CorruptedFile(t *testing.T) {
	mockFile := &mockWebDAVFile{
		readN: 0,
		readErr: &nzbfilesystem.CorruptedFileError{
			TotalExpected: 1000,
		},
	}

	file := &errorHandlingFile{
		File: mockFile,
		ctx:  context.Background(),
	}

	buf := make([]byte, 1000)
	_, err := file.Read(buf)

	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTPError, got %T", err)
	}
	if httpErr.StatusCode != 503 {
		t.Errorf("StatusCode = %d, want 503", httpErr.StatusCode)
	}
}

func TestErrorHandlingFile_Read_RegularError(t *testing.T) {
	regularErr := errors.New("read failed")
	mockFile := &mockWebDAVFile{
		readN:   0,
		readErr: regularErr,
	}

	file := &errorHandlingFile{
		File: mockFile,
		ctx:  context.Background(),
	}

	buf := make([]byte, 100)
	_, err := file.Read(buf)
	if err != regularErr {
		t.Errorf("Read() error = %v, want %v", err, regularErr)
	}
}
