package recordings

import (
	"bufio"
	"context"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A recording that starts after its time (created while its programme is on, or picked up late)
// misses the beginning from the live edge. Many HLS channels keep the last hours in their
// playlists (ARD two, ZDF three). Such a recording starts that far back instead: FFmpeg's HLS
// demuxer begins live_start_index segments before the playlist's end, reads the missed part as
// fast as the source delivers it and carries on live. Then the recording ends when its content
// reaches the stop time (-t), and the wall-clock stop allows for the time it started behind.
//
// FFmpeg reports what it recorded (-progress). An attempt that ends before its content reaches
// the stop time is continued from there while the window still has it; a backfilled start that
// records nothing is not tried again (the recording carries on live, as before).

const (
	// recordingBackfillMinLate: a recording starting later than this looks back (a recording that
	// starts on time is picked up within the poll interval).
	recordingBackfillMinLate = 30 * time.Second
	// recordingBackfillLead: FFmpeg reads the playlist a moment after the recorder: it has grown by
	// then, so the recording starts this much earlier than the playlist read says.
	recordingBackfillLead = 6 * time.Second
	// recordingBackfillClockSlack: program date-times further off the clock than this are not
	// trusted; the segments' durations from the playlist's end are.
	recordingBackfillClockSlack = 2 * time.Minute
	// recordingBackfillStopGrace: beyond the time a recording started behind, how long the
	// wall-clock stop waits for its content to reach the stop time.
	recordingBackfillStopGrace = 30 * time.Second
)

// recordingBackfill is where a late recording starts in the source's playlist.
type recordingBackfill struct {
	liveStartIndex int       // FFmpeg's -live_start_index (from the playlist's end); 0: live
	from           time.Time // the programme time the recording starts at
	behind         time.Duration
}

func (b recordingBackfill) active() bool { return b.liveStartIndex < 0 }

// contentLimit is the content the recording takes from its start to stopAt.
func (b recordingBackfill) contentLimit(stopAt time.Time) time.Duration {
	return stopAt.Sub(b.from)
}

// deadline is the wall-clock stop for a backfilled attempt: the content reaches stopAt at the
// latest as long after stopAt as the recording started behind.
func (b recordingBackfill) deadline(stopAt time.Time) time.Time {
	if !b.active() {
		return stopAt
	}
	return stopAt.Add(b.behind + recordingBackfillStopGrace)
}

// recordingBackfillPlaylistLimit: the longest media playlist read for a backfill (a long window
// of tokenized segment URLs is several megabytes); a longer one is not used.
const recordingBackfillPlaylistLimit = 16 << 20

// planRecordingBackfill finds where a recording that should have started at want begins in the
// source's DVR window: the chosen variant's playlist, or the source itself when its URL names a
// playlist. Nothing when the recording is on time, the source is no live HLS playlist, or its
// window does not reach back; err when the playlist did not load.
func planRecordingBackfill(ctx context.Context, sourceURL string, streams recordingStreamChoice, want time.Time) (recordingBackfill, error) {
	return planRecordingBackfillLead(ctx, sourceURL, streams, want, recordingBackfillLead)
}

// planRecordingBackfillLead is planRecordingBackfill starting lead earlier (none on a resume: the
// content up to want is in the file already).
func planRecordingBackfillLead(ctx context.Context, sourceURL string, streams recordingStreamChoice, want time.Time, lead time.Duration) (recordingBackfill, error) {
	if time.Since(want) < recordingBackfillMinLate {
		return recordingBackfill{}, nil
	}
	playlistURL := streams.variant
	if playlistURL == "" {
		source, err := url.Parse(sourceURL)
		if err != nil || (source.Scheme != "http" && source.Scheme != "https") || !hasPlaylistExtension(source) {
			return recordingBackfill{}, nil
		}
		playlistURL = sourceURL
	}
	ctx, cancel := context.WithTimeout(ctx, recordingBackfillBudget)
	defer cancel()
	body, base, err := fetchHLSPlaylist(ctx, recordingBackfillHTTPClient, playlistURL, recordingBackfillPlaylistLimit, true)
	if err != nil {
		return recordingBackfill{}, err
	}
	if variant := firstHLSVariant(body); variant != "" {
		// A master with one variant (several are chosen from in variant.go): its playlist.
		ref, err := url.Parse(variant)
		if err != nil {
			return recordingBackfill{}, nil
		}
		if body, _, err = fetchHLSPlaylist(ctx, recordingBackfillHTTPClient, base.ResolveReference(ref).String(), recordingBackfillPlaylistLimit, true); err != nil {
			return recordingBackfill{}, err
		}
	}
	if streams.variant != "" && streams.playlist != "" && !audioMatchesSegments(ctx, streams, playlistTargetDuration(body)) {
		// FFmpeg counts -live_start_index in every playlist: audio cut in other lengths would start
		// elsewhere.
		return recordingBackfill{}, nil
	}
	return backfillFromPlaylistLead(body, time.Now(), want, lead), nil
}

// recordingBackfillBudget bounds reading the source's window (a long one is megabytes).
const recordingBackfillBudget = 15 * time.Second

// recordingBackfillHTTPClient reads the source's window: within recordingBackfillBudget, on a
// connection closed right after (FFmpeg's follows).
var recordingBackfillHTTPClient = &http.Client{
	Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true},
}

