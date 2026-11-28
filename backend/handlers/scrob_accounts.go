package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/services/scrob"
)

type scrobAccountResponse struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	OwnerAccountID string `json:"ownerAccountId,omitempty"`
	BaseURL        string `json:"baseUrl"`
	Username       string `json:"username,omitempty"`
	HasAPIKey      bool   `json:"hasApiKey"`
	HasPassword    bool   `json:"hasPassword"`
	HasTOTPSecret  bool   `json:"hasTotpSecret"`
}

func publicScrobAccount(account config.ScrobAccount) scrobAccountResponse {
	return scrobAccountResponse{ID: account.ID, Name: account.Name, OwnerAccountID: account.OwnerAccountID, BaseURL: account.BaseURL, Username: account.Username, HasAPIKey: account.APIKey != "", HasPassword: account.Password != "", HasTOTPSecret: account.TOTPSecret != ""}
}

func validateScrobBaseURL(raw string) (string, bool) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	return raw, err == nil && u.Hostname() != "" && (u.Scheme == "http" || u.Scheme == "https") && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

func (h *AdminUIHandler) GetScrobAccounts(w http.ResponseWriter, r *http.Request) {
	settings, err := h.configManager.Load()
	if err != nil {
		http.Error(w, "Failed to load settings", http.StatusInternalServerError)
		return
	}
	result := make([]scrobAccountResponse, 0, len(settings.Scrob.Accounts))
	for _, account := range settings.Scrob.Accounts {
		if h.canAccessOwnedIntegration(r, account.OwnerAccountID, h.usersService.GetUsersByScrobAccountID(account.ID)) {
			result = append(result, publicScrobAccount(account))
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (h *AdminUIHandler) CreateScrobAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"name"`
		BaseURL    string `json:"baseUrl"`
		APIKey     string `json:"apiKey"`
		Username   string `json:"username"`
		Password   string `json:"password"`
		TOTPSecret string `json:"totpSecret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	baseURL, ok := validateScrobBaseURL(req.BaseURL)
	if !ok || strings.TrimSpace(req.APIKey) == "" {
		http.Error(w, "A valid Scrob URL and API key are required", http.StatusBadRequest)
		return
	}
	account := config.ScrobAccount{ID: uuid.New().String(), Name: strings.TrimSpace(req.Name), BaseURL: baseURL, APIKey: strings.TrimSpace(req.APIKey), Username: strings.TrimSpace(req.Username), Password: req.Password, TOTPSecret: strings.TrimSpace(req.TOTPSecret)}
	if account.Name == "" {
		account.Name = "Scrob Account"
	}
	if isAdmin, ownerID, _, _ := h.getPageRoleInfo(r); !isAdmin {
		account.OwnerAccountID = ownerID
	}
	if !updateSettingsPlain(w, h.configManager, "Failed to load settings", "Failed to save settings", func(settings *config.Settings) error {
		settings.Scrob.Accounts = append(settings.Scrob.Accounts, account)
		return nil
	}) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(publicScrobAccount(account))
}

func (h *AdminUIHandler) UpdateScrobAccount(w http.ResponseWriter, r *http.Request) {
	accountID := mux.Vars(r)["accountID"]
	var req struct {
		Name       *string `json:"name"`
		BaseURL    *string `json:"baseUrl"`
		APIKey     *string `json:"apiKey"`
		Username   *string `json:"username"`
		Password   *string `json:"password"`
		TOTPSecret *string `json:"totpSecret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	var updated config.ScrobAccount
	if !updateSettingsPlain(w, h.configManager, "Failed to load settings", "Failed to save settings", func(settings *config.Settings) error {
		account := settings.Scrob.GetAccountByID(accountID)
		if account == nil || !h.canAccessOwnedIntegration(r, account.OwnerAccountID, h.usersService.GetUsersByScrobAccountID(accountID)) {
			http.Error(w, "Account not found", http.StatusNotFound)
			return errSettingsUpdateAborted
		}
		if req.Name != nil {
			account.Name = strings.TrimSpace(*req.Name)
		}
		if req.BaseURL != nil {
			value, ok := validateScrobBaseURL(*req.BaseURL)
			if !ok {
				http.Error(w, "Invalid Scrob URL", http.StatusBadRequest)
				return errSettingsUpdateAborted
			}
			account.BaseURL = value
		}
		if req.APIKey != nil && *req.APIKey != "" {
			account.APIKey = strings.TrimSpace(*req.APIKey)
		}
		if req.Username != nil {
			account.Username = strings.TrimSpace(*req.Username)
		}
		if req.Password != nil && *req.Password != "" {
			account.Password = *req.Password
		}
		if req.TOTPSecret != nil && *req.TOTPSecret != "" {
			account.TOTPSecret = strings.TrimSpace(*req.TOTPSecret)
		}
		updated = *account
		return nil
	}) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(publicScrobAccount(updated))
}

func (h *AdminUIHandler) DeleteScrobAccount(w http.ResponseWriter, r *http.Request) {
	accountID := mux.Vars(r)["accountID"]
	if !updateSettingsPlain(w, h.configManager, "Failed to load settings", "Failed to save settings", func(settings *config.Settings) error {
		account := settings.Scrob.GetAccountByID(accountID)
		if account == nil || !h.canAccessOwnedIntegration(r, account.OwnerAccountID, h.usersService.GetUsersByScrobAccountID(accountID)) {
			http.Error(w, "Account not found", http.StatusNotFound)
			return errSettingsUpdateAborted
		}
		settings.Scrob.RemoveAccount(accountID)
		for _, user := range h.usersService.GetUsersByScrobAccountID(accountID) {
			_, _ = h.usersService.ClearScrobAccountID(user.ID)
		}
		return nil
	}) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *AdminUIHandler) TestScrobAccount(w http.ResponseWriter, r *http.Request) {
	accountID := mux.Vars(r)["accountID"]
	settings, err := h.configManager.Load()
	if err != nil {
		http.Error(w, "Failed to load settings", http.StatusInternalServerError)
		return
	}
	account := settings.Scrob.GetAccountByID(accountID)
	if account == nil || !h.canAccessOwnedIntegration(r, account.OwnerAccountID, h.usersService.GetUsersByScrobAccountID(accountID)) {
		http.Error(w, "Account not found", http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	err = scrob.NewClient().TestConnection(ctx, account.BaseURL, account.APIKey)
	message := "Connected to Scrob history API (pull authentication verified)"
	if err == nil && account.Username != "" && account.Password != "" {
		code := ""
		if account.TOTPSecret != "" {
			code, err = scrob.GenerateTOTPCode(account.TOTPSecret, time.Now().UTC())
		}
		if err == nil {
			_, err = scrob.NewClient().Login(ctx, account.BaseURL, account.APIKey, account.Username, account.Password, code)
			if err == nil {
				message = "Connected to Scrob and verified outbound login"
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "message": message})
}
