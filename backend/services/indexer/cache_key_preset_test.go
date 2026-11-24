package indexer

import (
	"reflect"
	"testing"

	"novastream/services/qualityprofiles"
)

// Runtime and episode title only matter to a preset; without one, callers that
// pass different values (prequeue vs manual search) must share a cache entry.
func TestSearchCacheOptionsPresetOnlyFields(t *testing.T) {
	a := SearchOptions{Query: "Dark S01E01", RuntimeMinutes: 52, EpisodeTitle: "Secrets"}
	b := SearchOptions{Query: "Dark S01E01"}
	if !reflect.DeepEqual(buildSearchCacheOptions(a), buildSearchCacheOptions(b)) {
		t.Fatalf("without a preset, runtime/episode title must not split the cache key")
	}
	a.activePreset, b.activePreset = &qualityprofiles.Active{}, &qualityprofiles.Active{}
	if reflect.DeepEqual(buildSearchCacheOptions(a), buildSearchCacheOptions(b)) {
		t.Fatalf("with a preset, runtime/episode title must be part of the cache key")
	}
}
