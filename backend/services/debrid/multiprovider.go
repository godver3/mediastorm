package debrid

import (
	"context"
	"fmt"
	"log"
	"strings"
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
	cfg     *config.Manager
	circuit *providerResolutionCircuit
}

// NewMultiProviderService creates a new multi-provider service
func NewMultiProviderService(cfg *config.Manager, circuit *providerResolutionCircuit) *MultiProviderService {
	return &MultiProviderService{cfg: cfg, circuit: circuit}
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
) (*ProviderCacheResult, error) {
	settings, err := s.cfg.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	settings = config.FilterSettingsForProfile(settings, strings.TrimSpace(candidate.Attributes["profileId"]))

	// Collect enabled providers with their priority (index = priority)
	var enabledProviders []providerEntry
	var restrictedErr error

	for i := range settings.Streaming.DebridProviders {
		p := &settings.Streaming.DebridProviders[i]
		if !p.Enabled || strings.TrimSpace(p.APIKey) == "" {
			continue
		}
		if err := realDebridRestrictionForCandidate(settings, *p, candidate); err != nil {
			log.Printf("[multi-provider] %s; skipping provider for title=%q", err, strings.TrimSpace(candidate.Title))
			if restrictedErr == nil {
				restrictedErr = err
			}
			continue
		}

		client, err := NewProviderFromSettings(*p)
		if err != nil {
			log.Printf("[multi-provider] skipping provider %q: %v", p.Name, err)
			continue
		}
		client = s.circuit.wrap(client)

		enabledProviders = append(enabledProviders, providerEntry{
			config:   p,
			client:   client,
			priority: i,
		})
	}

	if len(enabledProviders) == 0 {
		if restrictedErr != nil {
			return nil, restrictedErr
		}
		return nil, fmt.Errorf("no enabled debrid providers with API keys configured")
	}

	// If only one provider, just use it directly
	if len(enabledProviders) == 1 {
		log.Printf("[multi-provider] only one provider enabled (%s), using directly", enabledProviders[0].config.Name)
		return s.checkSingleProvider(ctx, candidate, enabledProviders[0])
	}

	// Multiple providers always race. Cache availability is the union of every
	// enabled provider, so the first cached response is immediately playable.
	log.Printf("[multi-provider] checking %d providers concurrently", len(enabledProviders))
	return s.checkFastestMode(ctx, candidate, enabledProviders)
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
				log.Printf("[multi-provider] %s returned CACHED first", result.Provider.Name)
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
		return nil, fmt.Errorf("torrent cache check failed: %w", firstError)
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
		addResp, err = addMagnetUnlessUncached(ctx, pe.client, candidate.Link)
	} else if torrentURL != "" {
		log.Printf("[multi-provider] %s: resolving torrent source from %s", providerName, safeURLForLog(torrentURL))
		source, downloadErr := downloadTorrentSource(ctx, torrentURL, 30*time.Second)
		if downloadErr != nil {
			result.Error = fmt.Errorf("download torrent file: %w", downloadErr)
			return result
		}
		addResp, err = source.add(ctx, pe.client)
		if source.magnet != "" {
			candidate.Link = source.magnet
		}
	} else {
		result.Error = fmt.Errorf("no magnet or torrent URL")
		return result
	}

	if err != nil {
		result.Error = fmt.Errorf("add torrent: %w", err)
		return result
	}

	result.TorrentID = addResp.ID
	if strings.HasPrefix(strings.ToLower(candidate.Link), "magnet:") {
		RegisterMagnet(providerName, result.TorrentID, candidate.Link)
	}
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
		RememberUncached(pe.client.Name(), extractInfoHashFromMagnet(candidate.Link), info.Hash)
		_ = pe.client.DeleteTorrent(ctx, result.TorrentID)
		result.TorrentID = "" // Clear since we deleted it
	}

	return result
}
