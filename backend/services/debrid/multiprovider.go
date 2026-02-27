package debrid

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/models"
)

// ProviderCacheResult represents the result of checking cache on a single provider
type ProviderCacheResult struct {
	Provider  *config.DebridProviderSettings
	Client    Provider
	IsCached  bool
	TorrentID string
	Error     error
	Priority  int // Lower = higher priority (based on array index)
}

// MultiProviderService handles parallel debrid provider operations
type MultiProviderService struct {
	cfg *config.Manager
}

// NewMultiProviderService creates a new multi-provider service
func NewMultiProviderService(cfg *config.Manager) *MultiProviderService {
	return &MultiProviderService{cfg: cfg}
}

// providerEntry holds provider config and client together
type providerEntry struct {
	config   *config.DebridProviderSettings
	client   Provider
	priority int
}

// CheckCacheAcrossProviders checks all enabled providers in parallel for cache status.
// Returns the winning provider result based on the configured mode.
func (s *MultiProviderService) CheckCacheAcrossProviders(
	ctx context.Context,
	candidate models.NZBResult,
	mode config.MultiProviderMode,
) (*ProviderCacheResult, error) {
	settings, err := s.cfg.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}

	// Collect enabled providers with their priority (index = priority)
	var enabledProviders []providerEntry

	for i := range settings.Streaming.DebridProviders {
		p := &settings.Streaming.DebridProviders[i]
		if !p.Enabled || strings.TrimSpace(p.APIKey) == "" {
			continue
		}

		client, ok := GetProvider(strings.ToLower(p.Provider), p.APIKey)
		if !ok {
			log.Printf("[multi-provider] provider %q not registered, skipping", p.Provider)
			continue
		}

		// Apply provider-specific configuration if supported
		if configurable, ok := client.(Configurable); ok && p.Config != nil {
			configurable.Configure(p.Config)
		}

		enabledProviders = append(enabledProviders, providerEntry{
			config:   p,
			client:   client,
			priority: i,
		})
	}

	if len(enabledProviders) == 0 {
		return nil, fmt.Errorf("no enabled debrid providers with API keys configured")
	}

	// If only one provider, just use it directly
	if len(enabledProviders) == 1 {
		log.Printf("[multi-provider] only one provider enabled (%s), using directly", enabledProviders[0].config.Name)
		return s.checkSingleProvider(ctx, candidate, enabledProviders[0])
	}

	// Multiple providers - run parallel checks based on mode
	log.Printf("[multi-provider] checking %d providers in %s mode", len(enabledProviders), mode)

	switch mode {
	case config.MultiProviderModePreferred:
		return s.checkPreferredMode(ctx, candidate, enabledProviders)
	case config.MultiProviderModeFastest:
		fallthrough
	default:
		return s.checkFastestMode(ctx, candidate, enabledProviders)
	}
}

// checkSingleProvider checks a single provider
func (s *MultiProviderService) checkSingleProvider(
	ctx context.Context,
	candidate models.NZBResult,
	pe providerEntry,
) (*ProviderCacheResult, error) {
	result := s.checkProviderCache(ctx, candidate, pe)
	if result.Error != nil {
		return nil, result.Error
	}
	if !result.IsCached {
		return nil, fmt.Errorf("torrent not cached on %s", pe.config.Name)
	}
	return result, nil
}

