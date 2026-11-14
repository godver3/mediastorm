package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/auth"
	"novastream/internal/livecapacity"
)

func TestStationIdentityMatchesQualitiesButSeparatesBroadcasts(t *testing.T) {
	base := LiveChannel{ID: "a", Name: "RTL HD", TvgID: "RTL.de"}
	key, _ := liveStationIdentity(base, nil)
	for _, channel := range []LiveChannel{
		{ID: "b", Name: "DE | RTL FHD", TvgID: "RTLHD.de"},
		{ID: "c", Name: "RTL Television SD", TvgID: "RTL.de"},
		{ID: "d", Name: "RTL 1080p", Group: "GERMANY NEW", TvgLanguage: "German"},
	} {
		if actual, _ := liveStationIdentity(channel, nil); actual != key {
			t.Errorf("not grouped: %q", channel.Name)
		}
	}
	for _, channel := range []LiveChannel{
		{ID: "b", Name: "RTLZWEI HD", TvgID: "RTLZWEI.de"},
		{ID: "c", Name: "RTLup HD", TvgID: "RTLup.de"},
		{ID: "d", Name: "RTL HD AT", TvgID: "RTL.at"},
		{ID: "e", Name: "RTL HD CH", TvgID: "RTL.ch"},
		{ID: "f", Name: "RTL +1 HD", TvgID: "RTLplus1.de"},
		{ID: "g", Name: "RTL HD", TvgID: "RTL.de", TvgLanguage: "English"},
		{ID: "h", Name: "RTL HD", TvgID: "different-programme.de"},
		{ID: "i", Name: "RTL HD AT", TvgID: "RTL.de"},
	} {
		if actual, _ := liveStationIdentity(channel, nil); actual == key {
			t.Errorf("different broadcast grouped: %+v", channel)
		}
	}
}

func TestStationMappingAndUnknownNamesRemainConservative(t *testing.T) {
	a := LiveChannel{ID: "a:1", SourceID: "a", Name: "Station HD"}
	b := LiveChannel{ID: "b:1", SourceID: "b", Name: "Station SD"}
	ka, _ := liveStationIdentity(a, nil)
	kb, _ := liveStationIdentity(b, nil)
	if ka == kb {
		t.Fatal("unverified names were merged")
	}
	mappings := []config.LiveStationMapping{
		{SourceID: "a", ChannelID: "a:1", StationID: "confirmed-station", Name: "Confirmed Station"},
		{SourceID: "b", ChannelID: "b:1", StationID: "confirmed-station", Name: "Confirmed Station"},
	}
	ka, _ = liveStationIdentity(a, mappings)
	kb, _ = liveStationIdentity(b, mappings)
	if ka != kb {
		t.Fatal("explicit mapping was ignored")
	}
	b.SourceID = "foreign"
	if other, _ := liveStationIdentity(b, mappings); other == ka {
		t.Fatal("mapping matched a different source")
	}
}

func TestStationGroupsAreContiguousAndPreserveEntryFavorites(t *testing.T) {
	channels := annotateLiveStations([]LiveChannel{
		{ID: "a:rtl", Name: "RTL HD", TvgID: "RTL.de", SourceID: "a"},
		{ID: "a:zdf", Name: "ZDF HD", TvgID: "ZDF.de", SourceID: "a"},
		{ID: "b:rtl", Name: "RTL Television FHD", TvgID: "RTLHD.de", SourceID: "b"},
		{ID: "b:zdf", Name: "ZDF SD", TvgID: "ZDF.de", SourceID: "b"},
	}, nil)
	result := orderLiveStations(channels, map[string]struct{}{"b:zdf": {}})
	ids := []string{}
	for _, c := range result {
		ids = append(ids, c.ID)
		if c.StationVariants != 2 {
			t.Fatal("lost variant count")
		}
	}
	if !slices.Equal(ids, []string{"a:zdf", "b:zdf", "a:rtl", "b:rtl"}) {
		t.Fatalf("order=%v", ids)
	}
	if channels[2].StationName != channels[0].StationName {
		t.Fatal("one group has inconsistent labels")
	}
}

