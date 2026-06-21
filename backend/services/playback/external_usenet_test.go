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

func TestResolveExternalUsenetAltMountRewritesSubmittedNZB(t *testing.T) {
	release := "Rick.and.Morty.S09E01.Theres.Something.About.Morty.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb-Scrambled"
	var addFileSeen bool
	engineServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mode") != "addfile" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": true,
				"queue":  map[string]any{"slots": []map[string]any{}},
			})
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		file, header, err := r.FormFile("nzbfile")
		if err != nil {
			t.Fatalf("FormFile(nzbfile): %v", err)
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		wantFileName := release + ".mkv.nzb"
		if header.Filename != wantFileName {
			t.Fatalf("filename = %q, want %q", header.Filename, wantFileName)
		}
		if !strings.Contains(string(data), release+".mkv") {
			t.Fatalf("uploaded NZB was not rewritten with media extension: %s", string(data))
		}
		if strings.Contains(string(data), `"TKpvdrWv5Ho7.mkv"`) {
			t.Fatalf("uploaded NZB still contains obfuscated media subject: %s", string(data))
		}
		addFileSeen = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  true,
			"nzo_ids": []string{"altmount-job-1"},
		})
	}))
	defer engineServer.Close()

	indexerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="`+release+`.nzb"`)
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <head>
    <meta type="name">`+release+`</meta>
    <meta type="title">96345F4P63F13t0P61t63k4J70J00437.mkv</meta>
  </head>
  <file subject="`+release+` [2/7] &quot;`+release+`.vol-01.par2&quot; yEnc (1/1)">
    <segments><segment bytes="6681" number="1">par2-id</segment></segments>
  </file>
  <file subject="`+release+` [1/7] &quot;TKpvdrWv5Ho7.mkv&quot; yEnc (1/269)">
    <segments><segment bytes="3961388" number="1">video-id</segment></segments>
  </file>
</nzb>`)
	}))
	defer indexerServer.Close()

	settings := config.DefaultSettings()
	settings.UsenetEngines = []config.UsenetEngineSettings{{
		Name:          "AltMount",
		Type:          "altmount",
		Enabled:       true,
		BaseURL:       engineServer.URL,
		WebDAVBaseURL: "http://webdav.example/webdav",
	}}
	cfg := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := cfg.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	svc := NewService(cfg, nil, nil, nil)
	res, err := svc.Resolve(context.Background(), models.NZBResult{
		Title:       release,
		DownloadURL: indexerServer.URL + "/" + release + ".nzb",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !addFileSeen {
		t.Fatal("engine addfile was not called")
	}
	if res.SourceNZBPath != release+".mkv.nzb" {
		t.Fatalf("SourceNZBPath = %q, want %q", res.SourceNZBPath, release+".mkv.nzb")
	}
}

func TestExternalQueueStatusRejectsMismatchedCompletedPath(t *testing.T) {
	svc := NewService(config.NewManager(filepath.Join(t.TempDir(), "settings.json")), nil, nil, nil)
	svc.externalJobs[42] = &externalUsenetJob{
		ID:             42,
		EngineJobID:    "altmount-job",
		SubmittedTitle: "The.Owl.House.S01E01.A.Lying.Witch.and.a.Warden.1080p.DSNP.WEB-DL.AAC2.0.H.264-LAZY",
		SourceNZBPath:  "The.Owl.House.S01E01.A.Lying.Witch.and.a.Warden.1080p.DSNP.WEB-DL.AAC2.0.H.264-LAZY.nzb",
		Engine: config.UsenetEngineSettings{
			Name:          "AltMount",
			Type:          "altmount",
			BaseURL:       "http://engine.invalid",
			WebDAVBaseURL: "http://127.0.0.1:3313/webdav",
		},
		FileSize:   123,
		LastStatus: "queued",
	}

	if externalOutputPathMatchesSubmitted(
		"http://127.0.0.1:3313/webdav/Default/complete/Bluey.S02E19.The.Show/Bluey.S02E19.The.Show.mkv",
		svc.externalJobs[42].SubmittedTitle,
		svc.externalJobs[42].SourceNZBPath,
	) {
		t.Fatal("Bluey path matched Owl House submission")
	}
	if !externalOutputPathMatchesSubmitted(
		"http://127.0.0.1:3313/webdav/Default/complete/The.Owl.House.S01E01.A.Lying.Witch.and.a.Warden.1080p.DSNP.WEB-DL.AAC2.0.H.264-LAZY/The.Owl.House.S01E01.mkv",
		svc.externalJobs[42].SubmittedTitle,
		svc.externalJobs[42].SourceNZBPath,
	) {
		t.Fatal("Owl House path did not match Owl House submission")
	}
	if !externalOutputPathMatchesSubmitted(
		"http://127.0.0.1:3313/webdav/Default/complete/The.Owl.House.S01E01.A.Lying.Witch.and.a.Warden.1080p.DSNP.WEB-DL.AAC2.0.H.264-LAZY.mkv",
		svc.externalJobs[42].SubmittedTitle,
		svc.externalJobs[42].SourceNZBPath,
	) {
		t.Fatal("exact Owl House file path did not match Owl House submission")
	}
}

func TestExternalQueueStatusFallsBackToAltMountWebDAVOnMismatchedCompletedPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sabnzbd/api" && r.URL.Query().Get("mode") == "queue":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": true,
				"queue":  map[string]any{"slots": []map[string]any{}},
			})
		case r.URL.Path == "/sabnzbd/api" && r.URL.Query().Get("mode") == "history":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": true,
				"history": map[string]any{
					"slots": []map[string]any{{
						"nzo_id":  "altmount-job",
						"status":  "Completed",
						"storage": "/mnt/remotes/altmount/Default/complete/Bluey.S02E19.The.Show/Bluey.S02E19.The.Show.mkv",
					}},
				},
			})
		case r.Method == "HEAD" && r.URL.Path == "/webdav/Default/complete/The.Owl.House.S01E01.A.Lying.Witch.and.a.Warden.1080p.DSNP.WEB-DL.AAC2.0.H.264-LAZY.mkv":
			w.WriteHeader(http.StatusOK)
		case r.Method == "HEAD":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == "PROPFIND":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Fatalf("unexpected request method=%q path=%q mode=%q", r.Method, r.URL.Path, r.URL.Query().Get("mode"))
		}
	}))
	defer server.Close()

	svc := NewService(config.NewManager(filepath.Join(t.TempDir(), "settings.json")), nil, nil, nil)
	svc.externalJobs[42] = &externalUsenetJob{
		ID:             42,
		EngineJobID:    "altmount-job",
		SubmittedTitle: "The.Owl.House.S01E01.A.Lying.Witch.and.a.Warden.1080p.DSNP.WEB-DL.AAC2.0.H.264-LAZY",
		SourceNZBPath:  "The.Owl.House.S01E01.A.Lying.Witch.and.a.Warden.1080p.DSNP.WEB-DL.AAC2.0.H.264-LAZY_2.nzb",
		Engine: config.UsenetEngineSettings{
			Name:          "AltMount",
			Type:          "altmount",
			BaseURL:       server.URL,
			APIPath:       "/sabnzbd/api",
			WebDAVBaseURL: server.URL + "/webdav",
			Config: map[string]string{
				"webdavPathPrefix": "/mnt/remotes/altmount",
			},
		},
	}

	res, handled, err := svc.externalQueueStatus(context.Background(), 42)
	if err != nil {
		t.Fatalf("externalQueueStatus: %v", err)
	}
	if !handled {
		t.Fatal("handled = false")
	}
	want := server.URL + "/webdav/Default/complete/The.Owl.House.S01E01.A.Lying.Witch.and.a.Warden.1080p.DSNP.WEB-DL.AAC2.0.H.264-LAZY.mkv"
	if res.WebDAVPath != want {
		t.Fatalf("WebDAVPath = %q, want %q", res.WebDAVPath, want)
	}
	if res.HealthStatus != "healthy" {
		t.Fatalf("HealthStatus = %q, want healthy", res.HealthStatus)
	}
}

