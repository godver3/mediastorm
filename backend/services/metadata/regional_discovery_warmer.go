package metadata

import (
	"context"
	"log"
	"strings"
	"time"
)

type RegionalDiscoveryWarmShelf struct {
	Language string
	Options  TMDBListOptions
}

// SetRegionalDiscoveryProvider supplies only public regional catalogue queries;
// watch history, indexers and playback preparation are not used by this worker.
func (s *Service) SetRegionalDiscoveryProvider(provider func() []RegionalDiscoveryWarmShelf) {
	s.regionalDiscoveryFn = provider
}

func uniqueRegionalWarmRequests(input []RegionalDiscoveryWarmShelf) []RegionalDiscoveryWarmShelf {
	seen := map[string]bool{}
	result := []RegionalDiscoveryWarmShelf{}
	for _, entry := range input {
		entry.Language = strings.TrimSpace(entry.Language)
		key := cacheKey(entry.Language, entry.Options.SourceType, entry.Options.SourceID, entry.Options.MediaType, entry.Options.DiscoverQuery, entry.Options.Sort)
		if seen[key] {
			continue
		}
		seen[key] = true
		entry.Options.Limit = 20
		entry.Options.Offset = 0
		entry.Options.DeferArtwork = true
		result = append(result, entry)
		if len(result) == 32 {
			break
		}
	}
	return result
}

func (s *Service) runRegionalDiscoveryWarmer(stop <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	warm := func() {
		if s.regionalDiscoveryFn == nil {
			return
		}
		requests := uniqueRegionalWarmRequests(s.regionalDiscoveryFn())
		cycle, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		for _, entry := range requests {
			if cycle.Err() != nil {
				return
			}
			service := s
			if entry.Language != "" {
				service = s.WithLanguage(entry.Language)
			}
			if _, _, err := service.GetTMDBList(cycle, entry.Options); err != nil {
				log.Printf("[metadata] regional discovery warm-up deferred: %v", err)
			}
		}
	}
	warm()
	// Refresh before the six-hour regional cache expires; ticking at the same
	// interval found every row still fresh and let them all expire minutes later.
	ticker := time.NewTicker(5 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			warm()
		}
	}
}
