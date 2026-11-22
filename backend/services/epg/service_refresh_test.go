package epg

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
)

func TestRefreshInfersXtreamEPGURLWhenXMLTVURLIsEmpty(t *testing.T) {
	now := time.Now().UTC()
	start := now.Add(-30 * time.Minute).Format("20060102150405 -0700")
	stop := now.Add(30 * time.Minute).Format("20060102150405 -0700")

	var xmltvRequests atomic.Int32
	var xmltvUserAgents []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/player_api.php" {
			if r.Header.Get("User-Agent") != xtreamBrowserUserAgent {
				http.Error(w, "unsupported client", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `[]`)
			return
		}
		if r.URL.Path != "/xmltv.php" {
			http.NotFound(w, r)
			return
		}
		xmltvUserAgents = append(xmltvUserAgents, r.Header.Get("User-Agent"))
		if r.Header.Get("User-Agent") != xtreamBrowserUserAgent {
			http.Error(w, "unsupported client", http.StatusForbidden)
			return
		}
		xmltvRequests.Add(1)
		if got := r.URL.Query().Get("username"); got != "user name" {
			t.Errorf("username = %q, want %q", got, "user name")
		}
		if got := r.URL.Query().Get("password"); got != "p@ss&word" {
			t.Errorf("password = %q, want %q", got, "p@ss&word")
		}
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<tv>
  <channel id="xtream.channel"><display-name>Xtream Channel</display-name></channel>
  <programme start="%s" stop="%s" channel="xtream.channel"><title>Xtream Program</title></programme>
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
			ID:             "xtream-source",
			Name:           "Xtream Source",
			Mode:           "xtream",
			Enabled:        &enabled,
			XtreamHost:     server.URL + "/",
			XtreamUsername: "user name",
			XtreamPassword: "p@ss&word",
			EPG: config.EPGSettings{
				Enabled:  true,
				XmltvUrl: "",
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

	if got := xmltvRequests.Load(); got != 1 {
		t.Fatalf("xmltv requests = %d, want 1", got)
	}
	if len(xmltvUserAgents) != 2 || xmltvUserAgents[0] != xtreamStreamUserAgent || xmltvUserAgents[1] != xtreamBrowserUserAgent {
		t.Fatalf("XMLTV User-Agent attempts = %q, want VLC then browser", xmltvUserAgents)
	}
	status := service.GetStatus()
	if status.ChannelCount != 1 || status.ProgramCount != 1 {
		t.Fatalf("status = %+v, want one channel and one program", status)
	}
}

func TestRefreshRetainsExistingScheduleWhenXtreamSourcesFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	enabled := true
	settings := config.DefaultSettings()
	settings.Cache.Directory = t.TempDir()
	settings.Live.EPG.Enabled = false
	settings.Live.Sources = []config.LivePlaylistSource{
		{
			ID:             "xtream-source",
			Name:           "Xtream Source",
			Mode:           "xtream",
			Enabled:        &enabled,
			XtreamHost:     server.URL,
			XtreamUsername: "user",
			XtreamPassword: "pass",
			EPG:            config.EPGSettings{Enabled: true},
		},
	}

	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := manager.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	now := time.Now().UTC()
	service := NewService(settings.Cache.Directory, manager)
	service.schedule = &models.EPGSchedule{
		Channels: map[string]models.EPGChannel{
			"existing.channel": {ID: "existing.channel", Name: "Existing Channel"},
		},
		Programs: map[string][]models.EPGProgram{
			"existing.channel": {
				{ChannelID: "existing.channel", Title: "Existing Program", Start: now.Add(-time.Hour), Stop: now.Add(time.Hour)},
			},
		},
		LastUpdated: now.Add(-time.Hour),
	}

	err := service.Refresh(context.Background())
	if err == nil {
		t.Fatal("refresh succeeded, want provider failure")
	}
	status := service.GetStatus()
	if status.ChannelCount != 1 || status.ProgramCount != 1 {
		t.Fatalf("status after failed refresh = %+v, want existing schedule retained", status)
	}
	if status.LastError == "" {
		t.Fatal("LastError is empty after failed refresh")
	}
	nowPlaying := service.GetNowPlaying([]string{"existing.channel"})
	if len(nowPlaying) != 1 || nowPlaying[0].Current == nil || nowPlaying[0].Current.Title != "Existing Program" {
		t.Fatalf("existing guide was not retained: %+v", nowPlaying)
	}
}

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

