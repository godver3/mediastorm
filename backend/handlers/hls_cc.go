package handlers

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CC (Closed Caption) support for live TV streams
// EIA-608 closed captions are embedded in H.264 SEI NAL units (ATSC A53 Part 4)
// and are NOT detectable by ffprobe as separate streams.

// detectClosedCaptions runs a short ffmpeg decode against a live URL to check
// for embedded EIA-608 closed captions. Returns true if CC data is found.
// This is non-blocking and meant to be called in a background goroutine.
func detectClosedCaptions(ctx context.Context, ffmpegPath, liveURL string) bool {
	// Use a short timeout — we only need a few seconds of decoded video
	detectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// Run ffmpeg with showinfo filter which logs CC data to stderr
	cmd := exec.CommandContext(detectCtx, ffmpegPath, // #nosec G204
		"-hide_banner",
		"-loglevel", "verbose",
		"-i", liveURL,
		"-vf", "showinfo",
		"-f", "null",
		"-t", "3",
		"-",
	)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Printf("[hls-cc] failed to create stderr pipe for CC detection: %v", err)
		return false
	}

	if err := cmd.Start(); err != nil {
		log.Printf("[hls-cc] failed to start CC detection ffmpeg: %v", err)
		return false
	}

	// Scan stderr for CC indicators
	scanner := bufio.NewScanner(stderr)
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		// FFmpeg logs "ATSC A53 Part 4 Closed Captions" when it encounters CC data
		if strings.Contains(line, "ATSC A53 Part 4 Closed Captions") ||
			strings.Contains(line, "Closed Captions") ||
			strings.Contains(line, "cc_data") {
			found = true
			break
		}
	}

	// Kill and wait — we don't care about the exit code
	cmd.Process.Kill()
	cmd.Wait()

	return found
}

// maxConcatBytes is the maximum size of the CC concat file before it gets
// rebuilt from on-disk segments. 250 MB caps disk usage for long live sessions.
const maxConcatBytes = 250 * 1024 * 1024

// ccExtractor manages periodic CC extraction from local TS segments.
// Appends new segments to a concat file (preserving PTS for correct timestamps),
// and rebuilds it from on-disk segments when it exceeds maxConcatBytes.
type ccExtractor struct {
	mu          sync.Mutex
	cancel      context.CancelFunc
	outputPath  string       // SRT output file path
	concatPath  string       // Append-only concatenated TS file
	outputDir   string       // Session output directory (where TS segments live)
	ffmpegPath  string
	running     bool
	seenSegs    map[int]bool // All segments ever seen (prevents re-appending)
	lastMaxSeen int          // Highest segment number seen (skip when unchanged)
}

// startCCExtraction starts a background goroutine that periodically runs ffmpeg
// to extract EIA-608 closed captions from local TS segments.
func startCCExtraction(ctx context.Context, outputDir string) (*ccExtractor, error) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("ffmpeg not found: %w", err)
	}

	srtPath := filepath.Join(outputDir, "captions.srt")
	extractCtx, cancel := context.WithCancel(ctx)

	ext := &ccExtractor{
		cancel:      cancel,
		outputPath:  srtPath,
		concatPath:  filepath.Join(outputDir, "cc_concat.ts"),
		outputDir:   outputDir,
		ffmpegPath:  ffmpegPath,
		running:     true,
		seenSegs:    make(map[int]bool),
		lastMaxSeen: -1,
	}

	go ext.extractionLoop(extractCtx)
	return ext, nil
}

// extractionLoop periodically scans for new TS segments and extracts CC
func (e *ccExtractor) extractionLoop(ctx context.Context) {
	log.Printf("[hls-cc] starting extraction loop for %s", e.outputDir)

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			e.mu.Lock()
			e.running = false
			e.mu.Unlock()
			log.Printf("[hls-cc] extraction loop stopped for %s", e.outputDir)
			return
		case <-ticker.C:
			e.processNewSegments(ctx)
		}
	}
}

// segmentNumRe extracts the numeric portion from segment filenames like "segment12.ts"
var segmentNumRe = regexp.MustCompile(`segment(\d+)\.ts$`)

