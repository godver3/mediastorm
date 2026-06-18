package playback

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"novastream/config"
	"novastream/models"
)

type failingUsenetHealthService struct {
	called bool
}

func (f *failingUsenetHealthService) CheckHealthWithNZB(ctx context.Context, candidate models.NZBResult, nzbBytes []byte, fileName string) (*models.NZBHealthCheck, error) {
	f.called = true
	return nil, errors.New("direct NNTP health should be skipped")
}

func TestParallelHealthCheckSkipsDirectHealthForExternalEngine(t *testing.T) {
	indexerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="release.nzb"`)
		_, _ = io.WriteString(w, `<?xml version="1.0"?><nzb><file subject="Movie.mkv"><segments><segment bytes="123456">abc</segment></segments></file></nzb>`)
	}))
	defer indexerServer.Close()

	settings := config.DefaultSettings()
	settings.UsenetEngines = []config.UsenetEngineSettings{{
		Name:          "NZBDav",
		Type:          "nzbdav",
		Enabled:       true,
		BaseURL:       "http://engine.example",
		WebDAVBaseURL: "http://webdav.example/webdav",
	}}
	cfg := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := cfg.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	health := &failingUsenetHealthService{}
	svc := NewService(cfg, health, nil, nil)
	results := svc.ParallelHealthCheck(context.Background(), []models.NZBResult{{
		Title:       "Movie",
		ServiceType: models.ServiceTypeUsenet,
		DownloadURL: indexerServer.URL + "/release.nzb",
	}}, 1)

	if len(results) != 1 {
		t.Fatalf("results len = %d, want 1", len(results))
	}
	if !results[0].Healthy {
		t.Fatalf("Healthy = false, error=%v", results[0].Error)
	}
	if health.called {
		t.Fatal("direct NNTP health check was called")
	}
	if len(results[0].NZBBytes) == 0 {
		t.Fatal("NZBBytes was not fetched")
	}
}

func TestResolveExternalUsenetEngineQueuesAndPollsCompletedWebDAVURL(t *testing.T) {
	var addFileSeen bool
	engineServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api" {
			t.Fatalf("engine path = %q, want /api", r.URL.Path)
		}
		switch r.URL.Query().Get("mode") {
		case "addfile":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("ParseMultipartForm: %v", err)
			}
			file, header, err := r.FormFile("nzbfile")
			if err != nil {
				t.Fatalf("FormFile(nzbfile): %v", err)
			}
			defer file.Close()
			data, _ := io.ReadAll(file)
			if !strings.Contains(string(data), "<nzb") {
				t.Fatalf("uploaded NZB = %q", string(data))
			}
			if header.Filename != "release.nzb" {
				t.Fatalf("filename = %q, want release.nzb", header.Filename)
			}
			addFileSeen = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":  true,
				"nzo_ids": []string{"external-job-1"},
			})
		case "queue":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": true,
				"queue":  map[string]any{"slots": []map[string]any{}},
			})
		case "history":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": true,
				"history": map[string]any{
					"slots": []map[string]any{{
						"nzo_id":   "external-job-1",
						"status":   "Completed",
						"nzb_name": "release.nzb",
						"bytes":    123456,
						"storage":  "/mnt/remote/nzbdav/completed-symlinks/movies/Movie/Movie.mkv",
					}},
				},
			})
		default:
			t.Fatalf("unexpected engine mode %q", r.URL.Query().Get("mode"))
		}
	}))
	defer engineServer.Close()

	indexerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="release.nzb"`)
		_, _ = io.WriteString(w, `<?xml version="1.0"?><nzb><file subject="Movie.mkv"><segments><segment bytes="123456">abc</segment></segments></file></nzb>`)
	}))
	defer indexerServer.Close()

	settings := config.DefaultSettings()
	settings.UsenetEngines = []config.UsenetEngineSettings{{
		Name:          "NZBDav",
		Type:          "nzbdav",
		Enabled:       true,
		BaseURL:       engineServer.URL,
		WebDAVBaseURL: "http://webdav.example/webdav",
		Config: map[string]string{
			"webdavPathPrefix": "/mnt/remote/nzbdav",
		},
	}}

	cfg := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := cfg.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	svc := NewService(cfg, nil, nil, nil)

	res, err := svc.Resolve(context.Background(), models.NZBResult{
		Title:       "Movie",
		DownloadURL: indexerServer.URL + "/release.nzb",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !addFileSeen {
		t.Fatal("engine addfile was not called")
	}
	if res.QueueID <= 0 {
		t.Fatalf("QueueID = %d, want positive", res.QueueID)
	}
	if res.WebDAVPath != "" {
		t.Fatalf("initial WebDAVPath = %q, want empty queued response", res.WebDAVPath)
	}

	ready, err := svc.QueueStatus(context.Background(), res.QueueID)
	if err != nil {
		t.Fatalf("QueueStatus: %v", err)
	}
	wantURL := "http://webdav.example/webdav/completed-symlinks/movies/Movie/Movie.mkv"
	if ready.WebDAVPath != wantURL {
		t.Fatalf("WebDAVPath = %q, want %q", ready.WebDAVPath, wantURL)
	}
	if ready.HealthStatus != "healthy" {
		t.Fatalf("HealthStatus = %q, want healthy", ready.HealthStatus)
	}
	if ready.FileSize != 123456 {
		t.Fatalf("FileSize = %d, want 123456", ready.FileSize)
	}
}

func TestResolveExternalUsenetEngineSelectsMediaFromCompletedWebDAVDirectory(t *testing.T) {
	var addFileSeen bool
	var webDAVAuthSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" {
			switch r.URL.Query().Get("mode") {
			case "addfile":
				addFileSeen = true
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status":  true,
					"nzo_ids": []string{"external-job-dir"},
				})
			case "queue":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status": true,
					"queue":  map[string]any{"slots": []map[string]any{}},
				})
			case "history":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status": true,
					"history": map[string]any{
						"slots": []map[string]any{{
							"nzo_id":  "external-job-dir",
							"status":  "Completed",
							"storage": "/mnt/remote/nzbdav/completed-symlinks/movies/Movie",
						}},
					},
				})
			default:
				t.Fatalf("unexpected mode %q", r.URL.Query().Get("mode"))
			}
			return
		}

		if r.Method != "PROPFIND" {
			t.Fatalf("method = %q, want PROPFIND for %s", r.Method, r.URL.Path)
		}
		username, password, ok := r.BasicAuth()
		if !ok || username != "webdav-user" || password != "webdav-pass" {
			t.Fatalf("missing WebDAV basic auth")
		}
		webDAVAuthSeen = true
		if r.URL.Path != "/webdav/completed-symlinks/movies/Movie/" {
			t.Fatalf("PROPFIND path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/webdav/completed-symlinks/movies/Movie/</D:href>
    <D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat>
  </D:response>
  <D:response>
    <D:href>/webdav/completed-symlinks/movies/Movie/Sample.mkv</D:href>
    <D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:displayname>Sample.mkv</D:displayname><D:getcontentlength>999999999</D:getcontentlength></D:prop></D:propstat>
  </D:response>
  <D:response>
    <D:href>/webdav/completed-symlinks/movies/Movie/Movie.mkv</D:href>
    <D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:displayname>Movie.mkv</D:displayname><D:getcontentlength>123456789</D:getcontentlength></D:prop></D:propstat>
  </D:response>
</D:multistatus>`)
	}))
	defer server.Close()

	indexerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="release.nzb"`)
		_, _ = io.WriteString(w, `<?xml version="1.0"?><nzb><file subject="Movie.mkv"><segments><segment bytes="123456">abc</segment></segments></file></nzb>`)
	}))
	defer indexerServer.Close()

	settings := config.DefaultSettings()
	settings.UsenetEngines = []config.UsenetEngineSettings{{
		Name:           "NZBDav",
		Type:           "nzbdav",
		Enabled:        true,
		BaseURL:        server.URL,
		WebDAVBaseURL:  server.URL + "/webdav",
		WebDAVUsername: "webdav-user",
		WebDAVPassword: "webdav-pass",
		Config: map[string]string{
			"webdavPathPrefix": "/mnt/remote/nzbdav",
		},
	}}

	cfg := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := cfg.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	svc := NewService(cfg, nil, nil, nil)

	res, err := svc.Resolve(context.Background(), models.NZBResult{
		Title:       "Movie",
		DownloadURL: indexerServer.URL + "/release.nzb",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !addFileSeen {
		t.Fatal("engine addfile was not called")
	}

	ready, err := svc.QueueStatus(context.Background(), res.QueueID)
	if err != nil {
		t.Fatalf("QueueStatus: %v", err)
	}
	wantURL := server.URL + "/webdav/completed-symlinks/movies/Movie/Movie.mkv"
	if ready.WebDAVPath != wantURL {
		t.Fatalf("WebDAVPath = %q, want %q", ready.WebDAVPath, wantURL)
	}
	if !webDAVAuthSeen {
		t.Fatal("WebDAV auth was not used")
	}
}

func TestResolveExternalUsenetEngineSelectsNZBDavExRcloneLink(t *testing.T) {
	var addFileSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" {
			switch r.URL.Query().Get("mode") {
			case "addfile":
				addFileSeen = true
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status":  true,
					"nzo_ids": []string{"external-job-rclonelink"},
				})
			case "queue":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status": true,
					"queue":  map[string]any{"slots": []map[string]any{}},
				})
			case "history":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status": true,
					"history": map[string]any{
						"slots": []map[string]any{{
							"nzo_id":  "external-job-rclonelink",
							"status":  "Completed",
							"storage": "/mnt/davex/completed-symlinks/movies/Movie",
						}},
					},
				})
			default:
				t.Fatalf("unexpected mode %q", r.URL.Query().Get("mode"))
			}
			return
		}

		switch {
		case r.Method == "PROPFIND" && r.URL.Path == "/webdav/completed-symlinks/movies/Movie/":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/webdav/completed-symlinks/movies/Movie/</D:href>
    <D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat>
  </D:response>
  <D:response>
    <D:href>/webdav/completed-symlinks/movies/Movie/Movie.mkv.rclonelink</D:href>
    <D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:displayname>Movie.mkv.rclonelink</D:displayname><D:getcontentlength>62</D:getcontentlength></D:prop></D:propstat>
  </D:response>
</D:multistatus>`)
		case r.Method == http.MethodGet && r.URL.Path == "/webdav/completed-symlinks/movies/Movie/Movie.mkv.rclonelink":
			_, _ = io.WriteString(w, "/mnt/davex/.ids/f/2/f/d/2/f2fd252f-36a4-4508-903e-4c1c4a1e0e52")
		default:
			t.Fatalf("unexpected WebDAV request method=%q path=%q", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	indexerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="release.nzb"`)
		_, _ = io.WriteString(w, `<?xml version="1.0"?><nzb><file subject="Movie.mkv"><segments><segment bytes="123456">abc</segment></segments></file></nzb>`)
	}))
	defer indexerServer.Close()

	settings := config.DefaultSettings()
	settings.UsenetEngines = []config.UsenetEngineSettings{{
		Name:          "NZBDavEx",
		Type:          "nzbdavex",
		Enabled:       true,
		BaseURL:       server.URL,
		WebDAVBaseURL: server.URL + "/webdav",
	}}

	cfg := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := cfg.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	svc := NewService(cfg, nil, nil, nil)

	res, err := svc.Resolve(context.Background(), models.NZBResult{
		Title:       "Movie",
		DownloadURL: indexerServer.URL + "/release.nzb",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !addFileSeen {
		t.Fatal("engine addfile was not called")
	}

	ready, err := svc.QueueStatus(context.Background(), res.QueueID)
	if err != nil {
		t.Fatalf("QueueStatus: %v", err)
	}
	wantURL := server.URL + "/webdav/.ids/f/2/f/d/2/f2fd252f-36a4-4508-903e-4c1c4a1e0e52"
	if ready.WebDAVPath != wantURL {
		t.Fatalf("WebDAVPath = %q, want %q", ready.WebDAVPath, wantURL)
	}
}

func TestExternalQueueStatusFallsBackToDecypharrWebDAVNZBFolder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sabnzbd/api" && r.URL.Query().Get("mode") == "queue":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": true,
				"queue":  map[string]any{"slots": []map[string]any{}},
			})
		case r.URL.Path == "/sabnzbd/api" && r.URL.Query().Get("mode") == "history":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":  true,
				"history": map[string]any{"slots": []map[string]any{}},
			})
		case r.Method == "PROPFIND" && (r.URL.Path == "/webdav/nzbs/Release.Name" || r.URL.Path == "/webdav/nzbs/Release.Name/"):
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/webdav/nzbs/Release.Name</D:href>
    <D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype><D:displayname>Release.Name</D:displayname></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>
  </D:response>
  <D:response>
    <D:href>/webdav/nzbs/Release.Name/Release.Name.mkv</D:href>
    <D:propstat><D:prop><D:resourcetype/><D:getcontentlength>123456</D:getcontentlength><D:displayname>Release.Name.mkv</D:displayname></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>
  </D:response>
