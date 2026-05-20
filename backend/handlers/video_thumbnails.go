package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
)

const (
	thumbnailDefaultIntervalSec = 60
	thumbnailMinIntervalSec     = 30
	thumbnailMaxCount           = 120
	thumbnailInitialPassCount   = 24
	thumbnailSecondPassCount    = 60
	thumbnailWorkerCount        = 2
	thumbnailWidth              = 240
	thumbnailFrameTimeout       = 45 * time.Second
	thumbnailFilterVersion      = 6
)

type ThumbnailManager struct {
	baseDir    string
	ffmpegPath string

	mu       sync.Mutex
	inFlight map[string]struct{}
}

type thumbnailManifest struct {
	Key         string             `json:"key"`
	Status      string             `json:"status"`
	PathHash    string             `json:"pathHash"`
	DurationSec float64            `json:"durationSec"`
	IntervalSec int                `json:"intervalSec"`
	Generated   int                `json:"generated"`
	Total       int                `json:"total"`
	UpdatedAt   time.Time          `json:"updatedAt"`
	Error       string             `json:"error,omitempty"`
	ToneMapped  bool               `json:"toneMapped,omitempty"`
	DVProfile   string             `json:"dvProfile,omitempty"`
	FilterVer   int                `json:"filterVersion,omitempty"`
	Thumbnails  []thumbnailDetails `json:"thumbnails"`
}

type thumbnailDetails struct {
	TimeSec float64 `json:"timeSec"`
	File    string  `json:"file"`
}

type thumbnailStatusResponse struct {
	Key         string             `json:"key"`
	Status      string             `json:"status"`
	DurationSec float64            `json:"durationSec"`
	IntervalSec int                `json:"intervalSec"`
	Generated   int                `json:"generated"`
	Total       int                `json:"total"`
	UpdatedAt   time.Time          `json:"updatedAt"`
	Error       string             `json:"error,omitempty"`
	ToneMapped  bool               `json:"toneMapped,omitempty"`
	DVProfile   string             `json:"dvProfile,omitempty"`
	FilterVer   int                `json:"filterVersion,omitempty"`
	Thumbnails  []thumbnailURLItem `json:"thumbnails"`
}

type thumbnailURLItem struct {
	TimeSec float64 `json:"timeSec"`
	URL     string  `json:"url"`
}

type thumbnailJob struct {
	TimeSec    float64
	FileName   string
	OutputPath string
}

type thumbnailResult struct {
	Details thumbnailDetails
	OK      bool
}

func NewThumbnailManager(baseDir, ffmpegPath string) *ThumbnailManager {
	return &ThumbnailManager{
		baseDir:    baseDir,
		ffmpegPath: ffmpegPath,
		inFlight:   make(map[string]struct{}),
	}
}

func thumbnailKey(cleanPath string) string {
	sum := sha256.Sum256([]byte(cleanPath))
	return hex.EncodeToString(sum[:])[:24]
}

func validThumbnailKey(key string) bool {
	if len(key) != 24 {
		return false
	}
	_, err := hex.DecodeString(key)
	return err == nil
}

func cleanVideoPathParam(raw string) string {
	cleanPath := strings.TrimSpace(raw)
	if strings.HasPrefix(cleanPath, "/webdav/") {
		return strings.TrimPrefix(cleanPath, "/webdav")
	}
	if strings.HasPrefix(cleanPath, "webdav/") {
		return "/" + strings.TrimPrefix(cleanPath, "webdav/")
	}
	return cleanPath
}

func (m *ThumbnailManager) manifestPath(key string) string {
	return filepath.Join(m.baseDir, key, "manifest.json")
}

