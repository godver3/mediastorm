package jellyfinbridge

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
)

func artworkServer(t *testing.T) *Server {
	t.Helper()
	s := New(Deps{}, config.JellyfinBridgeSettings{})
	t.Cleanup(func() { _ = s.Shutdown(t.Context()) })
	return s
}

// Check both the advertised cache key and the URL actually sent through the
// anonymous image endpoint, rather than only checking the stored context.
func assertArtwork(t *testing.T, s *Server, item dto.BaseItemDto, kind, source string) {
	t.Helper()
	if got := item.ImageTags[kind]; got != imageTag(source) {
		t.Fatalf("%s %s tag = %q, want tag for %q", item.Type, kind, got, source)
	}
	s.deps.Handlers.ImageProxy = func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.URL.Query().Get("url"))
	}
	h := &harness{t: t, srv: s, handler: s.Handler()}
	res := h.do(http.MethodGet, "/Items/"+item.ID+"/Images/"+kind+"?tag="+item.ImageTags[kind], "", nil)
	if res.Code != http.StatusOK || res.Body.String() != source {
		t.Fatalf("%s %s served %d %q, want %q", item.Type, kind, res.Code, res.Body.String(), source)
	}
}

func TestSeasonArtworkReplacesEpisodeSeedAndSurvivesSparseReads(t *testing.T) {
	s := artworkServer(t)
	series := s.seriesRefFrom(titleInfo{MediaType: "series", ID: "tmdb:tv:1", Name: "Show", Poster: "https://img.example/series.jpg"})
	ep := s.episodeItem(nil, series, episodeInfo{Season: 2, Episode: 1})
	season := models.SeriesSeason{Number: 2, Image: &models.Image{URL: "https://img.example/season-2.jpg"}}
	item := s.seasonItem(nil, series, season)
	if item.ID != ep.SeasonID {
		t.Fatal("episode and season have different season IDs")
	}
	assertArtwork(t, s, item, dto.ImagePrimary, season.Image.URL)

	// Sparse item lookups and later episode listings must keep the real cover.
	s.episodeItem(nil, series, episodeInfo{Season: 2, Episode: 2})
	item, ok := s.itemByID(t.Context(), &principal{}, nil, item.ID, false)
	if !ok {
		t.Fatal("season lookup failed")
	}
	assertArtwork(t, s, item, dto.ImagePrimary, season.Image.URL)

	// Metadata can also change the cover of a season already cached on disk.
	season.Image.URL = "https://img.example/season-2-updated.jpg"
	item = s.seasonItem(nil, series, season)
	assertArtwork(t, s, item, dto.ImagePrimary, season.Image.URL)
	other := s.seasonItem(nil, series, models.SeriesSeason{Number: 3})
	assertArtwork(t, s, other, dto.ImagePrimary, series.Info.Poster)
}

func TestChildArtworkDoesNotBecomeSeriesPosterAfterReload(t *testing.T) {
	s := artworkServer(t)
	path := filepath.Join(t.TempDir(), "ids.json")
	store := newIDStore(path)
	series := s.seriesRefFrom(titleInfo{MediaType: "series", ID: "tmdb:tv:2", Name: "Show", Poster: "https://img.example/series.jpg"})
	season := s.seasonItem(nil, series, models.SeriesSeason{Number: 1, Image: &models.Image{URL: "https://img.example/season.jpg"}})
	ep := s.episodeItem(nil, series, episodeInfo{Season: 1, Episode: 1, Still: "https://img.example/still.jpg", FromMetadata: true})
	for _, item := range []dto.BaseItemDto{season, ep} {
		ref, _ := s.ids.get(item.ID)
		store.put(ref.Kind, ref.Source, ref.Ctx)
	}
	store.close()
	store = newIDStore(path)
	defer store.close()
	for _, item := range []dto.BaseItemDto{season, ep} {
		ref, _ := store.get(item.ID)
		parent := seriesInfoFromSeasonRef(ref)
		if parent.Poster != series.Info.Poster {
			t.Fatalf("%s leaked child image into series: %q", item.Type, parent.Poster)
		}
	}
	// Old persisted refs have no separate parent poster. Do not treat their
	// child image as a series image; recover it from the parent ref instead.
	legacy, _ := s.ids.get(ep.ID)
	legacy.Ctx.SeriesPoster = ""
	if got := s.seriesWithArtwork(seriesInfoFromSeasonRef(legacy)).Poster; got != series.Info.Poster {
		t.Fatalf("legacy parent poster = %q", got)
	}
}

