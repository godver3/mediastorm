package sports

import (
	"encoding/json"
	"novastream/models"
	"testing"
)

func TestCoverageCatalogCandidatesNeverPoll(t *testing.T) {
	s := NewService(t.TempDir())
	s.SetEnabledLeagueIDs([]string{"espn:australian-football:afl"})
	for _, l := range s.Leagues() {
		if l.ID == "espn:australian-football:afl" && l.ImplementationStatus == "candidate" && l.Enabled {
			t.Fatal("candidate was activated")
		}
	}
	s.SetEnabledLeagueIDs([]string{"mlb"})
	for _, l := range s.Leagues() {
		if l.Enabled && l.ID != "mlb" {
			t.Fatal("explicit saved selection expanded")
		}
	}
}
func TestCoverageDescriptorPublicContract(t *testing.T) {
	l := League{ID: "espn:lacrosse:test", Name: "Test league", Sport: "lacrosse", Slug: "test", ApplicationSport: "lacrosse", Provider: "espn", ImplementationStatus: "limited", Capabilities: []string{"schedule"}, EventKind: "matchup", College: true}
	data, err := json.Marshal(l.descriptor(true))
	if err != nil {
		t.Fatal(err)
	}
	var got models.SportsLeague
	if err = json.Unmarshal(data, &got); err != nil || got.ProviderLeague != "test" || !got.College || !got.Enabled || got.ApplicationSport != "lacrosse" {
		t.Fatalf("descriptor=%s error=%v", data, err)
	}
	if !l.active() {
		t.Fatal("evidence-backed schedule not active")
	}
	l.Capabilities = nil
	if l.active() {
		t.Fatal("status alone activated league")
	}
}

func TestOrganizerProvidersAreNotAdvertisedAsESPN(t *testing.T) {
	for _, league := range LeagueCatalog {
		if league.ID == "motogp" || league.Sport == "cycling" {
			if league.Provider != "organizer" {
				t.Errorf("%s provider=%s", league.ID, league.Provider)
			}
		}
	}
}
