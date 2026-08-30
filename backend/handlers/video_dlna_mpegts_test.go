package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestParseDLNAStreamProfile(t *testing.T) {
	testCases := []struct {
		name  string
		raw   string
		want  dlnaStreamProfile
		mount transmuxContainer
	}{
		{name: "canonical value", raw: "avc-ts", want: dlnaProfileAVCTS, mount: outputMpegTs},
		{name: "mixed case", raw: "AVC-TS", want: dlnaProfileAVCTS, mount: outputMpegTs},
		{name: "surrounding space", raw: "  avc-ts\t", want: dlnaProfileAVCTS, mount: outputMpegTs},
		{name: "absent", raw: "", want: dlnaProfileNone, mount: outputFmp4},
		{name: "whitespace only", raw: "   ", want: dlnaProfileNone, mount: outputFmp4},
		{name: "unknown value", raw: "avc_ts", want: dlnaProfileNone, mount: outputFmp4},
		{name: "other profile", raw: "mpeg2-ts", want: dlnaProfileNone, mount: outputFmp4},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseDLNAStreamProfile(tc.raw)
			if got != tc.want {
				t.Fatalf("parseDLNAStreamProfile(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			if container := got.container(); container != tc.mount {
				t.Fatalf("container for %q = %v, want %v", tc.raw, container, tc.mount)
			}
		})
	}
}

func TestDLNAStreamProfileFromRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/video/stream?path=a.mkv&dlnaProfile=avc-ts&token=x", nil)
	if got := dlnaStreamProfileFromRequest(req); got != dlnaProfileAVCTS {
		t.Fatalf("profile = %q, want %q", got, dlnaProfileAVCTS)
	}

	plain := httptest.NewRequest(http.MethodGet, "/api/video/stream?path=a.mkv", nil)
	if got := dlnaStreamProfileFromRequest(plain); got != dlnaProfileNone {
		t.Fatalf("profile = %q, want none", got)
	}
}

func TestTransmuxContainerContentType(t *testing.T) {
	if got, want := outputMpegTs.contentType(), "video/mpeg"; got != want {
		t.Fatalf("mpegts content type = %q, want %q", got, want)
	}
	// The zero value must stay fMP4 so existing callers are unaffected.
	var zero transmuxContainer
	if got, want := zero.contentType(), "video/mp4"; got != want {
		t.Fatalf("default content type = %q, want %q", got, want)
	}
}

// The DLNA profile has to force the transcode on for any source container, even
// one that would normally be served directly or with transmux disabled.
func TestShouldTransmuxHonoursDLNAProfile(t *testing.T) {
	testCases := []struct {
		name          string
		transmux      bool
		query         string
		ext           string
		want          bool
		wantOverride  bool
		wantReasonHas string
	}{
		{
			name: "mkv with profile", transmux: true,
			query: "dlnaProfile=avc-ts", ext: ".mkv",
			want: true, wantOverride: true, wantReasonHas: "dlna",
		},
		{
			name: "mp4 with profile", transmux: true,
			query: "dlnaProfile=avc-ts", ext: ".mp4",
			want: true, wantOverride: true, wantReasonHas: "dlna",
		},
		{
			name: "unknown container with profile", transmux: true,
			query: "dlnaProfile=avc-ts", ext: "",
			want: true, wantOverride: true, wantReasonHas: "dlna",
		},
		{
			name: "profile beats manual disable", transmux: true,
			query: "dlnaProfile=avc-ts&transmux=0", ext: ".mkv",
			want: true, wantOverride: true, wantReasonHas: "dlna",
		},
		{
			name: "profile works with transmux disabled globally", transmux: false,
			query: "dlnaProfile=avc-ts", ext: ".mkv",
			want: true, wantOverride: true, wantReasonHas: "dlna",
		},
		{
			name: "unknown profile leaves mp4 direct", transmux: true,
			query: "dlnaProfile=bogus", ext: ".mp4",
			want: false, wantOverride: false, wantReasonHas: "already mp4",
		},
		{
			name: "no profile leaves mp4 direct", transmux: true,
			query: "", ext: ".mp4",
			want: false, wantOverride: false, wantReasonHas: "already mp4",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			h := &VideoHandler{transmux: tc.transmux}
			req := httptest.NewRequest(http.MethodGet, "/api/video/stream?path=movie"+tc.ext+"&"+tc.query, nil)

			should, override, reason := h.shouldTransmux(req, "movie"+tc.ext, tc.ext)
			if should != tc.want {
				t.Fatalf("shouldTransmux = %t, want %t (reason %q)", should, tc.want, reason)
			}
			if override != tc.wantOverride {
				t.Fatalf("override = %t, want %t (reason %q)", override, tc.wantOverride, reason)
			}
			if !strings.Contains(reason, tc.wantReasonHas) {
				t.Fatalf("reason = %q, want it to contain %q", reason, tc.wantReasonHas)
			}
		})
	}
}

