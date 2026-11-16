package debrid

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

const (
	defaultResolutionAPITimeout  = 12 * time.Second
	defaultResolutionCooldown    = 30 * time.Second
	defaultResolutionMaxCooldown = 5 * time.Minute

	// An unusable account (expired premium, revoked key, ban) will not recover
	// in seconds, and every retry is another rejected request on that account.
	defaultAccountDeniedCooldown    = 15 * time.Minute
	defaultAccountDeniedMaxCooldown = 6 * time.Hour
)

type resolutionCircuitState struct {
	failures  int
	openUntil time.Time
	probing   bool
}

// providerResolutionCircuit prevents a provider-wide API outage from making
// every candidate resolution wait for the same timeout. Streaming uses raw
// provider clients and is deliberately outside this circuit.
type providerResolutionCircuit struct {
	mu          sync.Mutex
	states      map[string]*resolutionCircuitState
	now         func() time.Time
	apiTimeout  time.Duration
	baseBackoff time.Duration
	maxBackoff  time.Duration

	accountBaseBackoff time.Duration
	accountMaxBackoff  time.Duration
}

type ResolutionCircuitOpenError struct {
	Provider   string
	RetryAfter time.Duration
}

func (e *ResolutionCircuitOpenError) Error() string {
	if e == nil {
		return ""
	}
	retryAfter := e.RetryAfter.Round(time.Second)
	if retryAfter < time.Second {
		retryAfter = time.Second
	}
	return fmt.Sprintf("%s resolution temporarily unavailable; retry in %s", e.Provider, retryAfter)
}

func newProviderResolutionCircuit() *providerResolutionCircuit {
	return &providerResolutionCircuit{
		states:      make(map[string]*resolutionCircuitState),
		now:         time.Now,
		apiTimeout:  defaultResolutionAPITimeout,
		baseBackoff: defaultResolutionCooldown,
		maxBackoff:  defaultResolutionMaxCooldown,

		accountBaseBackoff: defaultAccountDeniedCooldown,
		accountMaxBackoff:  defaultAccountDeniedMaxCooldown,
	}
}

func (c *providerResolutionCircuit) wrap(client Provider) Provider {
	if c == nil || client == nil {
		return client
	}
	return &resolutionCircuitProvider{Provider: client, circuit: c}
}

func (c *providerResolutionCircuit) before(provider string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	now := c.now()

	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.states[provider]
	if state == nil || state.failures == 0 {
		return nil
	}
	if now.Before(state.openUntil) {
		return &ResolutionCircuitOpenError{Provider: provider, RetryAfter: state.openUntil.Sub(now)}
	}
	if state.probing {
		return &ResolutionCircuitOpenError{Provider: provider, RetryAfter: time.Second}
	}
	state.probing = true
	log.Printf("[debrid-circuit] allowing recovery probe for %s after cooldown", provider)
	return nil
}

func (c *providerResolutionCircuit) after(provider, operation string, callErr error, parentErr error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if callErr != nil && parentErr != nil {
		// The caller went away; this says nothing about provider health.
		c.releaseProbe(provider)
		return
	}
	if callErr != nil && IsProviderAccountDeniedError(callErr) {
		c.open(provider, operation, callErr, c.accountBaseBackoff, c.accountMaxBackoff)
		return
	}
	if callErr != nil && IsProviderAPIUnavailableError(callErr) {
		c.open(provider, operation, callErr, c.baseBackoff, c.maxBackoff)
		return
	}
	c.close(provider)
}

func (c *providerResolutionCircuit) releaseProbe(provider string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if state := c.states[provider]; state != nil {
		state.probing = false
	}
}

func (c *providerResolutionCircuit) open(provider, operation string, err error, baseBackoff, maxBackoff time.Duration) {
	now := c.now()
	c.mu.Lock()
	state := c.states[provider]
	if state == nil {
		state = &resolutionCircuitState{}
		c.states[provider] = state
	}
	// Several resolutions may already be in flight when the first timeout opens
	// the circuit. Their later failures describe the same outage and must not
	// exponentially extend the cooldown. Only a failed half-open recovery probe
	// (after openUntil) advances the backoff.
	if now.Before(state.openUntil) {
		c.mu.Unlock()
		return
	}
	state.failures++
	backoff := baseBackoff
	for i := 1; i < state.failures && backoff < maxBackoff; i++ {
		backoff *= 2
	}
	if backoff > maxBackoff {
		backoff = maxBackoff
	}
	state.openUntil = now.Add(backoff)
	state.probing = false
	failures := state.failures
	c.mu.Unlock()
	log.Printf("[debrid-circuit] opened %s resolution circuit for %s after %s failure #%d: %v", provider, backoff, operation, failures, err)
}

