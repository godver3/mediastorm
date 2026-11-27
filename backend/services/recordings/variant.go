package recordings

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// An adaptive HLS channel lists several variants of one picture (360p … 1080p, or the same 720p
// from a second CDN, as ZDF does), each with its own audio renditions. FFmpeg's HLS demuxer makes
// every variant a program (its id is the variant's position in the master playlist), so "-map 0"
// recorded every variant and every rendition: ZDFinfo recordings carried two identical 720p
// pictures and six audio tracks. A recording takes only the best variant that loads (highest
// bandwidth, the first of equals) with its own audio tracks, all of them (programme language,
// original sound, audio description).
//
// Attempts of one recording append to one file, so its stream layout must not change: once
// something is recorded, a retry looks the chosen variant up again by its URL (its position may
// have moved) or takes an identical one (the same picture from another CDN), and otherwise keeps
// the previous choice.
//
// A source is only looked at when it may be a playlist (a HEAD request unless its URL names a
// playlist), on connections that are closed right after: a transport stream from an IPTV account
// limited to one connection must not see a second one. A source found not to be an adaptive
// master is not looked at again for a while. The lookups are short: they delay the recording.

const (
	recordingRequestTimeout = 3 * time.Second
	recordingChoiceBudget   = 6 * time.Second // the first attempt
	recordingRetryBudget    = 3 * time.Second // a retry, while the recording has a gap
	recordingMasterLimit    = 1 << 20
	recordingMasterUA       = "VLC/3.0.20 LibVLC/3.0.20"
)

// recordingHTTPClient keeps no connection open after a request, also after redirects: FFmpeg's
// own connection follows right after.
var recordingHTTPClient = &http.Client{
	Timeout:   recordingRequestTimeout,
	Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true},
}

// recordingAllStreams is the map for a source that is not an adaptive HLS channel.
var recordingAllStreams = []string{"-map", "0"}

// recordingMediaExtensions are sources that are no playlist: nothing to look up.
var recordingMediaExtensions = map[string]bool{
	".ts": true, ".m2ts": true, ".mts": true, ".mp4": true, ".m4v": true, ".mkv": true, ".webm": true,
	".mpg": true, ".mpeg": true, ".flv": true, ".avi": true, ".mov": true, ".aac": true, ".mp3": true,
}

// recordingStreamChoice is what a recording maps from its source.
type recordingStreamChoice struct {
	maps            []string
	pictureAndSound bool   // everything's picture and sound only (a recording that looked back)
	variant         string // the chosen variant's playlist URL; "" for all streams
	signature       string // the chosen variant's BANDWIDTH, RESOLUTION, CODECS and audio renditions
	playlist        string // where the master was read: the source is a playlist (no HEAD on a retry)
	final           bool   // all streams for good: no playlist, or FFmpeg found nothing in the variant
}

func (c recordingStreamChoice) args() []string {
	if len(c.maps) == 0 {
		return recordingAllStreams
	}
	return c.maps
}

// pictureAndSoundStreams maps a source's picture and sound only, for good.
func pictureAndSoundStreams() recordingStreamChoice {
	return recordingStreamChoice{maps: []string{"-map", "0:v?", "-map", "0:a?"}, pictureAndSound: true, final: true}
}

// allRecordingStreams is the choice that maps everything.
func allRecordingStreams() recordingStreamChoice {
	return recordingStreamChoice{maps: recordingAllStreams}
}

