package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"novastream/config"
	"novastream/models"
	"sort"
	"strings"
	"sync"
	"time"
)

type sportsDiscoveryKey struct{}
type sportsSourceStatus struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	State   string     `json:"state"`
	RetryAt *time.Time `json:"retryAt,omitempty"`
}
type sportsDiscoveryRequest struct {
	game                models.SportsGame
	sources, categories []string
	broad               bool
	mu                  sync.Mutex
	statuses            []sportsSourceStatus
}
type providerMetadataError struct {
	Status  int
	RetryAt time.Time
}

func (e *providerMetadataError) Error() string {
	return fmt.Sprintf("provider metadata HTTP %d", e.Status)
}
func metadataResponseError(response *http.Response) error {
	retry := time.Time{}
	if response.StatusCode == 429 {
		retry = time.Now().Add(retryDelay(response.Header.Get("Retry-After"), time.Now()))
	}
	return &providerMetadataError{Status: response.StatusCode, RetryAt: retry}
}
func (d *sportsDiscoveryRequest) record(id, name string, err error) {
	row := sportsSourceStatus{ID: id, Name: name, State: "complete"}
	if err != nil {
		row.State = "unavailable"
		var upstream *providerMetadataError
		if errors.As(err, &upstream) {
			if upstream.Status == 429 {
				row.State = "rate-limited"
				row.RetryAt = &upstream.RetryAt
			}
			if upstream.Status == 401 || upstream.Status == 403 {
				row.State = "authentication-required"
			}
		}
	}
	d.mu.Lock()
	d.statuses = append(d.statuses, row)
	d.mu.Unlock()
}
func sportsDiscoveryFrom(ctx context.Context) *sportsDiscoveryRequest {
	value, _ := ctx.Value(sportsDiscoveryKey{}).(*sportsDiscoveryRequest)
	return value
}
func containsSource(ids []string, id string) bool {
	if len(ids) == 0 {
		return true
	}
	for _, v := range ids {
		if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(id)) {
			return true
		}
	}
	return false
}

// Use advertised search capabilities first. Keep generic catalogs as a fallback;
// never delete replay content from ordinary Live TV browsing.
func (h *LiveHandler) fetchSportsAddon(ctx context.Context, manifestURL, proxy string, d *sportsDiscoveryRequest, sourceFilter config.LiveTVFilterSettings) ([]LiveChannel, error) {
	base := normalizeStremioBaseURL(manifestURL)
	if _, err := h.parseRemoteURL(ctx, manifestURL); err != nil {
		return nil, err
	}
	client := h.discoveryClient(h.livePlaylistScanHTTPClient(proxy), proxy, time.Minute)
	manifest, err := fetchStremioManifest(ctx, client, base)
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(d.game.AwayTeam.Name)
	// A golf leaderboard has athletes, but addons index the tournament itself.
	if d.game.EventKind == "tournament" && strings.TrimSpace(d.game.Title) != "" {
		query = strings.TrimSpace(d.game.Title)
	}
	if query == "" {
		query = strings.TrimSpace(d.game.HomeTeam.Name)
	}
	// Racing/cycling targets provide a literal event name instead of opponents.
	if query == "" {
		query = strings.TrimSpace(d.game.Title)
	}
	var candidates []stremioCatalogDef
	for _, c := range manifest.Catalogs {
		if strings.Contains(strings.ToLower(c.ID+" "+c.Name), "replay") {
			continue
		}
		candidates = append(candidates, c)
	}
	// Prefer broad live shelves, then event sport shelves; retain all as fallback.
	sort.SliceStable(candidates, func(i, j int) bool {
		rank := func(c stremioCatalogDef) int {
			name := strings.ToLower(c.ID + " " + c.Name)
			if strings.Contains(name, "live") {
				return 0
			}
			if d.game.Sport != "" && strings.Contains(name, strings.ToLower(string(d.game.Sport))) {
				return 1
			}
			if strings.Contains(name, "network") {
				return 2
			}
			return 3
		}
		return rank(candidates[i]) < rank(candidates[j])
	})
	var channels []LiveChannel
	seen := map[string]bool{}
	appendMetas := func(c stremioCatalogDef, metas []stremioMeta) {
		for _, meta := range metas {
			if meta.ID == "" || seen[meta.ID] {
				continue
			}
			seen[meta.ID] = true
			group := c.Name
			if len(meta.Genres) > 0 {
				group = meta.Genres[0]
			}
			channels = append(channels, LiveChannel{ID: meta.ID, Name: meta.Name, URL: stremioStreamResourceURL(base, c.Type, meta.ID), Logo: meta.Poster, Group: group, TvgID: meta.ID, SportsMetadata: meta.Description})
		}
	}
	// Limit query fanout: search a broad/live catalog before specialized shelves.
	if query != "" && !d.broad {
		for _, c := range candidates {
			searchable := false
			for _, extra := range c.Extra {
				if extra.Name == "search" {
					searchable = true
				}
			}
			if !searchable {
				continue
			}
			searchCatalog := c
			searchCatalog.Extra = nil
			for _, extra := range c.Extra {
				if extra.Name != "search" {
					searchCatalog.Extra = append(searchCatalog.Extra, extra)
				}
			}
			filters, filterErr := stremioCatalogFilters(searchCatalog)
			if filterErr != nil || len(filters) != 1 {
				continue
			}
			filters[0].Set("search", query)
			endpoint := base + "/catalog/" + url.PathEscape(c.Type) + "/" + url.PathEscape(c.ID) + "/" + strings.ReplaceAll(filters[0].Encode(), "+", "%20") + ".json"
			var response stremioCatalogResponse
			if err := getStremioJSON(ctx, client, endpoint, &response); err != nil {
				var provider *providerMetadataError
				if errors.As(err, &provider) && (provider.Status == 429 || provider.Status == 401 || provider.Status == 403 || provider.Status >= 500) {
					return nil, err
				}
				break // Some addons advertise search but do not implement it.
			}
			appendMetas(c, response.Metas)
			// Existing matcher will check opponents/series; an empty search falls back.
			visible := filterSportsChannelsByScope(filterChannels(channels, sourceFilter), nil, d.categories)
			if len(selectableSportsMatches(matchGameToChannels(d.game, visible, nil, ""))) > 0 {
				return channels, nil
			}
			break
		}
	}
	var firstErr error
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return channels, err
		}
		metas, err := fetchStremioCatalog(ctx, client, base, c)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			var provider *providerMetadataError
			if errors.As(err, &provider) && (provider.Status == 429 || provider.Status == 401 || provider.Status == 403 || provider.Status >= 500) {
				break
			}
			continue
		}
		appendMetas(c, metas)
	}
	return channels, firstErr
}
