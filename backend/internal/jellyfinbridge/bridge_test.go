package jellyfinbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
	"novastream/services/accounts"
	"novastream/services/sessions"
	"novastream/services/users"
)

// --- fakes -----------------------------------------------------------------

type fakeMetadata struct{}

func (fakeMetadata) Search(_ context.Context, query, mediaType string) ([]models.SearchResult, error) {
	if mediaType == "series" {
		return []models.SearchResult{{Title: models.Title{ID: "tmdb:tv:1399", Name: "Game of Thrones " + query, MediaType: "series", TMDBID: 1399}}}, nil
	}
	return []models.SearchResult{{Title: models.Title{ID: "tmdb:movie:603", Name: "The Matrix", MediaType: "movie", TMDBID: 603, Year: 1999}}}, nil
}

func (fakeMetadata) Trending(context.Context, string) ([]models.TrendingItem, error) {
	return []models.TrendingItem{{Rank: 1, Title: models.Title{ID: "tmdb:movie:27205", Name: "Inception", MediaType: "movie", TMDBID: 27205, Poster: &models.Image{URL: "https://image.tmdb.org/t/p/w500/inception.jpg"}}}}, nil
}

func (fakeMetadata) MovieDetails(_ context.Context, q models.MovieDetailsQuery) (*models.Title, error) {
	return &models.Title{ID: q.TitleID, Name: q.Name, MediaType: "movie", TMDBID: q.TMDBID, Overview: "details", RuntimeMinutes: 136}, nil
}

func (fakeMetadata) SeriesInfo(_ context.Context, q models.SeriesDetailsQuery) (*models.Title, error) {
	title := &models.Title{ID: q.TitleID, Name: q.Name, MediaType: "series", TMDBID: q.TMDBID}
	if q.TMDBID == 1399 {
		title.IMDBID = "tt0944947"
	}
	return title, nil
}

func (fakeMetadata) EnrichSearchCertifications(_ context.Context, results []models.SearchResult) {
	for i := range results {
		results[i].Title.Certification = "R"
	}
}

func (fakeMetadata) EnrichTrendingCertifications(_ context.Context, items []models.TrendingItem) {
	for i := range items {
		items[i].Title.Certification = "PG"
	}
}

func (fakeMetadata) Similar(_ context.Context, mediaType string, tmdbID int64) ([]models.Title, error) {
	if mediaType == "movie" {
		return []models.Title{
			{ID: "tmdb:movie:604", Name: "The Matrix Reloaded", MediaType: "movie", TMDBID: 604, Year: 2003},
			{ID: "tmdb:movie:605", Name: "The Matrix Revolutions", MediaType: "movie", TMDBID: 605, Year: 2003},
		}, nil
	}
	return nil, nil
}

func (fakeMetadata) PersonDetails(_ context.Context, personID int64) (*models.PersonDetails, error) {
	return &models.PersonDetails{
		Person: models.Person{ID: personID, Name: "Keanu Reeves", Biography: "Actor.", Birthday: "1964-09-02", PlaceOfBirth: "Beirut"},
		Filmography: []models.Title{
			{ID: "tmdb:movie:603", Name: "The Matrix", MediaType: "movie", TMDBID: 603, Year: 1999, Genres: []string{"Action"}},
			{ID: "tmdb:movie:245891", Name: "John Wick", MediaType: "movie", TMDBID: 245891, Year: 2014, Genres: []string{"Action", "Thriller"}},
		},
	}, nil
}

func (fakeMetadata) EnrichTitleCertifications(_ context.Context, titles []models.Title) {
	for i := range titles {
		titles[i].Certification = "PG"
	}
}

func (fakeMetadata) SeriesDetails(_ context.Context, q models.SeriesDetailsQuery) (*models.SeriesDetails, error) {
	future := time.Now().Add(30 * 24 * time.Hour).Format("2006-01-02")
	return &models.SeriesDetails{
		Title: models.Title{ID: q.TitleID, Name: q.Name, MediaType: "series", TMDBID: q.TMDBID},
		Seasons: []models.SeriesSeason{{
			Number: 1, Name: "Season 1", EpisodeCount: 3,
			Episodes: []models.SeriesEpisode{
				{SeasonNumber: 1, EpisodeNumber: 1, Name: "Pilot", AiredDate: "2011-04-17"},
				{SeasonNumber: 1, EpisodeNumber: 2, Name: "Second", AiredDate: "2011-04-24"},
				{SeasonNumber: 1, EpisodeNumber: 3, Name: "Unaired", AiredDate: future},
			},
		}},
	}, nil
}

