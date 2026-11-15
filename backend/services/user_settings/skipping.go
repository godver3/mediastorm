package user_settings

import (
	"strings"

	"novastream/models"
)

// SkipPreferences patches only supplied playback choices. Nil means unchanged.
type SkipPreferences struct {
	NextEpisode *bool `json:"nextEpisode,omitempty"`
	Intro       *bool `json:"intro,omitempty"`
	Recap       *bool `json:"recap,omitempty"`
	Credits     *bool `json:"credits,omitempty"`
}

// UpdateSkipping preserves unrelated settings and serializes concurrent toggles.
func (s *Service) UpdateSkipping(userID string, patch SkipPreferences) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ErrUserIDRequired
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, existed := s.settings[userID]
	updated := previous
	if patch.NextEpisode != nil {
		updated.Playback.NextEpisodeAutoPlay = models.BoolPtr(*patch.NextEpisode)
	}
	if patch.Intro != nil {
		updated.Playback.IntroAutoSkip = models.BoolPtr(*patch.Intro)
	}
	if patch.Recap != nil {
		updated.Playback.RecapAutoSkip = models.BoolPtr(*patch.Recap)
	}
	if patch.Credits != nil {
		updated.Playback.CreditsAutoSkip = models.BoolPtr(*patch.Credits)
	}
	s.settings[userID] = updated
	if err := s.saveLocked(); err != nil {
		if existed {
			s.settings[userID] = previous
		} else {
			delete(s.settings, userID)
		}
		return err
	}
	return nil
}
