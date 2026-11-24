package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/models"
	"novastream/services/badstreams"
	"novastream/services/indexer"
	"novastream/services/playback"
)

func TestNormalizePrequeueSeriesTitle(t *testing.T) {
	tests := map[string]string{
		"Legion • S02E01 – Chapter 9":          "Legion",
		"One Piece • S23E1162 – Episode Title": "One Piece",
		"Formula 1": "Formula 1",
	}

	for input, want := range tests {
		if got := normalizePrequeueSeriesTitle(input); got != want {
			t.Errorf("normalizePrequeueSeriesTitle(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestResolveFirstReadySourceForWorker(t *testing.T) {
	if !resolveFirstReadySourceForWorker(true, prequeueWorkerInteractive) {
		t.Fatal("interactive prequeue must honor enabled early resolution")
	}
	if resolveFirstReadySourceForWorker(true, prequeueWorkerScheduledPrewarm) {
		t.Fatal("scheduled pre-warm must ignore enabled early resolution")
	}
	if resolveFirstReadySourceForWorker(false, prequeueWorkerInteractive) {
		t.Fatal("interactive prequeue must preserve disabled early resolution")
	}
}

func TestCombinedPrequeueSearchOptionsPreservesAlternateTitles(t *testing.T) {
	opts := combinedPrequeueSearchOptions(indexer.SearchOptions{
		TitleID:         "tmdb:tv:299939",
		Query:           "Batman: Death in the Family",
		AlternateTitles: []string{"DC Showcase - Batman: Death in the Family"},
	})

	if !opts.IncludeFiltered {
		t.Fatal("combined prequeue search must include filtered results")
	}
	if opts.TitleID != "tmdb:tv:299939" {
		t.Fatalf("selected title identity lost: %q", opts.TitleID)
	}
	if len(opts.AlternateTitles) != 1 || opts.AlternateTitles[0] != "DC Showcase - Batman: Death in the Family" {
		t.Fatalf("alternate titles = %v, want canonical DC Showcase title", opts.AlternateTitles)
	}
}

func TestHasReusablePreparationRequiresCompleteDolbyVisionConfiguration(t *testing.T) {
	legacy := &playback.PrequeueEntry{
		HasDolbyVision:     true,
		DolbyVisionProfile: "dvhe.08.06",
		AudioTracks:        []playback.AudioTrackInfo{{Index: 1}},
	}
	if hasReusablePreparation(legacy) {
		t.Fatal("legacy Dolby Vision entry without decoder configuration should require a fresh probe")
	}

	legacy.DolbyVisionConfiguration = &models.DolbyVisionConfiguration{
		StreamIndex:             0,
		VersionMajor:            1,
		Profile:                 8,
		Level:                   6,
		RPUPresentFlag:          1,
		BLPresentFlag:           1,
		BLSignalCompatibilityID: 1,
	}
	if hasReusablePreparation(legacy) {
		t.Fatal("Dolby Vision entry without a pre-probed pixel format should require a fresh probe")
	}

	legacy.DolbyVisionConfiguration.PixelFormat = "yuv420p10le"
	if !hasReusablePreparation(legacy) {
		t.Fatal("Dolby Vision entry with decoder configuration and pixel format should remain reusable")
	}
}

func TestAdaptiveThroughputDoesNotChangePrequeueScope(t *testing.T) {
	base := prequeueScopeSignature{
		Filtering: models.FilterSettings{
			MaxSizeEpisodeGB: models.FloatPtr(0),
			HDRDVPolicy:      models.HDRDVPolicyIncludeHDR,
		},
	}

	fast := base
	displayPolicy := models.HDRDVPolicyIncludeHDRDV
	applyAdaptiveScopePolicy(&fast.Filtering, models.AdaptiveCaps{
		MaxSizeEpisodeGB: models.FloatPtr(32),
		HDRDVPolicy:      &displayPolicy,
	})

	slower := base
	applyAdaptiveScopePolicy(&slower.Filtering, models.AdaptiveCaps{
		MaxSizeEpisodeGB: models.FloatPtr(8),
		HDRDVPolicy:      &displayPolicy,
	})

	if got, want := prequeueScopeHash(fast), prequeueScopeHash(slower); got != want {
		t.Fatalf("throughput-only cap change altered scope: fast=%s slower=%s", got, want)
	}

	sdr := base
	sdrPolicy := models.HDRDVPolicyNoExclusion
	applyAdaptiveScopePolicy(&sdr.Filtering, models.AdaptiveCaps{HDRDVPolicy: &sdrPolicy})
	if prequeueScopeHash(fast) == prequeueScopeHash(sdr) {
		t.Fatal("display compatibility change should still alter scope")
	}
}

// Two devices of one profile with different quality presets must not share
// prequeues.
func TestDevicePresetChangesPrequeueScope(t *testing.T) {
	german, english := "p-german", "p-english"
	base := prequeueScopeSignature{}
	profile := base
	profile.QualityPresets = profile.QualityPresets.withPresets(nil, &german, nil, nil)

	tv := profile
	applyClientScopeOverrides(&tv, &models.ClientFilterSettings{QualityPresetSeries: &english})
	phone := profile
	applyClientScopeOverrides(&phone, &models.ClientFilterSettings{})

	if prequeueScopeHash(tv) == prequeueScopeHash(phone) {
		t.Fatal("a device's own series preset must change the scope")
	}
	if prequeueScopeHash(phone) != prequeueScopeHash(profile) {
		t.Fatal("a device without its own preset keeps the profile's scope")
	}
	if prequeueScopeHash(profile) == prequeueScopeHash(base) {
		t.Fatal("the profile's preset must change the scope")
	}
	if *profile.QualityPresets.Series != german || profile.QualityPresets.Movie != nil {
		t.Fatalf("profile presets %+v", profile.QualityPresets)
	}
}

func TestReadyEntryFitsAdaptiveCaps(t *testing.T) {
	const gib = int64(1024 * 1024 * 1024)

	episode := &playback.PrequeueEntry{MediaType: "series", FileSize: 6 * gib}
	if !readyEntryFitsAdaptiveCaps(episode, models.AdaptiveCaps{MaxSizeEpisodeGB: models.FloatPtr(8)}) {
		t.Fatal("ready episode below the new throughput cap should remain reusable")
	}
	if readyEntryFitsAdaptiveCaps(episode, models.AdaptiveCaps{MaxSizeEpisodeGB: models.FloatPtr(5)}) {
		t.Fatal("ready episode above the new throughput cap should require fresh resolution")
	}
	if readyEntryFitsAdaptiveCaps(&playback.PrequeueEntry{MediaType: "series"}, models.AdaptiveCaps{MaxSizeEpisodeGB: models.FloatPtr(8)}) {
		t.Fatal("entry with unknown size cannot be proven safe under an adaptive cap")
	}
	if !readyEntryFitsAdaptiveCaps(episode, models.AdaptiveCaps{}) {
		t.Fatal("entry should remain reusable when adaptive throughput supplies no size cap")
	}
}

type prequeueRoundTripFunc func(*http.Request) (*http.Response, error)

func (f prequeueRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type adoptMigrationPrewarmMock struct {
	updated []string
}

func (m *adoptMigrationPrewarmMock) GetWarm(titleID, userID string) *playback.WarmRef {
	return nil
}

func (m *adoptMigrationPrewarmMock) GetWarmScoped(titleID, userID, settingsScopeKey string) *playback.WarmRef {
	return nil
}

func (m *adoptMigrationPrewarmMock) AdoptEntry(prequeueID string) {}

func (m *adoptMigrationPrewarmMock) UpdateFromPrequeue(prequeueID string) {
	m.updated = append(m.updated, prequeueID)
}

func (m *adoptMigrationPrewarmMock) InvalidatePrequeue(prequeueID string) {}

type adoptMigrationFullProber struct {
	path  string
	calls int
}

func (m *adoptMigrationFullProber) ProbeVideoFull(_ context.Context, path string) (*VideoFullResult, error) {
	m.path = path
	m.calls++
	return &VideoFullResult{
		VideoCodec:         "h264",
		HasDolbyVision:     true,
		HasHDR10:           true,
		DolbyVisionProfile: "8",
		HasTrueHD:          true,
		HasCompatibleAudio: true,
		Duration:           123.5,
		AudioStreams: []AudioStreamInfo{
			{Index: 1, Codec: "eac3", Language: "eng", Title: "English"},
			{Index: 2, Codec: "aac", Language: "spa", Title: "Spanish"},
		},
		SubtitleStreams: []SubtitleStreamInfo{
			{Index: 3, Codec: "hdmv_pgs_subtitle", Language: "eng", Title: "English PGS"},
		},
	}, nil
}

func TestProbeResolvedCandidateReusesPlaybackProbe(t *testing.T) {
	prober := &adoptMigrationFullProber{}
	want := &models.VideoFullResult{
		VideoCodec: "hevc",
		AudioStreams: []models.AudioStreamInfo{
			{Index: 1, Codec: "ac3", Language: "eng"},
		},
	}
	got, err := probeResolvedCandidate(context.Background(), prober, &models.PlaybackResolution{
		WebDAVPath: "https://example.invalid/video.mkv",
		Probe:      want,
	})
	if err != nil {
		t.Fatalf("probeResolvedCandidate returned error: %v", err)
	}
	if got != want {
		t.Fatalf("probe result = %#v, want reused result %#v", got, want)
	}
	if prober.calls != 0 {
		t.Fatalf("ProbeVideoFull calls = %d, want 0", prober.calls)
	}
}

func TestValidatePrequeueVideoProbe(t *testing.T) {
	tests := []struct {
		name    string
		result  *VideoFullResult
		wantErr bool
	}{
		{name: "missing result", result: nil, wantErr: true},
		{name: "missing video track", result: &VideoFullResult{AudioStreams: []AudioStreamInfo{{Index: 1}}}, wantErr: true},
		{name: "playable video track", result: &VideoFullResult{VideoCodec: "h264"}, wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePrequeueVideoProbe(tt.result)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validatePrequeueVideoProbe() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// stubPlaybackService implements the prequeue playback-service seam so the
// concurrent resolution race can be exercised without a live importer/NNTP stack.
type stubPlaybackService struct {
	resolve func(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error)
}

func (s *stubPlaybackService) Resolve(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error) {
	if s.resolve == nil {
		return &models.PlaybackResolution{WebDAVPath: "/webdav/title.mkv"}, nil
	}
	return s.resolve(ctx, candidate)
}

func (s *stubPlaybackService) QueueStatus(ctx context.Context, queueID int64) (*models.PlaybackResolution, error) {
	return &models.PlaybackResolution{WebDAVPath: "/webdav/title.mkv"}, nil
}

func (s *stubPlaybackService) PrepareTorrentCandidates(ctx context.Context, candidates []models.NZBResult) []models.NZBResult {
	return candidates
}

type raceProbeResult struct{}

func (*raceProbeResult) ProbeVideoFull(ctx context.Context, path string) (*VideoFullResult, error) {
	return &VideoFullResult{VideoCodec: "h264", Duration: 100}, nil
}

// slowFailingResolve fails slowly at a generous 3s cadence but aborts instantly
// once its context is cancelled — the shape of a dead/slow top-ranked release
// whose articles stall resolution.
func slowFailingResolve(ctx context.Context, _ models.NZBResult) (*models.PlaybackResolution, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(3 * time.Second):
		return nil, fmt.Errorf("articles unavailable")
	}
}

// testResolutionConfigManager saves settings to a temp file and returns a
// config.Manager pointing at it, so handler tests can exercise the
// settings-driven resolution race policy (settle window + end-race-early).
func testResolutionConfigManager(t *testing.T, settings config.Settings) *config.Manager {
	t.Helper()
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	return mgr
}

func TestRacePrequeueResolutionsAdoptsFastSecondCandidate(t *testing.T) {
	// Bounded-mode regression coverage: with a positive settle window a
	// slow/failing top candidate must not stall the race — the fast healthy
	// candidate is adopted once the settle window elapses, far less than the
	// serial sum of the two resolutions. (Strict order, the default, instead
	// intentionally waits for the top candidate to fail; this test pins the
	// bounded fast-path compromise.)
	process := func(ctx context.Context, i int, _ models.NZBResult) (*candidateResolution, *candidateResolution, error) {
		switch i {
		case 0:
			_, err := slowFailingResolve(ctx, models.NZBResult{Title: "slow-dead"})
			return nil, nil, err
		case 1:
			return &candidateResolution{
				index:      1,
				result:     models.NZBResult{Title: "fast-good"},
				resolution: &models.PlaybackResolution{WebDAVPath: "/webdav/fast.mkv"},
			}, nil, nil
		default:
			return nil, nil, fmt.Errorf("unexpected index %d", i)
		}
	}

	start := time.Now()
	src := newSliceCandidateSource([]models.NZBResult{
		{Title: "slow-dead"}, {Title: "fast-good"},
	})
	winner, usedFallback, err := racePrequeueResolutions(context.Background(), src, 4, process, nil, 200*time.Millisecond, true)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("race returned error: %v", err)
	}
	if usedFallback {
		t.Fatal("fast candidate was adopted as a fallback instead of winning directly")
	}
	if winner == nil || winner.index != 1 {
		t.Fatalf("winner index = %v, want 1", winnerIndex(winner))
	}
	if elapsed >= time.Second {
		t.Fatalf("race took %v; the slow candidate was not cancelled when the bounded window elapsed (serial sum would be >3s)", elapsed)
	}
}

func TestPrequeuePreferenceBucketsIgnoreServicePriority(t *testing.T) {
	scored := []models.ScoredNZBResult{
		{NZBResult: models.NZBResult{Title: "preferred-service-2160"}, ScoreBreakdown: []models.ScoreBreakdownItem{
			{Criterion: "Service Priority", Points: 30_000},
			{Criterion: "Resolution", Points: 40_000},
		}},
		{NZBResult: models.NZBResult{Title: "other-service-2160"}, ScoreBreakdown: []models.ScoreBreakdownItem{
			{Criterion: "Service Priority", Points: 0},
			{Criterion: "Resolution", Points: 40_000},
		}},
		{NZBResult: models.NZBResult{Title: "preferred-service-1080"}, ScoreBreakdown: []models.ScoreBreakdownItem{
			{Criterion: "Service Priority", Points: 30_000},
			{Criterion: "Resolution", Points: 20_000},
		}},
	}

	buckets, criterion := prequeuePreferenceBuckets(scored)
	if criterion != "Resolution" {
		t.Fatalf("criterion = %q, want Resolution", criterion)
	}
	if want := []int{0, 0, 1}; !reflect.DeepEqual(buckets, want) {
		t.Fatalf("buckets = %v, want %v", buckets, want)
	}
}

func TestPrequeuePreferenceBucketsRankScoresRegardlessOfCandidateOrder(t *testing.T) {
	for _, tc := range []struct {
		name   string
		points []int
		want   []int
	}{
		{"promoted 720p before 2160p and 1080p", []int{10000, 40000, 20000, 10000, 20000}, []int{2, 0, 1, 2, 1}},
		{"negative preference scores", []int{-200, 0, -100, -200}, []int{2, 0, 1, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scored := make([]models.ScoredNZBResult, len(tc.points))
			for i, points := range tc.points {
				scored[i].ScoreBreakdown = []models.ScoreBreakdownItem{{Criterion: "Resolution", Points: points}}
			}
			buckets, criterion := prequeuePreferenceBuckets(scored)
			if criterion != "Resolution" || !reflect.DeepEqual(buckets, tc.want) {
				t.Fatalf("buckets = %v (%s), want %v (Resolution)", buckets, criterion, tc.want)
			}
		})
	}
}

func TestRacePrequeueResolutionsDoesNotStartLowerPreferenceBucketEarly(t *testing.T) {
	src := newStreamCandidateSource()
	topStarted := make(chan struct{}, 1)
	lowerStarted := make(chan struct{}, 1)
	releaseTop := make(chan struct{})

	process := func(_ context.Context, i int, candidate models.NZBResult) (*candidateResolution, *candidateResolution, error) {
		if candidate.Title == "2160-dead" {
			topStarted <- struct{}{}
			<-releaseTop
			return nil, nil, errors.New("top bucket unavailable")
		}
		lowerStarted <- struct{}{}
		return &candidateResolution{
			index:      i,
			result:     candidate,
			resolution: &models.PlaybackResolution{WebDAVPath: "/webdav/1080.mkv"},
		}, nil, nil
	}

	type outcome struct {
		winner *candidateResolution
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		winner, _, err := racePrequeueResolutions(context.Background(), src, 8, process, nil, 25*time.Millisecond, true)
		done <- outcome{winner: winner, err: err}
	}()
	go func() {
		src.feedRanked(prequeueRankedCandidate{result: models.NZBResult{Title: "2160-dead"}, bucket: 0})
		src.feedRanked(prequeueRankedCandidate{result: models.NZBResult{Title: "1080-fast"}, bucket: 1})
		src.Close()
	}()

	select {
	case <-topStarted:
	case <-time.After(time.Second):
		t.Fatal("preferred bucket did not start")
	}
	select {
	case <-lowerStarted:
		t.Fatal("lower preference bucket started while the preferred bucket was viable")
	case <-time.After(75 * time.Millisecond):
	}
	close(releaseTop)

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("race returned error: %v", got.err)
		}
		if got.winner == nil || got.winner.result.Title != "1080-fast" {
			t.Fatalf("winner = %+v, want lower bucket after preferred bucket failed", got.winner)
		}
	case <-time.After(time.Second):
		t.Fatal("race did not advance after preferred bucket failed")
	}
}

func TestRacePrequeueResolutionsReservesSlotForLaterSource(t *testing.T) {
	src := newStreamCandidateSource()
	raceCtx, cancelRace := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancelRace()
		src.Stop()
	})
	started := make(chan models.ContentServiceType, 16)
	process := func(ctx context.Context, i int, candidate models.NZBResult) (*candidateResolution, *candidateResolution, error) {
		started <- candidate.ServiceType
		if candidate.ServiceType == models.ServiceTypeDebrid {
			return &candidateResolution{
				index:      i,
				result:     candidate,
				resolution: &models.PlaybackResolution{WebDAVPath: "/webdav/debrid.mkv"},
			}, nil, nil
		}
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}

	type raceOutcome struct {
		winner *candidateResolution
		err    error
	}
	done := make(chan raceOutcome, 1)
	go func() {
		winner, _, err := racePrequeueResolutions(raceCtx, src, 8, process, nil, 100*time.Millisecond, true)
		done <- raceOutcome{winner: winner, err: err}
	}()

	// The first-ready service has more work than the entire race width. It may
	// occupy seven slots, but the eighth remains available for a later source.
	for i := 0; i < 8; i++ {
		if !src.Feed(models.NZBResult{Title: fmt.Sprintf("usenet-%d", i), ServiceType: models.ServiceTypeUsenet}) {
			t.Fatalf("stream rejected usenet candidate %d", i)
		}
	}
	for i := 0; i < 7; i++ {
		select {
		case serviceType := <-started:
			if serviceType != models.ServiceTypeUsenet {
				t.Fatalf("started service = %q, want usenet", serviceType)
			}
		case <-time.After(time.Second):
			t.Fatalf("only %d of 7 reserved-width usenet candidates started", i)
		}
	}
	select {
	case serviceType := <-started:
		t.Fatalf("unexpected eighth candidate started before peer source: %q", serviceType)
	case <-time.After(50 * time.Millisecond):
	}

	if !src.Feed(models.NZBResult{Title: "debrid-ready", ServiceType: models.ServiceTypeDebrid}) {
		t.Fatal("stream rejected later debrid candidate")
	}
	select {
	case outcome := <-done:
		src.Stop()
		if outcome.err != nil {
			t.Fatalf("race returned error: %v", outcome.err)
		}
		if outcome.winner == nil || outcome.winner.result.ServiceType != models.ServiceTypeDebrid {
			t.Fatalf("winner = %+v, want later debrid source", outcome.winner)
		}
	case <-time.After(time.Second):
		src.Stop()
		t.Fatal("later debrid source did not receive the reserved resolution slot")
	}
}

func TestRacePrequeueResolutionsReportsExhaustionContext(t *testing.T) {
	topRankedErr := errors.New("restricted release")
	src := newSliceCandidateSource([]models.NZBResult{
		{Title: "first"},
		{Title: "second"},
	})
	process := func(_ context.Context, i int, _ models.NZBResult) (*candidateResolution, *candidateResolution, error) {
		if i == 0 {
			return nil, nil, topRankedErr
		}
		return nil, nil, errors.New("not cached")
	}

	_, _, err := racePrequeueResolutions(context.Background(), src, 2, process, nil, 0, false)
	if err == nil {
		t.Fatal("race returned nil error for exhausted candidates")
	}
	if !errors.Is(err, topRankedErr) {
		t.Fatalf("error %q does not wrap the top-ranked failure", err)
	}
	if !strings.Contains(err.Error(), "all 2 candidates failed to resolve") {
		t.Fatalf("error = %q, want candidate exhaustion context", err)
	}
}

func winnerIndex(w *candidateResolution) int {
	if w == nil {
		return -1
	}
	return w.index
}

func TestRacePrequeueResolutionsSettlePrefersBetterRankedArrivingLate(t *testing.T) {
	// A worse-ranked candidate validates first; the better-ranked candidate is
	// still mid-download and validates shortly after. The settle window must
	// keep the better-ranked candidate alive and prefer it, instead of
	// finalizing the first validator instantly.
	process := func(ctx context.Context, i int, _ models.NZBResult) (*candidateResolution, *candidateResolution, error) {
		switch i {
		case 0:
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(300 * time.Millisecond):
			}
			return &candidateResolution{
				index:      0,
				result:     models.NZBResult{Title: "better-slow"},
				resolution: &models.PlaybackResolution{WebDAVPath: "/webdav/better.mkv"},
			}, nil, nil
		case 1:
			// Both candidates start resolving together; the worse-ranked one
			// merely validates a little sooner (the realistic "lost by a few ms"
			// shape) — the 50ms preamble also ensures the main loop has observed
			// candidate 0's dispatch before this result lands.
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
			return &candidateResolution{
				index:      1,
				result:     models.NZBResult{Title: "worse-fast"},
				resolution: &models.PlaybackResolution{WebDAVPath: "/webdav/worse.mkv"},
			}, nil, nil
		default:
			return nil, nil, fmt.Errorf("unexpected index %d", i)
		}
	}

	src := newSliceCandidateSource([]models.NZBResult{{Title: "better-slow"}, {Title: "worse-fast"}})
	start := time.Now()
	winner, usedFallback, err := racePrequeueResolutions(context.Background(), src, 4, process, nil, 1000*time.Millisecond, true)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("race returned error: %v", err)
	}
	if usedFallback {
		t.Fatal("first valid candidate was adopted as a fallback instead of winning directly")
	}
	if winner == nil || winner.index != 0 {
		t.Fatalf("winner index = %v, want 0 (better-ranked late arrival must win the settle)", winnerIndex(winner))
	}
	// The race must have actually waited for the better-ranked candidate (so it
	// did not finalize the fast winner on arrival) but settled well inside the
	// 1s window.
	if elapsed < 150*time.Millisecond {
		t.Fatalf("race returned in %v, want it to wait for the better-ranked candidate", elapsed)
	}
	if elapsed >= 900*time.Millisecond {
		t.Fatalf("race took %v, want the settle to close once the better-ranked candidate lands", elapsed)
	}
}

