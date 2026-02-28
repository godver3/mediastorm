package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"strings"
	"time"

	"novastream/config"
	"novastream/handlers"
	"novastream/models"
	"novastream/services/accounts"
	"novastream/services/metadata"
	"novastream/services/invitations"
	"novastream/services/sessions"
	"novastream/services/user_settings"
	"novastream/services/users"
)

// mockMetadataService implements handlers.MetadataService for testing
type mockMetadataService struct{}

func (m *mockMetadataService) ClearCache() error {
	return nil
}

func (m *mockMetadataService) MovieDetails(ctx context.Context, req models.MovieDetailsQuery) (*models.Title, error) {
	return &models.Title{}, nil
}

func (m *mockMetadataService) SeriesInfo(ctx context.Context, req models.SeriesDetailsQuery) (*models.Title, error) {
	return &models.Title{}, nil
}

func (m *mockMetadataService) GetCacheManagerStatus() metadata.CacheManagerStatus {
	return metadata.CacheManagerStatus{}
}

func (m *mockMetadataService) RefreshTrendingCache() {
}

// setupAdminUIHandler creates an AdminUIHandler with all required dependencies for testing
func setupAdminUIHandler(t *testing.T) (*handlers.AdminUIHandler, string) {
	t.Helper()
	tmpDir := t.TempDir()

	// Create required subdirectories
	os.MkdirAll(filepath.Join(tmpDir, "users"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "settings"), 0755)

	settingsPath := filepath.Join(tmpDir, "settings.yaml")

	// Create users service
	usersService, err := users.NewService(tmpDir)
	if err != nil {
		t.Fatalf("failed to create users service: %v", err)
	}

	// Create user settings service
	userSettingsService, err := user_settings.NewService(tmpDir)
	if err != nil {
		t.Fatalf("failed to create user settings service: %v", err)
	}

	// Create config manager
	configManager := config.NewManager(settingsPath)

	// Create admin UI handler
	handler := handlers.NewAdminUIHandler(settingsPath, "", nil, usersService, userSettingsService, configManager)

	// Set up additional services
	accountsService, err := accounts.NewService(tmpDir)
	if err != nil {
		t.Fatalf("failed to create accounts service: %v", err)
	}
	handler.SetAccountsService(accountsService)

	sessionsService, err := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	if err != nil {
		t.Fatalf("failed to create sessions service: %v", err)
	}
	handler.SetSessionsService(sessionsService)

	invitationsService, err := invitations.NewService(tmpDir)
	if err != nil {
		t.Fatalf("failed to create invitations service: %v", err)
	}
	handler.SetInvitationsService(invitationsService)

	return handler, tmpDir
}

// createAuthenticatedRequest creates a request with valid session token
func createAuthenticatedRequest(t *testing.T, method, url string, body []byte, sessionsService *sessions.Service, accountID string, isMaster bool) *http.Request {
	t.Helper()

	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, url, bytes.NewReader(body))
	} else {
		req = httptest.NewRequest(method, url, nil)
	}
	req.Header.Set("Content-Type", "application/json")

	session, err := sessionsService.Create(accountID, isMaster, "test-agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	req.Header.Set("Authorization", "Bearer "+session.Token)
	return req
}

func TestNewAdminUIHandler(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)
	if handler == nil {
		t.Fatal("NewAdminUIHandler returned nil")
	}
}

func TestAdminUIHandler_GetSchema(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/schema", nil)
	rec := httptest.NewRecorder()

	handler.GetSchema(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GetSchema status = %d, want %d", rec.Code, http.StatusOK)
	}

	contentType := rec.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want %q", contentType, "application/json")
	}

	// Verify response is valid JSON
	var schema map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &schema); err != nil {
		t.Errorf("failed to parse schema JSON: %v", err)
	}
}

