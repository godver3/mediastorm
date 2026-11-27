package recordings

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"novastream/models"
)

// livePlaylist is a live media playlist of n two-second segments whose last one ends at end; dated
// gives each segment its program date-time, shifted by clockOffset.
func livePlaylist(n int, end time.Time, dated bool, clockOffset time.Duration) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:1000\n")
	start := end.Add(-time.Duration(n) * 2 * time.Second)
	for i := 0; i < n; i++ {
		if dated {
			fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n", start.Add(time.Duration(i)*2*time.Second+clockOffset).Format("2006-01-02T15:04:05.000Z07:00"))
		}
		fmt.Fprintf(&b, "#EXTINF:2.000,\nseg%d.ts\n", 1000+i)
	}
	return b.String()
}

func TestBackfillFromPlaylist(t *testing.T) {
	now := time.Date(2026, 11, 26, 19, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		playlist   string
		want       time.Time
		index      int
		from       time.Time
		noBackfill bool
	}{
		// The segment holding want is the 70th of 100 (it begins 60 s before the end); three more
		// (6 s) for the playlist FFmpeg reads a moment later.
		{name: "dated", playlist: livePlaylist(100, now, true, 0), want: now.Add(-59 * time.Second), index: -33, from: now.Add(-66 * time.Second)},
		{name: "undated", playlist: livePlaylist(100, now, false, 0), want: now.Add(-59 * time.Second), index: -33, from: now.Add(-66 * time.Second)},
		{name: "dates an hour off the clock", playlist: livePlaylist(100, now, true, time.Hour), want: now.Add(-59 * time.Second), index: -33, from: now.Add(-66 * time.Second)},
		{name: "dates a little behind the clock", playlist: livePlaylist(100, now, true, -20*time.Second), want: now.Add(-79 * time.Second), index: -33, from: now.Add(-86 * time.Second)},
		{name: "window too short: its oldest segment", playlist: livePlaylist(100, now, true, 0), want: now.Add(-time.Hour), index: -103, from: now.Add(-200 * time.Second)},
		{name: "at the live edge", playlist: livePlaylist(100, now, true, 0), want: now.Add(-time.Second), noBackfill: true},
		{name: "finished", playlist: livePlaylist(100, now, true, 0) + "#EXT-X-ENDLIST\n", want: now.Add(-time.Minute), noBackfill: true},
		{name: "master", playlist: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv.m3u8\n", want: now.Add(-time.Minute), noBackfill: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := backfillFromPlaylist(tc.playlist, now, tc.want)
			if tc.noBackfill {
				if got.active() {
					t.Fatalf("backfill %+v, want none", got)
				}
				return
			}
			if got.liveStartIndex != tc.index || !got.from.Equal(tc.from) || got.behind != now.Sub(tc.from) {
				t.Fatalf("backfill index %d from %s behind %s, want %d from %s", got.liveStartIndex, got.from, got.behind, tc.index, tc.from)
			}
		})
	}
}

// ZDF's playlists date every segment (UTC, whole seconds) and mark a discontinuity; a segment
// without a date continues from the one before.
func TestBackfillFromPlaylistCarriesDates(t *testing.T) {
	now := time.Date(2026, 11, 26, 18, 1, 49, 0, time.UTC)
	playlist := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:1348860\n#EXT-X-DISCONTINUITY-SEQUENCE:179\n\n"
	for i := 0; i < 60; i++ {
		if i%10 == 0 {
			playlist += "#EXT-X-PROGRAM-DATE-TIME:" + now.Add(time.Duration(i-60)*2*time.Second).Format("2006-01-02T15:04:05Z") + "\n"
		}
		if i == 30 {
			playlist += "#EXT-X-DISCONTINUITY\n"
		}
		playlist += fmt.Sprintf("#EXTINF:2.000,\n%d.ts\n\n", 1348860+i)
	}
	got := backfillFromPlaylist(playlist, now.Add(3*time.Second), now.Add(-41*time.Second))
	// want is in the 40th segment (it begins 42 s before the end), plus three.
	if got.liveStartIndex != -24 || !got.from.Equal(now.Add(-48*time.Second)) {
		t.Fatalf("backfill index %d from %s, want -24 from %s", got.liveStartIndex, got.from, now.Add(-48*time.Second))
	}
}

