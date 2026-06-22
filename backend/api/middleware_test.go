package api

import "testing"

func TestIsStreamScopedPathAllowed(t *testing.T) {
	allowed := []string{
		"/api/video/hls/abc/stream.m3u8",
		"/api/video/metadata",
		"/api/video/stream",
		"/api/live/hls/start",
		"/api/live/recordings/1/stream",
	}
	for _, path := range allowed {
		if !isStreamScopedPathAllowed(path) {
			t.Errorf("expected %q to be allowed for stream-scoped session", path)
		}
	}

	denied := []string{
		"/api/settings",
		"/api/users/u1/history/progress",
		"/api/share/create",
		"/api/watchlist",
		"/api/discover/new",
		"/api/videos", // not a /video/ prefix
	}
	for _, path := range denied {
		if isStreamScopedPathAllowed(path) {
			t.Errorf("expected %q to be denied for stream-scoped session", path)
		}
	}
}
