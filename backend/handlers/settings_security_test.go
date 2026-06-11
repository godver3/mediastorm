package handlers

import (
	"testing"

	"novastream/config"
)

func TestRedactSettings(t *testing.T) {
	s := config.Settings{
		Server: config.ServerSettings{
			Host:           "0.0.0.0",
			Port:           7777,
			HomepageAPIKey: "secret-homepage-key",
		},
		Usenet: []config.UsenetSettings{
			{Name: "provider1", Host: "news.example.com", Password: "usenet-pass"},
		},
		Indexers: []config.IndexerConfig{
			{Name: "nzb", APIKey: "indexer-key"},
		},
		TorrentScrapers: []config.TorrentScraperConfig{
			{Name: "jackett", APIKey: "scraper-key"},
		},
		Metadata: config.MetadataSettings{
			TVDBAPIKey:   "tvdb-key",
			TMDBAPIKey:   "tmdb-key",
			AIAPIKey:     "ai-key",
			GeminiAPIKey: "gemini-key",
		},
		Playback: config.PlaybackSettings{
			YouTubeProxyURL: "http://user:pass@gluetun:8888",
		},
		MDBList: config.MDBListSettings{
			APIKey: "mdblist-key",
		},
		Trakt: config.TraktSettings{
			Accounts: []config.TraktAccount{
				{ID: "t1", ClientSecret: "trakt-secret", AccessToken: "trakt-access", RefreshToken: "trakt-refresh"},
			},
		},
		Plex: config.PlexSettings{
			Accounts: []config.PlexAccount{
				{ID: "p1", AuthToken: "plex-token"},
			},
		},
		Jellyfin: config.JellyfinSettings{
			Accounts: []config.JellyfinAccount{
				{ID: "j1", Token: "jellyfin-token"},
			},
		},
		Database: config.DatabaseSettings{
			URL: "postgres://user:secret@localhost:5432/db",
		},
	}

	redactSettings(&s)

	const redacted = "••••••••"

	// Verify sensitive fields are redacted
	if s.Server.HomepageAPIKey != redacted {
		t.Errorf("HomepageAPIKey not redacted: %q", s.Server.HomepageAPIKey)
	}
	if s.Usenet[0].Password != redacted {
		t.Errorf("Usenet password not redacted: %q", s.Usenet[0].Password)
	}
	if s.Indexers[0].APIKey != redacted {
		t.Errorf("Indexer APIKey not redacted: %q", s.Indexers[0].APIKey)
	}
	if s.TorrentScrapers[0].APIKey != redacted {
		t.Errorf("TorrentScraper APIKey not redacted: %q", s.TorrentScrapers[0].APIKey)
	}
	if s.Metadata.TVDBAPIKey != redacted {
		t.Errorf("TVDBAPIKey not redacted: %q", s.Metadata.TVDBAPIKey)
	}
	if s.Metadata.TMDBAPIKey != redacted {
		t.Errorf("TMDBAPIKey not redacted: %q", s.Metadata.TMDBAPIKey)
	}
	if s.Metadata.AIAPIKey != redacted {
		t.Errorf("AIAPIKey not redacted: %q", s.Metadata.AIAPIKey)
	}
	if s.Metadata.GeminiAPIKey != redacted {
		t.Errorf("GeminiAPIKey not redacted: %q", s.Metadata.GeminiAPIKey)
	}
	if s.Playback.YouTubeProxyURL != redacted {
		t.Errorf("YouTubeProxyURL not redacted: %q", s.Playback.YouTubeProxyURL)
	}
	if s.MDBList.APIKey != redacted {
		t.Errorf("MDBList APIKey not redacted: %q", s.MDBList.APIKey)
	}
	if s.Trakt.Accounts[0].ClientSecret != redacted {
		t.Errorf("Trakt account ClientSecret not redacted: %q", s.Trakt.Accounts[0].ClientSecret)
	}
	if s.Trakt.Accounts[0].AccessToken != redacted {
		t.Errorf("Trakt account AccessToken not redacted: %q", s.Trakt.Accounts[0].AccessToken)
	}
	if s.Trakt.Accounts[0].RefreshToken != redacted {
		t.Errorf("Trakt account RefreshToken not redacted: %q", s.Trakt.Accounts[0].RefreshToken)
	}
	if s.Plex.Accounts[0].AuthToken != redacted {
		t.Errorf("Plex account AuthToken not redacted: %q", s.Plex.Accounts[0].AuthToken)
	}
	if s.Jellyfin.Accounts[0].Token != redacted {
		t.Errorf("Jellyfin account Token not redacted: %q", s.Jellyfin.Accounts[0].Token)
	}
	if s.Database.URL != redacted {
		t.Errorf("Database URL not redacted: %q", s.Database.URL)
	}

	// Verify non-sensitive fields are untouched
	if s.Server.Host != "0.0.0.0" {
		t.Errorf("Host was modified: %q", s.Server.Host)
	}
	if s.Server.Port != 7777 {
		t.Errorf("Port was modified: %d", s.Server.Port)
	}
	if s.Usenet[0].Name != "provider1" {
		t.Errorf("Usenet name was modified: %q", s.Usenet[0].Name)
	}
	if s.Trakt.Accounts[0].ID != "t1" {
		t.Errorf("Trakt account ID was modified: %q", s.Trakt.Accounts[0].ID)
	}
	if s.Plex.Accounts[0].ID != "p1" {
		t.Errorf("Plex account ID was modified: %q", s.Plex.Accounts[0].ID)
	}
	if s.Jellyfin.Accounts[0].ID != "j1" {
		t.Errorf("Jellyfin account ID was modified: %q", s.Jellyfin.Accounts[0].ID)
	}
}

