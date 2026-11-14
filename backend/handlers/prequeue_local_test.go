package handlers

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/debrid"
	"novastream/services/indexer"
	"novastream/services/playback"
	"novastream/services/user_settings"
)

type fallbackProfiles struct{ profile models.User }

func (f fallbackProfiles) BelongsToAccount(profileID, accountID string) bool {
	return f.profile.ID == profileID && f.profile.AccountID == accountID
}
func (f fallbackProfiles) Get(id string) (models.User, bool) { return f.profile, id == f.profile.ID }

type fallbackAccess struct{ deny atomic.Bool }

func (a *fallbackAccess) CanAccess(context.Context, string, string, string, bool) (bool, error) {
	return !a.deny.Load(), nil
}

type fallbackCatalog struct {
	item        models.LocalMediaItem
	unavailable bool
	empty       bool
}

func (c *fallbackCatalog) FindPlaybackCandidates(_ context.Context, _ models.LocalMediaMatchQuery, _ *models.EpisodeReference, allow func(string) bool) ([]models.LocalMediaItem, error) {
	if c.empty || !allow(c.item.LibraryID) {
		return nil, nil
	}
	return []models.LocalMediaItem{c.item}, nil
}

func TestLocalPreferenceSelectsPlayableLocalBeforeSearch(t *testing.T) {
	for _, mode := range []string{"local available", "local missing", "no local match", "library denied", "local unplayable", "local exceeds quality", "local incompatible HDR", "search first"} {
		t.Run(mode, func(t *testing.T) {
			search := &fallbackSearch{results: []models.NZBResult{{Title: "Synthetic.Movie.1080p.WEB-DL", Link: "https://example.invalid/synthetic", ServiceType: models.ServiceTypeDebrid, Indexer: "AIOStreams", Attributes: map[string]string{"resolution": "1080p"}}}}
			h, catalog, access := newFallbackWorker(t, search)
			settings, err := user_settings.NewService(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := settings.Update("profile", models.UserSettings{Playback: models.PlaybackSettings{PreferLocalFiles: models.BoolPtr(mode != "search first")}}); err != nil {
				t.Fatal(err)
			}
			h.userSettingsSvc = settings
			if mode == "local exceeds quality" || mode == "local incompatible HDR" {
				cfg, err := h.configManager.Load()
				if err != nil {
					t.Fatal(err)
				}
				cfg.Filtering.MaxResolution = "1080p"
				cfg.Filtering.HDRDVPolicy = "none"
				if err := h.configManager.Save(cfg); err != nil {
					t.Fatal(err)
				}
			}
			catalog.unavailable = mode == "local missing"
			catalog.empty = mode == "no local match"
			access.deny.Store(mode == "library denied")
			h.fullProber = fallbackFullProber{run: func(_ context.Context, path string) (*VideoFullResult, error) {
				if mode == "local unplayable" && strings.HasPrefix(path, "localmedia:") {
					return nil, errors.New("synthetic invalid video")
				}
				probe := &VideoFullResult{VideoCodec: "h264", VideoWidth: 1920, VideoHeight: 1080, Duration: 100}
				if strings.HasPrefix(path, "localmedia:") && mode == "local exceeds quality" {
					probe.VideoWidth, probe.VideoHeight = 3840, 2160
				}
				if strings.HasPrefix(path, "localmedia:") && mode == "local incompatible HDR" {
					probe.HasHDR10 = true
				}
				return probe, nil
			}}
			got := runFallbackWorker(t, h)
			wantPath, wantSearches := "/webdav/title.mkv", int32(1)
			if mode == "local available" {
				wantPath, wantSearches = "localmedia:local-item/Synthetic.Movie.mkv", 0
			}
			if got.Status != playback.PrequeueStatusReady || got.StreamPath != wantPath || search.calls.Load() != wantSearches {
				t.Fatalf("status=%s path=%s searches=%d error=%s", got.Status, got.StreamPath, search.calls.Load(), got.Error)
			}
		})
	}
}

func TestLocalPreferenceInheritanceAndPreparedSourceReuse(t *testing.T) {
	h, _, _ := newFallbackWorker(t, &fallbackSearch{})
	settings, err := user_settings.NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h.userSettingsSvc = settings
	baseline := h.prequeueSettingsScopeKey("profile", "", "tmdb:movie:123")
	global, err := h.configManager.Load()
	if err != nil {
		t.Fatal(err)
	}
	global.Playback.PreferLocalFiles = true
	if err := h.configManager.Save(global); err != nil {
		t.Fatal(err)
	}
	if h.prequeueSettingsScopeKey("profile", "", "tmdb:movie:123") == baseline {
		t.Fatal("global preference change reused the old prepared source scope")
	}
	if !h.localPlaybackPreferred("profile") || !h.localPlaybackPreferred("other-profile") {
		t.Fatal("server preference was not inherited")
	}
	if err := settings.Update("profile", models.UserSettings{Playback: models.PlaybackSettings{PreferLocalFiles: models.BoolPtr(false)}}); err != nil {
		t.Fatal(err)
	}
	if h.localPlaybackPreferred("profile") || !h.localPlaybackPreferred("other-profile") {
		t.Fatal("profile choice leaked into another profile or ignored explicit false")
	}
	if err := settings.Update("profile", models.UserSettings{Playback: models.PlaybackSettings{PreferLocalFiles: models.BoolPtr(true)}}); err != nil {
		t.Fatal(err)
	}
	if h.prequeueSettingsScopeKey("profile", "", "tmdb:movie:123") == baseline {
		t.Fatal("preference change reused the old prepared source scope")
	}
	entry := &playback.PrequeueEntry{TitleID: "tmdb:movie:123", MediaType: "movie", UserID: "profile", StreamPath: "/webdav/synthetic.mkv"}
	r := httptest.NewRequest("GET", "/", nil)
	r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, "account"))
	if err := h.validateReadyEntryForRequest(r, entry); err == nil {
		t.Fatal("a warmed remote stream bypassed local-first selection")
	}
	entry.StreamPath = "localmedia:local-item/Synthetic.Movie.mkv"
	if err := h.validateReadyEntryForRequest(r, entry); err != nil {
		t.Fatalf("valid local stream could not be reused: %v", err)
	}
}
func (c *fallbackCatalog) GetFallbackItem(context.Context, string) (*models.LocalMediaItem, error) {
	if c.unavailable {
		return nil, errors.New("synthetic file unavailable")
	}
	item := c.item
	return &item, nil
}

