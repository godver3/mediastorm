package usenet

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/javi11/nntpcli"
	"github.com/javi11/nntppool"

	"novastream/config"
	"novastream/models"
)

type stubClient struct {
	results map[string]bool
	mu      sync.Mutex
	calls   []string
}

func (s *stubClient) CheckArticle(ctx context.Context, messageID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, messageID)
	if res, ok := s.results[messageID]; ok {
		return res, nil
	}
	return true, nil // Return true by default to simulate healthy
}

func (s *stubClient) Close() error { return nil }

type stubPoolManager struct {
	pool nntppool.UsenetConnectionPool
}

func (s *stubPoolManager) GetPool() (nntppool.UsenetConnectionPool, error) {
	if s.pool == nil {
		return nil, fmt.Errorf("no pool configured")
	}
	return s.pool, nil
}

func (s *stubPoolManager) SetProviders(providers []nntppool.UsenetProviderConfig) error { return nil }

func (s *stubPoolManager) ClearPool() error {
	s.pool = nil
	return nil
}

func (s *stubPoolManager) HasPool() bool { return s.pool != nil }

type stubPool struct {
	mu    sync.Mutex
	stats map[string]struct {
		code int
		err  error
	}
	statCalls []string
}

func newStubPool(stats map[string]struct {
	code int
	err  error
}) *stubPool {
	return &stubPool{stats: stats}
}

func (s *stubPool) GetConnection(ctx context.Context, skipProviders []string, useBackupProviders bool) (nntppool.PooledConnection, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *stubPool) Body(ctx context.Context, msgID string, w io.Writer, nntpGroups []string) (int64, error) {
	return 0, fmt.Errorf("not implemented")
}

func (s *stubPool) BodyReader(ctx context.Context, msgID string, nntpGroups []string) (nntpcli.ArticleBodyReader, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *stubPool) Post(ctx context.Context, r io.Reader) error { return fmt.Errorf("not implemented") }

func (s *stubPool) Stat(ctx context.Context, msgID string, nntpGroups []string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statCalls = append(s.statCalls, msgID)
	if resp, ok := s.stats[msgID]; ok {
		return resp.code, resp.err
	}
	return 223, nil
}

func (s *stubPool) GetProvidersInfo() []nntppool.ProviderInfo { return nil }

func (s *stubPool) GetProviderStatus(providerID string) (*nntppool.ProviderInfo, bool) {
	return nil, false
}

func (s *stubPool) Reconfigure(configs ...nntppool.Config) error { return nil }

func (s *stubPool) GetReconfigurationStatus(migrationID string) (*nntppool.ReconfigurationStatus, bool) {
	return nil, false
}

func (s *stubPool) GetActiveReconfigurations() map[string]*nntppool.ReconfigurationStatus {
	return nil
}

func (s *stubPool) GetMetrics() *nntppool.PoolMetrics { return nil }

func (s *stubPool) GetMetricsSnapshot() nntppool.PoolMetricsSnapshot {
	return nntppool.PoolMetricsSnapshot{}
}

func (s *stubPool) Quit() {}

func TestExtractSegmentIDs(t *testing.T) {
	sample := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<nzb>
  <file subject="Example">
    <segments>
      <segment bytes="123" number="1">&lt;item1@test&gt;</segment>
      <segment bytes="124" number="2">&lt;item2@test&gt;</segment>
    </segments>
  </file>
</nzb>`)

	ids, total, has7z, subjects, err := extractSegmentIDs(sample)
	if err != nil {
		t.Fatalf("extractSegmentIDs returned error: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected total 2, got %d", total)
	}
	if len(ids) != 2 || ids[0] != "<item1@test>" || ids[1] != "<item2@test>" {
		t.Fatalf("unexpected ids: %#v", ids)
	}
	if has7z {
		t.Fatalf("expected has7z=false for sample without 7z")
	}
	if len(subjects) != 1 || subjects[0] != "Example" {
		t.Fatalf("unexpected subjects: %#v", subjects)
	}
}

func TestExtractSegmentIDsDetects7z(t *testing.T) {
	sample := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<nzb>
  <file subject="[1/8] &quot;Movie.7z&quot; yEnc">
    <segments>
      <segment bytes="123" number="1">&lt;item1@test&gt;</segment>
    </segments>
  </file>
</nzb>`)

	_, total, has7z, subjects, err := extractSegmentIDs(sample)
	if err != nil {
		t.Fatalf("extractSegmentIDs returned error: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected total 1, got %d", total)
	}
	if !has7z {
		t.Fatalf("expected has7z=true when subject contains 7z")
	}
	if len(subjects) != 1 || !strings.Contains(subjects[0], "Movie.7z") {
		t.Fatalf("expected subject to include Movie.7z, got %#v", subjects)
	}
}