// playlistTargetDuration is a media playlist's EXT-X-TARGETDURATION (0 without one).
func playlistTargetDuration(playlist string) float64 {
	for _, line := range strings.Split(playlist, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "#EXT-X-TARGETDURATION:"); ok {
			d, _ := strconv.ParseFloat(strings.TrimSpace(value), 64)
			return d
		}
	}
	return 0
}

// audioMatchesSegments tells whether the chosen variant's audio renditions are cut like its
// picture (the same target duration). Renditions that cannot be read count as matching.
func audioMatchesSegments(ctx context.Context, streams recordingStreamChoice, target float64) bool {
	master, base, err := fetchHLSPlaylist(ctx, recordingBackfillHTTPClient, streams.playlist, recordingMasterLimit, false)
	if err != nil || target <= 0 {
		return true
	}
	for _, rendition := range hlsAudioRenditions(master, base, streams.variant) {
		// The target duration is in the header: the start of the playlist does.
		head, _, err := fetchHLSPlaylist(ctx, recordingBackfillHTTPClient, rendition, 64<<10, false)
		if err != nil {
			continue
		}
		if d := playlistTargetDuration(head); d > 0 && d != target {
			return false
		}
	}
	return true
}

// hlsAudioRenditions lists the audio renditions (resolved URLs) of the variant at variantURL in a
// master playlist.
func hlsAudioRenditions(master string, base *url.URL, variantURL string) []string {
	resolve := func(raw string) string {
		ref, err := url.Parse(raw)
		if err != nil {
			return raw
		}
		return base.ResolveReference(ref).String()
	}
	group, pending := "", ""
	for _, line := range strings.Split(master, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			pending = hlsAttribute(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"), "AUDIO")
		case line == "" || strings.HasPrefix(line, "#"):
		default:
			if group == "" && resolve(line) == variantURL {
				group = pending
			}
			pending = ""
		}
	}
	if group == "" {
		return nil
	}
	var renditions []string
	for _, line := range strings.Split(master, "\n") {
		attrs, ok := strings.CutPrefix(strings.TrimSpace(line), "#EXT-X-MEDIA:")
		if ok && strings.EqualFold(hlsAttribute(attrs, "TYPE"), "AUDIO") && hlsAttribute(attrs, "GROUP-ID") == group && hlsAttribute(attrs, "URI") != "" {
			renditions = append(renditions, resolve(hlsAttribute(attrs, "URI")))
		}
	}
	return renditions
}

// firstHLSVariant is the first variant a master playlist lists; "" for a media playlist.
func firstHLSVariant(playlist string) string {
	inVariant := false
	for _, line := range strings.Split(playlist, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			inVariant = true
		case line == "" || strings.HasPrefix(line, "#"):
		case inVariant:
			return line
		}
	}
	return ""
}

// backfillFromPlaylist places want in a live media playlist read at fetchedAt: the segment that
// holds it, counted from the playlist's end for FFmpeg, and when that segment began. A window
// that does not reach back that far starts with its oldest segment.
func backfillFromPlaylist(playlist string, fetchedAt, want time.Time) recordingBackfill {
	return backfillFromPlaylistLead(playlist, fetchedAt, want, recordingBackfillLead)
}

