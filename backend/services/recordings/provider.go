package recordings

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"novastream/models"
	"novastream/services/streaming"
)

const streamPathPrefix = "recording:"

// BuildStreamPath returns the streaming-provider path used to play a recording
// through the shared video/HLS pipeline (e.g. "recording:<id>/<filename>").
// The filename component is informational only; resolution keys off the ID.
func BuildStreamPath(recording models.Recording) string {
	filename := filepath.Base(strings.TrimSpace(recording.OutputPath))
	if filename == "" || filename == "." {
		filename = "recording.ts"
	}
	return streamPathPrefix + strings.TrimSpace(recording.ID) + "/" + filename
}

// ParseStreamPath extracts the recording ID from a streaming path, returning
// false when the path is not a recording path.
func ParseStreamPath(path string) (string, bool) {
	raw := strings.TrimSpace(path)
	if !strings.HasPrefix(raw, streamPathPrefix) {
		return "", false
	}
	raw = strings.TrimPrefix(raw, streamPathPrefix)
	if slash := strings.IndexByte(raw, '/'); slash >= 0 {
		raw = raw[:slash]
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	return raw, true
}

// StreamProvider adapts the recordings service to the streaming.Provider /
// streaming.DirectURLProvider interfaces so completed (and in-progress)
// recordings can be transmuxed through the existing HLS pipeline.
type StreamProvider struct {
	service *Service
}

// NewStreamProvider wires the recordings service into the streaming layer.
func NewStreamProvider(service *Service) *StreamProvider {
	return &StreamProvider{service: service}
}

// resolveFile looks up the on-disk output file for a recording path.
func (p *StreamProvider) resolveFile(path string) (string, string, error) {
	if p == nil || p.service == nil {
		return "", "", streaming.ErrNotFound
	}
	id, ok := ParseStreamPath(path)
	if !ok {
		return "", "", streaming.ErrNotFound
	}
	recording, err := p.service.Get(id)
	if err != nil {
		if errors.Is(err, ErrRecordingNotFound) {
			return "", "", streaming.ErrNotFound
		}
		return "", "", err
	}
	filePath := strings.TrimSpace(recording.OutputPath)
	if filePath == "" {
		return "", "", streaming.ErrNotFound
	}
	return filePath, filepath.Base(filePath), nil
}

// GetDirectURL returns the local filesystem path of the recording so FFmpeg can
// read it directly as an input (mirrors the local media provider behaviour).
func (p *StreamProvider) GetDirectURL(_ context.Context, path string) (string, error) {
	filePath, _, err := p.resolveFile(path)
	if err != nil {
		return "", err
	}
	return filePath, nil
}

// Stream serves the recording file with HTTP range support as a fallback for
// direct playback.
func (p *StreamProvider) Stream(_ context.Context, req streaming.Request) (*streaming.Response, error) {
	filePath, fileName, err := p.resolveFile(req.Path)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, streaming.ErrNotFound
		}
		return nil, fmt.Errorf("open recording file: %w", err)
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat recording file: %w", err)
	}
	if info.IsDir() {
		_ = file.Close()
		return nil, streaming.ErrNotFound
	}

	size := info.Size()
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(fileName)))
	if contentType == "" {
		contentType = "video/mp2t"
	}

	headers := make(http.Header)
	headers.Set("Accept-Ranges", "bytes")
	headers.Set("Content-Type", contentType)
	if fileName != "" {
		headers.Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, strings.ReplaceAll(fileName, `"`, "")))
	}

	if strings.EqualFold(strings.TrimSpace(req.Method), http.MethodHead) {
		headers.Set("Content-Length", strconv.FormatInt(size, 10))
		_ = file.Close()
		return &streaming.Response{Headers: headers, Status: http.StatusOK, ContentLength: size, Filename: fileName}, nil
	}

	if req.RangeHeader == "" {
		headers.Set("Content-Length", strconv.FormatInt(size, 10))
		return &streaming.Response{Body: file, Headers: headers, Status: http.StatusOK, ContentLength: size, Filename: fileName}, nil
	}

	start, end, err := parseRecordingRange(req.RangeHeader, size)
	if err != nil {
		headers.Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		_ = file.Close()
		return &streaming.Response{Headers: headers, Status: http.StatusRequestedRangeNotSatisfiable, Filename: fileName}, nil
	}

	length := end - start + 1
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("seek recording file: %w", err)
	}

	headers.Set("Content-Length", strconv.FormatInt(length, 10))
	headers.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
	return &streaming.Response{
		Body: struct {
			io.Reader
			io.Closer
		}{Reader: io.LimitReader(file, length), Closer: file},
		Headers:       headers,
		Status:        http.StatusPartialContent,
		ContentLength: length,
		Filename:      fileName,
	}, nil
}

func parseRecordingRange(header string, size int64) (int64, int64, error) {
	if size <= 0 {
		return 0, 0, fmt.Errorf("invalid size")
	}
	value := strings.TrimSpace(header)
	if !strings.HasPrefix(strings.ToLower(value), "bytes=") {
		return 0, 0, fmt.Errorf("unsupported range unit")
	}
	spec := strings.TrimSpace(value[6:])
	if spec == "" || strings.Contains(spec, ",") {
		return 0, 0, fmt.Errorf("invalid range")
	}
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid range")
	}
	startStr := strings.TrimSpace(parts[0])
	endStr := strings.TrimSpace(parts[1])
	switch {
	case startStr == "":
		suffixLen, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil || suffixLen <= 0 {
			return 0, 0, fmt.Errorf("invalid suffix range")
		}
		if suffixLen > size {
			suffixLen = size
		}
		return size - suffixLen, size - 1, nil
	case endStr == "":
		start, err := strconv.ParseInt(startStr, 10, 64)
		if err != nil || start < 0 || start >= size {
			return 0, 0, fmt.Errorf("invalid start range")
		}
		return start, size - 1, nil
	default:
		start, err := strconv.ParseInt(startStr, 10, 64)
		if err != nil || start < 0 {
			return 0, 0, fmt.Errorf("invalid start range")
		}
		end, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil || end < start {
			return 0, 0, fmt.Errorf("invalid end range")
		}
		if start >= size {
			return 0, 0, fmt.Errorf("range out of bounds")
		}
		if end >= size {
			end = size - 1
		}
		return start, end, nil
	}
}
