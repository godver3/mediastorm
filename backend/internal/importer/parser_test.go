package importer

import (
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
