package handlers

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

// packedAudioID3 builds the ID3 tag HLS packed audio starts with (as ARD and ZDF send it): one PRIV
// frame carrying the 33-bit timestamp, in ID3v2.4 (syncsafe sizes) or v2.3 (plain sizes).
func packedAudioID3(version byte, pts uint64) []byte {
	body := append([]byte(packedAudioTimestampOwner+"\x00"), make([]byte, 8)...)
	binary.BigEndian.PutUint64(body[len(body)-8:], pts)
	size := func(n int, safe bool) []byte {
		if !safe {
			b := make([]byte, 4)
			binary.BigEndian.PutUint32(b, uint32(n))
			return b
		}
		return []byte{byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)}
	}
	frame := append(append([]byte("PRIV"), size(len(body), version >= 4)...), 0, 0)
	frame = append(frame, body...)
	tag := append([]byte{'I', 'D', '3', version, 0, 0}, size(len(frame), true)...)
	return append(tag, frame...)
}

func TestPackedAudioTimestamp(t *testing.T) {
	// A ZDF tag: 0x1a972bdb4 ticks = 79309.294 s (its video segment started 4 ms before, at an AAC frame boundary).
	adts := []byte{0xff, 0xf1, 0x4c, 0x80}
	for _, version := range []byte{3, 4} {
		got, ok := packedAudioTimestamp(append(packedAudioID3(version, 0x1a972bdb4), adts...))
		if !ok || got < 79309.2940 || got > 79309.2941 {
			t.Fatalf("v2.%d: %v %v, want 79309.294", version, got, ok)
		}
	}
	// The 33 bits only.
	if got, ok := packedAudioTimestamp(packedAudioID3(4, 1<<33|90000)); !ok || got != 1 {
		t.Fatalf("masked: %v %v", got, ok)
	}
	// Another frame before the PRIV frame, and an extended header (v2.3: its size excludes the
	// size field; v2.4: a syncsafe size that includes it).
	withTXXX := func(version byte) []byte {
		tag := packedAudioID3(version, 90000)
		txxx := append([]byte("TXXX\x00\x00\x00\x04\x00\x00"), "\x03a\x00b"...)
		frames := append(txxx, tag[10:]...)
		header := append([]byte{'I', 'D', '3', version, 0, 0}, byte(len(frames)>>21&0x7f), byte(len(frames)>>14&0x7f), byte(len(frames)>>7&0x7f), byte(len(frames)&0x7f))
		return append(header, frames...)
	}
	withExtended := func(version byte) []byte {
		tag := packedAudioID3(version, 90000)
		ext := []byte{0, 0, 0, 6, 0, 0, 0, 0, 0, 0} // v2.3: 6 bytes after the size field
		if version >= 4 {
			ext = []byte{0, 0, 0, 6, 1, 0} // v2.4: 6 bytes in all
		}
		frames := append(ext, tag[10:]...)
		header := append([]byte{'I', 'D', '3', version, 0, 0x40}, byte(len(frames)>>21&0x7f), byte(len(frames)>>14&0x7f), byte(len(frames)>>7&0x7f), byte(len(frames)&0x7f))
		return append(header, frames...)
	}
	for _, version := range []byte{3, 4} {
		if got, ok := packedAudioTimestamp(withTXXX(version)); !ok || got != 1 {
			t.Errorf("v2.%d with TXXX first: %v %v", version, got, ok)
		}
		if got, ok := packedAudioTimestamp(withExtended(version)); !ok || got != 1 {
			t.Errorf("v2.%d with an extended header: %v %v", version, got, ok)
		}
	}
	for name, data := range map[string][]byte{
		"no tag":    adts,
		"short":     []byte("ID3\x04\x00"),
		"truncated": packedAudioID3(4, 1)[:20],
		"other priv": func() []byte {
			d := packedAudioID3(4, 1)
			copy(d[20:], "org")
			return d
		}(),
	} {
		if _, ok := packedAudioTimestamp(data); ok {
			t.Errorf("%s: found a timestamp", name)
		}
	}
}

func TestSegmentKindFromHead(t *testing.T) {
	for head, want := range map[string]string{
		"\x47\x40\x11":     liveBacklogSegmentTS,
		"ID3":              liveBacklogSegmentPacked,
		"\xff\xf1\x4c":     "", // ADTS without an ID3 tag: no timestamp
		"\x00\x00\x00\x18": "", // fMP4
		"":                 "",
	} {
		if got := segmentKindFromHead([]byte(head)); got != want {
			t.Errorf("%q: %q, want %q", head, got, want)
		}
	}
}

func TestLiveBacklogMuxArgsPackedAudio(t *testing.T) {
	args := strings.Join(liveBacklogMuxArgsFor("https://cdn/v1.ts", liveBacklogAudioInput{URL: "/out/segment1.ts.7.audio", File: true, Offset: 79309.290089},
		map[string]string{"Referer": "https://tv"}, liveBacklogAudio{decided: true, copy: true}, "/out/segment1.ts.tmp"), " ")
	for _, want := range []string{
		"-i https://cdn/v1.ts",
		"-protocol_whitelist file -itsoffset 79309.290089 -i /out/segment1.ts.7.audio",
		"-map 0:v:0? -map 1:a:0",
		"-c:a copy",
		"-copyts",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("packed audio mux lacks %q: %s", want, args)
		}
	}
	// The local file is not opened with the network inputs' options.
	if strings.Count(args, "-headers") != 1 {
		t.Errorf("headers passed to the local file too: %s", args)
	}
}

// FFmpeg reports a video segment starting just before the 33-bit clock wraps as negative; the ID3
// timestamp of its audio stays positive. They still line up.
func TestMpegTSTimeApartAcrossTheWrap(t *testing.T) {
	wrap := float64(1<<33) / 90000
	for _, tc := range []struct{ video, audio, want float64 }{
		{79309.290, 79309.294, -0.004},
		{-30, wrap - 30 + 0.02, -0.02},
		{wrap - 30, -30 + 0.02, -0.02},
		{100, 98, 2},
	} {
		if got := mpegTSTimeApart(tc.video, tc.audio); math.Abs(got-tc.want) > 1e-6 {
			t.Errorf("apart(%v, %v) = %v, want %v", tc.video, tc.audio, got, tc.want)
		}
	}
}
