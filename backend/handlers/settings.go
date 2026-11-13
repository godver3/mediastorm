package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/internal/apiusage"
	"novastream/internal/auth"
	"novastream/internal/pool"
	"novastream/services/debrid"
	"novastream/services/epg"
	"novastream/services/mdblist"
	"novastream/services/metadata"
	user_settings "novastream/services/user_settings"
)

// PrequeueClearer can clear all prequeue entries.
type PrequeueClearer interface {
	DeleteAll()
}

// SearchCacheClearer can clear cached search results.
type SearchCacheClearer interface {
	ClearSearchCache()
}

func shouldClearPrequeueForGlobalSettingsChange(oldSettings, newSettings config.Settings) bool {
	if oldSettings.Streaming.ResolveFirstReadySource != newSettings.Streaming.ResolveFirstReadySource ||
		oldSettings.Streaming.ResolutionEndRaceEarly != newSettings.Streaming.ResolutionEndRaceEarly ||
		oldSettings.Streaming.ResolutionSettleWindowMs != newSettings.Streaming.ResolutionSettleWindowMs {
		return true
	}
	if oldSettings.Display.ShowParsedBadges != newSettings.Display.ShowParsedBadges {
		return true
	}
	if !reflect.DeepEqual(oldSettings.Filtering, newSettings.Filtering) {
		return true
	}
	if !reflect.DeepEqual(oldSettings.Ranking, newSettings.Ranking) {
		return true
	}
	return false
}

type SettingsHandler struct {
	Manager             *config.Manager
	DemoMode            bool
	PoolManager         pool.Manager
	MetadataService     *metadata.Service
	MDBListListsClient  *mdblist.ListsClient
	DebridSearchService *debrid.SearchService
	ImageHandler        *ImageHandler
	EPGService          *epg.Service
	UserSettingsService *user_settings.Service
	ClientsLister       user_settings.ClientsLister
	ClientSettingsBatch user_settings.ClientSettingsBatch
	PrequeueStore       PrequeueClearer
	SearchCache         SearchCacheClearer
}

func NewSettingsHandler(m *config.Manager) *SettingsHandler {
	return &SettingsHandler{Manager: m, DemoMode: false}
}

func NewSettingsHandlerWithDemoMode(m *config.Manager, demoMode bool) *SettingsHandler {
	return &SettingsHandler{Manager: m, DemoMode: demoMode}
}

// SetPoolManager sets the pool manager for hot reloading usenet providers
func (h *SettingsHandler) SetPoolManager(pm pool.Manager) {
	h.PoolManager = pm
}

// SetMetadataService sets the metadata service for hot reloading API keys
func (h *SettingsHandler) SetMetadataService(ms *metadata.Service) {
	h.MetadataService = ms
}

// SetMDBListListsClient sets the MDBList external-lists client for hot reloading API keys.
func (h *SettingsHandler) SetMDBListListsClient(client *mdblist.ListsClient) {
	h.MDBListListsClient = client
}

// SetDebridSearchService sets the debrid search service for hot reloading scrapers
func (h *SettingsHandler) SetDebridSearchService(ds *debrid.SearchService) {
	h.DebridSearchService = ds
}

// SetImageHandler sets the image handler for clearing image cache
func (h *SettingsHandler) SetImageHandler(ih *ImageHandler) {
	h.ImageHandler = ih
}

// SetEPGService sets the EPG service for auto-refresh when new sources are added
func (h *SettingsHandler) SetEPGService(es *epg.Service) {
	h.EPGService = es
}

// SetUserSettingsService sets the user settings service for stripping redundant overrides
func (h *SettingsHandler) SetUserSettingsService(us *user_settings.Service) {
	h.UserSettingsService = us
}

// SetClientsLister sets the clients lister for client→profile mapping
func (h *SettingsHandler) SetClientsLister(cl user_settings.ClientsLister) {
	h.ClientsLister = cl
}

// SetClientSettingsBatch sets the client settings batch service for stripping redundant overrides
func (h *SettingsHandler) SetClientSettingsBatch(cs user_settings.ClientSettingsBatch) {
	h.ClientSettingsBatch = cs
}

// SetPrequeueStore sets the prequeue store for clearing cache when display settings change
func (h *SettingsHandler) SetPrequeueStore(ps PrequeueClearer) {
	h.PrequeueStore = ps
}

// SetSearchCacheClearer sets the search cache invalidator for ranking/filtering changes.
func (h *SettingsHandler) SetSearchCacheClearer(sc SearchCacheClearer) {
	h.SearchCache = sc
}

// SettingsResponse wraps config.Settings with additional runtime information.
type SettingsResponse struct {
	config.Settings
	DemoMode                   bool                                 `json:"demoMode"`
	UserEditableSettings       []string                             `json:"userEditableSettings"`
	UserEditableSettingsSchema map[string]UserEditableSettingSchema `json:"userEditableSettingsSchema"`
}

// LiveSettingsWithEffectiveURL wraps LiveSettings with a computed effective URL.
type LiveSettingsWithEffectiveURL struct {
	config.LiveSettings
	EffectivePlaylistURL string `json:"effectivePlaylistUrl,omitempty"`
}

// SettingsResponseWithLive extends SettingsResponse with computed live URL.
type SettingsResponseWithLive struct {
	SettingsResponse
	Live LiveSettingsWithEffectiveURL `json:"live"`
}

// PublicBrandingResponse exposes only display branding URLs for unauthenticated
// shells such as the dedicated /watch login page.
type PublicBrandingResponse struct {
	Branding PublicBrandingSettings `json:"branding"`
}

type PublicBrandingSettings struct {
	HomeTVImageURL     string `json:"homeTvImageUrl,omitempty"`
	HomeMobileLogoURL  string `json:"homeMobileLogoUrl,omitempty"`
	SettingsTVImageURL string `json:"settingsTvImageUrl,omitempty"`
	WebIconURL         string `json:"webIconUrl,omitempty"`
	LoadingLogoURL     string `json:"loadingLogoUrl,omitempty"`
}

type brandingSlot struct {
	FileName string
	Get      func(config.Settings) string
	Set      func(*config.Settings, string)
}

var brandingSlots = map[string]brandingSlot{
	"home-tv": {
		FileName: "home-tv",
		Get:      func(s config.Settings) string { return s.Display.Branding.HomeTVImageURL },
		Set:      func(s *config.Settings, v string) { s.Display.Branding.HomeTVImageURL = v },
	},
	"home-mobile-logo": {
		FileName: "home-mobile-logo",
		Get:      func(s config.Settings) string { return s.Display.Branding.HomeMobileLogoURL },
		Set:      func(s *config.Settings, v string) { s.Display.Branding.HomeMobileLogoURL = v },
	},
	"settings-tv": {
		FileName: "settings-tv",
		Get:      func(s config.Settings) string { return s.Display.Branding.SettingsTVImageURL },
		Set:      func(s *config.Settings, v string) { s.Display.Branding.SettingsTVImageURL = v },
	},
	"web-icon": {
		FileName: "web-icon",
		Get:      func(s config.Settings) string { return s.Display.Branding.WebIconURL },
		Set:      func(s *config.Settings, v string) { s.Display.Branding.WebIconURL = v },
	},
	"loading-logo": {
		FileName: "loading-logo",
		Get:      func(s config.Settings) string { return s.Display.Branding.LoadingLogoURL },
		Set:      func(s *config.Settings, v string) { s.Display.Branding.LoadingLogoURL = v },
	},
	// wizard-source stores the original uncropped image so the branding wizard
	// can reload and re-crop it later without re-selecting the file.
	"wizard-source": {
		FileName: "wizard-source",
		Get:      func(s config.Settings) string { return s.Display.Branding.WizardSourceURL },
		Set:      func(s *config.Settings, v string) { s.Display.Branding.WizardSourceURL = v },
	},
}

