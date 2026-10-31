package metadata

import (
	"context"
	"testing"

	"novastream/models"
)

func TestBigBrotherSeasonEpisodeReleaseException(t *testing.T) {
	for name, identity := range map[string]models.Title{
		"TVDB": {TVDBID: 76706},
		"TMDB": {TMDBID: 10160},
		"IMDb": {IMDBID: "tt0251497"},
	} {
		t.Run(name, func(t *testing.T) {
			for _, cachedDaily := range []bool{false, true} {
				title := identity
				title.Name = "Localized Big Brother title"
				title.Genres = []string{"Reality", "Game Show"}
				title.IsDaily = cachedDaily
				changed, _ := applyDateBasedSeriesClassification(&title)
				if title.IsDaily || changed != cachedDaily {
					t.Fatalf("cachedDaily=%v: daily=%v changed=%v", cachedDaily, title.IsDaily, changed)
				}
			}
		})
	}
	other := models.Title{Name: "Other Game Show", TVDBID: 123, Genres: []string{"Game Show"}}
	applyDateBasedSeriesClassification(&other)
	if !other.IsDaily {
		t.Fatal("exception changed classification for another game show")
	}
}

func TestBigBrotherCachedDailyFlagIsCorrected(t *testing.T) {
	for _, mode := range []string{"full", "lite-full-cache", "lite-cache", "batch"} {
		t.Run(mode, func(t *testing.T) {
			svc := &Service{
				client: &tvdbClient{language: "eng"},
				cache:  newFileCache(t.TempDir(), 24),
			}
			cacheID := seriesDetailsCacheKey("eng", 76706, "")
			if mode == "lite-cache" {
				cacheID = seriesDetailsLiteCacheKey("eng", 76706, "")
			}
			cached := models.SeriesDetails{
				Title: models.Title{ID: "tvdb:series:76706", Name: "Big Brother (US)", TVDBID: 76706,
					IMDBID: "tt0251497", Genres: []string{"Game Show"}, IsDaily: true},
				Seasons: []models.SeriesSeason{{Number: 10}},
			}
			if err := svc.cache.set(cacheID, cached); err != nil {
				t.Fatal(err)
			}
			query := models.SeriesDetailsQuery{TitleID: cached.Title.ID}
			var details *models.SeriesDetails
			var err error
			switch mode {
			case "full":
				details, err = svc.SeriesDetails(context.Background(), query)
			case "batch":
				results := svc.BatchSeriesDetails(context.Background(), []models.SeriesDetailsQuery{query})
				details = results[0].Details
			default:
				details, err = svc.SeriesDetailsLite(context.Background(), query)
			}
			if err != nil || details == nil || details.Title.IsDaily {
				t.Fatalf("cached classification: details=%+v err=%v", details, err)
			}
			var persisted models.SeriesDetails
			if ok, err := svc.cache.get(cacheID, &persisted); err != nil || !ok || persisted.Title.IsDaily {
				t.Fatalf("cached daily flag was not persisted: daily=%v ok=%v err=%v", persisted.Title.IsDaily, ok, err)
			}
		})
	}
}
