package handlers

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"novastream/config"
	"novastream/models"
)

func TestResolveBrandingURL(t *testing.T) {
	cases := []struct {
		base string
		in   []string
		want string
	}{
		{"", []string{"", " "}, ""},
		{"", []string{"/branding/images/loading-logo?v=1"}, "/api/branding/images/loading-logo?v=1"},
		{"/media", []string{"", "branding/images/home-mobile-logo"}, "/media/api/branding/images/home-mobile-logo"},
		{"/media", []string{"/api/x"}, "/media/api/x"},
		{"/media", []string{"https://cdn.example/logo.png"}, "https://cdn.example/logo.png"},
	}
	for _, c := range cases {
		if got := resolveBrandingURL(c.base, c.in...); got != c.want {
			t.Errorf("resolveBrandingURL(%q, %q) = %q, want %q", c.base, c.in, got, c.want)
		}
	}
}

func TestWebPlaybackHandlerUsesBrandingLogo(t *testing.T) {
	newHandler := func(logo string) *WebPlaybackHandler {
		settings := config.DefaultSettings()
		settings.Display.Branding.HomeMobileLogoURL = logo
		mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
		if err := mgr.Save(settings); err != nil {
			t.Fatalf("save settings: %v", err)
		}
		h := NewWebPlaybackHandler(fakeWebPlaybackUsers{users: []models.User{{ID: "p", Name: "Main"}}},
			fakeWebPlaybackSessions{token: "tok", session: models.Session{AccountID: "acc", IsMaster: true}}, "/media")
		h.Branding = NewSettingsHandler(mgr)
		return h
	}
	render := func(h *WebPlaybackHandler) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/watch/playback.html?token=tok", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		return rec.Body.String()
	}

	body := render(newHandler("/branding/images/home-mobile-logo?v=7"))
	if !strings.Contains(body, `<img class="brand-logo" src="/media/api/branding/images/home-mobile-logo?v=7"`) {
		t.Fatalf("expected branded logo in header")
	}
	if strings.Contains(body, `<div class="brand-title">mediastorm</div>`) {
		t.Fatalf("default wordmark should be replaced")
	}

	body = render(newHandler(""))
	if !strings.Contains(body, `<img class="brand-icon" src="/media/api/static/icon-80.png"`) || strings.Contains(body, "brand-logo\" src") {
		t.Fatalf("expected default wordmark without branding")
	}
}
