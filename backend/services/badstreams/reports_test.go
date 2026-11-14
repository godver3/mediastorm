package badstreams

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/models"
)

func reportFixture(t *testing.T) (*Service, ReportRequest) {
	t.Helper()
	service := New(filepath.Join(t.TempDir(), "reports.json"))
	request := ReportRequest{Result: models.NZBResult{Title: "Synthetic.Movie.1080p", ServiceType: models.ServiceTypeUsenet, Indexer: "Fixture indexer"}, Context: PlaybackContext{TitleID: "tmdb:movie:123", ProfileID: "profile-a", AudioLanguage: "deu"}, Reason: "missing_language", ReporterID: "profile-a"}
	return service, request
}

func TestReportLanguageDoesNotBlacklistEnglish(t *testing.T) {
	service, request := reportFixture(t)
	request.Shared = true
	entry, err := service.Report(request)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Scope == nil || entry.Scope.AudioLanguage != "deu" || entry.Scope.ProfileID != "" {
		t.Fatalf("wrong scope: %+v", entry.Scope)
	}
	for _, profile := range []string{"profile-a", "profile-b"} {
		ctx := request.Context
		ctx.ProfileID = profile
		if service.MatchFor(request.Result, ctx) == nil {
			t.Fatal("German request was not excluded")
		}
		ctx.AudioLanguage = "eng"
		if service.MatchFor(request.Result, ctx) != nil {
			t.Fatal("English request was blacklisted")
		}
	}
	if service.IsBad(request.Result) {
		t.Fatal("legacy callers interpreted a conditional report as global")
	}
	reloaded := New(service.path)
	if reloaded.MatchFor(request.Result, request.Context) == nil {
		t.Fatal("report did not survive restart")
	}
	if entry.SourcePath != "" {
		t.Fatal("report persisted a private source path")
	}
}

func TestPersonalReportAndSharedEpisodeMapping(t *testing.T) {
	service, request := reportFixture(t)
	request.Reason = "wrong_episode"
	request.Context.TitleID = "tmdb:tv:123"
	request.Context.Episode = &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 4}
	entry, err := service.Report(request)
	if err != nil {
		t.Fatal(err)
	}
	other := request.Context
	other.ProfileID = "profile-b"
	if service.MatchFor(request.Result, other) != nil {
		t.Fatal("personal report affected another profile")
	}
	if _, err := service.ShareReport(entry.ID); err != nil {
		t.Fatal(err)
	}
	if service.MatchFor(request.Result, other) == nil {
		t.Fatal("shared mapping did not apply to another profile")
	}
	other.Episode = &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 5}
	if service.MatchFor(request.Result, other) != nil {
		t.Fatal("one episode blacklisted the season pack")
	}
	other = request.Context
	other.TitleID = "tmdb:tv:456"
	if service.MatchFor(request.Result, other) != nil {
		t.Fatal("report affected another series")
	}
	if _, err := service.Report(request); err != nil {
		t.Fatal(err)
	}
	other = request.Context
	other.ProfileID = "profile-b"
	if service.MatchFor(request.Result, other) == nil {
		t.Fatal("repeated personal report narrowed an approved shared report")
	}
}

func TestTrackReportOnlyExcludesAffectedTrack(t *testing.T) {
	service, request := reportFixture(t)
	index := 2
	request.Reason = "audio_problem"
	request.Context.AudioTrackIndex = &index
	request.Shared = true
	if _, err := service.Report(request); err != nil {
		t.Fatal(err)
	}
	ctx := request.Context
	ctx.AudioTrackIndex = nil
	if service.MatchFor(request.Result, ctx) != nil {
		t.Fatal("track report blacklisted the whole file before probing")
	}
	ctx.AudioTrackIndex = &index
	if service.MatchFor(request.Result, ctx) == nil {
		t.Fatal("affected track not excluded")
	}
	other := 3
	ctx.AudioTrackIndex = &other
	if service.MatchFor(request.Result, ctx) != nil {
		t.Fatal("healthy alternate track was excluded")
	}
}

func TestContextualReportsCannotBecomeUniversal(t *testing.T) {
	for _, reason := range []string{"quality_problem", "device_problem", "source_unavailable"} {
		t.Run(reason, func(t *testing.T) {
			service, request := reportFixture(t)
			request.Reason = reason
			request.Shared = true
			request.Context.ClientID = "device-a"
			entry, err := service.Report(request)
			if err != nil {
				t.Fatal(err)
			}
			if entry.Scope.ProfileID != "profile-a" {
				t.Fatal("contextual report became universal")
			}
			if _, err := service.ShareReport(entry.ID); err == nil {
				t.Fatal("contextual report was promoted")
			}
			if service.MatchFor(request.Result, request.Context) == nil {
				t.Fatal("contextual report did not apply")
			}
			if reason == "source_unavailable" {
				service.mu.Lock()
				expired := time.Now().Add(-time.Minute)
				entry.ExpiresAt = &expired
				service.entries[entry.ID] = entry
				service.mu.Unlock()
				if service.MatchFor(request.Result, request.Context) != nil {
					t.Fatal("temporary outage became permanent")
				}
			}
		})
	}
}

func TestContentHashDistinguishesSameNamedFiles(t *testing.T) {
	service, request := reportFixture(t)
	request.Result.Attributes = map[string]string{"infoHash": strings.Repeat("a", 40)}
	if _, err := service.Report(request); err != nil {
		t.Fatal(err)
	}
	other := request.Result
	other.Attributes = map[string]string{"infoHash": strings.Repeat("b", 40)}
	if service.MatchFor(other, request.Context) != nil {
		t.Fatal("different content with the same name was blocked")
	}
	other = request.Result
	other.Indexer = "Another fixture indexer"
	if service.MatchFor(other, request.Context) == nil {
		t.Fatal("duplicate content from another indexer bypassed the report")
	}
}

func TestReportValidationAndRollback(t *testing.T) {
	for _, reason := range []string{"", "unsupported", "wrong_episode", "audio_problem", "device_problem"} {
		t.Run(reason, func(t *testing.T) {
			service, request := reportFixture(t)
			request.Reason = reason
			if _, err := service.Report(request); err == nil {
				t.Fatal("invalid report was accepted")
			}
			if len(service.List()) != 0 {
				t.Fatal("invalid report changed the registry")
			}
		})
	}
}

func TestLanguageReportFollowsRequestedLanguage(t *testing.T) {
	for _, requested := range []string{"fra", "spa", "eng", "jpn", "deu"} {
		t.Run(requested, func(t *testing.T) {
			service, request := reportFixture(t)
			request.Context.AudioLanguage = requested
			request.Shared = true
			if _, err := service.Report(request); err != nil {
				t.Fatal(err)
			}
			for _, playbackLanguage := range []string{"fra", "spa", "eng", "jpn", "deu"} {
				ctx := request.Context
				ctx.AudioLanguage = playbackLanguage
				if blocked := service.MatchFor(request.Result, ctx) != nil; blocked != (requested == playbackLanguage) {
					t.Fatalf("reported=%s requested=%s blocked=%v", requested, playbackLanguage, blocked)
				}
			}
		})
	}
}

func TestReportPersistenceFailureRollsBack(t *testing.T) {
	service, request := reportFixture(t)
	// A file in place of the parent directory forces an actual persistence failure.
	blocker := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocker, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	service.path = filepath.Join(blocker, "reports.json")
	if _, err := service.Report(request); err == nil {
		t.Fatal("persistence failure was ignored")
	}
	if len(service.List()) != 0 {
		t.Fatal("failed report changed the registry")
	}
}
