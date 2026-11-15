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

// A room store outage is not a revocation: the guest keeps the grant and the
// caller can answer with a retryable error.
func TestWatchPartyMediaGrantSurvivesAuthorityOutage(t *testing.T) {
	service, err := NewService("", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := service.CreateWatchPartyPlayback("family", "browser", "192.0.2.1", time.Hour, "room", "guest", "/source.mkv")
	if err != nil {
		t.Fatal(err)
	}
	outage := true
	service.SetWatchPartyPlaybackValidator(func(context.Context, string, string, string) error {
		if outage {
			return errors.Join(ErrAuthorityUnavailable, errors.New("pool exhausted"))
		}
		return nil
	})
	if _, err = service.Validate(grant.Token); !errors.Is(err, ErrAuthorityUnavailable) || errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("outage reported as %v, want a retryable error", err)
	}
	outage = false
	if _, err = service.Validate(grant.Token); err != nil {
		t.Fatalf("grant lost after the outage: %v", err)
	}
}

// Successful room checks are reused briefly so each guest segment request
// does not query the room store; denials are never cached.
func TestWatchPartyMediaGrantCachesRecentAuthority(t *testing.T) {
	service, err := NewService("", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	service.watchPartyAuthorityTTL = time.Hour
	grant, err := service.CreateWatchPartyPlayback("family", "browser", "192.0.2.1", time.Hour, "room", "guest", "/source.mkv")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	allow := false
	service.SetWatchPartyPlaybackValidator(func(context.Context, string, string, string) error {
		calls++
		if !allow {
			return errors.New("room ended")
		}
		return nil
	})
	for i := 0; i < 3; i++ {
		if _, err = service.Validate(grant.Token); !errors.Is(err, ErrSessionNotFound) {
			t.Fatal("denied grant accepted")
		}
	}
	if calls != 3 {
		t.Fatalf("denials were cached: %d checks", calls)
	}
	allow = true
	for i := 0; i < 5; i++ {
		if _, err = service.Validate(grant.Token); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 4 {
		t.Fatalf("room checked %d times for 5 requests, want 1", calls-3)
	}
	service.mu.Lock()
	for k := range service.watchPartyAuthority {
		service.watchPartyAuthority[k] = time.Now().Add(-time.Second)
	}
	service.mu.Unlock()
	allow = false
	if _, err = service.Validate(grant.Token); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal("expired cached authority still accepted")
	}
}
