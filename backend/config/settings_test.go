package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMigratesCreditsDetectionToCreditsAutoSkip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"playback":{"preferredPlayer":"native","creditsDetection":true}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if !settings.Playback.CreditsAutoSkip {
		t.Fatal("expected legacy creditsDetection=true to migrate to creditsAutoSkip=true")
	}
	if settings.Playback.CreditsDetection {
		t.Fatal("expected legacy creditsDetection field to be cleared after migration")
	}
}

func TestLoadMigratesYouTubeProxyURLFromMetadataToPlayback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"metadata":{"youtubeProxyUrl":"http://gluetun:8888"},"playback":{"preferredPlayer":"native"}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if settings.Playback.YouTubeProxyURL != "http://gluetun:8888" {
		t.Fatalf("Playback.YouTubeProxyURL = %q, want migrated proxy", settings.Playback.YouTubeProxyURL)
	}
}

func TestLoadBackfillsUnknownTrackPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"filtering":{"unknownTrackPolicy":"invalid"}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if settings.Filtering.UnknownTrackPolicy != UnknownTrackPolicyNone {
		t.Fatalf("UnknownTrackPolicy = %q, want %q", settings.Filtering.UnknownTrackPolicy, UnknownTrackPolicyNone)
	}
}

func TestLoadClampsHomeShelfAndHeroScale(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantShelf float64
		wantHero  float64
	}{
		{name: "missing", raw: `{"homeShelves":{"shelves":[]}}`, wantShelf: 1.0, wantHero: 1.0},
		{name: "too low", raw: `{"homeShelves":{"shelves":[],"homeShelfScale":0.25,"homeHeroScale":0.25}}`, wantShelf: 0.5, wantHero: 0.5},
		{name: "too high", raw: `{"homeShelves":{"shelves":[],"homeShelfScale":1.4,"homeHeroScale":1.4}}`, wantShelf: 1.0, wantHero: 1.0},
		{name: "valid", raw: `{"homeShelves":{"shelves":[],"homeShelfScale":0.75,"homeHeroScale":0.65}}`, wantShelf: 0.75, wantHero: 0.65},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(tt.raw), 0o600); err != nil {
				t.Fatalf("write settings: %v", err)
			}

			settings, err := NewManager(path).Load()
			if err != nil {
				t.Fatalf("load settings: %v", err)
			}

			if settings.HomeShelves.HomeShelfScale != tt.wantShelf {
				t.Fatalf("HomeShelfScale = %v, want %v", settings.HomeShelves.HomeShelfScale, tt.wantShelf)
			}
			if settings.HomeShelves.HomeHeroScale != tt.wantHero {
				t.Fatalf("HomeHeroScale = %v, want %v", settings.HomeShelves.HomeHeroScale, tt.wantHero)
			}
		})
	}
}

func TestLoadBackfillsStreamingServicesHomeShelf(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"homeShelves":{"shelves":[
		{"id":"continue-watching","name":"Continue Watching","enabled":true,"order":1},
		{"id":"trending-tv","name":"Trending TV Shows","enabled":true,"order":6}
	]}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	var shelf *ShelfConfig
	trendingTVOrder := -1
	for i := range settings.HomeShelves.Shelves {
		if settings.HomeShelves.Shelves[i].ID == "streaming-services" {
			shelf = &settings.HomeShelves.Shelves[i]
		}
		if settings.HomeShelves.Shelves[i].ID == "trending-tv" {
			trendingTVOrder = settings.HomeShelves.Shelves[i].Order
		}
	}
	if shelf == nil {
		t.Fatal("expected streaming-services shelf to be backfilled")
	}
	if !shelf.Enabled {
		t.Fatal("expected streaming-services shelf to default enabled")
	}
	if shelf.Order != trendingTVOrder+1 {
		t.Fatalf("streaming-services order = %d, want after trending-tv order %d", shelf.Order, trendingTVOrder)
	}
}

func TestLoadMigratesLegacyLiveSettingsToFirstSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{
		"live": {
			"mode": "m3u",
			"playlistUrl": "http://example.com/live.m3u",
			"maxStreams": 3,
			"playlistCacheTtlHours": 8,
			"probeSizeMb": 12,
			"analyzeDurationSec": 6,
			"lowLatency": true,
			"streamFormat": "direct",
			"filtering": {
				"enabledCategories": ["News"],
				"maxChannels": 50
			},
			"epg": {
				"enabled": true,
				"xmltvUrl": "http://example.com/epg.xml",
				"refreshIntervalHours": 4,
				"retentionDays": 2,
				"timeOffsetMinutes": 30
			}
		}
	}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if len(settings.Live.Sources) != 1 {
		t.Fatalf("Live.Sources length = %d, want 1", len(settings.Live.Sources))
	}
	src := settings.Live.Sources[0]
	if src.Name != "Default" || src.PlaylistURL != "http://example.com/live.m3u" {
		t.Fatalf("migrated source = %+v, want default source with legacy playlist URL", src)
	}
	if src.Enabled == nil || !*src.Enabled {
		t.Fatalf("source enabled = %v, want true", src.Enabled)
	}
	if src.MaxStreams != 3 || src.StreamFormat != "direct" || !src.LowLatency {
		t.Fatalf("source tuning not migrated: %+v", src)
	}
	if src.Filtering.MaxChannels != 50 || len(src.Filtering.EnabledCategories) != 1 || src.Filtering.EnabledCategories[0] != "News" {
		t.Fatalf("source filtering not migrated: %+v", src.Filtering)
	}
	if !src.EPG.Enabled || src.EPG.XmltvUrl != "http://example.com/epg.xml" || src.EPG.TimeOffsetMinutes != 30 {
		t.Fatalf("source EPG not migrated: %+v", src.EPG)
	}
}

func TestSavePreservesClearedLiveSourceProxyURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	manager := NewManager(path)

	enabled := true
	settings := DefaultSettings()
	settings.Live.ProxyURL = "socks5://127.0.0.1:18080"
	settings.Live.Sources = []LivePlaylistSource{
		{
			ID:          "default",
			Name:        "Default",
			Mode:        "m3u",
			PlaylistURL: "http://example.com/live.m3u",
			ProxyURL:    "",
			Enabled:     &enabled,
		},
	}

	if err := manager.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	loaded, err := manager.Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if len(loaded.Live.Sources) != 1 {
		t.Fatalf("Live.Sources length = %d, want 1", len(loaded.Live.Sources))
	}
	if loaded.Live.Sources[0].ProxyURL != "" {
		t.Fatalf("Live.Sources[0].ProxyURL = %q, want cleared value", loaded.Live.Sources[0].ProxyURL)
	}
}
