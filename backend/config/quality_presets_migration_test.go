package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestQualityPresetDefaultsMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	// A settings file written before quality presets existed.
	if err := os.WriteFile(path, []byte(`{"server":{"port":7777}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewManager(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if !s.QualityPresets.AllowRequestOverride || s.QualityPresets.MoviePreset != "" || s.QualityPresets.SeriesPreset != "" {
		t.Errorf("defaults not applied: %+v", s.QualityPresets)
	}
	// An explicit choice is kept.
	if err := os.WriteFile(path, []byte(`{"qualityPresets":{"allowRequestOverride":false,"seriesPreset":"abc"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err = NewManager(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if s.QualityPresets.AllowRequestOverride || s.QualityPresets.SeriesPreset != "abc" {
		t.Errorf("explicit settings overwritten: %+v", s.QualityPresets)
	}
}
