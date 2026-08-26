package handlers

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// HWAccelKind identifies a hardware-accelerated H.264 encode backend.
type HWAccelKind string

const (
	HWNone         HWAccelKind = "none"
	HWNVENC        HWAccelKind = "nvenc"
	HWQSV          HWAccelKind = "qsv"
	HWVAAPI        HWAccelKind = "vaapi"
	HWVideoToolbox HWAccelKind = "videotoolbox"
)

// vaapiDefaultDevice is the render node used for VAAPI/QSV when one exists.
// In Docker this requires `--device /dev/dri` (or equivalent) to be passed
// through; if the node is absent we fall back to CPU.
const vaapiDefaultDevice = "/dev/dri/renderD128"

// HWAccelCaps describes the hardware capabilities detected for a given ffmpeg
// binary plus the configured preference. It is computed once (the probe runs a
// real null test-encode) and reused for every transcode.
type HWAccelCaps struct {
	// Encode is the chosen H.264 encode backend (HWNone => CPU libx264).
	Encode HWAccelKind
	// EncodeDevice is the render node for VAAPI/QSV (empty otherwise).
	EncodeDevice string
	// Tonemap is the verified HDR/DV -> SDR tone-mapping implementation:
	//   "libplacebo" — GPU (Vulkan); the only path that correctly applies the
	//                  Dolby Vision RPU (incl. Profile 5), preferred when usable.
	//   "opencl"     — GPU (tonemap_opencl), vendor-neutral, HDR10/HLG only.
	//   "zscale"     — CPU (libzimg), reliable fallback, HDR10/HLG only.
	//   ""           — no tone mapping available (naive transcode).
	Tonemap string
	// Zscale reports whether the CPU tone-map fallback is compiled in.
	Zscale bool
}

type HWAccelStatus struct {
	Configured       string `json:"configured"`
	EffectiveEncoder string `json:"effectiveEncoder"`
	HardwareEncode   bool   `json:"hardwareEncode"`
	ToneMapper       string `json:"toneMapper,omitempty"`
	RetryAfter       string `json:"retryAfter,omitempty"`
}

// videoEncodePlan is the concrete set of ffmpeg arguments for one transcode,
// derived from HWAccelCaps plus whether tone mapping is required.
type videoEncodePlan struct {
	// GlobalArgs are options that must precede -i (device initialization).
	GlobalArgs []string
	// Filter is the -vf value ("" when no filtering is needed).
	Filter string
	// EncoderArgs are the output-side codec options (-c:v ...).
	EncoderArgs []string
	// Tonemapped reports whether the output was tone mapped to SDR. When true
	// the HLS playlist must advertise SDR rather than PQ.
	Tonemapped bool
	// HardwareEncode reports whether a GPU encoder was selected.
	HardwareEncode bool
	// Kind is the encode backend used (for logging).
	Kind HWAccelKind
	// Tonemap is the selected tone-mapping implementation, if any.
	Tonemap string
}

const hwAccelFailureRetryDelay = 5 * time.Minute

// hwAccelCaps lazily detects hardware support and caches it by configured
// preference. Changing the setting therefore takes effect on the next web
// transcode without restarting the server.
func (m *HLSManager) hwAccelCaps() HWAccelCaps {
	pref := "auto"
	if m.configManager != nil {
		if settings, err := m.configManager.Load(); err == nil {
			if p := strings.ToLower(strings.TrimSpace(settings.Transmux.HardwareAcceleration)); p != "" {
				pref = p
			}
		} else {
			log.Printf("[hwaccel] failed to load configured preference; using %q: %v", pref, err)
		}
	}

	m.hwAccelMu.Lock()
	defer m.hwAccelMu.Unlock()
	if m.hwAccelReady && m.hwAccelPref == pref &&
		(m.hwAccelRetryAfter.IsZero() || time.Now().Before(m.hwAccelRetryAfter)) {
		log.Printf("[hwaccel] using cached capabilities: preference=%s encoder=%s device=%q tonemap=%s retryAfter=%s",
			pref, effectiveEncoderName(m.hwAccel), m.hwAccel.EncodeDevice, effectiveTonemapName(m.hwAccel),
			formatHWAccelRetryTime(m.hwAccelRetryAfter))
		return m.hwAccel
	}

	log.Printf("[hwaccel] detecting capabilities: preference=%s ffmpeg=%q previousPreference=%q ready=%v retryAfter=%s",
		pref, m.ffmpegPath, m.hwAccelPref, m.hwAccelReady, formatHWAccelRetryTime(m.hwAccelRetryAfter))
	m.hwAccel = detectHWAccel(m.ffmpegPath, pref)
	m.hwAccelPref = pref
	m.hwAccelReady = true
	m.hwAccelRetryAfter = time.Time{}
	log.Printf("[hwaccel] detection complete: preference=%s encoder=%s hardwareEncode=%v device=%q tonemap=%s zscale=%v",
		pref, effectiveEncoderName(m.hwAccel), m.hwAccel.Encode != HWNone, m.hwAccel.EncodeDevice,
		effectiveTonemapName(m.hwAccel), m.hwAccel.Zscale)
	return m.hwAccel
}

