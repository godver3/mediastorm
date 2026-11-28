package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/internal/auth"
	"novastream/services/jellyfin"
)

// JellyfinAccountsHandler handles Jellyfin account management API endpoints.
type JellyfinAccountsHandler struct {
	configManager  *config.Manager
	jellyfinClient *jellyfin.Client
}

func canAccessJellyfinAccount(r *http.Request, account *config.JellyfinAccount) bool {
	if account == nil {
		return false
	}
	if auth.IsMaster(r) {
		return true
	}
	accountID := auth.GetAccountID(r)
	if accountID == "" {
		return true
	}
	return account.OwnerAccountID != "" && account.OwnerAccountID == accountID
}

// NewJellyfinAccountsHandler creates a new Jellyfin accounts handler.
func NewJellyfinAccountsHandler(configManager *config.Manager, jellyfinClient *jellyfin.Client) *JellyfinAccountsHandler {
	return &JellyfinAccountsHandler{
		configManager:  configManager,
		jellyfinClient: jellyfinClient,
	}
}

// JellyfinAccountResponse is the JSON response for a Jellyfin account.
type JellyfinAccountResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ServerURL string `json:"serverUrl"`
	Username  string `json:"username,omitempty"`
	Connected bool   `json:"connected"`
}

// ListAccounts returns registered Jellyfin accounts.
// GET /admin/api/jellyfin/accounts
func (h *JellyfinAccountsHandler) ListAccounts(w http.ResponseWriter, r *http.Request) {
	settings, err := h.configManager.Load()
	if err != nil {
		jsonError(w, "Failed to load settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	accounts := make([]JellyfinAccountResponse, 0, len(settings.Jellyfin.Accounts))
	for _, acc := range settings.Jellyfin.Accounts {
		if !canAccessJellyfinAccount(r, &acc) {
			continue
		}
		accounts = append(accounts, JellyfinAccountResponse{
			ID:        acc.ID,
			Name:      acc.Name,
			ServerURL: acc.ServerURL,
			Username:  acc.Username,
			Connected: acc.Token != "",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"accounts": accounts,
	})
}

// CreateAccount creates a new Jellyfin account by authenticating with username/password.
// POST /admin/api/jellyfin/accounts
func (h *JellyfinAccountsHandler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		ServerURL string `json:"serverUrl"`
		Username  string `json:"username"`
		Password  string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	req.ServerURL = strings.TrimSpace(req.ServerURL)
	req.Username = strings.TrimSpace(req.Username)

	if req.ServerURL == "" || req.Username == "" {
		jsonError(w, "Server URL and username are required", http.StatusBadRequest)
		return
	}
	normalizedServerURL, err := jellyfin.NormalizeServerURL(req.ServerURL)
	if err != nil {
		jsonError(w, "Invalid server URL: "+err.Error(), http.StatusBadRequest)
		return
	}
	req.ServerURL = normalizedServerURL

	// Authenticate with Jellyfin
	authResult, err := h.jellyfinClient.Authenticate(req.ServerURL, req.Username, req.Password)
	if err != nil {
		jsonError(w, "Authentication failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = req.Username + " @ " + req.ServerURL
	}

	newAccount := config.JellyfinAccount{
		ID:        uuid.NewString(),
		Name:      name,
		ServerURL: req.ServerURL,
		Token:     authResult.AccessToken,
		UserID:    authResult.User.ID,
		Username:  authResult.User.Name,
	}
	if !auth.IsMaster(r) {
		newAccount.OwnerAccountID = auth.GetAccountID(r)
	}

	if !updateSettings(w, h.configManager, "Failed to save settings: ", func(settings *config.Settings) error {

		settings.Jellyfin.Accounts = append(settings.Jellyfin.Accounts, newAccount)

		return nil
	}) {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"account": JellyfinAccountResponse{
			ID:        newAccount.ID,
			Name:      newAccount.Name,
			ServerURL: newAccount.ServerURL,
			Username:  newAccount.Username,
			Connected: true,
		},
	})
}

// DeleteAccount removes a Jellyfin account.
// DELETE /admin/api/jellyfin/accounts/{accountID}
func (h *JellyfinAccountsHandler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	accountID := mux.Vars(r)["accountID"]
	if accountID == "" {
		jsonError(w, "Account ID required", http.StatusBadRequest)
		return
	}

	if !updateSettings(w, h.configManager, "Failed to save settings: ", func(settings *config.Settings) error {

		account := settings.Jellyfin.GetAccountByID(accountID)
		if !canAccessJellyfinAccount(r, account) {
			jsonError(w, "Account not found", http.StatusNotFound)
			return errSettingsUpdateAborted
		}
		settings.Jellyfin.RemoveAccount(accountID)

		return nil
	}) {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}

// TestConnection tests connectivity to a Jellyfin server for an account.
// POST /admin/api/jellyfin/accounts/{accountID}/test
func (h *JellyfinAccountsHandler) TestConnection(w http.ResponseWriter, r *http.Request) {
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

	account := settings.Jellyfin.GetAccountByID(accountID)
	if !canAccessJellyfinAccount(r, account) {
		jsonError(w, "Account not found", http.StatusNotFound)
		return
	}

	if account.Token == "" {
		jsonError(w, "Account not connected", http.StatusBadRequest)
		return
	}

	if err := h.jellyfinClient.TestConnection(account.ServerURL, account.Token); err != nil {
		jsonError(w, "Connection test failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Connection successful",
	})
}
