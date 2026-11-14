package handlers

import (
	"testing"
	"time"

	"novastream/models"
)

func TestRegionalHomeRevisionRefreshesActiveRowsWithoutPersonalEdits(t *testing.T) {
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	for _, shelf := range []models.ShelfConfig{{ID: "regional-cinema-de-movie", Enabled: true}, {ID: "custom-chart", Enabled: true, TMDBDiscoverQuery: "country_chart=netflix&watch_region=DE"}} {
		rows := []models.ShelfConfig{shelf}
		before := regionalHomeRevision(rows, now)
		if before == "" || regionalHomeRevision(rows, now.Add(time.Hour)) != before || regionalHomeRevision(rows, now.Add(6*time.Hour)) == before {
			t.Fatal("public data would remain stale or reload continuously")
		}
		shelf.Enabled = false
		if regionalHomeRevision([]models.ShelfConfig{shelf}, now) != "" {
			t.Fatal("hidden row caused refresh")
		}
	}
	if regionalHomeRevision([]models.ShelfConfig{{ID: "ordinary-list", Enabled: true}}, now) != "" {
		t.Fatal("unrelated home received regional refresh")
	}
}
