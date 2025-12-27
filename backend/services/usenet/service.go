package usenet

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/internal/pool"
	"novastream/models"

	"github.com/javi11/nntppool"
)

type statClient interface {
	CheckArticle(ctx context.Context, messageID string) (bool, error)
	Close() error
}

type dialerFunc func(ctx context.Context, settings config.UsenetSettings) (statClient, error)

type Service struct {
	cfg         *config.Manager
	httpClient  *http.Client
	dialer      dialerFunc
	poolManager pool.Manager // Connection pool for faster health checks
	maxSegments int
	rand        *rand.Rand
}

const (
	defaultSegmentSample  = 1
	defaultRequestTimeout = 60 * time.Second
	edgeSampleCount       = 1 // always pick the first and last article
	randomSampleCount     = 1 // sample a single article from the middle slice
)

func NewService(cfg *config.Manager, poolManager pool.Manager) *Service {
	return &Service{
		cfg:         cfg,
		httpClient:  &http.Client{Timeout: defaultRequestTimeout},
		dialer:      newNNTPClient,
		poolManager: poolManager,
		maxSegments: defaultSegmentSample,
		rand:        rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (s *Service) CheckHealth(ctx context.Context, candidate models.NZBResult) (*models.NZBHealthCheck, error) {
	downloadURL := strings.TrimSpace(candidate.DownloadURL)
	if downloadURL == "" {
		downloadURL = strings.TrimSpace(candidate.Link)
	}
	if downloadURL == "" {
		return nil, fmt.Errorf("nzb result is missing a download URL")
	}

	start := time.Now()
	log.Printf("[usenet] health check start title=%q url=%q", strings.TrimSpace(candidate.Title), downloadURL)

	settings, err := s.loadUsenetSettings()
	if err != nil {
		return nil, err
	}

	nzbBytes, fileName, err := s.fetchNZB(ctx, downloadURL, candidate)
	if err != nil {
		return nil, err
	}
	return s.evaluateNZBHealth(ctx, settings, candidate, nzbBytes, fileName, start)
}

// CheckHealthWithNZB performs a Usenet health check using a pre-fetched NZB payload.
func (s *Service) CheckHealthWithNZB(ctx context.Context, candidate models.NZBResult, nzbBytes []byte, fileName string) (*models.NZBHealthCheck, error) {
	downloadURL := strings.TrimSpace(candidate.DownloadURL)
	if downloadURL == "" {
		downloadURL = strings.TrimSpace(candidate.Link)
	}
	start := time.Now()
	log.Printf("[usenet] health check start title=%q url=%q (prefetched=true)", strings.TrimSpace(candidate.Title), downloadURL)

	settings, err := s.loadUsenetSettings()
	if err != nil {
		return nil, err
	}

	if len(nzbBytes) == 0 {
		return nil, fmt.Errorf("nzb payload is empty")
	}
	return s.evaluateNZBHealth(ctx, settings, candidate, nzbBytes, fileName, start)
}

func (s *Service) evaluateNZBHealth(ctx context.Context, settings config.Settings, candidate models.NZBResult, nzbBytes []byte, fileName string, start time.Time) (*models.NZBHealthCheck, error) {
	allSegments, totalSegments, hasSevenZip, fileSubjects, err := extractSegmentIDs(nzbBytes)
	if err != nil {
		return nil, err
	}
	if totalSegments == 0 {
		return nil, fmt.Errorf("nzb did not contain any segments")
	}

	trimmedName := strings.TrimSpace(fileName)
	if len(fileSubjects) > 0 {
		log.Printf("[usenet] files title=%q count=%d list=%s", strings.TrimSpace(candidate.Title), len(fileSubjects), summarizeNZBFileList(fileSubjects))
	}
	// Note: 7z archives are now supported (for uncompressed/store mode)
	// Compressed 7z archives will be rejected during import, not during health check
	_ = hasSevenZip // suppress unused variable warning

	sampleSegments := s.sampleSegments(allSegments)

	enabledProviders := filterEnabledUsenetProviders(settings.Usenet)
	if len(enabledProviders) == 0 {
		return nil, fmt.Errorf("no enabled usenet providers configured")
	}

	missing, err := s.checkSegmentsConcurrently(ctx, sampleSegments, enabledProviders)
	if err != nil {
		return nil, err
	}

	checkedCount := len(sampleSegments)

	status := "healthy"
	if len(missing) > 0 {
		status = "missing_segments"
	}

	result := &models.NZBHealthCheck{
		Status:          status,
		Healthy:         len(missing) == 0,
		CheckedSegments: checkedCount,
		TotalSegments:   totalSegments,
		MissingSegments: missing,
		FileName:        trimmedName,
	}

	if totalSegments > checkedCount {
		result.Sampled = true
	}

	logHealthCheckResult(candidate, result, time.Since(start))

	return result, nil
}

func (s *Service) loadUsenetSettings() (config.Settings, error) {
	settings, err := s.cfg.Load()
	if err != nil {
		return config.Settings{}, fmt.Errorf("load settings: %w", err)
	}

	// Check if at least one enabled provider is configured
	hasEnabledProvider := false
	for _, provider := range settings.Usenet {
		if provider.Enabled && strings.TrimSpace(provider.Host) != "" {
			hasEnabledProvider = true
			break
		}
	}

	if !hasEnabledProvider {
		return config.Settings{}, fmt.Errorf("no enabled usenet providers configured")
	}

	return settings, nil
}

func (s *Service) fetchNZB(ctx context.Context, downloadURL string, candidate models.NZBResult) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build nzb request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download nzb: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, "", fmt.Errorf("download nzb failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read nzb body: %w", err)
	}

	fileName := deriveNZBFileName(resp, downloadURL, candidate.Title)
	return data, fileName, nil
}

