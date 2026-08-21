package playback

// Verification tests for the pre-download availability probe: it rejects
// releases whose sampled segments are missing from every provider BEFORE the
// expensive full download (ProcessNZBImmediatelyWithSource) completes, and stays
// fail-open — errors/timeouts/healthy verdicts all fall through to the full
// resolve so a healthy release can never be rejected by an inconclusive probe.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/importer"
	"novastream/internal/integration"
	"novastream/internal/pool"
	"novastream/models"
)

type stubAvailabilityChecker struct {
	result *models.NZBHealthCheck
	err    error
	calls  atomic.Int32
}

func (s *stubAvailabilityChecker) CheckHealthWithNZB(ctx context.Context, candidate models.NZBResult, nzbBytes []byte, fileName string) (*models.NZBHealthCheck, error) {
	s.calls.Add(1)
	return s.result, s.err
}

// deadlineProbeChecker wraps a stub checker and records whether the probe
// context carried a deadline plus how far out it was (the probe budget).
type deadlineProbeChecker struct {
	inner   *stubAvailabilityChecker
	mu      sync.Mutex
	budget  time.Duration
	hasDead bool
}

func (c *deadlineProbeChecker) CheckHealthWithNZB(ctx context.Context, candidate models.NZBResult, nzbBytes []byte, fileName string) (*models.NZBHealthCheck, error) {
	if d, ok := ctx.Deadline(); ok {
		c.mu.Lock()
		c.budget = time.Until(d)
		c.hasDead = true
		c.mu.Unlock()
	}
	return c.inner.CheckHealthWithNZB(ctx, candidate, nzbBytes, fileName)
}

func (c *deadlineProbeChecker) snapshot() (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.budget, c.hasDead
}

// newPreflightTestService builds a playback service whose importer service is
// stopped, so any call reaching ProcessNZBImmediatelyWithSource fails with the
// importer's own error — a distinguishable outcome from a probe rejection.
func newPreflightTestService(t *testing.T) (*Service, *integration.NzbSystem) {
	t.Helper()
	tempDir := t.TempDir()
	cfg := config.NewManager(filepath.Join(tempDir, "settings.json"))
	if err := cfg.Save(config.DefaultSettings()); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	adapter := config.NewConfigAdapter(cfg)
	poolManager := pool.NewManager()
	nzbCfg := integration.NzbConfig{
		QueueDatabasePath:   filepath.Join(tempDir, "queue.db"),
		MetadataRootPath:    filepath.Join(tempDir, "metadata"),
		Password:            "",
		Salt:                "",
		MaxProcessorWorkers: 1,
		MaxDownloadWorkers:  1,
	}
	nzbSystem, err := integration.NewNzbSystem(nzbCfg, poolManager, adapter.GetConfigGetter())
	if err != nil {
		t.Fatalf("new nzb system: %v", err)
	}
	t.Cleanup(func() {
		_ = nzbSystem.Close()
	})
	if err := nzbSystem.StopService(context.Background()); err != nil {
		t.Fatalf("stop nzb service: %v", err)
	}

	service := NewService(cfg, nzbSystem, nil)
	return service, nzbSystem
}

