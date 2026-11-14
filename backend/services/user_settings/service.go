package user_settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"

	"novastream/config"
	"novastream/internal/datastore"
	"novastream/models"
)

var (
	ErrStorageDirRequired = errors.New("storage directory not provided")
	ErrUserIDRequired     = errors.New("user id is required")
)

// Service manages persistence and retrieval of per-user settings.
type Service struct {
	mu       sync.RWMutex
	path     string
	store    *datastore.DataStore
	settings map[string]models.UserSettings
}

// useDB returns true when the service is backed by PostgreSQL.
func (s *Service) useDB() bool { return s.store != nil }

// NewServiceWithStore creates a user settings service backed by PostgreSQL.
func NewServiceWithStore(store *datastore.DataStore) (*Service, error) {
	svc := &Service{
		store:    store,
		settings: make(map[string]models.UserSettings),
	}
	if err := svc.load(); err != nil {
		return nil, err
	}
	return svc, nil
}

// NewService creates a user settings service storing data inside the provided directory.
func NewService(storageDir string) (*Service, error) {
	if strings.TrimSpace(storageDir) == "" {
		return nil, ErrStorageDirRequired
	}

	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		return nil, fmt.Errorf("create user settings dir: %w", err)
	}

	svc := &Service{
		path:     filepath.Join(storageDir, "user_settings.json"),
		settings: make(map[string]models.UserSettings),
	}

	if err := svc.load(); err != nil {
		return nil, err
	}

	return svc, nil
}

// sanitizeLanguageCode strips stray quotes and whitespace from language codes.
// Some frontends may wrap values in single quotes (e.g., "'eng'" instead of "eng").
func sanitizeLanguageCode(code string) string {
	code = strings.TrimSpace(code)
	code = strings.Trim(code, "'\"")
	code = strings.TrimSpace(code)
	return strings.ToLower(code)
}

func sanitizeLanguageCodes(codes []string) []string {
	if len(codes) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(codes))
	result := make([]string, 0, len(codes))
	for _, raw := range codes {
		code := sanitizeLanguageCode(raw)
		if code == "" {
			continue
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		result = append(result, code)
	}
	return result
}

func sanitizeOptionalLanguageCodes(codes *[]string) *[]string {
	if codes == nil {
		return nil
	}
	sanitized := sanitizeLanguageCodes(*codes)
	if sanitized == nil {
		sanitized = []string{}
	}
	return &sanitized
}

func defaultPreferredAudioLanguage(code string) string {
	code = sanitizeLanguageCode(code)
	if code == "" {
		return "eng"
	}
	return code
}

// normalizeSubtitleMode maps legacy subtitle mode values to canonical ones.
func normalizeSubtitleMode(mode string) string {
	switch mode {
	case "auto":
		return "forced-only"
	case "always":
		return "on"
	case "":
		return "off"
	default:
		return mode
	}
}

// Get returns the user's settings, or nil if not set.
func (s *Service) Get(userID string) (*models.UserSettings, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUserIDRequired
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if settings, ok := s.settings[userID]; ok {
		copy := settings
		copy.SportsPreferences = append(json.RawMessage(nil), settings.SportsPreferences...)
		return &copy, nil
	}

	return nil, nil
}

// ApplyKidsProfileDefaults enables profile-scoped defaults for kids profiles
// without replacing an explicit user choice. In particular, a stored false
// SimpleMode value means M.O.M. Mode was deliberately disabled and is kept.
func (s *Service) ApplyKidsProfileDefaults(userIDs ...string) (int, error) {
	cleaned := make([]string, 0, len(userIDs))
	seen := make(map[string]struct{}, len(userIDs))
	for _, userID := range userIDs {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			return 0, ErrUserIDRequired
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		cleaned = append(cleaned, userID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	changed := 0
	for _, userID := range cleaned {
		settings := s.settings[userID]
		if settings.Display.SimpleMode != nil {
			continue
		}
		settings.Display.SimpleMode = models.BoolPtr(true)
		s.settings[userID] = settings
		changed++
	}
	if changed == 0 {
		return 0, nil
	}
	if err := s.saveLocked(); err != nil {
		return 0, err
	}
	return changed, nil
}

// HasOverrides returns true if the user has custom settings stored.
func (s *Service) HasOverrides(userID string) bool {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	_, exists := s.settings[userID]
	return exists
}

// GetUsersWithOverrides returns a map of userID -> hasOverrides for all users
// that have custom settings stored.
func (s *Service) GetUsersWithOverrides() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]bool)
	for userID := range s.settings {
		result[userID] = true
	}
	return result
}

