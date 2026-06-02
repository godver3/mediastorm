package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/remoteaccess"
)

type RemoteAccessHandler struct {
	service *remoteaccess.Service
}

func NewRemoteAccessHandler(service *remoteaccess.Service) *RemoteAccessHandler {
	return &RemoteAccessHandler{service: service}
}

type createRemoteAccessInviteRequest struct {
	PeerName       string `json:"peerName"`
	ExpiresInHours int    `json:"expiresInHours"`
}

type claimRemoteAccessInviteRequest struct {
	Token  string `json:"token"`
	PeerID string `json:"peerId"`
}

type resolveRemoteAccessInviteRequest struct {
	Token string `json:"token"`
}

type remoteAccessInviteResponse struct {
	ID             string     `json:"id"`
	Token          string     `json:"token,omitempty"`
	ConnectionCode string     `json:"connectionCode,omitempty"`
	IrohInvite     string     `json:"irohInvite,omitempty"`
	PeerName       string     `json:"peerName,omitempty"`
	ExpiresAt      time.Time  `json:"expiresAt"`
	UsedAt         *time.Time `json:"usedAt,omitempty"`
	UsedByPeerID   string     `json:"usedByPeerId,omitempty"`
	RevokedAt      *time.Time `json:"revokedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
}

func (h *RemoteAccessHandler) Status(w http.ResponseWriter, r *http.Request) {
	h.writeJSON(w, h.service.Status(r.Context()))
}

func (h *RemoteAccessHandler) CreateInvite(w http.ResponseWriter, r *http.Request) {
	var req createRemoteAccessInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.ExpiresInHours < 0 {
		writeJSONError(w, "expiresInHours must be zero or greater", http.StatusBadRequest)
		return
	}
	inv, err := h.service.CreateInvite(r.Context(), auth.GetAccountID(r), remoteaccess.CreateInviteRequest{
		PeerName:  req.PeerName,
		ExpiresIn: time.Duration(req.ExpiresInHours) * time.Hour,
	})
	if err != nil {
		log.Printf("[remote-access] create invite failed peerName=%q: %v", req.PeerName, err)
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	h.writeJSON(w, toRemoteAccessInviteResponse(inv))
}

func (h *RemoteAccessHandler) ListInvites(w http.ResponseWriter, r *http.Request) {
	invites, err := h.service.ListInvites(r.Context())
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	result := make([]remoteAccessInviteResponse, 0, len(invites))
	for _, inv := range invites {
		result = append(result, toRemoteAccessInviteResponse(inv))
	}
	h.writeJSON(w, result)
}

func (h *RemoteAccessHandler) RevokeInvite(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(mux.Vars(r)["inviteID"])
	if err := h.service.RevokeInvite(r.Context(), id); err != nil {
		if errors.Is(err, remoteaccess.ErrInviteNotFound) {
			writeJSONError(w, "remote access invite not found", http.StatusNotFound)
			return
		}
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *RemoteAccessHandler) ResolveInvite(w http.ResponseWriter, r *http.Request) {
	var req resolveRemoteAccessInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	inv, err := h.service.ResolveInvite(r.Context(), req.Token)
	if err != nil {
		status := remoteAccessErrorStatus(err)
		writeJSONError(w, err.Error(), status)
		return
	}
	h.writeJSON(w, map[string]any{
		"id":             inv.ID,
		"connectionCode": inv.ConnectionCode,
		"irohInvite":     inv.IrohInvite,
		"expiresAt":      inv.ExpiresAt,
	})
}

func (h *RemoteAccessHandler) ResolveClaimedInvite(w http.ResponseWriter, r *http.Request) {
	peerID := strings.TrimSpace(r.URL.Query().Get("peerId"))
	if peerID == "" {
		peerID = strings.TrimSpace(r.Header.Get("X-Client-ID"))
	}
	inv, err := h.service.ResolveClaimedInviteForPeer(r.Context(), peerID)
	if err != nil {
		writeJSONError(w, err.Error(), remoteAccessErrorStatus(err))
		return
	}
	h.writeJSON(w, map[string]any{
		"id":             inv.ID,
		"connectionCode": inv.ConnectionCode,
		"irohInvite":     inv.IrohInvite,
		"usedAt":         inv.UsedAt,
		"usedByPeerId":   inv.UsedByPeerID,
	})
}

func (h *RemoteAccessHandler) ClaimInvite(w http.ResponseWriter, r *http.Request) {
	var req claimRemoteAccessInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	inv, err := h.service.ClaimInvite(r.Context(), req.Token, req.PeerID)
	if err != nil {
		writeJSONError(w, err.Error(), remoteAccessErrorStatus(err))
		return
	}
	h.writeJSON(w, map[string]any{
		"id":           inv.ID,
		"peerName":     inv.PeerName,
		"usedAt":       inv.UsedAt,
		"usedByPeerId": inv.UsedByPeerID,
	})
}

func (h *RemoteAccessHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-PIN")
	w.Header().Set("Access-Control-Max-Age", strconv.Itoa(86400))
	w.WriteHeader(http.StatusOK)
}

func (h *RemoteAccessHandler) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func toRemoteAccessInviteResponse(inv models.RemoteAccessInvite) remoteAccessInviteResponse {
	return remoteAccessInviteResponse{
		ID:             inv.ID,
		Token:          inv.Token,
		ConnectionCode: inv.ConnectionCode,
		IrohInvite:     inv.IrohInvite,
		PeerName:       inv.PeerName,
		ExpiresAt:      inv.ExpiresAt,
		UsedAt:         inv.UsedAt,
		UsedByPeerID:   inv.UsedByPeerID,
		RevokedAt:      inv.RevokedAt,
		CreatedAt:      inv.CreatedAt,
	}
}

func remoteAccessErrorStatus(err error) int {
	switch {
	case errors.Is(err, remoteaccess.ErrInviteNotFound):
		return http.StatusNotFound
	case errors.Is(err, remoteaccess.ErrInviteExpired), errors.Is(err, remoteaccess.ErrInviteUsed), errors.Is(err, remoteaccess.ErrInviteRevoked), errors.Is(err, remoteaccess.ErrInvalidToken):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
