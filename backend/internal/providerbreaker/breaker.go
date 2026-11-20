package providerbreaker

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultCooldown = time.Hour
	maximumCooldown = 24 * time.Hour
)

type Cooldown struct {
	Until    time.Time
	Failures int
}

type CooldownStore interface {
	LoadCooldowns(context.Context) (map[string]Cooldown, error)
	SaveCooldown(context.Context, string, Cooldown) error
	ClearCooldown(context.Context, string, time.Time) error
}

type state struct {
	until         time.Time
	failures      int
	probeInFlight bool
}

// Breaker prevents a rate-limited indexer from being retried for every query
// variant and every ranked result. A single request is allowed after the
// cooldown as a half-open probe.
type Breaker struct {
	mu     sync.Mutex
	states map[string]state
	store  CooldownStore
	// persist gates saving new pauses (the NZB budget setting); nil saves.
	persist func() bool
	now    func() time.Time
}

var shared = New()

func New() *Breaker {
	return &Breaker{
		states: make(map[string]state),
		now:    time.Now,
	}
}

// Shared returns the process-wide breaker used by indexer searches and NZB
// downloads so a 429 observed by either path protects both paths.
func Shared() *Breaker {
	return shared
}

// Restore is called once before serving requests. Refuse startup if persisted
// pauses cannot be loaded rather than silently discarding quota protection.
func (b *Breaker) Restore(ctx context.Context, store CooldownStore) error {
	states, err := store.LoadCooldowns(ctx)
	if err != nil {
		return fmt.Errorf("load indexer cooldowns: %w", err)
	}
	limit := b.now().Add(maximumCooldown)
	for key, c := range states {
		if c.Until.After(limit) {
			c.Until = limit
		}
		b.states[normalize(key)] = state{until: c.Until, failures: c.Failures}
	}
	b.store = store
	return nil
}

// SetPersist makes saving new pauses conditional; clearing saved ones always
// runs so a pause saved earlier can't outlive a later success.
func (b *Breaker) SetPersist(fn func() bool) {
	b.mu.Lock()
	b.persist = fn
	b.mu.Unlock()
}

func normalize(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

// Allow reports whether a provider request may proceed. probe is true when
// this is the sole half-open request allowed after an existing cooldown.
func (b *Breaker) Allow(provider string) (allowed bool, until time.Time, probe bool) {
	key := normalize(provider)
	if key == "" {
		return true, time.Time{}, false
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	current, ok := b.states[key]
	if !ok {
		return true, time.Time{}, false
	}
	now := b.now()
	if now.Before(current.until) {
		return false, current.until, false
	}
	if current.probeInFlight {
		return false, current.until, false
	}
	current.probeInFlight = true
	b.states[key] = current
	return true, current.until, true
}

// RecordRateLimit opens or extends the provider circuit. Retry hints are
// honored, but never shorten the exponential cooldown used when an indexer
// omits a reliable quota reset time.
func (b *Breaker) RecordRateLimit(provider string, retryHint time.Duration) time.Time {
	key := normalize(provider)
	if key == "" {
		return time.Time{}
	}

	b.mu.Lock()

	current := b.states[key]
	current.failures++
	delay := defaultCooldown
	for i := 1; i < current.failures && delay < maximumCooldown; i++ {
		delay *= 2
		if delay > maximumCooldown {
			delay = maximumCooldown
		}
	}
	if retryHint > delay {
		delay = retryHint
	}
	// Capped: search shares this breaker, and an indexer's huge Retry-After
	// must not switch it off for months (persisted pauses survive restarts).
	if delay > maximumCooldown {
		delay = maximumCooldown
	}
	until := b.now().Add(delay)
	if until.After(current.until) {
		current.until = until
	}
	current.probeInFlight = false
	b.states[key] = current
	persist := b.persist
	b.mu.Unlock()
	if b.store != nil && (persist == nil || persist()) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := b.store.SaveCooldown(ctx, key, Cooldown{Until: current.until, Failures: current.failures}); err != nil {
			log.Printf("[provider-breaker] persist cooldown failed: %v", err)
		}
	}
	return current.until
}

// RecordSuccess closes a half-open circuit after its probe succeeds. Ordinary
// concurrent successes do not erase a 429 recorded by another request.
func (b *Breaker) RecordSuccess(provider string, probe bool) {
	if !probe {
		return
	}
	key := normalize(provider)
	if key == "" {
		return
	}
	b.mu.Lock()
	current, ok := b.states[key]
	if !ok || !current.probeInFlight || b.now().Before(current.until) {
		b.mu.Unlock()
		return
	}
	delete(b.states, key)
	b.mu.Unlock()
	if b.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := b.store.ClearCooldown(ctx, key, current.until); err != nil {
			log.Printf("[provider-breaker] clear persisted cooldown failed: %v", err)
		}
	}
}

// ReleaseProbe allows another half-open probe after a non-rate-limit failure.
func (b *Breaker) ReleaseProbe(provider string, probe bool) {
	if !probe {
		return
	}
	key := normalize(provider)
	b.mu.Lock()
	if current, ok := b.states[key]; ok {
		current.probeInFlight = false
		b.states[key] = current
	}
	b.mu.Unlock()
}

// RetryHint extracts a Retry-After header. Bodies are deliberately not parsed:
// indexer HTML messages often describe a short anti-hammer delay even when the
// account has exhausted a longer rolling quota.
func RetryHint(header http.Header, now time.Time) time.Duration {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 && int64(seconds) <= int64((1<<63-1)/int64(time.Second)) {
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(value); err == nil && retryAt.After(now) {
		return retryAt.Sub(now)
	}
	return 0
}
