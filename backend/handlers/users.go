package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/users"

	"github.com/gorilla/mux"
)


type usersService interface {
	List() []models.User
	ListForAccount(accountID string) []models.User
	Create(name string) (models.User, error)
	CreateForAccount(accountID, name string) (models.User, error)
	BelongsToAccount(profileID, accountID string) bool
	Rename(id, name string) (models.User, error)
	SetColor(id, color string) (models.User, error)
	Delete(id string) error
	Exists(id string) bool
	SetPin(id, pin string) (models.User, error)
	ClearPin(id string) (models.User, error)
	VerifyPin(id, pin string) error
	HasPin(id string) bool
	SetTraktAccountID(id, traktAccountID string) (models.User, error)
	ClearTraktAccountID(id string) (models.User, error)
	SetPlexAccountID(id, plexAccountID string) (models.User, error)
	ClearPlexAccountID(id string) (models.User, error)
	SetKidsProfile(id string, isKids bool) (models.User, error)
}

var _ usersService = (*users.Service)(nil)

type UsersHandler struct {
	Service usersService
}

func NewUsersHandler(service usersService) *UsersHandler {
	return &UsersHandler{Service: service}
}

func (h *UsersHandler) List(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// All accounts (including master) only see their own profiles in the frontend app
	// Admin web UI uses a separate endpoint (/admin/api/profiles) to see all profiles
	accountID := auth.GetAccountID(r)
	json.NewEncoder(w).Encode(h.Service.ListForAccount(accountID))
}

func (h *UsersHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Create profile under the logged-in account
	accountID := auth.GetAccountID(r)
	user, err := h.Service.CreateForAccount(accountID, body.Name)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, users.ErrNameRequired):
			status = http.StatusBadRequest
		case errors.Is(err, users.ErrStorageDirRequired):
			status = http.StatusInternalServerError
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(user)
}

func (h *UsersHandler) Rename(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account
	accountID := auth.GetAccountID(r)
	if !h.Service.BelongsToAccount(id, accountID) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	user, err := h.Service.Rename(id, body.Name)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, users.ErrNameRequired):
			status = http.StatusBadRequest
		case errors.Is(err, users.ErrUserNotFound):
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

func (h *UsersHandler) Delete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account
	accountID := auth.GetAccountID(r)
	if !h.Service.BelongsToAccount(id, accountID) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	if err := h.Service.Delete(id); err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, users.ErrUserNotFound):
			status = http.StatusNotFound
		case strings.Contains(err.Error(), "cannot delete the last user"):
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *UsersHandler) SetColor(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account
	accountID := auth.GetAccountID(r)
	if !h.Service.BelongsToAccount(id, accountID) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	var body struct {
		Color string `json:"color"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	user, err := h.Service.SetColor(id, body.Color)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

func (h *UsersHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// SetPin sets or updates a user's PIN.
func (h *UsersHandler) SetPin(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account
	accountID := auth.GetAccountID(r)
	if !h.Service.BelongsToAccount(id, accountID) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	var body struct {
		Pin string `json:"pin"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	user, err := h.Service.SetPin(id, body.Pin)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, users.ErrUserNotFound):
			status = http.StatusNotFound
		case errors.Is(err, users.ErrPinRequired), errors.Is(err, users.ErrPinTooShort):
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// ClearPin removes a user's PIN.
func (h *UsersHandler) ClearPin(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account
	accountID := auth.GetAccountID(r)
	if !h.Service.BelongsToAccount(id, accountID) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	user, err := h.Service.ClearPin(id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// VerifyPin verifies a user's PIN.
func (h *UsersHandler) VerifyPin(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	var body struct {
		Pin string `json:"pin"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err := h.Service.VerifyPin(id, body.Pin)
	if err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"valid": true})
}

// SetTraktAccount associates a Trakt account with a user profile.
func (h *UsersHandler) SetTraktAccount(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account (skip for master accounts)
	if !auth.IsMaster(r) {
		accountID := auth.GetAccountID(r)
		if !h.Service.BelongsToAccount(id, accountID) {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
	} else if !h.Service.Exists(id) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	var body struct {
		TraktAccountID string `json:"traktAccountId"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	user, err := h.Service.SetTraktAccountID(id, body.TraktAccountID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// ClearTraktAccount removes the Trakt account association from a user profile.
func (h *UsersHandler) ClearTraktAccount(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account (skip for master accounts)
	if !auth.IsMaster(r) {
		accountID := auth.GetAccountID(r)
		if !h.Service.BelongsToAccount(id, accountID) {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
	} else if !h.Service.Exists(id) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	user, err := h.Service.ClearTraktAccountID(id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// SetPlexAccount associates a Plex account with a user profile.
func (h *UsersHandler) SetPlexAccount(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account (skip for master accounts)
	if !auth.IsMaster(r) {
		accountID := auth.GetAccountID(r)
		if !h.Service.BelongsToAccount(id, accountID) {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
	} else if !h.Service.Exists(id) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	var body struct {
		PlexAccountID string `json:"plexAccountId"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	user, err := h.Service.SetPlexAccountID(id, body.PlexAccountID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// ClearPlexAccount removes the Plex account association from a user profile.
func (h *UsersHandler) ClearPlexAccount(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account (skip for master accounts)
	if !auth.IsMaster(r) {
		accountID := auth.GetAccountID(r)
		if !h.Service.BelongsToAccount(id, accountID) {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
	} else if !h.Service.Exists(id) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	user, err := h.Service.ClearPlexAccountID(id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// SetKidsProfile sets or clears the kids profile flag for a user.
func (h *UsersHandler) SetKidsProfile(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["userID"])
	if id == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to the logged-in account
	accountID := auth.GetAccountID(r)
	if !h.Service.BelongsToAccount(id, accountID) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	var body struct {
		IsKidsProfile bool `json:"isKidsProfile"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	user, err := h.Service.SetKidsProfile(id, body.IsKidsProfile)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, users.ErrUserNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}
