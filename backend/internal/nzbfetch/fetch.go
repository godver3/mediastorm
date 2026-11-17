// Package nzbfetch is the single HTTP acquisition point for playback, Usenet
// availability checks and track probes.
package nzbfetch

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"novastream/internal/httpheaders"
	"novastream/internal/providerbreaker"
	"novastream/models"
)

const (
	Window          = 10 * time.Minute
	Spacing         = 30 * time.Second
	TitleLimit      = 3
	IndexerLimit    = 1
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
		p, err := f.download(ctx, client, breaker, downloadURL, provider, userAgent, "")
		return p.data, p.header, err
	}
	titleKey, err := candidate.NZBIdentity().Key()
	if err != nil {
		return nil, nil, err
	}
	if provider == "" {
		return nil, nil, fmt.Errorf("NZB budget requires an indexer name")
	}
	// Never keep credentials from a download URL in cache keys or logs.
	key := fmt.Sprintf("%s\x00%s\x00%x", titleKey, provider, sha256.Sum256([]byte(downloadURL)))
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
		p, err := f.download(callCtx, client, breaker, downloadURL, provider, userAgent, titleKey)
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
	for k, old := range f.cache {
		if time.Since(old.at) >= Window || f.cacheBytes+len(p.data) > maxCacheSize || len(f.cache) >= maxCacheEntries {
			delete(f.cache, k)
			f.cacheBytes -= len(old.data)
		}
	}
	f.cache[key] = p
	f.cacheBytes += len(p.data)
}

func (f *Fetcher) download(ctx context.Context, client *http.Client, breaker *providerbreaker.Breaker, downloadURL, provider, userAgent, titleKey string) (payload, error) {
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
		retryAt, err := f.store.ReserveNZB(ctx, titleKey, provider, time.Now())
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
		return payload{}, fmt.Errorf("NZB download request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		hint := providerbreaker.RetryHint(resp.Header, time.Now())
		if resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode == http.StatusServiceUnavailable && hint > 0) {
			breaker.RecordRateLimit(provider, hint)
		}
		return payload{}, fmt.Errorf("NZB download returned HTTP %d", resp.StatusCode)
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
