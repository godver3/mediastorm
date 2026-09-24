package sports

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const providerBodyLimit = 32 << 20

type sportsHTTPEntry struct {
	body     []byte
	header   http.Header
	status   int
	expires  time.Time
	failures int
}
type sportsHTTPFlight struct {
	done  chan struct{}
	entry *sportsHTTPEntry
	err   error
}
type sportsHTTPHost struct {
	slots chan struct{}
	until time.Time
}
type sportsHTTPPool struct {
	mu      sync.Mutex
	entries map[string]*sportsHTTPEntry
	flights map[string]*sportsHTTPFlight
	hosts   map[string]*sportsHTTPHost
	bytes   int
}
type sportsHTTPTransport struct {
	next http.RoundTripper
	pool *sportsHTTPPool
}

var sharedSportsHTTP = newSportsHTTPPool()

func newSportsHTTPPool() *sportsHTTPPool {
	return &sportsHTTPPool{entries: map[string]*sportsHTTPEntry{}, flights: map[string]*sportsHTTPFlight{}, hosts: map[string]*sportsHTTPHost{}}
}
func boundedSportsClient(client *http.Client) *http.Client {
	clone := *client
	next := client.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	clone.Transport = &sportsHTTPTransport{next: next, pool: sharedSportsHTTP}
	return &clone
}
func sportsHTTPResponse(req *http.Request, e *sportsHTTPEntry) *http.Response {
	return &http.Response{StatusCode: e.status, Header: e.header.Clone(), Body: io.NopCloser(bytes.NewReader(e.body)), ContentLength: int64(len(e.body)), Request: req}
}
func providerRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && seconds >= 0 {
		return time.Duration(min(seconds, int64((1<<63-1)/int64(time.Second)))) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		return date.Sub(now)
	}
	return time.Minute
}
func providerCooldown(until time.Time) *sportsHTTPEntry {
	return &sportsHTTPEntry{status: 429, header: http.Header{"Retry-After": []string{strconv.Itoa(max(1, int(time.Until(until).Seconds())+1))}}}
}
func (t *sportsHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return t.next.RoundTrip(req)
	}
	headers, _ := json.Marshal(req.Header)
	key := fmt.Sprintf("%x", sha256.Sum256(append([]byte(req.URL.String()), headers...)))
	p := t.pool
	p.mu.Lock()
	old := p.entries[key]
	if old != nil && time.Now().Before(old.expires) {
		p.mu.Unlock()
		return sportsHTTPResponse(req, old), nil
	}
	provider := sportsProviderKey(req.URL.Hostname())
	host := p.hosts[provider]
	if host == nil {
		host = &sportsHTTPHost{slots: make(chan struct{}, 4)}
		p.hosts[provider] = host
	}
	if time.Now().Before(host.until) {
		entry := providerCooldown(host.until)
		p.mu.Unlock()
		return sportsHTTPResponse(req, entry), nil
	}
	flight := p.flights[key]
	if flight == nil {
		flight = &sportsHTTPFlight{done: make(chan struct{})}
		p.flights[key] = flight
		go t.fetch(req, key, host, old, flight)
	}
	p.mu.Unlock()
	select {
	case <-req.Context().Done():
		return nil, req.Context().Err()
	case <-flight.done:
	}
	if flight.err != nil {
		return nil, flight.err
	}
	return sportsHTTPResponse(req, flight.entry), nil
}
func (t *sportsHTTPTransport) fetch(original *http.Request, key string, host *sportsHTTPHost, old *sportsHTTPEntry, f *sportsHTTPFlight) {
	p := t.pool
	defer func() { p.mu.Lock(); delete(p.flights, key); close(f.done); p.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(original.Context()), 20*time.Second)
	defer cancel()
	select {
	case host.slots <- struct{}{}:
		defer func() { <-host.slots }()
	case <-ctx.Done():
		f.err = ctx.Err()
		return
	}
	p.mu.Lock()
	until := host.until
	p.mu.Unlock()
	if time.Now().Before(until) {
		f.entry = providerCooldown(until)
		return
	}
	req := original.Clone(ctx)
	response, err := t.next.RoundTrip(req)
	if err != nil {
		response = &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}
	}
	defer response.Body.Close()
	var reader io.Reader = response.Body
	if strings.EqualFold(response.Header.Get("Content-Encoding"), "gzip") {
		zipped, e := gzip.NewReader(response.Body)
		if e != nil {
			f.err = e
			return
		}
		defer zipped.Close()
		reader = zipped
	}
	body, err := io.ReadAll(io.LimitReader(reader, providerBodyLimit+1))
	if err != nil || len(body) > providerBodyLimit {
		f.err = fmt.Errorf("sports provider response incomplete or exceeds %d bytes", providerBodyLimit)
		return
	}
	header := response.Header.Clone()
	header.Del("Content-Encoding")
	header.Del("Content-Length")
	e := &sportsHTTPEntry{body: body, header: header, status: response.StatusCode}
	ttl := sportsMetadataTTL(req.URL.Path, body)
	switch {
	case e.status == 429:
		p.mu.Lock()
		until := time.Now().Add(providerRetryAfter(header.Get("Retry-After"), time.Now()))
		if until.After(host.until) {
			host.until = until
		}
		p.mu.Unlock()
		f.entry = e
		return
	case e.status == 404:
		ttl = 24 * time.Hour
	case e.status == 401 || e.status == 403:
		ttl = 5 * time.Minute
	case e.status >= 500:
		e.failures = 1
		if old != nil {
			e.failures = old.failures + 1
		}
		ttl = time.Duration(15<<min(e.failures-1, 4)) * time.Second
	case e.status != 200:
		ttl = time.Minute
	}
	// Stable per-key jitter spreads refreshes without changing fixture semantics.
	jitter := time.Duration(sha256.Sum256([]byte(key))[0]%10) * ttl / 100
	e.expires = time.Now().Add(ttl + jitter)
	p.mu.Lock()
	if previous := p.entries[key]; previous != nil {
		p.bytes -= len(previous.body)
		delete(p.entries, key)
	}
	for len(p.entries) >= 1024 || p.bytes+len(e.body) > 96<<20 {
		for k, v := range p.entries {
			p.bytes -= len(v.body)
			delete(p.entries, k)
			break
		}
	}
	p.entries[key] = e
	p.bytes += len(e.body)
	p.mu.Unlock()
	f.entry = e
}
func sportsMetadataTTL(path string, body []byte) time.Duration {
	switch {
	case path == "/seasons" || strings.Contains(path, "/teams") || strings.Contains(path, "/leagues/dropdown"):
		return 24 * time.Hour
	case strings.Contains(path, "/fixtures/"):
		return 15 * time.Minute
	case strings.Contains(path, "/standings"):
		return 30 * time.Minute
	case strings.Contains(path, "/scoreboard"):
		var payload struct {
			Events []struct {
				Date         string     `json:"date"`
				Status       espnStatus `json:"status"`
				Competitions []struct {
					Status espnStatus `json:"status"`
				} `json:"competitions"`
			} `json:"events"`
		}
		if json.Unmarshal(body, &payload) != nil {
			return 30 * time.Second
		}
		for _, event := range payload.Events {
			if event.Status.Type.State == "in" {
				return 30 * time.Second
			}
			for _, c := range event.Competitions {
				if c.Status.Type.State == "in" {
					return 30 * time.Second
				}
			}
			start := parseESPNDate(event.Date)
			if !start.IsZero() && start.After(time.Now().Add(-15*time.Minute)) && start.Before(time.Now().Add(15*time.Minute)) {
				return 30 * time.Second
			}
		}
		return 15 * time.Minute
	default:
		return 30 * time.Second
	}
}

// ESPN metadata hosts share one provider budget and rate-limit cooldown.
func sportsProviderKey(host string) string {
	switch strings.ToLower(host) {
	case "site.api.espn.com", "site.web.api.espn.com", "sports.api.espn.com", "sports.core.api.espn.com", "site.api.espncricinfo.com":
		return "espn"
	default:
		return strings.ToLower(host)
	}
}
