package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A public-broadcaster style master: two 720p variants (the second a backup CDN), audio as
// renditions, subtitles.
const backlogTestMaster = `#EXTM3U
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a1",NAME="TV Ton",LANGUAGE="deu",DEFAULT=YES,URI="/live/a1/4.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a1",NAME="Klare Sprache",LANGUAGE="mul",URI="/live/a1/5.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a2",NAME="TV Ton",LANGUAGE="deu",DEFAULT=YES,URI="/live-b/a2/4.m3u8"
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="t1",NAME="Untertitel",LANGUAGE="deu",URI="/live/t1/7.m3u8"
#EXT-X-STREAM-INF:CODECS="avc1.64001f,mp4a.40.2",BANDWIDTH=1500000,AUDIO="a1",RESOLUTION=640x360
/live/v/low.m3u8
#EXT-X-STREAM-INF:CODECS="avc1.640028,mp4a.40.2",BANDWIDTH=4504154,AUDIO="a1",SUBTITLES="t1",RESOLUTION=1280x720
/live/v/3.m3u8
#EXT-X-STREAM-INF:CODECS="avc1.640028,mp4a.40.2",BANDWIDTH=4504154,AUDIO="a2",RESOLUTION=1280x720
/live-b/v/3.m3u8
`

func TestPickHLSBacklogRenditions(t *testing.T) {
	base, _ := url.Parse("https://cdn.example/hls/master.m3u8")
	got, ok := pickHLSBacklogRenditions(backlogTestMaster, base)
	if !ok {
		t.Fatal("master not recognized")
	}
	want := hlsBacklogRenditions{VideoURL: "https://cdn.example/live/v/3.m3u8", AudioURL: "https://cdn.example/live/a1/4.m3u8"}
	if got != want {
		t.Errorf("renditions = %+v, want %+v", got, want)
	}
	muxed := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=900000,RESOLUTION=1280x720\nhd.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=300000,RESOLUTION=640x360\nsd.m3u8\n"
	if got, ok := pickHLSBacklogRenditions(muxed, base); !ok || got.VideoURL != "https://cdn.example/hls/hd.m3u8" || got.AudioURL != "" {
		t.Errorf("muxed variant = %+v (%v)", got, ok)
	}
	if _, ok := pickHLSBacklogRenditions("#EXTM3U\n#EXTINF:2,\nseg1.ts\n", base); ok {
		t.Error("a media playlist is not a master")
	}
}

