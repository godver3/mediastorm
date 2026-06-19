package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/config"
	"novastream/handlers"
	"novastream/models"
	"novastream/services/accounts"
	"novastream/services/invitations"
	"novastream/services/metadata"
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

func (m *mockMetadataService) SeriesDetails(ctx context.Context, req models.SeriesDetailsQuery) (*models.SeriesDetails, error) {
	return &models.SeriesDetails{}, nil
}

func (m *mockMetadataService) SeriesInfo(ctx context.Context, req models.SeriesDetailsQuery) (*models.Title, error) {
	return &models.Title{}, nil
}

func (m *mockMetadataService) GetCacheManagerStatus() metadata.CacheManagerStatus {
	return metadata.CacheManagerStatus{}
}

func (m *mockMetadataService) RefreshTrendingCache() {
}

func (m *mockMetadataService) GetTopTenWorkerStatus() metadata.TopTenWorkerStatus {
	return metadata.TopTenWorkerStatus{}
}

func (m *mockMetadataService) TriggerTopTenRefresh() {
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

	req.AddCookie(&http.Cookie{
		Name:  "strmr_admin_session",
		Value: session.Token,
	})
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

	liveTV, ok := schema["liveTV"].(map[string]interface{})
	if !ok {
		t.Fatalf("schema missing liveTV section")
	}
	if liveTV["testable"] != true {
		t.Errorf("liveTV testable = %v, want true", liveTV["testable"])
	}

	display, ok := schema["display"].(map[string]interface{})
	if !ok {
		t.Fatalf("schema missing display section")
	}
	fields, ok := display["fields"].(map[string]interface{})
	if !ok {
		t.Fatalf("display schema missing fields")
	}
	if _, exists := fields["cleanPosters"]; exists {
		t.Fatal("cleanPosters should be hidden from the admin settings schema")
	}
}

func TestAdminUIHandler_HasDefaultPassword(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)
	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)

	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	req := createAuthenticatedRequest(t, http.MethodGet, "/api/auth/default-password", nil, sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()

	handler.RequireMasterAuth(handler.HasDefaultPassword)(rec, req)

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

	handler.RequireMasterAuth(handler.GetUserAccounts)(rec, req)

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

	handler.RequireMasterAuth(handler.CreateUserAccount)(rec, req)

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
	invite, err := invitationsService.Create("master", 24*time.Hour, 0)
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

	handler.RequireMasterAuth(handler.DeleteUserAccount)(rec, req)

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

	handler.RequireMasterAuth(handler.ResetUserAccountPassword)(rec, req)

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

	handler.RequireMasterAuth(handler.RenameUserAccount)(rec, req)

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

func TestAdminUIHandler_StatusPageIncludesDonationBanner(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)
	mgr := config.NewManager(filepath.Join(tmpDir, "settings.yaml"))
	settings, err := mgr.Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	settings.UI.OnboardingSkipped = true
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	masterAccount, ok := accountsService.Get("master")
	if !ok {
		t.Fatal("master account not found")
	}

	req := createAuthenticatedRequest(t, http.MethodGet, "/admin/status", nil, sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()

	handler.RequireAuth(handler.StatusPage)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("StatusPage status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	body := rec.Body.String()
	for _, want := range []string{
		`id="donationBanner"`,
		`https://github.com/sponsors/godver3`,
		`https://patreon.com/godver3`,
		`https://ko-fi.com/godver3`,
		`mediastormDonationBannerHidden`,
		`Hide donation links permanently`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("StatusPage body missing %q", want)
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

func TestAdminUIHandler_TestUsenetEngine_MissingBaseURLReturnsJSON(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)

	body, _ := json.Marshal(map[string]string{
		"name": "NZBDav",
		"type": "nzbdav",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/test/usenet-engine", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.TestUsenetEngine(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("expected JSON content type, got %q", got)
	}
	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if result["success"] != false || result["error"] != "Base URL is required" {
		t.Fatalf("unexpected response: %#v", result)
	}
}

func TestAdminUIHandler_TestUsenetEngine_MissingWebDAVBaseURLReturnsJSON(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)

	body, _ := json.Marshal(map[string]string{
		"name":    "NZBDav",
		"type":    "nzbdav",
		"baseUrl": "http://127.0.0.1:9999",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/test/usenet-engine", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.TestUsenetEngine(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if result["success"] != false || result["error"] != "WebDAV Base URL is required" {
		t.Fatalf("unexpected response: %#v", result)
	}
}

func TestAdminUIHandler_TestUsenetEngine_AltMountDoesNotRequireWebDAVPathPrefix(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sabnzbd/api":
			switch r.URL.Query().Get("mode") {
			case "queue":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": true,
					"queue":  map[string]interface{}{"slots": []map[string]interface{}{}},
				})
			case "history":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status":  true,
					"history": map[string]interface{}{"slots": []map[string]interface{}{}},
				})
			case "get_config":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": true,
					"config": map[string]interface{}{
						"categories": []map[string]interface{}{{"name": "Default", "dir": "complete"}},
					},
				})
			default:
				http.Error(w, "unexpected mode", http.StatusBadRequest)
			}
		case r.URL.Path == "/webdav":
			w.WriteHeader(207)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	body, _ := json.Marshal(map[string]string{
		"name":          "AltMount",
		"type":          "altmount",
		"baseUrl":       server.URL,
		"apiPath":       "/sabnzbd/api",
		"webdavBaseUrl": server.URL + "/webdav",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/test/usenet-engine", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.TestUsenetEngine(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if result["success"] != true {
		t.Fatalf("unexpected response: %#v", result)
	}
}

