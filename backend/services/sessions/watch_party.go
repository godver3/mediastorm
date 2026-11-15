package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"novastream/models"
)

// Keep the established restricted stream scope so older servers fail closed
// after a downgrade. Only a validated binding is unwrapped to a media source;
// an older server treats the reserved envelope as an unavailable resource.
const watchPartyPlaybackResourcePrefix = "watch-party-playback:v1:"

type watchPartyPlaybackBinding struct {
	RoomID   string `json:"roomId"`
	GuestID  string `json:"guestId"`
	Resource string `json:"resource"`
}

type WatchPartyPlaybackValidator func(context.Context, string, string, string) error

// ErrAuthorityUnavailable reports that a watch-party grant could not be
// checked (for example, a database outage). Callers answer it with a
// retryable error instead of ending the guest's playback. A validator returns
// an error wrapping it for failures that are not authorization decisions.
var ErrAuthorityUnavailable = errors.New("session authority temporarily unavailable")

// watchPartyAuthorityTTL bounds how long a successful room check is reused.
// Guest players request playlists and segments every few seconds; caching the
// decision briefly keeps that from costing several queries per request, at
// the price of a room end taking effect up to this long after it happens.
const watchPartyAuthorityTTL = 5 * time.Second

const watchPartyAuthorityCacheLimit = 1024

func (s *Service) SetWatchPartyPlaybackValidator(validator WatchPartyPlaybackValidator) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watchPartyPlaybackValidator = validator
}

// CreateWatchPartyPlayback grants one guest access to the current room source.
func (s *Service) CreateWatchPartyPlayback(accountID, userAgent, ipAddress string, duration time.Duration, roomID, guestID, resource string) (models.Session, error) {
	binding := watchPartyPlaybackBinding{RoomID: strings.TrimSpace(roomID), GuestID: strings.TrimSpace(guestID), Resource: strings.TrimSpace(resource)}
	if binding.RoomID == "" || binding.GuestID == "" || binding.Resource == "" {
		return models.Session{}, ErrInvalidToken
	}
	encoded, err := json.Marshal(binding)
	if err != nil {
		return models.Session{}, err
	}
	return s.createScopedWithResource(accountID, false, userAgent, ipAddress, duration, models.SessionScopeStream, watchPartyPlaybackResourcePrefix+string(encoded), "")
}

func (s *Service) validateWatchPartyPlayback(session models.Session) (models.Session, error) {
	if session.Scope != models.SessionScopeStream || !strings.HasPrefix(session.ScopeResource, watchPartyPlaybackResourcePrefix) {
		return session, nil
	}
	var binding watchPartyPlaybackBinding
	if json.Unmarshal([]byte(strings.TrimPrefix(session.ScopeResource, watchPartyPlaybackResourcePrefix)), &binding) != nil || binding.RoomID == "" || binding.GuestID == "" || binding.Resource == "" {
		return models.Session{}, ErrSessionNotFound
	}
	s.mu.RLock()
	validator := s.watchPartyPlaybackValidator
	s.mu.RUnlock()
	if validator == nil {
		return models.Session{}, ErrSessionNotFound
	}
	key := binding.RoomID + "\x00" + binding.GuestID + "\x00" + binding.Resource
	if !s.watchPartyAuthorityCached(key) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := validator(ctx, binding.RoomID, binding.GuestID, binding.Resource)
		cancel()
		if errors.Is(err, ErrAuthorityUnavailable) {
			return models.Session{}, err
		}
		if err != nil {
			return models.Session{}, ErrSessionNotFound
		}
		s.cacheWatchPartyAuthority(key)
	}
	session.Scope = models.SessionScopeStream
	session.ScopeResource = binding.Resource
	return session, nil
}

func (s *Service) watchPartyAuthorityCached(key string) bool {
	if s.watchPartyAuthorityTTL <= 0 {
		return false
	}
	s.mu.RLock()
	until, ok := s.watchPartyAuthority[key]
	s.mu.RUnlock()
	return ok && time.Now().Before(until)
}

func (s *Service) cacheWatchPartyAuthority(key string) {
	if s.watchPartyAuthorityTTL <= 0 {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.watchPartyAuthority == nil {
		s.watchPartyAuthority = make(map[string]time.Time)
	}
	if len(s.watchPartyAuthority) >= watchPartyAuthorityCacheLimit {
		for k, until := range s.watchPartyAuthority {
			if !now.Before(until) {
				delete(s.watchPartyAuthority, k)
			}
		}
		if len(s.watchPartyAuthority) >= watchPartyAuthorityCacheLimit {
			return
		}
	}
	s.watchPartyAuthority[key] = now.Add(s.watchPartyAuthorityTTL)
}
