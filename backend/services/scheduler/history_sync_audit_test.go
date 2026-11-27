package scheduler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/history"
	"novastream/services/scrob"
	"novastream/services/simkl"
	"novastream/services/trakt"
)

func auditHistoryService(t *testing.T, count int, watched bool) *history.Service {
	t.Helper()
	svc, err := history.NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	updates := make([]models.WatchHistoryUpdate, 0, count)
	for i := 1; i <= count; i++ {
		updates = append(updates, models.WatchHistoryUpdate{
			MediaType: "movie", ItemID: "tmdb:movie:" + strconv.Itoa(i), Name: fmt.Sprintf("Movie %d", i),
			Watched: &watched, WatchedAt: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
			ExternalIDs: map[string]string{"tmdb": strconv.Itoa(i)},
		})
	}
	if _, err := svc.ImportWatchHistory("profile", updates); err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestSimklFullImportRecoversOldCursorAndImportsAllBuckets(t *testing.T) {
	for _, preview := range []bool{false, true} {
		t.Run(strconv.FormatBool(preview), func(t *testing.T) {
			client := simkl.NewClient()
			var paths []string
			client.SetHTTPClientForTest(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				paths = append(paths, r.URL.Path)
				if r.URL.Query().Has("date_from") {
					t.Fatal("old broken cursor used for baseline")
				}
				switch r.URL.Path {
				case "/sync/activities":
					return jsonResponse(200, `{"all":"2026-01-01T00:00:00Z"}`), nil
				case "/sync/all-items/movies":
					return jsonResponse(200, `{"movies":[{"status":"completed","movie":{"title":"Film","ids":{"tmdb":1}},"last_watched_at":"2000-01-01T00:00:00Z"}]}`), nil
				case "/sync/all-items/shows":
					return jsonResponse(200, `{"shows":[{"status":"completed","show":{"title":"Show","ids":{"tmdb":2}},"seasons":[{"number":0,"episodes":[{"number":1,"watched_at":"2000-01-01T00:00:00Z"}]},{"number":1,"episodes":[{"number":1,"watched_at":"2000-01-01T00:00:00Z"}]}]}]}`), nil
				case "/sync/all-items/anime":
					return jsonResponse(200, `{"anime":[{"anime_type":"movie","status":"completed","show":{"title":"Anime Film","ids":{"tmdb":3}},"last_watched_at":"2000-01-01T00:00:00Z","seasons":[{"number":1,"episodes":[{"number":1}]}]}]}`), nil
				default:
					t.Fatalf("unsupported endpoint %s", r.URL.Path)
					return nil, nil
				}
			})})
			h := auditHistoryService(t, 0, true)
			svc := &Service{simklClient: client, historyService: h}
			task := config.ScheduledTask{Config: map[string]string{"lastSimklActivityAt": "2026-01-01T00:00:00Z"}}
			result, err := svc.syncSimklHistoryToLocal(task, &config.SimklAccount{ClientID: "client", AccessToken: "token"}, "profile", preview)
			if err != nil || result.Count != 4 || len(paths) != 4 {
				t.Fatalf("result=%+v paths=%v err=%v", result, paths, err)
			}
			items, err := h.ListWatchHistory("profile")
			if err != nil || (preview && len(items) != 0) || (!preview && len(items) != 4) {
				t.Fatalf("preview=%v items=%v err=%v", preview, items, err)
			}
			if preview && result.Config != nil {
				t.Fatal("preview advanced cursor")
			}
			if !preview && result.Config["simklHistoryImportVersion"] != "2" {
				t.Fatal("baseline recovery not recorded")
			}
		})
	}
}

