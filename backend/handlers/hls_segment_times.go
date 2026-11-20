package handlers

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// How the segment muxer reads -segment_times changed between FFmpeg versions.
// FFmpeg 5 compares them with each packet's timestamp; FFmpeg 7 (the
// jellyfin-ffmpeg build in mediastorm's image) measures them from the first
// packet it receives. A stable copy run restarted further in keeps source
// timestamps (-copyts), so the two need different cut lists. Which one this
// FFmpeg uses is found once, with a three-second test stream.

var segmentTimesMode struct {
	once     sync.Once
	absolute bool
}

// segmentTimesAbsolute reports whether this FFmpeg compares -segment_times
// with packet timestamps (true) or measures them from the first packet.
func (m *HLSManager) segmentTimesAbsolute() bool {
	segmentTimesMode.once.Do(func() {
		segmentTimesMode.absolute = probeSegmentTimesAbsolute(m.ffmpegPath)
	})
	return segmentTimesMode.absolute
}

// probeSegmentTimesAbsolute segments a test stream whose timestamps start at
// 10s with one cut time of 11: compared with the timestamps it cuts after a
// second (two files); measured from the first packet it would cut at 21,
// after the stream has ended (one file). Without a usable answer the
// first-packet reading (current FFmpeg) is assumed.
func probeSegmentTimesAbsolute(ffmpegPath string) bool {
	if ffmpegPath == "" {
		return false
	}
	dir, err := os.MkdirTemp("", "mediastorm-segment-times-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpegPath, "-nostdin", "-v", "error",
		"-f", "lavfi", "-i", "testsrc=size=32x32:rate=10:duration=3",
		"-output_ts_offset", "10", "-c:v", "mpeg2video", "-g", "1",
		"-f", "segment", "-segment_format", "mpegts", "-segment_times", "11",
		filepath.Join(dir, "s%d.ts"))
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[hls] segment time probe failed (%v: %s); assuming cut times from the first packet", err, out)
		return false
	}
	files, _ := filepath.Glob(filepath.Join(dir, "s*.ts"))
	absolute := len(files) >= 2
	log.Printf("[hls] FFmpeg reads -segment_times as %s", map[bool]string{true: "packet timestamps", false: "time since the first packet"}[absolute])
	return absolute
}
