package usenetengine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSABClientSubmitNZBUsesAddFileMultipart(t *testing.T) {
	var sawAddFile bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api" {
			t.Fatalf("path = %q, want /api", r.URL.Path)
		}
		if got := r.URL.Query().Get("mode"); got != "addfile" {
			t.Fatalf("mode = %q, want addfile", got)
		}
		if got := r.URL.Query().Get("apikey"); got != "secret" {
			t.Fatalf("apikey query = %q, want secret", got)
		}
		if got := r.Header.Get("X-Api-Key"); got != "secret" {
			t.Fatalf("X-Api-Key = %q, want secret", got)
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
		if string(data) != "<nzb/>" {
			t.Fatalf("uploaded data = %q", string(data))
		}
		if header.Filename != "movie.nzb" {
			t.Fatalf("filename = %q, want movie.nzb", header.Filename)
		}
		if got := r.FormValue("cat"); got != "movies" {
			t.Fatalf("cat = %q, want movies", got)
		}
		sawAddFile = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  true,
			"nzo_ids": []string{"job-1"},
		})
	}))
	defer server.Close()

	client, err := NewSABClient(SABConfig{Name: "test", BaseURL: server.URL, APIKey: "secret"}, server.Client())
	if err != nil {
		t.Fatalf("NewSABClient: %v", err)
	}
	res, err := client.SubmitNZB(context.Background(), SubmitRequest{
		FileName: "movie.nzb",
		NZB:      []byte("<nzb/>"),
		Category: "movies",
	})
	if err != nil {
		t.Fatalf("SubmitNZB: %v", err)
	}
	if !sawAddFile {
		t.Fatal("server did not receive addfile request")
	}
	if res.JobID != "job-1" {
		t.Fatalf("JobID = %q, want job-1", res.JobID)
	}
}

func TestSABClientSubmitNZBCanSendCategoryInQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("cat"); got != "mediastorm" {
			t.Fatalf("query cat = %q, want mediastorm", got)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		var multipartCat string
		if r.MultipartForm != nil && len(r.MultipartForm.Value["cat"]) > 0 {
			multipartCat = r.MultipartForm.Value["cat"][0]
		}
		if got := multipartCat; got != "" {
			t.Fatalf("multipart cat = %q, want empty", got)
		}
		if _, _, err := r.FormFile("name"); err != nil {
			t.Fatalf("FormFile(name): %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  true,
			"nzo_ids": []string{"decypharr-job"},
		})
	}))
	defer server.Close()

	client, err := NewSABClient(SABConfig{
		BaseURL:         server.URL,
		FileFieldName:   "name",
		CategoryInQuery: true,
	}, server.Client())
	if err != nil {
		t.Fatalf("NewSABClient: %v", err)
	}
	res, err := client.SubmitNZB(context.Background(), SubmitRequest{
		FileName: "movie.nzb",
		NZB:      []byte("<nzb/>"),
		Category: "mediastorm",
	})
	if err != nil {
		t.Fatalf("SubmitNZB: %v", err)
	}
	if res.JobID != "decypharr-job" {
		t.Fatalf("JobID = %q, want decypharr-job", res.JobID)
	}
}

func TestSABClientCanSendAPIKeyAsBearer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("apikey"); got != "decypharr-token" {
			t.Fatalf("apikey query = %q, want decypharr-token", got)
		}
		if got := r.Header.Get("X-Api-Key"); got != "decypharr-token" {
			t.Fatalf("X-Api-Key = %q, want decypharr-token", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer decypharr-token" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": true,
			"queue":  map[string]any{"slots": []map[string]any{}},
		})
	}))
	defer server.Close()

	client, err := NewSABClient(SABConfig{
		BaseURL:        server.URL,
		APIKey:         "decypharr-token",
		APIKeyAsBearer: true,
	}, server.Client())
	if err != nil {
		t.Fatalf("NewSABClient: %v", err)
	}
	if _, err := client.Status(context.Background(), "job-1"); err != nil {
		t.Fatalf("Status: %v", err)
	}
}

func TestSABClientCanSendAuthQueryParams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("ma_username"); got != "decypharr-user" {
			t.Fatalf("ma_username query = %q, want decypharr-user", got)
		}
		if got := r.URL.Query().Get("ma_password"); got != "decypharr-pass" {
			t.Fatalf("ma_password query = %q, want decypharr-pass", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": true,
			"queue":  map[string]any{"slots": []map[string]any{}},
		})
	}))
	defer server.Close()

	client, err := NewSABClient(SABConfig{
		BaseURL:       server.URL,
		Username:      "decypharr-user",
		Password:      "decypharr-pass",
		UsernameParam: "ma_username",
		PasswordParam: "ma_password",
	}, server.Client())
	if err != nil {
		t.Fatalf("NewSABClient: %v", err)
	}
	if _, err := client.Status(context.Background(), "job-1"); err != nil {
		t.Fatalf("Status: %v", err)
	}
}

