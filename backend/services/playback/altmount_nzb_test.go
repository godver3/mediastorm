package playback

import (
	"strings"
	"testing"

	"novastream/models"
)

func TestPrepareAltMountNZBSubmissionRewritesObfuscatedMediaName(t *testing.T) {
	release := "Rick.and.Morty.S09E01.Theres.Something.About.Morty.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb-Scrambled"
	input := `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <head>
    <meta type="name">` + release + `</meta>
    <meta type="title">96345F4P63F13t0P61t63k4J70J00437.mkv</meta>
  </head>
  <file poster="poster" date="1781753357" subject="` + release + ` [2/7] &quot;` + release + `.vol-01.par2&quot; yEnc (1/1)">
    <groups><group>alt.binaries.hdtv.repost</group></groups>
    <segments><segment bytes="6681" number="1">par2-id</segment></segments>
  </file>
  <file poster="poster" date="1781753357" subject="` + release + ` [1/7] &quot;TKpvdrWv5Ho7.mkv&quot; yEnc (1/269)">
    <groups><group>alt.binaries.hdtv.repost</group></groups>
    <segments>
      <segment bytes="3961388" number="1">video-id-1</segment>
      <segment bytes="3961201" number="2">video-id-2</segment>
    </segments>
  </file>
</nzb>`

	gotBytes, gotName := prepareAltMountNZBSubmission(models.NZBResult{Title: release}, []byte(input), release+".nzb")
	got := string(gotBytes)
	wantMedia := release + ".mkv"
	if gotName != wantMedia+".nzb" {
		t.Fatalf("fileName = %q, want %q", gotName, wantMedia+".nzb")
	}
	if !strings.Contains(got, `meta type="name">`+wantMedia+`</meta>`) {
		t.Fatalf("rewritten NZB does not contain media name meta: %s", got)
	}
	if !strings.Contains(got, `subject="`+release+` [1/7] &#34;`+wantMedia+`&#34; yEnc (1/269)"`) {
		t.Fatalf("rewritten NZB does not contain media subject: %s", got)
	}
	if !strings.Contains(got, "video-id-2") {
		t.Fatalf("rewritten NZB dropped segment IDs: %s", got)
	}
}

func TestPrepareAltMountNZBSubmissionLeavesNonMediaNZBUnchanged(t *testing.T) {
	input := []byte(`<?xml version="1.0"?><nzb><file subject="release.par2"><segments><segment bytes="123" number="1">id</segment></segments></file></nzb>`)
	gotBytes, gotName := prepareAltMountNZBSubmission(models.NZBResult{Title: "release"}, input, "release.nzb")
	if string(gotBytes) != string(input) {
		t.Fatalf("NZB changed unexpectedly: %s", string(gotBytes))
	}
	if gotName != "release.nzb" {
		t.Fatalf("fileName = %q, want release.nzb", gotName)
	}
}
