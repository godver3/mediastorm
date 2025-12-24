package handlers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
)

// MP4BoxSession represents an active MP4Box HLS session for testing
type MP4BoxSession struct {
	ID          string
	SourceURL   string    // Direct URL to the source file
	OutputDir   string
	CreatedAt   time.Time
	LastAccess  time.Time
	MP4BoxCmd   *exec.Cmd
	Cancel      context.CancelFunc
	mu          sync.RWMutex
	Completed   bool
	HasDV       bool
	DVProfile   string
	HasHDR      bool
	Duration    float64
	StartOffset float64

	// Tracking
	SegmentsCreated     int
	SegmentRequestCount int
	BytesStreamed       int64
}

// MP4BoxHLSManager manages MP4Box-based HLS sessions for debug testing
type MP4BoxHLSManager struct {
	sessions    map[string]*MP4BoxSession
	mu          sync.RWMutex
	baseDir     string
	mp4boxPath  string
	ffprobePath string
	cleanupDone chan struct{}
}

// NewMP4BoxHLSManager creates a new MP4Box HLS session manager for testing
func NewMP4BoxHLSManager(baseDir, mp4boxPath, ffprobePath string) *MP4BoxHLSManager {
	if baseDir == "" {
		baseDir = filepath.Join("/tmp", "novastream-mp4box-hls")
	}

	// Resolve MP4Box path
	resolvedMP4Box := strings.TrimSpace(mp4boxPath)
	if resolvedMP4Box == "" {
		resolvedMP4Box = "MP4Box"
	}
	if path, err := exec.LookPath(resolvedMP4Box); err == nil {
		resolvedMP4Box = path
	} else {
		log.Printf("[mp4box-hls] warning: MP4Box not found at %q: %v", resolvedMP4Box, err)
	}

	// Resolve ffprobe path
	resolvedFFprobe := strings.TrimSpace(ffprobePath)
	if resolvedFFprobe == "" {
		resolvedFFprobe = "ffprobe"
	}
	if path, err := exec.LookPath(resolvedFFprobe); err == nil {
		resolvedFFprobe = path
	}

	// Ensure base directory exists
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		log.Printf("[mp4box-hls] failed to create base directory %q: %v", baseDir, err)
	}

	manager := &MP4BoxHLSManager{
		sessions:    make(map[string]*MP4BoxSession),
		baseDir:     baseDir,
		mp4boxPath:  resolvedMP4Box,
		ffprobePath: resolvedFFprobe,
		cleanupDone: make(chan struct{}),
	}

	// Clean up orphaned directories
	manager.cleanupOrphanedDirectories()

	// Start cleanup goroutine
	go manager.cleanupLoop()

	log.Printf("[mp4box-hls] initialized MP4Box HLS debug manager (mp4box=%s, ffprobe=%s)", resolvedMP4Box, resolvedFFprobe)
	return manager
}

func generateMP4BoxSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("mp4box-session-%d", time.Now().UnixNano())
	}
	return "mp4box-" + hex.EncodeToString(b)
}

// CreateSession starts a new MP4Box HLS session from a direct URL
func (m *MP4BoxHLSManager) CreateSession(ctx context.Context, sourceURL string, hasDV bool, dvProfile string, hasHDR bool, startOffset float64) (*MP4BoxSession, error) {
	sessionID := generateMP4BoxSessionID()
	outputDir := filepath.Join(m.baseDir, sessionID)

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}

	bgCtx, cancel := context.WithCancel(context.Background())

	// Probe duration if possible
	var duration float64
	if m.ffprobePath != "" {
		if probedDuration, err := m.probeDuration(ctx, sourceURL); err == nil && probedDuration > 0 {
			duration = probedDuration
			log.Printf("[mp4box-hls] probed duration for session %s: %.2f seconds", sessionID, duration)
		} else if err != nil {
			log.Printf("[mp4box-hls] failed to probe duration: %v", err)
		}
	}

	now := time.Now()
	session := &MP4BoxSession{
		ID:          sessionID,
		SourceURL:   sourceURL,
		OutputDir:   outputDir,
		CreatedAt:   now,
		LastAccess:  now,
		Cancel:      cancel,
		HasDV:       hasDV,
		DVProfile:   dvProfile,
		HasHDR:      hasHDR,
		Duration:    duration,
		StartOffset: startOffset,
	}

	m.mu.Lock()
	m.sessions[sessionID] = session
	m.mu.Unlock()

	// Start MP4Box HLS generation in background
	go func() {
		if err := m.startMP4BoxHLS(bgCtx, session); err != nil {
			log.Printf("[mp4box-hls] session %s HLS generation failed: %v", sessionID, err)
			session.mu.Lock()
			session.Completed = true
			session.mu.Unlock()
		}
	}()

	log.Printf("[mp4box-hls] created session %s for URL %q (DV=%v, HDR=%v, duration=%.2fs)", sessionID, sourceURL, hasDV, hasHDR, duration)

	// Wait for first segment to be available
	if err := m.waitForFirstSegment(ctx, session); err != nil {
		log.Printf("[mp4box-hls] session %s: warning - first segment not ready: %v", sessionID, err)
	}

	return session, nil
}

