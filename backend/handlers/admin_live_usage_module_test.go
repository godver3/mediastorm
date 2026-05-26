package handlers

import (
	"path/filepath"
	"testing"

	"novastream/config"
	"novastream/models"
)

func TestBuildDashboardLiveUsageUsesConfiguredLiveSources(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	manager := config.NewManager(settingsPath)
	if err := manager.Save(config.Settings{
		Live: config.LiveSettings{
			Sources: []config.LivePlaylistSource{
				{
					ID:          "news",
					Name:        "News",
					Mode:        "m3u",
					PlaylistURL: "http://example.com/news.m3u",
					MaxStreams:  2,
				},
				{
					ID:             "sports",
					Name:           "Sports",
					Mode:           "xtream",
					XtreamHost:     "http://xtream.example.com",
					XtreamUsername: "viewer",
					XtreamPassword: "secret",
					MaxStreams:     5,
				},
			},
		},
	}); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	handler := &AdminUIHandler{configManager: manager}
	_, byUser, buckets := handler.buildDashboardLiveUsage(true, []models.User{
		{ID: "profile-1", Name: "Living Room"},
	}, nil)

	if len(byUser) != 2 {
		t.Fatalf("byUser rows = %d, want one row per configured source", len(byUser))
	}
	if len(buckets) != 2 {
		t.Fatalf("bucket rows = %d, want one row per configured source", len(buckets))
	}

	limits := map[string]int{}
	for _, bucket := range buckets {
		limits[bucket.Label] = bucket.Max
	}
	if limits["News"] != 2 {
		t.Fatalf("News limit = %d, want 2", limits["News"])
	}
	if limits["Sports"] != 5 {
		t.Fatalf("Sports limit = %d, want 5", limits["Sports"])
	}
}
