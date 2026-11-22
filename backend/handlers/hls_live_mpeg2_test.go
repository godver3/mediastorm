package handlers

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLiveVideoEncodeArgsReplacesOnlyVideoCopy(t *testing.T) {
	args := liveVideoEncodeArgs(liveHLSOutputArgs("native", "seg%d.ts", "stream.m3u8"))
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-c:v copy") || !strings.Contains(joined, "-c:v libx264") {
		t.Fatalf("video not encoded: %s", joined)
	}
	if strings.Contains(joined, "-c:a copy") || !strings.Contains(joined, "-c:a aac -ac 2") {
		t.Fatalf("audio not encoded to stereo AAC: %s", joined)
	}
	if !strings.Contains(joined, "-force_key_frames") {
		t.Fatalf("encoded segments need forced keyframes: %s", joined)
	}
}

func generateTunerFixture(t *testing.T, ctx context.Context, ffmpeg, codec, path string) []byte {
	t.Helper()
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=30", "-f", "lavfi", "-i", "sine=r=48000",
		"-t", "4", "-c:v", codec, "-g", "15", "-c:a", "ac3", "-f", "mpegts", path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("generate %s fixture: %v %s", codec, err, output)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPeekLiveTSVideoCodecReplaysPeekedBytes(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ts := generateTunerFixture(t, ctx, ffmpeg, "mpeg2video", filepath.Join(t.TempDir(), "mpeg2.ts"))
	codec, body := peekLiveTSVideoCodec(io.NopCloser(bytes.NewReader(ts)), liveCodecPeekLimit, time.Second)
	if codec != "mpeg2" {
		t.Fatalf("codec=%q", codec)
	}
	replayed, err := io.ReadAll(body)
	if err != nil || !bytes.Equal(replayed, ts) {
		t.Fatalf("replay changed the stream: err=%v len=%d want %d", err, len(replayed), len(ts))
	}
	if codec, _ := peekLiveTSVideoCodec(io.NopCloser(strings.NewReader("not a transport stream")), 1024, time.Second); codec != "" {
		t.Fatalf("garbage codec=%q", codec)
	}
}

// An HLS-requested tuner session encodes MPEG-2 video and its AC-3 for AVPlayer; an
// H.264 tuner, or a session that did not ask for HLS, keeps both copies.
func TestHDHomeRunHLSEncodesMPEG2VideoOnly(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg unavailable")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dir := t.TempDir()
	fixtures := map[string][]byte{
		"mpeg2": generateTunerFixture(t, ctx, ffmpeg, "mpeg2video", filepath.Join(dir, "mpeg2.ts")),
		"h264":  generateTunerFixture(t, ctx, ffmpeg, "libx264", filepath.Join(dir, "h264.ts")),
	}
	const streamURL = "http://192.168.1.100:5004/auto/v5.1"
	cases := []struct {
		name, fixture string
		encode        bool
		wantVideo     string
		wantAudio     string
	}{
		{"mpeg2-hls", "mpeg2", true, "h264", "aac"},
		{"mpeg2-direct", "mpeg2", false, "mpeg2video", "ac3"},
		{"h264-hls", "h264", true, "h264", "ac3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := fixtures[tc.fixture]
			transport := discoveryTestTransport(func(r *http.Request) (*http.Response, error) {
				return discoveryTestResponse(200, string(ts), http.Header{"Content-Type": []string{"video/mp2t"}}), nil
			})
			original := http.DefaultTransport
			http.DefaultTransport = transport
			defer func() { http.DefaultTransport = original }()
			m := &HLSManager{ffmpegPath: ffmpeg}
			session := &HLSSession{
				ID: "mpeg2-" + tc.name, Path: streamURL, LiveProvider: "m3u", IsLive: true,
				OutputDir: filepath.Join(dir, tc.name), PlaybackTarget: "native", LastSegmentRequest: time.Now(),
				LiveTuning: LiveTuningSettings{HDHomeRunInput: true, EncodeMPEG2Video: tc.encode},
			}
			if err := m.startLiveTranscoding(ctx, session, 0); err != nil {
				t.Fatalf("start: %v", err)
			}
			playlist := filepath.Join(session.OutputDir, "stream.m3u8")
			probe := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_entries", "stream=codec_name", "-of", "csv=p=0", playlist)
			output, err := probe.CombinedOutput()
			if err != nil {
				t.Fatalf("probe: %v %s", err, output)
			}
			got := string(output)
			if !strings.Contains(got, tc.wantVideo) || !strings.Contains(got, tc.wantAudio) {
				t.Fatalf("streams=%q want %s + %s", got, tc.wantVideo, tc.wantAudio)
			}
			session.mu.RLock()
			encoder := session.VideoEncoder
			session.mu.RUnlock()
			wantEncoder := "copy"
			if tc.name == "mpeg2-hls" {
				wantEncoder = "libx264"
			}
			if encoder != wantEncoder {
				t.Fatalf("encoder=%q want %q", encoder, wantEncoder)
			}
		})
	}
}
