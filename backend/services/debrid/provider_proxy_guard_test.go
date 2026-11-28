package debrid

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// These source-level guards keep new code from silently bypassing a
// provider's proxy. If one fails, route the new call through
// NewProviderFromSettings / NewProviderHTTPClient / streamHTTPClientFor
// rather than extending the allowlist, unless the traffic provably never
// goes to a debrid provider.

var commentLine = regexp.MustCompile(`(?m)^\s*//.*$`)

func readGoSources(t *testing.T, dir string) map[string]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		// Ignore whole-line comments (commented-out code is not a leak).
		out[filepath.Base(f)] = commentLine.ReplaceAllString(string(b), "")
	}
	if len(out) == 0 {
		t.Fatalf("no sources found in %s", dir)
	}
	return out
}

func TestGuardProviderConstructionGoesThroughSettingsFactory(t *testing.T) {
	re := regexp.MustCompile(`(^|[^A-Za-z.])GetProvider\(|DefaultRegistry\.Get\(|\.MustGet\(`)
	allowed := map[string]bool{"provider.go": true, "provider_proxy.go": true}
	for name, src := range readGoSources(t, ".") {
		if allowed[name] {
			continue
		}
		if loc := re.FindStringIndex(src); loc != nil {
			t.Errorf("%s builds a provider without NewProviderFromSettings (proxy would be skipped): %q", name, src[loc[0]:loc[1]])
		}
	}
}

func TestGuardProviderClientsUseProxyAwareHTTPClient(t *testing.T) {
	for name, src := range readGoSources(t, ".") {
		if !strings.HasSuffix(name, "_client.go") && name != "account_info.go" {
			continue
		}
		for _, banned := range []string{"&http.Client{", "http.DefaultClient", "http.Get(", "http.Post(", "http.Head("} {
			if strings.Contains(src, banned) {
				t.Errorf("%s uses %s; provider clients must use c.httpClient so the proxy applies", name, banned)
			}
		}
	}
}

func TestGuardDownloadPathsUsePerProviderClient(t *testing.T) {
	srcs := readGoSources(t, ".")
	for _, name := range []string{"streaming.go", "proxy.go"} {
		for _, banned := range []string{"p.httpClient.Do(", "s.httpClient.Do(", "(ctx, p.httpClient,", "(ctx, s.httpClient,"} {
			if strings.Contains(srcs[name], banned) {
				t.Errorf("%s uses the shared direct client (%s); use streamHTTPClientFor / downloadClient", name, banned)
			}
		}
	}
}

// http.DefaultClient in this package is only allowed for pre-resolved scraper
// stream URLs (addon-provided links, not a configured debrid provider).
func TestGuardDefaultClientUsageIsAllowlisted(t *testing.T) {
	allowed := map[string]int{"health.go": 3}
	for name, src := range readGoSources(t, ".") {
		if got := strings.Count(src, "http.DefaultClient"); got != allowed[name] {
			t.Errorf("%s has %d uses of http.DefaultClient, allowlist says %d; debrid provider traffic must use the proxy-aware clients", name, got, allowed[name])
		}
	}
}

func TestGuardEveryRegisteredProviderIsCoveredByProxyTests(t *testing.T) {
	re := regexp.MustCompile(`RegisterProvider\("([a-z0-9_-]+)"`)
	seen := 0
	for name, src := range readGoSources(t, ".") {
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			seen++
			rp, ok := realProviders[m[1]]
			if !ok {
				t.Errorf("provider %q (registered in %s) is missing from realProviders in provider_proxy_test.go; add it so its proxy routing is exercised", m[1], name)
				continue
			}
			if _, ok := rp.make("k").(httpTransportSetter); !ok {
				t.Errorf("provider %q does not implement useHTTPTransport", m[1])
			}
		}
	}
	if seen != len(realProviders) {
		t.Errorf("found %d RegisterProvider calls, realProviders has %d entries", seen, len(realProviders))
	}
}

func TestGuardHandlersDoNotBuildProviderClientsDirectly(t *testing.T) {
	re := regexp.MustCompile(`debrid\.(GetProvider|New(RealDebrid|Torbox|AllDebrid|Premiumize|DebridLink|Torrin)Client)\(`)
	for name, src := range readGoSources(t, filepath.Join("..", "..", "handlers")) {
		if loc := re.FindStringIndex(src); loc != nil {
			t.Errorf("handlers/%s builds a debrid client directly (%q); use debrid.NewProviderFromSettings", name, src[loc[0]:loc[1]])
		}
	}
	admin := readGoSources(t, filepath.Join("..", "..", "handlers"))["admin_ui.go"]
	start := strings.Index(admin, "func (h *AdminUIHandler) TestDebridProvider(")
	if start < 0 {
		t.Fatal("TestDebridProvider not found")
	}
	end := strings.Index(admin[start+1:], "\nfunc ")
	body := admin[start : start+1+end]
	for _, banned := range []string{"&http.Client{", "http.DefaultClient"} {
		if strings.Contains(body, banned) {
			t.Errorf("TestDebridProvider uses %s; the connection test must go through the provider's proxy", banned)
		}
	}
}
