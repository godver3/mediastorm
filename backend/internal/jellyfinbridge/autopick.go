package jellyfinbridge

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"novastream/services/playback"
	"novastream/utils/parsett"
)

// Auto version without video transcoding: when the stream mediastorm picks
// would have its video transcoded for the client (codec, HDR or Dolby Vision
// the client does not declare), the automatic version walks down
// mediastorm's ranking to the first release the client takes as it is.
const (
	autoPickTTL      = time.Hour
	autoPickAttempts = 3 // releases resolved after the first choice
	autoPickBudget   = 45 * time.Second
	autoPrequeueWait = 90 * time.Second
	autoReleaseWait  = 20 * time.Second
)

type autoPick struct {
	done   chan struct{}
	status *playback.PrequeueStatusResponse
	probe  *mediaProbe
	at     time.Time
	gen    uint64 // ranking generation it was picked under
}

type autoPicks struct {
	mu     sync.Mutex
	byKey  map[string]*autoPick      // profile|device|item|preset
	device map[string]*hlsClientCaps // profile|device -> last declared caps
}

func newAutoPicks() *autoPicks {
	return &autoPicks{byKey: map[string]*autoPick{}, device: map[string]*hlsClientCaps{}}
}

func deviceKey(p *principal) string { return p.Profile.ID + "|" + p.Client.DeviceID }

// rememberDeviceCaps keeps a device's declared caps, so a details page can
// pick a release for it before it sends its next DeviceProfile.
func (s *Server) rememberDeviceCaps(p *principal, caps *hlsClientCaps) {
	if caps == nil {
		return
	}
	s.autoPicks.mu.Lock()
	s.autoPicks.device[deviceKey(p)] = caps
	s.autoPicks.mu.Unlock()
}

// autoCaps returns the caps the automatic version must satisfy for this
// client (with the bridge's pass-through settings applied), or nil when
// avoiding video transcoding does not apply.
func (s *Server) autoCaps(p *principal, itemID string, profile *hlsClientCaps) *hlsClientCaps {
	cfg := s.settings()
	if !cfg.AutoAvoidVideoTranscoding || cfg.DisableVideoTranscoding || s.deps.Handlers.IndexerSearch == nil || s.deps.Handlers.ProbeVideo == nil {
		return nil
	}
	// A copy that failed on this device transcodes anyway.
	if at, ok := s.copyFailures.Load(p.Profile.ID + "|" + p.Client.DeviceID + "|" + itemID); ok && time.Since(at.(time.Time)) < copyFailureTTL {
		return nil
	}
	if profile == nil {
		s.autoPicks.mu.Lock()
		profile = s.autoPicks.device[deviceKey(p)]
		s.autoPicks.mu.Unlock()
	}
	if profile == nil {
		return nil
	}
	caps := *profile
	caps.VideoRanges = map[string][]string{}
	for codec, ranges := range profile.VideoRanges {
		caps.VideoRanges[codec] = append([]string(nil), ranges...)
	}
	caps.restrict(cfg.CopyHEVC, cfg.CopyHDR, cfg.CopyDolbyVision, 0)
	return &caps
}

// pickAuto returns the stream for the automatic version: mediastorm's own
// choice, or the best-ranked release whose video the client can copy.
// Concurrent calls for the same profile, device, item and preset share one
// pick; presetID is a quality preset picked as version ("" = the profile's).
func (s *Server) pickAuto(ctx context.Context, p *principal, itemID string, ref itemRef, caps *hlsClientCaps, presetID string) (*playback.PrequeueStatusResponse, *mediaProbe) {
	key := deviceKey(p) + "|" + itemID + "|" + presetID
	s.autoPicks.mu.Lock()
	for k, old := range s.autoPicks.byKey {
		if !old.at.IsZero() && time.Since(old.at) > autoPickTTL {
			delete(s.autoPicks.byKey, k)
		}
	}
	gen := s.rankingGeneration()
	pick, ok := s.autoPicks.byKey[key]
	if ok && pick.gen != gen {
		ok = false // ranked under older settings or presets
	}
	if !ok {
		pick = &autoPick{done: make(chan struct{}), gen: gen}
		s.autoPicks.byKey[key] = pick
		go s.runAutoPick(p, itemID, ref, caps, presetID, key, pick)
	}
	s.autoPicks.mu.Unlock()
	select {
	case <-pick.done:
		return pick.status, pick.probe
	case <-ctx.Done():
		return nil, nil
	}
}

