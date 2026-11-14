package sourceavailability

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"novastream/models"
	"novastream/services/badstreams"
	"novastream/services/indexer"
	"novastream/utils/parsett"
)

const MaxVisibleEpisodes = 12
const episodeViewTTL = 30 * time.Second
const maxEpisodeViews = 128

var ErrStaleEpisodeView = errors.New("stale visible episode version")
var ErrEpisodeViewLimit = errors.New("visible episode view limit reached")

type EpisodeHint struct {
	ID           string                     `json:"episodeId"`
	Season       int                        `json:"seasonNumber"`
	Episode      int                        `json:"episodeNumber"`
	Availability *models.SourceAvailability `json:"sourceAvailability,omitempty"`
}
type EpisodeViewResponse struct {
	Enabled bool          `json:"enabled"`
	Version int64         `json:"version"`
	Items   []EpisodeHint `json:"items"`
}
type episodeView struct {
	Version  int64
	Updated  time.Time
	Requests map[string]bool
}

func (s *Service) EpisodeHintsEnabled() bool {
	cfg, err := s.manager.Load()
	return err == nil && cfg.SourceAvailability.Enabled && s.scope != nil
}
func episodeCacheID(title string, ep *models.SeriesEpisode) string {
	data, _ := json.Marshal([]any{models.EpisodeNumberingKey(ep.Numbering), ep.SeasonNumber, ep.EpisodeNumber, ep.ID})
	sum := sha256.Sum256(data)
	return title + ":episode:" + hex.EncodeToString(sum[:12])
}
func episodeSelection(selection Selection, ep *models.SeriesEpisode, title models.Title) Selection {
	data, _ := json.Marshal([]any{selection.Key, ep.Numbering, ep.ID, ep.SeasonNumber, ep.EpisodeNumber, ep.AbsoluteEpisodeNumber, ep.AiredDate, ep.AiredDateTimeUTC, ep.SourceDateUnique, title.Name, title.OriginalName, title.Year, title.Language, title.IMDBID, title.TVDBID, title.TMDBID, title.IsDaily})
	sum := sha256.Sum256(data)
	selection.Key = hex.EncodeToString(sum[:])
	return selection
}
func (item request) cacheID() string {
	if item.Episode != nil {
		return episodeCacheID(item.Title.ID, item.Episode)
	}
	return item.Title.ID
}
func requestID(item request) string { return key(item.ProfileID, item.cacheID(), item.Scope) }
func (s *Service) neededUntilLocked(id string) time.Time {
	var until time.Time
	for _, view := range s.episodeViews {
		expiry := view.Updated.Add(episodeViewTTL)
		if view.Requests[id] && expiry.After(s.now()) && expiry.After(until) {
			until = expiry
		}
	}
	return until
}
func (s *Service) episodeNeeded(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.neededUntilLocked(id).IsZero()
}

