package sourceavailability

import (
	"context"
	"errors"
	"novastream/config"
	"novastream/models"
	"novastream/services/badstreams"
	"novastream/services/indexer"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

type ScopeFactory func(profileID, clientID string) (func(titleID string) (Selection, error), error)
type searcher interface {
	BeginAvailabilitySearch(context.Context) (context.Context, func(), error)
	SearchWithScoringSplit(context.Context, indexer.SearchOptions) (<-chan indexer.ScoredSplitSearchResult, <-chan indexer.ScoredSplitSearchResult)
}

type request struct {
	Episode                    *models.SeriesEpisode
	ProfileID, ClientID, Scope string
	AudioLanguage              string
	Title                      models.Title
	LastSeen                   time.Time
}
type Service struct {
	episodeViews    map[string]episodeView
	activeEpisodeID string
	cancelEpisode   context.CancelFunc
	Store           *Store
	manager         *config.Manager
	search          searcher
	scope           ScopeFactory
	reports         *badstreams.Service
	mu              sync.Mutex
	pending         map[string]request
	now             func() time.Time
}

func NewService(store *Store, manager *config.Manager, search searcher, scope ScopeFactory, reports ...*badstreams.Service) *Service {
	service := &Service{Store: store, manager: manager, search: search, scope: scope, pending: map[string]request{}, now: time.Now}
	if len(reports) > 0 {
		service.reports = reports[0]
	}
	return service
}

func (s *Service) Revision(profileID, clientID string) string {
	cfg, err := s.manager.Load()
	if err != nil || !cfg.SourceAvailability.Enabled || s.scope == nil {
		return "disabled"
	}
	resolve, err := s.scope(profileID, clientID)
	if err != nil {
		return "unknown"
	}
	selection, err := resolve("")
	if err != nil {
		return "unknown"
	}
	return selection.Key + ":" + s.Store.Revision(profileID) + ":" + s.reports.RevisionFor(badstreams.PlaybackContext{ProfileID: profileID, ClientID: clientID})
}

// AnnotateTitles performs cache lookups only. A bounded worker owns provider
// requests; the home/search response never waits for an indexer.
func (s *Service) AnnotateTitles(profileID, clientID string, titles []models.Title) []models.Title {
	if profileID == "" || s.scope == nil {
		return titles
	}
	cfg, err := s.manager.Load()
	if err != nil || !cfg.SourceAvailability.Enabled {
		return titles
	}
	scope, err := s.scope(profileID, clientID)
	if err != nil {
		return titles
	}
	result := append([]models.Title(nil), titles...)
	for i := range result {
		title := &result[i]
		if title.ID == "" || (title.MediaType != "movie" && title.MediaType != "series" && title.MediaType != "tv") {
			continue
		}
		identity, err := scope(title.ID)
		if err != nil {
			continue
		}
		title.CardSubtitle = cleanSubtitle(title.CardSubtitle)
		value := s.availability(profileID, clientID, title.ID, identity)
		title.SourceAvailability = &value
		if value.State == "found" || value.State == "not_found" {
			label := "Source candidate found"
			if value.State == "not_found" {
				label = "No matching source in checked indexers"
			}
			if title.CardSubtitle != "" {
				label += " · " + title.CardSubtitle
			}
			title.CardSubtitle = label
			title.ForceTitleOverlay = true
		}
		s.enqueue(request{ProfileID: profileID, ClientID: clientID, Scope: identity.Key, AudioLanguage: identity.AudioLanguage, Title: titles[i], LastSeen: s.now()})
	}
	return result
}

func (s *Service) enqueue(item request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enqueueLocked(item)
}

func (s *Service) enqueueLocked(item request) {
	id := requestID(item)
	s.pending[id] = item
	if len(s.pending) > MaxRecords {
		oldest := ""
		var seen time.Time
		for candidate, entry := range s.pending {
			if candidate == id {
				continue
			}
			if oldest == "" || entry.LastSeen.Before(seen) {
				oldest = candidate
				seen = entry.LastSeen
			}
		}
		if oldest != "" {
			delete(s.pending, oldest)
		}
	}
}

func (s *Service) ObservePlaybackStart(profileID, clientID, titleID, mediaType string) func(bool) {
	cfg, err := s.manager.Load()
	// A prepared episode says nothing about the other episodes of a series.
	if err != nil || !cfg.SourceAvailability.Enabled || s.scope == nil || mediaType != "movie" {
		return func(bool) {}
	}
	scope, err := s.scope(profileID, clientID)
	if err != nil {
		return func(bool) {}
	}
	identity, err := scope(titleID)
	if err != nil {
		return func(bool) {}
	}
	revision := s.reports.RevisionFor(s.reportContext(profileID, clientID, titleID, identity))
	return func(prepared bool) {
		if !prepared || !s.scopeStillMatches(profileID, clientID, titleID, identity.Key) || revision != s.reports.RevisionFor(s.reportContext(profileID, clientID, titleID, identity)) {
			return
		}
		current, evidence := s.Store.getObservation(profileID, titleID, identity.Key)
		if evidence != nil && evidence.Kind == "search" && s.evaluate(current, evidence, s.reportContext(profileID, clientID, titleID, identity)).State == "found" {
			return
		}
		checked := s.now().UTC()
		expires := checked.Add(time.Duration(cfg.SourceAvailability.EffectiveRefreshHours()) * time.Hour)
		_ = s.Store.putObservation(profileID, titleID, identity.Key, models.SourceAvailability{State: "found", CheckedAt: &checked, ExpiresAt: &expires, Reason: "prepared_source"}, &observation{Version: 1, Kind: "prepared", ReportRevision: revision})
	}
}

func (s *Service) scopeStillMatches(profile, client, title, identity string) bool {
	cfg, err := s.manager.Load()
	if err != nil || !cfg.SourceAvailability.Enabled {
		return false
	}
	resolve, err := s.scope(profile, client)
	if err != nil {
		return false
	}
	selection, err := resolve(title)
	return err == nil && selection.Key == identity
}

func (s *Service) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.checkOne(ctx)
			}
		}
	}()
}

