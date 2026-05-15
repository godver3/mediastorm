package scheduler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/history"
	"novastream/services/jellyfin"
	"novastream/services/plex"
	"novastream/services/simkl"
	"novastream/services/trakt"
	"novastream/services/watchlist"
)

type fakeSchedulerUsersProvider struct {
	users map[string]models.User
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type fakeLocalMediaScanner struct {
	libraries []models.LocalMediaLibrary
	summaries map[string]models.LocalMediaScanSummary
	scanned   []string
}

func (f *fakeLocalMediaScanner) ListLibraries(ctx context.Context) ([]models.LocalMediaLibrary, error) {
	return append([]models.LocalMediaLibrary(nil), f.libraries...), nil
}

func (f *fakeLocalMediaScanner) StartScan(ctx context.Context, libraryID string) (models.LocalMediaScanSummary, error) {
	f.scanned = append(f.scanned, libraryID)
	return f.summaries[libraryID], nil
}

func (f *fakeSchedulerUsersProvider) Exists(id string) bool {
	_, ok := f.users[id]
	return ok
}

func (f *fakeSchedulerUsersProvider) ListAll() []models.User {
	result := make([]models.User, 0, len(f.users))
	for _, user := range f.users {
		result = append(result, user)
	}
	return result
}

func TestSimklAllItemsToWatchHistoryParsesMoviesAndEpisodes(t *testing.T) {
	watched := true
	resp := &simkl.AllItemsResponse{
		Movies: []json.RawMessage{
			json.RawMessage(`{"status":"completed","movie":{"title":"Inception","year":2010,"ids":{"imdb":"tt1375666","tmdb":27205}},"last_watched_at":"2026-05-15T12:00:00Z"}`),
		},
		Shows: []json.RawMessage{
			json.RawMessage(`{"status":"watching","show":{"title":"The Show","year":2020,"ids":{"tvdb":12345}},"seasons":[{"number":1,"episodes":[{"number":2,"title":"Pilot","watched_at":"2026-05-15T13:00:00Z"}]}]}`),
		},
	}

	updates := simklAllItemsToWatchHistory(resp, &watched)
	if len(updates) != 2 {
		t.Fatalf("updates len = %d, want 2", len(updates))
	}
	if updates[0].MediaType != "movie" || updates[0].ItemID != "tmdb:movie:27205" {
		t.Fatalf("movie update = %+v", updates[0])
	}
	if updates[1].MediaType != "episode" || updates[1].ItemID != "tvdb:series:12345:s01e02" {
		t.Fatalf("episode update = %+v", updates[1])
	}
}

func TestResolveProfileID(t *testing.T) {
	t.Run("existing profile passes through", func(t *testing.T) {
		svc := &Service{
			usersService: &fakeSchedulerUsersProvider{
				users: map[string]models.User{
					"prof-1": {ID: "prof-1", Name: "Primary Profile"},
				},
			},
		}

		got, err := svc.resolveProfileID("prof-1")
		if err != nil {
			t.Fatalf("resolveProfileID() error = %v", err)
		}
		if got != "prof-1" {
			t.Fatalf("resolveProfileID() = %q, want %q", got, "prof-1")
		}
	})

	t.Run("legacy default resolves to sole profile", func(t *testing.T) {
		svc := &Service{
			usersService: &fakeSchedulerUsersProvider{
				users: map[string]models.User{
					"uuid-1": {ID: "uuid-1", Name: "Only Profile"},
				},
			},
		}

		got, err := svc.resolveProfileID(models.DefaultUserID)
		if err != nil {
			t.Fatalf("resolveProfileID() error = %v", err)
		}
		if got != "uuid-1" {
			t.Fatalf("resolveProfileID() = %q, want %q", got, "uuid-1")
		}
	})

	t.Run("legacy default resolves to primary profile by name", func(t *testing.T) {
		svc := &Service{
			usersService: &fakeSchedulerUsersProvider{
				users: map[string]models.User{
					"uuid-1": {ID: "uuid-1", Name: "Kids"},
					"uuid-2": {ID: "uuid-2", Name: models.DefaultUserName},
				},
			},
		}

		got, err := svc.resolveProfileID(models.DefaultUserID)
		if err != nil {
			t.Fatalf("resolveProfileID() error = %v", err)
		}
		if got != "uuid-2" {
			t.Fatalf("resolveProfileID() = %q, want %q", got, "uuid-2")
		}
	})

	t.Run("unknown non-legacy profile fails", func(t *testing.T) {
		svc := &Service{
			usersService: &fakeSchedulerUsersProvider{
				users: map[string]models.User{
					"uuid-1": {ID: "uuid-1", Name: models.DefaultUserName},
				},
			},
		}

		_, err := svc.resolveProfileID("missing")
		if err == nil {
			t.Fatal("resolveProfileID() error = nil, want error")
		}
		if !strings.Contains(err.Error(), `profile "missing" not found`) {
			t.Fatalf("resolveProfileID() error = %v, want missing profile error", err)
		}
	})
}

func TestIsNewerWatchState_PrefersExplicitUnwatchedOnTimestampTie(t *testing.T) {
	ts := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)
	watched := &models.WatchHistoryItem{
		ID:        "episode:show:s01e01",
		Watched:   true,
		WatchedAt: ts,
		UpdatedAt: ts,
	}
	unwatched := &models.WatchHistoryItem{
		ID:        "episode:show:s01e01:manual",
		Watched:   false,
		WatchedAt: ts,
		UpdatedAt: ts,
	}

	if !isNewerWatchState(unwatched, watched) {
		t.Fatal("expected explicit unwatched state to win on equal timestamps")
	}
	if isNewerWatchState(watched, unwatched) {
		t.Fatal("expected watched state to lose on equal timestamps against explicit unwatched state")
	}
}