func TestSABClientStatusNormalizesQueueSlot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("mode"); got != "queue" {
			t.Fatalf("mode = %q, want queue", got)
		}
		if got := r.URL.Query().Get("nzo_ids"); got != "job-2" {
			t.Fatalf("nzo_ids = %q, want job-2", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": true,
			"queue": map[string]any{
				"slots": []map[string]any{{
					"nzo_id":          "job-2",
					"status":          "Downloading",
					"filename":        "Show.S01E01.nzb",
					"cat":             "tv",
					"true_percentage": "42.5",
					"mb":              "100.00",
				}},
			},
		})
	}))
	defer server.Close()

	client, err := NewSABClient(SABConfig{BaseURL: server.URL}, server.Client())
	if err != nil {
		t.Fatalf("NewSABClient: %v", err)
	}
	status, err := client.Status(context.Background(), "job-2")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Status != StatusProcessing {
		t.Fatalf("Status = %q, want %q", status.Status, StatusProcessing)
	}
	if status.Progress != 42.5 {
		t.Fatalf("Progress = %v, want 42.5", status.Progress)
	}
	if status.SizeBytes != 100*1024*1024 {
		t.Fatalf("SizeBytes = %d, want %d", status.SizeBytes, 100*1024*1024)
	}
}

func TestSABClientUsesConfiguredAPIPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/sabnzbd/api") {
			t.Fatalf("path = %q, want /sabnzbd/api", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  true,
			"nzo_ids": []string{"decypharr-job"},
		})
	}))
	defer server.Close()

	client, err := NewSABClient(SABConfig{BaseURL: server.URL, APIPath: "/sabnzbd/api"}, server.Client())
	if err != nil {
		t.Fatalf("NewSABClient: %v", err)
	}
	res, err := client.SubmitNZB(context.Background(), SubmitRequest{FileName: "x.nzb", NZB: []byte("x")})
	if err != nil {
		t.Fatalf("SubmitNZB: %v", err)
	}
	if res.JobID != "decypharr-job" {
		t.Fatalf("JobID = %q, want decypharr-job", res.JobID)
	}
}

func TestSABClientUsesConfiguredFileFieldName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		if _, _, err := r.FormFile("nzbfile"); err == nil {
			t.Fatal("unexpected nzbfile upload field")
		}
		file, header, err := r.FormFile("name")
		if err != nil {
			t.Fatalf("FormFile(name): %v", err)
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		if string(data) != "x" {
			t.Fatalf("uploaded data = %q", string(data))
		}
		if header.Filename != "x.nzb" {
			t.Fatalf("filename = %q, want x.nzb", header.Filename)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  true,
			"nzo_ids": []string{"decypharr-job"},
		})
	}))
	defer server.Close()

	client, err := NewSABClient(SABConfig{BaseURL: server.URL, FileFieldName: "name"}, server.Client())
	if err != nil {
		t.Fatalf("NewSABClient: %v", err)
	}
	res, err := client.SubmitNZB(context.Background(), SubmitRequest{FileName: "x.nzb", NZB: []byte("x")})
	if err != nil {
		t.Fatalf("SubmitNZB: %v", err)
	}
	if res.JobID != "decypharr-job" {
		t.Fatalf("JobID = %q, want decypharr-job", res.JobID)
	}
}

func TestSABClientStatusFallsBackToHistoryStorage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("mode") {
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
						"nzo_id":   "job-3",
						"status":   "Completed",
						"nzb_name": "Movie.nzb",
						"bytes":    1234,
						"storage":  "/mnt/remote/nzbdav/completed-symlinks/movies/Movie",
					}},
				},
			})
		default:
			t.Fatalf("unexpected mode %q", r.URL.Query().Get("mode"))
		}
	}))
	defer server.Close()

	client, err := NewSABClient(SABConfig{BaseURL: server.URL}, server.Client())
	if err != nil {
		t.Fatalf("NewSABClient: %v", err)
	}
	status, err := client.Status(context.Background(), "job-3")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Status != StatusCompleted {
		t.Fatalf("Status = %q, want %q", status.Status, StatusCompleted)
	}
	if status.OutputPath != "/mnt/remote/nzbdav/completed-symlinks/movies/Movie" {
		t.Fatalf("OutputPath = %q", status.OutputPath)
	}
	if status.SizeBytes != 1234 {
		t.Fatalf("SizeBytes = %d, want 1234", status.SizeBytes)
	}
}
