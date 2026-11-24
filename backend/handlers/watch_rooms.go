package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/watchrooms"
)

type WatchRoomsHandler struct{ service *watchrooms.Service }

func NewWatchRoomsHandler(service *watchrooms.Service) *WatchRoomsHandler {
	return &WatchRoomsHandler{service: service}
}

func (h *WatchRoomsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var in models.WatchRoomCreate
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	room, err := h.service.Create(r.Context(), auth.GetAccountID(r), mux.Vars(r)["userID"], in)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeWatchRoomJSON(w, http.StatusCreated, room)
}

func (h *WatchRoomsHandler) InviteAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	invite, err := h.service.InviteAccount(r.Context(), auth.GetAccountID(r), mux.Vars(r)["userID"], mux.Vars(r)["roomID"], body.Username)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeWatchRoomJSON(w, http.StatusCreated, invite)
}

func (h *WatchRoomsHandler) AccountInvitations(w http.ResponseWriter, r *http.Request) {
	invites, err := h.service.AccountInvitations(r.Context(), auth.GetAccountID(r))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeWatchRoomJSON(w, http.StatusOK, invites)
}

func (h *WatchRoomsHandler) RoomAccountInvitations(w http.ResponseWriter, r *http.Request) {
	invites, err := h.service.RoomAccountInvitations(r.Context(), auth.GetAccountID(r), mux.Vars(r)["userID"], mux.Vars(r)["roomID"])
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeWatchRoomJSON(w, http.StatusOK, invites)
}

func (h *WatchRoomsHandler) AcceptAccountInvitation(w http.ResponseWriter, r *http.Request) {
	room, err := h.service.AcceptAccountInvitation(r.Context(), auth.GetAccountID(r), mux.Vars(r)["userID"], mux.Vars(r)["inviteID"])
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeWatchRoomJSON(w, http.StatusOK, room)
}

func (h *WatchRoomsHandler) DeclineAccountInvitation(w http.ResponseWriter, r *http.Request) {
	if err := h.service.DeclineAccountInvitation(r.Context(), auth.GetAccountID(r), mux.Vars(r)["inviteID"]); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *WatchRoomsHandler) RevokeAccountInvitation(w http.ResponseWriter, r *http.Request) {
	err := h.service.RevokeAccountInvitation(r.Context(), auth.GetAccountID(r), mux.Vars(r)["userID"], mux.Vars(r)["roomID"], mux.Vars(r)["inviteID"])
	if err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *WatchRoomsHandler) Invitations(w http.ResponseWriter, r *http.Request) {
	rooms, err := h.service.Invitations(r.Context(), mux.Vars(r)["userID"])
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeWatchRoomJSON(w, http.StatusOK, rooms)
}

func (h *WatchRoomsHandler) Get(w http.ResponseWriter, r *http.Request) {
	room, err := h.service.Get(r.Context(), mux.Vars(r)["roomID"], mux.Vars(r)["userID"])
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeWatchRoomJSON(w, http.StatusOK, room)
}

func (h *WatchRoomsHandler) Join(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ClientID     string                             `json:"clientId"`
		Capabilities models.WatchRoomClientCapabilities `json:"capabilities"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	room, err := h.service.Join(r.Context(), mux.Vars(r)["roomID"], mux.Vars(r)["userID"], body.ClientID, body.Capabilities)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeWatchRoomJSON(w, http.StatusOK, room)
}

func (h *WatchRoomsHandler) Ready(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ready bool `json:"ready"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	room, err := h.service.SetReady(r.Context(), mux.Vars(r)["roomID"], mux.Vars(r)["userID"], body.Ready)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeWatchRoomJSON(w, http.StatusOK, room)
}

func (h *WatchRoomsHandler) State(w http.ResponseWriter, r *http.Request) {
	var body models.WatchRoomStateUpdate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	room, err := h.service.UpdateState(r.Context(), mux.Vars(r)["roomID"], mux.Vars(r)["userID"], body)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeWatchRoomJSON(w, http.StatusOK, room)
}

func (h *WatchRoomsHandler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ClientID  string `json:"clientId"`
		Buffering bool   `json:"buffering"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := h.service.Heartbeat(r.Context(), mux.Vars(r)["roomID"], mux.Vars(r)["userID"], body.ClientID, body.Buffering); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *WatchRoomsHandler) Leave(w http.ResponseWriter, r *http.Request) {
	if err := h.service.Leave(r.Context(), mux.Vars(r)["roomID"], mux.Vars(r)["userID"]); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *WatchRoomsHandler) End(w http.ResponseWriter, r *http.Request) {
	if err := h.service.End(r.Context(), mux.Vars(r)["roomID"], mux.Vars(r)["userID"]); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *WatchRoomsHandler) Options(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (h *WatchRoomsHandler) writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, watchrooms.ErrNotFound), errors.Is(err, watchrooms.ErrNotInvited):
		status = http.StatusNotFound
	case errors.Is(err, watchrooms.ErrNotMember), errors.Is(err, watchrooms.ErrNotCreator), errors.Is(err, watchrooms.ErrForeignProfile), errors.Is(err, watchrooms.ErrEpisodeHostOnly):
		status = http.StatusForbidden
	case errors.Is(err, watchrooms.ErrInvalidMedia), errors.Is(err, watchrooms.ErrInvalidState), errors.Is(err, watchrooms.ErrIncompatibleClient), errors.Is(err, watchrooms.ErrSameAccount):
		status = http.StatusBadRequest
	case errors.Is(err, watchrooms.ErrAccountNotFound):
		status = http.StatusNotFound
	case errors.Is(err, watchrooms.ErrInviteUnavailable):
		status = http.StatusGone
	case errors.Is(err, watchrooms.ErrAlreadyInvited):
		status = http.StatusConflict
	case errors.Is(err, watchrooms.ErrRevisionConflict):
		status = http.StatusConflict
	case errors.Is(err, watchrooms.ErrRoomEnded):
		status = http.StatusGone
	}
	writeJSONError(w, strings.TrimSpace(err.Error()), status)
}

func writeWatchRoomJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
