package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// HLSClientCaps is what a client plays inside HLS, as declared by the
// Jellyfin bridge from the client's DeviceProfile. A stable copy session uses
// it to decide whether the source video can be copied instead of transcoded,
// into which container (MPEG-TS or fMP4), and whether the audio can stay.
type HLSClientCaps struct {
	TSVideo   []string `json:"tsVideo,omitempty"`
	TSAudio   []string `json:"tsAudio,omitempty"`
	FMP4Video []string `json:"fmp4Video,omitempty"`
	FMP4Audio []string `json:"fmp4Audio,omitempty"`
	// VideoRanges lists, per video codec, the Jellyfin VideoRangeType values
	// the client displays (SDR, HDR10, HDR10Plus, HLG, DOVI,
	// DOVIWithHDR10, ...). A codec without an entry is SDR-only.
	VideoRanges      map[string][]string `json:"videoRanges,omitempty"`
	MaxAudioChannels int                 `json:"maxAudioChannels,omitempty"`
	// NoVideoCopy forces a transcode (the client failed to play a copy).
	NoVideoCopy bool `json:"noVideoCopy,omitempty"`
	// ForceVideoCopy never transcodes the video (Jellyfin bridge "Disable
	// Video Transcoding"): it is copied whatever the client declared, and a
	// session that cannot be copied fails instead of falling back.
	// ForceAudioCopy keeps every audio track as it is ("Disable Audio
	// Transcoding"), also when the video is transcoded.
	ForceVideoCopy bool `json:"forceVideoCopy,omitempty"`
	ForceAudioCopy bool `json:"forceAudioCopy,omitempty"`
	// WebFallback (the /watch web player): a web-stable session whose video
	// cannot be copied plays as an ordinary web session (its own transcode,
	// seeks by new sessions) instead of the stable compatibility transcode.
	WebFallback bool `json:"webFallback,omitempty"`
}

func (c *HLSClientCaps) forceVideoCopy() bool { return c != nil && c.ForceVideoCopy }
func (c *HLSClientCaps) webFallback() bool    { return c != nil && c.WebFallback }
func (c *HLSClientCaps) forceAudioCopy() bool { return c != nil && c.ForceAudioCopy }

type hlsClientCapsKey struct{}

