package handlers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxLogLines   = 1000
	maxUploadSize = 10 << 20 // 10 MB limit for frontend logs
)

// pasteService defines a paste service configuration
type pasteService struct {
	name         string
	url          string
	urlPrefix    string            // if response is a key, prepend this to form the full URL
	headers      map[string]string // additional headers to send
	jsonKeyField string            // if non-empty, parse JSON response and extract this field as the key
}

// pasteServices is the ordered list of paste services to try
var pasteServices = []pasteService{
	{
		name: "paste.c-net.org",
		url:  "https://paste.c-net.org/",
		headers: map[string]string{
			"Content-Type": "text/plain; charset=utf-8",
			"x-uuid":       "1",
		},
	},
	{
		name: "paste.rs",
		url:  "https://paste.rs",
		headers: map[string]string{
			"Content-Type": "text/plain; charset=utf-8",
		},
	},
	{
		name:         "bytebin.lucko.me",
		url:          "https://bytebin.lucko.me/post",
		urlPrefix:    "https://bytebin.lucko.me/",
		jsonKeyField: "key",
		headers: map[string]string{
			"Content-Type": "text/plain; charset=utf-8",
		},
	},
	{
		name:         "pastes.dev",
		url:          "https://api.pastes.dev/post",
		urlPrefix:    "https://pastes.dev/",
		jsonKeyField: "key",
		headers: map[string]string{
			"Content-Type": "text/plain; charset=utf-8",
		},
	},
}

type LogsHandler struct {
	logger          *log.Logger
	logFile         string // path to the backend log file
	frontendLogsDir string
	frontendLogsMu  sync.RWMutex
}

type submitLogsRequest struct {
	FrontendLogs string `json:"frontendLogs"`
}

type uploadFrontendLogsRequest struct {
	FrontendLogs string `json:"frontendLogs"`
	DeviceType   string `json:"deviceType,omitempty"`
	OS           string `json:"os,omitempty"`
	AppVersion   string `json:"appVersion,omitempty"`
}

type frontendLogSnapshot struct {
	ClientID     string    `json:"clientId"`
	DeviceType   string    `json:"deviceType,omitempty"`
	OS           string    `json:"os,omitempty"`
	AppVersion   string    `json:"appVersion,omitempty"`
	UploadedAt   time.Time `json:"uploadedAt"`
	LogCount     int       `json:"logCount"`
	FrontendLogs string    `json:"frontendLogs"`
}

type frontendLogSummary struct {
	ClientID   string    `json:"clientId"`
	DeviceType string    `json:"deviceType,omitempty"`
	OS         string    `json:"os,omitempty"`
	AppVersion string    `json:"appVersion,omitempty"`
	UploadedAt time.Time `json:"uploadedAt"`
	LogCount   int       `json:"logCount"`
}

type logEntry struct {
	Origin    string    `json:"origin"`
	ClientID  string    `json:"clientId,omitempty"`
	Timestamp time.Time `json:"timestamp,omitempty"`
	Line      string    `json:"line"`
}

type submitLogsResponse struct {
	URL   string `json:"url,omitempty"`
	Error string `json:"error,omitempty"`
}

func NewLogsHandler(logger *log.Logger, logFile string) *LogsHandler {
	h := &LogsHandler{
		logger:  logger,
		logFile: logFile,
	}
	if h.logger == nil {
		h.logger = log.New(os.Stdout, "", log.LstdFlags)
	}
	h.frontendLogsDir = h.defaultFrontendLogsDir()
	return h
}

func (h *LogsHandler) Submit(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		h.respondError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload submitLogsRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxUploadSize))
	if err := decoder.Decode(&payload); err != nil {
		h.respondError(w, fmt.Sprintf("invalid payload: %v", err), http.StatusBadRequest)
		return
	}

	pasteURL, err := h.SubmitLogsPackage(payload.FrontendLogs, nil)
	if err != nil {
		h.logger.Printf("[logs] Failed to submit to paste service: %v", err)
		h.respondError(w, fmt.Sprintf("failed to submit logs: %v", err), http.StatusInternalServerError)
		return
	}

	h.logger.Printf("[logs] Successfully submitted logs to %s", pasteURL)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(submitLogsResponse{URL: pasteURL})
}

