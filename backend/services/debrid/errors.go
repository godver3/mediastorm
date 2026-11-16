package debrid

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// ProviderError preserves structured provider API failures so callers can
// distinguish retryable stream errors from provider-blocked files.
type ProviderError struct {
	Provider   string
	Operation  string
	StatusCode int
	Code       int
	Message    string
	Body       string
}

// IsProviderAPIUnavailableError reports provider-wide API failures suitable
// for temporarily pausing new resolution attempts. Item-specific source/CDN
// failures are intentionally excluded.
func IsProviderAPIUnavailableError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return true
	}
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		return providerErr.StatusCode == http.StatusTooManyRequests || providerErr.StatusCode >= http.StatusInternalServerError
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"context deadline exceeded",
		"client.timeout exceeded",
		"connection refused",
		"connection reset",
		"network is unreachable",
		"no such host",
		"unexpected eof",
		"database_error",
		"temporarily unavailable",
		"try again later",
		"cloudflare",
		"status 429",
		"status 500",
		"status 502",
		"status 503",
		"status 504",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// SourceError reports a failure while opening or reading the provider's direct
// CDN/download URL. It means the selected release is not playable from this
// source right now, so stream migration can try another result.
type SourceError struct {
	Provider   string
	URL        string
	StatusCode int
	Status     string
	Body       string
	Err        error
}

func (e *SourceError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return fmt.Sprintf("%s source request failed: %v", e.Provider, e.Err)
	}
	if e.Status != "" {
		return fmt.Sprintf("%s source request failed: %s: %s", e.Provider, e.Status, e.Body)
	}
	if e.StatusCode != 0 {
		return fmt.Sprintf("%s source request failed with status %d: %s", e.Provider, e.StatusCode, e.Body)
	}
	return fmt.Sprintf("%s source request failed", e.Provider)
}

func (e *SourceError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *ProviderError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code != 0 || e.Message != "" {
		return fmt.Sprintf("%s %s failed with status %d: %s (code: %d)", e.Provider, e.Operation, e.StatusCode, e.Message, e.Code)
	}
	if e.Body != "" {
		return fmt.Sprintf("%s %s failed with status %d: %s", e.Provider, e.Operation, e.StatusCode, e.Body)
	}
	return fmt.Sprintf("%s %s failed with status %d", e.Provider, e.Operation, e.StatusCode)
}

// IsBlockedContentError reports provider-side refusals where trying another
// source is the correct recovery path.
func IsBlockedContentError(err error) bool {
	if err == nil {
		return false
	}
	var restrictedTermErr *RestrictedTermError
	if errors.As(err, &restrictedTermErr) {
		return true
	}
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		if providerErr.StatusCode == 451 || providerErr.Code == 35 {
			return true
		}
		msg := strings.ToLower(providerErr.Message + " " + providerErr.Body)
		return strings.Contains(msg, "infringing_file")
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "status 451") ||
		strings.Contains(msg, "infringing_file") ||
		strings.Contains(msg, "error_code\": 35")
}

// IsProviderUnavailableError reports transient provider-side failures where the
// selected item may be unusable right now, but another source can still work.
func IsProviderUnavailableError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var sourceErr *SourceError
	if errors.As(err, &sourceErr) {
		return true
	}
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		if providerErr.StatusCode == http.StatusTooManyRequests || providerErr.StatusCode >= http.StatusInternalServerError {
			return true
		}
		msg := strings.ToLower(providerErr.Message + " " + providerErr.Body)
		return strings.Contains(msg, "database_error") ||
			strings.Contains(msg, "try again later") ||
			strings.Contains(msg, "temporarily unavailable")
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database_error") ||
		strings.Contains(msg, "try again later") ||
		strings.Contains(msg, "temporarily unavailable") ||
		strings.Contains(msg, "no download url returned")
}

// providerAccountDeniedMarkers identify errors that say the provider account
// itself cannot be used (expired premium, bad or revoked key, ban). Retrying
// other torrents cannot succeed, so these open the resolution circuit for a
// long cooldown instead of hammering the provider with rejected requests.
var providerAccountDeniedMarkers = []string{
	"permission_denied",     // Real-Debrid: no premium / feature not allowed
	"bad_token",             // Real-Debrid: invalid token
	"account_locked",        // Real-Debrid
	"account_not_activated", // Real-Debrid
	"authentication failed", // provider clients' 401/403 handling
	"invalid api key",
	"auth_bad_apikey",  // AllDebrid
	"auth_user_banned", // AllDebrid
	"auth_blocked",     // AllDebrid
	"must_be_premium",  // AllDebrid
	"no_server",        // AllDebrid: server/VPN IP not allowed
}

// IsProviderAccountDeniedError reports whether err means the provider account
// is unusable (as opposed to one torrent failing or a transient outage).
func IsProviderAccountDeniedError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var providerErr *ProviderError
	if errors.As(err, &providerErr) && providerErr.StatusCode == http.StatusUnauthorized {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range providerAccountDeniedMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
