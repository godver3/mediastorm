package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultPlaylistTimeout   = 15 * time.Second
	defaultMaxPlaylistSize   = 50 * 1024 * 1024 // 50 MiB
	playlistContentTypePlain = "text/plain; charset=utf-8"
	liveStreamTimeout        = 30 * time.Minute
	defaultCacheTTL          = 24 * time.Hour
	cacheDir                 = "cache/live"
)

// LiveHandler proxies remote M3U playlists through the backend and can transmux
// individual live channel streams into browser-friendly MP4 fragments.
type LiveHandler struct {
	client             *http.Client
	maxSize            int64
	transmuxEnabled    bool
	ffmpegPath         string
	cacheTTL           time.Duration
	cacheMu            sync.RWMutex
	probeSizeMB        int  // FFmpeg probesize in MB (0 = default)
	analyzeDurationSec int  // FFmpeg analyzeduration in seconds (0 = default)
	lowLatency         bool // Enable low-latency mode
}

// NewLiveHandler creates a handler capable of fetching remote playlists.
// The provided client may be nil, in which case a client with sensible
// defaults will be created. cacheTTLHours specifies how long to cache playlists.
func NewLiveHandler(client *http.Client, transmuxEnabled bool, ffmpegPath string, cacheTTLHours int, probeSizeMB int, analyzeDurationSec int, lowLatency bool) *LiveHandler {
	if client == nil {
		client = &http.Client{
			Timeout: defaultPlaylistTimeout,
		}
	}

	// Ensure cache directory exists
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		log.Printf("[live] failed to create cache directory: %v", err)
	}

	cacheTTL := defaultCacheTTL
	if cacheTTLHours > 0 {
		cacheTTL = time.Duration(cacheTTLHours) * time.Hour
	}

	return &LiveHandler{
		client:             client,
		maxSize:            defaultMaxPlaylistSize,
		transmuxEnabled:    transmuxEnabled,
		ffmpegPath:         strings.TrimSpace(ffmpegPath),
		cacheTTL:           cacheTTL,
		probeSizeMB:        probeSizeMB,
		analyzeDurationSec: analyzeDurationSec,
		lowLatency:         lowLatency,
	}
}

func (h *LiveHandler) FetchPlaylist(w http.ResponseWriter, r *http.Request) {
	targetURL, err := h.parseRemoteURL(r.URL.Query().Get("url"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Check cache first
	cacheKey := h.getCacheKey(targetURL.String())
	cachedData, contentType, err := h.getFromCache(cacheKey)
	if err == nil && cachedData != nil {
		log.Printf("[live] serving playlist from cache for %s", targetURL.String())
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Cache", "HIT")
		_, _ = w.Write(cachedData)
		return
	}

	// Cache miss or expired, fetch from source
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, targetURL.String(), nil)
	if err != nil {
		http.Error(w, "failed to construct playlist request", http.StatusInternalServerError)
		return
	}

	resp, err := h.client.Do(req)
	if err != nil {
		http.Error(w, "failed to download playlist", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		http.Error(w, http.StatusText(resp.StatusCode), resp.StatusCode)
		return
	}

	limited := io.LimitReader(resp.Body, h.maxSize+1)
	body, err := io.ReadAll(limited)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	if err != nil {
		http.Error(w, "failed to read playlist", http.StatusBadGateway)
		return
	}

	if int64(len(body)) > h.maxSize {
		http.Error(w, "playlist exceeds size limit", http.StatusRequestEntityTooLarge)
		return
	}

	contentType = resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = playlistContentTypePlain
	}

	// Store in cache
	if err := h.saveToCache(cacheKey, body, contentType); err != nil {
		log.Printf("[live] failed to cache playlist: %v", err)
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Cache", "MISS")
	_, _ = w.Write(body)
}

func (h *LiveHandler) StreamChannel(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Accept-Ranges", "none")
		w.WriteHeader(http.StatusOK)
		return
	}

	if !h.transmuxEnabled {
		http.Error(w, "live transmuxing disabled", http.StatusNotImplemented)
		return
	}
	if h.ffmpegPath == "" {
		http.Error(w, "ffmpeg is not configured", http.StatusServiceUnavailable)
		return
	}

	targetURL, err := h.parseRemoteURL(r.URL.Query().Get("url"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), liveStreamTimeout)
	defer cancel()

	// Build FFmpeg args with optional buffering settings
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
	}

	// Add probesize if configured (value in MB, convert to bytes)
	if h.probeSizeMB > 0 {
		args = append(args, "-probesize", fmt.Sprintf("%d", h.probeSizeMB*1024*1024))
	}

	// Add analyzeduration if configured (value in seconds, convert to microseconds)
	if h.analyzeDurationSec > 0 {
		args = append(args, "-analyzeduration", fmt.Sprintf("%d", h.analyzeDurationSec*1000000))
	}

	// Low latency mode: reduce buffering
	if h.lowLatency {
		args = append(args, "-fflags", "+genpts+nobuffer+discardcorrupt", "-flags", "+low_delay")
	} else {
		args = append(args, "-fflags", "+genpts")
	}

	// Reconnection options
	args = append(args,
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "3",
		"-i", targetURL.String(),
		"-c:v", "copy",
		"-c:a", "aac",
		"-ac", "2",
		"-b:a", "128k",
		"-ar", "48000",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof+omit_tfhd_offset",
		"-f", "mp4",
		"-reset_timestamps", "1",
		"pipe:1",
	)

	cmd := exec.CommandContext(ctx, h.ffmpegPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "failed to prepare live stream", http.StatusInternalServerError)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		http.Error(w, "failed to prepare live stream", http.StatusInternalServerError)
		return
	}

	if err := cmd.Start(); err != nil {
		http.Error(w, "failed to start transmuxer", http.StatusBadGateway)
		return
	}

	go func() {
		_, _ = io.Copy(io.Discard, stderr)
	}()

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, proxy-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Accept-Ranges", "none")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 256*1024)

	for {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return
		default:
		}

		n, readErr := stdout.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				_ = cmd.Process.Kill()
				if !errors.Is(writeErr, context.Canceled) && !errors.Is(writeErr, io.EOF) && !isConnectionError(writeErr) {
					log.Printf("[live] writer error for %q: %v", targetURL.String(), writeErr)
				}
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			_ = cmd.Process.Kill()
			log.Printf("[live] ffmpeg read error for %q: %v", targetURL.String(), readErr)
			return
		}
	}

	if err := cmd.Wait(); err != nil {
		if !errors.Is(err, context.Canceled) && !strings.Contains(strings.ToLower(err.Error()), "broken pipe") {
			log.Printf("[live] ffmpeg exited with error for %q: %v", targetURL.String(), err)
		}
	}
}

