package epg

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"novastream/config"
)

func TestRefreshFetchesSourceLevelUniversalEPG(t *testing.T) {
	now := time.Now().UTC()
	start := now.Add(-30 * time.Minute).Format("20060102150405 -0700")
	stop := now.Add(30 * time.Minute).Format("20060102150405 -0700")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<tv>
  <channel id="source.channel"><display-name>Source Channel</display-name></channel>
  <programme start="%s" stop="%s" channel="source.channel"><title>Source Program</title></programme>
</tv>`, start, stop)
	}))
	defer server.Close()

	enabled := true
	settings := config.DefaultSettings()
	settings.Cache.Directory = t.TempDir()
	settings.Live.EPG.Enabled = false
	settings.Live.EPG.XmltvUrl = ""
	settings.Live.EPG.Sources = nil
	settings.Live.Sources = []config.LivePlaylistSource{
		{
			ID:      "source-1",
			Name:    "Source 1",
			Enabled: &enabled,
			EPG: config.EPGSettings{
				Enabled:  true,
				XmltvUrl: server.URL,
			},
		},
	}

	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := manager.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	service := NewService(settings.Cache.Directory, manager)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	status := service.GetStatus()
	if status.ChannelCount != 1 {
		t.Fatalf("ChannelCount = %d, want 1", status.ChannelCount)
	}
	if status.ProgramCount != 1 {
		t.Fatalf("ProgramCount = %d, want 1", status.ProgramCount)
	}
	if status.SourceCount != 1 {
		t.Fatalf("SourceCount = %d, want 1", status.SourceCount)
	}

	nowPlaying := service.GetNowPlaying([]string{"source.channel"})
	if len(nowPlaying) != 1 || nowPlaying[0].Current == nil {
		t.Fatalf("expected current programme for source.channel, got %+v", nowPlaying)
	}
	if nowPlaying[0].Current.Title != "Source Program" {
		t.Fatalf("Current.Title = %q, want Source Program", nowPlaying[0].Current.Title)
	}
}

func TestRefreshDoesNotFetchGlobalXtreamWhenConfiguredSourceIsM3U(t *testing.T) {
	now := time.Now().UTC()
	start := now.Add(-30 * time.Minute).Format("20060102150405 -0700")
	stop := now.Add(30 * time.Minute).Format("20060102150405 -0700")

	m3uEPGServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<tv>
  <channel id="m3u.channel"><display-name>M3U Channel</display-name></channel>
  <programme start="%s" stop="%s" channel="m3u.channel"><title>M3U Program</title></programme>
</tv>`, start, stop)
	}))
	defer m3uEPGServer.Close()

	xtreamHit := false
	xtreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		xtreamHit = true
		http.Error(w, "xtream should not be used", http.StatusInternalServerError)
	}))
	defer xtreamServer.Close()

	enabled := true
	settings := config.DefaultSettings()
	settings.Cache.Directory = t.TempDir()
	settings.Live.Mode = "xtream"
	settings.Live.XtreamHost = xtreamServer.URL
	settings.Live.XtreamUsername = "user"
	settings.Live.XtreamPassword = "pass"
	settings.Live.EPG.Enabled = true
	settings.Live.EPG.XmltvUrl = ""
	settings.Live.EPG.Sources = nil
	settings.Live.Sources = []config.LivePlaylistSource{
		{
			ID:          "m3u-source",
			Name:        "M3U Source",
			Mode:        "m3u",
			Enabled:     &enabled,
			PlaylistURL: "http://example.com/live.m3u",
			EPG: config.EPGSettings{
				Enabled:  true,
				XmltvUrl: m3uEPGServer.URL,
			},
		},
	}

	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := manager.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	service := NewService(settings.Cache.Directory, manager)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if xtreamHit {
		t.Fatal("expected refresh not to call global Xtream when configured live source is M3U")
	}

	nowPlaying := service.GetNowPlaying([]string{"m3u.channel"})
	if len(nowPlaying) != 1 || nowPlaying[0].Current == nil || nowPlaying[0].Current.Title != "M3U Program" {
		t.Fatalf("expected M3U EPG now playing, got %+v", nowPlaying)
	}
}
