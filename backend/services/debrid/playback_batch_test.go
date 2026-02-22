package debrid

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"novastream/config"
	"novastream/models"
)

// mockProvider is a minimal Provider implementation that counts API calls.
type mockProvider struct {
	name            string
	addMagnetCalls  int64
	getInfoCalls    int64
	selectCalls     int64
	deleteCalls     int64
	files           []File   // files returned by GetTorrentInfo
	links           []string // links returned after selection
	status          string   // torrent status (e.g. "downloaded")
	torrentFilename string
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) AddMagnet(_ context.Context, _ string) (*AddMagnetResult, error) {
	atomic.AddInt64(&m.addMagnetCalls, 1)
	return &AddMagnetResult{ID: "test-torrent-id"}, nil
}
func (m *mockProvider) AddTorrentFile(_ context.Context, _ []byte, _ string) (*AddMagnetResult, error) {
	return &AddMagnetResult{ID: "test-torrent-id"}, nil
}
func (m *mockProvider) GetTorrentInfo(_ context.Context, _ string) (*TorrentInfo, error) {
	atomic.AddInt64(&m.getInfoCalls, 1)
	return &TorrentInfo{
		ID:       "test-torrent-id",
		Filename: m.torrentFilename,
		Status:   m.status,
		Files:    m.files,
		Links:    m.links,
	}, nil
}
func (m *mockProvider) SelectFiles(_ context.Context, _ string, _ string) error {
	atomic.AddInt64(&m.selectCalls, 1)
	return nil
}
func (m *mockProvider) DeleteTorrent(_ context.Context, _ string) error {
	atomic.AddInt64(&m.deleteCalls, 1)
	return nil
}
func (m *mockProvider) UnrestrictLink(_ context.Context, link string) (*UnrestrictResult, error) {
	return &UnrestrictResult{DownloadURL: link}, nil
}
func (m *mockProvider) CheckInstantAvailability(_ context.Context, _ string) (bool, error) {
	return true, nil
}

