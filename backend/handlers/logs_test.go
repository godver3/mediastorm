package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogsHandler_TryPasteService_DirectURL(t *testing.T) {
	// Mock server that returns a direct URL
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "https://paste.example.com/abc123")
	}))
	defer server.Close()

	h := NewLogsHandler(log.New(os.Stdout, "", 0), "")
	client := &http.Client{}

	service := pasteService{
		name: "test-direct",
		url:  server.URL,
		headers: map[string]string{
			"Content-Type": "text/plain",
		},
	}

	result, err := h.tryPasteService(client, service, "test content")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "https://paste.example.com/abc123" {
		t.Errorf("expected URL https://paste.example.com/abc123, got %s", result)
	}
}

func TestLogsHandler_TryPasteService_JSONKey(t *testing.T) {
	// Mock server that returns JSON with a key field
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"key": "xyz789"})
	}))
	defer server.Close()

	h := NewLogsHandler(log.New(os.Stdout, "", 0), "")
	client := &http.Client{}

	service := pasteService{
		name:         "test-json",
		url:          server.URL,
		urlPrefix:    "https://paste.example.com/",
		jsonKeyField: "key",
		headers: map[string]string{
			"Content-Type": "text/plain",
		},
	}

	result, err := h.tryPasteService(client, service, "test content")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "https://paste.example.com/xyz789" {
		t.Errorf("expected URL https://paste.example.com/xyz789, got %s", result)
	}
}

func TestLogsHandler_TryPasteService_ServerError(t *testing.T) {
	// Mock server that returns an error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "internal error")
	}))
	defer server.Close()

	h := NewLogsHandler(log.New(os.Stdout, "", 0), "")
	client := &http.Client{}

	service := pasteService{
		name: "test-error",
		url:  server.URL,
	}

	_, err := h.tryPasteService(client, service, "test content")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Errorf("expected error to contain 'status 500', got: %v", err)
	}
}

func TestLogsHandler_TryPasteService_InvalidJSONResponse(t *testing.T) {
	// Mock server that returns invalid JSON when JSON key is expected
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "not json")
	}))
	defer server.Close()

	h := NewLogsHandler(log.New(os.Stdout, "", 0), "")
	client := &http.Client{}

	service := pasteService{
		name:         "test-invalid-json",
		url:          server.URL,
		urlPrefix:    "https://paste.example.com/",
		jsonKeyField: "key",
	}

	_, err := h.tryPasteService(client, service, "test content")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	if !strings.Contains(err.Error(), "failed to parse JSON") {
		t.Errorf("expected JSON parse error, got: %v", err)
	}
}

func TestLogsHandler_TryPasteService_MissingKeyField(t *testing.T) {
	// Mock server that returns JSON without the expected key field
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"other": "value"})
	}))
	defer server.Close()

	h := NewLogsHandler(log.New(os.Stdout, "", 0), "")
	client := &http.Client{}

	service := pasteService{
		name:         "test-missing-key",
		url:          server.URL,
		urlPrefix:    "https://paste.example.com/",
		jsonKeyField: "key",
	}

	_, err := h.tryPasteService(client, service, "test content")
	if err == nil {
		t.Fatal("expected error for missing key field, got nil")
	}
	if !strings.Contains(err.Error(), "missing or invalid 'key' field") {
		t.Errorf("expected missing key error, got: %v", err)
	}
}

func TestLogsHandler_TryPasteService_InvalidURLResponse(t *testing.T) {
	// Mock server that returns non-URL response when direct URL expected
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "not a url")
	}))
	defer server.Close()

	h := NewLogsHandler(log.New(os.Stdout, "", 0), "")
	client := &http.Client{}

	service := pasteService{
		name: "test-invalid-url",
		url:  server.URL,
	}

	_, err := h.tryPasteService(client, service, "test content")
	if err == nil {
		t.Fatal("expected error for non-URL response, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected response") {
		t.Errorf("expected unexpected response error, got: %v", err)
	}
}

