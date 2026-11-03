package jellyfinbridge

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/config"
)

func TestResolveWaitOnlyCapsAndroidTV(t *testing.T) {
	cases := map[string]time.Duration{
		"Android TV":   8 * time.Second,
		"Jellyfin Web": 45 * time.Second,
		"Swiftfin iOS": 45 * time.Second,
		"":             45 * time.Second, // media requests carry no client header
	}
	for client, want := range cases {
		if got := resolveWaitFor(&principal{Client: clientInfo{Client: client}}, 45*time.Second); got != want {
			t.Errorf("%q: wait %v, want %v", client, got, want)
		}
	}
	if got := resolveWaitFor(nil, 45*time.Second); got != 45*time.Second {
		t.Errorf("nil owner: %v", got)
	}
}

func TestClientIPTrustedProxies(t *testing.T) {
	s := New(Deps{}, config.JellyfinBridgeSettings{TrustedProxies: []string{"172.18.0.0/16", "10.0.0.5"}})
	defer s.Shutdown(t.Context())
	req := func(remote, xff string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	cases := []struct {
		remote, xff, want string
	}{
		{"172.18.0.3:5000", "6.6.6.6, 203.0.113.9", "203.0.113.9"}, // trusted proxy: rightmost hop
		{"10.0.0.5:5000", "203.0.113.10", "203.0.113.10"},          // bare IP entry
		{"127.0.0.1:5000", "203.0.113.11", "203.0.113.11"},         // loopback always trusted
		{"198.51.100.4:5000", "203.0.113.12", "198.51.100.4"},      // untrusted peer: header ignored
		{"172.19.0.3:5000", "203.0.113.13", "172.19.0.3"},          // outside the CIDR
	}
	for _, tc := range cases {
		if got := s.clientIP(req(tc.remote, tc.xff)); got != tc.want {
			t.Errorf("remote %s xff %q: got %s, want %s", tc.remote, tc.xff, got, tc.want)
		}
	}
}

func TestSuccessfulLoginsDoNotConsumeNetworkBudget(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	// A household behind one address signs in many times.
	for i := 0; i < loginMaxFailures+5; i++ {
		h.login("Living Room", "alice-pass")
	}
	// Failures still count against the network.
	for i := 0; i < loginMaxFailures; i++ {
		h.do(http.MethodPost, "/Users/AuthenticateByName", "", map[string]string{"Username": "Kids", "Pw": "wrong"})
	}
	if rec := h.do(http.MethodPost, "/Users/AuthenticateByName", "", map[string]string{"Username": "Living Room", "Pw": "alice-pass"}); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("network budget not enforced after failures: %d", rec.Code)
	}
}

