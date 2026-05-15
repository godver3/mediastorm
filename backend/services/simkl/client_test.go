package simkl

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestScrobbleStartSendsHeadersAndBody(t *testing.T) {
	client := NewClient()
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/scrobble/start" {
				t.Fatalf("path = %q, want /scrobble/start", r.URL.Path)
			}
			if got := r.URL.Query().Get("client_id"); got != "client-id" {
				t.Fatalf("client_id query = %q", got)
			}
			if got := r.URL.Query().Get("app-name"); got != appName {
				t.Fatalf("app-name query = %q", got)
			}
			if got := r.URL.Query().Get("app-version"); got != appVersion {
				t.Fatalf("app-version query = %q", got)
			}
			if got := r.Header.Get("simkl-api-key"); got != "client-id" {
				t.Fatalf("simkl-api-key = %q", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer token" {
				t.Fatalf("Authorization = %q", got)
			}
			if got := r.Header.Get("User-Agent"); got != userAgent {
				t.Fatalf("User-Agent = %q", got)
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"progress":12.5`) {
				t.Fatalf("body missing progress: %s", string(body))
			}
			if !strings.Contains(string(body), `"imdb":"tt1375666"`) {
				t.Fatalf("body missing imdb id: %s", string(body))
			}
			return &http.Response{
				StatusCode: http.StatusCreated,
				Body:       io.NopCloser(strings.NewReader(`{"id":0,"action":"start","progress":12.5}`)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	resp, err := client.ScrobbleStart("client-id", "token", ScrobbleRequest{
		Progress: 12.5,
		Movie: &Movie{
			Title: "Inception",
			Year:  2010,
			IDs:   IDs{IMDB: "tt1375666", TMDB: 27205},
		},
	})
	if err != nil {
		t.Fatalf("ScrobbleStart() error = %v", err)
	}
	if resp.Action != "start" || resp.Progress != 12.5 {
		t.Fatalf("response = %+v", resp)
	}
}

func TestExchangeCodeSendsRequiredQueryAndHeaders(t *testing.T) {
	client := NewClient()
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/oauth/token" {
				t.Fatalf("path = %q, want /oauth/token", r.URL.Path)
			}
			if got := r.URL.Query().Get("client_id"); got != "client-id" {
				t.Fatalf("client_id query = %q", got)
			}
			if got := r.Header.Get("simkl-api-key"); got != "client-id" {
				t.Fatalf("simkl-api-key = %q", got)
			}
			if got := r.Header.Get("User-Agent"); got != userAgent {
				t.Fatalf("User-Agent = %q", got)
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"redirect_uri":"urn:ietf:wg:oauth:2.0:oob"`) {
				t.Fatalf("body missing redirect uri: %s", string(body))
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"token","token_type":"bearer"}`)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	token, err := client.ExchangeCode("client-id", "secret", "urn:ietf:wg:oauth:2.0:oob", "code")
	if err != nil {
		t.Fatalf("ExchangeCode() error = %v", err)
	}
	if token.AccessToken != "token" {
		t.Fatalf("access token = %q", token.AccessToken)
	}
}

func TestStartPINAuthSendsRequiredQueryAndHeaders(t *testing.T) {
	client := NewClient()
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/oauth/pin" {
				t.Fatalf("path = %q, want /oauth/pin", r.URL.Path)
			}
			if got := r.URL.Query().Get("client_id"); got != "client-id" {
				t.Fatalf("client_id query = %q", got)
			}
			if got := r.URL.Query().Get("app-name"); got != appName {
				t.Fatalf("app-name query = %q", got)
			}
			if got := r.URL.Query().Get("app-version"); got != appVersion {
				t.Fatalf("app-version query = %q", got)
			}
			if got := r.Header.Get("simkl-api-key"); got != "client-id" {
				t.Fatalf("simkl-api-key = %q", got)
			}
			if got := r.Header.Get("User-Agent"); got != userAgent {
				t.Fatalf("User-Agent = %q", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"user_code":"ABCD","verification_url":"https://simkl.com/pin/ABCD","expires_in":600,"interval":5}`)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	pin, err := client.StartPINAuth("client-id")
	if err != nil {
		t.Fatalf("StartPINAuth() error = %v", err)
	}
	if pin.UserCode != "ABCD" || pin.VerificationURL != "https://simkl.com/pin/ABCD" || pin.Interval != 5 {
		t.Fatalf("pin response = %+v", pin)
	}
}

