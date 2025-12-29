package handlers

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"novastream/config"
	"novastream/models"
	"novastream/services/accounts"
	"novastream/services/sessions"
	"novastream/services/trakt"
	user_settings "novastream/services/user_settings"
	"novastream/services/users"
)

//go:embed account_templates/*.html
var accountTemplatesFS embed.FS

// AccountUIHandler handles the regular account web UI.
type AccountUIHandler struct {
	templates           *template.Template
	loginTemplate       *template.Template
	dashboardTemplate   *template.Template
	statusTemplate      *template.Template
	settingsTemplate    *template.Template
	historyTemplate     *template.Template
	searchTemplate      *template.Template
	toolsTemplate       *template.Template
	accountsService     *accounts.Service
	sessionsService     *sessions.Service
	usersService        *users.Service
	userSettingsService *user_settings.Service
	hlsManager          *HLSManager
	configManager       *config.Manager
	traktClient         *trakt.Client
}

// NewAccountUIHandler creates a new account UI handler.
func NewAccountUIHandler(accountsSvc *accounts.Service, sessionsSvc *sessions.Service, usersSvc *users.Service, userSettingsSvc *user_settings.Service, hlsManager *HLSManager, configManager *config.Manager, traktClient *trakt.Client) *AccountUIHandler {
	funcMap := template.FuncMap{
		"json": func(v interface{}) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
		"slice": func(s string, start, end int) string {
			if start < 0 {
				start = 0
			}
			if end > len(s) {
				end = len(s)
			}
			if start >= len(s) {
				return ""
			}
			return s[start:end]
		},
	}

	// Parse base template first
	baseTmpl := template.Must(template.New("").Funcs(funcMap).ParseFS(accountTemplatesFS, "account_templates/base.html"))

	// Clone base and add each page template
	dashboardTmpl := template.Must(template.Must(baseTmpl.Clone()).ParseFS(accountTemplatesFS, "account_templates/dashboard.html"))
	loginTmpl := template.Must(template.Must(baseTmpl.Clone()).ParseFS(accountTemplatesFS, "account_templates/login.html"))
	statusTmpl := template.Must(template.Must(baseTmpl.Clone()).ParseFS(accountTemplatesFS, "account_templates/status.html"))
	settingsTmpl := template.Must(template.Must(baseTmpl.Clone()).ParseFS(accountTemplatesFS, "account_templates/settings.html"))
	historyTmpl := template.Must(template.Must(baseTmpl.Clone()).ParseFS(accountTemplatesFS, "account_templates/history.html"))
	searchTmpl := template.Must(template.Must(baseTmpl.Clone()).ParseFS(accountTemplatesFS, "account_templates/search.html"))
	toolsTmpl := template.Must(template.Must(baseTmpl.Clone()).ParseFS(accountTemplatesFS, "account_templates/tools.html"))

	return &AccountUIHandler{
		templates:           dashboardTmpl,
		loginTemplate:       loginTmpl,
		dashboardTemplate:   dashboardTmpl,
		statusTemplate:      statusTmpl,
		settingsTemplate:    settingsTmpl,
		historyTemplate:     historyTmpl,
		searchTemplate:      searchTmpl,
		toolsTemplate:       toolsTmpl,
		accountsService:     accountsSvc,
		sessionsService:     sessionsSvc,
		usersService:        usersSvc,
		userSettingsService: userSettingsSvc,
		hlsManager:          hlsManager,
		configManager:       configManager,
		traktClient:         traktClient,
	}
}

// sessionCookieName is the shared session cookie name (same as admin UI)
const sessionCookieName = "strmr_admin_session"

// LoginPage redirects to the unified login page.
func (h *AccountUIHandler) LoginPage(w http.ResponseWriter, r *http.Request) {
	// Redirect to unified login
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// LoginSubmit redirects to the unified login page (POST should go to /admin/login).
func (h *AccountUIHandler) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// Logout handles logout.
func (h *AccountUIHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		h.sessionsService.Revoke(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// RequireAuth is middleware that requires authentication for regular accounts.
func (h *AccountUIHandler) RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}

		session, err := h.sessionsService.Validate(cookie.Value)
		if err != nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}

		// Don't allow master accounts here - redirect to admin
		if session.IsMaster {
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}

		// Store session in request context
		r = r.WithContext(contextWithSession(r.Context(), &session))
		next(w, r)
	}
}

