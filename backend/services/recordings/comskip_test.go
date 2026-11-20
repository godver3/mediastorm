package recordings

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/models"
)

func TestParseEDL(t *testing.T) {
	got := parseEDL([]byte("0.00\t159.60\t0\n1395.36\t1609.60\t0\n\nnot a line\n200 100 0\nNaN 10 0\n10 Inf 0\n-Inf 4 0\n5.5 9.25 3\n"))
	want := []models.CommercialBreak{{Start: 0, End: 159.6}, {Start: 1395.36, End: 1609.6}, {Start: 5.5, End: 9.25}}
	if len(got) != len(want) {
		t.Fatalf("breaks = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("break %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A finished recording is analysed once: comskip's EDL lands next to it, a
// file without commercials gets an empty EDL (comskip exits 1), and deleting
// the recording deletes its EDL.
func TestComskipAnalysesFinishedRecordings(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	// Like comskip: --output=<dir>, the input last; "nocommercials" in the
	// name exits 1 without an EDL after saying so; "noframes" exits 1 without a word
	// (comskip decoded nothing).
	script := writeFakeFFmpeg(t, `#!/bin/sh
echo "$@" >> `+argsFile+`
for a in "$@"; do case "$a" in --output=*) out="${a#--output=}";; esac; in="$a"; done
base=$(basename "$in" .ts)
case "$base" in *nocommercials*) echo "Commercials were not found."; exit 1;; *noframes*) exit 1;; esac
printf '0.00\t159.60\t0\n1395.36\t1609.60\t0\n' > "$out/$base.edl"
`)
	withAds := filepath.Join(dir, "ProSieben Maxx - 2026-11-20 1611 - Dragonball.ts")
	without := filepath.Join(dir, "Arte - nocommercials.ts")
	noFrames := filepath.Join(dir, "Arte - noframes.ts")
	for _, f := range []string{withAds, without, noFrames} {
		if err := os.WriteFile(f, []byte("ts"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repo := newFakeRecordingRepo(
		models.Recording{ID: "ads", Status: models.RecordingStatusCompleted, OutputPath: withAds},
		models.Recording{ID: "none", Status: models.RecordingStatusCompleted, OutputPath: without},
		models.Recording{ID: "noframes", Status: models.RecordingStatusCompleted, OutputPath: noFrames},
		models.Recording{ID: "running", Status: models.RecordingStatusRunning, OutputPath: filepath.Join(dir, "running.ts")},
	)
	s := newTestService(repo, "ffmpeg", dir)
	enabled := true
	s.SetComskip(func() ComskipConfig { return ComskipConfig{Enabled: enabled, Path: script} })

	s.runComskip(context.Background(), "ads")
	s.runComskip(context.Background(), "none")
	s.runComskip(context.Background(), "running")
	s.runComskip(context.Background(), "noframes")
	if _, err := os.Stat(EDLPath(noFrames)); err == nil || s.comskipFailures["noframes"] != 1 {
		t.Fatalf("a file comskip decoded nothing of counts as analysed: %v, failures %d", err, s.comskipFailures["noframes"])
	}
	if got := ReadCommercials(withAds); len(got) != 2 || got[1].End != 1609.6 {
		t.Fatalf("commercials = %+v", got)
	}
	if info, err := os.Stat(EDLPath(without)); err != nil || info.Size() != 0 {
		t.Fatalf("no-commercials recording has no empty EDL: %v", err)
	}
	raw, _ := os.ReadFile(argsFile)
	if strings.Count(string(raw), "\n") != 3 || !strings.Contains(string(raw), "--output=") || !strings.Contains(string(raw), "--ini=") {
		t.Fatalf("comskip calls:\n%s", raw)
	}
	// Analysed once: an existing EDL is not redone.
	s.runComskip(context.Background(), "ads")
	if raw2, _ := os.ReadFile(argsFile); len(raw2) != len(raw) {
		t.Fatal("recording analysed again")
	}
	// Off: nothing runs.
	enabled = false
	s.comskipCachedAt = time.Time{}
	_ = os.Remove(EDLPath(withAds))
	s.runComskip(context.Background(), "ads")
	if _, err := os.Stat(EDLPath(withAds)); err == nil {
		t.Fatal("comskip ran while disabled")
	}
	// Deleting a recording deletes its EDL.
	enabled = true
	s.comskipCachedAt = time.Time{}
	s.runComskip(context.Background(), "ads")
	if err := s.Delete("ads"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(EDLPath(withAds)); !os.IsNotExist(err) {
		t.Fatalf("EDL kept after delete: %v", err)
	}
}

func TestComskipQueuesRecordingsWithoutEDL(t *testing.T) {
	dir := t.TempDir()
	done := filepath.Join(dir, "done.ts")
	todo := filepath.Join(dir, "todo.ts")
	for _, f := range []string{done, todo, EDLPath(done)} {
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repo := newFakeRecordingRepo(
		models.Recording{ID: "done", Status: models.RecordingStatusCompleted, OutputPath: done},
		models.Recording{ID: "todo", Status: models.RecordingStatusCompleted, OutputPath: todo},
		models.Recording{ID: "failed", Status: models.RecordingStatusFailed, OutputPath: filepath.Join(dir, "x.ts")},
	)
	s := newTestService(repo, "ffmpeg", dir)
	s.SetComskip(func() ComskipConfig { return ComskipConfig{Enabled: true, Path: "true"} })
	s.queueMissingComskip(context.Background())
	var queued []string
	for len(s.comskipQueue) > 0 {
		queued = append(queued, <-s.comskipQueue)
	}
	if len(queued) != 1 || queued[0] != "todo" {
		t.Fatalf("queued = %v, want [todo]", queued)
	}
	// Queued once, however often it is found.
	s.queueMissingComskip(context.Background())
	s.queueComskip("todo")
	if len(s.comskipQueue) != 0 {
		t.Fatal("recording still in the queue queued again")
	}
	delete(s.comskipQueued, "todo")
	s.queueComskip("todo")
	s.queueComskip("todo")
	if len(s.comskipQueue) != 1 {
		t.Fatalf("queued %d times", len(s.comskipQueue))
	}
	<-s.comskipQueue
	delete(s.comskipQueued, "todo")
	// A recording whose analysis failed comskipMaxAttempts times is not
	// queued again until restart.
	s.comskipFailures = map[string]int{"todo": comskipMaxAttempts}
	s.queueMissingComskip(context.Background())
	if len(s.comskipQueue) != 0 {
		t.Fatal("failed recording queued again")
	}
}

// One break split at the channel's bumpers comes back as one; blips go.
func TestMergeBreaks(t *testing.T) {
	// Ritas Welt (RTLup) as comskip found it.
	got := mergeBreaks(parseEDL([]byte("923.92\t1086.08\t0\n1106.24\t1161.88\t0\n1182.00\t1228.80\t0\n1978.60\t1979.84\t0\n")))
	if len(got) != 1 || got[0].Start != 923.92 || got[0].End != 1228.8 {
		t.Fatalf("breaks = %+v", got)
	}
	// Breaks a film scene apart stay apart.
	got = mergeBreaks([]models.CommercialBreak{{Start: 2201, End: 2668}, {Start: 132, End: 323}, {Start: 3892, End: 4278}})
	if len(got) != 3 || got[0].Start != 132 {
		t.Fatalf("breaks = %+v", got)
	}
}

// A recording deleted while comskip analyses it gets no EDL afterwards, and
// comskip's files never land next to the recording.
func TestComskipDiscardsAnalysisOfDeletedRecording(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	script := writeFakeFFmpeg(t, `#!/bin/sh
for a in "$@"; do case "$a" in --output=*) out="${a#--output=}";; esac; in="$a"; done
base=$(basename "$in" .ts)
printf 'log' > "$out/$base.log"
touch `+started+`
sleep 1
printf '10\t100\t0\n' > "$out/$base.edl"
`)
	file := filepath.Join(dir, "film.ts")
	if err := os.WriteFile(file, []byte("ts"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := newFakeRecordingRepo(models.Recording{ID: "film", Status: models.RecordingStatusCompleted, OutputPath: file})
	s := newTestService(repo, "ffmpeg", dir)
	s.SetComskip(func() ComskipConfig { return ComskipConfig{Enabled: true, Path: script} })

	done := make(chan struct{})
	go func() { s.runComskip(context.Background(), "film"); close(done) }()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(started); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := s.Delete("film"); err != nil {
		t.Fatal(err)
	}
	<-done
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "started" && e.Name() != filepath.Base(script) {
			t.Errorf("left behind: %s", e.Name())
		}
	}
	if s.comskipFailures["film"] != 0 {
		t.Fatal("a stopped analysis counted as failed")
	}
}

// A failing analysis is retried up to comskipMaxAttempts times; a missing
// binary is not a failure of the recording.
func TestComskipFailuresAndMissingBinary(t *testing.T) {
	dir := t.TempDir()
	script := writeFakeFFmpeg(t, "#!/bin/sh\necho 'boom' >&2\nexit 3\n")
	file := filepath.Join(dir, "film.ts")
	if err := os.WriteFile(file, []byte("ts"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := newFakeRecordingRepo(models.Recording{ID: "film", Status: models.RecordingStatusCompleted, OutputPath: file})
	s := newTestService(repo, "ffmpeg", dir)
	path := filepath.Join(dir, "no-such-comskip")
	s.SetComskip(func() ComskipConfig { return ComskipConfig{Enabled: true, Path: path} })
	s.runComskip(context.Background(), "film")
	s.queueMissingComskip(context.Background())
	if s.comskipFailures["film"] != 0 || len(s.comskipQueue) != 0 {
		t.Fatalf("missing binary: failures %d, queued %d", s.comskipFailures["film"], len(s.comskipQueue))
	}

	path = script
	s.comskipCachedAt = time.Time{}
	for i := 0; i < comskipMaxAttempts; i++ {
		s.queueMissingComskip(context.Background())
		if len(s.comskipQueue) != 1 {
			t.Fatalf("attempt %d not queued", i+1)
		}
		<-s.comskipQueue
		delete(s.comskipQueued, "film")
		s.runComskip(context.Background(), "film")
	}
	s.queueMissingComskip(context.Background())
	if len(s.comskipQueue) != 0 || s.comskipFailures["film"] != comskipMaxAttempts {
		t.Fatalf("after %d failures: queued %d, failures %d", comskipMaxAttempts, len(s.comskipQueue), s.comskipFailures["film"])
	}
	if _, err := os.Stat(EDLPath(file)); err == nil {
		t.Fatal("failed analysis left an EDL")
	}
}

// Switched off, players get no breaks (the EDL stays), and a running
// analysis stops.
func TestComskipOff(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "film.ts")
	if err := os.WriteFile(EDLPath(file), []byte("10\t100\t0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestService(newFakeRecordingRepo(), "ffmpeg", dir)
	enabled := true
	s.SetComskip(func() ComskipConfig { return ComskipConfig{Enabled: enabled} })
	if got := s.Commercials(file); len(got) != 1 {
		t.Fatalf("on: %+v", got)
	}
	enabled = false
	s.comskipCachedAt = time.Time{}
	if got := s.Commercials(file); got != nil {
		t.Fatalf("off: %+v", got)
	}
	if _, err := os.Stat(EDLPath(file)); err != nil {
		t.Fatal("EDL removed when switched off")
	}
	var none *Service
	if none.Commercials(file) != nil {
		t.Fatal("nil service")
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.edl")
	for _, data := range []string{"one", ""} {
		if err := writeFileAtomic(path, []byte(data)); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(path); string(got) != data {
			t.Fatalf("got %q", got)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("temp files left: %d entries", len(entries))
	}
}

// Recordings keep paths relative to the server's directory, while comskip
// runs in its temporary directory.
func TestComskipRelativeRecordingPath(t *testing.T) {
	dir := t.TempDir()
	script := writeFakeFFmpeg(t, `#!/bin/sh
for a in "$@"; do case "$a" in --output=*) out="${a#--output=}";; esac; in="$a"; done
[ -f "$in" ] || exit 255
printf '10\t100\t0\n' > "$out/$(basename "$in" .ts).edl"
`)
	if err := os.MkdirAll(filepath.Join(dir, "cache", "recordings"), 0o755); err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join("cache", "recordings", "VOX - Shopping Queen.ts")
	if err := os.WriteFile(filepath.Join(dir, rel), []byte("ts"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	repo := newFakeRecordingRepo(models.Recording{ID: "vox", Status: models.RecordingStatusCompleted, OutputPath: rel})
	s := newTestService(repo, "ffmpeg", dir)
	s.SetComskip(func() ComskipConfig { return ComskipConfig{Enabled: true, Path: script} })
	s.runComskip(context.Background(), "vox")
	if got := ReadCommercials(rel); len(got) != 1 || s.comskipFailures["vox"] != 0 {
		t.Fatalf("commercials %+v, failures %d", got, s.comskipFailures["vox"])
	}
}
