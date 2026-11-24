package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/internal/auth"
	"novastream/internal/trash"
	"novastream/models"
	"novastream/services/indexer"
	"novastream/services/qualityprofiles"
)

// maxPresetBody bounds request bodies (pasted preset JSON can be large).
const maxPresetBody = 8 << 20

type presetUserSettings interface {
	Get(userID string) (*models.UserSettings, error)
	Update(userID string, settings models.UserSettings) error
}

type presetClientSettings interface {
	Get(clientID, userID string) (*models.ClientFilterSettings, error)
	Update(clientID, userID string, settings models.ClientFilterSettings) error
}

// presetDevices tells which profiles have used a registered device.
type presetDevices interface {
	Get(clientID string) (*models.Client, error)
	HasProfile(clientID, userID string) bool
}

// QualityPresetsHandler serves the quality preset API.
type QualityPresetsHandler struct {
	Service        *qualityprofiles.Service
	Config         *config.Manager
	UserSettings   presetUserSettings
	ClientSettings presetClientSettings
	Devices        presetDevices
	// ProfileOwner returns the account that owns a profile ("" if unknown).
	ProfileOwner  func(profileID string) string
	SearchCache   SearchCacheClearer
	PrequeueStore PrequeueClearer
	// Select reports the preset a search would use ("" for the simple
	// ranking) and where the choice came from: request, title, device,
	// profile, global or none. titleID is "" for no particular title.
	Select func(mediaType, userID, clientID, titleID string, anime bool) (presetID, source string)
	// TitleAnime tells whether a title is anime (and why), from metadata.
	TitleAnime func(ctx context.Context, mediaType, titleID, name string, year int, imdbID string) (bool, string)
}

// clearCaches drops ranked results after anything that changes ranking.
func (h *QualityPresetsHandler) clearCaches() {
	if h.SearchCache != nil {
		h.SearchCache.ClearSearchCache()
	}
	if h.PrequeueStore != nil {
		h.PrequeueStore.DeleteAll()
	}
}

// RegisterQualityPresets registers the page and API. Reading and choosing
// one's own defaults work under /admin and /account for every signed-in
// account; managing presets only under /admin, for the administrator.
func (h *AdminUIHandler) RegisterQualityPresets(r *mux.Router, qp *QualityPresetsHandler) {
	for _, prefix := range []string{"/admin", "/account"} {
		r.HandleFunc(prefix+"/quality-presets", h.RequireAuth(h.QualityPresetsPage)).Methods(http.MethodGet)
		api := prefix + "/api/quality-presets"
		r.HandleFunc(api, h.RequireAuth(qp.List)).Methods(http.MethodGet)
		r.HandleFunc(api+"/fixed-checks", h.RequireAuth(qp.FixedChecks)).Methods(http.MethodGet)
		r.HandleFunc(api+"/assign", h.RequireAuth(qp.Assign)).Methods(http.MethodPut)
		r.HandleFunc(api+"/effective", h.RequireAuth(qp.ClientList)).Methods(http.MethodGet)
		r.HandleFunc(api+"/license", h.RequireAuth(qp.License)).Methods(http.MethodGet)
		if prefix == "/admin" {
			// Fixed paths come before /{id}: mux matches routes in order.
			h.registerQualityPresetAdmin(r, api, qp)
		}
		r.HandleFunc(api+"/{id}", h.RequireAuth(qp.Get)).Methods(http.MethodGet)
		r.HandleFunc(api+"/{id}/explain", h.RequireAuth(qp.Explain)).Methods(http.MethodPost)
	}
}