// probeDuration uses ffprobe to get duration from URL
func (m *MP4BoxHLSManager) probeDuration(ctx context.Context, sourceURL string) (float64, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	args := []string{
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		"-i", sourceURL,
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe execution: %w", err)
	}

	durationStr := strings.TrimSpace(string(output))
	duration, err := strconv.ParseFloat(durationStr, 64)
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", durationStr, err)
	}

	return duration, nil
}

// startMP4BoxHLS runs FFmpeg to generate HLS from the source URL with proper DV support
func (m *MP4BoxHLSManager) startMP4BoxHLS(ctx context.Context, session *MP4BoxSession) error {
	startTime := time.Now()
	log.Printf("[mp4box-hls] session %s: starting FFmpeg HLS pipeline with DV support", session.ID)

	playlistPath := filepath.Join(session.OutputDir, "stream.m3u8")
	initPath := filepath.Join(session.OutputDir, "init.mp4")
	segmentPattern := filepath.Join(session.OutputDir, "segment%d.m4s")

	// Use FFmpeg with -strict unofficial to enable Dolby Vision metadata writing
	// Key flags for DV:
	// -strict unofficial: enables writing dvcC/dvvC boxes
	// -tag:v dvh1: sets proper DV codec tag (Profile 8 compatible)
	// -hls_segment_type fmp4: fragmented MP4 for iOS AVPlayer compatibility

	args := []string{
		"-i", session.SourceURL,
		"-c:v", "copy",
		"-c:a", "copy",
		"-strict", "unofficial", // Enable DV metadata writing
		"-f", "hls",
		"-hls_time", "4",
		"-hls_list_size", "0",
		"-hls_playlist_type", "event",
		"-hls_flags", "independent_segments+temp_file",
		"-hls_segment_type", "fmp4",
		"-hls_fmp4_init_filename", filepath.Base(initPath),
		"-hls_segment_filename", segmentPattern,
	}

	// For Dolby Vision content, add proper codec tags and fix color VUI
	if session.HasDV {
		log.Printf("[mp4box-hls] session %s: configuring for Dolby Vision (profile: %s)", session.ID, session.DVProfile)
		// Use dvh1 tag for Profile 8 (single-layer with HDR10 fallback)
		// or dvhe for Profile 5/7 (dual-layer)
		if strings.Contains(session.DVProfile, "05") || strings.Contains(session.DVProfile, "07") {
			args = append(args, "-tag:v", "dvhe")
		} else {
			args = append(args, "-tag:v", "dvh1")
		}
		// Fix VUI color parameters for sources with incorrect metadata (e.g., bt709 instead of bt2020/PQ)
		// This prevents saturated colors on playback. hevc_metadata is safe with DV - it doesn't break dvcC.
		args = append(args, "-bsf:v", "hevc_metadata=colour_primaries=9:transfer_characteristics=16:matrix_coefficients=9")
	}

	args = append(args, playlistPath)

	// Use ffmpeg
	ffmpegPath := m.ffprobePath
	if strings.HasSuffix(ffmpegPath, "ffprobe") {
		ffmpegPath = strings.TrimSuffix(ffmpegPath, "ffprobe") + "ffmpeg"
	} else {
		ffmpegPath = "ffmpeg"
	}
	if path, err := exec.LookPath(ffmpegPath); err == nil {
		ffmpegPath = path
	}

	log.Printf("[mp4box-hls] session %s: running ffmpeg with args: %v", session.ID, args)

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	cmd.Dir = session.OutputDir

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("mp4box start: %w", err)
	}

	session.mu.Lock()
	session.MP4BoxCmd = cmd
	session.mu.Unlock()

	log.Printf("[mp4box-hls] session %s: MP4Box started (PID=%d)", session.ID, cmd.Process.Pid)

	// Log stderr in background
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				msg := string(buf[:n])
				log.Printf("[mp4box-hls] session %s stderr: %s", session.ID, msg)
			}
			if err != nil {
				break
			}
		}
	}()

	// Log stdout in background
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				msg := string(buf[:n])
				log.Printf("[mp4box-hls] session %s stdout: %s", session.ID, msg)
			}
			if err != nil {
				break
			}
		}
	}()

	// Wait for completion
	err = cmd.Wait()

	completionTime := time.Since(startTime)
	session.mu.Lock()
	session.Completed = true
	session.mu.Unlock()

	if err != nil && ctx.Err() == nil {
		log.Printf("[mp4box-hls] session %s: MP4Box failed after %v: %v", session.ID, completionTime, err)
		return fmt.Errorf("mp4box wait: %w", err)
	}

	log.Printf("[mp4box-hls] session %s: HLS generation completed in %v", session.ID, completionTime)

	// Post-process init segment for Dolby Vision - fix codec tag
	// FFmpeg's HLS muxer writes dvcC box but uses hev1 codec tag instead of dvhe
	// iOS AVPlayer requires the proper dvhe/dvh1 tag to enable DV processing
	if session.HasDV {
		if err := m.fixDVCodecTag(session); err != nil {
			log.Printf("[mp4box-hls] session %s: warning - failed to fix DV codec tag: %v", session.ID, err)
		}
	}

	return nil
}

