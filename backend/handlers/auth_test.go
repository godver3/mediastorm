package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"novastream/handlers"
	"novastream/models"
	"novastream/services/accounts"
	"novastream/services/sessions"
)

// fakeAccountsService implements a minimal accounts service for testing auth handlers.
type fakeAccountsService struct {
	authenticateAccount models.Account
	authenticateErr     error
	getAccount          models.Account
	getOK               bool
	updatePasswordErr   error
	hasDefault          bool
}

func (f *fakeAccountsService) Authenticate(username, password string) (models.Account, error) {
	return f.authenticateAccount, f.authenticateErr
}

func (f *fakeAccountsService) Get(id string) (models.Account, bool) {
	return f.getAccount, f.getOK
}

func (f *fakeAccountsService) UpdatePassword(id, newPassword string) error {
	return f.updatePasswordErr
}

func (f *fakeAccountsService) HasDefaultPassword() bool {
	return f.hasDefault
}

// fakeSessionsService implements a minimal sessions service for testing auth handlers.
type fakeSessionsService struct {
	createSession           models.Session
	createErr               error
	createPersistentSession models.Session
	createPersistentErr     error
	validateSession         models.Session
	validateErr             error
	revokeErr               error
	refreshSession          models.Session
	refreshErr              error
}

func (f *fakeSessionsService) Create(accountID string, isMaster bool, userAgent, ipAddress string) (models.Session, error) {
	return f.createSession, f.createErr
}

func (f *fakeSessionsService) CreatePersistent(accountID string, isMaster bool, userAgent, ipAddress string) (models.Session, error) {
	return f.createPersistentSession, f.createPersistentErr
}

func (f *fakeSessionsService) Validate(token string) (models.Session, error) {
	return f.validateSession, f.validateErr
}

func (f *fakeSessionsService) Revoke(token string) error {
	return f.revokeErr
}

func (f *fakeSessionsService) Refresh(token string) (models.Session, error) {
	return f.refreshSession, f.refreshErr
}

// Helper to create accounts and sessions services and auth handler for testing.
func setupAuthHandler(t *testing.T) (*handlers.AuthHandler, *accounts.Service, *sessions.Service) {
	t.Helper()
	tmpDir := t.TempDir()

	accountsSvc, err := accounts.NewService(tmpDir)
	if err != nil {
		t.Fatalf("failed to create accounts service: %v", err)
	}

	sessionsSvc, err := sessions.NewService(tmpDir, sessions.DefaultSessionDuration)
	if err != nil {
		t.Fatalf("failed to create sessions service: %v", err)
	}

	handler := handlers.NewAuthHandler(accountsSvc, sessionsSvc)
	return handler, accountsSvc, sessionsSvc
}