</D:multistatus>`)
		default:
			t.Fatalf("unexpected request method=%q path=%q mode=%q", r.Method, r.URL.Path, r.URL.Query().Get("mode"))
		}
	}))
	defer server.Close()

	svc := NewService(config.NewManager(filepath.Join(t.TempDir(), "settings.json")), nil, nil, nil)
	svc.externalJobs[42] = &externalUsenetJob{
		ID:            42,
		EngineJobID:   "decypharr-job",
		SourceNZBPath: "Release.Name.nzb",
		Engine: config.UsenetEngineSettings{
			Name:          "Decypharr",
			Type:          "decypharr",
			BaseURL:       server.URL,
			APIPath:       "/sabnzbd/api",
			WebDAVBaseURL: server.URL + "/webdav",
		},
	}

	res, handled, err := svc.externalQueueStatus(context.Background(), 42)
	if err != nil {
		t.Fatalf("externalQueueStatus: %v", err)
	}
	if !handled {
		t.Fatal("handled = false")
	}
	want := server.URL + "/webdav/nzbs/Release.Name/Release.Name.mkv"
	if res.WebDAVPath != want {
		t.Fatalf("WebDAVPath = %q, want %q", res.WebDAVPath, want)
	}
	if res.HealthStatus != "healthy" {
		t.Fatalf("HealthStatus = %q, want healthy", res.HealthStatus)
	}
}

func TestExternalWebDAVRelativePathStripsWebDAVPrefix(t *testing.T) {
	got := externalWebDAVRelativePath(config.UsenetEngineSettings{}, "/webdav/__all__/Movie/Movie.mkv")
	if got != "__all__/Movie/Movie.mkv" {
		t.Fatalf("relative path = %q, want __all__/Movie/Movie.mkv", got)
	}
}

func TestExternalWebDAVURLRewritesInternalEngineURL(t *testing.T) {
	got, err := externalWebDAVURL(config.UsenetEngineSettings{
		WebDAVBaseURL:  "http://127.0.0.1:3310",
		WebDAVUsername: "user",
		WebDAVPassword: "pass",
	}, "http://localhost:8080/completed-symlinks/movies/Movie/Movie.mkv")
	if err != nil {
		t.Fatalf("externalWebDAVURL: %v", err)
	}
	want := "http://127.0.0.1:3310/completed-symlinks/movies/Movie/Movie.mkv"
	if got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
}

func TestResolveWebDAVHrefRewritesInternalEngineURL(t *testing.T) {
	baseURL, err := url.Parse("http://127.0.0.1:3310/completed-symlinks/movies/Movie/")
	if err != nil {
		t.Fatalf("parse base URL: %v", err)
	}

	got := resolveWebDAVHref(baseURL, "http://localhost:8080/completed-symlinks/movies/Movie/Movie.mkv")
	want := "http://127.0.0.1:3310/completed-symlinks/movies/Movie/Movie.mkv"
	if got != want {
		t.Fatalf("href = %q, want %q", got, want)
	}
}