func backlogMediaPlaylist(firstSequence, count int, prefix string, discontinuityAt int) string {
	lines := []string{"#EXTM3U", "#EXT-X-VERSION:3", "#EXT-X-TARGETDURATION:2", fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:%d", firstSequence)}
	for i := 0; i < count; i++ {
		if i == discontinuityAt {
			lines = append(lines, "#EXT-X-DISCONTINUITY")
		}
		lines = append(lines, "#EXTINF:2.000,", fmt.Sprintf("%s%d.ts", prefix, firstSequence+i))
	}
	return strings.Join(lines, "\n") + "\n"
}

func TestParseHLSMediaPlaylist(t *testing.T) {
	base, _ := url.Parse("https://cdn.example/live/v/3.m3u8")
	p := parseHLSMediaPlaylist(backlogMediaPlaylist(100, 5, "v", 2), base)
	if !p.ok || len(p.Segments) != 5 || p.duration() != 10 {
		t.Fatalf("playlist = %+v", p)
	}
	if s := p.Segments[2]; s.Sequence != 102 || !s.Discontinuity || s.URI != "https://cdn.example/live/v/v102.ts" {
		t.Errorf("segment 2 = %+v", s)
	}
	for name, body := range map[string]string{
		"encrypted":  strings.Replace(backlogMediaPlaylist(1, 3, "v", -1), "#EXT-X-MEDIA-SEQUENCE:1", "#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\"", 1),
		"fmp4":       strings.Replace(backlogMediaPlaylist(1, 3, "v", -1), "#EXT-X-MEDIA-SEQUENCE:1", "#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-MAP:URI=\"init.mp4\"", 1),
		"ended":      backlogMediaPlaylist(1, 3, "v", -1) + "#EXT-X-ENDLIST\n",
		"master":     backlogTestMaster,
		"empty":      "#EXTM3U\n",
		"local file": "#EXTM3U\n#EXTINF:2,\nfile:///recordings/x.ts\n",
		"concat":     "#EXTM3U\n#EXTINF:2,\nconcat:a.ts|b.ts\n",
		"byte range": "#EXTM3U\n#EXTINF:2,\n#EXT-X-BYTERANGE:1000@0\nall.ts\n",
		"gap":        "#EXTM3U\n#EXT-X-GAP\n#EXTINF:2,\nmissing.ts\n",
	} {
		if parseHLSMediaPlaylist(body, base).ok {
			t.Errorf("%s playlist accepted", name)
		}
	}
}

func TestPlanLiveBacklog(t *testing.T) {
	base, _ := url.Parse("https://cdn.example/")
	now := time.Now()
	video := parseHLSMediaPlaylist(backlogMediaPlaylist(1000, 40, "v", 30), base)
	// Audio lists one segment more at its start: matched by media sequence, not position.
	audio := parseHLSMediaPlaylist(backlogMediaPlaylist(999, 41, "a", -1), base)

	plan := planLiveBacklog(video, audio, true, 20*time.Second, now, 10*time.Second)
	if len(plan) != 10 {
		t.Fatalf("planned %d segments, want 10 (20 s)", len(plan))
	}
	// It ends before the newest three, which the live encode starts with.
	if last := plan[len(plan)-1]; last.VideoURL != "https://cdn.example/v1036.ts" || last.AudioURL != "https://cdn.example/a1036.ts" || last.Sequence != 1036 {
		t.Errorf("last backlog segment = %+v", last)
	}
	if plan[0].VideoURL != "https://cdn.example/v1027.ts" || plan[0].Discontinuity {
		t.Errorf("first backlog segment = %+v (its discontinuity is not listed)", plan[0])
	}
	if !plan[3].Discontinuity || plan[3].VideoURL != "https://cdn.example/v1030.ts" {
		t.Errorf("the source's discontinuity must carry over: %+v", plan[3])
	}
	// The source (80 s) drops v1027, 26 s from its start, 54 s from now; it leaves 10 s earlier.
	if got := plan[0].Expires.Sub(now); got != 44*time.Second {
		t.Errorf("v1027 expires in %s, want 44s", got)
	}
	if got := plan[9].Expires.Sub(now); got != 62*time.Second {
		t.Errorf("v1036 expires in %s, want 62s", got)
	}

	if plan := planLiveBacklog(video, audio, false, time.Hour, now, 0); len(plan) != 37 {
		t.Errorf("a long window takes the whole source but the edge: %d", len(plan))
	}
	short := parseHLSMediaPlaylist(backlogMediaPlaylist(1020, 10, "a", -1), base)
	if planLiveBacklog(video, short, true, 20*time.Second, now, 0) != nil {
		t.Error("a video segment without its audio must not be planned")
	}
	if planLiveBacklog(video, hlsMediaPlaylist{}, true, 20*time.Second, now, 0) != nil {
		t.Error("missing audio playlist")
	}
	if planLiveBacklog(parseHLSMediaPlaylist(backlogMediaPlaylist(1, 3, "v", -1), base), audio, false, time.Hour, now, 0) != nil {
		t.Error("a source with only the live edge has no backlog")
	}
}

func TestLiveBacklogMuxArgs(t *testing.T) {
	args := liveBacklogMuxArgs("https://cdn/v1.ts", "https://cdn/a1.ts", map[string]string{"Referer": "https://tv"}, liveBacklogAudio{decided: true, copy: true}, "/out/segment1.ts.tmp")
	joined := strings.Join(args, " ")
	// Video is optional: radio channels have none.
	for _, want := range []string{"-map 0:v:0? -map 1:a:0 ", "-c:v copy", "-c:a copy", "-copyts", "-f mpegts /out/segment1.ts.tmp", "Referer: https://tv"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q lack %q", joined, want)
		}
	}
	// Every input is limited to network protocols (input options apply to the next -i only).
	for i, arg := range args {
		if arg == "-i" {
			j := -1
			for k := range i {
				if args[k] == "-protocol_whitelist" {
					j = k
				}
			}
			if j < 0 || args[j+1] != "http,https,tcp,tls,crypto" || slices.Contains(args[j:i], "-i") {
				t.Errorf("input %s is not limited to network protocols: %q", args[i+1], joined)
			}
		}
	}
	// A muxed source: the audio stream the live encode takes (here the third).
	joined = strings.Join(liveBacklogMuxArgs("https://cdn/v1.ts", "", nil, liveBacklogAudio{decided: true, stream: 2}, "o.ts"), " ")
	if !strings.Contains(joined, "-map 0:a:2 ") || !strings.Contains(joined, strings.Join(timeshiftAudioArgs, " ")) || strings.Contains(joined, "-headers") {
		t.Errorf("muxed segment args %q", joined)
	}
	if joined = strings.Join(liveBacklogMuxArgs("https://cdn/v1.ts", "", nil, liveBacklogAudio{decided: true, stream: -1}, "o.ts"), " "); strings.Contains(joined, ":a:") {
		t.Errorf("a source without audio maps none: %q", joined)
	}
}