// registerQualityPresetAdmin registers the administrator's preset routes.
func (h *AdminUIHandler) registerQualityPresetAdmin(r *mux.Router, api string, qp *QualityPresetsHandler) {
	r.HandleFunc(api+"/catalog", h.RequireMasterAuth(qp.Catalog)).Methods(http.MethodGet)
	r.HandleFunc(api+"/selection", h.RequireMasterAuth(qp.Selection)).Methods(http.MethodPut)
	r.HandleFunc(api+"/sources", h.RequireMasterAuth(qp.Sources)).Methods(http.MethodGet, http.MethodPut)
	r.HandleFunc(api+"/sync", h.RequireMasterAuth(qp.SyncAll)).Methods(http.MethodPost)
	r.HandleFunc(api+"/import/trash", h.RequireMasterAuth(qp.ImportTrash)).Methods(http.MethodPost)
	r.HandleFunc(api+"/import/arr/profiles", h.RequireMasterAuth(qp.ArrProfiles)).Methods(http.MethodPost)
	r.HandleFunc(api+"/import/arr", h.RequireMasterAuth(qp.ImportArr)).Methods(http.MethodPost)
	r.HandleFunc(api+"/import/json", h.RequireMasterAuth(qp.ImportJSON)).Methods(http.MethodPost)
	r.HandleFunc(api+"/{id}", h.RequireMasterAuth(qp.Delete)).Methods(http.MethodDelete)
	r.HandleFunc(api+"/{id}/export", h.RequireMasterAuth(qp.Export)).Methods(http.MethodGet)
	r.HandleFunc(api+"/{id}/duplicate", h.RequireMasterAuth(qp.Duplicate)).Methods(http.MethodPost)
	r.HandleFunc(api+"/{id}/settings", h.RequireMasterAuth(qp.UpdateSettings)).Methods(http.MethodPut)
	r.HandleFunc(api+"/{id}/override", h.RequireMasterAuth(qp.Override)).Methods(http.MethodPut, http.MethodDelete)
	r.HandleFunc(api+"/{id}/formats", h.RequireMasterAuth(qp.AddFormat)).Methods(http.MethodPost)
	r.HandleFunc(api+"/{id}/formats/{key}", h.RequireMasterAuth(qp.RemoveFormat)).Methods(http.MethodDelete)
	r.HandleFunc(api+"/{id}/formats/{key}/disabled", h.RequireMasterAuth(qp.SetDisabled)).Methods(http.MethodPut)
	r.HandleFunc(api+"/{id}/check", h.RequireMasterAuth(qp.Check)).Methods(http.MethodPost)
	r.HandleFunc(api+"/{id}/pending", h.RequireMasterAuth(qp.Pending)).Methods(http.MethodPost, http.MethodDelete)
	r.HandleFunc(api+"/{id}/conflicts", h.RequireMasterAuth(qp.ResolveConflict)).Methods(http.MethodPost)
	r.HandleFunc(api+"/{id}/review", h.RequireMasterAuth(qp.Review)).Methods(http.MethodPost)
	r.HandleFunc(api+"/{id}/rollback", h.RequireMasterAuth(qp.Rollback)).Methods(http.MethodPost)
	r.HandleFunc(api+"/{id}/dry-run", h.RequireMasterAuth(qp.DryRun)).Methods(http.MethodPost)
}

// QualityPresetsPage renders the management page.
func (h *AdminUIHandler) QualityPresetsPage(w http.ResponseWriter, r *http.Request) {
	master, account, base, username := h.getPageRoleInfo(r)
	data := AdminPageData{CurrentPath: base + "/quality-presets", BasePath: base, ServerBasePath: h.serverBasePath,
		IsAdmin: master, AccountID: account, Username: username, Users: h.getScopedUsers(master, account),
		Version: GetBackendVersion(), BuildID: GetBackendBuildID()}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if h.presetsTemplate == nil {
		http.Error(w, "Quality presets page unavailable", http.StatusInternalServerError)
		return
	}
	if err := h.presetsTemplate.ExecuteTemplate(w, "base", data); err != nil {
		http.Error(w, "Unable to render quality presets", http.StatusInternalServerError)
	}
}

func presetJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func presetError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, qualityprofiles.ErrNotFound) {
		status = http.StatusNotFound
	}
	if errors.Is(err, errPresetNotOffered) {
		status = http.StatusForbidden
	}
	if errors.Is(err, qualityprofiles.ErrStorage) {
		// Not the client's fault; the details belong in the log.
		log.Printf("[quality-presets] %v", err)
		writeJSONError(w, qualityprofiles.ErrStorage.Error(), http.StatusInternalServerError)
		return
	}
	writeJSONError(w, err.Error(), status)
}

func decodePresetBody(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodePresetBodyWith(w, r, v, false)
}

// decodeStrictPresetBody rejects unknown fields: on the selection and
// assignment endpoints a misspelled key would otherwise leave a slot
// unchanged without notice.
func decodeStrictPresetBody(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodePresetBodyWith(w, r, v, true)
}

func decodePresetBodyWith(w http.ResponseWriter, r *http.Request, v any, strict bool) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxPresetBody)
	dec := json.NewDecoder(r.Body)
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		writeJSONError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func isMasterRequest(r *http.Request) bool {
	v, _ := r.Context().Value(auth.ContextKeyIsMaster).(bool)
	return v
}

func requestAccountID(r *http.Request) string {
	v, _ := r.Context().Value(auth.ContextKeyAccountID).(string)
	return v
}

// presetDetail is the full view of one preset.
type presetDetail struct {
	Preset   *qualityprofiles.Preset   `json:"preset"`
	Resolved *qualityprofiles.Resolved `json:"resolved,omitempty"`
	Errors   []string                  `json:"errors,omitempty"`
}

func (h *QualityPresetsHandler) detail(ctx context.Context, p *qualityprofiles.Preset) presetDetail {
	d := presetDetail{Preset: p.Redacted()}
	res, err := qualityprofiles.Resolve(p)
	if err != nil {
		d.Errors = append(d.Errors, err.Error())
		return d
	}
	if _, errs := res.Evaluator(); len(errs) > 0 {
		for _, e := range errs {
			d.Errors = append(d.Errors, e.Error())
		}
	}
	d.Resolved = res
	return d
}

// FixedChecks lists the checks that always run before any ranking.
func (h *QualityPresetsHandler) FixedChecks(w http.ResponseWriter, r *http.Request) {
	presetJSON(w, http.StatusOK, indexer.FixedRankingChecks)
}

// Get returns one preset with its resolved state.
func (h *QualityPresetsHandler) Get(w http.ResponseWriter, r *http.Request) {
	p, err := h.Service.Get(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		presetError(w, err)
		return
	}
	d := h.detail(r.Context(), p)
	if !isMasterRequest(r) {
		// Other accounts see how a preset ranks, not where it comes from:
		// instance URLs and raw errors are for the administrator.
		cp := *d.Preset
		cp.Origin.ArrURL, cp.Origin.ArrAPIKey = "", ""
		if cp.LastCheckError != "" {
			cp.LastCheckError = "the last update check failed"
		}
		d.Preset = &cp
	}
	presetJSON(w, http.StatusOK, d)
}

// respond answers a preset change. The service clears the search caches
// itself when the change affects ranking (see Service.SetOnChange).
func (h *QualityPresetsHandler) respond(w http.ResponseWriter, r *http.Request, p *qualityprofiles.Preset, err error) {
	if err != nil {
		presetError(w, err)
		return
	}
	presetJSON(w, http.StatusOK, h.detail(r.Context(), p))
}

// Catalog lists importable TRaSH profiles.
func (h *QualityPresetsHandler) Catalog(w http.ResponseWriter, r *http.Request) {
	cat, err := h.Service.Catalog(r.Context())
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadGateway)
		return
	}
	presetJSON(w, http.StatusOK, cat)
}

// License serves the licence of the bundled TRaSH-Guides data.
func (h *QualityPresetsHandler) License(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, trash.BundledLicense)
}
