package usenet

// Verification tests for the NNTP provider circuit breaker + failover: a slow
// or failing provider must (a) fail fast — bounded by the per-provider pass
// timeout, not the outer deadline — (b) fall over to the next provider instead
// of aborting the whole health check, and (c) be short-circuited for a cooldown
// window on the immediately following call, with exactly one half-open recovery
// probe after the cooldown. A provider outage must never masquerade as a
// "segments missing from all providers" verdict (that would reject a healthy
// release), and a caller deadline must never open a circuit.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"novastream/config"
)

// blockingClient simulates a provider that accepts a connection but never
// answers: every article check blocks until its context (the per-provider pass
// timeout or an outer deadline) fires.
type blockingClient struct{}

func (blockingClient) CheckArticle(ctx context.Context, messageID string) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

func (blockingClient) Close() error { return nil }

// countingDialer wraps a dialer function and counts dials per provider host.
type countingDialer struct {
	mu    sync.Mutex
	dials map[string]int
	dial  func(ctx context.Context, settings config.UsenetSettings) (statClient, error)
}

func (d *countingDialer) dialFn(ctx context.Context, settings config.UsenetSettings) (statClient, error) {
	d.mu.Lock()
	d.dials[settings.Host]++
	dial := d.dial
	d.mu.Unlock()
	return dial(ctx, settings)
}

func (d *countingDialer) dialCount(host string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dials[host]
}

func newCircuitTestService(timeout, cooldown time.Duration) (*Service, *countingDialer) {
	svc := NewService(nil, nil)
	svc.breaker.timeout = timeout
	svc.breaker.baseBackoff = cooldown
	svc.breaker.maxBackoff = time.Hour
	return svc, &countingDialer{dials: make(map[string]int)}
}

func healthyStubClients() map[string]bool {
	return map[string]bool{
		"<a@example>": true,
		"<b@example>": true,
		"<c@example>": true,
	}
}

