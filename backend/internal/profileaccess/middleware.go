// Package profileaccess enforces account ownership before profile-aware handlers run.
package profileaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	"novastream/internal/apierror"
	"novastream/internal/auth"
	"novastream/models"
)

const maxProfileBodyBytes = 1 << 20

type ownershipKey struct{}
type ownerCheck func(profileID, accountID string) bool

// Middleware checks profile selectors on both API and cookie-authenticated
// requests. It must run after authentication and independently of PIN policy.
func Middleware(owns func(profileID, accountID string) bool) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(context.WithValue(r.Context(), ownershipKey{}, ownerCheck(owns)))
			if r.Method == http.MethodOptions || auth.IsMaster(r) || auth.IsShareLinkRequest(r) {
				next.ServeHTTP(w, r)
				return
			}
			check := func(id string) bool {
				id = strings.TrimSpace(id)
				if id != "" && (owns == nil || !owns(id, auth.GetAccountID(r))) {
					apierror.Write(w, http.StatusNotFound, "profile_not_found", "profile not found")
					return false
				}
				return true
			}
			hasProfilePath := false
			for _, key := range []string{"userID", "profileID"} {
				id := mux.Vars(r)[key]
				hasProfilePath = hasProfilePath || strings.TrimSpace(id) != ""
				if !check(id) {
					return
				}
			}
			for _, key := range []string{"profileId", "userId"} {
				for _, id := range r.URL.Query()[key] {
					if !check(id) {
						return
					}
				}
			}
			// Profile-path handlers use that path as the destination. In particular,
			// large watch-history imports need not be buffered or size-limited here.
			// Diagnostic uploads can contain large log records, but never select a
			// profile as an authorization target. Query/path selectors still apply.
			diagnostics := strings.HasSuffix(r.URL.Path, "/api/debug/log") || strings.Contains(r.URL.Path, "/api/logs/")
			if !hasProfilePath && !diagnostics && !checkBody(w, r, check) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CheckResource checks the stored profile of an existing playback resource, so
// omitting or substituting a query selector cannot grant access to its session.
// Internal integrations without this entrypoint middleware retain their own
// authorization layer. Share sessions remain restricted to their bound source.
func CheckResource(w http.ResponseWriter, r *http.Request, profileID, resource string) bool {
	check, installed := r.Context().Value(ownershipKey{}).(ownerCheck)
	if !installed {
		return true
	}
	allowed := false
	if session, ok := auth.GetSession(r); ok && session.Scope == models.SessionScopeStream {
		normalize := func(value string) string {
			value = strings.TrimSpace(value)
			if strings.HasPrefix(value, "/webdav/") {
				return strings.TrimPrefix(value, "/webdav")
			}
			if strings.HasPrefix(value, "webdav/") {
				return "/" + strings.TrimPrefix(value, "webdav/")
			}
			return value
		}
		allowed = normalize(resource) != "" && normalize(resource) == normalize(session.ScopeResource)
	} else {
		allowed = auth.IsMaster(r) || (profileID != "" && check != nil && check(profileID, auth.GetAccountID(r)))
	}
	if !allowed {
		apierror.Write(w, http.StatusNotFound, "resource_not_found", "resource not found")
	}
	return allowed
}

func checkBody(w http.ResponseWriter, r *http.Request, check func(string) bool) bool {
	if r.Body == nil || r.Body == http.NoBody || (r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch && r.Method != http.MethodDelete) {
		return true
	}
	// Handlers decode JSON regardless of Content-Type. Inspect the bytes rather
	// than trusting a header that a caller can omit or change.
	original := r.Body
	peeked, err := io.ReadAll(io.LimitReader(original, maxProfileBodyBytes+1))
	r.Body = restoredBody{Reader: io.MultiReader(bytes.NewReader(peeked), original), Closer: original}
	if err != nil {
		apierror.Write(w, http.StatusBadRequest, "invalid_request", "could not read request body")
		return false
	}
	trimmed := bytes.TrimSpace(peeked)
	if len(peeked) > maxProfileBodyBytes && (len(trimmed) == 0 || trimmed[0] == '{') {
		apierror.Write(w, http.StatusRequestEntityTooLarge, "request_too_large", "profile-aware request body is too large")
		return false
	}
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return true // Binary uploads and top-level bulk arrays carry no profile selector.
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(peeked, &fields) != nil {
		apierror.Write(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
		return false
	}
	for key, raw := range fields {
		// encoding/json also accepts case-insensitive struct field names.
		if !strings.EqualFold(key, "profileId") && !strings.EqualFold(key, "userId") {
			continue
		}
		var id string
		if json.Unmarshal(raw, &id) != nil {
			apierror.Write(w, http.StatusBadRequest, "invalid_request", "profile selector must be a string")
			return false
		}
		if !check(id) {
			return false
		}
	}
	return true
}

type restoredBody struct {
	io.Reader
	io.Closer
}