func TestPreserveRedactedFields_RestoresRealCredentials(t *testing.T) {
	existing := config.Settings{
		Metadata: config.MetadataSettings{
			TVDBAPIKey:   "real-tvdb-key",
			TMDBAPIKey:   "real-tmdb-key",
			AIAPIKey:     "real-ai-key",
			GeminiAPIKey: "real-gemini-key",
		},
		Playback: config.PlaybackSettings{
			YouTubeProxyURL: "http://real-proxy:8888",
		},
		Usenet: []config.UsenetSettings{
			{Name: "provider1", Password: "real-password"},
		},
		Indexers: []config.IndexerConfig{
			{Name: "nzb", APIKey: "real-indexer-key"},
		},
		MDBList: config.MDBListSettings{
			APIKey: "real-mdblist-key",
		},
		Trakt: config.TraktSettings{
			Accounts: []config.TraktAccount{
				{ID: "t1", ClientSecret: "real-trakt-secret", AccessToken: "real-trakt-access", RefreshToken: "real-trakt-refresh"},
			},
		},
		Plex: config.PlexSettings{
			Accounts: []config.PlexAccount{
				{ID: "p1", AuthToken: "real-plex-token"},
			},
		},
		Jellyfin: config.JellyfinSettings{
			Accounts: []config.JellyfinAccount{
				{ID: "j1", Token: "real-jellyfin-token"},
			},
		},
		Database: config.DatabaseSettings{
			URL: "postgres://user:secret@localhost:5432/db",
		},
	}

	// Simulate a non-master user saving back redacted settings
	incoming := config.Settings{
		Metadata: config.MetadataSettings{
			TVDBAPIKey:   redactedPlaceholder,
			TMDBAPIKey:   redactedPlaceholder,
			AIAPIKey:     redactedPlaceholder,
			GeminiAPIKey: redactedPlaceholder,
		},
		Playback: config.PlaybackSettings{
			YouTubeProxyURL: redactedPlaceholder,
		},
		Usenet: []config.UsenetSettings{
			{Name: "provider1", Password: redactedPlaceholder},
		},
		Indexers: []config.IndexerConfig{
			{Name: "nzb", APIKey: redactedPlaceholder},
		},
		MDBList: config.MDBListSettings{
			APIKey: redactedPlaceholder,
		},
		Trakt: config.TraktSettings{
			Accounts: []config.TraktAccount{
				{ID: "t1", ClientSecret: redactedPlaceholder, AccessToken: redactedPlaceholder, RefreshToken: redactedPlaceholder},
			},
		},
		Plex: config.PlexSettings{
			Accounts: []config.PlexAccount{
				{ID: "p1", AuthToken: redactedPlaceholder},
			},
		},
		Jellyfin: config.JellyfinSettings{
			Accounts: []config.JellyfinAccount{
				{ID: "j1", Token: redactedPlaceholder},
			},
		},
		Database: config.DatabaseSettings{
			URL: redactedPlaceholder,
		},
	}

	preserveRedactedFields(&incoming, &existing)

	// Verify real values are restored
	if incoming.Metadata.TVDBAPIKey != "real-tvdb-key" {
		t.Errorf("TVDBAPIKey not restored: got %q", incoming.Metadata.TVDBAPIKey)
	}
	if incoming.Metadata.TMDBAPIKey != "real-tmdb-key" {
		t.Errorf("TMDBAPIKey not restored: got %q", incoming.Metadata.TMDBAPIKey)
	}
	if incoming.Metadata.AIAPIKey != "real-ai-key" {
		t.Errorf("AIAPIKey not restored: got %q", incoming.Metadata.AIAPIKey)
	}
	if incoming.Playback.YouTubeProxyURL != "http://real-proxy:8888" {
		t.Errorf("YouTubeProxyURL not restored: got %q", incoming.Playback.YouTubeProxyURL)
	}
	if incoming.Usenet[0].Password != "real-password" {
		t.Errorf("Usenet password not restored: got %q", incoming.Usenet[0].Password)
	}
	if incoming.Indexers[0].APIKey != "real-indexer-key" {
		t.Errorf("Indexer APIKey not restored: got %q", incoming.Indexers[0].APIKey)
	}
	if incoming.MDBList.APIKey != "real-mdblist-key" {
		t.Errorf("MDBList APIKey not restored: got %q", incoming.MDBList.APIKey)
	}
	if incoming.Trakt.Accounts[0].ClientSecret != "real-trakt-secret" {
		t.Errorf("Trakt account ClientSecret not restored: got %q", incoming.Trakt.Accounts[0].ClientSecret)
	}
	if incoming.Trakt.Accounts[0].AccessToken != "real-trakt-access" {
		t.Errorf("Trakt account AccessToken not restored: got %q", incoming.Trakt.Accounts[0].AccessToken)
	}
	if incoming.Trakt.Accounts[0].RefreshToken != "real-trakt-refresh" {
		t.Errorf("Trakt account RefreshToken not restored: got %q", incoming.Trakt.Accounts[0].RefreshToken)
	}
	if incoming.Plex.Accounts[0].AuthToken != "real-plex-token" {
		t.Errorf("Plex account AuthToken not restored: got %q", incoming.Plex.Accounts[0].AuthToken)
	}
	if incoming.Jellyfin.Accounts[0].Token != "real-jellyfin-token" {
		t.Errorf("Jellyfin account Token not restored: got %q", incoming.Jellyfin.Accounts[0].Token)
	}
	if incoming.Database.URL != "postgres://user:secret@localhost:5432/db" {
		t.Errorf("Database URL not restored: got %q", incoming.Database.URL)
	}
}

