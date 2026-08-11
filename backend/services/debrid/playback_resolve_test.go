package debrid

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"novastream/models"
)

func TestResolveSkipsProviderBlockedSelectedFile(t *testing.T) {
	mock := &mockProvider{
		name:            "testprovider_blocked_selected",
		status:          "downloaded",
		torrentFilename: "The.Simpsons.S01.1080p.WEB.H264-BATV",
		files: []File{
			{ID: 1, Path: "The.Simpsons.S01E01.1080p.WEB.H264-BATV.mkv", Bytes: 1_000_000, Selected: 1},
			{ID: 2, Path: "The.Simpsons.S01E02.1080p.WEB.H264-BATV.mkv", Bytes: 1_000_000, Selected: 1},
		},
		links: []string{
			"https://real-debrid.com/d/BLOCKED",
			"https://real-debrid.com/d/OTHER",
		},
		unrestrictErr: &ProviderError{
			Provider:   "realdebrid",
			Operation:  "unrestrict",
			StatusCode: 451,
			Code:       35,
			Message:    "infringing_file",
		},
	}
	svc := newTestPlaybackService(t, mock)

	_, err := svc.Resolve(context.Background(), models.NZBResult{
		Title:       "The.Simpsons.S01.1080p.WEB.H264-BATV",
		Link:        "magnet:?xt=urn:btih:ABCDEF123456",
		ServiceType: models.ServiceTypeDebrid,
		Attributes: map[string]string{
			"provider":      mock.name,
			"targetSeason":  "1",
			"targetEpisode": "1",
		},
	})
	if err == nil {
		t.Fatal("expected resolve error for provider-blocked selected file")
	}
	if !IsBlockedContentError(err) {
		t.Fatalf("expected blocked-content error, got %v", err)
	}
	if !strings.Contains(err.Error(), "selected file blocked by provider") {
		t.Fatalf("expected selected-file context, got %v", err)
	}
	if got := atomic.LoadInt64(&mock.deleteCalls); got == 0 {
		t.Fatal("expected blocked torrent to be deleted")
	}
}

func TestFilterCachedResultsDoesNotInterpretPearTubeAsDebrid(t *testing.T) {
	mock := &mockProvider{name: "testprovider_peartube_guard"}
	svc := newTestPlaybackService(t, mock)
	candidate := models.NZBResult{
		Title:       "PearTube candidate with poisoned debrid fields",
		Link:        "magnet:?xt=urn:btih:THIS-MUST-NOT-BE-PARSED",
		ServiceType: models.ServiceTypePearTube,
		Attributes: map[string]string{
			"provider":               mock.name,
			"infoHash":               "THIS-MUST-NOT-REACH-CACHE-HEALTH",
			"peartube_candidate_ref": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		},
	}

	if cached := svc.FilterCachedResults(context.Background(), []models.NZBResult{candidate}); len(cached) != 0 {
		t.Fatalf("FilterCachedResults returned %d PearTube candidates, want 0", len(cached))
	}
	if calls := atomic.LoadInt64(&mock.instantCalls); calls != 0 {
		t.Fatalf("instant cache calls = %d, want 0", calls)
	}
	if calls := atomic.LoadInt64(&mock.addMagnetCalls); calls != 0 {
		t.Fatalf("magnet add calls = %d, want 0", calls)
	}
	if calls := atomic.LoadInt64(&mock.getInfoCalls); calls != 0 {
		t.Fatalf("torrent info calls = %d, want 0", calls)
	}
}
