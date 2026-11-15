package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"novastream/internal/auth"
	"novastream/internal/profileaccess"
	"novastream/models"
)

type postPlayHistoryFake struct {
	historyServiceInterface
	item     *models.WatchHistoryItem
	err      error
	calls    int
	history  []models.WatchHistoryItem
	progress []models.PlaybackProgress
	listErr  error
}

func (h *postPlayHistoryFake) GetWatchHistoryItem(profile, kind, id string) (*models.WatchHistoryItem, error) {
	h.calls++
	return h.item, h.err
}

func (h *postPlayHistoryFake) ListWatchHistory(profile string) ([]models.WatchHistoryItem, error) {
	if h.history != nil {
		return h.history, h.listErr
	}
	if h.item != nil {
		return []models.WatchHistoryItem{*h.item}, h.listErr
	}
	return nil, h.listErr
}
func (h *postPlayHistoryFake) ListPlaybackProgress(profile string) ([]models.PlaybackProgress, error) {
	return h.progress, h.listErr
}
func (h *postPlayHistoryFake) ListSeriesStates(profile string) ([]models.SeriesWatchState, error) {
	return nil, h.listErr
}

func postPlayRequest(kind, id, itemID string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/users/profile/post-play/"+kind+"/"+id+"?itemId="+itemID, nil)
	r = mux.SetURLVars(r, map[string]string{"userID": "profile", "mediaType": kind, "titleID": id})
	r.Header.Set("X-Client-ID", "fixture-client")
	return r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, "account"))
}
func postPlayHTTP(h *MetadataHandler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	profileaccess.Middleware(func(profile, account string) bool { return profile == "profile" && account == "account" })(http.HandlerFunc(h.PostPlayContext)).ServeHTTP(w, r)
	return w
}

func TestPostPlayContextUsesOwnedPlayedHistoryAndExistingRating(t *testing.T) {
	h, s, repo, title := ratingFixture(t)
	s.Observe("profile", "fixture-client", []models.Title{title})
	if err := s.Put(context.Background(), "profile", title, "love"); err != nil {
		t.Fatal(err)
	}
	before := repo.writes
	history := &postPlayHistoryFake{item: &models.WatchHistoryItem{MediaType: "movie", ItemID: title.ID, Watched: true, Source: models.WatchSourcePlayed}}
	h.HistoryService = history
	w := postPlayHTTP(h, postPlayRequest("movie", title.ID, ""))
	if w.Code != 200 {
		t.Fatalf("post-play %d: %s", w.Code, w.Body.String())
	}
	var reply struct {
		Title      models.Title `json:"title"`
		Rating     string       `json:"rating"`
		Scope      string       `json:"ratingScope"`
		Completion string       `json:"completion"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Title.ID != title.ID || reply.Rating != "love" || reply.Scope != "movie" || reply.Completion != "movie" {
		t.Fatal("post-play title or existing rating lost")
	}
	if repo.writes != before || history.calls != 1 {
		t.Fatal("opening post-play wrote a rating or fetched unrelated history")
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("private completion may be cached")
	}
}

func TestPostPlayContextRejectsUnrecordedMarkedImportedAndWrongTitleHistory(t *testing.T) {
	for _, mode := range []string{"missing", "unwatched", "marked", "imported", "wrong title", "wrong kind", "history failure"} {
		t.Run(mode, func(t *testing.T) {
			h, s, repo, title := ratingFixture(t)
			s.Observe("profile", "fixture-client", []models.Title{title})
			item := &models.WatchHistoryItem{MediaType: "movie", ItemID: title.ID, Watched: true, Source: models.WatchSourcePlayed}
			history := &postPlayHistoryFake{item: item}
			want := 409
			switch mode {
			case "missing":
				history.item = nil
			case "unwatched":
				item.Watched = false
			case "marked":
				item.Source = models.WatchSourceMarked
			case "imported":
				item.Source = models.WatchSourceImported
			case "wrong title":
				item.ItemID = "tmdb:movie:2"
			case "wrong kind":
				item.MediaType = "episode"
			case "history failure":
				history.err = errors.New("private storage error")
				want = 503
			}
			h.HistoryService = history
			w := postPlayHTTP(h, postPlayRequest("movie", title.ID, ""))
			if w.Code != want || repo.writes != 0 {
				t.Fatalf("status %d, want %d, writes %d", w.Code, want, repo.writes)
			}
			if strings.Contains(w.Body.String(), "private storage") {
				t.Fatal("private diagnostic returned")
			}
		})
	}
}

func TestPostPlayContextRejectsForeignLockedRestrictedAndUndeliveredRequests(t *testing.T) {
	for _, mode := range []string{"foreign", "locked", "stream", "other device", "missing client", "unknown title", "child restriction"} {
		t.Run(mode, func(t *testing.T) {
			h, s, repo, title := ratingFixture(t)
			s.Observe("profile", "fixture-client", []models.Title{title})
			history := &postPlayHistoryFake{}
			h.HistoryService = history
			r := postPlayRequest("movie", title.ID, "")
			want := 403
			switch mode {
			case "foreign":
				r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, "other"))
				want = 404
			case "locked":
				r = profileaccess.WithResourceGuard(r, func(w http.ResponseWriter, _ *http.Request, _ string) bool {
					http.Error(w, "locked", 403)
					return false
				})
			case "stream":
				r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeySession, models.Session{Scope: models.SessionScopeStream}))
			case "other device":
				r.Header.Set("X-Client-ID", "other")
				want = 409
			case "missing client":
				r.Header.Del("X-Client-ID")
				want = 400
			case "unknown title":
				r = mux.SetURLVars(r, map[string]string{"userID": "profile", "mediaType": "movie", "titleID": "tmdb:movie:9"})
				want = 409
			case "child restriction":
				h.UsersService = fallbackProfiles{models.User{ID: "profile", AccountID: "account", IsKidsProfile: true, KidsMode: "rating", KidsMaxMovieRating: "G"}}
				want = 404
			}
			w := postPlayHTTP(h, r)
			if w.Code != want || repo.writes != 0 || history.calls != 0 {
				t.Fatalf("status %d want %d, premature history calls %d", w.Code, want, history.calls)
			}
		})
	}
}

func TestPostPlaySeriesRequiresCompletedEpisodeAndLabelsSeriesRating(t *testing.T) {
	h, s, repo, title := ratingFixture(t)
	title.ID = "tmdb:tv:7"
	title.TMDBID = 7
	title.MediaType = "series"
	s.Observe("profile", "fixture-client", []models.Title{title})
	h.HistoryService = &postPlayHistoryFake{item: &models.WatchHistoryItem{MediaType: "episode", ItemID: "synthetic-s1e10", SeriesID: title.ID, Watched: true, Source: models.WatchSourcePlayed}}
	if w := postPlayHTTP(h, postPlayRequest("series", title.ID, "")); w.Code != 400 {
		t.Fatal("missing episode accepted")
	}
	w := postPlayHTTP(h, postPlayRequest("series", title.ID, "synthetic-s1e10"))
	if w.Code != 200 {
		t.Fatalf("series %d: %s", w.Code, w.Body.String())
	}
	var value map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value["ratingScope"] != "series" || value["completion"] != "episode" || repo.writes != 0 {
		t.Fatal("episode was presented as a confirmed series ending or a separate season rating")
	}
}
