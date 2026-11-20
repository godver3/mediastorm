package jellyfinbridge

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
)

// trailerSource is the part of the metadata service that finds trailers. It
// is optional: without it items carry no RemoteTrailers.
type trailerSource interface {
	Trailers(ctx context.Context, req models.TrailerQuery) (*models.TrailerResponse, error)
}

// trailerTTL keeps a title's trailers this long; the lookup asks TMDB/TVDB
// every time, and a details page is opened again and again.
const trailerTTL = 12 * time.Hour

// maxRemoteTrailers caps the list: clients play the first one and list the
// rest, so a long tail of teasers and clips is noise.
const maxRemoteTrailers = 5

// maxTrailerEntries bounds the trailer cache.
const maxTrailerEntries = 2000

type trailerEntry struct {
	urls    []dto.MediaURL
	expires time.Time
}

type trailerCache struct {
	mu      sync.Mutex
	entries map[string]trailerEntry
}

func (c *trailerCache) get(key string, now time.Time) ([]dto.MediaURL, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || now.After(e.expires) {
		return nil, false
	}
	return e.urls, true
}

func (c *trailerCache) put(key string, urls []dto.MediaURL, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]trailerEntry)
	}
	// A full cache drops expired entries, and if that is not enough, any
	// others (map order), so it stays bounded and a put stays cheap.
	if len(c.entries) >= maxTrailerEntries {
		for k, e := range c.entries {
			if now.After(e.expires) {
				delete(c.entries, k)
			}
		}
		for k := range c.entries {
			if len(c.entries) < maxTrailerEntries*9/10 {
				break
			}
			delete(c.entries, k)
		}
	}
	c.entries[key] = trailerEntry{urls: urls, expires: now.Add(trailerTTL)}
}

// trailerLookupTimeout caps an uncached trailer lookup so a slow TMDB/TVDB
// answer doesn't hold up the detail page; a timeout isn't cached.
const trailerLookupTimeout = 3 * time.Second

// remoteTrailers returns a title's trailers as Jellyfin RemoteTrailers (the
// clients' trailer button plays them, YouTube links included).
func (s *Server) remoteTrailers(ctx context.Context, t titleInfo) []dto.MediaURL {
	src, ok := s.deps.Metadata.(trailerSource)
	if !ok {
		return nil
	}
	key := t.MediaType + "|" + t.ID
	for _, k := range []string{"tmdb", "tvdb", "imdb"} {
		if v := t.ExternalIDs[k]; v != "" {
			key += "|" + k + ":" + v
		}
	}
	now := time.Now()
	if urls, ok := s.trailers.get(key, now); ok {
		return urls
	}
	q := models.TrailerQuery{MediaType: t.MediaType, TitleID: t.ID, Name: t.Name, Year: t.Year, IMDBID: t.ExternalIDs["imdb"]}
	q.TMDBID, _ = strconv.ParseInt(t.ExternalIDs["tmdb"], 10, 64)
	q.TVDBID, _ = strconv.ParseInt(t.ExternalIDs["tvdb"], 10, 64)
	// Bounded: a cold lookup runs inline on the detail page's request.
	ctx, cancel := context.WithTimeout(ctx, trailerLookupTimeout)
	defer cancel()
	resp, err := src.Trailers(ctx, q)
	if err != nil {
		// Not cached: a failed lookup is retried on the next open.
		return nil
	}
	urls := trailerURLs(resp)
	s.trailers.put(key, urls, now)
	return urls
}

// trailerURLs orders the primary trailer first, drops duplicates and links
// without a URL, and caps the list.
func trailerURLs(resp *models.TrailerResponse) []dto.MediaURL {
	if resp == nil {
		return nil
	}
	list := resp.Trailers
	if resp.PrimaryTrailer != nil {
		list = append([]models.Trailer{*resp.PrimaryTrailer}, list...)
	}
	seen := make(map[string]bool, len(list))
	var out []dto.MediaURL
	for _, tr := range list {
		u := strings.TrimSpace(tr.URL)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		name := strings.TrimSpace(tr.Name)
		if name == "" {
			name = "Trailer"
		}
		out = append(out, dto.MediaURL{Name: name, URL: u})
		if len(out) == maxRemoteTrailers {
			break
		}
	}
	return out
}
