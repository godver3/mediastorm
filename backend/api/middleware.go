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
// Tokens can be provided via Authorization header or ?token= query param.
func AccountAuthMiddleware(sessionsSvc *sessions.Service) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Always allow OPTIONS for CORS
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			// Extract token from header or query param
			token := extractToken(r)
			if token == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "authentication required"})
				return
			}

			if sessionsSvc == nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": "session service unavailable"})
				return
			}

			session, err := sessionsSvc.Validate(token)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid or expired session"})
				return
			}

			// Valid session - inject account context
			ctx := context.WithValue(r.Context(), auth.ContextKeyAccountID, session.AccountID)
			ctx = context.WithValue(ctx, auth.ContextKeyIsMaster, session.IsMaster)
			ctx = context.WithValue(ctx, auth.ContextKeySession, session)
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

// extractToken extracts the session token from headers or query param.
// Priority: Authorization header > X-PIN header > ?token= query param
func extractToken(r *http.Request) string {
	// First try Authorization header
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
			if token := strings.TrimSpace(parts[1]); token != "" {
				return token
			}
		}
	}

	// Try X-PIN header (for reverse proxy compatibility, e.g., Traefik)
	if token := strings.TrimSpace(r.Header.Get("X-PIN")); token != "" {
		return token
	}

	// Fall back to query parameter for streaming URLs (video players can't set headers)
	if token := strings.TrimSpace(r.URL.Query().Get("token")); token != "" {
		return token
	}

	return ""
}
