package handlers

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	"novastream/config"
)

func TestBrandingImageUploadPersistsSettingAndServesImage(t *testing.T) {
	tmp := t.TempDir()
	settingsPath := filepath.Join(tmp, "settings.json")
	settings := config.DefaultSettings()
	settings.Cache.Directory = filepath.Join(tmp, "cache")
	mgr := config.NewManager(settingsPath)
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	handler := NewSettingsHandler(mgr)

	var imageBuf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&imageBuf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("image", "branding.png")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(imageBuf.Bytes()); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/settings/branding/home-tv/image", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req = mux.SetURLVars(req, map[string]string{"slot": "home-tv"})
	rec := httptest.NewRecorder()
	handler.UploadBrandingImage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body=%s", rec.Code, rec.Body.String())
	}

	loaded, err := mgr.Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if !strings.HasPrefix(loaded.Display.Branding.HomeTVImageURL, "/branding/images/home-tv") {
		t.Fatalf("home TV branding URL not persisted: %q", loaded.Display.Branding.HomeTVImageURL)
	}

	serveReq := httptest.NewRequest(http.MethodGet, "/api/branding/images/home-tv", nil)
	serveReq = mux.SetURLVars(serveReq, map[string]string{"slot": "home-tv"})
	serveRec := httptest.NewRecorder()
	handler.ServeBrandingImage(serveRec, serveReq)
	if serveRec.Code != http.StatusOK {
		t.Fatalf("serve status = %d, body=%s", serveRec.Code, serveRec.Body.String())
	}
	if contentType := serveRec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "image/png") {
		t.Fatalf("content type = %q, want image/png", contentType)
	}
}

func TestWebUIBrandingURLFallsBackToStatic(t *testing.T) {
	settings := config.DefaultSettings()
	settings.Cache.Directory = t.TempDir()

	// No uploaded image -> bundled static asset, with server base path applied.
	got := webUIBrandingURL(settings, "/mediastorm", "settings-tv", "favicon-32.png")
	if got != "/mediastorm/api/static/favicon-32.png" {
		t.Fatalf("fallback URL = %q", got)
	}

	// Empty base path produces a root-relative static URL.
	got = webUIBrandingURL(settings, "", "loading-logo", "app-logo-wide.png")
	if got != "/api/static/app-logo-wide.png" {
		t.Fatalf("fallback URL = %q", got)
	}
}

func TestWizardSourceBrandingSlotRoundTrips(t *testing.T) {
	slot, ok := brandingSlots["wizard-source"]
	if !ok {
		t.Fatal("wizard-source branding slot not registered")
	}
	var settings config.Settings
	slot.Set(&settings, "/branding/images/wizard-source?v=1")
	if got := slot.Get(settings); got != "/branding/images/wizard-source?v=1" {
		t.Fatalf("wizard-source slot get = %q", got)
	}
	if settings.Display.Branding.WizardSourceURL != "/branding/images/wizard-source?v=1" {
		t.Fatalf("wizard-source slot did not back WizardSourceURL: %q", settings.Display.Branding.WizardSourceURL)
	}
}

func TestWebUIBrandingURLPrefersUploadedImage(t *testing.T) {
	settings := config.DefaultSettings()
	settings.Cache.Directory = t.TempDir()

	dir := brandingImageDir(settings)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir branding dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings-tv.png"), []byte("png"), 0o644); err != nil {
		t.Fatalf("write branding image: %v", err)
	}

	got := webUIBrandingURL(settings, "", "settings-tv", "favicon-32.png")
	if !strings.HasPrefix(got, "/api/branding/images/settings-tv?v=") {
		t.Fatalf("custom branding URL = %q", got)
	}
}
