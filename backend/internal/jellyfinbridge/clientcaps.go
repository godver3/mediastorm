package jellyfinbridge

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"time"

	"novastream/services/playback"
)

// hlsClientCaps mirrors mediastorm's HLSClientCaps: what a client plays
// inside HLS, read from its DeviceProfile. mediastorm decides from it (and
// the source) whether a stable copy session copies the video, into MPEG-TS
// or fMP4, and whether the audio stays.
type hlsClientCaps struct {
	TSVideo          []string            `json:"tsVideo,omitempty"`
	TSAudio          []string            `json:"tsAudio,omitempty"`
	FMP4Video        []string            `json:"fmp4Video,omitempty"`
	FMP4Audio        []string            `json:"fmp4Audio,omitempty"`
	VideoRanges      map[string][]string `json:"videoRanges,omitempty"`
	MaxAudioChannels int                 `json:"maxAudioChannels,omitempty"`
	// NoVideoCopy makes mediastorm transcode even H.264 (after a failure).
	NoVideoCopy bool `json:"noVideoCopy,omitempty"`
	// ForceVideoCopy / ForceAudioCopy: Disable Video / Audio Transcoding.
	ForceVideoCopy bool `json:"forceVideoCopy,omitempty"`
	ForceAudioCopy bool `json:"forceAudioCopy,omitempty"`
}