func TestProviderCircuitFailsFastFallsOverAndShortCircuits(t *testing.T) {
	svc, dialer := newCircuitTestService(300*time.Millisecond, 5*time.Second)
	dialer.dial = func(ctx context.Context, settings config.UsenetSettings) (statClient, error) {
		switch settings.Host {
		case "slow.example":
			return blockingClient{}, nil
		case "fast.example":
			return &stubClient{results: healthyStubClients()}, nil
		default:
			return nil, fmt.Errorf("unexpected host %s", settings.Host)
		}
	}
	svc.dialer = dialer.dialFn

	providers := []config.UsenetSettings{
		{Name: "Slow", Host: "slow.example", Enabled: true, Connections: 1},
		{Name: "Fast", Host: "fast.example", Enabled: true, Connections: 1},
	}
	segments := []string{"<a@example>", "<b@example>", "<c@example>"}

	// First call: the slow provider hangs past its pass timeout. The check must
	// fail fast (bounded by the ~300ms timeout, not the hang) and complete
	// healthy via the fast provider.
	start := time.Now()
	missing, err := svc.checkSegmentsConcurrently(context.Background(), segments, providers)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("call 1 returned error: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("call 1 missing = %v, want none", missing)
	}
	if elapsed < 200*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("call 1 took %v; want ~the 300ms provider pass timeout (fail fast, no hang)", elapsed)
	}
	if got := dialer.dialCount("slow.example"); got != 1 {
		t.Fatalf("slow.example dials after call 1 = %d, want 1", got)
	}

	// Immediately following call: the slow provider must be short-circuited
	// (cooldown in effect) — no dial at all — and the check still succeeds.
	missing, err = svc.checkSegmentsConcurrently(context.Background(), segments, providers)
	if err != nil {
		t.Fatalf("call 2 returned error: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("call 2 missing = %v, want none", missing)
	}
	if got := dialer.dialCount("slow.example"); got != 1 {
		t.Fatalf("slow.example dials after call 2 = %d, want 1 (short-circuited)", got)
	}
	if got := dialer.dialCount("fast.example"); got != 2 {
		t.Fatalf("fast.example dials after call 2 = %d, want 2", got)
	}

	// Advance past the cooldown: the next call allows exactly one half-open
	// recovery probe against the slow provider. It is still hung, so the probe
	// fails and the circuit re-opens with a longer backoff.
	svc.breaker.now = func() time.Time { return time.Now().Add(time.Hour) }
	missing, err = svc.checkSegmentsConcurrently(context.Background(), segments, providers)
	if err != nil {
		t.Fatalf("call 3 (recovery probe) returned error: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("call 3 missing = %v, want none", missing)
	}
	if got := dialer.dialCount("slow.example"); got != 2 {
		t.Fatalf("slow.example dials after call 3 = %d, want 2 (one recovery probe)", got)
	}

	// The failed probe doubled the backoff, so the slow provider is skipped
	// again… until the clock moves past the extended cooldown, at which point a
	// probe succeeds and closes the circuit.
	svc.breaker.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	dialer.dial = func(ctx context.Context, settings config.UsenetSettings) (statClient, error) {
		return &stubClient{results: healthyStubClients()}, nil
	}
	missing, err = svc.checkSegmentsConcurrently(context.Background(), segments, providers)
	if err != nil {
		t.Fatalf("call 4 (closing probe) returned error: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("call 4 missing = %v, want none", missing)
	}
	if got := dialer.dialCount("slow.example"); got != 3 {
		t.Fatalf("slow.example dials after call 4 = %d, want 3 (probe success)", got)
	}

	// Circuit closed: the next call dials the recovered provider without a
	// probe flag — the breaker is fully transparent again.
	missing, err = svc.checkSegmentsConcurrently(context.Background(), segments, providers)
	if err != nil {
		t.Fatalf("call 5 returned error: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("call 5 missing = %v, want none", missing)
	}
	if got := dialer.dialCount("slow.example"); got != 4 {
		t.Fatalf("slow.example dials after call 5 = %d, want 4 (circuit closed)", got)
	}
}

func TestAllProvidersInCooldownIsNotAMissingVerdict(t *testing.T) {
	// The cooldown must outlive the whole call: provider "One" fails ~300ms
	// into call 1 and "Two" ~600ms in, so a sub-second cooldown would have
	// expired by the time call 2 runs (and "One" would legitimately get a
	// recovery probe instead of a skip).
	svc, dialer := newCircuitTestService(300*time.Millisecond, 10*time.Second)
	dialer.dial = func(ctx context.Context, settings config.UsenetSettings) (statClient, error) {
		return blockingClient{}, nil
	}
	svc.dialer = dialer.dialFn

	providers := []config.UsenetSettings{
		{Name: "One", Host: "one.example", Enabled: true, Connections: 1},
		{Name: "Two", Host: "two.example", Enabled: true, Connections: 1},
	}
	segments := []string{"<a@example>"}

	// Both providers fail (pass timeout) → both circuits open. The check must
	// surface the outage as an error, not a verdict.
	if _, err := svc.checkSegmentsConcurrently(context.Background(), segments, providers); err == nil {
		t.Fatal("call 1: expected an error when every provider fails")
	}

	// Both providers are now in cooldown: the check must fail fast with an
	// explicit error and NEVER return "missing from all providers" — that
	// verdict would reject a release whose segments live on a provider that is
	// merely in a cooldown after an outage.
	missing, err := svc.checkSegmentsConcurrently(context.Background(), segments, providers)
	if err == nil || !strings.Contains(err.Error(), "cooldown") {
		t.Fatalf("call 2: err = %v, want an all-providers-in-cooldown error", err)
	}
	if missing != nil {
		t.Fatalf("call 2: missing = %v, want nil (cooldown is inconclusive, not a missing verdict)", missing)
	}
	if got := dialer.dialCount("one.example"); got != 1 {
		t.Fatalf("one.example dials = %d, want 1 (short-circuited on call 2)", got)
	}
	if got := dialer.dialCount("two.example"); got != 1 {
		t.Fatalf("two.example dials = %d, want 1 (short-circuited on call 2)", got)
	}
}

func TestParentCancellationDoesNotOpenCircuit(t *testing.T) {
	svc, dialer := newCircuitTestService(time.Hour, time.Hour) // no per-provider cap; the outer deadline decides
	dialer.dial = func(ctx context.Context, settings config.UsenetSettings) (statClient, error) {
		return blockingClient{}, nil
	}
	svc.dialer = dialer.dialFn

	providers := []config.UsenetSettings{
		{Name: "Slow", Host: "slow.example", Enabled: true, Connections: 1},
	}
	segments := []string{"<a@example>"}

	run := func(timeout time.Duration) error {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		_, err := svc.checkSegmentsConcurrently(ctx, segments, providers)
		return err
	}

	// A caller deadline firing during the pass yields an error — but it says
	// nothing about provider health, so the circuit must NOT open.
	if err := run(150 * time.Millisecond); err == nil {
		t.Fatal("expected an error when the outer context expires during the pass")
	}

	// The immediately following call must dial the provider for real (no
	// short-circuit): the failure was the caller's, not the provider's.
	if err := run(150 * time.Millisecond); err == nil {
		t.Fatal("expected an error (provider still hangs, dialed afresh)")
	}
	if got := dialer.dialCount("slow.example"); got != 2 {
		t.Fatalf("slow.example dials = %d, want 2 (no circuit was opened by a caller deadline)", got)
	}
}

// TestMissingWithUnavailableProviderIsFailOpen: when one provider answers
// "missing" but another is unavailable (errored or in cooldown), the check
// must fail open instead of emitting a definitive missing verdict — the
// segments may live on the provider that could not be consulted.
func TestMissingWithUnavailableProviderIsFailOpen(t *testing.T) {
	svc, dialer := newCircuitTestService(200*time.Millisecond, time.Hour)
	dialer.dial = func(ctx context.Context, settings config.UsenetSettings) (statClient, error) {
		switch settings.Host {
		case "missing.example":
			return &stubClient{results: map[string]bool{"<a@example>": false}}, nil
		case "broken.example":
			return blockingClient{}, nil
		default:
			return nil, fmt.Errorf("unexpected host %s", settings.Host)
		}
	}
	svc.dialer = dialer.dialFn

	providers := []config.UsenetSettings{
		{Name: "Missing", Host: "missing.example", Enabled: true, Connections: 1},
		{Name: "Broken", Host: "broken.example", Enabled: true, Connections: 1},
	}
	segments := []string{"<a@example>"}

	missing, err := svc.checkSegmentsConcurrently(context.Background(), segments, providers)
	if err == nil {
		t.Fatal("expected an error, not a missing-segments verdict, when an unavailable provider was not consulted")
	}
	if missing != nil {
		t.Fatalf("missing = %v, want nil (fail open, no verdict)", missing)
	}
	if !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("err = %v, want an incomplete-check error", err)
	}
}

func TestNNTPCircuitBreakerBackoffAndHalfOpenProbe(t *testing.T) {
	b := newNNTPCircuitBreaker()
	b.baseBackoff = 30 * time.Second
	b.maxBackoff = 5 * time.Minute
	now := time.Unix(1_700_000_000, 0)
	b.now = func() time.Time { return now }

	key := "slow.example"

	if allowed, probe, _ := b.allow(key); !allowed || probe {
		t.Fatalf("first allow = (%t, %t), want (true, false)", allowed, probe)
	}

	b.recordFailure(key)
	for i := 0; i < 3; i++ {
		now = now.Add(9 * time.Second) // +9, +18, +27s — inside the 30s cooldown
		if allowed, _, _ := b.allow(key); allowed {
			t.Fatalf("allow at +%ds must be denied inside the cooldown", (i+1)*9)
		}
	}

	// Failures from in-flight passes during an open circuit describe the same
	// outage and must not extend the cooldown.
	b.recordFailure(key)
	b.recordFailure(key)
	if allowed, _, _ := b.allow(key); allowed {
		t.Fatal("allow must still be denied inside the unchanged cooldown (no extension by in-flight failures)")
	}

	now = now.Add(5 * time.Second) // +32s: cooldown expired
	allowed, probe, _ := b.allow(key)
	if !allowed || !probe {
		t.Fatalf("first post-cooldown allow = (%t, %t), want (true, true) half-open probe", allowed, probe)
	}
	// A concurrent caller must be held back while the probe is in flight.
	if allowed, probe, _ := b.allow(key); allowed {
		t.Fatalf("second allow while a probe is in flight = (%t, %t), want denied", allowed, probe)
	}

	// Failed probe → exponential re-open (30s → 60s).
	b.recordFailure(key)
	now = now.Add(31 * time.Second)
	if allowed, _, _ := b.allow(key); allowed {
		t.Fatal("allow after a failed probe must be denied for the doubled cooldown")
	}

	// Successful probe closes the circuit entirely.
	now = now.Add(40 * time.Second)
	allowed, probe, _ = b.allow(key)
	if !allowed || !probe {
		t.Fatalf("post-cooldown allow = (%t, %t), want (true, true) recovery probe", allowed, probe)
	}
	b.recordSuccess(key)
	allowed, probe, _ = b.allow(key)
	if !allowed || probe {
		t.Fatalf("allow after a closed circuit = (%t, %t), want (true, false)", allowed, probe)
	}
}

// TestDeadlineWithoutWorkerErrorSurfacesAsFailure pins the partial-verdict
// guard: a pass that ends because the deadline fired while every worker exited
// silently on ctx.Done must be an error, never a partial-coverage "missing"
// verdict (which the availability probe would turn into a false rejection).
func TestDeadlineWithoutWorkerErrorSurfacesAsFailure(t *testing.T) {
	svc, dialer := newCircuitTestService(200*time.Millisecond, time.Hour)
	// One segment, but a dialer that returns a client which never interacts:
	// the worker sits blocked on segmentCh with no command in flight when the
	// pass deadline fires.
	dialer.dial = func(ctx context.Context, settings config.UsenetSettings) (statClient, error) {
		return &stubClient{results: map[string]bool{"<a@example>": false}}, nil
	}
	svc.dialer = dialer.dialFn

	shortCtx, cancel := context.WithCancel(context.Background())
	cancel() // deadline style: the pass aborts before any segment is served
	_, err := svc.checkSegmentsOnProvider(shortCtx, []string{"<a@example>"}, config.UsenetSettings{
		Name: "Slow", Host: "slow.example", Enabled: true, Connections: 1,
	})
	if err == nil {
		t.Fatal("expected an error, not a partial-coverage missing verdict, when the pass dies without a worker error")
	}
}
