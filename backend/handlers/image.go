package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
)

const imageProxyDefaultQuality = 80

type imageWarmRequest struct {
	Images []imageWarmItem `json:"images"`
}

type imageWarmItem struct {
	URL     string `json:"url"`
	Width   int    `json:"width,omitempty"`
	Quality int    `json:"quality,omitempty"`
}

type imageWarmResult struct {
	URL     string `json:"url"`
	Width   int    `json:"width,omitempty"`
	Quality int    `json:"quality,omitempty"`
	Cached  bool   `json:"cached"`
	Error   string `json:"error,omitempty"`
}

type imageWarmResponse struct {
	Results []imageWarmResult `json:"results"`
	Warmed  int               `json:"warmed"`
	Cached  int               `json:"cached"`
	Failed  int               `json:"failed"`
}

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

	if err := validateProxyImageURL(sourceURL); err != nil {
		http.Error(w, "invalid URL", http.StatusBadRequest)
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
	quality := imageProxyDefaultQuality
	if qStr := r.URL.Query().Get("q"); qStr != "" {
		if q, err := strconv.Atoi(qStr); err == nil && q >= 1 && q <= 100 {
			quality = q
		}
	}

	_, data, cached, err := h.ensureCached(sourceURL, targetWidth, quality)
	if err != nil {
		status := http.StatusBadGateway
		if strings.Contains(err.Error(), "decode") || strings.Contains(err.Error(), "encode") {
			status = http.StatusInternalServerError
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=2592000") // 30 days
	if cached {
		w.Header().Set("X-Cache", "HIT")
	} else {
		w.Header().Set("X-Cache", "MISS")
	}
	w.Write(data)
}

func validateProxyImageURL(sourceURL string) error {
	allowedImageHosts := map[string]struct{}{
		"image.tmdb.org":       {},
		"img.youtube.com":      {},
		"artworks.thetvdb.com": {},
		"thetvdb.com":          {},
		"www.thetvdb.com":      {},
	}
	parsedSource, err := url.Parse(sourceURL)
	if err != nil || parsedSource.Host == "" {
		return fmt.Errorf("invalid URL")
	}
	if _, ok := allowedImageHosts[parsedSource.Hostname()]; !ok {
		return fmt.Errorf("URL not allowed")
	}
	return nil
}

func normalizeProxyWidth(width int) int {
	if width > 0 && width <= 2000 {
		return width
	}
	return 0
}

func normalizeProxyQuality(quality int) int {
	if quality >= 1 && quality <= 100 {
		return quality
	}
	return imageProxyDefaultQuality
}

func (h *ImageHandler) ensureCached(sourceURL string, targetWidth, quality int) (string, []byte, bool, error) {
	cacheKey := h.cacheKey(sourceURL, targetWidth, quality)
	cachePath := filepath.Join(h.cacheDir, cacheKey+".jpg")
	if data, err := os.ReadFile(cachePath); err == nil {
		return cachePath, data, true, nil
	}

	// Prevent duplicate fetches for the same image
	h.mu.Lock()
	if ch, exists := h.inProgress[cacheKey]; exists {
		h.mu.Unlock()
		// Wait for other request to finish
		<-ch
		// Now try to serve from cache
		if data, err := os.ReadFile(cachePath); err == nil {
			return cachePath, data, true, nil
		}
		return cachePath, nil, false, fmt.Errorf("failed to load image")
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
		return cachePath, nil, false, fmt.Errorf("failed to fetch image")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[ImageProxy] Fetch returned %d for %s", resp.StatusCode, sourceURL)
		return cachePath, nil, false, fmt.Errorf("image source error")
	}

	// Decode the image
	img, _, err := image.Decode(resp.Body)
	if err != nil {
		log.Printf("[ImageProxy] Decode error for %s: %v", sourceURL, err)
		return cachePath, nil, false, fmt.Errorf("failed to decode image")
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
		var buf bytes.Buffer
		if encodeErr := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); encodeErr != nil {
			return cachePath, nil, false, fmt.Errorf("failed to encode image")
		}
		return cachePath, buf.Bytes(), false, nil
	}

	// Encode to temp file
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: quality}); err != nil {
		f.Close()
		os.Remove(tmpPath)
		log.Printf("[ImageProxy] Encode error: %v", err)
		return cachePath, nil, false, fmt.Errorf("failed to encode image")
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
		return cachePath, nil, false, fmt.Errorf("failed to read cached image")
	}

	return cachePath, data, false, nil
}

// Warm pre-fills the image proxy cache for a batch of source URLs and resize widths.
func (h *ImageHandler) Warm(w http.ResponseWriter, r *http.Request) {
	var req imageWarmRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid request body"})
		return
	}
	if len(req.Images) > 64 {
		req.Images = req.Images[:64]
	}

	response := imageWarmResponse{Results: make([]imageWarmResult, 0, len(req.Images))}
	seen := make(map[string]struct{})
	for _, item := range req.Images {
		sourceURL := strings.TrimSpace(item.URL)
		width := normalizeProxyWidth(item.Width)
		quality := normalizeProxyQuality(item.Quality)
		result := imageWarmResult{URL: sourceURL, Width: width, Quality: quality}
		key := h.cacheKey(sourceURL, width, quality)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		if sourceURL == "" {
			result.Error = "url required"
			response.Failed++
			response.Results = append(response.Results, result)
			continue
		}
		if err := validateProxyImageURL(sourceURL); err != nil {
			result.Error = err.Error()
			response.Failed++
			response.Results = append(response.Results, result)
			continue
		}

		_, _, cached, err := h.ensureCached(sourceURL, width, quality)
		if err != nil {
			result.Error = err.Error()
			response.Failed++
		} else if cached {
			result.Cached = true
			response.Cached++
		} else {
			response.Warmed++
		}
		response.Results = append(response.Results, result)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
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
