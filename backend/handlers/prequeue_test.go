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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/models"
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
	path string
}

func (m *adoptMigrationFullProber) ProbeVideoFull(_ context.Context, path string) (*VideoFullResult, error) {
	m.path = path
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
// OPP-1 resolution race can be exercised without a live importer/NNTP stack.
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
// whose articles stall resolution (OPP-1).
func slowFailingResolve(ctx context.Context, _ models.NZBResult) (*models.PlaybackResolution, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(3 * time.Second):
		return nil, fmt.Errorf("articles unavailable")
	}
}

func TestRacePrequeueResolutionsAdoptsFastSecondCandidate(t *testing.T) {
	// OPP-1 verification: a slow/failing top candidate must not stall the race;
	// the fast healthy candidate is adopted and wall-clock is far less than the
	// serial sum of the two resolutions.
	process := func(ctx context.Context, i int) (*candidateResolution, *candidateResolution, error) {
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
	winner, usedFallback, err := racePrequeueResolutions(context.Background(), 2, 4, process, nil)
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
		t.Fatalf("race took %v; the slow candidate was not cancelled concurrently (serial sum would be >3s)", elapsed)
	}
}

func winnerIndex(w *candidateResolution) int {
	if w == nil {
		return -1
	}
	return w.index
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

	process := func(ctx context.Context, i int) (*candidateResolution, *candidateResolution, error) {
		switch i {
		case 0:
			// Slow dead top candidate: holds a slot until cancelled.
			_, err := slowFailingResolve(ctx, models.NZBResult{Title: "slow-dead"})
			return nil, nil, err
		case 1:
			return &candidateResolution{index: 1, result: models.NZBResult{Title: "fast-good"}, resolution: &models.PlaybackResolution{WebDAVPath: "/webdav/fast.mkv"}}, nil, nil
		default:
			return nil, nil, fmt.Errorf("unexpected index %d", i)
		}
	}

	winner, _, err := racePrequeueResolutions(context.Background(), 4, 2, process, report)
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
	// First dispatch must open a single-slot window at candidate 0.
	if windows[0] != [2]int{0, 0} {
		t.Fatalf("first window = %v, want [0 0]", windows[0])
	}
	// The window must grow to include candidate 1 while both are in flight.
	grew := false
	for _, w := range windows {
		if w[0] == 0 && w[1] >= 1 {
			grew = true
		}
		if w[0] > w[1] {
			t.Fatalf("invalid window %v (min > max)", w)
		}
	}
	if !grew {
		t.Fatalf("window never grew past the first candidate: %v", windows)
	}
}

func TestRacePrequeueResolutionsFallsBackToDeprioritizedWhenNothingValidates(t *testing.T) {
	process := func(ctx context.Context, i int) (*candidateResolution, *candidateResolution, error) {
		if i == 0 {
			return nil, &candidateResolution{
				index:      0,
				result:     models.NZBResult{Title: "deprioritized"},
				resolution: &models.PlaybackResolution{WebDAVPath: "/webdav/fallback.mkv"},
			}, nil
		}
		return nil, nil, fmt.Errorf("candidate %d failed", i)
	}

	winner, usedFallback, err := racePrequeueResolutions(context.Background(), 3, 2, process, nil)
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
	process := func(ctx context.Context, i int) (*candidateResolution, *candidateResolution, error) {
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

	winner, usedFallback, err := racePrequeueResolutions(context.Background(), 3, 2, process, nil)
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
	process := func(ctx context.Context, i int) (*candidateResolution, *candidateResolution, error) {
		return nil, nil, fmt.Errorf("candidate %d failed", i)
	}

	winner, usedFallback, err := racePrequeueResolutions(context.Background(), 2, 2, process, nil)
	if winner != nil || usedFallback || err == nil {
		t.Fatalf("all-fail race returned winner=%v usedFallback=%v err=%v, want all failures surfaced", winner != nil, usedFallback, err)
	}
}

func TestRacePrequeueResolutionsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	process := func(ctx context.Context, i int) (*candidateResolution, *candidateResolution, error) {
		return nil, nil, fmt.Errorf("must not be invoked on cancelled context")
	}

	winner, usedFallback, err := racePrequeueResolutions(ctx, 2, 2, process, nil)
	if winner != nil || usedFallback {
		t.Fatalf("cancelled race returned winner=%v usedFallback=%v", winner != nil, usedFallback)
	}
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled race returned err=%v, want context.Canceled", err)
	}
}

// TestResolveCandidatesAdoptsFastHealthyCandidate is the handler-level OPP-1
// verification: the wired resolution phase (preflight gate, resolve, probe,
// policy checks) races candidates and adopts the fast healthy second candidate
// even though the top-ranked candidate fails slowly on the wire.
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
	}

	allResults := []models.NZBResult{
		{Title: "slow-dead", ServiceType: models.ServiceTypeUsenet},
		{Title: "fast-good", ServiceType: models.ServiceTypeUsenet},
	}

	start := time.Now()
	choice, err := handler.resolveCandidates(context.Background(), "prequeue-test", allResults, prequeueResolutionOptions{
		mediaType:          "movie",
		hdrDVPolicy:        models.HDRDVPolicyIncludeHDRDV,
		unknownTrackPolicy: "none",
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

func TestAllowedAudioTracksReject(t *testing.T) {
	tests := []struct {
		name     string
		allowed  []string
		streams  []AudioStreamInfo
		rejected bool
	}{
		{name: "empty allowlist permits all", streams: []AudioStreamInfo{{Language: "rus"}}},
		{name: "allowed language present", allowed: []string{"eng"}, streams: []AudioStreamInfo{{Language: "rus"}, {Language: "eng"}}},
		{name: "language in title is recognized", allowed: []string{"eng"}, streams: []AudioStreamInfo{{Title: "English Dolby Atmos"}}},
		{name: "disallowed language is rejected", allowed: []string{"eng"}, streams: []AudioStreamInfo{{Language: "rus"}}, rejected: true},
		{name: "unknown language is rejected", allowed: []string{"eng"}, streams: []AudioStreamInfo{{Index: 1}}, rejected: true},
		{name: "missing audio tracks is rejected", allowed: []string{"eng"}, rejected: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := allowedAudioTracksReject(tt.allowed, tt.streams)
			if got != tt.rejected {
				t.Fatalf("allowedAudioTracksReject() = %v, want %v", got, tt.rejected)
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
