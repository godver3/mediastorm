package metadata

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// TrailerStatus represents the current state of a prequeued trailer download
type TrailerStatus string

const (
	TrailerStatusPending     TrailerStatus = "pending"
	TrailerStatusDownloading TrailerStatus = "downloading"
	TrailerStatusReady       TrailerStatus = "ready"
	TrailerStatusFailed      TrailerStatus = "failed"
)

// TrailerPrequeueItem represents a trailer being downloaded or already downloaded
type TrailerPrequeueItem struct {
	ID             string        `json:"id"`
	VideoURL       string        `json:"videoUrl"`
	Status         TrailerStatus `json:"status"`
	FilePath       string        `json:"-"` // Internal path, not exposed
	Error          string        `json:"error,omitempty"`
	CreatedAt      time.Time     `json:"createdAt"`
	ReadyAt        *time.Time    `json:"readyAt,omitempty"`
	LastAccessedAt *time.Time    `json:"-"` // Track when trailer was last served
	FileSize       int64         `json:"fileSize,omitempty"`
}

// TrailerPrequeueManager manages trailer downloads and temporary file storage
type TrailerPrequeueManager struct {
	mu            sync.RWMutex
	items         map[string]*TrailerPrequeueItem
	tempDir       string
	maxAge        time.Duration // Max age for failed/pending items before cleanup
	cleanupC      chan struct{} // Signal to stop cleanup
	cleanupActive bool          // Whether cleanup goroutine is running
}

// NewTrailerPrequeueManager creates a new prequeue manager
func NewTrailerPrequeueManager(tempDir string) (*TrailerPrequeueManager, error) {
	// Create temp directory if it doesn't exist
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create trailer temp dir: %w", err)
	}

	mgr := &TrailerPrequeueManager{
		items:   make(map[string]*TrailerPrequeueItem),
		tempDir: tempDir,
		maxAge:  30 * time.Minute, // Cleanup stale failed/pending items after 30 min
	}

	log.Printf("[trailer-prequeue] initialized manager (temp dir: %s)", tempDir)
	return mgr, nil
}

// generateID creates a unique ID for a video URL
func (m *TrailerPrequeueManager) generateID(videoURL string) string {
	hash := sha256.Sum256([]byte(videoURL))
	return hex.EncodeToString(hash[:16]) // First 16 bytes = 32 hex chars
}

// Prequeue starts downloading a trailer in the background
// Returns the prequeue ID immediately
func (m *TrailerPrequeueManager) Prequeue(videoURL string) string {
	id := m.generateID(videoURL)

	m.mu.Lock()
	// Check if already exists
	if existing, ok := m.items[id]; ok {
		m.mu.Unlock()
		// If failed, allow retry
		if existing.Status != TrailerStatusFailed {
			log.Printf("[trailer-prequeue] already queued: %s (status: %s)", id, existing.Status)
			return id
		}
		// Reset for retry
		m.mu.Lock()
	}

	item := &TrailerPrequeueItem{
		ID:        id,
		VideoURL:  videoURL,
		Status:    TrailerStatusPending,
		CreatedAt: time.Now(),
	}
	m.items[id] = item

	// Start cleanup goroutine if not already running
	if !m.cleanupActive {
		m.cleanupActive = true
		m.cleanupC = make(chan struct{})
		go m.cleanupLoop()
		log.Printf("[trailer-prequeue] started cleanup goroutine")
	}
	m.mu.Unlock()

	// Start download in background
	go m.downloadTrailer(id, videoURL)

	log.Printf("[trailer-prequeue] queued: %s for %s", id, videoURL)
	return id
}

// GetStatus returns the current status of a prequeued trailer
func (m *TrailerPrequeueManager) GetStatus(id string) (*TrailerPrequeueItem, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	item, ok := m.items[id]
	if !ok {
		return nil, false
	}

	// Return a copy to avoid race conditions
	copy := *item
	return &copy, true
}

// ServeTrailer serves the downloaded trailer file with proper range request support
func (m *TrailerPrequeueManager) ServeTrailer(id string, w http.ResponseWriter, r *http.Request) error {
	m.mu.Lock()
	item, ok := m.items[id]
	if ok {
		// Record access time for cleanup tracking
		now := time.Now()
		item.LastAccessedAt = &now
	}
	m.mu.Unlock()

	if !ok {
		return fmt.Errorf("trailer not found: %s", id)
	}

	if item.Status != TrailerStatusReady {
		return fmt.Errorf("trailer not ready (status: %s)", item.Status)
	}

	// Open the file
	file, err := os.Open(item.FilePath)
	if err != nil {
		return fmt.Errorf("failed to open trailer file: %w", err)
	}
	defer file.Close()

	// Get file info for Content-Length
	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat trailer file: %w", err)
	}

	// Set content type
	w.Header().Set("Content-Type", "video/mp4")

	// Use http.ServeContent for proper range request support
	http.ServeContent(w, r, item.FilePath, stat.ModTime(), file)
	return nil
}