// markHWAccelFailed moves subsequent sessions to software encoding after a
// real media graph fails on the selected hardware path. Auto detection is
// retried later in case the failure was a transient driver/session-limit issue.
func (m *HLSManager) markHWAccelFailed(kind HWAccelKind, retainToneMapper bool) {
	if kind == HWNone {
		return
	}
	m.hwAccelMu.Lock()
	defer m.hwAccelMu.Unlock()
	if !m.hwAccelReady || m.hwAccel.Encode != kind {
		log.Printf("[hwaccel] ignoring failure quarantine for encoder=%s because cached encoder=%s ready=%v",
			kind, m.hwAccel.Encode, m.hwAccelReady)
		return
	}
	if retainToneMapper {
		log.Printf("[hwaccel] quarantining failed encoder=%s for %s; retaining tone mapper=%s",
			kind, hwAccelFailureRetryDelay, effectiveTonemapName(m.hwAccel))
		m.hwAccel.Encode = HWNone
		m.hwAccel.EncodeDevice = ""
	} else {
		log.Printf("[hwaccel] quarantining failed encoder=%s for %s; using software fallback",
			kind, hwAccelFailureRetryDelay)
		m.hwAccel = detectHWAccel(m.ffmpegPath, string(HWNone))
	}
	m.hwAccelPref = currentHWAccelPreference(m.configManager)
	m.hwAccelReady = true
	m.hwAccelRetryAfter = time.Now().Add(hwAccelFailureRetryDelay)
}

func currentHWAccelPreference(provider ConfigProvider) string {
	pref := "auto"
	if provider == nil {
		return pref
	}
	settings, err := provider.Load()
	if err != nil {
		return pref
	}
	if value := strings.ToLower(strings.TrimSpace(settings.Transmux.HardwareAcceleration)); value != "" {
		return value
	}
	return pref
}

func (m *HLSManager) HardwareAccelerationStatus() HWAccelStatus {
	caps := m.hwAccelCaps()
	status := HWAccelStatus{
		Configured:       currentHWAccelPreference(m.configManager),
		EffectiveEncoder: string(caps.Encode),
		HardwareEncode:   caps.Encode != HWNone,
		ToneMapper:       caps.Tonemap,
	}
	if caps.Encode == HWNone {
		status.EffectiveEncoder = "libx264"
	}
	m.hwAccelMu.Lock()
	if !m.hwAccelRetryAfter.IsZero() && time.Now().Before(m.hwAccelRetryAfter) {
		status.RetryAfter = m.hwAccelRetryAfter.UTC().Format(time.RFC3339)
	}
	m.hwAccelMu.Unlock()
	log.Printf("[hwaccel] status: configured=%s effectiveEncoder=%s hardwareEncode=%v toneMapper=%s retryAfter=%s",
		status.Configured, status.EffectiveEncoder, status.HardwareEncode,
		status.ToneMapper, status.RetryAfter)
	return status
}

func effectiveEncoderName(caps HWAccelCaps) string {
	if caps.Encode == HWNone {
		return "libx264"
	}
	return string(caps.Encode)
}

func effectiveTonemapName(caps HWAccelCaps) string {
	if caps.Tonemap == "" {
		return "none"
	}
	return caps.Tonemap
}

func supportsDolbyVisionToneMap(caps HWAccelCaps, profile string) bool {
	return !IsDVProfile5(profile) || caps.Tonemap == "libplacebo"
}