func selectedIndexers(settings config.Settings, profileID string) []string {
	allowed := map[string]bool{}
	for _, name := range settings.SourceAvailability.IndexerNames {
		allowed[strings.TrimSpace(name)] = true
	}
	visible := config.FilterSettingsForProfile(settings, profileID)
	result := []string{}
	for _, provider := range visible.Indexers {
		kind := strings.ToLower(strings.TrimSpace(provider.Type))
		if provider.Enabled && (kind == "" || kind == "newznab") && allowed[strings.TrimSpace(provider.Name)] {
			result = append(result, provider.Name)
		}
	}
	sort.Strings(result)
	return result
}

// checkOne limits total title checks per six-to-24-hour window and never
// downloads an NZB or resolves playback. Incomplete searches remain unknown.
func (s *Service) checkOne(ctx context.Context) {
	if s.search == nil || s.scope == nil {
		return
	}
	settings, err := s.manager.Load()
	if err != nil || !settings.SourceAvailability.Enabled || len(settings.SourceAvailability.IndexerNames) == 0 {
		return
	}
	now := s.now()
	interval := time.Duration(settings.SourceAvailability.EffectiveRefreshHours()) * time.Hour
	s.mu.Lock()
	var chosen request
	chosenID := ""
	for id, item := range s.pending {
		if now.Sub(item.LastSeen) > 24*time.Hour {
			delete(s.pending, id)
			continue
		}
		if item.Episode != nil && s.neededUntilLocked(id).IsZero() {
			delete(s.pending, id)
			continue
		}
		value, evidence := s.Store.getObservation(item.ProfileID, item.cacheID(), item.Scope)
		legacy := s.reports != nil && evidence == nil && value.CheckedAt != nil
		changedPreparation := evidence != nil && evidence.Kind == "prepared" && evidence.ReportRevision != s.reports.RevisionFor(badstreams.PlaybackContext{
			ProfileID: item.ProfileID, ClientID: item.ClientID, TitleID: item.Title.ID, AudioLanguage: item.AudioLanguage,
		})
		if value.ExpiresAt != nil && value.ExpiresAt.After(now) && !legacy && !changedPreparation {
			continue
		}
		if chosenID == "" || item.LastSeen.After(chosen.LastSeen) {
			chosenID = id
			chosen = item
		}
	}
	s.mu.Unlock()
	if chosenID == "" {
		return
	}
	scope, err := s.scope(chosen.ProfileID, chosen.ClientID)
	if err != nil {
		s.mu.Lock()
		delete(s.pending, chosenID)
		s.mu.Unlock()
		return
	}
	current, err := scope(chosen.Title.ID)
	if err != nil {
		return
	}
	if chosen.Episode != nil {
		current = episodeSelection(current, chosen.Episode, chosen.Title)
	}
	if current.Key != chosen.Scope {
		s.mu.Lock()
		delete(s.pending, chosenID)
		s.mu.Unlock()
		if chosen.Episode != nil {
			return
		}
		chosen.Scope = current.Key
		chosen.AudioLanguage = current.AudioLanguage
		s.enqueue(chosen)
		return
	}
	providers := selectedIndexers(settings, chosen.ProfileID)
	if len(providers) == 0 {
		s.mu.Lock()
		delete(s.pending, chosenID)
		s.mu.Unlock()
		return
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	if chosen.Episode != nil {
		s.mu.Lock()
		until := s.neededUntilLocked(chosenID)
		if until.IsZero() {
			s.mu.Unlock()
			cancel()
			return
		}
		cancel()
		bounded, cancel = context.WithDeadline(ctx, minTime(time.Now().Add(10*time.Second), until))
		s.activeEpisodeID, s.cancelEpisode = chosenID, cancel
		s.mu.Unlock()
		defer func() { s.mu.Lock(); s.activeEpisodeID = ""; s.cancelEpisode = nil; s.mu.Unlock() }()
	}
	defer cancel()
	work, release, err := s.search.BeginAvailabilitySearch(bounded)
	if err != nil {
		return
	}
	defer release()
	reserved, err := s.Store.reserveCheck(now, interval, settings.SourceAvailability.EffectiveMaxTitles())
	if err != nil || !reserved {
		return
	}
	media := chosen.Title.MediaType
	if media == "tv" {
		media = "series"
	}
	u, d := s.search.SearchWithScoringSplit(work, episodeSearchOptions(chosen, indexer.SearchOptions{AvailabilityCheck: true, AvailabilityIndexerNames: providers, TitleID: chosen.Title.ID, Query: chosen.Title.Name, MediaType: media, Year: chosen.Title.Year, IMDBID: chosen.Title.IMDBID, TVDBID: chosen.Title.TVDBID, UserID: chosen.ProfileID, ClientID: chosen.ClientID, OriginalLanguage: chosen.Title.Language, AlternateTitles: []string{chosen.Title.OriginalName}, MaxResults: 0}))
	result, err := receiveSearch(work, u, d)
	latest, loadErr := s.manager.Load()
	if loadErr != nil || !slices.Equal(providers, selectedIndexers(latest, chosen.ProfileID)) || !s.requestScopeStillMatches(chosen) {
		return
	}
	checked := s.now().UTC()
	expires := checked.Add(interval)
	value := models.SourceAvailability{State: "unknown", CheckedAt: &checked, ExpiresAt: &expires, Providers: providers, Reason: "search_incomplete"}
	evidence := searchObservation(result, err == nil && result.Err == nil && !result.Incomplete && !result.Disabled)
	if chosen.Episode != nil {
		if !s.episodeNeeded(chosenID) || work.Err() != nil {
			return
		}
		value = s.evaluateEpisode(value, evidence, chosen, current)
	} else {
		value = s.evaluate(value, evidence, s.reportContext(chosen.ProfileID, chosen.ClientID, chosen.Title.ID, current))
	}
	if value.State == "unknown" {
		retry := checked.Add(5 * time.Minute)
		value.ExpiresAt = &retry
	}
	_ = s.Store.putObservation(chosen.ProfileID, chosen.cacheID(), chosen.Scope, value, evidence)
}

func cleanSubtitle(subtitle string) string {
	for _, label := range []string{"Source candidate found", "No matching source in checked indexers"} {
		if subtitle == label {
			return ""
		}
		subtitle = strings.TrimPrefix(subtitle, label+" · ")
	}
	return subtitle
}

func receiveSearch(ctx context.Context, usenet, debrid <-chan indexer.ScoredSplitSearchResult) (indexer.ScoredSplitSearchResult, error) {
	var result indexer.ScoredSplitSearchResult
	got := false
	for usenet != nil || debrid != nil {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case value, ok := <-usenet:
			if !ok {
				usenet = nil
				continue
			}
			result = value
			got = true
		case _, ok := <-debrid:
			if !ok {
				debrid = nil
			}
		}
	}
	if !got {
		return result, errors.New("availability search did not complete")
	}
	return result, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func (s *Service) requestScopeStillMatches(item request) bool {
	if !s.EpisodeHintsEnabled() {
		return false
	}
	resolve, err := s.scope(item.ProfileID, item.ClientID)
	if err != nil {
		return false
	}
	selected, err := resolve(item.Title.ID)
	if err != nil {
		return false
	}
	if item.Episode != nil {
		selected = episodeSelection(selected, item.Episode, item.Title)
	}
	return selected.Key == item.Scope
}
