package importer

import (
	"context"
	"errors"
	"os"
	"testing"

	"novastream/internal/nzb/metadata"
	metapb "novastream/internal/nzb/metadata/proto"
)

func TestResolvedNZBCacheListFindAndDelete(t *testing.T) {
	ctx := context.Background()
	metaSvc := metadata.NewMetadataService(t.TempDir())
	cache := newResolvedNZBCache(metaSvc)

	virtualPath := "/movies/Movie.mkv"
	if err := metaSvc.WriteFileMetadata(virtualPath, &metapb.FileMetadata{
		FileSize:      1234,
		SourceNzbPath: "source.nzb",
		Status:        metapb.FileStatus_FILE_STATUS_HEALTHY,
	}); err != nil {
		t.Fatalf("WriteFileMetadata() error = %v", err)
	}

	const downloadURL = "https://indexer.example/release/1.nzb"
	if err := cache.put(ctx, resolvedNZBHash([]byte("nzb")), "release.nzb", virtualPath, ResolvedNZBSource{
		DownloadURL: downloadURL,
		Title:       "Movie Release",
		Indexer:     "Indexer",
		FileSize:    1234,
	}); err != nil {
		t.Fatalf("put() error = %v", err)
	}

	entry, ok, err := cache.findByDownloadURL(ctx, downloadURL)
	if err != nil {
		t.Fatalf("findByDownloadURL() error = %v", err)
	}
	if !ok || entry.StoragePath != virtualPath {
		t.Fatalf("findByDownloadURL() = (%+v, %t), want storage path %q", entry, ok, virtualPath)
	}

	list, err := cache.list(ctx, "movie", 1, 10)
	if err != nil {
		t.Fatalf("list() error = %v", err)
	}
	if list.Total != 1 || len(list.Items) != 1 {
		t.Fatalf("list() total/items = %d/%d, want 1/1", list.Total, len(list.Items))
	}

	if err := cache.delete(ctx, entry.Key); err != nil {
		t.Fatalf("delete() error = %v", err)
	}
	if metaSvc.FileExists(virtualPath) {
		t.Fatal("delete() left metadata file behind")
	}
	if err := cache.delete(ctx, entry.Key); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("delete() missing error = %v, want os.ErrNotExist", err)
	}
}
