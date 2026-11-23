package handlers

import (
	"bufio"
	"context"
	"fmt"
	"io"
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

// CC (Closed Caption) support for live TV streams.
// EIA-608 closed captions are embedded in H.264 SEI NAL units (ATSC A53 Part 4)
// and are NOT detectable by ffprobe as separate streams.
//
// Extraction prefers ccextractor (bitstream parse, cheap) feeding only new
// segment bytes once. When ccextractor is unavailable, falls back to a small
// sliding-window ffmpeg decode (never re-decode the whole session).

// segmentNumRe extracts the numeric portion from segment filenames like "segment12.ts"
var segmentNumRe = regexp.MustCompile(`segment(\d+)\.ts$`)

// maxFallbackWindowBytes caps the ffmpeg fallback window so we never re-decode
// a long-lived concat file. ~12 MB is roughly several live segments.
const maxFallbackWindowBytes = 12 * 1024 * 1024

// listLiveSegments returns sorted segment paths currently on disk, numbered from firstLive on
// (a timeshift backlog's segments, below it, are made on demand from the source; hls_live_backlog.go).
func listLiveSegments(outputDir string, firstLive int) []string {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return nil
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
		if num < firstLive {
			continue
		}
		segments = append(segments, segInfo{num: num, path: filepath.Join(outputDir, entry.Name())})
	}
	sort.Slice(segments, func(i, j int) bool {
		return segments[i].num < segments[j].num
	})
	paths := make([]string, 0, len(segments))
	for _, seg := range segments {
		paths = append(paths, seg.path)
	}
	return paths
}

// waitForLiveSegment waits until at least one TS segment exists or ctx is done.
func waitForLiveSegment(ctx context.Context, outputDir string) string {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if segs := listLiveSegments(outputDir, 0); len(segs) > 0 {
			return segs[len(segs)-1]
		}
		select {
		case <-ctx.Done():
			return ""
		case <-ticker.C:
		}
	}
}

// detectClosedCaptionsInSegments sniffs local HLS segments for EIA-608.
// Prefer ccextractor (no pixel decode); fall back to a short ffmpeg probe on one segment.
func detectClosedCaptionsInSegments(ctx context.Context, outputDir, ffmpegPath string) bool {
	segPath := waitForLiveSegment(ctx, outputDir)
	if segPath == "" {
		return false
	}

	if ccPath, err := exec.LookPath("ccextractor"); err == nil {
		tmpSRT := filepath.Join(outputDir, "cc_detect.srt")
		defer os.Remove(tmpSRT)
		detectCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(detectCtx, ccPath, // #nosec G204
			segPath,
			"--out", "srt",
			"-o", tmpSRT,
		)
		_ = cmd.Run()
		if data, err := os.ReadFile(tmpSRT); err == nil && strings.TrimSpace(string(data)) != "" {
			return true
		}
		// Empty SRT does not always mean no CC (some streams need more data);
		// fall through to ffmpeg showinfo on the same local segment.
	}

	if strings.TrimSpace(ffmpegPath) == "" {
		var err error
		ffmpegPath, err = exec.LookPath("ffmpeg")
		if err != nil {
			return false
		}
	}

	detectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(detectCtx, ffmpegPath, // #nosec G204
		"-hide_banner",
		"-loglevel", "verbose",
		"-i", segPath,
		"-vf", "showinfo",
		"-f", "null",
		"-t", "3",
		"-",
	)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return false
	}
	if err := cmd.Start(); err != nil {
		return false
	}
	scanner := bufio.NewScanner(stderr)
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "ATSC A53 Part 4 Closed Captions") ||
			strings.Contains(line, "Closed Captions") ||
			strings.Contains(line, "cc_data") {
			found = true
			break
		}
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
	return found
}

// ccExtractor feeds local live TS segments into a long-lived ccextractor process
// (preferred) or a bounded ffmpeg sliding window (fallback).
type ccExtractor struct {
	mu          sync.Mutex
	cancel      context.CancelFunc
	outputPath  string
	outputDir   string
	ffmpegPath  string
	ccPath      string // empty => ffmpeg fallback
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	running     bool
	seenSegs    map[int]bool
	lastMaxSeen int
	// firstLive is the first live segment number; a timeshift backlog's segments below it are
	// not fed (they are made out of order, on the source's timeline).
	firstLive int
	mode      string // "ccextractor" or "ffmpeg"
}

