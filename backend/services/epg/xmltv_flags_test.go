package epg

import (
	"strings"
	"testing"

	"novastream/models"
)

func TestParseXMLTVAiringFlags(t *testing.T) {
	const doc = `<?xml version="1.0"?>
<tv>
  <channel id="news.1"><display-name>News</display-name></channel>
  <programme start="20261106180000 +0000" stop="20261106190000 +0000" channel="news.1"><title>A</title><new/></programme>
  <programme start="20261106190000 +0000" stop="20261106200000 +0000" channel="news.1"><title>B</title><previously-shown start="20250101"/></programme>
  <programme start="20261106200000 +0000" stop="20261106210000 +0000" channel="news.1"><title>C</title><premiere>Series premiere</premiere></programme>
  <programme start="20261106210000 +0000" stop="20261106220000 +0000" channel="news.1"><title>D</title><live/></programme>
  <programme start="20261106220000 +0000" stop="20261106230000 +0000" channel="news.1"><title>E</title></programme>
</tv>`
	schedule := &models.EPGSchedule{Channels: map[string]models.EPGChannel{}, Programs: map[string][]models.EPGProgram{}}
	if err := (&Service{}).parseXMLTV(strings.NewReader(doc), schedule); err != nil {
		t.Fatalf("parseXMLTV: %v", err)
	}
	want := map[string][2]bool{"A": {true, false}, "B": {false, true}, "C": {true, false}, "D": {true, false}, "E": {false, false}}
	programs := schedule.Programs["news.1"]
	if len(programs) != len(want) {
		t.Fatalf("got %d programs, want %d", len(programs), len(want))
	}
	for _, p := range programs {
		if got := [2]bool{p.New, p.PreviouslyShown}; got != want[p.Title] {
			t.Errorf("%s: new/previouslyShown = %v, want %v", p.Title, got, want[p.Title])
		}
	}
}
