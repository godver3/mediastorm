package handlers

import (
	"io"
	"log"
	"net/http"
	"strings"

	"novastream/config"
	"novastream/internal/arr"
	"novastream/services/qualityprofiles"
)

// Sources reads or replaces the snapshot sources.
func (h *QualityPresetsHandler) Sources(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		presetJSON(w, http.StatusOK, h.Service.Sources())
		return
	}
	var in []qualityprofiles.Source
	if !decodePresetBody(w, r, &in) {
		return
	}
	if err := qualityprofiles.ValidateSources(in); err != nil {
		presetError(w, err)
		return
	}
	settings, err := h.Config.Load()
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	settings.QualityPresets.Sources = SourcesToConfig(in)
	if err := h.Config.Save(settings); err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	presetJSON(w, http.StatusOK, h.Service.Sources())
}

// SourcesFromConfig converts configured sources (defaults when empty).
func SourcesFromConfig(in []config.QualityPresetSource) []qualityprofiles.Source {
	if len(in) == 0 {
		return qualityprofiles.DefaultSources()
	}
	out := make([]qualityprofiles.Source, 0, len(in))
	for _, s := range in {
		out = append(out, qualityprofiles.Source{ID: s.ID, Name: s.Name, Type: qualityprofiles.SourceType(s.Type), Repo: s.Repo, Ref: s.Ref, URL: s.URL, Priority: s.Priority, Enabled: s.Enabled})
	}
	return out
}

// SourcesToConfig converts sources for storage in settings.
func SourcesToConfig(in []qualityprofiles.Source) []config.QualityPresetSource {
	out := make([]config.QualityPresetSource, 0, len(in))
	for _, s := range in {
		out = append(out, config.QualityPresetSource{ID: s.ID, Name: s.Name, Type: string(s.Type), Repo: s.Repo, Ref: s.Ref, URL: s.URL, Priority: s.Priority, Enabled: s.Enabled})
	}
	return out
}

// SyncAll checks every syncing preset now.
func (h *QualityPresetsHandler) SyncAll(w http.ResponseWriter, r *http.Request) {
	res, err := h.Service.SyncDue(r.Context(), true)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadGateway)
		return
	}
	presetJSON(w, http.StatusOK, res)
}

// ImportTrash creates a preset from a TRaSH profile.
func (h *QualityPresetsHandler) ImportTrash(w http.ResponseWriter, r *http.Request) {
	var in qualityprofiles.TrashImport
	if !decodePresetBody(w, r, &in) {
		return
	}
	p, warnings, err := h.Service.ImportTrash(r.Context(), in)
	if err != nil {
		presetError(w, err)
		return
	}
	d := h.detail(r.Context(), p)
	d.Errors = append(d.Errors, warnings...)
	h.ensureSyncTask()
	presetJSON(w, http.StatusCreated, d)
}

// ArrProfiles lists the profiles of a Sonarr/Radarr instance.
func (h *QualityPresetsHandler) ArrProfiles(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL    string `json:"url"`
		APIKey string `json:"apiKey"`
	}
	if !decodePresetBody(w, r, &in) {
		return
	}
	app, profiles, err := h.Service.ArrProfiles(r.Context(), in.URL, in.APIKey)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadGateway)
		return
	}
	type item struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	out := struct {
		App      arr.App `json:"app"`
		Profiles []item  `json:"profiles"`
	}{App: app}
	for _, p := range profiles {
		out.Profiles = append(out.Profiles, item{ID: p.ID, Name: p.Name})
	}
	presetJSON(w, http.StatusOK, out)
}

// ImportArr creates a preset from an instance's profile.
func (h *QualityPresetsHandler) ImportArr(w http.ResponseWriter, r *http.Request) {
	var in qualityprofiles.ArrImport
	if !decodePresetBody(w, r, &in) {
		return
	}
	p, err := h.Service.ImportArr(r.Context(), in)
	if err != nil {
		presetError(w, err)
		return
	}
	h.ensureSyncTask()
	presetJSON(w, http.StatusCreated, h.detail(r.Context(), p))
}

// ImportJSON creates a manual preset from pasted JSON.
func (h *QualityPresetsHandler) ImportJSON(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPresetBody))
	if err != nil {
		writeJSONError(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	app := arr.App(strings.ToLower(r.URL.Query().Get("app")))
	p, err := h.Service.ImportJSON(r.Context(), data, app, r.URL.Query().Get("name"))
	if err != nil {
		presetError(w, err)
		return
	}
	presetJSON(w, http.StatusCreated, h.detail(r.Context(), p))
}

// ensureSyncTask adds the hourly update check once a preset that follows
// an upstream exists (servers without presets do not get the task).
func (h *QualityPresetsHandler) ensureSyncTask() {
	if h.Config == nil {
		return
	}
	s, err := h.Config.Load()
	if err != nil || !EnsureQualityPresetSyncTask(&s) {
		return
	}
	if err := h.Config.Save(s); err != nil {
		log.Printf("[quality-presets] failed to add the update check task: %v", err)
	}
}
