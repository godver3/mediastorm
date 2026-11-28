package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"novastream/models"
)

const kidsTestList = "https://mdblist.com/lists/someone/kids-picks"

func kidsTestHandler(t *testing.T, fake *fakeMetadataService) *MetadataHandler {
	t.Helper()
	h := NewMetadataHandler(fake, testConfigManager(t))
	h.SetUsersService(&fakeUsersServiceForSearch{users: map[string]models.User{
		"adult":   {ID: "adult"},
		"kid":     {ID: "kid", IsKidsProfile: true, KidsMode: "rating", KidsMaxMovieRating: "PG", KidsMaxTVRating: "TV-PG"},
		"curated": {ID: "curated", IsKidsProfile: true, KidsMode: "content_list", KidsAllowedLists: []string{kidsTestList}},
	}})
	return h
}

func TestMovieDetailsKidsRatingGate(t *testing.T) {
	cases := []struct {
		user, cert string
		want       int
	}{
		{"kid", "R", http.StatusForbidden},
		{"kid", "", http.StatusForbidden}, // unknown rating is fail-closed
		{"kid", "G", http.StatusOK},
		{"adult", "R", http.StatusOK},
		{"", "R", http.StatusOK},
	}
	for _, tc := range cases {
		fake := &fakeMetadataService{movieResp: &models.Title{ID: "tmdb:movie:1", TMDBID: 1, MediaType: "movie", Certification: tc.cert}}
		h := kidsTestHandler(t, fake)
		rec := httptest.NewRecorder()
		h.MovieDetails(rec, httptest.NewRequest(http.MethodGet, "/api/metadata/movies/details?tmdbId=1&userId="+tc.user, nil))
		if rec.Code != tc.want {
			t.Errorf("user=%q cert=%q: got %d, want %d", tc.user, tc.cert, rec.Code, tc.want)
		}
		if tc.want == http.StatusForbidden {
			var body map[string]string
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if body["code"] != "kids_title_restricted" {
				t.Errorf("missing error code: %s", rec.Body.String())
			}
		}
	}
}

func TestSeriesDetailsCuratedKidsGate(t *testing.T) {
	fake := &fakeMetadataService{
		customListResp: []models.TrendingItem{{Title: models.Title{ID: "tmdb:tv:7", TMDBID: 7, MediaType: "series"}}},
	}
	h := kidsTestHandler(t, fake)

	fake.seriesResp = &models.SeriesDetails{Title: models.Title{ID: "tmdb:tv:7", TMDBID: 7, MediaType: "series"}}
	rec := httptest.NewRecorder()
	h.SeriesDetails(rec, httptest.NewRequest(http.MethodGet, "/api/metadata/series/details?tmdbId=7&userId=curated", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("listed title: got %d", rec.Code)
	}
	if fake.lastCustomListURL != kidsTestList+"/json" {
		t.Fatalf("allowed list not consulted, got %q", fake.lastCustomListURL)
	}

	fake.seriesResp = &models.SeriesDetails{Title: models.Title{ID: "tmdb:tv:8", TMDBID: 8, MediaType: "series", Certification: "TV-Y"}}
	rec = httptest.NewRecorder()
	h.SeriesDetails(rec, httptest.NewRequest(http.MethodGet, "/api/metadata/series/details?tmdbId=8&userId=curated", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unlisted title: got %d, want 403", rec.Code)
	}
}

func TestDiscoverNewCuratedKidsEmpty(t *testing.T) {
	fake := &fakeMetadataService{trendingResp: []models.TrendingItem{{Title: models.Title{ID: "a", Name: "A", MediaType: "movie", Certification: "G"}}}}
	h := kidsTestHandler(t, fake)
	rec := httptest.NewRecorder()
	h.DiscoverNew(rec, httptest.NewRequest(http.MethodGet, "/api/discover/new?type=movie&userId=curated", nil))
	var resp DiscoverNewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if len(resp.Items) != 0 {
		t.Fatalf("curated kids saw %d discovery items", len(resp.Items))
	}
}

func TestDiscoverNewRatingKidsFiltered(t *testing.T) {
	fake := &fakeMetadataService{trendingResp: []models.TrendingItem{
		{Title: models.Title{ID: "a", Name: "A", MediaType: "movie", Certification: "G"}},
		{Title: models.Title{ID: "b", Name: "B", MediaType: "movie", Certification: "R"}},
	}}
	h := kidsTestHandler(t, fake)
	rec := httptest.NewRecorder()
	h.DiscoverNew(rec, httptest.NewRequest(http.MethodGet, "/api/discover/new?type=movie&userId=kid", nil))
	var resp DiscoverNewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Title.ID != "a" {
		t.Fatalf("got %+v", resp.Items)
	}
}

func TestCustomListCuratedKidsOnlyAllowedLists(t *testing.T) {
	items := []models.TrendingItem{{Title: models.Title{ID: "x", Name: "X", MediaType: "movie"}}}
	for _, tc := range []struct {
		list string
		want int
	}{
		{kidsTestList, 1},
		{kidsTestList + "/", 1},
		{"https://mdblist.com/lists/someone/horror", 0},
	} {
		fake := &fakeMetadataService{customListResp: items, customListTotal: 1, customListUnfiltered: 1}
		h := kidsTestHandler(t, fake)
		rec := httptest.NewRecorder()
		h.CustomList(rec, httptest.NewRequest(http.MethodGet, "/api/lists/custom?userId=curated&url="+url.QueryEscape(tc.list), nil))
		var resp CustomListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v (%s)", err, rec.Body.String())
		}
		if len(resp.Items) != tc.want {
			t.Errorf("list %s: got %d items, want %d", tc.list, len(resp.Items), tc.want)
		}
	}
}

func TestSameKidsTitle(t *testing.T) {
	if sameKidsTitle(models.Title{TMDBID: 5, MediaType: "movie"}, models.Title{TMDBID: 5, MediaType: "series"}) {
		t.Fatal("cross media type matched")
	}
	if !sameKidsTitle(models.Title{IMDBID: "tt1", MediaType: "tv"}, models.Title{IMDBID: "TT1", MediaType: "series"}) {
		t.Fatal("imdb match failed")
	}
}
