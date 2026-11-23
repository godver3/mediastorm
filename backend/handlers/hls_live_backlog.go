package handlers

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"novastream/internal/netproxy"
	"novastream/internal/requestsecurity"
)

// Live timeshift backlog: a timeshift session normally lists only what it has played since the
// tune-in, because FFmpeg reads the channel from its live edge. Many channels are HLS with a DVR
// window of their own (public broadcasters keep hours of segments listed). For those, the session
// starts out listing the part of the source window its timeshift window covers, so a player can
// rewind to before the tune-in:
//
//   - at tune-in the source playlist is read once; its older segments (all but the newest
//     liveBacklogEdgeOverlap, which FFmpeg's HLS demuxer starts with) become the session's first
//     media sequences, segment0.ts … segment(B-1).ts, listed with the source's durations and
//     discontinuities;
//   - FFmpeg writes the live part from segment B on (-start_number B, discont_start), so the
//     stitched playlist has one discontinuity where the backlog meets the live encode;
//   - a backlog segment is made only when a player asks for it (and a few ahead): the source's
//     video segment, and its audio rendition's segment of the same media sequence when audio comes
//     separately, are muxed into one MPEG-TS segment with the source timestamps (-copyts), so the
//     backlog plays as one timeline. Nothing is fetched for a player that stays at the live edge,
//     and work queued for a position the player has seeked away from is dropped;
//   - the segment's URL is looked up in a fresh copy of the source playlist (by media sequence),
//     resolved again from the channel URL when its playlist URL stops working, so sources with
//     expiring tokens keep working;
//   - each backlog entry leaves the session's playlist before the source drops it (the source
//     keeps a segment listed until it is about its window old), however long the timeshift window.
//
// Only native timeshift sessions (video copied) on a plain HLS source qualify: no proxy, tuner,
// relay or video encode, unencrypted whole MPEG-TS video segments (no byte ranges or gaps) over
// http(s), with audio muxed in or as a separate rendition of MPEG-TS or packed audio segments
// (hls_live_backlog_packed.go) matched by media sequence. The audio is the one FFmpeg picks for the live
// encode (most channels); it is copied when it is stereo AAC already, else encoded like the
// timeshift's live audio. Both are decided with the first backlog segment, which also checks that
// a separate audio rendition lines up with the video; the backlog is withdrawn when it does not.

const (
	// liveBacklogEdgeOverlap is how many of the source's newest segments FFmpeg's HLS demuxer starts
	// with (its default live_start_index of -3); the backlog ends before them.
	liveBacklogEdgeOverlap = 3
	// liveBacklogPrefetch backlog segments are made ahead of the one a player asks for.
	liveBacklogPrefetch = 3
	// liveBacklogMuxTimeout bounds making one backlog segment.
	liveBacklogMuxTimeout = 45 * time.Second
	// liveBacklogParallel bounds the backlog segments made at once per session.
	liveBacklogParallel = 3
	// liveBacklogPlaylistLimit bounds how much of a source playlist is read.
	liveBacklogPlaylistLimit = 8 << 20
	// liveBacklogPlaylistMaxAge is how long a fetched source playlist is used to look up segments.
	liveBacklogPlaylistMaxAge = 5 * time.Second
	// liveBacklogExpiryMargin: a backlog entry leaves the session's playlist this long before the
	// source drops it. The backlog's length leaves room for it (liveBacklogMargin).
	liveBacklogExpiryMargin = 10 * time.Second
	// liveBacklogMinListed: a backlog entry that would leave the playlist sooner is left out; a
	// player could hardly rewind into it.
	liveBacklogMinListed = 30 * time.Second
	// liveBacklogMinDuration is the shortest backlog worth its costs (the tune-in requests, the
	// probe, the raised target duration); shorter timeshift windows need half of theirs.
	liveBacklogMinDuration = 60 * time.Second
	// liveBacklogMPEGTSSync is the first byte of every MPEG-TS packet.
	liveBacklogMPEGTSSync = 0x47
	// liveBacklogProtocols are what FFmpeg may open for a backlog segment.
	liveBacklogProtocols = "http,https,tcp,tls,crypto"
	// liveBacklogReadTimeout (µs) ends a probe or mux whose source connection stalls.
	liveBacklogReadTimeout = "15000000"
	// liveBacklogAutoAudio as liveBacklogAudio.stream leaves the streams to FFmpeg's own pick.
	liveBacklogAutoAudio = -2
)

var (
	// errLiveBacklogGone reports a backlog segment the source no longer lists.
	errLiveBacklogGone = errors.New("the source no longer lists this segment")
	// errLiveBacklogStale reports queued work for a position the player has left.
	errLiveBacklogStale = errors.New("the player has moved on")
	// errLiveBacklogStopped reports a stopped session or withdrawn backlog.
	errLiveBacklogStopped = errors.New("backlog stopped")
	// errLiveBacklogMisaligned reports a separate audio rendition that does not line up with the video.
	errLiveBacklogMisaligned = errors.New("the audio rendition does not line up with the video")
)

// liveBacklogSegment is one source segment of the backlog.
type liveBacklogSegment struct {
	Sequence      int    // the source's media sequence (video and audio alike)
	VideoURL      string // as listed at tune-in; looked up again when muxing
	AudioURL      string // "" when the video segment carries the audio
	Duration      float64
	Discontinuity bool
	// Expires is when the source drops it (less a margin); it leaves the session's playlist then.
	Expires time.Time
}