// GetWithDefaults returns the user's settings merged with defaults.
// If the user has no custom settings, returns the defaults.
// If the user has settings but is missing fields, those are filled from defaults.
func (s *Service) GetWithDefaults(userID string, defaults models.UserSettings) (models.UserSettings, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return models.UserSettings{}, ErrUserIDRequired
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if settings, ok := s.settings[userID]; ok {
		var base config.RecommendationSettings
		if defaults.Recommendations != nil {
			base = *defaults.Recommendations
		}
		merged := base.Merge(settings.Recommendations).WithDefaults()
		settings.Recommendations = &merged
		// Sanitize language codes (strip stray quotes/whitespace)
		settings.SportsPreferences = append(json.RawMessage(nil), settings.SportsPreferences...)
		settings.Playback.PreferredAudioLanguage = sanitizeLanguageCode(settings.Playback.PreferredAudioLanguage)
		settings.Playback.PreferredSubtitleLanguage = sanitizeLanguageCode(settings.Playback.PreferredSubtitleLanguage)
		settings.Playback.AllowedTrackLanguages = sanitizeOptionalLanguageCodes(settings.Playback.AllowedTrackLanguages)
		settings.Playback.PreferredSubtitleMode = strings.TrimSpace(strings.Trim(settings.Playback.PreferredSubtitleMode, "'\""))
		settings.Metadata.PrimaryLanguage = sanitizeLanguageCode(settings.Metadata.PrimaryLanguage)

		// Fill in missing Playback fields from defaults
		// Empty strings indicate "not set" and should inherit from defaults
		if settings.Playback.PreferredPlayer == "" {
			settings.Playback.PreferredPlayer = defaults.Playback.PreferredPlayer
		}
		if settings.Playback.PreferredAudioLanguage == "" {
			settings.Playback.PreferredAudioLanguage = defaultPreferredAudioLanguage(defaults.Playback.PreferredAudioLanguage)
		}
		if settings.Playback.PreferredSubtitleLanguage == "" {
			settings.Playback.PreferredSubtitleLanguage = sanitizeLanguageCode(defaults.Playback.PreferredSubtitleLanguage)
		}
		if settings.Playback.AllowedTrackLanguages == nil {
			settings.Playback.AllowedTrackLanguages = sanitizeOptionalLanguageCodes(defaults.Playback.AllowedTrackLanguages)
		}
		if settings.Playback.PreferredSubtitleMode == "" {
			settings.Playback.PreferredSubtitleMode = defaults.Playback.PreferredSubtitleMode
		}
		settings.Playback.PreferredSubtitleMode = normalizeSubtitleMode(settings.Playback.PreferredSubtitleMode)
		// SubtitleSize of 0 means "use default"
		if settings.Playback.SubtitleSize == 0 {
			settings.Playback.SubtitleSize = defaults.Playback.SubtitleSize
		}
		if settings.Playback.SubtitleUseCropDetectPosition == nil {
			settings.Playback.SubtitleUseCropDetectPosition = defaults.Playback.SubtitleUseCropDetectPosition
		}
		if settings.Playback.SubtitleColor == "" {
			settings.Playback.SubtitleColor = defaults.Playback.SubtitleColor
		}
		if settings.Playback.SubtitleOpacity == nil {
			settings.Playback.SubtitleOpacity = defaults.Playback.SubtitleOpacity
		}
		if settings.Playback.SubtitleFont == nil {
			settings.Playback.SubtitleFont = defaults.Playback.SubtitleFont
		}
		if settings.Playback.SubtitleBold == nil {
			settings.Playback.SubtitleBold = defaults.Playback.SubtitleBold
		}
		if settings.Playback.SubtitleOutlineEnabled == nil {
			settings.Playback.SubtitleOutlineEnabled = defaults.Playback.SubtitleOutlineEnabled
		}
		if settings.Playback.SubtitleOutlineColor == "" {
			settings.Playback.SubtitleOutlineColor = defaults.Playback.SubtitleOutlineColor
		}
		if settings.Playback.SubtitleOutlineWeight == nil {
			settings.Playback.SubtitleOutlineWeight = defaults.Playback.SubtitleOutlineWeight
		}
		if settings.Playback.SubtitleBackgroundEnabled == nil {
			settings.Playback.SubtitleBackgroundEnabled = defaults.Playback.SubtitleBackgroundEnabled
		}
		if settings.Playback.SubtitleBackgroundColor == "" {
			settings.Playback.SubtitleBackgroundColor = defaults.Playback.SubtitleBackgroundColor
		}
		if settings.Playback.SubtitleBackgroundOpacity == nil {
			settings.Playback.SubtitleBackgroundOpacity = defaults.Playback.SubtitleBackgroundOpacity
		}
		if settings.Playback.SeekForwardSeconds == 0 {
			settings.Playback.SeekForwardSeconds = defaults.Playback.SeekForwardSeconds
		}
		if settings.Playback.SeekBackwardSeconds == 0 {
			settings.Playback.SeekBackwardSeconds = defaults.Playback.SeekBackwardSeconds
		}
		if settings.Playback.ForceAACTranscoding == nil {
			settings.Playback.ForceAACTranscoding = defaults.Playback.ForceAACTranscoding
		}
		if settings.Playback.AutoPlayTrailersTV == nil {
			settings.Playback.AutoPlayTrailersTV = defaults.Playback.AutoPlayTrailersTV
		}
		if settings.Playback.PauseWhenAppInactive == nil {
			settings.Playback.PauseWhenAppInactive = defaults.Playback.PauseWhenAppInactive
		}
		if settings.Playback.UseLoadingScreen == nil {
			settings.Playback.UseLoadingScreen = defaults.Playback.UseLoadingScreen
		}
		if settings.Playback.RewindOnResumeFromPause == nil {
			settings.Playback.RewindOnResumeFromPause = defaults.Playback.RewindOnResumeFromPause
		}
		if settings.Playback.RewindOnPlaybackStart == nil {
			settings.Playback.RewindOnPlaybackStart = defaults.Playback.RewindOnPlaybackStart
		}
		if settings.Playback.IntroAutoSkip == nil {
			settings.Playback.IntroAutoSkip = defaults.Playback.IntroAutoSkip
		}
		if settings.Playback.RecapAutoSkip == nil {
			settings.Playback.RecapAutoSkip = defaults.Playback.RecapAutoSkip
		}
		if settings.Playback.CreditsAutoSkip == nil {
			settings.Playback.CreditsAutoSkip = defaults.Playback.CreditsAutoSkip
		}
		if settings.Playback.ScrobbleStartDelaySeconds == nil {
			settings.Playback.ScrobbleStartDelaySeconds = defaults.Playback.ScrobbleStartDelaySeconds
		}
		if settings.Playback.DisablePrequeue == nil {
			settings.Playback.DisablePrequeue = defaults.Playback.DisablePrequeue
		}
		if settings.Playback.PrerollMode == "" {
			settings.Playback.PrerollMode = defaults.Playback.PrerollMode
			settings.Playback.PrerollAssetID = defaults.Playback.PrerollAssetID
		}
		if settings.Playback.PrerollMediaScope == "" {
			settings.Playback.PrerollMediaScope = defaults.Playback.PrerollMediaScope
		}
		if settings.Playback.PrerollSkipIfPrequeueReady == nil {
			settings.Playback.PrerollSkipIfPrequeueReady = defaults.Playback.PrerollSkipIfPrequeueReady
		}
		if settings.Playback.StreamMigrationEnabled == nil {
			settings.Playback.StreamMigrationEnabled = defaults.Playback.StreamMigrationEnabled
		}
		if settings.Playback.CreditsDetectionEnabled == nil {
			settings.Playback.CreditsDetectionEnabled = defaults.Playback.CreditsDetectionEnabled
		}
		if settings.Playback.MatchFrameRate == nil {
			settings.Playback.MatchFrameRate = defaults.Playback.MatchFrameRate
		}
		if settings.Playback.LiveClosedCaptionExtraction == nil {
			settings.Playback.LiveClosedCaptionExtraction = defaults.Playback.LiveClosedCaptionExtraction
		}
		if settings.Metadata.PrimaryLanguage == "" {
			settings.Metadata.PrimaryLanguage = defaults.Metadata.PrimaryLanguage
		}
		if settings.Filtering.PreferredScraper == nil {
			settings.Filtering.PreferredScraper = defaults.Filtering.PreferredScraper
		}
		if settings.Filtering.ServicePriority == nil {
			settings.Filtering.ServicePriority = defaults.Filtering.ServicePriority
		}
		if settings.Filtering.AdaptivePlaybackEnabled == nil {
			settings.Filtering.AdaptivePlaybackEnabled = defaults.Filtering.AdaptivePlaybackEnabled
		}
		if settings.Filtering.AdaptiveTargetBufferFactor == nil {
			settings.Filtering.AdaptiveTargetBufferFactor = defaults.Filtering.AdaptiveTargetBufferFactor
		}

		// Fill in missing Display fields from defaults without overwriting explicit user overrides.
		if settings.Display.BadgeVisibility == nil {
			settings.Display.BadgeVisibility = defaults.Display.BadgeVisibility
		}
		if settings.Display.NavigationTabVisibility == nil {
			settings.Display.NavigationTabVisibility = defaults.Display.NavigationTabVisibility
		}
		if settings.Display.WatchStateIconStyle == "" {
			settings.Display.WatchStateIconStyle = defaults.Display.WatchStateIconStyle
		}
		if settings.Display.IncludeUnreleasedMoviesInLists == nil {
			settings.Display.IncludeUnreleasedMoviesInLists = defaults.Display.IncludeUnreleasedMoviesInLists
		}
		if settings.Display.IncludeUnreleasedShowsInLists == nil {
			settings.Display.IncludeUnreleasedShowsInLists = defaults.Display.IncludeUnreleasedShowsInLists
		}
		if settings.Display.IncludeUnreleasedMoviesInSearch == nil {
			settings.Display.IncludeUnreleasedMoviesInSearch = defaults.Display.IncludeUnreleasedMoviesInSearch
		}
		if settings.Display.IncludeUnreleasedShowsInSearch == nil {
			settings.Display.IncludeUnreleasedShowsInSearch = defaults.Display.IncludeUnreleasedShowsInSearch
		}
		if settings.Display.BypassFilteringForAIOStreamsOnly == nil {
			settings.Display.BypassFilteringForAIOStreamsOnly = defaults.Display.BypassFilteringForAIOStreamsOnly
		}
		if settings.Display.ShowStreamSourceInfo == nil {
			settings.Display.ShowStreamSourceInfo = defaults.Display.ShowStreamSourceInfo
		}
		if settings.Display.DisableMobileTopCarousel == nil {
			settings.Display.DisableMobileTopCarousel = defaults.Display.DisableMobileTopCarousel
		}
		if settings.Display.HideContinueWatchingHeroMetadata == nil {
			settings.Display.HideContinueWatchingHeroMetadata = defaults.Display.HideContinueWatchingHeroMetadata
		}
		if settings.Display.MoveDetailsRatingsToMetadata == nil {
			settings.Display.MoveDetailsRatingsToMetadata = defaults.Display.MoveDetailsRatingsToMetadata
		}
		if settings.Display.HideDetailsPoster == nil {
			settings.Display.HideDetailsPoster = defaults.Display.HideDetailsPoster
		}
		if settings.Display.HideTVDrawerRail == nil {
			settings.Display.HideTVDrawerRail = defaults.Display.HideTVDrawerRail
		}
		if settings.Display.SimpleMode == nil {
			settings.Display.SimpleMode = defaults.Display.SimpleMode
		}
		if settings.Display.SimpleModeHomeShelves == nil {
			settings.Display.SimpleModeHomeShelves = defaults.Display.SimpleModeHomeShelves
		}
		if settings.Display.DisableTVHomeCardDimming == nil {
			settings.Display.DisableTVHomeCardDimming = defaults.Display.DisableTVHomeCardDimming
		}
		if settings.Display.EnableAnimations == nil {
			settings.Display.EnableAnimations = defaults.Display.EnableAnimations
		}
		if settings.Display.EnableHeroArtPanning == nil {
			settings.Display.EnableHeroArtPanning = defaults.Display.EnableHeroArtPanning
		}
		if settings.Display.EnableHeroArtRotation == nil {
			settings.Display.EnableHeroArtRotation = defaults.Display.EnableHeroArtRotation
		}
		if settings.Display.ShowSeriesBackdropForMissingEpisodeArt == nil {
			settings.Display.ShowSeriesBackdropForMissingEpisodeArt = defaults.Display.ShowSeriesBackdropForMissingEpisodeArt
		}
		if settings.Display.AppLanguage == "" {
			settings.Display.AppLanguage = defaults.Display.AppLanguage
		}
		if settings.Display.Appearance.FontScale == nil {
			settings.Display.Appearance.FontScale = defaults.Display.Appearance.FontScale
		}
		if settings.Display.Appearance.AccentColor == "" {
			settings.Display.Appearance.AccentColor = defaults.Display.Appearance.AccentColor
		}
		if settings.Display.Appearance.TextColor == "" {
			settings.Display.Appearance.TextColor = defaults.Display.Appearance.TextColor
		}
		if settings.Display.Appearance.SecondaryTextColor == "" {
			settings.Display.Appearance.SecondaryTextColor = defaults.Display.Appearance.SecondaryTextColor
		}
		if settings.Display.Appearance.BackgroundColor == "" {
			settings.Display.Appearance.BackgroundColor = defaults.Display.Appearance.BackgroundColor
		}
		if settings.Display.Appearance.ModalBackgroundColor == "" {
			settings.Display.Appearance.ModalBackgroundColor = defaults.Display.Appearance.ModalBackgroundColor
		}
		if settings.Display.Appearance.ButtonStyle == "" {
			settings.Display.Appearance.ButtonStyle = defaults.Display.Appearance.ButtonStyle
		}
		if settings.Display.Appearance.ButtonRadius == "" {
			settings.Display.Appearance.ButtonRadius = defaults.Display.Appearance.ButtonRadius
		}
		if settings.Display.Appearance.HighContrast == nil {
			settings.Display.Appearance.HighContrast = defaults.Display.Appearance.HighContrast
		}
		if settings.Display.Appearance.ReduceOverlays == nil {
			settings.Display.Appearance.ReduceOverlays = defaults.Display.Appearance.ReduceOverlays
		}
		settings.HomeShelves.Views = config.MergeHomeViews(defaults.HomeShelves.Views, settings.HomeShelves.Views)
		settings.HomeShelves.Shelves = mergeShelvesWithDefaults(settings.HomeShelves.Shelves, defaults.HomeShelves.Shelves)
		if settings.HomeShelves.ExploreCardPosition == "" {
			settings.HomeShelves.ExploreCardPosition = string(defaults.HomeShelves.ExploreCardPosition)
		}
		if settings.HomeShelves.ItemCap <= 0 {
			settings.HomeShelves.ItemCap = defaults.HomeShelves.ItemCap
			if settings.HomeShelves.ItemCap <= 0 {
				settings.HomeShelves.ItemCap = 20
			}
		}
		if settings.HomeShelves.HomeShelfFocusModel == "" {
			settings.HomeShelves.HomeShelfFocusModel = defaults.HomeShelves.HomeShelfFocusModel
		}
		if settings.HomeShelves.HomeShelfFocusModel != "center" && settings.HomeShelves.HomeShelfFocusModel != "right" {
			settings.HomeShelves.HomeShelfFocusModel = "left"
		}
		if settings.HomeShelves.MobileTopShelfMode == "" {
			settings.HomeShelves.MobileTopShelfMode = defaults.HomeShelves.MobileTopShelfMode
		}
		if settings.HomeShelves.MobileTopShelfSourceID == "" {
			settings.HomeShelves.MobileTopShelfSourceID = defaults.HomeShelves.MobileTopShelfSourceID
		}
		if settings.HomeShelves.TVTopShelfMode == "" {
			settings.HomeShelves.TVTopShelfMode = defaults.HomeShelves.TVTopShelfMode
		}
		if settings.HomeShelves.TVTopShelfSourceID == "" {
			settings.HomeShelves.TVTopShelfSourceID = defaults.HomeShelves.TVTopShelfSourceID
		}
		if settings.HomeShelves.DisableTvLandscapeCardExpansion == nil {
			settings.HomeShelves.DisableTvLandscapeCardExpansion = defaults.HomeShelves.DisableTvLandscapeCardExpansion
			if settings.HomeShelves.DisableTvLandscapeCardExpansion == nil {
				settings.HomeShelves.DisableTvLandscapeCardExpansion = models.BoolPtr(false)
			}
		}
		if settings.HomeShelves.HomeShelfScale == nil || *settings.HomeShelves.HomeShelfScale <= 0 {
			settings.HomeShelves.HomeShelfScale = defaults.HomeShelves.HomeShelfScale
			if settings.HomeShelves.HomeShelfScale == nil || *settings.HomeShelves.HomeShelfScale <= 0 {
				settings.HomeShelves.HomeShelfScale = models.FloatPtr(1.0)
			}
		}
		if *settings.HomeShelves.HomeShelfScale < 0.5 {
			settings.HomeShelves.HomeShelfScale = models.FloatPtr(0.5)
		} else if *settings.HomeShelves.HomeShelfScale > 1.0 {
			settings.HomeShelves.HomeShelfScale = models.FloatPtr(1.0)
		}
		if settings.HomeShelves.HomeHeroScale == nil || *settings.HomeShelves.HomeHeroScale <= 0 {
			settings.HomeShelves.HomeHeroScale = defaults.HomeShelves.HomeHeroScale
			if settings.HomeShelves.HomeHeroScale == nil || *settings.HomeShelves.HomeHeroScale <= 0 {
				settings.HomeShelves.HomeHeroScale = models.FloatPtr(1.0)
			}
		}
		if *settings.HomeShelves.HomeHeroScale < 0.5 {
			settings.HomeShelves.HomeHeroScale = models.FloatPtr(0.5)
		} else if *settings.HomeShelves.HomeHeroScale > 1.0 {
			settings.HomeShelves.HomeHeroScale = models.FloatPtr(1.0)
		}
		return settings, nil
	}

	// Sanitize defaults too
	defaults.Playback.PreferredAudioLanguage = defaultPreferredAudioLanguage(defaults.Playback.PreferredAudioLanguage)
	defaults.Playback.PreferredSubtitleLanguage = sanitizeLanguageCode(defaults.Playback.PreferredSubtitleLanguage)
	return defaults, nil
}

