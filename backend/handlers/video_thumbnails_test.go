package handlers

import (
	"strings"
	"testing"
)

func TestThumbnailTimesCapsLongVideos(t *testing.T) {
	interval, times := thumbnailTimes(3*60*60, 30)
	if len(times) > thumbnailMaxCount {
		t.Fatalf("expected capped thumbnails, got %d", len(times))
	}
	if interval < 90 {
		t.Fatalf("expected interval to increase for long video, got %d", interval)
	}
}

func TestThumbnailTimesShortVideo(t *testing.T) {
	interval, times := thumbnailTimes(90, 60)
	if interval != thumbnailDefaultIntervalSec {
		t.Fatalf("expected default interval, got %d", interval)
	}
	if len(times) == 0 {
		t.Fatal("expected at least one thumbnail for short VOD")
	}
}

func TestThumbnailGenerationOrderSpreadsInitialWork(t *testing.T) {
	order := thumbnailGenerationOrder(120)
	if len(order) != 120 {
		t.Fatalf("expected all indexes, got %d", len(order))
	}
	seen := make(map[int]bool, len(order))
	for _, idx := range order {
		if seen[idx] {
			t.Fatalf("duplicate index %d in generation order", idx)
		}
		seen[idx] = true
	}
	if order[0] != 0 {
		t.Fatalf("expected first thumbnail first, got %d", order[0])
	}
	if order[thumbnailInitialPassCount-1] != 119 {
		t.Fatalf("expected initial spread to reach end of timeline, got %d", order[thumbnailInitialPassCount-1])
	}
	if order[1] <= 1 {
		t.Fatalf("expected early work to jump across timeline, got second index %d", order[1])
	}
	if order[thumbnailInitialPassCount] <= 1 {
		t.Fatalf("expected second pass to continue sparse fill before chronological fill, got %d", order[thumbnailInitialPassCount])
	}
}

func TestThumbnailNeedsToneMap(t *testing.T) {
	if thumbnailNeedsToneMap(nil) {
		t.Fatal("nil metadata should not require tone mapping")
	}
	if thumbnailNeedsToneMap(&videoMetadataResponse{VideoStreams: []videoStreamSummary{{ColorTransfer: "bt709"}}}) {
		t.Fatal("bt709 stream should not require tone mapping")
	}
	if !thumbnailNeedsToneMap(&videoMetadataResponse{VideoStreams: []videoStreamSummary{{ColorTransfer: "smpte2084"}}}) {
		t.Fatal("PQ HDR stream should require tone mapping")
	}
	if !thumbnailNeedsToneMap(&videoMetadataResponse{VideoStreams: []videoStreamSummary{{HasDolbyVision: true}}}) {
		t.Fatal("Dolby Vision stream should require tone mapping")
	}
	if !thumbnailNeedsToneMap(&videoMetadataResponse{VideoStreams: []videoStreamSummary{{HdrFormat: "HDR10"}}}) {
		t.Fatal("HDR format should require tone mapping")
	}
}

func TestDolbyVisionProfile5Detection(t *testing.T) {
	cases := []string{"dvhe.05.06", "dvh1.05.09", "profile 5", "5"}
	for _, tc := range cases {
		if !isDolbyVisionProfile5(tc) {
			t.Fatalf("expected %q to be detected as DV profile 5", tc)
		}
	}
	if isDolbyVisionProfile5("dvhe.08.06") {
		t.Fatal("DV profile 8 should not be detected as profile 5")
	}
}

func TestCleanVideoPathParam(t *testing.T) {
	if got := cleanVideoPathParam("/webdav/media/movie.mkv"); got != "/media/movie.mkv" {
		t.Fatalf("unexpected cleaned path: %q", got)
	}
	if got := cleanVideoPathParam("webdav/media/movie.mkv"); got != "/media/movie.mkv" {
		t.Fatalf("unexpected cleaned relative path: %q", got)
	}
}

func TestBuildLocalVideoStreamURL(t *testing.T) {
	h := &VideoHandler{}
	h.SetLocalBaseURL("http://127.0.0.1:7777")

	got := h.buildLocalVideoStreamURL("/debrid/torbox/1/file/2/Movie Name.mkv")
	if !strings.HasPrefix(got, "http://127.0.0.1:7777/api/video/internal-stream?") {
		t.Fatalf("unexpected local stream URL: %q", got)
	}
	if !strings.Contains(got, "path=%2Fdebrid%2Ftorbox%2F1%2Ffile%2F2%2FMovie+Name.mkv") {
		t.Fatalf("expected encoded path in local stream URL, got %q", got)
	}
	if !strings.Contains(got, "transmux=0") {
		t.Fatalf("expected transmux=0 in local stream URL, got %q", got)
	}
}
