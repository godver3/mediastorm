package debrid

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"novastream/config"
	"novastream/services/streaming"
)

// ---------------------------------------------------------------------------
// Test doubles: recording proxies and a direct-connection leak detector.
// ---------------------------------------------------------------------------

// recordingHTTPProxy is a forward proxy that records every target. CONNECT
// tunnels (HTTPS upstreams) are refused after recording; absolute-form
// requests (plain-HTTP upstreams) are answered by serve.
type recordingHTTPProxy struct {
	*httptest.Server
	mu      sync.Mutex
	targets []string
	auth    []string
	serve   http.HandlerFunc
}

func newRecordingHTTPProxy(t *testing.T, serve http.HandlerFunc) *recordingHTTPProxy {
	t.Helper()
	p := &recordingHTTPProxy{serve: serve}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		target := r.Host
		if r.Method != http.MethodConnect {
			target = r.URL.Host
		}
		p.targets = append(p.targets, target)
		p.auth = append(p.auth, r.Header.Get("Proxy-Authorization"))
		p.mu.Unlock()
		if r.Method == http.MethodConnect || p.serve == nil {
			http.Error(w, "recording proxy: tunnel refused", http.StatusForbidden)
			return
		}
		p.serve(w, r)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *recordingHTTPProxy) URL() string { return p.Server.URL }

func (p *recordingHTTPProxy) Targets() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.targets...)
}

// recordingSOCKS5Proxy accepts SOCKS5 CONNECTs, records the target and
// refuses it.
type recordingSOCKS5Proxy struct {
	ln      net.Listener
	mu      sync.Mutex
	targets []string
	users   []string
}

func newRecordingSOCKS5Proxy(t *testing.T) *recordingSOCKS5Proxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &recordingSOCKS5Proxy{ln: ln}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go p.handle(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return p
}

func (p *recordingSOCKS5Proxy) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil || head[0] != 5 {
		return
	}
	methods := make([]byte, head[1])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}
	user := ""
	useAuth := false
	for _, m := range methods {
		if m == 2 {
			useAuth = true
		}
	}
	if useAuth {
		_, _ = conn.Write([]byte{5, 2})
		// RFC 1929: ver, ulen, user, plen, pass
		b := make([]byte, 2)
		if _, err := io.ReadFull(conn, b); err != nil {
			return
		}
		u := make([]byte, b[1])
		_, _ = io.ReadFull(conn, u)
		pl := make([]byte, 1)
		_, _ = io.ReadFull(conn, pl)
		pw := make([]byte, pl[0])
		_, _ = io.ReadFull(conn, pw)
		user = string(u) + ":" + string(pw)
		_, _ = conn.Write([]byte{1, 0})
	} else {
		_, _ = conn.Write([]byte{5, 0})
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(conn, req); err != nil {
		return
	}
	var host string
	switch req[3] {
	case 1:
		ip := make([]byte, 4)
		_, _ = io.ReadFull(conn, ip)
		host = net.IP(ip).String()
	case 3:
		l := make([]byte, 1)
		_, _ = io.ReadFull(conn, l)
		name := make([]byte, l[0])
		_, _ = io.ReadFull(conn, name)
		host = string(name)
	case 4:
		ip := make([]byte, 16)
		_, _ = io.ReadFull(conn, ip)
		host = net.IP(ip).String()
	}
	portBytes := make([]byte, 2)
	_, _ = io.ReadFull(conn, portBytes)
	p.mu.Lock()
	p.targets = append(p.targets, net.JoinHostPort(host, fmt.Sprint(binary.BigEndian.Uint16(portBytes))))
	p.users = append(p.users, user)
	p.mu.Unlock()
	// Reply: connection not allowed by ruleset.
	_, _ = conn.Write([]byte{5, 2, 0, 1, 0, 0, 0, 0, 0, 0})
}

func (p *recordingSOCKS5Proxy) Targets() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.targets...)
}

// leakDetector replaces http.DefaultTransport for the test so any client that
// ignores its proxy and falls back to the default (direct) transport is
// recorded and refused instead of reaching the internet.
type leakDetector struct {
	mu   sync.Mutex
	hits []string
}