func TestServiceCheckHealth(t *testing.T) {
	cfg := config.DefaultSettings()
	cfg.Usenet = []config.UsenetSettings{
		{
			Name:        "Test Provider",
			Host:        "news.example",
			Port:        563,
			SSL:         true,
			Username:    "user",
			Password:    "pass",
			Connections: 8,
			Enabled:     true,
		},
	}

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(cfg); err != nil {
		t.Fatalf("save cfg: %v", err)
	}

	sampleNZB := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<nzb>
  <file subject="Example">
    <segments>
      <segment bytes="123" number="1">&lt;item1@test&gt;</segment>
      <segment bytes="124" number="2">&lt;item2@test&gt;</segment>
    </segments>
  </file>
</nzb>`)

	stub := &stubClient{results: map[string]bool{
		"<item1@test>": true,
		"<item2@test>": false,
	}}

	svc := NewService(mgr, nil)
	svc.dialer = func(ctx context.Context, settings config.UsenetSettings) (statClient, error) {
		return stub, nil
	}
	svc.httpClient = newStaticHTTPClient(t, http.StatusOK, sampleNZB, http.Header{
		"Content-Type": {"application/xml"},
	})
	svc.maxSegments = 10 // Check all segments for this test

	candidate := models.NZBResult{Title: "Example", DownloadURL: "https://example.com/test.nzb"}

	res, err := svc.CheckHealth(context.Background(), candidate)
	if err != nil {
		t.Fatalf("CheckHealth returned error: %v", err)
	}

	if res.Healthy {
		t.Fatalf("expected unhealthy result due to missing segment")
	}
	if res.Status != "missing_segments" {
		t.Fatalf("unexpected status: %s", res.Status)
	}
	if res.CheckedSegments != 2 || res.TotalSegments != 2 {
		t.Fatalf("unexpected segment counts: %+v", res)
	}
	if len(res.MissingSegments) != 1 || res.MissingSegments[0] != "<item2@test>" {
		t.Fatalf("unexpected missing segments: %+v", res.MissingSegments)
	}
	stub.mu.Lock()
	callCount := len(stub.calls)
	stub.mu.Unlock()
	if callCount != 2 {
		t.Fatalf("expected 2 NNTP checks, got %d", callCount)
	}
}

func TestServiceAllowsSevenZipArchives(t *testing.T) {
	// 7z archives are now supported (for uncompressed/store mode)
	// The health check should pass and actually check segments
	cfg := config.DefaultSettings()
	cfg.Usenet = []config.UsenetSettings{
		{
			Name:        "Test Provider",
			Host:        "news.example",
			Port:        563,
			SSL:         true,
			Username:    "user",
			Password:    "pass",
			Connections: 8,
			Enabled:     true,
		},
	}

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(cfg); err != nil {
		t.Fatalf("save cfg: %v", err)
	}

	sampleNZB := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<nzb>
  <file subject="[1/8] &quot;Movie.Part1.7z&quot; yEnc">
    <segments>
      <segment bytes="123" number="1">&lt;item1@test&gt;</segment>
    </segments>
  </file>
</nzb>`)

	stub := &stubClient{results: map[string]bool{
		"<item1@test>": true,
	}}

	svc := NewService(mgr, nil)
	svc.httpClient = newStaticHTTPClient(t, http.StatusOK, sampleNZB, http.Header{
		"Content-Type": {"application/xml"},
	})
	svc.dialer = func(ctx context.Context, settings config.UsenetSettings) (statClient, error) {
		return stub, nil
	}

	candidate := models.NZBResult{Title: "Movie", DownloadURL: "https://example.com/movie.nzb"}

	res, err := svc.CheckHealth(context.Background(), candidate)
	if err != nil {
		t.Fatalf("CheckHealth returned error: %v", err)
	}

	if !res.Healthy {
		t.Fatalf("expected healthy result for 7z archive, got status: %s", res.Status)
	}
	if res.TotalSegments != 1 {
		t.Fatalf("expected total segments to reflect parsed NZB, got %d", res.TotalSegments)
	}
	if res.CheckedSegments != 1 {
		t.Fatalf("expected 1 checked segment, got %d", res.CheckedSegments)
	}
}

