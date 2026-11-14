package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"novastream/config"
	"novastream/internal/streamheaders"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type staticLiveChannelProvider struct {
	channels []LiveChannel
	err      error
}

func (p staticLiveChannelProvider) FetchFilteredChannelsForRequest(*http.Request) ([]LiveChannel, error) {
	return p.channels, p.err
}

func TestLiveQualitySummaryChoosesHighestVideoAndPreservesAudio(t *testing.T) {
	result := summarizeLiveQuality(ffprobeOutput{Format: ffprobeFormat{FormatName: "hls", BitRate: "999999999"}, Streams: []ffprobeStream{
		{CodecType: "video", Width: 960, Height: 540, CodecName: "h264", AvgFrameRate: "30000/1001"},
		{CodecType: "video", Width: 1920, Height: 1080, CodecName: "hevc", AvgFrameRate: "60000/1001", BitRate: "6000000"},
		{CodecType: "video", Width: 3840, Height: 2160, Disposition: map[string]int{"attached_pic": 1}},
		{CodecType: "audio", CodecName: "eac3", Channels: 6, ChannelLayout: "5.1", Tags: map[string]string{"language": "eng"}, SampleRate: "48000", BitRate: "384000"},
	}})
	if result.Height != 1080 || result.Codec != "hevc" || result.FrameRate < 59.9 || result.Bitrate != 6000000 || !result.Adaptive {
		t.Fatalf("bad video: %+v", result)
	}
	if len(result.Audio) != 1 || result.Audio[0].Channels != 6 || result.Audio[0].Language != "eng" {
		t.Fatalf("bad audio: %+v", result.Audio)
	}
}

