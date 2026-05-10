package localmedia

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/models"
	"novastream/services/streaming"
)

func TestBuildAndParseStreamPath(t *testing.T) {
	item := models.LocalMediaItem{
		ID:       "item-123",
		FileName: "Movie.Title.2024.mkv",
	}

	path := BuildStreamPath(item)
	if path != "localmedia:item-123/Movie.Title.2024.mkv" {
		t.Fatalf("BuildStreamPath() = %q", path)
	}

	itemID, ok := ParseStreamPath(path)
	if !ok || itemID != "item-123" {
		t.Fatalf("ParseStreamPath() = %q, %t", itemID, ok)
	}
}

func TestProviderStreamRange(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "Movie.Title.2024.mkv")
	if err := os.WriteFile(filePath, []byte("0123456789"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	now := time.Now().UTC()
	repo := &fakeLocalMediaRepo{
		library: &models.LocalMediaLibrary{
			ID:        "lib1",
			Name:      "Movies",
			Type:      models.LocalMediaLibraryTypeMovie,
			RootPath:  root,
			CreatedAt: now,
			UpdatedAt: now,
		},
		items: map[string]*models.LocalMediaItem{
			"Movie.Title.2024.mkv": {
				ID:           "item1",
				LibraryID:    "lib1",
				FilePath:     filePath,
				FileName:     "Movie.Title.2024.mkv",
				RelativePath: "Movie.Title.2024.mkv",
				CreatedAt:    now,
				UpdatedAt:    now,
			},
		},
	}
	service := &Service{
		repo: repo,
	}
	provider := NewProvider(service)

	resp, err := provider.Stream(context.Background(), streaming.Request{
		Path:        "localmedia:item1/Movie.Title.2024.mkv",
		RangeHeader: "bytes=2-5",
		Method:      http.MethodGet,
	})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer resp.Close()

	if resp.Status != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", resp.Status, http.StatusPartialContent)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(body) != "2345" {
		t.Fatalf("body = %q, want %q", string(body), "2345")
	}
	if got := resp.Headers.Get("Content-Range"); got != "bytes 2-5/10" {
		t.Fatalf("Content-Range = %q", got)
	}
}

func TestProviderGetDirectURL(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "Mr Inbetween (2018) - S02E08.mkv")
	if err := os.WriteFile(filePath, []byte("video"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	now := time.Now().UTC()
	repo := &fakeLocalMediaRepo{
		library: &models.LocalMediaLibrary{
			ID:        "lib1",
			Name:      "TV Shows",
			Type:      models.LocalMediaLibraryTypeShow,
			RootPath:  root,
			CreatedAt: now,
			UpdatedAt: now,
		},
		items: map[string]*models.LocalMediaItem{
			"Mr Inbetween (2018) - S02E08.mkv": {
				ID:           "item1",
				LibraryID:    "lib1",
				FilePath:     filePath,
				FileName:     "Mr Inbetween (2018) - S02E08.mkv",
				RelativePath: "Mr Inbetween (2018) - S02E08.mkv",
				CreatedAt:    now,
				UpdatedAt:    now,
			},
		},
	}
	provider := NewProvider(&Service{repo: repo})

	got, err := provider.GetDirectURL(context.Background(), "localmedia:item1/Mr+Inbetween+(2018)+-+S02E08.mkv")
	if err != nil {
		t.Fatalf("GetDirectURL() error = %v", err)
	}
	if got != filePath {
		t.Fatalf("GetDirectURL() = %q, want %q", got, filePath)
	}
}

func TestProviderRejectsPathOutsideLibraryRoot(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	filePath := filepath.Join(outsideDir, "escape.mkv")
	if err := os.WriteFile(filePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	now := time.Now().UTC()
	repo := &fakeLocalMediaRepo{
		library: &models.LocalMediaLibrary{
			ID:        "lib1",
			Name:      "Movies",
			Type:      models.LocalMediaLibraryTypeMovie,
			RootPath:  root,
			CreatedAt: now,
			UpdatedAt: now,
		},
		items: map[string]*models.LocalMediaItem{
			"escape.mkv": {
				ID:           "item1",
				LibraryID:    "lib1",
				FilePath:     filePath,
				FileName:     "escape.mkv",
				RelativePath: "escape.mkv",
				CreatedAt:    now,
				UpdatedAt:    now,
			},
		},
	}
	service := &Service{
		repo: repo,
	}
	provider := NewProvider(service)

	_, err := provider.Stream(context.Background(), streaming.Request{
		Path:   "localmedia:item1/escape.mkv",
		Method: http.MethodGet,
	})
	if err == nil || !strings.Contains(err.Error(), "escaped library root") {
		t.Fatalf("Stream() error = %v, want escaped library root", err)
	}
}