// parseHLSClientCaps reads the clientCaps query value (JSON); nil when absent
// or invalid.
func parseHLSClientCaps(raw string) *HLSClientCaps {
	if strings.TrimSpace(raw) == "" || len(raw) > 16<<10 {
		return nil
	}
	var caps HLSClientCaps
	if json.Unmarshal([]byte(raw), &caps) != nil {
		return nil
	}
	norm := func(list []string) []string {
		out := make([]string, 0, len(list))
		for _, v := range list {
			if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	caps.TSVideo, caps.TSAudio = norm(caps.TSVideo), norm(caps.TSAudio)
	caps.FMP4Video, caps.FMP4Audio = norm(caps.FMP4Video), norm(caps.FMP4Audio)
	ranges := map[string][]string{}
	for codec, list := range caps.VideoRanges {
		ranges[strings.ToLower(strings.TrimSpace(codec))] = list
	}
	caps.VideoRanges = ranges
	return &caps
}

func withHLSClientCaps(ctx context.Context, caps *HLSClientCaps) context.Context {
	if caps == nil {
		return ctx
	}
	return context.WithValue(ctx, hlsClientCapsKey{}, caps)
}

func hlsClientCapsFrom(ctx context.Context) *HLSClientCaps {
	caps, _ := ctx.Value(hlsClientCapsKey{}).(*HLSClientCaps)
	return caps
}

func containsString(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func (c *HLSClientCaps) rangeAllowed(codec, rangeType string) bool {
	if rangeType == "SDR" {
		return true
	}
	if c == nil {
		return false
	}
	for _, r := range c.VideoRanges[codec] {
		if strings.EqualFold(r, rangeType) {
			return true
		}
	}
	return false
}

// Dolby Vision handling of a copy plan.
const (
	dvNone  = ""      // no Dolby Vision in the source
	dvKeep  = "keep"  // copy with RPU and dvcC (fMP4 only)
	dvStrip = "strip" // remove the RPU; the base layer plays as HDR10/HLG/SDR
)

// stableCopyPlan is how a stable copy session copies its video.
type stableCopyPlan struct {
	Codec     string // h264 | hevc | av1
	RangeType string // Jellyfin VideoRangeType of the source
	FMP4      bool   // fMP4 segments (else MPEG-TS)
	Tag       string // -tag:v for fMP4: avc1 | hvc1 | dvh1 | av01
	DV        string // dvNone | dvKeep | dvStrip
	// ColorFix sets HEVC VUI colour signalling for HDR output: "pq" or "hlg".
	ColorFix string
	// OutputRange is what the client gets: SDR, PQ or HLG (HLS VIDEO-RANGE).
	OutputRange string
	// RebuildConfig converts HEVC to Annex B before muxing, so the fMP4
	// muxer builds the hvcC from the in-band parameter sets: the source's
	// own hvcC has none, and a copy would get an empty hvcC that browsers
	// refuse (no codec string, no decoder configuration).
	RebuildConfig bool
}

// sourceVideoCodec normalizes the probe's codec name.
func sourceVideoCodec(probe *UnifiedProbeResult) string {
	if probe == nil {
		return ""
	}
	switch c := strings.ToLower(strings.TrimSpace(probe.VideoCodec)); c {
	case "h264", "avc", "avc1":
		return "h264"
	case "hevc", "h265":
		return "hevc"
	case "av1":
		return "av1"
	default:
		return c
	}
}

// dvProfileNumber reads the profile from "dvhe.08.06" / "dvav.09.05".
func dvProfileNumber(dvProfile string) int {
	parts := strings.Split(dvProfile, ".")
	if len(parts) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(parts[1])
	return n
}

// sourceRangeType classifies the source the way Jellyfin's VideoRangeType
// does. dvProfile is the "dvhe.PP.LL" string when the source has Dolby Vision.
func sourceRangeType(probe *UnifiedProbeResult, hasDV bool, dvProfile string, hasHDR bool) string {
	transfer := ""
	if probe != nil {
		transfer = strings.ToLower(strings.TrimSpace(probe.ColorTransfer))
	}
	hlg := transfer == "arib-std-b67"
	pq := transfer == "smpte2084" || hasHDR
	if hasDV {
		profile := dvProfileNumber(dvProfile)
		compat := -1
		if probe != nil && probe.DolbyVisionConfiguration != nil {
			if profile == 0 {
				profile = probe.DolbyVisionConfiguration.Profile
			}
			compat = probe.DolbyVisionConfiguration.BLSignalCompatibilityID
		}
		switch {
		case profile == 5:
			return "DOVI"
		case profile == 7:
			return "DOVIWithEL"
		case profile == 8 || profile == 10:
			switch {
			case compat == 1 || (compat < 0 && pq):
				return "DOVIWithHDR10"
			case compat == 4 || (compat < 0 && hlg):
				return "DOVIWithHLG"
			case compat == 2 || (compat < 0 && !pq && !hlg):
				return "DOVIWithSDR"
			}
		}
		return "DOVIInvalid"
	}
	switch {
	case hlg:
		return "HLG"
	case pq:
		return "HDR10"
	}
	return "SDR"
}

// dvBaseRange is the base layer a Dolby Vision source falls back to.
func dvBaseRange(rangeType string) string {
	switch rangeType {
	case "DOVIWithHDR10":
		return "HDR10"
	case "DOVIWithHLG":
		return "HLG"
	case "DOVIWithSDR":
		return "SDR"
	}
	return ""
}

func hlsVideoRange(rangeType string) string {
	switch rangeType {
	case "HDR10", "HDR10Plus", "DOVIWithHDR10", "DOVI":
		return "PQ"
	case "HLG", "DOVIWithHLG":
		return "HLG"
	}
	return "SDR"
}

// copyCompatiblePixFmt reports whether the source's pixel format is one a
// browser/TV decoder plays for the codec (4:2:0, 8 or 10 bit).
func copyCompatiblePixFmt(codec string, probe *UnifiedProbeResult) bool {
	if probe == nil {
		return false
	}
	pixFmt := strings.ToLower(strings.TrimSpace(probe.VideoPixFmt))
	profile := strings.ToLower(strings.TrimSpace(probe.VideoProfile))
	switch codec {
	case "h264":
		return isBrowserCopyCompatibleVideo(probe)
	case "hevc", "av1":
		if strings.Contains(profile, "4:2:2") || strings.Contains(profile, "4:4:4") || strings.Contains(profile, "rext") {
			return false
		}
		return pixFmt == "" || pixFmt == "yuv420p" || pixFmt == "yuv420p10le"
	}
	return false
}

// planStableCopy decides whether a web-stable session can copy its video for
// the client, and how. Without caps (a client that declared nothing) only
// browser-safe H.264 SDR is copied, into MPEG-TS, as before.
func planStableCopy(probe *UnifiedProbeResult, hasDV bool, dvProfile string, hasHDR bool, caps *HLSClientCaps) (stableCopyPlan, bool) {
	codec := sourceVideoCodec(probe)
	rangeType := sourceRangeType(probe, hasDV, dvProfile, hasHDR)
	plan := stableCopyPlan{Codec: codec, RangeType: rangeType, RebuildConfig: codec == "hevc" && probe != nil && probe.VideoParamSetsMissing}
	if caps.forceVideoCopy() {
		return planForcedCopy(plan, caps), codec != ""
	}
	if !copyCompatiblePixFmt(codec, probe) {
		return plan, false
	}
	if caps != nil && caps.NoVideoCopy {
		return plan, false
	}
	if caps == nil {
		if codec == "h264" && rangeType == "SDR" {
			plan.OutputRange = "SDR"
			return plan, true
		}
		return plan, false
	}
	inTS, inFMP4 := containsString(caps.TSVideo, codec), containsString(caps.FMP4Video, codec)
	if codec == "h264" && len(caps.TSVideo) == 0 && len(caps.FMP4Video) == 0 {
		inTS = true // an older bridge or client that listed no containers
	}
	if !inTS && !inFMP4 {
		return plan, false
	}

	output := rangeType
	switch {
	case strings.HasPrefix(rangeType, "DOVI"):
		if rangeType == "DOVIWithEL" || rangeType == "DOVIInvalid" {
			return plan, false // dual layer / unknown: transcode
		}
		if codec == "hevc" && inFMP4 && caps.rangeAllowed(codec, rangeType) {
			plan.DV = dvKeep // Dolby Vision signalling (dvcC) exists only in fMP4
		} else if base := dvBaseRange(rangeType); base != "" && caps.rangeAllowed(codec, base) {
			plan.DV, output = dvStrip, base
		} else {
			return plan, false
		}
	case rangeType != "SDR":
		if !caps.rangeAllowed(codec, rangeType) {
			return plan, false
		}
	}

	// H.264 SDR keeps MPEG-TS where possible (proven path); everything else
	// prefers fMP4, which browsers need for HEVC/AV1 and Dolby Vision needs
	// for its config box.
	plan.FMP4 = inFMP4 && (!inTS || codec != "h264" || plan.DV == dvKeep)
	if plan.DV == dvKeep && !plan.FMP4 {
		return plan, false
	}
	switch {
	case !plan.FMP4:
	case plan.DV == dvKeep:
		plan.Tag = "dvh1"
	case codec == "hevc":
		plan.Tag = "hvc1"
	case codec == "av1":
		plan.Tag = "av01"
	default:
		plan.Tag = "avc1"
	}
	plan.OutputRange = hlsVideoRange(output)
	if codec == "hevc" && plan.DV != dvKeep {
		switch plan.OutputRange {
		case "PQ":
			plan.ColorFix = "pq"
		case "HLG":
			plan.ColorFix = "hlg"
		}
	}
	return plan, true
}

// planForcedCopy copies whatever the source is when transcoding is
// disabled: the declared capabilities only pick the container and how much
// of Dolby Vision survives; nothing is refused.
func planForcedCopy(plan stableCopyPlan, caps *HLSClientCaps) stableCopyPlan {
	codec, rangeType := plan.Codec, plan.RangeType
	inTS, inFMP4 := containsString(caps.TSVideo, codec), containsString(caps.FMP4Video, codec)
	// Same container preference as a normal copy; without a declaration,
	// H.264/HEVC go into MPEG-TS and anything else into fMP4.
	switch {
	case inTS && (codec == "h264" || !inFMP4):
		plan.FMP4 = false
	case inFMP4:
		plan.FMP4 = true
	default:
		plan.FMP4 = codec != "h264" && codec != "hevc"
	}
	output := rangeType
	if strings.HasPrefix(rangeType, "DOVI") {
		base := dvBaseRange(rangeType)
		switch {
		case codec == "hevc" && plan.FMP4 && caps.rangeAllowed(codec, rangeType):
			plan.DV = dvKeep
		case base != "":
			plan.DV, output = dvStrip, base // the RPU is dropped, the base layer stays as is
		case codec == "hevc":
			plan.FMP4, plan.DV = true, dvKeep // profile 5/7: no base layer to fall back to
		}
	}
	switch {
	case !plan.FMP4:
	case plan.DV == dvKeep:
		plan.Tag = "dvh1"
	case codec == "hevc":
		plan.Tag = "hvc1"
	case codec == "av1":
		plan.Tag = "av01"
	case codec == "h264":
		plan.Tag = "avc1"
	}
	plan.OutputRange = hlsVideoRange(output)
	if codec == "hevc" && plan.DV != dvKeep {
		switch plan.OutputRange {
		case "PQ":
			plan.ColorFix = "pq"
		case "HLG":
			plan.ColorFix = "hlg"
		}
	}
	return plan
}

// videoCopyArgs are the FFmpeg output options that copy the video per plan.
// A filter that failed on this source before (the session restarted with
// DVDisabled or HDRMetadataDisabled set) is left out.
func (p stableCopyPlan) videoCopyArgs(dvFilterFailed, colorFilterFailed bool) []string {
	args := []string{"-c:v", "copy"}
	var bsfs []string
	if p.RebuildConfig && p.FMP4 {
		// First, so the filters below see the parameter sets too.
		bsfs = append(bsfs, "hevc_mp4toannexb")
	}
	if p.DV == dvStrip && !dvFilterFailed {
		bsfs = append(bsfs, "dovi_rpu=strip=1")
	}
	colorFix := p.ColorFix
	if colorFilterFailed {
		colorFix = ""
	}
	switch colorFix {
	case "pq":
		bsfs = append(bsfs, "hevc_metadata=colour_primaries=9:transfer_characteristics=16:matrix_coefficients=9")
	case "hlg":
		bsfs = append(bsfs, "hevc_metadata=colour_primaries=9:transfer_characteristics=18:matrix_coefficients=9")
	}
	if len(bsfs) > 0 {
		args = append(args, "-bsf:v", strings.Join(bsfs, ","))
	}
	if p.Tag != "" {
		args = append(args, "-tag:v", p.Tag)
	}
	if p.DV == dvKeep {
		args = append(args, "-strict", "unofficial")
	}
	return args
}

// copyAudioCodecs is what the client plays as-is in the plan's container.
func (p stableCopyPlan) copyAudioCodecs(caps *HLSClientCaps) []string {
	if caps == nil {
		return nil
	}
	if p.FMP4 {
		return caps.FMP4Audio
	}
	return caps.TSAudio
}

// stableCopyEligible reports whether a web-stable session can copy its video
// for this client (see planStableCopy).
func stableCopyEligible(probe *UnifiedProbeResult, hasDV bool, dvProfile string, hasHDR bool, duration float64, caps *HLSClientCaps) (stableCopyPlan, bool) {
	if duration <= 0 {
		return stableCopyPlan{}, false
	}
	return planStableCopy(probe, hasDV, dvProfile, hasHDR, caps)
}

// passesAudio reports whether a stable copy session's client plays codec
// unconverted in the session's container.
func (session *HLSSession) passesAudio(codec string) bool {
	if session.ClientCaps.forceAudioCopy() {
		return true
	}
	return containsString(session.CopyPlan.copyAudioCodecs(session.ClientCaps), codec)
}

// audioTranscodingDisabled reports whether the session must keep its audio
// as it is (Jellyfin bridge "Disable Audio Transcoding").
func (session *HLSSession) audioTranscodingDisabled() bool {
	return session != nil && session.ClientCaps.forceAudioCopy()
}

// errTranscodingDisabled marks a session refused because transcoding is
// disabled and the source cannot be copied (no keyframe index, no video).
var errTranscodingDisabled = errors.New("transcoding disabled")

// hevcConfigHeaderSize is the size of an hvcC record without parameter set
// arrays (ISO/IEC 14496-15): a decoder configuration that carries no
// VPS/SPS/PPS.
const hevcConfigHeaderSize = 23

// hevcParamSetsMissing reports an HEVC stream whose decoder configuration
// (ffprobe's extradata_size) carries no parameter sets. An unknown size
// counts as complete.
func hevcParamSetsMissing(codec string, extradataSize *int) bool {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "hevc", "h265":
		return extradataSize != nil && *extradataSize <= hevcConfigHeaderSize
	}
	return false
}
