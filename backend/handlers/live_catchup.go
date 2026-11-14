package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/internal/requestsecurity"
)

// Provider catch-up ("replay", "archive", "timeshift"): the provider keeps past
// programmes and plays them back from a URL that names a start time. Nothing is
// recorded here; the server only reads the provider's catch-up flags and turns a
// guide programme into that provider URL. Clients tune the URL like any channel.

// LiveCatchup tells clients a channel can play past programmes from the provider.
type LiveCatchup struct {
	// Days of archive the provider keeps.
	Days int `json:"days"`
	// Mode is the provider's catch-up scheme (xc, default, append, shift, flussonic).
	Mode string `json:"mode"`
}

// liveCatchupSource is what the server needs to build the archive URL. It never
// leaves the server (unexported, so it is not serialized).
type liveCatchupSource struct {
	mode     string
	template string // M3U catchup-source
	xtream   *xtreamCatchupRef
}

type xtreamCatchupRef struct {
	host, username, password, proxyURL string
	streamID                           int
	extension                          string
}

const (
	defaultM3UCatchupDays = 5
	maxCatchupDays        = 31
	// Providers cut a programme at its listed stop; a little more keeps the ending.
	catchupTailPadding = 2 * time.Minute
)

// m3uCatchup reads catch-up attributes from an #EXTINF line, falling back to
// playlist-wide ones on the #EXTM3U header.
func m3uCatchup(attributes, header map[string]string, streamURL string) (*LiveCatchup, *liveCatchupSource) {
	get := func(key string) string {
		if v := strings.TrimSpace(attributes[key]); v != "" {
			return v
		}
		return strings.TrimSpace(header[key])
	}
	mode := strings.ToLower(get("catchup"))
	if mode == "" {
		mode = strings.ToLower(get("catchup-type"))
	}
	source := get("catchup-source")
	days := atoiPositive(get("catchup-days"))
	if days == 0 {
		days = atoiPositive(get("timeshift"))
	}
	if days == 0 {
		days = atoiPositive(get("tvg-rec"))
	}
	switch mode {
	case "":
		if days == 0 {
			return nil, nil
		}
		// timeshift="N" / tvg-rec="N" alone mean the shift scheme.
		mode = "shift"
		if source != "" {
			mode = "default"
		}
	case "fs":
		mode = "flussonic"
	case "flussonic-hls", "flussonic-ts":
		mode = "flussonic"
	case "vod", "none", "disabled", "0":
		return nil, nil
	}
	switch mode {
	case "default", "append":
		if source == "" {
			return nil, nil
		}
	case "shift", "flussonic", "xc":
	default:
		return nil, nil
	}
	if days == 0 {
		days = defaultM3UCatchupDays
	}
	days = min(days, maxCatchupDays)
	// Schemes derived from the stream URL must be derivable now, or the flag would lie.
	if mode == "flussonic" || mode == "xc" {
		if _, err := deriveCatchupTemplate(mode, streamURL); err != nil {
			return nil, nil
		}
	}
	return &LiveCatchup{Days: days, Mode: mode}, &liveCatchupSource{mode: mode, template: source}
}

func atoiPositive(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

var (
	xcStreamPath     = regexp.MustCompile(`^(.*?)/(?:live/)?([^/]+)/([^/]+)/(\d+)(\.[A-Za-z0-9]+)?$`)
	flussonicTSPath  = regexp.MustCompile(`^(.*)/mpegts$`)
	flussonicHLSPath = regexp.MustCompile(`^(.*)/(index|video|mono)\.m3u8$`)
)

// deriveCatchupTemplate turns a live stream URL into an archive URL template for the
// schemes that have no catchup-source of their own.
func deriveCatchupTemplate(mode, streamURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(streamURL))
	if err != nil || u.Host == "" {
		return "", errors.New("invalid stream URL")
	}
	query := ""
	if u.RawQuery != "" {
		query = "?" + u.RawQuery
	}
	origin := u.Scheme + "://" + u.Host
	switch mode {
	case "flussonic":
		if m := flussonicTSPath.FindStringSubmatch(u.Path); m != nil {
			return origin + m[1] + "/timeshift_abs-{utc}.ts" + query, nil
		}
		if m := flussonicHLSPath.FindStringSubmatch(u.Path); m != nil {
			return origin + m[1] + "/" + m[2] + "-{utc}-{duration}.m3u8" + query, nil
		}
		return "", errors.New("not a Flussonic stream URL")
	case "xc":
		if m := xcStreamPath.FindStringSubmatch(u.Path); m != nil {
			ext := m[5]
			if ext == "" {
				ext = ".ts"
			}
			return origin + m[1] + "/timeshift/" + m[2] + "/" + m[3] + "/{duration:60}/{Y}-{m}-{d}:{H}-{M}/" + m[4] + ext + query, nil
		}
		return "", errors.New("not an Xtream stream URL")
	}
	return "", fmt.Errorf("catch-up mode %q has no derived URL", mode)
}

