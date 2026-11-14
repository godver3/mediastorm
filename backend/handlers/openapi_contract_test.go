package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"novastream/api/openapi"
	"novastream/internal/apierror"
	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/accounts"
	"novastream/services/clients"
	"novastream/services/customlists"
	"novastream/services/playback"
	"novastream/services/profileunlock"
	"novastream/services/sessions"
	"novastream/services/sourcehealth"
	user_settings "novastream/services/user_settings"
	"novastream/services/users"
	"novastream/services/watchlist"
)

// The OpenAPI contract tests drive real handlers with representative fixtures
// and validate each request and response against backend/api/openapi/openapi.yaml.
// Responses are also checked for JSON fields the spec does not document, so a
// new model field fails here until the spec (and the generated native clients)
// learn about it.

const (
	contractAccountID = "acct-1"
	contractClientID  = "device-abc"
)

type contractCall struct {
	method  string
	target  string
	body    any
	vars    map[string]string
	master  bool
	headers map[string]string
	// session, when set, is injected as the authenticated session.
	session *models.Session
	// invalid marks a deliberately non-conforming request (negative tests):
	// the spec must reject it, and the handler's error response is validated.
	invalid bool
}

func (c contractCall) request(t *testing.T) (*http.Request, []byte) {
	t.Helper()
	var raw []byte
	switch body := c.body.(type) {
	case nil:
	case string:
		raw = []byte(body)
	case []byte:
		raw = body
	default:
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		raw = encoded
	}
	req := httptest.NewRequest(c.method, c.target, bytes.NewReader(raw))
	if raw != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer contract-test-token")
	req.Header.Set("X-Client-ID", contractClientID)
	for key, value := range c.headers {
		req.Header.Set(key, value)
	}
	if len(c.vars) > 0 {
		req = mux.SetURLVars(req, c.vars)
	}
	ctx := context.WithValue(req.Context(), auth.ContextKeyAccountID, contractAccountID)
	ctx = context.WithValue(ctx, auth.ContextKeyIsMaster, c.master)
	if c.session != nil {
		ctx = context.WithValue(ctx, auth.ContextKeySession, *c.session)
	}
	return req.WithContext(ctx), raw
}

// exercise validates the request, runs the handler, validates the response
// and returns the recorder for further assertions.
func exercise(t *testing.T, v *openapi.Validator, call contractCall, handler http.HandlerFunc, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	req, raw := call.request(t)
	err := v.ValidateRequest(req, raw)
	switch {
	case call.invalid && err == nil:
		t.Fatal("spec accepted a request the test marks invalid")
	case !call.invalid && err != nil:
		t.Fatalf("request does not match spec: %v", err)
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, wantStatus, rec.Body.String())
	}
	if err := v.ValidateResponse(req, rec.Code, rec.Header(), rec.Body.Bytes()); err != nil {
		t.Fatalf("response does not match spec: %v\nbody=%s", err, rec.Body.String())
	}
	return rec
}

func decodeContract[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	return out
}

// --- fixtures ----------------------------------------------------------------

func contractImage(kind string) *models.Image {
	return &models.Image{URL: "https://image.example/" + kind + ".jpg", Type: kind, Width: 1000, Height: 1500, IsTextless: kind == "backdrop", Language: "en"}
}

func contractMovieTitle() models.Title {
	return models.Title{
		ID:              "tmdb:movie:603",
		Name:            "The Matrix",
		OriginalName:    "The Matrix",
		AlternateTitles: []string{"Matrix"},
		Overview:        "A hacker learns the truth.",
		Year:            1999,
		Language:        "en",
		CountryCode:     "US",
		Poster:          contractImage("poster"),
		TextPoster:      contractImage("poster"),
		Backdrop:        contractImage("backdrop"),
		Backdrops:       []models.Image{*contractImage("backdrop")},
		Logo:            contractImage("logo"),
		MediaType:       "movie",
		IMDBID:          "tt0133093",
		TMDBID:          603,
		Popularity:      80.5,
		VoteCount:       25000,
		Certification:   "R",
		PrimaryTrailer:  &models.Trailer{Name: "Trailer", Site: "YouTube", URL: "https://youtube.example/t", Key: "abc", Official: true},
		Releases:        []models.Release{{Type: "theatrical", Date: "1999-03-31", Country: "US", Source: "tmdb", Primary: true, Released: true}, {Type: "digital", Date: "1999-09-21", Source: "tmdb", Released: true}},
		Theatrical:      &models.Release{Type: "theatrical", Date: "1999-03-31", Source: "tmdb", Released: true},
		HomeRelease:     &models.Release{Type: "digital", Date: "1999-09-21", Source: "tmdb", Released: true},
		Ratings:         []models.Rating{{Source: "imdb", Value: 8.7, Max: 10}},
		Credits:         &models.Credits{Cast: []models.CastMember{{ID: 6384, Name: "Keanu Reeves", Character: "Neo", Order: 0, ProfileURL: "https://image.example/k.jpg"}}},
		RuntimeMinutes:  136,
		Collection:      &models.Collection{ID: 2344, Name: "The Matrix Collection", Poster: contractImage("poster")},
		Genres:          []string{"Action", "Science Fiction"},
		WatchState:      "partial",
	}
}

