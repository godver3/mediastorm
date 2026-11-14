package badstreams

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"novastream/models"
	"novastream/utils/language"
)

// ReportScope qualifies a report by the requested content and playback intent.
// An empty ProfileID is shared only when an authorized caller creates the rule.
type ReportScope struct {
	Kind            string `json:"kind"`
	TitleID         string `json:"titleId"`
	NumberingKey    string `json:"numberingKey,omitempty"`
	SeasonNumber    *int   `json:"seasonNumber,omitempty"`
	EpisodeNumber   *int   `json:"episodeNumber,omitempty"`
	ProfileID       string `json:"profileId,omitempty"`
	AudioLanguage   string `json:"audioLanguage,omitempty"`
	AudioTrackIndex *int   `json:"audioTrackIndex,omitempty"`
	ClientID        string `json:"clientId,omitempty"`
}

type PlaybackContext struct {
	ProfileID, ClientID, TitleID, AudioLanguage string
	Episode                                     *models.EpisodeReference
	AudioTrackIndex                             *int
	anyAudioTrack                               bool
}

type ReportRequest struct {
	Result     models.NZBResult
	Context    PlaybackContext
	Reason     string
	ReporterID string
	Shared     bool
}

func (s *Service) Report(req ReportRequest) (Entry, error) {
	if s == nil {
		return Entry{}, errors.New("release report service unavailable")
	}
	ctx := req.Context
	if strings.TrimSpace(ctx.TitleID) == "" || strings.TrimSpace(ctx.ProfileID) == "" || strings.TrimSpace(req.ReporterID) == "" || NormalizeReleaseName(req.Result.Title) == "" {
		return Entry{}, errors.New("a verified title, profile and selected release are required")
	}
	scope := ReportScope{TitleID: strings.ToLower(strings.TrimSpace(ctx.TitleID)), ProfileID: ctx.ProfileID}
	if ctx.Episode != nil {
		season, episode := ctx.Episode.SeasonNumber, ctx.Episode.EpisodeNumber
		if season < 0 || episode <= 0 {
			return Entry{}, errors.New("a valid episode is required")
		}
		scope.SeasonNumber, scope.EpisodeNumber = &season, &episode
		scope.NumberingKey = models.EpisodeNumberingKey(ctx.Episode.Numbering)
	}
	if req.Shared {
		scope.ProfileID = ""
	}
	var expires *time.Time
	serviceType, provider := "", ""
	switch req.Reason {
	case "wrong_title", "wrong_episode", "file_problem":
		scope.Kind = "title"
		if req.Reason == "wrong_episode" && ctx.Episode == nil {
			return Entry{}, errors.New("wrong episode requires an episode reference")
		}
	case "missing_language":
		scope.Kind = "language"
		scope.AudioLanguage = language.NormalizeToCode(ctx.AudioLanguage)
		if scope.AudioLanguage == "" || scope.AudioLanguage == "und" {
			return Entry{}, errors.New("the requested audio language is required")
		}
	case "audio_problem":
		scope.Kind = "track"
		scope.AudioTrackIndex = ctx.AudioTrackIndex
		if scope.AudioTrackIndex == nil || *scope.AudioTrackIndex < 0 {
			return Entry{}, errors.New("the affected audio track is required")
		}
	case "quality_problem":
		scope.Kind = "profile"
		scope.ProfileID = ctx.ProfileID
	case "device_problem":
		scope.Kind = "device"
		scope.ProfileID, scope.ClientID = ctx.ProfileID, strings.TrimSpace(ctx.ClientID)
		if scope.ClientID == "" {
			return Entry{}, errors.New("the affected device is required")
		}
	case "source_unavailable":
		scope.Kind = "provider"
		scope.ProfileID = ctx.ProfileID
		serviceType, provider = normalizeServiceType(string(req.Result.ServiceType)), providerForResult(req.Result)
		if provider == "" {
			provider = NormalizeProvider(req.Result.Indexer)
		}
		until := time.Now().UTC().Add(15 * time.Minute)
		expires = &until
	default:
		return Entry{}, errors.New("unsupported release report reason")
	}
	identity := contentIdentity(req.Result)
	encoded, _ := json.Marshal([]any{NormalizeReleaseName(req.Result.Title), identity, scope, req.Reason, provider, serviceType})
	sum := sha256.Sum256(encoded)
	id := "report-" + hex.EncodeToString(sum[:12])
	now := time.Now().UTC()
	entry := Entry{ID: id, ReleaseName: req.Result.Title, NormalizedReleaseName: NormalizeReleaseName(req.Result.Title), ServiceType: serviceType, Provider: provider, Reason: req.Reason, MarkedAt: now, LastSeenAt: now, HitCount: 1, Scope: &scope, ReportedBy: req.ReporterID, ExpiresAt: expires, ContentIdentity: identity}
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, ok := s.entries[id]; ok {
		entry.MarkedAt = previous.MarkedAt
		entry.HitCount = previous.HitCount + 1
		if previous.Scope != nil && previous.Scope.ProfileID == "" && entry.Scope.ProfileID != "" {
			entry.Scope = previous.Scope
		}
	}
	if _, exists := s.entries[id]; !exists && len(s.entries) >= 4096 {
		return Entry{}, errors.New("release report limit reached")
	}
	previous, existed := s.entries[id]
	s.entries[id] = entry
	if err := s.saveLocked(); err != nil {
		if existed {
			s.entries[id] = previous
		} else {
			delete(s.entries, id)
		}
		return Entry{}, err
	}
	return entry, nil
}