// liveBacklog is a session's backlog: segment i is media sequence i.
type liveBacklog struct {
	segments  []liveBacklogSegment
	sourceURL string // the channel URL, to resolve the playlists again
	headers   map[string]string

	ctx       context.Context
	cancel    context.CancelFunc
	withdrawn atomic.Bool // its entries leave the playlist (withdrawLiveBacklog)

	mu            sync.Mutex
	videoPlaylist string
	audioPlaylist string // "" when the video segments carry the audio
	// audioPacked: the audio rendition's segments are packed audio (hls_live_backlog_packed.go).
	// Set before the backlog is published, read-only after.
	audioPacked bool
	inflight    map[int]*liveBacklogJob
	playlists   map[string]liveBacklogPlaylist
	audio       liveBacklogAudio
	latest      int // the backlog segment a player asked for last
	// slots bound the muxes; playerSlot is one more that only a player's own request takes, so
	// it never waits behind its own prefetches.
	slots      chan struct{}
	playerSlot chan struct{}
	// audioTurn makes deciding the audio single-flight (liveBacklogAudio); waiting for it respects
	// the caller's deadline.
	audioTurn chan struct{}
}

// liveBacklogAudio is how the backlog takes its audio, decided with the first segment made.
type liveBacklogAudio struct {
	decided bool
	stream  int  // the audio stream of the audio input (the live encode's pick)
	copy    bool // stereo AAC: copied
}

// liveBacklogJob is one backlog segment being made.
type liveBacklogJob struct {
	done   chan struct{}
	err    error
	ctx    context.Context
	cancel context.CancelFunc
	player bool // started by a player's request (may take playerSlot)
	// waiters are player requests waiting for it: a job they wait on is not cancelled as stale.
	waiters int
}

// liveBacklogPlaylist is a fetched source playlist: segment URL by media sequence.
type liveBacklogPlaylist struct {
	fetched time.Time
	uris    map[int]string
}

func newLiveBacklog(segments []liveBacklogSegment, sourceURL, videoPlaylist, audioPlaylist string, headers map[string]string) *liveBacklog {
	ctx, cancel := context.WithCancel(context.Background())
	return &liveBacklog{
		segments:      segments,
		sourceURL:     sourceURL,
		headers:       headers,
		ctx:           ctx,
		cancel:        cancel,
		videoPlaylist: videoPlaylist,
		audioPlaylist: audioPlaylist,
		inflight:      map[int]*liveBacklogJob{},
		playlists:     map[string]liveBacklogPlaylist{},
		latest:        -1,
		slots:         make(chan struct{}, liveBacklogParallel),
		playerSlot:    make(chan struct{}, 1),
		audioTurn:     make(chan struct{}, 1),
	}
}

// liveBacklogEnd is the first media sequence after the session's backlog (0 without one).
func (s *HLSSession) liveBacklogEnd() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.liveBacklog == nil {
		return 0
	}
	return len(s.liveBacklog.segments)
}

// stopLiveBacklog cancels the session's backlog muxes (session cleanup).
func (s *HLSSession) stopLiveBacklog() {
	s.mu.RLock()
	backlog := s.liveBacklog
	s.mu.RUnlock()
	if backlog != nil {
		backlog.cancel()
	}
}

// withdrawLiveBacklog stops listing the session's backlog: its entries leave the playlist at the
// next refresh (they are its oldest, so the media sequence only moves forward).
func (s *HLSSession) withdrawLiveBacklog() {
	s.mu.RLock()
	backlog := s.liveBacklog
	s.mu.RUnlock()
	if backlog != nil {
		backlog.withdrawn.Store(true)
		backlog.cancel()
	}
}

// expiredLiveBacklogEntries counts the leading entries of a stitched window that are backlog
// segments the source is about to drop. Caller holds session.mu.
func expiredLiveBacklogEntries(session *HLSSession, now time.Time) int {
	backlog := session.liveBacklog
	if backlog == nil {
		return 0
	}
	withdrawn := backlog.withdrawn.Load()
	n := 0
	for _, entry := range session.livePlaylistWindow {
		if entry.MediaSequence < 0 || entry.MediaSequence >= len(backlog.segments) || (!withdrawn && now.Before(backlog.segments[entry.MediaSequence].Expires)) {
			break
		}
		n++
	}
	return n
}

// hlsMediaSegment is one entry of a source media playlist.
type hlsMediaSegment struct {
	Sequence      int
	Duration      float64
	URI           string
	Discontinuity bool
}

// hlsMediaPlaylist is a parsed live media playlist; ok is false for one the backlog cannot use
// (encrypted, fMP4, byte ranges, gaps, ended, non-http(s) segments or none, or without the
// EXT-X-MEDIA-SEQUENCE its segments are looked up again by).
type hlsMediaPlaylist struct {
	Segments []hlsMediaSegment
	ok       bool
}

func (p hlsMediaPlaylist) duration() float64 {
	total := 0.0
	for _, seg := range p.Segments {
		total += seg.Duration
	}
	return total
}

// parseHLSMediaPlaylist parses a media playlist, resolving segment URIs against base.
func parseHLSMediaPlaylist(body string, base *url.URL) hlsMediaPlaylist {
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), liveBacklogPlaylistLimit)
	var out hlsMediaPlaylist
	sequence := 0
	hasSequence := false
	pendingDuration := -1.0
	pendingDiscontinuity := false
	first := true
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if first {
			if !strings.HasPrefix(line, "#EXTM3U") {
				return hlsMediaPlaylist{}
			}
			first = false
			continue
		}
		switch {
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			var err error
			sequence, err = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:")))
			hasSequence = err == nil
		case strings.HasPrefix(line, "#EXTINF:"):
			value := strings.TrimPrefix(line, "#EXTINF:")
			if i := strings.IndexByte(value, ','); i >= 0 {
				value = value[:i]
			}
			d, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err != nil || d <= 0 {
				return hlsMediaPlaylist{}
			}
			pendingDuration = d
		case line == "#EXT-X-DISCONTINUITY":
			pendingDiscontinuity = true
		case strings.HasPrefix(line, "#EXT-X-KEY:"):
			if attrs := parseHLSAttributes(strings.TrimPrefix(line, "#EXT-X-KEY:")); !strings.EqualFold(attrs["METHOD"], "NONE") {
				return hlsMediaPlaylist{}
			}
		case strings.HasPrefix(line, "#EXT-X-MAP:"), strings.HasPrefix(line, "#EXT-X-BYTERANGE:"), line == "#EXT-X-GAP",
			line == "#EXT-X-ENDLIST", strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			// fMP4, parts of a resource, missing segments, a finished playlist or a master: not a
			// live playlist of whole MPEG-TS segments.
			return hlsMediaPlaylist{}
		case strings.HasPrefix(line, "#"):
		default:
			uri := resolveHLSURI(base, line)
			if pendingDuration <= 0 || !isHTTPURL(uri) {
				return hlsMediaPlaylist{}
			}
			out.Segments = append(out.Segments, hlsMediaSegment{
				Sequence:      sequence,
				Duration:      pendingDuration,
				URI:           uri,
				Discontinuity: pendingDiscontinuity,
			})
			sequence++
			pendingDuration, pendingDiscontinuity = -1, false
		}
	}
	out.ok = scanner.Err() == nil && len(out.Segments) > 0 && hasSequence
	return out
}

