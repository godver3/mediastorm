package metadata

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"novastream/models"
)

func tmdbCastPayload(n int) string {
	parts := make([]string, n)
	for i := range n {
		parts[i] = fmt.Sprintf(`{"id":%d,"name":"Actor %d","character":"Role %d","order":%d}`, i+1, i, i, i)
	}
	return `{"cast":[` + strings.Join(parts, ",") + `]}`
}

func TestFetchMovieCreditsKeepsTopBilledCastUpToLimit(t *testing.T) {
	tmdbHTTP := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(tmdbCastPayload(45))), Header: make(http.Header)}, nil
	})}
	client := newTMDBClient("tmdb-key", "en", tmdbHTTP, newFileCache(t.TempDir(), 24))
	credits, err := client.fetchMovieCredits(context.Background(), 7)
	if err != nil {
		t.Fatalf("fetchMovieCredits: %v", err)
	}
	if len(credits.Cast) != tmdbCastLimit {
		t.Fatalf("cast = %d, want %d", len(credits.Cast), tmdbCastLimit)
	}
	if credits.Cast[0].Name != "Actor 0" || credits.Cast[tmdbCastLimit-1].Order != tmdbCastLimit-1 {
		t.Fatalf("cast not kept in billing order: first=%+v last=%+v", credits.Cast[0], credits.Cast[tmdbCastLimit-1])
	}
}

func TestMovieDetailsCacheHitReplacesLegacyCappedCast(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	const tmdbID int64 = 99
	legacy := make([]models.CastMember, legacyTMDBCastLimit)
	for i := range legacy {
		legacy[i] = models.CastMember{ID: int64(i + 1), Name: fmt.Sprintf("Actor %d", i), Order: i}
	}
	cacheID := cacheKey("tmdb", "movie", "details", "v4", "en", strconv.FormatInt(tmdbID, 10))
	if err := cache.set(cacheID, models.Title{ID: "tmdb:movie:99", Name: "Ensemble", TMDBID: tmdbID, Credits: &models.Credits{Cast: legacy}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.set(cacheKey("tmdb", "images", "v10", "en", "movie", "99"), tmdbImagesResult{LogoSelectionVersion: 1}); err != nil {
		t.Fatal(err)
	}
	var creditRequests atomic.Int32
	tmdbHTTP := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{}`
		if strings.Contains(req.URL.Path, "/credits") {
			creditRequests.Add(1)
			body = tmdbCastPayload(20)
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	svc := &Service{client: &tvdbClient{language: "en"}, tmdb: newTMDBClient("tmdb-key", "en", tmdbHTTP, cache), cache: cache}

	for range 2 {
		got, err := svc.getMovieDetailsFromTMDB(context.Background(), models.MovieDetailsQuery{TMDBID: tmdbID})
		if err != nil {
			t.Fatalf("getMovieDetailsFromTMDB: %v", err)
		}
		if got.Credits == nil || len(got.Credits.Cast) != 20 {
			t.Fatalf("cast = %v, want 20 members", got.Credits)
		}
	}
	if n := creditRequests.Load(); n != 1 {
		t.Fatalf("credit requests = %d, want 1", n)
	}
}

func TestCreditsNeedRefreshAndImprove(t *testing.T) {
	eight := &models.Credits{Cast: make([]models.CastMember, legacyTMDBCastLimit)}
	twelve := &models.Credits{Cast: make([]models.CastMember, 12)}
	if !creditsNeedRefresh(nil) || !creditsNeedRefresh(eight) || creditsNeedRefresh(twelve) {
		t.Fatal("creditsNeedRefresh: want nil and legacy-capped lists refreshed only")
	}
	if creditsImprove(eight, eight) || creditsImprove(eight, &models.Credits{}) || !creditsImprove(eight, twelve) || !creditsImprove(nil, eight) {
		t.Fatal("creditsImprove: want writes only for a non-empty list of a different size")
	}
}
