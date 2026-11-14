package sourceavailability

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"novastream/config"
	"novastream/models"
	"novastream/utils/language"
)

type profileSettings interface {
	Get(string) (*models.UserSettings, error)
}
type deviceSettings interface {
	Get(string, string) (*models.ClientFilterSettings, error)
}
type contentPreferences interface {
	List(string) ([]models.ContentPreference, error)
}

type Selection struct {
	Key           string
	AudioLanguage string
}

// NewScopeFactory loads profile/device/content preferences once per response,
// then hashes each title in memory. Global defaults are always part of the
// identity: changing a default must also invalidate profiles inheriting it.
func NewScopeFactory(manager *config.Manager, profiles profileSettings, devices deviceSettings, content contentPreferences, exists func(string) bool) ScopeFactory {
	return func(profileID, clientID string) (func(string) (Selection, error), error) {
		if profileID == "" || (exists != nil && !exists(profileID)) {
			return nil, errors.New("profile not found")
		}
		cfg, err := manager.Load()
		if err != nil {
			return nil, err
		}
		visible := config.FilterSettingsForProfile(cfg, profileID)
		var profile *models.UserSettings
		if profiles != nil {
			profile, err = profiles.Get(profileID)
			if err != nil {
				return nil, err
			}
		}
		var device *models.ClientFilterSettings
		if clientID != "" && devices != nil {
			device, err = devices.Get(clientID, profileID)
			if err != nil {
				return nil, err
			}
		}
		preferences := map[string]models.ContentPreference{}
		if content != nil {
			items, err := content.List(profileID)
			if err != nil {
				return nil, err
			}
			for _, item := range items {
				preferences[strings.ToLower(strings.TrimSpace(item.ContentID))] = item
			}
		}
		// The digest never exposes configuration credentials. Hashing the entire
		// selected source configuration also detects permission/key/URL changes.
		parts := []any{visible.Indexers, cfg.SourceAvailability, cfg.Streaming, cfg.Filtering, cfg.AnimeFiltering, cfg.Ranking, cfg.Playback.PreferredAudioLanguage, cfg.Metadata, device}
		audio := cfg.Playback.PreferredAudioLanguage
		if profile != nil {
			parts = append(parts, profile.Filtering, profile.AnimeFiltering, profile.Ranking, profile.Playback.PreferredAudioLanguage, profile.Metadata)
			if profile.Playback.PreferredAudioLanguage != "" {
				audio = profile.Playback.PreferredAudioLanguage
			}
		}
		if device != nil && device.PreferredAudioLanguage != nil {
			audio = *device.PreferredAudioLanguage
		}
		base, err := json.Marshal(parts)
		if err != nil {
			return nil, err
		}
		return func(titleID string) (Selection, error) {
			preferred := audio
			pref, ok := preferences[strings.ToLower(strings.TrimSpace(titleID))]
			var selected *models.ContentPreference
			if ok {
				selected = &pref
				if pref.AudioLanguage != "" {
					preferred = pref.AudioLanguage
				}
			}
			data, err := json.Marshal(selected)
			if titleID == "" {
				data, err = json.Marshal(preferences)
			}
			if err != nil {
				return Selection{}, err
			}
			sum := sha256.Sum256(append(append(append([]byte(nil), base...), '\x00'), data...))
			return Selection{Key: hex.EncodeToString(sum[:]), AudioLanguage: language.NormalizeToCode(preferred)}, nil
		}, nil
	}
}
