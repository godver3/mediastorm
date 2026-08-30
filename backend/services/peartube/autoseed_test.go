package peartube

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"novastream/config"
)

func TestLegacyAutoSeedEnvironmentCannotGrantConsent(t *testing.T) {
	resolved := resolve(config.PearTubeSettings{MigrationRequired: true}, func(key string) string {
		if key == "PEARTUBE_AUTOSEED" {
			return "true"
		}
		return ""
	})
	if resolved.ContributeWatchedMedia || resolved.EffectiveMode != config.PearTubeModeMigrationRequired {
		t.Fatalf("legacy environment granted contribution consent: %+v", resolved)
	}
}

func TestEntityKeyMirrorsCatalogEntityIDs(t *testing.T) {
	movie := EntityKey(ArchiveCoordinates{ContentKind: "movie", TMDBID: "603"})
	if movie != "movie:603" {
		t.Fatalf("movie key = %q", movie)
	}
	if got := catalogEntityKey("tmdb:movie:603"); got != movie {
		t.Fatalf("catalog key = %q, want %q", got, movie)
	}

	episode := EntityKey(ArchiveCoordinates{ContentKind: "episode", TMDBID: "1399", TMDBSeason: 1, TMDBEpisode: 2})
	if episode != "show:1399:s1:e2" {
		t.Fatalf("episode key = %q", episode)
	}
	if got := catalogEntityKey("tmdb:episode:show:1399:s1:e2"); got != episode {
		t.Fatalf("catalog key = %q, want %q", got, episode)
	}

	// Coordinates the relay could not publish have no key, so nothing can claim
	// or match them.
	for _, coords := range []ArchiveCoordinates{
		{ContentKind: "movie"},
		{ContentKind: "episode", TMDBID: "1399"},
		{ContentKind: "series", TMDBID: "1399"},
	} {
		if key := EntityKey(coords); key != "" {
			t.Fatalf("%+v has key %q", coords, key)
		}
	}
}

func newCatalogRelay(t *testing.T, body string, status int) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func TestCatalogHasEntityMatchesPublishedCoordinates(t *testing.T) {
	relay := newCatalogRelay(t, catalogBody, http.StatusOK)

	published, err := relay.CatalogHasEntity(t.Context(), ArchiveCoordinates{ContentKind: "movie", TMDBID: "603"})
	if err != nil {
		t.Fatalf("CatalogHasEntity: %v", err)
	}
	if !published {
		t.Fatal("The Matrix is in the catalog but reported as absent")
	}

	absent, err := relay.CatalogHasEntity(t.Context(), ArchiveCoordinates{ContentKind: "movie", TMDBID: "424"})
	if err != nil {
		t.Fatalf("CatalogHasEntity: %v", err)
	}
	if absent {
		t.Fatal("a title the catalog does not list reported as published")
	}

	// Listed, but with no rendition to address: the stream endpoint could not
	// serve it, which is exactly the gap a seed fills.
	unservable, err := relay.CatalogHasEntity(t.Context(), ArchiveCoordinates{ContentKind: "movie", TMDBID: "605"})
	if err != nil {
		t.Fatalf("CatalogHasEntity: %v", err)
	}
	if unservable {
		t.Fatal("an entity with no addressable source reported as published")
	}
}

func TestCatalogHasEntityUsesSourceCoordinatesForOpaqueEntities(t *testing.T) {
	body := `{"entities":[{
	  "entityId":"3f66949c3f1d9fead2b43da629a0c5d43ae74b4eb46f03a70f625bfecdb7fb33",
	  "title":"Game of Thrones",
	  "sources":[{
	    "publicationId":"pub-opaque",
	    "renditionId":"rend-opaque",
	    "contentKind":"episode",
	    "mediaProvider":"tmdb",
	    "mediaId":"1399",
	    "seasonNumber":1,
	    "episodeNumber":2
	  }]
	}]}`
	relay := newCatalogRelay(t, body, http.StatusOK)
	published, err := relay.CatalogHasEntity(t.Context(), ArchiveCoordinates{
		ContentKind: "episode", TMDBID: "1399", TMDBSeason: 1, TMDBEpisode: 2,
	})
	if err != nil {
		t.Fatalf("CatalogHasEntity: %v", err)
	}
	if !published {
		t.Fatal("opaque entity source coordinates were reported as absent")
	}
}

