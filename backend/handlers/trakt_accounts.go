package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/services/trakt"
	"novastream/services/users"
)

// TraktAccountsHandler handles Trakt account management API endpoints.
type TraktAccountsHandler struct {
	configManager *config.Manager
	traktClient   *trakt.Client
	usersService  *users.Service
}

// NewTraktAccountsHandler creates a new Trakt accounts handler.
func NewTraktAccountsHandler(configManager *config.Manager, traktClient *trakt.Client, usersService *users.Service) *TraktAccountsHandler {
	return &TraktAccountsHandler{
		configManager: configManager,
		traktClient:   traktClient,
		usersService:  usersService,
	}
}

// TraktAccountResponse is the JSON response for a Trakt account.
type TraktAccountResponse struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Username          string   `json:"username,omitempty"`
	Connected         bool     `json:"connected"`
	ScrobblingEnabled bool     `json:"scrobblingEnabled"`
	ExpiresAt         int64    `json:"expiresAt,omitempty"`
	LinkedProfiles    []string `json:"linkedProfiles,omitempty"` // Profile IDs using this account
}

// ListAccounts returns all registered Trakt accounts.
// GET /api/trakt/accounts
func (h *TraktAccountsHandler) ListAccounts(w http.ResponseWriter, r *http.Request) {
	settings, err := h.configManager.Load()
	if err != nil {
		jsonError(w, "Failed to load settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Get all users to build linked profiles mapping
	allUsers := h.usersService.List()
	profilesByAccount := make(map[string][]string)
	for _, user := range allUsers {
		if user.TraktAccountID != "" {
			profilesByAccount[user.TraktAccountID] = append(profilesByAccount[user.TraktAccountID], user.ID)
		}
	}

	accounts := make([]TraktAccountResponse, 0, len(settings.Trakt.Accounts))
	for _, acc := range settings.Trakt.Accounts {
		accounts = append(accounts, TraktAccountResponse{
			ID:                acc.ID,
			Name:              acc.Name,
			Username:          acc.Username,
			Connected:         acc.AccessToken != "",
			ScrobblingEnabled: acc.ScrobblingEnabled,
			ExpiresAt:         acc.ExpiresAt,
			LinkedProfiles:    profilesByAccount[acc.ID],
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"accounts": accounts,
	})
}

// CreateAccount creates a new Trakt account entry.
// POST /api/trakt/accounts
func (h *TraktAccountsHandler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string `json:"name"`
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(req.ClientID) == "" || strings.TrimSpace(req.ClientSecret) == "" {
		jsonError(w, "Client ID and Client Secret are required", http.StatusBadRequest)
		return
	}

	settings, err := h.configManager.Load()
	if err != nil {
		jsonError(w, "Failed to load settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Trakt Account"
	}

	newAccount := config.TraktAccount{
		ID:           uuid.NewString(),
		Name:         name,
		ClientID:     strings.TrimSpace(req.ClientID),
		ClientSecret: strings.TrimSpace(req.ClientSecret),
	}

	settings.Trakt.Accounts = append(settings.Trakt.Accounts, newAccount)

	if err := h.configManager.Save(settings); err != nil {
		jsonError(w, "Failed to save settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"account": TraktAccountResponse{
			ID:                newAccount.ID,
			Name:              newAccount.Name,
			Connected:         false,
			ScrobblingEnabled: false,
		},
	})
}

// GetAccount returns a single Trakt account.
// GET /api/trakt/accounts/{id}
func (h *TraktAccountsHandler) GetAccount(w http.ResponseWriter, r *http.Request) {
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

	account := settings.Trakt.GetAccountByID(accountID)
	if account == nil {
		jsonError(w, "Account not found", http.StatusNotFound)
		return
	}

	// Get linked profiles
	linkedProfiles := h.usersService.GetUsersByTraktAccountID(accountID)
	profileIDs := make([]string, 0, len(linkedProfiles))
	for _, p := range linkedProfiles {
		profileIDs = append(profileIDs, p.ID)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(TraktAccountResponse{
		ID:                account.ID,
		Name:              account.Name,
		Username:          account.Username,
		Connected:         account.AccessToken != "",
		ScrobblingEnabled: account.ScrobblingEnabled,
		ExpiresAt:         account.ExpiresAt,
		LinkedProfiles:    profileIDs,
	})
}

// UpdateAccount updates a Trakt account's settings.
// PATCH /api/trakt/accounts/{id}
func (h *TraktAccountsHandler) UpdateAccount(w http.ResponseWriter, r *http.Request) {
	accountID := mux.Vars(r)["accountID"]
	if accountID == "" {
		jsonError(w, "Account ID required", http.StatusBadRequest)
		return
	}

	var req struct {
		Name              *string `json:"name,omitempty"`
		ClientID          *string `json:"clientId,omitempty"`
		ClientSecret      *string `json:"clientSecret,omitempty"`
		ScrobblingEnabled *bool   `json:"scrobblingEnabled,omitempty"`
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

	account := settings.Trakt.GetAccountByID(accountID)
	if account == nil {
		jsonError(w, "Account not found", http.StatusNotFound)
		return
	}

	if req.Name != nil {
		account.Name = strings.TrimSpace(*req.Name)
	}
	if req.ClientID != nil {
		account.ClientID = strings.TrimSpace(*req.ClientID)
	}
	if req.ClientSecret != nil {
		account.ClientSecret = strings.TrimSpace(*req.ClientSecret)
	}
	if req.ScrobblingEnabled != nil {
		account.ScrobblingEnabled = *req.ScrobblingEnabled
	}

	settings.Trakt.UpdateAccount(*account)

	if err := h.configManager.Save(settings); err != nil {
		jsonError(w, "Failed to save settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}

// DeleteAccount removes a Trakt account.
// DELETE /api/trakt/accounts/{id}
func (h *TraktAccountsHandler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
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

	if !settings.Trakt.RemoveAccount(accountID) {
		jsonError(w, "Account not found", http.StatusNotFound)
		return
	}

	// Clear any profile associations with this account
	allUsers := h.usersService.List()
	for _, user := range allUsers {
		if user.TraktAccountID == accountID {
			h.usersService.ClearTraktAccountID(user.ID)
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

// StartAuth initiates the Trakt device code OAuth flow for an account.
// POST /api/trakt/accounts/{id}/auth/start
func (h *TraktAccountsHandler) StartAuth(w http.ResponseWriter, r *http.Request) {
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

	account := settings.Trakt.GetAccountByID(accountID)
	if account == nil {
		jsonError(w, "Account not found", http.StatusNotFound)
		return
	}

	if account.ClientID == "" || account.ClientSecret == "" {
		jsonError(w, "Account credentials not configured", http.StatusBadRequest)
		return
	}

	// Update client with account credentials
	h.traktClient.UpdateCredentials(account.ClientID, account.ClientSecret)

	deviceCode, err := h.traktClient.GetDeviceCode()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"deviceCode":      deviceCode.DeviceCode,
		"userCode":        deviceCode.UserCode,
		"verificationUrl": deviceCode.VerificationURL,
		"expiresIn":       deviceCode.ExpiresIn,
		"interval":        deviceCode.Interval,
	})
}

// CheckAuth polls for Trakt OAuth token for an account.
// GET /api/trakt/accounts/{id}/auth/check/{deviceCode}
func (h *TraktAccountsHandler) CheckAuth(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	accountID := vars["accountID"]
	deviceCode := vars["deviceCode"]
	if accountID == "" || deviceCode == "" {
		jsonError(w, "Account ID and device code required", http.StatusBadRequest)
		return
	}

	settings, err := h.configManager.Load()
	if err != nil {
		jsonError(w, "Failed to load settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	account := settings.Trakt.GetAccountByID(accountID)
	if account == nil {
		jsonError(w, "Account not found", http.StatusNotFound)
		return
	}

	// Update client with account credentials
	h.traktClient.UpdateCredentials(account.ClientID, account.ClientSecret)

	token, err := h.traktClient.PollForToken(deviceCode)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if token == nil {
		// Still pending
		json.NewEncoder(w).Encode(map[string]interface{}{
			"authenticated": false,
			"pending":       true,
		})
		return
	}

	// Token received, update account
	account.AccessToken = token.AccessToken
	account.RefreshToken = token.RefreshToken
	account.ExpiresAt = token.CreatedAt + int64(token.ExpiresIn)

	// Get user profile
	profile, err := h.traktClient.GetUserProfile(token.AccessToken)
	if err == nil && profile != nil {
		account.Username = profile.Username
		if account.Name == "" || account.Name == "Trakt Account" {
			account.Name = profile.Username
		}
	}

	settings.Trakt.UpdateAccount(*account)

	if err := h.configManager.Save(settings); err != nil {
		jsonError(w, "Failed to save token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"authenticated": true,
		"username":      account.Username,
	})
}

// Disconnect removes OAuth tokens from a Trakt account.
// POST /api/trakt/accounts/{id}/disconnect
func (h *TraktAccountsHandler) Disconnect(w http.ResponseWriter, r *http.Request) {
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

	account := settings.Trakt.GetAccountByID(accountID)
	if account == nil {
		jsonError(w, "Account not found", http.StatusNotFound)
		return
	}

	account.AccessToken = ""
	account.RefreshToken = ""
	account.ExpiresAt = 0
	account.Username = ""

	settings.Trakt.UpdateAccount(*account)

	if err := h.configManager.Save(settings); err != nil {
		jsonError(w, "Failed to save settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}

// SetScrobbling enables or disables scrobbling for a Trakt account.
// POST /api/trakt/accounts/{id}/scrobbling
func (h *TraktAccountsHandler) SetScrobbling(w http.ResponseWriter, r *http.Request) {
	accountID := mux.Vars(r)["accountID"]
	if accountID == "" {
		jsonError(w, "Account ID required", http.StatusBadRequest)
		return
	}

	var req struct {
		Enabled bool `json:"enabled"`
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

	account := settings.Trakt.GetAccountByID(accountID)
	if account == nil {
		jsonError(w, "Account not found", http.StatusNotFound)
		return
	}

	account.ScrobblingEnabled = req.Enabled
	settings.Trakt.UpdateAccount(*account)

	if err := h.configManager.Save(settings); err != nil {
		jsonError(w, "Failed to save settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":           true,
		"scrobblingEnabled": req.Enabled,
	})
}

// GetHistory retrieves the watch history for a specific Trakt account.
// GET /api/trakt/accounts/{id}/history
func (h *TraktAccountsHandler) GetHistory(w http.ResponseWriter, r *http.Request) {
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

	account := settings.Trakt.GetAccountByID(accountID)
	if account == nil {
		jsonError(w, "Account not found", http.StatusNotFound)
		return
	}

	accessToken, err := h.ensureValidAccountToken(account)
	if err != nil {
		jsonError(w, "Failed to validate token: "+err.Error(), http.StatusUnauthorized)
		return
	}
	if accessToken == "" {
		jsonError(w, "Account not connected", http.StatusUnauthorized)
		return
	}

	// Update client with account credentials
	h.traktClient.UpdateCredentials(account.ClientID, account.ClientSecret)

	// Check if all items requested
	if r.URL.Query().Get("all") == "true" {
		items, err := h.traktClient.GetAllWatchHistory(accessToken)
		if err != nil {
			jsonError(w, "Failed to fetch history: "+err.Error(), http.StatusInternalServerError)
			return
		}

		normalizedItems := h.normalizeHistoryItems(items)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"items":      normalizedItems,
			"count":      len(normalizedItems),
			"totalCount": len(normalizedItems),
		})
		return
	}

	// Parse pagination params
	page := 1
	limit := 100
	if p := r.URL.Query().Get("page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}

	items, totalCount, err := h.traktClient.GetWatchHistory(accessToken, page, limit, "")
	if err != nil {
		jsonError(w, "Failed to fetch history: "+err.Error(), http.StatusInternalServerError)
		return
	}

	normalizedItems := h.normalizeHistoryItems(items)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"items":      normalizedItems,
		"count":      len(normalizedItems),
		"totalCount": totalCount,
		"page":       page,
		"limit":      limit,
	})
}

// normalizeHistoryItems converts Trakt history items to a normalized format.
func (h *TraktAccountsHandler) normalizeHistoryItems(items []trakt.HistoryItem) []map[string]interface{} {
	normalizedItems := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		normalized := map[string]interface{}{
			"id":        item.ID,
			"type":      trakt.NormalizeMediaType(item.Type),
			"watchedAt": item.WatchedAt,
			"action":    item.Action,
		}

		if item.Movie != nil {
			normalized["title"] = item.Movie.Title
			normalized["year"] = item.Movie.Year
			normalized["externalIds"] = trakt.IDsToMap(item.Movie.IDs)
		} else if item.Episode != nil && item.Show != nil {
			normalized["title"] = fmt.Sprintf("%s - S%02dE%02d - %s", item.Show.Title, item.Episode.Season, item.Episode.Number, item.Episode.Title)
			normalized["seriesTitle"] = item.Show.Title
			normalized["episodeTitle"] = item.Episode.Title
			normalized["season"] = item.Episode.Season
			normalized["episode"] = item.Episode.Number
			normalized["year"] = item.Show.Year
			normalized["externalIds"] = trakt.IDsToMap(item.Show.IDs)
			normalized["episodeIds"] = trakt.IDsToMap(item.Episode.IDs)
		}

		normalizedItems = append(normalizedItems, normalized)
	}
	return normalizedItems
}

// ensureValidAccountToken checks if the Trakt access token is valid and refreshes if needed.
func (h *TraktAccountsHandler) ensureValidAccountToken(account *config.TraktAccount) (string, error) {
	if account.AccessToken == "" {
		return "", nil
	}

	// Update client with account credentials
	h.traktClient.UpdateCredentials(account.ClientID, account.ClientSecret)

	// Check if token is expired or will expire within 1 hour
	if account.ExpiresAt > 0 {
		expiresIn := account.ExpiresAt - time.Now().Unix()
		if expiresIn < 3600 { // Less than 1 hour remaining
			if account.RefreshToken == "" {
				return "", nil
			}

			token, err := h.traktClient.RefreshAccessToken(account.RefreshToken)
			if err != nil {
				return "", err
			}

			// Update account with new tokens
			settings, err := h.configManager.Load()
			if err != nil {
				return "", err
			}

			account.AccessToken = token.AccessToken
			account.RefreshToken = token.RefreshToken
			account.ExpiresAt = token.CreatedAt + int64(token.ExpiresIn)
			settings.Trakt.UpdateAccount(*account)

			if err := h.configManager.Save(settings); err != nil {
				return "", err
			}

			return token.AccessToken, nil
		}
	}

	return account.AccessToken, nil
}

// Helper for JSON error responses
func jsonError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": message,
	})
}
