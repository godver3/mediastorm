package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"novastream/internal/auth"
	"novastream/internal/profileaccess"
	"novastream/models"
	"novastream/services/badstreams"
	"novastream/services/content_preferences"
	"novastream/services/playback"
	"novastream/services/user_settings"
)

type reportMetadataProber struct{}

func (reportMetadataProber) ProbeVideoMetadata(context.Context, string) (*VideoMetadataResult, error) {
	return &VideoMetadataResult{}, nil
}

func releaseReportFixture(t *testing.T) (*PrequeueHandler, *playback.PrequeueEntry, *badstreams.Service) {
	t.Helper()
	bad := models.NZBResult{Title: "Synthetic.Movie.Bad.1080p", Link: "https://example.invalid/bad", ServiceType: models.ServiceTypeDebrid, Indexer: "AIOStreams", Attributes: map[string]string{"resolution": "1080p", "languages": "German"}}
	good := models.NZBResult{Title: "Synthetic.Movie.Good.1080p", Link: "https://example.invalid/good", ServiceType: models.ServiceTypeDebrid, Indexer: "AIOStreams", Attributes: map[string]string{"resolution": "1080p", "languages": "German"}}
	h, catalog, _ := newFallbackWorker(t, &fallbackSearch{results: []models.NZBResult{bad, good}})
	catalog.empty = true
	settings, err := user_settings.NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.Update("profile", models.UserSettings{Playback: models.PlaybackSettings{PreferredAudioLanguage: "deu"}}); err != nil {
		t.Fatal(err)
	}
	h.userSettingsSvc = settings
	h.metadataProber = reportMetadataProber{}
	h.playbackSvc = &stubPlaybackService{resolve: func(_ context.Context, result models.NZBResult) (*models.PlaybackResolution, error) {
		path := "/webdav/good.mp4"
		if strings.Contains(result.Title, "Bad") {
			path = "/webdav/bad.mp4"
		}
		return &models.PlaybackResolution{WebDAVPath: path}, nil
	}}
	h.fullProber = fallbackFullProber{run: func(context.Context, string) (*VideoFullResult, error) {
		return &VideoFullResult{VideoCodec: "h264", VideoWidth: 1920, VideoHeight: 1080, Duration: 600, AudioStreams: []models.AudioStreamInfo{{Index: 1, Language: "eng", Codec: "aac"}, {Index: 2, Language: "deu", Codec: "aac"}, {Index: 3, Language: "deu", Codec: "aac"}}}, nil
	}}
	registry := badstreams.New(filepath.Join(t.TempDir(), "bad_streams.json"))
	h.SetBadStreamsService(registry)
	entry, _ := h.store.Create("tmdb:movie:123", "Synthetic Movie", "profile", "movie", 2024, nil, "manual")
	h.store.Update(entry.ID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusReady
		e.StreamPath = "/webdav/bad.mp4"
		e.SelectedResult = &bad
		e.Duration = 600
		e.SelectedAudioTrack = 2
		e.AudioTracks = []playback.AudioTrackInfo{{Index: 1, Language: "eng", Codec: "aac"}, {Index: 2, Language: "deu", Codec: "aac"}, {Index: 3, Language: "deu", Codec: "aac"}}
	})
	entry, _ = h.store.Get(entry.ID)
	return h, entry, registry
}

func reportRequest(t *testing.T, method, id string, body any, master bool) *http.Request {
	t.Helper()
	var data bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&data).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, "/api/playback/prequeue/"+id+"/report", &data)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Client-ID", "fixture-device")
	r = mux.SetURLVars(r, map[string]string{"prequeueID": id})
	ctx := context.WithValue(r.Context(), auth.ContextKeyAccountID, "account")
	ctx = context.WithValue(ctx, auth.ContextKeyIsMaster, master)
	return r.WithContext(ctx)
}

