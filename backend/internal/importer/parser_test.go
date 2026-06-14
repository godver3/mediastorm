package importer

import (
	"bytes"
	"testing"

	"github.com/javi11/nzbparser"
)

func sizes(segs []nzbparser.NzbSegment) []int {
	out := make([]int, len(segs))
	for i, s := range segs {
		out[i] = s.Bytes
	}
	return out
}

func TestIsRecoveryFile(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"Movie.part002.rev", true},
		{"Movie.REV", true},
		{`(005/386) - Description - "Once.Upon.a.Time.2019.part002.rev" -`, true}, // subject form
		{"Movie.par2", true},
		{"Movie.vol01+02.par2", true},
		{"Movie.PAR2", true},
		{"Movie.part001.rar", false},
		{"Movie.r00", false},
		{"Movie.mkv", false},
		{"Movie.revision.mkv", false}, // .rev only matches as a suffix
		{"", false},
	}
	for _, c := range cases {
		if got := isRecoveryFile(c.name); got != c.want {
			t.Errorf("isRecoveryFile(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNormalizeNzbSubjects(t *testing.T) {
	// Malformed NZB: filename lives in poster, subject absent (the KRaLiMaRKo case).
	malformed := []byte(`<nzb><file poster="&quot;movie.part001.rar&quot; - 74 GB - yEnc (1/2)" date="1"><groups><group>a.b.c</group></groups><segments><segment bytes="100" number="1">seg1@x</segment></segments></file>` +
		`<file poster="&quot;movie.part002.rar&quot; - 74 GB - yEnc (1/2)"><segments><segment bytes="100" number="1">seg2@x</segment></segments></file></nzb>`)
	out, fixed := normalizeNzbSubjects(malformed)
	if fixed != 2 {
		t.Fatalf("expected 2 files repaired, got %d", fixed)
	}
	if !bytes.Contains(out, []byte(`subject="&quot;movie.part001.rar&quot; - 74 GB - yEnc (1/2)"`)) ||
		!bytes.Contains(out, []byte(`subject="&quot;movie.part002.rar&quot; - 74 GB - yEnc (1/2)"`)) {
		t.Fatalf("poster value not copied into subject: %s", out)
	}

	// Empty subject attribute present + filename in poster: should be filled in place.
	emptySubj := []byte(`<file poster="&quot;a.part01.rar&quot; yEnc (1/1)" subject=""><segments></segments></file>`)
	out2, fixed2 := normalizeNzbSubjects(emptySubj)
	if fixed2 != 1 || !bytes.Contains(out2, []byte(`subject="&quot;a.part01.rar&quot; yEnc (1/1)"`)) {
		t.Fatalf("empty subject not repaired: fixed=%d out=%s", fixed2, out2)
	}

	// Standard NZB: real subject + poster is an uploader id — must be left untouched.
	standard := []byte(`<file poster="uploader@example.com" subject="[1/2] Movie - &quot;movie.mkv&quot; yEnc (1/2)"><segments></segments></file>`)
	out3, fixed3 := normalizeNzbSubjects(standard)
	if fixed3 != 0 || !bytes.Equal(out3, standard) {
		t.Fatalf("standard NZB should be unchanged, got fixed=%d", fixed3)
	}

	// Empty subject but poster is an ordinary id (no filename signal): do not fabricate.
	plainPoster := []byte(`<file poster="someuser" subject=""><segments></segments></file>`)
	_, fixed4 := normalizeNzbSubjects(plainPoster)
	if fixed4 != 0 {
		t.Fatalf("should not repair when poster is not a file description, got fixed=%d", fixed4)
	}
}

func TestApplyNormalizedSizes(t *testing.T) {
	tests := []struct {
		name      string
		in        []int
		firstPart int64
		lastPart  int64
		want      []int
	}{
		{
			name:      "multi-segment sets uniform body + distinct last",
			in:        []int{768000, 768000, 768000, 768000},
			firstPart: 739200, // decoded part size (~yEnc overhead stripped)
			lastPart:  123456,
			want:      []int{739200, 739200, 739200, 123456},
		},
		{
			name:      "single segment is left untouched",
			in:        []int{500000},
			firstPart: 739200,
			lastPart:  123456,
			want:      []int{500000},
		},
		{
			name:      "non-positive firstPart leaves body segments untouched, still fixes last",
			in:        []int{768000, 768000, 768000},
			firstPart: 0,
			lastPart:  123456,
			want:      []int{768000, 768000, 123456},
		},
		{
			name:      "non-positive lastPart leaves last untouched",
			in:        []int{768000, 768000, 768000},
			firstPart: 739200,
			lastPart:  0,
			want:      []int{739200, 739200, 768000},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			segs := make([]nzbparser.NzbSegment, len(tt.in))
			for i, b := range tt.in {
				segs[i] = nzbparser.NzbSegment{Number: i + 1, Bytes: b}
			}
			applyNormalizedSizes(segs, tt.firstPart, tt.lastPart)
			got := sizes(segs)
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("segment %d: got %d, want %d (full got=%v)", i, got[i], tt.want[i], got)
				}
			}
		})
	}
}
