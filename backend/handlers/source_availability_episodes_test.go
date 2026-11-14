package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"novastream/internal/auth"
	"novastream/internal/profileaccess"
	"novastream/models"
	"novastream/services/sourceavailability"
)

type episodeEndpointService struct {
	availabilityPresentationFake
	enabled  bool
	calls    int
	title    models.Title
	episodes []models.SeriesEpisode
	version  int64
}

func (s *episodeEndpointService) EpisodeHintsEnabled() bool { return s.enabled }
func (s *episodeEndpointService) ReplaceEpisodeView(profile, client, view string, version int64, title models.Title, episodes []models.SeriesEpisode) (sourceavailability.EpisodeViewResponse, error) {
	s.calls++
	s.title = title
	s.episodes = episodes
	s.version = version
	return sourceavailability.EpisodeViewResponse{Enabled: s.enabled, Version: version, Items: []sourceavailability.EpisodeHint{}}, nil
}
func episodeEndpointFixture() (*MetadataHandler, *fakeMetadataService, *episodeEndpointService) {
	ep := models.SeriesEpisode{ID: "ep-1", SeasonNumber: 1, EpisodeNumber: 1, AiredDate: "2020-01-01", Numbering: &models.EpisodeNumbering{SeriesID: "tvdb:123", Ordering: "official"}}
	meta := &fakeMetadataService{seriesResp: &models.SeriesDetails{Title: models.Title{ID: "tvdb:123", Name: "Synthetic Series", MediaType: "series"}, Seasons: []models.SeriesSeason{{Number: 1, Episodes: []models.SeriesEpisode{ep}}}}}
	service := &episodeEndpointService{enabled: true}
	return &MetadataHandler{Service: meta, SourceAvailability: service, UsersService: fallbackProfiles{models.User{ID: "profile", AccountID: "account"}}}, meta, service
}
func episodeDemandHTTP(body any) *http.Request {
	data, _ := json.Marshal(body)
	r := httptest.NewRequest("PUT", "/api/users/profile/source-availability/episodes/view-1", bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Client-ID", "fixture-client")
	r = mux.SetURLVars(r, map[string]string{"userID": "profile", "viewID": "view-1"})
	return r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, "account"))
}
func episodeDemandBody() map[string]any {
	return map[string]any{"version": 1, "titleId": "tmdb:series:456", "numbering": map[string]any{"seriesId": "tvdb:123", "ordering": "official"}, "episodes": []map[string]any{{"episodeId": "ep-1", "seasonNumber": 1, "episodeNumber": 1}}}
}
func TestEpisodeHintsEndpointUsesServerMetadataAndSelectedTitleIdentity(t *testing.T) {
	h, metadata, service := episodeEndpointFixture()
	w := httptest.NewRecorder()
	h.EpisodeSourceHints(w, episodeDemandHTTP(episodeDemandBody()))
	if w.Code != 200 || service.calls != 1 || service.title.Name != "Synthetic Series" || service.title.ID != "tmdb:series:456" || service.episodes[0].AiredDate != "2020-01-01" {
		t.Fatalf("wrong verified request: status=%d", w.Code)
	}
	if metadata.lastSeriesQuery.TitleID != "tmdb:series:456" || metadata.lastSeriesQuery.SeasonType != "official" {
		t.Fatal("metadata query lost identity")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("profile response may enter a shared cache")
	}
}
func TestEpisodeHintsEndpointRejectsForgedCoordinatesAndLabels(t *testing.T) {
	for _, mode := range []string{"title label", "wrong episode", "wrong ordering", "duplicate", "missing client", "invalid view", "missing numbering", "foreign profile"} {
		t.Run(mode, func(t *testing.T) {
			h, _, service := episodeEndpointFixture()
			body := episodeDemandBody()
			switch mode {
			case "title label":
				body["query"] = "Forged series"
			case "wrong episode":
				body["episodes"] = []map[string]any{{"episodeId": "ep-1", "seasonNumber": 1, "episodeNumber": 2}}
			case "wrong ordering":
				body["numbering"] = map[string]any{"seriesId": "tmdb:series:123", "ordering": "dvd"}
			case "duplicate":
				body["episodes"] = []map[string]any{{"episodeId": "ep-1", "seasonNumber": 1, "episodeNumber": 1}, {"episodeId": "ep-1", "seasonNumber": 1, "episodeNumber": 1}}
			case "missing numbering":
				delete(body, "numbering")
			}
			r := episodeDemandHTTP(body)
			if mode == "missing client" {
				r.Header.Del("X-Client-ID")
			}
			if mode == "invalid view" {
				r = mux.SetURLVars(r, map[string]string{"userID": "profile", "viewID": "../foreign"})
			}
			if mode == "foreign profile" {
				r = mux.SetURLVars(r, map[string]string{"userID": "another-profile", "viewID": "view-1"})
			}
			w := httptest.NewRecorder()
			h.EpisodeSourceHints(w, r)
			if w.Code < 400 || service.calls != 0 {
				t.Fatalf("forged demand accepted: %d", w.Code)
			}
		})
	}
}
func TestEpisodeHintsEndpointProfileGuardAndScopedSessionsStopBeforeMetadata(t *testing.T) {
	for _, mode := range []string{"foreign account", "locked profile", "stream scope"} {
		t.Run(mode, func(t *testing.T) {
			h, metadata, service := episodeEndpointFixture()
			r := episodeDemandHTTP(episodeDemandBody())
			w := httptest.NewRecorder()
			if mode == "locked profile" {
				r = profileaccess.WithResourceGuard(r, func(w http.ResponseWriter, _ *http.Request, _ string) bool {
					http.Error(w, "locked", 403)
					return false
				})
			}
			if mode == "stream scope" {
				r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeySession, models.Session{Scope: models.SessionScopeStream, ScopeResource: "/fixture/movie"}))
			}
			allowed := mode != "foreign account"
			profileaccess.Middleware(func(string, string) bool { return allowed })(http.HandlerFunc(h.EpisodeSourceHints)).ServeHTTP(w, r)
			if w.Code < 400 || service.calls != 0 || metadata.lastSeriesQuery.TitleID != "" {
				t.Fatalf("authorization bypass: status=%d", w.Code)
			}
		})
	}
}
func TestEpisodeHintsEndpointDisabledAndClearNeedNoMetadata(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "clear"}[enabled], func(t *testing.T) {
			h, metadata, service := episodeEndpointFixture()
			service.enabled = enabled
			body := episodeDemandBody()
			if enabled {
				body = map[string]any{"version": 2, "episodes": []any{}}
			}
			w := httptest.NewRecorder()
			h.EpisodeSourceHints(w, episodeDemandHTTP(body))
			if w.Code != 200 || metadata.lastSeriesQuery.TitleID != "" {
				t.Fatal("disabled/clear request loaded metadata")
			}
			if enabled && (service.calls != 1 || len(service.episodes) != 0) {
				t.Fatal("clear did not release the view")
			}
		})
	}
}
func TestEpisodeHintsEndpointKidsWithoutEstablishedAuthorizationStayClosed(t *testing.T) {
	for _, mode := range []string{"lists", "rating"} {
		t.Run(mode, func(t *testing.T) {
			h, _, service := episodeEndpointFixture()
			h.UsersService = fallbackProfiles{models.User{ID: "profile", AccountID: "account", IsKidsProfile: true, KidsMode: mode}}
			w := httptest.NewRecorder()
			h.EpisodeSourceHints(w, episodeDemandHTTP(episodeDemandBody()))
			if w.Code != 404 || service.calls != 0 {
				t.Fatal("unverified child authorization created demand")
			}
		})
	}
}

