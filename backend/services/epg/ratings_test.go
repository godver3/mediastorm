package epg

import (
	"novastream/models"
	"strings"
	"testing"
)

func TestXMLTVPreservesMultipleRatingSystems(t *testing.T) {
	s := &Service{}
	schedule := models.EPGSchedule{Channels: map[string]models.EPGChannel{}, Programs: map[string][]models.EPGProgram{}}
	xml := `<tv><channel id="channel"><display-name>Channel</display-name></channel><programme start="20261113100000 +0000" stop="20261113110000 +0000" channel="channel"><title>Fixture</title><rating system="MPAA"><value>PG</value></rating><rating system="FSK"><value>6</value></rating></programme></tv>`
	if err := s.parseXMLTV(strings.NewReader(xml), &schedule); err != nil {
		t.Fatal(err)
	}
	for _, programs := range schedule.Programs {
		if len(programs) != 1 || programs[0].Rating != "PG" || len(programs[0].Ratings) != 2 {
			t.Fatalf("ratings lost: %+v", programs)
		}
		if age, ok := models.ProgrammeMinimumAge(programs[0]); !ok || age != 6 {
			t.Fatal("German rating hidden behind first rating")
		}
		return
	}
	t.Fatal("programme not parsed")
}