func TestIsNewerWatchState_PrefersNewerTimestamp(t *testing.T) {
	older := &models.WatchHistoryItem{
		ID:        "episode:show:s01e01",
		Watched:   false,
		WatchedAt: time.Date(2026, 3, 24, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 24, 12, 0, 0, 0, time.UTC),
	}
	newer := &models.WatchHistoryItem{
		ID:        "episode:show:s01e01",
		Watched:   true,
		WatchedAt: time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC),
	}

	if !isNewerWatchState(newer, older) {
		t.Fatal("expected newer timestamp to win")
	}
	if isNewerWatchState(older, newer) {
		t.Fatal("expected older timestamp to lose")
	}
}

func TestLatestWatchStateForItem_PrefersNewestStateAcrossIDVariants(t *testing.T) {
	dir := t.TempDir()
	historySvc, err := history.NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	svc := &Service{historyService: historySvc}
	userID := "user-1"
	watched := true
	unwatched := false
	ts := time.Now().UTC().Add(-2 * time.Hour)

	if _, err := historySvc.UpdateWatchHistory(userID, models.WatchHistoryUpdate{
		MediaType:     "episode",
		ItemID:        "tmdb:tv:100:s03e11",
		Watched:       &watched,
		WatchedAt:     ts,
		SeriesID:      "tmdb:tv:100",
		SeriesName:    "Ragnarok",
		SeasonNumber:  3,
		EpisodeNumber: 11,
		ExternalIDs:   map[string]string{"tmdb": "100", "tvdb": "393810"},
	}); err != nil {
		t.Fatalf("UpdateWatchHistory() watched error = %v", err)
	}

	if _, err := historySvc.UpdateWatchHistory(userID, models.WatchHistoryUpdate{
		MediaType:     "episode",
		ItemID:        "tvdb:series:393810:s03e11",
		Watched:       &unwatched,
		SeriesID:      "tvdb:series:393810",
		SeriesName:    "Ragnarok",
		SeasonNumber:  3,
		EpisodeNumber: 11,
		ExternalIDs:   map[string]string{"tmdb": "100", "tvdb": "393810"},
	}); err != nil {
		t.Fatalf("UpdateWatchHistory() unwatched error = %v", err)
	}

	item, err := svc.latestWatchStateForItem(userID, "episode", []string{
		"tmdb:tv:100:s03e11",
		"tvdb:series:393810:s03e11",
	})
	if err != nil {
		t.Fatalf("latestWatchStateForItem() error = %v", err)
	}
	if item == nil {
		t.Fatal("expected a watch state")
	}
	if item.Watched {
		t.Fatal("expected newer explicit unwatched state to win across ID variants")
	}
}

func TestSyncPlaybackFromTrakt_SkipsAlreadyWatchedEpisodeAcrossProviderIDs(t *testing.T) {
	dir := t.TempDir()
	historySvc, err := history.NewService(dir)
	if err != nil {
		t.Fatalf("history.NewService() error = %v", err)
	}

	userID := "user-1"
	watched := true
	watchedAt := time.Date(2026, 4, 9, 4, 0, 0, 0, time.UTC)

	if _, err := historySvc.UpdateWatchHistory(userID, models.WatchHistoryUpdate{
		MediaType:     "episode",
		ItemID:        "tmdb:tv:114868:s01e01",
		Watched:       &watched,
		WatchedAt:     watchedAt,
		SeriesID:      "tmdb:tv:114868",
		SeriesName:    "Record of Ragnarok",
		SeasonNumber:  1,
		EpisodeNumber: 1,
		ExternalIDs:   map[string]string{"imdb": "tt13676344", "tmdb": "114868", "tvdb": "393810"},
	}); err != nil {
		t.Fatalf("UpdateWatchHistory() error = %v", err)
	}

	origURL := trakt.GetBaseURLForTest()
	trakt.SetBaseURLForTest("https://trakt.example")
	defer trakt.SetBaseURLForTest(origURL)

	traktClient := trakt.NewClient("id", "secret")
	traktClient.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			var body string
			switch req.URL.Path {
			case "/sync/playback/movies":
				body = `[]`
			case "/sync/playback/episodes":
				body = `[{"id":99,"progress":18.3,"paused_at":"` + watchedAt.Add(10*time.Minute).Format(time.RFC3339) + `","type":"episode","show":{"title":"Record of Ragnarok","ids":{"tvdb":393810,"tmdb":114868,"imdb":"tt13676344"}},"episode":{"season":1,"number":1,"title":"Thor's Hammer"}}]`
			default:
				t.Fatalf("unexpected path %s", req.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	})
	svc := &Service{
		historyService: historySvc,
		traktClient:    traktClient,
	}

	if err := svc.syncPlaybackFromTrakt(&config.TraktAccount{AccessToken: "token"}, userID, nil); err != nil {
		t.Fatalf("syncPlaybackFromTrakt() error = %v", err)
	}

	progress, err := historySvc.ListPlaybackProgress(userID)
	if err != nil {
		t.Fatalf("ListPlaybackProgress() error = %v", err)
	}
	if len(progress) != 0 {
		t.Fatalf("expected stale Trakt playback import to be skipped, got %d progress rows", len(progress))
	}
}