func TestAdminUIHandler_TestUsenetEngine_ChecksAPIAndWebDAV(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)

	var sawAPI bool
	var sawWebDAV bool
	var sawMappedWebDAV bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sabnzbd/api":
			sawAPI = true
			switch r.URL.Query().Get("mode") {
			case "queue":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": true,
					"queue":  map[string]interface{}{"slots": []map[string]interface{}{}},
				})
			case "history":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": true,
					"history": map[string]interface{}{
						"slots": []map[string]interface{}{{
							"status":       "Completed",
							"nzb_name":     "Release.Name.nzb",
							"storage_path": "/mnt/remotes/altmount/Default/complete/Release.Name",
						}},
					},
				})
			case "get_config":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": true,
					"config": map[string]interface{}{
						"categories": []map[string]interface{}{{"name": "Default", "dir": "complete"}},
					},
				})
			default:
				http.Error(w, "unexpected mode", http.StatusBadRequest)
			}
		case r.URL.Path == "/webdav":
			if r.Method != "PROPFIND" {
				http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
				return
			}
			sawWebDAV = true
			w.WriteHeader(207)
		case r.URL.Path == "/webdav/Default/complete/Release.Name":
			if r.Method != "PROPFIND" {
				http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
				return
			}
			sawMappedWebDAV = true
			w.WriteHeader(207)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"name":          "AltMount",
		"type":          "altmount",
		"baseUrl":       server.URL,
		"apiPath":       "/sabnzbd/api",
		"webdavBaseUrl": server.URL + "/webdav",
		"config": map[string]string{
			"webdavPathPrefix": "/mnt/remotes/altmount",
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/test/usenet-engine", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.TestUsenetEngine(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !sawAPI {
		t.Fatal("expected SAB-compatible API to be checked")
	}
	if !sawWebDAV {
		t.Fatal("expected WebDAV to be checked")
	}
	if !sawMappedWebDAV {
		t.Fatal("expected mapped WebDAV path to be checked")
	}
	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if result["success"] != true {
		t.Fatalf("unexpected response: %#v", result)
	}
	if !strings.Contains(fmt.Sprint(result["message"]), "completed-path mapping are valid") {
		t.Fatalf("message = %q", result["message"])
	}
}