func TestImageEndpointNeverRedirectsAndGatesLibraryArtwork(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	artworkCalls := 0
	h.srv.deps.Handlers.LibraryArtwork = func(w http.ResponseWriter, r *http.Request) {
		artworkCalls++
		w.WriteHeader(http.StatusOK)
	}
	h.srv.deps.Handlers.ImageProxy = func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid URL", http.StatusBadRequest)
	}
	// A movie ref whose poster points at mediastorm's artwork route (e.g. via a
	// crafted watchlist record) must not reach the artwork handler.
	movie := h.srv.ids.put(refMovie, "tmdb:movie:1", refContext{Poster: "/api/library/items/x/artwork/poster"})
	rec := h.do(http.MethodGet, "/Items/"+movie+"/Images/Primary", "", nil)
	if artworkCalls != 0 || rec.Code == http.StatusFound {
		t.Fatalf("non-local ref reached library artwork (calls=%d, code=%d)", artworkCalls, rec.Code)
	}
	local := h.srv.ids.putTrusted(refLocal, "file-1", refContext{Poster: "/api/library/items/file-1/artwork/poster"})
	if rec := h.do(http.MethodGet, "/Items/"+local+"/Images/Primary", "", nil); rec.Code != http.StatusOK || artworkCalls != 1 {
		t.Fatalf("local artwork: code=%d calls=%d", rec.Code, artworkCalls)
	}
	// Private addresses are refused by the safe client, and nothing redirects.
	private := h.srv.ids.put(refChannel, "ch-x", refContext{Poster: "http://127.0.0.1:1/logo.png"})
	rec = h.do(http.MethodGet, "/Items/"+private+"/Images/Primary", "", nil)
	if rec.Code != http.StatusNotFound || rec.Header().Get("Location") != "" {
		t.Fatalf("private image: code=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestServerIDSurvivesSettingsClobber(t *testing.T) {
	dir := t.TempDir()
	mgr := config.NewManager(filepath.Join(dir, "settings.json"))
	first, err := EnsureServerID(mgr, dir)
	if err != nil || normalizeID(first.ServerID) == "" {
		t.Fatalf("first id: %+v %v", first, err)
	}
	// A settings save from a stale page drops the ID.
	settings, _ := mgr.Load()
	settings.JellyfinBridge.ServerID = ""
	if err := mgr.Save(settings); err != nil {
		t.Fatal(err)
	}
	second, err := EnsureServerID(mgr, dir)
	if err != nil || second.ServerID != first.ServerID {
		t.Fatalf("server id changed after settings clobber: %q -> %q (%v)", first.ServerID, second.ServerID, err)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "jellyfin_bridge", "server_id")); strings.TrimSpace(string(raw)) != first.ServerID {
		t.Fatalf("id file = %q", raw)
	}
	// Defaults are backfilled for installs that predate the section.
	if !settings.JellyfinBridge.UDPDiscovery || settings.JellyfinBridge.Port != 8096 {
		t.Fatalf("defaults not applied: %+v", settings.JellyfinBridge)
	}
}

func TestBridgeDefaultsBackfilledForOldConfigs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"server":{"host":"0.0.0.0","port":7777}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewManager(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	b := settings.JellyfinBridge
	if b.Enabled || !b.UDPDiscovery || b.Port != 8096 || b.PlaybackInfoWaitSeconds != 20 {
		t.Fatalf("old config bridge settings = %+v", b)
	}
}

func TestFailedStartStopsStores(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	cfg := h.srv.cfg
	cfg.Host, cfg.Port = "127.0.0.1", busy.Addr().(*net.TCPAddr).Port
	srv := New(h.srv.deps, cfg)
	if err := srv.Start(); err == nil {
		t.Fatal("start on a taken port succeeded")
	}
	for name, done := range map[string]chan struct{}{"ids": srv.ids.stop, "prefs": srv.prefs.stop} {
		select {
		case <-done:
		default:
			t.Fatalf("%s flush loop still running after failed start", name)
		}
	}
}

func TestAmbiguousBareProfileNameRefusedBeforePasswordChecks(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	for _, name := range []string{"carol", "dave", "erin"} {
		acct, err := h.accounts.Create(name, name+"-pass")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.users.CreateForAccount(acct.ID, "Kids"); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(h.srv.loginCandidates("Kids")); n <= loginMaxCandidates {
		t.Fatalf("setup: %d candidates", n)
	}
	rec := h.do(http.MethodPost, "/Users/AuthenticateByName", "", map[string]string{"Username": "Kids", "Pw": "carol-pass"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bare name over the candidate cap = %d", rec.Code)
	}
	h.login("carol/Kids", "carol-pass")
}

func TestQuickConnectAuthorizeLimitsWrongCodes(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{QuickConnect: true})
	tok := h.login("Living Room", "alice-pass")
	for i := 0; i < loginMaxFailures; i++ {
		if rec := h.do(http.MethodPost, "/QuickConnect/Authorize?code=000000", tok, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("guess %d = %d", i, rec.Code)
		}
	}
	if rec := h.do(http.MethodPost, "/QuickConnect/Authorize?code=000000", tok, nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("guess past the limit = %d", rec.Code)
	}
}