// The audio stream is the one FFmpeg's automatic selection takes for the live encode: most
// channels, the first of those.
func TestParseLiveBacklogStreams(t *testing.T) {
	streams := parseLiveBacklogStreams("h264,video,1.400000\nmp2,audio,2,1.420000\naac,audio,6,1.430000\nac3,audio,6,1.430000\n")
	want := []liveBacklogStream{
		{codec: "h264", codecType: "video", start: 1.4},
		{codec: "mp2", codecType: "audio", channels: 2, start: 1.42},
		{codec: "aac", codecType: "audio", channels: 6, start: 1.43},
		{codec: "ac3", codecType: "audio", channels: 6, start: 1.43},
	}
	if !slices.Equal(streams, want) {
		t.Errorf("streams = %+v", streams)
	}
}

// Queued work for a position the player has left is dropped.
func TestLiveBacklogStale(t *testing.T) {
	b := newLiveBacklog(nil, "", "", "", nil)
	if b.stale(40) {
		t.Error("nothing asked yet: nothing is stale")
	}
	b.latest = 50
	for n, want := range map[int]bool{49: true, 50: false, 53: false, 54: true} {
		if b.stale(n) != want {
			t.Errorf("segment %d stale = %v with the player at 50", n, !want)
		}
	}
}

// backlogTestSource serves the master, its 720p and audio playlists (60 segments of 2 s, a
// discontinuity at the 41st) and MPEG-TS-looking segments; audio segments start like packed audio
// when packedAudio is set.
func backlogTestSource(t *testing.T, packedAudio bool) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/master.m3u8":
			_, _ = w.Write([]byte(backlogTestMaster))
		case r.URL.Path == "/live/v/3.m3u8":
			_, _ = w.Write([]byte(backlogMediaPlaylist(500, 60, "v", 40)))
		case r.URL.Path == "/live/a1/4.m3u8":
			_, _ = w.Write([]byte(backlogMediaPlaylist(500, 60, "a", -1)))
		case strings.HasPrefix(r.URL.Path, "/live/a1/") && packedAudio:
			_, _ = w.Write([]byte("ID3\x04"))
		case strings.HasSuffix(r.URL.Path, ".ts"):
			_, _ = w.Write([]byte{0x47, 0x40, 0x00, 0x10})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The backlog is listed from the start, the live encode follows it after one discontinuity, and a
// restart never numbers into it.
func TestPrepareLiveBacklogListsTheSourceWindow(t *testing.T) {
	srv := backlogTestSource(t, false)
	m := &HLSManager{}
	session := &HLSSession{ID: "backlog", IsLive: true, OutputDir: t.TempDir(), LiveTuning: LiveTuningSettings{TimeshiftWindow: time.Minute}}

	b, liveStart := m.prepareLiveBacklog(context.Background(), session, srv.URL+"/master.m3u8", srv.URL+"/master.m3u8", nil)
	if b != 30 || session.liveBacklogEnd() != 30 {
		t.Fatalf("backlog = %d segments, want 30 (one minute)", b)
	}
	// FFmpeg starts with v557, the segment after the backlog: the third from the end.
	if liveStart != -3 {
		t.Errorf("live_start_index %d, want -3", liveStart)
	}
	if first := session.liveBacklog.segments[0]; first.AudioURL != srv.URL+"/live/a1/a527.ts" || first.Sequence != 527 {
		t.Errorf("backlog = %+v", first)
	}

	playlist := m.buildSeamlessLivePlaylist(session, liveTestPlaylist(30, 3, 2, true))
	snapshot := parseLivePlaylist(playlist)
	// The backlog fills the minute: the live encode's first segments push its oldest out.
	if snapshot.MediaSequence != 3 || len(snapshot.Entries) != 30 {
		t.Fatalf("playlist starts at %d with %d entries:\n%s", snapshot.MediaSequence, len(snapshot.Entries), playlist)
	}
	if !strings.Contains(playlist, "#EXT-X-DISCONTINUITY\n#EXTINF:2.000000,\nsegment30.ts") {
		t.Errorf("no discontinuity where the live encode starts:\n%s", playlist)
	}
	// The source's own discontinuity (v540, backlog segment 13) is listed too.
	if at := slices.IndexFunc(snapshot.Entries, func(e livePlaylistEntry) bool { return e.Discontinuity }); at != 10 {
		t.Errorf("first discontinuity at entry %d, want 10", at)
	}
	if got := max(nextLiveSegmentNumber(session.OutputDir), session.liveBacklogEnd()); got != 30 {
		t.Errorf("a restart would start at %d", got)
	}
}

