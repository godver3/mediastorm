package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/internal/arr"
	"novastream/services/qualityprofiles"
)

// Delete removes a preset; selections pointing at it fall back to simple.
func (h *QualityPresetsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if err := h.Service.Delete(r.Context(), id); err != nil {
		presetError(w, err)
		return
	}
	if h.Config != nil {
		_ = h.Config.Update(func(s *config.Settings) error {
			changed := false
			if s.QualityPresets.MoviePreset == id {
				s.QualityPresets.MoviePreset, changed = "", true
			}
			if s.QualityPresets.SeriesPreset == id {
				s.QualityPresets.SeriesPreset, changed = "", true
			}
			if s.QualityPresets.AnimeMoviePreset == id {
				s.QualityPresets.AnimeMoviePreset, changed = "", true
			}
			if s.QualityPresets.AnimeSeriesPreset == id {
				s.QualityPresets.AnimeSeriesPreset, changed = "", true
			}
			kept := s.QualityPresets.PickerPresets[:0]
			for _, offered := range s.QualityPresets.PickerPresets {
				if offered == id {
					changed = true
					continue
				}
				kept = append(kept, offered)
			}
			s.QualityPresets.PickerPresets = kept
			hiddenKept := s.QualityPresets.HiddenByDefault[:0]
			for _, hidden := range s.QualityPresets.HiddenByDefault {
				if hidden == id {
					changed = true
					continue
				}
				hiddenKept = append(hiddenKept, hidden)
			}
			s.QualityPresets.HiddenByDefault = hiddenKept
			if !changed {
				return config.ErrNoChange
			}
			return nil
		})
	}
	h.clearCaches()
	w.WriteHeader(http.StatusNoContent)
}

// Export downloads a preset as JSON.
func (h *QualityPresetsHandler) Export(w http.ResponseWriter, r *http.Request) {
	p, err := h.Service.Get(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		presetError(w, err)
		return
	}
	redacted := p.Redacted()
	redacted.Origin.ArrAPIKey = ""
	data, err := qualityprofiles.Export(redacted)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	name := strings.Map(func(r rune) rune {
		if r == ' ' || r == '/' || r == '\\' || r == '"' {
			return '-'
		}
		return r
	}, p.DisplayName())
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".json"))
	_, _ = w.Write(data)
}

// Duplicate copies a preset.
func (h *QualityPresetsHandler) Duplicate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if r.ContentLength != 0 && !decodePresetBody(w, r, &in) {
		return
	}
	p, err := h.Service.Duplicate(r.Context(), mux.Vars(r)["id"], in.Name)
	if err != nil {
		presetError(w, err)
		return
	}
	presetJSON(w, http.StatusCreated, h.detail(r.Context(), p))
}

// UpdateSettings changes sync settings and options.
func (h *QualityPresetsHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	var in qualityprofiles.SettingsUpdate
	if !decodePresetBody(w, r, &in) {
		return
	}
	p, err := h.Service.UpdateSettings(r.Context(), mux.Vars(r)["id"], in)
	h.respond(w, r, p, err)
}

// Override sets (PUT {path, value}) or clears (DELETE ?path=) an override.
func (h *QualityPresetsHandler) Override(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if r.Method == http.MethodDelete {
		path := r.URL.Query().Get("path")
		if path == "" {
			writeJSONError(w, "path is required", http.StatusBadRequest)
			return
		}
		p, err := h.Service.ClearOverride(r.Context(), id, path)
		h.respond(w, r, p, err)
		return
	}
	var in struct {
		Path  string          `json:"path"`
		Value json.RawMessage `json:"value"`
	}
	if !decodePresetBody(w, r, &in) {
		return
	}
	p, err := h.Service.SetOverride(r.Context(), id, in.Path, in.Value)
	h.respond(w, r, p, err)
}

// AddFormat adds an own custom format.
func (h *QualityPresetsHandler) AddFormat(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Format arr.CustomFormat `json:"format"`
		Score  int              `json:"score"`
	}
	if !decodePresetBody(w, r, &in) {
		return
	}
	p, err := h.Service.AddFormat(r.Context(), mux.Vars(r)["id"], in.Format, in.Score)
	h.respond(w, r, p, err)
}

// RemoveFormat deletes an own format or reverts an edited one.
func (h *QualityPresetsHandler) RemoveFormat(w http.ResponseWriter, r *http.Request) {
	v := mux.Vars(r)
	p, err := h.Service.RemoveFormat(r.Context(), v["id"], v["key"])
	h.respond(w, r, p, err)
}

// SetDisabled enables or disables a format.
func (h *QualityPresetsHandler) SetDisabled(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Disabled bool `json:"disabled"`
	}
	if !decodePresetBody(w, r, &in) {
		return
	}
	v := mux.Vars(r)
	p, err := h.Service.SetDisabled(r.Context(), v["id"], v["key"], in.Disabled)
	h.respond(w, r, p, err)
}

