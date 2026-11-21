package handlers

import (
	"hash/maphash"
	"sync"
)

// parsedPlaylistCacheEntries bounds how many parsed playlists are kept. One entry per
// configured M3U source is enough; an edited playlist replaces its entry by content.
const parsedPlaylistCacheEntries = 8

var parsedPlaylistSeed = maphash.MakeSeed()

type parsedPlaylistKey struct {
	hash   uint64
	length int
}

// parsedPlaylistCache memoizes parseM3UPlaylist by the playlist's contents. The raw
// playlist is already cached, but GET/POST /api/live/channels re-parsed it on every page:
// for a 43k-channel lineup that was ~95% of a warm page's time. Keyed by content, so a
// refreshed playlist is parsed again and nothing needs invalidating. Callers must treat
// the returned slice as read-only (the channel pipeline copies before changing channels).
type parsedPlaylistCache struct {
	mu      sync.Mutex
	entries map[parsedPlaylistKey][]LiveChannel
	order   []parsedPlaylistKey
}

func (c *parsedPlaylistCache) parse(contents string) []LiveChannel {
	key := parsedPlaylistKey{hash: maphash.String(parsedPlaylistSeed, contents), length: len(contents)}
	c.mu.Lock()
	if channels, ok := c.entries[key]; ok {
		c.mu.Unlock()
		return channels
	}
	c.mu.Unlock()

	channels := parseM3UPlaylist(contents)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[parsedPlaylistKey][]LiveChannel)
	}
	if _, ok := c.entries[key]; !ok {
		if len(c.order) >= parsedPlaylistCacheEntries {
			delete(c.entries, c.order[0])
			c.order = c.order[1:]
		}
		c.order = append(c.order, key)
	}
	c.entries[key] = channels
	return channels
}