// A backlog entry leaves the playlist before the source drops it, however long the timeshift
// window.
func TestBacklogEntriesLeaveBeforeTheSourceDropsThem(t *testing.T) {
	srv := backlogTestSource(t, false)
	m := &HLSManager{}
	session := &HLSSession{ID: "expiry", IsLive: true, OutputDir: t.TempDir(), LiveTuning: LiveTuningSettings{TimeshiftWindow: 4 * time.Hour}}
	b, _ := m.prepareLiveBacklog(context.Background(), session, srv.URL+"/master.m3u8", srv.URL+"/master.m3u8", nil)
	// The source lists 120 s: the backlog is shorter by the edge and the margins.
	if b == 0 || float64(b)*2 > 120-6-20 {
		t.Fatalf("backlog of %d segments for a 120 s source", b)
	}
	session.mu.Lock()
	for i := range 5 {
		session.liveBacklog.segments[i].Expires = time.Now().Add(-time.Second)
	}
	session.mu.Unlock()
	snapshot := parseLivePlaylist(m.buildSeamlessLivePlaylist(session, liveTestPlaylist(b, 2, 2, true)))
	if snapshot.MediaSequence != 5 {
		t.Errorf("playlist starts at %d, want 5 (the expired entries left)", snapshot.MediaSequence)
	}
}

// ARD and ZDF send their audio rendition as packed audio: the backlog takes it, marked for muxing
// by its ID3 timestamp (hls_live_backlog_packed.go).
func TestPrepareLiveBacklogTakesPackedAudio(t *testing.T) {
	srv := backlogTestSource(t, true)
	session := &HLSSession{ID: "packed", IsLive: true, LiveTuning: LiveTuningSettings{TimeshiftWindow: time.Minute}}
	b, _ := (&HLSManager{}).prepareLiveBacklog(context.Background(), session, srv.URL+"/master.m3u8", srv.URL+"/master.m3u8", nil)
	defer session.stopLiveBacklog()
	if b == 0 || session.liveBacklog == nil || !session.liveBacklog.audioPacked {
		t.Fatalf("no packed-audio backlog: %d segments", b)
	}
	// MPEG-TS audio is not packed audio.
	ts := &HLSSession{ID: "ts", IsLive: true, LiveTuning: LiveTuningSettings{TimeshiftWindow: time.Minute}}
	srvTS := backlogTestSource(t, false)
	if b, _ := (&HLSManager{}).prepareLiveBacklog(context.Background(), ts, srvTS.URL+"/master.m3u8", srvTS.URL+"/master.m3u8", nil); b == 0 || ts.liveBacklog.audioPacked {
		t.Fatalf("MPEG-TS audio backlog: %d segments, packed=%v", b, b > 0 && ts.liveBacklog.audioPacked)
	}
	ts.stopLiveBacklog()
}

// Sources the backlog cannot use leave the session as before.
func TestPrepareLiveBacklogSkipsUnusableSources(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/edge.m3u8":
			_, _ = w.Write([]byte(backlogMediaPlaylist(1, 3, "v", -1)))
		case "/ended.m3u8":
			_, _ = w.Write([]byte(backlogMediaPlaylist(1, 30, "v", -1) + "#EXT-X-ENDLIST\n"))
		case "/stream.ts":
			_, _ = w.Write([]byte{0x47, 0x40, 0x00})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m := &HLSManager{}
	for _, path := range []string{"/edge.m3u8", "/ended.m3u8", "/stream.ts", "/missing.m3u8"} {
		session := &HLSSession{ID: "skip", IsLive: true, LiveTuning: LiveTuningSettings{TimeshiftWindow: time.Minute}}
		if b, _ := m.prepareLiveBacklog(context.Background(), session, srv.URL+path, srv.URL+path, nil); b != 0 || session.liveBacklog != nil || len(session.livePlaylistWindow) != 0 {
			t.Errorf("%s: backlog %d", path, b)
		}
	}
}

