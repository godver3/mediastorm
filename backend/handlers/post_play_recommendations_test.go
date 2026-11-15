package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"novastream/models"
	"novastream/services/titleratings"
)

type postPlayHiddenFake struct {
	items []models.HiddenItem
	err   error
}

func (h postPlayHiddenFake) List(profile string) ([]models.HiddenItem, error) { return h.items, h.err }

func postPlayRecommendationFixture(t *testing.T) (*MetadataHandler, *titleratings.Service, *ratingRepoFake, models.Title) {
	h, s, repo, current := ratingFixture(t)
	h.Service = nil
	s.Observe("profile", "fixture-client", []models.Title{current})
	h.HistoryService = &postPlayHistoryFake{item: &models.WatchHistoryItem{MediaType: "movie", ItemID: current.ID, Watched: true, Source: models.WatchSourcePlayed}}
	h.PostPlayHidden = postPlayHiddenFake{}
	return h, s, repo, current
}
func postPlayRecommendationsHTTP(t *testing.T, h *MetadataHandler, current models.Title) (int, []models.TrendingItem, string) {
	t.Helper()
	w := httptest.NewRecorder()
	h.PostPlayRecommendations(w, postPlayRequest(current.MediaType, current.ID, ""))
	var reply struct {
		Items  []models.TrendingItem `json:"items"`
		Source string                `json:"source"`
	}
	if w.Code == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
	}
	return w.Code, reply.Items, reply.Source
}

func TestPostPlayRecommendationsUseSavedChoicesWithoutProvidersOrRatingWrites(t *testing.T) {
	h, s, repo, current := postPlayRecommendationFixture(t)
	candidate := models.Title{ID: "tmdb:movie:8", MediaType: "movie", Name: "Synthetic saved choice", TMDBID: 8}
	if err := s.Put(context.Background(), "profile", candidate, "like"); err != nil {
		t.Fatal(err)
	}
	before := repo.writes
	code, items, source := postPlayRecommendationsHTTP(t, h, current)
	if code != 200 || source != "saved_choices" || len(items) != 1 || items[0].Title.ID != candidate.ID {
		t.Fatalf("fallback %d %s %v", code, source, items)
	}
	if repo.writes != before || h.Service != nil {
		t.Fatal("opening suggestions wrote a rating or required providers")
	}
}

func TestPostPlayRecommendationsFailClosedOnMissingHiddenOrFailedStoredInputs(t *testing.T) {
	for _, mode := range []string{"missing hidden", "hidden error", "ratings error", "history error"} {
		t.Run(mode, func(t *testing.T) {
			h, _, repo, current := postPlayRecommendationFixture(t)
			switch mode {
			case "missing hidden":
				h.PostPlayHidden = nil
			case "hidden error":
				h.PostPlayHidden = postPlayHiddenFake{err: errors.New("private hidden storage")}
			case "ratings error":
				repo.err = errors.New("private preference storage")
			case "history error":
				h.HistoryService.(*postPlayHistoryFake).listErr = errors.New("private history storage")
			}
			code, items, _ := postPlayRecommendationsHTTP(t, h, current)
			if code != 503 || len(items) != 0 || repo.writes != 0 {
				t.Fatalf("failure was presented as successful suggestions: %d", code)
			}
		})
	}
}

func TestPostPlayCachedFallbackSurvivesExpiredHomeCacheButDoesNotCrossProfiles(t *testing.T) {
	h, _, _, current := postPlayRecommendationFixture(t)
	candidate := models.Title{ID: "tmdb:movie:8", MediaType: "movie", Name: "Synthetic cached choice", TMDBID: 8}
	h.personalizedCache["profile:old"] = personalizedRecommendationsCacheEntry{profile: "profile", response: PersonalizedRecommendationsResponse{Items: []models.TrendingItem{{Title: candidate}}}, expiresAt: time.Now().Add(-time.Hour)}
	h.personalizedCache["profile:other:ambiguous-prefix"] = personalizedRecommendationsCacheEntry{profile: "profile:other", response: PersonalizedRecommendationsResponse{Items: []models.TrendingItem{{Title: models.Title{ID: "tmdb:movie:99", MediaType: "movie"}}}}, expiresAt: time.Now().Add(time.Hour)}
	code, items, source := postPlayRecommendationsHTTP(t, h, current)
	if code != 200 || source != "saved_choices" || len(items) != 1 || items[0].Title.ID != candidate.ID {
		t.Fatal("expired cache fallback lost or crossed profile identity")
	}
}

