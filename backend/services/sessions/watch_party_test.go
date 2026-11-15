package sessions

import (
	"context"
	"errors"
	"novastream/models"
	"strings"
	"testing"
	"time"
)

func TestWatchPartyMediaGrantRechecksRoomAfterRestart(t *testing.T) {
	dir := t.TempDir()
	service, err := NewService(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := service.CreateWatchPartyPlayback("family", "browser", "192.0.2.1", time.Hour, "room", "guest", "/source.mkv")
	if err != nil {
		t.Fatal(err)
	}
	service, err = NewService(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Validate(grant.Token); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal("grant accepted without its room validator")
	}
	active := true
	service.SetWatchPartyPlaybackValidator(func(ctx context.Context, room, guest, resource string) error {
		if room != "room" || guest != "guest" || resource != "/source.mkv" || !active {
			return errors.New("room ended")
		}
		return nil
	})
	session, err := service.Validate(grant.Token)
	if err != nil || session.Scope != models.SessionScopeStream || session.ScopeResource != "/source.mkv" {
		t.Fatalf("valid room grant rejected or lost its media binding: %v", err)
	}
	if service.sessions[grant.Token].Scope != models.SessionScopeStream || !strings.HasPrefix(service.sessions[grant.Token].ScopeResource, watchPartyPlaybackResourcePrefix) {
		t.Fatal("validation overwrote the persisted room scope")
	}
	active = false
	if _, err = service.Validate(grant.Token); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal("ended room still grants media access")
	}
	share, err := service.CreateScopedWithResource("family", false, "browser", "192.0.2.1", time.Hour, models.SessionScopeStream, "/source.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Validate(share.Token); err != nil {
		t.Fatal("ending a party revoked an independent share")
	}
}

func TestWatchPartyMediaGrantFailsClosedOnMalformedOrMissingBinding(t *testing.T) {
	service, err := NewService("", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	service.SetWatchPartyPlaybackValidator(func(context.Context, string, string, string) error { return nil })
	for _, value := range []string{"invalid", `{}`, `{"roomId":"r","guestId":"g"}`} {
		grant, err := service.createScopedWithResource("family", false, "browser", "192.0.2.1", time.Hour, models.SessionScopeStream, watchPartyPlaybackResourcePrefix+value, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = service.Validate(grant.Token); !errors.Is(err, ErrSessionNotFound) {
			t.Fatal("malformed room binding accepted")
		}
	}
}

func TestOrdinaryShareCannotInjectWatchPartyBinding(t *testing.T) {
	service, err := NewService("", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateScopedWithResource("family", false, "browser", "192.0.2.1", time.Hour, models.SessionScopeStream, watchPartyPlaybackResourcePrefix+`{"roomId":"foreign","guestId":"guest","resource":"/movie.mkv"}`)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatal("ordinary share source injected a room envelope")
	}
}