// backfillFromPlaylistLead is backfillFromPlaylist starting up to leadTime earlier.
func backfillFromPlaylistLead(playlist string, fetchedAt, want time.Time, leadTime time.Duration) recordingBackfill {
	type segment struct {
		duration float64
		dated    time.Time
	}
	var segments []segment
	var target float64
	duration, dated := -1.0, time.Time{}
	scanner := bufio.NewScanner(strings.NewReader(playlist))
	scanner.Buffer(make([]byte, 0, 64*1024), recordingMasterLimit)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "#EXT-X-ENDLIST" || strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			// Finished (FFmpeg reads it from the start anyway), or a master.
			return recordingBackfill{}
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			target, _ = strconv.ParseFloat(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"), 64)
		case strings.HasPrefix(line, "#EXT-X-PROGRAM-DATE-TIME:"):
			dated = parseProgramDateTime(strings.TrimPrefix(line, "#EXT-X-PROGRAM-DATE-TIME:"))
		case strings.HasPrefix(line, "#EXTINF:"):
			value, _, _ := strings.Cut(strings.TrimPrefix(line, "#EXTINF:"), ",")
			if d, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil && d >= 0 {
				duration = d
			}
		case line == "" || strings.HasPrefix(line, "#"):
		default:
			if duration < 0 {
				return recordingBackfill{}
			}
			segments = append(segments, segment{duration: duration, dated: dated})
			duration, dated = -1, time.Time{}
		}
	}
	if scanner.Err() != nil || len(segments) < 2 {
		return recordingBackfill{}
	}
	seconds := func(d float64) time.Duration { return time.Duration(d * float64(time.Second)) }

	// When each segment begins: by its program date-time, carried over the segments without one,
	// while those agree with the clock; else counted back from the playlist's end, read just now.
	starts := make([]time.Time, len(segments))
	dates := true
	for i, seg := range segments {
		switch {
		case !seg.dated.IsZero():
			starts[i] = seg.dated
		case i > 0 && !starts[i-1].IsZero():
			starts[i] = starts[i-1].Add(seconds(segments[i-1].duration))
		default:
			dates = false
		}
	}
	last := len(segments) - 1
	if !dates || absDuration(fetchedAt.Sub(starts[last].Add(seconds(segments[last].duration)))) > recordingBackfillClockSlack {
		end := fetchedAt
		for i := last; i >= 0; i-- {
			end = end.Add(-seconds(segments[i].duration))
			starts[i] = end
		}
	}

	first := 0
	for first < len(segments) && !starts[first].Add(seconds(segments[first].duration)).After(want) {
		first++
	}
	if first == len(segments) {
		return recordingBackfill{} // want is at the live edge
	}
	if target <= 0 {
		target = segments[last].duration
	}
	lead := 0
	if target > 0 && leadTime > 0 {
		lead = int(math.Ceil(leadTime.Seconds() / target))
	}
	// FFmpeg's default starts three segments before the end: looking back must reach further.
	if len(segments)-first <= 3 {
		return recordingBackfill{}
	}
	// The playlist FFmpeg reads may not have grown: then it starts up to lead segments earlier.
	from := starts[max(first-lead, 0)]
	return recordingBackfill{
		liveStartIndex: -(len(segments) - first + lead),
		from:           from,
		behind:         fetchedAt.Sub(from),
	}
}

// parseProgramDateTime reads an EXT-X-PROGRAM-DATE-TIME value (ISO 8601, with or without a colon
// in the zone offset); zero when unreadable.
func parseProgramDateTime(value string) time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999Z0700"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t
		}
	}
	return time.Time{}
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// recordingBackfillReachSlack: a backfilled attempt whose content got this close to the stop time
// reached it (FFmpeg's -t ends at the first packet past the limit).
const recordingBackfillReachSlack = time.Second

// recordingBackfillReachGrace: how long FFmpeg may take to stop by itself (-t) once its content
// reached the stop time, before the recorder interrupts it (a variable for tests).
var recordingBackfillReachGrace = 5 * time.Second

// recordedContent is how much content a recording holds by FFmpeg's progress reports: its
// finished attempts and the running one (microseconds).
type recordedContent struct {
	done    atomic.Int64
	current atomic.Int64
	since   time.Time // when following began
}

// recordingContentReportWait: FFmpeg reports the content only once every mapped stream has
// packets. Without a report for this long, the time since following began stands in for it.
var recordingContentReportWait = 15 * time.Second

