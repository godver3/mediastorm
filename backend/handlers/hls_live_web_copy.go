package handlers

import (
	"context"
	"errors"
	"io"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// A browser plays H.264 (8-bit 4:2:0) from HLS itself, so a web live session copies such video
// instead of encoding every frame with libx264; only the audio becomes stereo AAC (MSE cannot play
// AC-3, MP2 or multichannel AAC reliably). Anything else, or a source that cannot be checked, is
// encoded as before. The decision is made once per session; a player whose browser still cannot
// decode the copy switches the session to the encode (ForceLiveVideoEncode).

const liveWebCodecCheckTimeout = 10 * time.Second

// liveWebSafeAVCProfiles are the H.264 profile_idc values browsers decode, always 8-bit 4:2:0:
// Baseline, Main and High (not Extended, which MSE rejects).
var liveWebSafeAVCProfiles = map[int]bool{66: true, 77: true, 100: true}

// liveWebSafeProbedProfiles are ffprobe's names for those profiles.
var liveWebSafeProbedProfiles = map[string]bool{"Baseline": true, "Constrained Baseline": true, "Main": true, "High": true}

// avcProfileIDC reads the profile of an avc1/avc3 codec string's rest: "640020" (RFC 6381 hex) or
// Apple's legacy decimal "66.30" / "100.40".
func avcProfileIDC(rest string) (int, bool) {
	if first, _, ok := strings.Cut(rest, "."); ok {
		profile, err := strconv.Atoi(first)
		return profile, err == nil
	}
	if len(rest) < 2 {
		return 0, false
	}
	profile, err := strconv.ParseInt(rest[:2], 16, 0)
	return int(profile), err == nil
}

// browserSafeHLSCodecs reads one variant's CODECS attribute: video is false when it names no
// video codec (an audio-only variant), safe whether that video is H.264 a browser plays.
func browserSafeHLSCodecs(codecs string) (video, safe bool) {
	for _, codec := range strings.Split(codecs, ",") {
		codec = strings.ToLower(strings.TrimSpace(codec))
		kind, rest, _ := strings.Cut(codec, ".")
		switch kind {
		case "avc1", "avc3":
			video = true
			if profile, ok := avcProfileIDC(rest); !ok || !liveWebSafeAVCProfiles[profile] {
				return true, false
			}
			safe = true
		case "mp4a", "ac-3", "ec-3", "ac-4", "opus", "flac", "mp3", "dts", "dtsc", "dtse", "alac", "stpp", "wvtt", "":
		default:
			// Any other codec is taken for video a browser may not play (HEVC, Dolby Vision, AV1, …).
			return true, false
		}
	}
	return video, safe
}

// liveWebMasterVideoCopy decides from a master playlist's CODECS attributes: decided is false for
// a media playlist, when a variant does not name its codecs, or when none names a video codec;
// copy needs every variant with video to be browser-safe H.264, since FFmpeg may read any of them.
func liveWebMasterVideoCopy(playlist string) (decided, copy bool) {
	variants, withVideo := 0, 0
	for _, line := range strings.Split(playlist, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			continue
		}
		variants++
		codecs := strings.TrimSpace(parseHLSAttributes(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))["CODECS"])
		if codecs == "" {
			return false, false
		}
		// An audio-only variant says nothing about the video; the others decide.
		video, safe := browserSafeHLSCodecs(codecs)
		if video && !safe {
			return true, false
		}
		if video {
			withVideo++
		}
	}
	if variants == 0 || withVideo == 0 {
		return false, false
	}
	return true, true
}

// browserSafeProbedVideo reads ffprobe's codec_name,profile,pix_fmt,field_order csv lines (one
// per video stream): every one must be progressive H.264 in a browser profile and 8-bit 4:2:0,
// and there must be one. Interlaced video is encoded (deinterlaced): browsers show copied fields
// combed.
func browserSafeProbedVideo(out string) bool {
	seen := false
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Split(strings.TrimSpace(line), ",")
		if len(fields) < 4 || strings.TrimSpace(fields[0]) == "" {
			continue
		}
		profile := strings.TrimSpace(strings.Join(fields[1:len(fields)-2], ","))
		pixFmt := strings.TrimSpace(fields[len(fields)-2])
		fieldOrder := strings.TrimSpace(fields[len(fields)-1])
		if fields[0] != "h264" || !liveWebSafeProbedProfiles[profile] || (pixFmt != "yuv420p" && pixFmt != "yuvj420p") ||
			fieldOrder != "progressive" {
			return false
		}
		seen = true
	}
	return seen
}

