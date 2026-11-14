package users

import (
	"errors"
	"novastream/models"
	"strings"
	"time"
)

// SetKidsLiveMaxAge is only exposed through the parent-authorized profile route.
func (s *Service) SetKidsLiveMaxAge(id string, age *int) (models.User, error) {
	if age != nil && *age != 0 && *age != 6 && *age != 12 && *age != 16 && *age != 18 {
		return models.User{}, errors.New("live age must be 0, 6, 12, 16 or 18")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, ok := s.users[strings.TrimSpace(id)]
	if !ok {
		return models.User{}, ErrUserNotFound
	}
	user := previous
	user.KidsLiveMaxAge = nil
	if age != nil {
		value := *age
		user.KidsLiveMaxAge = &value
	}
	user.UpdatedAt = time.Now().UTC()
	s.users[user.ID] = user
	if err := s.saveLocked(); err != nil {
		s.users[user.ID] = previous
		return models.User{}, err
	}
	return user, nil
}