func TestSyncPlaybackFromTrakt_HiddenProgressSameResumePointStaysHidden(t *testing.T) {
	dir := t.TempDir()
	historySvc, err := history.NewService(dir)
	if err != nil {
		t.Fatalf("history.NewService() error = %v", err)
	}

	userID := "user-1"
	seriesID := "tvdb:series:75545"
	pausedAt := time.Date(2026, 3, 20, 14, 50, 20, 0, time.UTC)

	if _, err := historySvc.UpdatePlaybackProgress(userID, models.PlaybackProgressUpdate{
		MediaType:      "episode",
		ItemID:         seriesID + ":s01e12",
		PercentWatched: 8.062,
		Timestamp:      pausedAt,
		IsPaused:       true,
		SeriesID:       seriesID,
		SeriesName:     "Invader ZIM",
		SeasonNumber:   1,
		EpisodeNumber:  12,
		ExternalIDs:    map[string]string{"imdb": "tt0235923", "tmdb": "3793", "tvdb": "75545"},
	}); err != nil {
		t.Fatalf("UpdatePlaybackProgress() error = %v", err)
	}
	if err := historySvc.HideFromContinueWatching(userID, seriesID); err != nil {
		t.Fatalf("HideFromContinueWatching() error = %v", err)
	}

	origURL := trakt.GetBaseURLForTest()
	trakt.SetBaseURLForTest("https://trakt.example")
	defer trakt.SetBaseURLForTest(origURL)

	traktClient := trakt.NewClient("id", "secret")
	traktClient.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			var body string
			switch req.URL.Path {
			case "/sync/playback/movies":
				body = `[]`
			case "/sync/playback/episodes":
				body = `[{"id":99,"progress":8.1,"paused_at":"2026-04-09T11:44:11Z","type":"episode","show":{"title":"Invader ZIM","ids":{"tvdb":75545,"tmdb":3793,"imdb":"tt0235923"}},"episode":{"season":1,"number":12,"title":"A Room with a Moose"}}]`
			default:
				t.Fatalf("unexpected path %s", req.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	svc := &Service{
		historyService: historySvc,
		traktClient:    traktClient,
	}

	if err := svc.syncPlaybackFromTrakt(&config.TraktAccount{AccessToken: "token"}, userID, nil); err != nil {
		t.Fatalf("syncPlaybackFromTrakt() error = %v", err)
	}

	progress, err := historySvc.ListPlaybackProgress(userID)
	if err != nil {
		t.Fatalf("ListPlaybackProgress() error = %v", err)
	}
	if len(progress) != 1 {
		t.Fatalf("expected 1 progress row, got %d", len(progress))
	}
	if !progress[0].HiddenFromContinueWatching {
		t.Fatal("expected hidden progress row to stay hidden when Trakt resume point is unchanged")
	}
}

func TestSyncPlaybackFromTrakt_HiddenMarkerBlocksOlderResumeImport(t *testing.T) {
	dir := t.TempDir()
	historySvc, err := history.NewService(dir)
	if err != nil {
		t.Fatalf("history.NewService() error = %v", err)
	}

	userID := "user-1"
	seriesID := "tvdb:series:81189"
	if err := historySvc.HideFromContinueWatching(userID, seriesID); err != nil {
		t.Fatalf("HideFromContinueWatching() error = %v", err)
	}

	origURL := trakt.GetBaseURLForTest()
	trakt.SetBaseURLForTest("https://trakt.example")
	defer trakt.SetBaseURLForTest(origURL)

	traktClient := trakt.NewClient("id", "secret")
	traktClient.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			var body string
			switch req.URL.Path {
			case "/sync/playback/movies":
				body = `[]`
			case "/sync/playback/episodes":
				body = `[{"id":99,"progress":45.0,"paused_at":"2026-02-28T18:15:47Z","type":"episode","show":{"title":"Breaking Bad","ids":{"tvdb":81189,"tmdb":1396,"imdb":"tt0903747"}},"episode":{"season":1,"number":1,"title":"Pilot"}}]`
			default:
				t.Fatalf("unexpected path %s", req.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	svc := &Service{
		historyService: historySvc,
		traktClient:    traktClient,
	}

	if err := svc.syncPlaybackFromTrakt(&config.TraktAccount{AccessToken: "token"}, userID, nil); err != nil {
		t.Fatalf("syncPlaybackFromTrakt() error = %v", err)
	}

	progress, err := historySvc.ListPlaybackProgress(userID)
	if err != nil {
		t.Fatalf("ListPlaybackProgress() error = %v", err)
	}
	if len(progress) != 1 {
		t.Fatalf("expected hidden marker to remain as the only row, got %d rows", len(progress))
	}
	if progress[0].ItemID != seriesID || !progress[0].HiddenFromContinueWatching {
		t.Fatalf("expected hidden marker for %s to remain, got %+v", seriesID, progress[0])
	}
}

func TestSyncLocalHistoryToTrakt_SkipsCrossProviderMovieAlreadyOnTrakt(t *testing.T) {
	dir := t.TempDir()
	historySvc, err := history.NewService(dir)
	if err != nil {
		t.Fatalf("history.NewService() error = %v", err)
	}

	userID := "user-1"
	watched := true
	watchedAt := time.Date(2026, 4, 9, 11, 44, 0, 0, time.UTC)

	if _, err := historySvc.UpdateWatchHistory(userID, models.WatchHistoryUpdate{
		MediaType:   "movie",
		ItemID:      "tvdb:movie:370",
		Name:        "Ponyo",
		Watched:     &watched,
		WatchedAt:   watchedAt,
		ExternalIDs: map[string]string{"tvdb": "370", "imdb": "tt0876563"},
	}); err != nil {
		t.Fatalf("UpdateWatchHistory() error = %v", err)
	}

	origURL := trakt.GetBaseURLForTest()
	trakt.SetBaseURLForTest("https://trakt.example")
	defer trakt.SetBaseURLForTest(origURL)

	addToHistoryCalled := false
	traktClient := trakt.NewClient("id", "secret")
	traktClient.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/users/me/history" {
				if req.Method != http.MethodGet {
					t.Fatalf("unexpected method %s", req.Method)
				}
				resp := jsonResponse(http.StatusOK, `[{"id":1,"watched_at":"2026-04-09T11:44:00Z","type":"movie","movie":{"title":"Ponyo","year":2008,"ids":{"trakt":7217,"tmdb":12429,"imdb":"tt0876563"}}}]`)
				resp.Header.Set("X-Pagination-Item-Count", "1")
				return resp, nil
			}
			if req.URL.Path != "/sync/history" {
				t.Fatalf("unexpected path %s", req.URL.Path)
				return nil, nil
			}
			if req.Method == http.MethodPost {
				addToHistoryCalled = true
				return jsonResponse(http.StatusOK, `{"added":{"movies":0,"episodes":0}}`), nil
			}
			t.Fatalf("unexpected method %s", req.Method)
			return nil, nil
		}),
	})

	svc := &Service{
		historyService: historySvc,
		traktClient:    traktClient,
	}

	result, err := svc.syncLocalHistoryToTrakt(config.ScheduledTask{}, &config.TraktAccount{AccessToken: "token"}, userID, false)
	if err != nil {
		t.Fatalf("syncLocalHistoryToTrakt() error = %v", err)
	}

	if result.Count != 0 {
		t.Fatalf("expected no export for cross-provider duplicate movie, got Count=%d", result.Count)
	}
	if addToHistoryCalled {
		t.Fatal("expected AddToHistory to be skipped when Trakt already has the same movie under another provider ID")
	}
}

