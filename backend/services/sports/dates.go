package sports

import (
	"context"
	"errors"
	"fmt"
	"novastream/models"
	"sort"
	"strings"
	"sync"
	"time"
)

type LeagueAvailability struct {
	Partial     bool      `json:"partial,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	League      string    `json:"league"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Stale       bool      `json:"stale"`
	Unavailable bool      `json:"unavailable"`
}
type DatedScoreboard struct {
	Leagues   []LeagueAvailability `json:"leagues"`
	Games     []models.SportsGame  `json:"games"`
	UpdatedAt time.Time            `json:"updatedAt"`
	Stale     bool                 `json:"stale"`
	Date      string               `json:"date"`
}
type datedEntry struct {
	board    DatedScoreboard
	expires  time.Time
	inFlight *datedFlight
}

type datedFlight struct {
	done  chan struct{}
	board DatedScoreboard
	err   error
}

const datedCacheLimit = 4096

func ValidateScoreboardDate(date string, now time.Time) error {
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		return err
	}
	today := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	// Two UTC days accommodates yesterday/tomorrow in the viewer's local zone.
	if parsed.Before(today.AddDate(0, 0, -2)) || parsed.After(today.AddDate(0, 0, 2)) {
		return fmt.Errorf("date outside schedule window")
	}
	return nil
}

func (s *Service) GetDatedScoreboard(ctx context.Context, date, leagueID string) (DatedScoreboard, error) {
	if err := ValidateScoreboardDate(date, time.Now()); err != nil {
		return DatedScoreboard{}, err
	}
	s.mu.RLock()
	leagues := append([]League(nil), s.leagues...)
	s.mu.RUnlock()
	// Only enabled matchup competitions enter this scoreboard; races use the event contract.
	selected := []League{}
	ids := []string{}
	for _, l := range leagues {
		if supportsHubLeague(l.ID) && (leagueID == "" || leagueID == l.ID) {
			selected = append(selected, l)
			ids = append(ids, l.ID)
		}
	}
	if leagueID != "" && len(selected) == 0 {
		return DatedScoreboard{}, fmt.Errorf("league not enabled")
	}
	key := "board:" + date + ":" + strings.Join(ids, ",")
	s.dateMu.Lock()
	if s.dated == nil {
		s.dated = map[string]datedEntry{}
	}
	cached, exists := s.dated[key]
	if cached.inFlight != nil {
		flight := cached.inFlight
		s.dateMu.Unlock()
		select {
		case <-ctx.Done():
			return DatedScoreboard{}, ctx.Err()
		case <-flight.done:
			return flight.board, flight.err
		}
	}
	if exists && time.Now().Before(cached.expires) {
		s.dateMu.Unlock()
		return cached.board, nil
	}
	flight := &datedFlight{done: make(chan struct{})}
	pending := cached
	pending.inFlight = flight
	s.dated[key] = pending
	s.dateMu.Unlock()

	go func() {
		board, err := s.loadDatedScoreboard(context.WithoutCancel(ctx), date, key, selected, cached, exists)
		s.dateMu.Lock()
		if err != nil {
			if exists {
				s.dated[key] = cached
			} else {
				delete(s.dated, key)
			}
		}
		flight.board, flight.err = board, err
		entry := s.dated[key]
		entry.inFlight = nil
		if err == nil {
			s.dated[key] = entry
		}
		close(flight.done)
		s.dateMu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return DatedScoreboard{}, ctx.Err()
	case <-flight.done:
		return flight.board, flight.err
	}
}