type nzbDocument struct {
	Files []nzbFile `xml:"file"`
}

type nzbFile struct {
	Subject  string       `xml:"subject,attr"`
	Segments []nzbSegment `xml:"segments>segment"`
}

type nzbSegment struct {
	ID string `xml:",chardata"`
}

func extractSegmentIDs(data []byte) ([]string, int, bool, []string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false

	var doc nzbDocument
	if err := dec.Decode(&doc); err != nil {
		return nil, 0, false, nil, fmt.Errorf("parse nzb: %w", err)
	}

	ids := make([]string, 0)
	hasSevenZip := false
	subjects := make([]string, 0, len(doc.Files))

	for _, file := range doc.Files {
		trimmedSubject := strings.TrimSpace(file.Subject)
		if trimmedSubject != "" {
			subjects = append(subjects, trimmedSubject)
		}
		if containsSevenZipIndicator(trimmedSubject) {
			hasSevenZip = true
		}
		for _, segment := range file.Segments {
			id := strings.TrimSpace(segment.ID)
			if id == "" {
				continue
			}
			ids = append(ids, id)
		}
	}

	return ids, len(ids), hasSevenZip, subjects, nil
}

func containsSevenZipIndicator(subject string) bool {
	if subject == "" {
		return false
	}
	lower := strings.ToLower(subject)
	if strings.Contains(lower, ".7z") {
		return true
	}
	return strings.Contains(lower, ".7zip")
}

func summarizeNZBFileList(subjects []string) string {
	unique := make([]string, 0, len(subjects))
	seen := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		trimmed := strings.TrimSpace(subject)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		unique = append(unique, trimmed)
	}

	sort.Strings(unique)
	if len(unique) == 0 {
		return ""
	}

	const maxEntries = 10
	if len(unique) <= maxEntries {
		return strings.Join(unique, ", ")
	}

	summary := append([]string{}, unique[:maxEntries]...)
	summary = append(summary, fmt.Sprintf("... (+%d more)", len(unique)-maxEntries))
	return strings.Join(summary, ", ")
}

func logHealthCheckResult(candidate models.NZBResult, result *models.NZBHealthCheck, elapsed time.Duration) {
	log.Printf(
		"[usenet] health result title=%q status=%s sampled=%t checked=%d total=%d missing=%d duration=%s file=%q",
		strings.TrimSpace(candidate.Title),
		result.Status,
		result.Sampled,
		result.CheckedSegments,
		result.TotalSegments,
		len(result.MissingSegments),
		elapsed,
		strings.TrimSpace(result.FileName),
	)
}