// fixDVCodecTag modifies the init.mp4 to replace hev1 codec tag with dvhe/dvh1
// This is necessary because FFmpeg's HLS muxer doesn't properly set the DV codec tag
func (m *MP4BoxHLSManager) fixDVCodecTag(session *MP4BoxSession) error {
	initPath := filepath.Join(session.OutputDir, "init.mp4")

	// Read the init segment
	data, err := os.ReadFile(initPath)
	if err != nil {
		return fmt.Errorf("read init segment: %w", err)
	}

	// Determine target codec tag based on DV profile
	// Profile 5/7: dvhe (dual-layer, EL+BL or MEL)
	// Profile 8: dvh1 (single-layer with HDR10 fallback)
	var oldTag, newTag []byte
	if strings.Contains(session.DVProfile, "05") || strings.Contains(session.DVProfile, "07") {
		oldTag = []byte("hev1")
		newTag = []byte("dvhe")
	} else {
		oldTag = []byte("hev1")
		newTag = []byte("dvh1")
	}

	// Replace codec tag in the stsd box
	// The hev1/dvhe tag appears in the sample description box
	modified := bytes.Replace(data, oldTag, newTag, -1)

	if bytes.Equal(data, modified) {
		log.Printf("[mp4box-hls] session %s: no hev1 tag found in init segment (may already be correct)", session.ID)
		return nil
	}

	// Write back the modified init segment
	if err := os.WriteFile(initPath, modified, 0644); err != nil {
		return fmt.Errorf("write init segment: %w", err)
	}

	log.Printf("[mp4box-hls] session %s: fixed DV codec tag (hev1 -> %s)", session.ID, string(newTag))
	return nil
}

// waitForFirstSegment polls for the first segment to be ready
func (m *MP4BoxHLSManager) waitForFirstSegment(ctx context.Context, session *MP4BoxSession) error {
	playlistPath := filepath.Join(session.OutputDir, "stream.m3u8")
	// FFmpeg generates init segment as init.mp4 and segments as segment0.m4s, segment1.m4s, etc.
	initPath := filepath.Join(session.OutputDir, "init.mp4")
	segment0Path := filepath.Join(session.OutputDir, "segment0.m4s")

	deadline := time.Now().Add(60 * time.Second) // Increase timeout for remote URLs
	pollInterval := 500 * time.Millisecond

	log.Printf("[mp4box-hls] session %s: waiting for first segment", session.ID)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Check if playlist exists
		playlistInfo, err := os.Stat(playlistPath)
		if err != nil || playlistInfo.Size() < 50 {
			time.Sleep(pollInterval)
			continue
		}

		// Check for init segment
		initInfo, initErr := os.Stat(initPath)
		if initErr == nil && initInfo.Size() > 0 {
			// Check for first segment (FFmpeg uses segment0.m4s)
			segInfo, segErr := os.Stat(segment0Path)
			if segErr == nil && segInfo.Size() > 0 {
				log.Printf("[mp4box-hls] session %s: first segment ready (init=%d bytes, seg=%d bytes)",
					session.ID, initInfo.Size(), segInfo.Size())

				// Fix DV codec tag immediately when init is ready
				// This is critical: the player may request init before FFmpeg completes
				if session.HasDV {
					if err := m.fixDVCodecTag(session); err != nil {
						log.Printf("[mp4box-hls] session %s: warning - failed to fix DV codec tag early: %v", session.ID, err)
					}
				}

				return nil
			}
		}

		// List files in directory for debugging
		if initErr != nil {
			files, _ := os.ReadDir(session.OutputDir)
			if len(files) > 0 {
				var names []string
				for _, f := range files {
					names = append(names, f.Name())
				}
				log.Printf("[mp4box-hls] session %s: files in output dir: %v", session.ID, names)
			}
		}

		time.Sleep(pollInterval)
	}

	return fmt.Errorf("timeout waiting for first segment")
}

