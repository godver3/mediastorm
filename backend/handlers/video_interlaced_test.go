package handlers

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"novastream/models"
)

func TestComposeMetadataResponseMarksInterlacedVideo(t *testing.T) {
	for _, tc := range []struct {
		fieldOrder string
		want       bool
	}{
		{"tt", true}, {"bb", true}, {"tb", true}, {"bt", true},
		{"progressive", false}, {"unknown", false}, {"", false},
	} {
		meta := &ffprobeOutput{Streams: []ffprobeStream{{Index: 0, CodecType: "video", CodecName: "h264", Width: 1920, Height: 1080, FieldOrder: tc.fieldOrder}}}
		got := composeMetadataResponse(meta, "/rec.ts", audioPlan{}).VideoStreams[0]
		if got.Interlaced != tc.want || got.FieldOrder != tc.fieldOrder {
			t.Errorf("field_order %q: interlaced=%v fieldOrder=%q, want %v", tc.fieldOrder, got.Interlaced, got.FieldOrder, tc.want)
		}
	}
	if !models.IsInterlacedFieldOrder(" TT ") {
		t.Error("field order is matched case-insensitively")
	}
}

// TestMetadataProbeDetectsInterlacedVideoEndToEnd probes an interlaced and a progressive
// H.264 transport stream with the metadata probe's own ffprobe call. Runs only when
// MEDIASTORM_FFMPEG_E2E=1.
func TestMetadataProbeDetectsInterlacedVideoEndToEnd(t *testing.T) {
	if os.Getenv("MEDIASTORM_FFMPEG_E2E") != "1" {
		t.Skip("set MEDIASTORM_FFMPEG_E2E=1 to run against ffmpeg")
	}
	dir := t.TempDir()
	h := NewVideoHandler(false, "ffmpeg", "ffprobe")
	for name, interlaced := range map[string]bool{"interlaced.ts": true, "progressive.ts": false} {
		path := filepath.Join(dir, name)
		args := []string{"-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=720x576:rate=25", "-t", "3", "-c:v", "libx264", "-preset", "ultrafast"}
		if interlaced {
			args = append(args, "-flags", "+ildct+ilme", "-x264opts", "tff=1")
		}
		args = append(args, "-f", "mpegts", path)
		if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
			t.Fatalf("generate %s: %v %s", name, err, out)
		}
		meta, err := h.runFFProbe(context.Background(), path, nil)
		if err != nil {
			t.Fatalf("probe %s: %v", name, err)
		}
		got := composeMetadataResponse(meta, path, audioPlan{}).VideoStreams[0]
		if got.Interlaced != interlaced {
			t.Errorf("%s: interlaced=%v (field_order %q), want %v", name, got.Interlaced, got.FieldOrder, interlaced)
		}
	}
}
