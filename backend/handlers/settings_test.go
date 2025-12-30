package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"novastream/config"
)

func TestSettingsHandler_GetSettings(t *testing.T) {
	cfg := config.Settings{
		Server: config.ServerSettings{Host: "127.0.0.1", Port: 9999},
		Usenet: []config.UsenetSettings{
			{Name: "Test", Host: "news.example", Port: 563, SSL: true, Username: "user", Password: "pass", Connections: 16, Enabled: true},
		},
	}

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(cfg); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	handler := NewSettingsHandler(mgr)

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rec := httptest.NewRecorder()

	handler.GetSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("unexpected content-type %q", got)
	}

	var got config.Settings
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if got.Server.Port != cfg.Server.Port || got.Server.Host != cfg.Server.Host {
		t.Fatalf("unexpected server settings: %+v", got.Server)
	}
	if len(got.Usenet) != 1 || got.Usenet[0].Username != cfg.Usenet[0].Username || got.Usenet[0].Password != cfg.Usenet[0].Password {
		t.Fatalf("unexpected usenet settings: %+v", got.Usenet)
	}
}

func TestSettingsHandler_PutSettings(t *testing.T) {
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	handler := NewSettingsHandler(mgr)

	payload := config.Settings{
		Server: config.ServerSettings{Host: "0.0.0.0", Port: 8888},
		Usenet: []config.UsenetSettings{
			{Name: "Test", Host: "news.example", Port: 443, SSL: false, Username: "alice", Password: "hunter2", Connections: 4, Enabled: true},
		},
	}

	buf, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(buf))
	rec := httptest.NewRecorder()

	handler.PutSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("unexpected content-type %q", got)
	}

	var resp config.Settings
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Server.Port != payload.Server.Port || len(resp.Usenet) != 1 || resp.Usenet[0].Host != payload.Usenet[0].Host {
		t.Fatalf("unexpected response payload: %+v", resp)
	}

	saved, err := mgr.Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if len(saved.Usenet) != 1 || saved.Usenet[0].Username != payload.Usenet[0].Username || saved.Server.Port != payload.Server.Port {
		t.Fatalf("settings not persisted: %+v", saved)
	}
}
