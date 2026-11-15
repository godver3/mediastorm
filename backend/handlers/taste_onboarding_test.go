package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"novastream/internal/auth"
	"novastream/internal/profileaccess"
	"novastream/models"
	"novastream/services/tasteonboarding"
)

type tasteRepoFake struct {
	states map[string]models.TasteOnboardingState
	err    error
	writes int
}

func (f *tasteRepoFake) Get(ctx context.Context, p string) (models.TasteOnboardingState, error) {
	if f.err != nil {
		return models.TasteOnboardingState{}, f.err
	}
	state, ok := f.states[p]
	if !ok {
		state = models.TasteOnboardingState{Status: "new", Interests: []models.TasteInterest{}}
	}
	return state, nil
}
func (f *tasteRepoFake) Save(ctx context.Context, p string, state models.TasteOnboardingState) error {
	if f.err != nil {
		return f.err
	}
	state.Interests = models.MergeTasteInterests(f.states[p].Interests, state.Interests)
	f.states[p] = state
	f.writes++
	return nil
}
func (f *tasteRepoFake) Skip(ctx context.Context, p string, now time.Time) error {
	if f.err != nil {
		return f.err
	}
	state := f.states[p]
	state.Status = "skipped"
	state.UpdatedAt = now
	f.states[p] = state
	f.writes++
	return nil
}
func (f *tasteRepoFake) Delete(ctx context.Context, p string) error {
	if f.err != nil {
		return f.err
	}
	delete(f.states, p)
	f.writes++
	return nil
}
func tasteFixture(t *testing.T) (*MetadataHandler, *tasteRepoFake, *ratingRepoFake, models.Title) {
	h, _, ratings, title := ratingFixture(t)
	repo := &tasteRepoFake{states: map[string]models.TasteOnboardingState{}}
	h.Taste = tasteonboarding.New(repo)
	return h, repo, ratings, title
}
func tasteHTTP(t *testing.T, h *MetadataHandler, method, body string) (int, tasteOnboardingResponse) {
	t.Helper()
	r := tasteRequest(method, body)
	w := httptest.NewRecorder()
	h.TasteOnboarding(w, r)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("setup response is cacheable")
	}
	var state tasteOnboardingResponse
	_ = json.Unmarshal(w.Body.Bytes(), &state)
	return w.Code, state
}
func tasteRequest(method, body string) *http.Request {
	r := httptest.NewRequest(method, "/api/users/profile/taste-onboarding", strings.NewReader(body))
	r.Header.Set("X-Client-ID", "fixture-client")
	r = mux.SetURLVars(r, map[string]string{"userID": "profile"})
	return r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, "account"))
}
func tasteSaveBody(id, kind string) string {
	raw, _ := json.Marshal(map[string]any{"action": "save", "titles": []map[string]string{{"titleId": id, "mediaType": kind}}})
	return string(raw)
}
func TestTasteSetupSkipsEditsAndClearsWithoutChangingRatingsOrPlayback(t *testing.T) {
	h, repo, ratings, title := tasteFixture(t)
	history := &fakeMetadataHistoryService{}
	h.HistoryService = history
	code, state := tasteHTTP(t, h, "GET", "")
	if code != 200 || state.Status != "new" || !state.Available || state.CanUseHistory || len(state.Titles) != 0 || len(state.MediaTypes) != 2 {
		t.Fatal("new state invalid")
	}
	_, state = tasteHTTP(t, h, "PUT", `{"action":"skip"}`)
	if state.Status != "skipped" || len(state.Titles) != 0 {
		t.Fatal("skip created interests")
	}
	h.TitleRatings.Observe("profile", "fixture-client", []models.Title{title})
	if err := h.TitleRatings.Put(context.Background(), "profile", title, "love"); err != nil {
		t.Fatal(err)
	}
	beforeRatings := append([]models.TitleRating(nil), ratings.items["profile"]...)
	beforeHistory := append([]models.WatchHistoryItem(nil), history.history...)
	code, state = tasteHTTP(t, h, "PUT", tasteSaveBody(title.ID, "movie"))
	if code != 200 || state.Status != "completed" || state.Source != "manual" || len(state.Titles) != 1 {
		t.Fatal("selection not saved")
	}
	selectedAt := repo.states["profile"].Interests[0].AddedAt
	_, state = tasteHTTP(t, h, "PUT", `{"action":"skip"}`)
	if state.Status != "skipped" || len(state.Titles) != 1 || !repo.states["profile"].Interests[0].AddedAt.Equal(selectedAt) {
		t.Fatal("skip erased existing interests")
	}
	_, state = tasteHTTP(t, h, "PUT", `{"action":"save","titles":[]}`)
	if state.Status != "completed" || len(state.Titles) != 0 {
		t.Fatal("empty edit failed")
	}
	_, state = tasteHTTP(t, h, "DELETE", "")
	if state.Status != "new" || len(repo.states) != 0 {
		t.Fatal("clear retained setup data")
	}
	if !reflect.DeepEqual(beforeRatings, ratings.items["profile"]) || !reflect.DeepEqual(beforeHistory, history.history) {
		t.Fatal("setup changed ratings or history")
	}
}
func TestTasteSetupRejectsForgedMetadataUndeliveredTitlesAndInvalidActions(t *testing.T) {
	for _, test := range []struct {
		body   string
		status int
	}{
		{`{"action":"save","titles":[{"titleId":"unknown","mediaType":"movie"}]}`, 409},
		{`{"action":"save","titles":[{"titleId":"tmdb:movie:1","mediaType":"movie","name":"forged"}]}`, 400},
		{`{"action":"external-sync"}`, 400}, {`{"action":"history","titles":[{"titleId":"a","mediaType":"movie"}]}`, 400},
		{`{"action":"save"}`, 400}, {`{"action":"save","titles":[{"titleId":"a","mediaType":"person"}]}`, 400},
		{`{"action":"save","titles":[]} {}`, 400}, {`{"action":"save","titles":[],"userId":"other"}`, 400},
	} {
		t.Run(test.body[:min(len(test.body), 40)], func(t *testing.T) {
			h, repo, _, _ := tasteFixture(t)
			code, _ := tasteHTTP(t, h, "PUT", test.body)
			if code != test.status || repo.writes != 0 {
				t.Fatalf("invalid input accepted: %d", code)
			}
		})
	}
}
func TestTasteSetupRechecksOwnershipPINAndClientIdentity(t *testing.T) {
	for _, mode := range []string{"foreign account", "locked PIN", "stream scope", "other client"} {
		t.Run(mode, func(t *testing.T) {
			h, repo, _, title := tasteFixture(t)
			h.TitleRatings.Observe("profile", "fixture-client", []models.Title{title})
			r := tasteRequest("PUT", tasteSaveBody(title.ID, "movie"))
			want := 404
			switch mode {
			case "foreign account":
				r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, "other"))
			case "stream scope":
				r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeySession, models.Session{Scope: models.SessionScopeStream}))
				want = 403
			case "other client":
				r.Header.Set("X-Client-ID", "other")
				want = 409
			case "locked PIN":
				r = profileaccess.WithResourceGuard(r, func(w http.ResponseWriter, r *http.Request, p string) bool {
					http.Error(w, "locked", 403)
					return false
				})
				want = 403
			}
			w := httptest.NewRecorder()
			profileaccess.Middleware(func(p, a string) bool { return a == "account" })(http.HandlerFunc(h.TasteOnboarding)).ServeHTTP(w, r)
			if w.Code != want || repo.writes != 0 {
				t.Fatalf("scope guard failed: %d", w.Code)
			}
		})
	}
}
func TestTasteSetupHidesRestrictedStoredMetadataAndCanStillDeleteIt(t *testing.T) {
	for _, mode := range []string{"lower cap", "curated", "missing cap"} {
		t.Run(mode, func(t *testing.T) {
			h, repo, _, title := tasteFixture(t)
			_ = h.Taste.Save(context.Background(), "profile", "manual", []models.Title{title})
			user := models.User{ID: "profile", AccountID: "account", IsKidsProfile: true, KidsMode: "rating", KidsMaxMovieRating: "G"}
			if mode == "curated" {
				user.KidsMode = "content_list"
			}
			if mode == "missing cap" {
				user.KidsMaxMovieRating = ""
			}
			h.UsersService = fallbackProfiles{user}
			code, state := tasteHTTP(t, h, "GET", "")
			if code != 200 || len(state.Titles) != 0 || state.CanUseHistory {
				t.Fatal("restricted title metadata leaked")
			}
			if mode != "lower cap" && state.Available {
				t.Fatal("unsupported child policy offered setup")
			}
			code, _ = tasteHTTP(t, h, "DELETE", "")
			if code != 200 || len(repo.states) != 0 {
				t.Fatal("converted profile cannot delete own starting interests")
			}
		})
	}
}
func TestTasteSetupHistoryRequiresExplicitChoiceAndOnlyUsesThisProfilesRealWatchedTitles(t *testing.T) {
	h, repo, ratings, _ := tasteFixture(t)
	history := &fakeMetadataHistoryService{history: []models.WatchHistoryItem{
		{MediaType: "movie", ItemID: "tmdb:movie:7", Name: "Synthetic Watched", Watched: true},
		{MediaType: "movie", ItemID: "tmdb:movie:8", Name: "Synthetic Unwatched", Watched: false},
		{MediaType: "episode", ItemID: "episode:9", Name: "Ambiguous episode", Watched: true, ExternalIDs: map[string]string{"tmdb": "9"}},
		{MediaType: "episode", ItemID: "ep:10", SeriesID: "tmdb:tv:10", SeriesName: "Synthetic Series", Watched: true, ExternalIDs: map[string]string{"tmdb": "10"}},
	}}
	h.HistoryService = history
	_, state := tasteHTTP(t, h, "GET", "")
	if !state.CanUseHistory || len(repo.states) != 0 || ratings.writes != 0 {
		t.Fatal("read implicitly imported history")
	}
	code, state := tasteHTTP(t, h, "PUT", `{"action":"history"}`)
	if code != 200 || state.Source != "history" || len(state.Titles) != 2 || len(history.history) != 4 || ratings.writes != 0 {
		t.Fatal("history choice invented or modified activity")
	}
	for _, title := range state.Titles {
		if title.Name == "Synthetic Unwatched" || title.Name == "Ambiguous episode" {
			t.Fatal("unwatched/ambiguous history became a seed")
		}
	}
}
func TestTasteSetupHistoryFailureDoesNotReplaceExistingInterests(t *testing.T) {
	h, repo, _, title := tasteFixture(t)
	_ = h.Taste.Save(context.Background(), "profile", "manual", []models.Title{title})
	h.HistoryService = &fakeMetadataHistoryService{err: errors.New("private failure")}
	code, _ := tasteHTTP(t, h, "PUT", `{"action":"history"}`)
	if code != 503 || len(repo.states["profile"].Interests) != 1 {
		t.Fatal("history failure changed selection")
	}
	h.HistoryService = &fakeMetadataHistoryService{}
	code, _ = tasteHTTP(t, h, "PUT", `{"action":"history"}`)
	if code != 409 || len(repo.states["profile"].Interests) != 1 {
		t.Fatal("empty history erased selection")
	}
}
func TestTasteSetupSeedsPersistIndependentlyAndExplicitDislikesOverrideThem(t *testing.T) {
	h, repo, _, title := tasteFixture(t)
	title.TMDBID = 7
	title.ID = "tmdb:movie:7"
	h.HistoryService = &fakeMetadataHistoryService{}
	h.Service = &fakeMetadataService{similarByKey: map[string][]models.Title{"movie:7": {{ID: "tmdb:movie:100", TMDBID: 100, MediaType: "movie", Name: "Synthetic Suggestion"}}}}
	request := func() PersonalizedRecommendationsResponse {
		w := httptest.NewRecorder()
		h.GetPersonalizedRecommendations(w, httptest.NewRequest("GET", "/api/recommendations/personalized?userId=profile", nil))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var result PersonalizedRecommendationsResponse
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		return result
	}
	if len(request().Items) != 0 {
		t.Fatal("new profile inherited recommendations")
	}
	_ = h.Taste.Save(context.Background(), "profile", "manual", []models.Title{title})
	state := repo.states["profile"]
	state.Interests[0].AddedAt = time.Now().AddDate(-3, 0, 0)
	repo.states["profile"] = state
	result := request()
	if result.SeedCount != 1 || len(result.Items) != 1 || result.Explanation.Seeds[0].Source != "onboarding" || strings.Contains(strings.ToLower(result.Explanation.Summary), "watched") {
		t.Fatal("starting interests aged out or invented playback")
	}
	_ = h.TitleRatings.Put(context.Background(), "profile", title, "dislike")
	if request().SeedCount != 0 || len(repo.states["profile"].Interests) != 1 {
		t.Fatal("dislike failed to override the independent starting selection")
	}
	_ = h.Taste.Delete(context.Background(), "profile")
	ratings, _ := h.TitleRatings.List(context.Background(), "profile")
	if len(ratings) != 1 || ratings[0].Value != "dislike" {
		t.Fatal("clearing setup deleted an explicit rating")
	}
}
func TestTasteCandidateSearchDoesNotRecordNormalSearchInterestsAndDeliversOnlyAllowedTypes(t *testing.T) {
	h, _, _, title := tasteFixture(t)
	title.TMDBID = 1
	meta := &fakeMetadataService{searchResp: []models.SearchResult{{Title: title}, {Title: models.Title{ID: "person:7", MediaType: "person", Name: "Synthetic Person"}}, {Title: models.Title{ID: "adult", MediaType: "movie", Name: "Synthetic Mature", Adult: true}}}}
	h.Service = meta
	interests := &recommendationInterestsFake{}
	h.Interests = interests
	cfg, _ := h.CfgManager.Load()
	yes := true
	cfg.Recommendations.UseSearchInterests = &yes
	if err := h.CfgManager.Save(cfg); err != nil {
		t.Fatal(err)
	}
	r := tasteRequest("GET", "")
	r.URL.Path += "/candidates"
	r.URL.RawQuery = "kind=movie&q=" + url.QueryEscape(title.Name)
	w := httptest.NewRecorder()
	h.TasteCandidates(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var page struct {
		Items []models.TrendingItem `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if len(page.Items) != 1 || len(interests.observed) != 0 {
		t.Fatal("candidate search returned wrong types or recorded an implicit interest")
	}
	if _, ok := h.TitleRatings.Resolve("profile", "fixture-client", "movie", title.ID); !ok {
		t.Fatal("candidate was not delivered for a later explicit selection")
	}
}
func TestTasteCandidateChildPolicyFiltersUnknownMatureAndDisallowedMediaKinds(t *testing.T) {
	h, _, _, _ := tasteFixture(t)
	h.UsersService = fallbackProfiles{models.User{ID: "profile", AccountID: "account", IsKidsProfile: true, KidsMode: "rating", KidsMaxMovieRating: "G"}}
	h.Service = &fakeMetadataService{searchResp: []models.SearchResult{{Title: models.Title{ID: "allowed", Name: "Synthetic G", MediaType: "movie", Certification: "G"}}, {Title: models.Title{ID: "mature", Name: "Synthetic R", MediaType: "movie", Certification: "R"}}, {Title: models.Title{ID: "unknown", Name: "Synthetic Unknown", MediaType: "movie"}}}}
	r := tasteRequest("GET", "")
	r.URL.RawQuery = "kind=movie&q=Synthetic"
	w := httptest.NewRecorder()
	h.TasteCandidates(w, r)
	var page struct {
		Items []models.TrendingItem `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if w.Code != 200 || len(page.Items) != 1 || page.Items[0].Title.ID != "allowed" {
		t.Fatal("child candidate filtering failed")
	}
	r.URL.RawQuery = "kind=series&q=Synthetic"
	w = httptest.NewRecorder()
	h.TasteCandidates(w, r)
	if w.Code != 403 {
		t.Fatal("uncapped media kind was searched for a child profile")
	}
}

func TestTasteSignalsDoNotEraseStrongerExplicitRatingProvenance(t *testing.T) {
	now := time.Now()
	title := models.Title{ID: "tmdb:movie:7", TMDBID: 7, MediaType: "movie", Name: "Synthetic"}
	signals := []personalizedSeed{preferenceRecommendationSeed(title, []string{"movie:tmdb:7"}, now.AddDate(-2, 0, 0), 1.7, "rating_love"), preferenceRecommendationSeed(title, []string{"movie:tmdb:7"}, now, 1.1, "onboarding")}
	seeds := buildPersonalizedSeeds("profile", nil, nil, now.AddDate(0, 0, -14), now, 12, signals...)
	if len(seeds) != 1 || seeds[0].Source != "rating_love" || seeds[0].Strength < 1.7 {
		t.Fatal("later setup selection erased an explicit rating signal")
	}
}