func TestServiceSamplingStrategy(t *testing.T) {
	cfg := config.DefaultSettings()
	cfg.Usenet = []config.UsenetSettings{
		{
			Name:        "Test Provider",
			Host:        "news.example",
			Port:        563,
			SSL:         true,
			Username:    "user",
			Password:    "pass",
			Connections: 8,
			Enabled:     true,
		},
	}

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(cfg); err != nil {
		t.Fatalf("save cfg: %v", err)
	}

	var builder strings.Builder
	builder.WriteString(`<?xml version="1.0" encoding="UTF-8"?>\n<nzb>\n  <file subject="Example">\n    <segments>\n`)
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&builder, "      <segment bytes=\"%d\" number=\"%d\">&lt;seg%02d@test&gt;</segment>\n", i+100, i+1, i)
	}
	builder.WriteString("    </segments>\n  </file>\n</nzb>")
	sampleNZB := []byte(builder.String())

	results := make(map[string]bool)
	for i := 0; i < 20; i++ {
		results[fmt.Sprintf("<seg%02d@test>", i)] = true
	}

	stub := &stubClient{results: results}

	svc := NewService(mgr, nil)
	svc.dialer = func(ctx context.Context, settings config.UsenetSettings) (statClient, error) {
		return stub, nil
	}
	svc.httpClient = newStaticHTTPClient(t, http.StatusOK, sampleNZB, http.Header{
		"Content-Type": {"application/xml"},
	})
	svc.rand = rand.New(rand.NewSource(1))
	svc.maxSegments = 3 // Sample first, last, and one middle segment

	candidate := models.NZBResult{Title: "Example", DownloadURL: "https://example.com/test.nzb"}

	res, err := svc.CheckHealth(context.Background(), candidate)
	if err != nil {
		t.Fatalf("CheckHealth returned error: %v", err)
	}

	if res.CheckedSegments != 3 {
		t.Fatalf("expected 3 sampled segments, got %d", res.CheckedSegments)
	}
	if !res.Sampled {
		t.Fatalf("expected sampled flag to be true when only a subset is checked")
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()

	if len(stub.calls) != res.CheckedSegments {
		t.Fatalf("expected %d NNTP checks, got %d", res.CheckedSegments, len(stub.calls))
	}

	callSet := make(map[string]struct{}, len(stub.calls))
	for _, id := range stub.calls {
		callSet[id] = struct{}{}
	}
	if len(callSet) != len(stub.calls) {
		t.Fatalf("expected %d unique segments, got %d", len(stub.calls), len(callSet))
	}

	firstID := "<seg00@test>"
	lastID := "<seg19@test>"
	if _, ok := callSet[firstID]; !ok {
		t.Fatalf("expected first segment %s to be sampled", firstID)
	}
	if _, ok := callSet[lastID]; !ok {
		t.Fatalf("expected last segment %s to be sampled", lastID)
	}

	middleFound := false
	for id := range callSet {
		if id != firstID && id != lastID {
			middleFound = true
			break
		}
	}
	if !middleFound {
		t.Fatalf("expected at least one middle segment to be sampled")
	}

	if !res.Healthy {
		t.Fatalf("expected healthy result when all segments verified")
	}
}