func TestExternalQueueStatusAcceptsAltMountDuplicateSuffixedCompletedPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sabnzbd/api" && r.URL.Query().Get("mode") == "queue":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": true,
				"queue":  map[string]any{"slots": []map[string]any{}},
			})
		case r.URL.Path == "/sabnzbd/api" && r.URL.Query().Get("mode") == "history":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": true,
				"history": map[string]any{
					"slots": []map[string]any{{
						"nzo_id":   "altmount-job",
						"status":   "Completed",
						"nzb_name": "Sophies.Choice.1982.RERiP.MULTi.1080p.BluRay.x264-ULSHD.mkv_2.nzb",
						"storage":  "mediastorm/Sophies.Choice.1982.RERiP.MULTi.1080p.BluRay.x264-ULSHD.mkv_2",
					}},
				},
			})
		case r.Method == "PROPFIND" && r.URL.Path == "/webdav/mediastorm/Sophies.Choice.1982.RERiP.MULTi.1080p.BluRay.x264-ULSHD.mkv_2/":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/webdav/mediastorm/Sophies.Choice.1982.RERiP.MULTi.1080p.BluRay.x264-ULSHD.mkv_2/</D:href>
    <D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat>
  </D:response>
  <D:response>
    <D:href>/webdav/mediastorm/Sophies.Choice.1982.RERiP.MULTi.1080p.BluRay.x264-ULSHD.mkv_2/Sophies.Choice.1982.RERiP.MULTi.1080p.BluRay.x264-ULSHD.mkv</D:href>
    <D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:displayname>Sophies.Choice.1982.RERiP.MULTi.1080p.BluRay.x264-ULSHD.mkv</D:displayname><D:getcontentlength>123456789</D:getcontentlength></D:prop></D:propstat>
  </D:response>
