package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"novastream/models"
	"novastream/services/notifications"
)

// isAdminRequest reports whether the request's session belongs to a master account.
func (h *AdminUIHandler) isAdminRequest(r *http.Request) bool {
	session := adminSessionFromContext(r.Context())
	return session != nil && session.IsMaster
}

type NotificationPageData struct {
	AdminPageData
	Profile models.User
}

func (h *AdminUIHandler) NotificationsPage(w http.ResponseWriter, r *http.Request) {
	profileID := strings.TrimSpace(r.URL.Query().Get("profileId"))
	if ok, _ := h.requireProfileScope(w, r, profileID); !ok {
		return
	}
	profile, ok := h.usersService.Get(profileID)
	if !ok {
		http.Error(w, "Profile not found", http.StatusNotFound)
		return
	}
	isAdmin, accountID, basePath, username := h.getPageRoleInfo(r)
	data := NotificationPageData{
		AdminPageData: AdminPageData{
			CurrentPath:    basePath + "/accounts",
			BasePath:       basePath,
			ServerBasePath: h.serverBasePath,
			IsAdmin:        isAdmin,
			AccountID:      accountID,
			Username:       username,
			Version:        GetBackendVersion(),
			BuildID:        GetBackendBuildID(),
		},
		Profile: profile,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if h.notificationsTemplate == nil {
		http.Error(w, "Notifications template not loaded", http.StatusInternalServerError)
		return
	}
	if err := h.notificationsTemplate.ExecuteTemplate(w, "base", data); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func (h *AdminUIHandler) ListNotificationChannels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	if h.notificationService == nil {
		http.Error(w, "Notifications unavailable", http.StatusServiceUnavailable)
		return
	}
	profileID := strings.TrimSpace(r.URL.Query().Get("profileId"))
	if ok, _ := h.requireProfileScope(w, r, profileID); !ok {
		return
	}
	channels, err := h.notificationService.ListChannels(r.Context(), profileID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"channels": channels})
}

func (h *AdminUIHandler) SaveNotificationChannel(w http.ResponseWriter, r *http.Request) {
	if h.notificationService == nil {
		http.Error(w, "Notifications unavailable", http.StatusServiceUnavailable)
		return
	}
	var channel models.NotificationChannel
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&channel); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if ok, _ := h.requireProfileScope(w, r, channel.ProfileID); !ok {
		return
	}
	if strings.EqualFold(strings.TrimSpace(channel.Type), models.NotificationChannelWebhook) && !h.isAdminRequest(r) {
		http.Error(w, notifications.ErrGenericWebhookAdminOnly.Error(), http.StatusForbidden)
		return
	}
	saved, err := h.notificationService.SaveChannel(r.Context(), channel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if h.calendarService != nil {
		h.calendarService.Invalidate(saved.ProfileID)
		h.calendarService.Refresh()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(saved)
}

func (h *AdminUIHandler) DeleteNotificationChannel(w http.ResponseWriter, r *http.Request) {
	if h.notificationService == nil {
		http.Error(w, "Notifications unavailable", http.StatusServiceUnavailable)
		return
	}
	profileID := strings.TrimSpace(r.URL.Query().Get("profileId"))
	if ok, _ := h.requireProfileScope(w, r, profileID); !ok {
		return
	}
	if err := h.notificationService.DeleteChannel(r.Context(), profileID, r.URL.Query().Get("id")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AdminUIHandler) TestNotificationChannel(w http.ResponseWriter, r *http.Request) {
	if h.notificationService == nil {
		http.Error(w, "Notifications unavailable", http.StatusServiceUnavailable)
		return
	}
	profileID := strings.TrimSpace(r.URL.Query().Get("profileId"))
	if ok, _ := h.requireProfileScope(w, r, profileID); !ok {
		return
	}
	if !h.isAdminRequest(r) {
		channels, err := h.notificationService.ListChannels(r.Context(), profileID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		testID := strings.TrimSpace(r.URL.Query().Get("id"))
		for _, channel := range channels {
			if channel.ID == testID && channel.Type == models.NotificationChannelWebhook {
				http.Error(w, notifications.ErrGenericWebhookAdminOnly.Error(), http.StatusForbidden)
				return
			}
		}
	}
	if err := h.notificationService.TestChannel(r.Context(), profileID, r.URL.Query().Get("id")); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
}