func mergeShelvesWithDefaults(shelves []models.ShelfConfig, defaults []models.ShelfConfig) []models.ShelfConfig {
	if len(shelves) == 0 {
		if len(defaults) > 0 {
			return append([]models.ShelfConfig(nil), defaults...)
		}
		merged, _ := models.EnsureDefaultHomeShelves(nil)
		return merged
	}
	if len(defaults) == 0 {
		merged, _ := models.EnsureDefaultHomeShelves(shelves)
		return merged
	}
	merged := append([]models.ShelfConfig(nil), shelves...)
	existing := make(map[string]bool, len(merged))
	for _, sh := range merged {
		existing[sh.ID] = true
	}
	for _, sh := range defaults {
		if !existing[sh.ID] {
			merged = append(merged, sh)
			existing[sh.ID] = true
		}
	}
	return merged
}

// reconcileProfileHomeShelves repairs a profile's stored home-shelf override during load.
//
// An empty shelf list means "inherit the global shelf list", so the built-in defaults
// must never be materialized into it. Doing so turns an inheriting profile into one that
// permanently appears to deviate from a customized global (e.g. once the admin disables
// the calendar shelf globally, every profile still carrying the materialized default set
// is flagged as an override forever). A stored list that exactly matches the built-in
// defaults was written by that earlier buggy migration; revert it to the inherit state.
// Genuinely customized profiles keep their shelves, with any missing built-ins backfilled.
//
// Returns true if the stored shelves were modified.
func reconcileProfileHomeShelves(us *models.UserSettings) bool {
	shelves := us.HomeShelves.Shelves
	if len(shelves) == 0 {
		// No stored override → fully inherits global. Never materialize defaults here.
		return false
	}
	changed := models.MigrateLibraryShelfConfigs(shelves)
	if deduped, duplicatesRemoved := dedupeShelfConfigsByID(shelves); duplicatesRemoved {
		shelves = deduped
		us.HomeShelves.Shelves = shelves
		changed = true
	}
	if shelvesArePristineDefaults(shelves) {
		// Auto-materialized default set → restore the inherit state.
		us.HomeShelves.Shelves = nil
		return true
	}
	if merged, defaultsChanged := models.EnsureDefaultHomeShelves(shelves); defaultsChanged {
		us.HomeShelves.Shelves = merged
		return true
	}
	return changed
}

