package handlers

import (
	"bytes"
	"io"
	"time"
)

// An HDHomeRun's ATSC 1.0 channels carry MPEG-2 video. Native live sessions copy
// video into HLS, and AVPlayer (tvOS multiscreen tiles, the HLS fallback) cannot
// play MPEG-2 in HLS. A session the client explicitly asked to be HLS
// (LiveTuningSettings.EncodeMPEG2Video) therefore peeks the tuner's PMT before
// FFmpeg starts and, when the video is MPEG-2, encodes it to H.264 and the AC-3
// audio to stereo AAC: AVPlayer refuses AC-3 where no Dolby decoder is present
// (the tvOS simulator never readied a tile with it), and a multiscreen tile
// plays at most one stereo track anyway. H.264/HEVC tuners (ATSC 3.0) keep the
// copy.

const (
	liveCodecPeekLimit   = 2 << 20
	liveCodecPeekTimeout = 3 * time.Second
)

// peekLiveTSVideoCodec reads body until its PMT names a video codec, the limit,
// the timeout, or an error, and returns the codec ("" when unknown) with a
// reader that replays the peeked bytes before the rest of body.
func peekLiveTSVideoCodec(body io.ReadCloser, limit int, timeout time.Duration) (string, io.ReadCloser) {
	inspector := newTSInspector("codec-peek")
	inspector.logf = func(string, ...any) {}
	var peeked bytes.Buffer
	buf := make([]byte, 64<<10)
	deadline := time.Now().Add(timeout)
	codec := ""
	for codec == "" && peeked.Len() < limit && time.Now().Before(deadline) {
		n, err := body.Read(buf)
		if n > 0 {
			peeked.Write(buf[:n])
			_, _ = inspector.Write(buf[:n])
			codec = inspector.videoCodec()
		}
		if err != nil {
			break
		}
	}
	return codec, struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(peeked.Bytes()), body), body}
}

// videoCodec is the first video stream's codec in the current PMT, or "".
func (t *tsInspector) videoCodec() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, stream := range t.streams {
		if stream.Kind == "video" {
			return stream.Codec
		}
	}
	return ""
}

// liveVideoEncodeArgs swaps a native output's copies for an H.264 encode
// (deinterlacing ATSC's 1080i, a keyframe every hls_time so segments match the
// target duration) and the timeshift path's stereo AAC.
func liveVideoEncodeArgs(args []string) []string {
	out := make([]string, 0, len(args)+20)
	for i := 0; i < len(args); i++ {
		if args[i] == "-c:v" && i+1 < len(args) && args[i+1] == "copy" {
			out = append(out,
				"-vf", "bwdif=mode=send_frame:deint=interlaced",
				"-c:v", "libx264",
				"-preset", "veryfast",
				"-tune", "zerolatency",
				"-profile:v", "high",
				"-pix_fmt", "yuv420p",
				"-crf", "23",
				"-force_key_frames", "expr:gte(t,n_forced*2)",
				"-sc_threshold", "0",
			)
			i++
			continue
		}
		if args[i] == "-c:a" && i+1 < len(args) && args[i+1] == "copy" {
			out = append(out, timeshiftAudioArgs...)
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}
