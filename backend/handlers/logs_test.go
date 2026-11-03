package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/models"
)

type fakeLogClientActivityService struct {
	lastSeenIDs []string
}

func (s *fakeLogClientActivityService) UpdateLastSeen(id string) error {
	s.lastSeenIDs = append(s.lastSeenIDs, id)
	return nil
}

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

func TestDeidentifyPlaybackProgressRedactsLocalIdentifiers(t *testing.T) {
	item := models.PlaybackProgress{
		ID:        "episode:localmedia:/Users/alice/Shows/The Office/S02E08.mkv",
		MediaType: "episode",
		ItemID:    "localmedia:/Users/alice/Shows/The Office/S02E08.mkv",
		SeriesID:  "/Users/alice/Shows/The Office",
		ExternalIDs: map[string]string{
			"imdb":  "tt0386676",
			"local": "/Volumes/Media/The Office/S02E08.mkv",
		},
	}

	got := deidentifyPlaybackProgress(item)

	if strings.Contains(got.ID, "/Users/alice") || strings.Contains(got.ItemID, "/Users/alice") || strings.Contains(got.SeriesID, "/Users/alice") {
		t.Fatalf("expected local identifiers to be redacted, got: %+v", got)
	}
	if !strings.HasPrefix(got.ItemID, "redacted-local:") {
		t.Fatalf("expected item ID to be redacted, got %q", got.ItemID)
	}
	if got.ExternalIDs["imdb"] != "tt0386676" {
		t.Fatalf("expected public media ID to remain intact, got %q", got.ExternalIDs["imdb"])
	}
	if !strings.HasPrefix(got.ExternalIDs["local"], "redacted-local:") {
		t.Fatalf("expected local external ID to be redacted, got %q", got.ExternalIDs["local"])
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

func TestLogsHandler_UploadFrontendLogs_AndListSnapshots(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "backend.log")
	if err := os.WriteFile(logFile, []byte("backend line\n"), 0644); err != nil {
		t.Fatalf("failed to create temp log file: %v", err)
	}

	h := NewLogsHandler(log.New(os.Stdout, "", 0), logFile)
	activity := &fakeLogClientActivityService{}
	h.SetClientsService(activity)

	body := `{"frontendLogs":"one\ntwo","deviceType":"Android TV","os":"Android","appVersion":"1.2.3"}`
	req := httptest.NewRequest(http.MethodPost, "/api/logs/frontend", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-ID", "client-123")
	rec := httptest.NewRecorder()

	h.UploadFrontendLogs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	summaries, err := h.ListFrontendLogSummaries()
	if err != nil {
		t.Fatalf("unexpected error listing snapshots: %v", err)
	}

	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if summaries[0].ClientID != "client-123" {
		t.Fatalf("expected client id client-123, got %s", summaries[0].ClientID)
	}
	if summaries[0].LogCount != 2 {
		t.Fatalf("expected log count 2, got %d", summaries[0].LogCount)
	}
	if len(activity.lastSeenIDs) != 1 || activity.lastSeenIDs[0] != "client-123" {
		t.Fatalf("expected frontend upload to refresh client last seen, got %#v", activity.lastSeenIDs)
	}

	snapshot, err := h.GetFrontendLogSnapshot("client-123")
	if err != nil {
		t.Fatalf("unexpected error reading snapshot: %v", err)
	}
	if snapshot.DeviceType != "Android TV" {
		t.Fatalf("expected device type Android TV, got %s", snapshot.DeviceType)
	}
	if !strings.Contains(snapshot.FrontendLogs, "two") {
		t.Fatalf("expected stored frontend logs to include latest content")
	}
}

func TestLogsHandler_SubmitStoredLogsPackage_UsesStoredFrontendLogs(t *testing.T) {
	pasteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		content := string(body)
		if !strings.Contains(content, "backend line 1") {
			t.Fatalf("expected package to include backend logs")
		}
		if !strings.Contains(content, "frontend line 1") {
			t.Fatalf("expected package to include stored frontend logs")
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "https://paste.test.com/combined123")
	}))
	defer pasteServer.Close()

	originalServices := pasteServices
	pasteServices = []pasteService{{
		name: "test-paste",
		url:  pasteServer.URL,
		headers: map[string]string{
			"Content-Type": "text/plain",
		},
	}}
	defer func() { pasteServices = originalServices }()

	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "backend.log")
	if err := os.WriteFile(logFile, []byte("backend line 1\nbackend line 2\n"), 0644); err != nil {
		t.Fatalf("failed to create temp log file: %v", err)
	}

	h := NewLogsHandler(log.New(os.Stdout, "", 0), logFile)

	uploadReq := httptest.NewRequest(http.MethodPost, "/api/logs/frontend", strings.NewReader(`{"frontendLogs":"frontend line 1\nfrontend line 2"}`))
	uploadReq.Header.Set("Content-Type", "application/json")
	uploadReq.Header.Set("X-Client-ID", "client-456")
	uploadRec := httptest.NewRecorder()
	h.UploadFrontendLogs(uploadRec, uploadReq)
	if uploadRec.Code != http.StatusOK {
		t.Fatalf("expected upload status %d, got %d", http.StatusOK, uploadRec.Code)
	}

	url, err := h.SubmitStoredLogsPackage("client-456")
	if err != nil {
		t.Fatalf("unexpected error submitting stored package: %v", err)
	}
	if url != "https://paste.test.com/combined123" {
		t.Fatalf("expected paste url https://paste.test.com/combined123, got %s", url)
	}
}

