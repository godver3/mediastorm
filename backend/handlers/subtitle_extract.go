package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/services/streaming"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// SubtitleExtractSession represents an active subtitle extraction session
type SubtitleExtractSession struct {
	ID             string
	Path           string
	SubtitleTrack  int
	OutputDir      string
	VTTPath        string
	CreatedAt      time.Time
	LastAccess     time.Time
	cmd            *exec.Cmd
	cancel         context.CancelFunc
	mu             sync.Mutex
	extractionDone bool
	extractionErr  error
}

// SubtitleExtractManager manages subtitle extraction sessions
type SubtitleExtractManager struct {
	sessions    map[string]*SubtitleExtractSession
	mu          sync.RWMutex
	ffmpegPath  string
	ffprobePath string
	streamer    streaming.Provider
	cleanupDone chan struct{}

	// WebDAV URL building (for usenet paths)
	webdavMu     sync.RWMutex
	webdavBase   string
	webdavPrefix string
}

// NewSubtitleExtractManager creates a new subtitle extraction manager
func NewSubtitleExtractManager(ffmpegPath, ffprobePath string, streamer streaming.Provider) *SubtitleExtractManager {
	m := &SubtitleExtractManager{
		sessions:    make(map[string]*SubtitleExtractSession),
		ffmpegPath:  ffmpegPath,
		ffprobePath: ffprobePath,
		streamer:    streamer,
		cleanupDone: make(chan struct{}),
	}
	go m.cleanupLoop()
	return m
}

// ConfigureLocalWebDAVAccess configures WebDAV URL building for usenet paths
func (m *SubtitleExtractManager) ConfigureLocalWebDAVAccess(baseURL, prefix, username, password string) {
	m.webdavMu.Lock()
	defer m.webdavMu.Unlock()

	if baseURL == "" || prefix == "" {
		return
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		log.Printf("[subtitle-extract] invalid WebDAV base URL %q: %v", baseURL, err)
		return
	}

	if username != "" && password != "" {
		parsed.User = url.UserPassword(username, password)
	}

	m.webdavBase = strings.TrimRight(parsed.String(), "/")
	m.webdavPrefix = prefix
	log.Printf("[subtitle-extract] configured WebDAV access: base=%q prefix=%q", m.webdavBase, m.webdavPrefix)
}

// buildWebDAVURL constructs a WebDAV URL for the given path
func (m *SubtitleExtractManager) buildWebDAVURL(cleanPath string) string {
	m.webdavMu.RLock()
	base := m.webdavBase
	prefix := m.webdavPrefix
	m.webdavMu.RUnlock()

	if base == "" || prefix == "" {
		return ""
	}

	pathToUse := cleanPath
	if !strings.HasPrefix(pathToUse, "/") {
		pathToUse = "/" + pathToUse
	}
	if !strings.HasPrefix(pathToUse, prefix) {
		pathToUse = prefix + pathToUse
	}

	return base + pathToUse
}

// cleanupLoop periodically removes stale sessions
func (m *SubtitleExtractManager) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.cleanupStaleSessions()
		case <-m.cleanupDone:
			return
		}
	}
}

// cleanupStaleSessions removes sessions that haven't been accessed recently
func (m *SubtitleExtractManager) cleanupStaleSessions() {
	m.mu.Lock()
	defer m.mu.Unlock()

	staleThreshold := 5 * time.Minute
	now := time.Now()

	for id, session := range m.sessions {
		session.mu.Lock()
		lastAccess := session.LastAccess
		session.mu.Unlock()

		if now.Sub(lastAccess) > staleThreshold {
			log.Printf("[subtitle-extract] cleaning up stale session %s (path=%s)", id, session.Path)
			m.cleanupSessionLocked(session)
			delete(m.sessions, id)
		}
	}
}

// cleanupSessionLocked cleans up a session (caller must hold m.mu)
func (m *SubtitleExtractManager) cleanupSessionLocked(session *SubtitleExtractSession) {
	session.mu.Lock()
	defer session.mu.Unlock()

	if session.cancel != nil {
		session.cancel()
	}
	if session.OutputDir != "" {
		os.RemoveAll(session.OutputDir)
	}
}