// ReplaceEpisodeView accepts only server-resolved metadata. A short lease and
// ordered versions prevent hidden views and late requests from restarting work.
func (s *Service) ReplaceEpisodeView(profile, client, viewID string, version int64, title models.Title, episodes []models.SeriesEpisode) (EpisodeViewResponse, error) {
	response := EpisodeViewResponse{Enabled: s.EpisodeHintsEnabled(), Version: version, Items: []EpisodeHint{}}
	if profile == "" || client == "" || viewID == "" || len(viewID) > 64 || version < 1 || len(episodes) > MaxVisibleEpisodes {
		return response, errors.New("invalid visible episode demand")
	}
	identity := profile + "\x00" + client + "\x00" + viewID
	if !response.Enabled {
		return response, nil
	}
	var resolve func(string) (Selection, error)
	if len(episodes) > 0 {
		var err error
		resolve, err = s.scope(profile, client)
		if err != nil {
			return response, err
		}
		if title.ID == "" || title.Name == "" || title.MediaType != "series" && title.MediaType != "tv" {
			return response, errors.New("verified series required")
		}
	}
	pending := []request{}
	desired := map[string]bool{}
	for _, ep := range episodes {
		if ep.ID == "" || ep.SeasonNumber < 0 || ep.EpisodeNumber < 1 || ep.Numbering == nil || ep.Numbering.SeriesID == "" {
			return response, errors.New("verified episode numbering required")
		}
		selected, err := resolve(title.ID)
		if err != nil {
			return response, err
		}
		selected = episodeSelection(selected, &ep, title)
		copy := ep
		numbering := *ep.Numbering
		copy.Numbering = &numbering
		item := request{ProfileID: profile, ClientID: client, Title: title, Episode: &copy, Scope: selected.Key, AudioLanguage: selected.AudioLanguage, LastSeen: s.now()}
		value := s.episodeAvailability(item, selected)
		response.Items = append(response.Items, EpisodeHint{ID: ep.ID, Season: ep.SeasonNumber, Episode: ep.EpisodeNumber, Availability: &value})
		if episodeEligibility(title, ep, s.now()) == "" {
			pending = append(pending, item)
			desired[requestID(item)] = true
		}
	}
	s.mu.Lock()
	if s.episodeViews == nil {
		s.episodeViews = map[string]episodeView{}
	}
	for id, view := range s.episodeViews {
		if s.now().Sub(view.Updated) > 2*time.Minute {
			delete(s.episodeViews, id)
		}
	}
	if old, ok := s.episodeViews[identity]; ok && version <= old.Version {
		s.mu.Unlock()
		return response, ErrStaleEpisodeView
	}
	if _, ok := s.episodeViews[identity]; !ok {
		ownViews := 0
		for id := range s.episodeViews {
			if strings.HasPrefix(id, profile+"\x00") {
				ownViews++
			}
		}
		if len(s.episodeViews) >= maxEpisodeViews || ownViews >= 16 {
			s.mu.Unlock()
			return response, ErrEpisodeViewLimit
		}
	}
	s.episodeViews[identity] = episodeView{Version: version, Updated: s.now(), Requests: desired}
	for id, item := range s.pending {
		if item.Episode != nil && s.neededUntilLocked(id).IsZero() {
			delete(s.pending, id)
		}
	}
	if s.activeEpisodeID != "" && s.neededUntilLocked(s.activeEpisodeID).IsZero() && s.cancelEpisode != nil {
		s.cancelEpisode()
	}
	for _, item := range pending {
		s.enqueueLocked(item)
	}
	s.mu.Unlock()
	return response, nil
}

func episodeEligibility(title models.Title, ep models.SeriesEpisode, now time.Time) string {
	if ep.Numbering == nil || ep.Numbering.SeriesID == "" {
		return "numbering_unverified"
	}
	ordering := strings.ToLower(strings.TrimSpace(ep.Numbering.Ordering))
	if ordering != "" && ordering != "official" {
		return "numbering_unverified"
	}
	if ep.AiredDateTimeUTC != "" {
		aired, err := time.Parse(time.RFC3339Nano, ep.AiredDateTimeUTC)
		if err != nil {
			return "airdate_unverified"
		}
		if aired.After(now) {
			return "not_aired"
		}
	}
	if ep.AiredDate != "" {
		day, err := time.Parse("2006-01-02", ep.AiredDate)
		if err != nil {
			return "airdate_unverified"
		}
		if day.After(now.UTC()) {
			return "not_aired"
		}
	}
	if title.IsDaily && (ep.AiredDate == "" || !ep.SourceDateUnique) {
		return "airdate_unverified"
	}
	return ""
}
func (s *Service) episodeAvailability(item request, selection Selection) models.SourceAvailability {
	if reason := episodeEligibility(item.Title, *item.Episode, s.now()); reason != "" {
		return unknown(reason)
	}
	value, evidence := s.Store.getObservation(item.ProfileID, item.cacheID(), selection.Key)
	return s.evaluateEpisode(value, evidence, item, selection)
}
func (s *Service) evaluateEpisode(value models.SourceAvailability, evidence *observation, item request, selection Selection) models.SourceAvailability {
	if evidence == nil {
		return s.evaluate(value, nil, s.makeEpisodeReportContext(item, selection))
	}
	filtered := copyObservation(evidence)
	filtered.Candidates = nil
	uncertain := false
	for _, candidate := range evidence.Candidates {
		switch episodeCandidate(candidate.Title, item.Title, *item.Episode) {
		case "exact":
			filtered.Candidates = append(filtered.Candidates, candidate)
		case "unknown":
			uncertain = true
		}
	}
	value = s.evaluate(value, filtered, s.makeEpisodeReportContext(item, selection))
	if value.State == "not_found" && (uncertain || item.Episode.AiredDate == "" && item.Episode.AiredDateTimeUTC == "") {
		value.State, value.Complete, value.Reason = "unknown", false, "episode_not_confirmed"
	}
	return value
}

