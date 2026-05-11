package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/config"
	"novastream/handlers"
	"novastream/models"
	"novastream/services/accounts"
	"novastream/services/sessions"
	"novastream/services/users"
)

func newAdminOnboardingTestHandler(t *testing.T, mutate func(*config.Settings)) (*handlers.AdminUIHandler, *sessions.Service, string) {
	t.Helper()

	tmp := t.TempDir()
	settingsPath := filepath.Join(tmp, "settings.json")
	settings := config.DefaultSettings()
	if mutate != nil {
		mutate(&settings)
	}
	if err := config.NewManager(settingsPath).Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	accountsSvc, err := accounts.NewService(tmp)
	if err != nil {
		t.Fatalf("accounts service: %v", err)
	}
	usersSvc, err := users.NewService(tmp)
	if err != nil {
		t.Fatalf("users service: %v", err)
	}
	sessionsSvc, err := sessions.NewService("", time.Hour)
	if err != nil {
		t.Fatalf("sessions service: %v", err)
	}

	h := handlers.NewAdminUIHandler(settingsPath, "", nil, usersSvc, nil, config.NewManager(settingsPath))
	h.SetAccountsService(accountsSvc)
	h.SetSessionsService(sessionsSvc)
	return h, sessionsSvc, settingsPath
}

func newAdminRequestWithSession(t *testing.T, sessionsSvc *sessions.Service, method, path string, isMaster bool) *http.Request {
	t.Helper()
	session, err := sessionsSvc.Create(models.MasterAccountID, isMaster, "test", "127.0.0.1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	req := httptest.NewRequest(method, path, nil)
	req.AddCookie(&http.Cookie{Name: "strmr_admin_session", Value: session.Token})
	return req
}

func TestAdminOnboardingStatus_DefaultNeedsOnboarding(t *testing.T) {
	h, sessionsSvc, _ := newAdminOnboardingTestHandler(t, nil)
	req := newAdminRequestWithSession(t, sessionsSvc, http.MethodGet, "/admin/api/onboarding/status", true)
	rr := httptest.NewRecorder()

	h.RequireMasterAuth(h.GetOnboardingStatus).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if got := rr.Body.String(); !containsAll(got, `"needsOnboarding":true`, `"defaultPassword":true`, `"hasSearchSource":false`) {
		t.Fatalf("unexpected onboarding status body: %s", got)
	}
	if got := rr.Body.String(); !strings.Contains(got, `"adminWalkthroughDismissed":false`) {
		t.Fatalf("missing walkthrough dismissal state in onboarding status body: %s", got)
	}
}

func TestAdminOnboardingStatus_SearchSourceFollowsStreamingMode(t *testing.T) {
	tests := []struct {
		name          string
		mode          config.StreamingServiceMode
		addIndexer    bool
		wantHasSearch string
	}{
		{name: "debrid accepts torrent source", mode: config.StreamingServiceModeDebrid, wantHasSearch: `"hasSearchSource":true`},
		{name: "usenet requires indexer", mode: config.StreamingServiceModeUsenet, wantHasSearch: `"hasSearchSource":false`},
		{name: "hybrid requires both source classes", mode: config.StreamingServiceModeHybrid, wantHasSearch: `"hasSearchSource":false`},
		{name: "hybrid accepts both source classes", mode: config.StreamingServiceModeHybrid, addIndexer: true, wantHasSearch: `"hasSearchSource":true`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, sessionsSvc, _ := newAdminOnboardingTestHandler(t, func(settings *config.Settings) {
				settings.Streaming.ServiceMode = tt.mode
				if tt.addIndexer {
					settings.Indexers = []config.IndexerConfig{{Name: "Newznab", Type: "newznab", URL: "http://indexer.test/api", APIKey: "key", Enabled: true}}
				}
			})
			req := newAdminRequestWithSession(t, sessionsSvc, http.MethodGet, "/admin/api/onboarding/status", true)
			rr := httptest.NewRecorder()

			h.RequireMasterAuth(h.GetOnboardingStatus).ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
			}
			if got := rr.Body.String(); !strings.Contains(got, tt.wantHasSearch) {
				t.Fatalf("unexpected onboarding status body: %s", got)
			}
		})
	}
}

