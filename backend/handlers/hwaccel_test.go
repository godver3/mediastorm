package handlers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"novastream/config"
)

func joinArgs(args []string) string { return strings.Join(args, " ") }

func TestBuildVideoEncodePlanCPUNoTonemap(t *testing.T) {
	plan := buildVideoEncodePlan(HWAccelCaps{Encode: HWNone, Tonemap: "zscale"}, false)
	if plan.HardwareEncode {
		t.Fatalf("expected CPU encode, got hardware")
	}
	if plan.Tonemapped {
		t.Fatalf("did not expect tone mapping")
	}
	if !strings.Contains(joinArgs(plan.EncoderArgs), "libx264") {
		t.Fatalf("expected libx264 encoder, got %v", plan.EncoderArgs)
	}
	if plan.Filter != "format=yuv420p" {
		t.Fatalf("expected pixel-format normalization filter, got %q", plan.Filter)
	}
	if len(plan.GlobalArgs) != 0 {
		t.Fatalf("CPU plan should need no device init, got %v", plan.GlobalArgs)
	}
}

func TestBuildVideoEncodePlanCPUTonemap(t *testing.T) {
	plan := buildVideoEncodePlan(HWAccelCaps{Encode: HWNone, Tonemap: "zscale"}, true)
	if !plan.Tonemapped {
		t.Fatalf("expected tone mapping")
	}
	if !strings.Contains(plan.Filter, "tonemap=tonemap=mobius:param=0.35:desat=0:peak=1000") {
		t.Fatalf("expected CPU zscale tonemap chain, got %q", plan.Filter)
	}
	if !strings.Contains(plan.Filter, "eq=brightness=0.03:contrast=1.08:saturation=1.15:gamma=0.98") {
		t.Fatalf("expected SDR presentation correction, got %q", plan.Filter)
	}
	if !strings.HasSuffix(plan.Filter, "format=yuv420p") {
		t.Fatalf("tonemap output should be yuv420p, got %q", plan.Filter)
	}
	if !strings.HasPrefix(plan.Filter, "setparams=range=tv:color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc,") {
		t.Fatalf("tone map must normalize missing HDR input metadata before zscale, got %q", plan.Filter)
	}
}

func TestBuildVideoEncodePlanTonemapWithoutSupportFallsBack(t *testing.T) {
	// No tone-map implementation available -> cannot tone map; must not claim it did.
	plan := buildVideoEncodePlan(HWAccelCaps{Encode: HWNone, Tonemap: ""}, true)
	if plan.Tonemapped {
		t.Fatalf("must not tone map without a tonemap implementation")
	}
	if strings.Contains(plan.Filter, "tonemap") {
		t.Fatalf("unexpected tonemap filter: %q", plan.Filter)
	}
}

func TestBuildVideoEncodePlanLibplaceboTonemap(t *testing.T) {
	plan := buildVideoEncodePlan(HWAccelCaps{Encode: HWNVENC, Tonemap: "libplacebo"}, true)
	if !plan.Tonemapped || !plan.HardwareEncode {
		t.Fatalf("expected GPU tonemap + hardware encode")
	}
	if !strings.Contains(plan.Filter, "libplacebo") || !strings.Contains(plan.Filter, "apply_dolbyvision=true") {
		t.Fatalf("expected libplacebo DV tonemap, got %q", plan.Filter)
	}
	if !strings.Contains(joinArgs(plan.GlobalArgs), "vulkan=vk") {
		t.Fatalf("expected vulkan device init, got %v", plan.GlobalArgs)
	}
	if !strings.Contains(joinArgs(plan.EncoderArgs), "h264_nvenc") {
		t.Fatalf("expected nvenc encoder, got %v", plan.EncoderArgs)
	}
}

func TestSupportsDolbyVisionToneMap(t *testing.T) {
	tests := []struct {
		name    string
		caps    HWAccelCaps
		profile string
		want    bool
	}{
		{name: "profile 5 with libplacebo", caps: HWAccelCaps{Tonemap: "libplacebo"}, profile: "dvhe.05.06", want: true},
		{name: "profile 5 with zscale", caps: HWAccelCaps{Tonemap: "zscale"}, profile: "dvhe.05.06", want: false},
		{name: "profile 5 with OpenCL", caps: HWAccelCaps{Tonemap: "opencl"}, profile: "dvhe.05.09", want: false},
		{name: "profile 8 with zscale fallback", caps: HWAccelCaps{Tonemap: "zscale"}, profile: "dvhe.08.06", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := supportsDolbyVisionToneMap(tt.caps, tt.profile); got != tt.want {
				t.Fatalf("supportsDolbyVisionToneMap(%q) = %v, want %v", tt.profile, got, tt.want)
			}
		})
	}
}

