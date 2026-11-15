// Package titleratings stores explicit profile preferences and a bounded,
// temporary projection of titles already delivered by the catalogue. Clients
// can submit identifiers and a preference, never their own metadata or URLs.
package titleratings

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/models"
)

type Repository interface {
	List(context.Context, string) ([]models.TitleRating, error)
	Put(context.Context, string, models.TitleRating) error
}

type projectionKey struct{ profile, client, kind, id string }
type projection struct {
	title           models.Title
	expires         time.Time
	postPlayExpires time.Time
	sequence        uint64
}
type Service struct {
	repo      Repository
	mu        sync.Mutex
	delivered map[projectionKey]projection
	sequence  uint64
	now       func() time.Time
}

func New(repo Repository) *Service {
	return &Service{repo: repo, delivered: map[projectionKey]projection{}, now: time.Now}
}

// Aliases are scoped to media kind; the same provider number may refer to both
// a film and a series. Only server-provided identities enter this set.
func Aliases(title models.Title) []string {
	title = normalizedTitle(title)
	if title.MediaType != "movie" && title.MediaType != "series" {
		return nil
	}
	prefix := title.MediaType + ":"
	keys := []string{}
	if title.TMDBID > 0 {
		keys = append(keys, prefix+"tmdb:"+strconv.FormatInt(title.TMDBID, 10))
	}
	if title.IMDBID != "" {
		keys = append(keys, prefix+"imdb:"+title.IMDBID)
	}
	if title.TVDBID > 0 {
		keys = append(keys, prefix+"tvdb:"+strconv.FormatInt(title.TVDBID, 10))
	}
	if title.ID != "" && len(title.ID) <= 128 {
		keys = append(keys, prefix+"id:"+title.ID)
	}
	return keys
}

// BareTitle excludes artwork, provider responses, source status and playback
// context from durable preferences and the short-lived catalogue projection.
func BareTitle(t models.Title) models.Title {
	t = normalizedTitle(t)
	return models.Title{ID: t.ID, Name: t.Name, Year: t.Year, MediaType: t.MediaType, TMDBID: t.TMDBID, IMDBID: t.IMDBID, TVDBID: t.TVDBID, Certification: t.Certification, Adult: t.Adult}
}

func (s *Service) Observe(profile, client string, titles []models.Title) {
	if profile == "" || client == "" || len(client) > 128 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for key, value := range s.delivered {
		if !now.Before(value.postPlayExpires) {
			delete(s.delivered, key)
		}
	}
	for _, title := range titles {
		title = normalizedTitle(title)
		if strings.TrimSpace(title.ID) == "" || len(title.ID) > 128 || len(Aliases(title)) == 0 {
			continue
		}
		key := projectionKey{profile, client, title.MediaType, title.ID}
		if _, exists := s.delivered[key]; !exists && len(s.delivered) >= 2000 {
			var oldest projectionKey
			var seq uint64 = ^uint64(0)
			for k, v := range s.delivered {
				if v.sequence < seq {
					oldest = k
					seq = v.sequence
				}
			}
			delete(s.delivered, oldest)
		}
		s.sequence++
		s.delivered[key] = projection{title: BareTitle(title), expires: now.Add(30 * time.Minute), postPlayExpires: now.Add(24 * time.Hour), sequence: s.sequence}
	}
}

func (s *Service) Resolve(profile, client, kind, id string) (models.Title, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := projectionKey{profile, client, kind, id}
	item, ok := s.delivered[key]
	if !ok || !s.now().Before(item.expires) {
		if ok && !s.now().Before(item.postPlayExpires) {
			delete(s.delivered, key)
		}
		return models.Title{}, false
	}
	return item.title, true
}

// ResolveCompleted retains only a bounded, non-durable catalogue identity for
// long playback. The post-play handler must independently verify owned played
// history and current profile policy before renewing the ordinary projection.
// This never grants a normal rating request a longer authorization lifetime.
func (s *Service) ResolveCompleted(profile, client, kind, id string) (models.Title, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := projectionKey{profile, client, kind, id}
	item, ok := s.delivered[key]
	if !ok || !s.now().Before(item.postPlayExpires) {
		delete(s.delivered, key)
		return models.Title{}, false
	}
	return item.title, true
}

func (s *Service) List(ctx context.Context, profile string) ([]models.TitleRating, error) {
	return s.repo.List(ctx, profile)
}
func (s *Service) Put(ctx context.Context, profile string, title models.Title, value string) error {
	aliases := Aliases(title)
	if !models.ValidTitleRating(value) || len(aliases) == 0 {
		return ErrInvalidRating
	}
	return s.repo.Put(ctx, profile, models.TitleRating{Key: aliases[0], Aliases: aliases, Title: BareTitle(title), Value: value, UpdatedAt: s.now().UTC()})
}

func Match(items []models.TitleRating, title models.Title) string {
	aliases := Aliases(title)
	for _, item := range items {
		for _, stored := range item.Aliases {
			for _, alias := range aliases {
				if alias == stored {
					return item.Value
				}
			}
		}
	}
	return "none"
}

func normalizedTitle(t models.Title) models.Title {
	if t.MediaType == "tv" {
		t.MediaType = "series"
	}
	if t.TMDBID == 0 {
		prefix := "tmdb:movie:"
		if t.MediaType == "series" {
			prefix = "tmdb:tv:"
		}
		raw := strings.TrimPrefix(t.ID, prefix)
		if raw == t.ID && t.MediaType == "series" {
			raw = strings.TrimPrefix(t.ID, "tmdb:series:")
		}
		if raw != t.ID {
			if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id > 0 {
				t.TMDBID = id
			}
		}
	}
	if t.TVDBID == 0 && strings.HasPrefix(t.ID, "tvdb:") {
		if id, err := strconv.ParseInt(strings.TrimPrefix(t.ID, "tvdb:"), 10, 64); err == nil && id > 0 {
			t.TVDBID = id
		}
	}
	if t.IMDBID == "" && strings.HasPrefix(t.ID, "tt") {
		if _, err := strconv.ParseUint(t.ID[2:], 10, 64); err == nil {
			t.IMDBID = t.ID
		}
	}
	return t
}
