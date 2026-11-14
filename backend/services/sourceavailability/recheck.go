package sourceavailability

import (
	"errors"
	"math"
	"novastream/models"
	"time"
)

var ErrRecheckCooldown = errors.New("a source check was requested recently")
var ErrRecheckCapacity = errors.New("too many pending source checks")
var ErrRecheckDisabled = errors.New("source checks are disabled")

const recheckLifetime = 2 * time.Minute
const recheckCooldown = time.Minute

type RecheckResponse struct {
	Status            string `json:"status"`
	RetryAfterSeconds int    `json:"retryAfterSeconds"`
}
type recheckJob struct {
	ProfileID, CacheID, State string
	Generation                uint64
	Requested, Expires        time.Time
}

func (s *Service) recheckQueuedLocked(id string) bool {
	v, ok := s.rechecks[id]
	return ok && v.State == "queued" && v.Expires.After(s.now())
}
func (s *Service) finishRecheckLocked(id string, generation uint64, reason string) {
	if generation == 0 {
		return
	}
	v, ok := s.rechecks[id]
	if !ok || v.Generation != generation || v.State != "queued" {
		return
	}
	v.State = reason
	if reason == "" {
		v.State = "completed"
		v.Expires = v.Requested.Add(recheckCooldown)
	} else {
		v.Expires = s.now().Add(30 * time.Second)
	}
	s.rechecks[id] = v
}
func (s *Service) finishRecheck(id string, generation uint64, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishRecheckLocked(id, generation, reason)
}

// RequestRecheck receives metadata already authorized/resolved by the handler.
// An explicit request survives leaving the viewport, but never exceeds two
// minutes or resets the existing persisted provider budget.
func (s *Service) RequestRecheck(profile, client string, title models.Title, episode *models.SeriesEpisode) (RecheckResponse, error) {
	response := RecheckResponse{Status: "queued", RetryAfterSeconds: 1}
	if !s.EpisodeHintsEnabled() {
		return response, ErrRecheckDisabled
	}
	if profile == "" || client == "" || title.ID == "" || title.Name == "" || title.MediaType != "movie" && title.MediaType != "series" && title.MediaType != "tv" {
		return response, errors.New("verified catalogue title required")
	}
	resolve, err := s.scope(profile, client)
	if err != nil {
		return response, err
	}
	selected, err := resolve(title.ID)
	if err != nil {
		return response, err
	}
	if episode != nil {
		if title.MediaType == "movie" || episode.ID == "" || episode.Numbering == nil || episode.SeasonNumber < 0 || episode.EpisodeNumber < 1 {
			return response, errors.New("verified episode required")
		}
		copy := *episode
		numbering := *episode.Numbering
		copy.Numbering = &numbering
		episode = &copy
		selected = episodeSelection(selected, episode, title)
	}
	item := request{ProfileID: profile, ClientID: client, Title: title, Episode: episode, Scope: selected.Key, AudioLanguage: selected.AudioLanguage, LastSeen: s.now()}
	id := requestID(item)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rechecks == nil {
		s.rechecks = map[string]recheckJob{}
	}
	for k, v := range s.rechecks {
		if v.State == "queued" && !v.Expires.After(now) {
			v.State = "recheck_timed_out"
			v.Expires = now.Add(30 * time.Second)
			s.rechecks[k] = v
		}
		if v.State != "queued" && !v.Expires.After(now) && now.Sub(v.Requested) >= recheckCooldown {
			delete(s.rechecks, k)
		}
	}
	if s.recheckQueuedLocked(id) {
		return response, nil
	}
	pending, own := 0, 0
	for _, v := range s.rechecks {
		if v.ProfileID == profile && v.CacheID == item.cacheID() && now.Sub(v.Requested) < recheckCooldown {
			response.RetryAfterSeconds = int(math.Ceil((recheckCooldown - now.Sub(v.Requested)).Seconds()))
			return response, ErrRecheckCooldown
		}
		if v.State == "queued" && v.Expires.After(now) {
			pending++
			if v.ProfileID == profile {
				own++
			}
		}
	}
	if pending >= 32 || own >= 4 {
		response.RetryAfterSeconds = 30
		return response, ErrRecheckCapacity
	}
	if len(s.rechecks) >= MaxRecords {
		oldest := ""
		for k, v := range s.rechecks {
			if v.State != "queued" && (oldest == "" || v.Requested.Before(s.rechecks[oldest].Requested)) {
				oldest = k
			}
		}
		if oldest != "" {
			delete(s.rechecks, oldest)
		} else {
			return response, ErrRecheckCapacity
		}
	}
	s.recheckGeneration++
	s.rechecks[id] = recheckJob{ProfileID: profile, CacheID: item.cacheID(), State: "queued", Generation: s.recheckGeneration, Requested: now, Expires: now.Add(recheckLifetime)}
	delete(s.localRecords, id)
	s.enqueueLocked(item)
	return response, nil
}
func (s *Service) recheckPresentation(value models.SourceAvailability, profile, cacheID, scope string) models.SourceAvailability {
	id := key(profile, cacheID, scope)
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.rechecks[id]
	if !ok {
		return value
	}
	if v.State == "queued" && !v.Expires.After(s.now()) {
		v.State = "recheck_timed_out"
		v.Expires = s.now().Add(30 * time.Second)
		s.rechecks[id] = v
	}
	if v.State == "completed" || !v.Expires.After(s.now()) {
		return value
	}
	reason := v.State
	if reason == "queued" {
		reason = "recheck_queued"
	}
	value.State, value.Complete, value.Reason, value.SourceType = "unknown", false, reason, ""
	return value
}