// processNewSegments appends new TS segments to the concat file, then runs
// ffmpeg's movie filter with subcc to extract EIA-608 CC. The concat file is
// append-only so PTS stays continuous and ffmpeg produces correct timestamps.
// When the concat file exceeds maxConcatBytes, it is rebuilt from only the
// segments currently on disk (which HLS already trims to ~10 for live streams).
func (e *ccExtractor) processNewSegments(ctx context.Context) {
	entries, err := os.ReadDir(e.outputDir)
	if err != nil {
		return
	}

	type segInfo struct {
		num  int
		path string
	}
	var segments []segInfo
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		m := segmentNumRe.FindStringSubmatch(entry.Name())
		if m == nil {
			continue
		}
		num, _ := strconv.Atoi(m[1])
		segments = append(segments, segInfo{num: num, path: filepath.Join(e.outputDir, entry.Name())})
	}

	if len(segments) == 0 {
		return
	}

	sort.Slice(segments, func(i, j int) bool {
		return segments[i].num < segments[j].num
	})

	// Skip if no new segments since last run
	maxOnDisk := segments[len(segments)-1].num
	e.mu.Lock()
	if maxOnDisk <= e.lastMaxSeen {
		e.mu.Unlock()
		return
	}
	e.lastMaxSeen = maxOnDisk
	e.mu.Unlock()

	// Find new segments to append
	e.mu.Lock()
	var newSegs []segInfo
	for _, seg := range segments {
		if !e.seenSegs[seg.num] {
			newSegs = append(newSegs, seg)
		}
	}
	e.mu.Unlock()

	if len(newSegs) == 0 {
		return
	}

	// Check if concat file needs rebuilding (exceeds size cap)
	needsRebuild := false
	if fi, err := os.Stat(e.concatPath); err == nil && fi.Size() > maxConcatBytes {
		needsRebuild = true
	}

	if needsRebuild {
		// Rebuild from only the segments currently on disk (HLS keeps ~10)
		concatFile, err := os.OpenFile(e.concatPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			log.Printf("[hls-cc] failed to open concat file for rebuild: %v", err)
			return
		}
		for _, seg := range segments {
			data, err := os.ReadFile(seg.path)
			if err != nil {
				continue
			}
			concatFile.Write(data)
		}
		concatFile.Close()

		// Reset seen set to match what's on disk now
		e.mu.Lock()
		e.seenSegs = make(map[int]bool)
		for _, seg := range segments {
			e.seenSegs[seg.num] = true
		}
		e.mu.Unlock()

		log.Printf("[hls-cc] rebuilt concat file from %d on-disk segments (size cap exceeded)", len(segments))
	} else {
		// Normal path: append only new segments
		concatFile, err := os.OpenFile(e.concatPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			log.Printf("[hls-cc] failed to open concat file: %v", err)
			return
		}
		for _, seg := range newSegs {
			data, err := os.ReadFile(seg.path)
			if err != nil {
				continue
			}
			concatFile.Write(data)
		}
		concatFile.Close()

		e.mu.Lock()
		for _, seg := range newSegs {
			e.seenSegs[seg.num] = true
		}
		e.mu.Unlock()
	}

	// Run ffmpeg on the concat file to extract CC.
	// Write to a temp file then atomic-rename to avoid serving partial/empty SRT.
	tmpPath := e.outputPath + ".tmp"
	movieSrc := fmt.Sprintf("movie=%s[out+subcc]", e.concatPath)

	execCtx, execCancel := context.WithTimeout(ctx, 30*time.Second)
	defer execCancel()
	cmd := exec.CommandContext(execCtx, e.ffmpegPath, // #nosec G204
		"-y",
		"-copyts", // Preserve original PTS so SRT timestamps match HLS player timeline
		"-f", "lavfi", "-i", movieSrc,
		"-map", "0:s", "-c:s", "text",
		"-f", "srt",
		tmpPath,
	)
	output, err := cmd.CombinedOutput()

	if err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("[hls-cc] ffmpeg CC extraction failed: %v (output: %.500s)", err, string(output))
		os.Remove(tmpPath)
		return
	}

	// Post-process: strip \h (non-breaking space markers from ffmpeg CC decoder)
	srtData, err := os.ReadFile(tmpPath)
	if err == nil {
		content := strings.ReplaceAll(string(srtData), "\\h", " ")
		os.WriteFile(tmpPath, []byte(content), 0644)
	}

	// Atomic rename so readers never see a partial file
	os.Rename(tmpPath, e.outputPath)

	log.Printf("[hls-cc] extracted CC (%d new segments, concat %s)", len(newSegs), e.concatSizeStr())
}

