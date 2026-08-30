package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"novastream/config"
)

// An install with no relay configured is the shipped default, and PearTube has
// to be selectable and usable in exactly that state — otherwise there is no way
// to configure a relay from the admin UI at all. It lives with the other search
// sources, so what the page must carry is the scraper type and its fields.
func TestSettingsPageOffersPearTubeAsAScraperWithoutARelay(t *testing.T) {
	h, sessionsSvc, _ := newAdminOnboardingTestHandler(t, func(settings *config.Settings) {
		settings.UI.OnboardingCompleted = true
	})
	req := newAdminRequestWithSession(t, sessionsSvc, http.MethodGet, "/admin/settings", true)
	rr := httptest.NewRecorder()

	h.RequireAuth(h.SettingsPage).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !containsAll(body,
		`"torrentScrapers"`,
		`"peartube"`,
		`Contribute watched media`,
		`Enable archive retention`,
		`Watch-only`,
		`PEARTUBE_RELAY_URL`,
	) {
		t.Fatal("settings page does not expose explicit PearTube policy")
	}
	if strings.Contains(body, `PEARTUBE_AUTOSEED`) {
		t.Fatal("settings page still advertises environment-based contribution consent")
	}
	if !strings.Contains(body, `fieldKey.startsWith('config.') && typeof value === 'number'`) {
		t.Fatal("nested config number controls do not serialize decimal strings")
	}
	// The relay's own remedy string is what an operator needs to fix a gated
	// relay without reading logs, so the page must surface it.
	if !strings.Contains(body, `Fix on the relay:`) {
		t.Fatal("the page cannot surface the relay's remedy")
	}
}