func TestPostPlayRejectsCachedPositiveContextAfterItsSeedIsDisliked(t *testing.T) {
	h, s, _, current := postPlayRecommendationFixture(t)
	candidate := models.Title{ID: "tmdb:movie:8", MediaType: "movie", TMDBID: 8}
	h.personalizedCache["profile:old"] = personalizedRecommendationsCacheEntry{profile: "profile", response: PersonalizedRecommendationsResponse{Items: []models.TrendingItem{{Title: candidate}}, Explanation: &PersonalizedRecommendationExplanation{Seeds: []PersonalizedRecommendationSeed{{MediaType: "movie", TMDBID: current.TMDBID}}}}, expiresAt: time.Now().Add(time.Minute)}
	if err := s.Put(context.Background(), "profile", current, "dislike"); err != nil {
		t.Fatal(err)
	}
	code, items, source := postPlayRecommendationsHTTP(t, h, current)
	if code != 200 || source != "unavailable" || len(items) != 0 {
		t.Fatal("negative feedback reused a positive cached context")
	}
}

func TestPostPlayFiltersCurrentSeenProgressHiddenDislikesAndDuplicateAliasesByKind(t *testing.T) {
	h, _, _, current := postPlayRecommendationFixture(t)
	title := func(id string, kind string, tmdb int64) models.Title {
		return models.Title{ID: id, MediaType: kind, TMDBID: tmdb, Name: "Synthetic"}
	}
	candidates := []models.TrendingItem{{Title: current}, {Title: title("tmdb:movie:2", "movie", 2)}, {Title: title("tmdb:movie:3", "movie", 3)}, {Title: title("tmdb:movie:4", "movie", 4)}, {Title: title("tmdb:movie:5", "movie", 5)}, {Title: title("tmdb:movie:6", "movie", 6)}, {Title: title("tt006", "movie", 6)}, {Title: title("tmdb:tv:6", "series", 6)}}
	before := candidates[5].Rank
	got := h.filterPostPlaySuggestions("profile", current, candidates, []models.WatchHistoryItem{{MediaType: "movie", ItemID: "tmdb:movie:2", Watched: true}}, []models.PlaybackProgress{{MediaType: "movie", ItemID: "tmdb:movie:3", Position: 10}}, []models.HiddenItem{{MediaType: "movie", ID: "tmdb:movie:4"}}, []models.TitleRating{{Title: title("tmdb:movie:5", "movie", 5), Aliases: []string{"movie:tmdb:5"}, Value: "dislike"}})
	if len(got) != 2 || got[0].Title.ID != "tmdb:movie:6" || got[1].Title.ID != "tmdb:tv:6" || candidates[5].Rank != before {
		t.Fatalf("wrong filtering or shared candidate mutation: %v", got)
	}
}

func TestPostPlayFiltersRecheckChildPolicyAndKeepSeriesAheadAfterEpisode(t *testing.T) {
	h, _, _, current := postPlayRecommendationFixture(t)
	h.UsersService = fallbackProfiles{models.User{ID: "profile", AccountID: "account", IsKidsProfile: true, KidsMode: "rating", KidsMaxMovieRating: "PG", KidsMaxTVRating: "TV-PG"}}
	current.MediaType = "series"
	candidates := []models.TrendingItem{{Title: models.Title{ID: "tmdb:movie:8", MediaType: "movie", Certification: "PG"}}, {Title: models.Title{ID: "tmdb:tv:9", MediaType: "series", Certification: "TV-PG"}}, {Title: models.Title{ID: "tmdb:movie:10", MediaType: "movie", Certification: "R"}}, {Title: models.Title{ID: "tmdb:movie:11", MediaType: "movie"}}}
	got := h.filterPostPlaySuggestions("profile", current, candidates, nil, nil, nil, nil)
	if len(got) != 2 || got[0].Title.MediaType != "series" || got[1].Title.MediaType != "movie" {
		t.Fatal("child policy or episode suggestion priority lost")
	}
}

func TestPostPlayExcludesLegacyWholeSeriesWatchRowsWithoutParentField(t *testing.T) {
	h, _, _, current := postPlayRecommendationFixture(t)
	candidate := models.Title{ID: "tmdb:tv:8", MediaType: "series", TMDBID: 8}
	got := h.filterPostPlaySuggestions("profile", current, []models.TrendingItem{{Title: candidate}}, []models.WatchHistoryItem{{MediaType: "series", ItemID: candidate.ID, Watched: true}}, nil, nil, nil)
	if len(got) != 0 {
		t.Fatal("watched whole-series identity was ignored")
	}
}
