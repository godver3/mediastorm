package config

import "testing"

func TestIsProfileAllowed(t *testing.T) {
	tests := []struct {
		name     string
		allowed  []string
		profile  string
		expected bool
	}{
		{name: "empty allows all profiles", allowed: nil, profile: "profile-1", expected: true},
		{name: "matching profile allowed", allowed: []string{"profile-1"}, profile: "profile-1", expected: true},
		{name: "matching profile is case insensitive", allowed: []string{"Profile-1"}, profile: "profile-1", expected: true},
		{name: "non matching profile denied", allowed: []string{"profile-2"}, profile: "profile-1", expected: false},
		{name: "blank profile denied when scoped", allowed: []string{"profile-1"}, profile: "", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsProfileAllowed(tt.allowed, tt.profile); got != tt.expected {
				t.Fatalf("IsProfileAllowed(%v, %q) = %t, want %t", tt.allowed, tt.profile, got, tt.expected)
			}
		})
	}
}

func TestFilterSettingsForProfile(t *testing.T) {
	settings := Settings{
		Usenet: []UsenetSettings{
			{Name: "all", Enabled: true},
			{Name: "one", Enabled: true, AllowedProfiles: []string{"profile-1"}},
			{Name: "two", Enabled: true, AllowedProfiles: []string{"profile-2"}},
		},
		Indexers: []IndexerConfig{
			{Name: "all", Enabled: true},
			{Name: "one", Enabled: true, AllowedProfiles: []string{"profile-1"}},
			{Name: "two", Enabled: true, AllowedProfiles: []string{"profile-2"}},
		},
		TorrentScrapers: []TorrentScraperConfig{
			{Name: "all", Enabled: true},
			{Name: "one", Enabled: true, AllowedProfiles: []string{"profile-1"}},
			{Name: "two", Enabled: true, AllowedProfiles: []string{"profile-2"}},
		},
		Streaming: StreamingSettings{
			DebridProviders: []DebridProviderSettings{
				{Name: "all", Enabled: true},
				{Name: "one", Enabled: true, AllowedProfiles: []string{"profile-1"}},
				{Name: "two", Enabled: true, AllowedProfiles: []string{"profile-2"}},
			},
		},
		Live: LiveSettings{
			Sources: []LivePlaylistSource{
				{Name: "all"},
				{Name: "one", AllowedProfiles: []string{"profile-1"}},
				{Name: "two", AllowedProfiles: []string{"profile-2"}},
			},
		},
	}

	got := FilterSettingsForProfile(settings, "profile-1")

	if names := usenetNames(got.Usenet); !equalStrings(names, []string{"all", "one"}) {
		t.Fatalf("usenet names = %v, want [all one]", names)
	}
	if names := indexerNames(got.Indexers); !equalStrings(names, []string{"all", "one"}) {
		t.Fatalf("indexer names = %v, want [all one]", names)
	}
	if names := scraperNames(got.TorrentScrapers); !equalStrings(names, []string{"all", "one"}) {
		t.Fatalf("scraper names = %v, want [all one]", names)
	}
	if names := debridNames(got.Streaming.DebridProviders); !equalStrings(names, []string{"all", "one"}) {
		t.Fatalf("debrid provider names = %v, want [all one]", names)
	}
	if names := liveSourceNames(got.Live.Sources); !equalStrings(names, []string{"all", "one"}) {
		t.Fatalf("live source names = %v, want [all one]", names)
	}
}

func usenetNames(items []UsenetSettings) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
	}
	return names
}

func indexerNames(items []IndexerConfig) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
	}
	return names
}

func scraperNames(items []TorrentScraperConfig) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
	}
	return names
}

func debridNames(items []DebridProviderSettings) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
	}
	return names
}

func liveSourceNames(items []LivePlaylistSource) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
	}
	return names
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
