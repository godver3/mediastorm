package qualityprofiles

import (
	"context"
	"strings"
)

// Title preset memory: the preset a profile last picked for a title (a
// series or a movie) on one device. Auto ranks that title with it on that
// device; other devices keep their own choice.

func titleKey(userID, clientID, contentID string) (string, string, string, bool) {
	userID, clientID = strings.TrimSpace(userID), strings.TrimSpace(clientID)
	contentID = strings.ToLower(strings.TrimSpace(contentID))
	return userID, clientID, contentID, userID != "" && contentID != ""
}

// TitlePreset returns the preset remembered for a profile, device and title
// ("" when none).
func (s *Service) TitlePreset(ctx context.Context, userID, clientID, contentID string) (string, error) {
	userID, clientID, contentID, ok := titleKey(userID, clientID, contentID)
	if !ok {
		return "", nil
	}
	return s.store.GetTitlePreset(ctx, userID, clientID, contentID)
}

// RememberTitlePreset stores the preset picked for a title on a device; ""
// forgets it. changed is false when nothing was different.
func (s *Service) RememberTitlePreset(ctx context.Context, userID, clientID, contentID, presetID string) (changed bool, err error) {
	userID, clientID, contentID, ok := titleKey(userID, clientID, contentID)
	if !ok {
		return false, nil
	}
	presetID = strings.TrimSpace(presetID)
	old, err := s.store.GetTitlePreset(ctx, userID, clientID, contentID)
	if err != nil {
		return false, err
	}
	if old == presetID {
		return false, nil
	}
	return true, s.store.SetTitlePreset(ctx, userID, clientID, contentID, presetID)
}