func dedupeShelfConfigsByID(shelves []models.ShelfConfig) ([]models.ShelfConfig, bool) {
	seen := make(map[string]struct{}, len(shelves))
	deduped := make([]models.ShelfConfig, 0, len(shelves))
	for _, shelf := range shelves {
		if _, exists := seen[shelf.ID]; exists {
			continue
		}
		seen[shelf.ID] = struct{}{}
		deduped = append(deduped, shelf)
	}
	return deduped, len(deduped) != len(shelves)
}

// shelvesArePristineDefaults reports whether shelves is exactly the built-in default
// set, in default order and with default field values. Such a list provides no override
// beyond what inheriting from global would (missing global shelves are merged back in on
// read), so it is safe to drop in favor of inheritance.
func shelvesArePristineDefaults(shelves []models.ShelfConfig) bool {
	defaults := models.DefaultHomeShelfConfigs()
	if len(shelves) != len(defaults) {
		return false
	}
	for i := range defaults {
		if !shelfConfigEqualsDefault(shelves[i], defaults[i]) {
			return false
		}
	}
	return true
}

func shelfConfigEqualsDefault(stored, def models.ShelfConfig) bool {
	return stored.ID == def.ID &&
		stored.Name == def.Name &&
		stored.Enabled == def.Enabled &&
		stored.Order == def.Order &&
		stored.Type == def.Type &&
		stored.LibraryID == def.LibraryID &&
		stored.ListURL == def.ListURL &&
		stored.AddonManifestURL == def.AddonManifestURL &&
		stored.AddonCatalogType == def.AddonCatalogType &&
		stored.AddonCatalogID == def.AddonCatalogID &&
		stored.AddonName == def.AddonName &&
		stored.TMDBSourceType == def.TMDBSourceType &&
		stored.TMDBSourceID == def.TMDBSourceID &&
		stored.TMDBSourceName == def.TMDBSourceName &&
		stored.TMDBMediaType == def.TMDBMediaType &&
		stored.TMDBDiscoverQuery == def.TMDBDiscoverQuery &&
		stored.TraktAccountID == def.TraktAccountID &&
		stored.TraktListType == def.TraktListType &&
		stored.TraktListID == def.TraktListID &&
		stored.SimklAccountID == def.SimklAccountID &&
		stored.SimklListType == def.SimklListType &&
		stored.SimklMediaType == def.SimklMediaType &&
		stored.LetterboxdListID == def.LetterboxdListID &&
		stored.LetterboxdListURL == def.LetterboxdListURL &&
		stored.Limit == def.Limit &&
		stored.HideUnreleased == def.HideUnreleased &&
		stored.Sort == def.Sort &&
		calendarSettingsValueEqual(stored.CalendarSources, def.CalendarSources)
}

