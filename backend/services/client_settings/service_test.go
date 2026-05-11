package client_settings

import (
	"testing"

	"novastream/models"
)

func TestClearAppearanceOverrides_RemovesOnlyAppearance(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	accent := "#ff00cc"
	resolution := "1080p"
	if err := svc.Update("client1", models.ClientFilterSettings{
		MaxResolution: &resolution,
		Appearance: &models.AppearanceSettings{
			AccentColor: accent,
		},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	count, err := svc.ClearAppearanceOverrides()
	if err != nil {
		t.Fatalf("ClearAppearanceOverrides: %v", err)
	}
	if count != 1 {
		t.Fatalf("cleared count = %d, want 1", count)
	}

	got, err := svc.Get("client1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-appearance settings to remain")
	}
	if got.Appearance != nil {
		t.Fatalf("appearance override was not cleared: %+v", got.Appearance)
	}
	if got.MaxResolution == nil || *got.MaxResolution != "1080p" {
		t.Fatalf("maxResolution = %+v, want 1080p", got.MaxResolution)
	}
}

func TestClearAppearanceOverrides_DeletesAppearanceOnlySettings(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	accent := "#ff00cc"
	if err := svc.Update("client1", models.ClientFilterSettings{
		Appearance: &models.AppearanceSettings{
			AccentColor: accent,
		},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	count, err := svc.ClearAppearanceOverrides()
	if err != nil {
		t.Fatalf("ClearAppearanceOverrides: %v", err)
	}
	if count != 1 {
		t.Fatalf("cleared count = %d, want 1", count)
	}

	got, err := svc.Get("client1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("appearance-only settings should be deleted, got %+v", got)
	}
}