func (s *Service) sampleSegments(all []string) []string {
	total := len(all)
	if total == 0 {
		return nil
	}

	limit := s.maxSegments
	if limit <= 0 || limit > total {
		limit = total
	}

	// If the NZB is small, check everything we parsed.
	if total <= limit {
		return append([]string(nil), all...)
	}

	edge := edgeSampleCount
	if edge*2 > limit {
		edge = limit / 2
	}
	if edge*2 > total {
		edge = total / 2
	}
	if edge == 0 {
		edge = 1
	}

	selected := make([]string, 0, limit)
	seen := make(map[string]struct{}, limit)
	add := func(segment string) {
		if segment == "" {
			return
		}
		if _, ok := seen[segment]; ok {
			return
		}
		seen[segment] = struct{}{}
		selected = append(selected, segment)
	}

	for i := 0; i < edge && i < total; i++ {
		add(all[i])
	}
	for i := total - edge; i < total; i++ {
		if i < 0 {
			continue
		}
		add(all[i])
	}

	if len(selected) >= limit {
		return selected[:limit]
	}

	middleStart := edge
	middleEnd := total - edge
	if middleEnd < middleStart {
		middleStart = 0
		middleEnd = total
	}

	middle := all[middleStart:middleEnd]

	randomTarget := randomSampleCount
	if randomTarget <= 0 {
		randomTarget = 1
	}
	needed := limit - len(selected)
	if needed < randomTarget {
		randomTarget = needed
	}
	if randomTarget > len(middle) {
		randomTarget = len(middle)
	}

	if randomTarget > 0 {
		perm := s.rand.Perm(len(middle))
		for _, idx := range perm[:randomTarget] {
			add(middle[idx])
		}
	}

	if len(selected) > limit {
		return selected[:limit]
	}

	if len(selected) < limit {
		for _, segment := range middle {
			add(segment)
			if len(selected) >= limit {
				return selected[:limit]
			}
		}
		for _, segment := range all {
			add(segment)
			if len(selected) >= limit {
				return selected[:limit]
			}
		}
	}

	if len(selected) > limit {
		return selected[:limit]
	}

	return selected
}

// TODO: Consider removing this health check entirely since RAR analysis serves as
// implicit validation - if the RAR structure can be read from Usenet, the data is healthy.
// This would save ~5 seconds on playback initiation.
func (s *Service) checkSegmentsConcurrently(ctx context.Context, segments []string, providers []config.UsenetSettings) ([]string, error) {
	if len(segments) == 0 {
		return nil, nil
	}

	enabled := filterEnabledUsenetProviders(providers)
	if len(enabled) == 0 {
		return nil, fmt.Errorf("no enabled usenet providers configured")
	}

	// Skip pool for health checks - nntppool v1.5.5 has a bug where Stat
	// returns ErrArticleNotFoundInProviders even when articles exist.
	// Direct connections work correctly.
	// TODO: Re-enable pool usage once nntppool Stat is fixed upstream
	return s.checkSegmentsWithDialer(ctx, segments, enabled)
}