func newTestPlaybackService(t *testing.T, mock *mockProvider) *PlaybackService {
	t.Helper()
	tmpDir := t.TempDir()
	mgr := config.NewManager(tmpDir + "/settings.json")
	err := mgr.Save(config.Settings{
		Streaming: config.StreamingSettings{
			DebridProviders: []config.DebridProviderSettings{
				{Provider: mock.name, APIKey: "test-key", Enabled: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("save config: %v", err)
	}
	// Register the mock provider so GetProvider finds it
	RegisterProvider(mock.name, func(apiKey string) Provider { return mock })
	return NewPlaybackService(mgr, nil)
}

func TestResolveBatch_AddMagnetCalledOnce(t *testing.T) {
	mock := &mockProvider{
		name:            "testprovider_batch1",
		status:          "downloaded",
		torrentFilename: "Test.Show.S01.1080p",
		files: []File{
			{ID: 1, Path: "Test.Show.S01E01.mkv", Bytes: 1_000_000, Selected: 1},
			{ID: 2, Path: "Test.Show.S01E02.mkv", Bytes: 1_000_000, Selected: 1},
			{ID: 3, Path: "Test.Show.S01E03.mkv", Bytes: 1_000_000, Selected: 1},
		},
		links: []string{
			"https://download.example.com/file1.mkv",
			"https://download.example.com/file2.mkv",
			"https://download.example.com/file3.mkv",
		},
	}
	svc := newTestPlaybackService(t, mock)

	candidate := models.NZBResult{
		Title:       "Test.Show.S01.1080p",
		Link:        "magnet:?xt=urn:btih:ABCDEF123456",
		ServiceType: models.ServiceTypeDebrid,
		Attributes: map[string]string{
			"provider": mock.name,
		},
	}

	episodes := []models.BatchEpisodeTarget{
		{SeasonNumber: 1, EpisodeNumber: 1, EpisodeCode: "S01E01"},
		{SeasonNumber: 1, EpisodeNumber: 2, EpisodeCode: "S01E02"},
		{SeasonNumber: 1, EpisodeNumber: 3, EpisodeCode: "S01E03"},
	}

	resp, err := svc.ResolveBatch(context.Background(), candidate, episodes)
	if err != nil {
		t.Fatalf("ResolveBatch error: %v", err)
	}

	// AddMagnet must be called exactly once
	if got := atomic.LoadInt64(&mock.addMagnetCalls); got != 1 {
		t.Errorf("AddMagnet called %d times, want 1", got)
	}

	// SelectFiles must be called exactly once
	if got := atomic.LoadInt64(&mock.selectCalls); got != 1 {
		t.Errorf("SelectFiles called %d times, want 1", got)
	}

	// GetTorrentInfo called exactly twice (pre-select + post-select)
	if got := atomic.LoadInt64(&mock.getInfoCalls); got != 2 {
		t.Errorf("GetTorrentInfo called %d times, want 2", got)
	}

	if len(resp.Results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(resp.Results))
	}

	for i, r := range resp.Results {
		if r.Resolution == nil {
			t.Errorf("episode %d: resolution is nil, error=%q", i+1, r.Error)
		} else if r.Resolution.WebDAVPath == "" {
			t.Errorf("episode %d: empty webdav path", i+1)
		}
	}
}

func TestResolveBatch_PartialFailure(t *testing.T) {
	// Only 2 files in the torrent for 3 episodes — episode 3 should fail
	mock := &mockProvider{
		name:            "testprovider_batch2",
		status:          "downloaded",
		torrentFilename: "Test.Show.S01.1080p",
		files: []File{
			{ID: 1, Path: "Test.Show.S01E01.mkv", Bytes: 1_000_000, Selected: 1},
			{ID: 2, Path: "Test.Show.S01E02.mkv", Bytes: 1_000_000, Selected: 1},
		},
		links: []string{
			"https://download.example.com/file1.mkv",
			"https://download.example.com/file2.mkv",
		},
	}
	svc := newTestPlaybackService(t, mock)

	candidate := models.NZBResult{
		Title:       "Test.Show.S01.1080p",
		Link:        "magnet:?xt=urn:btih:ABCDEF123456",
		ServiceType: models.ServiceTypeDebrid,
		Attributes: map[string]string{
			"provider": mock.name,
		},
	}

	episodes := []models.BatchEpisodeTarget{
		{SeasonNumber: 1, EpisodeNumber: 1, EpisodeCode: "S01E01"},
		{SeasonNumber: 1, EpisodeNumber: 2, EpisodeCode: "S01E02"},
		{SeasonNumber: 1, EpisodeNumber: 3, EpisodeCode: "S01E03"},
	}

	resp, err := svc.ResolveBatch(context.Background(), candidate, episodes)
	if err != nil {
		t.Fatalf("ResolveBatch error: %v", err)
	}

	if len(resp.Results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(resp.Results))
	}

	// Episodes 1 and 2 should succeed
	for _, i := range []int{0, 1} {
		r := resp.Results[i]
		if r.Resolution == nil {
			t.Errorf("episode %d: expected resolution, got error=%q", i+1, r.Error)
		}
	}

	// Episode 3 should fail (file not found)
	r := resp.Results[2]
	if r.Resolution != nil {
		t.Errorf("episode 3: expected failure, got resolution with path=%q", r.Resolution.WebDAVPath)
	}
	if r.Error == "" {
		t.Errorf("episode 3: expected error message, got empty")
	}
}

func TestResolveBatch_NotCachedError(t *testing.T) {
	mock := &mockProvider{
		name:            "testprovider_batch3",
		status:          "downloading", // NOT cached
		torrentFilename: "Test.Show.S01.1080p",
		files: []File{
			{ID: 1, Path: "Test.Show.S01E01.mkv", Bytes: 1_000_000, Selected: 1},
		},
		links: []string{"https://download.example.com/file1.mkv"},
	}
	svc := newTestPlaybackService(t, mock)

	candidate := models.NZBResult{
		Title:       "Test.Show.S01.1080p",
		Link:        "magnet:?xt=urn:btih:ABCDEF123456",
		ServiceType: models.ServiceTypeDebrid,
		Attributes: map[string]string{
			"provider": mock.name,
		},
	}

	episodes := []models.BatchEpisodeTarget{
		{SeasonNumber: 1, EpisodeNumber: 1, EpisodeCode: "S01E01"},
	}

	_, err := svc.ResolveBatch(context.Background(), candidate, episodes)
	if err == nil {
		t.Fatal("expected error for non-cached torrent, got nil")
	}
	if got := fmt.Sprintf("%v", err); got == "" {
		t.Error("expected non-empty error message")
	}
}

func TestResolveBatch_EmptyEpisodes(t *testing.T) {
	mock := &mockProvider{
		name:   "testprovider_batch4",
		status: "downloaded",
	}
	svc := newTestPlaybackService(t, mock)

	candidate := models.NZBResult{
		Title:       "Test.Show.S01.1080p",
		Link:        "magnet:?xt=urn:btih:ABCDEF123456",
		ServiceType: models.ServiceTypeDebrid,
		Attributes: map[string]string{
			"provider": mock.name,
		},
	}

	_, err := svc.ResolveBatch(context.Background(), candidate, nil)
	if err == nil {
		t.Fatal("expected error for empty episodes, got nil")
	}
}