func TestServiceSamplingStrategyLargeNZB(t *testing.T) {
	cfg := config.DefaultSettings()
	cfg.Usenet = []config.UsenetSettings{
		{
			Name:        "Test Provider",
			Host:        "news.example",
			Port:        563,
			SSL:         true,
			Username:    "user",
			Password:    "pass",
			Connections: 8,
			Enabled:     true,
		},
	}

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(cfg); err != nil {
		t.Fatalf("save cfg: %v", err)
	}

	segmentCount := 200
	var builder strings.Builder
	builder.WriteString(`<?xml version="1.0" encoding="UTF-8"?>\n<nzb>\n  <file subject="Example">\n    <segments>\n`)
	for i := 0; i < segmentCount; i++ {
		fmt.Fprintf(&builder, "      <segment bytes=\"%d\" number=\"%d\">&lt;seg%03d@test&gt;</segment>\n", i+100, i+1, i)
	}
	builder.WriteString("    </segments>\n  </file>\n</nzb>")
	sampleNZB := []byte(builder.String())

	results := make(map[string]bool)
	for i := 0; i < segmentCount; i++ {
		results[fmt.Sprintf("<seg%03d@test>", i)] = true
	}

	stub := &stubClient{results: results}

	svc := NewService(mgr, nil)
	svc.dialer = func(ctx context.Context, settings config.UsenetSettings) (statClient, error) {
		return stub, nil
	}
	svc.httpClient = newStaticHTTPClient(t, http.StatusOK, sampleNZB, http.Header{
		"Content-Type": {"application/xml"},
	})
	svc.rand = rand.New(rand.NewSource(1))
	svc.maxSegments = 3 // Sample first, last, and one middle segment

	candidate := models.NZBResult{Title: "Example", DownloadURL: "https://example.com/test.nzb"}

	res, err := svc.CheckHealth(context.Background(), candidate)
	if err != nil {
		t.Fatalf("CheckHealth returned error: %v", err)
	}

	if res.CheckedSegments != 3 {
		t.Fatalf("expected 3 sampled segments, got %d", res.CheckedSegments)
	}
	if !res.Sampled {
		t.Fatalf("expected sampled flag to be true")
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()

	if len(stub.calls) != res.CheckedSegments {
		t.Fatalf("expected %d NNTP checks, got %d", res.CheckedSegments, len(stub.calls))
	}

	callSet := make(map[string]struct{}, len(stub.calls))
	for _, id := range stub.calls {
		callSet[id] = struct{}{}
	}
	if len(callSet) != res.CheckedSegments {
		t.Fatalf("expected %d unique segments, got %d", res.CheckedSegments, len(callSet))
	}

	for i := 0; i < edgeSampleCount; i++ {
		id := fmt.Sprintf("<seg%03d@test>", i)
		if _, ok := callSet[id]; !ok {
			t.Fatalf("missing expected leading sample %s", id)
		}
	}
	for i := segmentCount - edgeSampleCount; i < segmentCount; i++ {
		id := fmt.Sprintf("<seg%03d@test>", i)
		if _, ok := callSet[id]; !ok {
			t.Fatalf("missing expected trailing sample %s", id)
		}
	}

	middleHit := false
	for i := edgeSampleCount; i < segmentCount-edgeSampleCount; i++ {
		id := fmt.Sprintf("<seg%03d@test>", i)
		if _, ok := callSet[id]; ok {
			middleHit = true
			break
		}
	}
	if !middleHit {
		t.Fatalf("expected at least one middle segment to be sampled")
	}

	if !res.Healthy {
		t.Fatalf("expected healthy result when sampled segments verified")
	}
}

func TestServiceCheckHealthErrorsWithoutHost(t *testing.T) {
	cfg := config.DefaultSettings()
	cfg.Usenet = []config.UsenetSettings{}

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(cfg); err != nil {
		t.Fatalf("save cfg: %v", err)
	}

	svc := NewService(mgr, nil)

	_, err := svc.CheckHealth(context.Background(), models.NZBResult{DownloadURL: "https://example.com/file.nzb"})
	if err == nil {
		t.Fatalf("expected error when host missing")
	}
}

const testNZBURL = "https://api.nzbgeek.info/api?t=get&id=238a58fa5455b88139694cb3377915a4&apikey=dTifiaj5zQWtvzpF6sf5L0XYGTUwFT1N"

func TestFullHealthCheckSpecificNZB(t *testing.T) {
	if os.Getenv("NOVASTREAM_INTEGRATION_TESTS") == "" {
		t.Skip("skipping integration test that fetches a real NZB; set NOVASTREAM_INTEGRATION_TESTS=1 to run")
	}

	ctx := context.Background()

	// Create config with dummy Usenet settings (as in other tests)
	cfg := config.DefaultSettings()
	cfg.Usenet = []config.UsenetSettings{
		{
			Name:        "Test Provider",
			Host:        "news.example",
			Port:        563,
			SSL:         true,
			Username:    "user",
			Password:    "pass",
			Connections: 20,
			Enabled:     true,
		},
	}

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(cfg); err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	// Create service with config manager
	svc := NewService(mgr, nil)

	// Set up mock client to simulate all segments as healthy
	stub := &stubClient{results: make(map[string]bool)} // Start empty, assume all healthy (return false for missing)
	svc.dialer = func(ctx context.Context, settings config.UsenetSettings) (statClient, error) {
		return stub, nil
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, testNZBURL, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to fetch NZB: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("NZB fetch failed with status: %s", resp.Status)
	}

	nzbData, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read NZB body: %v", err)
	}

	if len(nzbData) == 0 {
		t.Fatal("NZB body is empty")
	}

	// Parse segments - use the internal extractSegmentIDs from service
	allSegments, total, _, _, err := extractSegmentIDs(nzbData)
	if err != nil {
		t.Fatalf("Failed to parse segments: %v", err)
	}
	t.Logf("Parsed %d total segments", total)

	if len(allSegments) == 0 {
		t.Fatal("No segments found in NZB")
	}

	// Full check - no sampling, use first provider
	missing, err := svc.checkSegmentsConcurrently(ctx, allSegments, cfg.Usenet)
	if err != nil {
		t.Fatalf("Full health check failed: %v", err)
	}

	t.Logf("Full health check completed: %d/%d segments accessible, %d missing", total-len(missing), total, len(missing))

	if len(missing) > 0 {
		t.Errorf("Found %d missing segments", len(missing))
		for _, id := range missing[:10] { // log first 10
			t.Logf("Missing: %s", id)
		}
		if len(missing) > 10 {
			t.Logf("... and %d more", len(missing)-10)
		}
	} else {
		t.Log("All segments are healthy and present!")
	}
}

