package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"novastream/internal/auth"
	"novastream/internal/livecapacity"
	"novastream/internal/netproxy"
	"novastream/internal/requestsecurity"

	"github.com/gorilla/mux"
)

const liveDirectTicketTTL = 30 * time.Minute

type liveDirectTarget struct {
	URL            string
	ProxyURL       string
	RequestHeaders map[string]string
	AccountID      string
	Provider       string
	BucketKey      string
	MaxStreams     int
	ExpiresAt      time.Time
	Active         int
}

func (h *VideoHandler) registerLiveDirectTarget(target liveDirectTarget) string {
	now := time.Now()
	target.ExpiresAt = now.Add(liveDirectTicketTTL)
	target.RequestHeaders = cloneStringMap(target.RequestHeaders)
	ticket := generateSessionID()

	h.liveDirectMu.Lock()
	for id, existing := range h.liveDirectTargets {
		if existing.Active == 0 && now.After(existing.ExpiresAt) {
			delete(h.liveDirectTargets, id)
		}
	}
	h.liveDirectTargets[ticket] = &target
	h.liveDirectMu.Unlock()
	return ticket
}

func cloneStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func (h *VideoHandler) acquireLiveDirectTarget(ticket, accountID string) (liveDirectTarget, bool) {
	now := time.Now()
	h.liveDirectMu.Lock()
	defer h.liveDirectMu.Unlock()
	target, ok := h.liveDirectTargets[ticket]
	if !ok || now.After(target.ExpiresAt) || (target.AccountID != "" && target.AccountID != accountID) {
		if ok && target.Active == 0 && now.After(target.ExpiresAt) {
			delete(h.liveDirectTargets, ticket)
		}
		return liveDirectTarget{}, false
	}
	target.Active++
	target.ExpiresAt = now.Add(liveDirectTicketTTL)
	return *target, true
}

func (h *VideoHandler) releaseLiveDirectTarget(ticket string) {
	h.liveDirectMu.Lock()
	defer h.liveDirectMu.Unlock()
	if target, ok := h.liveDirectTargets[ticket]; ok && target.Active > 0 {
		target.Active--
	}
}

func (h *VideoHandler) countActiveLiveDirectUsage(target liveStreamTarget) int {
	if h == nil {
		return 0
	}
	provider := normalizeLiveProvider(target.Provider)
	bucket := strings.TrimSpace(target.BucketKey)
	count := 0
	h.liveDirectMu.Lock()
	defer h.liveDirectMu.Unlock()
	for _, direct := range h.liveDirectTargets {
		if normalizeLiveProvider(direct.Provider) != provider {
			continue
		}
		if bucket != "" && strings.TrimSpace(direct.BucketKey) != bucket {
			continue
		}
		count += direct.Active
	}
	return count
}