func serveNZB(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="release.nzb"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func usenetCandidate(downloadURL string) models.NZBResult {
	return models.NZBResult{
		Title:       "Some.Release.2020.1080p.BluRay.x264-GROUP",
		DownloadURL: downloadURL,
		ServiceType: models.ServiceTypeUsenet,
	}
}

// TestResolveProbeMissingRejectsPromptly is the core probe verification: a
// definitive missing-segments verdict (delivered while the full resolve is
// grinding — here the importer fails instantly and the verdict still wins via
// the grace path) rejects the release cheaply: no resolution is produced and
// the returned error satisfies both IsArticleUnavailable (so the existing
// bad-stream marking fires) and ErrUsenetProbeRejected (so the probe rejection
// is distinguishable from a full-download failure in the latency
// instrumentation). The full process is ATTEMPTED (probe runs concurrently) but
// never completes — the whole point of the concurrent design is that the
// verdict lands long before a dead download would finish.
func TestResolveProbeMissingRejectsPromptly(t *testing.T) {
	service, _ := newPreflightTestService(t)
	srv := serveNZB(t, `<?xml version="1.0"?><nzb></nzb>`)
	checker := &stubAvailabilityChecker{result: &models.NZBHealthCheck{
		Status:          "missing_segments",
		Healthy:         false,
		CheckedSegments: 3,
		TotalSegments:   100,
		MissingSegments: []string{"<mid.segment@news.example>"},
		Sampled:         true,
	}}
	service.SetUsenetHealthChecker(checker)

	start := time.Now()
	res, err := service.Resolve(context.Background(), usenetCandidate(srv.URL+"/dead.nzb"))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Resolve returned nil error for a probe-rejected release")
	}
	if !importer.IsArticleUnavailable(err) {
		t.Fatalf("err = %v; want IsArticleUnavailable so bad-stream marking fires", err)
	}
	if !errors.Is(err, ErrUsenetProbeRejected) {
		t.Fatalf("err = %v; want ErrUsenetProbeRejected so the probe rejection is distinguishable", err)
	}
	if res != nil {
		t.Fatalf("Resolve returned a resolution for a rejected release: %+v", res)
	}
	// The importer entry was STARTED (parallel probe) but the probe verdict won;
	// rejection must be prompt — seconds, not the multi-minute download the
	// pre-probe path paid.
	if n := service.nzbProcessCount.Load(); n != 1 {
		t.Fatalf("nzbProcessCount = %d, want 1 (process started, then cancelled by the probe verdict)", n)
	}
	if elapsed >= 3*time.Second {
		t.Fatalf("probe rejection took %v; want seconds, not minutes", elapsed)
	}
	if checker.calls.Load() != 1 {
		t.Fatalf("availability checker calls = %d, want 1", checker.calls.Load())
	}
}

// TestResolveProbeVerdictWaitsOutFastResolveError pins the grace-window
// semantics: when the full resolve fails BEFORE the probe verdict lands, Resolve
// holds the failure up to preflightVerdictGrace so a definitive rejection is
// not masked by a faster-decaying resolve error (a dead release whose a resolve
// happened to fail fast would otherwise skip bad-stream marking).
func TestResolveProbeVerdictWaitsOutFastResolveError(t *testing.T) {
	service, _ := newPreflightTestService(t)
	srv := serveNZB(t, `<?xml version="1.0"?><nzb></nzb>`)
	// The checker blocks until released, then reports the missing verdict.
	release := make(chan struct{})
	checker := &gatedAvailabilityChecker{
		release: release,
		result: &models.NZBHealthCheck{
			Status:          "missing_segments",
			Healthy:         false,
			CheckedSegments: 3,
			TotalSegments:   100,
			MissingSegments: []string{"<mid.segment@news.example>"},
			Sampled:         true,
		},
	}
	service.SetUsenetHealthChecker(checker)

	done := make(chan error, 1)
	go func() {
		_, err := service.Resolve(context.Background(), usenetCandidate(srv.URL+"/dead-slow-verdict.nzb"))
		done <- err
	}()

	// The importer fails instantly, so the resolve error is available within
	// milliseconds — Resolve must NOT return it while the probe is pending.
	time.Sleep(150 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("Resolve returned early (%v) while the probe verdict was still pending", err)
	default:
	}

	close(release) // probe verdict lands: missing
	select {
	case err := <-done:
		if !errors.Is(err, ErrUsenetProbeRejected) || !importer.IsArticleUnavailable(err) {
			t.Fatalf("err = %v, want the probe rejection (grace window must prefer the verdict)", err)
		}
	case <-time.After(preflightVerdictGrace + 2*time.Second):
		t.Fatal("Resolve did not return after the probe verdict within the grace window")
	}
}

type gatedAvailabilityChecker struct {
	release chan struct{}
	result  *models.NZBHealthCheck
	calls   atomic.Int32
}

func (s *gatedAvailabilityChecker) CheckHealthWithNZB(ctx context.Context, candidate models.NZBResult, nzbBytes []byte, fileName string) (*models.NZBHealthCheck, error) {
	s.calls.Add(1)
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.result, nil
}

// TestResolveProbeHealthyProceedsToFullResolve: a healthy probe verdict must not
// skip the full resolve — the release still has to be downloaded and indexed.
func TestResolveProbeHealthyProceedsToFullResolve(t *testing.T) {
	service, _ := newPreflightTestService(t)
	srv := serveNZB(t, `<?xml version="1.0"?><nzb></nzb>`)
	checker := &stubAvailabilityChecker{result: &models.NZBHealthCheck{
		Status:          "healthy",
		Healthy:         true,
		CheckedSegments: 3,
		TotalSegments:   100,
		Sampled:         true,
	}}
	service.SetUsenetHealthChecker(checker)

	_, err := service.Resolve(context.Background(), usenetCandidate(srv.URL+"/good.nzb"))
	if err == nil {
		t.Fatal("Resolve returned nil error; the stopped importer should fail the full resolve")
	}
	if importer.IsArticleUnavailable(err) || errors.Is(err, ErrUsenetProbeRejected) {
		t.Fatalf("a healthy probe must never reject; err = %v", err)
	}
	if checker.calls.Load() != 1 {
		t.Fatalf("availability checker calls = %d, want 1", checker.calls.Load())
	}
}