// calendarSettingsValueEqual compares calendar source toggles by value, treating an
// unset (nil) toggle as off so a stored "{}" matches a zero-value default.
func calendarSettingsValueEqual(a, b models.CalendarSettings) bool {
	if calBoolValue(a.Watchlist) != calBoolValue(b.Watchlist) ||
		calBoolValue(a.History) != calBoolValue(b.History) ||
		calBoolValue(a.Trending) != calBoolValue(b.Trending) ||
		calBoolValue(a.TopTrending) != calBoolValue(b.TopTrending) ||
		calBoolValue(a.MDBLists) != calBoolValue(b.MDBLists) {
		return false
	}
	// Per-shelf MDBList calendar toggles are a genuine customization, never a default.
	return len(a.MDBListShelves) == 0 && len(b.MDBListShelves) == 0
}

func calBoolValue(b *bool) bool {
	return b != nil && *b
}

// Update saves the user's settings.
// If the settings are empty (no actual overrides), the user entry is deleted instead.
func (s *Service) Update(userID string, settings models.UserSettings) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ErrUserIDRequired
	}

	// Sanitize language codes on save to prevent stray quotes from persisting
	settings.Playback.PreferredAudioLanguage = sanitizeLanguageCode(settings.Playback.PreferredAudioLanguage)
	settings.Playback.PreferredSubtitleLanguage = sanitizeLanguageCode(settings.Playback.PreferredSubtitleLanguage)
	settings.Playback.AllowedTrackLanguages = sanitizeOptionalLanguageCodes(settings.Playback.AllowedTrackLanguages)
	settings.Playback.PreferredSubtitleMode = strings.TrimSpace(strings.Trim(settings.Playback.PreferredSubtitleMode, "'\""))
	settings.Metadata.PrimaryLanguage = sanitizeLanguageCode(settings.Metadata.PrimaryLanguage)
	if len(settings.Display.NavigationTabVisibility) > 0 {
		settings.Display.NavigationTabVisibilityIncludesSystemTabs = true
		settings.Display.NavigationTabVisibilityIncludesWatchlist = true
		settings.Display.NavigationTabVisibilityIncludesSports = true
	}

	log.Printf("[user-settings] Update(%q): subMode=%q, audioLang=%q, subLang=%q",
		userID, settings.Playback.PreferredSubtitleMode, settings.Playback.PreferredAudioLanguage, settings.Playback.PreferredSubtitleLanguage)

	s.mu.Lock()
	defer s.mu.Unlock()

	// If settings are empty, delete the entry instead of saving
	// General settings PUTs (including older clients) cannot replace sports data.
	settings.SportsPreferences = s.settings[userID].SportsPreferences
	// Older clients do not know this section and must not reset profile choices.
	if settings.Recommendations == nil {
		settings.Recommendations = s.settings[userID].Recommendations
	}
	// Intro and recap auto-skip change only through UpdateSkipping (PATCH
	// /settings/skipping). Clients send back the whole document they last
	// read, often from a cache older than the latest toggle, so a full PUT
	// must not overwrite these two values, whether it omits them or not.
	settings.Playback.IntroAutoSkip = s.settings[userID].Playback.IntroAutoSkip
	settings.Playback.RecapAutoSkip = s.settings[userID].Playback.RecapAutoSkip
	if isSettingsEmpty(settings) {
		log.Printf("[user-settings] Update(%q): settings empty, deleting entry", userID)
		delete(s.settings, userID)
	} else {
		log.Printf("[user-settings] Update(%q): storing in cache", userID)
		s.settings[userID] = settings
	}

	return s.saveLocked()
}

// hasExplicitPointerOverride makes pointer-backed settings automatically count
// as overrides, including explicit false, zero, and empty values. Pointers to
// empty nested setting structs do not count until one of their fields is set.
func hasExplicitPointerOverride(value reflect.Value) bool {
	for value.IsValid() && value.Kind() == reflect.Interface {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return false
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return false
		}
		if value.Elem().Kind() != reflect.Struct {
			return true
		}
		return hasExplicitPointerOverride(value.Elem())
	}
	if value.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < value.NumField(); i++ {
		if hasExplicitPointerOverride(value.Field(i)) {
			return true
		}
	}
	return false
}