// sameHLSSegment reports whether two URLs name the same segment, ignoring the query (tokens).
func sameHLSSegment(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	return errA == nil && errB == nil && ua.Host == ub.Host && ua.Path == ub.Path
}

func resolveHLSURI(base *url.URL, ref string) string {
	parsed, err := url.Parse(ref)
	if err != nil || base == nil {
		return ref
	}
	return base.ResolveReference(parsed).String()
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// hlsBacklogRenditions are the media playlists of a master the backlog reads: the variant with the
// largest picture (FFmpeg's default video pick, like the live session's) and, when its audio comes
// as renditions, the default one of its group.
type hlsBacklogRenditions struct {
	VideoURL string
	AudioURL string // "" when the variant carries its audio
}

// pickHLSBacklogRenditions chooses the renditions of a master playlist; ok is false when body is not
// a master playlist.
func pickHLSBacklogRenditions(body string, base *url.URL) (hlsBacklogRenditions, bool) {
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), liveBacklogPlaylistLimit)
	type audioRendition struct {
		uri       string
		isDefault bool
	}
	audioGroups := map[string][]audioRendition{}
	var (
		pending            map[string]string
		bestArea           int64 = -1
		bestURI, bestAudio string
		// The live encode reads one muxed variant, the highest bandwidth, when no variant names an
		// audio group (pickLiveHLSVariant); the backlog reads the same one.
		muxedBW    int64 = -1
		muxedURI   string
		namesAudio bool
	)
	first := true
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if first {
			if !strings.HasPrefix(line, "#EXTM3U") {
				return hlsBacklogRenditions{}, false
			}
			first = false
			continue
		}
		switch {
		case strings.HasPrefix(line, "#EXT-X-MEDIA:"):
			attrs := parseHLSAttributes(strings.TrimPrefix(line, "#EXT-X-MEDIA:"))
			if !strings.EqualFold(attrs["TYPE"], "AUDIO") {
				continue
			}
			group := attrs["GROUP-ID"]
			audioGroups[group] = append(audioGroups[group], audioRendition{uri: attrs["URI"], isDefault: strings.EqualFold(attrs["DEFAULT"], "YES")})
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			pending = parseHLSAttributes(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
		case strings.HasPrefix(line, "#"):
		case pending != nil:
			area := int64(0)
			if w, h, found := strings.Cut(pending["RESOLUTION"], "x"); found {
				wi, _ := strconv.ParseInt(w, 10, 64)
				hi, _ := strconv.ParseInt(h, 10, 64)
				area = wi * hi
			}
			bw, _ := strconv.ParseInt(pending["BANDWIDTH"], 10, 64)
			// The largest picture; of equal ones the first listed, as FFmpeg picks.
			if area > bestArea {
				bestArea, bestURI, bestAudio = area, line, pending["AUDIO"]
			}
			if bw > muxedBW {
				muxedBW, muxedURI = bw, line
			}
			namesAudio = namesAudio || strings.TrimSpace(pending["AUDIO"]) != ""
			pending = nil
		}
	}
	if scanner.Err() != nil || bestURI == "" {
		return hlsBacklogRenditions{}, false
	}
	if !namesAudio {
		return hlsBacklogRenditions{VideoURL: resolveHLSURI(base, muxedURI)}, true
	}
	out := hlsBacklogRenditions{VideoURL: resolveHLSURI(base, bestURI)}
	if group := audioGroups[bestAudio]; bestAudio != "" {
		// The default rendition (else the first), like the live encode's pick. One without a URI is
		// the variant's own muxed audio: the backlog then takes the variant's audio too.
		chosen := -1
		for i, rendition := range group {
			if chosen < 0 || (rendition.isDefault && !group[chosen].isDefault) {
				chosen = i
			}
		}
		if chosen >= 0 && group[chosen].uri != "" {
			out.AudioURL = resolveHLSURI(base, group[chosen].uri)
		}
	}
	return out, true
}

// planLiveBacklog picks the backlog from the source's video playlist (and audio playlist, if
// separate): the segments before the newest liveBacklogEdgeOverlap, newest first, as long as they
// fit within limit. Audio segments are matched by media sequence; nil when one is missing. Each
// segment expires when the source drops it, less `margin`.
func planLiveBacklog(video, audio hlsMediaPlaylist, separateAudio bool, limit time.Duration, now time.Time, margin time.Duration) []liveBacklogSegment {
	if !video.ok || limit <= 0 || (separateAudio && !audio.ok) {
		return nil
	}
	audioBySequence := map[int]string{}
	for _, seg := range audio.Segments {
		audioBySequence[seg.Sequence] = seg.URI
	}
	end := len(video.Segments) - liveBacklogEdgeOverlap
	if end <= 0 {
		return nil
	}
	start := end
	total := 0.0
	for start > 0 && total+video.Segments[start-1].Duration <= limit.Seconds() {
		start--
		total += video.Segments[start].Duration
	}
	if start == end {
		return nil
	}
	// A segment's age is the source time listed after its start; the source drops it at the
	// window's length.
	sourceWindow := video.duration()
	after := make([]float64, len(video.Segments)+1)
	for i := len(video.Segments) - 1; i >= 0; i-- {
		after[i] = after[i+1] + video.Segments[i].Duration
	}
	out := make([]liveBacklogSegment, 0, end-start)
	for i := start; i < end; i++ {
		seg := video.Segments[i]
		left := time.Duration((sourceWindow-after[i])*float64(time.Second)) - margin
		entry := liveBacklogSegment{
			Sequence:      seg.Sequence,
			VideoURL:      seg.URI,
			Duration:      seg.Duration,
			Discontinuity: i > start && seg.Discontinuity,
			Expires:       now.Add(left),
		}
		if separateAudio {
			uri, ok := audioBySequence[seg.Sequence]
			if !ok {
				return nil
			}
			entry.AudioURL = uri
		}
		out = append(out, entry)
	}
	return out
}