func TestBuildMpegTsArgsEmitsTransportStreamOutput(t *testing.T) {
	args := buildMpegTsArgs(mpegTsEncodeInput{
		inputURL:      "pipe:0",
		videoMap:      "0:0",
		audioMap:      "0:1",
		sourceWidth:   1920,
		sourceHeight:  1080,
		audioChannels: 6,
	}, HWAccelCaps{Encode: HWNone})

	joined := strings.Join(args, " ")

	// The 188-byte MPEG-TS pipe is the whole point of this arm.
	if !strings.HasSuffix(joined, "-f mpegts pipe:1") {
		t.Fatalf("args must end with the mpegts pipe output, got %q", joined)
	}
	if !strings.Contains(joined, "-mpegts_flags +resend_headers") {
		t.Fatalf("missing PAT/PMT resend flag: %q", joined)
	}
	// -movflags belongs to the MP4 muxer; FFmpeg complains when another muxer sees it.
	if strings.Contains(joined, "-movflags") {
		t.Fatalf("mpegts args must not carry -movflags: %q", joined)
	}
	if strings.Contains(joined, "-f mp4") {
		t.Fatalf("mpegts args must not select the mp4 muxer: %q", joined)
	}
	// Video must be re-encoded to H.264 High, never copied.
	if strings.Contains(joined, "-c:v copy") {
		t.Fatalf("mpegts args must transcode video, got %q", joined)
	}
	for _, want := range []string{
		"-i pipe:0", "-map 0:0", "-map 0:1", "-sn", "-dn",
		"-c:v libx264", "-profile:v high",
		"-c:a ac3", "-b:a 448k",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %q", want, joined)
		}
	}
	// 5.1 already fits the AC3 encoder, so no downmix should be forced.
	if strings.Contains(joined, "-ac 6") {
		t.Fatalf("5.1 source must not be re-downmixed: %q", joined)
	}
	// Subtitles cannot ride along in a DLNA transport stream.
	if strings.Contains(joined, "mov_text") {
		t.Fatalf("mpegts args must not map subtitles: %q", joined)
	}
}

func TestBuildMpegTsArgsPrefersHardwareEncoder(t *testing.T) {
	testCases := []struct {
		name string
		caps HWAccelCaps
		want string
	}{
		{name: "videotoolbox host", caps: HWAccelCaps{Encode: HWVideoToolbox}, want: "-c:v h264_videotoolbox"},
		{name: "no hardware encoder", caps: HWAccelCaps{Encode: HWNone}, want: "-c:v libx264"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			joined := strings.Join(buildMpegTsArgs(mpegTsEncodeInput{inputURL: "pipe:0"}, tc.caps), " ")
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("args missing %q: %q", tc.want, joined)
			}
			if !strings.Contains(joined, "-profile:v high") {
				t.Fatalf("args missing -profile:v high: %q", joined)
			}
		})
	}
}

func TestBuildMpegTsArgsScaleNeverUpscales(t *testing.T) {
	testCases := []struct {
		name      string
		width     int
		height    int
		wantScale bool
	}{
		{name: "4k source is capped", width: 3840, height: 2160, wantScale: true},
		{name: "ultra wide source is capped", width: 4096, height: 1716, wantScale: true},
		{name: "1080p source is left alone", width: 1920, height: 1080, wantScale: false},
		{name: "720p source is not upscaled", width: 1280, height: 720, wantScale: false},
		{name: "sd source is not upscaled", width: 720, height: 480, wantScale: false},
		// Without a probe the box is applied anyway: it can only shrink.
		{name: "unknown geometry keeps the cap", width: 0, height: 0, wantScale: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			joined := strings.Join(buildMpegTsArgs(mpegTsEncodeInput{
				inputURL:     "pipe:0",
				sourceWidth:  tc.width,
				sourceHeight: tc.height,
			}, HWAccelCaps{Encode: HWNone}), " ")

			hasScale := strings.Contains(joined, "scale=")
			if hasScale != tc.wantScale {
				t.Fatalf("scale filter present = %t, want %t: %q", hasScale, tc.wantScale, joined)
			}
			if !hasScale {
				return
			}
			// min(1080,ih) is what keeps a shorter source from being stretched.
			if !strings.Contains(joined, "h='min(1080,ih)'") || !strings.Contains(joined, "w='min(1920,iw)'") {
				t.Fatalf("scale filter must clamp to the 1080p box without upscaling: %q", joined)
			}
			if !strings.Contains(joined, "force_original_aspect_ratio=decrease") {
				t.Fatalf("scale filter must preserve aspect ratio: %q", joined)
			}
		})
	}
}

