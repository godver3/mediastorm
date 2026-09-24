package sports

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestProviderDistinctRequestsShareFourSlotsAcrossClients(t *testing.T) {
	pool := newSportsHTTPPool()
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	var active, peak atomic.Int32
	next := coverageTransport(func(r *http.Request) (*http.Response, error) {
		n := active.Add(1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		active.Add(-1)
		return coverageResponse(200, `{}`), nil
	})
	done := make(chan struct{}, 8)
	for i := 0; i < 8; i++ {
		go func(i int) {
			client := &http.Client{Transport: &sportsHTTPTransport{pool: pool, next: next}}
			resp, err := client.Get(fmt.Sprintf("https://%s/scoreboard/%d", []string{"site.api.espn.com", "site.web.api.espn.com", "sports.core.api.espn.com"}[i%3], i))
			if err != nil {
				t.Error(err)
			} else {
				resp.Body.Close()
			}
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 4; i++ {
		<-entered
	}
	if active.Load() != 4 {
		t.Fatal("provider slots not shared")
	}
	close(release)
	for i := 0; i < 8; i++ {
		<-done
	}
	if peak.Load() > 4 {
		t.Fatalf("peak=%d", peak.Load())
	}
}

func TestProviderCanceledWaiterDoesNotCancelSharedFetch(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	client := &http.Client{Transport: &sportsHTTPTransport{pool: newSportsHTTPPool(), next: coverageTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		close(entered)
		<-release
		if r.Context().Err() != nil {
			t.Error("shared provider request canceled by first waiter")
		}
		return coverageResponse(200, `{}`), nil
	})}}
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://cancel.test/teams", nil)
	done := make(chan error, 1)
	go func() { _, err := client.Do(req); done <- err }()
	<-entered
	cancel()
	if <-done == nil {
		t.Fatal("canceled caller still waited")
	}
	close(release)
	resp, err := client.Get("https://cancel.test/teams")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if calls.Load() != 1 {
		t.Fatal("shared work duplicated")
	}
}

type coverageTransport func(*http.Request) (*http.Response, error)

func (f coverageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func coverageResponse(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(bytes.NewBufferString(body))}
}
func TestProviderSharedConcurrencyAndCache(t *testing.T) {
	var active, peak, calls atomic.Int32
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	client := &http.Client{Transport: &sportsHTTPTransport{pool: newSportsHTTPPool(), next: coverageTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		n := active.Add(1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		active.Add(-1)
		return coverageResponse(200, `{"events":[]}`), nil
	})}}
	done := make(chan struct{}, 8)
	for i := 0; i < 8; i++ {
		go func() {
			resp, err := client.Get("https://provider.test/scoreboard")
			if err != nil {
				t.Error(err)
			} else {
				resp.Body.Close()
			}
			done <- struct{}{}
		}()
	}
	<-entered
	close(release)
	for i := 0; i < 8; i++ {
		<-done
	}
	if calls.Load() != 1 || peak.Load() > 4 {
		t.Fatalf("calls=%d peak=%d", calls.Load(), peak.Load())
	}
}
func TestProviderRetryAfterAndPermanentCapabilityBackoff(t *testing.T) {
	var calls atomic.Int32
	pool := newSportsHTTPPool()
	client := &http.Client{Transport: &sportsHTTPTransport{pool: pool, next: coverageTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Path == "/missing" {
			return coverageResponse(404, ""), nil
		}
		resp := coverageResponse(429, "")
		resp.Header.Set("Retry-After", "120")
		return resp, nil
	})}}
	for i := 0; i < 2; i++ {
		resp, err := client.Get("https://provider.test/missing")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatal("missingcapabilitychanged")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("missing capability hammered")
	}
	for _, path := range []string{"/limited", "/different"} {
		resp, err := client.Get("https://provider.test" + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 429 {
			t.Fatal("rate limit not propagated")
		}
	}
	if calls.Load() != 2 {
		t.Fatal("host cooldown bypassed")
	}
	if providerRetryAfter(time.Now().Add(time.Minute).UTC().Format(http.TimeFormat), time.Now()) < 50*time.Second {
		t.Fatal("HTTP date Retry-After")
	}
}
func TestProviderCompressedPayloadAndScheduleCadence(t *testing.T) {
	var body bytes.Buffer
	z := gzip.NewWriter(&body)
	z.Write([]byte(`{"events":[]}`))
	z.Close()
	client := &http.Client{Transport: &sportsHTTPTransport{pool: newSportsHTTPPool(), next: coverageTransport(func(r *http.Request) (*http.Response, error) {
		resp := coverageResponse(200, body.String())
		resp.Header.Set("Content-Encoding", "gzip")
		return resp, nil
	})}}
	resp, err := client.Get("https://provider.test/scoreboard")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if string(got) != `{"events":[]}` || resp.Header.Get("Content-Encoding") != "" {
		t.Fatal("compressed metadata lost")
	}
	if sportsMetadataTTL("/scoreboard", []byte(`{"events":[{"status":{"type":{"state":"in"}}}]}`)) != 30*time.Second {
		t.Fatal("livecadence")
	}
	if sportsMetadataTTL("/scoreboard", got) != 15*time.Minute {
		t.Fatal("empty schedule polled like live")
	}
	if sportsMetadataTTL("/teams", nil) != 24*time.Hour {
		t.Fatal("catalog cadence")
	}
}
