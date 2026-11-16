package handlers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"novastream/models"
	"novastream/services/playback"
)

type recordingMagnetReleaser struct {
	mu    sync.Mutex
	paths []string
}

func (r *recordingMagnetReleaser) ReleaseStreamPath(streamPath, _ string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, streamPath)
}

func (r *recordingMagnetReleaser) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...)
}

// failingPathProber rejects one resolved path so its candidate is discarded
// after resolution.
type failingPathProber struct{ failPath string }

func (p *failingPathProber) ProbeVideoFull(_ context.Context, path string) (*VideoFullResult, error) {
	if path == p.failPath {
		return nil, errors.New("no video stream")
	}
	return &VideoFullResult{VideoCodec: "h264", Duration: 100}, nil
}

func TestResolveCandidatesReleasesResolvedButUnusedCandidates(t *testing.T) {
	paths := map[string]string{
		"probe-fails": "/debrid/alldebrid/11/file/1/a.mkv",
		"winner":      "/debrid/alldebrid/22/file/1/b.mkv",
	}
	playbackSvc := &stubPlaybackService{
		resolve: func(_ context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error) {
			if candidate.Title == "resolve-fails" {
				return nil, errors.New("not cached")
			}
			return &models.PlaybackResolution{WebDAVPath: paths[candidate.Title], HealthStatus: "cached"}, nil
		},
	}
	releaser := &recordingMagnetReleaser{}
	handler := &PrequeueHandler{
		store:          playback.NewPrequeueStore(time.Minute),
		playbackSvc:    playbackSvc,
		fullProber:     &failingPathProber{failPath: paths["probe-fails"]},
		magnetReleaser: releaser,
	}

	choice, err := handler.resolveCandidates(context.Background(), "prequeue-release", newSliceCandidateSource([]models.NZBResult{
		{Title: "resolve-fails", ServiceType: models.ServiceTypeDebrid},
		{Title: "probe-fails", ServiceType: models.ServiceTypeDebrid},
		{Title: "winner", ServiceType: models.ServiceTypeDebrid},
	}), prequeueResolutionOptions{
		mediaType:          "movie",
		hdrDVPolicy:        models.HDRDVPolicyIncludeHDRDV,
		unknownTrackPolicy: "none",
	})
	if err != nil {
		t.Fatalf("resolveCandidates: %v", err)
	}
	if choice.resolution == nil || choice.resolution.WebDAVPath != paths["winner"] {
		t.Fatalf("winner = %+v, want %s", choice.resolution, paths["winner"])
	}
	got := releaser.got()
	if len(got) != 1 || got[0] != paths["probe-fails"] {
		t.Fatalf("released = %v, want only the candidate that failed its probe", got)
	}
}