// startCCExtraction starts background CC extraction for a live session directory.
func startCCExtraction(ctx context.Context, outputDir string, firstLive int) (*ccExtractor, error) {
	srtPath := filepath.Join(outputDir, "captions.srt")
	extractCtx, cancel := context.WithCancel(ctx)

	ext := &ccExtractor{
		cancel:      cancel,
		outputPath:  srtPath,
		outputDir:   outputDir,
		running:     true,
		seenSegs:    make(map[int]bool),
		lastMaxSeen: -1,
		firstLive:   firstLive,
	}

	if ccPath, err := exec.LookPath("ccextractor"); err == nil {
		ext.ccPath = ccPath
		ext.mode = "ccextractor"
		if err := ext.startCCExtractorProcess(extractCtx); err != nil {
			cancel()
			return nil, err
		}
	} else {
		ffmpegPath, err := exec.LookPath("ffmpeg")
		if err != nil {
			cancel()
			return nil, fmt.Errorf("neither ccextractor nor ffmpeg found for live CC extraction")
		}
		ext.ffmpegPath = ffmpegPath
		ext.mode = "ffmpeg"
		log.Printf("[hls-cc] ccextractor not found; using bounded ffmpeg sliding-window fallback for %s", outputDir)
	}

	go ext.extractionLoop(extractCtx)
	return ext, nil
}

func (e *ccExtractor) startCCExtractorProcess(ctx context.Context) error {
	// -s 120: treat stdin as a continuous live stream; exit if no data for 2m
	// (session stop closes stdin / kills the process sooner).
	// --forceflush / --koc: keep SRT readable while still growing.
	cmd := exec.CommandContext(ctx, e.ccPath, // #nosec G204
		"--stdin",
		"-s", "120",
		"--out", "srt",
		"-o", e.outputPath,
		"--forceflush",
		"--koc",
	)
	cmd.Dir = e.outputDir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("ccextractor stdin: %w", err)
	}
	// Discard noisy logs; failures still surface via process exit.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("start ccextractor: %w", err)
	}
	e.cmd = cmd
	e.stdin = stdin
	log.Printf("[hls-cc] started ccextractor (PID=%d) for %s", cmd.Process.Pid, e.outputDir)

	go func() {
		err := cmd.Wait()
		e.mu.Lock()
		e.running = false
		e.mu.Unlock()
		if err != nil && ctx.Err() == nil {
			log.Printf("[hls-cc] ccextractor exited for %s: %v", e.outputDir, err)
		}
	}()
	return nil
}

// extractionLoop periodically appends new TS segment bytes for CC extraction.
func (e *ccExtractor) extractionLoop(ctx context.Context) {
	log.Printf("[hls-cc] starting %s extraction loop for %s", e.mode, e.outputDir)

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			e.shutdownProcess()
			log.Printf("[hls-cc] extraction loop stopped for %s", e.outputDir)
			return
		case <-ticker.C:
			if e.mode == "ccextractor" {
				e.feedNewSegmentsToCCExtractor()
			} else {
				e.processFallbackWindow(ctx)
			}
		}
	}
}

func (e *ccExtractor) feedNewSegmentsToCCExtractor() {
	e.mu.Lock()
	stdin := e.stdin
	running := e.running
	e.mu.Unlock()
	if !running || stdin == nil {
		return
	}

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
		if num < e.firstLive {
			continue
		}
		segments = append(segments, segInfo{num: num, path: filepath.Join(e.outputDir, entry.Name())})
	}
	if len(segments) == 0 {
		return
	}
	sort.Slice(segments, func(i, j int) bool { return segments[i].num < segments[j].num })

	maxOnDisk := segments[len(segments)-1].num
	e.mu.Lock()
	if maxOnDisk <= e.lastMaxSeen {
		e.mu.Unlock()
		return
	}
	e.lastMaxSeen = maxOnDisk
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

	var fed int
	var fedBytes int64
	for _, seg := range newSegs {
		data, err := os.ReadFile(seg.path)
		if err != nil {
			continue
		}
		e.mu.Lock()
		stdin := e.stdin
		e.mu.Unlock()
		if stdin == nil {
			return
		}
		if _, err := stdin.Write(data); err != nil {
			log.Printf("[hls-cc] ccextractor stdin write failed: %v", err)
			return
		}
		fed++
		fedBytes += int64(len(data))
		e.mu.Lock()
		e.seenSegs[seg.num] = true
		e.mu.Unlock()
	}
	if fed > 0 {
		log.Printf("[hls-cc] fed %d new segment(s) (%.1fKB) to ccextractor", fed, float64(fedBytes)/1024)
	}
}