func (m *ThumbnailManager) readManifest(key string) (*thumbnailManifest, error) {
	data, err := os.ReadFile(m.manifestPath(key))
	if err != nil {
		return nil, err
	}
	var manifest thumbnailManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func (m *ThumbnailManager) writeManifest(manifest *thumbnailManifest) error {
	dir := filepath.Join(m.baseDir, manifest.Key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	manifest.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "manifest.json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "manifest.json"))
}

func thumbnailTimes(durationSec float64, requestedInterval int) (int, []float64) {
	if durationSec <= 0 || math.IsNaN(durationSec) || math.IsInf(durationSec, 0) {
		return thumbnailDefaultIntervalSec, nil
	}
	interval := requestedInterval
	if interval <= 0 {
		interval = thumbnailDefaultIntervalSec
	}
	if interval < thumbnailMinIntervalSec {
		interval = thumbnailMinIntervalSec
	}
	estimated := int(math.Ceil(durationSec / float64(interval)))
	if estimated > thumbnailMaxCount {
		interval = int(math.Ceil(durationSec / float64(thumbnailMaxCount)))
	}

	var times []float64
	for t := float64(interval) / 2; t < durationSec-1; t += float64(interval) {
		times = append(times, math.Round(t*10)/10)
	}
	if len(times) == 0 && durationSec > 5 {
		times = append(times, math.Round((durationSec/2)*10)/10)
	}
	if len(times) > thumbnailMaxCount {
		times = times[:thumbnailMaxCount]
	}
	return interval, times
}

func thumbnailGenerationOrder(count int) []int {
	if count <= 0 {
		return nil
	}
	seen := make([]bool, count)
	order := make([]int, 0, count)
	add := func(idx int) {
		if idx < 0 || idx >= count || seen[idx] {
			return
		}
		seen[idx] = true
		order = append(order, idx)
	}
	addSpread := func(target int) {
		if target <= 0 {
			return
		}
		if target > count {
			target = count
		}
		if target == 1 {
			add(0)
			return
		}
		for i := 0; i < target; i++ {
			idx := int(math.Round(float64(i) * float64(count-1) / float64(target-1)))
			add(idx)
		}
	}

	addSpread(thumbnailInitialPassCount)
	addSpread(thumbnailSecondPassCount)
	addSpread(count)
	return order
}

func thumbnailNeedsToneMap(metadata *videoMetadataResponse) bool {
	if metadata == nil || len(metadata.VideoStreams) == 0 {
		return false
	}
	stream := metadata.VideoStreams[0]
	if stream.HasDolbyVision || strings.TrimSpace(stream.HdrFormat) != "" {
		return true
	}
	transfer := strings.ToLower(strings.TrimSpace(stream.ColorTransfer))
	return transfer == "smpte2084" || transfer == "arib-std-b67"
}

func isDolbyVisionProfile5(profile string) bool {
	normalized := strings.ToLower(strings.TrimSpace(profile))
	return strings.Contains(normalized, "dvhe.05") || strings.Contains(normalized, "dvh1.05") || strings.Contains(normalized, "profile 5") || normalized == "5"
}

func parseThumbnailDVProfile(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("dvProfile"))
}

func parseThumbnailToneMapHint(r *http.Request, dvProfile string) bool {
	if isDolbyVisionProfile5(dvProfile) {
		return false
	}
	q := r.URL.Query()
	return parseBoolQuery(q.Get("toneMap")) || parseBoolQuery(q.Get("hdr")) || parseBoolQuery(q.Get("dv"))
}

func parseBoolQuery(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func thumbnailFilter(toneMap bool) string {
	if !toneMap {
		return fmt.Sprintf("scale=%d:-2:flags=lanczos,format=yuvj420p,setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709:range=full", thumbnailWidth)
	}
	return fmt.Sprintf("format=gbrpf32le,tonemap=tonemap=mobius:param=0.35:desat=0:peak=1000,eq=brightness=0.18:contrast=1.38:saturation=2.15:gamma=0.85,scale=%d:-2:flags=lanczos,format=yuvj420p,setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709:range=full", thumbnailWidth)
}

func thumbnailInputUnavailable(output []byte) bool {
	text := strings.ToLower(string(output))
	return strings.Contains(text, "error opening input") ||
		strings.Contains(text, "connection refused") ||
		strings.Contains(text, "operation timed out") ||
		strings.Contains(text, "server returned 4xx") ||
		strings.Contains(text, "server returned 5xx")
}

func (m *ThumbnailManager) markUnsupported(cleanPath string, durationSec float64, requestedInterval int, dvProfile string, reason string) (string, error) {
	key := thumbnailKey(cleanPath)
	dir := filepath.Join(m.baseDir, key)
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("failed to clear unsupported thumbnail cache: %w", err)
	}
	interval, times := thumbnailTimes(durationSec, requestedInterval)
	manifest := &thumbnailManifest{
		Key:         key,
		Status:      "failed",
		PathHash:    key,
		DurationSec: durationSec,
		IntervalSec: interval,
		Generated:   0,
		Total:       len(times),
		Error:       reason,
		ToneMapped:  false,
		DVProfile:   strings.TrimSpace(dvProfile),
		FilterVer:   thumbnailFilterVersion,
		Thumbnails:  []thumbnailDetails{},
	}
	if err := m.writeManifest(manifest); err != nil {
		return "", err
	}
	return key, nil
}

func (m *ThumbnailManager) start(cleanPath, sourceURL string, durationSec float64, intervalSec int, toneMap bool, dvProfile string) (string, bool, error) {
	if m == nil {
		return "", false, fmt.Errorf("thumbnail manager unavailable")
	}
	key := thumbnailKey(cleanPath)
	if manifest, err := m.readManifest(key); err == nil {
		compatible := (!toneMap || manifest.ToneMapped) && strings.EqualFold(strings.TrimSpace(manifest.DVProfile), strings.TrimSpace(dvProfile)) && manifest.FilterVer == thumbnailFilterVersion
		if compatible && manifest.Status == "ready" && manifest.Generated > 0 {
			return key, false, nil
		}
		if !compatible || manifest.Status == "failed" {
			if err := os.RemoveAll(filepath.Join(m.baseDir, key)); err != nil {
				return "", false, fmt.Errorf("failed to clear stale thumbnail cache for regeneration: %w", err)
			}
		}
	} else if err != nil && !os.IsNotExist(err) {
		if err := os.RemoveAll(filepath.Join(m.baseDir, key)); err != nil {
			return "", false, fmt.Errorf("failed to clear stale thumbnail cache for regeneration: %w", err)
		}
	}

	m.mu.Lock()
	if _, exists := m.inFlight[key]; exists {
		m.mu.Unlock()
		return key, false, nil
	}
	m.inFlight[key] = struct{}{}
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.inFlight, key)
			m.mu.Unlock()
		}()
		m.generate(key, cleanPath, sourceURL, durationSec, intervalSec, toneMap, dvProfile)
	}()

	return key, true, nil
}

