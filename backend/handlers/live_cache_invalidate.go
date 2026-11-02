package handlers

import "time"

// invalidateCatalogCaches marks every in-memory channel catalogue stale so the
// next channel request refetches from the providers: Xtream and Stremio
// catalogues, Stalker portal catalogues and the shared HTTP discovery cache.
//
// Stale entries are kept where they double as failure fallbacks (Xtream and
// Stalker channels, discovery validators for conditional GETs), and provider
// failure cooldowns are left in place so a forced refresh cannot hammer a
// provider that is already rate-limiting us. Returns the number of entries
// invalidated.
func (h *LiveHandler) invalidateCatalogCaches() int {
	now := time.Now()
	invalidated := 0

	h.xtreamMu.Lock()
	for key, entry := range h.xtreamCache {
		if entry.expiresAt.After(now) {
			// Non-zero, so a failed refetch can still serve these channels.
			entry.expiresAt = now
			h.xtreamCache[key] = entry
			invalidated++
		}
	}
	h.xtreamMu.Unlock()

	h.stremioMu.Lock()
	invalidated += len(h.stremioCache)
	clear(h.stremioCache)
	h.stremioMu.Unlock()

	stalkerSessionStore.Lock()
	sessions := make([]*stalkerPortalSession, 0, len(stalkerSessionStore.sessions))
	for _, session := range stalkerSessionStore.sessions {
		sessions = append(sessions, session)
	}
	stalkerSessionStore.Unlock()
	for _, session := range sessions {
		session.mu.Lock()
		if session.cacheExpiry.After(now) || len(session.categoryPages) > 0 {
			invalidated++
		}
		session.cacheExpiry = time.Time{}
		clear(session.categoryPages)
		session.mu.Unlock()
	}

	if d := h.discovery; d != nil {
		d.mu.Lock()
		for _, entry := range d.entries {
			if entry.expires.After(now) {
				entry.expires = now
				invalidated++
			}
		}
		d.mu.Unlock()
	}

	return invalidated
}
