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
	if got := defaultAPIPath("decypharr"); got != "/sabnzbd/api" {
		t.Fatalf("decypharr path = %q, want /sabnzbd/api", got)
	}
	if got := defaultAPIPath("nzbdav"); got != "/api" {
		t.Fatalf("nzbdav path = %q, want /api", got)
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
