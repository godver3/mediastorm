package jellyfinbridge

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"novastream/internal/jellyfinbridge/dto"
	"novastream/services/playback"
)

// mediaProbeWait bounds the ffprobe a PlaybackInfo may trigger. The prequeue
// usually leaves the result cached, so this only matters on a cold cache.
const mediaProbeWait = 5 * time.Second

// mediaProbe is the part of mediastorm's /api/video/metadata summary that
// Jellyfin's MediaStreams carry.
type mediaProbe struct {
	path            string
	DurationSeconds float64 `json:"durationSeconds"`
	FormatBitRate   int64   `json:"formatBitRate"`
	VideoStreams    []struct {
		Index          int    `json:"index"`
		CodecName      string `json:"codecName"`
		Width          int    `json:"width"`
		Height         int    `json:"height"`
		BitRate        int64  `json:"bitRate"`
		PixFmt         string `json:"pixFmt"`
		Profile        string `json:"profile"`
		Level          int    `json:"level"`
		AvgFrameRate   string `json:"avgFrameRate"`
		ColorTransfer  string `json:"colorTransfer"`
		ColorPrimaries string `json:"colorPrimaries"`
		ColorSpace     string `json:"colorSpace"`
		HasDolbyVision bool   `json:"hasDolbyVision"`
		DVProfile      string `json:"dolbyVisionProfile"`
		Interlaced     bool   `json:"interlaced"`
	} `json:"videoStreams"`
	AudioStreams []struct {
		Index         int    `json:"index"`
		CodecName     string `json:"codecName"`
		Channels      int    `json:"channels"`
		SampleRate    int    `json:"sampleRate"`
		BitRate       int64  `json:"bitRate"`
		ChannelLayout string `json:"channelLayout"`
		Profile       string `json:"profile"`
		Language      string `json:"language"`
		Title         string `json:"title"`
	} `json:"audioStreams"`
	SubtitleStreams []struct {
		Index       int            `json:"index"`
		CodecName   string         `json:"codecName"`
		Language    string         `json:"language"`
		Title       string         `json:"title"`
		Disposition map[string]int `json:"disposition"`
		IsBitmap    bool           `json:"isBitmap"`
	} `json:"subtitleStreams"`
	Chapters []struct {
		Title string  `json:"title"`
		Start float64 `json:"start"`
		End   float64 `json:"end,omitempty"`
	} `json:"chapters"`
}

// probeMedia returns the stream's technical details, probing at most once per
// play session and stream.
func (s *Server) probeMedia(ctx context.Context, p *principal, sess *playSession, status *playback.PrequeueStatusResponse) *mediaProbe {
	if s.deps.Handlers.ProbeVideo == nil || status == nil || status.Status != playback.PrequeueStatusReady || status.StreamPath == "" {
		return nil
	}
	sess.mu.Lock()
	cached := sess.probe
	sess.mu.Unlock()
	if cached != nil && cached.path == status.StreamPath {
		return cached
	}
	ctx, cancel := context.WithTimeout(ctx, mediaProbeWait)
	defer cancel()
	q := url.Values{"path": {status.StreamPath}, "profileId": {p.Profile.ID}}
	var probe mediaProbe
	if _, err := callJSON(s.deps.Handlers.ProbeVideo, internalRequest(ctx, p, http.MethodGet, "/api/video/metadata?"+q.Encode(), nil, nil), &probe); err != nil {
		log.Printf("[jellyfin-bridge] probe %s: %v", sess.ItemID, err)
		return nil
	}
	probe.path = status.StreamPath
	sess.mu.Lock()
	sess.probe = &probe
	if sess.duration <= 0 && probe.DurationSeconds > 0 {
		// Direct streams often have no duration in their status, and
		// metadata lacks many episode runtimes; progress needs one.
		sess.duration = probe.DurationSeconds
	}
	sess.mu.Unlock()
	s.rememberChapters(sess.ProfileID, sess.ItemID, &probe)
	s.rememberDuration(sess.ProfileID, sess.ItemID, probe.DurationSeconds)
	return &probe
}

// fillStatus completes a status that has no prequeue behind it (a release
// the user picked) with the probed duration, HDR format and tracks.
func (m *mediaProbe) fillStatus(status *playback.PrequeueStatusResponse) {
	if m == nil || status == nil {
		return
	}
	if status.Duration <= 0 {
		status.Duration = m.DurationSeconds
	}
	if len(m.VideoStreams) > 0 {
		v := m.VideoStreams[0]
		status.HasDolbyVision, status.DolbyVisionProfile = v.HasDolbyVision, v.DVProfile
		status.HasHDR10 = strings.EqualFold(v.ColorTransfer, "smpte2084")
		status.FrameRate = v.AvgFrameRate
	}
	if len(status.AudioTracks) == 0 {
		for _, a := range m.AudioStreams {
			status.AudioTracks = append(status.AudioTracks, playback.AudioTrackInfo{Index: a.Index, Language: a.Language, Codec: a.CodecName, Profile: a.Profile, Title: a.Title})
		}
	}
	if len(status.SubtitleTracks) == 0 {
		for _, sub := range m.SubtitleStreams {
			status.SubtitleTracks = append(status.SubtitleTracks, playback.SubtitleTrackInfo{
				Index: sub.Index, AbsoluteIndex: sub.Index, Language: sub.Language, Title: sub.Title,
				Codec: sub.CodecName, Forced: sub.Disposition["forced"] == 1, IsBitmap: sub.IsBitmap,
			})
		}
	}
	if status.SelectedAudioTrack < 0 && len(status.AudioTracks) > 0 {
		status.SelectedAudioTrack = status.AudioTracks[0].Index
	}
}

