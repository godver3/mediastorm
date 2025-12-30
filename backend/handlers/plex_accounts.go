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

	// Get user info (username and ID for filtering watch history)
	if userInfo, err := h.plexClient.GetUserInfo(pinResp.AuthToken); err == nil && userInfo != nil {
		account.Username = userInfo.Username
		account.UserID = userInfo.ID
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

// GetHomeUsers returns the list of users in the Plex Home for an account.
// GET /admin/api/plex/accounts/{accountID}/users
func (h *PlexAccountsHandler) GetHomeUsers(w http.ResponseWriter, r *http.Request) {
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

	if account.AuthToken == "" {
		jsonError(w, "Plex account not connected", http.StatusBadRequest)
		return
	}

	if h.plexClient == nil {
		jsonError(w, "Plex client not initialized", http.StatusInternalServerError)
		return
	}

	users, err := h.plexClient.GetHomeUsers(account.AuthToken)
	if err != nil {
		jsonError(w, "Failed to get home users: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"users": users,
		"count": len(users),
	})
}

// PlexHistoryItemResponse is the JSON response for a Plex watch history item.
type PlexHistoryItemResponse struct {
	RatingKey       string            `json:"ratingKey"`
	Title           string            `json:"title"`
	Type            string            `json:"type"` // "movie" or "episode"
	Year            int               `json:"year,omitempty"`
	SeriesTitle     string            `json:"seriesTitle,omitempty"`
	Season          int               `json:"season,omitempty"`
	Episode         int               `json:"episode,omitempty"`
	ViewedAt        int64             `json:"viewedAt"`
	ExternalIDs     map[string]string `json:"externalIds,omitempty"`
	ServerName      string            `json:"serverName,omitempty"`
}

// GetHistory fetches watch history from connected Plex servers.
// GET /admin/api/plex/accounts/{accountID}/history
func (h *PlexAccountsHandler) GetHistory(w http.ResponseWriter, r *http.Request) {
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

	if account.AuthToken == "" {
		jsonError(w, "Plex account not connected", http.StatusBadRequest)
		return
	}

	if h.plexClient == nil {
		jsonError(w, "Plex client not initialized", http.StatusInternalServerError)
		return
	}

	// Parse limit from query params (default 500)
	limit := 500
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 5000 {
			limit = parsed
		}
	}

	// Parse plexUserId filter from query params (0 = no filter, show all users)
	plexUserID := 0
	if uid := r.URL.Query().Get("plexUserId"); uid != "" {
		if parsed, err := strconv.Atoi(uid); err == nil && parsed > 0 {
			plexUserID = parsed
		}
	}

	// Fetch watch history from all connected servers, optionally filtered by Plex user
	historyItems, err := h.plexClient.GetAllWatchHistory(account.AuthToken, limit, plexUserID)
	if err != nil {
		jsonError(w, "Failed to fetch watch history: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Convert to response format
	items := make([]PlexHistoryItemResponse, 0, len(historyItems))
	for _, item := range historyItems {
		respItem := PlexHistoryItemResponse{
			RatingKey:   item.RatingKey,
			Title:       item.Title,
			Type:        item.Type,
			Year:        item.Year,
			ViewedAt:    item.ViewedAt,
			ExternalIDs: item.ExternalIDs,
			ServerName:  item.ServerName,
		}

		// For episodes, set series info
		if item.Type == "episode" {
			respItem.SeriesTitle = item.GrandparentTitle
			respItem.Season = item.ParentIndex
			respItem.Episode = item.Index
		}

		items = append(items, respItem)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"items": items,
		"count": len(items),
	})
}

// GetServers returns the list of online owned Plex servers for an account.
// GET /admin/api/plex/accounts/{accountID}/servers
func (h *PlexAccountsHandler) GetServers(w http.ResponseWriter, r *http.Request) {
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

	if account.AuthToken == "" {
		jsonError(w, "Plex account not connected", http.StatusBadRequest)
		return
	}

	if h.plexClient == nil {
		jsonError(w, "Plex client not initialized", http.StatusInternalServerError)
		return
	}

	servers, err := h.plexClient.GetOwnedServers(account.AuthToken)
	if err != nil {
		jsonError(w, "Failed to get servers: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Convert to simple response
	type serverInfo struct {
		Name     string `json:"name"`
		Platform string `json:"platform"`
		Online   bool   `json:"online"`
	}

	result := make([]serverInfo, 0, len(servers))
	for _, s := range servers {
		result = append(result, serverInfo{
			Name:     s.Name,
			Platform: s.Platform,
			Online:   s.Presence,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"servers": result,
		"count":   len(result),
	})
}

// PlexWatchlistItemResponse is the JSON response for a Plex watchlist item.
type PlexWatchlistItemResponse struct {
	RatingKey   string            `json:"ratingKey"`
	Title       string            `json:"title"`
	Type        string            `json:"type"` // "movie" or "show"
	Year        int               `json:"year,omitempty"`
	PosterURL   string            `json:"posterUrl,omitempty"`
	ExternalIDs map[string]string `json:"externalIds,omitempty"`
}

// GetWatchlist fetches watchlist from Plex for a specific account.
// GET /admin/api/plex/accounts/{accountID}/watchlist
func (h *PlexAccountsHandler) GetWatchlist(w http.ResponseWriter, r *http.Request) {
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

	if account.AuthToken == "" {
		jsonError(w, "Plex account not connected", http.StatusBadRequest)
		return
	}

	if h.plexClient == nil {
		jsonError(w, "Plex client not initialized", http.StatusInternalServerError)
		return
	}

	// Fetch watchlist
	watchlistItems, err := h.plexClient.GetWatchlist(account.AuthToken)
	if err != nil {
		jsonError(w, "Failed to fetch watchlist: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Convert to response format with external IDs
	items := make([]PlexWatchlistItemResponse, 0, len(watchlistItems))
	for _, item := range watchlistItems {
		// Try to get external IDs from item details
		externalIDs, _ := h.plexClient.GetItemDetails(account.AuthToken, item.RatingKey)
		if externalIDs == nil {
			externalIDs = plex.ParseGUID(item.GUID)
		}

		respItem := PlexWatchlistItemResponse{
			RatingKey:   item.RatingKey,
			Title:       item.Title,
			Type:        plex.NormalizeMediaType(item.Type),
			Year:        item.Year,
			PosterURL:   item.Thumb,
			ExternalIDs: externalIDs,
		}

		items = append(items, respItem)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"items": items,
		"count": len(items),
	})
}