type fallbackSearch struct {
	wait    bool
	results []models.NZBResult
	calls   atomic.Int32
}

type fallbackFullProber struct {
	run func(context.Context, string) (*VideoFullResult, error)
}

func (p fallbackFullProber) ProbeVideoFull(ctx context.Context, path string) (*VideoFullResult, error) {
	if p.run != nil {
		return p.run(ctx, path)
	}
	return &VideoFullResult{VideoCodec: "h264", VideoWidth: 1920, VideoHeight: 1080, Duration: 100}, nil
}

func (s *fallbackSearch) Search(ctx context.Context, _ debrid.SearchOptions) ([]models.NZBResult, error) {
	s.calls.Add(1)
	if s.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.results, nil
}

func newFallbackWorker(t *testing.T, search *fallbackSearch) (*PrequeueHandler, *fallbackCatalog, *fallbackAccess) {
	t.Helper()
	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	settings := config.DefaultSettings()
	settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
	settings.Display.BypassFilteringForAIOStreamsOnly = true
	settings.TorrentScrapers = []config.TorrentScraperConfig{{Name: "AIOStreams", Type: "aiostreams", URL: "https://example.invalid/manifest.json", Enabled: true}}
	if err := manager.Save(settings); err != nil {
		t.Fatal(err)
	}
	h := NewPrequeueHandler(indexer.NewService(manager, nil, search), nil, nil, nil, nil, false)
	h.configManager = manager
	h.playbackSvc = &stubPlaybackService{}
	h.fullProber = fallbackFullProber{}
	h.localFallbackWait = 80 * time.Millisecond
	h.users = fallbackProfiles{models.User{ID: "profile", AccountID: "account"}}
	catalog := &fallbackCatalog{item: models.LocalMediaItem{ID: "local-item", LibraryID: "library", LibraryType: models.LocalMediaLibraryTypeMovie, FileName: "Synthetic.Movie.mkv", MatchedTitleID: "tmdb:movie:123", MatchStatus: models.LocalMediaMatchStatusMatched, SizeBytes: 100}}
	access := &fallbackAccess{}
	h.SetLocalFallback(catalog, access)
	return h, catalog, access
}

