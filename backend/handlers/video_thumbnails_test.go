package handlers

import (
	"bytes"
	"os"
	"path/filepath"
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

func TestThumbnailGenerationOrderUsesPreviewPyramid(t *testing.T) {
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
	wantPrefix := []int{60, 30, 89, 15, 45, 74, 104}
	for i, want := range wantPrefix {
		if order[i] != want {
			t.Fatalf("expected preview pyramid index %d to be %d, got %d", i, want, order[i])
		}
	}
}

func TestThumbnailGenerationPassesProgressivelyRefineTimeline(t *testing.T) {
	passes := thumbnailGenerationPasses(48)
	if len(passes) != thumbnailPreviewLODPasses+1 {
		t.Fatalf("expected LOD passes plus fill pass, got %d", len(passes))
	}
	expectedPasses := [][]int{
		{24},
		{12, 35},
		{6, 18, 29, 41},
	}
	for i, want := range expectedPasses {
		if len(passes[i]) != len(want) {
			t.Fatalf("expected pass %d length %d, got %d (%v)", i, len(want), len(passes[i]), passes[i])
		}
		for j, wantIdx := range want {
			if passes[i][j] != wantIdx {
				t.Fatalf("expected pass %d index %d to be %d, got %d", i, j, wantIdx, passes[i][j])
			}
		}
	}

	total := 0
	seen := make(map[int]bool)
	for _, pass := range passes {
		total += len(pass)
		for _, idx := range pass {
			if seen[idx] {
				t.Fatalf("duplicate index %d in generation passes", idx)
			}
			seen[idx] = true
		}
	}
	if total != 48 {
		t.Fatalf("expected all indexes across passes, got %d", total)
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

func TestThumbnailToneMapModeSelection(t *testing.T) {
	tests := []struct {
		name      string
		caps      map[string]bool
		toneMap   bool
		dvProfile string
		want      thumbnailToneMapMode
	}{
		{
			name:    "sdr disables tone map",
			caps:    map[string]bool{"libplacebo": true, thumbnailLibplaceboRuntime: true},
			toneMap: false,
			want:    thumbnailToneMapNone,
		},
		{
			name:      "dv5 prefers libplacebo",
			caps:      map[string]bool{"libplacebo": true, thumbnailLibplaceboRuntime: true, "zscale": true, "tonemap": true},
			toneMap:   true,
			dvProfile: "dvhe.05.06",
			want:      thumbnailToneMapLibplacebo,
		},
		{
			name:      "dv5 without usable libplacebo is unsupported",
			caps:      map[string]bool{"zscale": true, "tonemap": true},
			toneMap:   true,
			dvProfile: "dvhe.05.06",
			want:      thumbnailToneMapUnsupported,
		},
		{
			name:      "dv5 with compiled libplacebo but failed runtime is unsupported",
			caps:      map[string]bool{"libplacebo": true, "zscale": true, "tonemap": true},
			toneMap:   true,
			dvProfile: "dvhe.05.06",
			want:      thumbnailToneMapUnsupported,
		},
		{
			name:    "hdr prefers zscale over libplacebo",
			caps:    map[string]bool{"libplacebo": true, thumbnailLibplaceboRuntime: true, "zscale": true, "tonemap": true},
			toneMap: true,
			want:    thumbnailToneMapZscale,
		},
		{
			name:    "hdr uses libplacebo when zscale unavailable",
			caps:    map[string]bool{"libplacebo": true, thumbnailLibplaceboRuntime: true, "tonemap": true},
			toneMap: true,
			want:    thumbnailToneMapLibplacebo,
		},
		{
			name:    "hdr skips unusable libplacebo",
			caps:    map[string]bool{"libplacebo": true, "tonemap": true},
			toneMap: true,
			want:    thumbnailToneMapFFmpeg,
		},
		{
			name:    "hdr falls back to zscale",
			caps:    map[string]bool{"zscale": true, "tonemap": true},
			toneMap: true,
			want:    thumbnailToneMapZscale,
		},
		{
			name:    "hdr falls back to native tonemap",
			caps:    map[string]bool{"tonemap": true},
			toneMap: true,
			want:    thumbnailToneMapFFmpeg,
		},
		{
			name:    "hdr without filters unsupported",
			caps:    map[string]bool{},
			toneMap: true,
			want:    thumbnailToneMapUnsupported,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := NewThumbnailManager(t.TempDir(), "ffmpeg")
			manager.filterCaps = tt.caps
			manager.filterOnce.Do(func() {})
			if got := manager.thumbnailToneMapMode(tt.toneMap, tt.dvProfile); got != tt.want {
				t.Fatalf("expected %s, got %s", tt.want, got)
			}
		})
	}
}

func TestThumbnailFiltersUseExpectedPipelines(t *testing.T) {
	if got := thumbnailFilter(thumbnailToneMapLibplacebo); !strings.Contains(got, "libplacebo=") || !strings.Contains(got, "tonemapping=bt.2390") {
		t.Fatalf("expected libplacebo tone map filter, got %q", got)
	}
	if got := thumbnailFilter(thumbnailToneMapZscale); !strings.Contains(got, "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc,zscale=t=linear") || !strings.Contains(got, "tonemap=tonemap=mobius") {
		t.Fatalf("expected zscale tone map filter, got %q", got)
	}
	if got := thumbnailFilter(thumbnailToneMapNone); strings.Contains(got, "tonemap") {
		t.Fatalf("SDR filter should not tone map, got %q", got)
	}
}

func TestManifestFilesCompleteRequiresUsableFiles(t *testing.T) {
	manager := NewThumbnailManager(t.TempDir(), "ffmpeg")
	manifest := &thumbnailManifest{
		Key:       "0123456789abcdef01234567",
		Generated: 2,
		Thumbnails: []thumbnailDetails{
			{TimeSec: 30, File: "thumb-0001.jpg"},
			{TimeSec: 90, File: "thumb-0002.jpg"},
		},
	}
	dir := filepath.Join(manager.baseDir, manifest.Key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir thumbnails: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "thumb-0001.jpg"), bytes.Repeat([]byte{1}, thumbnailMinJPEGBytes), 0o644); err != nil {
		t.Fatalf("write usable thumb: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "thumb-0002.jpg"), []byte{1, 2, 3}, 0o644); err != nil {
		t.Fatalf("write tiny thumb: %v", err)
	}
	if manager.manifestFilesComplete(manifest) {
		t.Fatal("manifest with tiny thumbnail should be incomplete")
	}
	if err := os.WriteFile(filepath.Join(dir, "thumb-0002.jpg"), bytes.Repeat([]byte{2}, thumbnailMinJPEGBytes), 0o644); err != nil {
		t.Fatalf("write second usable thumb: %v", err)
	}
	if !manager.manifestFilesComplete(manifest) {
		t.Fatal("manifest with all usable thumbnails should be complete")
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
