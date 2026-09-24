// Package sports fetches live scores/schedules from ESPN's public scoreboard API
// and caches them in memory + on disk, mirroring the refresh/cache shape of
// services/epg. Leagues are a fixed MVP set (MLB/NFL/NBA/NHL) - no user settings yet.
package sports

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"novastream/internal/apiusage"
	"novastream/models"
)

const (
	defaultHTTPTimeout   = 15 * time.Second
	sportsCacheDir       = "sports"
	sportsCacheFile      = "scoreboard.json"
	espnScoreboardURLFmt = "https://site.api.espn.com/apis/site/v2/sports/%s/%s/scoreboard"
	espnTeamsURLFmt      = "https://site.api.espn.com/apis/site/v2/sports/%s/%s/teams?limit=1000"
)

// League describes one supported league's ESPN sport/league slug pair.
type League struct {
	Provider             string   `json:"provider,omitempty"`
	ApplicationSport     string   `json:"applicationSport,omitempty"`
	College              bool     `json:"college,omitempty"`
	Adapter              string   `json:"adapter,omitempty"`
	ImplementationStatus string   `json:"implementationStatus,omitempty"`
	Capabilities         []string `json:"capabilities,omitempty"`
	Aliases              []string `json:"aliases,omitempty"`
	CoverageNote         string   `json:"coverageNote,omitempty"`

	ID            string
	Name          string
	Sport         string
	Slug          string
	Category      string
	EventKind     string
	SupportsTeams bool
}

