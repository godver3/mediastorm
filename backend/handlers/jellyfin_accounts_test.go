package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/handlers"
	"novastream/services/jellyfin"
)

// setupJellyfinHandler creates a config manager with a temp settings file and a real jellyfin client.
func setupJellyfinHandler(t *testing.T) (*handlers.JellyfinAccountsHandler, *config.Manager) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(cfgPath)
	client := jellyfin.NewClient()
	h := handlers.NewJellyfinAccountsHandler(mgr, client)
	return h, mgr
}

// saveSettingsWithAccounts persists settings containing the given Jellyfin accounts.
func saveSettingsWithAccounts(t *testing.T, mgr *config.Manager, accounts []config.JellyfinAccount) {
	t.Helper()
	s, err := mgr.Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	s.Jellyfin.Accounts = accounts
	if err := mgr.Save(s); err != nil {
		t.Fatalf("save settings: %v", err)
	}
}

// --- ListAccounts ---

func TestListJellyfinAccounts_Empty(t *testing.T) {
	h, _ := setupJellyfinHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/jellyfin/accounts", nil)
	rec := httptest.NewRecorder()

	h.ListAccounts(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Accounts []handlers.JellyfinAccountResponse `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Accounts) != 0 {
		t.Errorf("expected 0 accounts, got %d", len(resp.Accounts))
	}
}

func TestListJellyfinAccounts_WithAccounts(t *testing.T) {
	h, mgr := setupJellyfinHandler(t)

	saveSettingsWithAccounts(t, mgr, []config.JellyfinAccount{
		{ID: "acc1", Name: "Server One", ServerURL: "http://jf1:8096", Token: "tok1", Username: "alice"},
		{ID: "acc2", Name: "Server Two", ServerURL: "http://jf2:8096", Token: "", Username: "bob"},
	})

	req := httptest.NewRequest(http.MethodGet, "/admin/api/jellyfin/accounts", nil)
	rec := httptest.NewRecorder()

	h.ListAccounts(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		Accounts []handlers.JellyfinAccountResponse `json:"accounts"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)

	if len(resp.Accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(resp.Accounts))
	}

	// First account has a token → Connected = true
	if !resp.Accounts[0].Connected {
		t.Error("expected first account to be connected")
	}
	if resp.Accounts[0].Username != "alice" {
		t.Errorf("expected username 'alice', got %q", resp.Accounts[0].Username)
	}

	// Second account has no token → Connected = false
	if resp.Accounts[1].Connected {
		t.Error("expected second account to not be connected")
	}
}

// --- CreateAccount ---

