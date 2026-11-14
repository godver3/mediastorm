package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"novastream/internal/auth"
	"novastream/services/badstreams"
)

type BadStreamsHandler struct {
	service *badstreams.Service
	users   prequeueOwnershipService
}

func (h *BadStreamsHandler) SetUsersService(users prequeueOwnershipService) { h.users = users }

type badStreamsListResponse struct {
	Streams    []badstreams.Entry `json:"streams"`
	Items      []badstreams.Entry `json:"items"`
	Total      int                `json:"total"`
	Page       int                `json:"page"`
	PerPage    int                `json:"perPage"`
	TotalPages int                `json:"totalPages"`
}

func NewBadStreamsHandler(service *badstreams.Service) *BadStreamsHandler {
	return &BadStreamsHandler{service: service}
}

func (h *BadStreamsHandler) List(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil {
		http.Error(w, "bad stream service unavailable", http.StatusServiceUnavailable)
		return
	}
	includeScoped := r.URL.Query().Get("includeScoped") == "true"
	if includeScoped && !auth.IsMaster(r) {
		http.Error(w, "administrator access required", http.StatusForbidden)
		return
	}
	streams := h.service.List()
	if !includeScoped {
		legacy := make([]badstreams.Entry, 0, len(streams))
		for _, entry := range streams {
			if entry.Scope == nil {
				legacy = append(legacy, entry)
			}
		}
		streams = legacy
	}
	streams = filterBadStreams(streams, r.URL.Query().Get("q"))
	page, perPage := parseBadStreamsPage(r)
	total := len(streams)
	totalPages := 0
	if total > 0 {
		totalPages = (total + perPage - 1) / perPage
	}
	if page > totalPages && totalPages > 0 {
		page = totalPages
	}
	start := (page - 1) * perPage
	if start > total {
		start = total
	}
	end := start + perPage
	if end > total {
		end = total
	}
	items := streams[start:end]
	writeBadStreamsJSON(w, badStreamsListResponse{
		Streams:    items,
		Items:      items,
		Total:      total,
		Page:       page,
		PerPage:    perPage,
		TotalPages: totalPages,
	})
}

func (h *BadStreamsHandler) Mark(w http.ResponseWriter, r *http.Request) {
	if !auth.IsMaster(r) {
		http.Error(w, "use the selected release report action", http.StatusForbidden)
		return
	}
	if h == nil || h.service == nil {
		http.Error(w, "bad stream service unavailable", http.StatusServiceUnavailable)
		return
	}
	var req badstreams.MarkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	entry, err := h.service.Mark(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeBadStreamsJSON(w, entry)
}

func (h *BadStreamsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil {
		http.Error(w, "bad stream service unavailable", http.StatusServiceUnavailable)
		return
	}
	id := mux.Vars(r)["id"]
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	if !auth.IsMaster(r) {
		entry := h.service.Get(id)
		profile := strings.TrimSpace(r.URL.Query().Get("profileId"))
		if entry == nil || entry.Scope == nil || profile == "" || entry.Scope.ProfileID != profile || h.users == nil || !h.users.BelongsToAccount(profile, auth.GetAccountID(r)) {
			http.NotFound(w, r)
			return
		}
	}
	existing := h.service.Get(id)
	deleted := h.service.Delete(id)
	if existing != nil && !deleted {
		http.Error(w, "could not remove the report", http.StatusServiceUnavailable)
		return
	}
	writeBadStreamsJSON(w, map[string]interface{}{"deleted": deleted})
}

func (h *BadStreamsHandler) Clear(w http.ResponseWriter, r *http.Request) {
	if !auth.IsMaster(r) {
		http.Error(w, "administrator access required", http.StatusForbidden)
		return
	}
	if h == nil || h.service == nil {
		http.Error(w, "bad stream service unavailable", http.StatusServiceUnavailable)
		return
	}
	count := h.service.Clear()
	writeBadStreamsJSON(w, map[string]interface{}{"deleted": count})
}

func (h *BadStreamsHandler) ShareReport(w http.ResponseWriter, r *http.Request) {
	if !auth.IsMaster(r) {
		http.Error(w, "administrator access required", http.StatusForbidden)
		return
	}
	entry, err := h.service.ShareReport(mux.Vars(r)["id"])
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeBadStreamsJSON(w, entry)
}

func (h *BadStreamsHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func writeBadStreamsJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func parseBadStreamsPage(r *http.Request) (int, int) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(r.URL.Query().Get("perPage"))
	if perPage < 1 {
		perPage = 25
	}
	if perPage > 100 {
		perPage = 100
	}
	return page, perPage
}

func filterBadStreams(streams []badstreams.Entry, query string) []badstreams.Entry {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return streams
	}
	filtered := make([]badstreams.Entry, 0, len(streams))
	for _, stream := range streams {
		if badStreamMatchesQuery(stream, query) {
			filtered = append(filtered, stream)
		}
	}
	return filtered
}

func badStreamMatchesQuery(stream badstreams.Entry, query string) bool {
	fields := []string{
		stream.ReleaseName,
		stream.NormalizedReleaseName,
		stream.ServiceType,
		stream.Provider,
		stream.SourcePath,
		stream.Reason,
		stream.ID,
	}
	if stream.Scope != nil {
		fields = append(fields, stream.Scope.TitleID, stream.Scope.Kind, stream.Scope.ProfileID, stream.Scope.AudioLanguage, stream.Scope.ClientID)
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}