// getOrCreateSession gets an existing session or creates a new one
func (m *SubtitleExtractManager) getOrCreateSession(ctx context.Context, path string, subtitleTrack int) (*SubtitleExtractSession, error) {
	// Create a session key based on path and track
	sessionKey := fmt.Sprintf("%s:%d", path, subtitleTrack)

	m.mu.Lock()

	// Check for existing session
	for _, session := range m.sessions {
		if session.Path == path && session.SubtitleTrack == subtitleTrack {
			session.mu.Lock()
			session.LastAccess = time.Now()
			session.mu.Unlock()
			m.mu.Unlock()
			return session, nil
		}
	}

	// Create new session
	sessionID := uuid.New().String()
	outputDir, err := os.MkdirTemp("", "subtitle-extract-"+sessionID)
	if err != nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	vttPath := filepath.Join(outputDir, "subtitles.vtt")

	session := &SubtitleExtractSession{
		ID:            sessionID,
		Path:          path,
		SubtitleTrack: subtitleTrack,
		OutputDir:     outputDir,
		VTTPath:       vttPath,
		CreatedAt:     time.Now(),
		LastAccess:    time.Now(),
	}

	m.sessions[sessionID] = session
	m.mu.Unlock()

	// Start extraction in background
	go m.startExtraction(session)

	log.Printf("[subtitle-extract] created session %s for path=%s track=%d key=%s", sessionID, path, subtitleTrack, sessionKey)
	return session, nil
}

