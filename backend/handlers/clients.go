package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/client_settings"
	"novastream/services/clients"

	"github.com/gorilla/mux"
)

type clientsService interface {
	Register(id, userID, deviceType, os, appVersion, deviceName, nickname string) (models.Client, error)
	Get(id string) (*models.Client, error)
	List() []models.Client
	ListByUser(userID string) []models.Client
	Rename(id, name string) (models.Client, error)
	SetNickname(id, nickname string) (models.Client, error)
	SetFilterEnabled(id string, enabled bool) (models.Client, error)
	ReassignUser(id, fromUserID, newUserID string) (models.Client, error)
	UnassignProfile(id, userID string) (deletedDevice bool, err error)
	HasProfile(clientID, userID string) bool
	ProfileIDs(clientID string) []string
	UpdateLastSeen(id string) error
	Delete(id string) error
}

type clientSettingsService interface {
	Get(clientID, userID string) (*models.ClientFilterSettings, error)
	Update(clientID, userID string, settings models.ClientFilterSettings) error
	Delete(clientID, userID string) error
	DeleteByClient(clientID string) error
	Move(clientID, fromUserID, toUserID string) error
}

type clientOwnershipService interface {
	BelongsToAccount(profileID, accountID string) bool
}

var _ clientsService = (*clients.Service)(nil)
var _ clientSettingsService = (*client_settings.Service)(nil)

// pendingPing stores the timestamp when a ping was requested for a client
type pendingPing struct {
	timestamp time.Time
}

type pendingClientMessage struct {
	ID                  string
	Message             string
	TargetAll           bool
	ProfileIDs          map[string]struct{}
	TargetProfileCount  int
	CreatedAt           time.Time
	DeliveredProfileIDs map[string]struct{}
}

type ClientsHandler struct {
	clients         clientsService
	settings        clientSettingsService
	users           clientOwnershipService
	pendingPings    map[string]pendingPing
	pendingMessages []pendingClientMessage
	pingMu          sync.RWMutex
	messageMu       sync.Mutex
	configManager   *config.Manager
}

func (h *ClientsHandler) SetConfigManager(manager *config.Manager) {
	h.configManager = manager
}

const pingExpiry = 30 * time.Second // Pings expire after 30 seconds
const clientMessageExpiry = 24 * time.Hour
const clientMessageMaxLength = 1000

func NewClientsHandler(clientsSvc clientsService, settingsSvc clientSettingsService, usersSvc ...clientOwnershipService) *ClientsHandler {
	h := &ClientsHandler{
		clients:         clientsSvc,
		settings:        settingsSvc,
		pendingPings:    make(map[string]pendingPing),
		pendingMessages: make([]pendingClientMessage, 0),
	}
	if len(usersSvc) > 0 {
		h.users = usersSvc[0]
	}
	return h
}

func (h *ClientsHandler) canAccessProfile(r *http.Request, profileID string) bool {
	if auth.IsMaster(r) {
		return true
	}
	accountID := auth.GetAccountID(r)
	return accountID != "" && h.users != nil && h.users.BelongsToAccount(profileID, accountID)
}

// getAuthorizedClient loads a client and enforces account/profile ownership.
// logMiss controls whether missing/forbidden lookups are logged. Callers that
// poll frequently (ping, messages) should pass false to avoid log spam.
func (h *ClientsHandler) getAuthorizedClient(w http.ResponseWriter, r *http.Request, clientID string) (*models.Client, bool) {
	return h.authorizeClient(w, r, clientID, true)
}

func (h *ClientsHandler) authorizeClient(w http.ResponseWriter, r *http.Request, clientID string, logMiss bool) (*models.Client, bool) {
	client, err := h.clients.Get(clientID)
	if err != nil {
		log.Printf("[clients] get error clientId=%q account=%q master=%v err=%v",
			clientID, auth.GetAccountID(r), auth.IsMaster(r), err)
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return nil, false
	}
	if client == nil {
		if logMiss {
			log.Printf("[clients] not found clientId=%q account=%q master=%v reason=missing",
				clientID, auth.GetAccountID(r), auth.IsMaster(r))
		}
		writeJSONError(w, "client not found", http.StatusNotFound)
		return nil, false
	}
	if !h.canAccessClient(r, clientID, client.UserID) {
		if logMiss {
			log.Printf("[clients] not found clientId=%q ownerProfile=%q account=%q master=%v reason=forbidden",
				clientID, client.UserID, auth.GetAccountID(r), auth.IsMaster(r))
		}
		writeJSONError(w, "client not found", http.StatusNotFound)
		return nil, false
	}
	return client, true
}

