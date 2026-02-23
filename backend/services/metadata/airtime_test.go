package metadata

import (
	"novastream/models"
	"testing"
)

func TestInferTimezoneFromNetwork(t *testing.T) {
	tests := []struct {
		name     string
		network  string
		country  string
		wantTZ   string
	}{
		{"HBO direct match", "HBO", "", "America/New_York"},
		{"BBC One direct match", "BBC One", "", "Europe/London"},
		{"tvN Korean", "tvN", "", "Asia/Seoul"},
		{"Unknown network with USA country", "SomeNewNetwork", "usa", "America/New_York"},
		{"Unknown network with jpn country", "SomeNewNetwork", "jpn", "Asia/Tokyo"},
		{"Unknown network unknown country", "SomeNewNetwork", "xyz", ""},
		{"Empty network with country", "", "gbr", "Europe/London"},
		{"Partial match HBO Max", "HBO Max", "", "America/New_York"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferTimezoneFromNetwork(tt.network, tt.country)
			if got != tt.wantTZ {
				t.Errorf("inferTimezoneFromNetwork(%q, %q) = %q, want %q",
					tt.network, tt.country, got, tt.wantTZ)
			}
		})
	}
}

func TestApplyAirTimeFromTVDB(t *testing.T) {
	title := &models.Title{Network: "HBO"}
	applyAirTimeFromTVDB(title, "21:00", "HBO", "usa")

	if title.AirsTime != "21:00" {
		t.Errorf("AirsTime = %q, want %q", title.AirsTime, "21:00")
	}
	if title.AirsTimezone != "America/New_York" {
		t.Errorf("AirsTimezone = %q, want %q", title.AirsTimezone, "America/New_York")
	}
}

func TestApplyAirTimeFromTVDB_NoTime(t *testing.T) {
	title := &models.Title{Network: "HBO"}
	applyAirTimeFromTVDB(title, "", "HBO", "usa")

	if title.AirsTime != "" {
		t.Errorf("AirsTime should be empty, got %q", title.AirsTime)
	}
	// No timezone should be set when there's no air time
	if title.AirsTimezone != "" {
		t.Errorf("AirsTimezone should be empty when no air time, got %q", title.AirsTimezone)
	}
}
