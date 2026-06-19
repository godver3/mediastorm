package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"novastream/services/updates"
)

var defaultUpdatesService = updates.NewService()

type UpdatesHandler struct {
	service *updates.Service
}

func NewUpdatesHandler() *UpdatesHandler {
	return &UpdatesHandler{service: defaultUpdatesService}
}

func (h *UpdatesHandler) Status(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := updates.StatusRequest{
		BackendVersion:   GetBackendVersion(),
		BackendBuildID:   GetBackendBuildID(),
		FrontendVersion:  strings.TrimSpace(q.Get("frontendVersion")),
		FrontendBuildID:  strings.TrimSpace(q.Get("frontendBuildId")),
		FrontendPlatform: strings.TrimSpace(q.Get("frontendPlatform")),
		FrontendDevice:   strings.TrimSpace(q.Get("frontendDevice")),
		ForceRefresh:     q.Get("force") == "true" || q.Get("refresh") == "true",
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(h.service.Status(r.Context(), req))
}

func (h *UpdatesHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}
