// Package parentauth requires fresh account credentials for parental controls.
package parentauth

import (
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"novastream/internal/apierror"
	"novastream/internal/auth"
	"novastream/models"
)

const PasswordHeader = "X-Account-Password"
const RequiredCode = "parent_authorization_required"

type Accounts interface {
	Get(id string) (models.Account, bool)
	Authenticate(username, password string) (models.Account, error)
}

type Guard struct {
	accounts Accounts
	mu       sync.Mutex
	attempts map[string]*rate.Limiter
}

func New(accounts Accounts) *Guard {
	return &Guard{accounts: accounts, attempts: make(map[string]*rate.Limiter)}
}

// Require accepts an administrator or the current account's password for this
// mutation. Neither an account session nor a child's own PIN is parental proof.
// No password or successful authorization is retained after the request.
func (g *Guard) Require(w http.ResponseWriter, r *http.Request) bool {
	if auth.GetSessionScope(r) != "" {
		apierror.Write(w, http.StatusForbidden, apierror.CodeForbidden, "session not permitted for this endpoint")
		return false
	}
	if auth.IsMaster(r) {
		return true
	}
	accountID := auth.GetAccountID(r)
	password := r.Header.Get(PasswordHeader)
	deny := func() bool {
		apierror.Write(w, http.StatusForbidden, RequiredCode, "confirm the account password to manage parental controls")
		return false
	}
	if g == nil || g.accounts == nil || accountID == "" || password == "" {
		return deny()
	}
	// Browser headers cannot carry arbitrary UTF-8 or surrounding whitespace.
	if strings.HasPrefix(password, "base64:") {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(password, "base64:"))
		if err != nil {
			return deny()
		}
		password = string(decoded)
	}
	g.mu.Lock()
	limiter := g.attempts[accountID]
	if limiter == nil {
		limiter = rate.NewLimiter(rate.Every(12*time.Second), 10)
		g.attempts[accountID] = limiter
	}
	g.mu.Unlock()
	if !limiter.Allow() {
		w.Header().Set("Retry-After", "12")
		apierror.Write(w, http.StatusTooManyRequests, apierror.CodeRateLimited, "too many password attempts")
		return false
	}
	account, ok := g.accounts.Get(accountID)
	if !ok {
		return deny()
	}
	verified, err := g.accounts.Authenticate(account.Username, password)
	if err != nil || verified.ID != accountID {
		return deny()
	}
	return true
}
