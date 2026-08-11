package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"novastream/models"
	"novastream/services/peartube"
)

// autoSeedRelay is a relay that serves a catalog and counts seed submissions,
// which is exactly what an automatic seed exercises: read the catalog, then
// submit only what the swarm is missing.
type autoSeedRelay struct {
	// catalog is the entities array served at GET /api/v1/catalog. An empty
	// string means the relay carries nothing.
	catalog string
	// catalogStatus and catalogBody override the catalog answer, so a gated or
	// broken relay can be simulated.
	catalogStatus int
	catalogBody   string
	// archiveStatus overrides the seed answer, so a refusal can be simulated.
	archiveStatus int
	// archiveDelay holds the seed submission open, so a caller that waits on it
	// is visible as a caller that waits.
	archiveDelay time.Duration

	mu           sync.Mutex
	catalogReads int
	archives     []map[string]any
}

func (relay *autoSeedRelay) archiveCount() int {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	return len(relay.archives)
}

func (relay *autoSeedRelay) lastArchive() map[string]any {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if len(relay.archives) == 0 {
		return nil
	}
	return relay.archives[len(relay.archives)-1]
}

func (relay *autoSeedRelay) client(t *testing.T) *peartube.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/catalog"):
			relay.mu.Lock()
			relay.catalogReads++
			relay.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if relay.catalogStatus != 0 {
				w.WriteHeader(relay.catalogStatus)
				_, _ = w.Write([]byte(relay.catalogBody))
				return
			}
			_, _ = w.Write([]byte(`{"entities":[` + relay.catalog + `],"nextCursor":null}`))

		case strings.HasPrefix(r.URL.Path, "/api/v1/archive"):
			if relay.archiveDelay > 0 {
				time.Sleep(relay.archiveDelay)
			}
			body := map[string]any{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			relay.mu.Lock()
			relay.archives = append(relay.archives, body)
			relay.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if relay.archiveStatus != 0 {
				w.WriteHeader(relay.archiveStatus)
				_, _ = w.Write([]byte(`{"error":{"code":"INTERNAL","message":"relay exploded","field":null}}`))
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"jobId":"arch_1","status":"queued","entityHint":"movie:603"}`))

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client, err := peartube.New(server.URL)
	if err != nil {
		t.Fatalf("peartube.New: %v", err)
	}
	return client
}

// newAutoSeedHandler builds the handler as main.go wires it, with the switch on
// and a stream resolver that stands in for the composite streaming provider.
func newAutoSeedHandler(t *testing.T, relay *autoSeedRelay, resolver *fakeStreamResolver) *PearTubeHandler {
	t.Helper()
	var clockMu sync.Mutex
	now := time.Unix(1_700_000_000, 0)
	return &PearTubeHandler{
		relay:                  relay.client(t),
		streams:                resolver,
		contributeWatchedMedia: true,
		playbackNow: func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			now = now.Add(10 * time.Second)
			return now
		},
	}
}

func moviePlayback() models.PlaybackProgressUpdate {
	return models.PlaybackProgressUpdate{
		MediaType:  "movie",
		ItemID:     "tmdb:movie:603",
		MovieName:  "The Matrix",
		Year:       1999,
		SourcePath: "/debrid/torbox/12345/file/9/The.Matrix.1999.mkv",
		Position:   12,
		Duration:   8160,
	}
}

func episodePlayback() models.PlaybackProgressUpdate {
	return models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        "tmdb:tv:1399:s01e02",
		SeriesID:      "tmdb:tv:1399",
		SeriesName:    "Game of Thrones",
		SeasonNumber:  1,
		EpisodeNumber: 2,
		SourcePath:    "/debrid/realdebrid/98765/file/2/GoT.S01E02.mkv",
	}
}

const matrixCatalogEntity = `{"entityId":"tmdb:movie:603","entityKind":"movie","title":"The Matrix","year":1999,` +
	`"sources":[{"publicationId":"pub-matrix","publisherId":"abcdef0123456789","renditionId":"rend-1","byteLength":4096}]}`

// With no relay, or with the switch off, a playback heartbeat must not produce a
// single call — that is what keeps an install which never asked for p2p, or one
// that asked for the relay but not for automatic seeding, behaving as before.
func TestAutoSeedIsInertWhenDisabled(t *testing.T) {
	relay := &autoSeedRelay{}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"}

	off := &PearTubeHandler{relay: relay.client(t), streams: resolver, contributeWatchedMedia: false}
	if _, ok := off.planAutoSeed(moviePlayback()); ok {
		t.Fatal("autoseed planned a seed with the switch off")
	}
	off.OnPlaybackStarted(moviePlayback())

	unconfigured := &PearTubeHandler{contributeWatchedMedia: true}
	if _, ok := unconfigured.planAutoSeed(moviePlayback()); ok {
		t.Fatal("autoseed planned a seed without a relay")
	}
	unconfigured.OnPlaybackStarted(moviePlayback())

	if relay.catalogReads != 0 || relay.archiveCount() != 0 {
		t.Fatalf("relay was contacted: %d catalog reads, %d archives", relay.catalogReads, relay.archiveCount())
	}
	if resolver.asked != "" {
		t.Fatalf("the stream path was resolved: %q", resolver.asked)
	}
}

// A playback sends a heartbeat every few seconds and a player opens hundreds of
// byte-range requests, none of which reach this handler. Every heartbeat of one
// playback must collapse to a single submission.
func TestAutoSeedSubmitsOncePerTitleAcrossManyHeartbeats(t *testing.T) {
	relay := &autoSeedRelay{}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/FRESH-TOKEN/The.Matrix.1999.mkv"}
	handler := newAutoSeedHandler(t, relay, resolver)

	update := moviePlayback()
	for beat := range 50 {
		update.Position = float64(beat * 10)
		plan, ok := handler.planAutoSeed(update)
		if !ok {
			continue
		}
		if plan.key != "movie:603" {
			t.Fatalf("claim key = %q", plan.key)
		}
		// The player's own URL is never forwarded: the seed names the stream
		// path and the seed path re-resolves it.
		if plan.request.SourceURL != "" || plan.request.StreamPath != update.SourcePath {
			t.Fatalf("seed request = %+v", plan.request)
		}
		plan.submit()
	}

	if got := relay.archiveCount(); got != 0 {
		t.Fatalf("legacy archive submissions = %d, want 0", got)
	}
	if resolver.asked != update.SourcePath {
		t.Fatalf("resolver was asked for %q, want %q", resolver.asked, update.SourcePath)
	}
}

func TestAutoSeedPublishesAnEpisodeUnderItsSeriesCoordinates(t *testing.T) {
	relay := &autoSeedRelay{}
	handler := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/GoT.S01E02.mkv"})

	plan, ok := handler.planAutoSeed(episodePlayback())
	if !ok {
		t.Fatal("an episode playback was not seedable")
	}
	if plan.key != "show:1399:s1:e2" {
		t.Fatalf("claim key = %q", plan.key)
	}
	plan.submit()

	if got := relay.archiveCount(); got != 0 {
		t.Fatalf("remote episode entered legacy archive: %d calls", got)
	}
}

// The relay already serves this title, so asking it to fetch the whole file
// again is pure waste.
func TestAutoSeedSkipsATitleTheSwarmAlreadyServes(t *testing.T) {
	relay := &autoSeedRelay{catalog: matrixCatalogEntity}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"}
	handler := newAutoSeedHandler(t, relay, resolver)

	plan, ok := handler.planAutoSeed(moviePlayback())
	if !ok {
		t.Fatal("the plan was refused before the catalog was consulted")
	}
	plan.submit()

	if got := relay.archiveCount(); got != 0 {
		t.Fatalf("archive submissions = %d, want 0", got)
	}
	if relay.catalogReads != 1 {
		t.Fatalf("catalog reads = %d, want 1", relay.catalogReads)
	}
	// Nothing was re-resolved either: a debrid resolve can wake a torrent, and
	// there is no reason to pay for that when the swarm already has the title.
	if resolver.asked != "" {
		t.Fatalf("the stream path was resolved anyway: %q", resolver.asked)
	}
}

// Two viewers starting the same title at the same moment must produce one seed.
// Only the in-process claim can enforce that: both would read a catalog that
// does not list the title yet.
func TestAutoSeedFiresOnceForConcurrentPlays(t *testing.T) {
	relay := &autoSeedRelay{}
	handler := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"})

	const plays = 8
	var (
		wait   sync.WaitGroup
		start  = make(chan struct{})
		claims = make(chan autoSeedPlan, plays)
	)
	for range plays {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			if plan, ok := handler.planAutoSeed(moviePlayback()); ok {
				claims <- plan
			}
		}()
	}
	close(start)
	wait.Wait()
	close(claims)

	claimed := 0
	for plan := range claims {
		claimed++
		plan.submit()
	}
	if claimed != 1 {
		t.Fatalf("%d of %d concurrent plays claimed the title, want 1", claimed, plays)
	}
	if got := relay.archiveCount(); got != 0 {
		t.Fatalf("remote concurrent plays entered legacy archive: %d calls", got)
	}
}

// A relay that will not enumerate cannot say whether the swarm already has the
// title. Seeding anyway would mean re-fetching whole files on every catalog
// hiccup, so the attempt is abandoned — but the claim is released, because
// nothing was submitted and a later playback should ask again.
func TestAutoSeedDoesNotSeedWhenTheCatalogIsUnavailable(t *testing.T) {
	relay := &autoSeedRelay{
		catalogStatus: http.StatusForbidden,
		catalogBody:   `{"error":{"code":"OPEN_ACCESS_NOT_ENABLED","message":"not open","field":null}}`,
	}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"}
	handler := newAutoSeedHandler(t, relay, resolver)

	plan, ok := handler.planAutoSeed(moviePlayback())
	if !ok {
		t.Fatal("the plan was refused before the catalog was consulted")
	}
	plan.submit()

	if got := relay.archiveCount(); got != 0 {
		t.Fatalf("archive submissions = %d, want 0", got)
	}
	if resolver.asked != "" {
		t.Fatalf("the stream path was resolved anyway: %q", resolver.asked)
	}
	if _, ok := handler.planAutoSeed(moviePlayback()); !ok {
		t.Fatal("the claim was held after nothing was submitted")
	}
}

// A source the relay refuses keeps its claim: it would be refused again on the
// next heartbeat, and one log line per guard window is the right amount of noise.
func TestAutoSeedKeepsItsClaimAfterARefusedSeed(t *testing.T) {
	relay := &autoSeedRelay{archiveStatus: http.StatusInternalServerError}
	handler := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"})

	plan, ok := handler.planAutoSeed(moviePlayback())
	if !ok {
		t.Fatal("the movie was not seedable")
	}
	plan.submit()

	if got := relay.archiveCount(); got != 0 {
		t.Fatalf("remote source entered legacy archive: %d calls", got)
	}
	if _, ok := handler.planAutoSeed(moviePlayback()); ok {
		t.Fatal("a refused seed was retried on the next heartbeat")
	}
}

// Playback with no source to re-resolve, no title, no usable coordinates, or no
// identity at all cannot be published and must not be attempted. A playback
// missing only the TMDB number is not in this list: that is the ordinary app
// case, and it is recovered rather than dropped.
func TestAutoSeedSkipsPlaybackItCannotPublish(t *testing.T) {
	relay := &autoSeedRelay{}
	handler := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/x.mkv"})

	withoutSource := moviePlayback()
	withoutSource.SourcePath = ""

	unidentified := moviePlayback()
	unidentified.ItemID = ""

	untitled := moviePlayback()
	untitled.MovieName = ""

	partialEpisode := episodePlayback()
	partialEpisode.SeasonNumber = 0

	live := models.PlaybackProgressUpdate{
		MediaType:  "live",
		ItemID:     "live:channel-4",
		SourcePath: "/live/channel-4/stream.ts",
	}

	for name, update := range map[string]models.PlaybackProgressUpdate{
		"no source path":            withoutSource,
		"nothing to identify it by": unidentified,
		"no title":                  untitled,
		"no season number":          partialEpisode,
		"live tv":                   live,
		"no coordinates":            {MediaType: "movie", SourcePath: "/debrid/x/1/file/1"},
		"unknown media type":        {MediaType: "photo", ItemID: "tmdb:movie:603", MovieName: "x", SourcePath: "/debrid/x/1/file/1"},
	} {
		if _, ok := handler.planAutoSeed(update); ok {
			t.Fatalf("%s: planned a seed", name)
		}
	}
	if relay.catalogReads != 0 || relay.archiveCount() != 0 {
		t.Fatalf("relay was contacted: %d catalog reads, %d archives", relay.catalogReads, relay.archiveCount())
	}
}

// An episode whose player reported only external ids still publishes: the `tmdb`
// entry of an episode update is its series id, which is the coordinate the swarm
// keys an episode by.
func TestAutoSeedTakesTheTMDBIDFromExternalIDs(t *testing.T) {
	relay := &autoSeedRelay{}
	handler := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/GoT.S01E02.mkv"})

	update := episodePlayback()
	update.ItemID = "tvdb:series:121361:s01e02"
	update.SeriesID = "tvdb:series:121361"
	update.ExternalIDs = map[string]string{"TMDB": "1399", "tvdb": "121361"}

	plan, ok := handler.planAutoSeed(update)
	if !ok {
		t.Fatal("an episode identified by external ids was not seedable")
	}
	if plan.key != "show:1399:s1:e2" {
		t.Fatalf("claim key = %q", plan.key)
	}
}

// autoSeedHistoryService answers the one call the progress endpoint makes. The
// embedded nil interface satisfies the rest: a test that reaches them is asking
// the wrong question and will say so loudly.
type autoSeedHistoryService struct{ historyService }

func (autoSeedHistoryService) UpdatePlaybackProgress(string, models.PlaybackProgressUpdate) (models.PlaybackProgress, error) {
	return models.PlaybackProgress{ID: "movie:tmdb:movie:603", MediaType: "movie", PercentWatched: 2}, nil
}

// The viewer is the point. A relay that is slow and then fails must not delay,
// alter, or break the heartbeat the player is waiting on.
func TestAutoSeedFailureDoesNotAffectThePlaybackResponse(t *testing.T) {
	relay := &autoSeedRelay{
		archiveStatus: http.StatusInternalServerError,
		archiveDelay:  2 * time.Second,
	}
	seeder := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"})
	handler := &HistoryHandler{Service: autoSeedHistoryService{}, AutoSeeder: seeder}

	body, err := json.Marshal(moviePlayback())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/users/user1/history/progress", strings.NewReader(string(body)))
	req = mux.SetURLVars(req, map[string]string{"userID": "user1", "mediaType": "movie", "id": "tmdb:movie:603"})
	rec := httptest.NewRecorder()

	started := time.Now()
	handler.UpdatePlaybackProgress(rec, req)
	elapsed := time.Since(started)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var progress models.PlaybackProgress
	if err := json.Unmarshal(rec.Body.Bytes(), &progress); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if progress.PercentWatched != 2 {
		t.Fatalf("progress = %+v", progress)
	}
	if elapsed >= relay.archiveDelay {
		t.Fatalf("the heartbeat waited %v on the relay", elapsed)
	}
}

// fakeTMDBResolver stands in for the metadata service's id recovery, and records
// what it was asked so the caller can be shown to hand over the ids it holds.
type fakeTMDBResolver struct {
	tmdbID int64

	mu    sync.Mutex
	calls int
	kind  string
	ids   map[string]string
	title string
	year  int
}

func (f *fakeTMDBResolver) TMDBIDForExternalIDs(
	_ context.Context, contentKind string, externalIDs map[string]string, title string, year int,
) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.kind, f.ids, f.title, f.year = contentKind, externalIDs, title, year
	return f.tmdbID
}

func (f *fakeTMDBResolver) observed() (int, string, map[string]string, string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.kind, f.ids, f.title, f.year
}

// appPlayback is the progress heartbeat an app actually sends, captured off the
// wire during a real playback on a live install. It is the case that matters:
// the title is named by TVDB and IMDb, sourcePath is present, and there is no
// TMDB number anywhere in the payload.
func appPlayback() models.PlaybackProgressUpdate {
	return models.PlaybackProgressUpdate{
		MediaType:  "movie",
		ItemID:     "tvdb:movie:343856",
		MovieName:  "Supergirl",
		Year:       2026,
		SourcePath: "/debrid/torbox/65419880/file/2/Supergirl.2026.MULTi.1080p.AMZN.WEB-DL.x264.AC3-KiT.mkv",
		ExternalIDs: map[string]string{
			"imdb":    "tt8814476",
			"tvdb":    "343856",
			"titleId": "tvdb:movie:343856",
		},
		Position: 3410,
		Duration: 6492,
	}
}

// The case every app client is in: a playback that names its title by TVDB and
// IMDb and sends no TMDB id. It must still seed, published under the id the
// server recovers, because the swarm keys entities by TMDB number and no app
// release is going to change what the installed clients send.
func TestAutoSeedRecoversTheTMDBIDAnAppDoesNotSend(t *testing.T) {
	relay := &autoSeedRelay{}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/Supergirl.2026.mkv"}
	handler := newAutoSeedHandler(t, relay, resolver)
	tmdb := &fakeTMDBResolver{tmdbID: 402431}
	handler.tmdb = tmdb

	plan, ok := handler.planAutoSeed(appPlayback())
	if !ok {
		t.Fatal("a playback identified by tvdb and imdb was not seedable")
	}
	// The claim is taken before the id is known, on the identity the playback
	// does carry, so a heartbeat storm still costs one submission.
	if plan.pendingKey != "pending:movie:tt8814476" {
		t.Fatalf("pending claim = %q", plan.pendingKey)
	}
	// Nothing may be looked up on the player's request path.
	if calls, _, _, _, _ := tmdb.observed(); calls != 0 {
		t.Fatalf("the id was resolved on the request path (%d calls)", calls)
	}

	plan.submit()

	calls, kind, ids, title, year := tmdb.observed()
	if calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", calls)
	}
	if kind != "movie" || title != "Supergirl" || year != 2026 || ids["imdb"] != "tt8814476" {
		t.Fatalf("resolver asked with kind=%q title=%q year=%d ids=%#v", kind, title, year, ids)
	}
	if got := relay.archiveCount(); got != 0 {
		t.Fatalf("identified remote playback entered legacy archive: %d calls", got)
	}
	if resolver.asked != appPlayback().SourcePath {
		t.Fatalf("re-resolved %q", resolver.asked)
	}
}

// Once the id is recovered, the title is claimed under the swarm's own key too,
// so a second client that does name it by TMDB id cannot seed it again.
func TestAutoSeedClaimsTheRecoveredEntityKey(t *testing.T) {
	relay := &autoSeedRelay{}
	handler := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/x.mkv"})
	handler.tmdb = &fakeTMDBResolver{tmdbID: 603}

	plan, ok := handler.planAutoSeed(appPlayback())
	if !ok {
		t.Fatal("the app playback was not seedable")
	}
	plan.submit()
	if plan.key != "pending:movie:tt8814476" {
		t.Fatalf("the caller's copy of the plan was mutated: key = %q", plan.key)
	}

	// The same title arriving from a TMDB-native player is already accounted for.
	tmdbNative := moviePlayback()
	tmdbNative.ItemID = "tmdb:movie:603"
	if _, ok := handler.planAutoSeed(tmdbNative); ok {
		t.Fatal("the recovered title was seeded a second time under its tmdb id")
	}
	if got := relay.archiveCount(); got != 0 {
		t.Fatalf("recovered remote playback entered legacy archive: %d calls", got)
	}
}

// A title nothing can identify must not reach the relay, and must not be retried
// on every heartbeat for the rest of the guard window.
func TestAutoSeedAbandonsAPlaybackItCannotIdentify(t *testing.T) {
	relay := &autoSeedRelay{}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/x.mkv"}
	handler := newAutoSeedHandler(t, relay, resolver)
	tmdb := &fakeTMDBResolver{tmdbID: 0}
	handler.tmdb = tmdb

	plan, ok := handler.planAutoSeed(appPlayback())
	if !ok {
		t.Fatal("the playback was dropped before anything was attempted")
	}
	plan.submit()

	if relay.catalogReads != 0 || relay.archiveCount() != 0 {
		t.Fatalf("relay was contacted: %d catalog reads, %d archives", relay.catalogReads, relay.archiveCount())
	}
	if resolver.asked != "" {
		t.Fatalf("the stream path was resolved for a title with no coordinates: %q", resolver.asked)
	}
	if _, ok := handler.planAutoSeed(appPlayback()); ok {
		t.Fatal("an unidentifiable title was retried on the next heartbeat")
	}
	if calls, _, _, _, _ := tmdb.observed(); calls != 1 {
		t.Fatalf("resolver calls = %d, want 1 — the lookup was repeated", calls)
	}
}

// Without a resolver the handler must stay inert on an app playback rather than
// submit coordinates the relay will reject.
func TestAutoSeedWithoutAResolverDoesNotSubmitAnUnidentifiedTitle(t *testing.T) {
	relay := &autoSeedRelay{}
	handler := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/x.mkv"})

	handler.OnPlaybackStarted(appPlayback())
	waitForArchives(t, relay, 0)

	if relay.catalogReads != 0 || relay.archiveCount() != 0 {
		t.Fatalf("relay was contacted: %d catalog reads, %d archives", relay.catalogReads, relay.archiveCount())
	}
}

// A player that does send a TMDB id is answered without any lookup at all, so
// the recovery cannot become a cost on the path that already worked.
func TestAutoSeedDoesNotLookUpATitleThatNamesItsTMDBID(t *testing.T) {
	relay := &autoSeedRelay{}
	handler := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"})
	tmdb := &fakeTMDBResolver{tmdbID: 999999}
	handler.tmdb = tmdb

	plan, ok := handler.planAutoSeed(moviePlayback())
	if !ok {
		t.Fatal("a tmdb-identified playback was not seedable")
	}
	if plan.pendingKey != "" || plan.key != "movie:603" {
		t.Fatalf("claim key = %q, pending = %q", plan.key, plan.pendingKey)
	}
	plan.submit()

	if calls, _, _, _, _ := tmdb.observed(); calls != 0 {
		t.Fatalf("resolver calls = %d, want 0", calls)
	}
	if got := relay.archiveCount(); got != 0 {
		t.Fatalf("tmdb-native remote playback entered legacy archive: %d calls", got)
	}
}