func TestBuildMpegTsArgsToneMapsHDROnly(t *testing.T) {
	caps := HWAccelCaps{Encode: HWNone, Tonemap: "zscale", Zscale: true}

	hdr := strings.Join(buildMpegTsArgs(mpegTsEncodeInput{
		inputURL:       "pipe:0",
		sourceWidth:    3840,
		sourceHeight:   2160,
		hdr:            true,
		sourceTransfer: "smpte2084",
	}, caps), " ")
	// The curve itself is the shared encode plan's call, not this path's: assert
	// that an HDR source is tone mapped, not which operator does it.
	if !strings.Contains(hdr, "tonemap=tonemap=") {
		t.Fatalf("HDR source must be tone mapped to SDR: %q", hdr)
	}
	if !strings.Contains(hdr, "color_trc=smpte2084") {
		t.Fatalf("HDR tone map must declare the PQ input transfer: %q", hdr)
	}
	// Scale and tone map have to compose into a single -vf value.
	if got := strings.Count(hdr, "-vf"); got != 1 {
		t.Fatalf("-vf occurrences = %d, want 1: %q", got, hdr)
	}

	sdr := strings.Join(buildMpegTsArgs(mpegTsEncodeInput{
		inputURL:       "pipe:0",
		sourceWidth:    1920,
		sourceHeight:   1080,
		sourceTransfer: "bt709",
	}, caps), " ")
	if strings.Contains(sdr, "tonemap") {
		t.Fatalf("SDR source must not be tone mapped: %q", sdr)
	}

	hlg := strings.Join(buildMpegTsArgs(mpegTsEncodeInput{
		inputURL:       "pipe:0",
		hdr:            true,
		sourceTransfer: "arib-std-b67",
	}, caps), " ")
	if !strings.Contains(hlg, "color_trc=arib-std-b67") {
		t.Fatalf("HLG tone map must keep its own input transfer: %q", hlg)
	}
}

// A build without zscale/libplacebo/tonemap_opencl must still stream: an
// untone-mapped picture beats a refused connection.
func TestBuildMpegTsArgsWithoutToneMapperStillStreams(t *testing.T) {
	joined := strings.Join(buildMpegTsArgs(mpegTsEncodeInput{
		inputURL:       "pipe:0",
		sourceWidth:    3840,
		sourceHeight:   2160,
		hdr:            true,
		sourceTransfer: "smpte2084",
	}, HWAccelCaps{Encode: HWNone, Tonemap: ""}), " ")

	if strings.Contains(joined, "tonemap") {
		t.Fatalf("no tone mapper is available, so none may be requested: %q", joined)
	}
	if !strings.HasSuffix(joined, "-f mpegts pipe:1") {
		t.Fatalf("stream must still be produced: %q", joined)
	}
}

func TestBuildMpegTsArgsAudioHandling(t *testing.T) {
	testCases := []struct {
		name        string
		audioMap    string
		channels    int
		wantDownmix bool
		wantSilent  bool
	}{
		{name: "stereo is kept as is", audioMap: "0:1", channels: 2},
		{name: "5.1 is kept as is", audioMap: "0:1", channels: 6},
		{name: "7.1 is downmixed", audioMap: "0:1", channels: 8, wantDownmix: true},
		{name: "unknown layout is downmixed", audioMap: "0:1", channels: 0, wantDownmix: true},
		{name: "no audio stream", audioMap: "", wantSilent: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			joined := strings.Join(buildMpegTsArgs(mpegTsEncodeInput{
				inputURL:      "pipe:0",
				audioMap:      tc.audioMap,
				audioChannels: tc.channels,
			}, HWAccelCaps{Encode: HWNone}), " ")

			if tc.wantSilent {
				if !strings.Contains(joined, "-an") {
					t.Fatalf("a source without audio must disable audio: %q", joined)
				}
				if strings.Contains(joined, "-c:a ac3") {
					t.Fatalf("no audio stream to encode: %q", joined)
				}
				return
			}
			if !strings.Contains(joined, "-c:a ac3 -b:a 448k") {
				t.Fatalf("audio must be encoded to AC3: %q", joined)
			}
			if got := strings.Contains(joined, "-ac 6"); got != tc.wantDownmix {
				t.Fatalf("downmix present = %t, want %t: %q", got, tc.wantDownmix, joined)
			}
		})
	}
}