func splitCodecs(list string) []string {
	var out []string
	for _, c := range strings.Split(list, ",") {
		if c = strings.ToLower(strings.TrimSpace(c)); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// capsFromProfile reads the HLS transcoding profiles (MPEG-TS and fMP4) and
// the per-codec VideoRangeType conditions of a DeviceProfile. jellyfin-web
// lists exactly what its player decodes there: HEVC in fMP4 when the browser
// decodes HEVC, AC-3/E-AC-3 where the player handles them, HDR10/HLG/Dolby
// Vision per codec where the platform displays them.
func capsFromProfile(raw json.RawMessage) *hlsClientCaps {
	var profile struct {
		TranscodingProfiles []struct {
			Container        string `json:"Container"`
			Type             string `json:"Type"`
			Protocol         string `json:"Protocol"`
			AudioCodec       string `json:"AudioCodec"`
			VideoCodec       string `json:"VideoCodec"`
			MaxAudioChannels string `json:"MaxAudioChannels"`
		} `json:"TranscodingProfiles"`
		CodecProfiles []struct {
			Type       string `json:"Type"`
			Codec      string `json:"Codec"`
			Conditions []struct {
				Condition string `json:"Condition"`
				Property  string `json:"Property"`
				Value     string `json:"Value"`
			} `json:"Conditions"`
		} `json:"CodecProfiles"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &profile) != nil {
		return nil
	}
	caps := &hlsClientCaps{VideoRanges: map[string][]string{}}
	found := false
	for _, tp := range profile.TranscodingProfiles {
		if !strings.EqualFold(tp.Protocol, "hls") || (tp.Type != "" && !strings.EqualFold(tp.Type, "Video")) {
			continue
		}
		switch strings.ToLower(tp.Container) {
		case "ts", "mpegts":
			caps.TSVideo, caps.TSAudio = splitCodecs(tp.VideoCodec), splitCodecs(tp.AudioCodec)
		case "mp4", "fmp4":
			caps.FMP4Video, caps.FMP4Audio = splitCodecs(tp.VideoCodec), splitCodecs(tp.AudioCodec)
		default:
			continue
		}
		found = true
		if n, err := strconv.Atoi(strings.TrimSpace(tp.MaxAudioChannels)); err == nil && n > caps.MaxAudioChannels {
			caps.MaxAudioChannels = n
		}
	}
	for _, cp := range profile.CodecProfiles {
		if cp.Type != "" && !strings.EqualFold(cp.Type, "Video") {
			continue
		}
		for _, cond := range cp.Conditions {
			if !strings.EqualFold(cond.Property, "VideoRangeType") || !strings.EqualFold(cond.Condition, "EqualsAny") {
				continue
			}
			var ranges []string
			for _, r := range strings.Split(cond.Value, "|") {
				if r = strings.TrimSpace(r); r != "" {
					ranges = append(ranges, r)
				}
			}
			for _, codec := range splitCodecs(cp.Codec) {
				caps.VideoRanges[codec] = ranges
			}
		}
	}
	if !found {
		return nil
	}
	return caps
}

// restrict applies the bridge settings and the selected audio track to the
// caps: codecs the admin disabled are dropped, HDR and Dolby Vision ranges
// likewise, and audio other than AAC is dropped when the track has more
// channels than the client takes.
func (c *hlsClientCaps) restrict(copyHEVC, copyHDR, copyDV bool, audioChannels int) {
	if c == nil {
		return
	}
	dropVideo := func(list []string) []string {
		out := list[:0:0]
		for _, codec := range list {
			if !copyHEVC && (codec == "hevc" || codec == "av1") {
				continue
			}
			out = append(out, codec)
		}
		return out
	}
	c.TSVideo, c.FMP4Video = dropVideo(c.TSVideo), dropVideo(c.FMP4Video)
	for codec, ranges := range c.VideoRanges {
		out := ranges[:0:0]
		for _, r := range ranges {
			isDV := strings.HasPrefix(r, "DOVI")
			if (isDV && !copyDV) || (r != "SDR" && !isDV && !copyHDR) {
				continue
			}
			out = append(out, r)
		}
		c.VideoRanges[codec] = out
	}
	if c.MaxAudioChannels > 0 && audioChannels > c.MaxAudioChannels {
		aacOnly := func(list []string) []string {
			if containsFold(list, "aac") {
				return []string{"aac"}
			}
			return nil
		}
		c.TSAudio, c.FMP4Audio = aacOnly(c.TSAudio), aacOnly(c.FMP4Audio)
	}
}

// selectedAudioChannels is the channel count of the track playback uses.
func (sess *playSession) selectedAudioChannels(status *playback.PrequeueStatusResponse, audio int) int {
	sess.mu.Lock()
	probe := sess.probe
	sess.mu.Unlock()
	if probe == nil || status == nil {
		return 0
	}
	if audio < 0 {
		audio = status.SelectedAudioTrack
	}
	for _, a := range probe.AudioStreams {
		if a.Index == audio || (audio < 0 && a.Channels > 0) {
			return a.Channels
		}
	}
	return 0
}

// clientCapsParam is the clientCaps value for an HLS start, or "" to let
// mediastorm fall back to its defaults (H.264 copy, everything else
// transcoded). A play session that failed with a copy, or a request that
// disallowed stream copy, gets no caps.
func (s *Server) clientCapsParam(sess *playSession, status *playback.PrequeueStatusResponse, audio int) string {
	sess.mu.Lock()
	caps, failed := sess.caps, sess.copyFailed
	sess.mu.Unlock()
	cfg := s.settings()
	forceVideo, forceAudio := cfg.DisableVideoTranscoding, cfg.DisableAudioTranscoding
	if !forceVideo && !forceAudio {
		if failed {
			return `{"noVideoCopy":true}`
		}
		if caps == nil {
			return ""
		}
	}
	if caps == nil {
		caps = &hlsClientCaps{}
	}
	copied := *caps
	copied.VideoRanges = map[string][]string{}
	for codec, ranges := range caps.VideoRanges {
		copied.VideoRanges[codec] = append([]string(nil), ranges...)
	}
	copied.restrict(cfg.CopyHEVC, cfg.CopyHDR, cfg.CopyDolbyVision, sess.selectedAudioChannels(status, audio))
	// Video copy that cannot fall back to a transcode ignores a failure; the
	// audio setting applies either way.
	copied.ForceVideoCopy, copied.ForceAudioCopy = forceVideo, forceAudio
	copied.NoVideoCopy = failed && !forceVideo
	raw, err := json.Marshal(copied)
	if err != nil {
		return ""
	}
	return string(raw)
}

// copyFailureTTL is how long a failed stream copy makes the same profile,
// device and item transcode instead.
const copyFailureTTL = 6 * time.Hour

func copyFailureKey(p *principal, sess *playSession) string {
	return p.Profile.ID + "|" + p.Client.DeviceID + "|" + sess.ItemID
}

// applyClientCaps records what the client plays inside HLS for the play
// session, and whether stream copy is off for it: the request disallowed
// it (clients retry that way after an error), or a copy of this item
// failed on this device recently.
func (s *Server) applyClientCaps(p *principal, sess *playSession, req playbackInfoRequest) {
	caps := capsFromProfile(req.DeviceProfile)
	failed := req.AllowVideoCopy != nil && !*req.AllowVideoCopy
	if at, ok := s.copyFailures.Load(copyFailureKey(p, sess)); ok && time.Since(at.(time.Time)) < copyFailureTTL {
		failed = true
	}
	s.rememberDeviceCaps(p, caps)
	sess.mu.Lock()
	if caps != nil {
		sess.caps = caps
	}
	if failed {
		sess.copyFailed = true
	}
	sess.mu.Unlock()
}

// noteCopyFailure remembers a playback failure of a copied stream.
func (s *Server) noteCopyFailure(p *principal, sess *playSession) {
	sess.mu.Lock()
	hlsID := sess.currentHLS
	sess.mu.Unlock()
	if hlsID == "" || s.deps.StreamPlan == nil {
		return
	}
	if plan, ok := s.deps.StreamPlan(hlsID); ok && plan.VideoCopied {
		s.copyFailures.Store(copyFailureKey(p, sess), time.Now())
		log.Printf("[jellyfin-bridge] copied stream of %s failed on %s; transcoding next time", sess.ItemID, p.Client.Device)
	}
}

// dvRangeType is Jellyfin's VideoRangeType for a Dolby Vision source: the
// profile and base-layer compatibility decide the fallback clients can use.
func dvRangeType(status *playback.PrequeueStatusResponse) string {
	profile, compat := 0, -1
	if parts := strings.Split(status.DolbyVisionProfile, "."); len(parts) >= 2 {
		profile, _ = strconv.Atoi(parts[1])
	}
	if cfg := status.DolbyVisionConfiguration; cfg != nil {
		if profile == 0 {
			profile = cfg.Profile
		}
		compat = cfg.BLSignalCompatibilityID
	}
	switch {
	case profile == 5:
		return "DOVI"
	case profile == 7:
		return "DOVIWithEL"
	case (profile == 8 || profile == 10) && (compat == 1 || (compat < 0 && status.HasHDR10)):
		return "DOVIWithHDR10"
	case (profile == 8 || profile == 10) && compat == 4:
		return "DOVIWithHLG"
	case (profile == 8 || profile == 10) && compat == 2:
		return "DOVIWithSDR"
	}
	return "DOVI"
}

// liveTimeshift reports whether live channels tune with mediastorm's timeshift
// window (Live TV setting), so Jellyfin players can pause and rewind inside
// it: the HLS playlist then lists the window, which jellyfin-web, ExoPlayer
// and AVPlayer seek in. With timeshift off in mediastorm the tune is an
// ordinary live session. Not with both transcoding settings disabled: a
// timeshift session re-encodes copied live audio to AAC, and that pass-through
// promises the channel's own audio.
func (s *Server) liveTimeshift() bool {
	cfg := s.settings()
	return !(cfg.DisableVideoTranscoding && cfg.DisableAudioTranscoding)
}

// liveTarget is the mediastorm playback target for Live TV: browsers get the
// H.264/AAC compatibility encode, unless video transcoding is disabled, in
// which case channels are passed through as received (native transmux, the
// audio included: the live pipeline converts both or neither).
func (s *Server) liveTarget() string {
	if s.settings().DisableVideoTranscoding {
		return "native"
	}
	return "web"
}