func contractSeriesDetails() *models.SeriesDetails {
	airedEpisode := func(season, episode int, date string) models.SeriesEpisode {
		return models.SeriesEpisode{
			ID:            "tvdb:episode:" + date,
			TVDBID:        1000 + int64(season*100+episode),
			TMDBID:        2000 + int64(season*100+episode),
			Name:          "Episode",
			Overview:      "Overview",
			SeasonNumber:  season,
			EpisodeNumber: episode,
			AiredDate:     date,
			Runtime:       45,
			Image:         contractImage("still"),
		}
	}
	preferred := 1
	return &models.SeriesDetails{
		Title: models.Title{
			ID:              "tmdb:tv:95350",
			Name:            "Example Show",
			Overview:        "A show.",
			Year:            2020,
			Language:        "en",
			MediaType:       "series",
			TMDBID:          95350,
			TVDBID:          365000,
			IMDBID:          "tt1234567",
			Network:         "HBO",
			AirsTime:        "21:00",
			AirsTimezone:    "America/New_York",
			Status:          models.SeriesReleaseStatusReleased,
			LifecycleStatus: "Continuing",
			ReleaseDate:     "2020-01-05",
			Poster:          contractImage("poster"),
		},
		Seasons: []models.SeriesSeason{{
			ID:           "season-1",
			TVDBID:       5001,
			Name:         "Season 1",
			Number:       1,
			Overview:     "",
			Type:         "official",
			EpisodeCount: 2,
			Episodes: []models.SeriesEpisode{
				airedEpisode(1, 1, "2020-01-05"),
				airedEpisode(1, 2, "2020-01-12"),
			},
		}},
		PreferredSeason:    &preferred,
		AvailableOrderings: []models.SeriesOrdering{{Type: "official", Name: "Aired Order", SeasonCount: 1, IsOfficial: true}, {Type: "dvd", Name: "DVD Order", SeasonCount: 1}},
		ActiveOrdering:     "official",
	}
}

func contractSeriesState(now time.Time) models.SeriesWatchState {
	return models.SeriesWatchState{
		SeriesID:    "tmdb:tv:95350",
		SeriesTitle: "Example Show",
		PosterURL:   "https://image.example/poster.jpg",
		BackdropURL: "https://image.example/backdrop.jpg",
		Year:        2020,
		ExternalIDs: map[string]string{"tmdb": "95350", "tvdb": "365000"},
		UpdatedAt:   now,
		LastWatched: models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot", WatchedAt: now},
		NextEpisode: &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 2, Title: "Second", AirDate: "2020-01-12"},
		WatchedEpisodes: map[string]models.EpisodeReference{
			"s01e01": {SeasonNumber: 1, EpisodeNumber: 1, WatchedAt: now},
		},
		WatchedEpisodeCount: 1,
		TotalEpisodeCount:   2,
	}
}

func contractProgress(now time.Time) []models.PlaybackProgress {
	return []models.PlaybackProgress{
		{
			ID: "episode:tmdb:tv:95350:s01e02", MediaType: "episode", ItemID: "tmdb:tv:95350:s01e02",
			Position: 600, Duration: 2700, PercentWatched: 22.2, UpdatedAt: now,
			ExternalIDs: map[string]string{"tmdb": "95350"}, SeasonNumber: 1, EpisodeNumber: 2,
			SeriesID: "tmdb:tv:95350", SeriesName: "Example Show", EpisodeName: "Second",
		},
		{
			ID: "movie:tmdb:movie:603", MediaType: "movie", ItemID: "tmdb:movie:603",
			Position: 0, Duration: 0, PercentWatched: 40, UpdatedAt: now,
			MovieName: "The Matrix", Year: 1999,
		},
	}
}

// contractHistory implements the history service methods the client routes
// use; any other method panics via the nil embedded interface.
type contractHistory struct {
	historyService
	continueItems []models.SeriesWatchState
	seriesState   *models.SeriesWatchState
	progress      []models.PlaybackProgress
	watched       []models.WatchHistoryItem
}

func (h *contractHistory) ListContinueWatching(string) ([]models.SeriesWatchState, error) {
	return append([]models.SeriesWatchState(nil), h.continueItems...), nil
}

func (h *contractHistory) ListSeriesStates(string) ([]models.SeriesWatchState, error) {
	return h.continueItems, nil
}

func (h *contractHistory) GetSeriesWatchState(_, seriesID string) (*models.SeriesWatchState, error) {
	if h.seriesState == nil || h.seriesState.SeriesID != seriesID {
		return nil, nil
	}
	copy := *h.seriesState
	return &copy, nil
}

func (h *contractHistory) GetSeriesOrdering(string, int64) (string, error) { return "", nil }

func (h *contractHistory) ListPlaybackProgress(string) ([]models.PlaybackProgress, error) {
	return append([]models.PlaybackProgress{}, h.progress...), nil
}

func (h *contractHistory) ListWatchHistory(string) ([]models.WatchHistoryItem, error) {
	return append([]models.WatchHistoryItem{}, h.watched...), nil
}

func (h *contractHistory) UpdatePlaybackProgress(_ string, update models.PlaybackProgressUpdate) (models.PlaybackProgress, error) {
	percent := update.PercentWatched
	if update.Duration > 0 {
		percent = update.Position / update.Duration * 100
	}
	return models.PlaybackProgress{
		ID: update.MediaType + ":" + update.ItemID, MediaType: update.MediaType, ItemID: update.ItemID,
		Position: update.Position, Duration: update.Duration, PercentWatched: percent, UpdatedAt: time.Now().UTC(),
		IsPaused: update.IsPaused, ExternalIDs: update.ExternalIDs, SeasonNumber: update.SeasonNumber,
		EpisodeNumber: update.EpisodeNumber, SeriesID: update.SeriesID, SeriesName: update.SeriesName,
		EpisodeName: update.EpisodeName,
	}, nil
}

func (h *contractHistory) UpdateWatchHistory(_ string, update models.WatchHistoryUpdate) (models.WatchHistoryItem, error) {
	watched := update.Watched == nil || *update.Watched
	return models.WatchHistoryItem{
		ID: update.MediaType + ":" + update.ItemID, MediaType: update.MediaType, ItemID: update.ItemID,
		Name: update.Name, Watched: watched, WatchedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		SeasonNumber: update.SeasonNumber, EpisodeNumber: update.EpisodeNumber, SeriesID: update.SeriesID,
	}, nil
}