func TestRacePrequeueResolutionsSettleTimesOutKeepingFastWinner(t *testing.T) {
	// The better-ranked candidate never validates within the window (it is
	// dead/slow): the first validator must be finalized after the settle
	// timeout, cancelling the stuck candidate instead of waiting for it.
	process := func(ctx context.Context, i int, _ models.NZBResult) (*candidateResolution, *candidateResolution, error) {
		switch i {
		case 0:
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(5 * time.Second):
				return nil, nil, fmt.Errorf("articles unavailable")
			}
		case 1:
			// Same preamble as the prefer test: validate a short moment after
			// candidate 0 is dispatched so the settle window is reliably entered.
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
			return &candidateResolution{
				index:      1,
				result:     models.NZBResult{Title: "worse-fast"},
				resolution: &models.PlaybackResolution{WebDAVPath: "/webdav/worse.mkv"},
			}, nil, nil
		default:
			return nil, nil, fmt.Errorf("unexpected index %d", i)
		}
	}

	src := newSliceCandidateSource([]models.NZBResult{{Title: "better-dead"}, {Title: "worse-fast"}})
	start := time.Now()
	winner, usedFallback, err := racePrequeueResolutions(context.Background(), src, 4, process, nil, 400*time.Millisecond, true)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("race returned error: %v", err)
	}
	if usedFallback {
		t.Fatal("fast candidate was adopted as a fallback instead of winning directly")
	}
	if winner == nil || winner.index != 1 {
		t.Fatalf("winner index = %v, want 1 (fast candidate after settle timeout)", winnerIndex(winner))
	}
	// The settle must have bounded the wait: the dead candidate was cancelled
	// at the window close, well under its 5s stall.
	if elapsed < 250*time.Millisecond {
		t.Fatalf("race returned in %v, want it to respect the settle window", elapsed)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("race took %v, want the dead better-ranked candidate cancelled at the settle timeout", elapsed)
	}
}

func TestRacePrequeueResolutionsSettleSkippedWhenWinnerBestRanked(t *testing.T) {
	// The first validator is already the best-ranked candidate in flight: no
	// settle window is entered and the race returns immediately with zero added
	// latency (the fast path must not pay for a worse-ranked candidate that is
	// still resolving).
	process := func(ctx context.Context, i int, _ models.NZBResult) (*candidateResolution, *candidateResolution, error) {
		switch i {
		case 0:
			return &candidateResolution{
				index:      0,
				result:     models.NZBResult{Title: "best-fast"},
				resolution: &models.PlaybackResolution{WebDAVPath: "/webdav/best.mkv"},
			}, nil, nil
		case 1:
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(5 * time.Second):
				return nil, nil, fmt.Errorf("articles unavailable")
			}
		default:
			return nil, nil, fmt.Errorf("unexpected index %d", i)
		}
	}

	src := newSliceCandidateSource([]models.NZBResult{{Title: "best-fast"}, {Title: "worse-slow"}})
	start := time.Now()
	winner, usedFallback, err := racePrequeueResolutions(context.Background(), src, 4, process, nil, 1000*time.Millisecond, true)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("race returned error: %v", err)
	}
	if usedFallback {
		t.Fatal("winner adopted as fallback")
	}
	if winner == nil || winner.index != 0 {
		t.Fatalf("winner index = %v, want 0", winnerIndex(winner))
	}
	if elapsed >= 300*time.Millisecond {
		t.Fatalf("fast path took %v, want immediate adoption with no settle window", elapsed)
	}
}

func TestRacePrequeueResolutionsZeroSettleEndsOnFirstValidCandidate(t *testing.T) {
	// With endEarly enabled, a zero settle window means no grace for an
	// already-running, better-ranked candidate: the first valid candidate wins.
	process := func(ctx context.Context, i int, _ models.NZBResult) (*candidateResolution, *candidateResolution, error) {
		switch i {
		case 0:
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
			return &candidateResolution{index: 0, result: models.NZBResult{Title: "better-slow"}, resolution: &models.PlaybackResolution{WebDAVPath: "/webdav/better.mkv"}}, nil, nil
		case 1:
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(30 * time.Millisecond):
			}
			return &candidateResolution{index: 1, result: models.NZBResult{Title: "worse-fast"}, resolution: &models.PlaybackResolution{WebDAVPath: "/webdav/worse.mkv"}}, nil, nil
		default:
			return nil, nil, fmt.Errorf("unexpected index %d", i)
		}
	}

	src := newSliceCandidateSource([]models.NZBResult{{Title: "better-slow"}, {Title: "worse-fast"}})
	start := time.Now()
	winner, usedFallback, err := racePrequeueResolutions(context.Background(), src, 4, process, nil, 0, true)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("race returned error: %v", err)
	}
	if usedFallback {
		t.Fatal("better-ranked candidate was adopted as a fallback instead of winning directly")
	}
	if winner == nil || winner.index != 1 {
		t.Fatalf("winner index = %v, want 1 (first valid candidate with no grace)", winnerIndex(winner))
	}
	if elapsed >= 150*time.Millisecond {
		t.Fatalf("race took %v, want immediate adoption without a settle grace", elapsed)
	}
}

