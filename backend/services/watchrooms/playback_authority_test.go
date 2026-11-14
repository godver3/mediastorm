package watchrooms

import (
	"context"
	"novastream/models"
	"testing"
	"time"
)

func TestExternalPlaybackAuthorityTracksRoomGuestAndSource(t *testing.T) {
	now := time.Now().UTC()
	repo := &fakeRoomRepo{room: &models.WatchRoom{ID: "room", Status: models.WatchRoomStatusPlaying, ExpiresAt: now.Add(time.Hour)}, guestMembers: map[string]models.WatchRoomMember{"guest": {Joined: true}}, externalSource: &models.WatchRoomExternalSource{RoomID: "room", Resource: "/movie.mkv"}}
	service := New(repo, nil, nil)
	service.now = func() time.Time { return now }
	if err := service.ValidateExternalPlayback(context.Background(), "room", "guest", "/movie.mkv"); err != nil {
		t.Fatal(err)
	}
	repo.room.Status = models.WatchRoomStatusPaused
	if err := service.ValidateExternalPlayback(context.Background(), "room", "guest", "/movie.mkv"); err != nil {
		t.Fatal("paused room rejected")
	}
	cases := []struct {
		name   string
		change func()
	}{
		{"ended", func() { repo.room.Status = models.WatchRoomStatusEnded }},
		{"expired", func() { repo.room.ExpiresAt = now.Add(-time.Second) }},
		{"guest left", func() { delete(repo.guestMembers, "guest") }},
		{"source replaced", func() { repo.externalSource.Resource = "/other.mkv" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo.room.Status = models.WatchRoomStatusPlaying
			repo.room.ExpiresAt = now.Add(time.Hour)
			repo.guestMembers["guest"] = models.WatchRoomMember{Joined: true}
			repo.externalSource.Resource = "/movie.mkv"
			tc.change()
			if err := service.ValidateExternalPlayback(context.Background(), "room", "guest", "/movie.mkv"); err == nil {
				t.Fatal("stale playback authority accepted")
			}
		})
	}
}
