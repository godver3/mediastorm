package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"novastream/internal/auth"
	"novastream/internal/profileaccess"
	"novastream/models"
	"novastream/services/sourceavailability"
)

type episodeAvailabilityService interface {
	EpisodeHintsEnabled() bool
	ReplaceEpisodeView(string, string, string, int64, models.Title, []models.SeriesEpisode) (sourceavailability.EpisodeViewResponse, error)
}
type visibleEpisode struct {
	ID      string `json:"episodeId"`
	Season  int    `json:"seasonNumber"`
	Episode int    `json:"episodeNumber"`
}
type episodeDemandRequest struct {
	Version   int64                    `json:"version"`
	TitleID   string                   `json:"titleId"`
	Numbering *models.EpisodeNumbering `json:"numbering,omitempty"`
	Episodes  []visibleEpisode         `json:"episodes"`
}

var episodeViewIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// EpisodeSourceHints takes coordinates only. Provider queries and cache scope
// come from current server metadata and profile settings, never client labels.
func (h *MetadataHandler) EpisodeSourceHints(w http.ResponseWriter, r *http.Request) {
	profile := strings.TrimSpace(mux.Vars(r)["userID"])
	viewID := mux.Vars(r)["viewID"]
	client := requestClientID(r)
	if auth.IsShareLinkRequest(r) {
		http.Error(w, "a profile session is required", http.StatusForbidden)
		return
	}
	if session, ok := auth.GetSession(r); ok && session.Scope == models.SessionScopeStream {
		http.Error(w, "a profile session is required", http.StatusForbidden)
		return
	}
	if profile == "" || !episodeViewIDPattern.MatchString(viewID) || client == "" || len(client) > 128 {
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
	service, ok := h.SourceAvailability.(episodeAvailabilityService)
	if !ok {
		http.Error(w, "episode source hints unavailable", http.StatusNotImplemented)
		return
	}
	var body episodeDemandRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		http.Error(w, "invalid visible episode demand", 400)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		http.Error(w, "one JSON object is required", 400)
		return
	}
	if body.Version < 1 || body.Episodes == nil || len(body.Episodes) > sourceavailability.MaxVisibleEpisodes {
		http.Error(w, "invalid demand version or episode count", 400)
		return
	}
	if !service.EpisodeHintsEnabled() {
		writeEpisodeHints(w, sourceavailability.EpisodeViewResponse{Enabled: false, Version: body.Version, Items: []sourceavailability.EpisodeHint{}})
		return
	}
	episodes := []models.SeriesEpisode{}
	title := models.Title{}
	if len(body.Episodes) > 0 {
		if body.TitleID == "" || len(body.TitleID) > 128 || body.Numbering == nil || body.Numbering.SeriesID == "" {
			http.Error(w, "verified series and numbering required", 400)
			return
		}
		metadata := h.serviceForUser(profile)
		if metadata == nil {
			http.Error(w, "metadata unavailable", 503)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		details, err := metadata.SeriesDetails(ctx, models.SeriesDetailsQuery{TitleID: body.TitleID, SeasonType: body.Numbering.Ordering})
		if err != nil || details == nil || ctx.Err() != nil {
			http.Error(w, "series metadata unavailable", http.StatusBadGateway)
			return
		}
		title = details.Title
		title.ID = body.TitleID
		title.MediaType = "series"
		if h.UsersService != nil {
			if user, exists := h.UsersService.Get(profile); exists && user.IsKidsProfile {
				_, _, limited := h.kidsRatingLimits(profile)
				if user.KidsMode != "rating" || !limited || len(h.filterTitlesByKids(ctx, profile, metadata, []models.Title{title})) == 0 {
					http.NotFound(w, r)
					return
				}
			}
		}
		dates := map[string]int{}
		completeDates := len(details.Seasons) > 0
		if title.IsDaily {
			for _, season := range details.Seasons {
				if season.EpisodeCount <= 0 || season.EpisodeCount != len(season.Episodes) {
					completeDates = false
				}
				for _, ep := range season.Episodes {
					if ep.AiredDate == "" {
						completeDates = false
					} else {
						dates[ep.AiredDate]++
					}
				}
			}
		}
		seen := map[string]bool{}
		for _, wanted := range body.Episodes {
			var found *models.SeriesEpisode
			for _, season := range details.Seasons {
				if season.Number != wanted.Season {
					continue
				}
				for _, episode := range season.Episodes {
					if episode.SeasonNumber == wanted.Season && episode.EpisodeNumber == wanted.Episode && episode.ID == wanted.ID && models.SameEpisodeNumbering(episode.Numbering, body.Numbering) {
						copy := episode
						found = &copy
						break
					}
				}
			}
			if found == nil || seen[found.ID] {
				http.Error(w, "unknown or duplicate episode in selected numbering", 400)
				return
			}
			seen[found.ID] = true
			if title.IsDaily {
				found.SourceDateUnique = completeDates && dates[found.AiredDate] == 1
			}
			episodes = append(episodes, *found)
		}
	}
	if r.Context().Err() != nil {
		return
	}
	response, err := service.ReplaceEpisodeView(profile, client, viewID, body.Version, title, episodes)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, sourceavailability.ErrStaleEpisodeView) {
			status = http.StatusConflict
		}
		if errors.Is(err, sourceavailability.ErrEpisodeViewLimit) {
			status = http.StatusTooManyRequests
		}
		http.Error(w, "visible episode demand could not be accepted", status)
		return
	}
	writeEpisodeHints(w, response)
}
func writeEpisodeHints(w http.ResponseWriter, value sourceavailability.EpisodeViewResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}
