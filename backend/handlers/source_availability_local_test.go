package handlers

import (
	"context"
	"errors"
	"novastream/models"
	"novastream/services/badstreams"
	"novastream/services/localmedia"
	"novastream/services/sourceavailability"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type hintAccounts map[string]models.Account

func (a hintAccounts) Get(id string) (models.Account, bool) { v, ok := a[id]; return v, ok }

type hintCatalog struct{ *fallbackCatalog }

func (c hintCatalog) GetItem(ctx context.Context, id string) (*models.LocalMediaItem, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if id != c.item.ID {
		return nil, errors.New("missing synthetic item")
	}
	copy := c.item
	return &copy, nil
}
func localCheckerFixture(t *testing.T) (*PrequeueHandler, *fallbackCatalog, *fallbackAccess, *catalogueLocalChecker, sourceavailability.LocalRequest) {
	t.Helper()
	h, catalog, access := newFallbackWorker(t, &fallbackSearch{})
	h.localFallback = hintCatalog{catalog}
	h.fullProber = fallbackFullProber{run: func(context.Context, string) (*VideoFullResult, error) {
		return &VideoFullResult{VideoCodec: "h264", VideoWidth: 1920, VideoHeight: 1080, Duration: 100, AudioStreams: []models.AudioStreamInfo{{Index: 1, Language: "eng", Codec: "aac"}, {Index: 2, Language: "fra", Codec: "aac"}, {Index: 3, Language: "spa", Codec: "aac"}, {Index: 4, Language: "jpn", Codec: "aac"}, {Index: 5, Language: "deu", Codec: "aac"}}}, nil
	}}
	checker := NewCatalogueLocalChecker(h, hintAccounts{"account": {ID: "account"}}).(*catalogueLocalChecker)
	request := sourceavailability.LocalRequest{ProfileID: "profile", ClientID: "tv", AudioLanguage: "fra", Title: models.Title{ID: catalog.item.MatchedTitleID, Name: "Synthetic Movie", MediaType: "movie"}}
	return h, catalog, access, checker, request
}
func TestCatalogueLocalProofSupportsFiveRequestedLanguages(t *testing.T) {
	for _, lang := range []string{"fra", "spa", "eng", "jpn", "deu"} {
		t.Run(lang, func(t *testing.T) {
			_, _, _, checker, r := localCheckerFixture(t)
			r.AudioLanguage = lang
			p, err := checker.Check(t.Context(), r)
			if err != nil || p.ItemID == "" || !checker.Validate(t.Context(), r, p) {
				t.Fatalf("language %s: %v", lang, err)
			}
		})
	}
}
func TestCatalogueLocalProofRechecksRightsMatchAndPreferences(t *testing.T) {
	for _, mode := range []string{"rights", "match", "missing", "quality", "audio"} {
		t.Run(mode, func(t *testing.T) {
			h, catalog, access, c, r := localCheckerFixture(t)
			p, err := c.Check(t.Context(), r)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "rights":
				access.deny.Store(true)
			case "match":
				catalog.item.MatchedTitleID = "another-title"
			case "missing":
				catalog.item.IsMissing = true
			case "quality":
				cfg, _ := h.configManager.Load()
				cfg.Filtering.MaxResolution = "720p"
				if err := h.configManager.Save(cfg); err != nil {
					t.Fatal(err)
				}
			case "audio":
				r.AudioLanguage = "eng"
			}
			if c.Validate(t.Context(), r, p) {
				t.Fatalf("changed %s reused proof", mode)
			}
		})
	}
}
func TestCatalogueLocalAbsenceRechecksNewFilesAndGrantedLibraries(t *testing.T) {
	_, catalog, access, c, r := localCheckerFixture(t)
	catalog.empty = true
	p, err := c.Check(t.Context(), r)
	if err != nil || p.ItemID != "" || !c.Validate(t.Context(), r, p) {
		t.Fatal("empty catalogue not confirmed")
	}
	catalog.empty = false
	if c.Validate(t.Context(), r, p) {
		t.Fatal("new local item reused old absence")
	}
	access.deny.Store(true)
	p, err = c.Check(t.Context(), r)
	if err != nil || p.ItemID != "" {
		t.Fatal("denied library entered proof")
	}
	access.deny.Store(false)
	if c.Validate(t.Context(), r, p) {
		t.Fatal("new library grant reused old absence")
	}
}
func TestCatalogueLocalCandidateNeedsActualVideoAndRequestedAudio(t *testing.T) {
	for _, mode := range []string{"file missing", "audio absent", "wrong audio", "commentary only", "quality exceeds", "probe error"} {
		t.Run(mode, func(t *testing.T) {
			h, catalog, _, c, r := localCheckerFixture(t)
			catalog.unavailable = mode == "file missing"
			h.fullProber = fallbackFullProber{run: func(context.Context, string) (*VideoFullResult, error) {
				if mode == "probe error" {
					return nil, errors.New("synthetic probe error")
				}
				probe := &VideoFullResult{VideoCodec: "h264", VideoWidth: 1920, VideoHeight: 1080, Duration: 100, AudioStreams: []models.AudioStreamInfo{{Index: 1, Language: "fra", Codec: "aac"}}}
				switch mode {
				case "audio absent":
					probe.AudioStreams = nil
				case "wrong audio":
					probe.AudioStreams[0].Language = "eng"
				case "commentary only":
					probe.AudioStreams[0].Title = "Director commentary"
				case "quality exceeds":
					probe.VideoHeight = 2160
					probe.VideoWidth = 3840
				}
				return probe, nil
			}}
			cfg, _ := h.configManager.Load()
			cfg.Filtering.MaxResolution = "1080p"
			if err := h.configManager.Save(cfg); err != nil {
				t.Fatal(err)
			}
			p, err := c.Check(t.Context(), r)
			if err == nil || p.ItemID != "" {
				t.Fatalf("unverified %s became positive", mode)
			}
		})
	}
}
func TestCatalogueLocalProofHonorsScopedReleaseAndTrackReports(t *testing.T) {
	h, catalog, _, c, r := localCheckerFixture(t)
	registry := badstreams.New(filepath.Join(t.TempDir(), "reports.json"))
	h.badStreamsSvc = registry
	index := 2
	result := models.NZBResult{Title: catalog.item.FileName, Link: localmedia.BuildStreamPath(catalog.item), ServiceType: models.ContentServiceType("local"), Attributes: map[string]string{"localLibraryId": catalog.item.LibraryID, "profileId": "profile"}}
	report, err := registry.Report(badstreams.ReportRequest{Result: result, Context: badstreams.PlaybackContext{ProfileID: "profile", ClientID: "tv", TitleID: r.Title.ID, AudioLanguage: "fra", AudioTrackIndex: &index}, Reason: "audio_problem", ReporterID: "profile"})
	if err != nil {
		t.Fatal(err)
	}
	if p, err := c.Check(t.Context(), r); err == nil || p.ItemID != "" {
		t.Fatal("reported French track remained a positive proof")
	}
	r.AudioLanguage = "eng"
	if p, err := c.Check(t.Context(), r); err != nil || p.ItemID == "" {
		t.Fatal("French track report excluded healthy English")
	}
	registry.Delete(report.ID)
	r.AudioLanguage = "fra"
	if p, err := c.Check(t.Context(), r); err != nil || p.ItemID == "" {
		t.Fatal("undo failed")
	}
}
func TestCatalogueLocalProofRequiresCurrentAccountAndExactEpisodeContext(t *testing.T) {
	_, _, _, c, r := localCheckerFixture(t)
	expired := time.Now().Add(-time.Hour)
	c.accounts = hintAccounts{"account": {ID: "account", ExpiresAt: &expired}}
	if _, err := c.Check(t.Context(), r); err == nil {
		t.Fatal("expired account accepted")
	}
	c.accounts = hintAccounts{"account": {ID: "account"}}
	r.Title.MediaType = "series"
	if _, err := c.Check(t.Context(), r); err == nil {
		t.Fatal("one local episode qualified a whole series")
	}
	r.Episode = &models.SeriesEpisode{ID: "ep", SeasonNumber: 1, EpisodeNumber: 1, Numbering: &models.EpisodeNumbering{SeriesID: r.Title.ID, Ordering: "dvd"}}
	if _, err := c.Check(t.Context(), r); err == nil {
		t.Fatal("unverified alternative numbering accepted")
	}
}
func TestCatalogueLocalProofCancellationBoundsProbeWait(t *testing.T) {
	h, _, _, c, r := localCheckerFixture(t)
	h.fullProber = fallbackFullProber{run: func(ctx context.Context, _ string) (*VideoFullResult, error) { <-ctx.Done(); return nil, ctx.Err() }}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	if p, err := c.Check(ctx, r); err == nil || p.ItemID != "" || time.Since(started) > time.Second {
		t.Fatal("canceled local probe was unbounded or positive")
	}
}
func TestCatalogueLocalProofUsesRealMediaProbeWithoutPlayback(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg required")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe required")
	}
	file := filepath.Join(t.TempDir(), "synthetic.mkv")
	cmd := exec.CommandContext(t.Context(), ffmpeg, "-nostdin", "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=320x180:r=24", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-f", "lavfi", "-i", "sine=frequency=660:sample_rate=48000", "-t", "2", "-map", "0:v", "-map", "1:a", "-map", "2:a", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", "-metadata:s:a:0", "language=eng", "-metadata:s:a:1", "language=fra", file)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate synthetic media: %v %s", err, out)
	}
	h, catalog, _, c, r := localCheckerFixture(t)
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	catalog.item.SizeBytes = info.Size()
	manager := &HLSManager{probeCache: make(map[string]*cachedProbeEntry)}
	manager.CacheProbe(localmedia.BuildStreamPath(catalog.item), &UnifiedProbeResult{VideoCodec: "h264", VideoWidth: 320, VideoHeight: 180, Duration: 2, AudioStreams: []audioStreamInfo{{Index: 1, Language: "spa", Codec: "aac"}}})
	h.fullProber = &VideoHandler{ffprobePath: ffprobe, streamer: directCastTestProvider{directURL: file}, metadataCache: make(map[string]*cachedMetadataEntry), hlsManager: manager}
	if p, err := c.Check(t.Context(), r); err != nil || p.ItemID == "" {
		t.Fatalf("real local French audio proof failed: %v", err)
	}
	r.AudioLanguage = "spa"
	if p, err := c.Check(t.Context(), r); err == nil || p.ItemID != "" {
		t.Fatal("missing Spanish track qualified local file")
	}
}