// Two guides covering the same channel (a global guide and a provider's
// own, a minute apart with another title spelling) used to list each
// programme twice. The earlier source in priority order owns the channel's
// time; a later one only fills what is missing, and keeps its own channels.
func TestRefreshMergesOverlappingGuides(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Hour)
	at := func(min int) string { return base.Add(time.Duration(min) * time.Minute).Format("20060102150405 -0700") }
	serve := func(body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><tv>`+body+`</tv>`)
		}))
	}
	global := serve(fmt.Sprintf(`
  <channel id="dmax.de"><display-name>DMAX</display-name></channel>
  <channel id="only.global"><display-name>Global only</display-name></channel>
  <programme start="%s" stop="%s" channel="dmax.de"><title>Fang des Lebens – Alaska</title></programme>
  <programme start="%s" stop="%s" channel="dmax.de"><title>Fang des Lebens – Alaska</title></programme>
  <programme start="%s" stop="%s" channel="only.global"><title>Global Show</title></programme>`,
		at(0), at(60), at(60), at(120), at(0), at(60)))
	defer global.Close()
	provider := serve(fmt.Sprintf(`
  <channel id="DMAX.de"><display-name>DMAX</display-name></channel>
  <channel id="only.provider"><display-name>Provider only</display-name></channel>
  <programme start="%s" stop="%s" channel="DMAX.de"><title>Fang des Lebens - Alaska</title></programme>
  <programme start="%s" stop="%s" channel="DMAX.de"><title>Fang des Lebens - Alaska</title></programme>
  <programme start="%s" stop="%s" channel="DMAX.de"><title>Steel Buddies</title></programme>
  <programme start="%s" stop="%s" channel="only.provider"><title>Provider Show</title></programme>`,
		at(1), at(61), at(61), at(121), at(180), at(240), at(0), at(60)))
	defer provider.Close()

	enabled := true
	settings := config.DefaultSettings()
	settings.Cache.Directory = t.TempDir()
	settings.Live.EPG.Enabled = true
	settings.Live.EPG.XmltvUrl = global.URL
	settings.Live.EPG.Sources = nil
	settings.Live.Sources = []config.LivePlaylistSource{{
		ID: "provider", Name: "Provider", Enabled: &enabled,
		EPG: config.EPGSettings{Enabled: true, XmltvUrl: provider.URL},
	}}
	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := manager.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	service := NewService(settings.Cache.Directory, manager)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	progs := service.GetScheduleMultiple([]string{"dmax.de"}, base.Add(-time.Hour), base.Add(5*time.Hour))["dmax.de"]
	if len(progs) != 3 {
		t.Fatalf("dmax.de programmes = %d, want 3 (two from the global guide, one gap from the provider): %+v", len(progs), progs)
	}
	want := []struct {
		title string
		start int
	}{{"Fang des Lebens – Alaska", 0}, {"Fang des Lebens – Alaska", 60}, {"Steel Buddies", 180}}
	for i, w := range want {
		if progs[i].Title != w.title || !progs[i].Start.Equal(base.Add(time.Duration(w.start)*time.Minute)) {
			t.Fatalf("programme %d = %q at %v, want %q at +%dm", i, progs[i].Title, progs[i].Start, w.title, w.start)
		}
	}
	for _, id := range []string{"only.global", "only.provider"} {
		if got := service.GetScheduleMultiple([]string{id}, base.Add(-time.Hour), base.Add(5*time.Hour))[id]; len(got) != 1 {
			t.Fatalf("%s programmes = %d, want 1", id, len(got))
		}
	}
}

func TestMergeGuideFillsGapsOnly(t *testing.T) {
	at := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	m := func(min int) time.Time { return at.Add(time.Duration(min) * time.Minute) }
	dst := newGuidePart()
	mergeGuide(dst, &models.EPGSchedule{Channels: map[string]models.EPGChannel{"c": {ID: "c", Name: "First"}}, Programs: map[string][]models.EPGProgram{
		"c": {{Title: "A", Start: m(0), Stop: m(30)}, {Title: "B", Start: m(30), Stop: m(60)}},
	}})
	added, covered := mergeGuide(dst, &models.EPGSchedule{Channels: map[string]models.EPGChannel{"c": {ID: "c", Name: "Second"}}, Programs: map[string][]models.EPGProgram{
		"c": {{Title: "a", Start: m(-1), Stop: m(28)}, {Title: "b", Start: m(28), Stop: m(58)}, {Title: "gap", Start: m(60), Stop: m(90)}},
	}})
	got := dst.Programs["c"]
	if added != 1 || covered != 2 || len(got) != 3 || got[0].Title != "A" || got[1].Title != "B" || got[2].Title != "gap" {
		t.Fatalf("added %d covered %d, programmes %+v", added, covered, got)
	}
	if dst.Channels["c"].Name != "Second" {
		t.Fatalf("channel metadata = %+v, want the later source's (unchanged behaviour)", dst.Channels["c"])
	}
}
