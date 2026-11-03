package metadata

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"novastream/models"
)

const seasonBanner = "https://artworks.thetvdb.com/banners/v4/season/6527/banners/64ddac2483fe8.jpg"
const seriesPoster = "https://artworks.thetvdb.com/banners/posters/73838.jpg"

func TestSeasonPosterSelection(t *testing.T) {
	fallback := &models.Image{URL: seriesPoster, Type: "poster"}
	for _, source := range []string{seasonBanner, "https://artworks.thetvdb.com/banners/seasonwide/73838-1.jpg", ""} {
		got := tvdbSeasonPoster(source, fallback)
		if got == nil || got.URL != fallback.URL || got == fallback {
			t.Fatalf("source %q: want independent series-poster fallback, got %+v", source, got)
		}
	}
	valid := "https://artworks.thetvdb.com/banners/seasons/73838-1.jpg"
	if got := tvdbSeasonPoster(valid, fallback); got == nil || got.URL != valid {
		t.Fatalf("portrait season poster was not preserved: %+v", got)
	}
	if isPortraitSeasonImage(&models.Image{URL: "https://img.example/image.jpg", Width: 400, Height: 73}) {
		t.Fatal("landscape dimensions accepted as a poster")
	}
}

func TestSeasonPostersBuiltAndCachedWithoutTMDB(t *testing.T) {
	for _, ordering := range []string{"official", "dvd", "absolute"} {
		for _, lite := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/lite=%v", ordering, lite), func(t *testing.T) {
				cache := newFileCache(t.TempDir(), 24)
				var calls atomic.Int32
				httpc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls.Add(1)
					body := `{"data":{}}`
					switch {
					case r.URL.Path == "/v4/login":
						body = `{"data":{"token":"test-token"}}`
					case strings.HasSuffix(r.URL.Path, "/extended"):
						body = fmt.Sprintf(`{"data":{"id":73838,"name":"Malcolm in the Middle","poster":%q,"fanart":"https://img.example/backdrop.jpg","genres":[{"name":"Comedy"}],"seasons":[{"id":6527,"number":1,"type":{"id":1,"type":%q,"name":"Test ordering"},"image":%q}],"episodes":[{"id":1,"seasonNumber":1,"number":1,"name":"Pilot","aired":"2000-01-09"}]}}`, seriesPoster, ordering, seasonBanner)
					case r.URL.Path == "/v4/series/73838":
						body = `{"data":{"id":73838,"name":"Malcolm in the Middle"}}`
					case strings.Contains(r.URL.Path, "/episodes/"):
						body = `{"data":{"episodes":[{"id":1,"seasonNumber":1,"number":1,"name":"Pilot","aired":"2000-01-09"}]}}`
					case strings.Contains(r.URL.Path, "/seasons/"):
						body = `{"data":{"name":"First year"}}`
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
				})}
				s := &Service{client: newTVDBClient("key", "eng", httpc, 24), cache: cache, idCache: newFileCache(t.TempDir(), 24), inflightRequests: make(map[string]*inflightRequest)}
				s.client.minInterval = 0
				// A pre-fix cache entry must never be returned by any new details path.
				requestedOrdering := ordering
				if ordering == "official" {
					requestedOrdering = ""
				}
				orderingKey := ordering
				if requestedOrdering == "" {
					orderingKey = "default"
				}
				oldKey := cacheKey("tvdb", "series", "details", "v17", "eng", "73838", orderingKey)
				if lite {
					oldKey = cacheKey("tvdb", "series", "details", "v16-lite", "eng", "73838", orderingKey)
				}
				old := models.SeriesDetails{Title: models.Title{Name: "stale", TVDBID: 73838}, Seasons: []models.SeriesSeason{{Number: 1, Image: &models.Image{URL: seasonBanner}}}}
				if err := cache.set(oldKey, old); err != nil {
					t.Fatal(err)
				}
				fetch := s.SeriesDetails
				key := seriesDetailsCacheKey("eng", 73838, requestedOrdering)
				if lite {
					fetch = s.SeriesDetailsLite
					key = seriesDetailsLiteCacheKey("eng", 73838, requestedOrdering)
				}
				query := models.SeriesDetailsQuery{TitleID: "tvdb:series:73838", TVDBID: 73838, Name: "Malcolm in the Middle", SeasonType: requestedOrdering}
				got, err := fetch(t.Context(), query)
				if err != nil {
					t.Fatal(err)
				}
				if got.Title.Name == "stale" || got.Seasons[0].Image == nil || got.Seasons[0].Image.URL != seriesPoster {
					t.Fatalf("fresh construction did not replace banner: %+v", got)
				}
				if got.ActiveOrdering != ordering {
					t.Fatalf("wanted %s ordering, got %s", ordering, got.ActiveOrdering)
				}
				var saved models.SeriesDetails
				if ok, _ := cache.get(key, &saved); !ok || saved.Seasons[0].Image.URL != seriesPoster {
					t.Fatal("selected poster was not persisted")
				}
				before := calls.Load()
				if _, err := fetch(t.Context(), query); err != nil {
					t.Fatal(err)
				}
				if got := calls.Load(); got != before {
					t.Fatalf("cache hit made %d new requests", got-before)
				}
				// Batch reads share the corrected full-details key and do no repair/fetch.
				if !lite {
					items := s.BatchSeriesDetails(t.Context(), []models.SeriesDetailsQuery{query})
					if items[0].Details == nil || items[0].Details.Seasons[0].Image.URL != seriesPoster || calls.Load() != before {
						t.Fatal("batch cache path lost corrected poster or made an HTTP request")
					}
				}
			})
		}
	}
}

func TestSeasonPosterReusesExistingEpisodeResponse(t *testing.T) {
	for _, ordering := range []string{"official", "dvd", "absolute"} {
		t.Run(ordering, func(t *testing.T) {
			var calls atomic.Int32
			httpc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				if r.URL.Path != "/3/tv/2004/season/1" {
					t.Errorf("extra poster request: %s", r.URL.Path)
				}
				body := `{"id":1,"season_number":1,"poster_path":"/season-one.jpg","episodes":[]}`
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			s := &Service{tmdb: newTMDBClient("key", "eng", httpc, nil)}
			details := models.SeriesDetails{ActiveOrdering: ordering, Title: models.Title{Poster: &models.Image{URL: seriesPoster}}, Seasons: []models.SeriesSeason{{Number: 1, Image: &models.Image{URL: seriesPoster}, Episodes: []models.SeriesEpisode{{SeasonNumber: 1, EpisodeNumber: 1}}}}}
			changed, complete := s.enrichTMDBEpisodeMetadata(t.Context(), &details, 2004)
			wantChanged := ordering == "official"
			if !complete || changed != wantChanged || calls.Load() != 1 {
				t.Fatalf("changed=%v complete=%v calls=%d", changed, complete, calls.Load())
			}
			if ordering == "official" && !strings.HasSuffix(details.Seasons[0].Image.URL, "/season-one.jpg") {
				t.Fatal("existing TMDB response poster not reused")
			}
			if ordering != "official" && details.Seasons[0].Image.URL != seriesPoster {
				t.Fatal("aired-season poster leaked into alternate ordering")
			}
			if details.Seasons[0].Episodes[0].EpisodeNumber != 1 {
				t.Fatal("poster changed episode numbering")
			}
		})
	}
}