func (c *providerResolutionCircuit) close(provider string) {
	c.mu.Lock()
	state := c.states[provider]
	wasOpen := state != nil && state.failures > 0
	delete(c.states, provider)
	c.mu.Unlock()
	if wasOpen {
		log.Printf("[debrid-circuit] %s resolution API recovered; circuit closed", provider)
	}
}

type resolutionCircuitProvider struct {
	Provider
	circuit *providerResolutionCircuit
}

func (p *resolutionCircuitProvider) callContext(ctx context.Context, operation string) (context.Context, context.CancelFunc, error) {
	if err := p.circuit.before(p.Name()); err != nil {
		return nil, nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, p.circuit.apiTimeout)
	return callCtx, cancel, nil
}

func (p *resolutionCircuitProvider) AddMagnet(ctx context.Context, magnetURL string) (*AddMagnetResult, error) {
	callCtx, cancel, err := p.callContext(ctx, "add magnet")
	if err != nil {
		return nil, err
	}
	defer cancel()
	result, err := p.Provider.AddMagnet(callCtx, magnetURL)
	p.circuit.after(p.Name(), "add magnet", err, ctx.Err())
	return result, err
}

func (p *resolutionCircuitProvider) AddTorrentFile(ctx context.Context, data []byte, filename string) (*AddMagnetResult, error) {
	callCtx, cancel, err := p.callContext(ctx, "add torrent")
	if err != nil {
		return nil, err
	}
	defer cancel()
	result, err := p.Provider.AddTorrentFile(callCtx, data, filename)
	p.circuit.after(p.Name(), "add torrent", err, ctx.Err())
	return result, err
}

func (p *resolutionCircuitProvider) GetTorrentInfo(ctx context.Context, torrentID string) (*TorrentInfo, error) {
	callCtx, cancel, err := p.callContext(ctx, "get torrent info")
	if err != nil {
		return nil, err
	}
	defer cancel()
	result, err := p.Provider.GetTorrentInfo(callCtx, torrentID)
	p.circuit.after(p.Name(), "get torrent info", err, ctx.Err())
	return result, err
}

func (p *resolutionCircuitProvider) SelectFiles(ctx context.Context, torrentID, fileIDs string) error {
	callCtx, cancel, err := p.callContext(ctx, "select files")
	if err != nil {
		return err
	}
	defer cancel()
	err = p.Provider.SelectFiles(callCtx, torrentID, fileIDs)
	p.circuit.after(p.Name(), "select files", err, ctx.Err())
	return err
}

func (p *resolutionCircuitProvider) DeleteTorrent(ctx context.Context, torrentID string) error {
	callCtx, cancel, err := p.callContext(ctx, "delete torrent")
	if err != nil {
		return err
	}
	defer cancel()
	err = p.Provider.DeleteTorrent(callCtx, torrentID)
	p.circuit.after(p.Name(), "delete torrent", err, ctx.Err())
	return err
}

func (p *resolutionCircuitProvider) UnrestrictLink(ctx context.Context, link string) (*UnrestrictResult, error) {
	callCtx, cancel, err := p.callContext(ctx, "unrestrict link")
	if err != nil {
		return nil, err
	}
	defer cancel()
	result, err := p.Provider.UnrestrictLink(callCtx, link)
	p.circuit.after(p.Name(), "unrestrict link", err, ctx.Err())
	return result, err
}

func (p *resolutionCircuitProvider) CheckInstantAvailability(ctx context.Context, infoHash string) (bool, error) {
	callCtx, cancel, err := p.callContext(ctx, "instant availability")
	if err != nil {
		return false, err
	}
	defer cancel()
	result, err := p.Provider.CheckInstantAvailability(callCtx, infoHash)
	p.circuit.after(p.Name(), "instant availability", err, ctx.Err())
	return result, err
}
