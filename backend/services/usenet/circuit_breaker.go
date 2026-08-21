package usenet

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"novastream/config"
)

const (
	// defaultNNTPProviderTimeout bounds a single provider pass (dial +
	// sampled article checks + retries) so a hung provider fails fast instead
	// of holding the whole health check hostage until the outer deadline.
	// An earlier outer deadline (e.g. the preflight probe budget) still wins.
	defaultNNTPProviderTimeout = 12 * time.Second
	// defaultNNTPProviderCooldown is the initial circuit-open window after a
	// provider failure; it doubles on each failed recovery probe.
	defaultNNTPProviderCooldown = 30 * time.Second
	// defaultNNTPProviderMaxCooldown caps the exponential backoff.
	defaultNNTPProviderMaxCooldown = 5 * time.Minute
)

type nntpCircuitState struct {
	failures  int
	openUntil time.Time
	probing   bool
}

// nntpCircuitBreaker is the NNTP analog of the debrid resolution circuit: a
// per-provider health gate that opens on failure (skipping the provider for an
// exponential cooldown), admits exactly one half-open recovery pass after the
// cooldown, and closes again on a successful recovery pass.
type nntpCircuitBreaker struct {
	mu          sync.Mutex
	states      map[string]*nntpCircuitState
	now         func() time.Time
	timeout     time.Duration
	baseBackoff time.Duration
	maxBackoff  time.Duration
}

func newNNTPCircuitBreaker() *nntpCircuitBreaker {
	return &nntpCircuitBreaker{
		states:      make(map[string]*nntpCircuitState),
		now:         time.Now,
		timeout:     defaultNNTPProviderTimeout,
		baseBackoff: defaultNNTPProviderCooldown,
		maxBackoff:  defaultNNTPProviderMaxCooldown,
	}
}

// nntpProviderKey identifies a provider across calls. The settings name is the
// operator's identity for the provider; host:port is the fallback.
func nntpProviderKey(provider config.UsenetSettings) string {
	if name := strings.TrimSpace(provider.Name); name != "" {
		return strings.ToLower(name)
	}
	return strings.ToLower(fmt.Sprintf("%s:%d", strings.TrimSpace(provider.Host), provider.Port))
}

// nntpProviderLabel is the human-readable identity used in logs.
func nntpProviderLabel(provider config.UsenetSettings) string {
	if name := strings.TrimSpace(provider.Name); name != "" {
		return name
	}
	return fmt.Sprintf("%s:%d", strings.TrimSpace(provider.Host), provider.Port)
}

// allow reports whether a provider pass may proceed. probe is true only for
// the single half-open recovery pass granted after a cooldown; concurrent
// callers are held back (retryIn == 0) until that probe settles.
func (b *nntpCircuitBreaker) allow(key string) (allowed bool, probe bool, retryIn time.Duration) {
	now := b.now()

	b.mu.Lock()
	defer b.mu.Unlock()

	state := b.states[key]
	if state == nil || state.failures == 0 {
		return true, false, 0
	}
	if now.Before(state.openUntil) {
		return false, false, state.openUntil.Sub(now)
	}
	if state.probing {
		return false, false, 0
	}
	state.probing = true
	log.Printf("[usenet-circuit] allowing recovery probe for %q after cooldown", key)
	return true, true, 0
}

// recordFailure opens (or re-opens) the provider's circuit. Failures that land
// while the circuit is already open describe the same outage and must not
// extend the cooldown — only a failed half-open recovery probe advances the
// backoff.
func (b *nntpCircuitBreaker) recordFailure(key string) {
	now := b.now()

	b.mu.Lock()
	state := b.states[key]
	if state == nil {
		state = &nntpCircuitState{}
		b.states[key] = state
	}
	if now.Before(state.openUntil) {
		b.mu.Unlock()
		return
	}
	state.failures++
	backoff := b.baseBackoff
	for i := 1; i < state.failures && backoff < b.maxBackoff; i++ {
		backoff *= 2
	}
	if backoff > b.maxBackoff {
		backoff = b.maxBackoff
	}
	state.openUntil = now.Add(backoff)
	state.probing = false
	failures, until := state.failures, state.openUntil
	b.mu.Unlock()
	log.Printf("[usenet-circuit] opened %q after failure #%d (cooldown %s until %s)", key, failures, backoff, until.Format(time.RFC3339))
}

// recordSuccess closes the circuit. Because allow() only admits passes to a
// provider with an open circuit as a half-open probe, any successful pass is
// proof the provider recovered.
func (b *nntpCircuitBreaker) recordSuccess(key string) {
	b.mu.Lock()
	_, wasOpen := b.states[key]
	delete(b.states, key)
	b.mu.Unlock()
	if wasOpen {
		log.Printf("[usenet-circuit] %q recovered; circuit closed", key)
	}
}

// releaseProbe aborts a granted recovery probe without recording a verdict —
// the caller went away (context cancelled), which says nothing about provider
// health.
func (b *nntpCircuitBreaker) releaseProbe(key string, probe bool) {
	if !probe {
		return
	}
	b.mu.Lock()
	if state := b.states[key]; state != nil {
		state.probing = false
	}
	b.mu.Unlock()
}