// checkFastestMode returns as soon as any provider reports cached
func (s *MultiProviderService) checkFastestMode(
	ctx context.Context,
	candidate models.NZBResult,
	providers []providerEntry,
) (*ProviderCacheResult, error) {
	// Create cancellable context - cancel others once we have a winner
	checkCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	resultChan := make(chan *ProviderCacheResult, len(providers))

	// Launch parallel checks
	for _, p := range providers {
		go func(pe providerEntry) {
			result := s.checkProviderCache(checkCtx, candidate, pe)
			select {
			case resultChan <- result:
			case <-checkCtx.Done():
			}
		}(p)
	}

	// Wait for first cached result or all failures
	var firstError error
	checkedCount := 0

	for checkedCount < len(providers) {
		select {
		case result := <-resultChan:
			checkedCount++

			if result.IsCached {
				log.Printf("[multi-provider] fastest mode: %s returned CACHED first", result.Provider.Name)
				cancel() // Cancel remaining checks
				return result, nil
			}

			if result.Error != nil {
				log.Printf("[multi-provider] %s check failed: %v", result.Provider.Name, result.Error)
				if firstError == nil {
					firstError = result.Error
				}
			} else {
				log.Printf("[multi-provider] %s: not cached", result.Provider.Name)
			}

		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// No provider had cache
	if firstError != nil {
		return nil, fmt.Errorf("torrent not cached on any provider: %w", firstError)
	}
	return nil, fmt.Errorf("torrent not cached on any enabled provider")
}

// checkPreferredMode waits for all providers, returns highest priority cached result
func (s *MultiProviderService) checkPreferredMode(
	ctx context.Context,
	candidate models.NZBResult,
	providers []providerEntry,
) (*ProviderCacheResult, error) {
	var wg sync.WaitGroup
	results := make([]*ProviderCacheResult, len(providers))

	// Launch parallel checks
	for i, p := range providers {
		wg.Add(1)
		go func(idx int, pe providerEntry) {
			defer wg.Done()
			results[idx] = s.checkProviderCache(ctx, candidate, pe)
		}(i, p)
	}

	// Wait for all to complete
	wg.Wait()

	// Find highest priority (lowest index) cached result
	var bestResult *ProviderCacheResult
	var firstError error

	for i, result := range results {
		if result == nil {
			continue
		}

		providerName := providers[i].config.Name
		if result.IsCached {
			log.Printf("[multi-provider] %s: CACHED (priority %d)", providerName, result.Priority)
			if bestResult == nil || result.Priority < bestResult.Priority {
				// Clean up previous best if we're replacing it
				if bestResult != nil && bestResult.TorrentID != "" {
					log.Printf("[multi-provider] cleaning up lower-priority cached torrent from %s", bestResult.Provider.Name)
					_ = bestResult.Client.DeleteTorrent(ctx, bestResult.TorrentID)
				}
				bestResult = result
			} else {
				// This one is lower priority, clean it up
				if result.TorrentID != "" {
					log.Printf("[multi-provider] cleaning up lower-priority cached torrent from %s", providerName)
					_ = result.Client.DeleteTorrent(ctx, result.TorrentID)
				}
			}
		} else if result.Error != nil {
			log.Printf("[multi-provider] %s: error - %v", providerName, result.Error)
			if firstError == nil {
				firstError = result.Error
			}
		} else {
			log.Printf("[multi-provider] %s: not cached", providerName)
		}
	}

	if bestResult != nil {
		log.Printf("[multi-provider] preferred mode: using %s (priority %d)", bestResult.Provider.Name, bestResult.Priority)
		return bestResult, nil
	}

	if firstError != nil {
		return nil, fmt.Errorf("torrent not cached on any provider: %w", firstError)
	}
	return nil, fmt.Errorf("torrent not cached on any enabled provider")
}

// checkProviderCache adds torrent, checks status, returns result (and cleans up if not cached)
func (s *MultiProviderService) checkProviderCache(
	ctx context.Context,
	candidate models.NZBResult,
	pe providerEntry,
) *ProviderCacheResult {
	result := &ProviderCacheResult{
		Provider: pe.config,
		Client:   pe.client,
		Priority: pe.priority,
	}

	providerName := pe.config.Name
	torrentURL := strings.TrimSpace(candidate.Attributes["torrentURL"])

	// Add torrent
	var addResp *AddMagnetResult
	var err error

	if strings.HasPrefix(strings.ToLower(candidate.Link), "magnet:") {
		log.Printf("[multi-provider] %s: adding magnet", providerName)
		addResp, err = pe.client.AddMagnet(ctx, candidate.Link)
	} else if torrentURL != "" {
		log.Printf("[multi-provider] %s: downloading and uploading torrent file", providerName)
		torrentData, filename, downloadErr := s.downloadTorrentFile(ctx, torrentURL)
		if downloadErr != nil {
			result.Error = fmt.Errorf("download torrent file: %w", downloadErr)
			return result
		}
		addResp, err = pe.client.AddTorrentFile(ctx, torrentData, filename)
	} else {
		result.Error = fmt.Errorf("no magnet or torrent URL")
		return result
	}

	if err != nil {
		result.Error = fmt.Errorf("add torrent: %w", err)
		return result
	}

	result.TorrentID = addResp.ID
	log.Printf("[multi-provider] %s: torrent added with ID %s", providerName, result.TorrentID)

	// Get info and check status
	info, err := pe.client.GetTorrentInfo(ctx, result.TorrentID)
	if err != nil {
		_ = pe.client.DeleteTorrent(ctx, result.TorrentID)
		result.TorrentID = ""
		result.Error = fmt.Errorf("get torrent info: %w", err)
		return result
	}

	// Select files (required for some providers to trigger caching check)
	selection := selectMediaFiles(info.Files, buildSelectionHints(candidate, info.Filename))
	if selection == nil || len(selection.OrderedIDs) == 0 {
		_ = pe.client.DeleteTorrent(ctx, result.TorrentID)
		result.TorrentID = ""
		result.Error = fmt.Errorf("no media files found")
		return result
	}
	if selection.RejectionReason != "" {
		_ = pe.client.DeleteTorrent(ctx, result.TorrentID)
		result.TorrentID = ""
		result.Error = fmt.Errorf("%s", selection.RejectionReason)
		return result
	}

	fileSelection := strings.Join(selection.OrderedIDs, ",")
	if err := pe.client.SelectFiles(ctx, result.TorrentID, fileSelection); err != nil {
		_ = pe.client.DeleteTorrent(ctx, result.TorrentID)
		result.TorrentID = ""
		result.Error = fmt.Errorf("select files: %w", err)
		return result
	}

	// Re-check status after selection
	info, err = pe.client.GetTorrentInfo(ctx, result.TorrentID)
	if err != nil {
		_ = pe.client.DeleteTorrent(ctx, result.TorrentID)
		result.TorrentID = ""
		result.Error = fmt.Errorf("get torrent info after selection: %w", err)
		return result
	}

	result.IsCached = strings.ToLower(info.Status) == "downloaded"
	log.Printf("[multi-provider] %s: status=%s cached=%t", providerName, info.Status, result.IsCached)

	if !result.IsCached {
		// Clean up non-cached torrent
		log.Printf("[multi-provider] %s: not cached, cleaning up", providerName)
		_ = pe.client.DeleteTorrent(ctx, result.TorrentID)
		result.TorrentID = "" // Clear since we deleted it
	}

	return result
}

// downloadTorrentFile downloads a .torrent file from a URL and returns its contents.
func (s *MultiProviderService) downloadTorrentFile(ctx context.Context, torrentURL string) ([]byte, string, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, torrentURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; mediastorm/1.0)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	// Limit torrent file size to 10MB
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, "", fmt.Errorf("read response: %w", err)
	}

	// Verify it looks like a torrent file
	if len(data) < 10 || data[0] != 'd' {
		return nil, "", fmt.Errorf("invalid torrent file format")
	}

	// Extract filename
	filename := "download.torrent"
	if cd := resp.Header.Get("Content-Disposition"); cd != "" && strings.Contains(cd, "filename=") {
		parts := strings.Split(cd, "filename=")
		if len(parts) >= 2 {
			filename = strings.Trim(parts[1], `"' `)
		}
	}

	return data, filename, nil
}