// canAccessClient is true if the caller can access the last-active profile or any
// person associated with the device.
func (h *ClientsHandler) canAccessClient(r *http.Request, clientID, lastUserID string) bool {
	if h.canAccessProfile(r, lastUserID) {
		return true
	}
	for _, profileID := range h.clients.ProfileIDs(clientID) {
		if h.canAccessProfile(r, profileID) {
			return true
		}
	}
	return false
}

// resolveSettingsUserID picks the profile for person×device settings.
// Prefer explicit ?userId=; otherwise last-active client.UserID (backward compatible).
func (h *ClientsHandler) resolveSettingsUserID(r *http.Request, client *models.Client) (string, error) {
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	if userID == "" {
		userID = strings.TrimSpace(client.UserID)
	}
	if userID == "" {
		return "", errors.New("userId is required")
	}
	if !h.canAccessProfile(r, userID) {
		return "", errSettingsProfileForbidden
	}
	// Allow settings for a profile that has used the device, or last-active, or
	// any profile the caller can access when creating first overrides before
	// re-registration (admin configuring under a person).
	if !h.clients.HasProfile(client.ID, userID) && client.UserID != userID {
		// Still allow if the caller is configuring settings under a person they own;
		// Register will create the association on next use. Admin UI lists devices
		// only under associated people, so this is mainly for API convenience.
	}
	return userID, nil
}

var errSettingsProfileForbidden = errors.New("user not found")

// ClientRegistrationRequest is the request body for registering a client
type ClientRegistrationRequest struct {
	ID         string `json:"id"`
	UserID     string `json:"userId"`
	DeviceName string `json:"deviceName"`
	Nickname   string `json:"nickname"`
	DeviceType string `json:"deviceType"`
	OS         string `json:"os"`
	AppVersion string `json:"appVersion"`
}

// Register handles POST /api/clients/register
// Registers or updates a client device
func (h *ClientsHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req ClientRegistrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	accountID := auth.GetAccountID(r)
	isMaster := auth.IsMaster(r)
	nicknameSet := strings.TrimSpace(req.Nickname) != ""

	if req.ID == "" {
		log.Printf("[clients] register rejected clientId=<empty> userId=%q account=%q master=%v reason=missing_client_id",
			req.UserID, accountID, isMaster)
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}
	if !h.canAccessProfile(r, req.UserID) {
		log.Printf("[clients] register rejected clientId=%q userId=%q account=%q master=%v reason=profile_forbidden nicknameSet=%v",
			req.ID, req.UserID, accountID, isMaster, nicknameSet)
		writeJSONError(w, "user not found", http.StatusNotFound)
		return
	}

	// Snapshot prior last-active profile for logging. Client IDs are device-bound;
	// Register records a person×device association without removing other people
	// who have used the device. Last-active UserID is updated when userId is set.
	prevUserID := ""
	existed := false
	alreadyLinked := false
	if existing, err := h.clients.Get(req.ID); err != nil {
		log.Printf("[clients] register get-existing failed clientId=%q account=%q master=%v err=%v",
			req.ID, accountID, isMaster, err)
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	} else if existing != nil {
		existed = true
		prevUserID = existing.UserID
		alreadyLinked = h.clients.HasProfile(req.ID, req.UserID)
	}

	client, err := h.clients.Register(req.ID, req.UserID, req.DeviceType, req.OS, req.AppVersion, req.DeviceName, req.Nickname)
	if err != nil {
		if errors.Is(err, clients.ErrUserNotFound) {
			log.Printf("[clients] register failed clientId=%q userId=%q account=%q master=%v reason=user_not_found nicknameSet=%v",
				req.ID, req.UserID, accountID, isMaster, nicknameSet)
			writeJSONError(w, "user not found: "+req.UserID, http.StatusBadRequest)
			return
		}
		log.Printf("[clients] register failed clientId=%q userId=%q prevUserId=%q account=%q master=%v nicknameSet=%v err=%v",
			req.ID, req.UserID, prevUserID, accountID, isMaster, nicknameSet, err)
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	action := "created"
	if existed {
		if !alreadyLinked && req.UserID != "" {
			action = "linked"
		} else {
			action = "updated"
		}
	}
	log.Printf("[clients] register ok action=%s clientId=%q userId=%q prevUserId=%q account=%q master=%v deviceType=%q os=%q nicknameSet=%v nickname=%q",
		action, client.ID, client.UserID, prevUserID, accountID, isMaster, client.DeviceType, client.OS, nicknameSet, client.Nickname)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"client": client,
	})
}

