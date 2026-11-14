package handlers

import (
	"strings"
	"sync"
	"time"
)

// catchupGrantTTL bounds how long an issued catch-up URL stays playable. A
// client asks for a new URL when it seeks, so this only has to outlast one
// programme being watched.
const catchupGrantTTL = 12 * time.Hour

// catchupGrants remembers the catch-up URLs GetCatchup issued per profile, so
// that in account-managed Live TV playback can accept exactly those archive
// URLs besides the visible channels' own stream URLs.
type catchupGrants struct {
	mu     sync.Mutex
	grants map[string]time.Time // profile + "\x00" + URL -> expiry
}

func catchupGrantKey(profileID, raw string) string {
	return strings.TrimSpace(profileID) + "\x00" + strings.TrimSpace(raw)
}

func (g *catchupGrants) issue(profileID, raw string, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.grants == nil {
		g.grants = make(map[string]time.Time)
	}
	for key, expiry := range g.grants {
		if !now.Before(expiry) {
			delete(g.grants, key)
		}
	}
	g.grants[catchupGrantKey(profileID, raw)] = now.Add(catchupGrantTTL)
}

func (g *catchupGrants) allowed(profileID, raw string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	expiry, ok := g.grants[catchupGrantKey(profileID, raw)]
	return ok && now.Before(expiry)
}

func (h *LiveHandler) catchupURLIssued(profileID, raw string) bool {
	return h.catchupGrants.allowed(profileID, raw, time.Now())
}