// LeagueCatalog is every league this service knows how to fetch from ESPN. Not every
// catalog entry is necessarily enabled/polled - see config.Settings.Sports.EnabledLeagues
// and SetEnabledLeagueIDs. Limited to leagues that fit the home-team-vs-away-team scoreboard
// shape this package models (models.SportsGame), plus leagues served by a dedicated event
// contract (currently motorsports). Do not advertise a league here until one of those API
// contracts can actually surface it in the Sports Hub.
var LeagueCatalog = extendLeagueCatalog([]League{
	{ID: "pga", Name: "PGA Tour", Sport: "golf", Slug: "pga", Category: "golf", EventKind: "tournament"},
	{ID: "boxing", Name: "Boxing", Sport: "boxing", Category: "boxing", EventKind: "fight-card"},
	{ID: "cricket-8048", Name: "Indian Premier League", Sport: "cricket", Slug: "8048", Category: "cricket", EventKind: "matchup", SupportsTeams: true},
	{ID: "nfl", Name: "NFL", Sport: "football", Slug: "nfl", Category: "football", EventKind: "matchup", SupportsTeams: true},
	{ID: "college-football", Name: "NCAAF", Sport: "football", Slug: "college-football", Category: "football", EventKind: "matchup", SupportsTeams: true},
	{ID: "nba", Name: "NBA", Sport: "basketball", Slug: "nba", Category: "basketball", EventKind: "matchup", SupportsTeams: true},
	{ID: "mens-college-basketball", Name: "NCAAM", Sport: "basketball", Slug: "mens-college-basketball", Category: "basketball", EventKind: "matchup", SupportsTeams: true},
	{ID: "womens-college-basketball", Name: "NCAAW", Sport: "basketball", Slug: "womens-college-basketball", Category: "basketball", EventKind: "matchup", SupportsTeams: true},
	{ID: "wnba", Name: "WNBA", Sport: "basketball", Slug: "wnba", Category: "basketball", EventKind: "matchup", SupportsTeams: true},
	{ID: "mlb", Name: "MLB", Sport: "baseball", Slug: "mlb", Category: "baseball", EventKind: "matchup", SupportsTeams: true},
	{ID: "nhl", Name: "NHL", Sport: "hockey", Slug: "nhl", Category: "hockey", EventKind: "matchup", SupportsTeams: true},
	{ID: "soccer-fifa.world", Name: "FIFA World Cup", Sport: "soccer", Slug: "fifa.world", Category: "soccer", EventKind: "matchup", SupportsTeams: true},
	{ID: "soccer-fifa.wwc", Name: "FIFA Women's World Cup", Sport: "soccer", Slug: "fifa.wwc", Category: "soccer", EventKind: "matchup", SupportsTeams: true},
	{ID: "soccer-eng.1", Name: "Premier League", Sport: "soccer", Slug: "eng.1", Category: "soccer", EventKind: "matchup", SupportsTeams: true},
	{ID: "soccer-eng.2", Name: "Championship", Sport: "soccer", Slug: "eng.2", Category: "soccer", EventKind: "matchup", SupportsTeams: true},
	{ID: "soccer-esp.1", Name: "La Liga", Sport: "soccer", Slug: "esp.1", Category: "soccer", EventKind: "matchup", SupportsTeams: true},
	{ID: "soccer-ger.1", Name: "Bundesliga", Sport: "soccer", Slug: "ger.1", Category: "soccer", EventKind: "matchup", SupportsTeams: true},
	{ID: "soccer-ita.1", Name: "Serie A", Sport: "soccer", Slug: "ita.1", Category: "soccer", EventKind: "matchup", SupportsTeams: true},
	{ID: "soccer-fra.1", Name: "Ligue 1", Sport: "soccer", Slug: "fra.1", Category: "soccer", EventKind: "matchup", SupportsTeams: true},
	{ID: "soccer-usa.1", Name: "MLS", Sport: "soccer", Slug: "usa.1", Category: "soccer", EventKind: "matchup", SupportsTeams: true},
	{ID: "soccer-usa.nwsl", Name: "NWSL", Sport: "soccer", Slug: "usa.nwsl", Category: "soccer", EventKind: "matchup", SupportsTeams: true},
	{ID: "soccer-usa.nwsl.cup", Name: "NWSL Challenge Cup", Sport: "soccer", Slug: "usa.nwsl.cup", Category: "soccer", EventKind: "matchup", SupportsTeams: true},
	{ID: "soccer-uefa.champions", Name: "Champions League", Sport: "soccer", Slug: "uefa.champions", Category: "soccer", EventKind: "matchup", SupportsTeams: true},
	{ID: "soccer-uefa.europa", Name: "Europa League", Sport: "soccer", Slug: "uefa.europa", Category: "soccer", EventKind: "matchup", SupportsTeams: true},
	{ID: "soccer-uefa.europa.conf", Name: "Conference League", Sport: "soccer", Slug: "uefa.europa.conf", Category: "soccer", EventKind: "matchup", SupportsTeams: true},
	{ID: "soccer-mex.1", Name: "Liga MX", Sport: "soccer", Slug: "mex.1", Category: "soccer", EventKind: "matchup", SupportsTeams: true},
	{ID: "soccer-ned.1", Name: "Eredivisie", Sport: "soccer", Slug: "ned.1", Category: "soccer", EventKind: "matchup", SupportsTeams: true},
	{ID: "soccer-por.1", Name: "Primeira Liga", Sport: "soccer", Slug: "por.1", Category: "soccer", EventKind: "matchup", SupportsTeams: true},
	{ID: "ufc", Name: "UFC", Sport: "mma", Slug: "ufc", Category: "mma", EventKind: "fight-card"},
	{ID: "atp", Name: "ATP Tour", Sport: "tennis", Slug: "atp", Category: "tennis", EventKind: "matchup"},
	{ID: "wta", Name: "WTA Tour", Sport: "tennis", Slug: "wta", Category: "tennis", EventKind: "matchup"},
	{ID: "f1", Name: "Formula 1", Sport: "racing", Slug: "f1", Category: "racing", EventKind: "race"},
	{ID: "nascar", Name: "NASCAR Cup", Sport: "racing", Slug: "nascar-premier", Category: "racing", EventKind: "race"},
	{ID: "motogp", Name: "MotoGP", Sport: "racing", Category: "racing", EventKind: "race"},
	{ID: "indycar", Name: "IndyCar", Sport: "racing", Slug: "irl", Category: "racing", EventKind: "race"},
	{ID: "rugby-180659", Name: "Six Nations", Sport: "rugby", Slug: "180659", Category: "rugby", EventKind: "matchup", SupportsTeams: true},
	{ID: "rugby-164205", Name: "Rugby World Cup", Sport: "rugby", Slug: "164205", Category: "rugby", EventKind: "matchup", SupportsTeams: true},
	{ID: "rugby-267979", Name: "Premiership", Sport: "rugby", Slug: "267979", Category: "rugby", EventKind: "matchup", SupportsTeams: true},
	{ID: "rugby-242041", Name: "Super Rugby", Sport: "rugby", Slug: "242041", Category: "rugby", EventKind: "matchup", SupportsTeams: true},
	{ID: "rugby-270559", Name: "Top 14", Sport: "rugby", Slug: "270559", Category: "rugby", EventKind: "matchup", SupportsTeams: true},
	{ID: "rugby-league-3", Name: "NRL", Sport: "rugby-league", Slug: "3", Category: "rugby-league", EventKind: "matchup", SupportsTeams: true},
	{ID: "aso:tour", Name: "Tour de France", Sport: "cycling", Category: "cycling", EventKind: "race"},
	{ID: "aso:vuelta", Name: "La Vuelta", Sport: "cycling", Category: "cycling", EventKind: "race"},
	{ID: "aso:tour-femmes", Name: "Tour de France Femmes", Sport: "cycling", Category: "cycling", EventKind: "race"},
	{ID: "aso:paris-nice", Name: "Paris-Nice", Sport: "cycling", Category: "cycling", EventKind: "race"},
	{ID: "aso:vuelta-femenina", Name: "La Vuelta Femenina", Sport: "cycling", Category: "cycling", EventKind: "race"},
	{ID: "aso:paris-roubaix", Name: "Paris-Roubaix", Sport: "cycling", Category: "cycling", EventKind: "race"},
	{ID: "aso:paris-roubaix-femmes", Name: "Paris-Roubaix Femmes", Sport: "cycling", Category: "cycling", EventKind: "race"},
	{ID: "aso:liege-bastogne-liege", Name: "Liège-Bastogne-Liège", Sport: "cycling", Category: "cycling", EventKind: "race"},
	{ID: "aso:liege-bastogne-liege-femmes", Name: "Liège-Bastogne-Liège Femmes", Sport: "cycling", Category: "cycling", EventKind: "race"},
	{ID: "aso:fleche-wallonne", Name: "La Flèche Wallonne", Sport: "cycling", Category: "cycling", EventKind: "race"},
	{ID: "aso:fleche-wallonne-femmes", Name: "La Flèche Wallonne Femmes", Sport: "cycling", Category: "cycling", EventKind: "race"},
	{ID: "rcs:giro", Name: "Giro d’Italia", Sport: "cycling", Category: "cycling", EventKind: "race"},
	{ID: "cro:cro-race", Name: "CRO Race", Sport: "cycling", Category: "cycling", EventKind: "race"},
	{ID: "uci:road-worlds", Name: "UCI Road World Championships", Sport: "cycling", Category: "cycling", EventKind: "race"},
})

