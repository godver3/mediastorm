package netproxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// NewHTTPClient returns an HTTP client that optionally routes requests through
// an HTTP(S) or SOCKS5 proxy URL.
func NewHTTPClient(timeout time.Duration, rawProxyURL string) (*http.Client, error) {
	return NewHTTPClientWithOptions(HTTPClientOptions{Timeout: timeout}, rawProxyURL)
}

// HTTPClientOptions controls request-level and transport-level timeouts for
// proxied HTTP clients.
type HTTPClientOptions struct {
	Timeout               time.Duration
	ResponseHeaderTimeout time.Duration
}

// NewHTTPClientWithOptions returns an HTTP client that optionally routes
// requests through an HTTP(S) or SOCKS5 proxy URL.
func NewHTTPClientWithOptions(options HTTPClientOptions, rawProxyURL string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	transport.ResponseHeaderTimeout = options.ResponseHeaderTimeout

	proxyURL := strings.TrimSpace(rawProxyURL)
	if proxyURL != "" {
		if err := configureTransportProxy(transport, proxyURL); err != nil {
			return nil, err
		}
	}

	return &http.Client{
		Timeout:   options.Timeout,
		Transport: transport,
	}, nil
}

func configureTransportProxy(transport *http.Transport, rawProxyURL string) error {
	parsed, err := url.Parse(rawProxyURL)
	if err != nil {
		return fmt.Errorf("invalid proxy URL: %w", err)
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		transport.Proxy = http.ProxyURL(parsed)
		return nil
	case "socks5", "socks5h":
		auth := proxyAuth(parsed)
		dialer, err := proxy.SOCKS5("tcp", parsed.Host, auth, proxy.Direct)
		if err != nil {
			return fmt.Errorf("invalid SOCKS5 proxy: %w", err)
		}
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			type result struct {
				conn net.Conn
				err  error
			}
			ch := make(chan result, 1)
			go func() {
				conn, err := dialer.Dial(network, address)
				ch <- result{conn: conn, err: err}
			}()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case res := <-ch:
				return res.conn, res.err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported proxy scheme %q", parsed.Scheme)
	}
}

func proxyAuth(proxyURL *url.URL) *proxy.Auth {
	if proxyURL.User == nil {
		return nil
	}
	password, _ := proxyURL.User.Password()
	return &proxy.Auth{
		User:     proxyURL.User.Username(),
		Password: password,
	}
}