func TestTitleArtworkTagsFollowStoredMetadata(t *testing.T) {
	s := artworkServer(t)
	for _, mediaType := range []string{"movie", "series"} {
		t.Run(mediaType, func(t *testing.T) {
			title := titleInfo{MediaType: mediaType, ID: "tmdb:" + mediaType + ":3", Name: "Title", Poster: "https://img.example/poster.jpg", Backdrop: "https://img.example/backdrop.jpg", trusted: true}
			s.titleItem(nil, title)
			stale := title
			stale.Poster, stale.Backdrop, stale.trusted = "https://img.example/stale.jpg", "", false
			item := s.titleItem(nil, stale)
			assertArtwork(t, s, item, dto.ImagePrimary, title.Poster)
			assertArtwork(t, s, item, dto.ImageThumb, title.Backdrop)
			if len(item.BackdropImageTags) != 1 || item.BackdropImageTags[0] != imageTag(title.Backdrop) {
				t.Fatal("backdrop tag does not match stored artwork")
			}
		})
	}
}

func TestEpisodeArtworkTagsFollowServedStillAndFallback(t *testing.T) {
	s := artworkServer(t)
	series := s.seriesRefFrom(titleInfo{MediaType: "series", ID: "tmdb:tv:4", Name: "Show", Poster: "https://img.example/poster.jpg", Backdrop: "https://img.example/backdrop.jpg"})
	current := episodeInfo{Season: 1, Episode: 1, Still: "https://img.example/current.jpg", FromMetadata: true}
	s.episodeItem(nil, series, current)
	stale := current
	stale.Still, stale.FromMetadata = "https://img.example/stale.jpg", false
	item := s.episodeItem(nil, series, stale)
	assertArtwork(t, s, item, dto.ImagePrimary, current.Still)
	current.Still = "https://img.example/updated.jpg"
	item = s.episodeItem(nil, series, current)
	assertArtwork(t, s, item, dto.ImagePrimary, current.Still)
	item = s.episodeItem(nil, series, episodeInfo{Season: 1, Episode: 2})
	assertArtwork(t, s, item, dto.ImagePrimary, series.Info.Backdrop)
	series.Info.ID = "tmdb:tv:5"
	series.Info.Backdrop = ""
	series = s.seriesRefFrom(series.Info)
	item = s.episodeItem(nil, series, episodeInfo{Season: 0, Episode: 1})
	assertArtwork(t, s, item, dto.ImagePrimary, series.Info.Poster)
	if item.PrimaryImageAspectRatio == nil || *item.PrimaryImageAspectRatio != 2.0/3.0 {
		t.Fatal("poster fallback must have portrait aspect ratio")
	}
}

func TestLocalArtworkUsesFileIdentityAndEpisodeStill(t *testing.T) {
	s := artworkServer(t)
	g := models.LocalMediaItemGroup{ID: "group", Title: "Local", TMDBID: 6,
		Poster: &models.Image{URL: "https://img.example/local-poster.jpg"}, Backdrop: &models.Image{URL: "https://img.example/local-backdrop.jpg"},
		Items: []models.LocalMediaItem{{ID: "movie-file", FileName: "movie.mkv"}},
		Seasons: []models.LocalMediaSeasonGroup{{SeasonNumber: 1, Episodes: []models.LocalMediaEpisodeGroup{
			{EpisodeNumber: 1, EpisodeImage: &models.Image{URL: "https://img.example/local-still.jpg"}, Items: []models.LocalMediaItem{{ID: "episode-file"}}},
			{EpisodeNumber: 2, Items: []models.LocalMediaItem{{ID: "episode-file-2"}}},
		}}},
	}
	// Catalog artwork can differ from a local/synced library's artwork.
	s.titleItem(nil, titleInfo{MediaType: "movie", ID: "tmdb:movie:6", Poster: "https://img.example/catalog.jpg", trusted: true})
	item, ok := s.localMovieItem(nil, "lib", g)
	if !ok {
		t.Fatal("local movie missing")
	}
	assertArtwork(t, s, item, dto.ImagePrimary, g.Poster.URL)
	item = s.localSeriesItem(nil, "lib", g)
	assertArtwork(t, s, item, dto.ImagePrimary, g.Poster.URL)
	eps := s.localEpisodeItems(nil, "lib", g, 1)
	if len(eps) != 2 {
		t.Fatalf("got %d local episodes", len(eps))
	}
	assertArtwork(t, s, eps[0], dto.ImagePrimary, g.Seasons[0].Episodes[0].EpisodeImage.URL)
	assertArtwork(t, s, eps[1], dto.ImagePrimary, g.Backdrop.URL)
	if _, ok := s.ids.get(eps[0].SeasonID); !ok {
		t.Fatal("direct local episode listing did not register its season")
	}
	g.Seasons[0].Episodes[0].EpisodeImage.URL = "https://img.example/local-still-updated.jpg"
	eps = s.localEpisodeItems(nil, "lib", g, 1)
	assertArtwork(t, s, eps[0], dto.ImagePrimary, g.Seasons[0].Episodes[0].EpisodeImage.URL)
	g.Poster.URL = "https://img.example/local-poster-updated.jpg"
	seasons := s.localSeasonItems(nil, "lib", g)
	assertArtwork(t, s, seasons[0], dto.ImagePrimary, g.Poster.URL)
}

