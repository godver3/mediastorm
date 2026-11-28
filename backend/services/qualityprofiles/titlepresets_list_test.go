package qualityprofiles

import (
	"context"
	"testing"
)

func TestMemoryStoreListTitlePresets(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStore()
	_ = m.SetTitlePreset(ctx, "u2", "", "tmdb:movie:2", "p2")
	_ = m.SetTitlePreset(ctx, "u1", "tv", "tmdb:tv:1", "p1")
	recs, err := m.ListTitlePresets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].UserID != "u1" || recs[0].ClientID != "tv" || recs[0].ContentID != "tmdb:tv:1" || recs[0].PresetID != "p1" {
		t.Fatalf("unexpected records: %+v", recs)
	}
	if recs[1].ClientID != "" || recs[1].PresetID != "p2" {
		t.Fatalf("empty client id not preserved: %+v", recs[1])
	}
}