func (l *leakDetector) RoundTrip(r *http.Request) (*http.Response, error) {
	l.mu.Lock()
	l.hits = append(l.hits, r.URL.Host)
	l.mu.Unlock()
	return nil, errors.New("leak detector: direct connection refused")
}

func (l *leakDetector) Hits() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.hits...)
}

func installLeakDetector(t *testing.T) *leakDetector {
	t.Helper()
	l := &leakDetector{}
	orig := http.DefaultTransport
	http.DefaultTransport = l
	t.Cleanup(func() { http.DefaultTransport = orig })
	return l
}

// realProviders builds each production client by name, bypassing
// DefaultRegistry (other tests in this package register mocks over it).
var realProviders = map[string]struct {
	host string
	make func(apiKey string) Provider
}{
	"realdebrid": {"api.real-debrid.com:443", func(k string) Provider { return NewRealDebridClient(k) }},
	"torbox":     {"api.torbox.app:443", func(k string) Provider { return NewTorboxClient(k) }},
	"alldebrid":  {"api.alldebrid.com:443", func(k string) Provider { return NewAllDebridClient(k) }},
	"debridlink": {"debrid-link.com:443", func(k string) Provider { return NewDebridLinkClient(k) }},
	"premiumize": {"www.premiumize.me:443", func(k string) Provider { return NewPremiumizeClient(k) }},
	"torrin":     {"api.torrin.app:443", func(k string) Provider { return NewTorrinClient(k) }},
}

// withRealProviders points DefaultRegistry at the production constructors
// for the duration of the test.
func withRealProviders(t *testing.T) {
	t.Helper()
	orig := DefaultRegistry
	r := NewRegistry()
	for name, p := range realProviders {
		r.Register(name, p.make)
	}
	DefaultRegistry = r
	t.Cleanup(func() { DefaultRegistry = orig })
}

