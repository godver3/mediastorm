package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/gorilla/mux"

	"novastream/models"
	"novastream/services/accounts"
	"novastream/services/sessions"
	"novastream/services/users"
)

// AccountsHandler handles account management endpoints (master only).
type AccountsHandler struct {
	accounts *accounts.Service
	sessions *sessions.Service
	users    *users.Service
}

// NewAccountsHandler creates a new accounts handler.
func NewAccountsHandler(accountsSvc *accounts.Service, sessionsSvc *sessions.Service, usersSvc *users.Service) *AccountsHandler {
	return &AccountsHandler{
		accounts: accountsSvc,
		sessions: sessionsSvc,
		users:    usersSvc,
	}
}

// CreateAccountRequest represents the create account request body.
type CreateAccountRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// ReassignProfileRequest represents the reassign profile request body.
type ReassignProfileRequest struct {
	AccountID string `json:"accountId"`
}

// AccountWithProfiles represents an account with its profiles.
type AccountWithProfiles struct {
	models.Account
	Profiles []models.User `json:"profiles"`
}

// List returns all accounts (master only).
func (h *AccountsHandler) List(w http.ResponseWriter, r *http.Request) {
	accountsList := h.accounts.List()

	// Enrich with profile counts
	result := make([]AccountWithProfiles, 0, len(accountsList))
	for _, acc := range accountsList {
		profiles := h.users.ListForAccount(acc.ID)
		result = append(result, AccountWithProfiles{
			Account:  acc,
			Profiles: profiles,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// Create creates a new account (master only).
func (h *AccountsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req CreateAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error": "invalid request body"}`, http.StatusBadRequest)
		return
	}

	account, err := h.accounts.Create(req.Username, req.Password)
	if err != nil {
		status := http.StatusInternalServerError
		if err == accounts.ErrUsernameExists {
			status = http.StatusConflict
		} else if err == accounts.ErrUsernameRequired || err == accounts.ErrPasswordRequired {
			status = http.StatusBadRequest
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(account)
}

// Get returns a single account by ID (master only).
func (h *AccountsHandler) Get(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	accountID := vars["accountID"]

	account, ok := h.accounts.Get(accountID)
	if !ok {
		http.Error(w, `{"error": "account not found"}`, http.StatusNotFound)
		return
	}

	profiles := h.users.ListForAccount(account.ID)
	result := AccountWithProfiles{
		Account:  account,
		Profiles: profiles,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}


// Rename changes an account's username (master only).
func (h *AccountsHandler) Rename(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	accountID := vars["accountID"]

	var req struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error": "invalid request body"}`, http.StatusBadRequest)
		return
	}

	if err := h.accounts.Rename(accountID, req.Username); err != nil {
		status := http.StatusInternalServerError
		if err == accounts.ErrAccountNotFound {
			status = http.StatusNotFound
		} else if err == accounts.ErrUsernameRequired {
			status = http.StatusBadRequest
		} else if err == accounts.ErrUsernameExists {
			status = http.StatusConflict
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Get the updated account
	account, _ := h.accounts.Get(accountID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(account)
}

// Delete removes an account (master only).
func (h *AccountsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	accountID := vars["accountID"]

	// First, revoke all sessions for this account
	h.sessions.RevokeAllForAccount(accountID)

	if err := h.accounts.Delete(accountID); err != nil {
		status := http.StatusInternalServerError
		if err == accounts.ErrAccountNotFound {
			status = http.StatusNotFound
		} else if err == accounts.ErrCannotDeleteMaster || err == accounts.ErrCannotDeleteLastAcct {
			status = http.StatusForbidden
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ResetPassword resets an account's password (master only).
func (h *AccountsHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	accountID := vars["accountID"]

	var req struct {
		NewPassword string `json:"newPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error": "invalid request body"}`, http.StatusBadRequest)
		return
	}

	if err := h.accounts.UpdatePassword(accountID, req.NewPassword); err != nil {
		status := http.StatusInternalServerError
		if err == accounts.ErrAccountNotFound {
			status = http.StatusNotFound
		} else if err == accounts.ErrPasswordRequired {
			status = http.StatusBadRequest
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Revoke all sessions for this account (force re-login)
	h.sessions.RevokeAllForAccount(accountID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "password reset"})
}

// ReassignProfile moves a profile to a different account (master only).
func (h *AccountsHandler) ReassignProfile(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	profileID := vars["profileID"]

	var req ReassignProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error": "invalid request body"}`, http.StatusBadRequest)
		return
	}

	// Verify target account exists
	if _, ok := h.accounts.Get(req.AccountID); !ok {
		http.Error(w, `{"error": "target account not found"}`, http.StatusNotFound)
		return
	}

	profile, err := h.users.Reassign(profileID, req.AccountID)
	if err != nil {
		status := http.StatusInternalServerError
		if err == users.ErrUserNotFound {
			status = http.StatusNotFound
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(profile)
}

// HasDefaultPassword returns whether the master account has the default password.
func (h *AccountsHandler) HasDefaultPassword(w http.ResponseWriter, r *http.Request) {
	hasDefault := h.accounts.HasDefaultPassword()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"hasDefaultPassword": hasDefault})
}

// Options handles CORS preflight requests.
func (h *AccountsHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}