</D:multistatus>`)
		default:
			t.Fatalf("unexpected request method=%q path=%q mode=%q", r.Method, r.URL.Path, r.URL.Query().Get("mode"))
		}
	}))
	defer server.Close()

	svc := NewService(config.NewManager(filepath.Join(t.TempDir(), "settings.json")), nil, nil, nil)
	svc.externalJobs[42] = &externalUsenetJob{
		ID:             42,
		EngineJobID:    "altmount-job",
		SubmittedTitle: "Sophies.Choice.1982.RERiP.MULTi.1080p.BluRay.x264-ULSHD",
		SourceNZBPath:  "Sophies.Choice.1982.RERiP.MULTi.1080p.BluRay.x264-ULSHD.mkv.nzb",
		Engine: config.UsenetEngineSettings{
			Name:          "AltMount",
			Type:          "altmount",
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
	want := server.URL + "/webdav/mediastorm/Sophies.Choice.1982.RERiP.MULTi.1080p.BluRay.x264-ULSHD.mkv_2/Sophies.Choice.1982.RERiP.MULTi.1080p.BluRay.x264-ULSHD.mkv"
	if res.WebDAVPath != want {
		t.Fatalf("WebDAVPath = %q, want %q", res.WebDAVPath, want)
	}
	if res.HealthStatus != "healthy" {
		t.Fatalf("HealthStatus = %q, want healthy", res.HealthStatus)
	}
}

func TestExternalQueueStatusFallsBackToAltMountCategoryRoot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sabnzbd/api" && r.URL.Query().Get("mode") == "queue":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": true,
				"queue":  map[string]any{"slots": []map[string]any{}},
			})
		case r.URL.Path == "/sabnzbd/api" && r.URL.Query().Get("mode") == "history":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": true,
				"history": map[string]any{
					"slots": []map[string]any{{
						"nzo_id":   "altmount-job",
						"status":   "Completed",
						"nzb_name": "Over.Your.Dead.Body.2026.NORDiC.2160p.WEB-DL.H.264.DV.HDR.DDP5.1-ADDICTION.mkv.nzb",
						"storage":  "mediastorm",
					}},
				},
			})
		case r.Method == "PROPFIND" && r.URL.Path == "/webdav/mediastorm/Over.Your.Dead.Body.2026.NORDiC.2160p.WEB-DL.H.264.DV.HDR.DDP5.1-ADDICTION.mkv/":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/webdav/mediastorm/Over.Your.Dead.Body.2026.NORDiC.2160p.WEB-DL.H.264.DV.HDR.DDP5.1-ADDICTION.mkv/</D:href>
    <D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat>
  </D:response>
  <D:response>
    <D:href>/webdav/mediastorm/Over.Your.Dead.Body.2026.NORDiC.2160p.WEB-DL.H.264.DV.HDR.DDP5.1-ADDICTION.mkv/Over.Your.Dead.Body.2026.NORDiC.2160p.WEB-DL.H.264.DV.HDR.DDP5.1-ADDICTION.mkv</D:href>
    <D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:displayname>Over.Your.Dead.Body.2026.NORDiC.2160p.WEB-DL.H.264.DV.HDR.DDP5.1-ADDICTION.mkv</D:displayname><D:getcontentlength>123456789</D:getcontentlength></D:prop></D:propstat>
  </D:response>
</D:multistatus>`)
		case r.Method == "HEAD":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == "PROPFIND":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Fatalf("unexpected request method=%q path=%q mode=%q", r.Method, r.URL.Path, r.URL.Query().Get("mode"))
		}
	}))
	defer server.Close()

	svc := NewService(config.NewManager(filepath.Join(t.TempDir(), "settings.json")), nil, nil, nil)
	svc.externalJobs[42] = &externalUsenetJob{
		ID:             42,
		EngineJobID:    "altmount-job",
		SubmittedTitle: "Over.Your.Dead.Body.2026.NORDiC.2160p.WEB-DL.H.264.DV.HDR.DDP5.1-ADDICTION",
		SourceNZBPath:  "Over.Your.Dead.Body.2026.NORDiC.2160p.WEB-DL.H.264.DV.HDR.DDP5.1-ADDICTION.mkv.nzb",
		Engine: config.UsenetEngineSettings{
			Name:          "AltMount",
			Type:          "altmount",
			BaseURL:       server.URL,
			APIPath:       "/sabnzbd/api",
			WebDAVBaseURL: server.URL + "/webdav",
			Category:      "mediastorm",
		},
	}

	res, handled, err := svc.externalQueueStatus(context.Background(), 42)
	if err != nil {
		t.Fatalf("externalQueueStatus: %v", err)
	}
	if !handled {
		t.Fatal("handled = false")
	}
	want := server.URL + "/webdav/mediastorm/Over.Your.Dead.Body.2026.NORDiC.2160p.WEB-DL.H.264.DV.HDR.DDP5.1-ADDICTION.mkv/Over.Your.Dead.Body.2026.NORDiC.2160p.WEB-DL.H.264.DV.HDR.DDP5.1-ADDICTION.mkv"
	if res.WebDAVPath != want {
		t.Fatalf("WebDAVPath = %q, want %q", res.WebDAVPath, want)
	}
	if res.HealthStatus != "healthy" {
		t.Fatalf("HealthStatus = %q, want healthy", res.HealthStatus)
	}
}