func (m *ThumbnailManager) generate(key, cleanPath, sourceURL string, durationSec float64, requestedInterval int, toneMap bool, dvProfile string) {
	interval, times := thumbnailTimes(durationSec, requestedInterval)
	manifest := &thumbnailManifest{
		Key:         key,
		Status:      "generating",
		PathHash:    key,
		DurationSec: durationSec,
		IntervalSec: interval,
		Total:       len(times),
		ToneMapped:  toneMap,
		DVProfile:   strings.TrimSpace(dvProfile),
		FilterVer:   thumbnailFilterVersion,
		Thumbnails:  []thumbnailDetails{},
	}
	if err := m.writeManifest(manifest); err != nil {
		log.Printf("[thumbnails] failed to write initial manifest key=%s: %v", key, err)
		return
	}
	if len(times) == 0 {
		manifest.Status = "failed"
		manifest.Error = "duration too short or unavailable"
		_ = m.writeManifest(manifest)
		return
	}
	if toneMap {
		log.Printf("[thumbnails] tone-map enabled key=%s path=%q", key, cleanPath)
	} else if isDolbyVisionProfile5(dvProfile) {
		log.Printf("[thumbnails] generating DV profile 5 thumbnails without tone-map key=%s path=%q", key, cleanPath)
	}

	dir := filepath.Join(m.baseDir, key)
	order := thumbnailGenerationOrder(len(times))
	jobs := make(chan thumbnailJob)
	results := make(chan thumbnailResult)
	workerCount := thumbnailWorkerCount
	if len(order) < workerCount {
		workerCount = len(order)
	}

	for worker := 0; worker < workerCount; worker++ {
		go func() {
			for job := range jobs {
				results <- m.generateFrame(job, key, cleanPath, sourceURL, toneMap)
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, idx := range order {
			t := times[idx]
			fileName := fmt.Sprintf("thumb-%04d.jpg", idx+1)
			jobs <- thumbnailJob{
				TimeSec:    t,
				FileName:   fileName,
				OutputPath: filepath.Join(dir, fileName),
			}
		}
	}()

	for range order {
		result := <-results
		if !result.OK {
			continue
		}
		manifest.Thumbnails = append(manifest.Thumbnails, result.Details)
		manifest.Generated = len(manifest.Thumbnails)
		_ = m.writeManifest(manifest)
	}

	if manifest.Generated == 0 {
		manifest.Status = "failed"
		manifest.Error = "no thumbnails generated"
	} else {
		manifest.Status = "ready"
	}
	_ = m.writeManifest(manifest)
	log.Printf("[thumbnails] complete key=%s path=%q generated=%d/%d", key, cleanPath, manifest.Generated, manifest.Total)
}

func (m *ThumbnailManager) generateFrame(job thumbnailJob, key, cleanPath, sourceURL string, toneMap bool) thumbnailResult {
	details := thumbnailDetails{TimeSec: job.TimeSec, File: job.FileName}
	if _, err := os.Stat(job.OutputPath); err == nil {
		return thumbnailResult{Details: details, OK: true}
	}

	output, err := m.runFrameCommand(job, sourceURL, thumbnailFilter(toneMap))
	if err != nil && toneMap && !thumbnailInputUnavailable(output) {
		log.Printf("[thumbnails] tone-map frame failed key=%s time=%.1f path=%q err=%v output=%s; retrying without tone map", key, job.TimeSec, cleanPath, err, strings.TrimSpace(string(output)))
		output, err = m.runFrameCommand(job, sourceURL, thumbnailFilter(false))
	}
	if err != nil {
		if thumbnailInputUnavailable(output) {
			log.Printf("[thumbnails] source unavailable key=%s time=%.1f path=%q err=%v output=%s", key, job.TimeSec, cleanPath, err, strings.TrimSpace(string(output)))
		} else {
			log.Printf("[thumbnails] frame failed key=%s time=%.1f path=%q err=%v output=%s", key, job.TimeSec, cleanPath, err, strings.TrimSpace(string(output)))
		}
		return thumbnailResult{}
	}
	return thumbnailResult{Details: details, OK: true}
}

func (m *ThumbnailManager) runFrameCommand(job thumbnailJob, sourceURL, filter string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), thumbnailFrameTimeout)
	defer cancel()
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-ss", fmt.Sprintf("%.2f", job.TimeSec),
		"-i", sourceURL,
		"-frames:v", "1",
		"-vf", filter,
		"-q:v", "5",
		job.OutputPath,
	}
	cmd := exec.CommandContext(ctx, m.ffmpegPath, args...)
	return cmd.CombinedOutput()
}

