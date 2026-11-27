package recordings

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRankedHLSVariants(t *testing.T) {
	master := func(lines ...string) string { return "#EXTM3U\n" + strings.Join(lines, "\n") + "\n" }
	programs := func(variants []hlsVariant) []int {
		out := []int{}
		for _, v := range variants {
			out = append(out, v.program)
		}
		return out
	}
	for _, tc := range []struct {
		name     string
		playlist string
		want     []int
	}{
		{"highest bandwidth first", master(
			`#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360`, "low.m3u8",
			`#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2"`, "high.m3u8",
			`#EXT-X-STREAM-INF:BANDWIDTH=2500000,RESOLUTION=1280x720`, "mid.m3u8"), []int{1, 2, 0}},
		{"ZDF: the same variant from two CDNs, the first first", master(
			`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",NAME="TV Ton",LANGUAGE="deu",DEFAULT=YES,URI="a/4.m3u8"`,
			`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="b",NAME="TV Ton",LANGUAGE="deu",DEFAULT=YES,URI="b/4.m3u8"`,
			`#EXT-X-STREAM-INF:CODECS="avc1.640028,mp4a.40.2",BANDWIDTH=4504154,AUDIO="a"`, "a/3.m3u8",
			`#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=90000,URI="a/iframe.m3u8"`,
			`#EXT-X-STREAM-INF:CODECS="avc1.640028,mp4a.40.2",BANDWIDTH=4504154,AUDIO="b"`, "b/3.m3u8"), []int{0, 1}},
		{"a radio master: audio-only variants are not ranked", master(
			`#EXT-X-STREAM-INF:BANDWIDTH=128000,CODECS="mp4a.40.2"`, "hi.m3u8",
			`#EXT-X-STREAM-INF:BANDWIDTH=64000,CODECS="mp4a.40.5"`, "lo.m3u8"), []int{}},
		{"an audio-only variant next to pictures keeps its program number", master(
			`#EXT-X-STREAM-INF:BANDWIDTH=9000000,CODECS="mp4a.40.2"`, "audio.m3u8",
			`#EXT-X-STREAM-INF:BANDWIDTH=800000,CODECS="avc1.4d401f,mp4a.40.2"`, "v.m3u8"), []int{1}},
		{"a variant without attributes still counts, as in FFmpeg", master(
			`#EXT-X-STREAM-INF:`, "a.m3u8",
			`#EXT-X-STREAM-INF:BANDWIDTH=9`, "b.m3u8"), []int{1, 0}},
		{"a single variant: nothing to choose", master(`#EXT-X-STREAM-INF:BANDWIDTH=1`, "v.m3u8"), []int{}},
		{"media playlist", "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6,\ns1.ts\n", []int{}},
	} {
		if got := programs(rankedHLSVariants(tc.playlist)); fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRecordingStreamMaps(t *testing.T) {
	var streamGets atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stream":
			// A transport stream: HEAD is enough to tell; a GET would take an IPTV slot.
			if r.Method == http.MethodGet {
				streamGets.Add(1)
			}
			w.Header().Set("Content-Type", "video/mp2t")
			_, _ = w.Write([]byte{0x47, 0x40, 0x00, 0x10})
		case "/redirect":
			http.Redirect(w, r, "/master.m3u8", http.StatusFound)
		case "/nohead":
			http.Redirect(w, r, "/nohead/master.m3u8", http.StatusFound)
		case "/nohead/master.m3u8":
			// Refuses HEAD after the redirect: its extension tells.
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			_, _ = fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\n../low.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=9\n../high.m3u8\n")
		case "/master.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nlow.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=9\nhigh.m3u8\n")
		case "/broken.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nlow.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=9\nmissing.m3u8\n")
		case "/low.m3u8", "/high.m3u8":
			_, _ = fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2,\ns.ts\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	for path, want := range map[string]string{
		"/stream":       "-map 0",
		"/missing":      "-map 0",
		"/redirect":     "-map 0:v -map 0:a? < /high.m3u8",
		"/master.m3u8":  "-map 0:v -map 0:a? < /high.m3u8",
		"/broken.m3u8":  "-map 0:v -map 0:a? < /low.m3u8", // the best variant does not load: the next one
		"/nohead":       "-map 0:v -map 0:a? < /high.m3u8",
		"/low.m3u8":     "-map 0", // a media playlist
		"/stream?x=.ts": "-map 0",
	} {
		choice := chooseRecordingStreams(context.Background(), ts.URL+path, recordingStreamChoice{}, false)
		got := strings.Join(choice.args(), " ")
		if len(choice.inputs) > 0 {
			got += " < " + strings.TrimPrefix(strings.Join(choice.inputs, " "), ts.URL)
		}
		if got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
	if n := streamGets.Load(); n != 0 {
		t.Errorf("the transport stream was opened %d times", n)
	}
	if got := strings.Join(chooseRecordingStreams(context.Background(), "udp://239.0.0.1:1234", recordingStreamChoice{}, false).args(), " "); got != "-map 0" {
		t.Errorf("udp source: %q", got)
	}
}

// TestRecordingStreamsKeepLayout: once a recording has data, a retry finds its variant again by
// URL wherever the master now lists it, and keeps its choice when it cannot tell.
func TestRecordingStreamsKeepLayout(t *testing.T) {
	var master atomic.Value
	var requests atomic.Int64
	master.Store("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nlow.m3u8?t=1\n#EXT-X-STREAM-INF:BANDWIDTH=9\nhigh.m3u8?t=1\n")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/master.m3u8":
			body := master.Load().(string)
			if body == "" {
				http.Error(w, "down", http.StatusServiceUnavailable)
				return
			}
			_, _ = fmt.Fprint(w, body)
		case "/low.m3u8", "/high.m3u8", "/new.m3u8":
			_, _ = fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2,\ns.ts\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	ctx := context.Background()
	source := ts.URL + "/master.m3u8"
	join := func(c recordingStreamChoice) string { return strings.Join(c.args(), " ") }
	// FFmpeg reads the chosen variant's own playlist, never the master (which may change).
	from := func(c recordingStreamChoice, variant string) bool {
		return join(c) == "-map 0:v -map 0:a?" && c.variant == ts.URL+variant && len(c.inputs) == 1 && c.inputs[0] == c.variant
	}
	first := chooseRecordingStreams(ctx, source, recordingStreamChoice{}, false)
	if !from(first, "/high.m3u8?t=1") {
		t.Fatalf("first: %q %q", join(first), first.variant)
	}
	if n := requests.Load(); n != 2 {
		t.Errorf("%d requests for the first choice, want 2 (master and variant, no HEAD for a .m3u8)", n)
	}
	// The master now lists a better variant first and the chosen one second, with new tokens.
	master.Store("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=20\nnew.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=9\nhigh.m3u8?t=2\n#EXT-X-STREAM-INF:BANDWIDTH=1\nlow.m3u8?t=2\n")
	retry := chooseRecordingStreams(ctx, source, first, true)
	if !from(retry, "/high.m3u8?t=2") {
		t.Errorf("retry with data: %q, want the same variant at its new position", join(retry))
	}
	if fresh := chooseRecordingStreams(ctx, source, first, false); !from(fresh, "/high.m3u8?t=2") {
		t.Errorf("retry without data: %q, want the previous variant while it loads", join(fresh))
	}
	// The master is down: keep the choice once there is data, everything before.
	master.Store("")
	if kept := chooseRecordingStreams(ctx, source, retry, true); join(kept) != join(retry) {
		t.Errorf("master down with data: %q", join(kept))
	}
	if got := chooseRecordingStreams(ctx, source, retry, false); join(got) != "-map 0" {
		t.Errorf("master down without data: %q", join(got))
	}
	// All streams recorded, or FFmpeg found nothing in the variant: no more lookups.
	before := requests.Load()
	if got := chooseRecordingStreams(ctx, source, allRecordingStreams(), true); got.variant != "" || join(got) != "-map 0" {
		t.Errorf("all streams with data: %+v", got)
	}
	final := recordingStreamChoice{maps: recordingAllStreams, final: true}
	if got := chooseRecordingStreams(ctx, source, final, false); !got.final {
		t.Errorf("final: %+v", got)
	}
	// A file named like media is no playlist: no request at all, and none on retries.
	if got := chooseRecordingStreams(ctx, ts.URL+"/live/123.ts?token=x", recordingStreamChoice{}, false); !got.final || join(got) != "-map 0" {
		t.Errorf("media extension: %+v", got)
	}
	if n := requests.Load() - before; n != 0 {
		t.Errorf("%d requests for a fixed layout", n)
	}

	// The chosen variant is gone: with data, only an identical one (the same picture from
	// another CDN) may follow; a different rung of the ladder keeps the previous choice.
	master.Store("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=20,RESOLUTION=1920x1080\nnew.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=1\nlow.m3u8\n")
	if kept := chooseRecordingStreams(ctx, source, retry, true); join(kept) != join(retry) || kept.variant != retry.variant {
		t.Errorf("another rung with data: %q %q, want the previous choice", join(kept), kept.variant)
	}
	master.Store("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nlow.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=9\nnew.m3u8\n")
	if moved := chooseRecordingStreams(ctx, source, retry, true); !from(moved, "/new.m3u8") {
		t.Errorf("identical variant from another CDN: %q %q", join(moved), moved.variant)
	}
	// The variant moved and its playlist does not load right now: still its new position (FFmpeg
	// retries it), not the old one.
	gone := recordingStreamChoice{maps: []string{"-map", "0:p:0:v", "-map", "0:p:0:a?"}, variant: ts.URL + "/gone.m3u8", signature: "9|||0", playlist: source}
	master.Store("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nlow.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=5\nnew.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=9\ngone.m3u8\n")
	if got := chooseRecordingStreams(ctx, source, gone, true); !from(got, "/gone.m3u8") {
		t.Errorf("moved variant whose playlist fails: %q", join(got))
	}
	// Its CDN is down but still listed, and the same picture comes from another one: that one.
	master.Store("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=9\ngone.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=9\nhigh.m3u8\n")
	if got := chooseRecordingStreams(ctx, source, gone, true); !from(got, "/high.m3u8") {
		t.Errorf("CDN down: %q, want the same picture from the other CDN", join(got))
	}
	// Variants that differ only in their query: the URL without query is no identity.
	master.Store("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=9\nlow.m3u8?q=hi\n#EXT-X-STREAM-INF:BANDWIDTH=5\nlow.m3u8?q=mid\n")
	prev := recordingStreamChoice{maps: []string{"-map", "0:p:7:v"}, variant: ts.URL + "/low.m3u8?q=hi&t=1", signature: "9|||0", playlist: source}
	if got := chooseRecordingStreams(ctx, source, prev, true); !from(got, "/low.m3u8?q=hi") {
		t.Errorf("query ladder: %q, want the variant with the same signature", join(got))
	}
	prev.signature = "7|||0"
	if got := chooseRecordingStreams(ctx, source, prev, true); join(got) != "-map 0:p:7:v" {
		t.Errorf("query ladder without a match: %q, want the previous choice", join(got))
	}
}

// TestSameHLSVariantPrefersSignature: one picture listed with two audio groups; the retry takes
// the entry with the previous audio layout, whatever the ranking.
func TestSameHLSVariantPrefersSignature(t *testing.T) {
	variants := []hlsVariant{
		{program: 1, uri: "http://x/v.m3u8", signature: "9|||1"},
		{program: 0, uri: "http://x/v.m3u8", signature: "8|||3"},
	}
	got, ok := sameHLSVariant(variants, recordingStreamChoice{variant: "http://x/v.m3u8", signature: "8|||3"})
	if !ok || got.program != 0 {
		t.Errorf("got program %d %v, want 0", got.program, ok)
	}
}

// TestHLSVariantSignature: the number of audio tracks is part of a variant's layout.
func TestHLSVariantSignature(t *testing.T) {
	variants := rankedHLSVariants("#EXTM3U\n" +
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",NAME="1",URI="a1.m3u8"` + "\n" +
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",NAME="2",URI="a2.m3u8"` + "\n" +
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="b",NAME="1",URI="b1.m3u8"` + "\n" +
		`#EXT-X-STREAM-INF:BANDWIDTH=9,AUDIO="a"` + "\nv1.m3u8\n" +
		`#EXT-X-STREAM-INF:BANDWIDTH=9,AUDIO="b"` + "\nv2.m3u8\n")
	if len(variants) != 2 || variants[0].signature == variants[1].signature {
		t.Fatalf("signatures: %+v", variants)
	}
}

// TestRecordingStreamsPlaylistProbe: a source that is not a playlist, or does not answer HEAD,
// is recorded whole and not looked at again.
func TestRecordingStreamsPlaylistProbe(t *testing.T) {
	var heads atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			heads.Add(1)
		}
		switch r.URL.Path {
		case "/tv":
			w.Header().Set("Content-Type", "video/mp2t")
		case "/nohead":
			w.WriteHeader(http.StatusMethodNotAllowed)
		case "/gone":
			w.WriteHeader(http.StatusNotFound)
		case "/limited":
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer ts.Close()
	ctx := context.Background()
	tv := chooseRecordingStreams(ctx, ts.URL+"/tv", recordingStreamChoice{}, false)
	if !tv.final || strings.Join(tv.args(), " ") != "-map 0" {
		t.Errorf("transport stream: %+v", tv)
	}
	if again := chooseRecordingStreams(ctx, ts.URL+"/tv", tv, false); heads.Load() != 1 || !again.final {
		t.Errorf("transport stream retry: %d HEAD requests, %+v", heads.Load(), again)
	}
	// The next recording of the channel asks nothing either.
	if next := chooseRecordingStreams(ctx, ts.URL+"/tv", recordingStreamChoice{}, false); heads.Load() != 1 || !next.final {
		t.Errorf("next recording: %d HEAD requests, %+v", heads.Load(), next)
	}
	// A server that does not answer HEAD, at a URL not named like a playlist: everything, and
	// not asked again on the next attempt.
	busy := chooseRecordingStreams(ctx, ts.URL+"/busy", recordingStreamChoice{}, false)
	if !busy.final || strings.Join(busy.args(), " ") != "-map 0" {
		t.Errorf("unanswered HEAD: %+v", busy)
	}
	if chooseRecordingStreams(ctx, ts.URL+"/busy", busy, false); heads.Load() != 2 {
		t.Errorf("%d HEAD requests, want 2", heads.Load())
	}
	// An overloaded server found nothing out: the next recording asks again. One that refuses
	// HEAD did: the next recording asks nothing.
	if chooseRecordingStreams(ctx, ts.URL+"/busy", recordingStreamChoice{}, false); heads.Load() != 3 {
		t.Errorf("next recording after a 503: %d HEAD requests, want 3", heads.Load())
	}
	for _, path := range []string{"/gone", "/limited"} {
		chooseRecordingStreams(ctx, ts.URL+path, recordingStreamChoice{}, false)
		if notAdaptiveRecently(ts.URL + path) {
			t.Errorf("%s: a transient HEAD answer was remembered", path)
		}
	}
	heads.Store(3)
	chooseRecordingStreams(ctx, ts.URL+"/nohead", recordingStreamChoice{}, false)
	if next := chooseRecordingStreams(ctx, ts.URL+"/nohead", recordingStreamChoice{}, false); heads.Load() != 4 || !next.final {
		t.Errorf("next recording after a 405: %d HEAD requests, want 4", heads.Load())
	}
}

// TestRecordingTakesBestVariantEndToEnd records a ZDF-like channel (the same picture from two
// CDNs, each with two audio renditions) with FFmpeg: one picture and its two audio tracks, not
// two pictures and four tracks; with the first CDN down, the second one's. Runs only when
// MEDIASTORM_FFMPEG_E2E=1.
func TestRecordingTakesBestVariantEndToEnd(t *testing.T) {
	if os.Getenv("MEDIASTORM_FFMPEG_E2E") != "1" {
		t.Skip("set MEDIASTORM_FFMPEG_E2E=1 to run against ffmpeg")
	}
	src := t.TempDir()
	for _, cdn := range []string{"a", "b"} {
		dir := filepath.Join(src, cdn)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		gen := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
			"-t", "8", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25",
			"-t", "8", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
			"-t", "8", "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000",
			"-map", "0:v", "-c:v", "libx264", "-preset", "ultrafast", "-g", "50",
			"-f", "hls", "-hls_time", "2", "-hls_list_size", "0", "-hls_playlist_type", "vod", "-hls_segment_filename", filepath.Join(dir, "v%d.ts"), filepath.Join(dir, "v.m3u8"),
			"-map", "1:a", "-c:a", "aac", "-f", "hls", "-hls_time", "2", "-hls_list_size", "0", "-hls_playlist_type", "vod", "-hls_segment_filename", filepath.Join(dir, "d%d.ts"), filepath.Join(dir, "deu.m3u8"),
			"-map", "2:a", "-c:a", "aac", "-f", "hls", "-hls_time", "2", "-hls_list_size", "0", "-hls_playlist_type", "vod", "-hls_segment_filename", filepath.Join(dir, "o%d.ts"), filepath.Join(dir, "orig.m3u8"))
		if out, err := gen.CombinedOutput(); err != nil {
			t.Fatalf("generate %s: %v %s", cdn, err, out)
		}
	}
	master := "#EXTM3U\n"
	for _, cdn := range []string{"a", "b"} {
		master += fmt.Sprintf("#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"%[1]s\",NAME=\"TV Ton\",LANGUAGE=\"deu\",DEFAULT=YES,URI=\"%[1]s/deu.m3u8\"\n"+
			"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"%[1]s\",NAME=\"Originalton\",LANGUAGE=\"mul\",URI=\"%[1]s/orig.m3u8\"\n", cdn)
	}
	for _, cdn := range []string{"a", "b"} {
		master += fmt.Sprintf("#EXT-X-STREAM-INF:BANDWIDTH=4504154,CODECS=\"avc1.42c00d,mp4a.40.2\",AUDIO=\"%[1]s\"\n%[1]s/v.m3u8\n", cdn)
	}
	if err := os.WriteFile(filepath.Join(src, "master.m3u8"), []byte(master), 0o644); err != nil {
		t.Fatal(err)
	}
	var cdnADown atomic.Bool
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cdnADown.Load() && strings.HasPrefix(r.URL.Path, "/a/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".m3u8") {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		}
		http.FileServer(http.Dir(src)).ServeHTTP(w, r)
	}))
	defer files.Close()
	sourceURL := files.URL + "/master.m3u8"

	record := func() (video, audio int, languages string) {
		out := filepath.Join(t.TempDir(), "rec.ts")
		args := append([]string{"-nostdin", "-loglevel", "error", "-i", sourceURL}, chooseRecordingStreams(context.Background(), sourceURL, recordingStreamChoice{}, false).args()...)
		args = append(args, "-c", "copy", "-f", "mpegts", out)
		if res, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
			t.Fatalf("record: %v %s", err, res)
		}
		// ffprobe lists an MPEG-TS file's streams under its program and again on their own:
		// count each stream index once.
		probe, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=index,codec_type:stream_tags=language", "-of", "csv=p=0", out).Output()
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, line := range strings.Fields(string(probe)) {
			index, kind, _ := strings.Cut(line, ",")
			if seen[index] {
				continue
			}
			seen[index] = true
			switch {
			case strings.HasPrefix(kind, "video"):
				video++
			case strings.HasPrefix(kind, "audio"):
				audio++
			}
		}
		return video, audio, string(probe)
	}
	if video, audio, languages := record(); video != 1 || audio != 2 || !strings.Contains(languages, "deu") || !strings.Contains(languages, "mul") {
		t.Fatalf("recorded %d video and %d audio streams, want 1 and 2 (deu, mul):\n%s", video, audio, languages)
	}
	cdnADown.Store(true)
	if video, audio, languages := record(); video != 1 || audio != 2 {
		t.Fatalf("with CDN A down: %d video and %d audio streams, want 1 and 2:\n%s", video, audio, languages)
	}
}

