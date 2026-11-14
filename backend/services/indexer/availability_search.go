package indexer

import (
	"context"
	"errors"
	"novastream/config"
	"strings"
	"sync"
	"time"
)

// The caller must explicitly select indexers for background work. The subset
// is applied after profile permissions and becomes part of the normal cache
// identity through the filtered settings. Debrid and playback are not used.
func availabilitySearchSettings(settings config.Settings, opts SearchOptions) config.Settings {
	if !opts.AvailabilityCheck {
		return settings
	}
	names := map[string]bool{}
	for _, name := range opts.AvailabilityIndexerNames {
		names[strings.TrimSpace(name)] = true
	}
	selected := make([]config.IndexerConfig, 0, len(settings.Indexers))
	for _, indexer := range settings.Indexers {
		kind := strings.ToLower(strings.TrimSpace(indexer.Type))
		if indexer.Enabled && (kind == "" || kind == "newznab") && names[strings.TrimSpace(indexer.Name)] {
			selected = append(selected, indexer)
		}
	}
	settings.Indexers = selected
	settings.Streaming.ServiceMode = "usenet"
	settings.TorrentScrapers = nil
	return settings
}

// Interactive searches cancel background catalogue work before requesting the
// same providers. A quiet period prevents a browsing session from competing
// with a newly scheduled availability check.
func (s *Service) beginForegroundSearch(opts SearchOptions) func() {
	if opts.AvailabilityCheck {
		return func() {}
	}
	s.availabilityPriorityMu.Lock()
	s.foregroundSearches++
	s.lastForegroundSearch = time.Now()
	cancel := s.availabilityCancel
	s.availabilityCancel = nil
	s.availabilityGeneration++
	s.availabilityPriorityMu.Unlock()
	if cancel != nil {
		cancel()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			s.availabilityPriorityMu.Lock()
			s.foregroundSearches--
			s.lastForegroundSearch = time.Now()
			s.availabilityPriorityMu.Unlock()
		})
	}
}

func (s *Service) BeginAvailabilitySearch(ctx context.Context) (context.Context, func(), error) {
	s.availabilityPriorityMu.Lock()
	defer s.availabilityPriorityMu.Unlock()
	if s.foregroundSearches > 0 || (!s.lastForegroundSearch.IsZero() && time.Since(s.lastForegroundSearch) < 30*time.Second) || s.availabilityCancel != nil {
		return nil, nil, errors.New("interactive search has priority")
	}
	child, cancel := context.WithCancel(ctx)
	s.availabilityGeneration++
	generation := s.availabilityGeneration
	s.availabilityCancel = cancel
	return child, func() {
		cancel()
		s.availabilityPriorityMu.Lock()
		if generation == s.availabilityGeneration {
			s.availabilityCancel = nil
		}
		s.availabilityPriorityMu.Unlock()
	}, nil
}

// Both channels retain their one-result buffered contract. Keep the interactive
// priority active until both asynchronous source searches have settled.
func (s *Service) SearchWithScoringSplit(ctx context.Context, opts SearchOptions) (<-chan ScoredSplitSearchResult, <-chan ScoredSplitSearchResult) {
	finish := s.beginForegroundSearch(opts)
	usenet, debrid := s.searchWithScoringSplit(ctx, opts)
	if opts.AvailabilityCheck {
		finish()
		return usenet, debrid
	}
	u, d := make(chan ScoredSplitSearchResult, 1), make(chan ScoredSplitSearchResult, 1)
	var workers sync.WaitGroup
	workers.Add(2)
	forward := func(input <-chan ScoredSplitSearchResult, output chan<- ScoredSplitSearchResult) {
		defer workers.Done()
		defer close(output)
		for value := range input {
			output <- value
		}
	}
	go forward(usenet, u)
	go forward(debrid, d)
	go func() { workers.Wait(); finish() }()
	return u, d
}