func TestBuildMpegTsPlanFromProbe(t *testing.T) {
	h := &VideoHandler{}
	meta := &ffprobeOutput{
		Streams: []ffprobeStream{
			{
				Index:         0,
				CodecType:     "video",
				CodecName:     "hevc",
				Width:         3840,
				Height:        2160,
				ColorTransfer: "smpte2084",
			},
			{Index: 1, CodecType: "audio", CodecName: "truehd", Channels: 8},
			{Index: 2, CodecType: "subtitle", CodecName: "subrip"},
		},
		Format: ffprobeFormat{Duration: "7200.500"},
	}

	plan := h.buildMpegTsPlan(meta, "pipe:0", "", 0)
	joined := strings.Join(plan.args, " ")

	if plan.container != outputMpegTs {
		t.Fatalf("container = %v, want mpegts", plan.container)
	}
	if !plan.usedProbe {
		t.Fatalf("plan must record that the probe was used")
	}
	if plan.duration != 7200.5 {
		t.Fatalf("duration = %v, want 7200.5", plan.duration)
	}
	if plan.audio.mode != audioPlanTranscode {
		t.Fatalf("audio mode = %q, want transcode", plan.audio.mode)
	}
	for _, want := range []string{"-map 0:0", "-map 0:1", "-ac 6", "-f mpegts pipe:1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %q", want, joined)
		}
	}
	if strings.Contains(joined, "-map 0:2") {
		t.Fatalf("subtitle stream must not be mapped: %q", joined)
	}
}

func TestBuildMpegTsPlanWithoutProbe(t *testing.T) {
	h := &VideoHandler{}

	plan := h.buildMpegTsPlan(nil, "pipe:0", "ffprobe failed: boom", 0)
	joined := strings.Join(plan.args, " ")

	if plan.container != outputMpegTs {
		t.Fatalf("container = %v, want mpegts", plan.container)
	}
	if plan.usedProbe {
		t.Fatalf("plan must not claim probe metadata")
	}
	if plan.audio.reason != "ffprobe failed: boom" {
		t.Fatalf("audio reason = %q, want the ffprobe failure", plan.audio.reason)
	}
	// Unknown geometry and layout: cap the frame and downmix, since neither can
	// be verified from the source.
	for _, want := range []string{"-map 0:v:0", "-map 0:a:0?", "h='min(1080,ih)'", "-ac 6", "-f mpegts pipe:1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %q", want, joined)
		}
	}
}

func TestSourceNeedsToneMapping(t *testing.T) {
	testCases := []struct {
		name         string
		stream       *ffprobeStream
		want         bool
		wantTransfer string
	}{
		{name: "nil stream", stream: nil},
		{
			name:   "sdr h264",
			stream: &ffprobeStream{CodecName: "h264", ColorTransfer: "bt709"},
			want:   false, wantTransfer: "bt709",
		},
		{
			name:   "hdr10 hevc",
			stream: &ffprobeStream{CodecName: "hevc", ColorTransfer: "smpte2084"},
			want:   true, wantTransfer: "smpte2084",
		},
		{
			name:   "hlg hevc",
			stream: &ffprobeStream{CodecName: "hevc", ColorTransfer: "arib-std-b67"},
			want:   true, wantTransfer: "arib-std-b67",
		},
		{
			// detectDolbyVision only inspects HEVC, so the raw transfer has to
			// catch HDR10 in other codecs.
			name:   "hdr10 av1",
			stream: &ffprobeStream{CodecName: "av1", ColorTransfer: "smpte2084"},
			want:   true, wantTransfer: "smpte2084",
		},
		{
			name: "dolby vision side data",
			stream: &ffprobeStream{
				CodecName:    "hevc",
				SideDataList: []ffprobeSideData{{SideDataType: "DOVI configuration record", DVProfile: 5, DVLevel: 6}},
			},
			want: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, transfer := sourceNeedsToneMapping(tc.stream)
			if got != tc.want {
				t.Fatalf("sourceNeedsToneMapping = %t, want %t", got, tc.want)
			}
			if transfer != tc.wantTransfer {
				t.Fatalf("transfer = %q, want %q", transfer, tc.wantTransfer)
			}
		})
	}
}

// fakeFFmpegHandler builds a video handler whose "ffmpeg" copies stdin to
// stdout, so the response body is the provider payload and the surrounding
// header/plumbing behaviour is what gets asserted.
func fakeFFmpegHandler(t *testing.T, payload []byte) *VideoHandler {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake ffmpeg shell script is POSIX-only")
	}

	scriptPath := filepath.Join(t.TempDir(), "fake-ffmpeg.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexec cat\n"), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	// An unresolvable ffprobe keeps the plan on its no-metadata path.
	return NewVideoHandlerWithProvider(true, scriptPath, filepath.Join(t.TempDir(), "absent-ffprobe"), t.TempDir(), &mockProvider{data: payload})
}

