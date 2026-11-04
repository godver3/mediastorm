package handlers

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Live timeshift: a live HLS session that keeps the last LiveTuningSettings.TimeshiftWindow of the
// channel listed and on disk, so a player can pause it or seek back inside that window and return
// to the edge. Clients ask for it with `timeshift=1` on /live/hls/start; the window is the admin's
// Live TV setting (config.LiveSettings.TimeshiftWindow, 30 minutes by default).
//
// An ordinary live session lists a short sliding window and prunes segments just behind what the
// player has fetched (deleteOldLiveTransmuxSegments): a paused player would find its position gone
// within a couple of minutes. A timeshift session instead
//   - has FFmpeg list enough segments to cover the window, so the stitched playlist never misses a
//     segment between two playlist polls, however long the player waits between them;
//   - trims the stitched playlist by duration (the window), not by entry count;
//   - deletes only segments that have left the window, whatever the player has fetched, and also
//     by age, so a player that stops polling altogether cannot grow the directory without bound.
//
// The session stays up while the player is paused because keepalives count as activity for the
// idle timeout (KeepAlive refreshes LastSegmentRequest).

// timeshiftPruneMarginSegments are kept below the window's first segment for a player's in-flight
// fetch of the segment that has just left the playlist.
const timeshiftPruneMarginSegments = 10

// timeshiftPruneAgeSlack is added to the window before a segment is deleted by age alone.
const timeshiftPruneAgeSlack = 5 * time.Minute

// timeshiftHLSListSize is FFmpeg's -hls_list_size for a timeshift window: every segment of the
// window at the shortest segment FFmpeg cuts (hls_time 2 s), with slack for shorter cuts.
func timeshiftHLSListSize(window time.Duration) int {
	return int(window.Seconds()/2) + liveNativeHLSListSize
}

// applyTimeshiftListSize rewrites the -hls_list_size value of live output args for a timeshift
// window; args are returned unchanged when the window is zero.
func applyTimeshiftListSize(args []string, window time.Duration) []string {
	if window <= 0 {
		return args
	}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-hls_list_size" {
			args[i+1] = strconv.Itoa(timeshiftHLSListSize(window))
			break
		}
	}
	return args
}

// trimLivePlaylistWindow drops the oldest stitched entries beyond the session's window: a count
// (liveNativeHLSListSize) for an ordinary live session, a duration for a timeshift one. A
// timeshift session never trims below liveNativeHLSListSize entries. Caller holds session.mu.
func trimLivePlaylistWindow(session *HLSSession) {
	window := session.LiveTuning.TimeshiftWindow
	trimCount := 0
	if window <= 0 {
		trimCount = len(session.livePlaylistWindow) - liveNativeHLSListSize
	} else {
		retained := 0.0
		keep := 0
		for i := len(session.livePlaylistWindow) - 1; i >= 0; i-- {
			retained += session.livePlaylistWindow[i].Duration
			if retained > window.Seconds() && keep >= liveNativeHLSListSize {
				break
			}
			keep++
		}
		trimCount = len(session.livePlaylistWindow) - keep
	}
	if trimCount <= 0 {
		return
	}
	for _, removed := range session.livePlaylistWindow[:trimCount] {
		if removed.Discontinuity {
			session.liveDiscontinuitySequence++
		}
	}
	session.livePlaylistWindow = append([]livePlaylistEntry(nil), session.livePlaylistWindow[trimCount:]...)
}

// isTimeshiftSession reports whether the session keeps a timeshift window.
func (s *HLSSession) isTimeshiftSession() bool {
	return s != nil && s.IsLive && s.LiveTuning.TimeshiftWindow > 0
}

// pruneTimeshiftSegments deletes the session's segment files that have left the window: those
// numbered below the stitched playlist's first entry (less a margin), and any older than the window
// plus timeshiftPruneAgeSlack.
func (m *HLSManager) pruneTimeshiftSegments(session *HLSSession) {
	if !session.isTimeshiftSession() {
		return
	}
	session.mu.RLock()
	outputDir := session.OutputDir
	sessionID := session.ID
	window := session.LiveTuning.TimeshiftWindow
	firstListed := -1
	if len(session.livePlaylistWindow) > 0 {
		firstListed = session.livePlaylistWindow[0].MediaSequence
	}
	session.mu.RUnlock()

	cutoff := firstListed - timeshiftPruneMarginSegments
	deleted := pruneTimeshiftSegmentFiles(outputDir, cutoff, time.Now().Add(-(window + timeshiftPruneAgeSlack)))
	if deleted > 0 {
		log.Printf("[hls] session %s: deleted %d timeshift segments (window=%s, first listed=%d)",
			sessionID, deleted, window, firstListed)
	}
}

// pruneTimeshiftSegmentFiles removes segment%d.ts files numbered below `cutoff` (none when cutoff
// is negative) or last written before `olderThan`, returning how many it removed.
func pruneTimeshiftSegmentFiles(outputDir string, cutoff int, olderThan time.Time) int {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return 0
	}
	deleted := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "segment") || !strings.HasSuffix(name, ".ts") {
			continue
		}
		var number int
		if _, err := fmt.Sscanf(name, "segment%d.ts", &number); err != nil {
			continue
		}
		expired := cutoff >= 0 && number < cutoff
		if !expired {
			info, err := entry.Info()
			if err != nil {
				continue
			}
			expired = info.ModTime().Before(olderThan)
		}
		if expired && os.Remove(filepath.Join(outputDir, name)) == nil {
			deleted++
		}
	}
	return deleted
}