// worthwhileLiveBacklog leaves out the planned segments that would leave the playlist within
// liveBacklogMinListed (the oldest ones) and drops a backlog shorter than
// min(liveBacklogMinDuration, window/2): a source with a short window of its own offers nothing a
// player could rewind into for long.
func worthwhileLiveBacklog(segments []liveBacklogSegment, now time.Time, window time.Duration) []liveBacklogSegment {
	start := 0
	for start < len(segments) && segments[start].Expires.Before(now.Add(liveBacklogMinListed)) {
		start++
	}
	segments = segments[start:]
	if len(segments) > 0 {
		// The first listed entry carries no discontinuity of its own (planLiveBacklog).
		segments[0].Discontinuity = false
	}
	total := 0.0
	for _, seg := range segments {
		total += seg.Duration
	}
	if time.Duration(total*float64(time.Second)) < min(liveBacklogMinDuration, window/2) {
		return nil
	}
	return segments
}

// liveBacklogMargin is how much shorter than the source window the backlog is: the edge segments
// FFmpeg starts with and the expiry margin, so every backlog entry is listed for a while.
func liveBacklogMargin(targetDuration float64) time.Duration {
	return time.Duration(float64(liveBacklogEdgeOverlap)*targetDuration*float64(time.Second)) + 2*liveBacklogExpiryMargin
}

// fetchHLSPlaylist reads a playlist, following redirects; it returns the body and the final URL.
func fetchHLSPlaylist(ctx context.Context, client *http.Client, rawURL string, headers map[string]string) (string, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("User-Agent", liveStreamUserAgent)
	applyRequestHeaders(req.Header, headers)
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return "", nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, liveBacklogPlaylistLimit+1))
	if err != nil {
		return "", nil, err
	}
	// A cut playlist would lack its newest segments and end in half a URI.
	if len(body) > liveBacklogPlaylistLimit {
		return "", nil, fmt.Errorf("playlist larger than %d bytes", liveBacklogPlaylistLimit)
	}
	return string(body), resp.Request.URL, nil
}

// liveBacklogSource is a channel's media playlists as the backlog reads them.
type liveBacklogSource struct {
	videoPlaylist string
	audioPlaylist string // "" when the video segments carry the audio
	video, audio  hlsMediaPlaylist
}

// resolveLiveBacklogSource reads a channel URL (a media playlist, or a master and its chosen
// renditions); ok is false when it is not a usable live HLS source.
func resolveLiveBacklogSource(ctx context.Context, client *http.Client, sourceURL string, headers map[string]string) (liveBacklogSource, bool, error) {
	body, final, err := fetchHLSPlaylist(ctx, client, sourceURL, headers)
	if err != nil {
		return liveBacklogSource{}, false, err
	}
	src := liveBacklogSource{videoPlaylist: final.String(), video: parseHLSMediaPlaylist(body, final)}
	if src.video.ok {
		return src, true, nil
	}
	renditions, isMaster := pickHLSBacklogRenditions(body, final)
	if !isMaster || !isHTTPURL(renditions.VideoURL) || (renditions.AudioURL != "" && !isHTTPURL(renditions.AudioURL)) {
		return liveBacklogSource{}, false, nil
	}
	videoBody, videoURL, err := fetchHLSPlaylist(ctx, client, renditions.VideoURL, headers)
	if err != nil {
		return liveBacklogSource{}, false, err
	}
	src.videoPlaylist, src.video = videoURL.String(), parseHLSMediaPlaylist(videoBody, videoURL)
	if renditions.AudioURL != "" {
		audioBody, audioURL, err := fetchHLSPlaylist(ctx, client, renditions.AudioURL, headers)
		if err != nil {
			return liveBacklogSource{}, false, err
		}
		src.audioPlaylist, src.audio = audioURL.String(), parseHLSMediaPlaylist(audioBody, audioURL)
		if !src.audio.ok {
			return liveBacklogSource{}, false, nil
		}
	}
	return src, src.video.ok, nil
}

func newLiveBacklogClient() (*http.Client, error) {
	return netproxy.NewHTTPClientWithOptions(netproxy.HTTPClientOptions{ResponseHeaderTimeout: 10 * time.Second}, "")
}