func TestStreamVideoDLNAProfileServesMpegTs(t *testing.T) {
	payload := []byte("TRANSPORT-STREAM-PAYLOAD")
	h := fakeFFmpegHandler(t, payload)

	req := httptest.NewRequest(http.MethodGet, "/api/video/stream?path=movies/title.mkv&dlnaProfile=avc-ts", nil)
	req.Header.Set("transferMode.dlna.org", "Streaming")
	rec := httptest.NewRecorder()

	h.StreamVideo(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", res.StatusCode, rec.Body.String())
	}
	if got, want := res.Header.Get("Content-Type"), "video/mpeg"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	if got, want := res.Header.Get("Accept-Ranges"), "none"; got != want {
		t.Fatalf("Accept-Ranges = %q, want %q", got, want)
	}
	if got := res.Header["transferMode.dlna.org"]; len(got) != 1 || got[0] != "Streaming" {
		t.Fatalf("transferMode.dlna.org = %v, want [Streaming]", got)
	}
	// A DLNA 1.0 renderer that receives no contentFeatures cannot confirm the
	// profile and rejects SetAVTransportURI with UPnP 714 "Illegal MIME-type"
	// (observed on a Sony BRAVIA KDL-46NX700 before this header was sent).
	features := res.Header["contentFeatures.dlna.org"]
	if len(features) != 1 {
		t.Fatalf("contentFeatures.dlna.org = %v, want exactly one value", features)
	}
	for _, want := range []string{"DLNA.ORG_PN=AVC_TS_HD_24_AC3_ISO", "DLNA.ORG_CI=1"} {
		if !strings.Contains(features[0], want) {
			t.Fatalf("contentFeatures.dlna.org = %q, want it to contain %q", features[0], want)
		}
	}
	// The advertised operation must agree with Accept-Ranges: none, or the
	// renderer is promised byte seeking this arm cannot perform.
	if !strings.Contains(features[0], "DLNA.ORG_OP=00") {
		t.Fatalf("contentFeatures.dlna.org = %q, want DLNA.ORG_OP=00 to match Accept-Ranges: none", features[0])
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != string(payload) {
		t.Fatalf("body = %q, want %q", body, payload)
	}
}

// Legacy renderers open the stream with "Range: bytes=0-" and then play the
// whole 200 response; a 206 or an empty body kills playback.
func TestStreamVideoDLNAProfileIgnoresRange(t *testing.T) {
	payload := []byte("TRANSPORT-STREAM-PAYLOAD")
	h := fakeFFmpegHandler(t, payload)

	req := httptest.NewRequest(http.MethodGet, "/api/video/stream?path=movies/title.mkv&dlnaProfile=avc-ts", nil)
	req.Header.Set("Range", "bytes=0-")
	rec := httptest.NewRecorder()

	h.StreamVideo(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("Content-Range"); got != "" {
		t.Fatalf("Content-Range = %q, want none", got)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != string(payload) {
		t.Fatalf("body = %q, want the full stream %q", body, payload)
	}
}

// The renderer sends two HEADs before it opens the stream.
func TestStreamVideoDLNAProfileHeadAnnouncesMpegTs(t *testing.T) {
	h := fakeFFmpegHandler(t, []byte("payload"))

	req := httptest.NewRequest(http.MethodHead, "/api/video/stream?path=movies/title.mkv&dlnaProfile=avc-ts", nil)
	rec := httptest.NewRecorder()

	h.StreamVideo(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got, want := res.Header.Get("Content-Type"), "video/mpeg"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	if got, want := res.Header.Get("Accept-Ranges"), "none"; got != want {
		t.Fatalf("Accept-Ranges = %q, want %q", got, want)
	}
}

// Without the profile the transmux path must keep answering fMP4.
func TestStreamVideoWithoutDLNAProfileKeepsMp4(t *testing.T) {
	h := fakeFFmpegHandler(t, []byte("payload"))

	for _, method := range []string{http.MethodHead, http.MethodGet} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/video/stream?path=movies/title.mkv", nil)
			rec := httptest.NewRecorder()

			h.StreamVideo(rec, req)

			res := rec.Result()
			defer res.Body.Close()

			if res.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", res.StatusCode)
			}
			if got, want := res.Header.Get("Content-Type"), "video/mp4"; got != want {
				t.Fatalf("Content-Type = %q, want %q", got, want)
			}
			if got := res.Header["transferMode.dlna.org"]; len(got) != 0 {
				t.Fatalf("fMP4 responses must not echo DLNA transfer mode, got %v", got)
			}
		})
	}
}