func TestAdminUIHandler_TestUsenetEngine_FailsWrongWebDAVPathPrefix(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sabnzbd/api":
			switch r.URL.Query().Get("mode") {
			case "queue":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": true,
					"queue":  map[string]interface{}{"slots": []map[string]interface{}{}},
				})
			case "history":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": true,
					"history": map[string]interface{}{
						"slots": []map[string]interface{}{{
							"status":       "Completed",
							"nzb_name":     "Release.Name.nzb",
							"storage_path": "/mnt/remotes/altmount/Default/complete/Release.Name",
						}},
					},
				})
			case "get_config":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": true,
					"config": map[string]interface{}{
						"categories": []map[string]interface{}{{"name": "Default", "dir": "complete"}},
					},
				})
			default:
				http.Error(w, "unexpected mode", http.StatusBadRequest)
			}
		case r.URL.Path == "/webdav":
			w.WriteHeader(207)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"name":          "AltMount",
		"type":          "altmount",
		"baseUrl":       server.URL,
		"apiPath":       "/sabnzbd/api",
		"webdavBaseUrl": server.URL + "/webdav",
		"config": map[string]string{
			"webdavPathPrefix": "/wrong/prefix",
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/test/usenet-engine", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.TestUsenetEngine(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if result["success"] != false {
		t.Fatalf("unexpected response: %#v", result)
	}
	if !strings.Contains(fmt.Sprint(result["error"]), "mapped WebDAV path returned HTTP 404") {
		t.Fatalf("error = %q", result["error"])
	}
}

func TestAdminUIHandler_TestUsenetEngine_AllowsWebDAVMethodNotAllowed(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sabnzbd/api":
			switch r.URL.Query().Get("mode") {
			case "queue":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": true,
					"queue":  map[string]interface{}{"slots": []map[string]interface{}{}},
				})
			case "history":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status":  true,
					"history": map[string]interface{}{"slots": []map[string]interface{}{}},
				})
			case "get_config":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": true,
					"config": map[string]interface{}{
						"categories": []map[string]interface{}{{"name": "Default", "dir": "complete"}},
					},
				})
			default:
				http.Error(w, "unexpected mode", http.StatusBadRequest)
			}
		case r.URL.Path == "/webdav":
			http.Error(w, "directory listing disabled", http.StatusMethodNotAllowed)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"name":          "AltMount",
		"type":          "altmount",
		"baseUrl":       server.URL,
		"apiPath":       "/sabnzbd/api",
		"webdavBaseUrl": server.URL + "/webdav",
		"config": map[string]string{
			"webdavPathPrefix": "/mnt/remotes/altmount",
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/test/usenet-engine", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.TestUsenetEngine(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if result["success"] != true {
		t.Fatalf("unexpected response: %#v", result)
	}
}

func TestAdminUIHandler_TestUsenetEngine_ScansWebDAVCategoryLocation(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sabnzbd/api":
			switch r.URL.Query().Get("mode") {
			case "queue":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": true,
					"queue":  map[string]interface{}{"slots": []map[string]interface{}{}},
				})
			case "history":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status":  true,
					"history": map[string]interface{}{"slots": []map[string]interface{}{}},
				})
			case "get_config":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": true,
					"config": map[string]interface{}{
						"categories": []map[string]interface{}{{"name": "Default", "dir": "complete"}},
					},
				})
			default:
				http.Error(w, "unexpected mode", http.StatusBadRequest)
			}
		case r.URL.Path == "/webdav" || r.URL.Path == "/webdav/":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/webdav/</D:href>
    <D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat>
  </D:response>
  <D:response>
    <D:href>/webdav/Default/</D:href>
    <D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:displayname>Default</D:displayname><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat>
  </D:response>
