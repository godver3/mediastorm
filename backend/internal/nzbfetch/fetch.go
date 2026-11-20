// Package nzbfetch is the single HTTP acquisition point for playback, Usenet
// availability checks and track probes.
package nzbfetch

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"novastream/internal/httpheaders"
	"novastream/internal/providerbreaker"
	"novastream/models"
)

const (
	Window     = 10 * time.Minute
	Spacing    = 30 * time.Second
	TitleLimit = 3
	// ReleaseLimit bounds grabs of one release (indexer + download URL) per
	// title and window. It is per release, not per indexer, so falling back to
	// another release from the same indexer stays possible; TitleLimit still
	// caps the total.
	ReleaseLimit    = 1
	maxNZBSize      = 50 << 20
	maxCacheSize    = 64 << 20
	maxCacheEntries = 256
)

// BudgetStore atomically reserves an attempt across all profiles. A nonzero
// retry time means denied; storage errors must never fall back to a direct grab.
type BudgetStore interface {
	ReserveNZB(context.Context, string, string, time.Time) (time.Time, error)
}

type BudgetError struct{ RetryAt time.Time }

func (e *BudgetError) Error() string {
	return "NZB title download budget unavailable until " + e.RetryAt.UTC().Format(time.RFC3339)
}

type payload struct {
	data   []byte
	header http.Header
	at     time.Time
}

type Fetcher struct {
	store      BudgetStore
	group      singleflight.Group
	mu         sync.Mutex
	cache      map[string]payload
	cacheBytes int
}

// New is configured once at startup. A nil store disables title budgets and
// caching; the existing providerbreaker still protects every acquisition path.
func New(store BudgetStore) *Fetcher {
	return &Fetcher{store: store, cache: make(map[string]payload)}
}

var Shared = New(nil)

func (f *Fetcher) Fetch(ctx context.Context, client *http.Client, breaker *providerbreaker.Breaker, downloadURL string, candidate models.NZBResult, userAgent string) ([]byte, http.Header, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if breaker == nil {
		breaker = providerbreaker.Shared()
	}
	provider := strings.ToLower(strings.TrimSpace(candidate.Indexer))
	if f.store == nil {
		p, err := f.download(ctx, client, breaker, downloadURL, provider, "", userAgent, "")
		return p.data, p.header, err
	}
	if provider == "" {
		return nil, nil, fmt.Errorf("NZB budget requires an indexer name")
	}
	// Never keep credentials from a download URL in cache keys or logs.
	release := fmt.Sprintf("%s|%x", provider, sha256.Sum256([]byte(downloadURL)))
	titleKey, err := candidate.NZBIdentity().Key()
	if err != nil {
		// No usable catalog identity (a query without SxxEyy, a movie known
		// only by IMDb ID): budget the release itself rather than refusing
		// the grab outright.
		titleKey = "release:" + release
	}
	// The cache is per release, not per title: a season pack grabbed for one
	// episode serves the next without another grab.
	key := release
	if p, ok := f.cached(key); ok {
		return append([]byte(nil), p.data...), p.header.Clone(), nil
	}
	ch := f.group.DoChan(key, func() (any, error) {
		if p, ok := f.cached(key); ok {
			return p, nil
		}
		// Each waiter can cancel independently; the shared grab has its own bound.
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		p, err := f.download(callCtx, client, breaker, downloadURL, provider, release, userAgent, titleKey)
		if err == nil {
			f.remember(key, p)
		}
		return p, err
	})
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case result := <-ch:
		if result.Err != nil {
			return nil, nil, result.Err
		}
		p := result.Val.(payload)
		return append([]byte(nil), p.data...), p.header.Clone(), nil
	}
}

func (f *Fetcher) cached(key string) (payload, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.cache[key]
	if ok && time.Since(p.at) >= Window {
		delete(f.cache, key)
		f.cacheBytes -= len(p.data)
		ok = false
	}
	return p, ok
}

func (f *Fetcher) remember(key string, p payload) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if old, ok := f.cache[key]; ok {
		delete(f.cache, key)
		f.cacheBytes -= len(old.data)
	}
	for k, old := range f.cache {
		if time.Since(old.at) >= Window {
			delete(f.cache, k)
			f.cacheBytes -= len(old.data)
		}
	}
	// Then evict oldest first until the new entry fits.
	for len(f.cache) > 0 && (f.cacheBytes+len(p.data) > maxCacheSize || len(f.cache) >= maxCacheEntries) {
		var oldestKey string
		var oldest time.Time
		for k, old := range f.cache {
			if oldestKey == "" || old.at.Before(oldest) {
				oldestKey, oldest = k, old.at
			}
		}
		f.cacheBytes -= len(f.cache[oldestKey].data)
		delete(f.cache, oldestKey)
	}
	f.cache[key] = p
	f.cacheBytes += len(p.data)
}

// redactURL drops credentials and the query string (where indexer API keys
// live) so a download URL can appear in errors and logs.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid URL>"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func (f *Fetcher) download(ctx context.Context, client *http.Client, breaker *providerbreaker.Breaker, downloadURL, provider, release, userAgent, titleKey string) (payload, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil || (req.URL.Scheme != "http" && req.URL.Scheme != "https") || req.URL.Host == "" {
		return payload{}, fmt.Errorf("invalid NZB download URL")
	}
	allowed, until, probe := breaker.Allow(provider)
	if !allowed {
		return payload{}, fmt.Errorf("indexer %s is cooling down after rate limiting until %s", provider, until.UTC().Format(time.RFC3339))
	}
	defer breaker.ReleaseProbe(provider, probe)
	if f.store != nil {
		retryAt, err := f.store.ReserveNZB(ctx, titleKey, release, time.Now())
		if err != nil {
			return payload{}, fmt.Errorf("reserve NZB title budget: %w", err)
		}
		if !retryAt.IsZero() {
			return payload{}, &BudgetError{RetryAt: retryAt}
		}
	}
	httpheaders.SetNZBDownloadHeaders(req, userAgent)
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return payload{}, ctx.Err()
		}
		// The client error can quote the full URL (API key included).
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return payload{}, fmt.Errorf("NZB download from %s (%s) failed: %w", provider, redactURL(downloadURL), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		hint := providerbreaker.RetryHint(resp.Header, time.Now())
		if resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode == http.StatusServiceUnavailable && hint > 0) {
			breaker.RecordRateLimit(provider, hint)
		}
		// Indexers explain failures (expired API key, grab limit) in the body.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		msg := strings.TrimSpace(string(snippet))
		if msg != "" {
			msg = ": " + msg
		}
		return payload{}, fmt.Errorf("NZB download from %s (%s) returned HTTP %d%s", provider, redactURL(downloadURL), resp.StatusCode, msg)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxNZBSize+1))
	if err != nil {
		return payload{}, fmt.Errorf("read NZB body: %w", err)
	}
	if len(data) == 0 || len(data) > maxNZBSize {
		return payload{}, fmt.Errorf("NZB response is empty or exceeds 50 MiB")
	}
	breaker.RecordSuccess(provider, probe)
	return payload{data: data, header: resp.Header.Clone(), at: time.Now()}, nil
}
