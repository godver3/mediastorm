package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"novastream/config"
	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/localmedia"

	"github.com/gorilla/mux"
)

// metadataLanguageManager builds a config.Manager whose metadata settings enable
// the given languages with the given primary, for local-media localization tests.
func metadataLanguageManager(t *testing.T, languages []string, primary string) *config.Manager {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(path)
	settings := config.DefaultSettings()
	settings.Metadata.Language = languages
	settings.Metadata.PrimaryLanguage = primary
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	return mgr
}

func localGroupsRequest(t *testing.T, handler *LocalMediaHandler, libraryID, query string) *models.LocalMediaGroupListResult {
	t.Helper()
	target := "/api/library/libraries/" + libraryID + "/groups"
	if query != "" {
		target += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req = mux.SetURLVars(req, map[string]string{"libraryID": libraryID})
	rr := httptest.NewRecorder()
	handler.ListGroups(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("ListGroups status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var out models.LocalMediaGroupListResult
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return &out
}

func newLocalGroupsFixture() (*fakeLocalMediaPlaybackService, *fakeMetadataService) {
	svc := &fakeLocalMediaPlaybackService{
		groups: &models.LocalMediaGroupListResult{
			Groups: []models.LocalMediaItemGroup{{
				ID:          "movie:1",
				LibraryType: models.LocalMediaLibraryTypeMovie,
				Title:       "Top Gun",
				TMDBID:      744,
				TextPoster:  &models.Image{Type: "poster", Language: "en"},
			}},
			Total: 1,
		},
	}
	return svc, &fakeMetadataService{}
}

func TestLocalMediaListGroupsLocalizesArtworkForNonDefaultLanguage(t *testing.T) {
	svc, meta := newLocalGroupsFixture()
	meta.applyArtworkLang = "it"
	handler := NewLocalMediaHandler(svc, fakeLocalMediaUsersProvider{}, false)
	handler.SetMetadataLanguageProviders(
		meta,
		metadataLanguageManager(t, []string{"eng", "ita"}, "eng"),
		&mockUserSettingsProvider{settings: map[string]*models.UserSettings{
			"u1": {Metadata: models.MetadataSettings{PrimaryLanguage: "ita"}},
		}},
	)

	out := localGroupsRequest(t, handler, "lib1", "userId=u1&include=cards")

	if got := atomic.LoadInt32(&meta.applyArtworkCalls); got != 1 {
		t.Fatalf("ApplyLocalizedArtwork calls = %d, want 1", got)
	}
	if out.Groups[0].TextPoster == nil || out.Groups[0].TextPoster.Language != "it" {
		t.Fatalf("textPoster = %#v, want localized Italian poster", out.Groups[0].TextPoster)
	}
}

func TestLocalMediaListGroupsSkipsLocalizationForDefaultLanguage(t *testing.T) {
	svc, meta := newLocalGroupsFixture()
	meta.applyArtworkLang = "it"
	handler := NewLocalMediaHandler(svc, fakeLocalMediaUsersProvider{}, false)
	handler.SetMetadataLanguageProviders(
		meta,
		metadataLanguageManager(t, []string{"eng", "ita"}, "eng"),
		&mockUserSettingsProvider{settings: map[string]*models.UserSettings{
			// Profile matches the global default, so stored (English) artwork stands.
			"u1": {Metadata: models.MetadataSettings{PrimaryLanguage: "eng"}},
		}},
	)

	out := localGroupsRequest(t, handler, "lib1", "userId=u1&include=cards")

	if got := atomic.LoadInt32(&meta.applyArtworkCalls); got != 0 {
		t.Fatalf("ApplyLocalizedArtwork calls = %d, want 0 (default language)", got)
	}
	if out.Groups[0].TextPoster == nil || out.Groups[0].TextPoster.Language != "en" {
		t.Fatalf("textPoster = %#v, want untouched English poster", out.Groups[0].TextPoster)
	}
}

func TestLocalMediaListGroupsSkipsLocalizationWithoutUser(t *testing.T) {
	svc, meta := newLocalGroupsFixture()
	meta.applyArtworkLang = "it"
	handler := NewLocalMediaHandler(svc, fakeLocalMediaUsersProvider{}, false)
	handler.SetMetadataLanguageProviders(
		meta,
		metadataLanguageManager(t, []string{"eng", "ita"}, "eng"),
		&mockUserSettingsProvider{settings: map[string]*models.UserSettings{}},
	)

	out := localGroupsRequest(t, handler, "lib1", "include=cards")

	if got := atomic.LoadInt32(&meta.applyArtworkCalls); got != 0 {
		t.Fatalf("ApplyLocalizedArtwork calls = %d, want 0 (no user)", got)
	}
	if out.Groups[0].TextPoster == nil || out.Groups[0].TextPoster.Language != "en" {
		t.Fatalf("textPoster = %#v, want untouched English poster", out.Groups[0].TextPoster)
	}
}

type fakeLocalMediaPlaybackService struct {
	item      *models.LocalMediaItem
	probe     *models.LocalMediaProbe
	probeErr  error
	libraries []models.LocalMediaLibrary
	groups    *models.LocalMediaGroupListResult
	matches   []models.LocalMediaMatchedGroup
	lastQuery models.LocalMediaItemListQuery
	err       error
}

func (f *fakeLocalMediaPlaybackService) GetItem(ctx context.Context, itemID string) (*models.LocalMediaItem, error) {
	return f.item, f.err
}

func (f *fakeLocalMediaPlaybackService) ProbeItemForPlayback(ctx context.Context, item *models.LocalMediaItem) (*models.LocalMediaProbe, error) {
	if f.probeErr != nil {
		return nil, f.probeErr
	}
	if f.probe != nil {
		return f.probe, nil
	}
	return &models.LocalMediaProbe{VideoCodec: "h264", DurationSeconds: 120, SizeBytes: 1024}, nil
}

func (f *fakeLocalMediaPlaybackService) ListLibraries(ctx context.Context) ([]models.LocalMediaLibrary, error) {
	return f.libraries, f.err
}

func (f *fakeLocalMediaPlaybackService) ListGroups(ctx context.Context, libraryID string, query models.LocalMediaItemListQuery) (*models.LocalMediaGroupListResult, error) {
	f.lastQuery = query
	return f.groups, f.err
}

func (f *fakeLocalMediaPlaybackService) FindMatches(ctx context.Context, query models.LocalMediaMatchQuery) ([]models.LocalMediaMatchedGroup, error) {
	return f.matches, f.err
}

type fakeLocalMediaUsersProvider struct {
	allowed bool
	user    models.User
	userOK  bool
}

func (f fakeLocalMediaUsersProvider) BelongsToAccount(profileID, accountID string) bool {
	return f.allowed
}

func (f fakeLocalMediaUsersProvider) Get(id string) (models.User, bool) {
	return f.user, f.userOK
}

func TestLocalMediaHandlerGetPlayback(t *testing.T) {
	handler := NewLocalMediaHandler(&fakeLocalMediaPlaybackService{
		item: &models.LocalMediaItem{
			ID:            "item1",
			FileName:      "Movie.Title.2024.mkv",
			FilePath:      "/srv/media/Movie.Title.2024.mkv",
			MatchedName:   "Movie Title",
			DetectedTitle: "Movie Title",
		},
	}, fakeLocalMediaUsersProvider{allowed: true}, true)

	req := httptest.NewRequest(http.MethodGet, "/api/library/items/item1/playback?profileId=user1", nil)
	req = mux.SetURLVars(req, map[string]string{"itemID": "item1"})
	ctx := context.WithValue(req.Context(), auth.ContextKeyAccountID, "acct1")
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.GetPlayback(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp models.LocalMediaPlaybackResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.StreamPath != "localmedia:item1/Movie.Title.2024.mkv" {
		t.Fatalf("StreamPath = %q", resp.StreamPath)
	}
	if !strings.Contains(resp.StreamURL, "/api/video/stream?") {
		t.Fatalf("StreamURL = %q", resp.StreamURL)
	}
	if !strings.Contains(resp.StreamURL, "path=localmedia%3Aitem1%2FMovie.Title.2024.mkv") {
		t.Fatalf("StreamURL = %q", resp.StreamURL)
	}
	if !strings.Contains(resp.StreamURL, "transmux=0") {
		t.Fatalf("StreamURL = %q", resp.StreamURL)
	}
	if !strings.Contains(resp.StreamURL, "profileId=user1") {
		t.Fatalf("StreamURL = %q", resp.StreamURL)
	}
	if resp.HLSStartURL == "" || !resp.HLSAvailable {
		t.Fatalf("expected HLS response, got %+v", resp)
	}
}

func TestLocalMediaHandlerGetPlaybackRejectsUnplayableProbe(t *testing.T) {
	handler := NewLocalMediaHandler(&fakeLocalMediaPlaybackService{
		item: &models.LocalMediaItem{
			ID:            "item1",
			FileName:      "Movie.Title.2024.mkv",
			FilePath:      "/srv/media/Movie.Title.2024.mkv",
			MatchedName:   "Movie Title",
			DetectedTitle: "Movie Title",
		},
		probeErr: localmedia.ErrLocalMediaNotPlayable,
	}, fakeLocalMediaUsersProvider{allowed: true}, true)

	req := httptest.NewRequest(http.MethodGet, "/api/library/items/item1/playback?profileId=user1", nil)
	req = mux.SetURLVars(req, map[string]string{"itemID": "item1"})
	ctx := context.WithValue(req.Context(), auth.ContextKeyAccountID, "acct1")
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.GetPlayback(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnprocessableEntity)
	}
	if !strings.Contains(rec.Body.String(), "local media file is not playable") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestLocalMediaHandlerGetPlaybackEpisodeUsesEpisodeItemID(t *testing.T) {
	handler := NewLocalMediaHandler(&fakeLocalMediaPlaybackService{
		item: &models.LocalMediaItem{
			ID:               "item1",
			FileName:         "Show.S01E02.mkv",
			FilePath:         "/srv/media/Show.S01E02.mkv",
			LibraryType:      models.LocalMediaLibraryTypeShow,
			MatchedTitleID:   "tvdb:12345",
			MatchedMediaType: "series",
			MatchedName:      "Example Show",
			SeasonNumber:     1,
			EpisodeNumber:    2,
			EpisodeTitle:     "Second Episode",
		},
	}, fakeLocalMediaUsersProvider{allowed: true}, true)

	req := httptest.NewRequest(http.MethodGet, "/api/library/items/item1/playback?profileId=user1", nil)
	req = mux.SetURLVars(req, map[string]string{"itemID": "item1"})
	ctx := context.WithValue(req.Context(), auth.ContextKeyAccountID, "acct1")
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.GetPlayback(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp models.LocalMediaPlaybackResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	parsed, err := url.Parse(resp.StreamURL)
	if err != nil {
		t.Fatalf("parse StreamURL %q: %v", resp.StreamURL, err)
	}
	query := parsed.Query()
	if got, want := query.Get("itemId"), "tvdb:12345:s01e02"; got != want {
		t.Fatalf("StreamURL itemId = %q, want %q", got, want)
	}
	if got := query.Get("mediaType"); got != "episode" {
		t.Fatalf("StreamURL mediaType = %q, want episode", got)
	}
	if got := query.Get("seriesName"); got != "Example Show" {
		t.Fatalf("StreamURL seriesName = %q, want Example Show", got)
	}
	if got := query.Get("episodeName"); got != "Second Episode" {
		t.Fatalf("StreamURL episodeName = %q, want Second Episode", got)
	}
}

func TestLocalMediaHandlerGetPlaybackRejectsForeignProfile(t *testing.T) {
	handler := NewLocalMediaHandler(&fakeLocalMediaPlaybackService{
		item: &models.LocalMediaItem{ID: "item1", FileName: "Movie.mkv"},
	}, fakeLocalMediaUsersProvider{allowed: false}, false)

	req := httptest.NewRequest(http.MethodGet, "/api/library/items/item1/playback?profileId=user1", nil)
	req = mux.SetURLVars(req, map[string]string{"itemID": "item1"})
	ctx := context.WithValue(req.Context(), auth.ContextKeyAccountID, "acct1")
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.GetPlayback(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestLocalMediaHandlerListLibraries(t *testing.T) {
	handler := NewLocalMediaHandler(&fakeLocalMediaPlaybackService{
		libraries: []models.LocalMediaLibrary{
			{ID: "lib1", Name: "My Movies", Type: models.LocalMediaLibraryTypeMovie, LastScanStatus: models.LocalMediaScanStatusComplete},
		},
	}, fakeLocalMediaUsersProvider{}, false)

	req := httptest.NewRequest(http.MethodGet, "/api/library/libraries", nil)
	rec := httptest.NewRecorder()
	handler.ListLibraries(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var libraries []models.LocalMediaLibrary
	if err := json.NewDecoder(rec.Body).Decode(&libraries); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(libraries) != 1 || libraries[0].ID != "lib1" {
		t.Fatalf("unexpected libraries: %+v", libraries)
	}
}

func TestLocalMediaHandlerListLibrariesEmpty(t *testing.T) {
	handler := NewLocalMediaHandler(&fakeLocalMediaPlaybackService{
		libraries: nil,
	}, fakeLocalMediaUsersProvider{}, false)

	req := httptest.NewRequest(http.MethodGet, "/api/library/libraries", nil)
	rec := httptest.NewRecorder()
	handler.ListLibraries(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var libraries []models.LocalMediaLibrary
	if err := json.NewDecoder(rec.Body).Decode(&libraries); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if libraries == nil {
		t.Fatal("expected empty slice, got nil")
	}
}

func TestLocalMediaHandlerListGroups(t *testing.T) {
	service := &fakeLocalMediaPlaybackService{
		groups: &models.LocalMediaGroupListResult{
			Groups: []models.LocalMediaItemGroup{
				{ID: "g1", Title: "Inception", LibraryType: models.LocalMediaLibraryTypeMovie, ItemCount: 1},
			},
			Total: 1,
			Limit: 50,
		},
	}
	handler := NewLocalMediaHandler(service, fakeLocalMediaUsersProvider{}, false)

	req := httptest.NewRequest(http.MethodGet, "/api/library/libraries/lib1/groups?include=cards", nil)
	req = mux.SetURLVars(req, map[string]string{"libraryID": "lib1"})
	rec := httptest.NewRecorder()
	handler.ListGroups(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var result models.LocalMediaGroupListResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.Groups) != 1 || result.Groups[0].ID != "g1" {
		t.Fatalf("unexpected groups: %+v", result)
	}
	if !service.lastQuery.IncludeCards {
		t.Fatal("expected IncludeCards to be true")
	}
}

func TestLocalMediaHandlerListGroupsPassesKidsRatingCaps(t *testing.T) {
	service := &fakeLocalMediaPlaybackService{
		groups: &models.LocalMediaGroupListResult{Groups: []models.LocalMediaItemGroup{}, Total: 0},
	}
	users := fakeLocalMediaUsersProvider{
		userOK: true,
		user: models.User{
			IsKidsProfile:      true,
			KidsMode:           "rating",
			KidsMaxMovieRating: "PG",
			KidsMaxTVRating:    "TV-Y7",
		},
	}
	handler := NewLocalMediaHandler(service, users, false)

	req := httptest.NewRequest(http.MethodGet, "/api/library/libraries/lib1/groups?profileId=kid1", nil)
	req = mux.SetURLVars(req, map[string]string{"libraryID": "lib1"})
	handler.ListGroups(httptest.NewRecorder(), req)

	if service.lastQuery.MaxMovieRating != "PG" || service.lastQuery.MaxTVRating != "TV-Y7" {
		t.Fatalf("expected rating caps PG/TV-Y7, got %q/%q", service.lastQuery.MaxMovieRating, service.lastQuery.MaxTVRating)
	}
}

func TestLocalMediaHandlerListGroupsNoCapsForNonKids(t *testing.T) {
	service := &fakeLocalMediaPlaybackService{
		groups: &models.LocalMediaGroupListResult{Groups: []models.LocalMediaItemGroup{}, Total: 0},
	}
	users := fakeLocalMediaUsersProvider{userOK: true, user: models.User{IsKidsProfile: false}}
	handler := NewLocalMediaHandler(service, users, false)

	req := httptest.NewRequest(http.MethodGet, "/api/library/libraries/lib1/groups?profileId=adult1", nil)
	req = mux.SetURLVars(req, map[string]string{"libraryID": "lib1"})
	handler.ListGroups(httptest.NewRecorder(), req)

	if service.lastQuery.MaxMovieRating != "" || service.lastQuery.MaxTVRating != "" {
		t.Fatalf("expected no rating caps for non-kids profile, got %q/%q", service.lastQuery.MaxMovieRating, service.lastQuery.MaxTVRating)
	}
}

func TestLocalMediaHandlerFindMatches(t *testing.T) {
	handler := NewLocalMediaHandler(&fakeLocalMediaPlaybackService{
		matches: []models.LocalMediaMatchedGroup{
			{
				LibraryID:   "lib1",
				LibraryName: "Movies",
				LibraryType: models.LocalMediaLibraryTypeMovie,
				Group: models.LocalMediaItemGroup{
					ID:    "movie:tmdb:movie:123",
					Title: "Inception",
				},
			},
		},
	}, fakeLocalMediaUsersProvider{}, false)

	req := httptest.NewRequest(http.MethodGet, "/api/library/matches?mediaType=movie&tmdbId=123&title=Inception&year=2010", nil)
	rec := httptest.NewRecorder()
	handler.FindMatches(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var matches []models.LocalMediaMatchedGroup
	if err := json.NewDecoder(rec.Body).Decode(&matches); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(matches) != 1 || matches[0].LibraryID != "lib1" || matches[0].Group.ID != "movie:tmdb:movie:123" {
		t.Fatalf("unexpected matches: %+v", matches)
	}
}
