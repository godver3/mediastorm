package qualityprofiles

import (
	"context"
	"strings"
	"testing"

	"novastream/internal/arr"
)

// A preset's fallback is nothing, the simple ranking or another preset of
// the same app that does not lead back to it; deleting a preset clears the
// fallbacks pointing at it.
func TestPresetFallbackSettings(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	mk := func(app arr.App, name string) *Preset {
		t.Helper()
		list := `[` + cfJSON("x-"+name, "X "+name, `\bfoo\b`, map[string]int{"default": 50}) + `]`
		p, err := svc.ImportJSON(ctx, []byte(list), app, name)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	uhd, hd, sd, movies := mk(arr.AppSonarr, "UHD"), mk(arr.AppSonarr, "HD"), mk(arr.AppSonarr, "SD"), mk(arr.AppRadarr, "Movies")
	set := func(p *Preset, fallback string) (*Preset, error) {
		return svc.UpdateSettings(ctx, p.ID, SettingsUpdate{Fallback: &fallback})
	}

	if p, err := set(uhd, hd.ID); err != nil || p.Fallback != hd.ID {
		t.Fatalf("fallback to another series preset: %v %+v", err, p)
	}
	if p, err := set(hd, "Simple"); err != nil || p.Fallback != SimpleFallback {
		t.Fatalf("fallback to the simple ranking: %v", err)
	}
	if _, err := set(sd, sd.ID); err == nil {
		t.Error("a preset must not fall back to itself")
	}
	if _, err := set(sd, movies.ID); err == nil || !strings.Contains(err.Error(), "sonarr") {
		t.Errorf("a movie preset must not be a series preset's fallback: %v", err)
	}
	if _, err := set(sd, "no-such-preset"); err == nil {
		t.Error("unknown fallback accepted")
	}
	// UHD -> HD; HD -> UHD would be a loop.
	if _, err := set(hd, uhd.ID); err == nil {
		t.Error("a fallback loop was accepted")
	}

	// The compiled preset carries the fallback (a change renews it, as it
	// changes how the preset ranks).
	if _, err := set(sd, hd.ID); err != nil {
		t.Fatalf("set SD fallback: %v", err)
	}
	if a, err := svc.Active(ctx, sd.ID); err != nil || a.Fallback != hd.ID {
		t.Fatalf("compiled preset without its fallback: %v %+v", err, a)
	}
	if s := mustGet(t, svc, sd.ID).Summary(); s.Fallback != hd.ID {
		t.Errorf("summary fallback = %q", s.Fallback)
	}

	if err := svc.Delete(ctx, hd.ID); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*Preset{uhd, sd} {
		if got := mustGet(t, svc, p.ID).Fallback; got != "" {
			t.Errorf("%s still falls back to the deleted preset: %q", p.Name, got)
		}
	}

	if p, err := set(uhd, ""); err != nil || p.Fallback != "" {
		t.Errorf("clearing the fallback: %v", err)
	}
}

func mustGet(t *testing.T, svc *Service, id string) *Preset {
	t.Helper()
	p, err := svc.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