func TestStationGroupedCatalogPaginationAndDefaultOff(t *testing.T) {
	var provider *httptest.Server
	provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "#EXTM3U\n#EXTINF:-1 tvg-id=\"RTL.de\" tvg-country=\"DE\" group-title=\"Germany\",RTL HD\n%s/%s-rtl.ts\n#EXTINF:-1 tvg-id=\"ZDF.de\" group-title=\"Germany\",ZDF HD\n%s/%s-zdf.ts\n", provider.URL, r.URL.Path, provider.URL, r.URL.Path)
	}))
	defer provider.Close()
	cfg := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	settings := config.Settings{Live: config.LiveSettings{Sources: []config.LivePlaylistSource{{ID: "a", Mode: "m3u", PlaylistURL: provider.URL + "/a"}, {ID: "b", Mode: "m3u", PlaylistURL: provider.URL + "/b"}}}}
	if err := cfg.Save(settings); err != nil {
		t.Fatal(err)
	}
	h := NewLiveHandler(provider.Client(), false, "", 24, 0, 0, false, cfg, nil)
	read := func(group *bool, offset, limit int) LiveChannelsResponse {
		payload := liveChannelsRequest{GroupByStation: group, Offset: &offset, Limit: &limit}
		body, _ := json.Marshal(payload)
		w := httptest.NewRecorder()
		h.GetChannels(w, httptest.NewRequest("POST", "/api/live/channels", bytes.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("catalog %d %s", w.Code, w.Body.String())
		}
		var page LiveChannelsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	normal := read(nil, 0, 4)
	if normal.Channels[0].StationID == normal.Channels[1].StationID {
		t.Fatal("default changed playlist order")
	}
	on := true
	first := read(&on, 0, 2)
	second := read(&on, 2, 2)
	if first.Total != 4 || !first.HasMore || second.HasMore {
		t.Fatal("bad grouped pagination")
	}
	if first.Channels[0].StationID != first.Channels[1].StationID || first.Channels[0].StationVariants != 2 {
		t.Fatal("RTL variants not together before pagination")
	}
	if first.Channels[0].TvgCountry != "DE" {
		t.Fatal("M3U country metadata lost")
	}
	if first.Channels[0].StationID == second.Channels[0].StationID {
		t.Fatal("different station groups merged")
	}
}

func TestStationFallbackRespectsQualityCap(t *testing.T) {
	for _, tc := range []struct {
		candidate, cap string
		allow          bool
	}{
		{"RTL SD", "1080p", true}, {"RTL UHD", "1080p", false},
		{"RTL FHD", "2160p", true}, {"RTL unknown", "1080p", false},
		{"RTL UHD", "", true},
	} {
		if got := stationFallbackQualityAllowed(tc.candidate, tc.cap); got != tc.allow {
			t.Errorf("%+v got %v", tc, got)
		}
	}
}

func TestStationFailoverReleasesOnlyOwnSharedReservation(t *testing.T) {
	bucket := t.Name()
	viewer, _ := livecapacity.Default.Acquire(bucket, "shared", 1)
	defer viewer.Release()
	current, _ := livecapacity.Default.Acquire(bucket, "shared", 1)
	session := &HLSSession{liveCapacityLease: current}
	plan := &liveStationFailover{candidates: []LiveChannel{{ID: "a"}, {ID: "b"}}, attempts: 1, prepare: func(context.Context, LiveChannel) (liveStationCandidate, error) {
		return liveStationCandidate{channel: LiveChannel{ID: "b", URL: "https://example.test/b.ts"}, target: liveStreamTarget{Provider: "m3u", BucketKey: bucket, MaxStreams: 1}}, nil
	}}
	if err := plan.next(context.Background(), session); !errors.Is(err, livecapacity.ErrLimit) {
		t.Fatalf("expected capacity conflict, got %v", err)
	}
	if livecapacity.Default.Current(bucket) != 1 {
		t.Fatal("other viewer's connection was released")
	}
	viewer.Release()
}

func TestStationFailoverCandidateBudgetAndCancellation(t *testing.T) {
	var prepared int
	plan := &liveStationFailover{candidates: make([]LiveChannel, 20), attempts: 1, prepare: func(context.Context, LiveChannel) (liveStationCandidate, error) {
		prepared++
		return liveStationCandidate{}, errors.New("revoked")
	}}
	if err := plan.next(context.Background(), &HLSSession{}); !errors.Is(err, errLiveStationExhausted) || prepared != liveStationMaxAttempts-1 {
		t.Fatalf("unbounded plan: %d %v", prepared, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	prepared = 0
	plan.index = 0
	plan.attempts = 1
	if err := plan.next(ctx, &HLSSession{}); !errors.Is(err, context.Canceled) || prepared != 0 {
		t.Fatal("stop did not cancel failover")
	}
}

func TestStationPlanRechecksAccountAssignmentAndNeverAcceptsSubmittedURLs(t *testing.T) {
	cfg := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	accounts := []string{"household"}
	settings := config.Settings{Live: config.LiveSettings{AutoChannelFailover: true, RequireAccountAssignment: true, Sources: []config.LivePlaylistSource{{ID: "a", Mode: "m3u", PlaylistURL: "https://provider.example/a.m3u", AllowedAccounts: &accounts}, {ID: "b", Mode: "m3u", PlaylistURL: "https://provider.example/b.m3u", AllowedAccounts: &accounts}}}}
	if err := cfg.Save(settings); err != nil {
		t.Fatal(err)
	}
	h := NewVideoHandler(false, "", "")
	h.SetConfigManager(cfg)
	h.SetUsersService(liveAccountUsers{"adult": {ID: "adult", AccountID: "household"}})
	h.liveChannels = staticLiveChannelProvider{channels: []LiveChannel{{ID: "a:rtl", Name: "RTL HD", TvgID: "RTL.de", SourceID: "a", URL: "https://provider.example/a.ts"}, {ID: "b:rtl", Name: "RTL FHD", TvgID: "RTL.de", SourceID: "b", URL: "https://provider.example/b.ts"}}}
	r := httptest.NewRequest("GET", "/api/live/hls/start?profileId=adult&sourceId=a&fallbackUrl=https://attacker.example/evil.ts", nil)
	r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, "household"))
	plan := h.liveStationPlan(r, "https://provider.example/a.ts")
	if plan == nil || len(plan.candidates) != 2 {
		t.Fatal("permitted catalog candidates missing")
	}
	for _, c := range plan.candidates {
		if strings.Contains(c.URL, "attacker") {
			t.Fatal("client injected a fallback")
		}
	}
	settings.Live.Sources[1].AllowedAccounts = &[]string{}
	if err := cfg.Save(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := plan.prepare(context.Background(), plan.candidates[1]); err == nil {
		t.Fatal("revoked account access survived candidate snapshot")
	}
}

func TestStationFailoverPlaysSecondRealSourceWithoutOverbooking(t *testing.T) {
	for _, mode := range []string{"offline", "silent"} {
		t.Run(mode, func(t *testing.T) { runStationFailoverFixture(t, mode == "silent") })
	}
}

func runStationFailoverFixture(t *testing.T, silent bool) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	media := filepath.Join(t.TempDir(), "fixture.ts")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=320x180:r=25", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "6", "-c:v", "libx264", "-preset", "ultrafast", "-g", "25", "-c:a", "aac", "-f", "mpegts", media).CombinedOutput(); err != nil {
		t.Fatalf("media fixture: %v %s", err, out)
	}
	data, err := os.ReadFile(media)
	if err != nil {
		t.Fatal(err)
	}
	var open, maxOpen, failed, working atomic.Int32
	closed := make(chan struct{}, 4)
	var provider *httptest.Server
	provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/get.php" {
			source := r.URL.Query().Get("list")
			name := "RTL HD"
			if source == "b" {
				name = "RTL FHD"
			}
			fmt.Fprintf(w, "#EXTM3U\n#EXTINF:-1 tvg-id=\"RTL.de\" tvg-country=\"DE\" group-title=\"Germany\",%s\n%s/%s.ts\n", name, provider.URL, source)
			return
		}
		n := open.Add(1)
		for {
			old := maxOpen.Load()
			if old >= n || maxOpen.CompareAndSwap(old, n) {
				break
			}
		}
		defer open.Add(-1)
		if r.URL.Path == "/a.ts" {
			failed.Add(1)
			if silent {
				w.Header().Set("Content-Type", "video/mp2t")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				return
			}
			http.Error(w, "source offline", 503)
			return
		}
		working.Add(1)
		defer func() { closed <- struct{}{} }()
		w.Header().Set("Content-Type", "video/mp2t")
		for {
			for pos := 0; pos < len(data); pos += 188 * 50 {
				end := min(pos+188*50, len(data))
				if _, err := w.Write(data[pos:end]); err != nil {
					return
				}
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					return
				case <-time.After(25 * time.Millisecond):
				}
			}
		}
	}))
	defer provider.Close()
	cfg := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	settings := config.Settings{Live: config.LiveSettings{AutoChannelFailover: true, GroupChannelsByStation: true, Sources: []config.LivePlaylistSource{
		{ID: "a", Name: "German list A", Mode: "m3u", PlaylistURL: provider.URL + "/get.php?username=fixture&password=fixture&list=a", MaxStreams: 1, StreamFormat: "direct"},
		{ID: "b", Name: "German list B", Mode: "m3u", PlaylistURL: provider.URL + "/get.php?username=fixture&password=fixture&list=b", MaxStreams: 1, StreamFormat: "direct"},
	}}}
	if err := cfg.Save(settings); err != nil {
		t.Fatal(err)
	}
	live := NewLiveHandler(provider.Client(), true, ffmpeg, 24, 0, 0, false, cfg, nil)
	h := NewVideoHandlerWithProvider(true, ffmpeg, "", t.TempDir(), nil)
	h.SetConfigManager(cfg)
	h.SetLiveChannelProvider(live)
	t.Cleanup(h.hlsManager.Shutdown)
	r := httptest.NewRequest("GET", "/api/live/hls/start?sourceId=a&target=native&url="+url.QueryEscape(provider.URL+"/a.ts"), nil)
	w := httptest.NewRecorder()
	h.StartLiveHLSSession(w, r)
	if w.Code != 200 {
		t.Fatalf("start %d %s", w.Code, w.Body.String())
	}
	var response struct {
		SessionID string `json:"sessionId"`
		IsDirect  bool   `json:"isDirect"`
	}
	json.Unmarshal(w.Body.Bytes(), &response)
	if response.SessionID == "" || response.IsDirect {
		t.Fatal("automatic fallback did not choose managed HLS")
	}
	session, ok := h.hlsManager.GetSession(response.SessionID)
	if !ok {
		t.Fatal("session missing")
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		session.mu.RLock()
		source := session.LiveSourceID
		fatal := session.FatalError
		session.mu.RUnlock()
		if source == "b" && nextLiveSegmentNumber(session.OutputDir) > 0 {
			break
		}
		if fatal != "" || time.Now().After(deadline) {
			t.Fatalf("fallback did not produce video: source=%q fatal=%q", source, fatal)
		}
		time.Sleep(20 * time.Millisecond)
	}
	segment := filepath.Join(session.OutputDir, "segment0.ts")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-i", segment, "-frames:v", "1", "-f", "null", "-").CombinedOutput(); err != nil {
		t.Fatalf("fallback video decode: %v %s", err, out)
	}
	if failed.Load() != 1 || working.Load() != 1 || maxOpen.Load() != 1 {
		t.Fatalf("provider connections failed=%d working=%d peak=%d", failed.Load(), working.Load(), maxOpen.Load())
	}
	session.mu.RLock()
	bucket := session.LiveBucket
	session.mu.RUnlock()
	if livecapacity.Default.Current(bucket) != 1 {
		t.Fatal("fallback input was not accounted")
	}
	h.hlsManager.CleanupSession(session.ID)
	waitCapacityReleased(t, bucket)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("fallback connection survived stop")
	}
}