func TestParseProgramDateTime(t *testing.T) {
	want := time.Date(2026, 11, 26, 16, 1, 47, 820e6, time.UTC)
	for _, value := range []string{"2026-11-26T17:01:47.820+01:00", "2026-11-26T17:01:47.820+0100", "2026-11-26T16:01:47.820Z"} {
		if got := parseProgramDateTime(value); !got.Equal(want) {
			t.Errorf("parseProgramDateTime(%q) = %s, want %s", value, got, want)
		}
	}
	if got := parseProgramDateTime("yesterday"); !got.IsZero() {
		t.Errorf("unreadable date = %s, want zero", got)
	}
}

func TestPlanRecordingBackfill(t *testing.T) {
	var requests atomic.Int32
	end := time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/single.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1,RESOLUTION=640x360\nmedia/v.m3u8\n")
		case "/media/v.m3u8", "/live.m3u8", "/variant.m3u8":
			fmt.Fprint(w, livePlaylist(100, end, true, 0))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	want := end.Add(-59 * time.Second)

	if got, _ := planRecordingBackfill(ctx, srv.URL+"/live.m3u8", allRecordingStreams(), time.Now().Add(-10*time.Second)); got.active() || requests.Load() != 0 {
		t.Fatalf("on time: backfill %+v after %d requests, want none without asking", got, requests.Load())
	}
	if got, _ := planRecordingBackfill(ctx, srv.URL+"/stream/zdf", allRecordingStreams(), want); got.active() || requests.Load() != 0 {
		t.Fatalf("a URL that names no playlist and no chosen variant: backfill %+v after %d requests, want none without asking", got, requests.Load())
	}
	for _, tc := range []struct {
		name, source string
		streams      recordingStreamChoice
	}{
		{"media playlist", srv.URL + "/live.m3u8", allRecordingStreams()},
		{"master with one variant", srv.URL + "/single.m3u8", allRecordingStreams()},
		{"chosen variant", srv.URL + "/stream/zdf", recordingStreamChoice{variant: srv.URL + "/variant.m3u8"}},
	} {
		got, err := planRecordingBackfill(ctx, tc.source, tc.streams, want)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if got.liveStartIndex != -33 && got.liveStartIndex != -34 {
			t.Errorf("%s: live_start_index %d, want -33", tc.name, got.liveStartIndex)
		}
	}
}

// fakeFFmpegPrelude records each attempt's arguments (attempts separated by "--") and defines
// report N, which writes FFmpeg's progress (out_time_us) when it was asked for (-progress pipe:3),
// and limit_us, the -t limit in microseconds.
const fakeFFmpegPrelude = `#!/bin/sh
printf '%s\n' "$@" >> "$ARGS_FILE"
printf -- '--\n' >> "$ARGS_FILE"
limit=""; prev=""
for a in "$@"; do [ "$prev" = "-t" ] && limit=$a; prev=$a; done
case "$*" in *-progress*) progress=1 ;; *) progress="" ;; esac
report() { if [ -n "$progress" ]; then printf 'out_time_us=%s\nprogress=continue\n' "$1" >&3; fi; }
limit_us=$(awk "BEGIN{printf \"%d\", ${limit:-0}*1000000}")
`

// lateRecordingSetup serves a live playlist of 900 two-second segments and makes a recording that
// should have started back minutes ago and stops at stopAt. The playlist requests numbered in
// failRequests (from 1) are answered with an error.
func lateRecordingSetup(t *testing.T, script string, back time.Duration, stopAt time.Time, failRequests ...int32) (*Service, models.Recording, string) {
	t.Helper()
	end := time.Now()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		for _, fail := range failRequests {
			if n == fail {
				http.Error(w, "busy", http.StatusServiceUnavailable)
				return
			}
		}
		fmt.Fprint(w, livePlaylist(900, end, true, 0))
	}))
	t.Cleanup(srv.Close)
	originalDelay := recordingRetryDelay
	recordingRetryDelay = 10 * time.Millisecond
	t.Cleanup(func() { recordingRetryDelay = originalDelay })
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("ARGS_FILE", argsFile)
	recording := newTestRecording(stopAt)
	recording.SourceURL = srv.URL + "/live.m3u8"
	recording.StartAt = time.Now().UTC().Add(-back)
	svc := newTestService(newFakeRecordingRepo(recording), writeFakeFFmpeg(t, fakeFFmpegPrelude+script), t.TempDir())
	svc.ctx = context.Background()
	return svc, recording, argsFile
}

