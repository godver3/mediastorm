package handlers

import (
	"net/http/httptest"
	"slices"
	"testing"
)

func TestLiveTransmuxOutputDefaultsToFragmentedMP4(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/live/stream?target=native", nil)
	args, contentType := liveTransmuxOutput(r)
	if contentType != "video/mp4" {
		t.Fatalf("content type = %q, want video/mp4", contentType)
	}
	if !slices.Contains(args, "mp4") || !slices.Contains(args, "-movflags") {
		t.Fatalf("default output is not fragmented mp4: %v", args)
	}
	if args[len(args)-1] != "pipe:1" {
		t.Fatalf("output must end with pipe:1: %v", args)
	}
}

func TestLiveTransmuxOutputContainerTSIsMPEGTS(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/live/stream?target=native&container=TS", nil)
	args, contentType := liveTransmuxOutput(r)
	if contentType != "video/mp2t" {
		t.Fatalf("content type = %q, want video/mp2t", contentType)
	}
	if !slices.Contains(args, "mpegts") {
		t.Fatalf("container=ts output is not mpegts: %v", args)
	}
	for _, mp4Only := range []string{"-movflags", "mp4", "-reset_timestamps"} {
		if slices.Contains(args, mp4Only) {
			t.Fatalf("container=ts output carries mp4 option %q: %v", mp4Only, args)
		}
	}
	if !slices.Contains(args, "copy") || args[len(args)-1] != "pipe:1" {
		t.Fatalf("container=ts must keep video copy and pipe:1: %v", args)
	}
}
