package sourceavailability

import (
	"encoding/base32"
	"encoding/hex"
	"strings"

	"novastream/models"
	"novastream/services/badstreams"
	"novastream/services/indexer"
	"novastream/utils/language"
)

const maxCandidates = 20

// Store only identities and search language hints, never download URLs,
// credentials, raw attributes, or stream paths. This is not a media probe.
type candidate struct {
	Title       string                    `json:"title"`
	Indexer     string                    `json:"indexer,omitempty"`
	Provider    string                    `json:"provider,omitempty"`
	ServiceType models.ContentServiceType `json:"serviceType,omitempty"`
	Languages   string                    `json:"languages,omitempty"`
	InfoHash    string                    `json:"infoHash,omitempty"`
}

type observation struct {
	PreparedRemote bool        `json:"preparedRemote,omitempty"`
	Version        int         `json:"version"`
	Kind           string      `json:"kind"`
	Candidates     []candidate `json:"candidates,omitempty"`
	Complete       bool        `json:"complete"`
	ReportRevision string      `json:"reportRevision,omitempty"`
}

func copyObservation(value *observation) *observation {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Candidates = append([]candidate(nil), value.Candidates...)
	return &copy
}

func validObservation(value *observation) bool {
	if value == nil || value.Version != 1 || len(value.Candidates) > maxCandidates {
		return false
	}
	if value.Kind == "prepared" {
		return len(value.ReportRevision) > 0 && len(value.Candidates) == 0 && !value.Complete
	}
	if value.Kind != "search" {
		return false
	}
	for _, item := range value.Candidates {
		if projected, ok := projectCandidate(item.result()); !ok || projected != item {
			return false
		}
	}
	return true
}

func projectCandidate(result models.NZBResult) (candidate, bool) {
	item := candidate{Title: result.Title, Indexer: result.Indexer, ServiceType: result.ServiceType, Languages: result.Attributes["languages"]}
	for _, key := range []string{"provider", "debridProvider", "resolver", "resolverProvider"} {
		if item.Provider = badstreams.NormalizeProvider(result.Attributes[key]); item.Provider != "" {
			break
		}
	}
	for _, key := range []string{"infoHash", "info_hash", "infohash"} {
		raw := strings.TrimSpace(result.Attributes[key])
		if decoded, err := hex.DecodeString(raw); err == nil && len(decoded) == 20 {
			item.InfoHash = strings.ToLower(raw)
			break
		}
		if decoded, err := base32.StdEncoding.DecodeString(strings.ToUpper(raw)); err == nil && len(decoded) == 20 {
			item.InfoHash = hex.EncodeToString(decoded)
			break
		}
	}
	if strings.TrimSpace(item.Title) == "" || len(item.Title) > 512 || len(item.Indexer) > 128 || len(item.Provider) > 128 || len(item.Languages) > 256 ||
		item.ServiceType != models.ServiceTypeUnknown && item.ServiceType != models.ServiceTypeUsenet ||
		strings.HasPrefix(strings.ToLower(result.Link), "magnet:") || strings.HasPrefix(result.Link, "localmedia:") {
		return candidate{}, false
	}
	return item, true
}

func (c candidate) result() models.NZBResult {
	return models.NZBResult{Title: c.Title, Indexer: c.Indexer, ServiceType: c.ServiceType,
		Attributes: map[string]string{"provider": c.Provider, "languages": c.Languages, "infoHash": c.InfoHash}}
}

func searchObservation(result indexer.ScoredSplitSearchResult, complete bool) *observation {
	value := &observation{Version: 1, Kind: "search", Complete: complete}
	// Ranking limits can hide otherwise eligible alternatives. An exhausted
	// presentation subset cannot prove that the selected providers are empty.
	if result.RawCount-result.FilteredCount > len(result.Scored) {
		value.Complete = false
	}
	for _, scored := range result.Scored {
		if strings.EqualFold(scored.FilterStatus, "filtered") {
			continue
		}
		item, ok := projectCandidate(scored.NZBResult)
		if !ok || len(value.Candidates) >= maxCandidates {
			value.Complete = false
			continue
		}
		value.Candidates = append(value.Candidates, item)
	}
	return value
}

func (s *Service) reportContext(profile, client, title string, selection Selection) badstreams.PlaybackContext {
	return badstreams.PlaybackContext{ProfileID: profile, ClientID: client, TitleID: title, AudioLanguage: selection.AudioLanguage}
}

func (s *Service) evaluate(value models.SourceAvailability, evidence *observation, ctx badstreams.PlaybackContext) models.SourceAvailability {
	if value.CheckedAt == nil || value.Reason == "expired" {
		return value
	}
	before := s.reports.RevisionFor(ctx)
	if evidence == nil || !validObservation(evidence) {
		if s.reports != nil {
			value.State, value.Complete, value.Reason = "unknown", false, "evidence_not_recorded"
		}
		return value
	}
	if evidence.Kind == "prepared" {
		if evidence.ReportRevision != before {
			value.State, value.Complete, value.Reason = "unknown", false, "prepared_source_requires_recheck"
		}
		return value
	}
	value.State, value.Complete, value.Reason = "unknown", false, "search_incomplete"
	uncertain, blocked := false, false
	for _, item := range evidence.Candidates {
		result := item.result()
		if s.reports.MatchFor(result, ctx) != nil {
			blocked = true
			continue
		}
		if s.reports.HasAudioReportFor(result, ctx) {
			uncertain = true
			value.Reason = "audio_track_unverified"
			continue
		}
		if ctx.AudioLanguage != "" && !language.HasPreferredLanguage(item.Languages, ctx.AudioLanguage) {
			uncertain = true
			value.Reason = "audio_not_confirmed"
			continue
		}
		value.State, value.Complete, value.Reason = "found", evidence.Complete, "matching_candidates"
		break
	}
	if value.State != "found" && evidence.Complete && !uncertain {
		value.State, value.Complete, value.Reason = "not_found", true, "no_matching_candidates"
		if blocked {
			value.Reason = "reported_candidates"
		}
	}
	if s.reports.RevisionFor(ctx) != before {
		value.State, value.Complete, value.Reason = "unknown", false, "reports_changed"
	}
	return value
}

func (s *Service) availability(profile, client, title string, selection Selection) models.SourceAvailability {
	value, evidence := s.Store.getObservation(profile, title, selection.Key)
	value = s.evaluate(value, evidence, s.reportContext(profile, client, title, selection))
	if s.hasLocalChecker() && evidence != nil && evidence.Kind == "prepared" && !evidence.PreparedRemote && value.State == "found" {
		value.State, value.Complete, value.Reason = "unknown", false, "prepared_source_requires_recheck"
	}
	return s.recheckPresentation(s.localConservative(value), profile, title, selection.Key)
}
