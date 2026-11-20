package providerbreaker

import (
	"context"
	"errors"
	"testing"
	"time"
)

type memoryCooldowns map[string]Cooldown

func (s memoryCooldowns) LoadCooldowns(context.Context) (map[string]Cooldown, error) { return s, nil }
func (s memoryCooldowns) SaveCooldown(_ context.Context, provider string, c Cooldown) error {
	s[provider] = c
	return nil
}
func (s memoryCooldowns) ClearCooldown(_ context.Context, provider string, until time.Time) error {
	if !s[provider].Until.After(until) {
		delete(s, provider)
	}
	return nil
}

func TestPersistedCooldownRestoresHalfOpenBackoff(t *testing.T) {
	now := time.Now().UTC()
	store := memoryCooldowns{"fixture": {Until: now.Add(-time.Second), Failures: 2}}
	b := New()
	b.now = func() time.Time { return now }
	if err := b.Restore(t.Context(), store); err != nil {
		t.Fatal(err)
	}
	allowed, _, probe := b.Allow("fixture")
	if !allowed || !probe {
		t.Fatal("restored expired circuit did not half-open")
	}
	// A failed body must release the half-open probe without erasing its history.
	b.ReleaseProbe("fixture", probe)
	if allowed, _, probe = b.Allow("fixture"); !allowed || !probe {
		t.Fatal("failed probe left circuit stuck")
	}
	until := b.RecordRateLimit("fixture", 0)
	if until.Sub(now) != 4*time.Hour {
		t.Fatalf("backoff history lost: %v", until.Sub(now))
	}
	b.RecordSuccess("fixture", true) // late success must not erase the newer 429
	if allowed, _, _ := b.Allow("fixture"); allowed || store["fixture"].Until != until {
		t.Fatal("late success erased a newer cooldown")
	}
	now = until
	if allowed, _, probe = b.Allow("fixture"); !allowed || !probe {
		t.Fatal("missing next probe")
	}
	b.RecordSuccess("fixture", probe)
	if len(store) != 0 {
		t.Fatal("successful probe did not clear persisted cooldown")
	}
}

type brokenCooldownStore struct{ memoryCooldowns }

func (brokenCooldownStore) LoadCooldowns(context.Context) (map[string]Cooldown, error) {
	return nil, errors.New("unavailable")
}

func TestRestoreRefusesStorageFailure(t *testing.T) {
	if err := New().Restore(t.Context(), brokenCooldownStore{}); err == nil {
		t.Fatal("ignored storage failure")
	}
}

func TestLongRetryHintsAreCappedButNotShortened(t *testing.T) {
	b := New()
	now := time.Now()
	b.now = func() time.Time { return now }
	if until := b.RecordRateLimit("fixture", 365*24*time.Hour); until.Sub(now) != maximumCooldown {
		t.Fatalf("year-long hint not capped: %v", until.Sub(now))
	}
	until := b.RecordRateLimit("fixture", 12*time.Hour)
	if until.Sub(now) != maximumCooldown {
		t.Fatal("shorter hint shortened existing cooldown")
	}
	if got := b.RecordRateLimit("fixture", 0); got.Before(until) {
		t.Fatal("later short hint shortened existing cooldown")
	}
}

type longCooldownStore struct{ brokenCooldownStore }

func (longCooldownStore) LoadCooldowns(context.Context) (map[string]Cooldown, error) {
	return map[string]Cooldown{"fixture": {Until: time.Now().Add(365 * 24 * time.Hour), Failures: 1}}, nil
}

func TestRestoreCapsPersistedCooldowns(t *testing.T) {
	b := New()
	if err := b.Restore(t.Context(), longCooldownStore{}); err != nil {
		t.Fatal(err)
	}
	if _, until, _ := b.Allow("fixture"); time.Until(until) > maximumCooldown+time.Minute {
		t.Fatalf("restored cooldown not capped: %v", time.Until(until))
	}
}

func TestSetPersistGatesSavingPauses(t *testing.T) {
	store := memoryCooldowns{}
	b := New()
	if err := b.Restore(t.Context(), store); err != nil {
		t.Fatal(err)
	}
	on := false
	b.SetPersist(func() bool { return on })
	b.RecordRateLimit("off", time.Minute)
	on = true
	b.RecordRateLimit("on", time.Minute)
	if _, ok := store["off"]; ok {
		t.Fatal("pause saved while disabled")
	}
	if _, ok := store["on"]; !ok {
		t.Fatal("pause not saved while enabled")
	}
}