func (h *contractHistory) BulkUpdateWatchHistory(userID string, updates []models.WatchHistoryUpdate) ([]models.WatchHistoryItem, error) {
	return h.BulkUpdateScopedWatchHistory(userID, updates, "")
}

func (h *contractHistory) BulkUpdateScopedWatchHistory(userID string, updates []models.WatchHistoryUpdate, _ string) ([]models.WatchHistoryItem, error) {
	items := make([]models.WatchHistoryItem, 0, len(updates))
	for _, update := range updates {
		item, _ := h.UpdateWatchHistory(userID, update)
		h.watched = append(h.watched, item)
		items = append(items, item)
	}
	return items, nil
}

func (h *contractHistory) IsMovieWatched(_, itemID string, _ map[string]string) bool {
	for _, item := range h.watched {
		if item.MediaType == "movie" && item.ItemID == itemID {
			return item.Watched
		}
	}
	return false
}

type contractUsers struct{}

func (contractUsers) Exists(string) bool { return true }

type contractContentPrefs struct{}

func (contractContentPrefs) Get(_, contentID string) (*models.ContentPreference, error) {
	return &models.ContentPreference{ContentID: contentID, ContentType: "series", AudioLanguage: "jpn", SubtitleLanguage: "eng", SubtitleMode: "on", UpdatedAt: time.Now().UTC()}, nil
}
func (contractContentPrefs) Set(string, models.ContentPreference) error { return nil }
func (contractContentPrefs) Delete(string, string) error                { return nil }
func (contractContentPrefs) List(string) ([]models.ContentPreference, error) {
	return nil, nil
}

func contractProbe() *models.VideoFullResult {
	return &models.VideoFullResult{
		VideoCodec: "hevc",
		AudioStreams: []models.AudioStreamInfo{
			{Index: 1, Codec: "eac3", Profile: "Dolby Digital Plus + Dolby Atmos", Language: "eng", Title: "English Atmos"},
			{Index: 2, Codec: "aac", Language: "jpn", Title: "Japanese"},
		},
		SubtitleStreams: []models.SubtitleStreamInfo{
			{Index: 3, Codec: "hdmv_pgs_subtitle", Language: "eng", Title: "English PGS"},
			{Index: 4, Codec: "subrip", Language: "eng", Title: "English SDH", IsDefault: true},
		},
	}
}

func contractIndexerResult() models.NZBResult {
	return models.NZBResult{
		Title:       "Example.Show.S01E02.2160p.WEB-DL.DV.HDR.DDP5.1.Atmos-GRP",
		Indexer:     "Torbox",
		GUID:        "guid-1",
		Link:        "magnet:?xt=urn:btih:abc",
		DownloadURL: "magnet:?xt=urn:btih:abc",
		SizeBytes:   8_000_000_000,
		PublishDate: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Categories:  []string{"5040"},
		Attributes:  map[string]string{"resolution": "2160p", "infoHash": "abc", "titleId": "tmdb:tv:95350"},
		ServiceType: models.ServiceTypeDebrid,
	}
}

// --- the contract ------------------------------------------------------------

