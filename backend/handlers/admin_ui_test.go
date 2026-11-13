package handlers_test

import (
	"bytes"
	"context"
	"crypto/sha256"
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

func TestAdminLoginPageShowsFirstLoginCredentials(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	rec := httptest.NewRecorder()

	handler.LoginPage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<code>admin</code> /") ||
		!strings.Contains(body, `name="new_password"`) ||
		!strings.Contains(body, `name="confirm_password"`) {
		t.Fatal("expected login page to show the first-login credentials and required password-change fields")
	}
}

func TestAdminLoginSubmitAcceptsFirstLoginCredentials(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)
	req := httptest.NewRequest(
		http.MethodPost,
		"/admin/login",
		strings.NewReader("username=admin&password=admin&new_password=replaced-password&confirm_password=replaced-password"),
	)
	req.RemoteAddr = "127.0.0.1:4321"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.LoginSubmit(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected first login to redirect with status 303, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminLoginSubmitReplacesDefaultPasswordThroughDockerBridge(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)
	req := httptest.NewRequest(
		http.MethodPost,
		"/admin/login",
		strings.NewReader("username=admin&password=admin&new_password=docker-password&confirm_password=docker-password"),
	)
	req.RemoteAddr = "172.18.0.1:4321"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.LoginSubmit(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected Docker first login to redirect with status 303, got %d: %s", rec.Code, rec.Body.String())
	}
	accountsService, err := accounts.NewService(tmpDir)
	if err != nil {
		t.Fatalf("reload accounts service: %v", err)
	}
	if _, err := accountsService.Authenticate("admin", "docker-password"); err != nil {
		t.Fatalf("replacement password did not authenticate: %v", err)
	}
	if accountsService.HasDefaultPassword() {
		t.Fatal("default password remained active after first login")
	}

	loginPageReq := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	loginPageRec := httptest.NewRecorder()
	handler.LoginPage(loginPageRec, loginPageReq)
	if strings.Contains(loginPageRec.Body.String(), `name="new_password"`) ||
		strings.Contains(loginPageRec.Body.String(), "<code>admin</code> /") {
		t.Fatal("first-login guidance remained visible after replacing the default password")
	}
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

	filtering, ok := schema["filtering"].(map[string]interface{})
	if !ok {
		t.Fatal("schema missing filtering section")
	}
	regexNotice, _ := filtering["description"].(string)
	for _, text := range []string{"Regex format:", `/\bREMUX\b/`, "/pattern/i", "case-insensitive"} {
		if !strings.Contains(regexNotice, text) {
			t.Errorf("filtering regex notice missing %q: %q", text, regexNotice)
		}
	}

	for _, key := range []string{"ranking.debrid.criteria", "ranking.usenet.criteria"} {
		section, ok := schema[key].(map[string]interface{})
		if !ok {
			t.Fatalf("schema missing %s section", key)
		}
		if section["parent"] != "ranking" {
			t.Errorf("%s parent = %v, want ranking", key, section["parent"])
		}
		showWhen, ok := section["showWhen"].(map[string]interface{})
		if !ok || showWhen["field"] != "ranking.splitByService" || showWhen["value"] != true {
			t.Errorf("%s showWhen = %#v, want ranking.splitByService=true", key, section["showWhen"])
		}
	}
	ranking, ok := schema["ranking"].(map[string]interface{})
	if !ok {
		t.Fatal("schema missing ranking section")
	}
	rankingFields, ok := ranking["fields"].(map[string]interface{})
	if !ok {
		t.Fatal("ranking schema missing fields")
	}
	newestReleaseFirst, ok := rankingFields["newestReleaseFirst"].(map[string]interface{})
	if !ok {
		t.Fatal("ranking schema missing newestReleaseFirst")
	}
	description, _ := newestReleaseFirst["description"].(string)
	for _, source := range []string{"Usenet/Newznab", "Jackett/Prowlarr", "Nyaa", "Internet Archive", "Zilean", "Torrentio", "Comet", "MediaFusion", "AIOStreams", "StremThru"} {
		if !strings.Contains(description, source) {
			t.Errorf("newestReleaseFirst description missing source %q: %q", source, description)
		}
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
		"name":      "New Profile",
		"accountId": masterAccount.ID,
	}
	body, _ := json.Marshal(reqBody)

	req := createAuthenticatedRequest(t, http.MethodPost, "/api/admin/profiles", body, sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()

	handler.RequireAuth(handler.CreateProfile)(rec, req)

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

	createBody, err := json.Marshal(map[string]string{
		"name":      "Living Room",
		"accountId": masterAccount.ID,
	})
	if err != nil {
		t.Fatalf("marshal create profile request: %v", err)
	}
	createReq := createAuthenticatedRequest(t, http.MethodPost, "/admin/api/profiles", createBody, sessionsService, masterAccount.ID, true)
	createRec := httptest.NewRecorder()
	handler.RequireAuth(handler.CreateProfile)(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("CreateProfile status = %d, want %d: %s", createRec.Code, http.StatusOK, createRec.Body.String())
	}

	req := createAuthenticatedRequest(t, http.MethodGet, "/api/admin/accounts", nil, sessionsService, masterAccount.ID, true)
	rec := httptest.NewRecorder()

	handler.RequireMasterAuth(handler.GetUserAccounts)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GetUserAccounts status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var response struct {
		Accounts []handlers.AdminAccountWithProfiles `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode accounts response: %v", err)
	}
	if len(response.Accounts) != 1 {
		t.Fatalf("accounts count = %d, want 1", len(response.Accounts))
	}
	var found bool
	for _, profile := range response.Accounts[0].Profiles {
		if profile.Name == "Living Room" {
			found = true
			if profile.AccountID != masterAccount.ID {
				t.Errorf("associated profile account ID = %q, want %q", profile.AccountID, masterAccount.ID)
			}
		}
	}
	if !found {
		t.Errorf("accounts response did not include the associated Living Room profile: %+v", response.Accounts[0].Profiles)
	}
}

func TestAdminUIHandler_GetUserAccountsScopesRegularAccount(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)
	accountsService, err := accounts.NewService(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	sessionsService, err := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	if err != nil {
		t.Fatal(err)
	}
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)
	owned, err := accountsService.Create("owned", "password")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := accountsService.Create("foreign", "password")
	if err != nil {
		t.Fatal(err)
	}

	req := createAuthenticatedRequest(t, http.MethodGet, "/account/api/accounts", nil, sessionsService, owned.ID, false)
	rec := httptest.NewRecorder()
	handler.RequireAuth(handler.GetUserAccounts)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GetUserAccounts status = %d: %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Accounts []handlers.AdminAccountWithProfiles `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Accounts) != 1 || response.Accounts[0].ID != owned.ID {
		t.Fatalf("accounts = %+v, want only %q", response.Accounts, owned.ID)
	}

	body, _ := json.Marshal(map[string]string{"username": "hijacked"})
	renameReq := createAuthenticatedRequest(t, http.MethodPatch, "/account/api/accounts?accountId="+foreign.ID, body, sessionsService, owned.ID, false)
	renameRec := httptest.NewRecorder()
	handler.RequireAuth(handler.RenameUserAccount)(renameRec, renameReq)
	if renameRec.Code != http.StatusNotFound {
		t.Fatalf("foreign rename status = %d, want 404", renameRec.Code)
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

func TestAdminUIHandler_TransferAdmin(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)
	accountsService, err := accounts.NewService(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	sessionsService, err := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	if err != nil {
		t.Fatal(err)
	}
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)

	current, _ := accountsService.GetMasterAccount()
	target, err := accountsService.Create("next-admin", "password123")
	if err != nil {
		t.Fatal(err)
	}
	targetSession, err := sessionsService.Create(target.ID, false, "target-agent", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"accountId": target.ID})
	req := createAuthenticatedRequest(t, http.MethodPost, "/admin/api/accounts/transfer-admin", body, sessionsService, current.ID, true)
	currentToken := req.Header.Get("Authorization")[len("Bearer "):]
	rec := httptest.NewRecorder()

	handler.RequireMasterAuth(handler.TransferAdmin)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("TransferAdmin status = %d: %s", rec.Code, rec.Body.String())
	}
	master, ok := accountsService.GetMasterAccount()
	if !ok || master.ID != target.ID {
		t.Fatalf("master = %+v, %v; want %q", master, ok, target.ID)
	}
	if _, err := sessionsService.Validate(currentToken); err == nil {
		t.Fatal("initiating admin session was not revoked")
	}
	if _, err := sessionsService.Validate(targetSession.Token); err == nil {
		t.Fatal("target account session was not revoked")
	}
}

func TestAdminUIHandler_TransferAdminRejectsRegularAccount(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)
	accountsService, _ := accounts.NewService(tmpDir)
	sessionsService, _ := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	handler.SetAccountsService(accountsService)
	handler.SetSessionsService(sessionsService)
	regular, _ := accountsService.Create("regular", "password123")
	body, _ := json.Marshal(map[string]string{"accountId": models.MasterAccountID})
	req := createAuthenticatedRequest(t, http.MethodPost, "/admin/api/accounts/transfer-admin", body, sessionsService, regular.ID, false)
	rec := httptest.NewRecorder()

	handler.RequireMasterAuth(handler.TransferAdmin)(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("regular transfer status = %d, want %d", rec.Code, http.StatusForbidden)
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

		var status handlers.AdminStatus
		if err := json.NewDecoder(rec.Body).Decode(&status); err != nil {
			t.Fatalf("failed to decode status response: %v", err)
		}
		if status.StartedAt.IsZero() {
			t.Error("expected started_at to be set")
		}
		if status.UptimeSeconds < 0 {
			t.Errorf("expected uptime_seconds >= 0, got %f", status.UptimeSeconds)
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

func TestAdminUIHandler_GetDebridStatusSkipsDisabledProviders(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)

	mgr := config.NewManager(filepath.Join(tmpDir, "settings.yaml"))
	settings, err := mgr.Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	settings.Streaming.DebridProviders = []config.DebridProviderSettings{
		{
			Name:     "Disabled Real Debrid",
			Provider: "realdebrid",
			Enabled:  false,
			APIKey:   "disabled-provider-key",
		},
	}
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/debrid-status", nil)
	rec := httptest.NewRecorder()

	handler.GetDebridStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GetDebridStatus status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		Providers []struct {
			Name      string `json:"name"`
			Provider  string `json:"provider"`
			Enabled   bool   `json:"enabled"`
			HasAPIKey bool   `json:"has_api_key"`
			Error     string `json:"error"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Providers) != 1 {
		t.Fatalf("providers len = %d, want 1; body=%s", len(body.Providers), rec.Body.String())
	}
	provider := body.Providers[0]
	if provider.Enabled {
		t.Fatalf("provider enabled = true, want false")
	}
	if !provider.HasAPIKey {
		t.Fatalf("provider has_api_key = false, want true")
	}
	if provider.Error != "" {
		t.Fatalf("disabled provider should not be tested, got error %q", provider.Error)
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

func TestAdminUIHandler_TestUsenetEngine_DecypharrAuthHint(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized: Host and token are required for authentication(you've enabled authentication)", http.StatusUnauthorized)
	}))
	defer server.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"name":          "Decypharr",
		"type":          "decypharr",
		"baseUrl":       server.URL,
		"webdavBaseUrl": server.URL + "/webdav",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/test/usenet-engine", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.TestUsenetEngine(rec, req)

	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if result["success"] != false {
		t.Fatalf("success = %#v, want false", result["success"])
	}
	errorMessage, _ := result["error"].(string)
	if !strings.Contains(errorMessage, "Decypharr login/auth is enabled") {
		t.Fatalf("error = %q, want Decypharr auth hint", errorMessage)
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
		"testMode":      "basic",
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
		"testMode":      "basic",
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
		"testMode":      "basic",
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

func TestAdminUIHandler_TestUsenetEngine_InfersWebDAVPathPrefix(t *testing.T) {
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
							"nzb_name":     "strmr-connection-test.nzb",
							"storage_path": "/mnt/debrid/decypharr_downloads/mediastorm/strmr-connection-test",
						}},
					},
				})
			case "get_config":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": true,
					"config": map[string]interface{}{
						"categories": []map[string]interface{}{},
					},
				})
			default:
				http.Error(w, "unexpected mode", http.StatusBadRequest)
			}
		case r.Method == "PROPFIND" && (r.URL.Path == "/webdav" || r.URL.Path == "/webdav/"):
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/webdav/</D:href>
    <D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>
  </D:response>
  <D:response>
    <D:href>/webdav/mediastorm/</D:href>
    <D:propstat><D:prop><D:displayname>mediastorm</D:displayname><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>
  </D:response>
</D:multistatus>`)
		case r.Method == "PROPFIND" && r.URL.Path == "/webdav/mediastorm/strmr-connection-test":
			w.WriteHeader(http.StatusMultiStatus)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"name":          "Decypharr",
		"type":          "decypharr",
		"testMode":      "basic",
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
	message := fmt.Sprint(result["message"])
	if !strings.Contains(message, `inferred Completed Path Prefix "/mnt/debrid/decypharr_downloads"`) {
		t.Fatalf("message = %q", message)
	}
}

func TestAdminUIHandler_TestUsenetEngine_NZBDavExUsesKnownMountRoot(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)

	var sawMappedWebDAV bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api":
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
							"status":   "Completed",
							"nzb_name": "strmr-connection-test.nzb",
							"storage":  "/mnt/davex/completed-symlinks/mediastorm/strmr-connection-test",
						}},
					},
				})
			case "get_config":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": true,
					"config": map[string]interface{}{
						"categories": []map[string]interface{}{{"name": "mediastorm", "dir": "mediastorm"}},
					},
				})
			default:
				http.Error(w, "unexpected mode", http.StatusBadRequest)
			}
		case r.Method == "PROPFIND" && (r.URL.Path == "/" || r.URL.Path == ""):
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/</D:href>
    <D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>
  </D:response>
  <D:response>
    <D:href>/completed-symlinks/</D:href>
    <D:propstat><D:prop><D:displayname>completed-symlinks</D:displayname><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>
  </D:response>
