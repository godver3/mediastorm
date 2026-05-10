package localmedia

import (
	"context"
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

const streamPathPrefix = "localmedia:"

func BuildStreamPath(item models.LocalMediaItem) string {
	filename := strings.TrimSpace(item.FileName)
	if filename == "" {
		filename = filepath.Base(strings.TrimSpace(item.FilePath))
	}
	if filename == "" {
		filename = "stream.bin"
	}
	return streamPathPrefix + strings.TrimSpace(item.ID) + "/" + filename
}

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

type Provider struct {
	service *Service
}

func NewProvider(service *Service) *Provider {
	return &Provider{service: service}
}

func (p *Provider) GetDirectURL(ctx context.Context, path string) (string, error) {
	if p == nil || p.service == nil {
		return "", streaming.ErrNotFound
	}

	itemID, ok := ParseStreamPath(path)
	if !ok {
		return "", streaming.ErrNotFound
	}

	item, err := p.service.GetItem(ctx, itemID)
	if err != nil {
		if err == ErrItemNotFound || err == ErrLibraryNotFound {
			return "", streaming.ErrNotFound
		}
		return "", err
	}
	if item == nil {
		return "", streaming.ErrNotFound
	}

	filePath := strings.TrimSpace(item.FilePath)
	if filePath == "" {
		return "", streaming.ErrNotFound
	}
	return filePath, nil
}

func (p *Provider) Stream(ctx context.Context, req streaming.Request) (*streaming.Response, error) {
	if p == nil || p.service == nil {
		return nil, streaming.ErrNotFound
	}

	itemID, ok := ParseStreamPath(req.Path)
	if !ok {
		return nil, streaming.ErrNotFound
	}

	item, err := p.service.GetItem(ctx, itemID)
	if err != nil {
		if err == ErrItemNotFound || err == ErrLibraryNotFound {
			return nil, streaming.ErrNotFound
		}
		return nil, err
	}
	if item == nil {
		return nil, streaming.ErrNotFound
	}

	filePath := strings.TrimSpace(item.FilePath)
	if filePath == "" {
		return nil, streaming.ErrNotFound
	}

	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, streaming.ErrNotFound
		}
		return nil, fmt.Errorf("open local media file: %w", err)
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat local media file: %w", err)
	}
	if info.IsDir() {
		_ = file.Close()
		return nil, streaming.ErrNotFound
	}

	size := info.Size()
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(item.FileName)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	headers := make(http.Header)
	headers.Set("Accept-Ranges", "bytes")
	headers.Set("Content-Type", contentType)
	if item.FileName != "" {
		headers.Set("Content-Disposition", inlineContentDisposition(item.FileName))
	}

	if strings.EqualFold(strings.TrimSpace(req.Method), http.MethodHead) {
		headers.Set("Content-Length", strconv.FormatInt(size, 10))
		_ = file.Close()
		return &streaming.Response{
			Headers:       headers,
			Status:        http.StatusOK,
			ContentLength: size,
			Filename:      item.FileName,
		}, nil
	}

	if req.RangeHeader == "" {
		headers.Set("Content-Length", strconv.FormatInt(size, 10))
		return &streaming.Response{
			Body:          file,
			Headers:       headers,
			Status:        http.StatusOK,
			ContentLength: size,
			Filename:      item.FileName,
		}, nil
	}

	start, end, err := parseRangeHeader(req.RangeHeader, size)
	if err != nil {
		headers.Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		_ = file.Close()
		return &streaming.Response{
			Headers:       headers,
			Status:        http.StatusRequestedRangeNotSatisfiable,
			ContentLength: 0,
			Filename:      item.FileName,
		}, nil
	}

	length := end - start + 1
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("seek local media file: %w", err)
	}

	headers.Set("Content-Length", strconv.FormatInt(length, 10))
	headers.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
	return &streaming.Response{
		Body: struct {
			io.Reader
			io.Closer
		}{
			Reader: io.LimitReader(file, length),
			Closer: file,
		},
		Headers:       headers,
		Status:        http.StatusPartialContent,
		ContentLength: length,
		Filename:      item.FileName,
	}, nil
}

func parseRangeHeader(header string, size int64) (int64, int64, error) {
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

func inlineContentDisposition(filename string) string {
	safe := strings.ReplaceAll(filename, `"`, "")
	return fmt.Sprintf(`inline; filename="%s"`, safe)
}