func (h *LogsHandler) UploadFrontendLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		h.respondError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	clientID := strings.TrimSpace(r.Header.Get("X-Client-ID"))
	if clientID == "" {
		h.respondError(w, "client id is required", http.StatusBadRequest)
		return
	}

	var payload uploadFrontendLogsRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxUploadSize))
	if err := decoder.Decode(&payload); err != nil {
		h.respondError(w, fmt.Sprintf("invalid payload: %v", err), http.StatusBadRequest)
		return
	}

	snapshot := frontendLogSnapshot{
		ClientID:     clientID,
		DeviceType:   strings.TrimSpace(payload.DeviceType),
		OS:           strings.TrimSpace(payload.OS),
		AppVersion:   strings.TrimSpace(payload.AppVersion),
		UploadedAt:   time.Now().UTC(),
		LogCount:     countLogLines(payload.FrontendLogs),
		FrontendLogs: payload.FrontendLogs,
	}

	if err := h.saveFrontendLogSnapshot(snapshot); err != nil {
		h.logger.Printf("[logs] Failed to store frontend logs for client %s: %v", clientID, err)
		h.respondError(w, fmt.Sprintf("failed to store frontend logs: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"clientId":   snapshot.ClientID,
		"logCount":   snapshot.LogCount,
		"uploadedAt": snapshot.UploadedAt,
	})
}

