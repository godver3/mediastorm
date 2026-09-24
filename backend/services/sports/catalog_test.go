package sports

import (
	"novastream/config"
	"testing"
)

func TestLeagueCatalogIncludesConfiguredSports(t *testing.T) {
	required := []string{"nfl", "college-football", "nba", "wnba", "mlb", "nhl", "soccer-uefa.europa.conf", "ufc", "atp", "f1", "nascar", "indycar", "rugby-180659", "rugby-league-3"}
	seen := make(map[string]League)
	for _, league := range LeagueCatalog {
		seen[league.ID] = league
	}
	for _, id := range required {
		if _, ok := seen[id]; !ok {
			t.Errorf("league catalog missing %q", id)
		}
	}
	if seen["ufc"].SupportsTeams {
		t.Error("UFC must use event-title matching, not team linking")
	}
	if !seen["nba"].SupportsTeams {
		t.Error("NBA must expose its complete team catalog")
	}
}

func TestEveryCatalogLeagueHasAHubFeed(t *testing.T) {
	for _, league := range LeagueCatalog {
		if league.active() && !supportsHubLeague(league.ID) && racingSlug(league.ID) == "" && league.ID != "motogp" && league.Sport != "cycling" {
			t.Errorf("league %q is configurable but has no Sports Hub feed", league.ID)
		}
	}
}

func TestDefaultLeaguesIncludeEntireCatalog(t *testing.T) {
	want := config.DefaultSettings().Sports.EnabledLeagues
	if len(want) != 55 {
		t.Fatal("config defaults do not match full catalog")
	}
	got := defaultLeagues()
	if len(got) < len(want) {
		t.Fatalf("default leagues = %d, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("default[%d] = %q, want %q", i, got[i].ID, id)
		}
	}
}

func TestSavedLeagueSubsetIsPreserved(t *testing.T) {
	settings := config.SportsSettings{EnabledLeagues: []string{"nba"}}
	settings.Normalize()
	if len(settings.EnabledLeagues) != 1 || settings.EnabledLeagues[0] != "nba" {
		t.Fatal("overwrote saved selection")
	}
	var fresh config.SportsSettings
	fresh.Normalize()
	if len(fresh.EnabledLeagues) != 55 {
		t.Fatal("missing defaults")
	}
}

func TestCyclingAvailabilityControlsSeparateFeed(t *testing.T) {
	s := NewService(t.TempDir())
	if len(s.enabledCyclingCompetitions()) != len(cyclingCompetitions) {
		t.Fatal("cycling missing from defaults")
	}
	s.SetEnabledLeagueIDs([]string{"aso:tour", "rcs:giro"})
	selected := s.enabledCyclingCompetitions()
	if len(selected) != 2 || selected[0].id != "tour" || selected[1].id != "giro" {
		t.Fatalf("wrong cycling selection: %v", selected)
	}
	s.SetEnabledLeagueIDs([]string{"nba"})
	if len(s.enabledCyclingCompetitions()) != 0 {
		t.Fatal("disabled cycling still selected")
	}
}
