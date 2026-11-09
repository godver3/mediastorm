package handlers

import (
	"testing"
	"time"
)

func TestLiveLayoutWatchFiresOnLateAudioOrVideoStream(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
		want   bool
	}{
		{
			// FFmpeg 7+ at a Pluto TV ad break (captured from a real stitcher stream).
			name:   "ffmpeg7 video",
			chunks: []string{"[in#0/hls @ 0x7b4f014000] New video stream with index 2 at pos:3734056 and DTS:1.4s\n"},
			want:   true,
		},
		{
			name:   "ffmpeg6 audio",
			chunks: []string{"New audio stream 0:3 at pos:1880 and DTS:12.5s\n"},
			want:   true,
		},
		{
			name: "line split across reads",
			chunks: []string{
				"[aist#0:0/aac @ 0x1] timestamp discontinuity (stream id=0): 10005334, new offset= -94795245690\n[in#0/hls @ 0x2] New vid",
				"eo stream with index 2 at pos:3734056 and DTS:1.4s\n",
			},
			want: true,
		},
		{
			name:   "subtitle and data streams are ignored",
			chunks: []string{"[in#0/hls @ 0x1] New subtitle stream with index 4 at pos:1 and DTS:2s\n", "New data stream 0:5 at pos:9 and DTS:3s\n"},
			want:   false,
		},
		{
			name: "ordinary live warnings",
			chunks: []string{
				"mime type is not rfc8216 compliant\n",
				"[mpegts @ 0x1] Packet corrupt (stream = 2, dts = 2823000), dropping it.\n",
				"[vist#0:1/h264 @ 0x1] timestamp discontinuity (stream id=0): -94730115022, new offset= -3\n",
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var w liveLayoutWatch
			got := false
			for _, chunk := range tc.chunks {
				if w.observe(chunk) {
					got = true
				}
			}
			if got != tc.want {
				t.Fatalf("fired = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLiveLayoutWatchFiresOncePerRun(t *testing.T) {
	var w liveLayoutWatch
	line := "[in#0/hls @ 0x1] New video stream with index 2 at pos:1 and DTS:1.4s\n"
	if !w.observe(line) {
		t.Fatal("first announcement did not fire")
	}
	if w.observe(line) {
		t.Fatal("second announcement in the same run fired again")
	}
}

func TestLiveLayoutRestartsCapAndHandOff(t *testing.T) {
	var r liveLayoutRestarts
	if r.take() {
		t.Fatal("take before any claim = true")
	}
	start := time.Now()
	for i := 0; i < liveLayoutRestartLimit; i++ {
		if !r.claim(start.Add(time.Duration(i) * time.Second)) {
			t.Fatalf("claim %d refused under the cap", i)
		}
		if !r.take() {
			t.Fatalf("take after claim %d = false", i)
		}
		if r.take() {
			t.Fatalf("take twice after claim %d = true", i)
		}
	}
	if r.claim(start.Add(time.Minute)) {
		t.Fatal("claim past the cap inside the window was allowed")
	}
	if r.take() {
		t.Fatal("a refused claim requested a restart")
	}
	// Once the earliest restarts age out of the window, layout restarts are allowed again.
	if !r.claim(start.Add(liveRestartWindow + time.Second)) {
		t.Fatal("claim after the window refused")
	}

	var nilRestarts *liveLayoutRestarts
	if nilRestarts.claim(start) || nilRestarts.take() {
		t.Fatal("nil restarts claimed or took")
	}
}
