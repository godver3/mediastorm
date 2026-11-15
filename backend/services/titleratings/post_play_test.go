package titleratings

import (
	"fmt"
	"novastream/models"
	"testing"
	"time"
)

func TestCompletedCatalogueContextSurvivesLongPlaybackWithoutExtendingNormalRatings(t *testing.T) {
	s := New(&memoryRepo{})
	now := time.Now()
	s.now = func() time.Time { return now }
	title := models.Title{ID: "tmdb:movie:7", MediaType: "movie", Name: "Synthetic long film", Certification: "PG", Poster: &models.Image{URL: "https://synthetic.invalid/image"}}
	s.Observe("profile", "client", []models.Title{title})
	now = now.Add(3 * time.Hour)
	if _, ok := s.Resolve("profile", "client", "movie", title.ID); ok {
		t.Fatal("ordinary rating projection was silently extended")
	}
	got, ok := s.ResolveCompleted("profile", "client", "movie", title.ID)
	if !ok || got.ID != title.ID || got.Poster != nil {
		t.Fatal("long playback context missing or retained presentation data")
	}
	// Resolving a completion context itself does not renew ordinary rating access.
	if _, ok := s.Resolve("profile", "client", "movie", title.ID); ok {
		t.Fatal("completion lookup renewed rating access")
	}
	s.Observe("profile", "client", []models.Title{got})
	if _, ok := s.Resolve("profile", "client", "movie", title.ID); !ok {
		t.Fatal("authorized catalogue redelivery did not renew the ordinary projection")
	}
}

func TestCompletedCatalogueContextKeepsProfileClientKindExpiryAndRestartBoundaries(t *testing.T) {
	s := New(&memoryRepo{})
	now := time.Now()
	s.now = func() time.Time { return now }
	title := models.Title{ID: "tmdb:movie:7", MediaType: "movie", Name: "Synthetic"}
	s.Observe("profile", "client", []models.Title{title})
	for _, key := range [][3]string{{"other", "client", "movie"}, {"profile", "other", "movie"}, {"profile", "client", "series"}} {
		if _, ok := s.ResolveCompleted(key[0], key[1], key[2], title.ID); ok {
			t.Fatal("completion context crossed a boundary")
		}
	}
	if _, ok := New(&memoryRepo{}).ResolveCompleted("profile", "client", "movie", title.ID); ok {
		t.Fatal("completion context persisted")
	}
	now = now.Add(24 * time.Hour)
	if _, ok := s.ResolveCompleted("profile", "client", "movie", title.ID); ok {
		t.Fatal("expired completion context resolved")
	}
	if len(s.delivered) != 0 {
		t.Fatal("expired context was retained after lookup")
	}
}

func TestLongPlaybackCatalogueContextsRemainBoundedAndExpireDuringNewDelivery(t *testing.T) {
	s := New(&memoryRepo{})
	now := time.Now()
	s.now = func() time.Time { return now }
	for n := 1; n <= 2001; n++ {
		s.Observe("profile", "client", []models.Title{{ID: fmt.Sprintf("tmdb:movie:%d", n), MediaType: "movie"}})
	}
	if len(s.delivered) != 2000 {
		t.Fatal("completion contexts exceeded the catalogue bound")
	}
	if _, ok := s.ResolveCompleted("profile", "client", "movie", "tmdb:movie:1"); ok {
		t.Fatal("oldest context was not evicted")
	}
	now = now.Add(24 * time.Hour)
	s.Observe("profile", "client", []models.Title{{ID: "tmdb:movie:3000", MediaType: "movie"}})
	if len(s.delivered) != 1 {
		t.Fatal("expired contexts survived a new catalogue delivery")
	}
}