// Segment URLs are looked up in a fresh source playlist; when its tokenized URL stops working the
// playlist is resolved again from the channel URL. A segment the source no longer lists is gone.
func TestLiveBacklogSegmentURLs(t *testing.T) {
	var token, fetches atomic.Int64
	token.Store(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channel":
			http.Redirect(w, r, fmt.Sprintf("/media.m3u8?tok=%d", token.Load()), http.StatusFound)
		case "/media.m3u8":
			fetches.Add(1)
			if r.URL.Query().Get("tok") != strconv.FormatInt(token.Load(), 10) {
				http.Error(w, "expired", http.StatusForbidden)
				return
			}
			_, _ = fmt.Fprintf(w, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:10\n#EXTINF:2,\nv10.ts?tok=%d\n#EXTINF:2,\nv11.ts?tok=%d\n", token.Load(), token.Load())
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	b := newLiveBacklog(nil, srv.URL+"/channel", srv.URL+"/media.m3u8?tok=1", "", nil)
	ctx := context.Background()

	video, _, err := b.segmentURLs(ctx, liveBacklogSegment{Sequence: 11, VideoURL: srv.URL + "/v11.ts?tok=0"})
	if err != nil || video != srv.URL+"/v11.ts?tok=1" {
		t.Errorf("segment 11 = %q, %v", video, err)
	}
	if _, _, err := b.segmentURLs(ctx, liveBacklogSegment{Sequence: 9, VideoURL: "tune-in"}); !errors.Is(err, errLiveBacklogGone) {
		t.Errorf("a dropped segment: %v", err)
	}
	if fetches.Load() != 1 {
		t.Errorf("%d playlist fetches, want 1 (cached)", fetches.Load())
	}

	// The playlist's token expires: it is resolved again from the channel URL.
	token.Store(2)
	b.mu.Lock()
	b.playlists = map[string]liveBacklogPlaylist{}
	b.mu.Unlock()
	video, _, err = b.segmentURLs(ctx, liveBacklogSegment{Sequence: 10, VideoURL: srv.URL + "/v10.ts?tok=0"})
	if err != nil || video != srv.URL+"/v10.ts?tok=2" {
		t.Errorf("after the token expired: %q, %v", video, err)
	}

	// Nothing can be read: the tune-in URLs are used.
	unreachable := newLiveBacklog(nil, "http://127.0.0.1:1/channel", "http://127.0.0.1:1/media.m3u8", "", nil)
	if video, _, err := unreachable.segmentURLs(ctx, liveBacklogSegment{Sequence: 3, VideoURL: "tune-in"}); err != nil || video != "tune-in" {
		t.Errorf("unreachable source: %q, %v", video, err)
	}
}

// A player tuning in starts in the live encode, at its first segment while it is still shorter than
// the usual start offset, never in the backlog before it.
func TestLiveStartTagAfterBacklog(t *testing.T) {
	m := &HLSManager{}
	session := &HLSSession{ID: "start", IsLive: true, LiveTuning: LiveTuningSettings{TimeshiftWindow: time.Hour}}
	session.liveBacklog = newLiveBacklog(make([]liveBacklogSegment, 30), "", "", "", nil)
	for i := range 30 {
		session.liveBacklog.segments[i].Expires = time.Now().Add(time.Hour)
		session.livePlaylistWindow = append(session.livePlaylistWindow, livePlaylistEntry{MediaSequence: i, URI: fmt.Sprintf("segment%d.ts", i), ExtInf: "6.000000,", Duration: 6})
	}
	session.livePlaylistTargetDuration = 6
	playlist := m.buildSeamlessLivePlaylist(session, liveTestPlaylist(30, 3, 2, true))
	// Inside the first live segment, never on the boundary to the backlog.
	if got := livePlaylistStartTagAfterBacklog(playlist, 30); got != "#EXT-X-START:TIME-OFFSET=-5.900,PRECISE=YES" {
		t.Errorf("with 6 s of live: %q", got)
	}
	odd := strings.Replace(playlist, "#EXTINF:2.000000,\nsegment30.ts", "#EXTINF:2.000600,\nsegment30.ts", 1)
	if got := livePlaylistStartTagAfterBacklog(odd, 30); got != "#EXT-X-START:TIME-OFFSET=-5.900,PRECISE=YES" {
		t.Errorf("with 6.0006 s of live: %q", got)
	}
	// The usual offset from the live encode's own target duration (2 s), not the backlog's 6 s.
	playlist = m.buildSeamlessLivePlaylist(session, liveTestPlaylist(30, 12, 2, true))
	if got := livePlaylistStartTagAfterBacklog(playlist, 30); got != "#EXT-X-START:TIME-OFFSET=-14,PRECISE=YES" {
		t.Errorf("with 24 s of live: %q", got)
	}
	if got := livePlaylistStartTagAfterBacklog(playlist, 0); got != livePlaylistStartTag(playlist) {
		t.Errorf("without a backlog the usual tag: %q", got)
	}
}

// Moving the player cancels the work that became stale.
func TestLiveBacklogNoteRequestCancelsStaleJobs(t *testing.T) {
	b := newLiveBacklog(nil, "", "", "", nil)
	jobs := map[int]context.Context{}
	for _, n := range []int{10, 11, 40} {
		ctx, cancel := context.WithCancel(context.Background())
		jobs[n] = ctx
		b.inflight[n] = &liveBacklogJob{done: make(chan struct{}), ctx: ctx, cancel: cancel}
	}
	b.noteRequest(40)
	for n, want := range map[int]bool{10: true, 11: true, 40: false} {
		if cancelled := jobs[n].Err() != nil; cancelled != want {
			t.Errorf("job %d cancelled = %v, want %v", n, cancelled, want)
		}
	}
}

// A withdrawn backlog leaves the playlist at the next refresh.
func TestWithdrawnBacklogLeavesThePlaylist(t *testing.T) {
	m := &HLSManager{}
	session := &HLSSession{ID: "withdraw", IsLive: true, LiveTuning: LiveTuningSettings{TimeshiftWindow: time.Hour}}
	session.liveBacklog = newLiveBacklog(make([]liveBacklogSegment, 5), "", "", "", nil)
	for i := range 5 {
		session.liveBacklog.segments[i].Expires = time.Now().Add(time.Hour)
		session.livePlaylistWindow = append(session.livePlaylistWindow, livePlaylistEntry{MediaSequence: i, URI: fmt.Sprintf("segment%d.ts", i), ExtInf: "2.000000,", Duration: 2})
	}
	session.withdrawLiveBacklog()
	snapshot := parseLivePlaylist(m.buildSeamlessLivePlaylist(session, liveTestPlaylist(5, 3, 2, true)))
	if snapshot.MediaSequence != 5 || len(snapshot.Entries) != 3 {
		t.Errorf("playlist starts at %d with %d entries, want the live part only", snapshot.MediaSequence, len(snapshot.Entries))
	}
}

// A job cancelled for a position the player left is replaced, not joined, when the player comes
// back to it.
func TestCancelledBacklogJobIsReplaced(t *testing.T) {
	session := &HLSSession{ID: "rejoin", IsLive: true, OutputDir: t.TempDir()}
	session.liveBacklog = newLiveBacklog(make([]liveBacklogSegment, 5), "", "", "", nil)
	b := session.liveBacklog
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stale := &liveBacklogJob{done: make(chan struct{}), ctx: ctx, cancel: cancel}
	b.inflight[2] = stale
	// Keep the replacement waiting for a slot: all of them are taken.
	for range liveBacklogParallel {
		b.slots <- struct{}{}
	}
	b.playerSlot <- struct{}{}
	waitCtx, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	_ = (&HLSManager{}).ensureLiveBacklogSegment(waitCtx, session, 2, false)
	b.mu.Lock()
	replaced := b.inflight[2] != stale && b.inflight[2] != nil && b.inflight[2].player
	b.mu.Unlock()
	if !replaced {
		t.Error("the cancelled job was joined instead of replaced by a player job")
	}
	session.stopLiveBacklog()
}

// A job a player request still waits for is not cancelled when the player asks for the next one.
func TestNoteRequestKeepsAwaitedJobs(t *testing.T) {
	b := newLiveBacklog(nil, "", "", "", nil)
	awaitedCtx, cancelAwaited := context.WithCancel(context.Background())
	idleCtx, cancelIdle := context.WithCancel(context.Background())
	b.inflight[10] = &liveBacklogJob{done: make(chan struct{}), ctx: awaitedCtx, cancel: cancelAwaited, waiters: 1}
	b.inflight[11] = &liveBacklogJob{done: make(chan struct{}), ctx: idleCtx, cancel: cancelIdle}
	b.noteRequest(20)
	if awaitedCtx.Err() != nil {
		t.Error("a job a player waits for was cancelled")
	}
	if idleCtx.Err() == nil {
		t.Error("an abandoned job was kept")
	}
	if !b.abandoned(11, b.inflight[11]) || b.abandoned(10, b.inflight[10]) {
		t.Error("abandoned() disagrees")
	}
}

// Waiting for the audio decision respects the caller's deadline.
func TestLiveBacklogAudioWaitRespectsDeadline(t *testing.T) {
	b := newLiveBacklog(nil, "", "", "", nil)
	b.audioTurn <- struct{}{} // another decision is running
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := (&HLSManager{}).liveBacklogAudio(ctx, b, "https://cdn/v.ts", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline", err)
	}
}

// A default audio rendition without a URI is the variant's own audio: the backlog takes that, not
// another rendition of the group (audio description, say).
func TestPickHLSBacklogRenditionsDefaultInVariant(t *testing.T) {
	base, _ := url.Parse("https://cdn.example/hls/master.m3u8")
	master := "#EXTM3U\n" +
		"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",NAME=\"Deutsch\",LANGUAGE=\"deu\",DEFAULT=YES\n" +
		"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",NAME=\"Audiodeskription\",LANGUAGE=\"deu\",URI=\"ad.m3u8\"\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=4000000,RESOLUTION=1280x720,AUDIO=\"a\"\n" +
		"hd.m3u8\n"
	got, ok := pickHLSBacklogRenditions(master, base)
	if !ok || got.VideoURL != "https://cdn.example/hls/hd.m3u8" || got.AudioURL != "" {
		t.Errorf("renditions = %+v (%v), want the variant's own audio", got, ok)
	}
}

// Segments are looked up again by media sequence: a playlist without one, or whose sequence does
// not follow its segments, gets no backlog.
func TestBacklogNeedsAWorkingMediaSequence(t *testing.T) {
	base, _ := url.Parse("https://cdn.example/")
	if parseHLSMediaPlaylist("#EXTM3U\n#EXTINF:2,\na.ts\n#EXTINF:2,\nb.ts\n", base).ok {
		t.Error("a playlist without EXT-X-MEDIA-SEQUENCE accepted")
	}
	// The sequence never advances while the segments slide.
	var reads atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".m3u8"):
			shift := int(reads.Add(1)) - 1
			lines := []string{"#EXTM3U", "#EXT-X-TARGETDURATION:2", "#EXT-X-MEDIA-SEQUENCE:0"}
			for i := range 60 {
				lines = append(lines, "#EXTINF:2.000,", fmt.Sprintf("s%d.ts", i+shift))
			}
			_, _ = w.Write([]byte(strings.Join(lines, "\n") + "\n"))
		default:
			_, _ = w.Write([]byte{0x47, 0x40, 0x00})
		}
	}))
	defer srv.Close()
	session := &HLSSession{ID: "stuck", IsLive: true, LiveTuning: LiveTuningSettings{TimeshiftWindow: time.Minute}}
	if b, _ := (&HLSManager{}).prepareLiveBacklog(context.Background(), session, srv.URL+"/live.m3u8", srv.URL+"/live.m3u8", nil); b != 0 {
		t.Errorf("backlog of %d segments over a media sequence that does not advance", b)
	}
	if !sameHLSSegment("https://cdn/s/1.ts?tok=a", "https://cdn/s/1.ts?tok=b") || sameHLSSegment("https://cdn/s/1.ts", "https://cdn/s/2.ts") {
		t.Error("sameHLSSegment")
	}
}