func TestExecuteLocalMediaScan_AllLibraries(t *testing.T) {
	scanner := &fakeLocalMediaScanner{
		libraries: []models.LocalMediaLibrary{
			{ID: "lib-1", Name: "Movies"},
			{ID: "lib-2", Name: "Shows"},
		},
		summaries: map[string]models.LocalMediaScanSummary{
			"lib-1": {Discovered: 10, Matched: 8, LowConfidence: 1, Unmatched: 1},
			"lib-2": {Discovered: 20, Matched: 18, LowConfidence: 1, Unmatched: 1},
		},
	}
	svc := &Service{localMediaService: scanner}

	result, err := svc.executeLocalMediaScan(config.ScheduledTask{
		Config: map[string]string{
			"libraryId": config.ScheduledTaskLocalMediaAllLibraries,
		},
	})
	if err != nil {
		t.Fatalf("executeLocalMediaScan() error = %v", err)
	}
	if got, want := len(scanner.scanned), 2; got != want {
		t.Fatalf("scanned %d libraries, want %d", got, want)
	}
	if scanner.scanned[0] != "lib-1" || scanner.scanned[1] != "lib-2" {
		t.Fatalf("scan order = %v, want [lib-1 lib-2]", scanner.scanned)
	}
	if result.Count != 30 {
		t.Fatalf("result.Count = %d, want 30", result.Count)
	}
	if !strings.Contains(result.Message, "completed for 2 libraries") {
		t.Fatalf("result.Message = %q, want aggregated all-libraries summary", result.Message)
	}
}

