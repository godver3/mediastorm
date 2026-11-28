package debrid

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/internal/apiusage"
	"novastream/internal/netproxy"
)

// Per-provider proxy routing.
//
// A debrid provider with ProxyEnabled sends ALL of its traffic through its
// proxy: API calls (cache checks, adding magnets, unrestricting links),
// stream downloads, RAR header probes and the admin connection test. It is
// fail-closed: a provider whose proxy is enabled but misconfigured gets an
// error, never a silent direct connection from the server's own IP.
//
// Code that talks to a debrid provider must get its client from
// NewProviderFromSettings (API) or the streaming/proxy services' per-provider
// download clients. provider_proxy_guard_test.go rejects new direct calls.

// debridAPITimeout is the per-request timeout of every provider API client.
const debridAPITimeout = 30 * time.Second

// ErrDirectURLProxied is returned by GetDirectURL for a provider that routes
// through a proxy. Handing the raw CDN URL to FFmpeg, a probe or a client app
// would fetch it from the server (or the client) directly, bypassing the
// proxy, so callers fall back to streaming through the backend instead.
var ErrDirectURLProxied = errors.New("debrid provider routes through a proxy; direct URL unavailable")

// httpTransportSetter is implemented by every provider API client so a
// proxied transport can be installed after construction.
type httpTransportSetter interface {
	useHTTPTransport(rt http.RoundTripper)
}

// newProviderAPIClient builds a provider API client: the standard 30 s
// timeout, API usage tracking, and rt (nil = default transport).
func newProviderAPIClient(rt http.RoundTripper, label string) *http.Client {
	return apiusage.TrackClient(&http.Client{Timeout: debridAPITimeout, Transport: rt}, label, "API request")
}

// NewProviderFromSettings builds a configured provider client from its
// settings: registry lookup, provider-specific Configure, and the proxy.
// It is the only supported way to get a provider for real traffic.
func NewProviderFromSettings(pc config.DebridProviderSettings) (Provider, error) {
	name := strings.ToLower(strings.TrimSpace(pc.Provider))
	client, ok := DefaultRegistry.Get(name, pc.APIKey)
	if !ok {
		return nil, fmt.Errorf("provider %q not registered", pc.Provider)
	}
	if configurable, ok := client.(Configurable); ok && pc.Config != nil {
		configurable.Configure(pc.Config)
	}
	proxyURL, err := pc.EffectiveProxyURL()
	if err != nil {
		return nil, err
	}
	if proxyURL == "" {
		return client, nil
	}
	setter, ok := client.(httpTransportSetter)
	if !ok {
		return nil, fmt.Errorf("provider %q does not support proxy routing", pc.Provider)
	}
	transport, err := proxiedAPITransport(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("debrid provider %q proxy: %w", pc.Name, err)
	}
	setter.useHTTPTransport(transport)
	return client, nil
}

// NewProviderHTTPClient returns an HTTP client for ad-hoc requests to a
// provider (the admin connection test), routed through the provider's proxy
// when enabled.
func NewProviderHTTPClient(pc config.DebridProviderSettings, timeout time.Duration) (*http.Client, error) {
	proxyURL, err := pc.EffectiveProxyURL()
	if err != nil {
		return nil, err
	}
	if proxyURL == "" {
		return &http.Client{Timeout: timeout}, nil
	}
	transport, err := proxiedAPITransport(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("debrid provider %q proxy: %w", pc.Name, err)
	}
	return &http.Client{Timeout: timeout, Transport: transport}, nil
}

// proxyURLForProvider returns the proxy URL of the first enabled provider of
// the given type ("" = direct). It matches how the streaming paths pick a
// provider config.
func proxyURLForProvider(settings *config.Settings, providerName string) (string, error) {
	if settings == nil {
		return "", nil
	}
	for _, pc := range settings.Streaming.DebridProviders {
		if pc.Enabled && strings.EqualFold(strings.TrimSpace(pc.Provider), strings.TrimSpace(providerName)) {
			return pc.EffectiveProxyURL()
		}
	}
	return "", nil
}

// Proxied transports are cached per proxy URL so connections are pooled
// across the short-lived provider clients the services create per request.
var (
	proxiedTransportsMu     sync.Mutex
	proxiedAPITransports    = map[string]*http.Transport{}
	proxiedStreamTransports = map[string]*http.Transport{}
)

func proxiedAPITransport(proxyURL string) (*http.Transport, error) {
	proxiedTransportsMu.Lock()
	defer proxiedTransportsMu.Unlock()
	if t, ok := proxiedAPITransports[proxyURL]; ok {
		return t, nil
	}
	t, err := netproxy.NewTransport(netproxy.HTTPClientOptions{}, proxyURL)
	if err != nil {
		return nil, err
	}
	proxiedAPITransports[proxyURL] = t
	return t, nil
}

// proxiedStreamTransport mirrors the direct streaming transport's tuning
// (pooled warm connections for rapid seeks, 15 s header timeout) on top of
// the proxy routing.
func proxiedStreamTransport(proxyURL string) (*http.Transport, error) {
	proxiedTransportsMu.Lock()
	defer proxiedTransportsMu.Unlock()
	if t, ok := proxiedStreamTransports[proxyURL]; ok {
		return t, nil
	}
	t, err := netproxy.NewTransport(netproxy.HTTPClientOptions{ResponseHeaderTimeout: 15 * time.Second}, proxyURL)
	if err != nil {
		return nil, err
	}
	t.ForceAttemptHTTP2 = true
	t.MaxIdleConns = 20
	t.MaxIdleConnsPerHost = 8
	t.MaxConnsPerHost = 0
	t.IdleConnTimeout = 5 * time.Minute
	t.TLSHandshakeTimeout = 10 * time.Second
	t.ExpectContinueTimeout = 1 * time.Second
	proxiedStreamTransports[proxyURL] = t
	return t, nil
}

// streamHTTPClientFor returns the download client for a provider: direct
// when its proxy is off, otherwise a pooled client through the proxy.
func streamHTTPClientFor(direct *http.Client, settings *config.Settings, providerName string) (*http.Client, error) {
	proxyURL, err := proxyURLForProvider(settings, providerName)
	if err != nil {
		return nil, err
	}
	if proxyURL == "" {
		return direct, nil
	}
	t, err := proxiedStreamTransport(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("debrid provider %q proxy: %w", providerName, err)
	}
	return &http.Client{Transport: t, Timeout: direct.Timeout, CheckRedirect: direct.CheckRedirect}, nil
}