// ClientWithOverrides extends Client with hasOverrides flag for UI
type ClientWithOverrides struct {
	models.Client
	HasOverrides bool `json:"hasOverrides"`
}

// List handles GET /api/clients
// Returns all clients (master only) or clients for a specific user if userId query param is provided
func (h *ClientsHandler) List(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("userId")

	var clientList []models.Client
	if userID != "" {
		if !h.canAccessProfile(r, userID) {
			writeJSONError(w, "user not found", http.StatusNotFound)
			return
		}
		clientList = h.clients.ListByUser(userID)
	} else {
		for _, client := range h.clients.List() {
			if h.canAccessProfile(r, client.UserID) {
				clientList = append(clientList, client)
			}
		}
	}

	// Enrich with per-(device, person) override information
	result := make([]ClientWithOverrides, len(clientList))
	for i, c := range clientList {
		hasOverrides := false
		if c.UserID != "" {
			if settings, err := h.settings.Get(c.ID, c.UserID); err == nil && settings != nil {
				hasOverrides = !settings.IsEmpty()
			}
		}
		result[i] = ClientWithOverrides{
			Client:       c,
			HasOverrides: hasOverrides,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// Get handles GET /api/clients/{clientID}
func (h *ClientsHandler) Get(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}

	client, ok := h.getAuthorizedClient(w, r, clientID)
	if !ok {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(client)
}

// ClientUpdateRequest is the request body for updating a client
type ClientUpdateRequest struct {
	Name          *string `json:"name,omitempty"`
	Nickname      *string `json:"nickname,omitempty"`
	FilterEnabled *bool   `json:"filterEnabled,omitempty"`
}

// Update handles PUT /api/clients/{clientID}
// Updates client properties (name, filterEnabled)
func (h *ClientsHandler) Update(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}

	var req ClientUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Get current client
	client, ok := h.getAuthorizedClient(w, r, clientID)
	if !ok {
		return
	}

	// Apply updates
	if req.Name != nil {
		updated, err := h.clients.Rename(clientID, *req.Name)
		if err != nil {
			writeJSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		client = &updated
	}

	if req.Nickname != nil {
		updated, err := h.clients.SetNickname(clientID, *req.Nickname)
		if err != nil {
			log.Printf("[clients] set nickname failed clientId=%q userId=%q account=%q master=%v err=%v",
				clientID, client.UserID, auth.GetAccountID(r), auth.IsMaster(r), err)
			writeJSONError(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Printf("[clients] set nickname ok clientId=%q userId=%q account=%q master=%v nickname=%q",
			clientID, updated.UserID, auth.GetAccountID(r), auth.IsMaster(r), updated.Nickname)
		client = &updated
	}

	if req.FilterEnabled != nil {
		updated, err := h.clients.SetFilterEnabled(clientID, *req.FilterEnabled)
		if err != nil {
			writeJSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		client = &updated
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(client)
}

// Delete handles DELETE /api/clients/{clientID}
// With ?userId=, removes only that person×device association (and their settings).
// Without userId, removes the entire device and all person×device settings.
func (h *ClientsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}
	if _, ok := h.getAuthorizedClient(w, r, clientID); !ok {
		return
	}

	profileID := strings.TrimSpace(r.URL.Query().Get("userId"))
	if profileID != "" {
		if !h.canAccessProfile(r, profileID) {
			writeJSONError(w, "user not found", http.StatusNotFound)
			return
		}
		if err := h.settings.Delete(clientID, profileID); err != nil {
			writeJSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		deletedDevice, err := h.clients.UnassignProfile(clientID, profileID)
		if err != nil {
			if errors.Is(err, clients.ErrClientNotFound) {
				writeJSONError(w, "client not found", http.StatusNotFound)
				return
			}
			if errors.Is(err, clients.ErrProfileNotLinked) {
				writeJSONError(w, "device is not associated with that person", http.StatusNotFound)
				return
			}
			writeJSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if deletedDevice {
			_ = h.settings.DeleteByClient(clientID)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := h.settings.DeleteByClient(clientID); err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := h.clients.Delete(clientID); err != nil {
		if errors.Is(err, clients.ErrClientNotFound) {
			writeJSONError(w, "client not found", http.StatusNotFound)
			return
		}
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetSettings handles GET /api/clients/{clientID}/settings?userId=
func (h *ClientsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}

	client, ok := h.getAuthorizedClient(w, r, clientID)
	if !ok {
		return
	}
	userID, err := h.resolveSettingsUserID(r, client)
	if err != nil {
		if errors.Is(err, errSettingsProfileForbidden) {
			writeJSONError(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}

	settings, err := h.settings.Get(clientID, userID)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Return empty settings if none configured
	if settings == nil {
		settings = &models.ClientFilterSettings{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
}

// UpdateSettings handles PUT /api/clients/{clientID}/settings?userId=
func (h *ClientsHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}

	client, ok := h.getAuthorizedClient(w, r, clientID)
	if !ok {
		return
	}
	userID, err := h.resolveSettingsUserID(r, client)
	if err != nil {
		if errors.Is(err, errSettingsProfileForbidden) {
			writeJSONError(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}

	var settings models.ClientFilterSettings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := validateClientFilterTerms("filtering", &settings); err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if settings.AdaptivePlayback != nil && (settings.AdaptivePlayback.MeasuredMbps != nil || settings.AdaptivePlayback.MeasuredAt != nil) {
		log.Printf("[adaptive] stripping persisted throughput from client settings clientId=%q userId=%q", clientID, userID)
		settings.AdaptivePlayback.MeasuredMbps = nil
		settings.AdaptivePlayback.MeasuredAt = nil
	}
	previous, previousErr := h.settings.Get(clientID, userID)
	if previousErr != nil {
		// Without the stored settings the device's preset choices would be
		// lost.
		writeJSONError(w, previousErr.Error(), http.StatusInternalServerError)
		return
	}
	keepClientQualityPresets(&settings, previous)

	if err := h.settings.Update(clientID, userID, settings); err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if settings.AdaptivePlayback != nil || (previous != nil && previous.AdaptivePlayback != nil) {
		var previousAdaptive *models.AdaptivePlaybackSettings
		if previous != nil {
			previousAdaptive = previous.AdaptivePlayback
		}
		log.Printf("[adaptive] client measurement update clientId=%q userId=%q previousMbps=%v previousMeasuredAt=%v nextMbps=%v nextMeasuredAt=%v previousReadError=%v",
			clientID,
			userID,
			adaptiveFloatLogValue(previousAdaptive, func(a *models.AdaptivePlaybackSettings) *float64 { return a.MeasuredMbps }),
			adaptiveInt64LogValue(previousAdaptive, func(a *models.AdaptivePlaybackSettings) *int64 { return a.MeasuredAt }),
			adaptiveFloatLogValue(settings.AdaptivePlayback, func(a *models.AdaptivePlaybackSettings) *float64 { return a.MeasuredMbps }),
			adaptiveInt64LogValue(settings.AdaptivePlayback, func(a *models.AdaptivePlaybackSettings) *int64 { return a.MeasuredAt }),
			previousErr)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
}

func adaptiveFloatLogValue(settings *models.AdaptivePlaybackSettings, field func(*models.AdaptivePlaybackSettings) *float64) any {
	if settings == nil || field(settings) == nil {
		return nil
	}
	return *field(settings)
}

func adaptiveInt64LogValue(settings *models.AdaptivePlaybackSettings, field func(*models.AdaptivePlaybackSettings) *int64) any {
	if settings == nil || field(settings) == nil {
		return nil
	}
	return *field(settings)
}

// PatchFrontendSetting updates one admin-exposed device override without
// replacing adaptive measurements or other device-specific values.
func (h *ClientsHandler) PatchFrontendSetting(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}
	client, ok := h.getAuthorizedClient(w, r, clientID)
	if !ok {
		return
	}
	userID, err := h.resolveSettingsUserID(r, client)
	if err != nil {
		if errors.Is(err, errSettingsProfileForbidden) {
			writeJSONError(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	patch, err := decodeClientFrontendSettingPatch(json.NewDecoder(r.Body), h.configManager)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	path, supported := clientSettingPath(patch.Path)
	if !supported {
		writeJSONError(w, "setting is not supported for device overrides", http.StatusBadRequest)
		return
	}
	current, err := h.settings.Get(clientID, userID)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	raw, err := json.Marshal(current)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	raw, err = patchJSONObject(raw, path, patch.Value, patch.Reset)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	var next models.ClientFilterSettings
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&next); err != nil {
		writeJSONError(w, "setting is not supported for device overrides", http.StatusBadRequest)
		return
	}
	if err := validateClientFilterTerms("filtering", &next); err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.settings.Update(clientID, userID, next); err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(next)
}

// ResetSettings handles DELETE /api/clients/{clientID}/settings?userId=
// Resets person×device settings to inherit from profile/global defaults
func (h *ClientsHandler) ResetSettings(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}

	client, ok := h.getAuthorizedClient(w, r, clientID)
	if !ok {
		return
	}
	userID, err := h.resolveSettingsUserID(r, client)
	if err != nil {
		if errors.Is(err, errSettingsProfileForbidden) {
			writeJSONError(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.settings.Delete(clientID, userID); err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Client settings reset to defaults",
	})
}

// Ping handles POST /api/clients/{clientID}/ping
// Sets a pending ping for the client (called from admin UI to identify a device)
func (h *ClientsHandler) Ping(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}

	// Verify client exists
	client, ok := h.getAuthorizedClient(w, r, clientID)
	if !ok {
		return
	}

	// Set pending ping
	h.pingMu.Lock()
	h.pendingPings[clientID] = pendingPing{timestamp: time.Now()}
	h.pingMu.Unlock()

	log.Printf("[clients] admin ping queued clientId=%q userId=%q account=%q master=%v",
		clientID, client.UserID, auth.GetAccountID(r), auth.IsMaster(r))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"clientId": clientID,
		"message":  "Ping sent to client",
	})
}

// CheckPing handles GET /api/clients/{clientID}/ping
// Checks if there's a pending ping for this client (called by the app)
// Returns and clears the ping if present
func (h *ClientsHandler) CheckPing(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}
	// Quiet miss logs: clients poll this every few seconds while Settings is open.
	if _, ok := h.authorizeClient(w, r, clientID, false); !ok {
		return
	}

	h.pingMu.Lock()
	ping, exists := h.pendingPings[clientID]
	hasPing := exists && time.Since(ping.timestamp) < pingExpiry
	if hasPing {
		delete(h.pendingPings, clientID) // Clear the ping once checked
	}
	h.pingMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ping": hasPing,
	})
}

type SendClientMessageRequest struct {
	Message    string   `json:"message"`
	TargetAll  bool     `json:"targetAll"`
	ProfileIDs []string `json:"profileIds"`
}

type ClientMessageResponse struct {
	ID        string    `json:"id"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
}

// SendMessage handles POST /api/clients/messages.
// It queues a short-lived popup message for all profiles or selected profiles.
func (h *ClientsHandler) SendMessage(w http.ResponseWriter, r *http.Request) {
	var req SendClientMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	message := strings.TrimSpace(req.Message)
	if message == "" {
		writeJSONError(w, "message is required", http.StatusBadRequest)
		return
	}
	if len(message) > clientMessageMaxLength {
		writeJSONError(w, "message is too long", http.StatusBadRequest)
		return
	}

	profileIDs := make(map[string]struct{})
	for _, id := range req.ProfileIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		profileIDs[id] = struct{}{}
	}
	if !req.TargetAll && len(profileIDs) == 0 {
		writeJSONError(w, "select at least one profile or send to all profiles", http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	msg := pendingClientMessage{
		ID:                  now.Format("20060102150405.000000000"),
		Message:             message,
		TargetAll:           req.TargetAll,
		ProfileIDs:          profileIDs,
		TargetProfileCount:  targetProfileCount(req.TargetAll, profileIDs),
		CreatedAt:           now,
		DeliveredProfileIDs: make(map[string]struct{}),
	}

	h.messageMu.Lock()
	h.pruneExpiredMessagesLocked(now)
	h.pendingMessages = append(h.pendingMessages, msg)
	h.messageMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": ClientMessageResponse{
			ID:        msg.ID,
			Message:   msg.Message,
			CreatedAt: msg.CreatedAt,
		},
	})
}

// CheckMessages handles GET /api/clients/{clientID}/messages?profileId=...
// It returns and clears messages that match this client/profile pair.
func (h *ClientsHandler) CheckMessages(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	profileID := strings.TrimSpace(r.URL.Query().Get("profileId"))
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}
	if profileID == "" {
		writeJSONError(w, "profileId is required", http.StatusBadRequest)
		return
	}
	// Quiet miss logs: clients may poll messages frequently.
	if _, ok := h.authorizeClient(w, r, clientID, false); !ok {
		return
	}
	if !h.canAccessProfile(r, profileID) {
		writeJSONError(w, "client not found", http.StatusNotFound)
		return
	}
	// Deliver when this profile is (or becomes) linked to the device. Require either
	// an existing association or last-active match so random profiles cannot drain messages.
	if !h.clients.HasProfile(clientID, profileID) {
		writeJSONError(w, "client not found", http.StatusNotFound)
		return
	}

	now := time.Now().UTC()
	messages := make([]ClientMessageResponse, 0)

	h.messageMu.Lock()
	h.pruneExpiredMessagesLocked(now)
	for i := range h.pendingMessages {
		msg := &h.pendingMessages[i]
		if _, delivered := msg.DeliveredProfileIDs[profileID]; delivered {
			continue
		}
		if !msg.TargetAll {
			if _, ok := msg.ProfileIDs[profileID]; !ok {
				continue
			}
		}
		msg.DeliveredProfileIDs[profileID] = struct{}{}
		messages = append(messages, ClientMessageResponse{
			ID:        msg.ID,
			Message:   msg.Message,
			CreatedAt: msg.CreatedAt,
		})
	}
	h.pruneDeliveredMessagesLocked()
	h.messageMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"messages": messages,
	})
}

func (h *ClientsHandler) pruneExpiredMessagesLocked(now time.Time) {
	if len(h.pendingMessages) == 0 {
		return
	}
	next := h.pendingMessages[:0]
	for _, msg := range h.pendingMessages {
		if now.Sub(msg.CreatedAt) <= clientMessageExpiry {
			next = append(next, msg)
		}
	}
	h.pendingMessages = next
}

func (h *ClientsHandler) pruneDeliveredMessagesLocked() {
	if len(h.pendingMessages) == 0 {
		return
	}
	next := h.pendingMessages[:0]
	for _, msg := range h.pendingMessages {
		if msg.TargetProfileCount > 0 && len(msg.DeliveredProfileIDs) >= msg.TargetProfileCount {
			continue
		}
		next = append(next, msg)
	}
	h.pendingMessages = next
}

func targetProfileCount(targetAll bool, profileIDs map[string]struct{}) int {
	if targetAll {
		return 0
	}
	return len(profileIDs)
}

// ReassignRequest is the request body for moving a person×device association.
type ReassignRequest struct {
	UserID     string `json:"userId"`
	FromUserID string `json:"fromUserId,omitempty"`
}

// Reassign handles POST /api/clients/{clientID}/reassign
// Moves a device association (and its person×device settings) from one person to another.
func (h *ClientsHandler) Reassign(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}

	var req ReassignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(req.UserID) == "" {
		writeJSONError(w, "userId is required", http.StatusBadRequest)
		return
	}
	existing, ok := h.getAuthorizedClient(w, r, clientID)
	if !ok {
		return
	}
	if !h.canAccessProfile(r, req.UserID) {
		writeJSONError(w, "user not found", http.StatusNotFound)
		return
	}
	fromUserID := strings.TrimSpace(req.FromUserID)
	if fromUserID == "" {
		fromUserID = existing.UserID
	}
	if fromUserID != "" && !h.canAccessProfile(r, fromUserID) {
		writeJSONError(w, "user not found", http.StatusNotFound)
		return
	}

	client, err := h.clients.ReassignUser(clientID, fromUserID, req.UserID)
	if err != nil {
		if errors.Is(err, clients.ErrClientNotFound) {
			writeJSONError(w, "client not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, clients.ErrProfileNotLinked) {
			writeJSONError(w, "device is not associated with that person", http.StatusNotFound)
			return
		}
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if fromUserID != "" {
		if err := h.settings.Move(clientID, fromUserID, req.UserID); err != nil {
			log.Printf("[clients] reassign settings move failed clientId=%q from=%q to=%q err=%v",
				clientID, fromUserID, req.UserID, err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(client)
}

// Options handles OPTIONS requests for CORS
func (h *ClientsHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// writeJSONError writes a JSON error response
func writeJSONError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