func formatHWAccelRetryTime(retryAfter time.Time) string {
	if retryAfter.IsZero() {
		return "none"
	}
	return retryAfter.UTC().Format(time.RFC3339)
}

func shouldFallbackHardwareEncode(commandErr, contextErr error, idleTriggered, inputErrored bool, plan videoEncodePlan, actualSegments int, attempted bool) bool {
	return commandErr != nil &&
		contextErr == nil &&
		!idleTriggered &&
		!inputErrored &&
		plan.HardwareEncode &&
		actualSegments == 0 &&
		!attempted
}

// detectHWAccel probes the ffmpeg binary and host devices to pick the best
// working H.264 encode backend honoring the configured preference. "auto"
// tries each candidate in priority order and verifies it with a tiny null
// test-encode; the first that succeeds wins. Any explicit preference that
// fails verification falls back to CPU.
func detectHWAccel(ffmpegPath, pref string) HWAccelCaps {
	caps := HWAccelCaps{Encode: HWNone}
	if strings.TrimSpace(ffmpegPath) == "" {
		log.Printf("[hwaccel] detection stopped: FFmpeg path is empty; using libx264 with no verified tone mapper")
		return caps
	}

	encoders := ffmpegEncoderSet(ffmpegPath)
	filters := ffmpegFilterSet(ffmpegPath)
	caps.Zscale = filters["zscale"]
	log.Printf("[hwaccel] FFmpeg capabilities: h264_nvenc=%v h264_qsv=%v h264_vaapi=%v h264_videotoolbox=%v libplacebo=%v tonemap_opencl=%v zscale=%v",
		encoders["h264_nvenc"], encoders["h264_qsv"], encoders["h264_vaapi"],
		encoders["h264_videotoolbox"], filters["libplacebo"], filters["tonemap_opencl"], filters["zscale"])

	pref = strings.ToLower(strings.TrimSpace(pref))
	if pref == "" {
		pref = "auto"
	}
	log.Printf("[hwaccel] normalized hardware acceleration preference=%s", pref)

	// Pick the best tone-mapping implementation that actually works. GPU paths
	// require a runtime device (Vulkan/OpenCL) that may be absent even when the
	// filter is compiled in — so each is verified with a null filter-graph run.
	// libplacebo is preferred: it is the only filter that applies the Dolby
	// Vision RPU correctly (other paths mishandle DV Profile 5's IPT base layer).
	// An explicit "none" preference disables GPU tone mapping as well as GPU
	// encoding. CPU zscale remains available for HDR10/DV7/DV8 fallback.
	caps.Tonemap = detectTonemap(ffmpegPath, filters, pref != string(HWNone))

	if pref == string(HWNone) {
		log.Printf("[hwaccel] GPU encoding disabled by preference; selected encoder=libx264 toneMapper=%s",
			effectiveTonemapName(caps))
		return caps
	}

	var candidates []HWAccelKind
	switch pref {
	case "auto":
		candidates = autoEncodeCandidates(encoders)
	case string(HWNVENC):
		candidates = []HWAccelKind{HWNVENC}
	case string(HWQSV):
		candidates = []HWAccelKind{HWQSV}
	case string(HWVAAPI):
		candidates = []HWAccelKind{HWVAAPI}
	case string(HWVideoToolbox):
		candidates = []HWAccelKind{HWVideoToolbox}
	default:
		log.Printf("[hwaccel] unsupported hardware acceleration preference=%q; using libx264", pref)
		return caps
	}

	for _, kind := range candidates {
		device, ok, reason := hwEncoderUsable(ffmpegPath, kind, encoders)
		if !ok {
			log.Printf("[hwaccel] encoder candidate rejected: kind=%s reason=%s", kind, reason)
			continue
		}
		caps.Encode = kind
		caps.EncodeDevice = device
		log.Printf("[hwaccel] encoder candidate selected: kind=%s device=%q", kind, device)
		return caps
	}

	log.Printf("[hwaccel] no usable hardware encoder found for preference=%s candidates=%v; using libx264", pref, candidates)
	return caps
}