// Real ffprobe over HTTP fixtures, exercising the same handler used by the UI.
func TestLiveQualityIPTVAddonAndAdaptiveMedia(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe unavailable")
	}
	dir := t.TempDir()
	for _, height := range []int{360, 720} {
		file := filepath.Join(dir, fmt.Sprintf("%d.ts", height))
		cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", fmt.Sprintf("color=c=blue:s=%dx%d:r=30", height*16/9, height), "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "2", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-ac", "2", "-f", "mpegts", file)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture: %v %s", err, output)
		}
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stream/sport/event.json":
			fmt.Fprintf(w, `{"streams":[{"url":%q},{"url":%q,"behaviorHints":{"proxyHeaders":{"request":{"X-Probe-Fixture":"required","User-Agent":"QualityFixtureAgent"}}}}]}`, server.URL+"/360.ts", server.URL+"/protected.ts")
		case "/fragment.ts":
			if r.Header.Get("Referer") != "https://provider.example/" {
				http.Error(w, "referer required", 403)
				return
			}
			http.ServeFile(w, r, filepath.Join(dir, "720.ts"))
		case "/protected.ts":
			if r.Header.Get("X-Probe-Fixture") != "required" || r.UserAgent() != "QualityFixtureAgent" {
				http.Error(w, "header required", 403)
				return
			}
			http.ServeFile(w, r, filepath.Join(dir, "720.ts"))
		case "/master.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=500000,RESOLUTION=640x360\n/360.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720\n/720.m3u8\n")
		case "/360.m3u8", "/720.m3u8":
			fmt.Fprintf(w, "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2,\n%s.ts\n#EXT-X-ENDLIST\n", strings.TrimSuffix(r.URL.Path, ".m3u8"))
		default:
			http.ServeFile(w, r, filepath.Join(dir, filepath.Base(r.URL.Path)))
		}
	}))
	defer server.Close()
	h := NewVideoHandlerWithProvider(false, "", ffprobe, t.TempDir(), nil)
	h.SetConfigManager(fakeLiveUsageConfigProvider{settings: config.Settings{Live: config.LiveSettings{Mode: "m3u", PlaylistURL: server.URL + "/playlist.m3u"}}})
	for _, tc := range []struct {
		name, path string
		index      *int
		height     int
		adaptive   bool
	}{
		{"iptv-transport-stream", "/720.ts", nil, 720, false},
		{"addon-selected-feed-and-headers", "/stream/sport/event.json", new(1), 720, false},
		{"embedded-playback-headers", "/fragment.ts", nil, 720, false},
		{"adaptive-hls", "/master.m3u8", nil, 720, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streamURL := server.URL + tc.path
			if tc.name == "embedded-playback-headers" {
				streamURL = streamheaders.Attach(streamURL, map[string]string{"Referer": "https://provider.example/"})
			}
			h.SetLiveChannelProvider(staticLiveChannelProvider{channels: []LiveChannel{{ID: tc.name, SourceID: "default", URL: streamURL}}})
			body, _ := json.Marshal(liveQualityRequest{URL: streamURL, SourceID: "default", ChannelID: tc.name, StreamIndex: tc.index})
			req := httptest.NewRequest(http.MethodPost, "/video/live/quality", strings.NewReader(string(body)))
			rec := httptest.NewRecorder()
			h.ProbeLiveQuality(rec, req)
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			var result liveQualityResult
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Height != tc.height || result.Adaptive != tc.adaptive || len(result.Audio) == 0 || result.Audio[0].Channels != 2 || result.Codec != "h264" {
				t.Fatalf("result: %+v", result)
			}
		})
	}
	t.Run("configured-http-proxy", func(t *testing.T) {
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Hostname() != "provider.invalid" || r.UserAgent() != liveStreamUserAgent {
				http.Error(w, "wrong proxy request", 400)
				return
			}
			http.ServeFile(w, r, filepath.Join(dir, "720.ts"))
		}))
		defer proxy.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		result, err := h.probeLiveQuality(ctx, "http://provider.invalid/live.ts", proxy.URL, nil, false)
		if err != nil || result.Height != 720 {
			t.Fatalf("proxy result: %+v %v", result, err)
		}
	})
}
func TestLiveQualityCancellationReleasesConnection(t *testing.T) {
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe unavailable")
	}
	opened := make(chan struct{})
	closed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(opened); <-r.Context().Done(); close(closed) }))
	defer server.Close()
	h := NewVideoHandlerWithProvider(false, "", ffprobe, t.TempDir(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := h.probeLiveQuality(ctx, server.URL, "", nil, false); done <- err }()
	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("probe did not open")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled probe succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ffprobe was not killed")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("provider connection not released")
	}
}
func TestLiveQualityRejectsNonHTTPAndRedactsProbeErrors(t *testing.T) {
	h := NewVideoHandlerWithProvider(false, "", stubBinary(t, "false"), t.TempDir(), nil)
	rec := httptest.NewRecorder()
	h.ProbeLiveQuality(rec, httptest.NewRequest("POST", "/video/live/quality", strings.NewReader(`{"url":"file:///etc/passwd"}`)))
	if rec.Code != 400 {
		t.Fatal(rec.Code)
	}
	// Error contents are never returned by the handler, even when stderr has credentials.
	script := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho secret-provider-password >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	h.ffprobePath = script
	h.SetConfigManager(fakeLiveUsageConfigProvider{settings: config.Settings{Live: config.LiveSettings{PlaylistURL: server.URL}}})
	h.SetLiveChannelProvider(staticLiveChannelProvider{channels: []LiveChannel{{ID: "fixture", SourceID: "default", URL: server.URL}}})
	rec = httptest.NewRecorder()
	h.ProbeLiveQuality(rec, httptest.NewRequest("POST", "/video/live/quality", strings.NewReader(fmt.Sprintf(`{"url":%q,"sourceId":"default","channelId":"fixture"}`, server.URL))))
	if rec.Code != 502 || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("unsafe response: %d %s", rec.Code, rec.Body.String())
	}
}

func TestLiveQualityRequiresBackendIssuedChannelIdentity(t *testing.T) {
	h := NewVideoHandlerWithProvider(false, "", stubBinary(t, "false"), t.TempDir(), nil)
	h.SetLiveChannelProvider(staticLiveChannelProvider{channels: []LiveChannel{{ID: "known", SourceID: "source", URL: "https://provider.example/known.ts"}}})

	for _, body := range []string{
		`{"url":"https://attacker.example/redirect.m3u8","sourceId":"source","channelId":"known"}`,
		`{"url":"https://provider.example/known.ts","sourceId":"source","channelId":"other"}`,
		`{"url":"https://provider.example/known.ts","sourceId":"other","channelId":"known"}`,
	} {
		rec := httptest.NewRecorder()
		h.ProbeLiveQuality(rec, httptest.NewRequest(http.MethodPost, "/video/live/quality", strings.NewReader(body)))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d for %s", rec.Code, http.StatusNotFound, body)
		}
	}
}
