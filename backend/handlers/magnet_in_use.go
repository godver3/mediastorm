package handlers

import (
	"strings"
	"sync"
	"time"
)

// magnetInUseWindow is how long a source path counts as in use after the last
// sign of playback (request, heartbeat or progress report). Direct-link and
// HLS playbacks read from the provider without touching the backend proxy, so
// these signals are what keeps the magnet janitor from removing a magnet that
// is still being watched.
const magnetInUseWindow = 30 * time.Minute

var progressSourcePaths = struct {
	sync.Mutex
	seen map[string]time.Time
}{seen: make(map[string]time.Time)}

// noteProgressSourcePath records the source path from a playback progress
// report.
func noteProgressSourcePath(sourcePath string) {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return
	}
	now := time.Now()
	progressSourcePaths.Lock()
	defer progressSourcePaths.Unlock()
	progressSourcePaths.seen[sourcePath] = now
	for path, at := range progressSourcePaths.seen {
		if now.Sub(at) > magnetInUseWindow {
			delete(progressSourcePaths.seen, path)
		}
	}
}

// InUseSourcePaths lists source paths with playback activity inside
// magnetInUseWindow: open proxy streams, recently ended streams, direct-link
// playbacks with a fresh heartbeat, and progress reports.
func (t *StreamTracker) InUseSourcePaths() []string {
	var paths []string
	if t != nil {
		cutoff := time.Now().Add(-magnetInUseWindow)
		t.mu.RLock()
		for _, stream := range t.streams {
			if stream != nil && stream.Path != "" {
				paths = append(paths, stream.Path)
			}
		}
		for _, ended := range t.recentlyEnded {
			if ended.stream != nil && ended.stream.Path != "" && ended.endedAt.After(cutoff) {
				paths = append(paths, ended.stream.Path)
			}
		}
		for _, playback := range t.directLinkPlaybacks {
			if playback != nil && playback.Path != "" && playback.LastHeartbeat.After(cutoff) {
				paths = append(paths, playback.Path)
			}
		}
		t.mu.RUnlock()
	}

	cutoff := time.Now().Add(-magnetInUseWindow)
	progressSourcePaths.Lock()
	for path, at := range progressSourcePaths.seen {
		if at.After(cutoff) {
			paths = append(paths, path)
		}
	}
	progressSourcePaths.Unlock()
	return paths
}

// ActiveSourcePaths lists the source paths of all live HLS sessions. ffmpeg
// reads these straight from the provider for the whole session.
func (m *HLSManager) ActiveSourcePaths() []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	paths := make([]string, 0, len(m.sessions))
	for _, session := range m.sessions {
		if session == nil {
			continue
		}
		for _, path := range []string{session.Path, session.OriginalPath} {
			if path != "" {
				paths = append(paths, path)
			}
		}
	}
	return paths
}