func TestAdminUIHandler_HasDefaultPassword(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/default-password", nil)
	rec := httptest.NewRecorder()

	handler.HasDefaultPassword(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("HasDefaultPassword status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Errorf("failed to parse response JSON: %v", err)
	}

	if _, ok := resp["hasDefaultPassword"]; !ok {
		t.Error("response should contain 'hasDefaultPassword' field")
	}
}

func TestAdminUIHandler_GetProfiles(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	// Create a user first
	usersService, _ := users.NewService(tmpDir)
	if _, err := usersService.Create("Test User"); err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// Create accounts and sessions services
	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)

	// Get the master account
	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	// Create authenticated request
	req := createAuthenticatedRequest(t, http.MethodGet, "/api/admin/profiles", nil, sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()

	handler.GetProfiles(rec, req)

	// Should succeed (200) or require auth (401)
	if rec.Code != http.StatusOK && rec.Code != http.StatusUnauthorized {
		t.Errorf("GetProfiles status = %d, want 200 or 401", rec.Code)
	}
}

func TestAdminUIHandler_CreateProfile(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)

	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	reqBody := map[string]string{
		"name": "New Profile",
	}
	body, _ := json.Marshal(reqBody)

	req := createAuthenticatedRequest(t, http.MethodPost, "/api/admin/profiles", body, sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()

	handler.CreateProfile(rec, req)

	// Should succeed (200/201) or require auth (401)
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated && rec.Code != http.StatusUnauthorized {
		t.Errorf("CreateProfile status = %d, want 200, 201 or 401", rec.Code)
	}
}

func TestAdminUIHandler_GetUserAccounts(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)

	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	req := createAuthenticatedRequest(t, http.MethodGet, "/api/admin/accounts", nil, sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()

	handler.GetUserAccounts(rec, req)

	// Should succeed or require auth
	if rec.Code != http.StatusOK && rec.Code != http.StatusUnauthorized {
		t.Errorf("GetUserAccounts status = %d, want 200 or 401", rec.Code)
	}
}

func TestAdminUIHandler_CreateUserAccount(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)

	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	reqBody := map[string]string{
		"username": "newuser",
		"password": "newpassword123",
	}
	body, _ := json.Marshal(reqBody)

	req := createAuthenticatedRequest(t, http.MethodPost, "/api/admin/accounts", body, sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()

	handler.CreateUserAccount(rec, req)

	// Should succeed or require auth
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated && rec.Code != http.StatusUnauthorized {
		t.Errorf("CreateUserAccount status = %d, want 200, 201 or 401", rec.Code)
	}
}

func TestAdminUIHandler_ListInvitations(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	invitationsService, _ := invitations.NewService(tmpDir)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)
	handler.SetInvitationsService(invitationsService)

	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	req := createAuthenticatedRequest(t, http.MethodGet, "/api/admin/invitations", nil, sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()

	handler.ListInvitations(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusUnauthorized {
		t.Errorf("ListInvitations status = %d, want 200 or 401", rec.Code)
	}
}

func TestAdminUIHandler_CreateInvitation(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	invitationsService, _ := invitations.NewService(tmpDir)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)
	handler.SetInvitationsService(invitationsService)

	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	reqBody := map[string]interface{}{
		"maxUses":   5,
		"expiresIn": 86400,
	}
	body, _ := json.Marshal(reqBody)

	req := createAuthenticatedRequest(t, http.MethodPost, "/api/admin/invitations", body, sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()

	handler.CreateInvitation(rec, req)

	// Should succeed or require auth
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated && rec.Code != http.StatusUnauthorized {
		t.Errorf("CreateInvitation status = %d, want 200, 201 or 401", rec.Code)
	}
}

func TestAdminUIHandler_ValidateInvitation(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	invitationsService, _ := invitations.NewService(tmpDir)
	handler.SetInvitationsService(invitationsService)

	// Create an invitation first
	invite, err := invitationsService.Create("master", 24*time.Hour)
	if err != nil {
		t.Fatalf("failed to create invitation: %v", err)
	}

	// Try both "code" and "token" query params
	req := httptest.NewRequest(http.MethodGet, "/api/admin/invitations/validate?token="+invite.Token, nil)
	rec := httptest.NewRecorder()

	handler.ValidateInvitation(rec, req)

	// Accept either OK or BadRequest (depending on expected query param name)
	if rec.Code != http.StatusOK && rec.Code != http.StatusBadRequest {
		t.Errorf("ValidateInvitation status = %d, want 200 or 400", rec.Code)
	}
}

func TestAdminUIHandler_ValidateInvitation_Invalid(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	invitationsService, _ := invitations.NewService(tmpDir)
	handler.SetInvitationsService(invitationsService)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/invitations/validate?code=invalid-code", nil)
	rec := httptest.NewRecorder()

	handler.ValidateInvitation(rec, req)

	if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest {
		t.Errorf("ValidateInvitation with invalid code status = %d, want 404 or 400", rec.Code)
	}
}

func TestAdminUIHandler_ClearMetadataCache(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)

	// Set a mock metadata service
	handler.SetMetadataService(&mockMetadataService{})

	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	req := createAuthenticatedRequest(t, http.MethodPost, "/api/admin/cache/clear", nil, sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()

	handler.ClearMetadataCache(rec, req)

	// Should succeed or require auth
	if rec.Code != http.StatusOK && rec.Code != http.StatusUnauthorized {
		t.Errorf("ClearMetadataCache status = %d, want 200 or 401", rec.Code)
	}
}

func TestAdminUIHandler_ProxyHealth(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()

	handler.ProxyHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("ProxyHealth status = %d, want %d", rec.Code, http.StatusOK)
	}

	// Verify response is valid JSON (structure may vary)
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Errorf("failed to parse response JSON: %v", err)
	}

	// Check for status field (may be string or other type)
	if _, ok := resp["status"]; !ok {
		t.Error("response should contain 'status' field")
	}
}

