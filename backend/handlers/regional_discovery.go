package handlers

import (
	"encoding/json"
	"net/http"
	"novastream/config"
)

// RegionalDiscoveryPresets only returns templates. The normal settings save
// path retains account/profile authorization and persistence behavior.
func (h *SettingsHandler) RegionalDiscoveryPresets(w http.ResponseWriter, r *http.Request) {
	shelves, err := config.RegionalDiscoveryPresets(r.URL.Query().Get("region"), r.URL.Query().Get("preset"))
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"shelves": shelves})
}
