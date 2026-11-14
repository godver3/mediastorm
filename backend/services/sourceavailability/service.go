package sourceavailability

import (
	"context"
	"errors"
	"novastream/config"
	"novastream/models"
	"novastream/services/indexer"
	"novastream/utils/language"
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
	ProfileID, ClientID, Scope string
	Title                      models.Title
	LastSeen                   time.Time
}
type Service struct {
	Store   *Store
	manager *config.Manager
	search  searcher
	scope   ScopeFactory
	mu      sync.Mutex
	pending map[string]request
	now     func() time.Time
}

func NewService(store *Store, manager *config.Manager, search searcher, scope ScopeFactory) *Service {
	return &Service{Store: store, manager: manager, search: search, scope: scope, pending: map[string]request{}, now: time.Now}
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
	return selection.Key + ":" + s.Store.Revision(profileID)
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
		value := s.Store.Get(profileID, title.ID, identity.Key)
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
		s.enqueue(request{ProfileID: profileID, ClientID: clientID, Scope: identity.Key, Title: titles[i], LastSeen: s.now()})
	}
	return result
}

func (s *Service) enqueue(item request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := key(item.ProfileID, item.Title.ID, item.Scope)
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
	if err != nil || !cfg.SourceAvailability.Enabled || s.scope == nil {
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
	return func(prepared bool) {
		if !prepared {
			return
		}
		checked := s.now().UTC()
		expires := checked.Add(time.Duration(cfg.SourceAvailability.EffectiveRefreshHours()) * time.Hour)
		_ = s.Store.Put(profileID, titleID, identity.Key, models.SourceAvailability{State: "found", CheckedAt: &checked, ExpiresAt: &expires, Reason: "prepared_source"})
	}
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
		value := s.Store.Get(item.ProfileID, item.Title.ID, item.Scope)
		if value.ExpiresAt != nil && value.ExpiresAt.After(now) {
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
	if current.Key != chosen.Scope {
		s.mu.Lock()
		delete(s.pending, chosenID)
		s.mu.Unlock()
		chosen.Scope = current.Key
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
	u, d := s.search.SearchWithScoringSplit(work, indexer.SearchOptions{AvailabilityCheck: true, AvailabilityIndexerNames: providers, TitleID: chosen.Title.ID, Query: chosen.Title.Name, MediaType: media, Year: chosen.Title.Year, IMDBID: chosen.Title.IMDBID, TVDBID: chosen.Title.TVDBID, UserID: chosen.ProfileID, ClientID: chosen.ClientID, OriginalLanguage: chosen.Title.Language, AlternateTitles: []string{chosen.Title.OriginalName}, MaxResults: 20})
	result, err := receiveSearch(work, u, d)
	checked := s.now().UTC()
	expires := checked.Add(interval)
	value := models.SourceAvailability{State: "unknown", CheckedAt: &checked, ExpiresAt: &expires, Providers: providers, Reason: "search_incomplete"}
	hasCandidate := false
	for _, candidate := range result.Scored {
		if current.AudioLanguage == "" || language.HasPreferredLanguage(candidate.Attributes["languages"], current.AudioLanguage) {
			hasCandidate = true
			break
		}
	}
	if err == nil && hasCandidate {
		value.State = "found"
		value.Complete = !result.Incomplete && result.Err == nil
		value.Reason = "matching_candidates"
	} else if err == nil && len(result.Scored) == 0 && result.Err == nil && !result.Incomplete && !result.Disabled {
		value.State = "not_found"
		value.Complete = true
		value.Reason = "no_matching_candidates"
	} else if err == nil && len(result.Scored) > 0 {
		value.Reason = "audio_not_confirmed"
	}
	if value.State == "unknown" {
		retry := checked.Add(5 * time.Minute)
		value.ExpiresAt = &retry
	}
	_ = s.Store.Put(chosen.ProfileID, chosen.Title.ID, chosen.Scope, value)
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