// probeSubtitleStreams gets the list of subtitle streams from a URL
// Uses subtitleStreamInfo type defined in hls.go
func (m *SubtitleExtractManager) probeSubtitleStreams(ctx context.Context, streamURL string) ([]subtitleStreamInfo, error) {
	if m.ffprobePath == "" {
		return nil, fmt.Errorf("ffprobe not configured")
	}

	log.Printf("[subtitle-extract] probing subtitle streams from: %s", streamURL)

	probeCtx, probeCancel := context.WithTimeout(ctx, 60*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-select_streams", "s",
		"-show_entries", "stream=index,codec_name",
		"-of", "json",
		"-i", streamURL,
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	output, err := cmd.Output()
	if err != nil {
		log.Printf("[subtitle-extract] ffprobe failed: %v", err)
		return nil, err
	}

	var result struct {
		Streams []struct {
			Index     int    `json:"index"`
			CodecName string `json:"codec_name"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		log.Printf("[subtitle-extract] failed to parse ffprobe output: %v", err)
		return nil, err
	}

	streams := make([]subtitleStreamInfo, 0, len(result.Streams))
	for _, stream := range result.Streams {
		codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
		streams = append(streams, subtitleStreamInfo{Index: stream.Index, Codec: codec})
	}

	log.Printf("[subtitle-extract] probe results: %d subtitle streams", len(streams))
	return streams, nil
}

// probeSubtitleStreamsFromProvider probes subtitle streams via the streaming provider
func (m *SubtitleExtractManager) probeSubtitleStreamsFromProvider(ctx context.Context, path string) ([]subtitleStreamInfo, error) {
	if m.ffprobePath == "" {
		return nil, fmt.Errorf("ffprobe not configured")
	}
	if m.streamer == nil {
		return nil, fmt.Errorf("streamer not configured")
	}

	log.Printf("[subtitle-extract] probing subtitle streams from provider path: %s", path)

	request := streaming.Request{
		Path:        path,
		Method:      http.MethodGet,
		RangeHeader: "bytes=0-16777215", // First 16MB should contain stream info
	}

	resp, err := m.streamer.Stream(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to stream for probe: %w", err)
	}
	if resp.Body == nil {
		resp.Close()
		return nil, fmt.Errorf("probe returned empty body")
	}
	defer resp.Close()

	pr, pw := io.Pipe()
	copyDone := make(chan error, 1)

	go func() {
		defer pw.Close()
		buf := make([]byte, 128*1024)
		_, copyErr := io.CopyBuffer(pw, resp.Body, buf)
		copyDone <- copyErr
	}()

	probeCtx, probeCancel := context.WithTimeout(ctx, 30*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-select_streams", "s",
		"-show_entries", "stream=index,codec_name",
		"-of", "json",
		"-i", "pipe:0",
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	cmd.Stdin = pr

	output, err := cmd.Output()
	if err != nil {
		pw.CloseWithError(err)
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}

	var result struct {
		Streams []struct {
			Index     int    `json:"index"`
			CodecName string `json:"codec_name"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("failed to parse ffprobe output: %w", err)
	}

	streams := make([]subtitleStreamInfo, 0, len(result.Streams))
	for _, stream := range result.Streams {
		codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
		streams = append(streams, subtitleStreamInfo{Index: stream.Index, Codec: codec})
	}

	log.Printf("[subtitle-extract] probe results: %d subtitle streams", len(streams))
	return streams, nil
}

// startExtraction starts the ffmpeg extraction process
func (m *SubtitleExtractManager) startExtraction(session *SubtitleExtractSession) {
	session.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	session.cancel = cancel
	session.mu.Unlock()

	defer func() {
		session.mu.Lock()
		session.extractionDone = true
		session.mu.Unlock()
	}()

	// Get the stream URL from provider
	var streamURL string
	var useDirectProbe bool
	if strings.HasPrefix(session.Path, "http://") || strings.HasPrefix(session.Path, "https://") {
		streamURL = session.Path
		useDirectProbe = true
	} else if directProvider, ok := m.streamer.(streaming.DirectURLProvider); ok {
		url, err := directProvider.GetDirectURL(ctx, session.Path)
		if err == nil && url != "" {
			streamURL = url
			useDirectProbe = true
		} else {
			// Try WebDAV URL as fallback (for usenet paths)
			webdavURL := m.buildWebDAVURL(session.Path)
			if webdavURL != "" {
				log.Printf("[subtitle-extract] using WebDAV URL for session %s: %s", session.ID, session.Path)
				streamURL = webdavURL
				useDirectProbe = true
			} else {
				log.Printf("[subtitle-extract] failed to get direct URL for session %s: %v", session.ID, err)
				session.mu.Lock()
				session.extractionErr = err
				session.mu.Unlock()
				return
			}
		}
	} else {
		// No direct provider, try WebDAV URL
		webdavURL := m.buildWebDAVURL(session.Path)
		if webdavURL != "" {
			log.Printf("[subtitle-extract] using WebDAV URL for session %s: %s", session.ID, session.Path)
			streamURL = webdavURL
			useDirectProbe = true
		} else {
			session.mu.Lock()
			session.extractionErr = fmt.Errorf("no direct URL provider available")
			session.mu.Unlock()
			return
		}
	}

	// Probe subtitle streams to get actual stream indices
	var subtitleStreams []subtitleStreamInfo
	var err error
	if useDirectProbe {
		subtitleStreams, err = m.probeSubtitleStreams(ctx, streamURL)
	} else {
		subtitleStreams, err = m.probeSubtitleStreamsFromProvider(ctx, session.Path)
	}
	if err != nil {
		log.Printf("[subtitle-extract] session %s: failed to probe subtitles: %v", session.ID, err)
		session.mu.Lock()
		session.extractionErr = fmt.Errorf("failed to probe subtitle streams: %w", err)
		session.mu.Unlock()
		return
	}

	// Validate track index
	if len(subtitleStreams) == 0 {
		log.Printf("[subtitle-extract] session %s: no subtitle streams found in file", session.ID)
		// Create an empty VTT file so frontend doesn't get errors
		if err := os.WriteFile(session.VTTPath, []byte("WEBVTT\n\n"), 0644); err != nil {
			log.Printf("[subtitle-extract] session %s: failed to create empty VTT: %v", session.ID, err)
		}
		return
	}

	if session.SubtitleTrack < 0 || session.SubtitleTrack >= len(subtitleStreams) {
		log.Printf("[subtitle-extract] session %s: subtitle track %d out of range (file has %d subtitle streams), using track 0",
			session.ID, session.SubtitleTrack, len(subtitleStreams))
		// Fall back to track 0 instead of erroring
		session.SubtitleTrack = 0
	}

	// Get the actual stream index for the selected track
	actualStreamIndex := subtitleStreams[session.SubtitleTrack].Index
	log.Printf("[subtitle-extract] session %s: track %d maps to stream index %d (codec: %s)",
		session.ID, session.SubtitleTrack, actualStreamIndex, subtitleStreams[session.SubtitleTrack].Codec)

	log.Printf("[subtitle-extract] session %s: starting extraction from %s", session.ID, streamURL)

	// Build ffmpeg command to extract subtitle track to VTT
	// Use the absolute stream index, not the relative subtitle index
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-i", streamURL,
		"-map", fmt.Sprintf("0:%d", actualStreamIndex),
		"-c", "webvtt",
		"-f", "webvtt",
		"-flush_packets", "1",
		session.VTTPath,
	}

	cmd := exec.CommandContext(ctx, m.ffmpegPath, args...)

	// Capture stderr for logging
	stderr, _ := cmd.StderrPipe()

	session.mu.Lock()
	session.cmd = cmd
	session.mu.Unlock()

	if err := cmd.Start(); err != nil {
		log.Printf("[subtitle-extract] session %s: failed to start ffmpeg: %v", session.ID, err)
		session.mu.Lock()
		session.extractionErr = err
		session.mu.Unlock()
		return
	}

	// Log stderr in background
	go func() {
		if stderr != nil {
			buf := make([]byte, 4096)
			for {
				n, err := stderr.Read(buf)
				if n > 0 {
					log.Printf("[subtitle-extract] session %s ffmpeg: %s", session.ID, string(buf[:n]))
				}
				if err != nil {
					break
				}
			}
		}
	}()

	// Wait for completion
	if err := cmd.Wait(); err != nil {
		if ctx.Err() == context.Canceled {
			log.Printf("[subtitle-extract] session %s: extraction cancelled", session.ID)
		} else {
			log.Printf("[subtitle-extract] session %s: ffmpeg error: %v", session.ID, err)
			session.mu.Lock()
			session.extractionErr = err
			session.mu.Unlock()
		}
		return
	}

	log.Printf("[subtitle-extract] session %s: extraction complete", session.ID)
}

// ProbeSubtitleTracks probes a file and returns available subtitle tracks
func (h *VideoHandler) ProbeSubtitleTracks(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		h.HandleOptions(w, r)
		return
	}

	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}

	// Check if this is a webdav path before cleaning
	isWebDAVPath := strings.HasPrefix(path, "/webdav/") || strings.HasPrefix(path, "webdav/")

	// Clean the path
	cleanPath := path
	if strings.HasPrefix(cleanPath, "/webdav/") {
		cleanPath = strings.TrimPrefix(cleanPath, "/webdav")
	} else if strings.HasPrefix(cleanPath, "webdav/") {
		cleanPath = "/" + strings.TrimPrefix(cleanPath, "webdav/")
	}

	if h.subtitleExtractManager == nil {
		http.Error(w, "subtitle extraction not configured", http.StatusServiceUnavailable)
		return
	}

	// Get the stream URL
	var streamURL string
	if strings.HasPrefix(cleanPath, "http://") || strings.HasPrefix(cleanPath, "https://") {
		streamURL = cleanPath
	} else if isWebDAVPath {
		// For webdav paths (usenet), use the WebDAV URL directly
		webdavURL := h.buildWebDAVURL(cleanPath)
		if webdavURL == "" {
			log.Printf("[subtitle-extract] webdav URL not configured for path: %s", cleanPath)
			http.Error(w, "webdav access not configured", http.StatusServiceUnavailable)
			return
		}
		streamURL = webdavURL
		log.Printf("[subtitle-extract] using WebDAV URL for probe: %s", cleanPath)
	} else if directProvider, ok := h.subtitleExtractManager.streamer.(streaming.DirectURLProvider); ok {
		url, err := directProvider.GetDirectURL(r.Context(), cleanPath)
		if err != nil {
			log.Printf("[subtitle-extract] failed to get direct URL for probe: %v", err)
			http.Error(w, "failed to get stream URL", http.StatusInternalServerError)
			return
		}
		streamURL = url
	} else {
		http.Error(w, "no direct URL provider available", http.StatusServiceUnavailable)
		return
	}

	// Probe subtitle streams
	streams, err := h.subtitleExtractManager.probeSubtitleStreamsWithMetadata(r.Context(), streamURL)
	if err != nil {
		log.Printf("[subtitle-extract] failed to probe subtitles: %v", err)
		http.Error(w, "failed to probe subtitle tracks", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"tracks": streams,
	})
}

