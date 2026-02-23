package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
)

// ImageHandler handles image proxying with resize and caching
type ImageHandler struct {
	cacheDir   string
	httpc      *http.Client
	mu         sync.RWMutex
	inProgress map[string]chan struct{} // Prevent duplicate fetches
}

// NewImageHandler creates a new image proxy handler
func NewImageHandler(cacheDir string) *ImageHandler {
	// Create cache directory if needed
	imgCacheDir := filepath.Join(cacheDir, "images")
	if err := os.MkdirAll(imgCacheDir, 0755); err != nil {
		log.Printf("[ImageProxy] Warning: could not create cache dir %s: %v", imgCacheDir, err)
	}

	return &ImageHandler{
		cacheDir: imgCacheDir,
		httpc: &http.Client{
			Timeout: 30 * time.Second,
		},
		inProgress: make(map[string]chan struct{}),
	}
}

// Proxy handles image proxy requests
// Query params:
//   - url: source image URL (required)
//   - w: target width (optional, default: original)
//   - q: JPEG quality 1-100 (optional, default: 80)
func (h *ImageHandler) Proxy(w http.ResponseWriter, r *http.Request) {
	sourceURL := r.URL.Query().Get("url")

	if sourceURL == "" {
		http.Error(w, "url parameter required", http.StatusBadRequest)
		return
	}

	// Validate URL is from allowed image sources (exact hostname match)
	allowedImageHosts := map[string]struct{}{
		"image.tmdb.org":   {},
		"img.youtube.com":  {},
	}
	parsedSource, err := url.Parse(sourceURL)
	if err != nil || parsedSource.Host == "" {
		http.Error(w, "invalid URL", http.StatusBadRequest)
		return
	}
	if _, ok := allowedImageHosts[parsedSource.Hostname()]; !ok {
		http.Error(w, "URL not allowed", http.StatusForbidden)
		return
	}

	// Parse target width (0 = original size)
	targetWidth := 0
	if wStr := r.URL.Query().Get("w"); wStr != "" {
		if w, err := strconv.Atoi(wStr); err == nil && w > 0 && w <= 2000 {
			targetWidth = w
		}
	}

	// JPEG quality (default 80, good balance of size and quality)
	quality := 80
	if qStr := r.URL.Query().Get("q"); qStr != "" {
		if q, err := strconv.Atoi(qStr); err == nil && q >= 1 && q <= 100 {
			quality = q
		}
	}

	// Generate cache key from URL + width + quality
	cacheKey := h.cacheKey(sourceURL, targetWidth, quality)
	cachePath := filepath.Join(h.cacheDir, cacheKey+".jpg")

	// Check cache first
	if data, err := os.ReadFile(cachePath); err == nil {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "public, max-age=2592000") // 30 days
		w.Header().Set("X-Cache", "HIT")
		w.Write(data)
		return
	}

	// Prevent duplicate fetches for the same image
	h.mu.Lock()
	if ch, exists := h.inProgress[cacheKey]; exists {
		h.mu.Unlock()
		// Wait for other request to finish
		<-ch
		// Now try to serve from cache
		if data, err := os.ReadFile(cachePath); err == nil {
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Cache-Control", "public, max-age=2592000")
			w.Header().Set("X-Cache", "HIT")
			w.Write(data)
			return
		}
		http.Error(w, "Failed to load image", http.StatusInternalServerError)
		return
	}
	// Mark as in progress
	ch := make(chan struct{})
	h.inProgress[cacheKey] = ch
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.inProgress, cacheKey)
		close(ch)
		h.mu.Unlock()
	}()

	// Fetch the image
	resp, err := h.httpc.Get(sourceURL)
	if err != nil {
		log.Printf("[ImageProxy] Fetch error for %s: %v", sourceURL, err)
		http.Error(w, "Failed to fetch image", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[ImageProxy] Fetch returned %d for %s", resp.StatusCode, sourceURL)
		http.Error(w, "Image source error", resp.StatusCode)
		return
	}

	// Decode the image
	img, _, err := image.Decode(resp.Body)
	if err != nil {
		log.Printf("[ImageProxy] Decode error for %s: %v", sourceURL, err)
		http.Error(w, "Failed to decode image", http.StatusInternalServerError)
		return
	}

	// Resize if requested
	if targetWidth > 0 {
		bounds := img.Bounds()
		origWidth := bounds.Dx()
		origHeight := bounds.Dy()

		// Only resize if target is smaller than original
		if targetWidth < origWidth {
			ratio := float64(targetWidth) / float64(origWidth)
			targetHeight := int(float64(origHeight) * ratio)

			// Create new image with target dimensions
			dst := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))

			// Use CatmullRom for high quality downscaling
			draw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)
			img = dst
		}
	}

	// Encode as JPEG for consistent output and better compression
	tmpPath := cachePath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		log.Printf("[ImageProxy] Cache create error: %v", err)
		// Still serve the image, just don't cache
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("X-Cache", "MISS-NOCACHE")
		jpeg.Encode(w, img, &jpeg.Options{Quality: quality})
		return
	}

	// Encode to temp file
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: quality}); err != nil {
		f.Close()
		os.Remove(tmpPath)
		log.Printf("[ImageProxy] Encode error: %v", err)
		http.Error(w, "Failed to encode image", http.StatusInternalServerError)
		return
	}
	f.Close()

	// Atomic rename
	if err := os.Rename(tmpPath, cachePath); err != nil {
		os.Remove(tmpPath)
		log.Printf("[ImageProxy] Cache rename error: %v", err)
	}

	// Serve from cache
	data, err := os.ReadFile(cachePath)
	if err != nil {
		http.Error(w, "Failed to read cached image", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=2592000") // 30 days
	w.Header().Set("X-Cache", "MISS")
	w.Write(data)
}

// cacheKey generates a unique cache key for the image
func (h *ImageHandler) cacheKey(url string, width, quality int) string {
	data := fmt.Sprintf("%s|%d|%d", url, width, quality)
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:16]) // 32 char hex string
}

// Options handles CORS preflight
func (h *ImageHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// ClearCache removes all cached images
func (h *ImageHandler) ClearCache() error {
	entries, err := os.ReadDir(h.cacheDir)
	if err != nil {
		return err
	}

	var errs []error
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jpg") {
			if err := os.Remove(filepath.Join(h.cacheDir, entry.Name())); err != nil {
				errs = append(errs, err)
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to remove %d files", len(errs))
	}
	return nil
}

// CacheStats returns cache statistics
func (h *ImageHandler) CacheStats() (count int, sizeBytes int64) {
	entries, err := os.ReadDir(h.cacheDir)
	if err != nil {
		return 0, 0
	}

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jpg") {
			count++
			if info, err := entry.Info(); err == nil {
				sizeBytes += info.Size()
			}
		}
	}
	return
}

// Unused imports guard - these are actually used
var _ = jpeg.Encode
var _ = png.Decode
var _ = io.Copy
