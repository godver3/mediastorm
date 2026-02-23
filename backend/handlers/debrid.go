package handlers

import (
	"context"
	"encoding/json"
	"io"
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
		Result models.NZBResult `json:"result"`
	}

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	res, err := h.healthService.CheckHealthQuick(r.Context(), request.Result)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}
