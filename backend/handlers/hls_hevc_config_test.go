package handlers

import (
	"fmt"
	"strings"
	"testing"

	"novastream/models"
)

// An HEVC source whose hvcC carries no parameter sets (they are only in the
// stream) is recognised from ffprobe's extradata size.
func TestProbeDetectsHEVCWithoutParameterSets(t *testing.T) {
	m := &HLSManager{}
	probe := func(codec, extradata string) *UnifiedProbeResult {
		t.Helper()
		json := fmt.Sprintf(`{"format":{"duration":"10"},"streams":[{"index":0,"codec_type":"video","codec_name":%q,"pix_fmt":"yuv420p10le","width":3840,"height":2160%s}]}`, codec, extradata)
		res, err := m.parseUnifiedProbeOutput([]byte(json))
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	cases := []struct {
		name, codec, extradata string
		want                   bool
	}{
		{"hevc, empty hvcC", "hevc", `,"extradata_size":23`, true},
		{"hevc, no configuration at all", "hevc", `,"extradata_size":0`, true},
		{"hevc with parameter sets", "hevc", `,"extradata_size":2554`, false},
		{"hevc, size not reported", "hevc", ``, false},
		{"h264", "h264", `,"extradata_size":23`, false},
	}
	for _, tc := range cases {
		if got := probe(tc.codec, tc.extradata).VideoParamSetsMissing; got != tc.want {
			t.Errorf("%s: VideoParamSetsMissing = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Such a source is copied into fMP4 through Annex B, so the muxer rebuilds
// the hvcC from the in-band parameter sets; other sources are copied as
// before.
func TestStableCopyRebuildsEmptyHEVCConfig(t *testing.T) {
	src := hevcProbe("")
	src.VideoParamSetsMissing = true
	plan, ok := planStableCopy(src, false, "", false, chromeCaps)
	if !ok || !plan.RebuildConfig || !plan.FMP4 || plan.Tag != "hvc1" {
		t.Fatalf("plan = %+v ok=%v", plan, ok)
	}
	args := strings.Join(plan.videoCopyArgs(false, false), " ")
	if !strings.Contains(args, "-bsf:v hevc_mp4toannexb") || !strings.Contains(args, "-tag:v hvc1") {
		t.Errorf("args = %s", args)
	}

	// Transcoding disabled: the forced copy keeps the rebuild.
	forced, _ := planStableCopy(src, false, "", false, &HLSClientCaps{ForceVideoCopy: true, FMP4Video: []string{"hevc"}})
	if !forced.RebuildConfig || !strings.Contains(strings.Join(forced.videoCopyArgs(false, false), " "), "hevc_mp4toannexb") {
		t.Errorf("forced copy plan = %+v", forced)
	}

	// The conversion comes before the colour fix, which then sees the
	// parameter sets too.
	hdr := hevcProbe("smpte2084")
	hdr.VideoParamSetsMissing = true
	hdrPlan, _ := planStableCopy(hdr, false, "", true, chromeCaps)
	if args := strings.Join(hdrPlan.videoCopyArgs(false, false), " "); !strings.Contains(args, "-bsf:v hevc_mp4toannexb,hevc_metadata=") {
		t.Errorf("hdr args = %s", args)
	}

	// MPEG-TS needs no hvcC (the muxer converts to Annex B itself), and a
	// normal source keeps its own configuration.
	if args := strings.Join((stableCopyPlan{Codec: "hevc", RebuildConfig: true}).videoCopyArgs(false, false), " "); strings.Contains(args, "annexb") {
		t.Errorf("TS args = %s", args)
	}
	normal, _ := planStableCopy(hevcProbe(""), false, "", false, chromeCaps)
	if normal.RebuildConfig || strings.Contains(strings.Join(normal.videoCopyArgs(false, false), " "), "annexb") {
		t.Errorf("normal hevc plan = %+v", normal)
	}
}

// The prequeue's full probe feeds the same cache the HLS session reads: the
// flag has to survive both conversions.
func TestHEVCParamSetsFlagSurvivesFullProbeCache(t *testing.T) {
	h := &VideoHandler{}
	full := &models.VideoFullResult{VideoCodec: "hevc", VideoParamSetsMissing: true}
	cached := h.videoFullToUnifiedProbe(full)
	if !cached.VideoParamSetsMissing {
		t.Fatal("lost converting the full probe into the HLS cache")
	}
	if back := h.unifiedProbeToVideoFull(cached); !back.VideoParamSetsMissing {
		t.Fatal("lost converting the cached probe back")
	}
	size23, size900 := 23, 900
	for _, tc := range []struct {
		codec string
		size  *int
		want  bool
	}{{"hevc", &size23, true}, {"HEVC", &size23, true}, {"hevc", &size900, false}, {"hevc", nil, false}, {"h264", &size23, false}} {
		if got := hevcParamSetsMissing(tc.codec, tc.size); got != tc.want {
			t.Errorf("%s %v: %v", tc.codec, tc.size, got)
		}
	}
}