func TestRacePrequeueResolutionsLegacyNegativeSettleAlsoMeansNoGrace(t *testing.T) {
	// Defensive runtime behavior matches config normalization: a legacy negative
	// settle value means no grace, not an undocumented unbounded wait.
	process := func(ctx context.Context, i int, _ models.NZBResult) (*candidateResolution, *candidateResolution, error) {
		switch i {
		case 0:
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(150 * time.Millisecond):
				return nil, nil, fmt.Errorf("articles unavailable")
			}
		case 1:
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(20 * time.Millisecond):
			}
			return &candidateResolution{index: 1, result: models.NZBResult{Title: "fast-good"}, resolution: &models.PlaybackResolution{WebDAVPath: "/webdav/fast.mkv"}}, nil, nil
		default:
			return nil, nil, fmt.Errorf("unexpected index %d", i)
		}
	}

	src := newSliceCandidateSource([]models.NZBResult{{Title: "slow-dead"}, {Title: "fast-good"}})
	start := time.Now()
	winner, usedFallback, err := racePrequeueResolutions(context.Background(), src, 4, process, nil, -1, true)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("race returned error: %v", err)
	}
	if usedFallback {
		t.Fatal("fast candidate was adopted as a fallback instead of winning directly")
	}
	if winner == nil || winner.index != 1 {
		t.Fatalf("winner index = %v, want 1 (first valid candidate with legacy negative grace)", winnerIndex(winner))
	}
	if elapsed >= 120*time.Millisecond {
		t.Fatalf("race took %v, want legacy negative settle to behave as no grace", elapsed)
	}
}

func TestRacePrequeueResolutionsEndRaceEarlyOffWaitsForBatch(t *testing.T) {
	// endEarly=false: even when the very first release to validate is already
	// the best-ranked candidate in flight, the race keeps going until the whole
	// batch drains — a streaming source can still feed a higher-preference
	// release — and only then adopts the best-ranked validator. The fast path
	// (endEarly=true) is the thing this deliberately abandons.
	process := func(ctx context.Context, i int, _ models.NZBResult) (*candidateResolution, *candidateResolution, error) {
		switch i {
		case 0:
			return &candidateResolution{index: 0, result: models.NZBResult{Title: "best-fast"}, resolution: &models.PlaybackResolution{WebDAVPath: "/webdav/best.mkv"}}, nil, nil
		case 1:
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(150 * time.Millisecond):
				return nil, nil, fmt.Errorf("articles unavailable")
			}
		default:
			return nil, nil, fmt.Errorf("unexpected index %d", i)
		}
	}

	src := newSliceCandidateSource([]models.NZBResult{{Title: "best-fast"}, {Title: "worse-slow"}})
	start := time.Now()
	// endEarly=false must wait for the batch even though the settle grace is off
	// and the best-ranked candidate validated instantly.
	winner, usedFallback, err := racePrequeueResolutions(context.Background(), src, 4, process, nil, 0, false)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("race returned error: %v", err)
	}
	if usedFallback {
		t.Fatal("winner adopted as fallback")
	}
	if winner == nil || winner.index != 0 {
		t.Fatalf("winner index = %v, want 0", winnerIndex(winner))
	}
	// endEarly=false must NOT conclude the instant the best-ranked candidate
	// validates: it waited for the batch (the slow worse-ranked candidate) to
	// drain before deciding.
	if elapsed < 120*time.Millisecond {
		t.Fatalf("endRaceEarly off returned in %v, want it to wait for the batch to drain", elapsed)
	}
}

func TestRacePrequeueResolutionsEndRaceEarlyOffIgnoresBoundedSettleWindow(t *testing.T) {
	// endEarly=false must drain the batch even when a positive settle window is
	// configured. The settle window only bounds an early-ending race; it must not
	// override the explicit instruction to keep racing until every candidate has
	// resolved or failed.
	process := func(ctx context.Context, i int, _ models.NZBResult) (*candidateResolution, *candidateResolution, error) {
		switch i {
		case 0:
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
			return &candidateResolution{index: 0, result: models.NZBResult{Title: "better-slow"}, resolution: &models.PlaybackResolution{WebDAVPath: "/webdav/better.mkv"}}, nil, nil
		case 1:
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(30 * time.Millisecond):
			}
			return &candidateResolution{index: 1, result: models.NZBResult{Title: "worse-fast"}, resolution: &models.PlaybackResolution{WebDAVPath: "/webdav/worse.mkv"}}, nil, nil
		default:
			return nil, nil, fmt.Errorf("unexpected index %d", i)
		}
	}

	src := newSliceCandidateSource([]models.NZBResult{{Title: "better-slow"}, {Title: "worse-fast"}})
	start := time.Now()
	winner, usedFallback, err := racePrequeueResolutions(context.Background(), src, 4, process, nil, 50*time.Millisecond, false)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("race returned error: %v", err)
	}
	if usedFallback {
		t.Fatal("winner adopted as fallback")
	}
	if winner == nil || winner.index != 0 {
		t.Fatalf("winner index = %v, want 0 after endRaceEarly=false drains the batch", winnerIndex(winner))
	}
	if elapsed < 150*time.Millisecond {
		t.Fatalf("race returned in %v, want it to ignore the bounded settle window and drain the batch", elapsed)
	}
}

func TestRacePrequeueResolutionsReportsInFlightWindow(t *testing.T) {
	// During the race the reporter must publish the in-flight candidate window
	// (0-based indices, -1/-1 when idle) as workers pick candidates up and
	// finish them — what the frontend needs to say "trying streams 1–4"
	// instead of a single misleading "stream 4".
	var mu sync.Mutex
	var windows [][2]int
	report := func(min, max int) {
		mu.Lock()
		windows = append(windows, [2]int{min, max})
		mu.Unlock()
	}

	// Candidate 0 is slow and dead; candidate 1 only proceeds once candidate 0
	// has been PICKED (its process has started, which happens only after the
	// dispatch(0) handshake reached the owner). With a concurrent pick-up order
	// that is otherwise nondeterministic, this pins the observed in-flight
	// window sequence to [0 0] then [0 1] before the winner is adopted.
	picked0 := make(chan struct{})
	process := func(ctx context.Context, i int, _ models.NZBResult) (*candidateResolution, *candidateResolution, error) {
		switch i {
		case 0:
			// Slow dead top candidate: holds a slot until cancelled.
			close(picked0)
			_, err := slowFailingResolve(ctx, models.NZBResult{Title: "slow-dead"})
			return nil, nil, err
		case 1:
			select {
			case <-picked0:
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
			// Give the race owner a beat to observe candidate 0's dispatch, so the
			// in-flight window deterministically covers both candidates before
			// this (winning) result is published. Candidate 0 stays in flight for
			// 3s, so activeSet is {0,1} the whole time.
			time.Sleep(30 * time.Millisecond)
			return &candidateResolution{index: 1, result: models.NZBResult{Title: "fast-good"}, resolution: &models.PlaybackResolution{WebDAVPath: "/webdav/fast.mkv"}}, nil, nil
		default:
			return nil, nil, fmt.Errorf("unexpected index %d", i)
		}
	}

	src := newSliceCandidateSource([]models.NZBResult{
		{Title: "slow-dead"}, {Title: "fast-good"}, {Title: "c3"}, {Title: "c4"},
	})
	winner, _, err := racePrequeueResolutions(context.Background(), src, 2, process, report, 100*time.Millisecond, true)
	if err != nil {
		t.Fatalf("race returned error: %v", err)
	}
	if winner == nil || winner.index != 1 {
		t.Fatalf("winner index = %d, want 1", winnerIndex(winner))
	}

	mu.Lock()
	defer mu.Unlock()
	if len(windows) == 0 {
		t.Fatal("no windows reported")
	}
	// The first observed dispatch opens a single-slot window (candidate 0 or 1
	// — concurrent pick-up makes one of the two the first to report).
	if windows[0] != [2]int{0, 0} && windows[0] != [2]int{1, 1} {
		t.Fatalf("first window = %v, want a single-slot window", windows[0])
	}
	// Candidate 1 is gated on candidate 0 having been picked, so both are in
	// flight together before the winner is adopted: the window must cover [0 1].
	// (-1 -1 is the idle window and is fine.)
	grew := false
	for _, w := range windows {
		if w == [2]int{0, 1} {
			grew = true
		}
		if w[0] != -1 && (w[0] > w[1] || w[0] < 0) {
			t.Fatalf("invalid window %v (min > max)", w)
		}
	}
	if !grew {
		t.Fatalf("window never grew past the first candidate: %v", windows)
	}
}

func TestRacePrequeueResolutionsFallsBackToDeprioritizedWhenNothingValidates(t *testing.T) {
	process := func(ctx context.Context, i int, _ models.NZBResult) (*candidateResolution, *candidateResolution, error) {
		if i == 0 {
			return nil, &candidateResolution{
				index:      0,
				result:     models.NZBResult{Title: "deprioritized"},
				resolution: &models.PlaybackResolution{WebDAVPath: "/webdav/fallback.mkv"},
			}, nil
		}
		return nil, nil, fmt.Errorf("candidate %d failed", i)
	}

	src := newSliceCandidateSource([]models.NZBResult{
		{Title: "deprioritized"}, {Title: "c1"}, {Title: "c2"},
	})
	winner, usedFallback, err := racePrequeueResolutions(context.Background(), src, 2, process, nil, 0, false)
	if err != nil {
		t.Fatalf("race returned error: %v", err)
	}
	if !usedFallback {
		t.Fatal("expected the deprioritized candidate to be used as a fallback")
	}
	if winner == nil || winner.index != 0 {
		t.Fatalf("winner index = %d, want 0", winnerIndex(winner))
	}
}

func TestRacePrequeueResolutionsPrefersAcceptedOverEarlierDeprioritized(t *testing.T) {
	process := func(ctx context.Context, i int, _ models.NZBResult) (*candidateResolution, *candidateResolution, error) {
		if i == 0 {
			return nil, &candidateResolution{
				index:      0,
				result:     models.NZBResult{Title: "deprioritized"},
				resolution: &models.PlaybackResolution{WebDAVPath: "/webdav/fallback.mkv"},
			}, nil
		}
		if i == 1 {
			time.Sleep(20 * time.Millisecond)
			return &candidateResolution{
				index:      1,
				result:     models.NZBResult{Title: "accepted"},
				resolution: &models.PlaybackResolution{WebDAVPath: "/webdav/accepted.mkv"},
			}, nil, nil
		}
		return nil, nil, fmt.Errorf("candidate %d failed", i)
	}

	src := newSliceCandidateSource([]models.NZBResult{
		{Title: "deprioritized"}, {Title: "accepted"}, {Title: "c2"},
	})
	winner, usedFallback, err := racePrequeueResolutions(context.Background(), src, 2, process, nil, 0, false)
	if err != nil {
		t.Fatalf("race returned error: %v", err)
	}
	if usedFallback {
		t.Fatal("deprioritized candidate must not win when a fully validated candidate exists")
	}
	if winner == nil || winner.index != 1 {
		t.Fatalf("winner index = %d, want 1 (accepted beats earlier deprioritized)", winnerIndex(winner))
	}
}

func TestRacePrequeueResolutionsAllFailSurfacesFirstError(t *testing.T) {
	process := func(ctx context.Context, i int, _ models.NZBResult) (*candidateResolution, *candidateResolution, error) {
		return nil, nil, fmt.Errorf("candidate %d failed", i)
	}

	src := newSliceCandidateSource([]models.NZBResult{{Title: "c0"}, {Title: "c1"}})
	winner, usedFallback, err := racePrequeueResolutions(context.Background(), src, 2, process, nil, 0, false)
	if winner != nil || usedFallback || err == nil {
		t.Fatalf("all-fail race returned winner=%v usedFallback=%v err=%v, want all failures surfaced", winner != nil, usedFallback, err)
	}
}

func TestRacePrequeueResolutionsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	process := func(ctx context.Context, i int, _ models.NZBResult) (*candidateResolution, *candidateResolution, error) {
		return nil, nil, fmt.Errorf("must not be invoked on cancelled context")
	}

	src := newSliceCandidateSource([]models.NZBResult{{Title: "c0"}, {Title: "c1"}})
	winner, usedFallback, err := racePrequeueResolutions(ctx, src, 2, process, nil, 0, false)
	if winner != nil || usedFallback {
		t.Fatalf("cancelled race returned winner=%v usedFallback=%v", winner != nil, usedFallback)
	}
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled race returned err=%v, want context.Canceled", err)
	}
}

func TestRacePrequeueResolutionsDetachesLoserThatIgnoresCancellation(t *testing.T) {
	loserStarted := make(chan struct{})
	releaseLoser := make(chan struct{})
	defer close(releaseLoser)

	process := func(ctx context.Context, i int, _ models.NZBResult) (*candidateResolution, *candidateResolution, error) {
		if i == 1 {
			close(loserStarted)
			<-releaseLoser // Deliberately ignore ctx, matching a wedged provider read.
			return nil, nil, ctx.Err()
		}
		<-loserStarted
		return &candidateResolution{
			index:      0,
			result:     models.NZBResult{Title: "winner"},
			resolution: &models.PlaybackResolution{WebDAVPath: "/webdav/winner.mkv"},
		}, nil, nil
	}

	startedAt := time.Now()
	winner, usedFallback, err := racePrequeueResolutionsWithJoinTimeout(
		context.Background(),
		newSliceCandidateSource([]models.NZBResult{{Title: "winner"}, {Title: "stuck-loser"}}),
		2,
		process,
		nil,
		0,
		true,
		25*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("race returned error: %v", err)
	}
	if usedFallback || winner == nil || winner.index != 0 {
		t.Fatalf("race returned winner=%d usedFallback=%t, want winner 0", winnerIndex(winner), usedFallback)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("race waited %s for a loser that ignored cancellation", elapsed)
	}
}