// End-to-end proof against the real encoder: the response body has to be a
// transport stream carrying H.264 video and AC3 audio, which is the only
// combination the target renderers decode.
func TestStreamVideoDLNAProfileProducesPlayableMpegTs(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}

	// A tiny HEVC/AC3 Matroska source: none of it is playable by the renderers
	// this profile targets, so everything has to be re-encoded.
	sourcePath := filepath.Join(t.TempDir(), "source.mkv")
	build := exec.Command(ffmpegPath, "-nostdin", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=640x360:rate=24:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", sourcePath)
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("could not build the test source: %v (%s)", err, out)
	}
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read test source: %v", err)
	}

	h := NewVideoHandlerWithProvider(true, ffmpegPath, ffprobePath, t.TempDir(), &mockProvider{data: source})

	req := httptest.NewRequest(http.MethodGet, "/api/video/stream?path=movies/title.mkv&dlnaProfile=avc-ts", nil)
	req.Header.Set("Range", "bytes=0-")
	rec := httptest.NewRecorder()

	h.StreamVideo(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got, want := res.Header.Get("Content-Type"), "video/mpeg"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("empty response body")
	}
	// 0x47 is the MPEG-TS sync byte; 188 is the packet size the renderers expect.
	if body[0] != 0x47 {
		t.Fatalf("first byte = %#x, want the 0x47 transport stream sync byte", body[0])
	}
	if len(body)%188 != 0 {
		t.Fatalf("body length %d is not a multiple of 188 bytes", len(body))
	}

	outPath := filepath.Join(t.TempDir(), "out.ts")
	if err := os.WriteFile(outPath, body, 0o644); err != nil {
		t.Fatalf("write response: %v", err)
	}
	probe := exec.Command(ffprobePath, "-v", "error",
		"-show_entries", "format=format_name:stream=codec_name",
		"-of", "default=noprint_wrappers=1:nokey=1", outPath)
	out, err := probe.CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe response: %v (%s)", err, out)
	}
	report := string(out)
	for _, want := range []string{"mpegts", "h264", "ac3"} {
		if !strings.Contains(report, want) {
			t.Fatalf("ffprobe report %q missing %q", report, want)
		}
	}
}

// End-to-end proof against the real encoder that the offset cuts the stream
// rather than being carried and ignored: only the remainder of the item is
// muxed, and it is a transport stream the renderer still accepts.
func TestStreamVideoDLNAProfileStreamsOnlyTheRemainder(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}

	sourcePath := filepath.Join(t.TempDir(), "source.mkv")
	build := exec.Command(ffmpegPath, "-nostdin", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=24:duration=8",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=8",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "24", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", sourcePath)
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("could not build the test source: %v (%s)", err, out)
	}
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read test source: %v", err)
	}

	h := NewVideoHandlerWithProvider(true, ffmpegPath, ffprobePath, t.TempDir(), &mockProvider{data: source})

	req := httptest.NewRequest(http.MethodGet, "/api/video/stream?path=movies/title.mkv&dlnaProfile=avc-ts&start=6", nil)
	rec := httptest.NewRecorder()

	h.StreamVideo(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("empty response body")
	}
	if body[0] != 0x47 {
		t.Fatalf("first byte = %#x, want the 0x47 transport stream sync byte", body[0])
	}

	outPath := filepath.Join(t.TempDir(), "out.ts")
	if err := os.WriteFile(outPath, body, 0o644); err != nil {
		t.Fatalf("write response: %v", err)
	}
	probe := exec.Command(ffprobePath, "-v", "error",
		"-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", outPath)
	out, err := probe.CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe response: %v (%s)", err, out)
	}
	served, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		t.Fatalf("parse served duration %q: %v", out, err)
	}
	// Six seconds into an eight second item leaves two, give or take the
	// keyframe the input seek lands on and the muxer's trailing packets.
	if served > 3.5 {
		t.Fatalf("served %.3fs of an 8s item from a 6s offset, want only the remainder", served)
	}
	if served < 0.5 {
		t.Fatalf("served %.3fs, want the remainder of the item rather than nothing", served)
	}
}

