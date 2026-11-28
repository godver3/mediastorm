package netproxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewTransportRejectsBadProxyURLs(t *testing.T) {
	for _, raw := range []string{"ftp://h:21", "http://", "socks5://", "http://[::1", "h:8080"} {
		if _, err := NewTransport(HTTPClientOptions{}, raw); err == nil {
			t.Errorf("%q: expected error", raw)
		}
	}
}

func TestNewTransportProxyRouting(t *testing.T) {
	direct, err := NewTransport(HTTPClientOptions{ResponseHeaderTimeout: 7 * time.Second}, "")
	if err != nil {
		t.Fatal(err)
	}
	if direct.ResponseHeaderTimeout != 7*time.Second {
		t.Fatal("options not applied")
	}

	var seen string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.String() + "|" + r.Header.Get("Proxy-Authorization")
		_, _ = w.Write([]byte("ok"))
	}))
	defer proxy.Close()
	withAuth := strings.Replace(proxy.URL, "http://", "http://user:pw@", 1)
	tr, err := NewTransport(HTTPClientOptions{}, withAuth)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Transport: tr}).Get("http://upstream.test/x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !strings.HasPrefix(seen, "http://upstream.test/x|Basic ") {
		t.Fatalf("proxy saw %q", seen)
	}
}

func TestNewTransportIgnoresReplacedDefaultTransport(t *testing.T) {
	orig := http.DefaultTransport
	http.DefaultTransport = http.RoundTripper(nil)
	defer func() { http.DefaultTransport = orig }()
	if _, err := NewTransport(HTTPClientOptions{}, "http://127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
}
