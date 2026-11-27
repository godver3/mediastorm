package recordings

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"novastream/models"
)

// FFmpeg reports no content while a mapped stream has no packets (out_time N/A): an output that
// stands still after the stop time is stopped then, not at the extended deadline. Having run far
// shorter than the content it should hold, it ended early: the recording is flagged, not
// completed with its end missing.
func TestLateRecordingStopsWhenItsOutputStandsStill(t *testing.T) {
	originalStall := recordingBackfillStallAfter
	recordingBackfillStallAfter = 300 * time.Millisecond
	defer func() { recordingBackfillStallAfter = originalStall }()
	stopAt := time.Now().UTC().Add(time.Second)
	svc, recording, _ := lateRecordingSetup(t, `printf 'media'
trap 'exit 255' INT
while :; do
  if [ -n "$progress" ]; then printf 'total_size=5\nout_time_us=N/A\nprogress=continue\n' >&3; fi
  sleep 0.1
done
`, 5*time.Minute, stopAt)
	started := time.Now()

	svc.startRecording(recording)

	if latest, _ := svc.Get(recording.ID); latest.Status != models.RecordingStatusFailed || latest.OutputSizeBytes == 0 {
		t.Fatalf("recording %q (%q, %d bytes), want flagged with its output kept", latest.Status, latest.Error, latest.OutputSizeBytes)
	}
	if took := time.Since(started); took > 5*time.Second {
		t.Fatalf("recording took %s, want it stopped shortly after the stop time", took)
	}
}

// Without content reports, the time since following began stands in for the content after a
// while, so a growing-file estimate is not stuck waiting.
func TestRecordedContentWithoutReports(t *testing.T) {
	originalWait, originalStall := recordingContentReportWait, recordingBackfillStallAfter
	recordingContentReportWait, recordingBackfillStallAfter = 200*time.Millisecond, 300*time.Millisecond
	defer func() { recordingContentReportWait, recordingBackfillStallAfter = originalWait, originalStall }()
	stopAt := time.Now().UTC().Add(1500 * time.Millisecond)
	// Like FFmpeg with a mapped stream that has no packets: sizes, no content.
	svc, recording, _ := lateRecordingSetup(t, `printf 'media'
trap 'exit 255' INT
while :; do
  if [ -n "$progress" ]; then printf 'total_size=5\nout_time_us=N/A\nprogress=continue\n' >&3; fi
  sleep 0.1
done
`, 5*time.Minute, stopAt)
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.startRecording(recording)
	}()
	time.Sleep(100 * time.Millisecond)
	early, followed := svc.RecordedContent(recording.ID)
	if !followed || early != 0 {
		t.Errorf("RecordedContent before the wait = %s, %v; want 0, followed", early, followed)
	}
	time.Sleep(400 * time.Millisecond)
	later, _ := svc.RecordedContent(recording.ID)
	<-done
	if later < 200*time.Millisecond || later > time.Second {
		t.Fatalf("RecordedContent without reports = %s, want the time since following began", later)
	}
}

// A first look at the window that does not load is tried again with the next attempt.
func TestLateRecordingPlansAgainWhenTheWindowDidNotLoad(t *testing.T) {
	stopAt := time.Now().UTC().Add(2 * time.Second)
	// Requests: 1 the stream choice, 2 the backfill (fails), 3 the backfill again.
	svc, recording, argsFile := lateRecordingSetup(t, `case "$*" in
*-live_start_index*) printf 'media'; report $limit_us; exit 0 ;;
esac
exit 1
`, 5*time.Minute, stopAt, 2)

	svc.startRecording(recording)

	finishedRecording(t, svc, recording.ID)
	attempts := recordedAttempts(t, argsFile)
	if len(attempts) != 2 || indexOf(attempts[0], "-live_start_index") >= 0 || indexOf(attempts[1], "-live_start_index") < 0 {
		t.Fatalf("attempts %q, want a live one that failed, then a backfilled one", attempts)
	}
}