func TestLogin_Success(t *testing.T) {
	handler, _, _ := setupAuthHandler(t)

	reqBody := handlers.LoginRequest{
		Username: "admin",
		Password: "admin", // Default master password
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.Login(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp handlers.LoginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Token == "" {
		t.Error("expected non-empty token")
	}
	if resp.AccountID == "" {
		t.Error("expected non-empty AccountID")
	}
	if resp.Username != "admin" {
		t.Errorf("expected username 'admin', got %q", resp.Username)
	}
	if !resp.IsMaster {
		t.Error("expected IsMaster to be true")
	}
}

func TestLogin_InvalidJSON(t *testing.T) {
	handler, _, _ := setupAuthHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader([]byte("invalid json")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.Login(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestLogin_InvalidCredentials(t *testing.T) {
	handler, _, _ := setupAuthHandler(t)

	reqBody := handlers.LoginRequest{
		Username: "admin",
		Password: "wrongpassword",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.Login(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rec.Code)
	}
}

func TestLogin_WithRememberMe(t *testing.T) {
	handler, _, _ := setupAuthHandler(t)

	reqBody := handlers.LoginRequest{
		Username:   "admin",
		Password:   "admin",
		RememberMe: true,
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.Login(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp handlers.LoginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Verify the expiry is far in the future (persistent session)
	expiresAt, err := time.Parse("2006-01-02T15:04:05Z", resp.ExpiresAt)
	if err != nil {
		t.Fatalf("failed to parse expiry: %v", err)
	}

	// Persistent sessions should expire in ~100 years
	if expiresAt.Before(time.Now().Add(50 * 365 * 24 * time.Hour)) {
		t.Error("expected persistent session to have far future expiry")
	}
}

func TestLogin_CapturesMetadata(t *testing.T) {
	handler, _, sessionsSvc := setupAuthHandler(t)

	reqBody := handlers.LoginRequest{
		Username: "admin",
		Password: "admin",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "TestBrowser/1.0")
	req.Header.Set("X-Forwarded-For", "192.168.1.100")
	rec := httptest.NewRecorder()

	handler.Login(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var resp handlers.LoginResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)

	// Verify session metadata was captured
	session, err := sessionsSvc.Validate(resp.Token)
	if err != nil {
		t.Fatalf("failed to validate session: %v", err)
	}

	if session.UserAgent != "TestBrowser/1.0" {
		t.Errorf("expected UserAgent 'TestBrowser/1.0', got %q", session.UserAgent)
	}
	if session.IPAddress != "192.168.1.100" {
		t.Errorf("expected IPAddress '192.168.1.100', got %q", session.IPAddress)
	}
}

func TestLogout_Success(t *testing.T) {
	handler, _, sessionsSvc := setupAuthHandler(t)

	// Create a session first
	session, _ := sessionsSvc.Create("master", true, "", "")

	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+session.Token)
	rec := httptest.NewRecorder()

	handler.Logout(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify session is revoked
	_, err := sessionsSvc.Validate(session.Token)
	if err != sessions.ErrSessionNotFound {
		t.Errorf("expected session to be revoked, got %v", err)
	}
}

func TestLogout_NoToken(t *testing.T) {
	handler, _, _ := setupAuthHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	rec := httptest.NewRecorder()

	handler.Logout(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestLogout_SessionNotFound(t *testing.T) {
	handler, _, _ := setupAuthHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer nonexistent-token")
	rec := httptest.NewRecorder()

	handler.Logout(rec, req)

	// Should still return success (idempotent)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200 (idempotent), got %d", rec.Code)
	}
}

func TestMe_Success(t *testing.T) {
	handler, _, sessionsSvc := setupAuthHandler(t)

	// Create a session for master account
	session, _ := sessionsSvc.Create("master", true, "", "")

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+session.Token)
	rec := httptest.NewRecorder()

	handler.Me(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp handlers.AccountResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.ID != "master" {
		t.Errorf("expected ID 'master', got %q", resp.ID)
	}
	if resp.Username != "admin" {
		t.Errorf("expected username 'admin', got %q", resp.Username)
	}
	if !resp.IsMaster {
		t.Error("expected IsMaster to be true")
	}
}

func TestMe_NoToken(t *testing.T) {
	handler, _, _ := setupAuthHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	rec := httptest.NewRecorder()

	handler.Me(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rec.Code)
	}
}

func TestMe_InvalidSession(t *testing.T) {
	handler, _, _ := setupAuthHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	rec := httptest.NewRecorder()

	handler.Me(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rec.Code)
	}
}

func TestMe_AccountNotFound(t *testing.T) {
	handler, _, sessionsSvc := setupAuthHandler(t)

	// Create a session for a non-existent account
	session, _ := sessionsSvc.Create("nonexistent-account", false, "", "")

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+session.Token)
	rec := httptest.NewRecorder()

	handler.Me(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", rec.Code)
	}
}

func TestRefresh_Success(t *testing.T) {
	handler, _, sessionsSvc := setupAuthHandler(t)

	// Create a session
	session, _ := sessionsSvc.Create("master", true, "", "")

	req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	req.Header.Set("Authorization", "Bearer "+session.Token)
	rec := httptest.NewRecorder()

	handler.Refresh(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp handlers.LoginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Verify response contains expected fields
	if resp.Token != session.Token {
		t.Errorf("expected same token, got different")
	}
	if resp.AccountID != "master" {
		t.Errorf("expected AccountID 'master', got %q", resp.AccountID)
	}
	if resp.ExpiresAt == "" {
		t.Error("expected non-empty ExpiresAt")
	}
}

func TestRefresh_NoToken(t *testing.T) {
	handler, _, _ := setupAuthHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	rec := httptest.NewRecorder()

	handler.Refresh(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rec.Code)
	}
}

func TestRefresh_InvalidSession(t *testing.T) {
	handler, _, _ := setupAuthHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	rec := httptest.NewRecorder()

	handler.Refresh(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rec.Code)
	}
}

func TestChangePassword_Success(t *testing.T) {
	handler, accountsSvc, sessionsSvc := setupAuthHandler(t)

	// Create a session for master account
	session, _ := sessionsSvc.Create("master", true, "", "")

	reqBody := handlers.ChangePasswordRequest{
		CurrentPassword: "admin",
		NewPassword:     "newpassword123",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/change-password", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+session.Token)
	rec := httptest.NewRecorder()

	handler.ChangePassword(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify password was changed
	_, err := accountsSvc.Authenticate("admin", "newpassword123")
	if err != nil {
		t.Errorf("expected new password to work: %v", err)
	}

	// Verify old password no longer works
	_, err = accountsSvc.Authenticate("admin", "admin")
	if err == nil {
		t.Error("expected old password to fail")
	}
}

func TestChangePassword_NoToken(t *testing.T) {
	handler, _, _ := setupAuthHandler(t)

	reqBody := handlers.ChangePasswordRequest{
		CurrentPassword: "admin",
		NewPassword:     "newpassword",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/change-password", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ChangePassword(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rec.Code)
	}
}

func TestChangePassword_WrongCurrentPassword(t *testing.T) {
	handler, _, sessionsSvc := setupAuthHandler(t)

	// Create a session for master account
	session, _ := sessionsSvc.Create("master", true, "", "")

	reqBody := handlers.ChangePasswordRequest{
		CurrentPassword: "wrongpassword",
		NewPassword:     "newpassword",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/change-password", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+session.Token)
	rec := httptest.NewRecorder()

	handler.ChangePassword(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rec.Code)
	}
}

func TestChangePassword_InvalidJSON(t *testing.T) {
	handler, _, sessionsSvc := setupAuthHandler(t)

	session, _ := sessionsSvc.Create("master", true, "", "")

	req := httptest.NewRequest(http.MethodPost, "/api/auth/change-password", bytes.NewReader([]byte("invalid")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+session.Token)
	rec := httptest.NewRecorder()

	handler.ChangePassword(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestExtractBearerToken_WithBearer(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer mytoken123")

	// Use reflection or test via the handlers
	// Since extractBearerToken is not exported, we test it indirectly through handlers
	handler, _, sessionsSvc := setupAuthHandler(t)

	session, _ := sessionsSvc.Create("master", true, "", "")
	req.Header.Set("Authorization", "Bearer "+session.Token)
	rec := httptest.NewRecorder()

	handler.Me(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected Bearer token to be extracted")
	}
}

func TestExtractBearerToken_CaseInsensitive(t *testing.T) {
	handler, _, sessionsSvc := setupAuthHandler(t)

	session, _ := sessionsSvc.Create("master", true, "", "")
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.Header.Set("Authorization", "bearer "+session.Token) // lowercase "bearer"
	rec := httptest.NewRecorder()

	handler.Me(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected lowercase 'bearer' to work, got %d", rec.Code)
	}
}

func TestExtractBearerToken_NoPrefix(t *testing.T) {
	handler, _, sessionsSvc := setupAuthHandler(t)

	session, _ := sessionsSvc.Create("master", true, "", "")
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.Header.Set("Authorization", session.Token) // No "Bearer" prefix
	rec := httptest.NewRecorder()

	handler.Me(rec, req)

	// Should fail because no "Bearer" prefix
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without Bearer prefix, got %d", rec.Code)
	}
}

func TestGetClientIPAddress_XForwardedFor(t *testing.T) {
	handler, _, _ := setupAuthHandler(t)

	reqBody := handlers.LoginRequest{
		Username: "admin",
		Password: "admin",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("X-Forwarded-For", "10.0.0.1, 192.168.1.1")
	rec := httptest.NewRecorder()

	handler.Login(rec, req)

	// The first IP in X-Forwarded-For should be used
	// We can verify this through the session metadata
}

func TestGetClientIPAddress_XRealIP(t *testing.T) {
	handler, _, sessionsSvc := setupAuthHandler(t)

	reqBody := handlers.LoginRequest{
		Username: "admin",
		Password: "admin",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("X-Real-IP", "172.16.0.1")
	rec := httptest.NewRecorder()

	handler.Login(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp handlers.LoginResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)

	session, _ := sessionsSvc.Validate(resp.Token)
	if session.IPAddress != "172.16.0.1" {
		t.Errorf("expected IP '172.16.0.1', got %q", session.IPAddress)
	}
}

func TestGetClientIPAddress_RemoteAddr(t *testing.T) {
	handler, _, sessionsSvc := setupAuthHandler(t)

	reqBody := handlers.LoginRequest{
		Username: "admin",
		Password: "admin",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	req.RemoteAddr = "192.168.0.100:12345"
	rec := httptest.NewRecorder()

	handler.Login(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp handlers.LoginResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)

	session, _ := sessionsSvc.Validate(resp.Token)
	if session.IPAddress != "192.168.0.100" {
		t.Errorf("expected IP '192.168.0.100', got %q", session.IPAddress)
	}
}

func TestOptions_Success(t *testing.T) {
	handler, _, _ := setupAuthHandler(t)

	req := httptest.NewRequest(http.MethodOptions, "/api/auth/login", nil)
	rec := httptest.NewRecorder()

	handler.Options(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
}
