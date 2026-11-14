package handlers

import (
	"context"
	"encoding/json"
	"github.com/gorilla/mux"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/auth"
	"novastream/models"
	user_settings "novastream/services/user_settings"
	"novastream/services/users"
)

type recommendationInterestsFake struct {
	items    map[string][]models.RecommendationInterest
	observed []models.Title
	cleared  string
}

func (f *recommendationInterestsFake) Observe(profile string, title models.Title) {
	f.observed = append(f.observed, title)
}
func (f *recommendationInterestsFake) List(profile string) []models.RecommendationInterest {
	return f.items[profile]
}
func (f *recommendationInterestsFake) Clear(profile string) error {
	f.cleared = profile
	delete(f.items, profile)
	return nil
}

type recommendationListsFake struct {
	items map[string][]models.WatchlistItem
}

func (f *recommendationListsFake) ListLists(profile string) ([]models.CustomList, error) {
	if len(f.items[profile]) == 0 {
		return nil, nil
	}
	return []models.CustomList{{ID: "favorites"}}, nil
}
func (f *recommendationListsFake) ListItems(profile, list string) ([]models.WatchlistItem, error) {
	return f.items[profile], nil
}

type recommendationWatchlistFake struct {
	items map[string][]models.WatchlistItem
}

func (f *recommendationWatchlistFake) List(profile string) ([]models.WatchlistItem, error) {
	return f.items[profile], nil
}

func TestRecommendationInputsRequireSearchOptInAndUnambiguousExactTitle(t *testing.T) {
	h := NewMetadataHandler(&fakeMetadataService{}, testConfigManager(t))
	interests := &recommendationInterestsFake{}
	h.Interests = interests
	result := models.SearchResult{Title: models.Title{ID: "tmdb:movie:1", TMDBID: 1, MediaType: "movie", Name: "Example", OriginalName: "Original Example"}}
	h.observeSearchInterest("stefan", "Example", []models.SearchResult{result})
	if len(interests.observed) != 0 {
		t.Fatal("search recording was not opt in")
	}
	cfg, _ := h.CfgManager.Load()
	yes := true
	cfg.Recommendations.UseSearchInterests = &yes
	if err := h.CfgManager.Save(cfg); err != nil {
		t.Fatal(err)
	}
	h.observeSearchInterest("stefan", "Exam", []models.SearchResult{result})
	h.observeSearchInterest("stefan", "Example", []models.SearchResult{result, result})
	if len(interests.observed) != 0 {
		t.Fatal("stored partial/ambiguous query")
	}
	h.observeSearchInterest("stefan", "Original Example", []models.SearchResult{result})
	if len(interests.observed) != 1 || interests.observed[0].Name != "Example" {
		t.Fatal("exact resolved title was not recorded")
	}
}

func TestRecommendationInputsIncludeOldFavoritesAndRemainProfileLocal(t *testing.T) {
	h := NewMetadataHandler(&fakeMetadataService{}, testConfigManager(t))
	old := time.Now().AddDate(-2, 0, 0)
	h.WatchlistService = &recommendationWatchlistFake{items: map[string][]models.WatchlistItem{"stefan": {{ID: "tmdb:movie:1", MediaType: "movie", Name: "Favorite", AddedAt: old}}, "ella": {{ID: "tmdb:movie:2", MediaType: "movie", Name: "Child"}}}}
	h.CustomLists = &recommendationListsFake{items: map[string][]models.WatchlistItem{"stefan": {{ID: "tmdb:tv:3", MediaType: "series", Name: "Saved series", AddedAt: old}}}}
	additional := h.additionalRecommendationSeeds("stefan")
	now := time.Now()
	seeds := buildPersonalizedSeeds("stefan", nil, nil, now.AddDate(0, 0, -14), now, 12, additional...)
	if len(seeds) != 2 {
		t.Fatalf("old favorites discarded: %+v", seeds)
	}
	for _, seed := range seeds {
		if seed.Title == "Child" {
			t.Fatal("other profile seeded recommendations")
		}
	}
	if len(h.additionalRecommendationSeeds("bernd")) != 0 {
		t.Fatal("foreign profile inherited favorites")
	}
}

