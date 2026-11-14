package handlers

import (
	"encoding/json"
	"github.com/gorilla/mux"
	"io"
	"net/http"
	"novastream/internal/apierror"
	"novastream/models"
	"strings"
)

func (h *UsersHandler) SetKidsLiveMaxAge(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(mux.Vars(r)["userID"])
	if !h.canConfigureKidsProfile(r, id) {
		apierror.Write(w, http.StatusNotFound, "invalid_request", "profile not found")
		return
	}
	var body struct {
		MaxAge json.RawMessage `json:"maxAge"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		apierror.Write(w, http.StatusBadRequest, "invalid_request", "invalid live age setting")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		apierror.Write(w, http.StatusBadRequest, "invalid_request", "invalid live age setting")
		return
	}
	var maxAge *int
	if len(body.MaxAge) == 0 || json.Unmarshal(body.MaxAge, &maxAge) != nil {
		apierror.Write(w, http.StatusBadRequest, "invalid_request", "maxAge is required")
		return
	}
	if maxAge != nil && *maxAge != 0 && *maxAge != 6 && *maxAge != 12 && *maxAge != 16 && *maxAge != 18 {
		apierror.Write(w, http.StatusBadRequest, "invalid_request", "invalid live age")
		return
	}
	if !h.parentAuthorization.Require(w, r) {
		return
	}
	setter, ok := h.Service.(interface {
		SetKidsLiveMaxAge(string, *int) (models.User, error)
	})
	if !ok {
		apierror.Write(w, http.StatusServiceUnavailable, "invalid_request", "live age settings unavailable")
		return
	}
	user, err := setter.SetKidsLiveMaxAge(id, maxAge)
	if err != nil {
		apierror.Write(w, http.StatusInternalServerError, "invalid_request", "could not save live age")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(user)
}