type capturedAvailabilityPreparation struct {
	availabilityPresentationFake
	ready chan bool
}

func (c *capturedAvailabilityPreparation) ObservePlaybackStart(string, string, string, string) func(bool) {
	return func(prepared bool) { c.ready <- prepared }
}
func TestLocalPreparationNeverEntersRemoteSourceObservation(t *testing.T) {
	for _, local := range []bool{true, false} {
		t.Run(map[bool]string{true: "local", false: "remote"}[local], func(t *testing.T) {
			search := &fallbackSearch{results: []models.NZBResult{{Title: "Synthetic.Remote.1080p", Link: "https://example.invalid/synthetic", ServiceType: models.ServiceTypeDebrid, Indexer: "AIOStreams"}}}
			h, _, _ := newFallbackWorker(t, search)
			cfg, _ := h.configManager.Load()
			cfg.Playback.PreferLocalFiles = local
			if err := h.configManager.Save(cfg); err != nil {
				t.Fatal(err)
			}
			captured := &capturedAvailabilityPreparation{ready: make(chan bool, 1)}
			h.sourceAvailability = captured
			runFallbackWorker(t, h)
			select {
			case prepared := <-captured.ready:
				if prepared == local {
					t.Fatal("local/remote preparation evidence was conflated")
				}
			case <-time.After(time.Second):
				t.Fatal("availability observation did not complete")
			}
		})
	}
}
