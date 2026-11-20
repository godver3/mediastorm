package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gorilla/mux"

	"novastream/models"
	"novastream/services/badstreams"
	"novastream/services/trackselect"
)

// mockPlaybackService implements the playbackService interface for testing.
type mockPlaybackService struct {
	resolveFunc      func(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error)
	resolveBatchFunc func(ctx context.Context, candidate models.NZBResult, episodes []models.BatchEpisodeTarget) (*models.BatchResolveResponse, error)
	queueStatusFunc  func(ctx context.Context, queueID int64) (*models.PlaybackResolution, error)
}

type recordingThumbnailPrewarmer struct {
	paths []string
}

func (p *recordingThumbnailPrewarmer) PrewarmThumbnails(path string) {
	p.paths = append(p.paths, path)
}

func (m *mockPlaybackService) Resolve(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error) {
	if m.resolveFunc != nil {
		return m.resolveFunc(ctx, candidate)
	}
	return &models.PlaybackResolution{WebDAVPath: "/test"}, nil
}

func (m *mockPlaybackService) ResolveBatch(ctx context.Context, candidate models.NZBResult, episodes []models.BatchEpisodeTarget) (*models.BatchResolveResponse, error) {
	if m.resolveBatchFunc != nil {
		return m.resolveBatchFunc(ctx, candidate, episodes)
	}
	results := make([]models.BatchEpisodeResult, len(episodes))
	for i, ep := range episodes {
		results[i] = models.BatchEpisodeResult{
			SeasonNumber:  ep.SeasonNumber,
			EpisodeNumber: ep.EpisodeNumber,
			EpisodeCode:   ep.EpisodeCode,
			Resolution: &models.PlaybackResolution{
				WebDAVPath:   "/debrid/test/file",
				HealthStatus: "cached",
			},
		}
	}
	return &models.BatchResolveResponse{Results: results}, nil
}

func (m *mockPlaybackService) QueueStatus(ctx context.Context, queueID int64) (*models.PlaybackResolution, error) {
	if m.queueStatusFunc != nil {
		return m.queueStatusFunc(ctx, queueID)
	}
	return &models.PlaybackResolution{QueueID: queueID}, nil
}

