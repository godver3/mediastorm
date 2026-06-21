package usenetengine

import (
	"testing"

	"novastream/config"
)

func TestEnabledEnginesFiltersByProfile(t *testing.T) {
	settings := config.Settings{
		UsenetEngines: []config.UsenetEngineSettings{
			{Name: "disabled", Enabled: false, BaseURL: "http://disabled"},
			{Name: "missing-url", Enabled: true},
			{Name: "all", Enabled: true, BaseURL: "http://all"},
			{Name: "profile-a", Enabled: true, BaseURL: "http://profile-a", AllowedProfiles: []string{"a"}},
			{Name: "profile-b", Enabled: true, BaseURL: "http://profile-b", AllowedProfiles: []string{"b"}},
		},
	}

	got := EnabledEngines(settings, "a")
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %#v", len(got), got)
	}
	if got[0].Name != "all" || got[1].Name != "profile-a" {
		t.Fatalf("engines = %#v, want all/profile-a", got)
	}

	got = EnabledEngines(settings, "")
	if len(got) != 1 || got[0].Name != "all" {
		t.Fatalf("engines without profile = %#v, want only all", got)
	}
}

func TestDefaultAPIPath(t *testing.T) {
	if got := defaultAPIPath("altmount"); got != "/sabnzbd/api" {
		t.Fatalf("altmount path = %q, want /sabnzbd/api", got)
	}
	if got := defaultAPIPath("decypharr"); got != "/sabnzbd/api" {
		t.Fatalf("decypharr path = %q, want /sabnzbd/api", got)
	}
	if got := defaultAPIPath("nzbdav"); got != "/api" {
		t.Fatalf("nzbdav path = %q, want /api", got)
	}
}

func TestSplitConfiguredAPIEndpoint(t *testing.T) {
	baseURL, apiPath := splitConfiguredAPIEndpoint("http://engine:8282/sabnzbd/api", "", "decypharr")
	if baseURL != "http://engine:8282" || apiPath != "/sabnzbd/api" {
		t.Fatalf("decypharr split = %q %q, want root plus /sabnzbd/api", baseURL, apiPath)
	}

	baseURL, apiPath = splitConfiguredAPIEndpoint("http://engine:3000/api", "", "nzbdav")
	if baseURL != "http://engine:3000" || apiPath != "/api" {
		t.Fatalf("nzbdav split = %q %q, want root plus /api", baseURL, apiPath)
	}
}

func TestDefaultFileFieldName(t *testing.T) {
	if got := defaultFileFieldName("decypharr"); got != "name" {
		t.Fatalf("decypharr field = %q, want name", got)
	}
	if got := defaultFileFieldName("nzbdav"); got != "nzbfile" {
		t.Fatalf("nzbdav field = %q, want nzbfile", got)
	}
}

func TestCategoryInQuery(t *testing.T) {
	for _, engineType := range []string{"decypharr", "nzbdav"} {
		t.Run(engineType, func(t *testing.T) {
			if !categoryInQuery(engineType) {
				t.Fatalf("categoryInQuery(%q) = false, want true", engineType)
			}
		})
	}
	for _, engineType := range []string{"altmount", "nzbdavex", "sabnzbd", ""} {
		t.Run(engineType, func(t *testing.T) {
			if categoryInQuery(engineType) {
				t.Fatalf("categoryInQuery(%q) = true, want false", engineType)
			}
		})
	}
}

func TestDecypharrUsesSABAuthQueryParams(t *testing.T) {
	if got := usernameParam("decypharr"); got != "ma_username" {
		t.Fatalf("usernameParam(decypharr) = %q, want ma_username", got)
	}
	if got := passwordParam("decypharr"); got != "ma_password" {
		t.Fatalf("passwordParam(decypharr) = %q, want ma_password", got)
	}
	if got := usernameParam("nzbdav"); got != "" {
		t.Fatalf("usernameParam(nzbdav) = %q, want empty", got)
	}
	if got := passwordParam("nzbdav"); got != "" {
		t.Fatalf("passwordParam(nzbdav) = %q, want empty", got)
	}
}