// The fallback mux leaves the streams to FFmpeg's own pick and encodes the audio.
func TestLiveBacklogMuxArgsAutoAudio(t *testing.T) {
	joined := strings.Join(liveBacklogMuxArgs("https://cdn/v1.ts", "", nil, liveBacklogAudio{decided: true, stream: liveBacklogAutoAudio}, "o.ts"), " ")
	if strings.Contains(joined, "-map") || !strings.Contains(joined, strings.Join(timeshiftAudioArgs, " ")) {
		t.Errorf("fallback args %q", joined)
	}
}

// A source that started its media sequence over lists other files under the old numbers: such a
// segment is gone, not another one.
func TestLiveBacklogSegmentURLsRejectsRenumberedSegments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:10\n#EXTINF:2,\nnew-a.ts\n#EXTINF:2,\nnew-b.ts\n")
	}))
	defer srv.Close()
	b := newLiveBacklog(nil, srv.URL+"/v.m3u8", srv.URL+"/v.m3u8", "", nil)
	if _, _, err := b.segmentURLs(context.Background(), liveBacklogSegment{Sequence: 11, VideoURL: srv.URL + "/old-b.ts"}); !errors.Is(err, errLiveBacklogGone) {
		t.Errorf("renumbered segment: %v", err)
	}
	if got, _, err := b.segmentURLs(context.Background(), liveBacklogSegment{Sequence: 11, VideoURL: srv.URL + "/new-b.ts?tok=old"}); err != nil || got != srv.URL+"/new-b.ts" {
		t.Errorf("same file, new token: %q %v", got, err)
	}
}

