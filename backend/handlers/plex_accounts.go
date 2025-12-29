package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/services/accounts"
	"novastream/services/plex"
	"novastream/services/users"
)

// PlexAccountsHandler handles Plex account management API endpoints.
type PlexAccountsHandler struct {
	configManager   *config.Manager
	plexClient      *plex.Client
	usersService    *users.Service
	accountsService *accounts.Service
}

// NewPlexAccountsHandler creates a new Plex accounts handler.
func NewPlexAccountsHandler(configManager *config.Manager, plexClient *plex.Client, usersService *users.Service, accountsService *accounts.Service) *PlexAccountsHandler {
	return &PlexAccountsHandler{
		configManager:   configManager,
		plexClient:      plexClient,
		usersService:    usersService,
		accountsService: accountsService,
	}
}

// PlexAccountResponse is the JSON response for a Plex account.
type PlexAccountResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Username  string `json:"username,omitempty"`
	Connected bool   `json:"connected"`
}

// ListAccounts returns registered Plex accounts.
// For master accounts, returns all accounts.
// For non-master accounts, only returns accounts they own.
// GET /admin/api/plex/accounts
func (h *PlexAccountsHandler) ListAccounts(w http.ResponseWriter, r *http.Request) {
	settings, err := h.configManager.Load()
	if err != nil {
		jsonError(w, "Failed to load settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Check if the logged-in user is a master account
	session := adminSessionFromContext(r.Context())
	var isMaster bool
	var sessionAccountID string
	if session != nil {
		sessionAccountID = session.AccountID
		if loginAccount, ok := h.accountsService.Get(session.AccountID); ok {
			isMaster = loginAccount.IsMaster
		}
	}

	accounts := make([]PlexAccountResponse, 0, len(settings.Plex.Accounts))
	for _, acc := range settings.Plex.Accounts {
		// Master accounts see all; non-master only see their own accounts
		if isMaster || acc.OwnerAccountID == sessionAccountID {
			accounts = append(accounts, PlexAccountResponse{
				ID:        acc.ID,
				Name:      acc.Name,
				Username:  acc.Username,
				Connected: acc.AuthToken != "",
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"accounts": accounts,
	})
}

// CreateAccount creates a new Plex account entry.
// POST /admin/api/plex/accounts
func (h *PlexAccountsHandler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	session := adminSessionFromContext(r.Context())
	if session == nil {
		jsonError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Name string `json:"name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	settings, err := h.configManager.Load()
	if err != nil {
		jsonError(w, "Failed to load settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Plex Account"
	}

	newAccount := config.PlexAccount{
		ID:             uuid.NewString(),
		Name:           name,
		OwnerAccountID: session.AccountID,
	}

	settings.Plex.Accounts = append(settings.Plex.Accounts, newAccount)

	if err := h.configManager.Save(settings); err != nil {
		jsonError(w, "Failed to save settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"account": PlexAccountResponse{
			ID:        newAccount.ID,
			Name:      newAccount.Name,
			Connected: false,
		},
	})
}

// DeleteAccount removes a Plex account.
// DELETE /admin/api/plex/accounts/{accountID}
func (h *PlexAccountsHandler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	session := adminSessionFromContext(r.Context())
	if session == nil {
		jsonError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	accountID := mux.Vars(r)["accountID"]
	if accountID == "" {
		jsonError(w, "Account ID required", http.StatusBadRequest)
		return
	}

	settings, err := h.configManager.Load()
	if err != nil {
		jsonError(w, "Failed to load settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Verify ownership
	account := settings.Plex.GetAccountByID(accountID)
	if account == nil {
		jsonError(w, "Account not found", http.StatusNotFound)
		return
	}

	loginAccount, ok := h.accountsService.Get(session.AccountID)
	if !ok || (!loginAccount.IsMaster && account.OwnerAccountID != session.AccountID) {
		jsonError(w, "Not authorized to delete this account", http.StatusForbidden)
		return
	}

	if !settings.Plex.RemoveAccount(accountID) {
		jsonError(w, "Account not found", http.StatusNotFound)
		return
	}

	// Clear any profile associations with this account
	allUsers := h.usersService.List()
	for _, user := range allUsers {
		if user.PlexAccountID == accountID {
			h.usersService.ClearPlexAccountID(user.ID)
		}
	}

	if err := h.configManager.Save(settings); err != nil {
		jsonError(w, "Failed to save settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}

// CreatePIN initiates Plex PIN auth flow for an account.
// POST /admin/api/plex/accounts/{accountID}/pin
func (h *PlexAccountsHandler) CreatePIN(w http.ResponseWriter, r *http.Request) {
	session := adminSessionFromContext(r.Context())
	if session == nil {
		jsonError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	accountID := mux.Vars(r)["accountID"]
	if accountID == "" {
		jsonError(w, "Account ID required", http.StatusBadRequest)
		return
	}

	settings, err := h.configManager.Load()
	if err != nil {
		jsonError(w, "Failed to load settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	account := settings.Plex.GetAccountByID(accountID)
	if account == nil {
		jsonError(w, "Account not found", http.StatusNotFound)
		return
	}

	// Verify ownership
	loginAccount, ok := h.accountsService.Get(session.AccountID)
	if !ok || (!loginAccount.IsMaster && account.OwnerAccountID != session.AccountID) {
		jsonError(w, "Not authorized", http.StatusForbidden)
		return
	}

	if h.plexClient == nil {
		jsonError(w, "Plex client not initialized", http.StatusInternalServerError)
		return
	}

	pin, err := h.plexClient.CreatePIN()
	if err != nil {
		jsonError(w, "Failed to create PIN: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":      pin.ID,
		"code":    pin.Code,
		"authUrl": h.plexClient.GetAuthURL(pin.Code),
	})
}

// CheckPIN polls for Plex auth token for an account.
// GET /admin/api/plex/accounts/{accountID}/pin/{pinID}
func (h *PlexAccountsHandler) CheckPIN(w http.ResponseWriter, r *http.Request) {
	session := adminSessionFromContext(r.Context())
	if session == nil {
		jsonError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	vars := mux.Vars(r)
	accountID := vars["accountID"]
	pinID := vars["pinID"]
	if accountID == "" || pinID == "" {
		jsonError(w, "Account ID and PIN ID required", http.StatusBadRequest)
		return
	}

	settings, err := h.configManager.Load()
	if err != nil {
		jsonError(w, "Failed to load settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	account := settings.Plex.GetAccountByID(accountID)
	if account == nil {
		jsonError(w, "Account not found", http.StatusNotFound)
		return
	}

	// Verify ownership
	loginAccount, ok := h.accountsService.Get(session.AccountID)
	if !ok || (!loginAccount.IsMaster && account.OwnerAccountID != session.AccountID) {
		jsonError(w, "Not authorized", http.StatusForbidden)
		return
	}

	if h.plexClient == nil {
		jsonError(w, "Plex client not initialized", http.StatusInternalServerError)
		return
	}

	// Convert pinID to int
	pinIDInt, err := strconv.Atoi(pinID)
	if err != nil {
		jsonError(w, "Invalid PIN ID", http.StatusBadRequest)
		return
	}

	pinResp, err := h.plexClient.CheckPIN(pinIDInt)
	if err != nil {
		jsonError(w, "Failed to check PIN: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if pinResp.AuthToken == "" {
		// Still pending
		json.NewEncoder(w).Encode(map[string]interface{}{
			"authenticated": false,
		})
		return
	}

	// Token received, update account
	account.AuthToken = pinResp.AuthToken

	// Get username
	if userInfo, err := h.plexClient.GetUserInfo(pinResp.AuthToken); err == nil && userInfo != nil {
		account.Username = userInfo.Username
		if account.Name == "" || account.Name == "Plex Account" {
			account.Name = userInfo.Username
		}
	}

	settings.Plex.UpdateAccount(*account)

	if err := h.configManager.Save(settings); err != nil {
		jsonError(w, "Failed to save token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"authenticated": true,
		"username":      account.Username,
	})
}

// Disconnect removes auth token from a Plex account.
// POST /admin/api/plex/accounts/{accountID}/disconnect
func (h *PlexAccountsHandler) Disconnect(w http.ResponseWriter, r *http.Request) {
	session := adminSessionFromContext(r.Context())
	if session == nil {
		jsonError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	accountID := mux.Vars(r)["accountID"]
	if accountID == "" {
		jsonError(w, "Account ID required", http.StatusBadRequest)
		return
	}

	settings, err := h.configManager.Load()
	if err != nil {
		jsonError(w, "Failed to load settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	account := settings.Plex.GetAccountByID(accountID)
	if account == nil {
		jsonError(w, "Account not found", http.StatusNotFound)
		return
	}

	// Verify ownership
	loginAccount, ok := h.accountsService.Get(session.AccountID)
	if !ok || (!loginAccount.IsMaster && account.OwnerAccountID != session.AccountID) {
		jsonError(w, "Not authorized", http.StatusForbidden)
		return
	}

	account.AuthToken = ""
	account.Username = ""
	settings.Plex.UpdateAccount(*account)

	if err := h.configManager.Save(settings); err != nil {
		jsonError(w, "Failed to save settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}
