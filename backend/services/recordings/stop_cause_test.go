package recordings

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestRecordingEarlyExitBeforeDeadlineIsNotScheduledCompletion(t *testing.T) {
	// The recorder process exits immediately. Its child briefly retains stderr,
	// delaying Cmd.Wait past the scheduled deadline without stopping the recorder.
	script := writeFakeFFmpeg(t, "#!/bin/sh\nprintf 'partial'\n(sleep 0.2) >&2 &\nexit 0\n")
	recording := newTestRecording(time.Now().UTC().Add(50 * time.Millisecond))
	t.Cleanup(func() { _ = os.Remove(recording.OutputPath) })
	svc := newTestService(newFakeRecordingRepo(recording), script, t.TempDir())
	result := svc.runRecordingAttempt(context.Background(), recording, recording.EndAt, 1)
	if result.stoppedAtSchedule {
		t.Fatal("an already exited recorder was reported as stopped by the schedule")
	}
	if result.written != 7 {
		t.Fatalf("written=%d, want the early partial output", result.written)
	}
}
