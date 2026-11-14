package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"novastream/config"
	"testing"
)

func TestRegionalDiscoveryPresetEndpointDoesNotWriteSettings(t *testing.T) {
	h := &SettingsHandler{}
	w := httptest.NewRecorder()
	h.RegionalDiscoveryPresets(w, httptest.NewRequest(http.MethodGet, "/api/discovery/presets?region=DE&preset=netflix-chart", nil))
	var result struct {
		Shelves []config.ShelfConfig `json:"shelves"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Shelves) != 2 {
		t.Fatalf("preset response: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.RegionalDiscoveryPresets(w, httptest.NewRequest(http.MethodGet, "/api/discovery/presets?region=invalid&preset=netflix-chart", nil))
	if w.Code != 400 {
		t.Fatal("invalid region accepted")
	}
}