func TestResolveCandidatesSequentialUsesRankedOrder(t *testing.T) {
	var mu sync.Mutex
	active := 0
	maxActive := 0
	var attempted []string
	playbackSvc := &stubPlaybackService{
		resolve: func(_ context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error) {
			mu.Lock()
			active++
			if active > maxActive {
				maxActive = active
			}
			attempted = append(attempted, candidate.Title)
			mu.Unlock()
			defer func() {
				mu.Lock()
				active--
				mu.Unlock()
			}()

			if candidate.Title == "rank-0-fails" {
				time.Sleep(60 * time.Millisecond)
				return nil, errors.New("unavailable")
			}
			return &models.PlaybackResolution{WebDAVPath: "/webdav/selected.mkv", HealthStatus: "healthy"}, nil
		},
	}
	handler := &PrequeueHandler{
		store:       playback.NewPrequeueStore(time.Minute),
		playbackSvc: playbackSvc,
		fullProber:  &raceProbeResult{},
	}

	choice, err := handler.resolveCandidates(context.Background(), "prequeue-sequential", newSliceCandidateSource([]models.NZBResult{
		{Title: "rank-0-fails", ServiceType: models.ServiceTypeUsenet},
		{Title: "rank-1-wins", ServiceType: models.ServiceTypeDebrid},
		{Title: "rank-2-must-not-run", ServiceType: models.ServiceTypeUsenet},
	}), prequeueResolutionOptions{
		mediaType:          "movie",
		hdrDVPolicy:        models.HDRDVPolicyIncludeHDRDV,
		unknownTrackPolicy: "none",
		concurrent:         false,
	})

	if err != nil {
		t.Fatalf("resolveCandidates returned error: %v", err)
	}
	if choice.selectedResultIndex != 1 {
		t.Fatalf("selectedResultIndex = %d, want 1", choice.selectedResultIndex)
	}
	mu.Lock()
	defer mu.Unlock()
	if maxActive != 1 {
		t.Fatalf("maximum concurrent resolutions = %d, want 1", maxActive)
	}
	wantAttempts := []string{"rank-0-fails", "rank-1-wins"}
	if !reflect.DeepEqual(attempted, wantAttempts) {
		t.Fatalf("attempted candidates = %v, want %v", attempted, wantAttempts)
	}
}