func TestResolve_RejectsMarkedBadStreamByDefault(t *testing.T) {
	badStreamSvc := badstreams.New(filepath.Join(t.TempDir(), "bad_streams.json"))
	result := models.NZBResult{
		Title:       "Sample Release",
		ServiceType: models.ServiceTypeUsenet,
	}
	if _, err := badStreamSvc.Mark(badstreams.MarkRequest{
		ReleaseName: "Sample Release",
		ServiceType: string(models.ServiceTypeUsenet),
	}); err != nil {
		t.Fatalf("mark bad stream: %v", err)
	}

	resolveCalled := false
	h := NewPlaybackHandler(&mockPlaybackService{
		resolveFunc: func(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error) {
			resolveCalled = true
			return &models.PlaybackResolution{WebDAVPath: "/test"}, nil
		},
	})
	h.SetBadStreamsService(badStreamSvc)

	body, _ := json.Marshal(map[string]interface{}{"result": result})
	req := httptest.NewRequest(http.MethodPost, "/api/playback/resolve", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	h.Resolve(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d: %s", http.StatusConflict, rec.Code, rec.Body.String())
	}
	if resolveCalled {
		t.Fatal("expected marked bad stream to be rejected before resolve")
	}
}

func TestResolve_AllowsMarkedBadStreamWithManualOverride(t *testing.T) {
	badStreamSvc := badstreams.New(filepath.Join(t.TempDir(), "bad_streams.json"))
	result := models.NZBResult{
		Title:       "Sample Release",
		ServiceType: models.ServiceTypeUsenet,
	}
	if _, err := badStreamSvc.Mark(badstreams.MarkRequest{
		ReleaseName: "Sample Release",
		ServiceType: string(models.ServiceTypeUsenet),
	}); err != nil {
		t.Fatalf("mark bad stream: %v", err)
	}

	resolveCalled := false
	h := NewPlaybackHandler(&mockPlaybackService{
		resolveFunc: func(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error) {
			resolveCalled = true
			return &models.PlaybackResolution{WebDAVPath: "/test"}, nil
		},
	})
	h.SetBadStreamsService(badStreamSvc)

	body, _ := json.Marshal(map[string]interface{}{
		"result":         result,
		"allowMarkedBad": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/playback/resolve", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	h.Resolve(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if !resolveCalled {
		t.Fatal("expected marked bad stream override to continue to resolve")
	}
}

func TestResolve_PrewarmsFinalPlaybackPath(t *testing.T) {
	h := NewPlaybackHandler(&mockPlaybackService{
		resolveFunc: func(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error) {
			return &models.PlaybackResolution{WebDAVPath: "/debrid/movie.mkv"}, nil
		},
	})
	prewarmer := &recordingThumbnailPrewarmer{}
	h.SetThumbnailPrewarmer(prewarmer)

	body, _ := json.Marshal(map[string]interface{}{"result": models.NZBResult{Title: "Movie"}})
	req := httptest.NewRequest(http.MethodPost, "/api/playback/resolve", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	h.Resolve(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if len(prewarmer.paths) != 1 || prewarmer.paths[0] != "/debrid/movie.mkv" {
		t.Fatalf("prewarmed paths = %#v", prewarmer.paths)
	}
}

func TestResolve_RejectsM2TSPlaybackSource(t *testing.T) {
	h := NewPlaybackHandler(&mockPlaybackService{
		resolveFunc: func(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error) {
			return &models.PlaybackResolution{
				WebDAVPath: "/debrid/torbox/torrent/file/2/Disc/BDMV/STREAM/00060.m2ts",
			}, nil
		},
	})

	body, _ := json.Marshal(map[string]interface{}{"result": models.NZBResult{Title: "Blu-ray disc"}})
	req := httptest.NewRequest(http.MethodPost, "/api/playback/resolve", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	h.Resolve(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected status %d, got %d: %s", http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "unsupported .m2ts playback source\n" {
		t.Fatalf("body = %q", got)
	}
}

func TestQueueStatus_RejectsM2TSPlaybackSource(t *testing.T) {
	h := NewPlaybackHandler(&mockPlaybackService{
		queueStatusFunc: func(ctx context.Context, queueID int64) (*models.PlaybackResolution, error) {
			return &models.PlaybackResolution{
				QueueID:    queueID,
				WebDAVPath: "/debrid/torbox/torrent/file/2/Disc/BDMV/STREAM/00060.M2TS?token=test",
			}, nil
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/playback/queue/42", nil)
	req = mux.SetURLVars(req, map[string]string{"queueID": "42"})
	rec := httptest.NewRecorder()
	h.QueueStatus(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected status %d, got %d: %s", http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	}
}

func TestResolveBatch_MalformedJSON(t *testing.T) {
	h := NewPlaybackHandler(&mockPlaybackService{})
	req := httptest.NewRequest(http.MethodPost, "/api/playback/resolve-batch", bytes.NewBufferString(`{invalid`))
	rec := httptest.NewRecorder()
	h.ResolveBatch(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestResolveBatch_EmptyEpisodes(t *testing.T) {
	h := NewPlaybackHandler(&mockPlaybackService{})
	body, _ := json.Marshal(map[string]interface{}{
		"result":   models.NZBResult{Title: "Test"},
		"episodes": []models.BatchEpisodeTarget{},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/playback/resolve-batch", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	h.ResolveBatch(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestResolveBatch_OversizedBatch(t *testing.T) {
	h := NewPlaybackHandler(&mockPlaybackService{})
	episodes := make([]models.BatchEpisodeTarget, 101)
	for i := range episodes {
		episodes[i] = models.BatchEpisodeTarget{SeasonNumber: 1, EpisodeNumber: i + 1}
	}
	body, _ := json.Marshal(map[string]interface{}{
		"result":   models.NZBResult{Title: "Test"},
		"episodes": episodes,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/playback/resolve-batch", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	h.ResolveBatch(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestResolveBatch_Success(t *testing.T) {
	h := NewPlaybackHandler(&mockPlaybackService{})
	episodes := []models.BatchEpisodeTarget{
		{SeasonNumber: 1, EpisodeNumber: 1, EpisodeCode: "S01E01"},
		{SeasonNumber: 1, EpisodeNumber: 2, EpisodeCode: "S01E02"},
	}
	body, _ := json.Marshal(map[string]interface{}{
		"result":   models.NZBResult{Title: "Test", ServiceType: models.ServiceTypeDebrid},
		"episodes": episodes,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/playback/resolve-batch", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	h.ResolveBatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var resp models.BatchResolveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.Results))
	}

	for i, r := range resp.Results {
		if r.Resolution == nil {
			t.Errorf("result %d: nil resolution", i)
		} else if r.Resolution.WebDAVPath == "" {
			t.Errorf("result %d: empty webdav path", i)
		}
	}
}

type trackSelectionProber struct {
	calls int
}

func (p *trackSelectionProber) ProbeVideoFull(_ context.Context, _ string) (*VideoFullResult, error) {
	p.calls++
	return &VideoFullResult{
		AudioStreams: []AudioStreamInfo{
			{Index: 1, Codec: "ac3", Language: "eng"},
			{Index: 2, Codec: "eac3", Language: "jpn"},
		},
		SubtitleStreams: []SubtitleStreamInfo{
			{Index: 3, Codec: "hdmv_pgs_subtitle", Language: "eng"},
			{Index: 4, Codec: "subrip", Language: "eng"},
		},
	}, nil
}

type trackSelectionUserSettings struct{}

func (trackSelectionUserSettings) GetWithDefaults(userID string, defaults models.UserSettings) (models.UserSettings, error) {
	if userID == "profile-1" {
		defaults.Playback.PreferredAudioLanguage = "jpn"
		defaults.Playback.PreferredSubtitleLanguage = "eng"
		defaults.Playback.PreferredSubtitleMode = "on"
	}
	return defaults, nil
}

func TestResolve_IncludesTrackSelectionWhenRequested(t *testing.T) {
	h := NewPlaybackHandler(&mockPlaybackService{
		resolveFunc: func(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error) {
			return &models.PlaybackResolution{WebDAVPath: "/debrid/movie.mkv"}, nil
		},
	})
	prober := &trackSelectionProber{}
	h.SetVideoProber(prober)
	h.SetTrackPreferenceResolver(&trackselect.Resolver{UserSettings: trackSelectionUserSettings{}})

	body, _ := json.Marshal(map[string]interface{}{
		"result":                models.NZBResult{Title: "Movie"},
		"profileId":             "profile-1",
		"includeTrackSelection": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/playback/resolve", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	h.Resolve(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp models.PlaybackResolution
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	sel := resp.TrackSelection
	if sel == nil || sel.Audio == nil || sel.Audio.StreamIndex != 2 || sel.Audio.TypeIndex != 1 {
		t.Fatalf("audio selection = %#v", sel)
	}
	if sel.Subtitle == nil || sel.Subtitle.StreamIndex != 3 || !sel.Subtitle.Bitmap {
		t.Fatalf("subtitle selection = %#v", sel.Subtitle)
	}
	if sel.TextSubtitle == nil || sel.TextSubtitle.StreamIndex != 4 || sel.TextSubtitle.LanguageIndex != 1 {
		t.Fatalf("text subtitle selection = %#v", sel.TextSubtitle)
	}
	if prober.calls != 1 {
		t.Fatalf("probe calls = %d, want 1", prober.calls)
	}
}

func TestResolve_OmitsTrackSelectionByDefault(t *testing.T) {
	h := NewPlaybackHandler(&mockPlaybackService{})
	prober := &trackSelectionProber{}
	h.SetVideoProber(prober)

	body, _ := json.Marshal(map[string]interface{}{"result": models.NZBResult{Title: "Movie"}})
	req := httptest.NewRequest(http.MethodPost, "/api/playback/resolve", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	h.Resolve(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("trackSelection")) || prober.calls != 0 {
		t.Fatalf("unexpected track selection/probe: body=%s calls=%d", rec.Body.String(), prober.calls)
	}
}

func TestResolve_ReusesResolverProbeForTrackSelection(t *testing.T) {
	h := NewPlaybackHandler(&mockPlaybackService{
		resolveFunc: func(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error) {
			return &models.PlaybackResolution{
				WebDAVPath: "https://cdn.example/movie.mkv",
				Probe: &models.VideoFullResult{
					AudioStreams: []models.AudioStreamInfo{{Index: 1, Codec: "aac", Language: "eng"}},
				},
			}, nil
		},
	})
	prober := &trackSelectionProber{}
	h.SetVideoProber(prober)

	body, _ := json.Marshal(map[string]interface{}{"result": models.NZBResult{Title: "Movie"}, "includeTrackSelection": true})
	req := httptest.NewRequest(http.MethodPost, "/api/playback/resolve", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	h.Resolve(rec, req)

	var resp models.PlaybackResolution
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TrackSelection == nil || resp.TrackSelection.Audio == nil || resp.TrackSelection.Audio.StreamIndex != 1 {
		t.Fatalf("track selection = %#v", resp.TrackSelection)
	}
	if prober.calls != 0 {
		t.Fatalf("probe calls = %d, want resolver probe reuse", prober.calls)
	}
}

func TestResolve_IncludesProbedFrameRate(t *testing.T) {
	h := NewPlaybackHandler(&mockPlaybackService{
		resolveFunc: func(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error) {
			return &models.PlaybackResolution{
				WebDAVPath: "https://cdn.example/show.mkv",
				Probe: &models.VideoFullResult{
					AudioStreams: []models.AudioStreamInfo{{Index: 1, Codec: "ac3", Language: "fre"}},
					AvgFrameRate: "25/1",
				},
			}, nil
		},
	})
	h.SetVideoProber(&trackSelectionProber{})

	body, _ := json.Marshal(map[string]interface{}{"result": models.NZBResult{Title: "Show"}, "includeTrackSelection": true})
	req := httptest.NewRequest(http.MethodPost, "/api/playback/resolve", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	h.Resolve(rec, req)

	var resp models.PlaybackResolution
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.FrameRate != "25/1" {
		t.Fatalf("frameRate = %q, want 25/1", resp.FrameRate)
	}
}

func TestQueueStatus_IncludesTrackSelectionWhenRequested(t *testing.T) {
	h := NewPlaybackHandler(&mockPlaybackService{
		queueStatusFunc: func(ctx context.Context, queueID int64) (*models.PlaybackResolution, error) {
			return &models.PlaybackResolution{QueueID: queueID, WebDAVPath: "/usenet/movie.mkv"}, nil
		},
	})
	h.SetVideoProber(&trackSelectionProber{})
	h.SetTrackPreferenceResolver(&trackselect.Resolver{UserSettings: trackSelectionUserSettings{}})

	req := httptest.NewRequest(http.MethodGet, "/api/playback/queue/42?includeTrackSelection=1&profileId=profile-1", nil)
	req = mux.SetURLVars(req, map[string]string{"queueID": "42"})
	rec := httptest.NewRecorder()
	h.QueueStatus(rec, req)

	var resp models.PlaybackResolution
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TrackSelection == nil || resp.TrackSelection.AudioStreamIndex() != 2 || resp.TrackSelection.SubtitleStreamIndex() != 3 {
		t.Fatalf("track selection = %#v", resp.TrackSelection)
	}
}