func resetProxiedTransports(t *testing.T) {
	t.Helper()
	clear := func() {
		proxiedTransportsMu.Lock()
		proxiedAPITransports = map[string]*http.Transport{}
		proxiedStreamTransports = map[string]*http.Transport{}
		proxiedTransportsMu.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

type accountInfoer interface {
	GetAccountInfo(ctx context.Context) (*AccountInfo, error)
}

// exerciseProvider calls every network method on a provider and reports
// which ones produced proxy traffic.
func exerciseProvider(t *testing.T, client Provider, proxyHits func() int) map[string]int {
	t.Helper()
	const hash = "0123456789abcdef0123456789abcdef01234567"
	calls := []struct {
		name string
		fn   func(ctx context.Context) error
	}{
		{"AddMagnet", func(ctx context.Context) error {
			_, err := client.AddMagnet(ctx, "magnet:?xt=urn:btih:"+hash)
			return err
		}},
		{"AddTorrentFile", func(ctx context.Context) error {
			_, err := client.AddTorrentFile(ctx, []byte("d4:infod4:name1:aee"), "a.torrent")
			return err
		}},
		{"GetTorrentInfo", func(ctx context.Context) error { _, err := client.GetTorrentInfo(ctx, "123"); return err }},
		{"SelectFiles", func(ctx context.Context) error { return client.SelectFiles(ctx, "123", "1") }},
		{"DeleteTorrent", func(ctx context.Context) error { return client.DeleteTorrent(ctx, "123") }},
		{"UnrestrictLink", func(ctx context.Context) error {
			_, err := client.UnrestrictLink(ctx, "https://example.invalid/d/"+t.Name())
			return err
		}},
		{"CheckInstantAvailability", func(ctx context.Context) error {
			_, err := client.CheckInstantAvailability(ctx, hash)
			return err
		}},
	}
	if bulk, ok := client.(InstantAvailabilityBulkProvider); ok {
		calls = append(calls, struct {
			name string
			fn   func(ctx context.Context) error
		}{"CheckInstantAvailabilityBulk", func(ctx context.Context) error {
			_, err := bulk.CheckInstantAvailabilityBulk(ctx, []string{hash})
			return err
		}})
	}
	if info, ok := client.(accountInfoer); ok {
		calls = append(calls, struct {
			name string
			fn   func(ctx context.Context) error
		}{"GetAccountInfo", func(ctx context.Context) error { _, err := info.GetAccountInfo(ctx); return err }})
	}
	hits := map[string]int{}
	for _, c := range calls {
		before := proxyHits()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := c.fn(ctx)
		cancel()
		hits[c.name] = proxyHits() - before
		if hits[c.name] > 0 && err == nil {
			t.Errorf("%s: expected an error from the refusing proxy, got nil", c.name)
		}
	}
	return hits
}

// Calls every provider must make over the network (and therefore through the
// proxy). Others may legitimately short-circuit (e.g. Real-Debrid has no
// instant availability endpoint).
var mustHitNetwork = []string{"AddMagnet", "GetTorrentInfo", "UnrestrictLink", "GetAccountInfo"}

// Providers whose torrent links are already direct CDN URLs: UnrestrictLink
// only parses the link locally. The CDN fetch itself is covered by the
// streaming tests.
var localUnrestrict = map[string]bool{"debridlink": true, "premiumize": true, "torrin": true}

func assertNetworkMethodsProxied(t *testing.T, name string, hits map[string]int) {
	t.Helper()
	for _, m := range mustHitNetwork {
		if m == "UnrestrictLink" && localUnrestrict[name] {
			continue
		}
		if hits[m] == 0 {
			t.Errorf("%s.%s made no request through the proxy (hits=%v)", name, m, hits)
		}
	}
}

// ---------------------------------------------------------------------------
// API traffic
// ---------------------------------------------------------------------------

func TestEveryProviderRoutesAllAPICallsThroughHTTPProxy(t *testing.T) {
	withRealProviders(t)
	resetProxiedTransports(t)
	leaks := installLeakDetector(t)

	for name, want := range realProviders {
		t.Run(name, func(t *testing.T) {
			proxy := newRecordingHTTPProxy(t, nil)
			client, err := NewProviderFromSettings(config.DebridProviderSettings{
				Name: name, Provider: name, APIKey: "key-" + t.Name(), Enabled: true,
				ProxyEnabled: true, ProxyURL: proxy.URL(),
			})
			if err != nil {
				t.Fatalf("NewProviderFromSettings: %v", err)
			}
			hits := exerciseProvider(t, client, func() int { return len(proxy.Targets()) })
			assertNetworkMethodsProxied(t, name, hits)
			for _, target := range proxy.Targets() {
				if target != want.host {
					t.Errorf("%s proxied an unexpected target %q (want %q)", name, target, want.host)
				}
			}
		})
	}
	if hits := leaks.Hits(); len(hits) > 0 {
		t.Fatalf("direct (unproxied) requests leaked: %v", hits)
	}
}

func TestEveryProviderRoutesAPICallsThroughSOCKS5ProxyWithAuth(t *testing.T) {
	withRealProviders(t)
	resetProxiedTransports(t)
	leaks := installLeakDetector(t)

	for name, want := range realProviders {
		t.Run(name, func(t *testing.T) {
			proxy := newRecordingSOCKS5Proxy(t)
			client, err := NewProviderFromSettings(config.DebridProviderSettings{
				Name: name, Provider: name, APIKey: "key-" + t.Name(), Enabled: true,
				ProxyEnabled: true, ProxyURL: "socks5h://u1:p1@" + proxy.ln.Addr().String(),
			})
			if err != nil {
				t.Fatalf("NewProviderFromSettings: %v", err)
			}
			hits := exerciseProvider(t, client, func() int { return len(proxy.Targets()) })
			assertNetworkMethodsProxied(t, name, hits)
			proxy.mu.Lock()
			defer proxy.mu.Unlock()
			for i, target := range proxy.targets {
				// socks5h: the hostname, not a locally resolved IP, reaches the proxy.
				if target != want.host {
					t.Errorf("%s proxied target %q, want %q", name, target, want.host)
				}
				if proxy.users[i] != "u1:p1" {
					t.Errorf("%s SOCKS5 credentials = %q, want u1:p1", name, proxy.users[i])
				}
			}
		})
	}
	if hits := leaks.Hits(); len(hits) > 0 {
		t.Fatalf("direct (unproxied) requests leaked: %v", hits)
	}
}

func TestProviderWithoutProxyUsesDirectTransport(t *testing.T) {
	withRealProviders(t)
	leaks := installLeakDetector(t)
	proxy := newRecordingHTTPProxy(t, nil)
	t.Setenv("HTTPS_PROXY", "") // the direct path must not depend on env proxies

	for name := range realProviders {
		client, err := NewProviderFromSettings(config.DebridProviderSettings{
			Name: name, Provider: name, APIKey: "k", Enabled: true,
			ProxyEnabled: false, ProxyURL: proxy.URL(), // URL kept but switched off
		})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		info, ok := client.(accountInfoer)
		if !ok {
			t.Fatalf("%s has no GetAccountInfo", name)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = info.GetAccountInfo(ctx)
		cancel()
	}
	if got := proxy.Targets(); len(got) != 0 {
		t.Fatalf("proxy switched off but still used: %v", got)
	}
	if len(leaks.Hits()) != len(realProviders) {
		t.Fatalf("expected one direct request per provider, got %v", leaks.Hits())
	}
}

func TestNewProviderFromSettingsFailsClosed(t *testing.T) {
	withRealProviders(t)
	resetProxiedTransports(t)
	cases := []struct {
		name string
		pc   config.DebridProviderSettings
		want string
	}{
		{"enabled without URL", config.DebridProviderSettings{Name: "RD", Provider: "realdebrid", ProxyEnabled: true}, "no proxy URL"},
		{"whitespace URL", config.DebridProviderSettings{Name: "RD", Provider: "realdebrid", ProxyEnabled: true, ProxyURL: "   "}, "no proxy URL"},
		{"unsupported scheme", config.DebridProviderSettings{Name: "RD", Provider: "realdebrid", ProxyEnabled: true, ProxyURL: "ftp://proxy:21"}, "unsupported proxy scheme"},
		{"missing host", config.DebridProviderSettings{Name: "RD", Provider: "realdebrid", ProxyEnabled: true, ProxyURL: "http://"}, "missing host"},
		{"unparseable", config.DebridProviderSettings{Name: "RD", Provider: "realdebrid", ProxyEnabled: true, ProxyURL: "http://[::1"}, "invalid proxy URL"},
		{"unknown provider", config.DebridProviderSettings{Name: "X", Provider: "nope"}, "not registered"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewProviderFromSettings(tc.pc)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
			if client != nil {
				t.Fatalf("got a client despite error")
			}
		})
	}
}

// A provider that cannot take a proxied transport must be refused, not used
// directly.
func TestNewProviderFromSettingsRejectsProviderWithoutTransportHook(t *testing.T) {
	orig := DefaultRegistry
	r := NewRegistry()
	r.Register("fixedclient", func(string) Provider { return &mockProvider{name: "fixedclient"} })
	DefaultRegistry = r
	t.Cleanup(func() { DefaultRegistry = orig })

	_, err := NewProviderFromSettings(config.DebridProviderSettings{
		Name: "F", Provider: "fixedclient", ProxyEnabled: true, ProxyURL: "http://127.0.0.1:1",
	})
	if err == nil || !strings.Contains(err.Error(), "does not support proxy routing") {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewProviderFromSettings(config.DebridProviderSettings{Name: "F", Provider: "fixedclient"}); err != nil {
		t.Fatalf("unproxied mock should still build: %v", err)
	}
}

func TestNewProviderFromSettingsAppliesProviderConfig(t *testing.T) {
	withRealProviders(t)
	client, err := NewProviderFromSettings(config.DebridProviderSettings{
		Provider: "TorBox", APIKey: "k", Config: map[string]string{"autoClearQueue": "true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !client.(*TorboxClient).autoClearQueue {
		t.Fatal("provider Config not applied")
	}
}

func TestNewProviderHTTPClient(t *testing.T) {
	resetProxiedTransports(t)
	leaks := installLeakDetector(t)
	proxy := newRecordingHTTPProxy(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })

	c, err := NewProviderHTTPClient(config.DebridProviderSettings{Name: "RD", ProxyEnabled: true, ProxyURL: proxy.URL()}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Get("http://api.real-debrid.test/rest/1.0/user")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := proxy.Targets(); len(got) != 1 || got[0] != "api.real-debrid.test" {
		t.Fatalf("proxy targets = %v", got)
	}
	if _, err := NewProviderHTTPClient(config.DebridProviderSettings{Name: "RD", ProxyEnabled: true}, time.Second); err == nil {
		t.Fatal("expected fail-closed error for enabled proxy without URL")
	}
	if len(leaks.Hits()) != 0 {
		t.Fatalf("leaked: %v", leaks.Hits())
	}
}

func TestProxiedTransportsArePooledPerProxyURL(t *testing.T) {
	resetProxiedTransports(t)
	a1, _ := proxiedAPITransport("http://127.0.0.1:1")
	a2, _ := proxiedAPITransport("http://127.0.0.1:1")
	b, _ := proxiedAPITransport("http://127.0.0.1:2")
	if a1 != a2 || a1 == b {
		t.Fatal("API transports must be shared per proxy URL and distinct across URLs")
	}
	s1, _ := proxiedStreamTransport("http://127.0.0.1:1")
	if s1 == a1 {
		t.Fatal("stream and API transports must be separate pools")
	}
	if s1.Proxy == nil || s1.ResponseHeaderTimeout != 15*time.Second || s1.MaxIdleConnsPerHost != 8 {
		t.Fatalf("stream transport lost proxy or tuning: %+v", s1)
	}
}

// ---------------------------------------------------------------------------
// Download traffic (StreamingProvider / ProxyService)
// ---------------------------------------------------------------------------

// proxyFakeProvider is a minimal provider whose unrestricted links point at a
// plain-HTTP CDN host, so a recording HTTP proxy can serve the download.
type proxyFakeProvider struct {
	mockProvider
	cdnURL    string
	transport http.RoundTripper
	mu        sync.Mutex
	unlocks   int
}

func (p *proxyFakeProvider) useHTTPTransport(rt http.RoundTripper) { p.transport = rt }
func (p *proxyFakeProvider) GetTorrentInfo(context.Context, string) (*TorrentInfo, error) {
	return &TorrentInfo{ID: "t1", Status: "downloaded", Links: []string{"https://fake.debrid/l/1"},
		Files: []File{{ID: 1, Path: "/Movie.mkv", Bytes: 4, Selected: 1}}}, nil
}
func (p *proxyFakeProvider) UnrestrictLink(context.Context, string) (*UnrestrictResult, error) {
	p.mu.Lock()
	p.unlocks++
	p.mu.Unlock()
	return &UnrestrictResult{DownloadURL: p.cdnURL, Filename: "Movie.mkv"}, nil
}

func newProxyTestStreaming(t *testing.T, pc config.DebridProviderSettings, fake *proxyFakeProvider) (*StreamingProvider, *leakDetector) {
	t.Helper()
	orig := DefaultRegistry
	r := NewRegistry()
	r.Register("fakeproxied", func(string) Provider { return fake })
	DefaultRegistry = r
	t.Cleanup(func() { DefaultRegistry = orig })
	resetProxiedTransports(t)

	cfg := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	settings := config.DefaultSettings()
	settings.Streaming.DebridProviders = []config.DebridProviderSettings{pc}
	if err := cfg.Save(settings); err != nil {
		t.Fatal(err)
	}
	sp := NewStreamingProvider(cfg)
	leaks := &leakDetector{}
	sp.httpClient = &http.Client{Transport: leaks} // the direct download client
	return sp, leaks
}

func TestStreamDownloadsThroughProviderProxy(t *testing.T) {
	proxy := newRecordingHTTPProxy(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=0-3" {
			t.Errorf("Range not forwarded: %q", r.Header.Get("Range"))
		}
		w.Header().Set("Content-Range", "bytes 0-3/4")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "DATA")
	})
	fake := &proxyFakeProvider{mockProvider: mockProvider{name: "fakeproxied"}, cdnURL: "http://cdn.fake.test/dl/Movie.mkv"}
	sp, leaks := newProxyTestStreaming(t, config.DebridProviderSettings{
		Name: "Fake", Provider: "fakeproxied", APIKey: "k", Enabled: true, ProxyEnabled: true, ProxyURL: proxy.URL(),
	}, fake)

	for i := 0; i < 2; i++ { // second request reuses the cached link
		resp, err := sp.Stream(context.Background(), streaming.Request{Path: "/debrid/fakeproxied/t1/file/1", Method: http.MethodGet, RangeHeader: "bytes=0-3"})
		if err != nil {
			t.Fatalf("Stream #%d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != "DATA" {
			t.Fatalf("body = %q", body)
		}
	}
	if got := proxy.Targets(); len(got) != 2 || got[0] != "cdn.fake.test" {
		t.Fatalf("proxy targets = %v", got)
	}
	if fake.transport == nil {
		t.Fatal("provider API client was not given the proxy transport")
	}
	if len(leaks.Hits()) != 0 {
		t.Fatalf("download leaked via direct client: %v", leaks.Hits())
	}
}

func TestStreamFailsClosedOnMisconfiguredProxy(t *testing.T) {
	fake := &proxyFakeProvider{mockProvider: mockProvider{name: "fakeproxied"}, cdnURL: "http://cdn.fake.test/dl"}
	sp, leaks := newProxyTestStreaming(t, config.DebridProviderSettings{
		Name: "Fake", Provider: "fakeproxied", APIKey: "k", Enabled: true, ProxyEnabled: true,
	}, fake)
	// Pre-seed a cached link so only the download client decides routing.
	sp.setCachedURL(cacheKeyFor("t1", "1"), "http://cdn.fake.test/dl", "Movie.mkv", 0, 0)

	if _, err := sp.Stream(context.Background(), streaming.Request{Path: "/debrid/fakeproxied/t1/file/1", Method: http.MethodGet}); err == nil {
		t.Fatal("expected error for enabled proxy without URL")
	}
	if _, err := sp.streamWithProvider(context.Background(), streaming.Request{Path: "/debrid/fakeproxied/t1/file/1", Method: http.MethodGet}, fake, "t1", "1", false); err == nil {
		t.Fatal("expected streamWithProvider to fail closed")
	}
	if _, err := sp.GetDirectURL(context.Background(), "/debrid/fakeproxied/t1/file/1"); err == nil {
		t.Fatal("expected GetDirectURL to fail closed")
	}
	if len(leaks.Hits()) != 0 {
		t.Fatalf("leaked: %v", leaks.Hits())
	}
}

func TestGetDirectURLWithheldForProxiedProvider(t *testing.T) {
	fake := &proxyFakeProvider{mockProvider: mockProvider{name: "fakeproxied"}, cdnURL: "http://cdn.fake.test/dl"}
	sp, _ := newProxyTestStreaming(t, config.DebridProviderSettings{
		Name: "Fake", Provider: "fakeproxied", APIKey: "k", Enabled: true, ProxyEnabled: true, ProxyURL: "http://127.0.0.1:1",
	}, fake)

	path := "/debrid/fakeproxied/t1/file/1"
	for i := 0; i < 2; i++ { // fresh resolve, then cached
		url, err := sp.GetDirectURL(context.Background(), path)
		if !errors.Is(err, ErrDirectURLProxied) || url != "" {
			t.Fatalf("call %d: url=%q err=%v, want ErrDirectURLProxied", i, url, err)
		}
	}
	if fake.unlocks != 1 {
		t.Fatalf("link should be resolved once and cached for Stream, unlocks=%d", fake.unlocks)
	}
	if _, _, _, _, found := sp.getCachedURL(cacheKeyFor("t1", "1")); !found {
		t.Fatal("resolved link not cached for the proxied Stream path")
	}
	// CompositeProvider must not surface it either.
	if url, err := NewCompositeProvider(sp).GetDirectURL(context.Background(), path); err == nil || url != "" {
		t.Fatalf("composite exposed url=%q err=%v", url, err)
	}
}

func TestGetDirectURLStillReturnedWithoutProxy(t *testing.T) {
	fake := &proxyFakeProvider{mockProvider: mockProvider{name: "fakeproxied"}, cdnURL: "http://cdn.fake.test/dl"}
	sp, _ := newProxyTestStreaming(t, config.DebridProviderSettings{
		Name: "Fake", Provider: "fakeproxied", APIKey: "k", Enabled: true,
	}, fake)
	url, err := sp.GetDirectURL(context.Background(), "/debrid/fakeproxied/t1/file/1")
	if err != nil || url != "http://cdn.fake.test/dl" {
		t.Fatalf("url=%q err=%v", url, err)
	}
}

func TestDebridProxyServiceUsesProviderProxy(t *testing.T) {
	proxy := newRecordingHTTPProxy(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
	resetProxiedTransports(t)
	cfg := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	settings := config.DefaultSettings()
	settings.Streaming.DebridProviders = []config.DebridProviderSettings{{
		Name: "RD", Provider: "realdebrid", APIKey: "k", Enabled: true, ProxyEnabled: true, ProxyURL: proxy.URL(),
	}}
	if err := cfg.Save(settings); err != nil {
		t.Fatal(err)
	}
	svc := NewProxyService(cfg)
	leaks := &leakDetector{}
	svc.httpClient = &http.Client{Transport: leaks}

	resp, err := svc.Proxy(context.Background(), ProxyRequest{Provider: "realdebrid", ResourceURL: "http://download.real-debrid.com/d/x", Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := proxy.Targets(); len(got) != 1 || got[0] != "download.real-debrid.com" {
		t.Fatalf("proxy targets = %v", got)
	}
	if len(leaks.Hits()) != 0 {
		t.Fatalf("leaked: %v", leaks.Hits())
	}

	// Misconfigured → error, still no direct request.
	settings.Streaming.DebridProviders[0].ProxyURL = ""
	if err := cfg.Save(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Proxy(context.Background(), ProxyRequest{Provider: "realdebrid", ResourceURL: "http://download.real-debrid.com/d/x"}); err == nil {
		t.Fatal("expected fail-closed error")
	}
	if len(leaks.Hits()) != 0 {
		t.Fatalf("leaked: %v", leaks.Hits())
	}
}

// ---------------------------------------------------------------------------
// Settings lookups
// ---------------------------------------------------------------------------

func TestProxyURLForProvider(t *testing.T) {
	s := &config.Settings{}
	s.Streaming.DebridProviders = []config.DebridProviderSettings{
		{Provider: "realdebrid", Enabled: false, ProxyEnabled: true, ProxyURL: "http://disabled:1"},
		{Provider: "RealDebrid", Enabled: true, ProxyEnabled: true, ProxyURL: " http://p:1 "},
		{Provider: "torbox", Enabled: true, ProxyEnabled: false, ProxyURL: "http://off:1"},
		{Provider: "alldebrid", Enabled: true, ProxyEnabled: true},
	}
	if got, err := proxyURLForProvider(s, "realdebrid"); err != nil || got != "http://p:1" {
		t.Fatalf("realdebrid = %q, %v", got, err)
	}
	if got, err := proxyURLForProvider(s, "torbox"); err != nil || got != "" {
		t.Fatalf("torbox = %q, %v", got, err)
	}
	if _, err := proxyURLForProvider(s, "alldebrid"); err == nil {
		t.Fatal("alldebrid: expected fail-closed error")
	}
	if got, err := proxyURLForProvider(s, "premiumize"); err != nil || got != "" {
		t.Fatalf("unconfigured = %q, %v", got, err)
	}
}

func TestHealthServiceSkipsTrackProbeForProxiedProvider(t *testing.T) {
	cfg := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	settings := config.DefaultSettings()
	settings.Streaming.DebridProviders = []config.DebridProviderSettings{
		{Name: "RD", Provider: "realdebrid", APIKey: "k", Enabled: true, ProxyEnabled: true, ProxyURL: "http://p:1"},
		{Name: "TB", Provider: "torbox", APIKey: "k", Enabled: true},
		{Name: "AD", Provider: "alldebrid", APIKey: "k", Enabled: true, ProxyEnabled: true},
	}
	if err := cfg.Save(settings); err != nil {
		t.Fatal(err)
	}
	s := &HealthService{cfg: cfg}
	if !s.providerRoutesThroughProxy("realdebrid") {
		t.Error("realdebrid should be proxied")
	}
	if s.providerRoutesThroughProxy("torbox") {
		t.Error("torbox should be direct")
	}
	if !s.providerRoutesThroughProxy("alldebrid") {
		t.Error("misconfigured proxy must count as proxied (fail closed)")
	}
}
