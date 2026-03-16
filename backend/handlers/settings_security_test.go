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
			GeminiAPIKey: "gemini-key",
		},
		MDBList: config.MDBListSettings{
			APIKey: "mdblist-key",
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
	if s.Metadata.GeminiAPIKey != redacted {
		t.Errorf("GeminiAPIKey not redacted: %q", s.Metadata.GeminiAPIKey)
	}
	if s.MDBList.APIKey != redacted {
		t.Errorf("MDBList APIKey not redacted: %q", s.MDBList.APIKey)
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
}

func TestPreserveRedactedFields_RestoresRealCredentials(t *testing.T) {
	existing := config.Settings{
		Metadata: config.MetadataSettings{
			TVDBAPIKey:   "real-tvdb-key",
			TMDBAPIKey:   "real-tmdb-key",
			GeminiAPIKey: "real-gemini-key",
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
	}

	// Simulate a non-master user saving back redacted settings
	incoming := config.Settings{
		Metadata: config.MetadataSettings{
			TVDBAPIKey:   redactedPlaceholder,
			TMDBAPIKey:   redactedPlaceholder,
			GeminiAPIKey: redactedPlaceholder,
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
	}

	preserveRedactedFields(&incoming, &existing)

	// Verify real values are restored
	if incoming.Metadata.TVDBAPIKey != "real-tvdb-key" {
		t.Errorf("TVDBAPIKey not restored: got %q", incoming.Metadata.TVDBAPIKey)
	}
	if incoming.Metadata.TMDBAPIKey != "real-tmdb-key" {
		t.Errorf("TMDBAPIKey not restored: got %q", incoming.Metadata.TMDBAPIKey)
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