// downloadTrailer performs the actual download using yt-dlp + ffmpeg
func (m *TrailerPrequeueManager) downloadTrailer(id, videoURL string) {
	m.mu.Lock()
	item, ok := m.items[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	item.Status = TrailerStatusDownloading
	m.mu.Unlock()

	log.Printf("[trailer-prequeue] starting download: %s", id)

	// Find yt-dlp
	ytdlpPath := "/usr/local/bin/yt-dlp"
	if _, err := exec.LookPath(ytdlpPath); err != nil {
		ytdlpPath = "yt-dlp"
		if _, err := exec.LookPath(ytdlpPath); err != nil {
			m.setFailed(id, "yt-dlp not found")
			return
		}
	}

	// Output path
	outputPath := filepath.Join(m.tempDir, id+".mp4")

	// Use yt-dlp with format selection and ffmpeg merge
	// Format 137 = 1080p video, 140 = audio
	// Fallback to best available if 1080p not available
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, ytdlpPath,
		"-f", "137+140/bestvideo[height<=1080]+bestaudio/best",
		"--merge-output-format", "mp4",
		"--no-warnings",
		"--no-playlist",
		"-o", outputPath,
		videoURL,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := stderr.String()
		if errMsg == "" {
			errMsg = err.Error()
		}
		log.Printf("[trailer-prequeue] download failed for %s: %v, stderr: %s", id, err, errMsg)
		m.setFailed(id, fmt.Sprintf("download failed: %s", strings.TrimSpace(errMsg)))
		return
	}

	// Verify file exists and get size
	stat, err := os.Stat(outputPath)
	if err != nil {
		log.Printf("[trailer-prequeue] output file not found for %s: %v", id, err)
		m.setFailed(id, "output file not found")
		return
	}

	// Update status to ready
	m.mu.Lock()
	if item, ok := m.items[id]; ok {
		now := time.Now()
		item.Status = TrailerStatusReady
		item.FilePath = outputPath
		item.ReadyAt = &now
		item.FileSize = stat.Size()
	}
	m.mu.Unlock()

	log.Printf("[trailer-prequeue] download complete: %s (size: %d bytes)", id, stat.Size())
}

// setFailed marks a trailer as failed
func (m *TrailerPrequeueManager) setFailed(id, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if item, ok := m.items[id]; ok {
		item.Status = TrailerStatusFailed
		item.Error = errMsg
	}
}

// Cleanup timing constants
const (
	// How often to run cleanup
	cleanupInterval = 30 * time.Second
	// Delete ready trailers not accessed within this time
	unusedTimeout = 1 * time.Minute
	// Delete trailers this long after last access
	postAccessTimeout = 2 * time.Minute
)

// cleanupLoop periodically cleans up old trailer files
// Stops automatically when no items remain
func (m *TrailerPrequeueManager) cleanupLoop() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			hasItems := m.cleanup()
			if !hasItems {
				// No more items - stop the cleanup goroutine
				m.mu.Lock()
				m.cleanupActive = false
				m.mu.Unlock()
				log.Printf("[trailer-prequeue] stopped cleanup goroutine (no items)")
				return
			}
		case <-m.cleanupC:
			m.mu.Lock()
			m.cleanupActive = false
			m.mu.Unlock()
			return
		}
	}
}

// cleanup removes trailer files based on access patterns:
// - Ready trailers not accessed within 1 minute are deleted
// - Accessed trailers are deleted 2 minutes after last access
// - Failed/pending trailers older than maxAge are deleted
// Returns true if there are still items remaining
func (m *TrailerPrequeueManager) cleanup() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	toDelete := make([]string, 0)

	for id, item := range m.items {
		shouldDelete := false
		reason := ""

		switch item.Status {
		case TrailerStatusReady:
			if item.LastAccessedAt != nil {
				// Was accessed - delete after postAccessTimeout
				if now.Sub(*item.LastAccessedAt) > postAccessTimeout {
					shouldDelete = true
					reason = fmt.Sprintf("accessed %s ago", now.Sub(*item.LastAccessedAt).Round(time.Second))
				}
			} else if item.ReadyAt != nil {
				// Never accessed - delete after unusedTimeout from ready time
				if now.Sub(*item.ReadyAt) > unusedTimeout {
					shouldDelete = true
					reason = fmt.Sprintf("ready but unused for %s", now.Sub(*item.ReadyAt).Round(time.Second))
				}
			}
		case TrailerStatusFailed, TrailerStatusPending, TrailerStatusDownloading:
			// Clean up stale failed/pending items after maxAge
			if now.Sub(item.CreatedAt) > m.maxAge {
				shouldDelete = true
				reason = fmt.Sprintf("stale %s item", item.Status)
			}
		}

		if shouldDelete {
			toDelete = append(toDelete, id)
			log.Printf("[trailer-prequeue] marking for cleanup: %s (%s)", id, reason)
		}
	}

	for _, id := range toDelete {
		item := m.items[id]
		// Delete file if it exists
		if item.FilePath != "" {
			if err := os.Remove(item.FilePath); err != nil && !os.IsNotExist(err) {
				log.Printf("[trailer-prequeue] failed to delete file %s: %v", item.FilePath, err)
			}
		}
		delete(m.items, id)
	}

	if len(toDelete) > 0 {
		log.Printf("[trailer-prequeue] cleanup complete: removed %d trailers", len(toDelete))
	}

	return len(m.items) > 0
}

// Stop stops the cleanup goroutine if running
func (m *TrailerPrequeueManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cleanupActive && m.cleanupC != nil {
		close(m.cleanupC)
		m.cleanupActive = false
	}
}