func (m *ThumbnailManager) isInflight(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.inFlight[key]
	return ok
}

func (h *VideoHandler) StartThumbnails(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		h.writeCommonHeaders(w)
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.WriteHeader(http.StatusOK)
		return
	}
	h.writeCommonHeaders(w)

	if h.thumbnailManager == nil {
		http.Error(w, "thumbnail generation unavailable", http.StatusServiceUnavailable)
		return
	}
	cleanPath := cleanVideoPathParam(r.URL.Query().Get("path"))
	if cleanPath == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}
	durationSec, _ := strconv.ParseFloat(strings.TrimSpace(r.URL.Query().Get("duration")), 64)
	intervalSec, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("interval")))
	dvProfile := parseThumbnailDVProfile(r)

	sourceURL := h.buildLocalVideoStreamURL(cleanPath)
	if sourceURL != "" {
		log.Printf("[thumbnails] using local stream proxy key=%s path=%q", thumbnailKey(cleanPath), cleanPath)
	} else {
		var err error
		sourceURL, err = h.resolveSeekableURL(r.Context(), cleanPath)
		if err != nil || sourceURL == "" {
			log.Printf("[thumbnails] no seekable URL for %q: %v", cleanPath, err)
			http.Error(w, "no seekable URL available", http.StatusNotImplemented)
			return
		}
	}

	toneMap := parseThumbnailToneMapHint(r, dvProfile) || (!isDolbyVisionProfile5(dvProfile) && thumbnailNeedsToneMap(h.getCachedMetadata(cleanPath)))
	key, started, err := h.thumbnailManager.start(cleanPath, sourceURL, durationSec, intervalSec, toneMap, dvProfile)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"key":     key,
		"status":  h.thumbnailStatusForKey(key),
		"started": started,
	})
}