var catchupToken = regexp.MustCompile(`\$\{[^}]*\}|\{[^}]*\}`)

// expandCatchupTemplate fills the IPTV catch-up placeholders (the set Kodi IPTV
// Simple and most players accept) for a programme from start lasting duration.
// Calendar fields use loc (the provider's clock).
func expandCatchupTemplate(template string, start time.Time, duration time.Duration, now time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	end := start.Add(duration)
	seconds := int64(duration / time.Second)
	return catchupToken.ReplaceAllStringFunc(template, func(token string) string {
		inner := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(token, "$"), "{"), "}")
		name, arg, hasArg := strings.Cut(inner, ":")
		divide := func(v int64) string {
			if !hasArg {
				return strconv.FormatInt(v, 10)
			}
			d, err := strconv.ParseInt(arg, 10, 64)
			if err != nil || d <= 0 {
				return strconv.FormatInt(v, 10)
			}
			return strconv.FormatInt(int64(math.Ceil(float64(v)/float64(d))), 10)
		}
		switch name {
		case "utc", "start", "start-timestamp", "timestamp-start":
			if hasArg {
				return formatCatchupTime(start.In(loc), arg)
			}
			return strconv.FormatInt(start.Unix(), 10)
		case "utcend", "end", "end-timestamp", "timestamp-end":
			if hasArg {
				return formatCatchupTime(end.In(loc), arg)
			}
			return strconv.FormatInt(end.Unix(), 10)
		case "lutc", "now", "timestamp":
			if hasArg {
				return formatCatchupTime(now.In(loc), arg)
			}
			return strconv.FormatInt(now.Unix(), 10)
		case "duration":
			return divide(seconds)
		case "offset":
			return divide(int64(now.Sub(start) / time.Second))
		case "Y", "m", "d", "H", "M", "S":
			return formatCatchupTime(start.In(loc), name)
		}
		return token
	})
}