// checkSegmentsWithPool uses the connection pool for faster health checks
func (s *Service) checkSegmentsWithPool(ctx context.Context, segments []string, providers []config.UsenetSettings) ([]string, error) {
	pool, err := s.poolManager.GetPool()
	if err != nil {
		return nil, fmt.Errorf("get connection pool: %w", err)
	}

	var (
		wg                sync.WaitGroup
		mu                sync.Mutex
		retryIDs          []string               // Segments that need retry (connection issues)
		definitelyMissing []string               // Segments confirmed missing from all providers
		seen              = make(map[string]struct{})
	)

	// Check all segments concurrently using the pool
	for _, segmentID := range segments {
		segmentID := segmentID
		wg.Add(1)
		go func() {
			defer wg.Done()

			normalizedID := normalizeMessageID(segmentID)
			code, err := pool.Stat(ctx, normalizedID, nil)

			mu.Lock()
			defer mu.Unlock()

			if _, exists := seen[segmentID]; exists {
				return
			}
			seen[segmentID] = struct{}{}

			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				// Check if pool confirmed segment is missing from all providers
				if errors.Is(err, nntppool.ErrArticleNotFoundInProviders) {
					// Pool already checked all providers - no need to retry
					definitelyMissing = append(definitelyMissing, segmentID)
					return
				}
				// Other errors (connection issues) - queue for retry
				log.Printf("[usenet] warning: failed to check segment %s: %v", segmentID, err)
				retryIDs = append(retryIDs, segmentID)
				return
			}

			if code != 223 {
				// Non-found response - queue for retry with fresh connections
				retryIDs = append(retryIDs, segmentID)
			}
		}()
	}

	wg.Wait()

	// If any segments are definitively missing, fail fast
	if len(definitelyMissing) > 0 {
		log.Printf("[usenet] %d segment(s) confirmed missing from all providers by pool", len(definitelyMissing))
		return definitelyMissing, nil
	}

	// Retry segments that had connection issues
	if len(retryIDs) == 0 {
		return nil, nil
	}

	log.Printf("[usenet] retrying %d segment(s) with fresh connections", len(retryIDs))
	missing, err := s.checkSegmentsWithDialer(ctx, retryIDs, providers)
	if err != nil {
		return nil, err
	}

	return missing, nil
}

func (s *Service) checkSegmentsWithDialer(ctx context.Context, segments []string, providers []config.UsenetSettings) ([]string, error) {
	remaining := uniqueStrings(segments)
	if len(remaining) == 0 {
		return nil, nil
	}

	// Check segments one at a time against all providers for fast-fail behavior
	// If any single segment is missing from all providers, fail immediately
	for _, segment := range remaining {
		segmentFound := false

		for _, provider := range providers {
			missing, err := s.checkSegmentsOnProvider(ctx, []string{segment}, provider)
			if err != nil {
				return nil, err
			}

			// If this provider has the segment (missing is empty), mark as found
			if len(missing) == 0 {
				segmentFound = true
				break // No need to check other providers for this segment
			}
		}

		// If this segment wasn't found on any provider, fail immediately
		if !segmentFound {
			log.Printf("[usenet] segment %s missing from all providers - failing fast", segment)
			return []string{segment}, nil
		}
	}

	// All segments found across providers
	return nil, nil
}

func (s *Service) checkSegmentsOnProvider(ctx context.Context, segments []string, provider config.UsenetSettings) ([]string, error) {
	if len(segments) == 0 {
		return nil, nil
	}

	maxConcurrency := provider.Connections
	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}
	if maxConcurrency > len(segments) {
		maxConcurrency = len(segments)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	segmentCh := make(chan string)
	errCh := make(chan error, 1)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		missing []string
		errOnce sync.Once
	)

	sendErr := func(err error) {
		errOnce.Do(func() {
			select {
			case errCh <- err:
			default:
			}
		})
	}

	const maxRetries = 2
	const retryDelay = 500 * time.Millisecond

	for i := 0; i < maxConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var client statClient
			var err error

			// Helper to get a fresh connection
			getClient := func() error {
				if client != nil {
					client.Close()
				}
				client, err = s.dialer(ctx, provider)
				return err
			}

			if err := getClient(); err != nil {
				sendErr(fmt.Errorf("connect to usenet server: %w", err))
				cancel()
				return
			}
			defer func() {
				if client != nil {
					client.Close()
				}
			}()

			for {
				select {
				case <-ctx.Done():
					return
				case segmentID, ok := <-segmentCh:
					if !ok {
						return
					}

					var okArticle bool
					var lastErr error

					// Try with retries for transient NNTP errors (502, etc.)
					for attempt := 0; attempt <= maxRetries; attempt++ {
						okArticle, lastErr = client.CheckArticle(ctx, segmentID)
						if lastErr == nil {
							break
						}

						// Check if this is a retryable NNTP error
						if !isRetryableNNTPError(lastErr) {
							break
						}

						if attempt < maxRetries {
							log.Printf("[usenet] retryable error on segment %s (attempt %d/%d): %v",
								segmentID, attempt+1, maxRetries+1, lastErr)

							// Wait before retry
							select {
							case <-ctx.Done():
								return
							case <-time.After(retryDelay):
							}

							// Get fresh connection for retry
							if err := getClient(); err != nil {
								lastErr = fmt.Errorf("reconnect failed: %w", err)
								break
							}
						}
					}

					if lastErr != nil {
						sendErr(fmt.Errorf("check article %s: %w", segmentID, lastErr))
						cancel()
						return
					}
					if !okArticle {
						mu.Lock()
						missing = append(missing, segmentID)
						mu.Unlock()
					}
				}
			}
		}()
	}

	go func() {
		defer close(segmentCh)
		for _, segmentID := range segments {
			select {
			case <-ctx.Done():
				return
			case segmentCh <- segmentID:
			}
		}
	}()

	wg.Wait()

	select {
	case err := <-errCh:
		return nil, err
	default:
	}

	return uniqueStrings(missing), nil
}