// A window that keeps failing to load when a backfilled recording continues is given up after a
// few tries: the recording carries on live.
func TestLateRecordingGivesUpAWindowThatKeepsFailing(t *testing.T) {
	stopAt := time.Now().UTC().Add(2 * time.Second)
	svc, recording, argsFile := lateRecordingSetup(t, `printf 'media'
case "$*" in *-live_start_index*) report 60000000; exit 0 ;; esac
trap 'exit 255' INT
while :; do sleep 0.05; done
`, 5*time.Minute, stopAt, 3, 4, 5, 6, 7, 8, 9, 10)

	svc.startRecording(recording)

	finishedRecording(t, svc, recording.ID)
	attempts := recordedAttempts(t, argsFile)
	if len(attempts) != 2 || indexOf(attempts[1], "-live_start_index") >= 0 {
		t.Fatalf("attempts %q, want a backfilled one, then a live one", attempts)
	}
}

// Cancelled while it catches up, a recording ends where its content does, not at the clock.
func TestCancelledLateRecordingEndsWithItsContent(t *testing.T) {
	stopAt := time.Now().UTC().Add(time.Minute)
	svc, recording, _ := lateRecordingSetup(t, `printf 'media'
report 120000000
trap 'exit 255' INT
while :; do sleep 0.05; done
`, 30*time.Minute, stopAt)
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.startRecording(recording)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if content, _ := svc.RecordedContent(recording.ID); content > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := svc.Cancel(recording.ID); err != nil {
		t.Fatal(err)
	}
	<-done
	latest, err := svc.Get(recording.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Status != models.RecordingStatusCancelled || latest.ActualStartAt == nil || latest.ActualEndAt == nil {
		t.Fatalf("recording %+v, want cancelled with its start and end", latest)
	}
	if got := latest.ActualEndAt.Sub(*latest.ActualStartAt); got != 2*time.Minute {
		t.Fatalf("cancelled recording spans %s, want the 2m0s it holds", got)
	}
}

// After content jumped to the stop time, the recording counts what it ran, not the jump.
func TestJumpedContentIsNotCounted(t *testing.T) {
	stopAt := time.Now().UTC().Add(10 * time.Minute)
	svc, recording, argsFile := lateRecordingSetup(t, `printf 'media'
case "$*" in *-live_start_index*) report $limit_us; exit 0 ;; esac
trap 'exit 255' INT
while :; do sleep 0.05; done
`, 5*time.Minute, stopAt)
	ctx, cancel := context.WithCancel(context.Background())
	svc.ctx = ctx
	var during time.Duration
	go func() {
		for {
			time.Sleep(50 * time.Millisecond)
			raw, _ := os.ReadFile(argsFile)
			if strings.Count(string(raw), "--\n") >= 2 {
				time.Sleep(100 * time.Millisecond)
				during, _ = svc.RecordedContent(recording.ID)
				cancel()
				return
			}
		}
	}()

	svc.startRecording(recording)

	if during > 10*time.Second {
		t.Fatalf("RecordedContent after the jump = %s, want about what ran (well under the jumped 15 minutes)", during)
	}
}

// Audio cut in other lengths than the picture would start elsewhere (-live_start_index counts
// each playlist): no backfill then.
func TestPlanRecordingBackfillChecksTheAudioSegments(t *testing.T) {
	end := time.Now()
	audioTarget := "2"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/master.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",NAME=\"de\",URI=\"audio.m3u8\"\n"+
				"#EXT-X-STREAM-INF:BANDWIDTH=2,RESOLUTION=1280x720,AUDIO=\"a\"\nvideo.m3u8\n"+
				"#EXT-X-STREAM-INF:BANDWIDTH=1,RESOLUTION=640x360,AUDIO=\"a\"\nsmall.m3u8\n")
		case "/audio.m3u8":
			fmt.Fprintf(w, "#EXTM3U\n#EXT-X-TARGETDURATION:%s\n#EXT-X-MEDIA-SEQUENCE:1\n", audioTarget)
		default:
			fmt.Fprint(w, livePlaylist(900, end, true, 0))
		}
	}))
	defer srv.Close()
	streams := recordingStreamChoice{variant: srv.URL + "/video.m3u8", playlist: srv.URL + "/master.m3u8"}
	want := end.Add(-time.Minute)
	if got, err := planRecordingBackfill(context.Background(), srv.URL+"/stream", streams, want); err != nil || !got.active() {
		t.Fatalf("audio cut like the picture: backfill %+v, %v; want one", got, err)
	}
	audioTarget = "6"
	if got, err := planRecordingBackfill(context.Background(), srv.URL+"/stream", streams, want); err != nil || got.active() {
		t.Fatalf("audio cut in 6 s segments: backfill %+v, %v; want none", got, err)
	}
}
