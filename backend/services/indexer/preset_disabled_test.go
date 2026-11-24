package indexer

import (
	"context"
	"testing"
)

// The server-wide switch overrides every level: request, title, device,
// profile and server all give way to the simple ranking.
func TestSelectQualityPresetDisabled(t *testing.T) {
	svc, _, presetID := newPresetTestService(t, nil, nil)
	settings, _ := svc.cfg.Load()
	if got := svc.SelectQualityPreset(SearchOptions{MediaType: "series"}, settings); got.PresetID != presetID {
		t.Fatalf("enabled: got %+v, want the server preset", got)
	}
	settings.QualityPresets.Disabled = true
	for _, opts := range []SearchOptions{
		{MediaType: "series"},
		{MediaType: "series", QualityPreset: presetID},
	} {
		if got := svc.SelectQualityPreset(opts, settings); got.PresetID != "" || got.Source != "disabled" {
			t.Errorf("disabled: got %+v for %+v", got, opts)
		}
	}
	if err := svc.cfg.Save(settings); err != nil {
		t.Fatal(err)
	}
	// A preset an app still names is ignored, not refused.
	if err := svc.CheckRequestedPreset(context.Background(), SearchOptions{MediaType: "series", QualityPreset: "no-such-preset"}); err != nil {
		t.Errorf("disabled: requested preset should be ignored, got %v", err)
	}
}
