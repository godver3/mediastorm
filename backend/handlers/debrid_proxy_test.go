package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"novastream/config"
)

type debridTestProxy struct {
	*httptest.Server
	mu      sync.Mutex
	targets []string
}

func newDebridTestProxy(t *testing.T) *debridTestProxy {
	t.Helper()
	p := &debridTestProxy{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.targets = append(p.targets, r.Host)
		p.mu.Unlock()
		http.Error(w, "refused", http.StatusForbidden)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *debridTestProxy) Targets() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.targets...)
}

type directLeakTransport struct {
	mu   sync.Mutex
	hits []string
}

func (d *directLeakTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	d.mu.Lock()
	d.hits = append(d.hits, r.URL.Host)
	d.mu.Unlock()
	return nil, errors.New("direct connection refused by test")
}

func installDirectLeakTransport(t *testing.T) *directLeakTransport {
	t.Helper()
	d := &directLeakTransport{}
	orig := http.DefaultTransport
	http.DefaultTransport = d
	t.Cleanup(func() { http.DefaultTransport = orig })
	return d
}

func runTestDebridProvider(t *testing.T, req TestDebridProviderRequest) map[string]any {
	t.Helper()
	body, _ := json.Marshal(req)
	rec := httptest.NewRecorder()
	(&AdminUIHandler{}).TestDebridProvider(rec, httptest.NewRequest(http.MethodPost, "/admin/api/test/debrid-provider", bytes.NewReader(body)))
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestTestDebridProviderRoutesThroughProviderProxy(t *testing.T) {
	leaks := installDirectLeakTransport(t)
	hosts := map[string]string{
		"realdebrid": "api.real-debrid.com:443",
		"torbox":     "api.torbox.app:443",
		"alldebrid":  "api.alldebrid.com:443",
		"debridlink": "debrid-link.com:443",
		"premiumize": "www.premiumize.me:443",
		"torrin":     "api.torrin.app:443",
	}
	for provider, host := range hosts {
		t.Run(provider, func(t *testing.T) {
			proxy := newDebridTestProxy(t)
			out := runTestDebridProvider(t, TestDebridProviderRequest{
				Name: provider, Provider: provider, APIKey: "k", ProxyEnabled: true, ProxyURL: proxy.URL,
			})
			if out["success"] == true {
				t.Fatalf("expected failure through refusing proxy, got %v", out)
			}
			got := proxy.Targets()
			if len(got) == 0 {
				t.Fatalf("connection test never reached the proxy: %v", out)
			}
			for _, target := range got {
				if target != host {
					t.Fatalf("proxied target %q, want %q", target, host)
				}
			}
		})
	}
	if hits := leaks.hits; len(hits) != 0 {
		t.Fatalf("connection test leaked direct requests: %v", hits)
	}
}

func TestTestDebridProviderFailsClosedOnMissingProxyURL(t *testing.T) {
	leaks := installDirectLeakTransport(t)
	for _, provider := range []string{"realdebrid", "debridlink"} {
		out := runTestDebridProvider(t, TestDebridProviderRequest{Name: "P", Provider: provider, APIKey: "k", ProxyEnabled: true})
		if out["success"] == true || !strings.Contains(out["error"].(string), "no proxy URL") {
			t.Fatalf("%s: got %v", provider, out)
		}
	}
	if len(leaks.hits) != 0 {
		t.Fatalf("leaked: %v", leaks.hits)
	}
}

func TestDebridProxyURLIsRedactedAndRestored(t *testing.T) {
	s := config.Settings{}
	s.Streaming.DebridProviders = []config.DebridProviderSettings{{APIKey: "k", ProxyEnabled: true, ProxyURL: "socks5h://u:secret@h:1080"}}
	redactSettings(&s)
	if got := s.Streaming.DebridProviders[0].ProxyURL; got != redactedPlaceholder {
		t.Fatalf("proxy URL not redacted: %q", got)
	}
	if !s.Streaming.DebridProviders[0].ProxyEnabled {
		t.Fatal("proxyEnabled must stay visible")
	}

	existing := config.Settings{}
	existing.Streaming.DebridProviders = []config.DebridProviderSettings{{APIKey: "k", ProxyURL: "socks5h://u:secret@h:1080"}}
	incoming := config.Settings{}
	incoming.Streaming.DebridProviders = []config.DebridProviderSettings{{APIKey: redactedPlaceholder, ProxyURL: redactedPlaceholder}}
	preserveRedactedFields(&incoming, &existing)
	if got := incoming.Streaming.DebridProviders[0].ProxyURL; got != "socks5h://u:secret@h:1080" {
		t.Fatalf("proxy URL not restored: %q", got)
	}
}

func TestSettingsSchemaExposesDebridProxyFields(t *testing.T) {
	fields := SettingsSchema["debridProviders"].(map[string]interface{})["fields"].(map[string]interface{})
	enabled, ok := fields["proxyEnabled"].(map[string]interface{})
	if !ok || enabled["type"] != "boolean" {
		t.Fatalf("proxyEnabled field = %v", fields["proxyEnabled"])
	}
	url, ok := fields["proxyUrl"].(map[string]interface{})
	if !ok || url["type"] != "password" {
		t.Fatalf("proxyUrl field = %v", fields["proxyUrl"])
	}
}

func TestSettingsTemplateSendsProxyFieldsToConnectionTest(t *testing.T) {
	tpl, err := adminTemplates.ReadFile("admin_templates/settings.html")
	if err != nil {
		t.Skipf("template FS not available: %v", err)
	}
	if !bytes.Contains(tpl, []byte("proxyEnabled: item.proxyEnabled === true, proxyUrl: item.proxyUrl || ''")) {
		t.Fatal("debrid connection test payload must include proxyEnabled/proxyUrl")
	}
}
