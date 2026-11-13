package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/internal/apierror"
	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/profileunlock"
)

// ProfileUnlockHeader and ProfileUnlockQueryParam carry profile unlock tokens;
// see profileunlock.Header and profileunlock.QueryParam.
const (
	ProfileUnlockHeader     = profileunlock.Header
	ProfileUnlockQueryParam = profileunlock.QueryParam
)

// profileIDQueryParams are the query parameters through which a profile id
// enters a request. "userId" is the legacy name for the same thing.
var profileIDQueryParams = []string{"profileId", "userId"}

// profileIDBodyFields are the top-level JSON body fields carrying a profile id.
var profileIDBodyFields = []string{"profileId", "userId"}

// profileIDPathVars are the mux route variables that name a profile.
var profileIDPathVars = []string{"userID", "profileID"}

// maxProfileBodyPeekBytes bounds how much of a JSON body the PIN middleware
// buffers to look for a profile id. Larger bodies (bulk imports) are passed
// through unchanged; their profile id is in the path.
const maxProfileBodyPeekBytes = 1 << 20

type profilePinUsers interface {
	BelongsToAccount(profileID, accountID string) bool
	HasPin(id string) bool
	PinFingerprint(id string) string
}

type profileUnlockValidator interface {
	Validate(unlockTokens []string, sessionToken, profileID, pinFingerprint string) bool
}

// SecuritySettingsFunc returns the current security policy.
type SecuritySettingsFunc func() config.SecuritySettings

// CachedSecuritySettings loads security settings from the config manager at
// most once per ttl so per-request policy checks do not re-read settings.json.
func CachedSecuritySettings(manager *config.Manager, ttl time.Duration) SecuritySettingsFunc {
	var (
		mu       sync.Mutex
		cached   config.SecuritySettings
		loadedAt time.Time
		warned   bool
	)
	return func() config.SecuritySettings {
		mu.Lock()
		defer mu.Unlock()
		if !loadedAt.IsZero() && time.Since(loadedAt) < ttl {
			return cached
		}
		settings, err := manager.Load()
		if err != nil {
			if !warned {
				log.Printf("[profile-pin] failed to load security settings (keeping previous policy): %v", err)
				warned = true
			}
			loadedAt = time.Now()
			return cached
		}
		warned = false
		cached = settings.Security
		loadedAt = time.Now()
		return cached
	}
}

// ProfilePinMiddleware enforces server-side profile PINs when
// security.enforceProfilePins is on. It must run after AccountAuthMiddleware.
//
// A request "targets" a profile when a profile id appears in the route path
// ({userID} / {profileID}), in the profileId/userId query parameters, or in
// the top-level profileId/userId fields of a JSON body. When any targeted
// profile has a PIN, the request must carry a valid unlock token for it
// (X-Profile-Unlock header or profileUnlock query parameter), otherwise it is
// rejected with 403 {"error": "profile is locked", "code": "profile_locked"}.
//
// Exempt: CORS preflight, PIN verification, device registration, diagnostics
// log uploads, stream-scoped share
// sessions (bound to one resource by an already-authorised creator), and all
// requests while enforcement is off.
func ProfilePinMiddleware(usersSvc profilePinUsers, unlocks profileUnlockValidator, policy SecuritySettingsFunc) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		if usersSvc == nil || unlocks == nil || policy == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions || isProfilePinExemptPath(r) || !policy().EnforceProfilePins {
				next.ServeHTTP(w, r)
				return
			}
			session, _ := r.Context().Value(auth.ContextKeySession).(models.Session)
			if session.Scope == models.SessionScopeStream {
				next.ServeHTTP(w, r)
				return
			}

			profileIDs := requestProfileIDs(r)
			var locked []string
			for _, id := range profileIDs {
				// Profiles of other accounts are left to the ownership checks
				// downstream (404) so lock state is never revealed across accounts.
				if !session.IsMaster && !usersSvc.BelongsToAccount(id, session.AccountID) {
					continue
				}
				if usersSvc.HasPin(id) {
					locked = append(locked, id)
				}
			}
			if len(locked) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			tokens := profileunlock.RequestTokens(r)
			for _, id := range locked {
				if !unlocks.Validate(tokens, session.Token, id, usersSvc.PinFingerprint(id)) {
					apierror.Write(w, http.StatusForbidden, apierror.CodeProfileLocked, "profile is locked",
						map[string]string{"profileId": id})
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isProfilePinExemptPath lists routes that carry a profile id but neither read
// nor change that profile's data, so they must work before a profile is
// unlocked.
func isProfilePinExemptPath(r *http.Request) bool {
	path := r.URL.Path
	switch {
	case strings.HasPrefix(path, "/api/users/") && strings.HasSuffix(path, "/pin/verify"):
		// PIN verification is how a profile gets unlocked.
		return true
	case path == "/api/clients/register":
		// Device registration records the last active profile at app start-up.
		return true
	case path == "/api/debug/log" || strings.HasPrefix(path, "/api/logs/"):
		// Diagnostics uploads must never be blocked.
		return true
	}
	return false
}

// requestProfileIDs returns the distinct profile ids a request refers to.
func requestProfileIDs(r *http.Request) []string {
	seen := make(map[string]struct{})
	var ids []string
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		ids = append(ids, value)
	}

	vars := mux.Vars(r)
	for _, key := range profileIDPathVars {
		add(vars[key])
	}
	query := r.URL.Query()
	for _, key := range profileIDQueryParams {
		for _, value := range query[key] {
			add(value)
		}
	}
	for _, value := range jsonBodyProfileIDs(r) {
		add(value)
	}
	return ids
}

// jsonBodyProfileIDs peeks at a JSON request body for top-level profile id
// fields and restores the body so the handler can read it unchanged.
func jsonBodyProfileIDs(r *http.Request) []string {
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return nil
	}
	if !strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "json") {
		return nil
	}

	peeked, err := io.ReadAll(io.LimitReader(r.Body, maxProfileBodyPeekBytes+1))
	original := r.Body
	if len(peeked) > maxProfileBodyPeekBytes {
		r.Body = readCloser{Reader: io.MultiReader(bytes.NewReader(peeked), original), Closer: original}
		return nil
	}
	r.Body = readCloser{Reader: bytes.NewReader(peeked), Closer: original}
	if err != nil || len(peeked) == 0 {
		return nil
	}

	var fields map[string]json.RawMessage
	if json.Unmarshal(peeked, &fields) != nil {
		return nil
	}
	var ids []string
	for _, key := range profileIDBodyFields {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) != "" {
			ids = append(ids, value)
		}
	}
	return ids
}

type readCloser struct {
	io.Reader
	io.Closer
}