// A source with a short window of its own (IPTV: 6 × 10 s) gets no backlog: what it could offer
// would leave the playlist almost at once.
func TestShortSourceWindowGetsNoBacklog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".m3u8") {
			lines := []string{"#EXTM3U", "#EXT-X-TARGETDURATION:10", "#EXT-X-MEDIA-SEQUENCE:100"}
			for i := range 6 {
				lines = append(lines, "#EXTINF:10.000,", fmt.Sprintf("s%d.ts", 100+i))
			}
			_, _ = w.Write([]byte(strings.Join(lines, "\n") + "\n"))
			return
		}
		_, _ = w.Write([]byte{0x47, 0x40, 0x00})
	}))
	defer srv.Close()
	session := &HLSSession{ID: "iptv", IsLive: true, LiveTuning: LiveTuningSettings{TimeshiftWindow: time.Hour}}
	if b, _ := (&HLSManager{}).prepareLiveBacklog(context.Background(), session, srv.URL+"/live.m3u8", srv.URL+"/live.m3u8", nil); b != 0 {
		t.Errorf("backlog of %d segments from a 60 s source window", b)
	}
}

// Entries that would leave the playlist within half a minute are left out; what remains must be
// worth it.
func TestWorthwhileLiveBacklog(t *testing.T) {
	now := time.Now()
	plan := make([]liveBacklogSegment, 60)
	for i := range plan {
		plan[i] = liveBacklogSegment{Duration: 2, Expires: now.Add(time.Duration(i) * 2 * time.Second), Discontinuity: i == 15}
	}
	got := worthwhileLiveBacklog(plan, now, time.Hour)
	// Entries 0..14 expire within 30 s.
	if len(got) != 45 || got[0].Discontinuity {
		t.Fatalf("kept %d entries (first discontinuity %v), want 45 without a leading discontinuity", len(got), len(got) > 0 && got[0].Discontinuity)
	}
	if worthwhileLiveBacklog(plan[:20], now, time.Hour) != nil {
		t.Error("10 s left of a backlog for an hour's window is not worth it")
	}
	if got := worthwhileLiveBacklog(plan[15:30], now, 50*time.Second); len(got) != 15 {
		t.Errorf("30 s for a 50 s window: kept %d entries, want 15", len(got))
	}
}

// A playlist larger than the limit is refused, not cut.
func TestFetchHLSPlaylistRefusesOversizedPlaylists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n" + strings.Repeat("#", liveBacklogPlaylistLimit)))
	}))
	defer srv.Close()
	if _, _, err := fetchHLSPlaylist(context.Background(), srv.Client(), srv.URL, nil); err == nil {
		t.Error("an oversized playlist was accepted")
	}
}

// Live CC extraction lists only the live encode's segments, not a timeshift backlog's.
func TestListLiveSegmentsSkipsBacklog(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"segment3.ts", "segment19.ts", "segment20.ts", "segment21.ts"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte{0x47}, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := listLiveSegments(dir, 20)
	if len(got) != 2 || filepath.Base(got[0]) != "segment20.ts" || filepath.Base(got[1]) != "segment21.ts" {
		t.Errorf("segments = %v", got)
	}
	if len(listLiveSegments(dir, 0)) != 4 {
		t.Error("without a backlog every segment counts")
	}
}