func TestLogsHandler_SubmitStoredLogsPackage_RedactsSecretsBeforeUpload(t *testing.T) {
	var uploadedContent string
	pasteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		uploadedContent = string(body)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "https://paste.test.com/redacted123")
	}))
	defer pasteServer.Close()

	originalServices := pasteServices
	pasteServices = []pasteService{{
		name: "test-paste",
		url:  pasteServer.URL,
		headers: map[string]string{
			"Content-Type": "text/plain",
		},
	}}
	defer func() { pasteServices = originalServices }()

	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "backend.log")
	backendLog := strings.Join([]string{
		`2026/03/22 10:00:00 Authorization: Bearer backend-secret-token`,
		`2026/03/22 10:00:01 databaseUrl="postgres://dbuser:db-password@localhost:5432/mediastorm"`,
		`2026/03/22 10:00:02 /api/settings?token=query-secret&pin=1234`,
		`2026/03/22 10:00:03 url=https://private.example.test/video.mkv`,
		`2026/03/22 10:00:04 key=abcDEF1234567890ghiJKL1234567890mnopQR`,
		`2026/03/22 10:00:05 {"xtreamUsername":"iptv-viewer"}`,
		`2026/03/22 10:00:06 {"path":"network.homeWifiSSID","before":"Old Network","after":"Private Network"}`,
	}, "\n")
	if err := os.WriteFile(logFile, []byte(backendLog), 0644); err != nil {
		t.Fatalf("failed to create temp log file: %v", err)
	}

	h := NewLogsHandler(log.New(os.Stdout, "", 0), logFile)

	frontendLog := `{"apiKey":"frontend-api-secret","accessToken":"frontend-access-secret","homeWifiSSID":"Private Network","currentSSID":"Private Network","url":"https://user:webdav-secret@example.com/movie.mkv?token=frontend-query-secret"}`
	uploadBody, err := json.Marshal(map[string]string{"frontendLogs": frontendLog})
	if err != nil {
		t.Fatalf("failed to marshal upload body: %v", err)
	}
	uploadReq := httptest.NewRequest(http.MethodPost, "/api/logs/frontend", bytes.NewReader(uploadBody))
	uploadReq.Header.Set("Content-Type", "application/json")
	uploadReq.Header.Set("X-Client-ID", "client-secret")
	uploadRec := httptest.NewRecorder()
	h.UploadFrontendLogs(uploadRec, uploadReq)
	if uploadRec.Code != http.StatusOK {
		t.Fatalf("expected upload status %d, got %d", http.StatusOK, uploadRec.Code)
	}
	storedEntries, err := h.readFrontendLogEntries(100, "client-secret")
	if err != nil {
		t.Fatalf("failed to read stored frontend logs: %v", err)
	}
	storedFrontendLogs := make([]string, 0, len(storedEntries))
	for _, entry := range storedEntries {
		storedFrontendLogs = append(storedFrontendLogs, entry.Line)
	}
	storedFrontendContent := strings.Join(storedFrontendLogs, "\n")
	for _, secret := range []string{"frontend-api-secret", "frontend-access-secret", "Private Network"} {
		if strings.Contains(storedFrontendContent, secret) {
			t.Fatalf("stored frontend logs leaked %q in:\n%s", secret, storedFrontendContent)
		}
	}

	url, err := h.SubmitStoredLogsPackage("client-secret")
	if err != nil {
		t.Fatalf("unexpected error submitting stored package: %v", err)
	}
	if url != "https://paste.test.com/redacted123" {
		t.Fatalf("expected paste url https://paste.test.com/redacted123, got %s", url)
	}

	for _, secret := range []string{
		"backend-secret-token",
		"db-password",
		"query-secret",
		"frontend-api-secret",
		"frontend-access-secret",
		"webdav-secret",
		"frontend-query-secret",
		"https://private.example.test/video.mkv",
		"abcDEF1234567890ghiJKL1234567890mnopQR",
		"iptv-viewer",
		"Old Network",
		"Private Network",
	} {
		if strings.Contains(uploadedContent, secret) {
			t.Fatalf("uploaded log package leaked %q in:\n%s", secret, uploadedContent)
		}
	}
	if !strings.Contains(uploadedContent, logRedacted) {
		t.Fatalf("expected uploaded log package to contain redaction marker, got:\n%s", uploadedContent)
	}
}

