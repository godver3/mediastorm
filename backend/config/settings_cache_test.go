package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadCacheReturnsIndependentCopies(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "settings.json"))
	s := DefaultSettings()
	s.Playback.AllowedTrackLanguages = []string{"eng"}
	if err := m.Save(s); err != nil {
		t.Fatal(err)
	}
	a, err := m.Load()
	if err != nil {
		t.Fatal(err)
	}
	a.Playback.AllowedTrackLanguages[0] = "mutated"
	a.ScheduledTasks.Tasks = append(a.ScheduledTasks.Tasks, ScheduledTask{ID: "x"})
	b, _ := m.Load()
	if len(b.Playback.AllowedTrackLanguages) != 1 || b.Playback.AllowedTrackLanguages[0] != "eng" {
		t.Fatalf("cache leaked caller mutation: %v", b.Playback.AllowedTrackLanguages)
	}
	for _, task := range b.ScheduledTasks.Tasks {
		if task.ID == "x" {
			t.Fatal("cache leaked appended task")
		}
	}
}

func TestLoadCachePicksUpExternalEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	m := NewManager(path)
	s := DefaultSettings()
	s.Server.Port = 1111
	if err := m.Save(s); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.Load(); got.Server.Port != 1111 {
		t.Fatalf("port = %d", got.Server.Port)
	}
	data, _ := os.ReadFile(path)
	data = []byte(strings.Replace(string(data), "1111", "2222", 1))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	_ = os.Chtimes(path, future, future)
	if got, _ := m.Load(); got.Server.Port != 2222 {
		t.Fatalf("external edit not picked up, port = %d", got.Server.Port)
	}
}

func TestLegacyInlineDryRunDetailsMovedToSidecar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	legacy := `{"scheduledTasks":{"tasks":[{"id":"t1","type":"plex_history_sync","dryRunDetails":{"toAdd":[{"name":"A","mediaType":"movie","id":"1"}]}}]}}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(path)
	s, err := m.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.ScheduledTasks.Tasks) != 1 || !s.ScheduledTasks.Tasks[0].HasDryRunDetails {
		t.Fatalf("expected hasDryRunDetails, got %+v", s.ScheduledTasks.Tasks)
	}
	d, err := m.LoadDryRunDetails("t1")
	if err != nil || d == nil || len(d.ToAdd) != 1 || d.ToAdd[0].Name != "A" {
		t.Fatalf("sidecar = %+v, %v", d, err)
	}
	if err := m.Save(s); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "dryRunDetails") {
		t.Fatal("settings.json still contains inline dryRunDetails")
	}
}
