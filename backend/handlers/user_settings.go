package handlers

import (
	"context"
	"encoding/json"
	"log"
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

// localLibraryLister is the minimal interface needed to fetch local media libraries.
type localLibraryLister interface {
	ListLibraries(ctx context.Context) ([]models.LocalMediaLibrary, error)
}

type UserSettingsHandler struct {
	Service       userSettingsService
	Users         userService
	ConfigManager *config.Manager
	LocalMedia    localLibraryLister
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
	maxStreams := globalSettings.Live.MaxStreams
	if maxStreams < 0 {
		maxStreams = 0
	}

	shelves := convertShelves(globalSettings.HomeShelves.Shelves)
	if h.LocalMedia != nil {
		if libs, err := h.LocalMedia.ListLibraries(context.Background()); err == nil {
			shelves = injectLocalLibraryShelves(shelves, libs)
		}
	}

	return models.UserSettings{
		Playback: models.PlaybackSettings{
			PreferredPlayer:           globalSettings.Playback.PreferredPlayer,
			PreferredAudioLanguage:    globalSettings.Playback.PreferredAudioLanguage,
			PreferredSubtitleLanguage: globalSettings.Playback.PreferredSubtitleLanguage,
			PreferredSubtitleMode:     globalSettings.Playback.PreferredSubtitleMode,
			PauseWhenAppInactive:      globalSettings.Playback.PauseWhenAppInactive,
			UseLoadingScreen:          globalSettings.Playback.UseLoadingScreen,
			SubtitleSize:              globalSettings.Playback.SubtitleSize,
			SubtitleColor:             globalSettings.Playback.SubtitleColor,
			SubtitleOpacity:           models.FloatPtr(globalSettings.Playback.SubtitleOpacity),
			SubtitleFont:              globalSettings.Playback.SubtitleFont,
			SubtitleOutlineEnabled:    models.BoolPtr(globalSettings.Playback.SubtitleOutlineEnabled),
			SubtitleOutlineColor:      globalSettings.Playback.SubtitleOutlineColor,
			SubtitleOutlineWeight:     models.FloatPtr(globalSettings.Playback.SubtitleOutlineWeight),
			SubtitleBackgroundEnabled: models.BoolPtr(globalSettings.Playback.SubtitleBackgroundEnabled),
			SubtitleBackgroundColor:   globalSettings.Playback.SubtitleBackgroundColor,
			SubtitleBackgroundOpacity: models.FloatPtr(globalSettings.Playback.SubtitleBackgroundOpacity),
			RewindOnResumeFromPause:   globalSettings.Playback.RewindOnResumeFromPause,
			RewindOnPlaybackStart:     globalSettings.Playback.RewindOnPlaybackStart,
			CreditsAutoSkip:           globalSettings.Playback.CreditsAutoSkip || globalSettings.Playback.CreditsDetection,
		},
		HomeShelves: models.HomeShelvesSettings{
			Shelves: shelves,
		},
		Filtering: models.FilterSettings{
			MaxSizeMovieGB:         models.FloatPtr(globalSettings.Filtering.MaxSizeMovieGB),
			MaxSizeEpisodeGB:       models.FloatPtr(globalSettings.Filtering.MaxSizeEpisodeGB),
			MaxResolution:          globalSettings.Filtering.MaxResolution,
			HDRDVPolicy:            models.HDRDVPolicy(globalSettings.Filtering.HDRDVPolicy),
			RequiredTerms:          globalSettings.Filtering.RequiredTerms,
			FilterOutTerms:         globalSettings.Filtering.FilterOutTerms,
			PreferredTerms:         globalSettings.Filtering.PreferredTerms,
			NonPreferredTerms:      globalSettings.Filtering.NonPreferredTerms,
			DownloadPreferredTerms: globalSettings.Filtering.DownloadPreferredTerms,
		},
		Display: models.DisplaySettings{
			BadgeVisibility:                  globalSettings.Display.BadgeVisibility,
			NavigationTabVisibility:          globalSettings.Display.NavigationTabVisibility,
			WatchStateIconStyle:              globalSettings.Display.WatchStateIconStyle,
			BypassFilteringForAIOStreamsOnly: models.BoolPtr(globalSettings.Display.BypassFilteringForAIOStreamsOnly),
			AppLanguage:                      globalSettings.Display.AppLanguage,
		},
		LiveTV: models.LiveTVSettings{
			HiddenChannels:     []string{},
			FavoriteChannels:   []string{},
			SelectedCategories: []string{},
			MaxStreams:         &maxStreams,
		},
	}
}

// convertShelves converts config.ShelfConfig to models.ShelfConfig
func convertShelves(configShelves []config.ShelfConfig) []models.ShelfConfig {
	result := make([]models.ShelfConfig, len(configShelves))
	for i, s := range configShelves {
		result[i] = models.ShelfConfig{
			ID:             s.ID,
			Name:           s.Name,
			Enabled:        s.Enabled,
			Order:          s.Order,
			Type:           s.Type,
			ListURL:        s.ListURL,
			TraktAccountID: s.TraktAccountID,
			TraktListType:  s.TraktListType,
			TraktListID:    s.TraktListID,
			Limit:          s.Limit,
			HideUnreleased: s.HideUnreleased,
		}
	}
	return result
}

// injectLocalLibraryShelves adds any local media libraries that are not yet present
// in the shelves list. Existing entries (from saved settings) are preserved as-is,
// so the admin can configure ordering and enable/disable without losing their changes.
func injectLocalLibraryShelves(shelves []models.ShelfConfig, libs []models.LocalMediaLibrary) []models.ShelfConfig {
	existing := make(map[string]bool, len(shelves))
	maxOrder := -1
	for _, s := range shelves {
		existing[s.ID] = true
		if s.Order > maxOrder {
			maxOrder = s.Order
		}
	}

	log.Printf("[user-settings] injectLocalLibraryShelves: %d existing shelves, %d libraries", len(shelves), len(libs))
	for _, lib := range libs {
		log.Printf("[user-settings] injectLocalLibraryShelves: library id=%s name=%q type=%s", lib.ID, lib.Name, lib.Type)
	}

	// Build a lookup from shelf ID to library name for renaming existing shelves
	libNameByID := make(map[string]string, len(libs))
	for _, lib := range libs {
		libNameByID["local-library-"+lib.ID] = lib.Name
	}

	result := make([]models.ShelfConfig, 0, len(shelves))
	for _, s := range shelves {
		if s.Type == "local-library" {
			if libName, ok := libNameByID[s.ID]; ok {
				want := "Recently Added - " + libName
				if s.Name != want {
					s.Name = want
				}
			}
		}
		result = append(result, s)
	}

	injected := 0
	for _, lib := range libs {
		id := "local-library-" + lib.ID
		if !existing[id] {
			result = append(result, models.ShelfConfig{
				ID:      id,
				Name:    "Recently Added - " + lib.Name,
				Enabled: true,
				Order:   maxOrder + 1 + injected,
				Type:    "local-library",
			})
			log.Printf("[user-settings] injectLocalLibraryShelves: injected new shelf id=%s name=%q", id, "Recently Added - "+lib.Name)
			injected++
		} else {
			log.Printf("[user-settings] injectLocalLibraryShelves: shelf id=%s already present, skipping", id)
		}
	}
	return result
}
