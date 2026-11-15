package sessions

import (
	"context"
	"encoding/json"
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if validator(ctx, binding.RoomID, binding.GuestID, binding.Resource) != nil {
		return models.Session{}, ErrSessionNotFound
	}
	session.Scope = models.SessionScopeStream
	session.ScopeResource = binding.Resource
	return session, nil
}