func TestHistorySyncRetriesFailedAndPreviewRunsWithoutCursor(t *testing.T) {
	now := time.Now()
	for _, state := range []string{"success", "error", "preview", "full"} {
		t.Run(state, func(t *testing.T) {
			task := config.ScheduledTask{ID: "task", LastRunAt: &now, Config: map[string]string{}}
			switch state {
			case "error":
				task.LastStatus = config.ScheduledTaskStatusError
			case "preview":
				task.HasDryRunDetails = true
			case "full":
				task.Config["fullSync"] = "true"
			}
			old := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = old })
			calls := 0
			http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				cursor := r.URL.Query().Get("since") + r.URL.Query().Get("start_at")
				if (cursor != "") != (state == "success") {
					t.Fatalf("state=%s cursor=%q", state, cursor)
				}
				if strings.Contains(r.URL.Host, "mdblist") {
					return jsonResponse(200, `{"movies":[],"episodes":[]}`), nil
				}
				return jsonResponse(200, `[]`), nil
			})
			svc := &Service{traktClient: trakt.NewClient("client", "secret"), lastFullSyncTimes: map[string]time.Time{"task": now}}
			if _, err := svc.syncTraktHistoryToLocal(task, &config.TraktAccount{}, "profile", true); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.syncMDBListHistoryToLocal(task, &config.MDBListAccount{APIKey: "test"}, "profile", true); err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestFullExportBatchFailuresDoNotRecordCompletion(t *testing.T) {
	for _, provider := range []string{"simkl", "mdblist"} {
		for _, watched := range []bool{false, true} {
			t.Run(provider+"/watched="+strconv.FormatBool(watched), func(t *testing.T) {
				now := time.Now()
				key := "task:" + provider + "_export"
				batchSize := 100
				if provider == "simkl" {
					batchSize = 50
				}
				svc := &Service{historyService: auditHistoryService(t, batchSize+1, watched), lastFullSyncTimes: map[string]time.Time{key: now}}
				calls := 0
				transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.Method != http.MethodPost {
						t.Fatalf("unexpected method %s", r.Method)
					}
					calls++
					if calls == 2 {
						return jsonResponse(500, `{"error":"failed"}`), nil
					}
					return jsonResponse(200, `{}`), nil
				})
				old := http.DefaultTransport
				t.Cleanup(func() { http.DefaultTransport = old })
				http.DefaultTransport = transport
				task := config.ScheduledTask{ID: "task", LastRunAt: &now, Config: map[string]string{"fullSync": "true", "fullExport": "true"}}
				var result SyncResult
				var err error
				if provider == "simkl" {
					svc.simklClient = simkl.NewClient()
					svc.simklClient.SetHTTPClientForTest(&http.Client{Transport: transport})
					result, err = svc.syncLocalHistoryToSimkl(task, &config.SimklAccount{ClientID: "client", AccessToken: "token"}, "profile", false)
				} else {
					result, err = svc.syncLocalHistoryToMDBList(task, &config.MDBListAccount{APIKey: "test"}, "profile", false)
				}
				if err == nil || result.Count != batchSize || calls != 2 || !svc.lastFullSyncTimes[key].Equal(now) {
					t.Fatalf("count=%d calls=%d lastFull=%v err=%v", result.Count, calls, svc.lastFullSyncTimes[key], err)
				}
			})
		}
	}
}

func TestScrobFullExportRequiresCompletedRemoteWatch(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(strconv.FormatBool(completed), func(t *testing.T) {
			now := time.Now()
			client := scrob.NewClientWithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet {
					t.Fatal("preview wrote to Scrob")
				}
				return jsonResponse(200, fmt.Sprintf(`{"total_pages":1,"results":[{"completed":%v,"media":{"id":1,"type":"movie","tmdb_id":1}}]}`, completed)), nil
			})})
			svc := &Service{scrobClient: client, historyService: auditHistoryService(t, 1, true), lastFullSyncTimes: map[string]time.Time{"task:scrob_export": now}}
			task := config.ScheduledTask{ID: "task", LastRunAt: &now, Config: map[string]string{"fullSync": "true"}}
			result, err := svc.syncLocalHistoryToScrob(task, &config.ScrobAccount{BaseURL: "https://scrob.example", APIKey: "test"}, "profile", true)
			want := 1
			if completed {
				want = 0
			}
			if err != nil || result.Count != want || !svc.lastFullSyncTimes["task:scrob_export"].Equal(now) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestHistoryStatusDoesNotPersistFailedOrPreviewCursor(t *testing.T) {
	for _, outcome := range []string{"success", "error", "preview"} {
		t.Run(outcome, func(t *testing.T) {
			manager := config.NewManager(t.TempDir() + "/settings.json")
			settings := config.DefaultSettings()
			settings.ScheduledTasks.Tasks = []config.ScheduledTask{{ID: "task", Config: map[string]string{"lastSimklActivityAt": "old"}}}
			if err := manager.Save(settings); err != nil {
				t.Fatal(err)
			}
			result := SyncResult{DryRun: outcome == "preview", Config: map[string]string{"lastSimklActivityAt": "new"}}
			var runErr error
			if outcome == "error" {
				runErr = errors.New("failed export")
			}
			(&Service{configManager: manager}).updateTaskStatus("task", runErr, result)
			saved, err := manager.Load()
			if err != nil {
				t.Fatal(err)
			}
			cursor := saved.ScheduledTasks.Tasks[0].Config["lastSimklActivityAt"]
			if (cursor == "new") != (outcome == "success") {
				t.Fatalf("outcome=%s cursor=%s", outcome, cursor)
			}
		})
	}
}

