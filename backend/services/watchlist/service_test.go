package watchlist_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"novastream/models"
	"novastream/services/watchlist"
)

func TestServiceAddListAndPersist(t *testing.T) {
	dir := t.TempDir()
	svc, err := watchlist.NewService(dir)
	if err != nil {
		t.Fatalf("expected service, got error: %v", err)
	}

	added, err := svc.AddOrUpdate(models.DefaultUserID, models.WatchlistUpsert{
		ID:        "123",
		MediaType: "movie",
		Name:      "Example Movie",
		Year:      2024,
		PosterURL: "https://poster",
	})
	if err != nil {
		t.Fatalf("failed to add item: %v", err)
	}

	if added.Name != "Example Movie" {
		t.Fatalf("expected name to persist, got %q", added.Name)
	}

	items, err := svc.List(models.DefaultUserID)
	if err != nil {
		t.Fatalf("list returned error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 watchlist item, got %d", len(items))
	}

	if items[0].AddedAt.IsZero() {
		t.Fatalf("expected AddedAt to be set")
	}

	reloaded, err := watchlist.NewService(dir)
	if err != nil {
		t.Fatalf("failed to reload service: %v", err)
	}

	reloadedItems, err := reloaded.List(models.DefaultUserID)
	if err != nil {
		t.Fatalf("list after reload returned error: %v", err)
	}
	if len(reloadedItems) != 1 {
		t.Fatalf("expected 1 item after reload, got %d", len(reloadedItems))
	}

	if reloadedItems[0].Name != "Example Movie" {
		t.Fatalf("expected name to survive reload, got %q", reloadedItems[0].Name)
	}
}

func TestServiceUpdateAndRemove(t *testing.T) {
	dir := t.TempDir()
	svc, err := watchlist.NewService(dir)
	if err != nil {
		t.Fatalf("expected service, got error: %v", err)
	}

	_, err = svc.AddOrUpdate(models.DefaultUserID, models.WatchlistUpsert{ID: "s1", MediaType: "series", Name: "Pilot"})
	if err != nil {
		t.Fatalf("failed to seed watchlist: %v", err)
	}

	// Note: Watch progress tracking has been moved to a separate service (history service)
	// This test now only verifies basic watchlist functionality

	removed, err := svc.Remove(models.DefaultUserID, "series", "s1")
	if err != nil {
		t.Fatalf("remove returned error: %v", err)
	}
	if !removed {
		t.Fatalf("remove returned false")
	}

	if items, err := svc.List(models.DefaultUserID); err != nil {
		t.Fatalf("list after removal returned error: %v", err)
	} else if len(items) != 0 {
		t.Fatalf("expected watchlist to be empty, got %d", len(items))
	}
}

func TestServiceIsolatesUsers(t *testing.T) {
	dir := t.TempDir()
	svc, err := watchlist.NewService(dir)
	if err != nil {
		t.Fatalf("expected service, got error: %v", err)
	}

	alphaID := "alpha-user"
	betaID := "beta-user"

	if _, err := svc.AddOrUpdate(alphaID, models.WatchlistUpsert{ID: "1", MediaType: "movie", Name: "Alpha Movie"}); err != nil {
		t.Fatalf("failed to add alpha item: %v", err)
	}
	if _, err := svc.AddOrUpdate(betaID, models.WatchlistUpsert{ID: "2", MediaType: "movie", Name: "Beta Movie"}); err != nil {
		t.Fatalf("failed to add beta item: %v", err)
	}

	alphaItems, err := svc.List(alphaID)
	if err != nil {
		t.Fatalf("list alpha returned error: %v", err)
	}
	betaItems, err := svc.List(betaID)
	if err != nil {
		t.Fatalf("list beta returned error: %v", err)
	}

	if len(alphaItems) != 1 || alphaItems[0].Name != "Alpha Movie" {
		t.Fatalf("unexpected alpha items %+v", alphaItems)
	}
	if len(betaItems) != 1 || betaItems[0].Name != "Beta Movie" {
		t.Fatalf("unexpected beta items %+v", betaItems)
	}
}

func TestServiceAddOrUpdateGenresAndRuntime(t *testing.T) {
	dir := t.TempDir()
	svc, err := watchlist.NewService(dir)
	if err != nil {
		t.Fatalf("expected service, got error: %v", err)
	}

	added, err := svc.AddOrUpdate(models.DefaultUserID, models.WatchlistUpsert{
		ID:             "456",
		MediaType:      "movie",
		Name:           "Genre Movie",
		Year:           2025,
		Genres:         []string{"Action", "Comedy"},
		RuntimeMinutes: 120,
	})
	if err != nil {
		t.Fatalf("failed to add item: %v", err)
	}

	if len(added.Genres) != 2 || added.Genres[0] != "Action" || added.Genres[1] != "Comedy" {
		t.Fatalf("expected genres [Action Comedy], got %v", added.Genres)
	}
	if added.RuntimeMinutes != 120 {
		t.Fatalf("expected runtimeMinutes 120, got %d", added.RuntimeMinutes)
	}

	// Update with new genres — should replace
	updated, err := svc.AddOrUpdate(models.DefaultUserID, models.WatchlistUpsert{
		ID:        "456",
		MediaType: "movie",
		Genres:    []string{"Drama"},
	})
	if err != nil {
		t.Fatalf("failed to update item: %v", err)
	}
	if len(updated.Genres) != 1 || updated.Genres[0] != "Drama" {
		t.Fatalf("expected genres [Drama] after update, got %v", updated.Genres)
	}
	// RuntimeMinutes should be preserved (not zeroed out)
	if updated.RuntimeMinutes != 120 {
		t.Fatalf("expected runtimeMinutes to be preserved as 120, got %d", updated.RuntimeMinutes)
	}

	// Verify persistence across reload
	reloaded, err := watchlist.NewService(dir)
	if err != nil {
		t.Fatalf("failed to reload service: %v", err)
	}
	items, err := reloaded.List(models.DefaultUserID)
	if err != nil {
		t.Fatalf("list after reload returned error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item after reload, got %d", len(items))
	}
	if len(items[0].Genres) != 1 || items[0].Genres[0] != "Drama" {
		t.Fatalf("expected genres to survive reload, got %v", items[0].Genres)
	}
	if items[0].RuntimeMinutes != 120 {
		t.Fatalf("expected runtimeMinutes to survive reload, got %d", items[0].RuntimeMinutes)
	}
}

func TestServiceLoadsLegacyFormat(t *testing.T) {
	dir := t.TempDir()
	legacyItems := []models.WatchlistItem{{
		ID:        "legacy",
		MediaType: "movie",
		Name:      "Legacy",
		AddedAt:   time.Date(2023, 10, 1, 12, 0, 0, 0, time.UTC),
	}}

	data, err := json.Marshal(legacyItems)
	if err != nil {
		t.Fatalf("failed to marshal legacy items: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "watchlist.json"), data, 0o644); err != nil {
		t.Fatalf("failed to write legacy watchlist: %v", err)
	}

	svc, err := watchlist.NewService(dir)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	items, err := svc.List(models.DefaultUserID)
	if err != nil {
		t.Fatalf("list returned error: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 legacy item, got %d", len(items))
	}
	if items[0].Name != "Legacy" {
		t.Fatalf("expected legacy item name, got %q", items[0].Name)
	}
}
