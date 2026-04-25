package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMigratesCreditsDetectionToCreditsAutoSkip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"playback":{"preferredPlayer":"native","creditsDetection":true}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if !settings.Playback.CreditsAutoSkip {
		t.Fatal("expected legacy creditsDetection=true to migrate to creditsAutoSkip=true")
	}
	if settings.Playback.CreditsDetection {
		t.Fatal("expected legacy creditsDetection field to be cleared after migration")
	}
}