func TestCheckAndRunTasks_PeriodicPlexWatchlistSync(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := tmpDir + "/settings.json"
	manager := config.NewManager(configPath)

	lastRunAt := time.Now().UTC().Add(-10 * time.Minute)
	settings := config.DefaultSettings()
	settings.Plex.Accounts = []config.PlexAccount{
		{
			ID:        "plex-account-1",
			Name:      "Test Plex",
			AuthToken: "plex-token",
		},
	}
	settings.ScheduledTasks.Tasks = []config.ScheduledTask{
		{
			ID:        "plex-task-1",
			Type:      config.ScheduledTaskTypePlexWatchlistSync,
			Name:      "Plex Watchlist",
			Enabled:   true,
			Frequency: config.ScheduledTaskFrequency5Min,
			Config: map[string]string{
				"plexAccountId":  "plex-account-1",
				"profileId":      "profile-1",
				"syncDirection":  "source_to_target",
				"deleteBehavior": "additive",
			},
			LastRunAt:  &lastRunAt,
			LastStatus: config.ScheduledTaskStatusPending,
			CreatedAt:  time.Now().UTC().Add(-time.Hour),
		},
	}
	if err := manager.Save(settings); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	watchlistSvc, err := watchlist.NewService(tmpDir)
	if err != nil {
		t.Fatalf("watchlist.NewService() error = %v", err)
	}

	origTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Host == "discover.provider.plex.tv" && req.URL.Path == "/library/sections/watchlist/all":
			return jsonResponse(http.StatusOK, `{
				"MediaContainer": {
					"size": 1,
					"totalSize": 1,
					"offset": 0,
					"Metadata": [
						{
							"ratingKey": "rk-1",
							"guid": "plex://movie/abc123",
							"type": "movie",
							"title": "The Test Movie",
							"year": 2024,
							"thumb": "/thumb/1",
							"art": "/art/1"
						}
					]
				}
			}`), nil
		case req.URL.Host == "discover.provider.plex.tv" && req.URL.Path == "/library/metadata/rk-1":
			return jsonResponse(http.StatusOK, `{
				"MediaContainer": {
					"Metadata": [
						{
							"guid": "plex://movie/abc123",
							"Guid": [
								{"id": "tmdb://12345"},
								{"id": "imdb://tt1234567"}
							]
						}
					]
				}
			}`), nil
		default:
			return nil, io.EOF
		}
	})
	defer func() {
		http.DefaultTransport = origTransport
	}()

	svc := NewService(manager, plex.NewClient("test-client"), trakt.NewClient("", ""), watchlistSvc)
	svc.checkAndRunTasks()
	svc.wg.Wait()

	updated, err := manager.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	task := updated.ScheduledTasks.Tasks[0]
	if task.LastRunAt == nil {
		t.Fatal("expected LastRunAt to be updated")
	}
	if task.LastStatus != config.ScheduledTaskStatusSuccess {
		t.Fatalf("LastStatus = %q, want %q", task.LastStatus, config.ScheduledTaskStatusSuccess)
	}
	if task.ItemsImported != 1 {
		t.Fatalf("ItemsImported = %d, want 1", task.ItemsImported)
	}

	items, err := watchlistSvc.List("profile-1")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].ID != "tmdb:movie:12345" {
		t.Fatalf("item ID = %q, want %q", items[0].ID, "tmdb:movie:12345")
	}
	if items[0].SyncSource != "plex:plex-account-1:plex-task-1" {
		t.Fatalf("SyncSource = %q, want %q", items[0].SyncSource, "plex:plex-account-1:plex-task-1")
	}
}

