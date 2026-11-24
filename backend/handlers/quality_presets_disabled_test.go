package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Switched off, apps get no offered presets (so no Quality picker) and no
// per-search override, though the presets and the picker list are kept.
func TestQualityPresetClientListDisabled(t *testing.T) {
	h, _ := newPresetHandler(t)
	rec := httptest.NewRecorder()
	h.ImportJSON(rec, presetRequest(http.MethodPost, "/x?app=sonarr", handlerPresetJSON, true, "acc", nil))
	id := decodeDetail(t, rec).Preset.ID
	s, _ := h.Config.Load()
	s.QualityPresets.PickerPresets = []string{id, "simple"}
	s.QualityPresets.Disabled = true
	if err := h.Config.Save(s); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.ClientList(rec, presetRequest(http.MethodGet, "/api/quality-presets", "", false, "acc", nil))
	var got struct {
		Disabled             bool              `json:"disabled"`
		AllowRequestOverride bool              `json:"allowRequestOverride"`
		Offered              []json.RawMessage `json:"offered"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	if !got.Disabled || got.AllowRequestOverride || len(got.Offered) != 0 {
		t.Errorf("disabled list: %s", rec.Body.String())
	}
}
