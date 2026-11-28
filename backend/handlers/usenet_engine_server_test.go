package handlers

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"novastream/config"
	"novastream/services/usenetengine"
)

const testEngineKey = "0123456789abcdef0123456789abcdef-test"

const testNZB = `<?xml version="1.0"?><nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"><file subject="x"/></nzb>`

func newTestEngineServer(t *testing.T, importer EngineImporter) *UsenetEngineServer {
	t.Helper()
	s, err := NewUsenetEngineServer(testEngineKey, importer)
	if err != nil || s == nil {
		t.Fatalf("NewUsenetEngineServer: %v", err)
	}
	return s
}

func addFileRequest(t *testing.T, fileName, body string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("nzbfile", fileName)
	_, _ = part.Write([]byte(body))
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/internal/usenet-engine/api?mode=addfile&output=json", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("X-Api-Key", testEngineKey)
	return req
}

func TestEngineServerDisabledWithoutKeyAndRejectsShortKey(t *testing.T) {
	importer := func(context.Context, string, []byte) (string, error) { return "", nil }
	if s, err := NewUsenetEngineServer("", importer); s != nil || err != nil {
		t.Fatalf("empty key should disable: %v %v", s, err)
	}
	if _, err := NewUsenetEngineServer("short", importer); err == nil {
		t.Fatal("short key should be rejected")
	}
}

func TestEngineServerAuth(t *testing.T) {
	s := newTestEngineServer(t, func(context.Context, string, []byte) (string, error) { return "", nil })
	cases := []struct {
		name   string
		mutate func(*http.Request)
		want   int
	}{
		{"no key", func(r *http.Request) {}, http.StatusUnauthorized},
		{"query key ignored", func(r *http.Request) { r.URL.RawQuery += "&apikey=" + testEngineKey }, http.StatusUnauthorized},
		{"wrong key", func(r *http.Request) { r.Header.Set("X-Api-Key", testEngineKey+"x") }, http.StatusUnauthorized},
		{"proxied", func(r *http.Request) {
			r.Header.Set("X-Api-Key", testEngineKey)
			r.Header.Set("X-Forwarded-For", "1.2.3.4")
		}, http.StatusForbidden},
		{"ok", func(r *http.Request) { r.Header.Set("X-Api-Key", testEngineKey) }, http.StatusOK},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/internal/usenet-engine/api?mode=queue", nil)
		tc.mutate(req)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: got %d want %d", tc.name, rec.Code, tc.want)
		}
	}
}

func TestEngineServerLocksOutAfterRepeatedFailures(t *testing.T) {
	s := newTestEngineServer(t, func(context.Context, string, []byte) (string, error) { return "", nil })
	for i := 0; i < engineServerFailLimit; i++ {
		req := httptest.NewRequest(http.MethodGet, "/x?mode=queue", nil)
		req.Header.Set("X-Api-Key", "wrong")
		s.ServeHTTP(httptest.NewRecorder(), req)
	}
	req := httptest.NewRequest(http.MethodGet, "/x?mode=queue", nil)
	req.Header.Set("X-Api-Key", testEngineKey)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected lockout, got %d", rec.Code)
	}
	s.now = func() time.Time { return time.Now().Add(engineServerLockout + time.Second) }
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected lockout to expire, got %d", rec.Code)
	}
}

func TestEngineServerRejectsInvalidUploads(t *testing.T) {
	s := newTestEngineServer(t, func(context.Context, string, []byte) (string, error) { return "", nil })
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, addFileRequest(t, "a.nzb", "not xml"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-NZB payload: got %d", rec.Code)
	}
	get := addFileRequest(t, "a.nzb", testNZB)
	get.Method = http.MethodGet
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, get)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET addfile: got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x?mode=addurl&name=http://169.254.169.254/", nil)
	req.Header.Set("X-Api-Key", testEngineKey)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("addurl must be unsupported: got %d", rec.Code)
	}
}

func TestSanitizeEngineFileName(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd":       "passwd.nzb",
		`..\..\win.nzb`:          "win.nzb",
		"Show.S01E01.nzb":        "Show.S01E01.nzb",
		"":                       "download.nzb",
		"bad\x00name\n.nzb":      "badname.nzb",
		strings.Repeat("é", 300): strings.Repeat("é", 196) + ".nzb",
	}
	for in, want := range cases {
		if got := sanitizeEngineFileName(in); got != want {
			t.Errorf("sanitize(%q)=%q want %q", in, got, want)
		}
	}
}

// End to end through the real SAB client used by the importing instance.
func TestEngineServerRoundTripWithSABClient(t *testing.T) {
	var mu sync.Mutex
	var gotName string
	release := make(chan struct{})
	s := newTestEngineServer(t, func(ctx context.Context, fileName string, nzb []byte) (string, error) {
		mu.Lock()
		gotName = fileName
		mu.Unlock()
		<-release
		if strings.Contains(fileName, "Broken") {
			return "", errors.New("articles missing")
		}
		return "/1795_" + strings.TrimSuffix(fileName, ".nzb") + "/abc.mkv", nil
	})
	var sawQueryKey bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != "" {
			sawQueryKey = true
		}
		s.ServeHTTP(w, r)
	}))
	defer srv.Close()

	engine, err := usenetengine.NewFromSettings(config.UsenetEngineSettings{Type: "mediastorm", BaseURL: srv.URL, APIKey: testEngineKey}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	submit, err := engine.SubmitNZB(ctx, usenetengine.SubmitRequest{FileName: "My.Show.S10E35.nzb", NZB: []byte(testNZB)})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if sawQueryKey {
		t.Fatal("mediastorm engine type must not send the key in the query string")
	}
	status, err := engine.Status(ctx, submit.JobID)
	if err != nil || (status.Status != usenetengine.StatusQueued && status.Status != usenetengine.StatusProcessing) {
		t.Fatalf("pending status: %+v %v", status, err)
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err = engine.Status(ctx, submit.JobID)
		if err == nil && status.Status == usenetengine.StatusCompleted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status.Status != usenetengine.StatusCompleted || status.OutputPath != "/1795_My.Show.S10E35/abc.mkv" || status.FileName != "My.Show.S10E35.nzb" {
		t.Fatalf("completed status: %+v", status)
	}
	mu.Lock()
	if gotName != "My.Show.S10E35.nzb" {
		t.Fatalf("importer got %q", gotName)
	}
	mu.Unlock()

	failed, err := engine.SubmitNZB(ctx, usenetengine.SubmitRequest{FileName: "Broken.nzb", NZB: []byte(testNZB)})
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, _ = engine.Status(ctx, failed.JobID)
		if status.Status == usenetengine.StatusFailed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status.Status != usenetengine.StatusFailed || !strings.Contains(status.Error, "articles missing") {
		t.Fatalf("failed status: %+v", status)
	}

	if err := engine.Delete(ctx, submit.JobID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/x?mode=history", nil)
	req.Header.Set("X-Api-Key", testEngineKey)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), submit.JobID) {
		t.Fatalf("deleted job still listed: %s", rec.Body.String())
	}
}