func TestCreateJellyfinAccount_Success(t *testing.T) {
	// Stand up a mock Jellyfin server that accepts auth
	mockJF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users/AuthenticateByName" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"AccessToken": "test-token-123",
				"User": map[string]string{
					"Id":   "jf-user-id",
					"Name": "testuser",
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer mockJF.Close()

	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(cfgPath)
	client := jellyfin.NewClient()
	h := handlers.NewJellyfinAccountsHandler(mgr, client)

	body, _ := json.Marshal(map[string]string{
		"name":      "My Server",
		"serverUrl": mockJF.URL,
		"username":  "testuser",
		"password":  "pass123",
	})

	req := httptest.NewRequest(http.MethodPost, "/admin/api/jellyfin/accounts", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.CreateAccount(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Success bool                              `json:"success"`
		Account handlers.JellyfinAccountResponse `json:"account"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Success {
		t.Error("expected success=true")
	}
	if resp.Account.Name != "My Server" {
		t.Errorf("expected name 'My Server', got %q", resp.Account.Name)
	}
	if resp.Account.Username != "testuser" {
		t.Errorf("expected username 'testuser', got %q", resp.Account.Username)
	}
	if !resp.Account.Connected {
		t.Error("expected account to be connected")
	}
	if resp.Account.ID == "" {
		t.Error("expected non-empty account ID")
	}

	// Verify account persisted in settings
	s, _ := mgr.Load()
	if len(s.Jellyfin.Accounts) != 1 {
		t.Fatalf("expected 1 saved account, got %d", len(s.Jellyfin.Accounts))
	}
	if s.Jellyfin.Accounts[0].Token != "test-token-123" {
		t.Errorf("expected saved token 'test-token-123', got %q", s.Jellyfin.Accounts[0].Token)
	}
}

func TestCreateJellyfinAccount_DefaultName(t *testing.T) {
	// When name is empty, handler should generate "<username> @ <serverUrl>"
	mockJF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"AccessToken": "tok",
			"User":        map[string]string{"Id": "uid", "Name": "alice"},
		})
	}))
	defer mockJF.Close()

	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(cfgPath)
	h := handlers.NewJellyfinAccountsHandler(mgr, jellyfin.NewClient())

	body, _ := json.Marshal(map[string]string{
		"serverUrl": mockJF.URL,
		"username":  "alice",
		"password":  "pass",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/jellyfin/accounts", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.CreateAccount(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Account handlers.JellyfinAccountResponse `json:"account"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)

	expected := "alice @ " + mockJF.URL
	if resp.Account.Name != expected {
		t.Errorf("expected name %q, got %q", expected, resp.Account.Name)
	}
}

func TestCreateJellyfinAccount_MissingFields(t *testing.T) {
	h, _ := setupJellyfinHandler(t)

	tests := []struct {
		name string
		body map[string]string
	}{
		{"missing serverUrl", map[string]string{"username": "user", "password": "pass"}},
		{"missing username", map[string]string{"serverUrl": "http://jf:8096", "password": "pass"}},
		{"both empty", map[string]string{"serverUrl": "", "username": "", "password": "pass"}},
		{"whitespace only", map[string]string{"serverUrl": "  ", "username": "  ", "password": "pass"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.body)
			req := httptest.NewRequest(http.MethodPost, "/admin/api/jellyfin/accounts", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			h.CreateAccount(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestCreateJellyfinAccount_AuthFailure(t *testing.T) {
	// Mock server that rejects auth
	mockJF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("Invalid credentials"))
	}))
	defer mockJF.Close()

	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(cfgPath)
	h := handlers.NewJellyfinAccountsHandler(mgr, jellyfin.NewClient())

	body, _ := json.Marshal(map[string]string{
		"serverUrl": mockJF.URL,
		"username":  "user",
		"password":  "wrong",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/jellyfin/accounts", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.CreateAccount(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- DeleteAccount ---

func TestDeleteJellyfinAccount_Success(t *testing.T) {
	h, mgr := setupJellyfinHandler(t)

	saveSettingsWithAccounts(t, mgr, []config.JellyfinAccount{
		{ID: "del-me", Name: "Delete Me", ServerURL: "http://jf:8096", Token: "tok"},
		{ID: "keep-me", Name: "Keep Me", ServerURL: "http://jf:8096", Token: "tok2"},
	})

	req := httptest.NewRequest(http.MethodDelete, "/admin/api/jellyfin/accounts/del-me", nil)
	req = mux.SetURLVars(req, map[string]string{"accountID": "del-me"})
	rec := httptest.NewRecorder()

	h.DeleteAccount(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["success"] != true {
		t.Error("expected success=true")
	}

	// Verify only one account remains
	s, _ := mgr.Load()
	if len(s.Jellyfin.Accounts) != 1 {
		t.Fatalf("expected 1 remaining account, got %d", len(s.Jellyfin.Accounts))
	}
	if s.Jellyfin.Accounts[0].ID != "keep-me" {
		t.Errorf("expected remaining account 'keep-me', got %q", s.Jellyfin.Accounts[0].ID)
	}
}

func TestDeleteJellyfinAccount_NotFound(t *testing.T) {
	h, mgr := setupJellyfinHandler(t)

	saveSettingsWithAccounts(t, mgr, []config.JellyfinAccount{
		{ID: "existing", Name: "Existing", ServerURL: "http://jf:8096"},
	})

	req := httptest.NewRequest(http.MethodDelete, "/admin/api/jellyfin/accounts/nonexistent", nil)
	req = mux.SetURLVars(req, map[string]string{"accountID": "nonexistent"})
	rec := httptest.NewRecorder()

	h.DeleteAccount(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- TestConnection ---

func TestTestJellyfinConnection_Success(t *testing.T) {
	// Mock Jellyfin server that responds OK to /System/Info
	mockJF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/System/Info" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"ServerName":"TestJF"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer mockJF.Close()

	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(cfgPath)
	client := jellyfin.NewClient()
	h := handlers.NewJellyfinAccountsHandler(mgr, client)

	// Save an account pointing to mock server
	s, _ := mgr.Load()
	s.Jellyfin.Accounts = []config.JellyfinAccount{
		{ID: "test-acc", Name: "Test", ServerURL: mockJF.URL, Token: "valid-token"},
	}
	mgr.Save(s)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/jellyfin/accounts/test-acc/test", nil)
	req = mux.SetURLVars(req, map[string]string{"accountID": "test-acc"})
	rec := httptest.NewRecorder()

	h.TestConnection(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["success"] != true {
		t.Error("expected success=true")
	}
	if resp["message"] != "Connection successful" {
		t.Errorf("expected message 'Connection successful', got %v", resp["message"])
	}
}

func TestTestJellyfinConnection_NotConnected(t *testing.T) {
	h, mgr := setupJellyfinHandler(t)

	// Account with empty token
	saveSettingsWithAccounts(t, mgr, []config.JellyfinAccount{
		{ID: "no-token", Name: "No Token", ServerURL: "http://jf:8096", Token: ""},
	})

	req := httptest.NewRequest(http.MethodPost, "/admin/api/jellyfin/accounts/no-token/test", nil)
	req = mux.SetURLVars(req, map[string]string{"accountID": "no-token"})
	rec := httptest.NewRecorder()

	h.TestConnection(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTestJellyfinConnection_AccountNotFound(t *testing.T) {
	h, _ := setupJellyfinHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/jellyfin/accounts/missing/test", nil)
	req = mux.SetURLVars(req, map[string]string{"accountID": "missing"})
	rec := httptest.NewRecorder()

	h.TestConnection(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTestJellyfinConnection_ServerDown(t *testing.T) {
	// Mock Jellyfin server that returns 500
	mockJF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mockJF.Close()

	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(cfgPath)
	h := handlers.NewJellyfinAccountsHandler(mgr, jellyfin.NewClient())

	s, _ := mgr.Load()
	s.Jellyfin.Accounts = []config.JellyfinAccount{
		{ID: "down-acc", Name: "Down", ServerURL: mockJF.URL, Token: "tok"},
	}
	mgr.Save(s)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/jellyfin/accounts/down-acc/test", nil)
	req = mux.SetURLVars(req, map[string]string{"accountID": "down-acc"})
	rec := httptest.NewRecorder()

	h.TestConnection(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
}
