package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	metadatapkg "novastream/services/metadata"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gorilla/mux"
	"novastream/internal/auth"
	"novastream/internal/profileaccess"
	"novastream/models"
	"novastream/services/kids"
	"novastream/services/tasteonboarding"
)

type tasteOnboardingService interface {
	Get(context.Context, string) (models.TasteOnboardingState, error)
	Save(context.Context, string, string, []models.Title) error
	Skip(context.Context, string) error
	Delete(context.Context, string) error
}
type tasteTitleCoordinate struct {
	TitleID   string `json:"titleId"`
	MediaType string `json:"mediaType"`
}
type tasteOnboardingRequest struct {
	Action string                 `json:"action"`
	Titles []tasteTitleCoordinate `json:"titles,omitempty"`
}
type tasteOnboardingResponse struct {
	Status        string         `json:"status"`
	Source        string         `json:"source"`
	Titles        []models.Title `json:"titles"`
	Available     bool           `json:"available"`
	CanUseHistory bool           `json:"canUseHistory"`
	MediaTypes    []string       `json:"mediaTypes"`
}

// TasteOnboarding uses server-delivered catalogue titles or an explicit choice
// of this profile's existing history. It never imports or writes external data.
func (h *MetadataHandler) TasteOnboarding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	profile := mux.Vars(r)["userID"]
	client := requestClientID(r)
	if auth.IsShareLinkRequest(r) {
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
	if h.UsersService == nil {
		http.NotFound(w, r)
		return
	}
	user, ok := h.UsersService.Get(profile)
	if !ok || !auth.IsMaster(r) && (auth.GetAccountID(r) == "" || user.AccountID != auth.GetAccountID(r)) {
		http.NotFound(w, r)
		return
	}
	if h.Taste == nil || h.TitleRatings == nil {
		http.Error(w, "starting interests unavailable", 501)
		return
	}
	available := h.tasteAvailable(profile)
	switch r.Method {
	case http.MethodPut:
		if !available {
			http.Error(w, "starting interests unavailable for this profile", 403)
			return
		}
		var body tasteOnboardingRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&body) != nil {
			http.Error(w, "invalid starting interests", 400)
			return
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF || len(body.Titles) > tasteonboarding.MaxInterests || body.Action != "save" && len(body.Titles) > 0 {
			http.Error(w, "invalid starting interests", 400)
			return
		}
		switch body.Action {
		case "skip":
			if err := h.Taste.Skip(r.Context(), profile); err != nil {
				http.Error(w, "could not skip taste setup", 500)
				return
			}
		case "save":
			if body.Titles == nil {
				http.Error(w, "a title selection is required", 400)
				return
			}
			titles := []models.Title{}
			for _, coord := range body.Titles {
				if strings.TrimSpace(coord.TitleID) == "" || len(coord.TitleID) > 128 || coord.MediaType != "movie" && coord.MediaType != "series" {
					http.Error(w, "valid title coordinates are required", 400)
					return
				}
				title, exists := h.TitleRatings.Resolve(profile, client, coord.MediaType, coord.TitleID)
				if !exists {
					http.Error(w, "reload the selected titles before saving", 409)
					return
				}
				if !h.ratingTitleAllowed(profile, title) {
					http.NotFound(w, r)
					return
				}
				titles = append(titles, title)
			}
			if err := h.Taste.Save(r.Context(), profile, "manual", titles); err != nil {
				http.Error(w, "could not save starting interests", 500)
				return
			}
		case "history":
			titles, err := h.tasteHistoryTitles(profile)
			if err != nil {
				http.Error(w, "profile history unavailable", 503)
				return
			}
			if len(titles) == 0 {
				http.Error(w, "no usable history is available for this profile", 409)
				return
			}
			if err := h.Taste.Save(r.Context(), profile, "history", titles); err != nil {
				http.Error(w, "could not save starting interests", 500)
				return
			}
		default:
			http.Error(w, "invalid taste setup action", 400)
			return
		}
	case http.MethodDelete:
		// Clearing this independent dataset remains possible after restrictions
		// change; it does not reveal the previously selected title metadata.
		if err := h.Taste.Delete(r.Context(), profile); err != nil {
			http.Error(w, "could not clear starting interests", 500)
			return
		}
	case http.MethodGet:
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		http.Error(w, "method not allowed", 405)
		return
	}
	state, err := h.Taste.Get(r.Context(), profile)
	if err != nil {
		http.Error(w, "could not load starting interests", 500)
		return
	}
	response := tasteOnboardingResponse{Status: state.Status, Source: state.Source, Titles: []models.Title{}, Available: available, MediaTypes: h.tasteMediaTypes(profile)}
	for _, interest := range state.Interests {
		if h.ratingTitleAllowed(profile, interest.Title) {
			response.Titles = append(response.Titles, interest.Title)
		}
	}
	// Reading an editable selection delivers its trusted coordinates again.
	h.TitleRatings.Observe(profile, client, response.Titles)
	if available {
		if titles, err := h.tasteHistoryTitles(profile); err == nil {
			response.CanUseHistory = len(titles) > 0
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func (h *MetadataHandler) tasteAvailable(profile string) bool {
	return len(h.tasteMediaTypes(profile)) > 0
}
func (h *MetadataHandler) tasteMediaTypes(profile string) []string {
	types := []string{}
	if h.UsersService == nil {
		return types
	}
	user, ok := h.UsersService.Get(profile)
	if !ok {
		return types
	}
	if !user.IsKidsProfile {
		return []string{"movie", "series"}
	}
	if user.KidsMode != "rating" {
		return types
	}
	movie, tv, limited := h.kidsRatingLimits(profile)
	if !limited {
		return types
	}
	if kids.GetRatingLevel(movie, "movie") > 0 {
		types = append(types, "movie")
	}
	if kids.GetRatingLevel(tv, "series") > 0 {
		types = append(types, "series")
	}
	return types
}

func (h *MetadataHandler) tasteHistoryTitles(profile string) ([]models.Title, error) {
	titles := []models.Title{}
	if h.HistoryService == nil || h.UsersService == nil {
		return titles, nil
	}
	user, ok := h.UsersService.Get(profile)
	// Child selection uses delivered, certified catalogue titles. There is no
	// automatic metadata lookup or external activity fetch in the setup prompt.
	if !ok || user.IsKidsProfile {
		return titles, nil
	}
	items, err := h.HistoryService.ListWatchHistory(profile)
	if err != nil {
		return nil, err
	}
	items = append([]models.WatchHistoryItem(nil), items...)
	sort.SliceStable(items, func(i, j int) bool {
		a, b := firstNonZeroTime(items[i].WatchedAt, items[i].UpdatedAt), firstNonZeroTime(items[j].WatchedAt, items[j].UpdatedAt)
		if a.Equal(b) {
			return items[i].ItemID < items[j].ItemID
		}
		return a.After(b)
	})
	seen := map[string]bool{}
	for _, item := range items {
		if !item.Watched {
			continue
		}
		if item.MediaType == "episode" && strings.TrimSpace(item.SeriesID) == "" {
			continue
		}
		kind, name, id, ids := personalizedHistoryIdentity(item)
		tmdb := extractTMDBID(kind, id, ids)
		if tmdb <= 0 || kind == "" {
			continue
		}
		if strings.TrimSpace(id) == "" {
			prefix := "movie"
			if kind == "series" {
				prefix = "tv"
			}
			id = "tmdb:" + prefix + ":" + strconv.FormatInt(tmdb, 10)
		}
		title := titleFromSharedShelfItem(id, name, kind, item.Year, ids)
		title.TMDBID = tmdb
		key := personalizedCanonicalTitleKey(title)
		if seen[key] {
			continue
		}
		seen[key] = true
		titles = append(titles, title)
		if len(titles) == tasteonboarding.MaxInterests {
			break
		}
	}
	return titles, nil
}

func (h *MetadataHandler) tasteRecommendationSeeds(ctx context.Context, profile string) ([]personalizedSeed, error) {
	if h.Taste == nil {
		return nil, nil
	}
	state, err := h.Taste.Get(ctx, profile)
	if err != nil {
		return nil, err
	}
	seeds := []personalizedSeed{}
	for _, interest := range state.Interests {
		if !h.ratingTitleAllowed(profile, interest.Title) {
			continue
		}
		seeds = append(seeds, preferenceRecommendationSeed(interest.Title, interest.Aliases, interest.AddedAt, 1.1, "onboarding"))
	}
	return seeds, nil
}

// Candidate discovery is separate from normal search-interest observation.
// Only allowed, certified titles reach a child setup and no Usenet work runs.
func (h *MetadataHandler) TasteCandidates(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	profile := mux.Vars(r)["userID"]
	client := requestClientID(r)
	if auth.IsShareLinkRequest(r) {
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
	if h.UsersService == nil {
		http.NotFound(w, r)
		return
	}
	user, ok := h.UsersService.Get(profile)
	if !ok || !auth.IsMaster(r) && (auth.GetAccountID(r) == "" || user.AccountID != auth.GetAccountID(r)) {
		http.NotFound(w, r)
		return
	}
	if h.Taste == nil || h.TitleRatings == nil {
		http.Error(w, "starting interests unavailable", 501)
		return
	}
	kind, query := r.URL.Query().Get("kind"), strings.TrimSpace(r.URL.Query().Get("q"))
	if kind != "movie" && kind != "series" || !utf8.ValidString(query) || utf8.RuneCountInString(query) > 200 {
		http.Error(w, "invalid starting-title query", 400)
		return
	}
	allowed := false
	for _, value := range h.tasteMediaTypes(profile) {
		allowed = allowed || value == kind
	}
	if !allowed {
		http.Error(w, "starting titles unavailable for this profile", 403)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	service := h.serviceForUser(profile)
	titles := []models.Title{}
	if query != "" {
		hits, err := service.Search(ctx, query, kind)
		if err != nil {
			http.Error(w, "starting titles unavailable", 503)
			return
		}
		for _, hit := range hits {
			titles = append(titles, hit.Title)
		}
	} else {
		var items []models.TrendingItem
		var err error
		if options, ok := service.(trendingOptionsService); ok {
			items, err = options.TrendingWithOptions(ctx, kind, metadatapkg.ShelfLoadOptions{DeferArtwork: true})
		} else {
			items, err = service.Trending(ctx, kind)
		}
		if err != nil {
			http.Error(w, "starting titles unavailable", 503)
			return
		}
		for _, item := range items {
			titles = append(titles, item.Title)
		}
	}
	// Bound enrichment as well as the returned candidate page.
	if len(titles) > 80 {
		titles = titles[:80]
	}
	titles = append([]models.Title(nil), titles...)
	for i := range titles {
		if titles[i].MediaType == "tv" {
			titles[i].MediaType = "series"
		}
	}
	if user.IsKidsProfile {
		service.EnrichTitleCertifications(ctx, titles)
	}
	if ctx.Err() != nil {
		http.Error(w, "starting titles unavailable", 503)
		return
	}
	items := []models.TrendingItem{}
	for _, title := range titles {
		if title.MediaType != kind || title.Adult || title.ID == "" || len(title.ID) > 128 || !h.ratingTitleAllowed(profile, title) {
			continue
		}
		items = append(items, models.TrendingItem{Title: title})
		if len(items) == 24 {
			break
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = h.encodeMetadata(w, r, map[string]interface{}{"items": items})
}