func recordedAttempts(t *testing.T, argsFile string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	var attempts [][]string
	for _, attempt := range strings.Split(strings.TrimSuffix(string(raw), "--\n"), "--\n") {
		attempts = append(attempts, strings.Split(strings.TrimSpace(attempt), "\n"))
	}
	return attempts
}

func finishedRecording(t *testing.T, svc *Service, id string) *models.Recording {
	t.Helper()
	latest, err := svc.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Status != models.RecordingStatusCompleted {
		t.Fatalf("Status = %q, want completed (error=%q)", latest.Status, latest.Error)
	}
	return latest
}

// A recording created while its programme is on starts in the source's window: FFmpeg begins
// that far back and stops when its content reaches the stop time, after the wall clock did. The
// recording's content start is where it began in the source; it ends at the stop time.
func TestLateRecordingLooksBackInTheSourceWindow(t *testing.T) {
	stopAt := time.Now().UTC().Add(time.Second)
	svc, recording, argsFile := lateRecordingSetup(t, `printf 'caught-up'
report $((limit_us / 2))
sleep 1.5
report $limit_us
exit 0
`, 10*time.Minute, stopAt)
	recording.PaddingBeforeSeconds = 60
	svc.repo.(*fakeRecordingRepo).recordings[recording.ID] = recording

	svc.startRecording(recording)

	latest := finishedRecording(t, svc, recording.ID)
	wantStart := recording.StartAt.Add(-time.Minute)
	if latest.ActualStartAt == nil || latest.ActualStartAt.After(wantStart) || latest.ActualStartAt.Before(wantStart.Add(-10*time.Second)) {
		t.Fatalf("ActualStartAt = %v, want just before %s", latest.ActualStartAt, wantStart)
	}
	if latest.ActualEndAt == nil || absDuration(latest.ActualEndAt.Sub(stopAt)) > 10*time.Millisecond {
		t.Fatalf("ActualEndAt = %v, want the stop time %s (where the content ends)", latest.ActualEndAt, stopAt)
	}
	attempts := recordedAttempts(t, argsFile)
	if len(attempts) != 1 {
		t.Fatalf("%d attempts, want 1: %q", len(attempts), attempts)
	}
	args := attempts[0]
	index, limit := argAfter(args, "-live_start_index"), argAfter(args, "-t")
	// 11 minutes back in two-second segments, and three more.
	if index != "-333" && index != "-334" {
		t.Errorf("-live_start_index %q, want -333", index)
	}
	if limit == "" || !strings.HasPrefix(limit, "66") {
		t.Errorf("-t %q, want about 666 s (from the content start to the stop time)", limit)
	}
	if indexOf(args, "-live_start_index") > indexOf(args, "-i") || indexOf(args, "-t") < indexOf(args, "-i") || argAfter(args, "-progress") != "pipe:3" {
		t.Errorf("args %q: -live_start_index belongs to the input, -t to the output, and FFmpeg reports its progress", args)
	}
	// A source recorded whole (-map 0) is taken without streams that may have no packets.
	if !strings.Contains(strings.Join(args, " "), "-map 0:v? -map 0:a?") || argAfter(args, "-map") == "0" {
		t.Errorf("args %q: want picture and sound only (-map 0:v? -map 0:a?)", args)
	}
}

// A recording that starts on time records as before: no look back, no -t, no progress reports.
func TestOnTimeRecordingIsUnchanged(t *testing.T) {
	stopAt := time.Now().UTC().Add(time.Second)
	svc, recording, argsFile := lateRecordingSetup(t, `printf 'live'
trap 'exit 255' INT
while :; do sleep 0.05; done
`, 5*time.Second, stopAt)

	svc.startRecording(recording)

	finishedRecording(t, svc, recording.ID)
	for _, args := range recordedAttempts(t, argsFile) {
		for _, flag := range []string{"-live_start_index", "-t", "-progress"} {
			if indexOf(args, flag) >= 0 {
				t.Fatalf("on-time recording args %q carry %s", args, flag)
			}
		}
		if argAfter(args, "-map") != "0" {
			t.Fatalf("on-time recording args %q, want -map 0", args)
		}
	}
}