// rankingGeneration is mediastorm's ranking generation (0 when unknown).
func (s *Server) rankingGeneration() uint64 {
	if s.deps.RankingGeneration == nil {
		return 0
	}
	return s.deps.RankingGeneration()
}

func (s *Server) runAutoPick(p *principal, itemID string, ref itemRef, caps *hlsClientCaps, presetID, key string, pick *autoPick) {
	ctx, cancel := context.WithTimeout(context.Background(), autoPrequeueWait+autoPickBudget)
	defer cancel()
	status, probe := s.autoPickStream(ctx, p, itemID, ref, caps, presetID)
	if probe != nil {
		// The picked release's length, not that of a candidate probed last.
		s.rememberDuration(p.Profile.ID, itemID, probe.DurationSeconds)
	}
	s.autoPicks.mu.Lock()
	pick.status, pick.probe, pick.at = status, probe, time.Now()
	if (status == nil || status.Status != playback.PrequeueStatusReady) && s.autoPicks.byKey[key] == pick {
		delete(s.autoPicks.byKey, key) // try again next time
	}
	s.autoPicks.mu.Unlock()
	close(pick.done)
}

func (s *Server) autoPickStream(ctx context.Context, p *principal, itemID string, ref itemRef, caps *hlsClientCaps, presetID string) (*playback.PrequeueStatusResponse, *mediaProbe) {
	prequeueID, err := s.startPrequeue(ctx, p, ref, presetID)
	if err != nil {
		return &playback.PrequeueStatusResponse{Status: playback.PrequeueStatusFailed, Error: err.Error()}, nil
	}
	scratch := &playSession{ItemID: itemID, ProfileID: p.Profile.ID, prequeueID: prequeueID}
	first := s.awaitPrequeue(ctx, p, scratch, autoPrequeueWait)
	if first == nil || first.Status != playback.PrequeueStatusReady {
		return first, nil
	}
	firstProbe := s.probeMedia(ctx, p, scratch, first)
	if strings.HasPrefix(first.StreamPath, "localmedia:") {
		// The library copy plays as it is: no walk down releases to avoid
		// transcoding it.
		return first, firstProbe
	}
	if firstProbe == nil || videoCopyable(caps, first, firstProbe) {
		return first, firstProbe
	}

	deadline := time.Now().Add(autoPickBudget)
	budget, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	titleID := ""
	if req, err := s.prequeueRequest(budget, p, ref); err == nil {
		titleID = req.TitleID
	}
	attempts := 0
	for _, r := range s.releasesWait(p, itemID, ref, autoReleaseWait, presetID) {
		if attempts == autoPickAttempts || time.Now().After(deadline) {
			break
		}
		if sameRelease(r.Title, first) || !titleMayCopy(caps, r.Parsed) {
			continue
		}
		attempts++
		try := &playSession{ItemID: itemID, ProfileID: p.Profile.ID}
		status, err := s.resolveReleaseStatus(budget, p, try, r, titleID)
		if err != nil {
			log.Printf("[jellyfin-bridge] auto: release %q for %s: %v", r.Title, itemID, err)
			continue
		}
		try.mu.Lock()
		probe := try.probe
		try.mu.Unlock()
		if probe != nil && videoCopyable(caps, status, probe) {
			s.rememberChapters(p.Profile.ID, itemID, probe)
			log.Printf("[jellyfin-bridge] auto: %q needs video transcoding on %s; playing %q instead", first.DisplayName, p.Client.Device, r.Title)
			return status, probe
		}
	}
	log.Printf("[jellyfin-bridge] auto: no release of %s plays on %s without video transcoding", itemID, p.Client.Device)
	return first, firstProbe
}

func sameRelease(title string, status *playback.PrequeueStatusResponse) bool {
	name := strings.TrimSpace(status.DisplayName)
	return name != "" && (strings.EqualFold(title, name) || strings.HasPrefix(strings.ToLower(name), strings.ToLower(title)+"."))
}