// chooseRecordingStreams picks the streams of sourceURL to record. keepLayout is set once the
// recording has data: then only the previous variant (looked up by its URL) or an identical one
// may follow, and anything else keeps the previous choice.
func chooseRecordingStreams(ctx context.Context, sourceURL string, previous recordingStreamChoice, keepLayout bool) recordingStreamChoice {
	if previous.final || (keepLayout && previous.variant == "") {
		return previous
	}
	fallback := allRecordingStreams()
	if keepLayout {
		fallback = previous
	}
	everything := recordingStreamChoice{maps: recordingAllStreams, final: true}
	source, err := url.Parse(sourceURL)
	if err != nil || (source.Scheme != "http" && source.Scheme != "https") || recordingMediaExtensions[strings.ToLower(path.Ext(source.Path))] {
		return everything
	}
	if !keepLayout && notAdaptiveRecently(sourceURL) {
		// Looked at for an earlier recording: not an adaptive master. No requests this time.
		return everything
	}
	notAdaptive := func() recordingStreamChoice {
		if keepLayout {
			return fallback
		}
		rememberNotAdaptive(sourceURL)
		return everything
	}
	budget := recordingChoiceBudget
	if previous.playlist != "" {
		budget = recordingRetryBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	// A URL named like a playlist, or one seen to be a playlist before, needs no HEAD. The master
	// is read from the source URL, as FFmpeg does: a redirect target may carry an expiring token.
	if previous.playlist == "" && !hasPlaylistExtension(source) {
		switch isPlaylist, sure := recordingSourceIsPlaylist(ctx, sourceURL); {
		case !isPlaylist && sure:
			// Not a playlist, or a server that does not answer HEAD (and no playlist URL).
			return notAdaptive()
		case !isPlaylist:
			// No answer (timeout, refused, overloaded): everything for this recording without
			// asking again on every retry, but the next recording asks again.
			if keepLayout {
				return fallback
			}
			return everything
		}
	}
	body, master, err := fetchRecordingPlaylist(ctx, sourceURL)
	if err != nil {
		return fallback
	}
	variants := rankedHLSVariants(body)
	if len(variants) == 0 {
		// A media playlist, a single variant or no picture: everything, for good.
		return notAdaptive()
	}
	for i, variant := range variants {
		if ref, err := url.Parse(variant.uri); err == nil {
			variants[i].uri = master.ResolveReference(ref).String()
		}
	}
	choose := func(variant hlsVariant) recordingStreamChoice {
		return recordingStreamChoice{
			maps:      []string{"-map", fmt.Sprintf("0:p:%d:v", variant.program), "-map", fmt.Sprintf("0:p:%d:a?", variant.program)},
			variant:   variant.uri,
			signature: variant.signature,
			playlist:  master.String(),
		}
	}
	if keepLayout {
		// The file has the previous variant's layout. That variant, wherever the master lists it
		// now, while it loads; else one with the same signature (the same picture and audio
		// tracks from another CDN) that loads; else that variant anyway (FFmpeg retries its
		// playlist itself), else the previous choice.
		same, found := sameHLSVariant(variants, previous)
		if found {
			if _, _, err := fetchRecordingPlaylist(ctx, same.uri); err == nil {
				return choose(same)
			}
		}
		for _, variant := range variants {
			if variant.signature != previous.signature || (found && variant == same) {
				continue
			}
			if _, _, err := fetchRecordingPlaylist(ctx, variant.uri); err == nil {
				return choose(variant)
			}
		}
		if found {
			return choose(same)
		}
		return previous
	}
	if previous.variant != "" {
		// Nothing recorded yet: the previous variant first while it loads.
		if same, ok := sameHLSVariant(variants, previous); ok {
			sort.SliceStable(variants, func(i, j int) bool { return variants[i] == same && variants[j] != same })
		}
	}
	for _, variant := range variants {
		if _, _, err := fetchRecordingPlaylist(ctx, variant.uri); err != nil {
			continue
		}
		return choose(variant)
	}
	return fallback
}

// sameHLSVariant finds the previous variant in a fresh master: by its full URL (the one with the
// same signature first: a master may list one picture with several audio groups), else by its
// URL without query (tokens change) when that is unique and the signature matches.
func sameHLSVariant(variants []hlsVariant, previous recordingStreamChoice) (hlsVariant, bool) {
	var byURL []hlsVariant
	for _, variant := range variants {
		if variant.uri == previous.variant {
			if variant.signature == previous.signature {
				return variant, true
			}
			byURL = append(byURL, variant)
		}
	}
	if len(byURL) > 0 {
		return byURL[0], true
	}
	var match hlsVariant
	matches := 0
	for _, variant := range variants {
		if withoutQuery(variant.uri) == withoutQuery(previous.variant) {
			match = variant
			matches++
		}
	}
	return match, matches == 1 && match.signature == previous.signature
}

// withoutQuery is a URL without query and fragment.
func withoutQuery(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
	return u.String()
}

// recordingNotAdaptive remembers sources seen not to be an adaptive master (source URL → until
// when), so the next recordings of a channel ask its server nothing.
var recordingNotAdaptive sync.Map

const recordingNotAdaptiveFor = 6 * time.Hour

func notAdaptiveRecently(sourceURL string) bool {
	until, ok := recordingNotAdaptive.Load(sourceURL)
	if !ok {
		return false
	}
	if time.Now().After(until.(time.Time)) {
		recordingNotAdaptive.CompareAndDelete(sourceURL, until)
		return false
	}
	return true
}

// rememberNotAdaptive caches sourceURL and drops expired entries (token URLs would pile up).
func rememberNotAdaptive(sourceURL string) {
	now := time.Now()
	recordingNotAdaptive.Range(func(key, until any) bool {
		if now.After(until.(time.Time)) {
			recordingNotAdaptive.CompareAndDelete(key, until)
		}
		return true
	})
	recordingNotAdaptive.Store(sourceURL, now.Add(recordingNotAdaptiveFor))
}

func hasPlaylistExtension(u *url.URL) bool {
	ext := strings.ToLower(path.Ext(u.Path))
	return ext == ".m3u8" || ext == ".m3u"
}

// recordingSourceIsPlaylist tells from a HEAD request (following redirects) whether sourceURL
// serves an HLS playlist. A server that does not answer HEAD counts by the extension of where its
// redirects lead. sure is false when the server could not be asked (no answer, a server error).
func recordingSourceIsPlaylist(ctx context.Context, sourceURL string) (isPlaylist, sure bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, sourceURL, nil)
	if err != nil {
		return false, true
	}
	req.Header.Set("User-Agent", recordingMasterUA)
	req.Close = true
	resp, err := recordingHTTPClient.Do(req)
	if err != nil {
		return false, false
	}
	resp.Body.Close()
	if hasPlaylistExtension(resp.Request.URL) {
		return true, true
	}
	switch {
	case resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented:
		// HEAD is not supported here.
		return false, true
	case resp.StatusCode >= http.StatusBadRequest:
		// Offline, an expired token, a connection limit, a rate limit: nothing found out.
		return false, false
	}
	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if contentType == "" {
		// A HEAD answer without a type says nothing about what GET serves.
		return false, false
	}
	return strings.Contains(contentType, "mpegurl"), true
}