func TestSyncBidirectional_ResolvesPlexIDFromExternalIDsForLocalExport(t *testing.T) {
	tmpDir := t.TempDir()
	watchlistSvc, err := watchlist.NewService(tmpDir)
	if err != nil {
		t.Fatalf("watchlist.NewService() error = %v", err)
	}

	if _, err := watchlistSvc.AddOrUpdate("profile-1", models.WatchlistUpsert{
		ID:        "tvdb:movie:285",
		MediaType: "movie",
		Name:      "Aladdin",
		Year:      1992,
		ExternalIDs: map[string]string{
			"imdb": "tt0103639",
			"tmdb": "812",
			"tvdb": "285",
		},
	}); err != nil {
		t.Fatalf("AddOrUpdate() error = %v", err)
	}

	origTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Host == "discover.provider.plex.tv" && req.URL.Path == "/library/sections/watchlist/all":
			return jsonResponse(http.StatusOK, `{
				"MediaContainer": {
					"size": 0,
					"totalSize": 0,
					"offset": 0,
					"Metadata": []
				}
			}`), nil
		case req.URL.Host == "discover.provider.plex.tv" && req.URL.Path == "/library/search":
			return jsonResponse(http.StatusOK, `{
				"MediaContainer": {
					"SearchResults": [
						{
							"id": "external",
							"title": "More Ways To Watch",
							"SearchResult": [
								{
									"score": 0.98,
									"Metadata": {
										"ratingKey": "plex-aladdin-1",
										"guid": "plex://movie/abc123",
										"type": "movie",
										"title": "Aladdin",
										"year": 1992
									}
								}
							]
						}
					]
				}
			}`), nil
		case req.URL.Host == "discover.provider.plex.tv" && req.URL.Path == "/library/metadata/plex-aladdin-1":
			return jsonResponse(http.StatusOK, `{
				"MediaContainer": {
					"Metadata": [
						{
							"guid": "plex://movie/abc123",
							"Guid": [
								{"id": "tmdb://812"},
								{"id": "imdb://tt0103639"},
								{"id": "tvdb://285"}
							]
						}
					]
				}
			}`), nil
		case req.URL.Host == "discover.provider.plex.tv" && req.URL.Path == "/actions/addToWatchlist":
			if got := req.URL.Query().Get("ratingKey"); got != "plex-aladdin-1" {
				t.Fatalf("ratingKey = %q, want %q", got, "plex-aladdin-1")
			}
			return jsonResponse(http.StatusOK, `{}`), nil
		default:
			return nil, io.EOF
		}
	})
	defer func() {
		http.DefaultTransport = origTransport
	}()

	svc := &Service{
		plexClient:       plex.NewClient("test-client"),
		watchlistService: watchlistSvc,
	}

	result, err := svc.syncBidirectional("plex-token", "profile-1", "plex:test:task", "additive", "source_wins", false)
	if err != nil {
		t.Fatalf("syncBidirectional() error = %v", err)
	}
	if result.Count != 1 {
		t.Fatalf("result.Count = %d, want 1", result.Count)
	}
}

func TestSyncMDBListWatchlistToLocal_MirrorModeKeepsCanonicalMergedItem(t *testing.T) {
	tmpDir := t.TempDir()
	watchlistSvc, err := watchlist.NewService(tmpDir)
	if err != nil {
		t.Fatalf("watchlist.NewService() error = %v", err)
	}

	now := time.Now().UTC().Add(-time.Hour)
	if _, err := watchlistSvc.AddOrUpdate("profile-1", models.WatchlistUpsert{
		ID:        "tvdb:movie:344109",
		MediaType: "movie",
		Name:      "Zootopia 2",
		Year:      2025,
		ExternalIDs: map[string]string{
			"imdb": "tt26443597",
			"tmdb": "1084242",
			"tvdb": "344109",
			"plex": "63d15b0b38992be08a0efa6f",
		},
		SyncSource: "mdblist:acc-1:task-1",
		SyncedAt:   &now,
	}); err != nil {
		t.Fatalf("seed AddOrUpdate() error = %v", err)
	}

	origTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "api.mdblist.com" && req.URL.Path == "/watchlist/items" {
			return jsonResponse(http.StatusOK, `{
				"movies": [
					{
						"title": "Zootopia 2",
						"release_year": 2025,
						"ids": {
							"imdb": "tt26443597",
							"tmdb": 1084242
						}
					}
				],
				"shows": []
			}`), nil
		}
		return nil, io.EOF
	})
	defer func() {
		http.DefaultTransport = origTransport
	}()

	svc := &Service{
		watchlistService: watchlistSvc,
	}

	result, err := svc.syncMDBListWatchlistToLocal(&config.MDBListAccount{
		ID:     "acc-1",
		APIKey: "api-key",
	}, "profile-1", "mdblist:acc-1:task-1", "mirror", false)
	if err != nil {
		t.Fatalf("syncMDBListWatchlistToLocal() error = %v", err)
	}
	if result.Count != 1 {
		t.Fatalf("result.Count = %d, want 1", result.Count)
	}

	items, err := watchlistSvc.List("profile-1")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].ID != "tvdb:movie:344109" {
		t.Fatalf("item ID = %q, want %q", items[0].ID, "tvdb:movie:344109")
	}
	if items[0].SyncSource != "mdblist:acc-1:task-1" {
		t.Fatalf("sync source = %q, want %q", items[0].SyncSource, "mdblist:acc-1:task-1")
	}
	if got := items[0].ExternalIDs["plex"]; got != "63d15b0b38992be08a0efa6f" {
		t.Fatalf("expected plex external ID to survive, got %q", got)
	}
}