// Every supported league is enabled by default. Saved configuration can select a subset.
var defaultLeagueIDs = func() []string {
	ids := make([]string, 0, len(LeagueCatalog))
	for _, league := range LeagueCatalog {
		if league.active() {
			ids = append(ids, league.ID)
		}
	}
	return ids
}()

func defaultLeagues() []League {
	return selectLeagues(defaultLeagueIDs)
}

func selectLeagues(ids []string) []League {
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	out := make([]League, 0, len(ids))
	for _, league := range LeagueCatalog {
		if _, ok := wanted[league.ID]; (ok || wantedAll(wanted)) && league.active() {
			out = append(out, league)
		}
	}
	return out
}

// Service fetches and caches ESPN scoreboard data.
type Service struct {
	footballStandingMu sync.Mutex
	footballStandings  map[string]footballStandingEntry
	f1Archive          f1ArchiveCache
	standings          standingsCache
	motoGPExtra        motoGPEnrichmentCache
	cycling            cyclingCache
	raceDetailMu       sync.Mutex
	raceMu             sync.Mutex
	raceBoards         map[string]raceBoardEntry
	raceDetails        map[string]raceSessionEntry
	dateMu             sync.Mutex
	dated              map[string]datedEntry

	detailMu   sync.Mutex
	details    map[string]detailCacheEntry
	storageDir string
	client     *http.Client
	leagues    []League

	mu                 sync.RWMutex
	games              map[string][]models.SportsGame // league ID -> games
	teamCatalog        map[string][]models.SportsTeamRecord
	teamCatalogUpdated map[string]time.Time
	lastUpdated        time.Time
	refreshing         bool
	lastError          string
}

// NewService creates a new sports service and loads any cached scoreboard from disk.
func NewService(storageDir string) *Service {
	s := &Service{
		storageDir: storageDir,
		client: boundedSportsClient(apiusage.TrackClient(&http.Client{
			Timeout: defaultHTTPTimeout,
		}, "Sports", "ESPN sports fetch")),
		leagues:            defaultLeagues(),
		games:              make(map[string][]models.SportsGame),
		teamCatalog:        make(map[string][]models.SportsTeamRecord),
		teamCatalogUpdated: make(map[string]time.Time),
	}

	cacheDir := filepath.Join(storageDir, sportsCacheDir)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		log.Printf("[sports] failed to create cache directory: %v", err)
	}

	if err := s.loadFromDisk(); err != nil {
		log.Printf("[sports] no cached scoreboard found or error loading: %v", err)
	}

	return s
}

