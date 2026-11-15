package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"novastream/models"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNZBTrackProbeFetchUsesPolicyGateway(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/fetch" || r.Method != "POST" {
			t.Error("track probe bypassed policy route")
		}
		w.Header().Set("X-NZB-Filename", "probe.nzb")
		_, _ = w.Write([]byte("<nzb/>"))
	}))
	defer server.Close()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte(strings.Repeat("x", 48)), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NZB_GATEWAY_URL", server.URL)
	t.Setenv("NZB_GATEWAY_TOKEN_FILE", token)
	data, name, err := (&usenetTrackProber{}).fetchNZB(context.Background(), "https://indexer.invalid/file", "", models.NZBResult{Title: "Fixture", GUID: "release"})
	if err != nil || calls != 1 || string(data) != "<nzb/>" || name != "probe.nzb" {
		t.Fatalf("probe acquisition failed: %v", err)
	}
}

func TestNZBContentIdentityExcludesProfileAndDistinguishesEpisodes(t *testing.T) {
	first := models.NZBResult{}
	second := models.NZBResult{}
	annotateResultNZBContent(&first, "tmdb:42", "series", &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 2})
	annotateResultNZBContent(&second, "tmdb:42", "series", &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 2})
	annotateResultProfile(&first, "first-user")
	annotateResultProfile(&second, "second-user")
	if first.Attributes["nzbContentKey"] != second.Attributes["nzbContentKey"] {
		t.Fatal("profile changed the download budget identity")
	}
	annotateResultNZBContent(&second, "tmdb:42", "series", &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 3})
	if first.Attributes["nzbContentKey"] == second.Attributes["nzbContentKey"] {
		t.Fatal("different episodes share a title budget")
	}
}
