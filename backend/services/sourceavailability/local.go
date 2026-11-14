package sourceavailability

import (
	"context"
	"novastream/models"
	"time"
)

// LocalProof remains server-internal; no item identifier or filesystem path is
// added to the public hint. Validate must not probe media or touch the filesystem.
type LocalProof struct{ ItemID, Fingerprint string }
type LocalRequest struct {
	ProfileID, ClientID, AudioLanguage string
	Title                              models.Title
	Episode                            *models.SeriesEpisode
}
type LocalChecker interface {
	Check(context.Context, LocalRequest) (LocalProof, error)
	Validate(context.Context, LocalRequest, LocalProof) bool
}
type localObservation struct {
	Proof            LocalProof
	State            string
	Checked, Expires time.Time
	ReportRevision   string
}

func (s *Service) SetLocalChecker(checker LocalChecker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.localChecker = checker
	s.localRecords = map[string]localObservation{}
}
func localRequest(item request, selected Selection) LocalRequest {
	return LocalRequest{item.ProfileID, item.ClientID, selected.AudioLanguage, item.Title, item.Episode}
}
func (s *Service) localRevision(item request) string {
	return s.reports.RevisionFor(s.makeEpisodeReportContext(item, Selection{AudioLanguage: item.AudioLanguage}))
}
func (s *Service) localDueLocked(item request) bool {
	if s.localChecker == nil {
		return false
	}
	v, ok := s.localRecords[requestID(item)]
	return !ok || !v.Expires.After(s.now()) || v.ReportRevision != s.localRevision(item)
}
func (s *Service) checkedLocal(ctx context.Context, item request, selected Selection) bool {
	s.mu.Lock()
	checker := s.localChecker
	prior, ok := s.localRecords[requestID(item)]
	due := s.localDueLocked(item)
	s.mu.Unlock()
	if checker == nil {
		return false
	}
	if ok && !due {
		return prior.State == "found"
	}
	before := s.localRevision(item)
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	proof, err := checker.Check(bounded, localRequest(item, selected))
	if ctx.Err() != nil || !s.requestScopeStillMatches(item) || s.localRevision(item) != before {
		return false
	}
	now := s.now()
	value := localObservation{Proof: proof, State: "none", Checked: now, Expires: now.Add(2 * time.Minute), ReportRevision: before}
	if err != nil {
		value.State = "unknown"
		value.Expires = now.Add(30 * time.Second)
	} else if proof.ItemID != "" {
		value.State = "found"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.neededUntilLocked(requestID(item)).IsZero() {
		return false
	}
	if s.localRecords == nil {
		s.localRecords = map[string]localObservation{}
	}
	s.localRecords[requestID(item)] = value
	if len(s.localRecords) > MaxRecords {
		oldest := ""
		for id, v := range s.localRecords {
			if id != requestID(item) && (oldest == "" || v.Checked.Before(s.localRecords[oldest].Checked)) {
				oldest = id
			}
		}
		delete(s.localRecords, oldest)
	}
	return value.State == "found"
}
func (s *Service) localConservative(value models.SourceAvailability) models.SourceAvailability {
	s.mu.Lock()
	enabled := s.localChecker != nil
	s.mu.Unlock()
	if enabled && value.State == "not_found" {
		value.State, value.Complete, value.Reason = "unknown", false, "local_source_unconfirmed"
	}
	return value
}
func (s *Service) localHint(ctx context.Context, item request, selected Selection) models.SourceAvailability {
	value, evidence := s.Store.getObservation(item.ProfileID, item.cacheID(), selected.Key)
	if item.Episode != nil {
		if reason := episodeEligibility(item.Title, *item.Episode, s.now()); reason != "" {
			value = unknown(reason)
		} else {
			value = s.evaluateEpisode(value, evidence, item, selected)
		}
	} else {
		value = s.evaluate(value, evidence, s.reportContext(item.ProfileID, item.ClientID, item.Title.ID, selected))
	}
	s.mu.Lock()
	checker := s.localChecker
	observation, ok := s.localRecords[requestID(item)]
	due := s.localDueLocked(item)
	s.mu.Unlock()
	if checker == nil {
		return value
	}
	if evidence != nil && evidence.Kind == "prepared" && !evidence.PreparedRemote && value.State == "found" {
		value.State, value.Complete, value.Reason = "unknown", false, "prepared_source_requires_recheck"
	}
	if ok && !due && (observation.State == "found" || observation.State == "none") {
		if ctx.Err() == nil && checker.Validate(ctx, localRequest(item, selected), observation.Proof) && ctx.Err() == nil && s.localRevision(item) == observation.ReportRevision && s.requestScopeStillMatches(item) {
			if observation.State == "none" {
				return value
			}
			checked, expires := observation.Checked.UTC(), observation.Expires.UTC()
			return models.SourceAvailability{State: "found", Reason: "local_file_checked", SourceType: "local", CheckedAt: &checked, ExpiresAt: &expires}
		}
		s.mu.Lock()
		delete(s.localRecords, requestID(item))
		s.mu.Unlock()
		due = true
	}
	if !ok || due || observation.State == "unknown" {
		return s.localConservative(value)
	}
	return value
}
func (s *Service) ApplyLocalTitles(ctx context.Context, profile, client string, titles []models.Title, response TitleViewResponse) TitleViewResponse {
	resolve, err := s.scope(profile, client)
	if err != nil {
		return response
	}
	byID := map[TitleCoordinate]models.Title{}
	for _, title := range titles {
		byID[titleCoordinate(title)] = title
	}
	bounded, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	for i := range response.Items {
		title, ok := byID[response.Items[i].TitleCoordinate]
		if !ok {
			continue
		}
		selected, err := resolve(title.ID)
		if err != nil {
			continue
		}
		item := request{ProfileID: profile, ClientID: client, Title: title, Scope: selected.Key, AudioLanguage: selected.AudioLanguage}
		value := s.localHint(bounded, item, selected)
		value = s.recheckPresentation(value, item.ProfileID, item.cacheID(), selected.Key)
		response.Items[i].Availability = &value
	}
	return response
}
func (s *Service) ApplyLocalEpisodes(ctx context.Context, profile, client string, title models.Title, episodes []models.SeriesEpisode, response EpisodeViewResponse) EpisodeViewResponse {
	resolve, err := s.scope(profile, client)
	if err != nil {
		return response
	}
	bounded, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	for i := range response.Items {
		for _, ep := range episodes {
			if ep.ID != response.Items[i].ID || ep.SeasonNumber != response.Items[i].Season || ep.EpisodeNumber != response.Items[i].Episode {
				continue
			}
			selected, err := resolve(title.ID)
			if err != nil {
				continue
			}
			selected = episodeSelection(selected, &ep, title)
			item := request{ProfileID: profile, ClientID: client, Title: title, Episode: &ep, Scope: selected.Key, AudioLanguage: selected.AudioLanguage}
			value := s.localHint(bounded, item, selected)
			value = s.recheckPresentation(value, item.ProfileID, item.cacheID(), selected.Key)
			response.Items[i].Availability = &value
		}
	}
	return response
}

func (s *Service) hasLocalChecker() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.localChecker != nil
}