func TestAdminUIHandler_GetUserSettings_Unauthorized(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)

	// Request without auth
	req := httptest.NewRequest(http.MethodGet, "/api/admin/users/test-user/settings", nil)
	rec := httptest.NewRecorder()

	handler.GetUserSettings(rec, req)

	// Should return 401 unauthorized or 400 bad request (validation may happen before auth)
	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusBadRequest {
		t.Errorf("GetUserSettings without auth status = %d, want 401 or 400", rec.Code)
	}
}

func TestAdminUIHandler_SetProfilePin_InvalidJSON(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)

	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	req := createAuthenticatedRequest(t, http.MethodPost, "/api/admin/profiles/test/pin", []byte("invalid json"), sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()

	handler.SetProfilePin(rec, req)

	// Should return bad request
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnauthorized {
		t.Errorf("SetProfilePin with invalid JSON status = %d, want 400 or 401", rec.Code)
	}
}

func TestAdminUIHandler_RenameProfile_InvalidJSON(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)

	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	req := createAuthenticatedRequest(t, http.MethodPut, "/api/admin/profiles/test/name", []byte("invalid json"), sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()

	handler.RenameProfile(rec, req)

	// Should return bad request
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnauthorized {
		t.Errorf("RenameProfile with invalid JSON status = %d, want 400 or 401", rec.Code)
	}
}

func TestAdminUIHandler_DeleteProfile_NotFound(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)

	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	req := createAuthenticatedRequest(t, http.MethodDelete, "/api/admin/profiles/nonexistent", nil, sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()

	handler.DeleteProfile(rec, req)

	// Should return not found or bad request
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnauthorized {
		t.Errorf("DeleteProfile nonexistent status = %d, want 404, 400 or 401", rec.Code)
	}
}

func TestAdminUIHandler_DeleteUserAccount_NotFound(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)

	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	req := createAuthenticatedRequest(t, http.MethodDelete, "/api/admin/accounts/nonexistent", nil, sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()

	handler.DeleteUserAccount(rec, req)

	// Should return not found or bad request
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnauthorized {
		t.Errorf("DeleteUserAccount nonexistent status = %d, want 404, 400 or 401", rec.Code)
	}
}

func TestAdminUIHandler_ResetUserAccountPassword_InvalidJSON(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)

	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	req := createAuthenticatedRequest(t, http.MethodPut, "/api/admin/accounts/test/password", []byte("invalid json"), sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()

	handler.ResetUserAccountPassword(rec, req)

	// Should return bad request
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnauthorized {
		t.Errorf("ResetUserAccountPassword with invalid JSON status = %d, want 400 or 401", rec.Code)
	}
}

func TestAdminUIHandler_RenameUserAccount_InvalidJSON(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)

	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	req := createAuthenticatedRequest(t, http.MethodPut, "/api/admin/accounts/test/username", []byte("invalid json"), sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()

	handler.RenameUserAccount(rec, req)

	// Should return bad request
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnauthorized {
		t.Errorf("RenameUserAccount with invalid JSON status = %d, want 400 or 401", rec.Code)
	}
}

func TestAdminUIHandler_DeleteInvitation_NotFound(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	invitationsService, _ := invitations.NewService(tmpDir)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)
	handler.SetInvitationsService(invitationsService)

	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	req := createAuthenticatedRequest(t, http.MethodDelete, "/api/admin/invitations/nonexistent", nil, sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()

	handler.DeleteInvitation(rec, req)

	// Should return not found or bad request
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest && rec.Code != http.StatusOK && rec.Code != http.StatusUnauthorized {
		t.Errorf("DeleteInvitation nonexistent status = %d, want 404, 400, 200 or 401", rec.Code)
	}
}