// autoEncodeCandidates orders the "auto" preference probes. NVENC and QSV/VAAPI
// are the common Docker passthrough cases; VideoToolbox covers macOS hosts.
// Candidacy follows the encoder the configured ffmpeg actually reports rather
// than this process's GOOS, so a build that exposes VideoToolbox is always
// tried first. Every candidate still has to pass a real test encode.
func autoEncodeCandidates(encoders map[string]bool) []HWAccelKind {
	candidates := []HWAccelKind{HWNVENC, HWQSV, HWVAAPI}
	if encoders[hwEncoderName(HWVideoToolbox)] || runtime.GOOS == "darwin" {
		candidates = append([]HWAccelKind{HWVideoToolbox}, candidates...)
	}
	return candidates
}

// detectTonemap returns the best verified tone-mapping implementation.
func detectTonemap(ffmpegPath string, filters map[string]bool, allowGPU bool) string {
	if !allowGPU {
		log.Printf("[hwaccel] GPU tone mapping disabled by hardware acceleration preference")
	} else if !filters["libplacebo"] {
		log.Printf("[hwaccel] tone mapper candidate unavailable: kind=libplacebo reason=filter not present in FFmpeg")
	} else if libplaceboUsable(ffmpegPath) {
		log.Printf("[hwaccel] tone mapper selected: kind=libplacebo")
		return "libplacebo"
	}
	if allowGPU {
		if !filters["tonemap_opencl"] {
			log.Printf("[hwaccel] tone mapper candidate unavailable: kind=opencl reason=filter not present in FFmpeg")
		} else if openclTonemapUsable(ffmpegPath) {
			log.Printf("[hwaccel] tone mapper selected: kind=opencl")
			return "opencl"
		}
	}
	if filters["zscale"] {
		log.Printf("[hwaccel] tone mapper selected: kind=zscale execution=cpu")
		return "zscale"
	}
	log.Printf("[hwaccel] no usable tone mapper found; HDR/DV web transcodes will not be tone mapped")
	return ""
}

// libplaceboUsable verifies a Vulkan device initializes and the libplacebo
// filter runs on a real GPU. Mesa's Lavapipe/llvmpipe software Vulkan device
// can pass a tiny functional probe but is far too slow for real-time 4K HLS.
func libplaceboUsable(ffmpegPath string) bool {
	if softwareVulkanForcedByEnvironment() {
		log.Printf("[hwaccel] tone mapper candidate rejected: kind=libplacebo reason=software Vulkan forced by environment")
		return false
	}

	ok, output, err := runFilterProbeWithOutput(ffmpegPath,
		[]string{"-init_hw_device", "vulkan=vk", "-filter_hw_device", "vk"},
		"color=c=black:s=128x128:d=0.1",
		"libplacebo=format=yuv420p:apply_dolbyvision=true",
		"verbose")
	if !ok {
		log.Printf("[hwaccel] tone mapper candidate rejected: kind=libplacebo reason=probe failed error=%v output=%q",
			err, compactProbeOutput(output))
		return false
	}
	if vulkanProbeUsesSoftwareRenderer(output) {
		log.Printf("[hwaccel] tone mapper candidate rejected: kind=libplacebo reason=software Vulkan renderer output=%q",
			compactProbeOutput(output))
		return false
	}
	log.Printf("[hwaccel] tone mapper probe passed: kind=libplacebo")
	return true
}

// openclTonemapUsable verifies an OpenCL device initializes and tonemap_opencl runs.
func openclTonemapUsable(ffmpegPath string) bool {
	ok, output, err := runFilterProbeWithOutput(ffmpegPath,
		[]string{"-init_hw_device", "opencl=ocl", "-filter_hw_device", "ocl"},
		"color=c=black:s=128x128:d=0.1,format=yuv420p10le,setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc",
		"format=p010le,hwupload,tonemap_opencl=tonemap=hable:t=bt709:m=bt709:p=bt709:format=nv12,hwdownload,format=nv12",
		"error")
	if !ok {
		log.Printf("[hwaccel] tone mapper candidate rejected: kind=opencl reason=probe failed error=%v output=%q",
			err, compactProbeOutput(output))
		return false
	}
	log.Printf("[hwaccel] tone mapper probe passed: kind=opencl")
	return true
}

