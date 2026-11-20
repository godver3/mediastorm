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
