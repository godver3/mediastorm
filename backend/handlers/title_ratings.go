package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"novastream/internal/auth"
	"novastream/internal/profileaccess"
	"novastream/models"
	"novastream/services/kids"
	"novastream/services/titleratings"
)

type titleRatingService interface {
	Observe(string, string, []models.Title)
	Resolve(string, string, string, string) (models.Title, bool)
	List(context.Context, string) ([]models.TitleRating, error)
	Put(context.Context, string, models.Title, string) error
}

// A preference never accepts client-supplied metadata or permission claims.
func (h *MetadataHandler) TitleRating(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	vars := mux.Vars(r)
	profile, kind, id := vars["userID"], vars["mediaType"], vars["titleID"]
	client := requestClientID(r)
	if auth.IsShareLinkRequest(r) {
		http.Error(w, "a profile session is required", 403)
		return
	}
	if session, ok := auth.GetSession(r); ok && session.Scope == models.SessionScopeStream {
		http.Error(w, "a profile session is required", 403)
		return
	}
	if profile == "" || client == "" || len(client) > 128 || strings.TrimSpace(id) == "" || len(id) > 128 || kind != "movie" && kind != "series" {
		http.Error(w, "valid profile, client and title coordinates are required", 400)
		return
	}
	if !profileaccess.CheckResource(w, r, profile, "") {
		return
	}
	if h.UsersService == nil {
		http.NotFound(w, r)
		return
	}
	user, ok := h.UsersService.Get(profile)
	if !ok || !auth.IsMaster(r) && (auth.GetAccountID(r) == "" || user.AccountID != auth.GetAccountID(r)) {
		http.NotFound(w, r)
		return
	}
	if h.TitleRatings == nil {
		http.Error(w, "personal ratings unavailable", 501)
		return
	}
	title, ok := h.TitleRatings.Resolve(profile, client, kind, id)
	if !ok {
		http.Error(w, "reload this title before rating it", 409)
		return
	}
	if !h.ratingTitleAllowed(profile, title) {
		http.NotFound(w, r)
		return
	}
	value := "none"
	switch r.Method {
	case http.MethodPut:
		var body struct {
			Rating string `json:"rating"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&body) != nil {
			http.Error(w, "invalid personal rating", 400)
			return
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF || !models.ValidTitleRating(body.Rating) {
			http.Error(w, "invalid personal rating", 400)
			return
		}
		value = body.Rating
		if err := h.TitleRatings.Put(r.Context(), profile, title, value); err != nil {
			http.Error(w, "could not save personal rating", 500)
			return
		}
	case http.MethodGet:
		items, err := h.TitleRatings.List(r.Context(), profile)
		if err != nil {
			http.Error(w, "could not load personal rating", 500)
			return
		}
		value = titleratings.Match(items, title)
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", 405)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"rating": value})
}

// Stored adult-profile preferences cannot become seeds or explanations after
// that profile is converted to a restricted child profile.
func (h *MetadataHandler) ratingTitleAllowed(profile string, title models.Title) bool {
	if h.UsersService == nil {
		return false
	}
	user, ok := h.UsersService.Get(profile)
	if !ok {
		return false
	}
	if !user.IsKidsProfile {
		return true
	}
	if user.KidsMode != "rating" || title.Adult {
		return false
	}
	movie, tv, limited := h.kidsRatingLimits(profile)
	limit := tv
	if title.MediaType == "movie" {
		limit = movie
	}
	return limited && limit != "" && kids.IsRatingAllowed(title.Certification, limit, title.MediaType)
}

func (h *MetadataHandler) ratingRecommendationSeeds(ctx context.Context, profile string) ([]personalizedSeed, error) {
	if h.TitleRatings == nil {
		return nil, nil
	}
	items, err := h.TitleRatings.List(ctx, profile)
	if err != nil {
		return nil, err
	}
	seeds := []personalizedSeed{}
	for _, item := range items {
		if !h.ratingTitleAllowed(profile, item.Title) {
			continue
		}
		strength := 1.25
		if item.Value == "love" {
			strength = 1.7
		}
		if item.Value == "dislike" {
			strength = -1
		}
		tmdb := extractTMDBID(item.Title.MediaType, item.Title.ID, map[string]string{"tmdb": strconv.FormatInt(item.Title.TMDBID, 10)})
		keys := []string{}
		for _, alias := range item.Aliases {
			parts := strings.SplitN(alias, ":", 3)
			if len(parts) != 3 || parts[0] != item.Title.MediaType {
				continue
			}
			value := parts[2]
			if parts[1] == "id" {
				value = normalizePersonalizedID(value)
			} else {
				value = normalizeExternalID(value)
			}
			keys = append(keys, parts[0]+":"+parts[1]+":"+value)
			if tmdb == 0 && parts[1] == "tmdb" {
				parsed, _ := strconv.ParseInt(value, 10, 64)
				if parsed > 0 {
					tmdb = parsed
				}
			}
		}
		seeds = append(seeds, personalizedSeed{MediaType: item.Title.MediaType, TMDBID: tmdb, Title: item.Title.Name, Activity: item.UpdatedAt, Strength: strength, Source: "rating_" + item.Value, Identity: &item.Title, IdentityKeys: keys})
	}
	return seeds, nil
}