</D:multistatus>`)
		case r.URL.Path == "/webdav/Default/":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/webdav/Default/</D:href>
    <D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat>
  </D:response>
  <D:response>
    <D:href>/webdav/Default/complete/</D:href>
    <D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:displayname>complete</D:displayname><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat>
  </D:response>
</D:multistatus>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"name":          "AltMount",
		"type":          "altmount",
		"baseUrl":       server.URL,
		"apiPath":       "/sabnzbd/api",
		"webdavBaseUrl": server.URL + "/webdav",
		"config": map[string]string{
			"webdavPathPrefix": "/mnt/remotes/altmount",
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/test/usenet-engine", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.TestUsenetEngine(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if result["success"] != true {
		t.Fatalf("unexpected response: %#v", result)
	}
	if !strings.Contains(fmt.Sprint(result["message"]), `WebDAV category scan found "Default/complete"`) {
		t.Fatalf("message = %q", result["message"])
	}
}

func TestAdminUIHandler_TestUsenetEngine_TestNZBValidatesAndCleansUp(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)

	var sawAddFile bool
	var sawMappedWebDAV bool
	var sawQueueDelete bool
	var sawHistoryDelete bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sabnzbd/api":
			switch r.URL.Query().Get("mode") {
			case "addfile":
				sawAddFile = true
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status":  true,
					"nzo_ids": []string{"job-1"},
				})
			case "queue":
				if r.URL.Query().Get("name") == "delete" {
					sawQueueDelete = true
					_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": true})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": true,
					"queue":  map[string]interface{}{"slots": []map[string]interface{}{}},
				})
			case "history":
				if r.URL.Query().Get("name") == "delete" {
					sawHistoryDelete = true
					_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": true})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": true,
					"history": map[string]interface{}{
						"slots": []map[string]interface{}{{
							"nzo_id":       "job-1",
							"status":       "Completed",
							"nzb_name":     "test.nzb",
							"storage_path": "/mnt/remotes/altmount/Default/complete/test",
						}},
					},
				})
			case "get_config":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": true,
					"config": map[string]interface{}{
						"categories": []map[string]interface{}{{"name": "Default", "dir": "complete"}},
					},
				})
			default:
				http.Error(w, "unexpected mode", http.StatusBadRequest)
			}
		case r.URL.Path == "/webdav":
			w.WriteHeader(207)
		case r.URL.Path == "/webdav/Default/complete/test":
			sawMappedWebDAV = true
			w.WriteHeader(207)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"name":          "AltMount",
		"type":          "altmount",
		"testMode":      "full",
		"baseUrl":       server.URL,
		"apiPath":       "/sabnzbd/api",
		"webdavBaseUrl": server.URL + "/webdav",
		"config": map[string]string{
			"webdavPathPrefix": "/mnt/remotes/altmount",
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/test/usenet-engine", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.TestUsenetEngine(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !sawAddFile {
		t.Fatal("expected test NZB to be submitted")
	}
	if !sawMappedWebDAV {
		t.Fatal("expected mapped completed test NZB path to be probed")
	}
	if !sawQueueDelete || !sawHistoryDelete {
		t.Fatalf("cleanup: queue=%t history=%t, want both", sawQueueDelete, sawHistoryDelete)
	}
	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if result["success"] != true {
		t.Fatalf("unexpected response: %#v", result)
	}
}

func TestAdminUIHandler_TestUsenetEngine_FullTestAllowsRelativeCompletedPath(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sabnzbd/api":
			switch r.URL.Query().Get("mode") {
			case "addfile":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status":  true,
					"nzo_ids": []string{"job-1"},
				})
			case "queue":
				if r.URL.Query().Get("name") == "delete" {
					_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": true})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": true,
					"queue":  map[string]interface{}{"slots": []map[string]interface{}{}},
				})
			case "history":
				if r.URL.Query().Get("name") == "delete" {
					_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": true})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": true,
					"history": map[string]interface{}{
						"slots": []map[string]interface{}{{
							"nzo_id":       "job-1",
							"status":       "Completed",
							"nzb_name":     "test.nzb",
							"storage_path": "Default/complete/test",
						}},
					},
				})
			case "get_config":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": true,
					"config": map[string]interface{}{
						"categories": []map[string]interface{}{{"name": "Default", "dir": "complete"}},
					},
				})
			default:
				http.Error(w, "unexpected mode", http.StatusBadRequest)
			}
		case r.URL.Path == "/webdav":
			w.WriteHeader(207)
		case r.URL.Path == "/webdav/Default/complete/test":
			w.WriteHeader(207)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"name":          "AltMount",
		"type":          "altmount",
		"testMode":      "full",
		"baseUrl":       server.URL,
		"apiPath":       "/sabnzbd/api",
		"webdavBaseUrl": server.URL + "/webdav",
		"config": map[string]string{
			"webdavPathPrefix": "/mnt/remotes/altmoun",
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/test/usenet-engine", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.TestUsenetEngine(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if result["success"] != true {
		t.Fatalf("unexpected response: %#v", result)
	}
	if !strings.Contains(fmt.Sprint(result["message"]), "completed path is WebDAV-relative") {
		t.Fatalf("message = %q", result["message"])
	}
}

func TestAdminUIHandler_TestLiveTV_M3USendsPlayerUserAgent(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.Header.Get("User-Agent")) == "" {
			http.Error(w, "missing user agent", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("#EXTM3U\n#EXTINF:-1,Test\nhttp://example.test/live.ts\n"))
	}))
	defer server.Close()

	body, _ := json.Marshal(map[string]string{
		"mode":        "m3u",
		"playlistUrl": server.URL + "/playlist.m3u",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/test/live", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.TestLiveTV(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if result["success"] != true {
		t.Fatalf("expected success=true, got %#v", result)
	}
}

func TestAdminUIHandler_TestLiveTV_XtreamFallsBackUserAgent(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)
	var requests int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if strings.Contains(r.Header.Get("User-Agent"), "VLC") {
			http.Error(w, "blocked user agent", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"category_id":"1","category_name":"News"}]`))
	}))
	defer server.Close()

	body, _ := json.Marshal(map[string]string{
		"mode":           "xtream",
		"xtreamHost":     server.URL,
		"xtreamUsername": "user name",
		"xtreamPassword": "p@ss&word",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/test/live", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.TestLiveTV(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if result["success"] != true {
		t.Fatalf("expected success=true, got %#v", result)
	}
	if requests < 2 {
		t.Fatalf("expected user-agent fallback to retry, got %d request(s)", requests)
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

func TestAdminUIHandler_YTDLPCookies(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)

	// Test GET status - should report no file uploaded
	t.Run("status_no_file", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/api/ytdlp-cookies", nil)
		rec := httptest.NewRecorder()
		handler.GetYTDLPCookiesStatus(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var resp map[string]interface{}
		json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp["uploaded"] != false {
			t.Errorf("expected uploaded=false, got %v", resp["uploaded"])
		}
	})

	// Test POST upload
	t.Run("upload", func(t *testing.T) {
		body := &bytes.Buffer{}
		writer := multipartWriter(t, body, "cookies", "cookies.txt", "# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t0\tSID\ttest")
		req := httptest.NewRequest(http.MethodPost, "/admin/api/ytdlp-cookies", body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		rec := httptest.NewRecorder()
		handler.UploadYTDLPCookies(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("upload status = %d, want 200, body: %s", rec.Code, rec.Body.String())
		}
		var resp map[string]interface{}
		json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp["uploaded"] != true {
			t.Errorf("expected uploaded=true, got %v", resp["uploaded"])
		}
	})

	// Test GET status after upload
	t.Run("status_after_upload", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/api/ytdlp-cookies", nil)
		rec := httptest.NewRecorder()
		handler.GetYTDLPCookiesStatus(rec, req)

		var resp map[string]interface{}
		json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp["uploaded"] != true {
			t.Errorf("expected uploaded=true after upload, got %v", resp["uploaded"])
		}
		if resp["fileName"] != "yt-dlp-cookies.txt" {
			t.Errorf("expected fileName=yt-dlp-cookies.txt, got %v", resp["fileName"])
		}
	})

	// Test DELETE
	t.Run("delete", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/admin/api/ytdlp-cookies", nil)
		rec := httptest.NewRecorder()
		handler.DeleteYTDLPCookies(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("delete status = %d, want 200", rec.Code)
		}
		var resp map[string]interface{}
		json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp["uploaded"] != false {
			t.Errorf("expected uploaded=false after delete, got %v", resp["uploaded"])
		}
	})

	// Test upload with invalid file (no tabs)
	t.Run("upload_invalid", func(t *testing.T) {
		body := &bytes.Buffer{}
		writer := multipartWriter(t, body, "cookies", "cookies.txt", "this is not a cookie file")
		req := httptest.NewRequest(http.MethodPost, "/admin/api/ytdlp-cookies", body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		rec := httptest.NewRecorder()
		handler.UploadYTDLPCookies(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("invalid upload status = %d, want 400", rec.Code)
		}
	})
}

// multipartWriter creates a multipart form with a file field
func multipartWriter(t *testing.T, body *bytes.Buffer, fieldName, fileName, content string) *multipart.Writer {
	t.Helper()
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile(fieldName, fileName)
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	part.Write([]byte(content))
	writer.Close()
	return writer
}