func (h *VideoHandler) GetThumbnailsStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		h.writeCommonHeaders(w)
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.WriteHeader(http.StatusOK)
		return
	}
	h.writeCommonHeaders(w)

	if h.thumbnailManager == nil {
		http.Error(w, "thumbnail generation unavailable", http.StatusServiceUnavailable)
		return
	}
	cleanPath := cleanVideoPathParam(r.URL.Query().Get("path"))
	if cleanPath == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}
	key := thumbnailKey(cleanPath)
	resp, err := h.thumbnailStatusResponse(r, key)
	if err != nil {
		resp = &thumbnailStatusResponse{
			Key:    key,
			Status: h.thumbnailStatusForKey(key),
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *VideoHandler) ServeThumbnailImage(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		h.writeCommonHeaders(w)
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.WriteHeader(http.StatusOK)
		return
	}
	h.writeCommonHeaders(w)
	if h.thumbnailManager == nil {
		http.NotFound(w, r)
		return
	}
	vars := mux.Vars(r)
	key := vars["key"]
	file := filepath.Base(vars["file"])
	if !validThumbnailKey(key) || file == "." || !strings.HasSuffix(strings.ToLower(file), ".jpg") {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(h.thumbnailManager.baseDir, key, file)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, path)
}

func (h *VideoHandler) thumbnailStatusForKey(key string) string {
	if h.thumbnailManager == nil {
		return "disabled"
	}
	if manifest, err := h.thumbnailManager.readManifest(key); err == nil && manifest.Status != "" {
		if h.thumbnailManager.isInflight(key) && manifest.Status != "ready" {
			return "generating"
		}
		return manifest.Status
	}
	if h.thumbnailManager.isInflight(key) {
		return "generating"
	}
	return "pending"
}

func (h *VideoHandler) thumbnailStatusResponse(r *http.Request, key string) (*thumbnailStatusResponse, error) {
	manifest, err := h.thumbnailManager.readManifest(key)
	if err != nil {
		return nil, err
	}
	status := manifest.Status
	if h.thumbnailManager.isInflight(key) && status != "ready" {
		status = "generating"
	}
	resp := &thumbnailStatusResponse{
		Key:         key,
		Status:      status,
		DurationSec: manifest.DurationSec,
		IntervalSec: manifest.IntervalSec,
		Generated:   manifest.Generated,
		Total:       manifest.Total,
		UpdatedAt:   manifest.UpdatedAt,
		Error:       manifest.Error,
		ToneMapped:  manifest.ToneMapped,
		DVProfile:   manifest.DVProfile,
		FilterVer:   manifest.FilterVer,
		Thumbnails:  make([]thumbnailURLItem, 0, len(manifest.Thumbnails)),
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	for _, thumb := range manifest.Thumbnails {
		u := fmt.Sprintf("/api/video/thumbnails/image/%s/%s", key, thumb.File)
		if token != "" {
			u += "?token=" + url.QueryEscape(token)
		}
		resp.Thumbnails = append(resp.Thumbnails, thumbnailURLItem{TimeSec: thumb.TimeSec, URL: u})
	}
	sort.Slice(resp.Thumbnails, func(i, j int) bool {
		return resp.Thumbnails[i].TimeSec < resp.Thumbnails[j].TimeSec
	})
	return resp, nil
}