// SubtitleTrackInfo represents a subtitle track with metadata for the frontend
type SubtitleTrackInfo struct {
	Index    int    `json:"index"`    // Track index (0-based, for selection)
	Language string `json:"language"` // Language code (e.g., "eng", "spa")
	Title    string `json:"title"`    // Track title/name
	Codec    string `json:"codec"`    // Codec name
	Forced   bool   `json:"forced"`   // Whether this is a forced subtitle track
}

// probeSubtitleStreamsWithMetadata probes subtitle streams and returns detailed metadata
func (m *SubtitleExtractManager) probeSubtitleStreamsWithMetadata(ctx context.Context, streamURL string) ([]SubtitleTrackInfo, error) {
	if m.ffprobePath == "" {
		return nil, fmt.Errorf("ffprobe not configured")
	}

	log.Printf("[subtitle-extract] probing subtitle tracks with metadata from: %s", streamURL)

	probeCtx, probeCancel := context.WithTimeout(ctx, 60*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-select_streams", "s",
		"-show_entries", "stream=index,codec_name:stream_tags=language,title,forced",
		"-of", "json",
		"-i", streamURL,
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	output, err := cmd.Output()
	if err != nil {
		log.Printf("[subtitle-extract] ffprobe failed: %v", err)
		return nil, err
	}

	var result struct {
		Streams []struct {
			Index     int    `json:"index"`
			CodecName string `json:"codec_name"`
			Tags      struct {
				Language string `json:"language"`
				Title    string `json:"title"`
				Forced   string `json:"forced"`
			} `json:"tags"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		log.Printf("[subtitle-extract] failed to parse ffprobe output: %v", err)
		return nil, err
	}

	tracks := make([]SubtitleTrackInfo, 0, len(result.Streams))
	for i, stream := range result.Streams {
		forced := strings.ToLower(stream.Tags.Forced) == "1" || strings.ToLower(stream.Tags.Forced) == "true"
		tracks = append(tracks, SubtitleTrackInfo{
			Index:    i, // Use 0-based index for track selection
			Language: stream.Tags.Language,
			Title:    stream.Tags.Title,
			Codec:    strings.ToLower(strings.TrimSpace(stream.CodecName)),
			Forced:   forced,
		})
	}

	log.Printf("[subtitle-extract] probe found %d subtitle tracks", len(tracks))
	return tracks, nil
}

// ServeSubtitles serves the VTT file for a session
func (m *SubtitleExtractManager) ServeSubtitles(w http.ResponseWriter, r *http.Request, session *SubtitleExtractSession) {
	session.mu.Lock()
	session.LastAccess = time.Now()
	vttPath := session.VTTPath
	extractionErr := session.extractionErr
	session.mu.Unlock()

	// Check for extraction error
	if extractionErr != nil {
		http.Error(w, fmt.Sprintf("subtitle extraction failed: %v", extractionErr), http.StatusInternalServerError)
		return
	}

	// Check if file exists (might not be ready yet)
	stat, err := os.Stat(vttPath)
	if os.IsNotExist(err) {
		// Return empty VTT header if file doesn't exist yet
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Write([]byte("WEBVTT\n\n"))
		return
	}
	if err != nil {
		http.Error(w, "failed to stat subtitle file", http.StatusInternalServerError)
		return
	}

	// Read and serve the current contents
	content, err := os.ReadFile(vttPath)
	if err != nil {
		http.Error(w, "failed to read subtitle file", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))
	w.Write(content)
}

// StartSubtitleExtract is the HTTP handler to start/get a subtitle extraction session
func (h *VideoHandler) StartSubtitleExtract(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		h.HandleOptions(w, r)
		return
	}

	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}

	// Clean the path
	cleanPath := path
	if strings.HasPrefix(cleanPath, "/webdav/") {
		cleanPath = strings.TrimPrefix(cleanPath, "/webdav")
	} else if strings.HasPrefix(cleanPath, "webdav/") {
		cleanPath = "/" + strings.TrimPrefix(cleanPath, "webdav/")
	}

	subtitleTrackStr := strings.TrimSpace(r.URL.Query().Get("subtitleTrack"))
	if subtitleTrackStr == "" {
		http.Error(w, "missing subtitleTrack parameter", http.StatusBadRequest)
		return
	}

	subtitleTrack, err := strconv.Atoi(subtitleTrackStr)
	if err != nil || subtitleTrack < 0 {
		http.Error(w, "invalid subtitleTrack parameter", http.StatusBadRequest)
		return
	}

	if h.subtitleExtractManager == nil {
		http.Error(w, "subtitle extraction not configured", http.StatusServiceUnavailable)
		return
	}

	session, err := h.subtitleExtractManager.getOrCreateSession(r.Context(), cleanPath, subtitleTrack)
	if err != nil {
		log.Printf("[subtitle-extract] failed to create session: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Return session info
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"sessionId":   session.ID,
		"subtitleUrl": fmt.Sprintf("/api/video/subtitles/%s/subtitles.vtt", session.ID),
	})
}

// ServeExtractedSubtitles serves the VTT file for a subtitle extraction session
func (h *VideoHandler) ServeExtractedSubtitles(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		h.HandleOptions(w, r)
		return
	}

	vars := mux.Vars(r)
	sessionID := vars["sessionID"]
	if sessionID == "" {
		http.Error(w, "missing session ID", http.StatusBadRequest)
		return
	}

	if h.subtitleExtractManager == nil {
		http.Error(w, "subtitle extraction not configured", http.StatusServiceUnavailable)
		return
	}

	h.subtitleExtractManager.mu.RLock()
	session, exists := h.subtitleExtractManager.sessions[sessionID]
	h.subtitleExtractManager.mu.RUnlock()

	if !exists {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	h.subtitleExtractManager.ServeSubtitles(w, r, session)
}

// IsExtractionComplete returns whether the extraction has finished
func (s *SubtitleExtractSession) IsExtractionComplete() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.extractionDone
}

// GetExtractionError returns the extraction error, if any
func (s *SubtitleExtractSession) GetExtractionError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.extractionErr
}

// isTextBasedSubtitle checks if a codec is a text-based subtitle format
func isTextBasedSubtitle(codec string) bool {
	codec = strings.ToLower(codec)
	switch codec {
	case "subrip", "srt", "ass", "ssa", "webvtt", "vtt", "mov_text", "text", "dvd_subtitle", "dvdsub":
		return true
	default:
		return false
	}
}

// StartPreExtraction starts extraction for all text-based subtitle tracks using ONE ffmpeg process
// This is much more efficient than spawning one ffmpeg per track (which overwhelms debrid providers)
// Returns a map of track index -> session
func (m *SubtitleExtractManager) StartPreExtraction(ctx context.Context, path string, tracks []SubtitleTrackInfo) map[int]*SubtitleExtractSession {
	sessions := make(map[int]*SubtitleExtractSession)

	// Filter to text-based subtitles only
	var textTracks []SubtitleTrackInfo
	for _, track := range tracks {
		if isTextBasedSubtitle(track.Codec) {
			textTracks = append(textTracks, track)
		} else {
			log.Printf("[subtitle-extract] skipping non-text track %d (codec=%s)", track.Index, track.Codec)
		}
	}

	if len(textTracks) == 0 {
		log.Printf("[subtitle-extract] no text-based subtitle tracks to extract")
		return sessions
	}

	// Create a shared output directory for all tracks
	batchID := uuid.New().String()
	outputDir, err := os.MkdirTemp("", "subtitle-batch-"+batchID)
	if err != nil {
		log.Printf("[subtitle-extract] failed to create batch output dir: %v", err)
		return sessions
	}

	// Create session objects for each track (all share the same output dir)
	for _, track := range textTracks {
		vttPath := filepath.Join(outputDir, fmt.Sprintf("subtitles_%d.vtt", track.Index))
		sessionID := uuid.New().String()

		session := &SubtitleExtractSession{
			ID:            sessionID,
			Path:          path,
			SubtitleTrack: track.Index,
			OutputDir:     outputDir,
			VTTPath:       vttPath,
			CreatedAt:     time.Now(),
			LastAccess:    time.Now(),
		}

		m.mu.Lock()
		m.sessions[sessionID] = session
		m.mu.Unlock()

		sessions[track.Index] = session
		log.Printf("[subtitle-extract] created pre-extraction session %s for track %d (lang=%s)", sessionID, track.Index, track.Language)
	}

	// Start ONE ffmpeg process to extract all tracks
	go m.startBatchExtraction(path, outputDir, textTracks, sessions)

	log.Printf("[subtitle-extract] batch pre-extraction started for %d tracks with one ffmpeg process", len(textTracks))
	return sessions
}

// startBatchExtraction extracts all subtitle tracks in one ffmpeg invocation
func (m *SubtitleExtractManager) startBatchExtraction(path, outputDir string, tracks []SubtitleTrackInfo, sessions map[int]*SubtitleExtractSession) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Store cancel func in all sessions so they can be cancelled
	for _, session := range sessions {
		session.mu.Lock()
		session.cancel = cancel
		session.mu.Unlock()
	}

	markAllDone := func(err error) {
		for _, session := range sessions {
			session.mu.Lock()
			session.extractionDone = true
			session.extractionErr = err
			session.mu.Unlock()
		}
	}

	// Get stream URL
	var streamURL string
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		streamURL = path
	} else if directProvider, ok := m.streamer.(streaming.DirectURLProvider); ok {
		url, err := directProvider.GetDirectURL(ctx, path)
		if err == nil && url != "" {
			streamURL = url
		} else {
			webdavURL := m.buildWebDAVURL(path)
			if webdavURL != "" {
				streamURL = webdavURL
			} else {
				log.Printf("[subtitle-extract] batch extraction: failed to get URL: %v", err)
				markAllDone(fmt.Errorf("failed to get stream URL: %w", err))
				return
			}
		}
	} else {
		webdavURL := m.buildWebDAVURL(path)
		if webdavURL != "" {
			streamURL = webdavURL
		} else {
			markAllDone(fmt.Errorf("no URL provider available"))
			return
		}
	}

	// Build ffmpeg args with multiple -map outputs (like HLS does)
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-i", streamURL,
	}

	// Add each subtitle track as a separate output
	for _, track := range tracks {
		vttPath := filepath.Join(outputDir, fmt.Sprintf("subtitles_%d.vtt", track.Index))
		subtitleMap := fmt.Sprintf("0:s:%d", track.Index) // relative subtitle index
		args = append(args,
			"-map", subtitleMap,
			"-c", "webvtt",
			"-f", "webvtt",
			"-flush_packets", "1",
			vttPath,
		)
	}

	log.Printf("[subtitle-extract] batch extraction starting: %d tracks from %s", len(tracks), streamURL)

	cmd := exec.CommandContext(ctx, m.ffmpegPath, args...)

	// Capture stderr for logging
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		log.Printf("[subtitle-extract] batch extraction failed to start: %v", err)
		markAllDone(err)
		return
	}

	// Log stderr output
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				log.Printf("[subtitle-extract] batch ffmpeg: %s", strings.TrimSpace(string(buf[:n])))
			}
			if err != nil {
				break
			}
		}
	}()

	err := cmd.Wait()
	if err != nil {
		log.Printf("[subtitle-extract] batch extraction ffmpeg error: %v", err)
		markAllDone(err)
		return
	}

	log.Printf("[subtitle-extract] batch extraction completed successfully for %d tracks", len(tracks))
	markAllDone(nil)
}

// GetSession retrieves a session by ID
func (m *SubtitleExtractManager) GetSession(sessionID string) (*SubtitleExtractSession, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	session, exists := m.sessions[sessionID]
	return session, exists
}

// Shutdown cleans up all subtitle extraction sessions
func (m *SubtitleExtractManager) Shutdown() {
	close(m.cleanupDone)

	m.mu.Lock()
	defer m.mu.Unlock()

	for id, session := range m.sessions {
		log.Printf("[subtitle-extract] shutting down session %s", id)
		m.cleanupSessionLocked(session)
	}
	m.sessions = make(map[string]*SubtitleExtractSession)
}
