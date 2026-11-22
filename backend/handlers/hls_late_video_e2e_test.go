package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// An IPTV recording can start with seconds of audio from many tracks before
// the first video packet (ZDF's HLS: six audio renditions, video 8s in).
// Played from the local file, FFmpeg must probe far enough to learn the
// video's size, or the segment muxer refuses to start ("dimensions not set")
// and the playlist never appears.
func TestLocalRecordingWithLateVideoPlays(t *testing.T) {
	if os.Getenv("MEDIASTORM_FFMPEG_E2E") != "1" {
		t.Skip("set MEDIASTORM_FFMPEG_E2E=1 to run against ffmpeg")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "ZDFinfo - 2026-11-20 1830 - Doku.ts")
	args := []string{"-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=40",
		"-itsoffset", "8", "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=50:duration=32"}
	for i := 0; i < 6; i++ {
		args = append(args, "-map", "0:a")
	}
	args = append(args, "-map", "1:v",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "100", "-bf", "2",
		"-c:a", "aac", "-b:a", "256k", "-output_ts_offset", "1.4", "-f", "mpegts", src)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("generate source: %v %s", err, out)
	}
	provider := &growingTestProvider{directURL: src}
	m := NewHLSManager(filepath.Join(dir, "hls"), "ffmpeg", "ffprobe", provider)
	t.Cleanup(m.Shutdown)
	caps := &HLSClientCaps{TSVideo: []string{"h264"}, TSAudio: []string{"aac"}}
	path := "recording:rec-1/" + filepath.Base(src)
	session, err := m.CreateSession(withHLSClientCaps(context.Background(), caps), path, path, false, "", false, false, 0, 0, -1, -1, "p", "p", "127.0.0.1", false, "", "", 0, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer m.CleanupSession(session.ID)
	rr := httptest.NewRecorder()
	m.ServePlaylist(rr, httptest.NewRequest(http.MethodGet, "/video/hls/"+session.ID+"/stream.m3u8", nil), session.ID)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "#EXTINF") {
		t.Fatalf("playlist: %d %s", rr.Code, rr.Body.String())
	}
	// The first segment the playlist names.
	name := ""
	for _, line := range strings.Split(rr.Body.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			name = filepath.Base(strings.SplitN(line, "?", 2)[0])
			break
		}
	}
	if name == "" {
		t.Fatalf("no segment in playlist:\n%s", rr.Body.String())
	}
	rr = httptest.NewRecorder()
	m.ServeSegment(rr, httptest.NewRequest(http.MethodGet, "/video/hls/"+session.ID+"/"+name, nil), session.ID, name)
	if rr.Code != http.StatusOK || rr.Body.Len() == 0 {
		t.Fatalf("%s: %d %s", name, rr.Code, rr.Body.String())
	}
}
