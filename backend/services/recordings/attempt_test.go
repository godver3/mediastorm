package recordings

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// attemptSetup is a service with a fake FFmpeg (fakeFFmpegPrelude + script) and a recording that
// writes into a temporary directory.
func attemptSetup(t *testing.T, script string) (*Service, string) {
	t.Helper()
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("ARGS_FILE", argsFile)
	svc := newTestService(newFakeRecordingRepo(), writeFakeFFmpeg(t, fakeFFmpegPrelude+script), t.TempDir())
	svc.ctx = context.Background()
	return svc, argsFile
}

// A late attempt that runs out of time (its deadline) with its content short of the stop time —
// FFmpeg delivering slower than real time — is not done: it carries an error, so the recording is
// flagged rather than completed with its end missing.
func TestLateAttemptOutOfTimeIsFlagged(t *testing.T) {
	stopAt := time.Now().Add(300 * time.Millisecond)
	svc, _ := attemptSetup(t, `printf 'media'
trap 'exit 255' INT
while :; do report 10000000; sleep 0.1; done
`)
	recording := newTestRecording(stopAt)
	recording.OutputPath = filepath.Join(t.TempDir(), "rec.ts")
	backfill := recordingBackfill{liveStartIndex: -100, from: stopAt.Add(-5 * time.Minute), behind: 5 * time.Minute}
	content := svc.followContent(recording.ID)

	result := svc.runRecordingAttempt(context.Background(), recording, stopAt, time.Now().Add(time.Second), 1, allRecordingStreams(), backfill, content)

	if !result.stoppedAtSchedule || result.reachedContent || result.errMsg == "" || result.written == 0 {
		t.Fatalf("result %+v, want stopped at its deadline with an error (content short of the stop time)", result)
	}
}

// On a resume, the window starts no lead earlier: the content up to where it resumes is in the
// file already and would be appended twice.
func TestBackfillResumeStartsWithoutLead(t *testing.T) {
	now := time.Date(2026, 11, 26, 19, 0, 0, 0, time.UTC)
	playlist := livePlaylist(100, now, true, 0)
	want := now.Add(-59 * time.Second)
	first := backfillFromPlaylist(playlist, now, want)
	resume := backfillFromPlaylistLead(playlist, now, want, 0)
	if first.liveStartIndex != -33 || !first.from.Equal(now.Add(-66*time.Second)) {
		t.Fatalf("first start %+v, want -33 from 66 s back", first)
	}
	if resume.liveStartIndex != -30 || !resume.from.Equal(now.Add(-60*time.Second)) {
		t.Fatalf("resume %+v, want -30 from the segment holding want (60 s back)", resume)
	}
}

// An output that stands still after the stop time without content reports reached it only when
// the attempt ran at least as long as the content it should hold.
func TestStalledAttemptReachesTheStopTimeOnlyAfterRunningLongEnough(t *testing.T) {
	originalStall := recordingBackfillStallAfter
	recordingBackfillStallAfter = 700 * time.Millisecond
	defer func() { recordingBackfillStallAfter = originalStall }()
	stopAt := time.Now().Add(200 * time.Millisecond)
	svc, _ := attemptSetup(t, `printf 'media'
trap 'exit 255' INT
while :; do
  if [ -n "$progress" ]; then printf 'total_size=5\nout_time_us=N/A\nprogress=continue\n' >&3; fi
  sleep 0.05
done
`)
	recording := newTestRecording(stopAt)
	recording.OutputPath = filepath.Join(t.TempDir(), "rec.ts")
	// 1.5 s of content to the stop time (0.5 s limit after the slack): running past it is enough.
	backfill := recordingBackfill{liveStartIndex: -10, from: stopAt.Add(-1500 * time.Millisecond), behind: 2 * time.Second}
	content := svc.followContent(recording.ID)

	result := svc.runRecordingAttempt(context.Background(), recording, stopAt, time.Now().Add(10*time.Second), 1, allRecordingStreams(), backfill, content)

	if !result.reachedContent || !result.stoppedAtSchedule {
		t.Fatalf("result %+v, want the stall to count as reaching the stop time", result)
	}
}

// After a timestamp jump the attempt reports what it ran, not the jumped content, and is not
// continued from the window.
func TestJumpedAttemptReportsWhatItRan(t *testing.T) {
	stopAt := time.Now().Add(10 * time.Minute)
	svc, _ := attemptSetup(t, `printf 'media'
report $limit_us
exit 0
`)
	recording := newTestRecording(stopAt)
	recording.OutputPath = filepath.Join(t.TempDir(), "rec.ts")
	backfill := recordingBackfill{liveStartIndex: -100, from: time.Now().Add(-5 * time.Minute), behind: 5 * time.Minute}
	content := svc.followContent(recording.ID)

	result := svc.runRecordingAttempt(context.Background(), recording, stopAt, stopAt, 1, allRecordingStreams(), backfill, content)

	if !result.jumped || result.content > 5*time.Second || result.stoppedAtSchedule {
		t.Fatalf("result %+v, want a jump reported with the content it ran", result)
	}
}

