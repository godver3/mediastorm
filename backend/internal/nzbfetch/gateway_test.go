package nzbfetch

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

func TestGatewayRedirectCannotForwardToken(t *testing.T) {
	forwarded := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded = true
	}))
	defer target.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer gateway.Close()
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte(strings.Repeat("x", 48)), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NZB_GATEWAY_TOKEN_FILE", tokenPath)
	_, _, err := Fetch(context.Background(), gateway.URL, "https://indexer.invalid/file", models.NZBResult{Title: "Fixture"})
	if err == nil || forwarded {
		t.Fatalf("gateway redirect escaped the credential boundary: forwarded=%v err=%v", forwarded, err)
	}
}

func TestMissingTokenRejectsBeforeNetworkAccess(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()
	t.Setenv("NZB_GATEWAY_TOKEN_FILE", filepath.Join(t.TempDir(), "missing"))
	_, _, err := Fetch(context.Background(), server.URL, "https://indexer.invalid/file", models.NZBResult{Title: "Fixture"})
	if err == nil || called {
		t.Fatalf("missing credential allowed an acquisition: called=%v err=%v", called, err)
	}
}

func TestReleaseFallbackGroupsEncodingsAndSeparatesEpisodes(t *testing.T) {
	first := models.NZBResult{Title: "Fixture.Show.S01E02.1080p.WEB-DL-GROUP"}
	second := models.NZBResult{Title: "Fixture_Show_S01E02_720p_BluRay-GROUP"}
	third := models.NZBResult{Title: "Fixture.Show.S01E03.1080p.WEB-DL-GROUP"}
	if ContentKey(first) != ContentKey(second) {
		t.Fatal("another encoding bypassed the episode budget")
	}
	if ContentKey(first) == ContentKey(third) {
		t.Fatal("different episodes share a fallback budget")
	}
}
