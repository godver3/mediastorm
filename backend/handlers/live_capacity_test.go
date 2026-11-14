package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"novastream/config"
	"novastream/internal/livecapacity"
	"novastream/models"
)

func waitCapacityReleased(t *testing.T, bucket string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for livecapacity.Default.Current(bucket) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("provider slot not released after input stopped")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestLiveCapacityConcurrentHLSAdmission(t *testing.T) {
	for _, limit := range []int{1, 2} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			_, provider := newLiveTSServer(t, 0)
			m := NewHLSManager(t.TempDir(), stubBinary(t, "true"), "", nil)
			bucket := t.Name()
			t.Cleanup(func() { m.Shutdown(); waitCapacityReleased(t, bucket) })
			start := make(chan struct{})
			var admitted atomic.Int32
			var wg sync.WaitGroup
			for i := range 32 {
				wg.Go(func() {
					<-start
					_, err := m.CreateLiveSession(context.Background(), fmt.Sprintf("%s/%d.ts", provider.URL, i), "m3u", bucket, "", "", "", "native", LiveTuningSettings{MaxStreams: limit}, false)
					if err == nil {
						admitted.Add(1)
					} else if !errors.Is(err, livecapacity.ErrLimit) {
						t.Error(err)
					}
				})
			}
			close(start)
			wg.Wait()
			if got := int(admitted.Load()); got != limit {
				t.Fatalf("admitted %d inputs for limit %d", got, limit)
			}
		})
	}
}

func TestLiveCapacitySameChannelSharesLastSlot(t *testing.T) {
	_, provider := newLiveTSServer(t, 0)
	m := NewHLSManager(t.TempDir(), stubBinary(t, "true"), "", nil)
	bucket := t.Name()
	t.Cleanup(func() { m.Shutdown(); waitCapacityReleased(t, bucket) })
	create := func(path string) (*HLSSession, error) {
		return m.CreateLiveSession(context.Background(), provider.URL+path, "m3u", bucket, "", "", "", "native", LiveTuningSettings{MaxStreams: 1}, false)
	}
	a, err := create("/same.ts")
	if err != nil {
		t.Fatal(err)
	}
	_, err = create("/same.ts")
	if err != nil {
		t.Fatalf("shared viewer rejected at provider limit: %v", err)
	}
	if livecapacity.Default.Current(bucket) != 1 {
		t.Fatal("shared input used more than one slot")
	}
	m.CleanupSession(a.ID)
	if _, err := create("/other.ts"); !errors.Is(err, livecapacity.ErrLimit) {
		t.Fatal("stopping one viewer freed a still-shared connection")
	}
}