func (h *LiveHandler) parseRemoteURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("missing url query parameter")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil {
		return nil, errors.New("invalid playlist url")
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return nil, fmt.Errorf("unsupported url scheme %q", parsed.Scheme)
	}

	return parsed, nil
}

func (h *LiveHandler) getCacheKey(playlistURL string) string {
	hash := sha256.Sum256([]byte(playlistURL))
	return hex.EncodeToString(hash[:])
}

func (h *LiveHandler) getCacheFilePath(key string) string {
	return filepath.Join(cacheDir, key+".m3u")
}

func (h *LiveHandler) getMetaFilePath(key string) string {
	return filepath.Join(cacheDir, key+".meta")
}

func (h *LiveHandler) getFromCache(key string) ([]byte, string, error) {
	h.cacheMu.RLock()
	defer h.cacheMu.RUnlock()

	cacheFile := h.getCacheFilePath(key)
	metaFile := h.getMetaFilePath(key)

	// Check if cache file exists and is not expired
	stat, err := os.Stat(cacheFile)
	if err != nil {
		return nil, "", err
	}

	// Check if cache is expired
	if time.Since(stat.ModTime()) > h.cacheTTL {
		return nil, "", errors.New("cache expired")
	}

	// Read cached playlist
	data, err := os.ReadFile(cacheFile)
	if err != nil {
		return nil, "", err
	}

	// Read content type from meta file
	contentType := playlistContentTypePlain
	if metaData, err := os.ReadFile(metaFile); err == nil {
		contentType = strings.TrimSpace(string(metaData))
	}

	return data, contentType, nil
}

func (h *LiveHandler) saveToCache(key string, data []byte, contentType string) error {
	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()

	// Ensure cache directory exists
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	cacheFile := h.getCacheFilePath(key)
	metaFile := h.getMetaFilePath(key)

	// Write playlist data
	if err := os.WriteFile(cacheFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}

	// Write content type to meta file
	if err := os.WriteFile(metaFile, []byte(contentType), 0644); err != nil {
		log.Printf("[live] failed to write meta file: %v", err)
	}

	return nil
}

// ClearCache removes all cached playlists, forcing a fresh fetch on next request.
func (h *LiveHandler) ClearCache(w http.ResponseWriter, r *http.Request) {
	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()

	// Read all files in cache directory
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			// Cache directory doesn't exist, nothing to clear
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok","cleared":0}`))
			return
		}
		http.Error(w, fmt.Sprintf(`{"error":"failed to read cache directory: %v"}`, err), http.StatusInternalServerError)
		return
	}

	cleared := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Only remove .m3u and .meta files
		if strings.HasSuffix(name, ".m3u") || strings.HasSuffix(name, ".meta") {
			path := filepath.Join(cacheDir, name)
			if err := os.Remove(path); err != nil {
				log.Printf("[live] failed to remove cache file %s: %v", name, err)
			} else {
				cleared++
			}
		}
	}

	log.Printf("[live] cleared %d cached playlist files", cleared)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"ok","cleared":%d}`, cleared)))
}
