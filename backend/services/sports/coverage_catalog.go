package sports

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"novastream/models"
	"strings"
)

// Coverage decisions are versioned with provider evidence. A directory entry is
// not automatically a supported league and never enters refresh just by existing.
//
//go:embed coverage_catalog.json
var coverageCatalogJSON []byte

type coverageDescriptor struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	Provider             string   `json:"provider"`
	ProviderSport        string   `json:"providerSport"`
	ProviderLeague       string   `json:"providerLeague"`
	ApplicationSport     string   `json:"applicationSport"`
	College              bool     `json:"college"`
	Adapter              string   `json:"adapter"`
	ImplementationStatus string   `json:"implementationStatus"`
	Capabilities         []string `json:"capabilities"`
	Aliases              []string `json:"aliases"`
	CoverageNote         string   `json:"coverageNote"`
}

func applicationSport(sport, slug string) string {
	if slug == "college-softball" || slug == "lls" {
		return "softball"
	}
	switch sport {
	case "football":
		return "american-football"
	case "hockey":
		return "ice-hockey"
	case "rugby":
		return "rugby-union"
	case "racing":
		return "motorsport"
	}
	return sport
}
func (l League) active() bool {
	return l.ImplementationStatus == "" || ((l.ImplementationStatus == "validated" || l.ImplementationStatus == "limited") && l.hasCapability("schedule"))
}
func (l League) hasCapability(name string) bool {
	for _, c := range l.Capabilities {
		if c == name {
			return true
		}
	}
	return false
}
func wantedAll(ids map[string]struct{}) bool { _, ok := ids["*"]; return ok }
func extendLeagueCatalog(legacy []League) []League {
	var additions []coverageDescriptor
	if err := json.Unmarshal(coverageCatalogJSON, &additions); err != nil {
		panic(fmt.Sprintf("invalid embedded coverage catalog: %v", err))
	}
	seen := map[string]bool{}
	for i := range legacy {
		l := &legacy[i]
		seen[l.ID] = true
		l.ApplicationSport = applicationSport(l.Sport, l.Slug)
		l.College = strings.Contains(l.Slug, "college")
		l.Provider = "espn"
		l.Adapter = l.EventKind
		l.ImplementationStatus = "validated"
		l.Capabilities = []string{"schedule"}
		if l.ID == "motogp" {
			l.Provider = "organizer"
			l.Capabilities = append(l.Capabilities, "live-score", "final-result")
		} else if l.Sport == "cycling" {
			l.Provider = "organizer"
			l.ImplementationStatus = "limited"
		} else if l.ID == "boxing" {
			l.Provider = "thesportsdb"
			l.ImplementationStatus = "limited"
		} else {
			l.Capabilities = append(l.Capabilities, "live-score", "final-result")
		}
		if l.SupportsTeams {
			l.Capabilities = append(l.Capabilities, "team-catalog")
		}
	}
	for _, entry := range additions {
		if entry.ID == "" || seen[entry.ID] || entry.ProviderSport == "" || entry.ProviderLeague == "" {
			panic("invalid or duplicate coverage descriptor")
		}
		if entry.ID != "espn:"+entry.ProviderSport+":"+entry.ProviderLeague {
			panic("noncanonical coverage descriptor")
		}
		seen[entry.ID] = true
		app := entry.ApplicationSport
		if app == "" {
			app = applicationSport(entry.ProviderSport, entry.ProviderLeague)
		}
		kind := "matchup"
		switch entry.ProviderSport {
		case "golf":
			if entry.ProviderLeague != "tgl" {
				kind = "tournament"
			}
		case "mma":
			kind = "fight-card"
		case "racing":
			kind = "race"
		}
		l := League{ID: entry.ID, Name: entry.Name, Sport: entry.ProviderSport, Slug: entry.ProviderLeague, Category: entry.ProviderSport, EventKind: kind, Provider: entry.Provider, ApplicationSport: app, College: entry.College, Adapter: entry.Adapter, ImplementationStatus: entry.ImplementationStatus, Capabilities: entry.Capabilities, Aliases: entry.Aliases, CoverageNote: entry.CoverageNote}
		l.SupportsTeams = l.hasCapability("team-catalog") || l.hasCapability("team-identities")
		legacy = append(legacy, l)
	}
	return legacy
}
func (l League) descriptor(enabled bool) models.SportsLeague {
	return models.SportsLeague{ID: l.ID, Name: l.Name, Sport: l.Sport, Category: l.Category, EventKind: l.EventKind, SupportsTeams: l.SupportsTeams, Enabled: enabled, Provider: l.Provider, ProviderSport: l.Sport, ProviderLeague: l.Slug, ApplicationSport: l.ApplicationSport, College: l.College, Adapter: l.Adapter, ImplementationStatus: l.ImplementationStatus, Capabilities: l.Capabilities, Aliases: l.Aliases, CoverageNote: l.CoverageNote}
}
func providerEventID(game models.SportsGame) string {
	if game.ProviderEventID != "" {
		return game.ProviderEventID
	}
	return game.ID
}