func TestLocalFallbackProbeWaitIsBounded(t *testing.T) {
	h, catalog, _ := newFallbackWorker(t, &fallbackSearch{})
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h.fullProber = fallbackFullProber{run: func(context.Context, string) (*VideoFullResult, error) {
		close(entered)
		<-release
		close(finished)
		return nil, nil
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := h.resolveLocalItem(ctx, catalog.item, prequeueResolutionOptions{mediaType: "movie", userID: "profile", localScope: &prequeueLocalScope{accountID: "account"}})
	close(release)
	<-entered
	<-finished
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("unbounded local probe: %v", err)
	}
}

func TestLocalFallbackQualityLimits(t *testing.T) {
	resolution := "1080p"
	for _, tc := range []struct {
		name    string
		probe   VideoFullResult
		policy  models.HDRDVPolicy
		wantErr bool
	}{
		{"compatible", VideoFullResult{VideoCodec: "h264", VideoWidth: 1920, VideoHeight: 1080}, models.HDRDVPolicyNoExclusion, false},
		{"too large", VideoFullResult{VideoCodec: "h264", VideoWidth: 3840, VideoHeight: 2160}, models.HDRDVPolicyIncludeHDRDV, true},
		{"unknown dimensions", VideoFullResult{VideoCodec: "h264"}, models.HDRDVPolicyIncludeHDRDV, true},
		{"hdr blocked", VideoFullResult{VideoCodec: "hevc", VideoWidth: 1920, VideoHeight: 1080, HasHDR10: true}, models.HDRDVPolicyNoExclusion, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateLocalFallbackQuality(&tc.probe, models.FilterSettings{MaxResolution: &resolution, HDRDVPolicy: tc.policy})
			if (err != nil) != tc.wantErr {
				t.Fatalf("quality error=%v", err)
			}
		})
	}
}

func TestLocalFallbackRealMediaRetainsGermanTrackAndIdentity(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required for media integration")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is required for media integration")
	}
	file := filepath.Join(t.TempDir(), "synthetic.mkv")
	cmd := exec.CommandContext(t.Context(), ffmpeg, "-nostdin", "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=320x180:r=24", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-f", "lavfi", "-i", "sine=frequency=660:sample_rate=48000", "-t", "2", "-map", "0:v", "-map", "1:a", "-map", "2:a", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", "-metadata:s:a:0", "language=eng", "-metadata:s:a:1", "language=deu", file)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate synthetic fixture: %v %s", err, output)
	}
	h, catalog, _ := newFallbackWorker(t, &fallbackSearch{})
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	catalog.item.SizeBytes = info.Size()
	video := &VideoHandler{ffprobePath: ffprobe, streamer: directCastTestProvider{directURL: file}, metadataCache: make(map[string]*cachedMetadataEntry)}
	h.fullProber = video
	h.metadataProber = video
	settings, err := user_settings.NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	preferences := models.DefaultUserSettings()
	preferences.Playback.PreferredAudioLanguage = "deu"
	if err := settings.Update("profile", preferences); err != nil {
		t.Fatal(err)
	}
	h.userSettingsSvc = settings
	got := runFallbackWorker(t, h)
	if got.Status != playback.PrequeueStatusReady || got.StreamPath != "localmedia:local-item/Synthetic.Movie.mkv" || got.SelectedAudioTrack != 2 || len(got.AudioTracks) != 2 || got.Duration < 1.9 {
		t.Fatalf("real local playback: status=%s path=%q audio=%d tracks=%d duration=%f error=%s", got.Status, got.StreamPath, got.SelectedAudioTrack, len(got.AudioTracks), got.Duration, got.Error)
	}
}

