package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"novastream/internal/auth"
	"novastream/internal/profileaccess"
	"novastream/models"
	"novastream/services/sourceavailability"
	"strings"
	"testing"
)

func recheckBody() map[string]any { return map[string]any{"titleId": "a", "mediaType": "movie"} }
func TestSourceRecheckEndpointOnlyAcceptsDeliveredTitleAndQueuesWithoutWork(t *testing.T) {
	h, s := titleEndpointFixture(t)
	body := recheckBody()
	w := httptest.NewRecorder()
	h.RecheckSource(w, episodeDemandHTTP(body))
	if w.Code != 409 {
		t.Fatal("unknown title accepted")
	}
	s.AnnotateTitles("profile", "fixture-client", []models.Title{{ID: "a", Name: "Synthetic", MediaType: "movie"}})
	for n := 0; n < 2; n++ {
		w = httptest.NewRecorder()
		h.RecheckSource(w, episodeDemandHTTP(body))
		if w.Code != 202 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("request failed %d %s", w.Code, w.Body.String())
		}
		var reply sourceavailability.RecheckResponse
		if json.Unmarshal(w.Body.Bytes(), &reply) != nil || reply.Status != "queued" {
			t.Fatal("invalid acceptance")
		}
	}
}
func TestSourceRecheckEndpointRejectsForgedInputs(t *testing.T) {
	for _, mode := range []string{"query", "url", "force", "numbering", "episode", "mediaType", "missing client", "trailing", "large"} {
		t.Run(mode, func(t *testing.T) {
			h, _ := titleEndpointFixture(t)
			body := recheckBody()
			switch mode {
			case "query", "url", "force":
				body[mode] = "forged"
			case "numbering":
				body[mode] = map[string]string{"seriesId": "tvdb:1"}
			case "episode":
				body[mode] = map[string]any{"episodeId": "e", "seasonNumber": 1, "episodeNumber": 1}
			case "mediaType":
				body[mode] = "person"
			}
			r := episodeDemandHTTP(body)
			if mode == "missing client" {
				r.Header.Del("X-Client-ID")
			}
			if mode == "trailing" {
				r.Body = ioBody(`{"titleId":"a","mediaType":"movie"}{}`)
			}
			if mode == "large" {
				r.Body = ioBody(`{"titleId":"` + strings.Repeat("x", 17000) + `","mediaType":"movie"}`)
			}
			w := httptest.NewRecorder()
			h.RecheckSource(w, r)
			if w.Code != 400 {
				t.Fatalf("forged input accepted: %d", w.Code)
			}
		})
	}
}
func ioBody(s string) *readClose { return &readClose{bytes.NewReader([]byte(s))} }

type readClose struct{ *bytes.Reader }

func (*readClose) Close() error { return nil }
func TestSourceRecheckEndpointHonorsOwnershipPinAndSessionScope(t *testing.T) {
	for _, mode := range []string{"foreign account", "locked profile", "stream scope"} {
		t.Run(mode, func(t *testing.T) {
			h, _ := titleEndpointFixture(t)
			r := episodeDemandHTTP(recheckBody())
			if mode == "locked profile" {
				r = profileaccess.WithResourceGuard(r, func(w http.ResponseWriter, _ *http.Request, _ string) bool {
					http.Error(w, "locked", 403)
					return false
				})
			}
			if mode == "stream scope" {
				r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeySession, models.Session{Scope: models.SessionScopeStream}))
			}
			w := httptest.NewRecorder()
			profileaccess.Middleware(func(string, string) bool { return mode != "foreign account" })(http.HandlerFunc(h.RecheckSource)).ServeHTTP(w, r)
			want := 403
			if mode == "foreign account" {
				want = 404
			}
			if w.Code != want {
				t.Fatalf("guard bypass %d", w.Code)
			}
		})
	}
}
func TestSourceRecheckEndpointKidsPolicyRechecked(t *testing.T) {
	for _, rating := range []string{"G", "R", ""} {
		h, s := titleEndpointFixture(t)
		h.UsersService = fallbackProfiles{models.User{ID: "profile", AccountID: "account", IsKidsProfile: true, KidsMode: "rating", KidsMaxMovieRating: "PG"}}
		s.AnnotateTitles("profile", "fixture-client", []models.Title{{ID: "a", Name: "Synthetic", MediaType: "movie", Certification: rating}})
		w := httptest.NewRecorder()
		h.RecheckSource(w, episodeDemandHTTP(recheckBody()))
		want := 404
		if rating == "G" {
			want = 202
		}
		if w.Code != want {
			t.Fatalf("rating %q status %d", rating, w.Code)
		}
	}
}