// concatSizeStr returns a human-readable size of the concat file
func (e *ccExtractor) concatSizeStr() string {
	fi, err := os.Stat(e.concatPath)
	if err != nil {
		return "0B"
	}
	size := fi.Size()
	if size > 1024*1024 {
		return fmt.Sprintf("%.1fMB", float64(size)/(1024*1024))
	}
	return fmt.Sprintf("%.1fKB", float64(size)/1024)
}

// stop terminates the extraction loop
func (e *ccExtractor) stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cancel != nil {
		e.cancel()
	}
	e.running = false
}

// srtTimestampRe matches SRT timestamp lines like "00:01:23,456 --> 00:01:25,789"
var srtTimestampRe = regexp.MustCompile(`(\d{2}:\d{2}:\d{2}),(\d{3})`)

// srtTimestampLineRe matches a full SRT timestamp line for offset adjustment
var srtTimestampLineRe = regexp.MustCompile(`(\d{2}):(\d{2}):(\d{2}),(\d{3}) --> (\d{2}):(\d{2}):(\d{2}),(\d{3})`)

// parseSRTTimestamp parses "HH:MM:SS,mmm" to milliseconds
func parseSRTTimestamp(h, m, s, ms string) int {
	hours, _ := strconv.Atoi(h)
	mins, _ := strconv.Atoi(m)
	secs, _ := strconv.Atoi(s)
	millis, _ := strconv.Atoi(ms)
	return hours*3600000 + mins*60000 + secs*1000 + millis
}

// cleanSRT deduplicates roll-up CC lines, trims whitespace, and outputs clean SRT.
// Keeps <i> and other formatting tags that KSPlayer/SRT parsers handle natively.
// EIA-608 roll-up captions produce cues where line 1 repeats the previous cue's last line.
// We strip confirmed duplicates while preserving legitimate multi-line cues.
func cleanSRT(srtContent string) string {
	srtContent = strings.TrimPrefix(srtContent, "\xef\xbb\xbf")

	// Normalize line endings: ccextractor outputs \r\n (Windows), convert to \n
	srtContent = strings.ReplaceAll(srtContent, "\r\n", "\n")
	srtContent = strings.ReplaceAll(srtContent, "\r", "\n")

	if strings.Contains(srtContent, "\\h") {
		log.Printf("[hls-cc] cleanSRT stripping lingering \\h markers from served captions")
		srtContent = strings.ReplaceAll(srtContent, "\\h", " ")
	}

	type cue struct {
		startMs int
		endMs   int
		lines   []string
	}

	var cues []cue
	blocks := strings.Split(strings.TrimSpace(srtContent), "\n\n")
	for _, block := range blocks {
		blockLines := strings.Split(strings.TrimSpace(block), "\n")
		if len(blockLines) < 2 {
			continue
		}
		var tsLine string
		var textStart int
		for i, line := range blockLines {
			if srtTimestampLineRe.MatchString(line) {
				tsLine = line
				textStart = i + 1
				break
			}
		}
		if tsLine == "" {
			continue
		}
		m := srtTimestampLineRe.FindStringSubmatch(tsLine)
		if m == nil {
			continue
		}
		startMs := parseSRTTimestamp(m[1], m[2], m[3], m[4])
		endMs := parseSRTTimestamp(m[5], m[6], m[7], m[8])

		var textLines []string
		for _, tl := range blockLines[textStart:] {
			trimmed := strings.TrimSpace(tl)
			if trimmed != "" {
				textLines = append(textLines, trimmed)
			}
		}
		if len(textLines) > 0 {
			cues = append(cues, cue{startMs: startMs, endMs: endMs, lines: textLines})
		}
	}

	var b strings.Builder
	cueNum := 0
	var prevLastLine string
	for _, c := range cues {
		dedupedLines := c.lines
		if len(dedupedLines) > 1 && prevLastLine != "" && dedupedLines[0] == prevLastLine {
			dedupedLines = dedupedLines[1:]
		}

		text := strings.Join(dedupedLines, "\n")
		if text == prevLastLine {
			continue
		}

		cueNum++
		fmt.Fprintf(&b, "%d\n", cueNum)
		fmt.Fprintf(&b, "%s --> %s\n", formatSRTTimestamp(c.startMs), formatSRTTimestamp(c.endMs))
		b.WriteString(text)
		b.WriteString("\n\n")

		prevLastLine = c.lines[len(c.lines)-1]
	}

	return b.String()
}