func TestParseStartOffsetParam(t *testing.T) {
	testCases := []struct {
		name  string
		query string
		want  float64
	}{
		{name: "absent", query: "", want: 0},
		{name: "empty", query: "?start=", want: 0},
		{name: "legacy name", query: "?start=900", want: 900},
		{name: "fractional", query: "?start=900.25", want: 900.25},
		{name: "frontend name wins", query: "?startOffset=120&start=900", want: 120},
		{name: "negative", query: "?start=-30", want: 0},
		{name: "not a number", query: "?start=soon", want: 0},
		{name: "not a number (nan)", query: "?start=NaN", want: 0},
		{name: "not finite", query: "?start=Inf", want: 0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/video/stream"+tc.query, nil)
			if got := parseStartOffsetParam(req); got != tc.want {
				t.Fatalf("parseStartOffsetParam() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Without a keyframe probe available the requested second stands, so this
// covers the validation on its own.
func TestResolveMpegTsStartOffsetValidation(t *testing.T) {
	h := &VideoHandler{}
	testCases := []struct {
		name      string
		requested float64
		duration  float64
		want      float64
	}{
		{name: "absent", requested: 0, duration: 3600, want: 0},
		{name: "negative", requested: -30, duration: 3600, want: 0},
		{name: "inside the item", requested: 900, duration: 3600, want: 900},
		{name: "duration unknown", requested: 900, duration: 0, want: 900},
		{name: "at the very end", requested: 3600, duration: 3600, want: 3596},
		{name: "past the end", requested: 99999, duration: 3600, want: 3596},
		{name: "item shorter than the clamp", requested: 10, duration: 3, want: 0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := h.resolveMpegTsStartOffset(context.Background(), "movies/title.mkv", tc.requested, tc.duration)
			if got != tc.want {
				t.Fatalf("resolveMpegTsStartOffset(%v, %v) = %v, want %v", tc.requested, tc.duration, got, tc.want)
			}
		})
	}
}

// argIndex reports where a flag sits in an ffmpeg argument vector, so the tests
// can assert on ordering rather than on a substring.
func argIndex(args []string, flag string) int {
	for i, arg := range args {
		if arg == flag {
			return i
		}
	}
	return -1
}

// A transport stream has no byte offsets, so the only way to start mid-item is
// to seek the input before the transcode.
func TestBuildMpegTsArgsSeeksInputToStartOffset(t *testing.T) {
	base := mpegTsEncodeInput{
		inputURL:      "pipe:0",
		videoMap:      "0:0",
		audioMap:      "0:1",
		sourceWidth:   1920,
		sourceHeight:  1080,
		audioChannels: 6,
	}
	caps := HWAccelCaps{Encode: HWNone}

	fromZero := buildMpegTsArgs(base, caps)
	if argIndex(fromZero, "-ss") >= 0 {
		t.Fatalf("a zero offset must not seek: %q", strings.Join(fromZero, " "))
	}

	seeking := base
	seeking.startOffset = 900
	args := buildMpegTsArgs(seeking, caps)

	ssIdx := argIndex(args, "-ss")
	if ssIdx < 0 {
		t.Fatalf("offset never reached the transcode args: %q", strings.Join(args, " "))
	}
	if got := args[ssIdx+1]; got != "900.000" {
		t.Fatalf("-ss = %q, want 900.000", got)
	}
	// After -i the demuxer has already read the offset past; the seek only skips
	// work when it precedes the input.
	if inputIdx := argIndex(args, "-i"); ssIdx > inputIdx {
		t.Fatalf("-ss must precede -i, got %q", strings.Join(args, " "))
	}

	// The offset is the only difference: drop the seek and the invocation is the
	// one a start-at-zero request produces.
	withoutSeek := append(append([]string{}, args[:ssIdx]...), args[ssIdx+2:]...)
	if strings.Join(withoutSeek, " ") != strings.Join(fromZero, " ") {
		t.Fatalf("offset changed more than the seek:\n got %q\nwant %q", withoutSeek, fromZero)
	}
}

// A stream cut at an offset carries only what is left of the item, and the
// duration it advertises has to say so.
func TestBuildMpegTsPlanReportsRemainderFromStartOffset(t *testing.T) {
	h := &VideoHandler{}
	meta := &ffprobeOutput{
		Streams: []ffprobeStream{
			{Index: 0, CodecType: "video", CodecName: "h264", Width: 1920, Height: 1080},
			{Index: 1, CodecType: "audio", CodecName: "ac3", Channels: 6},
		},
		Format: ffprobeFormat{Duration: "3600.000"},
	}

	plan := h.buildMpegTsPlan(meta, "pipe:0", "", 900)

	if plan.startOffset != 900 {
		t.Fatalf("startOffset = %v, want 900", plan.startOffset)
	}
	if plan.duration != 2700 {
		t.Fatalf("duration = %v, want the 2700s remainder", plan.duration)
	}
	if joined := strings.Join(plan.args, " "); !strings.Contains(joined, "-ss 900.000 -i pipe:0") {
		t.Fatalf("plan must seek the input: %q", joined)
	}
}

// argvCapturingFFmpegHandler is fakeFFmpegHandler with a script that records the
// argument vector it was invoked with before copying stdin to stdout.
func argvCapturingFFmpegHandler(t *testing.T, payload []byte) (*VideoHandler, func() []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake ffmpeg shell script is POSIX-only")
	}

	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv")
	scriptPath := filepath.Join(dir, "fake-ffmpeg.sh")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argvPath + "\nexec cat\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}

	// An unresolvable ffprobe keeps the plan on its no-metadata path, which also
	// leaves the keyframe probe out of the way: the requested second stands.
	h := NewVideoHandlerWithProvider(true, scriptPath, filepath.Join(dir, "absent-ffprobe"), dir, &mockProvider{data: payload})
	return h, func() []string {
		t.Helper()
		recorded, err := os.ReadFile(argvPath)
		if err != nil {
			t.Fatalf("read recorded ffmpeg argv: %v", err)
		}
		return strings.Split(strings.TrimSuffix(string(recorded), "\n"), "\n")
	}
}

// Resuming a cast has to move the stream, because this renderer cannot be asked
// to jump: the offset on the URL must reach FFmpeg as an input seek.
func TestStreamVideoDLNAProfileSeeksMuxToStartOffset(t *testing.T) {
	h, recordedArgv := argvCapturingFFmpegHandler(t, []byte("TRANSPORT-STREAM-PAYLOAD"))

	req := httptest.NewRequest(http.MethodGet, "/api/video/stream?path=movies/title.mkv&dlnaProfile=avc-ts&start=900", nil)
	rec := httptest.NewRecorder()

	h.StreamVideo(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", res.StatusCode, rec.Body.String())
	}
	if got, want := res.Header.Get("X-Start-Offset"), "900.000"; got != want {
		t.Fatalf("X-Start-Offset = %q, want %q", got, want)
	}
	// A stream cut at an offset is still a live mux with no byte offsets, so
	// nothing the rung promises about seeking may change.
	if got, want := res.Header.Get("Accept-Ranges"), "none"; got != want {
		t.Fatalf("Accept-Ranges = %q, want %q", got, want)
	}
	features := res.Header["contentFeatures.dlna.org"]
	if len(features) != 1 {
		t.Fatalf("contentFeatures.dlna.org = %v, want exactly one value", features)
	}
	for _, want := range []string{"DLNA.ORG_OP=00", "DLNA.ORG_CI=1"} {
		if !strings.Contains(features[0], want) {
			t.Fatalf("contentFeatures.dlna.org = %q, want it to contain %q", features[0], want)
		}
	}

	argv := recordedArgv()
	ssIdx := argIndex(argv, "-ss")
	if ssIdx < 0 {
		t.Fatalf("offset never reached ffmpeg: %q", strings.Join(argv, " "))
	}
	if got := argv[ssIdx+1]; got != "900.000" {
		t.Fatalf("ffmpeg -ss = %q, want 900.000", got)
	}
	if inputIdx := argIndex(argv, "-i"); ssIdx > inputIdx {
		t.Fatalf("-ss must precede -i, got %q", strings.Join(argv, " "))
	}
}

// Every stream that starts at the beginning must invoke FFmpeg exactly as it did
// before offsets existed.
func TestStreamVideoDLNAProfileWithoutStartOffsetIsUnchanged(t *testing.T) {
	h, recordedArgv := argvCapturingFFmpegHandler(t, []byte("TRANSPORT-STREAM-PAYLOAD"))

	req := httptest.NewRequest(http.MethodGet, "/api/video/stream?path=movies/title.mkv&dlnaProfile=avc-ts", nil)
	rec := httptest.NewRecorder()

	h.StreamVideo(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", res.StatusCode, rec.Body.String())
	}
	if got := res.Header.Get("X-Start-Offset"); got != "" {
		t.Fatalf("X-Start-Offset = %q, want it absent", got)
	}

	argv := recordedArgv()
	if argIndex(argv, "-ss") >= 0 {
		t.Fatalf("an absent start parameter must not seek: %q", strings.Join(argv, " "))
	}
	want := h.buildMpegTsPlan(nil, "pipe:0", "", 0).args
	if strings.Join(argv, " ") != strings.Join(want, " ") {
		t.Fatalf("ffmpeg argv drifted from the no-offset plan:\n got %q\nwant %q", argv, want)
	}
}
