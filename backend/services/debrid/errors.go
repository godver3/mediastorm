package debrid

import (
	"errors"
	"fmt"
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