type recheckEndpointFake struct {
	episodeEndpointService
	err   error
	calls int
	ep    *models.SeriesEpisode
}

func (s *recheckEndpointFake) ResolveDeliveredTitles(string, string, []sourceavailability.TitleCoordinate) []models.Title {
	return []models.Title{{ID: "a", Name: "Synthetic", MediaType: "movie"}}
}
func (s *recheckEndpointFake) RequestRecheck(_ string, _ string, title models.Title, ep *models.SeriesEpisode) (sourceavailability.RecheckResponse, error) {
	s.calls++
	s.title = title
	s.ep = ep
	return sourceavailability.RecheckResponse{Status: "queued", RetryAfterSeconds: 27}, s.err
}
func TestSourceRecheckEndpointRetryAfterAndInternalErrorPrivacy(t *testing.T) {
	for _, failure := range []error{sourceavailability.ErrRecheckCooldown, sourceavailability.ErrRecheckCapacity, sourceavailability.ErrRecheckDisabled, errors.New("private internal filename")} {
		h, _, _ := episodeEndpointFixture()
		s := &recheckEndpointFake{episodeEndpointService: episodeEndpointService{enabled: true}, err: failure}
		h.SourceAvailability = s
		w := httptest.NewRecorder()
		h.RecheckSource(w, episodeDemandHTTP(recheckBody()))
		want := 400
		if failure == sourceavailability.ErrRecheckDisabled {
			want = 409
		}
		if failure == sourceavailability.ErrRecheckCooldown || failure == sourceavailability.ErrRecheckCapacity {
			want = 429
			if w.Header().Get("Retry-After") != "27" {
				t.Fatal("retry header missing")
			}
		}
		if w.Code != want || strings.Contains(w.Body.String(), "private internal") {
			t.Fatal("wrong failure response or private error leaked")
		}
	}
}
func TestSourceRecheckEpisodeUsesExactServerCoordinatesAndRejectsForgery(t *testing.T) {
	for _, forged := range []bool{false, true} {
		h, _, _ := episodeEndpointFixture()
		s := &recheckEndpointFake{episodeEndpointService: episodeEndpointService{enabled: true}}
		h.SourceAvailability = s
		body := episodeDemandBody()
		body["mediaType"] = "series"
		body["episode"] = body["episodes"].([]map[string]any)[0]
		delete(body, "episodes")
		delete(body, "version")
		if forged {
			body["episode"].(map[string]any)["episodeNumber"] = 2
		}
		w := httptest.NewRecorder()
		h.RecheckSource(w, episodeDemandHTTP(body))
		if forged {
			if w.Code < 400 || s.calls != 0 {
				t.Fatal("forged episode accepted")
			}
		} else if w.Code != 202 || s.title.ID != "tmdb:series:456" || s.ep.AiredDate != "2020-01-01" || s.ep.Numbering.SeriesID != "tvdb:123" {
			t.Fatalf("server identity lost %d", w.Code)
		}
	}
}
func TestSourceRecheckCanceledRequestDoesNotQueue(t *testing.T) {
	h, _, _ := episodeEndpointFixture()
	s := &recheckEndpointFake{episodeEndpointService: episodeEndpointService{enabled: true}}
	h.SourceAvailability = s
	r := episodeDemandHTTP(recheckBody())
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	r = r.WithContext(ctx)
	h.RecheckSource(httptest.NewRecorder(), r)
	if s.calls != 0 {
		t.Fatal("canceled request queued work")
	}
}
