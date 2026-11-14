package titleratings

import (
	"context"
	"errors"
	"fmt"
	"novastream/models"
	"reflect"
	"testing"
	"time"
)

type memoryRepo struct {
	items map[string][]models.TitleRating
	err   error
}

func (r *memoryRepo) List(ctx context.Context, p string) ([]models.TitleRating, error) {
	return r.items[p], r.err
}
func (r *memoryRepo) Put(ctx context.Context, p string, item models.TitleRating) error {
	if r.err != nil {
		return r.err
	}
	next := []models.TitleRating{}
	for _, old := range r.items[p] {
		if Match([]models.TitleRating{old}, item.Title) == "none" {
			next = append(next, old)
		}
	}
	if item.Value != "none" {
		next = append(next, item)
	}
	r.items[p] = next
	return nil
}
func TestProjectionIsBoundedTemporaryAndBoundToProfileClientAndKind(t *testing.T) {
	s := New(&memoryRepo{})
	now := time.Now()
	s.now = func() time.Time { return now }
	title := models.Title{ID: "tmdb:movie:1", Name: "Synthetic", MediaType: "movie", Poster: &models.Image{URL: "https://synthetic.invalid/private"}, PersonalRating: "dislike", SourceAvailability: &models.SourceAvailability{State: "available"}}
	s.Observe("p", "c", []models.Title{title})
	actual, ok := s.Resolve("p", "c", "movie", title.ID)
	if !ok || actual.Poster != nil || actual.SourceAvailability != nil || actual.PersonalRating != "" || actual.TMDBID != 1 {
		t.Fatal("projection retained presentation data or lost identity")
	}
	for _, key := range [][3]string{{"other", "c", "movie"}, {"p", "other", "movie"}, {"p", "c", "series"}} {
		if _, ok := s.Resolve(key[0], key[1], key[2], title.ID); ok {
			t.Fatal("foreign coordinate resolved")
		}
	}
	now = now.Add(30 * time.Minute)
	if _, ok := s.Resolve("p", "c", "movie", title.ID); ok {
		t.Fatal("expired projection resolved")
	}
	for n := 0; n < 2001; n++ {
		title.ID = fmt.Sprintf("tmdb:movie:%d", n+2)
		s.Observe("p", "c", []models.Title{title})
	}
	if len(s.delivered) != 2000 {
		t.Fatal("projection is not bounded")
	}
	if _, ok := s.Resolve("p", "c", "movie", "tmdb:movie:2"); ok {
		t.Fatal("oldest title not evicted")
	}
}
func TestPreferenceSurvivesProjectionExpiryAndServiceRestart(t *testing.T) {
	repo := &memoryRepo{items: map[string][]models.TitleRating{}}
	s := New(repo)
	title := models.Title{ID: "tmdb:movie:1", MediaType: "movie", Name: "Synthetic"}
	for _, value := range []string{"like", "love", "dislike"} {
		if err := s.Put(context.Background(), "p", title, value); err != nil {
			t.Fatal(err)
		}
		restarted := New(repo)
		items, err := restarted.List(context.Background(), "p")
		if err != nil || Match(items, title) != value || len(items) != 1 {
			t.Fatal("preference was not durable")
		}
		if _, ok := restarted.Resolve("p", "c", "movie", title.ID); ok {
			t.Fatal("private projection persisted")
		}
		other, _ := restarted.List(context.Background(), "other")
		if Match(other, title) != "none" {
			t.Fatal("profile crossed")
		}
	}
	if err := s.Put(context.Background(), "p", title, "none"); err != nil {
		t.Fatal(err)
	}
	if len(repo.items["p"]) != 0 {
		t.Fatal("clearing a preference retained a vote")
	}
}
func TestTrustedProviderAliasesMatchWithoutCrossingMediaKinds(t *testing.T) {
	film := models.Title{ID: "tvdb:100", MediaType: "movie", TMDBID: 20, IMDBID: "tt100", TVDBID: 100}
	item := models.TitleRating{Aliases: Aliases(film), Value: "love"}
	for _, title := range []models.Title{{ID: "tmdb:movie:20", MediaType: "movie"}, {ID: "tt100", MediaType: "movie"}, {ID: "tvdb:100", MediaType: "movie"}} {
		if Match([]models.TitleRating{item}, title) != "love" {
			t.Fatal("verified alias did not match")
		}
	}
	film.MediaType = "series"
	if Match([]models.TitleRating{item}, film) != "none" {
		t.Fatal("movie preference crossed into series")
	}
}
func TestOnlyExplicitSupportedPreferencesCanPersist(t *testing.T) {
	repo := &memoryRepo{items: map[string][]models.TitleRating{}}
	s := New(repo)
	title := models.Title{ID: "tmdb:movie:1", MediaType: "movie"}
	for _, value := range []string{"", "five", "Like", "watched", "hidden"} {
		if !errors.Is(s.Put(context.Background(), "p", title, value), ErrInvalidRating) {
			t.Fatal("invalid preference accepted")
		}
	}
	title.MediaType = "person"
	if s.Put(context.Background(), "p", title, "love") == nil {
		t.Fatal("person accepted")
	}
	repo.err = errors.New("synthetic database outage")
	title.MediaType = "movie"
	if s.Put(context.Background(), "p", title, "love") == nil {
		t.Fatal("storage error swallowed")
	}
	if !reflect.DeepEqual(repo.items, map[string][]models.TitleRating{}) {
		t.Fatal("failed write modified preferences")
	}
}

func TestLegacyTVMetadataUsesSeriesCoordinates(t *testing.T) {
	s := New(&memoryRepo{})
	title := models.Title{ID: "tvdb:series:7", MediaType: "tv", TVDBID: 7}
	s.Observe("p", "c", []models.Title{title})
	actual, ok := s.Resolve("p", "c", "series", title.ID)
	if !ok || actual.MediaType != "series" {
		t.Fatal("legacy TV title cannot be rated as a series")
	}
}
