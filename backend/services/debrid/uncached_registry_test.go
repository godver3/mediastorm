package debrid

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
)

// withFreshUncachedRegistry isolates a test from hashes remembered by others.
func withFreshUncachedRegistry(t *testing.T) *uncachedHashRegistry {
	t.Helper()
	previous := recentUncached
	fresh := newUncachedHashRegistry(uncachedHashTTL)
	recentUncached = fresh
	t.Cleanup(func() { recentUncached = previous })
	return fresh
}

func TestUncachedRegistryExpiresAfterTTL(t *testing.T) {
	registry := withFreshUncachedRegistry(t)
	now := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	registry.now = func() time.Time { return now }

	RememberUncached("AllDebrid", "", "ABCDEF")
	if !IsRecentlyUncached("alldebrid", "abcdef") {
		t.Fatal("hash should be remembered (case-insensitive) right after marking")
	}
	if IsRecentlyUncached("torbox", "abcdef") {
		t.Fatal("memory must be per provider")
	}

	now = now.Add(uncachedHashTTL - time.Minute)
	if !IsRecentlyUncached("alldebrid", "abcdef") {
		t.Fatal("hash should still be remembered just before the TTL")
	}
	now = now.Add(2 * time.Minute)
	if IsRecentlyUncached("alldebrid", "abcdef") {
		t.Fatal("hash should be forgotten after the TTL")
	}
}

func TestResolveSkipsUploadForRecentlyUncachedHash(t *testing.T) {
	withFreshUncachedRegistry(t)
	mock := &mockProvider{
		name:            "uncached_resolve_provider",
		status:          "downloading", // not cached
		torrentFilename: "Some.Movie.2026.1080p",
		files:           []File{{ID: 1, Path: "Some.Movie.2026.1080p.mkv", Bytes: 2_000_000_000, Selected: 1}},
	}
	svc := newTestPlaybackService(t, mock)
	candidate := models.NZBResult{
		Title:       "Some.Movie.2026.1080p",
		Link:        "magnet:?xt=urn:btih:1111222233334444555566667777888899990000",
		ServiceType: models.ServiceTypeDebrid,
		Attributes:  map[string]string{"provider": mock.name},
	}

	if _, err := svc.Resolve(context.Background(), candidate); err == nil {
		t.Fatal("first resolve: expected not-cached error")
	}
	if got := atomic.LoadInt64(&mock.addMagnetCalls); got != 1 {
		t.Fatalf("first resolve: add magnet calls = %d, want 1", got)
	}
	if got := atomic.LoadInt64(&mock.deleteCalls); got != 1 {
		t.Fatalf("first resolve: delete calls = %d, want 1", got)
	}

	_, err := svc.Resolve(context.Background(), candidate)
	if !errors.Is(err, ErrRecentlyUncached) {
		t.Fatalf("second resolve: error = %v, want ErrRecentlyUncached", err)
	}
	if got := atomic.LoadInt64(&mock.addMagnetCalls); got != 1 {
		t.Fatalf("second resolve must not upload again: add magnet calls = %d", got)
	}
}

func TestAddReportedUncachedIsRememberedWithoutFileRoundTrips(t *testing.T) {
	withFreshUncachedRegistry(t)
	// AllDebrid-style: the upload response itself says "not ready".
	mock := &mockProvider{
		name:             "uncached_add_flag_provider",
		cacheStatusKnown: true,
		cachedOnAdd:      false,
		status:           "downloading",
		files:            []File{{ID: 1, Path: "Show.S01E01.mkv", Bytes: 1_000_000, Selected: 1}},
	}
	svc := newTestPlaybackService(t, mock)
	candidate := models.NZBResult{
		Title:       "Show.S01E01.1080p",
		Link:        "magnet:?xt=urn:btih:aaaabbbbccccddddeeeeffff0000111122223333",
		ServiceType: models.ServiceTypeDebrid,
		Attributes:  map[string]string{"provider": mock.name},
	}

	if _, err := svc.Resolve(context.Background(), candidate); err == nil {
		t.Fatal("expected not-cached error")
	}
	if got := atomic.LoadInt64(&mock.getInfoCalls); got != 0 {
		t.Fatalf("get info calls = %d, want 0 when add already reported not cached", got)
	}
	if got := atomic.LoadInt64(&mock.deleteCalls); got != 1 {
		t.Fatalf("delete calls = %d, want 1", got)
	}
	if !IsRecentlyUncached(mock.name, "aaaabbbbccccddddeeeeffff0000111122223333") {
		t.Fatal("hash reported not cached on add should be remembered")
	}
}

func TestAddReportedCachedClearsRememberedHash(t *testing.T) {
	registry := withFreshUncachedRegistry(t)
	registry.mark("cache_flip_provider", "feedface")

	// A later authoritative "cached" answer (e.g. another path uploaded it after
	// the provider finished caching) must lift the block early.
	noteAddResult("cache_flip_provider", "FEEDFACE", &AddMagnetResult{CacheStatusKnown: true, Cached: true})
	if IsRecentlyUncached("cache_flip_provider", "feedface") {
		t.Fatal("a cached add response must clear the remembered hash")
	}

	// An add response without authoritative cache state changes nothing.
	noteAddResult("cache_flip_provider", "feedface", &AddMagnetResult{CacheStatusKnown: false})
	if IsRecentlyUncached("cache_flip_provider", "feedface") {
		t.Fatal("a non-authoritative add response must not mark the hash")
	}
}

func TestTorrentFileUploadSkippedForRecentlyUncachedHash(t *testing.T) {
	withFreshUncachedRegistry(t)
	data := []byte("d4:infod4:name4:test6:lengthi1eee")
	hash, err := torrentV1InfoHash(data)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	RememberUncached("uncached_file_provider", hash)

	mock := &mockProvider{name: "uncached_file_provider"}
	_, err = torrentSource{data: data, filename: "x.torrent"}.add(context.Background(), mock)
	if !errors.Is(err, ErrRecentlyUncached) {
		t.Fatalf("error = %v, want ErrRecentlyUncached", err)
	}
}

func TestQuickCacheReportsRememberedHashAsNotCachedForUnsupportedProvider(t *testing.T) {
	withFreshUncachedRegistry(t)
	unsupported := &unsupportedInstantProvider{mockProvider: &mockProvider{name: "uncached_quick_unsupported"}}
	RegisterProvider(unsupported.name, func(string) Provider { return unsupported })

	cfg := config.NewManager(t.TempDir() + "/settings.json")
	settings := config.DefaultSettings()
	settings.Streaming.DebridProviders = []config.DebridProviderSettings{
		{Name: "Unsupported", Provider: unsupported.name, APIKey: "key", Enabled: true},
	}
	if err := cfg.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	RememberUncached(unsupported.name, "dddd4444")

	results := []models.NZBResult{
		{Title: "Remembered", ServiceType: models.ServiceTypeDebrid, Attributes: map[string]string{"infoHash": "dddd4444"}},
		{Title: "Unknown", ServiceType: models.ServiceTypeDebrid, Attributes: map[string]string{"infoHash": "eeee5555"}},
	}
	out, err := NewHealthService(cfg).CheckQuickCacheOnlyBulk(context.Background(), results)
	if err != nil {
		t.Fatalf("CheckQuickCacheOnlyBulk: %v", err)
	}
	if out[0].Cached || out[0].Status != "not_cached" {
		t.Fatalf("remembered hash: got %#v, want not_cached", out[0])
	}
	if out[1].Status != "skipped" {
		t.Fatalf("unknown hash: got %#v, want skipped", out[1])
	}
}