func TestPreserveRedactedFields_AllowsRealUpdates(t *testing.T) {
	existing := config.Settings{
		Metadata: config.MetadataSettings{
			TVDBAPIKey: "old-key",
			TMDBAPIKey: "old-tmdb",
		},
	}

	// Master user provides a new real key (not the placeholder)
	incoming := config.Settings{
		Metadata: config.MetadataSettings{
			TVDBAPIKey: "brand-new-key",
			TMDBAPIKey: redactedPlaceholder, // unchanged
		},
	}

	preserveRedactedFields(&incoming, &existing)

	if incoming.Metadata.TVDBAPIKey != "brand-new-key" {
		t.Errorf("should accept new key, got %q", incoming.Metadata.TVDBAPIKey)
	}
	if incoming.Metadata.TMDBAPIKey != "old-tmdb" {
		t.Errorf("redacted field should be restored, got %q", incoming.Metadata.TMDBAPIKey)
	}
}

func TestRedactSettings_EmptyFieldsNotRedacted(t *testing.T) {
	s := config.Settings{
		Metadata: config.MetadataSettings{
			TVDBAPIKey: "",
			TMDBAPIKey: "has-a-key",
		},
	}

	redactSettings(&s)

	if s.Metadata.TVDBAPIKey != "" {
		t.Errorf("empty TVDBAPIKey should stay empty, got %q", s.Metadata.TVDBAPIKey)
	}
	if s.Metadata.TMDBAPIKey != "••••••••" {
		t.Errorf("non-empty TMDBAPIKey should be redacted, got %q", s.Metadata.TMDBAPIKey)
	}
}
