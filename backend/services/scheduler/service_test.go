package scheduler

import (
	"strings"
	"testing"
	"time"

	"novastream/models"
	"novastream/services/history"
)

type fakeSchedulerUsersProvider struct {
	users map[string]models.User
}

func (f *fakeSchedulerUsersProvider) Exists(id string) bool {
	_, ok := f.users[id]
	return ok
}

func (f *fakeSchedulerUsersProvider) ListAll() []models.User {
	result := make([]models.User, 0, len(f.users))
	for _, user := range f.users {
		result = append(result, user)
	}
	return result
}

func TestResolveProfileID(t *testing.T) {
	t.Run("existing profile passes through", func(t *testing.T) {
		svc := &Service{
			usersService: &fakeSchedulerUsersProvider{
				users: map[string]models.User{
					"prof-1": {ID: "prof-1", Name: "Primary Profile"},
				},
			},
		}

		got, err := svc.resolveProfileID("prof-1")
		if err != nil {
			t.Fatalf("resolveProfileID() error = %v", err)
		}
		if got != "prof-1" {
			t.Fatalf("resolveProfileID() = %q, want %q", got, "prof-1")
		}
	})

	t.Run("legacy default resolves to sole profile", func(t *testing.T) {
		svc := &Service{
			usersService: &fakeSchedulerUsersProvider{
				users: map[string]models.User{
					"uuid-1": {ID: "uuid-1", Name: "Only Profile"},
				},
			},
		}

		got, err := svc.resolveProfileID(models.DefaultUserID)
		if err != nil {
			t.Fatalf("resolveProfileID() error = %v", err)
		}
		if got != "uuid-1" {
			t.Fatalf("resolveProfileID() = %q, want %q", got, "uuid-1")
		}
	})

	t.Run("legacy default resolves to primary profile by name", func(t *testing.T) {
		svc := &Service{
			usersService: &fakeSchedulerUsersProvider{
				users: map[string]models.User{
					"uuid-1": {ID: "uuid-1", Name: "Kids"},
					"uuid-2": {ID: "uuid-2", Name: models.DefaultUserName},
				},
			},
		}

		got, err := svc.resolveProfileID(models.DefaultUserID)
		if err != nil {
			t.Fatalf("resolveProfileID() error = %v", err)
		}
		if got != "uuid-2" {
			t.Fatalf("resolveProfileID() = %q, want %q", got, "uuid-2")
		}
	})

	t.Run("unknown non-legacy profile fails", func(t *testing.T) {
		svc := &Service{
			usersService: &fakeSchedulerUsersProvider{
				users: map[string]models.User{
					"uuid-1": {ID: "uuid-1", Name: models.DefaultUserName},
				},
			},
		}

		_, err := svc.resolveProfileID("missing")
		if err == nil {
			t.Fatal("resolveProfileID() error = nil, want error")
		}
		if !strings.Contains(err.Error(), `profile "missing" not found`) {
			t.Fatalf("resolveProfileID() error = %v, want missing profile error", err)
		}
	})
}

func TestIsNewerWatchState_PrefersExplicitUnwatchedOnTimestampTie(t *testing.T) {
	ts := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)
	watched := &models.WatchHistoryItem{
		ID:        "episode:show:s01e01",
		Watched:   true,
		WatchedAt: ts,
		UpdatedAt: ts,
	}
	unwatched := &models.WatchHistoryItem{
		ID:        "episode:show:s01e01:manual",
		Watched:   false,
		WatchedAt: ts,
		UpdatedAt: ts,
	}

	if !isNewerWatchState(unwatched, watched) {
		t.Fatal("expected explicit unwatched state to win on equal timestamps")
	}
	if isNewerWatchState(watched, unwatched) {
		t.Fatal("expected watched state to lose on equal timestamps against explicit unwatched state")
	}
}

func TestIsNewerWatchState_PrefersNewerTimestamp(t *testing.T) {
	older := &models.WatchHistoryItem{
		ID:        "episode:show:s01e01",
		Watched:   false,
		WatchedAt: time.Date(2026, 3, 24, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 24, 12, 0, 0, 0, time.UTC),
	}
	newer := &models.WatchHistoryItem{
		ID:        "episode:show:s01e01",
		Watched:   true,
		WatchedAt: time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC),
	}

	if !isNewerWatchState(newer, older) {
		t.Fatal("expected newer timestamp to win")
	}
	if isNewerWatchState(older, newer) {
		t.Fatal("expected older timestamp to lose")
	}
}

func TestLatestWatchStateForItem_PrefersNewestStateAcrossIDVariants(t *testing.T) {
	dir := t.TempDir()
	historySvc, err := history.NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	svc := &Service{historyService: historySvc}
	userID := "user-1"
	watched := true
	unwatched := false
	ts := time.Now().UTC().Add(-2 * time.Hour)

	if _, err := historySvc.UpdateWatchHistory(userID, models.WatchHistoryUpdate{
		MediaType:     "episode",
		ItemID:        "tmdb:tv:100:s03e11",
		Watched:       &watched,
		WatchedAt:     ts,
		SeriesID:      "tmdb:tv:100",
		SeriesName:    "Ragnarok",
		SeasonNumber:  3,
		EpisodeNumber: 11,
		ExternalIDs:   map[string]string{"tmdb": "100", "tvdb": "393810"},
	}); err != nil {
		t.Fatalf("UpdateWatchHistory() watched error = %v", err)
	}

	if _, err := historySvc.UpdateWatchHistory(userID, models.WatchHistoryUpdate{
		MediaType:     "episode",
		ItemID:        "tvdb:series:393810:s03e11",
		Watched:       &unwatched,
		SeriesID:      "tvdb:series:393810",
		SeriesName:    "Ragnarok",
		SeasonNumber:  3,
		EpisodeNumber: 11,
		ExternalIDs:   map[string]string{"tmdb": "100", "tvdb": "393810"},
	}); err != nil {
		t.Fatalf("UpdateWatchHistory() unwatched error = %v", err)
	}

	item, err := svc.latestWatchStateForItem(userID, "episode", []string{
		"tmdb:tv:100:s03e11",
		"tvdb:series:393810:s03e11",
	})
	if err != nil {
		t.Fatalf("latestWatchStateForItem() error = %v", err)
	}
	if item == nil {
		t.Fatal("expected a watch state")
	}
	if item.Watched {
		t.Fatal("expected newer explicit unwatched state to win across ID variants")
	}
}
