package usenet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novastream/models"
)

func TestNZBHealthFetchUsesPolicyGateway(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/fetch" || r.Method != "POST" {
			t.Error("health check bypassed policy route")
		}
		w.Header().Set("X-NZB-Filename", "health.nzb")
		_, _ = w.Write([]byte("<nzb/>"))
	}))
	defer server.Close()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte(strings.Repeat("x", 48)), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NZB_GATEWAY_URL", server.URL)
	t.Setenv("NZB_GATEWAY_TOKEN_FILE", token)
	data, name, err := (&Service{}).fetchNZB(context.Background(), "https://indexer.invalid/file", models.NZBResult{Title: "Fixture", GUID: "release"})
	if err != nil || calls != 1 || string(data) != "<nzb/>" || name != "health.nzb" {
		t.Fatalf("health acquisition failed: %v", err)
	}
}
