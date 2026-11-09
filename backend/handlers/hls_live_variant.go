package handlers

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"novastream/internal/netproxy"
)

// liveHLSMasterLimit bounds how much of an upstream playlist is read while choosing a variant.
const liveHLSMasterLimit = 1 << 20

// resolveLiveHLSVariant returns the single media playlist FFmpeg should read for a live HLS source
// whose URL answers with a master playlist, or "" to keep reading the URL as before.
//
// Given a master playlist, FFmpeg's default stream selection takes video from the highest
// resolution variant and audio from whichever variant ranks first, often a different one, plus any
// WebVTT rendition. On Pluto TV (and stitched-ad FAST channels in general) those are three separate
// live playlists that splice ads in independently: at the first ad break their timestamps jump at
// different moments, the HLS demuxer's cross-playlist interleaving stalls, and the session stops
// producing segments for good. One muxed variant keeps audio and video on a single timeline. It is
// also the bandwidth of one rendition instead of two or three.
//
// Only masters whose variants carry their own audio qualify: a variant that names an AUDIO group
// needs the master's renditions, so the master is kept. Redirects are followed and the variant is
// resolved against the final URL, which also spares redirectors such as pluto-for-channels a second
// tune (each /watch request starts a new Pluto session).
func resolveLiveHLSVariant(ctx context.Context, rawURL string, headers map[string]string) string {
	client, err := netproxy.NewHTTPClientWithOptions(netproxy.HTTPClientOptions{ResponseHeaderTimeout: 15 * time.Second}, "")
	if err != nil {
		return ""
	}
	defer client.CloseIdleConnections()
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", liveStreamUserAgent)
	applyRequestHeaders(req.Header, headers)
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, liveHLSMasterLimit))
	if err != nil {
		return ""
	}
	return pickLiveHLSVariant(string(body), resp.Request.URL)
}

// pickLiveHLSVariant chooses the highest-bandwidth variant of a master playlist, resolved against
// base. It returns "" for a media playlist, for a master whose variants use separate audio
// renditions, or for anything it cannot parse.
func pickLiveHLSVariant(playlist string, base *url.URL) string {
	scanner := bufio.NewScanner(strings.NewReader(playlist))
	scanner.Buffer(make([]byte, 0, 64*1024), liveHLSMasterLimit)
	first := true
	var (
		pending    bool
		pendingBW  int64
		bestBW     int64 = -1
		bestURI    string
		sawVariant bool
	)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if first {
			if !strings.HasPrefix(line, "#EXTM3U") {
				return ""
			}
			first = false
			continue
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			attrs := parseHLSAttributes(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
			if strings.TrimSpace(attrs["AUDIO"]) != "" {
				return ""
			}
			bandwidth, _ := strconv.ParseInt(attrs["BANDWIDTH"], 10, 64)
			pending, pendingBW, sawVariant = true, bandwidth, true
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if pending {
			if pendingBW > bestBW {
				bestBW, bestURI = pendingBW, line
			}
			pending = false
		}
	}
	if scanner.Err() != nil || !sawVariant || bestURI == "" {
		return ""
	}
	ref, err := url.Parse(bestURI)
	if err != nil {
		return ""
	}
	resolved := ref
	if base != nil {
		resolved = base.ResolveReference(ref)
	}
	if scheme := strings.ToLower(resolved.Scheme); scheme != "http" && scheme != "https" {
		return ""
	}
	return resolved.String()
}

// parseHLSAttributes splits an HLS attribute list (KEY=value,KEY="quoted, value").
func parseHLSAttributes(list string) map[string]string {
	attrs := map[string]string{}
	for len(list) > 0 {
		eq := strings.IndexByte(list, '=')
		if eq < 0 {
			break
		}
		key := strings.ToUpper(strings.TrimSpace(list[:eq]))
		list = list[eq+1:]
		var value string
		if strings.HasPrefix(list, "\"") {
			end := strings.IndexByte(list[1:], '"')
			if end < 0 {
				value, list = list[1:], ""
			} else {
				value, list = list[1:1+end], list[2+end:]
			}
			if comma := strings.IndexByte(list, ','); comma >= 0 {
				list = list[comma+1:]
			} else {
				list = ""
			}
		} else if comma := strings.IndexByte(list, ','); comma >= 0 {
			value, list = list[:comma], list[comma+1:]
		} else {
			value, list = list, ""
		}
		attrs[key] = value
	}
	return attrs
}