func TestSyncPlexToLocal_MirrorModeKeepsCanonicalMergedItem(t *testing.T) {
	tmpDir := t.TempDir()
	watchlistSvc, err := watchlist.NewService(tmpDir)
	if err != nil {
		t.Fatalf("watchlist.NewService() error = %v", err)
	}

	now := time.Now().UTC().Add(-time.Hour)
	if _, err := watchlistSvc.AddOrUpdate("profile-1", models.WatchlistUpsert{
		ID:        "tvdb:movie:344109",
		MediaType: "movie",
		Name:      "Zootopia 2",
		Year:      2025,
		ExternalIDs: map[string]string{
			"imdb": "tt26443597",
			"tmdb": "1084242",
			"tvdb": "344109",
		},
		SyncSource: "plex:acc:task",
		SyncedAt:   &now,
	}); err != nil {
		t.Fatalf("seed AddOrUpdate() error = %v", err)
	}

	origTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Host == "discover.provider.plex.tv" && req.URL.Path == "/library/sections/watchlist/all":
			return jsonResponse(http.StatusOK, `{
				"MediaContainer": {
					"size": 1,
					"totalSize": 1,
					"offset": 0,
					"Metadata": [
						{
							"ratingKey": "plex-zootopia-2",
							"type": "movie",
							"title": "Zootopia 2",
							"year": 2025
						}
					]
				}
			}`), nil
		case req.URL.Host == "discover.provider.plex.tv" && req.URL.Path == "/library/metadata/plex-zootopia-2":
			return jsonResponse(http.StatusOK, `{
				"MediaContainer": {
					"Metadata": [
						{
							"Guid": [
								{"id": "tmdb://1084242"},
								{"id": "imdb://tt26443597"}
							]
						}
					]
				}
			}`), nil
		default:
			return nil, io.EOF
		}
	})
	defer func() {
		http.DefaultTransport = origTransport
	}()

	svc := &Service{
		plexClient:       plex.NewClient("test-client"),
		watchlistService: watchlistSvc,
	}

	result, err := svc.syncPlexToLocal("plex-token", "profile-1", "plex:acc:task", "mirror", false)
	if err != nil {
		t.Fatalf("syncPlexToLocal() error = %v", err)
	}
	if result.Count != 1 {
		t.Fatalf("result.Count = %d, want 1", result.Count)
	}

	items, err := watchlistSvc.List("profile-1")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].ID != "tvdb:movie:344109" {
		t.Fatalf("item ID = %q, want %q", items[0].ID, "tvdb:movie:344109")
	}
	if got := items[0].ExternalIDs["plex"]; got != "plex-zootopia-2" {
		t.Fatalf("expected plex external ID to be merged, got %q", got)
	}
}

func TestExecuteJellyfinFavoritesSync_MirrorModeKeepsCanonicalMergedItem(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := tmpDir + "/settings.json"
	manager := config.NewManager(configPath)

	settings := config.DefaultSettings()
	settings.Jellyfin.Accounts = []config.JellyfinAccount{
		{
			ID:        "jf-acc-1",
			Name:      "Test Jellyfin",
			ServerURL: "http://placeholder",
			Token:     "jf-token",
			UserID:    "jf-user",
		},
	}
	if err := manager.Save(settings); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	watchlistSvc, err := watchlist.NewService(tmpDir)
	if err != nil {
		t.Fatalf("watchlist.NewService() error = %v", err)
	}

	now := time.Now().UTC().Add(-time.Hour)
	if _, err := watchlistSvc.AddOrUpdate("profile-1", models.WatchlistUpsert{
		ID:        "tvdb:movie:344109",
		MediaType: "movie",
		Name:      "Zootopia 2",
		Year:      2025,
		ExternalIDs: map[string]string{
			"imdb": "tt26443597",
			"tmdb": "1084242",
			"tvdb": "344109",
		},
		SyncSource: "jellyfin:jf-acc-1:jf-task-1",
		SyncedAt:   &now,
	}); err != nil {
		t.Fatalf("seed AddOrUpdate() error = %v", err)
	}

	origTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "jellyfin.test" && strings.Contains(req.URL.Path, "/Users/jf-user/Items") {
			return jsonResponse(http.StatusOK, `{
				"Items": [
					{
						"Id": "jf-zootopia-2",
						"Name": "Zootopia 2",
						"Type": "Movie",
						"ProductionYear": 2025,
						"ProviderIds": {
							"Tmdb": "1084242",
							"Imdb": "tt26443597"
						}
					}
				]
			}`), nil
		}
		return nil, io.EOF
	})
	defer func() {
		http.DefaultTransport = origTransport
	}()

	settings.Jellyfin.Accounts[0].ServerURL = "http://jellyfin.test"
	if err := manager.Save(settings); err != nil {
		t.Fatalf("Save() updated settings error = %v", err)
	}

	svc := &Service{
		configManager:    manager,
		jellyfinClient:   jellyfin.NewClient(),
		watchlistService: watchlistSvc,
	}

	result, err := svc.executeJellyfinFavoritesSync(config.ScheduledTask{
		ID:   "jf-task-1",
		Type: config.ScheduledTaskTypeJellyfinFavoritesSync,
		Config: map[string]string{
			"jellyfinAccountId": "jf-acc-1",
			"profileId":         "profile-1",
			"syncDirection":     "source_to_target",
			"deleteBehavior":    "mirror",
		},
	})
	if err != nil {
		t.Fatalf("executeJellyfinFavoritesSync() error = %v", err)
	}
	if result.Count != 1 {
		t.Fatalf("result.Count = %d, want 1", result.Count)
	}

	items, err := watchlistSvc.List("profile-1")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].ID != "tvdb:movie:344109" {
		t.Fatalf("item ID = %q, want %q", items[0].ID, "tvdb:movie:344109")
	}
	if got := items[0].ExternalIDs["jellyfin"]; got != "jf-zootopia-2" {
		t.Fatalf("expected jellyfin external ID to be merged, got %q", got)
	}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestPlaybackPercentThresholds(t *testing.T) {
	tests := []struct {
		name         string
		percent      float64
		wantProgress bool
		wantStop     bool
		wantWatched  bool
	}{
		{name: "below minimum is ignored", percent: 1, wantProgress: false, wantStop: false, wantWatched: false},
		{name: "mid progress uses pause", percent: 45, wantProgress: true, wantStop: false, wantWatched: false},
		{name: "high incomplete progress uses stop", percent: 87, wantProgress: true, wantStop: true, wantWatched: false},
		{name: "watched threshold becomes history", percent: 90, wantProgress: false, wantStop: false, wantWatched: true},
		{name: "above watched threshold becomes history", percent: 95, wantProgress: false, wantStop: false, wantWatched: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := playbackPercentCountsAsProgress(tt.percent); got != tt.wantProgress {
				t.Fatalf("playbackPercentCountsAsProgress(%v) = %v, want %v", tt.percent, got, tt.wantProgress)
			}
			if got := playbackPercentNeedsStop(tt.percent); got != tt.wantStop {
				t.Fatalf("playbackPercentNeedsStop(%v) = %v, want %v", tt.percent, got, tt.wantStop)
			}
			if got := playbackPercentCountsAsWatched(tt.percent); got != tt.wantWatched {
				t.Fatalf("playbackPercentCountsAsWatched(%v) = %v, want %v", tt.percent, got, tt.wantWatched)
			}
		})
	}
}

