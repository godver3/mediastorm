package handlers

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"novastream/config"
	"novastream/models"
	"novastream/services/badstreams"
)

func TestZeroRunDetectorFiresOnceForPaddingSpanningChunks(t *testing.T) {
	d := &zeroRunDetector{}
	half := make([]byte, zeroFillRunThreshold/2)
	if d.observe(append([]byte{1, 2, 3}, half...)) {
		t.Fatal("fired before the run reached the threshold")
	}
	if !d.observe(append(half, 9)) {
		t.Fatal("did not fire when the run crossing two chunks reached the threshold")
	}
	if d.observe(make([]byte, zeroFillRunThreshold)) {
		t.Fatal("fired a second time for the same response")
	}
}

func TestZeroRunDetectorIgnoresShortRunsInRealData(t *testing.T) {
	d := &zeroRunDetector{}
	chunk := bytes.Repeat(append(make([]byte, 64<<10), 0x47), 16) // 64 KiB zero runs, broken up
	for i := 0; i < 8; i++ {
		if d.observe(chunk) {
			t.Fatal("fired on zero runs shorter than the threshold")
		}
	}
	var nilDetector *zeroRunDetector
	if nilDetector.observe(make([]byte, zeroFillRunThreshold)) {
		t.Fatal("nil detector (non-engine URL) must never fire")
	}
}

func TestExternalUsenetReleaseFromURL(t *testing.T) {
	cases := []struct{ url, category, want string }{
		{"http://mac-mini:3322/webdav/mediastorm/Show.S01E03.1080p-GRP/Show.S01E03.1080p-GRP/Show.S01E03.1080p-GRP.mkv", "mediastorm", "Show.S01E03.1080p-GRP"},
		{"http://mac-mini:3322/webdav/mediastorm/Movie.2024.2160p-GRP.mkv/Movie.2024.2160p-GRP.mkv", "mediastorm", "Movie.2024.2160p-GRP"},
		{"http://host:3322/webdav/mediastorm/Loose.File.720p-GRP.mkv", "mediastorm", "Loose.File.720p-GRP"},
		{"http://host:3321/Root.File-GRP.mp4", "", "Root.File-GRP"},
	}
	for _, c := range cases {
		if got := externalUsenetReleaseFromURL(c.url, c.category); got != c.want {
			t.Errorf("externalUsenetReleaseFromURL(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

func newEngineConfig(t *testing.T, engines ...config.UsenetEngineSettings) *config.Manager {
	t.Helper()
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	settings := config.DefaultSettings()
	settings.UsenetEngines = engines
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	return mgr
}

func TestZeroRunDetectorOnlyForExternalUsenetEngineURLs(t *testing.T) {
	mgr := newEngineConfig(t, config.UsenetEngineSettings{
		Name: "AltMount", Type: "altmount", Enabled: true, WebDAVBaseURL: "http://mac-mini:3322/webdav/",
	})
	h := &VideoHandler{configManager: mgr}
	engineURL, _ := url.Parse("http://mac-mini:3322/webdav/mediastorm/R/R.mkv")
	if h.newExternalUsenetZeroRunDetector(engineURL) == nil {
		t.Fatal("expected a detector for an engine WebDAV URL")
	}
	cdnURL, _ := url.Parse("https://cdn.example.com/dl/R.mkv")
	if h.newExternalUsenetZeroRunDetector(cdnURL) != nil {
		t.Fatal("debrid/CDN URLs must not be scanned")
	}
}

func TestReportZeroFilledExternalStreamMarksReleaseBad(t *testing.T) {
	service := badstreams.New(filepath.Join(t.TempDir(), "bad_streams.json"))
	SetStreamReleaseBadStreams(service)
	t.Cleanup(func() { SetStreamReleaseBadStreams(nil) })

	mgr := newEngineConfig(t, config.UsenetEngineSettings{
		Name: "AltMount", Type: "altmount", Enabled: true, WebDAVBaseURL: "http://mac-mini:3322/webdav/", Category: "mediastorm",
	})
	h := &VideoHandler{configManager: mgr, failures: &streamFailureRegistry{records: map[string]streamFailureRecord{}}}

	remembered := "http://mac-mini:3322/webdav/mediastorm/Known.S01E01-GRP/Known.S01E01-GRP.mkv"
	rememberStreamRelease(remembered, models.NZBResult{Title: "Known.S01E01-GRP", ServiceType: models.ServiceTypeUsenet})
	h.reportZeroFilledExternalStream(remembered, 123)
	if !service.IsBad(models.NZBResult{Title: "Known.S01E01-GRP", ServiceType: models.ServiceTypeUsenet}) {
		t.Fatal("remembered release was not marked bad")
	}

	// A path whose resolve is no longer remembered (backend restart) falls
	// back to the engine folder name.
	forgotten := "http://mac-mini:3322/webdav/mediastorm/Lost.S01E02-GRP/Lost.S01E02-GRP/Lost.S01E02-GRP.mkv"
	h.reportZeroFilledExternalStream(forgotten, 456)
	if !service.IsBad(models.NZBResult{Title: "Lost.S01E02-GRP", ServiceType: models.ServiceTypeUsenet}) {
		t.Fatal("release from engine folder name was not marked bad")
	}
	if service.IsBad(models.NZBResult{Title: "Other.Release-GRP", ServiceType: models.ServiceTypeUsenet}) {
		t.Fatal("unrelated release was marked bad")
	}
}

func TestValidateExternalURLAuthenticatesExternalUsenetEngines(t *testing.T) {
	originalTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = originalTransport }()

	for _, engineType := range []string{"altmount", "nzbdav", "nzbdavex", "decypharr"} {
		t.Run(engineType, func(t *testing.T) {
			mgr := newEngineConfig(t, config.UsenetEngineSettings{
				Name: engineType, Type: engineType, Enabled: true,
				WebDAVBaseURL: "https://example.com/webdav/", WebDAVUsername: "dav-user", WebDAVPassword: "dav-pass",
			})
			h := &PrequeueHandler{configManager: mgr}
			var sawAuth bool
			http.DefaultTransport = prequeueRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				user, pass, ok := r.BasicAuth()
				sawAuth = ok && user == "dav-user" && pass == "dav-pass"
				status := http.StatusUnauthorized
				if sawAuth {
					status = http.StatusOK
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header), Request: r}, nil
			})
			if err := h.ValidateExternalURL(context.Background(), "https://example.com/webdav/mediastorm/R/R.mkv"); err != nil {
				t.Fatalf("engine stream reported stale: %v", err)
			}
			if !sawAuth {
				t.Fatal("validator did not send the engine's WebDAV credentials")
			}
		})
	}

	t.Run("non-engine URLs stay unauthenticated", func(t *testing.T) {
		mgr := newEngineConfig(t, config.UsenetEngineSettings{
			Name: "AltMount", Type: "altmount", Enabled: true,
			WebDAVBaseURL: "https://example.com/webdav/", WebDAVUsername: "dav-user", WebDAVPassword: "dav-pass",
		})
		h := &PrequeueHandler{configManager: mgr}
		http.DefaultTransport = prequeueRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			if _, _, ok := r.BasicAuth(); ok {
				t.Fatal("credentials leaked to a non-engine URL")
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header), Request: r}, nil
		})
		if err := h.ValidateExternalURL(context.Background(), "https://example.com/other/stream.mkv"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
