package recordings

import (
	"testing"
	"time"

	"novastream/models"
)

type fakeSchedule map[string][]models.EPGProgram

func (f fakeSchedule) GetScheduleMultiple(ids []string, start, end time.Time) map[string][]models.EPGProgram {
	out := map[string][]models.EPGProgram{}
	for _, id := range ids {
		for _, p := range f[id] {
			if p.Stop.After(start) && p.Start.Before(end) {
				out[id] = append(out[id], p)
			}
		}
	}
	return out
}

// A recording made from the guide keeps what the guide said about the
// programme at its start, with the profile's guide time correction applied.
func TestGuideForRecording(t *testing.T) {
	at := time.Date(2026, 11, 20, 20, 15, 0, 0, time.UTC)
	s := &Service{epg: fakeSchedule{"one.de": {
		{Title: "Vorher", Start: at.Add(-time.Hour), Stop: at, Icon: "https://img.example/a.jpg"},
		{Title: "Rerun", Start: at, Stop: at.Add(time.Hour), Icon: "https://img.example/r.jpg"},
		{Title: "Die Kanzlei", SubTitle: "Schein und Sein", Episode: "S7 E3/12", Year: 2023, Categories: []string{"Serie"}, Rating: "12",
			Icon: "https://img.example/k.jpg", Start: at.Add(time.Minute), Stop: at.Add(time.Hour)},
	}}}
	g := s.guideFor("p", "one.de", at, "Die Kanzlei")
	if g == nil || g.EpisodeTitle != "Schein und Sein" || g.Episode != "S7 E3/12" || g.Year != 2023 || g.Image != "https://img.example/k.jpg" || g.Rating != "12" || len(g.Categories) != 1 {
		t.Fatalf("guide = %+v", g)
	}
	// Without a title match the programme at the start wins.
	if g := s.guideFor("p", "one.de", at, "Etwas anderes"); g == nil || g.Image != "https://img.example/r.jpg" {
		t.Fatalf("guide by start = %+v", g)
	}
	// With a one-hour guide correction for this profile, a recording at
	// 21:15 is the guide's 20:15 programme.
	s.epgoffset = func(string) time.Duration { return time.Hour }
	if g := s.guideFor("p", "one.de", at.Add(time.Hour), "Unbekannt"); g == nil || g.Image != "https://img.example/r.jpg" {
		t.Fatalf("offset guide = %+v", g)
	}
	if s.guideFor("p", "", at, "x") != nil || s.guideFor("p", "two.de", at, "x") != nil {
		t.Fatal("guide without a channel or programme")
	}
	if guideFromProgram(models.EPGProgram{Title: "Nur Titel"}) != nil {
		t.Fatal("details for a programme with nothing to keep")
	}
	if g := guideFromProgram(models.EPGProgram{Icon: "file:///etc/passwd", Year: 2020}); g == nil || g.Image != "" {
		t.Fatalf("unsafe image kept: %+v", g)
	}
}
