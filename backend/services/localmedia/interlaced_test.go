package localmedia

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"novastream/models"
)

// TestLocalProbeDetectsInterlacedVideoEndToEnd: the library probe records whether a file's
// video is interlaced. Runs only when MEDIASTORM_FFMPEG_E2E=1.
func TestLocalProbeDetectsInterlacedVideoEndToEnd(t *testing.T) {
	if os.Getenv("MEDIASTORM_FFMPEG_E2E") != "1" {
		t.Skip("set MEDIASTORM_FFMPEG_E2E=1 to run against ffmpeg")
	}
	dir := t.TempDir()
	service := &Service{ffprobePath: "ffprobe"}
	for name, interlaced := range map[string]bool{"interlaced.ts": true, "progressive.mkv": false} {
		path := filepath.Join(dir, name)
		args := []string{"-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=25", "-t", "2", "-c:v", "libx264", "-preset", "ultrafast"}
		if interlaced {
			args = append(args, "-flags", "+ildct+ilme", "-x264opts", "tff=1")
		}
		args = append(args, path)
		if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
			t.Fatalf("generate %s: %v %s", name, err, out)
		}
		probe, err := service.probeLocalFileWithError(context.Background(), path, 0)
		if err != nil {
			t.Fatalf("probe %s: %v", name, err)
		}
		if probe.Interlaced != interlaced || probe.Height != 1080 || probe.Version != localMediaProbeVersion {
			t.Errorf("%s: interlaced=%v height=%d version=%d, want %v", name, probe.Interlaced, probe.Height, probe.Version, interlaced)
		}
	}

	// An unchanged file probed before Interlaced existed is probed again on the next scan;
	// its match and everything else stay.
	path := filepath.Join(dir, "interlaced.ts")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	modified := info.ModTime().UTC()
	existing := models.LocalMediaItem{
		ID: "item-1", FilePath: path, SizeBytes: info.Size(), ModifiedAt: &modified,
		MatchStatus: models.LocalMediaMatchStatusManual, MatchedTitleID: "tmdb:movie:1",
		Probe: &models.LocalMediaProbe{VideoCodec: "h264", Width: 1920, Height: 1080},
	}
	library := models.LocalMediaLibrary{ID: "lib", Type: models.LocalMediaLibraryTypeMovie}
	item, reused, err := service.buildItem(context.Background(), library, path, "interlaced.ts", detectedTitle{}, nil, "scan2", existing, true)
	if err != nil || !reused {
		t.Fatalf("buildItem: reused=%v err=%v", reused, err)
	}
	if item.Probe == nil || !item.Probe.Interlaced || item.Probe.Version != localMediaProbeVersion || item.ID != "item-1" || item.MatchedTitleID != "tmdb:movie:1" {
		t.Fatalf("reused item = %+v probe %+v", item, item.Probe)
	}
	// A current probe is not redone (a re-probe would set Interlaced again).
	item.Probe.Interlaced = false
	again, _, _ := service.buildItem(context.Background(), library, path, "interlaced.ts", detectedTitle{}, nil, "scan3", item, true)
	if again.Probe.Interlaced {
		t.Fatal("a current probe was redone")
	}
	// A re-probe that fails is not repeated on every scan: the probe counts as current.
	broken := &Service{ffprobePath: "/nonexistent/ffprobe"}
	existing.Probe = &models.LocalMediaProbe{VideoCodec: "h264"}
	failed, _, _ := broken.buildItem(context.Background(), library, path, "interlaced.ts", detectedTitle{}, nil, "scan4", existing, true)
	if failed.Probe == nil || failed.Probe.Version != localMediaProbeVersion || failed.Probe.VideoCodec != "h264" {
		t.Fatalf("failed re-probe: %+v", failed.Probe)
	}
	existing.Probe = nil
	if none, _, _ := broken.buildItem(context.Background(), library, path, "interlaced.ts", detectedTitle{}, nil, "scan5", existing, true); none.Probe == nil || none.Probe.Version != localMediaProbeVersion {
		t.Fatalf("failed re-probe without a probe: %+v", none.Probe)
	}
	if p := broken.probeLocalFile(context.Background(), path, 7); p.Version != localMediaProbeVersion || p.SizeBytes != 7 {
		t.Fatalf("failed probe of a new file: %+v", p)
	}
}