// A variant with audio renditions: FFmpeg gets the variant's media playlist and each rendition of
// its group as inputs, with a map per input; renditions of other groups stay out.
func TestVariantStreamsReadTheVariantsOwnPlaylists(t *testing.T) {
	body := "#EXTM3U\n" +
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",NAME="deu",URI="a/deu.m3u8"` + "\n" +
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",NAME="AD",URI="a/ad.m3u8"` + "\n" +
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="b",NAME="deu",URI="b/deu.m3u8"` + "\n" +
		`#EXT-X-STREAM-INF:BANDWIDTH=9,RESOLUTION=1280x720,AUDIO="b"` + "\nb/v.m3u8\n" +
		`#EXT-X-STREAM-INF:BANDWIDTH=9,RESOLUTION=1280x720,AUDIO="a"` + "\na/v.m3u8\n"
	master, _ := url.Parse("http://cdn.example/live/master.m3u8")
	variants := rankedHLSVariants(body)
	var chosen hlsVariant
	for _, v := range variants {
		if v.uri == "a/v.m3u8" {
			chosen = v
		}
	}
	chosen.uri = master.ResolveReference(&url.URL{Path: chosen.uri}).String()
	got := variantStreams(body, master, chosen)
	wantInputs := "http://cdn.example/live/a/v.m3u8 http://cdn.example/live/a/deu.m3u8 http://cdn.example/live/a/ad.m3u8"
	if strings.Join(got.inputs, " ") != wantInputs {
		t.Errorf("inputs %q, want %q", got.inputs, wantInputs)
	}
	if maps := strings.Join(got.args(), " "); maps != "-map 0:v -map 0:a? -map 1:a? -map 2:a?" {
		t.Errorf("maps %q", maps)
	}
	if strings.Contains(strings.Join(got.args(), " "), ":p:") {
		t.Error("maps by program position: the master's order may change before FFmpeg reads it")
	}
}