// videoCodec is the primary video stream's codec ("" when unknown).
func (m *mediaProbe) videoCodec() string {
	if m == nil || len(m.VideoStreams) == 0 {
		return ""
	}
	return strings.ToLower(m.VideoStreams[0].CodecName)
}

// applyProbe fills resolution, bitrates, frame rate, bit depth, colors and
// audio channels into the MediaStreams built from the prequeue track list.
func applyProbe(streams []dto.MediaStream, m *mediaProbe) {
	if m == nil {
		return
	}
	for i := range streams {
		st := &streams[i]
		switch st.Type {
		case "Video":
			if len(m.VideoStreams) == 0 {
				continue
			}
			v := m.VideoStreams[0]
			st.Index = v.Index
			st.Codec = strings.ToLower(v.CodecName)
			st.Profile = v.Profile
			st.PixelFormat = v.PixFmt
			st.ColorSpace, st.ColorPrimaries, st.ColorTransfer = v.ColorSpace, v.ColorPrimaries, v.ColorTransfer
			if v.Level > 0 {
				level := float64(v.Level)
				st.Level = &level
			}
			if v.Width > 0 && v.Height > 0 {
				st.Width, st.Height = intPtr(v.Width), intPtr(v.Height)
			}
			if v.BitRate > 0 {
				st.BitRate = intPtr(int(v.BitRate))
			}
			if fps := parseFrameRate(v.AvgFrameRate); fps > 0 {
				st.AverageFrameRate, st.RealFrameRate = &fps, &fps
			}
			if strings.EqualFold(v.ColorTransfer, "arib-std-b67") && !strings.HasPrefix(st.VideoRangeType, "DOVI") {
				st.VideoRange, st.VideoRangeType = "HDR", "HLG"
			}
			if depth := pixFmtBitDepth(v.PixFmt); depth > 0 {
				st.BitDepth = intPtr(depth)
			}
			st.IsInterlaced = v.Interlaced
			st.DisplayTitle = videoDisplayTitle(v.Width, v.Height, v.Interlaced, st.Codec, st.VideoRangeType)
		case "Audio":
			for _, a := range m.AudioStreams {
				if a.Index != st.Index {
					continue
				}
				if a.Channels > 0 {
					st.Channels = intPtr(a.Channels)
				}
				if a.SampleRate > 0 {
					st.SampleRate = intPtr(a.SampleRate)
				}
				if a.BitRate > 0 {
					st.BitRate = intPtr(int(a.BitRate))
				}
				st.ChannelLayout = a.ChannelLayout
				if st.Profile == "" {
					st.Profile = a.Profile
				}
				if layout := displayChannelLayout(a.ChannelLayout, a.Channels); layout != "" {
					st.DisplayTitle += " " + layout
				}
			}
		}
	}
}

// parseFrameRate reads an ffprobe rate such as "24000/1001".
func parseFrameRate(rate string) float64 {
	num, den, found := strings.Cut(strings.TrimSpace(rate), "/")
	n, err := strconv.ParseFloat(num, 64)
	if err != nil || n <= 0 {
		return 0
	}
	if !found {
		return n
	}
	d, err := strconv.ParseFloat(den, 64)
	if err != nil || d <= 0 {
		return 0
	}
	return float64(int(n/d*1000+0.5)) / 1000
}

// pixFmtBitDepth derives the bit depth from a pixel format (yuv420p10le -> 10).
func pixFmtBitDepth(pixFmt string) int {
	pixFmt = strings.ToLower(pixFmt)
	switch {
	case pixFmt == "":
		return 0
	case strings.Contains(pixFmt, "p12"):
		return 12
	case strings.Contains(pixFmt, "p10"), strings.Contains(pixFmt, "p010"):
		return 10
	default:
		return 8
	}
}

// videoDisplayTitle mirrors Jellyfin's "1080p H264 SDR".
func videoDisplayTitle(width, height int, interlaced bool, codec, rangeType string) string {
	parts := []string{}
	scan := "p"
	if interlaced {
		scan = "i" // 1080i, 576i: as Jellyfin labels interlaced video
	}
	switch {
	case width >= 3800 || height >= 2100:
		parts = append(parts, "4K")
	case width >= 2500 || height >= 1400:
		parts = append(parts, "1440"+scan)
	case width >= 1900 || height >= 1000:
		parts = append(parts, "1080"+scan)
	case width >= 1260 || height >= 700:
		parts = append(parts, "720"+scan)
	case height > 0:
		parts = append(parts, fmt.Sprintf("%d%s", height, scan))
	}
	if codec != "" {
		parts = append(parts, strings.ToUpper(codec))
	}
	if rangeType != "" {
		parts = append(parts, rangeType)
	}
	if len(parts) == 0 {
		return "Video"
	}
	return strings.Join(parts, " ")
}

// displayChannelLayout renders "5.1(side)" as "5.1" and falls back to the
// channel count.
func displayChannelLayout(layout string, channels int) string {
	if i := strings.Index(layout, "("); i > 0 {
		layout = layout[:i]
	}
	switch {
	case layout != "":
		return layout
	case channels == 1:
		return "mono"
	case channels == 2:
		return "stereo"
	case channels > 0:
		return fmt.Sprintf("%d ch", channels)
	}
	return ""
}