func TestCheckPINAuthSendsRequiredQueryAndHeaders(t *testing.T) {
	client := NewClient()
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/oauth/pin/ABCD" {
				t.Fatalf("path = %q, want /oauth/pin/ABCD", r.URL.Path)
			}
			if got := r.URL.Query().Get("client_id"); got != "client-id" {
				t.Fatalf("client_id query = %q", got)
			}
			if got := r.URL.Query().Get("app-name"); got != appName {
				t.Fatalf("app-name query = %q", got)
			}
			if got := r.URL.Query().Get("app-version"); got != appVersion {
				t.Fatalf("app-version query = %q", got)
			}
			if got := r.Header.Get("simkl-api-key"); got != "client-id" {
				t.Fatalf("simkl-api-key = %q", got)
			}
			if got := r.Header.Get("User-Agent"); got != userAgent {
				t.Fatalf("User-Agent = %q", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"token","user_id":"simkl-user"}`)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	token, err := client.CheckPINAuth("client-id", "ABCD")
	if err != nil {
		t.Fatalf("CheckPINAuth() error = %v", err)
	}
	if token.AccessToken != "token" || token.UserID != "simkl-user" {
		t.Fatalf("pin token response = %+v", token)
	}
}

func TestGetActivitiesSendsRequiredQueryAndHeaders(t *testing.T) {
	client := NewClient()
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/sync/activities" {
				t.Fatalf("path = %q, want /sync/activities", r.URL.Path)
			}
			if got := r.URL.Query().Get("client_id"); got != "client-id" {
				t.Fatalf("client_id query = %q", got)
			}
			if got := r.URL.Query().Get("app-name"); got != appName {
				t.Fatalf("app-name query = %q", got)
			}
			if got := r.URL.Query().Get("app-version"); got != appVersion {
				t.Fatalf("app-version query = %q", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer token" {
				t.Fatalf("Authorization = %q", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"all":"2026-05-15T12:00:00Z"}`)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	activities, err := client.GetActivities("client-id", "token")
	if err != nil {
		t.Fatalf("GetActivities() error = %v", err)
	}
	if activities["all"] != "2026-05-15T12:00:00Z" {
		t.Fatalf("activities = %+v", activities)
	}
}

func TestGetInitialSyncItemsAcceptsArrayResponses(t *testing.T) {
	client := NewClient()
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/sync/movies" {
				t.Fatalf("path = %q, want /sync/movies", r.URL.Path)
			}
			if got := r.URL.Query().Get("extended"); got != "full" {
				t.Fatalf("extended query = %q", got)
			}
			if got := r.URL.Query().Get("episode_watched_at"); got != "yes" {
				t.Fatalf("episode_watched_at query = %q", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`[{"movie":{"title":"Inception","ids":{"imdb":"tt1375666"}}}]`)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	items, err := client.GetInitialSyncItems("client-id", "token", "movies")
	if err != nil {
		t.Fatalf("GetInitialSyncItems() error = %v", err)
	}
	if len(items.Movies) != 1 {
		t.Fatalf("movies len = %d, want 1", len(items.Movies))
	}
}

func TestGetAllItemsSinceSendsDateFrom(t *testing.T) {
	client := NewClient()
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/sync/all-items" {
				t.Fatalf("path = %q, want /sync/all-items", r.URL.Path)
			}
			if got := r.URL.Query().Get("date_from"); got != "2026-05-15T12:00:00Z" {
				t.Fatalf("date_from query = %q", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"movies":[],"shows":[],"anime":[]}`)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	if _, err := client.GetAllItemsSince("client-id", "token", "2026-05-15T12:00:00Z"); err != nil {
		t.Fatalf("GetAllItemsSince() error = %v", err)
	}
}

func TestBuildScrobbleRequestEpisodeUsesShowAndEpisode(t *testing.T) {
	req := BuildScrobbleRequest(testEpisodeUpdate(), 45.123)
	if req.Progress != 45.12 {
		t.Fatalf("progress = %v, want 45.12", req.Progress)
	}
	if req.Show == nil || req.Show.IDs.TVDB != 153021 || req.Show.IDs.IMDB != "tt1520211" {
		t.Fatalf("show ids = %+v", req.Show)
	}
	if req.Episode == nil || req.Episode.Season != 1 || req.Episode.Number != 3 {
		t.Fatalf("episode = %+v", req.Episode)
	}
}