func TestLocalFallbackKeepsEpisodeIdentityAndRequiresManualDailyMatch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		daily     bool
		status    models.LocalMediaMatchStatus
		wantReady bool
	}{
		{"episode", false, models.LocalMediaMatchStatusMatched, true},
		{"daily requires correction", true, models.LocalMediaMatchStatusMatched, false},
		{"daily manual correction", true, models.LocalMediaMatchStatusManual, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, catalog, _ := newFallbackWorker(t, &fallbackSearch{})
			target := &models.EpisodeReference{SeasonNumber: 2, EpisodeNumber: 3, EpisodeID: "synthetic-episode", AirDate: "2024-06-01"}
			catalog.item.LibraryType = models.LocalMediaLibraryTypeShow
			catalog.item.MatchedTitleID = "tmdb:tv:123"
			catalog.item.SeasonNumber, catalog.item.EpisodeNumber = 2, 3
			catalog.item.MatchStatus = tc.status
			h.metadataSvc = &mockSeriesDetailsProvider{details: &models.SeriesDetails{Title: models.Title{Name: "Synthetic Series", IsDaily: tc.daily}}}
			entry, _ := h.store.Create("tmdb:tv:123", "Synthetic Series", "profile", "series", 2024, target, "manual")
			h.runPrequeueWorker(entry.ID, entry.TitleID, entry.TitleName, "", "series", 2024, "profile", "", nil, target, 42, true, prequeueWorkerInteractive, prequeueLocalScope{accountID: "account"})
			got, _ := h.store.Get(entry.ID)
			if (got.Status == playback.PrequeueStatusReady) != tc.wantReady {
				t.Fatalf("status=%s error=%s", got.Status, got.Error)
			}
			if tc.wantReady && (got.TitleID != entry.TitleID || got.TargetEpisode == nil || got.TargetEpisode.EpisodeID != target.EpisodeID || got.TargetEpisode.SeasonNumber != 2 || got.TargetEpisode.EpisodeNumber != 3) {
				t.Fatal("fallback replaced episode identity")
			}
		})
	}
}

func TestLocalFallbackDoesNotStartAfterCancellation(t *testing.T) {
	h, _, _ := newFallbackWorker(t, &fallbackSearch{wait: true})
	h.localFallbackWait = 5 * time.Second
	entry, _ := h.store.Create("tmdb:movie:123", "Synthetic Movie", "profile", "movie", 2024, nil, "manual")
	started := make(chan struct{})
	search := &fallbackSearch{wait: true}
	// The actual indexer signals its invocation through the fake search provider.
	h.indexerSvc = indexer.NewService(h.configManager, nil, search)
	go func() {
		h.runPrequeueWorker(entry.ID, entry.TitleID, entry.TitleName, "", "movie", 2024, "profile", "", nil, nil, 0, true, prequeueWorkerInteractive, prequeueLocalScope{accountID: "account"})
		close(started)
	}()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for search.calls.Load() == 0 {
		select {
		case <-deadline.C:
			t.Fatal("worker did not start")
		case <-time.After(time.Millisecond):
		}
	}
	h.store.CancelWork(entry.ID)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cancelled worker did not stop")
	}
	got, _ := h.store.Get(entry.ID)
	if got.StreamPath != "" || got.Status == playback.PrequeueStatusReady {
		t.Fatal("started local playback after cancellation")
	}
}

func runFallbackWorker(t *testing.T, h *PrequeueHandler) *playback.PrequeueEntry {
	t.Helper()
	entry, _ := h.store.Create("tmdb:movie:123", "Synthetic Movie", "profile", "movie", 2024, nil, "manual")
	started := time.Now()
	h.runPrequeueWorker(entry.ID, entry.TitleID, entry.TitleName, "", "movie", 2024, "profile", "", nil, nil, 0, true, prequeueWorkerInteractive, prequeueLocalScope{accountID: "account"})
	if time.Since(started) > 2*time.Second {
		t.Fatal("local fallback did not bound remote preparation")
	}
	got, _ := h.store.Get(entry.ID)
	if got.TitleID != entry.TitleID || got.UserID != entry.UserID {
		t.Fatal("fallback changed playback identity")
	}
	return got
}

