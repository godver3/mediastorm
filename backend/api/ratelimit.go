package api

import (
	"net/http"
	"sync"
	"time"

	"novastream/internal/apierror"
	"novastream/internal/requestsecurity"

	"golang.org/x/time/rate"
)

// ipLimiterEntry holds a rate limiter and last-seen timestamp for cleanup.
type ipLimiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// IPRateLimiter manages per-IP rate limiters.
type IPRateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*ipLimiterEntry
	rate     rate.Limit
	burst    int
}

// NewIPRateLimiter creates a rate limiter that allows r events per second with
// the given burst size. For "5 per minute" pass rate.Every(12*time.Second) with burst 5.
func NewIPRateLimiter(r rate.Limit, burst int) *IPRateLimiter {
	rl := &IPRateLimiter{
		limiters: make(map[string]*ipLimiterEntry),
		rate:     r,
		burst:    burst,
	}
	go rl.cleanup()
	return rl
}

// getLimiter returns the rate limiter for the given IP, creating one if needed.
func (rl *IPRateLimiter) getLimiter(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	entry, exists := rl.limiters[ip]
	if !exists {
		limiter := rate.NewLimiter(rl.rate, rl.burst)
		rl.limiters[ip] = &ipLimiterEntry{limiter: limiter, lastSeen: time.Now()}
		return limiter
	}
	entry.lastSeen = time.Now()
	return entry.limiter
}

// cleanup evicts entries not seen in the last 10 minutes.
func (rl *IPRateLimiter) cleanup() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		for ip, entry := range rl.limiters {
			if time.Since(entry.lastSeen) > 10*time.Minute {
				delete(rl.limiters, ip)
			}
		}
		rl.mu.Unlock()
	}
}

// getClientIP only honors forwarding headers from explicitly trusted proxies.
func getClientIP(r *http.Request) string {
	return requestsecurity.ClientIP(r)
}

// RateLimitHandler wraps an http.Handler with per-IP rate limiting.
// Returns 429 Too Many Requests when the limit is exceeded.
func RateLimitHandler(rl *IPRateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)
		limiter := rl.getLimiter(ip)
		if !limiter.Allow() {
			w.Header().Set("Retry-After", "60")
			apierror.Write(w, http.StatusTooManyRequests, apierror.CodeRateLimited, "too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RateLimitHandlerFunc wraps an http.HandlerFunc with per-IP rate limiting.
func RateLimitHandlerFunc(rl *IPRateLimiter, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)
		limiter := rl.getLimiter(ip)
		if !limiter.Allow() {
			w.Header().Set("Retry-After", "60")
			apierror.Write(w, http.StatusTooManyRequests, apierror.CodeRateLimited, "too many requests")
			return
		}
		next(w, r)
	}
}

// newLiveStartLimiter limits live tuning (/live/hls/start) per IP. Channel surfing starts a
// session per channel, so it allows a burst of 30 refilling one a second; the VOD HLS start
// limit (5 a minute) would refuse the sixth channel in a minute.
func newLiveStartLimiter() *IPRateLimiter {
	return NewIPRateLimiter(rate.Every(time.Second), 30)
}
