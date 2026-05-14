package debrid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTorboxGetAccountInfoReportsCloudflareBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/me" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("error code: 1010"))
	}))
	defer server.Close()

	client := NewTorboxClient("test-key")
	client.baseURL = server.URL

	_, err := client.GetAccountInfo(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Cloudflare") || !strings.Contains(err.Error(), "1010") {
		t.Fatalf("expected Cloudflare 1010 error, got %v", err)
	}
}

func TestTorboxGetAccountInfoSendsUserAgent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "mediastorm/1.0" {
			t.Fatalf("User-Agent = %q, want mediastorm/1.0", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("Accept = %q, want application/json", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"email":"user@example.com","plan":1,"premium_expires_at":"2051-02-18T04:08:43Z","is_subscribed":true}}`))
	}))
	defer server.Close()

	client := NewTorboxClient("test-key")
	client.baseURL = server.URL

	info, err := client.GetAccountInfo(context.Background())
	if err != nil {
		t.Fatalf("GetAccountInfo returned error: %v", err)
	}
	if !info.PremiumActive {
		t.Fatal("expected premium account")
	}
}