type fakeHistory struct {
	progress []models.PlaybackProgressUpdate
	mu       sync.Mutex
	watched  []models.WatchHistoryItem
	onList   func() // runs inside ListWatchHistory (tests a write racing a read)
}

func (*fakeHistory) ListContinueWatching(string) ([]models.SeriesWatchState, error) {
	return []models.SeriesWatchState{{
		SeriesID: "tmdb:tv:1399", SeriesTitle: "Game of Thrones", ExternalIDs: map[string]string{"tmdb": "1399"},
		NextEpisode: &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 2, Title: "Second"},
	}}, nil
}
func (*fakeHistory) ListPlaybackProgress(string) ([]models.PlaybackProgress, error) {
	return []models.PlaybackProgress{
		{ID: "movie:tmdb:movie:603", MediaType: "movie", ItemID: "tmdb:movie:603", MovieName: "The Matrix", Position: 600, Duration: 8160, PercentWatched: 7, ExternalIDs: map[string]string{"tmdb": "603"}, UpdatedAt: time.Now()},
		// mediastorm records progress for live HLS sessions too; a channel
		// is not a title and must stay out of Resume and My Movies.
		{ID: "live:daserste.de", MediaType: "live", ItemID: "DasErste.de", MovieName: "ARD", Position: 6, Duration: 86406, UpdatedAt: time.Now().Add(time.Minute)},
	}, nil
}
func (h *fakeHistory) ListWatchHistory(string) ([]models.WatchHistoryItem, error) {
	h.mu.Lock()
	rows, hook := append([]models.WatchHistoryItem(nil), h.watched...), h.onList
	h.mu.Unlock()
	if hook != nil {
		hook()
	}
	return rows, nil
}
func (h *fakeHistory) UpdateWatchHistory(_ string, u models.WatchHistoryUpdate) (models.WatchHistoryItem, error) {
	item := models.WatchHistoryItem{MediaType: u.MediaType, ItemID: u.ItemID, ExternalIDs: u.ExternalIDs, SeasonNumber: u.SeasonNumber, EpisodeNumber: u.EpisodeNumber, SeriesID: u.SeriesID}
	if u.Watched != nil {
		item.Watched = *u.Watched
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, w := range h.watched {
		if w.MediaType == item.MediaType && w.ItemID == item.ItemID {
			h.watched[i] = item
			return item, nil
		}
	}
	h.watched = append(h.watched, item)
	return item, nil
}
func (h *fakeHistory) UpdatePlaybackProgressContext(_ context.Context, _ string, u models.PlaybackProgressUpdate) (models.PlaybackProgress, error) {
	h.progress = append(h.progress, u)
	return models.PlaybackProgress{}, nil
}

type fakeWatchlist struct{ items []models.WatchlistItem }

func (w *fakeWatchlist) List(string) ([]models.WatchlistItem, error) { return w.items, nil }
func (w *fakeWatchlist) AddOrUpdate(_ string, in models.WatchlistUpsert) (models.WatchlistItem, error) {
	item := models.WatchlistItem{ID: in.ID, MediaType: in.MediaType, Name: in.Name, ExternalIDs: in.ExternalIDs}
	w.items = append(w.items, item)
	return item, nil
}
func (w *fakeWatchlist) Remove(_, mediaType, id string) (bool, error) {
	for i, it := range w.items {
		if it.MediaType == mediaType && it.ID == id {
			w.items = append(w.items[:i], w.items[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

// --- harness ---------------------------------------------------------------

type harness struct {
	t        *testing.T
	srv      *Server
	handler  http.Handler
	accounts *accounts.Service
	users    *users.Service
	sessions *sessions.Service
	history  *fakeHistory
	alice    models.User
}

func newHarness(t *testing.T, cfg config.JellyfinBridgeSettings) *harness {
	t.Helper()
	dir := t.TempDir()
	acc, err := accounts.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	usr, err := users.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := sessions.NewService(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	alice, err := acc.Create("alice", "alice-pass")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := acc.Create("bob", "bob-pass")
	if err != nil {
		t.Fatal(err)
	}
	aliceProfile, err := usr.CreateForAccount(alice.ID, "Living Room")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := usr.CreateForAccount(bob.ID, "Living Room"); err != nil {
		t.Fatal(err)
	}
	if _, err := usr.CreateForAccount(bob.ID, "Kids"); err != nil {
		t.Fatal(err)
	}
	hist := &fakeHistory{}
	if cfg.ServerName == "" {
		cfg.ServerName = "test"
	}
	srv := New(Deps{
		Accounts:  acc,
		Users:     usr,
		Sessions:  sess,
		Metadata:  fakeMetadata{},
		History:   hist,
		Watchlist: &fakeWatchlist{items: []models.WatchlistItem{{ID: "tmdb:movie:603", MediaType: "movie", Name: "The Matrix", ExternalIDs: map[string]string{"tmdb": "603"}, PosterURL: "https://image.tmdb.org/t/p/w500/matrix.jpg"}}},
	}, cfg)
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return &harness{t: t, srv: srv, handler: srv.Handler(), accounts: acc, users: usr, sessions: sess, history: hist, alice: aliceProfile}
}

func (h *harness) do(method, path, token string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	auth := `MediaBrowser Client="Jellyfin Web", Device="Firefox", DeviceId="dev1", Version="10.11.0"`
	if token != "" {
		auth += `, Token="` + token + `"`
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func (h *harness) login(username, password string) string {
	h.t.Helper()
	rec := h.do(http.MethodPost, "/Users/AuthenticateByName", "", map[string]string{"Username": username, "Pw": password})
	if rec.Code != http.StatusOK {
		h.t.Fatalf("login %q: status %d: %s", username, rec.Code, rec.Body.String())
	}
	var res dto.AuthenticationResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		h.t.Fatal(err)
	}
	return res.AccessToken
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %T: %v\n%s", v, err, rec.Body.String())
	}
	return v
}

// --- tests -----------------------------------------------------------------

func TestParseAuthorization(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   clientInfo
		ok     bool
	}{
		{"web", `MediaBrowser Client="Jellyfin Web", Device="Firefox", DeviceId="abc", Version="10.11.0", Token="tok"`,
			clientInfo{Client: "Jellyfin Web", Device: "Firefox", DeviceID: "abc", Version: "10.11.0", Token: "tok"}, true},
		{"kotlin sdk url-encoded", `MediaBrowser Client="Android%20TV", Version="0.19.0", DeviceId="x%3Dy", Device="Shield", Token="t"`,
			clientInfo{Client: "Android TV", Device: "Shield", DeviceID: "x=y", Version: "0.19.0", Token: "t"}, true},
		{"emby scheme", `Emby Client=Infuse, Token=abc`, clientInfo{Client: "Infuse", Token: "abc"}, true},
		{"comma in quotes", `MediaBrowser Device="Bob, TV", Token="t"`, clientInfo{Device: "Bob, TV", Token: "t"}, true},
		{"bearer is not jellyfin", `Bearer abc`, clientInfo{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseAuthorization(tc.header)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("got %+v ok=%v, want %+v ok=%v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestRequestClientInfoTokenSources(t *testing.T) {
	cases := map[string]func(*http.Request){
		"X-Emby-Token":         func(r *http.Request) { r.Header.Set("X-Emby-Token", "tok") },
		"X-MediaBrowser-Token": func(r *http.Request) { r.Header.Set("X-MediaBrowser-Token", "tok") },
		"X-Emby-Authorization": func(r *http.Request) { r.Header.Set("X-Emby-Authorization", `MediaBrowser Token="tok"`) },
		"ApiKey query":         func(r *http.Request) { r.URL.RawQuery = "ApiKey=tok" },
		"api_key query":        func(r *http.Request) { r.URL.RawQuery = "api_key=tok" },
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/items", nil)
			set(req)
			if got := requestClientInfo(req).Token; got != "tok" {
				t.Fatalf("token = %q", got)
			}
		})
	}
}

func TestDeriveIDStableAndParseable(t *testing.T) {
	a := deriveID(refMovie, "tmdb:movie:603")
	if a != deriveID(refMovie, "tmdb:movie:603") || len(a) != 32 {
		t.Fatalf("unstable or malformed id %q", a)
	}
	if a == deriveID(refSeries, "tmdb:movie:603") {
		t.Fatal("kind must be part of the id")
	}
	dashed := a[:8] + "-" + a[8:12] + "-" + a[12:16] + "-" + a[16:20] + "-" + a[20:]
	if normalizeID(strings.ToUpper(dashed)) != a {
		t.Fatal("dashed/uppercase GUID did not normalize")
	}
	if normalizeID("not-a-guid") != "" {
		t.Fatal("garbage accepted")
	}
}

func TestIDStorePersists(t *testing.T) {
	path := t.TempDir() + "/ids.json"
	s1 := newIDStore(path)
	id := s1.put(refMovie, "tmdb:movie:603", refContext{Name: "The Matrix", Poster: "p"})
	s1.put(refMovie, "tmdb:movie:603", refContext{Year: 1999}) // sparse update keeps earlier context
	s1.close()
	s2 := newIDStore(path)
	defer s2.close()
	ref, ok := s2.get(id)
	if !ok || ref.Source != "tmdb:movie:603" || ref.Ctx.Name != "The Matrix" || ref.Ctx.Year != 1999 || ref.Ctx.Poster != "p" {
		t.Fatalf("ref after reload = %+v ok=%v", ref, ok)
	}
}

func TestPublicSystemInfoShape(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	rec := h.do(http.MethodGet, "/System/Info/Public", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var raw map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	if raw["ProductName"] != "Jellyfin Server" || raw["Version"] != ServerVersion || raw["StartupWizardCompleted"] != true {
		t.Fatalf("unexpected public info: %v", raw)
	}
	if id, _ := raw["Id"].(string); normalizeID(id) != id || id == "" {
		t.Fatalf("server id %q is not an N-format GUID", id)
	}
	// Branding must be 200 (Android TV fails "add server" on 404).
	if rec := h.do(http.MethodGet, "/Branding/Configuration", "", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"SplashscreenEnabled":false`) {
		t.Fatalf("branding: %d %s", rec.Code, rec.Body.String())
	}
}

func TestLoginResolution(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{AllowPinLogin: true})

	// "Living Room" exists on two accounts: the password picks the right one.
	if tok := h.login("Living Room", "alice-pass"); tok == "" {
		t.Fatal("empty token")
	}
	if tok := h.login("bob/living room", "bob-pass"); tok == "" {
		t.Fatal("account/profile login failed")
	}
	// The display name (account/profile) works too.
	tagged := h.srv.displayNames()
	for _, p := range h.users.ListAll() {
		if p.Name == "Living Room" && p.AccountID != h.alice.AccountID {
			if tok := h.login(tagged[p.ID], "bob-pass"); tok == "" {
				t.Fatal("tagged display name login failed")
			}
		}
	}
	if tok := h.login("bob/Kids", "bob-pass"); tok == "" {
		t.Fatal("account/profile login failed")
	}
	if rec := h.do(http.MethodPost, "/Users/AuthenticateByName", "", map[string]string{"Username": "Living Room", "Pw": "wrong"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: status %d", rec.Code)
	}
	// A profile without a PIN must not accept arbitrary passwords via the PIN path.
	if rec := h.do(http.MethodPost, "/Users/AuthenticateByName", "", map[string]string{"Username": "Kids", "Pw": "1234"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("PIN-less profile accepted a PIN: status %d", rec.Code)
	}
	// With a PIN set, the PIN works when AllowPinLogin is on.
	for _, p := range h.users.ListAll() {
		if p.Name == "Kids" {
			if _, err := h.users.SetPin(p.ID, "4321"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if tok := h.login("Kids", "4321"); tok == "" {
		t.Fatal("PIN login failed")
	}
}

func TestPublicUsersList(t *testing.T) {
	// Off by default: the list would name account logins.
	h := newHarness(t, config.JellyfinBridgeSettings{})
	if list := decode[[]dto.UserDto](t, h.do(http.MethodGet, "/Users/Public", "", nil)); len(list) != 0 {
		t.Fatalf("hidden list returned %d users", len(list))
	}
	h = newHarness(t, config.JellyfinBridgeSettings{ShowUserList: true})
	list := decode[[]dto.UserDto](t, h.do(http.MethodGet, "/Users/Public", "", nil))
	names := map[string]bool{}
	for _, u := range list {
		if u.Policy != nil {
			t.Fatal("public users must not include policy")
		}
		if names[u.Name] {
			t.Fatalf("duplicate public user name %q", u.Name)
		}
		names[u.Name] = true
	}
	if !names["alice/Living Room"] || !names["bob/Living Room"] || !names["bob/Kids"] {
		t.Fatalf("names = %v", names)
	}
}

func TestAuthenticatedFlow(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	if rec := h.do(http.MethodGet, "/Users/Me", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /Users/Me: %d", rec.Code)
	}
	tok := h.login("Living Room", "alice-pass")

	me := decode[dto.UserDto](t, h.do(http.MethodGet, "/users/me", tok, nil))
	if me.Policy == nil || me.Policy.IsAdministrator || me.Configuration == nil || me.ID != deriveID(refUser, h.alice.ID) {
		t.Fatalf("me = %+v", me)
	}
	// Mixed-case paths route to the same handler.
	if rec := h.do(http.MethodGet, "/USERS/ME", tok, nil); rec.Code != http.StatusOK {
		t.Fatalf("mixed case: %d", rec.Code)
	}

	views := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/UserViews?userId="+me.ID, tok, nil))
	var moviesView, trendingView string
	for _, v := range views.Items {
		switch v.Name {
		case "My Movies":
			moviesView = v.ID
		case "Trending Movies":
			trendingView = v.ID
		}
	}
	if moviesView == "" || trendingView == "" {
		t.Fatalf("views = %+v", views.Items)
	}

	movies := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Items?ParentId="+moviesView+"&IncludeItemTypes=Movie&Recursive=true&SortBy=SortName", tok, nil))
	if movies.TotalRecordCount != 1 || movies.Items[0].Name != "The Matrix" || movies.Items[0].Type != dto.KindMovie {
		t.Fatalf("movies = %+v", movies)
	}
	matrix := movies.Items[0]
	if matrix.UserData == nil || !matrix.UserData.IsFavorite || matrix.UserData.PlaybackPositionTicks != 600*ticksPerSecond {
		t.Fatalf("user data = %+v", matrix.UserData)
	}
	if matrix.LocationType != dto.LocationFileSystem || matrix.MediaType != dto.MediaTypeVideo {
		t.Fatal("movies must be playable (FileSystem/Video) for jellyfin-web's canPlay")
	}

	// Paging keeps TotalRecordCount and never emits null Items.
	paged := h.do(http.MethodGet, "/Items?ParentId="+trendingView+"&StartIndex=5&Limit=10", tok, nil)
	if !strings.Contains(paged.Body.String(), `"Items":[]`) {
		t.Fatalf("paged = %s", paged.Body.String())
	}

	// Item details by GUID.
	detail := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+matrix.ID, tok, nil))
	if detail.Overview != "details" || detail.RunTimeTicks == nil {
		t.Fatalf("detail = %+v", detail)
	}

	// Search hits metadata.
	search := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Items?searchTerm=matrix&IncludeItemTypes=Movie&Recursive=true", tok, nil))
	if search.TotalRecordCount != 1 {
		t.Fatalf("search = %+v", search)
	}

	// Resume and NextUp.
	resume := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/UserItems/Resume?MediaTypes=Video", tok, nil))
	if resume.TotalRecordCount != 1 || resume.Items[0].ID != matrix.ID {
		t.Fatalf("resume = %+v", resume)
	}
	next := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Shows/NextUp", tok, nil))
	if next.TotalRecordCount != 1 || next.Items[0].Type != dto.KindEpisode || next.Items[0].IndexNumber == nil || *next.Items[0].IndexNumber != 2 {
		t.Fatalf("nextup = %+v", next)
	}

	// Series → seasons → episodes; unaired episodes are hidden.
	seriesID := next.Items[0].SeriesID
	seasons := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Shows/"+seriesID+"/Seasons", tok, nil))
	if seasons.TotalRecordCount != 1 {
		t.Fatalf("seasons = %+v", seasons)
	}
	eps := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Shows/"+seriesID+"/Episodes?SeasonId="+seasons.Items[0].ID, tok, nil))
	if eps.TotalRecordCount != 2 {
		t.Fatalf("episodes = %d, want 2 aired", eps.TotalRecordCount)
	}
	if eps.Items[1].ID != next.Items[0].ID {
		t.Fatal("episode GUIDs must be stable across endpoints")
	}

	// Latest is a plain array.
	if rec := h.do(http.MethodGet, "/Items/Latest?ParentId="+moviesView, tok, nil); !strings.HasPrefix(strings.TrimSpace(rec.Body.String()), "[") {
		t.Fatalf("latest must be an array: %s", rec.Body.String())
	}

	// Favorite toggle round-trips through the watchlist.
	ud := decode[dto.UserItemData](t, h.do(http.MethodDelete, "/UserFavoriteItems/"+matrix.ID, tok, nil))
	if ud.IsFavorite {
		t.Fatal("unfavorite did not stick")
	}

	// Progress reports reach history in seconds.
	h.do(http.MethodPost, "/Sessions/Playing/Progress", tok, map[string]any{"ItemId": matrix.ID, "PositionTicks": int64(90) * ticksPerSecond})
	if len(h.history.progress) != 1 || h.history.progress[0].Position != 90 || h.history.progress[0].ItemID != "tmdb:movie:603" {
		t.Fatalf("progress = %+v", h.history.progress)
	}

	// Logout revokes the token.
	h.do(http.MethodPost, "/Sessions/Logout", tok, nil)
	if rec := h.do(http.MethodGet, "/Users/Me", tok, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token still valid after logout: %d", rec.Code)
	}
}

func TestNonBridgeTokenRejected(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	normal, err := h.sessions.Create(h.alice.AccountID, false, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if rec := h.do(http.MethodGet, "/Users/Me", normal.Token, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a full-access mediastorm token must not work on the bridge: %d", rec.Code)
	}
}

func TestProfileIsolation(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	aliceTok := h.login("Living Room", "alice-pass")
	bobTok := h.login("bob/Kids", "bob-pass")
	views := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/UserViews", aliceTok, nil))
	for _, v := range views.Items {
		if rec := h.do(http.MethodGet, "/Items/"+v.ID, bobTok, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("bob can read alice's view %s: %d", v.Name, rec.Code)
		}
		res := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Items?ParentId="+v.ID, bobTok, nil))
		if res.TotalRecordCount != 0 {
			t.Fatalf("bob can list alice's view %s", v.Name)
		}
	}
}

func TestMediaSourceWireShape(t *testing.T) {
	raw, _ := json.Marshal(dto.MediaSourceInfo{ID: "x", Protocol: "File", Type: "Default", RequiredHTTPHeaders: map[string]string{}, MediaStreams: []dto.MediaStream{}})
	for _, field := range []string{`"RequiredHttpHeaders":{}`, `"SupportsDirectPlay":false`, `"IsInfiniteStream":false`, `"TranscodingSubProtocol":"http"`, `"HasSegments":false`, `"MediaStreams":[]`} {
		if !bytes.Contains(raw, []byte(field)) {
			t.Errorf("MediaSourceInfo JSON missing %s: %s", field, raw)
		}
	}
	raw, _ = json.Marshal(dto.NewQueryResult[dto.BaseItemDto](nil, 0, 0))
	if string(raw) != `{"Items":[],"TotalRecordCount":0,"StartIndex":0}` {
		t.Fatalf("empty QueryResult = %s", raw)
	}
}

func TestKidsProfiles(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	var kidsProfile models.User
	for _, p := range h.users.ListAll() {
		if p.Name == "Kids" {
			kidsProfile = p
		}
	}
	if _, err := h.users.SetKidsProfile(kidsProfile.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.users.SetKidsMode(kidsProfile.ID, "rating"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.users.SetKidsMaxMovieRating(kidsProfile.ID, "PG"); err != nil {
		t.Fatal(err)
	}
	tok := h.login("bob/Kids", "bob-pass")
	// Search results are enriched to "R" by the fake and must be filtered out.
	search := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Items?searchTerm=matrix&IncludeItemTypes=Movie&Recursive=true", tok, nil))
	if search.TotalRecordCount != 0 {
		t.Fatalf("R-rated search result shown to a PG kids profile: %+v", search.Items)
	}
	// Trending is enriched to "PG" and stays visible.
	views := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/UserViews", tok, nil))
	trending := ""
	for _, v := range views.Items {
		if v.Name == "Trending Movies" {
			trending = v.ID
		}
	}
	if trending == "" {
		t.Fatal("rating-mode kids profile should keep trending")
	}
	if got := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Items?ParentId="+trending, tok, nil)); got.TotalRecordCount != 1 {
		t.Fatalf("PG trending hidden: %+v", got)
	}

	// Curated (content_list) kids profiles get no search and no trending.
	if _, err := h.users.SetKidsMode(kidsProfile.ID, "content_list"); err != nil {
		t.Fatal(err)
	}
	h.srv.views.invalidate(kidsProfile.ID)
	views = decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/UserViews", tok, nil))
	for _, v := range views.Items {
		if strings.HasPrefix(v.Name, "Trending") {
			t.Fatal("curated kids profile sees trending")
		}
	}
	search = decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Items?searchTerm=matrix&IncludeItemTypes=Movie&Recursive=true", tok, nil))
	if search.TotalRecordCount != 0 {
		t.Fatal("curated kids profile can search")
	}
}

func TestLoginRateLimitPerUser(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	for i := 0; i < loginMaxFailures; i++ {
		req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{"Username":"bob/Kids","Pw":"nope"}`))
		req.RemoteAddr = fmt.Sprintf("203.0.113.%d:1234", i+1) // a different address every time
		req.Header.Set("X-Forwarded-For", "198.51.100.7")      // ignored: not from loopback
		h.handler.ServeHTTP(httptest.NewRecorder(), req)
	}
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{"Username":"bob/Kids","Pw":"bob-pass"}`))
	req.RemoteAddr = "203.0.113.200:1234"
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("per-user limit not applied across addresses: %d", rec.Code)
	}
}

func TestEnforcedProfilePinNeedsPasswordAndPin(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	h.srv.deps.ProfilePinsEnforced = func() bool { return true }
	for _, p := range h.users.ListAll() {
		if p.Name == "Kids" {
			if _, err := h.users.SetPin(p.ID, "4321"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if rec := h.do(http.MethodPost, "/Users/AuthenticateByName", "", map[string]string{"Username": "bob/Kids", "Pw": "bob-pass"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("account password alone opened a PIN-locked profile: %d", rec.Code)
	}
	if tok := h.login("bob/Kids", "bob-pass4321"); tok == "" {
		t.Fatal("password+PIN rejected")
	}
}

func TestKidsCannotOpenTitlesByID(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	var kidsProfile models.User
	for _, p := range h.users.ListAll() {
		if p.Name == "Kids" {
			kidsProfile = p
		}
	}
	_, _ = h.users.SetKidsProfile(kidsProfile.ID, true)
	_, _ = h.users.SetKidsMode(kidsProfile.ID, "content_list")
	// An adult profile surfaces a trending title (not in any curated list),
	// which registers its GUID.
	adult := h.login("Living Room", "alice-pass")
	views := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/UserViews", adult, nil))
	var inception string
	for _, v := range views.Items {
		if v.Name == "Trending Movies" {
			items := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Items?ParentId="+v.ID, adult, nil))
			inception = items.Items[0].ID
		}
	}
	kid := h.login("bob/Kids", "bob-pass")
	if rec := h.do(http.MethodGet, "/Items/"+inception, kid, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("curated kids profile opened an uncurated title: %d", rec.Code)
	}
	if rec := h.do(http.MethodPost, "/Items/"+inception+"/PlaybackInfo", kid, map[string]any{}); rec.Code != http.StatusNotFound {
		t.Fatalf("curated kids profile got PlaybackInfo for an uncurated title: %d", rec.Code)
	}
}

func TestPagingBounds(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	tok := h.login("Living Room", "alice-pass")
	for _, path := range []string{
		"/Items/Latest?Limit=-1",
		"/Items?IncludeItemTypes=Movie&Recursive=true&StartIndex=1&Limit=9223372036854775807",
		"/Items?IncludeItemTypes=Movie&Recursive=true&StartIndex=-5&Limit=-3",
	} {
		if rec := h.do(http.MethodGet, path, tok, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
}

func TestBridgeTokenCannotUseMainAPIValidate(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	tok := h.login("Living Room", "alice-pass")
	if _, err := h.sessions.Validate(tok); err == nil {
		t.Fatal("sessions.Validate accepted a Jellyfin bridge token (admin/account UIs would trust it)")
	}
}

func TestDecodePlaybackInfoRequestLenient(t *testing.T) {
	req := decodePlaybackInfoRequest([]byte(`{"SubtitleStreamIndex":"-1","AudioStreamIndex":2,"StartTimeTicks":"600000000","EnableDirectPlay":true,"MediaSourceId":"abc"}`))
	if req.SubtitleStreamIndex == nil || *req.SubtitleStreamIndex != -1 || req.AudioStreamIndex == nil || *req.AudioStreamIndex != 2 {
		t.Fatalf("indexes = %v %v", req.SubtitleStreamIndex, req.AudioStreamIndex)
	}
	if req.StartTimeTicks == nil || *req.StartTimeTicks != 600000000 || req.EnableDirectPlay == nil || !*req.EnableDirectPlay || req.MediaSourceID != "abc" {
		t.Fatalf("req = %+v", req)
	}
}

type fakeEPG struct{}

func (fakeEPG) GetScheduleMultiple(ids []string, start, end time.Time) map[string][]models.EPGProgram {
	now := time.Now().Truncate(time.Minute)
	out := map[string][]models.EPGProgram{}
	for _, id := range ids {
		out[id] = []models.EPGProgram{{ChannelID: id, Title: "News at Now", Start: now.Add(-10 * time.Minute), Stop: now.Add(20 * time.Minute), Categories: []string{"News"}}}
	}
	return out
}

func TestLiveTV(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	h.srv.deps.EPG = fakeEPG{}
	h.srv.deps.Handlers.LiveChannels = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("profileId") == "" {
			http.Error(w, "missing profile", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"channels": []map[string]string{
			{"id": "ch-1", "name": "Channel One", "url": "http://tv.example/1.ts", "tvgId": "one", "logo": "https://img.example/1.png"},
			{"id": "ch-2", "name": "Channel Two", "url": "http://tv.example/2.ts"},
		}})
	}
	liveChannelCache.mu.Lock()
	liveChannelCache.entries = map[string]channelCacheEntry{}
	liveChannelCache.mu.Unlock()
	tok := h.login("Living Room", "alice-pass")

	views := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/UserViews", tok, nil))
	found := false
	for _, v := range views.Items {
		found = found || v.CollectionType == dto.CollectionLiveTV
	}
	if !found {
		t.Fatal("Live TV view missing when channels exist")
	}

	channels := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/LiveTv/Channels?AddCurrentProgram=true", tok, nil))
	if channels.TotalRecordCount != 2 || channels.Items[0].Type != dto.KindTvChannel || channels.Items[0].CurrentProgram == nil {
		t.Fatalf("channels = %+v", channels)
	}
	if channels.Items[0].CurrentProgram.Name != "News at Now" || channels.Items[1].CurrentProgram != nil {
		t.Fatal("current program mapping wrong")
	}

	programs := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodPost, "/LiveTv/Programs", tok, map[string]any{"ChannelIds": []string{channels.Items[0].ID}}))
	if programs.TotalRecordCount != 1 || programs.Items[0].Type != dto.KindProgram || programs.Items[0].ChannelID != channels.Items[0].ID {
		t.Fatalf("programs = %+v", programs)
	}

	// Program details resolve, and playing a channel yields an infinite HLS source.
	if rec := h.do(http.MethodGet, "/Items/"+programs.Items[0].ID, tok, nil); rec.Code != http.StatusOK {
		t.Fatalf("program details: %d", rec.Code)
	}
	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+channels.Items[0].ID+"/PlaybackInfo", tok, map[string]any{}))
	if len(info.MediaSources) != 1 || !info.MediaSources[0].IsInfiniteStream || info.MediaSources[0].RequiresOpening || info.MediaSources[0].TranscodingSubProtocol != "hls" || info.PlaySessionID == "" {
		t.Fatalf("live playback info = %+v", info)
	}
	if info.MediaSources[0].SupportsDirectPlay {
		t.Fatal("web clients must not get direct play for live TS")
	}

	// The tune asks for mediastorm's timeshift window, so players can rewind.
	var startQuery url.Values
	h.srv.deps.Handlers.LiveHLSStart = func(w http.ResponseWriter, r *http.Request) {
		startQuery = r.URL.Query()
		writeJSON(w, http.StatusOK, map[string]any{"sessionId": "live1", "playlistUrl": "/video/hls/live1/stream.m3u8"})
	}
	if rec := h.do(http.MethodGet, info.MediaSources[0].TranscodingURL, "", nil); rec.Code != http.StatusFound {
		t.Fatalf("live master playlist = %d", rec.Code)
	}
	if startQuery.Get("timeshift") != "1" || startQuery.Get("url") != "http://tv.example/1.ts" {
		t.Fatalf("live HLS start query = %v", startQuery)
	}
	// jellyfin-web's live seek bar counts media time from the moment playback started: no backlog.
	if startQuery.Get("timeshiftBacklog") != "0" {
		t.Fatalf("live HLS start asks for the source's DVR backlog: %v", startQuery)
	}
	// Full pass-through keeps the channel's own audio: no timeshift.
	h.srv.deps.Settings = func() (config.JellyfinBridgeSettings, error) {
		return config.JellyfinBridgeSettings{DisableVideoTranscoding: true, DisableAudioTranscoding: true}, nil
	}
	h.srv.live.mu.Lock()
	h.srv.live.loaded = time.Time{}
	h.srv.live.mu.Unlock()
	if h.srv.liveTimeshift() {
		t.Fatal("timeshift requested with audio and video pass-through")
	}
}

// Infuse fetches /Library/VirtualFolders while adding a server and fails the
// whole add on a 404.
func TestLibraryVirtualFolders(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	tok := h.login("Living Room", "alice-pass")

	views := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/UserViews", tok, nil))
	viewIDs := map[string]string{}
	for _, v := range views.Items {
		viewIDs[v.Name] = v.ID
	}

	folders := decode[[]dto.VirtualFolderInfo](t, h.do(http.MethodGet, "/Library/VirtualFolders", tok, nil))
	if len(folders) == 0 {
		t.Fatal("no virtual folders")
	}
	for _, f := range folders {
		if f.CollectionType == dto.CollectionLiveTV {
			t.Fatalf("live TV listed as a library: %+v", f)
		}
		if f.ItemID == "" || f.ItemID != viewIDs[f.Name] || len(f.Locations) == 0 || f.LibraryOptions == nil {
			t.Fatalf("folder = %+v, view ids %v", f, viewIDs)
		}
	}

	media := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Library/MediaFolders", tok, nil))
	if media.TotalRecordCount != len(folders) {
		t.Fatalf("media folders = %d, virtual folders = %d", media.TotalRecordCount, len(folders))
	}
	if rec := h.do(http.MethodGet, "/Library/VirtualFolders", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", rec.Code)
	}
}