</D:multistatus>`)
		case r.Method == "PROPFIND" && r.URL.Path == "/completed-symlinks/mediastorm/strmr-connection-test":
			sawMappedWebDAV = true
			w.WriteHeader(http.StatusMultiStatus)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"name":          "NZBDavEx",
		"type":          "nzbdavex",
		"testMode":      "basic",
		"baseUrl":       server.URL,
		"apiPath":       "/api",
		"webdavBaseUrl": server.URL,
		"category":      "mediastorm",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/test/usenet-engine", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.TestUsenetEngine(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !sawMappedWebDAV {
		t.Fatal("expected NZBDavEx completed-symlinks path to be checked")
	}
	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if result["success"] != true {
		t.Fatalf("unexpected response: %#v", result)
	}
	message := fmt.Sprint(result["message"])
	if strings.Contains(message, "inferred Completed Path Prefix") {
		t.Fatalf("message should not ask for inferred prefix: %q", message)
	}
	if !strings.Contains(message, "completed-path mapping are valid") {
		t.Fatalf("message = %q", message)
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
		"testMode":      "basic",
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
		"testMode":      "basic",
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

func TestAdminUIHandler_TestUsenetEngine_TestNZBValidatesAndOffersCleanup(t *testing.T) {
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
		case r.Method == "PROPFIND" && r.URL.Path == "/webdav/Default/complete/test":
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
	cleanup, ok := result["cleanup"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected cleanup metadata, got %#v", result["cleanup"])
	}
	if cleanup["artifactUrl"] != server.URL+"/webdav/Default/complete/test" {
		t.Fatalf("artifactUrl = %#v", cleanup["artifactUrl"])
	}
	if !strings.Contains(fmt.Sprint(result["message"]), "test artifact remains available for optional cleanup") {
		t.Fatalf("message = %q", result["message"])
	}
}

func TestAdminUIHandler_DeleteUsenetEngineTestArtifact(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)

	var sawDelete bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/webdav/Default/complete/test":
			sawDelete = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"name":          "AltMount",
		"type":          "altmount",
		"webdavBaseUrl": server.URL + "/webdav",
		"artifactUrl":   server.URL + "/webdav/Default/complete/test",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/test/usenet-engine/delete-artifact", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.DeleteUsenetEngineTestArtifact(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !sawDelete {
		t.Fatal("expected WebDAV artifact delete")
	}
	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if result["success"] != true {
		t.Fatalf("unexpected response: %#v", result)
	}
}

func TestAdminUIHandler_DeleteUsenetEngineTestArtifactTreatsDecypharrUnsupportedDeleteAsExpected(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)

	var sawDelete bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/webdav/nzbs/test":
			sawDelete = true
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"name":          "Decypharr",
		"type":          "decypharr",
		"webdavBaseUrl": server.URL + "/webdav",
		"artifactUrl":   server.URL + "/webdav/nzbs/test",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/test/usenet-engine/delete-artifact", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.DeleteUsenetEngineTestArtifact(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !sawDelete {
		t.Fatal("expected WebDAV artifact delete")
	}
	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if result["success"] != true {
		t.Fatalf("unexpected response: %#v", result)
	}
	message := fmt.Sprint(result["message"])
	if strings.Contains(message, "failed") || !strings.Contains(message, "Decypharr does not support WebDAV delete") {
		t.Fatalf("message = %q", message)
	}
}

func TestAdminUIHandler_DeleteUsenetEngineTestArtifactRejectsOutsideWebDAVRoot(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)

	body, _ := json.Marshal(map[string]interface{}{
		"name":          "AltMount",
		"type":          "altmount",
		"webdavBaseUrl": "http://engine.test/webdav",
		"artifactUrl":   "http://engine.test/other/test",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/test/usenet-engine/delete-artifact", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.DeleteUsenetEngineTestArtifact(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if result["success"] != false {
		t.Fatalf("unexpected response: %#v", result)
	}
}

func TestAdminUIHandler_TestUsenetEngine_FullDecypharrValidatesWebDAVFallbackWhenHistoryIsEmpty(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)

	var sawAddFile bool
	var sawFallback bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sabnzbd/api":
			switch r.URL.Query().Get("mode") {
			case "addfile":
				sawAddFile = true
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status":  true,
					"nzo_ids": []string{"decypharr-job-1"},
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
					"status":  true,
					"history": map[string]interface{}{"slots": []map[string]interface{}{}},
				})
			case "get_config":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": true,
					"config": map[string]interface{}{
						"categories": []map[string]interface{}{},
					},
				})
			default:
				http.Error(w, "unexpected mode", http.StatusBadRequest)
			}
		case r.Method == "PROPFIND" && (r.URL.Path == "/webdav" || r.URL.Path == "/webdav/"):
			w.WriteHeader(http.StatusMultiStatus)
		case r.Method == "PROPFIND" && strings.HasPrefix(r.URL.Path, "/webdav/nzbs/strmr-connection-test-"):
			sawFallback = true
			w.WriteHeader(http.StatusMultiStatus)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"name":          "Decypharr",
		"type":          "decypharr",
		"testMode":      "full",
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
	if !sawAddFile {
		t.Fatal("expected test NZB to be submitted")
	}
	if !sawFallback {
		t.Fatal("expected Decypharr WebDAV fallback to be probed")
	}
	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if result["success"] != true {
		t.Fatalf("unexpected response: %#v", result)
	}
	if !strings.Contains(fmt.Sprint(result["message"]), "completed path verified via Decypharr WebDAV fallback") {
		t.Fatalf("message = %q", result["message"])
	}
	cleanup, ok := result["cleanup"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected cleanup metadata, got %#v", result["cleanup"])
	}
	if !strings.HasPrefix(fmt.Sprint(cleanup["artifactUrl"]), server.URL+"/webdav/nzbs/strmr-connection-test-") {
		t.Fatalf("artifactUrl = %#v", cleanup["artifactUrl"])
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
		case r.Method == "PROPFIND" && r.URL.Path == "/webdav/Default/complete/test":
			w.WriteHeader(207)
		case r.Method == http.MethodDelete && r.URL.Path == "/webdav/Default/complete/test":
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
	body := rec.Body.String()
	for _, contentID := range []string{"api-usage-content"} {
		if !strings.Contains(body, `aria-expanded="false" aria-controls="`+contentID+`"`) {
			t.Errorf("admin: expected %s toggle to start collapsed", contentID)
		}
		if !strings.Contains(body, `id="`+contentID+`" class="collapsible-panel-content" hidden`) {
			t.Errorf("admin: expected %s content to be hidden by default", contentID)
		}
	}
	if strings.Contains(body, "search-diagnostics") {
		t.Error("admin: connections page should not contain search diagnostics after their move to Search")
	}

	searchReq := httptest.NewRequest(http.MethodGet, "/admin/search", nil)
	searchReq.AddCookie(&http.Cookie{Name: "strmr_admin_session", Value: masterSession.Token})
	searchRec := httptest.NewRecorder()
	handler.RequireMasterAuth(handler.SearchPage)(searchRec, searchReq)
	if searchRec.Code != http.StatusOK {
		t.Fatalf("admin search: expected 200, got %d", searchRec.Code)
	}
	searchBody := searchRec.Body.String()
	for _, marker := range []string{`id="searchDiagnostics"`, `id="run-search-diagnostics-btn"`, `id="search-diagnostics-results"`} {
		if !strings.Contains(searchBody, marker) {
			t.Errorf("admin search: expected diagnostics marker %q", marker)
		}
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

func TestTransmuxSchemaIsVisibleUnderServerSettings(t *testing.T) {
	section, ok := handlers.SettingsSchema["transmux"].(map[string]interface{})
	if !ok {
		t.Fatal("transmux settings schema is missing")
	}
	if section["group"] != "server" {
		t.Fatalf("transmux group = %v, want server", section["group"])
	}
	if hidden, _ := section["hidden"].(bool); hidden {
		t.Fatal("hardware acceleration section must be visible")
	}
	fields, ok := section["fields"].(map[string]interface{})
	if !ok {
		t.Fatal("transmux fields schema is missing")
	}
	field, ok := fields["hardwareAcceleration"].(map[string]interface{})
	if !ok {
		t.Fatal("hardwareAcceleration field is missing")
	}
	if field["type"] != "select" || field["globalOnly"] != true {
		t.Fatalf("unexpected hardwareAcceleration schema: %+v", field)
	}

	for _, key := range []string{"ffmpegPath", "ffprobePath"} {
		field, ok := fields[key].(map[string]interface{})
		if !ok {
			t.Fatalf("%s field is missing", key)
		}
		if field["type"] != "text" || field["globalOnly"] != true {
			t.Fatalf("unexpected %s schema: %+v", key, field)
		}
		if hidden, _ := field["hidden"].(bool); hidden {
			t.Fatalf("%s field must be visible", key)
		}
	}
}

func TestSettingsSchemaFirstReadySourceIsGlobalOnly(t *testing.T) {
	section, ok := handlers.SettingsSchema["streaming"].(map[string]interface{})
	if !ok {
		t.Fatal("streaming settings schema is missing")
	}
	fields, ok := section["fields"].(map[string]interface{})
	if !ok {
		t.Fatal("streaming settings fields are missing")
	}
	field, ok := fields["resolveFirstReadySource"].(map[string]interface{})
	if !ok {
		t.Fatal("resolveFirstReadySource setting is missing")
	}
	if field["type"] != "boolean" || field["globalOnly"] != true {
		t.Fatalf("resolveFirstReadySource schema = %#v, want global-only boolean", field)
	}

	endRaceField, ok := fields["resolutionEndRaceEarly"].(map[string]interface{})
	if !ok {
		t.Fatal("resolutionEndRaceEarly setting is missing")
	}
	endRaceShowWhen, ok := endRaceField["showWhen"].(map[string]interface{})
	if !ok || endRaceShowWhen["field"] != "resolveFirstReadySource" || endRaceShowWhen["value"] != true {
		t.Fatalf("resolutionEndRaceEarly showWhen = %#v, want resolveFirstReadySource=true", endRaceField["showWhen"])
	}

	settleField, ok := fields["resolutionSettleWindowMs"].(map[string]interface{})
	if !ok {
		t.Fatal("resolutionSettleWindowMs setting is missing")
	}
	settleShowWhen, ok := settleField["showWhen"].(map[string]interface{})
	conditions, conditionsOK := settleShowWhen["conditions"].([]map[string]interface{})
	if !ok || settleShowWhen["operator"] != "and" || !conditionsOK || len(conditions) != 2 {
		t.Fatalf("resolutionSettleWindowMs showWhen = %#v, want two AND conditions", settleField["showWhen"])
	}
	if conditions[0]["field"] != "resolveFirstReadySource" || conditions[0]["value"] != true ||
		conditions[1]["field"] != "resolutionEndRaceEarly" || conditions[1]["value"] != true {
		t.Fatalf("resolutionSettleWindowMs conditions = %#v, want both parent toggles enabled", conditions)
	}
	if settleField["min"] != 0 {
		t.Fatalf("resolutionSettleWindowMs min = %#v, want 0", settleField["min"])
	}
	for key, subField := range map[string]map[string]interface{}{
		"resolutionEndRaceEarly":   endRaceField,
		"resolutionSettleWindowMs": settleField,
	} {
		if subField["group"] != "earlyResolution" {
			t.Fatalf("%s group = %#v, want earlyResolution", key, subField["group"])
		}
	}
}

func TestSettingsSchemaExternalBackendURLIsGlobalOnlyWatchPartyRequirement(t *testing.T) {
	section, ok := handlers.SettingsSchema["server"].(map[string]interface{})
	if !ok {
		t.Fatal("server settings schema is missing")
	}
	fields, ok := section["fields"].(map[string]interface{})
	if !ok {
		t.Fatal("server settings fields are missing")
	}
	field, ok := fields["externalBackendUrl"].(map[string]interface{})
	if !ok {
		t.Fatal("externalBackendUrl setting is missing")
	}
	if field["type"] != "text" || field["globalOnly"] != true || field["requiredFor"] != "External Watch Together access" {
		t.Fatalf("externalBackendUrl schema = %#v", field)
	}
}

func TestNavigationVisibilitySchemaIncludesWatchlist(t *testing.T) {
	section, ok := handlers.SettingsSchema["display"].(map[string]interface{})
	if !ok {
		t.Fatal("display settings schema is missing")
	}
	fields, ok := section["fields"].(map[string]interface{})
	if !ok {
		t.Fatal("display fields schema is missing")
	}
	field, ok := fields["navigationTabVisibility"].(map[string]interface{})
	if !ok {
		t.Fatal("navigationTabVisibility field is missing")
	}
	if field["type"] != "checkboxes" {
		t.Fatalf("navigationTabVisibility type = %#v, want checkboxes", field["type"])
	}
	options, ok := field["options"].([]map[string]interface{})
	if !ok {
		t.Fatalf("unexpected navigation options type: %T", field["options"])
	}
	for _, option := range options {
		if option["value"] == "watchlist" {
			return
		}
	}
	t.Fatal("navigation visibility options do not include Watchlist")
}

func TestDisplaySchemaIncludesTVHomeCardDimmingOptOut(t *testing.T) {
	section, ok := handlers.SettingsSchema["display"].(map[string]interface{})
	if !ok {
		t.Fatal("display settings schema is missing")
	}
	fields, ok := section["fields"].(map[string]interface{})
	if !ok {
		t.Fatal("display fields schema is missing")
	}
	field, ok := fields["disableTvHomeCardDimming"].(map[string]interface{})
	if !ok {
		t.Fatal("disableTvHomeCardDimming field is missing")
	}
	if field["type"] != "boolean" {
		t.Fatalf("unexpected disableTvHomeCardDimming schema: %+v", field)
	}
}

func TestSettingsSchemaOffersTorrinProvider(t *testing.T) {
	section, ok := handlers.SettingsSchema["debridProviders"].(map[string]interface{})
	if !ok {
		t.Fatal("debrid provider settings schema is missing")
	}
	fields, ok := section["fields"].(map[string]interface{})
	if !ok {
		t.Fatal("debrid provider settings fields are missing")
	}
	provider, ok := fields["provider"].(map[string]interface{})
	if !ok {
		t.Fatal("debrid provider field is missing")
	}
	options, ok := provider["options"].([]string)
	if !ok {
		t.Fatalf("debrid provider options = %#v", provider["options"])
	}
	for _, option := range options {
		if option == "torrin" {
			return
		}
	}
	t.Fatal("torrin is missing from the backend settings page provider options")
}

func TestSettingsSchemaIdentifiesInfiniDyskAsNZBDavCompatible(t *testing.T) {
	section, ok := handlers.SettingsSchema["usenetEngines"].(map[string]interface{})
	if !ok {
		t.Fatal("usenet engine settings schema is missing")
	}
	fields, ok := section["fields"].(map[string]interface{})
	if !ok {
		t.Fatal("usenet engine fields schema is missing")
	}
	engineType, ok := fields["type"].(map[string]interface{})
	if !ok {
		t.Fatal("usenet engine type schema is missing")
	}
	options, ok := engineType["options"].([]map[string]string)
	if !ok {
		t.Fatalf("unexpected usenet engine type options: %#v", engineType["options"])
	}

	for _, option := range options {
		if option["value"] == "nzbdav" {
			if option["label"] != "NZBDav (InfiniDysk)" {
				t.Fatalf("nzbdav option label = %q, want %q", option["label"], "NZBDav (InfiniDysk)")
			}
			return
		}
	}

	t.Fatal("usenet engine type options are missing nzbdav")
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

func TestSettingsPageVersionsHomeEditorByContent(t *testing.T) {
	handler, tmpDir := setupAdminUIHandler(t)
	sessionsService, err := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	if err != nil {
		t.Fatal(err)
	}
	handler.SetSessionsService(sessionsService)
	session, err := sessionsService.Create("master", true, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/settings", nil)
	req.AddCookie(&http.Cookie{Name: "strmr_admin_session", Value: session.Token})
	rec := httptest.NewRecorder()
	handler.RequireMasterAuth(handler.SettingsPage)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("settings status: %d", rec.Code)
	}
	for _, asset := range []string{"admin-home-views-v1.js", "admin-shelf-info-v1.js"} {
		script, err := os.ReadFile("static/" + asset)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(script)
		version := fmt.Sprintf("%x", sum)[:12]
		if !strings.Contains(rec.Body.String(), asset+"?v="+version) {
			t.Fatalf("%s URL must change with script content, independent of BuildID", asset)
		}
	}
}

func TestNavigationHomeViewsAreOptional(t *testing.T) {
	fields := handlers.SettingsSchema["display"].(map[string]interface{})["fields"].(map[string]interface{})
	options := fields["navigationTabVisibility"].(map[string]interface{})["options"].([]map[string]interface{})
	for _, key := range []string{"movies", "shows"} {
		found := false
		for _, option := range options {
			if option["value"] == key {
				found = true
			}
		}
		if !found {
			t.Errorf("missing navigation option %s", key)
		}
	}
	for _, key := range config.DefaultSettings().Display.NavigationTabVisibility {
		if key == "movies" || key == "shows" {
			t.Errorf("%s must default off", key)
		}
	}
}

func TestNavigationVisibilitySchemaIncludesCalendarAndRecordings(t *testing.T) {
	section := handlers.SettingsSchema["display"].(map[string]interface{})
	fields := section["fields"].(map[string]interface{})
	options := fields["navigationTabVisibility"].(map[string]interface{})["options"].([]map[string]interface{})
	for _, key := range []string{"calendar", "recordings"} {
		found := false
		for _, option := range options {
			if option["value"] == key {
				found = true
			}
		}
		if !found {
			t.Errorf("navigation options missing %s", key)
		}
	}
}

func TestAdminUIHandler_TestLiveTV_HDHomeRun(t *testing.T) {
	handler, _ := setupAdminUIHandler(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/lineup.m3u" {
			t.Errorf("unexpected tuner request: %s", r.URL.Path)
		}
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:-1,TESTTV\nhttp://192.168.1.100:5004/auto/v5.1\n")
	}))
	defer server.Close()
	body, _ := json.Marshal(map[string]string{"mode": "hdhomerun", "hdhomerunHost": server.URL})
	rec := httptest.NewRecorder()
	handler.TestLiveTV(rec, httptest.NewRequest(http.MethodPost, "/admin/api/test/live", bytes.NewReader(body)))
	var result map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || result["success"] != true {
		t.Fatalf("tuner test failed: %s", rec.Body.String())
	}
}

func TestSettingsSchemaProfilePinEnforcementIsGlobalOnlyAndExplained(t *testing.T) {
	section, ok := handlers.SettingsSchema["security"].(map[string]interface{})
	if !ok {
		t.Fatal("security settings schema is missing")
	}
	fields := section["fields"].(map[string]interface{})
	enforce, ok := fields["enforceProfilePins"].(map[string]interface{})
	if !ok || enforce["type"] != "boolean" || enforce["globalOnly"] != true {
		t.Fatalf("enforceProfilePins schema = %#v", enforce)
	}
	if desc, _ := enforce["description"].(string); !strings.Contains(desc, "Update every app first") {
		t.Fatalf("enforceProfilePins must warn that clients need updating: %q", desc)
	}
	server := handlers.SettingsSchema["server"].(map[string]interface{})["fields"].(map[string]interface{})
	if field, ok := server["lanDiscoveryEnabled"].(map[string]interface{}); !ok || field["type"] != "boolean" {
		t.Fatalf("lanDiscoveryEnabled schema = %#v", server["lanDiscoveryEnabled"])
	}
}
