package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConvertSRTToWebVTT(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name: "basic SRT single lines",
			input: "1\n00:00:05,000 --> 00:00:07,500\nHello, world!\n\n2\n00:00:08,000 --> 00:00:10,500\nHow are you?",
			expected: "1\n00:00:05,000 --> 00:00:07,500\nHello, world!\n\n2\n00:00:08,000 --> 00:00:10,500\nHow are you?\n\n",
		},
		{
			name:     "empty SRT",
			input:    "",
			expected: "",
		},
		{
			name: "roll-up CC deduplication",
			input: "1\n00:00:05,000 --> 00:00:07,000\nOLD LINE\nNEW LINE ONE\n\n2\n00:00:07,000 --> 00:00:09,000\nNEW LINE ONE\nNEW LINE TWO\n\n3\n00:00:09,000 --> 00:00:11,000\nNEW LINE TWO\nNEW LINE THREE",
			expected: "1\n00:00:05,000 --> 00:00:07,000\nOLD LINE\nNEW LINE ONE\n\n2\n00:00:07,000 --> 00:00:09,000\nNEW LINE TWO\n\n3\n00:00:09,000 --> 00:00:11,000\nNEW LINE THREE\n\n",
		},
		{
			name: "trims whitespace",
			input: "1\n00:00:05,000 --> 00:00:07,000\n  HELLO WORLD     ",
			expected: "1\n00:00:05,000 --> 00:00:07,000\nHELLO WORLD\n\n",
		},
		{
			name: "preserves italic tags",
			input: "1\n00:00:05,000 --> 00:00:07,000\n<i>Whispered text</i>",
			expected: "1\n00:00:05,000 --> 00:00:07,000\n<i>Whispered text</i>\n\n",
		},
		{
			name: "handles CRLF line endings",
			input: "1\r\n00:00:06,000 --> 00:00:08,000\r\nHello!\r\n\r\n2\r\n00:00:09,000 --> 00:00:11,000\r\nWorld!",
			expected: "1\n00:00:06,000 --> 00:00:08,000\nHello!\n\n2\n00:00:09,000 --> 00:00:11,000\nWorld!\n\n",
		},
		{
			name: "strips lingering h escape markers",
			input: "1\n00:00:05,000 --> 00:00:07,000\nHELLO\\hWORLD",
			expected: "1\n00:00:05,000 --> 00:00:07,000\nHELLO WORLD\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cleanSRT(tt.input)
			if result != tt.expected {
				t.Errorf("cleanSRT()\n  got:  %q\n  want: %q", result, tt.expected)
			}
		})
	}
}

func TestServeLiveCaptions_SessionNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	req := httptest.NewRequest(http.MethodGet, "/video/hls/nonexistent/captions.vtt", nil)
	rr := httptest.NewRecorder()

	manager.ServeLiveCaptions(rr, req, "nonexistent")

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, rr.Code)
	}
}

func TestServeLiveCaptions_NotLiveSession(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	// Create a non-live session
	session := &HLSSession{
		ID:     "test-vod",
		IsLive: false,
	}
	manager.mu.Lock()
	manager.sessions["test-vod"] = session
	manager.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/video/hls/test-vod/captions.vtt", nil)
	rr := httptest.NewRecorder()

	manager.ServeLiveCaptions(rr, req, "test-vod")

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

func TestServeLiveCaptions_NoCCDetected(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	// Create a live session with no CC
	session := &HLSSession{
		ID:                "test-live-nocc",
		IsLive:            true,
		HasClosedCaptions: false,
	}
	manager.mu.Lock()
	manager.sessions["test-live-nocc"] = session
	manager.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/video/hls/test-live-nocc/captions.vtt", nil)
	rr := httptest.NewRecorder()

	manager.ServeLiveCaptions(rr, req, "test-live-nocc")

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	if rr.Header().Get("Content-Type") != "application/x-subrip; charset=utf-8" {
		t.Errorf("expected Content-Type application/x-subrip, got %s", rr.Header().Get("Content-Type"))
	}
	if rr.Body.String() != "" {
		t.Errorf("expected empty body, got %q", rr.Body.String())
	}
}

func TestServeLiveCCStatus_SessionNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	hasCaptions, detectionDone := manager.GetCCStatus("nonexistent")
	if hasCaptions {
		t.Error("expected hasCaptions=false for nonexistent session")
	}
	if detectionDone {
		t.Error("expected detectionDone=false for nonexistent session")
	}
}

func TestServeLiveCCStatus_DetectionPending(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	session := &HLSSession{
		ID:                "test-pending",
		IsLive:            true,
		HasClosedCaptions: false,
		CCDetectionDone:   false,
	}
	manager.mu.Lock()
	manager.sessions["test-pending"] = session
	manager.mu.Unlock()

	hasCaptions, detectionDone := manager.GetCCStatus("test-pending")
	if hasCaptions {
		t.Error("expected hasCaptions=false while detection pending")
	}
	if detectionDone {
		t.Error("expected detectionDone=false while detection pending")
	}
}

func TestServeLiveCCStatus_DetectionComplete(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	session := &HLSSession{
		ID:                "test-complete",
		IsLive:            true,
		HasClosedCaptions: true,
		CCDetectionDone:   true,
	}
	manager.mu.Lock()
	manager.sessions["test-complete"] = session
	manager.mu.Unlock()

	hasCaptions, detectionDone := manager.GetCCStatus("test-complete")
	if !hasCaptions {
		t.Error("expected hasCaptions=true after detection")
	}
	if !detectionDone {
		t.Error("expected detectionDone=true after detection")
	}
}

func TestServeLiveCCStatus_HTTPResponse(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	session := &HLSSession{
		ID:                "test-http",
		IsLive:            true,
		HasClosedCaptions: true,
		CCDetectionDone:   true,
	}
	manager.mu.Lock()
	manager.sessions["test-http"] = session
	manager.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/video/hls/test-http/cc-status", nil)
	rr := httptest.NewRecorder()

	manager.ServeLiveCCStatus(rr, req, "test-http")

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	expected := `{"hasClosedCaptions":true,"detectionDone":true}`
	if rr.Body.String() != expected {
		t.Errorf("expected body %q, got %q", expected, rr.Body.String())
	}
}