// formatCatchupTime renders a Y/m/d/H/M/S pattern (as in {utc:Y-m-d:H-M}).
func formatCatchupTime(t time.Time, pattern string) string {
	var b strings.Builder
	for _, r := range pattern {
		switch r {
		case 'Y':
			fmt.Fprintf(&b, "%04d", t.Year())
		case 'm':
			fmt.Fprintf(&b, "%02d", int(t.Month()))
		case 'd':
			fmt.Fprintf(&b, "%02d", t.Day())
		case 'H':
			fmt.Fprintf(&b, "%02d", t.Hour())
		case 'M':
			fmt.Fprintf(&b, "%02d", t.Minute())
		case 'S':
			fmt.Fprintf(&b, "%02d", t.Second())
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// appendCatchupQuery adds the shift scheme's utc/lutc parameters to a live URL.
func appendCatchupQuery(streamURL string) string {
	sep := "?"
	if strings.Contains(streamURL, "?") {
		sep = "&"
	}
	return streamURL + sep + "utc={utc}&lutc={lutc}"
}

// catchupTemplate is the archive URL template for a channel.
func (s *liveCatchupSource) catchupTemplate(streamURL string) (string, error) {
	switch s.mode {
	case "default":
		return s.template, nil
	case "append":
		return strings.TrimSpace(streamURL) + s.template, nil
	case "shift":
		return appendCatchupQuery(strings.TrimSpace(streamURL)), nil
	case "flussonic", "xc":
		return deriveCatchupTemplate(s.mode, streamURL)
	}
	return "", fmt.Errorf("unsupported catch-up mode %q", s.mode)
}

// xtreamCatchup marks an Xtream channel that has an archive.
func xtreamCatchup(stream XtreamStream, host, username, password, proxyURL string) (*LiveCatchup, *liveCatchupSource) {
	if atoiPositive(string(stream.TVArchive)) == 0 {
		return nil, nil
	}
	days := atoiPositive(string(stream.TVArchiveDuration))
	if days == 0 {
		days = 1
	}
	days = min(days, maxCatchupDays)
	return &LiveCatchup{Days: days, Mode: "xc"}, &liveCatchupSource{
		mode: "xc",
		xtream: &xtreamCatchupRef{
			host: host, username: username, password: password, proxyURL: proxyURL,
			streamID: stream.StreamID, extension: ".ts",
		},
	}
}

// Xtream panels read timeshift start times on their own clock, which player_api.php
// reports in server_info.
type xtreamClockEntry struct {
	loc       *time.Location
	expiresAt time.Time
}

var (
	xtreamClockMu    sync.Mutex
	xtreamClockCache = map[string]xtreamClockEntry{}
)

const xtreamClockTTL = 6 * time.Hour

func (h *LiveHandler) xtreamPanelLocation(ctx context.Context, ref *xtreamCatchupRef) *time.Location {
	key := strings.ToLower(strings.TrimRight(ref.host, "/")) + "|" + ref.username
	xtreamClockMu.Lock()
	entry, ok := xtreamClockCache[key]
	xtreamClockMu.Unlock()
	if ok && time.Now().Before(entry.expiresAt) {
		return entry.loc
	}
	loc, err := h.fetchXtreamPanelLocation(ctx, ref)
	ttl := xtreamClockTTL
	if err != nil {
		log.Printf("[live-catchup] panel clock unavailable host=%q: %v; using UTC", ref.host, err)
		loc, ttl = time.UTC, 10*time.Minute
	}
	xtreamClockMu.Lock()
	xtreamClockCache[key] = xtreamClockEntry{loc: loc, expiresAt: time.Now().Add(ttl)}
	xtreamClockMu.Unlock()
	return loc
}

type xtreamServerInfo struct {
	ServerInfo struct {
		Timezone     string     `json:"timezone"`
		TimeNow      string     `json:"time_now"`
		TimestampNow flexString `json:"timestamp_now"`
	} `json:"server_info"`
}

func (h *LiveHandler) fetchXtreamPanelLocation(ctx context.Context, ref *xtreamCatchupRef) (*time.Location, error) {
	host := strings.TrimRight(ref.host, "/")
	infoURL := fmt.Sprintf("%s/player_api.php?username=%s&password=%s", host, url.QueryEscape(ref.username), url.QueryEscape(ref.password))
	ctx, cancel := context.WithTimeout(ctx, xtreamCategoryTimeout)
	defer cancel()
	resp, _, err := h.fetchXtreamWithUAFallback(ctx, h.liveHTTPClient(ref.proxyURL), infoURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, metadataResponseError(resp)
	}
	var info xtreamServerInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode server_info: %w", err)
	}
	return xtreamLocationFromServerInfo(info.ServerInfo.Timezone, info.ServerInfo.TimeNow, string(info.ServerInfo.TimestampNow), time.Now())
}

// xtreamLocationFromServerInfo prefers the panel's IANA zone; otherwise it derives a
// fixed offset from the panel's wall clock (time_now) against UTC, to the quarter hour.
func xtreamLocationFromServerInfo(zone, timeNow, timestampNow string, now time.Time) (*time.Location, error) {
	if zone = strings.TrimSpace(zone); zone != "" {
		if loc, err := time.LoadLocation(zone); err == nil {
			return loc, nil
		}
	}
	wall, err := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(timeNow))
	if err != nil {
		return nil, fmt.Errorf("panel reports no usable clock (timezone %q)", zone)
	}
	reference := now.UTC()
	if ts, err := strconv.ParseInt(strings.TrimSpace(timestampNow), 10, 64); err == nil && ts > 0 {
		reference = time.Unix(ts, 0).UTC()
	}
	quarters := math.Round(wall.Sub(reference).Minutes() / 15)
	offset := int(quarters) * 15 * 60
	return time.FixedZone(fmt.Sprintf("panel%+d", offset/60), offset), nil
}

// LiveCatchupResponse is what GET /api/live/catchup returns: the provider URL to tune
// (as /api/live/hls/start's url) for the programme from offsetSeconds in.
type LiveCatchupResponse struct {
	URL string `json:"url"`
	// Seconds into the programme the stream starts at (the provider may round down).
	OffsetSeconds int `json:"offsetSeconds"`
	// Seconds the stream runs from there, padding included.
	DurationSeconds int `json:"durationSeconds"`
}

var errCatchupUnavailable = errors.New("catch-up is not available for this programme")

// buildCatchupURL resolves the provider archive URL for a programme. start/stop are
// real (provider) times; offset is how far into the programme to begin.
func (h *LiveHandler) buildCatchupURL(ctx context.Context, channel LiveChannel, start, stop time.Time, offset time.Duration, now time.Time) (LiveCatchupResponse, error) {
	if channel.Catchup == nil || channel.catchupSource == nil {
		return LiveCatchupResponse{}, errCatchupUnavailable
	}
	if !stop.After(start) {
		return LiveCatchupResponse{}, errors.New("programme stop must be after its start")
	}
	if offset < 0 {
		offset = 0
	}
	playStart := start.Add(offset)
	if !playStart.Before(now.Add(-30 * time.Second)) {
		return LiveCatchupResponse{}, errors.New("that point has not aired yet")
	}
	if playStart.Before(now.Add(-time.Duration(channel.Catchup.Days) * 24 * time.Hour)) {
		return LiveCatchupResponse{}, fmt.Errorf("the provider keeps %d days of this channel", channel.Catchup.Days)
	}
	source := channel.catchupSource
	loc := time.UTC
	var template string
	if source.xtream != nil {
		// Xtream timeshift starts on whole minutes of the panel's clock.
		playStart = playStart.Truncate(time.Minute)
		loc = h.xtreamPanelLocation(ctx, source.xtream)
		ref := source.xtream
		template = fmt.Sprintf("%s/timeshift/%s/%s/{duration:60}/{Y}-{m}-{d}:{H}-{M}/%d%s",
			strings.TrimRight(ref.host, "/"), url.PathEscape(ref.username), url.PathEscape(ref.password), ref.streamID, ref.extension)
	} else {
		var err error
		if template, err = source.catchupTemplate(channel.URL); err != nil {
			return LiveCatchupResponse{}, err
		}
		if source.mode == "xc" {
			playStart = playStart.Truncate(time.Minute)
		}
	}
	end := stop.Add(catchupTailPadding)
	if end.After(now) {
		end = now // a programme still on air: replay up to now
		if end.Sub(playStart) < time.Minute {
			end = playStart.Add(time.Minute)
		}
	}
	duration := end.Sub(playStart)
	return LiveCatchupResponse{
		URL:             expandCatchupTemplate(template, playStart, duration, now, loc),
		OffsetSeconds:   int(max(playStart.Sub(start), 0) / time.Second),
		DurationSeconds: int(duration / time.Second),
	}, nil
}

// GetCatchup answers GET /api/live/catchup?channelId=&sourceId=&start=&stop=&offset=
// with the provider URL that replays a past guide programme. start/stop are the guide's
// times as /api/live/epg returns them, the profile's EPG offset applied, and are used as they
// are: the offset corrects the guide to when programmes really air, which is also how the
// provider files its archive (on its own clock; see xtreamPanelLocation).
func (h *LiveHandler) GetCatchup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(status int, err error) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
	}
	query := r.URL.Query()
	channelID := strings.TrimSpace(query.Get("channelId"))
	start, errStart := time.Parse(time.RFC3339, strings.TrimSpace(query.Get("start")))
	stop, errStop := time.Parse(time.RFC3339, strings.TrimSpace(query.Get("stop")))
	if channelID == "" || errStart != nil || errStop != nil {
		fail(http.StatusBadRequest, errors.New("channelId, start and stop (RFC 3339) are required"))
		return
	}
	offsetSeconds, _ := strconv.Atoi(query.Get("offset"))
	if !requireLiveStreamProfile(w, r, h.usersSvc) {
		return
	}
	channels, err := h.FetchFilteredChannelsForRequest(r)
	if err != nil {
		fail(http.StatusBadGateway, err)
		return
	}
	var channel *LiveChannel
	for i := range channels {
		if channels[i].ID == channelID {
			channel = &channels[i]
			break
		}
	}
	if channel == nil {
		fail(http.StatusNotFound, errors.New("channel not found"))
		return
	}
	response, err := h.buildCatchupURL(r.Context(), *channel, start, stop, time.Duration(offsetSeconds)*time.Second, time.Now())
	if err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, errCatchupUnavailable) {
			status = http.StatusNotFound
		}
		fail(status, err)
		return
	}
	if err := requestsecurity.ValidateOutboundURL(r.Context(), response.URL, configuredProviderHostPolicy(h.cfgManager)); err != nil {
		fail(http.StatusBadGateway, fmt.Errorf("catch-up URL is not allowed: %w", err))
		return
	}
	h.catchupGrants.issue(r.URL.Query().Get("profileId"), response.URL, time.Now())
	log.Printf("[live-catchup] channel=%q mode=%s offset=%ds duration=%ds upstream=%s", channel.ID, channel.Catchup.Mode, response.OffsetSeconds, response.DurationSeconds, requestsecurity.URLForLog(response.URL))
	_ = json.NewEncoder(w).Encode(response)
}