// EnsureTeamCatalog fetches the complete team list for every enabled league that has not
// been fetched in the last 24 hours. Scoreboards only contain teams playing in the
// current date window, so they cannot populate the Manage Team Channels screen by
// themselves (one NBA game would otherwise produce a two-team list).
//
// Successful league catalogs are retained in memory for 24 hours. Failed leagues are retried on the
// next refresh tick, while any successful results are still returned to the caller.
func (s *Service) EnsureTeamCatalog(ctx context.Context) ([]models.SportsTeamRecord, error) {
	s.mu.RLock()
	leagues := append([]League(nil), s.leagues...)
	s.mu.RUnlock()

	return s.ensureTeamCatalog(ctx, leagues)
}

// EnsureLeagueTeamCatalog includes disabled leagues without enabling scoreboard polling.
func (s *Service) EnsureLeagueTeamCatalog(ctx context.Context, id string) ([]models.SportsTeamRecord, error) {
	league, ok := s.League(id)
	if !ok {
		return nil, fmt.Errorf("unknown league %q", id)
	}
	return s.ensureTeamCatalog(ctx, []League{league})
}

func (s *Service) ensureTeamCatalog(ctx context.Context, leagues []League) ([]models.SportsTeamRecord, error) {
	var firstErr error
	var resultMu sync.Mutex
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 4)
	for _, league := range leagues {
		if !league.SupportsTeams {
			continue
		}
		s.mu.RLock()
		loaded := len(s.teamCatalog[league.ID]) > 0 && time.Since(s.teamCatalogUpdated[league.ID]) < 24*time.Hour
		s.mu.RUnlock()
		if loaded {
			continue
		}
		league := league
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				resultMu.Lock()
				firstErr = ctx.Err()
				resultMu.Unlock()
				return
			}
			defer func() { <-semaphore }()
			teams, err := s.fetchLeagueTeams(ctx, league)
			if err != nil {
				log.Printf("[sports] team catalog fetch failed for %s: %v", league.ID, err)
				resultMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				resultMu.Unlock()
				return
			}
			s.mu.Lock()
			s.teamCatalog[league.ID] = teams
			if s.teamCatalogUpdated == nil {
				s.teamCatalogUpdated = map[string]time.Time{}
			}
			s.teamCatalogUpdated[league.ID] = time.Now()
			s.mu.Unlock()
		}()
	}
	wg.Wait()

	s.mu.RLock()
	var all []models.SportsTeamRecord
	for _, league := range leagues {
		all = append(all, s.teamCatalog[league.ID]...)
	}
	s.mu.RUnlock()
	return all, firstErr
}

// Leagues returns the currently enabled/tracked leagues.
func (s *Service) Leagues() []models.SportsLeague {
	s.mu.RLock()
	defer s.mu.RUnlock()
	enabled := make(map[string]struct{}, len(s.leagues))
	for _, l := range s.leagues {
		enabled[l.ID] = struct{}{}
	}
	out := make([]models.SportsLeague, 0, len(LeagueCatalog))
	for _, l := range LeagueCatalog {
		if !l.active() {
			continue
		}
		_, isEnabled := enabled[l.ID]
		out = append(out, l.descriptor(isEnabled))
	}
	return out
}

// League returns catalog metadata even when the league is currently disabled.
func (s *Service) League(id string) (League, bool) {
	for _, league := range LeagueCatalog {
		if league.ID == id {
			return league, true
		}
	}
	return League{}, false
}

func (s *Service) ClearLogoCache() error {
	dir := filepath.Join(s.storageDir, sportsCacheDir, "logos")
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o755)
}