// GetSession retrieves a session by ID
func (m *MP4BoxHLSManager) GetSession(sessionID string) (*MP4BoxSession, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	session, exists := m.sessions[sessionID]
	if exists {
		session.mu.Lock()
		session.LastAccess = time.Now()
		session.mu.Unlock()
	}

	return session, exists
}

// ServePlaylist serves the HLS playlist
func (m *MP4BoxHLSManager) ServePlaylist(w http.ResponseWriter, r *http.Request, sessionID string) {
	session, exists := m.GetSession(sessionID)
	if !exists {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	playlistPath := filepath.Join(session.OutputDir, "stream.m3u8")

	// Wait for playlist with timeout
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(playlistPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			http.Error(w, "playlist not ready", http.StatusGatewayTimeout)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}

	content, err := os.ReadFile(playlistPath)
	if err != nil {
		http.Error(w, "failed to read playlist", http.StatusInternalServerError)
		return
	}

	// Get API key from request for segment URLs
	apiKey := r.URL.Query().Get("apiKey")
	if apiKey == "" {
		apiKey = r.Header.Get("X-API-Key")
		if apiKey == "" {
			apiKey = r.Header.Get("X-PIN")
		}
	}

	// Rewrite segment URLs to include API key
	playlistContent := string(content)

	// Inject HLS tags for HDR/DV
	var headerTags []string
	if session.HasDV || session.HasHDR {
		if !strings.Contains(playlistContent, "#EXT-X-VIDEO-RANGE") {
			headerTags = append(headerTags, "#EXT-X-VIDEO-RANGE:PQ")
		}
	}

	if len(headerTags) > 0 {
		injection := "#EXTM3U\n" + strings.Join(headerTags, "\n") + "\n"
		playlistContent = strings.Replace(playlistContent, "#EXTM3U\n", injection, 1)
	}

	if apiKey != "" {
		lines := strings.Split(playlistContent, "\n")
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasSuffix(trimmed, ".m4s") || strings.HasSuffix(trimmed, ".ts") {
				lines[i] = line + "?apiKey=" + apiKey
			} else if strings.Contains(line, "#EXT-X-MAP:URI=") {
				// Rewrite init segment URL
				lines[i] = strings.Replace(line, `"init.mp4"`, `"init.mp4?apiKey=`+apiKey+`"`, 1)
			}
		}
		playlistContent = strings.Join(lines, "\n")
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write([]byte(playlistContent))

	log.Printf("[mp4box-hls] served playlist for session %s", sessionID)
}

// ServeSegment serves an HLS segment
func (m *MP4BoxHLSManager) ServeSegment(w http.ResponseWriter, r *http.Request, sessionID, segmentName string) {
	session, exists := m.GetSession(sessionID)
	if !exists {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	session.mu.Lock()
	session.SegmentRequestCount++
	session.mu.Unlock()

	if strings.Contains(segmentName, "..") || strings.Contains(segmentName, "/") {
		http.Error(w, "invalid segment name", http.StatusBadRequest)
		return
	}

	segmentPath := filepath.Join(session.OutputDir, segmentName)

	// Wait for segment with timeout
	deadline := time.Now().Add(30 * time.Second)
	var segmentSize int64
	for {
		if stat, err := os.Stat(segmentPath); err == nil {
			segmentSize = stat.Size()
			break
		}
		if time.Now().After(deadline) {
			http.Error(w, "segment not found", http.StatusNotFound)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}

	contentType := "video/mp4"
	if strings.HasSuffix(segmentName, ".m4s") {
		contentType = "video/mp4"
	} else if strings.HasSuffix(segmentName, ".ts") {
		contentType = "video/mp2t"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=31536000")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.FormatInt(segmentSize, 10))

	session.mu.Lock()
	session.BytesStreamed += segmentSize
	session.mu.Unlock()

	http.ServeFile(w, r, segmentPath)
	log.Printf("[mp4box-hls] served segment %s for session %s (%d bytes)", segmentName, sessionID, segmentSize)
}

// CleanupSession removes a session and its files
func (m *MP4BoxHLSManager) CleanupSession(sessionID string) {
	m.mu.Lock()
	session, exists := m.sessions[sessionID]
	if !exists {
		m.mu.Unlock()
		return
	}
	delete(m.sessions, sessionID)
	m.mu.Unlock()

	// Kill MP4Box process
	session.mu.Lock()
	cmd := session.MP4BoxCmd
	session.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		log.Printf("[mp4box-hls] killing MP4Box process for session %s", sessionID)
		_ = cmd.Process.Kill()
	}

	if session.Cancel != nil {
		session.Cancel()
	}

	// Remove output directory
	if err := os.RemoveAll(session.OutputDir); err != nil {
		log.Printf("[mp4box-hls] failed to remove session directory: %v", err)
	}

	log.Printf("[mp4box-hls] cleaned up session %s", sessionID)
}

// cleanupLoop periodically removes old sessions
func (m *MP4BoxHLSManager) cleanupLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.cleanupOldSessions()
		case <-m.cleanupDone:
			return
		}
	}
}

