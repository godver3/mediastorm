package indexer

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"novastream/config"
	"novastream/models"
)

// indexerCallTTL is how long a successful indexer response answers identical
// calls. Several paths often search the same item within seconds of each
// other (a prequeue, a release list, a next-episode prefetch); the result
// cache above them is skipped whenever any provider fails, so without this
// every one of them repeated every indexer call.
const indexerCallTTL = time.Minute

// indexerCallDedup shares identical indexer API calls: calls in flight are
// joined, and a successful response is reused for indexerCallTTL. Failures
// are shared with the callers already waiting but never reused. The zero
// value is ready to use.
type indexerCallDedup struct {
	group singleflight.Group
	mu    sync.Mutex
	done  map[string]indexerCallResult
}

type indexerCallResult struct {
	results []models.NZBResult
	at      time.Time
}

// indexerCallKey identifies a call by everything fetchTorznab reads.
func indexerCallKey(idx config.IndexerConfig, opts SearchOptions) string {
	return strings.Join([]string{
		idx.Name, idx.URL, idx.APIKey, idx.Type, idx.Categories, idx.UserAgent,
		opts.usenetSearchType, fmt.Sprint(opts.usenetUseID), fmt.Sprint(opts.TVDBID), opts.IMDBID,
		opts.usenetTitle, opts.Query, opts.usenetSeason, opts.usenetEpisode,
		strings.Join(opts.Categories, ","), opts.MediaType,
		fmt.Sprint(opts.EpisodeReleased), fmt.Sprint(opts.disableSeasonFallback),
	}, "\x00")
}

func (d *indexerCallDedup) recent(key string, now time.Time) ([]models.NZBResult, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	entry, ok := d.done[key]
	if !ok {
		return nil, false
	}
	if now.Sub(entry.at) > indexerCallTTL {
		delete(d.done, key)
		return nil, false
	}
	return entry.results, true
}

func (d *indexerCallDedup) remember(key string, results []models.NZBResult, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.done == nil {
		d.done = map[string]indexerCallResult{}
	}
	for k, entry := range d.done {
		if now.Sub(entry.at) > indexerCallTTL {
			delete(d.done, k)
		}
	}
	d.done[key] = indexerCallResult{results: results, at: now}
}

// sharedIndexerCallTimeout bounds a shared call whose starting caller set no
// deadline.
const sharedIndexerCallTimeout = 30 * time.Second

// sharedCallContext is the context of a shared call: it keeps the starting
// caller's deadline but not its cancellation, so one caller going away does
// not fail the call for the others waiting on it.
func sharedCallContext(ctx context.Context) (context.Context, context.CancelFunc) {
	detached := context.WithoutCancel(ctx)
	if deadline, ok := ctx.Deadline(); ok {
		return context.WithDeadline(detached, deadline)
	}
	return context.WithTimeout(detached, sharedIndexerCallTimeout)
}

// searchTorznab searches one Newznab/Torznab indexer, sharing identical calls
// (see indexerCallDedup).
func (s *Service) searchTorznab(ctx context.Context, idx config.IndexerConfig, opts SearchOptions) ([]models.NZBResult, error) {
	// Background work must stop its actual HTTP request when interactive work
	// arrives. The ordinary shared call deliberately detaches cancellation.
	if opts.AvailabilityCheck {
		return s.fetchTorznab(ctx, idx, opts)
	}
	key := indexerCallKey(idx, opts)
	if results, ok := s.indexerCalls.recent(key, time.Now()); ok {
		return cloneNZBResults(results), nil
	}
	ch := s.indexerCalls.group.DoChan(key, func() (any, error) {
		callCtx, cancel := sharedCallContext(ctx)
		defer cancel()
		results, err := s.fetchTorznab(callCtx, idx, opts)
		if err == nil {
			s.indexerCalls.remember(key, results, time.Now())
		}
		return results, err
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		return cloneNZBResults(res.Val.([]models.NZBResult)), nil
	}
}
