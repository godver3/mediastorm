package trakt

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// captureOAuthBodies records the JSON body of each /oauth request and answers
// with a token so PollForToken and RefreshAccessToken both succeed.
func captureOAuthBodies(t *testing.T) (*[]map[string]string, func()) {
	t.Helper()
	var bodies []map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		bodies = append(bodies, body)
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","expires_in":3600}`))
	}))
	originalURL := traktAPIBaseURL
	setBaseURL(server.URL)
	return &bodies, func() {
		setBaseURL(originalURL)
		server.Close()
	}
}

func TestOAuthOmitsClientSecretWhenNotIssued(t *testing.T) {
	bodies, done := captureOAuthBodies(t)
	defer done()

	c := NewClient("client-only", "")
	if !c.HasCredentials() {
		t.Fatal("a Client ID alone should count as configured credentials")
	}
	if _, err := c.PollForToken("device-code"); err != nil {
		t.Fatalf("PollForToken: %v", err)
	}
	if _, err := c.RefreshAccessToken("refresh"); err != nil {
		t.Fatalf("RefreshAccessToken: %v", err)
	}

	if len(*bodies) != 2 {
		t.Fatalf("expected 2 oauth requests, got %d", len(*bodies))
	}
	for i, body := range *bodies {
		if _, ok := body["client_secret"]; ok {
			t.Errorf("request %d sent client_secret for a secretless app: %v", i, body)
		}
		if body["client_id"] != "client-only" {
			t.Errorf("request %d client_id = %q", i, body["client_id"])
		}
	}
	if got := (*bodies)[1]["redirect_uri"]; got != OOBRedirectURI {
		t.Errorf("refresh redirect_uri = %q, want %q", got, OOBRedirectURI)
	}
}

func TestOAuthSendsClientSecretForLegacyApps(t *testing.T) {
	bodies, done := captureOAuthBodies(t)
	defer done()

	c := NewClient("legacy", "  shh  ")
	if _, err := c.PollForToken("device-code"); err != nil {
		t.Fatalf("PollForToken: %v", err)
	}
	if _, err := c.RefreshAccessToken("refresh"); err != nil {
		t.Fatalf("RefreshAccessToken: %v", err)
	}
	for i, body := range *bodies {
		if body["client_secret"] != "shh" {
			t.Errorf("request %d client_secret = %q, want trimmed legacy secret", i, body["client_secret"])
		}
	}
}
