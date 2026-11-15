package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
)

// All non-cache methods deliberately remain nil: an accidental provider call
// fails this fixture instead of returning a convincing empty response.
type lovedCacheOnlyMetadata struct {
	metadataService
	artworkReads int
}

func (m *lovedCacheOnlyMetadata) GetCachedArtworkURLs(string, int64, int64) (string, string, []string) {
	m.artworkReads++
	return "synthetic-cached-poster", "synthetic-cached-backdrop", nil
}
func (*lovedCacheOnlyMetadata) GetCachedOverview(string, int64, int64) string {
	return "Synthetic cached overview"
}

type lovedReply struct {
	Source string                `json:"source"`
	Items  []models.TrendingItem `json:"items"`
	Total  int                   `json:"total"`
}

func lovedRequest(query string) *http.Request {
	r := httptest.NewRequest("GET", "/api/users/profile/display-list?source=loved&"+query, nil)
	r.Header.Set("X-Client-ID", "fixture-client")
	r = mux.SetURLVars(r, map[string]string{"userID": "profile"})
	return r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, "account"))
}

func lovedHTTP(t *testing.T, h *MetadataHandler, query string) lovedReply {
	t.Helper()
	w := httptest.NewRecorder()
	h.LovedTitles(w, lovedRequest(query))
	if w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("personal list status/cache: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
	var reply lovedReply
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	return reply
}

func TestLovedTitlesUsesOnlyOwnLoveRatingsWithoutChangingTheRepository(t *testing.T) {
	h, ratings, repo, title := ratingFixture(t)
	meta := &lovedCacheOnlyMetadata{}
	h.Service = meta
	for i, value := range []string{"love", "love", "like", "dislike"} {
		copy := title
		copy.ID, copy.TMDBID = fmt.Sprintf("tmdb:movie:%d", i+1), int64(i+1)
		copy.Name = fmt.Sprintf("Synthetic title %d", i+1)
		if err := ratings.Put(context.Background(), "profile", copy, value); err != nil {
			t.Fatal(err)
		}
		repo.items["profile"][i].UpdatedAt = time.Unix(int64(i), 0)
	}
	_ = ratings.Put(context.Background(), "other", title, "love")
	before := append([]models.TitleRating(nil), repo.items["profile"]...)
	writes := repo.writes
	reply := lovedHTTP(t, h, "userId=other")
	if reply.Source != "loved" || reply.Total != 2 || len(reply.Items) != 2 || reply.Items[0].Title.TMDBID != 2 {
		t.Fatal("wrong preference selection or newest-first order")
	}
	for _, item := range reply.Items {
		if item.Title.PersonalRating != "love" || item.Title.TextPoster == nil || item.Title.Overview == "" {
			t.Fatal("preference annotation or cache-only presentation missing")
		}
		if _, ok := ratings.Resolve("profile", "fixture-client", "movie", item.Title.ID); !ok {
			t.Fatal("returned title cannot be edited on this device")
		}
		if _, ok := ratings.Resolve("other", "fixture-client", "movie", item.Title.ID); ok {
			t.Fatal("forged query moved delivery to another profile")
		}
	}
	if !reflect.DeepEqual(before, repo.items["profile"]) || writes != repo.writes || meta.artworkReads != 2 {
		t.Fatal("read mutated preferences or fetched outside the returned page")
	}
}

func TestLovedTitlesFiltersAndPagesBeforeCacheOnlyArtwork(t *testing.T) {
	h, ratings, repo, title := ratingFixture(t)
	meta := &lovedCacheOnlyMetadata{}
	h.Service = meta
	for i := 0; i < 505; i++ {
		copy := title
		copy.ID, copy.TMDBID = fmt.Sprintf("tmdb:movie:%d", i+1), int64(i+1)
		copy.Name = fmt.Sprintf("Synthetic %03d", i)
		_ = ratings.Put(context.Background(), "profile", copy, "love")
		repo.items["profile"][i].UpdatedAt = time.Unix(0, 0)
	}
	reply := lovedHTTP(t, h, "limit=3&offset=2&sortBy=name&sortDirection=asc")
	if reply.Total != 500 || len(reply.Items) != 3 || reply.Items[0].Title.Name != "Synthetic 002" || meta.artworkReads != 3 {
		t.Fatal("bounded paging or deterministic sorting failed")
	}
	if got := lovedHTTP(t, h, "limit=3&offset=500"); got.Total != 500 || len(got.Items) != 0 {
		t.Fatal("page cap did not remain bounded")
	}
	if got := lovedHTTP(t, h, "filterMediaType=series"); got.Total != 0 || got.Items == nil {
		t.Fatal("kind filter or empty-array shape failed")
	}
	if got := lovedHTTP(t, h, "titleFilter=004"); got.Total != 1 || len(got.Items) != 1 {
		t.Fatal("title filter applied after the cap")
	}
}

func TestLovedTitlesRejectsForeignLockedAndLimitedSessionsBeforeReading(t *testing.T) {
	for _, mode := range []string{"foreign", "anonymous", "missing profile", "no client", "stream", "locked"} {
		t.Run(mode, func(t *testing.T) {
			h, _, repo, _ := ratingFixture(t)
			repo.err = errors.New("repository must not be read before authorization")
			r := lovedRequest("")
			want := 404
			switch mode {
			case "foreign":
				r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, "other"))
			case "anonymous":
				r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, ""))
			case "missing profile":
				h.UsersService = fallbackProfiles{}
			case "no client":
				r.Header.Del("X-Client-ID")
				want = 400
			case "stream":
				r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeySession, models.Session{Scope: models.SessionScopeStream}))
				want = 403
			case "locked":
				r = profileaccess.WithResourceGuard(r, func(w http.ResponseWriter, _ *http.Request, _ string) bool {
					http.Error(w, "locked", 403)
					return false
				})
				want = 403
			}
			w := httptest.NewRecorder()
			profileaccess.Middleware(func(profile, account string) bool { return profile == "profile" && account == "account" })(http.HandlerFunc(h.LovedTitles)).ServeHTTP(w, r)
			if w.Code != want || repo.writes != 0 {
				t.Fatalf("scope guard: status %d, want %d", w.Code, want)
			}
		})
	}
}