// The source's window failing to load when a backfilled recording continues is asked again, not
// given up for live (which would leave the rest out).
func TestLateRecordingAsksTheWindowAgain(t *testing.T) {
	stopAt := time.Now().UTC().Add(2 * time.Second)
	// Requests: 1 the stream choice, 2 the backfill, 3 the first continuation (fails).
	svc, recording, argsFile := lateRecordingSetup(t, `printf 'media'
count=$(grep -c -- '^--$' "$ARGS_FILE")
if [ "$count" = 1 ]; then report 60000000; exit 0; fi
report $limit_us
exit 0
`, 5*time.Minute, stopAt, 3)

	svc.startRecording(recording)

	finishedRecording(t, svc, recording.ID)
	attempts := recordedAttempts(t, argsFile)
	if len(attempts) != 2 || indexOf(attempts[1], "-live_start_index") < 0 {
		t.Fatalf("attempts %q, want the second to continue from the window", attempts)
	}
}

// Content that reaches the stop time minutes before the clock does had its timestamps jump: not
// done, the recording carries on live.
func TestLateRecordingDoesNotTrustAJumpToTheEnd(t *testing.T) {
	stopAt := time.Now().UTC().Add(10 * time.Minute)
	svc, recording, argsFile := lateRecordingSetup(t, `printf 'media'
case "$*" in *-live_start_index*) report $limit_us; exit 0 ;; esac
trap 'exit 255' INT
while :; do sleep 0.05; done
`, 5*time.Minute, stopAt)
	ctx, cancel := context.WithCancel(context.Background())
	svc.ctx = ctx
	go func() {
		for {
			time.Sleep(50 * time.Millisecond)
			raw, _ := os.ReadFile(os.Getenv("ARGS_FILE"))
			if strings.Count(string(raw), "--\n") >= 2 {
				cancel()
				return
			}
		}
	}()

	svc.startRecording(recording)

	attempts := recordedAttempts(t, argsFile)
	if len(attempts) != 2 || indexOf(attempts[1], "-live_start_index") >= 0 || indexOf(attempts[1], "-t") >= 0 {
		t.Fatalf("attempts %q, want a live one after the jump", attempts)
	}
}

// A media playlist too long to read whole is not used: a cut one would place the start wrongly.
func TestPlanRecordingBackfillRefusesACutPlaylist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, livePlaylist(900, time.Now(), true, 0))
		fmt.Fprint(w, strings.Repeat("#EXT-X-COMMENT\n", recordingBackfillPlaylistLimit/15+1))
	}))
	defer srv.Close()
	got, err := planRecordingBackfill(context.Background(), srv.URL+"/live.m3u8", allRecordingStreams(), time.Now().Add(-time.Minute))
	if got.active() || err == nil {
		t.Fatalf("backfill %+v, err %v: want none from a playlist over the limit", got, err)
	}
}

// Content that reached the stop time a moment before the wall clock did (an undated playlist
// counted from its end, a small timestamp jump) completed the recording: no retry, no failure.
func TestLateRecordingCompletesWhenItsContentArrivesEarly(t *testing.T) {
	stopAt := time.Now().UTC().Add(3 * time.Second)
	svc, recording, argsFile := lateRecordingSetup(t, `printf 'all-of-it'
report $limit_us
exit 0
`, 5*time.Minute, stopAt)

	svc.startRecording(recording)

	latest := finishedRecording(t, svc, recording.ID)
	if attempts := recordedAttempts(t, argsFile); len(attempts) != 1 {
		t.Fatalf("%d attempts, want 1: %q", len(attempts), attempts)
	}
	if latest.ActualEndAt == nil || absDuration(latest.ActualEndAt.Sub(stopAt)) > 10*time.Millisecond {
		t.Fatalf("ActualEndAt = %v, want the stop time %s (where its content ends)", latest.ActualEndAt, stopAt)
	}
}