func TestOpenAPIContract(t *testing.T) {
	v, err := openapi.NewValidator(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)

	t.Run("auth", func(t *testing.T) {
		t.Setenv("STRMR_INITIAL_ADMIN_PASSWORD", "contract-test-master-password")
		dir := t.TempDir()
		accountsSvc, err := accounts.NewService(dir)
		if err != nil {
			t.Fatal(err)
		}
		sessionsSvc, err := sessions.NewService(dir, sessions.DefaultSessionDuration)
		if err != nil {
			t.Fatal(err)
		}
		h := NewAuthHandler(accountsSvc, sessionsSvc)
		login := func(password string, status int) {
			call := contractCall{method: http.MethodPost, target: "/api/auth/login",
				body: LoginRequest{Username: "admin", Password: password, RememberMe: true}}
			req, raw := call.request(t)
			req.RemoteAddr = "127.0.0.1:4321"
			if err := v.ValidateRequest(req, raw); err != nil {
				t.Fatalf("request: %v", err)
			}
			rec := httptest.NewRecorder()
			h.Login(rec, req)
			if rec.Code != status {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, status, rec.Body.String())
			}
			if err := v.ValidateResponse(req, rec.Code, rec.Header(), rec.Body.Bytes()); err != nil {
				t.Fatalf("response: %v\nbody=%s", err, rec.Body.String())
			}
		}
		login("contract-test-master-password", http.StatusOK)
		login("wrong", http.StatusUnauthorized)

		account, ok := accountsSvc.GetByUsername("admin")
		if !ok {
			t.Fatal("admin account missing")
		}
		session, err := sessionsSvc.Create(account.ID, true, "contract", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		bearer := map[string]string{"Authorization": "Bearer " + session.Token}

		rec := exercise(t, v, contractCall{method: http.MethodPost, target: "/api/auth/refresh", headers: bearer}, h.Refresh, http.StatusOK)
		refreshed := decodeContract[LoginResponse](t, rec)
		if refreshed.Token == "" || refreshed.Token == session.Token {
			t.Fatalf("refresh must rotate the token: %+v", refreshed)
		}
		// The rotated-out token is gone.
		rec = exercise(t, v, contractCall{method: http.MethodPost, target: "/api/auth/refresh", headers: bearer}, h.Refresh, http.StatusUnauthorized)
		if body := decodeContract[map[string]string](t, rec); body["code"] != "session_expired" {
			t.Fatalf("stale refresh code = %q", body["code"])
		}

		newBearer := map[string]string{"Authorization": "Bearer " + refreshed.Token}
		exercise(t, v, contractCall{method: http.MethodPost, target: "/api/auth/logout", headers: newBearer}, h.Logout, http.StatusOK)
		if _, err := sessionsSvc.Validate(refreshed.Token); err == nil {
			t.Fatal("logout must revoke the session")
		}
		exercise(t, v, contractCall{method: http.MethodPost, target: "/api/auth/logout", headers: map[string]string{"Authorization": ""}}, h.Logout, http.StatusBadRequest)

		exercise(t, v, contractCall{method: http.MethodGet, target: "/api/version"}, NewVersionHandler().GetVersion, http.StatusOK)
	})

	t.Run("profiles", func(t *testing.T) {
		usersSvc, err := users.NewService(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		profile, err := usersSvc.CreateForAccount(contractAccountID, "Living Room")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := usersSvc.SetPin(profile.ID, "1234"); err != nil {
			t.Fatal(err)
		}
		h := NewUsersHandler(usersSvc)
		unlocks := profileunlock.NewService(nil)
		h.SetProfileUnlocks(unlocks, func() time.Duration { return time.Hour })
		session := &models.Session{Token: "contract-test-token", AccountID: contractAccountID, ExpiresAt: time.Now().Add(24 * time.Hour)}

		rec := exercise(t, v, contractCall{method: http.MethodGet, target: "/api/users"}, h.List, http.StatusOK)
		profiles := decodeContract[[]map[string]any](t, rec)
		if len(profiles) == 0 || profiles[0]["hasPin"] != true {
			t.Fatalf("expected a PIN-protected profile, got %v", profiles)
		}
		if _, leaked := profiles[0]["pinHash"]; leaked {
			t.Fatal("pinHash must not be serialised")
		}

		verify := func(pin string, status int) *httptest.ResponseRecorder {
			return exercise(t, v, contractCall{method: http.MethodPost, target: "/api/users/" + profile.ID + "/pin/verify",
				body: map[string]string{"pin": pin}, vars: map[string]string{"userID": profile.ID}, session: session}, h.VerifyPin, status)
		}
		ok := decodeContract[VerifyPinResponse](t, verify("1234", http.StatusOK))
		if !ok.Valid || ok.UnlockToken == "" || ok.ExpiresAt == "" {
			t.Fatalf("verify must return an unlock token: %+v", ok)
		}
		if !unlocks.Validate([]string{ok.UnlockToken}, session.Token, profile.ID, usersSvc.PinFingerprint(profile.ID)) {
			t.Fatal("issued unlock token does not validate")
		}
		wrong := decodeContract[map[string]string](t, verify("0000", http.StatusUnauthorized))
		if wrong["code"] != "pin_invalid" || wrong["error"] != "invalid PIN" {
			t.Fatalf("wrong PIN body = %v", wrong)
		}

		// The enforcement middleware lives in package api (which imports this
		// package), so validate its 403 envelope against the spec directly.
		lockedReq, _ := contractCall{method: http.MethodGet, target: "/api/users/" + profile.ID + "/details-bundle?type=movie&titleId=tmdb:movie:603",
			headers: map[string]string{"X-Profile-Unlock": "pu_stale"}}.request(t)
		if err := v.ValidateRequest(lockedReq, nil); err != nil {
			t.Fatalf("X-Profile-Unlock request does not match spec: %v", err)
		}
		locked := httptest.NewRecorder()
		apierror.Write(locked, http.StatusForbidden, apierror.CodeProfileLocked, "profile is locked", map[string]string{"profileId": profile.ID})
		if err := v.ValidateResponse(lockedReq, locked.Code, locked.Header(), locked.Body.Bytes()); err != nil {
			t.Fatalf("profile_locked response does not match spec: %v", err)
		}
	})

	history := &contractHistory{
		continueItems: []models.SeriesWatchState{contractSeriesState(now)},
		progress:      contractProgress(now),
		watched: []models.WatchHistoryItem{{
			ID: "episode:tmdb:tv:95350:s01e01", MediaType: "episode", ItemID: "tmdb:tv:95350:s01e01", Name: "Pilot",
			Watched: true, WatchedAt: now, UpdatedAt: now, ExternalIDs: map[string]string{"tmdb": "95350"},
			SeasonNumber: 1, EpisodeNumber: 1, SeriesID: "tmdb:tv:95350", SeriesName: "Example Show",
		}},
	}
	state := contractSeriesState(now)
	history.seriesState = &state
	historyHandler := NewHistoryHandler(history, contractUsers{}, false)
	userVars := map[string]string{"userID": "u1"}

	t.Run("history/continue", func(t *testing.T) {
		rec := exercise(t, v, contractCall{method: http.MethodGet, target: "/api/users/u1/history/continue", vars: userVars},
			historyHandler.ListContinueWatching, http.StatusOK)
		items := decodeContract[[]models.SeriesWatchState](t, rec)
		if len(items) != 1 || items[0].Resume == nil || items[0].NextEpisode == nil || items[0].NextEpisode.ItemID == "" {
			t.Fatalf("continue item missing resume/itemId: %+v", items)
		}
		if items[0].Resume.Percent <= 1 {
			t.Fatalf("resume.percent should be 0–100, got %v", items[0].Resume.Percent)
		}
	})

	t.Run("history/series", func(t *testing.T) {
		exercise(t, v, contractCall{method: http.MethodGet, target: "/api/users/u1/history/series/tmdb:tv:95350",
			vars: map[string]string{"userID": "u1", "seriesID": "tmdb:tv:95350"}}, historyHandler.GetSeriesWatchState, http.StatusOK)
		exercise(t, v, contractCall{method: http.MethodGet, target: "/api/users/u1/history/series/tmdb:tv:1",
			vars: map[string]string{"userID": "u1", "seriesID": "tmdb:tv:1"}}, historyHandler.GetSeriesWatchState, http.StatusNotFound)
	})

	t.Run("history/progress", func(t *testing.T) {
		rec := exercise(t, v, contractCall{method: http.MethodGet, target: "/api/users/u1/history/progress?titleId=tmdb:tv:95350&type=series&tmdbId=95350", vars: userVars},
			historyHandler.ListPlaybackProgress, http.StatusOK)
		rows := decodeContract[[]models.PlaybackProgress](t, rec)
		if len(rows) != 1 || rows[0].Resume == nil {
			t.Fatalf("expected one scoped row with resume, got %+v", rows)
		}

		// Episode addressed by series + season + episode, no itemId.
		update := map[string]any{
			"mediaType": "episode", "seriesId": "tmdb:tv:95350", "seasonNumber": 1, "episodeNumber": 2,
			"position": 1200.5, "duration": 2700, "timestamp": now.Format(time.RFC3339), "isPaused": false,
			"isBuffering": false, "bufferAheadSeconds": 30, "requiredBandwidthMbps": 25.5,
			"sourcePath": "/debrid/torbox/file.mkv", "seriesName": "Example Show", "episodeName": "Second",
			"externalIds": map[string]string{"tmdb": "95350"},
		}
		rec = exercise(t, v, contractCall{method: http.MethodPost, target: "/api/users/u1/history/progress", body: update, vars: userVars},
			historyHandler.UpdatePlaybackProgress, http.StatusOK)
		stored := decodeContract[models.PlaybackProgress](t, rec)
		if stored.ItemID != "tmdb:tv:95350:s01e02" || stored.Resume == nil || stored.AllowedToContinue == nil {
			t.Fatalf("unexpected progress response: %+v", stored)
		}
		if stored.PercentWatched < 40 || stored.PercentWatched > 50 {
			t.Fatalf("percentWatched should be 0–100 (~44.5), got %v", stored.PercentWatched)
		}

		// The body is strict: an unknown field is rejected by the spec and the server.
		exercise(t, v, contractCall{method: http.MethodPost, target: "/api/users/u1/history/progress", vars: userVars, invalid: true,
			body: `{"mediaType":"movie","itemId":"tmdb:movie:603","position":1,"duration":2,"progressPercent":50}`},
			historyHandler.UpdatePlaybackProgress, http.StatusBadRequest)
	})

	t.Run("history/watched", func(t *testing.T) {
		exercise(t, v, contractCall{method: http.MethodGet, target: "/api/users/u1/history/watched", vars: userVars},
			historyHandler.ListWatchHistory, http.StatusOK)
		exercise(t, v, contractCall{method: http.MethodGet, target: "/api/users/u1/history/watched?fields=status", vars: userVars},
			historyHandler.ListWatchHistory, http.StatusOK)
		exercise(t, v, contractCall{method: http.MethodGet, target: "/api/users/u1/history/watched?fields=status&format=keys", vars: userVars},
			historyHandler.ListWatchHistory, http.StatusOK)
		exercise(t, v, contractCall{method: http.MethodPost, target: "/api/users/u1/history/watched", vars: userVars,
			body: map[string]any{"mediaType": "episode", "seriesId": "tmdb:tv:95350", "seasonNumber": 1, "episodeNumber": 2, "watched": true}},
			historyHandler.UpdateWatchHistory, http.StatusOK)
		exercise(t, v, contractCall{method: http.MethodPost, target: "/api/users/u1/history/watched/bulk?scope=season", vars: userVars,
			body: []map[string]any{
				{"mediaType": "episode", "seriesId": "tmdb:tv:95350", "seasonNumber": 1, "episodeNumber": 1, "watched": true},
				{"mediaType": "episode", "itemId": "tmdb:tv:95350:s01e02", "seriesId": "tmdb:tv:95350", "seasonNumber": 1, "episodeNumber": 2, "watched": true},
			}},
			historyHandler.BulkUpdateWatchHistory, http.StatusOK)
		exercise(t, v, contractCall{method: http.MethodPost, target: "/api/users/u1/history/watched/bulk", vars: userVars,
			body: []map[string]any{{"mediaType": "movie", "itemId": "tmdb:movie:603", "watched": true}}},
			historyHandler.BulkUpdateWatchHistory, http.StatusOK)
	})

	metadata := &fakeMetadataService{
		seriesResp: contractSeriesDetails(),
		movieResp:  func() *models.Title { t := contractMovieTitle(); return &t }(),
		searchResp: []models.SearchResult{
			{Title: contractMovieTitle(), Score: 100},
			{Title: contractSeriesDetails().Title, Score: 90},
		},
	}
	metadataHandler := NewMetadataHandler(metadata, testConfigManager(t))

	t.Run("details-bundle", func(t *testing.T) {
		h := NewDetailsBundleHandler(metadata, history, contractContentPrefs{}, contractUsers{})
		rec := exercise(t, v, contractCall{method: http.MethodGet,
			target: "/api/users/u1/details-bundle?type=series&titleId=tmdb:tv:95350&name=Example+Show&tmdbId=95350&tvdbId=365000", vars: userVars},
			h.GetDetailsBundle, http.StatusOK)
		bundle := decodeContract[DetailsBundleResponse](t, rec)
		if bundle.SeriesDetails == nil || bundle.UpNextEpisode == nil || bundle.Resume == nil {
			t.Fatalf("series bundle missing details/upNext/resume: %s", rec.Body.String())
		}
		episodes := bundle.SeriesDetails.Seasons[0].Episodes
		if episodes[0].ItemID == "" || !episodes[0].Watched || episodes[1].Resume == nil {
			t.Fatalf("episodes missing itemId/watched/resume: %+v", episodes)
		}

		wl, err := watchlist.NewService(t.TempDir())
		if err != nil {
			t.Fatalf("watchlist service: %v", err)
		}
		if _, err := wl.AddOrUpdate("u1", models.WatchlistUpsert{ID: "tmdb:movie:603", MediaType: "movie", Name: "The Matrix"}); err != nil {
			t.Fatalf("watchlist add: %v", err)
		}
		h.SetWatchlistService(wl)
		rec = exercise(t, v, contractCall{method: http.MethodGet,
			target: "/api/users/u1/details-bundle?type=movie&titleId=tmdb:movie:603&name=The+Matrix&year=1999&imdbId=tt0133093&tmdbId=603", vars: userVars},
			h.GetDetailsBundle, http.StatusOK)
		movie := decodeContract[DetailsBundleResponse](t, rec)
		if movie.InWatchlist == nil || !*movie.InWatchlist {
			t.Fatalf("movie bundle missing inWatchlist=true: %s", rec.Body.String())
		}
		// Marked watched in history/watched/bulk above.
		if movie.Watched == nil || !*movie.Watched {
			t.Fatalf("movie bundle missing watched=true: %s", rec.Body.String())
		}
	})

	t.Run("metadata/details", func(t *testing.T) {
		rec := exercise(t, v, contractCall{method: http.MethodGet, target: "/api/metadata/movies/details?titleId=tmdb:movie:603&name=The+Matrix&year=1999&imdbId=tt0133093&tmdbId=603"},
			metadataHandler.MovieDetails, http.StatusOK)
		movie := decodeContract[models.Title](t, rec)
		if movie.ReleaseDate == "" || movie.Availability != models.AvailabilityReleased {
			t.Fatalf("movie missing releaseDate/availability: %+v", movie)
		}
		exercise(t, v, contractCall{method: http.MethodGet, target: "/api/metadata/series/details?titleId=tmdb:tv:95350&name=Example+Show&tvdbId=365000&seasonType=official"},
			metadataHandler.SeriesDetails, http.StatusOK)
	})

	t.Run("search", func(t *testing.T) {
		exercise(t, v, contractCall{method: http.MethodGet, target: "/api/search?q=matrix"}, metadataHandler.Search, http.StatusOK)
	})

	t.Run("lists", func(t *testing.T) {
		dir := t.TempDir()
		wl, err := watchlist.NewService(dir)
		if err != nil {
			t.Fatal(err)
		}
		custom, err := customlists.NewService(dir)
		if err != nil {
			t.Fatal(err)
		}
		usersSvc, err := users.NewService(dir)
		if err != nil {
			t.Fatal(err)
		}
		profile, err := usersSvc.CreateForAccount(contractAccountID, "Lists")
		if err != nil {
			t.Fatal(err)
		}
		vars := map[string]string{"userID": profile.ID}
		base := "/api/users/" + profile.ID

		watchlistHandler := NewWatchlistHandler(wl, usersSvc, false)
		for _, upsert := range []models.WatchlistUpsert{
			{ID: "tmdb:movie:603", MediaType: "movie", Name: "The Matrix", Year: 1999, PosterURL: "https://image.example/p.jpg",
				ExternalIDs: map[string]string{"tmdb": "603", "imdb": "tt0133093"}, Genres: []string{"Action"}, RuntimeMinutes: 136, Status: "released"},
			{ID: "tmdb:tv:95350", MediaType: "series", Name: "Example Show", Year: 2020, ExternalIDs: map[string]string{"tmdb": "95350"}, Genres: []string{"Drama"}},
		} {
			exercise(t, v, contractCall{method: http.MethodPost, target: base + "/watchlist", body: upsert, vars: vars},
				watchlistHandler.Add, http.StatusOK)
		}
		exercise(t, v, contractCall{method: http.MethodGet, target: base + "/watchlist", vars: vars}, watchlistHandler.List, http.StatusOK)

		displayList := NewDisplayListHandler(wl, custom, usersSvc)
		rec := exercise(t, v, contractCall{method: http.MethodGet,
			target: base + "/display-list?source=watchlist&sortBy=name&sortDirection=asc&watchStatus=all&includeFacets=true&limit=1&offset=0", vars: vars},
			displayList.Get, http.StatusOK)
		page := decodeContract[DisplayListResponse](t, rec)
		if page.Total != 2 || len(page.Genres) == 0 {
			t.Fatalf("expected total=2 with genre facets, got %+v", page)
		}

		exercise(t, v, contractCall{method: http.MethodDelete, target: base + "/watchlist/movie/tmdb:movie:603",
			vars: map[string]string{"userID": profile.ID, "mediaType": "movie", "id": "tmdb:movie:603"}},
			watchlistHandler.Remove, http.StatusNoContent)
	})

	t.Run("indexers", func(t *testing.T) {
		gate := &gatedDebridChecker{release: make(chan struct{})}
		cfg := sourcehealth.DefaultConfig()
		cfg.InlineBudget = 10 * time.Millisecond
		results := []models.NZBResult{
			{Title: "Example.Show.S01E02.1080p.WEB-DL", Indexer: "NZBGeek", GUID: "u1", Link: "https://nzb.example/1", DownloadURL: "https://nzb.example/1.nzb",
				SizeBytes: 2_000_000_000, PublishDate: now, ServiceType: models.ServiceTypeUsenet},
			contractIndexerResult(),
		}
		h := NewIndexerHandler(&fakeIndexerService{results: results}, false)
		h.SetSourceHealthService(sourcehealth.New(gate, cfg))

		// Plain search (what the prototype uses) with health annotations.
		rec := exercise(t, v, contractCall{method: http.MethodGet,
			target: "/api/indexers/search?q=Example+Show+S01E02&mediaType=series&titleId=tmdb:tv:95350&year=2020&limit=20&userId=u1&includeSourceHealth=true"},
			h.Search, http.StatusOK)
		plain := decodeContract[[]map[string]any](t, rec)
		health, _ := plain[1]["sourceHealth"].(map[string]any)
		if health["state"] != models.SourceHealthPending || health["token"] == "" {
			t.Fatalf("debrid result should be pending with a token: %v", plain[1])
		}

		// Scored + wrapped search.
		exercise(t, v, contractCall{method: http.MethodGet,
			target: "/api/indexers/search?q=Example+Show+S01E02&mediaType=series&includeFiltered=true&includeAdaptiveSummary=true&includeSourceHealth=true"},
			h.Search, http.StatusOK)

		close(gate.release)
		token, _ := health["token"].(string)
		rec = exercise(t, v, contractCall{method: http.MethodGet, target: "/api/indexers/source-health?token=" + token + "&waitMs=2000"},
			h.SourceHealth, http.StatusOK)
		snapshot := decodeContract[sourcehealth.Snapshot](t, rec)
		if !snapshot.Complete {
			t.Fatalf("snapshot not complete: %+v", snapshot)
		}
		exercise(t, v, contractCall{method: http.MethodGet, target: "/api/indexers/source-health?token=missing"},
			h.SourceHealth, http.StatusNotFound)
	})

	t.Run("prequeue", func(t *testing.T) {
		store := playback.NewPrequeueStore(time.Hour)
		h := &PrequeueHandler{store: store}

		// An in-progress entry for the same title/profile is reused, so no
		// background worker starts.
		pending, _ := store.Create("tmdb:tv:95350", "Example Show", "u1", "series", 2020,
			&models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 2}, "details")
		store.Update(pending.ID, func(e *playback.PrequeueEntry) { e.Status = playback.PrequeueStatusSearching })
		rec := exercise(t, v, contractCall{method: http.MethodPost, target: "/api/playback/prequeue", body: playback.PrequeueRequest{
			TitleID: "tmdb:tv:95350", TitleName: "Example Show", MediaType: "series", UserID: "u1", ImdbID: "tt1234567",
			TmdbID: "95350", Year: 2020, SeasonNumber: 1, EpisodeNumber: 2, StartOffset: 600, Reason: "details", SkipHLS: true,
		}}, h.Prequeue, http.StatusOK)
		if resp := decodeContract[playback.PrequeueResponse](t, rec); resp.PrequeueID != pending.ID {
			t.Fatalf("expected reuse of %s, got %+v", pending.ID, resp)
		}

		ready, _ := store.Create("tmdb:movie:603", "The Matrix", "u1", "movie", 1999, nil, "details")
		selected := contractIndexerResult()
		store.Update(ready.ID, func(e *playback.PrequeueEntry) {
			e.Status = playback.PrequeueStatusReady
			e.StreamPath = "/debrid/torbox/matrix.mkv"
			e.ServiceType = "debrid"
			e.DebridProvider = "torbox"
			e.FileSize = 8_000_000_000
			e.HealthStatus = "cached"
			e.HasDolbyVision = true
			e.DolbyVisionProfile = "dvhe.08.06"
			e.DolbyVisionConfiguration = &models.DolbyVisionConfiguration{StreamIndex: 0, PixelFormat: "yuv420p10le", VersionMajor: 1, Profile: 8, Level: 6, RPUPresentFlag: 1, BLPresentFlag: 1, BLSignalCompatibilityID: 1}
			e.Duration = 8160.5
			e.FrameRate = "24000/1001"
			e.SelectedAudioTrack = 1
			e.SelectedSubtitleTrack = 4
			e.TrackSelection = &models.TrackSelection{
				Audio:            &models.SelectedTrack{StreamIndex: 1, TypeIndex: 0, Language: "eng", LanguageCode: "eng", LanguageCount: 1, Codec: "eac3", Title: "English Atmos"},
				Subtitle:         &models.SelectedTrack{StreamIndex: 4, TypeIndex: 1, Language: "eng", LanguageCode: "eng", LanguageIndex: 1, LanguageCount: 2, Codec: "subrip", Default: true},
				TextSubtitle:     &models.SelectedTrack{StreamIndex: 4, TypeIndex: 1, Language: "eng", LanguageCode: "eng", LanguageIndex: 1, LanguageCount: 2, Codec: "subrip", Default: true},
				AudioLanguage:    "eng",
				SubtitleLanguage: "eng",
				SubtitleMode:     "on",
			}
			e.AudioTracks = []playback.AudioTrackInfo{{Index: 1, Language: "eng", Codec: "eac3", Title: "English Atmos"}}
			e.SubtitleTracks = []playback.SubtitleTrackInfo{{Index: 3, AbsoluteIndex: 3, Language: "eng", Codec: "hdmv_pgs_subtitle", IsBitmap: true}, {Index: 4, AbsoluteIndex: 4, Language: "eng", Codec: "subrip"}}
			e.ResultAttributes = map[string]string{"resolution": "2160p"}
			e.SelectedResult = &selected
			e.SelectedResultIndex = 1
		})
		rec = exercise(t, v, contractCall{method: http.MethodGet, target: "/api/playback/prequeue/" + ready.ID,
			vars: map[string]string{"prequeueID": ready.ID}}, h.GetStatus, http.StatusOK)
		status := decodeContract[playback.PrequeueStatusResponse](t, rec)
		if status.TrackSelection == nil || status.TrackSelection.Subtitle.StreamIndex != status.SelectedSubtitleTrack {
			t.Fatalf("selectedSubtitleTrack must be the container stream index of trackSelection.subtitle: %+v", status)
		}
		exercise(t, v, contractCall{method: http.MethodGet, target: "/api/playback/prequeue/missing",
			vars: map[string]string{"prequeueID": "missing"}}, h.GetStatus, http.StatusNotFound)
	})

	t.Run("resolve", func(t *testing.T) {
		svc := &mockPlaybackService{
			resolveFunc: func(context.Context, models.NZBResult) (*models.PlaybackResolution, error) {
				return &models.PlaybackResolution{QueueID: 7, WebDAVPath: "/debrid/torbox/show.mkv", HealthStatus: "cached", DebridProvider: "torbox", FileSize: 8_000_000_000, Probe: contractProbe()}, nil
			},
			queueStatusFunc: func(_ context.Context, queueID int64) (*models.PlaybackResolution, error) {
				return &models.PlaybackResolution{QueueID: queueID, WebDAVPath: "/usenet/show.mkv", HealthStatus: "healthy", Probe: contractProbe()}, nil
			},
		}
		h := NewPlaybackHandler(svc)
		rec := exercise(t, v, contractCall{method: http.MethodPost, target: "/api/playback/resolve", body: map[string]any{
			"result": contractIndexerResult(), "startOffset": 0, "profileId": "u1", "includeTrackSelection": true, "titleId": "tmdb:tv:95350",
		}}, h.Resolve, http.StatusOK)
		if resolution := decodeContract[models.PlaybackResolution](t, rec); resolution.TrackSelection == nil {
			t.Fatalf("expected trackSelection: %s", rec.Body.String())
		}
		exercise(t, v, contractCall{method: http.MethodGet, target: "/api/playback/queue/7?includeTrackSelection=true&profileId=u1&titleId=tmdb:tv:95350",
			vars: map[string]string{"queueID": "7"}}, h.QueueStatus, http.StatusOK)
	})

	t.Run("video", func(t *testing.T) {
		h := NewVideoHandler(false, "", "")
		// A typical native direct-play URL matches the documented parameters.
		req, _ := contractCall{method: http.MethodGet,
			target:  "/api/video/stream?path=%2Fdebrid%2Ftorbox%2Fmatrix.mkv&transmux=0&profileId=u1&profileName=Living+Room&displayName=The+Matrix&clientId=" + contractClientID,
			headers: map[string]string{"Range": "bytes=0-"}}.request(t)
		if err := v.ValidateRequest(req, nil); err != nil {
			t.Fatalf("stream request does not match spec: %v", err)
		}
		// Missing path: GET and HEAD both answer 400.
		exercise(t, v, contractCall{method: http.MethodGet, target: "/api/video/stream?transmux=0", invalid: true}, h.StreamVideo, http.StatusBadRequest)
		exercise(t, v, contractCall{method: http.MethodHead, target: "/api/video/stream?transmux=0", invalid: true}, h.StreamVideo, http.StatusBadRequest)

		rec := exercise(t, v, contractCall{method: http.MethodPost, target: "/api/video/segments", body: map[string]any{
			"duration": 2700,
			"chapters": []map[string]any{{"title": "Previously On", "start": 0, "end": 45}, {"title": "Intro", "start": 45, "end": 120}, {"title": "Episode", "start": 120}},
		}}, h.GetIntroSegments, http.StatusOK)
		segments := decodeContract[map[string][]map[string]any](t, rec)["segments"]
		if len(segments) == 0 {
			t.Fatalf("expected chapter-derived segments: %s", rec.Body.String())
		}
	})

	t.Run("clients", func(t *testing.T) {
		dir := t.TempDir()
		usersSvc, err := users.NewService(dir)
		if err != nil {
			t.Fatal(err)
		}
		profile, err := usersSvc.CreateForAccount(contractAccountID, "Living Room")
		if err != nil {
			t.Fatal(err)
		}
		clientsSvc, err := clients.NewService(dir)
		if err != nil {
			t.Fatal(err)
		}
		h := NewClientsHandler(clientsSvc, nil, usersSvc)
		exercise(t, v, contractCall{method: http.MethodPost, target: "/api/clients/register", body: ClientRegistrationRequest{
			ID: contractClientID, UserID: profile.ID, DeviceName: "Apple TV", DeviceType: "Apple TV", OS: "tvOS", AppVersion: "0.1.0",
		}}, h.Register, http.StatusOK)
		exercise(t, v, contractCall{method: http.MethodPost, target: "/api/clients/register", body: map[string]string{"userId": profile.ID}, invalid: true},
			h.Register, http.StatusBadRequest)
	})

	t.Run("recommendation interests", func(t *testing.T) {
		service := &recommendationInterestsFake{items: map[string][]models.RecommendationInterest{}}
		h := &MetadataHandler{Interests: service, UsersService: &fakeUsersServiceForSearch{users: map[string]models.User{"own": {ID: "own", AccountID: contractAccountID}, "foreign": {ID: "foreign", AccountID: "other-account"}}}}
		exercise(t, v, contractCall{method: http.MethodDelete, target: "/api/users/own/recommendation-interests", vars: map[string]string{"userID": "own"}}, h.ClearSearchInterests, http.StatusNoContent)
		exercise(t, v, contractCall{method: http.MethodDelete, target: "/api/users/foreign/recommendation-interests", vars: map[string]string{"userID": "foreign"}}, h.ClearSearchInterests, http.StatusNotFound)
	})

	t.Run("profile-skipping", func(t *testing.T) {
		dir := t.TempDir()
		usersSvc, err := users.NewService(dir)
		if err != nil {
			t.Fatal(err)
		}
		profile, err := usersSvc.CreateForAccount(contractAccountID, "Fixture")
		if err != nil {
			t.Fatal(err)
		}
		settingsSvc, err := user_settings.NewService(dir)
		if err != nil {
			t.Fatal(err)
		}
		h := NewUserSettingsHandler(settingsSvc, usersSvc, nil)
		exercise(t, v, contractCall{method: http.MethodPatch,
			target: "/api/users/" + profile.ID + "/settings/skipping",
			vars:   map[string]string{"userID": profile.ID}, body: map[string]bool{"intro": true, "recap": false},
		}, h.PatchSkipping, http.StatusOK)
	})

	t.Run("coverage", func(t *testing.T) {
		covered := v.Covered()
		var missing []string
		for _, id := range v.OperationIDs() {
			if !covered[id] {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			t.Fatalf("spec operations without a contract test: %s", strings.Join(missing, ", "))
		}
	})
}