func TestRecommendationsFromListsInvalidateCacheWhenMembershipChanges(t *testing.T) {
	fake := &fakeMetadataService{similarByKey: map[string][]models.Title{"movie:1": {{ID: "tmdb:movie:101", TMDBID: 101, MediaType: "movie", Name: "First pick"}, {ID: "tmdb:movie:1", TMDBID: 1, MediaType: "movie", Name: "Saved seed"}}, "movie:2": {{ID: "tmdb:movie:102", TMDBID: 102, MediaType: "movie", Name: "New pick"}}}}
	h := NewMetadataHandler(fake, testConfigManager(t))
	h.HistoryService = &fakeMetadataHistoryService{}
	watchlist := &recommendationWatchlistFake{items: map[string][]models.WatchlistItem{"stefan": {{ID: "tmdb:movie:1", MediaType: "movie", Name: "Saved seed", AddedAt: time.Now()}}}}
	h.WatchlistService = watchlist
	request := func() PersonalizedRecommendationsResponse {
		w := httptest.NewRecorder()
		h.GetPersonalizedRecommendations(w, httptest.NewRequest("GET", "/api/recommendations/personalized?userId=stefan&limitPerType=1", nil))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var response PersonalizedRecommendationsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := request()
	if len(first.Items) != 1 || first.Items[0].Title.TMDBID != 101 || first.Explanation == nil || first.Explanation.Seeds[0].Source != "watchlist" {
		t.Fatalf("unexpected list recommendation: %+v", first)
	}
	watchlist.items["stefan"] = []models.WatchlistItem{{ID: "tmdb:movie:2", MediaType: "movie", Name: "New seed", AddedAt: time.Now()}}
	second := request()
	if len(second.Items) != 1 || second.Items[0].Title.TMDBID != 102 {
		t.Fatal("list edit reused stale recommendations")
	}
}

func TestRecommendationFingerprintChangesForPolicyLanguageAndSearchSignals(t *testing.T) {
	base := recommendationInputsFingerprint(nil, config.RecommendationSettings{}, personalizedKidsRatingFilter{}, "de")
	for _, value := range []string{recommendationInputsFingerprint(nil, config.RecommendationSettings{}, personalizedKidsRatingFilter{enabled: true, maxMovieRating: "G"}, "de"), recommendationInputsFingerprint(nil, config.RecommendationSettings{}, personalizedKidsRatingFilter{}, "en"), recommendationInputsFingerprint([]personalizedSeed{{TMDBID: 1, Source: "search"}}, config.RecommendationSettings{}, personalizedKidsRatingFilter{}, "de")} {
		if value == base {
			t.Fatal("reused incompatible cache key")
		}
	}
}

func TestClearSearchInterestsRejectsForeignAccount(t *testing.T) {
	service := &recommendationInterestsFake{items: map[string][]models.RecommendationInterest{"stefan": {{TMDBID: 1}}}}
	h := &MetadataHandler{Interests: service, UsersService: &fakeUsersServiceForSearch{users: map[string]models.User{"stefan": {ID: "stefan", AccountID: "household-a"}}}}
	for i, account := range []string{"household-b", "household-a"} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			r := httptest.NewRequest(http.MethodDelete, "/admin/api/recommendation-interests?userId=stefan", nil)
			r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, account))
			w := httptest.NewRecorder()
			h.ClearSearchInterests(w, r)
			want := 404
			if i == 1 {
				want = 204
			}
			if w.Code != want {
				t.Fatalf("status=%d want=%d", w.Code, want)
			}
		})
	}
	if service.cleared != "stefan" {
		t.Fatal("owner could not clear interests")
	}
}

func TestCuratedKidsProfilesDoNotReceiveUnrestrictedRecommendations(t *testing.T) {
	h := NewMetadataHandler(&fakeMetadataService{}, testConfigManager(t))
	h.HistoryService = &fakeMetadataHistoryService{}
	h.UsersService = &fakeUsersServiceForSearch{users: map[string]models.User{"ella": {ID: "ella", IsKidsProfile: true, KidsMode: "content_list"}}}
	w := httptest.NewRecorder()
	h.GetPersonalizedRecommendations(w, httptest.NewRequest("GET", "/api/recommendations/personalized?userId=ella", nil))
	var value PersonalizedRecommendationsResponse
	_ = json.Unmarshal(w.Body.Bytes(), &value)
	if w.Code != 200 || len(value.Items) != 0 {
		t.Fatal("curated child received broad recommendations")
	}
}

func TestRecommendationManifestChangesOnlyForEnabledOwnInterests(t *testing.T) {
	dir := t.TempDir()
	profiles, err := users.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := profiles.CreateForAccount("household-a", "Adult")
	if err != nil {
		t.Fatal(err)
	}
	prefs, err := user_settings.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	if err := prefs.Update(profile.ID, models.UserSettings{Recommendations: &config.RecommendationSettings{UseSearchInterests: &yes}}); err != nil {
		t.Fatal(err)
	}
	h := NewStartupHandler(prefs, nil, nil, nil, testConfigManager(t), profiles)
	signals := &recommendationInterestsFake{items: map[string][]models.RecommendationInterest{}}
	h.Interests = signals
	revision := func() string {
		r := mux.SetURLVars(httptest.NewRequest("GET", "/api/users/"+profile.ID+"/home-manifest", nil), map[string]string{"userID": profile.ID})
		w := httptest.NewRecorder()
		h.GetHomeManifest(w, r)
		var response HomeManifestResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Revision
	}
	before := revision()
	signals.items["bernd"] = []models.RecommendationInterest{{TMDBID: 1}}
	if revision() != before {
		t.Fatal("foreign interests changed home")
	}
	signals.items[profile.ID] = []models.RecommendationInterest{{TMDBID: 2, MediaType: "movie", Title: "Example", SearchedAt: time.Now()}}
	if revision() == before {
		t.Fatal("own interests did not refresh home")
	}
	no := false
	if err := prefs.Update(profile.ID, models.UserSettings{Recommendations: &config.RecommendationSettings{UseSearchInterests: &no}}); err != nil {
		t.Fatal(err)
	}
	disabled := revision()
	signals.items[profile.ID] = nil
	if revision() != disabled {
		t.Fatal("disabled interests still affected home")
	}
}