func TestExternalQueueStatusFallsBackToGenericWebDAVRoot(t *testing.T) {
	for _, engineType := range []string{"nzbdav", "nzbdavex"} {
		t.Run(engineType, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api" && r.URL.Query().Get("mode") == "queue":
					_ = json.NewEncoder(w).Encode(map[string]any{
						"status": true,
						"queue":  map[string]any{"slots": []map[string]any{}},
					})
				case r.URL.Path == "/api" && r.URL.Query().Get("mode") == "history":
					_ = json.NewEncoder(w).Encode(map[string]any{
						"status": true,
						"history": map[string]any{
							"slots": []map[string]any{{
								"nzo_id":  "generic-job",
								"status":  "Completed",
								"storage": "/unrelated/Other.Release",
							}},
						},
					})
				case r.Method == "PROPFIND" && r.URL.Path == "/webdav/Release.Name/":
					w.Header().Set("Content-Type", "application/xml")
					w.WriteHeader(http.StatusMultiStatus)
					_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/webdav/Release.Name/</D:href>
    <D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat>
  </D:response>
  <D:response>
    <D:href>/webdav/Release.Name/Release.Name.mkv</D:href>
    <D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:displayname>Release.Name.mkv</D:displayname><D:getcontentlength>123456789</D:getcontentlength></D:prop></D:propstat>
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
				EngineJobID:   "generic-job",
				SourceNZBPath: "Release.Name.nzb",
				Engine: config.UsenetEngineSettings{
					Name:          engineType,
					Type:          engineType,
					BaseURL:       server.URL,
					APIPath:       "/api",
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
			want := server.URL + "/webdav/Release.Name/Release.Name.mkv"
			if res.WebDAVPath != want {
				t.Fatalf("WebDAVPath = %q, want %q", res.WebDAVPath, want)
			}
			if res.HealthStatus != "healthy" {
				t.Fatalf("HealthStatus = %q, want healthy", res.HealthStatus)
			}
		})
	}
}