func (m *MP4BoxHLSManager) cleanupOldSessions() {
	now := time.Now()
	var toCleanup []string

	m.mu.RLock()
	for id, session := range m.sessions {
		session.mu.RLock()
		lastAccess := session.LastAccess
		session.mu.RUnlock()

		if now.Sub(lastAccess) > 30*time.Minute {
			toCleanup = append(toCleanup, id)
		}
	}
	m.mu.RUnlock()

	for _, id := range toCleanup {
		log.Printf("[mp4box-hls] cleaning up inactive session %s", id)
		m.CleanupSession(id)
	}
}

func (m *MP4BoxHLSManager) cleanupOrphanedDirectories() {
	entries, err := os.ReadDir(m.baseDir)
	if err != nil {
		return
	}

	cleaned := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirPath := filepath.Join(m.baseDir, entry.Name())
		if err := os.RemoveAll(dirPath); err == nil {
			cleaned++
		}
	}

	if cleaned > 0 {
		log.Printf("[mp4box-hls] cleaned up %d orphaned directories", cleaned)
	}
}

// Shutdown stops the manager and cleans up all sessions
func (m *MP4BoxHLSManager) Shutdown() {
	close(m.cleanupDone)

	m.mu.Lock()
	sessionIDs := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		sessionIDs = append(sessionIDs, id)
	}
	m.mu.Unlock()

	for _, id := range sessionIDs {
		m.CleanupSession(id)
	}

	log.Printf("[mp4box-hls] shutdown complete")
}

// DebugVideoHandler handles debug video streaming requests using MP4Box
type DebugVideoHandler struct {
	mp4boxManager *MP4BoxHLSManager
	ffprobePath   string
}

// NewDebugVideoHandler creates a new debug video handler with MP4Box
func NewDebugVideoHandler(mp4boxPath, ffprobePath string) *DebugVideoHandler {
	manager := NewMP4BoxHLSManager("", mp4boxPath, ffprobePath)
	return &DebugVideoHandler{
		mp4boxManager: manager,
		ffprobePath:   ffprobePath,
	}
}