func TestArtworkTagsInvalidateOldMismatchedCache(t *testing.T) {
	url := "https://img.example/season.jpg"
	old := sha1.Sum([]byte(url))
	if imageTag(url) == hex.EncodeToString(old[:8]) || imageTag("") != "" {
		t.Fatal("artwork tag must be empty for no image and invalidate old cached images")
	}
}

func TestLegacySeasonImageFallsBackToParentWithoutListing(t *testing.T) {
	s := artworkServer(t)
	series := s.seriesRefFrom(titleInfo{MediaType: "series", ID: "tmdb:tv:2004", Name: "Malcolm in the Middle", Poster: "https://img.example/series-poster.jpg"})
	id := s.ids.put(refSeason, seasonSource(series.Src, 1), refContext{Season: 1})
	item := dto.BaseItemDto{ID: id, ImageTags: imageTagsFor(series.Info.Poster)}
	assertArtwork(t, s, item, dto.ImagePrimary, series.Info.Poster)
}

func TestSeasonArtworkDoesNotLeakBetweenOrderings(t *testing.T) {
	s := artworkServer(t)
	s.deps.Metadata = orderingArtworkMetadata{}
	series := s.seriesRefFrom(titleInfo{MediaType: "series", ID: "tmdb:tv:2004", Name: "Show", Poster: "https://img.example/series.jpg"})
	official := s.seasonItem(nil, series, models.SeriesSeason{Number: 1, Image: &models.Image{URL: "https://img.example/official.jpg"}})
	for _, ordering := range []string{"dvd", "absolute", "alternate", "streaming"} {
		series.Ordering = ordering
		item := s.seasonItem(nil, series, models.SeriesSeason{Number: 1})
		if item.ID == official.ID {
			t.Fatalf("%s shares the official season ref", ordering)
		}
		assertArtwork(t, s, item, dto.ImagePrimary, series.Info.Poster)
		rebuilt, ok := s.itemByID(t.Context(), &principal{}, nil, item.ID, false)
		if !ok || rebuilt.ID != item.ID {
			t.Fatalf("%s sparse lookup changed identity: %+v", ordering, rebuilt)
		}
		assertArtwork(t, s, rebuilt, dto.ImagePrimary, series.Info.Poster)
		detailed, ok := s.itemByID(t.Context(), &principal{}, nil, item.ID, true)
		if !ok || detailed.ID != item.ID {
			t.Fatalf("%s detailed lookup changed identity: %+v", ordering, detailed)
		}
		ep := s.episodeItem(nil, series, episodeInfo{Season: 1, Episode: 1})
		if ep.SeasonID != item.ID {
			t.Fatalf("%s episode points at a different season", ordering)
		}
	}
	assertArtwork(t, s, official, dto.ImagePrimary, "https://img.example/official.jpg")
}

type orderingArtworkMetadata struct{ fakeMetadata }

func (orderingArtworkMetadata) SeriesDetails(_ context.Context, query models.SeriesDetailsQuery) (*models.SeriesDetails, error) {
	return &models.SeriesDetails{
		Title:          models.Title{ID: query.TitleID, MediaType: "series", Name: query.Name},
		ActiveOrdering: query.SeasonType,
		Seasons:        []models.SeriesSeason{{Number: 1}},
	}, nil
}
