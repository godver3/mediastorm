package auth

import "net/http"

// ContextKey is the type used for context keys
type ContextKey string

const (
	// ContextKeyAccountID is the key for the account ID in the context
	ContextKeyAccountID ContextKey = "accountID"
	// ContextKeyIsMaster is the key for the master flag in the context
	ContextKeyIsMaster ContextKey = "isMaster"
	// ContextKeySession is the key for the session in the context
	ContextKeySession ContextKey = "session"
)

// GetAccountID retrieves the authenticated account ID from the request context.
func GetAccountID(r *http.Request) string {
	if id, ok := r.Context().Value(ContextKeyAccountID).(string); ok {
		return id
	}
	return ""
}

// IsMaster checks if the authenticated account is a master account.
func IsMaster(r *http.Request) bool {
	if isMaster, ok := r.Context().Value(ContextKeyIsMaster).(bool); ok {
		return isMaster
	}
	return false
}
