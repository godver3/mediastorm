package handlers

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The fMP4 transmux args used "0:s:m:codec_name:subrip?" style maps, which are
// metadata-tag specifiers that current ffmpeg rejects even with "?" (exit 234,
// empty response). Both arms must build args ffmpeg accepts, keep text
// subtitles and drop bitmap ones.
func TestTransmuxArgsRunUnderFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}
	dir := t.TempDir()
	srt := filepath.Join(dir, "a.srt")
	if err := os.WriteFile(srt, []byte("1\n00:00:00,000 --> 00:00:01,000\nhello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "src.mkv")
	gen := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=d=2:s=64x64",
		"-f", "lavfi", "-i", "sine=d=2", "-i", srt,
		"-map", "0", "-map", "1", "-map", "2", "-c:v", "libx264", "-c:a", "ac3", "-c:s", "srt", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("cannot build fixture: %v %s", err, out)
	}
	raw, err := exec.Command(ffprobe, "-v", "error", "-show_streams", "-show_format", "-of", "json", src).Output()
	if err != nil {
		t.Fatal(err)
	}
	var meta ffprobeOutput
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	// A bitmap track the probe reports must not be mapped.
	meta.Streams = append(meta.Streams, ffprobeStream{Index: 9, CodecType: "subtitle", CodecName: "hdmv_pgs_subtitle"})
	if maps := textSubtitleMaps(&meta); len(maps) != 1 || maps[0] != "0:2" {
		t.Fatalf("textSubtitleMaps = %v, want [0:2]", maps)
	}
	meta.Streams = meta.Streams[:len(meta.Streams)-1]

	h := &VideoHandler{}
	cases := map[string][]string{
		"probe":  h.buildTransmuxPlan(&meta, src, false, "").args,
		"legacy": h.buildTransmuxPlan(nil, src, false, "").args,
	}
	for name, args := range cases {
		if strings.Contains(strings.Join(args, " "), "m:codec_name") {
			t.Fatalf("%s args still use codec_name metadata maps: %v", name, args)
		}
		out := filepath.Join(dir, name+".mp4")
		args = append(args[:len(args)-1], out) // replace the pipe output
		cmd := exec.Command(ffmpeg, append([]string{"-y"}, args...)...)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: ffmpeg rejected args %v: %v\n%s", name, args, err, b)
		}
		streams, _ := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_type", "-of", "csv=p=0", out).Output()
		got := strings.FieldsFunc(string(streams), func(r rune) bool { return r < 'a' || r > 'z' })
		if name == "probe" && strings.Join(got, ",") != "video,audio,subtitle" {
			t.Fatalf("probe output streams = %v, want video,audio,subtitle", got)
		}
		if name == "legacy" && strings.Join(got, ",") != "video,audio" {
			t.Fatalf("legacy output streams = %v, want video,audio", got)
		}
	}
}
