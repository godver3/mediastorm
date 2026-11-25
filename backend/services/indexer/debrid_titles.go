package indexer

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"novastream/models"
	"novastream/services/debrid"
)

// Search the budgeted language titles independently of whether another title
// returns results. Keep the caller's complete alias set for identity filtering.
func (s *Service) searchDebridTitles(ctx context.Context, base debrid.SearchOptions, opts SearchOptions, titles []string) ([]models.NZBResult, error) {
	queries := combineFilterTitles([]string{base.Query}, buildTitleSearchQueries(opts, debrid.ParseQueryWithYear(opts.Query, opts.Year), titles))
	if len(queries) <= 1 {
		return s.debrid.Search(ctx, base)
	}
	type batch struct {
		results []models.NZBResult
		err     error
	}
	batches := make([]batch, len(queries))
	var wg sync.WaitGroup
	for i, query := range queries {
		wg.Go(func() {
			search := base
			search.Query = query
			search.FilterQuery = base.Query
			// IMDB-based scrapers return the same streams for every title, and
			// repeating them can trip provider rate limits (AIOStreams).
			search.SkipIMDBScrapers = i > 0 && strings.TrimSpace(base.IMDBID) != ""
			batches[i].results, batches[i].err = s.debrid.Search(ctx, search)
		})
	}
	wg.Wait()
	var results []models.NZBResult
	var lastErr error
	successes := 0
	seen := make(map[string]bool)
	for _, batch := range batches {
		if batch.err != nil {
			lastErr = batch.err
			continue
		}
		successes++
		for _, result := range batch.results {
			// Preserve distinct provider URLs and torrent files, even when their
			// release titles or infohashes match.
			key := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s", result.Indexer, result.GUID, result.DownloadURL, result.Link, result.Attributes["fileIndex"], result.Attributes["debridProvider"], result.Attributes["provider"])
			if result.GUID == "" && result.DownloadURL == "" && result.Link == "" {
				identity := usenetResultDedupKey(result)
				if identity == "" {
					key = ""
				} else {
					key += "|" + identity
				}
			}
			if key != "" && seen[key] {
				continue
			}
			seen[key] = true
			results = append(results, result)
		}
	}
	if successes == 0 || (len(results) == 0 && lastErr != nil) {
		return nil, lastErr
	}
	if lastErr != nil {
		results = cloneNZBResults(results)
		for i := range results {
			if results[i].Attributes == nil {
				results[i].Attributes = make(map[string]string)
			}
			results[i].Attributes["searchIncomplete"] = "true"
		}
	}
	return results, nil
}
