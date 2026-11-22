package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"novastream/models"
	"novastream/services/debrid"
	"novastream/services/streaming"
)

type debridProxyService interface {
	Proxy(ctx context.Context, req debrid.ProxyRequest) (*streaming.Response, error)
}

type debridHealthService interface {
	CheckHealthQuick(ctx context.Context, candidate models.NZBResult) (*debrid.DebridHealthCheck, error)
	CheckHealthFull(ctx context.Context, candidate models.NZBResult) (*debrid.DebridHealthCheck, error)
	CheckQuickCacheOnly(ctx context.Context, candidate models.NZBResult) (*debrid.DebridHealthCheck, error)
	CheckQuickCacheOnlyBulk(ctx context.Context, candidates []models.NZBResult) ([]*debrid.DebridHealthCheck, error)
}

// DebridHandler proxies content from configured debrid providers to the frontend.
type DebridHandler struct {
	service       debridProxyService
	healthService debridHealthService
}

func NewDebridHandler(service debridProxyService, healthService debridHealthService) *DebridHandler {
	return &DebridHandler{
		service:       service,
		healthService: healthService,
	}
}

func (h *DebridHandler) Proxy(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		http.Error(w, "debrid proxy unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	resourceURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if resourceURL == "" {
		http.Error(w, "missing url parameter", http.StatusBadRequest)
		return
	}

	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	req := debrid.ProxyRequest{
		Provider:    provider,
		ResourceURL: resourceURL,
		Method:      r.Method,
		RangeHeader: r.Header.Get("Range"),
	}

	resp, err := h.service.Proxy(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if resp == nil {
		http.Error(w, "empty response from debrid proxy", http.StatusBadGateway)
		return
	}
	defer resp.Close()

	for key, values := range resp.Headers {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)

	if r.Method == http.MethodHead {
		return
	}

	if resp.Body != nil {
		// Track this stream for admin monitoring
		tracker := GetStreamTracker()
		filename := filepath.Base(resourceURL)
		streamID, bytesCounter, actCounter := tracker.StartStream(r, "debrid:"+filename, resp.ContentLength, 0, 0)
		defer tracker.EndStream(streamID)

		// Use a tracking writer to count bytes and activity
		trackingWriter := &trackingWriter{ResponseWriter: w, counter: bytesCounter, activityCounter: actCounter}
		if _, err := io.Copy(trackingWriter, resp.Body); err != nil {
			// Best effort logging; cannot write error to client at this point.
		}
	}
}

// trackingWriter wraps http.ResponseWriter to count bytes written
type trackingWriter struct {
	http.ResponseWriter
	counter         *int64
	activityCounter *int64
}

// Unwrap lets http.ResponseController reach the underlying writer (Flush).
func (tw *trackingWriter) Unwrap() http.ResponseWriter { return tw.ResponseWriter }

func (tw *trackingWriter) Write(b []byte) (int, error) {
	n, err := tw.ResponseWriter.Write(b)
	if n > 0 {
		if tw.counter != nil {
			atomic.AddInt64(tw.counter, int64(n))
		}
		if tw.activityCounter != nil {
			atomic.StoreInt64(tw.activityCounter, time.Now().UnixNano())
		}
	}
	return n, err
}

func (h *DebridHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// CheckCached accepts a debrid result and returns cached availability information.
func (h *DebridHandler) CheckCached(w http.ResponseWriter, r *http.Request) {
	if h.healthService == nil {
		http.Error(w, "debrid health service unavailable", http.StatusServiceUnavailable)
		return
	}

	var request struct {
		Result         models.NZBResult `json:"result"`
		QuickOnly      bool             `json:"quickOnly"`
		VerifyUncached bool             `json:"verifyUncached"`
	}

	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var (
		res *debrid.DebridHealthCheck
		err error
	)
	if request.QuickOnly {
		res, err = h.healthService.CheckQuickCacheOnly(r.Context(), request.Result)
	} else if request.VerifyUncached {
		res, err = h.healthService.CheckHealthFull(r.Context(), request.Result)
	} else {
		res, err = h.healthService.CheckHealthQuick(r.Context(), request.Result)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

// CheckCachedBulk accepts debrid results and returns safe quick cache status for each item.
func (h *DebridHandler) CheckCachedBulk(w http.ResponseWriter, r *http.Request) {
	if h.healthService == nil {
		http.Error(w, "debrid health service unavailable", http.StatusServiceUnavailable)
		return
	}

	startedAt := time.Now()
	contentLength := r.ContentLength

	var request struct {
		Results []models.NZBResult `json:"results"`
	}

	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&request); err != nil {
		log.Printf("[DebridCachedBulk] decode failed contentLength=%d elapsed=%s err=%v", contentLength, time.Since(startedAt), err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	count := len(request.Results)
	log.Printf("[DebridCachedBulk] start count=%d contentLength=%d", count, contentLength)

	res, err := h.healthService.CheckQuickCacheOnlyBulk(r.Context(), request.Results)
	if err != nil {
		log.Printf("[DebridCachedBulk] failed count=%d elapsed=%s err=%v", count, time.Since(startedAt), err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	cached, uncached, skipped := 0, 0, 0
	for _, item := range res {
		if item == nil || item.Status == "skipped" {
			skipped++
		} else if item.Cached {
			cached++
		} else {
			uncached++
		}
	}
	log.Printf(
		"[DebridCachedBulk] success count=%d response=%d cached=%d uncached=%d skipped=%d elapsed=%s",
		count,
		len(res),
		cached,
		uncached,
		skipped,
		time.Since(startedAt),
	)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}