// TestResolveProbeInconclusiveFallsThroughToFullResolve: probe errors, missing
// provider config and timeouts are all inconclusive — the release must fall
// through to the full resolve and never be rejected on a failed probe.
func TestResolveProbeInconclusiveFallsThroughToFullResolve(t *testing.T) {
	cases := map[string]error{
		"connect-error":    errors.New("connect to usenet server: dial tcp 1.2.3.4:563: i/o timeout"),
		"no-providers":     errors.New("no enabled usenet providers configured"),
		"check-deadline":   context.DeadlineExceeded,
		"checker-internal": errors.New("parse nzb: XML syntax error"),
	}
	for name, checkerErr := range cases {
		t.Run(name, func(t *testing.T) {
			service, _ := newPreflightTestService(t)
			srv := serveNZB(t, `<?xml version="1.0"?><nzb></nzb>`)
			checker := &stubAvailabilityChecker{err: checkerErr}
			service.SetUsenetHealthChecker(checker)

			_, err := service.Resolve(context.Background(), usenetCandidate(srv.URL+"/maybe.nzb"))
			if err == nil {
				t.Fatal("expected the full resolve to run (and fail on the stopped importer)")
			}
			if importer.IsArticleUnavailable(err) || errors.Is(err, ErrUsenetProbeRejected) {
				t.Fatalf("an inconclusive probe must never reject the release; err = %v", err)
			}
			if checker.calls.Load() != 1 {
				t.Fatalf("availability checker calls = %d, want 1", checker.calls.Load())
			}
		})
	}
}

// TestResolveWithoutCheckerSkipsProbe: no checker wired (the default) means the
// probe never runs and resolution behaves exactly as before.
func TestResolveWithoutCheckerSkipsProbe(t *testing.T) {
	service, _ := newPreflightTestService(t)
	srv := serveNZB(t, `<?xml version="1.0"?><nzb></nzb>`)

	_, err := service.Resolve(context.Background(), usenetCandidate(srv.URL+"/old.nzb"))
	if err == nil {
		t.Fatal("expected the full resolve to run (and fail on the stopped importer)")
	}
	if importer.IsArticleUnavailable(err) || errors.Is(err, ErrUsenetProbeRejected) {
		t.Fatalf("no checker means no probe rejection; err = %v", err)
	}
	if service.usenetHealth != nil {
		t.Fatal("usenetHealth must stay nil without SetUsenetHealthChecker")
	}
}

// TestResolveProbeRunsUnderConfiguredBudget: the probe context carries a
// deadline derived from Streaming.UsenetPreflightProbeSec, so a hung/slow
// provider cannot stretch the pre-flight past its budget (and the eventual
// timeout still fails open, per the inconclusive test above).
func TestResolveProbeRunsUnderConfiguredBudget(t *testing.T) {
	service, _ := newPreflightTestService(t)
	srv := serveNZB(t, `<?xml version="1.0"?><nzb></nzb>`)

	cfg := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := cfg.Save(config.DefaultSettings()); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	service.cfg = cfg
	loaded, _ := cfg.Load()
	loaded.Streaming.UsenetPreflightProbeSec = 1
	if err := cfg.Save(loaded); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	checker := &deadlineProbeChecker{inner: &stubAvailabilityChecker{result: &models.NZBHealthCheck{Healthy: true}}}
	service.SetUsenetHealthChecker(checker)

	_, err := service.Resolve(context.Background(), usenetCandidate(srv.URL+"/budget.nzb"))
	if err == nil {
		t.Fatal("expected the full resolve to run (and fail on the stopped importer)")
	}
	if importer.IsArticleUnavailable(err) {
		t.Fatalf("healthy probe must not reject; err = %v", err)
	}
	budget, hasDeadline := checker.snapshot()
	if !hasDeadline {
		t.Fatal("probe ran without a bounded context; the preflight budget is not applied")
	}
	if budget > 3*time.Second || budget < 500*time.Millisecond {
		t.Fatalf("probe deadline distance = %v, want ~1s (UsenetPreflightProbeSec)", budget)
	}
}