// isSettingsEmpty checks if user settings have no actual values set.
func isSettingsEmpty(s models.UserSettings) bool {
	if len(s.SportsPreferences) != 0 {
		return false
	}
	if hasExplicitPointerOverride(reflect.ValueOf(s)) {
		return false
	}
	// Check Playback - if any field is non-default, not empty
	if s.Playback.PreferredPlayer != "" ||
		s.Playback.PreferredAudioLanguage != "" ||
		s.Playback.PreferredSubtitleLanguage != "" ||
		s.Playback.AllowedTrackLanguages != nil ||
		s.Playback.PreferredSubtitleMode != "" ||
		s.Playback.PauseWhenAppInactive != nil ||
		s.Playback.UseLoadingScreen != nil ||
		s.Playback.SubtitleSize != 0 ||
		s.Playback.SubtitleUseCropDetectPosition != nil ||
		s.Playback.SubtitleColor != "" ||
		s.Playback.SubtitleOpacity != nil ||
		s.Playback.SubtitleFont != nil ||
		s.Playback.SubtitleBold != nil ||
		s.Playback.SubtitleOutlineEnabled != nil ||
		s.Playback.SubtitleOutlineColor != "" ||
		s.Playback.SubtitleOutlineWeight != nil ||
		s.Playback.SubtitleBackgroundEnabled != nil ||
		s.Playback.SubtitleBackgroundColor != "" ||
		s.Playback.SubtitleBackgroundOpacity != nil ||
		s.Playback.SeekForwardSeconds != 0 ||
		s.Playback.SeekBackwardSeconds != 0 ||
		s.Playback.ForceAACTranscoding != nil ||
		s.Playback.AutoPlayTrailersTV != nil ||
		s.Playback.RewindOnResumeFromPause != nil ||
		s.Playback.RewindOnPlaybackStart != nil ||
		s.Playback.StreamMigrationEnabled != nil ||
		s.Playback.MatchFrameRate != nil ||
		s.Playback.LiveClosedCaptionExtraction != nil ||
		s.Playback.DisablePrequeue != nil ||
		s.Playback.CreditsAutoSkip != nil ||
		s.Playback.IgnoreDVCompatibilityCheck != nil ||
		s.Playback.CreditsDetectionEnabled != nil ||
		s.Playback.MaxConcurrentStreams != nil {
		return false
	}
	if s.Playback.PrerollMode != "" || s.Playback.PrerollAssetID != "" ||
		s.Playback.PrerollMediaScope != "" || s.Playback.PrerollSkipIfPrequeueReady != nil {
		return false
	}

	// Check HomeShelves
	if len(s.HomeShelves.Shelves) > 0 ||
		s.HomeShelves.ExploreCardPosition != "" ||
		s.HomeShelves.ItemCap != 0 ||
		s.HomeShelves.HomeShelfFocusModel != "" ||
		s.HomeShelves.MobileTopShelfMode != "" ||
		s.HomeShelves.MobileTopShelfSourceID != "" ||
		s.HomeShelves.TVTopShelfMode != "" ||
		s.HomeShelves.TVTopShelfSourceID != "" ||
		s.HomeShelves.DisableTvLandscapeCardExpansion != nil ||
		s.HomeShelves.HomeShelfScale != nil ||
		s.HomeShelves.HomeHeroScale != nil ||
		len(s.HomeShelves.Views) > 0 ||
		s.HomeShelves.ExcludeUpcomingFromContinue != nil {
		return false
	}

	// Check Filtering - pointer fields
	if s.Filtering.MaxSizeMovieGB != nil ||
		s.Filtering.MaxSizeEpisodeGB != nil ||
		s.Filtering.MaxResolution != nil ||
		s.Filtering.HDRDVPolicy != "" ||
		s.Filtering.RequiredTerms != nil ||
		s.Filtering.FilterOutTerms != nil ||
		s.Filtering.PreferredTerms != nil ||
		s.Filtering.NonPreferredTerms != nil ||
		s.Filtering.DownloadPreferredTerms != nil ||
		s.Filtering.PreferredScraper != nil ||
		s.Filtering.ServicePriority != nil ||
		s.Filtering.UnknownTrackPolicy != "" ||
		s.Filtering.AdaptivePlaybackEnabled != nil ||
		s.Filtering.AdaptiveTargetBufferFactor != nil ||
		s.Filtering.RealDebridRestrictedTermsFilterEnabled != nil ||
		s.Filtering.SplitByService != nil ||
		s.Filtering.Debrid != nil ||
		s.Filtering.Usenet != nil {
		return false
	}

	// Check LiveTV
	if len(s.LiveTV.HiddenChannels) > 0 ||
		len(s.LiveTV.FavoriteChannels) > 0 ||
		len(s.LiveTV.SelectedCategories) > 0 ||
		s.LiveTV.Mode != nil ||
		s.LiveTV.PlaylistURL != nil ||
		s.LiveTV.ManifestURL != nil ||
		s.LiveTV.ProxyURL != nil ||
		len(s.LiveTV.Sources) > 0 ||
		len(s.LiveTV.PlaylistSources) > 0 ||
		s.LiveTV.SourcesOverride != nil ||
		s.LiveTV.XtreamHost != nil ||
		s.LiveTV.XtreamUsername != nil ||
		s.LiveTV.XtreamPassword != nil ||
		s.LiveTV.StalkerPortalURL != nil ||
		s.LiveTV.StalkerMAC != nil ||
		s.LiveTV.StalkerSerialNumber != nil ||
		s.LiveTV.StalkerDeviceID != nil ||
		s.LiveTV.StalkerDeviceID2 != nil ||
		s.LiveTV.StalkerSignature != nil ||
		s.LiveTV.StalkerModel != nil ||
		s.LiveTV.MaxStreams != nil ||
		s.LiveTV.PlaylistCacheTTLHours != nil ||
		s.LiveTV.ProbeSizeMB != nil ||
		s.LiveTV.AnalyzeDurationSec != nil ||
		s.LiveTV.LowLatency != nil ||
		s.LiveTV.StreamFormat != nil ||
		s.LiveTV.Filtering != nil ||
		s.LiveTV.EPG != nil {
		return false
	}

	// Check Display
	if s.Display.BadgeVisibility != nil ||
		s.Display.NavigationTabVisibility != nil ||
		s.Display.WatchStateIconStyle != "" ||
		s.Display.IncludeUnreleasedMoviesInLists != nil ||
		s.Display.IncludeUnreleasedShowsInLists != nil ||
		s.Display.IncludeUnreleasedMoviesInSearch != nil ||
		s.Display.IncludeUnreleasedShowsInSearch != nil ||
		s.Display.BypassFilteringForAIOStreamsOnly != nil ||
		s.Display.ShowStreamSourceInfo != nil ||
		s.Display.DisableMobileTopCarousel != nil ||
		s.Display.HideContinueWatchingHeroMetadata != nil ||
		s.Display.MoveDetailsRatingsToMetadata != nil ||
		s.Display.HideDetailsPoster != nil ||
		s.Display.HideTVDrawerRail != nil ||
		s.Display.SimpleMode != nil ||
		s.Display.SimpleModeHomeShelves != nil ||
		s.Display.DisableTVHomeCardDimming != nil ||
		s.Display.EnableAnimations != nil ||
		s.Display.EnableHeroArtPanning != nil ||
		s.Display.EnableHeroArtRotation != nil ||
		s.Display.ShowSeriesBackdropForMissingEpisodeArt != nil ||
		s.Display.BlurUnwatchedEpisodeThumbnails != nil ||
		s.Display.BlurUnwatchedEpisodeThumbnailsIncludeCurrent != nil ||
		s.Display.BlurUnwatchedEpisodeOverviews != nil ||
		s.Display.BlurUnwatchedEpisodeOverviewsIncludeCurrent != nil ||
		s.Display.AppLanguage != "" ||
		s.Display.Appearance.FontScale != nil ||
		s.Display.Appearance.AccentColor != "" ||
		s.Display.Appearance.TextColor != "" ||
		s.Display.Appearance.SecondaryTextColor != "" ||
		s.Display.Appearance.BackgroundColor != "" ||
		s.Display.Appearance.ModalBackgroundColor != "" ||
		s.Display.Appearance.ButtonStyle != "" ||
		s.Display.Appearance.ButtonRadius != "" ||
		s.Display.Appearance.HighContrast != nil ||
		s.Display.Appearance.ReduceOverlays != nil {
		return false
	}

	// Check Playback - MaxResultsPerResolution
	if s.Playback.MaxResultsPerResolution != nil {
		return false
	}

	// Check Metadata
	if s.Metadata.PrimaryLanguage != "" {
		return false
	}

	// Check Network
	if s.Network.HomeWifiSSID != "" ||
		s.Network.HomeBackendUrl != "" ||
		s.Network.RemoteBackendUrl != "" {
		return false
	}

	// Check AnimeFiltering
	if s.AnimeFiltering.AnimeLanguageEnabled != nil ||
		s.AnimeFiltering.AnimePreferredLanguage != nil {
		return false
	}

	// Check Ranking
	if s.Ranking != nil && (len(s.Ranking.Criteria) > 0 ||
		s.Ranking.NewestReleaseFirst != nil ||
		s.Ranking.SplitByService != nil ||
		s.Ranking.Debrid != nil ||
		s.Ranking.Usenet != nil) {
		return false
	}

	// Check Calendar
	if s.Calendar.Watchlist != nil ||
		s.Calendar.History != nil ||
		s.Calendar.Trending != nil ||
		s.Calendar.TopTrending != nil ||
		s.Calendar.MDBLists != nil ||
		len(s.Calendar.MDBListShelves) > 0 {
		return false
	}

	return true
}