func TestLogsHandler_ReadCombinedLogEntries_AllOrigins(t *testing.T) {
	pinLogDisplayZone(t, time.UTC)
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "backend.log")
	if err := os.WriteFile(logFile, []byte("2026/03/22 10:00:00 [backend] backend line 1\n"), 0644); err != nil {
		t.Fatalf("failed to create temp log file: %v", err)
	}

	h := NewLogsHandler(log.New(os.Stdout, "", 0), logFile)

	uploadReq := httptest.NewRequest(http.MethodPost, "/api/logs/frontend", strings.NewReader(`{"frontendLogs":"[2026-03-22T10:00:01Z] [INFO ] frontend line 1"}`))
	uploadReq.Header.Set("Content-Type", "application/json")
	uploadReq.Header.Set("X-Client-ID", "client-789")
	uploadRec := httptest.NewRecorder()
	h.UploadFrontendLogs(uploadRec, uploadReq)
	if uploadRec.Code != http.StatusOK {
		t.Fatalf("expected upload status %d, got %d", http.StatusOK, uploadRec.Code)
	}

	entries, err := h.ReadCombinedLogEntries(1000, "all", "")
	if err != nil {
		t.Fatalf("unexpected error reading combined entries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 combined entries, got %d", len(entries))
	}
	if entries[0].Origin != "backend" {
		t.Fatalf("expected first entry to be backend, got %s", entries[0].Origin)
	}
	if entries[1].Origin != "frontend" {
		t.Fatalf("expected second entry to be frontend, got %s", entries[1].Origin)
	}
	expectedTag := "frontend:" + truncateLogIdentifier("client-789")
	if !strings.Contains(entries[1].Line, expectedTag) {
		t.Fatalf("expected frontend line decoration, got %s", entries[1].Line)
	}
}

func TestLogsHandler_ReadCombinedLogEntries_FilterByClient(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "backend.log")
	if err := os.WriteFile(logFile, []byte("2026/03/22 10:00:00 [backend] backend line 1\n"), 0644); err != nil {
		t.Fatalf("failed to create temp log file: %v", err)
	}

	h := NewLogsHandler(log.New(os.Stdout, "", 0), logFile)

	for _, tc := range []struct {
		clientID string
		body     string
	}{
		{clientID: "client-a", body: `{"frontendLogs":"[2026-03-22T10:00:01Z] [INFO ] frontend line a"}`},
		{clientID: "client-b", body: `{"frontendLogs":"[2026-03-22T10:00:02Z] [INFO ] frontend line b"}`},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/logs/frontend", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Client-ID", tc.clientID)
		rec := httptest.NewRecorder()
		h.UploadFrontendLogs(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected upload status %d for %s, got %d", http.StatusOK, tc.clientID, rec.Code)
		}
	}

	entries, err := h.ReadCombinedLogEntries(1000, "frontend", "client-b")
	if err != nil {
		t.Fatalf("unexpected error reading client-filtered entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 frontend entry, got %d", len(entries))
	}
	if !strings.Contains(entries[0].Line, "client-b") {
		t.Fatalf("expected filtered frontend entry to contain client-b, got %s", entries[0].Line)
	}
}

