package handlers

import "testing"

func TestLiveSectionShowsOnlyTimeshift(t *testing.T) {
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
	if len(visible) != 1 || visible[0] != "timeshiftMinutes" {
		t.Fatalf("visible live fields = %v, want [timeshiftMinutes]", visible)
	}
}
