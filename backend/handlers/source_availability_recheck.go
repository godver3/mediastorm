package handlers

import (
	"encoding/json"
	"errors"
	"github.com/gorilla/mux"
	"io"
	"net/http"
	"novastream/internal/auth"
	"novastream/internal/profileaccess"
	"novastream/models"
	"novastream/services/kids"
	"novastream/services/sourceavailability"
	"strconv"
	"strings"
)

type sourceRecheckService interface {
	EpisodeHintsEnabled() bool
	ResolveDeliveredTitles(string, string, []sourceavailability.TitleCoordinate) []models.Title
	RequestRecheck(string, string, models.Title, *models.SeriesEpisode) (sourceavailability.RecheckResponse, error)
}
type sourceRecheckRequest struct {
	TitleID   string                   `json:"titleId"`
	MediaType string                   `json:"mediaType"`
	Numbering *models.EpisodeNumbering `json:"numbering,omitempty"`
	Episode   *visibleEpisode          `json:"episode,omitempty"`
}

// RecheckSource handles an explicit action; it accepts no query, release name,
// stream URL, permission flag or budget override from a client.
func (h *MetadataHandler) RecheckSource(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	profile := strings.TrimSpace(mux.Vars(r)["userID"])
	client := requestClientID(r)
	if auth.IsShareLinkRequest(r) {
		http.Error(w, "a profile session is required", 403)
		return
	}
	if session, ok := auth.GetSession(r); ok && session.Scope == models.SessionScopeStream {
		http.Error(w, "a profile session is required", 403)
		return
	}
	if profile == "" || client == "" || len(client) > 128 {
		http.Error(w, "profile and client identity are required", 400)
		return
	}
	if !profileaccess.CheckResource(w, r, profile, "") {
		return
	}
	if h.UsersService != nil {
		if _, ok := h.UsersService.Get(profile); !ok {
			http.NotFound(w, r)
			return
		}
	}
	service, ok := h.SourceAvailability.(sourceRecheckService)
	if !ok {
		http.Error(w, "source recheck unavailable", 501)
		return
	}
	var body sourceRecheckRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		http.Error(w, "invalid source recheck", 400)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		http.Error(w, "one JSON object is required", 400)
		return
	}
	if body.TitleID == "" || len(body.TitleID) > 128 || body.MediaType != "movie" && body.MediaType != "series" || body.Episode != nil && body.MediaType != "series" || body.Episode == nil && body.Numbering != nil {
		http.Error(w, "valid title coordinates are required", 400)
		return
	}
	if !service.EpisodeHintsEnabled() {
		http.Error(w, "source checks are disabled", 409)
		return
	}
	var title models.Title
	var episode *models.SeriesEpisode
	if body.Episode != nil {
		resolved, episodes, status, err := h.resolveSourceEpisodes(r.Context(), profile, episodeDemandRequest{TitleID: body.TitleID, Numbering: body.Numbering, Episodes: []visibleEpisode{*body.Episode}})
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}
		title = resolved
		episode = &episodes[0]
	} else {
		titles := service.ResolveDeliveredTitles(profile, client, []sourceavailability.TitleCoordinate{{ID: body.TitleID, MediaType: body.MediaType}})
		if len(titles) != 1 {
			http.Error(w, "reload the catalogue before rechecking this title", 409)
			return
		}
		title = titles[0]
		if h.UsersService != nil {
			if user, exists := h.UsersService.Get(profile); exists && user.IsKidsProfile {
				movie, tv, limited := h.kidsRatingLimits(profile)
				limit := tv
				if title.MediaType == "movie" {
					limit = movie
				}
				if user.KidsMode != "rating" || !limited || limit == "" || !kids.IsRatingAllowed(title.Certification, limit, title.MediaType) {
					http.NotFound(w, r)
					return
				}
			}
		}
	}
	if r.Context().Err() != nil {
		return
	}
	response, err := service.RequestRecheck(profile, client, title, episode)
	if err != nil {
		status := 400
		if errors.Is(err, sourceavailability.ErrRecheckDisabled) {
			status = 409
		}
		if errors.Is(err, sourceavailability.ErrRecheckCooldown) || errors.Is(err, sourceavailability.ErrRecheckCapacity) {
			status = 429
			w.Header().Set("Retry-After", strconv.Itoa(max(1, response.RetryAfterSeconds)))
		}
		message := "source recheck could not be accepted"
		if errors.Is(err, sourceavailability.ErrRecheckDisabled) || errors.Is(err, sourceavailability.ErrRecheckCooldown) || errors.Is(err, sourceavailability.ErrRecheckCapacity) {
			message = err.Error()
		}
		http.Error(w, message, status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(response)
}
