package sourceavailability

import (
	"context"
	"errors"
	"fmt"
	"novastream/models"
	"testing"
	"time"
)

func titleView(t *testing.T, s *Service, client, id string, version int64, titles ...models.Title) TitleViewResponse {
	t.Helper()
	v, err := s.ReplaceTitleView("profile", client, id, version, titles)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func TestVisibleTitlesDeliveryAndCacheExpiryNeverScheduleSearches(t *testing.T) {
	s, search, _, _ := reportFixture(t)
	title := testTitle("tmdb:movie:1")
	s.AnnotateTitles("profile", "tv", []models.Title{title})
	s.checkOne(t.Context())
	if search.calls != 0 || len(s.pending) != 0 {
		t.Fatal("delivered invisible title scheduled work")
	}
	now := time.Now()
	s.now = func() time.Time { return now.Add(7 * time.Hour) }
	s.Store.now = s.now
	s.AnnotateTitles("profile", "tv", []models.Title{title})
	s.checkOne(t.Context())
	if search.calls != 0 {
		t.Fatal("expiry started an invisible check")
	}
}
func TestVisibleTitlesOnlyDemandedCoordinatesSearch(t *testing.T) {
	s, search, _, _ := reportFixture(t)
	titles := []models.Title{testTitle("a"), testTitle("b"), testTitle("c")}
	s.AnnotateTitles("profile", "tv", titles)
	titleView(t, s, "tv", "grid", 1, titles[1])
	s.checkOne(t.Context())
	if search.calls != 1 || search.options.TitleID != "b" {
		t.Fatal("wrong visible title searched")
	}
	v := titleView(t, s, "tv", "grid", 2, titles[1])
	if v.Items[0].Availability.State != "not_found" {
		t.Fatal("visible evidence missing")
	}
	s.checkOne(t.Context())
	if search.calls != 1 {
		t.Fatal("duplicate search")
	}
}
func TestVisibleTitlesRetainOtherConsumersAndCancelLast(t *testing.T) {
	s, _, _, _ := reportFixture(t)
	search := &blockedEpisodeSearch{started: make(chan struct{}), done: make(chan struct{})}
	s.search = search
	title := testTitle("a")
	titleView(t, s, "tv", "a", 1, title)
	titleView(t, s, "tv", "b", 1, title)
	done := make(chan struct{})
	go func() { s.checkOne(context.Background()); close(done) }()
	select {
	case <-search.started:
	case <-time.After(time.Second):
		t.Fatal("search did not start")
	}
	titleView(t, s, "tv", "a", 2)
	select {
	case <-search.done:
		t.Fatal("hidden duplicate canceled visible consumer")
	default:
	}
	titleView(t, s, "tv", "b", 2)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("hidden work was not canceled")
	}
	selection, _ := s.scope("profile", "tv")
	key, _ := selection(title.ID)
	if s.Store.Get("profile", title.ID, key.Key).CheckedAt != nil {
		t.Fatal("hidden search committed a negative")
	}
}
func TestVisibleTitlesLeaseExpiryAndLateVersionsCannotRestoreDemand(t *testing.T) {
	s, search, _, _ := reportFixture(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	title := testTitle("a")
	titleView(t, s, "tv", "grid", 3, title)
	titleView(t, s, "tv", "grid", 4)
	if _, err := s.ReplaceTitleView("profile", "tv", "grid", 3, []models.Title{title}); !errors.Is(err, ErrStaleEpisodeView) {
		t.Fatal("late update accepted")
	}
	s.checkOne(t.Context())
	if search.calls != 0 {
		t.Fatal("late request restored demand")
	}
	titleView(t, s, "tv", "grid", 5, title)
	now = now.Add(31 * time.Second)
	s.checkOne(t.Context())
	if search.calls != 0 {
		t.Fatal("expired lease searched")
	}
}
func TestVisibleTitlesPreferenceChangeWaitsForFreshVisibleDemand(t *testing.T) {
	s, search, _, profiles := reportFixture(t)
	title := testTitle("a")
	titleView(t, s, "tv", "grid", 1, title)
	profiles["profile"].Playback.PreferredAudioLanguage = "eng"
	s.checkOne(t.Context())
	if search.calls != 0 || len(s.pending) != 0 {
		t.Fatal("old preference silently requeued")
	}
	titleView(t, s, "tv", "grid", 2, title)
	s.checkOne(t.Context())
	if search.calls != 1 {
		t.Fatal("fresh demand did not search")
	}
}
func TestVisibleTitlesAndEpisodesShareViewCapacityWithoutNamespaceCollision(t *testing.T) {
	s, _, _, _ := reportFixture(t)
	titleView(t, s, "tv", "same", 1, testTitle("a"))
	view(t, s, "tv", "same", 1, episode(1))
	for i := 0; i < 14; i++ {
		titleView(t, s, "tv", fmt.Sprint(i), 1)
	}
	if _, err := s.ReplaceTitleView("profile", "tv", "overflow", 1, nil); !errors.Is(err, ErrEpisodeViewLimit) {
		t.Fatal("separate endpoint bypassed profile view capacity")
	}
}
func TestVisibleTitlesDeliveredProjectionIsBoundedAndContainsNoSharedPointers(t *testing.T) {
	s, _, _, _ := reportFixture(t)
	title := testTitle("a")
	title.Overview = "private unneeded text"
	title.Poster = &models.Image{URL: "https://synthetic.invalid/art"}
	s.AnnotateTitles("profile", "tv", []models.Title{title})
	coord := TitleCoordinate{ID: "a", MediaType: "movie"}
	items := s.ResolveDeliveredTitles("profile", "tv", []TitleCoordinate{coord})
	if len(items) != 1 || items[0].Poster != nil || items[0].Overview != "" || items[0].SourceAvailability != nil {
		t.Fatal("projection retained unnecessary data")
	}
	items[0].Name = "mutated"
	if s.ResolveDeliveredTitles("profile", "tv", []TitleCoordinate{coord})[0].Name != title.Name {
		t.Fatal("shared projection mutated")
	}
	for i := 0; i < maxDeliveredTitles+1; i++ {
		s.rememberDelivered("profile", "tv", testTitle(fmt.Sprint(i)))
	}
	if len(s.delivered) > maxDeliveredTitles {
		t.Fatal("unbounded catalogue projections")
	}
}
func TestVisibleTitlesDeliveredMetadataIsScopedAndExpires(t *testing.T) {
	s, _, _, _ := reportFixture(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	title := testTitle("a")
	coord := TitleCoordinate{ID: "a", MediaType: "movie"}
	s.AnnotateTitles("profile", "tv", []models.Title{title})
	for _, other := range [][2]string{{"another-profile", "tv"}, {"profile", "phone"}} {
		if len(s.ResolveDeliveredTitles(other[0], other[1], []TitleCoordinate{coord})) != 0 {
			t.Fatal("metadata scope leaked")
		}
	}
	if len(s.ResolveDeliveredTitles("profile", "tv", []TitleCoordinate{{ID: "a", MediaType: "series"}})) != 0 {
		t.Fatal("media type changed")
	}
	now = now.Add(deliveredTitleTTL + time.Second)
	if len(s.ResolveDeliveredTitles("profile", "tv", []TitleCoordinate{coord})) != 0 {
		t.Fatal("expired metadata accepted")
	}
}