// followContent starts following a recording's content (from its first backfilled attempt on).
func (s *Service) followContent(id string) *recordedContent {
	c := &recordedContent{since: time.Now()}
	s.contents.Store(id, c)
	return c
}

func (s *Service) forgetContent(id string) {
	s.contents.Delete(id)
}

func (c *recordedContent) total() time.Duration {
	return time.Duration(c.done.Load()+c.current.Load()) * time.Microsecond
}

// RecordedContent is how much content a running recording that looked back in its source's
// window holds so far: it fills faster than the clock runs while it catches up (0 before FFmpeg's
// first report). followed is false for other recordings: their content keeps pace with the clock.
func (s *Service) RecordedContent(id string) (content time.Duration, followed bool) {
	v, found := s.contents.Load(id)
	if !found {
		return 0, false
	}
	c := v.(*recordedContent)
	content = c.total()
	if waited := time.Since(c.since); content <= 0 && waited >= recordingContentReportWait {
		// No report (a mapped stream without packets): the clock's pace, which the file outgrows
		// while catching up, so a size estimated from it comes out larger, not smaller.
		content = waited
	}
	return content, true
}

// followAttempt reads a started FFmpeg's progress reports (-progress pipe:3) into content. Once a
// backfilled attempt's content reached the stop time, FFmpeg has recordingBackfillReachGrace to
// stop by itself (-t) before it is interrupted: a mapped stream without packets past the limit
// (subtitles, data) would keep it running. The returned func, called after Wait, tells what the
// attempt recorded and whether it reached the stop time.
func followAttempt(cmd *exec.Cmd, read, write *os.File, content *recordedContent, backfill recordingBackfill, stopAt time.Time) func() (time.Duration, bool) {
	if read == nil {
		return func() (time.Duration, bool) { return 0, false }
	}
	write.Close()
	content.current.Store(0)
	var limit time.Duration
	if backfill.active() {
		limit = backfill.contentLimit(stopAt) - recordingBackfillReachSlack
	}
	var mu sync.Mutex
	var stop *time.Timer
	stalled := false
	began := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer read.Close()
		// Without a content report (out_time stays N/A while a mapped stream has no packets), an
		// output that stopped growing after the stop time has reached it (-t).
		var size int64
		grew := time.Now()
		reported := false
		scanner := bufio.NewScanner(read)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if value, ok := strings.CutPrefix(line, "total_size="); ok {
				if n, err := strconv.ParseInt(value, 10, 64); err == nil && n > size {
					size, grew = n, time.Now()
				}
				mu.Lock()
				if limit > 0 && !reported && stop == nil && time.Now().After(stopAt) && time.Since(grew) >= recordingBackfillStallAfter {
					// Stood still with no content report: counted as reaching the stop time only if
					// it ran at least as long as the content it should hold; otherwise it ended
					// early (what it caught up cannot be told without reports).
					stalled = time.Since(began) >= limit
					stop = time.AfterFunc(0, func() { _ = cmd.Process.Signal(os.Interrupt) })
				}
				mu.Unlock()
				continue
			}
			value, ok := strings.CutPrefix(line, "out_time_us=")
			if !ok {
				continue
			}
			us, err := strconv.ParseInt(value, 10, 64)
			if err != nil || us < 0 {
				continue // N/A before every mapped stream has a packet
			}
			reported = true
			content.current.Store(us)
			mu.Lock()
			if limit > 0 && stop == nil && time.Duration(us)*time.Microsecond >= limit {
				stop = time.AfterFunc(recordingBackfillReachGrace, func() { _ = cmd.Process.Signal(os.Interrupt) })
			}
			mu.Unlock()
		}
		_, _ = io.Copy(io.Discard, read)
	}()
	return func() (time.Duration, bool) {
		<-done
		mu.Lock()
		if stop != nil {
			stop.Stop()
		}
		mu.Unlock()
		us := content.current.Load()
		content.done.Add(us) // briefly counted twice rather than not at all
		content.current.Store(0)
		got := time.Duration(us) * time.Microsecond
		mu.Lock()
		defer mu.Unlock()
		return got, limit > 0 && (got >= limit || stalled)
	}
}

// recordingBackfillStallAfter: how long a backfilled attempt's output may stand still after the
// stop time, without content reports, before it counts as done (a variable for tests).
var recordingBackfillStallAfter = 20 * time.Second
