package metadata

import (
	"fmt"
	"testing"
)

func TestRegionalDiscoveryWarmRequestsAreBoundedAndDeduplicated(t *testing.T) {
	input := []RegionalDiscoveryWarmShelf{}
	for n := 0; n < 40; n++ {
		entry := RegionalDiscoveryWarmShelf{Language: "ger", Options: TMDBListOptions{SourceType: "custom-discover", MediaType: "movie", DiscoverQuery: fmt.Sprintf("watch_region=DE&with_watch_providers=%d", n), Limit: 500}}
		input = append(input, entry, entry)
	}
	requests := uniqueRegionalWarmRequests(input)
	if len(requests) != 32 {
		t.Fatalf("request budget not enforced: %d", len(requests))
	}
	for _, request := range requests {
		if request.Options.Limit != 20 || !request.Options.DeferArtwork {
			t.Fatal("background worker requested heavy catalogue enrichment")
		}
	}
}
