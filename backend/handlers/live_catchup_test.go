package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseM3UPlaylistCatchupAttributes(t *testing.T) {
	playlist := strings.Join([]string{
		`#EXTM3U catchup="shift" catchup-days="3"`,
		`#EXTINF:-1 tvg-id="a" catchup="default" catchup-days="7" catchup-source="http://cdn.example/a/{utc}-{duration}.m3u8",A`,
		`http://cdn.example/a/live.m3u8`,
		`#EXTINF:-1 tvg-id="b",B inherits the header`,
		`http://cdn.example/b/live.ts`,
		`#EXTINF:-1 tvg-id="c" catchup="vod",C has no archive`,
		`http://cdn.example/c/live.ts`,
		`#EXTINF:-1 tvg-id="d" catchup="flussonic",D is not Flussonic`,
		`http://cdn.example/d/live.ts`,
	}, "\n")
	channels := parseM3UPlaylist(playlist)
	if len(channels) != 4 {
		t.Fatalf("channels = %d, want 4", len(channels))
	}
	want := []*LiveCatchup{{Days: 7, Mode: "default"}, {Days: 3, Mode: "shift"}, nil, nil}
	for i, ch := range channels {
		got := ch.Catchup
		if (got == nil) != (want[i] == nil) || (got != nil && *got != *want[i]) {
			t.Errorf("%s catchup = %+v, want %+v", ch.Name, got, want[i])
		}
	}
	raw, _ := json.Marshal(channels[0])
	if strings.Contains(string(raw), "cdn.example/a/{utc}") {
		t.Fatalf("catch-up template leaked to clients: %s", raw)
	}
	if !strings.Contains(string(raw), `"catchup":{"days":7,"mode":"default"}`) {
		t.Fatalf("catchup flag missing: %s", raw)
	}
}

func TestM3UTimeshiftAttributeMeansShift(t *testing.T) {
	c, src := m3uCatchup(map[string]string{"timeshift": "2"}, nil, "http://h/x.ts")
	if c == nil || c.Mode != "shift" || c.Days != 2 || src == nil {
		t.Fatalf("got %+v", c)
	}
}

func TestDeriveCatchupTemplates(t *testing.T) {
	cases := map[string][2]string{
		"flussonic ts":  {"flussonic", "http://f.example/ch1/mpegts?token=x"},
		"flussonic hls": {"flussonic", "http://f.example/ch1/index.m3u8"},
		"xc":            {"xc", "http://x.example:8080/live/user/pass/123.m3u8"},
		"xc bare":       {"xc", "http://x.example/user/pass/123"},
	}
	want := map[string]string{
		"flussonic ts":  "http://f.example/ch1/timeshift_abs-{utc}.ts?token=x",
		"flussonic hls": "http://f.example/ch1/index-{utc}-{duration}.m3u8",
		"xc":            "http://x.example:8080/timeshift/user/pass/{duration:60}/{Y}-{m}-{d}:{H}-{M}/123.m3u8",
		"xc bare":       "http://x.example/timeshift/user/pass/{duration:60}/{Y}-{m}-{d}:{H}-{M}/123.ts",
	}
	for name, c := range cases {
		got, err := deriveCatchupTemplate(c[0], c[1])
		if err != nil || got != want[name] {
			t.Errorf("%s: got %q, %v; want %q", name, got, err, want[name])
		}
	}
}

func TestExpandCatchupTemplate(t *testing.T) {
	start := time.Date(2026, 11, 1, 20, 5, 0, 0, time.UTC)
	now := start.Add(3 * time.Hour)
	berlin, _ := time.LoadLocation("Europe/Berlin")
	got := expandCatchupTemplate(
		"u={utc}&e={utcend}&n={lutc}&d={duration}&m={duration:60}&o={offset:60}&s=${start}&t={Y}-{m}-{d}:{H}-{M}&f={utc:Y/m/d H.M.S}",
		start, 90*time.Minute, now, berlin)
	want := "u=1793563500&e=1793568900&n=1793574300&d=5400&m=90&o=180&s=1793563500&t=2026-11-01:21-05&f=2026/11/01 21.05.00"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if kept := expandCatchupTemplate("{unknown}", start, time.Hour, now, nil); kept != "{unknown}" {
		t.Fatalf("unknown token = %q", kept)
	}
}

