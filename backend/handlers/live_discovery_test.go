package handlers

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type discoveryTestTransport func(*http.Request) (*http.Response, error)

func (f discoveryTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func discoveryTestResponse(code int, body string, header http.Header) *http.Response {
	return &http.Response{StatusCode: code, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

func TestDiscoverySharesRequestsAndSurvivesCancellation(t *testing.T) {
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	h := &LiveHandler{}
	client := h.discoveryClient(&http.Client{Transport: discoveryTestTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		close(started)
		<-release
		if r.Context().Err() != nil {
			t.Error("shared upstream request cancelled")
		}
		return discoveryTestResponse(200, "catalog", http.Header{}), nil
	})}, "", time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://provider.test/catalog", nil)
	first := make(chan error, 1)
	go func() { _, err := client.Do(req); first <- err }()
	<-started
	cancel()
	if err := <-first; err == nil {
		t.Fatal("cancelled caller did not exit")
	}
	second := make(chan error, 1)
	go func() {
		resp, err := client.Get("https://provider.test/catalog")
		if resp != nil {
			resp.Body.Close()
		}
		second <- err
	}()
	close(release)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls=%d", calls.Load())
	}
}

func TestDiscoveryCooldownAndCredentialIsolation(t *testing.T) {
	var calls atomic.Int32
	h := &LiveHandler{}
	client := h.discoveryClient(&http.Client{Transport: discoveryTestTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Path == "/limit" {
			return discoveryTestResponse(429, "", http.Header{"Retry-After": []string{"120"}}), nil
		}
		if r.Header.Get("Authorization") == "bad" {
			return discoveryTestResponse(401, "", http.Header{}), nil
		}
		return discoveryTestResponse(200, "ok", http.Header{}), nil
	})}, "", time.Minute)
	request := func(path, auth string) int {
		t.Helper()
		req, _ := http.NewRequest("GET", "https://provider.test"+path, nil)
		req.Header.Set("Authorization", auth)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if request("/catalog", "bad") != 401 || request("/catalog", "good") != 200 {
		t.Fatal("credential caches mixed")
	}
	if request("/limit", "") != 429 || request("/different", "") != 429 {
		t.Fatal("host cooldown not honored")
	}
	if calls.Load() != 3 {
		t.Fatalf("requests during cooldown: %d", calls.Load())
	}
	// Previously successful metadata is still available during the cooldown.
	if request("/catalog", "good") != 200 || calls.Load() != 3 {
		t.Fatal("successful cached data lost")
	}
}

func TestDiscoveryConditionalRefresh(t *testing.T) {
	var calls atomic.Int32
	h := &LiveHandler{}
	client := h.discoveryClient(&http.Client{Transport: discoveryTestTransport(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return discoveryTestResponse(200, "catalog", http.Header{"Etag": []string{"v1"}}), nil
		}
		if r.Header.Get("If-None-Match") != "v1" {
			t.Error("missing conditional request")
		}
		return discoveryTestResponse(304, "", http.Header{"Etag": []string{"v2"}}), nil
	})}, "", 0)
	for i := 0; i < 2; i++ {
		resp, err := client.Get("https://provider.test/catalog")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(body) != "catalog" {
			t.Fatal("304 did not retain catalog")
		}
	}
}

func TestDiscoveryRetryAfterAndBackoff(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	if retryDelay("120", now) != 2*time.Minute || retryDelay(now.Add(time.Hour).Format(http.TimeFormat), now) != time.Hour {
		t.Fatal("Retry-After parsing")
	}
	if retryDelay("9223372036854775807", now) <= 0 {
		t.Fatal("overflowed delay")
	}
	var calls atomic.Int32
	h := &LiveHandler{}
	client := h.discoveryClient(&http.Client{Transport: discoveryTestTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return discoveryTestResponse(503, "", http.Header{}), nil
	})}, "", time.Minute)
	for i := 0; i < 2; i++ {
		resp, err := client.Get("https://provider.test/catalog")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 503 {
			t.Fatal("failure misreported as rate limit")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("provider hammered during backoff")
	}
}

func TestDiscoveryConcurrencyBudget(t *testing.T) {
	var active, peak atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	h := &LiveHandler{}
	client := h.discoveryClient(&http.Client{Transport: discoveryTestTransport(func(r *http.Request) (*http.Response, error) {
		n := active.Add(1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return discoveryTestResponse(200, "ok", http.Header{}), nil
	})}, "", time.Minute)
	done := make(chan struct{}, 8)
	for _, path := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		go func(path string) {
			resp, err := client.Get("https://provider.test/" + path)
			if err == nil {
				resp.Body.Close()
			} else {
				t.Error(err)
			}
			done <- struct{}{}
		}(path)
	}
	<-started
	<-started
	select {
	case <-started:
		t.Error("more than two requests for one provider")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	for i := 0; i < 8; i++ {
		<-done
	}
	if peak.Load() > 2 {
		t.Fatal("per-provider concurrency exceeded")
	}
}