func TestEpisodeHintsEndpointRequiresAnExplicitEpisodeArray(t *testing.T) {
	for _, body := range []map[string]any{{"version": 1}, {"version": 1, "episodes": nil}} {
		h, _, service := episodeEndpointFixture()
		w := httptest.NewRecorder()
		h.EpisodeSourceHints(w, episodeDemandHTTP(body))
		if w.Code != 400 || service.calls != 0 {
			t.Fatal("missing release array accepted")
		}
	}
}

func TestEpisodeHintsEndpointDailyDateRequiresCompleteUniqueMetadata(t *testing.T) {
	for _, mode := range []string{"unique", "duplicate date", "incomplete season"} {
		t.Run(mode, func(t *testing.T) {
			h, meta, service := episodeEndpointFixture()
			meta.seriesResp.Title.IsDaily = true
			meta.seriesResp.Seasons[0].EpisodeCount = 1
			if mode == "duplicate date" {
				copy := meta.seriesResp.Seasons[0].Episodes[0]
				copy.ID = "ep-2"
				copy.EpisodeNumber = 2
				meta.seriesResp.Seasons[0].Episodes = append(meta.seriesResp.Seasons[0].Episodes, copy)
				meta.seriesResp.Seasons[0].EpisodeCount = 2
			}
			if mode == "incomplete season" {
				meta.seriesResp.Seasons[0].EpisodeCount = 2
			}
			w := httptest.NewRecorder()
			h.EpisodeSourceHints(w, episodeDemandHTTP(episodeDemandBody()))
			if w.Code != 200 {
				t.Fatalf("status %d", w.Code)
			}
			if service.episodes[0].SourceDateUnique != (mode == "unique") {
				t.Fatal("daily uniqueness was assumed from incomplete metadata")
			}
		})
	}
}