func TestXtreamLocationFromServerInfo(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	loc, err := xtreamLocationFromServerInfo("Europe/Amsterdam", "", "", now)
	if err != nil || loc.String() != "Europe/Amsterdam" {
		t.Fatalf("zone: %v %v", loc, err)
	}
	// Unknown zone name: derive the offset from the panel's wall clock.
	loc, err = xtreamLocationFromServerInfo("Mars/Base", "2026-11-02 17:29:40", "1793620800", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, offset := now.In(loc).Zone(); offset != 5*3600+30*60 {
		t.Fatalf("offset = %d", offset)
	}
	if _, err := xtreamLocationFromServerInfo("", "", "", now); err == nil {
		t.Fatal("want error with no clock")
	}
}

func TestBuildCatchupURLXtream(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	stream := XtreamStream{StreamID: 42, TVArchive: "1", TVArchiveDuration: "3"}
	catchup, source := xtreamCatchup(stream, "http://panel.example:8080/", "us er", "pw", "")
	if catchup == nil || catchup.Days != 3 {
		t.Fatalf("catchup = %+v", catchup)
	}
	berlin, _ := time.LoadLocation("Europe/Berlin")
	xtreamClockMu.Lock()
	xtreamClockCache["http://panel.example:8080|us er"] = xtreamClockEntry{loc: berlin, expiresAt: now.Add(100 * 365 * 24 * time.Hour)}
	xtreamClockMu.Unlock()

	h := &LiveHandler{}
	channel := LiveChannel{ID: "42", Catchup: catchup, catchupSource: source}
	start := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	resp, err := h.buildCatchupURL(context.Background(), channel, start, start.Add(time.Hour), 95*time.Second, now)
	if err != nil {
		t.Fatal(err)
	}
	// 95 s in rounds down to the minute; 60 min + 2 min padding - 1 min = 61 min.
	want := "http://panel.example:8080/timeshift/us%20er/pw/61/2026-11-02:10-01/42.ts"
	if resp.URL != want || resp.OffsetSeconds != 60 || resp.DurationSeconds != 61*60 {
		t.Fatalf("got %+v\nwant %s", resp, want)
	}

	if _, err := h.buildCatchupURL(context.Background(), channel, now.Add(time.Minute), now.Add(time.Hour), 0, now); err == nil {
		t.Fatal("future programme should fail")
	}
	old := now.Add(-4 * 24 * time.Hour)
	if _, err := h.buildCatchupURL(context.Background(), channel, old, old.Add(time.Hour), 0, now); err == nil {
		t.Fatal("programme past the archive window should fail")
	}
	if _, err := h.buildCatchupURL(context.Background(), LiveChannel{}, start, start.Add(time.Hour), 0, now); err != errCatchupUnavailable {
		t.Fatalf("no catch-up: %v", err)
	}
}

func TestBuildCatchupURLOnAirProgrammeEndsNow(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	catchup, source := m3uCatchup(map[string]string{"catchup": "shift", "catchup-days": "1"}, nil, "http://h/x.ts")
	channel := LiveChannel{URL: "http://h/x.ts", Catchup: catchup, catchupSource: source}
	start := now.Add(-20 * time.Minute)
	resp, err := (&LiveHandler{}).buildCatchupURL(context.Background(), channel, start, now.Add(40*time.Minute), 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if resp.DurationSeconds != 20*60 || resp.URL != "http://h/x.ts?utc=1793619600&lutc=1793620800" {
		t.Fatalf("got %+v", resp)
	}
}