// A backfilled attempt that ends before its content reached the stop time is continued where its
// content ended, still from the source's window, not live.
func TestLateRecordingContinuesInTheWindowAfterAGap(t *testing.T) {
	stopAt := time.Now().UTC().Add(2 * time.Second)
	svc, recording, argsFile := lateRecordingSetup(t, `printf 'media'
count=$(grep -c -- '^--$' "$ARGS_FILE")
if [ "$count" = 1 ]; then report 60000000; exit 0; fi
report $limit_us
exit 0
`, 5*time.Minute, stopAt)

	svc.startRecording(recording)

	finishedRecording(t, svc, recording.ID)
	attempts := recordedAttempts(t, argsFile)
	if len(attempts) != 2 {
		t.Fatalf("%d attempts, want 2: %q", len(attempts), attempts)
	}
	first, _ := strconv.ParseFloat(argAfter(attempts[0], "-t"), 64)
	second, _ := strconv.ParseFloat(argAfter(attempts[1], "-t"), 64)
	// The second starts 60 s of content later (and up to the lead earlier).
	if argAfter(attempts[1], "-live_start_index") == "" || second < first-61 || second > first-50 {
		t.Fatalf("second attempt %q (-t %.0f after %.0f), want it to carry on 60 s in from the window", attempts[1], second, first)
	}
}

// A backfilled start that records nothing is not tried again: the recording carries on live,
// from where it is.
func TestLateRecordingGoesLiveWhenTheBackfillRecordsNothing(t *testing.T) {
	stopAt := time.Now().UTC().Add(1500 * time.Millisecond)
	svc, recording, argsFile := lateRecordingSetup(t, `case "$*" in *-live_start_index*) exit 1 ;; esac
printf 'live'
trap 'exit 255' INT
while :; do sleep 0.05; done
`, 5*time.Minute, stopAt)
	started := time.Now()

	svc.startRecording(recording)

	latest := finishedRecording(t, svc, recording.ID)
	attempts := recordedAttempts(t, argsFile)
	for i, args := range attempts[1:] {
		if indexOf(args, "-live_start_index") >= 0 || indexOf(args, "-t") >= 0 || indexOf(args, "-progress") >= 0 {
			t.Fatalf("attempt %d %q looks back again or is still followed", i+2, args)
		}
	}
	if len(attempts) < 2 || indexOf(attempts[0], "-live_start_index") < 0 {
		t.Fatalf("attempts %q, want a backfilled one, then live ones", attempts)
	}
	if latest.ActualStartAt == nil || latest.ActualStartAt.Before(started) {
		t.Fatalf("ActualStartAt = %v, want when it went live (after %s)", latest.ActualStartAt, started)
	}
}

// FFmpeg kept running after its content reached the stop time (a mapped stream without packets
// past -t): the recorder stops it shortly after, not at the extended deadline.
func TestLateRecordingStopsFFmpegThatOutlivesItsLimit(t *testing.T) {
	originalGrace := recordingBackfillReachGrace
	recordingBackfillReachGrace = 300 * time.Millisecond
	defer func() { recordingBackfillReachGrace = originalGrace }()
	stopAt := time.Now().UTC().Add(time.Second)
	svc, recording, _ := lateRecordingSetup(t, `printf 'media'
sleep 1.2
report $limit_us
trap 'exit 255' INT
while :; do sleep 0.05; done
`, 5*time.Minute, stopAt)
	started := time.Now()

	svc.startRecording(recording)

	finishedRecording(t, svc, recording.ID)
	if took := time.Since(started); took > 5*time.Second {
		t.Fatalf("recording took %s, want it stopped shortly after its content reached the stop time", took)
	}
}

// While it catches up, the service tells how much content the recording holds.
func TestRecordedContentFollowsTheProgress(t *testing.T) {
	stopAt := time.Now().UTC().Add(2 * time.Second)
	svc, recording, _ := lateRecordingSetup(t, `printf 'media'
report 90000000
sleep 1
report $limit_us
exit 0
`, 5*time.Minute, stopAt)
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.startRecording(recording)
	}()
	deadline := time.Now().Add(900 * time.Millisecond)
	var seen time.Duration
	for time.Now().Before(deadline) && seen == 0 {
		seen, _ = svc.RecordedContent(recording.ID)
		time.Sleep(20 * time.Millisecond)
	}
	<-done
	if seen != 90*time.Second {
		t.Fatalf("RecordedContent = %s while running, want 1m30s", seen)
	}
	if _, ok := svc.RecordedContent(recording.ID); ok {
		t.Fatal("RecordedContent known after the recording finished")
	}
}