// runFilterProbe runs a tiny null-output transcode to confirm a filter graph
// (and any hardware device it needs) is usable on this host.
func runFilterProbe(ffmpegPath string, globalArgs []string, lavfiSrc, filter string) bool {
	ok, _, _ := runFilterProbeWithOutput(ffmpegPath, globalArgs, lavfiSrc, filter, "error")
	return ok
}

func runFilterProbeWithOutput(ffmpegPath string, globalArgs []string, lavfiSrc, filter, logLevel string) (bool, string, error) {
	if strings.TrimSpace(ffmpegPath) == "" {
		return false, "", fmt.Errorf("FFmpeg path is empty")
	}
	args := append([]string{"-hide_banner", "-loglevel", logLevel}, globalArgs...)
	args = append(args, "-f", "lavfi", "-i", lavfiSrc, "-vf", filter, "-frames:v", "1", "-f", "null", "-")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, ffmpegPath, args...).CombinedOutput()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return err == nil, string(output), err
}

func softwareVulkanForcedByEnvironment() bool {
	icd := strings.ToLower(os.Getenv("VK_ICD_FILENAMES"))
	if strings.Contains(icd, "lavapipe") || strings.Contains(icd, "lvp_icd") {
		return true
	}
	return strings.TrimSpace(os.Getenv("LIBGL_ALWAYS_SOFTWARE")) == "1"
}

func vulkanProbeUsesSoftwareRenderer(output string) bool {
	lower := strings.ToLower(output)
	for _, marker := range []string{
		"lavapipe",
		"llvmpipe",
		"software rasterizer",
		"device type: cpu",
		"device_type: cpu",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// hwEncoderUsable reports whether the given backend's H.264 encoder exists and
// passes a quick null test-encode (which confirms a working device — this is
// what makes Docker /dev passthrough detection reliable rather than assumed).
func hwEncoderUsable(ffmpegPath string, kind HWAccelKind, encoders map[string]bool) (device string, ok bool, reason string) {
	encoder := hwEncoderName(kind)
	if encoder == "" {
		return "", false, "no FFmpeg encoder mapping"
	}
	if !encoders[encoder] {
		return "", false, fmt.Sprintf("%s not present in FFmpeg", encoder)
	}

	switch kind {
	case HWVAAPI, HWQSV:
		// Require the render node to exist before even attempting the probe.
		if _, err := os.Stat(vaapiDefaultDevice); err != nil {
			return "", false, fmt.Sprintf("render device %s unavailable: %v", vaapiDefaultDevice, err)
		}
		device = vaapiDefaultDevice
	}

	args := []string{"-hide_banner", "-loglevel", "error"}
	switch kind {
	case HWVAAPI:
		args = append(args, "-vaapi_device", device,
			"-f", "lavfi", "-i", "color=c=black:s=128x128:d=0.1",
			"-vf", "format=nv12,hwupload", "-c:v", encoder)
	case HWQSV:
		args = append(args, "-init_hw_device", "qsv=hw:"+device, "-filter_hw_device", "hw",
			"-f", "lavfi", "-i", "color=c=black:s=128x128:d=0.1",
			"-vf", "format=nv12,hwupload=extra_hw_frames=16,format=qsv", "-c:v", encoder)
	default:
		args = append(args, "-f", "lavfi", "-i", "color=c=black:s=128x128:d=0.1",
			"-c:v", encoder)
	}
	args = append(args, "-frames:v", "1", "-f", "null", "-")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, ffmpegPath, args...).CombinedOutput()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		return "", false, fmt.Sprintf("test encode failed: %v; output=%q", err, compactProbeOutput(string(output)))
	}
	return device, true, "test encode passed"
}

const maxProbeOutputLogBytes = 2048

func compactProbeOutput(output string) string {
	compact := strings.Join(strings.Fields(output), " ")
	if compact == "" {
		return "<no output>"
	}
	if len(compact) > maxProbeOutputLogBytes {
		return compact[:maxProbeOutputLogBytes] + "...[truncated]"
	}
	return compact
}

func hwEncoderName(kind HWAccelKind) string {
	switch kind {
	case HWNVENC:
		return "h264_nvenc"
	case HWQSV:
		return "h264_qsv"
	case HWVAAPI:
		return "h264_vaapi"
	case HWVideoToolbox:
		return "h264_videotoolbox"
	default:
		return ""
	}
}

