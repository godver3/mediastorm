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

func TestDashboardVODUsageIncludesHLSAndDirectButExcludesLive(t *testing.T) {
	users := []models.User{
		{ID: "profile-web", AccountID: "account-a"},
		{ID: "profile-native", AccountID: "account-b"},
		{ID: "profile-live", AccountID: "account-a"},
	}
	streams := []map[string]interface{}{
		{
			"type":       "hls",
			"profile_id": "profile-web",
			"is_live":    false,
		},
		{
			"type":       "direct",
			"profile_id": "profile-native",
			"is_live":    false,
		},
		{
			"type":       "hls",
			"profile_id": "profile-live",
			"is_live":    true,
		},
	}

	if got := countDashboardVODStreams(streams); got != 2 {
		t.Fatalf("countDashboardVODStreams() = %d, want 2", got)
	}

	byAccount := dashboardVODStreamsByAccount(streams, users)
	if got := byAccount["account-a"]; got != 1 {
		t.Fatalf("account-a VOD streams = %d, want 1", got)
	}
	if got := byAccount["account-b"]; got != 1 {
		t.Fatalf("account-b VOD streams = %d, want 1", got)
	}
}

func TestDashboardVODUsagePrefersTrackedAccountID(t *testing.T) {
	streams := []map[string]interface{}{
		{
			"type":       "direct",
			"profile_id": "missing-profile",
			"account_id": "account-a",
			"is_live":    false,
		},
	}

	byAccount := dashboardVODStreamsByAccount(streams, nil)
	if got := byAccount["account-a"]; got != 1 {
		t.Fatalf("account-a VOD streams = %d, want 1", got)
	}
}

func TestBuildDashboardLiveUsageHonorsAllowedProfiles(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	manager := config.NewManager(settingsPath)
	if err := manager.Save(config.Settings{
		Live: config.LiveSettings{
			Sources: []config.LivePlaylistSource{
				{ID: "news", Name: "News", Mode: "m3u", PlaylistURL: "http://example.com/news.m3u"},
				{
					ID: "sports", Name: "Sports", Mode: "m3u", PlaylistURL: "http://example.com/sports.m3u",
					AllowedProfiles: []string{"profile-allowed"},
				},
				{
					ID: "adult", Name: "Adult", Mode: "m3u", PlaylistURL: "http://example.com/adult.m3u",
					AllowedProfiles: []string{"profile-nobody"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	handler := &AdminUIHandler{configManager: manager}
	_, byUser, buckets := handler.buildDashboardLiveUsage(true, []models.User{
		{ID: "profile-allowed", Name: "Allowed"},
		{ID: "profile-kid", Name: "Kid"},
	}, nil)

	got := map[string][]string{}
	for _, row := range byUser {
		got[row.User] = append(got[row.User], row.Bucket)
	}
	if len(got["Allowed"]) != 2 {
		t.Fatalf("Allowed rows = %v, want News and Sports", got["Allowed"])
	}
	if len(got["Kid"]) != 1 || got["Kid"][0] != "News" {
		t.Fatalf("Kid rows = %v, want only News", got["Kid"])
	}

	usersByBucket := map[string][]string{}
	for _, bucket := range buckets {
		usersByBucket[bucket.Label] = bucket.Users
	}
	if users := usersByBucket["Sports"]; len(users) != 1 || users[0] != "Allowed" {
		t.Fatalf("Sports users = %v, want [Allowed]", users)
	}
	if _, ok := usersByBucket["Adult"]; ok {
		t.Fatalf("Adult bucket shown although no listed profile may use it")
	}
}

func TestBuildDashboardLiveUsageOmitsProfilesWithoutLiveTV(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	manager := config.NewManager(settingsPath)
	if err := manager.Save(config.Settings{
		Live: config.LiveSettings{
			Sources: []config.LivePlaylistSource{{
				ID: "only", Name: "Only", Mode: "m3u", PlaylistURL: "http://example.com/only.m3u",
				AllowedProfiles: []string{"profile-allowed"},
			}},
		},
	}); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	handler := &AdminUIHandler{configManager: manager}
	_, byUser, _ := handler.buildDashboardLiveUsage(true, []models.User{{ID: "profile-kid", Name: "Kid"}}, nil)
	if len(byUser) != 0 {
		t.Fatalf("byUser = %+v, want no rows for a profile without Live TV", byUser)
	}
}