// Check looks for an upstream update ({"apply": true} applies it now).
func (h *QualityPresetsHandler) Check(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Apply bool `json:"apply"`
	}
	if r.ContentLength != 0 && !decodePresetBody(w, r, &in) {
		return
	}
	res, err := h.Service.Check(r.Context(), mux.Vars(r)["id"], in.Apply)
	if err != nil {
		if res != nil && res.Preset != nil {
			presetJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "preset": res.Preset.Redacted()})
			return
		}
		presetError(w, err)
		return
	}
	res.Preset = res.Preset.Redacted()
	presetJSON(w, http.StatusOK, res)
}

// Pending applies (POST) or discards (DELETE) a staged update.
func (h *QualityPresetsHandler) Pending(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if r.Method == http.MethodDelete {
		p, err := h.Service.DiscardPending(r.Context(), id)
		h.respond(w, r, p, err)
		return
	}
	p, err := h.Service.ApplyPending(r.Context(), id)
	h.respond(w, r, p, err)
}

// ResolveConflict applies a conflict choice.
func (h *QualityPresetsHandler) ResolveConflict(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path   string                         `json:"path"`
		Choice qualityprofiles.ConflictChoice `json:"choice"`
		Value  json.RawMessage                `json:"value,omitempty"`
	}
	if !decodePresetBody(w, r, &in) {
		return
	}
	p, err := h.Service.ResolveConflict(r.Context(), mux.Vars(r)["id"], in.Path, in.Choice, in.Value)
	h.respond(w, r, p, err)
}

// Review approves or rejects formats waiting for review.
func (h *QualityPresetsHandler) Review(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Keys   []string `json:"keys"`
		Enable bool     `json:"enable"`
	}
	if !decodePresetBody(w, r, &in) {
		return
	}
	p, err := h.Service.ReviewNewFormats(r.Context(), mux.Vars(r)["id"], in.Keys, in.Enable)
	h.respond(w, r, p, err)
}

// Rollback restores a history entry.
func (h *QualityPresetsHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	var in struct {
		HistoryID string `json:"historyId"`
		Pin       bool   `json:"pin"`
	}
	if !decodePresetBody(w, r, &in) {
		return
	}
	p, err := h.Service.Rollback(r.Context(), mux.Vars(r)["id"], in.HistoryID, in.Pin)
	h.respond(w, r, p, err)
}

type sampleRequest struct {
	Titles           []string `json:"titles"`
	OriginalLanguage string   `json:"originalLanguage"`
	RuntimeMinutes   int      `json:"runtimeMinutes"`
	Detail           bool     `json:"detail"`
}

func (s sampleRequest) context() arr.ReleaseContext {
	return arr.ReleaseContext{OriginalLanguage: arr.LanguageFromCode(s.OriginalLanguage), RuntimeMinutes: s.RuntimeMinutes}
}

// Sample limits keep the regex work of one request bounded: the
// administrator may paste a long list, other accounts test a few names.
const (
	maxSampleTitles        = 500
	maxAccountSampleTitles = 50
	maxSampleTitleLength   = 400
)

func readSample(w http.ResponseWriter, r *http.Request) (sampleRequest, bool) {
	var in sampleRequest
	if !decodePresetBody(w, r, &in) {
		return in, false
	}
	if len(in.Titles) == 0 {
		writeJSONError(w, "titles are required", http.StatusBadRequest)
		return in, false
	}
	limit := maxSampleTitles
	if !isMasterRequest(r) {
		limit = maxAccountSampleTitles
	}
	if len(in.Titles) > limit {
		in.Titles = in.Titles[:limit]
	}
	for i, t := range in.Titles {
		if len(t) > maxSampleTitleLength {
			in.Titles[i] = t[:maxSampleTitleLength]
		}
	}
	return in, true
}

// Explain evaluates sample titles with a preset.
func (h *QualityPresetsHandler) Explain(w http.ResponseWriter, r *http.Request) {
	in, ok := readSample(w, r)
	if !ok {
		return
	}
	res, err := h.Service.Explain(r.Context(), mux.Vars(r)["id"], in.Titles, in.context(), in.Detail)
	if err != nil {
		presetError(w, err)
		return
	}
	presetJSON(w, http.StatusOK, res)
}

// DryRun previews an update's effect on sample titles.
func (h *QualityPresetsHandler) DryRun(w http.ResponseWriter, r *http.Request) {
	in, ok := readSample(w, r)
	if !ok {
		return
	}
	res, err := h.Service.DryRun(r.Context(), mux.Vars(r)["id"], in.Titles, in.context())
	if err != nil {
		presetError(w, err)
		return
	}
	presetJSON(w, http.StatusOK, res)
}