func normalizeMessageID(id string) string {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "<") && strings.HasSuffix(trimmed, ">") {
		return trimmed
	}
	trimmed = strings.Trim(trimmed, "<>")
	return "<" + trimmed + ">"
}

func uniqueStrings(values []string) []string {
	if len(values) <= 1 {
		return values
	}

	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, exists := seen[v]; exists {
			continue
		}
		seen[v] = struct{}{}
		result = append(result, v)
	}

	return result
}

// isRetryableNNTPError returns true if the error is a transient NNTP error
// that may succeed on retry with a fresh connection.
// NNTP 502 = "Service temporarily unavailable" / "Too many connections"
// NNTP 503 = "Program fault - command not performed"
func isRetryableNNTPError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	// Check for specific NNTP error codes that are transient
	if strings.Contains(errStr, "502") ||
		strings.Contains(errStr, "503") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "i/o timeout") {
		return true
	}
	return false
}

func filterEnabledUsenetProviders(providers []config.UsenetSettings) []config.UsenetSettings {
	enabled := make([]config.UsenetSettings, 0, len(providers))
	for _, provider := range providers {
		if !provider.Enabled {
			continue
		}
		if strings.TrimSpace(provider.Host) == "" {
			continue
		}
		enabled = append(enabled, provider)
	}
	return enabled
}

func deriveNZBFileName(resp *http.Response, downloadURL, title string) string {
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if name := parseFileNameFromContentDisposition(cd); name != "" {
			return ensureNZBExtension(name)
		}
	}

	if parsed, err := url.Parse(downloadURL); err == nil && parsed.Path != "" {
		parts := strings.Split(parsed.Path, "/")
		candidate := parts[len(parts)-1]
		if candidate != "" {
			return ensureNZBExtension(candidate)
		}
	}

	if trimmed := strings.TrimSpace(title); trimmed != "" {
		safe := strings.Map(func(r rune) rune {
			switch {
			case r == ' ':
				return '.'
			case r >= 'a' && r <= 'z':
				fallthrough
			case r >= 'A' && r <= 'Z':
				fallthrough
			case r >= '0' && r <= '9':
				return r
			case r == '.' || r == '-' || r == '_':
				return r
			default:
				return -1
			}
		}, trimmed)
		if safe != "" {
			return ensureNZBExtension(safe)
		}
	}

	return ensureNZBExtension("novastream")
}

func parseFileNameFromContentDisposition(cd string) string {
	params := strings.Split(cd, ";")
	for _, part := range params {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(part), "filename=") {
			value := strings.TrimPrefix(part, "filename=")
			value = strings.Trim(value, "\"")
			if value != "" {
				return value
			}
		}
	}
	return ""
}

func ensureNZBExtension(name string) string {
	if strings.HasSuffix(strings.ToLower(name), ".nzb") {
		return name
	}
	return name + ".nzb"
}
