package handlers

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLiveHLSOutputAllowsAudioOnlyAndVideoOnly(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg is not installed")
	}
	for _, target := range []string{"web", "native"} {
		for _, media := range []struct{ name, input, stream string }{
			{"radio", "sine=frequency=440:sample_rate=48000", "0:a:0"},
			{"video", "testsrc2=size=160x90:rate=10", "0:v:0"},
		} {
			t.Run(fmt.Sprintf("%s/%s", target, media.name), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				dir := t.TempDir()
				playlist := filepath.Join(dir, "stream.m3u8")
				// Generate a transport stream first so native mode can copy codecs.
				fixture := filepath.Join(dir, "input.ts")
				generate := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error",
					"-f", "lavfi", "-i", media.input, "-t", "1", "-c:v", "libx264", "-c:a", "aac", "-f", "mpegts", fixture)
				if output, err := generate.CombinedOutput(); err != nil {
					t.Fatalf("generate fixture: %v: %s", err, output)
				}
				args := []string{"-hide_banner", "-loglevel", "error", "-i", fixture}
				args = append(args, liveHLSOutputArgs(target, filepath.Join(dir, "segment%d.ts"), playlist)...)
				if output, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput(); err != nil {
					t.Fatalf("create HLS: %v: %s", err, output)
				}
				decode := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-i", playlist,
					"-map", media.stream, "-f", "null", "-")
				if output, err := decode.CombinedOutput(); err != nil {
					t.Fatalf("decode HLS: %v: %s", err, output)
				}
			})
		}
	}
}

func TestLiveTranscodingIgnoresTeletext(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg is not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	fixture := filepath.Join(t.TempDir(), "live.ts")
	args := []string{"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=10",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "3", "-c:v", "libx264", "-g", "10", "-c:a", "aac", "-f", "mpegts", fixture}
	if output, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("generate fixture: %v: %s", err, output)
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	addLiveTeletextDescriptor(t, data)
	if err := os.WriteFile(fixture, data, 0o600); err != nil {
		t.Fatal(err)
	}
	// The synthetic PMT advertises the same subtitle codec as a DVB broadcaster.
	// No provider connection or recorded programme is needed for this regression.
	probe := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-i", fixture)
	output, _ := probe.CombinedOutput()
	if !strings.Contains(string(output), "dvb_teletext") {
		t.Fatalf("fixture did not advertise teletext: %s", output)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/live.m3u8" {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-TARGETDURATION:3\n#EXTINF:3,\nsegment.ts\n#EXT-X-ENDLIST\n")
			return
		}
		w.Header().Set("Content-Type", "video/mp2t")
		_, _ = w.Write(data)
	}))
	defer upstream.Close()
	for _, target := range []string{"web", "native"} {
		t.Run(target, func(t *testing.T) {
			session := &HLSSession{ID: "teletext-" + target, Path: upstream.URL + "/live.m3u8",
				OutputDir: t.TempDir(), PlaybackTarget: target, IsLive: true, LastSegmentRequest: time.Now()}
			manager := &HLSManager{ffmpegPath: ffmpeg}
			if err := manager.startLiveTranscoding(ctx, session, 0); err != nil {
				t.Fatalf("transcode input with teletext: %v", err)
			}
			decode := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-i",
				filepath.Join(session.OutputDir, "stream.m3u8"), "-map", "0:v:0", "-map", "0:a:0",
				"-frames:v", "5", "-f", "null", "-")
			if output, err := decode.CombinedOutput(); err != nil {
				t.Fatalf("decode output audio and video: %v: %s", err, output)
			}
		})
	}
}

// Add a DVB teletext elementary stream to each single-packet PMT emitted by the
// fixture generator, including its MPEG-2 section CRC. Teletext need not contain
// cues: FFmpeg's automatic subtitle selection fails on the advertised codec.
func addLiveTeletextDescriptor(t *testing.T, data []byte) {
	t.Helper()
	modified := 0
	for offset := 0; offset+188 <= len(data); offset += 188 {
		packet := data[offset : offset+188]
		if packet[0] != 0x47 || int(packet[1]&0x1f)<<8|int(packet[2]) != 0x1000 {
			continue
		}
		start := 4
		if packet[3]&0x20 != 0 {
			start += 1 + int(packet[4])
		}
		start += 1 + int(packet[start])
		length := int(packet[start+1]&0x0f)<<8 | int(packet[start+2])
		end := start + 3 + length - 4
		descriptor := []byte{0x06, 0xe1, 0x20, 0xf0, 0x07, 0x56, 0x05, 'e', 'n', 'g', 0x11, 0x00}
		if end+len(descriptor)+4 > len(packet) {
			t.Fatal("fixture PMT does not fit in one transport packet")
		}
		copy(packet[end:], descriptor)
		length += len(descriptor)
		packet[start+1] = packet[start+1]&0xf0 | byte(length>>8)
		packet[start+2] = byte(length)
		end += len(descriptor)
		crc := uint32(0xffffffff)
		for _, b := range packet[start:end] {
			crc ^= uint32(b) << 24
			for range 8 {
				if crc&0x80000000 != 0 {
					crc = crc<<1 ^ 0x04c11db7
				} else {
					crc <<= 1
				}
			}
		}
		binary.BigEndian.PutUint32(packet[end:end+4], crc)
		modified++
	}
	if modified == 0 {
		t.Fatal("fixture contains no PMT")
	}
}