// GetCachedLogo keeps ESPN sports artwork available locally and only permits known CDN
// hosts, avoiding an open image proxy/SSRF surface.
func (s *Service) GetCachedLogo(ctx context.Context, rawURL string) ([]byte, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" {
		return nil, "", fmt.Errorf("invalid sports logo URL")
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "a.espncdn.com" && host != "a1.espncdn.com" && host != "secure.espncdn.com" {
		return nil, "", fmt.Errorf("sports logo host not allowed")
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(rawURL)))
	dir := filepath.Join(s.storageDir, sportsCacheDir, "logos")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", err
	}
	path := filepath.Join(dir, hash+".img")
	if data, readErr := os.ReadFile(path); readErr == nil {
		return data, http.DetectContentType(data), nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("sports logo status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (5<<20)+1))
	if err != nil || len(data) > 5<<20 {
		return nil, "", fmt.Errorf("sports logo exceeds size limit")
	}
	contentType := http.DetectContentType(data)
	if !strings.HasPrefix(contentType, "image/") {
		return nil, "", fmt.Errorf("sports logo is not an image")
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return nil, "", err
	}
	return data, contentType, nil
}

// SetEnabledLeagueIDs replaces the set of leagues this service polls/serves, filtered from
// LeagueCatalog by ID. Unknown IDs are ignored; an empty/all-unknown list falls back to
// DefaultLeagues rather than polling nothing. Safe to call concurrently with Refresh -
// intended to be called with the admin-configured config.Settings.Sports.EnabledLeagues
// before each refresh tick, so a settings change takes effect on the next poll without a
// server restart.
func (s *Service) SetEnabledLeagueIDs(ids []string) {
	enabled := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		enabled[id] = struct{}{}
	}
	next := make([]League, 0, len(LeagueCatalog))
	for _, l := range LeagueCatalog {
		if _, ok := enabled[l.ID]; (ok || wantedAll(enabled)) && l.active() {
			next = append(next, l)
		}
	}
	if len(next) == 0 && len(ids) == 0 {
		next = defaultLeagues()
	}
	s.mu.Lock()
	s.leagues = next
	s.mu.Unlock()
}

// GetStatus reports the current health of the scoreboard cache.
func (s *Service) GetStatus() models.SportsStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := 0
	for _, games := range s.games {
		count += len(games)
	}

	status := models.SportsStatus{
		Enabled:     true,
		DateQueries: true,
		GameCount:   count,
		Refreshing:  s.refreshing,
		LastError:   s.lastError,
	}
	if !s.lastUpdated.IsZero() {
		t := s.lastUpdated
		status.LastRefresh = &t
	}
	return status
}

// GetScoreboard returns cached games for a league ID ("mlb", "nfl", ...), or all
// leagues combined if league is empty.
func (s *Service) GetScoreboard(league string) []models.SportsGame {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if league != "" {
		games := s.games[league]
		out := make([]models.SportsGame, len(games))
		copy(out, games)
		return out
	}

	var all []models.SportsGame
	for _, l := range s.leagues {
		all = append(all, s.games[l.ID]...)
	}
	return all
}

// GetGame returns a single cached game by ID, if present.
func (s *Service) GetGame(id string) (models.SportsGame, bool) {
	s.mu.RLock()
	for _, games := range s.games {
		for _, g := range games {
			if g.ID == id {
				s.mu.RUnlock()
				return g, true
			}
		}
	}
	s.mu.RUnlock()
	s.dateMu.Lock()
	defer s.dateMu.Unlock()
	for _, entry := range s.dated {
		for _, g := range entry.board.Games {
			if g.ID == id {
				return g, true
			}
		}
	}
	return models.SportsGame{}, false
}

// Refresh fetches the current scoreboard for every tracked league from ESPN.
func (s *Service) Refresh(ctx context.Context) error {
	s.mu.Lock()
	if s.refreshing {
		s.mu.Unlock()
		return nil
	}
	s.refreshing = true
	s.lastError = ""
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.refreshing = false
		s.mu.Unlock()
	}()

	s.mu.RLock()
	leagues := append([]League(nil), s.leagues...)
	s.mu.RUnlock()

	// Start with the last known data for every enabled league. A refresh can
	// exhaust its shared context while workers are still waiting for a semaphore
	// slot; those workers never reach the fetch error path that normally restores
	// cached games. Seeding the result keeps transient timeouts from erasing
	// scoreboards in memory and on disk.
	nextGames := make(map[string][]models.SportsGame, len(leagues))
	s.mu.RLock()
	for _, league := range leagues {
		nextGames[league.ID] = append([]models.SportsGame(nil), s.games[league.ID]...)
	}
	s.mu.RUnlock()
	var firstErr error
	var resultMu sync.Mutex
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 4)
	for _, league := range leagues {
		league := league
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				resultMu.Lock()
				if firstErr == nil {
					firstErr = ctx.Err()
				}
				resultMu.Unlock()
				return
			}
			defer func() { <-semaphore }()
			games, err := s.fetchLeagueScoreboard(ctx, league)
			if err != nil {
				log.Printf("[sports] scoreboard fetch failed for %s: %v", league.ID, err)
				s.mu.RLock()
				if errors.Is(err, errPartialScoreboard) {
					games = mergeCoverageGames(s.games[league.ID], games)
				} else {
					games = s.games[league.ID]
				}
				s.mu.RUnlock()
				resultMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				nextGames[league.ID] = games
				resultMu.Unlock()
				return
			}
			resultMu.Lock()
			nextGames[league.ID] = games
			resultMu.Unlock()
		}()
	}
	wg.Wait()

	s.mu.Lock()
	s.games = nextGames
	s.lastUpdated = time.Now()
	if firstErr != nil {
		s.lastError = firstErr.Error()
	}
	s.mu.Unlock()

	if err := s.saveToDisk(); err != nil {
		log.Printf("[sports] failed to save scoreboard cache: %v", err)
	}

	return firstErr
}