// fetchRecordingPlaylist reads an HLS playlist (bounded) and returns it with its final URL.
func fetchRecordingPlaylist(ctx context.Context, rawURL string) (string, *url.URL, error) {
	return fetchHLSPlaylist(ctx, recordingHTTPClient, rawURL, recordingMasterLimit, false)
}

// fetchHLSPlaylist reads an HLS playlist of up to limit bytes; a longer one is cut there, or with
// whole set refused.
func fetchHLSPlaylist(ctx context.Context, client *http.Client, rawURL string, limit int64, whole bool) (string, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("User-Agent", recordingMasterUA)
	req.Close = true
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return "", nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", nil, err
	}
	if int64(len(body)) > limit {
		if whole {
			return "", nil, fmt.Errorf("playlist larger than %d bytes", limit)
		}
		body = body[:limit]
	}
	if !strings.HasPrefix(strings.TrimSpace(string(body)), "#EXTM3U") {
		return "", nil, fmt.Errorf("not an HLS playlist")
	}
	return string(body), resp.Request.URL, nil
}

type hlsVariant struct {
	program   int // the variant's position, FFmpeg's program id
	bandwidth int64
	signature string // BANDWIDTH, RESOLUTION, CODECS, audio renditions: the same layout from another CDN matches
	uri       string
}

