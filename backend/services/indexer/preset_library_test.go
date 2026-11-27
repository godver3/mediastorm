package indexer

import (
	"context"
	"testing"

	"novastream/config"
)

// "Library copy" names no ranking: a search asked for it ranks as without a
// request, and asking for it is not refused.
func TestLibraryCopyRanksAsWithoutRequest(t *testing.T) {
	svc, _, presetID := newPresetTestService(t, nil, nil)
	settings, _ := svc.cfg.Load()
	settings.QualityPresets.AllowRequestOverride = true
	got := svc.SelectQualityPreset(SearchOptions{MediaType: "series", QualityPreset: config.QualityPresetLocal}, settings)
	if got.PresetID != presetID || got.Source == "request" {
		t.Fatalf("library copy request: %+v, want the server preset", got)
	}
	if err := svc.CheckRequestedPreset(context.Background(), SearchOptions{MediaType: "series", QualityPreset: config.QualityPresetLocal}); err != nil {
		t.Fatalf("library copy request refused: %v", err)
	}
}