func TestBuildVideoEncodePlanOpenCLTonemap(t *testing.T) {
	plan := buildVideoEncodePlan(HWAccelCaps{Encode: HWNVENC, Tonemap: "opencl"}, true)
	if !strings.Contains(plan.Filter, "tonemap_opencl") {
		t.Fatalf("expected OpenCL tonemap, got %q", plan.Filter)
	}
	if !strings.Contains(joinArgs(plan.GlobalArgs), "opencl=ocl") {
		t.Fatalf("expected OpenCL device init, got %v", plan.GlobalArgs)
	}
}

func TestBuildVideoEncodePlanPreservesHLGInputTransfer(t *testing.T) {
	plan := buildVideoEncodePlan(HWAccelCaps{Encode: HWNone, Tonemap: "zscale"}, true, "arib-std-b67")
	if !strings.HasPrefix(plan.Filter, "setparams=range=tv:color_primaries=bt2020:color_trc=arib-std-b67:colorspace=bt2020nc,") {
		t.Fatalf("HLG tone map must retain its input transfer, got %q", plan.Filter)
	}
}

func TestBuildVideoEncodePlanVAAPIForcesCPUTonemap(t *testing.T) {
	// VAAPI encode + a GPU tonemap pref must not init a second filter device;
	// tone mapping is forced onto the CPU (zscale) to avoid the conflict.
	plan := buildVideoEncodePlan(HWAccelCaps{Encode: HWVAAPI, EncodeDevice: "/dev/dri/renderD128", Tonemap: "libplacebo", Zscale: true}, true)
	if strings.Contains(joinArgs(plan.GlobalArgs), "vulkan") {
		t.Fatalf("vaapi must not also init a vulkan filter device, got %v", plan.GlobalArgs)
	}
	if !strings.Contains(plan.Filter, "tonemap=tonemap=mobius") {
		t.Fatalf("expected CPU zscale tonemap for vaapi, got %q", plan.Filter)
	}
}

func TestBuildVideoEncodePlanVAAPIUploads(t *testing.T) {
	plan := buildVideoEncodePlan(HWAccelCaps{Encode: HWVAAPI, EncodeDevice: "/dev/dri/renderD128", Tonemap: "zscale", Zscale: true}, true)
	if !strings.Contains(joinArgs(plan.GlobalArgs), "-vaapi_device /dev/dri/renderD128") {
		t.Fatalf("expected vaapi device init, got %v", plan.GlobalArgs)
	}
	// Tone map (CPU here, TonemapGPU false) then upload to the GPU surface for h264_vaapi.
	if !strings.Contains(plan.Filter, "tonemap") {
		t.Fatalf("expected tonemap in filter, got %q", plan.Filter)
	}
	if !strings.HasSuffix(plan.Filter, "format=nv12,hwupload") {
		t.Fatalf("vaapi filter must end with hwupload, got %q", plan.Filter)
	}
	if !strings.Contains(joinArgs(plan.EncoderArgs), "h264_vaapi") {
		t.Fatalf("expected vaapi encoder, got %v", plan.EncoderArgs)
	}
}

func TestBuildVideoEncodePlanQSVDeviceInit(t *testing.T) {
	plan := buildVideoEncodePlan(HWAccelCaps{Encode: HWQSV, EncodeDevice: "/dev/dri/renderD128", Tonemap: "zscale"}, false)
	if !strings.Contains(joinArgs(plan.GlobalArgs), "qsv=hw:/dev/dri/renderD128") {
		t.Fatalf("expected qsv device init, got %v", plan.GlobalArgs)
	}
	if !strings.Contains(plan.Filter, "format=qsv") {
		t.Fatalf("expected qsv upload in filter, got %q", plan.Filter)
	}
	if !strings.Contains(joinArgs(plan.EncoderArgs), "h264_qsv") {
		t.Fatalf("expected qsv encoder, got %v", plan.EncoderArgs)
	}
}

func TestBuildVideoEncodePlanVideoToolbox(t *testing.T) {
	plan := buildVideoEncodePlan(HWAccelCaps{Encode: HWVideoToolbox, Tonemap: "zscale"}, false)
	if !plan.HardwareEncode {
		t.Fatalf("expected hardware encode")
	}
	if !strings.Contains(joinArgs(plan.EncoderArgs), "h264_videotoolbox") {
		t.Fatalf("expected videotoolbox encoder, got %v", plan.EncoderArgs)
	}
	if len(plan.GlobalArgs) != 0 {
		t.Fatalf("videotoolbox needs no device init, got %v", plan.GlobalArgs)
	}
}

func TestDetectHWAccelNonePreference(t *testing.T) {
	caps := detectHWAccel("/nonexistent/ffmpeg", "none")
	if caps.Encode != HWNone {
		t.Fatalf("none preference must yield HWNone, got %v", caps.Encode)
	}
}