// Cache locks never cover provider requests. Pending work is shared only for
// the same selected-league/date key; every caller, including the initiator, is
// only a waiter. Shared work has its own 15-second bound and survives any one
// caller cancellation.
func (s *Service) loadDatedScoreboard(ctx context.Context, date, key string, selected []League, cached datedEntry, exists bool) (DatedScoreboard, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	type result struct {
		games []models.SportsGame
		err   error
	}
	results := make([]result, len(selected))
	slots := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, l := range selected {
		wg.Add(1)
		go func(i int, l League) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				results[i].err = ctx.Err()
				return
			}
			defer func() { <-slots }()
			results[i].games, results[i].err = s.fetchDatedLeagueWithOverlap(ctx, l, date)
		}(i, l)
	}
	wg.Wait()

	s.dateMu.Lock()
	defer s.dateMu.Unlock()
	board := DatedScoreboard{Games: []models.SportsGame{}, Leagues: []LeagueAvailability{}, Date: date, UpdatedAt: time.Now()}
	successful := 0
	for i, r := range results {
		leagueKey := "league:" + date + ":" + selected[i].ID
		previous, hasPrevious := s.dated[leagueKey]
		availability := LeagueAvailability{League: selected[i].ID, UpdatedAt: board.UpdatedAt}
		if errors.Is(r.err, errPartialScoreboard) {
			successful++
			availability.Partial = true
			availability.Stale = true
			availability.Reason = r.err.Error()
			board.Stale = true
			games := mergeCoverageGames(previous.board.Games, r.games)
			board.Games = append(board.Games, games...)
			s.dated[leagueKey] = datedEntry{board: DatedScoreboard{Games: games, UpdatedAt: board.UpdatedAt}, expires: time.Now().Add(30 * time.Second)}
		} else if r.err != nil {
			availability.Stale = true
			board.Stale = true
			if hasPrevious {
				board.Games = append(board.Games, previous.board.Games...)
				availability.UpdatedAt = previous.board.UpdatedAt
			} else {
				availability.Unavailable = true
				availability.UpdatedAt = time.Time{}
			}
		} else {
			successful++
			board.Games = append(board.Games, r.games...)
			s.dated[leagueKey] = datedEntry{board: DatedScoreboard{Games: r.games, UpdatedAt: board.UpdatedAt}, expires: time.Now().Add(30 * time.Second)}
		}
		board.Leagues = append(board.Leagues, availability)
	}
	if successful == 0 && len(selected) > 0 {
		if exists {
			board.UpdatedAt = cached.board.UpdatedAt
		} else if len(board.Games) == 0 {
			return DatedScoreboard{}, fmt.Errorf("sports scoreboards unavailable")
		}
	}
	sort.SliceStable(board.Games, func(i, j int) bool { return board.Games[i].StartTime.Before(board.Games[j].StartTime) })
	for len(s.dated) >= datedCacheLimit {
		oldestKey := ""
		var oldest time.Time
		for k, entry := range s.dated {
			if entry.inFlight != nil {
				continue
			}
			if oldestKey == "" || entry.expires.Before(oldest) {
				oldestKey = k
				oldest = entry.expires
			}
		}
		if oldestKey == "" {
			break
		}
		delete(s.dated, oldestKey)
	}
	ttl := 30 * time.Second
	if board.Stale {
		ttl = 15 * time.Second
	}
	s.dated[key] = datedEntry{board: board, expires: time.Now().Add(ttl), inFlight: s.dated[key].inFlight}
	return board, nil
}

func supportsHubLeague(id string) bool {
	for _, league := range LeagueCatalog {
		if league.ID == id {
			return league.EventKind == "matchup" || league.EventKind == "fight-card" || league.EventKind == "tournament"
		}
	}
	return false
}

// Cricket date filters may index only the opening day of a multi-day Test.
// Merge one default series scoreboard, never a request per preceding day.
func (s *Service) fetchDatedLeagueWithOverlap(ctx context.Context, league League, date string) ([]models.SportsGame, error) {
	games, datedErr := s.fetchLeagueScoreboardDate(ctx, league, date)
	if league.Sport != "cricket" || date == "" {
		return games, datedErr
	}
	day, err := time.Parse("2006-01-02", date)
	if err != nil {
		return games, datedErr
	}
	current, currentErr := s.fetchLeagueScoreboardDate(ctx, league, "")
	if currentErr != nil && !errors.Is(currentErr, errPartialScoreboard) {
		return games, datedErr
	}
	seen := make(map[string]bool, len(games))
	for _, game := range games {
		seen[game.ID] = true
	}
	// With no client timezone here, retain the union of local-day intervals
	// across real UTC offsets. The caller/UI applies its final local-day filter.
	from, to := day.Add(-14*time.Hour), day.Add(38*time.Hour)
	added := false
	for _, game := range current {
		if game.ID == "" || seen[game.ID] || game.StartTime.IsZero() {
			continue
		}
		end := game.EndTime
		if end.IsZero() || end.Before(game.StartTime) {
			end = game.StartTime
		}
		if game.StartTime.Before(to) && !end.Before(from) {
			games = append(games, game)
			seen[game.ID], added = true, true
		}
	}
	if errors.Is(datedErr, errPartialScoreboard) || errors.Is(currentErr, errPartialScoreboard) {
		return games, errPartialScoreboard
	}
	if datedErr != nil {
		if added {
			return games, errPartialScoreboard
		}
		return games, datedErr
	}
	return games, nil
}