func TestTraktPlaybackItemToHistoryUpdate_Episode(t *testing.T) {
	svc := &Service{}
	pausedAt := time.Date(2026, 3, 28, 11, 30, 0, 0, time.UTC)

	update := svc.traktPlaybackItemToHistoryUpdate(trakt.PlaybackItem{
		Progress: 95,
		PausedAt: pausedAt,
		Type:     "episode",
		Show: &trakt.Show{
			Title: "Example Show",
			IDs: trakt.IDs{
				TVDB: 73762,
				IMDB: "tt0413573",
			},
		},
		Episode: &trakt.Episode{
			Season: 22,
			Number: 10,
			Title:  "Strip That Down",
		},
	})

	if update == nil {
		t.Fatal("expected watch history update")
	}
	if update.MediaType != "episode" {
		t.Fatalf("MediaType = %q, want episode", update.MediaType)
	}
	if update.ItemID != "tvdb:series:73762:s22e10" {
		t.Fatalf("ItemID = %q", update.ItemID)
	}
	if update.SeriesID != "tvdb:series:73762" {
		t.Fatalf("SeriesID = %q", update.SeriesID)
	}
	if update.SeriesName != "Example Show" {
		t.Fatalf("SeriesName = %q", update.SeriesName)
	}
	if update.Name != "Strip That Down" {
		t.Fatalf("Name = %q", update.Name)
	}
	if update.Watched == nil || !*update.Watched {
		t.Fatal("expected watched=true")
	}
	if !update.WatchedAt.Equal(pausedAt) {
		t.Fatalf("WatchedAt = %v, want %v", update.WatchedAt, pausedAt)
	}
}

func TestTraktPlaybackItemToHistoryUpdate_Movie(t *testing.T) {
	svc := &Service{}
	pausedAt := time.Date(2026, 3, 28, 9, 0, 0, 0, time.UTC)

	update := svc.traktPlaybackItemToHistoryUpdate(trakt.PlaybackItem{
		Progress: 92,
		PausedAt: pausedAt,
		Type:     "movie",
		Movie: &trakt.Movie{
			Title: "The Matrix",
			Year:  1999,
			IDs: trakt.IDs{
				TMDB: 603,
				IMDB: "tt0133093",
			},
		},
	})

	if update == nil {
		t.Fatal("expected watch history update")
	}
	if update.MediaType != "movie" {
		t.Fatalf("MediaType = %q, want movie", update.MediaType)
	}
	if update.ItemID != "tmdb:movie:603" {
		t.Fatalf("ItemID = %q", update.ItemID)
	}
	if update.Name != "The Matrix" {
		t.Fatalf("Name = %q", update.Name)
	}
	if update.Year != 1999 {
		t.Fatalf("Year = %d", update.Year)
	}
	if update.Watched == nil || !*update.Watched {
		t.Fatal("expected watched=true")
	}
	if !update.WatchedAt.Equal(pausedAt) {
		t.Fatalf("WatchedAt = %v, want %v", update.WatchedAt, pausedAt)
	}
}