func waitReportedReplacement(t *testing.T, h *PrequeueHandler, id string) *playback.PrequeueEntry {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if entry, ok := h.store.Get(id); ok && (entry.Status == playback.PrequeueStatusReady || entry.Status == playback.PrequeueStatusFailed) {
			return entry
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("replacement did not finish within its test deadline")
	return nil
}

func TestReleaseReportAndRetryBindsCurrentSource(t *testing.T) {
	h, entry, registry := releaseReportFixture(t)
	body := releaseReportRequest{Reason: "missing_language", AudioLanguage: "deu", SourceVersion: sourceReportVersion(entry), Retry: true, StartOffset: 120, SkipHLS: true, ClientID: "fixture-device"}
	w := httptest.NewRecorder()
	h.ReportRelease(w, reportRequest(t, "POST", entry.ID, body, false))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Report         badstreams.Entry          `json:"report"`
		Replacement    playback.PrequeueResponse `json:"replacement"`
		ResumePosition float64                   `json:"resumePosition"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Report.Scope == nil || response.Report.Scope.ProfileID != "profile" || response.Report.Scope.AudioLanguage != "deu" || response.ResumePosition != 120 {
		t.Fatalf("wrong report scope/position: %+v", response)
	}
	replacement := waitReportedReplacement(t, h, response.Replacement.PrequeueID)
	if replacement.Status != playback.PrequeueStatusReady || replacement.StreamPath != "/webdav/good.mp4" || replacement.TitleID != entry.TitleID || replacement.UserID != entry.UserID {
		t.Fatalf("wrong replacement: %+v", replacement)
	}
	if _, ok := h.store.Get(entry.ID); !ok {
		t.Fatal("preparation removed the source still playing")
	}
	ctx := badstreams.PlaybackContext{ProfileID: "profile", TitleID: entry.TitleID, AudioLanguage: "eng"}
	if registry.MatchFor(*entry.SelectedResult, ctx) != nil {
		t.Fatal("English playback was blocked")
	}
	if err := h.validateReadyEntryForRequest(reportRequest(t, "GET", entry.ID, nil, false), entry); err == nil {
		t.Fatal("old ready source bypassed the new report")
	}
}

func TestReleaseReportRejectsForgedStaleAndForeignSelections(t *testing.T) {
	for _, mode := range []string{"stale selection", "foreign account", "forged source", "invalid track", "locked profile"} {
		t.Run(mode, func(t *testing.T) {
			h, entry, registry := releaseReportFixture(t)
			body := map[string]any{"reason": "missing_language", "audioLanguage": "deu", "sourceVersion": sourceReportVersion(entry)}
			if mode == "stale selection" {
				body["sourceVersion"] = "stale"
			}
			if mode == "forged source" {
				body["releaseName"] = "Unrelated source"
			}
			if mode == "invalid track" {
				body["reason"] = "audio_problem"
				body["audioTrackIndex"] = 99
			}
			r := reportRequest(t, "POST", entry.ID, body, false)
			if mode == "foreign account" {
				r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, "other-account"))
			}
			w := httptest.NewRecorder()
			if mode == "locked profile" {
				r = profileaccess.WithResourceGuard(r, func(w http.ResponseWriter, _ *http.Request, _ string) bool {
					http.Error(w, "profile locked", http.StatusForbidden)
					return false
				})
				profileaccess.Middleware(func(string, string) bool { return true })(http.HandlerFunc(h.ReportRelease)).ServeHTTP(w, r)
			} else {
				h.ReportRelease(w, r)
			}
			if w.Code < 400 || len(registry.List()) != 0 {
				t.Fatalf("invalid report changed state: status=%d entries=%d", w.Code, len(registry.List()))
			}
		})
	}
}

func TestWrongContentReportResetsReplacementPosition(t *testing.T) {
	h, entry, _ := releaseReportFixture(t)
	w := httptest.NewRecorder()
	h.ReportRelease(w, reportRequest(t, "POST", entry.ID, releaseReportRequest{Reason: "wrong_title", SourceVersion: sourceReportVersion(entry), Retry: true, StartOffset: 250, SkipHLS: true}, true))
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		ResumePosition float64                   `json:"resumePosition"`
		Report         badstreams.Entry          `json:"report"`
		Replacement    playback.PrequeueResponse `json:"replacement"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	if response.ResumePosition != 0 || response.Report.Scope.ProfileID != "" {
		t.Fatal("wrong-content report did not reset position or apply the authorized shared scope")
	}
	waitReportedReplacement(t, h, response.Replacement.PrequeueID)
}

func TestManualReleaseRetryIsTransientAndIdempotent(t *testing.T) {
	h, entry, registry := releaseReportFixture(t)
	body := releaseReportRequest{SourceVersion: sourceReportVersion(entry), StartOffset: 50, SkipHLS: true}
	var firstID string
	for attempt := 0; attempt < 2; attempt++ {
		w := httptest.NewRecorder()
		h.RetryRelease(w, reportRequest(t, "POST", entry.ID, body, false))
		if w.Code != 200 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var response struct {
			Replacement playback.PrequeueResponse `json:"replacement"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &response)
		if firstID != "" && response.Replacement.PrequeueID != firstID {
			t.Fatal("duplicate request started another worker")
		}
		firstID = response.Replacement.PrequeueID
	}
	got := waitReportedReplacement(t, h, firstID)
	if got.StreamPath != "/webdav/good.mp4" || len(registry.List()) != 0 {
		t.Fatal("manual retry repeated the same release or created a permanent report")
	}
}

func TestReportedAudioTrackRetainsHealthyAlternative(t *testing.T) {
	h, entry, _ := releaseReportFixture(t)
	w := httptest.NewRecorder()
	h.ReportRelease(w, reportRequest(t, "POST", entry.ID, releaseReportRequest{Reason: "audio_problem", SourceVersion: sourceReportVersion(entry), Retry: true, SkipHLS: true}, false))
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Replacement playback.PrequeueResponse `json:"replacement"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	got := waitReportedReplacement(t, h, response.Replacement.PrequeueID)
	if got.Status != playback.PrequeueStatusReady || got.SelectedAudioTrack != 3 || got.TrackSelection.Audio.TypeIndex != 2 || got.TrackSelection.Audio.LanguageIndex != 1 {
		t.Fatalf("healthy German track was not selected: audio=%d status=%s error=%s", got.SelectedAudioTrack, got.Status, got.Error)
	}
}

func TestLegacyListsAndAdministrativeWritesRespectReportScopes(t *testing.T) {
	h, entry, registry := releaseReportFixture(t)
	if _, err := registry.Report(badstreams.ReportRequest{Result: *entry.SelectedResult, Context: badstreams.PlaybackContext{ProfileID: "profile", TitleID: entry.TitleID, AudioLanguage: "deu"}, Reason: "missing_language", ReporterID: "profile"}); err != nil {
		t.Fatal(err)
	}
	api := NewBadStreamsHandler(registry)
	api.SetUsersService(h.users)
	w := httptest.NewRecorder()
	api.List(w, reportRequest(t, "GET", entry.ID, nil, false))
	var list badStreamsListResponse
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if list.Total != 0 {
		t.Fatal("legacy player list exposed conditional reports as universal blocks")
	}
	w = httptest.NewRecorder()
	api.Clear(w, reportRequest(t, "DELETE", entry.ID, nil, false))
	if w.Code != http.StatusForbidden || len(registry.List()) != 1 {
		t.Fatal("ordinary profile purged shared state")
	}
}

func TestReleaseReportUsesEffectiveRequestedLanguage(t *testing.T) {
	for _, requested := range []string{"fra", "spa"} {
		t.Run(requested, func(t *testing.T) {
			h, entry, registry := releaseReportFixture(t)
			if err := h.userSettingsSvc.Update("profile", models.UserSettings{Playback: models.PlaybackSettings{PreferredAudioLanguage: requested}}); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			h.ReleaseReportOptions(w, reportRequest(t, "GET", entry.ID, nil, false))
			var options struct {
				AudioLanguage string `json:"audioLanguage"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &options); err != nil {
				t.Fatal(err)
			}
			if options.AudioLanguage != requested {
				t.Fatalf("dialog language=%s want=%s", options.AudioLanguage, requested)
			}
			w = httptest.NewRecorder()
			h.ReportRelease(w, reportRequest(t, "POST", entry.ID, releaseReportRequest{Reason: "missing_language", AudioLanguage: requested, SourceVersion: sourceReportVersion(entry)}, false))
			if w.Code != 200 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if registry.MatchFor(*entry.SelectedResult, h.reportContextForPrequeue(entry.ID, "profile", "fixture-device", nil)) == nil {
				t.Fatal("requested language was not excluded")
			}
			// An intentional title-specific English choice overrides the profile language.
			preferences, err := content_preferences.NewService(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			h.contentPreferencesSvc = preferences
			if err := preferences.Set("profile", models.ContentPreference{ContentID: entry.TitleID, AudioLanguage: "eng"}); err != nil {
				t.Fatal(err)
			}
			ctx := h.reportContextForPrequeue(entry.ID, "profile", "fixture-device", nil)
			if ctx.AudioLanguage != "eng" || registry.MatchFor(*entry.SelectedResult, ctx) != nil {
				t.Fatal("English title override inherited the other language's block")
			}
			w = httptest.NewRecorder()
			h.ReportRelease(w, reportRequest(t, "POST", entry.ID, releaseReportRequest{Reason: "missing_language", AudioLanguage: requested, SourceVersion: sourceReportVersion(entry)}, false))
			if w.Code != http.StatusConflict {
				t.Fatal("stale language intent was accepted")
			}
		})
	}
}

func TestManualResolveSkipsReportedTrackInOriginalContainerOrder(t *testing.T) {
	h, entry, registry := releaseReportFixture(t)
	index := 2
	if _, err := registry.Report(badstreams.ReportRequest{Result: *entry.SelectedResult, Context: badstreams.PlaybackContext{ProfileID: "profile", TitleID: entry.TitleID, AudioTrackIndex: &index}, Reason: "audio_problem", ReporterID: "profile"}); err != nil {
		t.Fatal(err)
	}
	manual := &PlaybackHandler{}
	manual.BadStreams = registry
	manual.TrackPreferences = h.trackPreferenceResolver()
	probe, err := h.fullProber.ProbeVideoFull(context.Background(), entry.StreamPath)
	if err != nil {
		t.Fatal(err)
	}
	original := &models.PlaybackResolution{WebDAVPath: entry.StreamPath, Probe: probe}
	ctx := h.reportContextForPrequeue(entry.ID, "profile", "fixture-device", nil)
	got, err := manual.filterResolvedReportedAudio(reportRequest(t, "POST", entry.ID, nil, false), *entry.SelectedResult, original, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.TrackSelection.Audio.StreamIndex != 3 || got.TrackSelection.Audio.TypeIndex != 2 {
		t.Fatalf("wrong track descriptor: %+v", got.TrackSelection.Audio)
	}
	if original.TrackSelection != nil || len(probe.AudioStreams) != 3 {
		t.Fatal("cached probe was mutated")
	}
	probe.AudioStreams = probe.AudioStreams[:2]
	if _, err := manual.filterResolvedReportedAudio(reportRequest(t, "POST", entry.ID, nil, false), *entry.SelectedResult, original, ctx); err == nil {
		t.Fatal("last reported requested-language track silently became English")
	}
}

func TestOnlyOwnerCanUndoPersonalReport(t *testing.T) {
	h, entry, registry := releaseReportFixture(t)
	report, err := registry.Report(badstreams.ReportRequest{Result: *entry.SelectedResult, Context: badstreams.PlaybackContext{ProfileID: "profile", TitleID: entry.TitleID, AudioLanguage: "spa"}, Reason: "missing_language", ReporterID: "profile"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewBadStreamsHandler(registry)
	handler.SetUsersService(h.users)
	for _, account := range []string{"foreign-account", "account"} {
		r := reportRequest(t, "DELETE", entry.ID, nil, false)
		r.URL.RawQuery = "profileId=profile"
		r = mux.SetURLVars(r, map[string]string{"id": report.ID})
		r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, account))
		w := httptest.NewRecorder()
		handler.Delete(w, r)
		if account == "foreign-account" && (w.Code != 404 || len(registry.List()) != 1) {
			t.Fatal("foreign account removed the report")
		}
		if account == "account" && (w.Code != 200 || len(registry.List()) != 0) {
			t.Fatal("owner could not undo the report")
		}
	}
}

func TestReportedAudioReplacementUsesRealMediaProbe(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required for media integration")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is required for media integration")
	}
	file := filepath.Join(t.TempDir(), "synthetic-three-tracks.mkv")
	cmd := exec.CommandContext(t.Context(), ffmpeg, "-nostdin", "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=1920x1080:r=24", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "2", "-map", "0:v", "-map", "1:a", "-map", "1:a", "-map", "1:a", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", "-metadata:s:a:0", "language=eng", "-metadata:s:a:1", "language=deu", "-metadata:s:a:2", "language=deu", file)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate synthetic video: %v %s", err, output)
	}
	h, entry, _ := releaseReportFixture(t)
	video := &VideoHandler{ffprobePath: ffprobe, streamer: directCastTestProvider{directURL: file}, metadataCache: make(map[string]*cachedMetadataEntry)}
	h.fullProber = video
	h.metadataProber = video
	w := httptest.NewRecorder()
	h.ReportRelease(w, reportRequest(t, "POST", entry.ID, releaseReportRequest{Reason: "audio_problem", SourceVersion: sourceReportVersion(entry), Retry: true, SkipHLS: true}, false))
	if w.Code != 200 {
		t.Fatalf("report status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Replacement playback.PrequeueResponse `json:"replacement"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	got := waitReportedReplacement(t, h, response.Replacement.PrequeueID)
	if got.Status != playback.PrequeueStatusReady || got.SelectedAudioTrack != 3 || got.TrackSelection.Audio.TypeIndex != 2 || got.Duration < 1.9 {
		t.Fatalf("real media selection: status=%s audio=%d duration=%f error=%s", got.Status, got.SelectedAudioTrack, got.Duration, got.Error)
	}
}