// liveWebVideoCopy decides for a web session whose input FFmpeg reads by URL. The channel URL
// (masterURL, read with masterHeaders) decides when it is a master whose variants all name their
// codecs; otherwise, for an HLS input, ffprobe reads FFmpeg's input (inputArg with inputHeaders).
// A non-HLS input (raw MPEG-TS IPTV) is not probed: that would open a second connection to the
// provider, which breaks providers allowing one, and delay the tune; it is encoded. answered is
// false only when ctx ended (the session is stopping); a probe that fails or finds no video
// stream answers "encode", so a restart does not probe again.
func (m *HLSManager) liveWebVideoCopy(ctx context.Context, sessionID, masterURL string, masterHeaders map[string]string, inputArg string, inputHeaders map[string]string, hlsInput bool) (copy, answered bool) {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, liveWebCodecCheckTimeout)
	defer cancel()
	if !hlsInput {
		log.Printf("[hls] live session %s: web video copy=false (a non-HLS input is not probed)", sessionID)
		return false, true
	}
	if isHTTPURL(masterURL) {
		if client, err := newLiveBacklogClient(); err == nil {
			body, _, err := fetchHLSPlaylist(ctx, client, masterURL, masterHeaders)
			client.CloseIdleConnections()
			if err == nil {
				if decided, copy := liveWebMasterVideoCopy(body); decided {
					log.Printf("[hls] live session %s: web video copy=%v (master playlist codecs)", sessionID, copy)
					return copy, true
				}
			}
		}
	}
	args := []string{"-v", "error", "-protocol_whitelist", liveBacklogProtocols, "-rw_timeout", liveBacklogReadTimeout, "-user_agent", liveStreamUserAgent}
	if h := ffmpegHeadersArg(inputHeaders); h != "" {
		args = append(args, "-headers", h)
	}
	args = append(args, "-allowed_extensions", "ALL", "-allowed_segment_extensions", "ALL", "-extension_picky", "0")
	args = append(args, "-select_streams", "v", "-show_entries", "stream=codec_name,profile,pix_fmt,field_order", "-of", "csv=p=0", inputArg)
	cmd := exec.CommandContext(ctx, m.ffprobePath, args...)
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if parent.Err() != nil {
		return false, false
	}
	if err != nil {
		log.Printf("[hls] live session %s: web video copy=false (probe failed: %v)", sessionID, err)
		return false, true
	}
	copy = browserSafeProbedVideo(string(out))
	log.Printf("[hls] live session %s: web video copy=%v (probed %q)", sessionID, copy, strings.TrimSpace(string(out)))
	return copy, true
}

var (
	errLiveEncodeNotApplicable = errors.New("not a web live session")
	errLiveEncodeNoSession     = errors.New("session not found")
)

// ForceLiveVideoEncode switches a web live session that copies its video to the libx264 encode,
// for a browser that cannot decode the copy after all (an in-band format change, a profile the
// PMT did not reveal). The copied backlog leaves the playlist, and the running FFmpeg is stopped
// so the supervisor rebuilds the session at once, as for a changed stream layout; every segment
// before the rebuild's first one then leaves the playlist (liveEncodedFrom). switched is false
// when the session already encodes. encodedFrom is a media sequence at or below the first encoded
// one: a playlist starting at or after it lists only encoded segments.
func (m *HLSManager) ForceLiveVideoEncode(sessionID string) (switched bool, encodedFrom int, err error) {
	session, ok := m.GetSession(sessionID)
	if !ok {
		return false, 0, errLiveEncodeNoSession
	}
	session.mu.Lock()
	if !session.IsLive || !isWebBrowserPlaybackTarget(session.PlaybackTarget) {
		session.mu.Unlock()
		return false, 0, errLiveEncodeNotApplicable
	}
	// Nothing to switch: the session encodes already, or its supervisor has ended and nothing
	// would rebuild it (the player shows its error at once instead of waiting).
	// Only a session decided to copy copies: an undecided one (its check failed, or a proxied
	// input) encodes this run, and stopping that would only cost the restart budget.
	if !session.LiveTuning.WebVideoCopy || !session.webVideoCopyDecided || !session.webVideoCopy || session.liveSupervisorDone || session.Stopped {
		encodedFrom = session.liveEncodedFrom
		session.mu.Unlock()
		return false, encodedFrom, nil
	}
	session.webVideoCopy, session.webVideoCopyDecided, session.webEncodePending = false, true, true
	cmd := session.FFmpegCmd
	session.mu.Unlock()
	// The rebuild resumes after every segment written so far.
	encodedFrom = max(nextLiveSegmentNumber(session.OutputDir), session.liveBacklogEnd())
	session.withdrawLiveBacklog()
	log.Printf("[hls] live session %s: the browser cannot decode the copied video; switching to the encode", sessionID)
	// The supervisor rebuilds a copying run that ends after a switch at once (liveRunCopiedWeb).
	// Between restarts there is no run to stop, and the next one encodes anyway.
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	return true, encodedFrom, nil
}

// liveEntriesBeforeEncode counts the leading stitched entries copied before a switch to the
// encode (ForceLiveVideoEncode). Caller holds session.mu.
func liveEntriesBeforeEncode(session *HLSSession) int {
	count := 0
	for count < len(session.livePlaylistWindow) && session.livePlaylistWindow[count].MediaSequence < session.liveEncodedFrom {
		count++
	}
	return count
}