// StartMP4BoxHLSSession starts a new MP4Box HLS session from a direct URL
func (h *DebugVideoHandler) StartMP4BoxHLSSession(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.WriteHeader(http.StatusOK)
		return
	}

	sourceURL := r.URL.Query().Get("url")
	if sourceURL == "" {
		http.Error(w, "missing url parameter", http.StatusBadRequest)
		return
	}

	// Parse DV/HDR flags
	hasDV := r.URL.Query().Get("dv") == "true"
	dvProfile := r.URL.Query().Get("dvProfile")
	hasHDR := r.URL.Query().Get("hdr") == "true"

	startOffset := 0.0
	if offsetStr := r.URL.Query().Get("startOffset"); offsetStr != "" {
		if parsed, err := strconv.ParseFloat(offsetStr, 64); err == nil {
			startOffset = parsed
		}
	}

	log.Printf("[debug-video] starting MP4Box HLS session for URL=%q dv=%v hdr=%v", sourceURL, hasDV, hasHDR)

	session, err := h.mp4boxManager.CreateSession(r.Context(), sourceURL, hasDV, dvProfile, hasHDR, startOffset)
	if err != nil {
		log.Printf("[debug-video] failed to create MP4Box session: %v", err)
		http.Error(w, fmt.Sprintf("failed to create session: %v", err), http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"sessionId":   session.ID,
		"playlistUrl": fmt.Sprintf("/api/video/debug/mp4box/%s/stream.m3u8", session.ID),
		"duration":    session.Duration,
		"startOffset": session.StartOffset,
		"hasDV":       session.HasDV,
		"hasHDR":      session.HasHDR,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(response)
}

// ServeMP4BoxPlaylist serves the HLS playlist for an MP4Box session
func (h *DebugVideoHandler) ServeMP4BoxPlaylist(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusOK)
		return
	}

	vars := mux.Vars(r)
	sessionID := vars["sessionID"]

	h.mp4boxManager.ServePlaylist(w, r, sessionID)
}

// ServeMP4BoxSegment serves an HLS segment for an MP4Box session
func (h *DebugVideoHandler) ServeMP4BoxSegment(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusOK)
		return
	}

	vars := mux.Vars(r)
	sessionID := vars["sessionID"]
	segment := vars["segment"]

	h.mp4boxManager.ServeSegment(w, r, sessionID, segment)
}

// ProbeVideoURL probes a video URL for metadata
func (h *DebugVideoHandler) ProbeVideoURL(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusOK)
		return
	}

	sourceURL := r.URL.Query().Get("url")
	if sourceURL == "" {
		http.Error(w, "missing url parameter", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	args := []string{
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		"-i", sourceURL,
	}

	cmd := exec.CommandContext(ctx, h.ffprobePath, args...)
	output, err := cmd.Output()
	if err != nil {
		log.Printf("[debug-video] ffprobe failed for %q: %v", sourceURL, err)
		http.Error(w, fmt.Sprintf("probe failed: %v", err), http.StatusInternalServerError)
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(output, &result); err != nil {
		http.Error(w, "failed to parse probe output", http.StatusInternalServerError)
		return
	}

	// Detect Dolby Vision
	hasDV := false
	dvProfile := ""
	hasHDR := false

	if streams, ok := result["streams"].([]interface{}); ok {
		for _, s := range streams {
			stream, ok := s.(map[string]interface{})
			if !ok {
				continue
			}
			codecType, _ := stream["codec_type"].(string)
			if codecType != "video" {
				continue
			}

			// Check for DV in side_data_list
			if sideData, ok := stream["side_data_list"].([]interface{}); ok {
				for _, sd := range sideData {
					sdMap, ok := sd.(map[string]interface{})
					if !ok {
						continue
					}
					sdType, _ := sdMap["side_data_type"].(string)
					sdTypeLower := strings.ToLower(sdType)
					// Check for "dovi" or "dolby vision" in the side_data_type
					if strings.Contains(sdTypeLower, "dovi") || strings.Contains(sdTypeLower, "dolby vision") {
						hasDV = true
						if profile, ok := sdMap["dv_profile"].(float64); ok {
							dvProfile = fmt.Sprintf("dvhe.%02d", int(profile))
						}
						log.Printf("[debug-video] detected Dolby Vision: profile=%d, type=%q", int(sdMap["dv_profile"].(float64)), sdType)
					}
				}
			}

			// Check for HDR10
			colorTransfer, _ := stream["color_transfer"].(string)
			colorPrimaries, _ := stream["color_primaries"].(string)
			if colorTransfer == "smpte2084" && colorPrimaries == "bt2020" {
				hasHDR = true
			}
		}
	}

	result["novastream_analysis"] = map[string]interface{}{
		"hasDolbyVision": hasDV,
		"dvProfile":      dvProfile,
		"hasHDR10":       hasHDR,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(result)
}

// Shutdown cleans up the debug handler
func (h *DebugVideoHandler) Shutdown() {
	if h.mp4boxManager != nil {
		h.mp4boxManager.Shutdown()
	}
}