func TestLogsHandler_ReadFrontendLogEntries_PreservesMultilineTimestampAndOrder(t *testing.T) {
	pinLogDisplayZone(t, time.UTC)
	tempDir := t.TempDir()
	h := NewLogsHandler(log.New(os.Stdout, "", 0), filepath.Join(tempDir, "backend.log"))

	frontendLogs := strings.Join([]string{
		`2026-07-22T22:55:43.123Z [LOG  ] [RouteTrace] {`,
		`  "pathname": "/",`,
		`  "platform": "ios"`,
		`}`,
		`2026-07-22T22:55:44.456Z [INFO ] next event`,
	}, "\n")
	body, err := json.Marshal(uploadFrontendLogsRequest{FrontendLogs: frontendLogs})
	if err != nil {
		t.Fatalf("failed to marshal frontend logs: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/logs/frontend", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-ID", "iphone-client")
	rec := httptest.NewRecorder()
	h.UploadFrontendLogs(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected upload status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	entries, err := h.readFrontendLogEntries(100, "iphone-client")
	if err != nil {
		t.Fatalf("unexpected frontend log read error: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("expected 5 frontend lines, got %d", len(entries))
	}
	for i := 1; i <= 3; i++ {
		if !entries[i].Timestamp.Equal(entries[0].Timestamp) {
			t.Fatalf("continuation %d timestamp %s did not match parent %s", i, entries[i].Timestamp, entries[0].Timestamp)
		}
		if !strings.Contains(entries[i].Line, "2026-07-22T22:55:43.123Z [CONT ]") {
			t.Fatalf("continuation %d missing visible parent timestamp: %q", i, entries[i].Line)
		}
	}
	if !strings.Contains(entries[1].Line, `"pathname": "/"`) || !strings.Contains(entries[2].Line, `"platform": "ios"`) || !strings.HasSuffix(entries[3].Line, "}") {
		t.Fatalf("multiline entry order was not preserved: %#v", entries)
	}
	if !entries[4].Timestamp.After(entries[0].Timestamp) || !strings.Contains(entries[4].Line, "next event") {
		t.Fatalf("expected later event after multiline entry, got %#v", entries[4])
	}
}

func TestVisibleLogTimestamp_PreservesLegacyLocalTimeWithoutInventingZone(t *testing.T) {
	line := `2026/07/22 16:55:43 [LOG  ] legacy event`
	parsed := parseLogTimestamp(line, time.Time{})
	if parsed.IsZero() {
		t.Fatal("expected legacy timestamp to parse")
	}
	if got := visibleLogTimestamp(line, parsed); got != "2026/07/22 16:55:43" {
		t.Fatalf("expected legacy local timestamp to remain zone-less, got %q", got)
	}
}

func TestLogsHandler_ReadAggregatedFrontendLogs_Selection(t *testing.T) {
	tempDir := t.TempDir()
	h := NewLogsHandler(log.New(os.Stdout, "", 0), filepath.Join(tempDir, "backend.log"))

	for _, tc := range []struct {
		clientID string
		line     string
	}{
		{clientID: "client-a", line: "frontend line a"},
		{clientID: "client-b", line: "frontend line b"},
	} {
		reqBody, err := json.Marshal(uploadFrontendLogsRequest{FrontendLogs: tc.line})
		if err != nil {
			t.Fatalf("failed to marshal frontend logs: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/logs/frontend", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Client-ID", tc.clientID)
		rec := httptest.NewRecorder()
		h.UploadFrontendLogs(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected upload status %d for %s, got %d", http.StatusOK, tc.clientID, rec.Code)
		}
	}

	selected, summaries, err := h.readAggregatedFrontendLogs(maxLogLines, "client-b")
	if err != nil {
		t.Fatalf("unexpected selected-client error: %v", err)
	}
	if len(summaries) != 1 || summaries[0].ClientID != "client-b" {
		t.Fatalf("expected only client-b summary, got %#v", summaries)
	}
	if !strings.Contains(selected, "frontend line b") || strings.Contains(selected, "frontend line a") {
		t.Fatalf("expected only client-b logs, got %q", selected)
	}

	backendOnly, summaries, err := h.readAggregatedFrontendLogs(maxLogLines, "")
	if err != nil {
		t.Fatalf("unexpected backend-only error: %v", err)
	}
	if backendOnly != "" || len(summaries) != 0 {
		t.Fatalf("expected no frontend logs for backend-only package, got %q and %#v", backendOnly, summaries)
	}

	all, summaries, err := h.readAggregatedFrontendLogs(maxLogLines, "*")
	if err != nil {
		t.Fatalf("unexpected all-clients error: %v", err)
	}
	if len(summaries) != 2 || !strings.Contains(all, "frontend line a") || !strings.Contains(all, "frontend line b") {
		t.Fatalf("expected both frontend clients, got %q and %#v", all, summaries)
	}

	if _, _, err := h.readAggregatedFrontendLogs(maxLogLines, "missing-client"); err == nil {
		t.Fatal("expected an error for an unknown frontend client")
	}
}

func TestEnrichFrontendLogSummaries_AddsClientIdentityAndLastSeen(t *testing.T) {
	lastSeen := time.Date(2026, time.July, 22, 22, 55, 44, 0, time.UTC)
	summaries := []frontendLogSummary{{
		ClientID:   "iphone-client",
		DeviceType: "iPhone",
		UploadedAt: lastSeen.Add(-time.Minute),
	}}
	clients := []models.Client{{
		ID:         "iphone-client",
		UserID:     "profile-godver3",
		Name:       "iPhone - iOS",
		DeviceName: "iPhone 15 Pro Max",
		DeviceType: "iPhone",
		OS:         "iOS",
		AppVersion: "1.5.0+20260722",
		LastSeenAt: lastSeen,
	}}
	users := []models.User{{ID: "profile-godver3", Name: "godver3"}}

	got := enrichFrontendLogSummaries(summaries, clients, users)
	if len(got) != 1 {
		t.Fatalf("expected one summary, got %d", len(got))
	}
	if got[0].DeviceName != "iPhone 15 Pro Max" || got[0].Name != "iPhone - iOS" {
		t.Fatalf("expected friendly client identity, got %#v", got[0])
	}
	if got[0].ProfileName != "godver3" {
		t.Fatalf("expected profile name godver3, got %q", got[0].ProfileName)
	}
	if got[0].AppVersion != "1.5.0+20260722" {
		t.Fatalf("expected registered app build, got %q", got[0].AppVersion)
	}
	if got[0].LastSeenAt == nil || !got[0].LastSeenAt.Equal(lastSeen) {
		t.Fatalf("expected last seen %s, got %v", lastSeen, got[0].LastSeenAt)
	}
}

func TestEnrichFrontendLogSummaries_UsesNewestProfileAssociation(t *testing.T) {
	newest := time.Date(2026, time.August, 17, 22, 0, 0, 0, time.UTC)
	older := newest.Add(-time.Hour)
	summaries := []frontendLogSummary{{ClientID: "shared-ipad", UploadedAt: newest}}
	clients := []models.Client{
		{ID: "shared-ipad", UserID: "yia", LastSeenAt: newest},
		{ID: "shared-ipad", UserID: "playa", LastSeenAt: older},
	}
	users := []models.User{{ID: "yia", Name: "Yia"}, {ID: "playa", Name: "Playa"}}

	got := enrichFrontendLogSummaries(summaries, clients, users)
	if got[0].ProfileName != "Yia" {
		t.Fatalf("expected newest profile Yia, got %q", got[0].ProfileName)
	}
	if got[0].LastSeenAt == nil || !got[0].LastSeenAt.Equal(newest) {
		t.Fatalf("expected newest last-seen %s, got %v", newest, got[0].LastSeenAt)
	}
}

func TestLogsHandler_LogPackagesUseLastTenThousandLines(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "backend.log")

	var content strings.Builder
	for i := 1; i <= maxLogLines+5; i++ {
		content.WriteString(fmt.Sprintf("line-%05d\n", i))
	}
	if err := os.WriteFile(logFile, []byte(content.String()), 0o644); err != nil {
		t.Fatalf("failed to create backend log: %v", err)
	}

	h := NewLogsHandler(log.New(os.Stdout, "", 0), logFile)
	backendLogs, err := h.readBackendLogs()
	if err != nil {
		t.Fatalf("unexpected backend log error: %v", err)
	}
	backendLines := strings.Split(backendLogs, "\n")
	if len(backendLines) != maxLogLines {
		t.Fatalf("expected %d backend lines, got %d", maxLogLines, len(backendLines))
	}
	if backendLines[0] != "line-00006" || backendLines[len(backendLines)-1] != "line-10005" {
		t.Fatalf("unexpected backend line range: %q through %q", backendLines[0], backendLines[len(backendLines)-1])
	}

	frontendLogs := lastNLogLines(content.String(), maxLogLines)
	frontendLines := strings.Split(frontendLogs, "\n")
	if len(frontendLines) != maxLogLines {
		t.Fatalf("expected %d frontend lines, got %d", maxLogLines, len(frontendLines))
	}
	if frontendLines[0] != "line-00006" || frontendLines[len(frontendLines)-1] != "line-10005" {
		t.Fatalf("unexpected frontend line range: %q through %q", frontendLines[0], frontendLines[len(frontendLines)-1])
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

func TestLocalizeLeadingLogTimestamp_RendersInDisplayZone(t *testing.T) {
	pinLogDisplayZone(t, time.FixedZone("EST", -5*3600))

	cases := map[string]string{
		`2026-11-02T23:09:06.815Z I [Player] play`:   `2026-11-02T18:09:06.815-05:00 I [Player] play`,
		`[2026-03-22T10:00:01Z] [INFO ] frontend`:    `[2026-03-22T05:00:01-05:00] [INFO ] frontend`,
		`2026/11/02 21:15:04 filter.go:428: backend`: `2026/11/02 21:15:04 filter.go:428: backend`,
		`  "pathname": "/"`:                          `  "pathname": "/"`,
	}
	for in, want := range cases {
		if got := localizeLeadingLogTimestamp(in); got != want {
			t.Errorf("localize(%q) = %q, want %q", in, got, want)
		}
	}
}

// pinLogDisplayZone fixes the server zone log timestamps are parsed and shown
// in, so assertions don't depend on the machine running the tests.
func pinLogDisplayZone(t *testing.T, loc *time.Location) {
	t.Helper()
	prev := logDisplayLocation
	logDisplayLocation = func() *time.Location { return loc }
	t.Cleanup(func() { logDisplayLocation = prev })
}

func TestReadCombinedLogEntries_OrdersLocalBackendAgainstUTCFrontend(t *testing.T) {
	// Server TZ is EST: the backend line at 18:00 local is 23:00Z, so it is
	// newer than a frontend line stamped 22:30Z.
	pinLogDisplayZone(t, time.FixedZone("EST", -5*3600))
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "backend.log")
	if err := os.WriteFile(logFile, []byte("2026/11/02 18:00:00 [backend] local line\n"), 0644); err != nil {
		t.Fatalf("failed to create temp log file: %v", err)
	}
	h := NewLogsHandler(log.New(os.Stdout, "", 0), logFile)
	req := httptest.NewRequest(http.MethodPost, "/api/logs/frontend", strings.NewReader(`{"frontendLogs":"2026-11-02T22:30:00.000Z I [Home] earlier"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-ID", "client-tz")
	rec := httptest.NewRecorder()
	h.UploadFrontendLogs(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status %d", rec.Code)
	}

	entries, err := h.ReadCombinedLogEntries(1000, "all", "")
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
	if entries[0].Origin != "frontend" || entries[1].Origin != "backend" {
		t.Fatalf("order = [%s %s], want [frontend backend]", entries[0].Origin, entries[1].Origin)
	}
	if !strings.Contains(entries[0].Line, "2026-11-02T17:30:00-05:00 I [Home] earlier") {
		t.Fatalf("frontend line not shown in server zone: %q", entries[0].Line)
	}
}