// prepareLiveBacklog reads the source at tune-in (inputURL, what FFmpeg reads: the channel URL or
// one variant of it) and, when it has a window to offer, lists the backlog as the session's first
// media sequences. It returns where the live encode's numbering starts (0 without a backlog) and,
// for FFmpeg's HLS demuxer, the live_start_index that makes it start with the source segment after
// the backlog (0: its default).
func (m *HLSManager) prepareLiveBacklog(ctx context.Context, session *HLSSession, channelURL, inputURL string, headers map[string]string) (int, int) {
	window := session.LiveTuning.TimeshiftWindow
	client, err := newLiveBacklogClient()
	if err != nil {
		return 0, 0
	}
	defer client.CloseIdleConnections()
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	src, ok, err := resolveLiveBacklogSource(reqCtx, client, inputURL, headers)
	if !ok {
		if err != nil {
			log.Printf("[hls] live session %s: no timeshift backlog (source playlist: %s)", session.ID, requestsecurity.TextForLog(err.Error()))
		}
		return 0, 0
	}
	targetDuration := 0.0
	for _, seg := range src.video.Segments {
		targetDuration = max(targetDuration, seg.Duration)
	}
	sourceWindow := time.Duration(src.video.duration() * float64(time.Second))
	limit := min(window, sourceWindow-liveBacklogMargin(targetDuration))
	now := time.Now()
	segments := worthwhileLiveBacklog(planLiveBacklog(src.video, src.audio, src.audioPlaylist != "", limit, now, liveBacklogExpiryMargin), now, window)
	if len(segments) == 0 {
		return 0, 0
	}
	newest := segments[len(segments)-1]
	audioKind := ""
	if newest.AudioURL != "" {
		audioKind = liveBacklogSegmentKind(reqCtx, client, newest.AudioURL, headers)
	}
	if liveBacklogSegmentKind(reqCtx, client, newest.VideoURL, headers) != liveBacklogSegmentTS ||
		(newest.AudioURL != "" && audioKind != liveBacklogSegmentTS && audioKind != liveBacklogSegmentPacked) {
		log.Printf("[hls] live session %s: no timeshift backlog (the source's segments are neither MPEG-TS nor packed audio)", session.ID)
		return 0, 0
	}
	// The source may have moved on while this ran: FFmpeg starts with the segment after the
	// backlog, counted from the end of the playlist as it is now. FFmpeg reads the playlist itself
	// a moment later; should the source add a segment just then (or a CDN edge serve an older
	// copy), one segment at the seam is missing (or repeated). There is no shared timeline to
	// check against: the live encode's timestamps are its own.
	liveStartIndex := 0
	if body, base, err := fetchHLSPlaylist(reqCtx, client, src.videoPlaylist, headers); err == nil {
		now := parseHLSMediaPlaylist(body, base)
		for i, seg := range now.Segments {
			// Segments are looked up by media sequence later: a source whose sequence does not
			// follow its segments (it never advances) would serve other segments under it.
			if seg.Sequence == newest.Sequence && !sameHLSSegment(seg.URI, newest.VideoURL) {
				log.Printf("[hls] live session %s: no timeshift backlog (the source's media sequence does not follow its segments)", session.ID)
				return 0, 0
			}
			if seg.Sequence == newest.Sequence+1 {
				liveStartIndex = i - len(now.Segments)
			}
		}
	}

	entries := make([]livePlaylistEntry, len(segments))
	backlogDuration, longest := 0.0, 0.0
	for i, seg := range segments {
		entries[i] = livePlaylistEntry{
			MediaSequence: i,
			URI:           fmt.Sprintf("segment%d.ts", i),
			ExtInf:        fmt.Sprintf("%.6f,", seg.Duration),
			Duration:      seg.Duration,
			Discontinuity: seg.Discontinuity,
		}
		backlogDuration += seg.Duration
		longest = max(longest, seg.Duration)
	}
	session.mu.Lock()
	session.liveBacklog = newLiveBacklog(segments, channelURL, src.videoPlaylist, src.audioPlaylist, headers)
	session.liveBacklog.audioPacked = audioKind == liveBacklogSegmentPacked
	session.livePlaylistWindow = entries
	session.livePlaylistTargetDuration = int(math.Ceil(longest))
	session.liveDiscontinuitySequence = 0
	backlog := session.liveBacklog
	session.mu.Unlock()
	// Decide a separate audio rendition's handling now, from the newest backlog segment, so one that
	// does not line up withdraws the backlog before a player rewinds into it.
	if newest.AudioURL != "" {
		go func() {
			ctx, cancel := context.WithTimeout(backlog.ctx, liveBacklogMuxTimeout)
			defer cancel()
			if _, err := m.liveBacklogAudio(ctx, backlog, newest.VideoURL, newest.AudioURL); errors.Is(err, errLiveBacklogMisaligned) {
				log.Printf("[hls] live session %s: withdrawing the timeshift backlog: %v", session.ID, err)
				session.withdrawLiveBacklog()
			}
		}()
	}
	log.Printf("[hls] live session %s: timeshift backlog of %d segments (%.0fs of the source's %s window, %s), live from index %d",
		session.ID, len(segments), backlogDuration, sourceWindow.Round(time.Second), requestsecurity.URLForLog(channelURL), liveStartIndex)
	return len(segments), liveStartIndex
}

// segmentURLs look a backlog segment up in fresh copies of the source playlists, so expiring
// segment URLs are renewed. When a playlist cannot be read, the playlists are resolved again from
// the channel URL (their own URLs may carry an expired token); when that fails too, the URLs from
// the tune-in are used.
func (b *liveBacklog) segmentURLs(ctx context.Context, seg liveBacklogSegment) (string, string, error) {
	client, err := newLiveBacklogClient()
	if err != nil {
		return seg.VideoURL, seg.AudioURL, nil
	}
	defer client.CloseIdleConnections()
	b.mu.Lock()
	videoPlaylist, audioPlaylist := b.videoPlaylist, b.audioPlaylist
	b.mu.Unlock()

	video, videoOK := b.lookup(ctx, client, videoPlaylist, seg.Sequence)
	audio, audioOK := "", true
	if audioPlaylist != "" {
		audio, audioOK = b.lookup(ctx, client, audioPlaylist, seg.Sequence)
	}
	if !videoOK || !audioOK {
		src, ok, _ := resolveLiveBacklogSource(ctx, client, b.sourceURL, b.headers)
		if !ok || (src.audioPlaylist == "") != (audioPlaylist == "") {
			return seg.VideoURL, seg.AudioURL, nil
		}
		b.mu.Lock()
		b.videoPlaylist, b.audioPlaylist = src.videoPlaylist, src.audioPlaylist
		b.mu.Unlock()
		b.store(src.videoPlaylist, src.video)
		video, videoOK = b.lookup(ctx, client, src.videoPlaylist, seg.Sequence)
		if src.audioPlaylist != "" {
			b.store(src.audioPlaylist, src.audio)
			audio, audioOK = b.lookup(ctx, client, src.audioPlaylist, seg.Sequence)
		}
		if !videoOK || !audioOK {
			return seg.VideoURL, seg.AudioURL, nil
		}
	}
	if video == "" || (audioPlaylist != "" && audio == "") {
		return "", "", errLiveBacklogGone
	}
	// The same file as at tune-in (a token may differ): a source that started its media sequence
	// over (an origin restart) lists other segments under the old numbers.
	if path.Base(urlPath(video)) != path.Base(urlPath(seg.VideoURL)) ||
		(seg.AudioURL != "" && path.Base(urlPath(audio)) != path.Base(urlPath(seg.AudioURL))) {
		return "", "", errLiveBacklogGone
	}
	return video, audio, nil
}