// An adaptive channel is read from the chosen variant's playlists: each input gets the input
// options, and FFmpeg keeps its own User-Agent (the lookups use the same Lavf one).
func TestAttemptReadsTheVariantsInputs(t *testing.T) {
	stopAt := time.Now().Add(300 * time.Millisecond)
	svc, argsFile := attemptSetup(t, `printf 'media'
trap 'exit 255' INT
while :; do sleep 0.05; done
`)
	recording := newTestRecording(stopAt)
	recording.OutputPath = filepath.Join(t.TempDir(), "rec.ts")
	master, _ := http.NewRequest(http.MethodGet, "http://cdn.example/live/master.m3u8", nil)
	streams := variantStreams("#EXTM3U\n"+`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",URI="deu.m3u8"`+"\n"+`#EXT-X-STREAM-INF:BANDWIDTH=9,AUDIO="a"`+"\nv.m3u8\n",
		master.URL, hlsVariant{uri: "http://cdn.example/live/v.m3u8"})

	svc.runRecordingAttempt(context.Background(), recording, stopAt, stopAt, 1, streams, recordingBackfill{}, nil)

	args := recordedAttempts(t, argsFile)[0]
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-user_agent") || strings.Contains(joined, "master.m3u8") {
		t.Fatalf("args %q: want FFmpeg's own User-Agent and no master", args)
	}
	if !strings.Contains(joined, "-reconnect_delay_max 3 -i http://cdn.example/live/v.m3u8 ") ||
		!strings.Contains(joined, "-reconnect_delay_max 3 -i http://cdn.example/live/deu.m3u8 ") ||
		!strings.Contains(joined, "-map 0:v -map 0:a? -map 1:a?") {
		t.Fatalf("args %q: want both playlists as inputs with their options and maps", args)
	}
}

// The master lookups ask with FFmpeg's (Lavf) User-Agent, as FFmpeg itself does.
func TestRecordingLookupsUseLavfUserAgent(t *testing.T) {
	var mu sync.Mutex
	var agents []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		agents = append(agents, r.Method+" "+r.UserAgent())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2,\ns.ts\n"))
	}))
	defer ts.Close()
	chooseRecordingStreams(context.Background(), ts.URL+"/live", recordingStreamChoice{}, false)
	mu.Lock()
	defer mu.Unlock()
	if len(agents) == 0 {
		t.Fatal("no lookup")
	}
	for _, agent := range agents {
		if !strings.Contains(agent, " Lavf/") {
			t.Errorf("lookup %q, want a Lavf User-Agent", agent)
		}
	}
}

// Once something is recorded, an empty variant keeps the file's layout instead of appending
// everything (-map 0); before that, everything is taken.
func TestEmptyVariantKeepsTheLayoutOnceRecorded(t *testing.T) {
	variant := recordingStreamChoice{maps: []string{"-map", "0:v", "-map", "0:a?"}, variant: "http://x/v.m3u8", inputs: []string{"http://x/v.m3u8"}}
	if got := afterEmptyVariant(variant, true, 0); got.variant != "" || !got.final || strings.Join(got.args(), " ") != "-map 0" {
		t.Errorf("nothing recorded: %+v, want everything", got)
	}
	for _, empty := range []bool{true, false} {
		if got := afterEmptyVariant(variant, empty, 1000); got.variant != variant.variant || strings.Join(got.args(), " ") != "-map 0:v -map 0:a?" {
			t.Errorf("recorded, empty=%v: %+v, want the variant kept", empty, got)
		}
	}
}

// An on-time recording whose first attempt fails does not turn late and fetch the window once
// recordingBackfillMinLate has passed: only a first attempt that started late looks back.
func TestOnTimeRecordingWithFailedAttemptsDoesNotLookBack(t *testing.T) {
	stopAt := time.Now().UTC().Add(2 * time.Second)
	svc, recording, argsFile := lateRecordingSetup(t, `count=$(grep -c -- '^--$' "$ARGS_FILE")
if [ "$count" = 1 ]; then sleep 0.8; exit 1; fi
printf 'live'
trap 'exit 255' INT
while :; do sleep 0.05; done
`, recordingBackfillMinLate-500*time.Millisecond, stopAt)

	svc.startRecording(recording)

	attempts := recordedAttempts(t, argsFile)
	if len(attempts) < 2 {
		t.Fatalf("attempts %q, want a retry", attempts)
	}
	for _, args := range attempts {
		if indexOf(args, "-live_start_index") >= 0 {
			t.Fatalf("attempts %q: an on-time recording looked back in the window", attempts)
		}
	}
}
