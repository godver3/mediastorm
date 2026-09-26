package integration

import (
	"context"
	"net/http"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/nzb/metadata"
	metapb "novastream/internal/nzb/metadata/proto"
	"novastream/internal/nzbfilesystem"
	"novastream/services/streaming"
)

func TestStorageStreamResumeHeaders(t *testing.T) {
	modified := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	service := metadata.NewMetadataService(t.TempDir())
	meta := service.CreateFileMetadata(1000, "source.nzb", metapb.FileStatus_FILE_STATUS_HEALTHY, nil, metapb.Encryption_NONE, "", "")
	meta.ModifiedAt = modified.Unix()
	if err := service.WriteFileMetadata("movies/test.mkv", meta); err != nil {
		t.Fatal(err)
	}
	remote := nzbfilesystem.NewMetadataRemoteFile(service, nil, nil, func() *config.AltMountConfig {
		return &config.AltMountConfig{}
	})
	fs := nzbfilesystem.NewNzbFilesystem(remote).(*nzbfilesystem.NzbFilesystem)
	system := &NzbSystem{fs: fs, nzbFs: fs}

	for _, tt := range []struct {
		name, method, rangeHeader, ifRange, contentRange string
		status                                           int
		length                                           int64
	}{
		{"head", http.MethodHead, "", "", "", http.StatusOK, 1000},
		{"full download", http.MethodGet, "", "", "", http.StatusOK, 1000},
		{"resume", http.MethodGet, "bytes=400-", modified.Format(http.TimeFormat), "bytes 400-999/1000", http.StatusPartialContent, 600},
		{"unconditional range", http.MethodGet, "bytes=400-", "", "bytes 400-999/1000", http.StatusPartialContent, 600},
		{"changed file", http.MethodGet, "bytes=400-", modified.Add(-time.Hour).Format(http.TimeFormat), "", http.StatusOK, 1000},
		{"unsupported validator", http.MethodGet, "bytes=400-", `"old-etag"`, "", http.StatusOK, 1000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response, err := system.Stream(context.Background(), streaming.Request{
				Path: "/webdav/movies/test.mkv", Method: tt.method, RangeHeader: tt.rangeHeader, IfRange: tt.ifRange,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer response.Close()
			if response.Status != tt.status || response.ContentLength != tt.length {
				t.Fatalf("status/length = %d/%d, want %d/%d", response.Status, response.ContentLength, tt.status, tt.length)
			}
			for key, want := range map[string]string{
				"Last-Modified": modified.Format(http.TimeFormat),
				"Accept-Ranges": "bytes",
				"Content-Range": tt.contentRange,
			} {
				if got := response.Headers.Get(key); got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
		})
	}
}