func urlPath(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Path
	}
	return raw
}

// lookup returns a segment's URL from a playlist fetched within liveBacklogPlaylistMaxAge ("" when
// the playlist no longer lists it); ok is false when the playlist cannot be read.
func (b *liveBacklog) lookup(ctx context.Context, client *http.Client, playlistURL string, sequence int) (string, bool) {
	b.mu.Lock()
	cached, ok := b.playlists[playlistURL]
	b.mu.Unlock()
	if !ok || time.Since(cached.fetched) > liveBacklogPlaylistMaxAge {
		body, base, err := fetchHLSPlaylist(ctx, client, playlistURL, b.headers)
		if err != nil {
			return "", false
		}
		parsed := parseHLSMediaPlaylist(body, base)
		if !parsed.ok {
			return "", false
		}
		cached = b.store(playlistURL, parsed)
	}
	return cached.uris[sequence], true
}

func (b *liveBacklog) store(playlistURL string, parsed hlsMediaPlaylist) liveBacklogPlaylist {
	cached := liveBacklogPlaylist{fetched: time.Now(), uris: make(map[int]string, len(parsed.Segments))}
	for _, seg := range parsed.Segments {
		cached.uris[seg.Sequence] = seg.URI
	}
	b.mu.Lock()
	// Only the playlists in use are kept: tokenized URLs change with every renewal.
	for url := range b.playlists {
		if url != b.videoPlaylist && url != b.audioPlaylist {
			delete(b.playlists, url)
		}
	}
	b.playlists[playlistURL] = cached
	b.mu.Unlock()
	return cached
}