func (h *SettingsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	s, err := h.Manager.Load()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	if accountID := auth.GetAccountID(r); accountID != "" && !auth.IsMaster(r) {
		s = config.FilterLiveSettingsForAccount(s, accountID)
	}
	effectivePlaylistURL := redactedEffectivePlaylistURL(s.Live)

	// Always redact credentials — secrets are write-only, never sent back to any client
	redactSettings(&s)

	// Build response with computed effective playlist URL
	editableSettings := filterUserEditableSettings(s.UI.UserEditableSettings)
	resp := SettingsResponseWithLive{
		SettingsResponse: SettingsResponse{
			Settings:                   s,
			DemoMode:                   h.DemoMode,
			UserEditableSettings:       editableSettings,
			UserEditableSettingsSchema: userEditableSettingsSchemaForSettings(editableSettings, s),
		},
		Live: LiveSettingsWithEffectiveURL{
			LiveSettings:         s.Live,
			EffectivePlaylistURL: effectivePlaylistURL,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func redactedEffectivePlaylistURL(live config.LiveSettings) string {
	if strings.TrimSpace(live.GetEffectivePlaylistURL()) == "" {
		return ""
	}
	return redactedPlaceholder
}

func (h *SettingsHandler) GetPublicBranding(w http.ResponseWriter, r *http.Request) {
	s, err := h.Manager.Load()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	branding := s.Display.Branding
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(PublicBrandingResponse{
		Branding: PublicBrandingSettings{
			HomeTVImageURL:     branding.HomeTVImageURL,
			HomeMobileLogoURL:  branding.HomeMobileLogoURL,
			SettingsTVImageURL: branding.SettingsTVImageURL,
			WebIconURL:         branding.WebIconURL,
			LoadingLogoURL:     branding.LoadingLogoURL,
		},
	})
}

func (h *SettingsHandler) GetBrandingImageStatus(w http.ResponseWriter, r *http.Request) {
	slot, ok := h.brandingSlotFromRequest(r)
	if !ok {
		jsonError(w, "Invalid branding image slot", http.StatusBadRequest)
		return
	}

	settings, err := h.Manager.Load()
	if err != nil {
		jsonError(w, "Failed to load settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	imageURL := slot.Get(settings)
	localUploaded := strings.HasPrefix(imageURL, "/branding/images/")
	resp := map[string]interface{}{
		"uploaded": localUploaded,
		"url":      imageURL,
	}
	if localUploaded {
		if path, err := h.brandingImagePath(settings, slot); err == nil {
			if info, statErr := os.Stat(path); statErr == nil {
				resp["fileSize"] = info.Size()
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *SettingsHandler) UploadBrandingImage(w http.ResponseWriter, r *http.Request) {
	slot, ok := h.brandingSlotFromRequest(r)
	if !ok {
		jsonError(w, "Invalid branding image slot", http.StatusBadRequest)
		return
	}

	if err := r.ParseMultipartForm(8 << 20); err != nil {
		jsonError(w, "File too large or invalid form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("image")
	if err != nil {
		jsonError(w, "image file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, 8*1024*1024+1))
	if err != nil {
		jsonError(w, "Failed to read file", http.StatusInternalServerError)
		return
	}
	if len(data) == 0 {
		jsonError(w, "image file is empty", http.StatusBadRequest)
		return
	}
	if len(data) > 8*1024*1024 {
		jsonError(w, "image file too large (max 8MB)", http.StatusBadRequest)
		return
	}

	contentType := header.Header.Get("Content-Type")
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = http.DetectContentType(data)
	}

	ext := ""
	switch {
	case strings.Contains(contentType, "image/png"):
		ext = ".png"
	case strings.Contains(contentType, "image/jpeg"), strings.Contains(contentType, "image/jpg"):
		ext = ".jpg"
	default:
		jsonError(w, "invalid image format, must be PNG or JPG", http.StatusBadRequest)
		return
	}

	settings, err := h.Manager.Load()
	if err != nil {
		jsonError(w, "Failed to load settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	brandingDir := h.brandingImageDir(settings)
	if err := os.MkdirAll(brandingDir, 0o755); err != nil {
		jsonError(w, "Failed to create branding image directory", http.StatusInternalServerError)
		return
	}

	for _, oldExt := range []string{".png", ".jpg"} {
		_ = os.Remove(filepath.Join(brandingDir, slot.FileName+oldExt))
	}

	localPath := filepath.Join(brandingDir, slot.FileName+ext)
	if err := os.WriteFile(localPath, data, 0o644); err != nil {
		jsonError(w, "Failed to save branding image", http.StatusInternalServerError)
		return
	}

	imageURL := fmt.Sprintf("/branding/images/%s?v=%d", slot.FileName, time.Now().Unix())
	slot.Set(&settings, imageURL)
	if err := h.Manager.Save(settings); err != nil {
		_ = os.Remove(localPath)
		jsonError(w, "Failed to save settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"uploaded": true,
		"url":      imageURL,
		"fileSize": len(data),
	})
}

func (h *SettingsHandler) DeleteBrandingImage(w http.ResponseWriter, r *http.Request) {
	slot, ok := h.brandingSlotFromRequest(r)
	if !ok {
		jsonError(w, "Invalid branding image slot", http.StatusBadRequest)
		return
	}

	settings, err := h.Manager.Load()
	if err != nil {
		jsonError(w, "Failed to load settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	brandingDir := h.brandingImageDir(settings)
	for _, ext := range []string{".png", ".jpg"} {
		_ = os.Remove(filepath.Join(brandingDir, slot.FileName+ext))
	}
	if strings.HasPrefix(slot.Get(settings), "/branding/images/") {
		slot.Set(&settings, "")
		if err := h.Manager.Save(settings); err != nil {
			jsonError(w, "Failed to save settings: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"uploaded": false,
		"url":      slot.Get(settings),
	})
}

func (h *SettingsHandler) ServeBrandingImage(w http.ResponseWriter, r *http.Request) {
	slot, ok := h.brandingSlotFromRequest(r)
	if !ok {
		http.Error(w, "invalid branding image slot", http.StatusBadRequest)
		return
	}

	settings, err := h.Manager.Load()
	if err != nil {
		http.Error(w, "failed to load settings", http.StatusInternalServerError)
		return
	}

	localPath, err := h.brandingImagePath(settings, slot)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeFile(w, r, localPath)
}

func (h *SettingsHandler) ServeWebIcon(w http.ResponseWriter, r *http.Request) {
	settings, err := h.Manager.Load()
	if err == nil {
		if localPath, pathErr := h.brandingImagePath(settings, brandingSlots["web-icon"]); pathErr == nil {
			w.Header().Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
			http.ServeFile(w, r, localPath)
			return
		}
	}

	data, readErr := staticAssets.ReadFile("static/favicon-32.png")
	if readErr != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
	w.Header().Set("Content-Type", "image/png")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (h *SettingsHandler) ServeAppleTouchIcon(w http.ResponseWriter, r *http.Request) {
	settings, err := h.Manager.Load()
	if err == nil {
		if localPath, pathErr := h.brandingImagePath(settings, brandingSlots["web-icon"]); pathErr == nil {
			w.Header().Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
			http.ServeFile(w, r, localPath)
			return
		}
	}

	data, readErr := staticAssets.ReadFile("static/icon-80.png")
	if readErr != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
	w.Header().Set("Content-Type", "image/png")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (h *SettingsHandler) brandingSlotFromRequest(r *http.Request) (brandingSlot, bool) {
	name := strings.TrimSpace(mux.Vars(r)["slot"])
	if name == "" {
		name = strings.TrimSpace(mux.Vars(r)["image"])
	}
	slot, ok := brandingSlots[name]
	return slot, ok
}

func (h *SettingsHandler) brandingImageDir(settings config.Settings) string {
	return brandingImageDir(settings)
}

func (h *SettingsHandler) brandingImagePath(settings config.Settings, slot brandingSlot) (string, error) {
	return brandingImagePath(settings, slot)
}

// brandingImageDir returns the on-disk directory where uploaded branding
// images are stored for the given settings.
func brandingImageDir(settings config.Settings) string {
	baseDir := strings.TrimSpace(settings.Cache.Directory)
	if baseDir == "" {
		baseDir = "cache"
	}
	return filepath.Join(baseDir, "branding")
}

// brandingImagePath returns the path to an uploaded branding image for the
// given slot, or os.ErrNotExist if none has been uploaded.
func brandingImagePath(settings config.Settings, slot brandingSlot) (string, error) {
	dir := brandingImageDir(settings)
	for _, ext := range []string{".png", ".jpg"} {
		localPath := filepath.Join(dir, slot.FileName+ext)
		if _, err := os.Stat(localPath); err == nil {
			return localPath, nil
		}
	}
	return "", os.ErrNotExist
}

// webUIBrandingURL returns the public URL for an admin webui branding surface,
// preferring a custom uploaded branding image (slotName) when present and
// falling back to the bundled static asset (defaultStatic). base is the server
// base-path prefix (may be empty). Cache-busting versions are appended so
// updates are picked up immediately.
func webUIBrandingURL(settings config.Settings, base, slotName, defaultStatic string) string {
	if slot, ok := brandingSlots[slotName]; ok {
		if localPath, err := brandingImagePath(settings, slot); err == nil {
			v := int64(0)
			if info, statErr := os.Stat(localPath); statErr == nil {
				v = info.ModTime().Unix()
			}
			return fmt.Sprintf("%s/api/branding/images/%s?v=%d", base, slot.FileName, v)
		}
	}
	return staticBrandingURL(base, defaultStatic)
}

func staticBrandingURL(base, defaultStatic string) string {
	url := base + "/api/static/" + defaultStatic
	if version := staticAssetVersion(defaultStatic); version != "" {
		url += "?v=" + version
	}
	return url
}

func staticAssetVersion(defaultStatic string) string {
	data, err := staticAssets.ReadFile("static/" + defaultStatic)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)[:12]
}

// redactSettings replaces sensitive credentials with a placeholder so
// non-master users cannot read API keys, passwords, or tokens.
func redactSettings(s *config.Settings) {
	const redacted = "••••••••"
	mask := func(v *string) {
		if *v != "" {
			*v = redacted
		}
	}

	// Server
	mask(&s.Server.HomepageAPIKey)

	// Usenet providers
	for i := range s.Usenet {
		mask(&s.Usenet[i].Password)
	}
	for i := range s.UsenetEngines {
		mask(&s.UsenetEngines[i].APIKey)
		mask(&s.UsenetEngines[i].Password)
		mask(&s.UsenetEngines[i].WebDAVPassword)
	}

	// Indexers (Newznab/Torznab)
	for i := range s.Indexers {
		mask(&s.Indexers[i].APIKey)
	}

	// Torrent scrapers (Prowlarr/Jackett)
	for i := range s.TorrentScrapers {
		mask(&s.TorrentScrapers[i].APIKey)
		if strings.EqualFold(strings.TrimSpace(s.TorrentScrapers[i].Type), "stremio-direct") {
			mask(&s.TorrentScrapers[i].URL)
		}
	}

	// Metadata API keys
	mask(&s.Metadata.TVDBAPIKey)
	mask(&s.Metadata.TMDBAPIKey)
	mask(&s.Metadata.AIAPIKey)
	mask(&s.Metadata.GeminiAPIKey)
	mask(&s.Playback.YouTubeProxyURL)
	mask(&s.Playback.Thumbnails.SeekrAPIKey)

	// WebDAV
	mask(&s.WebDAV.Password)

	// Debrid providers
	for i := range s.Streaming.DebridProviders {
		mask(&s.Streaming.DebridProviders[i].APIKey)
	}

	// SABnzbd
	mask(&s.SABnzbd.FallbackAPIKey)

	// Subtitles
	mask(&s.Subtitles.OpenSubtitlesPassword)
	mask(&s.Subtitles.SubDLAPIKey)
	mask(&s.Subtitles.SubSourceAPIKey)

	// MDBList
	mask(&s.MDBList.APIKey)
	for i := range s.MDBList.Accounts {
		mask(&s.MDBList.Accounts[i].APIKey)
	}
	for i := range s.PublicMetaDB.Accounts {
		mask(&s.PublicMetaDB.Accounts[i].APIKey)
	}

	// Trakt (legacy fields + account-level tokens)
	mask(&s.Trakt.ClientSecret)
	mask(&s.Trakt.AccessToken)
	mask(&s.Trakt.RefreshToken)
	for i := range s.Trakt.Accounts {
		mask(&s.Trakt.Accounts[i].ClientSecret)
		mask(&s.Trakt.Accounts[i].AccessToken)
		mask(&s.Trakt.Accounts[i].RefreshToken)
	}

	// Simkl account credentials
	for i := range s.Simkl.Accounts {
		mask(&s.Simkl.Accounts[i].ClientSecret)
		mask(&s.Simkl.Accounts[i].AccessToken)
		mask(&s.Simkl.Accounts[i].RefreshToken)
	}

	// Self-hosted Scrob credentials
	for i := range s.Scrob.Accounts {
		mask(&s.Scrob.Accounts[i].APIKey)
		mask(&s.Scrob.Accounts[i].Password)
		mask(&s.Scrob.Accounts[i].TOTPSecret)
	}

	// Plex (legacy field + account-level tokens)
	mask(&s.Plex.AuthToken)
	for i := range s.Plex.Accounts {
		mask(&s.Plex.Accounts[i].AuthToken)
	}

	// Jellyfin account tokens
	for i := range s.Jellyfin.Accounts {
		mask(&s.Jellyfin.Accounts[i].Token)
	}

	// Live provider URLs and credentials. Playlist, manifest, proxy, and EPG
	// URLs can contain embedded credentials or signed query parameters.
	mask(&s.Live.PlaylistURL)
	mask(&s.Live.ManifestURL)
	mask(&s.Live.ProxyURL)
	mask(&s.Live.HDHomeRunGuideEmail)
	mask(&s.Live.HDHomeRunGuideDeviceIDs)
	mask(&s.Live.XtreamHost)
	mask(&s.Live.XtreamUsername)
	mask(&s.Live.XtreamPassword)
	mask(&s.Live.StalkerPortalURL)
	mask(&s.Live.StalkerMAC)
	mask(&s.Live.StalkerSerialNumber)
	mask(&s.Live.StalkerDeviceID)
	mask(&s.Live.StalkerDeviceID2)
	mask(&s.Live.StalkerSignature)
	mask(&s.Live.EPG.XmltvUrl)
	for i := range s.Live.EPG.Sources {
		mask(&s.Live.EPG.Sources[i].URL)
	}
	for i := range s.Live.Sources {
		mask(&s.Live.Sources[i].PlaylistURL)
		mask(&s.Live.Sources[i].ManifestURL)
		mask(&s.Live.Sources[i].ProxyURL)
		mask(&s.Live.Sources[i].HDHomeRunGuideEmail)
		mask(&s.Live.Sources[i].HDHomeRunGuideDeviceIDs)
		mask(&s.Live.Sources[i].XtreamHost)
		mask(&s.Live.Sources[i].XtreamUsername)
		mask(&s.Live.Sources[i].XtreamPassword)
		mask(&s.Live.Sources[i].StalkerPortalURL)
		mask(&s.Live.Sources[i].StalkerMAC)
		mask(&s.Live.Sources[i].StalkerSerialNumber)
		mask(&s.Live.Sources[i].StalkerDeviceID)
		mask(&s.Live.Sources[i].StalkerDeviceID2)
		mask(&s.Live.Sources[i].StalkerSignature)
		mask(&s.Live.Sources[i].EPG.XmltvUrl)
		for j := range s.Live.Sources[i].EPG.Sources {
			mask(&s.Live.Sources[i].EPG.Sources[j].URL)
		}
	}
	for i := range s.Live.PlaylistSources {
		mask(&s.Live.PlaylistSources[i].PlaylistURL)
		mask(&s.Live.PlaylistSources[i].ManifestURL)
		mask(&s.Live.PlaylistSources[i].ProxyURL)
		mask(&s.Live.PlaylistSources[i].HDHomeRunGuideEmail)
		mask(&s.Live.PlaylistSources[i].HDHomeRunGuideDeviceIDs)
		mask(&s.Live.PlaylistSources[i].XtreamHost)
		mask(&s.Live.PlaylistSources[i].XtreamUsername)
		mask(&s.Live.PlaylistSources[i].XtreamPassword)
		mask(&s.Live.PlaylistSources[i].StalkerPortalURL)
		mask(&s.Live.PlaylistSources[i].StalkerMAC)
		mask(&s.Live.PlaylistSources[i].StalkerSerialNumber)
		mask(&s.Live.PlaylistSources[i].StalkerDeviceID)
		mask(&s.Live.PlaylistSources[i].StalkerDeviceID2)
		mask(&s.Live.PlaylistSources[i].StalkerSignature)
		mask(&s.Live.PlaylistSources[i].EPG.XmltvUrl)
		for j := range s.Live.PlaylistSources[i].EPG.Sources {
			mask(&s.Live.PlaylistSources[i].EPG.Sources[j].URL)
		}
	}

	// Database URL (may contain credentials in the connection string)
	mask(&s.Database.URL)
}

const redactedPlaceholder = "••••••••"

// preserveRedactedFields restores the real credential from existing settings
// whenever the incoming value equals the redaction placeholder. This prevents
// save-back of redacted values from overwriting real secrets.
func preserveRedactedFields(incoming *config.Settings, existing *config.Settings) {
	restore := func(newVal *string, oldVal string) {
		if *newVal == redactedPlaceholder {
			*newVal = oldVal
		}
	}

	// Server
	restore(&incoming.Server.HomepageAPIKey, existing.Server.HomepageAPIKey)

	// Usenet providers (match by index — frontend preserves order)
	for i := range incoming.Usenet {
		if i < len(existing.Usenet) {
			restore(&incoming.Usenet[i].Password, existing.Usenet[i].Password)
		}
	}
	for i := range incoming.UsenetEngines {
		if i < len(existing.UsenetEngines) {
			restore(&incoming.UsenetEngines[i].APIKey, existing.UsenetEngines[i].APIKey)
			restore(&incoming.UsenetEngines[i].Password, existing.UsenetEngines[i].Password)
			restore(&incoming.UsenetEngines[i].WebDAVPassword, existing.UsenetEngines[i].WebDAVPassword)
		}
	}

	// Indexers
	for i := range incoming.Indexers {
		if i < len(existing.Indexers) {
			restore(&incoming.Indexers[i].APIKey, existing.Indexers[i].APIKey)
		}
	}

	// Torrent scrapers
	for i := range incoming.TorrentScrapers {
		if i < len(existing.TorrentScrapers) {
			restore(&incoming.TorrentScrapers[i].APIKey, existing.TorrentScrapers[i].APIKey)
			if strings.EqualFold(strings.TrimSpace(incoming.TorrentScrapers[i].Type), "stremio-direct") {
				restore(&incoming.TorrentScrapers[i].URL, existing.TorrentScrapers[i].URL)
			}
		}
	}

	// Metadata
	restore(&incoming.Metadata.TVDBAPIKey, existing.Metadata.TVDBAPIKey)
	restore(&incoming.Metadata.TMDBAPIKey, existing.Metadata.TMDBAPIKey)
	if strings.EqualFold(strings.TrimSpace(incoming.Metadata.AIProvider), "ollama") && !strings.EqualFold(strings.TrimSpace(existing.Metadata.AIProvider), "ollama") && (incoming.Metadata.AIAPIKey == redactedPlaceholder || incoming.Metadata.AIAPIKey == existing.Metadata.AIAPIKey) {
		incoming.Metadata.AIAPIKey = ""
	} else {
		restore(&incoming.Metadata.AIAPIKey, existing.Metadata.AIAPIKey)
	}
	restore(&incoming.Metadata.GeminiAPIKey, existing.Metadata.GeminiAPIKey)
	restore(&incoming.Playback.YouTubeProxyURL, existing.Playback.YouTubeProxyURL)
	restore(&incoming.Playback.Thumbnails.SeekrAPIKey, existing.Playback.Thumbnails.SeekrAPIKey)

	// WebDAV
	restore(&incoming.WebDAV.Password, existing.WebDAV.Password)

	// Debrid providers
	for i := range incoming.Streaming.DebridProviders {
		if i < len(existing.Streaming.DebridProviders) {
			restore(&incoming.Streaming.DebridProviders[i].APIKey, existing.Streaming.DebridProviders[i].APIKey)
		}
	}

	// SABnzbd
	restore(&incoming.SABnzbd.FallbackAPIKey, existing.SABnzbd.FallbackAPIKey)

	// Subtitles
	restore(&incoming.Subtitles.OpenSubtitlesPassword, existing.Subtitles.OpenSubtitlesPassword)
	restore(&incoming.Subtitles.SubDLAPIKey, existing.Subtitles.SubDLAPIKey)
	restore(&incoming.Subtitles.SubSourceAPIKey, existing.Subtitles.SubSourceAPIKey)

	// MDBList
	restore(&incoming.MDBList.APIKey, existing.MDBList.APIKey)
	for i := range incoming.MDBList.Accounts {
		if i < len(existing.MDBList.Accounts) {
			restore(&incoming.MDBList.Accounts[i].APIKey, existing.MDBList.Accounts[i].APIKey)
		}
	}
	for i := range incoming.PublicMetaDB.Accounts {
		for _, old := range existing.PublicMetaDB.Accounts {
			if incoming.PublicMetaDB.Accounts[i].ID == old.ID {
				restore(&incoming.PublicMetaDB.Accounts[i].APIKey, old.APIKey)
				break
			}
		}
	}

	// Trakt (legacy fields + account-level tokens)
	restore(&incoming.Trakt.ClientSecret, existing.Trakt.ClientSecret)
	restore(&incoming.Trakt.AccessToken, existing.Trakt.AccessToken)
	restore(&incoming.Trakt.RefreshToken, existing.Trakt.RefreshToken)
	for i := range incoming.Trakt.Accounts {
		if i < len(existing.Trakt.Accounts) {
			restore(&incoming.Trakt.Accounts[i].ClientSecret, existing.Trakt.Accounts[i].ClientSecret)
			restore(&incoming.Trakt.Accounts[i].AccessToken, existing.Trakt.Accounts[i].AccessToken)
			restore(&incoming.Trakt.Accounts[i].RefreshToken, existing.Trakt.Accounts[i].RefreshToken)
		}
	}

	// Simkl account credentials
	for i := range incoming.Simkl.Accounts {
		if i < len(existing.Simkl.Accounts) {
			restore(&incoming.Simkl.Accounts[i].ClientSecret, existing.Simkl.Accounts[i].ClientSecret)
			restore(&incoming.Simkl.Accounts[i].AccessToken, existing.Simkl.Accounts[i].AccessToken)
			restore(&incoming.Simkl.Accounts[i].RefreshToken, existing.Simkl.Accounts[i].RefreshToken)
		}
	}

	// Self-hosted Scrob credentials
	for i := range incoming.Scrob.Accounts {
		if i < len(existing.Scrob.Accounts) {
			restore(&incoming.Scrob.Accounts[i].APIKey, existing.Scrob.Accounts[i].APIKey)
			restore(&incoming.Scrob.Accounts[i].Password, existing.Scrob.Accounts[i].Password)
			restore(&incoming.Scrob.Accounts[i].TOTPSecret, existing.Scrob.Accounts[i].TOTPSecret)
		}
	}

	// Plex (legacy field + account-level tokens)
	restore(&incoming.Plex.AuthToken, existing.Plex.AuthToken)
	for i := range incoming.Plex.Accounts {
		if i < len(existing.Plex.Accounts) {
			restore(&incoming.Plex.Accounts[i].AuthToken, existing.Plex.Accounts[i].AuthToken)
		}
	}

	// Jellyfin account tokens
	for i := range incoming.Jellyfin.Accounts {
		if i < len(existing.Jellyfin.Accounts) {
			restore(&incoming.Jellyfin.Accounts[i].Token, existing.Jellyfin.Accounts[i].Token)
		}
	}

	// Live provider URLs and credentials
	restore(&incoming.Live.PlaylistURL, existing.Live.PlaylistURL)
	restore(&incoming.Live.ManifestURL, existing.Live.ManifestURL)
	restore(&incoming.Live.ProxyURL, existing.Live.ProxyURL)
	restore(&incoming.Live.HDHomeRunGuideEmail, existing.Live.HDHomeRunGuideEmail)
	restore(&incoming.Live.HDHomeRunGuideDeviceIDs, existing.Live.HDHomeRunGuideDeviceIDs)
	restore(&incoming.Live.XtreamHost, existing.Live.XtreamHost)
	restore(&incoming.Live.XtreamUsername, existing.Live.XtreamUsername)
	restore(&incoming.Live.XtreamPassword, existing.Live.XtreamPassword)
	restore(&incoming.Live.StalkerPortalURL, existing.Live.StalkerPortalURL)
	restore(&incoming.Live.StalkerMAC, existing.Live.StalkerMAC)
	restore(&incoming.Live.StalkerSerialNumber, existing.Live.StalkerSerialNumber)
	restore(&incoming.Live.StalkerDeviceID, existing.Live.StalkerDeviceID)
	restore(&incoming.Live.StalkerDeviceID2, existing.Live.StalkerDeviceID2)
	restore(&incoming.Live.StalkerSignature, existing.Live.StalkerSignature)
	restore(&incoming.Live.EPG.XmltvUrl, existing.Live.EPG.XmltvUrl)
	for i := range incoming.Live.EPG.Sources {
		if i < len(existing.Live.EPG.Sources) {
			restore(&incoming.Live.EPG.Sources[i].URL, existing.Live.EPG.Sources[i].URL)
		}
	}
	for i := range incoming.Live.Sources {
		if i < len(existing.Live.Sources) {
			restore(&incoming.Live.Sources[i].PlaylistURL, existing.Live.Sources[i].PlaylistURL)
			restore(&incoming.Live.Sources[i].ManifestURL, existing.Live.Sources[i].ManifestURL)
			restore(&incoming.Live.Sources[i].ProxyURL, existing.Live.Sources[i].ProxyURL)
			restore(&incoming.Live.Sources[i].HDHomeRunGuideEmail, existing.Live.Sources[i].HDHomeRunGuideEmail)
			restore(&incoming.Live.Sources[i].HDHomeRunGuideDeviceIDs, existing.Live.Sources[i].HDHomeRunGuideDeviceIDs)
			restore(&incoming.Live.Sources[i].XtreamHost, existing.Live.Sources[i].XtreamHost)
			restore(&incoming.Live.Sources[i].XtreamUsername, existing.Live.Sources[i].XtreamUsername)
			restore(&incoming.Live.Sources[i].XtreamPassword, existing.Live.Sources[i].XtreamPassword)
			restore(&incoming.Live.Sources[i].StalkerPortalURL, existing.Live.Sources[i].StalkerPortalURL)
			restore(&incoming.Live.Sources[i].StalkerMAC, existing.Live.Sources[i].StalkerMAC)
			restore(&incoming.Live.Sources[i].StalkerSerialNumber, existing.Live.Sources[i].StalkerSerialNumber)
			restore(&incoming.Live.Sources[i].StalkerDeviceID, existing.Live.Sources[i].StalkerDeviceID)
			restore(&incoming.Live.Sources[i].StalkerDeviceID2, existing.Live.Sources[i].StalkerDeviceID2)
			restore(&incoming.Live.Sources[i].StalkerSignature, existing.Live.Sources[i].StalkerSignature)
			restore(&incoming.Live.Sources[i].EPG.XmltvUrl, existing.Live.Sources[i].EPG.XmltvUrl)
			for j := range incoming.Live.Sources[i].EPG.Sources {
				if j < len(existing.Live.Sources[i].EPG.Sources) {
					restore(&incoming.Live.Sources[i].EPG.Sources[j].URL, existing.Live.Sources[i].EPG.Sources[j].URL)
				}
			}
		}
	}
	for i := range incoming.Live.PlaylistSources {
		if i < len(existing.Live.PlaylistSources) {
			restore(&incoming.Live.PlaylistSources[i].PlaylistURL, existing.Live.PlaylistSources[i].PlaylistURL)
			restore(&incoming.Live.PlaylistSources[i].ManifestURL, existing.Live.PlaylistSources[i].ManifestURL)
			restore(&incoming.Live.PlaylistSources[i].ProxyURL, existing.Live.PlaylistSources[i].ProxyURL)
			restore(&incoming.Live.PlaylistSources[i].HDHomeRunGuideEmail, existing.Live.PlaylistSources[i].HDHomeRunGuideEmail)
			restore(&incoming.Live.PlaylistSources[i].HDHomeRunGuideDeviceIDs, existing.Live.PlaylistSources[i].HDHomeRunGuideDeviceIDs)
			restore(&incoming.Live.PlaylistSources[i].XtreamHost, existing.Live.PlaylistSources[i].XtreamHost)
			restore(&incoming.Live.PlaylistSources[i].XtreamUsername, existing.Live.PlaylistSources[i].XtreamUsername)
			restore(&incoming.Live.PlaylistSources[i].XtreamPassword, existing.Live.PlaylistSources[i].XtreamPassword)
			restore(&incoming.Live.PlaylistSources[i].StalkerPortalURL, existing.Live.PlaylistSources[i].StalkerPortalURL)
			restore(&incoming.Live.PlaylistSources[i].StalkerMAC, existing.Live.PlaylistSources[i].StalkerMAC)
			restore(&incoming.Live.PlaylistSources[i].StalkerSerialNumber, existing.Live.PlaylistSources[i].StalkerSerialNumber)
			restore(&incoming.Live.PlaylistSources[i].StalkerDeviceID, existing.Live.PlaylistSources[i].StalkerDeviceID)
			restore(&incoming.Live.PlaylistSources[i].StalkerDeviceID2, existing.Live.PlaylistSources[i].StalkerDeviceID2)
			restore(&incoming.Live.PlaylistSources[i].StalkerSignature, existing.Live.PlaylistSources[i].StalkerSignature)
			restore(&incoming.Live.PlaylistSources[i].EPG.XmltvUrl, existing.Live.PlaylistSources[i].EPG.XmltvUrl)
			for j := range incoming.Live.PlaylistSources[i].EPG.Sources {
				if j < len(existing.Live.PlaylistSources[i].EPG.Sources) {
					restore(&incoming.Live.PlaylistSources[i].EPG.Sources[j].URL, existing.Live.PlaylistSources[i].EPG.Sources[j].URL)
				}
			}
		}
	}

	// Database URL
	restore(&incoming.Database.URL, existing.Database.URL)
}

func (h *SettingsHandler) PutSettings(w http.ResponseWriter, r *http.Request) {
	// Load old settings to detect new EPG sources
	oldSettings, _ := h.Manager.Load()

	var s config.Settings
	dec := json.NewDecoder(r.Body)
	// Allow unknown fields for backward compatibility with old configs
	if err := dec.Decode(&s); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Older clients omit sports update controls. Preserve the saved selection so
	// normalization cannot re-enable background work during an unrelated save.
	if s.Sports.Enabled == nil {
		s.Sports.Enabled = oldSettings.Sports.Enabled
	}
	if s.Sports.EnabledLeagues == nil {
		s.Sports.EnabledLeagues = oldSettings.Sports.EnabledLeagues
	}

	// The Jellyfin bridge is configured on the Integrations page (which
	// applies changes live); a settings-page save keeps what is stored.
	s.JellyfinBridge = oldSettings.JellyfinBridge

	// An explicit navigation choice is not a legacy list requiring migration.
	if len(s.Display.NavigationTabVisibility) > 0 {
		s.UI.NavigationTabVisibilityIncludesSports = true
	}

	// Restore real credentials when the incoming value is the redaction placeholder.
	// This prevents non-master users from accidentally overwriting secrets when they
	// save settings that were returned with redacted values.
	preserveRedactedFields(&s, &oldSettings)
	if err := s.Server.NormalizeAllowedPrivateMediaOrigins(); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	if err := s.Server.NormalizeExternalBackendURL(); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	if err := s.Transmux.NormalizeHardwareAcceleration(); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	if err := validateConfigFilterTerms("filtering", &s.Filtering); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	if err := expandProwlarrSources(r.Context(), &s); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Auto-create/remove scheduled tasks based on feature settings
	h.EnsureEPGTaskForGuide(&s, "settings PUT")
	h.ensurePlaylistTaskIfConfigured(&s)

	if err := h.Manager.Save(s); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Clear prepared selections when resolution strategy, ranking, filtering, or
	// other selection-affecting settings change.
	if shouldClearPrequeueForGlobalSettingsChange(oldSettings, s) {
		if h.PrequeueStore != nil {
			log.Printf("[settings] prequeue-affecting settings changed, clearing prequeue cache")
			h.PrequeueStore.DeleteAll()
		}
		if h.SearchCache != nil {
			log.Printf("[settings] ranking/filtering-related settings changed, clearing search results cache")
			h.SearchCache.ClearSearchCache()
		}
	}

	// Strip redundant per-profile and per-client overrides
	if h.UserSettingsService != nil {
		go h.UserSettingsService.StripRedundantOverrides(s, h.ClientsLister, h.ClientSettingsBatch)
	}

	// Hot reload services that need it
	h.reloadServices(s)

	// Auto-refresh EPG if new sources were added
	h.triggerEPGRefreshIfNewSources(oldSettings, s)

	// Redact credentials before returning — secrets are write-only
	redactSettings(&s)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(s)
}

type prowlarrIndexerInfo struct {
	ID             int    `json:"id"`
	Name           string `json:"name"`
	Protocol       string `json:"protocol"`
	Enable         bool   `json:"enable"`
	SupportsSearch bool   `json:"supportsSearch"`
}

func expandProwlarrSources(ctx context.Context, s *config.Settings) error {
	cache := make(map[string][]prowlarrIndexerInfo)
	getIndexers := func(baseURL, apiKey string) ([]prowlarrIndexerInfo, error) {
		key := strings.TrimRight(strings.TrimSpace(baseURL), "/") + "\x00" + strings.TrimSpace(apiKey)
		if indexers, ok := cache[key]; ok {
			return indexers, nil
		}
		indexers, err := fetchProwlarrIndexers(ctx, baseURL, apiKey)
		if err != nil {
			return nil, err
		}
		cache[key] = indexers
		return indexers, nil
	}

	expandedIndexers := make([]config.IndexerConfig, 0, len(s.Indexers))
	for _, idx := range s.Indexers {
		if strings.ToLower(strings.TrimSpace(idx.Type)) != "prowlarr" {
			expandedIndexers = append(expandedIndexers, idx)
			continue
		}
		if isProwlarrPerIndexerURL(idx.URL) {
			idx.Type = "newznab"
			expandedIndexers = append(expandedIndexers, idx)
			continue
		}
		indexers, err := getIndexers(idx.URL, idx.APIKey)
		if err != nil {
			return fmt.Errorf("expand Prowlarr usenet indexer %q: %w", displayName(idx.Name, "Prowlarr"), err)
		}
		added := 0
		for _, pi := range indexers {
			if !shouldUseProwlarrIndexer(pi, "usenet") {
				continue
			}
			expandedIndexers = append(expandedIndexers, config.IndexerConfig{
				Name:       prowlarrGeneratedName(idx.Name, pi.Name),
				URL:        joinProwlarrIndexerURL(idx.URL, pi.ID),
				APIKey:     idx.APIKey,
				Type:       "newznab",
				Categories: idx.Categories,
				Enabled:    idx.Enabled,
				UserAgent:  idx.UserAgent,
			})
			added++
		}
		if added == 0 {
			return fmt.Errorf("expand Prowlarr usenet indexer %q: no enabled usenet indexers with search support were found", displayName(idx.Name, "Prowlarr"))
		}
	}
	s.Indexers = expandedIndexers

	expandedScrapers := make([]config.TorrentScraperConfig, 0, len(s.TorrentScrapers))
	for _, scraper := range s.TorrentScrapers {
		if strings.ToLower(strings.TrimSpace(scraper.Type)) != "prowlarr" || isProwlarrPerIndexerURL(scraper.URL) {
			expandedScrapers = append(expandedScrapers, scraper)
			continue
		}
		indexers, err := getIndexers(scraper.URL, scraper.APIKey)
		if err != nil {
			return fmt.Errorf("expand Prowlarr torrent source %q: %w", displayName(scraper.Name, "Prowlarr"), err)
		}
		added := 0
		for _, pi := range indexers {
			if !shouldUseProwlarrIndexer(pi, "torrent") {
				continue
			}
			expandedScrapers = append(expandedScrapers, config.TorrentScraperConfig{
				Name:              prowlarrGeneratedName(scraper.Name, pi.Name),
				Type:              "prowlarr",
				URL:               joinProwlarrIndexerURL(scraper.URL, pi.ID),
				APIKey:            scraper.APIKey,
				Options:           scraper.Options,
				Enabled:           scraper.Enabled,
				SkipNameFiltering: scraper.SkipNameFiltering,
				Config:            scraper.Config,
			})
			added++
		}
		if added == 0 {
			return fmt.Errorf("expand Prowlarr torrent source %q: no enabled torrent indexers with search support were found", displayName(scraper.Name, "Prowlarr"))
		}
	}
	s.TorrentScrapers = expandedScrapers

	return nil
}

func fetchProwlarrIndexers(ctx context.Context, baseURL, apiKey string) ([]prowlarrIndexerInfo, error) {
	baseURL = strings.TrimSpace(baseURL)
	apiKey = strings.TrimSpace(apiKey)
	if baseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("API key is required")
	}

	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid base URL %q", baseURL)
	}
	u.Path = path.Join(u.Path, "/api/v1/indexer")
	u.RawQuery = ""
	u.Fragment = ""

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := apiusage.Do(http.DefaultClient, "Prowlarr", "Indexer discovery", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("Prowlarr discovery failed: %s", msg)
	}

	var indexers []prowlarrIndexerInfo
	if err := json.NewDecoder(resp.Body).Decode(&indexers); err != nil {
		return nil, fmt.Errorf("decode Prowlarr indexers: %w", err)
	}
	return indexers, nil
}

func shouldUseProwlarrIndexer(idx prowlarrIndexerInfo, protocol string) bool {
	return idx.ID > 0 &&
		idx.Enable &&
		idx.SupportsSearch &&
		strings.EqualFold(strings.TrimSpace(idx.Protocol), protocol)
}

func isProwlarrPerIndexerURL(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return false
	}
	last := parts[len(parts)-1]
	if strings.EqualFold(last, "api") && len(parts) > 1 {
		last = parts[len(parts)-2]
	}
	id, err := strconv.Atoi(last)
	return err == nil && id > 0
}

func joinProwlarrIndexerURL(baseURL string, id int) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/" + strconv.Itoa(id)
}

func prowlarrGeneratedName(prefix, indexerName string) string {
	indexerName = strings.TrimSpace(indexerName)
	if indexerName == "" {
		indexerName = "Indexer"
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || strings.EqualFold(prefix, "prowlarr") {
		prefix = "Prowlarr"
	}
	return prefix + " - " + indexerName
}

func displayName(name, fallback string) string {
	if strings.TrimSpace(name) == "" {
		return fallback
	}
	return strings.TrimSpace(name)
}

// reloadServices reloads services that cache configuration at startup
func (h *SettingsHandler) reloadServices(s config.Settings) {
	// Reload NNTP connection pool with new usenet providers
	if h.PoolManager != nil {
		providers := config.ToNNTPProviders(s.Usenet)
		if err := h.PoolManager.SetProviders(providers); err != nil {
			log.Printf("[settings] failed to reload usenet pool: %v", err)
		} else {
			log.Printf("[settings] reloaded usenet pool with %d provider(s)", len(providers))
		}
	}

	// Reload metadata service with new API keys
	if h.MetadataService != nil {
		h.MetadataService.SetYTDLPProxyURL(s.Playback.YouTubeProxyURL)
		h.MetadataService.SetAllowAdultSearch(s.Metadata.AllowAdultSearch)
		h.MetadataService.UpdateAPIKeys(s.Metadata.TVDBAPIKey, s.Metadata.TMDBAPIKey, s.Metadata.EffectivePrimaryLanguage(), metadata.AIConfig{
			Provider: s.Metadata.AIProvider,
			APIKey:   s.Metadata.AIAPIKey,
			Model:    s.Metadata.AIModel,
			BaseURL:  s.Metadata.AIBaseURL,
		})
		log.Printf("[settings] reloaded metadata service API keys")

		// Reload MDBList settings (rating sources, API key, enabled state)
		h.MetadataService.UpdateMDBListSettings(metadata.MDBListConfig{
			APIKey:         s.MDBList.APIKey,
			Enabled:        s.MDBList.Enabled,
			EnabledRatings: s.MDBList.EnabledRatings,
		})
		log.Printf("[settings] reloaded MDBList settings (enabled=%v, ratings=%v)", s.MDBList.Enabled, s.MDBList.EnabledRatings)
	}
	if h.MDBListListsClient != nil {
		h.MDBListListsClient.UpdateAPIKey(s.MDBList.APIKey)
	}

	// Reload debrid scrapers (Torrentio, Jackett, etc.)
	if h.DebridSearchService != nil {
		h.DebridSearchService.ReloadScrapers()
	}
}

// ClearMetadataCache clears all cached metadata files and images
func (h *SettingsHandler) ClearMetadataCache(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.MetadataService == nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "metadata service not available"})
		return
	}
	if err := h.MetadataService.ClearCache(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	log.Printf("[settings] metadata cache cleared by user request")

	// Also clear image cache if available
	if h.ImageHandler != nil {
		if err := h.ImageHandler.ClearCache(); err != nil {
			log.Printf("[settings] warning: failed to clear image cache: %v", err)
		} else {
			log.Printf("[settings] image cache cleared by user request")
		}
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Metadata and image cache cleared"})
}

type epgGuideConfigSummary struct {
	globalEnabled         bool
	globalXMLTVConfigured bool
	globalSourceCount     int
	globalEnabledSources  int
	sourceEPGConfigured   int
	sourceEPGEnabled      int
	xtreamConfigured      bool
	taskCount             int
}

func summarizeEPGGuideConfig(s *config.Settings) epgGuideConfigSummary {
	summary := epgGuideConfigSummary{
		globalEnabled:         s.Live.EPG.Enabled,
		globalXMLTVConfigured: strings.TrimSpace(s.Live.EPG.XmltvUrl) != "",
		globalSourceCount:     len(s.Live.EPG.Sources),
		xtreamConfigured:      hasEffectiveXtreamEPGConfig(s),
	}
	for _, source := range s.Live.EPG.Sources {
		if source.Enabled {
			summary.globalEnabledSources++
		}
	}
	for _, source := range configuredLivePlaylistSources(s) {
		if strings.TrimSpace(source.EPG.XmltvUrl) != "" || len(source.EPG.Sources) > 0 {
			summary.sourceEPGConfigured++
		}
		if livePlaylistSourceEnabled(source) && source.EPG.Enabled {
			summary.sourceEPGEnabled++
		}
	}
	for _, task := range s.ScheduledTasks.Tasks {
		if task.Type == config.ScheduledTaskTypeEPGRefresh {
			summary.taskCount++
		}
	}
	return summary
}

func (summary epgGuideConfigSummary) hasGuideConfig() bool {
	return summary.globalEnabled ||
		summary.globalXMLTVConfigured ||
		summary.globalSourceCount > 0 ||
		summary.sourceEPGConfigured > 0 ||
		summary.sourceEPGEnabled > 0 ||
		summary.xtreamConfigured
}

func logEPGGuideConfig(reason string, summary epgGuideConfigSummary) {
	log.Printf("[settings] EPG guide config (%s): globalEnabled=%v globalXMLTVConfigured=%v globalSources=%d globalEnabledSources=%d sourceEPGConfigured=%d sourceEPGEnabled=%d xtreamConfigured=%v epgTaskCount=%d",
		reason,
		summary.globalEnabled,
		summary.globalXMLTVConfigured,
		summary.globalSourceCount,
		summary.globalEnabledSources,
		summary.sourceEPGConfigured,
		summary.sourceEPGEnabled,
		summary.xtreamConfigured,
		summary.taskCount,
	)
}

func epgConfigFingerprint(s config.Settings) string {
	var b strings.Builder
	b.WriteString(strconv.FormatBool(s.Live.EPG.Enabled))
	b.WriteString("|")
	b.WriteString(strings.TrimSpace(s.Live.EPG.XmltvUrl))
	b.WriteString(s.Live.Mode)
	b.WriteString(s.Live.HDHomeRunHost)
	b.WriteString("|guide:")
	b.WriteString(s.Live.HDHomeRunGuideEmail)
	b.WriteString(":")
	b.WriteString(s.Live.HDHomeRunGuideDeviceIDs)
	for _, source := range s.Live.EPG.Sources {
		b.WriteString("|g:")
		b.WriteString(source.ID)
		b.WriteString(":")
		b.WriteString(strconv.FormatBool(source.Enabled))
		b.WriteString(":")
		b.WriteString(strings.TrimSpace(source.Type))
		b.WriteString(":")
		b.WriteString(strings.TrimSpace(source.URL))
	}
	for _, source := range configuredLivePlaylistSources(&s) {
		b.WriteString(source.Mode)
		b.WriteString(":")
		b.WriteString(source.HDHomeRunHost)
		b.WriteString("|guide:")
		b.WriteString(source.HDHomeRunGuideEmail)
		b.WriteString(":")
		b.WriteString(source.HDHomeRunGuideDeviceIDs)
		b.WriteString(":")
		b.WriteString("|s:")
		b.WriteString(source.ID)
		b.WriteString(":")
		b.WriteString(strconv.FormatBool(livePlaylistSourceEnabled(source)))
		b.WriteString(":")
		b.WriteString(strconv.FormatBool(source.EPG.Enabled))
		b.WriteString(":")
		b.WriteString(strings.TrimSpace(source.EPG.XmltvUrl))
		for _, epgSource := range source.EPG.Sources {
			b.WriteString(":")
			b.WriteString(epgSource.ID)
			b.WriteString(":")
			b.WriteString(strconv.FormatBool(epgSource.Enabled))
			b.WriteString(":")
			b.WriteString(strings.TrimSpace(epgSource.Type))
			b.WriteString(":")
			b.WriteString(strings.TrimSpace(epgSource.URL))
		}
	}
	return b.String()
}

func livePlaylistSourceEnabled(source config.LivePlaylistSource) bool {
	return source.Enabled == nil || *source.Enabled
}

func configuredLivePlaylistSources(s *config.Settings) []config.LivePlaylistSource {
	if len(s.Live.Sources) > 0 {
		return s.Live.Sources
	}
	return s.Live.PlaylistSources
}

func hasEffectiveXtreamEPGConfig(s *config.Settings) bool {
	sources := configuredLivePlaylistSources(s)
	if len(sources) > 0 {
		for _, source := range sources {
			if !livePlaylistSourceEnabled(source) || !strings.EqualFold(strings.TrimSpace(source.Mode), "xtream") {
				continue
			}
			if strings.TrimSpace(source.XtreamHost) == "" ||
				strings.TrimSpace(source.XtreamUsername) == "" ||
				strings.TrimSpace(source.XtreamPassword) == "" {
				continue
			}
			if s.Live.EPG.Enabled || source.EPG.Enabled {
				return true
			}
		}
		return false
	}
	return s.Live.EPG.Enabled &&
		strings.EqualFold(strings.TrimSpace(s.Live.Mode), "xtream") &&
		strings.TrimSpace(s.Live.XtreamHost) != "" &&
		strings.TrimSpace(s.Live.XtreamUsername) != "" &&
		strings.TrimSpace(s.Live.XtreamPassword) != ""
}

// EnsureEPGTaskForGuide auto-creates an EPG refresh task when guide/EPG
// configuration exists and no EPG refresh task already exists. It also removes
// auto-created EPG tasks when all guide configuration is absent.
func (h *SettingsHandler) EnsureEPGTaskForGuide(s *config.Settings, reason string) bool {
	summary := summarizeEPGGuideConfig(s)
	if h.EPGService != nil && h.EPGService.HasHDHomeRunGuideForSettings(*s) {
		summary.sourceEPGEnabled++
	}
	logEPGGuideConfig(reason, summary)

	if !summary.hasGuideConfig() {
		changed := false
		filtered := s.ScheduledTasks.Tasks[:0]
		for _, task := range s.ScheduledTasks.Tasks {
			if task.Type == config.ScheduledTaskTypeEPGRefresh && strings.Contains(task.ID, "auto") {
				log.Printf("[settings] removing auto-created EPG refresh task (id=%s) because no guide/EPG config is present", task.ID)
				changed = true
				continue
			}
			filtered = append(filtered, task)
		}
		s.ScheduledTasks.Tasks = filtered
		return changed
	}

	// Check if an EPG refresh task already exists
	for _, task := range s.ScheduledTasks.Tasks {
		if task.Type == config.ScheduledTaskTypeEPGRefresh {
			log.Printf("[settings] EPG refresh task already present (reason=%s id=%s enabled=%v frequency=%s status=%s)", reason, task.ID, task.Enabled, task.Frequency, task.LastStatus)
			return false
		}
	}

	// Create default EPG refresh task
	epgTask := config.ScheduledTask{
		ID:         "auto-epg-refresh",
		Type:       config.ScheduledTaskTypeEPGRefresh,
		Name:       "EPG Refresh",
		Enabled:    summary.globalEnabled || summary.sourceEPGEnabled > 0,
		Frequency:  config.ScheduledTaskFrequency12Hours,
		Config:     map[string]string{},
		LastStatus: config.ScheduledTaskStatusPending,
		CreatedAt:  time.Now(),
	}
	s.ScheduledTasks.Tasks = append(s.ScheduledTasks.Tasks, epgTask)
	log.Printf("[settings] auto-created EPG refresh task (reason=%s enabled=%v)", reason, epgTask.Enabled)
	return true
}

// ensurePlaylistTaskIfConfigured auto-creates a playlist refresh task when Live TV is configured
// and no playlist refresh task already exists. Removes auto-created playlist tasks when unconfigured.
func (h *SettingsHandler) ensurePlaylistTaskIfConfigured(s *config.Settings) {
	var liveTVConfigured bool
	for _, src := range configuredLivePlaylistSources(s) {
		if !livePlaylistSourceEnabled(src) {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(src.Mode)) {
		case "hdhomerun":
			_, err := config.HDHomeRunURL(src.HDHomeRunHost, "/lineup.m3u")
			liveTVConfigured = err == nil
		case "xtream":
			liveTVConfigured = strings.TrimSpace(src.XtreamHost) != "" && strings.TrimSpace(src.XtreamUsername) != "" && strings.TrimSpace(src.XtreamPassword) != ""
		case "stremio":
			liveTVConfigured = strings.TrimSpace(src.ManifestURL) != ""
		case "stalker":
			liveTVConfigured = strings.TrimSpace(src.StalkerPortalURL) != "" && strings.TrimSpace(src.StalkerMAC) != ""
		default:
			liveTVConfigured = strings.TrimSpace(src.PlaylistURL) != ""
		}
		if liveTVConfigured {
			break
		}
	}
	if !liveTVConfigured {
		switch s.Live.Mode {
		case "hdhomerun":
			_, err := config.HDHomeRunURL(s.Live.HDHomeRunHost, "/lineup.m3u")
			liveTVConfigured = err == nil
		case "m3u":
			liveTVConfigured = strings.TrimSpace(s.Live.PlaylistURL) != ""
		case "xtream":
			liveTVConfigured = strings.TrimSpace(s.Live.XtreamHost) != "" &&
				strings.TrimSpace(s.Live.XtreamUsername) != "" &&
				strings.TrimSpace(s.Live.XtreamPassword) != ""
		case "stalker":
			liveTVConfigured = strings.TrimSpace(s.Live.StalkerPortalURL) != "" && strings.TrimSpace(s.Live.StalkerMAC) != ""
		case "stremio":
			liveTVConfigured = strings.TrimSpace(s.Live.ManifestURL) != ""
		default:
			liveTVConfigured = false
		}
	}

	if !liveTVConfigured {
		// Live TV is not configured - remove any auto-created playlist refresh tasks
		filtered := s.ScheduledTasks.Tasks[:0]
		for _, task := range s.ScheduledTasks.Tasks {
			// Only remove auto-created playlist tasks (ID contains "auto")
			if task.Type == config.ScheduledTaskTypePlaylistRefresh && strings.Contains(task.ID, "auto") {
				log.Printf("[settings] removing auto-created playlist refresh task (id=%s) because Live TV is not configured", task.ID)
				continue
			}
			filtered = append(filtered, task)
		}
		s.ScheduledTasks.Tasks = filtered
		return
	}

	// Check if a playlist refresh task already exists
	for _, task := range s.ScheduledTasks.Tasks {
		if task.Type == config.ScheduledTaskTypePlaylistRefresh {
			return // Task already exists
		}
	}

	// Create default playlist refresh task
	playlistTask := config.ScheduledTask{
		ID:         "auto-playlist-refresh",
		Type:       config.ScheduledTaskTypePlaylistRefresh,
		Name:       "Live TV Playlist Refresh",
		Enabled:    true,
		Frequency:  config.ScheduledTaskFrequencyDaily,
		Config:     map[string]string{},
		LastStatus: config.ScheduledTaskStatusPending,
		CreatedAt:  time.Now(),
	}
	s.ScheduledTasks.Tasks = append(s.ScheduledTasks.Tasks, playlistTask)
	log.Printf("[settings] auto-created playlist refresh task because Live TV is configured")
}

// triggerEPGRefreshIfNewSources triggers an immediate EPG refresh if new sources were added.
// This provides a better UX so users don't have to wait for the scheduled task to run.
func (h *SettingsHandler) triggerEPGRefreshIfNewSources(oldSettings config.Settings, newSettings config.Settings) {
	if h.EPGService == nil {
		return
	}

	newSummary := summarizeEPGGuideConfig(&newSettings)
	if !newSummary.globalEnabled && newSummary.sourceEPGEnabled == 0 {
		log.Printf("[settings] skipping immediate EPG refresh because no enabled guide/EPG config is present")
		return
	}

	// Build a set of old source IDs
	oldSourceIDs := make(map[string]bool)
	for _, src := range oldSettings.Live.EPG.Sources {
		oldSourceIDs[src.ID] = true
	}

	// Check for new sources
	hasNewSources := false
	for _, src := range newSettings.Live.EPG.Sources {
		if src.Enabled && !oldSourceIDs[src.ID] {
			hasNewSources = true
			log.Printf("[settings] detected new EPG source: %s (%s)", src.Name, src.ID)
			break
		}
	}

	// Also check if EPG was just enabled (and has sources or a simple URL configured)
	epgJustEnabled := !oldSettings.Live.EPG.Enabled && newSettings.Live.EPG.Enabled
	hasEPGConfig := len(newSettings.Live.EPG.Sources) > 0 || newSettings.Live.EPG.XmltvUrl != "" ||
		newSummary.xtreamConfigured ||
		newSummary.sourceEPGConfigured > 0
	epgConfigChanged := epgConfigFingerprint(oldSettings) != epgConfigFingerprint(newSettings)

	if hasNewSources || (epgJustEnabled && hasEPGConfig) || epgConfigChanged {
		log.Printf("[settings] triggering immediate EPG refresh due to new configuration")
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			if err := h.EPGService.Refresh(ctx); err != nil {
				log.Printf("[settings] auto EPG refresh failed: %v", err)
			} else {
				log.Printf("[settings] auto EPG refresh completed successfully")
			}
		}()
	}
}
