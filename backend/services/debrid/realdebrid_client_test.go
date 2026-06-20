package debrid

import (
	"testing"
	"time"
)

func TestRealDebridRateLimiterBackoffAndDecay(t *testing.T) {
	limiter := &realDebridRateLimiter{}
	now := time.Unix(1000, 0)

	if got := limiter.delayUntil(now); got != 0 {
		t.Fatalf("initial delay = %v, want 0", got)
	}

	first := limiter.recordRateLimit(now, 0)
	if first != realDebridRateLimitBaseDelay {
		t.Fatalf("first rate-limit delay = %v, want %v", first, realDebridRateLimitBaseDelay)
	}
	if got := limiter.delayUntil(now.Add(500 * time.Millisecond)); got != 1500*time.Millisecond {
		t.Fatalf("delay after first rate limit = %v, want 1.5s", got)
	}

	second := limiter.recordRateLimit(now.Add(time.Second), 0)
	if second != 2*realDebridRateLimitBaseDelay {
		t.Fatalf("second rate-limit delay = %v, want %v", second, 2*realDebridRateLimitBaseDelay)
	}

	limiter.recordSuccess(now.Add(2 * time.Second))
	if limiter.delay != 2*realDebridRateLimitBaseDelay {
		t.Fatalf("in-flight success changed active cooldown to %v, want %v", limiter.delay, 2*realDebridRateLimitBaseDelay)
	}

	limiter.recordSuccess(now.Add(10 * time.Second))
	if limiter.delay != realDebridRateLimitBaseDelay {
		t.Fatalf("delay after one success = %v, want %v", limiter.delay, realDebridRateLimitBaseDelay)
	}

	limiter.recordSuccess(now.Add(11 * time.Second))
	if limiter.delay != 0 {
		t.Fatalf("delay after second success = %v, want 0", limiter.delay)
	}
	if got := limiter.delayUntil(now.Add(11 * time.Second)); got != 0 {
		t.Fatalf("delay after reset = %v, want 0", got)
	}
}

func TestRealDebridRateLimiterHonorsRetryAfterWithCap(t *testing.T) {
	limiter := &realDebridRateLimiter{}
	now := time.Unix(1000, 0)

	delay := limiter.recordRateLimit(now, 45*time.Second)
	if delay != 45*time.Second {
		t.Fatalf("retry-after delay = %v, want 45s", delay)
	}

	delay = limiter.recordRateLimit(now.Add(time.Second), 5*time.Minute)
	if delay != realDebridRateLimitMaxDelay {
		t.Fatalf("capped retry-after delay = %v, want %v", delay, realDebridRateLimitMaxDelay)
	}
}

func TestRealDebridRateLimiterSharedByAPIKey(t *testing.T) {
	key := "shared-test-key"
	realDebridRateLimiters.Delete(key)

	first := NewRealDebridClient(key)
	second := NewRealDebridClient(" " + key + " ")
	if first.rateLimiter != second.rateLimiter {
		t.Fatal("expected Real-Debrid clients with the same API key to share a rate limiter")
	}

	now := time.Unix(1000, 0)
	first.rateLimiter.recordRateLimit(now, 0)
	if got := second.rateLimiter.delayUntil(now.Add(time.Second)); got != time.Second {
		t.Fatalf("shared limiter delay = %v, want 1s", got)
	}
}
