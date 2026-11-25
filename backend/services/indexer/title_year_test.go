package indexer

import (
	"slices"
	"testing"

	"novastream/models"
	"novastream/services/debrid"
	"novastream/utils/filter"
)

// A year in the title survives query building and filtering without help from
// an alias or an IMDb ID.
func TestTitleYearSearchWithoutAlias(t *testing.T) {
	opts := SearchOptions{Query: "Blade Runner 2099 S01E01", MediaType: "series", Year: 2026}
	parsed := debrid.ParseQueryWithYear(opts.Query, opts.Year)
	queries := buildSearchQueries(opts, parsed, nil)
	if !slices.Equal(queries, []string{"Blade Runner 2099 S01E01"}) {
		t.Fatalf("queries = %q", queries)
	}
	svc := &Service{}
	filterOpts := svc.buildFilterOptions(opts, models.FilterSettings{}, []string{})
	if filterOpts.ExpectedTitle != "Blade Runner 2099" || filterOpts.ExpectedYear != 2026 {
		t.Fatalf("expected title %q year %d", filterOpts.ExpectedTitle, filterOpts.ExpectedYear)
	}
	got := filter.Results([]models.NZBResult{
		{Title: "Blade.Runner.2099.S01E01.Hades.1080p.AMZN.WEB-DL"},
		{Title: "Blade.Runner.Black.Lotus.S01E01.1080p.WEB-DL"},
	}, filterOpts)
	if len(got) != 1 || got[0].Title != "Blade.Runner.2099.S01E01.Hades.1080p.AMZN.WEB-DL" {
		t.Fatalf("passed = %+v", got)
	}
}
