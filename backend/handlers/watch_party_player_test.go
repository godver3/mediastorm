package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"novastream/models"
)

func TestWebPlayerWatchPartyTermination(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is not installed")
	}
	handler := NewWebPlaybackHandler(fakeWebPlaybackUsers{users: []models.User{{ID: "profile-a", Name: "Main"}}}, fakeWebPlaybackSessions{token: "test-token", session: models.Session{AccountID: "account-a", IsMaster: true}}, "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/watch?token=test-token", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("render player: HTTP %d", rec.Code)
	}
	path := filepath.Join(t.TempDir(), "player.html")
	if err := os.WriteFile(path, rec.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "--test", "testdata/watch_party_player.cjs")
	cmd.Env = append(os.Environ(), "MEDIASTORM_PLAYER_HTML="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("watch party playback: %v\n%s", err, output)
	}
}