func TestDetectHWAccelMissingBinary(t *testing.T) {
	caps := detectHWAccel("", "auto")
	if caps.Encode != HWNone {
		t.Fatalf("missing ffmpeg must yield HWNone, got %v", caps.Encode)
	}
}

func TestSoftwareVulkanForcedByEnvironment(t *testing.T) {
	t.Run("lavapipe ICD", func(t *testing.T) {
		t.Setenv("VK_ICD_FILENAMES", "/usr/share/vulkan/icd.d/lavapipe_icd.json")
		t.Setenv("LIBGL_ALWAYS_SOFTWARE", "")
		if !softwareVulkanForcedByEnvironment() {
			t.Fatal("expected Lavapipe ICD to be treated as software Vulkan")
		}
	})

	t.Run("lvp ICD", func(t *testing.T) {
		t.Setenv("VK_ICD_FILENAMES", "/usr/share/vulkan/icd.d/lvp_icd.x86_64.json")
		t.Setenv("LIBGL_ALWAYS_SOFTWARE", "")
		if !softwareVulkanForcedByEnvironment() {
			t.Fatal("expected lvp ICD to be treated as software Vulkan")
		}
	})

	t.Run("hardware ICD", func(t *testing.T) {
		t.Setenv("VK_ICD_FILENAMES", "/usr/share/vulkan/icd.d/nvidia_icd.json")
		t.Setenv("LIBGL_ALWAYS_SOFTWARE", "")
		if softwareVulkanForcedByEnvironment() {
			t.Fatal("hardware ICD must not be treated as software Vulkan")
		}
	})
}

func TestVulkanProbeUsesSoftwareRenderer(t *testing.T) {
	for _, output := range []string{
		"Device 0: llvmpipe (LLVM 15.0.6, 256 bits)",
		"selected device: lavapipe",
		"renderer: Software Rasterizer",
		"device type: CPU",
	} {
		if !vulkanProbeUsesSoftwareRenderer(output) {
			t.Fatalf("expected software renderer detection for %q", output)
		}
	}
	if vulkanProbeUsesSoftwareRenderer("Device 0: NVIDIA GeForce RTX 4070, device type: discrete") {
		t.Fatal("hardware Vulkan renderer must remain eligible")
	}
}

