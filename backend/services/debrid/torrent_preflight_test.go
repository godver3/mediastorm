package debrid

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"novastream/config"
	"novastream/models"
)

func TestTorrentV1InfoHashUsesRawInfoDictionary(t *testing.T) {
	info := []byte("d6:lengthi123e4:name9:movie.mkve")
	metainfo := append([]byte("d8:announce13:https://test/4:info"), info...)
	metainfo = append(metainfo, 'e')

	sum := sha1.Sum(info)
	want := hex.EncodeToString(sum[:])
	got, err := torrentV1InfoHash(metainfo)
	if err != nil {
		t.Fatalf("torrentV1InfoHash returned error: %v", err)
	}
	if got != want {
		t.Fatalf("torrentV1InfoHash = %q, want %q", got, want)
	}
}

func TestTorrentV1InfoHashRejectsMissingInfo(t *testing.T) {
	if _, err := torrentV1InfoHash([]byte("d4:name4:teste")); err == nil {
		t.Fatal("expected missing info dictionary error")
	}
}

func TestNormalizedReleaseGroupKeyMatchesIndexerVariants(t *testing.T) {
	left := normalizedReleaseGroupKey("Project.Hail.Mary.2026.IMAX.Hybrid.2160p-WEB-DL")
	right := normalizedReleaseGroupKey("Project Hail Mary 2026 IMAX Hybrid 2160p WEB DL")
	if left != right {
		t.Fatalf("group keys differ: %q != %q", left, right)
	}
}

type torrentPreflightProvider struct {
	*mockProvider
	cached    map[string]bool
	bulkCalls atomic.Int64
}

func (p *torrentPreflightProvider) CheckInstantAvailabilityBulk(_ context.Context, hashes []string) (map[string]bool, error) {
	p.bulkCalls.Add(1)
	out := make(map[string]bool, len(hashes))
	for _, hash := range hashes {
		out[hash] = p.cached[hash]
	}
	return out, nil
}

func TestPrepareTorrentCandidatesEnrichesAndGroupsWithoutReordering(t *testing.T) {
	info := []byte("d6:lengthi123e4:name9:movie.mkve")
	metainfo := append([]byte("d8:announce13:https://test/4:info"), info...)
	metainfo = append(metainfo, 'e')
	sum := sha1.Sum(info)
	torrentHash := hex.EncodeToString(sum[:])

	var downloads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downloads.Add(1)
		w.Header().Set("Content-Disposition", `attachment; filename="movie.torrent"`)
		_, _ = w.Write(metainfo)
	}))
	defer server.Close()

	const cachedHash = "9c10f6f1825d348ad8746e016e42dadc4a668feb"
	provider := &torrentPreflightProvider{
		mockProvider: &mockProvider{name: "torbox"},
		cached: map[string]bool{
			cachedHash:  true,
			torrentHash: false,
		},
	}
	RegisterProvider("torbox", func(string) Provider { return provider })

	manager := config.NewManager(t.TempDir() + "/settings.json")
	if err := manager.Save(config.Settings{
		Streaming: config.StreamingSettings{
			DebridProviders: []config.DebridProviderSettings{
				{Provider: "torbox", APIKey: "test-key", Enabled: true},
			},
		},
	}); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	service := NewPlaybackService(manager, nil)

	torrentCandidate := func(indexer string) models.NZBResult {
		return models.NZBResult{
			Title:       "Project.Hail.Mary.2026.2160p-TMT",
			Indexer:     indexer,
			Link:        server.URL + "/" + indexer,
			DownloadURL: server.URL + "/" + indexer,
			ServiceType: models.ServiceTypeDebrid,
			Attributes: map[string]string{
				"torrentURL": server.URL + "/" + indexer,
			},
		}
	}
	candidates := []models.NZBResult{
		torrentCandidate("Jackett"),
		torrentCandidate("Prowlarr"),
		{Title: "usenet", ServiceType: models.ServiceTypeUsenet},
		{
			Title:       "cached Zilean",
			ServiceType: models.ServiceTypeDebrid,
			Link:        "magnet:?xt=urn:btih:" + cachedHash,
			Attributes:  map[string]string{"infoHash": cachedHash},
		},
	}

	got := service.PrepareTorrentCandidates(context.Background(), candidates)
	for i := range candidates {
		if got[i].Title != candidates[i].Title {
			t.Fatalf("result[%d] = %q, want original order %q", i, got[i].Title, candidates[i].Title)
		}
	}
	if got[2].Title != "usenet" {
		t.Fatalf("non-debrid slot moved: result[2] = %q", got[2].Title)
	}
	if got[0].Attributes["infoHash"] != torrentHash || got[1].Attributes["infoHash"] != torrentHash {
		t.Fatalf("grouped torrent candidates were not enriched with hash %q", torrentHash)
	}
	if calls := provider.bulkCalls.Load(); calls != 2 {
		t.Fatalf("bulk cache calls = %d, want 2", calls)
	}
	// The winner may cancel an alternate before that request reaches the
	// server. Both candidates must still share its enriched metainfo.
	preflightDownloads := downloads.Load()
	if preflightDownloads < 1 || preflightDownloads > 2 {
		t.Fatalf("torrent downloads = %d, want one winner and at most one alternate", preflightDownloads)
	}

	source, reused, err := service.torrentSourceForResolution(
		context.Background(), torrentHash, got[1].Attributes["torrentURL"],
	)
	if err != nil {
		t.Fatalf("torrentSourceForResolution returned error: %v", err)
	}
	if !reused {
		t.Fatal("torrentSourceForResolution did not reuse preflight metainfo")
	}
	if string(source.data) != string(metainfo) {
		t.Fatal("reused torrent metainfo differs from downloaded data")
	}
	if source.filename != "movie.torrent" {
		t.Fatalf("reused filename = %q, want movie.torrent", source.filename)
	}
	if count := downloads.Load(); count != preflightDownloads {
		t.Fatalf("torrent downloads after resolution lookup = %d, want cached preflight count %d", count, preflightDownloads)
	}
}
