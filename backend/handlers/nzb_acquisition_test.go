package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/nzbfetch"
	"novastream/models"
	"novastream/services/playback"
	"novastream/services/usenet"
)

type deniedNZBBudget struct{ keys []string }

func (s *deniedNZBBudget) ReserveNZB(_ context.Context, title, provider string, _ time.Time) (time.Time, error) {
	s.keys = append(s.keys, title+"/"+provider)
	return time.Now().Add(time.Minute), nil
}

func TestNZBAcquisitionIdentityOnResolveHealthAndTrackProbe(t *testing.T) {
	store := &deniedNZBBudget{}
	original := nzbfetch.Shared
	nzbfetch.Shared = nzbfetch.New(store)
	t.Cleanup(func() { nzbfetch.Shared = original })
	settings := config.DefaultSettings()
	settings.Usenet = []config.UsenetSettings{{Name: "Fixture", Host: "fixture.invalid", Port: 563, Enabled: true}}
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(settings); err != nil {
		t.Fatal(err)
	}
	candidate := models.NZBResult{Title: "Blade.Runner.2049.2017", Indexer: "Fixture", DownloadURL: "https://fixture.invalid/nzb"}.WithNZBIdentity(models.NZBIdentity{CatalogID: "tmdb:movie:335984", MediaType: "movie"})
	// Round-trip the existing result shape, as web and native clients do.
	body, err := json.Marshal(map[string]any{"result": candidate})
	if err != nil {
		t.Fatal(err)
	}
	resolve := NewPlaybackHandler(playback.NewService(mgr, nil, nil))
	recorder := httptest.NewRecorder()
	resolve.Resolve(recorder, httptest.NewRequest("POST", "/api/playback/resolve", bytes.NewReader(body)))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("resolve status: %d", recorder.Code)
	}
	health := &UsenetHandler{Service: usenet.NewService(mgr, nil)}
	recorder = httptest.NewRecorder()
	health.CheckHealth(recorder, httptest.NewRequest("POST", "/api/usenet/health", bytes.NewReader(body)))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("health status: %d", recorder.Code)
	}
	prober := &usenetTrackProber{httpClient: &http.Client{}}
	if _, _, err := prober.fetchNZB(t.Context(), candidate.DownloadURL, "", candidate); err == nil {
		t.Fatal("probe bypassed budget")
	}
	if len(store.keys) != 3 {
		t.Fatalf("expected 3 paths, got %v", store.keys)
	}
	for _, key := range store.keys {
		// The second part is the release: indexer plus hashed download URL.
		if !strings.HasPrefix(key, "tmdb:movie:335984/fixture|") || key != store.keys[0] {
			t.Errorf("inconsistent identity: %s", key)
		}
	}
}