func TestLiveCapacityRecordingBlocksEveryPlaybackEntryAndProbe(t *testing.T) {
	var opened atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		opened.Add(1)
		_, _ = w.Write([]byte("transport-stream-fixture"))
	}))
	defer provider.Close()
	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := manager.Save(config.Settings{Live: config.LiveSettings{Mode: "m3u", PlaylistURL: provider.URL + "/list.m3u", MaxStreams: 1}}); err != nil {
		t.Fatal(err)
	}
	live := NewLiveHandler(provider.Client(), true, stubBinary(t, "true"), 24, 0, 0, false, manager, nil)
	video := NewVideoHandlerWithProvider(true, stubBinary(t, "true"), stubBinary(t, "true"), t.TempDir(), nil)
	t.Cleanup(video.hlsManager.Shutdown)
	video.SetConfigManager(manager)
	video.liveChannels = staticLiveChannelProvider{channels: []LiveChannel{{ID: "channel", URL: provider.URL + "/channel.ts"}}}
	recording := models.Recording{SourceURL: provider.URL + "/channel.ts"}
	release, err := live.ReserveRecording(context.Background(), recording)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	target, err := live.liveCapacityTarget(httptest.NewRequest("GET", "/", nil), recording.SourceURL)
	if err != nil {
		t.Fatal(err)
	}
	ticket := video.registerLiveDirectTarget(liveDirectTarget{URL: recording.SourceURL, Provider: target.Provider, BucketKey: target.BucketKey, MaxStreams: 1})
	for _, mode := range []string{"hls", "proxy", "ticket", "quality"} {
		t.Run(mode, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/live/stream?url="+url.QueryEscape(recording.SourceURL)+"&target=native", nil)
			w := httptest.NewRecorder()
			switch mode {
			case "hls":
				video.StartLiveHLSSession(w, r)
			case "proxy":
				live.StreamChannel(w, r)
			case "ticket":
				video.ServeLiveDirect(w, mux.SetURLVars(r, map[string]string{"ticket": ticket}))
			case "quality":
				r = httptest.NewRequest("POST", "/api/live/quality", strings.NewReader(fmt.Sprintf(`{"url":%q,"channelId":"channel"}`, recording.SourceURL)))
				video.ProbeLiveQuality(w, r)
			}
			if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "LIVE_STREAM_LIMIT_REACHED") {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
	if opened.Load() != 0 {
		t.Fatal("rejected request opened provider input")
	}
	release()
	for range 2 {
		r := httptest.NewRequest("GET", "/api/live/stream?url="+url.QueryEscape(recording.SourceURL)+"&target=native", nil)
		w := httptest.NewRecorder()
		live.StreamChannel(w, r)
		if w.Code != 200 {
			t.Fatalf("freed slot not reusable: %d %s", w.Code, w.Body.String())
		}
		if livecapacity.Default.Current(target.BucketKey) != 0 {
			t.Fatal("ended proxy retained slot")
		}
	}
}

func TestLiveCapacitySelectsActualSourceAndRestrictsAliases(t *testing.T) {
	settings := config.Settings{Live: config.LiveSettings{Sources: []config.LivePlaylistSource{
		{ID: "unlimited", Mode: "m3u", PlaylistURL: "https://other.example/list"},
		{ID: "limited", Mode: "xtream", XtreamHost: "https://provider.example", XtreamUsername: "Account", XtreamPassword: "fixture", MaxStreams: 2},
		{ID: "alias", Mode: "m3u", PlaylistURL: "https://provider.example/get.php?username=Account&password=fixture&type=m3u_plus", MaxStreams: 1},
	}}}
	catalog := staticLiveChannelProvider{channels: []LiveChannel{{URL: "https://cdn.example/live.ts", SourceID: "limited"}}}
	r := httptest.NewRequest("GET", "/api/live/stream", nil)
	target, err := resolveLiveCapacityTarget(r, "https://cdn.example/live.ts", settings, buildGlobalLiveSource(settings), catalog)
	if err != nil || target.SourceID != "limited" || target.MaxStreams != 1 {
		t.Fatalf("actual source/alias cap lost: %+v %v", target, err)
	}
	r = httptest.NewRequest("GET", "/api/live/stream?sourceId=unlimited", nil)
	if _, err := resolveLiveCapacityTarget(r, "https://cdn.example/live.ts", settings, buildGlobalLiveSource(settings), catalog); err == nil {
		t.Fatal("unrelated source bypassed cap")
	}
}

func TestLiveCapacityReleasesFailedHLSCreation(t *testing.T) {
	m := &HLSManager{baseDir: "/dev/null/cannot-create"}
	_, err := m.CreateLiveSession(context.Background(), "https://example.test/a.ts", "m3u", t.Name(), "", "", "", "", LiveTuningSettings{MaxStreams: 1}, false)
	if err == nil {
		t.Fatal("expected directory creation failure")
	}
	if livecapacity.Default.Current(t.Name()) != 0 {
		t.Fatal("failed session leaked reservation")
	}
}

func TestLiveCapacityPlaylistAliasesPreserveAccountIdentity(t *testing.T) {
	first, _ := deriveLiveBucket("m3u", models.ResolvedLiveSource{PlaylistURL: "https://PROVIDER.example/list?token=CaseSensitive&type=ts"})
	alias, _ := deriveLiveBucket("m3u", models.ResolvedLiveSource{PlaylistURL: "https://provider.example/list?type=ts&token=CaseSensitive"})
	other, _ := deriveLiveBucket("m3u", models.ResolvedLiveSource{PlaylistURL: "https://provider.example/list?type=ts&token=casesensitive"})
	if first != alias {
		t.Fatal("equivalent playlist URLs created separate budgets")
	}
	if first == other {
		t.Fatal("case-sensitive credentials were conflated")
	}
	upper, _ := deriveLiveBucket("xtream", models.ResolvedLiveSource{XtreamHost: "https://provider.example", XtreamUsername: "Account"})
	lower, _ := deriveLiveBucket("xtream", models.ResolvedLiveSource{XtreamHost: "https://provider.example", XtreamUsername: "account"})
	if upper == lower {
		t.Fatal("distinct Xtream accounts share a budget")
	}
}

func TestLiveCapacityScheduledKidsRecordingIsRejectedBeforeReservation(t *testing.T) {
	h := &LiveHandler{usersSvc: liveAccountUsers{"child": {ID: "child", IsKidsProfile: true}}}
	if release, err := h.ReserveRecording(context.Background(), models.Recording{UserID: "child"}); err == nil || release != nil {
		t.Fatal("scheduled kids recording bypassed Live TV policy")
	}
}

func TestLiveCapacitySilentProviderCannotHoldStartupIndefinitely(t *testing.T) {
	closed := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(closed)
	}))
	defer provider.Close()
	source := openLiveUpstream(&HLSSession{Path: provider.URL})
	started := time.Now()
	input, err := source.open(context.Background())
	if input != nil {
		input.Close()
	}
	if err == nil {
		t.Fatal("silent provider was accepted")
	}
	if time.Since(started) > liveUpstreamReadTimeout+2*time.Second {
		t.Fatal("startup exceeded bounded input timeout")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("timed-out provider connection remained open")
	}
}