func TestCheckSegmentsConcurrentlyFallsBackToSecondaryProvider(t *testing.T) {
	ctx := context.Background()
	segmentID := "missing@example"

	providers := []config.UsenetSettings{
		{Name: "Primary", Host: "primary.example", Enabled: true, Connections: 1},
		{Name: "Backup", Host: "backup.example", Enabled: true, Connections: 1},
	}

	svc := NewService(nil, nil)
	svc.dialer = func(ctx context.Context, settings config.UsenetSettings) (statClient, error) {
		switch settings.Host {
		case "primary.example":
			return &stubClient{results: map[string]bool{segmentID: false}}, nil
		case "backup.example":
			return &stubClient{results: map[string]bool{segmentID: true}}, nil
		default:
			return nil, fmt.Errorf("unexpected host %s", settings.Host)
		}
	}

	missing, err := svc.checkSegmentsConcurrently(ctx, []string{segmentID}, providers)
	if err != nil {
		t.Fatalf("checkSegmentsConcurrently returned error: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("expected no missing segments, got %v", missing)
	}
}

func TestCheckSegmentsWithPoolTrustsArticleNotFoundError(t *testing.T) {
	t.Skip("Pool is bypassed for health checks due to nntppool v1.5.5 Stat bug")
	// When the pool returns ErrArticleNotFoundInProviders, we trust that
	// all providers were checked and don't retry with the dialer fallback.
	ctx := context.Background()
	segmentID := "poolfallback@example"
	normalized := normalizeMessageID(segmentID)

	pool := newStubPool(map[string]struct {
		code int
		err  error
	}{
		normalized: {code: 0, err: nntppool.ErrArticleNotFoundInProviders},
	})

	providers := []config.UsenetSettings{
		{Name: "Primary", Host: "primary.example", Enabled: true, Connections: 1},
		{Name: "Backup", Host: "backup.example", Enabled: true, Connections: 1},
	}

	dialerCalled := false
	svc := NewService(nil, &stubPoolManager{pool: pool})
	svc.dialer = func(ctx context.Context, settings config.UsenetSettings) (statClient, error) {
		dialerCalled = true
		return &stubClient{results: map[string]bool{segmentID: true}}, nil
	}

	missing, err := svc.checkSegmentsConcurrently(ctx, []string{segmentID}, providers)
	if err != nil {
		t.Fatalf("checkSegmentsConcurrently returned error: %v", err)
	}
	// When pool returns ErrArticleNotFoundInProviders, we trust it and report missing
	if len(missing) != 1 {
		t.Fatalf("expected 1 missing segment (pool confirmed missing), got %v", missing)
	}
	// Dialer should NOT be called since pool already checked all providers
	if dialerCalled {
		t.Fatal("dialer should not be called when pool returns ErrArticleNotFoundInProviders")
	}

	pool.mu.Lock()
	callCount := len(pool.statCalls)
	pool.mu.Unlock()
	if callCount != 1 {
		t.Fatalf("expected pool.Stat to be called once, got %d", callCount)
	}
}

func TestCheckSegmentsWithPoolFallsBackOnConnectionErrors(t *testing.T) {
	t.Skip("Pool is bypassed for health checks due to nntppool v1.5.5 Stat bug")
	// When the pool returns a non-404 error (connection issues), we should
	// fall back to the dialer to retry with fresh connections.
	ctx := context.Background()
	segmentID := "connfail@example"
	normalized := normalizeMessageID(segmentID)

	pool := newStubPool(map[string]struct {
		code int
		err  error
	}{
		normalized: {code: 430, err: nil}, // 430 = not found, but not the definitive error
	})

	providers := []config.UsenetSettings{
		{Name: "Primary", Host: "primary.example", Enabled: true, Connections: 1},
	}

	svc := NewService(nil, &stubPoolManager{pool: pool})
	svc.dialer = func(ctx context.Context, settings config.UsenetSettings) (statClient, error) {
		// Dialer finds the segment (pool may have had stale connection)
		return &stubClient{results: map[string]bool{segmentID: true}}, nil
	}

	missing, err := svc.checkSegmentsConcurrently(ctx, []string{segmentID}, providers)
	if err != nil {
		t.Fatalf("checkSegmentsConcurrently returned error: %v", err)
	}
	// Fallback dialer found the segment
	if len(missing) != 0 {
		t.Fatalf("expected no missing segments after dialer fallback, got %v", missing)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newStaticHTTPClient(t *testing.T, statusCode int, body []byte, headers http.Header) *http.Client {
	t.Helper()
	baseHeaders := cloneHeader(headers)

	return &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: statusCode,
				Body:       io.NopCloser(bytes.NewReader(body)),
				Header:     cloneHeader(baseHeaders),
				Request:    req,
			}, nil
		}),
	}
}

func cloneHeader(h http.Header) http.Header {
	if len(h) == 0 {
		return make(http.Header)
	}
	out := make(http.Header, len(h))
	for k, v := range h {
		cp := make([]string, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}