// A relay that cannot answer must not be read as "the swarm does not have this".
// Turning a catalog failure into an absence is what would make every playback
// re-seed the same file.
func TestCatalogHasEntityReportsAnUnavailableRelayAsAnError(t *testing.T) {
	relay := newCatalogRelay(t, gateBody, http.StatusForbidden)

	published, err := relay.CatalogHasEntity(t.Context(), ArchiveCoordinates{ContentKind: "movie", TMDBID: "603"})
	if err == nil {
		t.Fatal("a gated relay answered without an error")
	}
	if !IsRelayNotOpen(err) {
		t.Fatalf("error = %v, want the open-access gate", err)
	}
	if published {
		t.Fatal("a gated relay reported the title as published")
	}
}

// The same catalog read a search does, so a watch straight after a search costs
// no round trip — and a failed read is not retried on every heartbeat either.
func TestCatalogHasEntityReusesTheCachedCatalog(t *testing.T) {
	var reads int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, apiPrefix+"/catalog") {
			reads++
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(catalogBody))
	}))
	defer server.Close()
	relay, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for range 5 {
		if _, err := relay.CatalogHasEntity(t.Context(), ArchiveCoordinates{ContentKind: "movie", TMDBID: "603"}); err != nil {
			t.Fatalf("CatalogHasEntity: %v", err)
		}
	}
	if reads != 1 {
		t.Fatalf("catalog reads = %d, want 1", reads)
	}
}

func TestPlaybackObservationRequiresContinuousEvidenceAndDeduplicatesSource(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tracker := NewPlaybackObserver(PlaybackObserverConfig{
		MeaningfulWatchDuration: 20 * time.Second,
		MeaningfulWatchFraction: 0.05,
		MaxObservationGap:       15 * time.Second,
		EntryTTL:                time.Hour,
		Capacity:                8,
	})
	observe := func(playbackID, sourceID string, position time.Duration, advance time.Duration) PlaybackObservation {
		now = now.Add(advance)
		return tracker.Observe(PlaybackEvent{
			PlaybackID: playbackID,
			SourceID:   sourceID,
			Position:   position,
			Duration:   2 * time.Hour,
			ObservedAt: now,
		})
	}

	if got := observe("playback-1", "source-digest", 0, 0); got.State != PlaybackUnqualified {
		t.Fatalf("initial state = %q", got.State)
	}
	if got := observe("playback-1", "source-digest", 55*time.Minute, time.Second); got.State != PlaybackUnqualified {
		t.Fatalf("seek qualified playback: %+v", got)
	}
	if got := observe("playback-1", "source-digest", 55*time.Minute+10*time.Second, 30*time.Second); got.Accumulated != 0 {
		t.Fatalf("background gap accumulated evidence: %+v", got)
	}
	if got := observe("playback-1", "source-digest", 55*time.Minute+20*time.Second, 10*time.Second); got.State != PlaybackUnqualified {
		t.Fatalf("qualified too early: %+v", got)
	}
	qualified := observe("playback-1", "source-digest", 55*time.Minute+30*time.Second, 10*time.Second)
	if qualified.State != PlaybackQualified || !qualified.FirstQualified {
		t.Fatalf("meaningful watch did not qualify exactly once: %+v", qualified)
	}
	if repeated := observe("playback-1", "source-digest", 55*time.Minute+40*time.Second, 10*time.Second); repeated.FirstQualified {
		t.Fatalf("duplicate observation re-qualified: %+v", repeated)
	}
	if restarted := observe("playback-2", "source-digest", 0, time.Second); restarted.State != PlaybackQualified || restarted.FirstQualified {
		t.Fatalf("source qualified again after playback restart: %+v", restarted)
	}
}