func TestLovedTitlesRechecksChangedChildCapsAndCuratedRestrictions(t *testing.T) {
	for _, mode := range []string{"allowed", "too mature", "unknown title", "adult", "no cap", "unknown cap", "curated"} {
		t.Run(mode, func(t *testing.T) {
			h, ratings, _, title := ratingFixture(t)
			user := models.User{ID: "profile", AccountID: "account", IsKidsProfile: true, KidsMode: "rating", KidsMaxMovieRating: "PG"}
			switch mode {
			case "too mature":
				user.KidsMaxMovieRating = "G"
			case "unknown title":
				title.Certification = ""
			case "adult":
				title.Adult = true
			case "no cap":
				user.KidsMaxMovieRating = ""
			case "unknown cap":
				user.KidsMaxMovieRating = "synthetic-unknown"
			case "curated":
				user.KidsMode = "content_list"
			}
			_ = ratings.Put(context.Background(), "profile", title, "love")
			h.UsersService = fallbackProfiles{user}
			w := httptest.NewRecorder()
			h.LovedTitles(w, lovedRequest(""))
			if mode == "allowed" {
				if w.Code != 200 || !strings.Contains(w.Body.String(), title.Name) {
					t.Fatal("allowed child favorite missing")
				}
			} else if w.Code != 200 && w.Code != 403 || strings.Contains(w.Body.String(), title.Name) {
				t.Fatalf("restricted favorite exposed: %d", w.Code)
			}
		})
	}
}

func TestLovedTitlesKeepsWatchedStateIndependentAndFailsClosedForAnUnavailableFilter(t *testing.T) {
	h, ratings, _, title := ratingFixture(t)
	_ = ratings.Put(context.Background(), "profile", title, "love")
	h.HistoryService = &fakeMetadataHistoryService{history: []models.WatchHistoryItem{{MediaType: "movie", ItemID: title.ID, Watched: true}}}
	if got := lovedHTTP(t, h, "watchStatus=complete"); got.Total != 1 || got.Items[0].Title.WatchState != "complete" {
		t.Fatal("existing watched state not reflected")
	}
	if got := lovedHTTP(t, h, "watchStatus=none"); got.Total != 0 {
		t.Fatal("watched favorite matched unwatched filter")
	}
	h.HistoryService = &fakeMetadataHistoryService{err: errors.New("private diagnostic")}
	w := httptest.NewRecorder()
	h.LovedTitles(w, lovedRequest("watchStatus=complete"))
	if w.Code != 503 || strings.Contains(w.Body.String(), "private diagnostic") {
		t.Fatal("unavailable state claimed a successful filtered result")
	}
}

func TestLovedTitlesDisplayListRetainsPrivateCacheAndHiddenFiltering(t *testing.T) {
	h, ratings, _, title := ratingFixture(t)
	for _, id := range []string{"hidden", "visible"} {
		copy := title
		copy.ID, copy.TMDBID = id, 0
		_ = ratings.Put(context.Background(), "profile", copy, "love")
	}
	display := NewDisplayListHandler(nil, nil, nil)
	display.MetadataHandler = h
	display.HiddenItemsService = paginationHiddenItemsService{}
	w := httptest.NewRecorder()
	display.Get(w, lovedRequest(""))
	var reply lovedReply
	_ = json.Unmarshal(w.Body.Bytes(), &reply)
	if w.Code != 200 || len(reply.Items) != 1 || reply.Items[0].Title.ID != "visible" || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("display-list lost profile caching or hidden-title restrictions")
	}
}

func TestLovedTitlesFailureDoesNotFallbackToAnotherProfileOrExposeDiagnostics(t *testing.T) {
	h, _, repo, _ := ratingFixture(t)
	repo.err = errors.New("synthetic private database details")
	w := httptest.NewRecorder()
	h.LovedTitles(w, lovedRequest(""))
	if w.Code != 503 || strings.Contains(w.Body.String(), "database details") || repo.writes != 0 {
		t.Fatal("preference failure did not remain private and read-only")
	}
}

func TestDisplayListDelegationPreservesPrivateSourceCacheDirectives(t *testing.T) {
	for _, control := range []string{"no-store", "private, no-store", "private, max-age=10"} {
		t.Run(control, func(t *testing.T) {
			display := NewDisplayListHandler(nil, nil, nil)
			w := httptest.NewRecorder()
			display.delegateMetadata(w, lovedRequest(""), "loved", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Cache-Control", control)
				_, _ = w.Write([]byte(`{"items":[],"total":0}`))
			}, url.Values{"userId": {"profile"}})
			if w.Code != 200 || w.Header().Get("Cache-Control") != control {
				t.Fatal("profile source cache directive was discarded")
			}
		})
	}
}