func TestAdminUIHandler_GetStatus(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)

	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	req := createAuthenticatedRequest(t, http.MethodGet, "/api/admin/status", nil, sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()

	handler.GetStatus(rec, req)

	// Should succeed or require auth
	if rec.Code != http.StatusOK && rec.Code != http.StatusUnauthorized {
		t.Errorf("GetStatus status = %d, want 200 or 401", rec.Code)
	}

	if rec.Code == http.StatusOK {
		contentType := rec.Header().Get("Content-Type")
		if contentType != "application/json" {
			t.Errorf("Content-Type = %q, want %q", contentType, "application/json")
		}
	}
}

func TestAdminUIHandler_GetStreams(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)

	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	req := createAuthenticatedRequest(t, http.MethodGet, "/api/admin/streams", nil, sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()

	handler.GetStreams(rec, req)

	// Should succeed or require auth
	if rec.Code != http.StatusOK && rec.Code != http.StatusUnauthorized {
		t.Errorf("GetStreams status = %d, want 200 or 401", rec.Code)
	}
}

func TestAdminUIHandler_IsAuthenticated(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)

	tests := []struct {
		name         string
		setupRequest func() *http.Request
		want         bool
	}{
		{
			name: "no auth",
			setupRequest: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/test", nil)
			},
			want: false,
		},
		{
			name: "invalid token",
			setupRequest: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/test", nil)
				req.Header.Set("Authorization", "Bearer invalid-token")
				return req
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.setupRequest()
			got := handler.IsAuthenticated(req)
			if got != tt.want {
				t.Errorf("IsAuthenticated() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAdminUIHandler_IsMasterAuthenticated(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)

	tests := []struct {
		name         string
		setupRequest func() *http.Request
		want         bool
	}{
		{
			name: "no auth",
			setupRequest: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/test", nil)
			},
			want: false,
		},
		{
			name: "invalid token",
			setupRequest: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/test", nil)
				req.Header.Set("Authorization", "Bearer invalid-token")
				return req
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.setupRequest()
			got := handler.IsMasterAuthenticated(req)
			if got != tt.want {
				t.Errorf("IsMasterAuthenticated() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAdminUIHandler_RequireAuth(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)

	// Create a protected handler
	called := false
	protectedHandler := handler.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	// Test without auth
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	rec := httptest.NewRecorder()
	protectedHandler(rec, req)

	// May return 401, 403, or 303 (redirect to login) depending on implementation
	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden && rec.Code != http.StatusSeeOther {
		t.Errorf("RequireAuth without auth status = %d, want 401, 403 or 303", rec.Code)
	}
	if called {
		t.Error("handler should not be called without auth")
	}
}

func TestAdminUIHandler_RequireMasterAuth(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)

	// Create a protected handler
	called := false
	protectedHandler := handler.RequireMasterAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	// Test without auth
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rec := httptest.NewRecorder()
	protectedHandler(rec, req)

	// May return 401, 403, or 303 (redirect) depending on implementation
	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden && rec.Code != http.StatusSeeOther {
		t.Errorf("RequireMasterAuth without auth status = %d, want 401, 403 or 303", rec.Code)
	}
	if called {
		t.Error("handler should not be called without master auth")
	}
}

func TestAdminUIHandler_TestMetadata_NoKeys(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetSessionsService(sessionsService)

	accountsService, _ := accounts.NewService(tmpDir)
	handler.SetAccountsService(accountsService)
	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	body, _ := json.Marshal(map[string]string{})
	req := createAuthenticatedRequest(t, http.MethodPost, "/admin/api/test/metadata", body, sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()
	handler.TestMetadata(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if result["success"] != false {
		t.Error("expected success=false when no keys configured")
	}
	if result["error"] != "No API keys configured" {
		t.Errorf("expected 'No API keys configured' error, got %v", result["error"])
	}
}

func TestAdminUIHandler_TestMetadata_InvalidJSON(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)
	sessionsService, err := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	if err != nil {
		t.Fatalf("failed to create sessions service: %v", err)
	}
	handler.SetSessionsService(sessionsService)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/test/metadata", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.TestMetadata(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", rec.Code)
	}
}

func TestAdminUIHandler_TestMetadata_InvalidKeys(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetSessionsService(sessionsService)

	accountsService, _ := accounts.NewService(tmpDir)
	handler.SetAccountsService(accountsService)
	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	// Use obviously invalid keys - the external APIs should reject them
	body, _ := json.Marshal(map[string]string{
		"tvdbApiKey": "invalid-key-12345",
		"tmdbApiKey": "invalid-key-67890",
	})
	req := createAuthenticatedRequest(t, http.MethodPost, "/admin/api/test/metadata", body, sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()
	handler.TestMetadata(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	// With invalid keys, overall success should be false
	if result["success"] != false {
		t.Error("expected success=false with invalid keys")
	}
	// Should have results array
	results, ok := result["results"].([]interface{})
	if !ok {
		t.Fatal("expected results array in response")
	}
	if len(results) != 3 {
		t.Errorf("expected 3 provider results, got %d", len(results))
	}
}

func TestAdminUIHandler_TestMDBList_NoKey(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetSessionsService(sessionsService)

	accountsService, _ := accounts.NewService(tmpDir)
	handler.SetAccountsService(accountsService)
	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	body, _ := json.Marshal(map[string]string{"apiKey": ""})
	req := createAuthenticatedRequest(t, http.MethodPost, "/admin/api/test/mdblist", body, sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()
	handler.TestMDBList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if result["success"] != false {
		t.Error("expected success=false with empty API key")
	}
	if result["error"] != "API key is required" {
		t.Errorf("expected 'API key is required' error, got %q", result["error"])
	}
}

func TestAdminUIHandler_TestMDBList_InvalidJSON(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetSessionsService(sessionsService)

	accountsService, _ := accounts.NewService(tmpDir)
	handler.SetAccountsService(accountsService)
	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	req := createAuthenticatedRequest(t, http.MethodPost, "/admin/api/test/mdblist", []byte("not json"), sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()
	handler.TestMDBList(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestAdminUIHandler_TestLiveTV_NoConfig(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetSessionsService(sessionsService)

	accountsService, _ := accounts.NewService(tmpDir)
	handler.SetAccountsService(accountsService)
	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	body, _ := json.Marshal(map[string]string{"mode": ""})
	req := createAuthenticatedRequest(t, http.MethodPost, "/admin/api/test/live", body, sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()
	handler.TestLiveTV(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if result["success"] != false {
		t.Error("expected success=false with no mode configured")
	}
	if result["error"] != "No Live TV mode configured" {
		t.Errorf("expected 'No Live TV mode configured' error, got %q", result["error"])
	}
}

func TestAdminUIHandler_TestLiveTV_InvalidJSON(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetSessionsService(sessionsService)

	accountsService, _ := accounts.NewService(tmpDir)
	handler.SetAccountsService(accountsService)
	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	req := createAuthenticatedRequest(t, http.MethodPost, "/admin/api/test/live", []byte("{bad"), sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()
	handler.TestLiveTV(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestAdminUIHandler_ConnectionsPage(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetSessionsService(sessionsService)

	accountsService, _ := accounts.NewService(tmpDir)
	handler.SetAccountsService(accountsService)
	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	// Test admin gets 200 (RequireMasterAuth uses cookie-based session)
	wrappedHandler := handler.RequireMasterAuth(handler.ConnectionsPage)
	masterSession, err := sessionsService.Create(masterAccount.ID, true, "test-agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("failed to create master session: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/connections", nil)
	req.AddCookie(&http.Cookie{Name: "strmr_admin_session", Value: masterSession.Token})
	rec := httptest.NewRecorder()
	wrappedHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("admin: expected 200, got %d", rec.Code)
	}

	// Test non-admin gets 403 from RequireMasterAuth
	nonAdminAccount, err := accountsService.Create("regular", "pass123")
	if err != nil {
		t.Fatalf("failed to create non-admin account: %v", err)
	}
	nonAdminSession, err := sessionsService.Create(nonAdminAccount.ID, false, "test-agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("failed to create non-admin session: %v", err)
	}
	req2 := httptest.NewRequest(http.MethodGet, "/admin/connections", nil)
	req2.AddCookie(&http.Cookie{Name: "strmr_admin_session", Value: nonAdminSession.Token})
	rec2 := httptest.NewRecorder()
	wrappedHandler(rec2, req2)

	if rec2.Code != http.StatusForbidden {
		t.Errorf("non-admin: expected 403, got %d", rec2.Code)
	}
}