// ServeLiveDirect relays the provider's original transport stream without
// transcoding or HLS segmentation. The opaque ticket keeps the upstream URL,
// portal token, MAC address, and provider headers out of the client response.
func (h *VideoHandler) ServeLiveDirect(w http.ResponseWriter, r *http.Request) {
	ticket := strings.TrimSpace(mux.Vars(r)["ticket"])
	if ticket == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodHead {
		if _, ok := h.peekLiveDirectTarget(ticket, auth.GetAccountID(r)); !ok {
			http.NotFound(w, r)
			return
		}
		setLiveDirectResponseHeaders(w.Header(), "video/mp2t")
		w.WriteHeader(http.StatusOK)
		return
	}

	target, ok := h.acquireLiveDirectTarget(ticket, auth.GetAccountID(r))
	if !ok {
		http.NotFound(w, r)
		return
	}
	defer h.releaseLiveDirectTarget(ticket)
	lease, err := livecapacity.Default.Acquire(target.BucketKey, "", target.MaxStreams)
	if err != nil {
		writeLiveCapacityError(w, liveStreamTarget{Provider: target.Provider, BucketKey: target.BucketKey, MaxStreams: target.MaxStreams})
		return
	}
	defer lease.Release()

	// No overall deadline: the request context ends the stream when the client goes.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	client, err := netproxy.NewHTTPClientWithOptions(netproxy.HTTPClientOptions{
		ResponseHeaderTimeout: defaultStreamOpenTimeout,
	}, target.ProxyURL)
	if err != nil {
		log.Printf("[video] invalid live direct proxy: %v", err)
		http.Error(w, "live stream unavailable", http.StatusBadGateway)
		return
	}
	client = secureLiveRedirects(client, h.configuredExternalHostPolicy())

	upstreamRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL, nil)
	if err != nil {
		http.Error(w, "live stream unavailable", http.StatusBadGateway)
		return
	}
	applyRequestHeaders(upstreamRequest.Header, target.RequestHeaders)
	if strings.TrimSpace(upstreamRequest.Header.Get("User-Agent")) == "" {
		upstreamRequest.Header.Set("User-Agent", liveStreamUserAgent)
	}

	response, err := client.Do(upstreamRequest)
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			log.Printf("[video] live direct upstream open failed for %s: %v", requestsecurity.URLForLog(target.URL), err)
		}
		http.Error(w, "live stream unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		log.Printf("[video] live direct upstream returned %d for %s", response.StatusCode, requestsecurity.URLForLog(target.URL))
		http.Error(w, fmt.Sprintf("live stream returned status %d", response.StatusCode), http.StatusBadGateway)
		return
	}

	// The status waits for the provider's first body bytes (see liveDirectFirstByteTimeout).
	buffer := make([]byte, 256*1024)
	firstN, firstErr := readFirstLiveChunk(response.Body, buffer, liveDirectFirstByteTimeout, func() { _ = response.Body.Close() })
	if firstN == 0 {
		status := http.StatusBadGateway
		if errors.Is(firstErr, errLiveFirstByteTimeout) {
			status = http.StatusGatewayTimeout
		}
		log.Printf("[video] live direct upstream sent no data for %s (status %d): %v", requestsecurity.URLForLog(target.URL), status, firstErr)
		http.Error(w, "live stream sent no data", status)
		return
	}

	contentType := strings.TrimSpace(response.Header.Get("Content-Type"))
	if contentType == "" || strings.EqualFold(contentType, "application/octet-stream") {
		contentType = "video/mp2t"
	}
	setLiveDirectResponseHeaders(w.Header(), contentType)
	w.WriteHeader(http.StatusOK)

	tracker := GetStreamTracker()
	streamID, bytesCounter, activityCounter := tracker.StartStreamWithAccount(r, "stalker-live.ts", 0, 0, 0, target.AccountID)
	tracker.SetStreamCancel(streamID, cancel)
	defer tracker.EndStream(streamID)

	flusher, _ := w.(http.Flusher)
	var total int64
	for {
		n, readErr := firstN, firstErr
		if firstN > 0 {
			firstN, firstErr = 0, nil
		} else {
			n, readErr = response.Body.Read(buffer)
		}
		if n > 0 {
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				if !errors.Is(writeErr, context.Canceled) && !errors.Is(writeErr, io.EOF) && !isConnectionError(writeErr) {
					log.Printf("[video] live direct client write failed: %v", writeErr)
				}
				return
			}
			total += int64(n)
			atomic.StoreInt64(bytesCounter, total)
			atomic.StoreInt64(activityCounter, time.Now().UnixNano())
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, context.Canceled) && !errors.Is(readErr, context.DeadlineExceeded) {
				log.Printf("[video] live direct upstream read failed for %s: %v", requestsecurity.URLForLog(target.URL), readErr)
			}
			return
		}
	}
}

func (h *VideoHandler) peekLiveDirectTarget(ticket, accountID string) (liveDirectTarget, bool) {
	h.liveDirectMu.Lock()
	defer h.liveDirectMu.Unlock()
	target, ok := h.liveDirectTargets[ticket]
	if !ok || time.Now().After(target.ExpiresAt) || (target.AccountID != "" && target.AccountID != accountID) {
		return liveDirectTarget{}, false
	}
	return *target, true
}

func setLiveDirectResponseHeaders(header http.Header, contentType string) {
	header.Set("Content-Type", contentType)
	header.Set("Cache-Control", "no-store, no-cache, must-revalidate, proxy-revalidate")
	header.Set("Pragma", "no-cache")
	header.Set("Expires", "0")
	header.Set("Accept-Ranges", "none")
}