// ensureLiveBacklogSegment makes backlog segment n unless it exists, waiting until ctx ends. A
// player's request (prefetch false) also marks where it is, so queued work for a position it has
// left is dropped.
func (m *HLSManager) ensureLiveBacklogSegment(ctx context.Context, session *HLSSession, n int, prefetch bool) error {
	session.mu.RLock()
	backlog := session.liveBacklog
	outputDir := session.OutputDir
	stopped := session.Stopped
	session.mu.RUnlock()
	if backlog == nil || n < 0 || n >= len(backlog.segments) {
		return nil
	}
	target := filepath.Join(outputDir, fmt.Sprintf("segment%d.ts", n))
	if _, err := os.Stat(target); err == nil {
		return nil
	}
	if stopped || backlog.ctx.Err() != nil {
		return errLiveBacklogStopped
	}
	if !prefetch {
		backlog.noteRequest(n)
	}
	backlog.mu.Lock()
	// A job may have made it since the check above.
	if _, err := os.Stat(target); err == nil {
		backlog.mu.Unlock()
		return nil
	}
	job, running := backlog.inflight[n]
	// A job cancelled for a position the player left is not joined: the player is back.
	if !running || job.ctx.Err() != nil {
		jobCtx, cancel := context.WithTimeout(backlog.ctx, liveBacklogMuxTimeout)
		job = &liveBacklogJob{done: make(chan struct{}), ctx: jobCtx, cancel: cancel, player: !prefetch}
		backlog.inflight[n] = job
		go m.runLiveBacklogJob(session, backlog, n, target, job)
	}
	if !prefetch {
		job.waiters++
	}
	backlog.mu.Unlock()
	if !prefetch {
		defer func() {
			backlog.mu.Lock()
			job.waiters--
			backlog.mu.Unlock()
		}()
	}
	select {
	case <-job.done:
		return job.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// noteLiveBacklogRequest records that a player asked for backlog segment n.
func (s *HLSSession) noteLiveBacklogRequest(n int) {
	s.mu.RLock()
	backlog := s.liveBacklog
	s.mu.RUnlock()
	if backlog != nil {
		backlog.noteRequest(n)
	}
}

// noteRequest moves the player's position to n and stops the work that became stale, queued or
// running, so a seek does not wait behind it; work a player request still waits for goes on.
func (b *liveBacklog) noteRequest(n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.latest = n
	for other, job := range b.inflight {
		if b.staleLocked(other) && job.waiters == 0 {
			job.cancel()
		}
	}
}

// abandoned reports whether job n is stale and no player request waits for it.
func (b *liveBacklog) abandoned(n int, job *liveBacklogJob) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.staleLocked(n) && job.waiters == 0
}

// stale reports whether segment n is behind or well ahead of where the player is.
func (b *liveBacklog) stale(n int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.staleLocked(n)
}

func (b *liveBacklog) staleLocked(n int) bool {
	return b.latest >= 0 && (n < b.latest || n > b.latest+liveBacklogPrefetch)
}

func (m *HLSManager) runLiveBacklogJob(session *HLSSession, backlog *liveBacklog, n int, target string, job *liveBacklogJob) {
	defer func() {
		backlog.mu.Lock()
		if backlog.inflight[n] == job {
			delete(backlog.inflight, n)
		}
		backlog.mu.Unlock()
		job.cancel()
		close(job.done)
	}()
	ctx := job.ctx
	stopped := func() error {
		switch {
		case backlog.ctx.Err() != nil:
			return errLiveBacklogStopped
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return fmt.Errorf("backlog segment %d: timed out", n)
		}
		return errLiveBacklogStale
	}
	playerSlot := job.player
	if !playerSlot {
		select {
		case backlog.slots <- struct{}{}:
		case <-ctx.Done():
			job.err = stopped()
			return
		}
	} else {
		select {
		case backlog.playerSlot <- struct{}{}:
		case backlog.slots <- struct{}{}:
			playerSlot = false
		case <-ctx.Done():
			job.err = stopped()
			return
		}
	}
	defer func() {
		if playerSlot {
			<-backlog.playerSlot
		} else {
			<-backlog.slots
		}
	}()
	if backlog.abandoned(n, job) {
		job.err = errLiveBacklogStale
		return
	}

	seg := backlog.segments[n]
	videoURL, audioURL, err := backlog.segmentURLs(ctx, seg)
	if err != nil {
		job.err = fmt.Errorf("backlog segment %d: %w", n, err)
		return
	}
	// A muxed source's audio streams can change between programmes: each segment gets FFmpeg's own
	// pick (most channels, the first of those), encoded, as the live encode does. Only a separate
	// rendition, one stream all along, keeps the decision made once.
	audio, err := liveBacklogAudio{decided: true, stream: liveBacklogAutoAudio}, error(nil)
	if audioURL != "" {
		audio, err = m.liveBacklogAudio(ctx, backlog, videoURL, audioURL)
	}
	if errors.Is(err, errLiveBacklogMisaligned) {
		log.Printf("[hls] live session %s: withdrawing the timeshift backlog: %v", session.ID, err)
		session.withdrawLiveBacklog()
		job.err = errLiveBacklogStopped
		return
	}
	if err != nil {
		// A probe that failed (the source, the network): the next segment tries again.
		job.err = fmt.Errorf("backlog segment %d: %w", n, err)
		return
	}
	// Its own temp file: a cancelled job for the same segment may still be shutting down.
	stamp := time.Now().UnixNano()
	tmp := fmt.Sprintf("%s.%d.tmp", target, stamp)
	audioInput := liveBacklogAudioInput{URL: audioURL}
	if backlog.audioPacked && audioURL != "" {
		// Packed audio: fetched here for its ID3 timestamp, handed to FFmpeg as a file with that
		// timestamp as its offset.
		data, start, err := fetchPackedAudio(ctx, audioURL, backlog.headers)
		if err != nil {
			if ctx.Err() != nil {
				job.err = stopped()
				return
			}
			job.err = fmt.Errorf("backlog segment %d audio: %w", n, err)
			return
		}
		audioFile := fmt.Sprintf("%s.%d.audio", target, stamp)
		if err := os.WriteFile(audioFile, data, 0o644); err != nil {
			job.err = err
			return
		}
		defer os.Remove(audioFile)
		audioInput = liveBacklogAudioInput{URL: audioFile, File: true, Offset: start}
	}
	args := liveBacklogMuxArgsFor(videoURL, audioInput, backlog.headers, audio, tmp)
	out, err := exec.CommandContext(ctx, m.ffmpegPath, args...).CombinedOutput()
	if err != nil && ctx.Err() == nil {
		// An older segment may carry other audio streams than the newest one the choice was made
		// from (a programme with fewer tracks): FFmpeg's own pick, encoded, as the live encode does.
		_ = os.Remove(tmp)
		args = liveBacklogMuxArgsFor(videoURL, audioInput, backlog.headers, liveBacklogAudio{decided: true, stream: liveBacklogAutoAudio}, tmp)
		out, err = exec.CommandContext(ctx, m.ffmpegPath, args...).CombinedOutput()
	}
	if err != nil {
		_ = os.Remove(tmp)
		if ctx.Err() != nil {
			job.err = stopped()
			return
		}
		job.err = fmt.Errorf("mux backlog segment %d: %w: %s", n, err, requestsecurity.TextForLog(strings.TrimSpace(string(out))))
		return
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		if _, statErr := os.Stat(target); statErr != nil {
			job.err = err
		}
	}
}

// liveBacklogAudio decides once per session, from the first backlog segment made, which audio
// stream to take (the one FFmpeg picks for the live encode: most channels, the first of those) and
// whether it is stereo AAC to copy. With a separate audio rendition it also checks that the
// rendition's segment starts with the video's; an error means the backlog cannot be used.
func (m *HLSManager) liveBacklogAudio(ctx context.Context, backlog *liveBacklog, videoURL, audioURL string) (liveBacklogAudio, error) {
	current := func() liveBacklogAudio {
		backlog.mu.Lock()
		defer backlog.mu.Unlock()
		return backlog.audio
	}
	if decided := current(); decided.decided {
		return decided, nil
	}
	// One decision at a time: the others wait for it (within their deadline) instead of probing too.
	select {
	case backlog.audioTurn <- struct{}{}:
	case <-ctx.Done():
		return liveBacklogAudio{}, ctx.Err()
	}
	defer func() { <-backlog.audioTurn }()
	if decided := current(); decided.decided {
		return decided, nil
	}
	audioInput := videoURL
	if audioURL != "" {
		audioInput = audioURL
	}
	streams, err := m.probeLiveBacklogStreams(ctx, backlog.headers, audioInput)
	if err != nil {
		return liveBacklogAudio{}, err
	}
	out := liveBacklogAudio{decided: true, stream: -1}
	best, index := -1, 0
	var audioStart float64
	for _, s := range streams {
		if s.codecType != "audio" {
			continue
		}
		if s.channels > best {
			best, out.stream, audioStart = s.channels, index, s.start
			// Only a separate rendition is one stream all along; a muxed source's streams can
			// change between programmes, so its audio is always encoded like the live encode's.
			out.copy = audioURL != "" && s.codec == "aac" && s.channels <= 2
		}
		index++
	}
	if audioURL != "" && best >= 0 {
		if backlog.audioPacked {
			// Packed audio's timeline is its ID3 timestamp, which ffprobe does not read.
			_, start, err := fetchPackedAudio(ctx, audioURL, backlog.headers)
			if errors.Is(err, errPackedAudioNoTimestamp) {
				return liveBacklogAudio{}, fmt.Errorf("%w (%v)", errLiveBacklogMisaligned, err)
			}
			if err != nil {
				return liveBacklogAudio{}, err
			}
			audioStart = start
		}
		videoStreams, err := m.probeLiveBacklogStreams(ctx, backlog.headers, videoURL)
		if err != nil {
			return liveBacklogAudio{}, err
		}
		for _, s := range videoStreams {
			if apart := mpegTSTimeApart(s.start, audioStart); s.codecType == "video" && math.Abs(apart) > 1.5 {
				return liveBacklogAudio{}, fmt.Errorf("%w (%.1fs apart)", errLiveBacklogMisaligned, apart)
			}
		}
	}
	backlog.mu.Lock()
	if !backlog.audio.decided {
		backlog.audio = out
	}
	out = backlog.audio
	backlog.mu.Unlock()
	return out, nil
}

// liveBacklogStream is one stream of a probed segment.
type liveBacklogStream struct {
	codecType string
	codec     string
	channels  int
	start     float64
}

func (m *HLSManager) probeLiveBacklogStreams(ctx context.Context, headers map[string]string, input string) ([]liveBacklogStream, error) {
	args := []string{"-v", "error", "-protocol_whitelist", liveBacklogProtocols, "-rw_timeout", liveBacklogReadTimeout, "-user_agent", liveStreamUserAgent}
	if h := ffmpegHeadersArg(headers); h != "" {
		args = append(args, "-headers", h)
	}
	args = append(args, "-show_entries", "stream=codec_type,codec_name,channels,start_time", "-of", "csv=p=0", input)
	out, err := exec.CommandContext(ctx, m.ffprobePath, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("probe backlog segment: %w", err)
	}
	return parseLiveBacklogStreams(string(out)), nil
}

// parseLiveBacklogStreams reads ffprobe's csv lines: codec_name,codec_type,channels,start_time for
// audio, codec_name,codec_type,start_time for video (fields in ffprobe's order).
func parseLiveBacklogStreams(out string) []liveBacklogStream {
	var streams []liveBacklogStream
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Split(strings.TrimSpace(line), ",")
		if len(fields) < 2 {
			continue
		}
		s := liveBacklogStream{codec: fields[0], codecType: fields[1]}
		rest := fields[2:]
		if s.codecType == "audio" && len(rest) > 0 {
			s.channels, _ = strconv.Atoi(rest[0])
			rest = rest[1:]
		}
		if len(rest) > 0 {
			s.start, _ = strconv.ParseFloat(rest[len(rest)-1], 64)
		}
		streams = append(streams, s)
	}
	return streams
}

