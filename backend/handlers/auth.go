package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"novastream/models"
	"novastream/services/accounts"
	"novastream/services/sessions"
)

// AuthHandler handles authentication endpoints.
type AuthHandler struct {
	accounts *accounts.Service
	sessions *sessions.Service
}

// NewAuthHandler creates a new auth handler.
func NewAuthHandler(accountsSvc *accounts.Service, sessionsSvc *sessions.Service) *AuthHandler {
	return &AuthHandler{
		accounts: accountsSvc,
		sessions: sessionsSvc,
	}
}

// LoginRequest represents the login request body.
type LoginRequest struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	RememberMe bool   `json:"rememberMe"`
}

// LoginResponse represents the login response.
type LoginResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
	AccountID string `json:"accountId"`
	Username  string `json:"username"`
	IsMaster  bool   `json:"isMaster"`
}

// AccountResponse represents account info response.
type AccountResponse struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	IsMaster bool   `json:"isMaster"`
}

// Login authenticates a user and returns a session token.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error": "invalid request body"}`, http.StatusBadRequest)
		return
	}

	account, err := h.accounts.Authenticate(req.Username, req.Password)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid username or password"})
		return
	}

	// Create session
	userAgent := r.Header.Get("User-Agent")
	ipAddress := getClientIPAddress(r)
	var session models.Session
	if req.RememberMe {
		session, err = h.sessions.CreatePersistent(account.ID, account.IsMaster, userAgent, ipAddress)
	} else {
		session, err = h.sessions.Create(account.ID, account.IsMaster, userAgent, ipAddress)
	}
	if err != nil {
		http.Error(w, `{"error": "failed to create session"}`, http.StatusInternalServerError)
		return
	}

	resp := LoginResponse{
		Token:     session.Token,
		ExpiresAt: session.ExpiresAt.Format("2006-01-02T15:04:05Z"),
		AccountID: account.ID,
		Username:  account.Username,
		IsMaster:  account.IsMaster,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// Logout invalidates the current session.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	token := extractBearerToken(r)
	if token == "" {
		http.Error(w, `{"error": "no session token"}`, http.StatusBadRequest)
		return
	}

	if err := h.sessions.Revoke(token); err != nil {
		// Session not found is OK - might already be expired
		if err != sessions.ErrSessionNotFound {
			http.Error(w, `{"error": "failed to revoke session"}`, http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "logged out"})
}

// Me returns the current authenticated account info.
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	token := extractBearerToken(r)
	if token == "" {
		http.Error(w, `{"error": "not authenticated"}`, http.StatusUnauthorized)
		return
	}

	session, err := h.sessions.Validate(token)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid or expired session"})
		return
	}

	account, ok := h.accounts.Get(session.AccountID)
	if !ok {
		http.Error(w, `{"error": "account not found"}`, http.StatusNotFound)
		return
	}

	resp := AccountResponse{
		ID:       account.ID,
		Username: account.Username,
		IsMaster: account.IsMaster,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// Refresh extends the session expiration.
func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	token := extractBearerToken(r)
	if token == "" {
		http.Error(w, `{"error": "not authenticated"}`, http.StatusUnauthorized)
		return
	}

	session, err := h.sessions.Refresh(token)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid or expired session"})
		return
	}

	account, ok := h.accounts.Get(session.AccountID)
	if !ok {
		http.Error(w, `{"error": "account not found"}`, http.StatusNotFound)
		return
	}

	resp := LoginResponse{
		Token:     session.Token,
		ExpiresAt: session.ExpiresAt.Format("2006-01-02T15:04:05Z"),
		AccountID: account.ID,
		Username:  account.Username,
		IsMaster:  account.IsMaster,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ChangePasswordRequest represents password change request.
type ChangePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// ChangePassword changes the current account's password.
func (h *AuthHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	token := extractBearerToken(r)
	if token == "" {
		http.Error(w, `{"error": "not authenticated"}`, http.StatusUnauthorized)
		return
	}

	session, err := h.sessions.Validate(token)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid or expired session"})
		return
	}

	var req ChangePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error": "invalid request body"}`, http.StatusBadRequest)
		return
	}

	// Verify current password
	account, ok := h.accounts.Get(session.AccountID)
	if !ok {
		http.Error(w, `{"error": "account not found"}`, http.StatusNotFound)
		return
	}

	if _, err := h.accounts.Authenticate(account.Username, req.CurrentPassword); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "current password is incorrect"})
		return
	}

	// Update password
	if err := h.accounts.UpdatePassword(session.AccountID, req.NewPassword); err != nil {
		http.Error(w, `{"error": "failed to update password"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "password changed"})
}

// Options handles CORS preflight requests.
func (h *AuthHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// extractBearerToken extracts the bearer token from the Authorization header.
func extractBearerToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return ""
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}

	return strings.TrimSpace(parts[1])
}

// getClientIPAddress extracts the client IP address from the request.
func getClientIPAddress(r *http.Request) string {
	// Check X-Forwarded-For header first
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		// Take the first IP in the chain
		if idx := strings.Index(xff, ","); idx != -1 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}

	// Check X-Real-IP header
	xri := r.Header.Get("X-Real-IP")
	if xri != "" {
		return strings.TrimSpace(xri)
	}

	// Fall back to RemoteAddr
	addr := r.RemoteAddr
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		return addr[:idx]
	}
	return addr
}