func TestPlaybackObservationIgnoresNoiseAndCancelsOnlyOwningPlayback(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tracker := NewPlaybackObserver(PlaybackObserverConfig{
		MeaningfulWatchDuration: 10 * time.Second,
		MeaningfulWatchFraction: 0.05,
		MaxObservationGap:       15 * time.Second,
		EntryTTL:                time.Hour,
		Capacity:                2,
	})
	observe := func(event PlaybackEvent, advance time.Duration) PlaybackObservation {
		now = now.Add(advance)
		event.ObservedAt = now
		return tracker.Observe(event)
	}
	base := PlaybackEvent{PlaybackID: "playback-1", SourceID: "source-1", Duration: time.Hour}
	observe(base, 0)
	paused := base
	paused.Position = 5 * time.Second
	paused.Paused = true
	if got := observe(paused, 5*time.Second); got.Accumulated != 0 {
		t.Fatalf("paused noise accumulated: %+v", got)
	}
	outOfOrder := base
	outOfOrder.Position = 4 * time.Second
	if got := observe(outOfOrder, time.Second); got.Accumulated != 0 {
		t.Fatalf("out-of-order event accumulated: %+v", got)
	}
	first := base
	first.Position = 10 * time.Second
	observe(first, 5*time.Second)
	second := base
	second.Position = 15 * time.Second
	if got := observe(second, 5*time.Second); got.State != PlaybackQualified {
		t.Fatalf("continuous evidence did not qualify: %+v", got)
	}
	abandoned := second
	abandoned.Abandoned = true
	if got := observe(abandoned, time.Second); got.State != PlaybackCancelled || !got.FirstCancelled {
		t.Fatalf("qualified playback was not cancelled once: %+v", got)
	}
	if got := observe(abandoned, time.Second); got.FirstCancelled {
		t.Fatalf("abandonment cancelled twice: %+v", got)
	}
	other := PlaybackEvent{PlaybackID: "playback-2", SourceID: "source-2", Abandoned: true}
	if got := observe(other, time.Second); got.State != PlaybackCancelled || !got.FirstCancelled {
		t.Fatalf("unqualified abandonment state = %+v", got)
	}
}

// A source qualifies once per TTL, which is what stops one title becoming one
// submission per heartbeat. But an attempt that fails for a reason gone seconds
// later — a debrid link not yet unrestricted, a relay still starting — must be
// reachable again, and shortening the swarm-key claim alone cannot achieve that:
// no later heartbeat qualifies, so nothing ever reaches the attempt. Observed
// live: Thor Ragnarok qualified on its first heartbeat a moment before its link
// was ready, the claim was shortened to two minutes, and five minutes later
// nothing had retried.
func TestForgettingAQualifiedSourceLetsALaterHeartbeatQualifyItAgain(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tracker := NewPlaybackObserver(PlaybackObserverConfig{
		MeaningfulWatchDuration: 10 * time.Second,
		MeaningfulWatchFraction: 0.05,
		MaxObservationGap:       15 * time.Second,
		EntryTTL:                6 * time.Hour,
		Capacity:                8,
	})
	observe := func(playbackID string, position, advance time.Duration) PlaybackObservation {
		now = now.Add(advance)
		return tracker.Observe(PlaybackEvent{
			PlaybackID: playbackID,
			SourceID:   "source-digest",
			Position:   position,
			Duration:   2 * time.Hour,
			ObservedAt: now,
		})
	}

	observe("playback-1", 0, 0)
	first := observe("playback-1", 10*time.Second, 10*time.Second)
	if !first.FirstQualified {
		t.Fatalf("continuous watching did not qualify: %+v", first)
	}

	// The second attempt is the one the six-hour ledger refuses.
	observe("playback-2", 0, time.Second)
	blocked := observe("playback-2", 10*time.Second, 10*time.Second)
	if blocked.FirstQualified {
		t.Fatal("a source qualified twice inside its TTL, so one title can submit per heartbeat")
	}

	tracker.ForgetQualifiedSource("source-digest")

	observe("playback-3", 0, time.Second)
	revived := observe("playback-3", 10*time.Second, 10*time.Second)
	if !revived.FirstQualified {
		t.Fatalf("a forgotten source did not qualify again, so a transient failure is still terminal for six hours: %+v", revived)
	}

	// Forgetting one source must not open the floodgates for it either: the
	// ledger is back in force immediately afterwards.
	observe("playback-4", 0, time.Second)
	if again := observe("playback-4", 10*time.Second, 10*time.Second); again.FirstQualified {
		t.Fatal("qualification stayed open after being used, so the ledger no longer bounds submissions")
	}

	// An empty source id is a no-op rather than a panic or a wildcard.
	tracker.ForgetQualifiedSource("")
	observe("playback-5", 0, time.Second)
	if got := observe("playback-5", 10*time.Second, 10*time.Second); got.FirstQualified {
		t.Fatal("forgetting an empty source id cleared a real one")
	}
}
