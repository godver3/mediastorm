package handlers

import (
	"sort"
	"testing"
)

func TestLiveSectionShowsOnlyServerWideFields(t *testing.T) {
	section := SettingsSchema["live"].(map[string]interface{})
	if section["group"] != "providers" {
		t.Fatalf("live section group = %v, want providers (ungrouped sections are not drawn)", section["group"])
	}
	fields := section["fields"].(map[string]interface{})
	var visible []string
	for key, def := range fields {
		if hidden, _ := def.(map[string]interface{})["hidden"].(bool); !hidden {
			visible = append(visible, key)
		}
	}
	sort.Strings(visible)
	want := []string{"comskip.enabled", "comskip.iniPath", "comskip.path", "timeshiftMinutes"}
	if len(visible) != len(want) {
		t.Fatalf("visible live fields = %v, want %v", visible, want)
	}
	for i := range want {
		if visible[i] != want[i] {
			t.Fatalf("visible live fields = %v, want %v", visible, want)
		}
	}
}

// TestLivePlaybackSectionHasNoConnectionTest: the "Live TV Playback" card shows only timeshift
// and comskip; its Test Connection button tested the hidden single-source fields and failed
// for anyone with named sources only. The sources keep their own test.
func TestLivePlaybackSectionHasNoConnectionTest(t *testing.T) {
	live, _ := SettingsSchema["live"].(map[string]interface{})
	if live == nil {
		t.Fatal("no live section")
	}
	if testable, _ := live["testable"].(bool); testable {
		t.Error("the Live TV Playback section still offers Test Connection")
	}
	fields, _ := live["fields"].(map[string]interface{})
	playlist, _ := fields["playlistUrl"].(map[string]interface{})
	if hidden, _ := playlist["hidden"].(bool); !hidden {
		t.Error("the legacy playlist URL field is visible")
	}
	if tv, _ := SettingsSchema["liveTV"].(map[string]interface{}); tv != nil {
		if testable, _ := tv["testable"].(bool); !testable {
			t.Error("the profile Live TV section lost its test")
		}
	}
}
