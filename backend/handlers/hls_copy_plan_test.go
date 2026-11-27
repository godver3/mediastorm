package handlers

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"novastream/models"
)

// Capability sets as the Jellyfin bridge derives them from jellyfin-web.
var (
	chromeCaps = &HLSClientCaps{ // Chrome desktop, HEVC decoder present, HDR display
		TSVideo: []string{"h264"}, TSAudio: []string{"aac", "mp3"},
		FMP4Video: []string{"hevc", "h264", "av1"}, FMP4Audio: []string{"aac", "mp3", "opus", "flac"},
		VideoRanges: map[string][]string{"hevc": {"SDR", "HDR10", "HDR10Plus", "HLG"}, "av1": {"SDR", "HDR10", "HDR10Plus", "HLG"}, "h264": {"SDR"}},
	}
	safariCaps = &HLSClientCaps{
		TSVideo: []string{"h264"}, TSAudio: []string{"aac", "mp3", "ac3", "eac3"},
		FMP4Video: []string{"hevc", "h264"}, FMP4Audio: []string{"aac", "mp3", "ac3", "eac3"},
		VideoRanges: map[string][]string{"hevc": {"SDR", "HDR10", "HLG", "DOVI", "DOVIWithHDR10", "DOVIWithHLG", "DOVIWithSDR"}},
	}
	tizenCaps = &HLSClientCaps{ // TV browser: HEVC in MPEG-TS, no fMP4 profile
		TSVideo: []string{"h264", "hevc"}, TSAudio: []string{"aac", "ac3", "eac3"},
		VideoRanges: map[string][]string{"hevc": {"SDR", "HDR10", "HLG"}},
	}
	sdrOnlyCaps = &HLSClientCaps{
		TSVideo: []string{"h264"}, FMP4Video: []string{"hevc", "h264"},
	}
)

func hevcProbe(transfer string) *UnifiedProbeResult {
	return &UnifiedProbeResult{VideoCodec: "hevc", VideoPixFmt: "yuv420p10le", VideoProfile: "Main 10", ColorTransfer: transfer}
}

