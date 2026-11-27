package epg

import (
	"reflect"
	"testing"
	"time"

	"novastream/models"
)

func TestCompactSchedulePreservesContents(t *testing.T) {
	start := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	mk := func() *models.EPGSchedule {
		return &models.EPGSchedule{
			Channels: map[string]models.EPGChannel{
				"a": {ID: "a", Name: "A", Aliases: []string{"A HD"}},
				"b": {ID: "b", Name: "B"},
			},
			Programs: map[string][]models.EPGProgram{
				"a": append(make([]models.EPGProgram, 0, 16),
					models.EPGProgram{ChannelID: "a", Title: "News", Start: start, Stop: start.Add(time.Hour), Categories: []string{"News", "Live"}, New: true},
					models.EPGProgram{ChannelID: "a", Title: "News", Start: start.Add(time.Hour), Stop: start.Add(2 * time.Hour), Categories: []string{"News", "Live"}},
				),
				"b":     {{ChannelID: "b", Title: "Film", Year: 1999, Categories: []string{"Movie"}}},
				"empty": {},
			},
			LastUpdated: start,
			SourceType:  "xmltv",
		}
	}
	want := mk()
	got := mk()
	compactSchedule(got)

	if len(got.Programs["a"]) != cap(got.Programs["a"]) {
		t.Fatalf("program slice not exact-sized: len=%d cap=%d", len(got.Programs["a"]), cap(got.Programs["a"]))
	}
	if _, ok := got.Programs["empty"]; !ok {
		t.Fatal("channel key with no programs was dropped")
	}
	want.Programs["empty"] = nil
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("compaction changed contents:\nwant %+v\ngot  %+v", want, got)
	}

	// Shared category slices must not alias on append.
	c0 := got.Programs["a"][0].Categories
	c1 := got.Programs["a"][1].Categories
	c0 = append(c0, "Extra")
	if len(c1) != 2 || c1[0] != "News" || c1[1] != "Live" {
		t.Fatalf("append to one program's categories leaked into another: %v", c1)
	}
	_ = c0
}