// TestResolveCandidatesAdoptsFastHealthyCandidate is the handler-level
// verification for the concurrent resolution race: the wired resolution phase
// (preflight gate, resolve, probe, policy checks) races candidates and adopts
// the fast healthy second candidate even though the top-ranked candidate fails
// slowly on the wire.
func TestResolveCandidatesAdoptsFastHealthyCandidate(t *testing.T) {
	playbackSvc := &stubPlaybackService{
		resolve: func(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error) {
			if candidate.Title == "slow-dead" {
				return slowFailingResolve(ctx, candidate)
			}
			return &models.PlaybackResolution{WebDAVPath: "/webdav/fast.mkv", HealthStatus: "healthy"}, nil
		},
	}
	handler := &PrequeueHandler{
		store:       playback.NewPrequeueStore(time.Minute),
		playbackSvc: playbackSvc,
		fullProber:  &raceProbeResult{},
		// Configure a BOUNDED settle window (a positive resolutionSettleWindowMs)
		// so the fast healthy candidate is adopted once the window elapses instead
		// of strict order waiting for the slow top candidate to fail. This pins
		// the bounded fast-path compromise; strict order (the default) is covered
		// by TestRacePrequeueResolutionsStrictOrderWaitsForSlowDeadTopToFail.
		configManager: testResolutionConfigManager(t, config.Settings{Streaming: config.StreamingSettings{
			ResolutionSettleWindowMs: 200,
			ResolutionEndRaceEarly:   true,
		}}),
	}

	allResults := []models.NZBResult{
		{Title: "slow-dead", ServiceType: models.ServiceTypeUsenet},
		{Title: "fast-good", ServiceType: models.ServiceTypeUsenet},
	}

	start := time.Now()
	choice, err := handler.resolveCandidates(context.Background(), "prequeue-test", newSliceCandidateSource(allResults), prequeueResolutionOptions{
		mediaType:          "movie",
		hdrDVPolicy:        models.HDRDVPolicyIncludeHDRDV,
		unknownTrackPolicy: "none",
		concurrent:         true,
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("resolveCandidates returned error: %v", err)
	}
	if choice.resolution == nil {
		t.Fatal("resolveCandidates returned no resolution")
	}
	if choice.selectedResultIndex != 1 {
		t.Fatalf("selectedResultIndex = %d, want 1 (fast second candidate)", choice.selectedResultIndex)
	}
	if choice.resolution.WebDAVPath != "/webdav/fast.mkv" {
		t.Fatalf("WebDAVPath = %q, want the fast candidate's path", choice.resolution.WebDAVPath)
	}
	// The dead top candidate is cancelled once the healthy candidate wins, so
	// total elapsed must be far below its 3s serial stall.
	if elapsed >= time.Second {
		t.Fatalf("resolution phase took %v; the slow top candidate was not raced concurrently (serial sum is >3s)", elapsed)
	}
}

// TestResolveCandidatesProbeRejectionMarksBadStream covers the bad-stream
// marking of a candidate rejected by the cheap availability probe (surfaced
// as playback.ErrUsenetProbeRejected, which wraps importer.ErrArticleUnavailable):
// it must be marked bad immediately and the race must adopt the next healthy
// candidate — with the latency tracker recording the probe_rejected attempt.
func TestResolveCandidatesProbeRejectionMarksBadStream(t *testing.T) {
	badStreamsSvc := badstreams.New(filepath.Join(t.TempDir(), "bad_streams.json"))
	playbackSvc := &stubPlaybackService{
		resolve: func(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error) {
			if strings.Contains(candidate.Title, "DEAD") {
				return nil, fmt.Errorf("usenet availability probe rejected: %w", playback.ErrUsenetProbeRejected)
			}
			// The healthy candidate takes a touch longer so the dead one's
			// rejection deterministically reports before adoption.
			time.Sleep(30 * time.Millisecond)
			return &models.PlaybackResolution{WebDAVPath: "/webdav/good.mkv", HealthStatus: "healthy"}, nil
		},
	}
	tracker := NewPlaybackLatencyTracker(16)
	handler := &PrequeueHandler{
		store:          playback.NewPrequeueStore(time.Minute),
		playbackSvc:    playbackSvc,
		fullProber:     &raceProbeResult{},
		badStreamsSvc:  badStreamsSvc,
		latencyTracker: tracker,
	}

	allResults := []models.NZBResult{
		{Title: "Dead.Release.DEAD.2020.1080p", ServiceType: models.ServiceTypeUsenet},
		{Title: "Good.Release.2020.1080p", ServiceType: models.ServiceTypeUsenet},
	}

	choice, err := handler.resolveCandidates(context.Background(), "prequeue-probe-test", newSliceCandidateSource(allResults), prequeueResolutionOptions{
		mediaType:          "movie",
		hdrDVPolicy:        models.HDRDVPolicyIncludeHDRDV,
		unknownTrackPolicy: "none",
		concurrent:         true,
	})
	if err != nil {
		t.Fatalf("resolveCandidates returned error: %v", err)
	}
	if choice.selectedResultIndex != 1 {
		t.Fatalf("selectedResultIndex = %d, want 1 (healthy second candidate)", choice.selectedResultIndex)
	}

	entries := badStreamsSvc.List()
	if len(entries) != 1 {
		t.Fatalf("bad-stream entries = %d, want exactly the probe-rejected release", len(entries))
	}
	if !strings.Contains(entries[0].ReleaseName, "DEAD") {
		t.Fatalf("bad-stream release = %q, want the dead release", entries[0].ReleaseName)
	}
	if entries[0].Reason != "prequeue:usenet-articles-unavailable" {
		t.Fatalf("bad-stream reason = %q, want prequeue:usenet-articles-unavailable", entries[0].Reason)
	}

	// The latency tracker must carry the per-candidate attempts:
	// probe_rejected for the dead release, adopted for the winner, rejection
	// quicker than adoption.
	tracker.NotePrequeueOnlySample("prequeue-probe-test")
	samples := tracker.Latest(1)
	if len(samples) != 1 {
		t.Fatalf("tracker samples = %d, want 1", len(samples))
	}
	cands := samples[0].Candidates
	if len(cands) != 2 {
		t.Fatalf("candidates = %d, want 2", len(cands))
	}
	if cands[0].Outcome != PrequeueCandidateProbeRejected {
		t.Fatalf("candidate[0].Outcome = %q, want probe_rejected", cands[0].Outcome)
	}
	if cands[1].Outcome != PrequeueCandidateAdopted {
		t.Fatalf("candidate[1].Outcome = %q, want adopted", cands[1].Outcome)
	}
	if cands[0].DurationMs > cands[1].DurationMs {
		t.Fatalf("probe rejection (%dms) must be quicker than the adopted resolve (%dms)", cands[0].DurationMs, cands[1].DurationMs)
	}
}

// TestResolveCandidatesStreamsUsenetBeforeDebrid is the handler-level
// verification for streaming candidate sources: the resolution phase must
// start (and complete) on the first-ready source's candidates while the slower
// source has not even been published yet — the prequeue no longer waits for
// debrid scrapers before resolving usenet.
func TestResolveCandidatesStreamsUsenetBeforeDebrid(t *testing.T) {
	resolved := make(chan string, 4)
	playbackSvc := &stubPlaybackService{
		resolve: func(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error) {
			resolved <- candidate.Title
			if candidate.ServiceType == models.ServiceTypeUsenet {
				// Deliberately slow-ish usenet resolution: it must still complete
				// while debrid is absent from the stream.
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(150 * time.Millisecond):
				}
				return &models.PlaybackResolution{WebDAVPath: "/webdav/usenet.mkv", HealthStatus: "healthy"}, nil
			}
			return &models.PlaybackResolution{WebDAVPath: "/webdav/debrid.mkv", HealthStatus: "healthy"}, nil
		},
	}
	handler := &PrequeueHandler{
		store:       playback.NewPrequeueStore(time.Minute),
		playbackSvc: playbackSvc,
		fullProber:  &raceProbeResult{},
		// This test pins the cross-source end-early behavior (usenet adopts
		// while debrid is still scraping), which requires endRaceEarly=true.
		configManager: testResolutionConfigManager(t, config.Settings{Streaming: config.StreamingSettings{
			ResolutionSettleWindowMs: 0,
			ResolutionEndRaceEarly:   true,
		}}),
	}

	src := newStreamCandidateSource()
	resolveDone := make(chan prequeueResolutionChoice, 1)
	resolveErrCh := make(chan error, 1)
	go func() {
		choice, err := handler.resolveCandidates(context.Background(), "prequeue-stream", src, prequeueResolutionOptions{
			mediaType:          "movie",
			hdrDVPolicy:        models.HDRDVPolicyIncludeHDRDV,
			unknownTrackPolicy: "none",
			concurrent:         true,
		})
		if err != nil {
			resolveErrCh <- err
			return
		}
		resolveDone <- choice
	}()

	// Publish the usenet candidate first (the fast search source); debrid is
	// deliberately not fed yet, simulating a slow scraper still in flight.
	if !src.Feed(models.NZBResult{Title: "usenet-fast", ServiceType: models.ServiceTypeUsenet}) {
		t.Fatal("stream rejected the usenet candidate")
	}

	// Usenet resolution must START before debrid is available.
	select {
	case title := <-resolved:
		if title != "usenet-fast" {
			t.Fatalf("first resolved title = %q, want usenet-fast", title)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("usenet resolution never started while debrid was still absent")
	}

	// ...and it must COMPLETE (adopt a winner) before debrid is even published.
	select {
	case choice := <-resolveDone:
		if choice.resolution == nil || choice.resolution.WebDAVPath != "/webdav/usenet.mkv" {
			t.Fatalf("winner resolution = %+v, want the usenet path", choice.resolution)
		}
		if choice.selectedResult == nil || choice.selectedResult.ServiceType != models.ServiceTypeUsenet {
			t.Fatalf("winner candidate = %+v, want the usenet candidate", choice.selectedResult)
		}
	case err := <-resolveErrCh:
		t.Fatalf("resolveCandidates errored: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("resolution did not complete before debrid was fed")
	}

	// The race already adopted a usenet winner, so the stream is stopped and a
	// late debrid candidate must be rejected — debrid is never resolved after
	// usenet.
	src.Stop()
	if src.Feed(models.NZBResult{Title: "debrid-late", ServiceType: models.ServiceTypeDebrid}) {
		t.Fatal("stream accepted a candidate after the race concluded; debrid would be resolved after the usenet winner")
	}
	select {
	case title := <-resolved:
		t.Fatalf("debrid resolution started after the usenet winner (%q)", title)
	case <-time.After(50 * time.Millisecond):
		// nothing else was ever resolved — usenet won before debrid existed
	}
}

func TestStreamCandidateSourceSnapshotIncludesDeliveredCandidate(t *testing.T) {
	src := newStreamCandidateSource()
	want := models.NZBResult{Title: "cached-winner", ServiceType: models.ServiceTypeUsenet}
	fed := make(chan bool, 1)
	go func() { fed <- src.Feed(want) }()

	_, got, ok := src.Next(context.Background())
	if !ok || got.Title != want.Title {
		t.Fatalf("Next() = (%q, %t), want delivered candidate", got.Title, ok)
	}
	if !<-fed {
		t.Fatal("Feed unexpectedly failed")
	}
	snapshot := src.Snapshot()
	if len(snapshot) != 1 || snapshot[0].Title != want.Title {
		t.Fatalf("Snapshot() = %#v, want the delivered winning candidate", snapshot)
	}
	src.Stop()
}

func TestPrequeueSearchFeederReservesCapacityForSlowerSource(t *testing.T) {
	src := newStreamCandidateSource()
	state := newPrequeueFeedState()
	usenetCh := make(chan indexer.ScoredSplitSearchResult, 1)
	debridCh := make(chan indexer.ScoredSplitSearchResult, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go (&PrequeueHandler{store: playback.NewPrequeueStore(time.Minute)}).prequeueSearchFeeder(ctx, src, usenetCh, debridCh, state, prequeueFeederConfig{
		maxCandidates: 4,
	})

	debridCh <- splitSearchResult("debrid", models.ServiceTypeDebrid, 4)
	close(debridCh)

	// The fast debrid source may consume only its two reserved slots while the
	// usenet source is still pending.
	for i := 0; i < 2; i++ {
		_, candidate, ok := nextStreamCandidate(t, src)
		if !ok || candidate.ServiceType != models.ServiceTypeDebrid {
			t.Fatalf("candidate %d = (%q, %t), want debrid", i, candidate.ServiceType, ok)
		}
	}
	pending := make(chan models.NZBResult, 1)
	go func() {
		_, candidate, ok := src.Next(ctx)
		if ok {
			pending <- candidate
		}
	}()
	select {
	case candidate := <-pending:
		t.Fatalf("fast source exceeded its reservation with candidate %q", candidate.Title)
	case <-time.After(50 * time.Millisecond):
	}

	usenetCh <- splitSearchResult("usenet", models.ServiceTypeUsenet, 4)
	close(usenetCh)
	select {
	case candidate := <-pending:
		if candidate.ServiceType != models.ServiceTypeUsenet {
			t.Fatalf("third candidate service = %q, want usenet", candidate.ServiceType)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("slower usenet source was not admitted")
	}
	_, fourth, ok := nextStreamCandidate(t, src)
	if !ok || fourth.ServiceType != models.ServiceTypeUsenet {
		t.Fatalf("fourth candidate = (%q, %t), want usenet", fourth.ServiceType, ok)
	}
	if _, _, ok := nextStreamCandidate(t, src); ok {
		t.Fatal("stream exceeded the four-candidate cap")
	}
	<-state.done

	snapshot := src.Snapshot()
	if len(snapshot) != 4 {
		t.Fatalf("snapshot candidate count = %d, want 4", len(snapshot))
	}
}

func TestPrequeueSearchFeederInterruptsBlockedHandoffForNewSource(t *testing.T) {
	src := newStreamCandidateSource()
	state := newPrequeueFeedState()
	usenetCh := make(chan indexer.ScoredSplitSearchResult, 1)
	debridCh := make(chan indexer.ScoredSplitSearchResult, 1)
	ctx, cancel := context.WithCancel(context.Background())

	go (&PrequeueHandler{store: playback.NewPrequeueStore(time.Minute)}).prequeueSearchFeeder(ctx, src, usenetCh, debridCh, state, prequeueFeederConfig{
		maxCandidates: 4,
	})
	debridCh <- splitSearchResult("debrid", models.ServiceTypeDebrid, 4)
	close(debridCh)

	// Do not consume the stream yet: the first debrid handoff should block.
	waitForStreamSnapshot(t, src, func(snapshot []models.NZBResult) bool {
		return len(snapshot) == 1 && snapshot[0].ServiceType == models.ServiceTypeDebrid
	})

	// A newly completed Usenet source must interrupt and roll back that blocked
	// debrid handoff rather than waiting for a debrid worker slot to free.
	usenetCh <- splitSearchResult("usenet", models.ServiceTypeUsenet, 1)
	close(usenetCh)
	waitForStreamSnapshot(t, src, func(snapshot []models.NZBResult) bool {
		return len(snapshot) == 1 && snapshot[0].ServiceType == models.ServiceTypeUsenet
	})
	_, candidate, ok := nextStreamCandidate(t, src)
	if !ok || candidate.ServiceType != models.ServiceTypeUsenet {
		t.Fatalf("first delivered candidate = (%q, %t), want newly ready usenet", candidate.ServiceType, ok)
	}

	cancel()
	src.Stop()
	select {
	case <-state.done:
	case <-time.After(3 * time.Second):
		t.Fatal("feeder did not stop after cancellation")
	}
}

func TestPrequeueSearchFeederReleasesUnusedReservation(t *testing.T) {
	src := newStreamCandidateSource()
	state := newPrequeueFeedState()
	usenetCh := make(chan indexer.ScoredSplitSearchResult, 1)
	debridCh := make(chan indexer.ScoredSplitSearchResult, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go (&PrequeueHandler{store: playback.NewPrequeueStore(time.Minute)}).prequeueSearchFeeder(ctx, src, usenetCh, debridCh, state, prequeueFeederConfig{
		maxCandidates: 4,
	})
	debridCh <- splitSearchResult("debrid", models.ServiceTypeDebrid, 4)
	close(debridCh)
	usenetCh <- indexer.ScoredSplitSearchResult{Source: "usenet", Disabled: true}
	close(usenetCh)

	for i := 0; i < 4; i++ {
		_, candidate, ok := nextStreamCandidate(t, src)
		if !ok || candidate.ServiceType != models.ServiceTypeDebrid {
			t.Fatalf("candidate %d = (%q, %t), want debrid", i, candidate.ServiceType, ok)
		}
	}
	if _, _, ok := nextStreamCandidate(t, src); ok {
		t.Fatal("stream exceeded the four-candidate cap")
	}
	<-state.done
}

func splitSearchResult(source string, serviceType models.ContentServiceType, count int) indexer.ScoredSplitSearchResult {
	result := indexer.ScoredSplitSearchResult{Source: source}
	for i := 0; i < count; i++ {
		result.Scored = append(result.Scored, models.ScoredNZBResult{
			NZBResult: models.NZBResult{
				Title:       fmt.Sprintf("%s-%d", source, i),
				ServiceType: serviceType,
			},
		})
	}
	return result
}

func nextStreamCandidate(t *testing.T, src *streamCandidateSource) (int, models.NZBResult, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return src.Next(ctx)
}

func waitForStreamSnapshot(t *testing.T, src *streamCandidateSource, ready func([]models.NZBResult) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ready(src.Snapshot()) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("stream snapshot did not reach expected state; final=%#v", src.Snapshot())
}

// TestStreamCandidateSourceStopUnblocksBlockedFeed guards the worker's winner
// path: once the race adopts a candidate it stops consuming, so a feeder that is
// mid-Feed (blocked publishing a just-arrived candidate) is unblocked by Stop
// rather than deadlocking the worker's join.
func TestStreamCandidateSourceStopUnblocksBlockedFeed(t *testing.T) {
	src := newStreamCandidateSource()
	feedDone := make(chan bool, 1)
	go func() {
		feedDone <- src.Feed(models.NZBResult{Title: "c1"}) // blocks: no consumer yet
	}()

	// Give the Feed a moment to actually block on the channel send, then stop.
	time.Sleep(20 * time.Millisecond)
	src.Stop()

	select {
	case ok := <-feedDone:
		if ok {
			t.Fatal("Feed reported success after the source was stopped")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Feed was not unblocked by Stop (deadlock)")
	}

	if src.Feed(models.NZBResult{Title: "c2"}) {
		t.Fatal("Feed after Stop must be refused")
	}
	if got := src.Total(); got != 0 {
		t.Fatalf("Total = %d, want 0 — no candidate was ever consumed by a race", got)
	}
}

// TestStreamCandidateSourceCloseAndStopIdempotent guards against the
// benchmark crash ("close of closed channel"): on the exhaustion path the feeder
// Close()s the source and then the worker Stop()s it — both teardown paths must
// be idempotent in either order.
func TestStreamCandidateSourceCloseAndStopIdempotent(t *testing.T) {
	// Exhaustion order: feeder Close first, worker Stop after.
	src := newStreamCandidateSource()
	src.Close()
	src.Stop() // was panicking: double-close of s.done
	src.Close()
	src.Stop()
	if src.Feed(models.NZBResult{Title: "c"}) {
		t.Fatal("Feed after Close must be refused")
	}

	// Winner order: worker Stop first, feeder Close after.
	src2 := newStreamCandidateSource()
	src2.Stop()
	src2.Close()
	src2.Stop()
	src2.Close()
	if src2.Feed(models.NZBResult{Title: "c"}) {
		t.Fatal("Feed after Stop must be refused")
	}
}

// mockMovieDetailsProvider implements MovieDetailsProvider for testing
type mockMovieDetailsProvider struct {
	title *models.Title
	err   error
}

func (m *mockMovieDetailsProvider) MovieInfo(_ context.Context, _ models.MovieDetailsQuery) (*models.Title, error) {
	return m.title, m.err
}

type mockSeriesDetailsProvider struct {
	details   *models.SeriesDetails
	err       error
	lastQuery models.SeriesDetailsQuery
}

func (m *mockSeriesDetailsProvider) SeriesDetails(_ context.Context, query models.SeriesDetailsQuery) (*models.SeriesDetails, error) {
	m.lastQuery = query
	return m.details, m.err
}

func TestCreateEpisodeResolverPropagatesResolvedIMDBID(t *testing.T) {
	provider := &mockSeriesDetailsProvider{details: &models.SeriesDetails{
		Title: models.Title{Name: "Captain Star", Year: 1997, IMDBID: "tt0143031"},
	}}
	handler := &PrequeueHandler{metadataSvc: provider}

	got := handler.createEpisodeResolverAndLookupAbsoluteEp(
		context.Background(), "tmdb:series:196", "Captain Star", 1997, "", nil,
	)
	if got.IMDBID != "tt0143031" {
		t.Fatalf("resolved imdb id = %q, want tt0143031", got.IMDBID)
	}
	if provider.lastQuery.IMDBID != "" {
		t.Fatalf("metadata query imdb id = %q, want empty input", provider.lastQuery.IMDBID)
	}
}

func TestUnknownTrackPolicyRejects(t *testing.T) {
	tests := []struct {
		name      string
		policy    string
		audio     []AudioStreamInfo
		subtitles []SubtitleStreamInfo
		want      bool
	}{
		{
			name:   "off allows unknown tracks",
			policy: "none",
			audio:  []AudioStreamInfo{{Index: 1}},
			want:   false,
		},
		{
			name:   "audio rejects all unknown audio tracks",
			policy: "audio",
			audio:  []AudioStreamInfo{{Index: 1}, {Index: 2}},
			want:   true,
		},
		{
			name:   "audio allows any known audio track",
			policy: "audio",
			audio:  []AudioStreamInfo{{Index: 1}, {Index: 2, Language: "eng"}},
			want:   false,
		},
		{
			name:      "subtitles rejects all unknown subtitle tracks",
			policy:    "subtitles",
			subtitles: []SubtitleStreamInfo{{Index: 3}},
			want:      true,
		},
		{
			name:      "subtitles allows known subtitle title",
			policy:    "subtitles",
			subtitles: []SubtitleStreamInfo{{Index: 3, Title: "English"}},
			want:      false,
		},
		{
			name:      "both rejects unknown subtitle when audio is known",
			policy:    "both",
			audio:     []AudioStreamInfo{{Index: 1, Language: "eng"}},
			subtitles: []SubtitleStreamInfo{{Index: 3}},
			want:      true,
		},
		{
			name:      "both allows known audio and subtitles",
			policy:    "both",
			audio:     []AudioStreamInfo{{Index: 1, Language: "eng"}},
			subtitles: []SubtitleStreamInfo{{Index: 3, Language: "eng"}},
			want:      false,
		},
		{
			name:   "no tracks are not treated as unknown tracks",
			policy: "both",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := unknownTrackPolicyRejects(tt.policy, tt.audio, tt.subtitles)
			if got != tt.want {
				t.Fatalf("unknownTrackPolicyRejects() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFindAllowedAudioTrackFallsBackWithinAllowlist(t *testing.T) {
	streams := []AudioStreamInfo{{Index: 1, Language: "rus"}, {Index: 2, Language: "fra"}}
	if got := findAllowedAudioTrack(streams, []string{"fra"}, "eng"); got != 2 {
		t.Fatalf("findAllowedAudioTrack() = %d, want allowed French track 2", got)
	}
}

func TestCanonicalPrequeueTitleID(t *testing.T) {
	tests := []struct {
		name      string
		mediaType string
		titleID   string
		imdbID    string
		tmdbID    string
		tvdbID    string
		want      string
	}{
		{
			name:      "series bare tmdb number canonicalizes via tmdbId",
			mediaType: "series",
			titleID:   "2288",
			tmdbID:    "2288",
			want:      "tmdb:tv:2288",
		},
		{
			name:      "series already canonical tmdb form unchanged",
			mediaType: "series",
			titleID:   "tmdb:tv:2288",
			tmdbID:    "2288",
			want:      "tmdb:tv:2288",
		},
		{
			name:      "series tvdb-form id converges to tmdb when tmdbId present",
			mediaType: "series",
			titleID:   "tvdb:series:393199",
			tmdbID:    "2288",
			tvdbID:    "393199",
			want:      "tmdb:tv:2288",
		},
		{
			name:      "series imdb-only canonicalizes to imdb form",
			mediaType: "series",
			titleID:   "From",
			imdbID:    "tt9813792",
			want:      "imdb:tt9813792",
		},
		{
			name:      "tv mediatype alias treated as series",
			mediaType: "tv",
			titleID:   "2288",
			tmdbID:    "2288",
			want:      "tmdb:tv:2288",
		},
		{
			name:      "no identifiers normalizes case (still consistent across shelves)",
			mediaType: "series",
			titleID:   "From",
			want:      "from",
		},
		{
			name:      "movie bare tmdb number canonicalizes via tmdbId",
			mediaType: "movie",
			titleID:   "603",
			tmdbID:    "603",
			want:      "tmdb:movie:603",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := canonicalPrequeueTitleID(tt.mediaType, tt.titleID, tt.imdbID, tt.tmdbID, tt.tvdbID)
			if got != tt.want {
				t.Errorf("canonicalPrequeueTitleID(%q, %q, imdb=%q, tmdb=%q, tvdb=%q) = %q, want %q",
					tt.mediaType, tt.titleID, tt.imdbID, tt.tmdbID, tt.tvdbID, got, tt.want)
			}
		})
	}

	// The core guarantee: the same show opened from two shelves with different
	// ID forms but the same tmdbId resolves to one prequeue store key.
	fromContinueWatching := canonicalPrequeueTitleID("series", "tvdb:series:393199", "tt9813792", "2288", "393199")
	fromTopTen := canonicalPrequeueTitleID("series", "2288", "", "2288", "")
	if fromContinueWatching != fromTopTen {
		t.Errorf("expected shelves to converge: continueWatching=%q topTen=%q", fromContinueWatching, fromTopTen)
	}
}

func TestManualPrequeueStatusAndRemovalUseCanonicalProfileIdentity(t *testing.T) {
	h := NewPrequeueHandler(nil, nil, nil, nil, nil, false)
	entry, _ := h.GetStore().Create(
		"tmdb:movie:42",
		"Permanent Movie",
		"profile-1",
		"movie",
		2026,
		nil,
		playback.ManualPrequeueReason,
	)

	statusURL := "/api/playback/prequeue/manual?userId=profile-1&titleId=raw-id&mediaType=movie&tmdbId=42"
	statusReq := httptest.NewRequest(http.MethodGet, statusURL, nil)
	statusRec := httptest.NewRecorder()
	h.ManualPrequeueStatus(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200: %s", statusRec.Code, statusRec.Body.String())
	}
	var status manualPrequeueStatusResponse
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !status.Prequeued || status.PrequeueID != entry.ID {
		t.Fatalf("unexpected status: %+v", status)
	}

	otherReq := httptest.NewRequest(http.MethodGet, strings.Replace(statusURL, "profile-1", "profile-2", 1), nil)
	otherRec := httptest.NewRecorder()
	h.ManualPrequeueStatus(otherRec, otherReq)
	if otherRec.Code != http.StatusOK {
		t.Fatalf("other profile status code = %d, want 200", otherRec.Code)
	}
	var otherStatus manualPrequeueStatusResponse
	if err := json.Unmarshal(otherRec.Body.Bytes(), &otherStatus); err != nil {
		t.Fatalf("decode other profile status: %v", err)
	}
	if otherStatus.Prequeued {
		t.Fatal("manual prequeue leaked across profiles")
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, statusURL, nil)
	deleteRec := httptest.NewRecorder()
	h.RemoveManualPrequeue(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status code = %d, want 204: %s", deleteRec.Code, deleteRec.Body.String())
	}
	if _, exists := h.GetStore().Get(entry.ID); exists {
		t.Fatal("manual prequeue still exists after removal")
	}
}

func TestPrequeueEpisodeMatches(t *testing.T) {
	tests := []struct {
		name      string
		requested *models.EpisodeReference
		existing  *models.EpisodeReference
		want      bool
	}{
		{
			name:      "both nil",
			requested: nil,
			existing:  nil,
			want:      true,
		},
		{
			name:      "one nil",
			requested: &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 1},
			existing:  nil,
			want:      false,
		},
		{
			name:      "season and episode match",
			requested: &models.EpisodeReference{SeasonNumber: 23, EpisodeNumber: 7},
			existing:  &models.EpisodeReference{SeasonNumber: 23, EpisodeNumber: 7},
			want:      true,
		},
		{
			name:      "same absolute episode allows different numbering",
			requested: &models.EpisodeReference{SeasonNumber: 23, EpisodeNumber: 7, AbsoluteEpisodeNumber: 1162},
			existing:  &models.EpisodeReference{SeasonNumber: 23, EpisodeNumber: 1162, AbsoluteEpisodeNumber: 1162},
			want:      true,
		},
		{
			name:      "legacy request episode can match existing absolute episode",
			requested: &models.EpisodeReference{SeasonNumber: 23, EpisodeNumber: 1162},
			existing:  &models.EpisodeReference{SeasonNumber: 23, EpisodeNumber: 7, AbsoluteEpisodeNumber: 1162},
			want:      true,
		},
		{
			name:      "legacy cached episode can match requested absolute episode",
			requested: &models.EpisodeReference{SeasonNumber: 23, EpisodeNumber: 7, AbsoluteEpisodeNumber: 1162},
			existing:  &models.EpisodeReference{SeasonNumber: 23, EpisodeNumber: 1162},
			want:      true,
		},
		{
			name:      "different absolute episodes do not match",
			requested: &models.EpisodeReference{SeasonNumber: 23, EpisodeNumber: 7, AbsoluteEpisodeNumber: 1162},
			existing:  &models.EpisodeReference{SeasonNumber: 23, EpisodeNumber: 8, AbsoluteEpisodeNumber: 1163},
			want:      false,
		},
		{
			name:      "falls back to season episode when absolute missing",
			requested: &models.EpisodeReference{SeasonNumber: 23, EpisodeNumber: 7, AbsoluteEpisodeNumber: 1162},
			existing:  &models.EpisodeReference{SeasonNumber: 23, EpisodeNumber: 7},
			want:      true,
		},
		{
			name:      "canonical identity prevents absolute numbering collision",
			requested: &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 9, AbsoluteEpisodeNumber: 8},
			existing: &models.EpisodeReference{
				SeasonNumber: 1, EpisodeNumber: 8, AbsoluteEpisodeNumber: 8, EpisodeID: "tvdb:episode:85757",
			},
			want: false,
		},
		{
			name: "same canonical episode id survives legacy numbering difference",
			requested: &models.EpisodeReference{
				SeasonNumber: 23, EpisodeNumber: 1162, AbsoluteEpisodeNumber: 1162, EpisodeID: "tvdb:episode:11700059",
			},
			existing: &models.EpisodeReference{
				SeasonNumber: 23, EpisodeNumber: 7, AbsoluteEpisodeNumber: 1162, EpisodeID: "tvdb:episode:11700059",
			},
			want: true,
		},
		{
			name: "different canonical episode ids reject matching absolute",
			requested: &models.EpisodeReference{
				SeasonNumber: 1, EpisodeNumber: 9, AbsoluteEpisodeNumber: 8, EpisodeID: "tvdb:episode:85758",
			},
			existing: &models.EpisodeReference{
				SeasonNumber: 1, EpisodeNumber: 8, AbsoluteEpisodeNumber: 8, EpisodeID: "tvdb:episode:85757",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := prequeueEpisodeMatches(tt.requested, tt.existing)
			if got != tt.want {
				t.Fatalf("prequeueEpisodeMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPrequeueMovieAnimeDetection(t *testing.T) {
	tests := []struct {
		name      string
		title     models.Title
		wantAnime bool
	}{
		{
			name: "anime genre detected",
			title: models.Title{
				Name:   "Ponyo",
				Genres: []string{"Adventure", "Anime", "Fantasy"},
			},
			wantAnime: true,
		},
		{
			name: "east asian animated movie detected via original title",
			title: models.Title{
				Name:         "Spirited Away",
				OriginalName: "千と千尋の神隠し",
				Genres:       []string{"Animation", "Family"},
			},
			wantAnime: true,
		},
		{
			name: "case insensitive anime",
			title: models.Title{
				Name:   "Ponyo",
				Genres: []string{"Drama", "ANIME"},
			},
			wantAnime: true,
		},
		{
			name: "western animated movie is not anime",
			title: models.Title{
				Name:   "Hop",
				Genres: []string{"Animation", "Family"},
			},
			wantAnime: false,
		},
		{
			name: "east asian animated movie detected via language",
			title: models.Title{
				Name:     "Ne Zha",
				Language: "zho",
				Genres:   []string{"Animation", "Fantasy"},
			},
			wantAnime: true,
		},
		{
			name: "non-anime movie",
			title: models.Title{
				Name:   "John Wick",
				Genres: []string{"Action", "Drama"},
			},
			wantAnime: false,
		},
		{
			name: "empty genres",
			title: models.Title{
				Name: "Unknown",
			},
			wantAnime: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := &PrequeueHandler{
				movieMetadataSvc: &mockMovieDetailsProvider{
					title: &tt.title,
				},
			}

			// Simulate the movie anime detection logic from runPrequeueWorker
			var isAnime bool
			mediaType := "movie"

			if mediaType == "movie" && handler.movieMetadataSvc != nil {
				movieQuery := models.MovieDetailsQuery{
					TitleID: "test-id",
					Name:    "Ponyo",
					Year:    2008,
				}
				if movieTitle, err := handler.movieMetadataSvc.MovieInfo(context.Background(), movieQuery); err == nil && movieTitle != nil {
					isAnime = isAnimeTitle(movieTitle)
				}
			}

			if isAnime != tt.wantAnime {
				t.Errorf("isAnime = %v, want %v", isAnime, tt.wantAnime)
			}
		})
	}
}

func TestPrequeueMovieAnimeDetection_NilService(t *testing.T) {
	handler := &PrequeueHandler{
		movieMetadataSvc: nil,
	}

	var isAnime bool
	mediaType := "movie"

	if mediaType == "movie" && handler.movieMetadataSvc != nil {
		// Should not enter this block
		t.Fatal("should not attempt movie lookup with nil service")
	}

	if isAnime {
		t.Error("isAnime should be false when service is nil")
	}
}

func TestCreateEpisodeResolverPopulatesEpisodeAirYear(t *testing.T) {
	handler := &PrequeueHandler{
		metadataSvc: &mockSeriesDetailsProvider{
			details: &models.SeriesDetails{
				Title: models.Title{Name: "ONE PIECE (2023)", Year: 2023},
				Seasons: []models.SeriesSeason{
					{
						Number:       2,
						EpisodeCount: 8,
						Episodes: []models.SeriesEpisode{
							{
								SeasonNumber:  2,
								EpisodeNumber: 4,
								AiredDate:     "2026-03-10",
							},
						},
					},
				},
			},
		},
	}

	got := handler.createEpisodeResolverAndLookupAbsoluteEp(
		context.Background(),
		"tvdb:series:392276",
		"ONE PIECE (2023)",
		2023,
		"tt11737520",
		&models.EpisodeReference{SeasonNumber: 2, EpisodeNumber: 4},
	)

	if got.TargetAirDate != "2026-03-10" {
		t.Fatalf("TargetAirDate = %q, want 2026-03-10", got.TargetAirDate)
	}
	if got.EpisodeAirYear != 2026 {
		t.Fatalf("EpisodeAirYear = %d, want 2026", got.EpisodeAirYear)
	}
	if !got.EpisodeReleased {
		t.Fatal("EpisodeReleased = false, want true")
	}
}

func TestCreateEpisodeResolverNormalizesLegacyAbsoluteEpisode(t *testing.T) {
	handler := &PrequeueHandler{
		metadataSvc: &mockSeriesDetailsProvider{
			details: &models.SeriesDetails{
				Title: models.Title{Name: "One Piece", Year: 1999},
				Seasons: []models.SeriesSeason{
					{
						Number:       23,
						EpisodeCount: 13,
						Episodes: []models.SeriesEpisode{
							{
								ID:                    "tvdb:episode:11700059",
								TVDBID:                11700059,
								Name:                  "A Gargantuan Wave of Emotion",
								SeasonNumber:          23,
								EpisodeNumber:         7,
								AbsoluteEpisodeNumber: 1162,
								AiredDate:             "2026-05-24",
								Runtime:               24,
							},
						},
					},
				},
			},
		},
	}

	got := handler.createEpisodeResolverAndLookupAbsoluteEp(
		context.Background(),
		"tvdb:series:81797",
		"One Piece",
		1999,
		"tt0388629",
		&models.EpisodeReference{SeasonNumber: 23, EpisodeNumber: 1162},
	)

	if got.TargetEpisode == nil {
		t.Fatal("TargetEpisode is nil")
	}
	if got.TargetEpisode.SeasonNumber != 23 || got.TargetEpisode.EpisodeNumber != 7 {
		t.Fatalf("TargetEpisode = S%02dE%02d, want S23E07", got.TargetEpisode.SeasonNumber, got.TargetEpisode.EpisodeNumber)
	}
	if got.TargetEpisode.AbsoluteEpisodeNumber != 1162 {
		t.Fatalf("AbsoluteEpisodeNumber = %d, want 1162", got.TargetEpisode.AbsoluteEpisodeNumber)
	}
	if got.TargetAirDate != "2026-05-24" {
		t.Fatalf("TargetAirDate = %q, want 2026-05-24", got.TargetAirDate)
	}
	if query := handler.buildSearchQuery("One Piece", "series", got.TargetEpisode); query != "One Piece S23E07" {
		t.Fatalf("buildSearchQuery = %q, want One Piece S23E07", query)
	}
}

func TestCreateEpisodeResolverInfersMissingAbsoluteEpisodeFromSeason(t *testing.T) {
	handler := &PrequeueHandler{
		metadataSvc: &mockSeriesDetailsProvider{
			details: &models.SeriesDetails{
				Title: models.Title{Name: "One Piece", Year: 1999, Genres: []string{"Animation"}},
				Seasons: []models.SeriesSeason{
					{
						Number:       23,
						EpisodeCount: 17,
						Episodes: []models.SeriesEpisode{
							{SeasonNumber: 23, EpisodeNumber: 16, AbsoluteEpisodeNumber: 1171},
							{SeasonNumber: 23, EpisodeNumber: 17, AiredDate: "2026-08-02"},
						},
					},
				},
			},
		},
	}

	got := handler.createEpisodeResolverAndLookupAbsoluteEp(
		context.Background(),
		"tmdb:tv:37854",
		"One Piece",
		1999,
		"tt0388629",
		&models.EpisodeReference{SeasonNumber: 23, EpisodeNumber: 17},
	)

	if got.TargetEpisode == nil {
		t.Fatal("TargetEpisode is nil")
	}
	if got.TargetEpisode.AbsoluteEpisodeNumber != 1172 {
		t.Fatalf("AbsoluteEpisodeNumber = %d, want 1172", got.TargetEpisode.AbsoluteEpisodeNumber)
	}
}

func TestCreateEpisodeResolverUsesReleaseAbsoluteEpisodeInsteadOfSpecialShift(t *testing.T) {
	handler := &PrequeueHandler{
		metadataSvc: &mockSeriesDetailsProvider{
			details: &models.SeriesDetails{
				Title: models.Title{Name: "Kaiju No. 8", Year: 2024, Genres: []string{"Anime"}},
				Seasons: []models.SeriesSeason{
					{
						Number:       0,
						EpisodeCount: 1,
						Episodes: []models.SeriesEpisode{
							{Name: "Hoshina's Day Off", SeasonNumber: 0, EpisodeNumber: 1, AbsoluteEpisodeNumber: 13},
						},
					},
					{Number: 1, EpisodeCount: 12},
					{
						Number:       2,
						EpisodeCount: 11,
						Episodes: []models.SeriesEpisode{
							{Name: "Kaiju Weapon", SeasonNumber: 2, EpisodeNumber: 1, AbsoluteEpisodeNumber: 14},
						},
					},
				},
			},
		},
	}

	got := handler.createEpisodeResolverAndLookupAbsoluteEp(
		context.Background(),
		"tvdb:series:358612",
		"Kaiju No. 8",
		2024,
		"tt21975436",
		&models.EpisodeReference{SeasonNumber: 2, EpisodeNumber: 1, AbsoluteEpisodeNumber: 14},
	)

	if got.TargetEpisode == nil {
		t.Fatal("TargetEpisode is nil")
	}
	if got.TargetEpisode.AbsoluteEpisodeNumber != 13 {
		t.Fatalf("AbsoluteEpisodeNumber = %d, want release-style 13", got.TargetEpisode.AbsoluteEpisodeNumber)
	}
}

func TestCreateEpisodeResolverUsesReleaseAbsoluteEpisodeForNonAnime(t *testing.T) {
	handler := &PrequeueHandler{
		metadataSvc: &mockSeriesDetailsProvider{
			details: &models.SeriesDetails{
				Title: models.Title{Name: "Non-Anime Series", Year: 1997, Genres: []string{"Action", "Science Fiction"}},
				Seasons: []models.SeriesSeason{
					{Number: 0, EpisodeCount: 1, Episodes: []models.SeriesEpisode{
						{SeasonNumber: 0, EpisodeNumber: 1, AbsoluteEpisodeNumber: 13},
					}},
					{Number: 1, EpisodeCount: 12},
					{Number: 2, EpisodeCount: 11, Episodes: []models.SeriesEpisode{
						{ID: "tvdb:episode:85757", TVDBID: 85757, SeasonNumber: 2, EpisodeNumber: 1, AbsoluteEpisodeNumber: 14},
					}},
				},
			},
		},
	}

	got := handler.createEpisodeResolverAndLookupAbsoluteEp(
		context.Background(),
		"tvdb:series:12345",
		"Non-Anime Series",
		1997,
		"tt0118480",
		&models.EpisodeReference{SeasonNumber: 2, EpisodeNumber: 1, AbsoluteEpisodeNumber: 14},
	)

	if got.TargetEpisode == nil {
		t.Fatal("TargetEpisode is nil")
	}
	if got.IsAnime {
		t.Fatal("expected series to remain non-anime")
	}
	if got.TargetEpisode.AbsoluteEpisodeNumber != 13 {
		t.Fatalf("AbsoluteEpisodeNumber = %d, want release absolute 13", got.TargetEpisode.AbsoluteEpisodeNumber)
	}
}

func TestInferAbsoluteEpisodeNumberRejectsConflictingAnchors(t *testing.T) {
	seasons := []models.SeriesSeason{
		{
			Number: 23,
			Episodes: []models.SeriesEpisode{
				{SeasonNumber: 23, EpisodeNumber: 15, AbsoluteEpisodeNumber: 1170},
				{SeasonNumber: 23, EpisodeNumber: 16, AbsoluteEpisodeNumber: 999},
			},
		},
	}

	got := inferAbsoluteEpisodeNumber(seasons, models.SeriesEpisode{SeasonNumber: 23, EpisodeNumber: 17})
	if got != 0 {
		t.Fatalf("inferAbsoluteEpisodeNumber() = %d, want 0 for conflicting anchors", got)
	}
}

func TestAnnotateResultEpisodeClonesCachedAttributesAndAddsAbsoluteHint(t *testing.T) {
	cachedAttributes := map[string]string{"source": "cached"}
	result := models.NZBResult{Attributes: cachedAttributes}

	annotateResultEpisode(&result, &models.EpisodeReference{
		SeasonNumber:          23,
		EpisodeNumber:         17,
		AbsoluteEpisodeNumber: 1172,
	})

	if result.Attributes["targetEpisodeCode"] != "S23E17" {
		t.Fatalf("targetEpisodeCode = %q, want S23E17", result.Attributes["targetEpisodeCode"])
	}
	if result.Attributes["absoluteEpisodeNumber"] != "1172" {
		t.Fatalf("absoluteEpisodeNumber = %q, want 1172", result.Attributes["absoluteEpisodeNumber"])
	}
	if _, mutated := cachedAttributes["absoluteEpisodeNumber"]; mutated {
		t.Fatal("annotateResultEpisode mutated cached attributes")
	}
}

func TestPrequeueMovieAnimeDetection_SeriesSkipped(t *testing.T) {
	handler := &PrequeueHandler{
		movieMetadataSvc: &mockMovieDetailsProvider{
			title: &models.Title{
				Name:   "Some Series",
				Genres: []string{"Animation"},
			},
		},
	}

	var isAnime bool
	mediaType := "series"

	// The movie anime detection should not run for series
	if mediaType == "movie" && handler.movieMetadataSvc != nil {
		t.Fatal("should not attempt movie lookup for series media type")
	}

	if isAnime {
		t.Error("isAnime should be false for series media type")
	}
}

func TestShouldForceReresolveForStatus(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{
		{status: http.StatusUnauthorized, want: true},
		{status: http.StatusForbidden, want: true},
		{status: http.StatusNotFound, want: true},
		{status: http.StatusGone, want: true},
		{status: http.StatusMethodNotAllowed, want: false},
		{status: http.StatusTooManyRequests, want: false},
		{status: http.StatusInternalServerError, want: false},
		{status: http.StatusOK, want: false},
	}

	for _, tt := range tests {
		if got := shouldForceReresolveForStatus(tt.status); got != tt.want {
			t.Fatalf("status %d: got %v want %v", tt.status, got, tt.want)
		}
	}
}

func TestPrequeueEpisodeHelpers_AllowSpecials(t *testing.T) {
	handler := &PrequeueHandler{}
	episode := &models.EpisodeReference{SeasonNumber: 0, EpisodeNumber: 1}

	if got, want := buildDisplayName("The Bear", 2022, episode), "The Bear S00E01"; got != want {
		t.Fatalf("buildDisplayName = %q, want %q", got, want)
	}

	if got, want := handler.buildSearchQuery("The Bear", "series", episode), "The Bear S00E01"; got != want {
		t.Fatalf("buildSearchQuery = %q, want %q", got, want)
	}
}

func TestIsM2TSStreamPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "plain m2ts", path: "/debrid/realdebrid/BDMV/STREAM/00001.m2ts", want: true},
		{name: "uppercase m2ts", path: "/debrid/realdebrid/BDMV/STREAM/00001.M2TS", want: true},
		{name: "m2ts url with query", path: "https://example.com/BDMV/STREAM/00001.m2ts?token=abc", want: true},
		{name: "m2ts url with fragment", path: "https://example.com/BDMV/STREAM/00001.m2ts#stream", want: true},
		{name: "mkv", path: "/debrid/realdebrid/movie.mkv", want: false},
		{name: "m2ts in directory only", path: "/debrid/m2ts/movie.mkv", want: false},
		{name: "empty", path: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isM2TSStreamPath(tt.path); got != tt.want {
				t.Fatalf("isM2TSStreamPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestAdoptMigrationReplacesPrequeueStream(t *testing.T) {
	store := playback.NewPrequeueStore(time.Hour)
	entry, created := store.Create("movie:1", "Example", "user1", "movie", 2024, nil, "details")
	if !created {
		t.Fatal("Create returned created=false")
	}
	oldResult := models.NZBResult{
		Title:       "Old.Release.2024.2160p",
		Indexer:     "old-indexer",
		GUID:        "guid-old",
		ServiceType: models.ServiceTypeUsenet,
	}
	store.Update(entry.ID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusReady
		e.StreamPath = "/downloads/old/title.mkv"
		e.HLSSessionID = "old-hls"
		e.HLSPlaylistURL = "/video/hls/old/stream.m3u8"
		e.HasDolbyVision = true
		e.HasHDR10 = true
		e.DolbyVisionProfile = "8"
		e.NeedsAudioTranscode = true
		e.SelectedAudioTrack = 2
		e.SelectedSubtitleTrack = 3
		e.AudioTracks = []playback.AudioTrackInfo{{Index: 2, Language: "eng"}}
		e.SubtitleTracks = []playback.SubtitleTrackInfo{{Index: 3, Language: "eng"}}
		e.SubtitleSessions = map[int]*models.SubtitleSessionInfo{3: &models.SubtitleSessionInfo{SessionID: "old-sub"}}
		e.Error = "old error"
		e.SelectedResult = &oldResult
		e.SelectedResultIndex = 0
		e.MigrationCandidates = []models.NZBResult{oldResult}
	})
	workerCancelled := false
	store.SetCancelFunc(entry.ID, func() { workerCancelled = true })

	newResult := models.NZBResult{
		Title:       "Better.Release.2024.2160p",
		Indexer:     "new-indexer",
		GUID:        "guid-new",
		SizeBytes:   222,
		ServiceType: models.ServiceTypeDebrid,
		Attributes: map[string]string{
			"passthrough_format": "true",
			"raw_name":           "RD Better",
			"raw_description":    "RD description",
			"provider":           "realdebrid",
		},
	}
	reqBody, err := json.Marshal(adoptMigrationRequest{
		StreamPath:          "/debrid/realdebrid/better.mkv",
		Result:              newResult,
		SelectedResultIndex: 1,
		FileSize:            333,
		HealthStatus:        "healthy",
		MigrationCandidates: []models.NZBResult{oldResult, newResult},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	prewarm := &adoptMigrationPrewarmMock{}
	fullProber := &adoptMigrationFullProber{}
	handler := &PrequeueHandler{store: store, prewarmSvc: prewarm, fullProber: fullProber}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/playback/prequeue/"+entry.ID+"/adopt-migration", bytes.NewReader(reqBody))
	req = mux.SetURLVars(req, map[string]string{"prequeueID": entry.ID})

	handler.AdoptMigration(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !workerCancelled {
		t.Fatal("active prequeue worker was not cancelled before adopting migration")
	}

	var resp playback.PrequeueStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.StreamPath != "/debrid/realdebrid/better.mkv" {
		t.Fatalf("StreamPath = %q", resp.StreamPath)
	}
	if fullProber.path != "/debrid/realdebrid/better.mkv" {
		t.Fatalf("ProbeVideoFull path = %q", fullProber.path)
	}
	if resp.HLSSessionID != "" || resp.HLSPlaylistURL != "" {
		t.Fatalf("stale HLS state not cleared: %#v", resp)
	}
	if !resp.HasDolbyVision || !resp.HasHDR10 || resp.DolbyVisionProfile != "8" || !resp.NeedsAudioTranscode || resp.Duration != 123.5 {
		t.Fatalf("probe metadata not stored: %#v", resp)
	}
	if resp.SelectedAudioTrack != 1 || resp.SelectedSubtitleTrack != -1 {
		t.Fatalf("selected tracks = audio %d subtitle %d, want 1/-1", resp.SelectedAudioTrack, resp.SelectedSubtitleTrack)
	}
	if resp.TrackSelection == nil || resp.TrackSelection.Audio == nil || resp.TrackSelection.Audio.StreamIndex != 1 ||
		resp.TrackSelection.Audio.TypeIndex != 0 || resp.TrackSelection.Subtitle != nil || resp.TrackSelection.SubtitleMode != "off" {
		t.Fatalf("trackSelection = %#v, want audio stream 1 and subtitles off", resp.TrackSelection)
	}
	if len(resp.AudioTracks) != 2 || len(resp.SubtitleTracks) != 1 || len(resp.SubtitleSessions) != 0 {
		t.Fatalf("track/subtitle state = audio=%d subtitle=%d sessions=%d, want 2/1/0", len(resp.AudioTracks), len(resp.SubtitleTracks), len(resp.SubtitleSessions))
	}
	if resp.AudioTracks[0].Index != 1 || resp.AudioTracks[0].Language != "eng" {
		t.Fatalf("audio track metadata = %#v", resp.AudioTracks[0])
	}
	if resp.SubtitleTracks[0].Index != 3 || !resp.SubtitleTracks[0].IsBitmap {
		t.Fatalf("subtitle track metadata = %#v", resp.SubtitleTracks[0])
	}
	if resp.ServiceType != "debrid" || resp.FileSize != 333 || resp.HealthStatus != "healthy" {
		t.Fatalf("metadata = service %q size %d health %q", resp.ServiceType, resp.FileSize, resp.HealthStatus)
	}
	if resp.SelectedResult == nil || resp.SelectedResult.GUID != "guid-new" || resp.SelectedResultIndex != 1 {
		t.Fatalf("selected result = %#v index=%d", resp.SelectedResult, resp.SelectedResultIndex)
	}
	if len(resp.MigrationCandidates) != 2 {
		t.Fatalf("MigrationCandidates length = %d, want 2", len(resp.MigrationCandidates))
	}
	if resp.PassthroughName != "RD Better" || resp.PassthroughDescription != "RD description" {
		t.Fatalf("passthrough = %q / %q", resp.PassthroughName, resp.PassthroughDescription)
	}
	if adopted, ok := store.Get(entry.ID); !ok || !adopted.MigrationAdopted {
		t.Fatalf("MigrationAdopted = %v, exists=%v; want true", adopted != nil && adopted.MigrationAdopted, ok)
	}
	if len(prewarm.updated) != 1 || prewarm.updated[0] != entry.ID {
		t.Fatalf("prewarm updates = %#v, want [%s]", prewarm.updated, entry.ID)
	}
}

func TestAdoptMigrationRejectsM2TSStream(t *testing.T) {
	store := playback.NewPrequeueStore(time.Hour)
	entry, created := store.Create("movie:1", "Example", "user1", "movie", 2024, nil, "details")
	if !created {
		t.Fatal("Create returned created=false")
	}
	store.Update(entry.ID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusReady
		e.StreamPath = "/debrid/torbox/original.mkv"
	})

	reqBody, err := json.Marshal(adoptMigrationRequest{
		StreamPath: "/debrid/torbox/torrent/file/2/Disc/BDMV/STREAM/00060.m2ts",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	handler := &PrequeueHandler{store: store}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/playback/prequeue/"+entry.ID+"/adopt-migration", bytes.NewReader(reqBody))
	req = mux.SetURLVars(req, map[string]string{"prequeueID": entry.ID})

	handler.AdoptMigration(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	got, ok := store.Get(entry.ID)
	if !ok {
		t.Fatal("prequeue disappeared")
	}
	if got.StreamPath != "/debrid/torbox/original.mkv" || got.MigrationAdopted {
		t.Fatalf("prequeue mutated after rejected migration: %#v", got)
	}
}

func TestPrequeueReusesAdoptedMigrationWithoutTrackMetadata(t *testing.T) {
	store := playback.NewPrequeueStore(time.Hour)
	entry, created := store.Create("movie:1", "Example", "user1", "movie", 2024, nil, "details")
	if !created {
		t.Fatal("Create returned created=false")
	}
	store.Update(entry.ID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusReady
		e.StreamPath = "/debrid/realdebrid/final-good.mkv"
		e.ServiceType = "debrid"
		e.SelectedAudioTrack = -1
		e.SelectedSubtitleTrack = -1
		e.MigrationAdopted = true
	})

	body, err := json.Marshal(playback.PrequeueRequest{
		TitleID:   "movie:1",
		TitleName: "Example",
		MediaType: "movie",
		UserID:    "user1",
		Year:      2024,
		Reason:    "details",
		SkipHLS:   true,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	handler := &PrequeueHandler{store: store}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/playback/prequeue", bytes.NewReader(body))

	handler.Prequeue(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp playback.PrequeueResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.PrequeueID != entry.ID {
		t.Fatalf("PrequeueID = %q, want reused %q", resp.PrequeueID, entry.ID)
	}
	if resp.Status != playback.PrequeueStatusReady {
		t.Fatalf("Status = %q, want ready", resp.Status)
	}
	if len(store.ListAll()) != 1 {
		t.Fatalf("store entries = %d, want 1 reused entry", len(store.ListAll()))
	}
}

func TestDefaultExternalURLValidator(t *testing.T) {
	originalTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = originalTransport }()

	t.Run("allows head 200", func(t *testing.T) {
		http.DefaultTransport = prequeueRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodHead {
				t.Fatalf("expected HEAD request, got %s", r.Method)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		})

		if err := defaultExternalURLValidator(context.Background(), "https://example.com/stream"); err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	})

	t.Run("forces reresolve on 403", func(t *testing.T) {
		http.DefaultTransport = prequeueRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		})

		if err := defaultExternalURLValidator(context.Background(), "https://example.com/stream"); err == nil {
			t.Fatal("expected validation error for 403")
		}
	})

	t.Run("forces reresolve when head redirects to ElfHosted slate", func(t *testing.T) {
		http.DefaultTransport = prequeueRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			finalReq := r.Clone(r.Context())
			finalReq.URL, _ = url.Parse("https://slate.elfhosted.com/cache/link-expired.mp4")
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     make(http.Header),
				Request:    finalReq,
			}, nil
		})

		if err := defaultExternalURLValidator(context.Background(), "https://example.com/stream"); err == nil {
			t.Fatal("expected validation error for ElfHosted slate redirect")
		}
	})

	t.Run("forces reresolve when ranged fallback returns slate playlist", func(t *testing.T) {
		http.DefaultTransport = prequeueRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			status := http.StatusMethodNotAllowed
			body := ""
			if r.Method == http.MethodGet {
				status = http.StatusOK
				body = "#EXTM3U\n#EXTINF:120.960,\nhttps://slate.elfhosted.com/cache/link-expired.ts\n"
			}
			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		})

		if err := defaultExternalURLValidator(context.Background(), "https://example.com/stream"); err == nil {
			t.Fatal("expected validation error for ElfHosted slate playlist")
		}
	})

	t.Run("allows head 405 fallback", func(t *testing.T) {
		http.DefaultTransport = prequeueRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusMethodNotAllowed,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		})

		if err := defaultExternalURLValidator(context.Background(), "https://example.com/stream"); err != nil {
			t.Fatalf("expected nil error for 405, got %v", err)
		}
	})
}

func TestValidateReadyEntryForReuse(t *testing.T) {
	handler := &PrequeueHandler{}

	t.Run("skips non external paths", func(t *testing.T) {
		entry := &playback.PrequeueEntry{StreamPath: "/debrid/realdebrid/file.mkv"}
		if err := handler.validateReadyEntryForReuse(context.Background(), entry); err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	})

	t.Run("uses injected validator for external paths", func(t *testing.T) {
		called := false
		handler.externalURLValidator = func(_ context.Context, streamURL string) error {
			called = true
			if streamURL != "https://example.com/stream" {
				t.Fatalf("unexpected stream URL %q", streamURL)
			}
			return nil
		}

		entry := &playback.PrequeueEntry{StreamPath: "https://example.com/stream"}
		if err := handler.validateReadyEntryForReuse(context.Background(), entry); err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if !called {
			t.Fatal("expected validator to be called")
		}
	})

	t.Run("allows configured private usenet engine host", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodHead {
				t.Fatalf("method = %s, want HEAD", r.Method)
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
		settings := config.DefaultSettings()
		settings.UsenetEngines = []config.UsenetEngineSettings{{
			Name:          "Local engine",
			Enabled:       true,
			BaseURL:       server.URL,
			WebDAVBaseURL: server.URL,
		}}
		if err := manager.Save(settings); err != nil {
			t.Fatalf("save settings: %v", err)
		}

		configuredHandler := &PrequeueHandler{configManager: manager}
		entry := &playback.PrequeueEntry{StreamPath: server.URL + "/webdav/file.mkv"}
		if err := configuredHandler.validateReadyEntryForReuse(context.Background(), entry); err != nil {
			t.Fatalf("configured private host was rejected: %v", err)
		}
	})
}

func TestNormalizeClientID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"valid hardware id", "261dc357a50db1db", "261dc357a50db1db"},
		{"trims whitespace", "  abc123  ", "abc123"},
		{"unknown sentinel", "unknown", ""},
		{"unknown mixed case", "Unknown", ""},
		{"unknown padded", "  UNKNOWN ", ""},
		{"empty", "", ""},
		{"null sentinel", "null", ""},
		{"undefined sentinel", "undefined", ""},
		{"zero sentinel", "0", ""},
		{"valid generated id", "18f2a-1a2b-3c4d", "18f2a-1a2b-3c4d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeClientID(tc.in); got != tc.want {
				t.Fatalf("normalizeClientID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A quality preset picked as version gets its own prequeue, and only when the
// server allows that preset per request.
func TestPrequeueQualityPresetHasItsOwnScope(t *testing.T) {
	store := playback.NewPrequeueStore(time.Hour)
	ready := func(scope, path string) string {
		entry, _ := store.CreateScoped("movie:1", "Example", "user1", "movie", 2024, nil, "details", scope)
		store.Update(entry.ID, func(e *playback.PrequeueEntry) {
			e.Status = playback.PrequeueStatusReady
			e.StreamPath = path
			e.ServiceType = "debrid"
			e.SelectedAudioTrack = -1
			e.SelectedSubtitleTrack = -1
			e.MigrationAdopted = true
		})
		return entry.ID
	}
	defaultID := ready(playback.DefaultPrequeueSettingsScopeKey, "/debrid/rd/default.mkv")
	presetID := ready(playback.DefaultPrequeueSettingsScopeKey+"|preset:p-german", "/debrid/rd/german.mkv")

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	settings := config.DefaultSettings()
	settings.QualityPresets.AllowRequestOverride = false
	settings.QualityPresets.PickerPresets = []string{"p-german"}
	if err := mgr.Save(settings); err != nil {
		t.Fatal(err)
	}
	handler := &PrequeueHandler{store: store, configManager: mgr}

	ask := func(preset string) string {
		t.Helper()
		body, _ := json.Marshal(playback.PrequeueRequest{
			TitleID: "movie:1", TitleName: "Example", MediaType: "movie", UserID: "user1",
			Year: 2024, Reason: "details", SkipHLS: true, QualityPreset: preset,
		})
		rec := httptest.NewRecorder()
		handler.Prequeue(rec, httptest.NewRequest(http.MethodPost, "/api/playback/prequeue", bytes.NewReader(body)))
		var resp playback.PrequeueResponse
		if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&resp) != nil {
			t.Fatalf("preset %q: status %d %s", preset, rec.Code, rec.Body.String())
		}
		return resp.PrequeueID
	}
	if got := ask(""); got != defaultID {
		t.Errorf("no preset: got %s, want the default entry", got)
	}
	if got := ask("p-german"); got != presetID {
		t.Errorf("offered preset: got %s, want its own entry", got)
	}
	// A preset the server does not offer is ignored: the default applies.
	if got := ask("p-other"); got != defaultID {
		t.Errorf("not offered preset: got %s, want the default entry", got)
	}
}

// Preset-ranked results list one breakdown entry per matched custom format,
// so a position-by-position comparison mixed unrelated formats: in the FMA:B
// case every 1080p release shared the "Custom Format: 1080p" bucket and an
// English release with score 50 raced (and beat) the German ones with 13700.
// Buckets must follow the preset: quality position, then format score.
func TestPrequeuePreferenceBucketsFollowQualityPreset(t *testing.T) {
	preset := func(title string, quality, score int64, formats ...string) models.ScoredNZBResult {
		items := []models.ScoreBreakdownItem{
			{Criterion: indexer.PresetBreakdownCriterion},
			{Criterion: indexer.PresetQualityCriterion, RankValue: quality},
		}
		for _, f := range formats {
			items = append(items, models.ScoreBreakdownItem{Criterion: "Custom Format: " + f, Points: 50, RankValue: 50})
		}
		items = append(items,
			models.ScoreBreakdownItem{Criterion: indexer.PresetFormatScoreCriterion, Points: int(score), RankValue: score},
			models.ScoreBreakdownItem{Criterion: "Languages"})
		return models.ScoredNZBResult{NZBResult: models.NZBResult{Title: title}, ScoreBreakdown: items}
	}
	scored := []models.ScoredNZBResult{
		preset("Fullmetal.Alchemist.S01.German.DL.DTS.1080p.BluRay.x265.10bit-ABJ", 15, 13700, "1080p", "German DL", "German Bluray Tier 01"),
		preset("Fullmetal.Alchemist.Brotherhood.S01E01.German.DL.DTS.1080p.BluRay.x265-ABJ", 15, 13700, "1080p", "German DL", "German Bluray Tier 01"),
		preset("Fullmetal.Alchemist.Brotherhood.S01E01.German.DTS.DL.1080p.BluRay.x264-AST4u", 15, 13350, "1080p", "German DL"),
		preset("Fullmetal.Alchemist.S01.German.DL.DTS.1080p.Untouched.BluRay-Fiona", 15, 11700, "1080p", "German DL"),
		preset("[NotShadyCrab] Fullmetal Alchemist Brotherhood (Ep. 01-14) [BD 1080p Hi10 FLAC] [Dual Audio]", 15, 50, "1080p"),
		preset("[ANIX] Fullmetal Alchemist Brotherhood - Ep01 [BD 1080p H264 FLAC][Multiple Subtitle]", 15, 50, "1080p"),
		preset("Full.Metal.Alchemist.BrotherHood.E01.720p.Bluray.x264-OTAKU", 14, 5, "720p"),
		{NZBResult: models.NZBResult{Title: "unranked"}},
	}
	buckets, criterion := prequeuePreferenceBuckets(scored)
	if criterion != indexer.PresetBreakdownCriterion {
		t.Fatalf("criterion = %q", criterion)
	}
	if want := []int{0, 0, 1, 2, 3, 3, 4, 5}; !reflect.DeepEqual(buckets, want) {
		t.Fatalf("buckets = %v, want %v", buckets, want)
	}
	// A higher quality position beats any format score.
	better := []models.ScoredNZBResult{preset("low", 10, 99999), preset("high", 20, 0)}
	if b, _ := prequeuePreferenceBuckets(better); !reflect.DeepEqual(b, []int{1, 0}) {
		t.Fatalf("quality must come first: %v", b)
	}
	// With propers preferred, a PROPER does not race the release it fixes.
	withRevision := func(r models.ScoredNZBResult, rev int64) models.ScoredNZBResult {
		items := append([]models.ScoreBreakdownItem{}, r.ScoreBreakdown[:2]...)
		items = append(items, models.ScoreBreakdownItem{Criterion: indexer.PresetRevisionCriterion, RankValue: rev})
		r.ScoreBreakdown = append(items, r.ScoreBreakdown[2:]...)
		return r
	}
	propers := []models.ScoredNZBResult{withRevision(preset("plain", 15, 100), 100), withRevision(preset("proper", 15, 100), 200)}
	if b, _ := prequeuePreferenceBuckets(propers); !reflect.DeepEqual(b, []int{1, 0}) {
		t.Fatalf("the proper must rank alone: %v", b)
	}
}