// ffmpegEncoderSet returns the set of encoder names reported by ffmpeg.
func ffmpegEncoderSet(ffmpegPath string) map[string]bool {
	return ffmpegTokenSet(ffmpegPath, "-encoders")
}

// ffmpegFilterSet returns the set of filter names reported by ffmpeg.
func ffmpegFilterSet(ffmpegPath string) map[string]bool {
	return ffmpegTokenSet(ffmpegPath, "-filters")
}

// ffmpegTokenSet runs `ffmpeg -hide_banner <listFlag>` and extracts the second
// whitespace-separated token of each capability line (the encoder/filter name).
func ffmpegTokenSet(ffmpegPath, listFlag string) map[string]bool {
	set := make(map[string]bool)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpegPath, "-hide_banner", listFlag)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		log.Printf("[hwaccel] FFmpeg capability query failed: flag=%s ffmpeg=%q error=%v output=%q",
			listFlag, ffmpegPath, err, compactProbeOutput(out.String()))
		return set
	}
	for _, line := range strings.Split(out.String(), "\n") {
		fields := strings.Fields(line)
		// Capability lines look like " V..... h264_nvenc  NVIDIA NVENC ...".
		// The first field is the flags column, the second is the name.
		if len(fields) < 2 {
			continue
		}
		flags := fields[0]
		// Skip header/separator lines (flags column is letters/dots only).
		if !looksLikeCapabilityFlags(flags) {
			continue
		}
		set[fields[1]] = true
	}
	return set
}

func looksLikeCapabilityFlags(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r == '.' {
			continue
		}
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			continue
		}
		return false
	}
	return true
}

// buildVideoEncodePlan assembles the ffmpeg arguments for a web transcode given
// the detected capabilities and whether the source is HDR/DV (tonemapNeeded).
// sourceTransfer is optional for compatibility with callers that have no probe
// metadata; those sources conservatively default to PQ.
func buildVideoEncodePlan(caps HWAccelCaps, tonemapNeeded bool, sourceTransfer ...string) videoEncodePlan {
	return buildVideoEncodePlanWithLimits(caps, tonemapNeeded, 0, 0, 0, sourceTransfer...)
}

