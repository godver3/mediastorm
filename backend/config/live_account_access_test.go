package config

import "testing"

func TestLiveAccountAssignmentAndLegacyFallback(t *testing.T) {
	accounts := []string{"household"}
	nobody := []string{}
	settings := Settings{Live: LiveSettings{
		Mode: "xtream", XtreamHost: "http://provider.example", XtreamUsername: "fixture", XtreamPassword: "synthetic",
		Sources: []LivePlaylistSource{
			{ID: "family", AllowedAccounts: &accounts},
			{ID: "unassigned", AllowedAccounts: &nobody},
		},
	}}
	allowed := FilterLiveSettingsForAccount(settings, "household")
	if len(allowed.Live.Sources) != 1 || allowed.Live.Sources[0].ID != "family" {
		t.Fatal("account grant not applied")
	}
	denied := FilterLiveSettingsForAccount(settings, "bernd")
	if len(denied.Live.Sources) != 0 || denied.Live.XtreamHost != "" || denied.Live.XtreamPassword != "" {
		t.Fatal("denied named provider fell back to legacy credentials")
	}
	if len(settings.Live.Sources) != 2 || settings.Live.XtreamPassword != "synthetic" {
		t.Fatal("filter mutated shared settings")
	}
	settings.Live.Sources = nil
	settings.Live.RequireAccountAssignment = true
	if FilterLiveSettingsForAccount(settings, "household").Live.XtreamHost != "" {
		t.Fatal("strict mode exposed unassigned legacy provider")
	}
	settings.Live.AllowedAccounts = &accounts
	if FilterLiveSettingsForAccount(settings, "household").Live.XtreamHost == "" {
		t.Fatal("explicit legacy grant not applied")
	}
}

func TestLiveAccountExplicitEmptyNeverMeansEveryone(t *testing.T) {
	nobody := []string{}
	if IsLiveAccountAllowed(&nobody, "household", false) || IsLiveAccountAllowed(&nobody, "", false) {
		t.Fatal("empty assignment granted access")
	}
	if !IsLiveAccountAllowed(nil, "household", false) {
		t.Fatal("legacy mode changed without opting in")
	}
	if IsLiveAccountAllowed(nil, "household", true) {
		t.Fatal("strict mode permitted an unassigned source")
	}
}
