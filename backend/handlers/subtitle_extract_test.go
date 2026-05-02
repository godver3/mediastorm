package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServeSubtitles_StripsLingeringHEscapesForASS(t *testing.T) {
	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, "subtitles.ass")
	content := "[Script Info]\n[V4+ Styles]\n[Events]\nDialogue: 0,0:00:01.00,0:00:03.00,Default,,0,0,0,,HELLO\\hWORLD\n"
	if err := os.WriteFile(outputPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write subtitles: %v", err)
	}

	manager := NewSubtitleExtractManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	session := &SubtitleExtractSession{
		ID:             "12345678-test-session",
		OutputFormat:   "ass",
		OutputPath:     outputPath,
		CreatedAt:      time.Now(),
		LastAccess:     time.Now(),
		extractionDone: true,
	}

	req := httptest.NewRequest(http.MethodGet, "/video/subtitles/test/subtitles.ass", nil)
	rr := httptest.NewRecorder()

	manager.ServeSubtitles(rr, req, session)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	expected := "[Script Info]\n[V4+ Styles]\n[Events]\nDialogue: 0,0:00:01.00,0:00:03.00,Default,,0,0,0,,HELLO WORLD\n"
	if rr.Body.String() != expected {
		t.Fatalf("unexpected body: got %q want %q", rr.Body.String(), expected)
	}
}