// Dashboard renders the main account dashboard (profiles page).
func (h *AccountUIHandler) Dashboard(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}

	account, ok := h.accountsService.Get(session.AccountID)
	if !ok {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}

	profiles := h.usersService.ListForAccount(session.AccountID)

	data := map[string]interface{}{
		"Account":    account,
		"Profiles":   profiles,
		"ActivePage": "profiles",
	}

	h.dashboardTemplate.ExecuteTemplate(w, "dashboard", data)
}

// StatusPage renders the status monitoring page.
func (h *AccountUIHandler) StatusPage(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}

	account, ok := h.accountsService.Get(session.AccountID)
	if !ok {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}

	profiles := h.usersService.ListForAccount(session.AccountID)

	data := map[string]interface{}{
		"Account":    account,
		"Profiles":   profiles,
		"ActivePage": "status",
	}

	h.statusTemplate.ExecuteTemplate(w, "status", data)
}

// GetStatus returns basic backend status as JSON.
func (h *AccountUIHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	status := map[string]interface{}{
		"reachable": true,
		"timestamp": time.Now(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// GetStreams returns active streams filtered to the account's profiles.
func (h *AccountUIHandler) GetStreams(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Get account's profile IDs for filtering
	profiles := h.usersService.ListForAccount(session.AccountID)
	profileIDs := make(map[string]bool)
	for _, p := range profiles {
		profileIDs[p.ID] = true
	}

	streams := []map[string]interface{}{}

	// Get HLS sessions filtered by profile
	if h.hlsManager != nil {
		h.hlsManager.mu.RLock()
		for _, sess := range h.hlsManager.sessions {
			sess.mu.RLock()
			// Only include if profile belongs to this account
			if profileIDs[sess.ProfileID] {
				filename := filepath.Base(sess.Path)
				if filename == "" || filename == "." {
					filename = filepath.Base(sess.OriginalPath)
				}
				streams = append(streams, map[string]interface{}{
					"id":             sess.ID,
					"type":           "hls",
					"path":           sess.Path,
					"original_path":  sess.OriginalPath,
					"filename":       filename,
					"profile_id":     sess.ProfileID,
					"profile_name":   sess.ProfileName,
					"created_at":     sess.CreatedAt,
					"last_access":    sess.LastAccess,
					"duration":       sess.Duration,
					"bytes_streamed": sess.BytesStreamed,
					"has_dv":         sess.HasDV && !sess.DVDisabled,
					"has_hdr":        sess.HasHDR,
					"dv_profile":     sess.DVProfile,
					"segments":       sess.SegmentsCreated,
				})
			}
			sess.mu.RUnlock()
		}
		h.hlsManager.mu.RUnlock()
	}

	// Get direct streams from the global tracker, filtered by profile
	tracker := GetStreamTracker()
	for _, stream := range tracker.GetActiveStreams() {
		// Only include if profile belongs to this account
		if profileIDs[stream.ProfileID] {
			streams = append(streams, map[string]interface{}{
				"id":             stream.ID,
				"type":           "direct",
				"path":           stream.Path,
				"filename":       stream.Filename,
				"profile_id":     stream.ProfileID,
				"profile_name":   stream.ProfileName,
				"client_ip":      stream.ClientIP,
				"created_at":     stream.StartTime,
				"last_access":    stream.LastActivity,
				"bytes_streamed": stream.BytesStreamed,
				"content_length": stream.ContentLength,
				"user_agent":     stream.UserAgent,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"streams": streams,
	})
}

// SettingsPage renders the profile settings page.
func (h *AccountUIHandler) SettingsPage(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}

	account, ok := h.accountsService.Get(session.AccountID)
	if !ok {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}

	profiles := h.usersService.ListForAccount(session.AccountID)

	data := map[string]interface{}{
		"Account":    account,
		"Profiles":   profiles,
		"ActivePage": "settings",
	}

	h.settingsTemplate.ExecuteTemplate(w, "settings", data)
}

// GetUserSettings returns user-specific settings as JSON.
func (h *AccountUIHandler) GetUserSettings(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		http.Error(w, "profileId parameter required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to this account
	if !h.usersService.BelongsToAccount(profileID, session.AccountID) {
		http.Error(w, "Profile not found", http.StatusNotFound)
		return
	}

	if h.userSettingsService == nil {
		http.Error(w, "User settings service not available", http.StatusInternalServerError)
		return
	}

	// Use DefaultUserSettings as defaults
	defaults := models.DefaultUserSettings()

	userSettings, err := h.userSettingsService.GetWithDefaults(profileID, defaults)
	if err != nil {
		http.Error(w, "Failed to load user settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(userSettings)
}

// SaveUserSettings saves user-specific settings.
func (h *AccountUIHandler) SaveUserSettings(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		http.Error(w, "profileId parameter required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to this account
	if !h.usersService.BelongsToAccount(profileID, session.AccountID) {
		http.Error(w, "Profile not found", http.StatusNotFound)
		return
	}

	if h.userSettingsService == nil {
		http.Error(w, "User settings service not available", http.StatusInternalServerError)
		return
	}

	var settings models.UserSettings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.userSettingsService.Update(profileID, settings); err != nil {
		http.Error(w, "Failed to save user settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
}

// HistoryPage renders the watch history page.
func (h *AccountUIHandler) HistoryPage(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}

	account, ok := h.accountsService.Get(session.AccountID)
	if !ok {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}

	profiles := h.usersService.ListForAccount(session.AccountID)

	data := map[string]interface{}{
		"Account":    account,
		"Profiles":   profiles,
		"ActivePage": "history",
	}

	h.historyTemplate.ExecuteTemplate(w, "history", data)
}

// SearchPage renders the content search page.
func (h *AccountUIHandler) SearchPage(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}

	account, ok := h.accountsService.Get(session.AccountID)
	if !ok {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}

	profiles := h.usersService.ListForAccount(session.AccountID)

	data := map[string]interface{}{
		"Account":    account,
		"Profiles":   profiles,
		"ActivePage": "search",
	}

	h.searchTemplate.ExecuteTemplate(w, "search", data)
}

// ToolsPage renders the tools page (Trakt integration, etc).
func (h *AccountUIHandler) ToolsPage(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}

	account, ok := h.accountsService.Get(session.AccountID)
	if !ok {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}

	profiles := h.usersService.ListForAccount(session.AccountID)

	data := map[string]interface{}{
		"Account":    account,
		"Profiles":   profiles,
		"ActivePage": "tools",
	}

	h.toolsTemplate.ExecuteTemplate(w, "tools", data)
}

// GetProfiles returns profiles for the authenticated account.
func (h *AccountUIHandler) GetProfiles(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	profiles := h.usersService.ListForAccount(session.AccountID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(profiles)
}

// CreateProfile creates a new profile for the authenticated account.
func (h *AccountUIHandler) CreateProfile(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}

	profile, err := h.usersService.CreateForAccount(session.AccountID, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if req.Color != "" {
		profile, _ = h.usersService.SetColor(profile.ID, req.Color)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(profile)
}

// RenameProfile renames a profile.
func (h *AccountUIHandler) RenameProfile(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		http.Error(w, "profileId parameter required", http.StatusBadRequest)
		return
	}

	// Verify ownership
	if !h.usersService.BelongsToAccount(profileID, session.AccountID) {
		http.Error(w, "Profile not found", http.StatusNotFound)
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	profile, err := h.usersService.Rename(profileID, strings.TrimSpace(req.Name))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(profile)
}

// DeleteProfile deletes a profile.
func (h *AccountUIHandler) DeleteProfile(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		http.Error(w, "profileId parameter required", http.StatusBadRequest)
		return
	}

	// Verify ownership
	if !h.usersService.BelongsToAccount(profileID, session.AccountID) {
		http.Error(w, "Profile not found", http.StatusNotFound)
		return
	}

	// Check if this is the last profile for the account
	profiles := h.usersService.ListForAccount(session.AccountID)
	if len(profiles) <= 1 {
		http.Error(w, "Cannot delete last profile", http.StatusBadRequest)
		return
	}

	if err := h.usersService.Delete(profileID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}

// SetProfileColor sets a profile's color.
func (h *AccountUIHandler) SetProfileColor(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		http.Error(w, "profileId parameter required", http.StatusBadRequest)
		return
	}

	// Verify ownership
	if !h.usersService.BelongsToAccount(profileID, session.AccountID) {
		http.Error(w, "Profile not found", http.StatusNotFound)
		return
	}

	var req struct {
		Color string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	profile, err := h.usersService.SetColor(profileID, req.Color)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(profile)
}

// SetProfilePin sets a profile's PIN.
func (h *AccountUIHandler) SetProfilePin(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		http.Error(w, "profileId parameter required", http.StatusBadRequest)
		return
	}

	// Verify ownership
	if !h.usersService.BelongsToAccount(profileID, session.AccountID) {
		http.Error(w, "Profile not found", http.StatusNotFound)
		return
	}

	var req struct {
		Pin string `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if len(req.Pin) < 4 {
		http.Error(w, "PIN must be at least 4 characters", http.StatusBadRequest)
		return
	}

	profile, err := h.usersService.SetPin(profileID, req.Pin)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(profile)
}

// ClearProfilePin clears a profile's PIN.
func (h *AccountUIHandler) ClearProfilePin(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		http.Error(w, "profileId parameter required", http.StatusBadRequest)
		return
	}

	// Verify ownership
	if !h.usersService.BelongsToAccount(profileID, session.AccountID) {
		http.Error(w, "Profile not found", http.StatusNotFound)
		return
	}

	profile, err := h.usersService.ClearPin(profileID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(profile)
}

// SetKidsProfile toggles kids profile mode.
func (h *AccountUIHandler) SetKidsProfile(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		http.Error(w, "profileId parameter required", http.StatusBadRequest)
		return
	}

	// Verify ownership
	if !h.usersService.BelongsToAccount(profileID, session.AccountID) {
		http.Error(w, "Profile not found", http.StatusNotFound)
		return
	}

	var req struct {
		IsKidsProfile bool `json:"isKidsProfile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	profile, err := h.usersService.SetKidsProfile(profileID, req.IsKidsProfile)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(profile)
}

// ChangePassword changes the account password.
func (h *AccountUIHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	account, ok := h.accountsService.Get(session.AccountID)
	if !ok {
		http.Error(w, "Account not found", http.StatusNotFound)
		return
	}

	// Verify current password
	if _, err := h.accountsService.Authenticate(account.Username, req.CurrentPassword); err != nil {
		http.Error(w, "Current password is incorrect", http.StatusUnauthorized)
		return
	}

	// Update password
	if err := h.accountsService.UpdatePassword(session.AccountID, req.NewPassword); err != nil {
		http.Error(w, "Failed to update password", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "password changed"})
}

// GetProfileSettings returns settings for a specific profile.
// This is a placeholder - the frontend should use the app's settings API.
func (h *AccountUIHandler) GetProfileSettings(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	profileId := r.URL.Path[len("/account/api/settings/"):]
	if profileId == "" {
		http.Error(w, "Profile ID required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to this account
	if !h.profileBelongsToAccount(profileId, session.AccountID) {
		http.Error(w, "Profile not found", http.StatusNotFound)
		return
	}

	// Return placeholder settings - actual settings managed in app
	settings := map[string]interface{}{
		"playback": map[string]interface{}{
			"preferredPlayer":           "native",
			"preferredSubtitleLanguage": "en",
			"preferredSubtitleMode":     "auto",
			"useLoadingScreen":          false,
		},
		"filtering": map[string]interface{}{
			"maxSizeMovieGb":   0,
			"maxSizeEpisodeGb": 0,
			"excludeHdr":       false,
			"prioritizeHdr":    true,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
}

// UpdateProfileSettings updates settings for a specific profile.
// This is a placeholder - the frontend should use the app's settings API.
func (h *AccountUIHandler) UpdateProfileSettings(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	profileId := r.URL.Path[len("/account/api/settings/"):]
	if profileId == "" {
		http.Error(w, "Profile ID required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to this account
	if !h.profileBelongsToAccount(profileId, session.AccountID) {
		http.Error(w, "Profile not found", http.StatusNotFound)
		return
	}

	// Just acknowledge - actual settings managed in app
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// GetContinueWatching returns items to continue watching for a profile.
func (h *AccountUIHandler) GetContinueWatching(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	profileId := extractProfileIdFromPath(r.URL.Path, "/account/api/history/", "/continue")
	if profileId == "" {
		http.Error(w, "Profile ID required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to this account
	if !h.profileBelongsToAccount(profileId, session.AccountID) {
		http.Error(w, "Profile not found", http.StatusNotFound)
		return
	}

	// Return continue watching items (placeholder - integrate with watch history service)
	items := []map[string]interface{}{}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

// GetWatchedHistory returns watched items for a profile.
func (h *AccountUIHandler) GetWatchedHistory(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	profileId := extractProfileIdFromPath(r.URL.Path, "/account/api/history/", "/watched")
	if profileId == "" {
		http.Error(w, "Profile ID required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to this account
	if !h.profileBelongsToAccount(profileId, session.AccountID) {
		http.Error(w, "Profile not found", http.StatusNotFound)
		return
	}

	// Return watched items (placeholder - integrate with watch history service)
	items := []map[string]interface{}{}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

// GetTraktAccounts returns available Trakt accounts for linking.
// For non-master accounts, only returns accounts owned by that account.
func (h *AccountUIHandler) GetTraktAccounts(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	settings, err := h.configManager.Load()
	if err != nil {
		http.Error(w, "Failed to load settings", http.StatusInternalServerError)
		return
	}

	// Check if this is a master account
	account, ok := h.accountsService.Get(session.AccountID)
	if !ok {
		http.Error(w, "Account not found", http.StatusUnauthorized)
		return
	}

	// Filter accounts based on ownership
	var filteredAccounts []map[string]interface{}
	for _, acc := range settings.Trakt.Accounts {
		// Master accounts see all accounts; non-master only see their own
		if account.IsMaster || acc.OwnerAccountID == session.AccountID {
			filteredAccounts = append(filteredAccounts, map[string]interface{}{
				"id":          acc.ID,
				"name":        acc.Name,
				"username":    acc.Username,
				"isConnected": acc.AccessToken != "",
			})
		}
	}

	if filteredAccounts == nil {
		filteredAccounts = []map[string]interface{}{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(filteredAccounts)
}

// CreateTraktAccount creates a new Trakt account owned by the current login account.
func (h *AccountUIHandler) CreateTraktAccount(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Name         string `json:"name"`
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(req.ClientID) == "" || strings.TrimSpace(req.ClientSecret) == "" {
		http.Error(w, "Client ID and Client Secret are required", http.StatusBadRequest)
		return
	}

	settings, err := h.configManager.Load()
	if err != nil {
		http.Error(w, "Failed to load settings", http.StatusInternalServerError)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Trakt Account"
	}

	newAccount := config.TraktAccount{
		ID:             generateUUID(),
		Name:           name,
		OwnerAccountID: session.AccountID, // Set owner to current login account
		ClientID:       strings.TrimSpace(req.ClientID),
		ClientSecret:   strings.TrimSpace(req.ClientSecret),
	}

	settings.Trakt.Accounts = append(settings.Trakt.Accounts, newAccount)

	if err := h.configManager.Save(settings); err != nil {
		http.Error(w, "Failed to save settings", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":          newAccount.ID,
		"name":        newAccount.Name,
		"isConnected": false,
	})
}

// DeleteTraktAccount deletes a Trakt account owned by the current login account.
func (h *AccountUIHandler) DeleteTraktAccount(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	accountID := r.URL.Query().Get("id")
	if accountID == "" {
		http.Error(w, "Account ID required", http.StatusBadRequest)
		return
	}

	settings, err := h.configManager.Load()
	if err != nil {
		http.Error(w, "Failed to load settings", http.StatusInternalServerError)
		return
	}

	// Find the account and verify ownership
	var found bool
	for i, acc := range settings.Trakt.Accounts {
		if acc.ID == accountID {
			// Verify ownership (only owner or master can delete)
			loginAccount, ok := h.accountsService.Get(session.AccountID)
			if !ok || (!loginAccount.IsMaster && acc.OwnerAccountID != session.AccountID) {
				http.Error(w, "Not authorized to delete this account", http.StatusForbidden)
				return
			}

			// Remove the account
			settings.Trakt.Accounts = append(settings.Trakt.Accounts[:i], settings.Trakt.Accounts[i+1:]...)
			found = true
			break
		}
	}

	if !found {
		http.Error(w, "Account not found", http.StatusNotFound)
		return
	}

	// Clear any profile associations with this account
	allUsers := h.usersService.List()
	for _, user := range allUsers {
		if user.TraktAccountID == accountID {
			h.usersService.ClearTraktAccountID(user.ID)
		}
	}

	if err := h.configManager.Save(settings); err != nil {
		http.Error(w, "Failed to save settings", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// StartTraktAuth initiates OAuth flow for a Trakt account.
func (h *AccountUIHandler) StartTraktAuth(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	accountID := r.URL.Query().Get("id")
	if accountID == "" {
		http.Error(w, "Account ID required", http.StatusBadRequest)
		return
	}

	settings, err := h.configManager.Load()
	if err != nil {
		http.Error(w, "Failed to load settings", http.StatusInternalServerError)
		return
	}

	account := settings.Trakt.GetAccountByID(accountID)
	if account == nil {
		http.Error(w, "Account not found", http.StatusNotFound)
		return
	}

	// Verify ownership
	loginAccount, ok := h.accountsService.Get(session.AccountID)
	if !ok || (!loginAccount.IsMaster && account.OwnerAccountID != session.AccountID) {
		http.Error(w, "Not authorized", http.StatusForbidden)
		return
	}

	if account.ClientID == "" || account.ClientSecret == "" {
		http.Error(w, "Account credentials not configured", http.StatusBadRequest)
		return
	}

	// Update client with account credentials
	h.traktClient.UpdateCredentials(account.ClientID, account.ClientSecret)

	deviceCode, err := h.traktClient.GetDeviceCode()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"deviceCode":      deviceCode.DeviceCode,
		"userCode":        deviceCode.UserCode,
		"verificationUrl": deviceCode.VerificationURL,
		"expiresIn":       deviceCode.ExpiresIn,
		"interval":        deviceCode.Interval,
	})
}

// CheckTraktAuth polls for OAuth token for a Trakt account.
func (h *AccountUIHandler) CheckTraktAuth(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	accountID := r.URL.Query().Get("id")
	deviceCode := r.URL.Query().Get("deviceCode")
	if accountID == "" || deviceCode == "" {
		http.Error(w, "Account ID and device code required", http.StatusBadRequest)
		return
	}

	settings, err := h.configManager.Load()
	if err != nil {
		http.Error(w, "Failed to load settings", http.StatusInternalServerError)
		return
	}

	account := settings.Trakt.GetAccountByID(accountID)
	if account == nil {
		http.Error(w, "Account not found", http.StatusNotFound)
		return
	}

	// Verify ownership
	loginAccount, ok := h.accountsService.Get(session.AccountID)
	if !ok || (!loginAccount.IsMaster && account.OwnerAccountID != session.AccountID) {
		http.Error(w, "Not authorized", http.StatusForbidden)
		return
	}

	// Update client with account credentials
	h.traktClient.UpdateCredentials(account.ClientID, account.ClientSecret)

	token, err := h.traktClient.PollForToken(deviceCode)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if token == nil {
		// Still pending
		json.NewEncoder(w).Encode(map[string]interface{}{
			"authenticated": false,
			"pending":       true,
		})
		return
	}

	// Token received, update account
	account.AccessToken = token.AccessToken
	account.RefreshToken = token.RefreshToken
	account.ExpiresAt = token.CreatedAt + int64(token.ExpiresIn)

	// Get user profile
	profile, err := h.traktClient.GetUserProfile(token.AccessToken)
	if err == nil && profile != nil {
		account.Username = profile.Username
		if account.Name == "" || account.Name == "Trakt Account" {
			account.Name = profile.Username
		}
	}

	settings.Trakt.UpdateAccount(*account)

	if err := h.configManager.Save(settings); err != nil {
		http.Error(w, "Failed to save token", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"authenticated": true,
		"username":      account.Username,
	})
}

// DisconnectTraktAccount removes OAuth tokens from a Trakt account.
func (h *AccountUIHandler) DisconnectTraktAccount(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	accountID := r.URL.Query().Get("id")
	if accountID == "" {
		http.Error(w, "Account ID required", http.StatusBadRequest)
		return
	}

	settings, err := h.configManager.Load()
	if err != nil {
		http.Error(w, "Failed to load settings", http.StatusInternalServerError)
		return
	}

	account := settings.Trakt.GetAccountByID(accountID)
	if account == nil {
		http.Error(w, "Account not found", http.StatusNotFound)
		return
	}

	// Verify ownership
	loginAccount, ok := h.accountsService.Get(session.AccountID)
	if !ok || (!loginAccount.IsMaster && account.OwnerAccountID != session.AccountID) {
		http.Error(w, "Not authorized", http.StatusForbidden)
		return
	}

	account.AccessToken = ""
	account.RefreshToken = ""
	account.ExpiresAt = 0
	account.Username = ""

	settings.Trakt.UpdateAccount(*account)

	if err := h.configManager.Save(settings); err != nil {
		http.Error(w, "Failed to save settings", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// generateUUID generates a new UUID for Trakt accounts.
func generateUUID() string {
	return uuid.NewString()
}

// SetProfileTrakt links a Trakt account to a profile.
func (h *AccountUIHandler) SetProfileTrakt(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	profileId := r.URL.Query().Get("profileId")
	if profileId == "" {
		http.Error(w, "Profile ID required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to this account
	if !h.profileBelongsToAccount(profileId, session.AccountID) {
		http.Error(w, "Profile not found", http.StatusNotFound)
		return
	}

	var req struct {
		TraktAccountID string `json:"traktAccountId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// Update profile with Trakt account
	if _, err := h.usersService.SetTraktAccountID(profileId, req.TraktAccountID); err != nil {
		http.Error(w, "Failed to link Trakt account", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// ClearProfileTrakt removes Trakt link from a profile.
func (h *AccountUIHandler) ClearProfileTrakt(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	profileId := r.URL.Query().Get("profileId")
	if profileId == "" {
		http.Error(w, "Profile ID required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to this account
	if !h.profileBelongsToAccount(profileId, session.AccountID) {
		http.Error(w, "Profile not found", http.StatusNotFound)
		return
	}

	// Clear Trakt account from profile
	if _, err := h.usersService.ClearTraktAccountID(profileId); err != nil {
		http.Error(w, "Failed to unlink Trakt account", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// AddToWatchlist adds an item to a profile's watchlist.
func (h *AccountUIHandler) AddToWatchlist(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	profileId := r.URL.Path[len("/account/api/watchlist/"):]
	if profileId == "" {
		http.Error(w, "Profile ID required", http.StatusBadRequest)
		return
	}

	// Verify profile belongs to this account
	if !h.profileBelongsToAccount(profileId, session.AccountID) {
		http.Error(w, "Profile not found", http.StatusNotFound)
		return
	}

	var req struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// Add to watchlist (placeholder - integrate with watchlist service)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "added"})
}

// profileBelongsToAccount checks if a profile belongs to the given account.
func (h *AccountUIHandler) profileBelongsToAccount(profileId, accountId string) bool {
	profiles := h.usersService.ListForAccount(accountId)
	for _, p := range profiles {
		if p.ID == profileId {
			return true
		}
	}
	return false
}

// extractProfileIdFromPath extracts profile ID from a path like /prefix/{id}/suffix
func extractProfileIdFromPath(path, prefix, suffix string) string {
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	path = path[len(prefix):]
	if suffix != "" && strings.HasSuffix(path, suffix) {
		path = path[:len(path)-len(suffix)]
	}
	return path
}

type accountSessionKey struct{}

// contextWithSession adds session to context.
func contextWithSession(ctx context.Context, session *models.Session) context.Context {
	return context.WithValue(ctx, accountSessionKey{}, session)
}

// sessionFromContext retrieves session from context.
func sessionFromContext(ctx context.Context) *models.Session {
	if session, ok := ctx.Value(accountSessionKey{}).(*models.Session); ok {
		return session
	}
	return nil
}
