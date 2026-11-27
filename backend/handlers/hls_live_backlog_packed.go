package handlers

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
)

// Packed audio (HLS's raw audio segments: ADTS AAC, MP3, AC-3 or E-AC-3 behind an ID3 tag), as ARD
// and ZDF serve their audio renditions. Its timeline is the PRIV frame
// "com.apple.streaming.transportStreamTimestamp" of the ID3 tag: the 33-bit MPEG-TS timestamp of the
// segment's first sample. FFmpeg reads it only in its HLS demuxer, so a backlog segment fetches the
// audio segment itself and hands FFmpeg the timestamp as -itsoffset, which lines the audio up with
// the video segment's own MPEG-TS timestamps (-copyts).

const (
	// liveBacklogPackedAudioLimit bounds one packed audio segment (a few seconds of audio).
	liveBacklogPackedAudioLimit = 4 << 20
	packedAudioTimestampOwner   = "com.apple.streaming.transportStreamTimestamp"
)

// errPackedAudioNoTimestamp reports packed audio without the ID3 timestamp: it cannot be lined up
// with the video.
var errPackedAudioNoTimestamp = errors.New("packed audio without its ID3 timestamp")

// Segment formats the backlog can read.
const (
	liveBacklogSegmentTS     = "ts"
	liveBacklogSegmentPacked = "packed"
)

// liveBacklogSegmentKind sniffs a segment's first bytes: MPEG-TS, packed audio (an ID3 tag or an
// ADTS header first), or "" for anything else or an error.
func liveBacklogSegmentKind(ctx context.Context, client *http.Client, rawURL string, headers map[string]string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", liveStreamUserAgent)
	req.Header.Set("Range", "bytes=0-187")
	applyRequestHeaders(req.Header, headers)
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return ""
	}
	head := make([]byte, 3)
	n, _ := io.ReadFull(resp.Body, head)
	return segmentKindFromHead(head[:n])
}

func segmentKindFromHead(head []byte) string {
	switch {
	case len(head) >= 1 && head[0] == liveBacklogMPEGTSSync:
		return liveBacklogSegmentTS
	case len(head) >= 3 && string(head[:3]) == "ID3":
		return liveBacklogSegmentPacked
	}
	// Anything else, including ADTS without an ID3 tag first: audio that cannot be placed in time.
	return ""
}

// mpegTSTimeApart is a - b (seconds) on the 33-bit MPEG-TS clock, which wraps every ~26.5 h:
// FFmpeg reports a stream starting just before the wrap as negative, while an ID3 timestamp stays
// positive.
func mpegTSTimeApart(a, b float64) float64 {
	const wrap = float64(1<<33) / 90000
	d := math.Mod(a-b, wrap)
	switch {
	case d > wrap/2:
		d -= wrap
	case d < -wrap/2:
		d += wrap
	}
	return d
}

// fetchPackedAudio fetches a packed audio segment and returns it with its start time in seconds
// (the ID3 timestamp).
func fetchPackedAudio(ctx context.Context, rawURL string, headers map[string]string) ([]byte, float64, error) {
	client, err := newLiveBacklogClient()
	if err != nil {
		return nil, 0, err
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", liveStreamUserAgent)
	applyRequestHeaders(req.Header, headers)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, 0, fmt.Errorf("packed audio segment: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, liveBacklogPackedAudioLimit+1))
	if err != nil {
		return nil, 0, err
	}
	if len(data) > liveBacklogPackedAudioLimit {
		return nil, 0, fmt.Errorf("packed audio segment larger than %d bytes", liveBacklogPackedAudioLimit)
	}
	start, ok := packedAudioTimestamp(data)
	if !ok {
		return nil, 0, errPackedAudioNoTimestamp
	}
	return data, start, nil
}

// packedAudioTimestamp reads the start time (seconds) from a packed audio segment's leading ID3v2
// tag: the PRIV frame owned by com.apple.streaming.transportStreamTimestamp, whose 8 bytes hold a
// 33-bit 90 kHz timestamp.
func packedAudioTimestamp(data []byte) (float64, bool) {
	if len(data) < 10 || string(data[:3]) != "ID3" {
		return 0, false
	}
	version := data[3]
	tagSize := syncsafe(data[6:10])
	end := 10 + tagSize
	if end > len(data) {
		return 0, false
	}
	pos := 10
	if data[5]&0x40 != 0 && pos+4 <= end {
		// An extended header: skip it (its size is syncsafe in v2.4, plain in v2.3).
		ext := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		if version >= 4 {
			ext = syncsafe(data[pos : pos+4])
		} else {
			ext += 4
		}
		pos += ext
	}
	for pos+10 <= end {
		id := string(data[pos : pos+4])
		size := int(binary.BigEndian.Uint32(data[pos+4 : pos+8]))
		if version >= 4 {
			size = syncsafe(data[pos+4 : pos+8])
		}
		if id == "\x00\x00\x00\x00" || size <= 0 || pos+10+size > end {
			return 0, false
		}
		body := data[pos+10 : pos+10+size]
		if id == "PRIV" {
			if owner, rest, ok := bytes.Cut(body, []byte{0}); ok && string(owner) == packedAudioTimestampOwner && len(rest) >= 8 {
				ts := binary.BigEndian.Uint64(rest[:8]) & (1<<33 - 1)
				return float64(ts) / 90000, true
			}
		}
		pos += 10 + size
	}
	return 0, false
}

// syncsafe decodes an ID3v2 syncsafe integer (7 bits per byte).
func syncsafe(b []byte) int {
	n := 0
	for _, c := range b {
		n = n<<7 | int(c&0x7f)
	}
	return n
}
