package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"novastream/models"
	"novastream/services/client_settings"
	"novastream/services/clients"

	"github.com/gorilla/mux"
)

type clientsService interface {
	Register(id, userID, deviceType, os, appVersion string) (models.Client, error)
	Get(id string) (*models.Client, error)
	List() []models.Client
	ListByUser(userID string) []models.Client
	Rename(id, name string) (models.Client, error)
	SetFilterEnabled(id string, enabled bool) (models.Client, error)
	UpdateLastSeen(id string) error
	Delete(id string) error
}

type clientSettingsService interface {
	Get(clientID string) (*models.ClientFilterSettings, error)
	Update(clientID string, settings models.ClientFilterSettings) error
	Delete(clientID string) error
}

var _ clientsService = (*clients.Service)(nil)
var _ clientSettingsService = (*client_settings.Service)(nil)

// pendingPing stores the timestamp when a ping was requested for a client
type pendingPing struct {
	timestamp time.Time
}

type ClientsHandler struct {
	clients      clientsService
	settings     clientSettingsService
	pendingPings map[string]pendingPing
	pingMu       sync.RWMutex
}

const pingExpiry = 30 * time.Second // Pings expire after 30 seconds

func NewClientsHandler(clientsSvc clientsService, settingsSvc clientSettingsService) *ClientsHandler {
	return &ClientsHandler{
		clients:      clientsSvc,
		settings:     settingsSvc,
		pendingPings: make(map[string]pendingPing),
	}
}

// ClientRegistrationRequest is the request body for registering a client
type ClientRegistrationRequest struct {
	ID         string `json:"id"`
	UserID     string `json:"userId"`
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

	if req.ID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}

	client, err := h.clients.Register(req.ID, req.UserID, req.DeviceType, req.OS, req.AppVersion)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"client": client,
	})
}

// List handles GET /api/clients
// Returns all clients (master only) or clients for a specific user if userId query param is provided
func (h *ClientsHandler) List(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("userId")

	var result []models.Client
	if userID != "" {
		result = h.clients.ListByUser(userID)
	} else {
		result = h.clients.List()
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

	client, err := h.clients.Get(clientID)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if client == nil {
		writeJSONError(w, "client not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(client)
}

// ClientUpdateRequest is the request body for updating a client
type ClientUpdateRequest struct {
	Name          *string `json:"name,omitempty"`
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
	client, err := h.clients.Get(clientID)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if client == nil {
		writeJSONError(w, "client not found", http.StatusNotFound)
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
func (h *ClientsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}

	// Also delete client settings
	if err := h.settings.Delete(clientID); err != nil {
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

// GetSettings handles GET /api/clients/{clientID}/settings
func (h *ClientsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}

	// Verify client exists
	client, err := h.clients.Get(clientID)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if client == nil {
		writeJSONError(w, "client not found", http.StatusNotFound)
		return
	}

	settings, err := h.settings.Get(clientID)
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

// UpdateSettings handles PUT /api/clients/{clientID}/settings
func (h *ClientsHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}

	// Verify client exists
	client, err := h.clients.Get(clientID)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if client == nil {
		writeJSONError(w, "client not found", http.StatusNotFound)
		return
	}

	var settings models.ClientFilterSettings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := h.settings.Update(clientID, settings); err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
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
	client, err := h.clients.Get(clientID)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if client == nil {
		writeJSONError(w, "client not found", http.StatusNotFound)
		return
	}

	// Set pending ping
	h.pingMu.Lock()
	h.pendingPings[clientID] = pendingPing{timestamp: time.Now()}
	h.pingMu.Unlock()

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