func appearanceSettingsSet(a models.AppearanceSettings) bool {
	return a.FontScale != nil ||
		a.AccentColor != "" ||
		a.TextColor != "" ||
		a.SecondaryTextColor != "" ||
		a.BackgroundColor != "" ||
		a.ModalBackgroundColor != "" ||
		a.ButtonStyle != "" ||
		a.ButtonRadius != "" ||
		a.HighContrast != nil ||
		a.ReduceOverlays != nil
}

// Delete removes a user's settings.
func (s *Service) Delete(userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ErrUserIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.settings[userID]; !exists {
		return nil
	}

	delete(s.settings, userID)

	return s.saveLocked()
}

// ClearAppearanceOverrides removes profile-level display appearance overrides.
// It is intended for one-time cleanup of appearance values previously saved by
// the removed mobile settings UI.
func (s *Service) ClearAppearanceOverrides() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	changed := 0
	for userID, settings := range s.settings {
		if !appearanceSettingsSet(settings.Display.Appearance) {
			continue
		}
		settings.Display.Appearance = models.AppearanceSettings{}
		if isSettingsEmpty(settings) {
			delete(s.settings, userID)
		} else {
			s.settings[userID] = settings
		}
		changed++
	}

	if changed == 0 {
		return 0, nil
	}
	return changed, s.saveLocked()
}

