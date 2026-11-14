package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"novastream/internal/auth"
	"novastream/internal/profileaccess"
	"novastream/models"
	"novastream/services/kids"
	"novastream/services/sourceavailability"
)

type titleAvailabilityService interface {
	EpisodeHintsEnabled() bool
	ResolveDeliveredTitles(string, string, []sourceavailability.TitleCoordinate) []models.Title
	ReplaceTitleView(string, string, string, int64, []models.Title) (sourceavailability.TitleViewResponse, error)
}
type titleDemandRequest struct {
	Version int64                                `json:"version"`
	Titles  []sourceavailability.TitleCoordinate `json:"titles"`
}

// TitleSourceHints admits only identifiers previously delivered by this server.
// It never accepts query text and never waits for a provider or metadata request.
func (h *MetadataHandler) TitleSourceHints(w http.ResponseWriter, r *http.Request) {
	profile := strings.TrimSpace(mux.Vars(r)["userID"])
	view := mux.Vars(r)["viewID"]
	client := requestClientID(r)
	if auth.IsShareLinkRequest(r) {
		http.Error(w, "a profile session is required", 403)
		return
	}
	if session, ok := auth.GetSession(r); ok && session.Scope == models.SessionScopeStream {
		http.Error(w, "a profile session is required", 403)
		return
	}
	if profile == "" || !episodeViewIDPattern.MatchString(view) || client == "" || len(client) > 128 {
		http.Error(w, "profile, client and view identity are required", 400)
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
	service, ok := h.SourceAvailability.(titleAvailabilityService)
	if !ok {
		http.Error(w, "title source hints unavailable", 501)
		return
	}
	var body titleDemandRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		http.Error(w, "invalid visible title demand", 400)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		http.Error(w, "one JSON object is required", 400)
		return
	}
	if body.Version < 1 || body.Titles == nil || len(body.Titles) > sourceavailability.MaxVisibleTitles {
		http.Error(w, "invalid demand version or title count", 400)
		return
	}
	seen := map[sourceavailability.TitleCoordinate]bool{}
	for _, coord := range body.Titles {
		if coord.ID == "" || len(coord.ID) > 128 || coord.MediaType != "movie" && coord.MediaType != "series" || seen[coord] {
			http.Error(w, "invalid or duplicate title coordinates", 400)
			return
		}
		seen[coord] = true
	}
	if !service.EpisodeHintsEnabled() {
		writeTitleHints(w, sourceavailability.TitleViewResponse{Enabled: false, Version: body.Version, Items: []sourceavailability.TitleHint{}})
		return
	}
	titles := service.ResolveDeliveredTitles(profile, client, body.Titles)
	if len(body.Titles) > 0 && h.UsersService != nil {
		if user, exists := h.UsersService.Get(profile); exists && user.IsKidsProfile {
			movieRating, tvRating, limited := h.kidsRatingLimits(profile)
			if user.KidsMode != "rating" || !limited {
				http.NotFound(w, r)
				return
			}
			for _, title := range titles {
				maxRating := tvRating
				if title.MediaType == "movie" {
					maxRating = movieRating
				}
				if maxRating == "" || !kids.IsRatingAllowed(title.Certification, maxRating, title.MediaType) {
					http.NotFound(w, r)
					return
				}
			}
		}
	}
	if r.Context().Err() != nil {
		return
	}
	response, err := service.ReplaceTitleView(profile, client, view, body.Version, titles)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, sourceavailability.ErrStaleEpisodeView) {
			status = http.StatusConflict
		}
		if errors.Is(err, sourceavailability.ErrEpisodeViewLimit) {
			status = http.StatusTooManyRequests
		}
		http.Error(w, "visible title demand could not be accepted", status)
		return
	}
	// Returning every requested coordinate lets clients distinguish an unknown
	// catalogue projection from a delayed provider check, without inventing absence.
	byID := map[sourceavailability.TitleCoordinate]sourceavailability.TitleHint{}
	for _, item := range response.Items {
		byID[item.TitleCoordinate] = item
	}
	response.Items = []sourceavailability.TitleHint{}
	if response.Enabled {
		for _, coord := range body.Titles {
			item, ok := byID[coord]
			if !ok {
				value := models.SourceAvailability{State: "unknown", Reason: "catalogue_refresh_required"}
				item = sourceavailability.TitleHint{TitleCoordinate: coord, Availability: &value}
			}
			response.Items = append(response.Items, item)
		}
	}
	writeTitleHints(w, response)
}
func writeTitleHints(w http.ResponseWriter, response sourceavailability.TitleViewResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(response)
}
