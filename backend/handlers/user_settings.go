package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"novastream/config"
	"novastream/models"
	user_settings "novastream/services/user_settings"

	"github.com/gorilla/mux"
)

type userSettingsService interface {
	Get(userID string) (*models.UserSettings, error)
	GetWithDefaults(userID string, defaults models.UserSettings) (models.UserSettings, error)
	Update(userID string, settings models.UserSettings) error
	Delete(userID string) error
}

var _ userSettingsService = (*user_settings.Service)(nil)

type UserSettingsHandler struct {
	Service       userSettingsService
	Users         userService
	ConfigManager *config.Manager
}

func NewUserSettingsHandler(service userSettingsService, users userService, configManager *config.Manager) *UserSettingsHandler {
	return &UserSettingsHandler{
		Service:       service,
		Users:         users,
		ConfigManager: configManager,
	}
}

// GetSettings returns the user's settings merged with global defaults.
func (h *UserSettingsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	// Get global settings as defaults
	defaults := h.getDefaultsFromGlobal()

	settings, err := h.Service.GetWithDefaults(userID, defaults)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
}

// PutSettings updates the user's settings.
func (h *UserSettingsHandler) PutSettings(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	var settings models.UserSettings
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&settings); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.Service.Update(userID, settings); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(settings)
}

func (h *UserSettingsHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (h *UserSettingsHandler) requireUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	vars := mux.Vars(r)
	userID := strings.TrimSpace(vars["userID"])

	if userID == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return "", false
	}

	if h.Users != nil && !h.Users.Exists(userID) {
		http.Error(w, "user not found", http.StatusNotFound)
		return "", false
	}

	return userID, true
}

// getDefaultsFromGlobal extracts the per-user settings from global config as defaults.
func (h *UserSettingsHandler) getDefaultsFromGlobal() models.UserSettings {
	globalSettings, err := h.ConfigManager.Load()
	if err != nil {
		return models.DefaultUserSettings()
	}

	return models.UserSettings{
		Playback: models.PlaybackSettings{
			PreferredPlayer:           globalSettings.Playback.PreferredPlayer,
			PreferredAudioLanguage:    globalSettings.Playback.PreferredAudioLanguage,
			PreferredSubtitleLanguage: globalSettings.Playback.PreferredSubtitleLanguage,
			PreferredSubtitleMode:     globalSettings.Playback.PreferredSubtitleMode,
			UseLoadingScreen:          globalSettings.Playback.UseLoadingScreen,
			SubtitleSize:              globalSettings.Playback.SubtitleSize,
			RewindOnResumeFromPause:   globalSettings.Playback.RewindOnResumeFromPause,
			RewindOnPlaybackStart:     globalSettings.Playback.RewindOnPlaybackStart,
		},
		HomeShelves: models.HomeShelvesSettings{
			Shelves: convertShelves(globalSettings.HomeShelves.Shelves),
		},
		Filtering: models.FilterSettings{
			MaxSizeMovieGB:                   models.FloatPtr(globalSettings.Filtering.MaxSizeMovieGB),
			MaxSizeEpisodeGB:                 models.FloatPtr(globalSettings.Filtering.MaxSizeEpisodeGB),
			MaxResolution:                    globalSettings.Filtering.MaxResolution,
			HDRDVPolicy:                      models.HDRDVPolicy(globalSettings.Filtering.HDRDVPolicy),
			PrioritizeHdr:                    models.BoolPtr(globalSettings.Filtering.PrioritizeHdr),
			FilterOutTerms:                   globalSettings.Filtering.FilterOutTerms,
			PreferredTerms:                   globalSettings.Filtering.PreferredTerms,
			NonPreferredTerms:                globalSettings.Filtering.NonPreferredTerms,
			BypassFilteringForAIOStreamsOnly: models.BoolPtr(globalSettings.Filtering.BypassFilteringForAIOStreamsOnly),
		},
		LiveTV: models.LiveTVSettings{
			HiddenChannels:     []string{},
			FavoriteChannels:   []string{},
			SelectedCategories: []string{},
		},
	}
}

// convertShelves converts config.ShelfConfig to models.ShelfConfig
func convertShelves(configShelves []config.ShelfConfig) []models.ShelfConfig {
	result := make([]models.ShelfConfig, len(configShelves))
	for i, s := range configShelves {
		result[i] = models.ShelfConfig{
			ID:      s.ID,
			Name:    s.Name,
			Enabled: s.Enabled,
			Order:   s.Order,
			Type:    s.Type,
			ListURL: s.ListURL,
		}
	}
	return result
}
