package badstreams

import (
	"path/filepath"
	"testing"
	"time"

	"novastream/models"
)

func TestReportRevisionFollowsScopeRemovalAndExpiry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reports.json")
	svc := New(path)
	ctx := PlaybackContext{ProfileID: "profile", ClientID: "tv", TitleID: "tmdb:movie:1", AudioLanguage: "fra"}
	initial := svc.RevisionFor(ctx)
	req := ReportRequest{Result: models.NZBResult{Title: "Synthetic.Movie.1080p", Indexer: "Fixture", ServiceType: models.ServiceTypeUsenet}, Context: ctx, Reason: "missing_language", ReporterID: "profile", Shared: true}
	report, err := svc.Report(req)
	if err != nil {
		t.Fatal(err)
	}
	changed := svc.RevisionFor(ctx)
	if changed == initial || New(path).RevisionFor(ctx) != changed {
		t.Fatal("revision did not change or survive restart")
	}
	if _, err := svc.Report(req); err != nil || svc.RevisionFor(ctx) != changed {
		t.Fatal("repeated permanent report invalidated identity")
	}
	english := ctx
	english.AudioLanguage = "eng"
	if svc.RevisionFor(english) != initial {
		t.Fatal("French report changed English title identity")
	}
	other := ctx
	other.ProfileID = "another-profile"
	if svc.RevisionFor(other) != changed {
		t.Fatal("shared report missing for another profile")
	}
	if !svc.Delete(report.ID) || svc.RevisionFor(ctx) != initial {
		t.Fatal("undo did not restore identity")
	}
	req.Reason, req.Shared = "source_unavailable", false
	report, err = svc.Report(req)
	if err != nil || svc.RevisionFor(ctx) == initial {
		t.Fatal("temporary route did not affect identity")
	}
	if svc.RevisionFor(other) != initial {
		t.Fatal("private route report affected another profile")
	}
	svc.mu.Lock()
	entry := svc.entries[report.ID]
	past := time.Now().Add(-time.Minute)
	entry.ExpiresAt = &past
	svc.entries[report.ID] = entry
	svc.mu.Unlock()
	if svc.RevisionFor(ctx) != initial {
		t.Fatal("expired route kept the old identity")
	}
}

func TestReportRevisionKeepsEpisodeAndDeviceBoundaries(t *testing.T) {
	svc := New(filepath.Join(t.TempDir(), "reports.json"))
	ctx := PlaybackContext{ProfileID: "profile", ClientID: "tv", TitleID: "tmdb:series:1"}
	initial := svc.RevisionFor(ctx)
	episode := ctx
	episode.Episode = &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 2}
	req := ReportRequest{Result: models.NZBResult{Title: "Synthetic.Series.S01E02"}, Context: episode, Reason: "wrong_episode", ReporterID: "profile", Shared: true}
	if _, err := svc.Report(req); err != nil {
		t.Fatal(err)
	}
	if svc.RevisionFor(ctx) != initial || svc.RevisionFor(episode) == initial {
		t.Fatal("episode revision became a whole-series exclusion")
	}
	req.Context, req.Reason, req.Shared = ctx, "device_problem", false
	if _, err := svc.Report(req); err != nil {
		t.Fatal(err)
	}
	phone := ctx
	phone.ClientID = "phone"
	if svc.RevisionFor(ctx) == initial || svc.RevisionFor(phone) != initial {
		t.Fatal("device revision leaked")
	}
}