func (s *Service) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.useDB() {
		if cleared, err := s.store.UserSettings().ClearCleanPostersOverrides(context.Background()); err != nil {
			log.Printf("[user-settings] warning: failed to clear clean posters overrides: %v", err)
		} else if cleared > 0 {
			log.Printf("[user-settings] cleared clean posters overrides for %d users", cleared)
		}

		settings, err := s.store.UserSettings().List(context.Background())
		if err != nil {
			return fmt.Errorf("load user settings from db: %w", err)
		}
		s.settings = settings
		if s.settings == nil {
			s.settings = make(map[string]models.UserSettings)
		}

		// Apply migrations to DB-loaded settings
		needsSave := false
		for userID, us := range s.settings {
			changed := false
			if !us.Display.NavigationTabVisibilityIncludesSports {
				if tabs, tabsChanged := models.AddMissingSportsNavigationTab(us.Display.NavigationTabVisibility); tabsChanged {
					us.Display.NavigationTabVisibility = tabs
				}
				us.Display.NavigationTabVisibilityIncludesSports = true
				changed = true
			}
			if reconcileProfileHomeShelves(&us) {
				changed = true
			}
			for i := range us.HomeShelves.Shelves {
				id := us.HomeShelves.Shelves[i].ID
				if (id == "trending-movies" || id == "trending-tv") && us.HomeShelves.Shelves[i].HideUnreleased {
					us.HomeShelves.Shelves[i].HideUnreleased = false
					changed = true
				}
			}
			if changed {
				s.settings[userID] = us
				needsSave = true
			}
		}

		for userID, us := range s.settings {
			log.Printf("[user-settings] load: user=%q subMode=%q audioLang=%q subLang=%q",
				userID, us.Playback.PreferredSubtitleMode, us.Playback.PreferredAudioLanguage, us.Playback.PreferredSubtitleLanguage)
		}

		if needsSave {
			log.Printf("[user-settings] persisting migrated user settings to db")
			if err := s.syncToDB(); err != nil {
				log.Printf("[user-settings] warning: failed to persist migration: %v", err)
			}
		}
		return nil
	}

	// Read the whole file and release it before a migration rewrites it:
	// Windows cannot rename over a file that is still open.
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.settings = make(map[string]models.UserSettings)
		return nil
	}
	if err != nil {
		return fmt.Errorf("read user settings: %w", err)
	}
	if len(data) == 0 {
		s.settings = make(map[string]models.UserSettings)
		return nil
	}

	// First decode into raw map to apply migrations before struct unmarshalling
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMap); err != nil {
		return fmt.Errorf("decode user settings (raw): %w", err)
	}
	needsSave := false
	for userID, rawUser := range rawMap {
		var userRaw map[string]interface{}
		if err := json.Unmarshal(rawUser, &userRaw); err != nil {
			continue
		}
		// Check if migration is needed (old field exists in filtering)
		if filteringRaw, ok := userRaw["filtering"].(map[string]interface{}); ok {
			if _, has := filteringRaw["bypassFilteringForAioStreamsOnly"]; has {
				config.MigrateRawUserSettings(userRaw)
				migrated, _ := json.Marshal(userRaw)
				rawMap[userID] = migrated
				needsSave = true
			} else if _, has := filteringRaw["maxResultsPerResolution"]; has {
				config.MigrateRawUserSettings(userRaw)
				migrated, _ := json.Marshal(userRaw)
				rawMap[userID] = migrated
				needsSave = true
			}
		}
		if displayRaw, ok := userRaw["display"].(map[string]interface{}); ok {
			if _, has := displayRaw["cleanPosters"]; has {
				delete(displayRaw, "cleanPosters")
				migrated, _ := json.Marshal(userRaw)
				rawMap[userID] = migrated
				needsSave = true
			}
		}
	}
	// Re-encode and decode into typed map
	migratedData, err := json.Marshal(rawMap)
	if err != nil {
		return fmt.Errorf("re-encode user settings: %w", err)
	}

	var settings map[string]models.UserSettings
	if err := json.Unmarshal(migratedData, &settings); err != nil {
		return fmt.Errorf("decode user settings: %w", err)
	}

	// Migrate: force hideUnreleased=false on trending shelves (curated lists handle this now)
	for userID, us := range settings {
		changed := false
		if !us.Display.NavigationTabVisibilityIncludesSystemTabs {
			if tabs, tabsChanged := models.AddMissingSystemNavigationTabs(us.Display.NavigationTabVisibility); tabsChanged {
				us.Display.NavigationTabVisibility = tabs
			}
			us.Display.NavigationTabVisibilityIncludesSystemTabs = true
			changed = true
			needsSave = true
		}
		if !us.Display.NavigationTabVisibilityIncludesWatchlist {
			if tabs, tabsChanged := models.AddMissingWatchlistNavigationTab(us.Display.NavigationTabVisibility); tabsChanged {
				us.Display.NavigationTabVisibility = tabs
			}
			us.Display.NavigationTabVisibilityIncludesWatchlist = true
			changed = true
			needsSave = true
		}
		if !us.Display.NavigationTabVisibilityIncludesSports {
			if tabs, tabsChanged := models.AddMissingSportsNavigationTab(us.Display.NavigationTabVisibility); tabsChanged {
				us.Display.NavigationTabVisibility = tabs
			}
			us.Display.NavigationTabVisibilityIncludesSports = true
			changed = true
			needsSave = true
		}
		if reconcileProfileHomeShelves(&us) {
			changed = true
			needsSave = true
		}
		for i := range us.HomeShelves.Shelves {
			id := us.HomeShelves.Shelves[i].ID
			if (id == "trending-movies" || id == "trending-tv") && us.HomeShelves.Shelves[i].HideUnreleased {
				us.HomeShelves.Shelves[i].HideUnreleased = false
				changed = true
			}
		}
		if changed {
			settings[userID] = us
		}
	}

	s.settings = settings
	for userID, us := range s.settings {
		log.Printf("[user-settings] load: user=%q subMode=%q audioLang=%q subLang=%q",
			userID, us.Playback.PreferredSubtitleMode, us.Playback.PreferredAudioLanguage, us.Playback.PreferredSubtitleLanguage)
	}

	// Persist migrated data so migration only runs once
	if needsSave {
		log.Printf("[user-settings] persisting migrated user settings")
		if err := s.saveLocked(); err != nil {
			log.Printf("[user-settings] warning: failed to persist migration: %v", err)
		}
	}

	return nil
}

func (s *Service) saveLocked() error {
	if s.useDB() {
		return s.syncToDB()
	}

	tmp := s.path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create user settings temp file: %w", err)
	}

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s.settings); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("encode user settings: %w", err)
	}

	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync user settings: %w", err)
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close user settings temp file: %w", err)
	}

	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace user settings file: %w", err)
	}

	return nil
}

// syncToDB writes the full in-memory user settings state to PostgreSQL.
func (s *Service) syncToDB() error {
	ctx := context.Background()
	return s.store.WithTx(ctx, func(tx *datastore.Tx) error {
		// Get existing DB state to detect deletes
		existing, err := tx.UserSettings().List(ctx)
		if err != nil {
			return err
		}
		dbIDs := make(map[string]bool, len(existing))
		for id := range existing {
			dbIDs[id] = true
		}
		// A deleted profile's row cascades away in the DB but stays in memory; upserting it
		// would fail the users foreign key and with it every later save, so it is dropped.
		users, err := tx.Users().List(ctx)
		if err != nil {
			return err
		}
		userIDs := make(map[string]bool, len(users))
		for _, user := range users {
			userIDs[user.ID] = true
		}
		for _, userID := range dropOrphanedSettings(s.settings, userIDs) {
			log.Printf("[user-settings] dropping settings for deleted profile %q", userID)
		}
		// Upsert all in-memory settings
		for userID, us := range s.settings {
			settings := us
			if err := tx.UserSettings().Upsert(ctx, userID, &settings); err != nil {
				return err
			}
			delete(dbIDs, userID)
		}
		// Delete settings removed from memory
		for id := range dbIDs {
			if err := tx.UserSettings().Delete(ctx, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// dropOrphanedSettings removes settings whose profile no longer exists and
// returns the removed profile IDs, sorted.
func dropOrphanedSettings(settings map[string]models.UserSettings, userIDs map[string]bool) []string {
	var dropped []string
	for userID := range settings {
		if !userIDs[userID] {
			delete(settings, userID)
			dropped = append(dropped, userID)
		}
	}
	sort.Strings(dropped)
	return dropped
}
