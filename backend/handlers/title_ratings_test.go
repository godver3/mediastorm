package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"novastream/config"
	"novastream/internal/auth"
	"novastream/internal/profileaccess"
	"novastream/models"
	"novastream/services/titleratings"
)

type ratingRepoFake struct {
	items  map[string][]models.TitleRating
	err    error
	writes int
}

func (f *ratingRepoFake) List(ctx context.Context, profile string) ([]models.TitleRating, error) {
	return f.items[profile], f.err
}
func (f *ratingRepoFake) Put(ctx context.Context, profile string, item models.TitleRating) error {
	if f.err != nil {
		return f.err
	}
	f.writes++
	out := []models.TitleRating{}
	for _, old := range f.items[profile] {
		if titleratings.Match([]models.TitleRating{old}, item.Title) == "none" {
			out = append(out, old)
		}
	}
	if item.Value != "none" {
		out = append(out, item)
	}
	f.items[profile] = out
	return nil
}
func ratingFixture(t *testing.T) (*MetadataHandler, *titleratings.Service, *ratingRepoFake, models.Title) {
	t.Helper()
	repo := &ratingRepoFake{items: map[string][]models.TitleRating{}}
	s := titleratings.New(repo)
	h := NewMetadataHandler(&fakeMetadataService{}, testConfigManager(t))
	h.TitleRatings = s
	h.UsersService = fallbackProfiles{models.User{ID: "profile", AccountID: "account"}}
	title := models.Title{ID: "tmdb:movie:1", Name: "Synthetic Favorite", MediaType: "movie", TMDBID: 1, Certification: "PG"}
	return h, s, repo, title
}
func ratingRequest(method, id string, body string) *http.Request {
	r := httptest.NewRequest(method, "/api/users/profile/ratings/movie/"+id, strings.NewReader(body))
	r.Header.Set("X-Client-ID", "fixture-client")
	r = mux.SetURLVars(r, map[string]string{"userID": "profile", "mediaType": "movie", "titleID": id})
	return r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, "account"))
}
func ratingHTTP(t *testing.T, h *MetadataHandler, method, id, body string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	h.TitleRating(w, ratingRequest(method, id, body))
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("rating response may enter shared cache")
	}
	var reply struct {
		Rating string `json:"rating"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &reply)
	return w.Code, reply.Rating
}
func TestPersonalRatingRoundTripUsesDeliveredMetadataAndIndependentProfileStorage(t *testing.T) {
	h, s, repo, title := ratingFixture(t)
	if status, _ := ratingHTTP(t, h, "PUT", title.ID, `{"rating":"love"}`); status != 409 {
		t.Fatal("unknown title accepted")
	}
	s.Observe("profile", "fixture-client", []models.Title{title})
	for _, value := range []string{"none", "like", "love", "dislike", "none"} {
		if status, actual := ratingHTTP(t, h, "PUT", title.ID, `{"rating":"`+value+`"}`); status != 200 || actual != value {
			t.Fatalf("write failed %d %s", status, actual)
		}
		if status, actual := ratingHTTP(t, h, "GET", title.ID, ""); status != 200 || actual != value {
			t.Fatalf("read failed %d %s", status, actual)
		}
	}
	if len(repo.items["profile"]) != 0 || len(repo.items["other"]) != 0 {
		t.Fatal("removed vote retained or crossed profile")
	}
	if repo.writes != 5 {
		t.Fatal("unexpected writes")
	}
}
func TestPersonalRatingRejectsUntrustedFieldsValuesAndOversizedInput(t *testing.T) {
	for _, body := range []string{`{}`, `{"rating":"watched"}`, `{"rating":"Love"}`, `{"rating":"love","tmdbId":2}`, `{"rating":"like","url":"https://synthetic.invalid"}`, `{"rating":"like","name":"forged"}`, `{"rating":"like"}{}`, `{"rating":"` + strings.Repeat("x", 2000) + `"}`} {
		t.Run(body[:min(len(body), 45)], func(t *testing.T) {
			h, s, repo, title := ratingFixture(t)
			s.Observe("profile", "fixture-client", []models.Title{title})
			if status, _ := ratingHTTP(t, h, "PUT", title.ID, body); status != 400 {
				t.Fatalf("forged body accepted: %d", status)
			}
			if repo.writes != 0 {
				t.Fatal("invalid input wrote a vote")
			}
		})
	}
}
func TestPersonalRatingEnforcesOwnershipClientPINAndStreamScope(t *testing.T) {
	for _, mode := range []string{"foreign account", "other client", "PIN locked", "stream scope", "missing profile"} {
		t.Run(mode, func(t *testing.T) {
			h, s, repo, title := ratingFixture(t)
			s.Observe("profile", "fixture-client", []models.Title{title})
			r := ratingRequest("PUT", title.ID, `{"rating":"love"}`)
			switch mode {
			case "foreign account":
				r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, "other"))
			case "other client":
				r.Header.Set("X-Client-ID", "other")
			case "stream scope":
				r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeySession, models.Session{Scope: models.SessionScopeStream}))
			case "missing profile":
				h.UsersService = fallbackProfiles{}
			}
			if mode == "PIN locked" {
				r = profileaccess.WithResourceGuard(r, func(w http.ResponseWriter, r *http.Request, p string) bool {
					http.Error(w, "locked", 403)
					return false
				})
			}
			w := httptest.NewRecorder()
			profileaccess.Middleware(func(p, a string) bool { return a == "account" })(http.HandlerFunc(h.TitleRating)).ServeHTTP(w, r)
			want := 404
			if mode == "other client" {
				want = 409
			}
			if mode == "PIN locked" || mode == "stream scope" {
				want = 403
			}
			if w.Code != want || repo.writes != 0 {
				t.Fatalf("guard failed: %d, writes %d", w.Code, repo.writes)
			}
		})
	}
}
func TestPersonalRatingRechecksCurrentChildPolicyAndDoesNotLeakConvertedProfilePreferences(t *testing.T) {
	for _, mode := range []string{"rating allowed", "rating too mature", "rating missing", "no cap", "content list", "adult"} {
		t.Run(mode, func(t *testing.T) {
			h, s, repo, title := ratingFixture(t)
			_ = s.Put(context.Background(), "profile", title, "love")
			user := models.User{ID: "profile", AccountID: "account", IsKidsProfile: true, KidsMode: "rating", KidsMaxMovieRating: "PG"}
			switch mode {
			case "rating too mature":
				user.KidsMaxMovieRating = "G"
			case "rating missing":
				title.Certification = ""
			case "no cap":
				user.KidsMaxMovieRating = ""
			case "content list":
				user.KidsMode = "content_list"
			case "adult":
				title.Adult = true
			}
			// Simulate both the catalogue projection and durable metadata changing.
			repo.items["profile"][0].Title = title
			s.Observe("profile", "fixture-client", []models.Title{title})
			h.UsersService = fallbackProfiles{user}
			status, _ := ratingHTTP(t, h, "GET", title.ID, "")
			seeds, err := h.ratingRecommendationSeeds(context.Background(), "profile")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "rating allowed" {
				if status != 200 || len(seeds) != 1 {
					t.Fatal("allowed child title rejected")
				}
			} else if status != 404 || len(seeds) != 0 {
				t.Fatal("restricted preference exposed or seeded recommendations")
			}
		})
	}
}
func TestPersonalRatingAnnotationCopiesSharedResponsesAndWorksWithoutSourceHints(t *testing.T) {
	h, _, _, title := ratingFixture(t)
	_ = h.TitleRatings.Put(context.Background(), "profile", title, "love")
	shared := []models.TrendingItem{{Title: title}}
	before := append([]models.TrendingItem(nil), shared...)
	r := ratingRequest("GET", title.ID, "")
	w := httptest.NewRecorder()
	if err := h.encodeMetadata(w, r, shared); err != nil {
		t.Fatal(err)
	}
	var actual []models.TrendingItem
	_ = json.Unmarshal(w.Body.Bytes(), &actual)
	if len(actual) != 1 || actual[0].Title.PersonalRating != "love" || !reflect.DeepEqual(before, shared) {
		t.Fatal("shared metadata mutated or personal rating missing")
	}
	if _, ok := h.TitleRatings.Resolve("profile", "fixture-client", "movie", title.ID); !ok {
		t.Fatal("source-independent projection not delivered")
	}
	other := mux.SetURLVars(r, map[string]string{"userID": "other"})
	w = httptest.NewRecorder()
	_ = h.encodeMetadata(w, other, shared)
	if bytes.Contains(w.Body.Bytes(), []byte(`"personalRating"`)) {
		t.Fatal("rating crossed profile")
	}
	if w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("personal metadata cacheable")
	}
	for _, payload := range []any{&title, &models.SeriesDetails{Title: title}, []models.SearchResult{{Title: title}}} {
		w = httptest.NewRecorder()
		if err := h.encodeMetadata(w, r, payload); err != nil || !strings.Contains(w.Body.String(), `"personalRating":"love"`) {
			t.Fatal("detail/search annotation missing")
		}
	}
}
func TestExplicitRatingsSeedOldFavoritesOverrideImplicitSignalsAndInvalidateRecommendations(t *testing.T) {
	h, s, repo, title := ratingFixture(t)
	h.HistoryService = &fakeMetadataHistoryService{}
	h.Service = &fakeMetadataService{similarByKey: map[string][]models.Title{"movie:1": {{ID: "tmdb:movie:101", MediaType: "movie", TMDBID: 101, Name: "Synthetic suggestion"}, {ID: title.ID, MediaType: "movie", TMDBID: 1, Name: title.Name}}, "movie:101": {{ID: "tmdb:movie:102", MediaType: "movie", TMDBID: 102, Name: "Wrong implicit suggestion"}}}}
	request := func() PersonalizedRecommendationsResponse {
		w := httptest.NewRecorder()
		h.GetPersonalizedRecommendations(w, httptest.NewRequest("GET", "/api/recommendations/personalized?userId=profile", nil))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var reply PersonalizedRecommendationsResponse
		_ = json.Unmarshal(w.Body.Bytes(), &reply)
		return reply
	}
	if len(request().Items) != 0 {
		t.Fatal("new profile inherited recommendations")
	}
	_ = s.Put(context.Background(), "profile", title, "love")
	repo.items["profile"][0].UpdatedAt = time.Now().AddDate(-3, 0, 0)
	first := request()
	if first.SeedCount != 1 || len(first.Items) != 1 || first.Items[0].Title.TMDBID != 101 || first.Explanation.Seeds[0].Source != "rating_love" {
		t.Fatalf("old explicit favorite ignored: %+v", first)
	}
	candidate := models.Title{ID: "tmdb:movie:101", MediaType: "movie", TMDBID: 101, Name: "Synthetic suggestion"}
	_ = s.Put(context.Background(), "profile", candidate, "dislike")
	// Previously watched / saved dislikes cannot become seeds again.
	extra, _ := h.ratingRecommendationSeeds(context.Background(), "profile")
	extra = append(extra, personalizedSeed{MediaType: "movie", TMDBID: 101, Title: candidate.Name, Activity: time.Now(), Strength: .85, Source: "watchlist"})
	now := time.Now()
	seeds := buildPersonalizedSeeds("profile", nil, nil, now.AddDate(0, 0, -14), now, 12, extra...)
	for _, seed := range seeds {
		if seed.TMDBID == 101 {
			t.Fatal("dislike did not override list signal")
		}
	}
	second := request()
	if len(second.Items) != 0 {
		t.Fatal("cached recommendation survived explicit rejection")
	}
	_ = s.Put(context.Background(), "profile", candidate, "none")
	if len(request().Items) != 1 {
		t.Fatal("cleared dislike still excluded candidate")
	}
	repo.err = errors.New("synthetic storage outage")
	w := httptest.NewRecorder()
	h.GetPersonalizedRecommendations(w, httptest.NewRequest("GET", "/api/recommendations/personalized?userId=profile", nil))
	if w.Code != 503 || strings.Contains(w.Body.String(), repo.err.Error()) {
		t.Fatal("unavailable preferences silently ignored or internal error exposed")
	}
	// Values participate in the fingerprint even at the same timestamp.
	a := recommendationInputsFingerprint(extra, config.RecommendationSettings{}, personalizedKidsRatingFilter{}, "en")
	extra[0].Source = "rating_like"
	b := recommendationInputsFingerprint(extra, config.RecommendationSettings{}, personalizedKidsRatingFilter{}, "en")
	if a == b {
		t.Fatal("rating value missing from revision")
	}
}

func TestExplicitDislikeUsesProviderIdentityInsteadOfTitleTextAndOverridesAliasedHistory(t *testing.T) {
	now := time.Now()
	cutoff := now.AddDate(0, 0, -14)
	disliked := models.Title{ID: "tt0000007", MediaType: "movie", IMDBID: "tt0000007", Name: "Synthetic remake"}
	extra := []personalizedSeed{{MediaType: "movie", Title: disliked.Name, Strength: -1, Source: "rating_dislike", Identity: &disliked}}
	history := []models.WatchHistoryItem{{MediaType: "movie", ItemID: "tmdb:movie:7", Name: disliked.Name, Watched: true, WatchedAt: now, ExternalIDs: map[string]string{"tmdb": "7", "imdb": "tt0000007"}}}
	if seeds := buildPersonalizedSeeds("profile", history, nil, cutoff, now, 12, extra...); len(seeds) != 0 {
		t.Fatal("aliased dislike became a history seed")
	}
	// The original and a remake can have the same name. A dislike carries no
	// text-only suppression key; the provider identity is the preference target.
	disliked.Name = ""
	keys := titleExclusionKeys(disliked)
	remake := models.Title{ID: "tmdb:movie:8", TMDBID: 8, IMDBID: "tt0000008", MediaType: "movie", Name: "Synthetic remake"}
	if intersectsStringSet(titleExclusionKeys(remake), keys) {
		t.Fatal("dislike crossed into a different remake")
	}
	loved := models.Title{ID: "tmdb:movie:7", MediaType: "movie", TMDBID: 7, IMDBID: "tt0000007"}
	extra = []personalizedSeed{{MediaType: "movie", TMDBID: 7, Strength: 1.7, Activity: now.AddDate(-2, 0, 0), Source: "rating_love", Identity: &loved}}
	seeds := buildPersonalizedSeeds("profile", history, nil, cutoff, now, 12, extra...)
	if len(seeds) != 1 || seeds[0].Strength != 1.7 || seeds[0].Source != "rating_love" {
		t.Fatal("implicit activity weakened or obscured explicit preference")
	}
}

func TestSparseRatedMetadataRetainsVerifiedProviderAliasesInRecommendationInputs(t *testing.T) {
	h, _, repo, title := ratingFixture(t)
	sparse := models.Title{ID: "tt0000007", MediaType: "movie", IMDBID: "tt0000007", Name: "Synthetic"}
	repo.items["profile"] = []models.TitleRating{{Title: sparse, Value: "dislike", Aliases: []string{"movie:imdb:tt0000007", "movie:tmdb:7"}, UpdatedAt: time.Now()}}
	signals, err := h.ratingRecommendationSeeds(context.Background(), "profile")
	if err != nil || len(signals) != 1 || signals[0].TMDBID != 7 {
		t.Fatal("verified TMDB alias lost")
	}
	history := []models.WatchHistoryItem{{MediaType: "movie", ItemID: "tmdb:movie:7", Name: "Synthetic", Watched: true, WatchedAt: time.Now()}}
	now := time.Now()
	if len(buildPersonalizedSeeds("profile", history, nil, now.AddDate(0, 0, -14), now, 12, signals...)) != 0 {
		t.Fatal("sparse metadata lost explicit dislike")
	}
	title.Name = "Synthetic Seed"
	_ = h.TitleRatings.Put(context.Background(), "profile", title, "love")
	signals, err = h.ratingRecommendationSeeds(context.Background(), "profile")
	if err != nil || len(signals) != 2 {
		t.Fatal("other preference lost")
	}
}

func TestRatingRecommendationExplanationsNeverInventPlayback(t *testing.T) {
	for _, test := range []struct {
		name     string
		seeds    []personalizedSeed
		expected string
	}{
		{"rated only", []personalizedSeed{{Title: "Synthetic Favorite", Source: "rating_love"}}, "Because you liked Synthetic Favorite"},
		{"unnamed preference", []personalizedSeed{{Source: "rating_like"}}, "Based on your personal ratings"},
		{"mixed interests", []personalizedSeed{{Title: "Synthetic Favorite", Source: "rating_like"}, {Title: "Synthetic Saved", Source: "watchlist"}}, "Based on your interests in"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := buildPersonalizedExplanation(test.seeds, 14, false)
			if result == nil || !strings.HasPrefix(result.Summary, test.expected) || strings.Contains(strings.ToLower(result.Summary), "watched") {
				t.Fatal("unplayed preferences described as playback")
			}
		})
	}
}