func (h *LogsHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (h *LogsHandler) ListFrontendLogSummaries() ([]frontendLogSummary, error) {
	h.frontendLogsMu.RLock()
	defer h.frontendLogsMu.RUnlock()

	entries, err := os.ReadDir(h.frontendLogsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	summaries := make([]frontendLogSummary, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		snapshot, err := h.readFrontendLogSnapshotByPath(filepath.Join(h.frontendLogsDir, entry.Name()))
		if err != nil {
			continue
		}

		summaries = append(summaries, frontendLogSummary{
			ClientID:   snapshot.ClientID,
			DeviceType: snapshot.DeviceType,
			OS:         snapshot.OS,
			AppVersion: snapshot.AppVersion,
			UploadedAt: snapshot.UploadedAt,
			LogCount:   snapshot.LogCount,
		})
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UploadedAt.After(summaries[j].UploadedAt)
	})

	return summaries, nil
}

func (h *LogsHandler) GetFrontendLogSnapshot(clientID string) (*frontendLogSnapshot, error) {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return nil, fmt.Errorf("client id is required")
	}

	h.frontendLogsMu.RLock()
	defer h.frontendLogsMu.RUnlock()

	path := h.frontendLogSnapshotPath(clientID)
	snapshot, err := h.readFrontendLogSnapshotByPath(path)
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (h *LogsHandler) SubmitLogsPackage(frontendLogs string, snapshot *frontendLogSnapshot) (string, error) {
	pasteContent := h.buildCombinedLogPackage(frontendLogs, snapshot)
	return h.submitToPaste(pasteContent)
}

func (h *LogsHandler) SubmitStoredLogsPackage(clientID string) (string, error) {
	frontendLogs, summaries, err := h.readAggregatedFrontendLogs(maxLogLines, clientID)
	if err != nil {
		return "", err
	}
	pasteContent := h.buildCombinedStoredLogsPackage(frontendLogs, summaries)
	return h.submitToPaste(pasteContent)
}

func (h *LogsHandler) ReadCombinedLogEntries(linesCount int, source, clientID string) ([]logEntry, error) {
	source = strings.TrimSpace(strings.ToLower(source))
	if source == "" {
		source = "all"
	}
	clientID = strings.TrimSpace(clientID)

	entries := make([]logEntry, 0, linesCount*2)

	if source == "all" || source == "backend" {
		backendEntries, err := h.readBackendLogEntries(linesCount)
		if err != nil {
			return nil, err
		}
		entries = append(entries, backendEntries...)
	}

	if source == "all" || source == "frontend" {
		frontendEntries, err := h.readFrontendLogEntries(linesCount, clientID)
		if err != nil {
			return nil, err
		}
		entries = append(entries, frontendEntries...)
	}

	sort.SliceStable(entries, func(i, j int) bool {
		aHasTime := !entries[i].Timestamp.IsZero()
		bHasTime := !entries[j].Timestamp.IsZero()
		switch {
		case aHasTime && bHasTime:
			return entries[i].Timestamp.Before(entries[j].Timestamp)
		case aHasTime:
			return false
		case bHasTime:
			return true
		default:
			return entries[i].Line < entries[j].Line
		}
	})

	return entries, nil
}

func (h *LogsHandler) readBackendLogs() (string, error) {
	if h.logFile == "" {
		return "", fmt.Errorf("no log file configured")
	}

	logFile, err := os.Open(h.logFile)
	if err != nil {
		return "", fmt.Errorf("could not open log file %s: %w", h.logFile, err)
	}
	defer logFile.Close()

	// Read last N lines using a ring buffer approach
	lines, err := readLastNLines(logFile, maxLogLines)
	if err != nil {
		return "", err
	}

	return strings.Join(lines, "\n"), nil
}

func (h *LogsHandler) buildCombinedLogPackage(frontendLogs string, snapshot *frontendLogSnapshot) string {
	var combined strings.Builder

	combined.WriteString("═══════════════════════════════════════════════════════════════════════\n")
	combined.WriteString("                         STRMR LOG SUBMISSION\n")
	combined.WriteString("═══════════════════════════════════════════════════════════════════════\n")
	combined.WriteString(fmt.Sprintf("Submitted: %s\n", time.Now().UTC().Format(time.RFC3339)))
	combined.WriteString(fmt.Sprintf("Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH))
	if snapshot != nil {
		combined.WriteString(fmt.Sprintf("Frontend Client: %s\n", snapshot.ClientID))
		if snapshot.DeviceType != "" || snapshot.OS != "" {
			combined.WriteString(fmt.Sprintf("Frontend Device: %s %s\n", snapshot.DeviceType, snapshot.OS))
		}
		if snapshot.AppVersion != "" {
			combined.WriteString(fmt.Sprintf("Frontend App Version: %s\n", snapshot.AppVersion))
		}
		combined.WriteString(fmt.Sprintf("Frontend Uploaded: %s\n", snapshot.UploadedAt.Format(time.RFC3339)))
	}
	combined.WriteString("\n")

	combined.WriteString("═══════════════════════════════════════════════════════════════════════\n")
	combined.WriteString("                           BACKEND LOGS\n")
	combined.WriteString(fmt.Sprintf("                     (last %d lines)\n", maxLogLines))
	combined.WriteString("═══════════════════════════════════════════════════════════════════════\n\n")

	backendLogs, err := h.readBackendLogs()
	if err != nil {
		combined.WriteString(fmt.Sprintf("[Error reading backend logs: %v]\n", err))
	} else if backendLogs == "" {
		combined.WriteString("[No backend logs available]\n")
	} else {
		combined.WriteString(backendLogs)
	}
	combined.WriteString("\n\n")

	combined.WriteString("═══════════════════════════════════════════════════════════════════════\n")
	combined.WriteString("                          FRONTEND LOGS\n")
	combined.WriteString(fmt.Sprintf("                     (last %d entries)\n", maxLogLines))
	combined.WriteString("═══════════════════════════════════════════════════════════════════════\n\n")

	if strings.TrimSpace(frontendLogs) == "" {
		combined.WriteString("[No frontend logs provided]\n")
	} else {
		combined.WriteString(frontendLogs)
	}

	return combined.String()
}

func (h *LogsHandler) buildCombinedStoredLogsPackage(frontendLogs string, summaries []frontendLogSummary) string {
	var combined strings.Builder

	combined.WriteString("═══════════════════════════════════════════════════════════════════════\n")
	combined.WriteString("                         STRMR LOG SUBMISSION\n")
	combined.WriteString("═══════════════════════════════════════════════════════════════════════\n")
	combined.WriteString(fmt.Sprintf("Submitted: %s\n", time.Now().UTC().Format(time.RFC3339)))
	combined.WriteString(fmt.Sprintf("Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH))
	if len(summaries) > 0 {
		combined.WriteString(fmt.Sprintf("Frontend Clients Included: %d\n", len(summaries)))
	}
	combined.WriteString("\n")

	combined.WriteString("═══════════════════════════════════════════════════════════════════════\n")
	combined.WriteString("                           BACKEND LOGS\n")
	combined.WriteString(fmt.Sprintf("                     (last %d lines)\n", maxLogLines))
	combined.WriteString("═══════════════════════════════════════════════════════════════════════\n\n")

	backendLogs, err := h.readBackendLogs()
	if err != nil {
		combined.WriteString(fmt.Sprintf("[Error reading backend logs: %v]\n", err))
	} else if backendLogs == "" {
		combined.WriteString("[No backend logs available]\n")
	} else {
		combined.WriteString(backendLogs)
	}
	combined.WriteString("\n\n")

	combined.WriteString("═══════════════════════════════════════════════════════════════════════\n")
	combined.WriteString("                          FRONTEND LOGS\n")
	combined.WriteString(fmt.Sprintf("                     (last %d lines total)\n", maxLogLines))
	combined.WriteString("═══════════════════════════════════════════════════════════════════════\n\n")

	if len(summaries) == 0 {
		combined.WriteString("[No frontend logs uploaded]\n")
	} else {
		for _, summary := range summaries {
			combined.WriteString(fmt.Sprintf("- %s %s %s uploaded %s\n",
				summary.ClientID,
				strings.TrimSpace(summary.DeviceType),
				strings.TrimSpace(summary.OS),
				summary.UploadedAt.Format(time.RFC3339),
			))
		}
		combined.WriteString("\n")
		if strings.TrimSpace(frontendLogs) == "" {
			combined.WriteString("[No frontend logs available]\n")
		} else {
			combined.WriteString(frontendLogs)
		}
	}

	return combined.String()
}

func (h *LogsHandler) defaultFrontendLogsDir() string {
	if h.logFile != "" {
		return filepath.Join(filepath.Dir(h.logFile), "frontend_logs")
	}
	return filepath.Join(os.TempDir(), "strmr_frontend_logs")
}

func (h *LogsHandler) frontendLogSnapshotPath(clientID string) string {
	return filepath.Join(h.frontendLogsDir, urlSafeFileName(clientID)+".json")
}

func (h *LogsHandler) saveFrontendLogSnapshot(snapshot frontendLogSnapshot) error {
	h.frontendLogsMu.Lock()
	defer h.frontendLogsMu.Unlock()

	if err := os.MkdirAll(h.frontendLogsDir, 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(h.frontendLogSnapshotPath(snapshot.ClientID), data, 0o644)
}

func (h *LogsHandler) readFrontendLogSnapshotByPath(path string) (*frontendLogSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var snapshot frontendLogSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}

	return &snapshot, nil
}

func countLogLines(logs string) int {
	trimmed := strings.TrimSpace(logs)
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "\n") + 1
}

func urlSafeFileName(value string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "..", "_")
	return replacer.Replace(strings.TrimSpace(value))
}

func (h *LogsHandler) readBackendLogEntries(limit int) ([]logEntry, error) {
	if h.logFile == "" {
		return nil, fmt.Errorf("log file not configured")
	}

	file, err := os.Open(h.logFile)
	if err != nil {
		return nil, fmt.Errorf("could not open log file %s: %w", h.logFile, err)
	}
	defer file.Close()

	lines, err := readLastNLines(file, limit)
	if err != nil {
		return nil, err
	}

	entries := make([]logEntry, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		entries = append(entries, logEntry{
			Origin:    "backend",
			Timestamp: parseLogTimestamp(line, time.Time{}),
			Line:      line,
		})
	}

	return entries, nil
}

func (h *LogsHandler) readFrontendLogEntries(limit int, clientID string) ([]logEntry, error) {
	summaries, err := h.ListFrontendLogSummaries()
	if err != nil {
		return nil, err
	}

	entries := make([]logEntry, 0, limit)
	for _, summary := range summaries {
		if clientID != "" && summary.ClientID != clientID {
			continue
		}
		snapshot, err := h.GetFrontendLogSnapshot(summary.ClientID)
		if err != nil {
			continue
		}

		lines := strings.Split(snapshot.FrontendLogs, "\n")
		for _, rawLine := range lines {
			if strings.TrimSpace(rawLine) == "" {
				continue
			}
			entries = append(entries, logEntry{
				Origin:    "frontend",
				ClientID:  snapshot.ClientID,
				Timestamp: parseLogTimestamp(rawLine, snapshot.UploadedAt),
				Line:      decorateFrontendLogLine(snapshot, rawLine),
			})
		}
	}

	sort.SliceStable(entries, func(i, j int) bool {
		aHasTime := !entries[i].Timestamp.IsZero()
		bHasTime := !entries[j].Timestamp.IsZero()
		switch {
		case aHasTime && bHasTime:
			return entries[i].Timestamp.After(entries[j].Timestamp)
		case aHasTime:
			return true
		case bHasTime:
			return false
		default:
			return entries[i].Line > entries[j].Line
		}
	})

	if len(entries) > limit {
		entries = entries[:limit]
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Timestamp.Before(entries[j].Timestamp)
	})

	return entries, nil
}

func (h *LogsHandler) readAggregatedFrontendLogs(limit int, clientID string) (string, []frontendLogSummary, error) {
	summaries, err := h.ListFrontendLogSummaries()
	if err != nil {
		return "", nil, err
	}
	if clientID != "" {
		filtered := make([]frontendLogSummary, 0, len(summaries))
		for _, summary := range summaries {
			if summary.ClientID == clientID {
				filtered = append(filtered, summary)
			}
		}
		summaries = filtered
	}

	entries, err := h.readFrontendLogEntries(limit, clientID)
	if err != nil {
		return "", nil, err
	}

	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		lines = append(lines, entry.Line)
	}

	return strings.Join(lines, "\n"), summaries, nil
}

func decorateFrontendLogLine(snapshot *frontendLogSnapshot, rawLine string) string {
	tag := fmt.Sprintf("frontend:%s", truncateLogIdentifier(snapshot.ClientID))
	if snapshot.DeviceType != "" {
		tag = fmt.Sprintf("%s:%s", tag, strings.ReplaceAll(strings.ToLower(snapshot.DeviceType), " ", "-"))
	}

	trimmed := strings.TrimSpace(rawLine)
	if trimmed == "" {
		return fmt.Sprintf("[%s]", tag)
	}

	if strings.HasPrefix(trimmed, "[") {
		if idx := strings.Index(trimmed, "]"); idx != -1 {
			return trimmed[:idx+1] + " [" + tag + "]" + trimmed[idx+1:]
		}
	}

	return fmt.Sprintf("[%s] %s", tag, trimmed)
}

func parseLogTimestamp(line string, fallback time.Time) time.Time {
	line = strings.TrimSpace(line)
	if line == "" {
		return fallback
	}

	candidates := []string{}
	if strings.HasPrefix(line, "[") {
		if end := strings.Index(line, "]"); end > 1 {
			candidates = append(candidates, line[1:end])
		}
	}
	if len(line) >= 19 {
		candidates = append(candidates, line[:19])
	}

	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006/01/02 15:04:05",
		"2006-01-02 15:04:05",
	}

	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		for _, layout := range layouts {
			if ts, err := time.Parse(layout, candidate); err == nil {
				return ts
			}
		}
	}

	return fallback
}

func truncateLogIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}

func readLastNLines(file *os.File, n int) ([]string, error) {
	// Get file size
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}

	if stat.Size() == 0 {
		return nil, nil
	}

	// Read file in chunks from the end
	const chunkSize = 64 * 1024
	var lines []string
	var leftover []byte

	position := stat.Size()

	for position > 0 && len(lines) < n {
		readSize := int64(chunkSize)
		if position < readSize {
			readSize = position
		}
		position -= readSize

		chunk := make([]byte, readSize)
		_, err := file.ReadAt(chunk, position)
		if err != nil && err != io.EOF {
			return nil, err
		}

		// Prepend any leftover from previous iteration
		chunk = append(chunk, leftover...)

		// Split into lines
		chunkLines := bytes.Split(chunk, []byte("\n"))

		// The first element might be a partial line
		leftover = chunkLines[0]

		// Add complete lines in reverse order
		for i := len(chunkLines) - 1; i > 0; i-- {
			line := string(bytes.TrimRight(chunkLines[i], "\r"))
			if line != "" || i == len(chunkLines)-1 {
				lines = append([]string{line}, lines...)
			}
			if len(lines) >= n {
				break
			}
		}
	}

	// Add any remaining leftover as the first line
	if len(leftover) > 0 && len(lines) < n {
		lines = append([]string{string(leftover)}, lines...)
	}

	// Trim to exactly n lines
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	return lines, nil
}

