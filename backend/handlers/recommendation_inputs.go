package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/gorilla/mux"
	"novastream/config"
	"novastream/internal/auth"
	"novastream/models"
)

type interestService interface {
	Observe(string, models.Title)
	List(string) []models.RecommendationInterest
	Clear(string) error
}
type recommendationLists interface {
	ListLists(string) ([]models.CustomList, error)
	ListItems(string, string) ([]models.WatchlistItem, error)
}

func (h *MetadataHandler) recommendationSettings(profile string) config.RecommendationSettings {
	var settings config.RecommendationSettings
	if h.CfgManager != nil {
		cfg, err := h.CfgManager.Load()
		if err != nil {
			return settings
		}
		settings = cfg.Recommendations
	}
	if h.UserSettings != nil {
		prefs, err := h.UserSettings.Get(profile)
		if err != nil {
			return config.RecommendationSettings{}
		}
		if prefs != nil {
			settings = settings.Merge(prefs.Recommendations)
		}
	}
	return settings
}

// Only an unambiguous, exact movie/series title match becomes a signal. The
// user's free-text query and the other search results are never persisted.
func (h *MetadataHandler) observeSearchInterest(profile, query string, results []models.SearchResult) {
	if profile == "" || h.Interests == nil || !h.recommendationSettings(profile).SearchInterestsEnabled() {
		return
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return
	}
	var match *models.Title
	for _, result := range results {
		title := result.Title
		if title.MediaType != "movie" && title.MediaType != "series" {
			continue
		}
		if query != strings.ToLower(strings.TrimSpace(title.Name)) && query != strings.ToLower(strings.TrimSpace(title.OriginalName)) {
			continue
		}
		if title.TMDBID <= 0 {
			title.TMDBID = extractTMDBID(title.MediaType, title.ID, nil)
		}
		if title.TMDBID <= 0 {
			continue
		}
		if match != nil {
			return
		}
		copy := title
		match = &copy
	}
	if match != nil {
		h.Interests.Observe(profile, *match)
	}
}

func (h *MetadataHandler) additionalRecommendationSeeds(profile string) []personalizedSeed {
	settings := h.recommendationSettings(profile)
	seeds := []personalizedSeed{}
	add := func(items []models.WatchlistItem, source string) {
		if len(items) > 100 {
			items = items[:100]
		}
		for _, item := range items {
			kind := normalizePersonalizedMediaType(item.MediaType)
			if kind == "" {
				continue
			}
			id := extractTMDBID(kind, item.ID, item.ExternalIDs)
			if id <= 0 {
				continue
			}
			seeds = append(seeds, personalizedSeed{MediaType: kind, TMDBID: id, Title: item.Name, Activity: item.AddedAt, Strength: 0.85, Source: source})
		}
	}
	if settings.WatchlistEnabled() && h.WatchlistService != nil {
		if items, err := h.WatchlistService.List(profile); err == nil {
			add(items, "watchlist")
		}
	}
	if settings.CustomListsEnabled() && h.CustomLists != nil {
		if lists, err := h.CustomLists.ListLists(profile); err == nil {
			sort.SliceStable(lists, func(i, j int) bool { return lists[i].UpdatedAt.After(lists[j].UpdatedAt) })
			if len(lists) > 10 {
				lists = lists[:10]
			}
			for _, list := range lists {
				if items, err := h.CustomLists.ListItems(profile, list.ID); err == nil {
					add(items, "custom_list")
				}
			}
		}
	}
	if settings.SearchInterestsEnabled() && h.Interests != nil {
		for _, item := range h.Interests.List(profile) {
			seeds = append(seeds, personalizedSeed{MediaType: item.MediaType, TMDBID: item.TMDBID, Title: item.Title, Activity: item.SearchedAt, Strength: 0.55, Source: "search"})
		}
	}
	return seeds
}

func recommendationInputsFingerprint(seeds []personalizedSeed, settings config.RecommendationSettings, policy personalizedKidsRatingFilter, language string) string {
	seeds = append([]personalizedSeed(nil), seeds...)
	sort.Slice(seeds, func(i, j int) bool {
		a, _ := json.Marshal(seeds[i])
		b, _ := json.Marshal(seeds[j])
		return string(a) < string(b)
	})
	data, _ := json.Marshal([]any{seeds, settings, policy.enabled, policy.maxMovieRating, policy.maxTVRating, language})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (h *MetadataHandler) ClearSearchInterests(w http.ResponseWriter, r *http.Request) {
	profile := strings.TrimSpace(mux.Vars(r)["userID"])
	if profile == "" {
		profile = strings.TrimSpace(r.URL.Query().Get("userId"))
	}
	if h.UsersService == nil {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}
	user, ok := h.UsersService.Get(profile)
	if !ok || (!auth.IsMaster(r) && (auth.GetAccountID(r) == "" || user.AccountID != auth.GetAccountID(r))) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}
	if h.Interests == nil {
		http.Error(w, "search interests unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := h.Interests.Clear(profile); err != nil {
		http.Error(w, "failed to clear search interests", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