var explicitEpisodePattern = regexp.MustCompile(`(?i)\bS\d{1,4}E\d{1,4}\b`)
var releaseDatePattern = regexp.MustCompile(`(?:19|20)\d{2}[.\- ]\d{2}[.\- ]\d{2}`)

func titleKey(raw string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, raw)
}
func episodeCandidate(raw string, title models.Title, ep models.SeriesEpisode) string {
	parsed, err := parsett.ParseTitle(raw)
	if err != nil || parsed == nil {
		return "unknown"
	}
	sameTitle := func(raw string) bool {
		for _, name := range []string{title.Name, title.OriginalName} {
			if name != "" && titleKey(raw) == titleKey(name) {
				return true
			}
		}
		return false
	}
	if title.IsDaily {
		if explicitEpisodePattern.MatchString(raw) {
			return "unknown"
		}
		positions := releaseDatePattern.FindAllStringIndex(raw, -1)
		if len(positions) != 1 {
			return "unknown"
		}
		p := positions[0]
		if !sameTitle(strings.Trim(raw[:p[0]], " .-_")) {
			return "unknown"
		}
		date := strings.NewReplacer(".", "-", " ", "-").Replace(raw[p[0]:p[1]])
		if _, err := time.Parse("2006-01-02", date); err != nil {
			return "unknown"
		}
		if date == ep.AiredDate {
			return "exact"
		}
		return "wrong"
	}
	if !sameTitle(parsed.Title) {
		return "unknown"
	}
	if len(parsed.Seasons) == 1 && len(parsed.Episodes) == 1 {
		if parsed.Seasons[0] == ep.SeasonNumber && parsed.Episodes[0] == ep.EpisodeNumber {
			return "exact"
		}
		return "wrong"
	}
	return "unknown"
}
func episodeSearchOptions(item request, options indexer.SearchOptions) indexer.SearchOptions {
	if item.Episode == nil {
		return options
	}
	ep := item.Episode
	options.Query = item.Title.Name + " " + fmt.Sprintf("S%02dE%02d", ep.SeasonNumber, ep.EpisodeNumber)
	options.Numbering = ep.Numbering
	options.AbsoluteEpisodeNumber = ep.AbsoluteEpisodeNumber
	options.IsDaily = item.Title.IsDaily
	options.TargetAirDate = ep.AiredDate
	options.EpisodeReleased = ep.AiredDate != "" || ep.AiredDateTimeUTC != ""
	if options.IsDaily {
		options.Query = item.Title.Name + " " + ep.AiredDate
	}
	return options
}

// Keep the series identity and episode coordinates separate from the cache ID.
func (s *Service) makeEpisodeReportContext(item request, selection Selection) badstreams.PlaybackContext {
	ctx := s.reportContext(item.ProfileID, item.ClientID, item.Title.ID, selection)
	if ep := item.Episode; ep != nil {
		ctx.Episode = &models.EpisodeReference{Numbering: ep.Numbering, SeasonNumber: ep.SeasonNumber, EpisodeNumber: ep.EpisodeNumber}
	}
	return ctx
}