func TestLogsHandler_SubmitToPaste_Fallback(t *testing.T) {
	// First server fails
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "service unavailable")
	}))
	defer failServer.Close()

	// Second server succeeds
	successServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "https://backup.paste.com/success123")
	}))
	defer successServer.Close()

	// Temporarily replace pasteServices for this test
	originalServices := pasteServices
	pasteServices = []pasteService{
		{
			name: "failing-service",
			url:  failServer.URL,
			headers: map[string]string{
				"Content-Type": "text/plain",
			},
		},
		{
			name: "backup-service",
			url:  successServer.URL,
			headers: map[string]string{
				"Content-Type": "text/plain",
			},
		},
	}
	defer func() { pasteServices = originalServices }()

	h := NewLogsHandler(log.New(os.Stdout, "", 0), "")

	result, err := h.submitToPaste("test content")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "https://backup.paste.com/success123" {
		t.Errorf("expected backup URL, got %s", result)
	}
}

func TestLogsHandler_SubmitToPaste_AllFail(t *testing.T) {
	// All servers fail
	failServer1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer failServer1.Close()

	failServer2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer failServer2.Close()

	originalServices := pasteServices
	pasteServices = []pasteService{
		{name: "fail1", url: failServer1.URL},
		{name: "fail2", url: failServer2.URL},
	}
	defer func() { pasteServices = originalServices }()

	h := NewLogsHandler(log.New(os.Stdout, "", 0), "")

	_, err := h.submitToPaste("test content")
	if err == nil {
		t.Fatal("expected error when all services fail, got nil")
	}
	if !strings.Contains(err.Error(), "all paste services failed") {
		t.Errorf("expected 'all paste services failed' error, got: %v", err)
	}
}

func TestLogsHandler_Submit_Success(t *testing.T) {
	// Create a mock paste server
	pasteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "https://paste.test.com/log123")
	}))
	defer pasteServer.Close()

	// Replace paste services temporarily
	originalServices := pasteServices
	pasteServices = []pasteService{
		{
			name: "test-paste",
			url:  pasteServer.URL,
			headers: map[string]string{
				"Content-Type": "text/plain",
			},
		},
	}
	defer func() { pasteServices = originalServices }()

	// Create a temp log file
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "backend.log")
	if err := os.WriteFile(logFile, []byte("line1\nline2\nline3\n"), 0644); err != nil {
		t.Fatalf("failed to create temp log file: %v", err)
	}

	h := NewLogsHandler(log.New(os.Stdout, "", 0), logFile)

	payload := submitLogsRequest{FrontendLogs: "frontend log entry"}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/logs/submit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.Submit(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var resp submitLogsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.URL != "https://paste.test.com/log123" {
		t.Errorf("expected URL https://paste.test.com/log123, got %s", resp.URL)
	}
	if resp.Error != "" {
		t.Errorf("unexpected error in response: %s", resp.Error)
	}
}

func TestLogsHandler_Submit_InvalidMethod(t *testing.T) {
	h := NewLogsHandler(log.New(os.Stdout, "", 0), "")

	req := httptest.NewRequest(http.MethodGet, "/api/logs/submit", nil)
	rec := httptest.NewRecorder()

	h.Submit(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rec.Code)
	}
}

func TestLogsHandler_Submit_InvalidPayload(t *testing.T) {
	h := NewLogsHandler(log.New(os.Stdout, "", 0), "")

	req := httptest.NewRequest(http.MethodPost, "/api/logs/submit", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.Submit(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}

	var resp submitLogsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Error == "" {
		t.Error("expected error message in response")
	}
}

func TestLogsHandler_ReadBackendLogs(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "test.log")

	// Create a log file with 10 lines
	var content strings.Builder
	for i := 1; i <= 10; i++ {
		content.WriteString(fmt.Sprintf("log line %d\n", i))
	}
	if err := os.WriteFile(logFile, []byte(content.String()), 0644); err != nil {
		t.Fatalf("failed to create log file: %v", err)
	}

	h := NewLogsHandler(log.New(os.Stdout, "", 0), logFile)

	logs, err := h.readBackendLogs()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(logs, "log line 1") {
		t.Error("expected logs to contain 'log line 1'")
	}
	if !strings.Contains(logs, "log line 10") {
		t.Error("expected logs to contain 'log line 10'")
	}
}