// prefetchLiveBacklog makes the backlog segments after n in the background.
func (m *HLSManager) prefetchLiveBacklog(session *HLSSession, n int) {
	end := session.liveBacklogEnd()
	for next := n + 1; next <= n+liveBacklogPrefetch && next < end; next++ {
		go func(next int) {
			err := m.ensureLiveBacklogSegment(context.Background(), session, next, true)
			if err != nil && !errors.Is(err, errLiveBacklogGone) && !errors.Is(err, errLiveBacklogStale) && !errors.Is(err, errLiveBacklogStopped) {
				log.Printf("[hls] live session %s: %s", session.ID, requestsecurity.TextForLog(err.Error()))
			}
		}(next)
	}
}

// liveBacklogMuxArgs mux one source segment (and its separate audio segment) into an MPEG-TS
// segment keeping the source timestamps, with the audio stream the live encode takes.
func liveBacklogMuxArgs(videoURL, audioURL string, headers map[string]string, audio liveBacklogAudio, output string) []string {
	return liveBacklogMuxArgsFor(videoURL, liveBacklogAudioInput{URL: audioURL}, headers, audio, output)
}

// liveBacklogAudioInput is a backlog segment's separate audio: a URL, or a local file of packed
// audio with its ID3 timestamp as Offset (seconds).
type liveBacklogAudioInput struct {
	URL    string // "" when the video segment carries the audio
	File   bool
	Offset float64
}

func liveBacklogMuxArgsFor(videoURL string, audioIn liveBacklogAudioInput, headers map[string]string, audio liveBacklogAudio, output string) []string {
	input := func(uri string) []string {
		// Input options apply to the next -i only.
		args := []string{"-protocol_whitelist", liveBacklogProtocols, "-rw_timeout", liveBacklogReadTimeout, "-user_agent", liveStreamUserAgent}
		if h := ffmpegHeadersArg(headers); h != "" {
			args = append(args, "-headers", h)
		}
		return append(args, "-i", uri)
	}
	args := []string{"-nostdin", "-y", "-loglevel", "error"}
	args = append(args, input(videoURL)...)
	audioInput := "0"
	switch {
	case audioIn.File:
		// Packed audio starts at 0 for FFmpeg's raw demuxers: its ID3 timestamp moves it onto the
		// video's timeline (-copyts keeps both).
		args = append(args, "-protocol_whitelist", "file", "-itsoffset", strconv.FormatFloat(audioIn.Offset, 'f', 6, 64), "-i", audioIn.URL)
		audioInput = "1"
	case audioIn.URL != "":
		args = append(args, input(audioIn.URL)...)
		audioInput = "1"
	}
	if audio.stream != liveBacklogAutoAudio {
		args = append(args, "-map", "0:v:0?") // radio channels have none
		if audio.stream >= 0 {
			args = append(args, "-map", fmt.Sprintf("%s:a:%d", audioInput, audio.stream))
		}
	}
	// -copyinkf keeps the frames before a segment's first keyframe (sources cut by time).
	args = append(args, "-sn", "-dn", "-c:v", "copy", "-copyinkf")
	if audio.copy {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args, timeshiftAudioArgs...)
	}
	return append(args, "-copyts", "-muxdelay", "0", "-muxpreload", "0", "-f", "mpegts", output)
}