func buildVideoEncodePlanWithLimits(
	caps HWAccelCaps,
	tonemapNeeded bool,
	maxWidth, maxHeight, maxFPS int,
	sourceTransfer ...string,
) videoEncodePlan {
	plan := videoEncodePlan{Kind: caps.Encode}

	// VAAPI/QSV encoders need their own filter hardware device for the hwupload
	// step, which conflicts with a second (Vulkan/OpenCL) device for GPU tone
	// mapping. To stay robust we tone map on the CPU (zscale) for those encoders
	// and reserve the GPU tone-map filters for encoders that consume system
	// memory frames (NVENC / VideoToolbox / libx264).
	tonemapImpl := caps.Tonemap
	if caps.Encode == HWVAAPI || caps.Encode == HWQSV {
		if caps.Zscale {
			tonemapImpl = "zscale"
		} else {
			tonemapImpl = ""
		}
	}
	var filters []string
	if maxWidth > 0 && maxHeight > 0 {
		// Fit inside the receiver's decode box before tone mapping and hardware
		// upload. The min() bounds prevent upscaling; the aspect-ratio and
		// divisibility options keep the output valid for 4:2:0 H.264 encoders.
		filters = append(filters, fmt.Sprintf(
			"scale=w='min(%d,iw)':h='min(%d,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2",
			maxWidth,
			maxHeight,
		))
	}
	if maxFPS > 0 {
		// Cap high-frame-rate input without duplicating lower-rate frames.
		filters = append(filters, fmt.Sprintf("fps='min(source_fps,%d)'", maxFPS))
	}
	if tonemapNeeded && tonemapImpl != "" {
		plan.Tonemap = tonemapImpl
		inputTransfer := "smpte2084"
		if len(sourceTransfer) > 0 && strings.EqualFold(strings.TrimSpace(sourceTransfer[0]), "arib-std-b67") {
			inputTransfer = "arib-std-b67"
		}
		inputColorParams := "setparams=range=tv:color_primaries=bt2020:color_trc=" + inputTransfer + ":colorspace=bt2020nc"
		plan.Tonemapped = true
		switch tonemapImpl {
		case "libplacebo":
			// GPU tone mapping via libplacebo (Vulkan). Applies the Dolby Vision
			// RPU when present; output is downloaded to system-memory yuv420p.
			plan.GlobalArgs = append(plan.GlobalArgs, "-init_hw_device", "vulkan=vk", "-filter_hw_device", "vk")
			filters = append(filters,
				"libplacebo=tonemapping=bt.2390:colorspace=bt709:color_primaries=bt709:color_trc=bt709:range=tv:apply_dolbyvision=true:format=yuv420p")
		case "opencl":
			// GPU tone mapping via OpenCL (vendor-neutral, HDR10/HLG). Output is
			// downloaded back to system memory so the encoder stage is independent.
			plan.GlobalArgs = append(plan.GlobalArgs, "-init_hw_device", "opencl=ocl", "-filter_hw_device", "ocl")
			filters = append(filters,
				inputColorParams,
				"format=p010le",
				"hwupload",
				"tonemap_opencl=tonemap=hable:desat=0:t=bt709:m=bt709:p=bt709:format=nv12",
				"hwdownload",
				"format=nv12")
		default: // zscale (CPU, libzimg). Mobius operator with a mild SDR presentation lift.
			filters = append(filters,
				// Some DV8 files omit container-level color metadata even though
				// their base layer is HDR10. Without explicit input properties,
				// zscale aborts with "no path between colorspaces".
				inputColorParams,
				"zscale=t=linear:npl=100",
				"format=gbrpf32le",
				"tonemap=tonemap=mobius:param=0.35:desat=0:peak=1000",
				"zscale=t=bt709:m=bt709:p=bt709:r=tv",
				"eq=brightness=0.03:contrast=1.08:saturation=1.15:gamma=0.98",
				"format=yuv420p")
		}
	}

	// Encoder stage. VAAPI/QSV need their frames uploaded to the device.
	switch caps.Encode {
	case HWNVENC:
		plan.HardwareEncode = true
		if len(filters) == 0 {
			filters = append(filters, "format=yuv420p")
		}
		plan.EncoderArgs = []string{
			"-c:v", "h264_nvenc",
			"-preset", "p4",
			"-tune", "ll",
			"-rc", "vbr",
			"-cq", "23",
			"-profile:v", "high",
			"-pix_fmt", "yuv420p",
		}
	case HWVideoToolbox:
		plan.HardwareEncode = true
		if len(filters) == 0 {
			filters = append(filters, "format=yuv420p")
		}
		plan.EncoderArgs = []string{
			"-c:v", "h264_videotoolbox",
			"-realtime", "1",
			"-profile:v", "high",
			"-q:v", "60",
			"-pix_fmt", "yuv420p",
		}
	case HWVAAPI:
		plan.HardwareEncode = true
		plan.GlobalArgs = append(plan.GlobalArgs, "-vaapi_device", caps.EncodeDevice)
		filters = append(filters, "format=nv12", "hwupload")
		plan.EncoderArgs = []string{
			"-c:v", "h264_vaapi",
			"-rc_mode", "CQP",
			"-qp", "23",
			"-profile:v", "high",
		}
	case HWQSV:
		plan.HardwareEncode = true
		plan.GlobalArgs = append(plan.GlobalArgs, "-init_hw_device", "qsv=hw:"+caps.EncodeDevice, "-filter_hw_device", "hw")
		filters = append(filters, "format=nv12", "hwupload=extra_hw_frames=64", "format=qsv")
		plan.EncoderArgs = []string{
			"-c:v", "h264_qsv",
			"-preset", "veryfast",
			"-global_quality", "23",
			"-profile:v", "high",
		}
	default: // CPU libx264
		if len(filters) == 0 {
			filters = append(filters, "format=yuv420p")
		}
		plan.EncoderArgs = []string{
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			"-crf", "23",
			"-profile:v", "high",
			"-level", "4.1",
			"-pix_fmt", "yuv420p",
		}
	}
	if maxFPS > 0 {
		plan.EncoderArgs = append(plan.EncoderArgs, "-level:v", "4.1")
	}

	if len(filters) > 0 {
		plan.Filter = strings.Join(filters, ",")
	}
	return plan
}
