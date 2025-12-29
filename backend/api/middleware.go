package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	"novastream/internal/auth"
	"novastream/services/sessions"
	"novastream/services/users"
)

// Re-export from auth package for backward compatibility
var (
	GetAccountID = auth.GetAccountID
	IsMaster     = auth.IsMaster
)

// AccountAuthMiddleware creates middleware that validates session tokens.
// If no session service is provided, it falls back to legacy PIN auth.
func AccountAuthMiddleware(sessionsSvc *sessions.Service, getPIN func() string) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Always allow OPTIONS for CORS
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			// Try bearer token auth first
			token := extractBearerToken(r)
			if token != "" && sessionsSvc != nil {
				session, err := sessionsSvc.Validate(token)
				if err == nil {
					// Valid session - inject account context
					ctx := context.WithValue(r.Context(), auth.ContextKeyAccountID, session.AccountID)
					ctx = context.WithValue(ctx, auth.ContextKeyIsMaster, session.IsMaster)
					ctx = context.WithValue(ctx, auth.ContextKeySession, session)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				// Invalid token - don't fall back to PIN, return unauthorized
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid or expired session"})
				return
			}

			// Fall back to legacy PIN auth for backward compatibility
			expectedPIN := strings.TrimSpace(getPIN())
			if expectedPIN == "" {
				// No PIN configured and no session - allow with master access for migration
				ctx := context.WithValue(r.Context(), auth.ContextKeyAccountID, "master")
				ctx = context.WithValue(ctx, auth.ContextKeyIsMaster, true)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Check for PIN in various locations
			receivedPIN := extractPIN(r)
			if receivedPIN == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "authentication required"})
				return
			}

			if !secureCompare(receivedPIN, expectedPIN) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid PIN"})
				return
			}

			// Valid PIN - grant master access for legacy compatibility
			ctx := context.WithValue(r.Context(), auth.ContextKeyAccountID, "master")
			ctx = context.WithValue(ctx, auth.ContextKeyIsMaster, true)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// MasterOnlyMiddleware creates middleware that only allows master accounts.
func MasterOnlyMiddleware() mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			if !IsMaster(r) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]string{"error": "master account required"})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// ProfileOwnershipMiddleware creates middleware that verifies profile ownership.
// Master accounts can access any profile; regular accounts can only access their own.
func ProfileOwnershipMiddleware(usersSvc *users.Service) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			// Master accounts bypass ownership check
			if IsMaster(r) {
				next.ServeHTTP(w, r)
				return
			}

			// Get profile ID from URL
			vars := mux.Vars(r)
			profileID := vars["userID"]
			if profileID == "" {
				next.ServeHTTP(w, r)
				return
			}

			// Check ownership
			accountID := GetAccountID(r)
			if !usersSvc.BelongsToAccount(profileID, accountID) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]string{"error": "profile not found"})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// extractBearerToken extracts the bearer token from the Authorization header.
func extractBearerToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return ""
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}

	return strings.TrimSpace(parts[1])
}

// extractPIN extracts the PIN from various request locations.
func extractPIN(r *http.Request) string {
	// Check X-PIN header
	if pin := strings.TrimSpace(r.Header.Get("X-PIN")); pin != "" {
		return pin
	}

	// Check Authorization header for PIN prefix
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if authHeader != "" {
		lower := strings.ToLower(authHeader)
		if strings.HasPrefix(lower, "pin ") {
			return strings.TrimSpace(authHeader[4:])
		}
	}

	// Check query parameters
	query := r.URL.Query()
	for _, param := range []string{"pin", "PIN", "apiKey", "apikey", "api_key", "key"} {
		if val := strings.TrimSpace(query.Get(param)); val != "" {
			return val
		}
	}

	// Check legacy X-API-Key header
	if key := strings.TrimSpace(r.Header.Get("X-API-Key")); key != "" {
		return key
	}

	return ""
}

// secureCompare performs a constant-time string comparison.
func secureCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var result byte
	for i := 0; i < len(a); i++ {
		result |= a[i] ^ b[i]
	}
	return result == 0
}