// rankedHLSVariants lists a master playlist's variants with a picture best first (highest
// bandwidth, then the earlier one); empty for a media playlist, a master with a single variant
// (nothing to choose) or one without pictures (a radio channel: all of it, as before).
func rankedHLSVariants(playlist string) []hlsVariant {
	scanner := bufio.NewScanner(strings.NewReader(playlist))
	scanner.Buffer(make([]byte, 0, 64*1024), recordingMasterLimit)
	var variants []hlsVariant
	var groups []string
	renditions := map[string]int{} // audio renditions per GROUP-ID
	total := 0
	pending, inVariant := "", false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "#EXT-X-MEDIA:"):
			attrs := strings.TrimPrefix(line, "#EXT-X-MEDIA:")
			if strings.EqualFold(hlsAttribute(attrs, "TYPE"), "AUDIO") {
				renditions[hlsAttribute(attrs, "GROUP-ID")]++
			}
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			pending, inVariant = strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"), true
		case line == "" || strings.HasPrefix(line, "#"):
		case inVariant:
			if hlsVariantHasVideo(pending) {
				bandwidth, _ := strconv.ParseInt(hlsAttribute(pending, "BANDWIDTH"), 10, 64)
				variants = append(variants, hlsVariant{
					program:   total,
					bandwidth: bandwidth,
					signature: strings.Join([]string{hlsAttribute(pending, "BANDWIDTH"), hlsAttribute(pending, "RESOLUTION"), hlsAttribute(pending, "CODECS")}, "|"),
					uri:       line,
				})
				groups = append(groups, hlsAttribute(pending, "AUDIO"))
			}
			total++
			pending, inVariant = "", false
		}
	}
	if scanner.Err() != nil || total < 2 || len(variants) == 0 {
		return nil
	}
	for i := range variants {
		// The number of audio tracks belongs to the layout too.
		variants[i].signature += "|" + strconv.Itoa(renditions[groups[i]])
	}
	sort.SliceStable(variants, func(i, j int) bool { return variants[i].bandwidth > variants[j].bandwidth })
	return variants
}

// hlsVariantHasVideo tells from RESOLUTION and CODECS whether a variant carries a picture; one
// that declares neither counts as one.
func hlsVariantHasVideo(attrs string) bool {
	if hlsAttribute(attrs, "RESOLUTION") != "" {
		return true
	}
	codecs := hlsAttribute(attrs, "CODECS")
	if strings.TrimSpace(codecs) == "" {
		return true
	}
	for _, codec := range strings.Split(codecs, ",") {
		family, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(codec)), ".")
		switch family {
		case "avc1", "avc3", "hvc1", "hev1", "dvh1", "dvhe", "dvav", "dva1", "vp08", "vp09", "av01", "mp4v":
			return true
		}
	}
	return false
}

// hlsAttribute reads an attribute (KEY=value or KEY="value") from an HLS attribute list.
func hlsAttribute(list, key string) string {
	inQuotes := false
	start := 0
	for i := 0; i <= len(list); i++ {
		if i < len(list) && list[i] == '"' {
			inQuotes = !inQuotes
		}
		if i == len(list) || (list[i] == ',' && !inQuotes) {
			name, value, ok := strings.Cut(strings.TrimSpace(list[start:i]), "=")
			if ok && strings.EqualFold(strings.TrimSpace(name), key) {
				return strings.Trim(strings.TrimSpace(value), `"`)
			}
			start = i + 1
		}
	}
	return ""
}
