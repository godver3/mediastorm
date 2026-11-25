package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/config"
	"novastream/models"
	"novastream/services/localmedia"
)

// TestFindLibraryCopy: the catalog title ID's external ID is looked up too,
// and a kids profile's rating cap applies as in the library listings.
func TestFindLibraryCopy(t *testing.T) {
	svc := &fakeLocalMediaPlaybackService{copies: []localmedia.LibraryCopy{
		{Item: models.LocalMediaItem{ID: "sample", FileName: "sample.mkv", LibraryType: models.LocalMediaLibraryTypeShow, SizeBytes: 9}, LibraryName: "Series"},
		{Item: models.LocalMediaItem{ID: "e2", FileName: "Show.S01E02.mkv", LibraryType: models.LocalMediaLibraryTypeShow, SizeBytes: 99, Metadata: &models.Title{Certification: "TV-MA"}}, LibraryName: "Series"},
	}}
	h := NewLocalMediaHandler(svc, fakeLocalMediaUsersProvider{allowed: true}, false)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	got, err := h.FindLibraryCopy(r, "u1", LibraryCopyQuery{MediaType: "series", TitleID: "tvdb:series:396876", Season: 1, Episode: 2, Absolute: 14})
	if err != nil || got == nil || got.StreamPath != "localmedia:e2/Show.S01E02.mkv" || got.LibraryName != "Series" || got.SizeBytes != 99 {
		t.Fatalf("copy %+v, %v", got, err)
	}
	if q := svc.lastCopyQuery; q.TVDBID != "396876" || q.MediaType != "series" || svc.lastCopySeason != 1 || svc.lastCopyEpisode != 2 || svc.lastCopyAbsolute != 14 {
		t.Fatalf("query %+v S%dE%d (absolute %d)", q, svc.lastCopySeason, svc.lastCopyEpisode, svc.lastCopyAbsolute)
	}
	kidsProfile := models.User{ID: "kid", IsKidsProfile: true, KidsMode: "rating", KidsMaxTVRating: "TV-PG"}
	svc.copies = svc.copies[1:] // only the TV-MA episode
	kidsHandler := NewLocalMediaHandler(svc, fakeLocalMediaUsersProvider{allowed: true, user: kidsProfile, userOK: true}, false)
	if got, _ := kidsHandler.FindLibraryCopy(r, "kid", LibraryCopyQuery{MediaType: "series", TitleID: "tvdb:series:396876", Season: 1, Episode: 2}); got != nil {
		t.Fatal("a TV-MA file was offered to a TV-PG kids profile")
	}
}

// TestQualityPresetClientListOffersLibraryCopy: a title a local library has
// is offered "Library copy" first, and Auto uses it unless a preset is
// remembered for the title.
func TestQualityPresetClientListOffersLibraryCopy(t *testing.T) {
	h, _ := newPresetHandler(t)
	remembered := ""
	h.Select = func(mediaType, userID, clientID, titleID string, anime bool) (string, string) {
		if titleID != "" && remembered != "" {
			return remembered, "title"
		}
		return "", "global"
	}
	var lastQuery LibraryCopyQuery
	h.LibraryCopy = func(_ *http.Request, _ string, q LibraryCopyQuery) (*LibraryCopy, error) {
		lastQuery = q
		if q.TitleID == "tmdb:movie:1" && q.MediaType == "movie" {
			return &LibraryCopy{StreamPath: "localmedia:m/m.mkv"}, nil
		}
		return nil, nil
	}
	type selection struct{ PresetID, Source string }
	list := func(target string) (offered []string, movie, movieDefault selection, prefer bool) {
		rec := httptest.NewRecorder()
		h.ClientList(rec, presetRequest(http.MethodGet, target, "", false, "acc", nil))
		var got struct {
			Offered             []struct{ ID string }
			Movie, MovieDefault selection
			PreferLibraryCopies bool
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%v: %s", err, rec.Body.String())
		}
		for _, o := range got.Offered {
			offered = append(offered, o.ID)
		}
		return offered, got.Movie, got.MovieDefault, got.PreferLibraryCopies
	}

	offered, movie, def, prefer := list("/x?mediaType=movie&titleId=tmdb:movie:1&name=Heat")
	if len(offered) == 0 || offered[0] != config.QualityPresetLocal || movie.PresetID != config.QualityPresetLocal || movie.Source != "library" || def.PresetID != config.QualityPresetLocal || !prefer {
		t.Fatalf("library title: offered %v, movie %+v, default %+v, prefer %v", offered, movie, def, prefer)
	}
	if offered, movie, _, _ := list("/x?mediaType=movie&titleId=tmdb:movie:2&name=Other"); len(offered) != 0 || movie.PresetID != "" {
		t.Fatalf("title without a copy: offered %v, movie %+v", offered, movie)
	}
	// The episode about to play is passed on; a bare TMDB number is canonicalized.
	list("/x?mediaType=series&titleId=1399&tmdbId=1399&name=GoT&season=2&episode=5")
	if lastQuery.Season != 2 || lastQuery.Episode != 5 || lastQuery.TitleID == "1399" {
		t.Fatalf("series lookup %+v", lastQuery)
	}
	remembered = "simple"
	if _, movie, def, _ := list("/x?mediaType=movie&titleId=tmdb:movie:1&name=Heat"); movie.PresetID == config.QualityPresetLocal || def.PresetID != config.QualityPresetLocal {
		t.Fatalf("remembered preset: movie %+v, default %+v", movie, def)
	}

	s, _ := h.Config.Load()
	s.QualityPresets.IgnoreLibraryCopies = true
	if err := h.Config.Save(s); err != nil {
		t.Fatal(err)
	}
	remembered = ""
	if offered, movie, _, prefer := list("/x?mediaType=movie&titleId=tmdb:movie:1&name=Heat"); len(offered) != 0 || movie.PresetID != "" || prefer {
		t.Fatalf("switched off: offered %v, movie %+v, prefer %v", offered, movie, prefer)
	}
}