func TestPlanStableCopyMatrix(t *testing.T) {
	h264 := &UnifiedProbeResult{VideoCodec: "h264", VideoPixFmt: "yuv420p", VideoProfile: "High"}
	dv8 := hevcProbe("smpte2084")
	dv8.DolbyVisionConfiguration = &models.DolbyVisionConfiguration{Profile: 8, BLSignalCompatibilityID: 1}
	dv5 := hevcProbe("")
	dv5.DolbyVisionConfiguration = &models.DolbyVisionConfiguration{Profile: 5}

	type want struct {
		ok    bool
		fmp4  bool
		tag   string
		dv    string
		fix   string
		rng   string
		input string
	}
	cases := []struct {
		name      string
		probe     *UnifiedProbeResult
		dv        bool
		dvProfile string
		hdr       bool
		caps      *HLSClientCaps
		want      want
	}{
		{"h264 without caps", h264, false, "", false, nil, want{ok: true, rng: "SDR", input: "SDR"}},
		{"hevc without caps", hevcProbe(""), false, "", false, nil, want{input: "SDR"}},
		{"h264 chrome stays TS", h264, false, "", false, chromeCaps, want{ok: true, rng: "SDR", input: "SDR"}},
		{"hevc sdr chrome", hevcProbe(""), false, "", false, chromeCaps, want{ok: true, fmp4: true, tag: "hvc1", rng: "SDR", input: "SDR"}},
		{"hevc hdr10 chrome", hevcProbe("smpte2084"), false, "", true, chromeCaps, want{ok: true, fmp4: true, tag: "hvc1", fix: "pq", rng: "PQ", input: "HDR10"}},
		{"hevc hlg chrome", hevcProbe("arib-std-b67"), false, "", false, chromeCaps, want{ok: true, fmp4: true, tag: "hvc1", fix: "hlg", rng: "HLG", input: "HLG"}},
		{"hevc hdr10 sdr-only client", hevcProbe("smpte2084"), false, "", true, sdrOnlyCaps, want{input: "HDR10"}},
		{"dv8 chrome as hdr10", dv8, true, "dvhe.08.06", true, chromeCaps, want{ok: true, fmp4: true, tag: "hvc1", dv: dvStrip, fix: "pq", rng: "PQ", input: "DOVIWithHDR10"}},
		{"dv8 safari keeps dv", dv8, true, "dvhe.08.06", true, safariCaps, want{ok: true, fmp4: true, tag: "dvh1", dv: dvKeep, rng: "PQ", input: "DOVIWithHDR10"}},
		{"dv5 chrome transcodes", dv5, true, "dvhe.05.06", false, chromeCaps, want{input: "DOVI"}},
		{"dv5 safari keeps dv", dv5, true, "dvhe.05.06", false, safariCaps, want{ok: true, fmp4: true, tag: "dvh1", dv: dvKeep, rng: "PQ", input: "DOVI"}},
		{"dv7 transcodes", hevcProbe("smpte2084"), true, "dvhe.07.06", true, safariCaps, want{input: "DOVIWithEL"}},
		{"hevc hdr10 tizen TS", hevcProbe("smpte2084"), false, "", true, tizenCaps, want{ok: true, fix: "pq", rng: "PQ", input: "HDR10"}},
		{"dv5 tizen without dv", dv5, true, "dvhe.05.06", false, tizenCaps, want{input: "DOVI"}},
		{"no copy after failure", h264, false, "", false, &HLSClientCaps{NoVideoCopy: true}, want{input: "SDR"}},
	}
	for _, tc := range cases {
		plan, ok := planStableCopy(tc.probe, tc.dv, tc.dvProfile, tc.hdr, tc.caps)
		got := want{ok: ok, fmp4: plan.FMP4, tag: plan.Tag, dv: plan.DV, fix: plan.ColorFix, rng: plan.OutputRange, input: plan.RangeType}
		if !ok {
			got = want{input: plan.RangeType}
		}
		if got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestStableCopyVideoArgs(t *testing.T) {
	strip := stableCopyPlan{Codec: "hevc", FMP4: true, Tag: "hvc1", DV: dvStrip, ColorFix: "pq"}
	args := strings.Join(strip.videoCopyArgs(false, false), " ")
	if !strings.Contains(args, "-c:v copy") || !strings.Contains(args, "dovi_rpu=strip=1,hevc_metadata=colour_primaries=9:transfer_characteristics=16") || !strings.Contains(args, "-tag:v hvc1") || strings.Contains(args, "unofficial") {
		t.Fatalf("strip args = %s", args)
	}
	keep := stableCopyPlan{Codec: "hevc", FMP4: true, Tag: "dvh1", DV: dvKeep}
	if args := strings.Join(keep.videoCopyArgs(false, false), " "); !strings.Contains(args, "-tag:v dvh1") || !strings.Contains(args, "-strict unofficial") || strings.Contains(args, "-bsf") {
		t.Fatalf("keep args = %s", args)
	}
	if args := (stableCopyPlan{Codec: "h264"}).videoCopyArgs(false, false); !reflect.DeepEqual(args, []string{"-c:v", "copy"}) {
		t.Fatalf("ts h264 args = %v", args)
	}
	if args := strings.Join(strip.videoCopyArgs(true, true), " "); strings.Contains(args, "-bsf") {
		t.Fatalf("failed filters still used: %s", args)
	}
}

func TestClientCapsQueryAndAudio(t *testing.T) {
	caps := parseHLSClientCaps(`{"tsVideo":["H264"],"fmp4Audio":["aac","EAC3"],"videoRanges":{"HEVC":["SDR","HDR10"]}}`)
	if caps == nil || caps.TSVideo[0] != "h264" || caps.FMP4Audio[1] != "eac3" || len(caps.VideoRanges["hevc"]) != 2 {
		t.Fatalf("caps = %+v", caps)
	}
	if parseHLSClientCaps("{nope") != nil || parseHLSClientCaps("") != nil {
		t.Fatal("invalid caps parsed")
	}
	if got := hlsClientCapsFrom(withHLSClientCaps(context.Background(), caps)); got != caps {
		t.Fatal("context round trip")
	}
	session := &HLSSession{ClientCaps: caps, CopyPlan: stableCopyPlan{FMP4: true}}
	if !session.passesAudio("eac3") || session.passesAudio("dts") {
		t.Fatal("fMP4 audio passthrough")
	}
	session.CopyPlan.FMP4 = false
	if session.passesAudio("eac3") {
		t.Fatal("TS audio passthrough uses the fMP4 list")
	}
}

func TestDolbyVisionKeepOnlyForHEVC(t *testing.T) {
	av1 := &UnifiedProbeResult{VideoCodec: "av1", VideoPixFmt: "yuv420p10le", ColorTransfer: "smpte2084",
		DolbyVisionConfiguration: &models.DolbyVisionConfiguration{Profile: 10, BLSignalCompatibilityID: 1}}
	caps := &HLSClientCaps{FMP4Video: []string{"av1"}, VideoRanges: map[string][]string{"av1": {"SDR", "HDR10", "DOVIWithHDR10"}}}
	plan, ok := planStableCopy(av1, true, "dav1.10.06", true, caps)
	if !ok || plan.DV != dvStrip || plan.Tag != "av01" || plan.ColorFix != "" || plan.OutputRange != "PQ" {
		t.Fatalf("AV1 Dolby Vision plan = %+v ok=%v", plan, ok)
	}
}

func TestForcedCopyWhenTranscodingDisabled(t *testing.T) {
	force := &HLSClientCaps{ForceVideoCopy: true, ForceAudioCopy: true, TSVideo: []string{"h264"}, FMP4Video: []string{"h264"}}
	// HEVC HDR10 for a client that declared neither: copied anyway, fMP4 not
	// declared for HEVC, so MPEG-TS; colour signalling kept.
	plan, ok := planStableCopy(hevcProbe("smpte2084"), false, "", true, force)
	if !ok || plan.FMP4 || plan.OutputRange != "PQ" || plan.ColorFix != "pq" {
		t.Fatalf("forced HEVC plan = %+v ok=%v", plan, ok)
	}
	// 4:4:4 and DV profile 5 are copied too.
	dv5 := hevcProbe("")
	dv5.DolbyVisionConfiguration = &models.DolbyVisionConfiguration{Profile: 5}
	if plan, ok := planStableCopy(dv5, true, "dvhe.05.06", false, force); !ok || !plan.FMP4 || plan.DV != dvKeep || plan.Tag != "dvh1" {
		t.Fatalf("forced DV5 plan = %+v ok=%v", plan, ok)
	}
	if _, ok := planStableCopy(&UnifiedProbeResult{VideoCodec: "hevc", VideoPixFmt: "yuv444p"}, false, "", false, force); !ok {
		t.Fatal("forced copy refused 4:4:4")
	}
	// A failure record cannot switch copying off.
	session := &HLSSession{ClientCaps: force}
	if !session.audioTranscodingDisabled() || !session.passesAudio("truehd") {
		t.Fatal("forced session would transcode audio")
	}
	if (&HLSSession{}).audioTranscodingDisabled() {
		t.Fatal("transcoding disabled without caps")
	}
}

// A stable copy session converts AAC only beyond the channels the client declared: the web
// player declares two, so a broadcast's stereo AAC is copied and 5.1 AAC becomes stereo.
func TestStableCopyAudioNeedsAAC(t *testing.T) {
	web := &HLSSession{ClientCaps: &HLSClientCaps{TSAudio: []string{"aac", "mp3"}, MaxAudioChannels: 2, WebFallback: true}}
	for _, tc := range []struct {
		stream audioStreamInfo
		want   bool
	}{
		{audioStreamInfo{Codec: "aac", Channels: 2}, false},
		{audioStreamInfo{Codec: "aac", Channels: 1}, false},
		{audioStreamInfo{Codec: "aac", Channels: 6}, true},
		{audioStreamInfo{Codec: "aac"}, true}, // unknown count with a limit
		{audioStreamInfo{Codec: "mp3", Channels: 2}, false},
		{audioStreamInfo{Codec: "ac3", Channels: 6}, true},
	} {
		if got := stableCopyAudioNeedsAAC(tc.stream, web); got != tc.want {
			t.Errorf("web %+v: %v, want %v", tc.stream, got, tc.want)
		}
	}
	// No declared limit (as before): AAC is copied whatever its channels.
	noLimit := &HLSSession{ClientCaps: &HLSClientCaps{TSAudio: []string{"aac"}}}
	if stableCopyAudioNeedsAAC(audioStreamInfo{Codec: "aac", Channels: 6}, noLimit) {
		t.Error("AAC converted without a declared channel limit")
	}
	// Other clients (the Jellyfin bridge) keep AAC copied whatever channels they declared.
	bridge := &HLSSession{ClientCaps: &HLSClientCaps{TSAudio: []string{"aac"}, MaxAudioChannels: 2}}
	if stableCopyAudioNeedsAAC(audioStreamInfo{Codec: "aac", Channels: 6}, bridge) || stableCopyAudioNeedsAAC(audioStreamInfo{Codec: "aac"}, bridge) {
		t.Error("AAC converted for a non-web-player client")
	}
	// Audio transcoding disabled keeps everything.
	forced := &HLSSession{ClientCaps: &HLSClientCaps{ForceAudioCopy: true, MaxAudioChannels: 2}}
	if stableCopyAudioNeedsAAC(audioStreamInfo{Codec: "aac", Channels: 6}, forced) {
		t.Error("AAC converted although audio transcoding is disabled")
	}
}

// TestMappedAudioStream: the audio check looks at the stream FFmpeg is given, also when no
// track is selected and TrueHD comes first.
func TestMappedAudioStream(t *testing.T) {
	streams := []audioStreamInfo{
		{Index: 1, Codec: "truehd"},
		{Index: 2, Codec: "ac3", Title: "Director's Commentary"},
		{Index: 3, Codec: "eac3"},
	}
	for _, tc := range []struct {
		name                 string
		track                int
		hasTrueHD, hasCompat bool
		want                 int
	}{
		{"selected", 2, true, true, 2},
		{"TrueHD first: the first compatible non-commentary track", -1, true, true, 3},
		{"selected track missing", 9, true, true, 3},
		{"no TrueHD: the first stream", -1, false, true, 1},
	} {
		got, ok := mappedAudioStream(streams, tc.track, tc.hasTrueHD, tc.hasCompat)
		if !ok || got.Index != tc.want {
			t.Errorf("%s: %d %v, want %d", tc.name, got.Index, ok, tc.want)
		}
	}
	onlyCommentary := []audioStreamInfo{{Index: 1, Codec: "truehd"}, {Index: 2, Codec: "aac", Title: "Commentary"}}
	if got, ok := mappedAudioStream(onlyCommentary, -1, true, true); !ok || got.Index != 2 {
		t.Errorf("commentary fallback: %d %v", got.Index, ok)
	}
	if _, ok := mappedAudioStream(nil, -1, false, false); ok {
		t.Error("no streams")
	}
}