func TestLooksLikeCapabilityFlags(t *testing.T) {
	cases := map[string]bool{
		"V.....":   true,
		"VFS..D.":  true,
		"------":   false, // separator line
		"=":        false,
		"":         false,
		"Encoders": true, // letters only — header word, filtered out elsewhere by needing a 2nd field
	}
	for in, want := range cases {
		if got := looksLikeCapabilityFlags(in); got != want {
			t.Errorf("looksLikeCapabilityFlags(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestFFmpegTokenSetParsesNames(t *testing.T) {
	// Parsing is exercised indirectly; ensure missing binary returns empty set
	// rather than panicking.
	if set := ffmpegTokenSet("/nonexistent/ffmpeg", "-encoders"); len(set) != 0 {
		t.Fatalf("expected empty set for missing binary, got %v", set)
	}
}

func TestCompactProbeOutput(t *testing.T) {
	if got := compactProbeOutput("  first line\nsecond\tline  "); got != "first line second line" {
		t.Fatalf("compactProbeOutput normalized whitespace to %q", got)
	}
	if got := compactProbeOutput(" \n\t "); got != "<no output>" {
		t.Fatalf("compactProbeOutput(empty) = %q", got)
	}
	long := strings.Repeat("x", maxProbeOutputLogBytes+100)
	got := compactProbeOutput(long)
	if len(got) >= len(long) || !strings.HasSuffix(got, "...[truncated]") {
		t.Fatalf("compactProbeOutput did not bound long output: len=%d suffix=%q", len(got), got[len(got)-20:])
	}
}

func TestHWEncoderUsableExplainsMissingEncoder(t *testing.T) {
	_, ok, reason := hwEncoderUsable("/nonexistent/ffmpeg", HWNVENC, map[string]bool{})
	if ok {
		t.Fatal("missing encoder unexpectedly usable")
	}
	if !strings.Contains(reason, "h264_nvenc not present") {
		t.Fatalf("missing encoder reason = %q", reason)
	}
}

type mutableHWAccelConfigProvider struct {
	settings config.Settings
}

func (p *mutableHWAccelConfigProvider) Load() (config.Settings, error) {
	return p.settings, nil
}

func TestHWAccelCacheRefreshesWhenPreferenceChanges(t *testing.T) {
	provider := &mutableHWAccelConfigProvider{settings: config.DefaultSettings()}
	provider.settings.Transmux.HardwareAcceleration = "auto"
	manager := NewHLSManager(t.TempDir(), "", "", nil)
	manager.SetConfigManager(provider)

	_ = manager.hwAccelCaps()
	if manager.hwAccelPref != "auto" {
		t.Fatalf("cached preference = %q, want auto", manager.hwAccelPref)
	}

	provider.settings.Transmux.HardwareAcceleration = "none"
	_ = manager.hwAccelCaps()
	if manager.hwAccelPref != "none" {
		t.Fatalf("cached preference = %q, want none after settings change", manager.hwAccelPref)
	}
}

func TestMarkHWAccelFailedPreservesProfile5ToneMapper(t *testing.T) {
	provider := &mutableHWAccelConfigProvider{settings: config.DefaultSettings()}
	provider.settings.Transmux.HardwareAcceleration = "auto"
	manager := NewHLSManager(t.TempDir(), "", "", nil)
	manager.SetConfigManager(provider)
	manager.hwAccel = HWAccelCaps{Encode: HWNVENC, Tonemap: "libplacebo"}
	manager.hwAccelPref = "auto"
	manager.hwAccelReady = true

	manager.markHWAccelFailed(HWNVENC, true)
	status := manager.HardwareAccelerationStatus()
	if status.EffectiveEncoder != "libx264" || status.HardwareEncode {
		t.Fatalf("status after failure = %+v, want software fallback", status)
	}
	if status.ToneMapper != "libplacebo" {
		t.Fatalf("hardware encoder failure discarded tone mapper: %+v", status)
	}
	if status.RetryAfter == "" {
		t.Fatal("expected failed hardware path to have a retry time")
	}
	plan := buildVideoEncodePlan(manager.hwAccel, true)
	if plan.HardwareEncode || !strings.Contains(plan.Filter, "libplacebo") {
		t.Fatalf("fallback plan = %+v, want libx264 with libplacebo", plan)
	}
	if strings.Contains(joinArgs(plan.GlobalArgs), "-hwaccel videotoolbox") {
		t.Fatalf("fallback must disable VideoToolbox decode too: %v", plan.GlobalArgs)
	}
}

func TestMarkHWAccelFailedResetsToneMapperForOtherSources(t *testing.T) {
	provider := &mutableHWAccelConfigProvider{settings: config.DefaultSettings()}
	provider.settings.Transmux.HardwareAcceleration = "auto"
	manager := NewHLSManager(t.TempDir(), "", "", nil)
	manager.SetConfigManager(provider)
	manager.hwAccel = HWAccelCaps{Encode: HWNVENC, Tonemap: "libplacebo"}
	manager.hwAccelPref = "auto"
	manager.hwAccelReady = true

	manager.markHWAccelFailed(HWNVENC, false)
	status := manager.HardwareAccelerationStatus()
	if status.EffectiveEncoder != "libx264" || status.HardwareEncode || status.ToneMapper != "" {
		t.Fatalf("status after ordinary failure = %+v, want original software fallback", status)
	}
}

func TestShouldFallbackHardwareEncode(t *testing.T) {
	hardwarePlan := videoEncodePlan{HardwareEncode: true, Kind: HWNVENC}
	commandErr := errors.New("ffmpeg failed")
	if !shouldFallbackHardwareEncode(commandErr, nil, false, false, hardwarePlan, 0, false) {
		t.Fatal("expected early hardware failure to retry in software")
	}
	for name, got := range map[string]bool{
		"no command error": shouldFallbackHardwareEncode(nil, nil, false, false, hardwarePlan, 0, false),
		"context canceled": shouldFallbackHardwareEncode(commandErr, context.Canceled, false, false, hardwarePlan, 0, false),
		"idle timeout":     shouldFallbackHardwareEncode(commandErr, nil, true, false, hardwarePlan, 0, false),
		"input error":      shouldFallbackHardwareEncode(commandErr, nil, false, true, hardwarePlan, 0, false),
		"segment produced": shouldFallbackHardwareEncode(commandErr, nil, false, false, hardwarePlan, 1, false),
		"already retried":  shouldFallbackHardwareEncode(commandErr, nil, false, false, hardwarePlan, 0, true),
		"software plan":    shouldFallbackHardwareEncode(commandErr, nil, false, false, videoEncodePlan{}, 0, false),
	} {
		if got {
			t.Fatalf("%s unexpectedly requested a hardware fallback", name)
		}
	}
}

func TestHardwareAccelerationStatusReportsRetryDeadline(t *testing.T) {
	manager := NewHLSManager(t.TempDir(), "", "", nil)
	manager.hwAccel = HWAccelCaps{Encode: HWNone, Tonemap: "zscale"}
	manager.hwAccelPref = "auto"
	manager.hwAccelReady = true
	manager.hwAccelRetryAfter = time.Now().Add(time.Minute)
	status := manager.HardwareAccelerationStatus()
	if status.EffectiveEncoder != "libx264" || status.RetryAfter == "" {
		t.Fatalf("unexpected status: %+v", status)
	}
}