func (s *Service) MatchFor(result models.NZBResult, ctx PlaybackContext) *Entry {
	if legacy := s.Match(result); legacy != nil {
		return legacy
	}
	if s == nil || ctx.TitleID == "" || ctx.ProfileID == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	identity := contentIdentity(result)
	for _, entry := range s.entries {
		scope := entry.Scope
		if scope == nil || (entry.ExpiresAt != nil && !entry.ExpiresAt.After(time.Now())) {
			continue
		}
		if scope.TitleID != strings.ToLower(strings.TrimSpace(ctx.TitleID)) || (scope.ProfileID != "" && scope.ProfileID != ctx.ProfileID) {
			continue
		}
		if scope.SeasonNumber != nil && (ctx.Episode == nil || scope.EpisodeNumber == nil || ctx.Episode.SeasonNumber != *scope.SeasonNumber || ctx.Episode.EpisodeNumber != *scope.EpisodeNumber || scope.NumberingKey != models.EpisodeNumberingKey(ctx.Episode.Numbering)) {
			continue
		}
		if scope.SeasonNumber == nil && ctx.Episode != nil {
			continue
		}
		if entry.ContentIdentity != "" || identity != "" {
			if identity != entry.ContentIdentity {
				continue
			}
		} else if NormalizeReleaseName(result.Title) != entry.NormalizedReleaseName {
			continue
		}
		if scope.AudioLanguage != "" && language.NormalizeToCode(ctx.AudioLanguage) != scope.AudioLanguage {
			continue
		}
		if scope.AudioTrackIndex != nil && !ctx.anyAudioTrack && (ctx.AudioTrackIndex == nil || *ctx.AudioTrackIndex != *scope.AudioTrackIndex) {
			continue
		}
		if scope.ClientID != "" && scope.ClientID != ctx.ClientID {
			continue
		}
		if entry.ServiceType != "" && entry.ServiceType != normalizeServiceType(string(result.ServiceType)) {
			continue
		}
		if entry.Provider != "" {
			provider := providerForResult(result)
			if provider == "" {
				provider = NormalizeProvider(result.Indexer)
			}
			if provider != entry.Provider {
				continue
			}
		}
		copy := entry
		return &copy
	}
	return nil
}

func contentIdentity(result models.NZBResult) string {
	if strings.HasPrefix(result.Link, "localmedia:") {
		id := strings.SplitN(strings.TrimPrefix(result.Link, "localmedia:"), "/", 2)[0]
		if id != "" {
			sum := sha256.Sum256([]byte(id))
			return "local:" + hex.EncodeToString(sum[:])
		}
	}
	for _, key := range []string{"infoHash", "info_hash", "infohash"} {
		if value := normalizedInfoHash(result.Attributes[key]); value != "" {
			return "btih:" + value
		}
	}
	if strings.HasPrefix(strings.ToLower(result.Link), "magnet:") {
		if u, err := url.Parse(result.Link); err == nil {
			for _, value := range u.Query()["xt"] {
				if strings.HasPrefix(strings.ToLower(value), "urn:btih:") {
					if hash := normalizedInfoHash(value[len("urn:btih:"):]); hash != "" {
						return "btih:" + hash
					}
				}
			}
		}
	}
	return ""
}

func normalizedInfoHash(value string) string {
	value = strings.TrimSpace(value)
	if len(value) == 40 {
		if decoded, err := hex.DecodeString(value); err == nil && len(decoded) == 20 {
			return hex.EncodeToString(decoded)
		}
	}
	if len(value) == 32 {
		if decoded, err := base32.StdEncoding.DecodeString(strings.ToUpper(value)); err == nil && len(decoded) == 20 {
			return hex.EncodeToString(decoded)
		}
	}
	return ""
}

func (s *Service) Get(id string) *Entry {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.entries[id]
	if !ok {
		return nil
	}
	return &entry
}

func (s *Service) ShareReport(id string) (Entry, error) {
	if s == nil {
		return Entry{}, errors.New("release reports unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[id]
	if !ok || entry.Scope == nil {
		return Entry{}, errors.New("release report not found")
	}
	if entry.Scope.Kind != "title" && entry.Scope.Kind != "language" && entry.Scope.Kind != "track" {
		return Entry{}, errors.New("this report must remain contextual to its profile or device")
	}
	previous := entry
	scope := *entry.Scope
	scope.ProfileID = ""
	entry.Scope = &scope
	s.entries[id] = entry
	if err := s.saveLocked(); err != nil {
		s.entries[id] = previous
		return Entry{}, err
	}
	return entry, nil
}

func SameRelease(first, second models.NZBResult) bool {
	a, b := contentIdentity(first), contentIdentity(second)
	if a != "" || b != "" {
		return a != "" && a == b
	}
	name := NormalizeReleaseName(first.Title)
	return name != "" && name == NormalizeReleaseName(second.Title)
}

// HasAudioReportFor requests a full probe only when an applicable track report exists.
func (s *Service) HasAudioReportFor(result models.NZBResult, ctx PlaybackContext) bool {
	ctx.anyAudioTrack = true
	match := s.MatchFor(result, ctx)
	return match != nil && match.Scope != nil && match.Scope.Kind == "track"
}
