package models

import "testing"

func TestStringPtr(t *testing.T) {
	s := StringPtr("hello")
	if s == nil || *s != "hello" {
		t.Fatal("StringPtr failed")
	}
}

func newGlobal() *ResolvedLiveSource {
	return &ResolvedLiveSource{
		Mode:                  "m3u",
		PlaylistURL:           "http://global.m3u",
		XtreamHost:            "http://global.host",
		XtreamUsername:        "guser",
		XtreamPassword:        "gpass",
		PlaylistCacheTTLHours: 6,
		ProbeSizeMB:           10,
		AnalyzeDurationSec:    5,
		LowLatency:            false,
		EnabledCategories:     []string{"News"},
		MaxChannels:           500,
	}
}

func TestResolveLiveSource_AllNil(t *testing.T) {
	profile := &LiveTVSettings{}
	g := newGlobal()
	r := ResolveLiveSource(profile, g)

	if r.Mode != "m3u" {
		t.Errorf("Mode = %q, want %q", r.Mode, "m3u")
	}
	if r.PlaylistURL != "http://global.m3u" {
		t.Errorf("PlaylistURL = %q, want %q", r.PlaylistURL, "http://global.m3u")
	}
	if r.PlaylistCacheTTLHours != 6 {
		t.Errorf("PlaylistCacheTTLHours = %d, want 6", r.PlaylistCacheTTLHours)
	}
	if r.LowLatency != false {
		t.Errorf("LowLatency = %v, want false", r.LowLatency)
	}
	if r.MaxChannels != 500 {
		t.Errorf("MaxChannels = %d, want 500", r.MaxChannels)
	}
}

func TestResolveLiveSource_NilProfile(t *testing.T) {
	g := newGlobal()
	g.Mode = "xtream"
	r := ResolveLiveSource(nil, g)
	if r.Mode != "xtream" {
		t.Errorf("Mode = %q, want %q", r.Mode, "xtream")
	}
}

func TestResolveLiveSource_OverrideFields(t *testing.T) {
	profile := &LiveTVSettings{
		Mode:           StringPtr("xtream"),
		XtreamHost:     StringPtr("http://profile.host"),
		XtreamUsername: StringPtr("puser"),
		XtreamPassword: StringPtr("ppass"),
	}

	r := ResolveLiveSource(profile, newGlobal())

	if r.Mode != "xtream" {
		t.Errorf("Mode = %q, want %q", r.Mode, "xtream")
	}
	if r.PlaylistURL != "http://global.m3u" {
		t.Errorf("PlaylistURL should fall back to global, got %q", r.PlaylistURL)
	}
	if r.XtreamHost != "http://profile.host" {
		t.Errorf("XtreamHost = %q, want %q", r.XtreamHost, "http://profile.host")
	}
}

func TestResolveLiveSource_PartialOverride(t *testing.T) {
	profile := &LiveTVSettings{
		XtreamUsername: StringPtr("override-user"),
	}

	g := newGlobal()
	g.Mode = "xtream"
	r := ResolveLiveSource(profile, g)

	if r.Mode != "xtream" {
		t.Errorf("Mode = %q, want %q", r.Mode, "xtream")
	}
	if r.XtreamUsername != "override-user" {
		t.Errorf("XtreamUsername = %q, want %q", r.XtreamUsername, "override-user")
	}
	if r.XtreamPassword != "gpass" {
		t.Errorf("XtreamPassword = %q, want %q", r.XtreamPassword, "gpass")
	}
}

func TestResolveLiveSource_TuningOverrides(t *testing.T) {
	cacheTTL := 12
	probe := 20
	lowLat := true
	profile := &LiveTVSettings{
		PlaylistCacheTTLHours: &cacheTTL,
		ProbeSizeMB:           &probe,
		LowLatency:            &lowLat,
	}

	r := ResolveLiveSource(profile, newGlobal())

	if r.PlaylistCacheTTLHours != 12 {
		t.Errorf("PlaylistCacheTTLHours = %d, want 12", r.PlaylistCacheTTLHours)
	}
	if r.ProbeSizeMB != 20 {
		t.Errorf("ProbeSizeMB = %d, want 20", r.ProbeSizeMB)
	}
	if r.AnalyzeDurationSec != 5 {
		t.Errorf("AnalyzeDurationSec should fall back to global (5), got %d", r.AnalyzeDurationSec)
	}
	if r.LowLatency != true {
		t.Errorf("LowLatency = %v, want true", r.LowLatency)
	}
}

func TestResolveLiveSource_FilteringOverrides(t *testing.T) {
	maxCh := 100
	profile := &LiveTVSettings{
		Filtering: &LiveTVFilterOverrides{
			EnabledCategories: []string{"Sports", "Movies"},
			MaxChannels:       &maxCh,
		},
	}

	r := ResolveLiveSource(profile, newGlobal())

	if len(r.EnabledCategories) != 2 || r.EnabledCategories[0] != "Sports" {
		t.Errorf("EnabledCategories = %v, want [Sports Movies]", r.EnabledCategories)
	}
	if r.MaxChannels != 100 {
		t.Errorf("MaxChannels = %d, want 100", r.MaxChannels)
	}
}
