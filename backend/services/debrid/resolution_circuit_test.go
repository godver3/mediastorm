package debrid

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

type circuitTestProvider struct {
	mu    sync.Mutex
	calls int
	err   error
	wait  bool
}

func (p *circuitTestProvider) Name() string { return "torbox" }

func (p *circuitTestProvider) result(ctx context.Context) error {
	p.mu.Lock()
	p.calls++
	err := p.err
	wait := p.wait
	p.mu.Unlock()
	if wait {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (p *circuitTestProvider) AddMagnet(ctx context.Context, _ string) (*AddMagnetResult, error) {
	if err := p.result(ctx); err != nil {
		return nil, err
	}
	return &AddMagnetResult{ID: "1"}, nil
}

func (p *circuitTestProvider) AddTorrentFile(ctx context.Context, _ []byte, _ string) (*AddMagnetResult, error) {
	return p.AddMagnet(ctx, "")
}

func (p *circuitTestProvider) GetTorrentInfo(ctx context.Context, _ string) (*TorrentInfo, error) {
	if err := p.result(ctx); err != nil {
		return nil, err
	}
	return &TorrentInfo{Status: "downloaded"}, nil
}

func (p *circuitTestProvider) SelectFiles(ctx context.Context, _, _ string) error {
	return p.result(ctx)
}

func (p *circuitTestProvider) DeleteTorrent(ctx context.Context, _ string) error {
	return p.result(ctx)
}

func (p *circuitTestProvider) UnrestrictLink(ctx context.Context, _ string) (*UnrestrictResult, error) {
	if err := p.result(ctx); err != nil {
		return nil, err
	}
	return &UnrestrictResult{DownloadURL: "https://example.invalid/file"}, nil
}

func (p *circuitTestProvider) CheckInstantAvailability(ctx context.Context, _ string) (bool, error) {
	if err := p.result(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (p *circuitTestProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestResolutionCircuitFailsFastAndRecovers(t *testing.T) {
	now := time.Date(2026, 7, 17, 18, 0, 0, 0, time.UTC)
	circuit := newProviderResolutionCircuit()
	circuit.now = func() time.Time { return now }
	provider := &circuitTestProvider{err: &ProviderError{
		Provider:   "torbox",
		Operation:  "torrent info",
		StatusCode: http.StatusServiceUnavailable,
	}}
	wrapped := circuit.wrap(provider)

	if _, err := wrapped.GetTorrentInfo(context.Background(), "1"); err == nil {
		t.Fatal("first provider failure returned nil error")
	}
	provider.mu.Lock()
	provider.err = nil
	provider.mu.Unlock()

	if _, err := wrapped.GetTorrentInfo(context.Background(), "1"); err == nil {
		t.Fatal("open circuit returned nil error")
	} else {
		var openErr *ResolutionCircuitOpenError
		if !errors.As(err, &openErr) {
			t.Fatalf("open circuit error = %T, want ResolutionCircuitOpenError", err)
		}
	}
	if got := provider.callCount(); got != 1 {
		t.Fatalf("provider calls while circuit open = %d, want 1", got)
	}

	now = now.Add(defaultResolutionCooldown + time.Second)
	if _, err := wrapped.GetTorrentInfo(context.Background(), "1"); err != nil {
		t.Fatalf("half-open recovery probe failed: %v", err)
	}
	if _, err := wrapped.GetTorrentInfo(context.Background(), "1"); err != nil {
		t.Fatalf("closed circuit request failed: %v", err)
	}
	if got := provider.callCount(); got != 3 {
		t.Fatalf("provider calls after recovery = %d, want 3", got)
	}
}

func TestResolutionCircuitTimesOutProviderAPI(t *testing.T) {
	circuit := newProviderResolutionCircuit()
	circuit.apiTimeout = 20 * time.Millisecond
	provider := &circuitTestProvider{wait: true}
	wrapped := circuit.wrap(provider)

	started := time.Now()
	if _, err := wrapped.GetTorrentInfo(context.Background(), "1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("provider timeout took %v, want under 1s", elapsed)
	}
	if _, err := wrapped.GetTorrentInfo(context.Background(), "1"); err == nil {
		t.Fatal("second call should fail fast while circuit is open")
	}
	if got := provider.callCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
}

func TestResolutionCircuitDoesNotAmplifyConcurrentFailures(t *testing.T) {
	now := time.Date(2026, 7, 17, 18, 0, 0, 0, time.UTC)
	circuit := newProviderResolutionCircuit()
	circuit.now = func() time.Time { return now }
	err := &ProviderError{StatusCode: http.StatusServiceUnavailable}

	circuit.open("torbox", "first request", err, circuit.baseBackoff, circuit.maxBackoff)
	circuit.open("torbox", "already in flight request", err, circuit.baseBackoff, circuit.maxBackoff)

	circuit.mu.Lock()
	state := *circuit.states["torbox"]
	circuit.mu.Unlock()
	if state.failures != 1 {
		t.Fatalf("failure count = %d, want 1", state.failures)
	}
	if got := state.openUntil.Sub(now); got != defaultResolutionCooldown {
		t.Fatalf("cooldown = %v, want %v", got, defaultResolutionCooldown)
	}
}

func TestResolutionCircuitDoesNotOpenForItemFailure(t *testing.T) {
	circuit := newProviderResolutionCircuit()
	provider := &circuitTestProvider{err: errors.New("torrent not cached")}
	wrapped := circuit.wrap(provider)

	for i := 0; i < 2; i++ {
		if _, err := wrapped.GetTorrentInfo(context.Background(), "1"); err == nil {
			t.Fatal("item failure returned nil error")
		}
	}
	if got := provider.callCount(); got != 2 {
		t.Fatalf("provider calls = %d, want 2", got)
	}
}

func TestProviderAPIUnavailableClassification(t *testing.T) {
	if !IsProviderAPIUnavailableError(context.DeadlineExceeded) {
		t.Fatal("deadline exceeded should be provider-unavailable")
	}
	if !IsProviderAPIUnavailableError(&ProviderError{StatusCode: http.StatusTooManyRequests}) {
		t.Fatal("HTTP 429 should be provider-unavailable")
	}
	if IsProviderAPIUnavailableError(errors.New("torbox authentication failed: invalid API key")) {
		t.Fatal("authentication failure should not open a temporary circuit")
	}
	if IsProviderAPIUnavailableError(errors.New("torrent not cached")) {
		t.Fatal("item-specific cache miss should not open provider circuit")
	}
}

func TestResolutionCircuitHoldsOffDeniedAccountForLongCooldown(t *testing.T) {
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	circuit := newProviderResolutionCircuit()
	circuit.now = func() time.Time { return now }
	// Real-Debrid's response once premium lapses (seen 3k+ times in a day).
	provider := &circuitTestProvider{err: errors.New(`add magnet failed with status 403: {"error": "permission_denied", "error_code": 9}`)}
	wrapped := circuit.wrap(provider)

	if _, err := wrapped.AddMagnet(context.Background(), "magnet:?xt=urn:btih:abc"); err == nil {
		t.Fatal("denied account returned nil error")
	}
	// Well past the transient cooldown, still held off.
	now = now.Add(defaultResolutionMaxCooldown + time.Minute)
	if _, err := wrapped.AddMagnet(context.Background(), "magnet:?xt=urn:btih:def"); err == nil {
		t.Fatal("open circuit returned nil error")
	} else {
		var openErr *ResolutionCircuitOpenError
		if !errors.As(err, &openErr) {
			t.Fatalf("error = %T, want ResolutionCircuitOpenError", err)
		}
	}
	if got := provider.callCount(); got != 1 {
		t.Fatalf("provider calls while account denied = %d, want 1", got)
	}

	// After the account cooldown one probe is allowed; a second denial doubles it.
	now = now.Add(defaultAccountDeniedCooldown)
	if _, err := wrapped.AddMagnet(context.Background(), "magnet:?xt=urn:btih:ghi"); err == nil {
		t.Fatal("probe against denied account returned nil error")
	}
	now = now.Add(defaultAccountDeniedCooldown + time.Minute)
	if _, err := wrapped.AddMagnet(context.Background(), "magnet:?xt=urn:btih:jkl"); err == nil {
		t.Fatal("expected circuit still open after second denial")
	}
	if got := provider.callCount(); got != 2 {
		t.Fatalf("provider calls = %d, want 2 (initial + one probe)", got)
	}
}

func TestProviderAccountDeniedClassification(t *testing.T) {
	denied := []error{
		errors.New(`add magnet failed with status 403: {"error": "permission_denied", "error_code": 9}`),
		errors.New("alldebrid authentication failed: invalid API key"),
		errors.New("add magnet failed: This account is banned (AUTH_USER_BANNED)"),
		errors.New("unrestrict failed: Premium required (MUST_BE_PREMIUM)"),
		&ProviderError{Provider: "torbox", StatusCode: http.StatusUnauthorized},
	}
	for _, err := range denied {
		if !IsProviderAccountDeniedError(err) {
			t.Errorf("expected account-denied: %v", err)
		}
	}
	notDenied := []error{
		errors.New("torrent not cached"),
		&ProviderError{Provider: "torbox", StatusCode: http.StatusServiceUnavailable},
		context.Canceled,
		&RecentlyUncachedError{Provider: "alldebrid", InfoHash: "abc"},
	}
	for _, err := range notDenied {
		if IsProviderAccountDeniedError(err) {
			t.Errorf("unexpected account-denied: %v", err)
		}
	}
}

func TestAllDebridErrorTextKeepsCode(t *testing.T) {
	if got := allDebridErrorText("AUTH_USER_BANNED", "This account is banned"); got != "This account is banned (AUTH_USER_BANNED)" {
		t.Fatalf("got %q", got)
	}
	if got := allDebridErrorText("", "msg"); got != "msg" {
		t.Fatalf("got %q", got)
	}
}
