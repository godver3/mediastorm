// Package apierror writes the shared JSON error envelope used by the API:
//
//	{"error": "<human readable message>", "code": "<stable machine code>"}
//
// The message text is kept stable for older clients that match on it; new
// clients should branch on code instead.
package apierror

import (
	"encoding/json"
	"net/http"
)

// Stable machine-readable error codes. Never rename a published value.
const (
	// CodeUnauthorized: no credentials were supplied (401).
	CodeUnauthorized = "unauthorized"
	// CodeSessionExpired: the session token is unknown, revoked or expired (401).
	// Clients should refresh or sign in again.
	CodeSessionExpired = "session_expired"
	// CodeAccountExpired: the account itself has expired; all sessions were revoked (401).
	CodeAccountExpired = "account_expired"
	// CodeInvalidCredentials: wrong username/password at login (401).
	CodeInvalidCredentials = "invalid_credentials"
	// CodePasswordInvalid: the current account password supplied for a
	// re-authentication check was wrong (401). The session is still valid.
	CodePasswordInvalid = "password_invalid"
	// CodePinInvalid: wrong profile PIN (401). The session is still valid.
	CodePinInvalid = "pin_invalid"
	// CodePinRequired: PIN verify was called without a PIN for a PIN-protected
	// profile (401). The session is still valid.
	CodePinRequired = "pin_required"
	// CodeProfileLocked: the request targets a PIN-protected profile without a
	// valid profile unlock token (403). The session is still valid.
	CodeProfileLocked = "profile_locked"
	// CodeForbidden: the session may not use this endpoint (403).
	CodeForbidden = "forbidden"
	// CodeMasterRequired: the endpoint requires the master account (403).
	CodeMasterRequired = "master_required"
	// CodeRateLimited: too many requests; honour Retry-After (429).
	CodeRateLimited = "rate_limited"
	// CodeDefaultPassword: the master account still uses the default password
	// and must be changed via login with newPassword (428).
	CodeDefaultPassword = "default_password"
	// CodeServiceUnavailable: a dependency such as the session store could not
	// be reached; the request may be retried (503).
	CodeServiceUnavailable = "service_unavailable"
)

// Body is the JSON error envelope.
type Body struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// Write sends a JSON error with the given status, message and code. Extra
// string fields (for example profileId) may be merged into the body.
func Write(w http.ResponseWriter, status int, code, message string, extra ...map[string]string) {
	payload := map[string]string{"error": message}
	if code != "" {
		payload["code"] = code
	}
	for _, fields := range extra {
		for k, v := range fields {
			if k == "error" || k == "code" {
				continue
			}
			payload[k] = v
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