// videoCopyable mirrors mediastorm's stable copy plan (handlers
// planStableCopy): whether a web-stable session copies this stream's video
// for these caps.
func videoCopyable(caps *hlsClientCaps, status *playback.PrequeueStatusResponse, probe *mediaProbe) bool {
	if probe == nil || len(probe.VideoStreams) == 0 {
		return false
	}
	v := probe.VideoStreams[0]
	codec := strings.ToLower(v.CodecName)
	pixFmt, profile := strings.ToLower(v.PixFmt), strings.ToLower(v.Profile)
	switch codec {
	case "h264":
		if (pixFmt != "" && pixFmt != "yuv420p") || strings.Contains(profile, "10") || strings.Contains(profile, "4:2:2") || strings.Contains(profile, "4:4:4") {
			return false
		}
	case "hevc", "av1":
		if strings.Contains(profile, "4:2:2") || strings.Contains(profile, "4:4:4") || strings.Contains(profile, "rext") || (pixFmt != "" && pixFmt != "yuv420p" && pixFmt != "yuv420p10le") {
			return false
		}
	default:
		return false
	}
	inTS, inFMP4 := containsFold(caps.TSVideo, codec), containsFold(caps.FMP4Video, codec)
	if !inTS && !inFMP4 {
		return false
	}
	switch rangeType := streamRangeType(status, probe); {
	case rangeType == "SDR":
		return true
	case strings.HasPrefix(rangeType, "DOVI"):
		if rangeType == "DOVIWithEL" {
			return false
		}
		if codec == "hevc" && inFMP4 && rangeAllowed(caps, codec, rangeType) {
			return true
		}
		switch rangeType {
		case "DOVIWithHDR10":
			return rangeAllowed(caps, codec, "HDR10")
		case "DOVIWithHLG":
			return rangeAllowed(caps, codec, "HLG")
		case "DOVIWithSDR":
			return true
		}
		return false
	default:
		return rangeAllowed(caps, codec, rangeType)
	}
}

func streamRangeType(status *playback.PrequeueStatusResponse, probe *mediaProbe) string {
	switch {
	case status.HasDolbyVision:
		return dvRangeType(status)
	case status.HasHDR10:
		return "HDR10"
	}
	if transfer := strings.ToLower(probe.VideoStreams[0].ColorTransfer); transfer == "smpte2084" {
		return "HDR10"
	} else if transfer == "arib-std-b67" {
		return "HLG"
	}
	return "SDR"
}

func rangeAllowed(caps *hlsClientCaps, codec, rangeType string) bool {
	return containsFold(caps.VideoRanges[codec], rangeType)
}

// titleMayCopy rules out, from the release name alone, releases whose video
// the client surely cannot copy; the probe decides the rest.
func titleMayCopy(caps *hlsClientCaps, parsed *parsett.ParsedTitle) bool {
	if parsed == nil {
		return true
	}
	codec := ""
	switch strings.ToLower(parsed.Codec) {
	case "":
		return true
	case "avc":
		codec = "h264"
	case "hevc":
		codec = "hevc"
	case "av1":
		codec = "av1"
	default:
		return false // MPEG-2, Xvid...: always transcoded
	}
	if !containsFold(caps.TSVideo, codec) && !containsFold(caps.FMP4Video, codec) {
		return false
	}
	dv, hdr10, hlg := false, false, false
	for _, h := range parsed.HDR {
		switch strings.ToUpper(h) {
		case "DV":
			dv = true
		case "HDR", "HDR10", "HDR10+":
			hdr10 = true
		case "HLG":
			hlg = true
		}
	}
	dvOK := false
	for _, r := range caps.VideoRanges[codec] {
		dvOK = dvOK || strings.HasPrefix(r, "DOVI")
	}
	switch {
	case dv && !hdr10 && !hlg:
		return dvOK // likely profile 5: no base layer to fall back to
	case hdr10:
		return rangeAllowed(caps, codec, "HDR10") || (dv && dvOK)
	case hlg:
		return rangeAllowed(caps, codec, "HLG")
	}
	return true
}
