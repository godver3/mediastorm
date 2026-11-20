package epg

import (
	"strings"
	"testing"

	"novastream/models"
)

// Episode titles and years from the guide, as epgshare01 lists them.
func TestParseXMLTVEpisodeDetails(t *testing.T) {
	const doc = `<?xml version="1.0"?>
<tv>
  <channel id="one.de"><display-name>One</display-name></channel>
  <programme start="20261120180000 +0000" stop="20261120190000 +0000" channel="one.de">
    <title lang="de">Die Kanzlei</title><sub-title lang="de">Schein und Sein</sub-title>
    <date>2023</date><episode-num system="onscreen">S7 E3/12</episode-num>
  </programme>
  <programme start="20261120190000 +0000" stop="20261120200000 +0000" channel="one.de">
    <title>Film</title><date>19990315</date>
  </programme>
  <programme start="20261120200000 +0000" stop="20261120210000 +0000" channel="one.de">
    <title>Ohne</title><date>n/a</date>
  </programme>
</tv>`
	schedule := &models.EPGSchedule{Channels: map[string]models.EPGChannel{}, Programs: map[string][]models.EPGProgram{}}
	if err := (&Service{}).parseXMLTV(strings.NewReader(doc), schedule); err != nil {
		t.Fatalf("parseXMLTV: %v", err)
	}
	p := schedule.Programs["one.de"]
	if len(p) != 3 {
		t.Fatalf("programmes = %+v", p)
	}
	if p[0].SubTitle != "Schein und Sein" || p[0].Year != 2023 || p[0].Episode != "S7 E3/12" {
		t.Fatalf("episode = %+v", p[0])
	}
	if p[1].SubTitle != "" || p[1].Year != 1999 || p[2].Year != 0 {
		t.Fatalf("years = %d, %d", p[1].Year, p[2].Year)
	}
}