func argAfter(args []string, name string) string {
	if i := indexOf(args, name); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

func indexOf(args []string, name string) int {
	for i, arg := range args {
		if arg == name {
			return i
		}
	}
	return -1
}

// TestLateRecordingBackfillEndToEnd records a live HLS channel whose window holds the last 80 s,
// growing by a two-second segment every two seconds, as FFmpeg sees it: a recording that should
// have started 40 s ago holds those 40 s and the rest up to its stop time, not only what came
// after the recorder started. Runs only when MEDIASTORM_FFMPEG_E2E=1.
func TestLateRecordingBackfillEndToEnd(t *testing.T) {
	if os.Getenv("MEDIASTORM_FFMPEG_E2E") != "1" {
		t.Skip("set MEDIASTORM_FFMPEG_E2E=1 to run against ffmpeg")
	}
	dir := t.TempDir()
	gen := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "120", "-c:v", "libx264", "-preset", "ultrafast", "-g", "50", "-keyint_min", "50", "-sc_threshold", "0", "-c:a", "aac",
		"-f", "hls", "-hls_time", "2", "-hls_list_size", "0", "-hls_segment_filename", filepath.Join(dir, "seg%d.ts"), filepath.Join(dir, "all.m3u8"))
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate: %v %s", err, out)
	}
	serverStart := time.Now()
	t0 := serverStart.Add(-80 * time.Second) // segment i begins at t0 + 2i: the 40th ends now
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/live.m3u8" {
			http.ServeFile(w, r, filepath.Join(dir, filepath.Base(r.URL.Path)))
			return
		}
		last := min(39+int(time.Since(serverStart)/(2*time.Second)), 59)
		first := max(last-39, 0)
		fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:%d\n", first)
		for i := first; i <= last; i++ {
			fmt.Fprintf(w, "#EXT-X-PROGRAM-DATE-TIME:%s\n#EXTINF:2.000,\nseg%d.ts\n", t0.Add(time.Duration(i)*2*time.Second).UTC().Format(time.RFC3339Nano), i)
		}
	}))
	defer srv.Close()

	stopAt := time.Now().UTC().Add(6 * time.Second)
	recording := newTestRecording(stopAt)
	recording.SourceURL = srv.URL + "/live.m3u8"
	recording.StartAt = time.Now().UTC().Add(-40 * time.Second)
	recording.OutputPath = filepath.Join(t.TempDir(), "late.ts")
	repo := newFakeRecordingRepo(recording)
	svc := newTestService(repo, "ffmpeg", t.TempDir())
	svc.ctx = context.Background()

	svc.startRecording(recording)

	latest, err := svc.Get(recording.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Status != models.RecordingStatusCompleted {
		t.Fatalf("Status = %q, want completed (error=%q)", latest.Status, latest.Error)
	}
	probe, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration:stream=codec_type", "-of", "default=nw=1", latest.OutputPath).Output()
	if err != nil {
		t.Fatal(err)
	}
	var duration float64
	for _, line := range strings.Fields(string(probe)) {
		if value, ok := strings.CutPrefix(line, "duration="); ok {
			duration, _ = strconv.ParseFloat(value, 64)
		}
	}
	// 46 s from the wanted start to the stop time (and up to 6 s more from the lead); live only
	// it would be about 6 s.
	if duration < 44 || duration > 56 || !strings.Contains(string(probe), "codec_type=video") || !strings.Contains(string(probe), "codec_type=audio") {
		t.Fatalf("recorded %.1f s, want about 46 s of video and audio:\n%s", duration, probe)
	}
	if latest.ActualStartAt == nil || latest.ActualStartAt.After(recording.StartAt) || latest.ActualStartAt.Before(recording.StartAt.Add(-10*time.Second)) {
		t.Fatalf("ActualStartAt = %v, want just before %s", latest.ActualStartAt, recording.StartAt)
	}
}