// formatSRTTimestamp formats milliseconds as "HH:MM:SS,mmm"
func formatSRTTimestamp(totalMs int) string {
	if totalMs < 0 {
		totalMs = 0
	}
	h := totalMs / 3600000
	totalMs %= 3600000
	m := totalMs / 60000
	totalMs %= 60000
	s := totalMs / 1000
	ms := totalMs % 1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}

// ServeLiveCaptions serves the WebVTT captions for a live session.
// It reads the SRT output from ccextractor, converts to WebVTT, and serves it.
// Called on GET /video/hls/{sessionID}/captions.srt
func (m *HLSManager) ServeLiveCaptions(w http.ResponseWriter, r *http.Request, sessionID string) {
	session, exists := m.GetSession(sessionID)
	if !exists {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	session.mu.RLock()
	isLive := session.IsLive
	hasCaptions := session.HasClosedCaptions
	outputDir := session.OutputDir
	_ = session.ccExtractor // extraction starts automatically on detection
	session.mu.RUnlock()

	if !isLive {
		http.Error(w, "captions only available for live sessions", http.StatusBadRequest)
		return
	}

	if !hasCaptions {
		w.Header().Set("Content-Type", "application/x-subrip; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Write([]byte(""))
		return
	}

	// Start extraction if not already running (fallback — normally started on detection)
	session.mu.Lock()
	if session.ccExtractor == nil {
		ext, err := startCCExtraction(context.Background(), outputDir)
		if err != nil {
			session.mu.Unlock()
			log.Printf("[hls-cc] failed to start CC extraction for session %s: %v", sessionID, err)
			w.Header().Set("Content-Type", "application/x-subrip; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Write([]byte(""))
			return
		}
		session.ccExtractor = ext
	}
	srtPath := session.ccExtractor.outputPath
	session.mu.Unlock()

	// Read the SRT file (may be partially written)
	srtData, err := os.ReadFile(srtPath)
	if err != nil {
		w.Header().Set("Content-Type", "application/x-subrip; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Write([]byte(""))
		return
	}

	// Clean up: deduplicate roll-up lines, trim whitespace, keep <i> tags
	cleaned := cleanSRT(string(srtData))

	w.Header().Set("Content-Type", "application/x-subrip; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write([]byte(cleaned))
}

// detectAndSetClosedCaptions runs CC detection in background and updates the session.
// Called from CreateLiveSession after the session is started.
func (m *HLSManager) detectAndSetClosedCaptions(session *HLSSession) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		session.mu.RLock()
		liveURL := session.Path
		sessionID := session.ID
		session.mu.RUnlock()

		log.Printf("[hls-cc] detecting closed captions for session %s", sessionID)
		hasCC := detectClosedCaptions(ctx, m.ffmpegPath, liveURL)

		session.mu.Lock()
		session.HasClosedCaptions = hasCC
		session.CCDetectionDone = true
		session.mu.Unlock()

		if hasCC {
			log.Printf("[hls-cc] session %s: closed captions DETECTED — starting extraction immediately", sessionID)
			// Start extraction right away so cues are ready before the player needs them
			session.mu.RLock()
			outputDir := session.OutputDir
			session.mu.RUnlock()

			ext, err := startCCExtraction(context.Background(), outputDir)
			if err != nil {
				log.Printf("[hls-cc] session %s: failed to start immediate extraction: %v", sessionID, err)
			} else {
				session.mu.Lock()
				session.ccExtractor = ext
				session.mu.Unlock()
			}
		} else {
			log.Printf("[hls-cc] session %s: no closed captions found", sessionID)
		}
	}()
}

// GetCCStatus returns the CC detection status for a live session
func (m *HLSManager) GetCCStatus(sessionID string) (hasCaptions bool, detectionDone bool) {
	session, exists := m.GetSession(sessionID)
	if !exists {
		return false, false
	}

	session.mu.RLock()
	defer session.mu.RUnlock()
	return session.HasClosedCaptions, session.CCDetectionDone
}

// ServeLiveCCStatus handles GET /video/hls/{sessionID}/cc-status
// Returns the CC detection status so the frontend can poll for it
func (m *HLSManager) ServeLiveCCStatus(w http.ResponseWriter, r *http.Request, sessionID string) {
	hasCaptions, detectionDone := m.GetCCStatus(sessionID)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprintf(w, `{"hasClosedCaptions":%s,"detectionDone":%s}`,
		strconv.FormatBool(hasCaptions), strconv.FormatBool(detectionDone))
}