// processFallbackWindow re-extracts CC from a small trailing window of segments
// using ffmpeg. Cost stays O(window), not O(session length).
func (e *ccExtractor) processFallbackWindow(ctx context.Context) {
	paths := listLiveSegments(e.outputDir, e.firstLive)
	if len(paths) == 0 {
		return
	}

	// Build a trailing window under the size cap.
	var window []string
	var total int64
	for i := len(paths) - 1; i >= 0; i-- {
		fi, err := os.Stat(paths[i])
		if err != nil {
			continue
		}
		if total > 0 && total+fi.Size() > maxFallbackWindowBytes {
			break
		}
		window = append([]string{paths[i]}, window...)
		total += fi.Size()
	}
	if len(window) == 0 {
		return
	}

	// Skip work when the highest segment hasn't advanced.
	lastName := filepath.Base(window[len(window)-1])
	m := segmentNumRe.FindStringSubmatch(lastName)
	maxNum := -1
	if m != nil {
		maxNum, _ = strconv.Atoi(m[1])
	}
	e.mu.Lock()
	if maxNum >= 0 && maxNum <= e.lastMaxSeen {
		e.mu.Unlock()
		return
	}
	if maxNum >= 0 {
		e.lastMaxSeen = maxNum
	}
	e.mu.Unlock()

	concatPath := filepath.Join(e.outputDir, "cc_window.ts")
	f, err := os.OpenFile(concatPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return
	}
	for _, p := range window {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		_, _ = f.Write(data)
	}
	_ = f.Close()

	tmpPath := e.outputPath + ".tmp"
	movieSrc := fmt.Sprintf("movie=%s[out+subcc]", concatPath)
	execCtx, execCancel := context.WithTimeout(ctx, 20*time.Second)
	defer execCancel()
	cmd := exec.CommandContext(execCtx, e.ffmpegPath, // #nosec G204
		"-y",
		"-copyts",
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
		log.Printf("[hls-cc] ffmpeg fallback CC extraction failed: %v (output: %.300s)", err, string(output))
		_ = os.Remove(tmpPath)
		return
	}
	if srtData, err := os.ReadFile(tmpPath); err == nil {
		content := strings.ReplaceAll(string(srtData), "\\h", " ")
		_ = os.WriteFile(tmpPath, []byte(content), 0644)
	}
	_ = os.Rename(tmpPath, e.outputPath)
	log.Printf("[hls-cc] ffmpeg fallback extracted CC (window=%d segs, %.1fMB)", len(window), float64(total)/(1024*1024))
}

func (e *ccExtractor) shutdownProcess() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.running = false
	if e.stdin != nil {
		_ = e.stdin.Close()
		e.stdin = nil
	}
	if e.cmd != nil && e.cmd.Process != nil {
		_ = e.cmd.Process.Kill()
	}
}

// stop terminates the extraction loop and any child process.
func (e *ccExtractor) stop() {
	e.mu.Lock()
	cancel := e.cancel
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	e.shutdownProcess()
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

// ServeLiveCaptions serves SRT captions for a live session.
// Extraction is started lazily on first request (when enabled and CC was detected).
// Called on GET /video/hls/{sessionID}/captions.srt
func (m *HLSManager) ServeLiveCaptions(w http.ResponseWriter, r *http.Request, sessionID string) {
	session, exists := m.GetSession(sessionID)
	if !exists {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	session.mu.RLock()
	isLive := session.IsLive
	enabled := session.LiveCCExtractionEnabled
	hasCaptions := session.HasClosedCaptions
	outputDir := session.OutputDir
	session.mu.RUnlock()

	if !isLive {
		http.Error(w, "captions only available for live sessions", http.StatusBadRequest)
		return
	}

	emptySRT := func() {
		w.Header().Set("Content-Type", "application/x-subrip; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Write([]byte(""))
	}

	if !enabled || !hasCaptions {
		emptySRT()
		return
	}

	// Lazy start: only run ccextractor/ffmpeg once the player asks for captions.
	session.mu.Lock()
	if session.ccExtractor == nil {
		firstLive := 0
		if session.liveBacklog != nil {
			firstLive = len(session.liveBacklog.segments)
		}
		ext, err := startCCExtraction(context.Background(), outputDir, firstLive)
		if err != nil {
			session.mu.Unlock()
			log.Printf("[hls-cc] failed to start CC extraction for session %s: %v", sessionID, err)
			emptySRT()
			return
		}
		session.ccExtractor = ext
		log.Printf("[hls-cc] session %s: lazy-started %s CC extraction", sessionID, ext.mode)
	}
	srtPath := session.ccExtractor.outputPath
	session.mu.Unlock()

	srtData, err := os.ReadFile(srtPath)
	if err != nil {
		emptySRT()
		return
	}

	cleaned := cleanSRT(string(srtData))

	w.Header().Set("Content-Type", "application/x-subrip; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write([]byte(cleaned))
}

// detectAndSetClosedCaptions sniffs local segments for CC and updates the session.
// Does NOT start continuous extraction — that is lazy on ServeLiveCaptions.
func (m *HLSManager) detectAndSetClosedCaptions(session *HLSSession) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()

		session.mu.RLock()
		sessionID := session.ID
		outputDir := session.OutputDir
		enabled := session.LiveCCExtractionEnabled
		session.mu.RUnlock()

		if !enabled {
			session.mu.Lock()
			session.HasClosedCaptions = false
			session.CCDetectionDone = true
			session.mu.Unlock()
			return
		}

		log.Printf("[hls-cc] detecting closed captions for session %s", sessionID)
		hasCC := detectClosedCaptionsInSegments(ctx, outputDir, m.ffmpegPath)

		session.mu.Lock()
		session.HasClosedCaptions = hasCC
		session.CCDetectionDone = true
		session.mu.Unlock()

		if hasCC {
			log.Printf("[hls-cc] session %s: closed captions DETECTED — extraction will start on first captions request", sessionID)
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