func (h *LogsHandler) submitToPaste(content string) (string, error) {
	var lastErr error
	client := &http.Client{Timeout: 30 * time.Second}

	for _, service := range pasteServices {
		h.logger.Printf("[logs] Trying paste service: %s", service.name)

		url, err := h.tryPasteService(client, service, content)
		if err != nil {
			h.logger.Printf("[logs] %s failed: %v", service.name, err)
			lastErr = fmt.Errorf("%s: %w", service.name, err)
			continue
		}

		h.logger.Printf("[logs] Successfully uploaded to %s", service.name)
		return url, nil
	}

	return "", fmt.Errorf("all paste services failed, last error: %v", lastErr)
}

func (h *LogsHandler) tryPasteService(client *http.Client, service pasteService, content string) (string, error) {
	req, err := http.NewRequest(http.MethodPost, service.url, strings.NewReader(content))
	if err != nil {
		return "", err
	}

	for key, value := range service.headers {
		req.Header.Set(key, value)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	result := strings.TrimSpace(string(body))

	// If service returns JSON with a key field, parse it
	if service.jsonKeyField != "" {
		var jsonResp map[string]interface{}
		if err := json.Unmarshal(body, &jsonResp); err != nil {
			return "", fmt.Errorf("failed to parse JSON response: %w", err)
		}

		key, ok := jsonResp[service.jsonKeyField].(string)
		if !ok {
			return "", fmt.Errorf("missing or invalid '%s' field in response", service.jsonKeyField)
		}

		return service.urlPrefix + key, nil
	}

	// Otherwise expect a direct URL response
	if !strings.HasPrefix(result, "http") {
		return "", fmt.Errorf("unexpected response: %s", result)
	}

	return result, nil
}

func (h *LogsHandler) respondError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(submitLogsResponse{Error: message})
}

// readLastNLinesScanner is an alternative simpler implementation using Scanner
// Kept for reference but not used due to needing to read entire file
func readLastNLinesScanner(file *os.File, n int) ([]string, error) {
	scanner := bufio.NewScanner(file)
	lines := make([]string, 0, n)

	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return lines, nil
}