func TestMDBListFullImportDoesNotRestoreNullWatchRows(t *testing.T) {
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	http.DefaultTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"movies":[{"movie":{"ids":{"tmdb":1}},"last_watched_at":null}],"episodes":[{"episode":{"season":1,"number":1,"show":{"ids":{"tmdb":2}}},"last_watched_at":null}]}`), nil
	})
	result, err := (&Service{}).syncMDBListHistoryToLocal(config.ScheduledTask{}, &config.MDBListAccount{APIKey: "test"}, "profile", true)
	if err != nil || result.Count != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestSimklImportFailureDoesNotWritePartialHistoryOrCursor(t *testing.T) {
	client := simkl.NewClient()
	client.SetHTTPClientForTest(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/sync/activities":
			return jsonResponse(200, `{"all":"2026-01-01T00:00:00Z"}`), nil
		case "/sync/all-items/movies":
			return jsonResponse(200, `{"movies":[{"movie":{"ids":{"tmdb":1}},"status":"completed"}]}`), nil
		case "/sync/all-items/shows":
			return jsonResponse(200, `null`), nil
		default:
			t.Fatal("continued after invalid history response")
			return nil, nil
		}
	})})
	h := auditHistoryService(t, 0, true)
	result, err := (&Service{simklClient: client, historyService: h}).syncSimklHistoryToLocal(config.ScheduledTask{}, &config.SimklAccount{ClientID: "client", AccessToken: "token"}, "profile", false)
	items, _ := h.ListWatchHistory("profile")
	if err == nil || len(items) != 0 || result.Config != nil {
		t.Fatalf("result=%+v items=%v err=%v", result, items, err)
	}
}

func TestMDBListImportLaterPageFailurePreservesCompletedPages(t *testing.T) {
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("offset") == "0" {
			return jsonResponse(200, `{"movies":[{"movie":{"ids":{"tmdb":1}},"last_watched_at":"2000-01-01T00:00:00Z"}],"pagination":{"has_more":true}}`), nil
		}
		return jsonResponse(429, `{"error":"Daily API limit exceeded!"}`), nil
	})
	h := auditHistoryService(t, 0, true)
	result, err := (&Service{historyService: h}).syncMDBListHistoryToLocal(config.ScheduledTask{}, &config.MDBListAccount{APIKey: "test"}, "profile", false)
	items, _ := h.ListWatchHistory("profile")
	if err == nil || len(items) != 1 || result.Count != 1 {
		t.Fatalf("items=%v err=%v", items, err)
	}
}

func TestTraktAutomaticFullImportIncludesOldHistory(t *testing.T) {
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Has("start_at") {
			t.Fatal("automatic full sync excludes old history")
		}
		return jsonResponse(200, `[{"type":"movie","watched_at":"2000-01-01T00:00:00Z","movie":{"title":"Old Film","ids":{"tmdb":1}}}]`), nil
	})
	svc := &Service{traktClient: trakt.NewClient("client", "secret"), lastFullSyncTimes: map[string]time.Time{}}
	result, err := svc.syncTraktHistoryToLocal(config.ScheduledTask{ID: "task"}, &config.TraktAccount{}, "profile", true)
	if err != nil || result.Count != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestTraktFullExportDeduplicatesOldWatchesAndIncludesSpecials(t *testing.T) {
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	h := auditHistoryService(t, 1, true)
	watched := true
	_, err := h.ImportWatchHistory("profile", []models.WatchHistoryUpdate{{
		MediaType: "episode", ItemID: "tmdb:tv:2:s00e01", SeriesID: "tmdb:tv:2", SeasonNumber: 0, EpisodeNumber: 1,
		Watched: &watched, WatchedAt: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), ExternalIDs: map[string]string{"tmdb": "2"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	posts := 0
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			if r.URL.Query().Has("start_at") {
				t.Fatal("full export dedup only fetched recent history")
			}
			return jsonResponse(200, `[{"type":"movie","watched_at":"2000-01-01T00:00:00Z","movie":{"ids":{"tmdb":1}}}]`), nil
		}
		posts++
		var payload trakt.SyncHistoryRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Movies) != 0 || len(payload.Shows) != 1 || payload.Shows[0].Seasons[0].Number != 0 {
			t.Fatalf("unexpected full export %+v", payload)
		}
		return jsonResponse(201, `{"added":{"episodes":1}}`), nil
	})
	now := time.Now()
	svc := &Service{historyService: h, traktClient: trakt.NewClient("client", "secret")}
	result, err := svc.syncLocalHistoryToTrakt(config.ScheduledTask{LastRunAt: &now, Config: map[string]string{"fullSync": "true"}}, &config.TraktAccount{}, "profile", false)
	if err != nil || result.Count != 1 || posts != 1 {
		t.Fatalf("result=%+v posts=%d err=%v", result, posts, err)
	}
}

func TestBidirectionalHistoryPreviewPreservesRemovalsAndCursor(t *testing.T) {
	result := combineHistorySyncResults(
		SyncResult{Count: 1, DryRun: true, ToAdd: []config.DryRunItem{{ID: "incoming"}}, Config: map[string]string{"lastSimklActivityAt": "saved"}},
		SyncResult{Count: 2, DryRun: true, ToAdd: []config.DryRunItem{{ID: "outgoing"}}, ToRemove: []config.DryRunItem{{ID: "unwatched"}}},
	)
	if result.Count != 3 || len(result.ToAdd) != 2 || len(result.ToRemove) != 1 || result.Config["lastSimklActivityAt"] != "saved" {
		t.Fatalf("preview=%+v", result)
	}
}

func TestFullHistoryExportsDoNotCountUnmatchedMovies(t *testing.T) {
	for _, provider := range []string{"simkl", "mdblist", "trakt"} {
		t.Run(provider, func(t *testing.T) {
			old := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = old })
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodGet {
					return jsonResponse(200, `[]`), nil
				}
				return jsonResponse(201, `{"added":{"movies":0},"not_found":{"movies":[{"ids":{"tmdb":1}}]}}`), nil
			})
			http.DefaultTransport = transport
			svc := &Service{historyService: auditHistoryService(t, 1, true), lastFullSyncTimes: map[string]time.Time{}}
			var result SyncResult
			var err error
			switch provider {
			case "simkl":
				svc.simklClient = simkl.NewClient()
				svc.simklClient.SetHTTPClientForTest(&http.Client{Transport: transport})
				result, err = svc.syncLocalHistoryToSimkl(config.ScheduledTask{}, &config.SimklAccount{ClientID: "client", AccessToken: "token"}, "profile", false)
			case "mdblist":
				result, err = svc.syncLocalHistoryToMDBList(config.ScheduledTask{}, &config.MDBListAccount{APIKey: "test"}, "profile", false)
			case "trakt":
				svc.traktClient = trakt.NewClient("client", "secret")
				result, err = svc.syncLocalHistoryToTrakt(config.ScheduledTask{}, &config.TraktAccount{}, "profile", false)
			}
			if err != nil || result.Count != 0 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestHistoryImportsRejectNullResponses(t *testing.T) {
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) { return jsonResponse(200, `null`), nil })
	http.DefaultTransport = transport
	svc := &Service{traktClient: trakt.NewClient("client", "secret"), lastFullSyncTimes: map[string]time.Time{},
		scrobClient: scrob.NewClientWithHTTPClient(&http.Client{Transport: transport})}
	if _, err := svc.syncMDBListHistoryToLocal(config.ScheduledTask{}, &config.MDBListAccount{APIKey: "test"}, "profile", true); err == nil {
		t.Fatal("MDBList accepted null history")
	}
	if _, err := svc.syncTraktHistoryToLocal(config.ScheduledTask{}, &config.TraktAccount{}, "profile", true); err == nil {
		t.Fatal("Trakt accepted null history")
	}
	if _, err := svc.syncScrobHistoryToLocal(&config.ScrobAccount{BaseURL: "https://scrob.example", APIKey: "test"}, "profile", true); err == nil {
		t.Fatal("Scrob accepted null history")
	}
}
