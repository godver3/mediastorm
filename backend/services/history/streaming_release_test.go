package history

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"novastream/models"
)

func TestStreamingReleaseUsesStartOfAirDate(t *testing.T) {
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	date := time.Now().In(loc).Add(24 * time.Hour).Format("2006-01-02")
	day, _ := time.ParseInLocation("2006-01-02", date, loc)
	broadcast := day.Add(21*time.Hour + 15*time.Minute).UTC().Format(time.RFC3339)
	release := day.UTC().Format(time.RFC3339)

	for _, tc := range []struct {
		name, show, want string
	}{
		{"web channel without time", `{"id":73522,"network":null,"webChannel":{"id":173},"schedule":{"time":"","days":["Wednesday"]}}`, release},
		{"broadcast network", `{"id":1,"network":{"id":2},"webChannel":null,"schedule":{"time":"21:15"}}`, broadcast},
		{"network and web channel", `{"id":1,"network":{"id":2},"webChannel":{"id":173},"schedule":{"time":""}}`, broadcast},
		{"web channel with time", `{"id":1,"network":null,"webChannel":{"id":173},"schedule":{"time":"18:00"}}`, broadcast},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/lookup/shows" || r.URL.Query().Get("imdb") != "tt7579466" {
					t.Errorf("unexpected request: %v", r.URL)
				}
				fmt.Fprint(w, tc.show)
			}))
			defer server.Close()
			svc := &Service{metadataService: &airtimeMetadataService{tvdb: true}, airtimeClient: &tvmazeAirtimeClient{baseURL: server.URL, http: server.Client()}}
			title := models.Title{ID: "tvdb:series:345706", IMDBID: "tt7579466", AirsTime: "21:15", AirsTimezone: "America/Sao_Paulo"}
			for range 2 {
				ep := models.EpisodeReference{AirDate: date, AirDateTimeUTC: broadcast}
				svc.enrichContinueWatchingAirtime(context.Background(), title, nil, &ep)
				if ep.AirDateTimeUTC != tc.want || ep.AirTimeEstimated {
					t.Fatalf("got %+v, want airDateTimeUTC %s", ep, tc.want)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("made %d TVmaze calls, want 1 (cached)", calls.Load())
			}
		})
	}
}

func TestStreamingReleaseOutOfScopeMakesNoCalls(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	svc := &Service{metadataService: &airtimeMetadataService{tvdb: true}, airtimeClient: &tvmazeAirtimeClient{baseURL: server.URL, http: server.Client()}}
	tomorrow := time.Now().UTC().Add(24 * time.Hour).Format("2006-01-02")
	title := models.Title{ID: "tvdb:series:345706", IMDBID: "tt7579466", AirsTimezone: "America/Sao_Paulo"}
	for _, tc := range []struct {
		name  string
		title models.Title
		ep    models.EpisodeReference
	}{
		{"no timezone", models.Title{IMDBID: "tt7579466"}, models.EpisodeReference{AirDate: tomorrow, AirDateTimeUTC: tomorrow + "T23:00:00Z"}},
		{"no imdb", models.Title{AirsTimezone: "America/Sao_Paulo"}, models.EpisodeReference{AirDate: tomorrow, AirDateTimeUTC: tomorrow + "T23:00:00Z"}},
		{"far future", title, models.EpisodeReference{AirDate: "2999-01-01", AirDateTimeUTC: "2999-01-02T00:15:00Z"}},
		{"long aired", title, models.EpisodeReference{AirDate: "2000-01-01", AirDateTimeUTC: "2000-01-02T00:15:00Z"}},
		{"already midnight", title, models.EpisodeReference{AirDate: tomorrow, AirDateTimeUTC: tomorrow + "T03:00:00Z"}},
	} {
		ep := tc.ep
		svc.enrichContinueWatchingAirtime(context.Background(), tc.title, nil, &ep)
		if ep != tc.ep {
			t.Errorf("%s: episode changed to %+v", tc.name, ep)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("out-of-scope episodes made %d TVmaze calls", calls.Load())
	}
}
