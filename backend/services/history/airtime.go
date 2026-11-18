package history

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"novastream/models"
)

// TVmaze's public API requires no credentials. Enrichment belongs here rather
// than in metadata: only the selected continue-watching episode needs a lookup.
func (s *Service) enrichContinueWatchingAirtime(ctx context.Context, title models.Title, ids map[string]string, ep *models.EpisodeReference) {
	if ep == nil {
		return
	}
	if !ep.AirTimeEstimated {
		s.applyStreamingRelease(ctx, title, ids, ep)
		return
	}
	// Only TMDB-only installations may consult this fallback. A TMDB record
	// can carry a TVDB external ID, so check its canonical provider, not TVDBID.
	// The ID check also excludes cached TVDB metadata after a key is removed.
	s.mu.RLock()
	provider, known := s.metadataService.(interface{ TVDBConfigured() bool })
	s.mu.RUnlock()
	if !known || provider.TVDBConfigured() || !strings.HasPrefix(title.ID, "tmdb:") {
		return
	}
	date, err := time.Parse("2006-01-02", ep.AirDate)
	if err != nil || date.Before(time.Now().Add(-48*time.Hour)) || date.After(time.Now().Add(7*24*time.Hour)) {
		return
	}
	imdb := continueWatchingIMDB(title, ids)
	if imdb == "" {
		return
	}
	client := s.tvmazeClient()
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	if stamp := s.resolveAirtime(ctx, client, imdb, *ep); stamp != "" {
		ep.AirDateTimeUTC = stamp
		ep.AirTimeEstimated = false
	}
}

func continueWatchingIMDB(title models.Title, ids map[string]string) string {
	imdb := strings.TrimSpace(title.IMDBID)
	if imdb == "" {
		imdb = strings.TrimSpace(ids["imdb"])
	}
	if !strings.HasPrefix(imdb, "tt") {
		return ""
	}
	return imdb
}

func (s *Service) tvmazeClient() *tvmazeAirtimeClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.airtimeClient == nil {
		s.airtimeClient = &tvmazeAirtimeClient{baseURL: "https://api.tvmaze.com", http: &http.Client{Timeout: 15 * time.Second}}
	}
	return s.airtimeClient
}

type tvmazeEpisode struct {
	Type     string `json:"type"`
	Season   int    `json:"season"`
	Number   int    `json:"number"`
	Airdate  string `json:"airdate"`
	Airtime  string `json:"airtime"`
	Airstamp string `json:"airstamp"`
}

type airtimeCacheEntry struct {
	body    []byte
	expires time.Time
}

type tvmazeAirtimeClient struct {
	baseURL string
	http    *http.Client
	mu      sync.Mutex
	cache   map[string]airtimeCacheEntry
	next    time.Time
	group   singleflight.Group
}

func (c *tvmazeAirtimeClient) airtime(ctx context.Context, imdb string, ep models.EpisodeReference) string {
	body := c.get(ctx, "/lookup/shows?imdb="+url.QueryEscape(imdb), 30*24*time.Hour)
	var show struct {
		ID int `json:"id"`
	}
	if json.Unmarshal(body, &show) != nil || show.ID <= 0 {
		return ""
	}
	body = c.get(ctx, fmt.Sprintf("/shows/%d/episodesbydate?date=%s", show.ID, url.QueryEscape(ep.AirDate)), 6*time.Hour)
	var episodes []tvmazeEpisode
	if json.Unmarshal(body, &episodes) != nil {
		return ""
	}
	return matchTVmazeAirtime(episodes, ep)
}

func matchTVmazeAirtime(episodes []tvmazeEpisode, ep models.EpisodeReference) string {
	// Providers disagree on season numbering (One Piece uses calendar years on
	// TVmaze). A unique episode on the exact date is safe; multiple episodes on
	// that date require an unambiguous season/episode match.
	var dated, numbered []tvmazeEpisode
	for _, candidate := range episodes {
		if candidate.Airdate != ep.AirDate || (candidate.Type != "" && candidate.Type != "regular") {
			continue
		}
		dated = append(dated, candidate)
		if candidate.Season == ep.SeasonNumber && candidate.Number == ep.EpisodeNumber {
			numbered = append(numbered, candidate)
		}
	}
	if len(dated) != 1 {
		dated = numbered
	}
	if len(dated) != 1 || dated[0].Airtime == "" {
		// Global streaming channels can have a midnight airstamp but no known
		// release time. Do not promote that placeholder to a precise timestamp.
		return ""
	}
	stamp, err := time.Parse(time.RFC3339, dated[0].Airstamp)
	if err != nil {
		return ""
	}
	return stamp.UTC().Format(time.RFC3339)
}

// Cache successes across profiles and home refreshes; cache misses and failures
// too. Coalesce concurrent requests and bound memory. The cache is deliberately
// independent of the much shorter continue-watching response cache.
func (c *tvmazeAirtimeClient) get(ctx context.Context, path string, ttl time.Duration) []byte {
	result, _, _ := c.group.Do(path, func() (any, error) {
		c.mu.Lock()
		if entry, ok := c.cache[path]; ok && time.Now().Before(entry.expires) {
			c.mu.Unlock()
			return entry.body, nil
		}
		// Stay below TVmaze's public rate limit, including cold home loads with
		// several returning shows. Waiting respects the short request deadline.
		wait := time.Until(c.next)
		if wait < 0 {
			wait = 0
		}
		if deadline, ok := ctx.Deadline(); ok && time.Now().Add(wait).After(deadline) {
			c.mu.Unlock()
			return []byte(nil), nil
		}
		spacing := 600 * time.Millisecond
		if strings.HasPrefix(path, "/lookup/") {
			// A successful lookup follows one HTTP redirect to the show record.
			spacing *= 2
		}
		c.next = time.Now().Add(wait + spacing)
		c.mu.Unlock()
		var body []byte
		cacheTTL := 30 * time.Second
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return []byte(nil), nil
		case <-timer.C:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
		if err == nil {
			req.Header.Set("User-Agent", "MediaStorm/1.0 (continue-watching airtimes)")
			resp, requestErr := c.http.Do(req)
			if requestErr != nil {
				log.Printf("[history] TVmaze airtime request failed path=%s: %v", path, requestErr)
			}
			if requestErr == nil {
				defer resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					data, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
					if readErr == nil && json.Valid(data) {
						body, cacheTTL = data, ttl
					}
				} else if resp.StatusCode == http.StatusNotFound {
					cacheTTL = time.Hour
					if strings.HasPrefix(path, "/lookup/") {
						cacheTTL = 24 * time.Hour
					}
				} else {
					log.Printf("[history] TVmaze airtime request failed path=%s status=%d", path, resp.StatusCode)
				}
			}
		}
		c.mu.Lock()
		if c.cache == nil {
			c.cache = make(map[string]airtimeCacheEntry)
		}
		if len(c.cache) >= 512 {
			for key, entry := range c.cache {
				if time.Now().After(entry.expires) {
					delete(c.cache, key)
				}
			}
			if len(c.cache) >= 512 {
				for key := range c.cache {
					delete(c.cache, key)
					break
				}
			}
		}
		c.cache[path] = airtimeCacheEntry{body: body, expires: time.Now().Add(cacheTTL)}
		c.mu.Unlock()
		return body, nil
	})
	body, _ := result.([]byte)
	return body
}
