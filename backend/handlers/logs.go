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
	"runtime"
	"strings"
	"time"
)

const (
	maxLogLines   = 5000
	maxUploadSize = 10 << 20 // 10 MB limit for frontend logs
)

// pasteService defines a paste service configuration
type pasteService struct {
	name       string
	url        string
	urlPrefix  string            // if response is a key, prepend this to form the full URL
	headers    map[string]string // additional headers to send
	jsonKeyField string          // if non-empty, parse JSON response and extract this field as the key
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
	logger  *log.Logger
	logFile string // path to the backend log file
}

type submitLogsRequest struct {
	FrontendLogs string `json:"frontendLogs"`
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

	// Build the combined log content
	var combined strings.Builder

	// Header with metadata
	combined.WriteString("═══════════════════════════════════════════════════════════════════════\n")
	combined.WriteString("                         STRMR LOG SUBMISSION\n")
	combined.WriteString("═══════════════════════════════════════════════════════════════════════\n")
	combined.WriteString(fmt.Sprintf("Submitted: %s\n", time.Now().UTC().Format(time.RFC3339)))
	combined.WriteString(fmt.Sprintf("Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH))
	combined.WriteString("\n")

	// Backend logs
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

	// Frontend logs
	combined.WriteString("═══════════════════════════════════════════════════════════════════════\n")
	combined.WriteString("                          FRONTEND LOGS\n")
	combined.WriteString(fmt.Sprintf("                     (last %d entries)\n", maxLogLines))
	combined.WriteString("═══════════════════════════════════════════════════════════════════════\n\n")

	if strings.TrimSpace(payload.FrontendLogs) == "" {
		combined.WriteString("[No frontend logs provided]\n")
	} else {
		combined.WriteString(payload.FrontendLogs)
	}

	// Submit to paste.c-net.org
	pasteContent := combined.String()
	pasteURL, err := h.submitToPaste(pasteContent)
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

func (h *LogsHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
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