func TestExternalQueueStatusRejectsStaleAltMountStatusFileName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sabnzbd/api" && r.URL.Query().Get("mode") == "queue":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": true,
				"queue":  map[string]any{"slots": []map[string]any{}},
			})
		case r.URL.Path == "/sabnzbd/api" && r.URL.Query().Get("mode") == "history":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": true,
				"history": map[string]any{
					"slots": []map[string]any{{
						"nzo_id":   "altmount-job",
						"status":   "Completed",
						"nzb_name": "The.Owl.House.S01E01.A.Lying.Witch.and.a.Warden.1080p.DSNP.WEB-DL.AAC2.0.H.264-LAZY.nzb",
						"storage":  "/mnt/remotes/altmount/Default/complete/The.Owl.House.S01E01.A.Lying.Witch.and.a.Warden.1080p.DSNP.WEB-DL.AAC2.0.H.264-LAZY.mkv",
					}},
				},
			})
		case r.Method == "HEAD" || r.Method == "PROPFIND":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Fatalf("unexpected request method=%q path=%q mode=%q", r.Method, r.URL.Path, r.URL.Query().Get("mode"))
		}
	}))
	defer server.Close()

	svc := NewService(config.NewManager(filepath.Join(t.TempDir(), "settings.json")), nil, nil, nil)
	svc.externalJobs[42] = &externalUsenetJob{
		ID:             42,
		EngineJobID:    "altmount-job",
		SubmittedTitle: "The.Wonderfully.Weird.World.of.Gumball.S01E01.The.Burger.1080p.DSNP.WEB-DL.AAC2.0.x264-AndreMor",
		SourceNZBPath:  "The.Wonderfully.Weird.World.of.Gumball.S01E01.The.Burger.1080p.DSNP.WEB-DL.AAC2.0.x264-AndreMor.nzb",
		Engine: config.UsenetEngineSettings{
			Name:          "AltMount",
			Type:          "altmount",
			BaseURL:       server.URL,
			APIPath:       "/sabnzbd/api",
			WebDAVBaseURL: server.URL + "/webdav",
			Config: map[string]string{
				"webdavPathPrefix": "/mnt/remotes/altmount",
			},
		},
	}

	res, handled, err := svc.externalQueueStatus(context.Background(), 42)
	if err != nil {
		t.Fatalf("externalQueueStatus: %v", err)
	}
	if !handled {
		t.Fatal("handled = false")
	}
	if res.WebDAVPath != "" {
		t.Fatalf("WebDAVPath = %q, want empty while stale status is rejected", res.WebDAVPath)
	}
	if res.SourceNZBPath != "The.Wonderfully.Weird.World.of.Gumball.S01E01.The.Burger.1080p.DSNP.WEB-DL.AAC2.0.x264-AndreMor.nzb" {
		t.Fatalf("SourceNZBPath = %q, want original submitted NZB", res.SourceNZBPath)
	}
	if res.HealthStatus != "processing" {
		t.Fatalf("HealthStatus = %q, want processing", res.HealthStatus)
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
		if r.URL.Path == "/webdav/release/" || r.URL.Path == "/webdav/Movie/" {
			w.WriteHeader(http.StatusNotFound)
			return
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
		case r.Method == "PROPFIND" && (r.URL.Path == "/webdav/release/" || r.URL.Path == "/webdav/Movie/"):
			w.WriteHeader(http.StatusNotFound)
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
		case r.Method == "PROPFIND" && (r.URL.Path == "/webdav/Release.Name" || r.URL.Path == "/webdav/Release.Name/"):
			http.NotFound(w, r)
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

func TestResolveExternalUsenetReusesExistingWebDAVResolutionBeforeSubmit(t *testing.T) {
	for _, tt := range []struct {
		engineType string
		apiPath    string
		hitPath    string
	}{
		{engineType: "altmount", apiPath: "/sabnzbd/api", hitPath: "/webdav/Release.Name/"},
		{engineType: "nzbdav", apiPath: "/api", hitPath: "/webdav/Release.Name/"},
		{engineType: "nzbdavex", apiPath: "/api", hitPath: "/webdav/Release.Name/"},
		{engineType: "decypharr", apiPath: "/sabnzbd/api", hitPath: "/webdav/nzbs/Release.Name/"},
	} {
		t.Run(tt.engineType, func(t *testing.T) {
			var addFileSeen bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == tt.apiPath && r.URL.Query().Get("mode") == "addfile":
					addFileSeen = true
					t.Fatalf("addfile should not be called when existing %s WebDAV media is available", tt.engineType)
				case r.Method == "HEAD":
					w.WriteHeader(http.StatusNotFound)
				case r.Method == "PROPFIND" && r.URL.Path == tt.hitPath:
					w.Header().Set("Content-Type", "application/xml")
					w.WriteHeader(http.StatusMultiStatus)
					_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>`+strings.TrimRight(tt.hitPath, "/")+`/</D:href>
    <D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat>
  </D:response>
  <D:response>
    <D:href>`+strings.TrimRight(tt.hitPath, "/")+`/Release.Name.mkv</D:href>
    <D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:displayname>Release.Name.mkv</D:displayname><D:getcontentlength>123456789</D:getcontentlength></D:prop></D:propstat>
  </D:response>
</D:multistatus>`)
				case r.Method == "PROPFIND":
					w.WriteHeader(http.StatusNotFound)
				default:
					t.Fatalf("unexpected request method=%q path=%q mode=%q", r.Method, r.URL.Path, r.URL.Query().Get("mode"))
				}
			}))
			defer server.Close()

			indexerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Disposition", `attachment; filename="Release.Name.nzb"`)
				_, _ = io.WriteString(w, `<?xml version="1.0"?><nzb><file subject="Release.Name.mkv"><segments><segment bytes="123456">abc</segment></segments></file></nzb>`)
			}))
			defer indexerServer.Close()

			settings := config.DefaultSettings()
			settings.UsenetEngines = []config.UsenetEngineSettings{{
				Name:          tt.engineType,
				Type:          tt.engineType,
				Enabled:       true,
				BaseURL:       server.URL,
				APIPath:       tt.apiPath,
				WebDAVBaseURL: server.URL + "/webdav",
			}}
			cfg := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
			if err := cfg.Save(settings); err != nil {
				t.Fatalf("save settings: %v", err)
			}

			svc := NewService(cfg, nil, nil, nil)
			res, err := svc.Resolve(context.Background(), models.NZBResult{
				Title:       "Release.Name",
				DownloadURL: indexerServer.URL + "/Release.Name.nzb",
			})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if addFileSeen {
				t.Fatal("engine addfile was called")
			}
			if res.QueueID != 0 {
				t.Fatalf("QueueID = %d, want 0 for reused direct WebDAV result", res.QueueID)
			}
			want := server.URL + strings.TrimRight(tt.hitPath, "/") + "/Release.Name.mkv"
			if res.WebDAVPath != want {
				t.Fatalf("WebDAVPath = %q, want %q", res.WebDAVPath, want)
			}
			if res.HealthStatus != "healthy" {
				t.Fatalf("HealthStatus = %q, want healthy", res.HealthStatus)
			}
		})
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