func TestLocalFallbackWorkerHandlesMissingFailedAndSlowSources(t *testing.T) {
	remote := models.NZBResult{Title: "Synthetic.Movie.1080p.WEB-DL", Link: "https://example.invalid/synthetic", ServiceType: models.ServiceTypeDebrid, Indexer: "AIOStreams", Attributes: map[string]string{"resolution": "1080p"}}
	for _, mode := range []string{"no results", "slow search", "failed preparation", "slow preparation", "remote success", "missing local file", "denied library"} {
		t.Run(mode, func(t *testing.T) {
			search := &fallbackSearch{}
			if mode == "slow search" {
				search.wait = true
			}
			if mode == "failed preparation" || mode == "slow preparation" || mode == "remote success" {
				search.results = []models.NZBResult{remote}
			}
			h, catalog, access := newFallbackWorker(t, search)
			if mode == "missing local file" {
				catalog.unavailable = true
			}
			if mode == "denied library" {
				access.deny.Store(true)
			}
			h.playbackSvc = &stubPlaybackService{resolve: func(ctx context.Context, _ models.NZBResult) (*models.PlaybackResolution, error) {
				if mode == "slow preparation" {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				if mode == "failed preparation" {
					return nil, errors.New("synthetic unavailable articles")
				}
				return &models.PlaybackResolution{WebDAVPath: "/webdav/synthetic-remote.mkv"}, nil
			}}
			got := runFallbackWorker(t, h)
			if search.calls.Load() == 0 {
				t.Fatal("worker skipped the preferred search path")
			}
			if mode == "missing local file" || mode == "denied library" {
				if got.Status != playback.PrequeueStatusFailed || got.StreamPath != "" {
					t.Fatalf("unexpected playback: %+v", got)
				}
				return
			}
			want := "localmedia:local-item/Synthetic.Movie.mkv"
			if mode == "remote success" {
				want = "/webdav/synthetic-remote.mkv"
			}
			if got.Status != playback.PrequeueStatusReady || got.StreamPath != want {
				t.Fatalf("status=%s path=%q error=%q; want %q", got.Status, got.StreamPath, got.Error, want)
			}
		})
	}
}

func TestLocalFallbackKidsAuthorization(t *testing.T) {
	h, catalog, _ := newFallbackWorker(t, &fallbackSearch{})
	for _, tc := range []struct {
		name, mode, limit, rating string
		want                      bool
	}{
		{"adult", "", "", "", true}, {"allowed rating", "rating", "PG", "G", true}, {"blocked rating", "rating", "G", "R", false}, {"unrated", "rating", "PG", "", false}, {"unknown rating", "rating", "PG", "FSK 12", false}, {"content list", "content_list", "PG", "G", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.users = fallbackProfiles{models.User{ID: "profile", IsKidsProfile: tc.name != "adult", KidsMode: tc.mode, KidsMaxMovieRating: tc.limit}}
			item := catalog.item
			item.Metadata = &models.Title{Certification: tc.rating}
			if got := h.localItemAllowed(item, "profile"); got != tc.want {
				t.Fatalf("allowed=%v want %v", got, tc.want)
			}
		})
	}
}

func TestLocalFallbackRejectsChangedMatchAndRevokedAccess(t *testing.T) {
	h, catalog, access := newFallbackWorker(t, &fallbackSearch{})
	original := catalog.item
	opts := prequeueResolutionOptions{mediaType: "movie", userID: "profile", localScope: &prequeueLocalScope{accountID: "account"}}
	catalog.item.MatchedTitleID = "tmdb:movie:456"
	if _, err := h.resolveLocalItem(t.Context(), original, opts); err == nil {
		t.Fatal("accepted rematched item")
	}
	catalog.item = original
	access.deny.Store(true)
	if _, err := h.resolveLocalItem(t.Context(), original, opts); err == nil {
		t.Fatal("accepted revoked access")
	}
	entry := &playback.PrequeueEntry{UserID: "profile", StreamPath: "localmedia:local-item/Synthetic.Movie.mkv"}
	if h.localEntryAllowed(httptest.NewRequest("GET", "/", nil), entry) {
		t.Fatal("exposed revoked local prequeue")
	}
}

func TestLocalFallbackHonorsAdaptiveSizeLimit(t *testing.T) {
	h, catalog, _ := newFallbackWorker(t, &fallbackSearch{})
	limit := float64(1)
	catalog.item.SizeBytes = 2 * 1024 * 1024 * 1024
	_, err := h.resolveLocalItem(t.Context(), catalog.item, prequeueResolutionOptions{mediaType: "movie", userID: "profile", localScope: &prequeueLocalScope{accountID: "account"}, adaptiveCaps: models.AdaptiveCaps{MaxSizeMovieGB: &limit}})
	if err == nil {
		t.Fatal("accepted file exceeding current adaptive cap")
	}
}