func (s *Service) fetchLeagueScoreboard(ctx context.Context, league League) ([]models.SportsGame, error) {
	return s.fetchLeagueScoreboardDate(ctx, league, "")
}

func (s *Service) fetchLeagueScoreboardDate(ctx context.Context, league League, date string) ([]models.SportsGame, error) {
	if league.Provider == "cfl" {
		return s.fetchCFLScoreboardDate(ctx, date)
	}
	if league.ID == "boxing" {
		return s.fetchBoxingDate(ctx, date)
	}
	if league.Sport == "cycling" {
		return []models.SportsGame{}, nil
	}
	// Racing has multiple sessions and drivers; never synthesize a two-team matchup.
	if league.EventKind == "race" {
		return []models.SportsGame{}, nil
	}
	endpoint := fmt.Sprintf(espnScoreboardURLFmt, league.Sport, league.Slug)
	// Oversized limits (e.g. 1000) can silently fall back to 25 events.
	query := url.Values{"limit": {"200"}}
	if date != "" {
		query.Set("dates", strings.ReplaceAll(date, "-", ""))
	}
	// ESPN group IDs are sport-specific. Football 50 is CAA–South;
	// 90 covers Division I (FBS and FCS). Basketball uses 50 for Division I.
	switch league.ID {
	case "college-football":
		query.Set("groups", "90")
	case "mens-college-basketball", "womens-college-basketball":
		query.Set("groups", "50")
	}
	endpoint += "?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("espn scoreboard %s: status %d", league.ID, resp.StatusCode)
	}

	var payload espnScoreboardResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode espn scoreboard %s: %w", league.ID, err)
	}

	games := make([]models.SportsGame, 0, len(payload.Events))
	for _, event := range payload.Events {
		games = append(games, scoreboardEventGames(event, league)...)
	}
	if len(payload.Events) >= 200 || payload.Count > len(payload.Events) || payload.PageCount > max(1, payload.PageIndex) {
		return games, errPartialScoreboard
	}
	return games, nil
}

func (s *Service) fetchLeagueTeams(ctx context.Context, league League) ([]models.SportsTeamRecord, error) {
	if league.Provider == "cfl" {
		return s.fetchCFLTeams(ctx)
	}
	endpoint := fmt.Sprintf(espnTeamsURLFmt, league.Sport, league.Slug)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("espn teams %s: status %d", league.ID, resp.StatusCode)
	}

	var payload espnTeamsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode espn teams %s: %w", league.ID, err)
	}
	teams := espnTeamsToRecords(payload, league)
	if len(teams) == 0 {
		return nil, fmt.Errorf("espn teams %s: empty team catalog", league.ID)
	}
	return teams, nil
}

func (s *Service) saveToDisk() error {
	s.mu.RLock()
	data, err := json.Marshal(s.games)
	s.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("marshal scoreboard: %w", err)
	}

	cacheDir := filepath.Join(s.storageDir, sportsCacheDir)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}

	cachePath := filepath.Join(cacheDir, sportsCacheFile)
	tmpPath := cachePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmpPath, cachePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

func (s *Service) loadFromDisk() error {
	cachePath := filepath.Join(s.storageDir, sportsCacheDir, sportsCacheFile)
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return err
	}
	var games map[string][]models.SportsGame
	if err := json.Unmarshal(data, &games); err != nil {
		return fmt.Errorf("unmarshal scoreboard: %w", err)
	}
	s.mu.Lock()
	s.games = games
	s.mu.Unlock()
	return nil
}