func TestLogsHandler_ReadBackendLogs_NoFile(t *testing.T) {
	h := NewLogsHandler(log.New(os.Stdout, "", 0), "")

	_, err := h.readBackendLogs()
	if err == nil {
		t.Fatal("expected error when no log file configured")
	}
}

func TestLogsHandler_ReadBackendLogs_MissingFile(t *testing.T) {
	h := NewLogsHandler(log.New(os.Stdout, "", 0), "/nonexistent/path/log.txt")

	_, err := h.readBackendLogs()
	if err == nil {
		t.Fatal("expected error for missing log file")
	}
}

func TestReadLastNLines(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "test.log")

	// Create a file with 100 lines
	var content strings.Builder
	for i := 1; i <= 100; i++ {
		content.WriteString(fmt.Sprintf("line %d\n", i))
	}
	if err := os.WriteFile(logFile, []byte(content.String()), 0644); err != nil {
		t.Fatalf("failed to create log file: %v", err)
	}

	file, err := os.Open(logFile)
	if err != nil {
		t.Fatalf("failed to open log file: %v", err)
	}
	defer file.Close()

	// Read last 10 lines
	lines, err := readLastNLines(file, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(lines) < 9 {
		t.Fatalf("expected at least 9 lines, got %d", len(lines))
	}

	// Verify we got recent lines (allowing for trailing newline handling variations)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "line 100") {
		t.Errorf("expected output to contain 'line 100', got: %s", joined)
	}
	if !strings.Contains(joined, "line 92") {
		t.Errorf("expected output to contain 'line 92', got: %s", joined)
	}
}

func TestReadLastNLines_EmptyFile(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "empty.log")

	if err := os.WriteFile(logFile, []byte{}, 0644); err != nil {
		t.Fatalf("failed to create empty log file: %v", err)
	}

	file, err := os.Open(logFile)
	if err != nil {
		t.Fatalf("failed to open log file: %v", err)
	}
	defer file.Close()

	lines, err := readLastNLines(file, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(lines) != 0 {
		t.Errorf("expected 0 lines for empty file, got %d", len(lines))
	}
}

func TestReadLastNLines_FewerLinesThanRequested(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "small.log")

	if err := os.WriteFile(logFile, []byte("line1\nline2\nline3\n"), 0644); err != nil {
		t.Fatalf("failed to create log file: %v", err)
	}

	file, err := os.Open(logFile)
	if err != nil {
		t.Fatalf("failed to open log file: %v", err)
	}
	defer file.Close()

	lines, err := readLastNLines(file, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify all 3 content lines are present
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "line1") || !strings.Contains(joined, "line2") || !strings.Contains(joined, "line3") {
		t.Errorf("expected all lines present, got: %v", lines)
	}
}

func TestPasteServicesConfiguration(t *testing.T) {
	// Verify the paste services are configured correctly
	if len(pasteServices) < 2 {
		t.Error("expected at least 2 paste services configured for fallback")
	}

	for i, svc := range pasteServices {
		if svc.name == "" {
			t.Errorf("service %d has empty name", i)
		}
		if svc.url == "" {
			t.Errorf("service %s has empty URL", svc.name)
		}
		if svc.jsonKeyField != "" && svc.urlPrefix == "" {
			t.Errorf("service %s has jsonKeyField but no urlPrefix", svc.name)
		}
	}
}
