package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Metadata traffic has its own budget. Playback and quality probes never use it.
type liveDiscovery struct {
	mu      sync.Mutex
	hosts   map[string]*discoveryHost
	entries map[string]*discoveryEntry
	flights map[string]*discoveryFlight
	slots   chan struct{}
	parsed  map[string][]LiveChannel
	bytes   int
}
type discoveryHost struct {
	slots chan struct{}
	until time.Time
	code  int
}
type discoveryEntry struct {
	body     []byte
	header   http.Header
	code     int
	expires  time.Time
	failures int
}
type discoveryFlight struct {
	done  chan struct{}
	entry *discoveryEntry
	err   error
}
type discoveryTransport struct {
	owner *liveDiscovery
	next  http.RoundTripper
	proxy string
	ttl   time.Duration
}

func (h *LiveHandler) discoveryClient(client *http.Client, proxy string, ttl time.Duration) *http.Client {
	h.discoveryOnce.Do(func() {
		h.discovery = &liveDiscovery{hosts: map[string]*discoveryHost{}, entries: map[string]*discoveryEntry{}, flights: map[string]*discoveryFlight{}, slots: make(chan struct{}, 4)}
	})
	if client == nil {
		client = http.DefaultClient
	}
	copyClient := *client
	next := client.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	copyClient.Transport = &discoveryTransport{owner: h.discovery, next: next, proxy: proxy, ttl: ttl}
	return &copyClient
}
func discoveryResponse(req *http.Request, e *discoveryEntry) *http.Response {
	return &http.Response{StatusCode: e.code, Status: http.StatusText(e.code), Header: e.header.Clone(), Body: io.NopCloser(bytes.NewReader(e.body)), ContentLength: int64(len(e.body)), Request: req}
}
func retryDelay(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && seconds >= 0 {
		return time.Duration(min(seconds, int64((1<<63-1)/int64(time.Second)))) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		return date.Sub(now)
	}
	return time.Minute
}
func cooldownEntry(until time.Time, code int) *discoveryEntry {
	seconds := int(time.Until(until).Seconds()) + 1
	if seconds < 1 {
		seconds = 1
	}
	if code == 0 {
		code = http.StatusServiceUnavailable
	}
	return &discoveryEntry{code: code, header: http.Header{"Retry-After": []string{strconv.Itoa(seconds)}}, body: []byte("Provider cooldown; retry later")}
}
func (t *discoveryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return t.next.RoundTrip(req)
	}
	d := t.owner
	// Credentials/configuration and proxy are part of the cache identity; keys never expose them.
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(req.URL.String()+"\x00"+t.proxy+"\x00"+req.Header.Get("Authorization")+"\x00"+req.Header.Get("Cookie")+"\x00"+req.Header.Get("User-Agent"))))
	hostKey := strings.ToLower(req.URL.Hostname())
	d.mu.Lock()
	host := d.hosts[hostKey]
	if host == nil {
		host = &discoveryHost{slots: make(chan struct{}, 2)}
		d.hosts[hostKey] = host
	}
	old := d.entries[key]
	if old != nil && time.Now().Before(old.expires) {
		d.mu.Unlock()
		return discoveryResponse(req, old), nil
	}
	if time.Now().Before(host.until) {
		entry := cooldownEntry(host.until, host.code)
		d.mu.Unlock()
		return discoveryResponse(req, entry), nil
	}
	flight := d.flights[key]
	if flight == nil {
		flight = &discoveryFlight{done: make(chan struct{})}
		d.flights[key] = flight
		// A departing caller must not cancel another screen's shared metadata request.
		go t.fetch(req, key, host, old, flight)
	}
	d.mu.Unlock()
	select {
	case <-req.Context().Done():
		return nil, req.Context().Err()
	case <-flight.done:
	}
	if flight.err != nil {
		return nil, flight.err
	}
	return discoveryResponse(req, flight.entry), nil
}
func (t *discoveryTransport) fetch(original *http.Request, key string, host *discoveryHost, old *discoveryEntry, flight *discoveryFlight) {
	d := t.owner
	ctx, cancel := context.WithTimeout(context.WithoutCancel(original.Context()), 30*time.Second)
	defer cancel()
	defer func() { d.mu.Lock(); delete(d.flights, key); close(flight.done); d.mu.Unlock() }()
	select {
	case host.slots <- struct{}{}:
		defer func() { <-host.slots }()
	case <-ctx.Done():
		flight.err = ctx.Err()
		return
	}
	select {
	case d.slots <- struct{}{}:
		defer func() { <-d.slots }()
	case <-ctx.Done():
		flight.err = ctx.Err()
		return
	}
	d.mu.Lock()
	until, code := host.until, host.code
	d.mu.Unlock()
	if time.Now().Before(until) {
		flight.entry = cooldownEntry(until, code)
		return
	}
	req := original.Clone(ctx)
	if old != nil && old.code == 200 {
		if value := old.header.Get("ETag"); value != "" {
			req.Header.Set("If-None-Match", value)
		}
		if value := old.header.Get("Last-Modified"); value != "" {
			req.Header.Set("If-Modified-Since", value)
		}
	}
	response, err := t.next.RoundTrip(req)
	if err != nil {
		// Keep transport failures isolated to this endpoint and credential identity.
		response = discoveryResponse(req, &discoveryEntry{code: 503, header: http.Header{}})
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, defaultMaxPlaylistSize+1))
	if err != nil || len(body) > defaultMaxPlaylistSize {
		flight.err = fmt.Errorf("metadata response incomplete or too large")
		return
	}
	entry := &discoveryEntry{body: body, header: response.Header.Clone(), code: response.StatusCode, expires: time.Now().Add(t.ttl)}
	if response.StatusCode == 304 && old != nil {
		entry = &discoveryEntry{body: old.body, header: old.header.Clone(), code: 200, expires: time.Now().Add(t.ttl)}
		for k, values := range response.Header {
			entry.header[k] = values
		}
	}
	d.mu.Lock()
	switch {
	case response.StatusCode == 429:
		host.code = 429
		until := time.Now().Add(retryDelay(response.Header.Get("Retry-After"), time.Now()))
		if until.After(host.until) {
			host.until = until
		}
	case response.StatusCode >= 500:
		entry.failures = 1
		if old != nil {
			entry.failures = old.failures + 1
		}
		entry.expires = time.Now().Add(min(t.ttl, time.Duration(10<<min(entry.failures-1, 3))*time.Second))
	case response.StatusCode == 401 || response.StatusCode == 403:
		entry.expires = time.Now().Add(min(t.ttl, 5*time.Minute)) // Per credential/request, not a host-wide auth lockout.
	}
	if entry.code == 200 || entry.code == 401 || entry.code == 403 || entry.code >= 500 {
		for len(d.entries) >= 128 || d.bytes+len(entry.body) > 64*1024*1024 {
			for k, e := range d.entries {
				d.bytes -= len(e.body)
				delete(d.entries, k)
				break
			}
		}
		if previous := d.entries[key]; previous != nil {
			d.bytes -= len(previous.body)
		}
		d.entries[key] = entry
		d.bytes += len(entry.body)
	}
	d.mu.Unlock()
	flight.entry = entry
}

func (h *LiveHandler) parsedDiscoveryPlaylist(contents string) []LiveChannel {
	h.discoveryClient(nil, "", time.Minute)
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(contents)))
	h.discovery.mu.Lock()
	defer h.discovery.mu.Unlock()
	if h.discovery.parsed == nil {
		h.discovery.parsed = map[string][]LiveChannel{}
	}
	if channels, ok := h.discovery.parsed[key]; ok {
		return channels
	}
	channels := parseM3UPlaylist(contents)
	if len(h.discovery.parsed) >= 8 {
		for k := range h.discovery.parsed {
			delete(h.discovery.parsed, k)
			break
		}
	}
	// Large playlists are parsed once per fetch but do not occupy the parsed cache.
	if len(contents) <= 4*1024*1024 {
		h.discovery.parsed[key] = channels
	}
	return channels
}