func TestAdminOnboardingSkipAndCompletePersistState(t *testing.T) {
	h, sessionsSvc, settingsPath := newAdminOnboardingTestHandler(t, nil)

	skipReq := newAdminRequestWithSession(t, sessionsSvc, http.MethodPost, "/admin/api/onboarding/skip", true)
	skipRR := httptest.NewRecorder()
	h.RequireMasterAuth(h.SkipOnboarding).ServeHTTP(skipRR, skipReq)
	if skipRR.Code != http.StatusOK {
		t.Fatalf("skip status = %d, want %d; body=%s", skipRR.Code, http.StatusOK, skipRR.Body.String())
	}

	settings, err := config.NewManager(settingsPath).Load()
	if err != nil {
		t.Fatalf("load settings after skip: %v", err)
	}
	if !settings.UI.OnboardingSkipped || settings.UI.OnboardingSkippedAt == "" {
		t.Fatalf("skip state not persisted: %+v", settings.UI)
	}

	completeReq := newAdminRequestWithSession(t, sessionsSvc, http.MethodPost, "/admin/api/onboarding/complete", true)
	completeRR := httptest.NewRecorder()
	h.RequireMasterAuth(h.CompleteOnboarding).ServeHTTP(completeRR, completeReq)
	if completeRR.Code != http.StatusOK {
		t.Fatalf("complete status = %d, want %d; body=%s", completeRR.Code, http.StatusOK, completeRR.Body.String())
	}

	settings, err = config.NewManager(settingsPath).Load()
	if err != nil {
		t.Fatalf("load settings after complete: %v", err)
	}
	if !settings.UI.OnboardingCompleted || settings.UI.OnboardingCompletedAt == "" {
		t.Fatalf("complete state not persisted: %+v", settings.UI)
	}
}

func TestAdminWalkthroughDismissPersistsState(t *testing.T) {
	h, sessionsSvc, settingsPath := newAdminOnboardingTestHandler(t, func(settings *config.Settings) {
		settings.UI.OnboardingSkipped = true
	})

	dismissReq := newAdminRequestWithSession(t, sessionsSvc, http.MethodPost, "/admin/api/walkthrough/dismiss", true)
	dismissRR := httptest.NewRecorder()
	h.RequireMasterAuth(h.DismissAdminWalkthrough).ServeHTTP(dismissRR, dismissReq)
	if dismissRR.Code != http.StatusOK {
		t.Fatalf("dismiss status = %d, want %d; body=%s", dismissRR.Code, http.StatusOK, dismissRR.Body.String())
	}

	settings, err := config.NewManager(settingsPath).Load()
	if err != nil {
		t.Fatalf("load settings after dismiss: %v", err)
	}
	if !settings.UI.AdminWalkthroughDismissed || settings.UI.AdminWalkthroughDismissedAt == "" {
		t.Fatalf("walkthrough dismiss state not persisted: %+v", settings.UI)
	}

	statusReq := newAdminRequestWithSession(t, sessionsSvc, http.MethodGet, "/admin/api/onboarding/status", true)
	statusRR := httptest.NewRecorder()
	h.RequireMasterAuth(h.GetOnboardingStatus).ServeHTTP(statusRR, statusReq)
	if statusRR.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", statusRR.Code, http.StatusOK, statusRR.Body.String())
	}
	if got := statusRR.Body.String(); !strings.Contains(got, `"adminWalkthroughDismissed":true`) {
		t.Fatalf("walkthrough dismissal not reflected in status body: %s", got)
	}
}

func TestAdminOnboardingGateRedirectsIncompleteMasterPage(t *testing.T) {
	h, sessionsSvc, _ := newAdminOnboardingTestHandler(t, nil)
	req := newAdminRequestWithSession(t, sessionsSvc, http.MethodGet, "/admin/settings", true)
	rr := httptest.NewRecorder()

	h.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}).ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	if loc := rr.Header().Get("Location"); loc != "/admin/onboarding" {
		t.Fatalf("Location = %q, want /admin/onboarding", loc)
	}
}

func TestAdminOnboardingGateAllowsSkippedAndAPI(t *testing.T) {
	h, sessionsSvc, _ := newAdminOnboardingTestHandler(t, func(settings *config.Settings) {
		settings.UI.OnboardingSkipped = true
	})

	pageReq := newAdminRequestWithSession(t, sessionsSvc, http.MethodGet, "/admin/settings", true)
	pageRR := httptest.NewRecorder()
	h.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}).ServeHTTP(pageRR, pageReq)
	if pageRR.Code != http.StatusNoContent {
		t.Fatalf("skipped page status = %d, want %d", pageRR.Code, http.StatusNoContent)
	}

	h2, sessionsSvc2, _ := newAdminOnboardingTestHandler(t, nil)
	apiReq := newAdminRequestWithSession(t, sessionsSvc2, http.MethodGet, "/admin/api/status", true)
	apiRR := httptest.NewRecorder()
	h2.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}).ServeHTTP(apiRR, apiReq)
	if apiRR.Code != http.StatusNoContent {
		t.Fatalf("api status = %d, want %d", apiRR.Code, http.StatusNoContent)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}
