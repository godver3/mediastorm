package handlers

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"novastream/services/streaming"
)

// Watch-while-recording: an HLS session whose source is a recording that is
// still being written follows the file as it grows. FFmpeg reads the local file
// with the file protocol's follow mode, so it waits at the current end instead
// of treating it as EOF; the session's playlist stays an EVENT playlist (every
// segment from the start listed, no ENDLIST) until the recording stops, and is
// then finished with ENDLIST like any VOD.
const (
	// hlsGrowingFollowStallTimeout is how long a follow run waits at the end of
	// the file for more data before treating it as the end (FFmpeg -rw_timeout).
	// It is how a follow run finishes after the recording stops, so ENDLIST
	// appears this long after the last write. It must outlast the gaps between
	// writes of a running recording: IPTV sources arrive in bursts (one HLS
	// segment at a time) and a recorder reconnect takes several seconds.
	//
	// FFmpeg cannot be ended sooner from outside: a single SIGINT does not
	// interrupt a read blocked in follow mode, and a second one aborts without
	// finishing the playlist.
	hlsGrowingFollowStallTimeout = 20 * time.Second

	// hlsGrowingIdleTimeout replaces hlsIdleTimeout for follow runs. A viewer
	// paused at the live edge may fetch nothing for a while; the run is cheap
	// (a stream copy that is mostly waiting on the file) and must still be
	// there when playback resumes.
	hlsGrowingIdleTimeout = 10 * time.Minute
)

// sourceIsGrowing reports whether the session source is still being written,
// e.g. a recording whose status is running.
func (m *HLSManager) sourceIsGrowing(ctx context.Context, path string) bool {
	if m == nil || m.streamer == nil || strings.TrimSpace(path) == "" {
		return false
	}
	provider, ok := m.streamer.(streaming.GrowingSourceProvider)
	if !ok {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	growing, err := provider.IsGrowing(ctx, path)
	return err == nil && growing
}

// sourceGrowingState is sourceIsGrowing with the doubt kept: known is false
// when the source could not be asked (a passing error), gone when it no
// longer exists.
func (m *HLSManager) sourceGrowingState(ctx context.Context, path string) (growing, known, gone bool) {
	if m == nil || m.streamer == nil || strings.TrimSpace(path) == "" {
		return false, true, false
	}
	provider, ok := m.streamer.(streaming.GrowingSourceProvider)
	if !ok {
		return false, true, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	growing, err := provider.IsGrowing(ctx, path)
	switch {
	case err == nil:
		return growing, true, false
	case errors.Is(err, streaming.ErrNotFound):
		return false, true, true
	}
	return false, false, false
}

// growingFollowInputArgs are the FFmpeg input options (placed before -i) that
// make the file protocol follow a growing local file.
//
//   - -follow 1: at the current end of file, wait for more data instead of EOF.
//   - -rw_timeout: end the run cleanly (final segment and ENDLIST written) once
//     the file has not grown for hlsGrowingFollowStallTimeout.
//   - -skip_estimate_duration_from_pts: the MPEG-TS demuxer otherwise reads
//     the tail of the file to estimate its duration, which with follow mode
//     blocks startup until the stall timeout.
func growingFollowInputArgs() []string {
	return []string{
		"-follow", "1",
		"-rw_timeout", strconv.FormatInt(hlsGrowingFollowStallTimeout.Microseconds(), 10),
		"-skip_estimate_duration_from_pts", "1",
	}
}

// shouldFollowGrowingInput decides whether this FFmpeg run follows its input:
// only a session created on a growing source, reading a local file (not a pipe
// or HTTP proxy), while the source is still growing.
func shouldFollowGrowingInput(growingSource bool, directInputPath string, stillGrowing bool) bool {
	return growingSource && strings.TrimSpace(directInputPath) != "" && stillGrowing
}

// sessionSourceGrowing reports whether the session's current run follows a
// source that is still being written.
func (s *HLSSession) sessionSourceGrowing() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.SourceGrowing
}

// settleGrowingRun runs when a follow run's FFmpeg has exited. Once the source
// has stopped growing, the session becomes an ordinary finished VOD: its
// duration is what was segmented, so later seeks and progress reports use it.
func (m *HLSManager) settleGrowingRun(session *HLSSession) {
	if m.sourceIsGrowing(context.Background(), session.Path) {
		log.Printf("[hls] session %s: follow run ended while the source is still growing (no new data for %v); playlist ends here",
			session.ID, hlsGrowingFollowStallTimeout)
		return
	}
	session.mu.RLock()
	outputDir := session.OutputDir
	offset := session.TranscodingOffset
	session.mu.RUnlock()

	duration := 0.0
	if content, err := os.ReadFile(filepath.Join(outputDir, "stream.m3u8")); err == nil {
		duration = playlistMediaDuration(string(content))
	}
	session.mu.Lock()
	session.SourceGrowing = false
	if duration > 0 {
		session.Duration = offset + duration
	}
	finalDuration := session.Duration
	session.mu.Unlock()
	log.Printf("[hls] session %s: growing source finished; session duration %.2fs", session.ID, finalDuration)
}

// playlistMediaDuration sums the EXTINF durations of a media playlist.
func playlistMediaDuration(content string) float64 {
	total := 0.0
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#EXTINF:") {
			continue
		}
		value := strings.TrimPrefix(line, "#EXTINF:")
		if comma := strings.IndexByte(value, ','); comma >= 0 {
			value = value[:comma]
		}
		if d, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil && d > 0 {
			total += d
		}
	}
	return total
}

// growingPlaylistContent keeps a growing session's playlist an EVENT playlist:
// it is never rewritten to VOD with synthesised entries (the duration is not
// known yet), and once the session's FFmpeg has completed without writing
// ENDLIST (killed), ENDLIST is appended so players stop polling.
func growingPlaylistContent(content string, completed bool) string {
	if completed && !strings.Contains(content, "#EXT-X-ENDLIST") {
		return strings.TrimRight(content, "\n") + "\n#EXT-X-ENDLIST\n"
	}
	return content
}

// fmp4AACCopyNeedsADTSConversion reports whether copied AAC audio must go
// through aac_adtstoasc: MPEG-TS sources (recordings) carry AAC as ADTS, which
// the fMP4 segment muxer rejects. Other containers already store raw AAC.
func fmp4AACCopyNeedsADTSConversion(fmp4 bool, audioCodec string, inputPaths ...string) bool {
	if !fmp4 || !strings.EqualFold(strings.TrimSpace(audioCodec), "aac") {
		return false
	}
	for _, p := range inputPaths {
		if strings.TrimSpace(p) != "" && isTSLikePath(p) {
			return true
		}
	}
	return false
}
